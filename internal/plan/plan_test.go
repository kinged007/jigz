package plan

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/engine"
	config "github.com/kinged007/jigz/internal/fleetconfig"
)

func resolved(t *testing.T, dir string, mutate ...func(*config.Resolved)) config.Resolved {
	t.Helper()
	r := config.Resolved{Repo: config.Repo{Repo: "owner/alpha"}, Dir: dir}
	for _, m := range mutate {
		m(&r)
	}
	return r
}

func workflows(t *testing.T, dir string, names ...string) {
	t.Helper()
	w := filepath.Join(dir, ".sortie", "workflows")
	if err := os.MkdirAll(w, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		// Shaped like a shipped workflow: a polling: block, an agent:
		// block, and an adapter block carrying the model.
		body := "---\npolling:\n  interval_ms: 60000\n\nagent:\n  kind: pi\n  max_turns: 5\n  max_concurrent_agents: 1\n\npi:\n  model: \"\"\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(w, "WORKFLOW."+n+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// config.yaml is an overlay: only keys Jigz holds may appear, so
// everything else keeps Jigz's default.
func TestApplyWritesOnlyConfiguredKeys(t *testing.T) {
	dir := t.TempDir()
	r := resolved(t, dir)
	if err := Apply(r); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".sortie", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "repo: owner/alpha") {
		t.Errorf("config.yaml missing repo:\n%s", body)
	}
	for _, absent := range []string{"milestone:", "assignee:", "filters:", "token:", "labels:"} {
		if strings.Contains(body, absent) {
			t.Errorf("config.yaml sets %s with nothing configured:\n%s", absent, body)
		}
	}
}

// Re-applying must not churn the file: the supervisor rewrites it on
// every start, and a changed mtime alone would look like a change.
func TestApplyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	r := resolved(t, dir, func(r *config.Resolved) {
		r.Milestone = "v1"
		r.Assignee = &[]string{"someone"}[0]
		r.Repo.Filters = map[string]string{"build": "label:agent:build"}
	})
	if err := Apply(r); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".sortie", "config.yaml")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(r); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Errorf("Apply is not idempotent:\n%s\n---\n%s", first, second)
	}
	if !strings.Contains(string(first), `milestone: v1`) || !strings.Contains(string(first), "assignee: someone") {
		t.Errorf("configured scope missing from:\n%s", first)
	}
}

// The token must not reach disk from any path.
func TestApplyNeverWritesAToken(t *testing.T) {
	t.Setenv("JIG_TEST_TOKEN", "ghp_secret")
	dir := t.TempDir()
	r := resolved(t, dir, func(r *config.Resolved) { r.Repo.TokenEnv = "JIG_TEST_TOKEN" })
	if err := Apply(r); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".sortie", "config.yaml"))
	if strings.Contains(string(raw), "ghp_secret") {
		t.Errorf("token written to config.yaml:\n%s", raw)
	}
}

const realWorkflow = `---
tracker:
  kind: github
  api_key: $SORTIE_TRACKER_API_KEY
pi:
  model: ""
# a comment that must survive
claude-code:
  model: ""
---

body text
`

func TestPatchBlockRewritesOnlyTheTargetedLine(t *testing.T) {
	got, changed, err := patchBlock([]byte(realWorkflow), "pi", "model", "$JIG_MODEL", false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("changed = false on a file that needed a patch")
	}
	out := string(got)
	if !strings.Contains(out, "  model: $JIG_MODEL") {
		t.Errorf("model line not rewritten:\n%s", out)
	}
	// The other adapter's model must be untouched, the comment kept, and
	// the body byte-identical.
	if !strings.Contains(out, "claude-code:\n  model: \"\"") {
		t.Errorf("claude-code block was modified:\n%s", out)
	}
	if !strings.Contains(out, "# a comment that must survive") {
		t.Errorf("comment dropped:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n---\n\nbody text\n") {
		t.Errorf("front matter fence or body changed:\n%s", out)
	}
	// Idempotent: a second pass must be a no-op.
	again, changed, err := patchBlock(got, "pi", "model", "$JIG_MODEL", false)
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(again) != out {
		t.Errorf("patchBlock is not idempotent (changed=%v):\n%s\n---\n%s", changed, out, again)
	}
}

func TestPatchBlockPicksTheNamedBlock(t *testing.T) {
	got, _, err := patchBlock([]byte(realWorkflow), "claude-code", "model", "$X", false)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if !strings.Contains(out, "claude-code:\n  model: $X") {
		t.Errorf("claude-code model not rewritten:\n%s", out)
	}
	if !strings.Contains(out, "pi:\n  model: \"\"") {
		t.Errorf("pi block was modified:\n%s", out)
	}
}

func TestPatchBlockReportsAMissingBlock(t *testing.T) {
	for name, body := range map[string]string{
		"no fence":     "agent:\n  kind: pi\n",
		"unterminated": "---\nagent:\n  kind: pi\n",
		"no block":     "---\nagent:\n  kind: pi\n---\n",
	} {
		if _, _, err := patchBlock([]byte(body), "pi", "model", "$X", false); err == nil {
			t.Errorf("%s: patchBlock = nil error, want an error", name)
		}
	}
}

// A block that exists but lacks the key is a workflow that simply never
// set one, so the key is added under its header rather than refused.
func TestPatchBlockInsertsAnAbsentKey(t *testing.T) {
	body := "---\nagent:\n  kind: pi\n  max_turns: 5\n---\nbody\n"
	got, changed, err := patchBlock([]byte(body), "agent", "max_concurrent_agents", "3", false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false")
	}
	out := string(got)
	if !strings.Contains(out, "agent:\n  max_concurrent_agents: 3\n") {
		t.Errorf("key not inserted under its block header:\n%s", out)
	}
	if !strings.Contains(out, "  max_turns: 5\n") {
		t.Errorf("existing sibling lost:\n%s", out)
	}
	if !strings.HasSuffix(out, "---\nbody\n") {
		t.Errorf("body changed:\n%s", out)
	}
}

// No agent or workflow settings at all means no edit, so the workflow
// stays exactly as Jigz installed it.
func TestApplySettingsSkipsWhenUnset(t *testing.T) {
	dir := t.TempDir()
	workflows(t, dir, "build")
	path := filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.build.md")
	before, _ := os.ReadFile(path)
	if err := (Plan{}).ApplySettings(resolved(t, dir)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Errorf("ApplySettings changed a workflow with no settings:\n%s", after)
	}
}

// A model with no override keeps the original single-line behaviour.
func TestApplySettingsWritesTheModelLine(t *testing.T) {
	dir := t.TempDir()
	workflows(t, dir, "build")
	r := resolved(t, dir)
	r.Agent.Model = "gpt-5"
	r.Knobs = config.Settings(map[string]string{"model": "gpt-5"})
	if err := (Plan{}).ApplySettings(r); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.build.md"))
	if !strings.Contains(string(raw), "  model: $JIG_MODEL") {
		t.Errorf("model line not written:\n%s", raw)
	}
}

// Each workflow gets its own model variable, so one file's value cannot
// be read as another's.
func TestApplySettingsWritesPerWorkflowKnobs(t *testing.T) {
	dir := t.TempDir()
	workflows(t, dir, "plan", "build")
	r := resolved(t, dir)
	r.Workflows = []string{"plan", "build"}
	r.Knobs = config.Settings(map[string]string{
		"model":                 "gpt-5",
		"max_concurrent_agents": "2",
	})
	r.Overrides = map[string]config.Settings{
		"build": {"model": "opus", "max_concurrent_agents": "6"},
	}
	if err := (Plan{}).ApplySettings(r); err != nil {
		t.Fatal(err)
	}
	read := func(n string) string {
		raw, err := os.ReadFile(filepath.Join(dir, ".sortie", "workflows", "WORKFLOW."+n+".md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	plan, build := read("plan"), read("build")
	if !strings.Contains(plan, "  model: $JIG_MODEL\n") {
		t.Errorf("plan model line:\n%s", plan)
	}
	if !strings.Contains(build, "  model: $JIG_MODEL_BUILD\n") {
		t.Errorf("build model line:\n%s", build)
	}
	// plan takes the repository value, written to its file because
	// build overrides the knob.
	if !strings.Contains(plan, "  max_concurrent_agents: 2") {
		t.Errorf("plan did not take the repository value:\n%s", plan)
	}
	if !strings.Contains(build, "  max_concurrent_agents: 6") {
		t.Errorf("build did not take its own value:\n%s", build)
	}
}

func TestWorkflowFilesFollowsSelection(t *testing.T) {
	dir := t.TempDir()
	workflows(t, dir, "build", "merge", "plan")

	all, err := (Plan{}).workflowFiles(resolved(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(all); n != 3 {
		t.Errorf("no selection: got %d files, want all 3", n)
	}
	two, err := (Plan{}).workflowFiles(resolved(t, dir, func(r *config.Resolved) {
		r.Workflows = []string{"plan", "build"}
	}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range two {
		names = append(names, filepath.Base(f))
	}
	// Directory order, so the validate run and the started set agree.
	if strings.Join(names, ",") != "WORKFLOW.build.md,WORKFLOW.plan.md" {
		t.Errorf("selected files = %v, want build and plan only", names)
	}
}

// The shipped workflows name the polling key interval_ms inside the
// polling: block, not polling_interval_ms. Writing the knob's own name
// there would add a key the engine ignores, leaving the poll interval
// silently at the shipped 60000.
func TestPollingKnobUsesItsBlockKey(t *testing.T) {
	if got := knobTargets["polling_interval_ms"]; got.block != "polling" || got.key != "interval_ms" {
		t.Fatalf("polling_interval_ms target = %+v, want polling/interval_ms", got)
	}
	dir := t.TempDir()
	workflows(t, dir, "build")
	r := resolved(t, dir)
	r.Knobs = config.Settings{"polling_interval_ms": "15000"}
	r.Overrides = map[string]config.Settings{
		"build": {"polling_interval_ms": "15000"},
	}
	if err := (Plan{}).ApplySettings(r); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.build.md"))
	if !strings.Contains(string(raw), "polling:\n  interval_ms: 15000") {
		t.Errorf("poll interval not written to the real key:\n%s", raw)
	}
	if strings.Contains(string(raw), "polling_interval_ms") {
		t.Errorf("wrote a key the engine does not read:\n%s", raw)
	}
}

// A workflow that ships only the default kind's adapter block has no
// codex: block, so setting the model for a different kind has to add
// one. The engine reads the block named by the active kind.
func TestAdapterBlockIsCreatedForANonDefaultKind(t *testing.T) {
	dir := t.TempDir()
	workflows(t, dir, "build")
	r := resolved(t, dir)
	r.Agent.Kind = "codex"
	r.Knobs = config.Settings{"model": "gpt-5.6-luna"}
	if err := (Plan{}).ApplySettings(r); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.build.md"))
	if !strings.Contains(string(raw), "codex:\n  model: $JIG_MODEL\n") {
		t.Errorf("codex adapter block not created:\n%s", raw)
	}
	// The pi block is left alone: the engine only reads the active
	// kind's block, and rewriting it would edit a block nothing reads.
	if !strings.Contains(string(raw), "pi:\n  model: \"\"") {
		t.Errorf("pi block was modified:\n%s", raw)
	}
	// The kind itself is the env's business, not the file's.
	if !strings.Contains(string(raw), "agent:\n  kind: pi\n") {
		t.Errorf("agent block was modified:\n%s", raw)
	}
	// Still valid front matter: one closing fence, comments intact.
	if strings.Count(string(raw), "\n---\n") != 1 {
		t.Errorf("front matter fences changed:\n%s", raw)
	}
	if !strings.Contains(string(raw), "---\nbody\n") {
		t.Errorf("body changed:\n%s", raw)
	}
}

// A missing load-bearing block stays an error: inventing an agent: or
// polling: block could disable the loop in a way that only shows up
// when an issue is dispatched.
func TestLoadbearingBlockIsNeverInvented(t *testing.T) {
	body := "---\npi:\n  model: \"\"\n---\nbody\n"
	for _, block := range []string{"agent", "polling"} {
		if _, _, err := patchBlock([]byte(body), block, "x", "1", false); err == nil {
			t.Errorf("%s: nil error, want an error rather than an invented block", block)
		}
	}
}

func isolateEngineEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []string{"JIG_ENGINE_BIN", "JIG_ENGINE_REPO", "JIG_ENGINE_VERSION", "JIG_ENGINE_URL", "JIG_ENGINE_SHA256"} {
		t.Setenv(k, "")
	}
	t.Setenv("PATH", "/nonexistent")
}

// A clean install puts the engine in <prefix>/share/jigz, not on
// PATH, because install.sh honours --prefix. The plan must find the same
// file engine.Resolve finds, including the one for a configured
// repository and version.
func TestSortieBinIsTheEngineResolverResult(t *testing.T) {
	isolateEngineEnv(t)
	t.Setenv("JIG_ENGINE_REPO", "sortie-ai/sortie")
	t.Setenv("JIG_ENGINE_VERSION", "v1.29.0")
	prefix := t.TempDir()
	binDir := filepath.Join(prefix, "bin")
	share := filepath.Join(prefix, "share", "jigz")
	for _, d := range []string{binDir, share} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	loop := filepath.Join(binDir, "jig")
	if err := os.WriteFile(loop, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec, err := engine.Configured()
	if err != nil {
		t.Fatal(err)
	}
	want := spec.CachePath(share)
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := engine.Spec{Repo: "kinged007/sortie", Tag: "v1.29.0"}
	if err := os.WriteFile(other.CachePath(share), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := Plan{LoopBin: loop}
	got, err := p.sortieBin()
	if err != nil {
		t.Fatalf("sortieBin: %v", err)
	}
	if got != want {
		t.Errorf("sortieBin = %q, want %q", got, want)
	}
	if resolved, _, err := engine.Resolve(loop); err != nil || resolved != got {
		t.Errorf("engine.Resolve = %q, %v; want the plan's %q", resolved, err, got)
	}
	if viaPath, err := p.SortieBinPath(); err != nil || viaPath != got {
		t.Errorf("SortieBinPath = %q, %v; want %q", viaPath, err, got)
	}

	t.Setenv("JIG_ENGINE_BIN", "/custom/sortie")
	if got, _ := p.sortieBin(); got != "/custom/sortie" {
		t.Errorf("sortieBin = %q, want JIG_ENGINE_BIN to win", got)
	}
}

// A jig installed beside this binary has to be found without
// PATH, or running jig by absolute path fails.
func TestLoopBinaryPrefersASiblingOverPATH(t *testing.T) {
	prefix := t.TempDir()
	loop := filepath.Join(prefix, "jig")
	if err := os.WriteFile(loop, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_BIN", "")
	t.Setenv("PATH", "/nonexistent")
	if got := (Plan{LoopBin: loop}).LoopBinary(); got != loop {
		t.Errorf("LoopBinary = %q, want the explicit %q", got, loop)
	}
	// With nothing beside it, it falls back to PATH lookup by name.
	if got := (Plan{}).LoopBinary(); got != "jig" {
		t.Errorf("LoopBinary = %q, want the bare name for PATH lookup", got)
	}
}

// Setting a model and then removing it must leave the shipped file byte
// for byte. Jigz patches in place and never restores, so anything it
// writes has to be removable: a dangling $JIG_MODEL is an
// unresolved reference, and an adapter block Jigz created is a
// top-level key the engine rejects, and either one stops the loop.
func TestRemovingAModelRestoresTheShippedFile(t *testing.T) {
	for _, kind := range []string{"codex", "claude-code", "pi"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			workflows(t, dir, "plan")
			path := filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.plan.md")
			// The baseline is the file as Jigz leaves it with no
			// model, not the untouched shipped copy: Jigz always writes
			// the loop bind address, so that line is not part of what this
			// test is about.
			if err := (Plan{}).ApplySettings(resolved(t, dir)); err != nil {
				t.Fatal(err)
			}
			shipped, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			on := resolved(t, dir)
			on.Agent.Kind = kind
			on.Agent.Model = "some-model"
			on.Knobs = config.Settings{"kind": kind, "model": "some-model"}
			if err := (Plan{}).ApplySettings(on); err != nil {
				t.Fatal(err)
			}
			mid, _ := os.ReadFile(path)
			if !strings.Contains(string(mid), modelRef) {
				t.Fatalf("the model was not delivered:\n%s", mid)
			}

			off := resolved(t, dir)
			off.Agent.Kind = ""
			off.Agent.Model = ""
			off.Knobs = nil
			if err := (Plan{}).ApplySettings(off); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(shipped) {
				t.Errorf("removing the model did not restore the file:\n--- want ---\n%s\n--- got ---\n%s", shipped, after)
			}
		})
	}
}

// The reference must never survive into a file whose variable is no
// longer generated, whichever adapter it was written into.
func TestNoReferenceSurvivesAnUnsetModel(t *testing.T) {
	dir := t.TempDir()
	workflows(t, dir, "plan")
	path := filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.plan.md")
	r := resolved(t, dir)
	r.Agent.Kind, r.Agent.Model, r.Knobs = "codex", "m", config.Settings{"kind": "codex", "model": "m"}
	if err := (Plan{}).ApplySettings(r); err != nil {
		t.Fatal(err)
	}
	off := resolved(t, dir)
	if err := (Plan{}).ApplySettings(off); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), modelRef) {
		t.Errorf("a reference to %s survived:\n%s", modelRef, got)
	}
	// The adapter block Jigz created has to go with it: an emptied
	// block is still a key the engine does not know.
	if strings.Contains(string(got), "\ncodex:") {
		t.Errorf("the codex block Jigz created survived:\n%s", got)
	}
}

// A block the operator wrote is not Jigz's to delete, so only the
// reference is reverted.
func TestAHandWrittenAdapterBlockIsKept(t *testing.T) {
	dir := t.TempDir()
	workflows(t, dir, "plan")
	path := filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.plan.md")
	body, _ := os.ReadFile(path)
	body = append(body, []byte("codex:\n  model: \"hand-written\"\n  reasoning_effort: high\n")...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	r := resolved(t, dir)
	r.Agent.Kind, r.Agent.Model = "codex", "m"
	r.Knobs = config.Settings{"kind": "codex", "model": "m"}
	if err := (Plan{}).ApplySettings(r); err != nil {
		t.Fatal(err)
	}
	off := resolved(t, dir)
	if err := (Plan{}).ApplySettings(off); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "reasoning_effort") {
		t.Errorf("Jigz deleted an adapter block it did not write:\n%s", got)
	}
	if strings.Contains(string(got), modelRef) {
		t.Errorf("the reference survived:\n%s", got)
	}
	if !strings.Contains(string(got), modelUnset) {
		t.Errorf("the reference was not reverted to %s:\n%s", modelUnset, got)
	}
}

// A per-workflow model gets a suffixed variable, and that one has to be
// pruned too.
func TestASuffixedModelReferenceIsAlsoPruned(t *testing.T) {
	dir := t.TempDir()
	workflows(t, dir, "plan")
	path := filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.plan.md")
	r := resolved(t, dir)
	r.Agent.Kind = "codex"
	r.Overrides = map[string]config.Settings{"plan": {"kind": "codex", "model": "m"}}
	if err := (Plan{}).ApplySettings(r); err != nil {
		t.Fatal(err)
	}
	mid, _ := os.ReadFile(path)
	if !strings.Contains(string(mid), "_MODEL_PLAN") {
		t.Fatalf("the suffixed variable was not written:\n%s", mid)
	}
	if err := (Plan{}).ApplySettings(resolved(t, dir)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "_MODEL") {
		t.Errorf("a suffixed reference survived:\n%s", after)
	}
}

func TestTrackerKindReadsTheWorkflowFrontMatter(t *testing.T) {
	dir := t.TempDir()
	w := filepath.Join(dir, ".sortie", "workflows")
	if err := os.MkdirAll(w, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"build":   "---\ntracker:\n  kind: github\n  api_key: $K\n---\nkind: github-pr\n",
		"review":  "---\npolling:\n  interval_ms: 1\ntracker:\n  kind: github-pr\n---\nbody\n",
		"nokind":  "---\ntracker:\n  project: x\n---\nbody\n",
		"nofront": "tracker:\n  kind: github\n",
		"broken":  "---\ntracker: [\n---\n",
	} {
		if err := os.WriteFile(filepath.Join(w, "WORKFLOW."+name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := resolved(t, dir)
	for name, want := range map[string]string{
		"build": "github", "review": "github-pr", "nokind": "", "nofront": "", "broken": "", "missing": "",
	} {
		if got := TrackerKind(r, name); got != want {
			t.Errorf("TrackerKind(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestValidateFileReportsTheEnginesCheckPaths(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sortie")
	script := "#!/bin/sh\necho '{\"valid\":false,\"errors\":[{\"check\":\"config.dispatch.rules[0].agent\",\"message\":\"unknown agent kind\"}],\"warnings\":[]}'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Plan{SortieBin: bin}.ValidateFile(resolved(t, dir), filepath.Join(dir, "x.md"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || len(res.Errors) != 1 || res.Errors[0].Check != "config.dispatch.rules[0].agent" || res.Errors[0].Message != "unknown agent kind" {
		t.Errorf("result = %+v", res)
	}
}

// fakeSetup is a jig that records the environment it was given.
func fakeSetup(t *testing.T) (bin, record string) {
	t.Helper()
	d := t.TempDir()
	record = filepath.Join(d, "env")
	bin = filepath.Join(d, "jig")
	script := "#!/bin/sh\nenv > '" + record + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

func TestSetupPassesTheProjectsOwnToken(t *testing.T) {
	t.Setenv("TOK_A", "token-of-a")
	t.Setenv("TOK_B", "token-of-b")
	t.Setenv("GITHUB_TOKEN", "fallback-token")
	bin, record := fakeSetup(t)
	r := resolved(t, t.TempDir(), func(r *config.Resolved) { r.TokenEnv = "TOK_A" })
	if err := (Plan{LoopBin: bin}).Setup(r); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(record)
	env := string(raw)
	if !strings.Contains(env, "\nJIG_TOKEN=token-of-a\n") && !strings.HasPrefix(env, "JIG_TOKEN=token-of-a\n") {
		t.Error("JIG_TOKEN is not the project's token")
	}
	if strings.Contains(env, "JIG_TOKEN=token-of-b") {
		t.Error("another project's token was passed as the loop token")
	}
	if !strings.Contains(env, "JIG_REPO=owner/alpha") {
		t.Error("JIG_REPO is missing")
	}
}

func TestSetupWithoutTokenEnvKeepsTheFallback(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "fallback-token")
	bin, record := fakeSetup(t)
	if _, err := (Plan{LoopBin: bin}).RunSetup(resolved(t, t.TempDir())); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(record)
	if strings.Contains(string(raw), "JIG_TOKEN=") {
		t.Error("JIG_TOKEN is set without a token_env")
	}
	if !strings.Contains(string(raw), "GITHUB_TOKEN=fallback-token") {
		t.Error("the GITHUB_TOKEN fallback is gone")
	}
}

func TestSetupFailsWhenTheTokenVariableIsUnset(t *testing.T) {
	bin, record := fakeSetup(t)
	r := resolved(t, t.TempDir(), func(r *config.Resolved) { r.TokenEnv = "TOK_UNSET_XYZ" })
	err := (Plan{LoopBin: bin}).Setup(r)
	if err == nil || !strings.Contains(err.Error(), "TOK_UNSET_XYZ") {
		t.Errorf("err = %v, want a message naming the variable", err)
	}
	if _, serr := os.Stat(record); serr == nil {
		t.Error("setup ran without a token")
	}
}

func TestWithoutRuntimeBlockWarnings(t *testing.T) {
	raw := `{"valid":true,"errors":[],"warnings":[` +
		`{"severity":"warning","check":"unknown_key","message":"claude-code: unknown top-level key \"claude-code\""},` +
		`{"severity":"warning","check":"unknown_key","message":"pi: unknown top-level key \"pi\""},` +
		`{"severity":"warning","check":"unknown_key","message":"extra: unknown top-level key \"extra\""}]}`
	var res FileResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	got := res.WithoutRuntimeBlockWarnings().Warnings
	if len(got) != 1 || !strings.HasPrefix(got[0].Message, "extra:") {
		t.Fatalf("warnings = %+v", got)
	}
}

func TestMain(m *testing.M) {
	engine.SetDownload(func(engine.Spec) (string, error) {
		return "", errors.New("engine download disabled in unit tests")
	})
	os.Exit(m.Run())
}
