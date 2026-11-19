package main

import (
	"os"
	"path/filepath"
	"testing"

	fleetconfig "github.com/kinged007/jigz/internal/fleetconfig"
)

func writeRepoConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".jigz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".jigz", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGitHubFromPrefersEnabledProject(t *testing.T) {
	off := writeRepoConfig(t, "repo: o/a\ngithub:\n  api_url: http://off.example\n")
	on := writeRepoConfig(t, "repo: o/b\ngithub:\n  enrich: true\n  api_url: http://on.example\n")
	if g, ok := githubFrom([]string{off, on}); !ok || !g.Enrich || g.BaseURL() != "http://on.example" {
		t.Errorf("githubFrom = %+v, %v; want the enabled block", g, ok)
	}
	if g, ok := githubFrom([]string{off}); !ok || g.Enrich || g.BaseURL() != "http://off.example" {
		t.Errorf("githubFrom = %+v, %v; want the first block", g, ok)
	}
	if _, ok := githubFrom([]string{t.TempDir()}); ok {
		t.Error("githubFrom found a block without a config")
	}
}

func TestProjectTargetToken(t *testing.T) {
	for _, k := range []string{"JIG_REPO", "JIG_TOKEN", "GITHUB_TOKEN", "GH_TOKEN", "MY_TOKEN"} {
		t.Setenv(k, "")
	}
	dir := writeRepoConfig(t, "repo: o/r\ntoken: filetok\n")
	r := fleetconfig.Resolved{Repo: fleetconfig.Repo{Repo: "o/r"}, Dir: dir}
	t.Setenv("GITHUB_TOKEN", "envtok")
	if got := projectTarget(r).Token; got != "filetok" {
		t.Errorf("token %q, want the config token before GITHUB_TOKEN", got)
	}
	t.Setenv("JIG_TOKEN", "jigtok")
	if got := projectTarget(r).Token; got != "jigtok" {
		t.Errorf("token %q, want JIG_TOKEN first", got)
	}
	t.Setenv("JIG_TOKEN", "")
	if got := projectTarget(fleetconfig.Resolved{Repo: r.Repo, Dir: writeRepoConfig(t, "repo: o/r\n")}).Token; got != "envtok" {
		t.Errorf("token %q, want GITHUB_TOKEN without a config token", got)
	}
	t.Setenv("MY_TOKEN", "named")
	r.TokenEnv = "MY_TOKEN"
	if got := projectTarget(r).Token; got != "named" {
		t.Errorf("token %q, want the named variable", got)
	}
}
