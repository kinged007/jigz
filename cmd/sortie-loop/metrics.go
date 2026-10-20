package main

import (
	"html/template"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/rollup"
	"github.com/kinged007/sortie-loop/internal/store"
	"github.com/kinged007/sortie-loop/internal/supervise"
)

// tsLayout is the UTC timestamp form stored in runs_cache.
const tsLayout = "2006-01-02T15:04:05Z"

// timezone labels the day boundaries. The config has no time zone setting,
// so every day is a UTC day.
const timezone = "UTC"

// maxChartDays caps the days one chart draws.
const maxChartDays = 366

const topItemLimit = 10

type period struct {
	Key  string
	Days int
}

var metricPeriods = []period{{"24h", 1}, {"7d", 7}, {"14d", 14}, {"30d", 30}}

// parsePeriod returns the named period, or 14d for an unknown name.
func parsePeriod(s string) period {
	for _, p := range metricPeriods {
		if p.Key == s {
			return p
		}
	}
	return metricPeriods[2]
}

// daysBetween lists the UTC dates from the day of from to the day of to.
func daysBetween(from, to time.Time) []string {
	var out []string
	for d := from.UTC().Truncate(24 * time.Hour); !d.After(to.UTC()) && len(out) < maxChartDays; d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format(dateLayout))
	}
	return out
}

// perDay lays grouped days out along days, with zeros for days without runs.
func perDay(days []string, groups []store.Group) []store.Stats {
	by := map[string]store.Stats{}
	for _, g := range groups {
		by[g.Key] = g.Stats
	}
	out := make([]store.Stats, len(days))
	for i, d := range days {
		out[i] = by[d]
	}
	return out
}

// projectSeries is one project's tokens along the days of a chart.
type projectSeries struct {
	Project string  `json:"project"`
	Tokens  []int64 `json:"tokens"`
}

func projectSeriesFor(days []string, rows []store.DayTokens) []projectSeries {
	idx := map[string]int{}
	var out []projectSeries
	pos := map[string]int{}
	for i, d := range days {
		pos[d] = i
	}
	for _, r := range rows {
		i, ok := idx[r.Project]
		if !ok {
			i = len(out)
			idx[r.Project] = i
			out = append(out, projectSeries{Project: r.Project, Tokens: make([]int64, len(days))})
		}
		if p, ok := pos[r.Day]; ok {
			out[i].Tokens[p] += r.Total
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}

// metricsData is the Operations metrics section and GET /api/v1/metrics.
type metricsData struct {
	Period   string          `json:"period"`
	Timezone string          `json:"timezone"`
	Since    string          `json:"since"`
	Until    string          `json:"until"`
	Days     []string        `json:"days"`
	Totals   store.Stats     `json:"totals"`
	Daily    []store.Stats   `json:"daily"`
	Projects []projectSeries `json:"projects"`
	// Issues and PRs count the issues and pull requests behind the
	// averages, which cover measured runs only. The averages are null
	// when there are none.
	Issues         int    `json:"issues_measured"`
	PRs            int    `json:"prs_measured"`
	AvgTokensIssue *int64 `json:"avg_tokens_per_issue"`
	AvgTokensPR    *int64 `json:"avg_tokens_per_pr"`
	ActiveRuns     int    `json:"active_runs"`
	// GitHub is set when enrichment is on; only then are the PR series
	// present.
	GitHub    bool      `json:"github_enrichment"`
	PRsOpened *prSeries `json:"prs_opened,omitempty"`
	PRsMerged *prSeries `json:"prs_merged,omitempty"`
}

// prSeries counts cached pull requests along the days of a chart.
type prSeries struct {
	Total int   `json:"total"`
	Daily []int `json:"daily"`
}

// prSeriesFor lays per-day counts out along days, with zeros for the rest.
func prSeriesFor(days []string, byDay map[string]int) *prSeries {
	out := &prSeries{Total: *sum(byDay), Daily: make([]int, len(days))}
	for i, d := range days {
		out.Daily[i] = byDay[d]
	}
	return out
}

// prSeries reads the cached pull requests opened and merged in a range,
// or nil when enrichment is off.
func (a *app) prSeries(project, since, until string, days []string) (opened, merged *prSeries, err error) {
	if !a.githubOn() {
		return nil, nil, nil
	}
	o, m, err := a.store.PRDays(project, since, until)
	if err != nil {
		return nil, nil, err
	}
	return prSeriesFor(days, o), prSeriesFor(days, m), nil
}

func average(total int64, n int) *int64 {
	if n == 0 {
		return nil
	}
	v := total / int64(n)
	return &v
}

func (a *app) metrics(now time.Time, per period) (metricsData, error) {
	until := now.UTC()
	since := until.Add(-time.Duration(per.Days) * 24 * time.Hour)
	f := store.UsageFilter{Since: since.Format(tsLayout)}
	m := metricsData{Period: per.Key, Timezone: timezone, Since: f.Since, Until: until.Format(tsLayout), Days: daysBetween(since, until)}
	byDay, err := a.store.Usage(f, "day")
	if err != nil {
		return m, err
	}
	m.Daily = perDay(m.Days, byDay)
	all, err := a.store.Usage(f, "all")
	if err != nil {
		return m, err
	}
	m.Totals = all[0].Stats
	rows, err := a.store.DailyTokens(f, "project")
	if err != nil {
		return m, err
	}
	m.Projects = projectSeriesFor(m.Days, rows)
	items, err := a.store.ItemTokens(f)
	if err != nil {
		return m, err
	}
	m.Issues, m.PRs = items.Issues, items.PRs
	m.AvgTokensIssue, m.AvgTokensPR = average(items.IssueTokens, items.Issues), average(items.PRTokens, items.PRs)
	m.GitHub = a.githubOn()
	m.PRsOpened, m.PRsMerged, err = a.prSeries("", f.Since, "", m.Days)
	return m, err
}

func (m metricsData) series(pick func(store.Stats) int64) []int64 {
	out := make([]int64, len(m.Daily))
	for i, d := range m.Daily {
		out[i] = pick(d)
	}
	return out
}

// Empty is true when no run started in the period.
func (m metricsData) Empty() bool { return m.Totals.Runs == 0 }

// Window is the period's first and last UTC day.
func (m metricsData) Window() string {
	if len(m.Days) == 0 {
		return ""
	}
	return m.Days[0] + " to " + m.Days[len(m.Days)-1]
}

func (m metricsData) TokensChart() template.HTML {
	return lineChart(m.Days, []chartSeries{{"All projects", "var(--acc)", m.series(func(s store.Stats) int64 { return s.Total })}},
		true, false, "Tokens per day, all projects")
}

func (m metricsData) ProjectsChart() template.HTML {
	return lineChart(m.Days, projectChartSeries(shortProjects(m.Projects)), false, true, "Tokens per day, one line per project")
}

// shortProjects names the series after the repository alone, unless two
// projects share a repository name.
func shortProjects(ps []projectSeries) []projectSeries {
	seen := map[string]int{}
	for _, p := range ps {
		_, name, _ := strings.Cut(p.Project, "/")
		seen[name]++
	}
	out := slices.Clone(ps)
	for i, p := range out {
		if _, name, ok := strings.Cut(p.Project, "/"); ok && seen[name] == 1 {
			out[i].Project = name
		}
	}
	return out
}

func projectChartSeries(ps []projectSeries) []chartSeries {
	out := make([]chartSeries, len(ps))
	for i, p := range ps {
		out[i] = chartSeries{p.Project, seriesColor(i), p.Tokens}
	}
	return out
}

func (m metricsData) RunsChart() template.HTML {
	stacks := make([][]int64, len(m.Daily))
	labels := make([]string, len(m.Daily))
	for i, d := range m.Daily {
		stacks[i] = []int64{int64(d.Succeeded), int64(d.Failed), int64(d.Runs - d.Succeeded - d.Failed)}
		labels[i] = shortDay(m.Days[i])
	}
	return stackedBars(labels, []barSegment{{"succeeded", "var(--ok)"}, {"failed", "var(--err)"}, {"other (cancelled)", "var(--idle)"}},
		stacks, "Runs per day by outcome")
}

// Tile is one count tile with its sparkline.
type metricTile struct {
	Label, Tip string
	Value      string
	Spark      template.HTML
}

const (
	tipTokens = "Source: run_history input_tokens, output_tokens, cache_read_tokens, total_tokens. Runs with tokens_measured = 0 are left out."
	tipRuns   = "Source: run_history rows by started_at and status (failed counts status = 'failed')."
	tipIssues = "Source: distinct issue_id in run_history of workflows whose tracker.kind is not github-pr (workflow_kind, read from the workflow file)."
	tipPRs    = "Source: distinct issue_id in run_history of workflows whose tracker.kind is github-pr (workflow_kind, read from the workflow file)."
	tipOpened = "Source: GitHub pull requests central has cached, by created_at (UTC day). Only pull requests found through central's runs are counted."
	tipMerged = "Source: GitHub pull requests central has cached, by merged_at (UTC day). Only pull requests found through central's runs are counted."
	tipCost   = "Needs a cost field in run_history, which the engine does not record yet. Planned."
)

func (m metricsData) Tiles() []metricTile {
	cnt := func(pick func(store.Stats) int) []int64 {
		return m.series(func(s store.Stats) int64 { return int64(pick(s)) })
	}
	prs := []metricTile{{"PRs worked on", tipPRs, commas(m.Totals.PRs), sparkline(cnt(func(s store.Stats) int { return s.PRs }), "var(--acc)", "PRs worked on per day")}}
	if m.GitHub && m.PRsOpened != nil && m.PRsMerged != nil {
		prs = []metricTile{
			{"PRs opened", tipOpened, commas(m.PRsOpened.Total), sparkline(ints(m.PRsOpened.Daily), "var(--acc)", "PRs opened per day")},
			{"PRs merged", tipMerged, commas(m.PRsMerged.Total), sparkline(ints(m.PRsMerged.Daily), "var(--ok)", "PRs merged per day")},
		}
	}
	tiles := []metricTile{
		{"runs started", tipRuns, commas(m.Totals.Runs), sparkline(cnt(func(s store.Stats) int { return s.Runs }), "var(--acc)", "Runs started per day")},
		{"runs failed", tipRuns, commas(m.Totals.Failed), sparkline(cnt(func(s store.Stats) int { return s.Failed }), "var(--err)", "Runs failed per day")},
		{"issues worked on", tipIssues, commas(m.Totals.Issues), sparkline(cnt(func(s store.Stats) int { return s.Issues }), "var(--acc)", "Issues worked on per day")},
	}
	tiles = append(tiles, prs...)
	return append(tiles, metricTile{"tokens used", tipTokens, commas(m.Totals.Total), sparkline(m.series(func(s store.Stats) int64 { return s.Total }), "var(--acc)", "Tokens per day")})
}

func ints(v []int) []int64 {
	out := make([]int64, len(v))
	for i, n := range v {
		out[i] = int64(n)
	}
	return out
}

func (m metricsData) AvgIssue() string { return avgText(m.AvgTokensIssue) }
func (m metricsData) AvgPR() string    { return avgText(m.AvgTokensPR) }

func avgText(v *int64) string {
	if v == nil {
		return "—"
	}
	return commas(*v)
}

func (m metricsData) TipTokens() string { return tipTokens }
func (m metricsData) TipIssues() string { return tipIssues }
func (m metricsData) TipPRs() string    { return tipPRs }
func (m metricsData) TipCost() string   { return tipCost }

// opsView carries what only the Operations page needs.
type opsView struct {
	Period    string
	Periods   []period
	Sort      string
	Metrics   *metricsData
	MetricErr string
	Active    int
	Retrying  int
	Projects  []opsProject
	Alerts    []alert
	// Audit is the newest audit rows, empty when the viewer may not see
	// them or the history is unavailable.
	Audit []store.AuditEntry
	// Week is the measured tokens of runs started in the last 7 days.
	Week int64
	// Tokens by "project" and "project workflow", for the selected period
	// and since UTC midnight.
	period, today map[string]int64
}

// loopFigures are the token columns of one loop row.
type loopFigures struct {
	Period, Today int64
	Share         int
}

// Figures returns the token columns of a loop, with its share of its
// project's tokens in the period.
func (d pageData) Figures(l rollup.Loop) loopFigures {
	key := l.Repo + " " + l.Loop
	return loopFigures{d.Ops.period[key], d.Ops.today[key], share(d.Ops.period[key], d.Ops.period[l.Repo])}
}

// auditRows is how many audit entries Operations shows.
const auditRows = 8

var sortKeys = []string{"name", "tokens", "state"}

func parseSort(s string) string {
	for _, k := range sortKeys {
		if s == k {
			return k
		}
	}
	return "name"
}

func share(part, whole int64) int {
	if whole <= 0 {
		return 0
	}
	return int(part * 100 / whole)
}

// stateRank orders project states by how much attention they need.
var stateRank = map[string]int{
	supervise.StateFailed: 0, supervise.StateInvalid: 1, supervise.StateRunning: 2,
	supervise.StateStopped: 3, supervise.StateDisabled: 4,
}

// activeRuns is the agents running or waiting to retry across the fleet.
func activeRuns(f rollup.Fleet) int {
	n := f.Running
	for _, l := range f.Loops {
		n += l.Retrying
	}
	return n
}

// opsFor is dataFor plus the metrics, token columns and sorting of the
// Operations page. The period and sort come from the query string, so the
// live fragment keeps them.
func (a *app) opsFor(r *http.Request, msg, errMsg string) pageData {
	d := a.dataFor(r, msg, errMsg)
	q := r.URL.Query()
	per, sortBy := parsePeriod(q.Get("period")), parseSort(q.Get("sort"))
	now := time.Now()
	v := opsView{Period: per.Key, Periods: metricPeriods, Sort: sortBy, Active: activeRuns(d.Fleet)}
	d.Sorts = sortKeys
	if a.store != nil {
		m, err := a.metrics(now, per)
		if err != nil {
			log.Printf("metrics: %v", err)
			v.MetricErr = "could not read run history"
		} else {
			m.ActiveRuns = v.Active
			v.Metrics = &m
			v.period = a.tokensBy(m.Since)
			v.today = a.tokensBy(now.UTC().Truncate(24 * time.Hour).Format(tsLayout))
		}
	}
	for i := range d.Repos {
		p := &d.Repos[i]
		p.Period, p.Today, p.Share = v.period[p.Slug], v.today[p.Slug], share(v.period[p.Slug], v.period[""])
	}
	d.Fleet.Loops = slices.Clone(d.Fleet.Loops)
	sortOps(d.Repos, d.Fleet.Loops, v.period, sortBy)
	v.Retrying = v.Active - d.Fleet.Running
	d.Ops = v
	d.Ops.Projects = a.opsProjects(d)
	d.Ops.Alerts = a.opsAlerts(d, d.Ops.Projects)
	if a.store != nil {
		d.Ops.Week = a.tokensBy(now.UTC().AddDate(0, 0, -7).Format(tsLayout))[""]
		if d.Auth.Authenticated {
			if rows, err := a.store.Audit(auditRows); err == nil {
				d.Ops.Audit = rows
			}
		}
	}
	return d
}

// tokensBy returns the measured tokens of runs started since a timestamp,
// keyed by project, by "project workflow", and "" for all of them.
func (a *app) tokensBy(since string) map[string]int64 {
	out := map[string]int64{}
	f := store.UsageFilter{Since: since}
	for _, by := range []string{"project", "workflow"} {
		gs, err := a.store.Usage(f, by)
		if err != nil {
			log.Printf("tokens by %s: %v", by, err)
			continue
		}
		for _, g := range gs {
			out[g.Key] = g.Total
			if by == "project" {
				out[""] += g.Total
			}
		}
	}
	return out
}

func sortOps(repos []repoView, loops []rollup.Loop, period map[string]int64, by string) {
	sort.SliceStable(repos, func(i, j int) bool {
		x, y := repos[i], repos[j]
		switch by {
		case "tokens":
			if x.Period != y.Period {
				return x.Period > y.Period
			}
		case "state":
			if stateRank[x.State] != stateRank[y.State] {
				return stateRank[x.State] < stateRank[y.State]
			}
		}
		return x.Slug < y.Slug
	})
	sort.SliceStable(loops, func(i, j int) bool {
		x, y := loops[i], loops[j]
		switch by {
		case "tokens":
			if tx, ty := period[x.Repo+" "+x.Loop], period[y.Repo+" "+y.Loop]; tx != ty {
				return tx > ty
			}
		case "state":
			if x.Live != y.Live {
				return !x.Live
			}
		}
		if x.Repo != y.Repo {
			return x.Repo < y.Repo
		}
		return x.Loop < y.Loop
	})
}

// apiMetrics serves the Operations metrics.
func (a *app) apiMetrics(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		apiError(w, http.StatusServiceUnavailable, "run history is unavailable")
		return
	}
	per := parsePeriod(r.URL.Query().Get("period"))
	if v := r.URL.Query().Get("period"); v != "" && v != per.Key {
		apiError(w, http.StatusBadRequest, "period must be 24h, 7d, 14d or 30d")
		return
	}
	m, err := a.metrics(time.Now(), per)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	f := a.src().Fleet()
	m.ActiveRuns = activeRuns(f)
	writeJSON(w, http.StatusOK, m)
}
