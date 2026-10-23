package store

import (
	"fmt"
	"strings"
)

// prWorkflow selects runs of workflows that work on pull requests. Every
// other run counts as an issue run, including those of unknown kind.
const prWorkflow = `workflow_kind = 'pr'`

// itemKey identifies an issue or pull request across projects.
const itemKey = `project || '#' || issue_id`

// UsageFilter bounds the runs an aggregate covers. Since and Until bound
// started_at as UTC timestamps of the form 2006-01-02T15:04:05Z, so days
// are UTC days. Empty fields match everything.
type UsageFilter struct {
	Project, Workflow, Model string
	Since, Until             string
}

func (f UsageFilter) where() (string, []any) {
	var conds []string
	var args []any
	for _, c := range []struct{ cond, v string }{
		{"project = ?", f.Project}, {"workflow = ?", f.Workflow}, {"model = ?", f.Model},
		{"started_at >= ?", f.Since}, {"started_at < ?", f.Until},
	} {
		if c.v != "" {
			conds = append(conds, c.cond)
			args = append(args, c.v)
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Stats are the figures for one group of runs. Token sums cover measured
// runs only; Unmeasured counts the rest. Issues and PRs are distinct
// counts within the group.
type Stats struct {
	Runs       int   `json:"runs"`
	Succeeded  int   `json:"succeeded"`
	Failed     int   `json:"failed"`
	Unmeasured int   `json:"unmeasured"`
	Issues     int   `json:"issues"`
	PRs        int   `json:"prs"`
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_tokens"`
	Total      int64 `json:"total_tokens"`
}

// Group is Stats for one value of the grouping column. Key is the project,
// "project workflow", the model or the UTC day. Project and Workflow are
// set for the groupings that have them.
type Group struct {
	Key      string `json:"key"`
	Project  string `json:"project,omitempty"`
	Workflow string `json:"workflow,omitempty"`
	Stats
}

// Groupings accepted by Usage.
var groupColumns = map[string]string{
	"project":  "project, '', project",
	"workflow": "project, workflow, project || ' ' || workflow",
	"model":    "'', '', model",
	"day":      "'', '', substr(started_at, 1, 10)",
	"all":      "'', '', ''",
}

// Usage groups the filtered runs by "project", "workflow", "model" or
// "day", or returns one group for all of them with by "all". Days and
// models come back in key order, the others by tokens, largest first.
func (s *Store) Usage(f UsageFilter, by string) ([]Group, error) {
	cols, ok := groupColumns[by]
	if !ok {
		return nil, fmt.Errorf("unknown grouping %q", by)
	}
	where, args := f.where()
	q := `SELECT ` + cols + `,
		COUNT(*), COALESCE(SUM(status = 'succeeded'), 0), COALESCE(SUM(status = 'failed'), 0),
		COALESCE(SUM(tokens_measured = 0), 0),
		COUNT(DISTINCT CASE WHEN NOT (` + prWorkflow + `) THEN ` + itemKey + ` END),
		COUNT(DISTINCT CASE WHEN ` + prWorkflow + ` THEN ` + itemKey + ` END),
		COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN input_tokens END), 0),
		COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN output_tokens END), 0),
		COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN cache_read_tokens END), 0),
		COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN total_tokens END), 0)
		FROM runs_cache` + where
	if by != "all" {
		q += ` GROUP BY 3`
	}
	switch by {
	case "day", "model":
		q += ` ORDER BY 3`
	case "project", "workflow":
		q += ` ORDER BY 13 DESC, 3`
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		var project, workflow, key string
		st := &g.Stats
		if err := rows.Scan(&project, &workflow, &key, &st.Runs, &st.Succeeded, &st.Failed, &st.Unmeasured,
			&st.Issues, &st.PRs, &st.Input, &st.Output, &st.CacheRead, &st.Total); err != nil {
			return nil, err
		}
		g.Project, g.Workflow, g.Key = project, workflow, key
		out = append(out, g)
	}
	return out, rows.Err()
}

// DayTokens is the tokens one project or workflow used on one UTC day.
type DayTokens struct {
	Day      string `json:"day"`
	Project  string `json:"project"`
	Workflow string `json:"workflow,omitempty"`
	Total    int64  `json:"total_tokens"`
}

// DailyTokens returns measured tokens per UTC day for each project, or
// for each project and workflow with by "workflow". Days without runs are
// absent.
func (s *Store) DailyTokens(f UsageFilter, by string) ([]DayTokens, error) {
	wf := "''"
	switch by {
	case "project":
	case "workflow":
		wf = "workflow"
	default:
		return nil, fmt.Errorf("unknown grouping %q", by)
	}
	where, args := f.where()
	rows, err := s.db.Query(`SELECT substr(started_at, 1, 10), project, `+wf+`,
		COALESCE(SUM(CASE WHEN tokens_measured = 1 THEN total_tokens END), 0)
		FROM runs_cache`+where+` GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayTokens
	for rows.Next() {
		var d DayTokens
		if err := rows.Scan(&d.Day, &d.Project, &d.Workflow, &d.Total); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ItemTokens are the tokens of measured runs summed over every stage and
// retry of each issue or pull request, with the number of each.
type ItemTokens struct {
	Issues, PRs           int
	IssueTokens, PRTokens int64
}

// ItemTokens returns the per-issue and per-PR token totals.
func (s *Store) ItemTokens(f UsageFilter) (ItemTokens, error) {
	where, args := f.where()
	if where == "" {
		where = " WHERE tokens_measured = 1"
	} else {
		where += " AND tokens_measured = 1"
	}
	var t ItemTokens
	err := s.db.QueryRow(`SELECT
		COUNT(DISTINCT CASE WHEN NOT (`+prWorkflow+`) THEN `+itemKey+` END),
		COUNT(DISTINCT CASE WHEN `+prWorkflow+` THEN `+itemKey+` END),
		COALESCE(SUM(CASE WHEN NOT (`+prWorkflow+`) THEN total_tokens END), 0),
		COALESCE(SUM(CASE WHEN `+prWorkflow+` THEN total_tokens END), 0)
		FROM runs_cache`+where, args...).Scan(&t.Issues, &t.PRs, &t.IssueTokens, &t.PRTokens)
	return t, err
}

// TopItem is one issue or pull request with its tokens over all stages
// and retries.
type TopItem struct {
	Project           string `json:"project"`
	IssueID           string `json:"issue_id"`
	DisplayIdentifier string `json:"display_identifier"`
	Runs              int    `json:"runs"`
	Total             int64  `json:"total_tokens"`
}

// TopItems returns the issues and pull requests that used the most
// measured tokens, largest first.
func (s *Store) TopItems(f UsageFilter, limit int) ([]TopItem, error) {
	where, args := f.where()
	if where == "" {
		where = " WHERE tokens_measured = 1"
	} else {
		where += " AND tokens_measured = 1"
	}
	rows, err := s.db.Query(`SELECT project, issue_id, MAX(display_identifier), COUNT(*), SUM(total_tokens)
		FROM runs_cache`+where+` GROUP BY project, issue_id
		ORDER BY SUM(total_tokens) DESC, project, issue_id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopItem
	for rows.Next() {
		var t TopItem
		if err := rows.Scan(&t.Project, &t.IssueID, &t.DisplayIdentifier, &t.Runs, &t.Total); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
