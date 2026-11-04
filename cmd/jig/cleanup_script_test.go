package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncScriptsInstallsExecutable(t *testing.T) {
	root := t.TempDir()
	got := syncScripts(root)
	if got == "" {
		t.Fatal("syncScripts installed nothing")
	}
	fi, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", fi.Mode())
	}
	want, err := embeddedScripts.ReadFile("scripts/cleanup.sh")
	if err != nil {
		t.Fatal(err)
	}
	cur, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(cur) != string(want) {
		t.Fatal("installed script differs from the embedded copy")
	}
	if err := os.WriteFile(got, []byte("# edited\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if again := syncScripts(root); again != "" {
		t.Fatalf("syncScripts reinstalled an edited script: %q", again)
	}
	if cur, _ := os.ReadFile(got); string(cur) != "# edited\n" {
		t.Fatal("existing script was overwritten")
	}
}

func gitCheckout(t *testing.T, dir string, dirty bool) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "initial")
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v2-uncommitted"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
}

func runInstalledScript(t *testing.T, root string, dryRun string) (string, int) {
	t.Helper()
	script := syncScripts(root)
	if script == "" {
		script = filepath.Join(root, ".jigz", "scripts", "cleanup.sh")
	}
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(),
		"JIG_CLEANUP_WORKSPACE_ROOT="+filepath.Join(root, ".jigz", "workspaces"),
		"JIG_CLEANUP_MAX_AGE_DAYS=14",
		"JIG_CLEANUP_DRY_RUN="+dryRun,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if xe, ok := err.(*exec.ExitError); ok {
			code = xe.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return string(out), code
}

func TestCleanupScriptKeepsDirtyWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	ws := filepath.Join(root, ".jigz", "workspaces")
	oldClean := filepath.Join(ws, "issues", "old-clean")
	oldDirty := filepath.Join(ws, "issues", "old-dirty")
	newClean := filepath.Join(ws, "issues", "new-clean")
	plain := filepath.Join(ws, "prs", "plain-dir")
	gitCheckout(t, oldClean, false)
	gitCheckout(t, oldDirty, true)
	gitCheckout(t, newClean, false)
	if err := os.Chtimes(newClean, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(plain, old, old); err != nil {
		t.Fatal(err)
	}

	out, code := runInstalledScript(t, root, "0")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if _, err := os.Stat(oldClean); !os.IsNotExist(err) {
		t.Errorf("old clean checkout was not removed\n%s", out)
	}
	for _, kept := range []string{oldDirty, newClean, plain} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed, want kept\n%s", kept, out)
		}
	}
	for _, want := range []string{
		"removed: " + oldClean,
		"kept: " + oldDirty + " (dirty worktree)",
		"kept: " + plain + " (not a git checkout)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "removed: "+newClean) {
		t.Errorf("young checkout reported removed\n%s", out)
	}
}

func TestCleanupScriptDryRunRemovesNothing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	target := filepath.Join(root, ".jigz", "workspaces", "issues", "old-clean")
	gitCheckout(t, target, false)

	out, code := runInstalledScript(t, root, "1")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("dry run removed the checkout\n%s", out)
	}
	if !strings.Contains(out, "removed: "+target) {
		t.Errorf("dry run did not report the checkout\n%s", out)
	}
}
