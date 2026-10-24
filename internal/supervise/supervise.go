// Package supervise owns the loop child processes of a connected daemon:
// one `jig --no-dashboard <root>` per started (project, workflow), keyed
// by Key(project, workflow).
//
// It is also where a child's failure is attributed. A child that exits
// on its own is reported against its loop, with the tail of its output,
// and the other loops keep running.
package supervise

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/plan"
	"github.com/kinged007/jigz/internal/source"
)

// gracefulTimeout is how long a child gets to exit after SIGTERM before
// its process group is killed.
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

// outputLines is how much of a child's output is kept. The tail is what
// explains a failure; the whole stream is the child log's job.
const outputLines = 50

// Loop and repository states. Repo and the states are also what the
// dashboard reports for its own runs.
const (
	StateStopped  = "stopped"
	StateRunning  = "running"
	StateFailed   = "failed"
	StateInvalid  = "invalid"
	StateDisabled = "disabled"
)

// Repo is one repository's supervisable state, as the dashboard sees it.
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

// Child is one loop's supervised state.
type Child struct {
	Key  string
	Loop source.Loop
	// StartedHash is Loop.Hash as it was when the running child started,
	// "" when no child runs.
	StartedHash string
	State       string
	// Exited is set when the last child ended without being asked to.
	Exited bool
	// Stopping is set while a child runs that was asked to stop.
	Stopping bool
	PID      int
	Since    time.Time
	Err      string
	Output   []string
}

type entry struct {
	loop    source.Loop
	started string
	state   string
	cmd     *exec.Cmd
	done    chan struct{}
	since   time.Time
	err     string
	exited  bool
	out     *ring
	// wanted is whether this loop should be running. A child that exits
	// while it is false was asked to stop, and its exit is not a failure.
	wanted bool
}

func (e *entry) pid() int {
	if e.cmd == nil || e.cmd.Process == nil {
		return 0
	}
	return e.cmd.Process.Pid
}

// Supervisor owns the loop children.
type Supervisor struct {
	spawn func(l source.Loop) *exec.Cmd
	// Grace overrides the SIGTERM-to-kill budget. Zero means 60s.
	Grace time.Duration

	mu    sync.Mutex
	loops map[string]*entry
}

// New returns a supervisor that builds each child with spawn. The
// supervisor puts the child in its own process group and captures its
// output.
func New(spawn func(l source.Loop) *exec.Cmd) *Supervisor {
	return &Supervisor{spawn: spawn, loops: map[string]*entry{}}
}

// Key names a loop: project/workflow.
func Key(project, workflow string) string { return project + "/" + workflow }

func (s *Supervisor) graceBudget() time.Duration {
	if s.Grace > 0 {
		return s.Grace
	}
	return gracefulTimeout
}

// Update adds l or replaces the recorded loop. A running child is left
// alone: it keeps the files it started with until it is restarted.
func (s *Supervisor) Update(l source.Loop) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := Key(l.Project, l.Workflow)
	if e, ok := s.loops[k]; ok {
		e.loop = l
		return
	}
	s.loops[k] = &entry{loop: l, state: StateStopped, out: newRing(outputLines)}
}

// Remove stops the child of key, waits for it, and forgets the loop.
func (s *Supervisor) Remove(key string) {
	s.terminate(key)
	s.mu.Lock()
	delete(s.loops, key)
	s.mu.Unlock()
}

// Keys lists every loop, in name order.
func (s *Supervisor) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.loops))
	for k := range s.loops {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Start launches the child of key. A loop that already has a child is
// left alone.
func (s *Supervisor) Start(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.loops[key]
	if !ok {
		return fmt.Errorf("no loop %q", key)
	}
	if e.cmd != nil {
		return nil
	}
	cmd := s.spawn(e.loop)
	setChildGroup(cmd)
	// A real file, not an io.Writer: exec passes a file descriptor
	// straight through and Wait returns as soon as the child exits.
	// Given a writer it copies through a pipe in a goroutine, and Wait
	// then blocks until that pipe reaches EOF, which a surviving
	// grandchild holding the write end prevents.
	w, closeWrite, err := capture(e.out)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		closeWrite()
		e.state, e.err = StateFailed, err.Error()
		return err
	}
	// The parent must drop its own write end, or the drainer never sees
	// EOF even after every descendant has gone.
	closeWrite()
	done := make(chan struct{})
	e.cmd, e.done, e.wanted, e.since = cmd, done, true, time.Now()
	e.started, e.state, e.err, e.exited = e.loop.Hash, StateRunning, "", false
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

// watch reaps one child and records how it ended. An exit while the loop
// is still wanted is reported; an exit after a stop was requested is not.
func (s *Supervisor) watch(e *entry, cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	// Closed after the entry is updated: a waiter that restarts the loop
	// as soon as done closes must not find the old cmd still registered.
	defer close(done)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.cmd != cmd {
		return
	}
	e.cmd, e.done, e.started = nil, nil, ""
	switch {
	case !e.wanted:
		e.state = StateStopped
	case err != nil:
		e.state, e.exited, e.err = StateFailed, true, exitReason(err)
	default:
		e.state, e.exited, e.err = StateStopped, true, "the loop exited on its own"
	}
}

// Stop asks the child of key to exit and waits for it, killing its
// process group after the grace period.
func (s *Supervisor) Stop(key string) error {
	s.mu.Lock()
	_, ok := s.loops[key]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no loop %q", key)
	}
	s.terminate(key)
	return nil
}

// StopAll stops every child at once and waits for all of them. It runs
// on shutdown, where a surviving loop would keep claiming issues for a
// daemon that is no longer there.
func (s *Supervisor) StopAll() {
	var wg sync.WaitGroup
	for _, k := range s.Keys() {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			s.terminate(k)
		}(k)
	}
	wg.Wait()
}

// WaitAll blocks until every child has exited.
func (s *Supervisor) WaitAll() {
	for {
		s.mu.Lock()
		var waiting []chan struct{}
		for _, e := range s.loops {
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

// terminate signals a child and waits for it, killing its process group
// when it outlives the grace period. The engine runs in the child's
// group, so the kill reaches it too.
func (s *Supervisor) terminate(key string) {
	s.mu.Lock()
	e, ok := s.loops[key]
	if !ok {
		s.mu.Unlock()
		return
	}
	e.wanted = false
	cmd, done := e.cmd, e.done
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
		killGroup(cmd)
		<-done
	}
	// A grandchild that left the engine's drain unfinished still holds
	// the group; nothing of a stopped loop may outlive it.
	killGroup(cmd)
}

// State returns a loop's current state, or "" if unknown.
func (s *Supervisor) State(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.loops[key]
	if !ok {
		return ""
	}
	return e.state
}

// Running reports whether key has a child.
func (s *Supervisor) Running(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.loops[key]
	return ok && e.cmd != nil
}

// StopWhenIdle asks the child of key to stop each of its engine loops as
// that loop finishes what it is working on, then exit. It returns at
// once; the child decides when it is done.
func (s *Supervisor) StopWhenIdle(key string) error {
	s.mu.Lock()
	if e, ok := s.loops[key]; ok {
		e.wanted = false
	}
	s.mu.Unlock()
	return s.Signal(key, sigStopIdle)
}

// RecycleWhenIdle asks the child of key to replace each engine loop as it
// goes idle, picking up the files now on disk. The child stays up and
// records the loop's current hash as the one it runs.
func (s *Supervisor) RecycleWhenIdle(key string) error {
	if err := s.Signal(key, sigRestartIdle); err != nil {
		return err
	}
	s.mu.Lock()
	if e, ok := s.loops[key]; ok && e.cmd != nil {
		e.started = e.loop.Hash
	}
	s.mu.Unlock()
	return nil
}

// Reload asks the child of key to re-read its config.yaml (sleep
// settings) without restarting its engine.
func (s *Supervisor) Reload(key string) error { return s.Signal(key, sigReload) }

// Signal sends sig to the child of key. A loop with no child has nothing
// to signal, which is not an error.
func (s *Supervisor) Signal(key string, sig syscall.Signal) error {
	if !idleSignals {
		return errors.New("this platform has no control signals for a loop child")
	}
	s.mu.Lock()
	e, ok := s.loops[key]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("no loop %q", key)
	}
	cmd := e.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal %s: %w", sig, err)
	}
	return nil
}

// Snapshot returns every loop's state, in key order.
func (s *Supervisor) Snapshot() []Child {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Child, 0, len(s.loops))
	for k, e := range s.loops {
		out = append(out, Child{
			Key: k, Loop: e.loop, StartedHash: e.started, State: e.state, Exited: e.exited,
			Stopping: e.cmd != nil && !e.wanted,
			PID:      e.pid(), Since: e.since, Err: e.err, Output: e.out.tail(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// exitReason describes a child's non-zero exit. The message itself is in
// the output ring.
func exitReason(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit status %d", ee.ExitCode())
	}
	return err.Error()
}

// ring keeps the last n lines written to it.
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
