//go:build linux

package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The dashboard shows these as percentages, so the totals have to be real
// figures rather than zeros that render as a dash.
func TestReadReportsRealTotals(t *testing.T) {
	m := New(t.TempDir()).Read()
	if m.MemTotal <= 0 {
		t.Errorf("MemTotal = %d, want a positive total", m.MemTotal)
	}
	if m.MemUsed < 0 || m.MemUsed > m.MemTotal {
		t.Errorf("MemUsed = %d, want between 0 and %d", m.MemUsed, m.MemTotal)
	}
	if m.DiskTotal <= 0 {
		t.Errorf("DiskTotal = %d, want a positive total", m.DiskTotal)
	}
	if m.DiskUsed < 0 || m.DiskUsed > m.DiskTotal {
		t.Errorf("DiskUsed = %d, want between 0 and %d", m.DiskUsed, m.DiskTotal)
	}
	if m.Cores <= 0 {
		t.Errorf("Cores = %d, want at least one", m.Cores)
	}
}

// A second read compares against the first, so it produces a real
// percentage inside the possible range.
//
// The wait is not politeness: /proc/stat counts in clock ticks, so two
// reads a microsecond apart see the same total and there is genuinely no
// interval to measure. The arithmetic itself is proved deterministically
// by the setCPU tests below.
func TestTheSecondReadMeasuresCPUUse(t *testing.T) {
	r := New("/")
	r.Read()
	var m Metrics
	// A tick is 10ms at worst; allow several for a loaded machine.
	for i := 0; i < 20; i++ {
		time.Sleep(20 * time.Millisecond)
		if m = r.Read(); m.CPU >= 0 {
			break
		}
	}
	if m.CPU < 0 || m.CPU > 100 {
		t.Errorf("CPU = %v, want a percentage between 0 and 100", m.CPU)
	}
}

// Busy and idle are distinguished by the idle counters, and a machine
// that is doing nothing must not be reported as fully loaded.
func TestIdleCountersAreNotCountedAsBusy(t *testing.T) {
	// Two jiffies elapsed, all of them idle.
	prev := cpuSample{total: 100, idle: 90}
	cur := cpuSample{total: 102, idle: 100}
	m := Metrics{CPU: -1}
	r := &Reader{}
	r.prev = prev
	if !r.setCPU(cur, &m) {
		t.Fatal("setCPU declined a valid pair of samples")
	}
	if m.CPU != 0 {
		t.Errorf("CPU = %v for a fully idle interval, want 0", m.CPU)
	}
	// Two jiffies elapsed, one of them idle: half of one core's time.
	cur = cpuSample{total: 104, idle: 101}
	m = Metrics{CPU: -1}
	if !r.setCPU(cur, &m) {
		t.Fatal("setCPU declined a valid pair of samples")
	}
	if m.CPU < 40 || m.CPU > 60 {
		t.Errorf("CPU = %v for a half-busy interval, want about 50", m.CPU)
	}
}

// A counter that went backwards, which happens when /proc is read across
// a counter reset, would otherwise produce a nonsense percentage.
func TestCPUIsSkippedWhenCountersGoBackwards(t *testing.T) {
	r := &Reader{prev: cpuSample{total: 100, idle: 50}}
	m := Metrics{CPU: -1}
	if r.setCPU(cpuSample{total: 10, idle: 5}, &m) {
		t.Error("setCPU accepted a sample that went backwards")
	}
	if m.CPU != -1 {
		t.Errorf("CPU = %v, want it left at -1", m.CPU)
	}
}

// /proc/meminfo gives MemAvailable, which excludes reclaimable cache.
// Using MemFree instead would report a machine with a warm page cache as
// nearly out of memory, so the parser has to prefer it.
func TestMemAvailableIsPreferredOverMemFree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meminfo")
	// MemFree is a rounding error next to MemAvailable, which is the
	// realistic case on a machine with a warm page cache.
	body := "MemTotal:       16000000 kB\nMemFree:         100000 kB\nMemAvailable:   12000000 kB\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Metrics{}
	parseMeminfo(&m, path)
	if m.MemTotal != 16000000*1024 {
		t.Errorf("MemTotal = %d", m.MemTotal)
	}
	if want := int64(4000000 * 1024); m.MemUsed != want {
		t.Errorf("MemUsed = %d, want %d from MemAvailable, not MemFree", m.MemUsed, want)
	}
}

func TestMemInfoThatIsMissingLeavesTheFiguresAtZero(t *testing.T) {
	m := Metrics{}
	parseMeminfo(&m, filepath.Join(t.TempDir(), "absent"))
	if m.MemTotal != 0 || m.MemUsed != 0 {
		t.Errorf("an unreadable meminfo produced %+v, want zeros", m)
	}
}

// A filesystem that cannot be stat'd must leave disk at zero, so the page
// shows a dash rather than a plausible wrong number.
func TestUnreadableDiskLeavesTheFiguresAtZero(t *testing.T) {
	m := Metrics{DiskPath: filepath.Join(t.TempDir(), "not-a-mount")}
	readDisk(&m)
	if m.DiskTotal != 0 || m.DiskUsed != 0 {
		t.Errorf("an unreadable path produced %+v, want zeros", m)
	}
}

// The disk figure describes the filesystem holding the state directory,
// which is where the workspaces are filling up. An empty path falls back
// to the root rather than reporting nothing.
func TestDiskPathFallsBackToTheRoot(t *testing.T) {
	if got := New("").Read().DiskPath; got != "/" {
		t.Errorf("DiskPath = %q, want the root filesystem", got)
	}
}

// /proc/stat's first line is the aggregate; field 5 is iowait, which is
// not computing either.
func TestStatSplitsTheAggregateLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")
	body := "cpu  100 20 300 400 50 6 0 0 0 0\ncpu0 50 10 150 200 25 3 0 0 0 0\nintr 1 2 3\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	total, idle, ok := parseStat(path)
	if !ok {
		t.Fatal("readStatFrom declined a well-formed /proc/stat")
	}
	if want := uint64(100 + 20 + 300 + 400 + 50 + 6); total != want {
		t.Errorf("total = %d, want %d from the cpu line only", total, want)
	}
	if want := uint64(400 + 50); idle != want {
		t.Errorf("idle = %d, want %d counting idle and iowait", idle, want)
	}
}

func TestStatThatIsMissingIsRefused(t *testing.T) {
	if _, _, ok := parseStat(filepath.Join(t.TempDir(), "absent")); ok {
		t.Error("readStatFrom accepted a file that does not exist")
	}
}

func TestStatWithNoCPULineIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")
	if err := os.WriteFile(path, []byte("intr 1 2 3\nctxt 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := parseStat(path); ok {
		t.Error("readStatFrom accepted a file with no cpu line")
	}
}
