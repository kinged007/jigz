package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// Item is one issue or pull request with its normalized timeline.
type Item struct {
	ID          string   `json:"id"`
	Number      int      `json:"number"`
	Kind        string   `json:"kind"` // "issue" or "pr"
	Title       string   `json:"title"`
	Body        string   `json:"body,omitempty"`
	State       string   `json:"state"`
	StateReason string   `json:"stateReason,omitempty"`
	Author      string   `json:"author"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
	ClosedAt    string   `json:"closedAt,omitempty"`
	MergedAt    string   `json:"mergedAt,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Assignees   []string `json:"assignees,omitempty"`
	Milestone   string   `json:"milestone,omitempty"`
	Comments    int      `json:"comments"`
	HeadRef     string   `json:"headRef,omitempty"`
	BaseRef     string   `json:"baseRef,omitempty"`
	MergeCommit string   `json:"mergeCommit,omitempty"`
	Additions   int      `json:"additions,omitempty"`
	Deletions   int      `json:"deletions,omitempty"`
	Closes      []int    `json:"closes,omitempty"`
	Events      []Event  `json:"events"`
	// TimelineCursor is set while the timeline has more pages to fetch.
	TimelineCursor string `json:"timelineCursor,omitempty"`
}

// Event is one timeline entry of an item.
type Event struct {
	Type  string `json:"type"` // labeled, unlabeled, closed, reopened, merged, xref, comment, assigned, review, duplicate
	At    string `json:"at"`
	Actor string `json:"actor,omitempty"`
	Label string `json:"label,omitempty"`
	Ref   int    `json:"ref,omitempty"`   // referenced issue/PR number
	Close bool   `json:"close,omitempty"` // cross reference will close the target
	Oid   string `json:"oid,omitempty"`
	State string `json:"state,omitempty"` // review state
}

// Store keeps fetched GitHub data in SQLite so a sync only fetches what changed.
type Store struct{ db *sql.DB }

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS items (
  kind TEXT NOT NULL,
  number INTEGER NOT NULL,
  state TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  fetched_at TEXT NOT NULL,
  timeline_complete INTEGER NOT NULL,
  data TEXT NOT NULL,
  PRIMARY KEY (kind, number)
);`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init store: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) getMeta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) setMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// storedUpdatedAt returns the updated_at of a stored item and whether its
// timeline is complete; ok is false when the item is not stored.
func (s *Store) storedUpdatedAt(kind string, number int) (updatedAt string, complete, ok bool, err error) {
	var c int
	err = s.db.QueryRow(`SELECT updated_at, timeline_complete FROM items WHERE kind = ? AND number = ?`, kind, number).Scan(&updatedAt, &c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	return updatedAt, c == 1, err == nil, err
}

func (s *Store) putItem(it *Item, fetchedAt string) error {
	data, err := json.Marshal(it)
	if err != nil {
		return err
	}
	complete := 0
	if it.TimelineCursor == "" {
		complete = 1
	}
	_, err = s.db.Exec(`INSERT INTO items (kind, number, state, updated_at, fetched_at, timeline_complete, data)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, number) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at,
  fetched_at = excluded.fetched_at, timeline_complete = excluded.timeline_complete, data = excluded.data`,
		it.Kind, it.Number, it.State, it.UpdatedAt, fetchedAt, complete, string(data))
	return err
}

func (s *Store) items(where string, args ...any) ([]*Item, error) {
	q := `SELECT data FROM items`
	if where != "" {
		q += " WHERE " + where
	}
	rows, err := s.db.Query(q+" ORDER BY kind, number", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		it := &Item{}
		if err := json.Unmarshal([]byte(data), it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
