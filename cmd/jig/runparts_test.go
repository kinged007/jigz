package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/runlive"
	"github.com/kinged007/jigz/internal/store"
)

const (
	liveProject  = "o/p"
	liveWorkflow = "issues"
)

// opSource is a fleet source with fixed running agents, project root and
// engine log lines.
type opSource struct {
	loopsrc.StatusDoc
	fleet rollup.Fleet
	root  string
	lines []string
}

func (o opSource) Fleet() rollup.Fleet { return o.fleet }
func (o opSource) Root(string) (string, error) {
	return o.root, nil
}
func (o opSource) ProjectCapabilities(string) loopsrc.Capabilities {
	return loopsrc.Capabilities{Logs: true}
}
func (o opSource) LogTail(string, string, int) ([]string, error) { return o.lines, nil }

type liveRig struct {
	a          *app
	root, ws   string
	home, db   string
	started    time.Time
	transcript string
}

const claudeFixture = `{"type":"user","timestamp":"2026-01-01T10:00:00.000Z","message":{"role":"user","content":"the prompt"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"looking at <b>files</b>"},{"type":"tool_use","id":"u1","name":"Read","input":{"file_path":"/a/b.go"}}]}}
{"type":"user","timestamp":"2026-01-01T10:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"package b <script>alert(1)</script>","is_error":false}]}}
{"type":"assistant","timestamp":"2026-01-01T10:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"half a li`

func newLiveRig(t *testing.T, yaml string) *liveRig {
	t.Helper()
	r := &liveRig{root: t.TempDir(), home: t.TempDir(), started: time.Now().UTC().Add(-time.Minute)}
	r.ws = filepath.Join(r.root, ".jigz", "workspaces", liveWorkflow, "7")
	mustWrite(t, filepath.Join(r.ws, ".sortie", "scm.json"), `{"pr_number":12,"owner":"o","repo":"p","branch":"agent/7"}`)
	mustWrite(t, filepath.Join(r.ws, ".sortie", "status"), "needs-human-review\n")
	r.transcript = filepath.Join(runlive.SessionDir(r.home, runlive.Claude, r.ws), "sess-7.jsonl")
	mustWrite(t, r.transcript, claudeFixture)
	mustWrite(t, filepath.Join(r.root, ".jigz", "workflows", "WORKFLOW.issues.md"), "---\ndb_path: .sortie-issues.db\n---\n")
	r.db = filepath.Join(r.root, ".jigz", "workflows", ".sortie-issues.db")
	db, err := sql.Open("sqlite", r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE run_history (id INTEGER, workspace TEXT, dispatch_id TEXT, session_id TEXT)`,
		`INSERT INTO run_history VALUES (5, '` + r.ws + `', 'D5', 'sess-7')`,
		`INSERT INTO run_history VALUES (6, '` + r.ws + `', 'D6', 'sess-7')`,
		`CREATE TABLE run_events (dispatch_id TEXT, seq INTEGER, at TEXT, event TEXT, completed_at TEXT, size INTEGER, payload TEXT)`,
		`INSERT INTO run_events VALUES ('D5', 1, '2026-01-01T10:00:01Z', 'notification', 'c', 1, '{"message":"engine row note"}')`,
		`INSERT INTO run_events VALUES ('D5', 2, '2026-01-01T10:00:02Z', 'tool_result', 'c', 1, '{"message":"engine row out","tool_name":"Bash","tool_error":true}')`,
		`CREATE TABLE session_metadata (issue_id TEXT, session_id TEXT, input_tokens INT, output_tokens INT, total_tokens INT,
			updated_at TEXT, cache_read_tokens INT, model_name TEXT, dispatch_id TEXT, cache_write_tokens INT)`,
		`INSERT INTO session_metadata VALUES ('7', 'sess-7', 1000, 200, 1500, '2026-01-01T10:00:03Z', 300, 'model-x', 'D7', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r.a = hideApp(t, yaml)
	r.a.liveHome = r.home
	lines := []string{
		`time=` + r.started.Format(time.RFC3339Nano) + ` level=INFO msg="issue dispatched" issue_id=7 issue_identifier=7 agent_kind=claude-code`,
		`time=` + r.started.Add(time.Second).Format(time.RFC3339Nano) + ` level=INFO msg="turn started" issue_id=7 issue_identifier=7`,
		`time=` + r.started.Add(time.Second).Format(time.RFC3339Nano) + ` level=INFO msg="turn started" issue_id=70 issue_identifier=70`,
	}
	r.a.source = opSource{root: r.root, lines: lines, fleet: rollup.Fleet{Loops: []rollup.Loop{{
		Repo: liveProject, Loop: liveWorkflow, Live: true, Running: []rollup.Run{{
			Identifier: "o/p#7", State: "running", Turns: 1, Tokens: 1500, Model: "model-x",
			AgentKind: "claude-code", StartedAt: r.started.Format(time.RFC3339),
		}},
	}}}}
	return r
}

func (r *liveRig) storeRuns(t *testing.T, ids ...int64) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var runs []store.Run
	for _, id := range ids {
		runs = append(runs, store.Run{
			Project: liveProject, Workflow: liveWorkflow, ID: id, IssueID: "7", DisplayIdentifier: "o/p#7",
			Status: "succeeded", Attempt: int(id), AgentAdapter: "claude-code", SessionID: "sess-7",
			StartedAt: "2026-01-01T10:00:00Z", CompletedAt: "2026-01-01T10:05:00Z",
		})
	}
	if err := st.AddRuns(runs); err != nil {
		t.Fatal(err)
	}
	r.a.store = st
}

func body(t *testing.T, a *app, path string, want int) string {
	t.Helper()
	rec := get(a, path)
	if rec.Code != want {
		t.Fatalf("GET %s = %d, want %d: %.300s", path, rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s lacks %q", what, w)
		}
	}
}

func mustNotContain(t *testing.T, what, got string, nots ...string) {
	t.Helper()
	for _, n := range nots {
		if strings.Contains(got, n) {
			t.Errorf("%s contains %q", what, n)
		}
	}
}

func TestLivePageShowsSessionTokensDeliveryAndLogs(t *testing.T) {
	r := newLiveRig(t, "")
	page := body(t, r.a, "/runs/o/p/issues/live-7", http.StatusOK)
	mustContain(t, "live page", page,
		"sess-7", "model-x", "1,500", "1,000",
		"claude -r sess-7", "cd &#39;"+r.ws+"&#39;",
		"agent/7", "https://github.com/o/p/pull/12", "needs-human-review",
		`id="pane-agent"`, `id="pane-engine"`, `data-running="true"`,
		"Read", "/a/b.go", "package b", "issue dispatched", "turn started",
	)
	mustNotContain(t, "live page", page, "half a li", "<script>alert(1)</script>", "<b>files</b>")
	mustContain(t, "live page escapes agent text", page, "&lt;script&gt;alert(1)&lt;/script&gt;", "&lt;b&gt;files&lt;/b&gt;")
}

func TestLiveFragmentAndAgentLogRoutes(t *testing.T) {
	r := newLiveRig(t, "")
	frag := body(t, r.a, "/runs/o/p/issues/live-7/live", http.StatusOK)
	mustContain(t, "fragment", frag, "sess-7", "model-x", "agent/7")
	mustNotContain(t, "fragment", frag, `id="pane-agent"`, "<html")
	var d struct {
		Running bool   `json:"running"`
		HTML    string `json:"html"`
	}
	if err := json.Unmarshal([]byte(body(t, r.a, "/runs/o/p/issues/live-7/agent-log", http.StatusOK)), &d); err != nil {
		t.Fatal(err)
	}
	if !d.Running || !strings.Contains(d.HTML, "/a/b.go") {
		t.Errorf("agent-log = %+v", d)
	}
	body(t, r.a, "/runs/o/p/issues/live-8/live", http.StatusGone)
	body(t, r.a, "/runs/o/p/issues/live-8/agent-log", http.StatusGone)
	body(t, r.a, "/runs/o/p/issues/5/live", http.StatusNotFound)
}

func TestEngineLogRouteFiltersToTheIssueAndWindow(t *testing.T) {
	r := newLiveRig(t, "")
	var d struct{ Lines []string }
	json.Unmarshal([]byte(body(t, r.a, "/runs/o/p/issues/log?issue=7", http.StatusOK)), &d)
	if len(d.Lines) != 2 {
		t.Fatalf("issue 7 lines = %d, want 2: %v", len(d.Lines), d.Lines)
	}
	from := r.started.Add(time.Second).Format(time.RFC3339)
	json.Unmarshal([]byte(body(t, r.a, "/runs/o/p/issues/log?issue=7&from="+from, http.StatusOK)), &d)
	if len(d.Lines) != 1 || !strings.Contains(d.Lines[0], "turn started") {
		t.Errorf("windowed lines = %v", d.Lines)
	}
	json.Unmarshal([]byte(body(t, r.a, "/runs/o/p/issues/log", http.StatusOK)), &d)
	if len(d.Lines) != 3 {
		t.Errorf("unfiltered lines = %d, want 3", len(d.Lines))
	}
}

func TestHiddenAgentLogAndSession(t *testing.T) {
	r := newLiveRig(t, "dashboard:\n  hide:\n    sections: [agent_log]\n")
	page := body(t, r.a, "/runs/o/p/issues/live-7", http.StatusOK)
	mustContain(t, "page", page, `id="pane-engine"`, "sess-7")
	mustNotContain(t, "page with agent_log hidden", page, `id="pane-agent"`, "/a/b.go", "package b")
	frag := body(t, r.a, "/runs/o/p/issues/live-7/live", http.StatusOK)
	mustNotContain(t, "fragment with agent_log hidden", frag, "/a/b.go", "package b", "<td>Read</td>")
	mustContain(t, "fragment keeps engine milestones", frag, "issue dispatched")
	body(t, r.a, "/runs/o/p/issues/live-7/agent-log", http.StatusNotFound)

	r = newLiveRig(t, "dashboard:\n  hide:\n    sections: [session]\n")
	page = body(t, r.a, "/runs/o/p/issues/live-7", http.StatusOK)
	mustNotContain(t, "page with session hidden", page, `id="part-session"`, "claude -r", r.ws, "sess-7")
	mustContain(t, "page with session hidden", page, `id="pane-agent"`, "/a/b.go")
	frag = body(t, r.a, "/runs/o/p/issues/live-7/live", http.StatusOK)
	mustNotContain(t, "fragment with session hidden", frag, "claude -r", r.ws, "sess-7")

	r = newLiveRig(t, "dashboard:\n  hide:\n    sections: [log_tail]\n")
	page = body(t, r.a, "/runs/o/p/issues/live-7", http.StatusOK)
	mustNotContain(t, "page with log_tail hidden", page, `id="pane-engine"`, "issue dispatched")
	mustContain(t, "page with log_tail hidden", page, `id="pane-agent"`)
	body(t, r.a, "/runs/o/p/issues/log", http.StatusNotFound)

	r = newLiveRig(t, "dashboard:\n  hide:\n    pages: [runs]\n")
	for _, p := range []string{"/runs/o/p/issues/live-7/live", "/runs/o/p/issues/live-7/agent-log"} {
		body(t, r.a, p, http.StatusNotFound)
	}
}

func TestStoredRunReadsEngineEventsThenTheTranscript(t *testing.T) {
	r := newLiveRig(t, "")
	r.storeRuns(t, 5, 6)
	page := body(t, r.a, "/runs/o/p/issues/5", http.StatusOK)
	mustContain(t, "stored run", page,
		"engine row note", "engine row out", "Result: Bash", "engine events",
		"sess-7", "claude -r sess-7", "agent/7", "needs-human-review", `data-running="false"`, `id="pane-engine"`)
	mustNotContain(t, "stored run", page, "/a/b.go")

	page = body(t, r.a, "/runs/o/p/issues/6", http.StatusOK)
	mustContain(t, "stored run without event rows", page, "agent transcript", "/a/b.go", "package b")
	mustNotContain(t, "stored run without event rows", page, "engine row note")

	body(t, r.a, "/runs/o/p/issues/5/live", http.StatusNotFound)
	var d struct {
		Running bool
		HTML    string
	}
	json.Unmarshal([]byte(body(t, r.a, "/runs/o/p/issues/5/agent-log", http.StatusOK)), &d)
	if d.Running || !strings.Contains(d.HTML, "engine row note") {
		t.Errorf("stored agent-log = %+v", d)
	}
}

func TestStoredRunWithoutFilesStillRenders(t *testing.T) {
	r := newLiveRig(t, "")
	r.a.source = opSource{root: "", lines: nil}
	r.storeRuns(t, 5)
	page := body(t, r.a, "/runs/o/p/issues/5", http.StatusOK)
	mustContain(t, "stored run without files", page, "sess-7", "not on this machine")
	mustNotContain(t, "stored run without files", page, "claude -r")
}

func TestLiveRunBeforeTheFirstTurnFindsTheTranscript(t *testing.T) {
	r := newLiveRig(t, "")
	db, err := sql.Open("sqlite", r.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM session_metadata`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	page := body(t, r.a, "/runs/o/p/issues/live-7", http.StatusOK)
	mustContain(t, "page before the first turn", page, "sess-7", "claude -r sess-7", "/a/b.go", "after the first turn")
}

func TestRunPartsPaths(t *testing.T) {
	r := newLiveRig(t, "")
	evil := filepath.Join(t.TempDir(), "stolen.jsonl")
	mustWrite(t, evil, `{"type":"user","message":{"role":"user","content":"stolen"}}`+"\n")
	dir := filepath.Dir(r.transcript)
	if err := os.Symlink(evil, filepath.Join(dir, "evil-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	p := r.a.runParts(runInput{Project: liveProject, Workflow: liveWorkflow, IssueID: "7", Running: true, Adapter: "claude-code", SessionID: "evil-1", Started: r.started})
	if len(p.Agent) != 0 || p.AgentWhy == "" {
		t.Errorf("symlinked transcript was read: %+v", p.Agent)
	}
	p = r.a.runParts(runInput{Project: liveProject, Workflow: liveWorkflow, IssueID: "7", Running: true, Adapter: "claude-code", SessionID: "../../../etc/passwd", Started: r.started})
	if len(p.Agent) != 0 {
		t.Errorf("a path as session id was read: %+v", p.Agent)
	}
	if p.Session.Resume != "" {
		t.Errorf("resume command for an unusable id: %q", p.Session.Resume)
	}
	p = r.a.runParts(runInput{Project: liveProject, Workflow: liveWorkflow, IssueID: "7", Running: true, Adapter: "other", Started: r.started})
	if p.AgentWhy == "" || p.Session.Resume != "" {
		t.Errorf("unknown agent: why=%q resume=%q", p.AgentWhy, p.Session.Resume)
	}
}

func TestRunRoutesAreGetOnly(t *testing.T) {
	r := newLiveRig(t, "")
	h := r.a.routes(newHostGuard("127.0.0.1"))
	for _, path := range []string{"/runs/o/p/issues/live-7/live", "/runs/o/p/issues/live-7/agent-log", "/runs/o/p/issues/log"} {
		for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(m, path, nil)
			req.Host = "127.0.0.1"
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", m, path, rec.Code)
			}
		}
	}
}

func TestEventKindsHaveIcons(t *testing.T) {
	for _, k := range []string{"message", "thinking", "tool_call", "tool_result", "notification", "turn", "session", "error"} {
		out := render(t, "event-icon", runlive.Event{Kind: k})
		if !strings.Contains(out, "<svg") {
			t.Errorf("kind %s has no icon", k)
		}
	}
	if render(t, "event-icon", runlive.Event{Kind: "tool_result"}) == render(t, "event-icon", runlive.Event{Kind: "message"}) {
		t.Error("tool_result and message share an icon")
	}
}
