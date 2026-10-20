package main

import (
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/rollup"
	"github.com/kinged007/sortie-loop/internal/store"
)

var usageGroups = []string{"project", "workflow", "model", "day"}

var usagePeriods = []string{"day", "week", "month", "custom"}

func validGroup(g string) bool {
	for _, k := range usageGroups {
		if g == k {
			return true
		}
	}
	return false
}

// usageRange resolves the page's period to whole UTC days, first and last
// inclusive. The Runs page takes the same two dates, so a drill-down link
// covers exactly the runs counted here.
func usageRange(v url.Values, now time.Time) (per, from, to string) {
	today := now.UTC().Truncate(24 * time.Hour)
	per = v.Get("period")
	switch per {
	case "day":
		return per, today.Format(dateLayout), today.Format(dateLayout)
	case "month":
		return per, today.AddDate(0, 0, -29).Format(dateLayout), today.Format(dateLayout)
	case "custom":
		f, errF := time.Parse(dateLayout, v.Get("from"))
		t, errT := time.Parse(dateLayout, v.Get("to"))
		if errT != nil {
			t = today
		}
		if errF != nil {
			f = t.AddDate(0, 0, -13)
		}
		if f.After(t) {
			f = t
		}
		if t.Sub(f) > (maxChartDays-1)*24*time.Hour {
			f = t.AddDate(0, 0, -(maxChartDays - 1))
		}
		return per, f.Format(dateLayout), t.Format(dateLayout)
	}
	return "week", today.AddDate(0, 0, -6).Format(dateLayout), today.Format(dateLayout)
}

// dayBounds turns two inclusive dates into the timestamp range of
// UsageFilter.
func dayBounds(from, to string) (since, until string) {
	f, _ := time.Parse(dateLayout, from)
	t, _ := time.Parse(dateLayout, to)
	return f.Format(tsLayout), t.AddDate(0, 0, 1).Format(tsLayout)
}

// usageRow is one table row of the Usage page.
type usageRow struct {
	store.Group
	Label string
	Href  string
}

type topRow struct {
	store.TopItem
	Href string
}

type usageData struct {
	pageData
	Group, Period, From, To string
	Periods, Groups         []string
	Totals                  store.Stats
	Rows                    []usageRow
	Top                     []topRow
	Unavailable, Empty      bool
	Holds                   []alert
	Days                    []string
	Projects                []projectSeries
	// PRsOpened and PRsMerged are set when GitHub enrichment is on.
	PRsOpened, PRsMerged *prSeries
}

// tokenSplit is the input, output and cache-read tokens of a group for a
// stacked bar. Input includes cache reads, so the bar shows input without
// them.
func (u usageData) tokenSplit(s store.Stats) []int64 {
	return []int64{max(s.Input-s.CacheRead, 0), s.Output, s.CacheRead}
}

func (u usageData) GroupChart() template.HTML {
	labels := make([]string, len(u.Rows))
	stacks := make([][]int64, len(u.Rows))
	for i, r := range u.Rows {
		labels[i] = r.Label
		stacks[i] = u.tokenSplit(r.Stats)
	}
	segs := []barSegment{{"input (not cached)", "var(--info)"}, {"output", "var(--ok)"}, {"cache read", "var(--idle)"}}
	if u.Group == "day" {
		return stackedBars(labels, segs, stacks, "Tokens by day")
	}
	return horizontalBars(labels, segs, stacks, "Tokens by "+u.Group)
}

func (u usageData) ProjectsChart() template.HTML {
	return lineChart(u.Days, projectChartSeries(u.Projects), false, true, "Tokens per day, one line per project")
}

// runsHref is the Runs page filtered the way a group was selected.
func runsHref(group string, g store.Group, from, to string) string {
	v := url.Values{}
	switch group {
	case "project":
		v.Set("project", g.Project)
	case "workflow":
		v.Set("project", g.Project)
		v.Set("workflow", g.Workflow)
	case "model":
		if g.Key != "" {
			v.Set("model", g.Key)
		}
	case "day":
		from, to = g.Key, g.Key
	}
	v.Set("from", from)
	v.Set("to", to)
	return "/runs?" + v.Encode()
}

func (a *app) usage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := usageData{pageData: a.dataFor(r, "", ""), Periods: usagePeriods, Groups: usageGroups, Unavailable: a.store == nil}
	d.Nav, d.Title = "usage", "Usage"
	d.Group = q.Get("group")
	if !validGroup(d.Group) {
		d.Group = "project"
	}
	d.Period, d.From, d.To = usageRange(q, time.Now())
	if a.store != nil {
		since, until := dayBounds(d.From, d.To)
		f := store.UsageFilter{Since: since, Until: until}
		if err := a.fillUsage(&d, f); err != nil {
			a.fail(w, "usage", err)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "usage", d); err != nil {
		log.Printf("usage: %v", err)
	}
}

func (a *app) fillUsage(d *usageData, f store.UsageFilter) error {
	groups, err := a.store.Usage(f, d.Group)
	if err != nil {
		return err
	}
	all, err := a.store.Usage(f, "all")
	if err != nil {
		return err
	}
	d.Holds = budgetHolds(d.Fleet.Loops)
	d.Totals = all[0].Stats
	d.Empty = d.Totals.Runs == 0
	for _, g := range groups {
		label := g.Key
		switch {
		case g.Key == "" && d.Group == "model":
			label = "unknown model"
		case d.Group == "workflow":
			label = g.Project + " " + g.Workflow
		}
		d.Rows = append(d.Rows, usageRow{Group: g, Label: label, Href: runsHref(d.Group, g, d.From, d.To)})
	}
	top, err := a.store.TopItems(f, topItemLimit)
	if err != nil {
		return err
	}
	for _, t := range top {
		v := url.Values{"project": {t.Project}, "q": {t.DisplayIdentifier}, "from": {d.From}, "to": {d.To}}
		d.Top = append(d.Top, topRow{t, "/runs?" + v.Encode()})
	}
	first, _ := time.Parse(dateLayout, d.From)
	last, _ := time.Parse(dateLayout, d.To)
	d.Days = daysBetween(first, last)
	rows, err := a.store.DailyTokens(f, "project")
	if err != nil {
		return err
	}
	d.Projects = projectSeriesFor(d.Days, rows)
	d.PRsOpened, d.PRsMerged, err = a.prSeries("", f.Since, f.Until, d.Days)
	return err
}

// budgetHolds are the budget hold notices readable from the loops'
// databases.
func budgetHolds(loops []rollup.Loop) []alert {
	var out []alert
	for _, l := range loops {
		for _, al := range loopDBAlerts(l) {
			if al.Kind == "Budget hold" {
				out = append(out, al)
			}
		}
	}
	return out
}

type apiUsageBody struct {
	Group    string          `json:"group"`
	Timezone string          `json:"timezone"`
	Since    string          `json:"since"`
	Until    string          `json:"until,omitempty"`
	Totals   store.Stats     `json:"totals"`
	Groups   []store.Group   `json:"groups"`
	Top      []store.TopItem `json:"top_issues"`
	// GitHub is set when enrichment is on; only then are the PR counts
	// present. They cover the cached pull requests of the filtered
	// project, opened or merged in the range.
	GitHub    bool `json:"github_enrichment"`
	PRsOpened *int `json:"prs_opened,omitempty"`
	PRsMerged *int `json:"prs_merged,omitempty"`
}

// apiUsage serves the Usage page's aggregates. since and until are RFC
// 3339 timestamps; since defaults to seven days ago. Totals leave out
// unmeasured runs, which are counted in unmeasured.
func (a *app) apiUsage(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		apiError(w, http.StatusServiceUnavailable, "run history is unavailable")
		return
	}
	q := r.URL.Query()
	group := q.Get("group")
	if group == "" {
		group = "project"
	}
	if !validGroup(group) {
		apiError(w, http.StatusBadRequest, "group must be "+strings.Join(usageGroups, ", "))
		return
	}
	f := store.UsageFilter{Project: q.Get("project"), Workflow: q.Get("workflow"), Model: q.Get("model")}
	f.Since = time.Now().UTC().AddDate(0, 0, -7).Format(tsLayout)
	for name, dst := range map[string]*string{"since": &f.Since, "until": &f.Until} {
		v := q.Get(name)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			apiError(w, http.StatusBadRequest, name+" must be an RFC 3339 timestamp")
			return
		}
		*dst = t.UTC().Format(tsLayout)
	}
	body := apiUsageBody{Group: group, Timezone: timezone, Since: f.Since, Until: f.Until, Groups: []store.Group{}, Top: []store.TopItem{}}
	groups, err := a.store.Usage(f, group)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	all, err := a.store.Usage(f, "all")
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	top, err := a.store.TopItems(f, topItemLimit)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a.githubOn() {
		o, m, err := a.store.PRDays(f.Project, f.Since, f.Until)
		if err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
		body.GitHub = true
		body.PRsOpened, body.PRsMerged = sum(o), sum(m)
	}
	body.Totals = all[0].Stats
	body.Groups = append(body.Groups, groups...)
	body.Top = append(body.Top, top...)
	writeJSON(w, http.StatusOK, body)
}

func sum(byDay map[string]int) *int {
	n := 0
	for _, v := range byDay {
		n += v
	}
	return &n
}
