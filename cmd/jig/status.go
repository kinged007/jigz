package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/rollup"
)

const (
	statusDirName       = "status"
	statusSchemaVersion = 1
)

// testedSchemaVersions lists the workflow DB schema_migrations versions
// whose layout this release was checked against.
var testedSchemaVersions = rollup.SupportedSchemas

// statusDoc is the machine-readable state of one supervisor, written to
// the status file derived from the repo root. Later features add
// top-level sections without changing the fields below.
type statusDoc struct {
	SchemaVersion        int              `json:"schema_version"`
	Version              string           `json:"version"`
	EngineVersion        string           `json:"engine_version"`
	TestedSchemaVersions []int            `json:"tested_schema_versions"`
	PID                  int              `json:"pid"`
	Repo                 string           `json:"repo"`
	RepoName             string           `json:"repo_name"`
	StartedAt            time.Time        `json:"started_at"`
	UpdatedAt            time.Time        `json:"updated_at"`
	State                string           `json:"state"`
	Workflows            []workflowStatus `json:"workflows"`
	Cleanup              *cleanupRecord   `json:"cleanup,omitempty"`
	Sleep                sleepStatus      `json:"sleep"`
}

type workflowStatus struct {
	Name         string        `json:"name"`
	WorkflowFile string        `json:"workflow_file"`
	LoopType     string        `json:"loop_type"`
	PID          int           `json:"pid"`
	State        string        `json:"state"`
	StartedAt    time.Time     `json:"started_at"`
	RestartCount int           `json:"restart_count"`
	LastExitCode *int          `json:"last_exit_code"`
	ConfigHash   string        `json:"config_hash"`
	LogPath      string        `json:"log_path"`
	Budget       *budgetStatus `json:"budget,omitempty"`
	// OfflineReason is why the gate keeps this loop down, empty while it
	// may run. ETA is its estimated start, absent without an estimate.
	// Agent is the agent the manager selected. Drift is the document
	// drift report; phase 3 fills it.
	OfflineReason string     `json:"offline_reason,omitempty"`
	ETA           *time.Time `json:"eta,omitempty"`
	Agent         string     `json:"agent,omitempty"`
	Drift         string     `json:"drift,omitempty"`
	// Primary is the first candidate agent. FallbackReason names the
	// trigger of the standby switch and when the primary is usable
	// again, set while the loop runs a non-primary agent.
	Primary        string `json:"primary,omitempty"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	// Sessions are the agent sessions running now. SessionsKnown is false
	// while the supervisor cannot account for every session of the engine.
	Sessions      []rollup.Session `json:"sessions"`
	SessionsKnown bool             `json:"sessions_known"`
}

// statusPath returns the status file for a repo root. The name is a
// hash of the root, so two checkouts of one repo never share a file and
// no repo name reaches the file system.
func statusPath(root string) (string, error) {
	reg, err := registryPath()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	name := hex.EncodeToString(sum[:])[:16] + ".json"
	return filepath.Join(filepath.Dir(reg), statusDirName, name), nil
}

// writeFileAtomic replaces path through a temp file in the same
// directory, so a reader never sees a partial file.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := firstErr(werr, cerr, os.Chmod(tmp.Name(), 0o644)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func writeStatus(doc statusDoc) error {
	path, err := statusPath(doc.Repo)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func removeStatus(root string) {
	if path, err := statusPath(root); err == nil {
		_ = os.Remove(path)
	}
}

// configHash identifies the inputs a child was started with: the
// workflow file, the env file and the query filter. Each part is
// length-prefixed so the boundaries are unambiguous.
func configHash(workflow, env []byte, filter string) string {
	h := sha256.New()
	for _, part := range [][]byte{workflow, env, []byte(filter)} {
		_ = binary.Write(h, binary.BigEndian, uint64(len(part)))
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loopTypeOf reads the tracker kind from a workflow's front matter,
// the block between the first two --- lines.
func loopTypeOf(workflow []byte) string {
	lines := strings.Split(string(workflow), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "issues"
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "---" {
			break
		}
		if val, ok := strings.CutPrefix(line, "kind:"); ok {
			val, _, _ = strings.Cut(val, " #")
			if strings.Trim(strings.TrimSpace(val), `"'`) == "github-pr" {
				return "prs"
			}
		}
	}
	return "issues"
}
