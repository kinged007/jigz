package runlive

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Agent kinds whose transcripts are read.
const (
	Pi     = "pi"
	Claude = "claude-code"
)

const (
	// MaxTailBytes is how much of the end of a transcript is read.
	MaxTailBytes = 1 << 20
	// MaxEvents is the most events a tail returns.
	MaxEvents = 300
)

// AgentKind maps an adapter name to a kind with a readable transcript,
// and returns "" for any other adapter.
func AgentKind(adapter string) string {
	switch adapter {
	case "claude-code", "claude":
		return Claude
	case "pi":
		return Pi
	}
	return ""
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// ValidID reports whether a session id is safe to use in a file name and
// in a shell command.
func ValidID(id string) bool { return idPattern.MatchString(id) && !strings.HasPrefix(id, ".") }

// SessionDir is the directory the agent keeps the transcripts of
// workspace in, under home.
func SessionDir(home, kind, workspace string) string {
	switch kind {
	case Pi:
		name := strings.TrimLeft(workspace, `/\`)
		name = strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(name)
		return filepath.Join(home, ".pi", "agent", "sessions", "--"+name+"--")
	case Claude:
		name := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				return r
			}
			return '-'
		}, workspace)
		return filepath.Join(home, ".claude", "projects", name)
	}
	return ""
}

// FindTranscript returns the transcript file of a session and its id. With
// an id it looks for that session. Without one it takes the file modified
// last at or after since. Only regular files that resolve inside the
// session directory are returned. A missing transcript gives "".
func FindTranscript(home, kind, workspace, id string, since time.Time) (path, sessionID string) {
	dir := SessionDir(home, kind, workspace)
	if dir == "" || (id != "" && !ValidID(id)) {
		return "", ""
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", ""
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return "", ""
	}
	type cand struct {
		path, id string
		mod      time.Time
	}
	var found []cand
	for _, e := range entries {
		name := e.Name()
		stem, ok := strings.CutSuffix(name, ".jsonl")
		if !ok {
			continue
		}
		fid := stem
		if kind == Pi {
			_, fid, _ = strings.Cut(stem, "_")
		}
		if !ValidID(fid) || (id != "" && fid != id) {
			continue
		}
		p := filepath.Join(real, name)
		if !inside(real, p) {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || (id == "" && fi.ModTime().Before(since)) {
			continue
		}
		found = append(found, cand{p, fid, fi.ModTime()})
	}
	if len(found) == 0 {
		return "", ""
	}
	sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	return found[0].path, found[0].id
}

// inside reports whether p, after resolving symlinks, lies in the
// resolved directory dir.
func inside(dir, p string) bool {
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, rp)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

// TailEvents returns the last events of a transcript, at most limit. Only
// the last MaxTailBytes of the file are read, and a line that is cut by
// that window or still being written is skipped.
func TailEvents(path, kind string, limit int) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("transcript is not a regular file")
	}
	off := max(fi.Size()-MaxTailBytes, 0)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, MaxTailBytes))
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = nil
		}
	}
	parse := parser(kind)
	if parse == nil {
		return nil, fmt.Errorf("no transcript format for %q", kind)
	}
	var out []Event
	names := map[string]string{}
	pos := off
	for _, line := range bytes.Split(buf, []byte("\n")) {
		at := pos
		pos += int64(len(line)) + 1
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		for i, e := range parse(line, names, MaxText) {
			e.Ref = TranscriptRef(at, i)
			out = append(out, e)
		}
	}
	return out[max(len(out)-max(limit, 1), 0):], nil
}

func parser(kind string) func(line []byte, names map[string]string, max int) []Event {
	switch kind {
	case Pi:
		return parsePi
	case Claude:
		return parseClaude
	}
	return nil
}

// maxLineBytes is the longest transcript line EventText reads.
const maxLineBytes = 8 << 20

// ErrNoEvent says that a reference does not name an event of the
// transcript.
var ErrNoEvent = errors.New("no such event")

// EventText returns the text of the sub-th event of the transcript line
// that starts at off, cut at MaxFullText bytes, and the number of
// characters cut. off must be the start of a line.
func EventText(path, kind string, off int64, sub int) (string, int, error) {
	parse := parser(kind)
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() || parse == nil || off < 0 || sub < 0 || off >= fi.Size() {
		return "", 0, ErrNoEvent
	}
	if off > 0 {
		var prev [1]byte
		if _, err := f.ReadAt(prev[:], off-1); err != nil || prev[0] != '\n' {
			return "", 0, ErrNoEvent
		}
	}
	line, err := bufio.NewReaderSize(io.NewSectionReader(f, off, maxLineBytes+1), 64<<10).ReadBytes('\n')
	if err != nil && err != io.EOF || len(line) > maxLineBytes {
		return "", 0, ErrNoEvent
	}
	evs := parse(bytes.TrimRight(line, "\r\n"), map[string]string{}, MaxFullText)
	if sub >= len(evs) {
		return "", 0, ErrNoEvent
	}
	return evs[sub].Text, evs[sub].More, nil
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	ToolUseID string          `json:"tool_use_id"`
}

// blocks reads a content field that is a string or a list of blocks.
func blocks(raw json.RawMessage) []block {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		return []block{{Type: "text", Text: s}}
	}
	var out []block
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func plainText(raw json.RawMessage) string {
	var parts []string
	for _, b := range blocks(raw) {
		if b.Type == "text" || b.Type == "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// argSummary is the text shown for the arguments of a tool call: the
// value of the one field that says what the call does, else the JSON.
func argSummary(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return string(raw)
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "query", "url", "description"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

type piLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	ModelID   string `json:"modelId"`
	Message   struct {
		Role         string          `json:"role"`
		Content      json.RawMessage `json:"content"`
		ToolName     string          `json:"toolName"`
		IsError      bool            `json:"isError"`
		StopReason   string          `json:"stopReason"`
		ErrorMessage string          `json:"errorMessage"`
	} `json:"message"`
}

func parsePi(line []byte, _ map[string]string, max int) []Event {
	newEvent := func(at, kind, title, text, tool string, failed bool) Event {
		return newEventMax(max, at, kind, title, text, tool, failed)
	}
	var l piLine
	if json.Unmarshal(line, &l) != nil {
		return nil
	}
	at := l.Timestamp
	switch l.Type {
	case "session":
		return []Event{newEvent(at, KindSession, "Session started", "", "", false)}
	case "model_change":
		return []Event{newEvent(at, KindSession, "Model "+l.ModelID, "", "", false)}
	case "message":
	default:
		return nil
	}
	m := l.Message
	switch m.Role {
	case "user":
		if t := plainText(m.Content); t != "" {
			return []Event{newEvent(at, KindMessage, "Prompt", t, "", false)}
		}
	case "toolResult":
		return []Event{newEvent(at, KindToolResult, "Result: "+m.ToolName, plainText(m.Content), m.ToolName, m.IsError)}
	case "assistant":
		var out []Event
		for _, b := range blocks(m.Content) {
			switch b.Type {
			case "text":
				out = append(out, newEvent(at, KindMessage, "Assistant", b.Text, "", false))
			case "thinking":
				out = append(out, newEvent(at, KindThinking, "Thinking", b.Thinking, "", false))
			case "toolCall":
				out = append(out, newEvent(at, KindToolCall, b.Name, argSummary(b.Arguments), b.Name, false))
			}
		}
		if m.StopReason == "error" {
			out = append(out, newEvent(at, KindError, "Error", m.ErrorMessage, "", true))
		}
		return out
	}
	return nil
}

type claudeLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func parseClaude(line []byte, names map[string]string, max int) []Event {
	newEvent := func(at, kind, title, text, tool string, failed bool) Event {
		return newEventMax(max, at, kind, title, text, tool, failed)
	}
	var l claudeLine
	if json.Unmarshal(line, &l) != nil || (l.Type != "user" && l.Type != "assistant") {
		return nil
	}
	at := l.Timestamp
	var out []Event
	for _, b := range blocks(l.Message.Content) {
		switch {
		case b.Type == "tool_use":
			names[b.ID] = b.Name
			out = append(out, newEvent(at, KindToolCall, b.Name, argSummary(b.Input), b.Name, false))
		case b.Type == "tool_result":
			tool := names[b.ToolUseID]
			title := "Result"
			if tool != "" {
				title += ": " + tool
			}
			out = append(out, newEvent(at, KindToolResult, title, plainText(b.Content), tool, b.IsError))
		case b.Type == "thinking":
			if b.Thinking != "" {
				out = append(out, newEvent(at, KindThinking, "Thinking", b.Thinking, "", false))
			}
		case b.Type == "text" && b.Text != "":
			title := "Assistant"
			if l.Type == "user" {
				title = "Prompt"
			}
			out = append(out, newEvent(at, KindMessage, title, b.Text, "", false))
		}
	}
	return out
}
