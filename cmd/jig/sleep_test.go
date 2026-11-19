package main

import (
	"encoding/json"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/config"
)

type sleepRig struct {
	sup   *supervisor
	root  string
	now   time.Time
	idle  bool
	known bool
	polls int
}

func newSleepRig(t *testing.T, enabled bool) *sleepRig {
	t.Helper()
	sup, root := startedSup(t, "while true; do sleep 1; done\n")
	r := &sleepRig{sup: sup, root: root, now: time.Now(), idle: true, known: true}
	sup.clock = func() time.Time { return r.now }
	sup.quiet = func(string) (bool, bool) { r.polls++; return r.idle, r.known }
	sup.sleepCfg = config.Sleep{Enabled: enabled, IdleTimeout: 10 * time.Minute, WakeInterval: time.Hour}
	sup.idleSince = r.now
	return r
}

func (r *sleepRig) step(t *testing.T, d time.Duration) {
	t.Helper()
	r.now = r.now.Add(d)
	if code, done := r.sup.sleepStep(); done {
		t.Fatalf("sleepStep ended the run with code %d", code)
	}
}

// settle delivers the exit events of the stopped children, as run does.
func (r *sleepRig) settle(t *testing.T) {
	t.Helper()
	for !r.sup.empty() {
		select {
		case ev := <-r.sup.events:
			if code, done := r.sup.onExit(ev); done {
				t.Fatalf("a sleep stop ended the run with code %d", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("child did not exit")
		}
	}
}

func (r *sleepRig) sleeping() bool {
	r.sup.mu.Lock()
	defer r.sup.mu.Unlock()
	return r.sup.sleeping
}

func (r *sleepRig) put(t *testing.T) {
	t.Helper()
	if err := requestWake(r.root); err != nil {
		t.Fatal(err)
	}
}

func TestSleepsAfterIdleTimeout(t *testing.T) {
	r := newSleepRig(t, true)
	r.step(t, time.Minute)
	if r.sleeping() {
		t.Fatal("slept before idle_timeout")
	}
	r.step(t, 10*time.Minute)
	if !r.sleeping() {
		t.Fatal("did not sleep after idle_timeout")
	}
	if w := readStatus(t, r.root).Workflows[0]; w.State != "sleeping" {
		t.Errorf("workflow state %q, want sleeping", w.State)
	}
	r.settle(t)
	if r.sup.failed || r.sup.finished() {
		t.Error("a sleep stop was treated as a crash or ended the run")
	}
}

func TestBusyChildBlocksSleep(t *testing.T) {
	r := newSleepRig(t, true)
	r.idle = false
	r.step(t, time.Hour)
	if r.sleeping() {
		t.Error("slept with a busy child")
	}
}

func TestQueryErrorBlocksSleep(t *testing.T) {
	r := newSleepRig(t, true)
	r.known = false
	r.step(t, time.Hour)
	if r.sleeping() {
		t.Error("slept although the state query failed")
	}
}

func TestBusyObservationResetsTheIdleClock(t *testing.T) {
	r := newSleepRig(t, true)
	r.step(t, 5*time.Minute)
	r.idle = false
	r.step(t, time.Minute)
	r.idle = true
	r.step(t, time.Minute)
	r.step(t, 9*time.Minute)
	if r.sleeping() {
		t.Fatal("slept 9 minutes after the last busy observation")
	}
	r.step(t, time.Minute)
	if !r.sleeping() {
		t.Error("did not sleep 10 minutes after the last busy observation")
	}
}

func TestPollIsRateLimited(t *testing.T) {
	r := newSleepRig(t, true)
	r.step(t, time.Minute)
	r.step(t, 2*time.Second)
	r.step(t, 2*time.Second)
	if r.polls != 1 {
		t.Fatalf("polled %d times within one interval, want 1", r.polls)
	}
	r.step(t, sleepPollEvery)
	if r.polls != 2 {
		t.Errorf("polled %d times, want 2", r.polls)
	}
}

func (r *sleepRig) sleepNow(t *testing.T) {
	t.Helper()
	r.step(t, time.Minute)
	r.step(t, 10*time.Minute)
	if !r.sleeping() {
		t.Fatal("not sleeping")
	}
	r.settle(t)
}

func TestWakesAtTheInterval(t *testing.T) {
	r := newSleepRig(t, true)
	r.sleepNow(t)
	r.step(t, 59*time.Minute)
	if !r.sleeping() || !r.sup.empty() {
		t.Fatal("woke before wake_interval")
	}
	r.step(t, 2*time.Minute)
	if r.sleeping() || r.sup.empty() {
		t.Fatal("did not wake at wake_interval")
	}
	doc := readStatus(t, r.root)
	if !doc.Sleep.Awake || doc.Sleep.IdleSince == nil || doc.Workflows[0].State != "running" {
		t.Errorf("status after wake: %+v", doc)
	}
	r.step(t, time.Minute)
	if r.sleeping() {
		t.Error("slept again before a fresh idle_timeout")
	}
}

func TestManualWake(t *testing.T) {
	r := newSleepRig(t, true)
	r.sleepNow(t)
	r.put(t)
	r.step(t, 2*time.Second)
	if r.sleeping() || r.sup.empty() {
		t.Error("manual wake did not start the children")
	}
}

func TestWakeWhileStoppingWaitsForTheExit(t *testing.T) {
	r := newSleepRig(t, true)
	r.step(t, time.Minute)
	r.step(t, 10*time.Minute)
	r.put(t)
	r.step(t, 2*time.Second)
	if !r.sleeping() {
		t.Fatal("woke while the old child was still exiting")
	}
	r.settle(t)
	r.step(t, 2*time.Second)
	if r.sleeping() || r.sup.empty() {
		t.Error("wake was lost")
	}
}

func TestWakeWhileAwakeDoesNothing(t *testing.T) {
	r := newSleepRig(t, true)
	pid := r.sup.procs["plan"].cmd.Process.Pid
	r.put(t)
	r.step(t, 2*time.Second)
	if r.sleeping() || r.sup.procs["plan"].cmd.Process.Pid != pid || r.sup.procs["plan"].restarts != 0 {
		t.Error("a wake while awake changed the child")
	}
	r.sleepNow(t)
	r.step(t, 2*time.Second)
	if !r.sleeping() {
		t.Error("a stale wake request woke the next sleep")
	}
}

func TestStopWhileSleepingEndsTheRun(t *testing.T) {
	r := newSleepRig(t, true)
	r.sleepNow(t)
	if _, done := r.sup.onSignal(syscall.SIGTERM); !done {
		t.Error("SIGTERM while sleeping did not end the run")
	}
}

func TestRestartWhileSleepingWakes(t *testing.T) {
	r := newSleepRig(t, true)
	r.sleepNow(t)
	if _, done := r.sup.onSignal(sigRestartIdle); done || r.sleeping() || r.sup.empty() {
		t.Error("restart --when-idle did not wake the sleeping loop")
	}
}

func TestShutdownWhileSleepingLeavesNoChild(t *testing.T) {
	r := newSleepRig(t, true)
	r.step(t, time.Minute)
	r.step(t, 10*time.Minute)
	pid := r.sup.procs["plan"].cmd.Process.Pid
	r.sup.shutdown()
	if !r.sup.empty() {
		t.Error("children remain after shutdown")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Error("child process still exists")
	}
}

func TestDisabledNeverSleeps(t *testing.T) {
	r := newSleepRig(t, false)
	r.step(t, 24*time.Hour)
	r.put(t)
	r.step(t, 24*time.Hour)
	if r.sleeping() || r.polls != 0 {
		t.Errorf("disabled sleep acted: sleeping=%v polls=%d", r.sleeping(), r.polls)
	}
}

func rawSleep(t *testing.T, root string) map[string]any {
	t.Helper()
	path, _ := statusPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Sleep map[string]any `json:"sleep"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Sleep
}

func TestStatusSleepObject(t *testing.T) {
	off := newSleepRig(t, false)
	off.sup.republish()
	s := rawSleep(t, off.root)
	if _, has := s["idle_since"]; s["enabled"] != false || s["awake"] != true || has ||
		s["sleeping_since"] != nil || s["next_wake"] != nil {
		t.Errorf("disabled: %v", s)
	}

	r := newSleepRig(t, true)
	r.sup.republish()
	s = rawSleep(t, r.root)
	if s["enabled"] != true || s["awake"] != true || s["idle_since"] == nil || s["sleeping_since"] != nil || s["next_wake"] != nil {
		t.Errorf("awake: %v", s)
	}
	r.idle = false
	r.step(t, time.Minute)
	r.sup.republish()
	if s = rawSleep(t, r.root); s["idle_since"] != nil {
		t.Errorf("busy: idle_since %v, want null", s["idle_since"])
	}
	r.idle = true
	r.step(t, time.Minute)
	r.step(t, 10*time.Minute)
	s = rawSleep(t, r.root)
	if s["awake"] != false || s["sleeping_since"] == nil || s["next_wake"] == nil || s["idle_since"] != nil {
		t.Errorf("sleeping: %v", s)
	}
	want := r.now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if got, _ := time.Parse(time.RFC3339Nano, s["next_wake"].(string)); !got.Equal(r.now.Add(time.Hour)) {
		t.Errorf("next_wake %v, want %s", s["next_wake"], want)
	}
}
