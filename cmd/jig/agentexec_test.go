package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/machine"
)

func agentExecEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("JIG_STATE_PATH", filepath.Join(home, ".jigz", "data", "state.json"))
	if err := os.MkdirAll(filepath.Join(home, ".jigz", "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

// linkAs exposes a script under the name the runtime binary has, so
// LookPath finds the fake instead of the real CLI.
func linkAs(t *testing.T, script, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(script, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeAgentRepo writes a .jigz/config.yaml with the given agents.
func writeAgentRepo(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".jigz", "config.yaml"), []byte("repo: owner/repo\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAgentScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func agentExhaustedUntil(t *testing.T, name string) time.Time {
	t.Helper()
	st, err := machine.LoadState(os.Getenv("JIG_STATE_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	return st.Agents[name].ExhaustedUntil
}

func execAgentExec(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runAgentExec(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

// A fleet agent name runs that agent with its own env mapping: the
// runtime CLI from the runtime default, references resolved from the
// supervisor's environment.
func TestAgentExecAgentName(t *testing.T) {
	home := agentExecEnv(t)
	t.Setenv("AGENT_SUB", "sub-1")
	mustWriteConfig(t, home, `repo: owner/repo
agents:
  builder:
    runtime: claude-code
    env: { FROM_PARENT: { from_env: AGENT_SUB }, LITERAL: lit }
`)
	t.Setenv("JIG_STATE_PATH", filepath.Join(home, ".jigz", "data", "state.json"))
	t.Setenv("PATH", stubCommand(t, "claude", `printf 'args=%s\n' "$*"
printf 'sub=%s lit=%s\n' "$FROM_PARENT" "$LITERAL"
`)+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, out, errs := execAgentExec("builder", "say", "hi")
	if code != 0 || out != "args=say hi\nsub=sub-1 lit=lit\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

// A usage-limit line from the agent's CLI exhausts the agent by name,
// so the gate picks the next candidate.
func TestAgentExecAgentLimitExhausts(t *testing.T) {
	home := agentExecEnv(t)
	mustWriteConfig(t, home, `repo: owner/repo
agents:
  builder: { runtime: claude-code }
`)
	t.Setenv("JIG_STATE_PATH", filepath.Join(home, ".jigz", "data", "state.json"))
	epoch := time.Now().Add(2 * time.Hour).Unix()
	t.Setenv("PATH", stubCommand(t, "claude", fmt.Sprintf(`printf '{"type":"result","is_error":true,"result":"usage limit reached|%d"}\n'
`, epoch))+string(os.PathListSeparator)+os.Getenv("PATH"))
	if code, _, _ := execAgentExec("builder"); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if got := agentExhaustedUntil(t, "builder"); got.Unix() != epoch {
		t.Fatalf("exhausted until %v, want %d", got, epoch)
	}
}

// An unset credential reference makes the agent unavailable without
// running anything.
func TestAgentExecAgentUnresolvedEnv(t *testing.T) {
	home := agentExecEnv(t)
	mustWriteConfig(t, home, `repo: owner/repo
agents:
  builder: { runtime: claude-code, env: { TOK: { from_env: AGENT_MISSING } } }
`)
	t.Setenv("JIG_STATE_PATH", filepath.Join(home, ".jigz", "data", "state.json"))
	marker := filepath.Join(t.TempDir(), "ran")
	t.Setenv("PATH", stubCommand(t, "claude", "touch "+marker+"\n")+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, _, errs := execAgentExec("builder")
	if code != exitNoAgent || !strings.Contains(errs, "AGENT_MISSING") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child ran")
	}
}

// A name that is neither a fleet agent nor a runtime keeps the legacy
// error.
func TestAgentExecUnknownName(t *testing.T) {
	home := agentExecEnv(t)
	mustWriteConfig(t, home, "repo: owner/repo\n")
	t.Setenv("JIG_STATE_PATH", filepath.Join(home, ".jigz", "data", "state.json"))
	if code, _, errs := execAgentExec("nope"); code != 2 || !strings.Contains(errs, "nope") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func mustWriteConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".jigz", "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stubCommand(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAgentExecUsage(t *testing.T) {
	agentExecEnv(t)
	code, out, errs := execAgentExec()
	if code != 2 || out != "" || !strings.Contains(errs, "usage:") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestAgentExecUnknownAgent(t *testing.T) {
	home := agentExecEnv(t)
	writeAgentRepo(t, home, "agents:\n  builder: { runtime: claude-code, model: m }\n")
	if code, _, errs := execAgentExec("ghost"); code != 2 || !strings.Contains(errs, "unknown agent") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	// A runtime resolves to the first available fleet agent with that
	// runtime.
	script := writeAgentScript(t, "echo runtime-ok\n")
	t.Setenv("PATH", linkAs(t, script, "claude")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if code, out, _ := execAgentExec("claude-code"); code != 0 || out != "runtime-ok\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestAgentExecPassthrough(t *testing.T) {
	home := agentExecEnv(t)
	t.Setenv("FAKE_VAR", "parent")
	script := writeAgentScript(t, `printf 'args=%s\n' "$*"
printf 'cwd=%s\n' "$PWD"
printf 'var=%s\n' "$FAKE_VAR"
head -c 100000 /dev/zero | tr '\0' x
printf '\n'
printf 'last'
echo diag >&2
`)
	writeAgentRepo(t, home, `agents:
  a: { runtime: claude-code, model: m, env: { FAKE_VAR: child } }
`)
	t.Setenv("PATH", linkAs(t, script, "claude")+string(os.PathListSeparator)+os.Getenv("PATH"))
	wd, _ := os.Getwd()
	code, out, errs := execAgentExec("a", "--mode", "json", "x y")
	want := "args=--mode json x y\ncwd=" + wd + "\nvar=child\n" + strings.Repeat("x", 100000) + "\nlast"
	if code != 0 || out != want {
		t.Fatalf("code=%d out len=%d want len=%d", code, len(out), len(want))
	}
	if errs != "diag\n" {
		t.Fatalf("stderr=%q", errs)
	}
}

func TestAgentExecExitCode(t *testing.T) {
	home := agentExecEnv(t)
	writeAgentRepo(t, home, "agents:\n  a: { runtime: claude-code, model: m }\n")
	script := writeAgentScript(t, "exit 3\n")
	t.Setenv("PATH", linkAs(t, script, "claude")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if code, _, _ := execAgentExec("a"); code != 3 {
		t.Fatalf("code=%d", code)
	}
}

func TestAgentExecCommandNotFound(t *testing.T) {
	home := agentExecEnv(t)
	t.Setenv("JIG_TEST_MISSING", "x")
	writeAgentRepo(t, home, "agents:\n  a: { runtime: claude-code, model: m, env: { PATH: { from_env: JIG_TEST_MISSING } } }\n")
	// An empty PATH makes the runtime binary unresolvable.
	t.Setenv("PATH", "")
	if code, _, errs := execAgentExec("a"); code != 127 || errs == "" {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

// A usage limit exhausts the agent; the next call for the same agent
// holds instead of running. Failover to the standby happens in the
// supervisor, which picks the next candidate.
func TestAgentExecLimitExhaustsTheAgent(t *testing.T) {
	home := agentExecEnv(t)
	epoch := time.Now().Add(2 * time.Hour).Unix()
	script := writeAgentScript(t, fmt.Sprintf(`printf '{"type":"result","is_error":true,"result":"Claude AI usage limit reached|%d"}\n'
exit 1
`, epoch))
	writeAgentRepo(t, home, "agents:\n  one: { runtime: claude-code, model: m }\n")
	t.Setenv("PATH", linkAs(t, script, "claude")+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, out, errs := execAgentExec("one")
	if code != 1 || !strings.HasPrefix(out, `{"type":"result"`) {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if got := agentExhaustedUntil(t, "one"); got.Unix() != epoch {
		t.Fatalf("exhausted until %v, want %d", got, epoch)
	}
	if !strings.Contains(errs, "one hit a usage limit; exhausted until "+time.Unix(epoch, 0).Format(time.RFC3339)) {
		t.Fatalf("stderr=%q", errs)
	}

	code, out, errs = execAgentExec("one")
	if code != exitNoAgent || out != "" {
		t.Fatalf("exhausted agent ran: code=%d out=%q", code, out)
	}
}

// A runtime name runs the first available fleet agent with that
// runtime, skipping exhausted ones.
func TestAgentExecRuntimeSkipsExhaustedAgents(t *testing.T) {
	home := agentExecEnv(t)
	script := writeAgentScript(t, "echo ran-$AGENT_NAME\n")
	writeAgentRepo(t, home, "agents:\n  one: { runtime: claude-code, model: m, env: { AGENT_NAME: one } }\n  two: { runtime: claude-code, model: m, env: { AGENT_NAME: two } }\n")
	t.Setenv("PATH", linkAs(t, script, "claude")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := machine.MarkExhausted(os.Getenv("JIG_STATE_PATH"), "one", time.Now().Add(time.Hour), "limit"); err != nil {
		t.Fatal(err)
	}
	code, out, _ := execAgentExec("claude-code")
	if code != 0 || out != "ran-two\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestAgentExecPiLimitWithZeroExit(t *testing.T) {
	home := agentExecEnv(t)
	script := writeAgentScript(t, `printf '{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"insufficient_quota"}}\n'
`)
	writeAgentRepo(t, home, "agents:\n  p: { runtime: pi, model: sonnet }\n")
	t.Setenv("PATH", linkAs(t, script, "pi")+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, out, _ := execAgentExec("p")
	if code != 1 || !strings.Contains(out, "insufficient_quota") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if !agentExhaustedUntil(t, "p").After(time.Now()) {
		t.Fatal("agent is not exhausted")
	}
}

func TestAgentExecExhaustedAgentHolds(t *testing.T) {
	home := agentExecEnv(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := writeAgentScript(t, "touch "+marker+"\n")
	t.Setenv("PATH", linkAs(t, script, "claude")+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeAgentRepo(t, home, "agents:\n  a: { runtime: pi, model: sonnet }\n")
	p := os.Getenv("JIG_STATE_PATH")
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := machine.MarkExhausted(p, "a", until, "test"); err != nil {
		t.Fatal(err)
	}
	code, out, errs := execAgentExec("a")
	if code != exitNoAgent || out != "" || !strings.Contains(errs, "a") || !strings.Contains(errs, until.Format(time.RFC3339)) {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child ran")
	}
}

// A model the runtime does not know fails the run and exhausts the
// agent so the standby takes over.
func TestAgentExecModelUnavailableExhausts(t *testing.T) {
	home := agentExecEnv(t)
	script := writeAgentScript(t, `echo '{"type":"result","is_error":true,"result":"model not found: sonnet-x"}\n' >&2
exit 1
`)
	writeAgentRepo(t, home, "agents:\n  a: { runtime: claude-code, model: sonnet-x }\n")
	t.Setenv("PATH", linkAs(t, script, "claude")+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, _, errs := execAgentExec("a")
	if code != 1 || !strings.Contains(errs, "model unavailable") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	st, err := machine.LoadState(os.Getenv("JIG_STATE_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Agents["a"].ExhaustedUntil.IsZero() || !strings.Contains(st.Agents["a"].Reason, "model not found") {
		t.Errorf("agent not marked: %+v", st.Agents["a"])
	}
}

func TestAgentExecForwardsSIGTERM(t *testing.T) {
	home := agentExecEnv(t)
	script := writeAgentScript(t, `trap 'exit 42' TERM
echo ready
while :; do sleep 0.1; done
`)
	writeAgentRepo(t, home, "agents:\n  a: { runtime: pi, model: sonnet }\n")
	// The fake must be named after the runtime binary.
	t.Setenv("PATH", linkAs(t, script, "pi")+string(os.PathListSeparator)+os.Getenv("PATH"))
	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- runAgentExec([]string{"a"}, strings.NewReader(""), pw, io.Discard)
		pw.Close()
	}()
	buf := make([]byte, 6)
	if _, err := io.ReadFull(pr, buf); err != nil || string(buf) != "ready\n" {
		t.Fatalf("read %q: %v", buf, err)
	}
	go io.Copy(io.Discard, pr)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 42 {
			t.Fatalf("code=%d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child was not stopped")
	}
}
