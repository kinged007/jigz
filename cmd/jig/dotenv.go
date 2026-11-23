package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// repoEnvFromFile records the keys loadDotenv set from each file path, so
// a reload can re-apply changed values. A key the environment already
// held is never recorded, so the environment keeps winning for it.
var (
	repoEnvMu       sync.Mutex
	repoEnvFromFile = map[string]map[string]bool{}
)

// loadRepoEnv loads <root>/.jigz/.env, the file the user owns. Jig never
// writes it.
func loadRepoEnv(root string) error {
	return loadDotenv(filepath.Join(root, ".jigz", ".env"))
}

// reloadRepoEnv re-reads <root>/.jigz/.env into this process. Keys first
// set from the file are updated to the new file values, and new keys the
// environment does not hold are added. Keys deleted from the file are
// unset. A key the environment held before the first load keeps winning.
// A missing file is not an error.
func reloadRepoEnv(root string) error {
	path := filepath.Join(root, ".jigz", ".env")
	pairs, err := parseDotenv(path)
	if err != nil {
		return err
	}
	repoEnvMu.Lock()
	defer repoEnvMu.Unlock()
	fromFile := repoEnvFromFile[path]
	if fromFile == nil {
		fromFile = map[string]bool{}
		repoEnvFromFile[path] = fromFile
	}
	seen := map[string]bool{}
	for _, kv := range pairs {
		seen[kv[0]] = true
	}
	for key := range fromFile {
		if !seen[key] {
			if err := os.Unsetenv(key); err != nil {
				return err
			}
			delete(fromFile, key)
		}
	}
	for _, kv := range pairs {
		if fromFile[kv[0]] {
			if err := os.Setenv(kv[0], kv[1]); err != nil {
				return err
			}
			continue
		}
		if _, set := os.LookupEnv(kv[0]); !set {
			if err := os.Setenv(kv[0], kv[1]); err != nil {
				return err
			}
			fromFile[kv[0]] = true
		}
	}
	return nil
}

// loadDotenv sets the KEY=VALUE pairs of the file at path that are not
// already in the environment. A missing file is not an error.
func loadDotenv(path string) error {
	pairs, err := parseDotenv(path)
	repoEnvMu.Lock()
	defer repoEnvMu.Unlock()
	fromFile := repoEnvFromFile[path]
	if fromFile == nil {
		fromFile = map[string]bool{}
		repoEnvFromFile[path] = fromFile
	}
	for _, kv := range pairs {
		if _, set := os.LookupEnv(kv[0]); !set {
			if err := os.Setenv(kv[0], kv[1]); err != nil {
				return err
			}
			fromFile[kv[0]] = true
		}
	}
	return err
}

// parseDotenv returns the KEY=VALUE pairs of the file at path in file
// order. Blank lines and lines starting with # are skipped, and one pair of
// surrounding quotes is removed from a value. A missing file has no pairs.
func parseDotenv(path string) ([][2]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var pairs [][2]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		val = strings.TrimSpace(val)
		if n := len(val); n >= 2 && (val[0] == '"' || val[0] == '\'') && val[n-1] == val[0] {
			val = val[1 : n-1]
		}
		pairs = append(pairs, [2]string{key, val})
	}
	return pairs, sc.Err()
}

// applyTargetRepoEnv appends a target repo's .env pairs to a child env.
// Keys already set after base (from .env.loop or the config) stay as they
// are; other keys are appended, so the target repo's value wins for this
// child even when the process environment holds the dashboard repo's value
// for the same key. A key the process environment held before any .env
// load keeps winning over the target file.
func applyTargetRepoEnv(env []string, base int, pairs [][2]string) []string {
	if base < 0 {
		base = 0
	}
	if base > len(env) {
		base = len(env)
	}
	managed := map[string]bool{}
	for _, kv := range env[base:] {
		if k, _, ok := strings.Cut(kv, "="); ok {
			managed[k] = true
		}
	}
	for _, kv := range pairs {
		if managed[kv[0]] {
			continue
		}
		if _, set := os.LookupEnv(kv[0]); set && !repoEnvFileSourced(kv[0]) {
			continue
		}
		env = append(env, kv[0]+"="+kv[1])
	}
	return env
}

// repoEnvFileSourced reports whether key was set from any loaded .jigz/.env
// file. Such keys may be overridden by a target repo's file; exported keys
// may not.
func repoEnvFileSourced(key string) bool {
	repoEnvMu.Lock()
	defer repoEnvMu.Unlock()
	for _, keys := range repoEnvFromFile {
		if keys[key] {
			return true
		}
	}
	return false
}
