package runlive

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const piTranscript = `{"type":"session","version":3,"id":"s1","timestamp":"2026-01-01T10:00:00.000Z","cwd":"/w"}
{"type":"model_change","id":"a","timestamp":"2026-01-01T10:00:01.000Z","provider":"p","modelId":"m-1"}
{"type":"message","id":"b","timestamp":"2026-01-01T10:00:02.000Z","message":{"role":"system","content":"","sections":{"x":"y"}}}
{"type":"message","id":"c","timestamp":"2026-01-01T10:00:03.000Z","message":{"role":"user","content":[{"type":"text","text":"do the task"}]}}
{"type":"message","id":"d","timestamp":"2026-01-01T10:00:04.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"looking"},{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"ls -la"}}]}}
{"type":"message","id":"e","timestamp":"2026-01-01T10:00:05.000Z","message":{"role":"toolResult","toolCallId":"t1","toolName":"bash","content":[{"type":"text","text":"file.txt"}],"isError":true}}
{"type":"message","id":"f","timestamp":"2026-01-01T10:00:06.000Z","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"rate limited"}}
{"type":"message","id":"g","timestamp":"2026-01-01T10:00:07.000Z","message":{"role":"user","content":"plain string prompt"}}
{"type":"message","id":"h","timestamp":"2026-01-01T10:00:08.000Z","message":{"role":"assistant","content":[{"type":"text","text":"partial`

const claudeTranscript = `{"type":"queue-operation","operation":"enqueue","sessionId":"s"}
{"type":"user","timestamp":"2026-01-01T10:00:00.000Z","message":{"role":"user","content":"the prompt"}}
{"type":"api-request-blob","message":{"role":"user","content":[{"type":"text","text":"hidden"}]}}
{"type":"assistant","timestamp":"2026-01-01T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"x"}]}}
{"type":"assistant","timestamp":"2026-01-01T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"on it"},{"type":"tool_use","id":"u1","name":"Read","input":{"file_path":"/a/b.go"}}]}}
{"type":"user","timestamp":"2026-01-01T10:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"package b","is_error":false}]}}
{"type":"user","timestamp":"2026-01-01T10:00:04.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u2","content":[{"type":"text","text":"boom"}],"is_error":true}]}}
`

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func kinds(evs []Event) string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return strings.Join(k, ",")
}

func TestTailPiNormalisesAndSkipsPartialLastLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, p, piTranscript)
	evs, err := TailEvents(p, Pi, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := "session,session,message,thinking,message,tool_call,tool_result,error,message"
	if got := kinds(evs); got != want {
		t.Fatalf("kinds = %s, want %s", got, want)
	}
	call, res, errEv := evs[5], evs[6], evs[7]
	if call.Title != "bash" || call.Text != "ls -la" || call.Tool != "bash" {
		t.Errorf("tool call = %+v", call)
	}
	if res.Text != "file.txt" || !res.Failed || res.Tool != "bash" {
		t.Errorf("tool result = %+v", res)
	}
	if !errEv.Failed || errEv.Text != "rate limited" {
		t.Errorf("error = %+v", errEv)
	}
	if evs[8].Text != "plain string prompt" {
		t.Errorf("string content = %+v", evs[8])
	}
}

func TestTailClaude(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, p, claudeTranscript)
	evs, err := TailEvents(p, Claude, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := kinds(evs), "message,message,tool_call,tool_result,tool_result"; got != want {
		t.Fatalf("kinds = %s, want %s", got, want)
	}
	if evs[2].Text != "/a/b.go" || evs[3].Tool != "Read" || evs[3].Text != "package b" || evs[3].Failed {
		t.Errorf("tool events = %+v %+v", evs[2], evs[3])
	}
	if !evs[4].Failed || evs[4].Text != "boom" || evs[4].Tool != "" {
		t.Errorf("unmatched failing result = %+v", evs[4])
	}
}

func TestTailLimitAndByteWindow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString(`{"type":"message","timestamp":"t","message":{"role":"user","content":"` + strings.Repeat("x", 1200<<10) + `"}}` + "\n")
	}
	b.WriteString(`{"type":"message","timestamp":"t","message":{"role":"user","content":"last"}}` + "\n")
	write(t, p, b.String())
	evs, err := TailEvents(p, Pi, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Text != "last" {
		t.Fatalf("window gave %d events", len(evs))
	}
	write(t, p, piTranscript)
	if evs, _ = TailEvents(p, Pi, 2); len(evs) != 2 {
		t.Errorf("limit gave %d events", len(evs))
	}
}

func TestTextIsClippedAtARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", MaxText)
	e := newEvent("", KindMessage, "t", long, "", false)
	if !e.Truncated || len(e.Text) > MaxText || !strings.HasSuffix(e.Text, "é") {
		t.Errorf("clip gave %d bytes, truncated=%v", len(e.Text), e.Truncated)
	}
}

func TestSessionDirNames(t *testing.T) {
	ws := "/home/u/repo/.jigz/workspaces/issues/20"
	if got, want := SessionDir("/h", Pi, ws), "/h/.pi/agent/sessions/--home-u-repo-.jigz-workspaces-issues-20--"; got != want {
		t.Errorf("pi dir = %s", got)
	}
	if got, want := SessionDir("/h", Claude, ws), "/h/.claude/projects/-home-u-repo--jigz-workspaces-issues-20"; got != want {
		t.Errorf("claude dir = %s", got)
	}
	if SessionDir("/h", "other", ws) != "" {
		t.Error("unknown kind has a dir")
	}
}

func TestFindTranscript(t *testing.T) {
	home := t.TempDir()
	ws := "/r/.jigz/workspaces/issues/7"
	pi := SessionDir(home, Pi, ws)
	write(t, filepath.Join(pi, "2026-01-01T00-00-00-000Z_aaa-111.jsonl"), "{}\n")
	write(t, filepath.Join(pi, "2026-01-01T00-00-01-000Z_bbb-222.jsonl"), "{}\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(pi, "2026-01-01T00-00-00-000Z_aaa-111.jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	if p, id := FindTranscript(home, Pi, ws, "aaa-111", time.Time{}); !strings.HasSuffix(p, "_aaa-111.jsonl") || id != "aaa-111" {
		t.Errorf("by id = %s %s", p, id)
	}
	if p, id := FindTranscript(home, Pi, ws, "", time.Now().Add(-time.Minute)); !strings.HasSuffix(p, "_bbb-222.jsonl") || id != "bbb-222" {
		t.Errorf("newest = %s %s", p, id)
	}
	if p, _ := FindTranscript(home, Pi, ws, "", time.Now().Add(time.Minute)); p != "" {
		t.Errorf("older than since = %s", p)
	}
	cl := SessionDir(home, Claude, ws)
	write(t, filepath.Join(cl, "ccc-333.jsonl"), "{}\n")
	if p, id := FindTranscript(home, Claude, ws, "ccc-333", time.Time{}); filepath.Base(p) != "ccc-333.jsonl" || id != "ccc-333" {
		t.Errorf("claude = %s %s", p, id)
	}
}

func TestFindTranscriptRejectsEscapes(t *testing.T) {
	home := t.TempDir()
	ws := "/r/.jigz/workspaces/issues/7"
	secret := filepath.Join(t.TempDir(), "secret.jsonl")
	write(t, secret, "{}\n")
	cl := SessionDir(home, Claude, ws)
	write(t, filepath.Join(cl, "keep.jsonl"), "{}\n")
	if err := os.Symlink(secret, filepath.Join(cl, "link-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../secret", "a/b", "..", ".hidden", "x y", strings.Repeat("a", 200)} {
		if p, _ := FindTranscript(home, Claude, ws, id, time.Time{}); p != "" {
			t.Errorf("id %q resolved to %s", id, p)
		}
	}
	if p, _ := FindTranscript(home, Claude, ws, "link-1", time.Time{}); p != "" {
		t.Errorf("symlink out of the session dir resolved to %s", p)
	}
	if p, _ := FindTranscript(home, Claude, ws, "", time.Time{}); filepath.Base(p) != "keep.jsonl" {
		t.Errorf("newest scan followed the symlink: %s", p)
	}
}

func TestLocate(t *testing.T) {
	base := "/r/.jigz/workspaces"
	cases := map[string][2]string{
		"derived":        {"", "/r/.jigz/workspaces/issues/20"},
		"recorded":       {"/r/.jigz/workspaces/issues/20", "/r/.jigz/workspaces/issues/20"},
		"outside":        {"/etc", "/r/.jigz/workspaces/issues/20"},
		"escape":         {"/r/.jigz/workspaces/../../etc", "/r/.jigz/workspaces/issues/20"},
		"the base":       {"/r/.jigz/workspaces", "/r/.jigz/workspaces/issues/20"},
		"sibling prefix": {"/r/.jigz/workspaces-x/a", "/r/.jigz/workspaces/issues/20"},
	}
	for name, c := range cases {
		if got := Locate(base, "issues", "20", c[0]); got != c[1] {
			t.Errorf("%s: Locate = %s, want %s", name, got, c[1])
		}
	}
	if got := Locate(base, "issues", "../x", ""); got != "/r/.jigz/workspaces/issues/.._x" {
		t.Errorf("issue id is not one path element: %s", got)
	}
}

func TestReadDelivery(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "issues", "7")
	write(t, filepath.Join(ws, ".sortie", "scm.json"), `{"pr_number":"12","owner":"o","repo":"r","branch":"agent/7"}`)
	write(t, filepath.Join(ws, ".sortie", "status"), "needs-human-review\nextra\n")
	d := ReadDelivery(base, ws)
	want := Delivery{Branch: "agent/7", PR: 12, Owner: "o", Repo: "r", Status: "needs-human-review"}
	if d != want {
		t.Errorf("delivery = %+v, want %+v", d, want)
	}
	write(t, filepath.Join(ws, ".sortie", "scm.json"), `{"pr_number":12,"branch":"b"}`)
	if d = ReadDelivery(base, ws); d.PR != 12 || d.Branch != "b" {
		t.Errorf("numeric pr = %+v", d)
	}
	if d = ReadDelivery(base, filepath.Join(base, "issues", "none")); !d.Empty() {
		t.Errorf("missing workspace = %+v", d)
	}
}

func TestReadDeliveryIsCappedAndStaysInside(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "issues", "7")
	write(t, filepath.Join(ws, ".sortie", "status"), strings.Repeat("a", 1<<20))
	if d := ReadDelivery(base, ws); len(d.Status) > MaxText {
		t.Errorf("status is %d bytes", len(d.Status))
	}
	outside := filepath.Join(t.TempDir(), "scm.json")
	write(t, outside, `{"branch":"stolen"}`)
	if err := os.Remove(filepath.Join(ws, ".sortie", "status")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, ".sortie", "scm.json")); err != nil {
		t.Fatal(err)
	}
	if d := ReadDelivery(base, ws); !d.Empty() {
		t.Errorf("symlinked scm.json was read: %+v", d)
	}
	linked := filepath.Join(base, "issues", "8")
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, ".sortie", "status"), "blocked")
	if err := os.Symlink(elsewhere, linked); err != nil {
		t.Fatal(err)
	}
	if d := ReadDelivery(base, linked); !d.Empty() {
		t.Errorf("symlinked workspace was read: %+v", d)
	}
}

func TestResumeCommand(t *testing.T) {
	cases := []struct{ kind, ws, id, want string }{
		{Claude, "/r/ws/7", "abc-1", "cd '/r/ws/7' && claude -r abc-1"},
		{Pi, "/r/ws/7", "abc-1", "cd '/r/ws/7' && pi --session abc-1"},
		{Pi, "/r/my dir/it's", "x", `cd '/r/my dir/it'\''s' && pi --session x`},
		{Claude, "/r/$(id)/`x`", "x", "cd '/r/$(id)/`x`' && claude -r x"},
		{"other", "/r", "x", ""},
		{Pi, "/r", "x; rm -rf /", ""},
		{Pi, "/r", "", ""},
		{Pi, "", "x", ""},
	}
	for _, c := range cases {
		if got := ResumeCommand(c.kind, c.ws, c.id); got != c.want {
			t.Errorf("ResumeCommand(%q, %q, %q) = %q, want %q", c.kind, c.ws, c.id, got, c.want)
		}
	}
}

func TestEngineLog(t *testing.T) {
	lines := []string{
		`time=2026-01-01T10:00:00.000Z level=INFO msg="issue dispatched" issue_id=20 issue_identifier=20 agent_kind=pi`,
		`time=2026-01-01T10:00:01.000Z level=INFO msg="running hook" issue_id=20 issue_identifier=20 hook=before_run`,
		`time=2026-01-01T10:00:02.000Z level=INFO msg="issue dispatched" issue_id=200 issue_identifier=200`,
		`time=2026-01-01T10:00:03.000Z level=INFO msg="agent session id accepted" issue_id=20 accepted_session_id=sess-1`,
		`time=2026-01-01T10:00:04.000Z level=WARN msg="slow" issue_id=20`,
		`time=2026-01-01T10:00:05.000Z level=INFO msg="tick completed" running=1`,
	}
	mine := IssueLines(lines, "20", time.Time{}, time.Time{})
	if len(mine) != 4 {
		t.Fatalf("IssueLines = %d lines, want 4 (issue 200 and tick lines are not issue 20)", len(mine))
	}
	from := time.Date(2026, 1, 1, 10, 0, 2, 0, time.UTC)
	if got := IssueLines(lines, "20", from, from.Add(time.Second)); len(got) != 1 || !strings.Contains(got[0], "accepted_session_id") {
		t.Errorf("IssueLines in a window = %v", got)
	}
	if LogSession(mine) != "sess-1" {
		t.Errorf("LogSession = %q", LogSession(mine))
	}
	evs := LogEvents(mine)
	if got := kinds(evs); got != "notification,notification,error" {
		t.Errorf("log event kinds = %s", got)
	}
	if evs[0].Title != "issue dispatched" || evs[0].Time != "2026-01-01T10:00:00.000Z" {
		t.Errorf("first log event = %+v", evs[0])
	}
}

func engineDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "e.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE run_history (id INTEGER, workspace TEXT, dispatch_id TEXT, session_id TEXT)`,
		`INSERT INTO run_history VALUES (5, '/r/.jigz/workspaces/issues/7', 'D1', 'sess-5')`,
		`CREATE TABLE run_events (dispatch_id TEXT, seq INTEGER, at TEXT, event TEXT, completed_at TEXT, size INTEGER, payload TEXT)`,
		`INSERT INTO run_events VALUES ('D1', 2, 't2', 'tool_result', 'c', 1, '{"message":"out","tool_name":"Bash","tool_error":true}')`,
		`INSERT INTO run_events VALUES ('D1', 1, 't1', 'notification', 'c', 1, '{"message":"hello"}')`,
		`INSERT INTO run_events VALUES ('D1', 3, 't3', 'malformed', 'c', 1, 'not json')`,
		`INSERT INTO run_events VALUES ('D2', 1, 't1', 'notification', 'c', 1, '{"message":"other run"}')`,
		`CREATE TABLE session_metadata (issue_id TEXT, session_id TEXT, input_tokens INT, output_tokens INT, total_tokens INT,
			updated_at TEXT, cache_read_tokens INT, model_name TEXT, dispatch_id TEXT, cache_write_tokens INT)`,
		`INSERT INTO session_metadata VALUES ('7', 'sess-7', 10, 20, 30, 'u', 4, 'model-x', 'D7', 5)`,
		`INSERT INTO session_metadata VALUES ('8', 'sess-8', 1, 2, 3, 'u', 0, 'model-y', '', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestEngineDatabase(t *testing.T) {
	path := engineDB(t)
	m, err := ReadRun(path, 5)
	if err != nil || m != (RunMeta{Workspace: "/r/.jigz/workspaces/issues/7", DispatchID: "D1", SessionID: "sess-5"}) {
		t.Errorf("ReadRun = %+v, %v", m, err)
	}
	if m, err = ReadRun(path, 99); err != nil || m != (RunMeta{}) {
		t.Errorf("ReadRun of an unknown run = %+v, %v", m, err)
	}
	evs, err := ReadRunEvents(path, "D1")
	if err != nil || kinds(evs) != "notification,tool_result,error" {
		t.Fatalf("ReadRunEvents = %v, %v", kinds(evs), err)
	}
	if evs[1].Tool != "Bash" || !evs[1].Failed || evs[1].Text != "out" || evs[2].Text != "not json" {
		t.Errorf("events = %+v", evs)
	}
	if evs, _ = ReadRunEvents(path, ""); evs != nil {
		t.Errorf("no dispatch id gave %v", evs)
	}
	s, ok, err := ReadLive(path, "7")
	want := LiveSession{SessionID: "sess-7", DispatchID: "D7", Model: "model-x", UpdatedAt: "u", Input: 10, Output: 20, CacheRead: 4, CacheWrite: 5, Total: 30}
	if err != nil || !ok || s != want {
		t.Errorf("ReadLive = %+v %v %v", s, ok, err)
	}
	if _, ok, _ = ReadLive(path, "8"); ok {
		t.Error("a session of a finished run is live")
	}
	if _, _, err = ReadLive(filepath.Join(t.TempDir(), "none.db"), "7"); err == nil {
		t.Error("a missing database is not an error")
	}
}
