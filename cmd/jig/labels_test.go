package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/kinged007/jigz/internal/config"
)

// allowedLabels is every label the shipped workflows and prompts may name.
// A custom loop in some target repo may use others; these files ship, so
// their vocabulary is fixed.
var allowedLabels = map[string]bool{
	"agent:plan": true, "agent:build": true, "agent:review": true,
	"agent:merge": true, "agent:plan-build": true, "agent:docs": true,
	"in-progress": true, "agent:done": true,
	"needs-human": true,
	// Parked quick track only: nothing seeds or watches agent:quick until the
	// track is switched back on, but its prompt still has to stay name-clean.
	"agent:quick": true,
}

// virtualStates are derivation fallbacks, never labels: no item carries them.
// No virtual fallback remains under 1.27; kept as an empty set so the
// active_states membership check below stays uniform.
var virtualStates = map[string]bool{}

// labelArg matches the value of a --add-label / --remove-label argument in a
// prompt, including the multi-line form (`\` before the next line).
var labelArg = regexp.MustCompile(`--(?:add|remove)-label\s+"([^"]*)"`)

// queryLabel matches label:NAME and -label:NAME clauses in a query filter.
var queryLabel = regexp.MustCompile(`-?label:([A-Za-z0-9:_-]+)`)

// frontMatter returns the leading "---" block of a workflow file.
func frontMatter(t *testing.T, name string) string {
	t.Helper()
	raw, err := embeddedWorkflows.ReadFile("workflows/" + name)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatalf("%s: no front matter", name)
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n")
		}
	}
	t.Fatalf("%s: unterminated front matter", name)
	return ""
}

type trackerFront struct {
	Tracker struct {
		Kind            string   `yaml:"kind"`
		QueryFilter     string   `yaml:"query_filter"`
		ActiveStates    []string `yaml:"active_states"`
		InProgressState string   `yaml:"in_progress_state"`
		HandoffState    string   `yaml:"handoff_state"`
		TerminalStates  []string `yaml:"terminal_states"`
	} `yaml:"tracker"`
}

// TestWorkflowLabels pins the state contract every shipped loop carries: a
// claim label that is also an active state, a fallback that never is, and a
// handoff to agent:done that neither closes the item nor re-dispatches it.
func TestWorkflowLabels(t *testing.T) {
	entries, err := fs.ReadDir(embeddedWorkflows, "workflows")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "WORKFLOW.") {
			continue
		}
		seen++
		name := e.Name()
		var fm trackerFront
		if err := yaml.Unmarshal([]byte(frontMatter(t, name)), &fm); err != nil {
			t.Fatalf("%s: parse front matter: %v", name, err)
		}
		tr := fm.Tracker

		if tr.InProgressState != "in-progress" {
			t.Errorf("%s: in_progress_state = %q, want in-progress", name, tr.InProgressState)
		}
		if len(tr.ActiveStates) == 0 {
			t.Errorf("%s: active_states must be non-empty (1.27)", name)
		}
		if !contains(tr.ActiveStates, "in-progress") {
			t.Errorf("%s: active_states %v must contain in-progress", name, tr.ActiveStates)
		}
		hasTrigger := contains(tr.ActiveStates, "agent:plan") || contains(tr.ActiveStates, "agent:plan-build") || contains(tr.ActiveStates, "agent:build") || contains(tr.ActiveStates, "agent:docs") || contains(tr.ActiveStates, "agent:review") || contains(tr.ActiveStates, "agent:merge")
		if !hasTrigger {
			t.Errorf("%s: active_states %v must contain at least one dispatch trigger (1.27: a trigger alone must satisfy the gate)", name, tr.ActiveStates)
		}
		if tr.HandoffState != "agent:done" {
			t.Errorf("%s: handoff_state = %q, want agent:done", name, tr.HandoffState)
		}
		if contains(tr.ActiveStates, tr.HandoffState) {
			t.Errorf("%s: handoff_state %q must not be an active state", name, tr.HandoffState)
		}
		if contains(tr.TerminalStates, "agent:done") {
			t.Errorf("%s: agent:done must not be terminal — terminal states close the item", name)
		}
		if !strings.Contains(tr.QueryFilter, "in-progress") {
			t.Errorf("%s: query_filter %q must match in-progress so a crashed run is re-picked", name, tr.QueryFilter)
		}
		if !strings.Contains(tr.QueryFilter, "-label:needs-human") {
			t.Errorf("%s: query_filter %q must exclude needs-human", name, tr.QueryFilter)
		}
		for _, l := range queryLabel.FindAllStringSubmatch(tr.QueryFilter, -1) {
			if !allowedLabels[l[1]] {
				t.Errorf("%s: query_filter names unknown label %q", name, l[1])
			}
		}
		for _, l := range tr.ActiveStates {
			if !virtualStates[l] && !allowedLabels[l] {
				t.Errorf("%s: active_states names unknown state %q", name, l)
			}
		}
		loop := strings.TrimSuffix(strings.TrimPrefix(name, "WORKFLOW."), ".md")
		if def := config.DefaultLabelFilter(loop); def != tr.QueryFilter {
			t.Errorf("%s: query_filter %q != config default %q (the loop overwrites one with the other)", name, tr.QueryFilter, def)
		}
	}
	if seen != 2 {
		t.Errorf("checked %d workflows, want 2", seen)
	}
}

// TestPromptLabels checks the finish step of every prompt: known label names,
// the loop's own trigger released, and agent:done reached.
func TestPromptLabels(t *testing.T) {
	entries, err := fs.ReadDir(embeddedWorkflows, "workflows/prompts")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := embeddedWorkflows.ReadFile("workflows/prompts/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		releasedTrigger := false
		addedDone := false
		for _, m := range labelArg.FindAllStringSubmatch(body, -1) {
			removed := strings.HasPrefix(m[0], "--remove-label")
			for _, l := range strings.Split(m[1], ",") {
				l = strings.TrimSpace(l)
				if l == "" {
					continue
				}
				if !allowedLabels[l] {
					t.Errorf("%s: unknown label %q in %q", e.Name(), l, m[0])
				}
				if removed && (strings.HasPrefix(l, "agent:") && l != "agent:done") {
					releasedTrigger = true
				}
				if !removed && l == "agent:done" {
					addedDone = true
				}
			}
		}
		if !releasedTrigger {
			t.Errorf("%s: no finish step removes a trigger label", e.Name())
		}
		if !addedDone {
			t.Errorf("%s: no finish step adds agent:done", e.Name())
		}
	}
}

func TestPromptProcessLifecycle(t *testing.T) {
	entries, err := fs.ReadDir(embeddedWorkflows, "workflows/prompts")
	if err != nil {
		t.Fatal(err)
	}
	required := []string{
		"## Process lifecycle (mandatory)",
		"Prefer a foreground supervisor",
		"finite timeout budget",
		"success, failure, timeout, cancellation",
		"SIGTERM",
		"SIGKILL",
		"timeout --signal=TERM --kill-after=15s 1h",
		"Track each PID, port, and temporary directory",
		"Never use",
		"pattern-based kill",
		"all recorded PIDs and child processes are",
		"Remove temporary PID files, log files,",
		"do not report completion",
	}
	for _, e := range entries {
		raw, err := embeddedWorkflows.ReadFile("workflows/prompts/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		for _, phrase := range required {
			if !strings.Contains(body, phrase) {
				t.Errorf("%s: process lifecycle guidance missing %q", e.Name(), phrase)
			}
		}
	}
}

// TestSortieValidate runs the engine's own validator over each shipped
// workflow, so a front-matter mistake fails here instead of at runtime.
// Needs the sortie fork (the github-pr tracker and pi adapter live there);
// skipped when no such binary is available.
func TestSortieValidate(t *testing.T) {
	bin := sortieBin()
	if bin == "" {
		t.Skip("no sortie binary available (set JIG_ENGINE_BIN, or build the fork to /tmp/gobin/sortie-fork)")
	}
	root := t.TempDir()
	syncWorkflows(root)
	dir := filepath.Join(root, ".jigz", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"SORTIE_TRACKER_PROJECT=owner/name",
		"SORTIE_TRACKER_API_KEY=test",
		"JIG_WORKSPACES="+filepath.Join(root, "ws"),
	)
	build := ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "WORKFLOW.") {
			continue
		}
		if e.Name() == "WORKFLOW.issues.md" {
			build = e.Name()
		}
		cmd := exec.Command(bin, "validate", e.Name())
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s: %v\n%s", e.Name(), err, out)
		}
		if got := dropRuntimeBlockWarnings(string(out)); strings.Contains(got, "unknown top-level key") {
			t.Errorf("%s: unknown-key warnings remain:\n%s", e.Name(), got)
		}
	}
	if build == "" {
		t.Fatal("WORKFLOW.issues.md not installed")
	}
	// The engine must also accept the filter Jigz injects at launch.
	cmd := exec.Command(bin, "validate", build)
	cmd.Dir = dir
	cmd.Env = append(env,
		"SORTIE_TRACKER_QUERY_FILTER="+config.DefaultLabelFilter("issues")+" assignee:@me",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("injected filter rejected: %v\n%s", err, out)
	}
}

// sortieBin returns a validator binary, preferring the fork build: the stock
// engine has neither the github-pr tracker nor the pi adapter, so it rejects
// every shipped workflow. A fork installed normally is linked as a plain
// `sortie`, so that is checked too; without it this test never runs on an
// installed machine.
func sortieBin() string {
	candidates := []string{os.Getenv("JIG_ENGINE_BIN"), "/tmp/gobin/sortie-fork"}
	if home := os.Getenv("HOME"); home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "sortie-fork"),
			filepath.Join(home, ".local", "bin", "sortie"),
		)
	}
	if p, err := exec.LookPath("sortie"); err == nil {
		candidates = append(candidates, p)
	}
	for _, p := range candidates {
		if p != "" {
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
				return p
			}
		}
	}
	return ""
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestDefaultLabelsCoverWorkflows checks the seeded label set covers what the
// shipped workflows act on, so setup creates every label the loops use.
func TestDefaultLabelsCoverWorkflows(t *testing.T) {
	have := map[string]bool{}
	for _, l := range config.DefaultLabels {
		have[l.Name] = true
	}
	for _, l := range []string{"agent:plan", "agent:plan-build", "agent:build", "agent:review", "agent:merge", "agent:docs", "in-progress", "agent:done", "needs-human"} {
		if !have[l] {
			t.Errorf("DefaultLabels is missing %q", l)
		}
	}
	if len(config.DefaultLabels) != len(have) {
		t.Errorf("DefaultLabels has duplicate names")
	}
	if have["agent:quick"] {
		t.Errorf("agent:quick is parked and must not be seeded")
	}
}
