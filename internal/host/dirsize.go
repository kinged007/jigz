package host

import (
	"io/fs"
	"os"
	"path/filepath"
)

// DirSize is the space a directory tree occupies, in bytes.
//
// It sums the space the filesystem charged for, not the bytes the files
// claim: a small file still costs a whole block and a sparse one costs
// only what it wrote, so a sum of sizes is wrong in both directions. Directories are not counted, and symlinks are not followed,
// so a link out of the tree costs its own few bytes rather than whatever
// it points at and a cycle cannot make this loop.
//
// Directories are counted, because a directory costs a block of its own
// and the figure is then the same one du -s reports.
//
// A path that cannot be read reports -1 rather than 0, so a caller can
// tell "empty" from "not measured".
func DirSize(root string) int64 {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable entry is skipped rather than failing the whole
			// walk: one directory the user cannot read should not make
			// every figure say nothing.
			return nil //nolint:nilerr // the entry is skipped on purpose
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += diskBlocks(info)
		return nil
	})
	if err != nil {
		return -1
	}
	if _, err := os.Stat(root); err != nil {
		return -1
	}
	return total
}
