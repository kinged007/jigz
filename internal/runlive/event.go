// Package runlive reads what a run leaves on the machine while it is
// running: the agent's own transcript, the files the agent writes in its
// workspace, and the engine's database and log. Every read is bounded and
// stays inside the directories it is given.
package runlive

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Event kinds.
const (
	KindMessage      = "message"
	KindThinking     = "thinking"
	KindToolCall     = "tool_call"
	KindToolResult   = "tool_result"
	KindNotification = "notification"
	KindTurn         = "turn"
	KindSession      = "session"
	KindError        = "error"
)

// MaxText is the most bytes of text an Event keeps in the event list.
const MaxText = 4000

// MaxFullText is the most bytes of one event that the full-text route
// returns.
const MaxFullText = 256 << 10

// engineCaps are the character counts after which the engine cuts the
// message of a notification or turn event and appends an ellipsis.
var engineCaps = map[int]bool{200: true, 500: true}

// Event is one step of a run, the same shape for every agent kind and for
// the engine's own event rows.
type Event struct {
	Time      string `json:"time"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Text      string `json:"text,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Failed    bool   `json:"failed,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// More is the number of characters Text lacks when Truncated is set.
	More int `json:"more,omitempty"`
	// EngineCut is set when the engine stored a shortened message.
	EngineCut bool `json:"engine_cut,omitempty"`
	// Ref names the event for the full-text route and keeps its place in
	// the list between polls.
	Ref string `json:"ref,omitempty"`
}

// TranscriptRef names the sub-th event of the transcript line at off.
func TranscriptRef(off int64, sub int) string {
	return "t" + strconv.FormatInt(off, 10) + "." + strconv.Itoa(sub)
}

// RowRef names the engine event row with the given sequence number.
func RowRef(seq int64) string { return "d" + strconv.FormatInt(seq, 10) }

// engineCut reports whether the engine shortened msg.
func engineCut(msg string) bool {
	body, ok := strings.CutSuffix(msg, "…")
	return ok && engineCaps[utf8.RuneCountInString(body)]
}

// clip shortens s to max bytes at a rune boundary and returns the number
// of characters cut.
func clip(s string, max int) (string, int) {
	if len(s) <= max {
		return s, 0
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], utf8.RuneCountInString(s[n:])
}

func newEvent(at, kind, title, text, tool string, failed bool) Event {
	return newEventMax(MaxText, at, kind, title, text, tool, failed)
}

func newEventMax(max int, at, kind, title, text, tool string, failed bool) Event {
	text, more := clip(text, max)
	return Event{Time: at, Kind: kind, Title: title, Text: text, Tool: tool, Failed: failed, Truncated: more > 0, More: more}
}
