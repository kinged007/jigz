package main

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

// openRO opens a sortie SQLite DB read-only without disturbing the
// writer. Plain mode=ro (not immutable=1): sortie runs in WAL mode and
// the WAL is where the newest rows are, which an immutable open cannot
// see. A read-only query takes no write lock, and _query_only blocks
// writes through this handle.
func openRO(path string) (*sql.DB, error) {
	// The DSN is a URL, so a repository path holding ? or # would end the
	// path early and turn the rest into query parameters.
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_query_only=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func fmtTokens(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
}

// statePayload is the subset of the loop's /api/v1/state answer the
// supervisor needs to decide whether a loop has work in flight.
type statePayload struct {
	Counts struct {
		Retrying int `json:"retrying"`
	} `json:"counts"`
	Retrying []struct {
		IssueID string `json:"issue_id"`
	} `json:"retrying"`
	Running []struct {
		IssueIdentifier string `json:"issue_identifier"`
	} `json:"running"`
}
