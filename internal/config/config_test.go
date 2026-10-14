package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeRepo(t *testing.T) {
	cases := map[string]string{
		"https://github.com/octo/hello-world.git": "octo/hello-world",
		"https://github.com/octo/hello-world":     "octo/hello-world",
		"git@github.com:octo/hello-world.git":     "octo/hello-world",
		"octo/hello-world":                        "octo/hello-world",
		"owner/name/":                             "owner/name",
	}
	for in, want := range cases {
		if got := NormalizeRepo(in); got != want {
			t.Errorf("NormalizeRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadEnvAndMilestone(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sortie"), 0o755)
	os.WriteFile(filepath.Join(dir, ".sortie", "config.yaml"),
		[]byte("repo: file/o\ntoken: filetok\n"), 0o644)
	t.Setenv("SORTIE_LOOP_REPO", "o/r")
	t.Setenv("SORTIE_LOOP_TOKEN", "tok")
	t.Setenv("SORTIE_LOOP_MILESTONE", "v2")
	t.Setenv("SORTIE_LOOP_ASSIGNEE", "")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "o/r" || cfg.Token != "tok" || cfg.Tracker != "o/r" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.FilterFor("build") != `label:agent:build,in-progress -label:agent:plan -label:needs-human milestone:"v2"` {
		t.Errorf("build filter: %q", cfg.FilterFor("build"))
	}
	if cfg.FilterFor("review") != `label:agent:review,in-progress -label:agent:build -label:agent:merge -label:needs-human milestone:"v2"` {
		t.Errorf("review filter should exclude needs-human, got %q", cfg.FilterFor("review"))
	}
	if cfg.FilterFor("merge") != `label:agent:merge,in-progress -label:agent:build -label:agent:review -label:needs-human milestone:"v2"` {
		t.Errorf("merge filter should exclude needs-human, got %q", cfg.FilterFor("merge"))
	}
	if cfg.FilterFor("review-fix") != `label:agent:build,in-progress -label:agent:review -label:agent:merge -label:needs-human milestone:"v2"` {
		t.Errorf("review-fix filter should exclude needs-human, got %q", cfg.FilterFor("review-fix"))
	}
	// Unknown loops get the default filter (no label constraint).
	if cfg.FilterFor("triage") != `-label:needs-human milestone:"v2"` {
		t.Errorf("unknown loop filter: %q", cfg.FilterFor("triage"))
	}
}

func TestLoadAssigneeDefaultAndOptOut(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sortie"), 0o755)
	os.WriteFile(filepath.Join(dir, ".sortie", "config.yaml"),
		[]byte("repo: file/o\ntoken: filetok\n"), 0o644)
	t.Setenv("SORTIE_LOOP_REPO", "o/r")
	t.Setenv("SORTIE_LOOP_TOKEN", "tok")
	t.Setenv("SORTIE_LOOP_MILESTONE", "v2")
	os.Unsetenv("SORTIE_LOOP_ASSIGNEE")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FilterFor("plan") != `label:agent:plan,in-progress -label:agent:build -label:needs-human milestone:"v2" assignee:@me` {
		t.Errorf("plan filter: %q", cfg.FilterFor("plan"))
	}
	if cfg.FilterFor("review") != `label:agent:review,in-progress -label:agent:build -label:agent:merge -label:needs-human milestone:"v2" assignee:@me` {
		t.Errorf("review filter: %q", cfg.FilterFor("review"))
	}
	if cfg.FilterFor("merge") != `label:agent:merge,in-progress -label:agent:build -label:agent:review -label:needs-human milestone:"v2" assignee:@me` {
		t.Errorf("merge filter: %q", cfg.FilterFor("merge"))
	}
	if cfg.FilterFor("review-fix") != `label:agent:build,in-progress -label:agent:review -label:agent:merge -label:needs-human milestone:"v2" assignee:@me` {
		t.Errorf("review-fix filter: %q", cfg.FilterFor("review-fix"))
	}
	t.Setenv("SORTIE_LOOP_ASSIGNEE", "")
	cfg, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FilterFor("plan") != `label:agent:plan,in-progress -label:agent:build -label:needs-human milestone:"v2"` {
		t.Errorf("opt-out plan filter: %q", cfg.FilterFor("plan"))
	}
	if cfg.FilterFor("review") != `label:agent:review,in-progress -label:agent:build -label:agent:merge -label:needs-human milestone:"v2"` {
		t.Errorf("opt-out review filter: %q", cfg.FilterFor("review"))
	}
	if cfg.FilterFor("merge") != `label:agent:merge,in-progress -label:agent:build -label:agent:review -label:needs-human milestone:"v2"` {
		t.Errorf("opt-out merge filter: %q", cfg.FilterFor("merge"))
	}
	if cfg.FilterFor("review-fix") != `label:agent:build,in-progress -label:agent:review -label:agent:merge -label:needs-human milestone:"v2"` {
		t.Errorf("opt-out review-fix filter: %q", cfg.FilterFor("review-fix"))
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sortie"), 0o755)
	os.WriteFile(filepath.Join(dir, ".sortie", "config.yaml"),
		[]byte("repo: file/o\ntoken: filetok\n"), 0o644)
	t.Setenv("SORTIE_LOOP_REPO", "")
	t.Setenv("SORTIE_LOOP_TOKEN", "")
	t.Setenv("SORTIE_LOOP_MILESTONE", "")
	t.Setenv("GH_MILESTONE", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "file/o" || cfg.Token != "filetok" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.CloneURL != "https://github.com/file/o.git" {
		t.Errorf("clone url: %q", cfg.CloneURL)
	}
}

func TestLoadFilterOverrides(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sortie"), 0o755)
	os.WriteFile(filepath.Join(dir, ".sortie", "config.yaml"),
		[]byte("repo: file/o\ntoken: filetok\nfilters:\n  triage: label:agent:triage\n  build: label:agent:custom\n"), 0o644)
	t.Setenv("SORTIE_LOOP_REPO", "")
	t.Setenv("SORTIE_LOOP_TOKEN", "")
	t.Setenv("SORTIE_LOOP_MILESTONE", "")
	t.Setenv("GH_MILESTONE", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("SORTIE_LOOP_ASSIGNEE", "")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FilterFor("triage") != "label:agent:triage" {
		t.Errorf("override filter: %q", cfg.FilterFor("triage"))
	}
	if cfg.FilterFor("build") != "label:agent:custom" {
		t.Errorf("known-loop override: %q", cfg.FilterFor("build"))
	}
	if cfg.FilterFor("merge") != "label:agent:merge,in-progress -label:agent:build -label:agent:review -label:needs-human" {
		t.Errorf("untouched default: %q", cfg.FilterFor("merge"))
	}
}

// TestDefaultLabels pins the shipped label set: one yellow trigger per loop,
// one claim, one finished state, one escalation. Colours are part of the
// contract because the board reads them at a glance.
func TestDefaultLabels(t *testing.T) {
	want := []Label{
		{"agent:plan", "fbca04", "Trigger: write an implementation plan for this issue"},
		{"agent:build", "fbca04", "Trigger: implement this issue, or apply review feedback on this PR"},
		{"agent:review", "fbca04", "Trigger: review this PR"},
		{"agent:merge", "fbca04", "Trigger: merge this PR"},
		{"in-progress", "5319e7", "State: claimed by an agent"},
		{"agent:done", "0e8a16", "State: agent finished"},
		{"needs-human", "d73a4a", "Escalation: agent needs a person"},
		{"united-into", "c5def5", "State: work folded into another issue's fix; that issue is the one to read"},
		{"backlog", "ededed", "Deferred by a person; do not dispatch"},
		{"P0", "d73a4a", "Priority: highest, dispatch before everything else"},
		{"P1", "f9826c", "Priority: high"},
		{"P2", "fbca04", "Priority: normal"},
		{"P3", "ededed", "Priority: lowest"},
	}
	if len(DefaultLabels) != len(want) {
		t.Fatalf("DefaultLabels = %d labels, want %d", len(DefaultLabels), len(want))
	}
	for i, w := range want {
		if DefaultLabels[i] != w {
			t.Errorf("label %d = %+v, want %+v", i, DefaultLabels[i], w)
		}
	}
}

// TestDefaultLabelPrefix enforces the naming rule: agent-owned labels carry
// the agent: prefix, the item-level and person-owned labels do not.
func TestDefaultLabelPrefix(t *testing.T) {
	itemLevel := map[string]bool{
		"in-progress": true, "needs-human": true, "united-into": true,
		"backlog": true, "P0": true, "P1": true, "P2": true, "P3": true,
	}
	for _, l := range DefaultLabels {
		unprefixed := itemLevel[l.Name]
		if unprefixed == strings.HasPrefix(l.Name, "agent:") {
			t.Errorf("label %q: wrong prefix (want prefixed=%v)", l.Name, !unprefixed)
		}
	}
}

func TestLoadMissingConfigFails(t *testing.T) {
	dir := t.TempDir() // no .sortie/config.yaml, git remote irrelevant
	t.Setenv("SORTIE_LOOP_REPO", "o/r")
	if _, err := Load(dir); err == nil {
		t.Error("expected error with no config file")
	}
}

func TestLoadNoRepoFails(t *testing.T) {
	dir := t.TempDir() // no git remote, no config
	t.Setenv("SORTIE_LOOP_REPO", "")
	if _, err := Load(dir); err == nil {
		t.Error("expected error with no repo detectable")
	}
}

func loadWithConfig(t *testing.T, yaml string, workflows map[string]string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sortie", "workflows"), 0o755)
	os.WriteFile(filepath.Join(dir, ".sortie", "config.yaml"), []byte("repo: o/r\n"+yaml), 0o644)
	for name, body := range workflows {
		os.WriteFile(filepath.Join(dir, ".sortie", "workflows", name), []byte(body), 0o644)
	}
	cfg, err := Load(dir)
	if err != nil {
		return nil, err
	}
	return cfg, cfg.CheckCleanup()
}

const cleanupYAML = "cleanup:\n  enabled: true\n  interval: 6h\n  command: ./scripts/cleanup.sh\n  max_age_days: 14\n  dry_run: true\n"

func TestCleanupValid(t *testing.T) {
	wf := map[string]string{
		"WORKFLOW.plan.md":  "---\nworkspace:\n  retention_days: 30\n---\n",
		"WORKFLOW.other.md": "---\nkind: x\n---\n",
	}
	cfg, err := loadWithConfig(t, cleanupYAML, wf)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Cleanup
	if !c.Enabled || c.Interval != 6*time.Hour || c.Command != "./scripts/cleanup.sh" ||
		c.MaxAgeDays != 14 || !c.DryRun || c.Timeout != 10*time.Minute {
		t.Errorf("unexpected cleanup config: %+v", c)
	}
}

func TestCleanupDisabledByDefault(t *testing.T) {
	cfg, err := loadWithConfig(t, "", nil)
	if err != nil || cfg.Cleanup.Enabled || cfg.Cleanup.Timeout != 10*time.Minute {
		t.Errorf("cfg %+v err %v", cfg, err)
	}
}

func TestCleanupInvalid(t *testing.T) {
	retention := map[string]string{"WORKFLOW.plan.md": "---\nworkspace:\n  retention_days: 14\n---\n"}
	cases := []struct {
		name, yaml, want string
		wf               map[string]string
	}{
		{"missing command", "cleanup:\n  enabled: true\n  interval: 1h\n  max_age_days: 3\n", "cleanup.command", nil},
		{"missing interval", "cleanup:\n  enabled: true\n  command: x\n  max_age_days: 3\n", "cleanup.interval", nil},
		{"bad interval", "cleanup:\n  interval: soon\n", "cleanup.interval", nil},
		{"missing max age", "cleanup:\n  enabled: true\n  command: x\n  interval: 1h\n", "max_age_days", nil},
		{"max age equals retention", cleanupYAML, "retention_days (14)", retention},
	}
	for _, tc := range cases {
		_, err := loadWithConfig(t, tc.yaml, tc.wf)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestSleepValid(t *testing.T) {
	cfg, err := loadWithConfig(t, "sleep:\n  enabled: true\n  idle_timeout: 15m\n  wake_interval: 1h\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := cfg.Sleep; !s.Enabled || s.IdleTimeout != 15*time.Minute || s.WakeInterval != time.Hour {
		t.Errorf("unexpected sleep config: %+v", s)
	}
}

func TestSleepDisabledByDefault(t *testing.T) {
	cfg, err := loadWithConfig(t, "sleep:\n  idle_timeout: 5m\n", nil)
	if err != nil || cfg.Sleep.Enabled {
		t.Errorf("cfg %+v err %v", cfg, err)
	}
}

func TestSleepInvalid(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"missing idle timeout", "sleep:\n  enabled: true\n  wake_interval: 1h\n", "sleep.idle_timeout"},
		{"missing wake interval", "sleep:\n  enabled: true\n  idle_timeout: 5m\n", "sleep.wake_interval"},
		{"zero", "sleep:\n  enabled: true\n  idle_timeout: 0s\n  wake_interval: 1h\n", "sleep.idle_timeout"},
		{"negative", "sleep:\n  enabled: true\n  idle_timeout: 5m\n  wake_interval: -1h\n", "sleep.wake_interval"},
		{"unparsable when disabled", "sleep:\n  wake_interval: soon\n", "sleep.wake_interval"},
	}
	for _, tc := range cases {
		_, err := loadWithConfig(t, tc.yaml, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		}
	}
}

const budgetYAML = "workflows:\n  plan:\n    budget:\n      period: weekly\n      timezone: Africa/Johannesburg\n      max_tokens: 20000000\n      warn_percent: 70\n      on_exceed: pause\n      poll_interval: 30s\n      max_usd: 50\n"

func TestBudgetValid(t *testing.T) {
	cfg, err := loadWithConfig(t, budgetYAML, map[string]string{"WORKFLOW.plan.md": "---\n---\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckBudgets(); err != nil {
		t.Fatal(err)
	}
	b := cfg.Budgets["plan"]
	if b.Period != "weekly" || b.Location.String() != "Africa/Johannesburg" || b.MaxTokens != 20000000 ||
		b.WarnPercent != 70 || b.PollInterval != 30*time.Second || b.MaxUSD != 50 {
		t.Errorf("unexpected budget: %+v", b)
	}
}

func TestBudgetDefaults(t *testing.T) {
	cfg, err := loadWithConfig(t, "workflows:\n  plan:\n    budget:\n      period: monthly\n      max_tokens: 5\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Budgets["plan"]
	if b.Location.String() != "UTC" || b.WarnPercent != 80 || b.PollInterval != time.Minute {
		t.Errorf("unexpected defaults: %+v", b)
	}
}

func TestBudgetDisabledWithoutMaxTokens(t *testing.T) {
	cfg, err := loadWithConfig(t, "workflows:\n  plan:\n    budget:\n      period: weekly\n", nil)
	if err != nil || cfg.Budgets["plan"].MaxTokens != 0 {
		t.Errorf("cfg %+v err %v, want a disabled budget", cfg, err)
	}
	if cfg, err = loadWithConfig(t, "", nil); err != nil || len(cfg.Budgets) != 0 {
		t.Errorf("no workflows: %v", err)
	}
}

func TestBudgetInvalid(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"missing period", "max_tokens: 5", "period is required"},
		{"bad period", "period: daily\n      max_tokens: 5", "period"},
		{"bad timezone", "period: weekly\n      max_tokens: 5\n      timezone: Mars/Base", "timezone"},
		{"bad on_exceed", "period: weekly\n      max_tokens: 5\n      on_exceed: stop", "on_exceed"},
		{"negative cap", "period: weekly\n      max_tokens: -5", "max_tokens"},
		{"warn percent too high", "period: weekly\n      max_tokens: 5\n      warn_percent: 101", "warn_percent"},
		{"bad poll interval", "period: weekly\n      max_tokens: 5\n      poll_interval: soon", "poll_interval"},
		{"negative usd", "period: weekly\n      max_tokens: 5\n      max_usd: -1", "max_usd"},
	} {
		_, err := loadWithConfig(t, "workflows:\n  plan:\n    budget:\n      "+c.body+"\n", nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestBudgetUnknownLoop(t *testing.T) {
	cfg, err := loadWithConfig(t, "workflows:\n  nope:\n    budget:\n      period: weekly\n      max_tokens: 5\n", map[string]string{"WORKFLOW.plan.md": "---\n---\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckBudgets(); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %v, want the unknown loop named", err)
	}
}
