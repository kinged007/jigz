//go:build contract

// Contract tests for the engine matrix (section 7.2, 7.4, 7.5): every
// golden validates clean on its engine, the CLI and capability probes
// hold per release, and rule-level agent kinds behave as section 4.5
// relies on. They need both cached engine binaries and run under the
// contract build tag, in parallel with check and acceptance.
package gen

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/fakegithub"
)

type validateReport struct {
	Valid  bool `json:"valid"`
	Errors []struct {
		Check   string `json:"check"`
		Message string `json:"message"`
	} `json:"errors"`
	Warnings []struct {
		Check   string `json:"check"`
		Message string `json:"message"`
	} `json:"warnings"`
}

func engineFor(t *testing.T, p Profile) string {
	t.Helper()
	bin := engine.Installed(engine.ShareDirs(), engine.Spec{Repo: p.Repo, Tag: p.Version})
	if bin == "" {
		t.Fatalf("no cached engine for %s", p.EngineVersion())
	}
	return bin
}

func profileDir(p Profile) string {
	return profileName(p) + "-" + strings.TrimPrefix(p.Version, "v")
}

// docWithStubs copies a golden to a scratch directory with stub prompts
// for every template it points at; the engine only checks readability.
func docWithStubs(t *testing.T, raw []byte, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	prompts := filepath.Join(dir, "prompts")
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "template:")
		if !ok {
			continue
		}
		p := strings.Trim(strings.TrimSpace(v), `"`)
		p = strings.TrimPrefix(p, "./")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("# prompt\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = prompts
	return filepath.Join(dir, name)
}

func validateEnv(dir string) []string {
	return append(os.Environ(),
		"JIG_TRACKER_TOKEN=test-token-12345678",
		"SORTIE_TRACKER_API_KEY=test-key-12345678",
		"SORTIE_TRACKER_PROJECT=owner/repo",
		"JIG_WORKSPACES="+dir,
	)
}

func runValidate(t *testing.T, bin, doc string, env []string) validateReport {
	t.Helper()
	cmd := exec.Command(bin, "validate", "--format", "json", doc)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			out = ee.Stderr
			if len(out) == 0 {
				out, _ = cmd.CombinedOutput()
			}
		}
		var rep validateReport
		if jerr := json.Unmarshal(out, &rep); jerr == nil && (!rep.Valid || len(rep.Errors) > 0) {
			return rep
		}
		t.Fatalf("validate: %v\n%s", err, out)
	}
	var rep validateReport
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("validate output is not JSON: %v\n%s", err, out)
	}
	return rep
}

// TestContractGoldensValidate runs sortie validate on every golden file
// with the matching engine and requires valid:true with no unknown keys.
func TestContractGoldensValidate(t *testing.T) {
	for _, p := range Profiles() {
		entries, err := os.ReadDir(filepath.Join("testdata", "golden", profileDir(p)))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			t.Fatalf("no goldens for %s", p.EngineVersion())
		}
		bin := engineFor(t, p)
		for _, e := range entries {
			raw, err := os.ReadFile(filepath.Join("testdata", "golden", profileDir(p), e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			rep := runValidate(t, bin, docWithStubs(t, raw, e.Name()), validateEnv(dir))
			if !rep.Valid {
				t.Errorf("%s/%s: invalid: %+v", p.EngineVersion(), e.Name(), rep.Errors)
			}
			for _, w := range rep.Warnings {
				if strings.Contains(w.Check, "unknown") {
					t.Errorf("%s/%s: unknown key %q", p.EngineVersion(), e.Name(), w.Message)
				}
			}
		}
	}
}

// TestContractCLI pins the section 7.5 flags and the JSON shape.
func TestContractCLI(t *testing.T) {
	for _, p := range Profiles() {
		bin := engineFor(t, p)
		out, err := exec.Command(bin, "--help").Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--env-file", "--port", "0 to disable"} {
			if !strings.Contains(string(out), want) {
				t.Errorf("%s --help lacks %q", p.EngineVersion(), want)
			}
		}
	}
}

// TestContractDryRun runs one poll cycle of a golden document against a
// fake tracker and requires exit 0.
func TestContractDryRun(t *testing.T) {
	gh := fakegithub.New("octo", "token")
	gh.AddRepo("acme/widgets", "agent:build", "in-progress", "agent:done", "needs-human")
	srv := httptest.NewServer(gh)
	defer srv.Close()

	var p Profile
	for _, q := range Profiles() {
		if q.Repo == "kinged007/sortie" {
			p = q
		}
	}
	bin := engineFor(t, p)
	raw, err := os.ReadFile(filepath.Join("testdata", "golden", profileDir(p), "default-issues-issues.md"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	doc := docWithStubs(t, raw, "WORKFLOW.issues.md")
	envFile := filepath.Join(dir, "env")
	env := "SORTIE_TRACKER_ENDPOINT=" + srv.URL + "\n" +
		"SORTIE_TRACKER_PROJECT=acme/widgets\n" +
		"SORTIE_TRACKER_API_KEY=token\n" +
		"JIG_TRACKER_TOKEN=token\n" +
		"JIG_WORKSPACES=" + dir + "\n"
	if err := os.WriteFile(envFile, []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--dry-run", "--port", "0", "--env-file", envFile, doc)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		"JIG_WORKSPACES="+dir,
		"SORTIE_TRACKER_PROJECT=acme/widgets",
		"SORTIE_TRACKER_API_KEY=token",
		"JIG_TRACKER_TOKEN=token",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("dry run: %v\n%s", err, out)
	}
}

// TestContractProbe pins the registered kinds per release to the
// profile table.
func TestContractProbe(t *testing.T) {
	for _, p := range Profiles() {
		caps := engine.Probe(engineFor(t, p))
		if !caps.Known() {
			t.Fatalf("%s: probe failed", p.EngineVersion())
		}
		if got := "v" + caps.Version; got != p.Version {
			t.Errorf("probe version %s, want %s", got, p.Version)
		}
		for _, k := range p.Trackers {
			if !caps.HasTracker(k) {
				t.Errorf("%s: tracker kind %q not registered", p.EngineVersion(), k)
			}
		}
		for _, k := range p.Agents {
			if !caps.HasAgent(k) {
				t.Errorf("%s: agent kind %q not registered", p.EngineVersion(), k)
			}
		}
	}
}

// TestContractRuleAgent pins section 4.5: a rule naming the pi kind
// validates on the fork with a pi block, and fails with unknown agent
// kind where pi is not registered. A per-rule model is an unknown key
// on both.
func TestContractRuleAgent(t *testing.T) {
	const body = `---
tracker:
  kind: github
  api_key: testkey12345678
  project: owner/repo
  query_filter: "label:x -label:needs-human"
  active_states: [x, in-progress]
  in_progress_state: in-progress
  handoff_state: agent:done
  terminal_states: [closed]
  comments:
    on_completion: false
polling:
  interval_ms: 5000
db_path: ../data/jigz-probe.db
workspace:
  root: /tmp/probe-wsx/probe
  retention_days: 30
hooks:
  after_create: |
    true
  before_run: |
    true
  timeout_ms: 60000
agent:
  kind: claude-code
  command: /bin/true
  max_turns: 1
  max_concurrent_agents: 1
claude-code:
  model: "m"
%s
dispatch:
  rules:
    - name: r
%s
      match:
        labels: ["x"]
      template: ./prompts/p.md
  default:
    template: ./prompts/p.md
---
`
	var fork, upstream Profile
	for _, p := range Profiles() {
		if p.Repo == "kinged007/sortie" {
			fork = p
		} else {
			upstream = p
		}
	}
	withBlock := "pi:\n  model: \"sonnet\"\n"
	piRule := "      agent: pi\n"
	dir := t.TempDir()
	rep := runValidate(t, engineFor(t, fork), docWithStubs(t, []byte(fmt.Sprintf(body, withBlock, piRule)), "w.md"), validateEnv(dir))
	if !rep.Valid {
		t.Errorf("fork rule agent pi with block: invalid: %+v", rep.Errors)
	}
	rep = runValidate(t, engineFor(t, upstream), docWithStubs(t, []byte(fmt.Sprintf(body, withBlock, piRule)), "w.md"), validateEnv(dir))
	if rep.Valid {
		t.Errorf("upstream rule agent pi accepted")
	} else {
		found := false
		for _, e := range rep.Errors {
			found = found || strings.Contains(e.Message, `unknown agent kind "pi"`)
		}
		if !found {
			t.Errorf("upstream errors %+v, want unknown agent kind", rep.Errors)
		}
	}
	modelRule := "      model: sub\n"
	for _, p := range Profiles() {
		rep := runValidate(t, engineFor(t, p), docWithStubs(t, []byte(fmt.Sprintf(body, "", modelRule)), "w.md"), validateEnv(dir))
		if rep.Valid {
			t.Errorf("%s per-rule model accepted", p.EngineVersion())
			continue
		}
		found := false
		for _, w := range rep.Warnings {
			found = found || strings.Contains(w.Check, "unknown")
		}
		if !found {
			t.Errorf("%s per-rule model: no unknown-key warning in %+v", p.EngineVersion(), rep.Warnings)
		}
	}
}
