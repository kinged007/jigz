package config

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestLoadEnvAndFilterFor(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755)
	os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"),
		[]byte("repo: file/o\ntoken: filetok\n"), 0o644)
	t.Setenv("JIG_REPO", "o/r")
	t.Setenv("JIG_TOKEN", "tok")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "o/r" || cfg.Token != "tok" || cfg.Tracker != "o/r" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if got := cfg.FilterFor("issues"); got != "" {
		t.Errorf("filter without query_filter or filters entry: %q", got)
	}
	if cfg.Labels != nil {
		t.Errorf("labels without a labels key: %+v", cfg.Labels)
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755)
	os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"),
		[]byte("repo: file/o\ntoken: filetok\n"), 0o644)
	t.Setenv("JIG_REPO", "")
	t.Setenv("JIG_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "file/o" || cfg.Token != "filetok" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Tracker != "file/o" {
		t.Errorf("tracker: %q", cfg.Tracker)
	}
}

func TestLoadFilterOverrides(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755)
	os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"),
		[]byte("repo: file/o\ntoken: filetok\nfilters:\n  triage: label:agent:triage\n  issues: label:agent:custom\n"), 0o644)
	t.Setenv("JIG_REPO", "")
	t.Setenv("JIG_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FilterFor("triage") != "label:agent:triage assignee:@me" {
		t.Errorf("override filter: %q", cfg.FilterFor("triage"))
	}
	if cfg.FilterFor("issues") != "label:agent:custom assignee:@me" {
		t.Errorf("known-loop override: %q", cfg.FilterFor("issues"))
	}
	if got := cfg.FilterFor("prs"); got != "" {
		t.Errorf("slug without an entry: %q", got)
	}
}

// TestFilterForWorkQueryFilter pins the precedence: a work entry's
// query_filter is the complete tracker query and wins over the filters:
// override and the defaults; the assignee scope is appended to all.
func TestFilterForWorkQueryFilter(t *testing.T) {
	cfg, err := loadWithConfig(t, "filters:\n  issues: label:from-filters\nwork:\n  issues:\n    managed: false\n    query_filter: \"label:from-work\"\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.FilterFor("issues"); got != "label:from-work assignee:@me" {
		t.Errorf("work query_filter: %q", got)
	}
}

func TestWorkEntryWithoutFilterFails(t *testing.T) {
	_, err := loadWithConfig(t, "work:\n  issues:\n    rules:\n    - {name: build, match: {labels: [agent:build]}, prompt: prompts/build.md}\n", nil)
	if err == nil || !strings.Contains(err.Error(), "work.issues needs query_filter") {
		t.Fatalf("err = %v", err)
	}
	cfg, err := loadWithConfig(t, "filters:\n  issues: label:agent:build\nwork:\n  issues:\n    rules:\n    - {name: build, match: {labels: [agent:build]}, prompt: prompts/build.md}\n", nil)
	if err != nil {
		t.Fatalf("filters entry: %v", err)
	}
	if got := cfg.FilterFor("issues"); got != "label:agent:build assignee:@me" {
		t.Errorf("filters entry: %q", got)
	}
}

func TestManagedWorkEntryWithoutRulesFails(t *testing.T) {
	_, err := loadWithConfig(t, "work:\n  issues:\n    query_filter: label:agent:build\n", nil)
	if err == nil || !strings.Contains(err.Error(), "work.issues needs rules") {
		t.Fatalf("err = %v", err)
	}
	if _, err := loadWithConfig(t, "work:\n  issues:\n    managed: false\n    query_filter: label:agent:build\n", nil); err != nil {
		t.Fatalf("unmanaged entry: %v", err)
	}
}

func TestLoadMissingConfigFails(t *testing.T) {
	dir := t.TempDir() // no .jigz/config.yaml, git remote irrelevant
	t.Setenv("JIG_REPO", "o/r")
	if _, err := Load(dir); err == nil {
		t.Error("expected error with no config file")
	}
}

func TestLoadNoRepoFails(t *testing.T) {
	dir := t.TempDir() // no git remote, no config
	t.Setenv("JIG_REPO", "")
	if _, err := Load(dir); err == nil {
		t.Error("expected error with no repo detectable")
	}
}

func loadWithConfig(t *testing.T, yaml string, workflows map[string]string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jigz", "workflows"), 0o755)
	os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte("repo: o/r\n"+yaml), 0o644)
	for name, body := range workflows {
		os.WriteFile(filepath.Join(dir, ".jigz", "workflows", name), []byte(body), 0o644)
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

const budgetYAML = "work:\n  plan:\n    managed: false\n    query_filter: x\n    budget:\n      period: weekly\n      timezone: Africa/Johannesburg\n      max_tokens: 20000000\n      warn_percent: 70\n      on_exceed: pause\n      poll_interval: 30s\n      max_usd: 50\n"

func TestBudgetValid(t *testing.T) {
	cfg, err := loadWithConfig(t, budgetYAML, map[string]string{"WORKFLOW.plan.md": "---\n---\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckBudgets(); err != nil {
		t.Fatal(err)
	}
	b := cfg.Budgets()["plan"]
	if b.Period != "weekly" || b.Location.String() != "Africa/Johannesburg" || b.MaxTokens != 20000000 ||
		b.WarnPercent != 70 || b.PollInterval != 30*time.Second || b.MaxUSD != 50 {
		t.Errorf("unexpected budget: %+v", b)
	}
}

func TestBudgetDefaults(t *testing.T) {
	cfg, err := loadWithConfig(t, "work:\n  plan:\n    managed: false\n    query_filter: x\n    budget:\n      period: monthly\n      max_tokens: 5\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Budgets()["plan"]
	if b.Location.String() != "UTC" || b.WarnPercent != 80 || b.PollInterval != time.Minute {
		t.Errorf("unexpected defaults: %+v", b)
	}
}

func TestBudgetDisabledWithoutMaxTokens(t *testing.T) {
	cfg, err := loadWithConfig(t, "work:\n  plan:\n    managed: false\n    query_filter: x\n    budget:\n      period: weekly\n", nil)
	if err != nil || cfg.Budgets()["plan"].MaxTokens != 0 {
		t.Errorf("cfg %+v err %v, want a disabled budget", cfg, err)
	}
	if cfg, err = loadWithConfig(t, "", nil); err != nil || len(cfg.Budgets()) != 0 {
		t.Errorf("no work entries: %v", err)
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
		_, err := loadWithConfig(t, "work:\n  plan:\n    managed: false\n    query_filter: x\n    budget:\n      "+c.body+"\n", nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestBudgetUnknownLoop(t *testing.T) {
	cfg, err := loadWithConfig(t, "work:\n  nope:\n    managed: false\n    query_filter: x\n    budget:\n      period: weekly\n      max_tokens: 5\n", map[string]string{"WORKFLOW.plan.md": "---\n---\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckBudgets(); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %v, want the unknown loop named", err)
	}
}

func TestMigrateRepoDirMovesLegacyDirectory(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, ".sortie")
	if err := os.MkdirAll(filepath.Join(old, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "config.yaml"), []byte("repo: o/r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	MigrateRepoDir(dir)
	if _, err := os.Stat(filepath.Join(dir, ".jigz", "config.yaml")); err != nil {
		t.Fatalf("config not moved: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf(".sortie still present after move")
	}
	// A second run leaves an existing .jigz alone.
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	MigrateRepoDir(dir)
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("a leftover .sortie must not be moved over an existing .jigz: %v", err)
	}
}

func TestGitHubBlock(t *testing.T) {
	cfg, err := loadWithConfig(t, "github:\n  enrich: true\n  questions: false\n  api_url: http://127.0.0.1:9/\n  interval: 5m\n  details: true\n  questions_interval: 3m\n  questions_window: 24h\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := cfg.GitHub
	if !g.Enrich || g.QuestionsOn() || !g.DetailsOn() || g.BaseURL() != "http://127.0.0.1:9" || g.PassInterval() != 5*time.Minute ||
		g.QuestionPassInterval() != 3*time.Minute || g.QuestionWindow() != 24*time.Hour {
		t.Errorf("unexpected github block: %+v", g)
	}
	dir := filepath.Dir(filepath.Dir(cfg.Workspaces))
	if lg, err := LoadGitHub(dir); err != nil || !reflect.DeepEqual(lg, g) {
		t.Errorf("LoadGitHub = %+v, %v; want %+v", lg, err, g)
	}
}

func TestGitHubBlockDefaults(t *testing.T) {
	cfg, err := loadWithConfig(t, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := cfg.GitHub
	if g.Enrich || !g.QuestionsOn() || g.BaseURL() != "https://api.github.com" || g.PassInterval() != 10*time.Minute ||
		g.QuestionPassInterval() != 2*time.Minute || g.QuestionWindow() != 168*time.Hour {
		t.Errorf("unexpected default github block: %+v", g)
	}
}

func TestGitHubQuestionsKey(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want bool
	}{
		{"github:\n  enrich: false\n", true},
		{"github:\n  questions: true\n", true},
		{"github:\n  questions: false\n", false},
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		g, err := LoadGitHub(dir)
		if err != nil || g.QuestionsOn() != tc.want {
			t.Errorf("%q: QuestionsOn = %v, %v; want %v", tc.yaml, g.QuestionsOn(), err, tc.want)
		}
	}
}

func TestGitHubBlockInvalid(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"unknown key", "github:\n  enrich: true\n  enrichh: true\n", "enrichh"},
		{"bad api url", "github:\n  api_url: ftp://x\n", "github.api_url"},
		{"short interval", "github:\n  interval: 10s\n", "github.interval"},
		{"bad questions window", "github:\n  questions_window: soon\n", "github.questions_window"},
	}
	for _, tc := range cases {
		_, err := loadWithConfig(t, tc.yaml, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestLoadTokenHasNoSideEffects(t *testing.T) {
	t.Setenv("JIG_TOKEN", "")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte("token: filetok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No repo: and no git checkout, so Load would fail on detectRepo.
	if tok, err := LoadToken(dir); err != nil || tok != "filetok" {
		t.Errorf("LoadToken = %q, %v; want filetok", tok, err)
	}
	t.Setenv("JIG_TOKEN", "jigtok")
	if tok, _ := LoadToken(dir); tok != "jigtok" {
		t.Errorf("LoadToken = %q, want JIG_TOKEN first", tok)
	}
	old := t.TempDir()
	if err := os.MkdirAll(filepath.Join(old, ".sortie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(old); err == nil {
		t.Error("LoadToken read a config from a dir without .jigz/config.yaml")
	}
	if _, err := os.Stat(filepath.Join(old, ".sortie")); err != nil {
		t.Errorf("LoadToken moved .sortie: %v", err)
	}
}
