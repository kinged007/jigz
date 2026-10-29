package plan

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/engine"
	config "github.com/kinged007/jigz/internal/fleetconfig"
)

func resolved(t *testing.T, dir string, mutate ...func(*config.Resolved)) config.Resolved {
	t.Helper()
	r := config.Resolved{Repo: config.Repo{Repo: "owner/alpha"}, Dir: dir}
	for _, m := range mutate {
		m(&r)
	}
	return r
}

func workflows(t *testing.T, dir string, names ...string) {
	t.Helper()
	w := filepath.Join(dir, ".jigz", "workflows")
	if err := os.MkdirAll(w, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		// Shaped like a shipped workflow: a polling: block, an agent:
		// block, and an adapter block carrying the model.
		body := "---\npolling:\n  interval_ms: 60000\n\nagent:\n  kind: claude-code\n  max_turns: 5\n  max_concurrent_agents: 1\n\nclaude-code:\n  model: \"\"\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(w, "WORKFLOW."+n+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// config.yaml is an overlay: only keys Jigz holds may appear, so
func isolateEngineEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []string{"JIG_ENGINE_BIN", "JIG_ENGINE_REPO", "JIG_ENGINE_VERSION", "JIG_ENGINE_URL", "JIG_ENGINE_SHA256"} {
		t.Setenv(k, "")
	}
	t.Setenv("PATH", "/nonexistent")
}

// A clean install puts the engine in <prefix>/share/jigz, not on
// PATH, because install.sh honours --prefix. The plan must find the same
// file engine.Resolve finds, including the one for a configured
// repository and version.
func TestSortieBinIsTheEngineResolverResult(t *testing.T) {
	isolateEngineEnv(t)
	t.Setenv("JIG_ENGINE_REPO", "sortie-ai/sortie")
	t.Setenv("JIG_ENGINE_VERSION", "v1.29.0")
	prefix := t.TempDir()
	binDir := filepath.Join(prefix, "bin")
	share := filepath.Join(prefix, "share", "jigz")
	for _, d := range []string{binDir, share} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	loop := filepath.Join(binDir, "jig")
	if err := os.WriteFile(loop, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec, err := engine.Configured()
	if err != nil {
		t.Fatal(err)
	}
	want := spec.CachePath(share)
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := engine.Spec{Repo: "kinged007/sortie", Tag: "v1.29.0"}
	if err := os.WriteFile(other.CachePath(share), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := Plan{LoopBin: loop}
	got, err := p.sortieBin()
	if err != nil {
		t.Fatalf("sortieBin: %v", err)
	}
	if got != want {
		t.Errorf("sortieBin = %q, want %q", got, want)
	}
	if resolved, _, err := engine.Resolve(loop); err != nil || resolved != got {
		t.Errorf("engine.Resolve = %q, %v; want the plan's %q", resolved, err, got)
	}
	if viaPath, err := p.SortieBinPath(); err != nil || viaPath != got {
		t.Errorf("SortieBinPath = %q, %v; want %q", viaPath, err, got)
	}

	t.Setenv("JIG_ENGINE_BIN", "/custom/sortie")
	if got, _ := p.sortieBin(); got != "/custom/sortie" {
		t.Errorf("sortieBin = %q, want JIG_ENGINE_BIN to win", got)
	}
}

// A jig installed beside this binary has to be found without
// PATH, or running jig by absolute path fails.
func TestLoopBinaryPrefersASiblingOverPATH(t *testing.T) {
	prefix := t.TempDir()
	loop := filepath.Join(prefix, "jig")
	if err := os.WriteFile(loop, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_BIN", "")
	t.Setenv("PATH", "/nonexistent")
	if got := (Plan{LoopBin: loop}).LoopBinary(); got != loop {
		t.Errorf("LoopBinary = %q, want the explicit %q", got, loop)
	}
	// With nothing beside it, it falls back to PATH lookup by name.
	if got := (Plan{}).LoopBinary(); got != "jig" {
		t.Errorf("LoopBinary = %q, want the bare name for PATH lookup", got)
	}
}

// Setting a model and then removing it must leave the shipped file byte
// for byte. Jigz patches in place and never restores, so anything it
// writes has to be removable: a dangling $JIG_MODEL is an
// unresolved reference, and an adapter block Jigz created is a
// top-level key the engine rejects, and either one stops the loop.
func TestTrackerKindReadsTheWorkflowFrontMatter(t *testing.T) {
	dir := t.TempDir()
	w := filepath.Join(dir, ".jigz", "workflows")
	if err := os.MkdirAll(w, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"build":   "---\ntracker:\n  kind: github\n  api_key: $K\n---\nkind: github-pr\n",
		"review":  "---\npolling:\n  interval_ms: 1\ntracker:\n  kind: github-pr\n---\nbody\n",
		"nokind":  "---\ntracker:\n  project: x\n---\nbody\n",
		"nofront": "tracker:\n  kind: github\n",
		"broken":  "---\ntracker: [\n---\n",
	} {
		if err := os.WriteFile(filepath.Join(w, "WORKFLOW."+name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := resolved(t, dir)
	for name, want := range map[string]string{
		"build": "github", "review": "github-pr", "nokind": "", "nofront": "", "broken": "", "missing": "",
	} {
		if got := TrackerKind(r, name); got != want {
			t.Errorf("TrackerKind(%s) = %q, want %q", name, got, want)
		}
	}
}
func TestWithoutRuntimeBlockWarnings(t *testing.T) {
	raw := `{"valid":true,"errors":[],"warnings":[` +
		`{"severity":"warning","check":"unknown_key","message":"claude-code: unknown top-level key \"claude-code\""},` +
		`{"severity":"warning","check":"unknown_key","message":"pi: unknown top-level key \"pi\""},` +
		`{"severity":"warning","check":"unknown_key","message":"extra: unknown top-level key \"extra\""}]}`
	var res FileResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	got := res.WithoutRuntimeBlockWarnings().Warnings
	if len(got) != 1 || !strings.HasPrefix(got[0].Message, "extra:") {
		t.Fatalf("warnings = %+v", got)
	}
}

func TestMain(m *testing.M) {
	engine.SetDownload(func(engine.Spec) (string, error) {
		return "", errors.New("engine download disabled in unit tests")
	})
	os.Exit(m.Run())
}
