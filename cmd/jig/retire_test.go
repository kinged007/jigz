package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/gen"
)

const retireBase = `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
  light: { runtime: claude-code, model: claude-haiku-5-5 }
`

const retireSplitWork = `work:
  issues:
    query_filter: "label:agent:build,agent:docs,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md, agent: builder }
      - { name: docs, match: { labels: [agent:docs] }, prompt: prompts/docs.md, agent: light }
`

func writeLoopDoc(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".jigz", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "WORKFLOW."+name+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSyncWorkflowsSkipsShippedFilesOfManagedEntries(t *testing.T) {
	root := writeGenConfig(t, retireBase+retireSplitWork)
	syncWorkflows(root)
	dir := filepath.Join(root, ".jigz", "workflows")
	if exists(filepath.Join(dir, "WORKFLOW.issues.md")) {
		t.Error("shipped WORKFLOW.issues.md installed for a managed entry")
	}
	if !exists(filepath.Join(dir, "WORKFLOW.prs.md")) {
		t.Error("shipped WORKFLOW.prs.md not installed for a slug without an entry")
	}
}

func TestSyncWorkflowsJSONSkipsShippedFilesOfManagedEntries(t *testing.T) {
	root := writeGenConfig(t, retireBase+retireSplitWork)
	res := &setupResult{}
	syncWorkflowsJSON(root, res)
	dir := filepath.Join(root, ".jigz", "workflows")
	if exists(filepath.Join(dir, "WORKFLOW.issues.md")) {
		t.Error("shipped WORKFLOW.issues.md installed for a managed entry")
	}
	if !exists(filepath.Join(dir, "WORKFLOW.prs.md")) {
		t.Error("shipped WORKFLOW.prs.md not installed for a slug without an entry")
	}
}

func TestSyncWorkflowsInstallsShippedFilesForUnmanagedEntries(t *testing.T) {
	root := writeGenConfig(t, retireBase+strings.Replace(retireSplitWork, "  issues:\n", "  issues:\n    managed: false\n", 1))
	syncWorkflows(root)
	if !exists(filepath.Join(root, ".jigz", "workflows", "WORKFLOW.issues.md")) {
		t.Error("shipped WORKFLOW.issues.md not installed for an unmanaged entry")
	}
}

func TestSyncWorkflowsWithBrokenConfigInstallsShippedFiles(t *testing.T) {
	root := writeGenConfig(t, "not: [valid\n")
	syncWorkflows(root)
	if !exists(filepath.Join(root, ".jigz", "workflows", "WORKFLOW.issues.md")) {
		t.Error("shipped WORKFLOW.issues.md not installed without a loadable config")
	}
}

func TestRetireStaleLoops(t *testing.T) {
	root := writeGenConfig(t, retireBase+retireSplitWork)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	shipped, err := embeddedWorkflows.ReadFile("workflows/WORKFLOW.issues.md")
	if err != nil {
		t.Fatal(err)
	}
	unedited := writeLoopDoc(t, root, "issues", string(shipped))
	generatedOld := writeLoopDoc(t, root, "issues-gone", gen.MarkerPrefix+" digest\n")
	handWritten := writeLoopDoc(t, root, "issues-mine", "---\n# mine\n")
	edited := writeLoopDoc(t, root, "issues-edited", string(shipped)+"\n# tweak\n")
	other := writeLoopDoc(t, root, "triage", "---\n# triage\n")

	removed, notes, err := retireStaleLoops(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "issues-gone,issues" {
		t.Errorf("removed = %v", removed)
	}
	if exists(unedited) || exists(generatedOld) {
		t.Error("retired documents are still on disk")
	}
	for _, p := range []string{handWritten, edited, other} {
		if !exists(p) {
			t.Errorf("%s was removed", p)
		}
	}
	if len(notes) != 2 || !strings.Contains(notes[0]+notes[1], handWritten) || !strings.Contains(notes[0]+notes[1], edited) {
		t.Errorf("notes = %v", notes)
	}
	for _, d := range docs {
		if !exists(gen.DocPath(root, d.Loop)) {
			t.Errorf("generated %s was removed", d.Loop)
		}
	}
}

func TestRetireKeepsDocsOfOtherConfiguredSlugs(t *testing.T) {
	root := writeGenConfig(t, retireBase+`work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
  issues-fast:
    query_filter: "label:agent:fast,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: fast, match: { labels: [agent:fast] }, prompt: prompts/build.md }
`)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	removed, notes, err := retireStaleLoops(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 || len(notes) != 0 {
		t.Errorf("removed %v, notes %v", removed, notes)
	}
	for _, name := range []string{"issues", "issues-fast"} {
		if !exists(gen.DocPath(root, name)) {
			t.Errorf("%s was removed", name)
		}
	}
}

func TestRetireLeavesUnmanagedEntriesAlone(t *testing.T) {
	root := writeGenConfig(t, retireBase+strings.Replace(retireSplitWork, "  issues:\n", "  issues:\n    managed: false\n", 1))
	path := writeLoopDoc(t, root, "issues-old", gen.MarkerPrefix+" digest\n")
	removed, notes, err := retireStaleLoops(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 || len(notes) != 0 || !exists(path) {
		t.Errorf("removed %v, notes %v, kept %v", removed, notes, exists(path))
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c := loopsCheck(root, cfg); !c.OK {
		t.Errorf("unmanaged entry failed the loops check: %+v", c)
	}
}

func TestLoopsCheckFailsOnDuplicateLoopDocument(t *testing.T) {
	root := writeGenConfig(t, retireBase+retireSplitWork)
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c := loopsCheck(root, cfg); !c.OK {
		t.Fatalf("generated documents failed: %+v", c)
	}
	writeLoopDoc(t, root, "issues", "---\n# shipped\n")
	c := loopsCheck(root, cfg)
	if c.OK || !strings.Contains(c.Error, "WORKFLOW.issues.md") {
		t.Errorf("duplicate loop passed: %+v", c)
	}
}

func TestGenerateAllRetiresAndReports(t *testing.T) {
	t.Setenv("JIG_ENGINE_BIN", testEngineBin(t))
	root := writeGenConfig(t, retireBase+retireSplitWork)
	path := writeLoopDoc(t, root, "issues-gone", gen.MarkerPrefix+" digest\n")
	sum, err := generateAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sum.Retired, ",") != "issues-gone" || exists(path) {
		t.Errorf("retired = %v, kept = %v", sum.Retired, exists(path))
	}
	out := captureStdout(t, func() { reportGeneration(root, sum) })
	if !strings.Contains(out, "removed "+path) {
		t.Errorf("report missing removal:\n%s", out)
	}
}
