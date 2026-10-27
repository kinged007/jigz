package rollup

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureDB(t *testing.T, extra ...string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/schema18.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "w.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range append([]string{string(raw)}, extra...) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

const inFlight = `INSERT INTO session_metadata
	(issue_id,session_id,agent_pid,input_tokens,output_tokens,total_tokens,updated_at,model_name,dispatch_id)
	VALUES ('30','sess-0030','1030',60,40,100,'2026-10-15T10:00:00Z','claude-sonnet-5-5','DISPATCH30')`

func TestLoopWithoutAProcessIsReadFromTheDatabase(t *testing.T) {
	l := Reader{}.Loop("acme/app", "build", fixtureDB(t, inFlight), false, nil, false)
	if l.Live || l.Runs != 16 || l.Tokens == 0 || len(l.Running) != 0 {
		t.Errorf("loop = live %v, runs %d, tokens %d, running %d, want the totals and nothing running", l.Live, l.Runs, l.Tokens, len(l.Running))
	}
}

func TestLoopReadsRetriesAndHoldsFromTheDatabase(t *testing.T) {
	l := Reader{}.Loop("acme/app", "build", fixtureDB(t), true, nil, true)
	if l.Retrying != 1 {
		t.Errorf("retrying = %d, want the 1 retry_entries row", l.Retrying)
	}
	if len(l.Parked) != 1 || l.Parked[0] != (Held{"13", "handoff absence"}) {
		t.Errorf("parked = %+v", l.Parked)
	}
	if len(l.BudgetHeld) != 1 || l.BudgetHeld[0] != (Held{"20", "period token cap reached"}) {
		t.Errorf("budget held = %+v", l.BudgetHeld)
	}
}

func TestSessionsFromTheSupervisorAreFilledFromTheDatabase(t *testing.T) {
	started := time.Date(2026, 10, 15, 9, 55, 0, 0, time.UTC)
	sessions := []Session{
		{IssueID: "30", Identifier: "acme/app#30", Attempt: 1, StartedAt: started, Turns: 2},
		{IssueID: "31", Identifier: "acme/app#31", Attempt: 1, StartedAt: started},
	}
	l := Reader{}.Loop("acme/app", "build", fixtureDB(t, inFlight), true, sessions, true)
	if len(l.Running) != 2 {
		t.Fatalf("running = %+v, want the two sessions the supervisor reports", l.Running)
	}
	first, second := l.Running[0], l.Running[1]
	if first.Identifier != "acme/app#30" || first.Tokens != 100 || first.Model != "claude-sonnet-5-5" || first.Turns != 2 || first.StartedAt != "2026-10-15T09:55:00Z" {
		t.Errorf("first = %+v, want the supervisor's identifier and turns with the tokens and model of the database", first)
	}
	if second.Tokens != 0 || second.Model != "" {
		t.Errorf("second = %+v, want no tokens before its first turn has ended", second)
	}
}

func TestFinishedSessionRowIsNotRunning(t *testing.T) {
	sessions := []Session{{IssueID: "10", Identifier: "acme/app#10", StartedAt: time.Now()}}
	l := Reader{}.Loop("acme/app", "build", fixtureDB(t), true, sessions, true)
	if len(l.Running) != 1 || l.Running[0].Tokens != 0 {
		t.Errorf("running = %+v, want the new attempt without the tokens of the finished one", l.Running)
	}
}

func TestRunningListFallsBackToTheDatabaseWhenSessionsAreUnknown(t *testing.T) {
	path := fixtureDB(t, inFlight)
	l := Reader{}.Loop("acme/app", "build", path, true, nil, false)
	if len(l.Running) != 1 || l.Running[0].Identifier != "30" || l.Running[0].Tokens != 100 {
		t.Errorf("running = %+v, want the in-flight row of the database", l.Running)
	}
	l = Reader{}.Loop("acme/app", "build", path, true, nil, true)
	if len(l.Running) != 0 {
		t.Errorf("running = %+v, want none when the supervisor accounts for no session", l.Running)
	}
}

func TestInFlightRowGetsTheDisplayIdentifierOfTheHistory(t *testing.T) {
	path := fixtureDB(t, `INSERT INTO session_metadata
		(issue_id,session_id,updated_at,dispatch_id) VALUES ('19','s','2026-10-15T10:00:00Z','D19')`)
	l := Reader{}.Loop("acme/app", "build", path, true, nil, false)
	if len(l.Running) != 1 || l.Running[0].Identifier != "acme/app#19" {
		t.Errorf("running = %+v, want the display identifier of issue 19 from run_history", l.Running)
	}
}

func TestCountRetrying(t *testing.T) {
	if n, err := CountRetrying(fixtureDB(t)); err != nil || n != 1 {
		t.Errorf("retrying = %d, %v, want 1", n, err)
	}
	if n, err := CountRetrying(filepath.Join(t.TempDir(), "none.db")); err != nil || n != 0 {
		t.Errorf("missing database: retrying = %d, %v, want 0 and no error", n, err)
	}
}

func TestSessionKeepsTheLoggedKindAndModelUnlessTheDatabaseHasOne(t *testing.T) {
	sessions := []Session{
		{IssueID: "30", Identifier: "acme/app#30", AgentKind: "pi", RuleName: "plan", Model: "logged", Effort: "high"},
		{IssueID: "31", Identifier: "acme/app#31", AgentKind: "pi", Model: "logged"},
	}
	l := Reader{}.Loop("acme/app", "build", fixtureDB(t, inFlight), true, sessions, true)
	first, second := l.Running[0], l.Running[1]
	if first.AgentKind != "pi" || first.RuleName != "plan" || first.Effort != "high" || first.Model != "claude-sonnet-5-5" {
		t.Errorf("first = %+v, want the logged kind, rule and effort with the database model", first)
	}
	if second.AgentKind != "pi" || second.Model != "logged" {
		t.Errorf("second = %+v, want the logged kind and model before the first turn", second)
	}
	raw, _ := json.Marshal(second)
	if !strings.Contains(string(raw), `"agent_kind":"pi"`) {
		t.Errorf("json = %s, want agent_kind", raw)
	}
}
