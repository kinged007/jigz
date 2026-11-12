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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/machine"
)

const exitNoAgent = 75

// runAgentExec is the agent command the supervisor sets for the engine.
// A fleet agent name runs that agent with its own env mapping; a
// runtime runs the first available fleet agent with that runtime.
// Either way output passes through unchanged.
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
	var cfg *config.Config
	if root := agentExecRoot(); root != "" {
		if c, err := config.Load(root); err == nil {
			cfg = c
		}
	}
	if cfg == nil {
		return fail(2, "unknown agent %q: fleet agents live in .jigz/config.yaml", name)
	}
	if _, ok := cfg.Agents[name]; ok {
		return runAgentTarget(cfg, state, statePath, fail, name, childArgs, stdin, stdout, stderr)
	}
	now := time.Now()
	in := gateInputs{agents: cfg.Agents, refs: cfg.UnresolvedRefs(), state: state, operating: cfg.OperatingHours}
	found := false
	for _, aname := range sortedKeys(cfg.Agents) {
		if cfg.Agents[aname].Runtime != name {
			continue
		}
		found = true
		if in.block(aname, nil, now) != "" {
			continue
		}
		return runAgentTarget(cfg, state, statePath, fail, aname, childArgs, stdin, stdout, stderr)
	}
	if found {
		return fail(exitNoAgent, "no %s agent is available", name)
	}
	return fail(2, "unknown agent %q: fleet agents live in .jigz/config.yaml", name)
}

// runAgentTarget runs one fleet agent: its runtime with its env mapping
// resolved from this process's environment. An unset credential
// reference makes the agent unavailable.
func runAgentTarget(cfg *config.Config, state machine.State, statePath string, fail func(int, string, ...any) int, name string, childArgs []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a, ok := cfg.Agents[name]
	if !ok {
		return fail(2, "unknown agent %q", name)
	}
	if until := state.Agents[name].ExhaustedUntil; time.Now().Before(until) {
		// An unknown-model block records the model that failed; a config
		// change to another model clears it without waiting it out.
		if e := state.Agents[name]; e.ModelStale(a.Model) {
			e.ExhaustedUntil, e.Reason, e.Model = time.Time{}, "", ""
			if err := machine.UpdateState(statePath, func(st *machine.State) { st.Agents[name] = e }); err != nil {
				return fail(exitNoAgent, "agent %s is exhausted until %s", name, until.Format(time.RFC3339))
			}
			state.Agents[name] = e
		} else if e.Unusable() {
			return fail(exitNoAgent, "agent %s is unusable: %s", name, e.UnusableText())
		} else {
			return fail(exitNoAgent, "agent %s is exhausted until %s", name, until.Format(time.RFC3339))
		}
	}
	var env []string
	if wd, err := os.Getwd(); err == nil {
		env = append(env, "JIG_WORKSPACE="+wd)
	}
	for _, k := range sortedKeys(a.Env) {
		v := a.Env[k]
		if v.FromEnv != "" {
			if s := os.Getenv(v.FromEnv); s != "" {
				env = append(env, k+"="+s)
			} else {
				return fail(exitNoAgent, "agent %s needs %s, which is not set", name, v.FromEnv)
			}
			continue
		}
		env = append(env, k+"="+v.Str)
	}
	// Usage-limit text uses the machine file's limits block when it
	// loads; a fleet agent carries no custom patterns of its own.
	mine := &machine.Config{}
	if path, err := machine.DefaultPath(); err == nil {
		if c, err := machine.Load(path); err == nil {
			mine = c
		}
	}
	detect := func(line string, isStderr bool) (time.Time, string, bool) {
		return mine.DetectLimit(a.Runtime, line, isStderr, time.Now())
	}
	modelDetect := func(line string, isStderr bool) (string, bool) {
		return machine.IsModelError(a.Runtime, line, isStderr)
	}
	unusableDetect := func(line string, isStderr bool) (string, bool) {
		return machine.IsUnusable(a.Runtime, line, isStderr)
	}
	// The engine reads the version through the agent command before it
	// starts a session, so the check runs on that call.
	var versionProblem func(string) string
	if len(childArgs) == 1 && childArgs[0] == "--version" {
		versionProblem = func(out string) string { return machine.VersionProblem(a.Runtime, out) }
	}
	command := a.Command
	if command == "" {
		command = machine.DefaultCommand(a.Runtime)
	}
	return spawnAgentChild(stdin, stdout, stderr, fail, command, childArgs,
		append(os.Environ(), env...), detect, modelDetect, unusableDetect, versionProblem, name, name, a.Model, statePath, time.Now())
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

// spawnAgentChild runs the agent command, split on whitespace without a
// shell, with the engine's arguments
// and environment, marks the entry used, and exhausts it when the
// output matches a usage-limit pattern. It marks the agent unusable when
// the output shows a failure that no retry fixes, when the command is not
// installed, or when versionProblem (set for a --version call) refuses the
// version. markName is the state key.
func spawnAgentChild(stdin io.Reader, stdout, stderr io.Writer, fail func(int, string, ...any) int, command string, childArgs, env []string, detect func(string, bool) (time.Time, string, bool), modelDetect, unusableDetect func(string, bool) (string, bool), versionProblem func(string) string, markName, displayName, model, statePath string, now time.Time) int {
	if err := machine.MarkUsed(statePath, markName, now); err != nil {
		fmt.Fprintln(stderr, "jig agent-exec: warning:", err)
	}

	words := strings.Fields(command)
	if len(words) == 0 {
		return fail(127, "agent command is empty")
	}
	path, err := exec.LookPath(words[0])
	if err != nil {
		markUnusable(statePath, markName, displayName, "agent command not found: "+err.Error(), now, stderr)
		return fail(127, "%v", err)
	}
	cmd := exec.Command(path, append(words[1:], childArgs...)...)
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
	// An unknown model never recovers on its own: the cooldown is a
	// placeholder far enough out that only a config change clears it.
	modelCooldown := now.AddDate(100, 0, 0)
	modelAgent := model
	onLine := func(line string, isStderr bool) {
		if until, reason, hit := detect(line, isStderr); hit {
			once.Do(func() {
				limited = true
				if err := machine.MarkExhausted(statePath, markName, until, reason); err != nil {
					fmt.Fprintln(stderr, "jig agent-exec: warning:", err)
				}
				fmt.Fprintf(stderr, "jig agent-exec: %s hit a usage limit; exhausted until %s: %s\n", displayName, until.Format(time.RFC3339), reason)
			})
			return
		}
		if msg, ok := modelDetect(line, isStderr); ok {
			once.Do(func() {
				limited = true
				if err := machine.MarkModelExhausted(statePath, markName, modelCooldown, modelAgent, "model unavailable: "+msg); err != nil {
					fmt.Fprintln(stderr, "jig agent-exec: warning:", err)
				}
				fmt.Fprintf(stderr, "jig agent-exec: %s: model unavailable, exhausted until %s: %s\n", displayName, modelCooldown.Format(time.RFC3339), msg)
			})
			return
		}
		if msg, ok := unusableDetect(line, isStderr); ok {
			once.Do(func() {
				limited = true
				markUnusable(statePath, markName, displayName, msg, now, stderr)
			})
		}
	}
	var version strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyLines(outPipe, stdout, func(l string) {
			if versionProblem != nil && version.Len() < 4096 {
				version.WriteString(l + "\n")
			}
			onLine(l, false)
		})
	}()
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
	if versionProblem != nil && code == 0 && !limited {
		if msg := versionProblem(version.String()); msg != "" {
			markUnusable(statePath, markName, displayName, msg, now, stderr)
		}
	}
	if limited && code == 0 {
		code = 1
	}
	return code
}

func markUnusable(statePath, name, displayName, reason string, now time.Time, stderr io.Writer) {
	if err := machine.MarkUnusable(statePath, name, now, reason); err != nil {
		fmt.Fprintln(stderr, "jig agent-exec: warning:", err)
	}
	fmt.Fprintf(stderr, "jig agent-exec: %s is unusable until its block is cleared: %s\n", displayName, reason)
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
