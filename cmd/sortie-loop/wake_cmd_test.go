package main

import (
	"strings"
	"testing"
	"time"

	"github.com/kinged007/sortie-loop/internal/loopsrc"
)

func TestWakeCommandWithoutSupervisor(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, err := wakeSupervisor(t.TempDir()); err == nil {
		t.Error("wake without a supervisor succeeded")
	}
}

func TestWakeCommandWhileAwake(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		r := newSleepRig(t, enabled)
		r.sup.republish()
		res, err := wakeSupervisor(r.root)
		if err != nil || res.Woke || res.WasSleeping {
			t.Errorf("enabled=%v: res %+v err %v, want a no-op", enabled, res, err)
		}
		if r.sup.consumeWake() {
			t.Errorf("enabled=%v: an awake wake left a request behind", enabled)
		}
	}
}

func TestWakeCommandWakesASleepingSupervisor(t *testing.T) {
	r := newSleepRig(t, true)
	r.sleepNow(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				r.sup.sleepStep()
			}
		}
	}()
	res, err := wakeSupervisor(r.root)
	close(stop)
	<-done
	if err != nil || !res.Woke || !res.WasSleeping {
		t.Errorf("res %+v err %v, want woke and was_sleeping", res, err)
	}
}

func TestWakeCommandTimesOut(t *testing.T) {
	old := wakeBudget
	wakeBudget = 300 * time.Millisecond
	defer func() { wakeBudget = old }()
	r := newSleepRig(t, true)
	r.sleepNow(t)
	res, err := wakeSupervisor(r.root)
	if err == nil || res.Woke || !res.WasSleeping {
		t.Errorf("res %+v err %v, want was_sleeping with an error", res, err)
	}
}

func TestSleepSummary(t *testing.T) {
	now := time.Now()
	if sleepSummary(sleepStatus{}) != "" {
		t.Error("disabled sleep printed a line")
	}
	if got := sleepSummary(sleepStatus{Enabled: true, Awake: true}); got != "sleep: awake" {
		t.Errorf("awake: %q", got)
	}
	got := sleepSummary(sleepStatus{Enabled: true, SleepingSince: &now, NextWake: &now})
	if got == "" || got == "sleep: awake" {
		t.Errorf("sleeping: %q", got)
	}
}

func TestParseArgsWakeIsASubcommand(t *testing.T) {
	c, err := parseArgs([]string{"wake", "--json", "/some/repo"})
	if err != nil || c.remote != "wake" || c.dir != "" || len(c.remoteArgs) != 2 {
		t.Errorf("wake parsed as %+v, err %v", c, err)
	}
}

func TestDashboardShowsSleep(t *testing.T) {
	now := time.Now()
	p := opsProject{}
	p.Status = &loopsrc.StatusInfo{
		Liveness: loopsrc.Live,
		Sleeping: true,
		Sleep:    loopsrc.SleepInfo{Enabled: true, SleepingSince: &now, NextWake: &now},
	}
	p.applyStatus(loopsrc.Capabilities{Sleep: true, Wake: true})
	if p.Sleep != "sleeping" || !strings.Contains(p.SleepNote, "since") {
		t.Errorf("sleep state = %q note %q", p.Sleep, p.SleepNote)
	}
	if !p.CanWake {
		t.Error("a sleeping project does not offer wake")
	}
}
