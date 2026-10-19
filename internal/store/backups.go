package store

import "time"

// Backup is one saved copy of a config file, taken before an edit replaced it.
// Scope is "central" or the project slug.
type Backup struct {
	ID         int64  `json:"id"`
	TS         string `json:"ts"`
	Scope      string `json:"scope"`
	Path       string `json:"path"`
	BackupPath string `json:"backup_path"`
	SHA256     string `json:"sha256"`
	Actor      string `json:"actor"`
}

// AddBackup records a backup and returns its id.
func (s *Store) AddBackup(ts time.Time, b Backup) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO config_backups (ts, scope, path, backup_path, sha256, actor) VALUES (?,?,?,?,?,?)`,
		ts.UTC().Format(time.RFC3339), b.Scope, b.Path, b.BackupPath, b.SHA256, b.Actor)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Backup returns one backup by id.
func (s *Store) Backup(id int64) (Backup, error) {
	var b Backup
	err := s.db.QueryRow(`SELECT id, ts, scope, path, backup_path, sha256, actor FROM config_backups WHERE id = ?`, id).
		Scan(&b.ID, &b.TS, &b.Scope, &b.Path, &b.BackupPath, &b.SHA256, &b.Actor)
	return b, err
}

// Backups lists backups newest first. An empty path lists every file's.
func (s *Store) Backups(path string, limit int) ([]Backup, error) {
	rows, err := s.db.Query(`SELECT id, ts, scope, path, backup_path, sha256, actor FROM config_backups
		WHERE ? = '' OR path = ? ORDER BY id DESC LIMIT ?`, path, path, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		var b Backup
		if err := rows.Scan(&b.ID, &b.TS, &b.Scope, &b.Path, &b.BackupPath, &b.SHA256, &b.Actor); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PruneBackups deletes all but the newest keep backups of one file and
// returns the backup paths of the rows it deleted.
func (s *Store) PruneBackups(path string, keep int) ([]string, error) {
	rows, err := s.db.Query(`SELECT id, backup_path FROM config_backups WHERE path = ? ORDER BY id DESC LIMIT -1 OFFSET ?`, path, keep)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var files []string
	for rows.Next() {
		var id int64
		var f string
		if err := rows.Scan(&id, &f); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		files = append(files, f)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.db.Exec(`DELETE FROM config_backups WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return files, nil
}
