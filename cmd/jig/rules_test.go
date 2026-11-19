package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/store"
)

func ruleTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	runs := []store.Run{
		{Project: "o/p", Workflow: "build", ID: 1, IssueID: "1", DisplayIdentifier: "o/p#1",
			Status: "succeeded", StartedAt: "2026-01-01T10:00:00Z", CompletedAt: "2026-01-01T10:05:00Z",
			RuleName: "plan"},
		{Project: "o/p", Workflow: "build", ID: 2, IssueID: "2", DisplayIdentifier: "o/p#2",
			Status: "succeeded", StartedAt: "2026-01-02T10:00:00Z", CompletedAt: "2026-01-02T10:05:00Z",
			RuleName: "triage"},
	}
	if err := st.AddRuns(runs); err != nil {
		t.Fatal(err)
	}
	return st
}

// The Runs API rule parameter returns only stored runs with that rule.
func TestApiRunsFiltersByRule(t *testing.T) {
	a := &app{sizes: newRepoSizes(), stateDir: t.TempDir(), store: ruleTestStore(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/runs?rule=plan", nil)
	req.Host = "127.0.0.1"
	a.routes(newHostGuard("127.0.0.1")).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /api/v1/runs?rule=plan = %d: %.300s", rec.Code, rec.Body.String())
	}
	var body struct {
		Runs []store.Run `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 1 || body.Runs[0].RuleName != "plan" {
		t.Errorf("runs = %+v, want the one plan run", body.Runs)
	}
}

// A rule filter matches no active rows: live runs carry no rule.
func TestActiveRowsExcludedWhenRuleFilterSet(t *testing.T) {
	live := []activeRun{{
		Repo: "o/p", Loop: "build", Identifier: "o/p#7", State: "running",
		PickedUp: "2026-01-03T10:00:00Z",
	}}
	if got := activeRows(live, runQuery{}); len(got) != 1 {
		t.Fatalf("activeRows without a rule = %d rows, want 1", len(got))
	}
	if got := activeRows(live, runQuery{Rule: "plan"}); len(got) != 0 {
		t.Errorf("activeRows with a rule = %d rows, want 0", len(got))
	}
}

// Drilling down from a rule group opens the Runs page filtered by that
// rule. The "no rule" group leaves the parameter off, so the page stays
// unfiltered.
func TestRunsHrefRuleGroup(t *testing.T) {
	got := runsHref("rule", store.Group{Key: "plan"}, "2026-01-01", "2026-01-07")
	if !strings.Contains(got, "rule=plan") {
		t.Errorf("runsHref(rule plan) = %q, want rule=plan", got)
	}
	got = runsHref("rule", store.Group{}, "2026-01-01", "2026-01-07")
	if strings.Contains(got, "rule=") {
		t.Errorf("runsHref(rule empty) = %q, want no rule parameter", got)
	}
}

// The run page tile shows the routing rule, not the workflow name.
func TestStoredRowShowsRuleName(t *testing.T) {
	row := storedRow(store.Run{Workflow: "build", RuleName: "plan"})
	if row.Rule != "plan" || row.Workflow != "build" {
		t.Errorf("storedRow rule = %q workflow = %q, want plan and build", row.Rule, row.Workflow)
	}
}
