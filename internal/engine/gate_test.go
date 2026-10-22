package engine

import (
	"strings"
	"testing"
)

func caps(version, trackers, agents string) Capabilities {
	return Capabilities{Bin: "/bin/sortie", Version: version, Trackers: strings.Split(trackers, ", "), Agents: strings.Split(agents, ", ")}
}

var (
	spec     = Spec{Repo: "sortie-ai/sortie", Tag: "v1.29.0"}
	fork     = NewGate(caps("1.28.1", forkTrackers, forkAgents), Spec{Repo: DefaultRepo, Tag: DefaultTag})
	upstream = NewGate(caps("1.29.0", upTrackers, upAgents), spec)
	old      = NewGate(caps("1.26.0", forkTrackers, forkAgents), spec)
	unknown  = NewGate(Capabilities{Bin: "/bin/sortie", Err: "boom"}, spec)
)

func TestGateLoopsOff(t *testing.T) {
	for _, c := range []struct {
		name string
		g    Gate
		off  bool
	}{
		{"fork", fork, false},
		{"upstream", upstream, false},
		{"minimum", NewGate(caps("1.27.0", forkTrackers, forkAgents), spec), false},
		{"older", old, true},
		{"build suffix", NewGate(caps("1.24.0+pi.github-pr.1", forkTrackers, forkAgents), spec), true},
		{"unreadable version", NewGate(caps("dev", forkTrackers, forkAgents), spec), false},
		{"unknown", unknown, false},
		{"zero", Gate{}, false},
	} {
		if got := c.g.LoopsOff() != ""; got != c.off {
			t.Errorf("%s: LoopsOff = %q, off = %v", c.name, c.g.LoopsOff(), c.off)
		}
	}
	if got := old.LoopsOff(); !strings.Contains(got, "engine sortie-ai/sortie v1.26.0") || !strings.Contains(got, "1.27.0") {
		t.Errorf("LoopsOff text = %q", got)
	}
}

func TestGateTrackerOff(t *testing.T) {
	if got := fork.TrackerOff("github-pr"); got != "" {
		t.Errorf("fork: %q", got)
	}
	want := "PR workflow is off: engine sortie-ai/sortie v1.29.0 has no github-pr tracker."
	if got := upstream.TrackerOff("github-pr"); got != want {
		t.Errorf("upstream: %q, want %q", got, want)
	}
	if got := upstream.TrackerOff("github"); got != "" {
		t.Errorf("github: %q", got)
	}
	if got := upstream.TrackerOff("bitbucket"); !strings.Contains(got, `"bitbucket"`) {
		t.Errorf("unknown kind: %q", got)
	}
	for _, g := range []Gate{unknown, {}} {
		if got := g.TrackerOff("github-pr"); got != "" {
			t.Errorf("unknown capabilities disabled the PR workflow: %q", got)
		}
	}
}

func TestGateAgent(t *testing.T) {
	for _, c := range []struct {
		name      string
		g         Gate
		kind      string
		explicit  bool
		use       string
		note, bad bool
	}{
		{"fork keeps pi", fork, "pi", false, "pi", false, false},
		{"upstream falls back", upstream, "pi", false, "claude-code", true, false},
		{"explicit pi is an error", upstream, "pi", true, "pi", false, true},
		{"explicit codex available", upstream, "codex", true, "codex", false, false},
		{"explicit unknown kind", upstream, "gemini", true, "gemini", false, true},
		{"file kind unknown", upstream, "gemini", false, "gemini", false, true},
		{"no fallback agent", NewGate(Capabilities{Bin: "x", Version: "1.29.0", Trackers: []string{"github"}, Agents: []string{"mock"}}, spec), "pi", false, "pi", false, true},
		{"unknown capabilities", unknown, "pi", false, "pi", false, false},
		{"no kind", upstream, "", false, "", false, false},
	} {
		use, note, bad := c.g.Agent(c.kind, c.explicit)
		if use != c.use || (note != "") != c.note || (bad != "") != c.bad {
			t.Errorf("%s: Agent = %q, %q, %q", c.name, use, note, bad)
		}
	}
}

func TestGateRuntimeOff(t *testing.T) {
	if got := upstream.RuntimeOff("pi"); !strings.Contains(got, "runtime pi") {
		t.Errorf("upstream pi: %q", got)
	}
	for _, c := range []struct {
		g       Gate
		runtime string
	}{{upstream, "claude-code"}, {fork, "pi"}, {unknown, "pi"}} {
		if got := c.g.RuntimeOff(c.runtime); got != "" {
			t.Errorf("%s: %q", c.runtime, got)
		}
	}
}

func TestGateUnknown(t *testing.T) {
	if unknown.Unknown() != "boom" || upstream.Unknown() != "" || (Gate{}).Unknown() != "" {
		t.Fatalf("Unknown = %q, %q, %q", unknown.Unknown(), upstream.Unknown(), (Gate{}).Unknown())
	}
}

func TestLabelWithSortieBin(t *testing.T) {
	t.Setenv("SORTIE_BIN", "/x/sortie")
	if got := NewGate(caps("1.29.0", upTrackers, upAgents), spec).Label; got != "engine v1.29.0" {
		t.Fatalf("Label = %q", got)
	}
}
