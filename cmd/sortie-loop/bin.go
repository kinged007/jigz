package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/kinged007/sortie-loop/internal/config"
)

// resolveSortieBin finds the sortie binary: SORTIE_BIN, then the pinned
// engine installed in a share directory, then a verified download of the
// pinned release. Only when the download fails does it fall back to a
// `sortie` beside this binary or on PATH, whose version is not checked.
//
// The pin is preferred over PATH because the workflows are written for one
// engine version, and a machine with another `sortie` on PATH would
// otherwise run that one.
func resolveSortieBin() (string, error) {
	bin, _, err := resolveEngine(sortieShareDirs())
	return bin, err
}

// resolveSortieBinIn is resolveSortieBin over an explicit share-directory
// list, so the precedence is testable without relocating the binary.
func resolveSortieBinIn(dirs []string) (string, error) {
	bin, _, err := resolveEngine(dirs)
	return bin, err
}

// downloadPinnedEngine runs at most once per process, so an offline machine
// does not retry the download and repeat the warning on every resolution.
// It is a variable so tests can replace the download.
var downloadPinnedEngine = sync.OnceValues(ensureEngine)

// fallbackWarned limits the unpinned-engine warning to once per process.
var fallbackWarned = new(sync.Once)

// resolveEngine is the resolution behind resolveSortieBin. unpinned is true
// when the result is the fallback engine, whose version is not checked.
func resolveEngine(dirs []string) (bin string, unpinned bool, err error) {
	if v := os.Getenv("SORTIE_BIN"); v != "" {
		return v, false, nil
	}
	if p := installedSortieBinIn(dirs); p != "" {
		return p, false, nil
	}
	p, dlErr := downloadPinnedEngine()
	if dlErr == nil {
		return p, false, nil
	}
	if p := unpinnedSortieBin(); p != "" {
		fallbackWarned.Do(func() {
			fmt.Fprintf(os.Stderr, "sortie-loop: warning: using %s, whose version is not checked and may not be the pinned %s: could not download the pinned engine: %v\n",
				p, engineVersion(), dlErr)
		})
		return p, true, nil
	}
	_, notFound := ensureSortieBinIn(dirs)
	return "", false, fmt.Errorf("%w; could not download the pinned engine: %v", notFound, dlErr)
}

// unpinnedSortieBin is a `sortie` beside the running binary or on PATH, or
// "" when there is none.
func unpinnedSortieBin() string {
	if self, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(self), "sortie"); p != self {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	if p, err := exec.LookPath("sortie"); err == nil {
		return p
	}
	return ""
}

// installedSortieBin is the version-pinned engine in a share directory,
// or "" when there is none. It skips symlinks: the bin/sortie link is
// itself built from this lookup, so following it here would resolve back
// to whatever a PATH hit pointed at.
func installedSortieBin() string {
	return installedSortieBinIn(sortieShareDirs())
}

// installedSortieBinIn is installedSortieBin over an explicit candidate
// list, so the search order is testable without relocating the binary.
func installedSortieBinIn(dirs []string) string {
	for _, dir := range dirs {
		p := filepath.Join(dir, engineBinary+"-"+engineVersion())
		st, err := os.Lstat(p)
		if err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// writeEnvFile writes the resolved settings to .sortie/.env.loop so the
// workflows can load them via --env-file without touching the user's env.
func writeEnvFile(dir string, cfg *config.Config) (string, error) {
	path := filepath.Join(dir, ".sortie", ".env.loop")
	body := "SORTIE_TRACKER_PROJECT=" + cfg.Tracker + "\n" +
		"SORTIE_LOOP_CLONE_URL=" + cfg.CloneURL + "\n" +
		"SORTIE_LOOP_WORKSPACES=" + cfg.Workspaces + "\n" +
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
	bin, unpinned, err := resolveEngine(sortieShareDirs())
	if err != nil {
		fmt.Fprintln(os.Stderr, "sortie-loop: warning: sortie engine not found:", err)
		return
	}
	if unpinned {
		return
	}
	dest := filepath.Join(dir, engineBinary)
	if samePath(dest, bin) {
		// The engine resolveSortieBin found is already this path, so
		// linking would point the name at itself and destroy the binary.
		// Reached whenever both are installed in one directory, which is
		// the normal case.
		return
	}
	if linkEngineAt(bin, dir) == "" {
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

// sortieShareDirs are the directories the installer may have put the
// version-named engine in.
//
// install.sh honours --prefix and writes the engine to
// <prefix>/share/sortie-loop, so a custom prefix is where the engine
// actually is. Looking only at ~/.local meant resolveSortieBin failed
// for every non-default prefix, which left ensureSortieLink unable to
// create the sortie symlink and reported "engine not found" on a
// perfectly complete installation. Deriving the directory from the
// running binary makes --prefix work, with the home default kept for an
// install whose binaries moved later.
func sortieShareDirs() []string {
	var dirs []string
	add := func(dir string) {
		if dir != "" && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	add(engineDataDir())
	if self, err := os.Executable(); err == nil {
		abs, err := filepath.Abs(self)
		if err == nil {
			// <prefix>/bin/sortie-loop -> <prefix>/share/sortie-loop
			add(filepath.Join(filepath.Dir(filepath.Dir(abs)), "share", "sortie-loop"))
		}
	}
	return dirs
}

func ensureSortieBin() (string, error) {
	return ensureSortieBinIn(sortieShareDirs())
}

// ensureSortieBinIn is ensureSortieBin over an explicit candidate list, so
// the search order is testable without relocating the running binary.
func ensureSortieBinIn(dirs []string) (string, error) {
	for _, dir := range dirs {
		dest := filepath.Join(dir, engineBinary+"-"+engineVersion())
		if _, err := os.Stat(dest); err == nil {
			return dest, nil
		}
	}
	looked := "SORTIE_BIN, ./sortie, PATH"
	if len(dirs) > 0 {
		looked += ", and " + strings.Join(dirs, ", ") + "/" + engineBinary + "-" + engineVersion()
	}
	return "", fmt.Errorf("sortie binary not found (looked for %s); download sortie %s from https://github.com/kinged007/sortie/releases and set SORTIE_BIN, or rerun install.sh",
		looked, engineVersion())
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}
