package supervise

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/source"
)

// sleeper returns a spawn that runs a shell command, so a test can
// exercise a child that lives, fails, or ignores SIGTERM.
func sleeper(script string) func(source.Loop) *exec.Cmd {
	return func(source.Loop) *exec.Cmd { return exec.Command("/bin/sh", "-c", script) }
}

func loop(project, workflow, hash string) source.Loop {
	return source.Loop{Project: project, Workflow: workflow, Root: "/tmp/supervise-test", Hash: hash}
}

// pidAlive reports whether pid is still running, so a teardown test can
// prove the child is gone rather than trusting the return.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func newTest(t *testing.T, script string, keys ...string) *Supervisor {
	t.Helper()
	s := New(sleeper(script))
	for _, k := range keys {
		project, wf, _ := strings.Cut(k, ":")
		s.Update(loop(project, wf, "h1"))
	}
	t.Cleanup(s.StopAll)
	return s
}

func child(s *Supervisor, key string) Child {
	for _, c := range s.Snapshot() {
		if c.Key == key {
			return c
		}
	}
	return Child{}
}

func waitState(t *testing.T, s *Supervisor, key, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.State(key) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s state = %q, want %q", key, s.State(key), want)
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("pid %d is still running", pid)
}

func waitFile(t *testing.T, path, why string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// A shell redirect creates the file before it writes, so an empty
		// file is not yet the content.
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(why)
	return nil
}

func TestStartRecordsTheHashTheChildStartedWith(t *testing.T) {
	s := newTest(t, "sleep 30", "acme/w:issues")
	key := Key("acme/w", "issues")
	if err := s.Start(key); err != nil {
		t.Fatal(err)
	}
	c := child(s, key)
	if c.State != StateRunning || c.PID == 0 || c.StartedHash != "h1" {
		t.Fatalf("child = %+v, want running with hash h1", c)
	}
	s.Update(loop("acme/w", "issues", "h2"))
	c = child(s, key)
	if c.PID == 0 || c.StartedHash != "h1" || c.Loop.Hash != "h2" {
		t.Errorf("after Update: %+v, want the running child kept with its start hash h1 and the loop at h2", c)
	}
	if err := s.Start(key); err != nil {
		t.Fatal(err)
	}
	if got := child(s, key).PID; got != c.PID {
		t.Errorf("a second Start replaced the child (%d -> %d)", c.PID, got)
	}
}

func TestChildFailureIsAttributedToItsLoop(t *testing.T) {
	s := newTest(t, "echo boom; exit 3", "acme/w:issues", "acme/w:prs")
	if err := s.Start(Key("acme/w", "issues")); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Key("acme/w", "issues"), StateFailed)
	c := child(s, Key("acme/w", "issues"))
	if !c.Exited || c.Err != "exit status 3" {
		t.Errorf("child = %+v, want exited with exit status 3", c)
	}
	if got := s.State(Key("acme/w", "prs")); got != StateStopped {
		t.Errorf("other loop state = %q, want stopped", got)
	}
}

func TestRequestedStopIsNotAFailure(t *testing.T) {
	s := newTest(t, "sleep 30", "acme/w:issues")
	key := Key("acme/w", "issues")
	if err := s.Start(key); err != nil {
		t.Fatal(err)
	}
	pid := child(s, key).PID
	if err := s.Stop(key); err != nil {
		t.Fatal(err)
	}
	c := child(s, key)
	if c.State != StateStopped || c.Exited || c.Err != "" || c.StartedHash != "" {
		t.Errorf("child = %+v, want stopped with no error", c)
	}
	waitGone(t, pid)
}

func TestUnknownLoopIsAnError(t *testing.T) {
	s := newTest(t, "sleep 30")
	if err := s.Start("a/b/c"); err == nil {
		t.Error("Start on an unknown loop = nil error")
	}
	if err := s.Stop("a/b/c"); err == nil {
		t.Error("Stop on an unknown loop = nil error")
	}
	if err := s.Signal("a/b/c", syscall.SIGHUP); err == nil {
		t.Error("Signal on an unknown loop = nil error")
	}
}

func TestRemoveStopsAndForgetsTheLoop(t *testing.T) {
	s := newTest(t, "sleep 30", "acme/w:issues", "acme/w:prs")
	key := Key("acme/w", "issues")
	if err := s.Start(key); err != nil {
		t.Fatal(err)
	}
	pid := child(s, key).PID
	s.Remove(key)
	if got := strings.Join(s.Keys(), ","); got != "acme/w/prs" {
		t.Errorf("keys = %q, want acme/w/prs", got)
	}
	waitGone(t, pid)
}

// TestStubbornHelper is re-executed as a child that ignores SIGTERM and
// starts a grandchild in its own process group, as the loop child starts
// the engine.
func TestStubbornHelper(t *testing.T) {
	if os.Getenv("JIG_TEST_STUBBORN") != "1" {
		t.Skip("helper; only runs when re-executed as a child")
	}
	signal.Ignore(syscall.SIGTERM)
	gc := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 1; done")
	if err := gc.Start(); err != nil {
		os.Exit(1)
	}
	fmt.Println("stubborn-ready", gc.Process.Pid)
	for {
		time.Sleep(time.Hour)
	}
}

// A child that ignores SIGTERM is killed with its whole process group
// after the grace period, so its engine does not outlive it.
func TestStopKillsTheGroupOfAChildThatIgnoresSIGTERM(t *testing.T) {
	const grace = time.Second
	s := New(func(source.Loop) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestStubbornHelper")
		cmd.Env = append(os.Environ(), "JIG_TEST_STUBBORN=1")
		return cmd
	})
	s.Grace = grace
	s.Update(loop("acme/w", "issues", "h"))
	key := Key("acme/w", "issues")
	if err := s.Start(key); err != nil {
		t.Fatal(err)
	}
	var grandchild int
	deadline := time.Now().Add(10 * time.Second)
	for grandchild == 0 && time.Now().Before(deadline) {
		for _, l := range child(s, key).Output {
			if v, ok := strings.CutPrefix(l, "stubborn-ready "); ok {
				grandchild, _ = strconv.Atoi(v)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatal("the helper never came up")
	}
	pid := child(s, key).PID
	start := time.Now()
	s.StopAll()
	if elapsed := time.Since(start); elapsed < grace {
		t.Errorf("StopAll returned after %s, want it to wait out the %s grace", elapsed, grace)
	}
	waitGone(t, pid)
	waitGone(t, grandchild)
}

// The budget is not a free parameter: the engine can need 50s to drain
// its agents after SIGTERM, and killing it sooner loses the runs.
func TestGracefulTimeoutCoversTheEngineDrain(t *testing.T) {
	const engineCeiling = 5*time.Second + 3*5*time.Second + 30*time.Second
	if gracefulTimeout <= engineCeiling {
		t.Errorf("gracefulTimeout = %s, the engine can need %s to drain", gracefulTimeout, engineCeiling)
	}
}

// Each child leads its own process group, so a child that signals its
// group on teardown does not reach the supervisor or its siblings.
func TestStoppingOneLoopLeavesTheOthersAlive(t *testing.T) {
	s := newTest(t, `trap 'kill -TERM -$$' TERM; while :; do sleep 0.2; done`, "a/b:one", "a/b:two")
	for _, k := range s.Keys() {
		if err := s.Start(k); err != nil {
			t.Fatal(err)
		}
	}
	other := child(s, "a/b/two").PID
	if err := s.Stop("a/b/one"); err != nil {
		t.Fatal(err)
	}
	if !pidAlive(other) {
		t.Error("stopping one loop killed its sibling")
	}
}

// The control requests travel as the signals the child listens for.
func TestControlRequestsAreSignals(t *testing.T) {
	dir := t.TempDir()
	rec, ready := filepath.Join(dir, "signal"), filepath.Join(dir, "ready")
	s := newTest(t, "trap 'echo usr1 >> "+rec+"' USR1\n"+
		"trap 'echo usr2 >> "+rec+"' USR2\n"+
		"trap 'echo hup >> "+rec+"' HUP\n"+
		"echo up > "+ready+"\n"+
		"while :; do sleep 0.05; done\n", "a/b:issues")
	key := Key("a/b", "issues")
	if err := s.Start(key); err != nil {
		t.Fatal(err)
	}
	waitFile(t, ready, "the child never came up")
	s.Update(loop("a/b", "issues", "h2"))
	for _, f := range []func(string) error{s.Reload, s.RecycleWhenIdle, s.StopWhenIdle} {
		if err := f(key); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := strings.Fields(string(waitFile(t, rec, "no signal reached the child"))); strings.Join(got, ",") != "hup,usr2,usr1" {
		t.Errorf("signals = %v, want hup, usr2, usr1", got)
	}
	if got := child(s, key).StartedHash; got != "h2" {
		t.Errorf("start hash after recycle = %q, want h2", got)
	}
	if err := s.Stop(key); err != nil {
		t.Fatal(err)
	}
	// A stopped loop has nothing to signal, which is what was asked.
	if err := s.StopWhenIdle(key); err != nil {
		t.Errorf("stop when idle on a stopped loop: %v", err)
	}
}

// A loop drained with StopWhenIdle that then exits is stopped, not
// exited on its own.
func TestDrainedExitIsAStop(t *testing.T) {
	s := newTest(t, "trap 'exit 0' USR1; while :; do sleep 0.05; done", "a/b:issues")
	key := Key("a/b", "issues")
	if err := s.Start(key); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := s.StopWhenIdle(key); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, key, StateStopped)
	if c := child(s, key); c.Exited {
		t.Errorf("child = %+v, want a requested stop", c)
	}
}

func TestChildOutputIsCaptured(t *testing.T) {
	s := newTest(t, "for i in $(seq 1 80); do echo line$i; done; exit 2", "a/b:issues")
	key := Key("a/b", "issues")
	if err := s.Start(key); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, key, StateFailed)
	var out []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out = child(s, key).Output
		if len(out) == outputLines && out[len(out)-1] == "line80" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("output = %d lines ending %v, want the last %d ending line80", len(out), out[max(0, len(out)-1):], outputLines)
}

func TestRingKeepsPartialLines(t *testing.T) {
	r := newRing(5)
	_, _ = r.Write([]byte("par"))
	if len(r.tail()) != 0 {
		t.Errorf("ring = %v, want nothing before the newline", r.tail())
	}
	_, _ = r.Write([]byte("tial\nnext\n"))
	if got := strings.Join(r.tail(), ","); got != "partial,next" {
		t.Errorf("ring = %q, want partial,next", got)
	}
}

func TestRingDropsOnlyTheOldestLines(t *testing.T) {
	r := newRing(3)
	_, _ = r.Write([]byte("a\nb\nc\nd\ne\n"))
	if got := strings.Join(r.tail(), ","); got != "c,d,e" {
		t.Errorf("ring = %q, want c,d,e", got)
	}
}
