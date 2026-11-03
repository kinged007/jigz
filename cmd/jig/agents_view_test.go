package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/rollup"
)

const secretValue = "sk-secret-value-123"

func isolateDirs(t *testing.T) (config, state, cache string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	config, state, cache = t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CACHE_HOME", cache)
	return
}

func getPage(t *testing.T, a *app, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "127.0.0.1"
	a.routes(newHostGuard("127.0.0.1")).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestAgentsPageWithoutAConfig(t *testing.T) {
	isolateDirs(t)
	body := getPage(t, &app{sizes: newRepoSizes(), stateDir: t.TempDir()}, "/agents")
	if !strings.Contains(body, "No repository configuration found") {
		t.Errorf("page lacks the empty-fleet note: %s", body)
	}
}

func TestAgentsPageShowsTheConfiguredFleet(t *testing.T) {
	_, _, cache := isolateDirs(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".jigz", "config.yaml"), `repo: owner/repo
tracker:
  kind: github
  api_key: { from_env: JIG_TEST_TOKEN }
  project: owner/repo
  in_progress_state: in-progress
agents:
  builder: { runtime: claude-code, model: claude-sonnet-5-5 }
  spare: { runtime: pi, model: sonnet, schedule: { timezone: UTC, windows: [{ days: [sat, sun], start: "09:00", end: "18:00" }] } }
  idle: { runtime: claude-code, model: m, env: { TOK: { from_env: JIG_MISSING_TOKEN } } }
pools:
  build-pool: [builder, spare]
work:
  issues:
    query_filter: "label:x"
    agent: build-pool
    rules:
      - { name: build, match: { labels: [x] }, prompt: p.md }
`)
	t.Setenv("JIG_TEST_TOKEN", "test-token")
	dir := filepath.Join(cache, "jigz")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := registry{Entries: map[string]registryEntry{root: {
		RepoName: "owner/repo", UpdatedAt: time.Now(), PID: os.Getpid(),
		Loops: []loopEndpoint{{Repo: root, RepoName: "owner/repo", Loop: "issues"}},
	}}}
	raw, _ := json.Marshal(reg)
	mustWrite(t, filepath.Join(dir, "registry.json"), string(raw))
	// Exhaust one agent through the state file; the others stay usable
	// on a weekday run and the pool lists both members.
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	mustWrite(t, filepath.Join(root, ".jigz", "data", "state.json"),
		`{"agents": {"builder": {"exhausted_until": "`+until+`", "reason": "usage limit"}}}`)
	a := &app{sizes: newRepoSizes(), stateDir: dir}
	a.sup = &fleetSupervisor{}
	body := getPage(t, a, "/agents")
	for _, want := range []string{"owner/repo", "builder", "claude-code", "claude-sonnet-5-5", "spare", "pi", "build-pool", "issues", "exhausted", "usage limit", "unresolved references", "JIG_MISSING_TOKEN"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestOperationsRowShowsHold(t *testing.T) {
	_, _, cache := isolateDirs(t)
	root := t.TempDir()
	dir := filepath.Join(cache, "jigz")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := registry{Entries: map[string]registryEntry{root: {
		RepoName: "owner/repo", UpdatedAt: time.Now(), PID: os.Getpid(),
		Loops: []loopEndpoint{{Repo: root, RepoName: "owner/repo", Loop: "build"}},
	}}}
	raw, _ := json.Marshal(reg)
	mustWrite(t, filepath.Join(dir, "registry.json"), string(raw))
	path, err := statusPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := statusDoc{
		SchemaVersion: statusSchemaVersion, PID: os.Getpid(), Repo: root, RepoName: "owner/repo",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), State: "running",
		Workflows: []workflowStatus{{Name: "build", PID: os.Getpid(), State: "running", OfflineReason: "outside operating hours", Agent: "builder", Primary: "builder"}},
	}
	raw, _ = json.Marshal(doc)
	mustWrite(t, path, string(raw))

	a := &app{sizes: newRepoSizes(), stateDir: dir}
	a.sup = &fleetSupervisor{}
	a.source = loopsrc.StatusDoc{Reader: rollup.Reader{}, Roots: a.projectRoots, Dir: filepath.Join(dir, "status")}
	body := getPage(t, a, "/live")
	for _, want := range []string{"offline: outside operating hours", "builder"} {
		if !strings.Contains(body, want) {
			t.Errorf("operations page lacks %q", want)
		}
	}
}

func TestLivePageShowsTheAgentKindOfARunningSession(t *testing.T) {
	_, _, cache := isolateDirs(t)
	root := t.TempDir()
	dir := filepath.Join(cache, "jigz")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := registry{Entries: map[string]registryEntry{root: {
		RepoName: "owner/repo", UpdatedAt: time.Now(), PID: os.Getpid(),
		Loops: []loopEndpoint{{Repo: root, RepoName: "owner/repo", Loop: "build"}},
	}}}
	raw, _ := json.Marshal(reg)
	mustWrite(t, filepath.Join(dir, "registry.json"), string(raw))
	path, err := statusPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := statusDoc{
		SchemaVersion: statusSchemaVersion, PID: os.Getpid(), Repo: root, RepoName: "owner/repo",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), State: "running",
		Workflows: []workflowStatus{{Name: "build", PID: os.Getpid(), State: "running", SessionsKnown: true, Sessions: []rollup.Session{{
			IssueID: "20", Identifier: "owner/repo#20", Attempt: 1, StartedAt: time.Now().UTC(),
			AgentKind: "pi", RuleName: "plan", Model: "9router/coding-manual",
		}}}},
	}
	raw, _ = json.Marshal(doc)
	mustWrite(t, path, string(raw))

	a := &app{sizes: newRepoSizes(), stateDir: dir}
	a.sup = &fleetSupervisor{}
	a.source = loopsrc.StatusDoc{Reader: rollup.Reader{}, Roots: a.projectRoots, Dir: filepath.Join(dir, "status")}
	body := getPage(t, a, "/live")
	for _, want := range []string{"Pi &middot; 9router/coding-manual"} {
		if !strings.Contains(body, want) {
			t.Errorf("operations page lacks %q", want)
		}
	}
}
