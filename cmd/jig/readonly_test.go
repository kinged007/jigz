package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/supervise"
)

func readOnlyServer(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	a := &app{sizes: newRepoSizes(), stateDir: t.TempDir()}
	return a.routes(newHostGuard("127.0.0.1"))
}

// The dashboard can view and nothing else: every route that used to
// change something is gone, for any method.
func TestRemovedRoutesAreGone(t *testing.T) {
	h := readOnlyServer(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/action"},
		{"GET", "/action"},
		{"POST", "/projects/new"},
		{"GET", "/projects/new"},
		{"POST", "/projects/owner/name/remove"},
		{"GET", "/projects/owner/name/remove"},
		{"GET", "/edit/workflow/owner/name/plan"},
		{"POST", "/edit/validate"},
		{"POST", "/edit/apply"},
		{"GET", "/edit/validate"},
		{"POST", "/backups/restore"},
		{"GET", "/backups/restore"},
	} {
		code := serveForm(h, c.method, c.path, "op=stop&repo=owner/name", "").Code
		if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405", c.method, c.path, code)
		}
	}
}

func TestReadOnlyMethods(t *testing.T) {
	h := readOnlyServer(t)
	for _, path := range []string{"/", "/live", "/usage", "/runs", "/questions", "/workflows", "/projects", "/agents", "/settings", "/backups", "/api/v1/fleet"} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			if code := serveForm(h, method, path, "", "").Code; code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", method, path, code)
			}
		}
	}
}

// No page offers a form that posts, a control for a removed route, or a
// link to a removed page.
func TestPagesOfferNoControls(t *testing.T) {
	h := readOnlyServer(t)
	var pages []string
	for _, path := range []string{"/", "/live", "/usage", "/runs", "/questions", "/workflows", "/projects", "/agents", "/settings", "/backups", "/status"} {
		rec := serveForm(h, "GET", path, "", "")
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		pages = append(pages, rec.Body.String())
	}
	project := opsProject{
		repoView: repoView{Repo: supervise.Repo{Slug: "owner/repo", State: supervise.StateRunning}},
		Loops:    []opsLoop{{L: &rollup.Loop{Repo: "owner/repo", Loop: "plan"}, State: "running", Class: "running", Paused: true}},
		CanWake:  true, CanCleanup: true,
	}
	var buf bytes.Buffer
	d := pageData{Ops: opsView{Projects: []opsProject{project}}}
	for _, name := range []string{"livebody", "live", "page"} {
		buf.Reset()
		if err := page.ExecuteTemplate(&buf, name, d); err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		pages = append(pages, buf.String())
	}
	for i, body := range pages {
		lower := strings.ToLower(body)
		for _, bad := range []string{`method="post"`, `action="/action"`, `/projects/new`, `/edit/`, `/backups/restore`, `name="op"`} {
			if strings.Contains(lower, bad) {
				t.Errorf("page %d contains %q", i, bad)
			}
		}
	}
}
