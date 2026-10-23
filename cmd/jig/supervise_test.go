package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newTestSup builds a supervisor over a repo root with one loop, and no
// real children: the drain path is exercised through a fake engine, not
// through a real sortie.
func newTestSup(t *testing.T, root string, names ...string) *supervisor {
	t.Helper()
	// The registry is shared across every repo on the machine, and a
	// republish would leave this test's rows in the user's real one.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var defs []loopDef
	for _, n := range names {
		defs = append(defs, loopDef{name: n, file: "WORKFLOW." + n + ".md"})
	}
	return newSupervisor(root, "sortie", defs, make([]int, len(defs)))
}

// fakeLoop answers the state API with a fixed payload.
func fakeLoop(t *testing.T, body string, status int) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return portOf(t, srv.URL)
}

// portOf pulls the port out of a test server URL.
func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// running builds a state payload with n sessions in flight.
func running(n int) string {
	var b []byte
	b = append(b, `{"counts":{"running":`...)
	b = append(b, strconv.Itoa(n)...)
	b = append(b, `,"retrying":0},"running":[`...)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"display_identifier":"a/b#1"}`...)
	}
	return string(append(b, `]}`...))
}

// retrying builds a state payload with a pending retry and nothing running.
func retrying(n int) string {
	return `{"counts":{"running":0,"retrying":` + strconv.Itoa(n) + `},"running":[]}`
}

func TestALoopWithNothingInFlightIsIdle(t *testing.T) {
	idle, known := loopQuiet(fakeLoop(t, running(0), 200))
	if !known || !idle {
		t.Errorf("an empty loop reported idle=%v known=%v, want both true", idle, known)
	}
}

func TestALoopWithASessionIsNotIdle(t *testing.T) {
	if idle, known := loopQuiet(fakeLoop(t, running(2), 200)); !known || idle {
		t.Errorf("a loop with two sessions reported idle=%v known=%v, want idle=false", idle, known)
	}
}

// A pending retry means the loop will start that run by itself. Calling
// it idle would cancel work the loop was about to do on its own.
func TestAPendingRetryIsNotIdle(t *testing.T) {
	if idle, known := loopQuiet(fakeLoop(t, retrying(1), 200)); !known || idle {
		t.Errorf("a loop with a pending retry reported idle=%v known=%v, want idle=false", idle, known)
	}
}

func TestARetryingEntryIsNotIdle(t *testing.T) {
	body := `{"counts":{"running":0,"retrying":0},"running":[],"retrying":[{"issue_id":"1"}]}`
	if idle, known := loopQuiet(fakeLoop(t, body, 200)); !known || idle {
		t.Errorf("a loop with a retrying entry reported idle=%v known=%v, want idle=false", idle, known)
	}
}

// An engine that does not answer is unknown, not idle. Reading a
// connection failure as quiet would stop a loop mid-run every time the
// engine was briefly busy.
func TestAnUnreachableLoopIsUnknownNotIdle(t *testing.T) {
	// A port nothing listens on: the dial fails.
	if idle, known := loopQuiet(1); known || idle {
		t.Errorf("an unreachable loop reported idle=%v known=%v, want both false", idle, known)
	}
	if idle, known := loopQuiet(0); known || idle {
		t.Errorf("a loop with no server reported idle=%v known=%v, want both false", idle, known)
	}
}

func TestAnErrorResponseIsUnknownNotIdle(t *testing.T) {
	if idle, known := loopQuiet(fakeLoop(t, `{}`, 503)); known || idle {
		t.Errorf("a 503 reported idle=%v known=%v, want both false", idle, known)
	}
}

// A loop nobody asked to stop has died on its own, and the run goes down
// with it. This is the behaviour Jigz reads to attribute a failure, so
// it has to survive the rewrite of the reaper.
func TestALoopThatDiesOnItsOwnEndsTheRun(t *testing.T) {
	sup := newTestSup(t, t.TempDir(), "plan")
	sup.procs["plan"] = &loopProc{
		def:   loopDef{name: "plan", file: "WORKFLOW.plan.md"},
		cmd:   &exec.Cmd{},
		since: time.Now(),
	}
	code, done := sup.onExit(exitEvent{loop: "plan", err: fmt.Errorf("boom")})
	if !done || code != 1 {
		t.Errorf("an unexpected exit gave code=%d done=%v, want 1 and true", code, done)
	}
}

// The bug the per-loop reaper exists to fix: a loop that was stopped on
// purpose is not a crash, and must not take its siblings with it.
func TestALoopThatWasAskedToStopDoesNotEndTheRun(t *testing.T) {
	sup := newTestSup(t, t.TempDir(), "plan", "build")
	for _, n := range []string{"plan", "build"} {
		sup.procs[n] = &loopProc{
			def: loopDef{name: n, file: "WORKFLOW." + n + ".md"},
			cmd: &exec.Cmd{}, stopping: true, since: time.Now(),
		}
	}
	if _, done := sup.onExit(exitEvent{loop: "plan", err: fmt.Errorf("terminated")}); done {
		t.Error("a deliberate stop ended the run")
	}
	if _, ok := sup.procs["plan"]; ok {
		t.Error("the stopped loop is still registered")
	}
	if _, ok := sup.procs["build"]; !ok {
		t.Error("the sibling loop was removed with it")
	}
}

// Draining stops each loop on its own, so a loop that is quiet is signalled
// while a busy sibling is left alone.
func TestDrainStopsQuietLoopsAndLeavesBusyOnes(t *testing.T) {
	quiet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(running(0)))
	}))
	defer quiet.Close()
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(running(1)))
	}))
	defer busy.Close()

	root := t.TempDir()
	sup := newTestSup(t, root, "plan", "build")
	signalled := map[string]bool{}
	add := func(name string, port int) {
		cmd := exec.Command("/bin/sh", "-c", "exec sleep 30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
		sup.procs[name] = &loopProc{
			def: loopDef{name: name, file: "WORKFLOW." + name + ".md"},
			cmd: cmd, port: port, stopping: true, drain: true,
			since: time.Now().Add(-time.Hour),
		}
		signalled[name] = false
	}
	add("plan", portOf(t, quiet.URL))
	add("build", portOf(t, busy.URL))

	if _, done := sup.drainStep(); done {
		t.Fatal("the run ended while loops were still draining")
	}
	if !sup.procs["plan"].stopping || sup.procs["plan"].drain {
		t.Error("the quiet loop was not signalled")
	}
	if sup.procs["build"].drain != true {
		t.Error("the busy loop was signalled anyway")
	}
}

// A loop that has just started has not reported a run yet. Judging it
// idle would stop a loop before it was ever given the chance to work.
func TestAFreshlyStartedLoopIsNotJudgedIdle(t *testing.T) {
	root := t.TempDir()
	sup := newTestSup(t, root, "plan")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(running(0)))
	}))
	defer srv.Close()
	sup.procs["plan"] = &loopProc{
		def:  loopDef{name: "plan", file: "WORKFLOW.plan.md"},
		cmd:  exec.Command("/bin/sh", "-c", "exec sleep 30"),
		port: portOf(t, srv.URL), stopping: true, drain: true, since: time.Now(),
	}
	if _, done := sup.drainStep(); done {
		t.Error("a loop that had just started ended the run")
	}
	if sup.procs["plan"].drain != true {
		t.Error("a loop inside the settle window was signalled")
	}
}

// stop --when-idle ends the process once every loop has gone, and not
// before.
func TestAStopWhenIdleEndsTheRunOnlyWhenEmpty(t *testing.T) {
	root := t.TempDir()
	sup := newTestSup(t, root, "plan", "build")
	for _, n := range []string{"plan", "build"} {
		sup.procs[n] = &loopProc{
			def:      loopDef{name: n, file: "WORKFLOW." + n + ".md"},
			cmd:      exec.Command("/bin/sh", "-c", "exec sleep 30"),
			stopping: true, drain: true, since: time.Now(),
		}
	}
	if _, done := sup.onSignal(sigStopIdle); done {
		t.Error("the run ended with loops still draining")
	}
	if _, done := sup.onExit(exitEvent{loop: "plan"}); done {
		t.Error("the run ended with one loop left")
	}
	if _, done := sup.onExit(exitEvent{loop: "build"}); !done {
		t.Error("the run did not end once every loop had stopped")
	}
}

// A loop that is replaced must pick up the config now on disk, not the
// copy from boot. A supervisor rewrites config.yaml and the env file and
// then asks for a restart, and a stale read would keep the old settings
// with nothing to show for it.
func TestAReplacedLoopReadsTheConfigOnDisk(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, ".jigz", "config.yaml")
	mustWrite(t, cfgPath, "repo: owner/from-boot\n")
	sup := newTestSup(t, root, "plan")

	if _, _, _, err := sup.childEnv("plan"); err != nil {
		t.Fatal(err)
	}
	if got := envValue(t, root, "SORTIE_TRACKER_PROJECT"); got != "owner/from-boot" {
		t.Fatalf("at boot the env file has %q, want owner/from-boot", got)
	}

	mustWrite(t, cfgPath, "repo: owner/from-apply\n")
	if _, _, _, err := sup.childEnv("plan"); err != nil {
		t.Fatal(err)
	}
	if got := envValue(t, root, "SORTIE_TRACKER_PROJECT"); got != "owner/from-apply" {
		t.Errorf("after a rewrite the env file has %q, want owner/from-apply", got)
	}
}

func envValue(t *testing.T, root, key string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, ".jigz", ".env.loop"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// Signals must be distinct from SIGTERM, or an immediate stop would drain.
func TestTheDrainSignalsAreDistinctFromStop(t *testing.T) {
	for _, sig := range []syscall.Signal{sigStopIdle, sigRestartIdle} {
		if sig == syscall.SIGTERM || sig == syscall.SIGINT {
			t.Errorf("%v collides with an immediate stop", sig)
		}
	}
	if sigStopIdle == sigRestartIdle {
		t.Error("stop-when-idle and restart-when-idle share a signal")
	}
}

// A restart must also carry the per-loop filter, which lives in the same
// config and is applied to the environment rather than the file.
func TestAReplacedLoopCarriesItsFilter(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"),
		"repo: owner/name\nfilters:\n  plan: \"label:plan\"\n")
	sup := newTestSup(t, root, "plan")
	_, env, _, err := sup.childEnv("plan")
	if err != nil {
		t.Fatal(err)
	}
	// The value carries the assignee clause FilterFor appends, so this
	// checks the configured part is present rather than equal.
	const key = "SORTIE_TRACKER_QUERY_FILTER="
	got := ""
	for _, e := range env {
		if strings.HasPrefix(e, key) {
			got = e
		}
	}
	if !strings.HasPrefix(got, key+"label:plan") {
		t.Errorf("filter is %q, want it to carry label:plan", got)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The registry is what points a supervisor at a loop's port and its
// dashboard. A loop that is replaced leaves a window where it is
// published as nothing at all, so the start has to publish it again: if
// it does not, the entry stays empty until some unrelated event, and the
// loop's dashboard link is dead in the meantime.
func TestStartingALoopPublishesIt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "repo: owner/name\n")
	mustWrite(t, filepath.Join(root, ".jigz", "workflows", "WORKFLOW.plan.md"), "body\n")

	sup := newTestSup(t, root, "plan")
	// Port 0 disables the loop's own server, so the child starts without
	// binding anything and the test needs no engine.
	sup.ports["plan"] = 0
	sup.bin = writeScript(t, root, "sortie", "while true; do sleep 1; done\n")

	if err := sup.startOne("plan"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.shutdown)

	entry := registryEntryFor(t, root)
	if len(entry.Loops) != 1 {
		t.Fatalf("a started loop published %d entries, want 1", len(entry.Loops))
	}
	if entry.Loops[0].Loop != "plan" {
		t.Errorf("published loop %q, want plan", entry.Loops[0].Loop)
	}
	if entry.PID != os.Getpid() {
		t.Errorf("published pid %d, want this process %d", entry.PID, os.Getpid())
	}
}

// writeScript puts an executable stub in dir and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func registryEntryFor(t *testing.T, root string) registryEntry {
	t.Helper()
	path, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	return reg.Entries[root]
}
