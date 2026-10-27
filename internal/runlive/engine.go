package runlive

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/rollup"
)

// MaxDBEvents is the most engine event rows read for one run.
const MaxDBEvents = 1000

func hasColumn(db *sql.DB, table, col string) bool {
	var n int
	return db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n) == nil && n > 0
}

func open(dbPath string) (*sql.DB, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, err
	}
	return rollup.OpenReadOnly(dbPath)
}

// RunMeta is what the engine records for a finished run.
type RunMeta struct {
	Workspace, DispatchID, SessionID string
}

// ReadRun reads the workspace, dispatch id and session id of a finished
// run from the engine database. Columns an older engine lacks stay empty.
func ReadRun(dbPath string, runID int64) (RunMeta, error) {
	db, err := open(dbPath)
	if err != nil {
		return RunMeta{}, err
	}
	defer db.Close()
	col := func(name string) string {
		if hasColumn(db, "run_history", name) {
			return "COALESCE(" + name + ", '')"
		}
		return "''"
	}
	var m RunMeta
	err = db.QueryRow(`SELECT `+col("workspace")+`, `+col("dispatch_id")+`, `+col("session_id")+
		` FROM run_history WHERE id = ?`, runID).Scan(&m.Workspace, &m.DispatchID, &m.SessionID)
	if err == sql.ErrNoRows {
		return RunMeta{}, nil
	}
	return m, err
}

// LiveSession is the engine's record of a session in flight. The engine
// writes it after the session's first turn and clears the dispatch id when
// the run ends.
type LiveSession struct {
	SessionID, DispatchID, Model, UpdatedAt string
	Input, Output, CacheRead, CacheWrite    int64
	Total                                   int64
}

// ReadLive returns the in-flight session of an issue, and false when the
// engine has none.
func ReadLive(dbPath, issueID string) (LiveSession, bool, error) {
	db, err := open(dbPath)
	if err != nil {
		return LiveSession{}, false, err
	}
	defer db.Close()
	if !hasColumn(db, "session_metadata", "dispatch_id") {
		return LiveSession{}, false, nil
	}
	cw := "0"
	if hasColumn(db, "session_metadata", "cache_write_tokens") {
		cw = "COALESCE(cache_write_tokens, 0)"
	}
	var s LiveSession
	err = db.QueryRow(`SELECT COALESCE(session_id, ''), dispatch_id, COALESCE(model_name, ''), COALESCE(updated_at, ''),
		COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(cache_read_tokens, 0), `+cw+`, COALESCE(total_tokens, 0)
		FROM session_metadata WHERE issue_id = ? AND dispatch_id <> ''`, issueID).
		Scan(&s.SessionID, &s.DispatchID, &s.Model, &s.UpdatedAt, &s.Input, &s.Output, &s.CacheRead, &s.CacheWrite, &s.Total)
	if err == sql.ErrNoRows {
		return LiveSession{}, false, nil
	}
	return s, err == nil, err
}

// ReadRunEvents returns the engine's event rows of a finished run in
// order. The engine writes them when the run ends.
func ReadRunEvents(dbPath, dispatchID string) ([]Event, error) {
	if dispatchID == "" {
		return nil, nil
	}
	db, err := open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var n int
	if db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'run_events'`).Scan(&n) != nil || n == 0 {
		return nil, nil
	}
	rows, err := db.Query(`SELECT COALESCE(at, ''), event, COALESCE(payload, '') FROM run_events
		WHERE dispatch_id = ? ORDER BY seq LIMIT ?`, dispatchID, MaxDBEvents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var at, event, payload string
		if err := rows.Scan(&at, &event, &payload); err != nil {
			return nil, err
		}
		out = append(out, rowEvent(at, event, payload))
	}
	return out, rows.Err()
}

func rowEvent(at, event, payload string) Event {
	var p struct {
		Message   string `json:"message"`
		ToolName  string `json:"tool_name"`
		ToolError bool   `json:"tool_error"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		p.Message = payload
	}
	switch event {
	case "tool_result":
		return newEvent(at, KindToolResult, "Result: "+p.ToolName, p.Message, p.ToolName, p.ToolError)
	case "session_started":
		return newEvent(at, KindSession, "Session started", "", "", false)
	case "turn_completed":
		return newEvent(at, KindTurn, "Turn completed", p.Message, "", false)
	case "malformed":
		return newEvent(at, KindError, "Malformed event", p.Message, "", true)
	}
	return newEvent(at, KindNotification, "Assistant", p.Message, "", false)
}

// IssueLines keeps the lines of an engine log that belong to an issue and
// are logged between from and to. A zero bound leaves that side open.
func IssueLines(lines []string, issueID string, from, to time.Time) []string {
	if issueID == "" {
		return nil
	}
	key := " issue_id=" + issueID + " "
	var out []string
	for _, l := range lines {
		if !strings.Contains(l+" ", key) {
			continue
		}
		if !from.IsZero() || !to.IsZero() {
			t, err := time.Parse(time.RFC3339Nano, logValue(l, "time"))
			if err != nil || t.Before(from) || (!to.IsZero() && t.After(to)) {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// LogSession returns the id of the agent session the engine accepted last
// in the given log lines, or "".
func LogSession(lines []string) string {
	id := ""
	for _, l := range lines {
		if v := logValue(l, "accepted_session_id"); v != "" {
			id = v
		}
	}
	return id
}

var milestones = map[string]bool{
	"issue dispatched": true, "retried issue dispatched": true, "workspace prepared": true,
	"agent session started": true, "agent session id accepted": true, "turn started": true,
	"turn completed": true, "worker exiting": true, "worker run failed, scheduling retry": true,
	"handoff transition succeeded, releasing claim": true,
}

// LogEvents turns the milestone lines of an engine log, and its warnings
// and errors, into events.
func LogEvents(lines []string) []Event {
	var out []Event
	for _, l := range lines {
		msg := logValue(l, "msg")
		level := logValue(l, "level")
		if !milestones[msg] && level != "WARN" && level != "ERROR" {
			continue
		}
		bad := level == "WARN" || level == "ERROR"
		out = append(out, newEvent(logValue(l, "time"), map[bool]string{true: KindError, false: KindNotification}[bad], msg, "", "", bad))
	}
	return out
}

// logValue returns the value of key in a logfmt line, unquoted.
func logValue(line, key string) string {
	rest := line
	for {
		i := strings.Index(rest, key+"=")
		if i < 0 {
			return ""
		}
		if i > 0 && rest[i-1] != ' ' {
			rest = rest[i+len(key)+1:]
			continue
		}
		v := rest[i+len(key)+1:]
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				return ""
			}
			return v[1 : 1+end]
		}
		v, _, _ = strings.Cut(v, " ")
		return v
	}
}
