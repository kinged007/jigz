package rollup

import (
	"database/sql"
	"fmt"
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
