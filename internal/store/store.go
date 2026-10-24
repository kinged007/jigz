// Package store is the dashboard history database, <state_dir>/history.db.
// Workflow databases hold runs but no time series, and their rows go away
// when a workflow is cleaned up, so Jigz keeps its own copy.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// FileName is the database file inside the state directory.
const FileName = "history.db"

// SampleRetention is how long samples are kept.
const SampleRetention = 14 * 24 * time.Hour

// Store is an open history database.
type Store struct {
	db *sql.DB
}

// migrations are applied in order, once each. A migration is never edited
// after release: a change is a new entry at the end.
var migrations = []string{
	`CREATE TABLE runs_cache (
		id                 INTEGER PRIMARY KEY AUTOINCREMENT,
		project            TEXT    NOT NULL,
		workflow           TEXT    NOT NULL,
		run_id             INTEGER NOT NULL,
		issue_id           TEXT    NOT NULL,
		display_identifier TEXT    NOT NULL,
		status             TEXT    NOT NULL,
		started_at         TEXT    NOT NULL,
		completed_at       TEXT    NOT NULL,
		attempt            INTEGER NOT NULL,
		turns_completed    INTEGER NOT NULL,
		error              TEXT    NOT NULL DEFAULT '',
		agent_adapter      TEXT    NOT NULL,
		model              TEXT    NOT NULL DEFAULT '',
		model_source       TEXT    NOT NULL DEFAULT '' CHECK (model_source IN ('', 'observed', 'configured')),
		input_tokens       INTEGER NOT NULL,
		output_tokens      INTEGER NOT NULL,
		total_tokens       INTEGER NOT NULL,
		cache_read_tokens  INTEGER NOT NULL,
		tokens_measured    INTEGER NOT NULL,
		workspace          TEXT    NOT NULL,
		UNIQUE (project, workflow, run_id)
	);
	CREATE INDEX runs_cache_started ON runs_cache (started_at);
	CREATE TABLE samples (
		ts         TEXT    NOT NULL,
		scope      TEXT    NOT NULL,
		key        TEXT    NOT NULL,
		running    INTEGER NOT NULL DEFAULT 0,
		retrying   INTEGER NOT NULL DEFAULT 0,
		cpu_pct    REAL    NOT NULL DEFAULT 0,
		mem_used   INTEGER NOT NULL DEFAULT 0,
		mem_total  INTEGER NOT NULL DEFAULT 0,
		disk_used  INTEGER NOT NULL DEFAULT 0,
		disk_total INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX samples_ts ON samples (ts);
	CREATE TABLE events (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		ts       TEXT NOT NULL,
		project  TEXT NOT NULL,
		workflow TEXT NOT NULL,
		issue_id TEXT NOT NULL,
		kind     TEXT NOT NULL,
		detail   TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX events_issue ON events (project, workflow, issue_id);
	CREATE TABLE audit_log (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		ts      TEXT NOT NULL,
		actor   TEXT NOT NULL,
		action  TEXT NOT NULL,
		project TEXT NOT NULL DEFAULT '',
		target  TEXT NOT NULL DEFAULT '',
		result  TEXT NOT NULL DEFAULT ''
	);`,
	`CREATE INDEX runs_cache_project_started ON runs_cache (project, workflow, started_at);`,
	`ALTER TABLE runs_cache ADD COLUMN workflow_kind TEXT NOT NULL DEFAULT '' CHECK (workflow_kind IN ('', 'issue', 'pr'));`,
	`CREATE TABLE config_backups (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		ts          TEXT NOT NULL,
		scope       TEXT NOT NULL,
		path        TEXT NOT NULL,
		backup_path TEXT NOT NULL,
		sha256      TEXT NOT NULL,
		actor       TEXT NOT NULL
	);
	CREATE INDEX config_backups_path ON config_backups (path, id);`,
	`CREATE TABLE github_cache (
		project    TEXT    NOT NULL,
		kind       TEXT    NOT NULL CHECK (kind IN ('pr', 'issue')),
		number     INTEGER NOT NULL,
		branch     TEXT    NOT NULL DEFAULT '',
		url        TEXT    NOT NULL DEFAULT '',
		state      TEXT    NOT NULL DEFAULT '' CHECK (state IN ('', 'open', 'closed', 'merged', 'unavailable')),
		title      TEXT    NOT NULL DEFAULT '',
		created_at TEXT    NOT NULL DEFAULT '',
		merged_at  TEXT    NOT NULL DEFAULT '',
		closed_at  TEXT    NOT NULL DEFAULT '',
		fetched_at TEXT    NOT NULL,
		etag       TEXT    NOT NULL DEFAULT '',
		PRIMARY KEY (project, kind, number)
	);
	CREATE INDEX github_cache_created ON github_cache (created_at);
	CREATE INDEX github_cache_merged ON github_cache (merged_at);
	CREATE TABLE github_links (
		project      TEXT    NOT NULL,
		issue_number INTEGER NOT NULL,
		pr_number    INTEGER NOT NULL,
		PRIMARY KEY (project, issue_number, pr_number)
	);`,
	`ALTER TABLE runs_cache ADD COLUMN workflow_file TEXT NOT NULL DEFAULT '';
	ALTER TABLE runs_cache ADD COLUMN template_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE runs_cache ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE runs_cache ADD COLUMN api_requests INTEGER;
	ALTER TABLE runs_cache ADD COLUMN handoff_reset INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE runs_cache ADD COLUMN review_metadata TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE github_cache ADD COLUMN details_at TEXT NOT NULL DEFAULT '';
	ALTER TABLE github_cache ADD COLUMN commit_count INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE github_cache ADD COLUMN commit_messages TEXT NOT NULL DEFAULT '';
	ALTER TABLE github_cache ADD COLUMN files_changed INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE github_cache ADD COLUMN additions INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE github_cache ADD COLUMN deletions INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE github_cache ADD COLUMN head_sha TEXT NOT NULL DEFAULT '';
	ALTER TABLE github_cache ADD COLUMN checks_ok INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE github_cache ADD COLUMN checks_failed INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE github_cache ADD COLUMN checks_pending INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE github_cache ADD COLUMN commits_etag TEXT NOT NULL DEFAULT '';
	ALTER TABLE github_cache ADD COLUMN checks_etag TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE runs_cache DROP COLUMN workspace;
	ALTER TABLE runs_cache DROP COLUMN template_id;
	ALTER TABLE runs_cache DROP COLUMN api_requests;
	ALTER TABLE runs_cache DROP COLUMN handoff_reset;
	ALTER TABLE runs_cache DROP COLUMN review_metadata;
	DELETE FROM events WHERE kind IN ('handoff_reset', 'handoff_reset_cleared');`,
	`CREATE TABLE question_items (
	project TEXT NOT NULL,
	number INTEGER NOT NULL,
	is_pr INTEGER NOT NULL DEFAULT 0,
	title TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	labels TEXT NOT NULL DEFAULT '',
	assignees TEXT NOT NULL DEFAULT '',
	first_labels TEXT NOT NULL DEFAULT '',
	issue_etag TEXT NOT NULL DEFAULT '',
	comments_etag TEXT NOT NULL DEFAULT '',
	scanned_at TEXT NOT NULL DEFAULT '',
	problems TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project, number)
	);
	CREATE TABLE questions (
	project TEXT NOT NULL,
	number INTEGER NOT NULL,
	qid TEXT NOT NULL,
	audience TEXT NOT NULL DEFAULT '',
	blocking INTEGER NOT NULL DEFAULT 0,
	resume TEXT NOT NULL DEFAULT '',
	question TEXT NOT NULL DEFAULT '',
	suggested TEXT NOT NULL DEFAULT '',
	asked_comment_url TEXT NOT NULL DEFAULT '',
	asked_at TEXT NOT NULL DEFAULT '',
	asked_count INTEGER NOT NULL DEFAULT 0,
	answer TEXT NOT NULL DEFAULT '',
	answered_by TEXT NOT NULL DEFAULT '',
	answer_comment_url TEXT NOT NULL DEFAULT '',
	answered_at TEXT NOT NULL DEFAULT '',
	ignored_answers TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project, number, qid)
	);`,
}

// Open opens dir/history.db, creating the directory and the database, and
// brings the schema up to date.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, FileName)
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var have int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&have); err != nil {
		return err
	}
	if have > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this build knows (%d)", have, len(migrations))
	}
	for i := have; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (?, ?)`,
			i+1, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Version is the schema version of the open database.
func (s *Store) Version() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	return v, err
}

// AuditEntry is one row of audit_log.
type AuditEntry struct {
	ID      int64  `json:"id"`
	TS      string `json:"ts"`
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Project string `json:"project"`
	Target  string `json:"target"`
	Result  string `json:"result"`
}

// AddAudit records one change event.
func (s *Store) AddAudit(ts time.Time, actor, action, project, target, result string) error {
	_, err := s.db.Exec(`INSERT INTO audit_log (ts, actor, action, project, target, result) VALUES (?,?,?,?,?,?)`,
		ts.UTC().Format(time.RFC3339), actor, action, project, target, result)
	return err
}

// Audit returns the newest limit rows, newest first.
func (s *Store) Audit(limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT id, ts, actor, action, project, target, result
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Action, &e.Project, &e.Target, &e.Result); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
