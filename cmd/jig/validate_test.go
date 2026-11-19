package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

func TestDropRuntimeBlockWarnings(t *testing.T) {
	in := "error: x: bad\n" +
		"warning: unknown_key: claude-code: unknown top-level key \"claude-code\"\n" +
		"warning: unknown_key: extra: unknown top-level key \"extra\"\n"
	want := "error: x: bad\n" +
		"warning: unknown_key: extra: unknown top-level key \"extra\"\n"
	if got := dropRuntimeBlockWarnings(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestValidatePrintsRootAndConfig(t *testing.T) {
	root := t.TempDir()
	out, _ := validateOutputFor(root, false)
	abs, _ := filepath.Abs(root)
	for _, want := range []string{
		"root: " + abs,
		"config: " + filepath.Join(abs, ".jigz", "config.yaml"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q:\n%s", want, out)
		}
	}
	jsonOut, _ := validateOutputFor(root, true)
	var v struct {
		Root   string `json:"root"`
		Config string `json:"config"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &v); err != nil {
		t.Fatalf("json output does not parse: %v\n%s", err, jsonOut)
	}
	if v.Root != abs || v.Config != filepath.Join(abs, ".jigz", "config.yaml") {
		t.Errorf("json root/config = %q/%q, want %q", v.Root, v.Config, abs)
	}
}

func TestAgentVersionChecks(t *testing.T) {
	cfgFor := func(t *testing.T, agents string) *config.Config {
		t.Helper()
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "repo: o/r\nagents:\n"+agents)
		cfg, err := config.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	stub := func(t *testing.T, out string, err error) *[]string {
		t.Helper()
		var ran []string
		old := agentVersionOutput
		agentVersionOutput = func(command string, _ []string) (string, error) {
			ran = append(ran, command)
			return out, err
		}
		t.Cleanup(func() { agentVersionOutput = old })
		return &ran
	}
	oc := "  oc: {runtime: opencode}\n"
	for _, c := range []struct {
		name, out string
		err       error
		ok        bool
		contains  string
	}{
		{"opencode 1.x fails", "1.18.29\n", nil, false, "agent oc: OpenCode 1.18.29 is not supported; install OpenCode 2.x, published on npm as @opencode/cli"},
		{"opencode 2.x passes", "2.0.3\n", nil, true, ""},
		{"a missing command fails", "", errors.New(`exec: "opencode": executable file not found in $PATH`), false, `agent oc: could not run "opencode" --version`},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub(t, c.out, c.err)
			checks := agentVersionChecks(cfgFor(t, oc))
			if len(checks) != 1 || checks[0].Name != "agent-version:oc" || checks[0].OK != c.ok || !strings.Contains(checks[0].Error, c.contains) {
				t.Fatalf("checks %+v", checks)
			}
		})
	}
	t.Run("other runtimes are not run", func(t *testing.T) {
		ran := stub(t, "1.0.0\n", nil)
		checks := agentVersionChecks(cfgFor(t, "  c: {runtime: claude-code}\n  x: {runtime: codex}\n"))
		if len(checks) != 0 || len(*ran) != 0 {
			t.Fatalf("checks %+v ran %v", checks, *ran)
		}
	})
	t.Run("a shared command is probed once", func(t *testing.T) {
		ran := stub(t, "2.0.0\n", nil)
		checks := agentVersionChecks(cfgFor(t, "  a: {runtime: opencode}\n  b: {runtime: opencode}\n  c: {runtime: opencode, env: {X: y}}\n"))
		if len(checks) != 3 || len(*ran) != 2 {
			t.Fatalf("checks %+v ran %v", checks, *ran)
		}
	})
	t.Run("the agent command is run", func(t *testing.T) {
		ran := stub(t, "2.0.0\n", nil)
		agentVersionChecks(cfgFor(t, "  oc: {runtime: opencode, command: \"npx opencode\"}\n"))
		if len(*ran) != 1 || (*ran)[0] != "npx opencode" {
			t.Fatalf("ran %v", *ran)
		}
	})
}

func TestAgentVersionChecksUseTheAgentEnv(t *testing.T) {
	script := writeAgentScript(t, "echo \"$JIG_TEST_PROBE_TOKEN\"\n")
	t.Setenv("JIG_TEST_PROBE_SRC", "from-process")
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "repo: o/r\nagents:\n  pi: {runtime: pi, command: "+script+", env: {JIG_TEST_PROBE_TOKEN: {from_env: JIG_TEST_PROBE_SRC}}}\n")
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	checks := agentVersionChecks(cfg)
	if len(checks) != 1 || checks[0].OK || !strings.Contains(checks[0].Error, "from-process") {
		t.Fatalf("the probe did not see the agent env: %+v", checks)
	}
}

func TestAgentVersionChecksMissingEnv(t *testing.T) {
	cfgFor := func(t *testing.T) *config.Config {
		t.Helper()
		root := t.TempDir()
		mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), "repo: o/r\nagents:\n  pi: {runtime: pi, env: {PI_API_KEY: {from_env: JIG_TEST_MISSING_70_SRC}}}\n")
		cfg, err := config.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	t.Run("unset from_env fails the check", func(t *testing.T) {
		os.Unsetenv("JIG_TEST_MISSING_70_SRC")
		called := false
		old := agentVersionOutput
		agentVersionOutput = func(string, []string) (string, error) { called = true; return "0.99.0\n", nil }
		t.Cleanup(func() { agentVersionOutput = old })
		checks := agentVersionChecks(cfgFor(t))
		if len(checks) != 1 || checks[0].Name != "agent-version:pi" || checks[0].OK {
			t.Fatalf("checks %+v", checks)
		}
		if want := "agent pi needs JIG_TEST_MISSING_70_SRC, which is not set"; !strings.Contains(checks[0].Error, want) {
			t.Fatalf("error %q does not contain %q", checks[0].Error, want)
		}
		if called {
			t.Fatal("the version probe ran despite the missing variable")
		}
	})
	t.Run("set from_env probes the version", func(t *testing.T) {
		t.Setenv("JIG_TEST_MISSING_70_SRC", "x")
		old := agentVersionOutput
		agentVersionOutput = func(string, []string) (string, error) { return "0.99.0\n", nil }
		t.Cleanup(func() { agentVersionOutput = old })
		checks := agentVersionChecks(cfgFor(t))
		if len(checks) != 1 || !checks[0].OK {
			t.Fatalf("checks %+v", checks)
		}
	})
}

func TestAgentVersionOutputRunsTheCommand(t *testing.T) {
	script := writeAgentScript(t, "echo \"$@\"\n")
	out, err := agentVersionOutput(script+" sub", nil)
	if err != nil || out != "sub --version\n" {
		t.Fatalf("out %q err %v", out, err)
	}
	if _, err := agentVersionOutput("jig-no-such-agent-cli", nil); err == nil {
		t.Fatal("a missing command did not fail")
	}
}
