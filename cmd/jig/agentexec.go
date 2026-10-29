package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/machine"
)

const exitNoAgent = 75

// runAgentExec is the agent command the supervisor sets for the engine.
// A fleet agent name runs that agent with its own env mapping; a
// runtime keeps the legacy account-pool behaviour. Either way output
// passes through unchanged.
func runAgentExec(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: jig agent-exec <agent|runtime> [args...]")
		return 2
	}
	name, childArgs := args[0], args[1:]
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(stderr, "jig agent-exec: "+format+"\n", a...)
		return code
	}

	statePath := os.Getenv("JIG_STATE_PATH")
	if statePath == "" {
		return fail(2, "JIG_STATE_PATH is not set; agent-exec runs through a Jigz loop")
	}
	state, err := machine.LoadState(statePath)
	if err != nil {
		return fail(2, "%v", err)
	}
	if code, ok := execFleetAgent(name, childArgs, stdin, stdout, stderr, fail, statePath); ok {
		return code
	}

	cfgPath, err := machine.DefaultPath()
	if err != nil {
		return fail(2, "%v", err)
	}
	cfg, err := machine.Load(cfgPath)
	if err != nil {
		return fail(2, "%v", err)
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintln(stderr, "jig agent-exec: warning:", w)
	}
	if !slices.Contains(machine.Runtimes, name) {
		return fail(2, "unknown runtime %q", name)
	}
	if !slices.ContainsFunc(cfg.Pool, func(e machine.Entry) bool { return e.Runtime == name }) {
		return fail(2, "no %s entry in the pool of %s", name, cfgPath)
	}
	now := time.Now()
	entry, ok := cfg.Pick(state, name, now)
	if !ok {
		if next := cfg.NextChange(state, now); !next.IsZero() {
			return fail(exitNoAgent, "no %s entry is available until %s", name, next.Format(time.RFC3339))
		}
		return fail(exitNoAgent, "no %s entry is available", name)
	}
	detect := func(line string, isStderr bool) (time.Time, string, bool) {
		return cfg.DetectLimit(entry.Runtime, line, isStderr, time.Now())
	}
	return spawnAgentChild(stdin, stdout, stderr, fail, entry.Command, childArgs,
		append(os.Environ(), entry.ExpandedEnv()...), detect, entry.Name, entry.Name, statePath, now)
}

// execFleetAgent runs a fleet agent from .jigz/config.yaml by name: the
// agent's runtime with its env mapping resolved from this process's
// environment. It reports whether the name was a fleet agent; anything
// else falls through to the runtime pool. An unset credential reference
// makes the agent unavailable.
func execFleetAgent(name string, childArgs []string, stdin io.Reader, stdout, stderr io.Writer, fail func(int, string, ...any) int, statePath string) (int, bool) {
	root := agentExecRoot()
	if root == "" {
		return 0, false
	}
	cfg, err := config.Load(root)
	if err != nil {
		return 0, false
	}
	a, ok := cfg.Agents[name]
	if !ok {
		return 0, false
	}
	if !slices.Contains(machine.Runtimes, a.Runtime) {
		return fail(2, "agent %s has unknown runtime %q", name, a.Runtime), true
	}
	var env []string
	for _, k := range sortedKeys(a.Env) {
		v := a.Env[k]
		if v.FromEnv != "" {
			if s := os.Getenv(v.FromEnv); s != "" {
				env = append(env, k+"="+s)
			} else {
				return fail(exitNoAgent, "agent %s needs %s, which is not set", name, v.FromEnv), true
			}
			continue
		}
		env = append(env, k+"="+v.Str)
	}
	// Usage-limit text is detected with the builtin patterns; a fleet
	// agent carries no custom patterns of its own.
	mine := &machine.Config{}
	detect := func(line string, isStderr bool) (time.Time, string, bool) {
		return mine.DetectLimit(a.Runtime, line, isStderr, time.Now())
	}
	return spawnAgentChild(stdin, stdout, stderr, fail, machine.DefaultCommand(a.Runtime), childArgs,
		append(os.Environ(), env...), detect, name, name, statePath, time.Now()), true
}

// agentExecRoot finds the repository the loop runs in: agent-exec runs
// with the repository root as its working directory, through a
// workspace below it, or anywhere below .jigz.
func agentExecRoot() string {
	if sp := os.Getenv("JIG_STATE_PATH"); sp != "" {
		// The state file lives at <root>/.jigz/data/state.json.
		if root := filepath.Dir(filepath.Dir(filepath.Dir(sp))); isRepoRoot(root) {
			return root
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if isRepoRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isRepoRoot(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".jigz", "config.yaml"))
	return err == nil && !fi.IsDir()
}

// spawnAgentChild runs the agent executable with the engine's arguments
// and environment, marks the entry used, and exhausts it when the
// output matches a usage-limit pattern. markName is the state key.
func spawnAgentChild(stdin io.Reader, stdout, stderr io.Writer, fail func(int, string, ...any) int, command string, childArgs, env []string, detect func(string, bool) (time.Time, string, bool), markName, displayName, statePath string, now time.Time) int {
	if err := machine.MarkUsed(statePath, markName, now); err != nil {
		fmt.Fprintln(stderr, "jig agent-exec: warning:", err)
	}

	path, err := exec.LookPath(command)
	if err != nil {
		return fail(127, "%v", err)
	}
	cmd := exec.Command(path, childArgs...)
	cmd.Env = env
	cmd.Stdin = stdin
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fail(127, "%v", err)
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return fail(127, "%v", err)
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return fail(127, "%v", err)
	}
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()

	var once sync.Once
	limited := false
	onLine := func(line string, isStderr bool) {
		until, reason, hit := detect(line, isStderr)
		if !hit {
			return
		}
		once.Do(func() {
			limited = true
			if err := machine.MarkExhausted(statePath, markName, until, reason); err != nil {
				fmt.Fprintln(stderr, "jig agent-exec: warning:", err)
			}
			fmt.Fprintf(stderr, "jig agent-exec: %s hit a usage limit; exhausted until %s\n", displayName, until.Format(time.RFC3339))
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyLines(outPipe, stdout, func(l string) { onLine(l, false) }) }()
	go func() { defer wg.Done(); copyLines(errPipe, stderr, func(l string) { onLine(l, true) }) }()
	wg.Wait()
	waitErr := cmd.Wait()

	code := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) {
			return fail(1, "%v", waitErr)
		}
		code = ee.ExitCode()
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
	}
	if limited && code == 0 {
		code = 1
	}
	return code
}

// copyLines writes each line to w as soon as it is read, byte for byte, and
// passes it to onLine without its line ending.
func copyLines(r io.Reader, w io.Writer, onLine func(string)) {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line)
			text := string(line)
			if text[len(text)-1] == '\n' {
				text = text[:len(text)-1]
			}
			onLine(text)
		}
		if err != nil {
			return
		}
	}
}
