// Package rollup reads the live state of every running loop across
// every repository.
//
// It reads the registry that Jigz writes rather than being told
// what is running, so a repository started outside Jigz still shows
// up. Every loop is read from its SQLite database: all-time totals, the
// retries and holds the engine persists, and the tokens of a running session
// once its first turn has ended. A session that has not ended a turn is
// not in the database; the supervisor that owns the loop reports it.
package rollup

import (
	"database/sql"
	"encoding/json"
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

// Run is one item an agent is working on right now.
type Run struct {
	Identifier string `json:"identifier"`
	State      string `json:"state"`
	Turns      int    `json:"turns"`
	Tokens     int64  `json:"tokens"`
	Model      string `json:"model"`
	AgentKind  string `json:"agent_kind,omitempty"`
	RuleName   string `json:"rule_name,omitempty"`
	Effort     string `json:"effort,omitempty"`
	StartedAt  string `json:"started_at"`
	// PickUp is when the loop first took this issue on its current
	// pass, taken from the run history rather than the engine: a retried
	// attempt gets a fresh StartedAt, so the engine's own figure
	// restarts while the work has been going for an hour. Empty when the
	// loop has no history for the issue, which is the first attempt
	// still in flight.
	PickUp  string `json:"picked_up,omitempty"`
	issueID string
}

// Loop is one running loop: live state plus all-time totals.
type Loop struct {
	Repo      string `json:"repo"`
	Loop      string `json:"loop"`
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
	Unmeasured int    `json:"unmeasured"`
	Tokens     int64  `json:"tokens"`
	Input      int64  `json:"input"`
	Output     int64  `json:"output"`
	DBError    string `json:"db_error,omitempty"`
	// Schema is set when the database is at an unsupported version or
	// lacks a column; the figures that depend on it are left empty.
	Schema *SchemaNotice `json:"schema_notice,omitempty"`
	// Parked and BudgetHeld are the issues the engine keeps out of
	// dispatch because they are parked and because a budget stopped them.
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
	DBPath   string `json:"db_path"`
	// Probe says whether a live process runs the loop. A repository whose
	// run exited keeps its entry so the page can offer start again.
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
// a test can point RegistryPath elsewhere.
type Reader struct {
	RegistryPath string
	// Repos, when set, keeps only loops belonging to these
	// repositories. Empty means everything in the registry.
	Repos []string
	// Exclude drops loops of these repositories, which another source
	// reports.
	Exclude []string
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

// Collect reads the registry, then the database of every loop in it. A
// loop without a live process is reported as not live.
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
			ep.Probe = e.PID != 0
			if ep.RepoName == "" {
				ep.RepoName = e.RepoName
			}
			out = append(out, ep)
		}
	}
	return out
}

// one collects one loop's live state and all-time totals. The registry
// does not say which sessions run, so only the database speaks for them.
func (r Reader) one(ep endpoint) Loop {
	return r.Loop(ep.RepoName, ep.Loop, ep.DBPath, ep.Probe, nil, false)
}

// Session is an agent session the process that owns a loop has seen
// start and not yet end.
type Session struct {
	IssueID string `json:"issue_id"`
	// Identifier is the issue as displayed, owner/repo#7.
	Identifier string    `json:"identifier"`
	Attempt    int       `json:"attempt"`
	StartedAt  time.Time `json:"started_at"`
	Turns      int       `json:"turns"`
	// AgentKind, RuleName, Model and Effort are what the engine logged
	// when it dispatched the session.
	AgentKind string `json:"agent_kind,omitempty"`
	// Agent is the agent name the loop runs with. One loop has one agent
	// at a time because changes happen at idle.
	Agent    string `json:"agent,omitempty"`
	RuleName string `json:"rule_name,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// Loop builds one loop's figures from its database. live says a process
// runs the loop. sessions are the sessions its supervisor reports, and
// known says the supervisor can account for all of them; without that,
// the running list is what the database holds.
func (Reader) Loop(repo, loop, dbPath string, live bool, sessions []Session, known bool) Loop {
	l := Loop{Repo: repo, Loop: loop, DBPath: dbPath, Live: live}
	l.readDB(live, sessions, known)
	return l
}

// readDB fills the live and all-time figures. A missing or fresh database
// leaves zeros rather than an error: a loop that has not run yet is not
// broken.
func (l *Loop) readDB(live bool, sessions []Session, known bool) {
	if live {
		l.Running = runsOf(sessions)
	}
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
	l.readHeld(db, schema)
	if live {
		l.readSessions(db, schema, known)
	}
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

func runsOf(sessions []Session) []Run {
	var out []Run
	for _, s := range sessions {
		out = append(out, Run{
			Identifier: s.Identifier, State: "running", Turns: s.Turns, issueID: s.IssueID,
			AgentKind: s.AgentKind, RuleName: s.RuleName, Model: s.Model, Effort: s.Effort,
			StartedAt: s.StartedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// readHeld fills the retry count and the issues held out of dispatch.
func (l *Loop) readHeld(db *sql.DB, schema Schema) {
	if schema.Has("retry_entries") {
		_ = db.QueryRow(`SELECT COUNT(*) FROM retry_entries`).Scan(&l.Retrying)
	}
	for i, table := range []string{"budget_hold_notices", "parked_issues"} {
		if !schema.Has(table) {
			continue
		}
		rows, err := db.Query("SELECT issue_id, reason FROM " + table + " ORDER BY issue_id")
		if err != nil {
			continue
		}
		for rows.Next() {
			var h Held
			var reason sql.NullString
			if rows.Scan(&h.ID, &reason) == nil {
				h.Reason = reason.String
				if i == 0 {
					l.BudgetHeld = append(l.BudgetHeld, h)
				} else {
					l.Parked = append(l.Parked, h)
				}
			}
		}
		rows.Close()
	}
}

// readSessions adds what the database knows about running sessions: the
// engine records a session's tokens and model after its first turn and
// marks the row with the dispatch it belongs to until the run ends. When
// the supervisor cannot account for every session, rows that are in
// flight in the database are listed too.
func (l *Loop) readSessions(db *sql.DB, schema Schema, known bool) {
	if !schema.Has("session_metadata") || !columns(db, "session_metadata")["dispatch_id"] {
		return
	}
	rows, err := db.Query(`SELECT issue_id, total_tokens, model_name, updated_at FROM session_metadata WHERE dispatch_id <> ''`)
	if err != nil {
		return
	}
	type flight struct {
		tokens         int64
		model, updated string
	}
	inFlight := map[string]flight{}
	var order []string
	for rows.Next() {
		var id string
		var f flight
		if rows.Scan(&id, &f.tokens, &f.model, &f.updated) == nil {
			inFlight[id] = f
			order = append(order, id)
		}
	}
	rows.Close()
	listed := map[string]bool{}
	for i, r := range l.Running {
		listed[r.issueID] = true
		if f, ok := inFlight[r.issueID]; ok {
			l.Running[i].Tokens = f.tokens
			if f.model != "" {
				l.Running[i].Model = f.model
			}
		}
	}
	if known {
		return
	}
	for _, id := range order {
		if listed[id] {
			continue
		}
		f := inFlight[id]
		l.Running = append(l.Running, Run{
			Identifier: l.displayOf(db, id), State: "running", Tokens: f.tokens, Model: f.model, issueID: id,
		})
	}
}

// displayOf is the displayed identifier of an issue as the run history
// spells it, and the issue id when the history has none.
func (l *Loop) displayOf(db *sql.DB, issueID string) string {
	var d sql.NullString
	err := db.QueryRow(`SELECT display_identifier FROM run_history WHERE issue_id = ? AND display_identifier <> '' ORDER BY id DESC LIMIT 1`, issueID).Scan(&d)
	if err != nil || d.String == "" {
		return issueID
	}
	return d.String
}

// CountRetrying is the number of retries a workflow database holds. The
// engine persists them, so they resume after a restart. A database that
// does not exist yet holds none.
func CountRetrying(dbPath string) (int, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	db, err := OpenReadOnly(dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if !tableExists(db, "retry_entries") {
		return 0, nil
	}
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM retry_entries`).Scan(&n)
	return n, err
}

// pickedUp is when the loop first started work on an issue on its
// current pass: the earliest attempt since the last one that succeeded.
// The cutoff matters because an issue can be picked up more than once,
// and a run that finished yesterday is not the run going on now.
//
// The identifier is matched either way round because running sessions
// carry the display form (owner/repo#7) while older history rows hold
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
