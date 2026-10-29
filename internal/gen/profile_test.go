package gen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

var errNoLoops = errors.New("gen: no loops")

func writeConfig(t *testing.T, body string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_TRACKER_TOKEN", "test-token")
	t.Setenv("CLAUDE_SUB_1", "sub-1")
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func baseConfig() string {
	return `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TRACKER_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
`
}

func forkProfile(t *testing.T) Profile {
	t.Helper()
	p, _, err := ProfileFor("", "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestProfile selects the fork profile by default, the upstream one on
// request, and rejects unknown repos and too-old versions.
func TestProfile(t *testing.T) {
	p, name, err := ProfileFor("", "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "fork" || p.EngineVersion() != "kinged007/sortie@v1.28.1" {
		t.Errorf("default = %s %s", name, p.EngineVersion())
	}
	if !p.SupportsTracker("github-pr") || p.SupportsAgent("pi") != true {
		t.Errorf("fork supports github-pr and pi")
	}
	up, name, err := ProfileFor("sortie-ai/sortie", "v1.29.0")
	if err != nil {
		t.Fatal(err)
	}
	if name != "upstream" {
		t.Errorf("name = %q", name)
	}
	if up.SupportsTracker("github-pr") || up.SupportsAgent("pi") {
		t.Errorf("upstream must not support github-pr or pi")
	}
	if _, _, err := ProfileFor("unknown/repo", "v1.28.1"); err == nil {
		t.Errorf("unknown repo accepted")
	}
	if _, _, err := ProfileFor("", "v1.26.0"); err == nil {
		t.Errorf("too-old version accepted")
	}
	u, _, err := ProfileFor("", "v1.29.0")
	if err != nil {
		t.Fatal(err)
	}
	_ = u
}

// TestSplitStages keeps a one-group stage chain in one loop and rejects a
// cross-group next before rendering.
func TestSplitStages(t *testing.T) {
	cfg := writeConfig(t, baseConfig()+`agents:
  planner: { runtime: claude-code, model: claude-sonnet-5-5 }
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  delivery:
    query_filter: "label:in-progress -label:needs-human"
    rules:
      - { name: plan, match: { labels: [agent:plan] }, prompt: prompts/plan.md, agent: planner, next: chain }
      - { name: chain, stage: stage-build, prompt: prompts/build.md, agent: planner }
`)
	p := forkProfile(t)
	res := cfg.Resolve()["delivery"]
	loops := Split(res, cfg.Agents)
	if len(loops) != 1 {
		t.Fatalf("%d loops, want 1", len(loops))
	}
	out := mustRender(t, p, loops[0], cfg.Agents)
	if !strings.Contains(string(out), "- name: plan") || !strings.Contains(string(out), "- name: chain") {
		t.Errorf("one-group chain not rendered as written")
	}

	cfg = writeConfig(t, baseConfig()+`agents:
  planner: { runtime: claude-code, model: claude-sonnet-5-5 }
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  delivery:
    query_filter: "label:in-progress -label:needs-human"
    rules:
      - { name: plan, match: { labels: [agent:plan] }, prompt: prompts/plan.md, agent: planner, next: chain }
      - { name: chain, stage: stage-build, prompt: prompts/build.md, agent: builder }
`)
	loops = Split(cfg.Resolve()["delivery"], cfg.Agents)
	if len(loops) != 2 {
		t.Fatalf("%d loops, want 2", len(loops))
	}
	if _, err := Render(p, loops[0], cfg.Agents, "/usr/local/bin/jig", "prompts/default.md"); err == nil {
		t.Errorf("loop %s: cross-group next accepted", loops[0].Name)
	} else if !strings.Contains(err.Error(), "replacing the label with") {
		t.Errorf("loop %s: error %q names no prompt fix", loops[0].Name, err)
	}
	if _, err := Render(p, loops[1], cfg.Agents, "/usr/local/bin/jig", "prompts/default.md"); err != nil {
		t.Errorf("loop %s: unexpected error %q", loops[1].Name, err)
	}
}

// TestRenderNegatives fails for an unknown tracker kind at render time
// and rejects an unknown agent at load time. The model is passed to the
// runtime unvalidated, so no case rejects it here.
func TestRenderNegatives(t *testing.T) {
	p := forkProfile(t)
	cfg := writeConfig(t, `repo: owner/repo
tracker:
  kind: bogus
  api_key: { from_env: JIG_TRACKER_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
agents:
  b: { runtime: claude-code, model: claude-haiku-5-5 }
work: { issues: { query_filter: "label:x", agent: b, rules: [{ name: r, match: { labels: [x] }, prompt: p.md }] } }
`)
	loops := Split(cfg.Resolve()["issues"], cfg.Agents)
	if len(loops) == 0 {
		t.Fatal(errNoLoops)
	}
	if _, err := Render(p, loops[0], cfg.Agents, "/usr/local/bin/jig", "prompts/default.md"); err == nil {
		t.Errorf("unknown tracker kind accepted")
	}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755)
	body := baseConfig() + `agents:
  b: { runtime: claude-code, model: claude-haiku-5-5 }
work: { issues: { query_filter: "label:x", agent: missing, rules: [{ name: r, match: { labels: [x] }, prompt: p.md }] } }
`
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_TRACKER_TOKEN", "test-token")
	if _, err := config.Load(dir); err == nil {
		t.Errorf("unknown agent accepted")
	}
}
