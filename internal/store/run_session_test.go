package store

import (
	"strings"
	"testing"
)

func TestFreshAndV5DatabasesMigrateToV6KeepingRows(t *testing.T) {
	fresh := open(t)
	if v, _ := fresh.Version(); v != len(migrations) {
		t.Fatalf("fresh version = %d of %d", v, len(migrations))
	}

	dir := t.TempDir()
	saved := migrations
	migrations = saved[:5]
	old, err := Open(dir)
	migrations = saved
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO runs_cache (project, workflow, run_id, issue_id, display_identifier,
		status, started_at, completed_at, attempt, turns_completed, agent_adapter, input_tokens, output_tokens,
		total_tokens, cache_read_tokens, tokens_measured, workspace) VALUES ('o/a','build',7,'7','o/a#7','failed',
		'2026-10-01T00:00:00Z','2026-10-01T00:01:00Z',2,3,'pi',1,2,3,0,1,'/w')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.Version(); v != len(migrations) {
		t.Fatalf("version = %d, want %d", v, len(migrations))
	}
	r, err := s.Run("o/a", "build", 7)
	if err != nil || r.Attempt != 2 || r.TotalTokens != 3 {
		t.Fatalf("migrated run = %+v, %v, want the old row", r, err)
	}
	if r.WorkflowFile != "" || r.SessionID != "" {
		t.Errorf("new fields = %+v, want empty defaults", r)
	}
}

func TestSessionFieldsAreStoredAndNotBlankedByARecopy(t *testing.T) {
	s := open(t)
	r := run("build", 1, "2026-10-01T00:00:00Z", "succeeded")
	r.WorkflowFile, r.SessionID = "WORKFLOW.build.md", "s1"
	if err := s.AddRuns([]Run{r}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRuns([]Run{run("build", 1, "2026-10-01T00:00:00Z", "succeeded")}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run("o/a", "build", 1)
	if got.WorkflowFile != "WORKFLOW.build.md" || got.SessionID != "s1" {
		t.Errorf("run = %+v, want the session fields kept", got)
	}
}

func TestMigrationDropsTheColumnsWithoutAnAPISource(t *testing.T) {
	s := open(t)
	for _, col := range []string{"workspace", "template_id", "api_requests", "handoff_reset", "review_metadata"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('runs_cache') WHERE name = ?`, col).Scan(&n); err != nil || n != 0 {
			t.Errorf("runs_cache still has %s (%d, %v)", col, n, err)
		}
	}
}

func TestMigrationRemovesHandoffResetEvents(t *testing.T) {
	dir := t.TempDir()
	saved := migrations
	// locate the handoff_reset deletion by content, not position
	idx := -1
	for i, m := range saved {
		if strings.Contains(m, "DELETE FROM events WHERE kind IN ('handoff_reset'") {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("handoff_reset deletion migration not found")
	}
	migrations = saved[:idx]
	old, err := Open(dir)
	migrations = saved
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO events (ts, project, workflow, issue_id, kind) VALUES
		('t','o/a','build','1','handoff_reset'), ('t','o/a','build','1','handoff_reset_cleared'), ('t','o/a','build','1','parked')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ev, err := s.IssueEvents("o/a", "build", "1")
	if err != nil || len(ev) != 1 || ev[0].Kind != "parked" {
		t.Errorf("events = %+v, %v, want only parked", ev, err)
	}
}
