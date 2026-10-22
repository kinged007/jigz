package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	forkTrackers = "file, gitea, github, github-pr, gitlab, jira, linear"
	forkAgents   = "agent-client-protocol, claude-code, codex, copilot-cli, mock, opencode, pi"
	upTrackers   = "file, gitea, github, gitlab, jira, linear"
	upAgents     = "agent-client-protocol, claude-code, codex, copilot-cli, mock, opencode"
)

func validateJSON(trackers, agents string) string {
	return fmt.Sprintf(`{"valid":false,"errors":[`+
		`{"severity":"error","check":"tracker_adapter","message":"unknown tracker adapter kind \"__probe__\"; registered: [%s]"},`+
		`{"severity":"error","check":"agent_adapter","message":"unknown agent adapter kind \"__probe__\"; registered: [%s]"}],"warnings":[]}`, trackers, agents)
}

func TestParseRegistered(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"list", `unknown tracker adapter kind "x"; registered: [github, file, jira]`, []string{"file", "github", "jira"}},
		{"single", `registered: [file]`, []string{"file"}},
		{"empty list", `registered: []`, nil},
		{"no list", `unknown tracker adapter kind "x"`, nil},
		{"garbled", `registered: [file, `, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parseRegistered(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseRegistered(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestParseKinds(t *testing.T) {
	tr, ag, err := parseKinds([]byte(validateJSON(forkTrackers, forkAgents)))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(tr, "github-pr") || !contains(ag, "pi") || len(tr) != 7 || len(ag) != 7 {
		t.Fatalf("fork kinds = %v, %v", tr, ag)
	}
	tr, ag, err = parseKinds([]byte(validateJSON(upTrackers, upAgents)))
	if err != nil || contains(tr, "github-pr") || contains(ag, "pi") {
		t.Fatalf("upstream kinds = %v, %v, %v", tr, ag, err)
	}
	for name, raw := range map[string]string{
		"not json":      "sortie: command not found",
		"empty":         "",
		"no diagnostic": `{"valid":true,"errors":[],"warnings":[]}`,
		"agent only":    `{"errors":[{"check":"agent_adapter","message":"registered: [pi]"}]}`,
		"other check":   `{"errors":[{"check":"workflow_load","message":"registered: [a]"}]}`,
	} {
		if _, _, err := parseKinds([]byte(raw)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func fakeEngine(t *testing.T, version, validate string) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	script := fmt.Sprintf(`#!/bin/sh
echo "$1 agent=$SORTIE_AGENT_KIND" >> %q
case "$1" in
-dumpversion) echo %q ;;
validate) cat <<'JSON'
%s
JSON
exit 1 ;;
esac
`, calls, version, validate)
	bin = filepath.Join(dir, "sortie")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func TestProbe(t *testing.T) {
	isolate(t)
	t.Setenv("SORTIE_AGENT_KIND", "codex")
	bin, calls := fakeEngine(t, "1.29.0", validateJSON(upTrackers, upAgents))
	c := Probe(bin)
	if !c.Known() || c.Version != "1.29.0" || c.HasTracker("github-pr") || !c.HasAgent("claude-code") || c.HasAgent("pi") {
		t.Fatalf("Probe = %+v", c)
	}
	if again := Probe(bin); !reflect.DeepEqual(again, c) {
		t.Fatalf("second Probe = %+v, want %+v", again, c)
	}
	log, _ := os.ReadFile(calls)
	if got := strings.Count(string(log), "\n"); got != 2 {
		t.Fatalf("engine ran %d times for two probes, want 2:\n%s", got, log)
	}
	if strings.Contains(string(log), "agent=codex") {
		t.Fatalf("SORTIE_* reached the probe:\n%s", log)
	}
}

func TestProbeCacheFollowsTheFile(t *testing.T) {
	isolate(t)
	bin, _ := fakeEngine(t, "1.28.1", validateJSON(forkTrackers, forkAgents))
	if c := Probe(bin); !c.HasTracker("github-pr") {
		t.Fatalf("first Probe = %+v", c)
	}
	repl, _ := fakeEngine(t, "1.29.0 ", validateJSON(upTrackers, upAgents))
	data, _ := os.ReadFile(repl)
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if c := Probe(bin); c.HasTracker("github-pr") || c.Version != "1.29.0" {
		t.Fatalf("Probe after replacing the binary = %+v", c)
	}
}

func TestProbeErrors(t *testing.T) {
	isolate(t)
	good := validateJSON(upTrackers, upAgents)
	for name, bin := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "none"),
		"garbled": func() string { b, _ := fakeEngine(t, "1.29.0", "not json"); return b }(),
		"empty":   func() string { b, _ := fakeEngine(t, "", good); return b }(),
		"not an engine": func() string {
			p := filepath.Join(t.TempDir(), "x")
			os.WriteFile(p, []byte("#!/bin/sh\nexit 3\n"), 0o755)
			return p
		}(),
	} {
		c := Probe(bin)
		if c.Known() || c.Err == "" || len(c.Trackers) != 0 {
			t.Errorf("%s: Probe = %+v, want an error and no kinds", name, c)
		}
	}
}

func TestProbeRealEngine(t *testing.T) {
	bin := os.Getenv("SORTIE_BIN")
	if bin == "" {
		bin, _ = exec.LookPath(Binary)
	}
	if bin == "" {
		t.Skip("no sortie binary on SORTIE_BIN or PATH")
	}
	isolate(t)
	c := Probe(bin)
	if !c.Known() {
		t.Fatalf("Probe(%s) = %+v", bin, c)
	}
	if !c.HasTracker("github") || !c.HasAgent("claude-code") || c.Version == "" {
		t.Fatalf("Probe(%s) = %+v", bin, c)
	}
}
