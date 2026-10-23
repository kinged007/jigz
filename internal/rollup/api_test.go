package rollup

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const stateBody = `{"counts":{"running":1,"retrying":2},
	"agent_totals":{"input_tokens":100,"output_tokens":50,"total_tokens":150},
	"running":[{"display_identifier":"acme/app#7","state":"running","turn_count":2,"model_name":"m",
		"started_at":"2026-10-01T01:00:00Z","dispatched_at":"2026-10-01T00:30:00Z","tokens":{"total_tokens":42}},
		{"issue_identifier":"8","state":"running","dispatched_at":null,"tokens":{}}]}`

const statsBody = `{"summary":{"runs":9,"issues":4,"succeeded":5,"failed":3,"stopped":1,"tokens_unmeasured_runs":2},
	"by_status":[{"name":"succeeded","runs":5},{"name":"failed","runs":2},{"name":"ci_failed","runs":1},{"name":"needs_person","runs":1}]}`

func engine(t *testing.T, stats string) int {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/state", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(stateBody)) })
	mux.HandleFunc("/api/v1/stats", func(w http.ResponseWriter, r *http.Request) {
		if stats == "" {
			http.Error(w, "history unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(stats))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return port
}

func TestLiveLoopIsReadThroughTheEngineAPI(t *testing.T) {
	port := engine(t, statsBody)
	missing := filepath.Join(t.TempDir(), "none.db")
	l := Reader{}.Loop("acme/app", "build", port, missing, true)
	if !l.Live || l.DBError != "" || l.Schema != nil {
		t.Fatalf("loop = %+v, want live with no database read", l)
	}
	if l.Input != 100 || l.Output != 50 || l.Tokens != 150 {
		t.Errorf("tokens = %d/%d/%d, want 100/50/150 from agent_totals", l.Input, l.Output, l.Tokens)
	}
	if l.Runs != 9 || l.Issues != 4 || l.Unmeasured != 2 || l.Succeeded != 5 || l.Failed != 2 {
		t.Errorf("counts = runs %d issues %d unmeasured %d succeeded %d failed %d, want 9 4 2 5 2 (failed is the status failed only)",
			l.Runs, l.Issues, l.Unmeasured, l.Succeeded, l.Failed)
	}
	if len(l.Running) != 2 || l.Running[0].PickUp != "2026-10-01T00:30:00Z" || l.Running[1].PickUp != "" {
		t.Errorf("running = %+v, want the pass dispatch time as the pickup time", l.Running)
	}
}

func TestLoopFallsBackToTheDatabaseWhenStatsFail(t *testing.T) {
	port := engine(t, "")
	raw, err := os.ReadFile("testdata/schema18.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "w.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	l := Reader{}.Loop("acme/app", "build", port, path, true)
	if !l.Live || l.Runs != 16 {
		t.Errorf("loop = live %v, runs %d, want live with the 16 runs of the database", l.Live, l.Runs)
	}
}

func TestLoopWithoutAnEngineIsReadFromTheDatabase(t *testing.T) {
	raw, err := os.ReadFile("testdata/schema18.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "w.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	l := Reader{}.Loop("acme/app", "build", 0, path, false)
	if l.Live || l.Runs != 16 || l.Tokens == 0 {
		t.Errorf("loop = live %v, runs %d, tokens %d, want the totals of the database", l.Live, l.Runs, l.Tokens)
	}
}

func TestLiveLoopReadsHeldIssuesFromState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/state" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"counts":{},"parked":[{"issue_id":"7","reason":"needs a person"}],
			"budget_exhausted":[{"issue_id":"9","reason":"token budget used"}]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	l := Reader{}.Loop("acme/app", "build", port, filepath.Join(t.TempDir(), "none.db"), true)
	if len(l.Parked) != 1 || l.Parked[0] != (Held{"7", "needs a person"}) {
		t.Errorf("parked = %+v", l.Parked)
	}
	if len(l.BudgetHeld) != 1 || l.BudgetHeld[0] != (Held{"9", "token budget used"}) {
		t.Errorf("budget held = %+v", l.BudgetHeld)
	}
}
