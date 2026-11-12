package main

import (
	"encoding/json"
	"errors"
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
		agentVersionOutput = func(command string) (string, error) {
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
	t.Run("the agent command is run", func(t *testing.T) {
		ran := stub(t, "2.0.0\n", nil)
		agentVersionChecks(cfgFor(t, "  oc: {runtime: opencode, command: \"npx opencode\"}\n"))
		if len(*ran) != 1 || (*ran)[0] != "npx opencode" {
			t.Fatalf("ran %v", *ran)
		}
	})
}

func TestAgentVersionOutputRunsTheCommand(t *testing.T) {
	script := writeAgentScript(t, "echo \"$@\"\n")
	out, err := agentVersionOutput(script + " sub")
	if err != nil || out != "sub --version\n" {
		t.Fatalf("out %q err %v", out, err)
	}
	if _, err := agentVersionOutput("jig-no-such-agent-cli"); err == nil {
		t.Fatal("a missing command did not fail")
	}
}
