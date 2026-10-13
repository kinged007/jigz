package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
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
	Retrying []struct {
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
		st.Running = append(st.Running, runRow{
			Identifier: id,
			State:      r.State,
			Turns:      r.TurnCount,
			Tokens:     toks,
			Model:      r.ModelName,
			StartedAt:  r.StartedAt.Format("15:04:05"),
		})
	}
	return st
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
	Controls bool
}

type repoRollup struct {
	Name    string
	Loops   int
	Running int
	Runs    int
	Tokens  int64
}

// collectStats fans out to every known endpoint: live state via HTTP,
// all-time figures straight from each SQLite DB.
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
// The server is bound to loopback, and the control route is no more open
// than that: it can only signal the process it is served by, and only a
// process on this host that can reach the port can do that. That is the
// same bar SIGTERM has always had, which is why the buttons are just
// signals to self.
func serveDashboard(port int, unite bool, sup *supervisor) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := dashTmpl.Execute(w, collectStats(unite, sup)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("POST /control", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sig, ok := map[string]syscall.Signal{
			"stop":     syscall.SIGTERM,
			"stopIdle": sigStopIdle,
			"restart":  sigRestartIdle,
		}[r.PostFormValue("op")]
		if !ok {
			http.Error(w, "unknown op", http.StatusBadRequest)
			return
		}
		// Signalled to self rather than handled here: one code path serves
		// the buttons, the command line and any other caller.
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusSeeOther)
	})
	go func() {
		_ = http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", port), mux)
	}()
	fmt.Printf("dashboard http://127.0.0.1:%d%s\n", port, map[bool]string{true: " (unite: all repos)", false: ""}[unite])
}
