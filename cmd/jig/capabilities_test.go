package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/plan"
)

const (
	forkTrackers = "file, github, github-pr, jira"
	forkAgents   = "claude-code, codex, mock, pi"
	upTrackers   = "file, github, jira"
	upAgents     = "claude-code, codex, mock"
)

func testGate(version, trackers, agents string) engine.Gate {
	c := engine.Capabilities{Bin: "/bin/sortie", Version: version, Trackers: strings.Split(trackers, ", "), Agents: strings.Split(agents, ", ")}
	return engine.NewGate(c, engine.Spec{Repo: "sortie-ai/sortie", Tag: "v" + version})
}

func flow(tracker, agent string) string {
	return "---\ntracker:\n  kind: " + tracker + "\nagent:\n  kind: " + agent + "\n---\nbody\n"
}

func TestCapAlerts(t *testing.T) {
	upstream := testGate("1.29.0", upTrackers, upAgents)
	fork := testGate("1.28.1", forkTrackers, forkAgents)
	old := testGate("1.26.0", forkTrackers, forkAgents)
	unknown := engine.NewGate(engine.Capabilities{Bin: "/bin/sortie", Err: "exit status 3"}, engine.Spec{})
	issues := loopFile{"o/a", "issues", flow("github", "claude-code")}
	prs := loopFile{"o/a", "prs", flow("github-pr", "claude-code")}
	prs2 := loopFile{"o/b", "prs", flow("github-pr", "claude-code")}

	for _, c := range []struct {
		name  string
		g     engine.Gate
		files []loopFile
		env   []string
		pool  []string
		want  []string
	}{
		{"fork runs everything", fork, []loopFile{issues, prs}, nil, []string{"pi"}, nil},
		{"upstream", upstream, []loopFile{issues, prs, prs2}, nil, nil, []string{
			"warn|Feature off|PR workflow is off: engine sortie-ai/sortie v1.29.0 has no github-pr tracker."}},
		{"upstream with a pi pool entry", upstream, nil, nil, []string{"pi", "claude-code"}, []string{
			"warn|Pool|Pool entries with runtime pi are unavailable: engine sortie-ai/sortie v1.29.0 has no pi agent."}},
		{"explicit kind the engine lacks", upstream, []loopFile{{"o/a", "issues", flow("github", "gemini")}}, nil, nil, []string{
			`err|Agent kind|o/a/issues: Agent kind "gemini" is not available: engine sortie-ai/sortie v1.29.0 registers claude-code, codex, mock. The loop is not started.`}},
		{"explicit pi is an error with no fallback", upstream, []loopFile{{"o/a", "issues", flow("github", "pi")}}, nil, nil, []string{
			`err|Agent kind|o/a/issues: Agent kind "pi" is not available: engine sortie-ai/sortie v1.29.0 registers claude-code, codex, mock. The loop is not started.`}},
		{"explicit env kind the engine lacks", upstream, []loopFile{issues}, []string{"SORTIE_AGENT_KIND=pi"}, nil, []string{
			`err|Agent kind|o/a/issues: Agent kind "pi" is not available: engine sortie-ai/sortie v1.29.0 registers claude-code, codex, mock. The loop is not started.`}},
		{"codex in the file", upstream, []loopFile{{"o/a", "issues", flow("github", "codex")}}, nil, nil, nil},
		{"old engine", old, []loopFile{issues, prs}, nil, nil, []string{
			"err|Engine|Loops are off: engine sortie-ai/sortie v1.26.0 is older than 1.27.0, the oldest the shipped workflows run on."}},
		{"unknown", unknown, []loopFile{issues, prs}, nil, []string{"pi"}, []string{
			"warn|Engine|The engine's features could not be detected, so none is disabled: exit status 3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, a := range capAlerts(c.g, c.files, c.env, c.pool) {
				got = append(got, a.Sev+"|"+a.Kind+"|"+a.Msg)
			}
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("alerts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

func TestEngineCapabilitiesRows(t *testing.T) {
	rows := engineCapabilities(testGate("1.29.0", upTrackers, upAgents))
	if len(rows) != 2 || rows[0].Value != upTrackers || rows[1].Value != upAgents {
		t.Fatalf("rows = %v", rows)
	}
	rows = engineCapabilities(engine.NewGate(engine.Capabilities{Err: "boom"}, engine.Spec{}))
	if len(rows) != 1 || !strings.Contains(rows[0].Value, "unknown: boom") {
		t.Fatalf("rows = %v", rows)
	}
}

func gatedSup(t *testing.T, g engine.Gate) (*supervisor, string) {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "repo: owner/name\n")
	mustWrite(t, filepath.Join(root, ".jigz", "workflows", "WORKFLOW.issues.md"), flow("github", "claude-code"))
	mustWrite(t, filepath.Join(root, ".jigz", "workflows", "WORKFLOW.prs.md"), flow("github-pr", "claude-code"))
	sup := newTestSup(t, root, "issues", "prs")
	sup.bin = writeScript(t, root, "sortie", `echo "$SORTIE_AGENT_KIND $SORTIE_AGENT_COMMAND" >> "`+root+`/agent.log"`+"\nwhile true; do sleep 1; done\n")
	sup.setGate(g)
	t.Cleanup(sup.shutdown)
	return sup, root
}

func TestUpstreamEngineSkipsThePRLoop(t *testing.T) {
	sup, root := gatedSup(t, testGate("1.29.0", upTrackers, upAgents))
	if err := sup.startAll([]string{"issues", "prs"}); err != nil {
		t.Fatal(err)
	}
	if n := len(sup.procs); n != 1 || sup.procs["issues"] == nil {
		t.Fatalf("running loops = %v, want only issues", sup.names())
	}
	why := sup.disabled["prs"]
	if !strings.Contains(why, "PR workflow is off") {
		t.Fatalf("disabled[prs] = %q", why)
	}
	var found bool
	for _, w := range sup.statusDoc("running").Workflows {
		if w.Name == "prs" {
			found = true
			if w.State != "disabled" || w.OfflineReason != why {
				t.Errorf("prs status = %q, offline reason %q", w.State, w.OfflineReason)
			}
		}
	}
	if !found {
		t.Error("the status document lacks the disabled loop")
	}
	// The file names claude-code, which the engine has: the loop runs
	// with the file's own agent settings and nothing is injected.
	var logged string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && logged == ""; time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(filepath.Join(root, "agent.log"))
		logged = string(b)
	}
	if strings.TrimSpace(logged) != "" {
		t.Fatalf("the issues loop ran with agent overrides %q, want the file's own settings", strings.TrimSpace(logged))
	}
}

func TestAnExplicitPiKindOnAnEngineWithoutPiDoesNotStart(t *testing.T) {
	sup, root := gatedSup(t, testGate("1.29.0", upTrackers, upAgents))
	mustWrite(t, filepath.Join(root, ".jigz", "workflows", "WORKFLOW.issues.md"), flow("github", "pi"))
	if err := sup.startAll([]string{"issues", "prs"}); err != nil {
		t.Fatal(err)
	}
	why := sup.disabled["issues"]
	if !strings.Contains(why, `Agent kind "pi" is not available`) {
		t.Fatalf("disabled[issues] = %q", why)
	}
	if len(sup.procs) != 0 {
		t.Fatalf("running %v, want none", sup.names())
	}
}

// The shipped workflows parse to the claude-code agent, the claude
// command and the claude-haiku-5-5 model in the claude-code block.
func TestShippedWorkflowsUseTheClaudeCodeDefault(t *testing.T) {
	for _, name := range []string{"WORKFLOW.issues.md", "WORKFLOW.prs.md"} {
		raw, err := embeddedWorkflows.ReadFile("workflows/" + name)
		if err != nil {
			t.Fatal(err)
		}
		wf, err := plan.ParseWorkflow(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if wf.AgentKind != "claude-code" || wf.Knobs["command"] != "claude" || wf.Model != "claude-haiku-5-5" {
			t.Errorf("%s: agent %q command %q model %q, want claude-code, claude, claude-haiku-5-5", name, wf.AgentKind, wf.Knobs["command"], wf.Model)
		}
	}
}

func TestForkEngineStartsBothLoopsWithTheDefaultAgent(t *testing.T) {
	sup, _ := gatedSup(t, testGate("1.28.1", forkTrackers, forkAgents))
	if err := sup.startAll([]string{"issues", "prs"}); err != nil {
		t.Fatal(err)
	}
	if len(sup.procs) != 2 || len(sup.disabled) != 0 {
		t.Fatalf("running %v, disabled %v", sup.names(), sup.disabled)
	}
}

func TestOldEngineStartsNoLoop(t *testing.T) {
	sup, _ := gatedSup(t, testGate("1.26.0", forkTrackers, forkAgents))
	if err := sup.startAll([]string{"issues", "prs"}); err != nil {
		t.Fatal(err)
	}
	if len(sup.procs) != 0 || len(sup.disabled) != 2 {
		t.Fatalf("running %v, disabled %v", sup.names(), sup.disabled)
	}
	if sup.engineVersion() != "1.26.0" {
		t.Errorf("status engine version = %q", sup.engineVersion())
	}
}

func TestPoolSkipsRuntimesTheEngineLacks(t *testing.T) {
	sup, _ := gatedSup(t, testGate("1.29.0", upTrackers, upAgents))
	cfg, dir := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
	mustWrite(t, filepath.Join(cfg, "jigz", "machine.yaml"),
		"pool:\n  - name: a-pi\n    runtime: pi\n  - name: b-claude\n    runtime: claude-code\n")
	sup.initMachine()
	sup.setGate(testGate("1.29.0", upTrackers, upAgents))
	env, runtime, err := sup.agentEnv()
	if err != nil || runtime != "claude-code" || !strings.Contains(strings.Join(env, " "), "SORTIE_AGENT_KIND=claude-code") {
		t.Fatalf("agentEnv = %v, %q, %v", env, runtime, err)
	}
}
