package rollup

import (
	"database/sql"
	"slices"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInspectSchemaAcceptsPinnedEngineVersion(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, s := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT)`,
		`INSERT INTO schema_migrations VALUES (38, '')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Contains(SupportedSchemas, EngineSchema) {
		t.Fatalf("SupportedSchemas %v lacks EngineSchema %d", SupportedSchemas, EngineSchema)
	}
	n := InspectSchema(db).Notice
	if n != nil && len(n.Missing) == 0 {
		t.Errorf("schema 38 rejected: %v", n)
	}
}
