package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	config "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/store"
)

// fakeGitHub answers the pulls and timeline endpoints from maps and
// records every request.
type fakeGitHub struct {
	*httptest.Server
	mu       sync.Mutex
	reqs     []string
	auth     map[string]string
	pulls    map[string]string // "acme/app/1" -> JSON
	timeline map[string]string
	commits  map[string]string // "acme/app/9" -> JSON of the commits page
	checks   map[string]string // "acme/app/<sha>" -> JSON
	status   int               // forced status for every request when set
	reset    time.Time
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{auth: map[string]string{}, pulls: map[string]string{}, timeline: map[string]string{},
		commits: map[string]string{}, checks: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodGet {
		http.Error(w, "read only", http.StatusMethodNotAllowed)
		return
	}
	f.reqs = append(f.reqs, r.URL.Path+" "+r.Header.Get("If-None-Match"))
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	project := strings.Join(strings.SplitN(path, "/", 3)[:2], "/")
	f.auth[project] = r.Header.Get("Authorization")
	if !f.reset.IsZero() {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(f.reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	body, ok := "", false
	switch {
	case strings.Contains(path, "/pulls/") && strings.HasSuffix(path, "/commits"):
		body, ok = f.commits[strings.TrimSuffix(strings.Replace(path, "/pulls/", "/", 1), "/commits")]
	case strings.Contains(path, "/check-runs"):
		body, ok = f.checks[strings.TrimSuffix(strings.Replace(path, "/commits/", "/", 1), "/check-runs")]
	case strings.Contains(path, "/pulls/"):
		body, ok = f.pulls[strings.Replace(path, "/pulls/", "/", 1)]
	case strings.HasSuffix(path, "/timeline"):
		body, ok = f.timeline[strings.Replace(strings.TrimSuffix(path, "/timeline"), "/issues/", "/", 1)]
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	etag := fmt.Sprintf(`"%x"`, len(body))
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Write([]byte(body))
}

func (f *fakeGitHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func pullJSON(n int, state, merged string) string {
	m := "null"
	if merged != "" {
		m = `"` + merged + `"`
	}
	closed := "null"
	if state == "closed" {
		closed = `"2026-10-02T00:00:00Z"`
	}
	return fmt.Sprintf(`{"number":%d,"html_url":"https://github.com/acme/app/pull/%d","state":%q,"title":"PR %d",
		"created_at":"2026-10-01T10:00:00Z","closed_at":%s,"merged_at":%s,"head":{"ref":"sortie/issue-%d"}}`,
		n, n, state, n, closed, m, n)
}

type enrichRig struct {
	*Enricher
	st  *store.Store
	gh  *fakeGitHub
	now time.Time
	cfg config.GitHub
	tok map[string]string
}

func newEnrichRig(t *testing.T, runs ...store.Run) *enrichRig {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.AddRuns(runs); err != nil {
		t.Fatal(err)
	}
	r := &enrichRig{st: st, gh: newFakeGitHub(t), now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
		tok: map[string]string{"acme/app": "tok-app", "acme/web": "tok-web"}}
	r.cfg = config.GitHub{Enrich: true, APIURL: r.gh.URL}
	r.Enricher = &Enricher{
		Store: st, Settings: func() config.GitHub { return r.cfg }, Now: func() time.Time { return r.now },
		Targets: func() []Target {
			var out []Target
			for _, p := range []string{"acme/app", "acme/web"} {
				out = append(out, Target{Project: p, Token: r.tok[p], TokenEnv: "GH_TOKEN"})
			}
			return out
		},
	}
	return r
}

func ghRun(id int64, project, workflow, kind, issue, started string) store.Run {
	return store.Run{
		Project: project, Workflow: workflow, WorkflowKind: kind, ID: id, IssueID: issue,
		DisplayIdentifier: project + "#" + issue, Status: "succeeded", Attempt: 1,
		StartedAt: started, CompletedAt: started, AgentAdapter: "claude-code",
	}
}

func prs(t *testing.T, st *store.Store, run store.Run) []store.PR {
	t.Helper()
	out, err := st.RunPRs(run)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOffMakesNoRequests(t *testing.T) {
	r := newEnrichRig(t, ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z"))
	r.cfg.Enrich = false
	r.Pass(context.Background())
	r.Once(context.Background())
	if n := r.gh.count(); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
	if got := r.Status("acme/app"); !got.At.IsZero() {
		t.Errorf("status = %+v, want none", got)
	}
}

func TestPullRequestWorkflowRunIsFilled(t *testing.T) {
	run := ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z")
	r := newEnrichRig(t, run)
	r.gh.pulls["acme/app/5"] = pullJSON(5, "closed", "2026-10-02T09:00:00Z")
	r.Pass(context.Background())
	got := prs(t, r.st, run)
	if len(got) != 1 {
		t.Fatalf("prs = %+v", got)
	}
	p := got[0]
	if p.Branch != "sortie/issue-5" || p.State != store.PRMerged || p.MergedAt != "2026-10-02T09:00:00Z" ||
		p.CreatedAt != "2026-10-01T10:00:00Z" || p.URL != "https://github.com/acme/app/pull/5" {
		t.Errorf("pr = %+v", p)
	}
	if st := r.Status("acme/app"); !st.OK || st.Text() != "last pass ok" || st.Calls != 1 {
		t.Errorf("status = %+v", st)
	}
}

func TestMergedPullRequestIsNotFetchedAgain(t *testing.T) {
	r := newEnrichRig(t, ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z"))
	r.gh.pulls["acme/app/5"] = pullJSON(5, "closed", "2026-10-02T09:00:00Z")
	r.Pass(context.Background())
	r.Pass(context.Background())
	r.now = r.now.Add(48 * time.Hour)
	r.Pass(context.Background())
	if n := r.gh.count(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestOpenPullRequestIsRefreshedWithItsETag(t *testing.T) {
	run := ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z")
	r := newEnrichRig(t, run)
	r.gh.pulls["acme/app/5"] = pullJSON(5, "open", "")
	r.Pass(context.Background())
	r.now = r.now.Add(10 * time.Minute)
	r.Pass(context.Background())
	if len(r.gh.reqs) != 2 || strings.HasSuffix(r.gh.reqs[0], " ") == false || !strings.Contains(r.gh.reqs[1], `"`) {
		t.Fatalf("requests = %q, want the second to carry the ETag", r.gh.reqs)
	}
	if p := prs(t, r.st, run)[0]; p.State != store.PROpen || p.FetchedAt != "2026-10-13T12:10:00Z" {
		t.Errorf("pr = %+v, want open and touched by the 304", p)
	}
	r.gh.pulls["acme/app/5"] = pullJSON(5, "closed", "2026-10-13T12:15:00Z")
	r.Pass(context.Background())
	if p := prs(t, r.st, run)[0]; p.State != store.PRMerged {
		t.Errorf("pr = %+v, want merged after the change", p)
	}
}

func TestIssueRunFindsLinkedPullRequestsFromTheTimeline(t *testing.T) {
	run := ghRun(1, "acme/app", "build", store.KindIssue, "7", "2026-10-01T00:00:00Z")
	r := newEnrichRig(t, run)
	r.gh.timeline["acme/app/7"] = `[
	  {"event":"labeled"},
	  {"event":"connected"},
	  {"event":"cross-referenced","source":{"type":"issue","issue":{"number":12,"pull_request":{},"repository":{"full_name":"acme/app"}}}},
	  {"event":"cross-referenced","source":{"type":"issue","issue":{"number":12,"pull_request":{},"repository":{"full_name":"acme/app"}}}},
	  {"event":"cross-referenced","source":{"type":"issue","issue":{"number":13,"repository":{"full_name":"acme/app"}}}},
	  {"event":"cross-referenced","source":{"type":"issue","issue":{"number":14,"pull_request":{},"repository":{"full_name":"other/repo"}}}}
	]`
	r.gh.pulls["acme/app/12"] = pullJSON(12, "open", "")
	r.Pass(context.Background())
	got := prs(t, r.st, run)
	if len(got) != 1 || got[0].Number != 12 || got[0].Branch != "sortie/issue-12" {
		t.Fatalf("prs = %+v, want only PR 12", got)
	}
	if r.gh.count() != 2 {
		t.Errorf("requests = %q, want the timeline and one pull request", r.gh.reqs)
	}
}

func TestEachProjectUsesOnlyItsOwnToken(t *testing.T) {
	r := newEnrichRig(t,
		ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z"),
		ghRun(2, "acme/web", "review", store.KindPR, "6", "2026-10-01T00:00:00Z"))
	r.gh.pulls["acme/app/5"] = pullJSON(5, "open", "")
	r.gh.pulls["acme/web/6"] = pullJSON(6, "open", "")
	r.Pass(context.Background())
	if r.gh.auth["acme/app"] != "Bearer tok-app" || r.gh.auth["acme/web"] != "Bearer tok-web" {
		t.Errorf("authorization by project = %v", r.gh.auth)
	}
}

func TestCallBudgetAndNewestFirst(t *testing.T) {
	var runs []store.Run
	for i := 1; i <= 5; i++ {
		runs = append(runs, ghRun(int64(i), "acme/app", "review", store.KindPR, strconv.Itoa(i), fmt.Sprintf("2026-10-0%dT00:00:00Z", i)))
	}
	r := newEnrichRig(t, runs...)
	for i := 1; i <= 5; i++ {
		r.gh.pulls[fmt.Sprintf("acme/app/%d", i)] = pullJSON(i, "closed", "2026-10-02T09:00:00Z")
	}
	r.Budget = 2
	r.Pass(context.Background())
	if r.gh.count() != 2 || !strings.HasPrefix(r.gh.reqs[0], "/repos/acme/app/pulls/5") || !strings.HasPrefix(r.gh.reqs[1], "/repos/acme/app/pulls/4") {
		t.Errorf("requests = %q, want PRs 5 then 4", r.gh.reqs)
	}
	r.Pass(context.Background())
	if r.gh.count() != 4 {
		t.Errorf("requests = %d after the second pass, want 4", r.gh.count())
	}
}

func TestDefaultBudgetIsForty(t *testing.T) {
	var runs []store.Run
	for i := 1; i <= 60; i++ {
		runs = append(runs, ghRun(int64(i), "acme/app", "review", store.KindPR, strconv.Itoa(i), "2026-10-01T00:00:00Z"))
	}
	r := newEnrichRig(t, runs...)
	r.gh.status = http.StatusNotFound
	r.Pass(context.Background())
	if n := r.gh.count(); n != DefaultCallBudget {
		t.Errorf("requests = %d, want %d", n, DefaultCallBudget)
	}
}

func TestAuthErrorsAreRecordedAndStopTheProjectPass(t *testing.T) {
	r := newEnrichRig(t,
		ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z"),
		ghRun(2, "acme/app", "review", store.KindPR, "6", "2026-10-02T00:00:00Z"))
	r.gh.status = http.StatusUnauthorized
	r.Pass(context.Background())
	st := r.Status("acme/app")
	if st.OK || st.Text() != "error: GitHub answered 401 Unauthorized" || r.gh.count() != 1 {
		t.Errorf("status = %+v after %d requests", st, r.gh.count())
	}
}

func TestMissingPullRequestIsMarkedAndRetriedLater(t *testing.T) {
	r := newEnrichRig(t,
		ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z"),
		ghRun(2, "acme/app", "review", store.KindPR, "6", "2026-10-02T00:00:00Z"))
	r.gh.pulls["acme/app/5"] = pullJSON(5, "open", "")
	r.Pass(context.Background())
	if st := r.Status("acme/app"); st.OK || !strings.Contains(st.Text(), "acme/app#6") || r.gh.count() != 2 {
		t.Errorf("status = %+v after %d requests, want PR 5 still fetched", st, r.gh.count())
	}
	r.now = r.now.Add(10 * time.Minute)
	r.Pass(context.Background())
	if n := len(r.gh.reqs); n != 3 {
		t.Errorf("requests = %q, want only the open PR again", r.gh.reqs)
	}
}

func TestMissingTokenIsAStatusNotARequest(t *testing.T) {
	r := newEnrichRig(t, ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z"))
	r.tok["acme/app"] = ""
	r.Pass(context.Background())
	if st := r.Status("acme/app"); st.OK || st.Text() != "error: no token: GH_TOKEN is unset or empty" || r.gh.count() != 0 {
		t.Errorf("status = %+v, %d requests", st, r.gh.count())
	}
}

func TestRateLimitSkipsPassesUntilReset(t *testing.T) {
	r := newEnrichRig(t,
		ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z"),
		ghRun(2, "acme/app", "review", store.KindPR, "6", "2026-10-02T00:00:00Z"))
	r.gh.pulls["acme/app/5"] = pullJSON(5, "open", "")
	r.gh.pulls["acme/app/6"] = pullJSON(6, "open", "")
	r.gh.reset = r.now.Add(30 * time.Minute)
	r.Pass(context.Background())
	if st := r.Status("acme/app"); st.OK || !strings.Contains(st.Text(), "rate limit") || r.gh.count() != 1 {
		t.Fatalf("status = %+v after %d requests", st, r.gh.count())
	}
	r.now = r.now.Add(10 * time.Minute)
	r.Pass(context.Background())
	if r.gh.count() != 1 {
		t.Errorf("requests = %d while limited, want no more", r.gh.count())
	}
	r.now = r.now.Add(21 * time.Minute)
	r.gh.reset = time.Time{}
	r.Pass(context.Background())
	if st := r.Status("acme/app"); !st.OK || r.gh.count() != 3 {
		t.Errorf("status = %+v after %d requests, want both PRs fetched after the reset", st, r.gh.count())
	}
}

func TestOnceWaitsForTheInterval(t *testing.T) {
	r := newEnrichRig(t, ghRun(1, "acme/app", "review", store.KindPR, "5", "2026-10-01T00:00:00Z"))
	r.gh.pulls["acme/app/5"] = pullJSON(5, "open", "")
	r.cfg.Interval = "10m"
	r.Once(context.Background())
	r.now = r.now.Add(5 * time.Minute)
	r.Once(context.Background())
	if r.gh.count() != 1 {
		t.Errorf("requests = %d before the interval, want 1", r.gh.count())
	}
	r.now = r.now.Add(5 * time.Minute)
	r.Once(context.Background())
	if r.gh.count() != 2 {
		t.Errorf("requests = %d after the interval, want 2", r.gh.count())
	}
}
