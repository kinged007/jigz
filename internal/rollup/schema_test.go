package rollup

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func schemaDB(t *testing.T, version int, extra ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	stmts := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT)`,
		fmt.Sprintf(`INSERT INTO schema_migrations VALUES (%d, '')`, version),
		`CREATE TABLE run_history (id INTEGER, issue_id TEXT, identifier TEXT, display_identifier TEXT, attempt INTEGER,
			agent_adapter TEXT, started_at TEXT, completed_at TEXT, status TEXT, error TEXT, turns_completed INTEGER,
			input_tokens INTEGER, output_tokens INTEGER, total_tokens INTEGER, cache_read_tokens INTEGER, tokens_measured INTEGER)`,
	}
	for _, s := range append(stmts, extra...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestInspectSchemaAcceptsTheRangeUpToTheNewestKnownEngine(t *testing.T) {
	for _, v := range []int{18, 35, 38, 40, 43} {
		if !InspectSchema(schemaDB(t, v)).Has("run_history") {
			t.Errorf("schema %d: run_history is not readable", v)
		}
	}
}

func TestInspectSchemaRejectsOutsideTheRange(t *testing.T) {
	for v, want := range map[int]string{
		17: "unsupported schema version 17",
		44: "written by a newer engine than this release knows",
	} {
		s := InspectSchema(schemaDB(t, v))
		if s.Notice == nil || !strings.Contains(s.Notice.String(), want) || s.Has("run_history") {
			t.Errorf("schema %d: notice %v, readable %v, want %q and nothing read", v, s.Notice, s.Has("run_history"), want)
		}
	}
}

func TestInspectSchemaReportsAMissingColumn(t *testing.T) {
	s := InspectSchema(schemaDB(t, 43, `ALTER TABLE run_history DROP COLUMN tokens_measured`))
	if s.Has("run_history") || s.Notice == nil || !slices.Contains(s.Notice.Missing, "run_history.tokens_measured") {
		t.Errorf("notice %+v, readable %v, want run_history.tokens_measured missing and the table unread", s.Notice, s.Has("run_history"))
	}
}

// TestSchemaFixturesCoverReleases pins section 7.3: every supported
// engine release has a recorded schema fixture, no fixture is
// orphaned, and every fixture builds a database InspectSchema reads
// clean with writable required tables.
func TestSchemaFixturesCoverReleases(t *testing.T) {
	entries, err := filepath.Glob("testdata/schema/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]bool{}
	for _, e := range entries {
		var v int
		if _, err := fmt.Sscanf(filepath.Base(e), "%d.sql", &v); err != nil {
			t.Errorf("fixture %s is not <schema>.sql", e)
			continue
		}
		if v <= 0 {
			t.Errorf("fixture %s names no schema version", e)
			continue
		}
		got[v] = true
		db := buildFromFixture(t, e)
		s := InspectSchema(db)
		if s.Notice != nil {
			t.Errorf("%s: %v", e, s.Notice)
		}
		for table := range requiredColumns {
			if !s.Has(table) {
				t.Errorf("%s: %s is not readable", e, table)
			}
		}
		insertRequiredRow(t, db, v)
	}
	want := map[int]bool{}
	for _, r := range EngineReleases {
		want[r.Schema] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fixtures %v, want one per release schema %v", got, want)
	}
}

// buildFromFixture executes a recorded schema dump in memory.
func buildFromFixture(t *testing.T, path string) *sql.DB {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	var stmts []string
	for _, line := range strings.Split(string(raw), "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "--") {
			continue
		} else {
			stmts = append(stmts, line)
		}
	}
	for _, stmt := range strings.Split(strings.Join(stmts, "\n"), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	return db
}

// insertRequiredRow writes one row per required table, proving the
// columns Jigz touches accept writes on a real engine schema. Columns
// are zero-valued by their declared type, covering NOT NULL columns
// without defaults that Jigz never sets.
func insertRequiredRow(t *testing.T, db *sql.DB, version int) {
	t.Helper()
	type col struct {
		name       string
		ctype      string
		notnull    bool
		hasDefault bool
		pk         bool
	}
	columns := func(table string) []col {
		rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []col
		for rows.Next() {
			var c col
			var cid, pk int
			var dflt sql.NullString
			if err := rows.Scan(&cid, &c.name, &c.ctype, &c.notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			c.pk = pk > 0
			c.hasDefault = dflt.Valid
			out = append(out, c)
		}
		return out
	}
	zero := func(ctype string) any {
		switch strings.ToUpper(ctype) {
		case "INTEGER":
			return 0
		case "REAL":
			return 0.0
		default:
			return ""
		}
	}
	for table := range requiredColumns {
		byName := map[string]col{}
		for _, c := range columns(table) {
			byName[c.name] = c
		}
		seen := map[string]bool{}
		var names []string
		var args []any
		add := func(name string) {
			if seen[name] {
				return
			}
			seen[name] = true
			names = append(names, name)
			if c, ok := byName[name]; ok {
				args = append(args, zero(c.ctype))
			} else {
				args = append(args, "")
			}
		}
		for _, c := range columns(table) {
			if c.pk {
				continue
			}
			if c.notnull && !c.hasDefault {
				add(c.name)
			}
		}
		for _, c := range requiredColumns[table] {
			add(c)
		}
		q := make([]string, len(names))
		for i := range q {
			q[i] = "?"
		}
		if _, err := db.Exec(fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(names, ","), strings.Join(q, ",")), args...); err != nil {
			t.Errorf("schema %d: insert into %s: %v", version, table, err)
		}
	}
}

func TestEngineReleasesEndAtTheirSchema(t *testing.T) {
	for tag, want := range map[string]int{"v1.28.1": 38, "1.29.0": 43} {
		if got, ok := SchemaOf(tag); !ok || got != want {
			t.Errorf("SchemaOf(%q) = %d, %v, want %d", tag, got, ok, want)
		}
	}
	if _, ok := SchemaOf("v9.9.9"); ok {
		t.Error("an unknown release has a schema")
	}
	if NewestSchema() != 43 {
		t.Errorf("NewestSchema = %d, want 43", NewestSchema())
	}
}
