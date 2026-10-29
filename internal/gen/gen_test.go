package gen

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

// flagUpdate rewrites the golden files instead of comparing against
// them: go test ./internal/gen/ -update. Review the diff before
// committing; a golden churns only when the generator changes on
// purpose.
var flagUpdate = flag.Bool("update", false, "rewrite golden files")

func defaultFixture(t *testing.T) (*config.Config, map[string]config.Agent) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755)
	body := `repo: owner/repo
engine: { repo: kinged007/sortie, version: v1.28.1 }
operating_hours: {}
tracker:
  kind: github
  api_key: { from_env: JIG_TRACKER_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  reviewer:
    { runtime: claude-code, model: claude-sonnet-5-5,
      schedule: { timezone: Europe/Madrid, windows: [ { days: [mon, tue, wed, thu, fri], start: "09:00", end: "18:00" } ] } }
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
  prs:
    tracker: { kind: github-pr }
    query_filter: "label:agent:review -label:needs-human"
    agent: reviewer
    rules:
      - { name: review, match: { labels: [agent:review] }, prompt: prompts/review.md }
`
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_TRACKER_TOKEN", "test-token")
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg, cfg.Agents
}

// renderLoops renders every loop of every entry of cfg.
func renderLoops(t *testing.T, p Profile, cfg *config.Config, agents map[string]config.Agent) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for slug, res := range cfg.Resolve() {
		for _, l := range Split(res, agents) {
			text, err := Render(p, l, agents, "/usr/local/bin/jig", "prompts/default.md")
			if err != nil {
				t.Fatalf("render %s: %v", l.Name, err)
			}
			out[l.Name+"/"+slug] = text
		}
	}
	return out
}

func goldenPath(profile, name string) string {
	return filepath.Join("testdata", "golden", profile, name+".md")
}

func checkGolden(t *testing.T, profile, name string, text []byte) {
	t.Helper()
	path := goldenPath(profile, name)
	if *flagUpdate {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, text, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run with -update to write it)", path, err)
	}
	if string(want) != string(text) {
		t.Errorf("golden %s differs; run with -update and review the diff", path)
	}
}

// TestRenderDefault renders the two default loops and checks determinism.
func TestRenderDefault(t *testing.T) {
	cfg, agents := defaultFixture(t)
	p, _, err := ProfileFor("", "")
	if err != nil {
		t.Fatal(err)
	}
	once := renderLoops(t, p, cfg, agents)
	twice := renderLoops(t, p, cfg, agents)
	for name, a := range once {
		if string(a) != string(twice[name]) {
			t.Errorf("loop %s is not deterministic", name)
		}
		checkGolden(t, "fork-1.28.1", "default-"+strings.Replace(name, "/", "-", 1), a)
		if err := os.WriteFile("/tmp/gen-"+strings.Split(name, "/")[0]+".md", a, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoopSplitting(t *testing.T) {
	_, agents := defaultFixture(t)
	p, _, err := ProfileFor("", "")
	if err != nil {
		t.Fatal(err)
	}
	body := `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TRACKER_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
agents:
  planner: { runtime: claude-code, model: claude-sonnet-5-5 }
  builder-a: { runtime: claude-code, model: claude-haiku-5-5 }
  builder-b: { runtime: pi, model: sonnet }
work:
  delivery:
    query_filter: "label:in-progress -label:needs-human"
    hooks: { timeout_ms: 300000 }
    rules:
      - { name: plan, match: { labels: [agent:plan] }, prompt: prompts/plan.md, agent: planner }
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md, agent: [builder-a, builder-b] }
`
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	splitCfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	merged := map[string]config.Agent{}
	for k, v := range agents {
		merged[k] = v
	}
	for k, v := range splitCfg.Agents {
		merged[k] = v
	}
	res := splitCfg.Resolve()["delivery"]
	loops := Split(res, merged)
	if len(loops) != 2 {
		t.Fatalf("delivery: %d loops, want 2", len(loops))
	}
	if loops[0].Name != "delivery-planner" || loops[1].Name != "delivery-builder-a" {
		t.Fatalf("loop names = %q, %q", loops[0].Name, loops[1].Name)
	}
	for _, l := range loops {
		out, err := Render(p, l, merged, "/usr/local/bin/jig", "prompts/default.md")
		if err != nil {
			t.Fatalf("render %s: %v", l.Name, err)
		}
		checkGolden(t, "fork-1.28.1", "split-"+l.Name, out)
	}
	// The two queries must not overlap: each carries only its own labels.
	if strings.Contains(string(mustRender(t, p, loops[0], merged)), "agent:build") {
		t.Errorf("planner loop query carries the build label")
	}
	if strings.Contains(string(mustRender(t, p, loops[1], merged)), "agent:plan") {
		t.Errorf("builder loop query carries the plan label")
	}
}

func mustRender(t *testing.T, p Profile, l Loop, agents map[string]config.Agent) []byte {
	t.Helper()
	out, err := Render(p, l, agents, "/usr/local/bin/jig", "prompts/default.md")
	if err != nil {
		t.Fatal(err)
	}
	return out
}
