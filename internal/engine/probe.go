package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Capabilities is what one engine binary reports about itself. No engine
// command lists adapter kinds, so they are read from the diagnostics of
// `sortie validate` on a workflow that names an unknown tracker and agent.
// Err is set when the binary could not be probed; the other fields are then
// empty and the capability set is unknown.
type Capabilities struct {
	Bin      string
	Version  string
	Trackers []string
	Agents   []string
	Err      string
}

// Known reports whether the probe succeeded.
func (c Capabilities) Known() bool { return c.Err == "" && c.Bin != "" }

func (c Capabilities) HasTracker(kind string) bool { return contains(c.Trackers, kind) }
func (c Capabilities) HasAgent(kind string) bool   { return contains(c.Agents, kind) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

const (
	probeKind    = "__probe__"
	probeTimeout = 20 * time.Second
	probeFlow    = `---
tracker:
  kind: ` + probeKind + `
  active_states: [open]
  terminal_states: [closed]
agent:
  kind: ` + probeKind + `
---
probe
`
)

var registeredRe = regexp.MustCompile(`registered: \[([^\]]*)\]`)

// parseRegistered returns the kinds in the `registered: [a, b]` suffix of an
// engine diagnostic, or nil when there is none.
func parseRegistered(message string) []string {
	m := registeredRe.FindStringSubmatch(message)
	if m == nil {
		return nil
	}
	var out []string
	for _, k := range strings.Split(m[1], ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// parseKinds reads the tracker and agent kinds from the output of
// `sortie validate --format json`.
func parseKinds(raw []byte) (trackers, agents []string, err error) {
	var res struct {
		Errors []struct {
			Check   string `json:"check"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, nil, fmt.Errorf("unreadable validate output: %w", err)
	}
	for _, e := range res.Errors {
		switch e.Check {
		case "tracker_adapter":
			trackers = parseRegistered(e.Message)
		case "agent_adapter":
			agents = parseRegistered(e.Message)
		}
	}
	if len(trackers) == 0 || len(agents) == 0 {
		return nil, nil, errors.New("validate output does not list the registered tracker and agent kinds")
	}
	return trackers, agents, nil
}

type probeKey struct {
	path  string
	mtime int64
	size  int64
}

var (
	probeMu    sync.Mutex
	probeCache = map[probeKey]Capabilities{}
)

// Probe asks the engine binary for its version and registered tracker and
// agent kinds. The result is cached per binary path, modification time and
// size for the life of the process.
func Probe(bin string) Capabilities {
	fi, err := os.Stat(bin)
	if err != nil {
		return Capabilities{Bin: bin, Err: err.Error()}
	}
	key := probeKey{bin, fi.ModTime().UnixNano(), fi.Size()}
	probeMu.Lock()
	defer probeMu.Unlock()
	if c, ok := probeCache[key]; ok {
		return c
	}
	c := probeBinary(bin)
	probeCache[key] = c
	return c
}

func probeBinary(bin string) Capabilities {
	c := Capabilities{Bin: bin}
	fail := func(err error) Capabilities {
		c.Err = err.Error()
		return c
	}
	out, err := runProbe(bin, "", "-dumpversion")
	if err != nil {
		return fail(fmt.Errorf("%s -dumpversion: %w", filepath.Base(bin), err))
	}
	c.Version = strings.TrimPrefix(strings.TrimSpace(firstLineOf(out)), "v")
	if c.Version == "" {
		return fail(errors.New("-dumpversion printed no version"))
	}
	dir, err := os.MkdirTemp("", "sortie-probe-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(file, []byte(probeFlow), 0o600); err != nil {
		return fail(err)
	}
	// validate exits non-zero for the unknown kinds; its JSON is on stdout.
	out, _ = runProbe(bin, dir, "validate", "--format", "json", file)
	if c.Trackers, c.Agents, err = parseKinds(out); err != nil {
		return fail(err)
	}
	return c
}

// runProbe runs the engine with the SORTIE_* variables removed, so a tracker
// or agent override in the caller's environment cannot change the probe.
func runProbe(bin, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SORTIE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	return out, err
}

func firstLineOf(b []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return line
}
