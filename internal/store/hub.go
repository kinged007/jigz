package store

// HubRuns returns up to limit stored runs that the hub has not accepted
// yet: those past the hub cursor of their workflow. Workflows take turns:
// the batch holds the oldest pending run of each workflow, then the
// second oldest, and so on, so a workflow whose oldest run is never
// accepted does not fill every batch. Within a workflow, runs are in run
// id order and form a prefix of its pending runs.
func (s *Store) HubRuns(limit int) ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runColumns+` FROM (
		SELECT *, ROW_NUMBER() OVER (PARTITION BY project, workflow ORDER BY run_id) AS turn
		FROM runs_cache
		WHERE run_id > COALESCE((SELECT c.run_id FROM hub_cursor c
			WHERE c.project = runs_cache.project AND c.workflow = runs_cache.workflow), 0))
		ORDER BY turn, project, workflow LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetHubCursor records that the hub accepted every run of the workflow up
// to runID. The cursor never moves back.
func (s *Store) SetHubCursor(project, workflow string, runID int64) error {
	_, err := s.db.Exec(`INSERT INTO hub_cursor (project, workflow, run_id) VALUES (?, ?, ?)
		ON CONFLICT (project, workflow) DO UPDATE SET run_id = MAX(run_id, excluded.run_id)`,
		project, workflow, runID)
	return err
}
