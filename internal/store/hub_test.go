package store

import (
	"strings"
	"testing"
)

func hubRun(project, workflow string, id int64) Run {
	return Run{Project: project, Workflow: workflow, ID: id, IssueID: "i", DisplayIdentifier: project + "#1",
		Status: "succeeded", StartedAt: "2026-10-24T10:00:00Z", CompletedAt: "2026-10-24T10:05:00Z", Attempt: 1, AgentAdapter: "mock"}
}

func hubIDs(t *testing.T, s *Store, limit int) string {
	t.Helper()
	runs, err := s.HubRuns(limit)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.Workflow+":"+string(rune('0'+r.ID)))
	}
	return strings.Join(ids, ",")
}

func TestHubRunsArePastTheCursorOfTheirWorkflow(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddRuns([]Run{hubRun("o/a", "prs", 1), hubRun("o/a", "issues", 2), hubRun("o/a", "issues", 1), hubRun("o/a", "issues", 3)}); err != nil {
		t.Fatal(err)
	}
	if got := hubIDs(t, s, 100); got != "issues:1,issues:2,issues:3,prs:1" {
		t.Errorf("pending = %s, want every run ordered by workflow and id", got)
	}
	if got := hubIDs(t, s, 2); got != "issues:1,issues:2" {
		t.Errorf("limited = %s, want the oldest two of the first workflow", got)
	}
	if err := s.SetHubCursor("o/a", "issues", 2); err != nil {
		t.Fatal(err)
	}
	if got := hubIDs(t, s, 100); got != "issues:3,prs:1" {
		t.Errorf("pending after cursor 2 = %s, want issues:3,prs:1", got)
	}
	if err := s.SetHubCursor("o/a", "issues", 1); err != nil {
		t.Fatal(err)
	}
	if got := hubIDs(t, s, 100); got != "issues:3,prs:1" {
		t.Errorf("pending after a lower cursor = %s, want the cursor kept at 2", got)
	}
}

func TestPreviousVersionMigratesToHubCursor(t *testing.T) {
	idx := -1
	for i, m := range migrations {
		if strings.Contains(m, "CREATE TABLE hub_cursor") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no hub_cursor migration")
	}
	dir := t.TempDir()
	saved := migrations
	migrations = saved[:idx]
	old, err := Open(dir)
	migrations = saved
	if err != nil {
		t.Fatal(err)
	}
	if err := old.AddRuns([]Run{hubRun("o/a", "issues", 1)}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.Version(); err != nil || v != len(migrations) {
		t.Fatalf("version = %d, %v, want %d", v, err, len(migrations))
	}
	if got := hubIDs(t, s, 100); got != "issues:1" {
		t.Errorf("pending after migration = %s, want the stored run", got)
	}
}
