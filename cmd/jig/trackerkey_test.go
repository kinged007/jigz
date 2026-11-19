package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

const trackerKeyConfig = `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: TK_DEFAULT }
  project: owner/repo
agents:
  main: { runtime: claude-code, model: claude-sonnet-5-5 }
work:
  issues:
    query_filter: "label:agent:build -label:needs-human"
    agent: main
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
  prs:
    query_filter: "label:agent:review -label:needs-human"
    agent: main
    tracker:
      api_key: { from_env: TK_PRS }
    rules:
      - { name: review, match: { labels: [agent:review] }, prompt: prompts/review.md }
`

func trackerKeyRoot(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), body)
	for _, k := range []string{"JIG_TOKEN", "GITHUB_TOKEN", "GH_TOKEN", "TK_DEFAULT", "TK_PRS"} {
		t.Setenv(k, "")
	}
	return root
}

func trackerKeySup(t *testing.T, root string) *supervisor {
	t.Helper()
	s := testSupervisor(t, root)
	s.refreshWork("test")
	return s
}

func envFileValue(t *testing.T, path, key string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// AC-1, AC-4
func TestEachLoopGetsItsOwnTrackerCredential(t *testing.T) {
	root := trackerKeyRoot(t, trackerKeyConfig)
	t.Setenv("JIG_TOKEN", "chain-token")
	t.Setenv("TK_DEFAULT", "default-token")
	t.Setenv("TK_PRS", "prs-token")
	s := trackerKeySup(t, root)
	want := map[string]string{"issues": "default-token", "prs": "prs-token"}
	files := map[string]string{}
	for name, tok := range want {
		envFile, env, _, err := s.childEnv(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := lastEnv(env, "SORTIE_TRACKER_API_KEY"); got != tok {
			t.Errorf("%s: process env key = %q, want %q", name, got, tok)
		}
		if got := envFileValue(t, envFile, "SORTIE_TRACKER_API_KEY"); got != tok {
			t.Errorf("%s: env file key = %q, want %q", name, got, tok)
		}
		files[name] = envFile
	}
	if files["issues"] == files["prs"] {
		t.Error("loops with different credentials share one env file")
	}
	if got := envFileValue(t, filepath.Join(root, ".jigz", ".env.loop"), "SORTIE_TRACKER_API_KEY"); got != "default-token" {
		t.Errorf("shared env file key = %q, want the default credential", got)
	}
}

// AC-3, AC-4, AC-7
func TestLoopFailsWhenItsTrackerVariableIsUnset(t *testing.T) {
	root := trackerKeyRoot(t, trackerKeyConfig)
	t.Setenv("JIG_TOKEN", "chain-secret")
	t.Setenv("TK_DEFAULT", "default-token")
	s := trackerKeySup(t, root)
	if _, _, _, err := s.childEnv("issues"); err != nil {
		t.Fatalf("issues: %v", err)
	}
	_, _, _, err := s.childEnv("prs")
	if err == nil {
		t.Fatal("prs: want an error for an unset TK_PRS")
	}
	for _, want := range []string{"TK_PRS", "tracker.api_key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "chain-secret") || strings.Contains(err.Error(), "default-token") {
		t.Errorf("error contains a credential: %v", err)
	}
}

// AC-3: validate fails; start runs the same check before any loop starts.
func TestValidateFailsOnUnsetTrackerVariable(t *testing.T) {
	root := trackerKeyRoot(t, trackerKeyConfig)
	t.Setenv("TK_DEFAULT", "default-token")
	checks, _, _ := collectValidateChecks(root)
	for _, c := range checks {
		if c.Name == "tracker" {
			if c.OK || !strings.Contains(c.Error, "TK_PRS") {
				t.Errorf("tracker check = %+v, want a failure naming TK_PRS", c)
			}
			return
		}
	}
	t.Fatalf("no tracker check in %+v", checks)
}

// AC-8
func TestSetupSeedsNoActiveTrackerAPIKey(t *testing.T) {
	root := trackerKeyRoot(t, configTemplateBody("owner/repo"))
	t.Setenv("JIG_TRACKER_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "gh-token")
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckToken(); err != nil {
		t.Errorf("seeded config with JIG_TRACKER_TOKEN unset: %v", err)
	}
	tok, err := cfg.TrackerToken(cfg.TrackerDefaults)
	if err != nil || tok != "gh-token" {
		t.Errorf("token = %q, %v, want the chain value", tok, err)
	}
	if !strings.Contains(configTemplateBody("owner/repo"), "#api_key: { from_env: JIG_TRACKER_TOKEN }") {
		t.Error("the commented api_key line is missing")
	}
}
