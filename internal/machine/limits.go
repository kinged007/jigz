package machine

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BuiltinLimit matches the usage-limit wordings of the agent CLIs,
// including Claude's "You've hit your 5-hour limit" and "weekly limit".
// The error classification of run history uses it too.
var BuiltinLimit = regexp.MustCompile(`(?i)usage limit|limit reached|limit exhausted|hit your (?:[\w-]+ ){0,3}limit|\b\d+-(?:hour|day|week) limit|weekly limit|insufficient_quota|quota exceeded|out of credits`)

// plainLimitRe matches a line that is a usage-limit message from its
// first word, the form the claude-code CLI prints as plain text. Tool
// output and prose that merely mention a limit do not start this way.
var plainLimitRe = regexp.MustCompile(`(?i)^\W*(?:you(?:['’]ve| have) hit your\b|claude ai usage limit reached|(?:\d+-(?:hour|day|week)|weekly|monthly) limit reached)`)

// BuiltinModelError matches a CLI line reporting an unknown or
// unavailable model. The per-CLI message catalog grows as recorded
// engine logs show new wordings; the gate treats a hit like a usage
// limit so the standby takes over. The does-not-exist wording takes a
// single token so "model file foo does not exist" prose does not match.
var BuiltinModelError = regexp.MustCompile(`(?i)model not found|unknown model|invalid model|model ['"]?\S+['"]? does not exist|no such model`)

// IsModelError reports whether an agent CLI line says the model is
// unavailable, returning the message. The text is extracted the same
// way usage-limit text is; the per-CLI wording catalog grows as
// recorded engine logs show new variants.
func IsModelError(runtime, line string, stderr bool) (string, bool) {
	text := cliErrorText(runtime, line, stderr)
	if text == "" || !BuiltinModelError.MatchString(text) {
		return "", false
	}
	return strings.TrimSpace(text), true
}

// cliErrorText returns the error text of one CLI output line, or "" when
// the line is not error-shaped.
func cliErrorText(runtime, line string, stderr bool) string {
	if stderr {
		return stderrError(runtime, line)
	}
	return stdoutError(runtime, line)
}

// stderrErrorRe matches CLI-error framing on a stderr line. Stdout is
// already scoped to error events; stderr needs the same gate so routine
// tool warnings mentioning a limit phrase do not exhaust the agent.
var stderrErrorRe = regexp.MustCompile(`(?i)\berror\b|\bfailed\b|\bfailure\b|\bfatal\b|\b[45]\d\d\b`)

// stderrError returns the error text of a stderr line, or "" when the
// line is not error-shaped. JSON error events count on either stream;
// otherwise the line must carry CLI-error framing.
func stderrError(runtime, line string) string {
	if s := stdoutError(runtime, line); s != "" {
		return s
	}
	if plainLimitRe.MatchString(line) {
		return strings.TrimSpace(line)
	}
	if !stderrErrorRe.MatchString(line) {
		return ""
	}
	return strings.TrimSpace(line)
}

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
	if stderr {
		text = stderrError(runtime, line)
	} else {
		text = stdoutError(runtime, line)
		if text == "" && plainLimitRe.MatchString(line) {
			text = strings.TrimSpace(line)
		}
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

const clockPat = `(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\s*(?:\(([^)]+)\))?`

var (
	resetRelative = regexp.MustCompile(`(?i)\b(?:resets?|try again|retry|available again)\s+in\s+((?:\d+\s*[a-z]+[\s,]*(?:and\s+)?)+)`)
	relativeUnit  = regexp.MustCompile(`(?i)(\d+)\s*(d|h|m|s)[a-z]*`)
	resetDate     = regexp.MustCompile(`(?i)resets?\s+(?:on\s+)?(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s*(?:at\s+)?` + clockPat)
	resetWeekday  = regexp.MustCompile(`(?i)resets?\s+(?:on\s+)?(mon|tue|wed|thu|fri|sat|sun)[a-z]*,?\s*(?:at\s+)?` + clockPat)
)

var (
	monthNames   = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	weekdayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
)

// resetTime returns when a limit message says the limit resets: an epoch
// after "|", a duration ("resets in 3h"), a date and clock ("resets Nov 14
// at 5pm (UTC)"), a weekday and clock ("resets Friday 5pm") or a clock
// alone. A time that has passed rolls forward: a month-day to next year, a
// weekday to next week, a clock to tomorrow. A message with none of these
// resets after the default reset.
func (c *Config) resetTime(text string, now time.Time) time.Time {
	if m := resetEpoch.FindStringSubmatch(text); m != nil {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			if t := time.Unix(sec, 0); t.After(now) {
				return t
			}
		}
	}
	if m := resetRelative.FindStringSubmatch(text); m != nil {
		var d time.Duration
		for _, u := range relativeUnit.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(u[1])
			d += time.Duration(n) * map[string]time.Duration{"d": 24 * time.Hour, "h": time.Hour, "m": time.Minute, "s": time.Second}[strings.ToLower(u[2])]
		}
		if d > 0 {
			return now.Add(d)
		}
	}
	if m := resetDate.FindStringSubmatch(text); m != nil && (m[4] != "" || m[5] != "") {
		if h, min, ok := clockOf(m[3], m[4], m[5]); ok {
			loc := c.location(m[6])
			month := time.Month(indexOf(monthNames, strings.ToLower(m[1])) + 1)
			day, _ := strconv.Atoi(m[2])
			l := now.In(loc)
			t := time.Date(l.Year(), month, day, h, min, 0, 0, loc)
			if !t.After(now) {
				t = time.Date(l.Year()+1, month, day, h, min, 0, 0, loc)
			}
			if t.Month() == month && t.Day() == day {
				return t
			}
		}
	}
	if m := resetWeekday.FindStringSubmatch(text); m != nil && (m[3] != "" || m[4] != "") {
		if h, min, ok := clockOf(m[2], m[3], m[4]); ok {
			loc := c.location(m[5])
			l := now.In(loc)
			ahead := (indexOf(weekdayNames, strings.ToLower(m[1])) - int(l.Weekday()) + 7) % 7
			t := time.Date(l.Year(), l.Month(), l.Day()+ahead, h, min, 0, 0, loc)
			if !t.After(now) {
				t = time.Date(l.Year(), l.Month(), l.Day()+ahead+7, h, min, 0, 0, loc)
			}
			return t
		}
	}
	if m := resetClock.FindStringSubmatch(text); m != nil && (m[2] != "" || m[3] != "") {
		if h, min, ok := clockOf(m[1], m[2], m[3]); ok {
			loc := c.location(m[4])
			l := now.In(loc)
			t := time.Date(l.Year(), l.Month(), l.Day(), h, min, 0, 0, loc)
			if !t.After(now) {
				t = time.Date(l.Year(), l.Month(), l.Day()+1, h, min, 0, 0, loc)
			}
			return t
		}
	}
	reset := c.Limits.reset
	if reset <= 0 {
		reset = defaultReset
	}
	return now.Add(reset)
}

// location is the named time zone of a message, else the configured hours
// zone, else the local zone.
func (c *Config) location(name string) *time.Location {
	if name = strings.TrimSpace(name); name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			return l
		}
	}
	if c.Hours.loc != nil {
		return c.Hours.loc
	}
	return time.Local
}

// clockOf converts the hour, minute and am/pm fields of a message.
func clockOf(hour, minute, ampm string) (h, min int, ok bool) {
	h, _ = strconv.Atoi(hour)
	min, _ = strconv.Atoi(minute)
	if ap := strings.ToLower(ampm); ap != "" {
		if h < 1 || h > 12 {
			return 0, 0, false
		}
		h %= 12
		if ap == "pm" {
			h += 12
		}
	}
	return h, min, h < 24 && min < 60
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
