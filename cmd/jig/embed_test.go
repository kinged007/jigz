package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

func TestSetupSeedsLabelsIntoFreshConfig(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".sortie"), 0o755)
	// Simulate the config body setup writes for a fresh repo, then check
	// it parses and carries every default label.
	var body string
	for _, l := range config.DefaultLabels {
		body += "- {name: \"" + l.Name + "\", color: \"" + l.Color + "\", description: \"x\"}\n"
	}
	os.WriteFile(filepath.Join(root, ".sortie", "config.yaml"),
		[]byte("repo: o/r\ntoken: tok\nlabels:\n"+body), 0o644)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Labels) != len(config.DefaultLabels) {
		t.Fatalf("labels = %d, want %d", len(cfg.Labels), len(config.DefaultLabels))
	}
	if cfg.Labels[0].Name != config.DefaultLabels[0].Name {
		t.Errorf("first label = %q", cfg.Labels[0].Name)
	}
}

func TestSyncWorkflowsKeepsEditsFillsGaps(t *testing.T) {
	root := t.TempDir()
	if legacy := syncWorkflows(root); len(legacy) != 0 {
		t.Fatalf("fresh repo reported legacy files %v", legacy)
	}
	issues := filepath.Join(root, ".sortie", "workflows", "WORKFLOW.issues.md")
	if _, err := os.Stat(filepath.Join(root, ".sortie", "workflows", "WORKFLOW.prs.md")); err != nil {
		t.Fatalf("setup did not install the prs workflow: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".sortie", "workflows", "prompts", "docs.md")); err != nil {
		t.Fatalf("setup did not install prompts: %v", err)
	}
	f, _ := os.OpenFile(issues, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("\n# local tweak")
	f.Close()
	promptFile := filepath.Join(root, ".sortie", "workflows", "prompts", "docs.md")
	os.WriteFile(promptFile, []byte("mine\n"), 0o644)
	os.Remove(filepath.Join(root, ".sortie", "workflows", "prompts", "quick.md"))
	if got := workflowPath(root, "WORKFLOW.issues.md"); got != issues {
		t.Fatalf("workflowPath = %q, want %q", got, issues)
	}
	raw, _ := os.ReadFile(issues)
	if !strings.HasSuffix(string(raw), "\n# local tweak") {
		t.Error("sync overwrote local workflow edit")
	}
	if got, _ := os.ReadFile(promptFile); string(got) != "mine\n" {
		t.Error("sync overwrote local prompt edit")
	}
	if _, err := os.Stat(filepath.Join(root, ".sortie", "workflows", "prompts", "quick.md")); err != nil {
		t.Error("sync did not restore missing prompt file")
	}
}

func TestSyncWorkflowsSkipsConsolidatedFilesWhileLegacyExist(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".sortie", "workflows")
	os.MkdirAll(dir, 0o755)
	const old = "---\n# old plan\n"
	os.WriteFile(filepath.Join(dir, "WORKFLOW.plan.md"), []byte(old), 0o644)
	os.WriteFile(filepath.Join(dir, "WORKFLOW.merge.md"), []byte(old), 0o644)

	legacy := syncWorkflows(root)
	if strings.Join(legacy, ",") != "WORKFLOW.plan.md,WORKFLOW.merge.md" && strings.Join(legacy, ",") != "WORKFLOW.merge.md,WORKFLOW.plan.md" {
		t.Errorf("legacy = %v", legacy)
	}
	for _, name := range consolidatedWorkflows {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s written while legacy files exist", name)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "WORKFLOW.plan.md")); string(got) != old {
		t.Error("legacy file was changed")
	}
	names := loopNames(discoverWorkflows(root))
	if names != "merge,plan" {
		t.Errorf("discoverWorkflows = %s, want only the legacy loops", names)
	}

	os.Remove(filepath.Join(dir, "WORKFLOW.plan.md"))
	os.Remove(filepath.Join(dir, "WORKFLOW.merge.md"))
	if legacy := syncWorkflows(root); len(legacy) != 0 {
		t.Errorf("legacy = %v after deleting the legacy files", legacy)
	}
	if names := loopNames(discoverWorkflows(root)); names != "issues,prs" {
		t.Errorf("discoverWorkflows = %s, want issues,prs", names)
	}
}

func TestWarnLegacyMessage(t *testing.T) {
	r, w, _ := os.Pipe()
	saved := os.Stderr
	os.Stderr = w
	warnLegacy([]string{"WORKFLOW.plan.md"})
	warnLegacy(nil)
	w.Close()
	os.Stderr = saved
	out, _ := io.ReadAll(r)
	got := string(out)
	if strings.Count(got, "warning:") != 1 || !strings.Contains(got, "WORKFLOW.plan.md") || !strings.Contains(got, "skipped until the legacy files are deleted") {
		t.Errorf("message = %q", got)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	saved := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = saved
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestWarnStalePrompts(t *testing.T) {
	root := t.TempDir()
	syncWorkflows(root)
	if got := captureStderr(t, func() { warnStalePrompts(root) }); got != "" {
		t.Errorf("shipped prompts warned: %q", got)
	}
	os.WriteFile(filepath.Join(root, ".sortie", "workflows", "prompts", "build.md"), []byte("old\n"), 0o644)
	got := captureStderr(t, func() { warnStalePrompts(root) })
	if strings.Count(got, "note:") != 1 || !strings.Contains(got, "prompts/build.md") {
		t.Errorf("message = %q", got)
	}
}

func TestWarnMissingLabels(t *testing.T) {
	if got := captureStderr(t, func() { warnMissingLabels(config.DefaultLabels) }); got != "" {
		t.Errorf("full list warned: %q", got)
	}
	got := captureStderr(t, func() { warnMissingLabels([]config.Label{{Name: "agent:plan"}}) })
	if want := len(config.DefaultLabels) - 1; strings.Count(got, "note:") != want {
		t.Errorf("got %d notes, want %d: %q", strings.Count(got, "note:"), want, got)
	}
	for _, name := range []string{"agent:plan-build", "agent:docs"} {
		if !strings.Contains(got, `"`+name+`"`) {
			t.Errorf("no note for %s: %q", name, got)
		}
	}
	if strings.Contains(got, `"agent:plan" `) {
		t.Errorf("noted a label that is in the list: %q", got)
	}
}

func loopNames(loops []loopDef) string {
	var n []string
	for _, l := range loops {
		n = append(n, l.name)
	}
	return strings.Join(n, ",")
}

// withStdin runs fn with os.Stdin replaced by a pipe carrying input, so
// confirmOverwrite is exercised on its real code path. Passing "" leaves
// the write end closed, which is what a non-interactive run sees.
func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if input != "" {
		if _, err := w.WriteString(input); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	saved, savedReader := os.Stdin, promptIn
	os.Stdin, promptIn = r, nil
	defer func() { os.Stdin, promptIn = saved, savedReader }()
	fn()
}

func TestSyncPMAgentInstallsPrompt(t *testing.T) {
	root := t.TempDir()
	syncPMAgent(root, keepEdits)
	dest := filepath.Join(root, ".sortie", "pm-agent.md")
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("setup did not install the pm prompt: %v", err)
	}
	if !strings.Contains(string(raw), "united-into") {
		t.Error("installed pm prompt lacks the label contract it routes on")
	}
	// A hand-edited prompt survives setup without confirmation.
	const mine = "# mine\n"
	os.WriteFile(dest, []byte(mine), 0o644)
	withStdin(t, "", func() { syncPMAgent(root, askBeforeOverwrite) })
	if got, _ := os.ReadFile(dest); string(got) != mine {
		t.Error("setup replaced a customized pm prompt without confirmation")
	}
}

func TestEnsureGitignoreIgnoresSortieWholesale(t *testing.T) {
	root := t.TempDir()
	// Fresh repo: .gitignore is created with the wholesale entry only.
	ensureGitignore(root)
	raw, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(raw), ".sortie/\n") {
		t.Fatalf(".gitignore = %q, want wholesale .sortie/ entry", raw)
	}
	for _, e := range oldGitignoreEntries {
		if strings.Contains(string(raw), e+"\n") {
			t.Errorf(".gitignore still has granular entry %q", e)
		}
	}
	// Old granular scheme is migrated to wholesale on re-run.
	os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte("node_modules\n"+strings.Join(oldGitignoreEntries, "\n")+"\n"), 0o644)
	ensureGitignore(root)
	raw, _ = os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(raw), "node_modules\n") {
		t.Errorf("existing entries lost: %q", raw)
	}
	if !strings.Contains(string(raw), ".sortie/\n") {
		t.Errorf("no wholesale entry after migration: %q", raw)
	}
	// Selective tracking (negations) is left alone.
	os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte(".sortie/*\n!.sortie/config.yaml\n"), 0o644)
	ensureGitignore(root)
	raw, _ = os.ReadFile(filepath.Join(root, ".gitignore"))
	if strings.Contains(string(raw), ".sortie/\n") {
		t.Errorf("selective gitignore overwritten: %q", raw)
	}
}

// JIG_ENGINE_BIN wins over every other source. setup resolves the engine the
// same way the run path does, so a user who set it keeps their own binary
// instead of having a second engine downloaded and linked beside it.
func TestResolveSortieBinPrefersEnv(t *testing.T) {
	mine := filepath.Join(t.TempDir(), "sortie")
	if err := os.WriteFile(mine, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_ENGINE_BIN", mine)

	got, err := resolveSortieBin()
	if err != nil {
		t.Fatalf("resolveSortieBin: %v", err)
	}
	if got != mine {
		t.Errorf("got %q, want %q", got, mine)
	}
}

// setup must resolve the engine the same way the run path does. It called
// ensureEngine directly, so a user who set JIG_ENGINE_BIN had a second engine
// downloaded and linked onto PATH beside the one they chose.
func TestSetupHonoursSortieBin(t *testing.T) {
	mine := filepath.Join(t.TempDir(), "sortie")
	if err := os.WriteFile(mine, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_ENGINE_BIN", mine)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		// setup continues into label sync, which needs gh and a token and
		// will fatal. The engine step runs before that, which is all this
		// is checking, so the failure past it is expected.
		defer func() { recover() }()
		runSetup(repo, "owner/name", false)
	})
	if strings.Contains(out, "engine not installed") {
		t.Errorf("setup downloaded an engine despite JIG_ENGINE_BIN:\n%s", out)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stdout = old
	w.Close()
	s := <-done
	r.Close()
	return s
}

func TestDiscoverWorkflowsPicksUpNewFile(t *testing.T) {
	root := t.TempDir()
	syncWorkflows(root)
	// A hand-added workflow (no rebuild, no code change) is discovered.
	os.WriteFile(filepath.Join(root, ".sortie", "workflows", "WORKFLOW.triage.md"), []byte("---\n"), 0o644)
	// Non-workflow files are ignored.
	os.WriteFile(filepath.Join(root, ".sortie", "workflows", "notes.md"), []byte("x"), 0o644)
	got := discoverWorkflows(root)
	var names []string
	for _, l := range got {
		names = append(names, l.name)
	}
	want := []string{"issues", "prs", "triage"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("discoverWorkflows = %v, want %v", names, want)
	}
	if got[len(got)-1].file != "WORKFLOW.triage.md" {
		t.Errorf("triage file = %q", got[len(got)-1].file)
	}
}

func TestSelectLoops(t *testing.T) {
	all := []loopDef{{"build", "WORKFLOW.build.md"}, {"merge", "WORKFLOW.merge.md"}, {"plan", "WORKFLOW.plan.md"}}
	names := func(ls []loopDef) string {
		var n []string
		for _, l := range ls {
			n = append(n, l.name)
		}
		return strings.Join(n, ",")
	}
	// Unset/blank keeps every loop, in discovery order: a plain run is
	// unaffected by the selector.
	for _, only := range []string{"", "   "} {
		got, err := selectLoops(all, only)
		if err != nil || names(got) != "build,merge,plan" {
			t.Errorf("selectLoops(%q) = %v, %v; want all", only, names(got), err)
		}
	}
	// A subset keeps discovery order, not the order requested, and
	// carries the file through so the caller runs the right one.
	got, err := selectLoops(all, " merge , build ")
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "build,merge" || got[1].file != "WORKFLOW.merge.md" {
		t.Errorf("selectLoops = %v %q, want build,merge with merge's file", names(got), got[1].file)
	}
	// An unknown name is fatal even alongside known ones: starting the
	// rest would leave the asked-for loop's work unwatched.
	for _, only := range []string{"build,triage", "triage", "plan,build,review", ",", " , "} {
		if _, err := selectLoops(all, only); err == nil {
			t.Errorf("selectLoops(%q) = nil error, want an error", only)
		}
	}
	// Discovery order is preserved, not the requested order.
	if got, err := selectLoops(all, "plan,build"); err != nil || names(got) != "build,plan" {
		t.Errorf("selectLoops = %v, %v; want build,plan in discovery order", names(got), err)
	}
}

// TestSelectLoopsAgainstInstalledWorkflows pins the selector to the
// workflows setup actually installs, so a renamed or dropped loop is
// caught here rather than by a supervisor that names it.
func TestSelectLoopsAgainstInstalledWorkflows(t *testing.T) {
	root := t.TempDir()
	syncWorkflows(root)
	all := discoverWorkflows(root)
	if got, err := selectLoops(all, ""); err != nil || len(got) != len(all) {
		t.Fatalf("selectLoops(\"\") = %d loops, %v; want %d", len(got), err, len(all))
	}
	var want []string
	for _, l := range all {
		want = append(want, l.name)
	}
	got, err := selectLoops(all, strings.Join(want, ","))
	if err != nil {
		t.Fatalf("selectLoops(all names) = %v; every installed loop must be selectable", err)
	}
	if len(got) != len(all) {
		t.Errorf("selectLoops(all names) = %d loops, want %d", len(got), len(all))
	}
}

// An engine in the same directory as the link destination must not be
// rewritten as a symlink to itself, which would leave a binary that can
// never run.
func TestEnsureSortieLinkDoesNotDestroyTheEngineBesideIt(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "sortie")
	body := []byte("#!/bin/sh\necho engine\n")
	if err := os.WriteFile(engine, body, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real engine binary at the exact path ensureSortieLink would link.
	if err := os.WriteFile(filepath.Join(dir, "sortie"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_ENGINE_BIN", engine)
	t.Setenv("PATH", dir)

	ensureSortieLinkIn(dir)
	got, err := os.ReadFile(filepath.Join(dir, "sortie"))
	if err != nil {
		t.Fatalf("engine is unreadable: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("engine at %s was replaced: %q", dir, got)
	}
	info, err := os.Lstat(filepath.Join(dir, "sortie"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("engine became a symlink to itself")
	}
}

// A genuine second location still gets the convenience link, and an
// existing correct link is left alone rather than recreated.
func TestEnsureSortieLinkLinksADistinctEngine(t *testing.T) {
	dir := t.TempDir()
	loops := filepath.Join(dir, "bin")
	if err := os.MkdirAll(loops, 0o755); err != nil {
		t.Fatal(err)
	}
	engine := filepath.Join(dir, "elsewhere", "sortie")
	if err := os.MkdirAll(filepath.Dir(engine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(engine, []byte("engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_ENGINE_BIN", engine)

	ensureSortieLinkIn(loops)
	link := filepath.Join(loops, "sortie")
	cur, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("no link created at %s: %v", link, err)
	}
	if cur != engine {
		t.Errorf("link = %q, want %q", cur, engine)
	}
	// Idempotent: a second call must not fail or change the link.
	ensureSortieLinkIn(loops)
	again, err := os.Readlink(link)
	if err != nil || again != engine {
		t.Errorf("second call changed the link: %q %v", again, err)
	}
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	if !samePath(a, link) {
		t.Error("a and a symlink to it are the same file")
	}
	if !samePath(a, a) {
		t.Error("a path is the same as itself")
	}
	if samePath(a, filepath.Join(dir, "missing")) {
		t.Error("a missing path is the same as a")
	}
}

// The shared registry is keyed by repo identity, and a supervisor
// filters and groups it by owner/name. A state directory is not a git
// checkout, so the git remote lookup fails and the old fallback kept
// only filepath.Base: the registry said "myxon-beta" where every caller
// keys on "kinged007/myxon-beta", and the match silently found nothing.
// The repo: written into .sortie/config.yaml is authoritative.
func TestRepoNameUsesTheConfiguredSlugForANonCheckout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repos", "kinged007", "myxon-beta")
	if err := os.MkdirAll(filepath.Join(root, ".sortie"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "repo: kinged007/myxon-beta\ntoken: \"\"\nassignee: \"@me\"\n"
	if err := os.WriteFile(filepath.Join(root, ".sortie", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// No git remote here, which is the whole point.
	if got := guessRepo(root); got != "" {
		t.Fatalf("guessRepo = %q, want empty for a non-checkout", got)
	}
	if got := repoName(root); got != "kinged007/myxon-beta" {
		t.Errorf("repoName = %q, want the configured slug", got)
	}
}

// A real checkout still resolves through the git remote.
func TestRepoNameStillUsesTheGitRemote(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sortie"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "https://github.com/kinged007/from-remote.git"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, c...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", c, err, out)
		}
	}
	if got := repoName(root); got != "kinged007/from-remote" {
		t.Errorf("repoName = %q, want the slug from the git remote", got)
	}
}

// With nothing to go on, the basename is still the best answer.
func TestRepoNameFallsBackToTheBasename(t *testing.T) {
	root := filepath.Join(t.TempDir(), "just-a-dir")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := repoName(root); got != "just-a-dir" {
		t.Errorf("repoName = %q, want the basename", got)
	}
}
