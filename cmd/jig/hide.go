package main

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/kinged007/jigz/internal/machine"
)

// pageShared marks a route that belongs to no page and is always served.
const pageShared = ""

// route is one dashboard route. Page is the name from machine.HidePages
// that hides it. Section, when set, is a name from machine.HideSections
// that hides this route even though its page is shown.
type route struct {
	pattern, page, section string
	handler                func(*app, http.ResponseWriter, *http.Request)
}

// routeTable lists every route of the dashboard. A route is served only
// through this table, so each one has a page.
var routeTable []route

func init() {
	routeTable = []route{
		{"GET /{$}", "operations", "", (*app).page},
		{"GET /live", "operations", "", (*app).live},
		{"GET /status", pageShared, "", (*app).status},
		{"GET /usage", "usage", "", (*app).usage},
		{"GET /runs", "runs", "", (*app).runs},
		{"GET /runs.csv", "runs", "", (*app).runsCSV},
		{"GET /questions", "questions", "", (*app).questionsPage},
		{"GET /runs/{owner}/{name}/{workflow}/{id}", "runs", "", (*app).runDetail},
		{"GET /runs/{owner}/{name}/{workflow}/log", "runs", "log_tail", (*app).runLog},
		{"GET /logs/{owner}/{name}/{workflow}", "logs", "", (*app).loopLog},
		{"GET /workflows", "workflows", "", (*app).workflows},
		{"GET /projects", "projects", "", (*app).projectsPage},
		{"GET /projects/{owner}/{name}", "projects", "", (*app).projectPage},
		{"GET /agents", "agents", "", (*app).agents},
		{"GET /settings", "settings", "", (*app).settings},
		{"GET /backups", "backups", "", (*app).backupsPage},
		{"GET /api/v1/fleet", "operations", "", (*app).apiFleet},
		{"GET /api/v1/projects", "projects", "", (*app).apiProjects},
		{"GET /api/v1/projects/{owner}/{name}", "projects", "", (*app).apiProject},
		{"GET /api/v1/runs", "runs", "", (*app).apiRuns},
		{"GET /api/v1/runs/{rest...}", "runs", "", (*app).apiRun},
		{"GET /api/v1/usage", "usage", "", (*app).apiUsage},
		{"GET /api/v1/metrics", "operations", "", (*app).apiMetrics},
		{"GET /api/v1/audit", "operations", "audit_log", (*app).apiAudit},
		{"GET /api/v1/questions", "questions", "", (*app).apiQuestions},
	}
}

// routeMux serves routeTable. The handler of a route is wrapped so that
// the hide check runs before it, whatever the handler does.
func (a *app) routeMux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range routeTable {
		mux.HandleFunc(rt.pattern, func(w http.ResponseWriter, r *http.Request) {
			if a.hideView().hidesRoute(rt) {
				http.NotFound(w, r)
				return
			}
			rt.handler(a, w, r)
		})
	}
	return mux
}

// pathMux matches a path against the patterns of routeTable without
// serving anything.
var pathMux = sync.OnceValue(func() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range routeTable {
		mux.HandleFunc(rt.pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return mux
})

func routeFor(pattern string) (route, bool) {
	i := slices.IndexFunc(routeTable, func(rt route) bool { return rt.pattern == pattern })
	if i < 0 {
		return route{}, false
	}
	return routeTable[i], true
}

// hideView is what the dashboard hides, as seen by templates and
// handlers. The zero value hides nothing.
type hideView struct {
	pages, sections map[string]bool
	// all hides every page. It is set when the settings could not be read
	// and no earlier reading exists.
	all bool
}

func (h hideView) hidesPage(name string) bool {
	return name != pageShared && (h.all || h.pages[name])
}

func (h hideView) hidesRoute(rt route) bool {
	return h.hidesPage(rt.page) || (rt.section != "" && h.sections[rt.section])
}

// HidesPage reports whether a page is hidden. Templates use it.
func (h hideView) HidesPage(name string) bool { return h.hidesPage(name) }

// HidesSection reports whether a section is hidden. Templates use it.
func (h hideView) HidesSection(name string) bool { return h.all || h.sections[name] }

// HidesLink reports whether the route that serves an internal link is
// hidden. A link no route serves counts as hidden.
func (h hideView) HidesLink(href string) bool {
	u, err := url.Parse(href)
	if err != nil {
		return true
	}
	_, pattern := pathMux().Handler(&http.Request{Method: http.MethodGet, URL: u})
	rt, ok := routeFor(pattern)
	return !ok || h.hidesRoute(rt)
}

// hider reads the dashboard block of the machine file and rereads it when
// the file changes, so an edit applies without a restart.
type hider struct {
	path string

	mu     sync.Mutex
	mod    time.Time
	size   int64
	seen   bool
	failed bool
	view   hideView
}

func newHider(path string) *hider { return &hider{path: path} }

// machineHider reads the machine file at its default path.
func machineHider() *hider {
	path, err := machine.DefaultPath()
	if err != nil {
		log.Printf("dashboard: %v", err)
	}
	return newHider(path)
}

func (h *hider) get() hideView {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := os.Stat(h.path)
	switch {
	case err != nil && !os.IsNotExist(err):
		h.fail(err)
		return h.view
	case err != nil:
		h.seen, h.mod, h.size, h.failed = true, time.Time{}, -1, false
		h.view = hideView{}
		return h.view
	case h.seen && st.ModTime().Equal(h.mod) && st.Size() == h.size:
		return h.view
	}
	d, err := machine.LoadDashboard(h.path)
	if err != nil {
		h.fail(err)
		h.mod, h.size = st.ModTime(), st.Size()
		return h.view
	}
	h.seen, h.failed, h.mod, h.size = true, false, st.ModTime(), st.Size()
	h.view = hideView{pages: set(d.Hide.Pages), sections: set(d.Hide.Sections)}
	return h.view
}

// fail keeps the last good reading. With none, every page is hidden.
func (h *hider) fail(err error) {
	if !h.failed {
		log.Printf("dashboard: %v", err)
	}
	h.failed = true
	if !h.seen {
		h.view = hideView{all: true}
	}
}

func set(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// hideView is what the dashboard hides now.
func (a *app) hideView() hideView {
	if a.hide == nil {
		return hideView{}
	}
	return a.hide.get()
}
