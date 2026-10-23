package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/engine"
)

// resolveSortieBin finds the sortie binary with engine.Resolve: JIG_ENGINE_BIN,
// then the configured engine installed in a share directory, then a
// verified download of its release. Only when the download fails does it
// fall back to a `sortie` beside this binary or on PATH, whose version is
// not checked.
func resolveSortieBin() (string, error) {
	bin, _, err := engine.Resolve()
	return bin, err
}

// writeEnvFile writes the resolved settings to .jigz/.env.loop so the
// workflows can load them via --env-file without touching the user's env.
func writeEnvFile(dir string, cfg *config.Config) (string, error) {
	path := filepath.Join(dir, ".jigz", ".env.loop")
	body := "SORTIE_TRACKER_PROJECT=" + cfg.Tracker + "\n" +
		"JIG_WORKSPACES=" + cfg.Workspaces + "\n" +
		"SORTIE_TRACKER_API_KEY=" + cfg.Token + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	cfg.EnvFile = path
	return path, nil
}

// ensureSortieLink exposes the resolved sortie engine as `sortie` next to
// the loop binary so bare `sortie` commands (e.g. `sortie validate`)
// work. A missing engine is a warning — config and labels don't need it.
func ensureSortieLink() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	ensureSortieLinkIn(filepath.Dir(self))
}

// ensureSortieLinkIn is ensureSortieLink for a chosen directory, so the
// self-link guard is testable without relocating the running binary.
func ensureSortieLinkIn(dir string) {
	bin, unpinned, err := engine.Resolve()
	if err != nil {
		fmt.Fprintln(os.Stderr, "jig: warning: engine not found:", err)
		return
	}
	if unpinned {
		return
	}
	dest := filepath.Join(dir, engine.Binary)
	if samePath(dest, bin) {
		// The engine resolveSortieBin found is already this path, so
		// linking would point the name at itself and destroy the binary.
		// Reached whenever both are installed in one directory, which is
		// the normal case.
		return
	}
	if engine.LinkAt(bin, dir) == "" {
		return
	}
	fmt.Println("linked", dest, "->", bin)
}

// samePath reports whether two paths name the same file, following
// symlinks. A path that cannot be stat'd is not the same as anything.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}
