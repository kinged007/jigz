package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/kinged007/sortie-loop/internal/machine"
)

// Spec names one engine release: a GitHub repository and a release tag.
type Spec struct {
	Repo string
	Tag  string
}

// Version is the tag without its leading "v", the form GoReleaser puts in
// asset names.
func (s Spec) Version() string { return strings.TrimPrefix(s.Tag, "v") }

func (s Spec) String() string { return s.Repo + " " + s.Tag }

// Configured returns the engine release to use. SORTIE_ENGINE_REPO and
// SORTIE_ENGINE_VERSION win over the engine block of machine.yaml, which
// wins over the defaults. Every value is validated.
func Configured() (Spec, error) {
	var file machine.Engine
	if path, err := machine.DefaultPath(); err == nil {
		if file, err = machine.LoadEngine(path); err != nil {
			return Spec{}, err
		}
	}
	spec := Spec{Repo: DefaultRepo, Tag: DefaultTag}
	if file.Repo != "" {
		spec.Repo = file.Repo
	}
	if file.Version != "" {
		spec.Tag = file.Version
	}
	if v := os.Getenv("SORTIE_ENGINE_REPO"); v != "" {
		spec.Repo = strings.TrimSuffix(v, "/")
		if err := machine.ValidateEngineRepo(spec.Repo); err != nil {
			return Spec{}, fmt.Errorf("SORTIE_ENGINE_REPO: %w", err)
		}
	}
	if v := os.Getenv("SORTIE_ENGINE_VERSION"); v != "" {
		spec.Tag = "v" + strings.TrimPrefix(v, "v")
		if err := machine.ValidateEngineVersion(spec.Tag); err != nil {
			return Spec{}, fmt.Errorf("SORTIE_ENGINE_VERSION: %w", err)
		}
	}
	return spec, nil
}

// Current is Configured for display: an invalid setting gives the defaults.
func Current() Spec {
	if spec, err := Configured(); err == nil {
		return spec
	}
	return Spec{Repo: DefaultRepo, Tag: DefaultTag}
}

// ShareDirs are the directories the engine may be installed in: the data
// directory, and <prefix>/share/sortie-loop for the running binary and each
// of bins, where prefix is the directory above the binary's directory.
func ShareDirs(bins ...string) []string {
	dirs := []string{DataDir()}
	if self, err := os.Executable(); err == nil {
		bins = append([]string{self}, bins...)
	}
	for _, bin := range bins {
		abs, err := filepath.Abs(bin)
		if bin == "" || err != nil {
			continue
		}
		d := filepath.Join(filepath.Dir(filepath.Dir(abs)), "share", "sortie-loop")
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// Installed is the engine for spec in one of dirs, or "" when there is none.
// It skips symlinks: the bin/sortie link is built from this lookup, so
// following it here would resolve back to whatever a PATH hit pointed at.
func Installed(dirs []string, spec Spec) string {
	for _, dir := range dirs {
		p := spec.CachePath(dir)
		if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

var (
	downloadMu sync.Mutex
	downloads  = map[Spec]downloadResult{}
	warned     = map[Spec]bool{}
	ensure     = Ensure
)

type downloadResult struct {
	path string
	err  error
}

// SetDownload replaces the engine download, for tests. It returns a function
// that restores the previous one and forgets earlier results.
func SetDownload(fn func(Spec) (string, error)) (restore func()) {
	downloadMu.Lock()
	defer downloadMu.Unlock()
	prev := ensure
	ensure = fn
	downloads = map[Spec]downloadResult{}
	warned = map[Spec]bool{}
	return func() {
		downloadMu.Lock()
		defer downloadMu.Unlock()
		ensure = prev
		downloads = map[Spec]downloadResult{}
		warned = map[Spec]bool{}
	}
}

// downloadOnce attempts each spec's download once per process, so an offline
// machine does not retry and repeat the warning on every resolution.
func downloadOnce(spec Spec) (string, error) {
	downloadMu.Lock()
	defer downloadMu.Unlock()
	if r, ok := downloads[spec]; ok {
		return r.path, r.err
	}
	path, err := ensure(spec)
	downloads[spec] = downloadResult{path, err}
	return path, err
}

// Resolve finds the engine for the configured release: SORTIE_BIN, then the
// engine installed in a share directory, then a verified download. bins are
// extra binaries whose share directory is searched, in addition to the
// running binary's. unpinned is true when the result is a `sortie` found
// beside the running binary or on PATH after the download failed; its
// version is not checked.
func Resolve(bins ...string) (bin string, unpinned bool, err error) {
	return ResolveIn(ShareDirs(bins...))
}

// ResolveIn is Resolve over an explicit share-directory list.
func ResolveIn(dirs []string) (bin string, unpinned bool, err error) {
	if v := os.Getenv("SORTIE_BIN"); v != "" {
		return v, false, nil
	}
	spec, err := Configured()
	if err != nil {
		return "", false, err
	}
	if p := Installed(dirs, spec); p != "" {
		return p, false, nil
	}
	p, dlErr := downloadOnce(spec)
	if dlErr == nil {
		return p, false, nil
	}
	if p := unpinnedBin(); p != "" {
		downloadMu.Lock()
		first := !warned[spec]
		warned[spec] = true
		downloadMu.Unlock()
		if first {
			fmt.Fprintf(os.Stderr, "sortie-loop: warning: using %s, whose version is not checked and may not be the configured %s: could not download the engine: %v\n",
				p, spec, dlErr)
		}
		return p, true, nil
	}
	return "", false, fmt.Errorf("sortie binary not found (looked for SORTIE_BIN, ./sortie, PATH, and %s in %s); download sortie %s from https://github.com/%s/releases and set SORTIE_BIN; could not download the engine: %w",
		spec.CacheName(), strings.Join(dirs, ", "), spec.Tag, spec.Repo, dlErr)
}

// unpinnedBin is a `sortie` beside the running binary or on PATH, or "" when
// there is none.
func unpinnedBin() string {
	if self, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(self), Binary); p != self {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	if p, err := exec.LookPath(Binary); err == nil {
		return p
	}
	return ""
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}
