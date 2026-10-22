package loopsrc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/sortie-loop/internal/rollup"
)

const supportedSchema = 1

var (
	// ErrNoStatus means no status document exists for the project: the
	// loops never ran, or exited cleanly.
	ErrNoStatus = errors.New("no status document")
	// ErrUnsupported means the document has a schema version central does
	// not read.
	ErrUnsupported = errors.New("unsupported status schema")
	// ErrUnknownProject means central has no repository by that name.
	ErrUnknownProject = errors.New("unknown project")
)

// Liveness says whether the supervisor that wrote a status document is
// still there. updated_at is not a heartbeat, so the pid decides.
type Liveness string

const (
	// Live: the document says running and its pid is alive.
	Live Liveness = "live"
	// Stale: the document says running but its pid is dead.
	Stale Liveness = "stale"
	// Exited: the supervisor recorded that it stopped; the run failed.
	Exited Liveness = "exited"
)

// SleepInfo is the sleep section of a status document.
type SleepInfo struct {
	Enabled       bool       `json:"enabled"`
	Awake         bool       `json:"awake"`
	SleepingSince *time.Time `json:"sleeping_since,omitempty"`
	NextWake      *time.Time `json:"next_wake,omitempty"`
	IdleSince     *time.Time `json:"idle_since,omitempty"`
}

// CleanupInfo is the last cleanup run.
type CleanupInfo struct {
	LastRunAt  time.Time `json:"last_run_at"`
	DurationMs int64     `json:"duration_ms"`
	ExitCode   int       `json:"exit_code"`
	DryRun     bool      `json:"dry_run"`
	Paths      []string  `json:"paths"`
}

// WorkflowInfo is one engine child as the supervisor reports it.
type WorkflowInfo struct {
	Name         string    `json:"name"`
	WorkflowFile string    `json:"workflow_file"`
	LoopType     string    `json:"loop_type"`
	Port         int       `json:"port"`
	PID          int       `json:"pid"`
	State        string    `json:"state"`
	StartedAt    time.Time `json:"started_at"`
	RestartCount int       `json:"restart_count"`
	LastExitCode *int      `json:"last_exit_code"`
	ConfigHash   string    `json:"config_hash"`
	LogPath      string    `json:"log_path"`
	// Budget is the loop's spend for the current period, absent when the
	// workflow configures none.
	Budget *BudgetInfo `json:"budget,omitempty"`
	// Hold is why the machine gate keeps this workflow down.
	Hold string `json:"hold,omitempty"`
}

// GateInfo is the machine gate of a status document, absent when the
// machine file sets neither working hours nor a pool.
type GateInfo struct {
	Open       bool       `json:"open"`
	HoursOpen  bool       `json:"hours_open"`
	Runtime    string     `json:"runtime"`
	NextChange *time.Time `json:"next_change"`
	Hold       string     `json:"hold"`
}

// BudgetInfo is one workflow's token budget as the status document reports
// it.
type BudgetInfo struct {
	Period         string `json:"period"`
	TokensUsed     int64  `json:"tokens_used"`
	Cap            int64  `json:"cap"`
	WarnPercent    int    `json:"warn_percent"`
	UnmeasuredRuns int    `json:"unmeasured_runs"`
	Paused         bool   `json:"paused"`
	PausedBy       string `json:"paused_by"`
	Override       bool   `json:"override"`
}

// Failed reports whether the child exited with a non-zero code, which
// includes -1 for a child killed by a signal. A child that has not
// exited has a null code and is not failed.
func (w WorkflowInfo) Failed() bool {
	return w.State == "exited" && w.LastExitCode != nil && *w.LastExitCode != 0
}

// StatusInfo is a project's status document with its liveness decided.
type StatusInfo struct {
	Project       string         `json:"project"`
	Version       string         `json:"version"`
	EngineVersion string         `json:"engine_version"`
	PID           int            `json:"pid"`
	StartedAt     time.Time      `json:"started_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	State         string         `json:"state"`
	Liveness      Liveness       `json:"liveness"`
	Sleeping      bool           `json:"sleeping"`
	Sleep         SleepInfo      `json:"sleep"`
	Cleanup       *CleanupInfo   `json:"cleanup,omitempty"`
	Workflows     []WorkflowInfo `json:"workflows"`
	Gate          *GateInfo      `json:"gate,omitempty"`
}

// Workflow returns the named workflow of the document.
func (s StatusInfo) Workflow(name string) (WorkflowInfo, bool) {
	for _, w := range s.Workflows {
		if w.Name == name {
			return w, true
		}
	}
	return WorkflowInfo{}, false
}

// FailedWorkflows returns the children that died on their own. A
// supervisor that is gone without a record has no children to judge,
// and while any child is stopping a stop was requested, so a non-zero
// exit may be the stop itself.
func (s StatusInfo) FailedWorkflows() []WorkflowInfo {
	if s.Liveness == Stale {
		return nil
	}
	var out []WorkflowInfo
	for _, w := range s.Workflows {
		if w.State == "stopping" {
			return nil
		}
		if w.Failed() {
			out = append(out, w)
		}
	}
	return out
}

type statusDocJSON struct {
	SchemaVersion int            `json:"schema_version"`
	Version       string         `json:"version"`
	EngineVersion string         `json:"engine_version"`
	PID           int            `json:"pid"`
	StartedAt     time.Time      `json:"started_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	State         string         `json:"state"`
	Workflows     []WorkflowInfo `json:"workflows"`
	Cleanup       *CleanupInfo   `json:"cleanup"`
	Sleep         SleepInfo      `json:"sleep"`
	Gate          *GateInfo      `json:"gate"`
}

// StatusDoc reads the status document each sortie-loop supervisor
// writes, and falls back to the registry (Legacy) for a project that has
// none or one central cannot read. It also drives the wake and cleanup
// commands.
type StatusDoc struct {
	// Reader supplies the legacy reading and the database and state
	// probes.
	Reader rollup.Reader
	// Roots returns each project's absolute repository root by project
	// name. The root string must equal the one sortie-loop was started
	// with, because the status file name is a hash of it.
	Roots func() map[string]string
	// Dir is the status directory; empty means
	// <user cache dir>/sortie-loop/status.
	Dir string
	// Binary is the sortie-loop binary to run for wake and cleanup.
	Binary string
	// WakeTimeout and CleanupTimeout bound the commands; zero uses
	// 15s and 10m.
	WakeTimeout, CleanupTimeout time.Duration
	// PollEvery is how often WaitRecycled re-reads the document; zero
	// uses 500ms.
	PollEvery time.Duration
}

func (StatusDoc) Name() string { return "status-doc" }

func (d StatusDoc) roots() map[string]string {
	if d.Roots == nil {
		return nil
	}
	return d.Roots()
}

func (d StatusDoc) dir() (string, error) {
	if d.Dir != "" {
		return d.Dir, nil
	}
	c, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "sortie-loop", "status"), nil
}

func (d StatusDoc) file(root, suffix string) (string, error) {
	dir, err := d.dir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, hex.EncodeToString(sum[:])[:16]+suffix), nil
}

func (d StatusDoc) root(project string) (string, error) {
	root, ok := d.roots()[project]
	if !ok || root == "" {
		return "", fmt.Errorf("%w: %s", ErrUnknownProject, project)
	}
	return root, nil
}

// Status reads and classifies a project's status document. The error
// wraps ErrNoStatus when the file is missing and ErrUnsupported when the
// schema version is not 1; a corrupt file gives a plain error.
func (d StatusDoc) Status(project string) (StatusInfo, error) {
	root, err := d.root(project)
	if err != nil {
		return StatusInfo{}, err
	}
	return d.status(project, root)
}

func (d StatusDoc) status(project, root string) (StatusInfo, error) {
	path, err := d.file(root, ".json")
	if err != nil {
		return StatusInfo{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return StatusInfo{}, fmt.Errorf("%w for %s", ErrNoStatus, project)
	}
	if err != nil {
		return StatusInfo{}, err
	}
	var doc statusDocJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return StatusInfo{}, fmt.Errorf("status document of %s: %w", project, err)
	}
	if doc.SchemaVersion != supportedSchema {
		return StatusInfo{}, fmt.Errorf("%w: version %d (central reads %d)", ErrUnsupported, doc.SchemaVersion, supportedSchema)
	}
	st := StatusInfo{
		Project: project, Version: doc.Version, EngineVersion: doc.EngineVersion, PID: doc.PID,
		StartedAt: doc.StartedAt, UpdatedAt: doc.UpdatedAt, State: doc.State, Sleep: doc.Sleep,
		Cleanup: doc.Cleanup, Workflows: doc.Workflows, Gate: doc.Gate,
	}
	if st.Workflows == nil {
		st.Workflows = []WorkflowInfo{}
	}
	switch {
	case doc.State == "exited":
		st.Liveness = Exited
	case pidAlive(doc.PID):
		st.Liveness = Live
	default:
		st.Liveness = Stale
	}
	st.Sleeping = st.Liveness == Live && doc.Sleep.Enabled && !doc.Sleep.Awake
	if rec := d.cleanupRecord(root); rec != nil && (st.Cleanup == nil || rec.LastRunAt.After(st.Cleanup.LastRunAt)) {
		st.Cleanup = rec
	}
	return st, nil
}

// cleanupRecord reads the sibling file a manual cleanup refreshes.
func (d StatusDoc) cleanupRecord(root string) *CleanupInfo {
	path, err := d.file(root, ".cleanup.json")
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec CleanupInfo
	if json.Unmarshal(raw, &rec) != nil {
		return nil
	}
	if rec.Paths == nil {
		rec.Paths = []string{}
	}
	return &rec
}

var full = Capabilities{Sleep: true, Wake: true, Cleanup: true, Logs: true, ConfigHash: true}

// ProjectCapabilities is what this project's loop reports: the status
// document capabilities when a readable document exists, none otherwise.
func (d StatusDoc) ProjectCapabilities(project string) Capabilities {
	if _, err := d.Status(project); err != nil {
		return Capabilities{}
	}
	return full
}

// Capabilities is the union over all projects, so a page that is not
// about one project can tell whether any control applies.
func (d StatusDoc) Capabilities() Capabilities {
	for p := range d.roots() {
		if _, err := d.Status(p); err == nil {
			return full
		}
	}
	return Capabilities{}
}

// Fleet reports each project with a readable status document from that
// document, and every other loop from the registry. Only a live,
// awake supervisor's running workflows are probed; a sleeping, stale or
// exited one is never contacted.
func (d StatusDoc) Fleet() rollup.Fleet {
	roots := d.roots()
	names := make([]string, 0, len(roots))
	for p := range roots {
		names = append(names, p)
	}
	sort.Strings(names)

	var mu sync.Mutex
	var wg sync.WaitGroup
	var loops []rollup.Loop
	var handled []string
	for _, p := range names {
		st, err := d.status(p, roots[p])
		if err != nil {
			continue
		}
		handled = append(handled, p)
		for _, w := range st.Workflows {
			wg.Add(1)
			go func() {
				defer wg.Done()
				probe := st.Liveness == Live && !st.Sleeping && w.State == "running" && w.Port > 0 && pidAlive(w.PID)
				l := d.Reader.Loop(p, w.Name, w.Port, dbPath(roots[p], w.WorkflowFile), probe)
				mu.Lock()
				loops = append(loops, l)
				mu.Unlock()
			}()
		}
	}
	rd := d.Reader
	rd.Exclude = append(append([]string(nil), rd.Exclude...), handled...)
	legacy := rd.Collect()
	wg.Wait()
	return rollup.Assemble(append(loops, legacy.Loops...))
}

// dbPath resolves a workflow's database like sortie-loop does: the
// db_path front-matter, relative to the workflows directory, else the
// default file there.
func dbPath(root, file string) string {
	dir := filepath.Join(root, ".sortie", "workflows")
	if raw, err := os.ReadFile(filepath.Join(dir, file)); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "db_path:"); ok {
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				if v == "" {
					break
				}
				if filepath.IsAbs(v) {
					return v
				}
				return filepath.Join(dir, v)
			}
		}
	}
	return filepath.Join(dir, ".sortie.db")
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
