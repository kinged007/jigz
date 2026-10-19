package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustNotFatal(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected fatal: %v", r)
		}
	}()
	fn()
}

func mustFatal(t *testing.T, fn func()) error {
	t.Helper()
	var got error
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected fatal, got none")
				return
			}
			fe, ok := r.(fatalError)
			if !ok {
				t.Fatalf("unexpected panic %T: %v", r, r)
			}
			got = fe.err
		}()
		fn()
	}()
	return got
}

func putRegistryEntry(t *testing.T, root string, pid int) {
	t.Helper()
	path, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	var reg registry
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &reg)
	}
	if reg.Entries == nil {
		reg.Entries = map[string]registryEntry{}
	}
	reg.Entries[root] = registryEntry{RepoName: "o/n", UpdatedAt: time.Now(), PID: pid}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}
}

func putStatusDoc(t *testing.T, root string, pid int, state string) {
	t.Helper()
	path, err := statusPath(root)
	if err != nil {
		t.Fatal(err)
	}
	doc := statusDoc{
		SchemaVersion: 1,
		PID:           pid,
		Repo:          root,
		RepoName:      "o/n",
		StartedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
		State:         state,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}
}

func TestParseArgsStart(t *testing.T) {
	c, err := parseArgs([]string{"start", "--unite", "--no-server", "--no-dashboard", "--dashboard-port=9999", "/tmp/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.start || !c.unite || !c.noServer || !c.noDashboard || c.dashPort != 9999 || c.dir != "/tmp/repo" {
		t.Errorf("parse start flags wrong: %+v", c)
	}
	if c.setup || c.remote != "" {
		t.Errorf("unexpected setup/remote: %+v", c)
	}

	c, err = parseArgs([]string{"start", "--dashboard-port", "1234", "--unite"})
	if err != nil {
		t.Fatal(err)
	}
	if c.dashPort != 1234 || !c.unite {
		t.Errorf("start dashboard-port separate arg: %+v", c)
	}

	c, err = parseArgs([]string{"start", "/a/b"})
	if err != nil {
		t.Fatal(err)
	}
	if c.dir != "/a/b" || c.remote != "" {
		t.Errorf("start bare arg: %+v", c)
	}

	c, err = parseArgs([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.start || c.dir != "" {
		t.Errorf("start no args: %+v", c)
	}

	for _, argv := range [][]string{
		{"start", "setup"},
		{"start", "setup", "/tmp/repo"},
		{"start", "stop"},
		{"start", "restart"},
		{"start", "status"},
		{"start", "cleanup"},
		{"start", "wake"},
		{"start", "pause"},
		{"start", "budget"},
		{"start", "reload"},
		{"start", "validate"},
		{"start", "--json"},
		{"start", "--nope"},
		{"start", "--dashboard-port=notanumber"},
		{"start", "--dashboard-port=-1"},
	} {
		if _, err := parseArgs(argv); err == nil {
			t.Errorf("%v: expected error", argv)
		}
	}

	c, err = parseArgs([]string{"start", "--no-server", "--unite", "/tmp/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.noServer || !c.unite || c.dir != "/tmp/repo" {
		t.Errorf("start flag order: %+v", c)
	}
}

func TestBuildStartArgv(t *testing.T) {
	c := cmdline{unite: true, noServer: true, noDashboard: true, dashPort: 8888, dir: "/tmp/repo"}
	got := buildStartArgv(c)
	want := []string{"--unite", "--no-server", "--no-dashboard", "--dashboard-port=8888", "/tmp/repo"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("buildStartArgv = %v, want %v", got, want)
	}

	c = cmdline{dir: "/tmp/repo"}
	if got := buildStartArgv(c); strings.Join(got, " ") != "/tmp/repo" {
		t.Errorf("buildStartArgv dir only = %v", got)
	}

	c = cmdline{unite: true}
	if got := buildStartArgv(c); strings.Join(got, " ") != "--unite" {
		t.Errorf("buildStartArgv unite only = %v", got)
	}
}

func TestWaitForStartReady(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	pid := os.Getpid()
	putStatusDoc(t, root, pid, "running")
	if err := waitForStartReady(root, pid, 500*time.Millisecond); err != nil {
		t.Errorf("running doc: %v", err)
	}

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root2 := t.TempDir()
	other := deadPID(t)
	putStatusDoc(t, root2, other, "running")
	if err := waitForStartReady(root2, pid, 150*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("stale doc pid mismatch: err=%v want timeout", err)
	}

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root3 := t.TempDir()
	dead := deadPID(t)
	putStatusDoc(t, root3, dead, "running")
	start := time.Now()
	err := waitForStartReady(root3, dead, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "process exited") {
		t.Errorf("dead pid with stale liveness: err=%v want process exited", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("dead pid was slow: %s", time.Since(start))
	}

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root4 := t.TempDir()
	if err := waitForStartReady(root4, dead, 5*time.Second); err == nil || !strings.Contains(err.Error(), "process exited") {
		t.Errorf("no doc, dead pid: err=%v want process exited", err)
	}
	start = time.Now()
	if err := waitForStartReady(root4, dead, 5*time.Second); err == nil {
		t.Error("no doc, dead pid: no error")
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Errorf("no doc dead pid slow: %s", time.Since(start))
	}

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root5 := t.TempDir()
	if err := waitForStartReady(root5, pid, 150*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("no doc, alive pid: err=%v want timeout", err)
	}

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root6 := t.TempDir()
	putStatusDoc(t, root6, pid, "exited")
	if err := waitForStartReady(root6, pid, 150*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("exited doc, alive pid: err=%v want timeout", err)
	}
}

func TestSupervisorLogPath(t *testing.T) {
	if got := supervisorLogPath("/repo"); got != filepath.Join("/repo", ".sortie", "logs", "supervisor.log") {
		t.Errorf("supervisorLogPath = %q", got)
	}
}

func TestLaunchDetachedCreatesLogDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".sortie", "logs", "supervisor.log")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := launchDetached(self, []string{"--version"}, logPath)
	if err != nil {
		t.Fatalf("launchDetached: %v", err)
	}
	if pid <= 0 {
		t.Errorf("pid = %d", pid)
	}
	if _, err := os.Stat(filepath.Dir(logPath)); err != nil {
		t.Errorf("log dir not created: %v", err)
	}
}

func TestRunStartAlreadyRunning(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	putRegistryEntry(t, root, os.Getpid())
	origLauncher := startLauncher
	origExec := startExecutable
	defer func() { startLauncher = origLauncher; startExecutable = origExec }()
	called := false
	startLauncher = func(self string, argv []string, logPath string) (int, error) {
		called = true
		return 9999, nil
	}
	err := mustFatal(t, func() { runStart(cmdline{dir: root}) })
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("err = %v, want already running", err)
	}
	if called {
		t.Error("launcher called despite already running")
	}
}

func TestRunStartStaleDoesNotBlock(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	putRegistryEntry(t, root, deadPID(t))
	origLauncher := startLauncher
	origExec := startExecutable
	origBudget := startReadyBudget
	defer func() {
		startLauncher = origLauncher
		startExecutable = origExec
		startReadyBudget = origBudget
	}()
	fakePID := os.Getpid()
	startExecutable = func() (string, error) { return "/fake/sortie-loop", nil }
	startLauncher = func(self string, argv []string, logPath string) (int, error) {
		if self != "/fake/sortie-loop" {
			t.Errorf("self = %q", self)
		}
		putStatusDoc(t, root, fakePID, "running")
		return fakePID, nil
	}
	startReadyBudget = 500 * time.Millisecond
	mustNotFatal(t, func() { runStart(cmdline{dir: root}) })
}

func TestRunStartLauncherError(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	origLauncher := startLauncher
	origExec := startExecutable
	defer func() { startLauncher = origLauncher; startExecutable = origExec }()
	startExecutable = func() (string, error) { return "/fake/self", nil }
	startLauncher = func(self string, argv []string, logPath string) (int, error) {
		return 0, errors.New("boom")
	}
	err := mustFatal(t, func() { runStart(cmdline{dir: root}) })
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "/fake/self") {
		t.Errorf("err = %v, want boom with self path", err)
	}
}

func TestRunStartExecutableError(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	origExec := startExecutable
	defer func() { startExecutable = origExec }()
	startExecutable = func() (string, error) { return "", fmt.Errorf("no exe") }
	err := mustFatal(t, func() { runStart(cmdline{dir: root}) })
	if err == nil || !strings.Contains(err.Error(), "no exe") {
		t.Errorf("err = %v", err)
	}
}
