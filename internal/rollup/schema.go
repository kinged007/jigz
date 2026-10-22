package rollup

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// OldestSchema is the oldest schema_migrations version this reader
// accepts: 18, written by the older engine. A database between OldestSchema
// and the newest known engine schema is read when it has every column in
// requiredColumns; later migrations only add columns, tables and indexes.
const OldestSchema = 18

// EngineRelease is a sortie release and the schema_migrations version its
// database ends at.
type EngineRelease struct {
	Version string
	Schema  int
}

// EngineReleases lists the engine releases this reader has been checked
// against, oldest first. A database newer than the last entry was written by
// an engine this release does not know and is not read.
var EngineReleases = []EngineRelease{{"1.28.1", 38}, {"1.29.0", 43}}

// SupportedSchemas lists the schema versions with a checked layout: 18 for
// the older engine, 35 for engine 1.27.0, and the end version of each
// EngineReleases entry. It is informative; the accepted range is
// OldestSchema to NewestSchema.
var SupportedSchemas = []int{OldestSchema, 35, 38, 43}

// NewestSchema is the schema version of the newest known engine release.
func NewestSchema() int { return EngineReleases[len(EngineReleases)-1].Schema }

// SchemaOf returns the schema version of an engine release tag such as
// "v1.28.1" or "1.28.1", and false for a release that is not known.
func SchemaOf(tag string) (int, bool) {
	tag = strings.TrimPrefix(tag, "v")
	for _, r := range EngineReleases {
		if r.Version == tag {
			return r.Schema, true
		}
	}
	return 0, false
}

// requiredColumns is every table and column Jigz reads. A supported
// version that lacks one hides only what depends on that table.
var requiredColumns = map[string][]string{
	"run_history": {
		"id", "issue_id", "identifier", "display_identifier", "attempt", "agent_adapter",
		"started_at", "completed_at", "status", "error", "turns_completed",
		"input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "tokens_measured",
	},
	"aggregate_metrics":   {"key", "input_tokens", "output_tokens", "total_tokens", "cache_read_tokens"},
	"session_metadata":    {"issue_id", "model_name"},
	"parked_issues":       {"issue_id", "reason"},
	"retry_entries":       {"issue_id", "attempt", "error"},
	"budget_hold_notices": {"issue_id", "reason"},
}

// SchemaNotice says why a workflow database is not fully read.
type SchemaNotice struct {
	// Version is the highest schema_migrations version found, 0 when the
	// table is absent.
	Version   int   `json:"version"`
	Supported []int `json:"supported"`
	// Missing lists "table" or "table.column" entries a supported
	// version lacks.
	Missing []string `json:"missing,omitempty"`
}

func (n SchemaNotice) String() string {
	if len(n.Missing) > 0 {
		return fmt.Sprintf("schema %d is missing %s", n.Version, strings.Join(n.Missing, ", "))
	}
	if n.Version > NewestSchema() {
		return fmt.Sprintf("schema %d was written by a newer engine than this release knows (newest known: schema %d)", n.Version, NewestSchema())
	}
	return fmt.Sprintf("unsupported schema version %d (supported: %d to %d)", n.Version, OldestSchema, NewestSchema())
}

// Schema is what one workflow database offers.
type Schema struct {
	// Notice is nil when every table is readable, and also for a
	// database that has no tables yet.
	Notice *SchemaNotice
	usable map[string]bool
}

// Has reports whether a table can be read with the columns Jigz uses.
func (s Schema) Has(table string) bool { return s.usable[table] }

// OpenReadOnly opens a workflow database that another process is writing.
func OpenReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(500)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// InspectSchema checks schema_migrations against the accepted range and
// the columns of every table Jigz reads.
func InspectSchema(db *sql.DB) Schema {
	version := 0
	if tableExists(db, "schema_migrations") {
		var v sql.NullInt64
		if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err == nil {
			version = int(v.Int64)
		}
	} else if !tableExists(db, "run_history") {
		// A database that has not been written yet is not broken.
		return Schema{}
	}
	if version < OldestSchema || version > NewestSchema() {
		return Schema{Notice: &SchemaNotice{Version: version, Supported: SupportedSchemas}}
	}
	s := Schema{usable: map[string]bool{}}
	var missing []string
	for table, cols := range requiredColumns {
		have := columns(db, table)
		ok := len(have) > 0
		if !ok {
			missing = append(missing, table)
		}
		for _, c := range cols {
			if ok && !have[c] {
				ok = false
				missing = append(missing, table+"."+c)
			}
		}
		s.usable[table] = ok
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		s.Notice = &SchemaNotice{Version: version, Supported: SupportedSchemas, Missing: missing}
	}
	return s
}

func columns(db *sql.DB, table string) map[string]bool {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out[n] = true
		}
	}
	return out
}
