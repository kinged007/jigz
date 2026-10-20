package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/auth"
	"github.com/kinged007/sortie-loop/internal/loopsrc"
	"github.com/kinged007/sortie-loop/internal/rollup"
	"github.com/kinged007/sortie-loop/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type apiProject struct {
	Project  string    `json:"project"`
	State    string    `json:"state"`
	Draining bool      `json:"draining,omitempty"`
	Loops    []string  `json:"loops"`
	PID      int       `json:"pid,omitempty"`
	Since    time.Time `json:"since,omitzero"`
	Error    string    `json:"error,omitempty"`
	Observed int       `json:"observed_loops"`
	Running  int       `json:"running"`
	Tokens   int64     `json:"tokens"`
	// Capabilities is what this project's loop reports; all false when it
	// has no readable status document.
	Capabilities loopsrc.Capabilities `json:"capabilities"`
	// DiskBytes is omitted until the first measurement lands.
	DiskBytes *int64 `json:"disk_bytes,omitempty"`
}

type apiHost struct {
	// CPUPct is omitted until there are two samples to compare.
	CPUPct    *float64 `json:"cpu_pct,omitempty"`
	Cores     int      `json:"cores"`
	MemUsed   int64    `json:"mem_used"`
	MemTotal  int64    `json:"mem_total"`
	DiskUsed  int64    `json:"disk_used"`
	DiskTotal int64    `json:"disk_total"`
	DiskPath  string   `json:"disk_path,omitempty"`
}

type apiNotice struct {
	Project  string              `json:"project"`
	Workflow string              `json:"workflow"`
	Notice   rollup.SchemaNotice `json:"notice"`
}

type apiFleetBody struct {
	At            time.Time            `json:"at"`
	Source        string               `json:"source"`
	Capabilities  loopsrc.Capabilities `json:"capabilities"`
	Projects      []apiProject         `json:"projects"`
	Loops         []rollup.Loop        `json:"loops"`
	SchemaNotices []apiNotice          `json:"schema_notices"`
	Totals        struct {
		Live    int   `json:"live"`
		Running int   `json:"running"`
		Runs    int   `json:"runs"`
		Failed  int   `json:"failed"`
		Tokens  int64 `json:"tokens"`
	} `json:"totals"`
	Host apiHost `json:"host"`
}

// apiFleet serves the same data as the dashboard.
func (a *app) apiFleet(w http.ResponseWriter, r *http.Request) {
	d := a.data("", "", requestHost(r), a.loopURLTemplate)
	src := a.src()
	body := apiFleetBody{
		At: d.Now.UTC(), Source: src.Name(), Capabilities: src.Capabilities(),
		Projects: []apiProject{}, Loops: d.Fleet.Loops, SchemaNotices: []apiNotice{},
	}
	if body.Loops == nil {
		body.Loops = []rollup.Loop{}
	}
	for _, v := range d.Repos {
		p := apiProject{
			Project: v.Slug, State: v.State, Draining: v.Draining, Loops: v.Loops, PID: v.PID,
			Since: v.Since, Error: v.Err, Observed: v.Observed, Running: v.Running, Tokens: v.Tokens,
		}
		if op, ok := src.(loopsrc.Operator); ok {
			p.Capabilities = op.ProjectCapabilities(v.Slug)
		}
		if v.DiskKnown {
			p.DiskBytes = &v.Disk
		}
		body.Projects = append(body.Projects, p)
	}
	for _, l := range d.Fleet.Loops {
		if l.Schema != nil {
			body.SchemaNotices = append(body.SchemaNotices, apiNotice{Project: l.Repo, Workflow: l.Loop, Notice: *l.Schema})
		}
	}
	t := &body.Totals
	t.Live, t.Running, t.Runs, t.Failed, t.Tokens = d.Fleet.Live, d.Fleet.Running, d.Fleet.Runs, d.Fleet.Failed, d.Fleet.Tokens
	m := d.Sys
	body.Host = apiHost{
		Cores: m.Cores, MemUsed: m.MemUsed, MemTotal: m.MemTotal,
		DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal, DiskPath: m.DiskPath,
	}
	if m.CPU >= 0 {
		body.Host.CPUPct = &m.CPU
	}
	writeJSON(w, http.StatusOK, body)
}

// apiRuns lists runs from central's copy, newest first. next_cursor is
// present when there may be more; pass it back as cursor.
func (a *app) apiRuns(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		apiError(w, http.StatusServiceUnavailable, "run history is unavailable")
		return
	}
	q := r.URL.Query()
	f := store.RunFilter{
		Project: q.Get("project"), Workflow: q.Get("workflow"), State: q.Get("state"),
		Since: q.Get("since"), Until: q.Get("until"), Q: q.Get("q"),
	}
	for name, v := range map[string]string{"since": f.Since, "until": f.Until} {
		if v == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			apiError(w, http.StatusBadRequest, name+" must be an RFC 3339 timestamp")
			return
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			apiError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		f.Limit = min(n, store.MaxLimit)
	}
	if f.Limit == 0 {
		f.Limit = store.DefaultLimit
	}
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			apiError(w, http.StatusBadRequest, "cursor is not one returned by this endpoint")
			return
		}
		f.Cursor = n
	}
	runs, err := a.store.Runs(f)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a.githubOn() {
		for i := range runs {
			if runs[i].PRs, err = a.runPRs(runs[i]); err != nil {
				apiError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	body := struct {
		Runs       []store.Run `json:"runs"`
		NextCursor string      `json:"next_cursor,omitempty"`
	}{Runs: runs}
	if body.Runs == nil {
		body.Runs = []store.Run{}
	}
	if len(runs) == f.Limit {
		body.NextCursor = strconv.FormatInt(runs[len(runs)-1].Cursor, 10)
	}
	writeJSON(w, http.StatusOK, body)
}

// apiRun returns one run with the events central observed for its issue.
// The path is /api/v1/runs/{project}/{workflow}/{id}, where the project
// is owner/name, so the last two segments are the workflow and the id.
func (a *app) apiRun(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		apiError(w, http.StatusServiceUnavailable, "run history is unavailable")
		return
	}
	parts := strings.Split(r.PathValue("rest"), "/")
	if len(parts) < 3 {
		apiError(w, http.StatusNotFound, "expected /api/v1/runs/{project}/{workflow}/{id}")
		return
	}
	id, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil {
		apiError(w, http.StatusNotFound, "run id is not a number")
		return
	}
	workflow := parts[len(parts)-2]
	project := strings.Join(parts[:len(parts)-2], "/")
	run, err := a.store.Run(project, workflow, id)
	if errors.Is(err, sql.ErrNoRows) {
		apiError(w, http.StatusNotFound, "no such run")
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	events, err := a.store.IssueEvents(project, workflow, run.IssueID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []store.Event{}
	}
	if a.githubOn() {
		if run.PRs, err = a.runPRs(run); err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	runs, err := a.store.IssueRuns(project, workflow, run.IssueID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows := buildAttempts(runs, run.ID)
	cur := slices.IndexFunc(rows, func(r attemptRow) bool { return r.Current })
	writeJSON(w, http.StatusOK, struct {
		Run        store.Run     `json:"run"`
		ErrorClass errorClass    `json:"error_class"`
		Summary    string        `json:"summary"`
		Attempts   []apiAttempt  `json:"attempts"`
		Events     []store.Event `json:"events"`
		// GitHub is set when enrichment is on; the run's pull_requests
		// are then what is cached so far.
		GitHub bool `json:"github_enrichment"`
	}{run, rows[cur].Class, summarize(rows, cur), apiAttempts(rows), events, a.githubOn()})
}

// apiAttempt is one attempt of the issue a run belongs to, with the facts
// derived from the stored runs. GapSeconds is the wait since the previous
// attempt ended and is omitted for the first.
type apiAttempt struct {
	RunID           int64      `json:"run_id"`
	Attempt         int        `json:"attempt"`
	Status          string     `json:"status"`
	StartedAt       string     `json:"started_at"`
	CompletedAt     string     `json:"completed_at"`
	DurationSeconds int64      `json:"duration_seconds"`
	GapSeconds      *int64     `json:"gap_seconds,omitempty"`
	TurnsCompleted  int        `json:"turns_completed"`
	TotalTokens     int64      `json:"total_tokens"`
	TokensMeasured  bool       `json:"tokens_measured"`
	ErrorClass      errorClass `json:"error_class"`
	Error           string     `json:"error,omitempty"`
}

func apiAttempts(rows []attemptRow) []apiAttempt {
	out := make([]apiAttempt, len(rows))
	for i, r := range rows {
		out[i] = apiAttempt{
			RunID: r.Run.ID, Attempt: r.Attempt, Status: r.Status, StartedAt: r.Run.StartedAt,
			CompletedAt: r.Run.CompletedAt, DurationSeconds: int64(r.ended.Sub(r.started).Seconds()),
			TurnsCompleted: r.Turns, TotalTokens: r.Run.TotalTokens, TokensMeasured: r.Run.TokensMeasured,
			ErrorClass: r.Class, Error: r.Error,
		}
		if i > 0 && r.Gap != "" {
			g := int64(r.started.Sub(rows[i-1].ended).Seconds())
			out[i].GapSeconds = &g
		}
	}
	return out
}

// apiAudit lists the newest audit rows, newest first. Rows carry client
// IPs, so it needs a login even when public_read is on.
func (a *app) apiAudit(w http.ResponseWriter, r *http.Request) {
	if !auth.Session(r).Authenticated {
		apiError(w, http.StatusUnauthorized, "login required")
		return
	}
	if a.store == nil {
		apiError(w, http.StatusServiceUnavailable, "audit log is unavailable")
		return
	}
	limit := store.DefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			apiError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, store.MaxLimit)
	}
	rows, err := a.store.Audit(limit)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []store.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, struct {
		Audit []store.AuditEntry `json:"audit"`
	}{rows})
}
