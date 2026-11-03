package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const allKeysYAML = `repo: o/r
token: t
filters:
  issues: "label:agent:build"
labels:
- {name: "agent:build", color: "fbca04", description: "Trigger"}
cleanup:
  enabled: true
  interval: 6h
  command: ./scripts/cleanup.sh
  max_age_days: 14
  dry_run: true
  timeout: 10m
sleep:
  enabled: true
  idle_timeout: 15m
  wake_interval: 1h
github:
  enrich: true
  questions: true
  interval: 10m
  api_url: https://api.github.com
  details: false
  questions_interval: 2m
  questions_window: 168h
engine:
  repo: kinged007/sortie
  version: v1.28.1
operating_hours:
  timezone: UTC
  end: finish
  windows:
  - days: [mon, tue, wed, thu, fri]
    start: "09:00"
    end: "18:00"
tracker:
  kind: github
  api_key: {from_env: JIG_TRACKER_TOKEN}
  project: o/r
  active_states: [in-progress]
  in_progress_state: in-progress
  handoff_state: needs-human
hooks:
  after_create: ./hooks/create.sh
  before_run: ./hooks/before.sh
  after_run: ./hooks/after.sh
  timeout_ms: 60000
agents:
  builder:
    runtime: claude-code
    model: claude-sonnet-5-5
    schedule:
      timezone: Europe/Madrid
      end: stop
      windows:
      - days: [mon]
        start: "09:00"
        end: "18:00"
    env:
      CLAUDE_CONFIG_DIR: ~/.claude-work
      ANTHROPIC_OAUTH_TOKEN: {from_env: CLAUDE_SUB_1}
pools:
  build-pool: [builder]
work:
  issues:
    managed: false
    query_filter: "label:agent:build"
    tracker:
      kind: github-pr
    hooks:
      timeout_ms: 300000
    max_concurrent_agents: 2
    max_turns: 10
    polling_interval_ms: 5000
    budget:
      period: weekly
      timezone: UTC
      max_tokens: 1000
      warn_percent: 80
      on_exceed: pause
      poll_interval: 1m
      max_usd: 5
    agent: build-pool
    rules:
    - name: build
      match: {labels: [agent:build]}
      prompt: prompts/build.md
      agent: {use: builder, model: claude-haiku-5-5, schedule: {timezone: UTC, windows: [{days: [tue], start: "10:00", end: "12:00"}]}}
      next: chain
      stage: stage-build
    - name: chain
      prompt: prompts/build.md
      agent: [builder]
`

func loadersOn(t *testing.T, body string) map[string]error {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, loadErr := Load(dir)
	_, tokenErr := LoadToken(dir)
	_, githubErr := LoadGitHub(dir)
	return map[string]error{"Load": loadErr, "LoadToken": tokenErr, "LoadGitHub": githubErr}
}

func TestAllDocumentedKeysAccepted(t *testing.T) {
	for name, err := range loadersOn(t, allKeysYAML) {
		if err != nil {
			t.Errorf("%s rejected a valid config: %v", name, err)
		}
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	cases := []struct{ name, body, path string }{
		{"top-level", "repo: o/r\nasignee: x\n", "asignee"},
		{"cleanup", "cleanup:\n  enabled: true\n  intervl: 6h\n", "cleanup.intervl"},
		{"sleep", "sleep:\n  enabled: true\n  idle_timout: 1m\n", "sleep.idle_timout"},
		{"github", "github:\n  enrich: true\n  enrch: true\n", "github.enrch"},
		{"label", "labels:\n- {name: a, colour: b}\n", "labels[0].colour"},
		{"engine", "engine:\n  verison: v1.28.1\n", "engine.verison"},
		{"operating_hours", "operating_hours:\n  windows:\n  - {days: [mon], start: \"09:00\", end: \"10:00\", form: x}\n", "operating_hours.windows[0].form"},
		{"tracker", "tracker:\n  kind: github\n  handoff_evidence: x\n", "tracker.handoff_evidence"},
		{"tracker ref", "tracker:\n  api_key: {frome_env: X}\n", "tracker.api_key.frome_env"},
		{"hooks", "hooks:\n  time_out: 1\n", "hooks.time_out"},
		{"agent", "agents:\n  reviewer:\n    runtime: pi\n    effort: 1\n", "agents.reviewer.effort"},
		{"agent schedule", "agents:\n  reviewer:\n    runtime: pi\n    schedule: {timezonne: UTC}\n", "agents.reviewer.schedule.timezonne"},
		{"agent env", "agents:\n  a:\n    runtime: pi\n    env:\n      X: {from_env: Y, oops: z}\n", "agents.a.env.X.oops"},
		{"rule agent schedule", "work:\n  issues:\n    rules:\n    - {name: build, prompt: p.md, agent: {use: a, schedule: {form: x}}}\n", "work.issues.rules[0].agent.schedule.form"},
		{"work", "work:\n  issues:\n    query_filter: x\n    budgt: {}\n", "work.issues.budgt"},
		{"work tracker", "work:\n  issues:\n    tracker: {kind: github, proejct: o/r}\n", "work.issues.tracker.proejct"},
		{"work hooks", "work:\n  issues:\n    hooks: {time_out: 1}\n", "work.issues.hooks.time_out"},
		{"budget", "work:\n  issues:\n    budget:\n      max_token: 1\n", "work.issues.budget.max_token"},
		{"rule", "work:\n  issues:\n    rules:\n    - {name: build, prompt: p.md, effort: 1}\n", "work.issues.rules[0].effort"},
		{"rule match", "work:\n  issues:\n    rules:\n    - {name: build, match: {lables: [x]}, prompt: p.md}\n", "work.issues.rules[0].match.lables"},
		{"rule agent mapping", "work:\n  issues:\n    rules:\n    - {name: build, prompt: p.md, agent: {use: a, modle: m}}\n", "work.issues.rules[0].agent.modle"},
	}
	for _, c := range cases {
		for loader, err := range loadersOn(t, c.body) {
			if err == nil {
				t.Errorf("%s/%s: unknown key accepted", c.name, loader)
				continue
			}
			if !strings.Contains(err.Error(), ".jigz/config.yaml") || !strings.Contains(err.Error(), `unknown key "`+c.path+`"`) {
				t.Errorf("%s/%s: error %q does not name the file and unknown key %q", c.name, loader, err, c.path)
			}
		}
	}
}

func TestRemovedKeysRejected(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"milestone", "milestone: m1\n", `unknown key "milestone" (milestone is not configurable)`},
		{"assignee", "assignee: \"@me\"\n", `unknown key "assignee" (the assignee scope is always assignee:@me)`},
		{"workflows", "workflows:\n  issues:\n    budget: {}\n", `unknown key "workflows" (workflows.<name>.budget moves to work.<slug>.budget)`},
	}
	for _, c := range cases {
		for loader, err := range loadersOn(t, c.body) {
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s/%s: error %v, want it to contain %q", c.name, loader, err, c.want)
			}
		}
	}
}

func TestMissingConfigUnchanged(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "jig setup") {
		t.Errorf("Load on a missing file = %v", err)
	}
	if _, err := LoadToken(dir); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Errorf("LoadToken on a missing file = %v", err)
	}
	if _, err := LoadGitHub(dir); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Errorf("LoadGitHub on a missing file = %v", err)
	}
}
