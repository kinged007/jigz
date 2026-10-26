package main

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	machine "github.com/kinged007/jigz/internal/host"
	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/supervise"
)

//go:embed templates/*.html
var templateFS embed.FS

var page = template.Must(template.New("page").Funcs(funcs).ParseFS(templateFS, "templates/*.html"))

// funcs are the formatting helpers the templates need. Go's html/template
// has no thousands-separator, duration, or currency filter, and doing the
// grouping in the browser would mean shipping every number as text and
// reformatting it on each tick.
var funcs = template.FuncMap{
	"commas":   commas,
	"liveHref": liveHref,
	"since":    since,
	"at":       at,
	"pct":      pct,
	"cpu":      cpuPct,
	"size":     size,
	"gib":      gib,
	"agent":    agentLabel,
	"utc":      utc,
	"trunc":    trunc,
}

// size renders a directory size in whichever unit keeps it readable.
// A repository's own directory is routinely a few hundred kilobytes, so
// stopping at MiB would report those as 0 MiB and read as an empty
// directory.
func size(b int64) string {
	const (
		kiB = 1 << 10
		miB = 1 << 20
		giB = 1 << 30
	)
	switch {
	case b < 0:
		return "\u2014"
	case b < miB:
		return fmt.Sprintf("%.0f KiB", float64(b)/kiB)
	case b < giB:
		return fmt.Sprintf("%.0f MiB", float64(b)/miB)
	default:
		return fmt.Sprintf("%.1f GiB", float64(b)/giB)
	}
}

// cpuPct renders a CPU percentage, or a dash when there is no figure yet.
// The first reading has no previous sample to measure against, so it
// reports -1 rather than a misleading number.
func cpuPct(v float64) string {
	if v < 0 {
		return "\u2014"
	}
	return fmt.Sprintf("%.0f%%", v)
}

// pct renders a used/total pair as a percentage, or a dash when the
// total is zero, which is what an unreadable reading looks like.
func pct(used, total int64) string {
	if total <= 0 {
		return "\u2014"
	}
	return fmt.Sprintf("%.0f%%", float64(used)/float64(total)*100)
}

// gib renders a byte count in binary units, to one decimal. Metric units
// would read smaller, and a dashboard about a filesystem should use the
// same base the filesystem does.
func gib(b int64) string {
	if b <= 0 {
		return "\u2014"
	}
	return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
}

// agentLabel turns an adapter name into something readable. The names are
// the config's own, so an unknown one is passed through rather than
// hidden: a new adapter should be recognisable, not blank.
func agentLabel(kind string) string {
	switch kind {
	case "":
		return ""
	case "claude-code":
		return "Claude"
	case "pi":
		return "Pi"
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	default:
		return kind
	}
}

// commas groups a number with thousands separators: 6978753 renders as
// 6,978,753. Token counts run into the millions within a day, and an
// ungrouped one is genuinely hard to read at a glance.
func commas(v any) string {
	var n int64
	switch t := v.(type) {
	case int:
		n = int64(t)
	case int64:
		n = t
	default:
		return fmt.Sprint(v)
	}
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// at renders a timestamp in the machine's own time zone. The page
// reports durations on their own, so the one absolute time an operator
// needs is the moment a loop picked an issue up.
func at(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04 MST")
}

// since renders how long ago a timestamp was, in the largest two units.
// The figure is read from the pickup time rather than the run's own
// start, so a retry does not make an hour of work look like a minute.
func since(started string) string {
	t, err := time.Parse(time.RFC3339, started)
	if err != nil {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		days := int(d.Hours()) / 24
		return fmt.Sprintf("%dd %dh", days, int(d.Hours())%24)
	}
}

// activeRun is one busy agent, flattened across every loop so the page can
// show the whole fleet's current work in one place.
type activeRun struct {
	Repo       string
	Loop       string
	Identifier string
	State      string
	Turns      int
	Tokens     int64
	// Agent is the loop's agent adapter, empty when the setting is unset
	// and the engine's own default applies.
	Agent string
	Model string
	// StartedAt is when the engine started this attempt.
	StartedAt string
	// PickedUp is when the loop first took the issue on its current
	// pass, retries included. It falls back to this attempt's own start
	// when the loop's history has nothing for the issue, which is the
	// first attempt still in flight.
	PickedUp string
}

// active collects every running agent in the fleet. agents maps
// repo/loop to the agent adapter it runs with, which the config knows and
// the engine does not report.
func active(f rollup.Fleet, agents map[string]string) []activeRun {
	var out []activeRun
	for _, l := range f.Loops {
		for _, r := range l.Running {
			pickedUp := r.PickUp
			if pickedUp == "" {
				pickedUp = r.StartedAt
			}
			run := activeRun{
				Repo:       l.Repo,
				Loop:       l.Loop,
				Agent:      agents[l.Repo+"/"+l.Loop],
				Identifier: r.Identifier,
				State:      r.State,
				Turns:      r.Turns,
				Tokens:     r.Tokens,
				Model:      r.Model,
				StartedAt:  r.StartedAt,
				PickedUp:   pickedUp,
			}
			out = append(out, run)
		}
	}
	// Oldest first: the run that has been going longest is the one an
	// operator wants to see, and it is the one most likely stuck.
	sort.SliceStable(out, func(i, j int) bool { return out[i].PickedUp < out[j].PickedUp })
	return out
}

// hostGuard decides which Host header values are served.
//
// A browser on another site can reach a private address with an arbitrary
// Host header (DNS rebinding), so the header is checked rather than
// trusted. The loopback names are always accepted.
type hostGuard struct {
	// addrs are the specific addresses accepted, from a --host that names
	// one interface. A request to that address is unambiguously ours, so
	// the check stays on and reaching the page over the network is safe
	// from rebinding.
	addrs map[string]bool
	// wildcard is set when --host is 0.0.0.0 or ::. Which interface a
	// client used is unknowable then, so there is no Host value left to
	// check against: the page has to answer on whichever address it was
	// reached at, and the rebinding protection is necessarily dropped.
	// The default is loopback, and startServer says so loudly, so this
	// is only reached by asking for it explicitly.
	wildcard bool
	// v6 is set when the wildcard was written as ::.
	v6 bool
}

// allows reports whether a Host header value is served.
func (g hostGuard) allows(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch host {
	case "127.0.0.1", "localhost", "[::1]", "::1":
		return true
	}
	return g.wildcard || g.addrs[host]
}

// guard wraps a handler in the Host check.
func (g hostGuard) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.allows(r.Host) {
			http.Error(w, "this page does not answer for this Host", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// newHostGuard derives the check from the --host value being bound.
func newHostGuard(host string) hostGuard {
	switch host {
	case "0.0.0.0", "::", "":
		return hostGuard{wildcard: true, v6: host == "::", addrs: map[string]bool{}}
	}
	// A bare address and a bracketed IPv6 literal name the same host.
	return hostGuard{addrs: map[string]bool{
		host:                     true,
		strings.Trim(host, "[]"): true,
	}}
}

// isLoopbackHost reports whether binding to host exposes nothing beyond
// this machine. "localhost" is a loopback bind even though it is not an
// address literal.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func serve(a *app, g hostGuard, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(g.bindAddr(), strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("control page: %w", err)
	}
	return (&http.Server{Handler: a.routes(g), ReadHeaderTimeout: 10 * time.Second}).Serve(ln)
}

func (a *app) routes(g hostGuard) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.page)
	mux.HandleFunc("GET /live", a.live)
	mux.HandleFunc("GET /usage", a.usage)
	mux.HandleFunc("GET /runs", a.runs)
	mux.HandleFunc("GET /runs.csv", a.runsCSV)
	mux.HandleFunc("GET /questions", a.questionsPage)
	mux.HandleFunc("GET /runs/{owner}/{name}/{workflow}/{id}", a.runDetail)
	mux.HandleFunc("GET /runs/{owner}/{name}/{workflow}/log", a.runLog)
	mux.HandleFunc("GET /logs/{owner}/{name}/{workflow}", a.loopLog)
	mux.HandleFunc("GET /workflows", a.workflows)
	mux.HandleFunc("GET /projects", a.projectsPage)
	mux.HandleFunc("GET /projects/{owner}/{name}", a.projectPage)
	mux.HandleFunc("GET /agents", a.agents)
	mux.HandleFunc("GET /settings", a.settings)
	mux.HandleFunc("GET /backups", a.backupsPage)
	mux.HandleFunc("GET /status", a.status)
	mux.HandleFunc("GET /api/v1/fleet", a.apiFleet)
	mux.HandleFunc("GET /api/v1/projects", a.apiProjects)
	mux.HandleFunc("GET /api/v1/projects/{owner}/{name}", a.apiProject)
	mux.HandleFunc("GET /api/v1/runs", a.apiRuns)
	mux.HandleFunc("GET /api/v1/runs/{rest...}", a.apiRun)
	mux.HandleFunc("GET /api/v1/usage", a.apiUsage)
	mux.HandleFunc("GET /api/v1/metrics", a.apiMetrics)
	mux.HandleFunc("GET /api/v1/audit", a.apiAudit)
	mux.HandleFunc("GET /api/v1/questions", a.apiQuestions)
	return g.guard(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "this page does not answer a request from another origin", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// sameOrigin refuses a non-GET request a browser sent from another site.
// The page has no authentication, so binding loopback is what keeps it
// local, and that alone does not stop a page on the internet posting a
// form to 127.0.0.1: the Origin header is what tells the two apart. A
// request without one is not a cross-site form post.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// bindAddr is the address the listener uses. A wildcard host binds the
// empty string, which is what net.Listen means by "every interface".
func (g hostGuard) bindAddr() string {
	if g.wildcard {
		return ""
	}
	for a := range g.addrs {
		if a != "" && !strings.HasPrefix(a, "[") {
			return a
		}
	}
	return ""
}

// repoView is a supervised repository with its live figures already
// totalled. Summing here rather than in the template keeps the markup
// from repeating a per-loop number once per loop.
type repoView struct {
	supervise.Repo
	// Running is the number of agents working across this repository's
	// loops, and Tokens the all-time token count.
	Running int
	Tokens  int64
	// Observed is how many of the repository's loops answered.
	Observed int
	// Disk is what this repository's directory occupies, and DiskKnown is
	// false until the first measurement lands. Walking a workspace tree is
	// I/O the render must not do, so it is measured on a timer.
	Disk      int64
	DiskKnown bool
	// Period and Today are the tokens used in the selected period and since
	// UTC midnight, and Share is Period as a percentage of every project's.
	Period, Today int64
	Share         int
}

// IsRunning is whether the child process is up.
func (v repoView) IsRunning() bool { return v.State == supervise.StateRunning }

// pageData is everything the template renders.
type pageData struct {
	Now        time.Time
	Fleet      rollup.Fleet
	Repos      []repoView
	ConfigPath string
	// Sys is the load of this machine. Named for the system rather than
	// the host because Host is the hostname the request arrived on.
	Sys machine.Metrics
	Msg string
	Err string
	// Exposed is set when the page is bound beyond loopback.
	Exposed bool
	// Active is every busy agent in the fleet, flattened.
	Active []activeRun
	// Nav is the sidebar entry of the page being rendered, and Title the
	// page's name in the browser tab.
	Nav, Title string
	// Ops is set on the Operations page only.
	Ops opsView
	// Sorts are the orderings the Operations page offers.
	Sorts []string
	// Unite marks a page that spans every registered repository, which is
	// how a later --unite run recognises this one before joining it.
	Unite bool
}

func (a *app) data(msg, errMsg string) pageData {
	f := a.src().Fleet()
	totals := map[string]*repoView{}
	agents := map[string]string{}
	for _, r := range a.repos() {
		v := &repoView{Repo: r}
		if a.sizes != nil {
			if n, ok := a.sizes.get(r.Slug); ok {
				v.Disk, v.DiskKnown = n, true
			}
		}
		totals[r.Slug] = v
		for loop, kind := range r.Agents {
			agents[r.Slug+"/"+loop] = kind
		}
	}
	for _, l := range f.Loops {
		v, ok := totals[l.Repo]
		if !ok {
			continue
		}
		v.Observed++
		v.Running += len(l.Running)
		v.Tokens += l.Tokens
	}
	repos := make([]repoView, 0, len(totals))
	for _, v := range totals {
		repos = append(repos, *v)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Slug < repos[j].Slug })
	return pageData{
		Now:        time.Now(),
		Fleet:      f,
		Repos:      repos,
		ConfigPath: a.configPath,
		Msg:        msg,
		Err:        errMsg,
		Exposed:    a.exposed,
		Active:     active(f, agents),
		Sys:        a.readSys(),
		Unite:      a.unite,
	}
}

// readSys takes a host reading. It is called once per render, which is
// every few seconds, so the CPU figure is measured over that interval.
func (a *app) readSys() machine.Metrics {
	if a.sys == nil {
		return machine.Metrics{CPU: -1}
	}
	return a.sys.Read()
}

// dataFor is data for one request.
func (a *app) dataFor(r *http.Request, msg, errMsg string) pageData {
	d := a.data(msg, errMsg)
	d.Nav = "ops"
	return d
}

func (a *app) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "page", a.opsFor(r, "", "")); err != nil {
		// A template that fails partway has already written a header, so
		// http.Error's own is superfluous and its message never reaches
		// the browser. Log it: otherwise the page is silently truncated
		// and nothing says why.
		log.Printf("page: %v", err)
	}
}

// live renders just the part that changes, so the page can refresh
// without a rebuild step on the client.
func (a *app) live(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "live", a.opsFor(r, "", "")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// status renders the header's load figures and clock. They are their own
// fragment because they sit outside the live block, and the CPU
// percentage has to be re-sampled to mean anything.
func (a *app) status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "status", a.dataFor(r, "", "")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *app) auditAs(actor, action, project, target, result string) {
	if a.store == nil {
		return
	}
	if err := a.store.AddAudit(time.Now(), actor, action, project, target, result); err != nil {
		log.Printf("audit: %v", err)
	}
}
