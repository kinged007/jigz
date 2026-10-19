package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kinged007/sortie-loop/internal/host"
)

//go:embed dashboard.html
var dashboardHTML string

// statePayload mirrors the subset of sortie's GET /api/v1/state the
// unite dashboard consumes.
type statePayload struct {
	Counts struct {
		Running         int `json:"running"`
		Retrying        int `json:"retrying"`
		BudgetExhausted int `json:"budget_exhausted"`
	} `json:"counts"`
	ActiveEstimatedCostUSD *float64 `json:"active_estimated_cost_usd"`
	Retrying               []struct {
		IssueID string `json:"issue_id"`
	} `json:"retrying"`
	Running []struct {
		DisplayIdentifier string    `json:"display_identifier"`
		IssueIdentifier   string    `json:"issue_identifier"`
		State             string    `json:"state"`
		TurnCount         int       `json:"turn_count"`
		ModelName         string    `json:"model_name"`
		StartedAt         time.Time `json:"started_at"`
		Tokens            struct {
			TotalTokens *int64 `json:"total_tokens"`
		} `json:"tokens"`
	} `json:"running"`
	AgentTotals struct {
		InputTokens     int64 `json:"input_tokens"`
		OutputTokens    int64 `json:"output_tokens"`
		TotalTokens     int64 `json:"total_tokens"`
		CacheReadTokens int64 `json:"cache_read_tokens"`
	} `json:"agent_totals"`
}

var dashTmpl = template.Must(template.New("dash").Funcs(template.FuncMap{
	"fmtTokens": fmtTokens,
	"fmtDur":    fmtDuration,
	"commas":    commas,
	"usd":       usd,
	"since":     since,
	"at":        at,
	"pct":       pct,
	"cpu":       cpuPct,
	"size":      size,
	"gib":       gib,
	"agent":     agentLabel,
	"lower":     strings.ToLower,
	"even":      func(i int) bool { return i%2 == 0 },
}).Parse(dashboardHTML))

// fetchState pulls one loop's live state with a short timeout; a dead or
// restarting loop yields Live=false rather than failing the page.
func fetchState(ep loopEndpoint) loopStats {
	st := loopStats{Endpoint: ep}
	readLoopDB(&st)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/state", ep.Port))
	if err != nil {
		return st
	}
	defer resp.Body.Close()
	var p statePayload
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return st
	}
	st.Live = true
	st.Cost = p.ActiveEstimatedCostUSD
	st.Retrying = p.Counts.Retrying
	st.BudgetExhausted = p.Counts.BudgetExhausted
	for _, r := range p.Running {
		id := r.DisplayIdentifier
		if id == "" {
			id = r.IssueIdentifier
		}
		var toks int64
		if r.Tokens.TotalTokens != nil {
			toks = *r.Tokens.TotalTokens
		}
		ru := runRow{
			Identifier: id,
			State:      r.State,
			Turns:      r.TurnCount,
			Tokens:     toks,
			Model:      r.ModelName,
			StartedAt:  r.StartedAt.Format(time.RFC3339),
		}
		st.Running = append(st.Running, ru)
	}
	if st.Endpoint.DBPath != "" {
		st.Running = applyPickups(st.Running, st.Endpoint.DBPath)
	}
	return st
}

func applyPickups(rows []runRow, dbPath string) []runRow {
	db, err := openRO(dbPath)
	if err != nil {
		return rows
	}
	defer db.Close()
	if _, err := db.Exec("SELECT 1"); err != nil {
		return rows
	}
	if !tableExists(db, "run_history") {
		return rows
	}
	for i, r := range rows {
		if r.Identifier != "" {
			rows[i].PickUp = pickedUp(db, r.Identifier)
			if rows[i].PickUp == "" {
				rows[i].PickUp = r.StartedAt
			}
		}
	}
	return rows
}

type dashData struct {
	GeneratedAt time.Time
	Unite       bool
	Loops       []loopStats
	Issues      []issueStats
	Repos       []repoRollup
	AllTotal    int64
	AllInput    int64
	AllOutput   int64
	AllCache    int64
	AllRuns     int
	AllFailed   int
	RunningN    int
	// Draining marks the loops that are waiting to stop, and Controls
	// says the page can act: a unite page spans repos, and a button on it
	// would have no single run to signal.
	Draining map[string]bool
	Sleep    sleepStatus
	Controls bool
}

type repoRollup struct {
	Name    string
	Loops   int
	Running int
	Runs    int
	Tokens  int64
}

// Fleet is the fleet view for the united dashboard.
type Fleet struct {
	At      time.Time   `json:"at"`
	Loops   []FleetLoop `json:"loops"`
	Repos   []FleetRepo `json:"repos"`
	Live    int         `json:"live"`
	Running int         `json:"running"`
	Tokens  int64       `json:"tokens"`
	Runs    int         `json:"runs"`
	Failed  int         `json:"failed"`
}

type FleetLoop struct {
	Repo      string   `json:"repo"`
	RepoName  string   `json:"repo_name"`
	Loop      string   `json:"loop"`
	Port      int      `json:"port"`
	DBPath    string   `json:"db_path"`
	Live      bool     `json:"live"`
	Running   []runRow `json:"running,omitempty"`
	Retrying  int      `json:"retrying"`
	Runs      int      `json:"runs"`
	Succeeded int      `json:"succeeded"`
	Failed    int      `json:"failed"`
	Tokens    int64    `json:"tokens"`
	Input     int64    `json:"input"`
	Output    int64    `json:"output"`
	Cost      *float64 `json:"cost,omitempty"`
	DBError   string   `json:"db_error,omitempty"`
}

type FleetRepo struct {
	Repo    string `json:"repo"`
	Loops   int    `json:"loops"`
	Live    int    `json:"live"`
	Running int    `json:"running"`
	Runs    int    `json:"runs"`
	Failed  int    `json:"failed"`
	Tokens  int64  `json:"tokens"`
}

type activeRun struct {
	Repo       string
	Loop       string
	Port       int
	Identifier string
	State      string
	Turns      int
	Tokens     int64
	Agent      string
	Model      string
	PickedUp   string
	Cost       *float64
}

type repoView struct {
	Slug       string
	Dir        string
	Loops      []string
	State      string
	Running    int
	Tokens     int64
	Observed   int
	PID        int
	Args       []string
	Version    string
	StatusPath string
	Err        string
	Diags      struct {
		Errors   []string
		Warnings []string
	}
	Output    []string
	Disk      int64
	DiskKnown bool
	Sleep     sleepStatus
}

func (v repoView) IsRunning() bool { return v.State == "running" }

type pageData struct {
	Now          time.Time
	Fleet        Fleet
	Repos        []repoView
	ConfigPath   string
	Sys          host.Metrics
	Msg          string
	Err          string
	Edit         string
	Exposed      bool
	Active       []activeRun
	Confirm      bool
	WhenIdle     bool
	Host         string
	Link         linkBuilder
	LinkTemplate string
	Unite        bool
	GeneratedAt  time.Time
	Loops        []loopStats
	Issues       []issueStats
	AllTotal     int64
	AllInput     int64
	AllOutput    int64
	AllCache     int64
	AllRuns      int
	AllFailed    int
	RunningN     int
	Controls     bool
	Draining     map[string]bool
	Sleep        sleepStatus
}

type linkBuilder struct {
	Host     string
	Template string
}

func (l linkBuilder) Build(r *FleetLoop) string {
	if l.Template == "" {
		return "http://" + net.JoinHostPort(strings.Trim(l.Host, "[]"), strconv.Itoa(r.Port)) + "/"
	}
	rep := strings.NewReplacer("{host}", l.Host, "{port}", strconv.Itoa(r.Port), "{loop}", r.Loop, "{repo}", r.Repo)
	return rep.Replace(l.Template)
}

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

type hostGuard struct {
	addrs    map[string]bool
	wildcard bool
}

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

func (g hostGuard) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.allows(r.Host) {
			http.Error(w, "this page does not answer for this Host", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func newHostGuard(host string) hostGuard {
	switch host {
	case "0.0.0.0", "::", "":
		return hostGuard{wildcard: true, addrs: map[string]bool{}}
	}
	return hostGuard{addrs: map[string]bool{host: true, strings.Trim(host, "[]"): true}}
}

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

func query(s string) string { return url.QueryEscape(s) }

func active(f Fleet, agents map[string]string) []activeRun {
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
			run := activeRun{Repo: l.Repo, Loop: l.Loop, Port: l.Port, Identifier: r.Identifier, State: r.State, Turns: r.Turns, Tokens: r.Tokens, Agent: agents[l.Repo+"/"+l.Loop], Model: r.Model, PickedUp: pickedUp}
			if l.Cost != nil {
				share := *l.Cost
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
	sort.SliceStable(out, func(i, j int) bool { return out[i].PickedUp < out[j].PickedUp })
	return out
}

var diskCache struct {
	sync.Mutex
	m    map[string]int64
	last time.Time
}

func cachedDisk(root string) (int64, bool) {
	diskCache.Lock()
	defer diskCache.Unlock()
	if diskCache.m == nil {
		diskCache.m = map[string]int64{}
	}
	if time.Since(diskCache.last) > 10*time.Minute {
		diskCache.m = map[string]int64{}
		diskCache.last = time.Now()
	}
	if v, ok := diskCache.m[root]; ok {
		return v, true
	}
	n := host.DirSize(filepath.Join(root, ".sortie"))
	if n >= 0 {
		diskCache.m[root] = n
		return n, true
	}
	return -1, false
}

func collectFleet(unite bool, sup *supervisor) Fleet {
	var endpoints []loopEndpoint
	if unite {
		endpoints = readRegistry()
	} else {
		if sup != nil {
			endpoints = sup.endpoints()
		}
	}
	loops := make([]FleetLoop, len(endpoints))
	done := make(chan int, len(endpoints))
	for i, ep := range endpoints {
		go func(i int, ep loopEndpoint) {
			st := fetchState(ep)
			loops[i] = FleetLoop{Repo: st.Endpoint.RepoName, RepoName: st.Endpoint.RepoName, Loop: st.Endpoint.Loop, Port: st.Endpoint.Port, DBPath: st.Endpoint.DBPath, Live: st.Live, Running: st.Running, Retrying: st.Retrying, Runs: st.Runs, Succeeded: st.Succeeded, Failed: st.Failed, Tokens: st.AllTotal, Input: st.AllInput, Output: st.AllOutput, Cost: st.Cost, DBError: st.DBError}
			done <- i
		}(i, ep)
	}
	for range endpoints {
		<-done
	}
	sort.Slice(loops, func(i, j int) bool {
		if loops[i].Repo != loops[j].Repo {
			return loops[i].Repo < loops[j].Repo
		}
		return loops[i].Loop < loops[j].Loop
	})
	f := Fleet{At: time.Now().UTC(), Loops: loops}
	byRepo := map[string]*FleetRepo{}
	for _, l := range loops {
		f.Tokens += l.Tokens
		f.Runs += l.Runs
		f.Failed += l.Failed
		f.Running += len(l.Running)
		r, ok := byRepo[l.Repo]
		if !ok {
			r = &FleetRepo{Repo: l.Repo}
			byRepo[l.Repo] = r
		}
		r.Loops++
		r.Runs += l.Runs
		r.Failed += l.Failed
		r.Tokens += l.Tokens
		r.Running += len(l.Running)
		if l.Live {
			r.Live++
			f.Live++
		}
	}
	for _, r := range byRepo {
		f.Repos = append(f.Repos, *r)
	}
	sort.Slice(f.Repos, func(i, j int) bool { return f.Repos[i].Repo < f.Repos[j].Repo })
	return f
}

func buildRepos(fleet Fleet, unite bool, sup *supervisor) []repoView {
	// Build from registry entries when unite, else from single sup.
	if !unite {
		if sup == nil {
			return nil
		}
		doc, err := readStatusDoc(sup.root)
		if err != nil {
			doc = statusDoc{Repo: sup.root, RepoName: repoName(sup.root)}
		}
		var loops []string
		for _, w := range doc.Workflows {
			loops = append(loops, w.Name)
		}
		sort.Strings(loops)
		state := "running"
		if doc.State != "" {
			state = doc.State
		}
		if liveness := statusLiveness(doc); liveness == "stale" {
			state = "stale"
		} else if liveness == "exited" {
			state = "exited"
		}
		rv := repoView{Slug: doc.RepoName, Dir: sup.root, Loops: loops, State: state, PID: doc.PID, StatusPath: "", Sleep: doc.Sleep}
		if rv.Slug == "" {
			rv.Slug = repoName(sup.root)
		}
		for _, l := range fleet.Loops {
			if l.Repo == rv.Slug {
				rv.Running += len(l.Running)
				rv.Tokens += l.Tokens
				rv.Observed++
			}
		}
		if n, ok := cachedDisk(sup.root); ok {
			rv.Disk, rv.DiskKnown = n, true
		}
		return []repoView{rv}
	}
	path, err := registryPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil
	}
	var out []repoView
	for root, e := range reg.Entries {
		if time.Since(e.UpdatedAt) > 5*time.Minute && !pidAlive(e.PID) {
			continue
		}
		doc, _ := readStatusDoc(root)
		state := "running"
		if doc.State != "" {
			state = doc.State
		}
		if liveness := statusLiveness(doc); liveness == "stale" {
			state = "stale"
		} else if liveness == "exited" {
			state = "exited"
		} else if e.PID > 0 && !pidAlive(e.PID) {
			state = "stopped"
		}
		var loops []string
		for _, l := range e.Loops {
			loops = append(loops, l.Loop)
		}
		if len(loops) == 0 {
			for _, w := range doc.Workflows {
				loops = append(loops, w.Name)
			}
		}
		sort.Strings(loops)
		rv := repoView{Slug: e.RepoName, Dir: root, Loops: loops, State: state, PID: e.PID, Args: e.Args, Version: e.Version, StatusPath: e.StatusPath, Sleep: doc.Sleep}
		if rv.Slug == "" {
			rv.Slug = repoName(root)
		}
		for _, l := range fleet.Loops {
			if l.Repo == rv.Slug {
				rv.Running += len(l.Running)
				rv.Tokens += l.Tokens
				rv.Observed++
			}
		}
		if n, ok := cachedDisk(root); ok {
			rv.Disk, rv.DiskKnown = n, true
		}
		out = append(out, rv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

func pageForRequest(r *http.Request, msg, errMsg string, unite bool, sup *supervisor, hostR *host.Reader, linkTemplate string, exposed bool) pageData {
	fleet := collectFleet(unite, sup)
	repos := buildRepos(fleet, unite, sup)
	agents := map[string]string{}
	if sup != nil {
		if doc, err := readStatusDoc(sup.root); err == nil {
			for _, w := range doc.Workflows {
				agents[doc.RepoName+"/"+w.Name] = ""
			}
		}
	}
	if unite {
		if path, err := registryPath(); err == nil {
			if data, err := os.ReadFile(path); err == nil {
				var reg registry
				if err := json.Unmarshal(data, &reg); err == nil {
					for root, e := range reg.Entries {
						if doc, err := readStatusDoc(root); err == nil {
							for _, w := range doc.Workflows {
								agents[e.RepoName+"/"+w.Name] = ""
							}
						}
					}
				}
			}
		}
	}
	var sys host.Metrics
	if hostR != nil {
		sys = hostR.Read()
	} else {
		sys = host.Metrics{CPU: -1}
	}
	configPath := ""
	if sup != nil {
		configPath = filepath.Join(sup.root, ".sortie", "config.yaml")
	} else if len(repos) > 0 {
		configPath = filepath.Join(repos[0].Dir, ".sortie", "config.yaml")
	}
	ds := collectStats(unite, sup)
	return pageData{Now: time.Now(), Fleet: fleet, Repos: repos, ConfigPath: configPath, Sys: sys, Msg: msg, Err: errMsg, Exposed: exposed, Active: active(fleet, agents), Host: requestHost(r), Link: linkBuilder{Host: requestHost(r), Template: linkTemplate}, LinkTemplate: linkTemplate, Unite: unite, GeneratedAt: ds.GeneratedAt, Loops: ds.Loops, Issues: ds.Issues, AllTotal: ds.AllTotal, AllInput: ds.AllInput, AllOutput: ds.AllOutput, AllCache: ds.AllCache, AllRuns: ds.AllRuns, AllFailed: ds.AllFailed, RunningN: ds.RunningN, Controls: ds.Controls, Draining: ds.Draining, Sleep: ds.Sleep}
}

func resolveRepo(slug string, unite bool, sup *supervisor) (string, registryEntry, bool) {
	if !unite {
		if sup == nil {
			return "", registryEntry{}, false
		}
		return sup.root, registryEntry{PID: os.Getpid(), RepoName: repoName(sup.root)}, true
	}
	path, err := registryPath()
	if err != nil {
		return "", registryEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", registryEntry{}, false
	}
	var reg registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return "", registryEntry{}, false
	}
	for rt, e := range reg.Entries {
		if e.RepoName == slug {
			return rt, e, true
		}
	}
	return "", registryEntry{}, false
}

func collectStats(unite bool, sup *supervisor) dashData {
	var endpoints []loopEndpoint
	if unite {
		endpoints = readRegistry()
	} else {
		endpoints = sup.endpoints()
	}
	stats := make([]loopStats, len(endpoints))
	done := make(chan int, len(endpoints))
	for i, ep := range endpoints {
		go func(i int, ep loopEndpoint) {
			stats[i] = fetchState(ep)
			done <- i
		}(i, ep)
	}
	for range endpoints {
		<-done
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Endpoint.RepoName != stats[j].Endpoint.RepoName {
			return stats[i].Endpoint.RepoName < stats[j].Endpoint.RepoName
		}
		return stats[i].Endpoint.Loop < stats[j].Endpoint.Loop
	})
	d := dashData{
		GeneratedAt: time.Now().UTC(), Unite: unite, Loops: stats,
		Issues: readIssueStats(endpoints), Controls: !unite,
	}
	if !unite {
		d.Draining = sup.draining()
		d.Sleep = sup.sleepStatus()
	}
	repos := map[string]*repoRollup{}
	for _, s := range stats {
		d.AllTotal += s.AllTotal
		d.AllInput += s.AllInput
		d.AllOutput += s.AllOutput
		d.AllCache += s.AllCache
		d.AllRuns += s.Runs
		d.AllFailed += s.Failed
		d.RunningN += len(s.Running)
		r, ok := repos[s.Endpoint.RepoName]
		if !ok {
			r = &repoRollup{Name: s.Endpoint.RepoName}
			repos[s.Endpoint.RepoName] = r
		}
		r.Loops++
		r.Running += len(s.Running)
		r.Runs += s.Runs
		r.Tokens += s.AllTotal
	}
	for _, r := range repos {
		d.Repos = append(d.Repos, *r)
	}
	sort.Slice(d.Repos, func(i, j int) bool { return d.Repos[i].Name < d.Repos[j].Name })
	sort.Slice(d.Issues, func(i, j int) bool { return d.Issues[i].Tokens > d.Issues[j].Tokens })
	return d
}

// serveDashboard runs this repo's dashboard HTTP server in the background.
//
// The server is bound to loopback by default, and the control route is
// only reachable via loopback unless --host is set. That matches the
// SIGTERM bar: only a process on this host that can reach the port can
// act.
func serveDashboard(port int, unite bool, sup *supervisor, hostFlag, loopURLTemplate string) {
	var hostR *host.Reader
	if p, err := registryPath(); err == nil {
		hostR = host.New(filepath.Dir(p))
	} else {
		hostR = host.New("/")
	}
	var g hostGuard
	var bindAddr string
	if hostFlag == "" {
		bindAddr = "127.0.0.1"
		g = hostGuard{addrs: map[string]bool{}}
	} else {
		g = newHostGuard(hostFlag)
		bindAddr = g.bindAddr()
		if bindAddr == "" && !g.wildcard {
			bindAddr = hostFlag
		}
	}
	exposed := g.wildcard || (hostFlag != "" && !isLoopbackHost(hostFlag))
	if exposed {
		fmt.Fprintf(os.Stderr, "sortie-loop: dashboard exposed beyond loopback on %s (no login)\n", bindAddr)
	}
	pageFor := func(r *http.Request, msg, errMsg string) pageData {
		return pageForRequest(r, msg, errMsg, unite, sup, hostR, loopURLTemplate, exposed)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", g.guard(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := pageFor(r, r.URL.Query().Get("msg"), r.URL.Query().Get("err"))
		if dashTmpl.Lookup("page") != nil {
			if err := dashTmpl.ExecuteTemplate(w, "page", data); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		if err := dashTmpl.Execute(w, collectStats(unite, sup)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	mux.HandleFunc("GET /live", g.guard(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := pageFor(r, "", "")
		if err := dashTmpl.ExecuteTemplate(w, "live", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	mux.HandleFunc("GET /status", g.guard(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := pageFor(r, "", "")
		if err := dashTmpl.ExecuteTemplate(w, "status", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	mux.HandleFunc("GET /edit", g.guard(func(w http.ResponseWriter, r *http.Request) {
		slug := r.URL.Query().Get("repo")
		var root, cfgPath string
		if unite && slug != "" {
			if rt, _, ok := resolveRepo(slug, unite, sup); ok {
				root = rt
			}
		} else if sup != nil {
			root = sup.root
		} else if slug != "" {
			if rt, _, ok := resolveRepo(slug, true, sup); ok {
				root = rt
			}
		}
		if root != "" {
			cfgPath = filepath.Join(root, ".sortie", "config.yaml")
		} else if sup != nil {
			cfgPath = filepath.Join(sup.root, ".sortie", "config.yaml")
		} else {
			cfgPath = "unknown"
		}
		body, rerr := os.ReadFile(cfgPath)
		data := pageFor(r, r.URL.Query().Get("msg"), r.URL.Query().Get("err"))
		data.Edit = string(body)
		data.ConfigPath = cfgPath
		if rerr != nil && data.Err == "" {
			data.Err = rerr.Error()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := dashTmpl.ExecuteTemplate(w, "edit", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	mux.HandleFunc("POST /edit", g.guard(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/edit?err="+query("bad form"), http.StatusSeeOther)
			return
		}
		slug := r.FormValue("repo")
		if slug == "" {
			slug = r.URL.Query().Get("repo")
		}
		var root string
		if unite && slug != "" {
			if rt, _, ok := resolveRepo(slug, true, sup); ok {
				root = rt
			}
		} else if sup != nil {
			root = sup.root
		}
		if root == "" {
			http.Redirect(w, r, "/edit?err="+query("unknown repo"), http.StatusSeeOther)
			return
		}
		cfgPath := filepath.Join(root, ".sortie", "config.yaml")
		body := r.FormValue("config")
		if strings.TrimSpace(body) == "" {
			body = r.FormValue("body")
		}
		if err := writeFileAtomic(cfgPath, []byte(body)); err != nil {
			http.Redirect(w, r, "/edit?repo="+query(slug)+"&err="+query(err.Error()), http.StatusSeeOther)
			return
		}
		if _, err := runningEntry(root); err == nil {
			_, _ = reloadSupervisor(root)
		}
		http.Redirect(w, r, "/edit?repo="+query(slug)+"&msg="+query("saved; running repos keep current settings until restarted"), http.StatusSeeOther)
	}))
	mux.HandleFunc("POST /action", g.guard(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/?err="+query("bad form"), http.StatusSeeOther)
			return
		}
		slug := r.FormValue("repo")
		op := r.FormValue("op")
		whenIdle := r.FormValue("when_idle") != ""
		workflow := r.FormValue("workflow")
		if workflow == "" {
			workflow = r.FormValue("loop")
		}
		if op == "stopall" && r.FormValue("confirm") == "" {
			data := pageFor(r, "", "")
			data.Confirm = true
			data.WhenIdle = whenIdle
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := dashTmpl.ExecuteTemplate(w, "confirm", data); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		var err error
		stopping := false
		msg := ""
		switch op {
		case "start":
			err = fmt.Errorf("start not implemented via dashboard")
		case "stop":
			if !unite {
				err = syscall.Kill(os.Getpid(), syscall.SIGTERM)
				msg = "stop"
			} else {
				if _, ent, ok := resolveRepo(slug, true, sup); ok {
					err = syscall.Kill(ent.PID, syscall.SIGTERM)
					msg = "stop " + slug
				} else {
					err = fmt.Errorf("unknown repo %q", slug)
				}
			}
		case "drain":
			if !unite {
				err = syscall.Kill(os.Getpid(), sigStopIdle)
				msg = "drain"
			} else {
				if _, ent, ok := resolveRepo(slug, true, sup); ok {
					err = syscall.Kill(ent.PID, sigStopIdle)
					msg = "drain " + slug
				} else {
					err = fmt.Errorf("unknown repo %q", slug)
				}
			}
		case "restart":
			if !unite {
				err = syscall.Kill(os.Getpid(), sigRestartIdle)
				msg = "restart"
			} else {
				if _, ent, ok := resolveRepo(slug, true, sup); ok {
					err = syscall.Kill(ent.PID, sigRestartIdle)
					msg = "restart " + slug
				} else {
					err = fmt.Errorf("unknown repo %q", slug)
				}
			}
		case "wake":
			var rt string
			if unite {
				if r2, _, ok := resolveRepo(slug, true, sup); ok {
					rt = r2
				}
			} else if sup != nil {
				rt = sup.root
			}
			if rt == "" {
				err = fmt.Errorf("unknown repo %q", slug)
			} else {
				err = requestWake(rt)
				msg = "wake " + slug
			}
		case "pause", "resume", "override":
			var rt string
			if unite {
				if r2, _, ok := resolveRepo(slug, true, sup); ok {
					rt = r2
				}
			} else if sup != nil {
				rt = sup.root
			}
			if rt == "" {
				err = fmt.Errorf("unknown repo %q", slug)
			} else if workflow == "" {
				err = fmt.Errorf("workflow required")
			} else {
				err = requestBudgetAction(rt, op, workflow)
				msg = op + " " + workflow + " on " + slug
			}
		case "stopall":
			stopping = true
			if unite {
				path, _ := registryPath()
				if data, ferr := os.ReadFile(path); ferr == nil {
					var reg registry
					if jerr := json.Unmarshal(data, &reg); jerr == nil {
						for _, e := range reg.Entries {
							if time.Since(e.UpdatedAt) > 5*time.Minute && !pidAlive(e.PID) {
								continue
							}
							sig := syscall.SIGTERM
							if whenIdle {
								sig = sigStopIdle
							}
							_ = syscall.Kill(e.PID, sig)
						}
					}
				}
				if whenIdle {
					msg = "every loop, each as it goes idle, then this process"
				} else {
					msg = "every loop now, then this process"
				}
			} else {
				sig := syscall.SIGTERM
				if whenIdle {
					sig = sigStopIdle
				}
				_ = syscall.Kill(os.Getpid(), sig)
				if whenIdle {
					msg = "every loop, each as it goes idle, then this process"
				} else {
					msg = "every loop now, then this process"
				}
			}
			if whenIdle && !unite {
				// let supervisor handle draining
			}
		case "apply":
			if unite {
				path, _ := registryPath()
				if data, ferr := os.ReadFile(path); ferr == nil {
					var reg registry
					if jerr := json.Unmarshal(data, &reg); jerr == nil {
						for rt := range reg.Entries {
							_, _ = reloadSupervisor(rt)
						}
					}
				}
				msg = "reloaded all repos"
			} else if sup != nil {
				_, err = reloadSupervisor(sup.root)
				if err == nil {
					msg = "reloaded"
				}
			}
		default:
			err = fmt.Errorf("unknown action %q", op)
		}
		text := msg
		if text == "" {
			text = op + " " + slug
		}
		if err != nil {
			text = err.Error()
		}
		if stopping {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			data := pageFor(r, text, "")
			if e := dashTmpl.ExecuteTemplate(w, "page", data); e != nil {
				http.Error(w, e.Error(), http.StatusInternalServerError)
			}
			return
		}
		if err != nil {
			http.Redirect(w, r, "/?err="+query(text), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+query(text), http.StatusSeeOther)
	}))
	mux.HandleFunc("POST /control", g.guard(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sig, ok := map[string]syscall.Signal{"stop": syscall.SIGTERM, "stopIdle": sigStopIdle, "restart": sigRestartIdle}[r.PostFormValue("op")]
		if !ok {
			http.Error(w, "unknown op", http.StatusBadRequest)
			return
		}
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusSeeOther)
	}))
	go func() {
		addr := net.JoinHostPort(bindAddr, strconv.Itoa(port))
		_ = http.ListenAndServe(addr, mux)
	}()
	fmt.Printf("dashboard http://%s:%d%s\n", bindAddr, port, map[bool]string{true: " (unite: all repos)", false: ""}[unite])
}
