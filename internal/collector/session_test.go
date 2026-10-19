package collector

import (
	"context"
	"testing"

	"github.com/kinged007/sortie-loop/internal/store"
)

func TestSessionAndPromptFieldsAreStored(t *testing.T) {
	path, _ := fixture(t)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	get := func(id int64) store.Run {
		r, err := st.Run("acme/app", "build", id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := get(1)
	if r.WorkflowFile != "WORKFLOW.build.md" || r.TemplateID != "/work/acme/app/.sortie/workflows/prompts/build.md" ||
		r.SessionID != "sess-0001" || r.APIRequests == nil || *r.APIRequests != 8 || r.HandoffReset {
		t.Errorf("run 1 = %+v, want file, template, session sess-0001, 8 requests, no reset", r)
	}
	if r := get(3); r.SessionID != "" || r.APIRequests != nil || r.WorkflowFile == "" {
		t.Errorf("run 3 = %+v, want no session for a run that is not the newest of its issue", r)
	}
	if r := get(5); r.SessionID != "sess-0002" {
		t.Errorf("run 5 session = %q, want sess-0002 on the newest attempt of issue 11", r.SessionID)
	}
	if r := get(7); r.SessionID != "sess-0003" || r.APIRequests != nil {
		t.Errorf("run 7 = %+v, want unmeasured requests stored as NULL", r)
	}
	if r := get(8); !r.HandoffReset {
		t.Error("run 8 handoff_reset = false, want true for the matching reset_run_id")
	}
	if r := get(9); r.HandoffReset || r.SessionID != "" {
		t.Errorf("run 9 = %+v, want no reset and no session", r)
	}
}

func TestHandoffResetOnlyForTheMatchingRun(t *testing.T) {
	path, _ := fixture(t, `INSERT INTO run_history (issue_id, identifier, attempt, agent_adapter, workspace,
		started_at, completed_at, status) VALUES ('13','13',2,'codex','','2026-10-13T17:00:00Z','2026-10-13T17:01:00Z','failed')`)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	for id, want := range map[int64]bool{8: true, 17: false} {
		if r, _ := st.Run("acme/app", "build", id); r.HandoffReset != want {
			t.Errorf("run %d handoff_reset = %v, want %v", id, r.HandoffReset, want)
		}
	}
}

func TestLoopWithoutSessionMetadataLeavesSessionFieldsEmpty(t *testing.T) {
	path, _ := fixture(t, `DROP TABLE session_metadata`)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	r, err := st.Run("acme/app", "build", 1)
	if err != nil || r.SessionID != "" || r.APIRequests != nil || r.Model != "cfg-model" || r.WorkflowFile == "" {
		t.Errorf("run 1 = %+v, %v, want configured model, no session, workflow file", r, err)
	}
}

func TestOlderSchemaWithoutNewColumnsStillCopies(t *testing.T) {
	path, _ := fixture(t,
		`ALTER TABLE session_metadata DROP COLUMN session_id`,
		`ALTER TABLE session_metadata DROP COLUMN api_requests_measured`,
		`ALTER TABLE run_history DROP COLUMN template_id`,
		`ALTER TABLE run_history DROP COLUMN review_metadata`,
		`ALTER TABLE retry_entries DROP COLUMN due_at_ms`)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	r, err := st.Run("acme/app", "build", 1)
	if err != nil || r.Model != "claude-sonnet-5-5" || r.WorkflowFile != "WORKFLOW.build.md" ||
		r.TemplateID != "" || r.SessionID != "" || r.APIRequests != nil {
		t.Fatalf("run 1 = %+v, %v, want the run copied with the missing columns empty", r, err)
	}
}

func TestRetryEventKeepsTheDueTime(t *testing.T) {
	path, db := fixture(t)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	if _, err := db.Exec(`INSERT INTO retry_entries(issue_id,identifier,attempt,due_at_ms,error)
		VALUES ('19','19',2,1791969000000,'boom')`); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	evs, err := st.IssueEvents("acme/app", "build", "19")
	if err != nil || len(evs) != 1 || evs[0].Detail != "attempt 2 due 1791969000000: boom" {
		t.Errorf("events = %+v, %v, want one retry_scheduled with the due time", evs, err)
	}
}

func TestStoredRunsGetTheirWorkflowFileBackfilled(t *testing.T) {
	path, _ := fixture(t)
	c, st := newCollector(t, path)
	var old []store.Run
	for id := int64(1); id <= 16; id++ {
		old = append(old, store.Run{Project: "acme/app", Workflow: "build", ID: id, IssueID: "x", Status: "failed",
			StartedAt: "2026-10-01T00:00:00Z", CompletedAt: "2026-10-01T00:00:00Z", AgentAdapter: "pi"})
	}
	if err := st.AddRuns(old); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	r, err := st.Run("acme/app", "build", 2)
	if err != nil || r.WorkflowFile != "WORKFLOW.build.md" || r.TemplateID == "" || r.SessionID != "" {
		t.Errorf("run 2 = %+v, %v, want workflow file and template backfilled, no session", r, err)
	}
}
