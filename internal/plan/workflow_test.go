package plan

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func fixture(t *testing.T, name string) Workflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "sortie-central", "testdata", "WORKFLOW."+name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWorkflow(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestParseShippedWorkflows(t *testing.T) {
	cases := []struct {
		name, kind, stage, template string
		exclude                     []string
	}{
		{"plan", "github", "agent:plan", "./prompts/plan.md", []string{"agent:build", "needs-human"}},
		{"build", "github", "agent:build", "", []string{"agent:plan", "needs-human"}},
		{"review", "github-pr", "agent:review", "./prompts/review.md", []string{"agent:build", "agent:merge", "needs-human"}},
		{"review-fix", "github-pr", "agent:build", "", []string{"agent:review", "agent:merge", "needs-human"}},
		{"merge", "github-pr", "agent:merge", "", []string{"agent:build", "agent:review", "needs-human"}},
	}
	for _, c := range cases {
		w := fixture(t, c.name)
		if w.TrackerKind != c.kind || w.StageLabel() != c.stage || w.HandoffState != "agent:done" || w.PollingMS != 60000 || w.AgentKind != "pi" {
			t.Errorf("%s: kind %q stage %q handoff %q polling %d agent %q", c.name, w.TrackerKind, w.StageLabel(), w.HandoffState, w.PollingMS, w.AgentKind)
		}
		if c.template != "" && w.Template != c.template {
			t.Errorf("%s: template %q", c.name, w.Template)
		}
		if _, ex := w.FilterLabels(); !slices.Equal(ex, c.exclude) {
			t.Errorf("%s: excluded %v, want %v", c.name, ex, c.exclude)
		}
		if len(w.Rules) != 0 {
			t.Errorf("%s: shipped workflow has rules %v", c.name, w.Rules)
		}
	}
}

func TestParseRulesAndModel(t *testing.T) {
	w, err := ParseWorkflow(`---
tracker:
  kind: github
  query_filter: "label:agent:a,in-progress label:b -label:x"
agent:
  kind: claude-code
claude-code:
  model: opus
dispatch:
  default:
    template: ./d.md
  rules:
    - name: plan
      match: {labels: [agent:plan, plan]}
      agent: pi
      template: ./plan.md
      next: build
    - name: build
      stage: stage-build
---
body
`)
	if err != nil {
		t.Fatal(err)
	}
	if w.Model != "opus" || w.Template != "./d.md" {
		t.Errorf("model %q template %q", w.Model, w.Template)
	}
	inc, ex := w.FilterLabels()
	if !slices.Equal(inc, []string{"agent:a", "in-progress", "b"}) || !slices.Equal(ex, []string{"x"}) {
		t.Errorf("include %v exclude %v", inc, ex)
	}
	if len(w.Rules) != 2 {
		t.Fatalf("rules %+v", w.Rules)
	}
	if r := w.Rules[0]; r.Name != "plan" || !slices.Equal(r.Labels, []string{"agent:plan", "plan"}) || r.Agent != "pi" || r.Template != "./plan.md" || r.Next != "build" {
		t.Errorf("rule 0 %+v", r)
	}
	if r := w.Rules[1]; r.Name != "build" || r.Stage != "stage-build" || r.Next != "" {
		t.Errorf("rule 1 %+v", r)
	}
}

func TestParseWorkflowWithoutFrontMatter(t *testing.T) {
	if _, err := ParseWorkflow("no fence"); err == nil {
		t.Error("expected an error")
	}
	if got := (Workflow{}).StageLabel(); got != "" {
		t.Errorf("stage %q", got)
	}
}

func TestRepoLabelOrder(t *testing.T) {
	got := RepoLabelOrder("repo: o/r\nlabels:\n- {name: \"agent:plan\", color: x}\n- {name: \"agent:build\"}\n")
	if !slices.Equal(got, []string{"agent:plan", "agent:build"}) {
		t.Errorf("got %v", got)
	}
	if RepoLabelOrder(": bad: [") != nil {
		t.Error("expected nil for unreadable config")
	}
}

func TestParseWorkflowKnobs(t *testing.T) {
	w := fixture(t, "build")
	want := map[string]string{"kind": "pi", "command": "pi", "max_turns": "10", "max_concurrent_agents": "1",
		"turn_timeout_ms": "3600000", "read_timeout_ms": "120000", "stall_timeout_ms": "300000",
		"stop_grace_ms": "5000", "max_retry_backoff_ms": "120000", "polling_interval_ms": "60000"}
	if !maps.Equal(w.Knobs, want) {
		t.Errorf("knobs = %v, want %v", w.Knobs, want)
	}
	w, err := ParseWorkflow("---\nagent:\n  kind: claude-code\nclaude-code:\n  model: opus\n---\n")
	if err != nil || w.Knobs["model"] != "opus" || w.Knobs["kind"] != "claude-code" {
		t.Errorf("model knob = %v, %v", w.Knobs, err)
	}
}
