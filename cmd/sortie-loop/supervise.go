package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/sortie-loop/internal/config"
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
	def  loopDef
	cmd  *exec.Cmd
	port int
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
	// ports is the port each loop last bound. A loop is restarted on the
	// port it already published, so a rebound port stays visible to
	// whoever reads the registry.
	ports map[string]int

	mu    sync.Mutex
	procs map[string]*loopProc
	// exitWhenEmpty ends the process once the loops it was asked to stop
	// are gone.
	exitWhenEmpty bool

	// gone holds the loops that have exited, so the status document can
	// still report their exit code.
	gone      map[string]*loopProc
	startedAt time.Time
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
	quiet     func(port int) (idle, known bool)
	pollEvery time.Duration

	budgets       map[string]config.Budget
	budgetStates  map[string]*budgetState
	budgetUse     map[string]budgetUsage
	budgetPolled  map[string]time.Time
	budgetReadErr map[string]string
}

func newSupervisor(root, bin string, defs []loopDef, ports []int) *supervisor {
	s := &supervisor{
		root: root, bin: bin,
		defs:      map[string]loopDef{},
		ports:     map[string]int{},
		procs:     map[string]*loopProc{},
		gone:      map[string]*loopProc{},
		startedAt: time.Now(),
		events:    make(chan exitEvent, len(defs)+4),
		clock:     time.Now, quiet: loopQuiet, pollEvery: sleepPollEvery,
		budgets: map[string]config.Budget{}, budgetStates: map[string]*budgetState{},
		budgetUse: map[string]budgetUsage{}, budgetPolled: map[string]time.Time{},
		budgetReadErr: map[string]string{},
	}
	for i, d := range defs {
		s.defs[d.name] = d
		if i < len(ports) {
			s.ports[d.name] = ports[i]
		}
	}
	return s
}

// childEnv resolves everything a child needs that is not the workflow
// itself: the env file written from the current config, and the
// environment to launch it with.
//
// Both are read from disk on every call rather than once at boot. A
// supervisor rewrites config.yaml and the env file and then asks for a
// restart, and a child launched against the settings from boot would
// keep the old ones with nothing to show for it.
func (s *supervisor) childEnv(name string) (envFile string, env []string, err error) {
	cfg, err := config.Load(s.root)
	if err != nil {
		return "", nil, err
	}
	if envFile, err = writeEnvFile(s.root, cfg); err != nil {
		return "", nil, err
	}
	// The env override replaces the workflow's query_filter, so
	// defaultFilters must mirror each loop's label clauses (the github-pr
	// adapter enforces label: client-side); a filters: entry narrows
	// further, it does not add to the default.
	env = append(os.Environ(), cfg.Env()...)
	if filter := cfg.FilterFor(name); filter != "" {
		env = append(env, "SORTIE_TRACKER_QUERY_FILTER="+filter)
	}
	return envFile, env, nil
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

// startOne launches one loop on the port it last used, so a loop that is
// replaced keeps the address it already published.
func (s *supervisor) startOne(name string) error {
	def, ok := s.defs[name]
	if !ok {
		return fmt.Errorf("no loop %q", name)
	}
	envFile, env, err := s.childEnv(name)
	if err != nil {
		return err
	}
	workflow, err := os.ReadFile(filepath.Join(s.root, ".sortie", "workflows", def.file))
	if err != nil {
		return err
	}
	envBody, err := os.ReadFile(envFile)
	if err != nil {
		return err
	}
	var logw io.Writer
	var logf *rotatingLog
	if lf, err := openRotatingLog(childLogPath(s.root, name), childLogMaxBytes, childLogKeep); err != nil {
		fmt.Fprintln(os.Stderr, "sortie-loop: child log:", err)
	} else {
		logf, logw = lf, lf
	}
	cmd, port, err := startLoopChild(s.bin, envFile, s.root, def.file, env, s.ports[name], logw)
	if err != nil {
		if logf != nil {
			_ = logf.Close()
		}
		return fmt.Errorf("start %s loop: %w", name, err)
	}
	s.mu.Lock()
	s.ports[name] = port
	p := &loopProc{
		def: def, cmd: cmd, port: port, since: time.Now(),
		configHash: configHash(workflow, envBody, lastEnv(env, "SORTIE_TRACKER_QUERY_FILTER")), loopType: loopTypeOf(workflow),
	}
	if prev := s.gone[name]; prev != nil {
		p.restarts = prev.restarts + 1
		delete(s.gone, name)
	}
	s.procs[name] = p
	s.idleSince = s.clock()
	s.mu.Unlock()
	go func() {
		err := cmd.Wait()
		if logf != nil {
			_ = logf.Close()
		}
		s.events <- exitEvent{loop: name, err: err}
	}()
	// Republished here rather than only at boot: a loop that is replaced
	// leaves the registry advertising nothing for a moment, and the entry
	// is what points a supervisor at its port and its dashboard.
	s.republish()
	return nil
}

// startAll launches every loop in the run except the paused ones.
func (s *supervisor) startAll(names []string) error {
	for _, n := range names {
		if s.isPaused(n) {
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

// run supervises the loops until the process should end, and returns the
// exit code.
func (s *supervisor) run() int {
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, sigStopIdle, sigRestartIdle)
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
			if code, done := s.sleepStep(); done {
				return code
			}
		}
	}
}

func (s *supervisor) onSignal(sg os.Signal) (int, bool) {
	switch sg {
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
		fmt.Fprintln(os.Stderr, "sortie-loop:", p.def.name, "exited:", ev.err)
		s.republish()
		return 1, true
	}
	s.republish()
	if p.recycle && !s.isPaused(p.def.name) {
		if err := s.startOne(p.def.name); err != nil {
			fmt.Fprintln(os.Stderr, "sortie-loop:", err)
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
		// The poll is HTTP, so it happens with the lock released.
		idle, known := loopQuiet(p.port)
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
			Port: p.port, DBPath: dbPathFor(s.root, p.def.file),
			WorkflowFile: filepath.Join(s.root, ".sortie", "workflows", p.def.file),
			LoopType:     p.loopType,
		})
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].Loop < eps[j].Loop })
	return eps
}

// republish rewrites this repo's registry entry from the loops actually
// running, so a rebound port or a stopped loop is visible to whatever
// reads the registry.
func (s *supervisor) republish() {
	if err := writeRegistry(s.root, s.endpoints(), s.startedAt); err != nil {
		fmt.Fprintln(os.Stderr, "sortie-loop: registry:", err)
	}
	s.publishStatus("running")
}

func (s *supervisor) publishStatus(state string) {
	if err := writeStatus(s.statusDoc(state)); err != nil {
		fmt.Fprintln(os.Stderr, "sortie-loop: status:", err)
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
		SchemaVersion: statusSchemaVersion,
		Version:       version,
		EngineVersion: engineVersion(),
		PID:           os.Getpid(),
		Repo:          s.root,
		RepoName:      repoName(s.root),
		StartedAt:     s.startedAt.UTC(),
		UpdatedAt:     time.Now().UTC(),
		State:         state,
		Workflows:     []workflowStatus{},
		Cleanup:       readCleanupRecord(s.root),
		Sleep:         s.sleepStatusLocked(),
	}
	add := func(p *loopProc) {
		st := "running"
		bs := s.budgetStatusLocked(p.def.name)
		switch {
		case bs != nil && bs.Paused:
			st = "paused"
		case s.sleeping:
			st = "sleeping"
		case p.exitCode != nil:
			st = "exited"
		case p.stopping:
			st = "stopping"
		}
		pid := 0
		if p.cmd != nil && p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
		}
		doc.Workflows = append(doc.Workflows, workflowStatus{
			Name: p.def.name, WorkflowFile: p.def.file, LoopType: p.loopType,
			Port: p.port, PID: pid, State: st, StartedAt: p.since.UTC(),
			RestartCount: p.restarts, LastExitCode: p.exitCode, ConfigHash: p.configHash,
			LogPath: childLogPath(s.root, p.def.name), Budget: bs,
		})
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
		wf, _ := os.ReadFile(filepath.Join(s.root, ".sortie", "workflows", def.file))
		doc.Workflows = append(doc.Workflows, workflowStatus{
			Name: name, WorkflowFile: def.file, LoopType: loopTypeOf(wf), Port: s.ports[name],
			State: "paused", StartedAt: s.startedAt.UTC(), LogPath: childLogPath(s.root, name),
			Budget: s.budgetStatusLocked(name),
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

// loopQuiet reports whether a loop has no work in flight, and whether
// the engine answered at all.
//
// A pending retry counts as work: the loop will start that run by itself,
// so stopping now would only cancel it. An engine that does not answer
// is unknown, not idle, and must never be read as a loop with nothing to
// lose.
func loopQuiet(port int) (idle, known bool) {
	if port == 0 {
		return false, false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/state", port))
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	var p statePayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p); err != nil {
		return false, false
	}
	return len(p.Running) == 0 && len(p.Retrying) == 0 && p.Counts.Retrying == 0, true
}
