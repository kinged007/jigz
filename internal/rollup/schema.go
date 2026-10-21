package rollup

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// SupportedSchemas lists the schema_migrations versions whose layout
// this reader has been checked against: 18 for the older engine, 35 for
// engine 1.27.0, 38 for the pinned engine (EngineSchema). A workflow database at any other
// version is reported with a SchemaNotice and read for nothing, because
// a column that moved would turn into a wrong number rather than an
// error.
var SupportedSchemas = []int{18, 35, EngineSchema}

// EngineSchema is the latest schema_migrations version of the pinned
// engine. Migrations 36 to 38 only add columns and tables that this
// reader does not use.
const EngineSchema = 38

// requiredColumns is every table and column central reads. A supported
// version that lacks one hides only what depends on that table.
var requiredColumns = map[string][]string{
	"run_history": {
		"id", "issue_id", "identifier", "display_identifier", "attempt", "agent_adapter",
		"workspace", "started_at", "completed_at", "status", "error", "turns_completed",
		"input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "tokens_measured",
	},
	"aggregate_metrics":      {"key", "input_tokens", "output_tokens", "total_tokens", "cache_read_tokens"},
	"session_metadata":       {"issue_id", "model_name"},
	"parked_issues":          {"issue_id", "reason"},
	"retry_entries":          {"issue_id", "attempt", "error"},
	"budget_hold_notices":    {"issue_id", "reason"},
	"handoff_absence_resets": {"issue_id", "reset_run_id"},
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
	return fmt.Sprintf("unsupported schema version %d (supported: %v)", n.Version, n.Supported)
}

// Schema is what one workflow database offers.
type Schema struct {
	// Notice is nil when every table is readable, and also for a
	// database that has no tables yet.
	Notice *SchemaNotice
	usable map[string]bool
}

// Has reports whether a table can be read with the columns central uses.
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

// InspectSchema checks schema_migrations against SupportedSchemas and
// the columns of every table central reads.
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
	if !slices.Contains(SupportedSchemas, version) {
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
