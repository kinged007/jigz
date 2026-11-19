package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/dirs"
)

const (
	startBudget    = 15 * time.Second
	startLogLines  = 20
	startPollEvery = 100 * time.Millisecond
)

// starter holds what start needs from the machine, so tests can replace it.
type starter struct {
	logPath func(root string) (string, error)
	running func(root string) (registryEntry, error)
	spawn   func(argv []string, logPath string) (pid int, exited <-chan error, err error)
	budget  time.Duration
	out     func(format string, a ...any)
}

func defaultStarter() starter {
	return starter{
		logPath: startLogPath,
		running: runningEntry,
		spawn:   spawnDetached,
		budget:  startBudget,
		out:     func(format string, a ...any) { fmt.Printf(format, a...) },
	}
}

func startLogPath(root string) (string, error) {
	dir, err := dirs.State()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, strings.ReplaceAll(repoName(root), "/", "-")+".log"), nil
}

// spawnDetached runs this binary with argv in its own session, with
// stdin on /dev/null and stdout and stderr appended to logPath.
func spawnDetached(argv []string, logPath string) (int, <-chan error, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, nil, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, nil, err
	}
	defer log.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return 0, nil, err
	}
	defer null.Close()
	cmd := exec.Command(self, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return cmd.Process.Pid, exited, nil
}

// startArgv is the bare-form command line for the background run.
func startArgv(c cmdline, root string) []string {
	var argv []string
	if c.noDashboard {
		argv = append(argv, "--no-dashboard")
	}
	if c.unite {
		argv = append(argv, "--unite")
	}
	if c.dashPort > 0 {
		argv = append(argv, "--dashboard-port="+strconv.Itoa(c.dashPort))
	}
	if c.host != "" {
		argv = append(argv, "--host="+c.host)
	}
	return append(argv, root)
}

func runStart(args []string) int {
	c, err := parseArgs(args)
	if errors.Is(err, errHelp) {
		usage()
		return 0
	}
	if err != nil {
		fatal(err)
	}
	if c.setup || c.remote != "" || c.showVersion || c.dumpVersion {
		fatal(errors.New("start takes only the run flags and a repo-root"))
	}
	dir := c.dir
	if dir == "" {
		dir = "."
	}
	abs, err := absRoot(dir)
	if err != nil {
		fatal(err)
	}
	if _, err := generateAll(abs); err != nil {
		fatal(err)
	}
	if err := defaultStarter().start(c, abs); err != nil {
		fatal(err)
	}
	return 0
}

func (s starter) start(c cmdline, root string) error {
	if e, err := s.running(root); err == nil {
		s.out("already running for %s (pid %d)\n", root, e.PID)
		if e.Dashboard != "" {
			s.out("dashboard %s\n", e.Dashboard)
		}
		s.out("status: jig status %s\nstop:   jig stop %s\n", root, root)
		return nil
	}
	logPath, err := s.logPath(root)
	if err != nil {
		return err
	}
	var offset int64
	if fi, err := os.Stat(logPath); err == nil {
		offset = fi.Size()
	}
	pid, exited, err := s.spawn(startArgv(c, root), logPath)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	deadline := time.Now().Add(s.budget)
	var registered registryEntry
	for done := false; !done; {
		select {
		case werr := <-exited:
			return fmt.Errorf("the run exited before it registered (%v)\n%s", werr, startLogTail(logPath, offset, startLogLines))
		default:
		}
		if e, err := s.running(root); err == nil && e.PID == pid {
			registered, done = e, true
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the run (pid %d) did not register within %s and is still running\n%s", pid, s.budget, startLogTail(logPath, offset, startLogLines))
		}
		time.Sleep(startPollEvery)
	}
	s.out("started %s (pid %d)\n", root, pid)
	if registered.Dashboard != "" {
		s.out("dashboard %s\n", registered.Dashboard)
	}
	s.out("log:    %s\nstatus: jig status %s\nstop:   jig stop %s\n", logPath, root, root)
	return nil
}

func logFrom(path string, offset int64) string {
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) < offset {
		return ""
	}
	return string(data[offset:])
}

func startLogTail(path string, offset int64, n int) string {
	text := strings.TrimRight(logFrom(path, offset), "\n")
	if text == "" {
		return "the log " + path + " has no output"
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "last log lines (" + path + "):\n" + strings.Join(lines, "\n")
}
