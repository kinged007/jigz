package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// envMap turns the Env slice into a lookup, failing on a duplicate key
// so two knobs cannot quietly fight over one variable.
func envMap(t *testing.T, pairs []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, p := range pairs {
		k, v, _ := strings.Cut(p, "=")
		if _, dup := m[k]; dup {
			t.Fatalf("duplicate env key %q", k)
		}
		m[k] = v
	}
	return m
}

const twoRepos = `
defaults:
  assignee: "@me"
  agent:
    max_concurrent_agents: 1
    max_turns: 10
repos:
  - repo: owner/alpha
  - repo: owner/beta
    agent:
      max_concurrent_agents: 4
      model: gpt-5
    workflows: [plan, build]
    milestone: "v1"
    assignee: "someone"
    polling_interval_ms: 30000
`

// The whole design rests on unset staying unset: a repository with no
// agent block must produce no agent environment at all, so the engine's
// own defaults apply.
func TestUnsetInheritsNothingAndDefaultsApply(t *testing.T) {
	c, err := Load(write(t, twoRepos))
	if err != nil {
		t.Fatal(err)
	}
	all, err := c.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("resolved %d repos, want 2", len(all))
	}
	// Defaults cascade onto a repository that sets nothing.
	if got := *all[0].Assignee; got != "@me" {
		t.Errorf("alpha assignee = %q, want @me from defaults", got)
	}
	if all[0].Agent.MaxConcurrentAgents == nil || *all[0].Agent.MaxConcurrentAgents != 1 {
		t.Errorf("alpha max_concurrent_agents = %v, want 1 from defaults", all[0].Agent.MaxConcurrentAgents)
	}
	// A repository that overrides one field keeps the rest of defaults.
	if got := *all[1].Agent.MaxConcurrentAgents; got != 4 {
		t.Errorf("beta max_concurrent_agents = %d, want 4", got)
	}
	if got := *all[1].Agent.MaxTurns; got != 10 {
		t.Errorf("beta max_turns = %d, want 10 inherited", got)
	}
	if all[1].Agent.Model != "gpt-5" {
		t.Errorf("beta model = %q, want gpt-5", all[1].Agent.Model)
	}
	// A repository with no default-block field stays nil so nothing is
	// written for it.
	if all[0].PollingIntervalMS != nil {
		t.Errorf("alpha polling_interval_ms = %v, want nil", *all[0].PollingIntervalMS)
	}
	if all[0].Milestone != "" {
		t.Errorf("alpha milestone = %q, want empty", all[0].Milestone)
	}
}

func TestEnvOmitsUnsetAndSetsConfigured(t *testing.T) {
	c, err := Load(write(t, twoRepos))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.Resolve()

	alpha := envMap(t, mustEnv(t, all[0]))
	// Exactly the two default-derived knobs, and nothing else.
	if len(alpha) != 2 {
		t.Errorf("alpha env = %v, want only max_concurrent_agents and max_turns", alpha)
	}
	for k, want := range map[string]string{
		"SORTIE_AGENT_MAX_CONCURRENT_AGENTS": "1",
		"SORTIE_AGENT_MAX_TURNS":             "10",
	} {
		if alpha[k] != want {
			t.Errorf("alpha %s = %q, want %q", k, alpha[k], want)
		}
	}
	// Unset knobs must be absent, not empty: an empty value is a value
	// the engine would coerce, and absent is what leaves its default.
	for _, k := range []string{
		"SORTIE_AGENT_KIND", "SORTIE_AGENT_COMMAND", "SORTIE_CENTRAL_MODEL",
		"SORTIE_POLLING_INTERVAL_MS", "SORTIE_LOOP_ONLY", "SORTIE_LOOP_TOKEN",
	} {
		if v, ok := alpha[k]; ok {
			t.Errorf("alpha env sets %s=%q with nothing configured", k, v)
		}
	}

	beta := envMap(t, mustEnv(t, all[1]))
	for k, want := range map[string]string{
		"SORTIE_AGENT_MAX_CONCURRENT_AGENTS": "4",
		"SORTIE_AGENT_MAX_TURNS":             "10",
		"SORTIE_CENTRAL_MODEL":               "gpt-5",
		"SORTIE_POLLING_INTERVAL_MS":         "30000",
		"SORTIE_LOOP_ONLY":                   "plan,build",
	} {
		if beta[k] != want {
			t.Errorf("beta %s = %q, want %q", k, beta[k], want)
		}
	}
	if _, ok := beta["SORTIE_AGENT_KIND"]; ok {
		t.Error("beta env sets SORTIE_AGENT_KIND with no kind configured")
	}
}

// An explicit zero must survive the cascade: a repository asking for no
// concurrent agents means 0, not the default.
func TestExplicitZeroIsNotTreatedAsUnset(t *testing.T) {
	c, err := Load(write(t, `
defaults:
  agent:
    max_concurrent_agents: 3
repos:
  - repo: owner/zero
    agent:
      max_concurrent_agents: 0
`))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.Resolve()
	env := envMap(t, mustEnv(t, all[0]))
	if got := env["SORTIE_AGENT_MAX_CONCURRENT_AGENTS"]; got != "0" {
		t.Errorf("max_concurrent_agents = %q, want 0 (explicit zero must win over the default)", got)
	}
}

func TestTokenEnv(t *testing.T) {
	t.Setenv("SORTIE_TEST_TOKEN", "ghp_secret")
	c, err := Load(write(t, `
repos:
  - repo: owner/a
    token_env: SORTIE_TEST_TOKEN
  - repo: owner/b
    token_env: SORTIE_TEST_ABSENT
`))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.Resolve()
	if got := envMap(t, mustEnv(t, all[0]))["SORTIE_LOOP_TOKEN"]; got != "ghp_secret" {
		t.Errorf("SORTIE_LOOP_TOKEN = %q, want the resolved secret", got)
	}
	// A named variable that is not set is a startup failure, not a
	// silently unauthenticated loop that polls and claims nothing.
	if _, err := all[1].Env(); err == nil {
		t.Error("Env() with an unset token_env = nil error, want an error")
	}
}

// The token must never reach a file, so it belongs in the child
// environment only.
func TestTokenIsNeverInConfig(t *testing.T) {
	t.Setenv("SORTIE_TEST_TOKEN", "ghp_secret")
	c, err := Load(write(t, "repos:\n  - repo: owner/a\n    token_env: SORTIE_TEST_TOKEN\n"))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.Resolve()
	for _, p := range mustEnv(t, all[0]) {
		if !strings.HasPrefix(p, "SORTIE_LOOP_TOKEN=") && strings.Contains(p, "ghp_secret") {
			t.Errorf("secret leaked into env entry %q", p)
		}
	}
}

func TestDirDefaultsUnderStateDir(t *testing.T) {
	c, err := Load(write(t, "state_dir: /tmp/sc\nrepos:\n  - repo: owner/alpha\n  - repo: owner/beta\n    path: ~/somewhere/beta\n"))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.Resolve()
	if want := "/tmp/sc/repos/owner/alpha"; all[0].Dir != want {
		t.Errorf("alpha dir = %q, want %q", all[0].Dir, want)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, "somewhere", "beta"); all[1].Dir != want {
		t.Errorf("beta dir = %q, want %q (tilde expanded)", all[1].Dir, want)
	}
}

func TestEnabledDefaultsTrueAndFilters(t *testing.T) {
	c, err := Load(write(t, `
repos:
  - repo: owner/on
  - repo: owner/off
    enabled: false
  - repo: owner/scoped
    filters:
      build: "label:agent:build -label:needs-human"
`))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.Resolve()
	got := Enabled(all)
	if len(got) != 2 || got[0].Repo.Repo != "owner/on" || got[1].Repo.Repo != "owner/scoped" {
		var names []string
		for _, r := range got {
			names = append(names, r.Repo.Repo)
		}
		t.Errorf("Enabled() = %v, want the two enabled repos", names)
	}
	if all[1].Enabled() {
		t.Error("enabled: false still reports enabled")
	}
	if f := all[2].Repo.Filters["build"]; f == "" {
		t.Errorf("filters lost: %v", all[2].Repo.Filters)
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	for name, body := range map[string]string{
		"no repos":       "repos: []\n",
		"bare name":      "repos:\n  - repo: alpha\n",
		"too many parts": "repos:\n  - repo: a/b/c\n",
		"empty owner":    "repos:\n  - repo: /alpha\n",
		"whitespace":     "repos:\n  - repo: \"own er/alpha\"\n",
		"duplicate":      "repos:\n  - repo: o/a\n  - repo: o/a\n",
		"empty filter":   "repos:\n  - repo: o/a\n    filters:\n      \"\": x\n",
		"unknown key":    "repos:\n  - repo: o/a\n    workflowz: [build]\n",
		"empty workflow": "repos:\n  - repo: o/a\n    workflows: [\"\"]\n",
		"malformed":      "repos: [\n",
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: Load = nil error, want an error", name)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("missing file: Load = nil error, want an error")
	}
}

func TestGitHubSection(t *testing.T) {
	c, err := Load(write(t, "repos:\n  - repo: o/a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.GitHub.Enrich || c.GitHub.PassInterval() != 10*time.Minute || c.GitHub.BaseURL() != "https://api.github.com" {
		t.Errorf("defaults = %+v", c.GitHub)
	}
	c, err = Load(write(t, "github:\n  enrich: true\n  interval: 2m\n  api_url: https://ghe.example/api/v3/\nrepos:\n  - repo: o/a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.GitHub.Enrich || c.GitHub.PassInterval() != 2*time.Minute || c.GitHub.BaseURL() != "https://ghe.example/api/v3" {
		t.Errorf("section = %+v", c.GitHub)
	}
	for path, body := range map[string]string{
		"github.interval": "github:\n  interval: soon\n",
		"github.api_url":  "github:\n  api_url: ftp://x\n",
	} {
		_, err := Load(write(t, body+"repos:\n  - repo: o/a\n"))
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Path != path {
			t.Errorf("%s: err = %v, want a field error", path, err)
		}
	}
	if _, err := Load(write(t, "github:\n  interval: 5s\nrepos:\n  - repo: o/a\n")); err == nil {
		t.Error("an interval under a minute was accepted")
	}
}

func mustEnv(t *testing.T, r Resolved) []string {
	t.Helper()
	env, err := r.Env()
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// The shipped example is documentation, and documentation that does not
// load is worse than none: a copy of it would fail on the first run.
func TestExampleConfigLoadsAndResolves(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "sortie-central.example.yaml"))
	if err != nil {
		t.Fatalf("the shipped example does not load: %v", err)
	}
	all, err := c.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(c.Repos) {
		t.Fatalf("resolved %d of %d repositories", len(all), len(c.Repos))
	}
	var scoped, disabled, tokenScoped int
	for _, r := range all {
		if len(r.Workflows) > 0 {
			scoped++
			if _, err := r.Env(); err != nil {
				t.Errorf("%s: %v", r.Repo.Repo, err)
			}
		}
		if !r.Enabled() {
			disabled++
		}
		if r.Repo.TokenEnv != "" {
			tokenScoped++
		}
	}
	if scoped == 0 || disabled == 0 || tokenScoped == 0 {
		t.Errorf("the example no longer demonstrates scoping (%d), a disabled repository (%d), or token_env (%d)", scoped, disabled, tokenScoped)
	}
}

// The trap this whole partition exists to avoid: the engine applies the
// environment over front matter, and that environment is per
// repository. A knob any workflow overrides must therefore be absent
// from the environment, or it would silently beat the override on every
// workflow and the per-workflow value would never apply.
func TestOverriddenKnobLeavesTheEnvironment(t *testing.T) {
	c, err := Load(write(t, `
repos:
  - repo: owner/alpha
    agent:
      max_concurrent_agents: 2
      max_turns: 10
    workflows: [plan, build]
    workflow_overrides:
      build:
        agent:
          max_concurrent_agents: 6
`))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.Resolve()
	r := all[0]
	env := envMap(t, mustEnv(t, r))

	if _, ok := env["SORTIE_AGENT_MAX_CONCURRENT_AGENTS"]; ok {
		t.Error("an overridden knob is still in the environment; it would silently beat the override")
	}
	// A knob no workflow overrides stays on the environment path.
	if got := env["SORTIE_AGENT_MAX_TURNS"]; got != "10" {
		t.Errorf("SORTIE_AGENT_MAX_TURNS = %q, want 10 (nothing overrides it)", got)
	}
	// The repository value becomes the file default for both workflows,
	// with build's own value winning for build.
	plan := r.FileSettings("plan")
	if plan["max_concurrent_agents"] != "2" {
		t.Errorf("plan max_concurrent_agents = %q, want the repository value 2", plan["max_concurrent_agents"])
	}
	build := r.FileSettings("build")
	if build["max_concurrent_agents"] != "6" {
		t.Errorf("build max_concurrent_agents = %q, want its own 6", build["max_concurrent_agents"])
	}
	// A knob overridden by one workflow is written into every selected
	// workflow, so the non-overriding one is not left to the shipped
	// file default.
	if plan["max_turns"] != "" {
		t.Errorf("plan max_turns = %q, want it left on the environment path", plan["max_turns"])
	}
}

// With no repository value, only the overriding workflow gets the knob
// written; the others are untouched.
func TestOverrideWithNoRepositoryValue(t *testing.T) {
	c, _ := Load(write(t, `
repos:
  - repo: owner/alpha
    workflows: [plan, build]
    workflow_overrides:
      build:
        agent:
          model: opus
`))
	all, _ := c.Resolve()
	r := all[0]
	env := envMap(t, mustEnv(t, r))
	if v := env["SORTIE_CENTRAL_MODEL_BUILD"]; v != "opus" {
		t.Errorf("SORTIE_CENTRAL_MODEL_BUILD = %q, want opus", v)
	}
	if _, ok := env["SORTIE_CENTRAL_MODEL"]; ok {
		t.Error("a repository model variable was set with no repository model")
	}
	if len(r.FileSettings("plan")) != 0 {
		t.Errorf("plan file settings = %v, want none", r.FileSettings("plan"))
	}
	if got := r.FileSettings("build")["model"]; got != "opus" {
		t.Errorf("build model = %q, want opus", got)
	}
	if got := r.ModelVar("build"); got != "SORTIE_CENTRAL_MODEL_BUILD" {
		t.Errorf("ModelVar(build) = %q", got)
	}
	if got := r.ModelVar("plan"); got != "SORTIE_CENTRAL_MODEL" {
		t.Errorf("ModelVar(plan) = %q", got)
	}
}

// A repository model plus one override: the shared variable still has to
// exist for the workflows that did not override it.
func TestRepositoryModelCoexistsWithAnOverride(t *testing.T) {
	c, _ := Load(write(t, `
repos:
  - repo: owner/alpha
    agent:
      model: gpt-5
    workflows: [plan, build]
    workflow_overrides:
      build:
        agent:
          model: opus
`))
	all, _ := c.Resolve()
	env := envMap(t, mustEnv(t, all[0]))
	if env["SORTIE_CENTRAL_MODEL"] != "gpt-5" {
		t.Errorf("SORTIE_CENTRAL_MODEL = %q, want gpt-5", env["SORTIE_CENTRAL_MODEL"])
	}
	if env["SORTIE_CENTRAL_MODEL_BUILD"] != "opus" {
		t.Errorf("SORTIE_CENTRAL_MODEL_BUILD = %q, want opus", env["SORTIE_CENTRAL_MODEL_BUILD"])
	}
}

// An override naming a workflow that will not run would look configured
// while changing nothing.
func TestOverrideForAnUnselectedWorkflowIsRejected(t *testing.T) {
	_, err := Load(write(t, `
repos:
  - repo: owner/alpha
    workflows: [plan]
    workflow_overrides:
      review:
        agent:
          model: opus
`))
	if err == nil || !strings.Contains(err.Error(), "not in its workflows list") {
		t.Fatalf("Load = %v, want an error naming the unselected workflow", err)
	}
	// With no explicit list central cannot know what is installed, so
	// the name is left to be resolved at start.
	if _, err := Load(write(t, `
repos:
  - repo: owner/alpha
    workflow_overrides:
      review:
        agent:
          model: opus
`)); err != nil {
		t.Errorf("Load with no workflows list = %v, want it accepted", err)
	}
}

// The child is sortie-loop, which derives the tracker project, token,
// clone URL and workspace root itself. Validation calls the engine
// directly, so without these the workflow's $SORTIE_TRACKER_PROJECT and
// $SORTIE_LOOP_WORKSPACES expand to empty and validation rejects a
// repository that runs correctly under the child.
func TestValidateEnvCarriesWhatTheWorkflowsReference(t *testing.T) {
	t.Setenv("BOT_TOKEN", "ghp_secret")
	c, err := Load(write(t, `
repos:
  - repo: owner/alpha
    token_env: BOT_TOKEN
    workflows: [build]
    agent:
      model: gpt-5.6-luna
`))
	if err != nil {
		t.Fatal(err)
	}
	all, _ := c.Resolve()
	r := all[0]
	v, err := r.ValidateEnv()
	if err != nil {
		t.Fatal(err)
	}
	env := envMap(t, v)
	for k, want := range map[string]string{
		"SORTIE_TRACKER_PROJECT": "owner/alpha",
		"SORTIE_TRACKER_API_KEY": "ghp_secret",
		"SORTIE_LOOP_CLONE_URL":  "https://github.com/owner/alpha.git",
		"SORTIE_LOOP_WORKSPACES": filepath.Join(r.Dir, ".sortie", "workspaces"),
		"SORTIE_CENTRAL_MODEL":   "gpt-5.6-luna",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	// The child environment stays minimal: sortie-loop derives these
	// itself, so central must not duplicate them into the child.
	child := envMap(t, mustEnv(t, r))
	if _, ok := child["SORTIE_TRACKER_PROJECT"]; ok {
		t.Error("central duplicated a variable the child derives itself")
	}
}

// A named token_env that is unset is still fatal: the loop would
// otherwise authenticate as nobody.
func TestValidateEnvFailsOnAnUnsetTokenEnv(t *testing.T) {
	t.Setenv("ABSENT_TOKEN", "")
	c, _ := Load(write(t, `
repos:
  - repo: owner/alpha
    token_env: ABSENT_TOKEN
`))
	all, _ := c.Resolve()
	if _, err := all[0].ValidateEnv(); err == nil {
		t.Fatal("ValidateEnv = nil error, want a failure for an unset token_env")
	}
}

// With no token_env the engine's own GITHUB_TOKEN/GH_TOKEN fallback
// still has to reach a direct validation call.
func TestValidateEnvFallsBackToTheStandardTokenVariables(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "from-github")
	c, _ := Load(write(t, "repos:\n  - repo: owner/alpha\n"))
	all, _ := c.Resolve()
	env := envMap(t, mustValidate(t, all[0]))
	if env["SORTIE_TRACKER_API_KEY"] != "from-github" {
		t.Errorf("SORTIE_TRACKER_API_KEY = %q, want the GITHUB_TOKEN fallback", env["SORTIE_TRACKER_API_KEY"])
	}
}

func mustValidate(t *testing.T, r Resolved) []string {
	t.Helper()
	v, err := r.ValidateEnv()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// token_env at defaults level applies to every repository that does not
// name its own. The resolved value is what the child environment and the
// token lookup read, not the raw repository, so this also pins that the
// cascade is wired to the field that is actually used.
func TestTokenEnvInheritsFromDefaults(t *testing.T) {
	noAmbientToken(t)
	t.Setenv("CENTRAL_TOKEN", "central-secret")
	t.Setenv("REPO_TOKEN", "repo-secret")

	cfg := `
defaults:
  token_env: CENTRAL_TOKEN
repos:
  - repo: owner/inherits
  - repo: owner/overrides
    token_env: REPO_TOKEN
`
	c, err := Load(write(t, cfg))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	res, err := c.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := res[0].TokenEnv; got != "CENTRAL_TOKEN" {
		t.Errorf("inheriting repo TokenEnv = %q, want the defaults value", got)
	}
	if got := res[0].Token(); got != "central-secret" {
		t.Errorf("inheriting repo token = %q, want the value of CENTRAL_TOKEN", got)
	}
	// The token reaches the child as SORTIE_LOOP_TOKEN, sortie-loop's
	// highest-precedence override. The named variable needs no entry:
	// the child inherits central's own environment, which already has
	// it. What matters is that the secret appears in no other entry.
	env := mustEnv(t, res[0])
	if got := envValue(env, "SORTIE_LOOP_TOKEN"); got != "central-secret" {
		t.Errorf("SORTIE_LOOP_TOKEN did not carry the token from CENTRAL_TOKEN")
	}
	for _, e := range env {
		if strings.Contains(e, "central-secret") && !strings.HasPrefix(e, "SORTIE_LOOP_TOKEN=") {
			t.Errorf("the token leaked into another env entry: %s", strings.SplitN(e, "=", 2)[0])
		}
	}

	// A repository with its own token_env wins over the default.
	if got := res[1].TokenEnv; got != "REPO_TOKEN" {
		t.Errorf("overriding repo TokenEnv = %q, want its own", got)
	}
	if got := res[1].Token(); got != "repo-secret" {
		t.Errorf("overriding repo token = %q, want its own token", got)
	}
}

// With no token_env anywhere, the engine's own GITHUB_TOKEN/GH_TOKEN
// fallback applies, exactly as before.
func TestNoTokenEnvAnywhereFallsBackToGitHubToken(t *testing.T) {
	noAmbientToken(t)
	t.Setenv("CENTRAL_TOKEN", "unused")
	t.Setenv("GITHUB_TOKEN", "github-secret")
	c, err := Load(write(t, "repos:\n  - repo: owner/plain\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	res, err := c.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := res[0].TokenEnv; got != "" {
		t.Errorf("TokenEnv = %q, want empty", got)
	}
	if got := res[0].Token(); got != "github-secret" {
		t.Errorf("token = %q, want GITHUB_TOKEN", got)
	}
	// No named variable, so nothing is forced into the environment.
	if got := envValue(mustEnv(t, res[0]), "SORTIE_LOOP_TOKEN"); got != "" {
		t.Errorf("SORTIE_LOOP_TOKEN = %q, want unset without a token_env to source it from", got)
	}
}

// envValue reads one KEY=VALUE entry out of an environment slice.
func envValue(env []string, key string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}

// noAmbientToken clears the engine's own token lookups for the duration
// of a test. Without it a real token in the developer's environment is
// picked up by the GITHUB_TOKEN/GH_TOKEN fallback, and comparing it
// against an expected value prints the secret into test output.
func noAmbientToken(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
}

func TestAccessKeys(t *testing.T) {
	c, err := Load(write(t, "access:\n  public_read: true\n  trusted_proxy: true\nrepos:\n  - repo: o/a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Access.PublicRead || !c.Access.TrustedProxy {
		t.Errorf("Access = %+v, want both true", c.Access)
	}
	c, err = Load(write(t, "repos:\n  - repo: o/a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Access.PublicRead || c.Access.TrustedProxy {
		t.Errorf("default Access = %+v, want both false", c.Access)
	}
	if _, err := Load(write(t, "access:\n  password: x\nrepos:\n  - repo: o/a\n")); err == nil {
		t.Error("a password key under access loaded; the password must come from the environment")
	}
}

func TestCascadeReportsWhereEachValueCameFrom(t *testing.T) {
	c := &Config{
		Defaults: Defaults{Agent: Agent{MaxTurns: intp(10), Model: "m0"}, PollingIntervalMS: intp(60000)},
		Repos: []Repo{{
			Repo: "o/a", Agent: Agent{Model: "m1"},
			Overrides: map[string]Workflow{"plan": {Agent: Agent{MaxTurns: intp(3)}}},
		}},
	}
	want := map[string]Setting{
		"max_turns":           {"3", OriginWorkflow},
		"model":               {"m1", OriginRepo},
		"polling_interval_ms": {"60000", OriginDefault},
	}
	got := c.Cascade("o/a", "plan")
	if len(got) != len(want) {
		t.Fatalf("cascade = %+v", got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got[k], w)
		}
	}
	if got := c.Cascade("o/a", ""); got["max_turns"] != (Setting{"10", OriginDefault}) {
		t.Errorf("repo level = %+v", got)
	}
	if got := c.Cascade("", ""); got["model"] != (Setting{"m0", OriginDefault}) {
		t.Errorf("defaults level = %+v", got)
	}
}

func TestLoadNamesTheFieldOfARepoError(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	os.WriteFile(p, []byte("repos:\n  - repo: o/a\n  - repo: bad\n"), 0o600)
	_, err := Load(p)
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Path != "repos.1.repo" {
		t.Errorf("err = %v, want a field error for repos.1.repo", err)
	}
}

func intp(n int) *int { return &n }
