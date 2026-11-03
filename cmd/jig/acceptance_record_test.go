//go:build acceptance

package main

import (
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

// TestRecordEngineLog runs the real engine against fakegithub with a fake
// claude CLI and saves the child log for the livelog contract fixture.
// It only runs with RECORD_LOG=1, writing to the path in RECORD_LOG.
// Re-record per engine release: the replay test pins the curated shape,
// and the curator keeps one of each required line (section 7.4).
func TestRecordEngineLog(t *testing.T) {
	out := os.Getenv("RECORD_LOG")
	if out == "" {
		t.Skip("RECORD_LOG is not set")
	}
	engineBin := acceptanceEngine(t)
	loopBin := buildLoop(t)

	const (
		slug  = "acme/widgets"
		owner = "octo"
		token = "acceptance-token"
	)
	gh := fakegithub.New(owner, token)
	gh.AddRepo(slug, labelNames(config.DefaultLabels)...)
	srv := httptest.NewServer(gh)
	defer srv.Close()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	repoDir := filepath.Join(root, "repo")

	fakes := filepath.Join(root, "fakes")
	if err := os.MkdirAll(fakes, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"rec-1"}'
if [ ! -f %s/failed-once ]; then
  touch %s/failed-once
  echo '{"type":"result","subtype":"success","is_error":true,"result":"boom","usage":{"input_tokens":10,"output_tokens":5}}'
  exit 1
fi
sleep 5
echo '{"type":"result","subtype":"success","is_error":false,"result":"All done.","usage":{"input_tokens":100,"output_tokens":50}}'
exit 0
`, fakes, fakes)
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
  rec: { runtime: claude-code, model: claude-sonnet-5-5 }
work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    max_concurrent_agents: 2
    polling_interval_ms: 1000
    hooks:
      after_create: "echo created > .after-create"
      before_run: "touch .before-run"
      after_run: "touch .after-run"
    agent: rec
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
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
		"SORTIE_TRACKER_API_KEY="+token,
		"PATH="+fakes+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = logf, logf
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

	n1 := gh.Add(fakegithub.Item{Repo: slug, Title: "One", Body: "First.",
		Labels: []string{"agent:build"}, Assignees: []string{owner}})
	n2 := gh.Add(fakegithub.Item{Repo: slug, Title: "Two", Body: "Second.",
		Labels: []string{"agent:build"}, Assignees: []string{owner}})
	run.focus = n1
	run.waitFor("the child log", 60*time.Second, func() bool {
		raw, err := os.ReadFile(filepath.Join(repoDir, ".jigz", "logs", "issues.log"))
		return err == nil && strings.Contains(string(raw), "issue dispatched")
	})
	time.Sleep(75 * time.Second)
	raw, err := os.ReadFile(filepath.Join(repoDir, ".jigz", "logs", "issues.log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %d lines for #%d and #%d", len(strings.Split(string(raw), "\n")), n1, n2)
}
