package supervise

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/engine"
	config "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/plan"
)

// sleeper returns a spawn that runs a shell command, so a test can
// exercise a child that lives, fails, or ignores SIGTERM.
func sleeper(script string) func(dir string, env []string) *exec.Cmd {
	return func(dir string, env []string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}
}

func res(slug string) config.Resolved {
	return config.Resolved{Repo: config.Repo{Repo: slug}, Dir: "/tmp/supervise-test/" + strings.ReplaceAll(slug, "/", "_")}
}

// pidAlive reports whether pid is still running, so a teardown test can
// prove the child is gone rather than trusting the return.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func newTest(t *testing.T, script string) *Supervisor {
	t.Helper()
	s := New(plan.Plan{})
	s.spawn = sleeper(script)
	return s
}

// waitState waits for a repository to reach a state.
func waitState(t *testing.T, s *Supervisor, slug, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.State(slug) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s state = %q, want %q", slug, s.State(slug), want)
}

// waitUp waits for a repository to be running with a live pid. Start
// reports running just before cmd.Start returns, so a test that
// samples the pid straight afterwards can read zero.
func waitUp(t *testing.T, s *Supervisor, slug string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.State(slug) == StateRunning {
			for _, r := range s.Snapshot() {
				if r.Slug == slug && r.PID > 0 {
					return r.PID
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not come up with a pid (state %q)", slug, s.State(slug))
	return 0
}

// Prepare needs a real state directory and a real engine, which a unit
// test has neither of. Registering the entry directly exercises the
// same start/stop/watch paths the supervisor uses in production.
func register(s *Supervisor, r config.Resolved) {
	s.entry(r)
}

func TestStartRunsChildWithTheChildEnvironment(t *testing.T) {
	s := newTest(t, "sleep 30")
	r := config.Resolved{
		Repo:      config.Repo{Repo: "owner/alpha"},
		Dir:       "/tmp/supervise-test/owner_alpha",
		Workflows: []string{"plan", "build"},
		Agent:     config.Agent{MaxConcurrentAgents: iptr(3)},
	}
	register(s, r)
	if err := s.Start(r.Repo.Repo); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	pid := waitUp(t, s, r.Repo.Repo)
	if pid == 0 {
		t.Fatal("no pid")
	}
	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v, want one repository", snap)
	}
	if got := strings.Join(snap[0].Loops, ","); got != "plan,build" {
		t.Errorf("loops = %q, want plan,build", got)
	}
}

// A child that dies is reported against its own repository, with its
// reason, and does not disturb anything else.
func TestChildFailureIsAttributedToItsRepository(t *testing.T) {
	s := newTest(t, `echo "jig: no WORKFLOW.*.md named triage" >&2; exit 1`)
	r := res("owner/broken")
	register(s, r)
	if err := s.Start(r.Repo.Repo); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, r.Repo.Repo, StateFailed)
	snap := s.Snapshot()[0]
	if !strings.Contains(snap.Err, "exit status 1") {
		t.Errorf("err = %q, want the exit status", snap.Err)
	}
	if snap.PID != 0 {
		t.Errorf("pid = %d after exit, want 0", snap.PID)
	}
}

// A repository that failed validation cannot be started, so a start can
// never bypass the check.
func TestStartRefusesAnInvalidRepository(t *testing.T) {
	s := newTest(t, "sleep 30")
	r := res("owner/bad")
	register(s, r)
	s.setState(r.Repo.Repo, StateInvalid)
	s.mu.Lock()
	s.repos[r.Repo.Repo].diags = plan.Diagnostics{Repo: r.Repo.Repo, Errors: []string{"WORKFLOW.build.md: unknown state"}}
	s.mu.Unlock()
	err := s.Start(r.Repo.Repo)
	if err == nil || !strings.Contains(err.Error(), "unknown state") {
		t.Fatalf("Start = %v, want the validation error", err)
	}
	if s.State(r.Repo.Repo) == StateRunning {
		t.Error("an invalid repository was started")
	}
}

// Teardown must leave nothing behind: a loop that outlives its
// supervisor would keep claiming issues with nobody watching.
// A drain that ends must leave the fleet reported stopped, not failed:
// the children exit on their own, which is what was asked for. WaitAll
// returns when the last of them has gone.
func TestStopAllWhenIdleDrainsThenReportsStopped(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	// Exits on the drain signal instead of on SIGTERM, so a pass proves
	// the request went out as SIGUSR1 and not as a stop.
	s := newTest(t, "trap 'exit 0' USR1\n"+
		"echo up > "+ready+"\n"+
		"while true; do :; done\n")
	defer s.StopAll()
	for _, slug := range []string{"owner/a", "owner/b"} {
		register(s, res(slug))
		if err := s.Start(slug); err != nil {
			t.Fatal(err)
		}
	}
	waitFile(t, ready, "the children never came up")

	s.StopAllWhenIdle()
	done := make(chan struct{})
	go func() { s.WaitAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("WaitAll did not return; a child outlived the drain")
	}
	for _, r := range s.Snapshot() {
		if r.State != StateStopped {
			t.Errorf("%s state = %q (%s), want stopped", r.Slug, r.State, r.Err)
		}
	}
	// A fleet that is already down has nothing to wait for.
	s.WaitAll()
}

func TestStopAllTerminatesEveryChild(t *testing.T) {
	s := newTest(t, "sleep 30")
	for _, slug := range []string{"owner/a", "owner/b", "owner/c"} {
		register(s, res(slug))
		if err := s.Start(slug); err != nil {
			t.Fatal(err)
		}
	}
	for _, slug := range []string{"owner/a", "owner/b", "owner/c"} {
		waitUp(t, s, slug)
	}
	pids := map[int]bool{}
	for _, r := range s.Snapshot() {
		pids[r.PID] = true
	}
	if len(pids) != 3 {
		t.Fatalf("pids = %v, want 3 distinct children", pids)
	}

	done := make(chan struct{})
	go func() { s.StopAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("StopAll did not return; a child outlived the stop")
	}
	for pid := range pids {
		if pidAlive(pid) {
			t.Errorf("pid %d still running after StopAll", pid)
		}
	}
	for _, r := range s.Snapshot() {
		if r.State == StateRunning {
			t.Errorf("%s still reported running", r.Slug)
		}
	}
}

// A child that ignores SIGTERM must still be gone when Stop returns.
// TestStubbornHelper is the child used by the kill-escalation test: it
// ignores SIGTERM and never returns on its own. Re-executing the test
// binary is what makes that deterministic — a shell's SIGTERM
// disposition varies, and a test that sometimes exercises the kill path
// and sometimes not proves nothing.
//
// It announces itself once the signal is ignored, so the parent never
// signals a child that has not armed yet: a Go binary needs tens of
// milliseconds to start, and an early SIGTERM would kill it outright
// and make this test pass for the wrong reason.
func TestStubbornHelper(t *testing.T) {
	if os.Getenv("JIG_TEST_STUBBORN") != "1" {
		t.Skip("helper; only runs when re-executed as a child")
	}
	signal.Ignore(syscall.SIGTERM)
	fmt.Println("stubborn-ready")
	// A sleep loop, not select{}: the runtime's deadlock detector would
	// panic and exit the process when every goroutine is asleep.
	for {
		time.Sleep(time.Hour)
	}
}

// waitOutput waits for a line to appear in a repository's captured
// output.
func waitOutput(t *testing.T, s *Supervisor, slug, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range s.Snapshot() {
			if r.Slug != slug {
				continue
			}
			for _, l := range r.Output {
				if strings.Contains(l, want) {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never reported %q", slug, want)
}

func TestStopKillsAChildThatIgnoresSIGTERM(t *testing.T) {
	s := newTest(t, "")
	// The real budget is sized for the engine's own drain, so a test
	// proving the escalation would wait a minute for it. A short budget
	// exercises the same path.
	const grace = 2 * time.Second
	s.grace = grace
	s.spawn = func(dir string, env []string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestStubbornHelper")
		cmd.Env = append(os.Environ(), "JIG_TEST_STUBBORN=1")
		return cmd
	}
	r := res("owner/stubborn")
	register(s, r)
	if err := s.Start(r.Repo.Repo); err != nil {
		t.Fatal(err)
	}
	probe := waitUp(t, s, r.Repo.Repo)
	// Only signal once the child says it is ignoring SIGTERM.
	waitOutput(t, s, r.Repo.Repo, "stubborn-ready")
	if err := syscall.Kill(probe, syscall.SIGTERM); err != nil {
		t.Fatalf("probe SIGTERM to %d: %v", probe, err)
	}
	time.Sleep(300 * time.Millisecond)
	if !pidAlive(probe) {
		t.Fatal("the child did not ignore SIGTERM; the kill path is untested")
	}

	start := time.Now()
	done := make(chan struct{})
	go func() { _ = s.Stop(r.Repo.Repo); close(done) }()
	select {
	case <-done:
	case <-time.After(grace + 15*time.Second):
		t.Fatal("Stop did not escalate to a kill")
	}
	if elapsed := time.Since(start); elapsed < grace {
		t.Errorf("Stop returned after %s, want it to wait out the %s grace period then kill", elapsed, grace)
	}
	// Give the kernel a moment to reap.
	for i := 0; i < 50 && pidAlive(probe); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if pidAlive(probe) {
		t.Errorf("pid %d survived a kill", probe)
	}
	if s.State(r.Repo.Repo) != StateStopped {
		t.Errorf("state = %q, want stopped", s.State(r.Repo.Repo))
	}
}

// A requested stop is not a failure, however the child exits.
func TestRequestedStopIsNotAFailure(t *testing.T) {
	s := newTest(t, "sleep 30")
	r := res("owner/clean")
	register(s, r)
	if err := s.Start(r.Repo.Repo); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, r.Repo.Repo, StateRunning)
	if err := s.Stop(r.Repo.Repo); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()[0]
	if snap.State != StateStopped {
		t.Errorf("state = %q, want stopped", snap.State)
	}
	if snap.Err != "" {
		t.Errorf("err = %q, want none after a requested stop", snap.Err)
	}
}

func TestStartOnAnUnknownRepositoryIsAnError(t *testing.T) {
	s := newTest(t, "sleep 30")
	if err := s.Start("owner/nope"); err == nil {
		t.Error("Start on an unknown repository = nil error, want an error")
	}
	if err := s.Stop("owner/nope"); err == nil {
		t.Error("Stop on an unknown repository = nil error, want an error")
	}
}

// The tail of a child's output is what explains a failure, so it must
// be captured and bounded. The output is drained from the pipe by a
// goroutine, so it lands shortly after the child is reaped rather than
// at the instant it exits; the test waits for it rather than sampling
// once and hoping.
func TestChildOutputIsCaptured(t *testing.T) {
	s := newTest(t, "for i in $(seq 1 80); do echo line$i; done; exit 2")
	r := res("owner/chatty")
	register(s, r)
	if err := s.Start(r.Repo.Repo); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, r.Repo.Repo, StateFailed)

	var out []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, snap := range s.Snapshot() {
			if snap.Slug == r.Repo.Repo {
				out = snap.Output
			}
		}
		if len(out) == outputLines && out[len(out)-1] == "line80" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(out) != outputLines {
		t.Errorf("kept %d lines, want the last %d", len(out), outputLines)
	}
	if len(out) > 0 && out[len(out)-1] != "line80" {
		t.Errorf("last line = %q, want line80", out[len(out)-1])
	}
}

// A partial line is held until its newline arrives, so a write split
// mid-line is not mistaken for two lines.
func TestRingKeepsPartialLines(t *testing.T) {
	r := newRing(5)
	if _, err := r.Write([]byte("par")); err != nil {
		t.Fatal(err)
	}
	if len(r.tail()) != 0 {
		t.Errorf("ring = %v, want nothing before the newline", r.tail())
	}
	if _, err := r.Write([]byte("tial\nnext\n")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.tail(), ","); got != "partial,next" {
		t.Errorf("ring = %q, want partial,next", got)
	}
}

func TestRingDropsOnlyTheOldestLines(t *testing.T) {
	r := newRing(3)
	if _, err := r.Write([]byte("a\nb\nc\nd\ne\n")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.tail(), ","); got != "c,d,e" {
		t.Errorf("ring = %q, want c,d,e", got)
	}
}

func iptr(v int) *int { return &v }

// The gate that matters: a repository whose workflows did not validate
// must never be started, however the fleet is brought up.
func TestStartAllSkipsInvalidRepositories(t *testing.T) {
	s := newTest(t, "sleep 30")
	defer s.StopAll()
	for _, slug := range []string{"owner/good", "owner/bad"} {
		register(s, res(slug))
	}
	s.mu.Lock()
	s.repos["owner/bad"].state = StateInvalid
	s.repos["owner/bad"].diags = plan.Diagnostics{
		Repo: "owner/bad", Errors: []string{"WORKFLOW.build.md: handoff_state is not an active state"},
	}
	s.mu.Unlock()

	s.StartAll()
	if got := s.State("owner/good"); got != StateRunning {
		t.Errorf("valid repository state = %q, want running", got)
	}
	if got := s.State("owner/bad"); got != StateInvalid {
		t.Errorf("invalid repository state = %q, want invalid", got)
	}
	for _, r := range s.Snapshot() {
		if r.Slug == "owner/bad" && r.PID != 0 {
			t.Errorf("invalid repository was started with pid %d", r.PID)
		}
		if r.Slug == "owner/good" && r.PID == 0 {
			t.Error("valid repository has no pid")
		}
	}
}

// A real Jigz child shares the supervisor's process group unless
// Jigz puts it in its own. When it does, the child's teardown, which
// signals its process group, reaches every sibling and the supervisor
// itself: stopping one repository killed the whole fleet. This
// reproduces that shape exactly.
func TestStoppingOneRepositoryLeavesTheFleetAlive(t *testing.T) {
	// The child answers SIGTERM by signalling its whole process group,
	// which is what Jigz's teardown does.
	const groupSignaller = `trap 'kill -TERM -$$' TERM; while :; do sleep 0.2; done`
	s := newTest(t, groupSignaller)
	defer s.StopAll()

	slugs := []string{"owner/repo0", "owner/repo1", "owner/repo2"}
	for _, slug := range slugs {
		register(s, res(slug))
		if err := s.Start(slug); err != nil {
			t.Fatalf("Start(%s): %v", slug, err)
		}
	}
	pids := make([]int, len(slugs))
	for i, slug := range slugs {
		pids[i] = waitUp(t, s, slug)
	}

	// Every child must lead its own group, distinct from this process's,
	// or a group signal from it lands on the supervisor.
	self := syscall.Getpgrp()
	for i, pid := range pids {
		if got := pgidOf(pid); got == self {
			t.Errorf("%s (pid %d) shares the supervisor's process group %d", slugs[i], pid, self)
		}
	}

	if err := s.Stop(slugs[0]); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitState(t, s, slugs[0], StateStopped)

	// The siblings must survive, and the supervisor must still work.
	for i := 1; i < len(slugs); i++ {
		if !pidAlive(pids[i]) {
			t.Errorf("%s died when %s was stopped", slugs[i], slugs[0])
		}
	}
	if err := s.Stop(slugs[1]); err != nil {
		t.Errorf("Stop after a group signal: %v", err)
	}
}

// A re-read only reaches a running child by replacing it: the child
// reads its environment and its generated files once, at exec. Without
// that, editing workflows: in the config re-validates against settings
// the child never picks up.
func TestPrepareRestartsARunningChildWhoseConfigMoved(t *testing.T) {
	dir := t.TempDir()
	w := filepath.Join(dir, ".sortie", "workflows")
	if err := os.MkdirAll(w, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"plan", "build"} {
		body := "---\npolling:\n  interval_ms: 60000\n\nagent:\n  kind: pi\n  max_turns: 5\n  max_concurrent_agents: 1\n\npi:\n  model: \"\"\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(w, "WORKFLOW."+n+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// setup and validate are the only binaries Prepare drives; both are
	// stubs, so the test needs no engine.
	dirBin := t.TempDir()
	loop := stubBin(t, dirBin, "loop", "#!/bin/sh\nexit 0\n")
	engine := stubBin(t, dirBin, "engine", "#!/bin/sh\necho '{\"valid\":true}'\n")

	s := New(plan.Plan{LoopBin: loop, SortieBin: engine})
	s.spawn = sleeper("sleep 30")
	defer s.StopAll()

	slug := "owner/alpha"
	scoped := config.Resolved{Repo: config.Repo{Repo: slug}, Dir: dir, Workflows: []string{"plan", "build"}}
	s.Prepare([]config.Resolved{scoped})
	if err := s.Start(slug); err != nil {
		t.Fatal(err)
	}
	first := waitUp(t, s, slug)

	all := scoped
	all.Workflows = nil
	s.Prepare([]config.Resolved{all})

	second := waitUp(t, s, slug)
	if second == first {
		t.Errorf("pid %d unchanged: the child was not restarted for the new workflow selection", first)
	}
	waitGone(t, first)
	if got := strings.Join(s.Snapshot()[0].Loops, ","); got != "" {
		t.Errorf("loops = %q, want the empty selection to be reported", got)
	}
}

// A repository the config now disables must not keep its child: the
// entry says stopped-for-now and the fleet says it is off.
func TestPrepareStopsARunningChildThatBecameDisabled(t *testing.T) {
	s := newTest(t, "sleep 30")
	defer s.StopAll()
	slug := "owner/alpha"
	on := res(slug)
	register(s, on)
	if err := s.Start(slug); err != nil {
		t.Fatal(err)
	}
	pid := waitUp(t, s, slug)

	off := res(slug)
	off.Repo.Enabled = &[]bool{false}[0]
	s.Prepare([]config.Resolved{off})

	waitState(t, s, slug, StateDisabled)
	waitGone(t, pid)
}

func TestPrepareStopsAndForgetsARepositoryRemovedFromTheConfig(t *testing.T) {
	s := newTest(t, "sleep 30")
	defer s.StopAll()
	gone, kept := res("owner/gone"), res("owner/kept")
	off := &[]bool{false}[0]
	gone.Repo.Enabled, kept.Repo.Enabled = off, off
	s.Prepare([]config.Resolved{gone, kept})
	register(s, res("owner/running"))
	if err := s.Start("owner/running"); err != nil {
		t.Fatal(err)
	}
	pid := waitUp(t, s, "owner/running")

	s.Prepare([]config.Resolved{kept})

	if got := s.Snapshot(); len(got) != 1 || got[0].Slug != "owner/kept" {
		t.Errorf("repositories after removal = %+v, want only owner/kept", got)
	}
	waitGone(t, pid)
}

// stubBin writes an executable shell script and returns its path.
func stubBin(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// waitGone waits for a replaced child to leave the process table, which
// happens once the supervisor has reaped it.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("the replaced child (pid %d) is still running", pid)
}

// pgidOf reads a process's process group id.
func pgidOf(pid int) int {
	out, err := exec.Command("ps", "-o", "pgid=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return -1
	}
	var pgid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &pgid); err != nil {
		return -1
	}
	return pgid
}

// The budget is not a free parameter. The engine cancels each running
// agent on SIGTERM and then waits for every one to exit, persisting each
// result so the run is retried. Its own ceiling is agent.stop_grace_ms
// (default 5s) plus three 5s drain periods plus a 30s teardown margin,
// which is 50s. A budget below that kills the engine mid-drain, leaving
// the run recorded as still Running with no clean requeue.
func TestGracefulTimeoutCoversTheEngineDrain(t *testing.T) {
	const stopGrace, drainGrace, teardownMargin = 5 * time.Second, 3 * 5 * time.Second, 30 * time.Second
	engineCeiling := stopGrace + drainGrace + teardownMargin
	if gracefulTimeout <= engineCeiling {
		t.Errorf("gracefulTimeout = %s, but the engine can need %s to drain cleanly; "+
			"a child is killed before it can persist and requeue its running agents",
			gracefulTimeout, engineCeiling)
	}
}

// A repository marked to stop when idle has to survive while an agent is
// working, and stop on the first pass where nothing is.
// The engine's state API reports the model but not the adapter, so the
// page's agent name comes from the config. A per-workflow override has to
// win over the repository value, the same way the knob itself resolves.
func TestAgentsReportsTheAdapterPerLoop(t *testing.T) {
	repo := config.Repo{
		Repo:      "a/b",
		Agent:     config.Agent{Kind: "pi"},
		Workflows: []string{"plan", "build"},
		Overrides: map[string]config.Workflow{
			"build": {Agent: config.Agent{Kind: "claude-code"}},
		},
	}
	all, err := (&config.Config{Repos: []config.Repo{repo}}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	got := agentKinds(all[0])
	if got["plan"] != "pi" {
		t.Errorf("plan = %q, want the repository value pi", got["plan"])
	}
	if got["build"] != "claude-code" {
		t.Errorf("build = %q, want the override claude-code", got["build"])
	}
}

// An unset kind stays absent rather than becoming an empty string, so the
// page shows the engine's default instead of a blank cell.
func TestAgentsOmitsAnUnsetKind(t *testing.T) {
	repo := config.Repo{Repo: "a/b", Workflows: []string{"plan"}}
	all, err := (&config.Config{Repos: []config.Repo{repo}}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got := agentKinds(all[0]); got != nil {
		t.Errorf("agentKinds = %v, want nothing for an unset kind", got)
	}
	// A repository whose loops were not pinned has no loop names to map.
	if got := agentKinds(config.Resolved{}); got != nil {
		t.Errorf("agentKinds = %v, want nothing with no loops", got)
	}
}

// Jigz does not decide when a loop stops any more: the Jigz
// run owns its loops and is what watches them, so the request travels as
// the signal that run listens for. Getting the signal wrong would either
// stop a repo mid-run or leave it waiting that nothing watches.
// newProbeSup starts a supervisor over a real state directory whose child
// records the control signals it receives. Both drain signals terminate a
// process by default, so only a child that traps them can say which one
// arrived.
func newProbeSup(t *testing.T) (*Supervisor, string, string) {
	t.Helper()
	dir := t.TempDir()
	w := filepath.Join(dir, ".sortie", "workflows")
	if err := os.MkdirAll(w, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"plan", "build"} {
		body := "---\npolling:\n  interval_ms: 60000\n\nagent:\n  kind: pi\n  max_turns: 5\n  max_concurrent_agents: 1\n\npi:\n  model: \"\"\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(w, "WORKFLOW."+n+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dirBin := t.TempDir()
	rec := filepath.Join(t.TempDir(), "signal")
	ready := filepath.Join(t.TempDir(), "ready")
	// The plan drives setup through a stub that returns at once; the child
	// under supervision is the probe, which is a different binary on
	// purpose so it can trap the signals forever.
	loop := stubBin(t, dirBin, "loop", "#!/bin/sh\nexit 0\n")
	probe := stubBin(t, dirBin, "probe", "#!/bin/sh\n"+
		"trap 'echo usr1 > "+rec+"' USR1\n"+
		"trap 'echo usr2 > "+rec+"' USR2\n"+
		// Announced once the traps exist: a signal sent before that is
		// handled by the default action, which is to terminate, and the
		// test would be measuring that instead.
		"echo up > "+ready+"\n"+
		"while true; do :; done\n")
	engine := stubBin(t, dirBin, "engine", "#!/bin/sh\necho '{\"valid\":true}'\n")

	s := New(plan.Plan{LoopBin: loop, SortieBin: engine})
	s.spawn = sleeper("exec " + probe)
	s.Prepare([]config.Resolved{{
		Repo: config.Repo{Repo: "a/b"}, Dir: dir, Workflows: []string{"plan"},
	}})
	return s, rec, ready
}

// startProbe brings the child up and waits until it can be signalled: a
// signal sent before the traps are installed is handled by the default
// action, which terminates, and the test would be measuring that instead.
func startProbe(t *testing.T, s *Supervisor, slug, ready string) {
	t.Helper()
	if err := s.Start(slug); err != nil {
		t.Fatal(err)
	}
	waitFile(t, ready, "the child never came up")
}

func TestTheStopRequestsAreSignalsNotDecisions(t *testing.T) {
	s, rec, ready := newProbeSup(t)
	defer s.StopAll()
	startProbe(t, s, "a/b", ready)
	if err := s.StopWhenIdle("a/b"); err != nil {
		t.Fatal(err)
	}
	if got := signalReceived(t, rec); got != sigStopIdle {
		t.Errorf("stop when idle sent %v, want SIGUSR1", got)
	}
	_ = os.Remove(rec)
	if err := s.RecycleWhenIdle("a/b"); err != nil {
		t.Fatal(err)
	}
	if got := signalReceived(t, rec); got != sigRestartIdle {
		t.Errorf("recycle when idle sent %v, want SIGUSR2", got)
	}
}

// A repository that is already stopped has nothing to signal, and
// stopped is what the request asked for. A second request is the case
// that matters: the operator pressed stop when idle, the loops finished,
// and pressed it again.
func TestAStoppedRepositoryNeedsNoSignal(t *testing.T) {
	s, _, ready := newProbeSup(t)
	defer s.StopAll()
	slug := "a/b"
	startProbe(t, s, slug, ready)
	if err := s.Stop(slug); err != nil {
		t.Fatal(err)
	}
	if err := s.StopWhenIdle(slug); err != nil {
		t.Errorf("asking a stopped repository to stop when idle: %v", err)
	}
	if err := s.RecycleWhenIdle(slug); err != nil {
		t.Errorf("asking a stopped repository to recycle when idle: %v", err)
	}
}

// The two signals must not collide with SIGTERM, or an immediate stop
// would drain instead of stopping.
func TestTheDrainSignalsAreDistinctFromStop(t *testing.T) {
	if sigStopIdle == syscall.SIGTERM || sigRestartIdle == syscall.SIGTERM {
		t.Error("a drain signal collides with an immediate stop")
	}
	if sigStopIdle == sigRestartIdle {
		t.Error("stop-when-idle and recycle-when-idle share a signal")
	}
}

// waitFile blocks until path exists and has content.
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

// signalReceived waits for the probe child to record a control signal and
// returns which one it was.
func signalReceived(t *testing.T, rec string) syscall.Signal {
	t.Helper()
	switch strings.TrimSpace(string(waitFile(t, rec, "no control signal reached the child"))) {
	case "usr1":
		return sigStopIdle
	case "usr2":
		return sigRestartIdle
	}
	t.Fatal("the child recorded a signal this test does not send")
	return 0
}

func TestMain(m *testing.M) {
	engine.SetDownload(func(engine.Spec) (string, error) {
		return "", errors.New("engine download disabled in unit tests")
	})
	os.Exit(m.Run())
}

// The child loop is pinned to the engine the engine package resolves for the
// configured repository and version.
func TestRunLoopPinsTheResolvedEngine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("JIG_ENGINE_BIN", "")
	t.Setenv("JIG_ENGINE_REPO", "sortie-ai/sortie")
	t.Setenv("JIG_ENGINE_VERSION", "v1.29.0")
	spec, err := engine.Configured()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(engine.DataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	want := spec.CachePath(engine.DataDir())
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(plan.Plan{})
	cmd := s.runLoop(t.TempDir(), nil)
	if !slices.Contains(cmd.Env, "JIG_ENGINE_BIN="+want) {
		t.Errorf("child env lacks JIG_ENGINE_BIN=%s", want)
	}
}
