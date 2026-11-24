package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/github"
	"github.com/kinged007/jigz/internal/store"
)

// fakeTracker is an in-memory issueTracker for restart tests.
type fakeTracker struct {
	labels map[int][]string
	isPR   map[int]bool
	calls  []string
	login  string
	fail   map[string]error
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{labels: map[int][]string{}, isPR: map[int]bool{}, fail: map[string]error{}}
}

func (f *fakeTracker) issueLabels(_ context.Context, _, _ string, num int) ([]string, bool, error) {
	f.calls = append(f.calls, "get")
	if err, ok := f.fail["get"]; ok {
		return nil, false, err
	}
	labels, ok := f.labels[num]
	if !ok {
		return nil, false, &github.Error{Status: 404}
	}
	return slices.Clone(labels), f.isPR[num], nil
}

func (f *fakeTracker) addLabels(_ context.Context, _, _ string, num int, labels []string) error {
	f.calls = append(f.calls, "add")
	if err, ok := f.fail["add"]; ok {
		return err
	}
	for _, l := range labels {
		if !hasLabelFold(f.labels[num], l) {
			f.labels[num] = append(f.labels[num], l)
		}
	}
	return nil
}

func (f *fakeTracker) removeLabel(_ context.Context, _, _ string, num int, label string) error {
	f.calls = append(f.calls, "remove")
	if err, ok := f.fail["remove"]; ok {
		return err
	}
	kept := f.labels[num][:0]
	for _, l := range f.labels[num] {
		if !strings.EqualFold(l, label) {
			kept = append(kept, l)
		}
	}
	f.labels[num] = kept
	return nil
}

func (f *fakeTracker) user(_ context.Context, _, _ string) (string, error) {
	if f.login != "" {
		return f.login, nil
	}
	return "tester", nil
}

func testResolved() map[string]config.Resolved {
	return map[string]config.Resolved{
		"issues": {
			Slug: "issues",
			Rules: []config.ResolvedRule{
				{Name: "build", Labels: []string{"agent:build"}},
			},
			Tracker: config.Tracker{InProgressState: "in-progress"},
		},
		"prs": {
			Slug: "prs",
			Rules: []config.ResolvedRule{
				{Name: "review", Labels: []string{"agent:review"}},
			},
			Tracker: config.Tracker{InProgressState: "in-progress"},
		},
	}
}

func testDeps(t *testing.T, tracker *fakeTracker, live ...string) *restartDeps {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	liveSet := map[string]bool{}
	for _, l := range live {
		liveSet[l] = true
	}
	return &restartDeps{
		tracker: tracker, token: "tok", slug: "o/r",
		loops: testResolved(), live: liveSet,
		history: st, events: st,
		now:   func() time.Time { return time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC) },
		actor: "tester",
	}
}

func mustRestart(t *testing.T, d *restartDeps, num int, hint string) issueRestartResult {
	t.Helper()
	res, err := applyIssueRestart(context.Background(), d, num, hint)
	if err != nil {
		t.Fatalf("restart %d: %v", num, err)
	}
	return res
}

func mustRefuse(t *testing.T, d *restartDeps, num int, hint, want string) {
	t.Helper()
	res, err := applyIssueRestart(context.Background(), d, num, hint)
	if err == nil {
		t.Fatalf("restart %d succeeded, want refusal %q", num, want)
	}
	if err.Error() != want {
		t.Fatalf("restart %d refused %q, want %q", num, err.Error(), want)
	}
	if res.Restarted {
		t.Fatalf("restart %d marked restarted on refusal", num)
	}
}

// A mid-run issue (claim marker held by an active run) is refused with
// one line and nothing changes.
func TestRestartRefusesMidRun(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[7] = []string{"agent:build", "in-progress"}
	d := testDeps(t, tr, "7")
	before := append([]string{}, tr.calls...)
	mustRefuse(t, d, 7, "", "issue 7 is mid-run")
	for _, c := range tr.calls[len(before):] {
		if c != "get" {
			t.Fatalf("refused restart wrote to the tracker: %v", tr.calls[len(before):])
		}
	}
	if !slices.Equal(tr.labels[7], []string{"agent:build", "in-progress"}) {
		t.Fatalf("refused restart changed labels: %v", tr.labels[7])
	}
}

// An issue claimed by a live session but without the claim marker is
// refused with one line and nothing changes.
func TestRestartRefusesLiveClaim(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[8] = []string{"agent:build"}
	d := testDeps(t, tr, "8")
	mustRefuse(t, d, 8, "", "issue 8 is claimed by a live session")
	if !slices.Equal(tr.labels[8], []string{"agent:build"}) {
		t.Fatalf("refused restart changed labels: %v", tr.labels[8])
	}
}

// An issue no loop of this root owns is refused with one line.
func TestRestartRefusesForeignLoop(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[9] = []string{"agent:other"}
	d := testDeps(t, tr)
	mustRefuse(t, d, 9, "", "issue 9 belongs to another loop")
	if len(tr.calls) != 1 {
		t.Fatalf("refused restart went beyond the label read: %v", tr.calls)
	}
}

// A missing issue and a pull request are refused with one line.
func TestRestartRefusesUnknownAndPR(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[11] = []string{"agent:build"}
	tr.isPR[11] = true
	d := testDeps(t, tr)
	mustRefuse(t, d, 10, "", "no issue 10 in o/r")
	mustRefuse(t, d, 11, "", "issue 11 is a pull request, not an issue")
}

// Restarting twice leaves the issue restartable, not double-claimed:
// the second run changes no labels.
func TestRestartIsIdempotent(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[7] = []string{"agent:build", "in-progress"}
	d := testDeps(t, tr)
	first := mustRestart(t, d, 7, "")
	if !first.ReleasedClaim || first.AppliedTrigger {
		t.Fatalf("first restart = %+v, want claim released and trigger kept", first)
	}
	n := len(tr.calls)
	second := mustRestart(t, d, 7, "")
	if second.ReleasedClaim || second.AppliedTrigger {
		t.Fatalf("second restart changed labels: %+v", second)
	}
	if len(tr.calls) != n+1 {
		t.Fatalf("second restart made %d tracker writes, want only the label read", len(tr.calls)-n-1)
	}
	if !slices.Equal(tr.labels[7], []string{"agent:build"}) {
		t.Fatalf("labels after two restarts = %v", tr.labels[7])
	}
}

// Trigger-without-claim and claim-without-trigger both restart.
func TestRestartHalfLabelStates(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[7] = []string{"agent:build"}
	d := testDeps(t, tr)
	res := mustRestart(t, d, 7, "")
	if res.ReleasedClaim || res.AppliedTrigger {
		t.Fatalf("trigger-only restart changed labels: %+v", res)
	}

	tr2 := newFakeTracker()
	tr2.labels[8] = []string{"in-progress"}
	d2 := testDeps(t, tr2)
	seedRestartHistory(t, d2.history, "o/r", "issues", "8")
	res2 := mustRestart(t, d2, 8, "")
	if !res2.ReleasedClaim || !res2.AppliedTrigger || res2.Trigger != "agent:build" {
		t.Fatalf("claim-only restart = %+v, want claim released and agent:build applied", res2)
	}
	if !slices.Equal(tr2.labels[8], []string{"agent:build"}) {
		t.Fatalf("labels after restart = %v", tr2.labels[8])
	}
}

// seedRestartHistory stores one finished run of an issue so the history
// fallback can find its workflow.
func seedRestartHistory(t *testing.T, st *store.Store, project, workflow, issue string) {
	t.Helper()
	if err := st.AddRuns([]store.Run{{
		Project: project, Workflow: workflow, ID: 3, IssueID: issue,
		DisplayIdentifier: "o/r#" + issue, Status: "failed",
		StartedAt: "2026-11-24T09:00:00Z", CompletedAt: "2026-11-24T09:05:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
}

// A restart requested while the supervisor is down is stored, applied
// exactly once on start, then cleared.
func TestRestartPendingAppliesOnce(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	req := restartRequest{Issue: 7, Actor: "tester", At: time.Now().UTC()}
	if already, err := storeRestartRequest(root, req); err != nil || already {
		t.Fatalf("store = %v, %v", already, err)
	}
	if already, err := storeRestartRequest(root, req); err != nil || !already {
		t.Fatalf("second store = %v, %v, want already=true", already, err)
	}
	pending, err := pendingRestartIssues(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Issue != 7 {
		t.Fatalf("pending = %+v, want exactly issue 7", pending)
	}

	tr := newFakeTracker()
	tr.labels[7] = []string{"agent:build", "in-progress"}
	d := testDeps(t, tr)
	applied := 0
	for _, p := range pending {
		res, err := applyIssueRestart(context.Background(), d, p.Issue, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Restarted {
			applied++
		}
		prefix, _ := cleanupFilePath(root, restartRequestSuffix)
		files, _ := filepath.Glob(prefix + "*")
		for _, f := range files {
			os.Remove(f)
		}
	}
	if applied != 1 {
		t.Fatalf("applied %d times, want exactly once", applied)
	}
	if pending, _ := pendingRestartIssues(root); len(pending) != 0 {
		t.Fatalf("pending after apply = %+v, want cleared", pending)
	}
}

// The restart event renders on the run page with the same actor and
// timestamp the JSON report carries.
func TestRestartEventMatchesJSON(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[7] = []string{"agent:build", "in-progress"}
	d := testDeps(t, tr)
	res := mustRestart(t, d, 7, "")
	if !res.Restarted || res.Actor != "tester" || res.At == "" {
		t.Fatalf("result = %+v, want restarted with actor and timestamp", res)
	}
	evs, err := d.events.IssueEvents("o/r", "issues", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != restartEventKind {
		t.Fatalf("events = %+v, want one restart event", evs)
	}
	if evs[0].TS != res.At || !strings.Contains(evs[0].Detail, res.Actor) {
		t.Fatalf("event %+v does not match result %+v", evs[0], res)
	}
	rows := runEvents(nil, evs)
	found := false
	for _, r := range rows {
		if r.TS == res.At && strings.Contains(r.Detail, res.Actor) {
			found = true
		}
	}
	if !found {
		t.Fatalf("run page events %+v miss the restart by %s at %s", rows, res.Actor, res.At)
	}
	var decoded issueRestartResult
	out, _ := json.Marshal(res)
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Actor != res.Actor || decoded.At != res.At {
		t.Fatalf("JSON round trip lost actor/timestamp: %+v", decoded)
	}
}

// Parked and retry state is read-only: no Go source outside tests and
// fixtures writes the engine's parked_issues or retry_entries tables.
func TestRestartWritesNoEngineParkedState(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Dir(root)
	var files []string
	err = filepath.WalkDir(repo, func(path string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+"testdata"+string(filepath.Separator)) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := []string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		upper := strings.ToUpper(string(data))
		for _, table := range []string{"PARKED_ISSUES", "RETRY_ENTRIES"} {
			for _, verb := range []string{"INSERT INTO " + table, "UPDATE " + table, "DELETE FROM " + table, "DROP TABLE " + table, "ALTER TABLE " + table, "CREATE TABLE " + table} {
				if strings.Contains(upper, verb) {
					bad = append(bad, f+": "+verb)
				}
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("engine parked/retry writes (must stay read-only):\n%s", strings.Join(bad, "\n"))
	}
}

// A tracker failure mid-sequence reports which step failed and leaves
// the issue restartable.
func TestRestartReportsFailedStep(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[7] = []string{"agent:build", "in-progress"}
	tr.fail["remove"] = errors.New("GitHub answered 403 Forbidden")
	d := testDeps(t, tr)
	_, err := applyIssueRestart(context.Background(), d, 7, "")
	if err == nil || !strings.Contains(err.Error(), "release claim") {
		t.Fatalf("err = %v, want the release-claim step named", err)
	}
	if !slices.Equal(tr.labels[7], []string{"agent:build", "in-progress"}) {
		t.Fatalf("failed restart changed labels: %v", tr.labels[7])
	}
}

// dashboardRestartRig is a dashboard app serving one run, with a fake
// tracker and a registry pointing at a temp root.
type dashboardRestartRig struct {
	app  *app
	root string
	tr   *fakeTracker
}

func newDashboardRestartRig(t *testing.T, labels []string) *dashboardRestartRig {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	regDir := t.TempDir()
	t.Setenv("JIG_REGISTRY_DIR", regDir)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "repo: o/r\ntoken: test-token\nagents:\n  a1: { runtime: pi }\npools:\n  build-pool: [a1]\nwork:\n  issues:\n" +
		"    query_filter: \"label:agent:build,in-progress -label:needs-human\"\n" +
		"    agent: build-pool\n" +
		"    rules:\n" +
		"      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }\n" +
		"  prs:\n" +
		"    query_filter: \"label:agent:review,in-progress -label:needs-human\"\n" +
		"    agent: build-pool\n" +
		"    rules:\n" +
		"      - { name: review, match: { labels: [agent:review] }, prompt: prompts/build.md }\n"
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	t.Setenv("JIG_STATE_DIR", stateDir)
	_ = stateDir
	reg := map[string]any{"entries": map[string]any{root: map[string]any{"repo_name": "o/r", "pid": 1}}}
	data, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// The rig registers a live supervisor, so it needs a readable
	// status doc with a known (empty) session set; without one the
	// restart refuses, as it cannot rule out a live session.
	statusFile, err := statusPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(statusFile), 0o755); err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(statusDoc{SchemaVersion: statusSchemaVersion, Workflows: []workflowStatus{}})
	if err := os.WriteFile(statusFile, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.AddRuns([]store.Run{{
		Project: "o/r", Workflow: "issues", ID: 5, IssueID: "7",
		DisplayIdentifier: "o/r#7", Status: "failed",
		StartedAt: "2026-11-24T09:00:00Z", CompletedAt: "2026-11-24T09:05:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	tr := newFakeTracker()
	tr.labels[7] = labels
	a := &app{stateDir: t.TempDir(), store: st, restartTrackerForTest: tr}
	return &dashboardRestartRig{app: a, root: root, tr: tr}
}

func restartPost(t *testing.T, a *app, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Host = "127.0.0.1"
	req.SetPathValue("owner", "o")
	req.SetPathValue("name", "r")
	req.SetPathValue("workflow", "issues")
	req.SetPathValue("id", "5")
	rec := httptest.NewRecorder()
	a.runRestart(rec, req)
	return rec
}

// The dashboard restart control applies the same operation as the
// command line and redirects to the run page.
func TestDashboardRestartRestarts(t *testing.T) {
	rig := newDashboardRestartRig(t, []string{"agent:build", "in-progress"})
	rec := restartPost(t, rig.app, "/runs/o/r/issues/5/restart")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/runs/o/r/issues/5" {
		t.Fatalf("redirect = %q, want the run page", loc)
	}
	if !slices.Equal(rig.tr.labels[7], []string{"agent:build"}) {
		t.Fatalf("labels = %v, want claim released", rig.tr.labels[7])
	}
	evs, err := rig.app.store.IssueEvents("o/r", "issues", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != restartEventKind {
		t.Fatalf("events = %+v, want one restart event", evs)
	}
}

// The dashboard restart control refuses an unsafe restart with a
// one-line reason, like the command line.
func TestDashboardRestartRefuses(t *testing.T) {
	rig := newDashboardRestartRig(t, []string{"agent:review"})
	rec := restartPost(t, rig.app, "/runs/o/r/issues/5/restart")
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "issue 7 belongs to another loop" {
		t.Fatalf("refusal = %q, want one line", body)
	}
}

// The run page offers exactly one control: the restart form posting to
// the run's restart route. Every other page offers none.
func TestRunPageOffersOnlyRestartControl(t *testing.T) {
	var buf bytes.Buffer
	d := runDetailData{Row: runRow{Href: "/runs/o/r/issues/5", IssueURL: "https://github.com/o/r/issues/7"}, IssueNumber: "7"}
	if err := page.ExecuteTemplate(&buf, "run", d); err != nil {
		t.Fatalf("render run: %v", err)
	}
	body := strings.ToLower(buf.String())
	if n := strings.Count(body, `method="post"`); n != 1 {
		t.Fatalf("run page has %d post forms, want exactly the restart control", n)
	}
	if !strings.Contains(body, `<form method="post" action="/runs/o/r/issues/5/restart">`) {
		t.Fatalf("run page restart form does not post to the run restart route")
	}
	for _, bad := range []string{`action="/action"`, `/projects/new`, `/edit/`, `/backups/restore`, `name="op"`} {
		if strings.Contains(body, bad) {
			t.Fatalf("run page contains %q", bad)
		}
	}
}

// restartStep drops an unreadable request file but keeps a valid one
// it cannot apply yet, so the issue stays restartable.
func TestRestartStepKeepsUnappliableRequests(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	regDir := t.TempDir()
	t.Setenv("JIG_REGISTRY_DIR", regDir)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "repo: o/r\ntoken: test-token\nagents:\n  a1: { runtime: pi }\npools:\n  build-pool: [a1]\nwork:\n  issues:\n" +
		"    query_filter: \"label:agent:build,in-progress -label:needs-human\"\n" +
		"    agent: build-pool\n" +
		"    rules:\n" +
		"      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }\n"
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := registry{Entries: map[string]registryEntry{root: {
		RepoName: "o/r", UpdatedAt: time.Now(), PID: os.Getpid(),
	}}}
	raw, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(regDir, "registry.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := statusPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := statusDoc{SchemaVersion: statusSchemaVersion, PID: os.Getpid(), Repo: root, RepoName: "o/r",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), State: "running",
		Workflows: []workflowStatus{{Name: "issues", PID: 1, State: "running", SessionsKnown: false}}}
	raw, _ = json.Marshal(doc)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prefix+"1-1", []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := restartRequest{Issue: 7, Actor: "tester", At: time.Now().UTC()}
	data, _ := json.Marshal(req)
	if err := os.WriteFile(prefix+"2-1", data, 0o644); err != nil {
		t.Fatal(err)
	}
	(&supervisor{root: root}).restartStep()
	files, _ := filepath.Glob(prefix + "*")
	if len(files) != 1 {
		t.Fatalf("files after step = %v, want only the valid request kept", files)
	}
	pending, err := pendingRestartIssues(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Issue != 7 {
		t.Fatalf("pending = %+v, want issue 7", pending)
	}
}

// An unknown live set refuses like a live one, never like an empty
// one: the issue could be mid-run under a session we cannot see.
func TestRestartRefusesWhenSessionsUnknown(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[7] = []string{"agent:build", "in-progress"}
	d := testDeps(t, tr)
	d.sessionsUnknown = true
	mustRefuse(t, d, 7, "", "issue 7 is mid-run")
	if !slices.Equal(tr.labels[7], []string{"agent:build", "in-progress"}) {
		t.Fatalf("refused restart changed labels: %v", tr.labels[7])
	}

	tr2 := newFakeTracker()
	tr2.labels[8] = []string{"agent:build"}
	d2 := testDeps(t, tr2)
	d2.sessionsUnknown = true
	mustRefuse(t, d2, 8, "", "issue 8 is claimed by a live session")
}

// The history fallback picks the most recent run by start time, not by
// run id, and re-applies that run's rule label: run ids are per-loop
// counters, so the highest id can belong to the older run.
func TestRestartHistoryPrefersNewestRunAndRule(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[7] = []string{"in-progress"}
	loops := map[string]config.Resolved{
		"issues": {Slug: "issues",
			Rules: []config.ResolvedRule{
				{Name: "build", Labels: []string{"agent:build"}},
				{Name: "slow", Labels: []string{"agent:slow"}},
			},
			Tracker: config.Tracker{InProgressState: "in-progress"}},
		"prs": {Slug: "prs",
			Rules: []config.ResolvedRule{
				{Name: "review", Labels: []string{"agent:review"}},
			},
			Tracker: config.Tracker{InProgressState: "in-progress"}},
	}
	d := testDeps(t, tr)
	d.loops = loops
	if err := d.history.AddRuns([]store.Run{
		{Project: "o/r", Workflow: "prs", ID: 9, IssueID: "7",
			DisplayIdentifier: "o/r#7", Status: "failed", RuleName: "review",
			StartedAt: "2026-11-24T08:00:00Z", CompletedAt: "2026-11-24T08:05:00Z"},
		{Project: "o/r", Workflow: "issues", ID: 2, IssueID: "7",
			DisplayIdentifier: "o/r#7", Status: "failed", RuleName: "slow",
			StartedAt: "2026-11-24T09:00:00Z", CompletedAt: "2026-11-24T09:05:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	res := mustRestart(t, d, 7, "")
	if res.Loop != "issues" || res.Trigger != "agent:slow" {
		t.Fatalf("restart = %+v, want loop issues with trigger agent:slow", res)
	}
	if !slices.Equal(tr.labels[7], []string{"agent:slow"}) {
		t.Fatalf("labels after restart = %v", tr.labels[7])
	}
}

// A loop that matches by query, not by label, restarts by releasing the
// claim alone: there is no trigger to re-apply.
func TestRestartTriggerlessLoop(t *testing.T) {
	tr := newFakeTracker()
	tr.labels[7] = []string{"in-progress"}
	loops := map[string]config.Resolved{
		"issues": {Slug: "issues", Tracker: config.Tracker{InProgressState: "in-progress"}},
	}
	d := testDeps(t, tr)
	d.loops = loops
	seedRestartHistory(t, d.history, "o/r", "issues", "7")
	res := mustRestart(t, d, 7, "")
	if !res.ReleasedClaim || res.AppliedTrigger || res.Trigger != "" {
		t.Fatalf("restart = %+v, want claim release only", res)
	}
}

// A restart stored while the supervisor is down is kept while the live
// session set is unknown, then applied once the sessions are known.
func TestRestartStoredDefersWhileSessionsUnknown(t *testing.T) {
	regDir := t.TempDir()
	t.Setenv("JIG_REGISTRY_DIR", regDir)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "repo: o/r\ntoken: test-token\nagents:\n  a1: { runtime: pi }\npools:\n  build-pool: [a1]\nwork:\n  issues:\n" +
		"    query_filter: \"label:agent:build,in-progress -label:needs-human\"\n" +
		"    agent: build-pool\n" +
		"    rules:\n" +
		"      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }\n"
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := registry{Entries: map[string]registryEntry{root: {
		RepoName: "o/r", UpdatedAt: time.Now(), PID: os.Getpid(),
	}}}
	raw, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(regDir, "registry.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	writeRestartStatus := func(known bool) {
		t.Helper()
		path, err := statusPath(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		doc := statusDoc{SchemaVersion: statusSchemaVersion, PID: os.Getpid(), Repo: root, RepoName: "o/r",
			StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), State: "running",
			Workflows: []workflowStatus{{Name: "issues", PID: 1, State: "running", SessionsKnown: known}}}
		raw, _ := json.Marshal(doc)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeRestartStatus(false)
	req := restartRequest{Issue: 7, Actor: "tester", At: time.Now().UTC()}
	if already, err := storeRestartRequest(root, req); err != nil || already {
		t.Fatalf("store = %v, %v", already, err)
	}
	tr := newFakeTracker()
	tr.labels[7] = []string{"agent:build", "in-progress"}
	s := &supervisor{root: root, restartTracker: tr}
	s.restartStep()
	if pending, _ := pendingRestartIssues(root); len(pending) != 1 || pending[0].Issue != 7 {
		t.Fatalf("pending while sessions unknown = %+v, want issue 7 kept", pending)
	}
	if !slices.Equal(tr.labels[7], []string{"agent:build", "in-progress"}) {
		t.Fatalf("labels changed while sessions unknown: %v", tr.labels[7])
	}
	writeRestartStatus(true)
	s.restartStep()
	if pending, _ := pendingRestartIssues(root); len(pending) != 0 {
		t.Fatalf("pending once sessions known = %+v, want cleared", pending)
	}
	if !slices.Equal(tr.labels[7], []string{"agent:build"}) {
		t.Fatalf("labels once sessions known = %v, want claim released", tr.labels[7])
	}
	hst, err := store.Open(regDir)
	if err != nil {
		t.Fatal(err)
	}
	defer hst.Close()
	evs, err := hst.IssueEvents("o/r", "issues", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != restartEventKind {
		t.Fatalf("events = %+v, want one restart event", evs)
	}
}

// A hard-link failure that is not EEXIST still records the request
// through an exclusive create, so state dirs without link support work.
func TestRestartStoreFallsBackWithoutLink(t *testing.T) {
	old := restartLink
	restartLink = func(_, _ string) error { return errors.New("operation not permitted") }
	defer func() { restartLink = old }()
	root := t.TempDir()
	req := restartRequest{Issue: 7, Actor: "tester", At: time.Now().UTC()}
	if already, err := storeRestartRequest(root, req); err != nil || already {
		t.Fatalf("store with link failing = %v, %v, want stored", already, err)
	}
	pending, err := pendingRestartIssues(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Issue != 7 {
		t.Fatalf("pending = %+v, want issue 7", pending)
	}
	if already, err := storeRestartRequest(root, req); err != nil || !already {
		t.Fatalf("second store = %v, %v, want already", already, err)
	}
}

// A stored restart with no usable config is dropped instead of
// retried forever: the next tick would fail identically.
func TestRestartStepDropsBadConfig(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("JIG_REGISTRY_DIR", t.TempDir())
	root := t.TempDir()
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		t.Fatal(err)
	}
	req := restartRequest{Issue: 7, Actor: "tester", At: time.Now().UTC()}
	data, _ := json.Marshal(req)
	if err := os.WriteFile(prefix+"7", data, 0o644); err != nil {
		t.Fatal(err)
	}
	(&supervisor{root: root}).restartStep()
	files, _ := filepath.Glob(prefix + "*")
	if len(files) != 0 {
		t.Fatalf("files after step = %v, want the bad-config request dropped", files)
	}
}

// The no-hard-link fallback publishes the complete temp file with a
// rename, so a crash cannot leave a partial per-issue request behind:
// the stored file parses as the recorded request.
func TestRestartStoreFallbackPublishesCompleteFile(t *testing.T) {
	old := restartLink
	restartLink = func(_, _ string) error { return errors.New("operation not permitted") }
	defer func() { restartLink = old }()
	root := t.TempDir()
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	req := restartRequest{Issue: 7, Actor: "tester", At: at}
	if already, err := storeRestartRequest(root, req); err != nil || already {
		t.Fatalf("store with link failing = %v, %v, want stored", already, err)
	}
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(prefix + "7")
	if err != nil {
		t.Fatal(err)
	}
	var got restartRequest
	if err := json.Unmarshal(raw, &got); err != nil || got.Issue != 7 || got.Actor != "tester" {
		t.Fatalf("stored file = %q, want the complete recorded request", raw)
	}
	if pending, err := pendingRestartIssues(root); err != nil || len(pending) != 1 || pending[0].Issue != 7 {
		t.Fatalf("pending = %+v, %v, want exactly issue 7", pending, err)
	}
	if already, err := storeRestartRequest(root, req); err != nil || !already {
		t.Fatalf("second store = %v, %v, want already", already, err)
	}
}

// A stored restart with no token is dropped as permanent: no later tick
// can supply the missing credential.
func TestRestartStepDropsMissingToken(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("JIG_REGISTRY_DIR", t.TempDir())
	t.Setenv("JIG_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	root := t.TempDir()
	cfg := "repo: o/r\nagents:\n  a1: { runtime: pi }\npools:\n  build-pool: [a1]\nwork:\n  issues:\n" +
		"    query_filter: \"label:agent:build,in-progress -label:needs-human\"\n" +
		"    agent: build-pool\n" +
		"    rules:\n" +
		"      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }\n"
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		t.Fatal(err)
	}
	req := restartRequest{Issue: 7, Actor: "tester", At: time.Now().UTC()}
	data, _ := json.Marshal(req)
	if err := os.WriteFile(prefix+"7", data, 0o644); err != nil {
		t.Fatal(err)
	}
	(&supervisor{root: root}).restartStep()
	if files, _ := filepath.Glob(prefix + "*"); len(files) != 0 {
		t.Fatalf("files after step = %v, want the missing-token request dropped", files)
	}
}

// A stored restart the tracker forbids is dropped as permanent: a 403
// on the label write fails identically on every tick.
func TestRestartStepDropsForbiddenTrackerError(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	regDir := t.TempDir()
	t.Setenv("JIG_REGISTRY_DIR", regDir)
	t.Setenv("JIG_TOKEN", "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "repo: o/r\ntoken: test-token\nagents:\n  a1: { runtime: pi }\npools:\n  build-pool: [a1]\nwork:\n  issues:\n" +
		"    query_filter: \"label:agent:build,in-progress -label:needs-human\"\n" +
		"    agent: build-pool\n" +
		"    rules:\n" +
		"      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }\n"
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := registry{Entries: map[string]registryEntry{root: {
		RepoName: "o/r", UpdatedAt: time.Now(), PID: os.Getpid(),
	}}}
	raw, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(regDir, "registry.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := statusPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := statusDoc{SchemaVersion: statusSchemaVersion, PID: os.Getpid(), Repo: root, RepoName: "o/r",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), State: "running",
		Workflows: []workflowStatus{{Name: "issues", PID: 1, State: "running", SessionsKnown: true}}}
	raw, _ = json.Marshal(doc)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	req := restartRequest{Issue: 7, Actor: "tester", At: time.Now().UTC()}
	if already, err := storeRestartRequest(root, req); err != nil || already {
		t.Fatalf("store = %v, %v", already, err)
	}
	tr := newFakeTracker()
	tr.labels[7] = []string{"agent:build", "in-progress"}
	tr.fail["remove"] = &github.Error{Status: 403}
	s := &supervisor{root: root, restartTracker: tr}
	s.restartStep()
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(prefix + "*"); len(files) != 0 {
		t.Fatalf("files after step = %v, want the forbidden request dropped", files)
	}
	if !slices.Equal(tr.labels[7], []string{"agent:build", "in-progress"}) {
		t.Fatalf("labels after refused write = %v, want untouched", tr.labels[7])
	}
}

// A half-written config file is transient: the request is kept across
// ticks and dropped only after maxRestartConfigAttempts attempts. The
// drop resolves the loop from the issue's last run and records an event,
// so the run page shows what happened to the recorded request.
func TestRestartStepRetriesTransientConfigThenDrops(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("JIG_REGISTRY_DIR", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	hst, err := store.Open(dashboardStateDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := hst.AddRuns([]store.Run{{
		Project: "o/r", Workflow: "issues", ID: 3, IssueID: "7",
		DisplayIdentifier: "o/r#7", Status: "failed",
		StartedAt: "2026-11-24T09:00:00Z", CompletedAt: "2026-11-24T09:05:00Z",
	}}); err != nil {
		hst.Close()
		t.Fatal(err)
	}
	hst.Close()
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		t.Fatal(err)
	}
	req := restartRequest{Issue: 7, Actor: "tester", At: time.Now().UTC()}
	data, _ := json.Marshal(req)
	if err := os.WriteFile(prefix+"7", data, 0o644); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{root: root}
	for i := 1; i < maxRestartConfigAttempts; i++ {
		s.restartStep()
		files, _ := filepath.Glob(prefix + "*")
		if len(files) != 1 {
			t.Fatalf("files after attempt %d = %v, want the request kept", i, files)
		}
		if pending, _ := pendingRestartIssues(root); len(pending) != 1 || pending[0].Issue != 7 {
			t.Fatalf("pending after attempt %d = %+v, want issue 7 kept", i, pending)
		}
	}
	s.restartStep()
	if files, _ := filepath.Glob(prefix + "*"); len(files) != 0 {
		t.Fatalf("files after attempt %d = %v, want the request dropped", maxRestartConfigAttempts, files)
	}
	hst, err = store.Open(dashboardStateDir())
	if err != nil {
		t.Fatal(err)
	}
	defer hst.Close()
	evs, err := hst.IssueEvents("o/r", "issues", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != restartEventKind {
		t.Fatalf("events = %+v, want one restart drop event", evs)
	}
	if !strings.Contains(evs[0].Detail, "gave up") || !strings.Contains(evs[0].Detail, "tester") {
		t.Fatalf("drop event %+v misses gave-up and actor", evs[0])
	}
}

// A failed restore keeps the in-flight marker: the only copy of a
// retryable request is not deleted when the state dir cannot take the
// restored file, so the next tick can reprocess it.
func TestRestartStepKeepsInflightWhenRestoreFails(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("JIG_REGISTRY_DIR", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	prefix, err := cleanupFilePath(root, restartRequestSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		t.Fatal(err)
	}
	req := restartRequest{Issue: 7, Actor: "tester", At: time.Now().UTC()}
	data, _ := json.Marshal(req)
	if err := os.WriteFile(prefix+"7", data, 0o644); err != nil {
		t.Fatal(err)
	}
	old := restartRestoreWrite
	restartRestoreWrite = func(string, []byte) error { return errors.New("disk full") }
	defer func() { restartRestoreWrite = old }()
	(&supervisor{root: root}).restartStep()
	files, _ := filepath.Glob(prefix + "*")
	if len(files) != 1 || !strings.HasSuffix(files[0], ".applying") {
		t.Fatalf("files after failed restore = %v, want the in-flight marker kept", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var got restartRequest
	if err := json.Unmarshal(raw, &got); err != nil || got.Issue != 7 {
		t.Fatalf("in-flight file = %q, want the stored request kept", raw)
	}
}
