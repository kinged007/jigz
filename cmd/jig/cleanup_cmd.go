package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kinged007/jigz/internal/config"
)

// cleanupRecord is the last cleanup run, stored next to the status file
// and reported in the status document.
type cleanupRecord struct {
	LastRunAt  time.Time `json:"last_run_at"`
	DurationMs int64     `json:"duration_ms"`
	ExitCode   int       `json:"exit_code"`
	DryRun     bool      `json:"dry_run"`
	Paths      []string  `json:"paths"`
}

func writeCleanupRecord(root string, res cleanupResult) error {
	path, err := cleanupFilePath(root, ".cleanup.json")
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cleanupRecord{
		LastRunAt: res.StartedAt, DurationMs: res.DurationMs, ExitCode: res.ExitCode,
		DryRun: res.DryRun, Paths: res.Paths,
	}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// readCleanupRecord returns nil when no cleanup has run for the root.
func readCleanupRecord(root string) *cleanupRecord {
	path, err := cleanupFilePath(root, ".cleanup.json")
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec cleanupRecord
	if json.Unmarshal(data, &rec) != nil {
		return nil
	}
	if rec.Paths == nil {
		rec.Paths = []string{}
	}
	return &rec
}

// runCleanupRecorded runs the command and stores the result as the
// last run, unless ctx ended the run.
func runCleanupRecorded(ctx context.Context, cfg *config.Config, dryRun bool) (cleanupResult, error) {
	res, err := runCleanup(ctx, cfg, dryRun)
	if err == nil && ctx.Err() == nil {
		if werr := writeCleanupRecord(cfg.Dir, res); werr != nil {
			fmt.Fprintln(os.Stderr, "jig: cleanup record:", werr)
		}
	}
	return res, err
}

// runCleanupCmd is `jig cleanup [--dry-run] [--json] [repo-root]`.
func runCleanupCmd(args []string) {
	dir, dryRun, asJSON := ".", false, false
	for _, a := range args {
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--json":
			asJSON = true
		case strings.HasPrefix(a, "-"):
			fatal(fmt.Errorf("unknown flag %q", a))
		default:
			dir = a
		}
	}
	abs, err := absRoot(dir)
	if err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	out, err := cleanupOutput(ctx, abs, dryRun, asJSON)
	if out != "" {
		fmt.Println(out)
	}
	if err != nil {
		fatal(err)
	}
}

func cleanupOutput(ctx context.Context, root string, dryRun, asJSON bool) (string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return jsonError(err, asJSON), err
	}
	res, err := runCleanupRecorded(ctx, cfg, dryRun)
	if err != nil {
		return jsonError(err, asJSON), err
	}
	if res.ExitCode != 0 {
		err = fmt.Errorf("cleanup failed: %s", res.Error)
	}
	if asJSON {
		out, _ := json.MarshalIndent(res, "", "  ")
		return string(out), err
	}
	var b strings.Builder
	mode := ""
	if res.DryRun {
		mode = " (dry run)"
	}
	fmt.Fprintf(&b, "cleanup%s: exit %d in %dms, %d paths", mode, res.ExitCode, res.DurationMs, len(res.Paths))
	for _, p := range res.Paths {
		fmt.Fprintf(&b, "\n  %s", p)
	}
	if res.ExitCode != 0 {
		fmt.Fprintf(&b, "\n%s", strings.TrimRight(res.OutputTail, "\n"))
	}
	return b.String(), err
}

func jsonError(err error, asJSON bool) string {
	if !asJSON || err == nil {
		return ""
	}
	msg, _ := json.Marshal(map[string]string{"error": err.Error()})
	return string(msg)
}

func cleanupSummary(rec *cleanupRecord) string {
	if rec == nil {
		return ""
	}
	mode := ""
	if rec.DryRun {
		mode = ", dry run"
	}
	return fmt.Sprintf("cleanup: last run %s, exit %d, %dms, %d paths%s",
		rec.LastRunAt.Format(time.RFC3339), rec.ExitCode, rec.DurationMs, len(rec.Paths), mode)
}
