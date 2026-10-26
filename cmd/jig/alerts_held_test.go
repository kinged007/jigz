package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/kinged007/jigz/internal/rollup"
)

func TestLoopAlertsFromTheDatabase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	path := filepath.Join(t.TempDir(), "w.db")
	writeTestDB(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`CREATE TABLE schema_migrations (version INTEGER)`,
		`INSERT INTO schema_migrations VALUES (38)`,
		`CREATE TABLE budget_hold_notices (issue_id TEXT, reason TEXT)`,
		`CREATE TABLE parked_issues (issue_id TEXT, reason TEXT)`,
		`INSERT INTO budget_hold_notices VALUES ('1', 'db budget')`,
		`INSERT INTO parked_issues VALUES ('2', 'db parked')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	l := rollup.Reader{}.Loop("acme/app", "build", path, true, nil, true)
	got := loopDBAlerts(l)
	if len(got) != 2 ||
		got[0].Kind != "Budget hold" || got[0].Msg != "acme/app/build: held by the budget #1 (db budget)" ||
		got[1].Kind != "Parked issue" || got[1].Msg != "acme/app/build: parked #2 (db parked)" {
		t.Errorf("alerts = %+v", got)
	}
}
