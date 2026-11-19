package loopsrc

import "testing"

func TestLegacyReportsNoCapabilities(t *testing.T) {
	var s Source = Legacy{}
	if s.Name() != "legacy" {
		t.Errorf("name = %q, want legacy", s.Name())
	}
	if s.Capabilities() != (Capabilities{}) {
		t.Errorf("capabilities = %+v, want all false", s.Capabilities())
	}
}
