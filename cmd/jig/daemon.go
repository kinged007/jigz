package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/dirs"
	"github.com/kinged007/jigz/internal/github"
	"github.com/kinged007/jigz/internal/hubclient"
	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/hubstate"
	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/machine"
	"github.com/kinged007/jigz/internal/plan"
	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/source"
	"github.com/kinged007/jigz/internal/store"
	"github.com/kinged007/jigz/internal/supervise"
)

const (
	defaultIdleTimeout      = machine.DefaultIdleTimeout
	defaultFallbackInterval = machine.DefaultFallbackInterval
	// fallbackWhileDown is the fallback search interval while the hub
	// does not answer.
	fallbackWhileDown = time.Minute
	// hubRefusedRetry is how often a refused credential or an unsupported
	// protocol version is tried again.
	hubRefusedRetry   = 10 * time.Minute
	defaultHeartbeat  = 30 * time.Second
	runsEvery         = 30 * time.Second
	reconcileEvery    = 2 * time.Second
	runsBatch         = 100
	maxSearchQuery    = 255
	defaultGitHubAPI  = "https://api.github.com"
	shutdownRequests  = 15 * time.Second
	initialConfigWait = 15 * time.Second
)

// daemonOptions are the inputs of a daemon. runDaemon fills them from the
// machine; tests replace the binaries, the clock and the timings.
type daemonOptions struct {
	hub hubstate.State
	// stateDir is <state>/jigz/hub: config.json, state.json,
	// status.json, the history database and the children's registry.
	stateDir string
	// dataDir is dirs.Data(); loop roots are written under it.
	dataDir   string
	engineBin string
	// childBin is run as `<childBin> --no-dashboard <root>`.
	childBin string
	idle     time.Duration
	fallback time.Duration
	// grace is the SIGTERM-to-kill budget of a child; zero is 60s.
	grace time.Duration
	logw  io.Writer
	// sleep waits for d or until ctx ends. Every periodic wait of the
	// daemon goes through it, so a test can scale time.
	sleep    func(ctx context.Context, d time.Duration) error
	validate source.ValidateFunc
	// quiet reports whether every engine loop of a running child has no
	// work in flight, and whether the child answered at all.
	quiet func(root string) (idle, known bool)
	http  *http.Client
}

type daemon struct {
	opt    daemonOptions
	log    *log.Logger
	hub    *hubclient.Client
	src    *source.Hub
	sup    *supervise.Supervisor
	store  *store.Store
	coll   *collector.Collector
	redact func(string) string

	fetchMu sync.Mutex
	ghMu    sync.Mutex
	gh      map[string]*github.Client

	fallbackNow chan struct{}

	// ctl makes pause and every start that checks it atomic.
	ctl sync.Mutex

	mu       sync.Mutex
	etag     string
	paused   bool
	revoked  bool
	hubDown  bool
	badProto bool
	lastErr  string
	// pending holds loops with staged files, applied when each is idle.
	pending map[string]source.Loop
	// retired loops are no longer in the config; they drain, then are
	// forgotten.
	retired map[string]bool
	// exitSeen is the start time of the last child exit already logged.
	exitSeen map[string]time.Time
	// sentStarted holds the ids of started events the hub accepted.
	sentStarted map[string]bool
	// applyWait counts reconcile ticks before a failed apply of a loop is
	// tried again; applyBackoff is the next wait.
	applyWait, applyBackoff map[string]int
	// wakeAfter holds loops woken while their child was stopping; they
	// start once it has exited.
	wakeAfter map[string]bool
	// gone are removed loops whose database the collector still reads
	// once, so their last runs reach the hub.
	gone map[string]source.Loop
}

// storedConfig is <stateDir>/config.json.
type storedConfig struct {
	ETag   string                  `json:"etag"`
	Config hubproto.ConfigResponse `json:"config"`
}

// daemonState is <stateDir>/state.json.
type daemonState struct {
	Paused bool `json:"paused"`
}

// runDaemon is `jig daemon`: the connected-mode process the jigd service
// runs. It runs in the foreground until SIGTERM or SIGINT.
func runDaemon(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: jig daemon")
		return 2
	}
	st, err := hubstate.Load()
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "jig daemon: this machine is not enrolled with a hub; run jig join <hub-url> <token>")
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "jig daemon:", err)
		return 1
	}
	stateRoot, err := dirs.State()
	if err != nil {
		fmt.Fprintln(os.Stderr, "jig daemon:", err)
		return 1
	}
	logPath := filepath.Join(stateRoot, "jigd.log")
	lf, err := openRotatingLog(logPath, childLogMaxBytes, childLogKeep)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jig daemon: log:", err)
		return 1
	}
	defer lf.Close()
	w := &redactWriter{w: lf, secrets: daemonSecrets(st)}
	// The collector logs through the standard logger.
	log.SetOutput(w)
	fmt.Fprintln(os.Stderr, "jig daemon: logging to", logPath)
	fail := func(err error) int {
		log.Printf("jig daemon: %v", err)
		return 1
	}
	bin, err := resolveSortieBin()
	if err != nil {
		return fail(err)
	}
	exe, err := selfExe()
	if err != nil {
		return fail(err)
	}
	hubDir := filepath.Join(stateRoot, "hub")
	// The daemon reads the children's status documents and writes their
	// wake files, so it resolves those paths the way its children do.
	if err := os.Setenv("JIG_REGISTRY_DIR", hubDir); err != nil {
		return fail(err)
	}
	idle, fallback := loadConnected()
	d, err := newDaemon(daemonOptions{
		hub: st, stateDir: hubDir, dataDir: dirs.Data(), engineBin: bin, childBin: exe,
		idle: idle, fallback: fallback, logw: w, validate: engineValidate(bin),
	})
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return d.run(ctx)
}

// daemonSecrets are the values no log line, error or status may contain.
func daemonSecrets(st hubstate.State) []string {
	out := []string{st.Credential, st.TrackerToken}
	for _, k := range []string{"JIG_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		out = append(out, os.Getenv(k))
	}
	return out
}

// redactWriter removes secrets from every write.
type redactWriter struct {
	w       io.Writer
	secrets []string
}

func (r *redactWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(r.w, redactAll(string(p), r.secrets...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// loadConnected returns the connected settings from machine.yaml, or the
// defaults when the file cannot be loaded.
func loadConnected() (idle, fallback time.Duration) {
	idle, fallback = defaultIdleTimeout, defaultFallbackInterval
	path, err := machine.DefaultPath()
	if err != nil {
		return
	}
	cfg, err := machine.Load(path)
	if err != nil {
		return
	}
	return cfg.Connected.Idle(), cfg.Connected.Fallback()
}

// engineValidate checks a staged workflow with `sortie validate`, in the
// environment source.Hub gives it and nothing else.
func engineValidate(bin string) source.ValidateFunc {
	return func(file string, env []string) error {
		cmd := exec.Command(bin, "validate", "--format", "json", file)
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()
		var res plan.FileResult
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
			return fmt.Errorf("validate: %v: %s", runErr, lines[len(lines)-1])
		}
		if res.Valid {
			return nil
		}
		if len(res.Errors) > 0 {
			return fmt.Errorf("%s: %s", res.Errors[0].Check, res.Errors[0].Message)
		}
		return errors.New("the engine reports the workflow invalid")
	}
}

func newDaemon(opt daemonOptions) (*daemon, error) {
	if opt.sleep == nil {
		opt.sleep = sleepCtx
	}
	if opt.logw == nil {
		opt.logw = os.Stderr
	}
	if opt.quiet == nil {
		opt.quiet = childQuiet
	}
	if err := os.MkdirAll(opt.stateDir, 0o700); err != nil {
		return nil, err
	}
	secrets := daemonSecrets(opt.hub)
	d := &daemon{
		opt:         opt,
		log:         log.New(&redactWriter{w: opt.logw, secrets: secrets}, "", log.LstdFlags),
		redact:      func(s string) string { return redactAll(s, secrets...) },
		gh:          map[string]*github.Client{},
		fallbackNow: make(chan struct{}, 1),
		pending:     map[string]source.Loop{},
		retired:     map[string]bool{},
		exitSeen:    map[string]time.Time{},
		sentStarted: map[string]bool{},
		applyWait:   map[string]int{}, applyBackoff: map[string]int{},
		wakeAfter: map[string]bool{},
		gone:      map[string]source.Loop{},
	}
	d.hub = &hubclient.Client{
		URL: opt.hub.HubURL, DaemonID: opt.hub.DaemonID, Credential: opt.hub.Credential,
		Version: version, Secrets: secrets[1:], HTTP: opt.http, Logf: d.log.Printf, Sleep: opt.sleep,
	}
	src, err := source.NewHub(opt.dataDir, opt.hub.RunnerLabel, opt.idle, opt.validate)
	if err != nil {
		return nil, err
	}
	d.src = src
	d.sup = supervise.New(d.spawn)
	d.sup.Grace = opt.grace
	st, err := store.Open(opt.stateDir)
	if err != nil {
		return nil, err
	}
	d.store = st
	d.coll = &collector.Collector{
		Store:    st,
		Source:   loopsrc.Legacy{Reader: rollup.Reader{RegistryPath: filepath.Join(opt.stateDir, registryName)}},
		Kind:     d.workflowKind,
		Discover: d.discover,
		Interval: runsEvery,
	}
	return d, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// spawn builds one loop child. The tracker token reaches it only through
// its environment.
func (d *daemon) spawn(l source.Loop) *exec.Cmd {
	cmd := exec.Command(d.opt.childBin, "--no-dashboard", l.Root)
	cmd.Env = append(os.Environ(),
		"JIG_TOKEN="+d.opt.hub.TrackerToken,
		"JIG_REGISTRY_DIR="+d.opt.stateDir,
		"JIG_ENGINE_BIN="+d.opt.engineBin,
		"JIG_ONLY=",
	)
	return cmd
}

// run serves until ctx ends, then stops every child and sends the last
// runs and status. It returns the exit code.
func (d *daemon) run(ctx context.Context) int {
	defer d.store.Close()
	defer d.guard()
	d.log.Printf("jig daemon %s: hub %s, runner %s, idle %s, fallback every %s",
		version, d.opt.hub.HubURL, d.opt.hub.RunnerLabel, d.opt.idle, d.opt.fallback)
	d.killOrphans()
	d.loadState()
	d.loadStoredConfig()
	ictx, cancel := context.WithTimeout(ctx, initialConfigWait)
	d.fetchConfig(ictx)
	cancel()

	var wg sync.WaitGroup
	for _, f := range []func(context.Context){d.commandLoop, d.heartbeatLoop, d.runsLoop, d.fallbackLoop, d.reconcileLoop, d.coll.Run} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer d.guard()
			f(ctx)
		}()
	}
	<-ctx.Done()
	wg.Wait()

	d.log.Printf("stopping: every loop child gets SIGTERM, its engine drains")
	d.sup.StopAll()
	fctx, cancel := context.WithTimeout(context.Background(), shutdownRequests)
	defer cancel()
	d.coll.Once(fctx)
	d.postRuns(fctx)
	doc := d.status()
	d.saveStatus(doc)
	if _, err := d.hub.PostStatus(fctx, doc); err != nil {
		d.log.Printf("final status: %v", err)
	}
	d.log.Printf("stopped")
	return 0
}

// guard is deferred by run and by every daemon goroutine. On a panic it
// stops every loop child, then panics again, so a crash leaves no loop
// running without its daemon.
func (d *daemon) guard() {
	if r := recover(); r != nil {
		d.log.Printf("panic: %v; stopping every loop child", r)
		d.sup.StopAll()
		panic(r)
	}
}

// goGuarded runs f on its own goroutine under guard.
func (d *daemon) goGuarded(f func()) {
	go func() {
		defer d.guard()
		f()
	}()
}

const orphanGrace = 10 * time.Second

// killOrphans stops the loop children a previous daemon left running. A
// child's status document names its pid and root; a live process whose
// command line is `... --no-dashboard <root>` gets SIGTERM, then SIGKILL
// after a short grace, to its whole process group.
func (d *daemon) killOrphans() {
	files, _ := filepath.Glob(filepath.Join(d.opt.stateDir, statusDirName, "*.json"))
	for _, f := range files {
		var doc statusDoc
		raw, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(raw, &doc) != nil || doc.PID <= 0 || doc.PID == os.Getpid() || !pidAlive(doc.PID) {
			continue
		}
		out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(doc.PID)).Output()
		if err != nil || !strings.Contains(string(out), "--no-dashboard "+doc.Repo) {
			continue
		}
		d.log.Printf("stopping loop child %d of a previous daemon (%s)", doc.PID, doc.Repo)
		_ = syscall.Kill(-doc.PID, syscall.SIGTERM)
		for deadline := time.Now().Add(orphanGrace); pidAlive(doc.PID) && time.Now().Before(deadline); {
			time.Sleep(100 * time.Millisecond)
		}
		_ = syscall.Kill(-doc.PID, syscall.SIGKILL)
		_ = os.Remove(f)
	}
}

// --- state files

func (d *daemon) path(name string) string { return filepath.Join(d.opt.stateDir, name) }

// writePrivate replaces path through a 0600 temporary file.
func writePrivate(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(append(data, '\n'))
	if err := firstErr(werr, tmp.Chmod(0o600), tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (d *daemon) loadState() {
	var s daemonState
	raw, err := os.ReadFile(d.path("state.json"))
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		// A pause that cannot be read is kept: resume clears it.
		s.Paused = true
		d.setErr(fmt.Errorf("state.json is unreadable, starting paused: %w", err))
	}
	d.mu.Lock()
	d.paused = s.Paused
	d.mu.Unlock()
	if s.Paused {
		d.log.Printf("paused: wake, start and fallback results are ignored until resume")
	}
}

func (d *daemon) setPaused(p bool) {
	d.mu.Lock()
	d.paused = p
	d.mu.Unlock()
	if err := writePrivate(d.path("state.json"), daemonState{Paused: p}); err != nil {
		d.setErr(fmt.Errorf("state.json: %w", err))
	}
}

func (d *daemon) isPaused() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.paused
}

func (d *daemon) loadStoredConfig() {
	raw, err := os.ReadFile(d.path("config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var sc storedConfig
	if err == nil {
		err = json.Unmarshal(raw, &sc)
	}
	if err != nil {
		d.setErr(fmt.Errorf("stored config: %w", err))
		return
	}
	if d.applyConfig(sc.Config) {
		d.mu.Lock()
		d.etag = sc.ETag
		d.mu.Unlock()
	}
}

// setErr records the latest error for the status and logs it.
func (d *daemon) setErr(err error) {
	msg := d.redact(err.Error())
	d.mu.Lock()
	d.lastErr = msg
	d.mu.Unlock()
	d.log.Print(msg)
}

// --- hub requests

// fetchConfig pulls the config, stores it, then applies it.
func (d *daemon) fetchConfig(ctx context.Context) {
	d.fetchMu.Lock()
	defer d.fetchMu.Unlock()
	d.mu.Lock()
	etag := d.etag
	d.mu.Unlock()
	cfg, newTag, err := d.hub.GetConfig(ctx, etag)
	if err != nil {
		d.hubFailed(err)
		return
	}
	d.hubOK()
	if cfg == nil {
		return
	}
	if err := writePrivate(d.path("config.json"), storedConfig{ETag: newTag, Config: *cfg}); err != nil {
		d.setErr(fmt.Errorf("store config: %w", err))
		return
	}
	d.log.Printf("config %s fetched: %d projects", newTag, len(cfg.Projects))
	// The ETag is kept only for a config that was taken in, so the next
	// heartbeat fetches a refused one again.
	if d.applyConfig(*cfg) {
		d.mu.Lock()
		d.etag = newTag
		d.mu.Unlock()
	}
}

// hubFailed handles a failed hub request and returns how long to wait
// before the next attempt of that request.
func (d *daemon) hubFailed(err error) time.Duration {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return defaultHeartbeat
	case errors.Is(err, hubclient.ErrRevoked):
		d.revoke()
		return hubRefusedRetry
	case errors.Is(err, hubclient.ErrUnsupportedVersion):
		d.mu.Lock()
		first := !d.badProto
		d.badProto = true
		d.mu.Unlock()
		if first {
			d.setErr(err)
		}
		return hubRefusedRetry
	}
	d.mu.Lock()
	first := !d.hubDown
	d.hubDown = true
	d.mu.Unlock()
	if first {
		d.setErr(fmt.Errorf("%w; running on the stored config", err))
		d.nudgeFallback()
	}
	return defaultHeartbeat
}

func (d *daemon) hubOK() {
	d.mu.Lock()
	was := d.revoked || d.hubDown
	d.revoked, d.hubDown, d.badProto = false, false, false
	d.mu.Unlock()
	if was {
		d.log.Printf("hub: reachable")
	}
}

// revoke drains every loop and stops the fallback search, once per
// refusal.
func (d *daemon) revoke() {
	d.mu.Lock()
	first := !d.revoked
	d.revoked = true
	d.mu.Unlock()
	if !first {
		return
	}
	d.setErr(errors.New("hub: the credential was refused; every loop drains and the fallback search stops; run jig join <hub-url> <token> to enroll this machine again (retrying every 10m)"))
	for _, k := range d.sup.Keys() {
		_ = d.sup.StopWhenIdle(k)
	}
}

func (d *daemon) isRevoked() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.revoked
}

func (d *daemon) heartbeatLoop(ctx context.Context) {
	for {
		wait := d.beat(ctx)
		if d.opt.sleep(ctx, wait) != nil {
			return
		}
	}
}

func (d *daemon) beat(ctx context.Context) time.Duration {
	doc := d.status()
	d.saveStatus(doc)
	resp, err := d.hub.PostStatus(ctx, doc)
	if err != nil {
		return d.hubFailed(err)
	}
	d.hubOK()
	d.mu.Lock()
	changed := resp.ConfigETag != d.etag
	d.mu.Unlock()
	if changed {
		d.fetchConfig(ctx)
	}
	return time.Duration(resp.IntervalSeconds) * time.Second
}

func (d *daemon) saveStatus(doc hubproto.StatusRequest) {
	if err := writePrivate(d.path("status.json"), doc); err != nil {
		d.log.Printf("status.json: %v", err)
	}
}

// commandLoop keeps the command socket open. A refused connect is tried
// again every 10 minutes.
func (d *daemon) commandLoop(ctx context.Context) {
	for {
		err := d.hub.Commands(ctx, d.handle)
		if ctx.Err() != nil {
			return
		}
		d.hubFailed(err)
		if d.opt.sleep(ctx, hubRefusedRetry) != nil {
			return
		}
	}
}

// --- config and loops

// applyConfig hands cfg to the hub source and brings the supervisor in
// line: new loops are added, changed ones wait for idle, removed ones
// drain. It reports false when the config could not be taken in; the
// loops then keep running as they are.
func (d *daemon) applyConfig(cfg hubproto.ConfigResponse) bool {
	rep, err := d.src.Sync(cfg)
	if err != nil {
		d.setErr(err)
		return false
	}
	for _, inv := range rep.Invalid {
		d.setErr(inv)
	}
	loops, err := d.src.Loops(context.Background())
	if err != nil {
		d.setErr(err)
		return false
	}
	want := map[string]bool{}
	for _, l := range loops {
		k := supervise.Key(l.Project, l.Workflow)
		want[k] = true
		d.sup.Update(l)
	}
	d.mu.Lock()
	for k := range want {
		delete(d.retired, k)
	}
	// Sync dropped every staged set it did not report pending, such as
	// one a reverted config no longer needs.
	d.pending = map[string]source.Loop{}
	for _, l := range rep.Pending {
		d.pending[supervise.Key(l.Project, l.Workflow)] = l
	}
	var gone []string
	for _, k := range d.sup.Keys() {
		if !want[k] && !d.retired[k] {
			d.retired[k] = true
			delete(d.pending, k)
			gone = append(gone, k)
		}
	}
	d.mu.Unlock()
	for _, k := range gone {
		d.log.Printf("%s: not in the config any more; draining", k)
		_ = d.sup.StopWhenIdle(k)
	}
	return true
}

func (d *daemon) reconcileLoop(ctx context.Context) {
	for {
		d.reconcile()
		if d.opt.sleep(ctx, reconcileEvery) != nil {
			return
		}
	}
}

// reconcile applies staged files to idle loops, forgets drained removed
// loops and reports children that exited on their own.
func (d *daemon) reconcile() {
	d.mu.Lock()
	pending := make([]source.Loop, 0, len(d.pending))
	for _, l := range d.pending {
		pending = append(pending, l)
	}
	var retired []string
	for k := range d.retired {
		retired = append(retired, k)
	}
	d.mu.Unlock()
	for _, l := range pending {
		d.applyIdle(l)
	}
	for _, k := range retired {
		if d.sup.Running(k) {
			continue
		}
		l, _ := d.loop(k)
		d.sup.Remove(k)
		d.mu.Lock()
		delete(d.retired, k)
		d.gone[k] = l
		d.mu.Unlock()
		d.log.Printf("%s: stopped and removed; its root is kept", k)
	}
	d.mu.Lock()
	var woken []string
	for k := range d.wakeAfter {
		woken = append(woken, k)
	}
	d.mu.Unlock()
	for _, k := range woken {
		if d.sup.Running(k) {
			continue
		}
		d.mu.Lock()
		delete(d.wakeAfter, k)
		d.mu.Unlock()
		if err := d.wake(k); err != nil {
			d.setErr(fmt.Errorf("%s: %w", k, err))
		}
	}
	for _, c := range d.sup.Snapshot() {
		if !c.Exited {
			continue
		}
		d.mu.Lock()
		seen := d.exitSeen[c.Key].Equal(c.Since)
		d.exitSeen[c.Key] = c.Since
		d.mu.Unlock()
		if !seen {
			msg := c.Key + ": the loop child exited: " + c.Err
			if n := len(c.Output); n > 0 {
				msg += ": " + c.Output[n-1]
			}
			d.setErr(errors.New(msg))
		}
	}
}

// applyIdle moves staged files into a loop root when the loop is stopped,
// sleeping or idle. A running loop then replaces its engine as it goes
// idle, so the new filter takes effect.
func (d *daemon) applyIdle(l source.Loop) {
	k := supervise.Key(l.Project, l.Workflow)
	running := d.sup.Running(k)
	sleeping := running && childSleeping(l.Root)
	if running && !sleeping {
		if idle, known := d.opt.quiet(l.Root); !idle || !known {
			return
		}
	}
	d.mu.Lock()
	wait := d.applyWait[k] > 0
	if wait {
		d.applyWait[k]--
	}
	d.mu.Unlock()
	if wait {
		return
	}
	applied, err := d.src.Apply(l)
	d.mu.Lock()
	if err != nil {
		b := min(max(2*d.applyBackoff[k], 1), maxApplyBackoff)
		d.applyBackoff[k], d.applyWait[k] = b, b
	} else {
		if d.pending[k].Hash == l.Hash {
			delete(d.pending, k)
		}
		delete(d.applyBackoff, k)
		delete(d.applyWait, k)
	}
	d.mu.Unlock()
	if err != nil {
		d.setErr(fmt.Errorf("%s: apply: %w", k, err))
		return
	}
	d.sup.Update(applied)
	d.log.Printf("%s: config applied (%s)", k, shortHash(applied.Hash))
	if !running {
		return
	}
	_ = d.sup.Reload(k)
	if !sleeping {
		_ = d.sup.RecycleWhenIdle(k)
	}
}

// maxApplyBackoff is the longest wait, in reconcile ticks, before a
// failed apply is tried again.
const maxApplyBackoff = int(5 * time.Minute / reconcileEvery)

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// childSleeping reports whether the child of root has stopped its engine
// for idle sleep.
func childSleeping(root string) bool {
	doc, err := readStatusDoc(root)
	return err == nil && doc.Sleep.Enabled && !doc.Sleep.Awake
}

// childQuiet reports whether every engine loop of the child of root has
// no work in flight.
func childQuiet(root string) (idle, known bool) {
	doc, err := readStatusDoc(root)
	if err != nil {
		return false, false
	}
	for _, w := range doc.Workflows {
		if w.Port == 0 || w.PID == 0 {
			continue
		}
		if idle, known := loopQuiet(w.Port); !idle || !known {
			return idle, known
		}
	}
	return true, true
}

// wake starts the loop's child, or wakes it when it sleeps.
//
// The pause check and the start hold ctl, which pause also holds, so no
// loop starts after a pause has drained the loops. A loop whose child is
// stopping starts again once the child has exited.
func (d *daemon) wake(k string) error {
	d.ctl.Lock()
	defer d.ctl.Unlock()
	if d.isPaused() {
		return errPaused
	}
	c := d.child(k)
	if c.PID != 0 {
		switch {
		case c.Stopping:
			d.mu.Lock()
			d.wakeAfter[k] = true
			d.mu.Unlock()
			d.log.Printf("%s: starts again once its child has stopped", k)
		case childSleeping(c.Loop.Root):
			d.log.Printf("%s: waking", k)
			return requestWake(c.Loop.Root)
		}
		return nil
	}
	d.mu.Lock()
	l, staged := d.pending[k]
	d.mu.Unlock()
	if staged {
		d.applyIdle(l)
	}
	d.log.Printf("%s: starting", k)
	return d.sup.Start(k)
}

// match returns the live loops a command names: all of them without a
// project, the project's with only a project.
func (d *daemon) match(a hubproto.LoopArgs) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, k := range d.sup.Keys() {
		if d.retired[k] {
			continue
		}
		project, wf := k[:strings.LastIndex(k, "/")], k[strings.LastIndex(k, "/")+1:]
		if a.Project != "" && !strings.EqualFold(project, a.Project) {
			continue
		}
		if a.Workflow != "" && wf != a.Workflow {
			continue
		}
		out = append(out, k)
	}
	return out
}

// handle carries out one hub command. hubclient has already refused
// unsupported types and malformed arguments.
func (d *daemon) handle(ctx context.Context, cmd hubproto.Command) *hubproto.AckError {
	fail := func(code hubproto.AckCode, msg string) *hubproto.AckError {
		return &hubproto.AckError{Code: code, Message: d.redact(msg)}
	}
	switch cmd.Type {
	case hubproto.CmdPause:
		d.ctl.Lock()
		defer d.ctl.Unlock()
		d.setPaused(true)
		d.mu.Lock()
		d.wakeAfter = map[string]bool{}
		d.mu.Unlock()
		d.log.Printf("paused: every loop drains")
		for _, k := range d.sup.Keys() {
			_ = d.sup.StopWhenIdle(k)
		}
		return nil
	case hubproto.CmdResume:
		d.setPaused(false)
		d.log.Printf("resumed")
		d.nudgeFallback()
		return nil
	case hubproto.CmdReloadConfig:
		d.goGuarded(func() { d.fetchConfig(ctx) })
		return nil
	}
	args, err := cmd.LoopArgs()
	if err != nil {
		return fail(hubproto.AckInvalid, err.Error())
	}
	keys := d.match(args)
	if len(keys) == 0 {
		return fail(hubproto.AckInvalid, "no such project or workflow")
	}
	switch cmd.Type {
	case hubproto.CmdStart, hubproto.CmdWake:
		for _, k := range keys {
			if err := d.wake(k); err != nil {
				return fail(hubproto.AckFailed, err.Error())
			}
		}
	case hubproto.CmdStop:
		for _, k := range keys {
			d.goGuarded(func() { _ = d.sup.Stop(k) })
		}
	case hubproto.CmdDrain:
		for _, k := range keys {
			if err := d.sup.StopWhenIdle(k); err != nil {
				return fail(hubproto.AckFailed, err.Error())
			}
		}
	case hubproto.CmdRestartWhenIdle:
		for _, k := range keys {
			if err := d.sup.RecycleWhenIdle(k); err != nil {
				return fail(hubproto.AckFailed, err.Error())
			}
		}
	}
	return nil
}

// --- loop facts read from the loop roots

type loopMeta struct {
	kind      hubproto.ItemKind
	include   []string
	exclude   []string
	maxAgents int
	endpoint  string
}

// readLoopMeta reads what the daemon needs from a loop's applied files:
// the item kind, the labels of its filter without the runner label, its
// agent concurrency and its tracker endpoint.
func readLoopMeta(l source.Loop) loopMeta {
	m := loopMeta{kind: hubproto.KindIssue, maxAgents: 1}
	text, err := os.ReadFile(filepath.Join(l.Root, ".jigz", "workflows", source.WorkflowFile(l.Workflow)))
	if err != nil {
		return m
	}
	w, err := plan.ParseWorkflow(string(text))
	if err != nil {
		return m
	}
	if w.TrackerKind == "github-pr" {
		m.kind = hubproto.KindPR
	}
	if n, err := strconv.Atoi(w.Knobs["max_concurrent_agents"]); err == nil && n > 0 {
		m.maxAgents = n
	}
	var front struct {
		Tracker struct {
			Endpoint string `yaml:"endpoint"`
		} `yaml:"tracker"`
	}
	if parts := strings.SplitN(string(text), "\n---", 2); len(parts) == 2 {
		_ = yaml.Unmarshal([]byte(strings.TrimPrefix(parts[0], "---")), &front)
	}
	m.endpoint = strings.TrimRight(front.Tracker.Endpoint, "/")
	filter := w.QueryFilter
	var cfg struct {
		Filters map[string]string `yaml:"filters"`
	}
	if raw, err := os.ReadFile(filepath.Join(l.Root, ".jigz", "config.yaml")); err == nil && yaml.Unmarshal(raw, &cfg) == nil {
		if f, ok := cfg.Filters[l.Workflow]; ok {
			filter = f
		}
	}
	var terms []string
	for _, t := range strings.Fields(filter) {
		if !strings.HasPrefix(t, "label:runner:") {
			terms = append(terms, t)
		}
	}
	m.include, m.exclude = plan.Workflow{QueryFilter: strings.Join(terms, " ")}.FilterLabels()
	return m
}

var errPaused = errors.New("the daemon is paused")

func (d *daemon) child(k string) supervise.Child {
	for _, c := range d.sup.Snapshot() {
		if c.Key == k {
			return c
		}
	}
	return supervise.Child{}
}

func (d *daemon) loop(k string) (source.Loop, bool) {
	for _, c := range d.sup.Snapshot() {
		if c.Key == k {
			return c.Loop, true
		}
	}
	return source.Loop{}, false
}

// workflowKind is the collector's store.KindIssue or store.KindPR.
func (d *daemon) workflowKind(project, workflow string) string {
	l, ok := d.loop(supervise.Key(project, workflow))
	if !ok {
		return ""
	}
	if readLoopMeta(l).kind == hubproto.KindPR {
		return store.KindPR
	}
	return store.KindIssue
}

// discover lists the workflow database of every loop, so runs of a loop
// that is not running are still copied.
// A removed loop is listed in one more pass, so the runs it finished
// before it stopped are copied and reach the hub.
func (d *daemon) discover() []rollup.Loop {
	loops := []source.Loop{}
	for _, c := range d.sup.Snapshot() {
		loops = append(loops, c.Loop)
	}
	d.mu.Lock()
	for _, l := range d.gone {
		loops = append(loops, l)
	}
	d.gone = map[string]source.Loop{}
	d.mu.Unlock()
	var out []rollup.Loop
	for _, c := range loops {
		for _, l := range loopsrc.Discover(map[string]string{c.Project: c.Root}) {
			if l.Loop == c.Workflow {
				out = append(out, l)
			}
		}
	}
	return out
}

// --- status

// status builds the heartbeat document from the children's status
// documents, their engines' state and the machine settings.
func (d *daemon) status() hubproto.StatusRequest {
	now := time.Now()
	d.mu.Lock()
	paused, lastErr := d.paused, d.lastErr
	retired := copySet(d.retired)
	d.mu.Unlock()
	req := hubproto.StatusRequest{
		Time: now.UTC(), Paused: paused, Loops: []hubproto.Loop{}, Accounts: []hubproto.Account{},
		Capacity: &hubproto.Capacity{}, Hours: &hubproto.Hours{Open: true}, LastError: lastErr,
	}
	for _, c := range d.sup.Snapshot() {
		if retired[c.Key] {
			continue
		}
		l := hubproto.Loop{Project: c.Loop.Project, Workflow: c.Loop.Workflow, State: hubproto.LoopStopped, Files: []hubproto.FileHash{}}
		if files, err := source.AppliedFiles(c.Loop.Root); err == nil && files != nil {
			l.Files = files
		}
		req.Capacity.MaxSessions += readLoopMeta(c.Loop).maxAgents
		switch {
		case c.PID != 0:
			l.State = hubproto.LoopRunning
			d.childStatus(c.Loop.Root, &l)
			if c.Stopping {
				l.Detail = hubproto.DetailStopping
			}
		case c.Exited:
			l.Detail, l.Reason = hubproto.DetailExited, d.redact(c.Err)
		}
		if paused {
			l.Detail = hubproto.DetailPaused
		}
		req.Capacity.RunningSessions += l.Running
		req.Loops = append(req.Loops, l)
	}
	cfgPath, _ := machine.DefaultPath()
	cfg, err := machine.Load(cfgPath)
	if err != nil {
		req.LastError = d.redact("machine config: " + err.Error())
		return req
	}
	statePath, _ := machine.StatePath()
	st, _ := machine.LoadState(statePath)
	req.Hours.Open = cfg.Hours.Open(now)
	if next := cfg.NextChange(st, now); !next.IsZero() {
		next = next.UTC()
		req.Hours.NextChange = &next
	}
	for _, e := range cfg.Pool {
		a := hubproto.Account{Name: e.Name, Runtime: e.Runtime, Available: e.Available(st, now)}
		if !a.Available {
			until := st[e.Name].ExhaustedUntil
			if !until.After(now) {
				until = e.Hours.Next(now)
			}
			if until.IsZero() {
				until = now
			}
			until = until.UTC()
			a.ExhaustedUntil = &until
		}
		req.Accounts = append(req.Accounts, a)
	}
	return req
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// childStatus fills a running loop's state, detail, counts and budget
// from its child's status document and its engine.
func (d *daemon) childStatus(root string, l *hubproto.Loop) {
	doc, err := readStatusDoc(root)
	if err != nil {
		return
	}
	if doc.Sleep.Enabled && !doc.Sleep.Awake {
		l.State = hubproto.LoopSleeping
	}
	for _, w := range doc.Workflows {
		switch w.State {
		case "held", "paused":
			l.Detail, l.Reason = hubproto.DetailHeld, d.redact(w.Hold)
		case "disabled":
			l.Detail, l.Reason = hubproto.DetailDisabled, d.redact(w.Hold)
		case "stopping":
			l.Detail = hubproto.DetailStopping
		}
		if b := w.Budget; b != nil && b.Cap > 0 {
			l.Budget = &hubproto.Budget{Period: b.Period, MaxTokens: b.Cap, UsedTokens: max(b.TokensUsed, 0)}
		}
		if l.State != hubproto.LoopRunning || w.Port == 0 || w.PID == 0 {
			continue
		}
		if p, ok := fetchLoopState(w.Port); ok {
			l.Running += len(p.Running)
			l.Retrying += max(len(p.Retrying), p.Counts.Retrying)
		}
	}
}

// --- run events

func (d *daemon) runsLoop(ctx context.Context) {
	for {
		if d.opt.sleep(ctx, runsEvery) != nil {
			return
		}
		d.postRuns(ctx)
	}
}

// postRuns sends the started events of the running engines and the
// finished runs past the hub cursor, in batches of up to 100. A cursor
// moves only over runs the hub accepted.
func (d *daemon) postRuns(ctx context.Context) {
	if d.isRevoked() {
		return
	}
	if started := d.startedEvents(); len(started) > 0 {
		for len(started) > 0 {
			n := min(len(started), runsBatch)
			accepted, err := d.hub.PostRuns(ctx, started[:n])
			if err != nil {
				d.hubFailed(err)
				return
			}
			d.mu.Lock()
			for _, id := range accepted {
				d.sentStarted[id] = true
			}
			d.mu.Unlock()
			started = started[n:]
		}
	}
	for range 10 {
		runs, err := d.store.HubRuns(runsBatch)
		if err != nil {
			d.setErr(fmt.Errorf("run events: %w", err))
			return
		}
		if len(runs) == 0 {
			return
		}
		var events []hubproto.RunEvent
		skip := map[int]bool{}
		for i, r := range runs {
			ev, ok := finishedEvent(r)
			if !ok {
				skip[i] = true
				continue
			}
			events = append(events, ev)
		}
		ok := map[string]bool{}
		if len(events) > 0 {
			accepted, err := d.hub.PostRuns(ctx, events)
			if err != nil {
				d.hubFailed(err)
				return
			}
			for _, id := range accepted {
				ok[id] = true
			}
		}
		moved := d.advanceCursors(runs, func(i int, r store.Run) bool { return skip[i] || ok[finishedID(r)] })
		if !moved || len(runs) < runsBatch {
			return
		}
	}
}

// advanceCursors moves each workflow's cursor over the leading runs that
// done reports, and stops at the first one it does not. Within a
// workflow, runs are in run id order.
func (d *daemon) advanceCursors(runs []store.Run, done func(int, store.Run) bool) bool {
	moved := false
	blocked := map[string]bool{}
	for i, r := range runs {
		k := supervise.Key(r.Project, r.Workflow)
		if blocked[k] {
			continue
		}
		if !done(i, r) {
			blocked[k] = true
			continue
		}
		if err := d.store.SetHubCursor(r.Project, r.Workflow, r.ID); err != nil {
			d.setErr(fmt.Errorf("hub cursor: %w", err))
			return false
		}
		moved = true
	}
	return moved
}

func finishedID(r store.Run) string {
	return fmt.Sprintf("run:%s:%s:%d", r.Project, r.Workflow, r.ID)
}

// finishedEvent maps a stored run to a finished event. A run without an
// item number or start time cannot be reported.
func finishedEvent(r store.Run) (hubproto.RunEvent, bool) {
	n, err := strconv.Atoi(issueNumber(r.DisplayIdentifier, r.IssueID))
	started, okStart := parseRunTime(r.StartedAt)
	if err != nil || n < 1 || !okStart || !strings.Contains(r.Project, "/") {
		return hubproto.RunEvent{}, false
	}
	finished, ok := parseRunTime(r.CompletedAt)
	if !ok {
		finished = started
	}
	kind := hubproto.KindIssue
	if r.WorkflowKind == store.KindPR {
		kind = hubproto.KindPR
	}
	finished = finished.UTC()
	ev := hubproto.RunEvent{
		ID: finishedID(r), Type: hubproto.EventFinished, Workflow: r.Workflow,
		Item:    hubproto.Item{Project: r.Project, Number: n, Kind: kind},
		Attempt: max(r.Attempt, 1), StartedAt: started.UTC(), FinishedAt: &finished,
		Outcome: hubproto.OutcomeClass(store.ClassifyOutcome(r.Status, r.Error).Class),
		Usage: &hubproto.Usage{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
			TotalTokens: r.TotalTokens, Measured: r.TokensMeasured},
	}
	if kind == hubproto.KindIssue {
		ev.Links = &hubproto.Links{Branch: "auto/" + strconv.Itoa(n)}
	}
	return ev, ev.Validate() == nil
}

// engineRunning is the part of an engine's /api/v1/state the started
// events need.
type engineRunning struct {
	Running []struct {
		IssueIdentifier   string `json:"issue_identifier"`
		DisplayIdentifier string `json:"display_identifier"`
		Attempt           *int   `json:"attempt"`
		StartedAt         string `json:"started_at"`
	} `json:"running"`
}

// startedEvents lists one started event per (loop, item, attempt) running
// now that the hub has not accepted yet.
func (d *daemon) startedEvents() []hubproto.RunEvent {
	var out []hubproto.RunEvent
	live := map[string]bool{}
	// Ids of items no longer running are dropped, so the set stays as
	// small as the work in flight.
	defer func() {
		d.mu.Lock()
		for id := range d.sentStarted {
			if !live[id] {
				delete(d.sentStarted, id)
			}
		}
		d.mu.Unlock()
	}()
	for _, c := range d.sup.Snapshot() {
		if c.PID == 0 {
			continue
		}
		doc, err := readStatusDoc(c.Loop.Root)
		if err != nil {
			continue
		}
		kind := readLoopMeta(c.Loop).kind
		for _, w := range doc.Workflows {
			if w.Port == 0 || w.PID == 0 {
				continue
			}
			var st engineRunning
			if rollup.GetJSON(&http.Client{Timeout: 3 * time.Second}, w.Port, "/api/v1/state", &st) != nil {
				continue
			}
			for _, r := range st.Running {
				id := r.DisplayIdentifier
				if id == "" {
					id = r.IssueIdentifier
				}
				n, err := strconv.Atoi(issueNumber(id, ""))
				started, ok := parseRunTime(r.StartedAt)
				if err != nil || n < 1 || !ok {
					continue
				}
				attempt := 1
				if r.Attempt != nil && *r.Attempt > 0 {
					attempt = *r.Attempt
				}
				evID := fmt.Sprintf("start:%s:%s:%d:%d:%d", c.Loop.Project, c.Loop.Workflow, n, attempt, started.Unix())
				live[evID] = true
				d.mu.Lock()
				sent := d.sentStarted[evID]
				d.mu.Unlock()
				if sent {
					continue
				}
				out = append(out, hubproto.RunEvent{
					ID: evID, Type: hubproto.EventStarted, Workflow: c.Loop.Workflow,
					Item:    hubproto.Item{Project: c.Loop.Project, Number: n, Kind: kind},
					Attempt: attempt, StartedAt: started.UTC(),
				})
			}
		}
	}
	return out
}

// --- fallback search

func (d *daemon) nudgeFallback() {
	select {
	case d.fallbackNow <- struct{}{}:
	default:
	}
}

// fallbackLoop runs the search at start, every fallback interval, every
// minute while the hub is down, and on resume. It does nothing while the
// daemon is paused or its credential is refused.
func (d *daemon) fallbackLoop(ctx context.Context) {
	for {
		d.mu.Lock()
		skip := d.paused || d.revoked
		wait := d.opt.fallback
		if d.hubDown {
			wait = min(wait, fallbackWhileDown)
		}
		d.mu.Unlock()
		if !skip {
			d.fallbackSearch(ctx)
		}
		wctx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-d.fallbackNow:
				cancel()
			case <-wctx.Done():
			}
		}()
		_ = d.opt.sleep(wctx, wait)
		cancel()
		if ctx.Err() != nil {
			return
		}
	}
}

// searchQueries groups the projects into search queries for open items
// with the runner label, each under GitHub's 256-character limit.
func fallbackQueries(runnerLabel string, projects []string) []string {
	prefix := "state:open label:" + runnerLabel
	var out []string
	cur := prefix
	for _, p := range projects {
		term := " repo:" + p
		if len(cur)+len(term) > maxSearchQuery && cur != prefix {
			out = append(out, cur)
			cur = prefix
		}
		cur += term
	}
	if cur != prefix {
		out = append(out, cur)
	}
	return out
}

type searchItem struct {
	Number        int    `json:"number"`
	HTMLURL       string `json:"html_url"`
	RepositoryURL string `json:"repository_url"`
	Labels        []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
}

// project is the owner/name of the item's repository.
func (it searchItem) project() string {
	if u, err := url.Parse(it.RepositoryURL); err == nil && it.RepositoryURL != "" {
		if i := strings.Index(u.Path, "/repos/"); i >= 0 {
			return strings.Trim(u.Path[i+len("/repos/"):], "/")
		}
	}
	if u, err := url.Parse(it.HTMLURL); err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return ""
}

// fits reports whether a loop with meta m would pick the item up.
func (m loopMeta) fits(it searchItem) bool {
	if (len(it.PullRequest) > 0) != (m.kind == hubproto.KindPR) {
		return false
	}
	has := map[string]bool{}
	for _, l := range it.Labels {
		has[l.Name] = true
	}
	for _, l := range m.exclude {
		if has[l] {
			return false
		}
	}
	if len(m.include) == 0 {
		return true
	}
	return slices.ContainsFunc(m.include, func(l string) bool { return has[l] })
}

func (d *daemon) github(base string) *github.Client {
	d.ghMu.Lock()
	defer d.ghMu.Unlock()
	c, ok := d.gh[base]
	if !ok {
		c = &github.Client{BaseURL: base, HTTP: d.opt.http}
		d.gh[base] = c
	}
	return c
}

// fallbackSearch asks the provider for open items with the runner label
// in the loops' projects and wakes every loop that would pick one up.
func (d *daemon) fallbackSearch(ctx context.Context) {
	metas := map[string]loopMeta{}
	byProject := map[string][]string{}
	base := ""
	for _, c := range d.sup.Snapshot() {
		d.mu.Lock()
		gone := d.retired[c.Key]
		d.mu.Unlock()
		if gone {
			continue
		}
		m := readLoopMeta(c.Loop)
		metas[c.Key] = m
		p := strings.ToLower(c.Loop.Project)
		byProject[p] = append(byProject[p], c.Key)
		if base == "" && m.kind == hubproto.KindIssue && m.endpoint != "" {
			base = m.endpoint
		}
	}
	if len(byProject) == 0 {
		return
	}
	if base == "" {
		base = defaultGitHubAPI
	}
	var projects []string
	for _, c := range d.sup.Snapshot() {
		if _, ok := metas[c.Key]; ok && !slices.Contains(projects, c.Loop.Project) {
			projects = append(projects, c.Loop.Project)
		}
	}
	sort.Strings(projects)
	gh := d.github(base)
	woken := map[string]bool{}
	for _, q := range fallbackQueries(d.opt.hub.RunnerLabel, projects) {
		resp, err := gh.Get(ctx, "fallback", d.opt.hub.TrackerToken, "/search/issues?q="+url.QueryEscape(q)+"&per_page=100", "")
		if err != nil {
			if ctx.Err() == nil {
				d.log.Printf("fallback search: %s", d.redact(err.Error()))
			}
			continue
		}
		var res struct {
			Items []searchItem `json:"items"`
		}
		if err := json.Unmarshal(resp.Body, &res); err != nil {
			d.log.Printf("fallback search: unreadable response: %v", err)
			continue
		}
		for _, it := range res.Items {
			for _, k := range byProject[strings.ToLower(it.project())] {
				if woken[k] || !metas[k].fits(it) || d.isPaused() || d.isRevoked() {
					continue
				}
				woken[k] = true
				if err := d.wake(k); err != nil {
					d.setErr(fmt.Errorf("%s: %w", k, err))
				}
			}
		}
	}
}
