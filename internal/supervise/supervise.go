// Package supervise owns the Jigz child processes: one per
// enabled repository, each started only after its generated
// configuration has been validated.
//
// It is also where a child's failure is attributed. A Jigz
// process that exits non-zero — a rejected workflow name, a missing
// engine, a port it could not bind — is reported against the
// repository it belonged to, with the tail of its output, and the rest
// of the fleet keeps running. One broken repository must not take down
// the others.
package supervise

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	config "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/plan"
)

// gracefulTimeout is how long a child gets to exit after SIGTERM before
// it is killed.
//
// It has to cover the engine's own drain, not merely its exit. On SIGTERM
// the engine cancels each running agent and then waits for every one of
// them to finish, persisting each result so the run is retried rather
// than lost. That wait is bounded by agent.stop_grace_ms plus three
// drain periods plus a 30s teardown margin, which at the shipped
// defaults reaches 50s. Killing sooner truncates the drain: the run is
// left recorded as still Running in SQLite, and the clean
// HandleWorkerExit that would have requeued it never happens. The floor
// is 60s so the default configuration can always finish; a deployment
// that raises agent.stop_grace_ms should raise this too.
const gracefulTimeout = 60 * time.Second

// outputLines is how much of a child's output is kept for the UI. The
// tail is what explains a failure; the whole stream is a log file's job.
const outputLines = 50

// Repository states as the UI reports them.
const (
	StateStopped  = "stopped"
	StateRunning  = "running"
	StateFailed   = "failed"
	StateInvalid  = "invalid"
	StateDisabled = "disabled"
)

// Repo is one repository's supervisable state, as the UI sees it.
type Repo struct {
	Slug  string   `json:"repo"`
	Dir   string   `json:"dir"`
	Loops []string `json:"loops"`
	State string   `json:"state"`
	// Draining is set while the repository is waiting to become idle
	// before it is stopped.
	Draining bool `json:"draining,omitempty"`
	// Agents maps each loop to the agent adapter it runs, absent where the
	// setting is unset and the engine's own default applies.
	Agents map[string]string `json:"agents,omitempty"`
	Diags  plan.Diagnostics  `json:"diagnostics"`
	PID    int               `json:"pid,omitempty"`
	Since  time.Time         `json:"since,omitempty"`
	Err    string            `json:"error,omitempty"`
	Output []string          `json:"output,omitempty"`
}

type entry struct {
	res   config.Resolved
	state string
	diags plan.Diagnostics
	cmd   *exec.Cmd
	done  chan struct{}
	since time.Time
	err   string
	out   *ring
	// wanted is whether this repository should be running. A child that
	// exits while it is false was asked to stop, and its exit is not a
	// failure.
	wanted bool
}

func (e *entry) pid() int {
	if e.cmd == nil || e.cmd.Process == nil {
		return 0
	}
	return e.cmd.Process.Pid
}

// Supervisor owns the children for one resolved config.
type Supervisor struct {
	plan plan.Plan
	// spawn builds a child. Tests replace it; the default runs
	// Jigz with no dashboard, because Jigz serves the combined
	// view.
	spawn func(dir string, env []string) *exec.Cmd

	mu    sync.Mutex
	repos map[string]*entry
	// settle is replaced by recycleIdle: a running repository whose
	// settings moved is handed back to its own run to replace, and this
	// says whether that run should wait for the loops to go quiet.
	recycleIdle bool

	// grace overrides the SIGTERM-to-SIGKILL budget. Zero means
	// gracefulTimeout; a test sets it low so it does not have to wait out
	// the real budget to prove the escalation.
	grace time.Duration
}

// New returns a supervisor that starts each repository with p's
// binaries.
func New(p plan.Plan) *Supervisor {
	s := &Supervisor{plan: p, repos: map[string]*entry{}}
	s.spawn = s.runLoop
	return s
}

// graceBudget is the SIGTERM-to-SIGKILL budget actually in force.
func (s *Supervisor) graceBudget() time.Duration {
	if s.grace > 0 {
		return s.grace
	}
	return gracefulTimeout
}

// Plan exposes the materialization plan.
func (s *Supervisor) Plan() plan.Plan { return s.plan }

// Prepare materializes and validates every repository, recording each
// one's diagnostics. It never returns early: one invalid repository
// must not hide the state of the rest.
//
// A running child whose configuration moved is restarted, and one that
// is now disabled is stopped: a child reads its environment and its
// generated files once, at exec, so without that a re-read would only
// re-validate settings the child never picked up.
//
// A repository that fails validation is reported through its
// diagnostics and its state, not through the returned error, which is
// reserved for nothing so far: every failure here is attributable to
// one repository.
func (s *Supervisor) Prepare(all []config.Resolved) {
	var moved []string
	for _, r := range all {
		e := s.entry(r)
		s.mu.Lock()
		// Compared before the entry takes the new value: the running
		// child is still the one started from the old one.
		changed := e.cmd != nil && !reflect.DeepEqual(e.res, r)
		e.res = r
		disabled := !r.Enabled()
		s.mu.Unlock()

		if disabled {
			if changed {
				_ = s.Stop(r.Repo.Repo)
			}
			s.setState(r.Repo.Repo, StateDisabled)
			continue
		}
		if err := s.prepareOne(e, r); err != nil {
			s.mu.Lock()
			e.state = StateFailed
			e.err = err.Error()
			s.mu.Unlock()
			continue
		}
		if changed {
			moved = append(moved, r.Repo.Repo)
		}
	}
	s.forgetRemoved(all)
	// After every repository is materialized: each replacement runs its
	// loops against the files this pass just wrote.
	idle := s.recycleIdle
	for _, slug := range moved {
		if idle {
			_ = s.RecycleWhenIdle(slug)
			continue
		}
		_ = s.Restart(slug)
	}
}

// forgetRemoved stops and drops every repository that is no longer in the
// config, so a removed project does not stay listed.
func (s *Supervisor) forgetRemoved(all []config.Resolved) {
	keep := map[string]bool{}
	for _, r := range all {
		keep[r.Repo.Repo] = true
	}
	for _, slug := range s.slugs() {
		if keep[slug] {
			continue
		}
		s.terminate(slug)
		s.mu.Lock()
		delete(s.repos, slug)
		s.mu.Unlock()
	}
}

// prepareOne runs the materialization steps for one repository and
// leaves its diagnostics and state in the entry.
//
// A repository that fails validation becomes StateInvalid, which is
// what stops it from being started. A child that is already running is
// left alone: it was validated when it started, and Prepare decides
// separately whether to replace it. Both facts are then visible at
// once, which is the honest report.
func (s *Supervisor) prepareOne(e *entry, r config.Resolved) error {
	if err := plan.Apply(r); err != nil {
		return err
	}
	if err := s.plan.Setup(r); err != nil {
		return err
	}
	if err := s.plan.ApplySettings(r); err != nil {
		return err
	}
	diags, err := s.plan.Validate(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e.diags = diags
	e.err = ""
	switch {
	case !diags.Valid:
		e.state = StateInvalid
	case e.state == StateInvalid || e.state == StateFailed || e.state == StateDisabled:
		e.state = StateStopped
	}
	return nil
}

// Start launches the child for one repository. It refuses a repository
// that failed validation, so a start can never bypass it.
func (s *Supervisor) Start(slug string) error {
	e, ok := s.lookup(slug)
	if !ok {
		return fmt.Errorf("no repository %q", slug)
	}
	s.mu.Lock()
	if e.state == StateInvalid {
		err := fmt.Errorf("%s did not validate: %s", slug, firstDiag(e.diags))
		s.mu.Unlock()
		return err
	}
	if e.cmd != nil {
		s.mu.Unlock()
		return nil
	}
	res := e.res
	env, err := res.Env()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	cmd := s.spawn(res.Dir, env)
	// Applied here rather than in runLoop so it holds for any spawn
	// implementation, including the stand-ins tests substitute.
	setChildGroup(cmd)
	if cmd.Env == nil {
		// The child passes its own environment on to every loop it
		// starts, so these reach the engine too.
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, env...)
	if e.out == nil {
		e.out = newRing(outputLines)
	}
	// A real file, not an io.Writer: exec passes a file descriptor
	// straight through and Wait returns as soon as the child exits.
	// Given a writer it copies through a pipe in a goroutine, and Wait
	// then blocks until that pipe reaches EOF — which a surviving
	// grandchild holding the write end prevents. Jigz starts one
	// engine child per loop, so a killed supervisor would otherwise
	// hang in Wait for as long as those grandchildren live.
	w, closeWrite, err := capture(e.out)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	cmd.Stdout, cmd.Stderr = w, w
	done := make(chan struct{})
	e.cmd, e.done, e.wanted, e.since, e.err = cmd, done, true, time.Now(), ""
	e.state = StateRunning
	if err := cmd.Start(); err != nil {
		closeWrite()
		e.cmd, e.done, e.wanted = nil, nil, false
		e.state = StateFailed
		e.err = err.Error()
		s.mu.Unlock()
		return err
	}
	// The parent must drop its own write end, or the drainer never sees
	// EOF even after every descendant has gone.
	closeWrite()
	s.mu.Unlock()
	go s.watch(e, cmd, done)
	return nil
}

// capture returns the write end of a pipe a child can write to, and
// drains the read end into r. The returned function closes the write
// end in the parent, and must be called after the child starts.
func capture(r *ring) (*os.File, func(), error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	go func() {
		defer pr.Close()
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				_, _ = r.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return pw, func() { _ = pw.Close() }, nil
}

// watch reaps one child and records how it ended. A non-zero exit while
// the repository is still wanted is a failure to report; an exit after
// a stop was requested is not.
func (s *Supervisor) watch(e *entry, cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	// Closed after the entry is updated: a waiter that restarts the
	// repository as soon as done closes must not find the old cmd still
	// registered, or Start does nothing and watch then marks it stopped.
	defer close(done)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.cmd != cmd {
		return // superseded by a newer child
	}
	e.cmd, e.done = nil, nil
	switch {
	case !e.wanted:
		e.state = StateStopped
	case err != nil:
		e.state = StateFailed
		e.err = exitReason(err)
	default:
		// Exited cleanly on its own. Jigz does this when one of
		// its loops dies, and the rest of the fleet is expected to keep
		// going, so this is reported rather than treated as an error.
		e.state = StateStopped
		e.err = "Jigz exited on its own"
	}
}

// Stop asks one repository's child to exit and waits for it.
func (s *Supervisor) Stop(slug string) error {
	if _, ok := s.lookup(slug); !ok {
		return fmt.Errorf("no repository %q", slug)
	}
	s.terminate(slug)
	return nil
}

// Restart stops a repository and starts it again, picking up whatever
// the configuration now says.
func (s *Supervisor) Restart(slug string) error {
	if err := s.Stop(slug); err != nil {
		return err
	}
	return s.Start(slug)
}

// StopAll terminates every child and waits for all of them. It runs on
// shutdown, where a surviving loop would keep claiming issues for a
// supervisor that is no longer there.
func (s *Supervisor) StopAll() {
	var wg sync.WaitGroup
	for _, slug := range s.slugs() {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			s.terminate(slug)
		}(slug)
	}
	wg.Wait()
}

// StopAllWhenIdle asks every child to stop each of its loops as that loop
// finishes what it is working on, then exit, and returns at once. The
// children decide when they are done, so the wait belongs to whoever is
// shutting the fleet down: pair this with WaitAll.
//
// Each child is marked no longer wanted first, so the exit that comes
// back is reported as the stop it was rather than as a failure.
func (s *Supervisor) StopAllWhenIdle() {
	s.mu.Lock()
	for _, e := range s.repos {
		e.wanted = false
	}
	s.mu.Unlock()
	for _, slug := range s.slugs() {
		_ = s.StopWhenIdle(slug)
	}
}

// WaitAll blocks until every child has exited. A repository that is not
// running has nothing to wait for, so it returns at once. Each round
// re-reads the entries, because a child can be replaced while an earlier
// one is still going and the replacement is the one that has to finish.
func (s *Supervisor) WaitAll() {
	for {
		s.mu.Lock()
		var waiting []chan struct{}
		for _, e := range s.repos {
			if e.done != nil {
				waiting = append(waiting, e.done)
			}
		}
		s.mu.Unlock()
		if len(waiting) == 0 {
			return
		}
		for _, d := range waiting {
			<-d
		}
	}
}

// slugs is every repository's name.
func (s *Supervisor) slugs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.repos))
	for slug := range s.repos {
		out = append(out, slug)
	}
	return out
}

// terminate signals a child and waits for it, escalating to a kill if
// it ignores the signal. It waits on the monitor's channel rather than
// reaping a second time, so the reap stays in one place.
func (s *Supervisor) terminate(slug string) {
	s.mu.Lock()
	e, ok := s.repos[slug]
	if !ok {
		s.mu.Unlock()
		return
	}
	e.wanted = false
	cmd, done := e.cmd, e.done
	if cmd != nil {
		e.state = StateStopped
	}
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		if done != nil {
			<-done
		}
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(s.graceBudget()):
		_ = cmd.Process.Kill()
		<-done
	}
}

// State returns a repository's current state, or "" if unknown.
func (s *Supervisor) State(slug string) string {
	// The entry is read under the lock rather than through lookup, which
	// takes it itself: e.state is written by watch() on another
	// goroutine, and reading it unlocked is a data race.
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.repos[slug]
	if !ok {
		return ""
	}
	return e.state
}

// StopWhenIdle asks one repository's Jigz to stop each of its loops
// as that loop finishes what it is working on, rather than cutting a run
// off mid-turn. It returns at once: a run can take many more minutes, and
// an HTTP request is not the place to wait for one.
//
// The decision is the child's, because the child is what watches the
// loops. Jigz signals and steps back.
func (s *Supervisor) StopWhenIdle(slug string) error {
	return s.signal(slug, sigStopIdle)
}

// RecycleWhenIdle asks one repository's Jigz to replace each of
// its loops as that loop goes idle, picking up the workflow and settings
// now on disk. The run stays up, so the repository keeps its pid and its
// registry entry, and each loop is replaced at a moment of its own rather
// than all at once.
func (s *Supervisor) RecycleWhenIdle(slug string) error {
	return s.signal(slug, sigRestartIdle)
}

// signal sends sig to one repository's running child.
func (s *Supervisor) signal(slug string, sig syscall.Signal) error {
	if !idleSignals {
		return errors.New("this platform has no idle signal, so stop-when-idle and recycle are unavailable")
	}
	s.mu.Lock()
	e, ok := s.repos[slug]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("no repository %q", slug)
	}
	cmd := e.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		// Nothing to signal: the repository is already stopped, and that
		// is the state the request asked for anyway.
		return nil
	}
	if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		// A child that exited after cmd was read is stopped, which is
		// the same outcome as the nil case above.
		return fmt.Errorf("signal %s: %w", sig, err)
	}
	return nil
}

// SetRecycleIdle chooses how a running repository is replaced when its
// settings move: waiting for each loop to go quiet, or stopping every loop
// at once. It is set before Prepare, which is the only thing that reads
// it, and applies to the whole pass.
func (s *Supervisor) SetRecycleIdle(idle bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recycleIdle = idle
}

// Snapshot returns every repository's state, in name order.
func (s *Supervisor) Snapshot() []Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Repo, 0, len(s.repos))
	for _, e := range s.repos {
		out = append(out, Repo{
			Slug:   e.res.Repo.Repo,
			Dir:    e.res.Dir,
			Loops:  e.res.Workflows,
			State:  e.state,
			Agents: agentKinds(e.res),
			Diags:  e.diags,
			PID:    e.pid(),
			Since:  e.since,
			Err:    e.err,
			Output: e.out.tail(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// StartAll starts every prepared, valid, enabled repository. One
// failure is recorded against its repository; the rest still start.
func (s *Supervisor) StartAll() {
	for _, e := range s.Snapshot() {
		if e.State != StateStopped {
			continue
		}
		// Start records its own error against the repository.
		_ = s.Start(e.Slug)
	}
}

// runLoop is the default child.
func (s *Supervisor) runLoop(dir string, env []string) *exec.Cmd {
	cmd := exec.Command(s.plan.LoopBinary(), "--no-dashboard", dir)
	// Pin the engine for the child. Jigz resolves it on its own and
	// normally reaches the same file, but Jigz just validated the
	// workflows against a specific binary; passing the path makes the loop
	// use exactly that one instead of a second, independent lookup that
	// could land somewhere else.
	if bin, err := s.plan.SortieBinPath(); err == nil && os.Getenv("JIG_ENGINE_BIN") == "" {
		env = append(env, "JIG_ENGINE_BIN="+bin)
	}
	cmd.Env = append(os.Environ(), env...)
	return cmd
}

func (s *Supervisor) entry(r config.Resolved) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.repos[r.Repo.Repo]
	if !ok {
		e = &entry{res: r, state: StateStopped, out: newRing(outputLines)}
		s.repos[r.Repo.Repo] = e
	}
	return e
}

func (s *Supervisor) lookup(slug string) (*entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.repos[slug]
	return e, ok
}

func (s *Supervisor) setState(slug, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.repos[slug]; ok {
		e.state = state
	}
}

func firstDiag(d plan.Diagnostics) string {
	if len(d.Errors) > 0 {
		return d.Errors[0]
	}
	return "no reason reported"
}

// exitReason describes a child's non-zero exit. The message itself is
// in the entry's output ring: ExitError.Stderr is only populated by
// Output(), and this child writes to a buffer instead.
func exitReason(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit status %d", ee.ExitCode())
	}
	return err.Error()
}

// ring keeps the last n lines written to it, for showing the tail of a
// child's output in the UI.
type ring struct {
	mu    sync.Mutex
	lines []string
	max   int
	part  string
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.part += string(p)
	for {
		i := strings.IndexByte(r.part, '\n')
		if i < 0 {
			break
		}
		r.push(r.part[:i])
		r.part = r.part[i+1:]
	}
	return len(p), nil
}

func (r *ring) push(line string) {
	if line == "" {
		return
	}
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

func (r *ring) tail() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// agentKinds reports the agent adapter each of a repository's loops runs
// with. A per-workflow override wins over the repository value, matching
// how the knob itself is resolved.
func agentKinds(r config.Resolved) map[string]string {
	if len(r.Workflows) == 0 {
		return nil
	}
	kind := r.Knobs["kind"]
	out := make(map[string]string, len(r.Workflows))
	for _, w := range r.Workflows {
		k := kind
		if o, ok := r.Overrides[w]; ok {
			if v, ok := o["kind"]; ok {
				k = v
			}
		}
		if k != "" {
			out[w] = k
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
