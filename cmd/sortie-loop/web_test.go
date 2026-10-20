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

	"github.com/kinged007/sortie-loop/internal/loopsrc"
	"github.com/kinged007/sortie-loop/internal/rollup"
	"github.com/kinged007/sortie-loop/internal/supervise"
)

func TestHostGuardAllows(t *testing.T) {
	loopback := newHostGuard("127.0.0.1")
	named := newHostGuard("10.0.0.5")
	wildcard := newHostGuard("0.0.0.0")
	for _, c := range []struct {
		name string
		g    hostGuard
		host string
		want bool
	}{
		{"loopback name always answers", named, "localhost:7677", true},
		{"v6 loopback always answers", named, "[::1]:7677", true},
		{"the bound address answers", named, "10.0.0.5:7677", true},
		{"another address does not", named, "evil.example:7677", false},
		{"a wildcard bind answers anything", wildcard, "evil.example:7677", true},
		{"the default bind answers loopback", loopback, "127.0.0.1:7677", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.g.allows(c.host); got != c.want {
				t.Errorf("allows(%q) = %v", c.host, got)
			}
		})
	}
}

func TestLinkBuilderBuild(t *testing.T) {
	l := linkBuilder{Host: "127.0.0.1"}
	if got := l.Build(&rollup.Loop{Port: 7678}); got != "http://127.0.0.1:7678/" {
		t.Errorf("default link = %q", got)
	}
	v6 := linkBuilder{Host: "::1"}
	if got := v6.Build(&rollup.Loop{Port: 7678}); got != "http://[::1]:7678/" {
		t.Errorf("v6 link = %q", got)
	}
	l = linkBuilder{Host: "h", Template: "https://{repo}.loops.example.com/{loop}/{port}/{host}"}
	if got := l.Build(&rollup.Loop{Repo: "owner/repo", Loop: "build", Port: 9}); got != "https://owner/repo.loops.example.com/build/9/h" {
		t.Errorf("template link = %q", got)
	}
}

// The page and its two fragments render with no registry at all: a fresh
// machine is a valid empty fleet, not an error.
func TestFragmentsRenderEmptyFleet(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	a := &app{sizes: newRepoSizes(), stateDir: t.TempDir()}
	g := newHostGuard("127.0.0.1")
	h := a.routes(g)

	for _, path := range []string{"/", "/live", "/status"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", path, nil)
			req.Host = "127.0.0.1"
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s content type = %q", path, ct)
			}
		})
	}
}

// A browser on another site can reach a private address with an arbitrary
// Host header, so the page refuses one it was not configured for.
func TestHostHeaderRefused(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	a := &app{sizes: newRepoSizes(), stateDir: t.TempDir()}
	h := a.routes(newHostGuard("127.0.0.1"))
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "rebind.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("foreign Host = %d, want 403", rec.Code)
	}
}

// A registered repository renders with its loops, even with no engine
// answering: the status document and the registry carry enough.
func TestPageRendersRegisteredRepo(t *testing.T) {
	root := t.TempDir()
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", t.TempDir())

	reg := registry{Entries: map[string]registryEntry{
		root: {
			RepoName:  "owner/repo",
			UpdatedAt: time.Now(),
			PID:       os.Getpid(),
			Loops:     []loopEndpoint{{Repo: root, RepoName: "owner/repo", Loop: "build", Port: 1}},
		},
	}}
	dir := filepath.Join(cache, "sortie-loop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(dir, "registry.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := statusPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := statusDoc{
		SchemaVersion: statusSchemaVersion,
		PID:           os.Getpid(),
		Repo:          root,
		RepoName:      "owner/repo",
		StartedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
		State:         "running",
		Workflows:     []workflowStatus{{Name: "build", Port: 1, PID: os.Getpid(), State: "running"}},
	}
	raw, _ = json.Marshal(doc)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	a := &app{sizes: newRepoSizes(), stateDir: dir}
	a.sup = &fleetSupervisor{}
	a.source = loopsrc.StatusDoc{Reader: rollup.Reader{}, Roots: a.projectRoots, Dir: filepath.Join(dir, "status")}
	h := a.routes(newHostGuard("127.0.0.1"))

	for _, path := range []string{"/", "/live", "/projects"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "127.0.0.1"
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "owner/repo") {
			t.Errorf("GET %s does not name the repository", path)
		}
	}
}

// A workflow validates in the environment its loop runs with. The fleet
// cascade central validates against does not exist here, so an empty
// tracker key from that environment must not turn a real workflow red.
func TestValidateWorkflowUsesTheReposEnv(t *testing.T) {
	if _, err := resolveSortieBin(); err != nil {
		t.Skip("no sortie binary on PATH")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".sortie", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sortie", "config.yaml"), []byte("repo: acme/app\ntoken: test-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sortie", ".env.loop"), []byte("SORTIE_TRACKER_PROJECT=acme/app\nSORTIE_TRACKER_API_KEY=test-token\nSORTIE_LOOP_WORKSPACES="+filepath.Join(dir, ".sortie", "workspaces")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `---
tracker:
  kind: github
  api_key: $SORTIE_TRACKER_API_KEY
  project: $SORTIE_TRACKER_PROJECT
  active_states: [todo]
  terminal_states: [done]
  query_filter: "label:agent:build"
agent:
  kind: pi
  command: pi
  max_turns: 2
---
work the issue.
`
	path := filepath.Join(dir, ".sortie", "workflows", "WORKFLOW.build.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := validateWorkflow(dir, path)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !res.Valid {
		t.Fatalf("valid workflow rejected: %+v", res.Errors)
	}
}

// A workflow with a cap shows its spend against it, and a paused one says
// why, from the status document alone.
func TestLoopRowShowsBudget(t *testing.T) {
	p := &opsProject{Status: &loopsrc.StatusInfo{
		Liveness: loopsrc.Live,
		Workflows: []loopsrc.WorkflowInfo{{
			Name:  "build",
			State: "running",
			Budget: &loopsrc.BudgetInfo{
				Period: "weekly", TokensUsed: 1234, Cap: 10000, Paused: true, PausedBy: "budget",
			},
		}},
	}}
	p.Repo.State = supervise.StateRunning
	row := loopRow(&rollup.Loop{Loop: "build", Port: 7678, Live: true}, p)
	if row.Budget != "1,234 / 10,000 tokens" {
		t.Errorf("budget = %q", row.Budget)
	}
	if row.BudgetPct != 12 {
		t.Errorf("budget percent = %d", row.BudgetPct)
	}
	if !row.Paused || row.PausedBy != "budget" {
		t.Errorf("paused = %v by %q", row.Paused, row.PausedBy)
	}
}

// A loop started with no server has no state API, so its status document
// is the only word on it: it is running, not "not answering".
func TestLoopRowWithoutServerUsesStatusDoc(t *testing.T) {
	p := &opsProject{Status: &loopsrc.StatusInfo{
		Liveness:  loopsrc.Live,
		Workflows: []loopsrc.WorkflowInfo{{Name: "build", State: "running"}},
	}}
	p.Repo.State = supervise.StateRunning
	row := loopRow(&rollup.Loop{Loop: "build", Port: 0}, p)
	if row.State != "running" {
		t.Errorf("state = %q, want running", row.State)
	}
}
