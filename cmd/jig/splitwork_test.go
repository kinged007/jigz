package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSplitConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	body := `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  planner: { runtime: claude-code, model: claude-sonnet-5-5 }
  builder: { runtime: claude-code, model: claude-haiku-5-5 }
work:
  delivery:
    query_filter: "label:in-progress -label:needs-human"
    rules:
      - { name: plan, match: { labels: [agent:plan] }, prompt: prompts/plan.md, agent: planner }
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md, agent: builder }
  legacy:
    managed: false
    query_filter: "label:old"
    agent: builder
    rules:
      - { name: r, match: { labels: [old] }, prompt: prompts/plan.md }
`
	if err := os.MkdirAll(filepath.Join(root, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_TEST_TOKEN", "test-token")
	return root
}

func testSupervisor(t *testing.T, root string) *supervisor {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := newSupervisor(root, "/bin/jig", nil)
	return s
}

// TestRefreshWorkSplits keys the gate inputs by loop, not by entry.
func TestRefreshWorkSplits(t *testing.T) {
	root := writeSplitConfig(t)
	s := testSupervisor(t, root)
	s.refreshWork("test")
	for _, name := range []string{"delivery-planner", "delivery-builder", "legacy"} {
		if _, ok := s.work[name]; !ok {
			t.Errorf("no work for %s (have %v)", name, workKeys(s.work))
		}
	}
	if got := s.work["delivery-planner"].Agents; len(got) != 1 || got[0] != "planner" {
		t.Errorf("planner loop agents = %v", got)
	}
	if q := s.queries["delivery-planner"]; strings.Contains(q, "agent:build") || !strings.Contains(q, "agent:plan") {
		t.Errorf("planner query = %q", q)
	}
	if q := s.queries["delivery-builder"]; strings.Contains(q, "agent:plan") || !strings.Contains(q, "agent:build") {
		t.Errorf("builder query = %q", q)
	}
}

// TestChildEnvNarrowedQuery launches split loops with their own filter.
func TestChildEnvNarrowedQuery(t *testing.T) {
	root := writeSplitConfig(t)
	s := testSupervisor(t, root)
	s.refreshWork("test")
	_, env, _, err := s.childEnv("delivery-planner")
	if err != nil {
		t.Fatal(err)
	}
	got := lastEnv(env, "SORTIE_TRACKER_QUERY_FILTER")
	if !strings.Contains(got, "agent:plan") || strings.Contains(got, "agent:build") {
		t.Errorf("planner filter = %q", got)
	}
	if !strings.HasSuffix(got, " assignee:@me") {
		t.Errorf("filter lacks the assignee scope: %q", got)
	}
}

// TestRecycleDrain marks a running loop for drain+restart and holds a
// stopped one for the gate.
func TestRecycleDrain(t *testing.T) {
	root := writeSplitConfig(t)
	s := testSupervisor(t, root)
	s.refreshWork("test")
	s.defs["delivery-planner"] = loopDef{"delivery-planner", "WORKFLOW.delivery-planner.md"}
	s.defs["delivery-builder"] = loopDef{"delivery-builder", "WORKFLOW.delivery-builder.md"}
	s.procs["delivery-planner"] = &loopProc{}
	s.recycleDrain("delivery-planner")
	p := s.procs["delivery-planner"]
	if !p.stopping || !p.drain || !p.recycle {
		t.Errorf("running loop not marked: %+v", p)
	}
	s.recycleDrain("delivery-builder")
	if !s.gateHeld["delivery-builder"] {
		t.Errorf("stopped loop not held")
	}
}

// TestMergeDiscoveredDefs picks up documents written since boot.
func TestMergeDiscoveredDefs(t *testing.T) {
	root := writeSplitConfig(t)
	s := testSupervisor(t, root)
	dir := filepath.Join(root, ".jigz", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "WORKFLOW.triage.md"), []byte("---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.mergeDiscoveredDefs()
	if _, ok := s.defs["triage"]; !ok {
		t.Errorf("new document not merged: %v", s.defs)
	}
}

func workKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
