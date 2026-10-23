package main

import (
	"database/sql"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/store"
)

type runDetailData struct {
	pageData
	Run              store.Run
	Row              runRow
	GitHub           bool
	PRs              []store.PR
	Detail           bool
	Journey          []journeyRow
	Attempts         []attemptRow
	Hidden           int
	Timeline         template.HTML
	TokenBars        template.HTML
	Events           []eventRow
	Summary          string
	Class            errorClass
	HandoffWhy       string
	Input, Output    string
	CacheRead, Total string
	FailedTokens     string
	Share            string
	IssueNumber      string
	OtherRuns        string
	Log              logCard
}

// logCard says where the log tail loads from, or why it cannot.
type logCard struct {
	URL, Why string
}

func (a *app) runDetail(w http.ResponseWriter, r *http.Request) {
	d := runDetailData{pageData: a.dataFor(r, "", "")}
	d.Nav, d.Title = "runs", "Run"
	notFound := func(code int, msg string) {
		d.Err = msg
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		if err := page.ExecuteTemplate(w, "run-missing", d); err != nil {
			log.Printf("run: %v", err)
		}
	}
	if issue, ok := liveID(r.PathValue("id")); ok {
		a.runLive(w, r, issue)
		return
	}
	if a.store == nil {
		notFound(http.StatusServiceUnavailable, "Run history is unavailable.")
		return
	}
	project := r.PathValue("owner") + "/" + r.PathValue("name")
	workflow := r.PathValue("workflow")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(http.StatusNotFound, "No such run.")
		return
	}
	run, err := a.store.Run(project, workflow, id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(http.StatusNotFound, "No such run.")
		return
	}
	if err != nil {
		a.fail(w, "run", err)
		return
	}
	runs, err := a.store.IssueRuns(project, workflow, run.IssueID)
	if err != nil {
		a.fail(w, "run", err)
		return
	}
	observed, err := a.store.IssueEvents(project, workflow, run.IssueID)
	if err != nil {
		a.fail(w, "run", err)
		return
	}
	d.Run, d.Row = run, storedRow(run)
	d.Attempts = buildAttempts(runs, run.ID)
	cur := slices.IndexFunc(d.Attempts, func(r attemptRow) bool { return r.Current })
	for _, r := range d.Attempts {
		if r.Collapsed {
			d.Hidden++
		}
	}
	d.Summary = summarize(d.Attempts, cur)
	d.Class = d.Attempts[cur].Class
	if d.Class.Key == store.OutcomeHandoffWithheld {
		d.HandoffWhy = handoffReason(run.Error)
	}
	d.Timeline = attemptTimeline(timelineLabel(run.DisplayIdentifier, d.Attempts), d.Attempts, observed)
	d.Events = runEvents(d.Attempts, observed)
	d.IssueNumber = issueNumber(run.DisplayIdentifier, run.IssueID)
	d.OtherRuns = "/runs?" + url.Values{"project": {project}, "workflow": {workflow}, "q": {d.IssueNumber}}.Encode()
	if d.GitHub = a.githubOn(); d.GitHub {
		d.Detail = a.githubSettings().DetailsOn()
		if d.PRs, err = a.runPRs(run); err != nil {
			a.fail(w, "run", err)
			return
		}
		if d.Journey, err = a.journey(run, d.PRs); err != nil {
			a.fail(w, "run", err)
			return
		}
	}
	if run.TokensMeasured {
		d.Input, d.Output = commas(run.InputTokens), commas(run.OutputTokens)
		d.CacheRead, d.Total = commas(run.CacheReadTokens), commas(run.TotalTokens)
		d.TokenBars, d.FailedTokens = attemptTokens(d.Attempts, run.ID)
		if d.Share, err = a.projectShare(run); err != nil {
			a.fail(w, "run", err)
			return
		}
	}
	d.Log = a.logCard(r, project, workflow)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "run", d); err != nil {
		log.Printf("run: %v", err)
	}
}

// runPRs returns the cached pull requests of a run, with the extra GitHub
// detail only when that is switched on.
func (a *app) runPRs(run store.Run) ([]store.PR, error) {
	prs, err := a.store.RunPRs(run)
	if !a.githubSettings().DetailsOn() {
		for i := range prs {
			prs[i].Details = nil
		}
	}
	return prs, err
}

// journeyRow is the latest run of one pull request workflow for a pull
// request linked to the viewed issue.
type journeyRow struct {
	runRow
	PR    int
	PRURL string
	// Runs is how many runs the workflow has for the pull request.
	Runs int
}

// journey lists the pull request workflow runs of the pull requests linked
// to an issue run, one row per pull request and workflow, in time order of
// the latest run. A pull request run has none.
func (a *app) journey(run store.Run, prs []store.PR) ([]journeyRow, error) {
	if run.WorkflowKind == store.KindPR {
		return nil, nil
	}
	runs, err := a.store.JourneyRuns(run.Project, run.IssueID)
	if err != nil {
		return nil, err
	}
	urls := map[int]string{}
	for _, p := range prs {
		urls[p.Number] = p.URL
	}
	var out []journeyRow
	at := map[string]int{}
	for _, r := range runs {
		n, _ := strconv.Atoi(r.IssueID)
		key := strconv.Itoa(n) + "/" + r.Workflow
		i, seen := at[key]
		if !seen {
			i = len(out)
			at[key] = i
			out = append(out, journeyRow{PR: n, PRURL: urls[n]})
		}
		out[i].runRow = storedRow(r)
		out[i].Runs++
	}
	slices.SortStableFunc(out, func(x, y journeyRow) int { return strings.Compare(x.Started, y.Started) })
	return out, nil
}

// issueNumber is the part of the identifier after "#", or the issue id.
func issueNumber(identifier, issueID string) string {
	if i := strings.LastIndex(identifier, "#"); i >= 0 {
		return identifier[i+1:]
	}
	return issueID
}

// attemptTokens draws the tokens of every measured attempt and sums what
// the failed attempts other than the viewed one used.
func attemptTokens(rows []attemptRow, current int64) (template.HTML, string) {
	var labels []string
	var stacks [][]int64
	var failed int64
	for _, r := range rows {
		if r.Unmeasured {
			continue
		}
		labels = append(labels, "#"+strconv.Itoa(r.Attempt))
		stacks = append(stacks, []int64{r.Run.InputTokens, r.Run.OutputTokens, r.Run.CacheReadTokens})
		if r.Status != "succeeded" && r.Run.ID != current {
			failed += r.Run.TotalTokens
		}
	}
	var bars template.HTML
	if len(rows) > 1 && len(labels) > 0 {
		segs := []barSegment{{"input", "var(--info)"}, {"output", "var(--ok)"}, {"cache read", "var(--idle)"}}
		bars = stackedBars(labels, segs, stacks, "Tokens per attempt")
	}
	if failed == 0 {
		return bars, ""
	}
	return bars, commas(failed)
}

// shareDays is the period a run's share of its project's tokens is
// measured over.
const shareDays = 7

// projectShare is the part of the project's tokens in the last shareDays
// days that this run used, or "" when the run is older than that.
func (a *app) projectShare(run store.Run) (string, error) {
	started, ok := parseTS(run.StartedAt)
	since := time.Now().UTC().AddDate(0, 0, -shareDays)
	if !ok || started.Before(since) || run.TotalTokens == 0 {
		return "", nil
	}
	g, err := a.store.Usage(store.UsageFilter{Project: run.Project, Since: since.Format("2006-01-02T15:04:05Z")}, "all")
	if err != nil || len(g) == 0 || g[0].Total == 0 {
		return "", err
	}
	return strconv.FormatFloat(float64(run.TotalTokens)/float64(g[0].Total)*100, 'f', 1, 64) + "%", nil
}

// logCard decides whether this viewer can load the workflow log.
func (a *app) logCard(r *http.Request, project, workflow string) logCard {
	op, ok := a.src().(loopsrc.Operator)
	switch {
	case !ok || !op.ProjectCapabilities(project).Logs:
		return logCard{Why: "needs Jigz logs"}
	}
	return logCard{URL: "/runs/" + project + "/" + workflow + "/log"}
}

const logLines = 200

// runLog returns the last lines of a workflow's log. The log belongs to
// the workflow, not to one run.
func (a *app) runLog(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("owner") + "/" + r.PathValue("name")
	workflow := r.PathValue("workflow")
	reply := func(lines []string, msg string) {
		writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "message": msg})
	}
	if c := a.logCard(r, project, workflow); c.Why != "" {
		reply([]string{}, c.Why)
		return
	}
	lines, err := a.src().(loopsrc.Operator).LogTail(project, workflow, logLines)
	switch {
	case errors.Is(err, os.ErrNotExist):
		reply([]string{}, "the log file does not exist; needs Jigz logs")
	case err != nil:
		log.Printf("run log: %v", err)
		reply([]string{}, "the log is not available")
	default:
		if lines == nil {
			lines = []string{}
		}
		reply(lines, "")
	}
}
