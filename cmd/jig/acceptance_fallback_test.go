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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/fakegithub"
	_ "modernc.org/sqlite"
)

// TestAcceptanceFallback runs one loop through a standby switch and back:
// the primary hits a usage limit on the credential check, the document is
// rewritten for the standby at idle, the run completes under the
// standby's model, and the primary returns after its reset passes.
func TestAcceptanceFallback(t *testing.T) {
	start := time.Now()
	engineBin := acceptanceEngine(t)
	loopBin := buildLoop(t)

	const (
		slug  = "acme/widgets"
		owner = "octo"
		token = "acceptance-token"
	)
	gh := fakegithub.New(owner, token)
	gh.Logf = t.Logf
	gh.AddRepo(slug, labelNames(config.DefaultLabels)...)
	srv := httptest.NewServer(gh)
	defer srv.Close()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	repoDir := filepath.Join(root, "repo")

	// The fake claude answers for the agent named in AGENT_NAME. The
	// primary limits once, then recovers; the standby always succeeds.
	reset := time.Now().Add(75 * time.Second).Truncate(time.Second)
	fakes := filepath.Join(root, "fakes")
	if err := os.MkdirAll(fakes, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
echo "$AGENT_NAME" >> %s/calls
if [ "$AGENT_NAME" = main ]; then
  echo '{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached|%d"}'
  exit 1
fi
echo '{"type":"system","subtype":"init","session_id":"fallback-1"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"All done.","usage":{"input_tokens":100,"output_tokens":50}}'
exit 0
`, fakes, reset.Unix())
	if err := os.WriteFile(filepath.Join(fakes, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := fmt.Sprintf(`repo: %s
token: %s
tracker:
  kind: github
  api_key: { from_env: SORTIE_TRACKER_API_KEY }
  project: %s
  in_progress_state: in-progress
  handoff_state: agent:done
agents:
  main: { runtime: claude-code, model: claude-sonnet-5-5, env: { AGENT_NAME: main } }
  spare: { runtime: claude-code, model: claude-haiku-5-5, env: { AGENT_NAME: spare } }
work:
  issues:
    query_filter: "label:agent:plan,in-progress -label:needs-human"
    polling_interval_ms: 1000
    hooks:
      after_create: "echo created > .after-create"
      before_run: "touch .before-run"
      after_run: "touch .after-run"
    agent: [main, spare]
    rules:
      - { name: plan, match: { labels: [agent:plan] }, prompt: prompts/build.md }
`, slug, token, slug)
	if err := os.MkdirAll(filepath.Join(repoDir, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".jigz", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	dashPort := freePort(t)
	logPath := filepath.Join(root, "jig.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	cmd := exec.Command(loopBin, "--host", "127.0.0.1", "--dashboard-port", strconv.Itoa(dashPort), repoDir)
	cmd.Dir = repoDir
	env := append(isolatedEnv(home),
		"JIG_ENGINE_BIN="+engineBin,
		"SORTIE_TRACKER_ENDPOINT="+srv.URL,
		"PATH="+fakes+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SORTIE_TRACKER_API_KEY="+token,
	)
	cmd.Env = env
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
	run := &acceptanceRun{t: t, gh: gh, slug: slug, name: "Jigz", logPath: logPath, exited: exited}
	waitFor, assertHandedOff := run.waitFor, run.assertHandedOff
	docPath := filepath.Join(repoDir, ".jigz", "workflows", "WORKFLOW.issues.md")
	status := func() statusDoc {
		t.Helper()
		sc := exec.Command(loopBin, "status", "--json", repoDir)
		sc.Env = env
		out, err := sc.Output()
		if err != nil {
			t.Fatalf("jig status: %v", err)
		}
		var doc statusDoc
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("status document: %v", err)
		}
		return doc
	}
	workflow := func() workflowStatus {
		for _, w := range status().Workflows {
			if w.Name == "issues" {
				return w
			}
		}
		t.Fatal("no issues loop in status")
		return workflowStatus{}
	}

	waitFor("the generated document", 60*time.Second, func() bool {
		raw, err := os.ReadFile(docPath)
		return err == nil && strings.Contains(string(raw), "jig:generated")
	})
	item := gh.Add(fakegithub.Item{Repo: slug, Title: "Fallback", Body: "Switch accounts.",
		Labels: []string{"agent:plan"}, Assignees: []string{owner}})
	run.focus = item

	waitFor("the document to switch to the standby", 90*time.Second, func() bool {
		raw, err := os.ReadFile(docPath)
		return err == nil && strings.Contains(string(raw), "agent-exec spare")
	})
	t.Logf("standby rewrite after %s", time.Since(start).Round(time.Second))

	log := assertHandedOff(t, item, 120*time.Second)
	if !slices.Contains(log, "-agent:plan") {
		t.Errorf("label events %v, want the plan trigger removed", log)
	}

	// The run after the switch shows the standby's model, and status
	// names the fallback.
	w := workflow()
	if w.Agent != "spare" || w.Primary != "main" || !strings.Contains(w.FallbackReason, "usage limit") {
		t.Errorf("status = agent %q primary %q fallback %q", w.Agent, w.Primary, w.FallbackReason)
	}
	// The run after the switch shows the standby's model: the dispatch
	// line carries the runtime-block model, and run history records the
	// CLI's own model next to the configured one.
	dbPath := filepath.Join(repoDir, ".jigz", "data", "jigz-issues.db")
	modelSeen := func() (dispatch, configured, reported string) {
		if raw, err := os.ReadFile(w.LogPath); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.Contains(line, "issue dispatched") || strings.Contains(line, "retried issue dispatched") {
					dispatch += line + "\n"
				}
			}
		}
		db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
		if err != nil {
			return dispatch, "", ""
		}
		defer db.Close()
		rows, err := db.Query(`SELECT configured_model FROM run_history`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var m string
				if err := rows.Scan(&m); err == nil && m != "" {
					configured += m + "\n"
				}
			}
		}
		rows2, err := db.Query(`SELECT model_name FROM session_metadata`)
		if err == nil {
			defer rows2.Close()
			for rows2.Next() {
				var m string
				if err := rows2.Scan(&m); err == nil && m != "" {
					reported += m + "\n"
				}
			}
		}
		return dispatch, configured, reported
	}
	waitFor("the standby model in the dispatch", 60*time.Second, func() bool {
		dispatch, _, _ := modelSeen()
		return strings.Contains(dispatch, "model=claude-haiku-5-5")
	})
	if dispatch, configured, reported := modelSeen(); !strings.Contains(dispatch, "model=claude-haiku-5-5") || !strings.Contains(configured, "claude-haiku-5-5") {
		t.Errorf("dispatches:\n%sconfigured models:\n%sreported models:\n%s", dispatch, configured, reported)
	}

	waitFor("the document to return to the primary", 150*time.Second, func() bool {
		raw, err := os.ReadFile(docPath)
		return err == nil && strings.Contains(string(raw), "agent-exec main")
	})
	t.Logf("primary return after %s", time.Since(start).Round(time.Second))
	if w := workflow(); w.Agent != "main" || w.FallbackReason != "" {
		t.Errorf("after return: agent %q fallback %q", w.Agent, w.FallbackReason)
	}
}
