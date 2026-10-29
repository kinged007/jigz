package plan

import (
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/engine"
)

func gate(version, trackers, agents string) engine.Gate {
	c := engine.Capabilities{Bin: "/bin/sortie", Version: version, Trackers: strings.Split(trackers, ", "), Agents: strings.Split(agents, ", ")}
	return engine.NewGate(c, engine.Spec{Repo: "sortie-ai/sortie", Tag: "v" + version})
}

func flow(tracker, agent string) string {
	return "---\ntracker:\n  kind: " + tracker + "\nagent:\n  kind: " + agent + "\n---\nbody\n"
}

func TestGateLoop(t *testing.T) {
	fork := gate("1.28.1", "github, github-pr", "claude-code, pi")
	up := gate("1.29.0", "github", "claude-code, codex")
	old := gate("1.26.0", "github, github-pr", "claude-code, pi")
	for _, c := range []struct {
		name    string
		g       engine.Gate
		text    string
		env     []string
		off     string
		problem bool
	}{
		{name: "the shipped default runs as is on any engine", g: up, text: flow("github", "claude-code")},
		{name: "fork PR loop", g: fork, text: flow("github-pr", "claude-code")},
		{name: "explicit pi on an engine without pi is an error", g: up, text: flow("github", "pi"), off: `Agent kind "pi"`, problem: true},
		{name: "explicit pi on an engine with pi runs as is", g: fork, text: flow("github", "pi")},
		{name: "upstream PR loop", g: up, text: flow("github-pr", "claude-code"), off: "PR workflow is off"},
		{name: "upstream codex", g: up, text: flow("github", "codex")},
		{name: "upstream unknown agent", g: up, text: flow("github", "gemini"), off: `Agent kind "gemini"`, problem: true},
		{name: "env kind the engine lacks", g: up, text: flow("github", "claude-code"), env: []string{"SORTIE_AGENT_KIND=pi"}, off: `Agent kind "pi"`, problem: true},
		{name: "pool picks claude-code", g: up, text: flow("github", "pi"), env: []string{"SORTIE_AGENT_KIND=claude-code"}},
		{name: "old engine", g: old, text: flow("github", "claude-code"), off: "Loops are off", problem: true},
		{name: "unreadable file", g: up, text: "no front matter"},
		{name: "zero gate", g: engine.Gate{}, text: flow("github-pr", "claude-code")},
	} {
		t.Run(c.name, func(t *testing.T) {
			lg := GateLoop(c.g, c.text, c.env)
			if (c.off == "") != (lg.Off == "") || !strings.Contains(lg.Off, c.off) || lg.Problem != c.problem {
				t.Fatalf("GateLoop = %+v, want off %q problem %v", lg, c.off, c.problem)
			}
		})
	}
}
