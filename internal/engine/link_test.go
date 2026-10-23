package engine

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LinkAt owns the only symlink this tool writes. The directory is
// passed in so the test never reaches into the real home directory.
func TestLinkEngineAtReplacesStaleLink(t *testing.T) {
	dir := t.TempDir()
	// The cache location decides which links Jigz may refresh, so
	// the test points the real one inside the temp dir.
	t.Setenv("XDG_DATA_HOME", dir)
	bin := DataDir()
	src := filepath.Join(bin, "sortie-1.0.0")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	bindir := filepath.Join(dir, "bin")
	link := filepath.Join(bindir, "sortie")

	if got := LinkAt(src, bindir); got != link {
		t.Fatalf("link = %q, want %q", got, link)
	}
	if got, err := os.Readlink(link); err != nil || got != src {
		t.Fatalf("readlink = %q, %v; want %q", got, err, src)
	}
	// Re-linking to the same target is a no-op, not an error.
	if got := LinkAt(src, bindir); got != link {
		t.Errorf("idempotent link = %q, want %q", got, link)
	}
	// A version bump re-points the same link at the new binary.
	next := filepath.Join(bin, "sortie-2.0.0")
	if err := os.WriteFile(next, []byte("engine2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := LinkAt(next, bindir); got != link {
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
	src := filepath.Join(dir, "data", "jigz", "sortie-1.0.0")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := LinkAt(src, bindir); got != "" {
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
	src := filepath.Join(DataDir(), "sortie-1.0.0")
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
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
	if got := LinkAt(src, bindir); got != "" {
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
	if err == nil || !strings.Contains(err.Error(), Binary) {
		t.Errorf("err = %v, want one naming %q", err, Binary)
	}
}

// A release published by hand has no checksums.txt, so the error names the
// missing asset layout rather than leaving a bare 404 to interpret.
func TestEngineReleaseHintExplainsMissingChecksums(t *testing.T) {
	// Hermetic: a temp HOME so LinkOnPath cannot reach the real
	// ~/.local/bin, and an unroutable base so the failure is fast and
	// offline. The message names checksums.txt either way, which is what
	// the hint keys on.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	orig := releaseBase
	releaseBase = func(_, _ string) string { return "http://127.0.0.1:1/none" }
	defer func() { releaseBase = orig }()
	_, err := Ensure(Spec{Repo: "owner/engine", Tag: "v1.0.0"})
	if err == nil {
		t.Fatal("expected a failure with no release to fetch")
	}
	if !strings.Contains(err.Error(), "checksums.txt") {
		t.Errorf("err does not name the missing file: %v", err)
	}
	if !strings.Contains(err.Error(), "JIG_ENGINE_URL") {
		t.Errorf("err offers no way forward: %v", err)
	}
	if !strings.Contains(err.Error(), "owner/engine") {
		t.Errorf("err does not name the repo: %v", err)
	}
}

// A network failure is not a release-shape problem and must not get the
// hint attached.
func TestEngineReleaseHintIgnoresOtherErrors(t *testing.T) {
	if got := releaseHint(Spec{"o/r", "v1.0.0"}, errors.New("fetch https://example.invalid/x: dial tcp: no such host")); got != "" {
		t.Errorf("hint = %q, want empty for a network error", got)
	}
}

func TestEngineBindirPrefersLoopDirOnPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, ".local", "bin")

	t.Setenv("PATH", local)
	if dir, onPath := bindir(); dir != local || !onPath {
		t.Errorf("bindir = %q, onPath = %v; want %q, true", dir, onPath, local)
	}
	t.Setenv("PATH", "/nonexistent")
	if dir, onPath := bindir(); dir != local || onPath {
		t.Errorf("bindir = %q, onPath = %v; want %q, false", dir, onPath, local)
	}
}
