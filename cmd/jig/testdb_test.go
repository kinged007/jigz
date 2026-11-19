package main

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
)

// writeTestDB creates a sortie-shaped database: the two tables
// readLoopDB reads, with two run_history rows. The DSN is built as a URL
// for the same reason openRO does — a path holding ? or # would otherwise
// be cut short and the database created would not be the one read back.
func writeTestDB(t *testing.T, path string) {
	t.Helper()
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE aggregate_metrics (key TEXT, input_tokens INTEGER, output_tokens INTEGER,
			total_tokens INTEGER, cache_read_tokens INTEGER, seconds_running REAL, updated_at TEXT)`,
		`CREATE TABLE run_history (id INTEGER PRIMARY KEY, display_identifier TEXT, identifier TEXT,
			status TEXT, input_tokens INTEGER, output_tokens INTEGER, total_tokens INTEGER,
			cache_read_tokens INTEGER, completed_at TEXT)`,
		`INSERT INTO run_history (display_identifier, identifier, status, input_tokens, output_tokens, total_tokens, cache_read_tokens, completed_at)
			VALUES ('#1', '1', 'succeeded', 10, 5, 15, 0, '2026-01-01'),
			('#2', '2', 'failed', 20, 10, 30, 0, '2026-01-02')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v (path %s)", s, err, filepath.Base(path))
		}
	}
}
