package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRotatingLogRotatesAtLimitAndKeepsThree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "plan.log")
	l, err := openRotatingLog(path, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"aaaaaa", "bbbbbb", "cccccc", "dddddd", "eeeeee"} {
		if _, err := l.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	_ = l.Close()
	want := map[string]string{"": "eeeeee", ".1": "dddddd", ".2": "cccccc", ".3": "bbbbbb"}
	for suffix, content := range want {
		if got := readFile(t, path+suffix); got != content {
			t.Errorf("%s%s = %q, want %q", filepath.Base(path), suffix, got, content)
		}
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Error("a fourth rotated file was kept")
	}
}

func TestRotatingLogAppendsAfterReopenAndRotatesOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.log")
	l, _ := openRotatingLog(path, 10, 3)
	_, _ = l.Write([]byte("one\n"))
	_ = l.Close()
	l, _ = openRotatingLog(path, 10, 3)
	_, _ = l.Write([]byte("two\n"))
	_ = l.Close()
	if got := readFile(t, path); got != "one\ntwo\n" {
		t.Fatalf("after reopen = %q", got)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 11)), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := openRotatingLog(path, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	if got := readFile(t, path+".1"); got != strings.Repeat("x", 11) {
		t.Errorf(".1 = %q", got)
	}
	if got := readFile(t, path); got != "" {
		t.Errorf("current = %q, want empty", got)
	}
}

func TestRotatingLogConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.log")
	l, _ := openRotatingLog(path, 1<<20, 3)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = l.Write([]byte("line\n"))
			}
		}()
	}
	wg.Wait()
	_ = l.Close()
	if got := strings.Count(readFile(t, path), "line\n"); got != 1600 {
		t.Errorf("%d lines, want 1600", got)
	}
}

func TestRotatingLogOpenFailsInUnwritableDir(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "logs")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := openRotatingLog(filepath.Join(blocker, "plan.log"), 10, 3); err == nil {
		t.Fatal("open succeeded under a file")
	}
}

func TestChildStartsWhenLogCannotBeOpened(t *testing.T) {
	root2 := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mustWrite(t, filepath.Join(root2, ".sortie", "config.yaml"), "repo: owner/name\n")
	mustWrite(t, filepath.Join(root2, ".sortie", "workflows", "WORKFLOW.plan.md"), "body\n")
	mustWrite(t, filepath.Join(root2, ".sortie", "logs"), "not a directory")
	s2 := newTestSup(t, root2, "plan")
	s2.ports["plan"] = 0
	s2.bin = writeScript(t, root2, "sortie", "while true; do sleep 1; done\n")
	if err := s2.startOne("plan"); err != nil {
		t.Fatalf("child failed to start without a log file: %v", err)
	}
	t.Cleanup(s2.shutdown)
}

func TestStatusReportsLogPath(t *testing.T) {
	sup, root := startedSup(t, "while true; do sleep 1; done\n")
	doc := sup.statusDoc("running")
	if len(doc.Workflows) != 1 {
		t.Fatalf("%d workflows", len(doc.Workflows))
	}
	want := filepath.Join(root, ".sortie", "logs", "plan.log")
	if doc.Workflows[0].LogPath != want {
		t.Errorf("log_path = %q, want %q", doc.Workflows[0].LogPath, want)
	}
}

func TestChildOutputReachesLogAndTerminal(t *testing.T) {
	term, err := os.CreateTemp(t.TempDir(), "term")
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = term, term
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mustWrite(t, filepath.Join(root, ".sortie", "config.yaml"), "repo: owner/name\n")
	mustWrite(t, filepath.Join(root, ".sortie", "workflows", "WORKFLOW.plan.md"), "body\n")
	sup := newTestSup(t, root, "plan")
	sup.ports["plan"] = 0
	sup.bin = writeScript(t, root, "sortie", "echo to-out\necho to-err >&2\n")
	if err := sup.startOne("plan"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.shutdown)
	for i := 0; i < 200; i++ {
		if out := readFile(t, term.Name()); strings.Contains(out, "to-out") && strings.Contains(out, "to-err") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	logged := readFile(t, childLogPath(root, "plan"))
	shown := readFile(t, term.Name())
	for _, want := range []string{"to-out", "to-err"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log lacks %q: %q", want, logged)
		}
		if !strings.Contains(shown, want) {
			t.Errorf("terminal lacks %q: %q", want, shown)
		}
	}
}
