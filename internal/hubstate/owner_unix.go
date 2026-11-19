//go:build !windows

package hubstate

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func checkOwner(path string, info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: owner unknown", path)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not by this user", path, st.Uid)
	}
	return nil
}
