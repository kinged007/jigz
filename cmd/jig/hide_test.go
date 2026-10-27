package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/machine"
)

func hideApp(t *testing.T, yaml string) *app {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machine.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return &app{sizes: newRepoSizes(), stateDir: t.TempDir(), hide: newHider(path)}
}

func get(a *app, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "127.0.0.1"
	a.routes(newHostGuard("127.0.0.1")).ServeHTTP(rec, req)
	return rec
}

func concretePath(pattern string) string {
	p := strings.TrimPrefix(pattern, "GET ")
	for old, v := range map[string]string{
		"{owner}": "o", "{name}": "n", "{workflow}": "w", "{id}": "1", "{rest...}": "o/n/w/1", "{$}": "",
	} {
		p = strings.ReplaceAll(p, old, v)
	}
	return p
}

func TestRouteTableNames(t *testing.T) {
	pages := map[string]bool{}
	for _, n := range machine.HidePages {
		pages[n.Name] = false
	}
	sections := map[string]bool{}
	for _, n := range machine.HideSections {
		sections[n.Name] = true
	}
	seen := map[string]bool{}
	for _, rt := range routeTable {
		if seen[rt.pattern] {
			t.Errorf("%s is listed twice", rt.pattern)
		}
		seen[rt.pattern] = true
		if rt.page != pageShared {
			if _, ok := pages[rt.page]; !ok {
				t.Errorf("%s: page %q is not in machine.HidePages", rt.pattern, rt.page)
			}
			pages[rt.page] = true
		}
		if rt.section != "" && !sections[rt.section] {
			t.Errorf("%s: section %q is not in machine.HideSections", rt.pattern, rt.section)
		}
		if rt.handler == nil {
			t.Errorf("%s has no handler", rt.pattern)
		}
	}
	for name, used := range pages {
		if !used {
			t.Errorf("page %q is served by no route", name)
		}
	}
}

func TestHiddenPageRoutesAnswer404(t *testing.T) {
	for _, p := range machine.HidePages {
		a := hideApp(t, "dashboard:\n  hide:\n    pages: ["+p.Name+"]\n")
		for _, rt := range routeTable {
			path := concretePath(rt.pattern)
			rec := get(a, path)
			if rt.page == p.Name {
				if rec.Code != http.StatusNotFound || rec.Body.String() != "404 page not found\n" {
					t.Errorf("hide %s: GET %s = %d %q, want the plain 404", p.Name, path, rec.Code, rec.Body.String())
				}
			}
		}
	}
}

func TestDefaultHidesNothing(t *testing.T) {
	a := hideApp(t, "")
	for _, rt := range routeTable {
		if strings.Contains(rt.pattern, "{") && !strings.HasSuffix(rt.pattern, "{$}") {
			continue
		}
		if rec := get(a, concretePath(rt.pattern)); rec.Code == http.StatusNotFound {
			t.Errorf("%s = 404 with nothing hidden", rt.pattern)
		}
	}
	body := get(a, "/agents").Body.String()
	for _, n := range []string{"/", "/runs", "/questions", "/usage", "/workflows", "/projects", "/agents", "/settings"} {
		if !strings.Contains(body, `<a href="`+n+`"`) {
			t.Errorf("sidebar lacks %s by default", n)
		}
	}
}

func TestSidebarOmitsHiddenPages(t *testing.T) {
	a := hideApp(t, "dashboard:\n  hide:\n    pages: [runs, questions, settings]\n")
	body := get(a, "/agents").Body.String()
	for _, n := range []string{"/runs", "/questions", "/settings"} {
		if strings.Contains(body, `href="`+n+`"`) {
			t.Errorf("sidebar links the hidden page %s", n)
		}
	}
	for _, n := range []string{"/usage", "/workflows", "/projects", "/agents"} {
		if !strings.Contains(body, `<a href="`+n+`"`) {
			t.Errorf("sidebar lacks the shown page %s", n)
		}
	}
}

func TestHiddenSectionRoutesAnswer404(t *testing.T) {
	a := hideApp(t, "dashboard:\n  hide:\n    sections: [log_tail, audit_log]\n")
	for _, p := range []string{"/runs/o/n/w/log", "/api/v1/audit"} {
		if rec := get(a, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	if rec := get(a, "/runs"); rec.Code != http.StatusOK {
		t.Errorf("GET /runs = %d, want the page to stay", rec.Code)
	}
}

func render(t *testing.T, name string, data any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := page.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestHiddenSectionsLeaveTheRenderedPage(t *testing.T) {
	hide := func(pages, sections []string) hideView {
		return hideView{pages: set(pages), sections: set(sections)}
	}
	cases := []struct {
		name, marker string
		render       func(hideView) string
		pages        []string
		section      string
	}{
		{"audit_log on Operations", "<h2>Audit log</h2>", func(h hideView) string {
			return render(t, "page", pageData{Hide: h})
		}, nil, "audit_log"},
		{"audit_log on a project", "<h2>Audit</h2>", func(h hideView) string {
			return render(t, "project", projectView{pageData: pageData{Hide: h}, Slug: "o/n"})
		}, nil, "audit_log"},
		{"effective_config on a project", "<h3>Effective config</h3>", func(h hideView) string {
			return render(t, "project", projectView{pageData: pageData{Hide: h}, Slug: "o/n"})
		}, nil, "effective_config"},
		{"backups on a project", "<h2>Backups</h2>", func(h hideView) string {
			return render(t, "project", projectView{pageData: pageData{Hide: h}, Slug: "o/n"})
		}, []string{"backups"}, ""},
		{"log_tail on a run", `id="pane-engine"`, func(h hideView) string {
			return render(t, "run", runDetailData{pageData: pageData{Hide: h}, Logs: logsView{Engine: logCard{URL: "/x"}}})
		}, nil, "log_tail"},
		{"agent_log on a run", `id="pane-agent"`, func(h hideView) string {
			return render(t, "run", runDetailData{pageData: pageData{Hide: h}, Logs: logsView{AgentURL: "/x"}})
		}, nil, "agent_log"},
		{"session on a run", `id="part-session"`, func(h hideView) string {
			return render(t, "run", runDetailData{pageData: pageData{Hide: h}, Parts: runParts{Hide: h}})
		}, nil, "session"},
		{"session on a live run", `id="part-session"`, func(h hideView) string {
			return render(t, "runlive", runLiveData{pageData: pageData{Hide: h}, Parts: runParts{Hide: h}})
		}, nil, "session"},
		{"agent_log on a live run", `id="pane-agent"`, func(h hideView) string {
			return render(t, "runlive", runLiveData{pageData: pageData{Hide: h}, Parts: runParts{Hide: h}})
		}, nil, "agent_log"},
	}
	for _, c := range cases {
		if !strings.Contains(c.render(hideView{}), c.marker) {
			t.Errorf("%s: %q is missing by default", c.name, c.marker)
		}
		var sections []string
		if c.section != "" {
			sections = []string{c.section}
		}
		if strings.Contains(c.render(hide(c.pages, sections)), c.marker) {
			t.Errorf("%s: %q is still rendered when hidden", c.name, c.marker)
		}
	}
}

func TestHiddenPagesAreNotLinked(t *testing.T) {
	h := hideView{pages: set([]string{"projects", "agents", "workflows", "settings", "backups"})}
	for _, c := range []struct{ tmpl, href string }{
		{"project", `href="/agents"`},
		{"project", `href="/workflows"`},
		{"settings", `href="/backups"`},
		{"backups", `href="/settings"`},
		{"logs", `href="/workflows"`},
	} {
		var data any = projectView{pageData: pageData{Hide: h}, Slug: "o/n"}
		switch c.tmpl {
		case "settings":
			data = settingsView{pageData: pageData{Hide: h}}
		case "backups":
			data = backupsView{pageData: pageData{Hide: h}}
		case "logs":
			data = struct {
				pageData
				Project, Workflow, Why string
				Lines                  []string
			}{pageData: pageData{Hide: h}}
		}
		if strings.Contains(render(t, c.tmpl, data), c.href) {
			t.Errorf("%s links the hidden %s", c.tmpl, c.href)
		}
	}
}

func TestHidesLink(t *testing.T) {
	h := hideView{pages: set([]string{"runs"}), sections: set([]string{"log_tail"})}
	for href, want := range map[string]bool{
		"/runs?project=o/n":          true,
		"/runs/o/n/w/1":              true,
		"/projects/o/n":              false,
		"/settings":                  false,
		"/nowhere":                   true,
		"/runs/o/n/w/log?issue=1":    true,
		"/api/v1/projects/o/n":       false,
		"/logs/o/n/w":                false,
		"/runs/../projects":          false,
		"/projects?period=7d&sort=x": false,
	} {
		if got := h.HidesLink(href); got != want {
			t.Errorf("HidesLink(%q) = %v, want %v", href, got, want)
		}
	}
}

func TestHideConfigErrors(t *testing.T) {
	dir := t.TempDir()
	for yaml, want := range map[string]string{
		"dashboard:\n  hide:\n    pages: [nope]\n":    "dashboard.hide.pages: \"nope\" is not a known name (use operations, runs, questions",
		"dashboard:\n  hide:\n    sections: [nope]\n": "dashboard.hide.sections: \"nope\" is not a known name (use agent_log, session, log_tail, audit_log, effective_config)",
		"dashboard:\n  hide:\n    page: [runs]\n":     "field page not found",
		"dashboard:\n  hid: {}\n":                     "field hid not found",
	} {
		path := filepath.Join(dir, "m.yaml")
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := machine.LoadDashboard(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("LoadDashboard(%q) = %v, want %q", yaml, err, want)
		}
		if _, err := machine.Load(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Load(%q) = %v, want %q", yaml, err, want)
		}
	}
}

func TestHideReloadsAndFailsClosed(t *testing.T) {
	a := hideApp(t, "")
	if rec := get(a, "/agents"); rec.Code != http.StatusOK {
		t.Fatalf("GET /agents = %d", rec.Code)
	}
	write := func(s string) {
		if err := os.WriteFile(a.hide.path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("dashboard:\n  hide:\n    pages: [agents]\n")
	if rec := get(a, "/agents"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /agents after an edit = %d, want 404", rec.Code)
	}
	write("dashboard:\n  hide:\n    pages: [bogus]\n")
	if rec := get(a, "/agents"); rec.Code != http.StatusNotFound {
		t.Errorf("an invalid edit dropped the last good settings: GET /agents = %d", rec.Code)
	}
	if rec := get(a, "/usage"); rec.Code != http.StatusOK {
		t.Errorf("GET /usage = %d, want 200", rec.Code)
	}

	b := hideApp(t, "dashboard:\n  hide:\n    pages: [bogus]\n")
	for _, p := range []string{"/", "/runs", "/api/v1/fleet", "/settings"} {
		if rec := get(b, p); rec.Code != http.StatusNotFound {
			t.Errorf("unreadable settings: GET %s = %d, want 404", p, rec.Code)
		}
	}
	if rec := get(b, "/status"); rec.Code != http.StatusOK {
		t.Errorf("GET /status = %d, want 200", rec.Code)
	}
}

func TestHideIgnoresOtherMachineErrors(t *testing.T) {
	a := hideApp(t, "hours:\n  timezone: Nowhere/Land\ndashboard:\n  hide:\n    pages: [agents]\n")
	if rec := get(a, "/agents"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /agents = %d, want 404", rec.Code)
	}
	if rec := get(a, "/usage"); rec.Code != http.StatusOK {
		t.Errorf("GET /usage = %d, want 200", rec.Code)
	}
}
