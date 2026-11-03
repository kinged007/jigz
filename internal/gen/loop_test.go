package gen

import (
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

// TestSplitOverlap rejects two loops that would claim one label and
// checks the narrowed query shape: exclusions stay, base labels go,
// the loop's own labels are one comma-joined term.
func TestSplitOverlap(t *testing.T) {
	res := config.Resolved{
		QueryFilter: "label:agent:plan,in-progress -label:needs-human",
		Tracker:     config.Tracker{Kind: "github", InProgressState: "in-progress"},
		Rules: []config.ResolvedRule{
			{Name: "plan", Labels: []string{"agent:plan"}, Agents: []string{"a"}},
			{Name: "build", Labels: []string{"agent:build"}, Agents: []string{"b"}},
		},
	}
	loops := Split(res, map[string]config.Agent{})
	if err := CheckOverlap(loops); err != nil {
		t.Errorf("disjoint split rejected: %v", err)
	}
	if q := loops[0].Query; q != "-label:needs-human label:agent:plan" {
		t.Errorf("planner query = %q", q)
	}
	dup := config.Resolved{
		QueryFilter: "label:agent:build -label:needs-human",
		Tracker:     config.Tracker{Kind: "github", InProgressState: "in-progress"},
		Rules: []config.ResolvedRule{
			{Name: "one", Labels: []string{"agent:build"}, Agents: []string{"a"}},
			{Name: "two", Labels: []string{"agent:build"}, Agents: []string{"b"}},
		},
	}
	if err := CheckOverlap(Split(dup, map[string]config.Agent{})); err == nil {
		t.Errorf("shared label accepted")
	} else if !strings.Contains(err.Error(), "share label") {
		t.Errorf("error %q names no shared label", err)
	}
}

// TestForAgent swaps the running agent of a split loop: the same
// candidate list and query, the standby's kind and model.
func TestForAgent(t *testing.T) {
	agents := map[string]config.Agent{
		"main":  {Runtime: "claude-code", Model: "claude-sonnet-5-5"},
		"spare": {Runtime: "pi", Model: "sonnet"},
	}
	res := config.Resolved{
		QueryFilter: "label:in-progress -label:needs-human",
		Tracker:     config.Tracker{Kind: "github", InProgressState: "in-progress"},
		Rules: []config.ResolvedRule{
			{Name: "build", Labels: []string{"agent:build"}, Agents: []string{"main", "spare"}},
		},
	}
	loops := Split(res, agents)
	if len(loops) != 1 {
		t.Fatalf("%d loops, want 1", len(loops))
	}
	got, ok := loops[0].ForAgent(agents, "spare")
	if !ok {
		t.Fatal("spare not accepted")
	}
	if got.Agent != "spare" || got.Runtime != "pi" || got.Model != "sonnet" {
		t.Errorf("loop = %+v", got)
	}
	if len(got.Agents) != 2 || got.Query != loops[0].Query {
		t.Errorf("candidate list or query changed: %+v", got)
	}
	if _, ok := loops[0].ForAgent(agents, "missing"); ok {
		t.Errorf("unknown agent accepted")
	}
}
