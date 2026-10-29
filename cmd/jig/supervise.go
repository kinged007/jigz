package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/gen"
	"github.com/kinged007/jigz/internal/machine"
	"github.com/kinged007/jigz/internal/plan"
	"github.com/kinged007/jigz/internal/rollup"
)

// Control signals. A loop is stopped by signalling the process that owns
// it, and the only thing that may decide when that happens is the
// process watching the loop. A request has to reach that process, and
// because it is already running in the background a signal is the whole
// channel: the command line and the dashboard buttons below are just two
// clients of these.
//
// Only the two ways of stopping *politely* need a signal. Stopping now
// is what SIGTERM already means, and a caller that wants an immediate
// restart stops the process and starts a new one, which re-reads
// everything anyway. Two signals is one short of the portable set, so
// there is room for exactly these.
var (
	// sigStopIdle stops every loop as each falls idle, then exits.
	sigStopIdle = syscall.SIGUSR1
	// sigRestartIdle replaces every loop, one at a time, each at its
	// first moment with no work in flight, and stays up.
	sigRestartIdle = syscall.SIGUSR2
	// sigReload asks the supervisor to re-read .jigz/config.yaml and
	// apply supervisor-level settings (sleep, cleanup, budgets) without
	// restarting children. SIGHUP is preferred over a third SIGUSR* so
	// the request stays a signal rather than a file. An accidental
	// terminal close also sends SIGHUP, but reload never exits the run:
	// validation failures only republish the status.
	sigReload = syscall.SIGHUP
)

// drainSettle is how long a freshly started loop is left alone before it
// can be judged idle. A loop that has just launched has not reported a
// run yet, and reading that as idle would stop a loop that was never
// given the chance to work.
const drainSettle = 20 * time.Second

// shutdownBudget is how long a loop gets to exit after SIGTERM before it
// is killed. Without it a loop that ignores the signal would hold the
// whole run open indefinitely.
const shutdownBudget = 30 * time.Second

// loopProc is one loop child and the supervisor's intent for it.
type loopProc struct {
	def loopDef
	cmd *exec.Cmd
	// live follows the sessions of the engine from its log.
	live *liveTracker
	// stopping marks a loop the supervisor asked to end, as opposed to
	// one that died on its own. It is what keeps a deliberate stop from
	// reading as a crash.
	stopping bool
	// drain delays that stop until the loop reports no work in flight.
	drain bool
	// recycle starts the loop again once it has stopped.
	recycle bool
	// since is when this child started, for the settle guard.
	since time.Time
	// restarts counts how many times the supervisor recycled this loop.
	restarts int
	// exitCode is nil until the child has exited.
	exitCode   *int
	configHash string
	loopType   string
	// runtime is the agent runtime the child was started with, empty
	// without a pool.
	runtime string
}

type exitEvent struct {
	loop string
	err  error
}

// supervisor owns this run's loop children. It is the only thing that
// decides when a loop ends, which is why a stop requested from outside
// arrives as a signal rather than as a call.
type supervisor struct {
	root string
	bin  string
	defs map[string]loopDef

	mu    sync.Mutex
	procs map[string]*loopProc
	// exitWhenEmpty ends the process once the loops it was asked to stop
	// are gone.
	exitWhenEmpty bool

	// gone holds the loops that have exited, so the status document can
	// still report their exit code.
	gone      map[string]*loopProc
	startedAt time.Time
	dash      dashInfo
	// failed marks a run that ended because a loop died unrequested.
	failed bool

	events chan exitEvent

	cleanupCancel context.CancelFunc
	cleanupWG     sync.WaitGroup

	sleepCfg      config.Sleep
	sleeping      bool
	sleepingSince time.Time
	nextWake      time.Time
	// idleSince is the first idle observation after the last busy one or
	// the last start; zero while the last observation was busy.
	idleSince time.Time
	lastPoll  time.Time
	wakeNow   bool
	clock     func() time.Time
	quiet     func(name string) (idle, known bool)
	pollEvery time.Duration

	budgets       map[string]config.Budget
	budgetStates  map[string]*budgetState
	budgetUse     map[string]budgetUsage
	budgetPolled  map[string]time.Time
	budgetReadErr map[string]string

	machineState
	// gate is what the engine can run. The zero value allows everything.
	gate engine.Gate
	// disabled is why a loop the gate does not allow is not running.
	disabled map[string]string
	// sessions reports whether a loop has a session running; retries do
	// not count.
	sessions func(name string) (active, known bool)
	// docAgent is the agent the on-disk document was rendered for, by
	// loop. genLoops and genProf re-render it when the gate picks a
	// standby agent. pickCnt counts consecutive ticks with the same
	// pick, so the return to the primary waits out flapping.
	// fallback records the last standby switch per loop for status.
	docAgent map[string]string
	genLoops map[string]gen.Loop
	genProf  map[string]gen.Profile
	pickCnt  map[string]pickStable
	fallback map[string]fallbackInfo
}

func newSupervisor(root, bin string, defs []loopDef) *supervisor {
	s := &supervisor{
		root: root, bin: bin,
		defs:      map[string]loopDef{},
		procs:     map[string]*loopProc{},
		gone:      map[string]*loopProc{},
		startedAt: time.Now(),
		events:    make(chan exitEvent, len(defs)+4),
		clock:     time.Now, pollEvery: sleepPollEvery,
		budgets: map[string]config.Budget{}, budgetStates: map[string]*budgetState{},
		budgetUse: map[string]budgetUsage{}, budgetPolled: map[string]time.Time{},
		budgetReadErr: map[string]string{},
		disabled:      map[string]string{},
		docAgent:      map[string]string{},
		genLoops:      map[string]gen.Loop{},
		genProf:       map[string]gen.Profile{},
		pickCnt:       map[string]pickStable{},
		fallback:      map[string]fallbackInfo{},
	}
	s.quiet, s.sessions = s.loopQuiet, s.loopSessions
	s.initState()
	for _, d := range defs {
		s.defs[d.name] = d
	}
	return s
}

// childEnv resolves everything a child needs that is not the workflow
// itself: the env file written from the current config, and the
// environment to launch it with. The gate is re-evaluated here because
// the decision and the start read the clock separately, so an agent
// that became unavailable between them holds the loop instead of
// starting a child that would fail at once.
//
// Both are read from disk on every call rather than once at boot. A
// supervisor rewrites config.yaml and the env file and then asks for a
// restart, and a child launched against the settings from boot would
// keep the old ones with nothing to show for it.
func (s *supervisor) childEnv(name string) (envFile string, env []string, runtime string, err error) {
	cfg, err := config.Load(s.root)
	if err != nil {
		return "", nil, "", err
	}
	if envFile, err = writeEnvFile(s.root, cfg); err != nil {
		return "", nil, "", err
	}
	s.refreshState(s.clock(), false)
	s.mu.Lock()
	in := s.gateInputsLocked()
	res, ok := s.work[name]
	agents := s.agents
	s.mu.Unlock()
	now := s.clock()
	if ok {
		if lv := in.workView(res, now); !lv.Open {
			reason := lv.Reason
			if reason == "" {
				reason = "no agent available"
			}
			return "", nil, "", fmt.Errorf("the gate holds the loop: %s: %w", reason, errNoAgent)
		}
		if len(res.Agents) > 0 {
			if a, known := agents[res.Agents[0]]; known {
				runtime = a.Runtime
			}
		}
	}
	// The env override replaces the workflow's query_filter; a filters:
	// entry narrows further, it does not add to the default. For
	// generated loops the query is the loop's narrowed filter, which
	// keeps split loops from claiming each other's items; otherwise the
	// legacy lookup by loop name applies.
	env = append(os.Environ(), cfg.Env()...)
	env = append(env, "JIG_STATE_PATH="+machine.StatePath(s.root))
	if q, ok := s.queryFor(name); ok && strings.TrimSpace(q) != "" {
		env = append(env, "SORTIE_TRACKER_QUERY_FILTER="+q+" assignee:@me")
	} else if filter := cfg.FilterFor(name); filter != "" {
		env = append(env, "SORTIE_TRACKER_QUERY_FILTER="+filter)
	}
	return envFile, env, runtime, nil
}

// lastEnv returns the last value set for key, as a child would see it.
func lastEnv(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			val = v
		}
	}
	return val
}

// startOne launches one loop.
func (s *supervisor) startOne(name string) error {
	def, ok := s.defs[name]
	if !ok {
		return fmt.Errorf("no loop %q", name)
	}
	envFile, env, runtime, err := s.childEnv(name)
	if errors.Is(err, errNoAgent) {
		s.mu.Lock()
		first := !s.gateHeld[name]
		s.gateHeld[name] = true
		s.mu.Unlock()
		if first {
			fmt.Fprintf(os.Stderr, "jig: %s: %v\n", name, err)
		}
		s.republish()
		return nil
	}
	if err != nil {
		return err
	}
	workflow, err := os.ReadFile(filepath.Join(s.root, ".jigz", "workflows", def.file))
	if err != nil {
		return err
	}
	envBody, err := os.ReadFile(envFile)
	if err != nil {
		return err
	}
	s.mu.Lock()
	gate := s.gate
	s.mu.Unlock()
	lg := plan.GateLoop(gate, string(workflow), env)
	if lg.Off != "" {
		s.mu.Lock()
		_, again := s.disabled[name]
		s.disabled[name] = lg.Off
		delete(s.gateHeld, name)
		s.mu.Unlock()
		if !again {
			fmt.Fprintf(os.Stderr, "jig: %s: %s\n", name, lg.Off)
		}
		s.republish()
		return nil
	}
	var logw io.Writer
	var logf *rotatingLog
	if lf, err := openRotatingLog(childLogPath(s.root, name), childLogMaxBytes, childLogKeep); err != nil {
		fmt.Fprintln(os.Stderr, "jig: child log:", err)
	} else {
		logf, logw = lf, lf
	}
	live := newLiveTracker(s.issueDisplay, func() { s.publishStatus("running") })
	cmd, err := startLoopChild(s.bin, envFile, s.root, def.file, env, live, logw)
	if err != nil {
		if logf != nil {
			_ = logf.Close()
		}
		return fmt.Errorf("start %s loop: %w", name, err)
	}
	s.mu.Lock()
	p := &loopProc{
		def: def, cmd: cmd, live: live, since: time.Now(),
		configHash: configHash(workflow, envBody, lastEnv(env, "SORTIE_TRACKER_QUERY_FILTER")), loopType: loopTypeOf(workflow),
		runtime: runtime,
	}
	if prev := s.gone[name]; prev != nil {
		p.restarts = prev.restarts + 1
		delete(s.gone, name)
	}
	s.procs[name] = p
	delete(s.gateHeld, name)
	delete(s.disabled, name)
	s.idleSince = s.clock()
	s.mu.Unlock()
	go func() {
		err := cmd.Wait()
		if logf != nil {
			_ = logf.Close()
		}
		s.events <- exitEvent{loop: name, err: err}
	}()
	s.republish()
	return nil
}

// startAll launches every loop in the run except the paused ones and
// the ones the machine gate keeps down.
func (s *supervisor) startAll(names []string) error {
	for _, n := range names {
		if !s.startable(n) {
			continue
		}
		if err := s.startOne(n); err != nil {
			return err
		}
	}
	return nil
}

// stopAll marks every loop, then either signals it now or leaves it to
// the drain pass. recycle starts each one again once it has stopped.
func (s *supervisor) stopAll(drain, recycle bool) {
	s.mu.Lock()
	for _, p := range s.procs {
		p.stopping, p.drain, p.recycle = true, drain, recycle
	}
	s.mu.Unlock()
	if drain {
		return
	}
	for _, name := range s.names() {
		s.terminate(name)
	}
}

// terminate signals one loop and clears its drain: from here the stop
// waits on nothing but the process.
func (s *supervisor) terminate(name string) {
	s.mu.Lock()
	p := s.procs[name]
	if p == nil {
		s.mu.Unlock()
		return
	}
	p.drain = false
	cmd := p.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

// reload re-reads the configuration, picks up new loop documents, and
// restarts the loops whose query changed: the engine reads the filter
// only at start. A changed loop drains first, so in-flight work
// finishes; a loop that is not running is held for the gate to start.
func (s *supervisor) reload() {
	cfg, err := config.Load(s.root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jig: reload:", err)
		s.republish()
		return
	}
	if err := cfg.CheckCleanup(); err != nil {
		fmt.Fprintln(os.Stderr, "jig: reload:", err)
		s.republish()
		return
	}
	if err := cfg.CheckBudgets(); err != nil {
		fmt.Fprintln(os.Stderr, "jig: reload:", err)
		s.republish()
		return
	}
	for name, b := range cfg.Budgets() {
		if b.MaxUSD > 0 {
			fmt.Fprintf(os.Stderr, "jig: work.%s.budget.max_usd is ignored: the engine records no cost per run\n", name)
		}
	}
	s.mu.Lock()
	s.sleepCfg = cfg.Sleep
	s.mu.Unlock()
	s.reloadBudgets(cfg.Budgets())
	s.reloadCleanup(cfg)
	old := s.querySnapshot()
	s.refreshState(s.clock(), true)
	for name, q := range s.querySnapshot() {
		if oq, ok := old[name]; ok && oq != q {
			fmt.Fprintf(os.Stderr, "jig: %s: query changed, draining and restarting\n", name)
			s.recycleDrain(name)
		}
	}
	s.mergeDiscoveredDefs()
	s.republish()
	fmt.Fprintln(os.Stderr, "jig: reloaded config")
}

// mergeDiscoveredDefs picks up loop documents that appeared since boot:
// generation on reload writes them, and the supervisor starts them
// through the gate like any other loop. Removed documents keep their
// running loops; deleting a loop is a stop.
func (s *supervisor) mergeDiscoveredDefs() {
	for _, d := range listWorkflows(s.root) {
		s.mu.Lock()
		_, ok := s.defs[d.name]
		if !ok {
			s.defs[d.name] = d
		}
		s.mu.Unlock()
		if !ok {
			fmt.Fprintf(os.Stderr, "jig: %s: new loop document, starting\n", d.name)
		}
	}
}

func (s *supervisor) reloadBudgets(budgets map[string]config.Budget) {
	s.mu.Lock()
	filtered := map[string]config.Budget{}
	for n, b := range budgets {
		if _, ok := s.defs[n]; ok && b.MaxTokens > 0 {
			filtered[n] = b
		}
	}
	if len(filtered) == len(s.budgets) {
		match := true
		for n, b := range filtered {
			if cur, ok := s.budgets[n]; !ok || cur != b {
				match = false
				break
			}
		}
		if match {
			s.mu.Unlock()
			return
		}
	}
	s.budgets = filtered
	s.mu.Unlock()
	s.budgetPoll(true)
	s.saveBudgetStates()
}

func (s *supervisor) reloadCleanup(cfg *config.Config) {
	s.mu.Lock()
	wasEnabled := s.cleanupCancel != nil
	s.mu.Unlock()
	if wasEnabled || cfg.Cleanup.Enabled {
		s.stopCleanup()
		if cfg.Cleanup.Enabled {
			s.startCleanup(cfg)
		}
	}
}

func (s *supervisor) run() int {
	sig := make(chan os.Signal, 5)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, sigStopIdle, sigRestartIdle, sigReload)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case ev := <-s.events:
			if code, done := s.onExit(ev); done {
				return code
			}
		case sg := <-sig:
			if code, done := s.onSignal(sg); done {
				return code
			}
		case <-tick.C:
			if code, done := s.drainStep(); done {
				return code
			}
			if code, done := s.budgetStep(); done {
				return code
			}
			if code, done := s.gateStep(); done {
				return code
			}
			if code, done := s.sleepStep(); done {
				return code
			}
		}
	}
}

func (s *supervisor) onSignal(sg os.Signal) (int, bool) {
	switch sg {
	case sigReload:
		s.reload()
		return 0, false
	case syscall.SIGINT, syscall.SIGTERM:
		s.stopAll(false, false)
		s.exitWhenEmpty = true
	case sigStopIdle:
		s.stopAll(true, false)
		s.exitWhenEmpty = true
	case sigRestartIdle:
		s.mu.Lock()
		asleep := s.sleeping
		s.wakeNow = s.wakeNow || asleep
		s.mu.Unlock()
		if asleep {
			return s.sleepStep()
		}
		s.stopAll(true, true)
	}
	return 0, s.finished()
}

func (s *supervisor) onExit(ev exitEvent) (int, bool) {
	p := s.reap(ev)
	if p == nil {
		return 0, false
	}
	if !p.stopping {
		s.failed = true
		// Nobody asked for this loop to end, so it died on its own and
		// the repo is no longer whole. Everything goes down, as before: a
		// run left watching half a repo is worse than none, and a
		// supervisor reads the exit as a failure it can attribute.
		fmt.Fprintln(os.Stderr, "jig:", p.def.name, "exited:", ev.err)
		s.republish()
		return 1, true
	}
	s.republish()
	if p.recycle && s.startable(p.def.name) {
		if err := s.startOne(p.def.name); err != nil {
			fmt.Fprintln(os.Stderr, "jig:", err)
			s.failed = true
			return 1, true
		}
	}
	return 0, s.finished()
}

// reap moves an exited loop from procs to gone with its exit code.
func (s *supervisor) reap(ev exitEvent) *loopProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.procs[ev.loop]
	if p == nil {
		return nil
	}
	delete(s.procs, ev.loop)
	code := 0
	if ev.err != nil {
		code = -1
		var xe *exec.ExitError
		if errors.As(ev.err, &xe) {
			code = xe.ExitCode()
		}
	}
	p.exitCode = &code
	s.gone[ev.loop] = p
	return p
}

// drainStep stops the loops that were asked to stop once idle and are
// now quiet.
//
// The wait has to happen here. The engine aborts a run the instant it
// is signalled, and it has no signal for "finish the current run first",
// so the only way to stop without losing in-flight work is for whoever
// owns the process to wait for the quiet and then send the signal.
func (s *supervisor) drainStep() (int, bool) {
	s.mu.Lock()
	var due []*loopProc
	for _, p := range s.procs {
		if p.stopping && p.drain && time.Since(p.since) >= drainSettle {
			due = append(due, p)
		}
	}
	s.mu.Unlock()
	for _, p := range due {
		// The poll reads a database, so it happens with the lock released.
		idle, known := s.quiet(p.def.name)
		if !known || !idle {
			continue
		}
		s.terminate(p.def.name)
	}
	return 0, s.finished()
}

// finished reports whether the run has nothing left to do.
func (s *supervisor) finished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitWhenEmpty && len(s.procs) == 0
}

func (s *supervisor) empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs) == 0
}

func (s *supervisor) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.procs))
	for n := range s.procs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// draining reports which loops are waiting to stop.
func (s *supervisor) draining() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for n, p := range s.procs {
		if p.stopping && p.drain {
			out[n] = true
		}
	}
	return out
}

// endpoints describes the loops currently running, for the dashboard and
// the shared registry.
func (s *supervisor) endpoints() []loopEndpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	eps := make([]loopEndpoint, 0, len(s.procs))
	for _, p := range s.procs {
		eps = append(eps, loopEndpoint{
			Repo: s.root, RepoName: repoName(s.root), Loop: p.def.name,
			DBPath:       dbPathFor(s.root, p.def.file),
			WorkflowFile: filepath.Join(s.root, ".jigz", "workflows", p.def.file),
			LoopType:     p.loopType,
		})
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].Loop < eps[j].Loop })
	return eps
}

// republish rewrites this repo's registry entry from the loops actually
// running, so a stopped loop is visible to whatever reads the registry.
func (s *supervisor) republish() {
	if err := writeRegistry(s.root, s.endpoints(), s.startedAt, s.dash); err != nil {
		fmt.Fprintln(os.Stderr, "jig: registry:", err)
	}
	s.publishStatus("running")
}

func (s *supervisor) publishStatus(state string) {
	if err := writeStatus(s.statusDoc(state)); err != nil {
		fmt.Fprintln(os.Stderr, "jig: status:", err)
	}
}

// closeStatus runs once the loops are gone. A clean exit removes the
// status file; a run that ended on a loop failure leaves a final
// document with the exit codes.
func (s *supervisor) closeStatus() {
	if s.failed {
		s.publishStatus("exited")
		return
	}
	removeStatus(s.root)
}

func (s *supervisor) statusDoc(state string) statusDoc {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := statusDoc{
		SchemaVersion:        statusSchemaVersion,
		Version:              version,
		EngineVersion:        s.engineVersion(),
		TestedSchemaVersions: testedSchemaVersions,
		PID:                  os.Getpid(),
		Repo:                 s.root,
		RepoName:             repoName(s.root),
		StartedAt:            s.startedAt.UTC(),
		UpdatedAt:            time.Now().UTC(),
		State:                state,
		Workflows:            []workflowStatus{},
		Cleanup:              readCleanupRecord(s.root),
		Sleep:                s.sleepStatusLocked(),
	}
	in := s.gateInputsLocked()
	drifts, derr := driftReports(s.root)
	if derr != nil {
		fmt.Fprintln(os.Stderr, "jig: drift:", derr)
	}
	now := s.clock()
	primaryOf := func(name string) string {
		return primaryCandidate(s.work[name])
	}
	fallbackOf := func(name string) string {
		fb, ok := s.fallback[name]
		if !ok {
			return ""
		}
		return fb.String(primaryOf(name))
	}
	loopInfo := func(name string, bs *budgetStatus) loopView {
		lv := in.loopView(name, now)
		if bs != nil && bs.Paused {
			lv.Reason, lv.ETA = "", time.Time{}
		}
		return lv
	}
	add := func(p *loopProc) {
		bs := s.budgetStatusLocked(p.def.name)
		lv := loopInfo(p.def.name, bs)
		st := "running"
		switch {
		case bs != nil && bs.Paused:
			st = "paused"
		case s.gateHeld[p.def.name] && p.exitCode != nil:
			st = "offline"
		case s.sleeping:
			st = "sleeping"
		case p.exitCode != nil:
			st = "exited"
		case p.stopping && p.drain:
			st = "draining"
		case p.stopping:
			st = "stopping"
		case !lv.Open:
			st = "draining"
		}
		pid := 0
		if p.cmd != nil && p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
		}
		w := workflowStatus{
			Name: p.def.name, WorkflowFile: p.def.file, LoopType: p.loopType,
			PID: pid, State: st, StartedAt: p.since.UTC(),
			RestartCount: p.restarts, LastExitCode: p.exitCode, ConfigHash: p.configHash,
			LogPath: childLogPath(s.root, p.def.name), Budget: bs,
			OfflineReason: lv.Reason, ETA: timePtr(lv.ETA), Agent: lv.Agent,
			Drift:   drifts[p.def.name],
			Primary: primaryOf(p.def.name), FallbackReason: fallbackOf(p.def.name),
		}
		if p.exitCode == nil {
			w.Sessions, w.SessionsKnown = p.live.snapshot()
			// One loop runs one agent at a time: agent changes happen at
			// idle, so every running session belongs to the current agent.
			for i := range w.Sessions {
				w.Sessions[i].Agent = lv.Agent
				if w.Sessions[i].Agent == "" {
					w.Sessions[i].Agent = p.runtime
				}
			}
		}
		doc.Workflows = append(doc.Workflows, w)
	}
	for _, p := range s.procs {
		add(p)
	}
	for _, p := range s.gone {
		add(p)
	}
	for name, def := range s.defs {
		if s.procs[name] != nil || s.gone[name] != nil {
			continue
		}
		wf, _ := os.ReadFile(filepath.Join(s.root, ".jigz", "workflows", def.file))
		bs := s.budgetStatusLocked(name)
		lv := loopInfo(name, bs)
		st, offline := "paused", lv.Reason
		if s.gateHeld[name] && (bs == nil || !bs.Paused) {
			st = "offline"
		}
		if why, ok := s.disabled[name]; ok {
			st, offline = "disabled", why
		}
		doc.Workflows = append(doc.Workflows, workflowStatus{
			Name: name, WorkflowFile: def.file, LoopType: loopTypeOf(wf),
			State: st, StartedAt: s.startedAt.UTC(), LogPath: childLogPath(s.root, name),
			Budget: bs, OfflineReason: offline, ETA: timePtr(lv.ETA), Agent: lv.Agent,
			Drift:   drifts[name],
			Primary: primaryOf(name), FallbackReason: fallbackOf(name),
		})
	}
	sort.Slice(doc.Workflows, func(i, j int) bool { return doc.Workflows[i].Name < doc.Workflows[j].Name })
	return doc
}

// shutdown stops every remaining loop and waits for it, so a run that is
// ending leaves no engine behind holding a GitHub token.
func (s *supervisor) shutdown() {
	s.stopCleanup()
	s.stopAll(false, false)
	deadline := time.After(shutdownBudget)
	for !s.empty() {
		select {
		case ev := <-s.events:
			s.reap(ev)
		case <-deadline:
			s.mu.Lock()
			for _, p := range s.procs {
				if p.cmd != nil && p.cmd.Process != nil {
					_ = p.cmd.Process.Kill()
				}
			}
			s.mu.Unlock()
			// The kills land on their own; the waits still have to reap.
			for !s.empty() {
				s.reap(<-s.events)
			}
			return
		}
	}
}

// issueDisplay is an issue as the dashboard shows it, owner/repo#7.
func (s *supervisor) issueDisplay(issue string) string {
	if _, err := strconv.Atoi(issue); err != nil {
		return issue
	}
	return repoName(s.root) + "#" + issue
}

// loopSessions reports whether a loop has a session running, and whether
// the supervisor can tell. Unlike loopQuiet it ignores pending retries: the
// engine persists them, so they resume after a restart.
func (s *supervisor) loopSessions(name string) (active, known bool) {
	s.mu.Lock()
	p := s.procs[name]
	s.mu.Unlock()
	if p == nil {
		return false, false
	}
	sessions, known := p.live.snapshot()
	return len(sessions) > 0, known
}

// loopQuiet reports whether a loop has no work in flight, and whether
// the supervisor can tell.
//
// A pending retry counts as work: the loop will start that run by itself,
// so stopping now would only cancel it. A loop whose sessions cannot be
// accounted for is unknown, not idle, and must never be read as a loop
// with nothing to lose.
func (s *supervisor) loopQuiet(name string) (idle, known bool) {
	active, known := s.loopSessions(name)
	if !known {
		return false, false
	}
	if active {
		return false, true
	}
	s.mu.Lock()
	def := s.defs[name]
	s.mu.Unlock()
	retrying, err := rollup.CountRetrying(dbPathFor(s.root, def.file))
	if err != nil {
		return false, false
	}
	return retrying == 0, true
}
