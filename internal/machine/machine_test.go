package machine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func isolate(t *testing.T) {
	t.Helper()
	d := t.TempDir()
	t.Setenv("HOME", d)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(d, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(d, "state"))
}

func load(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "machine.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func mustLoad(t *testing.T, body string) *Config {
	t.Helper()
	c, err := load(t, body)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func at(t *testing.T, loc, s string) time.Time {
	t.Helper()
	l, err := time.LoadLocation(loc)
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02 15:04", s, l)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLoadMissingFile(t *testing.T) {
	isolate(t)
	c, err := Load(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pool) != 0 || !c.Hours.Open(time.Now()) || c.Hours.End != EndFinish {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestDefaultPath(t *testing.T) {
	isolate(t)
	p, err := DefaultPath()
	if err != nil || p != filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "jigz", "machine.yaml") {
		t.Fatalf("got %q, %v", p, err)
	}
}

func TestLoadFull(t *testing.T) {
	c := mustLoad(t, `
hours:
  timezone: Europe/Madrid
  end: stop
  windows:
    - days: [mon, Tue]
      start: "09:00"
      end: "18:00"
pool:
  - name: a
    runtime: claude-code
    env: {CLAUDE_CONFIG_DIR: ~/.claude-work}
    cost_class: subscription
  - name: b
    runtime: pi
    command: /opt/pi
limits:
  default_reset: 30m
  patterns: ["too many tokens"]
`)
	if c.Hours.End != EndStop || c.Pool[0].Command != "claude" || c.Pool[1].Command != "/opt/pi" || c.Limits.reset != 30*time.Minute {
		t.Fatalf("unexpected: %+v", c)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings: %v", c.Warnings)
	}
}

func TestLoadWarnsOnOpenPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.yaml")
	os.WriteFile(p, []byte("pool: []\n"), 0o600)
	os.Chmod(p, 0o644)
	c, err := Load(p)
	if err != nil || len(c.Warnings) != 1 {
		t.Fatalf("err=%v warnings=%v", err, c.Warnings)
	}
}

func TestLoadValidation(t *testing.T) {
	win := func(extra string) string {
		return "hours:\n  windows:\n    - " + extra + "\n"
	}
	cases := []struct{ name, body, want string }{
		{"unknown key", "bogus: 1\n", "bogus"},
		{"bad timezone", "hours:\n  timezone: Mars/Base\n", "hours.timezone"},
		{"bad end", "hours:\n  end: pause\n", "hours.end"},
		{"no days", win("{start: \"09:00\", end: \"10:00\"}"), "hours.windows[0].days"},
		{"bad day", win("{days: [xyz], start: \"09:00\", end: \"10:00\"}"), "hours.windows[0].days"},
		{"bad start", win("{days: [mon], start: \"9am\", end: \"10:00\"}"), "hours.windows[0].start"},
		{"bad end clock", win("{days: [mon], start: \"09:00\", end: \"25:00\"}"), "hours.windows[0].end"},
		{"equal", win("{days: [mon], start: \"09:00\", end: \"09:00\"}"), "start and end must differ"},
		{"no name", "pool:\n  - runtime: pi\n", "pool[0].name"},
		{"dup name", "pool:\n  - {name: a, runtime: pi}\n  - {name: a, runtime: pi}\n", "pool[1].name"},
		{"runtime", "pool:\n  - {name: a, runtime: pi}\n  - {name: b, runtime: codex}\n", `pool[1].runtime: unknown runtime "codex"`},
		{"entry end", "pool:\n  - {name: a, runtime: pi, hours: {end: stop}}\n", "pool[0].hours.end"},
		{"entry window", "pool:\n  - name: a\n    runtime: pi\n    hours:\n      windows:\n        - {days: [mon], start: \"x\", end: \"10:00\"}\n", "pool[0].hours.windows[0].start"},
		{"reset", "limits:\n  default_reset: soon\n", "limits.default_reset"},
		{"reset neg", "limits:\n  default_reset: -1h\n", "limits.default_reset"},
		{"pattern", "limits:\n  patterns: [\"(\"]\n", "limits.patterns[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestHoursOpen(t *testing.T) {
	c := mustLoad(t, `
hours:
  timezone: Europe/Madrid
  windows:
    - {days: [mon, tue, wed, thu, fri], start: "09:00", end: "18:00"}
    - {days: [fri], start: "22:00", end: "02:00"}
`)
	// 2026-10-19 is a Monday.
	cases := []struct {
		when string
		want bool
	}{
		{"2026-10-19 08:59", false},
		{"2026-10-19 09:00", true},
		{"2026-10-19 17:59", true},
		{"2026-10-19 18:00", false},
		{"2026-10-24 12:00", false}, // Saturday
		{"2026-10-23 23:00", true},  // Friday night window
		{"2026-10-24 01:59", true},  // wraps into Saturday
		{"2026-10-24 02:00", false},
		{"2026-10-25 01:00", false}, // Sunday: Saturday has no window
		{"2026-10-19 01:00", false}, // Monday: Sunday has no window
	}
	for _, tc := range cases {
		if got := c.Hours.Open(at(t, "Europe/Madrid", tc.when)); got != tc.want {
			t.Errorf("Open(%s) = %v, want %v", tc.when, got, tc.want)
		}
	}
	// Same instant, other zone: 08:30 UTC is 09:30 in Madrid.
	if !c.Hours.Open(at(t, "UTC", "2026-10-19 08:30")) {
		t.Error("timezone conversion: expected open")
	}
}

func TestHoursOpenNoWindows(t *testing.T) {
	c := mustLoad(t, "hours: {timezone: UTC}\n")
	if !c.Hours.Open(time.Now()) || !c.Hours.Next(time.Now()).IsZero() {
		t.Fatal("no windows must be always open with no boundary")
	}
}

func TestHoursNext(t *testing.T) {
	c := mustLoad(t, `
hours:
  timezone: Europe/Madrid
  windows:
    - {days: [mon, fri], start: "09:00", end: "18:00"}
    - {days: [sat], start: "22:00", end: "02:00"}
`)
	cases := []struct{ from, want string }{
		{"2026-10-19 08:00", "2026-10-19 09:00"},
		{"2026-10-19 09:00", "2026-10-19 18:00"},
		{"2026-10-19 18:00", "2026-10-23 09:00"}, // Monday evening -> Friday
		{"2026-10-24 12:00", "2026-10-24 22:00"},
		{"2026-10-24 23:00", "2026-10-25 02:00"},
		{"2026-10-25 02:00", "2026-10-26 09:00"},
	}
	for _, tc := range cases {
		got := c.Hours.Next(at(t, "Europe/Madrid", tc.from))
		if want := at(t, "Europe/Madrid", tc.want); !got.Equal(want) {
			t.Errorf("Next(%s) = %v, want %v", tc.from, got, want)
		}
	}
}

func TestHoursDST(t *testing.T) {
	// Europe/Madrid leaves DST on 2026-10-25; a 09:00 start stays at 09:00 local.
	c := mustLoad(t, "hours:\n  timezone: Europe/Madrid\n  windows:\n    - {days: [sat, sun], start: \"09:00\", end: \"10:00\"}\n")
	got := c.Hours.Next(at(t, "Europe/Madrid", "2026-10-24 10:00"))
	if want := at(t, "Europe/Madrid", "2026-10-25 09:00"); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !c.Hours.Open(got) {
		t.Fatal("boundary start must be open")
	}
}

const poolYAML = `
pool:
  - {name: c1, runtime: claude-code}
  - name: p1
    runtime: pi
  - name: c2
    runtime: claude-code
    hours:
      timezone: UTC
      windows:
        - {days: [mon], start: "10:00", end: "12:00"}
`

func TestPickAndActiveRuntime(t *testing.T) {
	c := mustLoad(t, poolYAML)
	now := at(t, "UTC", "2026-10-19 09:00") // Monday, c2 closed
	st := State{}
	if e, ok := c.Pick(st, "claude-code", now); !ok || e.Name != "c1" {
		t.Fatalf("pick = %v %v", e.Name, ok)
	}
	if got := c.ActiveRuntime(st, now); got != "claude-code" {
		t.Fatalf("active = %q", got)
	}
	st["c1"] = EntryState{ExhaustedUntil: now.Add(2 * time.Hour)}
	if _, ok := c.Pick(st, "claude-code", now); ok {
		t.Fatal("c1 exhausted and c2 closed: nothing available")
	}
	if got := c.ActiveRuntime(st, now); got != "pi" {
		t.Fatalf("active = %q, want pi", got)
	}
	if e, ok := c.Pick(st, "claude-code", at(t, "UTC", "2026-10-19 10:30")); !ok || e.Name != "c2" {
		t.Fatalf("c2 should be picked in its window, got %v %v", e.Name, ok)
	}
	if e, ok := c.Pick(st, "claude-code", now.Add(2*time.Hour)); !ok || e.Name != "c1" {
		t.Fatalf("exhaustion expiring exactly now must be available, got %v %v", e.Name, ok)
	}
	st["p1"] = EntryState{ExhaustedUntil: now.Add(time.Hour)}
	st["gone"] = EntryState{}
	if got := c.ActiveRuntime(st, now); got != "" {
		t.Fatalf("active = %q, want empty", got)
	}
	if _, ok := c.Pick(st, "codex", now); ok {
		t.Fatal("unknown runtime")
	}
}

func TestNextChange(t *testing.T) {
	c := mustLoad(t, poolYAML+"hours:\n  timezone: UTC\n  windows:\n    - {days: [mon], start: \"08:00\", end: \"20:00\"}\n")
	now := at(t, "UTC", "2026-10-19 09:00")
	if got, want := c.NextChange(State{}, now), at(t, "UTC", "2026-10-19 10:00"); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	st := State{"c1": EntryState{ExhaustedUntil: at(t, "UTC", "2026-10-19 09:30")}}
	if got, want := c.NextChange(st, now), at(t, "UTC", "2026-10-19 09:30"); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	empty := mustLoad(t, "")
	if !empty.NextChange(State{}, now).IsZero() {
		t.Fatal("empty config has no change")
	}
}

func TestDetectLimit(t *testing.T) {
	c := mustLoad(t, "hours: {timezone: UTC}\nlimits:\n  patterns: [\"slow down forever\"]\n")
	now := at(t, "UTC", "2026-10-19 12:00")
	cases := []struct {
		name, runtime, line string
		stderr              bool
		limited             bool
		until               time.Time
	}{
		{"claude epoch", "claude-code", `{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached|{epoch}"}`, false, true, now.Add(2 * time.Hour)},
		{"claude clock pm", "claude-code", `{"type":"result","is_error":true,"result":"5-hour limit reached ∙ resets 3pm"}`, false, true, at(t, "UTC", "2026-10-19 15:00")},
		{"claude clock next day", "claude-code", `{"type":"result","is_error":true,"result":"5-hour limit reached ∙ resets 11:30am"}`, false, true, at(t, "UTC", "2026-10-20 11:30")},
		{"claude zone", "claude-code", `{"type":"result","is_error":true,"result":"You've hit your usage limit · resets 3:30pm (Europe/Madrid)"}`, false, true, at(t, "Europe/Madrid", "2026-10-19 15:30")},
		{"claude 24h", "claude-code", `{"type":"result","is_error":true,"result":"usage limit reached, resets 15:00"}`, false, true, at(t, "UTC", "2026-10-19 15:00")},
		{"claude 12am", "claude-code", `{"type":"result","is_error":true,"result":"usage limit, resets 12am"}`, false, true, at(t, "UTC", "2026-10-20 00:00")},
		{"claude default reset", "claude-code", `{"type":"result","is_error":true,"result":"usage limit reached"}`, false, true, now.Add(time.Hour)},
		{"claude bad hour", "claude-code", `{"type":"result","is_error":true,"result":"usage limit, resets 13pm"}`, false, true, now.Add(time.Hour)},
		{"claude custom pattern", "claude-code", `{"type":"result","is_error":true,"result":"Slow Down Forever"}`, false, true, now.Add(time.Hour)},
		{"pi nested", "pi", `{"type":"turn_end","message":{"role":"assistant","stopReason":"error","errorMessage":"insufficient_quota"}}`, false, true, now.Add(time.Hour)},
		{"pi top level", "pi", `{"stopReason":"error","errorMessage":"429 quota exceeded"}`, false, true, now.Add(time.Hour)},
		{"stderr", "pi", `Error: usage limit reached|{epoch}`, true, true, now.Add(2 * time.Hour)},
		{"claude overloaded", "claude-code", `{"type":"result","is_error":true,"result":"Overloaded"}`, false, false, time.Time{}},
		{"claude 529", "claude-code", `{"type":"result","is_error":true,"result":"API Error: 529 overloaded_error"}`, false, false, time.Time{}},
		{"claude 429", "claude-code", `{"type":"system","subtype":"api_retry","error":"rate_limit","status":429}`, false, false, time.Time{}},
		{"claude success", "claude-code", `{"type":"result","is_error":false,"result":"done; fixed the usage limit docs"}`, false, false, time.Time{}},
		{"claude assistant text", "claude-code", `{"type":"assistant","message":"usage limit reached"}`, false, false, time.Time{}},
		{"pi not error", "pi", `{"stopReason":"stop","errorMessage":"usage limit reached"}`, false, false, time.Time{}},
		{"pi rate limit", "pi", `{"stopReason":"error","errorMessage":"rate_limit 429"}`, false, false, time.Time{}},
		{"non json stdout", "claude-code", `usage limit reached`, false, false, time.Time{}},
		{"json array", "pi", `[1,2]`, false, false, time.Time{}},
		{"stderr 429", "claude-code", `HTTP 429 rate_limit`, true, false, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := strings.ReplaceAll(tc.line, "{epoch}", strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10))
			until, reason, ok := c.DetectLimit(tc.runtime, line, tc.stderr, now)
			if ok != tc.limited {
				t.Fatalf("limited = %v, want %v (reason %q)", ok, tc.limited, reason)
			}
			if ok && (!until.Equal(tc.until) || reason == "") {
				t.Fatalf("until = %v reason %q, want %v", until, reason, tc.until)
			}
		})
	}
}

func TestStateMissingAndUpdate(t *testing.T) {
	isolate(t)
	p, err := StatePath()
	if err != nil || p != filepath.Join(os.Getenv("XDG_STATE_HOME"), "jigz", "pool.json") {
		t.Fatalf("path %q, %v", p, err)
	}
	st, err := LoadState(p)
	if err != nil || len(st) != 0 {
		t.Fatalf("missing file: %v %v", st, err)
	}
	until := time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)
	if err := MarkExhausted(p, "a", until, strings.Repeat("x", 500)); err != nil {
		t.Fatal(err)
	}
	if err := MarkUsed(p, "a", until.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	st, err = LoadState(p)
	if err != nil {
		t.Fatal(err)
	}
	e := st["a"]
	if !e.ExhaustedUntil.Equal(until) || len(e.Reason) != maxReason || !e.LastUsed.Equal(until.Add(-time.Hour)) {
		t.Fatalf("unexpected state: %+v", e)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestStateCorrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pool.json")
	os.WriteFile(p, []byte("{"), 0o600)
	if _, err := LoadState(p); err == nil {
		t.Fatal("expected error")
	}
}

func TestStateConcurrentUpdates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "pool.json")
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- UpdateState(p, func(st State) {
				e := st["a"]
				e.Reason += "x"
				st["a"] = e
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := LoadState(p)
	if err != nil || len(st["a"].Reason) != n {
		t.Fatalf("lost updates: %d of %d (%v)", len(st["a"].Reason), n, err)
	}
}

func TestExpandedEnv(t *testing.T) {
	isolate(t)
	t.Setenv("MY_KEY", "secret")
	e := Entry{Env: map[string]string{
		"B": "~/.claude-work", "A": "$MY_KEY", "C": "${MY_KEY}-x", "D": "a/~/b", "E": "~",
	}}
	home := os.Getenv("HOME")
	want := []string{"A=secret", "B=" + home + "/.claude-work", "C=secret-x", "D=a/~/b", "E=" + home}
	got := e.ExpandedEnv()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLoadEngineBlock(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "machine.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c, err := Load(write("engine:\n  repo: sortie-ai/sortie\n  version: v1.29.0\n"))
	if err != nil || c.Engine != (Engine{Repo: "sortie-ai/sortie", Version: "v1.29.0"}) {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	for body, want := range map[string]string{
		"engine:\n  repo: sortie\n":         "engine.repo",
		"engine:\n  repo: a/b/c\n":          "engine.repo",
		"engine:\n  version: 1.29.0\n":      "engine.version",
		"engine:\n  version: v1.29\n":       "engine.version",
		"engine:\n  version: v1.29.0-rc1\n": "engine.version",
	} {
		if _, err := Load(write(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Load(%q) err = %v, want %s", body, err, want)
		}
		if _, err := LoadEngine(write(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("LoadEngine(%q) err = %v, want %s", body, err, want)
		}
	}
	if e, err := LoadEngine(filepath.Join(dir, "absent.yaml")); err != nil || e != (Engine{}) {
		t.Errorf("LoadEngine(missing) = %+v, %v", e, err)
	}
}
