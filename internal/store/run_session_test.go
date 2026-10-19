package store

import "testing"

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
	if err != nil || r.Attempt != 2 || r.TotalTokens != 3 || r.Workspace != "/w" {
		t.Fatalf("migrated run = %+v, %v, want the old row", r, err)
	}
	if r.WorkflowFile != "" || r.SessionID != "" || r.APIRequests != nil || r.HandoffReset {
		t.Errorf("new fields = %+v, want empty defaults", r)
	}
}

func TestSessionFieldsAreStoredAndNotBlankedByARecopy(t *testing.T) {
	s := open(t)
	n := int64(0)
	r := run("build", 1, "2026-10-01T00:00:00Z", "succeeded")
	r.WorkflowFile, r.TemplateID, r.SessionID, r.APIRequests, r.HandoffReset = "WORKFLOW.build.md", "/p/build.md", "s1", &n, true
	if err := s.AddRuns([]Run{r}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRuns([]Run{run("build", 1, "2026-10-01T00:00:00Z", "succeeded")}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run("o/a", "build", 1)
	if got.WorkflowFile != "WORKFLOW.build.md" || got.TemplateID != "/p/build.md" || got.SessionID != "s1" ||
		got.APIRequests == nil || *got.APIRequests != 0 || !got.HandoffReset {
		t.Errorf("run = %+v, want the session fields kept, zero requests distinct from NULL", got)
	}
}
