package machine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// UnusablePrefix starts the recorded reason of an agent that cannot run
// until something outside the loop is fixed.
const UnusablePrefix = "unusable: "

const modelUnavailablePrefix = "model unavailable:"

// unusableCooldown is a placeholder far enough out that only a clear
// (supervisor start, reload or config change) ends it.
const unusableCooldown = 100 * 365 * 24 * time.Hour

// BuiltinUnusable matches CLI lines that report a failure no retry
// fixes: a missing or refused credential, or no model configured.
// The catalog grows as recorded engine logs show new wordings.
var BuiltinUnusable = regexp.MustCompile(`(?i)invalid api key|invalid x-api-key|incorrect api key|run /login|not logged in|oauth token has expired|authentication_error|could not resolve authentication method|missing bearer or basic authentication|401 unauthorized|no api key (?:found )?for|api key (?:is )?(?:missing|not set|required)|providerautherror|(?:codex|opencode) (?:auth )?login|refused its credential|no models? (?:selected|available|configured)|no default model|model (?:is )?(?:not configured|required)`)

// plainAuthRe matches the plain-text credential messages of the claude-code
// CLI, which carry no error wording: "Invalid API key · Please run /login".
var plainAuthRe = regexp.MustCompile(`(?i)^\W*(?:invalid api key|not logged in)\b`)

// IsUnusable reports whether an agent CLI line shows a non-transient
// failure, returning the message. A usage-limit text is never unusable:
// the limit check has precedence and carries a reset time.
func IsUnusable(runtime, line string, stderr bool) (string, bool) {
	text := cliErrorText(runtime, line, stderr)
	if text == "" && plainAuthRe.MatchString(line) {
		text = line
	}
	text = strings.TrimSpace(text)
	if text == "" || BuiltinLimit.MatchString(text) || !BuiltinUnusable.MatchString(text) {
		return "", false
	}
	return text, true
}

// MarkUnusable records that an agent cannot run until the block is
// cleared.
func MarkUnusable(path, name string, at time.Time, reason string) error {
	return MarkExhausted(path, name, at.Add(unusableCooldown), UnusablePrefix+reason)
}

// Unusable reports whether the entry is blocked by a failure that no
// reset time ends, as opposed to a usage limit.
func (e EntryState) Unusable() bool {
	return strings.HasPrefix(e.Reason, UnusablePrefix) || strings.HasPrefix(e.Reason, modelUnavailablePrefix)
}

// UnusableText is the reason without its prefix.
func (e EntryState) UnusableText() string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(e.Reason, UnusablePrefix), modelUnavailablePrefix))
}

// ClearUnusable removes every unusable block of the state file and
// leaves usage-limit blocks as they are.
func ClearUnusable(path string) error {
	return UpdateState(path, func(st *State) {
		for name, e := range st.Agents {
			if e.Unusable() {
				e.ExhaustedUntil, e.Reason, e.Model = time.Time{}, "", ""
				st.Agents[name] = e
			}
		}
	})
}

// VersionChecked reports whether the engine adapter of the runtime
// refuses releases below a minimum, so that VersionProblem has a rule for
// it.
func VersionChecked(runtime string) bool { return runtime == "opencode" || runtime == "pi" }

// VersionProblem judges the output of `<command> --version` against the
// release the engine adapter of the runtime supports and returns why it
// is refused, or "" when the version is accepted or the runtime has no
// minimum. The rules and messages follow the engine adapters: OpenCode
// needs major 2, Pi needs 0.99.0 or later.
func VersionProblem(runtime, output string) string {
	var name, hint string
	switch runtime {
	case "opencode":
		name, hint = "OpenCode", "install OpenCode 2.x, published on npm as @opencode/cli"
	case "pi":
		name, hint = "Pi", "install Pi "+minPiVersion+" or later, published on npm as @earendil-works/pi-coding-agent"
	default:
		return ""
	}
	version := parseVersion(output)
	if version == "" {
		line := firstLine(output)
		if line == "" {
			line = "no output"
		}
		return fmt.Sprintf("the %s command reported no version that can be read: %s", name, truncate(line, 200))
	}
	ok := false
	if runtime == "opencode" {
		ok = versionParts(version)[0] == 2
	} else {
		ok = versionAtLeast(version, minPiVersion)
	}
	if ok {
		return ""
	}
	return fmt.Sprintf("%s %s is not supported; %s", name, version, hint)
}

const minPiVersion = "0.99.0"

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]*)?$`)

// parseVersion returns the first field of the first non-empty line that
// is a full semantic version, after one leading "v" and one trailing "."
// are dropped.
func parseVersion(out string) string {
	for _, field := range strings.Fields(firstLine(out)) {
		field = strings.TrimSuffix(strings.TrimPrefix(field, "v"), ".")
		if versionRe.MatchString(field) {
			return field
		}
	}
	return ""
}

func firstLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func versionParts(v string) [3]int {
	v = strings.FieldsFunc(v, func(r rune) bool { return r == '-' || r == '+' })[0]
	var p [3]int
	for i, s := range strings.SplitN(v, ".", 3) {
		p[i], _ = strconv.Atoi(s)
	}
	return p
}

func versionAtLeast(v, min string) bool {
	a, b := versionParts(v), versionParts(min)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}
