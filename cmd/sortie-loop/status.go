package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	statusDirName       = "status"
	statusSchemaVersion = 1
)

// statusDoc is the machine-readable state of one supervisor, written to
// the status file named by the registry entry. Later features add
// top-level sections without changing the fields below.
type statusDoc struct {
	SchemaVersion int              `json:"schema_version"`
	Version       string           `json:"version"`
	EngineVersion string           `json:"engine_version"`
	PID           int              `json:"pid"`
	Repo          string           `json:"repo"`
	RepoName      string           `json:"repo_name"`
	StartedAt     time.Time        `json:"started_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	State         string           `json:"state"`
	Workflows     []workflowStatus `json:"workflows"`
}

type workflowStatus struct {
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
}

// statusPath returns the status file for a repo name. A name of the
// form owner/name becomes a subdirectory.
func statusPath(repoName string) (string, error) {
	reg, err := registryPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(reg), statusDirName, repoName+".json"), nil
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
	path, err := statusPath(doc.RepoName)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func removeStatus(repoName string) {
	if path, err := statusPath(repoName); err == nil {
		_ = os.Remove(path)
	}
}

// configHash identifies the inputs a child was started with: the
// workflow file followed by the env file.
func configHash(workflow, env []byte) string {
	h := sha256.New()
	h.Write(workflow)
	h.Write(env)
	return hex.EncodeToString(h.Sum(nil))
}

// loopTypeOf reads the tracker kind from a workflow's front matter.
func loopTypeOf(workflow []byte) string {
	for _, line := range strings.Split(string(workflow), "\n") {
		if strings.TrimSpace(line) == "kind: github-pr" {
			return "prs"
		}
	}
	return "issues"
}
