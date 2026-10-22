package store

import (
	"regexp"
	"strings"

	"github.com/kinged007/sortie-loop/internal/machine"
)

// Outcome classes of a run. The keys are the hub protocol's outcome classes.
const (
	OutcomeSucceeded       = "succeeded"
	OutcomeFailedAgent     = "failed_agent"
	OutcomeFailedWorkspace = "failed_workspace"
	OutcomeFailedLimit     = "failed_limit"
	OutcomeCancelled       = "cancelled"
	OutcomeTimeout         = "timeout"
	OutcomeHandoffWithheld = "handoff_withheld"
)

var outcomeLabels = map[string]string{
	OutcomeSucceeded:       "completed",
	OutcomeFailedAgent:     "agent error",
	OutcomeFailedWorkspace: "workspace setup failed",
	OutcomeFailedLimit:     "usage limit",
	OutcomeCancelled:       "cancelled",
	OutcomeTimeout:         "timeout",
	OutcomeHandoffWithheld: "handoff withheld",
}

// Outcome is how a run ended: its class key and the short label shown for
// it.
type Outcome struct {
	Class, Text string
}

func outcome(class string) Outcome { return Outcome{class, outcomeLabels[class]} }

var (
	workspaceStage = regexp.MustCompile(`^(workspace preparation|mcp config generation):|preparing the workspace|setting up the agent's tool configuration|hook timed out`)
	stageCanceled  = regexp.MustCompile(`context canceled$|^sortie stopped the run while `)
	sessionBudget  = regexp.MustCompile(`turn_token_limit|turn_request_limit|because a token limit was reached|used up its request limit`)
	credential     = regexp.MustCompile(`credential_unverified|could not confirm its credential`)
	cancelled      = regexp.MustCompile(`context canceled|turn_cancelled|^sortie stopped the (run|session) `)
	timedOut       = regexp.MustCompile(`timeout|timed out|deadline exceeded|\bstall(ed)?\b|ran out of time|did not respond in time|time limit`)
)

// ClassifyOutcome maps a run's status and error text to an outcome class.
// The error text is either the raw text the engine stores or the sentence
// its API serves for it; both forms of one failure get the same class.
func ClassifyOutcome(status, errText string) Outcome {
	switch status {
	case "succeeded":
		return outcome(OutcomeSucceeded)
	case "cancelled", "budget_stopped":
		return outcome(OutcomeCancelled)
	}
	e := strings.ToLower(strings.TrimSpace(errText))
	switch {
	case strings.HasPrefix(e, "handoff withheld:"), strings.Contains(e, "did not hand the issue off"):
		return outcome(OutcomeHandoffWithheld)
	case workspaceStage.MatchString(e):
		if stageCanceled.MatchString(e) {
			return outcome(OutcomeCancelled)
		}
		return outcome(OutcomeFailedWorkspace)
	case sessionBudget.MatchString(e):
		return outcome(OutcomeFailedAgent)
	case machine.BuiltinLimit.MatchString(e):
		return outcome(OutcomeFailedLimit)
	case credential.MatchString(e):
		return outcome(OutcomeFailedAgent)
	case cancelled.MatchString(e):
		return outcome(OutcomeCancelled)
	case timedOut.MatchString(e):
		return outcome(OutcomeTimeout)
	}
	return outcome(OutcomeFailedAgent)
}
