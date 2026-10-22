package main

import (
	"encoding/csv"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/store"
)

const runsPageSize = 50

const dateLayout = "2006-01-02"

// runQuery is the Runs page's filter as it appears in the URL.
type runQuery struct {
	Project, Workflow, State, Model, Adapter string
	From, To, Q                              string
	Cursor, After                            int64
}

func parseRunQuery(v url.Values) runQuery {
	q := runQuery{
		Project: v.Get("project"), Workflow: v.Get("workflow"), State: v.Get("state"),
		Model: v.Get("model"), Adapter: v.Get("adapter"), Q: strings.TrimSpace(v.Get("q")),
	}
	if _, err := time.Parse(dateLayout, v.Get("from")); err == nil {
		q.From = v.Get("from")
	}
	if _, err := time.Parse(dateLayout, v.Get("to")); err == nil {
		q.To = v.Get("to")
	}
	if n, err := strconv.ParseInt(v.Get("cursor"), 10, 64); err == nil && n > 0 {
		q.Cursor = n
	}
	if n, err := strconv.ParseInt(v.Get("after"), 10, 64); err == nil && n > 0 && q.Cursor == 0 {
		q.After = n
	}
	return q
}

// values is the query string for the filter alone, without paging.
func (q runQuery) values() url.Values {
	v := url.Values{}
	for k, s := range map[string]string{
		"project": q.Project, "workflow": q.Workflow, "state": q.State, "model": q.Model,
		"adapter": q.Adapter, "from": q.From, "to": q.To, "q": q.Q,
	} {
		if s != "" {
			v.Set(k, s)
		}
	}
	return v
}

func (q runQuery) Filtered() bool { return len(q.values()) > 0 }

func (q runQuery) filter() store.RunFilter {
	f := store.RunFilter{
		Project: q.Project, Workflow: q.Workflow, State: q.State, Model: q.Model, Adapter: q.Adapter, Q: q.Q,
		Cursor: q.Cursor, After: q.After,
	}
	if q.From != "" {
		f.Since = q.From + "T00:00:00Z"
	}
	if q.To != "" {
		to, _ := time.Parse(dateLayout, q.To)
		f.Until = to.AddDate(0, 0, 1).Format("2006-01-02T00:00:00Z")
	}
	return f
}

// runRow is one table row, already formatted for display.
type runRow struct {
	Identifier, Short, Title, IssueURL, Href string
	Project, Repo, Workflow, Rule, State     string
	Model, ModelSource, Adapter              string
	Started, Duration, Tokens, Error         string
	Outcome                                  store.Outcome
	Unmeasured, Active                       bool
	// PRs are the cached pull requests of the run, shown when GitHub
	// enrichment is on (Delivery).
	Delivery bool
	PRs      []store.PR
}

// shortIdentifier drops the owner from owner/repo#N.
func shortIdentifier(identifier string) string {
	if i := strings.Index(identifier, "/"); i >= 0 {
		return identifier[i+1:]
	}
	return identifier
}

func repoNameFromProject(project string) string {
	if i := strings.LastIndex(project, "/"); i >= 0 {
		return project[i+1:]
	}
	return project
}

func issueURL(project, identifier, issueID string) string {
	n := issueID
	if i := strings.LastIndex(identifier, "#"); i >= 0 {
		n = identifier[i+1:]
	}
	if _, err := strconv.Atoi(n); err != nil || !strings.Contains(project, "/") {
		return ""
	}
	return "https://github.com/" + project + "/issues/" + n
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func parseTS(ts string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, ts)
	return t, err == nil
}

func utc(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func spanText(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s < 60:
		return strconv.Itoa(s) + "s"
	case s < 3600:
		return strconv.Itoa(s/60) + "m " + pad2(s%60) + "s"
	default:
		return strconv.Itoa(s/3600) + "h " + pad2(s/60%60) + "m"
	}
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func duration(start, end string) string {
	s, ok1 := parseTS(start)
	e, ok2 := parseTS(end)
	if !ok1 || !ok2 || e.Before(s) {
		return ""
	}
	return spanText(e.Sub(s))
}

func tokenText(r store.Run) string {
	if !r.TokensMeasured {
		return ""
	}
	return commas(r.TotalTokens)
}

func storedRow(r store.Run) runRow {
	return runRow{
		Identifier: r.DisplayIdentifier, Short: shortIdentifier(r.DisplayIdentifier),
		IssueURL: issueURL(r.Project, r.DisplayIdentifier, r.IssueID),
		Href:     "/runs/" + r.Project + "/" + r.Workflow + "/" + strconv.FormatInt(r.ID, 10),
		Project:  r.Project, Repo: repoNameFromProject(r.Project), Workflow: r.Workflow, Rule: r.Workflow, State: r.Status,
		Model: r.Model, ModelSource: r.ModelSource, Adapter: r.AgentAdapter,
		Started: utc(r.StartedAt), Duration: duration(r.StartedAt, r.CompletedAt),
		Tokens: tokenText(r), Unmeasured: !r.TokensMeasured, Error: r.Error,
		Outcome: store.ClassifyOutcome(r.Status, r.Error),
	}
}

// activeRows are the fleet's running agents that match the filter. Their
// state is the engine's own, so a state filter matches it directly.
func activeRows(all []activeRun, q runQuery) []runRow {
	f := q.filter()
	var out []runRow
	for _, a := range all {
		picked, ok := parseTS(a.PickedUp)
		switch {
		case q.Project != "" && a.Repo != q.Project,
			q.Workflow != "" && a.Loop != q.Workflow,
			q.State != "" && a.State != q.State,
			q.Model != "" && a.Model != q.Model,
			q.Adapter != "" && a.Agent != q.Adapter,
			q.Q != "" && !strings.Contains(strings.ToLower(a.Identifier), strings.ToLower(q.Q)):
			continue
		}
		if ok && (f.Since != "" && a.PickedUp < f.Since || f.Until != "" && a.PickedUp >= f.Until) {
			continue
		}
		row := runRow{
			Identifier: a.Identifier, Short: shortIdentifier(a.Identifier), IssueURL: issueURL(a.Repo, a.Identifier, ""),
			Project: a.Repo, Repo: repoNameFromProject(a.Repo), Workflow: a.Loop, State: a.State,
			Model: a.Model, Adapter: a.Agent, Active: true,
			Href: liveHref(a.Repo, a.Loop, a.Identifier),
		}
		if a.Model != "" {
			row.ModelSource = store.ModelObserved
		}
		if ok {
			row.Started = picked.UTC().Format("2006-01-02 15:04")
			row.Duration = spanText(time.Since(picked))
		}
		if a.Tokens > 0 {
			row.Tokens = commas(a.Tokens)
		}
		out = append(out, row)
	}
	return out
}

type runsData struct {
	pageData
	Q                  runQuery
	States             []string
	Opts               store.RunOptions
	Live, Rows         []runRow
	Prev, Next, CSVURL string
	// Total is the number of finished runs matching the filter.
	Total, Page, Pages int
	Unavailable        bool
	// GitHub adds the pull request column.
	GitHub bool
}

// Cols is the number of table columns.
func (d runsData) Cols() int {
	if d.GitHub {
		return 12
	}
	return 11
}

func (a *app) runs(w http.ResponseWriter, r *http.Request) {
	q := parseRunQuery(r.URL.Query())
	d := runsData{pageData: a.dataFor(r, "", ""), Q: q, States: []string{"succeeded", "failed", "cancelled", "running", "retrying"}, Unavailable: a.store == nil}
	d.Nav, d.Title = "runs", "Runs"
	if a.store != nil {
		var err error
		if d.Opts, err = a.store.RunOptions(); err != nil {
			a.fail(w, "runs", err)
			return
		}
		f := q.filter()
		f.Limit = runsPageSize + 1
		stored, err := a.store.Runs(f)
		if err != nil {
			a.fail(w, "runs", err)
			return
		}
		more := len(stored) > runsPageSize
		if more {
			if q.After > 0 {
				stored = stored[len(stored)-runsPageSize:]
			} else {
				stored = stored[:runsPageSize]
			}
		}
		all := f
		all.Cursor, all.After = 0, 0
		if d.Total, err = a.store.CountRuns(all); err != nil {
			a.fail(w, "runs", err)
			return
		}
		d.Page, d.Pages = 1, max(1, (d.Total+runsPageSize-1)/runsPageSize)
		if len(stored) > 0 && (q.Cursor > 0 || q.After > 0) {
			all.After = stored[0].Cursor
			newer, err := a.store.CountRuns(all)
			if err != nil {
				a.fail(w, "runs", err)
				return
			}
			d.Page = min(d.Pages, newer/runsPageSize+1)
		}
		d.GitHub = a.githubOn()
		for _, s := range stored {
			row := storedRow(s)
			if d.GitHub {
				row.Delivery = true
				if row.PRs, err = a.store.RunPRs(s); err != nil {
					a.fail(w, "runs", err)
					return
				}
				if s.WorkflowKind == store.KindPR && len(row.PRs) > 0 {
					row.Title = row.PRs[0].Title
				}
			}
			d.Rows = append(d.Rows, row)
		}
		base := q.values()
		link := func(k string, n int64) string {
			v := url.Values{}
			for key, val := range base {
				v[key] = val
			}
			v.Set(k, strconv.FormatInt(n, 10))
			return "/runs?" + v.Encode()
		}
		if len(stored) > 0 {
			first, last := stored[0].Cursor, stored[len(stored)-1].Cursor
			if q.After > 0 {
				d.Next = link("cursor", last)
				if more {
					d.Prev = link("after", first)
				}
			} else {
				if more {
					d.Next = link("cursor", last)
				}
				if q.Cursor > 0 {
					d.Prev = link("after", first)
				}
			}
		}
		if q.Cursor == 0 && q.After == 0 {
			d.Live = activeRows(d.Active, q)
			for i := range d.Live {
				d.Live[i].Delivery = d.GitHub
			}
		}
		d.CSVURL = "/runs.csv"
		if len(base) > 0 {
			d.CSVURL += "?" + base.Encode()
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "runs", d); err != nil {
		log.Printf("runs: %v", err)
	}
}

func (a *app) fail(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	http.Error(w, "could not read run history", http.StatusInternalServerError)
}

// csvText stops a spreadsheet from running a value as a formula.
func csvText(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (a *app) runsCSV(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		http.Error(w, "run history is unavailable", http.StatusServiceUnavailable)
		return
	}
	f := parseRunQuery(r.URL.Query()).filter()
	f.Cursor, f.After, f.Limit = 0, 0, store.MaxLimit
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="sortie-runs.csv"`)
	out := csv.NewWriter(w)
	out.Write([]string{"run_id", "identifier", "project", "workflow", "rule", "state", "model", "model_source",
		"adapter", "started_at", "completed_at", "duration", "input_tokens", "output_tokens",
		"cache_read_tokens", "total_tokens", "tokens_measured", "outcome", "error"})
	for {
		runs, err := a.store.Runs(f)
		if err != nil {
			log.Printf("runs.csv: %v", err)
			break
		}
		for _, s := range runs {
			tok := []string{"", "", "", ""}
			if s.TokensMeasured {
				tok = []string{strconv.FormatInt(s.InputTokens, 10), strconv.FormatInt(s.OutputTokens, 10),
					strconv.FormatInt(s.CacheReadTokens, 10), strconv.FormatInt(s.TotalTokens, 10)}
			}
			out.Write(append(append([]string{
				strconv.FormatInt(s.ID, 10), csvText(s.DisplayIdentifier), csvText(s.Project), csvText(s.Workflow),
				csvText(s.Workflow), s.Status, csvText(s.Model), s.ModelSource, csvText(s.AgentAdapter),
				s.StartedAt, s.CompletedAt, duration(s.StartedAt, s.CompletedAt)}, tok...),
				strconv.FormatBool(s.TokensMeasured), store.ClassifyOutcome(s.Status, s.Error).Class, csvText(s.Error)))
		}
		if len(runs) < f.Limit {
			break
		}
		f.Cursor = runs[len(runs)-1].Cursor
	}
	out.Flush()
}
