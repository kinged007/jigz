package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	dispatched7 = `time=2026-10-26T18:30:13.585Z level=INFO msg="issue dispatched" issue_id=7 issue_identifier=7 agent_kind=mock rule_name=plan model="" effort=""`
	retried7    = `time=2026-10-26T18:34:24.710Z level=INFO msg="retried issue dispatched" issue_id=7 issue_identifier=7 attempt=1 agent_kind=mock rule_name=plan model="" effort=""`
	turn1of7    = `time=2026-10-26T18:30:38.613Z level=INFO msg="turn completed" issue_id=7 issue_identifier=7 session_id=s turn_number=1 max_turns=4`
	exiting7    = `time=2026-10-26T18:30:38.625Z level=INFO msg="worker exiting" issue_id=7 issue_identifier=7 session_id=s exit_kind=normal turns_completed=1`
	failed7     = `time=2026-10-26T18:34:14.702Z level=WARN msg="worker run failed, scheduling retry" issue_id=7 issue_identifier=7 session_id=s error="agent turn 1: agent: turn_failed: x" next_attempt=1 delay_ms=10000`
	tickIdle    = `time=2026-10-26T18:30:38.830Z level=INFO msg="tick completed" candidates=0 dispatched=0 running=0 retrying=0 held_by_blockers=0`
	tickBusy    = `time=2026-10-26T18:30:14.593Z level=INFO msg="tick completed" candidates=0 dispatched=0 running=1 retrying=0 held_by_blockers=0`
)

func fedTracker(lines ...string) *liveTracker {
	t := newLiveTracker(func(id string) string { return "acme/app#" + id }, nil)
	for _, l := range lines {
		t.line(l)
	}
	return t
}

func TestTrackerFollowsASessionFromDispatchToExit(t *testing.T) {
	tr := fedTracker(tickIdle)
	if s, known := tr.snapshot(); len(s) != 0 || !known {
		t.Fatalf("after the first tick: %+v known %v, want none and known", s, known)
	}
	tr.line(dispatched7)
	s, known := tr.snapshot()
	if len(s) != 1 || !known || s[0].IssueID != "7" || s[0].Identifier != "acme/app#7" || s[0].Attempt != 1 {
		t.Fatalf("after dispatch: %+v known %v, want session 7, attempt 1", s, known)
	}
	tr.line(turn1of7)
	if s, _ := tr.snapshot(); s[0].Turns != 1 {
		t.Errorf("turns = %d, want 1", s[0].Turns)
	}
	tr.line(exiting7)
	if s, known := tr.snapshot(); len(s) != 0 || !known {
		t.Errorf("after exit: %+v known %v, want none", s, known)
	}
}

func TestTrackerSeesAFailedRunEndAndTheRetryStart(t *testing.T) {
	tr := fedTracker(tickIdle, dispatched7, failed7)
	if s, _ := tr.snapshot(); len(s) != 0 {
		t.Fatalf("after the failure: %+v, want none", s)
	}
	tr.line(retried7)
	if s, _ := tr.snapshot(); len(s) != 1 || s[0].Attempt != 2 {
		t.Errorf("after the retry: %+v, want attempt 2", s)
	}
}

func TestTrackerIsUnknownBeforeTheFirstTick(t *testing.T) {
	tr := fedTracker(dispatched7)
	if s, known := tr.snapshot(); len(s) != 1 || known {
		t.Errorf("%+v known %v, want the session but unknown", s, known)
	}
	var none *liveTracker
	if _, known := none.snapshot(); known {
		t.Error("a missing tracker is known")
	}
}

func TestTrackerIsUnknownWhenATickCountsAnUnseenSession(t *testing.T) {
	tr := fedTracker(tickBusy)
	if s, known := tr.snapshot(); len(s) != 0 || known {
		t.Errorf("%+v known %v, want unknown", s, known)
	}
	tr.line(tickIdle)
	if _, known := tr.snapshot(); !known {
		t.Error("still unknown after a tick that counts none")
	}
}

func TestTrackerSettlesATickThatCameBeforeTheStartLine(t *testing.T) {
	tr := fedTracker(tickBusy)
	tr.line(dispatched7)
	if s, known := tr.snapshot(); len(s) != 1 || !known {
		t.Errorf("%+v known %v, want session 7 and known", s, known)
	}
}

func TestTrackerDropsASessionAnIdleTickDoesNotCount(t *testing.T) {
	now := time.Now()
	tr := fedTracker()
	tr.now = func() time.Time { return now }
	tr.line(dispatched7)
	now = now.Add(time.Second)
	tr.line(tickIdle)
	if s, _ := tr.snapshot(); len(s) != 1 {
		t.Fatalf("a session of a second ago was dropped: %+v", s)
	}
	now = now.Add(tickDropAfter + time.Second)
	tr.line(tickIdle)
	if s, known := tr.snapshot(); len(s) != 0 || !known {
		t.Errorf("%+v known %v, want the stale session dropped", s, known)
	}
}

func TestTrackerReadsLinesAcrossWrites(t *testing.T) {
	tr := newLiveTracker(nil, nil)
	w := tr.writer()
	line := dispatched7 + "\n" + tickBusy + "\n"
	for i := 0; i < len(line); i += 7 {
		w.Write([]byte(line[i:min(i+7, len(line))]))
	}
	if s, known := tr.snapshot(); len(s) != 1 || !known || s[0].Identifier != "7" {
		t.Errorf("%+v known %v, want session 7", s, known)
	}
	w.Write([]byte(strings.Repeat("x", maxLogLine+1)))
	w.Write([]byte("\n" + exiting7 + "\n"))
	if s, _ := tr.snapshot(); len(s) != 0 {
		t.Errorf("a line after an oversized one was lost: %+v", s)
	}
}

func TestTrackerReportsChanges(t *testing.T) {
	changes := 0
	tr := newLiveTracker(nil, func() { changes++ })
	for _, l := range []string{tickIdle, tickIdle, dispatched7, turn1of7, tickBusy, exiting7, exiting7} {
		tr.line(l)
	}
	if changes != 4 {
		t.Errorf("changes = %d, want 4 (first tick, dispatch, turn, exit)", changes)
	}
}

func quietSup(t *testing.T, live *liveTracker) *supervisor {
	t.Helper()
	root := t.TempDir()
	sup := newTestSup(t, root, "plan")
	sup.procs["plan"] = &loopProc{def: loopDef{name: "plan", file: "WORKFLOW.plan.md"}, live: live, since: time.Now().Add(-time.Hour)}
	return sup
}

func writeRetry(t *testing.T, root string) {
	t.Helper()
	path := dbPathFor(root, "WORKFLOW.plan.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []string{
		`CREATE TABLE retry_entries (issue_id TEXT PRIMARY KEY, identifier TEXT, attempt INTEGER, due_at_ms INTEGER, error TEXT)`,
		`INSERT INTO retry_entries VALUES ('7', '7', 1, 0, 'x')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
}

func TestALoopWithNothingInFlightIsIdle(t *testing.T) {
	sup := quietSup(t, fedTracker(tickIdle))
	if idle, known := sup.loopQuiet("plan"); !known || !idle {
		t.Errorf("an empty loop reported idle=%v known=%v, want both true", idle, known)
	}
}

// The database holds nothing for a session that has not ended a turn, so
// the first turn is judged from the log alone.
func TestALoopInItsFirstTurnIsNotIdle(t *testing.T) {
	sup := quietSup(t, fedTracker(tickIdle, dispatched7))
	if idle, known := sup.loopQuiet("plan"); !known || idle {
		t.Errorf("a loop with a session reported idle=%v known=%v, want idle=false", idle, known)
	}
	if active, known := sup.loopSessions("plan"); !known || !active {
		t.Errorf("sessions: active=%v known=%v, want both true", active, known)
	}
}

func TestAPendingRetryIsNotIdleButHasNoSession(t *testing.T) {
	sup := quietSup(t, fedTracker(tickIdle))
	writeRetry(t, sup.root)
	if idle, known := sup.loopQuiet("plan"); !known || idle {
		t.Errorf("a loop with a pending retry reported idle=%v known=%v, want idle=false", idle, known)
	}
	if active, known := sup.loopSessions("plan"); !known || active {
		t.Errorf("sessions: active=%v known=%v, want a retry not to count", active, known)
	}
}

func TestALoopTheSupervisorCannotAccountForIsUnknownNotIdle(t *testing.T) {
	for name, live := range map[string]*liveTracker{
		"no tick yet":       fedTracker(),
		"unseen session":    fedTracker(tickBusy),
		"no tracker at all": nil,
	} {
		sup := quietSup(t, live)
		if idle, known := sup.loopQuiet("plan"); known || idle {
			t.Errorf("%s: idle=%v known=%v, want both false", name, idle, known)
		}
	}
	sup := quietSup(t, fedTracker(tickIdle))
	if idle, known := sup.loopQuiet("missing"); known || idle {
		t.Errorf("an unknown loop reported idle=%v known=%v, want both false", idle, known)
	}
}

func TestLoopArgvAlwaysDisablesTheEngineServer(t *testing.T) {
	got := strings.Join(engineArgs("/env", "/wf"), " ")
	if got != "--env-file /env --port 0 /wf" {
		t.Errorf("argv = %q", got)
	}
}

// A started loop is launched with --port 0 and its log feeds the tracker.
func TestStartedLoopRunsWithPortZeroAndIsTracked(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "repo: owner/name\n")
	mustWrite(t, filepath.Join(root, ".jigz", "workflows", "WORKFLOW.plan.md"), "body\n")
	argvFile := filepath.Join(root, "argv")
	sup := newTestSup(t, root, "plan")
	sup.bin = writeScript(t, root, "sortie", "echo \"$@\" > "+argvFile+"\n"+
		"echo '"+tickIdle+"'\necho '"+dispatched7+"'\necho '"+tickBusy+"' >&2\nwhile true; do sleep 1; done\n")
	if err := sup.startOne("plan"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.shutdown)

	deadline := time.Now().Add(10 * time.Second)
	for {
		if active, known := sup.loopSessions("plan"); known && active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session in the child's log was not tracked")
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	if args := strings.Fields(string(raw)); len(args) < 4 || args[2] != "--port" || args[3] != "0" {
		t.Errorf("engine argv = %q, want --port 0", raw)
	}
	doc := sup.statusDoc("running")
	if len(doc.Workflows) != 1 || !doc.Workflows[0].SessionsKnown || len(doc.Workflows[0].Sessions) != 1 ||
		doc.Workflows[0].Sessions[0].Identifier != "owner/name#7" {
		t.Errorf("status workflows = %+v, want the tracked session as owner/name#7", doc.Workflows)
	}
}
