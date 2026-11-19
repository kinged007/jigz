package store

import "time"

// Sample is one reading taken by the collector. Scope "loop" rows carry
// running and retrying for the loop named by Key; scope "host" rows carry
// the machine figures.
type Sample struct {
	TS        string  `json:"ts"`
	Scope     string  `json:"scope"`
	Key       string  `json:"key"`
	Running   int     `json:"running"`
	Retrying  int     `json:"retrying"`
	CPUPct    float64 `json:"cpu_pct"`
	MemUsed   int64   `json:"mem_used"`
	MemTotal  int64   `json:"mem_total"`
	DiskUsed  int64   `json:"disk_used"`
	DiskTotal int64   `json:"disk_total"`
}

// AddSamples stores samples.
func (s *Store) AddSamples(samples []Sample) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, x := range samples {
		if _, err := tx.Exec(`INSERT INTO samples (ts, scope, key, running, retrying, cpu_pct,
			mem_used, mem_total, disk_used, disk_total) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			x.TS, x.Scope, x.Key, x.Running, x.Retrying, x.CPUPct, x.MemUsed, x.MemTotal,
			x.DiskUsed, x.DiskTotal); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneSamples deletes samples older than SampleRetention.
func (s *Store) PruneSamples(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM samples WHERE ts < ?`, now.Add(-SampleRetention).UTC().Format(time.RFC3339))
	return err
}

// CountSamples is the number of stored samples.
func (s *Store) CountSamples() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM samples`).Scan(&n)
	return n, err
}

// Event is a state change Jigz saw in a workflow database.
type Event struct {
	TS       string `json:"ts"`
	Project  string `json:"project"`
	Workflow string `json:"workflow"`
	IssueID  string `json:"issue_id"`
	Kind     string `json:"kind"`
	Detail   string `json:"detail,omitempty"`
}

// AddEvents stores events.
func (s *Store) AddEvents(events []Event) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		if _, err := tx.Exec(`INSERT INTO events (ts, project, workflow, issue_id, kind, detail)
			VALUES (?,?,?,?,?,?)`, e.TS, e.Project, e.Workflow, e.IssueID, e.Kind, e.Detail); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IssueEvents returns the events recorded for one issue, oldest first.
func (s *Store) IssueEvents(project, workflow, issueID string) ([]Event, error) {
	rows, err := s.db.Query(`SELECT ts, project, workflow, issue_id, kind, detail FROM events
		WHERE project = ? AND workflow = ? AND issue_id = ? ORDER BY id`, project, workflow, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.TS, &e.Project, &e.Workflow, &e.IssueID, &e.Kind, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
