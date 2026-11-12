package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLabelOn(t *testing.T) {
	const exact = `[{"name":"type:bug","color":"d73a4a","description":"Bug"}]`
	const substring = `[{"name":"type:bugfix","color":"ffffff","description":"other"}]`
	cases := []struct {
		name   string
		stdout string
		want   *existingLabel
	}{
		{"empty output", "", nil},
		{"whitespace output", "\n", nil},
		{"empty array", "[]", nil},
		{"substring match only", substring, nil},
		{"exact match", exact, &existingLabel{color: "d73a4a", description: "Bug"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := stubCommand(t, "gh", "printf '%s' '"+tc.stdout+"'\n")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			got, err := labelOn("owner/repo", "type:bug")
			if err != nil {
				t.Fatalf("labelOn: %v", err)
			}
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("labelOn = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// setupRig prepares an isolated environment for running setup: a fake gh
// that logs every call, a fake engine, and private config and data
// directories. It returns the repo directory and the gh call log.
func setupRig(t *testing.T) (repo, ghLog string) {
	t.Helper()
	root := t.TempDir()
	ghLog = filepath.Join(root, "gh.log")
	dir := stubCommand(t, "gh", "echo \"$@\" >> '"+ghLog+"'\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	engine := filepath.Join(root, "sortie")
	if err := os.WriteFile(engine, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIG_ENGINE_BIN", engine)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	repo = filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, ghLog
}

func ghCalls(t *testing.T, log string) string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSetupCreatesOnlyTheLabelsOfTheConfig(t *testing.T) {
	repo, log := setupRig(t)
	cfg := filepath.Join(repo, ".jigz", "config.yaml")
	if err := os.WriteFile(cfg, []byte("repo: o/r\ntoken: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() { runSetup(repo, "o/r", false, false) })
	})
	if got := ghCalls(t, log); got != "" {
		t.Errorf("setup without a labels key called gh:\n%s", got)
	}
	if strings.Contains(stderr, "label") {
		t.Errorf("setup without a labels key warned about labels: %q", stderr)
	}

	body := "repo: o/r\ntoken: t\nlabels:\n- {name: \"custom\", color: \"ffffff\", description: \"x\"}\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() { runSetup(repo, "o/r", false, false) })
	got := ghCalls(t, log)
	if !strings.Contains(got, "label create custom") || strings.Count(got, "label create") != 1 {
		t.Errorf("gh calls:\n%s", got)
	}
}

func TestSetupSkipBuild(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		repo, log := setupRig(t)
		out := captureStdout(t, func() { runSetup(repo, "o/r", asJSON, true) })
		if got := ghCalls(t, log); got != "" {
			t.Errorf("json=%v: --skip-build called gh:\n%s", asJSON, got)
		}
		dir := filepath.Join(repo, ".jigz")
		raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
		if err != nil || !strings.Contains(string(raw), "labels:") {
			t.Errorf("json=%v: config not created: %v", asJSON, err)
		}
		if docs, _ := filepath.Glob(filepath.Join(dir, "workflows", "WORKFLOW.*.md")); len(docs) != 0 {
			t.Errorf("json=%v: workflow documents generated: %v", asJSON, docs)
		}
		for _, p := range []string{"workflows/prompts/build.md", "scripts/cleanup.sh", "data"} {
			if !exists(filepath.Join(dir, p)) {
				t.Errorf("json=%v: %s not created", asJSON, p)
			}
		}
		if asJSON {
			var res setupResult
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatalf("json output: %v\n%s", err, out)
			}
			if len(res.SyncedLabels) != 0 {
				t.Errorf("synced labels: %+v", res.SyncedLabels)
			}
		}
	}
}

func TestSetupInvalidConfigWritesNothing(t *testing.T) {
	for _, skip := range []bool{false, true} {
		for _, asJSON := range []bool{false, true} {
			repo, log := setupRig(t)
			body := "repo: o/r\ntoken: t\nlabels:\n- {name: \"custom\", color: \"ffffff\", description: \"x\"}\n" +
				"work:\n  issues:\n    rules:\n    - {name: build, match: {labels: [agent:build]}, prompt: prompts/build.md}\n"
			if err := os.WriteFile(filepath.Join(repo, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("skip=%v json=%v: setup accepted an invalid config", skip, asJSON)
					}
				}()
				captureStdout(t, func() { runSetup(repo, "o/r", asJSON, skip) })
			}()
			dir := filepath.Join(repo, ".jigz")
			for _, p := range []string{"workflows", "scripts", "data"} {
				if exists(filepath.Join(dir, p)) {
					t.Errorf("skip=%v json=%v: %s was written", skip, asJSON, p)
				}
			}
			if got := ghCalls(t, log); got != "" {
				t.Errorf("skip=%v json=%v: gh called:\n%s", skip, asJSON, got)
			}
		}
	}
}
