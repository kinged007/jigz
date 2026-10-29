package gen

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

func TestShippedDiff(t *testing.T) {
	cfg, agents := shippedFixture(t)
	p, _, err := ProfileFor("", "")
	if err != nil {
		t.Fatal(err)
	}
	res := cfg.Resolve()
	pairs := map[string]string{
		"issues": "../../cmd/jig/workflows/WORKFLOW.issues.md",
		"prs":    "../../cmd/jig/workflows/WORKFLOW.prs.md",
	}
	for slug, shipped := range pairs {
		loops := Split(res[slug], agents)
		if len(loops) != 1 {
			t.Fatalf("%s: %d loops, want 1", slug, len(loops))
		}
		got, err := Render(p, loops[0], agents, "/usr/local/bin/jig", "prompts/default.md")
		if err != nil {
			t.Fatalf("%s: render: %v", slug, err)
		}
		raw, err := os.ReadFile(shipped)
		if err != nil {
			t.Fatal(err)
		}
		line := func(text []byte, key string) string {
			for _, l := range strings.Split(string(text), "\n") {
				if strings.HasPrefix(strings.TrimSpace(l), key) {
					return strings.TrimSpace(l)
				}
			}
			return ""
		}
		if genMarker := line(got, "# jig:generated"); genMarker == "" {
			t.Errorf("%s: generated document has no marker line", slug)
		}
		if ship := line(raw, "# jig:generated"); ship != "" {
			t.Errorf("%s: shipped document unexpectedly has a marker: %q", slug, ship)
		}
		gotLines := normalize(structure(got))
		wantLines := normalize(structure(raw))
		// Filter intentional differences before comparing so extra
		// lines cannot shift the alignment: the agent-exec command
		// (shipped says claude), the second runtime block (the
		// generator emits only the kind's block) and the dispatch
		// safety net (shipped predates dispatch.default). The marker
		// is already stripped by structure and db_path matches.
		gotLines = filterShippedNoise(gotLines)
		wantLines = filterShippedNoise(wantLines)
		var diffs []string
		for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
			var g, w string
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g == w {
				continue
			}
			diffs = append(diffs, "line "+strconv.Itoa(i+1)+": got "+strconv.Quote(g)+" want "+strconv.Quote(w))
		}
		for _, d := range diffs {
			t.Errorf("%s: unexpected diff %s", slug, d)
		}
	}
}

// shippedFixture mirrors the shipped WORKFLOW.issues.md and
// WORKFLOW.prs.md rule sets (5 + 3 rules) with tracker references and
// prompts as the shipped files carry them. defaultFixture stays section
// 3.5 Example 1 verbatim; this one exists only for the shipped-diff test.
func shippedFixture(t *testing.T) (*config.Config, map[string]config.Agent) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755)
	body := `repo: owner/repo
engine: { repo: kinged007/sortie, version: v1.28.1 }
operating_hours: {}
tracker:
  kind: github
  api_key: { from_env: SORTIE_TRACKER_API_KEY }
  project: { from_env: SORTIE_TRACKER_PROJECT }
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  issues:
    query_filter: "label:agent:plan,agent:plan-build,agent:build,agent:docs,in-progress -label:needs-human"
    tracker: { active_states: [agent:plan, agent:plan-build, agent:build, agent:docs, in-progress] }
    agent: builder
    rules:
      - { name: plan, match: { labels: [agent:plan] }, prompt: ./prompts/plan.md }
      - { name: plan-build, match: { labels: [agent:plan-build] }, prompt: ./prompts/plan.md, next: build-chain }
      - { name: build, match: { labels: [agent:build] }, prompt: ./prompts/build.md }
      - { name: docs, match: { labels: [agent:docs] }, prompt: ./prompts/docs.md }
      - { name: build-chain, stage: stage-build, prompt: ./prompts/build.md }
  prs:
    tracker: { kind: github-pr, active_states: [agent:review, agent:build, agent:merge, in-progress] }
    query_filter: "label:agent:review,agent:build,agent:merge,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: review, match: { labels: [agent:review] }, prompt: ./prompts/review.md }
      - { name: review-fix, match: { labels: [agent:build] }, prompt: ./prompts/review-fix.md }
      - { name: merge, match: { labels: [agent:merge] }, prompt: ./prompts/merge.md }
`
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SORTIE_TRACKER_API_KEY", "test-key")
	t.Setenv("SORTIE_TRACKER_PROJECT", "owner/repo")
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg, cfg.Agents
}

// structure keeps the front matter without the marker line; the trailing
// routing body below the closing --- is out of scope for generation.
func structure(text []byte) []string {
	var out []string
	closes := 0
	for _, l := range strings.Split(string(text), "\n") {
		if strings.TrimSpace(l) == "---" {
			closes++
			if closes == 2 {
				break
			}
			out = append(out, l)
			continue
		}
		if closes == 1 && !strings.HasPrefix(strings.TrimSpace(l), "# jig:generated") {
			out = append(out, l)
		}
	}
	return out
}

// normalize drops comment and blank lines; the shipped comments are
// per-file prose the generator does not reproduce.
func normalize(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// filterShippedNoise drops the intentional differences until phase 3b
// rewires the CLI: the agent command line, the standby pi block, and
// the dispatch safety net the shipped files predate.
func filterShippedNoise(lines []string) []string {
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.Contains(l, "command:"),
			t == "pi:",
			strings.Contains(l, `model: "sonnet"`),
			t == "default:",
			strings.Contains(l, "prompts/default.md"):
			continue
		case t == "db_path: jigz-issues.db" || t == "db_path: jigz-prs.db":
			continue
		}
		out = append(out, l)
	}
	return out
}
