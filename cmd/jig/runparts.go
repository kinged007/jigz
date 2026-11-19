package main

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/runlive"
	"github.com/kinged007/jigz/internal/store"
)

// livePollMillis is how often a running run's page asks for new data.
const livePollMillis = 4000

// timelineMax is the most rows the events timeline shows.
const timelineMax = 200

// runInput names the run whose files are read.
type runInput struct {
	Project, Workflow, IssueID string
	// RunID is the stored run id, 0 for a run in flight.
	RunID   int64
	Running bool
	Adapter string
	// SessionID is the session the stored run recorded.
	SessionID      string
	Started, Ended time.Time
}

type timelineRow struct {
	runlive.Event
	Source string
}

type sessionView struct {
	ID, Adapter, Workspace, Resume string
	WorkspaceMissing               bool
}

type liveTokens struct {
	Measured                    bool
	Model                       string
	Input, Output, Cache, Total string
	InputN, OutputN, CacheN     int64
	CacheWrite                  string
	UpdatedAt                   string
	SessionStarted              bool
}

type deliveryView struct {
	runlive.Delivery
	PRURL string
}

// runParts is what a run page shows that comes from the machine's files
// and changes while the run is in flight.
type runParts struct {
	Hide    hideView
	Running bool
	// Why says why the files of the run cannot be read.
	Why      string
	Session  sessionView
	Tokens   liveTokens
	Delivery deliveryView
	Timeline []timelineRow
	// Agent is the agent log, AgentWhy the reason it is empty and
	// AgentFrom where it was read.
	Agent     []runlive.Event
	AgentWhy  string
	AgentFrom string
}

// runPaths is where one workflow's files are on this machine.
type runPaths struct {
	base, db, home string
	why            string
}

func (a *app) homeDir() string {
	if a.liveHome != "" {
		return a.liveHome
	}
	h, _ := os.UserHomeDir()
	return h
}

func (a *app) runPaths(project, workflow string) runPaths {
	r, ok := a.src().(interface {
		Root(project string) (string, error)
	})
	if !ok {
		return runPaths{why: "needs Jigz logs"}
	}
	root, err := r.Root(project)
	if err != nil || root == "" {
		return runPaths{why: "this project is not on this machine"}
	}
	p := runPaths{base: filepath.Join(root, ".jigz", "workspaces"), home: a.homeDir()}
	for _, l := range loopsrc.Discover(map[string]string{project: root}) {
		if l.Loop == workflow {
			p.db = l.DBPath
		}
	}
	return p
}

// engineLines returns the last lines of the workflow log, or nil.
func (a *app) engineLines(project, workflow string) []string {
	op, ok := a.src().(loopsrc.Operator)
	if !ok || !op.ProjectCapabilities(project).Logs {
		return nil
	}
	lines, _ := op.LogTail(project, workflow, 1000)
	return lines
}

func prURL(d runlive.Delivery) string {
	if d.PR <= 0 || !runlive.ValidID(d.Owner) || !runlive.ValidID(d.Repo) {
		return ""
	}
	return "https://github.com/" + d.Owner + "/" + d.Repo + "/pull/" + strconv.Itoa(d.PR)
}

// runFiles is where the files of one run are and what the engine recorded
// about it.
type runFiles struct {
	paths      runPaths
	meta       runlive.RunMeta
	live       runlive.LiveSession
	haveLive   bool
	lines      []string
	workspace  string
	kind       string
	sessionID  string
	transcript string
}

// locate finds the files of a run.
func (a *app) locate(in runInput, paths runPaths) runFiles {
	f := runFiles{paths: paths}
	if paths.db != "" {
		if in.Running {
			f.live, f.haveLive, _ = runlive.ReadLive(paths.db, in.IssueID)
		} else {
			f.meta, _ = runlive.ReadRun(paths.db, in.RunID)
		}
	}
	from, to := in.Started.Add(-5*time.Second), time.Time{}
	if !in.Ended.IsZero() {
		to = in.Ended.Add(5 * time.Second)
	}
	f.lines = runlive.IssueLines(a.engineLines(in.Project, in.Workflow), in.IssueID, from, to)
	f.workspace = runlive.Locate(paths.base, in.Workflow, in.IssueID, f.meta.Workspace)
	f.kind = runlive.AgentKind(in.Adapter)
	id := firstNonEmpty(in.SessionID, f.meta.SessionID, f.live.SessionID, runlive.LogSession(f.lines))
	var found string
	f.transcript, found = runlive.FindTranscript(paths.home, f.kind, f.workspace, id, in.Started.Add(-time.Minute))
	f.sessionID = firstNonEmpty(id, found)
	return f
}

// runParts reads the files of a run. A section the dashboard hides is not
// read.
func (a *app) runParts(in runInput) runParts {
	hide := a.hideView()
	p := runParts{Hide: hide, Running: in.Running}
	p.Session = sessionView{ID: in.SessionID, Adapter: in.Adapter}
	paths := a.runPaths(in.Project, in.Workflow)
	if paths.base == "" {
		p.Why = paths.why
		return p
	}
	f := a.locate(in, paths)
	if f.haveLive {
		live := f.live
		p.Tokens = liveTokens{
			Measured: live.Total > 0, Model: live.Model,
			Input: commas(live.Input), Output: commas(live.Output), Cache: commas(live.CacheRead), Total: commas(live.Total),
			InputN: live.Input, OutputN: live.Output, CacheN: live.CacheRead,
			CacheWrite: commas(live.CacheWrite), UpdatedAt: live.UpdatedAt, SessionStarted: true,
		}
	}
	d := runlive.ReadDelivery(paths.base, f.workspace)
	p.Delivery = deliveryView{Delivery: d, PRURL: prURL(d)}

	if !hide.HidesSection("session") {
		p.Session = sessionView{ID: f.sessionID, Adapter: in.Adapter, Workspace: f.workspace, Resume: runlive.ResumeCommand(f.kind, f.workspace, f.sessionID)}
		_, err := os.Stat(f.workspace)
		p.Session.WorkspaceMissing = err != nil
	}
	if !hide.HidesSection("agent_log") {
		p.Agent, p.AgentFrom, p.AgentWhy = a.agentEvents(paths, in, f.meta, f.transcript, f.kind)
	}
	p.Timeline = timeline(p.Agent, f.lines, hide)
	return p
}

// agentEvents reads the agent log of a run: the engine's event rows once
// the run has ended, else the agent's own transcript.
func (a *app) agentEvents(paths runPaths, in runInput, meta runlive.RunMeta, transcript, kind string) ([]runlive.Event, string, string) {
	if !in.Running && paths.db != "" {
		evs, err := runlive.ReadRunEvents(paths.db, meta.DispatchID)
		if err != nil {
			log.Printf("run events: %v", err)
		}
		if len(evs) > 0 {
			return evs[max(len(evs)-runlive.MaxEvents, 0):], "engine events", ""
		}
	}
	switch {
	case kind == "":
		return nil, "", "the agent of this run has no transcript Jigz can read"
	case transcript == "":
		return nil, "", "no agent transcript found on this machine"
	}
	evs, err := runlive.TailEvents(transcript, kind, runlive.MaxEvents)
	if err != nil {
		log.Printf("agent transcript: %v", err)
		return nil, "", "the agent transcript could not be read"
	}
	return evs, "agent transcript", ""
}

// timeline merges the engine log milestones and the agent events by time.
func timeline(agent []runlive.Event, lines []string, hide hideView) []timelineRow {
	var rows []timelineRow
	if !hide.HidesSection("log_tail") {
		for _, e := range runlive.LogEvents(lines) {
			rows = append(rows, timelineRow{e, "engine log"})
		}
	}
	for _, e := range agent {
		e.Text = ""
		rows = append(rows, timelineRow{e, "agent"})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ti, _ := parseTS(rows[i].Time)
		tj, _ := parseTS(rows[j].Time)
		return ti.Before(tj)
	})
	return rows[max(len(rows)-timelineMax, 0):]
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// eventTime formats the time of an event.
func eventTime(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// renderPart renders a named template to a string.
func renderPart(name string, data any) string {
	var buf bytes.Buffer
	if err := page.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("%s: %v", name, err)
	}
	return buf.String()
}

// runAgentLog answers the polling request of the agent log tab.
func (a *app) runAgentLog(w http.ResponseWriter, r *http.Request) {
	in, ok := a.runInputFor(r)
	if !ok {
		writeJSON(w, http.StatusGone, map[string]any{"running": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"running": in.Running, "html": renderPart("run-agentlog", a.runParts(in))})
}

// runAgentEvent returns the full text of one agent log event. The event is
// named by the ref of the event list, so no path comes from the request.
func (a *app) runAgentEvent(w http.ResponseWriter, r *http.Request) {
	in, ok := a.runInputFor(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	paths := a.runPaths(in.Project, in.Workflow)
	if paths.base == "" {
		http.NotFound(w, r)
		return
	}
	f := a.locate(in, paths)
	ref := r.URL.Query().Get("ref")
	var text string
	var more int
	var err error
	switch {
	case strings.HasPrefix(ref, "d") && !in.Running && paths.db != "":
		seq, perr := strconv.ParseInt(ref[1:], 10, 64)
		if perr != nil || f.meta.DispatchID == "" {
			http.NotFound(w, r)
			return
		}
		text, more, err = runlive.ReadRowText(paths.db, f.meta.DispatchID, seq)
	case strings.HasPrefix(ref, "t") && f.transcript != "" && f.kind != "":
		off, sub, perr := parseTranscriptRef(ref)
		if perr != nil {
			http.NotFound(w, r)
			return
		}
		text, more, err = runlive.EventText(f.transcript, f.kind, off, sub)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "more": more, "limit": runlive.MaxFullText})
}

func parseTranscriptRef(ref string) (int64, int, error) {
	o, s, ok := strings.Cut(ref[1:], ".")
	if !ok {
		return 0, 0, errors.New("bad ref")
	}
	off, err := strconv.ParseInt(o, 10, 64)
	if err != nil {
		return 0, 0, err
	}
	sub, err := strconv.Atoi(s)
	return off, sub, err
}

// runInputFor finds the run a request names, stored or in flight.
func (a *app) runInputFor(r *http.Request) (runInput, bool) {
	project := r.PathValue("owner") + "/" + r.PathValue("name")
	workflow := r.PathValue("workflow")
	if issue, ok := liveID(r.PathValue("id")); ok {
		for _, ar := range a.dataFor(r, "", "").Active {
			if ar.Repo == project && ar.Loop == workflow && issueNumber(ar.Identifier, "") == issue {
				return liveInput(project, workflow, issue, ar), true
			}
		}
		return runInput{}, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if a.store == nil || err != nil {
		return runInput{}, false
	}
	run, err := a.store.Run(project, workflow, id)
	if err != nil {
		return runInput{}, false
	}
	return storedInput(run), true
}

func liveInput(project, workflow, issue string, ar activeRun) runInput {
	started, _ := parseTS(ar.StartedAt)
	return runInput{Project: project, Workflow: workflow, IssueID: issue, Running: true, Adapter: ar.Agent, Started: started}
}

func storedInput(run store.Run) runInput {
	started, _ := parseTS(run.StartedAt)
	ended, _ := parseTS(run.CompletedAt)
	return runInput{
		Project: run.Project, Workflow: run.Workflow, IssueID: run.IssueID, RunID: run.ID,
		Adapter: run.AgentAdapter, SessionID: run.SessionID, Started: started, Ended: ended,
	}
}
