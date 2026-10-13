package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/sortie-loop/internal/config"
)

func cleanupCfg(t *testing.T, script string) *config.Config {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	return &config.Config{
		Dir: root, Workspaces: filepath.Join(root, ".sortie", "workspaces"),
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
	cfg.Cleanup.Command = `{ echo "$SORTIE_CLEANUP_DRY_RUN"; echo "$SORTIE_CLEANUP_MAX_AGE_DAYS"; echo "$SORTIE_CLEANUP_WORKSPACE_ROOT"; echo "$SORTIE_CLEANUP_DB_DIR"; pwd; } > ` + out + `
echo "removed: /a/b"; echo noise; printf 'removed: /c'`
	res, err := runCleanup(context.Background(), cfg, false)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	got, _ := os.ReadFile(out)
	want := "0\n14\n" + cfg.Workspaces + "\n" + filepath.Join(cfg.Dir, ".sortie", "workflows") + "\n"
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
	sup := newSupervisor(cfg.Dir, "sortie", nil, nil)
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
	sup := newSupervisor(cfg.Dir, "sortie", nil, nil)
	sup.startCleanup(cfg)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if data, _ := os.ReadFile(pidFile); strings.TrimSpace(string(data)) != "" {
			break
		}
	}
	sup.shutdown()
	waitProcGone(t, pidFile)
}
