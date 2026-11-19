package gen

import (
	"strings"
	"testing"
)

// TestPartialsAndMaxSessions renders the optional keys into every loop
// of an entry, and nothing when they are unset.
func TestPartialsAndMaxSessions(t *testing.T) {
	p, _, err := ProfileFor("", "")
	if err != nil {
		t.Fatal(err)
	}
	const head = `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TRACKER_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  a: { runtime: claude-code, model: claude-sonnet-5-5 }
  b: { runtime: claude-code, model: claude-sonnet-5-5 }
work:
  w:
    query_filter: "label:in-progress -label:needs-human"
    agent: a
`
	const rules = `    rules:
      - { name: one, match: { labels: [agent:one] }, prompt: prompts/build.md }
      - { name: two, match: { labels: [agent:two] }, prompt: prompts/build.md, agent: b }
`
	cfg, agents := variantFixture(t, head+"    max_sessions: 3\n    partials: [prompts/partials/*.md]\n"+rules)
	docs := renderLoops(t, p, cfg, agents)
	if len(docs) != 2 {
		t.Fatalf("%d loops, want 2", len(docs))
	}
	for name, text := range docs {
		s := string(text)
		if !strings.Contains(s, "  max_turns: 10\n  max_sessions: 3\n  max_concurrent_agents: 1\n") {
			t.Errorf("%s: max_sessions not rendered after max_turns", name)
		}
		if !strings.Contains(s, "notifications:\n  - kind: tracker_comment\n    events: [auto_merge.merged, budget.held]\n\n") {
			t.Errorf("%s: tracker_comment notification not rendered", name)
		}
		if !strings.Contains(s, "dispatch:\n  partials:\n    - \"prompts/partials/*.md\"\n  rules:\n") {
			t.Errorf("%s: partials not rendered under dispatch", name)
		}
	}
	cfg, agents = variantFixture(t, head+rules)
	for name, text := range renderLoops(t, p, cfg, agents) {
		if strings.Contains(string(text), "max_sessions") || strings.Contains(string(text), "notifications") || strings.Contains(string(text), "partials") {
			t.Errorf("%s: unset keys rendered", name)
		}
	}
}
