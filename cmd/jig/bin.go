package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// managedEnv is the env file entries jig owns, in file order.
func managedEnv(cfg *config.Config) [][2]string {
	return [][2]string{
		{"SORTIE_TRACKER_PROJECT", cfg.Tracker},
		{"JIG_TRACKER_PROJECT", cfg.Tracker},
		{"JIG_WORKSPACES", cfg.Workspaces},
		{"SORTIE_TRACKER_API_KEY", cfg.Token},
	}
}

// writeEnvFile writes the resolved settings to .jigz/.env.loop so the
// workflows can load them via --env-file without touching the user's env.
// Lines jig does not manage are kept as they are. changed reports whether
// a managed value differs from the file as it was.
func writeEnvFile(dir string, cfg *config.Config) (path string, changed bool, err error) {
	path = filepath.Join(dir, ".jigz", ".env.loop")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	managed := managedEnv(cfg)
	changed = mergeEnv(string(old), managed) != mergeEnv(string(old), nil)
	if err := writeFileAtomicMode(path, []byte(mergeEnv(string(old), managed)), 0o600); err != nil {
		return "", false, err
	}
	cfg.EnvFile = path
	return path, changed, nil
}

// mergeEnv sets the managed keys in an env file body. A managed key is
// replaced where it stands, or appended when missing. Other lines,
// comments and the line order are unchanged.
func mergeEnv(body string, managed [][2]string) string {
	values := map[string]string{}
	for _, kv := range managed {
		values[kv[0]] = kv[1]
	}
	seen := map[string]bool{}
	var lines []string
	if body != "" {
		lines = strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	}
	for i, line := range lines {
		key, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
		key = strings.TrimSpace(key)
		if v, managed := values[key]; ok && managed {
			lines[i] = key + "=" + v
			seen[key] = true
		}
	}
	for _, kv := range managed {
		if !seen[kv[0]] {
			lines = append(lines, kv[0]+"="+kv[1])
		}
	}
	return strings.Join(lines, "\n") + "\n"
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
