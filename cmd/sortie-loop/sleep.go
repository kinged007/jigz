package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// sleepPollEvery is how often every child is asked whether it has work.
const sleepPollEvery = 30 * time.Second

// wakeFileSuffix names the file `sortie-loop wake` creates next to the
// status file. A file is used because both control signals are taken and
// a third one (SIGHUP) is also what a closing terminal sends.
const wakeFileSuffix = ".wake"

// sleepStatus is the `sleep` object of the status document.
type sleepStatus struct {
	Enabled       bool       `json:"enabled"`
	Awake         bool       `json:"awake"`
	SleepingSince *time.Time `json:"sleeping_since"`
	NextWake      *time.Time `json:"next_wake"`
	IdleSince     *time.Time `json:"idle_since"`
}

func (s sleepStatus) MarshalJSON() ([]byte, error) {
	type plain sleepStatus
	if s.Enabled {
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		Enabled       bool       `json:"enabled"`
		Awake         bool       `json:"awake"`
		SleepingSince *time.Time `json:"sleeping_since"`
		NextWake      *time.Time `json:"next_wake"`
	}{s.Enabled, s.Awake, s.SleepingSince, s.NextWake})
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func (s *supervisor) sleepStatus() sleepStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sleepStatusLocked()
}

func (s *supervisor) sleepStatusLocked() sleepStatus {
	st := sleepStatus{Enabled: s.sleepCfg.Enabled, Awake: !s.sleeping}
	if s.sleeping {
		st.SleepingSince, st.NextWake = timePtr(s.sleepingSince), timePtr(s.nextWake)
	} else if s.sleepCfg.Enabled {
		st.IdleSince = timePtr(s.idleSince)
	}
	return st
}

// consumeWake reports whether a wake was requested, and clears the request.
func (s *supervisor) consumeWake() bool {
	path, err := cleanupFilePath(s.root, wakeFileSuffix)
	if err != nil {
		return false
	}
	return os.Remove(path) == nil
}

// sleepStep runs on every tick. Awake, it polls the children every
// sleepPollEvery and puts them to sleep once all have been idle for
// idle_timeout. Asleep, it starts them again at the wake time or on a
// manual wake.
func (s *supervisor) sleepStep() (int, bool) {
	if !s.sleepCfg.Enabled {
		return 0, false
	}
	now := s.clock()
	manual := s.consumeWake()
	s.mu.Lock()
	if s.sleeping {
		if manual {
			s.wakeNow = true
		}
		// A child that is still exiting has to be gone before it is
		// started again.
		due := (s.wakeNow || !now.Before(s.nextWake)) && len(s.procs) == 0
		s.mu.Unlock()
		if due {
			return s.wake(now)
		}
		return 0, false
	}
	if s.exitWhenEmpty || len(s.procs) == 0 || now.Sub(s.lastPoll) < s.pollEvery {
		s.mu.Unlock()
		return 0, false
	}
	var ports []int
	for _, p := range s.procs {
		if p.stopping {
			s.mu.Unlock()
			return 0, false
		}
		ports = append(ports, p.port)
	}
	s.lastPoll = now
	s.mu.Unlock()

	busy := false
	for _, port := range ports {
		if idle, known := s.quiet(port); !known || !idle {
			busy = true
			break
		}
	}
	s.mu.Lock()
	if busy {
		s.idleSince = time.Time{}
	} else if s.idleSince.IsZero() {
		s.idleSince = now
	}
	due := !busy && now.Sub(s.idleSince) >= s.sleepCfg.IdleTimeout
	s.mu.Unlock()
	if due {
		s.sleep(now)
	}
	return 0, false
}

// sleep stops every child as a requested stop, so no exit counts as a
// crash.
func (s *supervisor) sleep(now time.Time) {
	s.mu.Lock()
	s.sleeping, s.wakeNow = true, false
	s.sleepingSince, s.nextWake = now, now.Add(s.sleepCfg.WakeInterval)
	s.idleSince = time.Time{}
	s.mu.Unlock()
	fmt.Fprintf(os.Stderr, "sortie-loop: idle for %s, sleeping until %s\n",
		s.sleepCfg.IdleTimeout, now.Add(s.sleepCfg.WakeInterval).Format(time.RFC3339))
	s.stopAll(false, false)
	s.republish()
}

// wake starts every loop again. A child that fails to start ends the run,
// as it does at boot.
func (s *supervisor) wake(now time.Time) (int, bool) {
	s.mu.Lock()
	s.sleeping, s.wakeNow = false, false
	s.sleepingSince, s.nextWake = time.Time{}, time.Time{}
	s.idleSince, s.lastPoll = now, now
	names := make([]string, 0, len(s.defs))
	for n := range s.defs {
		names = append(names, n)
	}
	s.mu.Unlock()
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "sortie-loop: waking")
	if err := s.startAll(names); err != nil {
		fmt.Fprintln(os.Stderr, "sortie-loop:", err)
		s.failed = true
		return 1, true
	}
	s.republish()
	return 0, false
}

// requestWake asks the supervisor of root to wake. The supervisor
// consumes the file on its next tick.
func requestWake(root string) error {
	path, err := cleanupFilePath(root, wakeFileSuffix)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, nil)
}
