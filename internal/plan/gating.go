package plan

import (
	"strings"

	"github.com/kinged007/sortie-loop/internal/engine"
)

// LoopGate is what an engine's capabilities decide for one workflow file.
type LoopGate struct {
	// Off is why the loop does not start, "" when it starts. Problem is
	// true when Off is an error: an engine too old for the workflows, or
	// an agent kind set explicitly that the engine lacks. Otherwise Off
	// names a feature the engine cannot run.
	Off     string
	Problem bool
	// Note says that the loop runs with another agent than its file names.
	Note string
	// Env sets that other agent for the engine process.
	Env []string
}

// GateLoop applies the gate to a workflow file's text. env is the
// environment the loop starts with: SORTIE_AGENT_KIND there is an explicit
// agent setting, as is any agent.kind in the file other than the shipped
// default. A file that cannot be read gives no decision, and the engine
// reports it.
func GateLoop(g engine.Gate, text string, env []string) LoopGate {
	wf, err := ParseWorkflow(text)
	if err != nil {
		return LoopGate{}
	}
	if off := g.LoopsOff(); off != "" {
		return LoopGate{Off: off, Problem: true}
	}
	if off := g.TrackerOff(wf.TrackerKind); off != "" {
		return LoopGate{Off: off}
	}
	kind, explicit := lastEnv(env, "SORTIE_AGENT_KIND"), true
	if kind == "" {
		kind = wf.AgentKind
		explicit = kind != engine.DefaultAgent
	}
	use, note, problem := g.Agent(kind, explicit)
	switch {
	case problem != "":
		return LoopGate{Off: problem, Problem: true}
	case use != kind:
		return LoopGate{Note: note, Env: []string{"SORTIE_AGENT_KIND=" + use, "SORTIE_AGENT_COMMAND=" + engine.FallbackCommand}}
	}
	return LoopGate{}
}

func lastEnv(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			val = v
		}
	}
	return val
}
