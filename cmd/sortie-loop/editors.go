package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kinged007/sortie-loop/internal/auth"
	"github.com/kinged007/sortie-loop/internal/edit"
	fleetconfig "github.com/kinged007/sortie-loop/internal/fleetconfig"
	"github.com/kinged007/sortie-loop/internal/loopsrc"
	"github.com/kinged007/sortie-loop/internal/store"
	"github.com/kinged007/sortie-loop/internal/supervise"
)

// How long a workflow edit waits for the repository's loops to answer
// again after the recycle request.
const defaultApplyWait = 45 * time.Second

var workflowName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// editSpec is one editor: the file it changes and how its form maps to
// the file's YAML.
type editSpec struct {
	Kind, ID, Title string
	Back, BackLabel string
	// Nav is the sidebar entry the editor sits under.
	Nav    string
	URL    string
	Target edit.Target
	// FrontOnly is set for workflow files, whose edits touch the front
	// matter and nothing after it.
	FrontOnly bool
	// RawURL is the advanced tab of this editor.
	RawURL string
	// Engine is the engine version the workflow's loop reports, empty
	// when unknown. hide lists the form fields that version cannot use.
	Engine string
	hide   []string

	form func() any
	// scope picks the mapping the form's paths are relative to.
	scope func(root *yaml.Node, create bool) *yaml.Node
	// field turns a path reported by a validator into a form field name.
	field func(path string) string
	// inherit gives the values a blank agent field falls back to.
	inherit func(c *fleetconfig.Config) map[string]string
}

func (a *app) centralTarget() edit.Target {
	return edit.Target{Scope: "central", Name: filepath.Base(a.configPath), Path: a.configPath}
}

func (a *app) project(slug string) (fleetconfig.Resolved, bool) {
	for _, r := range a.projects() {
		if r.Repo.Repo == slug {
			return r, true
		}
	}
	return fleetconfig.Resolved{}, false
}

func (a *app) workflowPath(r fleetconfig.Resolved, workflow string) string {
	return filepath.Join(r.Dir, ".sortie", "workflows", "WORKFLOW."+workflow+".md")
}

func slugOf(id string) string {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

func same(p string) string { return p }

// spec resolves an editor from its kind and id, or reports there is none.
func (a *app) spec(kind, id string) (*editSpec, bool) {
	root := func(root *yaml.Node, _ bool) *yaml.Node { return root }
	switch kind {
	case "central", "project", "agent":
		// The central, project and agent editors edit a fleet config
		// file, which sortie-loop does not have: its settings live in
		// .sortie/config.yaml with a different shape, and a form built for
		// the fleet document would replace them. Workflow files are the
		// one document both programs share.
		return nil, false
	case "workflow":
		r, ok := a.project(slugOf(id))
		parts := strings.Split(id, "/")
		if !ok || len(parts) != 3 || !workflowName.MatchString(parts[2]) {
			return nil, false
		}
		path := a.workflowPath(r, parts[2])
		if _, err := os.Stat(path); err != nil {
			return nil, false
		}
		engine := ""
		if op, ok := a.src().(loopsrc.Operator); ok {
			if st, err := op.Status(slugOf(id)); err == nil {
				engine = st.EngineVersion
			}
		}
		return &editSpec{Kind: kind, ID: id, Title: "Workflow " + parts[2] + " of " + slugOf(id), Back: "/workflows", BackLabel: "Workflows", Nav: "workflows",
			URL: "/edit/workflow/" + id, RawURL: "/edit/workflow/" + id + "?raw=1",
			Target:    edit.Target{Scope: slugOf(id), Name: filepath.Base(path), Path: path},
			FrontOnly: true, Engine: engine, hide: hideFor(engine),
			form: func() any { return &edit.Workflow{} }, scope: root, field: same}, true
	}
	return nil, false
}

// engineNum orders engine versions such as 1.26.0 or 1.24.0+pi.github-pr.1.
// An empty or unreadable version is 0.
func engineNum(version string) int {
	if i := strings.IndexAny(version, "+-"); i >= 0 {
		version = version[:i]
	}
	parts := strings.Split(version, ".")
	n := 0
	for i := 0; i < 3; i++ {
		p := 0
		if i < len(parts) {
			p, _ = strconv.Atoi(parts[i])
		}
		n = n*1000 + p
	}
	return n
}

// hideFor lists the form fields an engine version cannot use. An unknown
// version hides all of them.
func hideFor(engine string) []string {
	var hide []string
	for _, need := range []string{edit.EngineRuleSettings, edit.EngineStages} {
		if engineNum(engine) < engineNum(need) {
			hide = append(hide, need)
		}
	}
	return hide
}

// engineNotes says which rule fields are not offered and why.
func engineNotes(s *editSpec) []string {
	var notes []string
	seen := "the engine version of this loop is not known"
	if s.Engine != "" {
		seen = "this loop reports engine " + s.Engine
	}
	for _, tag := range s.hide {
		switch tag {
		case edit.EngineRuleSettings:
			notes = append(notes, "Per-rule model and effort need engine "+tag+"; "+seen+".")
		case edit.EngineStages:
			notes = append(notes, "Stage and next need engine "+tag+"; "+seen+".")
		}
	}
	return notes
}

// hasRows reports whether a table has a row that is filled in.
func hasRows(t edit.Table) bool {
	for _, row := range t.Rows {
		if row[0].Value != "" {
			return true
		}
	}
	return false
}

// trimRepoIndex drops the repos.<n>. prefix fleetconfig.Load puts on a path.
func trimRepoIndex(p string) string {
	rest, ok := strings.CutPrefix(p, "repos.")
	if !ok {
		return p
	}
	_, field, _ := strings.Cut(rest, ".")
	return field
}

// inheritFor is what an unset agent field of scope id falls back to.
func inheritFor(c *fleetconfig.Config, id string) map[string]string {
	parent := map[string]fleetconfig.Setting{}
	switch parts := strings.Split(id, "/"); len(parts) {
	case 2:
		parent = c.Cascade("", "")
	case 3:
		parent = c.Cascade(parts[0]+"/"+parts[1], "")
	}
	out := map[string]string{}
	for k, s := range parent {
		if k == "polling_interval_ms" {
			out[k] = s.Value
		} else {
			out["agent."+k] = s.Value
		}
	}
	return out
}

// read parses the form's current values out of the file's text.
func (s *editSpec) read(current string) (any, error) {
	text := current
	if s.FrontOnly {
		var err error
		if text, _, err = edit.SplitFrontMatter(current); err != nil {
			return nil, err
		}
	}
	d, err := edit.ParseDoc(text)
	if err != nil {
		return nil, err
	}
	v := s.form()
	if m := s.scope(d.Root(), false); m != nil {
		edit.Read(m, v, s.hide...)
	}
	return v, nil
}

// build makes the candidate from posted values.
func (s *editSpec) build(vals url.Values, v any) edit.Build {
	if vals.Get("mode") == "raw" {
		return edit.Raw(vals.Get("raw"), s.FrontOnly)
	}
	probs := edit.Parse(vals, v, s.hide...)
	write := func(d *edit.Doc) []edit.Problem {
		if len(probs) > 0 {
			return probs
		}
		m := s.scope(d.Root(), true)
		if m == nil {
			return []edit.Problem{{Msg: "this project is not in the config any more"}}
		}
		edit.Write(m, v, s.hide...)
		return nil
	}
	if s.FrontOnly {
		return edit.BuildFront(write)
	}
	return edit.BuildDoc(write)
}

// validate checks a candidate: fleetconfig.Load for the central file, and
// `sortie validate` on a sibling copy for a workflow file.
func (a *app) validate(t edit.Target, candidate string) (edit.Report, error) {
	if t.Scope == "central" {
		return validateCentral(t, candidate)
	}
	r, ok := a.project(t.Scope)
	if !ok {
		return edit.Report{}, fmt.Errorf("no project %q", t.Scope)
	}
	// Beside the real file, so the prompt paths it names resolve; the
	// leading dot keeps sortie-loop from discovering it as a workflow.
	tmp, err := os.CreateTemp(filepath.Dir(t.Path), ".candidate-*.md")
	if err != nil {
		return edit.Report{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(candidate); err != nil {
		tmp.Close()
		return edit.Report{}, err
	}
	if err := tmp.Close(); err != nil {
		return edit.Report{}, err
	}
	res, err := validateWorkflow(r.Dir, tmp.Name())
	if err != nil {
		return edit.Report{}, fmt.Errorf("could not run sortie validate: %w", err)
	}
	var rep edit.Report
	for _, e := range res.Errors {
		rep.Errors = append(rep.Errors, edit.Problem{Path: checkPath(e.Check), Msg: e.Message})
	}
	if !res.Valid && len(rep.Errors) == 0 {
		rep.Errors = append(rep.Errors, edit.Problem{Msg: "sortie validate rejected the file"})
	}
	for _, w := range res.Warnings {
		rep.Warnings = append(rep.Warnings, w.Message)
	}
	return rep, nil
}

var indexRe = regexp.MustCompile(`\[(\d+)\]`)

// checkPath turns the engine's fleetconfig.dispatch.rules[0].agent into the
// form's dispatch.rules.0.agent.
func checkPath(check string) string {
	return indexRe.ReplaceAllString(strings.TrimPrefix(check, "fleetconfig."), ".$1")
}

func validateCentral(t edit.Target, candidate string) (edit.Report, error) {
	tmp, err := os.CreateTemp(filepath.Dir(t.Path), filepath.Base(t.Path)+".*")
	if err != nil {
		return edit.Report{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(candidate); err != nil {
		tmp.Close()
		return edit.Report{}, err
	}
	if err := tmp.Close(); err != nil {
		return edit.Report{}, err
	}
	if _, err := fleetconfig.Load(tmp.Name()); err != nil {
		var fe *fleetconfig.FieldError
		p := edit.Problem{Msg: strings.ReplaceAll(err.Error(), tmp.Name(), t.Path)}
		if errors.As(err, &fe) {
			p.Path = fe.Path
		}
		return edit.Report{Errors: []edit.Problem{p}}, nil
	}
	return edit.Report{}, nil
}

// appApplier makes an installed file take effect.
type appApplier struct{ a *app }

// Apply reloads the central config, which recycles each running project
// whose settings moved as its loops go idle, or asks the project's
// sortie-loop to recycle its loops and waits for the edited workflow to
// come back. When the project's status document reports config hashes,
// the wait is for a restarted child with a different hash, and a restart
// with the same hash counts as the child running again. Without hashes the
// wait is for every loop of the project to answer. The string returned
// says which check confirmed the change. Added or removed workflow files
// and changes to the set of projects need a stop and start, which this
// does not do.
func (p appApplier) Apply(t edit.Target) (string, error) {
	a := p.a
	if t.Scope == "central" {
		var running []string
		for _, r := range a.repos() {
			if r.State == supervise.StateRunning {
				running = append(running, r.Slug)
			}
		}
		if err := a.reload(true); err != nil {
			return "", err
		}
		a.sup.StartAll()
		for _, slug := range running {
			if st := a.sup.State(slug); st != supervise.StateRunning && st != supervise.StateDisabled {
				return "", fmt.Errorf("%s is %s after the reload", slug, st)
			}
		}
		return "", nil
	}
	if a.sup.State(t.Scope) != supervise.StateRunning {
		return ": the project is not running, so the file applies when its loops start", nil
	}
	check, what := a.restartCheck(t.Scope, strings.TrimSuffix(strings.TrimPrefix(t.Name, "WORKFLOW."), ".md"))
	if err := a.sup.RecycleWhenIdle(t.Scope); err != nil {
		return "", err
	}
	wait := a.applyWait
	if wait == 0 {
		wait = defaultApplyWait
	}
	alive := func() error {
		if st := a.sup.State(t.Scope); st != supervise.StateRunning {
			return fmt.Errorf("sortie-loop for %s is %s after the recycle request", t.Scope, st)
		}
		return nil
	}
	return settle(wait, a.applyPoll(), what, alive, check)
}

// restartCheck is how a recycle of one workflow is recognized, with what
// it waits for. It compares the status document with its state now when
// the project reports config hashes, and asks every loop for an answer
// otherwise.
func (a *app) restartCheck(slug, workflow string) (func() (string, bool), string) {
	answers := func() (string, bool) { return ": child running again", a.loopsAnswer(slug) }
	op, ok := a.src().(loopsrc.Operator)
	if !ok || !op.ProjectCapabilities(slug).ConfigHash {
		return answers, "the loops of " + slug + " to answer"
	}
	st, err := op.Status(slug)
	if err != nil {
		return answers, "the loops of " + slug + " to answer"
	}
	prev, ok := st.Workflow(workflow)
	if !ok {
		return answers, "the loops of " + slug + " to answer"
	}
	return func() (string, bool) {
		st, err := op.Status(slug)
		if err != nil {
			return "", false
		}
		w, ok := st.Workflow(workflow)
		switch {
		case !ok:
		case loopsrc.Recycled(prev, w):
			return ": confirmed by config hash", true
		case w.State == "running" && w.ConfigHash == prev.ConfigHash && (w.RestartCount > prev.RestartCount || w.StartedAt.After(prev.StartedAt)):
			return ": child running again (its config hash is the same, so the edit is not part of the hash)", true
		}
		return "", false
	}, "workflow " + workflow + " of " + slug + " to restart"
}

// settle polls check until it reports done, which returns how, or until
// alive fails or wait passes, which is an error.
func settle(wait, poll time.Duration, what string, alive func() error, check func() (string, bool)) (string, error) {
	deadline := time.Now().Add(wait)
	for {
		time.Sleep(poll)
		if err := alive(); err != nil {
			return "", err
		}
		if how, done := check(); done {
			return how, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out after %s waiting for %s after the recycle request", wait, what)
		}
	}
}

func (a *app) applyPoll() time.Duration {
	if a.pollEvery > 0 {
		return a.pollEvery
	}
	return time.Second
}

// loopsAnswer reports whether every loop of the project answers.
func (a *app) loopsAnswer(slug string) bool {
	n := 0
	for _, l := range loopsFor(a.src().Fleet(), slug) {
		if !l.Live {
			return false
		}
		n++
	}
	return n > 0
}

// pipeline is one per app, so its lock serializes every edit.
func (a *app) pipeline() *edit.Pipeline {
	a.pipeOnce.Do(func() {
		a.mu.Lock()
		dir := a.stateDir
		a.mu.Unlock()
		a.pipe = &edit.Pipeline{Dir: filepath.Join(dir, "backups"), Store: a.store, Validate: a.validate, Applier: appApplier{a}}
	})
	return a.pipe
}

// editView is everything the editor templates render.
type editView struct {
	pageData
	Spec   *editSpec
	Stage  string // edit, preview, conflict or result
	Raw    bool
	RawTxt string
	Hash   string
	Groups []fieldGroup
	Tables []edit.Table
	Hidden []hiddenField
	Probs  []edit.Problem
	Warns  []string
	Diff   []diffLine
	NoDiff bool
	Result *edit.Outcome
	Steps  []stepView
	// Backups lists this file's backups, newest first.
	Backups []store.Backup
	// Kept names the front matter keys of a workflow file this form
	// leaves alone.
	Kept []string
	// Notes explain form fields left out for the engine version. AddRules
	// is set while the dispatch rules table is left out and can be added.
	Notes    []string
	AddRules bool
	// ShowRules asks for the rules table although the file has no rules.
	ShowRules bool
}

type fieldGroup struct {
	Title  string
	Fields []edit.Field
}

type hiddenField struct{ Name, Value string }

type diffLine struct{ Class, Text string }

type stepView struct{ Name, Class, Note string }

func groupFields(fields []edit.Field) []fieldGroup {
	var out []fieldGroup
	for _, f := range fields {
		if n := len(out); n > 0 && out[n-1].Title == f.Group {
			out[n-1].Fields = append(out[n-1].Fields, f)
			continue
		}
		out = append(out, fieldGroup{Title: f.Group, Fields: []edit.Field{f}})
	}
	return out
}

func diffLines(d string) []diffLine {
	var out []diffLine
	for _, l := range strings.Split(strings.TrimSuffix(d, "\n"), "\n") {
		c := "ctx"
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "@@"):
			c = "hdr"
		case strings.HasPrefix(l, "+"):
			c = "add"
		case strings.HasPrefix(l, "-"):
			c = "del"
		}
		out = append(out, diffLine{c, l})
	}
	return out
}

var pipelineSteps = []string{"Edit", "Validate", "Diff preview", "Backup", "Apply", "Rollback"}

// stepsFor marks how far the pipeline got.
func stepsFor(stage string, o *edit.Outcome) []stepView {
	state := make([]string, len(pipelineSteps))
	for i := range state {
		state[i] = "todo"
	}
	set := func(done int, cur string) {
		for i := 0; i < done; i++ {
			state[i] = "done"
		}
		if done < len(state) {
			state[done] = cur
		}
	}
	switch stage {
	case "edit":
		set(0, "cur")
	case "preview":
		set(2, "cur")
	case "conflict":
		set(2, "fail")
	case "result":
		by := map[string]string{"Rollback": "skip"}
		for _, st := range o.Steps {
			by[st.Name] = map[bool]string{true: "done", false: "fail"}[st.OK]
		}
		state[0], state[1], state[2], state[3], state[4], state[5] = "done", by["Validate"], "done", by["Backup"], by["Apply"], by["Rollback"]
		for i := range state {
			if state[i] == "" {
				state[i] = "todo"
			}
		}
	}
	out := make([]stepView, len(state))
	for i, s := range state {
		note := map[string]string{"fail": " (failed)", "done": " (done)", "skip": " (not needed)"}[s]
		out[i] = stepView{pipelineSteps[i], s, note}
	}
	return out
}

func (a *app) editViewFor(r *http.Request, s *editSpec) editView {
	d := a.dataFor(r, "", "")
	d.Nav, d.Title = s.Nav, "Edit "+s.Title
	v := editView{pageData: d, Spec: s}
	if a.store != nil {
		v.Backups, _ = a.store.Backups(s.Target.Path, edit.KeepBackups)
	}
	return v
}

func (a *app) renderEdit(w http.ResponseWriter, v editView) {
	v.Steps = stepsFor(v.Stage, v.Result)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "edit", v); err != nil {
		log.Printf("edit: %v", err)
	}
}

// editForm fills the edit stage from values: the file's own when vals is
// nil, otherwise what was posted.
func (a *app) editForm(v *editView, current string, vals url.Values, probs []edit.Problem) error {
	s := v.Spec
	v.Hash = edit.Hash(current)
	v.Raw = (vals != nil && vals.Get("mode") == "raw") || (vals == nil && v.Raw)
	if v.Raw {
		txt := current
		if s.FrontOnly {
			txt, _, _ = edit.SplitFrontMatter(current)
		}
		if vals != nil {
			txt = vals.Get("raw")
		}
		v.RawTxt = txt
		return nil
	}
	f, err := s.read(current)
	if err != nil {
		return err
	}
	if vals != nil {
		f = s.form()
		edit.Parse(vals, f, s.hide...)
	}
	var inherit map[string]string
	if s.inherit != nil {
		if c, err := fleetconfig.Load(a.configPath); err == nil {
			inherit = s.inherit(c)
		}
	}
	form := edit.Describe(f, probs, inherit, s.hide...)
	v.Groups, v.Tables = groupFields(form.Fields), form.Tables
	if s.FrontOnly {
		v.Kept, v.Notes = keptKeys(current), engineNotes(s)
		if len(v.Tables) > 0 && !v.ShowRules && !hasRows(v.Tables[0]) {
			v.Tables, v.AddRules = nil, true
		}
	}
	return nil
}

// keptKeys are the top-level front matter keys of a workflow file, which
// the form does not own wholesale.
func keptKeys(current string) []string {
	front, _, err := edit.SplitFrontMatter(current)
	if err != nil {
		return nil
	}
	d, err := edit.ParseDoc(front)
	if err != nil {
		return nil
	}
	var keys []string
	for i := 0; i+1 < len(d.Root().Content); i += 2 {
		keys = append(keys, d.Root().Content[i].Value)
	}
	return keys
}

// editGet shows an editor, for signed-in clients only.
func (a *app) editGet(kind string, rawDefault bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.Session(r).Authenticated {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		id := r.PathValue("owner") + "/" + r.PathValue("name")
		switch {
		case kind == "central":
			id = ""
		case kind == "agent":
			id = r.PathValue("scope")
		case kind == "workflow":
			id += "/" + r.PathValue("workflow")
		}
		s, ok := a.spec(kind, id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		a.showEditor(w, r, s, rawDefault || r.URL.Query().Get("raw") != "")
	}
}

func (a *app) showEditor(w http.ResponseWriter, r *http.Request, s *editSpec, raw bool) {
	v := a.editViewFor(r, s)
	v.Stage, v.Raw = "edit", raw
	v.ShowRules = r.URL.Query().Get("rules") != ""
	v.Msg, v.Err = r.URL.Query().Get("msg"), r.URL.Query().Get("err")
	cur, err := os.ReadFile(s.Target.Path)
	if err == nil {
		err = a.editForm(&v, string(cur), nil, nil)
	}
	if err != nil {
		v.Err = err.Error()
	}
	a.renderEdit(w, v)
}

func (a *app) editValidate(w http.ResponseWriter, r *http.Request) { a.editPost(w, r, false) }
func (a *app) editApply(w http.ResponseWriter, r *http.Request)    { a.editPost(w, r, true) }

func (a *app) editPost(w http.ResponseWriter, r *http.Request, apply bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	vals := r.PostForm
	s, ok := a.spec(vals.Get("kind"), vals.Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	v := a.editViewFor(r, s)
	v.Stage = "edit"
	pipe := a.pipeline()
	build := s.build(vals, s.form())
	hash := vals.Get("hash")
	cur, err := os.ReadFile(s.Target.Path)
	if err != nil {
		v.Err = err.Error()
		a.renderEdit(w, v)
		return
	}
	redisplay := func(probs []edit.Problem, warns []string) {
		v.Probs, v.Warns = a.mapProblems(s, probs), warns
		if err := a.editForm(&v, string(cur), vals, v.Probs); err != nil {
			v.Err = err.Error()
		}
		v.Hash = hash
		a.renderEdit(w, v)
	}
	if vals.Get("back") != "" {
		redisplay(nil, nil)
		return
	}
	hidden := hiddenFor(vals)
	switch {
	case apply:
		out, err := pipe.Apply(s.Target, hash, build, auth.Session(r).Actor)
		switch {
		case err != nil:
			v.Err = err.Error()
			redisplay(nil, nil)
		case out.Conflict:
			a.conflict(w, v, hidden, string(cur))
		case len(out.Report.Errors) > 0:
			redisplay(out.Report.Errors, nil)
		case out.NoChange:
			v.Stage, v.NoDiff = "preview", true
			a.renderEdit(w, v)
		default:
			v.Stage, v.Result = "result", &out
			v.Backups, _ = a.store.Backups(s.Target.Path, edit.KeepBackups)
			a.renderEdit(w, v)
		}
	default:
		pv, err := pipe.Preview(s.Target, hash, build)
		switch {
		case err != nil:
			v.Err = err.Error()
			redisplay(nil, nil)
		case pv.Stale:
			a.conflict(w, v, hidden, string(cur))
		case len(pv.Report.Errors) > 0:
			redisplay(pv.Report.Errors, pv.Report.Warnings)
		default:
			v.Stage, v.NoDiff = "preview", pv.Diff == ""
			v.Diff, v.Warns, v.Hash, v.Hidden = diffLines(pv.Diff), pv.Report.Warnings, pv.Hash, hidden
			a.renderEdit(w, v)
		}
	}
}

// conflict says the file changed since the form was loaded. The hidden
// fields carry the edits and the file's new hash, so "re-apply" is a
// plain validate of the same edits against the latest file.
func (a *app) conflict(w http.ResponseWriter, v editView, hidden []hiddenField, current string) {
	v.Stage, v.Hash = "conflict", edit.Hash(current)
	v.Hidden = hidden
	a.renderEdit(w, v)
}

// hiddenFor re-sends a post's values on the next form. The hash is left
// out because each page sets its own.
func hiddenFor(vals url.Values) []hiddenField {
	var out []hiddenField
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != auth.CSRFField && k != "hash" && k != "back" && k != "kind" && k != "id" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, val := range vals[k] {
			out = append(out, hiddenField{k, val})
		}
	}
	return out
}

func (a *app) mapProblems(s *editSpec, probs []edit.Problem) []edit.Problem {
	out := make([]edit.Problem, len(probs))
	for i, p := range probs {
		out[i] = edit.Problem{Path: s.field(p.Path), Msg: p.Msg}
	}
	return out
}

// backupsPage lists every saved backup, with a restore button each.
func (a *app) backupsPage(w http.ResponseWriter, r *http.Request) {
	if !auth.Session(r).Authenticated {
		http.Redirect(w, r, "/login?next=/backups", http.StatusSeeOther)
		return
	}
	d := backupsView{pageData: a.dataFor(r, r.URL.Query().Get("msg"), r.URL.Query().Get("err"))}
	d.Nav, d.Title = "settings", "Backups"
	if a.store != nil {
		var err error
		if d.Backups, err = a.store.Backups("", 200); err != nil {
			d.Err = err.Error()
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "backups", d); err != nil {
		log.Printf("backups: %v", err)
	}
}

type backupsView struct {
	pageData
	Backups []store.Backup
}

// backupRestore puts a backup back and shows the pipeline's report.
func (a *app) backupRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad backup id", http.StatusBadRequest)
		return
	}
	out, err := a.pipeline().Restore(id, auth.Session(r).Actor)
	d := backupsView{pageData: a.dataFor(r, "", "")}
	d.Nav, d.Title = "settings", "Restore"
	var res restoreView
	res.pageData = d.pageData
	res.Result = &out
	if err != nil {
		res.Err = err.Error()
		a.auditAs(auth.Session(r).Actor, "edit_restore", "", strconv.FormatInt(id, 10), err.Error())
	}
	res.Steps = stepsFor("result", &out)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "restore", res); err != nil {
		log.Printf("restore: %v", err)
	}
}

type restoreView struct {
	pageData
	Result *edit.Outcome
	Steps  []stepView
}
