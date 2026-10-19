package edit

import (
	"strings"
	"testing"
)

const realKeysMD = `---
tracker:
  query_filter: "label:a"
agent:
  kind: pi
  max_turns: 10
pi:
  model: ""
claude-code:
  model: opus
dispatch:
  rules:
    - name: a
      agent: claude-code
      claude-code:
        model: fast
    - name: b
      stage: stage-b
      next: a
---
body
`

func TestModelIsWrittenToTheBlockOfTheAgentKind(t *testing.T) {
	build := BuildFront(func(d *Doc) []Problem {
		var w Workflow
		Read(d.Root(), &w)
		if w.AgentKind != "pi" || w.Model != "" || *w.MaxTurns != 10 || w.QueryFilter != "label:a" {
			t.Errorf("read = %+v", w)
		}
		w.Model = "strong"
		w.Rules[0].Model = "faster"
		Write(d.Root(), &w)
		return nil
	})
	got, _ := build(realKeysMD)
	want := strings.NewReplacer(`model: ""`, `model: "strong"`, "model: fast", "model: faster").Replace(realKeysMD)
	if got != want {
		t.Errorf("edit changed more than the models:\n%s", Diff("w", realKeysMD, got))
	}
}

func TestRuleModelFollowsTheFileKindWhenTheRuleNamesNone(t *testing.T) {
	text := "---\nagent:\n  kind: pi\ndispatch:\n  rules:\n    - name: a\n---\n"
	got, _ := BuildFront(func(d *Doc) []Problem {
		var w Workflow
		Read(d.Root(), &w)
		w.Rules[0].Model = "m"
		Write(d.Root(), &w)
		return nil
	})(text)
	if !strings.Contains(got, "- name: a\n      pi:\n        model: m") {
		t.Errorf("got:\n%s", got)
	}
}

func TestHiddenFieldsAreNeitherShownNorTouched(t *testing.T) {
	hide := []string{EngineRuleSettings, EngineStages}
	var w Workflow
	Read(rootOf(t, strings.Split(realKeysMD, "---\n")[1]).Root(), &w, hide...)
	if w.Rules[1].Stage != "" || w.Rules[0].Model != "" {
		t.Errorf("hidden fields were read: %+v", w.Rules)
	}
	form := Describe(&w, nil, nil, hide...)
	for _, h := range form.Tables[0].Head {
		if h.Label == "Stage" || h.Label == "Next" || h.Label == "Model" || h.Label == "Effort" {
			t.Errorf("column %q is shown for an engine without it", h.Label)
		}
	}
	got, _ := BuildFront(func(d *Doc) []Problem {
		var w Workflow
		Read(d.Root(), &w, hide...)
		Write(d.Root(), &w, hide...)
		return nil
	})(realKeysMD)
	if got != realKeysMD {
		t.Errorf("a round trip with hidden fields changed the file:\n%s", Diff("w", realKeysMD, got))
	}
	if shown := Describe(&w, nil, nil, EngineStages); len(shown.Tables[0].Head) != 6 {
		t.Errorf("an engine with rule settings but no stages shows %d columns, want 6", len(shown.Tables[0].Head))
	}
}
