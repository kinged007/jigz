package store

import "database/sql"

// IssuePark is one item parked Jigz-side after a non-retryable failure.
// The engine's parked_issues and retry_entries tables are read-only to
// the dashboard, so the park lives here in history.db: one active row
// per issue, cleared by the issue restart action.
type IssuePark struct {
	Project  string `json:"project"`
	Workflow string `json:"workflow"`
	IssueID  string `json:"issue_id"`
	Class    string `json:"class"`
	Fix      string `json:"fix"`
	Reason   string `json:"reason"`
	TS       string `json:"ts"`
}

// ParkEventKind is the run event recorded once with a park, carrying
// the same class and fix the dashboard alert shows.
const ParkEventKind = "nonretryable_parked"

// ParkIssue records the park, returning false when the issue already
// has an active park: recording is idempotent, so repeated passes add
// no second notice. A cleared park parks again when the failure recurs.
func (s *Store) ParkIssue(p IssuePark) (bool, error) {
	res, err := s.db.Exec(`INSERT INTO issue_parks (project, workflow, issue_id, class, fix, reason, ts, cleared)
		VALUES (?,?,?,?,?,?,?,0)
		ON CONFLICT (project, workflow, issue_id) DO UPDATE
		SET class = excluded.class, fix = excluded.fix, reason = excluded.reason, ts = excluded.ts, cleared = 0
		WHERE issue_parks.cleared = 1`, p.Project, p.Workflow, p.IssueID, p.Class, p.Fix, p.Reason, p.TS)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ActivePark returns the issue's park, or false when it has none.
func (s *Store) ActivePark(project, workflow, issueID string) (IssuePark, bool, error) {
	var p IssuePark
	err := s.db.QueryRow(`SELECT project, workflow, issue_id, class, fix, reason, ts FROM issue_parks
		WHERE project = ? AND workflow = ? AND issue_id = ? AND cleared = 0`,
		project, workflow, issueID).Scan(&p.Project, &p.Workflow, &p.IssueID, &p.Class, &p.Fix, &p.Reason, &p.TS)
	if err != nil {
		if err == sql.ErrNoRows {
			return IssuePark{}, false, nil
		}
		return IssuePark{}, false, err
	}
	return p, true, nil
}

// ActiveParks lists every uncleared park, oldest first, for alerts.
func (s *Store) ActiveParks() ([]IssuePark, error) {
	rows, err := s.db.Query(`SELECT project, workflow, issue_id, class, fix, reason, ts FROM issue_parks
		WHERE cleared = 0 ORDER BY ts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IssuePark
	for rows.Next() {
		var p IssuePark
		if err := rows.Scan(&p.Project, &p.Workflow, &p.IssueID, &p.Class, &p.Fix, &p.Reason, &p.TS); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClearIssuePark clears the issue's park in one workflow, returning
// whether a park was active. Clearing twice is a no-op.
func (s *Store) ClearIssuePark(project, workflow, issueID string) (bool, error) {
	res, err := s.db.Exec(`UPDATE issue_parks SET cleared = 1
		WHERE project = ? AND workflow = ? AND issue_id = ? AND cleared = 0`,
		project, workflow, issueID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClearIssueParks clears the issue's parks in every workflow of the
// project, returning how many were active.
func (s *Store) ClearIssueParks(project, issueID string) (int64, error) {
	res, err := s.db.Exec(`UPDATE issue_parks SET cleared = 1
		WHERE project = ? AND issue_id = ? AND cleared = 0`, project, issueID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
