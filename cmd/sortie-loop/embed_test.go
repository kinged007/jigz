package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kinged007/sortie-loop/internal/config"
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

// linkEngineAt owns the only symlink this tool writes. The directory is
// passed in so the test never reaches into the real home directory.
func TestLinkEngineAtReplacesStaleLink(t *testing.T) {
	dir := t.TempDir()
	// The cache location decides which links sortie-loop may refresh, so
	// the test points the real one inside the temp dir.
	t.Setenv("XDG_DATA_HOME", dir)
	bin := engineDataDir()
	src := filepath.Join(bin, "sortie-1.0.0")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	bindir := filepath.Join(dir, "bin")
	link := filepath.Join(bindir, "sortie")

	if got := linkEngineAt(src, bindir); got != link {
		t.Fatalf("link = %q, want %q", got, link)
	}
	if got, err := os.Readlink(link); err != nil || got != src {
		t.Fatalf("readlink = %q, %v; want %q", got, err, src)
	}
	// Re-linking to the same target is a no-op, not an error.
	if got := linkEngineAt(src, bindir); got != link {
		t.Errorf("idempotent link = %q, want %q", got, link)
	}
	// A version bump re-points the same link at the new binary.
	next := filepath.Join(bin, "sortie-2.0.0")
	if err := os.WriteFile(next, []byte("engine2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := linkEngineAt(next, bindir); got != link {
		t.Fatalf("relink = %q, want %q", got, link)
	}
	if got, _ := os.Readlink(link); got != next {
		t.Errorf("readlink = %q, want %q", got, next)
	}
}

// A `sortie` this tool did not install is left alone rather than
// replaced.
func TestLinkEngineAtLeavesForeignBinary(t *testing.T) {
	dir := t.TempDir()
	bindir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bindir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(bindir, "sortie")
	if err := os.WriteFile(foreign, []byte("not ours"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "data", "sortie-loop", "sortie-1.0.0")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := linkEngineAt(src, bindir); got != "" {
		t.Errorf("foreign link replaced, returned %q", got)
	}
	if raw, err := os.ReadFile(foreign); err != nil || string(raw) != "not ours" {
		t.Errorf("foreign binary changed: %q, %v", raw, err)
	}
}

// A symlink the user made is their choice even though a symlink is not
// covered by the foreign-binary check above: it may point at a hand-built
// engine from source.
func TestLinkEngineAtLeavesUserSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	src := filepath.Join(engineDataDir(), "sortie-1.0.0")
	if err := os.MkdirAll(engineDataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	hand := filepath.Join(t.TempDir(), "my-sortie")
	if err := os.WriteFile(hand, []byte("built by hand"), 0o755); err != nil {
		t.Fatal(err)
	}
	bindir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bindir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bindir, "sortie")
	if err := os.Symlink(hand, link); err != nil {
		t.Fatal(err)
	}
	if got := linkEngineAt(src, bindir); got != "" {
		t.Errorf("user's symlink replaced, returned %q", got)
	}
	if got, err := os.Readlink(link); err != nil || got != hand {
		t.Errorf("readlink = %q, %v; want %q", got, err, hand)
	}
}

// The engine must be the archive's `sortie`, at the root or inside a
// single wrapper directory. A tampered archive must not reach it through a
// traversal or a nested path.
func TestEngineEntry(t *testing.T) {
	for _, ok := range []string{
		"sortie",             // flat archive
		"linux_amd64/sortie", // per-platform wrapper, the current GoReleaser layout
		"darwin_arm64/sortie",
	} {
		if !engineEntry(ok) {
			t.Errorf("engineEntry(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"../../sortie", // traversal
		"a/b/sortie",   // nested deeper than a wrapper
		"/sortie",      // absolute
		"./sortie",     // relative
		"sortie/../sortie",
		"linux_amd64/sortie.exe",
		"linux_amd64/sortie/LICENS",
		"docs/notes.md",
		"",
	} {
		if engineEntry(bad) {
			t.Errorf("engineEntry(%q) = true, want false", bad)
		}
	}
}

// A real GoReleaser archive from the fork wraps the binary in a
// per-platform directory. Extraction has to find it there; a check that
// only accepted a root-level entry passed every unit test and failed on
// the actual release.
func TestCopyEngineEntryPerPlatformWrapper(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{
		{"linux_amd64/sortie", "the engine"},
		{"linux_amd64/LICENSE", "license text"},
		{"linux_amd64/README.md", "readme"},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := copyEngineEntry(tar.NewReader(&buf), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "the engine" {
		t.Errorf("extracted %q, want the engine", out.String())
	}
}

// A hostile entry is skipped rather than winning the match, so a tampered
// archive cannot substitute its own binary for the one that was verified.
func TestCopyEngineEntryRejectsNestedPath(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// The hostile entries come first: with a basename test they would win.
	for _, f := range []struct{ name, body string }{
		{"../../sortie", "hostile"},
		{"a/b/sortie", "hostile"},
		{"sortie", "the real engine"},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := copyEngineEntry(tar.NewReader(&buf), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "the real engine" {
		t.Errorf("extracted %q, want the top-level engine", out.String())
	}
}

// An archive with no engine in it is an error naming what was missing, not
// a silent empty install.
func TestCopyEngineEntryRequiresEngine(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "bin/other", Mode: 0o755, Size: 2, Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	var out bytes.Buffer
	err := copyEngineEntry(tar.NewReader(&buf), &out)
	if err == nil || !strings.Contains(err.Error(), engineBinary) {
		t.Errorf("err = %v, want one naming %q", err, engineBinary)
	}
}

// SORTIE_BIN wins over every other source. setup resolves the engine the
// same way the run path does, so a user who set it keeps their own binary
// instead of having a second engine downloaded and linked beside it.
func TestResolveSortieBinPrefersEnv(t *testing.T) {
	mine := filepath.Join(t.TempDir(), "sortie")
	if err := os.WriteFile(mine, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SORTIE_BIN", mine)
	// Any download attempt fails loudly instead of falling through.
	orig := releaseBase
	releaseBase = func(_, _ string) string { return "http://127.0.0.1:1/none" }
	defer func() { releaseBase = orig }()

	got, err := resolveSortieBin()
	if err != nil {
		t.Fatalf("resolveSortieBin: %v", err)
	}
	if got != mine {
		t.Errorf("got %q, want %q", got, mine)
	}
}

// setup must resolve the engine the same way the run path does. It called
// ensureEngine directly, so a user who set SORTIE_BIN had a second engine
// downloaded and linked onto PATH beside the one they chose.
func TestSetupHonoursSortieBin(t *testing.T) {
	mine := filepath.Join(t.TempDir(), "sortie")
	if err := os.WriteFile(mine, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SORTIE_BIN", mine)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	// A download would reach for this and fail.
	orig := releaseBase
	releaseBase = func(_, _ string) string { return "http://127.0.0.1:1/none" }
	defer func() { releaseBase = orig }()

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
		t.Errorf("setup downloaded an engine despite SORTIE_BIN:\n%s", out)
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

// A release published by hand has no checksums.txt, so the error names the
// missing asset layout rather than leaving a bare 404 to interpret.
func TestEngineReleaseHintExplainsMissingChecksums(t *testing.T) {
	// Hermetic: a temp HOME so linkEngineOnPath cannot reach the real
	// ~/.local/bin, and an unroutable base so the failure is fast and
	// offline. The message names checksums.txt either way, which is what
	// the hint keys on.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	orig := releaseBase
	releaseBase = func(_, _ string) string { return "http://127.0.0.1:1/none" }
	defer func() { releaseBase = orig }()
	_, err := ensureEngine()
	if err == nil {
		t.Fatal("expected a failure with no release to fetch")
	}
	if !strings.Contains(err.Error(), "checksums.txt") {
		t.Errorf("err does not name the missing file: %v", err)
	}
	if !strings.Contains(err.Error(), "SORTIE_ENGINE_URL") {
		t.Errorf("err offers no way forward: %v", err)
	}
	if !strings.Contains(err.Error(), engineRepo()) {
		t.Errorf("err does not name the repo: %v", err)
	}
}

// A network failure is not a release-shape problem and must not get the
// hint attached.
func TestEngineReleaseHintIgnoresOtherErrors(t *testing.T) {
	if got := engineReleaseHint(errors.New("fetch https://example.invalid/x: dial tcp: no such host")); got != "" {
		t.Errorf("hint = %q, want empty for a network error", got)
	}
}

func TestEngineBindirPrefersLoopDirOnPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, ".local", "bin")

	t.Setenv("PATH", local)
	if dir, onPath := engineBindir(); dir != local || !onPath {
		t.Errorf("bindir = %q, onPath = %v; want %q, true", dir, onPath, local)
	}
	t.Setenv("PATH", "/nonexistent")
	if dir, onPath := engineBindir(); dir != local || onPath {
		t.Errorf("bindir = %q, onPath = %v; want %q, false", dir, onPath, local)
	}
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

// resolveSortieBin finds the engine on PATH, which is the same
// ~/.local/bin directory sortie-loop itself lives in. ensureSortieLink
// then computed dest == bin and rewrote the engine as a symlink to
// itself, leaving a 29MB binary that can never run. The engine must
// survive being next to the loop binary.
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
	t.Setenv("SORTIE_BIN", engine)
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
	t.Setenv("SORTIE_BIN", engine)

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

// install.sh honours --prefix, so the engine lands in <prefix>/share.
// Looking only under ~/.local made a complete custom-prefix install
// report "sortie binary not found" and left ensureSortieLink unable to
// create the sortie symlink.
func TestEnsureSortieBinFindsACustomPrefix(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "sortie-"+engineVersion())
	if err := os.WriteFile(engine, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ensureSortieBinIn([]string{filepath.Join(dir, "absent"), dir})
	if err != nil {
		t.Fatalf("ensureSortieBinIn: %v", err)
	}
	if got != engine {
		t.Errorf("got %q, want %q", got, engine)
	}
	// The error has to name what was searched, or the operator cannot tell
	// a missing engine from a wrongly guessed prefix.
	empty := t.TempDir()
	if _, err := ensureSortieBinIn([]string{empty}); err == nil {
		t.Error("expected an error when the directory is empty")
	} else if !strings.Contains(err.Error(), empty) {
		t.Errorf("error does not name the directory searched: %v", err)
	}
}

// A share directory derived from the running binary: <prefix>/bin/sortie-loop
// must resolve to <prefix>/share/sortie-loop.
func TestShareDirsFollowThePrefixOfTheRunningBinary(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(self)
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Dir(filepath.Dir(abs))
	want := filepath.Join(prefix, "share", "sortie-loop")
	if !slices.Contains(sortieShareDirs(), want) {
		t.Errorf("sortieShareDirs() = %v, want it to include %q", sortieShareDirs(), want)
	}
}

// Two engines on one machine is common enough during development, and the
// prefix is not self-consistent if a PATH hit wins: bin/sortie ends up
// pointing at a binary the prefix never installed.
func TestResolveSortieBinPrefersTheInstalledEngineOverPATH(t *testing.T) {
	prefix := t.TempDir()
	share := filepath.Join(prefix, "share", "sortie-loop")
	if err := os.MkdirAll(share, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(share, "sortie-"+engineVersion())
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A different engine, and a bin/sortie link to it, as an earlier
	// lookup would have left behind.
	other := t.TempDir()
	pathHit := filepath.Join(other, "sortie")
	if err := os.WriteFile(pathHit, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SORTIE_BIN", "")
	t.Setenv("PATH", other)
	// The precedence that matters: with a different engine on PATH, the one
	// this loop was installed with has to win.
	t.Setenv("SORTIE_BIN", "")
	if got, err := resolveSortieBinIn([]string{share}); err != nil || got != installed {
		t.Errorf("resolveSortieBinIn = %q, %v; want the installed engine %q", got, err, installed)
	}
	// A symlink is not an engine: following it would resolve back to the
	// PATH hit that created it, which is the whole failure this guards.
	link := filepath.Join(share, "sortie-"+engineVersion()+".link")
	if err := os.Symlink(pathHit, link); err != nil {
		t.Fatal(err)
	}
	os.Remove(installed)
	if got := installedSortieBinIn([]string{share}); got != "" {
		t.Errorf("installedSortieBin = %q, want \"\" when only a symlink is present", got)
	}
	_ = pathHit
}
