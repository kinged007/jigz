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

func TestAgentsPageWithoutMachineFile(t *testing.T) {
	config, _, _ := isolateDirs(t)
	body := getPage(t, &app{sizes: newRepoSizes(), stateDir: t.TempDir()}, "/agents")
	for _, want := range []string{"No machine file at", filepath.Join(config, "jigz", "machine.yaml"), "without working hours or an account pool"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "Account pool") && strings.Contains(body, "<th>Runtime</th>") {
		t.Error("pool table shown without a machine file")
	}
}

func TestAgentsPageShowsHoursAndPool(t *testing.T) {
	config, state, _ := isolateDirs(t)
	dir := filepath.Join(config, "jigz")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "machine.yaml"), `hours:
  timezone: UTC
  end: stop
  windows:
    - {days: [mon, tue, wed, thu, fri, sat, sun], start: "00:00", end: "23:59"}
pool:
  - {name: work, runtime: claude-code, cost_class: subscription, env: {API_KEY: `+secretValue+`}}
  - {name: spare, runtime: pi, command: pi-real}
`)
	if err := os.Chmod(filepath.Join(dir, "machine.yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	sdir := filepath.Join(state, "jigz")
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	mustWrite(t, filepath.Join(sdir, "pool.json"),
		`{"work":{"exhausted_until":"`+until+`","reason":"usage limit reached"},"spare":{"last_used":"`+time.Now().UTC().Format(time.RFC3339)+`"}}`)

	body := getPage(t, &app{sizes: newRepoSizes(), stateDir: t.TempDir()}, "/agents")
	for _, want := range []string{"UTC", "mon,tue,wed,thu,fri,sat,sun 00:00-23:59", "<td>stop</td>", "open", "work", "subscription",
		"exhausted until", "usage limit reached", "spare", "pi-real", "available", "always"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, secretValue) {
		t.Error("env value is in the page")
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
		Workflows: []workflowStatus{{Name: "build", PID: os.Getpid(), State: "running", OfflineReason: "outside working hours"}},
		Gate:      &gateStatus{Hold: "outside working hours", Runtime: "claude-code"},
	}
	raw, _ = json.Marshal(doc)
	mustWrite(t, path, string(raw))

	a := &app{sizes: newRepoSizes(), stateDir: dir}
	a.sup = &fleetSupervisor{}
	a.source = loopsrc.StatusDoc{Reader: rollup.Reader{}, Roots: a.projectRoots, Dir: filepath.Join(dir, "status")}
	body := getPage(t, a, "/live")
	for _, want := range []string{"offline: outside working hours", "claude-code"} {
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
