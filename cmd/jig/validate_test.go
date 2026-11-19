package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
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
