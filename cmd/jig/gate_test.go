package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/machine"
)

// gateWorkCfg loads a config body as a repository's .jigz/config.yaml
// and returns the gate inputs it resolves to over an empty machine
// config.
func gateWorkCfg(t *testing.T, body string, st machine.State) gateInputs {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "repo: o/r\n"+body)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return gateInputs{
		state: st, work: cfg.Resolve(),
		agents: cfg.Agents, refs: cfg.UnresolvedRefs(), operating: cfg.OperatingHours,
	}
}

const (
	opHoursFinish = "operating_hours:\n  timezone: UTC\n  windows:\n    - {days: [mon, tue, wed, thu, fri], start: \"09:00\", end: \"18:00\"}\n"
	opHoursStop   = opHoursFinish + "  end: stop\n"
)

// 2026-10-21 is a Wednesday.
var (
	gateOpenAt   = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	gateClosedAt = time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
)

func TestGateDecide(t *testing.T) {
	cases := []struct {
		name  string
		in    gateInputs
		now   time.Time
		loops []gateLoop
		want  gateAction
	}{
		{"reopen starts a held loop", gateInputs{}, gateOpenAt, []gateLoop{{name: "a", held: true}}, gateStart},
		{"reopen leaves a loop stopped for another reason", gateInputs{}, gateOpenAt, []gateLoop{{name: "a"}}, gateNone},
		{"reopen leaves a paused loop", gateInputs{}, gateOpenAt, []gateLoop{{name: "a", held: true, paused: true}}, gateNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, acts := gateDecide(c.in, c.now, c.loops)
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

const (
	workPlan       = "work:\n  plan:\n    agent: [a]\n"
	agentsAB       = "agents:\n  a:\n    runtime: pi\n  b:\n    runtime: pi\n"
	agentPlainA    = "agents:\n  a:\n    runtime: pi\n"
	agentHoursA    = "agents:\n  a:\n    runtime: pi\n    schedule:\n      timezone: UTC\n      windows:\n        - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"09:00\", end: \"11:00\"}\n"
	opHoursMorning = "operating_hours:\n  timezone: UTC\n  windows:\n    - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"09:00\", end: \"11:00\"}\n"
)

func TestWorkGateDecide(t *testing.T) {
	// 2026-10-21 12:00 UTC is outside the 09:00-11:00 windows.
	now := gateOpenAt
	nextDay9 := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		cfg    string
		st     machine.State
		env    map[string]string
		gate   engine.Gate
		now    time.Time
		loops  []gateLoop
		open   bool
		agent  string
		reason string
		eta    time.Time
		end    string
		want   gateAction
	}{
		{
			name: "an available candidate is picked in order", cfg: agentsAB + "work:\n  plan:\n    agent: [b, a]\n",
			now: now, loops: []gateLoop{{name: "plan", up: true}}, open: true, agent: "b", want: gateNone,
		},
		{
			name: "an exhausted candidate is skipped", cfg: agentsAB + "work:\n  plan:\n    agent: [a, b]\n",
			st:  machine.State{Agents: map[string]machine.EntryState{"a": {ExhaustedUntil: now.Add(time.Hour)}}},
			now: now, loops: []gateLoop{{name: "plan", up: true}}, open: true, agent: "b", want: gateNone,
		},
		{
			name: "operating hours closed alone", cfg: agentPlainA + opHoursMorning + workPlan,
			now: now, loops: []gateLoop{{name: "plan", up: true}},
			agent: "a", reason: reasonOperatingHours, eta: nextDay9, end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "agent hours closed alone", cfg: agentHoursA + workPlan,
			now: now, loops: []gateLoop{{name: "plan", up: true}},
			agent: "a", reason: "agent a is outside its working hours", eta: nextDay9, end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "both closed", cfg: agentHoursA + opHoursMorning + workPlan,
			now: now, loops: []gateLoop{{name: "plan", up: true}},
			agent: "a", reason: reasonOperatingHours, eta: nextDay9, end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "operating hours end wins when operating hours closed",
			cfg:  agentEnd("finish") + opEnd("stop") + workPlan,
			now:  now, loops: []gateLoop{{name: "plan", up: true}},
			agent: "a", reason: reasonOperatingHours, eta: nextDay9, end: machine.EndStop, want: gateStopNow,
		},
		{
			name: "operating hours finish wins over an agent end stop",
			cfg:  agentEnd("stop") + opEnd("finish") + workPlan,
			now:  now, loops: []gateLoop{{name: "plan", up: true}},
			agent: "a", reason: reasonOperatingHours, eta: nextDay9, end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "the agent end applies when only its hours closed",
			cfg:  agentEnd("stop") + workPlan,
			now:  now, loops: []gateLoop{{name: "plan", up: true}},
			agent: "a", reason: "agent a is outside its working hours", eta: nextDay9, end: machine.EndStop, want: gateStopNow,
		},
		{
			name: "all candidates outside their hours",
			cfg:  "agents:\n  a:\n    runtime: pi\n    schedule:\n      timezone: UTC\n      windows:\n        - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"09:00\", end: \"11:00\"}\n  b:\n    runtime: pi\n    schedule:\n      timezone: UTC\n      windows:\n        - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"08:00\", end: \"10:00\"}\nwork:\n  plan:\n    agent: [a, b]\n",
			now:  now, loops: []gateLoop{{name: "plan", up: true}},
			agent: "a", reason: "agent a is outside its working hours", eta: time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC),
			end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "all candidates exhausted",
			cfg:  "agents:\n  a:\n    runtime: pi\n  b:\n    runtime: pi\nwork:\n  plan:\n    agent: [a, b]\n",
			st: machine.State{Agents: map[string]machine.EntryState{
				"a": {ExhaustedUntil: time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)},
				"b": {ExhaustedUntil: time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC)},
			}},
			now: now, loops: []gateLoop{{name: "plan", up: true}},
			agent: "a", reason: reasonExhausted, eta: time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC),
			end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "all from_env references unresolved",
			cfg:  "agents:\n  a:\n    runtime: pi\n    env:\n      PLAN_TOKEN: {from_env: JIGZ_TEST_PLAN_TOKEN}\n  b:\n    runtime: pi\n    env:\n      PLAN_TOKEN: {from_env: JIGZ_TEST_PLAN_TOKEN}\nwork:\n  plan:\n    agent: [a, b]\n",
			env:  map[string]string{"JIGZ_TEST_PLAN_TOKEN": ""},
			now:  now, loops: []gateLoop{{name: "plan", up: true}}, agent: "a",
			reason: reasonUnresolved, end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "all runtimes unregistered on the engine",
			cfg:  "agents:\n  a:\n    runtime: claude-code\n  b:\n    runtime: claude-code\nwork:\n  plan:\n    agent: [a, b]\n",
			gate: testGate("1.28.1", "github", "pi"),
			now:  now, loops: []gateLoop{{name: "plan", up: true}}, agent: "a",
			reason: reasonUnregistered, end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "the estimated start is the earliest of the three inputs",
			cfg:  "agents:\n  a:\n    runtime: pi\n    schedule:\n      timezone: UTC\n      windows:\n        - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"13:00\", end: \"18:00\"}\n  b:\n    runtime: pi\n" + "operating_hours:\n  timezone: UTC\n  windows:\n    - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"15:00\", end: \"20:00\"}\nwork:\n  plan:\n    agent: [a, b]\n",
			st: machine.State{Agents: map[string]machine.EntryState{
				"b": {ExhaustedUntil: time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)},
			}},
			now: time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC), loops: []gateLoop{{name: "plan", up: true}},
			agent: "a", reason: reasonOperatingHours, eta: time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC),
			end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "a closed work gate stops a paused loop", cfg: agentHoursA + workPlan,
			now: now, loops: []gateLoop{{name: "plan", up: true, paused: true}},
			agent: "a", reason: "agent a is outside its working hours", eta: nextDay9, end: machine.EndFinish, want: gateStopIdle,
		},
		{
			name: "an open work gate never starts a paused loop", cfg: agentPlainA + workPlan,
			now: now, loops: []gateLoop{{name: "plan", held: true, paused: true}},
			open: true, agent: "a", want: gateNone,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			in := gateWorkCfg(t, c.cfg, c.st)
			in.engine = c.gate
			views, acts := gateDecide(in, c.now, c.loops)
			lv := views["plan"]
			if lv.Open != c.open || lv.Agent != c.agent || lv.Reason != c.reason || !lv.ETA.Equal(c.eta) {
				t.Errorf("view %+v, want open=%v agent=%q reason=%q eta=%v", lv, c.open, c.agent, c.reason, c.eta)
			}
			if !c.open && lv.End != c.end {
				t.Errorf("end %q, want %q", lv.End, c.end)
			}
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

// agentEnd is an agent with closed hours and the given end mode.
func agentEnd(end string) string {
	return "agents:\n  a:\n    runtime: pi\n    schedule:\n      timezone: UTC\n      end: " + end + "\n      windows:\n        - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"09:00\", end: \"11:00\"}\n"
}

// opEnd is operating hours with closed windows and the given end mode.
func opEnd(end string) string {
	return "operating_hours:\n  timezone: UTC\n  end: " + end + "\n  windows:\n    - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"09:00\", end: \"11:00\"}\n"
}

// A window that wraps past midnight runs across the spring-forward
// night of Europe/Berlin: the wall clocks 02:00 to 02:59 do not exist on
// 2026-03-29 and the window ends when the clock jumps.
func TestWorkGateWindowWrapsMidnightAcrossDST(t *testing.T) {
	cfg := "agents:\n  a:\n    runtime: pi\n    schedule:\n      timezone: Europe/Berlin\n      windows:\n        - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"22:00\", end: \"02:00\"}\n" + workPlan
	in := gateWorkCfg(t, cfg, machine.State{})
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		now    time.Time
		open   bool
		reason string
		eta    time.Time
	}{
		{"the evening before the change", time.Date(2026, 3, 28, 23, 0, 0, 0, berlin), true, "", time.Time{}},
		{"the wrapped hours before the change", time.Date(2026, 3, 29, 0, 30, 0, 0, berlin), true, "", time.Time{}},
		{"the last minute before the jump", time.Date(2026, 3, 29, 1, 59, 0, 0, berlin), true, "", time.Time{}},
		{"after the jump past the window end", time.Date(2026, 3, 29, 3, 30, 0, 0, berlin), false, "agent a is outside its working hours",
			time.Date(2026, 3, 29, 22, 0, 0, 0, berlin)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			views, _ := gateDecide(in, c.now, []gateLoop{{name: "plan", up: true}})
			lv := views["plan"]
			if lv.Open != c.open || lv.Reason != c.reason || !lv.ETA.Equal(c.eta) {
				t.Errorf("at %v: %+v, want open=%v reason=%q eta=%v", c.now, lv, c.open, c.reason, c.eta)
			}
		})
	}
}

// Agent hours in Asia/Tokyo and operating hours in America/New_York open
// and close at different moments. The two windows overlap between 08:00
// and 09:00 UTC.
func TestWorkGateTimezones(t *testing.T) {
	cfg := "agents:\n  a:\n    runtime: pi\n    schedule:\n      timezone: Asia/Tokyo\n      windows:\n        - {days: [mon, tue, wed, thu, fri], start: \"09:00\", end: \"18:00\"}\n" +
		"operating_hours:\n  timezone: America/New_York\n  windows:\n    - {days: [mon, tue, wed, thu, fri], start: \"04:00\", end: \"17:00\"}\n" + workPlan
	in := gateWorkCfg(t, cfg, machine.State{})
	cases := []struct {
		name   string
		now    time.Time
		open   bool
		reason string
		eta    time.Time
	}{
		{"operating hours open while the agent is in its night",
			time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC), false, "agent a is outside its working hours",
			time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)},
		{"the agent is at work while operating hours are closed",
			time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC), false, reasonOperatingHours,
			time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)},
		{"both open", time.Date(2026, 10, 21, 8, 30, 0, 0, time.UTC), true, "", time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			views, _ := gateDecide(in, c.now, []gateLoop{{name: "plan", up: true}})
			lv := views["plan"]
			if lv.Open != c.open || lv.Reason != c.reason || !lv.ETA.Equal(c.eta) {
				t.Errorf("at %v: %+v, want open=%v reason=%q eta=%v", c.now, lv, c.open, c.reason, c.eta)
			}
		})
	}
}

// Loops of one work entry are evaluated independently.
func TestGateLoopsOfOneWorkEntryAreIndependent(t *testing.T) {
	in := gateWorkCfg(t, agentPlainA+workPlan, machine.State{})
	res := in.work["plan"]
	in.work = map[string]config.Resolved{"plan": res, "plan-build": res}
	views, acts := gateDecide(in, gateOpenAt, []gateLoop{
		{name: "plan", up: true},
		{name: "plan-build", held: true},
	})
	if !views["plan"].Open || !views["plan-build"].Open || views["plan"].Agent != "a" {
		t.Errorf("views %+v", views)
	}
	if len(acts) != 1 || acts[0] != (gateAct{name: "plan-build", action: gateStart}) {
		t.Errorf("actions %v, want the held loop to start", acts)
	}
}

type gateRig struct {
	sup     *supervisor
	root    string
	now     time.Time
	active  bool
	envFile string
}

func newGateRig(t *testing.T, opHours string) *gateRig {
	t.Helper()
	sup, root := startedSup(t, "echo \"${SORTIE_AGENT_KIND-unset}|${SORTIE_AGENT_COMMAND-unset}\" > agentenv\nwhile true; do sleep 1; done\n")
	r := &gateRig{sup: sup, root: root, now: gateOpenAt, envFile: filepath.Join(root, "agentenv")}
	sup.clock = func() time.Time { return r.now }
	sup.sessions = func(string) (bool, bool) { return r.active, true }
	if opHours != "" {
		mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "repo: owner/name\n"+opHours)
	}
	sup.refreshState(r.now, true)
	return r
}

// setConfig replaces the repo's .jigz/config.yaml and reloads it.
func (r *gateRig) setConfig(t *testing.T, body string) {
	t.Helper()
	mustWrite(t, filepath.Join(r.root, ".jigz", "config.yaml"), "repo: owner/name\n"+body)
	r.sup.refreshState(r.now, true)
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

// feed writes engine log lines to the loop's live tracker.
func (r *gateRig) feed(t *testing.T, lines ...string) {
	t.Helper()
	r.sup.mu.Lock()
	p := r.sup.procs["plan"]
	r.sup.mu.Unlock()
	if p == nil {
		t.Fatal("the plan loop is not running")
	}
	if _, err := io.WriteString(p.live.writer(), strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
}

func TestGateStopEndsLoopsNowAndReopenStartsThem(t *testing.T) {
	r := newGateRig(t, opHoursStop)
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
	if w := r.workflow(t); w.State != "offline" || w.OfflineReason != "outside operating hours" {
		t.Errorf("status %+v", w)
	}
	r.now = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	r.step(t, 0)
	if r.sup.empty() {
		t.Fatal("the window opening did not start the loop")
	}
	if w := r.workflow(t); w.State != "running" || w.OfflineReason != "" {
		t.Errorf("status after reopen %+v", w)
	}
}

func TestGateFinishWaitsForTheSessionNotTheRetry(t *testing.T) {
	r := newGateRig(t, opHoursFinish)
	r.now = gateClosedAt
	r.active = true
	r.step(t, 0)
	if r.sup.empty() {
		t.Fatal("finish stopped a loop with a running session")
	}
	if w := r.workflow(t); w.State != "draining" || w.OfflineReason != "outside operating hours" {
		t.Errorf("status while draining %+v", w)
	}
	r.active = false
	r.step(t, 2*time.Second)
	r.settle(t)
	if !r.sup.empty() {
		t.Fatal("finish kept a loop with no running session")
	}
}

const (
	dispatched8 = `time=2026-10-26T18:30:13.585Z level=INFO msg="issue dispatched" issue_id=8 issue_identifier=8 agent_kind=mock rule_name=plan model="" effort=""`
	exiting8    = `time=2026-10-26T18:30:38.625Z level=INFO msg="worker exiting" issue_id=8 issue_identifier=8 session_id=s exit_kind=normal turns_completed=1`
	tickTwo     = `time=2026-10-26T18:30:14.593Z level=INFO msg="tick completed" candidates=0 dispatched=0 running=2 retrying=0 held_by_blockers=0`
)

// Several sessions when the gate closes: finish waits for all of them,
// stop ends the loop at once.
func TestGateSeveralSessionsWhenTheGateCloses(t *testing.T) {
	t.Run("finish waits", func(t *testing.T) {
		r := newGateRig(t, opHoursFinish)
		r.sup.sessions = r.sup.loopSessions
		r.feed(t, dispatched7, dispatched8, tickTwo)
		r.now = gateClosedAt
		r.step(t, 0)
		if r.sup.empty() {
			t.Fatal("finish stopped a loop with two running sessions")
		}
		if w := r.workflow(t); len(w.Sessions) != 2 {
			t.Errorf("sessions %+v, want two", w.Sessions)
		}
		r.feed(t, exiting7, exiting8, tickIdle)
		r.step(t, 2*time.Second)
		r.settle(t)
		if !r.sup.empty() {
			t.Fatal("finish kept a loop with no running session")
		}
	})
	t.Run("stop ends at once", func(t *testing.T) {
		r := newGateRig(t, opHoursStop)
		r.sup.sessions = r.sup.loopSessions
		r.feed(t, dispatched7, dispatched8, tickTwo)
		r.now = gateClosedAt
		r.step(t, 0)
		r.settle(t)
		if !r.sup.empty() {
			t.Fatal("end: stop left a loop with sessions running")
		}
	})
}

// A loop whose tick reports more sessions than the tracker saw start is
// never treated as idle.
func TestGateUnaccountedSessionsKeepTheLoopBusy(t *testing.T) {
	r := newGateRig(t, opHoursFinish)
	r.sup.sessions = r.sup.loopSessions
	r.feed(t, tickTwo)
	r.now = gateClosedAt
	r.step(t, 0)
	if r.sup.empty() {
		t.Fatal("finish stopped a loop whose sessions cannot be accounted for")
	}
}

// The work entry's agent hours closing during a session: the loop goes
// offline with a reason and an estimate, then starts again.
func TestGateWorkEntryAgentHoursCloseAndReopen(t *testing.T) {
	openCfg := "agents:\n  a:\n    runtime: pi\n    schedule:\n      timezone: UTC\n      windows:\n        - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"00:00\", end: \"23:59\"}\nwork:\n  plan:\n    agent: a\n"
	closedCfg := "agents:\n  a:\n    runtime: pi\n    schedule:\n      timezone: UTC\n      windows:\n        - {days: [mon, tue, wed, thu, fri, sat, sun], start: \"20:00\", end: \"21:00\"}\nwork:\n  plan:\n    agent: a\n"
	r := newGateRig(t, "")
	r.sup.sessions = r.sup.loopSessions
	r.setConfig(t, openCfg)
	r.step(t, time.Minute)
	if r.sup.empty() {
		t.Fatal("open agent hours did not run the loop")
	}
	if w := r.workflow(t); w.State != "running" || w.Agent != "a" || w.OfflineReason != "" {
		t.Errorf("status with open hours %+v", w)
	}
	r.feed(t, dispatched7, tickBusy)
	if w := r.workflow(t); len(w.Sessions) != 1 || w.Sessions[0].Agent != "a" {
		t.Errorf("session record %+v, want one session of agent a", w.Sessions)
	}
	r.setConfig(t, closedCfg)
	r.step(t, 0)
	if r.sup.empty() {
		t.Fatal("finish stopped a loop with a running session")
	}
	if w := r.workflow(t); w.State != "draining" || w.OfflineReason != "agent a is outside its working hours" {
		t.Errorf("status while draining %+v", w)
	}
	r.feed(t, exiting7, tickIdle)
	r.step(t, 2*time.Second)
	r.settle(t)
	w := r.workflow(t)
	if w.State != "offline" || w.OfflineReason != "agent a is outside its working hours" || w.Agent != "a" {
		t.Errorf("offline status %+v", w)
	}
	if want := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC); w.ETA == nil || !w.ETA.Equal(want) {
		t.Errorf("eta %v, want %v", w.ETA, want)
	}
	r.setConfig(t, openCfg)
	r.step(t, 0)
	if r.sup.empty() {
		t.Fatal("reopening the agent hours did not start the loop")
	}
	if w := r.workflow(t); w.State != "running" || w.Agent != "a" || w.OfflineReason != "" {
		t.Errorf("status after reopen %+v", w)
	}
}

func TestGateDoesNotStartAPausedLoop(t *testing.T) {
	r := newGateRig(t, opHoursFinish)
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
	r := newGateRig(t, opHoursFinish)
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
	if w := r.workflow(t); w.State != "offline" {
		t.Errorf("state %q, want offline", w.State)
	}
	r.step(t, 0)
	r.now = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	r.step(t, 0)
	if r.sup.empty() {
		t.Error("reopening did not start the held loop")
	}
}

// The supervisor never injects agent overrides: the document carries
// the agent kind and command, and the engine reads them from the file.
func TestNoAgentOverridesAreInjected(t *testing.T) {
	r := newGateRig(t, opHoursFinish)
	if got := r.agentEnv(t); got != "unset|unset" {
		t.Errorf("child env %q, want unset|unset", got)
	}
}

func TestInvalidConfigFailsTheStart(t *testing.T) {
	root := t.TempDir()
	sup := newTestSup(t, root, "plan")
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "work: [unclosed\n")
	mustWrite(t, filepath.Join(root, ".jigz", "workflows", "WORKFLOW.plan.md"), "---\n---\n")
	if err := sup.startAll([]string{"plan"}); err == nil {
		t.Error("startAll accepted an invalid config")
	}
}

const agentDaily = "agents:\n  a:\n    runtime: claude-code\n    model: m\n    schedule:\n      timezone: UTC\n      windows:\n        - {days: [wed, thu], start: \"09:00\", end: \"18:00\"}\nwork:\n  plan:\n    agent: a\n    query_filter: \"label:x\"\n    rules:\n      - { name: r, match: { labels: [x] }, prompt: p.md }\n"

func TestAnAccountClosingBetweenDecisionAndStartHoldsTheLoop(t *testing.T) {
	r := newGateRig(t, "")
	mustWrite(t, filepath.Join(r.root, ".jigz", "config.yaml"), "repo: owner/name\n"+agentDaily)
	r.sup.refreshState(r.now, true)
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
	if w := r.workflow(t); w.State != "offline" {
		t.Errorf("state %q, want offline", w.State)
	}

	r.now = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	r.sup.clock = func() time.Time { return r.now }
	r.step(t, 0)
	if r.sup.empty() {
		t.Fatal("the account opening did not start the held loop")
	}
}
