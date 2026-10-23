package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesTheSchemaOnceAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.Version(); err != nil || v != len(migrations) {
		t.Fatalf("version = %d, %v, want %d", v, err, len(migrations))
	}
	for _, table := range []string{"runs_cache", "samples", "events", "audit_log"} {
		if _, err := s.db.Exec("SELECT 1 FROM " + table); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v, want wal", mode, err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s.Close()
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&n)
	if n != len(migrations) {
		t.Errorf("schema_version rows = %d, want %d: a migration ran twice", n, len(migrations))
	}
}

func TestOpenRefusesADatabaseFromANewerBuild(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`INSERT INTO schema_version VALUES (99, 'now')`)
	s.Close()
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("err = %v, want a newer-schema refusal", err)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved := migrations
	defer func() { migrations = saved }()
	migrations = []string{`CREATE TABLE a (x); CREATE TABLE a (x);`}
	if err := migrate(db); err == nil {
		t.Fatal("migrate succeeded on a broken migration")
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&n)
	if n != 0 {
		t.Errorf("schema_version rows = %d, want 0", n)
	}
}

func run(workflow string, id int64, started, status string) Run {
	return Run{
		Project: "o/a", Workflow: workflow, ID: id, IssueID: fmt.Sprint(id),
		DisplayIdentifier: fmt.Sprintf("o/a#%d", id), Status: status, StartedAt: started,
		CompletedAt: started, AgentAdapter: "pi", TokensMeasured: true,
	}
}

func TestRunsAreUpsertedAndFilteredNewestFirst(t *testing.T) {
	s := open(t)
	runs := []Run{
		run("build", 1, "2026-10-01T00:00:00Z", "succeeded"),
		run("build", 2, "2026-10-02T00:00:00Z", "failed"),
		run("plan", 1, "2026-10-03T00:00:00Z", "succeeded"),
		run("build", 3, "2026-10-04T00:00:00Z", "succeeded"),
	}
	if err := s.AddRuns(runs); err != nil {
		t.Fatal(err)
	}
	runs[1].Status = "cancelled"
	runs[1].ModelSource = ModelObserved
	if err := s.AddRuns(runs[1:2]); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.LastRunID("o/a", "build"); id != 3 {
		t.Errorf("last build run id = %d, want 3", id)
	}
	if id, _ := s.LastRunID("o/a", "none"); id != 0 {
		t.Errorf("last id of an unseen workflow = %d, want 0", id)
	}
	all, err := s.Runs(RunFilter{})
	if err != nil || len(all) != 4 {
		t.Fatalf("runs = %d, %v, want 4: the upsert duplicated a row", len(all), err)
	}
	if all[0].ID != 3 || all[1].Workflow != "plan" {
		t.Errorf("order = %d, %s, want newest first", all[0].ID, all[1].Workflow)
	}
	got, _ := s.Runs(RunFilter{Workflow: "build", State: "cancelled"})
	if len(got) != 1 || got[0].ID != 2 || got[0].ModelSource != ModelObserved {
		t.Errorf("filtered = %+v, want the updated run 2", got)
	}
	got, _ = s.Runs(RunFilter{Since: "2026-10-02T00:00:00Z", Until: "2026-10-04T00:00:00Z"})
	if len(got) != 2 {
		t.Errorf("date range matched %d, want 2", len(got))
	}
	got, _ = s.Runs(RunFilter{Q: "#3"})
	if len(got) != 1 || got[0].ID != 3 {
		t.Errorf("search matched %+v, want run 3", got)
	}
	if got, _ = s.Runs(RunFilter{Q: "%"}); len(got) != 0 {
		t.Errorf("a literal percent matched %d runs, want 0", len(got))
	}
}

func TestRunsPageByCursor(t *testing.T) {
	s := open(t)
	var runs []Run
	for i := 1; i <= 5; i++ {
		runs = append(runs, run("build", int64(i), fmt.Sprintf("2026-10-0%dT00:00:00Z", i), "succeeded"))
	}
	s.AddRuns(runs)
	first, _ := s.Runs(RunFilter{Limit: 2})
	second, _ := s.Runs(RunFilter{Limit: 2, Cursor: first[1].Cursor})
	third, _ := s.Runs(RunFilter{Limit: 2, Cursor: second[1].Cursor})
	var ids []int64
	for _, page := range [][]Run{first, second, third} {
		for _, r := range page {
			ids = append(ids, r.ID)
		}
	}
	if fmt.Sprint(ids) != "[5 4 3 2 1]" {
		t.Errorf("paged ids = %v, want [5 4 3 2 1]", ids)
	}
}

func TestRunLookup(t *testing.T) {
	s := open(t)
	s.AddRuns([]Run{run("build", 7, "2026-10-01T00:00:00Z", "failed")})
	r, err := s.Run("o/a", "build", 7)
	if err != nil || r.IssueID != "7" {
		t.Fatalf("run = %+v, %v", r, err)
	}
	if _, err := s.Run("o/a", "build", 8); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v, want no rows", err)
	}
}

func TestPruneSamplesDropsOnlyOldOnes(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	s.AddSamples([]Sample{
		{TS: ts(15 * 24 * time.Hour), Scope: "host"},
		{TS: ts(13 * 24 * time.Hour), Scope: "host"},
		{TS: ts(0), Scope: "loop", Key: "o/a/build", Running: 1},
	})
	if err := s.PruneSamples(now); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountSamples(); n != 2 {
		t.Errorf("samples = %d, want 2", n)
	}
}

func TestIssueEventsAreScopedToTheIssue(t *testing.T) {
	s := open(t)
	s.AddEvents([]Event{
		{TS: "t1", Project: "o/a", Workflow: "build", IssueID: "1", Kind: "parked", Detail: "why"},
		{TS: "t2", Project: "o/a", Workflow: "build", IssueID: "2", Kind: "parked"},
		{TS: "t3", Project: "o/a", Workflow: "build", IssueID: "1", Kind: "unparked"},
	})
	got, err := s.IssueEvents("o/a", "build", "1")
	if err != nil || len(got) != 2 || got[0].Kind != "parked" || got[1].Kind != "unparked" {
		t.Errorf("events = %+v, %v", got, err)
	}
}

func TestAudit(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, action := range []string{"login", "start", "stop"} {
		if err := s.AddAudit(now.Add(time.Duration(i)*time.Second), "10.0.0.1", action, "o/r", "", "ok"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Audit(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Action != "stop" || got[1].Action != "start" {
		t.Fatalf("Audit(2) = %+v, want stop then start", got)
	}
	if got[0].TS != "2026-01-02T03:04:07Z" || got[0].Actor != "10.0.0.1" || got[0].Project != "o/r" || got[0].Result != "ok" {
		t.Errorf("row = %+v", got[0])
	}
}

func TestPruneBackupsKeepsTheNewestOfOneFile(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 5; i++ {
		for _, path := range []string{"/a", "/b"} {
			if _, err := s.AddBackup(time.Now(), Backup{Scope: "jigz", Path: path, BackupPath: path + string(rune('0'+i))}); err != nil {
				t.Fatal(err)
			}
		}
	}
	gone, err := s.PruneBackups("/a", 2)
	if err != nil || len(gone) != 3 || gone[0] != "/a2" {
		t.Fatalf("pruned %v, err %v, want the three oldest /a backups", gone, err)
	}
	if a, _ := s.Backups("/a", 10); len(a) != 2 || a[0].BackupPath != "/a4" {
		t.Errorf("/a backups = %+v", a)
	}
	if b, _ := s.Backups("/b", 10); len(b) != 5 {
		t.Errorf("/b was pruned: %+v", b)
	}
	if all, _ := s.Backups("", 100); len(all) != 7 {
		t.Errorf("all backups = %d, want 7", len(all))
	}
}
