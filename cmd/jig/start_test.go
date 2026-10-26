package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeStart struct {
	st       starter
	out      strings.Builder
	spawned  [][]string
	logPath  string
	live     bool
	registry registryEntry
}

func newFakeStart(t *testing.T) *fakeStart {
	f := &fakeStart{logPath: filepath.Join(t.TempDir(), "repo.log")}
	f.st = starter{
		logPath: func(string) (string, error) { return f.logPath, nil },
		running: func(string) (registryEntry, error) {
			if f.live {
				return f.registry, nil
			}
			return registryEntry{}, errors.New("no loop is running")
		},
		budget: 300 * time.Millisecond,
		out:    func(format string, a ...any) { fmt.Fprintf(&f.out, format, a...) },
	}
	return f
}

func appendLog(t *testing.T, path, text string) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	fh.WriteString(text)
}

func TestStartArgv(t *testing.T) {
	c, err := parseArgs([]string{"start", "--no-dashboard", "--host", "0.0.0.0", "--dashboard-port=9000", "--unite", "--no-server", "./x"})
	if err != nil || c.remote != "start" {
		t.Fatalf("parse: %+v %v", c, err)
	}
	inner, err := parseArgs(c.remoteArgs)
	if err != nil {
		t.Fatal(err)
	}
	got := startArgv(inner, "/abs/x")
	want := []string{"--no-server", "--no-dashboard", "--unite", "--dashboard-port=9000", "--host=0.0.0.0", "/abs/x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
	if got := startArgv(cmdline{}, "/r"); !reflect.DeepEqual(got, []string{"/r"}) {
		t.Errorf("bare argv = %v", got)
	}
}

func TestStartAlreadyRunning(t *testing.T) {
	f := newFakeStart(t)
	f.live, f.registry = true, registryEntry{PID: 42}
	f.st.spawn = func([]string, string) (int, <-chan error, error) {
		t.Fatal("spawned although a run serves the root")
		return 0, nil, nil
	}
	if err := f.st.start(cmdline{}, "/r"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "already running for /r (pid 42)") {
		t.Errorf("output: %s", f.out.String())
	}
}

func TestStartRegisters(t *testing.T) {
	f := newFakeStart(t)
	appendLog(t, f.logPath, "dashboard http://old:1 (bound to x)\n")
	f.st.spawn = func(argv []string, logPath string) (int, <-chan error, error) {
		f.spawned = append(f.spawned, argv)
		appendLog(t, logPath, "loop plan WORKFLOW.plan.md filter \"\"\ndashboard http://127.0.0.1:7677 (bound to 127.0.0.1:7677)\n")
		f.registry = registryEntry{PID: 77}
		f.live = true
		return 77, make(chan error), nil
	}
	if err := f.st.start(cmdline{unite: true}, "/r"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.spawned, [][]string{{"--unite", "/r"}}) {
		t.Errorf("spawned %v", f.spawned)
	}
	out := f.out.String()
	for _, want := range []string{"started /r (pid 77)", "dashboard http://127.0.0.1:7677\n", "log:    " + f.logPath, "jig status /r", "jig stop /r"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestStartNoDashboardOmitsURL(t *testing.T) {
	f := newFakeStart(t)
	f.live, f.registry = false, registryEntry{PID: 5}
	f.st.spawn = func(argv []string, logPath string) (int, <-chan error, error) {
		f.live = true
		return 5, make(chan error), nil
	}
	if err := f.st.start(cmdline{noDashboard: true}, "/r"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.out.String(), "dashboard ") {
		t.Errorf("output: %s", f.out.String())
	}
}

func TestStartFailsWhenRunExits(t *testing.T) {
	f := newFakeStart(t)
	f.st.spawn = func(argv []string, logPath string) (int, <-chan error, error) {
		appendLog(t, logPath, "jig: engine binary not found\n")
		ch := make(chan error, 1)
		ch <- errors.New("exit status 1")
		return 9, ch, nil
	}
	err := f.st.start(cmdline{}, "/r")
	if err == nil || !strings.Contains(err.Error(), "exited before it registered") || !strings.Contains(err.Error(), "engine binary not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartFailsOnTimeout(t *testing.T) {
	f := newFakeStart(t)
	f.st.spawn = func(argv []string, logPath string) (int, <-chan error, error) {
		return 9, make(chan error), nil
	}
	err := f.st.start(cmdline{}, "/r")
	if err == nil || !strings.Contains(err.Error(), "did not register") || !strings.Contains(err.Error(), "no output") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartSpawnError(t *testing.T) {
	f := newFakeStart(t)
	f.st.spawn = func([]string, string) (int, <-chan error, error) { return 0, nil, errors.New("boom") }
	if err := f.st.start(cmdline{}, "/r"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestLogTailKeepsLastLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.log")
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	appendLog(t, p, b.String())
	got := startLogTail(p, 0, 3)
	if !strings.Contains(got, "line 29") || !strings.Contains(got, "line 27") || strings.Contains(got, "line 26") {
		t.Errorf("tail: %s", got)
	}
}

func TestIsJigdName(t *testing.T) {
	for arg, want := range map[string]bool{
		"jigd": true, "/usr/local/bin/jigd": true, "jigd.exe": true,
		"jig": false, "/usr/bin/jig": false, "jigdx": false,
	} {
		if got := isJigdName(arg); got != want {
			t.Errorf("isJigdName(%q) = %v, want %v", arg, got, want)
		}
	}
}
