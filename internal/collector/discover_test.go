package collector

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/store"
)

func repoWithDBs(t *testing.T, files map[string]string, dbs ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".sortie", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src, _ := fixture(t)
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range dbs {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func discovering(t *testing.T, loops []rollup.Loop, roots map[string]string) (*Collector, *store.Store) {
	t.Helper()
	c, st := newCollector(t, "")
	c.Source = fakeSource{loops: loops}
	c.Discover = func() []rollup.Loop { return loopsrc.Discover(roots) }
	return c, st
}

func runCounts(t *testing.T, st *store.Store) map[string]int {
	t.Helper()
	all, err := st.Runs(store.RunFilter{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, r := range all {
		out[r.Project+"/"+r.Workflow]++
	}
	return out
}

func TestStoppedProjectIsCopiedFromItsDatabases(t *testing.T) {
	root := repoWithDBs(t, map[string]string{
		"WORKFLOW.build.md": "---\ndb_path: .sortie-build.db\n---\n",
		"WORKFLOW.plain.md": "no front matter\n",
	}, ".sortie-build.db", ".sortie.db", ".sortie-orphan.db")
	c, st := discovering(t, nil, map[string]string{"acme/app": root})
	c.Once(context.Background())
	got := runCounts(t, st)
	want := map[string]int{"acme/app/build": 16, "acme/app/plain": 16, "acme/app/orphan": 16}
	if len(got) != len(want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("runs = %v, want %v", got, want)
		}
	}
}

func TestWorkflowsSharingADatabaseAreCountedOnce(t *testing.T) {
	root := repoWithDBs(t, map[string]string{
		"WORKFLOW.a.md": "---\ndb_path: shared.db\n---\n",
		"WORKFLOW.b.md": "---\ndb_path: \"./shared.db\"\n---\n",
	}, "shared.db")
	c, st := discovering(t, nil, map[string]string{"acme/app": root})
	c.Once(context.Background())
	got := runCounts(t, st)
	if len(got) != 1 || got["acme/app/a"] != 16 {
		t.Fatalf("runs = %v, want only acme/app/a: 16", got)
	}
}

func TestRegisteredLoopIsNotCountedAgainWhenDiscovered(t *testing.T) {
	root := repoWithDBs(t, map[string]string{
		"WORKFLOW.build.md": "---\ndb_path: .sortie-build.db\n---\n",
		"WORKFLOW.other.md": "---\ndb_path: .sortie-build.db\n---\n",
	}, ".sortie-build.db")
	live := rollup.Loop{
		Repo: "acme/app", Loop: "other", Live: true,
		DBPath: filepath.Join(root, ".sortie", "workflows", ".sortie-build.db"),
	}
	c, st := discovering(t, []rollup.Loop{live}, map[string]string{"acme/app": root})
	c.Once(context.Background())
	c.Once(context.Background())
	got := runCounts(t, st)
	if len(got) != 1 || got["acme/app/other"] != 16 {
		t.Fatalf("runs = %v, want only acme/app/other: 16", got)
	}
}

func TestDiscoveredDatabasesKeepGainingRuns(t *testing.T) {
	root := repoWithDBs(t, nil, ".sortie-build.db")
	c, st := discovering(t, nil, map[string]string{"acme/app": root})
	c.Once(context.Background())
	if n := runCounts(t, st)["acme/app/build"]; n != 16 {
		t.Fatalf("first pass = %d runs, want 16", n)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, ".sortie", "workflows", ".sortie-build.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO run_history (issue_id, identifier, attempt, agent_adapter, workspace,
		started_at, completed_at, status, display_identifier) VALUES ('21','21',1,'pi','','2026-10-15T00:00:00Z','2026-10-15T00:01:00Z','succeeded','acme/app#21')`); err != nil {
		t.Fatal(err)
	}
	c.Once(context.Background())
	if n := runCounts(t, st)["acme/app/build"]; n != 17 {
		t.Fatalf("second pass = %d runs, want 17", n)
	}
}

func TestGarbageDiscoveredDatabaseIsSkipped(t *testing.T) {
	root := repoWithDBs(t, nil, ".sortie-ok.db")
	bad := filepath.Join(root, ".sortie", "workflows", ".sortie-bad.db")
	if err := os.WriteFile(bad, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, st := discovering(t, nil, map[string]string{"acme/app": root})
	c.Once(context.Background())
	got := runCounts(t, st)
	if got["acme/app/ok"] != 16 || got["acme/app/bad"] != 0 {
		t.Fatalf("runs = %v", got)
	}
}
