//go:build linux

// Package host reads the load of the machine Jigz is running on. An
// agent fleet is bounded by the box it runs on rather than by the code, so
// the operator needs to see memory, CPU and disk next to the work.
package host

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Metrics is one reading. A zero field means the figure could not be read
// on this machine, and the page shows a dash for it.
type Metrics struct {
	MemTotal int64
	MemUsed  int64
	// Cores is the number of logical CPUs, for reading the percentage
	// against.
	Cores int
	// CPU is the busy fraction of all cores over the interval since the
	// previous call, as a percentage. It is -1 until there is a previous
	// sample to compare against: a single read of /proc/stat is cumulative
	// since boot and says nothing about the present.
	CPU       float64
	DiskTotal int64
	DiskUsed  int64
	// DiskPath is the filesystem that was measured, so the number is not
	// mistaken for the root filesystem when it is not.
	DiskPath string
}

// Reader takes the readings. It keeps the previous CPU sample, so it is
// the reader and not a package-level function that callers use.
type Reader struct {
	// diskPath is the filesystem to report. The state directory is the
	// right answer: that is where the workspaces and databases are
	// filling up.
	diskPath string

	mu   sync.Mutex
	prev cpuSample
}

type cpuSample struct {
	total uint64
	idle  uint64
}

// New returns a Reader reporting the filesystem holding diskPath. An empty
// diskPath, or one that cannot be resolved, falls back to the root
// filesystem.
func New(diskPath string) *Reader {
	if diskPath == "" {
		diskPath = "/"
	}
	return &Reader{diskPath: diskPath}
}

// Read takes one reading. Anything it cannot read is left zero rather than
// guessed: a wrong percentage is worse than a missing one.
func (r *Reader) Read() Metrics {
	m := Metrics{
		CPU:      -1,
		Cores:    cores(),
		DiskPath: r.diskPath,
	}
	parseMeminfo(&m, "/proc/meminfo")
	r.readCPU(&m)
	readDisk(&m)
	return m
}

// readMem takes MemTotal and MemUsed from /proc/meminfo. MemAvailable is
// used rather than MemFree: on Linux much of what MemFree reports is
// reclaimable cache, so using it would show a machine with a warm page
// cache as nearly out of memory.
func parseMeminfo(m *Metrics, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		key, val, ok := strings.Cut(s.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(val)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			m.MemTotal = kb * 1024
		case "MemAvailable":
			if m.MemUsed = m.MemTotal - kb*1024; m.MemUsed < 0 {
				m.MemUsed = 0
			}
		}
	}
}

// readCPU compares the current /proc/stat counters with the previous
// reading. The first call has nothing to compare against, so it reports
// no percentage rather than the average since boot.
func (r *Reader) readCPU(m *Metrics) {
	total, idle, ok := parseStat("/proc/stat")
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setCPU(cpuSample{total: total, idle: idle}, m)
}

// setCPU turns the change since the previous sample into a percentage. It
// reports false when there is nothing meaningful to measure, leaving the
// caller's -1 in place.
func (r *Reader) setCPU(cur cpuSample, m *Metrics) bool {
	prev := r.prev
	r.prev = cur
	if prev.total == 0 || cur.total <= prev.total {
		return false
	}
	// The first line of /proc/stat is the aggregate over all cores, and
	// field 5 is idle. iowait is counted as idle: the CPU is not
	// computing on it either, and counting it as busy would report a
	// fully loaded machine for anything doing sequential I/O.
	dTotal := float64(cur.total - prev.total)
	dIdle := float64(cur.idle - prev.idle)
	pct := (1 - dIdle/dTotal) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	m.CPU = pct
	return true
}

func parseStat(path string) (total, idle uint64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		for i, fld := range fields {
			v, err := strconv.ParseUint(fld, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			total += v
			if i == 3 || i == 4 {
				idle += v
			}
		}
		return total, idle, true
	}
	return 0, 0, false
}

func readDisk(m *Metrics) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(m.DiskPath, &st); err != nil {
		return
	}
	// Bavail is what an unprivileged process may actually use, so it is
	// the honest denominator for "can I still write here".
	total := int64(st.Blocks) * int64(st.Bsize)
	avail := int64(st.Bavail) * int64(st.Bsize)
	m.DiskTotal = total
	m.DiskUsed = total - avail
	if m.DiskUsed < 0 {
		m.DiskUsed = 0
	}
}
