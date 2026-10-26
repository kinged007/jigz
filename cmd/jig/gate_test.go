package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/machine"
)

func gateCfg(t *testing.T, body string) *machine.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "machine.yaml")
	mustWrite(t, p, body)
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := machine.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const (
	hoursFinish = "hours:\n  timezone: UTC\n  windows:\n    - {days: [mon, tue, wed, thu, fri], start: \"09:00\", end: \"18:00\"}\n"
	hoursStop   = hoursFinish + "  end: stop\n"
	poolTwo     = "pool:\n  - {name: c, runtime: claude-code}\n  - {name: p, runtime: pi}\n"
)

// 2026-10-21 is a Wednesday.
var (
	gateOpenAt   = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	gateClosedAt = time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
)

func TestGateDecide(t *testing.T) {
	exhausted := func(names ...string) machine.State {
		st := machine.State{}
		for _, n := range names {
			st[n] = machine.EntryState{ExhaustedUntil: gateOpenAt.Add(24 * time.Hour)}
		}
		return st
	}
	up := gateLoop{name: "a", up: true}
	cases := []struct {
		name  string
		cfg   string
		st    machine.State
		now   time.Time
		loops []gateLoop
		want  gateAction
	}{
		{"closed finish waits for idle", hoursFinish, nil, gateClosedAt, []gateLoop{up}, gateStopIdle},
		{"closed stop stops now", hoursStop, nil, gateClosedAt, []gateLoop{up}, gateStopNow},
		{"no runtime finishes although end is stop", hoursStop + poolTwo, exhausted("c", "p"), gateOpenAt, []gateLoop{{name: "a", up: true, runtime: "pi"}}, gateStopIdle},
		{"window closed with end stop and a pool still stops now", hoursStop + poolTwo, nil, gateClosedAt, []gateLoop{{name: "a", up: true, runtime: "claude-code"}}, gateStopNow},
		{"closed loop that is down needs nothing", hoursStop, nil, gateClosedAt, []gateLoop{{name: "a", held: true}}, gateNone},
		{"reopen starts a held loop", hoursFinish, nil, gateOpenAt, []gateLoop{{name: "a", held: true}}, gateStart},
		{"reopen leaves a loop stopped for another reason", hoursFinish, nil, gateOpenAt, []gateLoop{{name: "a"}}, gateNone},
		{"reopen leaves a paused loop", hoursFinish, nil, gateOpenAt, []gateLoop{{name: "a", held: true, paused: true}}, gateNone},
		{"closed leaves a paused loop", hoursFinish, nil, gateClosedAt, []gateLoop{{name: "a", paused: true}}, gateNone},
		{"runtime switch recycles when idle", poolTwo, exhausted("c"), gateOpenAt, []gateLoop{{name: "a", up: true, runtime: "claude-code"}}, gateRecycleIdle},
		{"same runtime is left alone", poolTwo, nil, gateOpenAt, []gateLoop{{name: "a", up: true, runtime: "claude-code"}}, gateNone},
		{"a loop that is stopping is not recycled", poolTwo, exhausted("c"), gateOpenAt, []gateLoop{{name: "a", runtime: "claude-code"}}, gateNone},
		{"removing the pool recycles", "", nil, gateOpenAt, []gateLoop{{name: "a", up: true, runtime: "pi"}}, gateRecycleIdle},
		{"no hours and no pool does nothing", "", nil, gateClosedAt, []gateLoop{up}, gateNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, acts := gateDecide(gateCfg(t, c.cfg), c.st, c.now, c.loops)
			got := gateNone
			if len(acts) > 0 {
				got = acts[0].action
			}
			if got != c.want || len(acts) > 1 {
				t.Errorf("actions %v, want %v", acts, c.want)
			}
		})
	}
}

func TestGateViewHold(t *testing.T) {
	cfg := gateCfg(t, hoursFinish+poolTwo)
	st := machine.State{
		"c": {ExhaustedUntil: gateOpenAt.Add(time.Hour)},
		"p": {ExhaustedUntil: gateOpenAt.Add(2 * time.Hour)},
	}
	v := gateEval(cfg, st, gateOpenAt)
	if v.Open || v.Reason != gatePool || !strings.Contains(v.hold(), "no agent account available until "+gateOpenAt.Add(time.Hour).Format(time.RFC3339)) {
		t.Errorf("pool hold: %+v %q", v, v.hold())
	}
	v = gateEval(cfg, nil, gateClosedAt)
	if v.Open || v.Reason != gateHours || v.hold() != "outside working hours" {
		t.Errorf("hours hold: %+v %q", v, v.hold())
	}
}

type gateRig struct {
	sup     *supervisor
	root    string
	now     time.Time
	active  bool
	envFile string
}

func newGateRig(t *testing.T, machineYAML string) *gateRig {
	t.Helper()
	sup, root := startedSup(t, "echo \"${SORTIE_AGENT_KIND-unset}|${SORTIE_AGENT_COMMAND-unset}\" > agentenv\nwhile true; do sleep 1; done\n")
	r := &gateRig{sup: sup, root: root, now: gateOpenAt, envFile: filepath.Join(root, "agentenv")}
	sup.clock = func() time.Time { return r.now }
	sup.sessions = func(string) (bool, bool) { return r.active, true }
	r.setMachine(t, machineYAML)
	return r
}

func (r *gateRig) setMachine(t *testing.T, body string) {
	t.Helper()
	mustWrite(t, r.sup.machinePath, body)
	if err := os.Chmod(r.sup.machinePath, 0o600); err != nil {
		t.Fatal(err)
	}
	r.sup.refreshMachine(r.now, true)
}

func (r *gateRig) step(t *testing.T, d time.Duration) {
	t.Helper()
	r.now = r.now.Add(d)
	r.sup.mu.Lock()
	for _, p := range r.sup.procs {
		p.since = time.Now().Add(-time.Minute)
	}
	r.sup.mu.Unlock()
	if code, done := r.sup.gateStep(); done {
		t.Fatalf("gateStep ended the run with code %d", code)
	}
}

func (r *gateRig) settle(t *testing.T) {
	t.Helper()
	for {
		r.sup.mu.Lock()
		waiting := false
		for _, p := range r.sup.procs {
			waiting = waiting || p.stopping
		}
		r.sup.mu.Unlock()
		if !waiting {
			return
		}
		select {
		case ev := <-r.sup.events:
			if code, done := r.sup.onExit(ev); done {
				t.Fatalf("a gate stop ended the run with code %d", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("child did not exit")
		}
	}
}

func (r *gateRig) agentEnv(t *testing.T) string {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(r.envFile); err == nil && strings.Contains(string(b), "\n") {
			_ = os.Remove(r.envFile)
			return strings.TrimSpace(string(b))
		}
	}
	t.Fatal("the child did not report its environment")
	return ""
}

func (r *gateRig) workflow(t *testing.T) workflowStatus {
	t.Helper()
	return readStatus(t, r.root).Workflows[0]
}

func TestGateStopEndsLoopsNowAndReopenStartsThem(t *testing.T) {
	r := newGateRig(t, hoursStop)
	r.active = true
	r.step(t, time.Minute)
	if r.sup.empty() {
		t.Fatal("an open window stopped the loop")
	}
	r.now = gateClosedAt
	r.step(t, 0)
	r.settle(t)
	if !r.sup.empty() {
		t.Fatal("end: stop left the loop running")
	}
	if w := r.workflow(t); w.State != "held" || w.Hold != "outside working hours" {
		t.Errorf("status %+v", w)
	}
	if g := readStatus(t, r.root).Gate; g == nil || g.Open || g.HoursOpen || g.NextChange == nil {
		t.Errorf("gate status %+v", g)
	}
	r.now = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	r.step(t, 0)
	if r.sup.empty() {
		t.Fatal("the window opening did not start the loop")
	}
	if w := r.workflow(t); w.State != "running" || w.Hold != "" {
		t.Errorf("status after reopen %+v", w)
	}
}

func TestGateFinishWaitsForTheSessionNotTheRetry(t *testing.T) {
	r := newGateRig(t, hoursFinish)
	r.now = gateClosedAt
	r.active = true
	r.step(t, 0)
	if r.sup.empty() {
		t.Fatal("finish stopped a loop with a running session")
	}
	if w := r.workflow(t); w.State != "running" || w.Hold != "outside working hours" {
		t.Errorf("status while draining %+v", w)
	}
	r.active = false
	r.step(t, 2*time.Second)
	r.settle(t)
	if !r.sup.empty() {
		t.Fatal("finish kept a loop with no running session")
	}
}

func TestGateDoesNotStartAPausedLoop(t *testing.T) {
	r := newGateRig(t, hoursFinish)
	r.sup.pauseLoop("plan", "manual")
	r.settle(t)
	r.now = gateClosedAt
	r.step(t, 0)
	r.now = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	r.step(t, 0)
	if !r.sup.empty() {
		t.Error("the gate started a paused loop")
	}
	if w := r.workflow(t); w.State != "paused" {
		t.Errorf("state %q, want paused", w.State)
	}
}

func TestGateHoldsLoopsAtBootAndWake(t *testing.T) {
	r := newGateRig(t, hoursFinish)
	r.sup.stopAll(false, false)
	r.settle(t)
	r.now = gateClosedAt
	if err := r.sup.startAll([]string{"plan"}); err != nil {
		t.Fatal(err)
	}
	if !r.sup.empty() {
		t.Fatal("startAll started a loop while the window was closed")
	}
	r.sup.republish()
	if w := r.workflow(t); w.State != "held" {
		t.Errorf("state %q, want held", w.State)
	}
	r.step(t, 0)
	r.now = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	r.step(t, 0)
	if r.sup.empty() {
		t.Error("reopening did not start the held loop")
	}
}

func TestGateRecyclesOnRuntimeChangeWhenIdle(t *testing.T) {
	r := newGateRig(t, poolTwo)
	r.sup.stopAll(false, false)
	r.settle(t)
	_ = os.Remove(r.envFile)
	r.sup.mu.Lock()
	r.sup.gateHeld["plan"] = true
	r.sup.mu.Unlock()
	r.step(t, 0)
	if got := r.agentEnv(t); !strings.HasPrefix(got, "claude-code|") || !strings.HasSuffix(got, " agent-exec claude-code") {
		t.Fatalf("child env %q", got)
	}

	statePath, _ := machine.StatePath()
	if err := machine.MarkExhausted(statePath, "c", r.now.Add(time.Hour), "limit"); err != nil {
		t.Fatal(err)
	}
	r.active = true
	r.step(t, 11*time.Second)
	if p := r.sup.procs["plan"]; p == nil || p.stopping || p.runtime != "claude-code" {
		t.Fatalf("a busy loop was recycled: %+v", p)
	}
	r.active = false
	r.step(t, 2*time.Second)
	r.settle(t)
	if got := r.agentEnv(t); !strings.HasPrefix(got, "pi|") || !strings.HasSuffix(got, " agent-exec pi") {
		t.Fatalf("recycled child env %q", got)
	}
	if p := r.sup.procs["plan"]; p == nil || p.runtime != "pi" {
		t.Fatalf("recycled loop %+v", p)
	}
}

func TestEmptyPoolLeavesTheAgentEnvironmentAlone(t *testing.T) {
	t.Setenv("SORTIE_AGENT_KIND", "keep")
	r := newGateRig(t, hoursFinish)
	if got := r.agentEnv(t); got != "keep|unset" {
		t.Errorf("child env %q, want keep|unset", got)
	}
}

func TestAPoolNeedsAPathWithoutWhitespace(t *testing.T) {
	r := newGateRig(t, poolTwo)
	old := selfExe
	selfExe = func() (string, error) { return "/opt/my tools/jig", nil }
	t.Cleanup(func() { selfExe = old })
	_, _, _, err := r.sup.childEnv("plan")
	if err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("error %v, want a whitespace error", err)
	}
}

func TestAnInvalidMachineFileFailsTheStart(t *testing.T) {
	sup := newTestSup(t, t.TempDir(), "plan")
	mustWrite(t, sup.machinePath, "hours: [\n")
	sup.bootErr = sup.refreshMachine(time.Now(), true)
	if err := sup.startAll([]string{"plan"}); err == nil {
		t.Error("startAll accepted an invalid machine file")
	}
}

func TestMachineFileReloadsWhenItChanges(t *testing.T) {
	r := newGateRig(t, "")
	r.step(t, 0)
	mustWrite(t, r.sup.machinePath, hoursFinish)
	if err := os.Chmod(r.sup.machinePath, 0o600); err != nil {
		t.Fatal(err)
	}
	r.now = gateClosedAt
	r.active = false
	r.step(t, 0)
	r.settle(t)
	if !r.sup.empty() {
		t.Error("a new machine file was not picked up")
	}
}

const poolDaily = "pool:\n  - name: c\n    runtime: claude-code\n    hours:\n      timezone: UTC\n      windows:\n        - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"09:00\", end: \"18:00\"}\n"

func TestAnAccountClosingBetweenDecisionAndStartHoldsTheLoop(t *testing.T) {
	r := newGateRig(t, poolDaily)
	r.sup.stopAll(false, false)
	r.settle(t)

	before := time.Date(2026, 10, 21, 17, 59, 59, 0, time.UTC)
	after := before.Add(2 * time.Second)
	reads := 0
	r.sup.clock = func() time.Time {
		reads++
		if reads == 1 {
			return before
		}
		return after
	}
	if err := r.sup.startAll([]string{"plan"}); err != nil {
		t.Fatalf("startAll failed on a boundary: %v", err)
	}
	if !r.sup.empty() {
		t.Fatal("a loop started without an available account")
	}
	r.sup.clock = func() time.Time { return after }
	r.sup.republish()
	if w := r.workflow(t); w.State != "held" {
		t.Errorf("state %q, want held", w.State)
	}

	r.now = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	r.sup.clock = func() time.Time { return r.now }
	r.step(t, 0)
	if r.sup.empty() {
		t.Fatal("the account opening did not start the held loop")
	}
}

func TestAWhitespacePathStaysFatalAtStart(t *testing.T) {
	r := newGateRig(t, poolTwo)
	r.sup.stopAll(false, false)
	r.settle(t)
	old := selfExe
	selfExe = func() (string, error) { return "/opt/my tools/jig", nil }
	t.Cleanup(func() { selfExe = old })
	if err := r.sup.startOne("plan"); err == nil {
		t.Error("a whitespace path did not fail the start")
	}
}

func TestGateStatusStaysCurrent(t *testing.T) {
	cfg := gateCfg(t, hoursFinish)
	open := gateEval(cfg, nil, gateOpenAt)
	s := &supervisor{}
	s.gateHeld = map[string]bool{}
	if s.noteGate(open, cfg) {
		t.Fatal("first open evaluation reported a change")
	}
	if s.noteGate(open, cfg) {
		t.Error("an identical view reported a change")
	}
	nextDay := open
	nextDay.Next = open.Next.Add(24 * time.Hour)
	if !s.noteGate(nextDay, cfg) {
		t.Error("a new next_change was not reported")
	}
	hours := nextDay
	hours.HoursOpen = false
	if !s.noteGate(hours, cfg) {
		t.Error("a new hours_open was not reported")
	}
}

func TestGateStatusRepublishesNextChange(t *testing.T) {
	r := newGateRig(t, hoursFinish)
	r.step(t, 0)
	r.now = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	r.step(t, 0)
	g := readStatus(t, r.root).Gate
	want := time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC)
	if g == nil || g.NextChange == nil || !g.NextChange.Equal(want) {
		t.Errorf("next_change %+v, want %v", g, want)
	}
}
