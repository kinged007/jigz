package main

import (
	"bytes"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kinged007/jigz/internal/rollup"
)

// maxLogLine bounds a line the tracker keeps while it waits for the
// newline.
const maxLogLine = 64 << 10

// tickDropAfter is how long a session survives a poll tick that counts
// no running session. The engine logs the tick after the dispatches it
// made, so a session older than this that the tick does not count has
// ended without a line the tracker knows.
const tickDropAfter = 5 * time.Second

// liveTracker follows the sessions of one engine child through the lines
// the engine logs. The workflow database holds a run only when it has
// ended and a session's tokens only after its first turn, so the log is
// the only record of a session between its dispatch and its first turn.
type liveTracker struct {
	mu       sync.Mutex
	display  func(issue string) string
	changed  func()
	now      func() time.Time
	sessions map[string]*rollup.Session
	ticked   bool
	// unaccounted is the number of running sessions the last poll tick
	// counted beyond the ones the tracker has seen start. A start line
	// that arrives after the tick that counted it settles one.
	unaccounted int
}

func newLiveTracker(display func(issue string) string, changed func()) *liveTracker {
	return &liveTracker{display: display, changed: changed, now: time.Now, sessions: map[string]*rollup.Session{}}
}

// snapshot returns the running sessions, oldest first, and whether the
// tracker can account for every session of the engine. It is false until
// the engine has logged its first poll tick, and while a tick counts more
// sessions than the tracker has seen.
func (t *liveTracker) snapshot() ([]rollup.Session, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]rollup.Session, 0, len(t.sessions))
	for _, s := range t.sessions {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].IssueID < out[j].IssueID
	})
	return out, t.ticked && t.unaccounted == 0
}

// writer returns a writer that feeds the lines written to it to the
// tracker. Each log stream needs its own.
func (t *liveTracker) writer() io.Writer { return &lineWriter{t: t} }

type lineWriter struct {
	t   *liveTracker
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.t.line(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLogLine {
		w.buf = nil
	}
	return len(p), nil
}

// engineLogLines are the engine log messages the live view depends
// on. The contract test searches each engine binary for them; a rename
// breaks the live view and history, so it must update this list, the
// log fixtures and the replay test together.
var engineLogLines = []string{
	"issue dispatched",
	"retried issue dispatched",
	"turn completed",
	"worker exiting",
	"worker run failed, scheduling retry",
	"tick completed",
}

func (t *liveTracker) line(line string) {
	msg, ok := logField(line, "msg")
	if !ok || !slices.Contains(engineLogLines, msg) {
		return
	}
	var changed bool
	switch msg {
	case "issue dispatched":
		changed = t.start(line, 1)
	case "retried issue dispatched":
		attempt, _ := strconv.Atoi(fieldOr(line, "attempt"))
		changed = t.start(line, attempt+1)
	case "turn completed":
		changed = t.turn(line)
	case "worker exiting", "worker run failed, scheduling retry":
		changed = t.end(fieldOr(line, "issue_id"))
	case "tick completed":
		n, err := strconv.Atoi(fieldOr(line, "running"))
		if err != nil {
			return
		}
		changed = t.tick(n)
	}
	if changed && t.changed != nil {
		t.changed()
	}
}

func (t *liveTracker) start(line string, attempt int) bool {
	id := fieldOr(line, "issue_id")
	if id == "" {
		return false
	}
	ident := fieldOr(line, "issue_identifier")
	if t.display != nil {
		ident = t.display(ident)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, seen := t.sessions[id]; !seen && t.unaccounted > 0 {
		t.unaccounted--
	}
	t.sessions[id] = &rollup.Session{
		IssueID: id, Identifier: ident, Attempt: attempt, StartedAt: t.now(),
		AgentKind: fieldOr(line, "agent_kind"), RuleName: fieldOr(line, "rule_name"),
		Model: fieldOr(line, "model"), Effort: fieldOr(line, "effort"),
	}
	return true
}

func (t *liveTracker) turn(line string) bool {
	n, err := strconv.Atoi(fieldOr(line, "turn_number"))
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[fieldOr(line, "issue_id")]
	if err != nil || s == nil {
		return false
	}
	s.Turns = n
	return true
}

func (t *liveTracker) end(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.sessions[id]; !ok {
		return false
	}
	delete(t.sessions, id)
	return true
}

func (t *liveTracker) tick(running int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	before := len(t.sessions)
	wasKnown := t.ticked && t.unaccounted == 0
	t.ticked = true
	if running == 0 {
		for id, s := range t.sessions {
			if t.now().Sub(s.StartedAt) > tickDropAfter {
				delete(t.sessions, id)
			}
		}
	}
	t.unaccounted = max(0, running-len(t.sessions))
	return len(t.sessions) != before || wasKnown != (t.unaccounted == 0)
}

// logField returns the value of key in a logfmt line, unquoted.
func logField(line, key string) (string, bool) {
	for rest := line; ; {
		i := strings.Index(rest, key+"=")
		if i < 0 {
			return "", false
		}
		if i > 0 && rest[i-1] != ' ' {
			rest = rest[i+len(key)+1:]
			continue
		}
		v := rest[i+len(key)+1:]
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				return "", false
			}
			return v[1 : 1+end], true
		}
		v, _, _ = strings.Cut(v, " ")
		return v, true
	}
}

func fieldOr(line, key string) string {
	v, _ := logField(line, key)
	return v
}
