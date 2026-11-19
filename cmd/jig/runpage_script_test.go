package main

import (
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

const pageMarkdown = "# Title\n\n- one\n- two\n\n1. first\n2. second\n\n" +
	"**bold** and `code` and [ok](https://example.com/x) and [bad](javascript:alert(1)) <script>window.pwned=1</script>\n\n" +
	"```go\nfmt.Println(1)\n```\n\n> quote\n\n---\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```\nstill open"

func transcriptLine(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return `{"type":"assistant","timestamp":"2026-01-01T10:00:07.000Z","message":{"role":"assistant","content":[{"type":"text","text":` + string(b) + `}]}}`
}

// TestRunPageScript runs the script of the Logs card in jsdom against a
// live dashboard. It is skipped when node or the jsdom package is not
// available; point NODE_PATH at a node_modules directory that has jsdom.
func TestRunPageScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if err := exec.Command(node, "-e", "require('jsdom')").Run(); err != nil {
		t.Skip("jsdom is not available to node")
	}
	r := newLiveRig(t, "")
	r.appendTranscript(t, transcriptLine(t, pageMarkdown))
	r.appendTranscript(t, transcriptLine(t, strings.Repeat("m", 6000)))
	srv := httptest.NewServer(r.a.routes(newHostGuard("127.0.0.1")))
	defer srv.Close()
	out, err := exec.Command(node, "testdata/runpage.js", srv.URL+"/runs/o/p/issues/live-7", r.transcript).CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatalf("page script check failed: %v", err)
	}
}
