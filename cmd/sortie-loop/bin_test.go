package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kinged007/sortie-loop/internal/engine"
)

// isolateEngineEnv points the directories and variables engine resolution
// reads at temp dirs.
func isolateEngineEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []string{"SORTIE_BIN", "SORTIE_ENGINE_REPO", "SORTIE_ENGINE_VERSION", "SORTIE_ENGINE_URL", "SORTIE_ENGINE_SHA256"} {
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
