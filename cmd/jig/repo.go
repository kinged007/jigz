package main

import (
	"os"
	"os/exec"
	"strings"

	"github.com/kinged007/jigz/internal/config"
)

// configRepo resolves the tracker repo for setup without requiring a
// token: the --repo value, then the environment, then config.yaml, then
// the git remote.
func configRepo(dir, flag string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("JIG_REPO"); v != "" {
		return v
	}
	if cfg, err := config.Load(dir); err == nil {
		return cfg.Repo
	}
	return guessRepo(dir)
}

// guessRepo derives owner/name from git, else empty (config keeps repo: blank).
func guessRepo(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return config.NormalizeRepo(strings.TrimSpace(string(out)))
}
