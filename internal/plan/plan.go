// Package plan holds the small shared pieces around workflow files:
// locating the binaries that drive a repository, reading a tracker's
// kind, and interpreting engine validation results. Document
// generation lives in internal/gen; the manager starts and stops
// loops.
package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kinged007/jigz/internal/engine"
	config "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/machine"
	"gopkg.in/yaml.v3"
)

func frontMatter(lines []string) (int, int, error) {
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "---" {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, errors.New("no front matter (no leading --- fence)")
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return start, i, nil
		}
	}
	return 0, 0, errors.New("unterminated front matter")
}

// Diagnostics is one repository's validation result. Empty Errors and
// Warnings with Valid true means the repository is ready to start.
type Diagnostics struct {
	Repo     string   `json:"repo"`
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Plan materializes one repository. Every step is idempotent: running
// it twice leaves the same bytes, so it is safe to re-run on every
// start and from the UI's apply action.
type Plan struct {
	// LoopBin is the jig binary. Empty resolves it from PATH.
	LoopBin string
	// SortieBin is the engine binary. Empty resolves it from PATH.
	SortieBin string
}

// LoopBinary returns the jig binary to drive: the explicit
// setting, then JIG_BIN, then a binary beside this one, then
// PATH.
func (p Plan) LoopBinary() string {
	if p.LoopBin != "" {
		return p.LoopBin
	}
	if v := os.Getenv("JIG_BIN"); v != "" {
		return v
	}
	// A binary installed next to this one, which is what the installers
	// produce. Resolving by name alone would fail for anyone who ran
	// jig by absolute path from a shell whose PATH does not
	// include the prefix.
	if self, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(self), "jig")
		if fileExists(cand) {
			return cand
		}
	}
	return "jig"
}

// SortieBinPath exposes the resolved engine so a child process can be
// pinned to it.
func (p Plan) SortieBinPath() (string, error) { return p.sortieBin() }

// sortieBin is the explicit engine path when one is set, otherwise the
// engine Jigz itself resolves (engine.Resolve), with the share
// directory of the driven loop binary searched as well.
func (p Plan) sortieBin() (string, error) {
	if p.SortieBin != "" {
		return p.SortieBin, nil
	}
	bin, _, err := engine.Resolve(p.LoopBinary())
	return bin, err
}

// Issue is one finding of `sortie validate`. Check is the dotted path of
// the setting it concerns, such as config.dispatch.rules[0].agent.
type Issue struct {
	Check   string `json:"check"`
	Message string `json:"message"`
}

// IsRuntimeBlockWarning reports whether an engine warning only says that
// the workflow carries the adapter block of a runtime other than the active
// one. The shipped workflows hold one block per runtime and the engine reads
// only the block named by agent.kind.
func IsRuntimeBlockWarning(check, message string) bool {
	if check != "unknown_key" {
		return false
	}
	for _, rt := range machine.Runtimes {
		if message == fmt.Sprintf("%s: unknown top-level key %q", rt, rt) {
			return true
		}
	}
	return false
}

// WithoutRuntimeBlockWarnings drops the warnings IsRuntimeBlockWarning matches.
func (r FileResult) WithoutRuntimeBlockWarnings() FileResult {
	var keep []Issue
	for _, w := range r.Warnings {
		if !IsRuntimeBlockWarning(w.Check, w.Message) {
			keep = append(keep, w)
		}
	}
	r.Warnings = keep
	return r
}

// FileResult is the engine's verdict on one workflow file.
type FileResult struct {
	Valid    bool    `json:"valid"`
	Errors   []Issue `json:"errors"`
	Warnings []Issue `json:"warnings"`
}

// TrackerKind is tracker.kind from a workflow's front matter, "" when the
// file or the key is missing or unreadable.
func TrackerKind(r config.Resolved, workflow string) string {
	raw, err := os.ReadFile(filepath.Join(r.Dir, ".jigz", "workflows", "WORKFLOW."+workflow+".md"))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	start, end, err := frontMatter(lines)
	if err != nil {
		return ""
	}
	var doc struct {
		Tracker struct {
			Kind string `yaml:"kind"`
		} `yaml:"tracker"`
	}
	if yaml.Unmarshal([]byte(strings.Join(lines[start+1:end], "\n")), &doc) != nil {
		return ""
	}
	return doc.Tracker.Kind
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
