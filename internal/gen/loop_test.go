package gen

import (
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

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
