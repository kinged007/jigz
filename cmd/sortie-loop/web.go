package main

import (
	"context"
	"embed"
	"errors"
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

	"github.com/kinged007/sortie-loop/internal/auth"
	machine "github.com/kinged007/sortie-loop/internal/host"
	"github.com/kinged007/sortie-loop/internal/loopsrc"
	"github.com/kinged007/sortie-loop/internal/rollup"
	"github.com/kinged007/sortie-loop/internal/supervise"
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
	"usd":      usd,
	"since":    since,
	"at":       at,
	"pct":      pct,
	"cpu":      cpuPct,
	"size":     size,
	"gib":      gib,
	"agent":    agentLabel,
	"utc":      utc,
	"trunc":    trunc,
	"ctl":      ctl,
}

// ctl returns the attributes of a control that changes something: its title
// when the viewer is signed in, otherwise disabled with the read-only
// reason, so the control stays visible.
func ctl(authenticated bool, title string) template.HTMLAttr {
	if authenticated {
		if title == "" {
			return ""
		}
		return template.HTMLAttr(`title="` + template.HTMLEscapeString(title) + `"`)
	}
	return `disabled title="Read-only: sign in to use this action."`
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

// usd renders a cost estimate. The engine reports nil when a workflow
// configures no token_rates, and an em dash says "not priced" rather than
// "$0.00", which would read as free.
func usd(c *float64) string {
	if c == nil {
		return "—"
	}
	switch {
	case *c >= 100:
		return fmt.Sprintf("$%.0f", *c)
	case *c >= 1:
		return fmt.Sprintf("$%.2f", *c)
	case *c > 0:
		return fmt.Sprintf("$%.3f", *c)
	default:
		return "$0.00"
	}
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
	Port       int
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
	// Cost is this run's share of its loop's active cost estimate. The
	// engine reports cost per loop, not per run, so a loop running
	// several agents splits it by token share. With one agent on the loop
	// the share is the whole figure.
	Cost *float64
}

// active collects every running agent in the fleet. agents maps
// repo/loop to the agent adapter it runs with, which the config knows and
// the engine's state API does not report.
func active(f rollup.Fleet, agents map[string]string) []activeRun {
	var out []activeRun
	for _, l := range f.Loops {
		var loopTokens int64
		for _, r := range l.Running {
			loopTokens += r.Tokens
		}
		for _, r := range l.Running {
			pickedUp := r.PickUp
			if pickedUp == "" {
				pickedUp = r.StartedAt
			}
			run := activeRun{
				Repo:       l.Repo,
				Loop:       l.Loop,
				Agent:      agents[l.Repo+"/"+l.Loop],
				Port:       l.Port,
				Identifier: r.Identifier,
				State:      r.State,
				Turns:      r.Turns,
				Tokens:     r.Tokens,
				Model:      r.Model,
				StartedAt:  r.StartedAt,
				PickedUp:   pickedUp,
			}
			if l.Cost != nil {
				share := *l.Cost
				// A loop whose agents report no tokens cannot be split
				// meaningfully, so the estimate is left off the rows
				// rather than attributed to whichever one sorts first.
				if loopTokens > 0 {
					share *= float64(r.Tokens) / float64(loopTokens)
				} else {
					share = 0
				}
				run.Cost = &share
			}
			out = append(out, run)
		}
	}
	// Oldest first: the run that has been going longest is the one an
	// operator wants to see, and it is the one most likely stuck.
	sort.SliceStable(out, func(i, j int) bool { return out[i].PickedUp < out[j].PickedUp })
	return out
}

// serve runs the control page on the loopback interface only. It binds
// the literal address rather than a wildcard because the page starts
// agents and rewrites the fleetconfig.
// hostGuard decides which Host header values are served.
//
// The page can start and stop agents holding a GitHub write token, and a
// browser on another site can reach a private address with an arbitrary
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
		return hostGuard{wildcard: true, addrs: map[string]bool{}}
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
	mux.HandleFunc("GET /", a.page)
	mux.HandleFunc("GET /live", a.live)
	mux.HandleFunc("GET /usage", a.usage)
	mux.HandleFunc("GET /runs", a.runs)
	mux.HandleFunc("GET /runs.csv", a.runsCSV)
	mux.HandleFunc("GET /runs/{owner}/{name}/{workflow}/{id}", a.runDetail)
	mux.HandleFunc("GET /runs/{owner}/{name}/{workflow}/log", a.runLog)
	mux.HandleFunc("GET /logs/{owner}/{name}/{workflow}", a.loopLog)
	mux.HandleFunc("GET /workflows", a.workflows)
	mux.HandleFunc("GET /projects", a.projectsPage)
	mux.HandleFunc("GET /projects/{owner}/{name}", a.projectPage)
	mux.HandleFunc("GET /agents", a.agents)
	mux.HandleFunc("GET /settings", a.settings)
	mux.HandleFunc("GET /edit/workflow/{owner}/{name}/{workflow}", a.editGet("workflow", false))
	mux.HandleFunc("POST /edit/validate", a.editValidate)
	mux.HandleFunc("POST /edit/apply", a.editApply)
	mux.HandleFunc("GET /backups", a.backupsPage)
	mux.HandleFunc("POST /backups/restore", a.backupRestore)
	mux.HandleFunc("GET /status", a.status)
	mux.HandleFunc("POST /action", a.action)
	mux.HandleFunc("GET /api/v1/fleet", a.apiFleet)
	mux.HandleFunc("GET /api/v1/projects", a.apiProjects)
	mux.HandleFunc("GET /api/v1/projects/{owner}/{name}", a.apiProject)
	mux.HandleFunc("GET /api/v1/runs", a.apiRuns)
	mux.HandleFunc("GET /api/v1/runs/{rest...}", a.apiRun)
	mux.HandleFunc("GET /api/v1/usage", a.apiUsage)
	mux.HandleFunc("GET /api/v1/metrics", a.apiMetrics)
	mux.HandleFunc("GET /api/v1/audit", a.apiAudit)
	var h http.Handler = mux
	if a.auth != nil {
		h = a.auth.Protect(mux)
	}
	return g.guard(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "this page does not answer a request from another origin", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// sameOrigin refuses a state-changing request a browser sent from another
// site. The page has no login, so binding loopback is what keeps it local,
// and that alone does not stop a page on the internet posting a form to
// 127.0.0.1: the Origin header is what tells the two apart. A request
// without one is not a cross-site form post.
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

// IsRunning is whether the child process is up, which is what decides
// between offering start and offering stop.
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
	// Exposed is set when the page is bound beyond loopback. The page
	// shows the warning itself: whoever opens it over a network is
	// looking at a page that can start and stop agents holding a write
	// token, with no login, and should see that stated where they are.
	Exposed bool
	// Active is every busy agent in the fleet, flattened.
	Active []activeRun
	// Confirm and WhenIdle mark the page that asks before stopping the
	// whole fleet, and say which of the two stops is about to happen.
	Confirm  bool
	WhenIdle bool
	// Host is the hostname the request arrived on, so the per-loop
	// dashboard links point back at the machine the browser is already
	// talking to rather than at a loopback address that would only work
	// on this host.
	Host string
	// Link renders the per-loop dashboard links, honouring an operator's
	// URL template when one is configured.
	Link linkBuilder
	// Auth is the request's access state: who is signed in, the CSRF token
	// for forms, and whether a password is set at all.
	Auth auth.Info
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

// link builds the URL for a loop's own dashboard.
//
// The default points at the host the browser already reached us on, which
// is right for a loopback or LAN install and needs no configuration. A
// template exists for the case that is actually common in practice: the
// per-loop dashboards sit behind a reverse proxy or a wildcard DNS zone,
// and their real address is neither 127.0.0.1 nor a port. This is the
// same shape as the base-URL setting every other tool with a dashboard
// exposes -- Grafana's root_url, Prometheus's external_url, Jupyter's
// base_url -- because the address a user is given has to be the one that
// works from their browser, not the one the process happens to bind.
//
// Placeholders: {host} the request hostname, {port}, {loop}, {repo}. A
// path template is more robust than a port-as-subdomain one: HTTP/2 is
// 443-only, so a URL carrying a port needs a proxy that will route it
// and a certificate that covers it.
func (l linkBuilder) Build(r *rollup.Loop) string {
	if l.Template == "" {
		// Trim the brackets: JoinHostPort adds its own around an IPv6
		// literal, and a Host header of "[::1]" arrives bracketed, which
		// would otherwise come out as "[[::1]]".
		return "http://" + net.JoinHostPort(strings.Trim(l.Host, "[]"), strconv.Itoa(r.Port)) + "/"
	}
	rep := strings.NewReplacer(
		"{host}", l.Host,
		"{port}", strconv.Itoa(r.Port),
		"{loop}", r.Loop,
		"{repo}", r.Repo,
	)
	return rep.Replace(l.Template)
}

// requestHost is the hostname to build links with, or 127.0.0.1 when the
// Host header names none.
func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return "127.0.0.1"
	}
	return host
}

// linkBuilder renders the per-loop dashboard links.
type linkBuilder struct {
	// Host is the request hostname, used by the default template.
	Host string
	// Template overrides it when set.
	Template string
}

func (a *app) data(msg, errMsg, host, urlTemplate string) pageData {
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
		Host:       host,
		Link:       linkBuilder{Host: host, Template: urlTemplate},
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

// dataFor is data for one request, with its access state.
func (a *app) dataFor(r *http.Request, msg, errMsg string) pageData {
	d := a.data(msg, errMsg, requestHost(r), a.loopURLTemplate)
	d.Auth = auth.Session(r)
	d.Nav = "ops"
	return d
}

func (a *app) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q := r.URL.Query()
	if err := page.ExecuteTemplate(w, "page", a.opsFor(r, q.Get("msg"), q.Get("err"))); err != nil {
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

// action starts, stops, or restarts one repository, or brings the whole
// fleet down. It is a plain form post, so the page works without
// scripting.
func (a *app) action(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/?err="+query("bad form"), http.StatusSeeOther)
		return
	}
	slug, op := r.FormValue("repo"), r.FormValue("op")
	whenIdle := r.FormValue("when_idle") != ""
	// Stopping the fleet takes this process down with it, and there is
	// no way back from that, so the button asks first and this is the
	// answer. The confirmation page says which of the two stops is about
	// to happen, because the unticked box is a different promise.
	if op == "stopall" && r.FormValue("confirm") == "" {
		d := a.dataFor(r, "", "")
		d.Confirm, d.WhenIdle = true, whenIdle
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := page.ExecuteTemplate(w, "confirm", d); err != nil {
			log.Printf("confirm: %v", err)
		}
		return
	}
	var err error
	var note, target string
	stopping := false
	switch op {
	case "startall":
		a.sup.StartAll()
		note = "start all: every stopped project was started"
	case "drainall":
		err = a.drainAll()
		note = "drain all: each project stops as its loops go idle"
	case "wake":
		note, err = a.wake(r.Context(), slug)
	case "pause", "resume", "override":
		// Per workflow, not per repository: the request is the same file
		// the pause, resume and budget commands write.
		workflow := r.FormValue("workflow")
		root, _, ok := resolveRepo(slug, true, nil)
		switch {
		case !ok:
			err = fmt.Errorf("unknown project %q", slug)
		case workflow == "":
			err = fmt.Errorf("%s needs a workflow", op)
		default:
			var res budgetCmdResult
			if res, err = controlWorkflow(root, op, workflow); err == nil {
				switch {
				case res.Paused:
					note = op + " " + workflow + " on " + slug + ": paused"
				case res.Override:
					note = op + " " + workflow + " on " + slug + ": budget overridden for this period"
				default:
					note = op + " " + workflow + " on " + slug
				}
			}
		}
	case "cleanup":
		dry := r.FormValue("dry_run") != "0"
		target = "run"
		if dry {
			target = "dry run"
		}
		note, err = a.cleanup(r.Context(), slug, dry)
	case "start":
		err = a.sup.Start(slug)
	case "stop":
		err = a.sup.Stop(slug)
	case "drain":
		// Wait for the agents to finish, then stop. Returns at once; the
		// supervisor watches for the idle moment.
		err = a.sup.StopWhenIdle(slug)
	case "restart":
		err = a.sup.Restart(slug)
	case "stopall":
		a.stopAll(whenIdle)
		if whenIdle {
			slug = "every loop, each as it goes idle, then this process"
		} else {
			slug = "every loop now, then this process"
		}
		stopping = true
	case "apply":
		// Re-read the config and re-materialize everything, then start
		// whatever is ready and stopped. A running child whose settings
		// moved is handed back to its own run, which waits for its loops
		// to go quiet unless the box is unticked.
		if err = a.reload(whenIdle); err == nil {
			a.sup.StartAll()
		}
	default:
		err = fmt.Errorf("unknown action %q", op)
	}
	text := op + " " + slug
	result := "ok"
	if note != "" {
		text, result = note, note
	}
	if err != nil {
		text, result = err.Error(), err.Error()
	}
	project := slug
	if op == "stopall" || op == "apply" || op == "startall" || op == "drainall" {
		project = ""
	}
	a.audit(r, op, project, target, result)
	// A redirect here would point the browser at a port the process is
	// already leaving, so it gets this page instead: the last thing the
	// browser loads, and its next tick says the process is gone.
	if stopping {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := page.ExecuteTemplate(w, "page", a.opsFor(r, text, "")); e != nil {
			log.Printf("page: %v", e)
		}
		return
	}
	back := "/"
	if next := auth.SafeNext(r.FormValue("next")); strings.HasPrefix(next, "/projects/") {
		back = next
	}
	key := "msg"
	if err != nil {
		key = "err"
	}
	http.Redirect(w, r, back+"?"+key+"="+query(text), http.StatusSeeOther)
}

// drainAll asks every running project to stop each loop as it goes idle.
// Unlike stop all it leaves this process running.
func (a *app) drainAll() error {
	var errs []error
	for _, r := range a.repos() {
		if r.State == supervise.StateRunning {
			if err := a.sup.StopWhenIdle(r.Slug); err != nil {
				errs = append(errs, fmt.Errorf("drain %s: %w", r.Slug, err))
			}
		}
	}
	return errors.Join(errs...)
}

// operator returns the loop source when the project reports the capability
// can asks for, or an error saying that it does not.
func (a *app) operator(slug string, can func(loopsrc.Capabilities) bool, what string) (loopsrc.Operator, error) {
	op, ok := a.src().(loopsrc.Operator)
	if !ok || !can(op.ProjectCapabilities(slug)) {
		return nil, fmt.Errorf("%s %s: not supported by this project's sortie-loop", what, slug)
	}
	return op, nil
}

// wake wakes a sleeping project and describes what happened.
func (a *app) wake(ctx context.Context, slug string) (string, error) {
	op, err := a.operator(slug, func(c loopsrc.Capabilities) bool { return c.Wake }, "wake")
	if err != nil {
		return "", err
	}
	res, err := op.Wake(ctx, slug)
	switch {
	case err != nil:
		return "", fmt.Errorf("wake %s: %w", slug, err)
	case res.Woke && res.WasSleeping:
		return "woke " + slug, nil
	case res.Woke:
		return slug + " was already awake", nil
	}
	return "wake " + slug + ": the loop did not wake", nil
}

// cleanup runs a project's cleanup command, or lists what it would remove
// when dry is set, and describes the outcome.
func (a *app) cleanup(ctx context.Context, slug string, dry bool) (string, error) {
	op, err := a.operator(slug, func(c loopsrc.Capabilities) bool { return c.Cleanup }, "cleanup")
	if err != nil {
		return "", err
	}
	what := "cleanup"
	if dry {
		what = "cleanup (dry run)"
	}
	res, err := op.Cleanup(ctx, slug, dry)
	if err != nil {
		return "", fmt.Errorf("%s of %s: %w", what, slug, err)
	}
	msg := fmt.Sprintf("%s of %s: exit code %d in %d ms", what, slug, res.ExitCode, res.DurationMs)
	if out := strings.TrimSpace(res.OutputTail); out != "" {
		msg += ". Output: " + trunc(out, 300)
	}
	return msg, nil
}

// stopAll stops every loop and then this process, which is the only
// supervisor for them: a loop left running after central is gone keeps
// claiming issues with nobody watching.
//
// It returns at once either way. A drained fleet takes as long as its
// agents take, and an HTTP request is not the place to wait for that.
func (a *app) stopAll(whenIdle bool) {
	if whenIdle {
		a.sup.StopAllWhenIdle()
		go func() {
			a.sup.WaitAll()
			a.shutdown()
		}()
		return
	}
	go func() {
		a.sup.StopAll()
		a.shutdown()
	}()
}

// query escapes a message for a redirect target. html/template escapes
// it again on the way out, so the round trip is safe in both
// directions.
func query(s string) string { return url.QueryEscape(s) }
