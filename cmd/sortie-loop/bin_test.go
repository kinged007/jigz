package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// stubEngineDownload replaces the pinned-engine download for one test and
// isolates the environment the resolution reads.
func stubEngineDownload(t *testing.T, fn func() (string, error)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SORTIE_BIN", "")
	origDownload, origWarned := downloadPinnedEngine, fallbackWarned
	downloadPinnedEngine = sync.OnceValues(fn)
	fallbackWarned = new(sync.Once)
	t.Cleanup(func() {
		downloadPinnedEngine = origDownload
		fallbackWarned = origWarned
	})
}

func pathEngine(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "sortie")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return p
}

func TestResolveEngineInstalledPinBeatsPATH(t *testing.T) {
	calls := 0
	stubEngineDownload(t, func() (string, error) { calls++; return "", errors.New("must not download") })
	pathEngine(t)
	share := t.TempDir()
	pin := filepath.Join(share, "sortie-"+engineVersion())
	if err := os.WriteFile(pin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, unpinned, err := resolveEngine([]string{share})
	if err != nil || got != pin || unpinned || calls != 0 {
		t.Errorf("got %q unpinned=%v err=%v calls=%d; want the installed pin", got, unpinned, err, calls)
	}
}

func TestResolveEngineDownloadBeatsPATH(t *testing.T) {
	downloaded := filepath.Join(t.TempDir(), "sortie-pinned")
	stubEngineDownload(t, func() (string, error) { return downloaded, nil })
	pathEngine(t)
	got, unpinned, err := resolveEngine([]string{t.TempDir()})
	if err != nil || got != downloaded || unpinned {
		t.Errorf("got %q unpinned=%v err=%v; want the downloaded pin %q", got, unpinned, err, downloaded)
	}
}

func TestResolveEngineFallsBackToPATHWithOneWarning(t *testing.T) {
	calls := 0
	stubEngineDownload(t, func() (string, error) { calls++; return "", errors.New("network down") })
	hit := pathEngine(t)
	var results []string
	stderr := captureStderr(t, func() {
		for range 3 {
			got, unpinned, err := resolveEngine([]string{t.TempDir()})
			if err != nil || !unpinned {
				t.Errorf("got %q unpinned=%v err=%v; want the PATH fallback", got, unpinned, err)
			}
			results = append(results, got)
		}
	})
	for _, got := range results {
		if got != hit {
			t.Errorf("got %q, want %q", got, hit)
		}
	}
	if calls != 1 {
		t.Errorf("download attempted %d times, want 1", calls)
	}
	if n := strings.Count(stderr, "warning"); n != 1 {
		t.Errorf("%d warnings, want 1: %q", n, stderr)
	}
	for _, want := range []string{hit, "not checked", engineVersion(), "network down"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("warning %q does not contain %q", stderr, want)
		}
	}
}

func TestResolveEngineErrorsWithoutFallback(t *testing.T) {
	stubEngineDownload(t, func() (string, error) { return "", errors.New("network down") })
	t.Setenv("PATH", t.TempDir())
	_, _, err := resolveEngine([]string{t.TempDir()})
	if err == nil {
		t.Fatal("expected an error with no engine anywhere")
	}
	for _, want := range []string{"network down", "SORTIE_BIN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestEnsureSortieLinkSkipsAnUnpinnedFallback(t *testing.T) {
	stubEngineDownload(t, func() (string, error) { return "", errors.New("network down") })
	pathEngine(t)
	loops := t.TempDir()
	captureStderr(t, func() { ensureSortieLinkIn(loops) })
	if _, err := os.Lstat(filepath.Join(loops, "sortie")); err == nil {
		t.Error("bin/sortie was linked to an unpinned engine")
	}
}

func TestMain(m *testing.M) {
	downloadPinnedEngine = func() (string, error) {
		return "", errors.New("engine download disabled in unit tests")
	}
	os.Exit(m.Run())
}
