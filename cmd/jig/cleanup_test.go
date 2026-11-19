package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/config"
)

func cleanupCfg(t *testing.T, script string) *config.Config {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	return &config.Config{
		Dir: root, Workspaces: filepath.Join(root, ".jigz", "workspaces"),
		Cleanup: config.Cleanup{
			Command: script, Interval: time.Hour, MaxAgeDays: 14, Timeout: time.Minute,
		},
	}
}

// procGone reports whether pid is gone or a zombie waiting to be reaped.
func procGone(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := strings.LastIndex(string(data), ") ")
	return i >= 0 && strings.HasPrefix(string(data)[i+2:], "Z")
}

func waitProcGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("pid file: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if procGone(pid) {
			return
		}
	}
	t.Fatalf("process %d is still running", pid)
}

func TestCleanupPassesEnvironment(t *testing.T) {
	cfg := cleanupCfg(t, "")
	out := filepath.Join(cfg.Dir, "env.txt")
	cfg.Cleanup.Command = `{ echo "$JIG_CLEANUP_DRY_RUN"; echo "$JIG_CLEANUP_MAX_AGE_DAYS"; echo "$JIG_CLEANUP_WORKSPACE_ROOT"; echo "$JIG_CLEANUP_DB_DIR"; pwd; } > ` + out + `
echo "removed: /a/b"; echo noise; printf 'removed: /c'`
	res, err := runCleanup(context.Background(), cfg, false)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	got, _ := os.ReadFile(out)
	want := "0\n14\n" + cfg.Workspaces + "\n" + filepath.Join(cfg.Dir, ".jigz", "workflows") + "\n"
	if !strings.HasPrefix(string(got), want) || !strings.HasSuffix(strings.TrimSpace(string(got)), filepath.Base(cfg.Dir)) {
		t.Errorf("script saw:\n%s\nwant prefix:\n%s", got, want)
	}
	if strings.Join(res.Paths, ",") != "/a/b,/c" {
		t.Errorf("paths = %v", res.Paths)
	}
	res, _ = runCleanup(context.Background(), cfg, true)
	got, _ = os.ReadFile(out)
	if !res.DryRun || !strings.HasPrefix(string(got), "1\n") {
		t.Errorf("dry run not passed: %+v %q", res, got)
	}
}

func TestCleanupFailureExitCode(t *testing.T) {
	cfg := cleanupCfg(t, "echo out; echo err >&2; exit 3")
	res, err := runCleanup(context.Background(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.Error == "" || !strings.Contains(res.OutputTail, "out") || !strings.Contains(res.OutputTail, "err") {
		t.Errorf("res %+v", res)
	}
}

func TestCleanupOutputTailIsCapped(t *testing.T) {
	cfg := cleanupCfg(t, "i=0; while [ $i -lt 2000 ]; do echo line-$i; i=$((i+1)); done")
	res, _ := runCleanup(context.Background(), cfg, false)
	if len(res.OutputTail) > cleanupTailBytes || !strings.Contains(res.OutputTail, "line-1999") {
		t.Errorf("tail length %d", len(res.OutputTail))
	}
}

func TestCleanupTimeoutKillsProcessGroup(t *testing.T) {
	cfg := cleanupCfg(t, "")
	pidFile := filepath.Join(cfg.Dir, "child.pid")
	cfg.Cleanup.Command = "sleep 300 & echo $! > " + pidFile + "; wait"
	cfg.Cleanup.Timeout = 300 * time.Millisecond
	start := time.Now()
	res, err := runCleanup(context.Background(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 || !strings.Contains(res.Error, "timed out") || time.Since(start) > 10*time.Second {
		t.Errorf("res %+v after %s", res, time.Since(start))
	}
	waitProcGone(t, pidFile)
}

func TestCleanupRejectsOverlap(t *testing.T) {
	cfg := cleanupCfg(t, "")
	started := filepath.Join(cfg.Dir, "started")
	cfg.Cleanup.Command = "touch " + started + "; sleep 300"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = runCleanup(ctx, cfg, false); close(done) }()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
	}
	second := *cfg
	second.Cleanup.Command = "true"
	if _, err := runCleanup(context.Background(), &second, false); err != errCleanupBusy {
		t.Errorf("second run error = %v, want busy", err)
	}
	cancel()
	<-done
	if _, err := runCleanup(context.Background(), &second, false); err != nil {
		t.Errorf("run after the first ended: %v", err)
	}
}

func TestCleanupNoCommand(t *testing.T) {
	if _, err := runCleanup(context.Background(), cleanupCfg(t, ""), false); err == nil {
		t.Error("no error without a command")
	}
}

func TestSupervisorRunsCleanupOnInterval(t *testing.T) {
	cfg := cleanupCfg(t, "")
	runs := filepath.Join(cfg.Dir, "runs")
	cfg.Cleanup.Command = "echo x >> " + runs
	cfg.Cleanup.Interval = 150 * time.Millisecond
	sup := newSupervisor(cfg.Dir, "sortie", nil)
	start := time.Now()
	sup.startCleanup(cfg)
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(runs); err == nil {
		t.Error("cleanup ran before the first interval")
	}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if data, _ := os.ReadFile(runs); strings.Count(string(data), "x") >= 2 {
			break
		}
	}
	sup.stopCleanup()
	data, _ := os.ReadFile(runs)
	if strings.Count(string(data), "x") < 2 {
		t.Errorf("runs after %s: %q", time.Since(start), data)
	}
}

func TestShutdownKillsRunningCleanup(t *testing.T) {
	cfg := cleanupCfg(t, "")
	pidFile := filepath.Join(cfg.Dir, "child.pid")
	cfg.Cleanup.Command = "sleep 300 & echo $! > " + pidFile + "; wait"
	cfg.Cleanup.Interval = 50 * time.Millisecond
	sup := newSupervisor(cfg.Dir, "sortie", nil)
	sup.startCleanup(cfg)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if data, _ := os.ReadFile(pidFile); strings.TrimSpace(string(data)) != "" {
			break
		}
	}
	sup.shutdown()
	waitProcGone(t, pidFile)
}

func writeCleanupConfig(t *testing.T, root, command string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"),
		"repo: owner/name\ncleanup:\n  enabled: true\n  interval: 1h\n  max_age_days: 14\n  command: "+strconv.Quote(command)+"\n")
}

func TestCleanupCommandRecordsLastRun(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	writeCleanupConfig(t, root, "echo 'removed: /w/one'; echo 'removed: /w/two'")
	out, err := cleanupOutput(context.Background(), root, true, true)
	if err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	for _, k := range []string{"started_at", "duration_ms", "exit_code", "dry_run", "command", "output_tail"} {
		if _, ok := res[k]; !ok {
			t.Errorf("result lacks %q: %s", k, out)
		}
	}
	if res["dry_run"] != true || res["exit_code"] != float64(0) {
		t.Errorf("result: %s", out)
	}

	rec := readCleanupRecord(root)
	if rec == nil || !rec.DryRun || strings.Join(rec.Paths, ",") != "/w/one,/w/two" {
		t.Fatalf("record: %+v", rec)
	}
	rewriteStatusBase(t, root)
	statusJSON, err := statusOutput(root, true)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion int `json:"schema_version"`
		Cleanup       struct {
			LastRunAt  string   `json:"last_run_at"`
			DurationMs *int64   `json:"duration_ms"`
			ExitCode   *int     `json:"exit_code"`
			DryRun     bool     `json:"dry_run"`
			Paths      []string `json:"paths"`
		} `json:"cleanup"`
	}
	if err := json.Unmarshal([]byte(statusJSON), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || doc.Cleanup.LastRunAt == "" || doc.Cleanup.DurationMs == nil ||
		doc.Cleanup.ExitCode == nil || !doc.Cleanup.DryRun || len(doc.Cleanup.Paths) != 2 {
		t.Errorf("status cleanup: %s", statusJSON)
	}
	human, _ := statusOutput(root, false)
	if !strings.Contains(human, "cleanup: last run") || !strings.Contains(human, "2 paths") {
		t.Errorf("summary lacks the cleanup line:\n%s", human)
	}
}

func rewriteStatusBase(t *testing.T, root string) {
	t.Helper()
	if err := writeStatus(statusDoc{SchemaVersion: statusSchemaVersion, Repo: root, State: "running", PID: os.Getpid(), Workflows: []workflowStatus{}}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupCommandFailureAndNoCommand(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	writeCleanupConfig(t, root, "exit 4")
	out, err := cleanupOutput(context.Background(), root, false, true)
	if err == nil || !strings.Contains(out, `"exit_code": 4`) || !strings.Contains(out, `"error"`) {
		t.Errorf("failure: err %v out %s", err, out)
	}
	if rec := readCleanupRecord(root); rec == nil || rec.ExitCode != 4 {
		t.Errorf("failed run not recorded: %+v", rec)
	}

	bare := t.TempDir()
	mustWrite(t, filepath.Join(bare, ".jigz", "config.yaml"), "repo: owner/name\n")
	if out, err := cleanupOutput(context.Background(), bare, false, true); err == nil || !strings.Contains(out, "no cleanup command") {
		t.Errorf("no command: err %v out %s", err, out)
	}
}

func TestSupervisorRunWritesRecord(t *testing.T) {
	cfg := cleanupCfg(t, "echo 'removed: /x'")
	cfg.Cleanup.Interval = 50 * time.Millisecond
	sup := newSupervisor(cfg.Dir, "sortie", nil)
	sup.startCleanup(cfg)
	defer sup.stopCleanup()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if rec := readCleanupRecord(cfg.Dir); rec != nil {
			if len(rec.Paths) != 1 {
				t.Errorf("record: %+v", rec)
			}
			return
		}
	}
	t.Error("no record written")
}

func TestParseArgsCleanupIsASubcommand(t *testing.T) {
	c, err := parseArgs([]string{"cleanup", "--dry-run", "/some/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if c.remote != "cleanup" || c.dir != "" || len(c.remoteArgs) != 2 {
		t.Errorf("cleanup parsed as %+v", c)
	}
}
