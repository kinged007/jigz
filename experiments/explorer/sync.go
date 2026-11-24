package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type syncOptions struct {
	Owner, Name string
	PageSize    int  // items per GraphQL page
	Reserve     int  // stop (or wait) when the remaining GraphQL points drop below this
	MaxPages    int  // 0 = no limit; a stopped sync resumes from its saved cursor
	Wait        bool // sleep until the rate limit resets instead of stopping
}

// RepoMeta is the repository summary stored under meta key "repo".
type RepoMeta struct {
	NameWithOwner string `json:"nameWithOwner"`
	Description   string `json:"description"`
	URL           string `json:"url"`
	Stars         int    `json:"stars"`
	Forks         int    `json:"forks"`
	DefaultBranch string `json:"defaultBranch"`
	Language      string `json:"language"`
	CreatedAt     string `json:"createdAt"`
	PushedAt      string `json:"pushedAt"`
}

// runSync fetches issues and pull requests newest-updated first and stops at
// the first item not updated since the previous sync, so a repeat sync costs
// one request per kind when nothing changed. Unchanged items (closed ones
// included) are never fetched again. Progress is saved after every page.
func runSync(ctx context.Context, st *Store, gh *GitHub, o syncOptions) error {
	if err := syncRepo(ctx, st, gh, o); err != nil {
		return err
	}
	for _, kind := range []string{"issue", "pr"} {
		if err := syncKind(ctx, st, gh, o, kind); err != nil {
			return err
		}
	}
	if err := syncTimelines(ctx, st, gh, o); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "sync done: %d requests, %d points spent, %d remaining\n", gh.Requests, gh.Spent, gh.Remaining)
	return st.setMeta("synced_at", time.Now().UTC().Format(time.RFC3339))
}

func syncRepo(ctx context.Context, st *Store, gh *GitHub, o syncOptions) error {
	var out struct {
		Repository struct {
			NameWithOwner    string                 `json:"nameWithOwner"`
			Description      string                 `json:"description"`
			URL              string                 `json:"url"`
			StargazerCount   int                    `json:"stargazerCount"`
			ForkCount        int                    `json:"forkCount"`
			CreatedAt        string                 `json:"createdAt"`
			PushedAt         string                 `json:"pushedAt"`
			DefaultBranchRef *struct{ Name string } `json:"defaultBranchRef"`
			PrimaryLanguage  *struct{ Name string } `json:"primaryLanguage"`
		} `json:"repository"`
	}
	if err := gh.query(ctx, repoQuery, map[string]any{"owner": o.Owner, "name": o.Name}, &out); err != nil {
		return fmt.Errorf("repo: %w", err)
	}
	r := out.Repository
	m := RepoMeta{NameWithOwner: r.NameWithOwner, Description: r.Description, URL: r.URL, Stars: r.StargazerCount,
		Forks: r.ForkCount, CreatedAt: r.CreatedAt, PushedAt: r.PushedAt}
	if r.DefaultBranchRef != nil {
		m.DefaultBranch = r.DefaultBranchRef.Name
	}
	if r.PrimaryLanguage != nil {
		m.Language = r.PrimaryLanguage.Name
	}
	b, _ := json.Marshal(m)
	return st.setMeta("repo", string(b))
}

// budget blocks or fails when the GraphQL budget is below the reserve.
func budget(ctx context.Context, gh *GitHub, o syncOptions) error {
	if gh.Remaining < 0 || gh.Remaining >= o.Reserve {
		return nil
	}
	if !o.Wait {
		return fmt.Errorf("%w (%d points left, resets %s); run sync again later to resume",
			errRateLimited, gh.Remaining, gh.ResetAt.Local().Format(time.Kitchen))
	}
	d := time.Until(gh.ResetAt) + 5*time.Second
	fmt.Fprintf(os.Stderr, "  rate limit low (%d), waiting %s\n", gh.Remaining, d.Round(time.Second))
	sleepCtx(ctx, d)
	gh.Remaining = -1
	return ctx.Err()
}

func syncKind(ctx context.Context, st *Store, gh *GitHub, o syncOptions, kind string) error {
	wmKey, curKey, pendKey := kind+".watermark", kind+".cursor", kind+".pending_watermark"
	watermark, _ := st.getMeta(wmKey)
	cursor, _ := st.getMeta(curKey)
	pending, _ := st.getMeta(pendKey)
	q := itemsQuery(kind)
	pageSize := o.PageSize
	var fetched, skipped int
	for page := 0; ; page++ {
		if o.MaxPages > 0 && page >= o.MaxPages {
			fmt.Fprintf(os.Stderr, "%s: page limit reached, %d stored; next sync resumes\n", kind, fetched)
			return nil
		}
		if err := budget(ctx, gh, o); err != nil {
			return err
		}
		vars := map[string]any{"owner": o.Owner, "name": o.Name, "first": pageSize}
		if cursor != "" {
			vars["after"] = cursor
		}
		var out struct {
			Repository map[string]struct {
				PageInfo pageInfo  `json:"pageInfo"`
				Nodes    []rawItem `json:"nodes"`
			} `json:"repository"`
		}
		if err := gh.query(ctx, q, vars, &out); err != nil {
			// Large pages can hit GitHub's server timeout; retry smaller.
			if pageSize > 10 && errors.Is(err, errServer) {
				pageSize /= 2
				fmt.Fprintf(os.Stderr, "  %v; retrying with page size %d\n", err, pageSize)
				page--
				continue
			}
			return fmt.Errorf("%s page: %w", kind, err)
		}
		conn := out.Repository["issues"]
		if kind == "pr" {
			conn = out.Repository["pullRequests"]
		}
		reachedOld := false
		now := time.Now().UTC().Format(time.RFC3339)
		for i := range conn.Nodes {
			r := &conn.Nodes[i]
			if r.UpdatedAt > pending {
				pending = r.UpdatedAt
			}
			if watermark != "" && r.UpdatedAt <= watermark {
				reachedOld = true
				break
			}
			stored, complete, ok, err := st.storedUpdatedAt(kind, r.Number)
			if err != nil {
				return err
			}
			if ok && complete && stored == r.UpdatedAt {
				skipped++
				continue
			}
			if err := st.putItem(normalizeItem(kind, r), now); err != nil {
				return err
			}
			fetched++
		}
		cursor = conn.PageInfo.EndCursor
		if err := st.setMeta(curKey, cursor); err != nil {
			return err
		}
		if err := st.setMeta(pendKey, pending); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: page %d, %d stored, %d unchanged, %d points left\n", kind, page+1, fetched, skipped, gh.Remaining)
		if reachedOld || !conn.PageInfo.HasNextPage {
			break
		}
	}
	if err := st.setMeta(wmKey, pending); err != nil {
		return err
	}
	if err := st.setMeta(curKey, ""); err != nil {
		return err
	}
	return st.setMeta(pendKey, "")
}

// syncTimelines fetches the remaining timeline pages of items with more than
// 100 timeline entries.
func syncTimelines(ctx context.Context, st *Store, gh *GitHub, o syncOptions) error {
	items, err := st.items("timeline_complete = 0")
	if err != nil {
		return err
	}
	for _, it := range items {
		q := timelineQuery(it.Kind)
		for it.TimelineCursor != "" {
			if err := budget(ctx, gh, o); err != nil {
				return err
			}
			var out struct {
				Node struct {
					Timeline timelineConn `json:"timelineItems"`
				} `json:"node"`
			}
			if err := gh.query(ctx, q, map[string]any{"id": it.ID, "after": it.TimelineCursor}, &out); err != nil {
				return fmt.Errorf("timeline %s #%d: %w", it.Kind, it.Number, err)
			}
			it.Events = appendEvents(it.Events, out.Node.Timeline.Nodes)
			it.TimelineCursor = ""
			if out.Node.Timeline.PageInfo.HasNextPage {
				it.TimelineCursor = out.Node.Timeline.PageInfo.EndCursor
			}
			if err := st.putItem(it, time.Now().UTC().Format(time.RFC3339)); err != nil {
				return err
			}
		}
	}
	if len(items) > 0 {
		fmt.Fprintf(os.Stderr, "timelines: completed %d long timelines\n", len(items))
	}
	return nil
}

func splitRepo(s string) (owner, name string, err error) {
	s = strings.TrimSuffix(strings.TrimPrefix(s, "https://github.com/"), ".git")
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("repo must be owner/name, got %q", s)
	}
	return owner, name, nil
}
