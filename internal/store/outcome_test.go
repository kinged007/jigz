package store

import "testing"

// Each failure appears as the raw text stored in run history and as the
// sentence the engine API serves for it. The wording is the same at engine
// v1.28.1 and v1.29.0.
func TestClassifyOutcome(t *testing.T) {
	for _, c := range []struct {
		name, status, err, class string
	}{
		{"succeeded", "succeeded", "", OutcomeSucceeded},
		{"succeeded ignores text", "succeeded", "agent turn 1: boom", OutcomeSucceeded},
		{"cancelled status", "cancelled", "", OutcomeCancelled},
		{"cancelled status ignores text", "cancelled", "agent turn 1: agent: turn_timeout: x", OutcomeCancelled},
		{"budget stopped status", "budget_stopped", "", OutcomeCancelled},
		{"ci failed status", "ci_failed", "", OutcomeFailedAgent},
		{"needs person status", "needs_person", "", OutcomeFailedAgent},
		{"failed without text", "failed", "", OutcomeFailedAgent},
		{"unrecognised text", "failed", "something new", OutcomeFailedAgent},

		{"workspace raw", "failed", "workspace preparation: no space left on device", OutcomeFailedWorkspace},
		{"workspace hook raw", "failed", "workspace preparation: hook run: exit_code=1: exit status 1", OutcomeFailedWorkspace},
		{"workspace hook timeout raw", "failed", "workspace preparation: hook timed out after 60000ms", OutcomeFailedWorkspace},
		{"workspace deadline raw", "failed", "workspace preparation: context deadline exceeded", OutcomeFailedWorkspace},
		{"mcp config raw", "failed", "mcp config generation: write file: permission denied", OutcomeFailedWorkspace},
		{"workspace sentence", "failed", "Sortie hit an error while preparing the workspace: no space left on device.", OutcomeFailedWorkspace},
		{"workspace timeout sentence", "failed", "Sortie gave up on preparing the workspace because it took too long.", OutcomeFailedWorkspace},
		{"mcp config sentence", "failed", "Sortie hit an error while setting up the agent's tool configuration: permission denied.", OutcomeFailedWorkspace},
		{"workspace cancelled raw", "failed", "workspace preparation: context canceled", OutcomeCancelled},
		{"workspace cancelled sentence", "failed", "Sortie stopped the run while preparing the workspace.", OutcomeCancelled},

		{"handoff absence raw", "failed", "handoff withheld: absence of work observed under observed policy", OutcomeHandoffWithheld},
		{"handoff undetermined raw", "failed", "handoff withheld: evidence not determinable", OutcomeHandoffWithheld},
		{"handoff other raw", "failed", "handoff withheld: policy refused", OutcomeHandoffWithheld},
		{"handoff absence sentence", "failed", "The agent finished without changing anything, so Sortie did not hand the issue off.", OutcomeHandoffWithheld},
		{"handoff undetermined sentence", "failed", "Sortie could not tell whether the agent changed anything, so it did not hand the issue off.", OutcomeHandoffWithheld},
		{"handoff other sentence", "failed", "Sortie did not hand the issue off: policy refused.", OutcomeHandoffWithheld},

		{"turn timeout raw", "failed", "agent turn 3: agent: turn_timeout: turn timed out", OutcomeTimeout},
		{"turn timeout sentence", "failed", "The agent did not finish turn 3 within the turn time limit, so Sortie stopped it.", OutcomeTimeout},
		{"response timeout raw", "failed", "agent turn 1: agent: response_timeout: no response", OutcomeTimeout},
		{"response timeout sentence", "failed", "The agent did not respond in time: no response.", OutcomeTimeout},
		{"handshake deadline raw", "failed", "agent session start: agent: response_error: handshake failed: initialize: context deadline exceeded", OutcomeTimeout},
		{"deadline raw", "failed", "context deadline exceeded", OutcomeTimeout},
		{"deadline sentence", "failed", "The run ran out of time.", OutcomeTimeout},
		{"stall raw", "failed", "agent turn 2: session stalled for 5m", OutcomeTimeout},
		{"installed is no stall", "failed", "agent turn 2: agent: response_error: package installed twice", OutcomeFailedAgent},

		{"cancelled raw", "failed", "context canceled", OutcomeCancelled},
		{"cancelled sentence", "failed", "Sortie stopped the run before it finished.", OutcomeCancelled},
		{"turn cancelled raw", "failed", "agent turn 1: agent: turn_cancelled: turn cancelled", OutcomeCancelled},
		{"turn cancelled ended raw", "failed", "agent turn 1 ended: turn_cancelled", OutcomeCancelled},
		{"turn cancelled sentence", "failed", "Sortie stopped the session before the agent finished turn 1.", OutcomeCancelled},
		{"start cancelled sentence", "failed", "Sortie stopped the session before the agent finished starting.", OutcomeCancelled},

		{"turn error raw", "failed", "agent turn 2: agent: response_error: boom", OutcomeFailedAgent},
		{"turn error sentence", "failed", "The agent returned an error: boom.", OutcomeFailedAgent},
		{"session start raw", "failed", "agent session start: agent: response_error: handshake failed: bad version", OutcomeFailedAgent},
		{"agent other sentence", "failed", "The agent reported an error: boom.", OutcomeFailedAgent},
		{"port exit raw", "failed", "agent turn 1: agent: port_exit: runtime exited before responding: exit status 1: boom", OutcomeFailedAgent},
		{"port exit sentence", "failed", "The agent process ended unexpectedly: boom.", OutcomeFailedAgent},
		{"credential raw", "failed", "agent session start: agent: credential_unverified: the agent runtime did not complete a credential verification request: boom", OutcomeFailedAgent},
		{"credential sentence", "failed", "The agent could not confirm its credential before the first turn: boom.", OutcomeFailedAgent},
		{"token limit raw", "failed", "agent turn 2: agent: turn_token_limit: token limit reached", OutcomeFailedAgent},
		{"token limit sentence", "failed", "The agent ended turn 2 because a token limit was reached: token limit reached.", OutcomeFailedAgent},
		{"request limit raw", "failed", "agent turn 2: agent: turn_request_limit: request limit reached", OutcomeFailedAgent},
		{"request limit sentence", "failed", "The agent ended turn 2 because it used up its request limit.", OutcomeFailedAgent},

		{"usage limit turn raw", "failed", "agent turn 2: agent: turn_failed: Claude AI usage limit reached|1760000000", OutcomeFailedLimit},
		{"usage limit turn sentence", "failed", "The agent reported that turn 2 failed: Claude AI usage limit reached|1760000000.", OutcomeFailedLimit},
		{"usage limit port exit raw", "failed", "agent turn 1: agent: port_exit: runtime exited before responding: exit status 1: sortie-loop agent-exec: work hit a usage limit; exhausted until 2026-01-02T03:04:05Z", OutcomeFailedLimit},
		{"usage limit port exit sentence", "failed", "The agent process ended unexpectedly: sortie-loop agent-exec: work hit a usage limit; exhausted until 2026-01-02T03:04:05Z.", OutcomeFailedLimit},
		{"usage limit credential raw", "failed", "agent session start: agent: credential_unverified: the agent runtime refused its credential: out of credits", OutcomeFailedLimit},
		{"usage limit credential sentence", "failed", "The agent could not confirm its credential before the first turn: the agent runtime refused its credential: out of credits.", OutcomeFailedLimit},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyOutcome(c.status, c.err)
			if got.Class != c.class || got.Text != outcomeLabels[c.class] {
				t.Errorf("ClassifyOutcome(%q, %q) = %+v, want class %q", c.status, c.err, got, c.class)
			}
		})
	}
}
