package host

import (
	"os"
	"path/filepath"
	"testing"
)

// The figure is a total over a tree, so every file has to be counted,
// including the ones in nested directories.
func TestDirSizeCountsTheWholeTree(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.bin"), 1<<20)
	one := DirSize(dir)
	if one < 1<<20 {
		t.Errorf("DirSize = %d, want at least the 1 MiB written", one)
	}
	// A file in a nested directory has to be counted too.
	write(t, filepath.Join(dir, "sub", "deeper", "b.bin"), 1<<20)
	if got := DirSize(dir); got < one+(1<<20) {
		t.Errorf("DirSize = %d after a nested file, want more than %d", got, one)
	}
}

// The figure is what the filesystem charged, so a file smaller than a
// block still costs a block. Summing file sizes would report this
// directory as a few hundred bytes and let a fleet of them look free.
func TestDirSizeCountsBlocksNotClaimedBytes(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "tiny"), 100)
	got := DirSize(dir)
	if got < 4096 {
		t.Errorf("DirSize = %d for a 100 byte file, want the block it occupies", got)
	}
	if got > 1<<20 {
		t.Errorf("DirSize = %d, want a single block, not a whole megabyte", got)
	}
}

// A link out of the tree must not pull in what it points at. Following it
// would either double count or, with a cycle, never finish.
func TestDirSizeDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "big.bin"), 1<<20)
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A fast symlink is stored in its inode and occupies no data block of
	// its own, so the only thing counted is the tree's own directory.
	// The megabyte behind the link must not appear.
	if got := DirSize(dir); got >= 1<<20 {
		t.Errorf("DirSize = %d, want the link's target left out of the tree", got)
	}
}

// An empty tree is measured, not unmeasured, so the page shows a figure
// rather than a dash for a repository that exists but holds nothing yet.
// The figure is the block the directory itself costs, which is what an
// empty directory does occupy.
func TestDirSizeOfAnEmptyTreeIsMeasured(t *testing.T) {
	if got := DirSize(t.TempDir()); got < 0 {
		t.Errorf("DirSize = %d, want a figure rather than unknown", got)
	}
}

// A path that is not there is reported as unmeasured, not as empty.
func TestDirSizeOfAMissingPathIsUnknown(t *testing.T) {
	if got := DirSize(filepath.Join(t.TempDir(), "absent")); got != -1 {
		t.Errorf("DirSize = %d, want -1", got)
	}
}

// One unreadable entry must not make the whole figure say nothing.
func TestDirSizeSkipsAnUnreadableEntry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "readable"), 4096)
	locked := filepath.Join(dir, "locked")
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	// Written before the directory is closed, since that is the only way
	// to get a populated directory the walk cannot enter.
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "hidden"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	// The readable file is counted and the locked subtree is skipped,
	// rather than the whole walk giving up and reporting nothing.
	got := DirSize(dir)
	if got < 4096 {
		t.Errorf("DirSize = %d, want the readable file counted", got)
	}
	if got >= 1<<20 {
		t.Errorf("DirSize = %d, want the unreadable subtree skipped", got)
	}
}

func write(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}
