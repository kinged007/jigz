package machine

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestIsUnusable(t *testing.T) {
	for _, c := range []struct {
		name, runtime, line string
		stderr, want        bool
	}{
		{"claude invalid key plain stdout", "claude-code", "Invalid API key · Please run /login", false, true},
		{"claude invalid key plain stderr", "claude-code", "Invalid API key · Please run /login", true, true},
		{"claude not logged in", "claude-code", "Not logged in · Please run /login", false, true},
		{"claude result event", "claude-code", `{"type":"result","is_error":true,"result":"Invalid API key · Please run /login"}`, false, true},
		{"claude oauth expired", "claude-code", `{"type":"result","is_error":true,"result":"OAuth token has expired. Please obtain a new token or refresh your existing token. authentication_error"}`, false, true},
		{"codex unauthorized", "codex", "ERROR: unexpected status 401 Unauthorized: Missing bearer or basic authentication in header", true, true},
		{"codex not logged in", "codex", "Error: Not logged in. Run codex login", true, true},
		{"opencode auth", "opencode", "Error: ProviderAuthError: authentication failed", true, true},
		{"pi no api key", "pi", `{"type":"message","message":{"stopReason":"error","errorMessage":"No API key found for anthropic."}}`, false, true},
		{"pi no model", "pi", `{"type":"message","message":{"stopReason":"error","errorMessage":"No models available. Set an API key first."}}`, false, true},
		{"no model configured", "opencode", "Error: no model configured", true, true},
		{"limit wrapped in credential_unverified", "claude-code", `{"type":"result","is_error":true,"result":"credential_unverified: You've hit your 5-hour limit · resets 1:10am (UTC) Invalid API key"}`, false, false},
		{"usage limit stderr", "codex", "error: usage limit reached, not logged in to another account", true, false},
		{"prose without error framing on stderr", "codex", "hint: you are not logged in to gh", true, false},
		{"assistant event is not checked", "claude-code", `{"type":"assistant","message":{"content":[{"type":"text","text":"Invalid API key"}]}}`, false, false},
		{"routine stdout text", "codex", "the user is not logged in", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, got := IsUnusable(c.runtime, c.line, c.stderr)
			if got != c.want {
				t.Errorf("IsUnusable = %v, want %v", got, c.want)
			}
		})
	}
}

func TestLimitHasPrecedenceOverCredential(t *testing.T) {
	line := `{"type":"result","is_error":true,"result":"credential_unverified: You've hit your 5-hour limit · resets 1:10am (UTC)"}`
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	until, _, limited := (&Config{}).DetectLimit("claude-code", line, false, now)
	if !limited || until.Hour() != 1 || until.Minute() != 10 {
		t.Fatalf("limit = %v until %v, want a limit that resets at 01:10", limited, until)
	}
	if _, ok := IsUnusable("claude-code", line, false); ok {
		t.Error("a limit message was read as unusable")
	}
}

func TestVersionProblem(t *testing.T) {
	for _, c := range []struct {
		name, runtime, out, want string
	}{
		{"opencode 1.x", "opencode", "1.18.29\n", "OpenCode 1.18.29 is not supported; install OpenCode 2.x, published on npm as @opencode/cli"},
		{"opencode 2.x", "opencode", "2.0.3\n", ""},
		{"opencode prefixed", "opencode", "opencode v2.1.0\n", ""},
		{"opencode 3.x", "opencode", "3.0.0\n", "OpenCode 3.0.0 is not supported; install OpenCode 2.x, published on npm as @opencode/cli"},
		{"opencode no version", "opencode", "usage: opencode\n", "the OpenCode command reported no version that can be read: usage: opencode"},
		{"opencode empty", "opencode", "", "the OpenCode command reported no version that can be read: no output"},
		{"pi old", "pi", "0.98.9\n", "Pi 0.98.9 is not supported; install Pi 0.99.0 or later, published on npm as @earendil-works/pi-coding-agent"},
		{"pi minimum", "pi", "0.99.0\n", ""},
		{"pi newer", "pi", "0.100.1\n", ""},
		{"pi newer major", "pi", "1.0.0\n", ""},
		{"claude has no minimum", "claude-code", "1.0.0\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := VersionProblem(c.runtime, c.out); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestClearUnusableKeepsLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Now()
	if err := MarkUnusable(path, "broken", now, "Invalid API key"); err != nil {
		t.Fatal(err)
	}
	if err := MarkModelExhausted(path, "model", now.Add(time.Hour), "m", "model unavailable: unknown model m"); err != nil {
		t.Fatal(err)
	}
	if err := MarkExhausted(path, "limited", now.Add(time.Hour), "usage limit"); err != nil {
		t.Fatal(err)
	}
	if err := ClearUnusable(path); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Agents["broken"].ExhaustedUntil.IsZero() || !st.Agents["model"].ExhaustedUntil.IsZero() {
		t.Errorf("unusable blocks remain: %+v", st.Agents)
	}
	if st.Agents["limited"].ExhaustedUntil.IsZero() {
		t.Error("the usage-limit block was cleared")
	}
}

func TestResetTime(t *testing.T) {
	// 2026-10-21 is a Wednesday.
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	at := func(y int, mo time.Month, d, h, m int) time.Time { return time.Date(y, mo, d, h, m, 0, 0, time.UTC) }
	cases := []struct {
		text string
		want time.Time
	}{
		{"You've hit your weekly limit · resets Nov 14, 5pm", at(2026, 11, 14, 17, 0)},
		{"You've hit your weekly limit · resets Nov 14 at 5pm (UTC)", at(2026, 11, 14, 17, 0)},
		{"resets November 14th at 5:30pm (America/New_York)", time.Date(2026, 11, 14, 17, 30, 0, 0, ny)},
		{"resets on Dec 3 at 09:15 (UTC)", at(2026, 12, 3, 9, 15)},
		{"resets Oct 1, 9am", at(2027, 10, 1, 9, 0)},
		{"resets Oct 21, 9am", at(2027, 10, 21, 9, 0)},
		{"resets Oct 21, 5pm", at(2026, 10, 21, 17, 0)},
		{"resets Friday 5pm", at(2026, 10, 23, 17, 0)},
		{"resets Fri at 5pm (UTC)", at(2026, 10, 23, 17, 0)},
		{"resets Wednesday 5pm", at(2026, 10, 21, 17, 0)},
		{"resets Wednesday 9am", at(2026, 10, 28, 9, 0)},
		{"resets on Mon at 1:10am (UTC)", at(2026, 10, 26, 1, 10)},
		{"resets in 3h", at(2026, 10, 21, 15, 0)},
		{"try again in 2 hours 15 minutes", at(2026, 10, 21, 14, 15)},
		{"resets in 1 day, 2 hours and 5 minutes", at(2026, 10, 22, 14, 5)},
		{"retry in 90 minutes", at(2026, 10, 21, 13, 30)},
		{"resets 1:10am (UTC)", at(2026, 10, 22, 1, 10)},
		{"resets at 3pm", at(2026, 10, 21, 15, 0)},
		{fmt.Sprintf("Claude AI usage limit reached|%d", now.Add(2*time.Hour).Unix()), now.Add(2 * time.Hour)},
		{"usage limit reached", at(2026, 10, 21, 13, 0)},
		{"resets 5", at(2026, 10, 21, 13, 0)},
		{"resets Feb 30, 5pm", at(2026, 10, 21, 13, 0)},
	}
	c := &Config{}
	c.Hours.loc = time.UTC
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			if got := c.resetTime(tc.text, now); !got.Equal(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
