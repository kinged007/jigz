package store

import "testing"

func TestClassifyOutcome(t *testing.T) {
	for _, c := range []struct {
		name, status, err string
		want              Outcome
	}{
		{"succeeded", "succeeded", "", Outcome{"", "completed"}},
		{"succeeded ignores text", "succeeded", "agent turn 1: boom", Outcome{"", "completed"}},
		{"hook", "failed", "workspace preparation: hook run: exit_code=1: exit status 1", Outcome{OutcomeWorkspace, "workspace hook failed"}},
		{"workspace without hook", "failed", "workspace preparation: no space left on device", Outcome{OutcomeWorkspace, "workspace setup failed"}},
		{"cancelled in workspace", "cancelled", "workspace preparation: context canceled", Outcome{OutcomeCancelled, "cancelled during workspace setup"}},
		{"cancelled mid turn", "cancelled", "agent turn 1: agent: turn_cancelled: turn cancelled", Outcome{OutcomeCancelled, "cancelled mid-turn (turn 1)"}},
		{"cancelled in later turn", "cancelled", "agent turn 4: agent: turn_cancelled: turn cancelled: context canceled", Outcome{OutcomeCancelled, "cancelled mid-turn (turn 4)"}},
		{"handoff withheld", "failed", "handoff withheld: absence of work observed under observed policy", Outcome{OutcomeHandoff, "handoff withheld"}},
		{"handshake deadline", "failed", "agent session start: agent: response_error: handshake failed: initialize: context deadline exceeded", Outcome{OutcomeTimeout, "timeout or stall"}},
		{"session start error", "failed", "agent session start: agent: response_error: handshake failed: bad version", Outcome{OutcomeAgent, "agent error"}},
		{"turn error", "failed", "agent turn 2: agent: response_error: boom", Outcome{OutcomeAgent, "agent error"}},
		{"turn timeout", "failed", "agent turn 3: agent: turn_timeout: turn timed out", Outcome{OutcomeTimeout, "timeout or stall"}},
		{"stall", "failed", "stalled: no events for 300s", Outcome{OutcomeTimeout, "timeout or stall"}},
		{"agent prefix", "failed", "agent: boom", Outcome{OutcomeAgent, "agent error"}},
		{"session stalled", "failed", "agent turn 2: session stalled for 5m", Outcome{OutcomeTimeout, "timeout or stall"}},
		{"unrecognised", "failed", "something new", Outcome{OutcomeUnknown, "unknown error"}},
		{"empty on failure", "failed", "", Outcome{OutcomeUnknown, "no error recorded"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyOutcome(c.status, c.err); got != c.want {
				t.Errorf("ClassifyOutcome(%q, %q) = %+v, want %+v", c.status, c.err, got, c.want)
			}
		})
	}
}
