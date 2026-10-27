// Package runlive reads what a run leaves on the machine while it is
// running: the agent's own transcript, the files the agent writes in its
// workspace, and the engine's database and log. Every read is bounded and
// stays inside the directories it is given.
package runlive

import "unicode/utf8"

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

// MaxText is the most bytes of text an Event keeps.
const MaxText = 4000

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
}

// clip shortens s to MaxText bytes at a rune boundary.
func clip(s string) (string, bool) {
	if len(s) <= MaxText {
		return s, false
	}
	n := MaxText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

func newEvent(at, kind, title, text, tool string, failed bool) Event {
	text, cut := clip(text)
	return Event{Time: at, Kind: kind, Title: title, Text: text, Tool: tool, Failed: failed, Truncated: cut}
}
