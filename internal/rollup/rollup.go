// Package rollup reads the live state of every running loop across
// every repository.
//
// It reads the registry that Jigz writes rather than being told
// what is running, so a repository started outside Jigz still shows
// up. A loop whose engine answers is read entirely through the engine's
// HTTP API: /api/v1/state for live figures and /api/v1/stats for all-time
// run counts. A loop whose engine does not answer is read from its SQLite
// database, so totals survive restarts.
package rollup

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kinged007/jigz/internal/dirs"
)

// staleAfter is how long a registry entry may go unrefreshed before it
// is dropped, unless the process that wrote it is still alive. It
// matches the rule Jigz applies when reading its own registry.
const staleAfter = 5 * time.Minute

// probeTimeout is short: a loop that is restarting yields no live state
// rather than holding up the page.
const probeTimeout = 3 * time.Second

// Run is one item an agent is working on right now.
type Run struct {
	Identifier string `json:"identifier"`
	State      string `json:"state"`
	Turns      int    `json:"turns"`
	Tokens     int64  `json:"tokens"`
	Model      string `json:"model"`
	StartedAt  string `json:"started_at"`
	// PickUp is when the loop first took this issue on its current
	// pass, taken from the run history rather than the engine: a retried
	// attempt gets a fresh StartedAt, so the engine's own figure
	// restarts while the work has been going for an hour. Empty when the
	// loop has no history for the issue, which is the first attempt
	// still in flight.
	PickUp string `json:"picked_up,omitempty"`
}

// Loop is one running loop: live state plus all-time totals.
type Loop struct {
	Repo      string `json:"repo"`
	Loop      string `json:"loop"`
	Port      int    `json:"port"`
	DBPath    string `json:"db_path"`
	Live      bool   `json:"live"`
	Running   []Run  `json:"running,omitempty"`
	Retrying  int    `json:"retrying"`
	Runs      int    `json:"runs"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	// Issues is the number of distinct issues behind Runs: a retried
	// issue adds a run per attempt but is still one issue.
	Issues int `json:"issues"`
	// Unmeasured is the number of runs whose adapter reported no
	// tokens, so Tokens undercounts by an unknown amount.
	Unmeasured int   `json:"unmeasured"`
	Tokens     int64 `json:"tokens"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	// Cost is the engine's own estimate of what the agents currently
	// running on this loop have cost, in USD. Nil when the workflow file
	// configures no token_rates, in which case the engine omits the
	// figure rather than guessing one.
	Cost    *float64 `json:"cost,omitempty"`
	DBError string   `json:"db_error,omitempty"`
	// Schema is set when the database is at an unsupported version or
	// lacks a column; the figures that depend on it are left empty.
	Schema *SchemaNotice `json:"schema_notice,omitempty"`
	// Parked and BudgetHeld are the issues the engine's state API lists
	// as parked and as stopped by a budget. They are set only while the
	// engine answers.
	Parked     []Held `json:"-"`
	BudgetHeld []Held `json:"-"`
}

// Held is one issue the engine keeps out of dispatch, with the reason.
type Held struct {
	ID     string
	Reason string
}

// Repo is one repository's totals across its loops.
type Repo struct {
	Repo    string `json:"repo"`
	Loops   int    `json:"loops"`
	Live    int    `json:"live"`
	Running int    `json:"running"`
	Runs    int    `json:"runs"`
	Failed  int    `json:"failed"`
	Tokens  int64  `json:"tokens"`
}

// Fleet is everything the dashboard shows.
type Fleet struct {
	At      time.Time `json:"at"`
	Loops   []Loop    `json:"loops"`
	Repos   []Repo    `json:"repos"`
	Live    int       `json:"live"`
	Running int       `json:"running"`
	Tokens  int64     `json:"tokens"`
	Runs    int       `json:"runs"`
	Failed  int       `json:"failed"`
}

// endpoint mirrors the fields Jigz records in its registry.
type endpoint struct {
	Repo     string `json:"repo"`
	RepoName string `json:"repo_name"`
	Loop     string `json:"loop"`
	Port     int    `json:"port"`
	DBPath   string `json:"db_path"`
	// Probe says whether a live process owns the port. A repository whose
	// run exited keeps its entry so the page can offer start again, and
	// probing its dead ports would cost the page a timeout per loop.
	Probe bool `json:"-"`
}

type registry struct {
	Entries map[string]struct {
		RepoName  string     `json:"repo_name"`
		UpdatedAt time.Time  `json:"updated_at"`
		PID       int        `json:"pid"`
		Loops     []endpoint `json:"loops"`
	} `json:"entries"`
}

// RegistryPath is the shared registry Jigz writes and every
// supervisor reads.
func RegistryPath() (string, error) {
	dir, err := dirs.Cache()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "registry.json"), nil
}

// Reader collects the fleet. The zero value reads the shared registry;
// a test can point RegistryPath and Client elsewhere.
type Reader struct {
	RegistryPath string
	Client       *http.Client
	// Repos, when set, keeps only loops belonging to these
	// repositories. Empty means everything in the registry.
	Repos []string
	// Exclude drops loops of these repositories, which another source
	// reports.
	Exclude []string
}

func (r Reader) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: probeTimeout}
}

func (r Reader) path() string {
	if r.RegistryPath != "" {
		return r.RegistryPath
	}
	p, err := RegistryPath()
	if err != nil {
		return ""
	}
	return p
}

// Collect reads the registry, then every live loop in it. A loop that
// is not answering is reported as not live rather than failing the
// page: a restarting loop is normal, not an error.
func (r Reader) Collect() Fleet {
	eps := r.endpoints()
	loops := make([]Loop, len(eps))
	var wg sync.WaitGroup
	for i, ep := range eps {
		wg.Add(1)
		go func(i int, ep endpoint) {
			defer wg.Done()
			loops[i] = r.one(ep)
		}(i, ep)
	}
	wg.Wait()
	return Assemble(loops)
}

// Assemble sorts loops and totals them into a Fleet.
func Assemble(loops []Loop) Fleet {
	f := Fleet{At: time.Now().UTC(), Loops: loops}
	sort.Slice(f.Loops, func(i, j int) bool {
		if f.Loops[i].Repo != f.Loops[j].Repo {
			return f.Loops[i].Repo < f.Loops[j].Repo
		}
		return f.Loops[i].Loop < f.Loops[j].Loop
	})
	byRepo := map[string]*Repo{}
	for _, l := range f.Loops {
		f.Tokens += l.Tokens
		f.Runs += l.Runs
		f.Failed += l.Failed
		f.Running += len(l.Running)
		r, ok := byRepo[l.Repo]
		if !ok {
			r = &Repo{Repo: l.Repo}
			byRepo[l.Repo] = r
		}
		r.Loops++
		r.Runs += l.Runs
		r.Failed += l.Failed
		r.Tokens += l.Tokens
		r.Running += len(l.Running)
		if l.Live {
			r.Live++
			f.Live++
		}
	}
	for _, r := range byRepo {
		f.Repos = append(f.Repos, *r)
	}
	sort.Slice(f.Repos, func(i, j int) bool { return f.Repos[i].Repo < f.Repos[j].Repo })
	return f
}

// endpoints returns the live registry entries, filtered to Repos.
func (r Reader) endpoints() []endpoint {
	path := r.path()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var reg registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		return nil
	}
	keep := map[string]bool{}
	for _, s := range r.Repos {
		keep[s] = true
	}
	skip := map[string]bool{}
	for _, s := range r.Exclude {
		skip[s] = true
	}
	var out []endpoint
	for _, e := range reg.Entries {
		if (len(keep) > 0 && !keep[e.RepoName]) || skip[e.RepoName] {
			continue
		}
		// A stale heartbeat from a dead process is skipped, never
		// deleted here: the file is shared and its owner may still be
		// rewriting it.
		if e.PID != 0 && time.Since(e.UpdatedAt) > staleAfter && !pidAlive(e.PID) {
			continue
		}
		for _, ep := range e.Loops {
			if ep.Port <= 0 {
				continue
			}
			ep.Probe = e.PID != 0
			if ep.RepoName == "" {
				ep.RepoName = e.RepoName
			}
			out = append(out, ep)
		}
	}
	return out
}

// one collects one loop's live state and all-time totals.
func (r Reader) one(ep endpoint) Loop {
	l := Loop{Repo: ep.RepoName, Loop: ep.Loop, Port: ep.Port, DBPath: ep.DBPath}
	// The state read comes first: the pickup times are keyed by the
	// identifiers it reports. A port nobody stands behind is not probed.
	if ep.Probe {
		l.readState(r.client())
	}
	l.readTotals(r.client())
	return l
}

// Loop builds one loop's figures for a loop known from elsewhere than
// the registry. The engine's API is read only when probe is set; without
// an answer the database totals are read.
func (r Reader) Loop(repo, loop string, port int, dbPath string, probe bool) Loop {
	l := Loop{Repo: repo, Loop: loop, Port: port, DBPath: dbPath}
	if probe && port > 0 {
		l.readState(r.client())
	}
	l.readTotals(r.client())
	return l
}

// readTotals fills the all-time figures from the engine's stats API when the
// state read found the engine live, and from the database otherwise or when
// the stats read fails.
func (l *Loop) readTotals(c *http.Client) {
	if l.Live && l.readStats(c) {
		return
	}
	l.readDB()
}

// statePayload mirrors the subset of the loop's state API the dashboard
// consumes.
type statePayload struct {
	Counts struct {
		Running  int `json:"running"`
		Retrying int `json:"retrying"`
	} `json:"counts"`
	// ActiveEstimatedCostUSD is the engine's cost estimate for the
	// running sessions, present only when the workflow configures
	// token_rates.
	ActiveEstimatedCostUSD *float64 `json:"active_estimated_cost_usd"`
	AgentTotals            struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"agent_totals"`
	Parked []struct {
		IssueID string `json:"issue_id"`
		Reason  string `json:"reason"`
	} `json:"parked"`
	BudgetExhausted []struct {
		IssueID string `json:"issue_id"`
		Reason  string `json:"reason"`
	} `json:"budget_exhausted"`
	Running []struct {
		DisplayIdentifier string `json:"display_identifier"`
		IssueIdentifier   string `json:"issue_identifier"`
		State             string `json:"state"`
		TurnCount         int    `json:"turn_count"`
		ModelName         string `json:"model_name"`
		StartedAt         string `json:"started_at"`
		// DispatchedAt is the start of the first run of the issue's
		// current pass.
		DispatchedAt *string `json:"dispatched_at"`
		Tokens       struct {
			TotalTokens *int64 `json:"total_tokens"`
		} `json:"tokens"`
	} `json:"running"`
}

// GetJSON reads one path of an engine's HTTP API on loopback and decodes
// the JSON answer into out.
func GetJSON(c *http.Client, port int, path string, out any) error {
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (l *Loop) readState(c *http.Client) {
	var p statePayload
	if err := GetJSON(c, l.Port, "/api/v1/state", &p); err != nil {
		return
	}
	l.Live = true
	l.Retrying = p.Counts.Retrying
	l.Cost = p.ActiveEstimatedCostUSD
	l.Input, l.Output, l.Tokens = p.AgentTotals.InputTokens, p.AgentTotals.OutputTokens, p.AgentTotals.TotalTokens
	for _, e := range p.Parked {
		l.Parked = append(l.Parked, Held{e.IssueID, e.Reason})
	}
	for _, e := range p.BudgetExhausted {
		l.BudgetHeld = append(l.BudgetHeld, Held{e.IssueID, e.Reason})
	}
	for _, run := range p.Running {
		id := run.DisplayIdentifier
		if id == "" {
			id = run.IssueIdentifier
		}
		var toks int64
		if run.Tokens.TotalTokens != nil {
			toks = *run.Tokens.TotalTokens
		}
		r := Run{
			Identifier: id,
			State:      run.State,
			Turns:      run.TurnCount,
			Tokens:     toks,
			Model:      run.ModelName,
			StartedAt:  run.StartedAt,
		}
		if run.DispatchedAt != nil {
			r.PickUp = *run.DispatchedAt
		}
		l.Running = append(l.Running, r)
	}
}

// statsPayload mirrors the subset of GET /api/v1/stats the dashboard
// consumes. The report covers every run of the engine's database.
type statsPayload struct {
	Summary struct {
		Runs                 int `json:"runs"`
		Issues               int `json:"issues"`
		TokensUnmeasuredRuns int `json:"tokens_unmeasured_runs"`
	} `json:"summary"`
	ByStatus []struct {
		Name string `json:"name"`
		Runs int    `json:"runs"`
	} `json:"by_status"`
}

// readStats fills the run counts from the engine's stats API and reports
// whether it could. Succeeded and Failed count the runs with exactly that
// status, as the database read does: the engine's own failed total also
// holds other statuses.
func (l *Loop) readStats(c *http.Client) bool {
	var p statsPayload
	if err := GetJSON(c, l.Port, "/api/v1/stats", &p); err != nil {
		return false
	}
	l.Runs, l.Issues, l.Unmeasured = p.Summary.Runs, p.Summary.Issues, p.Summary.TokensUnmeasuredRuns
	l.Succeeded, l.Failed = 0, 0
	for _, g := range p.ByStatus {
		switch g.Name {
		case "succeeded":
			l.Succeeded = g.Runs
		case "failed":
			l.Failed = g.Runs
		}
	}
	return true
}

// readDB fills the all-time figures. A missing or fresh database leaves
// zeros rather than an error: a loop that has not run yet is not
// broken.
func (l *Loop) readDB() {
	if l.DBPath == "" {
		return
	}
	if _, err := os.Stat(l.DBPath); err != nil {
		l.DBError = "no database yet"
		return
	}
	db, err := OpenReadOnly(l.DBPath)
	if err != nil {
		l.DBError = err.Error()
		return
	}
	defer db.Close()
	schema := InspectSchema(db)
	l.Schema = schema.Notice
	if schema.Has("aggregate_metrics") {
		err = db.QueryRow(`SELECT input_tokens, output_tokens, total_tokens FROM aggregate_metrics WHERE key='agent_totals'`).
			Scan(&l.Input, &l.Output, &l.Tokens)
		if err != nil && err != sql.ErrNoRows {
			l.DBError = err.Error()
			return
		}
	}
	if !schema.Has("run_history") {
		return
	}
	rows, err := db.Query(`SELECT status, COUNT(*) FROM run_history GROUP BY status`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			continue
		}
		l.Runs += n
		switch status {
		case "succeeded":
			l.Succeeded += n
		case "failed":
			l.Failed += n
		}
	}
	// Closed before the next queries, which need the single connection
	// this result set is holding. There is no return between the two.
	rows.Close()
	if err := db.QueryRow(`SELECT COUNT(DISTINCT issue_id), COALESCE(SUM(tokens_measured = 0), 0) FROM run_history`).
		Scan(&l.Issues, &l.Unmeasured); err != nil {
		l.DBError = err.Error()
	}
	for i := range l.Running {
		if id := l.Running[i].Identifier; id != "" {
			l.Running[i].PickUp = pickedUp(db, id)
		}
	}
}

// pickedUp is when the loop first started work on an issue on its
// current pass: the earliest attempt since the last one that succeeded.
// The cutoff matters because an issue can be picked up more than once,
// and a run that finished yesterday is not the run going on now.
//
// The identifier is matched either way round because the state API
// reports the display form (owner/repo#7) while older history rows hold
// only the bare number.
func pickedUp(db *sql.DB, identifier string) string {
	var at sql.NullString
	err := db.QueryRow(`SELECT MIN(started_at) FROM run_history
		WHERE (display_identifier = ?1 OR identifier = ?1)
		AND id > COALESCE((SELECT MAX(id) FROM run_history
			WHERE (display_identifier = ?1 OR identifier = ?1) AND status = 'succeeded'), 0)`,
		identifier).Scan(&at)
	if err != nil || !at.Valid {
		return ""
	}
	return at.String
}

func tableExists(db *sql.DB, name string) bool {
	var found string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&found); err != nil {
		return false
	}
	return true
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
