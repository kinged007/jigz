//go:build acceptance

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/fakegithub"
)

// TestAcceptanceRunningSession checks that a session in its first turn is
// reported as running while the engine has no server and its database has
// no row for the run yet: the status document, the dashboard and the idle
// checks all rely on what the supervisor reads from the engine's log.
func TestAcceptanceRunningSession(t *testing.T) {
	engineBin := acceptanceEngine(t)
	loopBin := buildLoop(t)

	const (
		slug  = "acme/widgets"
		token = "acceptance-token"
	)
	gh := fakegithub.New("octo", token)
	gh.Logf = t.Logf
	gh.AddRepo(slug, labelNames(config.DefaultLabels)...)
	srv := httptest.NewServer(gh)
	defer srv.Close()
	planned := gh.Add(fakegithub.Item{Repo: slug, Title: "Plan this", Body: "Write a plan.",
		Labels: []string{"agent:plan"}, Assignees: []string{"octo"}})

	root := t.TempDir()
	home := filepath.Join(root, "home")
	repoDir := filepath.Join(root, "repo")
	writeAcceptanceRepo(t, repoDir, slug, token, srv.URL)
	wf := filepath.Join(repoDir, ".jigz", "workflows", "WORKFLOW.issues.md")
	raw, err := os.ReadFile(wf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wf, []byte(strings.Replace(string(raw), "turn_delay_ms: 500", "turn_delay_ms: 20000", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	dashPort := freePort(t)
	logPath := filepath.Join(root, "jig.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	env := append(isolatedEnv(home), "JIG_ENGINE_BIN="+engineBin, "JIG_ONLY=issues")
	cmd := exec.Command(loopBin, "--host", "127.0.0.1", "--dashboard-port", strconv.Itoa(dashPort), repoDir)
	cmd.Dir, cmd.Env = repoDir, env
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-exited
	}()
	run := &acceptanceRun{t: t, gh: gh, slug: slug, name: "Jigz", logPath: logPath, exited: exited, focus: planned}
	dash := fmt.Sprintf("http://127.0.0.1:%d", dashPort)

	status := func() (statusDoc, bool) {
		sc := exec.Command(loopBin, "status", "--json", repoDir)
		sc.Env = env
		out, err := sc.Output()
		var doc statusDoc
		return doc, err == nil && json.Unmarshal(out, &doc) == nil
	}
	sessions := func() int {
		doc, ok := status()
		if !ok || len(doc.Workflows) != 1 || !doc.Workflows[0].SessionsKnown {
			return -1
		}
		return len(doc.Workflows[0].Sessions)
	}

	run.waitFor("the session to appear in the status document", 90*time.Second, func() bool { return sessions() == 1 })
	doc, _ := status()
	if s := doc.Workflows[0].Sessions[0]; s.Identifier != fmt.Sprintf("%s#%d", slug, planned) || s.Attempt != 1 || s.StartedAt.IsZero() {
		t.Errorf("session %+v, want %s#%d, attempt 1", s, slug, planned)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(repoDir, ".jigz", "workflows", "jigz-issues.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var finished int
	if err := db.QueryRow(`SELECT COUNT(*) FROM run_history`).Scan(&finished); err != nil || finished != 0 {
		t.Errorf("run_history rows %d, %v; the test needs the run to be in its first turn", finished, err)
	}

	run.waitFor("the dashboard to count the running session", 60*time.Second, func() bool {
		var fleet struct{ Totals struct{ Running int } }
		return getJSON(dash+"/api/v1/fleet", &fleet) == nil && fleet.Totals.Running == 1
	})
	if !strings.Contains(getBody(t, dash+"/live"), fmt.Sprintf("%s#%d", slug, planned)) {
		t.Errorf("the live page does not list %s#%d", slug, planned)
	}

	run.waitFor("the session to end", 90*time.Second, func() bool { return sessions() == 0 })
	if err := db.QueryRow(`SELECT COUNT(*) FROM run_history WHERE status = 'succeeded'`).Scan(&finished); err != nil || finished != 1 {
		t.Errorf("succeeded runs %d, %v, want 1", finished, err)
	}
}
