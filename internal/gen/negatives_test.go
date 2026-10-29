package gen

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

func negativeBase() config.Resolved {
	return config.Resolved{
		QueryFilter: "label:x -label:needs-human",
		Tracker:     config.Tracker{Kind: "github", InProgressState: "in-progress", HandoffState: "agent:done"},
	}
}

// TestNegativesExactText pins the section 7.2 failure texts owned by
// the generator: a shared label across split loops, a next that leaves
// the loop, and a tracker kind the profile does not register. Unknown
// keys, plain secrets and undefined agents fail in config.Load with
// their own exact texts (internal/config).
func TestNegativesExactText(t *testing.T) {
	agents := map[string]config.Agent{"a": {Runtime: "claude-code", Model: "m"}, "b": {Runtime: "claude-code", Model: "m"}}
	p, _, err := ProfileFor("", "")
	if err != nil {
		t.Fatal(err)
	}

	shared := negativeBase()
	shared.Rules = []config.ResolvedRule{
		{Name: "one", Labels: []string{"x"}, Agents: []string{"a"}},
		{Name: "two", Labels: []string{"x"}, Agents: []string{"b"}},
	}
	loops := Split(shared, agents)
	err = CheckOverlap(loops)
	want := fmt.Sprintf("gen: loops %s and %s share label %q; give the rules different labels", loops[0].Name, loops[1].Name, "x")
	if err == nil || err.Error() != want {
		t.Errorf("shared label error = %v, want %q", err, want)
	}

	chain := negativeBase()
	chain.Rules = []config.ResolvedRule{
		{Name: "plan", Labels: []string{"p"}, Agents: []string{"a"}, Next: "chain"},
		{Name: "chain", Stage: "s", Agents: []string{"b"}},
	}
	loops = Split(chain, agents)
	if len(loops) != 2 {
		t.Fatalf("%d loops, want 2", len(loops))
	}
	_, err = Render(p, loops[0], agents, "/usr/local/bin/jig", "prompts/default.md")
	want = fmt.Sprintf("gen: rule plan of loop %s chains to %q, which lives in another loop; hand the item over by replacing the label with the next trigger instead", loops[0].Name, "chain")
	if err == nil || err.Error() != want {
		t.Errorf("cross-group next error = %v, want %q", err, want)
	}

	up, _, err := ProfileFor("sortie-ai/sortie", "v1.29.0")
	if err != nil {
		t.Fatal(err)
	}
	pr := negativeBase()
	pr.Tracker.Kind = "github-pr"
	pr.Rules = []config.ResolvedRule{{Name: "review", Labels: []string{"x"}, Agents: []string{"a"}}}
	loops = Split(pr, agents)
	_, err = Render(up, loops[0], agents, "/usr/local/bin/jig", "prompts/default.md")
	want = fmt.Sprintf("gen: loop %s uses tracker kind %q, which %s does not support", loops[0].Name, "github-pr", up.EngineVersion())
	if err == nil || err.Error() != want {
		t.Errorf("unsupported tracker error = %v, want %q", err, want)
	}
	if !strings.Contains(want, "sortie-ai/sortie@v1.29.0") {
		t.Errorf("error names no profile: %q", want)
	}
}
