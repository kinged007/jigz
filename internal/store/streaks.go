package store

// Streak is an issue whose latest attempts all failed.
type Streak struct {
	Project, Workflow, IssueID, Identifier string
	Count                                  int
	LastError                              string
}

// FailureStreaks lists issues whose most recent attempts, among runs
// started since the given timestamp, are at least atLeast consecutive
// failures. A later success or cancellation ends a streak.
func (s *Store) FailureStreaks(atLeast int, since string) ([]Streak, error) {
	rows, err := s.db.Query(`SELECT project, workflow, issue_id, display_identifier, status, error
		FROM runs_cache WHERE started_at >= ?
		ORDER BY project, workflow, issue_id, started_at DESC, run_id DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Streak
	var cur Streak
	counting := false
	for rows.Next() {
		var project, workflow, issue, ident, status, errText string
		if err := rows.Scan(&project, &workflow, &issue, &ident, &status, &errText); err != nil {
			return nil, err
		}
		if cur.Project != project || cur.Workflow != workflow || cur.IssueID != issue {
			if cur.Count >= atLeast {
				out = append(out, cur)
			}
			cur = Streak{Project: project, Workflow: workflow, IssueID: issue, Identifier: ident, LastError: errText}
			counting = true
		}
		if counting && status == "failed" {
			cur.Count++
		} else {
			counting = false
		}
	}
	if cur.Count >= atLeast {
		out = append(out, cur)
	}
	return out, rows.Err()
}
