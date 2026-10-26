package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/config"
)

const (
	cleanupTailBytes  = 4096
	cleanupMaxPaths   = 200
	cleanupPathPrefix = "removed: "
)

var errCleanupBusy = errors.New("a cleanup run is already in progress")

type cleanupResult struct {
	StartedAt  time.Time `json:"started_at"`
	DurationMs int64     `json:"duration_ms"`
	ExitCode   int       `json:"exit_code"`
	DryRun     bool      `json:"dry_run"`
	Command    string    `json:"command"`
	OutputTail string    `json:"output_tail"`
	Error      string    `json:"error,omitempty"`
	Paths      []string  `json:"-"`
}

// cleanupCapture keeps the last cleanupTailBytes of the combined output
// and collects the paths the script reports on stdout.
type cleanupCapture struct {
	mu    sync.Mutex
	tail  []byte
	paths []string
	line  []byte
}

func (c *cleanupCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tail = append(c.tail, p...)
	if len(c.tail) > cleanupTailBytes {
		c.tail = c.tail[len(c.tail)-cleanupTailBytes:]
	}
	return len(p), nil
}

// stdout returns the writer for the script's stdout.
func (c *cleanupCapture) stdout() *pathWriter { return &pathWriter{c: c} }

type pathWriter struct{ c *cleanupCapture }

func (w *pathWriter) Write(p []byte) (int, error) {
	if _, err := w.c.Write(p); err != nil {
		return 0, err
	}
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	w.c.line = append(w.c.line, p...)
	for {
		i := bytes.IndexByte(w.c.line, '\n')
		if i < 0 {
			break
		}
		w.c.addPath(string(w.c.line[:i]))
		w.c.line = w.c.line[i+1:]
	}
	return len(p), nil
}

func (c *cleanupCapture) addPath(line string) {
	path, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), cleanupPathPrefix)
	if ok && path != "" && len(c.paths) < cleanupMaxPaths {
		c.paths = append(c.paths, path)
	}
}

// finish returns the output tail and paths, including an unterminated
// last stdout line.
func (c *cleanupCapture) finish() (string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.line) > 0 {
		c.addPath(string(c.line))
		c.line = nil
	}
	return string(c.tail), append([]string{}, c.paths...)
}

// cleanupLockPath is the lock file next to the status file, shared by
// the supervisor and the manual command so two runs never overlap.
func cleanupLockPath(root string) (string, error) {
	return cleanupFilePath(root, ".cleanup.lock")
}

func cleanupFilePath(root, suffix string) (string, error) {
	path, err := statusPath(root)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, ".json") + suffix, nil
}

func lockCleanup(root string) (*fileLock, error) {
	path, err := cleanupLockPath(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errCleanupBusy
		}
		return nil, err
	}
	return &fileLock{f: f}, nil
}

// runCleanup runs the configured command once through the shell, in its
// own process group, and kills the group when ctx ends or the timeout
// passes. It returns errCleanupBusy without running when another cleanup
// holds the lock.
func runCleanup(ctx context.Context, cfg *config.Config, dryRun bool) (cleanupResult, error) {
	res := cleanupResult{Command: cfg.Cleanup.Command, DryRun: dryRun || cfg.Cleanup.DryRun, Paths: []string{}}
	if res.Command == "" {
		return res, errors.New("no cleanup command configured (set cleanup.command in .jigz/config.yaml)")
	}
	lock, err := lockCleanup(cfg.Dir)
	if err != nil {
		return res, err
	}
	defer lock.release()

	ctx, cancel := context.WithTimeout(ctx, cfg.Cleanup.Timeout)
	defer cancel()
	dry := "0"
	if res.DryRun {
		dry = "1"
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", res.Command)
	cmd.Dir = cfg.Dir
	cmd.Env = append(os.Environ(),
		"JIG_CLEANUP_DRY_RUN="+dry,
		"JIG_CLEANUP_MAX_AGE_DAYS="+strconv.Itoa(cfg.Cleanup.MaxAgeDays),
		"JIG_CLEANUP_WORKSPACE_ROOT="+cfg.Workspaces,
		"JIG_CLEANUP_DB_DIR="+filepath.Join(cfg.Dir, ".jigz", "workflows"),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	capture := &cleanupCapture{}
	cmd.Stdout, cmd.Stderr = capture.stdout(), capture

	res.StartedAt = time.Now().UTC()
	runErr := cmd.Run()
	res.DurationMs = time.Since(res.StartedAt).Milliseconds()
	res.OutputTail, res.Paths = capture.finish()
	switch {
	case runErr == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ExitCode, res.Error = -1, fmt.Sprintf("timed out after %s", cfg.Cleanup.Timeout)
	case ctx.Err() != nil:
		res.ExitCode, res.Error = -1, "interrupted"
	default:
		res.ExitCode, res.Error = -1, runErr.Error()
		var xe *exec.ExitError
		if errors.As(runErr, &xe) {
			res.ExitCode = xe.ExitCode()
		}
	}
	return res, nil
}

func logCleanup(res cleanupResult) {
	mode := ""
	if res.DryRun {
		mode = " (dry run)"
	}
	fmt.Fprintf(os.Stderr, "jig: cleanup%s: started %s, took %dms, exit %d, %d paths\n",
		mode, res.StartedAt.Format(time.RFC3339), res.DurationMs, res.ExitCode, len(res.Paths))
	for _, p := range res.Paths {
		fmt.Fprintln(os.Stderr, "jig: cleanup: removed", p)
	}
	if res.ExitCode != 0 {
		fmt.Fprintf(os.Stderr, "jig: cleanup failed: %s\n%s\n", res.Error, strings.TrimRight(res.OutputTail, "\n"))
	}
}

// startCleanup runs the cleanup command every interval until shutdown.
// The first run comes one interval after start. A failed run is logged
// and does not stop the supervisor.
func (s *supervisor) startCleanup(cfg *config.Config) {
	ctx, cancel := context.WithCancel(context.Background())
	s.cleanupCancel = cancel
	s.cleanupWG.Add(1)
	go func() {
		defer s.cleanupWG.Done()
		tick := time.NewTicker(cfg.Cleanup.Interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			res, err := runCleanupRecorded(ctx, cfg, false)
			switch {
			case ctx.Err() != nil:
				return
			case errors.Is(err, errCleanupBusy):
				fmt.Fprintln(os.Stderr, "jig: cleanup skipped:", err)
			case err != nil:
				fmt.Fprintln(os.Stderr, "jig: cleanup:", err)
			default:
				logCleanup(res)
				s.publishStatus("running")
			}
		}
	}()
}

// stopCleanup cancels a running cleanup, killing its process group, and
// waits for the goroutine.
func (s *supervisor) stopCleanup() {
	if s.cleanupCancel != nil {
		s.cleanupCancel()
	}
	s.cleanupWG.Wait()
}
