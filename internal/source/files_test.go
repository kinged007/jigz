package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesListsInstalledWorkflowsNarrowedByOnly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".jigz", "workflows")
	os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	for _, n := range []string{"WORKFLOW.prs.md", "WORKFLOW.issues.md", "notes.md", "jigz-issues.db"} {
		os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "prompts", "plan.md"), []byte("p"), 0o644)
	synced := ""
	f := Files{Root: root, Project: "acme/widgets", Sync: func(r string) { synced = r }}
	ls, err := f.Loops(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if synced != root || len(ls) != 2 || ls[0].Workflow != "issues" || ls[1].Workflow != "prs" {
		t.Fatalf("Loops = %+v (synced %q)", ls, synced)
	}
	if ls[0].Root != root || ls[0].Project != "acme/widgets" || ls[0].Hash == "" || ls[0].Hash == ls[1].Hash {
		t.Errorf("loop = %+v", ls[0])
	}

	os.WriteFile(filepath.Join(dir, "jigz-issues.db"), []byte("new engine state"), 0o644)
	again, _ := f.Loops(context.Background())
	if again[0].Hash != ls[0].Hash {
		t.Error("engine database changed the hash")
	}
	os.WriteFile(filepath.Join(dir, "prompts", "plan.md"), []byte("edited"), 0o644)
	again, _ = f.Loops(context.Background())
	if again[0].Hash == ls[0].Hash {
		t.Error("prompt edit did not change the hash")
	}

	f.Only = "prs"
	if ls, err := f.Loops(context.Background()); err != nil || len(ls) != 1 || ls[0].Workflow != "prs" {
		t.Errorf("Only=prs: %+v, %v", ls, err)
	}
	f.Only = "triage"
	if _, err := f.Loops(context.Background()); err == nil {
		t.Error("Only naming no workflow accepted")
	}
	if _, err := (Files{Root: t.TempDir()}).Loops(context.Background()); err == nil {
		t.Error("missing workflows directory accepted")
	}
}
