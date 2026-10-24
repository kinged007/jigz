//go:build !windows

package hubstate

import (
	"os"
	"syscall"
	"testing"
	"time"
)

type fakeInfo struct{ st *syscall.Stat_t }

func (fakeInfo) Name() string       { return "hub.json" }
func (fakeInfo) Size() int64        { return 0 }
func (fakeInfo) Mode() os.FileMode  { return 0o600 }
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any         { return f.st }

func TestCheckOwner(t *testing.T) {
	me := uint32(os.Getuid())
	if err := checkOwner("hub.json", fakeInfo{&syscall.Stat_t{Uid: me}}); err != nil {
		t.Errorf("own file refused: %v", err)
	}
	if err := checkOwner("hub.json", fakeInfo{&syscall.Stat_t{Uid: me + 1}}); err == nil {
		t.Error("a file of another uid accepted")
	}
}
