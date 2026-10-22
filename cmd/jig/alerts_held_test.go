package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/kinged007/jigz/internal/rollup"
)

func TestLoopAlertsFromStateAndDatabaseFallback(t *testing.T) {
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/state" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"counts":{},"parked":[{"issue_id":"8","reason":"api parked"},{"issue_id":"7"}],
			"budget_exhausted":[{"issue_id":"9","reason":"api budget"}]}`))
	}))
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	live := rollup.Reader{}.Loop("acme/app", "build", port, path, true)
	got := loopDBAlerts(live)
	if len(got) != 2 ||
		got[0].Kind != "Budget hold" || got[0].Msg != "acme/app/build: held by the budget #9 (api budget)" ||
		got[1].Kind != "Parked issue" || got[1].Msg != "acme/app/build: parked #7, #8 (api parked)" {
		t.Errorf("live alerts = %+v", got)
	}

	srv.Close()
	down := rollup.Reader{}.Loop("acme/app", "build", port, path, true)
	down.Schema = nil
	got = loopDBAlerts(down)
	if len(got) != 2 || got[0].Msg != "acme/app/build: held by the budget #1 (db budget)" ||
		got[1].Msg != "acme/app/build: parked #2 (db parked)" {
		t.Errorf("fallback alerts = %+v", got)
	}
}
