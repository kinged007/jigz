package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/store"
)

func TestIssueRestartClearsParksOnTheSharedPath(t *testing.T) {
	dir := t.TempDir()
	stDir := t.TempDir()
	st, err := store.Open(stDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ParkIssue(store.IssuePark{Project: "repo", Workflow: "build", IssueID: "21", Class: "c", TS: "t"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// repoName falls back to the directory base without a git remote.
	out, err := issueRestart(stDir, []string{"restart", "21", root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "park cleared") {
		t.Errorf("output = %q, want the park cleared", out)
	}
	// Running it twice does not double-claim: the second run is a no-op.
	out, err = issueRestart(stDir, []string{"restart", "21", root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already clear") {
		t.Errorf("second output = %q, want already clear", out)
	}
	// A failure after the restart parks again on the same path.
	st2, err := store.Open(stDir)
	if err != nil {
		t.Fatal(err)
	}
	parks, _ := st2.ActiveParks()
	st2.Close()
	if len(parks) != 0 {
		t.Errorf("active parks = %+v, want none after the restart", parks)
	}
}

func TestIssueRestartRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"restart"},
		{"drop", "21"},
		{"restart", "21", "--bogus"},
	} {
		if _, err := issueRestart(t.TempDir(), args); err == nil {
			t.Errorf("issueRestart(%v) succeeded, want a usage error", args)
		}
	}
}
