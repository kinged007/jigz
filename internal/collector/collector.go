// Package collector copies what Jigz needs from the loops into
// history.db on a timer: finished runs, state changes, and samples.
//
// Every loop is read from its workflow database, which is only read,
// never written. A database that is missing, locked, or at an unsupported
// schema is skipped for that pass and tried again on the next one.
package collector

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	machine "github.com/kinged007/jigz/internal/host"
	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/store"
)

// DefaultInterval is the time between passes.
const DefaultInterval = 60 * time.Second

// batch bounds how many runs are read from one database at a time.
const batch = 500

// Collector runs passes over the fleet. Fill the exported fields before
// calling Run or Once.
type Collector struct {
	Store  *store.Store
	Source loopsrc.Source
	// Model returns the model a workflow is configured with, or "".
	Model func(project, workflow string) string
	// Kind returns store.KindIssue or store.KindPR for a workflow, or ""
	// when it cannot be told.
	Kind func(project, workflow string) string
	// Discover returns the workflow databases found on disk for every
	// configured project. Those not already read through the Source are
	// copied too, so a stopped project keeps its history. Nil skips it.
	Discover func() []rollup.Loop
	// Host reads machine load for the samples. Nil skips the host sample.
	Host     func() machine.Metrics
	Interval time.Duration
	Now      func() time.Time

	// seen is the last state of each watched list per loop, keyed by loop
	// and list, mapping issue id to detail. A loop with no entry yet is
	// only recorded, so a restart of Jigz does not report every parked
	// issue as new.
	seen map[string]map[string]string
	// filled marks loops whose stored runs were checked for a missing
	// workflow file in this process.
	filled map[string]bool
}

// Run collects until ctx is cancelled and returns after the pass in
// progress finishes.
func (c *Collector) Run(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		c.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once runs one pass.
func (c *Collector) Once(ctx context.Context) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	ts := now.UTC().Format(time.RFC3339)
	f := c.Source.Fleet()
	var samples []store.Sample
	for _, l := range f.Loops {
		if ctx.Err() != nil {
			return
		}
		if l.Live {
			samples = append(samples, store.Sample{
				TS: ts, Scope: "loop", Key: l.Repo + "/" + l.Loop,
				Running: len(l.Running), Retrying: l.Retrying,
			})
		}
		c.readLoop(l, ts)
	}
	for _, l := range c.undiscovered(f.Loops) {
		if ctx.Err() != nil {
			return
		}
		c.readLoop(l, ts)
	}
	if c.Host != nil {
		m := c.Host()
		samples = append(samples, store.Sample{
			TS: ts, Scope: "host", CPUPct: max(m.CPU, 0),
			MemUsed: m.MemUsed, MemTotal: m.MemTotal, DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal,
		})
	}
	if err := c.Store.AddSamples(samples); err != nil {
		log.Printf("collector: samples: %v", err)
	}
	if err := c.Store.PruneSamples(now); err != nil {
		log.Printf("collector: prune: %v", err)
	}
}

// undiscovered returns the discovered databases that no loop of the fleet
// already covers, by workflow or by database file.
func (c *Collector) undiscovered(loops []rollup.Loop) []rollup.Loop {
	if c.Discover == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, l := range loops {
		seen[l.Repo+"/"+l.Loop] = true
		if l.DBPath != "" {
			seen[l.Repo+"\x00"+filepath.Clean(l.DBPath)] = true
		}
	}
	var out []rollup.Loop
	for _, l := range c.Discover() {
		if !seen[l.Repo+"/"+l.Loop] && !seen[l.Repo+"\x00"+l.DBPath] {
			out = append(out, l)
		}
	}
	return out
}

func (c *Collector) readLoop(l rollup.Loop, ts string) {
	if c.Kind != nil {
		if kind := c.Kind(l.Repo, l.Loop); kind != "" {
			if err := c.Store.SetWorkflowKind(l.Repo, l.Loop, kind); err != nil {
				log.Printf("collector: %s/%s: workflow kind: %v", l.Repo, l.Loop, err)
			}
		}
	}
	if l.DBPath == "" {
		return
	}
	if _, err := os.Stat(l.DBPath); err != nil {
		return
	}
	db, err := rollup.OpenReadOnly(l.DBPath)
	if err != nil {
		log.Printf("collector: %s/%s: %v", l.Repo, l.Loop, err)
		return
	}
	defer db.Close()
	schema := rollup.InspectSchema(db)
	if schema.Has("run_history") {
		if err := c.copyRuns(db, l, schema); err != nil {
			log.Printf("collector: %s/%s: runs: %v", l.Repo, l.Loop, err)
		}
	}
	c.diffState(db, l, schema, ts)
}

// session is the latest session of an issue in a workflow database.
type session struct {
	model, id string
}

// readSessions reads session_metadata. Columns an older schema lacks are
// left empty.
func readSessions(db *sql.DB) map[string]session {
	sessionID := "NULL"
	if hasColumn(db, "session_metadata", "session_id") {
		sessionID = "session_id"
	}
	rows, err := db.Query(`SELECT issue_id, model_name, ` + sessionID + ` FROM session_metadata`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]session{}
	for rows.Next() {
		var issue string
		var model, id sql.NullString
		if rows.Scan(&issue, &model, &id) == nil {
			out[issue] = session{model.String, id.String}
		}
	}
	return out
}

// copyRuns stores the runs the workflow database has gained since the last
// run id Jigz holds. The session of an issue is overwritten by its next
// attempt, so its facts are taken now, for the newest run of the issue in
// the batch.
func (c *Collector) copyRuns(db *sql.DB, l rollup.Loop, schema rollup.Schema) error {
	last, err := c.Store.LastRunID(l.Repo, l.Loop)
	if err != nil {
		return err
	}
	if err := c.fillRunFiles(db, l, last); err != nil {
		log.Printf("collector: %s/%s: run files: %v", l.Repo, l.Loop, err)
	}
	configured, kind := "", ""
	if c.Model != nil {
		configured = c.Model(l.Repo, l.Loop)
	}
	if c.Kind != nil {
		kind = c.Kind(l.Repo, l.Loop)
	}
	for {
		runs, err := readRuns(db, last)
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			return nil
		}
		var sessions map[string]session
		if schema.Has("session_metadata") {
			sessions = readSessions(db)
		}
		newest := map[string]int64{}
		for _, r := range runs {
			newest[r.IssueID] = r.ID
		}
		for i := range runs {
			r := &runs[i]
			r.Project, r.Workflow, r.WorkflowKind = l.Repo, l.Loop, kind
			s, ok := sessions[r.IssueID]
			switch {
			case s.model != "":
				r.Model, r.ModelSource = s.model, store.ModelObserved
			case configured != "":
				r.Model, r.ModelSource = configured, store.ModelConfigured
			}
			if ok && newest[r.IssueID] == r.ID {
				r.SessionID = s.id
			}
		}
		if err := c.Store.AddRuns(runs); err != nil {
			return err
		}
		last = runs[len(runs)-1].ID
		if len(runs) < batch {
			return nil
		}
	}
}

// fillRunFiles sets the workflow file on runs stored before Jigz kept
// it, once per loop while Jigz runs.
func (c *Collector) fillRunFiles(db *sql.DB, l rollup.Loop, last int64) error {
	key := l.Repo + "/" + l.Loop
	if c.filled[key] {
		return nil
	}
	need, err := c.Store.NeedsRunFiles(l.Repo, l.Loop)
	if err != nil || !need {
		return err
	}
	if c.filled == nil {
		c.filled = map[string]bool{}
	}
	c.filled[key] = true
	for after := int64(0); after < last; {
		runs, err := readRuns(db, after)
		if err != nil || len(runs) == 0 {
			return err
		}
		if err := c.Store.FillRunFiles(l.Repo, l.Loop, runs); err != nil {
			return err
		}
		after = runs[len(runs)-1].ID
	}
	return nil
}

func hasColumn(db *sql.DB, table, col string) bool {
	var n int
	return db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n) == nil && n > 0
}

func readRuns(db *sql.DB, after int64) ([]store.Run, error) {
	col := func(name string) string {
		if hasColumn(db, "run_history", name) {
			return "COALESCE(" + name + ", '')"
		}
		return "''"
	}
	rows, err := db.Query(`SELECT id, issue_id, COALESCE(NULLIF(display_identifier, ''), identifier), status,
		started_at, completed_at, attempt, turns_completed, COALESCE(error, ''), agent_adapter,
		input_tokens, output_tokens, total_tokens, cache_read_tokens, tokens_measured, `+
		col("workflow_file")+`
		FROM run_history WHERE id > ? ORDER BY id LIMIT ?`, after, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Run
	for rows.Next() {
		var r store.Run
		if err := rows.Scan(&r.ID, &r.IssueID, &r.DisplayIdentifier, &r.Status, &r.StartedAt, &r.CompletedAt,
			&r.Attempt, &r.TurnsCompleted, &r.Error, &r.AgentAdapter, &r.InputTokens, &r.OutputTokens,
			&r.TotalTokens, &r.CacheReadTokens, &r.TokensMeasured, &r.WorkflowFile); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// watched is a current-state table whose rows appearing and disappearing
// are recorded as events. Each query returns an issue id and a detail.
var watched = []struct {
	table, query, added, removed string
	// bare replaces query when a column the query reads is missing.
	needs, bare string
}{
	{"parked_issues", `SELECT issue_id, reason FROM parked_issues`, "parked", "unparked", "", ""},
	{"retry_entries", `SELECT issue_id, 'attempt ' || attempt || ' due ' || due_at_ms || COALESCE(': ' || error, '') FROM retry_entries`,
		"retry_scheduled", "retry_cleared", "due_at_ms",
		`SELECT issue_id, 'attempt ' || attempt || COALESCE(': ' || error, '') FROM retry_entries`},
	{"budget_hold_notices", `SELECT issue_id, reason FROM budget_hold_notices`, "budget_hold", "budget_hold_cleared", "", ""},
}

func (c *Collector) diffState(db *sql.DB, l rollup.Loop, schema rollup.Schema, ts string) {
	var events []store.Event
	for _, w := range watched {
		if !schema.Has(w.table) {
			continue
		}
		query := w.query
		if w.needs != "" && !hasColumn(db, w.table, w.needs) {
			query = w.bare
		}
		now, err := snapshot(db, query)
		if err != nil {
			log.Printf("collector: %s/%s: %s: %v", l.Repo, l.Loop, w.table, err)
			continue
		}
		events = append(events, c.diff(l, "db/"+w.table, w.added, w.removed, now, ts)...)
	}
	if err := c.Store.AddEvents(events); err != nil {
		log.Printf("collector: events: %v", err)
	}
}

// diff records now as the state of a list and returns an event for every
// issue that appeared or disappeared since the previous pass. The first
// pass of a list returns nothing.
func (c *Collector) diff(l rollup.Loop, list, added, removed string, now map[string]string, ts string) []store.Event {
	if c.seen == nil {
		c.seen = map[string]map[string]string{}
	}
	key := l.Repo + "/" + l.Loop + "/" + list
	before, known := c.seen[key]
	c.seen[key] = now
	if !known {
		return nil
	}
	var events []store.Event
	for _, id := range sortedKeys(now) {
		if _, ok := before[id]; !ok {
			events = append(events, store.Event{TS: ts, Project: l.Repo, Workflow: l.Loop, IssueID: id, Kind: added, Detail: now[id]})
		}
	}
	for _, id := range sortedKeys(before) {
		if _, ok := now[id]; !ok {
			events = append(events, store.Event{TS: ts, Project: l.Repo, Workflow: l.Loop, IssueID: id, Kind: removed, Detail: before[id]})
		}
	}
	return events
}

func snapshot(db *sql.DB, query string) (map[string]string, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id string
		var detail sql.NullString
		if err := rows.Scan(&id, &detail); err != nil {
			return nil, err
		}
		out[id] = detail.String
	}
	return out, rows.Err()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
