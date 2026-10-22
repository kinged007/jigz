package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/store"
)

type fakeEngine struct {
	srv *httptest.Server

	mu       sync.Mutex
	runs     []apiRun
	state    apiState
	requests []string
	failRuns bool
}

func newFakeEngine(t *testing.T) *fakeEngine {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	e := &fakeEngine{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/state", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		json.NewEncoder(w).Encode(e.state)
	})
	mux.HandleFunc("/api/v1/runs", e.serveRuns)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *fakeEngine) serveRuns(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, r.URL.RawQuery)
	if e.failRuns {
		http.Error(w, "history unavailable", http.StatusServiceUnavailable)
		return
	}
	limit, before := 50, int64(1<<62)
	for k, v := range r.URL.Query() {
		n, err := strconv.ParseInt(v[0], 10, 64)
		if (k != "limit" && k != "cursor") || err != nil || n < 1 || len(v) != 1 {
			http.Error(w, "bad parameter "+k, http.StatusBadRequest)
			return
		}
		if k == "limit" {
			limit = int(min(n, 200))
		} else {
			before = n
		}
	}
	page := struct {
		Runs       []apiRun `json:"runs"`
		NextCursor *string  `json:"next_cursor"`
	}{Runs: []apiRun{}}
	for i := len(e.runs) - 1; i >= 0; i-- {
		if e.runs[i].RunID >= before {
			continue
		}
		if len(page.Runs) == limit {
			next := strconv.FormatInt(page.Runs[len(page.Runs)-1].RunID, 10)
			page.NextCursor = &next
			break
		}
		page.Runs = append(page.Runs, e.runs[i])
	}
	json.NewEncoder(w).Encode(page)
}

func (e *fakeEngine) add(first, last int64, file string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id := first; id <= last; id++ {
		e.runs = append(e.runs, apiRun{
			RunID: id, IssueID: strconv.FormatInt(id, 10), IssueIdentifier: strconv.FormatInt(id, 10),
			Status: "succeeded", WorkflowFile: file, AgentKind: "mock",
			StartedAt: "2026-10-01T00:00:00Z", CompletedAt: "2026-10-01T00:01:00Z",
		})
	}
}

func (e *fakeEngine) runRequests() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.requests)
}

func (e *fakeEngine) loop(t *testing.T, name, dbPath string) rollup.Loop {
	t.Helper()
	u, err := url.Parse(e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	return rollup.Loop{Repo: "acme/app", Loop: name, Port: port, Live: true, DBPath: dbPath}
}

func apiCollector(t *testing.T, l rollup.Loop) (*Collector, *store.Store) {
	t.Helper()
	c, st := newCollector(t, l.DBPath)
	c.Source = fakeSource{loops: []rollup.Loop{l}}
	return c, st
}

func storedIDs(t *testing.T, st *store.Store) []int64 {
	t.Helper()
	runs, err := st.Runs(store.RunFilter{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestAPIRunsAreCopiedAcrossPagesAndPollsWithoutGapsOrDuplicates(t *testing.T) {
	e := newFakeEngine(t)
	e.add(1, 450, "WORKFLOW.build.md")
	c, st := apiCollector(t, e.loop(t, "build", ""))
	c.Once(context.Background())
	if n, _ := st.CountRuns(store.RunFilter{}); n != 450 {
		t.Fatalf("first poll stored %d runs, want 450 from three pages", n)
	}
	if got := e.runRequests(); got != 3 {
		t.Errorf("first poll made %d run requests, want 3", got)
	}

	e.add(451, 455, "WORKFLOW.build.md")
	c.Once(context.Background())
	if got := e.runRequests(); got != 4 {
		t.Errorf("second poll made %d run requests in total, want one more page", got)
	}
	if n, _ := st.CountRuns(store.RunFilter{}); n != 455 {
		t.Fatalf("stored %d runs, want 455", n)
	}
	if last, _ := st.LastRunID("acme/app", "build"); last != 455 {
		t.Errorf("last run id = %d, want 455", last)
	}

	e.add(456, 700, "WORKFLOW.build.md")
	c.Once(context.Background())
	if n, _ := st.CountRuns(store.RunFilter{}); n != 700 {
		t.Errorf("stored %d runs after a poll that spans two pages, want 700", n)
	}
	for id := int64(1); id <= 700; id++ {
		if _, err := st.Run("acme/app", "build", id); err != nil {
			t.Fatalf("run %d: %v", id, err)
		}
	}
}

func TestAPIRunFieldsMapToTheStoreRun(t *testing.T) {
	e := newFakeEngine(t)
	sess := "sess-1"
	msg := "The agent finished without changing anything, so Sortie did not hand the issue off."
	in, out, total, cache := int64(10), int64(5), int64(15), int64(3)
	r1 := apiRun{RunID: 1, IssueID: "7", IssueIdentifier: "7", DisplayIdentifier: "acme/app#7", Attempt: 2, Status: "failed",
		Error: &msg, AgentKind: "pi", ModelName: "m-observed", ConfiguredModel: "m-configured",
		WorkflowFile: "WORKFLOW.build.md", StartedAt: "2026-10-01T00:00:00Z", CompletedAt: "2026-10-01T00:02:00Z",
		TurnsCompleted: 3, TokensMeasured: true, SessionID: &sess}
	r1.Tokens.Input, r1.Tokens.Output, r1.Tokens.Total, r1.Tokens.CacheRead = &in, &out, &total, &cache
	r2 := apiRun{RunID: 2, IssueID: "8", IssueIdentifier: "8", Status: "succeeded", ConfiguredModel: "m-configured"}
	r3 := apiRun{RunID: 3, IssueID: "9", IssueIdentifier: "9", Status: "succeeded"}
	e.runs = []apiRun{r1, r2, r3}
	c, st := apiCollector(t, e.loop(t, "build", ""))
	c.Kind = func(string, string) string { return store.KindIssue }
	c.Once(context.Background())
	get := func(id int64) store.Run {
		r, err := st.Run("acme/app", "build", id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	want := store.Run{Project: "acme/app", Workflow: "build", WorkflowKind: store.KindIssue, ID: 1, IssueID: "7",
		DisplayIdentifier: "acme/app#7", Status: "failed", StartedAt: "2026-10-01T00:00:00Z", CompletedAt: "2026-10-01T00:02:00Z",
		Attempt: 2, TurnsCompleted: 3, Error: msg, AgentAdapter: "pi", Model: "m-observed", ModelSource: store.ModelObserved,
		InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CacheReadTokens: 3, TokensMeasured: true,
		WorkflowFile: "WORKFLOW.build.md", SessionID: "sess-1"}
	got := get(1)
	got.Cursor = 0
	if got.Project != want.Project || got.Workflow != want.Workflow || got.WorkflowKind != want.WorkflowKind ||
		got.IssueID != want.IssueID || got.DisplayIdentifier != want.DisplayIdentifier || got.Status != want.Status ||
		got.StartedAt != want.StartedAt || got.CompletedAt != want.CompletedAt || got.Attempt != want.Attempt ||
		got.TurnsCompleted != want.TurnsCompleted || got.Error != want.Error || got.AgentAdapter != want.AgentAdapter ||
		got.Model != want.Model || got.ModelSource != want.ModelSource || got.InputTokens != 10 || got.OutputTokens != 5 ||
		got.TotalTokens != 15 || got.CacheReadTokens != 3 || !got.TokensMeasured ||
		got.WorkflowFile != want.WorkflowFile || got.SessionID != want.SessionID {
		t.Errorf("run 1 = %+v, want %+v", got, want)
	}
	if r := get(2); r.Model != "m-configured" || r.ModelSource != store.ModelConfigured || r.DisplayIdentifier != "8" ||
		r.TokensMeasured || r.TotalTokens != 0 {
		t.Errorf("run 2 = %+v, want the configured model, the issue identifier and zero tokens", r)
	}
	if r := get(3); r.Model != "cfg-model" || r.ModelSource != store.ModelConfigured {
		t.Errorf("run 3 = %+v, want the workflow's configured model", r)
	}
}

func TestAPIRunsOfOtherWorkflowsInASharedDatabaseAreSkipped(t *testing.T) {
	e := newFakeEngine(t)
	e.add(1, 2, "WORKFLOW.build.md")
	e.add(3, 4, "WORKFLOW.other.md")
	e.add(5, 5, "")
	e.add(6, 6, "/repo/.sortie/workflows/WORKFLOW.build.md")
	c, st := apiCollector(t, e.loop(t, "build", ""))
	c.Once(context.Background())
	got := storedIDs(t, st)
	if len(got) != 4 || got[0] != 6 || got[1] != 5 || got[2] != 2 || got[3] != 1 {
		t.Errorf("stored ids = %v, want [6 5 2 1]", got)
	}
}

func TestAPIResetsStoredRunsWhenTheEngineDatabaseWasReplaced(t *testing.T) {
	e := newFakeEngine(t)
	e.add(1, 3, "WORKFLOW.build.md")
	c, st := apiCollector(t, e.loop(t, "build", ""))
	var old []store.Run
	for id := int64(1); id <= 10; id++ {
		old = append(old, store.Run{Project: "acme/app", Workflow: "build", ID: id, IssueID: "old", Status: "failed", AgentAdapter: "pi"})
	}
	if err := st.AddRuns(old); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRuns([]store.Run{{Project: "acme/app", Workflow: "other", ID: 20, IssueID: "x", Status: "failed"}}); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	r, err := st.Run("acme/app", "build", 3)
	if err != nil || r.IssueID != "3" || r.AgentAdapter != "mock" {
		t.Fatalf("run 3 = %+v, %v, want the engine's run", r, err)
	}
	if n, _ := st.CountRuns(store.RunFilter{Workflow: "build"}); n != 3 {
		t.Errorf("build has %d stored runs, want the 3 of the new database", n)
	}
	if n, _ := st.CountRuns(store.RunFilter{Workflow: "other"}); n != 1 {
		t.Errorf("another workflow lost its runs: %d left", n)
	}
}

func TestEmptyEngineHistoryKeepsStoredRuns(t *testing.T) {
	e := newFakeEngine(t)
	c, st := apiCollector(t, e.loop(t, "build", ""))
	if err := st.AddRuns([]store.Run{{Project: "acme/app", Workflow: "build", ID: 5, IssueID: "x", Status: "failed"}}); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	if n, _ := st.CountRuns(store.RunFilter{}); n != 1 {
		t.Errorf("stored runs = %d, want the existing one kept", n)
	}
}

func TestAPIIsReadInsteadOfTheDatabaseWhenTheEngineAnswers(t *testing.T) {
	path, _ := fixture(t)
	e := newFakeEngine(t)
	e.add(1, 2, "WORKFLOW.build.md")
	c, st := apiCollector(t, e.loop(t, "build", path))
	c.Once(context.Background())
	if got := storedIDs(t, st); len(got) != 2 {
		t.Errorf("stored ids = %v, want only the engine's two runs, not the 16 of the database", got)
	}
}

func TestDatabaseIsReadWhenTheEngineDoesNotAnswer(t *testing.T) {
	path, _ := fixture(t)
	e := newFakeEngine(t)
	e.add(1, 2, "WORKFLOW.build.md")
	l := e.loop(t, "build", path)
	e.srv.Close()
	c, st := apiCollector(t, l)
	c.Once(context.Background())
	if n, _ := st.CountRuns(store.RunFilter{}); n != 16 {
		t.Errorf("stored %d runs with the engine down, want the 16 of the database", n)
	}
}

func TestDatabaseIsReadWhenTheRunsEndpointFails(t *testing.T) {
	path, _ := fixture(t)
	e := newFakeEngine(t)
	e.failRuns = true
	c, st := apiCollector(t, e.loop(t, "build", path))
	c.Once(context.Background())
	if n, _ := st.CountRuns(store.RunFilter{}); n != 16 {
		t.Errorf("stored %d runs after a failed runs request, want the 16 of the database", n)
	}
}

func TestAPIStateChangesBecomeEventsAfterTheFirstPass(t *testing.T) {
	e := newFakeEngine(t)
	e.state.Parked = append(e.state.Parked, struct {
		IssueID string `json:"issue_id"`
		Reason  string `json:"reason"`
	}{"13", "handoff absence"})
	c, st := apiCollector(t, e.loop(t, "build", ""))
	c.Once(context.Background())
	if ev, _ := st.IssueEvents("acme/app", "build", "13"); len(ev) != 0 {
		t.Fatalf("first pass recorded %+v, want a baseline only", ev)
	}

	due := time.UnixMilli(1791969000000).UTC()
	e.mu.Lock()
	e.state.Parked = e.state.Parked[:0]
	e.state.Retrying = append(e.state.Retrying, struct {
		IssueID string    `json:"issue_id"`
		Attempt int       `json:"attempt"`
		DueAt   time.Time `json:"due_at"`
		Error   string    `json:"error"`
	}{"19", 2, due, "boom"})
	e.state.BudgetExhausted = append(e.state.BudgetExhausted, struct {
		IssueID string `json:"issue_id"`
		Reason  string `json:"reason"`
	}{"20", "token budget"})
	e.mu.Unlock()
	c.Once(context.Background())
	for issue, want := range map[string]string{
		"13": "unparked:handoff absence;",
		"19": "retry_scheduled:attempt 2 due 1791969000000: boom;",
		"20": "budget_hold:token budget;",
	} {
		ev, _ := st.IssueEvents("acme/app", "build", issue)
		got := ""
		for _, x := range ev {
			got += x.Kind + ":" + x.Detail + ";"
		}
		if got != want {
			t.Errorf("issue %s events = %q, want %q", issue, got, want)
		}
	}
	c.Once(context.Background())
	if ev, _ := st.IssueEvents("acme/app", "build", "19"); len(ev) != 1 {
		t.Errorf("an unchanged state recorded again: %+v", ev)
	}
}
