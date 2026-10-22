package machine

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var BuiltinLimit = regexp.MustCompile(`(?i)usage limit|limit reached|limit exhausted|insufficient_quota|quota exceeded|out of credits`)

var (
	resetEpoch = regexp.MustCompile(`\|(\d{9,11})\b`)
	resetClock = regexp.MustCompile(`(?i)resets?\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\s*(?:\(([^)]+)\))?`)
)

// DetectLimit checks one output line of the real agent CLI for a usage-limit
// message. stderr tells whether the line came from stderr; stdout lines are
// JSONL events of the runtime. It returns the time the limit resets and the
// message.
func (c *Config) DetectLimit(runtime, line string, stderr bool, now time.Time) (until time.Time, reason string, limited bool) {
	text := line
	if !stderr {
		text = stdoutError(runtime, line)
	}
	if text == "" || !c.isLimit(text) {
		return time.Time{}, "", false
	}
	return c.resetTime(text, now), text, true
}

func (c *Config) isLimit(text string) bool {
	if BuiltinLimit.MatchString(text) {
		return true
	}
	for _, re := range c.Limits.patterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// stdoutError returns the error text of a stdout event, or "" when the line
// is not an error event.
func stdoutError(runtime, line string) string {
	var v any
	if json.Unmarshal([]byte(line), &v) != nil {
		return ""
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	switch runtime {
	case "claude-code":
		if obj["type"] == "result" && obj["is_error"] == true {
			res, _ := obj["result"].(string)
			sub, _ := obj["subtype"].(string)
			return strings.TrimSpace(res + " " + sub)
		}
	case "pi":
		return piError(obj)
	}
	return ""
}

// piError finds a map with stopReason "error" anywhere in the event and
// returns its errorMessage.
func piError(v any) string {
	switch x := v.(type) {
	case map[string]any:
		if x["stopReason"] == "error" {
			if msg, ok := x["errorMessage"].(string); ok {
				return msg
			}
		}
		for _, child := range x {
			if s := piError(child); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range x {
			if s := piError(child); s != "" {
				return s
			}
		}
	}
	return ""
}

func (c *Config) resetTime(text string, now time.Time) time.Time {
	if m := resetEpoch.FindStringSubmatch(text); m != nil {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			if t := time.Unix(sec, 0); t.After(now) {
				return t
			}
		}
	}
	if m := resetClock.FindStringSubmatch(text); m != nil && (m[2] != "" || m[3] != "") {
		loc := c.Hours.loc
		if loc == nil {
			loc = time.Local
		}
		if m[4] != "" {
			if l, err := time.LoadLocation(strings.TrimSpace(m[4])); err == nil {
				loc = l
			}
		}
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		if ap := strings.ToLower(m[3]); ap != "" {
			if h < 1 || h > 12 {
				h = -1
			} else {
				h %= 12
				if ap == "pm" {
					h += 12
				}
			}
		}
		if h >= 0 && h < 24 && min < 60 {
			l := now.In(loc)
			t := time.Date(l.Year(), l.Month(), l.Day(), h, min, 0, 0, loc)
			if !t.After(now) {
				t = time.Date(l.Year(), l.Month(), l.Day()+1, h, min, 0, 0, loc)
			}
			return t
		}
	}
	return now.Add(c.Limits.reset)
}
