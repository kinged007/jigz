package collector

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/kinged007/sortie-loop/internal/rollup"
	"github.com/kinged007/sortie-loop/internal/store"
)

// apiTimeout bounds each request to an engine. A loop that does not answer
// in time is read from its database for the pass.
const apiTimeout = 3 * time.Second

// apiPage is the page size of GET /api/v1/runs, its maximum.
const apiPage = 200

type apiRun struct {
	RunID             int64   `json:"run_id"`
	IssueID           string  `json:"issue_id"`
	IssueIdentifier   string  `json:"issue_identifier"`
	DisplayIdentifier string  `json:"display_identifier"`
	Attempt           int     `json:"attempt"`
	Status            string  `json:"status"`
	Error             *string `json:"error"`
	AgentKind         string  `json:"agent_kind"`
	ConfiguredModel   string  `json:"configured_model"`
	ModelName         string  `json:"model_name"`
	WorkflowFile      string  `json:"workflow_file"`
	StartedAt         string  `json:"started_at"`
	CompletedAt       string  `json:"completed_at"`
	TurnsCompleted    int     `json:"turns_completed"`
	Tokens            struct {
		Input     *int64 `json:"input_tokens"`
		Output    *int64 `json:"output_tokens"`
		Total     *int64 `json:"total_tokens"`
		CacheRead *int64 `json:"cache_read_tokens"`
	} `json:"tokens"`
	TokensMeasured bool    `json:"tokens_measured"`
	SessionID      *string `json:"session_id"`
}

type apiRuns struct {
	Runs       []apiRun `json:"runs"`
	NextCursor *string  `json:"next_cursor"`
}

// apiState is the part of GET /api/v1/state that is diffed into events.
type apiState struct {
	Parked []struct {
		IssueID string `json:"issue_id"`
		Reason  string `json:"reason"`
	} `json:"parked"`
	Retrying []struct {
		IssueID string    `json:"issue_id"`
		Attempt int       `json:"attempt"`
		DueAt   time.Time `json:"due_at"`
		Error   string    `json:"error"`
	} `json:"retrying"`
	BudgetExhausted []struct {
		IssueID string `json:"issue_id"`
		Reason  string `json:"reason"`
	} `json:"budget_exhausted"`
}

func (c *Collector) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: apiTimeout}
}

// readAPI copies a loop's runs and state changes from its engine and
// reports whether the engine answered. A false result sends the loop to the
// database reader.
func (c *Collector) readAPI(l rollup.Loop, ts string) bool {
	var st apiState
	if err := rollup.GetJSON(c.client(), l.Port, "/api/v1/state", &st); err != nil {
		return false
	}
	if err := c.copyRunsAPI(l); err != nil {
		log.Printf("collector: %s/%s: runs: %v", l.Repo, l.Loop, err)
		return false
	}
	c.diffAPIState(l, st, ts)
	return true
}

// copyRunsAPI stores the runs the engine has gained since the last run id
// central holds. Pages are read newest first, following next_cursor, until
// one holds a run already stored; the runs found are stored oldest first
// once every page has been read, so a failed read leaves no gap below the
// last stored id.
//
// When the newest run of the engine has a lower id than the last stored
// run, the engine database was replaced. The stored runs of the workflow are
// then removed and its runs are copied again, because run ids of the new
// database would otherwise overwrite unrelated stored runs.
func (c *Collector) copyRunsAPI(l rollup.Loop) error {
	last, err := c.Store.LastRunID(l.Repo, l.Loop)
	if err != nil {
		return err
	}
	configured, kind := "", ""
	if c.Model != nil {
		configured = c.Model(l.Repo, l.Loop)
	}
	if c.Kind != nil {
		kind = c.Kind(l.Repo, l.Loop)
	}
	file := "WORKFLOW." + l.Loop + ".md"
	var found []store.Run
	query := "?limit=" + strconv.Itoa(apiPage)
	for first := true; ; first = false {
		var page apiRuns
		if err := rollup.GetJSON(c.client(), l.Port, "/api/v1/runs"+query, &page); err != nil {
			return err
		}
		if first && len(page.Runs) > 0 && page.Runs[0].RunID < last {
			if err := c.Store.DeleteRuns(l.Repo, l.Loop); err != nil {
				return err
			}
			log.Printf("collector: %s/%s: engine database was replaced; stored runs reset", l.Repo, l.Loop)
			last = 0
		}
		reached := false
		for _, r := range page.Runs {
			if r.RunID <= last {
				reached = true
				break
			}
			if r.WorkflowFile != "" && filepath.Base(r.WorkflowFile) != file {
				continue
			}
			found = append(found, r.run(l, kind, configured))
		}
		if reached || page.NextCursor == nil || *page.NextCursor == "" || len(page.Runs) == 0 {
			break
		}
		query = "?limit=" + strconv.Itoa(apiPage) + "&cursor=" + url.QueryEscape(*page.NextCursor)
	}
	if len(found) > 0 {
		log.Printf("collector: %s/%s: runs copied from the engine API: %d", l.Repo, l.Loop, len(found))
	}
	slices.Reverse(found)
	for len(found) > 0 {
		n := min(len(found), batch)
		if err := c.Store.AddRuns(found[:n]); err != nil {
			return err
		}
		found = found[n:]
	}
	return nil
}

func (r apiRun) run(l rollup.Loop, kind, configured string) store.Run {
	out := store.Run{
		Project: l.Repo, Workflow: l.Loop, WorkflowKind: kind, ID: r.RunID, IssueID: r.IssueID,
		DisplayIdentifier: r.DisplayIdentifier, Status: r.Status, StartedAt: r.StartedAt,
		CompletedAt: r.CompletedAt, Attempt: r.Attempt, TurnsCompleted: r.TurnsCompleted,
		AgentAdapter: r.AgentKind, TokensMeasured: r.TokensMeasured, WorkflowFile: r.WorkflowFile,
	}
	if out.DisplayIdentifier == "" {
		out.DisplayIdentifier = r.IssueIdentifier
	}
	if r.Error != nil {
		out.Error = *r.Error
	}
	if r.SessionID != nil {
		out.SessionID = *r.SessionID
	}
	if r.TokensMeasured {
		out.InputTokens, out.OutputTokens = deref(r.Tokens.Input), deref(r.Tokens.Output)
		out.TotalTokens, out.CacheReadTokens = deref(r.Tokens.Total), deref(r.Tokens.CacheRead)
	}
	switch {
	case r.ModelName != "":
		out.Model, out.ModelSource = r.ModelName, store.ModelObserved
	case r.ConfiguredModel != "":
		out.Model, out.ModelSource = r.ConfiguredModel, store.ModelConfigured
	case configured != "":
		out.Model, out.ModelSource = configured, store.ModelConfigured
	}
	return out
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// diffAPIState turns the parked, retrying and budget lists of the state API
// into events with the semantics of diffState.
func (c *Collector) diffAPIState(l rollup.Loop, st apiState, ts string) {
	parked := map[string]string{}
	for _, p := range st.Parked {
		parked[p.IssueID] = p.Reason
	}
	retry := map[string]string{}
	for _, r := range st.Retrying {
		d := fmt.Sprintf("attempt %d due %d", r.Attempt, r.DueAt.UnixMilli())
		if r.Error != "" {
			d += ": " + r.Error
		}
		retry[r.IssueID] = d
	}
	budget := map[string]string{}
	for _, b := range st.BudgetExhausted {
		budget[b.IssueID] = b.Reason
	}
	var events []store.Event
	for _, w := range []struct {
		table, added, removed string
		now                   map[string]string
	}{
		{"parked", "parked", "unparked", parked},
		{"retrying", "retry_scheduled", "retry_cleared", retry},
		{"budget_exhausted", "budget_hold", "budget_hold_cleared", budget},
	} {
		events = append(events, c.diff(l, "api/"+w.table, w.added, w.removed, w.now, ts)...)
	}
	if err := c.Store.AddEvents(events); err != nil {
		log.Printf("collector: events: %v", err)
	}
}
