package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/config"
	fleetconfig "github.com/kinged007/jigz/internal/fleetconfig"
	machine "github.com/kinged007/jigz/internal/host"
	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/plan"
	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/store"
	"github.com/kinged007/jigz/internal/supervise"
)

// repoSizes caches per-repository directory sizes on a timer so
// the render path never walks a workspace tree.
type repoSizes struct {
	mu sync.Mutex
	m  map[string]int64
	at time.Time
}

func newRepoSizes() *repoSizes {
	return &repoSizes{m: map[string]int64{}}
}

// get returns the last measured size for a repository. The second result
// is false until the first measurement lands, so the page shows a dash
// rather than zero for a repository nobody has measured yet.
func (s *repoSizes) get(slug string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.m[slug]
	return n, ok
}

func (s *repoSizes) set(slug string, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[slug] = n
	s.at = time.Now()
}

// measure walks every repository's directory. One slow tree delays the
// rest of the pass, which is acceptable on a ten minute timer.
func (s *repoSizes) measure(repos []supervise.Repo) {
	for _, r := range repos {
		if r.Dir == "" {
			continue
		}
		if n := machine.DirSize(r.Dir); n >= 0 {
			s.set(r.Slug, n)
		}
	}
}

// watchSizes measures on a timer until stop is closed.
func (a *app) watchSizes(stop <-chan struct{}) {
	if a.sizes == nil {
		return
	}
	a.sizes.measure(a.repos())
	t := time.NewTicker(sizeInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			a.sizes.measure(a.repos())
		}
	}
}

// sizeInterval is how often the per-repository disk usage is re-measured.
const sizeInterval = 10 * time.Minute

// app is the dashboard server's global state. It adapts Jigz's
// registry-based discovery to the interface Jigz's handlers expect.
type app struct {
	configPath string
	sup        *fleetSupervisor
	mu         sync.Mutex
	exposed    bool
	bindAddr   string
	sizes      *repoSizes
	sys        *machine.Reader
	source     loopsrc.Source
	stateDir   string
	github     fleetconfig.GitHub
	enricher   *collector.Enricher
	questions  *collector.Scanner
	store      *store.Store
	applyWait  time.Duration
	pollEvery  time.Duration
	verOnce    sync.Once
	versions   []pair
	alertCache dbAlertCache
	// hide is nil when nothing can be hidden, as in tests.
	hide *hider
	// liveHome is the home directory the agents keep their transcripts in.
	// Empty means the home of the user running the dashboard.
	liveHome string
	// unite is set when this page spans every registered repository,
	// which the status fragment states so a later run can find it.
	unite bool
}

// src returns the live fleet source. When the app is built with a
// StatusDoc source it provides wake/cleanup/log-tail; otherwise the
// Legacy source reads the registry and probes loop endpoints.
func (a *app) src() loopsrc.Source {
	if a.source != nil {
		return a.source
	}
	return loopsrc.Legacy{Reader: rollup.Reader{}}
}

// repos returns every supervised repository snapshot.
func (a *app) repos() []supervise.Repo {
	if a.sup == nil {
		return nil
	}
	return a.sup.Snapshot()
}

// projectRoots maps each project slug to its absolute repository root.
func (a *app) projectRoots() map[string]string {
	out := map[string]string{}
	for _, r := range a.repos() {
		if abs, err := filepath.Abs(r.Dir); err == nil {
			out[r.Slug] = abs
		}
	}
	return out
}

// validateWorkflow runs the engine's validator on one workflow file in
// the environment a real loop gets: this repository's own config, whose
// env carries the tracker token. The fleet cascade Jigz validates
// against has no counterpart here.
func validateWorkflow(dir, path string) (plan.FileResult, error) {
	bin, err := resolveSortieBin()
	if err != nil {
		return plan.FileResult{}, err
	}
	env := os.Environ()
	// The env file is what the loops themselves run with, and it keeps
	// the tracker token even when this process's environment lost it.
	if raw, err := os.ReadFile(filepath.Join(dir, ".jigz", ".env.loop")); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.Contains(line, "=") {
				env = append(env, line)
			}
		}
	} else if cfg, err := config.Load(dir); err == nil {
		env = append(env, cfg.Env()...)
	}
	cmd := exec.Command(bin, "validate", "--format", "json", path)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return plan.FileResult{}, fmt.Errorf("%s validate %s: %w: %s", bin, filepath.Base(path), err, msg)
	}
	var out plan.FileResult
	if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
		return plan.FileResult{}, fmt.Errorf("%s validate %s: unreadable output: %w", bin, filepath.Base(path), err)
	}
	return out.WithoutRuntimeBlockWarnings(), nil
}

// ---------------------------------------------------------------------------
// fleetSupervisor — thin shim over registry + signals
// ---------------------------------------------------------------------------

// fleetSupervisor adapts Jigz's registry-based process management
// to the interface Jigz's handlers expect from supervise.Supervisor.
// It discovers repos from the registry, reads status documents for state,
// and sends signals to the registered PIDs.
type fleetSupervisor struct {
	app *app
	// local is the supervisor this process runs, when it has one. Its
	// in-memory state is fresher than the registry file.
	local *supervisor
	// localOnly keeps the page on this process's own repository, which is
	// what a plain run shows. A --unite page spans the whole registry.
	localOnly bool
}

// snapshot returns one repository's live state.
func (fs *fleetSupervisor) snapshot(root string, ent registryEntry) supervise.Repo {
	if fs.local != nil && fs.local.root == root {
		return fs.localView()
	}
	doc, _ := readStatusDoc(root)
	state := supervise.StateRunning
	switch {
	case ent.PID == 0:
		// A run that exited cleanly left its entry behind so this page
		// can offer start again.
		state = supervise.StateStopped
	default:
		switch statusLiveness(doc) {
		case "stale":
			state = supervise.StateFailed
		case "exited":
			state = supervise.StateStopped
		}
		if !pidAlive(ent.PID) {
			state = supervise.StateStopped
		}
	}
	var loops []string
	for _, l := range ent.Loops {
		loops = append(loops, l.Loop)
	}
	if len(loops) == 0 {
		for _, w := range doc.Workflows {
			loops = append(loops, w.Name)
		}
	}
	sort.Strings(loops)
	slug := ent.RepoName
	if slug == "" {
		slug = repoName(root)
	}
	return supervise.Repo{
		Slug: slug, Dir: root, Loops: loops, State: state,
		Agents: workflowAgents(doc), PID: ent.PID, Since: ent.UpdatedAt,
	}
}

// localView builds the snapshot for the supervisor this process runs.
func (fs *fleetSupervisor) localView() supervise.Repo {
	s := fs.local
	doc := s.statusDoc("running")
	names := s.names()
	if len(names) == 0 {
		for _, w := range doc.Workflows {
			names = append(names, w.Name)
		}
	}
	state := supervise.StateRunning
	if len(names) == 0 {
		state = supervise.StateStopped
	}
	return supervise.Repo{
		Slug: doc.RepoName, Dir: s.root, Loops: names, State: state,
		Agents: workflowAgents(doc), PID: doc.PID, Since: doc.StartedAt,
	}
}

// target returns the PID a control signal should reach.
func (fs *fleetSupervisor) target(slug string) (int, error) {
	if fs.local != nil && (slug == "" || slug == repoName(fs.local.root)) {
		return os.Getpid(), nil
	}
	_, ent, ok := resolveRepo(slug, true, nil)
	if !ok {
		return 0, fmt.Errorf("unknown project %q", slug)
	}
	if ent.PID <= 0 || !pidAlive(ent.PID) {
		return 0, fmt.Errorf("%s is not running", slug)
	}
	return ent.PID, nil
}

// Start starts a stopped project by exec-ing a new Jigz process.
func (fs *fleetSupervisor) Start(slug string) error {
	_, ent, ok := resolveRepo(slug, true, nil)
	if !ok {
		return fmt.Errorf("unknown project %q", slug)
	}
	if ent.PID > 0 && pidAlive(ent.PID) {
		return fmt.Errorf("%s is already running (pid %d)", slug, ent.PID)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	root, _, ok := resolveRepo(slug, true, nil)
	if !ok {
		return fmt.Errorf("unknown project %q", slug)
	}
	argv := restartArgv(ent, root)
	cmd := exec.Command(self, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = os.Environ()
	return cmd.Start()
}

// Stop sends SIGTERM to the project's PID.
func (fs *fleetSupervisor) Stop(slug string) error {
	pid, err := fs.target(slug)
	if err != nil {
		return err
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// StopWhenIdle sends SIGUSR1 to drain the project.
func (fs *fleetSupervisor) StopWhenIdle(slug string) error {
	pid, err := fs.target(slug)
	if err != nil {
		return err
	}
	return syscall.Kill(pid, sigStopIdle)
}

// Restart sends SIGUSR2 to recycle the project's loops.
func (fs *fleetSupervisor) Restart(slug string) error {
	pid, err := fs.target(slug)
	if err != nil {
		return err
	}
	return syscall.Kill(pid, sigRestartIdle)
}

// RecycleWhenIdle sends SIGUSR2 (same as Restart in Jigz).
func (fs *fleetSupervisor) RecycleWhenIdle(slug string) error {
	return fs.Restart(slug)
}

// State returns the current state of a project.
func (fs *fleetSupervisor) State(slug string) string {
	repos := fs.Snapshot()
	for _, r := range repos {
		if r.Slug == slug {
			return r.State
		}
	}
	return supervise.StateStopped
}

// Snapshot returns the live state of every registered project.
func (fs *fleetSupervisor) Snapshot() []supervise.Repo {
	var repos []supervise.Repo
	seen := map[string]bool{}
	if fs.local != nil {
		seen[fs.local.root] = true
		repos = append(repos, fs.localView())
	}
	if fs.localOnly {
		return repos
	}
	for root, e := range readRegistryEntries() {
		if seen[root] {
			continue
		}
		// A stopped entry carries no pid and is kept on purpose; only an
		// entry whose process died without saying so goes stale.
		if e.PID != 0 && time.Since(e.UpdatedAt) > 5*time.Minute && !pidAlive(e.PID) {
			continue
		}
		repos = append(repos, fs.snapshot(root, e))
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Slug < repos[j].Slug })
	return repos
}

// StartAll starts every stopped project.
func (fs *fleetSupervisor) StartAll() {
	for _, r := range fs.Snapshot() {
		if r.State == supervise.StateStopped {
			_ = fs.Start(r.Slug)
		}
	}
}

// StopAll stops every running project immediately.
func (fs *fleetSupervisor) StopAll() {
	for _, r := range fs.Snapshot() {
		if r.State == supervise.StateRunning && r.PID > 0 {
			_ = syscall.Kill(r.PID, syscall.SIGTERM)
		}
	}
}

// StopAllWhenIdle drains every running project.
func (fs *fleetSupervisor) StopAllWhenIdle() {
	for _, r := range fs.Snapshot() {
		if r.State == supervise.StateRunning && r.PID > 0 {
			_ = syscall.Kill(r.PID, sigStopIdle)
		}
	}
}

// WaitAll blocks until every registered project has exited.
func (fs *fleetSupervisor) WaitAll() {
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); {
		allGone := true
		for _, r := range fs.Snapshot() {
			if r.State == supervise.StateRunning {
				allGone = false
				break
			}
		}
		if allGone {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Plan returns the materialization plan. In Jigz the binary is
// the running executable.
func (fs *fleetSupervisor) Plan() plan.Plan {
	self, _ := os.Executable()
	return plan.Plan{LoopBin: self}
}

// Prepare configures the supervisor for a set of resolved repos.
// In Jigz this is a no-op since discovery is registry-based.
func (fs *fleetSupervisor) Prepare(all []fleetconfig.Resolved) {}

// SetRecycleIdle controls whether recycling waits for idle.
func (fs *fleetSupervisor) SetRecycleIdle(idle bool) {}

// workflowAgents maps each loop to the agent adapter its workflow file
// names. The status document reports the loop type, which is not the
// adapter, so the file is the only source.
func workflowAgents(doc statusDoc) map[string]string {
	out := map[string]string{}
	for _, w := range doc.Workflows {
		path := w.WorkflowFile
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if wf, err := plan.ParseWorkflow(string(raw)); err == nil && wf.AgentKind != "" {
			out[w.Name] = wf.AgentKind
		}
	}
	return out
}

// readRegistryEntries reads the raw registry entries map. This is
// extracted so both fleetSupervisor and other code can use it.
func readRegistryEntries() map[string]registryEntry {
	path, err := registryPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil
	}
	return reg.Entries
}

// loopsFor returns the fleet loops of one repository.
func loopsFor(f rollup.Fleet, slug string) []rollup.Loop {
	var out []rollup.Loop
	for _, l := range f.Loops {
		if l.Repo == slug {
			out = append(out, l)
		}
	}
	return out
}
