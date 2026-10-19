package store

import (
	"slices"
	"strings"
)

// Model sources: whether the model name was read from a session or taken
// from the workflow configuration.
const (
	ModelObserved   = "observed"
	ModelConfigured = "configured"
)

// Workflow kinds, from the tracker kind in the workflow file. An empty kind
// means the file was unreadable or names another tracker; such runs are
// counted as issue runs.
const (
	KindIssue = "issue"
	KindPR    = "pr"
)

// Run is one finished run copied from a workflow database.
type Run struct {
	// Cursor is the row's position in runs_cache, used for paging. A
	// run id alone is ambiguous across workflows.
	Cursor            int64  `json:"-"`
	Project           string `json:"project"`
	Workflow          string `json:"workflow"`
	ID                int64  `json:"id"`
	IssueID           string `json:"issue_id"`
	DisplayIdentifier string `json:"display_identifier"`
	Status            string `json:"status"`
	StartedAt         string `json:"started_at"`
	CompletedAt       string `json:"completed_at"`
	Attempt           int    `json:"attempt"`
	TurnsCompleted    int    `json:"turns_completed"`
	Error             string `json:"error,omitempty"`
	AgentAdapter      string `json:"agent_adapter"`
	Model             string `json:"model,omitempty"`
	ModelSource       string `json:"model_source,omitempty"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
	CacheReadTokens   int64  `json:"cache_read_tokens"`
	TokensMeasured    bool   `json:"tokens_measured"`
	Workspace         string `json:"workspace,omitempty"`
	WorkflowKind      string `json:"workflow_kind,omitempty"`
	WorkflowFile      string `json:"workflow_file,omitempty"`
	TemplateID        string `json:"template_id,omitempty"`
	// SessionID and APIRequests are the issue's latest session as it was
	// when the run was copied. APIRequests is nil when the session did not
	// measure them.
	SessionID    string `json:"session_id,omitempty"`
	APIRequests  *int64 `json:"api_requests,omitempty"`
	HandoffReset bool   `json:"handoff_reset,omitempty"`
	// ReviewMetadata is reserved: the engine leaves it empty today.
	ReviewMetadata string `json:"review_metadata,omitempty"`
	// PRs is filled by the API when GitHub enrichment is on.
	PRs []PR `json:"pull_requests,omitempty"`
}

const runColumns = `id, project, workflow, run_id, issue_id, display_identifier, status, started_at,
	completed_at, attempt, turns_completed, error, agent_adapter, model, model_source,
	input_tokens, output_tokens, total_tokens, cache_read_tokens, tokens_measured, workspace, workflow_kind,
	workflow_file, template_id, session_id, api_requests, handoff_reset, review_metadata`

// AddRuns inserts runs, replacing any already stored under the same
// project, workflow and run id.
func (s *Store) AddRuns(runs []Run) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range runs {
		_, err := tx.Exec(`INSERT INTO runs_cache (project, workflow, run_id, issue_id, display_identifier,
			status, started_at, completed_at, attempt, turns_completed, error, agent_adapter, model,
			model_source, input_tokens, output_tokens, total_tokens, cache_read_tokens, tokens_measured, workspace, workflow_kind,
			workflow_file, template_id, session_id, api_requests, handoff_reset, review_metadata)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (project, workflow, run_id) DO UPDATE SET
			issue_id=excluded.issue_id, display_identifier=excluded.display_identifier,
			status=excluded.status, started_at=excluded.started_at, completed_at=excluded.completed_at,
			attempt=excluded.attempt, turns_completed=excluded.turns_completed, error=excluded.error,
			agent_adapter=excluded.agent_adapter, model=excluded.model, model_source=excluded.model_source,
			input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
			total_tokens=excluded.total_tokens, cache_read_tokens=excluded.cache_read_tokens,
			tokens_measured=excluded.tokens_measured, workspace=excluded.workspace,
			workflow_kind=CASE WHEN excluded.workflow_kind = '' THEN workflow_kind ELSE excluded.workflow_kind END,
			workflow_file=CASE WHEN excluded.workflow_file = '' THEN workflow_file ELSE excluded.workflow_file END,
			template_id=CASE WHEN excluded.template_id = '' THEN template_id ELSE excluded.template_id END,
			session_id=CASE WHEN excluded.session_id = '' THEN session_id ELSE excluded.session_id END,
			api_requests=COALESCE(excluded.api_requests, api_requests),
			handoff_reset=MAX(handoff_reset, excluded.handoff_reset),
			review_metadata=CASE WHEN excluded.review_metadata = '' THEN review_metadata ELSE excluded.review_metadata END`,
			r.Project, r.Workflow, r.ID, r.IssueID, r.DisplayIdentifier, r.Status, r.StartedAt,
			r.CompletedAt, r.Attempt, r.TurnsCompleted, r.Error, r.AgentAdapter, r.Model, r.ModelSource,
			r.InputTokens, r.OutputTokens, r.TotalTokens, r.CacheReadTokens, r.TokensMeasured, r.Workspace, r.WorkflowKind,
			r.WorkflowFile, r.TemplateID, r.SessionID, r.APIRequests, r.HandoffReset, r.ReviewMetadata)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetWorkflowKind records the kind of a workflow on its stored runs, which
// fills in runs copied before the kind was known.
func (s *Store) SetWorkflowKind(project, workflow, kind string) error {
	_, err := s.db.Exec(`UPDATE runs_cache SET workflow_kind = ? WHERE project = ? AND workflow = ? AND workflow_kind != ?`,
		kind, project, workflow, kind)
	return err
}

// FillRunFiles sets workflow_file and template_id on stored runs that have
// none, matching runs by id. It fills in runs copied before those columns
// existed.
func (s *Store) FillRunFiles(project, workflow string, runs []Run) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range runs {
		if _, err := tx.Exec(`UPDATE runs_cache SET workflow_file = ?, template_id = ?
			WHERE project = ? AND workflow = ? AND run_id = ? AND workflow_file = ''`,
			r.WorkflowFile, r.TemplateID, project, workflow, r.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NeedsRunFiles reports whether a workflow has stored runs without a
// workflow file.
func (s *Store) NeedsRunFiles(project, workflow string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM runs_cache WHERE project = ? AND workflow = ? AND workflow_file = '')`,
		project, workflow).Scan(&n)
	return n == 1, err
}

// LastRunID is the highest run id stored for a workflow, 0 when none.
func (s *Store) LastRunID(project, workflow string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(run_id), 0) FROM runs_cache WHERE project = ? AND workflow = ?`,
		project, workflow).Scan(&id)
	return id, err
}

// RunFilter selects runs. Empty fields match everything.
type RunFilter struct {
	Project, Workflow, State, Model, Adapter string
	// Since and Until bound started_at, as RFC 3339 timestamps.
	Since, Until string
	// Q matches part of the issue identifier.
	Q string
	// Cursor is the Cursor of the last run of the previous page. After is
	// the Cursor of the first run of the following page and selects the
	// runs just newer than it. Cursor and After are exclusive.
	Cursor, After int64
	Limit         int
}

// DefaultLimit and MaxLimit bound a page of runs.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// where is the WHERE clause and arguments for the filter, without paging.
func (f RunFilter) where() (string, []any) {
	var where []string
	var args []any
	add := func(cond string, v ...any) {
		where = append(where, cond)
		args = append(args, v...)
	}
	if f.Project != "" {
		add("project = ?", f.Project)
	}
	if f.Workflow != "" {
		add("workflow = ?", f.Workflow)
	}
	if f.State != "" {
		add("status = ?", f.State)
	}
	if f.Model != "" {
		add("model = ?", f.Model)
	}
	if f.Adapter != "" {
		add("agent_adapter = ?", f.Adapter)
	}
	if f.Since != "" {
		add("started_at >= ?", f.Since)
	}
	if f.Until != "" {
		add("started_at < ?", f.Until)
	}
	if f.Q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Q) + "%"
		add(`(display_identifier LIKE ? ESCAPE '\' OR issue_id LIKE ? ESCAPE '\')`, like, like)
	}
	if f.Cursor > 0 {
		add(`(started_at, id) < (SELECT started_at, id FROM runs_cache WHERE id = ?)`, f.Cursor)
	}
	if f.After > 0 {
		add(`(started_at, id) > (SELECT started_at, id FROM runs_cache WHERE id = ?)`, f.After)
	}
	if len(where) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(where, " AND "), args
}

// CountRuns returns how many runs match the filter, ignoring Limit.
func (s *Store) CountRuns(f RunFilter) (int, error) {
	w, args := f.where()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM runs_cache`+w, args...).Scan(&n)
	return n, err
}

// Runs returns runs newest first.
func (s *Store) Runs(f RunFilter) ([]Run, error) {
	w, args := f.where()
	q := `SELECT ` + runColumns + ` FROM runs_cache` + w
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	order := "DESC"
	if f.After > 0 {
		order = "ASC"
	}
	q += ` ORDER BY started_at ` + order + `, id ` + order + ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if f.After > 0 {
		slices.Reverse(out)
	}
	return out, nil
}

// IssueRuns returns every stored run of one issue in a workflow, oldest
// first. Each is one attempt.
func (s *Store) IssueRuns(project, workflow, issueID string) ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runColumns+` FROM runs_cache
		WHERE project = ? AND workflow = ? AND issue_id = ? ORDER BY started_at, id`, project, workflow, issueID)
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

// RunOptions are the distinct values stored runs have, for filter menus.
type RunOptions struct {
	Projects, Workflows, Models, Adapters []string
}

// RunOptions returns the distinct non-empty values of the filterable columns.
func (s *Store) RunOptions() (RunOptions, error) {
	var o RunOptions
	for col, dst := range map[string]*[]string{
		"project": &o.Projects, "workflow": &o.Workflows, "model": &o.Models, "agent_adapter": &o.Adapters,
	} {
		rows, err := s.db.Query(`SELECT DISTINCT ` + col + ` FROM runs_cache WHERE ` + col + ` != '' ORDER BY ` + col)
		if err != nil {
			return o, err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return o, err
			}
			*dst = append(*dst, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

// Run returns one run, or sql.ErrNoRows.
func (s *Store) Run(project, workflow string, id int64) (Run, error) {
	return scanRun(s.db.QueryRow(`SELECT `+runColumns+` FROM runs_cache
		WHERE project = ? AND workflow = ? AND run_id = ?`, project, workflow, id))
}

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	err := row.Scan(&r.Cursor, &r.Project, &r.Workflow, &r.ID, &r.IssueID, &r.DisplayIdentifier, &r.Status,
		&r.StartedAt, &r.CompletedAt, &r.Attempt, &r.TurnsCompleted, &r.Error, &r.AgentAdapter, &r.Model,
		&r.ModelSource, &r.InputTokens, &r.OutputTokens, &r.TotalTokens, &r.CacheReadTokens,
		&r.TokensMeasured, &r.Workspace, &r.WorkflowKind,
		&r.WorkflowFile, &r.TemplateID, &r.SessionID, &r.APIRequests, &r.HandoffReset, &r.ReviewMetadata)
	return r, err
}
