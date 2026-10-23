package collector

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	machine "github.com/kinged007/jigz/internal/host"
	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/store"
)

type fakeSource struct{ loops []rollup.Loop }

func (fakeSource) Name() string                       { return "fake" }
func (fakeSource) Capabilities() loopsrc.Capabilities { return loopsrc.Capabilities{} }
func (f fakeSource) Fleet() rollup.Fleet              { return rollup.Fleet{Loops: f.loops} }

func fixture(t *testing.T, extra ...string) (path string, db *sql.DB) {
	t.Helper()
	raw, err := os.ReadFile("../rollup/testdata/schema18.sql")
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), ".sortie-build.db")
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range append([]string{string(raw)}, extra...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return path, db
}

func newCollector(t *testing.T, dbPath string) (*Collector, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	src := fakeSource{loops: []rollup.Loop{{
		Repo: "acme/app", Loop: "build", DBPath: dbPath, Live: true,
		Running: []rollup.Run{{Identifier: "acme/app#20"}}, Retrying: 2,
	}}}
	return &Collector{
		Store: st, Source: src,
		Model: func(project, workflow string) string { return "cfg-model" },
		Host:  func() machine.Metrics { return machine.Metrics{CPU: -1, MemUsed: 5, MemTotal: 10} },
	}, st
}

func TestRunsAreCopiedIncrementallyWithTheirModel(t *testing.T) {
	path, db := fixture(t)
	c, st := newCollector(t, path)
	c.Once(context.Background())

	all, err := st.Runs(store.RunFilter{Limit: store.MaxLimit})
	if err != nil || len(all) != 16 {
		t.Fatalf("runs = %d, %v, want 16", len(all), err)
	}
	byID := map[int64]store.Run{}
	for _, r := range all {
		byID[r.ID] = r
	}
	for id, want := range map[int64][2]string{
		1:  {"claude-sonnet-5-5", store.ModelObserved},
		13: {"coding-manual", store.ModelObserved},
		6:  {"cfg-model", store.ModelConfigured},
		8:  {"cfg-model", store.ModelConfigured},
	} {
		if r := byID[id]; r.Model != want[0] || r.ModelSource != want[1] {
			t.Errorf("run %d model = %q (%s), want %q (%s)", id, r.Model, r.ModelSource, want[0], want[1])
		}
	}
	if r := byID[7]; r.TokensMeasured || r.Error == "" || r.Project != "acme/app" || r.Workflow != "build" {
		t.Errorf("run 7 = %+v, want unmeasured with an error", r)
	}
	if r := byID[1]; !r.TokensMeasured || r.Error != "" || r.TotalTokens != 768329 {
		t.Errorf("run 1 = %+v, want measured, no error, 768329 tokens", r)
	}

	// Only rows past the last seen id are read on the next pass.
	if _, err := db.Exec(`INSERT INTO run_history (issue_id, identifier, attempt, agent_adapter, workspace,
		started_at, completed_at, status, display_identifier) VALUES ('21','21',1,'pi','','2026-10-15T00:00:00Z','2026-10-15T00:01:00Z','succeeded','acme/app#21')`); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	all, _ = st.Runs(store.RunFilter{Limit: store.MaxLimit})
	if len(all) != 17 || all[0].ID != 17 || all[0].DisplayIdentifier != "acme/app#21" {
		t.Errorf("after the second pass: %d runs, newest %+v, want 17 with run 17 first", len(all), all[0])
	}
}

func TestPassWritesLoopAndHostSamples(t *testing.T) {
	path, _ := fixture(t)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	if n, _ := st.CountSamples(); n != 2 {
		t.Errorf("samples = %d, want one loop sample and one host sample", n)
	}
}

func TestStateChangesBecomeEventsAfterTheFirstPass(t *testing.T) {
	path, db := fixture(t)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	if ev, _ := st.IssueEvents("acme/app", "build", "13"); len(ev) != 0 {
		t.Fatalf("first pass recorded %+v, want a baseline only", ev)
	}
	for _, s := range []string{
		`DELETE FROM parked_issues WHERE issue_id = '13'`,
		`INSERT INTO parked_issues(issue_id,identifier,display_id,reason,parked_state,label,label_applied,parked_at)
			VALUES ('14','14','acme/app#14','handoff absence','','needs-human',1,'now')`,
		`DELETE FROM retry_entries`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	c.Once(context.Background())
	kinds := func(issue string) string {
		ev, _ := st.IssueEvents("acme/app", "build", issue)
		s := ""
		for _, e := range ev {
			s += e.Kind + ";"
		}
		return s
	}
	if got := kinds("13"); got != "unparked;" {
		t.Errorf("issue 13 events = %q, want unparked", got)
	}
	if got := kinds("14"); got != "parked;" {
		t.Errorf("issue 14 events = %q, want parked", got)
	}
	if got := kinds("18"); got != "retry_cleared;" {
		t.Errorf("issue 18 events = %q, want retry_cleared", got)
	}
	c.Once(context.Background())
	if got := kinds("14"); got != "parked;" {
		t.Errorf("an unchanged state recorded again: %q", got)
	}
}

func TestUnusableDatabasesAreSkippedWithoutFailing(t *testing.T) {
	good, _ := fixture(t)
	newer, _ := fixture(t, `INSERT INTO schema_migrations VALUES (44, 'now')`)
	garbage := filepath.Join(t.TempDir(), "x.db")
	os.WriteFile(garbage, []byte("not a database"), 0o600)
	for name, path := range map[string]string{
		"missing":     filepath.Join(t.TempDir(), "none.db"),
		"empty path":  "",
		"garbage":     garbage,
		"unsupported": newer,
	} {
		c, st := newCollector(t, good)
		c.Source = fakeSource{loops: []rollup.Loop{{Repo: "acme/app", Loop: "build", DBPath: path}}}
		c.Once(context.Background())
		if runs, _ := st.Runs(store.RunFilter{}); len(runs) != 0 {
			t.Errorf("%s: copied %d runs, want none", name, len(runs))
		}
	}
}

func TestLockedDatabaseIsRetriedOnTheNextPass(t *testing.T) {
	path, db := fixture(t)
	c, st := newCollector(t, path)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	if runs, _ := st.Runs(store.RunFilter{}); len(runs) != 0 {
		t.Errorf("read %d runs through an exclusive lock, want none", len(runs))
	}
	if _, err := conn.ExecContext(context.Background(), `COMMIT`); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	if runs, _ := st.Runs(store.RunFilter{Limit: store.MaxLimit}); len(runs) != 16 {
		t.Errorf("runs = %d after the lock cleared, want 16", len(runs))
	}
}

func TestRunStopsWhenTheContextIsCancelled(t *testing.T) {
	path, _ := fixture(t)
	c, st := newCollector(t, path)
	c.Interval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		if n, _ := st.CountSamples(); n >= 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("collector made no repeated passes")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestWorkflowKindIsStoredOnNewRunsAndBackfilled(t *testing.T) {
	path, _ := fixture(t)
	c, st := newCollector(t, path)
	c.Once(context.Background())
	if r, err := st.Run("acme/app", "build", 1); err != nil || r.WorkflowKind != "" {
		t.Fatalf("run 1 kind = %q, %v, want unknown before a kind is known", r.WorkflowKind, err)
	}
	c.Kind = func(project, workflow string) string { return store.KindPR }
	c.Once(context.Background())
	all, _ := st.Usage(store.UsageFilter{}, "all")
	if all[0].PRs == 0 || all[0].Issues != 0 {
		t.Errorf("after the kind is known, issues, prs = %d, %d, want only PRs", all[0].Issues, all[0].PRs)
	}
	if r, _ := st.Run("acme/app", "build", 1); r.WorkflowKind != store.KindPR {
		t.Errorf("run 1 kind = %q, want pr", r.WorkflowKind)
	}
}
