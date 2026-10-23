package loopsrc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/rollup"
)

type env struct {
	t    *testing.T
	dir  string
	root string
	src  StatusDoc
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, dir: t.TempDir(), root: root}
	e.src = StatusDoc{
		Dir:       e.dir,
		Roots:     func() map[string]string { return map[string]string{"o/p": root} },
		Reader:    rollup.Reader{RegistryPath: filepath.Join(t.TempDir(), "none.json")},
		PollEvery: 10 * time.Millisecond,
	}
	return e
}

func (e *env) path(suffix string) string {
	sum := sha256.Sum256([]byte(e.root))
	return filepath.Join(e.dir, hex.EncodeToString(sum[:])[:16]+suffix)
}

func (e *env) write(suffix, body string) {
	e.t.Helper()
	if err := os.WriteFile(e.path(suffix), []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func doc(schema int, state string, pid int, sleep, workflows string) string {
	return fmt.Sprintf(`{"schema_version":%d,"version":"1.2.0","engine_version":"0.9","pid":%d,"repo":"x","repo_name":"o/p",
"started_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:01:00Z","state":%q,"workflows":[%s],"sleep":%s}`,
		schema, pid, state, workflows, sleep)
}

const awake = `{"enabled":true,"awake":true,"sleeping_since":null,"next_wake":null}`

func wf(name string, port, pid int, state, hash string, restarts int, log string) string {
	return fmt.Sprintf(`{"name":%q,"workflow_file":"WORKFLOW.%s.md","loop_type":"issues","port":%d,"pid":%d,"state":%q,
"started_at":"2026-01-01T00:00:00Z","restart_count":%d,"last_exit_code":null,"config_hash":%q,"log_path":%q}`,
		name, name, port, pid, state, restarts, hash, log)
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestStatusMissingAndProjectUnknown(t *testing.T) {
	e := newEnv(t)
	if _, err := e.src.Status("o/p"); !errors.Is(err, ErrNoStatus) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := e.src.Status("nope"); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("unknown project: %v", err)
	}
	if e.src.ProjectCapabilities("o/p") != (Capabilities{}) || e.src.Capabilities() != (Capabilities{}) {
		t.Error("capabilities reported without a document")
	}
}

func TestStatusLivenessClasses(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name, state string
		pid         int
		want        Liveness
	}{
		{"running alive", "running", os.Getpid(), Live},
		{"running dead pid", "running", deadPID(t), Stale},
		{"exited", "exited", os.Getpid(), Exited},
	}
	for _, c := range cases {
		e.write(".json", doc(1, c.state, c.pid, awake, wf("build", 1, c.pid, "running", "h1", 0, "")))
		st, err := e.src.Status("o/p")
		if err != nil || st.Liveness != c.want {
			t.Errorf("%s: liveness %q err %v, want %q", c.name, st.Liveness, err, c.want)
		}
		if e.src.ProjectCapabilities("o/p") != full {
			t.Errorf("%s: capabilities = %+v", c.name, e.src.ProjectCapabilities("o/p"))
		}
	}
	if c := e.src.ProjectCapabilities("o/p"); c.Budget || c.Pause {
		t.Errorf("budget/pause must stay false: %+v", c)
	}
}

func TestStatusUnsupportedAndCorrupt(t *testing.T) {
	e := newEnv(t)
	e.write(".json", doc(2, "running", os.Getpid(), awake, ""))
	if _, err := e.src.Status("o/p"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("schema 2: %v", err)
	}
	e.write(".json", `{"schema_version":`)
	_, err := e.src.Status("o/p")
	if err == nil || errors.Is(err, ErrNoStatus) || errors.Is(err, ErrUnsupported) {
		t.Errorf("corrupt: %v", err)
	}
	if e.src.ProjectCapabilities("o/p") != (Capabilities{}) {
		t.Error("capabilities from an unreadable document")
	}
}

func TestStatusSleepingAndCleanupSibling(t *testing.T) {
	e := newEnv(t)
	sleeping := `{"enabled":true,"awake":false,"sleeping_since":"2026-01-01T00:00:00Z","next_wake":"2026-01-01T06:00:00Z"}`
	body := strings.Replace(doc(1, "running", os.Getpid(), sleeping, ""), `"sleep":`, `"cleanup":{"last_run_at":"2026-01-01T00:00:00Z","duration_ms":5,"exit_code":0,"dry_run":true,"paths":["a"]},"sleep":`, 1)
	e.write(".json", body)
	st, err := e.src.Status("o/p")
	if err != nil || !st.Sleeping || st.Sleep.NextWake == nil || st.Cleanup == nil || !st.Cleanup.DryRun {
		t.Fatalf("status = %+v, %v", st, err)
	}
	e.write(".cleanup.json", `{"last_run_at":"2026-01-02T00:00:00Z","duration_ms":9,"exit_code":0,"dry_run":false,"paths":["a","b"]}`)
	st, _ = e.src.Status("o/p")
	if st.Cleanup.DryRun || len(st.Cleanup.Paths) != 2 {
		t.Errorf("newer sibling record not used: %+v", st.Cleanup)
	}
}

func TestFleetProbesOnlyAwakeRunningWorkflowsAndFallsBack(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/state" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		fmt.Fprint(w, `{"counts":{"running":1,"retrying":2},"running":[{"issue_identifier":"7","state":"running"}]}`)
	}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])

	e := newEnv(t)
	e.write(".json", doc(1, "running", os.Getpid(), awake, wf("build", port, os.Getpid(), "running", "h", 0, "")))
	f := e.src.Fleet()
	if len(f.Loops) != 1 || !f.Loops[0].Live || f.Loops[0].Retrying != 2 || f.Loops[0].Repo != "o/p" || hits.Load() != 1 {
		t.Fatalf("awake: loops %+v hits %d", f.Loops, hits.Load())
	}

	sleeping := `{"enabled":true,"awake":false,"sleeping_since":"2026-01-01T00:00:00Z","next_wake":null}`
	e.write(".json", doc(1, "running", os.Getpid(), sleeping, wf("build", port, os.Getpid(), "sleeping", "h", 0, "")))
	hits.Store(0)
	f = e.src.Fleet()
	if len(f.Loops) != 1 || f.Loops[0].Live || hits.Load() != 0 {
		t.Errorf("sleeping: loops %+v hits %d", f.Loops, hits.Load())
	}

	dead := deadPID(t)
	e.write(".json", doc(1, "running", dead, awake, wf("build", port, dead, "running", "h", 0, "")))
	f = e.src.Fleet()
	if len(f.Loops) != 1 || f.Loops[0].Live || hits.Load() != 0 {
		t.Errorf("stale: loops %+v hits %d", f.Loops, hits.Load())
	}

	e.write(".json", doc(2, "running", os.Getpid(), awake, wf("build", port, os.Getpid(), "running", "h", 0, "")))
	if f = e.src.Fleet(); len(f.Loops) != 0 || hits.Load() != 0 {
		t.Errorf("unsupported schema must fall back to the (empty) registry: %+v", f.Loops)
	}
}

func TestDBPathFromFrontMatter(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".jigz", "workflows")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "WORKFLOW.a.md"), []byte("---\ndb_path: .sortie-a.db\n---\nbody"), 0o644)
	if got := dbPath(root, "WORKFLOW.a.md"); got != filepath.Join(dir, ".sortie-a.db") {
		t.Errorf("db path = %q", got)
	}
	if got := dbPath(root, "missing.md"); got != filepath.Join(dir, ".sortie.db") {
		t.Errorf("default db path = %q", got)
	}
}

func fakeLoop(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jig")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWakeParsesJSONAndExitCode(t *testing.T) {
	e := newEnv(t)
	e.src.Binary = fakeLoop(t, `echo "$@" > "$0.args"; echo '{"woke":true,"was_sleeping":true}'`)
	res, err := e.src.Wake(context.Background(), "o/p")
	if err != nil || !res.Woke || !res.WasSleeping {
		t.Fatalf("res %+v err %v", res, err)
	}
	args, _ := os.ReadFile(e.src.Binary + ".args")
	if strings.TrimSpace(string(args)) != "wake --json "+e.root {
		t.Errorf("args = %q", args)
	}

	e.src.Binary = fakeLoop(t, `echo '{"woke":false,"was_sleeping":false,"error":"no running supervisor"}'; exit 1`)
	res, err = e.src.Wake(context.Background(), "o/p")
	if err == nil || err.Error() != "no running supervisor" || res.Woke {
		t.Errorf("failure: res %+v err %v", res, err)
	}

	e.src.Binary = fakeLoop(t, `echo boom >&2; exit 2`)
	if _, err = e.src.Wake(context.Background(), "o/p"); err == nil {
		t.Error("exit 2 without JSON must be an error")
	}
	if _, err = e.src.Wake(context.Background(), "nope"); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("unknown project: %v", err)
	}
}

func TestWakeTimeout(t *testing.T) {
	e := newEnv(t)
	e.src.Binary = fakeLoop(t, `exec sleep 5`)
	e.src.WakeTimeout = 100 * time.Millisecond
	start := time.Now()
	if _, err := e.src.Wake(context.Background(), "o/p"); err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("err %v after %v", err, time.Since(start))
	}
}

func TestCleanupDryRunAndFailure(t *testing.T) {
	e := newEnv(t)
	e.src.Binary = fakeLoop(t, `echo "$@" > "$0.args"; echo '{"started_at":"2026-01-01T00:00:00Z","duration_ms":12,"exit_code":0,"dry_run":true,"command":"rm x","output_tail":"ok"}'`)
	res, err := e.src.Cleanup(context.Background(), "o/p", true)
	if err != nil || !res.DryRun || res.DurationMs != 12 || res.OutputTail != "ok" {
		t.Fatalf("res %+v err %v", res, err)
	}
	args, _ := os.ReadFile(e.src.Binary + ".args")
	if strings.TrimSpace(string(args)) != "cleanup --dry-run --json "+e.root {
		t.Errorf("args = %q", args)
	}

	e.src.Binary = fakeLoop(t, `echo '{"exit_code":3,"duration_ms":1,"output_tail":"denied","error":"cleanup failed: exit 3"}'; exit 1`)
	res, err = e.src.Cleanup(context.Background(), "o/p", false)
	if err == nil || err.Error() != "cleanup failed: exit 3" || res.ExitCode != 3 || res.OutputTail != "denied" {
		t.Errorf("failure: res %+v err %v", res, err)
	}
}

func logEnv(t *testing.T) (*env, string) {
	e := newEnv(t)
	logs := filepath.Join(e.root, ".jigz", "logs")
	os.MkdirAll(logs, 0o755)
	return e, logs
}

func TestLogTail(t *testing.T) {
	e, logs := logEnv(t)
	good := filepath.Join(logs, "build.log")
	e.write(".json", doc(1, "running", os.Getpid(), awake, wf("build", 1, os.Getpid(), "running", "h", 0, good)))

	if _, err := e.src.LogTail("o/p", "build", 5); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing log: %v", err)
	}

	var b strings.Builder
	for i := 1; i <= 200000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	os.WriteFile(good, []byte(b.String()), 0o644)
	os.WriteFile(good+".1", []byte("old rotated\n"), 0o644)
	got, err := e.src.LogTail("o/p", "build", 3)
	if err != nil || strings.Join(got, "|") != "line 199998|line 199999|line 200000" {
		t.Errorf("big file: %v %v", got, err)
	}
	got, _ = e.src.LogTail("o/p", "build", 100000)
	if len(got) > maxLines || got[len(got)-1] != "line 200000" {
		t.Errorf("cap: %d lines, last %q", len(got), got[len(got)-1])
	}

	os.WriteFile(good, []byte("fresh after rotation\n"), 0o644)
	got, _ = e.src.LogTail("o/p", "build", 10)
	if len(got) != 1 || got[0] != "fresh after rotation" {
		t.Errorf("rotated: %v", got)
	}

	os.WriteFile(good, nil, 0o644)
	if got, err = e.src.LogTail("o/p", "build", 10); err != nil || len(got) != 0 {
		t.Errorf("empty: %v %v", got, err)
	}
	if _, err = e.src.LogTail("o/p", "other", 10); err == nil {
		t.Error("unknown workflow accepted")
	}
}

func TestLogTailRejectsEscapes(t *testing.T) {
	e, logs := logEnv(t)
	secret := filepath.Join(t.TempDir(), "secret.log")
	os.WriteFile(secret, []byte("secret\n"), 0o644)
	link := filepath.Join(logs, "link.log")
	os.Symlink(secret, link)
	for name, path := range map[string]string{
		"absolute":  secret,
		"dotdot":    filepath.Join(logs, "..", "..", "..", secret),
		"symlink":   link,
		"logs root": logs,
	} {
		e.write(".json", doc(1, "running", os.Getpid(), awake, wf("build", 1, os.Getpid(), "running", "h", 0, path)))
		if got, err := e.src.LogTail("o/p", "build", 5); err == nil {
			t.Errorf("%s accepted: %v", name, got)
		}
	}
}

func TestWaitRecycled(t *testing.T) {
	e := newEnv(t)
	pid := os.Getpid()
	write := func(hash string, restarts int) {
		e.write(".json", doc(1, "running", pid, awake, wf("build", 1, pid, "running", hash, restarts, "")))
	}
	write("old", 0)
	before, err := e.src.Status("o/p")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(60 * time.Millisecond)
		write("old", 1)
		time.Sleep(60 * time.Millisecond)
		write("new", 1)
	}()
	st, err := e.src.WaitRecycled(context.Background(), "o/p", "build", before, 3*time.Second)
	if w, _ := st.Workflow("build"); err != nil || w.ConfigHash != "new" {
		t.Fatalf("st %+v err %v", st, err)
	}

	st, err = e.src.WaitRecycled(context.Background(), "o/p", "build", st, 100*time.Millisecond)
	if !errors.Is(err, ErrNotRecycled) {
		t.Errorf("unchanged: %v", err)
	}
	if _, err = e.src.WaitRecycled(context.Background(), "o/p", "gone", st, time.Second); err == nil {
		t.Error("unknown workflow accepted")
	}
}

func TestRecycled(t *testing.T) {
	t0 := time.Now()
	b := WorkflowInfo{State: "running", ConfigHash: "a", RestartCount: 1, StartedAt: t0}
	for name, tc := range map[string]struct {
		a    WorkflowInfo
		want bool
	}{
		"same hash restarted": {WorkflowInfo{State: "running", ConfigHash: "a", RestartCount: 2, StartedAt: t0.Add(time.Second)}, false},
		"hash changed only":   {WorkflowInfo{State: "running", ConfigHash: "b", RestartCount: 1, StartedAt: t0}, false},
		"restart count":       {WorkflowInfo{State: "running", ConfigHash: "b", RestartCount: 2, StartedAt: t0}, true},
		"started later":       {WorkflowInfo{State: "running", ConfigHash: "b", RestartCount: 1, StartedAt: t0.Add(time.Second)}, true},
		"not running":         {WorkflowInfo{State: "exited", ConfigHash: "b", RestartCount: 2}, false},
	} {
		if Recycled(b, tc.a) != tc.want {
			t.Errorf("%s: want %v", name, tc.want)
		}
	}
}

func TestWorkflowFailedAndFailedWorkflows(t *testing.T) {
	n := func(v int) *int { return &v }
	for name, tc := range map[string]struct {
		w    WorkflowInfo
		want bool
	}{
		"running":       {WorkflowInfo{State: "running"}, false},
		"sleeping":      {WorkflowInfo{State: "sleeping"}, false},
		"stopping":      {WorkflowInfo{State: "stopping"}, false},
		"exited null":   {WorkflowInfo{State: "exited"}, false},
		"exited 0":      {WorkflowInfo{State: "exited", LastExitCode: n(0)}, false},
		"exited 1":      {WorkflowInfo{State: "exited", LastExitCode: n(1)}, true},
		"exited killed": {WorkflowInfo{State: "exited", LastExitCode: n(-1)}, true},
	} {
		if got := tc.w.Failed(); got != tc.want {
			t.Errorf("%s: Failed() = %v, want %v", name, got, tc.want)
		}
	}
	st := StatusInfo{Liveness: Stale, Workflows: []WorkflowInfo{{State: "exited", LastExitCode: n(1)}}}
	if len(st.FailedWorkflows()) != 0 {
		t.Error("a stale document should judge no children")
	}
	st.Liveness = Exited
	if len(st.FailedWorkflows()) != 1 {
		t.Error("an exited run should report its failed child")
	}
}
