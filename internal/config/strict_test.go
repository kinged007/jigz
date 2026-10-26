package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const allKeysYAML = `repo: o/r
token: t
milestone: m1
assignee: "@me"
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
workflows:
  issues:
    budget:
      period: weekly
      timezone: UTC
      max_tokens: 1000
      warn_percent: 80
      on_exceed: pause
      poll_interval: 1m
      max_usd: 5
github:
  enrich: true
  interval: 10m
  api_url: https://api.github.com
  details: false
  questions_interval: 2m
  questions_window: 168h
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
	cases := []struct{ name, body, key string }{
		{"top-level", "repo: o/r\nasignee: x\n", "asignee"},
		{"cleanup", "cleanup:\n  enabled: true\n  intervl: 6h\n", "intervl"},
		{"sleep", "sleep:\n  enabled: true\n  idle_timout: 1m\n", "idle_timout"},
		{"workflow", "workflows:\n  issues:\n    budgt: {}\n", "budgt"},
		{"budget", "workflows:\n  issues:\n    budget:\n      max_token: 1\n", "max_token"},
		{"github", "github:\n  enrich: true\n  enrch: true\n", "enrch"},
		{"label", "labels:\n- {name: a, colour: b}\n", "colour"},
	}
	for _, c := range cases {
		for loader, err := range loadersOn(t, c.body) {
			if err == nil {
				t.Errorf("%s/%s: unknown key accepted", c.name, loader)
				continue
			}
			if !strings.Contains(err.Error(), ".jigz/config.yaml") || !strings.Contains(err.Error(), c.key) {
				t.Errorf("%s/%s: error %q does not name the file and key %q", c.name, loader, err, c.key)
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
