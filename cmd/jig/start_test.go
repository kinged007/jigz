package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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
	t.Setenv("JIG_REGISTRY_DIR", t.TempDir())
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
	c, err := parseArgs([]string{"start", "--no-dashboard", "--host", "0.0.0.0", "--dashboard-port=9000", "--unite", "./x"})
	if err != nil || c.remote != "start" {
		t.Fatalf("parse: %+v %v", c, err)
	}
	inner, err := parseArgs(c.remoteArgs)
	if err != nil {
		t.Fatal(err)
	}
	got := startArgv(inner, "/abs/x")
	want := []string{"--no-dashboard", "--unite", "--dashboard-port=9000", "--host=0.0.0.0", "/abs/x"}
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
		f.registry = registryEntry{PID: 77, Dashboard: "http://127.0.0.1:7677"}
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

func TestRotateStartLogKeepsSmallFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.log")
	appendLog(t, p, "small\n")
	rotateStartLog(p)
	if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
		t.Errorf("small log rotated unexpectedly")
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "small\n" {
		t.Errorf("small log changed: %q %v", data, err)
	}
}

func TestRotateStartLogShiftsLargeFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.log")
	big := make([]byte, childLogMaxBytes+1024)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(p, big, 0o600); err != nil {
		t.Fatal(err)
	}
	appendLog(t, p+".1", "old1\n")
	rotateStartLog(p)
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("rotated log missing: %v", err)
	}
	if _, err := os.Stat(p + ".2"); err != nil {
		t.Fatalf("shifted log missing: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("original should move to .1, leaving no file behind")
	}
}

func TestStartConcurrentSecondSeesAlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JIG_REGISTRY_DIR", dir)
	var live atomic.Bool
	var spawns atomic.Int32
	logs := []string{filepath.Join(t.TempDir(), "r1.log"), filepath.Join(t.TempDir(), "r2.log")}
	mk := func(logPath string) starter {
		return starter{
			logPath: func(string) (string, error) { return logPath, nil },
			running: func(string) (registryEntry, error) {
				if live.Load() {
					return registryEntry{PID: 77}, nil
				}
				return registryEntry{}, errors.New("no loop is running")
			},
			spawn: func([]string, string) (int, <-chan error, error) {
				spawns.Add(1)
				time.Sleep(100 * time.Millisecond)
				live.Store(true)
				return 77, make(chan error), nil
			},
			budget: 5 * time.Second,
			out:    func(string, ...any) {},
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = mk(logs[i]).start(cmdline{}, "/r")
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := spawns.Load(); n != 1 {
		t.Errorf("spawned %d times, want 1", n)
	}
}
