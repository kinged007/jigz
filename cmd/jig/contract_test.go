//go:build contract

package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/gen"
)

// TestContractEngineLogLiterals searches each engine binary for the log
// messages the live view depends on. A rename breaks the live view and
// history; the replay fixtures pin the current lines (section 7.4).
func TestContractEngineLogLiterals(t *testing.T) {
	for _, p := range gen.Profiles() {
		spec := engine.Spec{Repo: p.Repo, Tag: p.Version}
		bin := engine.Installed(engine.ShareDirs(), spec)
		if bin == "" {
			var err error
			if bin, err = engine.Fetch(spec); err != nil {
				t.Fatalf("no engine for %s: %v", p.EngineVersion(), err)
			}
		}
		raw, err := os.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		for _, lit := range engineLogLines {
			if !bytes.Contains(raw, []byte(lit)) {
				t.Errorf("%s: binary lacks %q", p.EngineVersion(), lit)
			}
		}
		for _, near := range []string{"turn complete", "worker exit", "session completed", "tick done", "turn finished"} {
			if bytes.Contains(raw, []byte(near)) {
				t.Logf("%s: near-miss literal present, not matched: %q", p.EngineVersion(), near)
			}
		}
	}
}
