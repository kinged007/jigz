package plan

import (
	"os"
	"path/filepath"
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

func TestValidateFileReportsADisabledWorkflowWithoutRunningTheEngine(t *testing.T) {
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	bin := filepath.Join(dir, "sortie")
	body := `#!/bin/sh
case "$1" in
-dumpversion) echo 1.29.0 ;;
validate)
  case "$*" in *probe*|*WORKFLOW.md*) cat <<'JSON'
{"valid":false,"errors":[{"check":"tracker_adapter","message":"registered: [github]"},{"check":"agent_adapter","message":"registered: [claude-code, codex]"}],"warnings":[]}
JSON
  exit 1;; esac
  echo ran >> ` + ran + `
  echo '{"valid":true,"errors":[],"warnings":[]}' ;;
esac
`
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	prs := filepath.Join(dir, "WORKFLOW.prs.md")
	issues := filepath.Join(dir, "WORKFLOW.issues.md")
	os.WriteFile(prs, []byte(flow("github-pr", "claude-code")), 0o644)
	os.WriteFile(issues, []byte(flow("github", "claude-code")), 0o644)

	p := Plan{}
	res, err := p.validateFile(bin, nil, prs)
	if err != nil || !res.Valid || len(res.Errors) != 0 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, "PR workflow is off") {
		t.Fatalf("PR workflow: %+v, %v", res, err)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the engine validated a workflow it cannot run")
	}
	res, err = p.validateFile(bin, nil, issues)
	if err != nil || !res.Valid || len(res.Warnings) != 0 {
		t.Fatalf("issue workflow: %+v, %v", res, err)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatal("the issue workflow was not validated by the engine")
	}
}
