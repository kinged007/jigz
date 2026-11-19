package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func rewriteStatus(t *testing.T, root string, edit func(*statusDoc)) {
	t.Helper()
	doc := readStatus(t, root)
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

// TestLoopSummaryReportsGateAndDrift checks the per-loop status line.
func TestLoopSummaryReportsGateAndDrift(t *testing.T) {
	w := workflowStatus{
		Name: "issues", Agent: "spare", Primary: "main",
		FallbackReason: "usage limit; main usable again unknown",
		OfflineReason:  "outside operating hours",
		Drift:          "line 3: disk \"x\" generated \"y\"\nline 4: disk \"x\" generated \"y\"",
	}
	got := loopSummary(w)
	for _, want := range []string{"agent: spare (primary main)", "offline: outside operating hours", "fallback: usage limit", "drift: line 3:"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q: %q", want, got)
		}
	}
	if loopSummary(workflowStatus{Name: "prs"}) != "" {
		t.Errorf("empty loop has a summary")
	}
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
	rewriteStatus(t, root, func(d *statusDoc) { d.PID = pid })
	if got := statusLivenessJSON(t, root); got != "stale" {
		t.Errorf("liveness = %q, want stale", got)
	}
}

func TestStatusCommandExited(t *testing.T) {
	_, root := startedSup(t, "while true; do sleep 1; done\n")
	pid := deadPID(t)
	rewriteStatus(t, root, func(d *statusDoc) { d.PID = pid; d.State = "exited" })
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
	path, _ := statusPath(root)
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

func TestStatusAfterFailedRunWithoutRegistryEntry(t *testing.T) {
	sup, root := startedSup(t, "exit 3\n")
	select {
	case ev := <-sup.events:
		if _, done := sup.onExit(ev); !done {
			t.Fatal("an unrequested exit did not end the run")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not exit")
	}
	sup.shutdown()
	sup.closeStatus()
	dropRegistry(root)
	out, err := statusOutput(root, true)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		State     string `json:"state"`
		Liveness  string `json:"liveness"`
		Workflows []struct {
			LastExitCode *int `json:"last_exit_code"`
		} `json:"workflows"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "exited" || got.Liveness != "exited" ||
		got.Workflows[0].LastExitCode == nil || *got.Workflows[0].LastExitCode != 3 {
		t.Errorf("unexpected status after a failed run: %s", out)
	}
}

func TestStatusPathIsPerRoot(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	a, err := statusPath("/work/a/repo")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := statusPath("/work/b/repo")
	again, _ := statusPath("/work/a/repo")
	if a == b || a != again {
		t.Errorf("paths a=%s b=%s again=%s", a, b, again)
	}
	if filepath.Dir(a) != filepath.Dir(b) || !regexp.MustCompile(`^[0-9a-f]{16}\.json$`).MatchString(filepath.Base(a)) {
		t.Errorf("unexpected path shape: %s", a)
	}
}
