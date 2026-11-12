package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/engine"
)

// isolateEngineEnv points the directories and variables engine resolution
// reads at temp dirs.
func isolateEngineEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []string{"JIG_ENGINE_BIN", "JIG_ENGINE_REPO", "JIG_ENGINE_VERSION", "JIG_ENGINE_URL", "JIG_ENGINE_SHA256"} {
		t.Setenv(k, "")
	}
}

func TestEnsureSortieLinkSkipsAnUnpinnedFallback(t *testing.T) {
	isolateEngineEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sortie"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	loops := t.TempDir()
	captureStderr(t, func() { ensureSortieLinkIn(loops) })
	if _, err := os.Lstat(filepath.Join(loops, "sortie")); err == nil {
		t.Error("bin/sortie was linked to an unpinned engine")
	}
}

func TestMain(m *testing.M) {
	engine.SetDownload(func(engine.Spec) (string, error) {
		return "", errors.New("engine download disabled in unit tests")
	})
	os.Exit(m.Run())
}

func TestWriteEnvFileKeepsUserLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".jigz", ".env.loop")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	old := "# my notes\nMY_VAR=1\nSORTIE_TRACKER_PROJECT=old/repo\n\nexport OTHER=\"a b\"\nSORTIE_TRACKER_API_KEY=old\nSORTIE_AGENT_MAX_TURNS=7"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Tracker: "new/repo", Token: "tok", Workspaces: "/ws"}
	if _, _, err := writeEnvFile(dir, cfg); err != nil {
		t.Fatal(err)
	}
	want := "# my notes\nMY_VAR=1\nSORTIE_TRACKER_PROJECT=new/repo\n\nexport OTHER=\"a b\"\nSORTIE_TRACKER_API_KEY=tok\nSORTIE_AGENT_MAX_TURNS=7\n" +
		"JIG_TRACKER_PROJECT=new/repo\nJIG_WORKSPACES=/ws\n"
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("env file:\n%s\nwant:\n%s", got, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	if _, _, err := writeEnvFile(dir, cfg); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if string(again) != want {
		t.Errorf("a second write changed the file:\n%s", again)
	}
	left, _ := filepath.Glob(path + ".tmp*")
	if len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestWriteEnvFileCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Tracker: "o/r", Token: "tok", Workspaces: "/ws"}
	path, _, err := writeEnvFile(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	for _, line := range []string{"SORTIE_TRACKER_PROJECT=o/r", "JIG_TRACKER_PROJECT=o/r", "JIG_WORKSPACES=/ws", "SORTIE_TRACKER_API_KEY=tok"} {
		if !strings.Contains(string(got), line+"\n") {
			t.Errorf("missing %q in %q", line, got)
		}
	}
}
