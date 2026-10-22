package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/machine"
)

// poolReadEvery is how often pool.json is read again. The agent-exec
// processes of the loops write it, and a limit hit only needs to reach
// the gate within a retry interval.
const poolReadEvery = 10 * time.Second

const (
	gateHours = "hours"
	gatePool  = "pool"
)

type gateAction int

const (
	gateNone gateAction = iota
	gateStart
	gateStopNow
	gateStopIdle
	gateRecycleIdle
)

// gateView is what the machine settings say at one moment.
type gateView struct {
	// Open is false while the window is closed or the pool has no
	// available account.
	Open      bool
	HoursOpen bool
	// Runtime is the runtime of the first available pool entry, empty
	// without a pool or when none is available.
	Runtime string
	Next    time.Time
	// Reason is gateHours or gatePool while the gate is closed.
	Reason string
}

// gateLoop is what the gate needs to know about one loop.
type gateLoop struct {
	name string
	// up is a child that runs and was not asked to stop.
	up bool
	// held is a loop the gate stopped, or kept from starting, and has to
	// start again when it reopens.
	held    bool
	paused  bool
	runtime string
}

type gateAct struct {
	name   string
	action gateAction
}

func gateEval(cfg *machine.Config, st machine.State, now time.Time) gateView {
	v := gateView{HoursOpen: cfg.Hours.Open(now), Next: cfg.NextChange(st, now)}
	if len(cfg.Pool) > 0 {
		v.Runtime = cfg.ActiveRuntime(st, now)
	}
	switch {
	case !v.HoursOpen:
		v.Reason = gateHours
	case len(cfg.Pool) > 0 && v.Runtime == "":
		v.Reason = gatePool
	}
	v.Open = v.Reason == ""
	return v
}

// gateDecide returns the actions that bring the loops in line with the
// machine settings. Stopping when idle and recycling are left to the
// caller to carry out once the loop has no running session.
func gateDecide(cfg *machine.Config, st machine.State, now time.Time, loops []gateLoop) (gateView, []gateAct) {
	v := gateEval(cfg, st, now)
	var acts []gateAct
	for _, l := range loops {
		a := gateNone
		switch {
		case !v.Open:
			if l.up {
				a = gateStopIdle
				if v.Reason == gateHours && cfg.Hours.End == machine.EndStop {
					a = gateStopNow
				}
			}
		case l.paused:
		case l.held && !l.up:
			a = gateStart
		case l.up && l.runtime != v.Runtime:
			a = gateRecycleIdle
		}
		if a != gateNone {
			acts = append(acts, gateAct{l.name, a})
		}
	}
	return v, acts
}

func (v gateView) hold() string {
	switch v.Reason {
	case gateHours:
		return "outside working hours"
	case gatePool:
		if v.Next.IsZero() {
			return "no agent account available"
		}
		return "no agent account available until " + v.Next.Format(time.RFC3339)
	}
	return ""
}

// gateStatus is the `gate` object of the status document. It is absent
// when the machine file sets neither working hours nor a pool.
//
//	open         false while the gate keeps loops down
//	hours_open   the working-hours window is open
//	runtime      the agent runtime the loops use, empty without a pool
//	next_change  the next time the window or an account changes
//	hold         why the gate is closed, empty when open
//
// A workflow has `hold` set to the same text while the gate is closed,
// unless a budget pause holds it. A workflow the gate stopped has state
// "held".
type gateStatus struct {
	Open       bool       `json:"open"`
	HoursOpen  bool       `json:"hours_open"`
	Runtime    string     `json:"runtime"`
	NextChange *time.Time `json:"next_change"`
	Hold       string     `json:"hold"`
}

// holdFor is the hold text of a workflow.
func (g *gateStatus) holdFor(bs *budgetStatus) string {
	if g == nil || (bs != nil && bs.Paused) {
		return ""
	}
	return g.Hold
}

// machineState is the supervisor's view of machine.yaml and pool.json.
// The gate state is in memory: after a restart it is recomputed from the
// files and the clock, which gives the same answer.
type machineState struct {
	machinePath string
	poolPath    string
	machine     *machine.Config
	stamp       string
	bootErr     error
	poolState   machine.State
	poolRead    time.Time
	poolErr     string
	gateHeld    map[string]bool
	gateSeen    bool
	gateLast    gateView
}

func (s *supervisor) initMachine() {
	s.machinePath, _ = machine.DefaultPath()
	s.poolPath, _ = machine.StatePath()
	s.gateHeld = map[string]bool{}
	s.machine, _ = machine.Load("")
	s.bootErr = s.refreshMachine(s.clock(), true)
}

func (s *supervisor) machineErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bootErr
}

func fileStamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", fi.ModTime().UnixNano(), fi.Size())
}

// refreshMachine reads machine.yaml when it changed (or force is set) and
// pool.json when it is stale. A machine.yaml that does not load keeps the
// previous settings and its error is returned.
func (s *supervisor) refreshMachine(now time.Time, force bool) (loadErr error) {
	s.mu.Lock()
	path, poolPath, old := s.machinePath, s.poolPath, s.stamp
	loaded := s.machine
	s.mu.Unlock()

	stamp := fileStamp(path)
	if force || stamp != old {
		cfg, err := machine.Load(path)
		s.mu.Lock()
		s.stamp = stamp
		if err != nil {
			fmt.Fprintln(os.Stderr, "sortie-loop: machine config:", err)
			loadErr = err
		} else {
			for _, w := range cfg.Warnings {
				fmt.Fprintln(os.Stderr, "sortie-loop: machine config:", w)
			}
			s.machine, s.bootErr = cfg, nil
			s.poolRead = time.Time{}
			loaded = cfg
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	stale := len(loaded.Pool) > 0 && now.Sub(s.poolRead) >= poolReadEvery
	s.mu.Unlock()
	if !stale {
		return loadErr
	}
	st, err := machine.LoadState(poolPath)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.poolRead = now
	if err != nil {
		if s.poolErr != err.Error() {
			s.poolErr = err.Error()
			fmt.Fprintln(os.Stderr, "sortie-loop: pool state:", err)
		}
		return loadErr
	}
	s.poolErr = ""
	s.poolState = st
	return loadErr
}

// startable reports whether a loop may start now. A loop that the gate
// keeps down is remembered as held, so that the gate starts it when it
// opens again.
func (s *supervisor) startable(name string) bool {
	if s.isPaused(name) {
		return false
	}
	now := s.clock()
	s.refreshMachine(now, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !gateEval(s.machine, s.poolState, now).Open {
		s.gateHeld[name] = true
		return false
	}
	return true
}

var selfExe = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// errNoAgent is returned when the pool has no available account at the
// moment a child starts. The gate decision and the start read the clock
// separately, so a boundary can fall between them.
var errNoAgent = errors.New("no agent account available")

// agentEnv is the agent part of a child's environment. The engine splits
// agent.command on whitespace, so a path with whitespace cannot be used.
func (s *supervisor) agentEnv() (env []string, runtime string, err error) {
	now := s.clock()
	s.refreshMachine(now, false)
	s.mu.Lock()
	pool := len(s.machine.Pool) > 0
	runtime = gateEval(s.machine, s.poolState, now).Runtime
	s.mu.Unlock()
	if !pool {
		return nil, "", nil
	}
	if runtime == "" {
		return nil, "", errNoAgent
	}
	exe, err := selfExe()
	if err != nil {
		return nil, "", fmt.Errorf("agent pool: %w", err)
	}
	if strings.ContainsAny(exe, " \t\r\n") {
		return nil, "", fmt.Errorf("agent pool: the sortie-loop path %q contains whitespace, which the engine cannot pass to agent.command; move the binary", exe)
	}
	return []string{
		"SORTIE_AGENT_KIND=" + runtime,
		"SORTIE_AGENT_COMMAND=" + exe + " agent-exec " + runtime,
	}, runtime, nil
}

func (s *supervisor) gateStatusLocked() *gateStatus {
	cfg := s.machine
	if len(cfg.Hours.Windows) == 0 && len(cfg.Pool) == 0 {
		return nil
	}
	v := gateEval(cfg, s.poolState, s.clock())
	return &gateStatus{
		Open: v.Open, HoursOpen: v.HoursOpen, Runtime: v.Runtime,
		NextChange: timePtr(v.Next), Hold: v.hold(),
	}
}

// gateStep runs on every tick. It applies the machine settings to the
// loops and logs each change of the gate once.
func (s *supervisor) gateStep() (int, bool) {
	now := s.clock()
	s.refreshMachine(now, false)
	s.mu.Lock()
	cfg, st := s.machine, s.poolState
	loops := s.gateLoopsLocked()
	idle := !s.sleeping && !s.exitWhenEmpty
	s.mu.Unlock()

	view, acts := gateDecide(cfg, st, now, loops)
	if s.noteGate(view, cfg) {
		s.republish()
	}
	if !idle {
		return 0, false
	}
	for _, a := range acts {
		switch a.action {
		case gateStart:
			if err := s.gateStart(a.name); err != nil {
				fmt.Fprintln(os.Stderr, "sortie-loop:", err)
				s.failed = true
				return 1, true
			}
		case gateStopNow:
			s.gateStop(a.name, false)
		case gateStopIdle, gateRecycleIdle:
			if s.noSession(a.name) {
				s.gateStop(a.name, a.action == gateRecycleIdle)
			}
		}
	}
	return 0, false
}

func (s *supervisor) gateLoopsLocked() []gateLoop {
	loops := make([]gateLoop, 0, len(s.defs))
	names := make([]string, 0, len(s.defs))
	for n := range s.defs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		l := gateLoop{name: n, held: s.gateHeld[n]}
		if bs := s.budgetStates[n]; bs != nil {
			l.paused = bs.Paused
		}
		if p := s.procs[n]; p != nil {
			l.up, l.runtime = !p.stopping, p.runtime
		}
		loops = append(loops, l)
	}
	return loops
}

// noSession reports whether the loop is settled and has no session
// running. A loop whose engine does not answer is not.
func (s *supervisor) noSession(name string) bool {
	s.mu.Lock()
	p := s.procs[name]
	if p == nil || time.Since(p.since) < drainSettle {
		s.mu.Unlock()
		return false
	}
	port := p.port
	s.mu.Unlock()
	active, known := s.sessions(port)
	return known && !active
}

// gateStop stops a loop as a requested stop, so the exit does not count
// as a crash. With recycle the loop starts again with the current
// environment once it has exited.
func (s *supervisor) gateStop(name string, recycle bool) {
	s.mu.Lock()
	if p := s.procs[name]; p != nil {
		p.stopping, p.drain, p.recycle = true, false, recycle
	}
	if !recycle {
		s.gateHeld[name] = true
	}
	s.mu.Unlock()
	s.terminate(name)
}

// gateStart starts a held loop. One that has not exited yet starts when
// it has.
func (s *supervisor) gateStart(name string) error {
	s.mu.Lock()
	p := s.procs[name]
	if p != nil {
		p.recycle = true
	}
	s.mu.Unlock()
	if p != nil {
		return nil
	}
	return s.startOne(name)
}

// noteGate logs a change of the gate and reports whether there was one.
func (s *supervisor) noteGate(v gateView, cfg *machine.Config) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, seen := s.gateLast, s.gateSeen
	s.gateLast, s.gateSeen = v, true
	if !seen {
		if !v.Open {
			s.logClosed(v, cfg)
		}
		return !v.Open
	}
	switch {
	case prev.Open != v.Open || prev.Reason != v.Reason:
		if v.Open {
			fmt.Fprintln(os.Stderr, "sortie-loop: gate open, starting the loops it held")
		} else {
			s.logClosed(v, cfg)
		}
	case v.Open && prev.Runtime != v.Runtime:
		fmt.Fprintf(os.Stderr, "sortie-loop: agent runtime changed from %q to %q, loops restart as they go idle\n", prev.Runtime, v.Runtime)
	}
	return prev.Open != v.Open || prev.HoursOpen != v.HoursOpen || prev.Runtime != v.Runtime ||
		!prev.Next.Equal(v.Next) || prev.hold() != v.hold()
}

func (s *supervisor) logClosed(v gateView, cfg *machine.Config) {
	how := "loops stop as they go idle"
	if v.Reason == gateHours && cfg.Hours.End == machine.EndStop {
		how = "stopping loops now"
	}
	fmt.Fprintf(os.Stderr, "sortie-loop: gate closed (%s), %s\n", v.hold(), how)
}
