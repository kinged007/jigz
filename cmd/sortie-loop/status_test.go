package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func startedSup(t *testing.T, script string) (*supervisor, string) {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".sortie", "config.yaml"), "repo: owner/name\n")
	mustWrite(t, filepath.Join(root, ".sortie", "workflows", "WORKFLOW.plan.md"),
		"---\ntracker:\n  kind: github-pr\n---\nbody\n")
	sup := newTestSup(t, root, "plan")
	sup.ports["plan"] = 0
	sup.bin = writeScript(t, root, "sortie", script)
	if err := sup.startOne("plan"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.shutdown)
	return sup, root
}

func readStatus(t *testing.T) statusDoc {
	t.Helper()
	path, err := statusPath("owner/name")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc statusDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestStatusAfterStart(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	doc := readStatus(t)
	if doc.SchemaVersion != 1 || doc.State != "running" || doc.PID != os.Getpid() ||
		doc.Repo != root || doc.RepoName != "owner/name" || doc.EngineVersion != engineVersion() ||
		doc.Version != version || doc.StartedAt.IsZero() {
		t.Errorf("unexpected top-level status: %+v", doc)
	}
	if len(doc.Workflows) != 1 {
		t.Fatalf("got %d workflows, want 1", len(doc.Workflows))
	}
	w := doc.Workflows[0]
	if w.Name != "plan" || w.WorkflowFile != "WORKFLOW.plan.md" || w.LoopType != "prs" ||
		w.State != "running" || w.RestartCount != 0 || w.LastExitCode != nil ||
		len(w.ConfigHash) != 64 || w.PID == 0 {
		t.Errorf("unexpected workflow status: %+v", w)
	}
}

func TestStatusAfterRecycle(t *testing.T) {
	sup, _ := startedSup(t, "while true; do sleep 1; done\n")
	before := readStatus(t).Workflows[0].ConfigHash
	sup.stopAll(false, true)
	select {
	case ev := <-sup.events:
		if _, done := sup.onExit(ev); done {
			t.Fatal("a recycle ended the run")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not stop")
	}
	w := readStatus(t).Workflows[0]
	if w.RestartCount != 1 || w.ConfigHash != before || w.State != "running" {
		t.Errorf("after recycle got restart_count=%d state=%s hash stable=%v",
			w.RestartCount, w.State, w.ConfigHash == before)
	}
}

func TestStatusAfterUnrequestedExit(t *testing.T) {
	sup, _ := startedSup(t, "exit 3\n")
	var ev exitEvent
	select {
	case ev = <-sup.events:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not exit")
	}
	if _, done := sup.onExit(ev); !done {
		t.Fatal("an unrequested exit did not end the run")
	}
	sup.shutdown()
	sup.closeStatus()
	doc := readStatus(t)
	w := doc.Workflows[0]
	if doc.State != "exited" || w.State != "exited" || w.LastExitCode == nil || *w.LastExitCode != 3 {
		t.Errorf("unexpected final status: %+v", doc)
	}
}

func TestCleanExitRemovesStatus(t *testing.T) {
	sup, _ := startedSup(t, "while true; do sleep 1; done\n")
	sup.shutdown()
	sup.closeStatus()
	path, _ := statusPath("owner/name")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("status file remains after a clean exit: %v", err)
	}
}

func TestAtomicWriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "a.json")
	for _, body := range []string{"one", "two"} {
		if err := writeFileAtomic(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "a.json" {
		t.Errorf("directory holds %d entries, want only a.json", len(entries))
	}
	if got, _ := os.ReadFile(path); string(got) != "two" {
		t.Errorf("file holds %q, want two", got)
	}
}

func TestLoopTypeOf(t *testing.T) {
	if got := loopTypeOf([]byte("tracker:\n  kind: github\n")); got != "issues" {
		t.Errorf("got %s, want issues", got)
	}
	if got := loopTypeOf([]byte("tracker:\n  kind: github-pr\n")); got != "prs" {
		t.Errorf("got %s, want prs", got)
	}
}
