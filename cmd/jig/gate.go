package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/gen"
	"github.com/kinged007/jigz/internal/machine"
)

// stateReadEvery is how often state.json is read again. The agent-exec
// processes of the loops write it, and a limit hit only needs to reach
// the gate within a retry interval.
const stateReadEvery = 10 * time.Second

const (
	gateHours = "hours"
	gatePool  = "pool"
)

// Offline reasons of the work-entry gate. The machine gate keeps its own
// texts through gateView.hold.
const (
	reasonOperatingHours = "outside operating hours"
	reasonExhausted      = "every agent is exhausted"
	reasonUnresolved     = "every agent has unresolved environment references"
	reasonUnregistered   = "no agent runtime is registered on the engine"
	reasonNoCandidate    = "no candidate agent is available"
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

// loopView is one loop's availability at one moment: the work entry's
// operating hours and candidate agents, intersected with the machine
// gate.
type loopView struct {
	// Open is false while the loop must not run.
	Open bool
	// Agent is the first available candidate, empty while the gate is
	// closed or no work entry binds the loop.
	Agent string
	// Reason is why the loop is offline, empty while it may run.
	Reason string
	// ETA is the estimated start, zero when there is no estimate.
	ETA time.Time
	// End is the end mode the offline reason selects: finish or stop.
	End string
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

// gateInputs are the settings the gate decision reads.
type gateInputs struct {
	machine *machine.Config
	state   machine.State
	// work holds the resolved work entries by loop name. A loop without
	// an entry has no candidate agents.
	work map[string]config.Resolved
	// agents is the fleet; refs the unresolved from_env references.
	agents    map[string]config.Agent
	refs      config.RefStatus
	operating machine.Hours
	engine    engine.Gate
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

// gateDecide returns the machine gate view, the per-loop decisions and
// the actions that bring the loops in line with the settings. Stopping
// when idle and recycling are left to the caller to carry out once the
// loop has no running session.
func gateDecide(in gateInputs, now time.Time, loops []gateLoop) (gateView, map[string]loopView, []gateAct) {
	mv := gateEval(in.machine, in.state, now)
	views := map[string]loopView{}
	var acts []gateAct
	for _, l := range loops {
		lv := in.loopView(l.name, mv, now)
		views[l.name] = lv
		a := gateNone
		switch {
		case !lv.Open:
			if l.up {
				a = gateStopIdle
				if lv.End == machine.EndStop {
					a = gateStopNow
				}
			}
		case l.paused:
		case l.held && !l.up:
			a = gateStart
		case l.up && l.runtime != mv.Runtime:
			a = gateRecycleIdle
		}
		if a != gateNone {
			acts = append(acts, gateAct{l.name, a})
		}
	}
	return mv, views, acts
}

// loopView evaluates one loop: its work entry first, then the machine
// gate. A closed work gate decides the end behaviour (section 5.2); the
// machine gate decides only while the work gate is open.
func (in gateInputs) loopView(name string, mv gateView, now time.Time) loopView {
	wv := in.workView(in.work[name], now)
	switch {
	case !wv.Open:
		return wv
	case !mv.Open:
		end := machine.EndFinish
		if mv.Reason == gateHours && in.machine.Hours.End == machine.EndStop {
			end = machine.EndStop
		}
		return loopView{Reason: mv.hold(), ETA: mv.Next, End: end}
	}
	return wv
}

// workView evaluates a loop's work entry: operating hours first, then
// the ordered candidate agents. No candidates means no per-loop
// constraint.
func (in gateInputs) workView(res config.Resolved, now time.Time) loopView {
	names, over := candidates(res)
	if !in.operating.Open(now) {
		v := loopView{Reason: reasonOperatingHours, End: endOrFinish(in.operating.End)}
		if len(names) > 0 {
			v.Agent = names[0]
		}
		v.estimate(res, in, now)
		return v
	}
	for _, name := range names {
		if in.block(name, over, now) == "" {
			return loopView{Open: true, Agent: name}
		}
	}
	if len(names) == 0 {
		return loopView{Open: true}
	}
	v := loopView{Agent: names[0], End: machine.EndFinish}
	// The reason that closed the gate chooses the end behaviour: the
	// first candidate whose only block is closed hours gives its
	// schedule.end; every other closure ends with finish.
	for _, name := range names {
		b := in.blockOf(name, over, now)
		if b.hours && !b.exhausted && !b.refs && !b.runtime {
			v.Reason = "agent " + name + " is outside its working hours"
			v.End = endOrFinish(in.schedule(name, over).End)
			break
		}
	}
	if v.Reason == "" {
		all := blockAll{exhausted: true, refs: true, runtime: true}
		for _, name := range names {
			b := in.blockOf(name, over, now)
			all.exhausted = all.exhausted && b.exhausted
			all.refs = all.refs && b.refs
			all.runtime = all.runtime && b.runtime
		}
		switch {
		case all.exhausted:
			v.Reason = reasonExhausted
		case all.refs:
			v.Reason = reasonUnresolved
		case all.runtime:
			v.Reason = reasonUnregistered
		default:
			v.Reason = reasonNoCandidate
		}
	}
	v.estimate(res, in, now)
	return v
}

// estimate fills the estimated start: the earliest of the next working
// hours start of a candidate, the earliest exhaustion of a candidate and
// the next operating hours start. No future input leaves the estimate
// zero and the reason shows alone.
func (v *loopView) estimate(res config.Resolved, in gateInputs, now time.Time) {
	consider := func(t time.Time) {
		if t.After(now) && (v.ETA.IsZero() || t.Before(v.ETA)) {
			v.ETA = t
		}
	}
	names, over := candidates(res)
	for _, name := range names {
		sched := in.schedule(name, over)
		if !sched.Open(now) {
			consider(sched.Next(now))
		}
		consider(in.state.Agents[name].ExhaustedUntil)
	}
	if !in.operating.Open(now) {
		consider(in.operating.Next(now))
	}
}

// candidates returns the loop's ordered candidate agent names and the
// schedule overrides of the rules that select an agent: the entry's
// list when set, else the rules' lists in rule order.
func candidates(res config.Resolved) ([]string, map[string]machine.Hours) {
	names := res.Agents
	over := map[string]machine.Hours{}
	if len(names) == 0 {
		seen := map[string]bool{}
		for _, r := range res.Rules {
			for _, n := range r.Agents {
				if !seen[n] {
					seen[n] = true
					names = append(names, n)
				}
			}
		}
	}
	for _, r := range res.Rules {
		if r.Schedule != nil {
			for _, n := range r.Agents {
				if _, ok := over[n]; !ok {
					over[n] = *r.Schedule
				}
			}
		}
	}
	return names, over
}

// schedule is the agent's effective working hours: the rule override
// when a rule selects the agent with one, else the fleet agent's
// schedule.
func (in gateInputs) schedule(name string, over map[string]machine.Hours) machine.Hours {
	if h, ok := over[name]; ok {
		return h
	}
	return in.agents[name].Schedule
}

// blockAll reports which blocks hold for every candidate.
type blockAll struct {
	exhausted, refs, runtime bool
}

// agentBlock is why one candidate cannot serve now.
type agentBlock struct {
	hours, exhausted, refs, runtime bool
}

func (in gateInputs) blockOf(name string, over map[string]machine.Hours, now time.Time) agentBlock {
	a, ok := in.agents[name]
	return agentBlock{
		hours:     !ok || !in.schedule(name, over).Open(now),
		exhausted: now.Before(in.state.Agents[name].ExhaustedUntil),
		refs:      len(in.refs.Agents[name]) > 0,
		runtime:   ok && in.engine.RuntimeOff(a.Runtime) != "",
	}
}

// block is why one candidate cannot serve now, "" when it can.
func (in gateInputs) block(name string, over map[string]machine.Hours, now time.Time) string {
	b := in.blockOf(name, over, now)
	switch {
	case b.hours:
		return "agent " + name + " is outside its working hours"
	case b.exhausted:
		return "agent " + name + " is exhausted"
	case b.refs:
		return "agent " + name + " has unresolved environment references"
	case b.runtime:
		return "agent " + name + " has no runtime registered on the engine"
	}
	return ""
}

func endOrFinish(end string) string {
	if end == "" {
		return machine.EndFinish
	}
	return end
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
// Each workflow carries its own offline_reason and eta from the
// per-loop decision. A workflow the gate stopped has state "offline";
// one that capability detection disabled has state "disabled".
type gateStatus struct {
	Open       bool       `json:"open"`
	HoursOpen  bool       `json:"hours_open"`
	Runtime    string     `json:"runtime"`
	NextChange *time.Time `json:"next_change"`
	Hold       string     `json:"hold"`
}

// machineState is the supervisor's view of machine.yaml, the work
// config and state.json. The gate state is in memory: after a restart it
// is recomputed from the files and the clock, which gives the same
// answer.
type machineState struct {
	machinePath string
	statePath   string
	machine     *machine.Config
	stamp       string
	bootErr     error
	agentState  machine.State
	stateRead   time.Time
	stateErr    string
	workStamp   string
	work        map[string]config.Resolved
	// queries holds the tracker query of every loop by loop name: the
	// narrowed query for split loops, the entry query otherwise. The
	// engine reads the filter only at start, so a change needs a drain
	// and restart.
	queries   map[string]string
	agents    map[string]config.Agent
	refs      config.RefStatus
	operating machine.Hours
	gateHeld  map[string]bool
	gateSeen  bool
	gateLast  gateView
	recorded  map[string]machine.LoopState
}

func (s *supervisor) initMachine() {
	s.machinePath, _ = machine.DefaultPath()
	s.statePath = machine.StatePath(s.root)
	s.gateHeld = map[string]bool{}
	s.recorded = map[string]machine.LoopState{}
	s.machine, _ = machine.Load("")
	s.bootErr = s.refreshMachine(s.clock(), true)
}

// setGate replaces the engine's gate, and with it the runtimes the pool
// and the fleet may pick.
func (s *supervisor) setGate(g engine.Gate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = g
	s.machine.Unsupported = unsupportedRuntimes(g)
}

// engineVersion is the version the engine reports, or the configured one
// when the engine could not be probed.
func (s *supervisor) engineVersion() string {
	if v := s.gate.Caps.Version; v != "" {
		return v
	}
	return engine.Current().Version()
}

func unsupportedRuntimes(g engine.Gate) map[string]bool {
	out := map[string]bool{}
	for _, rt := range machine.Runtimes {
		if g.RuntimeOff(rt) != "" {
			out[rt] = true
		}
	}
	return out
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

// refreshMachine reads machine.yaml when it changed (or force is set),
// the work config when its file changed, and state.json when it is
// stale. A machine.yaml that does not load keeps the previous settings
// and its error is returned.
func (s *supervisor) refreshMachine(now time.Time, force bool) (loadErr error) {
	s.mu.Lock()
	path, statePath, old := s.machinePath, s.statePath, s.stamp
	workOld := s.workStamp
	s.mu.Unlock()

	stamp := fileStamp(path)
	if force || stamp != old {
		cfg, err := machine.Load(path)
		s.mu.Lock()
		s.stamp = stamp
		if err != nil {
			fmt.Fprintln(os.Stderr, "jig: machine config:", err)
			loadErr = err
		} else {
			for _, w := range cfg.Warnings {
				fmt.Fprintln(os.Stderr, "jig: machine config:", w)
			}
			s.machine, s.bootErr = cfg, nil
			s.machine.Unsupported = unsupportedRuntimes(s.gate)
			s.stateRead = time.Time{}
		}
		s.mu.Unlock()
	}

	workStamp := fileStamp(filepath.Join(s.root, ".jigz", "config.yaml"))
	if force || workStamp != workOld {
		s.refreshWork(workStamp)
	}

	s.mu.Lock()
	stale := now.Sub(s.stateRead) >= stateReadEvery
	s.mu.Unlock()
	if !stale {
		return loadErr
	}
	st, err := machine.LoadState(statePath)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateRead = now
	if err != nil {
		if s.stateErr != err.Error() {
			s.stateErr = err.Error()
			fmt.Fprintln(os.Stderr, "jig: state:", err)
		}
		return loadErr
	}
	s.stateErr = ""
	s.agentState = st
	return loadErr
}

// refreshWork re-reads .jigz/config.yaml and resolves the work entries
// the gate evaluates. A config that does not load keeps the previous
// resolution; the load fails the start, so this only guards a bad edit
// during a run. Entries split into one resolved value per loop, keyed
// by loop name; unmanaged entries resolve to an empty candidate list,
// so the gate leaves them open and the manager only starts and stops
// them.
func (s *supervisor) refreshWork(stamp string) {
	cfg, err := config.Load(s.root)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workStamp = stamp
	if err != nil {
		fmt.Fprintln(os.Stderr, "jig: work config:", err)
		return
	}
	work := map[string]config.Resolved{}
	queries := map[string]string{}
	loops := map[string]gen.Loop{}
	profs := map[string]gen.Profile{}
	resolved := cfg.Resolve()
	for _, slug := range sortedKeys(resolved) {
		res := resolved[slug]
		if !res.Managed {
			r := config.Resolved{Slug: res.Slug, Managed: false, QueryFilter: res.QueryFilter, OperatingHours: res.OperatingHours}
			work[res.Slug] = r
			queries[res.Slug] = res.QueryFilter
			continue
		}
		p, _, err := gen.ProfileFor(res.Engine.Repo, res.Engine.Version)
		if err != nil {
			fmt.Fprintln(os.Stderr, "jig: work config:", err)
			return
		}
		for _, l := range gen.Split(res, cfg.Agents) {
			r := res
			r.Slug = l.Name
			r.Agents = l.Agents
			r.Rules = l.Rules
			r.QueryFilter = l.Query
			work[l.Name] = r
			queries[l.Name] = l.Query
			loops[l.Name] = l
			profs[l.Name] = p
		}
	}
	s.work = work
	s.queries = queries
	s.genLoops = loops
	s.genProf = profs
	s.agents = cfg.Agents
	s.refs = cfg.UnresolvedRefs()
	s.operating = cfg.OperatingHours
	// Drop per-loop memory of loops the config no longer defines.
	for n := range s.docAgent {
		if _, ok := work[n]; !ok {
			delete(s.docAgent, n)
		}
	}
	for n := range s.fallback {
		if _, ok := work[n]; !ok {
			delete(s.fallback, n)
		}
	}
	for n := range s.pickCnt {
		if _, ok := work[n]; !ok {
			delete(s.pickCnt, n)
		}
	}
}

// queryFor reports the loop's tracker query for the launch override.
func (s *supervisor) queryFor(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.queries[name]
	return q, ok
}

// querySnapshot copies the loop queries for change detection.
func (s *supervisor) querySnapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.queries))
	for n, q := range s.queries {
		out[n] = q
	}
	return out
}

// recycleDrain stops a loop once idle and starts it again: the query
// filter is read only at start, so a changed query needs a restart.
// A loop that is not running is held for the gate to start.
func (s *supervisor) recycleDrain(name string) {
	s.mu.Lock()
	if p := s.procs[name]; p != nil {
		p.stopping, p.drain, p.recycle = true, true, true
	} else if _, ok := s.defs[name]; ok {
		s.gateHeld[name] = true
	}
	s.mu.Unlock()
}

// gateInputsLocked assembles the gate decision inputs. The caller holds
// s.mu.
func (s *supervisor) gateInputsLocked() gateInputs {
	return gateInputs{
		machine: s.machine, state: s.agentState, work: s.work,
		agents: s.agents, refs: s.refs, operating: s.operating, engine: s.gate,
	}
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
	in := s.gateInputsLocked()
	if lv := in.loopView(name, gateEval(in.machine, in.state, now), now); !lv.Open {
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
	runtime = gateEval(s.machine, s.agentState, now).Runtime
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
		return nil, "", fmt.Errorf("agent pool: the binary path %q contains whitespace, which the engine cannot pass to agent.command; move the binary", exe)
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
	v := gateEval(cfg, s.agentState, s.clock())
	return &gateStatus{
		Open: v.Open, HoursOpen: v.HoursOpen, Runtime: v.Runtime,
		NextChange: timePtr(v.Next), Hold: v.hold(),
	}
}

// gateStep runs on every tick. It applies the machine settings and the
// work entries to the loops and logs each change of the gate once.
func (s *supervisor) gateStep() (int, bool) {
	now := s.clock()
	s.refreshMachine(now, false)
	s.mu.Lock()
	in := s.gateInputsLocked()
	loops := s.gateLoopsLocked()
	idle := !s.sleeping && !s.exitWhenEmpty
	s.mu.Unlock()

	mv, views, acts := gateDecide(in, now, loops)
	changed := s.recordLoops(in, now, views)
	if s.noteGate(mv, in.machine) {
		changed = true
	}
	if changed {
		s.republish()
	}
	if !idle {
		return 0, false
	}
	for _, a := range acts {
		switch a.action {
		case gateStart:
			if err := s.gateStart(a.name); err != nil {
				fmt.Fprintln(os.Stderr, "jig:", err)
				s.failed = true
				return 1, true
			}
		case gateStopNow:
			s.gateStop(a.name, false)
		case gateStopIdle, gateRecycleIdle:
			// A draining loop keeps claiming new items until it goes
			// idle: max_concurrent_agents 0 means no limit, not "claim
			// nothing", so no document value stops new claims while
			// running sessions finish. Drain is finish semantics only.
			if s.noSession(a.name) {
				s.gateStop(a.name, a.action == gateRecycleIdle)
			}
		}
	}
	s.fallbackStep(in, views, now)
	return 0, false
}

// fallbackStableTicks is how many consecutive ticks a new pick must
// survive before the document is rewritten for it. A pick that replaces
// an unavailable agent applies at once; the return to the primary waits
// out flapping limits.
const fallbackStableTicks = 2

// pickStable counts consecutive ticks the gate picked the same agent.
type pickStable struct {
	agent string
	n     int
}

// fallbackInfo records a standby switch for status: what drove the
// previous agent out and when the primary is usable again.
type fallbackInfo struct {
	from, to, trigger string
	at                time.Time
	usable            time.Time
}

func (f fallbackInfo) String(primary string) string {
	usable := "unknown"
	if !f.usable.IsZero() {
		usable = f.usable.Format(time.RFC3339)
	}
	return fmt.Sprintf("%s (since %s); %s usable again %s", f.trigger, f.at.Format(time.RFC3339), primary, usable)
}

// fallbackStep rewrites a managed document at idle when the gate picked
// a different agent than the document runs. The engine re-reads the
// document live, so no restart follows unless the query changed (which
// reload handles separately).
func (s *supervisor) fallbackStep(in gateInputs, views map[string]loopView, now time.Time) {
	for _, l := range s.gateLoopsLocked() {
		v, ok := views[l.name]
		if !ok || !v.Open || v.Agent == "" {
			continue
		}
		s.mu.Lock()
		res := s.work[l.name]
		loop, lok := s.genLoops[l.name]
		prof, pok := s.genProf[l.name]
		cur, seen := s.docAgent[l.name]
		st := s.pickCnt[l.name]
		s.mu.Unlock()
		if !lok || !pok {
			continue
		}
		if !seen {
			cur = primaryCandidate(res)
			s.mu.Lock()
			if dup, ok := s.docAgent[l.name]; ok {
				cur = dup
			} else {
				s.docAgent[l.name] = cur
			}
			s.mu.Unlock()
		}
		if v.Agent == cur {
			if st.agent != "" {
				s.mu.Lock()
				delete(s.pickCnt, l.name)
				s.mu.Unlock()
			}
			continue
		}
		if st.agent == v.Agent {
			st.n++
		} else {
			st = pickStable{agent: v.Agent, n: 1}
		}
		s.mu.Lock()
		s.pickCnt[l.name] = st
		s.mu.Unlock()
		_, over := candidates(res)
		blocked := cur == "" || in.block(cur, over, now) != ""
		if !blocked && st.n < fallbackStableTicks {
			continue
		}
		s.mu.Lock()
		_, running := s.procs[l.name]
		s.mu.Unlock()
		if running && !s.noSession(l.name) {
			continue
		}
		s.rewriteAgent(l.name, v.Agent, cur, loop, prof, res, in, now)
	}
}

// rewriteAgent renders the loop's document for the newly picked agent
// and replaces the managed copy, keeping a .bak when the file differs.
func (s *supervisor) rewriteAgent(name, agent, prev string, loop gen.Loop, p gen.Profile, res config.Resolved, in gateInputs, now time.Time) {
	s.mu.Lock()
	agents := s.agents
	s.mu.Unlock()
	newLoop, ok := loop.ForAgent(agents, agent)
	if !ok {
		return
	}
	if !p.SupportsAgent(newLoop.Runtime) {
		fmt.Fprintf(os.Stderr, "jig: %s: agent kind %q is not supported, staying on %s\n", name, newLoop.Runtime, prev)
		return
	}
	jigPath, err := selfExe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "jig:", err)
		return
	}
	text, err := gen.Render(p, newLoop, agents, jigPath, defaultPrompt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jig:", err)
		return
	}
	if err := gen.AtomicWrite(gen.DocPath(s.root, name), text); err != nil {
		fmt.Fprintln(os.Stderr, "jig:", err)
		return
	}
	trigger, usable := fallbackTrigger(in, res, prev, now)
	if agent == primaryCandidate(res) {
		trigger, usable = "primary usable again", now
	}
	s.mu.Lock()
	s.docAgent[name] = agent
	if agent == primaryCandidate(res) {
		delete(s.fallback, name)
	} else {
		s.fallback[name] = fallbackInfo{from: prev, to: agent, trigger: trigger, at: now, usable: usable}
	}
	delete(s.pickCnt, name)
	s.mu.Unlock()
	fmt.Fprintf(os.Stderr, "jig: %s: agent changed from %q to %q: %s\n", name, prev, agent, trigger)
	s.republish()
}

// fallbackTrigger names what drove the previous agent out and when the
// primary is usable again.
func fallbackTrigger(in gateInputs, res config.Resolved, prev string, now time.Time) (string, time.Time) {
	if _, ok := in.agents[prev]; !ok {
		return "unknown agent", time.Time{}
	}
	_, over := candidates(res)
	b := in.blockOf(prev, over, now)
	switch {
	case b.exhausted:
		return "usage limit", in.state.Agents[prev].ExhaustedUntil
	case b.hours:
		return "outside working hours", in.schedule(prev, over).Next(now)
	case b.refs:
		return "unresolved environment references", time.Time{}
	case b.runtime:
		return "runtime not registered", time.Time{}
	default:
		return "not available", time.Time{}
	}
}

// primaryCandidate is the loop's first candidate agent: the primary the
// document is generated for and the gate prefers.
func primaryCandidate(res config.Resolved) string {
	names, _ := candidates(res)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// recordLoops writes the per-loop decisions that changed to the state
// file, logs each agent change with its reason once, and reports whether
// anything changed.
func (s *supervisor) recordLoops(in gateInputs, now time.Time, views map[string]loopView) bool {
	s.mu.Lock()
	updates := map[string]machine.LoopState{}
	for name, v := range views {
		rec := machine.LoopState{Agent: v.Agent, OfflineReason: v.Reason, ETA: v.ETA}
		prev := s.recorded[name]
		if rec == prev {
			continue
		}
		updates[name] = rec
		s.recorded[name] = rec
		if prev.Agent != "" && rec.Agent != "" && prev.Agent != rec.Agent {
			_, over := candidates(in.work[name])
			fmt.Fprintf(os.Stderr, "jig: %s: agent changed from %q to %q: %s\n",
				name, prev.Agent, rec.Agent, in.block(prev.Agent, over, now))
		}
	}
	path := s.statePath
	s.mu.Unlock()
	if len(updates) == 0 {
		return false
	}
	if err := machine.UpdateState(path, func(st *machine.State) {
		for n, rec := range updates {
			st.Loops[n] = rec
		}
	}); err != nil {
		fmt.Fprintln(os.Stderr, "jig: state:", err)
	}
	return true
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
// running. A loop whose sessions cannot be accounted for is not.
func (s *supervisor) noSession(name string) bool {
	s.mu.Lock()
	p := s.procs[name]
	if p == nil || time.Since(p.since) < drainSettle {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	active, known := s.sessions(name)
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
			fmt.Fprintln(os.Stderr, "jig: gate open, starting the loops it held")
		} else {
			s.logClosed(v, cfg)
		}
	case v.Open && prev.Runtime != v.Runtime:
		fmt.Fprintf(os.Stderr, "jig: agent runtime changed from %q to %q, loops restart as they go idle\n", prev.Runtime, v.Runtime)
	}
	return prev.Open != v.Open || prev.HoursOpen != v.HoursOpen || prev.Runtime != v.Runtime ||
		!prev.Next.Equal(v.Next) || prev.hold() != v.hold()
}

func (s *supervisor) logClosed(v gateView, cfg *machine.Config) {
	how := "loops stop as they go idle"
	if v.Reason == gateHours && cfg.Hours.End == machine.EndStop {
		how = "stopping loops now"
	}
	fmt.Fprintf(os.Stderr, "jig: gate closed (%s), %s\n", v.hold(), how)
}
