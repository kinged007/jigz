package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTrackerReplaysRecordedRuns feeds the recorded engine logs (one per
// supported release, section 7.4) through the live tracker and asserts
// the session lifecycle they describe: two concurrent sessions, a
// failed end with a retry start at attempt 2, a turn, normal ends and
// the final idle tick.
func TestTrackerReplaysRecordedRuns(t *testing.T) {
	entries, err := filepath.Glob("testdata/enginelog/*.log")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no engine log fixtures: %v", err)
	}
	for _, path := range entries {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		tr := newLiveTracker(nil, nil)
		var sawConcurrent, sawRetry, sawFail, sawExit, sawTurn, sawIdle bool
		var dispatchModel, dispatchRule string
		for _, line := range lines {
			tr.line(line)
			switch {
			case strings.Contains(line, "running=2 retrying=0"):
				s, known := tr.snapshot()
				if len(s) != 2 || !known {
					t.Errorf("%s: concurrent tick: %+v known %v, want 2 known", path, s, known)
				}
				sawConcurrent = true
			case strings.Contains(line, "worker run failed"):
				sawFail = true
			case strings.Contains(line, "retried issue dispatched"):
				s, _ := tr.snapshot()
				found := false
				for _, x := range s {
					if x.Attempt == 2 {
						found = true
					}
				}
				if !found {
					t.Errorf("%s: retry start: %+v, want an attempt 2 session", path, s)
				}
				sawRetry = true
			case strings.Contains(line, `"turn completed"`):
				sawTurn = true
			case strings.Contains(line, "worker exiting"):
				sawExit = true
			case strings.Contains(line, "running=0 retrying=0"):
				sawIdle = true
			case strings.Contains(line, "issue dispatched") && !strings.Contains(line, "retried"):
				for _, kv := range strings.Fields(line) {
					if v, ok := strings.CutPrefix(kv, "model="); ok {
						dispatchModel = v
					}
					if v, ok := strings.CutPrefix(kv, "rule_name="); ok {
						dispatchRule = v
					}
				}
			}
		}
		s, known := tr.snapshot()
		if len(s) != 0 || !known {
			t.Errorf("%s: final: %+v known %v, want none known", path, s, known)
		}
		for _, seen := range []struct {
			name string
			ok   bool
		}{
			{"concurrent sessions", sawConcurrent},
			{"failed end", sawFail},
			{"retry start", sawRetry},
			{"turn", sawTurn},
			{"normal end", sawExit},
			{"idle tick", sawIdle},
		} {
			if !seen.ok {
				t.Errorf("%s: no %s in the fixture", path, seen.name)
			}
		}
		if dispatchModel == "" || dispatchRule == "" {
			t.Errorf("%s: dispatch lines lack model/rule: model=%q rule=%q", path, dispatchModel, dispatchRule)
		}
	}
}
