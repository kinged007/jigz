package engine

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// MinVersion is the oldest engine the shipped workflows run on:
	// dispatch.rules stage and next need 1.27.0.
	MinVersion = "1.27.0"
	// PRTracker is the tracker kind of the PR workflow.
	PRTracker = "github-pr"
	// DefaultAgent is the agent kind of the shipped workflows.
	DefaultAgent = "claude-code"
)

// Gate holds the features one engine can run. The zero value allows
// everything, which is also the result for an engine that could not be
// probed.
type Gate struct {
	Caps  Capabilities
	Label string
}

// Detect probes bin and returns its gate.
func Detect(bin string) Gate { return NewGate(Probe(bin), Current()) }

// NewGate is the gate for c, labelled with the configured release spec.
func NewGate(c Capabilities, spec Spec) Gate {
	label := "engine"
	if c.Known() {
		if os.Getenv("JIG_ENGINE_BIN") != "" {
			label += " v" + c.Version
		} else {
			label += " " + spec.Repo + " v" + c.Version
		}
	} else if c.Bin != "" {
		label += " " + c.Bin
	}
	return Gate{Caps: c, Label: label}
}

// Unknown is the reason the capabilities could not be read, "" when they
// were read or no engine was probed.
func (g Gate) Unknown() string {
	if g.Caps.Known() {
		return ""
	}
	return g.Caps.Err
}

// LoopsOff is why no loop may start, "" when loops may start.
func (g Gate) LoopsOff() string {
	if !g.Caps.Known() {
		return ""
	}
	cur, ok1 := parseVersion(g.Caps.Version)
	min, _ := parseVersion(MinVersion)
	if !ok1 || cur >= min {
		return ""
	}
	return fmt.Sprintf("Loops are off: %s is older than %s, the oldest the shipped workflows run on.", g.Label, MinVersion)
}

// TrackerOff is why a workflow with the tracker kind cannot run, "" when it
// can.
func (g Gate) TrackerOff(kind string) string {
	if !g.Caps.Known() || kind == "" || g.Caps.HasTracker(kind) {
		return ""
	}
	if kind == PRTracker {
		return fmt.Sprintf("PR workflow is off: %s has no %s tracker.", g.Label, PRTracker)
	}
	return fmt.Sprintf("Tracker kind %q is not available: %s registers %s.", kind, g.Label, strings.Join(g.Caps.Trackers, ", "))
}

// Agent reports why a loop cannot run with the agent kind, "" when it
// can. A kind the engine lacks is an error whatever the file or the
// environment named it: there is no fallback agent.
func (g Gate) Agent(kind string) string {
	if kind == "" || !g.Caps.Known() || g.Caps.HasAgent(kind) {
		return ""
	}
	return fmt.Sprintf("Agent kind %q is not available: %s registers %s.", kind, g.Label, strings.Join(g.Caps.Agents, ", "))
}

// RuntimeOff is why pool entries of the runtime cannot be used, "" when they
// can.
func (g Gate) RuntimeOff(runtime string) string {
	if !g.Caps.Known() || g.Caps.HasAgent(runtime) {
		return ""
	}
	return fmt.Sprintf("Pool entries with runtime %s are unavailable: %s has no %s agent.", runtime, g.Label, runtime)
}

// parseVersion reads the numeric X.Y.Z prefix of a version such as 1.27.0 or
// 1.24.0+pi.github-pr.1.
func parseVersion(v string) (n int, ok bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "+-"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return 0, false
	}
	for _, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		n = n*1000 + x
	}
	return n, true
}
