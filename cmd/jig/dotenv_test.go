package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeRepoEnv(t *testing.T, root, body string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, ".jigz", ".env"), body)
}

func TestRepoEnvReachesTheChildEnv(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "repo: owner/name\n")
	writeRepoEnv(t, root, "# mine\nJIG_TEST_FROM_FILE=file-value\nexport JIG_TEST_QUOTED=\"a b\"\n")
	os.Unsetenv("JIG_TEST_FROM_FILE")
	os.Unsetenv("JIG_TEST_QUOTED")
	t.Cleanup(func() { os.Unsetenv("JIG_TEST_FROM_FILE"); os.Unsetenv("JIG_TEST_QUOTED") })
	abs, err := absRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	sup := newTestSup(t, abs, "plan")
	_, env, _, err := sup.childEnv("plan")
	if err != nil {
		t.Fatal(err)
	}
	if got := lastEnv(env, "JIG_TEST_FROM_FILE"); got != "file-value" {
		t.Errorf("JIG_TEST_FROM_FILE = %q in the child env", got)
	}
	if got := lastEnv(env, "JIG_TEST_QUOTED"); got != "a b" {
		t.Errorf("JIG_TEST_QUOTED = %q in the child env", got)
	}
	if body, _ := os.ReadFile(filepath.Join(abs, ".jigz", ".env.loop")); len(body) == 0 {
		t.Error("the env file was not written")
	}
}

func TestTheEnvironmentWinsOverRepoEnv(t *testing.T) {
	root := t.TempDir()
	writeRepoEnv(t, root, "JIG_TEST_WINS=file\n")
	t.Setenv("JIG_TEST_WINS", "environment")
	if _, err := absRoot(root); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("JIG_TEST_WINS"); got != "environment" {
		t.Errorf("JIG_TEST_WINS = %q, want environment", got)
	}
}

func TestRepoEnvOfOneRootDoesNotLeakToAnother(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeRepoEnv(t, a, "JIG_TEST_A_ONLY=a\n")
	writeRepoEnv(t, b, "JIG_TEST_B_ONLY=b\n")
	os.Unsetenv("JIG_TEST_A_ONLY")
	os.Unsetenv("JIG_TEST_B_ONLY")
	t.Cleanup(func() { os.Unsetenv("JIG_TEST_A_ONLY"); os.Unsetenv("JIG_TEST_B_ONLY") })
	if _, err := absRoot(a); err != nil {
		t.Fatal(err)
	}
	if _, set := os.LookupEnv("JIG_TEST_B_ONLY"); set {
		t.Error("a variable of repo b is set after resolving only repo a")
	}
	if got := os.Getenv("JIG_TEST_A_ONLY"); got != "a" {
		t.Errorf("JIG_TEST_A_ONLY = %q", got)
	}
}

func TestMissingRepoEnvIsNotAnError(t *testing.T) {
	if _, err := absRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestRepoEnvIsGitignored(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	ensureGitignore(root)
	writeRepoEnv(t, root, "X=1\n")
	if err := exec.Command("git", "-C", root, "check-ignore", "-q", ".jigz/.env").Run(); err != nil {
		t.Errorf(".jigz/.env is not ignored: %v", err)
	}
}
