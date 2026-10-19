package host

import "testing"

// One read of /proc/stat is cumulative since boot, which says nothing
// about the present. Reporting that as a percentage would be a number
// that looks authoritative and means nothing, so the first read has to
// decline.
func TestTheFirstReadHasNoCPUPercentage(t *testing.T) {
	if m := New("/").Read(); m.CPU != -1 {
		t.Errorf("CPU = %v on the first read, want -1", m.CPU)
	}
}
