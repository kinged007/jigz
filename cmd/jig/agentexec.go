package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/machine"
)

const exitNoAgent = 75

// runAgentExec is the agent command the supervisor sets for the engine. It
// runs the next available pool entry of the runtime and passes its output
// through unchanged.
func runAgentExec(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: jig agent-exec <runtime> [args...]")
		return 2
	}
	runtime, childArgs := args[0], args[1:]
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(stderr, "jig agent-exec: "+format+"\n", a...)
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
	if !slices.Contains(machine.Runtimes, runtime) {
		return fail(2, "unknown runtime %q", runtime)
	}
	if !slices.ContainsFunc(cfg.Pool, func(e machine.Entry) bool { return e.Runtime == runtime }) {
		return fail(2, "no %s entry in the pool of %s", runtime, cfgPath)
	}
	statePath := os.Getenv("JIG_STATE_PATH")
	if statePath == "" {
		return fail(2, "JIG_STATE_PATH is not set; agent-exec runs through a Jigz loop")
	}
	state, err := machine.LoadState(statePath)
	if err != nil {
		return fail(2, "%v", err)
	}
	now := time.Now()
	entry, ok := cfg.Pick(state, runtime, now)
	if !ok {
		if next := cfg.NextChange(state, now); !next.IsZero() {
			return fail(exitNoAgent, "no %s entry is available until %s", runtime, next.Format(time.RFC3339))
		}
		return fail(exitNoAgent, "no %s entry is available", runtime)
	}
	if err := machine.MarkUsed(statePath, entry.Name, now); err != nil {
		fmt.Fprintln(stderr, "jig agent-exec: warning:", err)
	}

	path, err := exec.LookPath(entry.Command)
	if err != nil {
		return fail(127, "%v", err)
	}
	cmd := exec.Command(path, childArgs...)
	cmd.Env = append(os.Environ(), entry.ExpandedEnv()...)
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
		until, reason, hit := cfg.DetectLimit(runtime, line, isStderr, time.Now())
		if !hit {
			return
		}
		once.Do(func() {
			limited = true
			if err := machine.MarkExhausted(statePath, entry.Name, until, reason); err != nil {
				fmt.Fprintln(stderr, "jig agent-exec: warning:", err)
			}
			fmt.Fprintf(stderr, "jig agent-exec: %s hit a usage limit; exhausted until %s\n", entry.Name, until.Format(time.RFC3339))
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
