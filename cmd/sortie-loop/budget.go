package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/config"
)

const (
	budgetStateSuffix   = ".budget.json"
	budgetRequestSuffix = ".budget.request."
)

var budgetRequestName = regexp.MustCompile(`^\d+-\d+$`)

// budgetState is what the supervisor remembers per workflow across
// restarts.
type budgetState struct {
	PeriodStart string `json:"period_start"`
	Paused      bool   `json:"paused"`
	PausedBy    string `json:"paused_by"`
	Override    bool   `json:"override"`
	Warned      bool   `json:"warned"`
}

type budgetUsage struct {
	tokens     int64
	unmeasured int
}

// budgetStatus is the `budget` object of a workflow in the status document.
type budgetStatus struct {
	Period         string     `json:"period"`
	PeriodStart    *time.Time `json:"period_start"`
	TokensUsed     int64      `json:"tokens_used"`
	Cap            int64      `json:"cap"`
	WarnPercent    int        `json:"warn_percent"`
	UnmeasuredRuns int        `json:"unmeasured_runs"`
	Paused         bool       `json:"paused"`
	PausedBy       string     `json:"paused_by"`
	Override       bool       `json:"override"`
	Warned         bool       `json:"warned"`
}

type budgetRequest struct {
	Action   string `json:"action"`
	Workflow string `json:"workflow"`
}

// periodStart returns the start of the weekly (ISO week, Monday) or
// monthly period that contains now, in loc.
func periodStart(now time.Time, period string, loc *time.Location) time.Time {
	t := now.In(loc)
	if period == "monthly" {
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	}
	return time.Date(t.Year(), t.Month(), t.Day()-(int(t.Weekday())+6)%7, 0, 0, 0, 0, loc)
}

var runTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parseRunTime reads a run timestamp. The engine writes RFC3339 in UTC
// ("2026-09-25T07:52:40Z"). A timestamp without a zone is read as UTC.
func parseRunTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, !x.IsZero()
	case string:
		x = strings.TrimSpace(x)
		for _, l := range runTimeLayouts {
			if t, err := time.Parse(l, x); err == nil {
				return t, true
			}
		}
	case []byte:
		return parseRunTime(string(x))
	}
	return time.Time{}, false
}

// readBudgetUsage sums total_tokens of the runs that completed (or, without
// a completion time, started) at or after start, and counts those whose
// tokens were not measured. A missing file, table or column reads as zero.
// Any other read error is returned.
func readBudgetUsage(dbPath string, start time.Time) (budgetUsage, error) {
	var u budgetUsage
	if _, err := os.Stat(dbPath); err != nil {
		return u, nil
	}
	db, err := openRO(dbPath)
	if err != nil {
		return u, err
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='run_history'`).Scan(&tables); err != nil {
		return u, err
	}
	if tables == 0 {
		return u, nil
	}
	cols, err := tableColumns(db, "run_history")
	if err != nil {
		return u, err
	}
	col := func(name, fallback string) string {
		if cols[name] {
			return name
		}
		return fallback
	}
	rows, err := db.Query(fmt.Sprintf(`SELECT COALESCE(%s,0), COALESCE(%s,1), %s, %s FROM run_history`,
		col("total_tokens", "0"), col("tokens_measured", "1"), col("completed_at", "NULL"), col("started_at", "NULL")))
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var total, measured int64
		var completed, started any
		if err := rows.Scan(&total, &measured, &completed, &started); err != nil {
			return budgetUsage{}, err
		}
		at, ok := parseRunTime(completed)
		if !ok {
			if at, ok = parseRunTime(started); !ok {
				continue
			}
		}
		if at.Before(start) {
			continue
		}
		u.tokens += total
		if measured == 0 {
			u.unmeasured++
		}
	}
	if err := rows.Err(); err != nil {
		return budgetUsage{}, err
	}
	return u, nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		cols[n] = true
	}
	return cols, rows.Err()
}

func loadBudgetStates(root string) map[string]*budgetState {
	states := map[string]*budgetState{}
	path, err := cleanupFilePath(root, budgetStateSuffix)
	if err != nil {
		return states
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return states
	}
	if err := json.Unmarshal(data, &states); err != nil || states == nil {
		fmt.Fprintln(os.Stderr, "sortie-loop: budget state ignored:", err)
		return map[string]*budgetState{}
	}
	return states
}

func (s *supervisor) saveBudgetStates() {
	s.mu.Lock()
	data, err := json.MarshalIndent(s.budgetStates, "", "  ")
	s.mu.Unlock()
	if err == nil {
		var path string
		if path, err = cleanupFilePath(s.root, budgetStateSuffix); err == nil {
			err = writeFileAtomic(path, data)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sortie-loop: budget state:", err)
	}
}

func (s *supervisor) stateLocked(name string) *budgetState {
	st := s.budgetStates[name]
	if st == nil {
		st = &budgetState{}
		s.budgetStates[name] = st
	}
	return st
}

// initBudget loads the saved state and applies the current period before
// any child starts, so a workflow that is over its cap is never started.
func (s *supervisor) initBudget(budgets map[string]config.Budget) {
	s.mu.Lock()
	s.budgets = map[string]config.Budget{}
	for n, b := range budgets {
		if _, ok := s.defs[n]; ok && b.MaxTokens > 0 {
			s.budgets[n] = b
		}
	}
	s.budgetStates = loadBudgetStates(s.root)
	s.mu.Unlock()
	s.budgetPoll(true)
}

func (s *supervisor) isPaused(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.budgetStates[name]
	return st != nil && st.Paused
}

// pauseLoop marks the workflow paused and stops its child as a requested
// stop, so the exit does not count as a crash.
func (s *supervisor) pauseLoop(name, by string) {
	s.mu.Lock()
	st := s.stateLocked(name)
	st.Paused, st.PausedBy = true, by
	if p := s.procs[name]; p != nil {
		p.stopping, p.drain, p.recycle = true, false, false
	}
	s.mu.Unlock()
	s.terminate(name)
}

func (s *supervisor) clearPause(name string) {
	s.mu.Lock()
	st := s.stateLocked(name)
	st.Paused, st.PausedBy = false, ""
	s.mu.Unlock()
}

// resumeLoop clears the pause and starts the child. A child still exiting
// from the pause is recycled when it has exited. While the run sleeps or
// ends, the next wake starts it.
func (s *supervisor) resumeLoop(name string) error {
	s.clearPause(name)
	s.mu.Lock()
	hold := s.sleeping || s.exitWhenEmpty
	p := s.procs[name]
	if p != nil && p.stopping && !hold {
		p.recycle = true
	}
	start := p == nil && !hold
	s.mu.Unlock()
	if start && s.startable(name) {
		return s.startOne(name)
	}
	return nil
}

// warnThreshold is the token count at which percent of max is reached,
// computed without overflow.
func warnThreshold(max int64, percent int) int64 {
	p := int64(percent)
	t := max / 100 * p
	r := max % 100 * p
	t += r / 100
	if r%100 != 0 {
		t++
	}
	return t
}

// budgetStep runs on every tick: it applies control requests and polls the
// budgets that are due.
func (s *supervisor) budgetStep() (int, bool) {
	changed, err := s.budgetRequests()
	if err == nil {
		var polled bool
		polled, err = s.budgetPoll(false)
		changed = changed || polled
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sortie-loop:", err)
		s.failed = true
		return 1, true
	}
	if changed {
		s.saveBudgetStates()
		s.republish()
	}
	return 0, false
}

// budgetPoll evaluates every budget that is due. With boot set it only
// brings the state up to date and starts nothing.
func (s *supervisor) budgetPoll(boot bool) (bool, error) {
	s.mu.Lock()
	names := make([]string, 0, len(s.budgets))
	for n := range s.budgets {
		names = append(names, n)
	}
	s.mu.Unlock()
	sort.Strings(names)
	polled := false
	for _, name := range names {
		b := s.budgets[name]
		now := s.clock()
		if last, ok := s.budgetPolled[name]; ok && !boot && now.Sub(last) < b.PollInterval {
			continue
		}
		s.budgetPolled[name] = now
		polled = true
		start := periodStart(now, b.Period, b.Location)
		s.mu.Lock()
		st := s.stateLocked(name)
		prev, perr := time.Parse(time.RFC3339, st.PeriodStart)
		rolled := perr != nil || !prev.Equal(start)
		release := rolled && st.Paused && st.PausedBy == "budget"
		if rolled {
			st.PeriodStart, st.Warned, st.Override = start.Format(time.RFC3339), false, false
		}
		s.mu.Unlock()
		if release {
			if boot {
				s.clearPause(name)
			} else if err := s.resumeLoop(name); err != nil {
				return polled, err
			}
		}
		u, err := readBudgetUsage(dbPathFor(s.root, s.defs[name].file), start)
		if err != nil {
			if s.budgetReadErr[name] != err.Error() {
				s.budgetReadErr[name] = err.Error()
				fmt.Fprintf(os.Stderr, "sortie-loop: budget: reading the %s database: %v\n", name, err)
			}
			continue
		}
		delete(s.budgetReadErr, name)
		s.mu.Lock()
		s.budgetUse[name] = u
		warn := !st.Warned && u.tokens >= warnThreshold(b.MaxTokens, b.WarnPercent)
		if warn {
			st.Warned = true
		}
		exceed := u.tokens >= b.MaxTokens && !st.Override && !st.Paused
		under := u.tokens < b.MaxTokens && st.Paused && st.PausedBy == "budget"
		s.mu.Unlock()
		if under {
			if boot {
				s.clearPause(name)
			} else if err := s.resumeLoop(name); err != nil {
				return polled, err
			}
		}
		if warn {
			fmt.Fprintf(os.Stderr, "sortie-loop: budget: %s used %d of %d tokens this %s period (warn at %d%%)\n",
				name, u.tokens, b.MaxTokens, strings.TrimSuffix(b.Period, "ly"), b.WarnPercent)
		}
		if exceed {
			fmt.Fprintf(os.Stderr, "sortie-loop: budget: %s reached its cap of %d tokens, pausing the loop until the next period or a resume\n",
				name, b.MaxTokens)
			s.pauseLoop(name, "budget")
		}
	}
	return polled, nil
}

// budgetRequests applies the requests written by the pause, resume and
// budget override commands, one file per request, oldest first. Files are
// consumed on every tick, whatever the sleep settings are.
func (s *supervisor) budgetRequests() (bool, error) {
	prefix, err := cleanupFilePath(s.root, budgetRequestSuffix)
	if err != nil {
		return false, nil
	}
	files, _ := filepath.Glob(prefix + "*")
	sort.Strings(files)
	changed := false
	for _, f := range files {
		if !budgetRequestName.MatchString(strings.TrimPrefix(f, prefix)) {
			continue
		}
		data, rerr := os.ReadFile(f)
		_ = os.Remove(f)
		var req budgetRequest
		if rerr != nil || json.Unmarshal(data, &req) != nil {
			fmt.Fprintln(os.Stderr, "sortie-loop: budget: dropped an unreadable request file", filepath.Base(f))
			continue
		}
		did, err := s.applyBudgetRequest(req)
		if err != nil {
			return changed, err
		}
		changed = changed || did
	}
	return changed, nil
}

func (s *supervisor) applyBudgetRequest(req budgetRequest) (bool, error) {
	if _, ok := s.defs[req.Workflow]; !ok {
		fmt.Fprintf(os.Stderr, "sortie-loop: budget: no loop %q\n", req.Workflow)
		return false, nil
	}
	s.mu.Lock()
	st := s.stateLocked(req.Workflow)
	paused, by := st.Paused, st.PausedBy
	b, budgeted := s.budgets[req.Workflow]
	over := budgeted && s.budgetUse[req.Workflow].tokens >= b.MaxTokens
	s.mu.Unlock()
	setOverride := func() {
		s.mu.Lock()
		s.stateLocked(req.Workflow).Override = true
		s.mu.Unlock()
	}
	switch req.Action {
	case "pause":
		switch {
		case !paused:
			s.pauseLoop(req.Workflow, "manual")
		case by == "budget":
			s.mu.Lock()
			s.stateLocked(req.Workflow).PausedBy = "manual"
			s.mu.Unlock()
		default:
			return false, nil
		}
	case "resume":
		if !paused {
			return false, nil
		}
		if over {
			setOverride()
		}
		return true, s.resumeLoop(req.Workflow)
	case "override":
		if !budgeted {
			fmt.Fprintf(os.Stderr, "sortie-loop: budget: %s has no budget\n", req.Workflow)
			return false, nil
		}
		setOverride()
		if paused && by == "budget" {
			return true, s.resumeLoop(req.Workflow)
		}
	default:
		return false, nil
	}
	return true, nil
}

// budgetStatusLocked is the budget object of a workflow, nil when it has no
// budget and is not paused.
func (s *supervisor) budgetStatusLocked(name string) *budgetStatus {
	b, budgeted := s.budgets[name]
	st := s.budgetStates[name]
	if !budgeted && (st == nil || !st.Paused) {
		return nil
	}
	out := &budgetStatus{}
	if budgeted {
		out.Period, out.Cap, out.WarnPercent = b.Period, b.MaxTokens, b.WarnPercent
		u := s.budgetUse[name]
		out.TokensUsed, out.UnmeasuredRuns = u.tokens, u.unmeasured
	}
	if st != nil {
		if t, err := time.Parse(time.RFC3339, st.PeriodStart); err == nil && budgeted {
			out.PeriodStart = &t
		}
		out.Paused, out.PausedBy, out.Override, out.Warned = st.Paused, st.PausedBy, st.Override, st.Warned
	}
	return out
}

// requestBudgetAction asks the supervisor of root to pause, resume or
// override a workflow. Each request is its own file, so concurrent commands
// never overwrite each other; the supervisor consumes them on its next tick.
func requestBudgetAction(root, action, workflow string) error {
	prefix, err := cleanupFilePath(root, budgetRequestSuffix)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(budgetRequest{Action: action, Workflow: workflow})
	return writeFileAtomic(fmt.Sprintf("%s%d-%d", prefix, time.Now().UnixNano(), os.Getpid()), line)
}
