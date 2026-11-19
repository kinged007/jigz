package main

import (
	"os"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/gen"
)

const standbyPoolConfig = `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  reviewer: { runtime: claude-code, model: claude-sonnet-5-5 }
  planner: { runtime: claude-code, model: claude-haiku-5-5 }
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
pools:
  review-pool: [reviewer, planner]
work:
  issues:
    query_filter: "label:in-progress -label:needs-human"
    rules:
      - { name: review, match: { labels: [agent:review] }, prompt: prompts/review.md, agent: review-pool }
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md, agent: builder }
`

// writeStandbyDoc writes the loop document the way the supervisor does
// when the gate picks agent.
func writeStandbyDoc(t *testing.T, root, loopName, agent string) []byte {
	t.Helper()
	s := testSupervisor(t, root)
	s.refreshWork("test")
	standby, ok := s.genLoops[loopName].ForAgent(s.agents, agent)
	if !ok {
		t.Fatalf("%s is not an agent", agent)
	}
	jigPath, err := selfExe()
	if err != nil {
		t.Fatal(err)
	}
	text, err := gen.Render(s.genProf[loopName], standby, s.agents, jigPath, defaultPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if err := gen.AtomicWrite(gen.DocPath(root, loopName), text); err != nil {
		t.Fatal(err)
	}
	return text
}

func promoteAll(t *testing.T, root string) genSummary {
	t.Helper()
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := promoteDocs(root, docs)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// TestStandbyDocIsCurrent: a loop document rendered for a standby agent
// of a pool passes validate and is left alone by generation.
func TestStandbyDocIsCurrent(t *testing.T) {
	root := writeGenConfig(t, standbyPoolConfig)
	promoteAll(t, root)
	text := writeStandbyDoc(t, root, "issues-reviewer", "planner")
	path := gen.DocPath(root, "issues-reviewer")
	os.Remove(path + ".bak")

	reports, err := driftReports(root)
	if err != nil {
		t.Fatal(err)
	}
	if reports["issues-reviewer"] != "" {
		t.Errorf("standby render reports drift: %q", reports["issues-reviewer"])
	}
	for _, c := range generationChecks(root) {
		if !c.OK {
			t.Errorf("validate check failed: %+v", c)
		}
	}

	sum := promoteAll(t, root)
	if len(sum.Drifted) != 0 || len(sum.Generated) != 0 {
		t.Errorf("generation rewrote a current document: %+v", sum)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != string(text) {
		t.Error("standby document was overwritten")
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("generation kept a .bak of the standby document")
	}
}

// TestNoCandidateDocIsDrift: a document matching none of the candidate
// renders is drift and is regenerated.
func TestNoCandidateDocIsDrift(t *testing.T) {
	root := writeGenConfig(t, standbyPoolConfig)
	promoteAll(t, root)
	text := writeStandbyDoc(t, root, "issues-reviewer", "planner")
	edited := strings.Replace(string(text), "agent-exec planner", "agent-exec someone-else", 1)
	if edited == string(text) {
		t.Fatal("test edit did not apply")
	}
	if err := os.WriteFile(gen.DocPath(root, "issues-reviewer"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	reports, err := driftReports(root)
	if err != nil {
		t.Fatal(err)
	}
	if reports["issues-reviewer"] == "" {
		t.Error("edited document reports clean")
	}
	if sum := promoteAll(t, root); len(sum.Drifted) != 1 {
		t.Errorf("drifted = %v, want issues-reviewer", sum.Drifted)
	}
}
