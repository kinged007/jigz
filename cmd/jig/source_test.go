package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/source"
)

func TestNoSyncWorkflowsOnHubRoot(t *testing.T) {
	h, err := source.NewHub(t.TempDir(), "runner:ci-1", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	wf := "---\ntracker:\n  kind: github\n---\nbody\n"
	cfg := hubproto.ConfigResponse{
		Member:   hubproto.Member{Login: "octo"},
		Projects: []hubproto.ConfigProject{{Provider: "github", Repo: "acme/widgets", Workflows: []string{"issues"}}},
		Files:    []hubproto.ConfigFile{{Path: "WORKFLOW.issues.md", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(wf))), Content: wf}},
		Labels:   []hubproto.Label{},
	}
	if _, err := h.Sync(cfg); err != nil {
		t.Fatal(err)
	}
	loops, err := h.Loops(context.Background())
	if err != nil || len(loops) != 1 {
		t.Fatalf("Loops = %v, %v", loops, err)
	}
	root := loops[0].Root
	if got := loopNames(discoverWorkflows(root)); got != "issues" {
		t.Errorf("discoverWorkflows = %s, want issues only", got)
	}
	workflowPath(root, "WORKFLOW.issues.md")
	for _, shipped := range []string{"WORKFLOW.prs.md", "prompts/plan.md"} {
		if _, err := os.Stat(filepath.Join(root, ".jigz", "workflows", filepath.FromSlash(shipped))); !os.IsNotExist(err) {
			t.Errorf("shipped %s installed into a hub root: %v", shipped, err)
		}
	}
	if got := readString(t, filepath.Join(root, ".jigz", "workflows", "WORKFLOW.issues.md")); got != wf {
		t.Errorf("hub workflow changed to %q", got)
	}
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
