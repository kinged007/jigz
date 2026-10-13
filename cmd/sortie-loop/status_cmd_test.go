package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func rewriteStatus(t *testing.T, edit func(*statusDoc)) {
	t.Helper()
	doc := readStatus(t)
	edit(&doc)
	if err := writeStatus(doc); err != nil {
		t.Fatal(err)
	}
}

func statusLivenessJSON(t *testing.T, root string) string {
	t.Helper()
	out, err := statusOutput(root, true)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got["schema_version"] == nil {
		t.Errorf("document fields missing: %s", out)
	}
	return got["liveness"].(string)
}

func TestStatusCommandRunning(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	if got := statusLivenessJSON(t, root); got != "running" {
		t.Errorf("liveness = %q, want running", got)
	}
	out, err := statusOutput(root, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"owner/name", "plan:", "restarts 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

func TestStatusCommandStale(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	pid := deadPID(t)
	rewriteStatus(t, func(d *statusDoc) { d.PID = pid })
	if got := statusLivenessJSON(t, root); got != "stale" {
		t.Errorf("liveness = %q, want stale", got)
	}
}

func TestStatusCommandExited(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	pid := deadPID(t)
	rewriteStatus(t, func(d *statusDoc) { d.PID = pid; d.State = "exited" })
	if got := statusLivenessJSON(t, root); got != "exited" {
		t.Errorf("liveness = %q, want exited", got)
	}
}

func TestStatusCommandNoSupervisor(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, err := statusOutput(t.TempDir(), true); err == nil {
		t.Error("no error without a registered supervisor")
	}
}

func TestStatusCommandNoDocument(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	path, _ := statusPath("owner/name")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := statusOutput(root, false); err == nil {
		t.Error("no error without a status document")
	}
}

func TestParseArgsStatusIsASubcommand(t *testing.T) {
	c, err := parseArgs([]string{"status", "--json", "/some/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if c.remote != "status" || c.dir != "" || len(c.remoteArgs) != 2 {
		t.Errorf("status parsed as %+v", c)
	}
}
