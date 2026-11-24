package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GitHub is a small GraphQL client that tracks the primary rate limit and
// backs off on secondary limits.
type GitHub struct {
	token     string
	http      *http.Client
	Remaining int
	ResetAt   time.Time
	Spent     int
	Requests  int
}

func githubToken() (string, error) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", errors.New("no GitHub token: set GH_TOKEN or log in with gh")
	}
	return strings.TrimSpace(string(out)), nil
}

func newGitHub() (*GitHub, error) {
	tok, err := githubToken()
	if err != nil {
		return nil, err
	}
	return &GitHub{token: tok, http: &http.Client{Timeout: 60 * time.Second}, Remaining: -1}, nil
}

var (
	errRateLimited = errors.New("rate limit reserve reached")
	errServer      = errors.New("graphql failed after retries")
)

type rateLimit struct {
	Cost      int    `json:"cost"`
	Remaining int    `json:"remaining"`
	ResetAt   string `json:"resetAt"`
}

// query runs a GraphQL query. The query must select rateLimit { cost remaining resetAt }.
func (g *GitHub) query(ctx context.Context, q string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": q, "variables": vars})
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", bytes.NewReader(body))
		req.Header.Set("Authorization", "bearer "+g.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := g.http.Do(req)
		if err != nil {
			lastErr = err
			sleepCtx(ctx, backoff(attempt))
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		g.Requests++
		switch {
		case resp.StatusCode == 403 || resp.StatusCode == 429:
			// Secondary rate limit: honour Retry-After, else wait a minute.
			wait := time.Minute
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
				wait = time.Duration(s) * time.Second
			}
			fmt.Fprintf(os.Stderr, "  secondary rate limit, waiting %s\n", wait)
			lastErr = fmt.Errorf("http %d", resp.StatusCode)
			sleepCtx(ctx, wait)
			continue
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("http %d", resp.StatusCode)
			sleepCtx(ctx, backoff(attempt))
			continue
		case resp.StatusCode != 200:
			return fmt.Errorf("graphql http %d: %s", resp.StatusCode, truncate(string(data), 300))
		}
		var env struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			return fmt.Errorf("decode graphql: %w", err)
		}
		if len(env.Errors) > 0 {
			if env.Errors[0].Type == "RATE_LIMITED" {
				return errRateLimited
			}
			if strings.Contains(strings.ToLower(env.Errors[0].Message), "timeout") ||
				strings.Contains(env.Errors[0].Message, "Something went wrong") {
				return fmt.Errorf("%w: %s", errServer, env.Errors[0].Message)
			}
			return fmt.Errorf("graphql: %s", env.Errors[0].Message)
		}
		var rl struct {
			RateLimit rateLimit `json:"rateLimit"`
		}
		if json.Unmarshal(env.Data, &rl) == nil && rl.RateLimit.ResetAt != "" {
			g.Remaining = rl.RateLimit.Remaining
			g.Spent += rl.RateLimit.Cost
			g.ResetAt, _ = time.Parse(time.RFC3339, rl.RateLimit.ResetAt)
		}
		return json.Unmarshal(env.Data, out)
	}
	return fmt.Errorf("%w: %v", errServer, lastErr)
}

func backoff(attempt int) time.Duration { return time.Duration(1<<attempt) * 2 * time.Second }

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

const issueTimelineTypes = `[LABELED_EVENT, UNLABELED_EVENT, CLOSED_EVENT, REOPENED_EVENT, CROSS_REFERENCED_EVENT, ISSUE_COMMENT, ASSIGNED_EVENT, CONNECTED_EVENT, MARKED_AS_DUPLICATE_EVENT]`
const prTimelineTypes = `[LABELED_EVENT, UNLABELED_EVENT, CLOSED_EVENT, REOPENED_EVENT, CROSS_REFERENCED_EVENT, ISSUE_COMMENT, ASSIGNED_EVENT, CONNECTED_EVENT, MERGED_EVENT, PULL_REQUEST_REVIEW]`

const timelineNodeFields = `
__typename
... on LabeledEvent { createdAt actor { login } label { name } }
... on UnlabeledEvent { createdAt actor { login } label { name } }
... on ClosedEvent { createdAt actor { login } closer { __typename ... on PullRequest { number } ... on Commit { oid } } }
... on ReopenedEvent { createdAt actor { login } }
... on CrossReferencedEvent { createdAt actor { login } willCloseTarget isCrossRepository source { __typename ... on Issue { number } ... on PullRequest { number } } }
... on IssueComment { createdAt author { login } }
... on AssignedEvent { createdAt actor { login } assignee { __typename ... on User { login } ... on Bot { login } } }
... on ConnectedEvent { createdAt actor { login } subject { __typename ... on Issue { number } ... on PullRequest { number } } }
`

const issueTimelineFields = timelineNodeFields + `
... on MarkedAsDuplicateEvent { createdAt actor { login } canonical { __typename ... on Issue { number } ... on PullRequest { number } } }`

const prTimelineFields = timelineNodeFields + `
... on MergedEvent { createdAt actor { login } commit { oid } }
... on PullRequestReview { createdAt author { login } state }`

const commonItemFields = `
id number title body state createdAt updatedAt closedAt
author { login }
labels(first: 30) { nodes { name } }
assignees(first: 10) { nodes { login } }
milestone { title }
comments { totalCount }`

func itemsQuery(kind string) string {
	conn, types, fields, extra := "issues", issueTimelineTypes, issueTimelineFields, "stateReason"
	if kind == "pr" {
		conn, types, fields = "pullRequests", prTimelineTypes, prTimelineFields
		extra = `mergedAt headRefName baseRefName additions deletions mergeCommit { oid }
closingIssuesReferences(first: 10) { nodes { number } }`
	}
	return fmt.Sprintf(`query($owner: String!, $name: String!, $first: Int!, $after: String) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    %s(first: $first, after: $after, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes { %s %s
        timelineItems(first: 100, itemTypes: %s) { pageInfo { hasNextPage endCursor } nodes { %s } }
      }
    }
  }
}`, conn, commonItemFields, extra, types, fields)
}

func timelineQuery(kind string) string {
	typ, types, fields := "Issue", issueTimelineTypes, issueTimelineFields
	if kind == "pr" {
		typ, types, fields = "PullRequest", prTimelineTypes, prTimelineFields
	}
	return fmt.Sprintf(`query($id: ID!, $after: String) {
  rateLimit { cost remaining resetAt }
  node(id: $id) { ... on %s {
    timelineItems(first: 100, after: $after, itemTypes: %s) { pageInfo { hasNextPage endCursor } nodes { %s } }
  } }
}`, typ, types, fields)
}

const repoQuery = `query($owner: String!, $name: String!) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    nameWithOwner description url stargazerCount forkCount createdAt pushedAt
    defaultBranchRef { name }
    primaryLanguage { name }
  }
}`

type login struct {
	Login string `json:"login"`
}

type numberRef struct {
	Typename string `json:"__typename"`
	Number   int    `json:"number"`
	Oid      string `json:"oid"`
	Login    string `json:"login"`
}

type rawEvent struct {
	Typename          string                 `json:"__typename"`
	CreatedAt         string                 `json:"createdAt"`
	Actor             *login                 `json:"actor"`
	Author            *login                 `json:"author"`
	Label             *struct{ Name string } `json:"label"`
	Closer            *numberRef             `json:"closer"`
	WillCloseTarget   bool                   `json:"willCloseTarget"`
	IsCrossRepository bool                   `json:"isCrossRepository"`
	Source            *numberRef             `json:"source"`
	Assignee          *numberRef             `json:"assignee"`
	Subject           *numberRef             `json:"subject"`
	Canonical         *numberRef             `json:"canonical"`
	Commit            *numberRef             `json:"commit"`
	State             string                 `json:"state"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type timelineConn struct {
	PageInfo pageInfo   `json:"pageInfo"`
	Nodes    []rawEvent `json:"nodes"`
}

type rawItem struct {
	ID          string `json:"id"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	State       string `json:"state"`
	StateReason string `json:"stateReason"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	ClosedAt    string `json:"closedAt"`
	MergedAt    string `json:"mergedAt"`
	Author      *login `json:"author"`
	Labels      struct {
		Nodes []struct{ Name string } `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []login `json:"nodes"`
	} `json:"assignees"`
	Milestone *struct{ Title string } `json:"milestone"`
	Comments  struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	HeadRefName string     `json:"headRefName"`
	BaseRefName string     `json:"baseRefName"`
	Additions   int        `json:"additions"`
	Deletions   int        `json:"deletions"`
	MergeCommit *numberRef `json:"mergeCommit"`
	Closing     *struct {
		Nodes []numberRef `json:"nodes"`
	} `json:"closingIssuesReferences"`
	Timeline timelineConn `json:"timelineItems"`
}

const maxBody = 4000

func normalizeItem(kind string, r *rawItem) *Item {
	it := &Item{
		ID: r.ID, Number: r.Number, Kind: kind, Title: r.Title, Body: truncate(r.Body, maxBody),
		State: r.State, StateReason: r.StateReason, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		ClosedAt: r.ClosedAt, MergedAt: r.MergedAt, Comments: r.Comments.TotalCount,
		HeadRef: r.HeadRefName, BaseRef: r.BaseRefName, Additions: r.Additions, Deletions: r.Deletions,
	}
	if r.Author != nil {
		it.Author = r.Author.Login
	} else {
		it.Author = "ghost"
	}
	for _, l := range r.Labels.Nodes {
		it.Labels = append(it.Labels, l.Name)
	}
	for _, a := range r.Assignees.Nodes {
		it.Assignees = append(it.Assignees, a.Login)
	}
	if r.Milestone != nil {
		it.Milestone = r.Milestone.Title
	}
	if r.MergeCommit != nil {
		it.MergeCommit = r.MergeCommit.Oid
	}
	if r.Closing != nil {
		for _, n := range r.Closing.Nodes {
			it.Closes = append(it.Closes, n.Number)
		}
	}
	it.Events = appendEvents(nil, r.Timeline.Nodes)
	if r.Timeline.PageInfo.HasNextPage {
		it.TimelineCursor = r.Timeline.PageInfo.EndCursor
	}
	return it
}

func appendEvents(out []Event, nodes []rawEvent) []Event {
	for _, n := range nodes {
		e := Event{At: n.CreatedAt}
		if n.Actor != nil {
			e.Actor = n.Actor.Login
		} else if n.Author != nil {
			e.Actor = n.Author.Login
		}
		switch n.Typename {
		case "LabeledEvent", "UnlabeledEvent":
			e.Type = "labeled"
			if n.Typename == "UnlabeledEvent" {
				e.Type = "unlabeled"
			}
			if n.Label != nil {
				e.Label = n.Label.Name
			}
		case "ClosedEvent":
			e.Type = "closed"
			if n.Closer != nil {
				e.Ref, e.Oid = n.Closer.Number, n.Closer.Oid
			}
		case "ReopenedEvent":
			e.Type = "reopened"
		case "CrossReferencedEvent":
			if n.IsCrossRepository || n.Source == nil {
				continue
			}
			e.Type, e.Ref, e.Close = "xref", n.Source.Number, n.WillCloseTarget
		case "IssueComment":
			e.Type = "comment"
		case "AssignedEvent":
			e.Type = "assigned"
			if n.Assignee != nil {
				e.Label = n.Assignee.Login
			}
		case "ConnectedEvent":
			if n.Subject == nil {
				continue
			}
			e.Type, e.Ref = "connected", n.Subject.Number
		case "MarkedAsDuplicateEvent":
			if n.Canonical == nil {
				continue
			}
			e.Type, e.Ref = "duplicate", n.Canonical.Number
		case "MergedEvent":
			e.Type = "merged"
			if n.Commit != nil {
				e.Oid = n.Commit.Oid
			}
		case "PullRequestReview":
			e.Type, e.State = "review", n.State
		default:
			continue
		}
		out = append(out, e)
	}
	return out
}
