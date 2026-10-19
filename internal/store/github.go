package store

import (
	"strconv"
	"strings"
)

// Kinds of github_cache rows. A "pr" row holds a pull request. An "issue"
// row records that the issue's timeline was read, with its ETag; the pull
// requests found there are in github_links.
const (
	GitHubPR    = "pr"
	GitHubIssue = "issue"
)

// Pull request states. Unavailable marks a pull request or issue GitHub
// would not return.
const (
	PROpen        = "open"
	PRClosed      = "closed"
	PRMerged      = "merged"
	PRUnavailable = "unavailable"
)

// PR is a pull request as cached from GitHub.
type PR struct {
	Project   string `json:"project"`
	Number    int    `json:"number"`
	Branch    string `json:"branch,omitempty"`
	URL       string `json:"url"`
	State     string `json:"state"`
	Title     string `json:"title,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	MergedAt  string `json:"merged_at,omitempty"`
	ClosedAt  string `json:"closed_at,omitempty"`
	FetchedAt string `json:"fetched_at"`
	ETag      string `json:"-"`
	// Details is nil until the optional GitHub detail was fetched.
	Details *PRDetails `json:"details,omitempty"`
}

// Bounds of the stored pull request detail.
const (
	MaxCommitMessages = 3
	MaxMessageRunes   = 120
)

// PRDetails is the optional extra GitHub detail of a pull request. Checks
// are the check runs of the head commit.
type PRDetails struct {
	At            string   `json:"fetched_at"`
	Commits       int      `json:"commits"`
	Messages      []string `json:"latest_commit_messages,omitempty"`
	Files         int      `json:"files_changed"`
	Additions     int      `json:"additions"`
	Deletions     int      `json:"deletions"`
	HeadSHA       string   `json:"head_sha,omitempty"`
	ChecksOK      int      `json:"checks_success"`
	ChecksFailed  int      `json:"checks_failure"`
	ChecksPending int      `json:"checks_pending"`
	CommitsETag   string   `json:"-"`
	ChecksETag    string   `json:"-"`
}

// GitHubRow is one github_cache row as the enrichment pass reads it back.
type GitHubRow struct {
	Kind      string
	Number    int
	State     string
	FetchedAt string
	ETag      string
	Details   PRDetails
}

// SaveDetails stores the detail of a cached pull request, cutting the
// commit messages to their bounds.
func (s *Store) SaveDetails(project string, number int, d PRDetails) error {
	var msgs []string
	for _, m := range d.Messages[:min(len(d.Messages), MaxCommitMessages)] {
		if r := []rune(m); len(r) > MaxMessageRunes {
			m = string(r[:MaxMessageRunes-1]) + "…"
		}
		msgs = append(msgs, m)
	}
	_, err := s.db.Exec(`UPDATE github_cache SET details_at=?, commit_count=?, commit_messages=?, files_changed=?,
		additions=?, deletions=?, head_sha=?, checks_ok=?, checks_failed=?, checks_pending=?, commits_etag=?, checks_etag=?
		WHERE project = ? AND kind = 'pr' AND number = ?`,
		d.At, d.Commits, strings.Join(msgs, "\n"), d.Files, d.Additions, d.Deletions, d.HeadSHA, d.ChecksOK,
		d.ChecksFailed, d.ChecksPending, d.CommitsETag, d.ChecksETag, project, number)
	return err
}

const detailColumns = `c.details_at, c.commit_count, c.commit_messages, c.files_changed, c.additions, c.deletions,
	c.head_sha, c.checks_ok, c.checks_failed, c.checks_pending, c.commits_etag, c.checks_etag`

func scanDetails(sc interface{ Scan(...any) error }, p *PRDetails, msgs *string, rest ...any) error {
	dst := append(rest, &p.At, &p.Commits, msgs, &p.Files, &p.Additions, &p.Deletions, &p.HeadSHA,
		&p.ChecksOK, &p.ChecksFailed, &p.ChecksPending, &p.CommitsETag, &p.ChecksETag)
	if err := sc.Scan(dst...); err != nil {
		return err
	}
	if *msgs != "" {
		p.Messages = strings.Split(*msgs, "\n")
	}
	return nil
}

// SavePR stores or replaces a pull request.
func (s *Store) SavePR(p PR) error {
	_, err := s.db.Exec(`INSERT INTO github_cache (project, kind, number, branch, url, state, title,
		created_at, merged_at, closed_at, fetched_at, etag) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (project, kind, number) DO UPDATE SET branch=excluded.branch, url=excluded.url,
		state=excluded.state, title=excluded.title, created_at=excluded.created_at,
		merged_at=excluded.merged_at, closed_at=excluded.closed_at, fetched_at=excluded.fetched_at,
		etag=excluded.etag`,
		p.Project, GitHubPR, p.Number, p.Branch, p.URL, p.State, p.Title, p.CreatedAt, p.MergedAt, p.ClosedAt,
		p.FetchedAt, p.ETag)
	return err
}

// SaveUnavailable records that GitHub would not return a pull request or
// issue, so it is retried later rather than on every pass.
func (s *Store) SaveUnavailable(project, kind string, number int, fetchedAt string) error {
	_, err := s.db.Exec(`INSERT INTO github_cache (project, kind, number, state, fetched_at)
		VALUES (?,?,?,?,?) ON CONFLICT (project, kind, number) DO UPDATE SET state=excluded.state,
		fetched_at=excluded.fetched_at, etag=''`, project, kind, number, PRUnavailable, fetchedAt)
	return err
}

// TouchGitHub records that a cached row was confirmed unchanged.
func (s *Store) TouchGitHub(project, kind string, number int, fetchedAt string) error {
	_, err := s.db.Exec(`UPDATE github_cache SET fetched_at = ? WHERE project = ? AND kind = ? AND number = ?`,
		fetchedAt, project, kind, number)
	return err
}

// SaveIssueLinks records that an issue's timeline was read and replaces
// the pull requests linked to it.
func (s *Store) SaveIssueLinks(project string, issue int, etag, fetchedAt string, prs []int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO github_cache (project, kind, number, fetched_at, etag) VALUES (?,?,?,?,?)
		ON CONFLICT (project, kind, number) DO UPDATE SET state='', fetched_at=excluded.fetched_at, etag=excluded.etag`,
		project, GitHubIssue, issue, fetchedAt, etag); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM github_links WHERE project = ? AND issue_number = ?`, project, issue); err != nil {
		return err
	}
	for _, n := range prs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO github_links (project, issue_number, pr_number) VALUES (?,?,?)`,
			project, issue, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RunItem is an issue or pull request number central has runs for.
type RunItem struct {
	PR     bool
	Number int
	// Latest is the newest started_at of its runs.
	Latest string
}

// RunItems lists the numbers a project has runs for, from workflows of the
// matching kind. Identifiers that are not numbers are left out.
func (s *Store) RunItems(project string) ([]RunItem, error) {
	rows, err := s.db.Query(`SELECT workflow_kind = 'pr', CAST(issue_id AS INTEGER) AS n, MAX(started_at)
		FROM runs_cache WHERE project = ? GROUP BY 1, 2 HAVING n > 0`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunItem
	for rows.Next() {
		var it RunItem
		if err := rows.Scan(&it.PR, &it.Number, &it.Latest); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GitHubRows returns the cached rows of a project, keyed by kind and number
// through GitHubKey.
func (s *Store) GitHubRows(project string) (map[string]GitHubRow, error) {
	rows, err := s.db.Query(`SELECT c.kind, c.number, c.state, c.fetched_at, c.etag, `+detailColumns+`
		FROM github_cache c WHERE c.project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]GitHubRow{}
	for rows.Next() {
		var r GitHubRow
		var msgs string
		if err := scanDetails(rows, &r.Details, &msgs, &r.Kind, &r.Number, &r.State, &r.FetchedAt, &r.ETag); err != nil {
			return nil, err
		}
		out[GitHubKey(r.Kind, r.Number)] = r
	}
	return out, rows.Err()
}

// GitHubKey is the key of a row in the map GitHubRows returns.
func GitHubKey(kind string, number int) string {
	return kind + "#" + strconv.Itoa(number)
}

// IssueLinks returns the pull requests linked to each issue of a project.
func (s *Store) IssueLinks(project string) (map[int][]int, error) {
	rows, err := s.db.Query(`SELECT issue_number, pr_number FROM github_links WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int][]int{}
	for rows.Next() {
		var issue, pr int
		if err := rows.Scan(&issue, &pr); err != nil {
			return nil, err
		}
		out[issue] = append(out[issue], pr)
	}
	return out, rows.Err()
}

const prColumns = `c.project, c.number, c.branch, c.url, c.state, c.title, c.created_at, c.merged_at, c.closed_at, c.fetched_at, ` + detailColumns

// RunPRs returns the cached pull requests of a run's issue or pull
// request, oldest first. A PR workflow run is for the pull request itself;
// an issue workflow run is for the pull requests linked to the issue.
func (s *Store) RunPRs(r Run) ([]PR, error) {
	q := `SELECT ` + prColumns + ` FROM github_links l JOIN github_cache c
		ON c.project = l.project AND c.kind = 'pr' AND c.number = l.pr_number
		WHERE l.project = ? AND l.issue_number = CAST(? AS INTEGER) AND c.state != 'unavailable'`
	if r.WorkflowKind == KindPR {
		q = `SELECT ` + prColumns + ` FROM github_cache c
			WHERE c.project = ? AND c.kind = 'pr' AND c.number = CAST(? AS INTEGER) AND c.state != 'unavailable'`
	}
	rows, err := s.db.Query(q+` ORDER BY c.number`, r.Project, r.IssueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PR
	for rows.Next() {
		var p PR
		var d PRDetails
		var msgs string
		if err := scanDetails(rows, &d, &msgs, &p.Project, &p.Number, &p.Branch, &p.URL, &p.State, &p.Title,
			&p.CreatedAt, &p.MergedAt, &p.ClosedAt, &p.FetchedAt); err != nil {
			return nil, err
		}
		if d.At != "" {
			p.Details = &d
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// JourneyRuns returns the runs of pull request workflows for the pull
// requests linked to an issue, oldest first.
func (s *Store) JourneyRuns(project, issue string) ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runColumns+` FROM runs_cache
		WHERE project = ? AND workflow_kind = 'pr' AND CAST(issue_id AS INTEGER) IN
			(SELECT pr_number FROM github_links WHERE project = ? AND issue_number = CAST(? AS INTEGER))
		ORDER BY started_at, id LIMIT 200`, project, project, issue)
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

// PRDays counts cached pull requests opened and merged per UTC day, by
// created_at and merged_at within [since, until). Empty bounds are open.
// Only pull requests central found through its runs are cached, so these
// are not counts for the whole repository.
func (s *Store) PRDays(project, since, until string) (opened, merged map[string]int, err error) {
	opened, merged = map[string]int{}, map[string]int{}
	for col, dst := range map[string]map[string]int{"created_at": opened, "merged_at": merged} {
		q := `SELECT substr(` + col + `, 1, 10), COUNT(*) FROM github_cache WHERE kind = 'pr' AND ` + col + ` != ''`
		var args []any
		for _, c := range []struct{ cond, v string }{
			{"project = ?", project}, {col + " >= ?", since}, {col + " < ?", until},
		} {
			if c.v != "" {
				q += " AND " + c.cond
				args = append(args, c.v)
			}
		}
		rows, err := s.db.Query(q+` GROUP BY 1`, args...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var day string
			var n int
			if err := rows.Scan(&day, &n); err != nil {
				rows.Close()
				return nil, nil, err
			}
			dst[day] = n
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	return opened, merged, nil
}
