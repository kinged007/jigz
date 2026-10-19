// Package rollup reads the live state of every running loop across
// every repository.
//
// It reads the registry that sortie-loop writes rather than being told
// what is running, so a repository started outside central still shows
// up. Live figures come from each loop's own state API; all-time
// figures come from its SQLite database, so totals survive restarts.
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
)

// staleAfter is how long a registry entry may go unrefreshed before it
// is dropped, unless the process that wrote it is still alive. It
// matches the rule sortie-loop applies when reading its own registry.
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

// endpoint mirrors the fields sortie-loop records in its registry.
type endpoint struct {
	Repo     string `json:"repo"`
	RepoName string `json:"repo_name"`
	Loop     string `json:"loop"`
	Port     int    `json:"port"`
	DBPath   string `json:"db_path"`
}

type registry struct {
	Entries map[string]struct {
		RepoName  string     `json:"repo_name"`
		UpdatedAt time.Time  `json:"updated_at"`
		PID       int        `json:"pid"`
		Loops     []endpoint `json:"loops"`
	} `json:"entries"`
}

// RegistryPath is the shared registry sortie-loop writes and every
// supervisor reads.
func RegistryPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sortie-loop", "registry.json"), nil
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
		if time.Since(e.UpdatedAt) > staleAfter && !pidAlive(e.PID) {
			continue
		}
		for _, ep := range e.Loops {
			if ep.Port <= 0 {
				continue
			}
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
	// identifiers it reports.
	l.readState(r.client())
	l.readDB()
	return l
}

// Loop builds one loop's figures for a loop known from elsewhere than
// the registry. The engine's state API is read only when probe is set;
// the database totals are always read.
func (r Reader) Loop(repo, loop string, port int, dbPath string, probe bool) Loop {
	l := Loop{Repo: repo, Loop: loop, Port: port, DBPath: dbPath}
	if probe && port > 0 {
		l.readState(r.client())
	}
	l.readDB()
	return l
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
	Running                []struct {
		DisplayIdentifier string `json:"display_identifier"`
		IssueIdentifier   string `json:"issue_identifier"`
		State             string `json:"state"`
		TurnCount         int    `json:"turn_count"`
		ModelName         string `json:"model_name"`
		StartedAt         string `json:"started_at"`
		Tokens            struct {
			TotalTokens *int64 `json:"total_tokens"`
		} `json:"tokens"`
	} `json:"running"`
}

func (l *Loop) readState(c *http.Client) {
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/state", l.Port))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var p statePayload
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return
	}
	l.Live = true
	l.Retrying = p.Counts.Retrying
	l.Cost = p.ActiveEstimatedCostUSD
	for _, run := range p.Running {
		id := run.DisplayIdentifier
		if id == "" {
			id = run.IssueIdentifier
		}
		var toks int64
		if run.Tokens.TotalTokens != nil {
			toks = *run.Tokens.TotalTokens
		}
		l.Running = append(l.Running, Run{
			Identifier: id,
			State:      run.State,
			Turns:      run.TurnCount,
			Tokens:     toks,
			Model:      run.ModelName,
			StartedAt:  run.StartedAt,
		})
	}
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
