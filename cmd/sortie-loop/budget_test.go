package main

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/sortie-loop/internal/config"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestPeriodStart(t *testing.T) {
	jhb := mustLoc(t, "Africa/Johannesburg")
	for _, c := range []struct {
		name, period string
		now          time.Time
		loc          *time.Location
		want         string
	}{
		{"weekly midweek", "weekly", time.Date(2026, 10, 14, 15, 0, 0, 0, time.UTC), time.UTC, "2026-10-12T00:00:00Z"},
		{"weekly monday", "weekly", time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), time.UTC, "2026-10-12T00:00:00Z"},
		{"weekly sunday belongs to the previous Monday", "weekly", time.Date(2026, 10, 18, 23, 59, 0, 0, time.UTC), time.UTC, "2026-10-12T00:00:00Z"},
		{"weekly across a month", "weekly", time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), time.UTC, "2026-10-26T00:00:00Z"},
		{"weekly across a year", "weekly", time.Date(2027, 1, 1, 10, 0, 0, 0, time.UTC), time.UTC, "2026-12-28T00:00:00Z"},
		{"weekly timezone ahead of UTC", "weekly", time.Date(2026, 10, 18, 23, 30, 0, 0, time.UTC), jhb, "2026-10-19T00:00:00+02:00"},
		{"monthly", "monthly", time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC), time.UTC, "2026-10-01T00:00:00Z"},
		{"monthly rollover", "monthly", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.UTC, "2026-11-01T00:00:00Z"},
		{"monthly timezone ahead of UTC", "monthly", time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC), jhb, "2026-11-01T00:00:00+02:00"},
		{"monthly timezone behind UTC", "monthly", time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC), mustLoc(t, "America/New_York"), "2026-10-01T00:00:00-04:00"},
	} {
		if got := periodStart(c.now, c.period, c.loc).Format(time.RFC3339); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func openRW(t *testing.T, path string) *sql.DB {
	t.Helper()
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func execAll(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

const runHistoryDDL = `CREATE TABLE run_history (id INTEGER PRIMARY KEY, total_tokens INTEGER, tokens_measured INTEGER, started_at TEXT, completed_at TEXT)`

func TestReadBudgetUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	execAll(t, openRW(t, path), runHistoryDDL,
		`INSERT INTO run_history (total_tokens, tokens_measured, started_at, completed_at) VALUES
		(100, 1, '2026-10-11T10:00:00Z', '2026-10-12T00:00:00Z'),
		(50, 1, '2026-10-11T23:00:00Z', '2026-10-11T23:59:59Z'),
		(200, 0, '2026-10-13T10:00:00Z', '2026-10-13 11:00:00'),
		(0, 0, '2026-10-14T10:00:00Z', ''),
		(7, 1, '2026-10-14T10:00:00Z', NULL),
		(900, 1, 'garbage', 'garbage')`)
	got, err := readBudgetUsage(path, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.tokens != 307 || got.unmeasured != 2 {
		t.Errorf("got %+v, want 307 tokens and 2 unmeasured runs", got)
	}
}

func TestReadBudgetUsageToleratesMissingSchema(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if got, err := readBudgetUsage(filepath.Join(dir, "absent.db"), start); got != (budgetUsage{}) || err != nil {
		t.Errorf("missing file: %+v %v", got, err)
	}
	noTable := filepath.Join(dir, "a.db")
	execAll(t, openRW(t, noTable), `CREATE TABLE other (x INTEGER)`)
	if got, err := readBudgetUsage(noTable, start); got != (budgetUsage{}) || err != nil {
		t.Errorf("missing table: %+v %v", got, err)
	}
	noCols := filepath.Join(dir, "b.db")
	execAll(t, openRW(t, noCols),
		`CREATE TABLE run_history (id INTEGER PRIMARY KEY, started_at TEXT)`,
		`INSERT INTO run_history (started_at) VALUES ('2026-10-13T00:00:00Z')`)
	if got, err := readBudgetUsage(noCols, start); got != (budgetUsage{}) || err != nil {
		t.Errorf("missing token columns: %+v %v", got, err)
	}
	noTimes := filepath.Join(dir, "c.db")
	execAll(t, openRW(t, noTimes),
		`CREATE TABLE run_history (id INTEGER PRIMARY KEY, total_tokens INTEGER)`,
		`INSERT INTO run_history (total_tokens) VALUES (5)`)
	if got, err := readBudgetUsage(noTimes, start); got != (budgetUsage{}) || err != nil {
		t.Errorf("missing time columns: %+v %v", got, err)
	}
}

type budgetRig struct {
	sup  *supervisor
	root string
	now  time.Time
	db   *sql.DB
}

func newBudgetRig(t *testing.T, maxTokens int64) *budgetRig {
	t.Helper()
	sup, root := startedSup(t, "while true; do sleep 1; done\n")
	r := &budgetRig{sup: sup, root: root, now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	sup.clock = func() time.Time { return r.now }
	r.db = openRW(t, dbPathFor(root, "WORKFLOW.plan.md"))
	execAll(t, r.db, runHistoryDDL)
	if maxTokens > 0 {
		sup.initBudget(map[string]config.Budget{"plan": {
			Period: "weekly", Location: time.UTC, MaxTokens: maxTokens, WarnPercent: 80, PollInterval: time.Minute,
		}})
	}
	return r
}

func (r *budgetRig) addRun(t *testing.T, tokens int, measured int, at time.Time) {
	t.Helper()
	execAll(t, r.db, `INSERT INTO run_history (total_tokens, tokens_measured, started_at, completed_at) VALUES (`+
		itoa(tokens)+`,`+itoa(measured)+`,'`+at.Format(time.RFC3339)+`','`+at.Format(time.RFC3339)+`')`)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (r *budgetRig) step(t *testing.T, d time.Duration) {
	t.Helper()
	r.now = r.now.Add(d)
	if code, done := r.sup.budgetStep(); done {
		t.Fatalf("budgetStep ended the run with code %d", code)
	}
}

// settle delivers the exit events of stopped children, as run does.
func (r *budgetRig) settle(t *testing.T) {
	t.Helper()
	for !r.sup.empty() {
		select {
		case ev := <-r.sup.events:
			if code, done := r.sup.onExit(ev); done {
				t.Fatalf("a requested stop ended the run with code %d", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("child did not exit")
		}
	}
}

func (r *budgetRig) workflow(t *testing.T) workflowStatus {
	t.Helper()
	return readStatus(t, r.root).Workflows[0]
}

func TestBudgetWarnsOnceThenPausesAtCap(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.step(t, time.Minute)
	if w := r.workflow(t); w.State != "running" || w.Budget == nil || w.Budget.TokensUsed != 0 || w.Budget.Cap != 1000 {
		t.Fatalf("initial: %+v %+v", w, w.Budget)
	}
	r.addRun(t, 800, 1, r.now)
	r.addRun(t, 5, 0, r.now)
	r.step(t, time.Minute)
	w := r.workflow(t)
	if !w.Budget.Warned || w.State != "running" || w.Budget.TokensUsed != 805 || w.Budget.UnmeasuredRuns != 1 ||
		w.Budget.Period != "weekly" || w.Budget.WarnPercent != 80 || w.Budget.PeriodStart == nil {
		t.Errorf("after warn: %+v %+v", w, w.Budget)
	}
	r.addRun(t, 195, 1, r.now)
	r.step(t, time.Minute)
	if w = r.workflow(t); w.State != "paused" || !w.Budget.Paused || w.Budget.PausedBy != "budget" {
		t.Fatalf("at cap: %+v %+v", w, w.Budget)
	}
	r.settle(t)
	if r.sup.failed || r.sup.finished() {
		t.Error("a budget pause was treated as a crash or ended the run")
	}
	if w = r.workflow(t); w.State != "paused" || w.LastExitCode == nil {
		t.Errorf("after exit: %+v", w)
	}
	r.step(t, time.Minute)
	if !r.sup.empty() {
		t.Error("the paused workflow was started again")
	}
}

func TestBudgetThrottledToPollInterval(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.step(t, time.Minute)
	r.addRun(t, 5000, 1, r.now)
	r.step(t, 10*time.Second)
	if r.workflow(t).State != "running" {
		t.Error("polled before poll_interval")
	}
	r.step(t, time.Minute)
	if r.workflow(t).State != "paused" {
		t.Error("did not poll after poll_interval")
	}
}

func TestBudgetRolloverRestartsTheChild(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.addRun(t, 5000, 1, r.now)
	r.step(t, time.Minute)
	r.settle(t)
	r.step(t, 5*24*time.Hour)
	if r.sup.empty() {
		t.Fatal("rollover did not start the child")
	}
	w := r.workflow(t)
	if w.State != "running" || w.Budget.Paused || w.Budget.Warned || w.Budget.TokensUsed != 0 {
		t.Errorf("after rollover: %+v %+v", w, w.Budget)
	}
}

func TestBudgetStatePersistsAndHoldsTheChildDown(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.addRun(t, 5000, 1, r.now)
	r.step(t, time.Minute)
	r.settle(t)
	path, _ := cleanupFilePath(r.root, budgetStateSuffix)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]budgetState
	if err := json.Unmarshal(data, &saved); err != nil || !saved["plan"].Paused || saved["plan"].PausedBy != "budget" || saved["plan"].PeriodStart == "" {
		t.Fatalf("saved state %s (%v)", data, err)
	}

	next := newSupervisor(r.root, r.sup.bin, []loopDef{{name: "plan", file: "WORKFLOW.plan.md"}}, []int{0})
	next.clock = r.sup.clock
	next.initBudget(map[string]config.Budget{"plan": r.sup.budgets["plan"]})
	if err := next.startAll([]string{"plan"}); err != nil {
		t.Fatal(err)
	}
	if !next.empty() {
		t.Error("a paused workflow was started at boot")
	}
	next.publishStatus("running")
	if w := readStatus(t, r.root).Workflows; len(w) != 1 || w[0].State != "paused" || w[0].Budget == nil || w[0].Budget.TokensUsed != 5000 {
		t.Errorf("status of a workflow that never started: %+v", w)
	}
}

func TestBudgetStateRolloverAtBootKeepsManualPause(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.sup.pauseLoop("plan", "manual")
	r.sup.saveBudgetStates()
	r.settle(t)
	r.now = r.now.Add(30 * 24 * time.Hour)
	next := newSupervisor(r.root, r.sup.bin, []loopDef{{name: "plan", file: "WORKFLOW.plan.md"}}, []int{0})
	next.clock = r.sup.clock
	next.initBudget(map[string]config.Budget{"plan": r.sup.budgets["plan"]})
	if !next.isPaused("plan") {
		t.Error("a manual pause ended at rollover")
	}
}

func TestPauseResumeRequests(t *testing.T) {
	r := newBudgetRig(t, 0)
	pid := r.sup.procs["plan"].cmd.Process.Pid
	if err := requestBudgetAction(r.root, "pause", "plan"); err != nil {
		t.Fatal(err)
	}
	r.step(t, 2*time.Second)
	if w := r.workflow(t); w.State != "paused" || w.Budget == nil || w.Budget.PausedBy != "manual" {
		t.Fatalf("after pause: %+v %+v", w, w.Budget)
	}
	r.settle(t)
	if r.sup.failed || r.sup.finished() {
		t.Error("a manual pause was treated as a crash")
	}
	if err := requestBudgetAction(r.root, "resume", "plan"); err != nil {
		t.Fatal(err)
	}
	r.step(t, 2*time.Second)
	if w := r.workflow(t); w.State != "running" || w.Budget != nil {
		t.Errorf("after resume: %+v %+v", w, w.Budget)
	}
	if r.sup.procs["plan"] == nil || r.sup.procs["plan"].cmd.Process.Pid == pid {
		t.Error("resume did not start a new child")
	}
}

func TestResumeWhileTheChildIsStillExiting(t *testing.T) {
	r := newBudgetRig(t, 0)
	requestBudgetAction(r.root, "pause", "plan")
	requestBudgetAction(r.root, "resume", "plan")
	r.step(t, 2*time.Second)
	select {
	case ev := <-r.sup.events:
		if code, done := r.sup.onExit(ev); done {
			t.Fatalf("the stop ended the run with code %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit")
	}
	if r.sup.empty() || r.sup.isPaused("plan") {
		t.Error("the child was not restarted after pause and resume in one batch")
	}
}

func TestRequestsAreConsumedWithSleepDisabled(t *testing.T) {
	r := newBudgetRig(t, 0)
	if r.sup.sleepCfg.Enabled {
		t.Fatal("rig has sleep enabled")
	}
	requestBudgetAction(r.root, "pause", "plan")
	r.step(t, 2*time.Second)
	if !r.sup.isPaused("plan") {
		t.Error("the request was not consumed")
	}
	prefix, _ := cleanupFilePath(r.root, budgetRequestSuffix)
	if files, _ := filepath.Glob(prefix + "*"); len(files) != 0 {
		t.Error("the request file was left behind")
	}
}

func TestBudgetOverride(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.addRun(t, 5000, 1, r.now)
	r.step(t, time.Minute)
	r.settle(t)
	requestBudgetAction(r.root, "override", "plan")
	r.step(t, 2*time.Second)
	if r.sup.empty() {
		t.Fatal("override did not start the child")
	}
	w := r.workflow(t)
	if w.State != "running" || !w.Budget.Override || w.Budget.Paused {
		t.Errorf("after override: %+v %+v", w, w.Budget)
	}
	r.step(t, 2*time.Minute)
	if r.workflow(t).State != "running" {
		t.Error("the cap was enforced during an override")
	}
	r.step(t, 7*24*time.Hour)
	if w = r.workflow(t); w.Budget.Override {
		t.Errorf("override survived the rollover: %+v", w.Budget)
	}
}

func TestResumeOfABudgetPauseActsAsOverride(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.addRun(t, 5000, 1, r.now)
	r.step(t, time.Minute)
	r.settle(t)
	requestBudgetAction(r.root, "resume", "plan")
	r.step(t, 2*time.Second)
	r.step(t, 2*time.Minute)
	if w := r.workflow(t); w.State != "running" || !w.Budget.Override {
		t.Errorf("after resume: %+v %+v", w, w.Budget)
	}
}

func TestOverrideWithoutBudgetIsIgnored(t *testing.T) {
	r := newBudgetRig(t, 0)
	requestBudgetAction(r.root, "override", "plan")
	r.step(t, 2*time.Second)
	if w := r.workflow(t); w.Budget != nil {
		t.Errorf("override created a budget: %+v", w.Budget)
	}
}

func TestSleepDoesNotWakePausedWorkflows(t *testing.T) {
	r := newSleepRig(t, true)
	r.sup.pauseLoop("plan", "manual")
	r.settle(t)
	r.sup.sleep(r.now)
	r.sup.wake(r.now)
	if !r.sup.empty() {
		t.Error("wake started a paused workflow")
	}
	if w := readStatus(t, r.root).Workflows[0]; w.State != "paused" {
		t.Errorf("state %q, want paused", w.State)
	}
}

func TestRecycleDoesNotStartAPausedWorkflow(t *testing.T) {
	r := newBudgetRig(t, 0)
	r.sup.stopAll(false, true)
	r.sup.pauseLoop("plan", "manual")
	r.settle(t)
	if !r.sup.empty() {
		t.Error("a recycle started a paused workflow")
	}
}

func TestControlWorkflowCommand(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.sup.republish()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				r.sup.budgetStep()
			}
		}
	}()
	defer func() { close(stop); <-done }()

	res, err := controlWorkflow(r.root, "pause", "plan")
	if err != nil || !res.Changed || !res.Paused {
		t.Fatalf("pause: %+v %v", res, err)
	}
	if res, err = controlWorkflow(r.root, "pause", "plan"); err != nil || res.Changed || !res.Paused {
		t.Errorf("second pause: %+v %v", res, err)
	}
	if res, err = controlWorkflow(r.root, "resume", "plan"); err != nil || !res.Changed || res.Paused {
		t.Errorf("resume: %+v %v", res, err)
	}
	if res, err = controlWorkflow(r.root, "resume", "plan"); err != nil || res.Changed {
		t.Errorf("second resume: %+v %v", res, err)
	}
	if res, err = controlWorkflow(r.root, "override", "plan"); err != nil || !res.Changed || !res.Override {
		t.Errorf("override: %+v %v", res, err)
	}
	if res, err = controlWorkflow(r.root, "override", "plan"); err != nil || res.Changed {
		t.Errorf("second override: %+v %v", res, err)
	}
	if _, err = controlWorkflow(r.root, "pause", "nope"); err == nil || !strings.Contains(err.Error(), "plan") {
		t.Errorf("unknown workflow: %v", err)
	}
}

func TestControlWorkflowWithoutBudgetOrSupervisor(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, err := controlWorkflow(t.TempDir(), "pause", "plan"); err == nil {
		t.Error("pause without a supervisor succeeded")
	}
	r := newBudgetRig(t, 0)
	r.sup.republish()
	if _, err := controlWorkflow(r.root, "override", "plan"); err == nil || !strings.Contains(err.Error(), "no budget") {
		t.Errorf("override without a budget: %v", err)
	}
}

func TestControlWorkflowTimesOut(t *testing.T) {
	old := budgetCmdWait
	budgetCmdWait = 300 * time.Millisecond
	defer func() { budgetCmdWait = old }()
	r := newBudgetRig(t, 0)
	r.sup.republish()
	if res, err := controlWorkflow(r.root, "pause", "plan"); err == nil || res.Changed {
		t.Errorf("res %+v err %v, want a timeout", res, err)
	}
}

func TestParseArgsBudgetCommands(t *testing.T) {
	for _, name := range []string{"pause", "resume", "budget"} {
		c, err := parseArgs([]string{name, "plan", "--json"})
		if err != nil || c.remote != name || len(c.remoteArgs) != 2 {
			t.Errorf("%s parsed as %+v, err %v", name, c, err)
		}
	}
}

func TestBudgetSummary(t *testing.T) {
	if budgetSummary(nil) != "" || budgetSummary(&budgetStatus{}) != "" {
		t.Error("a workflow without a budget printed a line")
	}
	got := budgetSummary(&budgetStatus{Period: "weekly", TokensUsed: 1500, Cap: 2000000, UnmeasuredRuns: 2, Paused: true, PausedBy: "budget"})
	for _, want := range []string{"1.5k", "2.00M", "weekly", "2 unmeasured", "paused by budget"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
}

func TestParseRunTimeRealFormat(t *testing.T) {
	got, ok := parseRunTime("2026-09-25T07:52:40Z")
	if !ok || !got.Equal(time.Date(2026, 9, 25, 7, 52, 40, 0, time.UTC)) {
		t.Errorf("engine format: %v %v", got, ok)
	}
	got, ok = parseRunTime("2026-09-25 07:52:40")
	if !ok || got.Location() != time.UTC || got.Hour() != 7 {
		t.Errorf("zone-less timestamp: %v %v, want UTC", got, ok)
	}
}

func TestReadBudgetUsageReportsReadErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(path, []byte(strings.Repeat("not a database ", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readBudgetUsage(path, time.Now()); err == nil {
		t.Error("a corrupt database read without an error")
	}
}

func TestBudgetReadErrorKeepsPreviousUsage(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.addRun(t, 500, 1, r.now)
	r.step(t, time.Minute)
	if got := r.workflow(t).Budget.TokensUsed; got != 500 {
		t.Fatalf("used %d, want 500", got)
	}
	path := dbPathFor(r.root, "WORKFLOW.plan.md")
	r.db.Close()
	if err := os.WriteFile(path, []byte(strings.Repeat("not a database ", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	r.step(t, time.Minute)
	w := r.workflow(t)
	if w.Budget.TokensUsed != 500 || w.State != "running" {
		t.Errorf("after a read error: %+v %+v", w, w.Budget)
	}
}

func TestPausedBudgetReleasesWhenUnderTheCap(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.addRun(t, 1500, 1, r.now)
	r.step(t, time.Minute)
	r.settle(t)
	b := r.sup.budgets["plan"]
	b.MaxTokens = 5000
	r.sup.budgets["plan"] = b
	r.step(t, time.Minute)
	if r.sup.empty() {
		t.Fatal("raising the cap did not start the child")
	}
	if w := r.workflow(t); w.State != "running" || w.Budget.Paused {
		t.Errorf("after release: %+v %+v", w, w.Budget)
	}
}

func TestManualResumeOverTheCapSetsOverride(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.addRun(t, 5000, 1, r.now)
	r.step(t, time.Minute)
	r.settle(t)
	requestBudgetAction(r.root, "pause", "plan")
	r.step(t, 2*time.Second)
	if w := r.workflow(t); w.Budget.PausedBy != "manual" {
		t.Fatalf("pause of a budget pause: %+v", w.Budget)
	}
	requestBudgetAction(r.root, "resume", "plan")
	r.step(t, 2*time.Second)
	r.step(t, 5*time.Minute)
	if w := r.workflow(t); w.State != "running" || !w.Budget.Override {
		t.Errorf("after resume: %+v %+v", w, w.Budget)
	}
}

func TestPauseOfABudgetPauseBecomesManual(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.addRun(t, 5000, 1, r.now)
	r.step(t, time.Minute)
	r.settle(t)
	r.sup.republish()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				if ch, _ := r.sup.budgetRequests(); ch {
					r.sup.republish()
				}
			}
		}
	}()
	res, err := controlWorkflow(r.root, "pause", "plan")
	close(stop)
	<-done
	if err != nil || !res.Changed || !res.Paused {
		t.Fatalf("pause: %+v %v", res, err)
	}
	r.step(t, 8*24*time.Hour)
	if !r.sup.isPaused("plan") || r.sup.states("plan").PausedBy != "manual" {
		t.Error("the converted pause ended at rollover")
	}
}

func (s *supervisor) states(name string) budgetState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.stateLocked(name)
}

func TestEachRequestIsItsOwnFile(t *testing.T) {
	r := newBudgetRig(t, 0)
	for i := 0; i < 3; i++ {
		if err := requestBudgetAction(r.root, "pause", "plan"); err != nil {
			t.Fatal(err)
		}
	}
	prefix, _ := cleanupFilePath(r.root, budgetRequestSuffix)
	files, _ := filepath.Glob(prefix + "*")
	if len(files) != 3 {
		t.Fatalf("%d request files, want 3: %v", len(files), files)
	}
	old := prefix + "1-1"
	os.WriteFile(old, []byte("not json"), 0o644)
	requestBudgetAction(r.root, "resume", "plan")
	r.step(t, 2*time.Second)
	if files, _ = filepath.Glob(prefix + "*"); len(files) != 0 {
		t.Errorf("request files left behind: %v", files)
	}
	if r.sup.isPaused("plan") {
		t.Error("requests were not applied in order (pause x3 then resume)")
	}
}

func TestPeriodStartChangeResetsOnlyOnANewPeriod(t *testing.T) {
	r := newBudgetRig(t, 1000)
	r.addRun(t, 900, 1, r.now)
	r.step(t, time.Minute)
	requestBudgetAction(r.root, "override", "plan")
	r.step(t, 2*time.Second)
	if st := r.sup.states("plan"); !st.Warned || !st.Override {
		t.Fatalf("state %+v", st)
	}
	r.sup.mu.Lock()
	r.sup.budgetStates["plan"].PeriodStart = periodStart(r.now, "weekly", time.UTC).In(time.FixedZone("x", 3600)).Format(time.RFC3339)
	r.sup.mu.Unlock()
	r.step(t, time.Minute)
	if st := r.sup.states("plan"); !st.Warned || !st.Override {
		t.Errorf("the same instant in another notation reset the state: %+v", st)
	}
	b := r.sup.budgets["plan"]
	b.Location = mustLoc(t, "Pacific/Kiritimati")
	r.sup.budgets["plan"] = b
	r.step(t, time.Minute)
	if st := r.sup.states("plan"); st.Override || strings.Contains(st.PeriodStart, "+14:00") == false {
		t.Errorf("a new period start kept the state: %+v", st)
	}
}

func TestWarnThresholdDoesNotOverflow(t *testing.T) {
	for _, c := range []struct {
		max  int64
		pct  int
		want int64
	}{{1000, 80, 800}, {101, 80, 81}, {7, 50, 4}, {100, 100, 100}, {math.MaxInt64, 100, math.MaxInt64}} {
		if got := warnThreshold(c.max, c.pct); got != c.want {
			t.Errorf("warnThreshold(%d, %d) = %d, want %d", c.max, c.pct, got, c.want)
		}
	}
	if warnThreshold(math.MaxInt64/2, 80) <= 0 {
		t.Error("overflow")
	}
}
