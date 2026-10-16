package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// supervisorLogPath is where a detached supervisor writes its stdout/stderr.
func supervisorLogPath(root string) string {
	return filepath.Join(root, ".sortie", "logs", "supervisor.log")
}

var (
	startExecutable = os.Executable
	startLauncher   = launchDetached
	// startReadyBudget is how long start waits for the child's status
	// document to report liveness "running".
	startReadyBudget = 15 * time.Second
)

// runStart launches a foreground sortie-loop run detached in the background.
// It refuses when a live run is already registered for the same root.
func runStart(c cmdline) {
	dir := c.dir
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fatal(err)
	}
	if e, err := runningEntry(abs); err == nil {
		fatal(fmt.Errorf("sortie-loop is already running for %s (pid %d); use `sortie-loop restart` or `sortie-loop status`", abs, e.PID))
	}
	argv := buildStartArgv(c)
	self, err := startExecutable()
	if err != nil {
		fatal(err)
	}
	logPath := supervisorLogPath(abs)
	pid, err := startLauncher(self, argv, logPath)
	if err != nil {
		fatal(fmt.Errorf("start %s: %w", abs, err))
	}
	if err := waitForStartReady(abs, pid, startReadyBudget); err != nil {
		fatal(fmt.Errorf("sortie-loop %s (pid %d) did not become ready: %w; see %s", abs, pid, err, logPath))
	}
	fmt.Printf("sortie-loop: %s started (pid %d), log %s\n", abs, pid, logPath)
}

func buildStartArgv(c cmdline) []string {
	var argv []string
	if c.unite {
		argv = append(argv, "--unite")
	}
	if c.noServer {
		argv = append(argv, "--no-server")
	}
	if c.noDashboard {
		argv = append(argv, "--no-dashboard")
	}
	if c.dashPort != 0 {
		argv = append(argv, fmt.Sprintf("--dashboard-port=%d", c.dashPort))
	}
	if c.dir != "" {
		argv = append(argv, c.dir)
	}
	return argv
}

func waitForStartReady(root string, pid int, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		doc, err := readStatusDoc(root)
		if err == nil && doc.PID == pid && statusLiveness(doc) == "running" {
			return nil
		}
		if err == nil && statusLiveness(doc) != "running" && !pidAlive(pid) {
			// Child died before publishing running; surface quickly.
			return fmt.Errorf("process exited")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Final check after deadline.
	if doc, err := readStatusDoc(root); err == nil && doc.PID == pid && statusLiveness(doc) == "running" {
		return nil
	}
	if !pidAlive(pid) {
		return fmt.Errorf("process exited")
	}
	return fmt.Errorf("timed out after %s", budget)
}

func launchDetached(self string, argv []string, logPath string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return 0, err
	}
	defer null.Close()
	cmd := exec.Command(self, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = null
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
