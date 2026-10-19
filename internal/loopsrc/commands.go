package loopsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// WakeResult is the JSON of `sortie-loop wake --json`.
type WakeResult struct {
	Woke        bool   `json:"woke"`
	WasSleeping bool   `json:"was_sleeping"`
	Error       string `json:"error,omitempty"`
}

// CleanupResult is the JSON of `sortie-loop cleanup --json`.
type CleanupResult struct {
	StartedAt  time.Time `json:"started_at"`
	DurationMs int64     `json:"duration_ms"`
	ExitCode   int       `json:"exit_code"`
	DryRun     bool      `json:"dry_run"`
	Command    string    `json:"command"`
	OutputTail string    `json:"output_tail"`
	Error      string    `json:"error,omitempty"`
}

// Wake wakes a sleeping project. The result is returned with an error
// when the command failed, because the JSON still says why.
func (d StatusDoc) Wake(ctx context.Context, project string) (WakeResult, error) {
	root, err := d.root(project)
	if err != nil {
		return WakeResult{}, err
	}
	var res WakeResult
	err = d.run(ctx, orDefault(d.WakeTimeout, 15*time.Second), &res, "wake", "--json", root)
	if err != nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	return res, err
}

// Cleanup runs the project's cleanup command, or lists what it would
// remove when dryRun is set.
func (d StatusDoc) Cleanup(ctx context.Context, project string, dryRun bool) (CleanupResult, error) {
	root, err := d.root(project)
	if err != nil {
		return CleanupResult{}, err
	}
	args := []string{"cleanup"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	var res CleanupResult
	err = d.run(ctx, orDefault(d.CleanupTimeout, 10*time.Minute), &res, append(args, "--json", root)...)
	if err != nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	return res, err
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// run executes the sortie-loop binary and decodes its stdout into out
// even when the exit code is not zero, since failures are reported in
// the JSON. The returned error is nil only on exit 0.
func (d StatusDoc) run(ctx context.Context, timeout time.Duration, out any, args ...string) error {
	bin := d.Binary
	if bin == "" {
		bin = "sortie-loop"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	jsonErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), out)
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("sortie-loop %s: %w", args[0], ctx.Err())
	case runErr != nil:
		return fmt.Errorf("sortie-loop %s: %w", args[0], runErr)
	case jsonErr != nil:
		return fmt.Errorf("sortie-loop %s: unreadable output: %w", args[0], jsonErr)
	}
	return nil
}

const (
	tailBytes = 64 << 10
	maxLines  = 1000
)

// LogTail returns the last lines of a workflow's child log, at most
// maxLines. Only the last 64 KiB of the file is read. The path comes
// from the status document and must resolve inside the project's
// .sortie/logs directory; a missing file wraps os.ErrNotExist.
func (d StatusDoc) LogTail(project, workflow string, lines int) ([]string, error) {
	root, err := d.root(project)
	if err != nil {
		return nil, err
	}
	st, err := d.status(project, root)
	if err != nil {
		return nil, err
	}
	w, ok := st.Workflow(workflow)
	if !ok || w.LogPath == "" {
		return nil, fmt.Errorf("no log for workflow %q", workflow)
	}
	logs, err := filepath.EvalSymlinks(filepath.Join(root, ".sortie", "logs"))
	if err != nil {
		return nil, err
	}
	path, err := filepath.EvalSymlinks(w.LogPath)
	if err != nil {
		return nil, err
	}
	if rel, err := filepath.Rel(logs, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("log path of %q is outside the project's log directory", workflow)
	}
	return tailFile(path, min(max(lines, 1), maxLines))
}

func tailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	off := max(fi.Size()-tailBytes, 0)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, tailBytes))
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(buf), "\n")
	if off > 0 {
		// The first line is probably cut in half.
		if _, rest, ok := strings.Cut(text, "\n"); ok {
			text = rest
		} else {
			text = ""
		}
	}
	if text == "" {
		return []string{}, nil
	}
	all := strings.Split(text, "\n")
	return all[max(len(all)-n, 0):], nil
}

// Recycled reports whether a workflow's child was restarted onto a
// different configuration: the hash changed and the child restarted.
func Recycled(before, after WorkflowInfo) bool {
	return after.State == "running" && after.ConfigHash != "" && after.ConfigHash != before.ConfigHash &&
		(after.RestartCount > before.RestartCount || after.StartedAt.After(before.StartedAt))
}

// ErrNotRecycled is returned by WaitRecycled when the timeout ends first.
var ErrNotRecycled = errors.New("the workflow was not restarted onto a new configuration")

// WaitRecycled polls the status document until the workflow's child has
// been recycled relative to before, and returns the new document. On
// timeout or cancellation it returns the last document read with
// ErrNotRecycled.
func (d StatusDoc) WaitRecycled(ctx context.Context, project, workflow string, before StatusInfo, timeout time.Duration) (StatusInfo, error) {
	prev, ok := before.Workflow(workflow)
	if !ok {
		return before, fmt.Errorf("workflow %q was not in the status document before the apply", workflow)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(orDefault(d.PollEvery, 500*time.Millisecond))
	defer tick.Stop()
	last := before
	for {
		if st, err := d.Status(project); err == nil {
			last = st
			if w, ok := st.Workflow(workflow); ok && Recycled(prev, w) {
				return st, nil
			}
		}
		select {
		case <-ctx.Done():
			return last, ErrNotRecycled
		case <-tick.C:
		}
	}
}
