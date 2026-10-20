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
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/kinged007/sortie-loop/internal/auth"
	"github.com/kinged007/sortie-loop/internal/edit"
	fleetconfig "github.com/kinged007/sortie-loop/internal/fleetconfig"
	"github.com/kinged007/sortie-loop/internal/plan"
	"github.com/kinged007/sortie-loop/internal/store"
	"github.com/kinged007/sortie-loop/internal/supervise"
)

var (
	ownerRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	repoRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// wizKeys are the form fields the wizard carries from step to step, named
// after the project and agent editors' own fields.
var wizKeys = []string{"repo", "path", "token_env", "workflows", "assignee", "milestone", "filters", "agent.kind", "agent.model"}

var wizStep1 = []string{"repo", "path", "token_env"}

var wizStepNames = []string{"Repository", "Workflows", "Dry run", "Create"}

// maxOutputLines is how much of a command's output a result shows.
const maxOutputLines = 40

type dryRow struct {
	What, Detail string
	Warn         bool
}

type stepResult struct {
	Name string
	// Status is ok, failed or skipped.
	Status, Note, Output string
}

type wizardView struct {
	pageData
	Step   int
	Steps  []stepView
	F      map[string]edit.Field
	Known  []knownWorkflow
	Hidden []hiddenField
	Probs  []edit.Problem
	// Token says whether the variable that will hold the token is set in
	// central's own environment.
	Token dryRow
	// Checked shows the token result on step 1 after the Check button.
	Checked  bool
	DefPath  string
	Dry      []dryRow
	Diff     []diffLine
	CanBuild bool
	Warns    []string
}

type knownWorkflow struct {
	Name    string
	Checked bool
}

// knownWorkflows are the workflow names the config and the installed
// projects already use, which is what central can offer without
// inventing names.
func (a *app) knownWorkflows() []string {
	seen := map[string]bool{}
	for _, p := range a.projects() {
		for _, n := range append(slices.Clone(p.Workflows), installedWorkflows(p)...) {
			seen[n] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// wizValues keeps the wizard's fields of a post. Workflows may arrive as
// several checkbox values or one comma separated value; both come out as
// one comma separated value.
func wizValues(src url.Values) url.Values {
	v := url.Values{}
	for _, k := range wizKeys {
		v.Set(k, strings.TrimSpace(src.Get(k)))
	}
	var names []string
	for _, w := range src["workflows"] {
		for _, n := range strings.Split(w, ",") {
			if n = strings.TrimSpace(n); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	v.Set("workflows", strings.Join(names, ","))
	return v
}

// parseWizard reads the entry the wizard describes and what is wrong with it.
func (a *app) parseWizard(v url.Values) (edit.Project, edit.Agent, []edit.Problem) {
	var p edit.Project
	var ag edit.Agent
	probs := append(edit.Parse(v, &p), edit.Parse(v, &ag)...)
	add := func(path, msg string) { probs = append(probs, edit.Problem{Path: path, Msg: msg}) }
	owner, name, ok := strings.Cut(p.Repo, "/")
	switch {
	case !ok || !ownerRe.MatchString(owner) || !repoRe.MatchString(name) || name == "." || name == "..":
		add("repo", "Use owner/name, for example acme/mobile.")
	case a.hasProject(p.Repo):
		add("repo", "This repository is already in the fleetconfig.")
	}
	if p.Path != "" && !filepath.IsAbs(p.Path) && !strings.HasPrefix(p.Path, "~/") {
		add("path", "Use an absolute path, or one starting with ~/.")
	} else if dir, err := a.dirFor(p); err == nil && a.pathTaken(dir, p.Repo) {
		add("path", "Another project already uses this path.")
	}
	if p.TokenEnv != "" && !envNameRe.MatchString(p.TokenEnv) {
		add("token_env", "Use the name of an environment variable, for example GH_TOKEN_ACME.")
	}
	for _, n := range p.Workflows {
		if !workflowName.MatchString(n) {
			add("workflows", fmt.Sprintf("%q is not a workflow name.", n))
		}
	}
	return p, ag, probs
}

func (a *app) hasProject(slug string) bool {
	for _, p := range a.projects() {
		if strings.EqualFold(p.Repo.Repo, slug) {
			return true
		}
	}
	return false
}

func (a *app) pathTaken(dir, slug string) bool {
	for _, p := range a.projects() {
		if p.Dir == dir && p.Repo.Repo != slug {
			return true
		}
	}
	return false
}

// dirFor is where the project's .sortie directory will be.
func (a *app) dirFor(p edit.Project) (string, error) {
	cfg, err := fleetconfig.Load(a.configPath)
	if err != nil {
		return "", err
	}
	return cfg.Dir(fleetconfig.Repo{Repo: p.Repo, Path: p.Path})
}

// tokenRow says which variable will hold the token and whether central
// has it. The value is never read into the page.
func (a *app) tokenRow(p edit.Project) dryRow {
	name := p.TokenEnv
	if cfg, err := fleetconfig.Load(a.configPath); err == nil && name == "" {
		name = cfg.Defaults.TokenEnv
	}
	set := false
	if name == "" {
		name, set = "GITHUB_TOKEN or GH_TOKEN", os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("GH_TOKEN") != ""
	} else {
		set = os.Getenv(name) != ""
	}
	if set {
		return dryRow{What: "Token", Detail: name + " is set in central's environment."}
	}
	return dryRow{What: "Token", Detail: name + " is not set in central's environment. Setup will fail when it talks to GitHub. Add it to the .env file beside the config and restart central.", Warn: true}
}

// wizardStepOf is the step a problem on field belongs to.
func wizardStepOf(field string) int {
	if slices.Contains(wizStep1, field) {
		return 1
	}
	return 2
}

func (a *app) wizardGet(w http.ResponseWriter, r *http.Request) {
	if !auth.Session(r).Authenticated {
		http.Redirect(w, r, "/login?next=/projects/new", http.StatusSeeOther)
		return
	}
	a.showWizard(w, r, 1, url.Values{}, nil, false)
}

// wizardPost moves the wizard one step: each step posts the fields it
// shows plus the ones carried from the others.
func (a *app) wizardPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	step, _ := strconv.Atoi(r.PostFormValue("step"))
	v := wizValues(r.PostForm)
	if r.PostFormValue("back") != "" {
		a.showWizard(w, r, max(step-1, 1), v, nil, false)
		return
	}
	if r.PostFormValue("check") != "" {
		a.showWizard(w, r, step, v, nil, false)
		return
	}
	_, _, probs := a.parseWizard(v)
	if len(probs) > 0 {
		first := 3
		for _, p := range probs {
			first = min(first, wizardStepOf(p.Path))
		}
		a.showWizard(w, r, first, v, probs, false)
		return
	}
	switch step {
	case 1:
		a.showWizard(w, r, 2, v, nil, true)
	case 2:
		a.showWizard(w, r, 3, v, nil, false)
	default:
		a.createResult(w, r, v)
	}
}

func (a *app) showWizard(w http.ResponseWriter, r *http.Request, step int, v url.Values, probs []edit.Problem, firstVisit bool) {
	p, ag, _ := a.parseWizard(v)
	view := wizardView{pageData: a.pageView(r, "projects", "Add project"), Step: step, Probs: probs, Checked: r.PostFormValue("check") != ""}
	for i, n := range wizStepNames {
		c := ""
		switch {
		case i+1 < step:
			c = "done"
		case i+1 == step:
			c = "cur"
		}
		view.Steps = append(view.Steps, stepView{Name: n, Class: c})
	}
	inherit := map[string]string{}
	if cfg, err := fleetconfig.Load(a.configPath); err == nil {
		inherit = inheritFor(cfg, "new/project")
		inherit["assignee"], inherit["milestone"], inherit["token_env"] = deref(cfg.Defaults.Assignee), cfg.Defaults.Milestone, cfg.Defaults.TokenEnv
	}
	view.F = map[string]edit.Field{}
	for _, f := range append(edit.Describe(&p, probs, inherit).Fields, edit.Describe(&ag, probs, inherit).Fields...) {
		view.F[f.Name] = f
	}
	for _, n := range a.knownWorkflows() {
		view.Known = append(view.Known, knownWorkflow{n, firstVisit || slices.Contains(p.Workflows, n)})
	}
	a.mu.Lock()
	view.DefPath = filepath.Join(a.stateDir, "repos", "owner", "name")
	a.mu.Unlock()
	shown := map[int][]string{1: wizStep1, 2: {"workflows", "assignee", "milestone", "filters", "agent.kind", "agent.model"}}[step]
	for _, k := range wizKeys {
		if !slices.Contains(shown, k) {
			view.Hidden = append(view.Hidden, hiddenField{k, v.Get(k)})
		}
	}
	view.Token = a.tokenRow(p)
	if step == 3 {
		a.dryRun(&view, p, ag)
	}
	execPage(w, "wizard", view)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// addEntry builds the config with the project's entry appended, disabled.
func addEntry(p edit.Project, ag edit.Agent) edit.Build {
	off := false
	p.Enabled = &off
	return edit.BuildDoc(func(d *edit.Doc) []edit.Problem {
		if edit.Repo(d.Root(), p.Repo) != nil {
			return []edit.Problem{{Path: "repo", Msg: "This repository is already in the fleetconfig."}}
		}
		m := edit.AddRepo(d.Root(), p.Repo)
		edit.Write(m, &p)
		edit.Write(m, &ag)
		return nil
	})
}

func setEnabled(slug string, on bool) edit.Build {
	return edit.BuildDoc(func(d *edit.Doc) []edit.Problem {
		m := edit.Repo(d.Root(), slug)
		if m == nil {
			return []edit.Problem{{Msg: slug + " is not in the config any more."}}
		}
		var p edit.Project
		edit.Read(m, &p)
		p.Enabled = &on
		edit.Write(m, &p)
		return nil
	})
}

func removeEntry(slug string) edit.Build {
	return edit.BuildDoc(func(d *edit.Doc) []edit.Problem {
		if !edit.RemoveRepo(d.Root(), slug) {
			return []edit.Problem{{Msg: slug + " is not in the config any more."}}
		}
		return nil
	})
}

// dryRun fills step 3: what creating the project would change, without
// writing the config, the project directory or anything on GitHub.
func (a *app) dryRun(view *wizardView, p edit.Project, ag edit.Agent) {
	pv, err := a.pipeline().Preview(a.centralTarget(), "", addEntry(p, ag))
	switch {
	case err != nil:
		view.Err = err.Error()
		return
	case !pv.Report.OK():
		view.Probs = append(view.Probs, pv.Report.Errors...)
		return
	}
	view.CanBuild = true
	view.Diff, view.Warns = diffLines(pv.Diff), pv.Report.Warnings
	dir, err := a.dirFor(p)
	if err != nil {
		view.Err = err.Error()
		view.CanBuild = false
		return
	}
	view.Dry = append(view.Dry,
		dryRow{What: "Config entry", Detail: "Add " + p.Repo + " to " + a.configPath + " with enabled: false. It is switched on after setup and validation pass."},
		dryRow{What: "Project directory", Detail: dir + existsNote(dir)},
		dryRow{What: "Config overlay", Detail: filepath.Join(dir, ".sortie", "fleetconfig.yaml") + " with the repository, assignee, milestone and filters."})
	if len(p.Workflows) == 0 {
		view.Dry = append(view.Dry, dryRow{What: "Workflow files", Detail: "Every workflow sortie-loop setup ships, under " + filepath.Join(dir, ".sortie", "workflows") + "."})
	}
	for _, n := range p.Workflows {
		file := filepath.Join(dir, ".sortie", "workflows", "WORKFLOW."+n+".md")
		note := " (added by setup)"
		if _, err := os.Stat(file); err == nil {
			note = " (already there, setup keeps it)"
		}
		view.Dry = append(view.Dry, dryRow{What: "Workflow file", Detail: file + note})
	}
	view.Dry = append(view.Dry,
		dryRow{What: "GitHub labels", Detail: "Synced by setup. It adds labels and never removes any."},
		view.Token)
}

func existsNote(path string) string {
	if _, err := os.Stat(path); err == nil {
		return " (already exists)"
	}
	return ""
}

type resultView struct {
	pageData
	Kind  string
	Slug  string
	Steps []stepResult
	// Failed is the name of the step that failed, empty when all passed.
	Failed string
}

func (a *app) renderResult(w http.ResponseWriter, r *http.Request, kind, slug string, steps []stepResult) {
	v := resultView{pageData: a.pageView(r, "projects", map[string]string{"add": "Add project", "remove": "Remove project"}[kind]), Kind: kind, Slug: slug, Steps: steps}
	for _, s := range steps {
		if s.Status == "failed" {
			v.Failed = s.Name
		}
	}
	execPage(w, "projectresult", v)
	a.audit(r, map[string]string{"add": "project_create", "remove": "project_remove"}[kind], slug, "", auditResult(steps))
}

func auditResult(steps []stepResult) string {
	for _, s := range steps {
		if s.Status == "failed" {
			return "failed at " + s.Name + ": " + s.Note
		}
	}
	return "ok"
}

// runSteps runs the steps in order and stops at the first failure; the
// steps after it are listed as skipped.
func runSteps(names []string, run []func() (note, output string, err error)) []stepResult {
	var out []stepResult
	for i, f := range run {
		note, output, err := f()
		if err != nil {
			out = append(out, stepResult{names[i], "failed", err.Error(), output})
			for _, n := range names[i+1:] {
				out = append(out, stepResult{Name: n, Status: "skipped"})
			}
			return out
		}
		out = append(out, stepResult{names[i], "ok", note, output})
	}
	return out
}

// tail keeps the last n lines of a command's output.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// applyCentral runs a config change through the edit pipeline. A
// returned error says why nothing is live; output lists the pipeline's
// own steps.
func (a *app) applyCentral(build edit.Build, actor string) (note, output string, err error) {
	cur, err := os.ReadFile(a.configPath)
	if err != nil {
		return "", "", err
	}
	o, err := a.pipeline().Apply(a.centralTarget(), edit.Hash(string(cur)), build, actor)
	var lines []string
	for _, s := range o.Steps {
		lines = append(lines, fmt.Sprintf("%s: %s", s.Name, s.Msg))
	}
	output = strings.Join(lines, "\n")
	switch {
	case err != nil:
		return "", output, err
	case o.Conflict:
		return "", output, errors.New("the config file changed while this ran. Nothing was written. Try again")
	case len(o.Report.Errors) > 0:
		msgs := make([]string, len(o.Report.Errors))
		for i, p := range o.Report.Errors {
			msgs[i] = p.Msg
		}
		return "", output, errors.New("the config change is not valid: " + strings.Join(msgs, "; "))
	case o.NoChange:
		return "the config already says this", output, nil
	case !o.Applied:
		return "", output, errors.New("the change was not applied, and the previous config is back in place")
	}
	return "config updated, backup " + strconv.FormatInt(o.BackupID, 10), output, nil
}

// createProject runs the add steps. The entry is written disabled and is
// only enabled once setup and validation have passed, so a failure at any
// step leaves it disabled.
func (a *app) createProject(v url.Values, actor string) []stepResult {
	p, ag, _ := a.parseWizard(v)
	slug := p.Repo
	var res fleetconfig.Resolved
	return runSteps([]string{"Write repo entry", "Run setup", "Validate workflows", "Start project"}, []func() (string, string, error){
		func() (string, string, error) { return a.applyCentral(addEntry(p, ag), actor) },
		func() (string, string, error) {
			var ok bool
			if res, ok = a.project(slug); !ok {
				return "", "", errors.New("the project is not in the loaded config")
			}
			if err := plan.Apply(res); err != nil {
				return "", "", err
			}
			out, err := a.sup.Plan().RunSetup(res)
			if err != nil {
				return "", tail(out, maxOutputLines), fmt.Errorf("sortie-loop setup failed: %w", err)
			}
			if err := a.sup.Plan().ApplySettings(res); err != nil {
				return "", tail(out, maxOutputLines), err
			}
			a.measureSizes()
			return "sortie-loop setup finished", tail(out, maxOutputLines), nil
		},
		func() (string, string, error) { return a.validateWorkflows(res) },
		func() (string, string, error) { return a.startProject(slug, actor) },
	})
}

// measureSizes refreshes the disk figures in the background, so a new
// project shows its size without waiting for the next timer pass.
func (a *app) measureSizes() {
	if a.sizes != nil {
		go a.sizes.measure(a.repos())
	}
}

// validateWorkflows runs sortie validate on each workflow the project
// starts.
func (a *app) validateWorkflows(res fleetconfig.Resolved) (string, string, error) {
	names := projectWorkflows(res)
	if len(names) == 0 {
		return "", "", errors.New("setup installed no workflow files")
	}
	var lines []string
	bad := 0
	for _, n := range names {
		path := a.workflowPath(res, n)
		if _, err := os.Stat(path); err != nil {
			lines = append(lines, n+": the workflow file is missing")
			bad++
			continue
		}
		fr, err := a.sup.Plan().ValidateFile(res, path)
		switch {
		case err != nil:
			return "", strings.Join(lines, "\n"), fmt.Errorf("could not run sortie validate on %s: %w", n, err)
		case fr.Valid:
			lines = append(lines, n+": valid")
		default:
			bad++
			lines = append(lines, n+": invalid")
			for _, e := range fr.Errors {
				lines = append(lines, "  "+e.Message)
			}
		}
	}
	out := strings.Join(lines, "\n")
	if bad > 0 {
		return "", out, fmt.Errorf("%d of %d workflow files did not validate", bad, len(names))
	}
	return fmt.Sprintf("%d workflow files valid", len(names)), out, nil
}

// startProject enables the entry, which makes central start it, and puts
// the entry back to disabled when it did not come up.
func (a *app) startProject(slug, actor string) (string, string, error) {
	if _, out, err := a.applyCentral(setEnabled(slug, true), actor); err != nil {
		return "", out, err
	}
	if a.sup.State(slug) == supervise.StateRunning {
		return "the loops are running", "", nil
	}
	var why []string
	for _, r := range a.repos() {
		if r.Slug != slug {
			continue
		}
		why = append(why, "state: "+r.State)
		if r.Err != "" {
			why = append(why, r.Err)
		}
		why = append(why, r.Diags.Errors...)
		why = append(why, r.Output...)
	}
	note := "the project did not start and was left disabled"
	if _, _, err := a.applyCentral(setEnabled(slug, false), actor); err != nil {
		note = "the project did not start, and disabling it again failed: " + err.Error()
	}
	return "", tail(strings.Join(why, "\n"), maxOutputLines), errors.New(note)
}

func (a *app) createResult(w http.ResponseWriter, r *http.Request, v url.Values) {
	p, _, _ := a.parseWizard(v)
	a.renderResult(w, r, "add", p.Repo, a.createProject(v, auth.Session(r).Actor))
}

// within reports whether path is dir or inside it, and where.
func within(dir, path string) (string, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// resolved is a path made absolute with symlinks followed where it exists.
func resolved(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// deletable says why a project's directory must not be deleted, or nil.
// Only a directory strictly inside the state directory qualifies, and not
// one that holds central's own files or another project's directory.
func (a *app) deletable(res fleetconfig.Resolved) error {
	a.mu.Lock()
	state := a.stateDir
	a.mu.Unlock()
	if state == "" {
		return errors.New("the state directory is not known")
	}
	root, dir := resolved(state), resolved(res.Dir)
	rel, ok := within(root, dir)
	switch {
	case !ok:
		return fmt.Errorf("%s is outside the state directory %s", dir, root)
	case rel == ".":
		return fmt.Errorf("%s is the state directory itself", dir)
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	if strings.HasPrefix(first, store.FileName) || first == "backups" || first == "session.key" {
		return fmt.Errorf("%s holds central's own files", dir)
	}
	for _, o := range a.projects() {
		if o.Repo.Repo == res.Repo.Repo {
			continue
		}
		if _, nested := within(dir, resolved(o.Dir)); nested {
			return fmt.Errorf("%s contains the directory of %s", dir, o.Repo.Repo)
		}
	}
	return nil
}

type removeView struct {
	pageData
	Slug, Dir string
	Running   bool
	// NoDelete is why the files cannot be deleted, empty when they can.
	NoDelete    string
	DeleteFiles bool
}

func (a *app) projectRemove(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("owner") + "/" + r.PathValue("name")
	res, ok := a.project(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	del := r.PostFormValue("delete_files") != ""
	v := removeView{pageData: a.pageView(r, "projects", "Remove "+slug), Slug: slug, Dir: res.Dir, DeleteFiles: del,
		Running: a.sup.State(slug) == supervise.StateRunning}
	if err := a.deletable(res); err != nil {
		v.NoDelete = err.Error()
	}
	switch {
	case len(a.projects()) < 2:
		v.Err = "Central needs at least one project in its config, so the last one cannot be removed."
	case del && v.NoDelete != "":
		v.Err = "The files were not deleted and nothing was changed: " + v.NoDelete + "."
	case del && r.PostFormValue("typed") != slug:
		v.Err = "To delete the files, type " + slug + " in the box."
	}
	if r.PostFormValue("confirm") == "" || v.Err != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if v.Err != "" && r.PostFormValue("confirm") != "" {
			w.WriteHeader(http.StatusBadRequest)
		}
		if err := page.ExecuteTemplate(w, "projectremove", v); err != nil {
			log.Printf("projectremove: %v", err)
		}
		return
	}
	a.renderResult(w, r, "remove", slug, a.removeProject(res, del, auth.Session(r).Actor))
}

// removeProject disables the project, which stops its loops, takes the
// entry out of the config, and optionally deletes its directory.
func (a *app) removeProject(res fleetconfig.Resolved, deleteFiles bool, actor string) []stepResult {
	slug := res.Repo.Repo
	wasRunning := a.sup.State(slug) == supervise.StateRunning
	return runSteps([]string{"Disable project", "Stop loops", "Remove repo entry", "Delete files"}, []func() (string, string, error){
		func() (string, string, error) { return a.applyCentral(setEnabled(slug, false), actor) },
		func() (string, string, error) {
			if a.sup.State(slug) == supervise.StateRunning {
				if err := a.sup.Stop(slug); err != nil {
					return "", "", err
				}
			}
			if wasRunning {
				return "the loops were stopped", "", nil
			}
			return "the loops were not running", "", nil
		},
		func() (string, string, error) { return a.applyCentral(removeEntry(slug), actor) },
		func() (string, string, error) {
			if !deleteFiles {
				return "kept " + res.Dir, "", nil
			}
			if err := a.deletable(res); err != nil {
				return "", "", err
			}
			dir := resolved(res.Dir)
			if err := os.RemoveAll(dir); err != nil {
				return "", "", err
			}
			return "deleted " + dir, "", nil
		},
	})
}
