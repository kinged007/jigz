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

func agentExecEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("JIG_STATE_PATH", filepath.Join(home, "state.json"))
}

func writeAgentScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeAgentMachine(t *testing.T, yaml string) {
	t.Helper()
	p, err := machine.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func agentPoolEntry(name, runtime, cmd string) string {
	return fmt.Sprintf("  - name: %s\n    runtime: %s\n    command: %s\n", name, runtime, cmd)
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

func TestAgentExecUsage(t *testing.T) {
	agentExecEnv(t)
	code, out, errs := execAgentExec()
	if code != 2 || out != "" || !strings.Contains(errs, "usage:") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestAgentExecUnknownRuntimeAndEmptyPool(t *testing.T) {
	agentExecEnv(t)
	if code, _, _ := execAgentExec("pi"); code != 2 {
		t.Fatalf("empty pool: code=%d", code)
	}
	writeAgentMachine(t, "pool:\n"+agentPoolEntry("a", "pi", "pi"))
	if code, _, errs := execAgentExec("nope"); code != 2 || !strings.Contains(errs, "nope") {
		t.Fatalf("unknown runtime: code=%d err=%q", code, errs)
	}
}

func TestAgentExecPassthrough(t *testing.T) {
	agentExecEnv(t)
	t.Setenv("FAKE_VAR", "parent")
	script := writeAgentScript(t, `printf 'args=%s\n' "$*"
printf 'cwd=%s\n' "$PWD"
printf 'var=%s\n' "$FAKE_VAR"
head -c 100000 /dev/zero | tr '\0' x
printf '\n'
printf 'last'
echo diag >&2
`)
	writeAgentMachine(t, "pool:\n"+agentPoolEntry("a", "pi", script)+"    env:\n      FAKE_VAR: child\n")
	wd, _ := os.Getwd()
	code, out, errs := execAgentExec("pi", "--mode", "json", "x y")
	want := "args=--mode json x y\ncwd=" + wd + "\nvar=child\n" + strings.Repeat("x", 100000) + "\nlast"
	if code != 0 || out != want {
		t.Fatalf("code=%d out len=%d want len=%d", code, len(out), len(want))
	}
	if errs != "diag\n" {
		t.Fatalf("stderr=%q", errs)
	}
}

func TestAgentExecExitCode(t *testing.T) {
	agentExecEnv(t)
	script := writeAgentScript(t, "exit 3\n")
	writeAgentMachine(t, "pool:\n"+agentPoolEntry("a", "pi", script))
	if code, _, _ := execAgentExec("pi"); code != 3 {
		t.Fatalf("code=%d", code)
	}
}

func TestAgentExecCommandNotFound(t *testing.T) {
	agentExecEnv(t)
	writeAgentMachine(t, "pool:\n"+agentPoolEntry("a", "pi", "/nonexistent/agent"))
	if code, _, errs := execAgentExec("pi"); code != 127 || errs == "" {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestAgentExecClaudeLimitMovesToNextEntry(t *testing.T) {
	agentExecEnv(t)
	epoch := time.Now().Add(2 * time.Hour).Unix()
	first := writeAgentScript(t, fmt.Sprintf(`printf '{"type":"result","is_error":true,"result":"Claude AI usage limit reached|%d"}\n'
exit 1
`, epoch))
	second := writeAgentScript(t, "echo second\n")
	writeAgentMachine(t, "pool:\n"+agentPoolEntry("one", "claude-code", first)+agentPoolEntry("two", "claude-code", second))

	code, out, errs := execAgentExec("claude-code")
	if code != 1 || !strings.HasPrefix(out, `{"type":"result"`) {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if got := agentExhaustedUntil(t, "one"); got.Unix() != epoch {
		t.Fatalf("exhausted until %v, want %d", got, epoch)
	}
	if !strings.Contains(errs, "one hit a usage limit; exhausted until "+time.Unix(epoch, 0).Format(time.RFC3339)) {
		t.Fatalf("stderr=%q", errs)
	}

	code, out, _ = execAgentExec("claude-code")
	if code != 0 || out != "second\n" {
		t.Fatalf("second: code=%d out=%q", code, out)
	}
}

func TestAgentExecPiLimitWithZeroExit(t *testing.T) {
	agentExecEnv(t)
	script := writeAgentScript(t, `printf '{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"insufficient_quota"}}\n'
`)
	writeAgentMachine(t, "pool:\n"+agentPoolEntry("p", "pi", script))
	code, out, _ := execAgentExec("pi")
	if code != 1 || !strings.Contains(out, "insufficient_quota") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if !agentExhaustedUntil(t, "p").After(time.Now()) {
		t.Fatal("entry is not exhausted")
	}
}

func TestAgentExecAllExhausted(t *testing.T) {
	agentExecEnv(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := writeAgentScript(t, "touch "+marker+"\n")
	writeAgentMachine(t, "pool:\n"+agentPoolEntry("a", "pi", script))
	p := os.Getenv("JIG_STATE_PATH")
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := machine.MarkExhausted(p, "a", until, "test"); err != nil {
		t.Fatal(err)
	}
	code, out, errs := execAgentExec("pi")
	if code != 75 || out != "" || !strings.Contains(errs, "pi") || !strings.Contains(errs, until.Format(time.RFC3339)) {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child ran")
	}
}

func TestAgentExecForwardsSIGTERM(t *testing.T) {
	agentExecEnv(t)
	script := writeAgentScript(t, `trap 'exit 42' TERM
echo ready
while :; do sleep 0.1; done
`)
	writeAgentMachine(t, "pool:\n"+agentPoolEntry("a", "pi", script))
	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- runAgentExec([]string{"pi"}, strings.NewReader(""), pw, io.Discard)
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
