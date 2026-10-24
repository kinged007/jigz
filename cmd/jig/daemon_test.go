package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/fakegithub"
	"github.com/kinged007/jigz/internal/fakehub"
	"github.com/kinged007/jigz/internal/hubclient"
	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/hubstate"
	"github.com/kinged007/jigz/internal/store"
	"github.com/kinged007/jigz/internal/supervise"
)

const (
	daemonCanaryTracker = "ghp_canaryTrackerToken0123456789"
	daemonCanaryEnroll  = "enroll-canary-token-9876"
	daemonCanaryEnv     = "jig-token-canary-env-5555"
	testRunner          = "runner:ci-1"
	testProject         = "acme/widgets"
)

// syncBuffer is a log destination the daemon and the test share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// rig is one daemon against a fake hub and a fake GitHub, with a stub loop
// child that records its starts, environment and signals.
type rig struct {
	t       *testing.T
	hub     *fakehub.Server
	gh      *fakegithub.Server
	ghURL   string
	st      hubstate.State
	opt     daemonOptions
	rec     string
	logs    *syncBuffer
	idle    atomic.Bool
	smu     sync.Mutex
	sleeps  []time.Duration
	d       *daemon
	cancel  context.CancelFunc
	done    chan int
	stopped bool
}

func workflowText(endpoint string) string {
	return "---\ntracker:\n  kind: github\n  endpoint: " + endpoint +
		"\n  query_filter: \"label:agent:plan -label:needs-human\"\nagent:\n  kind: mock\n  max_concurrent_agents: 2\n---\nDo the work.\n"
}

func hubConfig(endpoint, additions string, projects ...string) hubproto.ConfigResponse {
	text := workflowText(endpoint)
	sum := sha256.Sum256([]byte(text))
	cfg := hubproto.ConfigResponse{
		Member:   hubproto.Member{Login: "octo"},
		Projects: []hubproto.ConfigProject{},
		Files:    []hubproto.ConfigFile{{Path: "WORKFLOW.issues.md", SHA256: hex.EncodeToString(sum[:]), Content: text}},
		Labels:   []hubproto.Label{},
	}
	for _, p := range projects {
		cfg.Projects = append(cfg.Projects, hubproto.ConfigProject{Provider: hubproto.ProviderGitHub, Repo: p, Workflows: []string{"issues"}, FilterAdditions: additions})
	}
	return cfg
}

// newRig enrolls with a fake hub and builds a daemon; start runs it.
func newRig(t *testing.T) *rig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the daemon runs on Linux and macOS")
	}
	home := t.TempDir()
	for k, v := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"), "XDG_STATE_HOME": filepath.Join(home, "state"),
		"XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_CACHE_HOME": filepath.Join(home, "cache"),
		"JIG_TOKEN": daemonCanaryEnv, "GITHUB_TOKEN": "", "GH_TOKEN": "",
	} {
		t.Setenv(k, v)
	}
	stateDir := filepath.Join(home, "state", "jigz", "hub")
	t.Setenv("JIG_REGISTRY_DIR", stateDir)

	r := &rig{t: t, logs: &syncBuffer{}, rec: t.TempDir()}
	r.gh = fakegithub.New("octo", daemonCanaryTracker)
	r.gh.AddRepo(testProject, "agent:plan", testRunner, "needs-human")
	ghSrv := httptest.NewServer(r.gh)
	t.Cleanup(ghSrv.Close)
	r.ghURL = ghSrv.URL

	r.hub = fakehub.New(daemonCanaryEnroll)
	hubSrv := httptest.NewServer(r.hub)
	t.Cleanup(hubSrv.Close)
	enroll, err := (&hubclient.Client{URL: hubSrv.URL, Version: "test"}).Enroll(context.Background(), hubproto.EnrollRequest{
		Token: daemonCanaryEnroll, Name: "ci-1", Host: hubproto.Host{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: 1},
		JigVersion: "test", EngineVersion: "test", TrackerLogin: "octo",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.st = hubstate.State{HubURL: hubSrv.URL, DaemonID: enroll.DaemonID, Name: "ci-1",
		Credential: enroll.Credential, RunnerLabel: enroll.RunnerLabel, TrackerToken: daemonCanaryTracker}
	r.hub.SetConfig(hubConfig(r.ghURL, "", testProject))

	bin := t.TempDir()
	engine := writeScript(t, bin, "engine", `echo '{"valid":true,"errors":[],"warnings":[]}'`+"\n")
	child := writeScript(t, bin, "child", `root="$2"
rec=`+r.rec+`
if [ -f "$rec/ignore-term" ]; then
  trap '' TERM
  sleep 300 &
  echo $! > "$rec/grandchild"
else
  trap 'echo "term $root" >> "$rec/signals"; exit 0' TERM
fi
trap 'echo "usr1 $root" >> "$rec/signals"; exit 0' USR1
trap 'echo "usr2 $root" >> "$rec/signals"' USR2
trap 'echo "hup $root" >> "$rec/signals"' HUP
env | grep '^JIG_' | sort > "$rec/env"
echo "$$ $root" >> "$rec/starts"
while :; do sleep 0.05; done
`)
	r.idle.Store(true)
	r.opt = daemonOptions{
		hub: r.st, stateDir: stateDir, dataDir: filepath.Join(home, "data", "jigz"),
		engineBin: engine, childBin: child, idle: 30 * time.Second, fallback: 20 * time.Second,
		grace: time.Second, logw: r.logs, validate: engineValidate(engine),
		quiet: func(string) (bool, bool) { return r.idle.Load(), true },
		// Time runs 200 times faster: a 30 s heartbeat takes 150 ms.
		sleep: func(ctx context.Context, d time.Duration) error {
			r.smu.Lock()
			r.sleeps = append(r.sleeps, d)
			r.smu.Unlock()
			return sleepCtx(ctx, d/200)
		},
	}
	t.Cleanup(r.stop)
	t.Cleanup(r.checkSecrets)
	return r
}

func (r *rig) start() {
	r.t.Helper()
	d, err := newDaemon(r.opt)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.d, r.cancel, r.done, r.stopped = d, cancel, make(chan int, 1), false
	go func() { r.done <- d.run(ctx) }()
	r.eventually("the command socket", r.hub.Connected)
}

// stop ends the daemon as SIGTERM does and returns its exit code.
func (r *rig) stop() {
	if r.cancel == nil || r.stopped {
		return
	}
	r.stopped = true
	r.cancel()
	select {
	case code := <-r.done:
		if code != 0 {
			r.t.Errorf("daemon exit code = %d, want 0", code)
		}
	case <-time.After(30 * time.Second):
		r.t.Fatal("the daemon did not stop")
	}
}

// checkSecrets fails when a secret reached the log or a request body.
func (r *rig) checkSecrets() {
	text := r.logs.String() + strings.Join(r.hub.Bodies(), "\n")
	if raw, err := os.ReadFile(filepath.Join(r.opt.stateDir, "status.json")); err == nil {
		text += string(raw)
	}
	for _, s := range []string{daemonCanaryTracker, daemonCanaryEnroll, daemonCanaryEnv, r.st.Credential} {
		if s != "" && strings.Contains(text, s) {
			r.t.Errorf("a secret reached the daemon log, status or a hub request body")
		}
	}
	if u := r.hub.Unexpected(); len(u) > 0 {
		r.t.Errorf("fake hub rejected requests: %v", u)
	}
}

func (r *rig) eventually(what string, ok func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for %s; daemon log:\n%s", what, r.logs.String())
}

func (r *rig) root(project string) string {
	return filepath.Join(r.opt.dataDir, "hub", "loops", filepath.FromSlash(project), "issues")
}

func (r *rig) record(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.rec, name))
	return string(b)
}

func (r *rig) send(typ hubproto.CommandType, args string) hubproto.Ack {
	r.t.Helper()
	ack, err := r.hub.Send(hubproto.Command{Type: typ, Args: json.RawMessage(args)})
	if err != nil {
		r.t.Fatal(err)
	}
	return ack
}

func loopArgs(project string) string {
	return `{"project":"` + project + `","workflow":"issues"}`
}

// lastLoop returns the loop of project in the newest heartbeat.
func (r *rig) lastLoop(project string) (hubproto.Loop, bool) {
	st := r.hub.Statuses()
	if len(st) == 0 {
		return hubproto.Loop{}, false
	}
	for _, l := range st[len(st)-1].Loops {
		if l.Project == project {
			return l, true
		}
	}
	return hubproto.Loop{}, false
}

func (r *rig) loopState(project string, want hubproto.LoopState) func() bool {
	return func() bool {
		l, ok := r.lastLoop(project)
		return ok && l.State == want
	}
}

func (r *rig) wake(project string) {
	r.t.Helper()
	if ack := r.send(hubproto.CmdWake, loopArgs(project)); !ack.OK {
		r.t.Fatalf("wake ack = %+v", ack)
	}
	r.eventually("the child to start", func() bool { return strings.Contains(r.record("starts"), r.root(project)) })
}

func TestDaemonStartsALoopOnWakeWithTheRunnerFilter(t *testing.T) {
	r := newRig(t)
	r.start()
	r.eventually("a heartbeat with the loop stopped", r.loopState(testProject, hubproto.LoopStopped))
	l, _ := r.lastLoop(testProject)
	if len(l.Files) != 1 || l.Files[0].Path != "WORKFLOW.issues.md" {
		t.Errorf("files = %+v, want the applied workflow", l.Files)
	}
	cfg := readFile(t, filepath.Join(r.root(testProject), ".jigz", "config.yaml"))
	if !strings.Contains(cfg, "issues: label:agent:plan -label:needs-human label:"+testRunner) || !strings.Contains(cfg, "assignee: octo") {
		t.Errorf("config.yaml lacks the runner filter or the assignee:\n%s", cfg)
	}
	if r.record("starts") != "" {
		t.Fatal("a loop started before any wake")
	}

	r.wake(testProject)
	env := r.record("env")
	for _, want := range []string{"JIG_TOKEN=" + daemonCanaryTracker, "JIG_REGISTRY_DIR=" + r.opt.stateDir, "JIG_ENGINE_BIN=" + r.opt.engineBin} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("child environment lacks %s", strings.SplitN(want, "=", 2)[0])
		}
	}
	r.eventually("a heartbeat with the loop running", r.loopState(testProject, hubproto.LoopRunning))
	st := r.hub.Statuses()
	if c := st[len(st)-1].Capacity; c.MaxSessions != 2 {
		t.Errorf("capacity = %+v, want max_sessions 2", c)
	}

	if ack := r.send(hubproto.CmdWake, loopArgs("acme/other")); ack.OK || ack.Error.Code != hubproto.AckInvalid {
		t.Errorf("wake of an unknown project = %+v, want invalid", ack)
	}
	if ack := r.send(hubproto.CmdSessionOpen, `{}`); ack.OK || ack.Error.Code != hubproto.AckUnsupported {
		t.Errorf("session.open = %+v, want unsupported", ack)
	}
	if ack := r.send(hubproto.CmdStart, `{"project":"`+testProject+`"}`); !ack.OK {
		t.Errorf("start of a running project = %+v, want ok", ack)
	}
	if n := strings.Count(r.record("starts"), "\n"); n != 1 {
		t.Errorf("child started %d times, want once", n)
	}
	fi, err := os.Stat(filepath.Join(r.opt.stateDir, "config.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("config.json: %v, mode %v, want 0600", err, fi)
	}
}

func TestDaemonAppliesANewConfigWhenTheLoopIsIdle(t *testing.T) {
	r := newRig(t)
	r.start()
	r.wake(testProject)
	r.idle.Store(false)
	cfgPath := filepath.Join(r.root(testProject), ".jigz", "config.yaml")
	r.hub.SetConfig(hubConfig(r.ghURL, "-label:backlog", testProject))
	if ack := r.send(hubproto.CmdReloadConfig, `{}`); !ack.OK {
		t.Fatalf("reload_config = %+v", ack)
	}
	r.eventually("the new files to be staged", func() bool {
		_, err := os.Stat(filepath.Join(r.root(testProject), ".jigz.next"))
		return err == nil
	})
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(readFile(t, cfgPath), "backlog") || strings.Contains(r.record("signals"), "usr2") {
		t.Fatal("the config was applied to a busy loop")
	}
	r.idle.Store(true)
	r.eventually("the config to be applied", func() bool { return strings.Contains(readFile(t, cfgPath), "-label:backlog label:"+testRunner) })
	r.eventually("the child to be told to reload and restart when idle", func() bool {
		s := r.record("signals")
		return strings.Contains(s, "hup ") && strings.Contains(s, "usr2 ")
	})
	for _, c := range r.d.sup.Snapshot() {
		if c.StartedHash != c.Loop.Hash {
			t.Errorf("%s runs %s, want the applied %s", c.Key, c.StartedHash, c.Loop.Hash)
		}
	}
}

func TestDaemonDrainsARemovedProjectAndKeepsItsRoot(t *testing.T) {
	r := newRig(t)
	r.hub.SetConfig(hubConfig(r.ghURL, "", testProject, "acme/gadgets"))
	r.start()
	r.wake(testProject)
	r.hub.SetConfig(hubConfig(r.ghURL, "", "acme/gadgets"))
	r.send(hubproto.CmdReloadConfig, `{}`)
	r.eventually("the removed loop to drain", func() bool { return strings.Contains(r.record("signals"), "usr1 "+r.root(testProject)) })
	r.eventually("a heartbeat without the removed loop", func() bool {
		_, has := r.lastLoop(testProject)
		_, other := r.lastLoop("acme/gadgets")
		return !has && other
	})
	if _, err := os.Stat(filepath.Join(r.root(testProject), ".jigz", "config.yaml")); err != nil {
		t.Errorf("the removed loop's root is gone: %v", err)
	}
	if ack := r.send(hubproto.CmdWake, loopArgs(testProject)); ack.OK {
		t.Error("wake of a removed project was accepted")
	}
}

func TestDaemonPauseIsKeptAcrossARestart(t *testing.T) {
	r := newRig(t)
	r.start()
	r.wake(testProject)
	if ack := r.send(hubproto.CmdPause, `{}`); !ack.OK {
		t.Fatalf("pause = %+v", ack)
	}
	r.eventually("the loop to drain", func() bool { return strings.Contains(r.record("signals"), "usr1 ") })
	if ack := r.send(hubproto.CmdWake, loopArgs(testProject)); ack.OK || ack.Error.Code != hubproto.AckFailed {
		t.Errorf("wake while paused = %+v, want failed", ack)
	}
	r.stop()

	r.start()
	r.eventually("a paused heartbeat after the restart", func() bool {
		st := r.hub.Statuses()
		return len(st) > 0 && st[len(st)-1].Paused && st[len(st)-1].Loops[0].Detail == hubproto.DetailPaused
	})
	if ack := r.send(hubproto.CmdResume, `{}`); !ack.OK {
		t.Fatalf("resume = %+v", ack)
	}
	var s daemonState
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(r.opt.stateDir, "state.json"))), &s); err != nil || s.Paused {
		t.Errorf("state.json = %+v, %v, want not paused", s, err)
	}
	r.wake(testProject)
}

func fallbackSearches(gh *fakegithub.Server) []string {
	var out []string
	for _, req := range gh.Requests() {
		if req.Path == "/search/issues" {
			v, _ := url.ParseQuery(req.Query)
			out = append(out, v.Get("q"))
		}
	}
	return out
}

func TestDaemonFallbackSearchWakesMatchingLoops(t *testing.T) {
	r := newRig(t)
	r.gh.Add(fakegithub.Item{Repo: testProject, Title: "parked", Labels: []string{"agent:plan", testRunner, "needs-human"}})
	r.opt.fallback = defaultFallbackInterval
	r.start()
	r.eventually("the first search", func() bool { return len(fallbackSearches(r.gh)) > 0 })
	if q := fallbackSearches(r.gh)[0]; q != "state:open label:"+testRunner+" repo:"+testProject {
		t.Errorf("search q = %q", q)
	}
	time.Sleep(100 * time.Millisecond)
	if r.record("starts") != "" {
		t.Fatal("an item the filter excludes woke the loop")
	}
	r.gh.Add(fakegithub.Item{Repo: testProject, Title: "work", Labels: []string{"agent:plan", testRunner}})
	r.hub.SetDown(true)
	r.eventually("the search to wake the loop", func() bool { return strings.Contains(r.record("starts"), r.root(testProject)) })
	r.eventually("a fallback wait of one minute while the hub is down", func() bool {
		r.smu.Lock()
		defer r.smu.Unlock()
		return slices.Contains(r.sleeps, time.Minute)
	})
	r.hub.SetDown(false)
}

func TestFallbackQueriesStayUnderTheLimit(t *testing.T) {
	var projects []string
	for i := range 20 {
		projects = append(projects, "an-owner-name/a-fairly-long-repository-"+strconv.Itoa(i))
	}
	qs := fallbackQueries(testRunner, projects)
	if len(qs) < 2 {
		t.Fatalf("queries = %d, want the projects split", len(qs))
	}
	n := 0
	for _, q := range qs {
		if len(q) >= 256 || !strings.HasPrefix(q, "state:open label:"+testRunner+" repo:") {
			t.Errorf("query %q (%d chars)", q, len(q))
		}
		n += strings.Count(q, " repo:")
	}
	if n != len(projects) {
		t.Errorf("queries name %d repos, want %d", n, len(projects))
	}
}

func TestDaemonRefusedCredentialDrainsAndStopsTheSearch(t *testing.T) {
	r := newRig(t)
	r.start()
	r.wake(testProject)
	r.hub.Revoke()
	r.eventually("the loop to drain", func() bool { return strings.Contains(r.record("signals"), "usr1 ") })
	r.eventually("a 10 minute retry", func() bool {
		r.smu.Lock()
		defer r.smu.Unlock()
		return slices.Contains(r.sleeps, hubRefusedRetry)
	})
	searches := len(fallbackSearches(r.gh))
	// Longer than several fallback intervals at the test's time scale.
	time.Sleep(time.Second)
	if n := len(fallbackSearches(r.gh)); n != searches {
		t.Errorf("fallback searches went on after the refusal: %d -> %d", searches, n)
	}
	if n := strings.Count(r.logs.String(), "jig join"); n != 1 {
		t.Errorf("log names jig join %d times, want once:\n%s", n, r.logs.String())
	}
	var doc hubproto.StatusRequest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(r.opt.stateDir, "status.json"))), &doc); err != nil || !strings.Contains(doc.LastError, "jig join") {
		t.Errorf("status.json last_error = %q, %v", doc.LastError, err)
	}
}

func TestDaemonStopLeavesNoChild(t *testing.T) {
	r := newRig(t)
	r.hub.SetConfig(hubConfig(r.ghURL, "", testProject, "acme/gadgets"))
	if err := os.WriteFile(filepath.Join(r.rec, "ignore-term"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.wake(testProject)
	r.wake("acme/gadgets")
	r.eventually("the grandchild", func() bool { return r.record("grandchild") != "" })
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(r.record("starts")), "\n") {
		pid, _ := strconv.Atoi(strings.Fields(line)[0])
		pids = append(pids, pid)
	}
	gc, _ := strconv.Atoi(strings.TrimSpace(r.record("grandchild")))
	pids = append(pids, gc)
	start := time.Now()
	r.stop()
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("stop took %s", elapsed)
	}
	for _, pid := range pids {
		r.eventually("pid "+strconv.Itoa(pid)+" to end", func() bool { return procGone(pid) })
	}
	st := r.hub.Statuses()
	for _, l := range st[len(st)-1].Loops {
		if l.State != hubproto.LoopStopped {
			t.Errorf("final status %s %s = %s, want stopped", l.Project, l.Workflow, l.State)
		}
	}
}

func TestDaemonRunEventsMovePastAcceptedRunsOnly(t *testing.T) {
	r := newRig(t)
	d, err := newDaemon(r.opt)
	if err != nil {
		t.Fatal(err)
	}
	defer d.store.Close()
	run := func(id int64, n int) store.Run {
		return store.Run{Project: testProject, Workflow: "issues", ID: id, IssueID: "x",
			DisplayIdentifier: testProject + "#" + strconv.Itoa(n), Status: "succeeded",
			StartedAt: "2026-10-24T10:00:00Z", CompletedAt: "2026-10-24T10:01:00Z", Attempt: 1,
			AgentAdapter: "mock", InputTokens: 100, OutputTokens: 50, TotalTokens: 150, TokensMeasured: true, WorkflowKind: store.KindIssue}
	}
	if err := d.store.AddRuns([]store.Run{run(1, 7), run(2, 8), run(3, 9)}); err != nil {
		t.Fatal(err)
	}
	// The hub accepts every id except run 2 on the first batch.
	var calls atomic.Int32
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, "/runs") {
			r.hub.ServeHTTP(w, req)
			return
		}
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.hub.ServeHTTP(rec, req)
		var resp hubproto.RunsResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if calls.Add(1) == 1 {
			resp.Accepted = slices.DeleteFunc(resp.Accepted, func(id string) bool { return strings.HasSuffix(id, ":2") })
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer hubSrv.Close()
	d.hub.URL = hubSrv.URL
	d.postRuns(context.Background())
	d.postRuns(context.Background())
	d.postRuns(context.Background())
	var ids []string
	for _, e := range r.hub.Runs() {
		ids = append(ids, e.ID)
	}
	want := "run:acme/widgets:issues:1,run:acme/widgets:issues:2,run:acme/widgets:issues:3," +
		"run:acme/widgets:issues:2,run:acme/widgets:issues:3"
	if got := strings.Join(ids, ","); got != want {
		t.Errorf("events sent = %s\nwant %s", got, want)
	}
	e := r.hub.Runs()[0]
	if e.Type != hubproto.EventFinished || e.Item != (hubproto.Item{Project: testProject, Number: 7, Kind: hubproto.KindIssue}) ||
		e.Outcome != hubproto.OutcomeSucceeded || e.Usage.InputTokens != 100 || e.Usage.OutputTokens != 50 ||
		e.Links == nil || e.Links.Branch != "auto/7" {
		t.Errorf("event = %+v", e)
	}
	if pending, _ := d.store.HubRuns(100); len(pending) != 0 {
		t.Errorf("runs still pending after acceptance: %d", len(pending))
	}
}

func TestDaemonRefusesAGroupReadableHubFile(t *testing.T) {
	r := newRig(t)
	if err := hubstate.Save(r.st); err != nil {
		t.Fatal(err)
	}
	path, _ := hubstate.Path()
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStderr(t, func() { code = runDaemon(nil) })
	if code != 1 || !strings.Contains(out, "group or others") {
		t.Errorf("runDaemon = %d, stderr %q; want a refusal", code, out)
	}
	if strings.Contains(out, daemonCanaryTracker) || strings.Contains(out, r.st.Credential) {
		t.Error("the refusal printed a secret")
	}
	if ack := supervise.Key("a/b", "c"); ack != "a/b/c" {
		t.Errorf("Key = %q", ack)
	}
}
