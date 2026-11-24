package main

import (
	"strings"
	"testing"
)

// A CLI that prints its version on stderr is readable: no unusable block.
func TestAgentExecVersionOnStderr(t *testing.T) {
	code, _, e := execUnusableAgent(t, "opencode", "opencode", `echo "2.0.3" >&2`, "--version")
	if code != 0 || e.Unusable() {
		t.Fatalf("code=%d entry %+v", code, e)
	}
}

// The validate probe reads a version printed on stderr.
func TestAgentVersionOutputReadsStderr(t *testing.T) {
	script := writeAgentScript(t, `echo "2.0.3" >&2`+"\n")
	out, err := agentVersionOutput(script, nil)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(out, "2.0.3") {
		t.Fatalf("out %q misses the stderr version", out)
	}
}
