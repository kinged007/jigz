package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

// A repository path holding ? or # must not end the SQLite DSN early.
// Before the fix the query was concatenated, so "?" turned the rest of
// the path into driver parameters and the open failed or read the wrong
// file.
func TestOpenRORepositoryPathWithQueryCharacters(t *testing.T) {
	for _, name := range []string{
		"plain.db",
		"with?query.db",
		"with#hash.db",
		"with space.db",
		"with'quote.db",
		"amp&ersand.db",
		"pct%20encoded.db",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name)
			// A database with run_history, so the open and a real query
			// both run.
			writeTestDB(t, path)
			db, err := openRO(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer db.Close()
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM run_history`).Scan(&n); err != nil {
				t.Fatalf("query: %v", err)
			}
			if n != 2 {
				t.Errorf("runs = %d, want 2", n)
			}
		})
	}
}

func TestTokenCheckNamesEverySource(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"),
		[]byte("repo: owner/name\ntoken: \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A token in the environment satisfies the check.
	t.Setenv("JIG_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckToken(); err == nil {
		t.Fatal("no token set, want an error")
	} else {
		for _, want := range []string{"JIG_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %s: %v", want, err)
			}
		}
	}
	t.Setenv("GITHUB_TOKEN", "ghp_example")
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckToken(); err != nil {
		t.Errorf("GITHUB_TOKEN should satisfy the check, got %v", err)
	}
}
