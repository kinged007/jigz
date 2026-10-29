package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadFixture loads testdata/<name> as a repository's .jigz/config.yaml.
// The fixture supplies repo: itself or the test sets JIG_REPO first.
func loadFixture(t *testing.T, name string) *Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return cfg
}

func TestSecretLiteralRejected(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"api_key", "tracker:\n  api_key: literal\n", "tracker.api_key"},
		{"token", "agents:\n  a:\n    runtime: pi\n    env:\n      GH_TOKEN: literal\n", "agents.a.env.GH_TOKEN"},
		{"key", "agents:\n  a:\n    runtime: pi\n    env:\n      SOME_KEY: literal\n", "agents.a.env.SOME_KEY"},
		{"secret", "agents:\n  a:\n    runtime: pi\n    env:\n      A_SECRET: literal\n", "agents.a.env.A_SECRET"},
		{"password", "agents:\n  a:\n    runtime: pi\n    env:\n      DB_PASSWORD: literal\n", "agents.a.env.DB_PASSWORD"},
	}
	for _, c := range cases {
		_, err := loadWithConfig(t, c.body, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "from_env") {
			t.Errorf("%s: error %v, want it to name %q and the from_env form", c.name, err, c.want)
		}
	}
}

func TestSecretReferenceAccepted(t *testing.T) {
	t.Setenv("JIG_TRACKER_TOKEN", "s3cret")
	cfg, err := loadWithConfig(t, "tracker:\n  api_key: {from_env: JIG_TRACKER_TOKEN}\n  project: o/r\nagents:\n  a:\n    runtime: pi\n    env:\n      ANTHROPIC_API_KEY: {from_env: JIG_TRACKER_TOKEN}\n      CLAUDE_CONFIG_DIR: ~/.claude\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Agents["a"].Env["ANTHROPIC_API_KEY"]; got.FromEnv != "JIG_TRACKER_TOKEN" {
		t.Errorf("env reference = %+v", got)
	}
	if got := cfg.Agents["a"].Env["CLAUDE_CONFIG_DIR"]; got.Str != "~/.claude" {
		t.Errorf("plain env value = %+v", got)
	}
	if st := cfg.UnresolvedRefs(); len(st.Agents) != 0 || len(st.Tracker) != 0 {
		t.Errorf("UnresolvedRefs = %+v, want none", st)
	}
}

// project names a project, not a credential: a plain string is accepted.
func TestTrackerProjectPlainStringAccepted(t *testing.T) {
	cfg, err := loadWithConfig(t, "tracker:\n  project: o/r\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.TrackerDefaults.Project.Str; got != "o/r" {
		t.Errorf("project = %q", got)
	}
}

func TestUnresolvedRefsReported(t *testing.T) {
	t.Setenv("JIGZ_TEST_MISSING_TRACKER", "")
	t.Setenv("JIGZ_TEST_MISSING_TOKEN", "")
	cfg, err := loadWithConfig(t, "tracker:\n  api_key: {from_env: JIGZ_TEST_MISSING_TRACKER}\nagents:\n  a:\n    runtime: pi\n    env:\n      SOME_TOKEN: {from_env: JIGZ_TEST_MISSING_TOKEN}\n", nil)
	if err != nil {
		t.Fatalf("an unresolved reference must not fail the load: %v", err)
	}
	st := cfg.UnresolvedRefs()
	if len(st.Tracker) != 1 || st.Tracker[0] != "JIGZ_TEST_MISSING_TRACKER" {
		t.Errorf("tracker refs = %v", st.Tracker)
	}
	if got := st.Agents["a"]; len(got) != 1 || got[0] != "JIGZ_TEST_MISSING_TOKEN" {
		t.Errorf("agent refs = %v", got)
	}
}

func TestNameResolution(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"pool names a missing agent", "agents:\n  a: {runtime: pi}\npools:\n  p: [a, ghost]\n", `pools.p: unknown agent "ghost"`},
		{"pool and agent share a name", "agents:\n  x: {runtime: pi}\npools:\n  x: [x]\n", "pools.x: name is also an agent"},
		{"work agent missing", "agents:\n  a: {runtime: pi}\nwork:\n  issues:\n    agent: ghost\n", `work.issues.agent: unknown agent or pool "ghost"`},
		{"rule agent missing", "agents:\n  a: {runtime: pi}\nwork:\n  issues:\n    rules:\n    - {name: r, prompt: p.md, agent: ghost}\n", `work.issues.rules[0].agent: unknown agent or pool "ghost"`},
		{"rule agent mapping use missing", "agents:\n  a: {runtime: pi}\nwork:\n  issues:\n    rules:\n    - {name: r, prompt: p.md, agent: {use: ghost}}\n", `work.issues.rules[0].agent: unknown agent or pool "ghost"`},
		{"rule agent mapping without use", "work:\n  issues:\n    rules:\n    - {name: r, prompt: p.md, agent: {model: m}}\n", "work.issues.rules[0].agent: use is required"},
		{"duplicate rule names", "work:\n  issues:\n    rules:\n    - {name: build, prompt: p.md}\n    - {name: build, prompt: p.md}\n", `work.issues.rules[1].name: "build" is used twice`},
		{"next names an earlier rule", "work:\n  issues:\n    rules:\n    - {name: a, prompt: p.md}\n    - {name: b, prompt: p.md, next: a}\n", `work.issues.rules[1].next: "a" does not name a later rule`},
		{"next names a missing rule", "work:\n  issues:\n    rules:\n    - {name: a, prompt: p.md, next: ghost}\n", `work.issues.rules[0].next: "ghost" does not name a later rule`},
		{"bad runtime", "agents:\n  a: {runtime: opencode}\n", `agents.a.runtime: unknown runtime "opencode"`},
		{"bad engine version", "engine:\n  version: 1.28\n", "engine.version"},
		{"bad work name", "work:\n  my slug:\n    query_filter: x\n", "work.my slug"},
	}
	for _, c := range cases {
		_, err := loadWithConfig(t, c.body, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want it to contain %q", c.name, err, c.want)
		}
	}
}

// Example 1 of the engine interface plan: a business-hours reviewer and
// a 24/7 builder.
func TestExample1(t *testing.T) {
	cfg := loadFixture(t, "example1.yaml")
	res := cfg.Resolve()
	if len(res) != 2 {
		t.Fatalf("resolved %d entries, want 2", len(res))
	}
	issues := res["issues"]
	if issues.QueryFilter != "label:agent:build,in-progress -label:needs-human" {
		t.Errorf("issues query_filter = %q", issues.QueryFilter)
	}
	if !issues.Managed {
		t.Error("issues must default to managed")
	}
	if got := issues.Tracker; got.Kind != "github" || got.ApiKey.FromEnv != "JIG_TRACKER_TOKEN" || got.Project.Str != "owner/repo" {
		t.Errorf("issues tracker = %+v", got)
	}
	if len(issues.Agents) != 1 || issues.Agents[0] != "builder" {
		t.Errorf("issues agents = %v", issues.Agents)
	}
	if len(issues.Rules) != 1 || issues.Rules[0].Name != "build" ||
		strings.Join(issues.Rules[0].Labels, ",") != "agent:build" ||
		issues.Rules[0].Prompt != "prompts/build.md" ||
		len(issues.Rules[0].Agents) != 1 || issues.Rules[0].Agents[0] != "builder" {
		t.Errorf("issues rules = %+v", issues.Rules)
	}
	prs := res["prs"]
	// The entry's tracker override replaces the kind and keeps the
	// defaults' reference and project.
	if got := prs.Tracker; got.Kind != "github-pr" || got.ApiKey.FromEnv != "JIG_TRACKER_TOKEN" || got.Project.Str != "owner/repo" {
		t.Errorf("prs tracker = %+v", got)
	}
	if len(prs.Agents) != 1 || prs.Agents[0] != "reviewer" {
		t.Errorf("prs agents = %v", prs.Agents)
	}
	if issues.Engine.Repo != "kinged007/sortie" || issues.Engine.Version != "v1.28.1" {
		t.Errorf("engine = %+v", issues.Engine)
	}
	if len(issues.OperatingHours.Windows) != 0 || issues.OperatingHours.End != "finish" {
		t.Errorf("operating hours = %+v, want 24/7 with end finish", issues.OperatingHours)
	}
	if issues.Budget != nil {
		t.Errorf("budget = %+v, want none", issues.Budget)
	}
	reviewer := cfg.Agents["reviewer"]
	if reviewer.Runtime != "claude-code" || reviewer.Model != "claude-sonnet-5-5" ||
		reviewer.Schedule.Timezone != "Europe/Madrid" || len(reviewer.Schedule.Windows) != 1 ||
		reviewer.Schedule.Windows[0].Start != "09:00" || reviewer.Schedule.Windows[0].End != "18:00" ||
		strings.Join(reviewer.Schedule.Windows[0].Days, ",") != "mon,tue,wed,thu,fri" {
		t.Errorf("reviewer agent = %+v", reviewer)
	}
}

// Example 2 of the engine interface plan: two runtimes with a fallback
// pool.
func TestExample2(t *testing.T) {
	t.Setenv("JIG_REPO", "o/r")
	cfg := loadFixture(t, "example2.yaml")
	res := cfg.Resolve()
	issues := res["issues"]
	want := []string{"claude-main", "claude-spare", "pi-api"}
	if strings.Join(issues.Agents, ",") != strings.Join(want, ",") {
		t.Errorf("pool expanded to %v, want %v", issues.Agents, want)
	}
	if got := issues.Rules[0].Agents; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rule candidates = %v, want the entry's %v", got, want)
	}
	if got := cfg.Agents["pi-api"].Env["ANTHROPIC_API_KEY"]; got.FromEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("pi-api env = %+v", got)
	}
	st := cfg.UnresolvedRefs()
	for _, name := range want {
		if len(st.Agents[name]) == 0 {
			t.Errorf("agent %s has no unresolved refs reported: %+v", name, st)
		}
	}
}

// Example 3 of the engine interface plan: rules with their own agents,
// an agent array and stages.
func TestExample3(t *testing.T) {
	t.Setenv("JIG_REPO", "o/r")
	cfg := loadFixture(t, "example3.yaml")
	res := cfg.Resolve()
	delivery := res["delivery"]
	if delivery.Hooks.TimeoutMS != 300000 {
		t.Errorf("hooks override = %+v", delivery.Hooks)
	}
	if len(delivery.Agents) != 0 {
		t.Errorf("entry candidates = %v, want none without an entry agent", delivery.Agents)
	}
	if len(delivery.Rules) != 4 {
		t.Fatalf("rules = %+v", delivery.Rules)
	}
	if got := delivery.Rules[0]; got.Name != "plan" || strings.Join(got.Agents, ",") != "planner" || got.Next != "" {
		t.Errorf("rule plan = %+v", got)
	}
	if got := delivery.Rules[1]; got.Name != "build" || strings.Join(got.Agents, ",") != "builder-a,builder-b" {
		t.Errorf("rule build = %+v", got)
	}
	if got := delivery.Rules[2]; got.Name != "plan-build" || got.Next != "build-chain" {
		t.Errorf("rule plan-build = %+v", got)
	}
	if got := delivery.Rules[3]; got.Name != "build-chain" || got.Stage != "stage-build" ||
		strings.Join(got.Agents, ",") != "builder-a,builder-b" {
		t.Errorf("rule build-chain = %+v", got)
	}
}
