//go:build linux

package host

import (
	"io/fs"
	"syscall"
)

// diskBlocks is the space a file actually occupies, which is what the
// filesystem charged for it. A file of a few bytes still costs a whole
// block, and a sparse file costs only the blocks it has written, so
// summing file sizes understates a directory in both directions.
func diskBlocks(fi fs.FileInfo) int64 {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fi.Size()
	}
	return st.Blocks * 512
}
