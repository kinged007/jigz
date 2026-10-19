//go:build !linux

// Package host reads the load of the machine central is running on. The
// figures come from /proc and sysinfo, which are Linux interfaces; on any
// other system every reading is reported as unavailable rather than
// approximated from something that does not mean the same thing.
package host

import "io/fs"

// Metrics is one reading. A zero field means the figure could not be read
// on this machine, and the page shows a dash for it.
type Metrics struct {
	MemTotal  int64
	MemUsed   int64
	Cores     int
	CPU       float64
	DiskTotal int64
	DiskUsed  int64
	DiskPath  string
}

// Reader takes the readings.
type Reader struct{}

// New returns a Reader. diskPath is ignored here.
func New(diskPath string) *Reader { return &Reader{} }

// diskBlocks falls back to the file's apparent size, which overstates a
// block-rounded file and misreports a sparse one. It is the portable
// answer, and this platform has no cheaper one.
func diskBlocks(fi fs.FileInfo) int64 { return fi.Size() }

// Read reports that nothing is available. CPU is -1, which is what the
// Linux build reports before it has a second sample, so the page renders
// the same way on both.
func (r *Reader) Read() Metrics { return Metrics{CPU: -1} }
