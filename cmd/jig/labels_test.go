package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

// allowedLabels is every label the shipped prompts may name.
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

// labelArg matches the value of a --add-label / --remove-label argument in a
// prompt, including the multi-line form (`\` before the next line).
var labelArg = regexp.MustCompile(`--(?:add|remove)-label\s+"([^"]*)"`)

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

// TestSortieValidate runs the engine's own validator over the documents
// generated from the setup scaffold, so a front-matter mistake fails here
// instead of at runtime. Needs the sortie fork (the github-pr tracker and
// pi adapter live there); skipped when no such binary is available.
func TestSortieValidate(t *testing.T) {
	bin := sortieBin()
	if bin == "" {
		t.Skip("no sortie binary available (set JIG_ENGINE_BIN, or build the fork to /tmp/gobin/sortie-fork)")
	}
	root := writeGenConfig(t, configTemplateBody("owner/name"))
	docs, _, err := generateDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensurePrompts(root, docs); err != nil {
		t.Fatal(err)
	}
	if _, err := promoteDocs(root, docs); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".jigz", "workflows")
	env := append(os.Environ(),
		"SORTIE_TRACKER_PROJECT=owner/name",
		"SORTIE_TRACKER_API_KEY=test",
		"JIG_WORKSPACES="+filepath.Join(root, "ws"),
	)
	build := ""
	for _, d := range docs {
		name := "WORKFLOW." + d.Loop + ".md"
		if d.Loop == "issues" {
			build = name
		}
		cmd := exec.Command(bin, "validate", name)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
		if got := dropRuntimeBlockWarnings(string(out)); strings.Contains(got, "unknown top-level key") {
			t.Errorf("%s: unknown-key warnings remain:\n%s", name, got)
		}
	}
	if build == "" {
		t.Fatal("WORKFLOW.issues.md not generated")
	}
	// The engine must also accept the filter Jigz injects at launch.
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "validate", build)
	cmd.Dir = dir
	cmd.Env = append(env, "SORTIE_TRACKER_QUERY_FILTER="+cfg.FilterFor("issues"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("injected filter rejected: %v\n%s", err, out)
	}
}

// sortieBin returns a validator binary, preferring the fork build: the stock
// engine has neither the github-pr tracker nor the pi adapter, so it rejects
// every generated workflow. A fork installed normally is linked as a plain
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

// TestScaffoldLabelsCoverPrompts checks the seeded label set covers what the
// shipped prompts act on, so setup creates every label the loops use.
func TestScaffoldLabelsCoverPrompts(t *testing.T) {
	have := map[string]bool{}
	for _, l := range scaffoldLabels {
		have[l.Name] = true
	}
	for _, l := range []string{"agent:plan", "agent:plan-build", "agent:build", "agent:review", "agent:merge", "agent:docs", "in-progress", "agent:done", "needs-human"} {
		if !have[l] {
			t.Errorf("scaffoldLabels is missing %q", l)
		}
	}
	if len(scaffoldLabels) != len(have) {
		t.Errorf("scaffoldLabels has duplicate names")
	}
	if have["agent:quick"] {
		t.Errorf("agent:quick is parked and must not be seeded")
	}
}
