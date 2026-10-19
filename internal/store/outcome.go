package store

import (
	"regexp"
	"strings"
)

// Failure classes of a run, derived from the engine's error text.
const (
	OutcomeWorkspace = "workspace"
	OutcomeHandoff   = "handoff"
	OutcomeCancelled = "cancelled"
	OutcomeAgent     = "agent"
	OutcomeTimeout   = "timeout"
	OutcomeUnknown   = "unknown"
)

// Outcome is the short reason shown for a finished run. Class is empty for
// a succeeded run.
type Outcome struct {
	Class, Text string
}

var turnPrefix = regexp.MustCompile(`^agent turn (\d+):`)

// ClassifyOutcome describes how a run ended. The engine records free text
// in the error column, so the class comes from its prefixes.
func ClassifyOutcome(status, errText string) Outcome {
	if status == "succeeded" {
		return Outcome{Text: "completed"}
	}
	e := strings.ToLower(errText)
	turn := turnPrefix.FindStringSubmatch(e)
	switch {
	case e == "":
		return Outcome{OutcomeUnknown, "no error recorded"}
	case strings.HasPrefix(e, "workspace preparation:") && strings.Contains(e, "context canceled"):
		return Outcome{OutcomeCancelled, "cancelled during workspace setup"}
	case strings.HasPrefix(e, "workspace preparation:"):
		if strings.Contains(e, "hook") {
			return Outcome{OutcomeWorkspace, "workspace hook failed"}
		}
		return Outcome{OutcomeWorkspace, "workspace setup failed"}
	case strings.HasPrefix(e, "handoff withheld:"):
		return Outcome{OutcomeHandoff, "handoff withheld"}
	case turn != nil && strings.Contains(e, "turn_cancelled"):
		return Outcome{OutcomeCancelled, "cancelled mid-turn (turn " + turn[1] + ")"}
	case strings.Contains(e, "stall"), strings.Contains(e, "timed out"), strings.Contains(e, "timeout"),
		strings.Contains(e, "deadline exceeded"):
		return Outcome{OutcomeTimeout, "timeout or stall"}
	case strings.HasPrefix(e, "agent session start:"), strings.HasPrefix(e, "agent:"), turn != nil:
		return Outcome{OutcomeAgent, "agent error"}
	}
	return Outcome{OutcomeUnknown, "unknown error"}
}
