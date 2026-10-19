package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	config "github.com/kinged007/sortie-loop/internal/fleetconfig"
	"github.com/kinged007/sortie-loop/internal/github"
	"github.com/kinged007/sortie-loop/internal/store"
)

// DefaultCallBudget is the most GitHub requests one project gets per pass.
const DefaultCallBudget = 40

// retryAfter is how long an issue whose timeline showed no merged pull
// request, or an item GitHub would not return, waits before it is read
// again.
const retryAfter = time.Hour

// Target is a project the enrichment may call GitHub for, with the token
// of that project. The token is only ever sent for its own project.
type Target struct {
	Project  string
	Token    string
	TokenEnv string
}

// EnrichStatus is the outcome of the last pass over one project.
type EnrichStatus struct {
	At    time.Time
	OK    bool
	Err   string
	Calls int
}

// Text is the status as the pages show it.
func (s EnrichStatus) Text() string {
	switch {
	case s.At.IsZero():
		return "no pass yet"
	case s.OK:
		return "last pass ok"
	}
	return "error: " + s.Err
}

// Enricher fills github_cache for the runs central has stored: branch,
// pull request and its opened, merged and closed times. It only reads from
// GitHub. Fill the exported fields before calling Run or Once.
type Enricher struct {
	Store *store.Store
	// Settings returns the current github section, so it can change
	// without a restart.
	Settings func() config.GitHub
	Targets  func() []Target
	// Budget is the most requests per project per pass, DefaultCallBudget
	// when zero.
	Budget int
	Now    func() time.Time

	client github.Client
	mu     sync.Mutex
	status map[string]EnrichStatus
	last   time.Time
}

// Status returns the outcome of the last pass over a project.
func (e *Enricher) Status(project string) EnrichStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status[project]
}

func (e *Enricher) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Run checks every few seconds whether a pass is due, until ctx is
// cancelled.
func (e *Enricher) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		e.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once runs a pass when enrichment is on and the interval has passed.
func (e *Enricher) Once(ctx context.Context) {
	g := e.Settings()
	e.mu.Lock()
	due := g.Enrich && (e.last.IsZero() || e.now().Sub(e.last) >= g.PassInterval())
	e.mu.Unlock()
	if due {
		e.Pass(ctx)
	}
}

// Pass runs one pass over every target, if enrichment is on.
func (e *Enricher) Pass(ctx context.Context) {
	g := e.Settings()
	if !g.Enrich {
		return
	}
	e.mu.Lock()
	e.last = e.now()
	e.mu.Unlock()
	e.client.BaseURL, e.client.Now = g.BaseURL(), e.Now
	for _, t := range e.Targets() {
		if ctx.Err() != nil {
			return
		}
		st := e.project(ctx, t)
		e.mu.Lock()
		if e.status == nil {
			e.status = map[string]EnrichStatus{}
		}
		e.status[t.Project] = st
		e.mu.Unlock()
	}
}

type job struct {
	pr     bool
	number int
	// class orders jobs: 0 never fetched, 1 open pull request refresh,
	// 2 retry of an item that is not settled.
	class  int
	latest string
	etag   string
	// details is set for a pull request whose commits and checks are read.
	details bool
	// prev is the detail cached for the pull request.
	prev store.PRDetails
}

func (j job) kind() string {
	if j.pr {
		return store.GitHubPR
	}
	return store.GitHubIssue
}

func (e *Enricher) project(ctx context.Context, t Target) EnrichStatus {
	now := e.now()
	st := EnrichStatus{At: now, OK: true}
	fail := func(err error) EnrichStatus {
		st.OK, st.Err = false, err.Error()
		return st
	}
	if t.Token == "" {
		if t.TokenEnv != "" {
			return fail(fmt.Errorf("no token: %s is unset or empty", t.TokenEnv))
		}
		return fail(errors.New("no token: set token_env, GITHUB_TOKEN or GH_TOKEN"))
	}
	if until, limited := e.client.LimitedUntil(t.Project); limited {
		return fail(&github.RateLimitError{Until: until})
	}
	details := e.Settings().DetailsOn()
	jobs, err := e.plan(t.Project, now, details)
	if err != nil {
		return fail(err)
	}
	queued := map[string]bool{}
	for _, j := range jobs {
		queued[store.GitHubKey(j.kind(), j.number)] = true
	}
	budget := e.Budget
	if budget <= 0 {
		budget = DefaultCallBudget
	}
	spend := func() bool {
		if st.Calls >= budget {
			return false
		}
		st.Calls++
		return true
	}
	for i := 0; i < len(jobs) && ctx.Err() == nil && spend(); i++ {
		j := jobs[i]
		found, err := e.fetch(ctx, t, j, spend)
		var apiErr *github.Error
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			if err := e.Store.SaveUnavailable(t.Project, j.kind(), j.number, now.UTC().Format(time.RFC3339)); err != nil {
				return fail(err)
			}
			st.OK, st.Err = false, fmt.Sprintf("%s#%d: %v", t.Project, j.number, err)
			continue
		}
		if err != nil {
			return fail(err)
		}
		for _, n := range found {
			if key := store.GitHubKey(store.GitHubPR, n); !queued[key] {
				queued[key] = true
				jobs = append(jobs, job{pr: true, number: n, latest: j.latest, details: details})
			}
		}
	}
	return st
}

// plan lists what to fetch for a project, most urgent first: items never
// fetched, newest run first, then open pull requests, then retries.
func (e *Enricher) plan(project string, now time.Time, details bool) ([]job, error) {
	items, err := e.Store.RunItems(project)
	if err != nil {
		return nil, err
	}
	rows, err := e.Store.GitHubRows(project)
	if err != nil {
		return nil, err
	}
	links, err := e.Store.IssueLinks(project)
	if err != nil {
		return nil, err
	}
	stale := func(r store.GitHubRow) bool {
		t, err := time.Parse(time.RFC3339, r.FetchedAt)
		return err != nil || now.Sub(t) >= retryAfter
	}
	jobs := map[string]job{}
	add := func(pr bool, number int, latest string) {
		j := job{pr: pr, number: number, latest: latest, details: pr && details}
		key := store.GitHubKey(j.kind(), number)
		row, cached := rows[key]
		switch {
		case !cached:
		case pr && row.State == store.PROpen:
			j.class = 1
		case j.details && row.State != store.PRUnavailable && row.Details.At == "":
			j.class = 1
		case pr && row.State == store.PRUnavailable && stale(row):
			j.class = 2
		case !pr && stale(row) && !anyMerged(links[number], rows):
			j.class = 2
		default:
			return
		}
		if cached {
			j.etag, j.prev = row.ETag, row.Details
			if j.details && j.prev.At == "" {
				j.etag = ""
			}
		}
		if old, ok := jobs[key]; ok && (old.class < j.class || old.class == j.class && old.latest >= j.latest) {
			return
		}
		jobs[key] = j
	}
	for _, it := range items {
		add(it.PR, it.Number, it.Latest)
		if !it.PR {
			for _, n := range links[it.Number] {
				add(true, n, it.Latest)
			}
		}
	}
	out := make([]job, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		switch {
		case x.class != y.class:
			return x.class < y.class
		case x.latest != y.latest:
			return x.latest > y.latest
		case x.pr != y.pr:
			return x.pr
		}
		return x.number > y.number
	})
	return out, nil
}

func anyMerged(prs []int, rows map[string]store.GitHubRow) bool {
	for _, n := range prs {
		if rows[store.GitHubKey(store.GitHubPR, n)].State == store.PRMerged {
			return true
		}
	}
	return false
}

type pull struct {
	Number       int     `json:"number"`
	HTMLURL      string  `json:"html_url"`
	State        string  `json:"state"`
	Title        string  `json:"title"`
	CreatedAt    string  `json:"created_at"`
	ClosedAt     *string `json:"closed_at"`
	MergedAt     *string `json:"merged_at"`
	Commits      int     `json:"commits"`
	ChangedFiles int     `json:"changed_files"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	Head         struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

type timelineEvent struct {
	Event  string `json:"event"`
	Source struct {
		Issue struct {
			Number      int              `json:"number"`
			PullRequest *json.RawMessage `json:"pull_request"`
			Repository  struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"issue"`
	} `json:"source"`
}

// fetch reads one job from GitHub and stores the result. For an issue it
// returns the numbers of the pull requests its timeline links to.
func (e *Enricher) fetch(ctx context.Context, t Target, j job, spend func() bool) ([]int, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", t.Project, j.number)
	if !j.pr {
		path = fmt.Sprintf("/repos/%s/issues/%d/timeline?per_page=100", t.Project, j.number)
	}
	resp, err := e.client.Get(ctx, t.Project, t.Token, path, j.etag)
	if err != nil {
		return nil, err
	}
	ts := e.now().UTC().Format(time.RFC3339)
	if resp.NotModified {
		if err := e.Store.TouchGitHub(t.Project, j.kind(), j.number, ts); err != nil || !j.details || j.prev.At == "" {
			return nil, err
		}
		return nil, e.details(ctx, t, j, ts, j.prev, false, spend)
	}
	if j.pr {
		var p pull
		if err := json.Unmarshal(resp.Body, &p); err != nil {
			return nil, fmt.Errorf("pull request %d: %w", j.number, err)
		}
		pr := store.PR{
			Project: t.Project, Number: j.number, Branch: p.Head.Ref, URL: p.HTMLURL, Title: p.Title,
			CreatedAt: p.CreatedAt, FetchedAt: ts, ETag: resp.ETag, State: store.PRClosed,
		}
		if p.ClosedAt != nil {
			pr.ClosedAt = *p.ClosedAt
		}
		switch {
		case p.MergedAt != nil && *p.MergedAt != "":
			pr.State, pr.MergedAt = store.PRMerged, *p.MergedAt
		case p.State == "open":
			pr.State = store.PROpen
		}
		if err := e.Store.SavePR(pr); err != nil || !j.details {
			return nil, err
		}
		d := j.prev
		d.Commits, d.Files, d.Additions, d.Deletions, d.HeadSHA = p.Commits, p.ChangedFiles, p.Additions, p.Deletions, p.Head.SHA
		return nil, e.details(ctx, t, j, ts, d, true, spend)
	}
	var events []timelineEvent
	if err := json.Unmarshal(resp.Body, &events); err != nil {
		return nil, fmt.Errorf("timeline of issue %d: %w", j.number, err)
	}
	var prs []int
	for _, ev := range events {
		src := ev.Source.Issue
		if ev.Event != "cross-referenced" || src.PullRequest == nil || src.Number == 0 ||
			src.Repository.FullName != "" && !strings.EqualFold(src.Repository.FullName, t.Project) {
			continue
		}
		prs = append(prs, src.Number)
	}
	slices.Sort(prs)
	prs = slices.Compact(prs)
	if err := e.Store.SaveIssueLinks(t.Project, j.number, resp.ETag, ts, prs); err != nil {
		return nil, err
	}
	return prs, nil
}

// details reads what GitHub has beyond the pull request itself and stores
// it. Nothing is stored when the budget runs out first, so the next pass
// tries again. The commit messages are read only when the pull request
// changed. A status other than a rate limit leaves that part empty, so a
// token that cannot read checks does not make every pass repeat the call.
func (e *Enricher) details(ctx context.Context, t Target, j job, ts string, d store.PRDetails, changed bool, spend func() bool) error {
	d.At = ts
	if changed && d.Commits > 0 {
		if !spend() {
			return nil
		}
		path := fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100&page=%d", t.Project, j.number, (d.Commits+99)/100)
		resp, err := e.client.Get(ctx, t.Project, t.Token, path, d.CommitsETag)
		if err = soften(err); err != nil {
			return err
		}
		if !resp.NotModified {
			d.CommitsETag, d.Messages = resp.ETag, latestMessages(resp.Body)
		}
	}
	if sha := d.HeadSHA; isHex(sha) {
		if !spend() {
			return nil
		}
		resp, err := e.client.Get(ctx, t.Project, t.Token, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", t.Project, sha), d.ChecksETag)
		if err = soften(err); err != nil {
			return err
		}
		if !resp.NotModified {
			d.ChecksETag = resp.ETag
			d.ChecksOK, d.ChecksFailed, d.ChecksPending = countChecks(resp.Body)
		}
	}
	return e.Store.SaveDetails(t.Project, j.number, d)
}

// soften turns an answer with an error status other than a rate limit into
// no error, and leaves other errors.
func soften(err error) error {
	var apiErr *github.Error
	if errors.As(err, &apiErr) {
		return nil
	}
	return err
}

func isHex(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// latestMessages returns the first lines of the newest commits of a page
// of commits, newest first.
func latestMessages(body []byte) []string {
	var commits []struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	if json.Unmarshal(body, &commits) != nil {
		return nil
	}
	var out []string
	for i := len(commits) - 1; i >= 0 && len(out) < store.MaxCommitMessages; i-- {
		line, _, _ := strings.Cut(commits[i].Commit.Message, "\n")
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// countChecks counts check runs that passed, failed and are not finished.
// Neutral and skipped runs count as passed.
func countChecks(body []byte) (ok, failed, pending int) {
	var v struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if json.Unmarshal(body, &v) != nil {
		return 0, 0, 0
	}
	for _, c := range v.CheckRuns {
		switch {
		case c.Status != "completed":
			pending++
		case c.Conclusion == "success" || c.Conclusion == "neutral" || c.Conclusion == "skipped":
			ok++
		default:
			failed++
		}
	}
	return ok, failed, pending
}
