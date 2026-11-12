package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// loadRepoEnv loads <root>/.jigz/.env, the file the user owns. Jig never
// writes it.
func loadRepoEnv(root string) error {
	return loadDotenv(filepath.Join(root, ".jigz", ".env"))
}

// loadDotenv sets the KEY=VALUE pairs of the file at path that are not
// already in the environment. A missing file is not an error.
func loadDotenv(path string) error {
	pairs, err := parseDotenv(path)
	for _, kv := range pairs {
		if _, set := os.LookupEnv(kv[0]); !set {
			if err := os.Setenv(kv[0], kv[1]); err != nil {
				return err
			}
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
