package main

import (
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/store"
)

const livePrefix = "live-"

type runLiveData struct {
	pageData
	Agent    activeRun
	Project  string
	Workflow string
	Issue    string
	Href     string
	Started  string
	Elapsed  string
	Summary  string
	Attempts []attemptRow
	Timeline template.HTML
	Row      runRow
}

// liveHref is the live detail page of a running agent.
func liveHref(project, workflow, identifier string) string {
	return "/runs/" + project + "/" + workflow + "/" + livePrefix + issueNumber(identifier, "")
}

// runLive shows an agent that is running now. Its run is not stored until
// it ends, so the page is built from the fleet's live state plus the
// stored earlier attempts of the same issue. Once the agent is gone the
// page redirects to the latest stored run of the issue.
func (a *app) runLive(w http.ResponseWriter, r *http.Request, issue string) {
	d := runLiveData{pageData: a.dataFor(r, "", ""), Issue: issue}
	d.Nav, d.Title = "runs", "Live run"
	d.Project = r.PathValue("owner") + "/" + r.PathValue("name")
	d.Workflow = r.PathValue("workflow")
	var stored []store.Run
	if a.store != nil {
		var err error
		if stored, err = a.issueRuns(d.Project, d.Workflow, issue); err != nil {
			a.fail(w, "live run", err)
			return
		}
	}
	idx := -1
	for i, ar := range d.pageData.Active {
		if ar.Repo == d.Project && ar.Loop == d.Workflow && issueNumber(ar.Identifier, "") == issue {
			idx = i
			break
		}
	}
	if idx < 0 {
		if n := len(stored); n > 0 {
			http.Redirect(w, r, storedRow(stored[n-1]).Href, http.StatusSeeOther)
			return
		}
		d.Err = "No agent is running this issue and no run of it is stored."
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		if err := page.ExecuteTemplate(w, "run-missing", d); err != nil {
			log.Printf("live run: %v", err)
		}
		return
	}
	ar := d.pageData.Active[idx]
	d.Agent = ar
	d.Href = liveHref(d.Project, d.Workflow, ar.Identifier)
	now := time.Now().UTC()
	live := store.Run{
		ID: -1, Project: d.Project, Workflow: d.Workflow, IssueID: issue, DisplayIdentifier: ar.Identifier,
		Status: "running", Attempt: 1, StartedAt: ar.StartedAt, Model: ar.Model, AgentAdapter: ar.Agent,
		TurnsCompleted: ar.Turns, TokensMeasured: ar.Tokens > 0, TotalTokens: ar.Tokens,
	}
	if n := len(stored); n > 0 {
		live.Attempt = stored[n-1].Attempt + 1
	}
	d.Attempts = buildAttempts(append(stored, live), -1)
	cur := len(d.Attempts) - 1
	row := &d.Attempts[cur]
	row.Href, row.Class, row.ErrorExpected = d.Href, errorClass{}, false
	if row.started.IsZero() {
		row.started = now
	}
	row.ended = now
	row.Duration, row.Start = spanText(now.Sub(row.started)), utc(row.started.Format(time.RFC3339))
	d.Started, d.Elapsed = row.Start, spanText(time.Since(parseOr(ar.PickedUp, row.started)))
	d.Summary = summarize(d.Attempts, cur)
	d.Timeline = attemptTimeline(timelineLabel(ar.Identifier, d.Attempts), d.Attempts, nil)
	d.Row = runRow{IssueURL: issueURL(d.Project, ar.Identifier, issue)}
	d.Row.Href = "/runs?" + url.Values{"project": {d.Project}, "workflow": {d.Workflow}, "q": {issue}}.Encode()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "runlive", d); err != nil {
		log.Printf("live run: %v", err)
	}
}

func parseOr(ts string, fallback time.Time) time.Time {
	if t, ok := parseTS(ts); ok {
		return t
	}
	return fallback
}

// issueRuns are the stored runs of the issue with this number, oldest first.
func (a *app) issueRuns(project, workflow, number string) ([]store.Run, error) {
	found, err := a.store.Runs(store.RunFilter{Project: project, Workflow: workflow, Q: number, Limit: store.MaxLimit})
	if err != nil {
		return nil, err
	}
	for _, f := range found {
		if issueNumber(f.DisplayIdentifier, f.IssueID) == number {
			return a.store.IssueRuns(project, workflow, f.IssueID)
		}
	}
	return nil, nil
}

// liveID reports whether a run path segment names a live run, and the issue.
func liveID(id string) (string, bool) {
	n, ok := strings.CutPrefix(id, livePrefix)
	if _, err := strconv.Atoi(n); err != nil {
		return "", false
	}
	return n, ok
}
