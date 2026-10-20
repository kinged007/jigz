package main

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kinged007/sortie-loop/internal/auth"
	"github.com/kinged007/sortie-loop/internal/config"
	fleetconfig "github.com/kinged007/sortie-loop/internal/fleetconfig"
	machine "github.com/kinged007/sortie-loop/internal/host"
	"github.com/kinged007/sortie-loop/internal/loopsrc"
	"github.com/kinged007/sortie-loop/internal/rollup"
	"github.com/kinged007/sortie-loop/internal/store"
	"github.com/kinged007/sortie-loop/internal/supervise"
)

// projectPeriodDays is the window of the token figure on the Projects page.
const projectPeriodDays = 14

// projectSummary is one project as the list shows it and the API returns it.
type projectSummary struct {
	Project   string   `json:"project"`
	State     string   `json:"state"`
	Enabled   bool     `json:"enabled"`
	Path      string   `json:"path"`
	Workflows []string `json:"workflows"`
	LoopsLive int      `json:"loops_live"`
	Running   int      `json:"running"`
	// Tokens are the measured tokens of runs started in the last PeriodDays.
	Tokens     int64  `json:"tokens_period"`
	PeriodDays int    `json:"period_days"`
	DiskBytes  *int64 `json:"disk_bytes,omitempty"`
	// Validation is valid, invalid or not checked, from the last time
	// central prepared the project.
	Validation       string `json:"validation"`
	ValidationDetail string `json:"validation_detail,omitempty"`
	// GitHub is the last enrichment outcome, absent when enrichment is off.
	GitHub string `json:"github_enrichment,omitempty"`
}

// DiskText is the directory size, or a dash until it is measured.
func (p projectSummary) DiskText() string {
	if p.DiskBytes == nil {
		return "—"
	}
	return size(*p.DiskBytes)
}

func (a *app) projectSummaries(d pageData) []projectSummary {
	var tokens map[string]int64
	if a.store != nil {
		tokens = a.tokensBy(time.Now().UTC().AddDate(0, 0, -projectPeriodDays).Format(tsLayout))
	}
	out := []projectSummary{}
	for _, v := range d.Repos {
		res, ok := a.project(v.Slug)
		if !ok {
			continue
		}
		s := projectSummary{
			Project: v.Slug, State: v.State, Enabled: res.Enabled(), Path: res.Dir,
			Workflows: projectWorkflows(res), Running: v.Running, Tokens: tokens[v.Slug], PeriodDays: projectPeriodDays,
			Validation: "not checked", GitHub: a.githubStatus(v.Slug),
		}
		for _, l := range loopsFor(d.Fleet, v.Slug) {
			if l.Live {
				s.LoopsLive++
			}
		}
		if v.DiskKnown {
			s.DiskBytes = &v.Disk
		}
		switch {
		case v.State == supervise.StateInvalid:
			s.Validation = "invalid"
			if len(v.Diags.Errors) > 0 {
				s.ValidationDetail = v.Diags.Errors[0]
			}
		case v.State == supervise.StateFailed:
			s.Validation, s.ValidationDetail = "failed", v.Err
		case v.State != supervise.StateDisabled && v.Diags.Valid:
			s.Validation = "valid"
		}
		out = append(out, s)
	}
	return out
}

// projectWorkflows is the configured workflow list, or the installed
// files when the project starts every one it has.
func projectWorkflows(r fleetconfig.Resolved) []string {
	if len(r.Workflows) > 0 {
		return r.Workflows
	}
	names := installedWorkflows(r)
	sort.Strings(names)
	if names == nil {
		names = []string{}
	}
	return names
}

type projectsView struct {
	pageData
	Projects []projectSummary
	GitHub   bool
}

func (a *app) projectsPage(w http.ResponseWriter, r *http.Request) {
	d := a.pageView(r, "projects", "Projects")
	execPage(w, "projects", projectsView{pageData: d, Projects: a.projectSummaries(d), GitHub: a.githubOn()})
}

func (a *app) apiProjects(w http.ResponseWriter, r *http.Request) {
	d := a.data("", "", requestHost(r), a.loopURLTemplate)
	writeJSON(w, http.StatusOK, struct {
		Projects []projectSummary `json:"projects"`
	}{a.projectSummaries(d)})
}

type cfgRow struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Origin string `json:"origin,omitempty"`
}

type wfStatus struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Warnings int    `json:"warnings,omitempty"`
}

type backupRow struct {
	store.Backup
	File string `json:"file"`
}

// diskSplit is where a project's disk use goes, in bytes: workspaces, the
// workflow databases, and the backups of its files.
type diskSplit struct {
	Workspaces int64 `json:"workspaces"`
	Databases  int64 `json:"databases"`
	Backups    int64 `json:"backups"`
}

// projectDetail is what the detail page shows and the API returns.
type projectDetail struct {
	projectSummary
	Config        []cfgRow            `json:"config"`
	WorkflowFiles []wfStatus          `json:"workflow_files"`
	Loops         []rollup.Loop       `json:"loops"`
	Disk          *diskSplit          `json:"disk_split,omitempty"`
	Status        *loopsrc.StatusInfo `json:"status,omitempty"`
	Backups       []backupRow         `json:"backups,omitempty"`
	Audit         []store.AuditEntry  `json:"audit,omitempty"`
}

// projLoop is one loop row of the detail page: the counts, when the loop
// last ran and last failed, and what the status document says about its
// process.
type projLoop struct {
	L                    rollup.Loop
	LastRun, LastFailure string
	Process              *loopsrc.WorkflowInfo
}

type projectView struct {
	pageData
	projectDetail
	Slug  string
	Files []wfFile
	Rows  []projLoop
	// Ops carries the sleep and cleanup lines. It is set only when the
	// project has a status document. ShowHash says the loop reports config
	// hashes.
	Ops      *opsProject
	ShowHash bool
}

// IsRunning decides between offering start and stop.
func (v projectView) IsRunning() bool { return v.State == supervise.StateRunning }

// detail gathers one project's page. Backups and audit rows are left out
// for a client that is not signed in: they carry paths and client addresses.
func (a *app) detail(r *http.Request, d pageData, slug string) (projectView, bool) {
	res, ok := a.project(slug)
	if !ok {
		return projectView{}, false
	}
	v := projectView{pageData: d, Slug: slug}
	for _, s := range a.projectSummaries(d) {
		if s.Project == slug {
			v.projectSummary = s
		}
	}
	v.Config = a.configRows(res)
	for _, n := range projectWorkflows(res) {
		f := a.workflowFile(res, n)
		v.Files = append(v.Files, f)
		v.WorkflowFiles = append(v.WorkflowFiles, wfStatus{Name: n, Status: f.Status, Detail: f.Detail, Warnings: f.Warnings})
	}
	v.Config = withFileSettings(v.Config, v.Files)
	v.Loops = loopsFor(d.Fleet, slug)
	a.applyStatus(&v, slug)
	for _, l := range v.Loops {
		row := projLoop{L: l}
		if v.Status != nil {
			if w, ok := v.Status.Workflow(l.Loop); ok {
				row.Process = &w
			}
		}
		row.LastRun, row.LastFailure = a.lastRuns(slug, l.Loop)
		v.Rows = append(v.Rows, row)
	}
	v.Disk = diskSplitOf(res.Dir, a.backupBytes(slug))
	if a.store != nil && auth.Session(r).Authenticated {
		v.Backups = a.projectBackups(slug)
		if rows, err := a.store.Audit(500); err == nil {
			for _, e := range rows {
				if e.Project == slug && len(v.Audit) < 10 {
					v.Audit = append(v.Audit, e)
				}
			}
		}
	}
	return v, true
}

// applyStatus reads the project's status document when its loop source
// has one, and leaves the view alone otherwise.
func (a *app) applyStatus(v *projectView, slug string) {
	op, ok := a.src().(loopsrc.Operator)
	if !ok {
		return
	}
	st, err := op.Status(slug)
	if err != nil {
		return
	}
	caps := op.ProjectCapabilities(slug)
	v.Status, v.ShowHash = &st, caps.ConfigHash
	v.Ops = &opsProject{Status: &st}
	v.Ops.applyStatus(caps)
}

// lastRuns is how long ago the loop last started a run and last failed one.
func (a *app) lastRuns(slug, workflow string) (run, failed string) {
	if a.store == nil {
		return "", ""
	}
	ago := func(state string) string {
		rows, err := a.store.Runs(store.RunFilter{Project: slug, Workflow: workflow, State: state, Limit: 1})
		if err != nil || len(rows) == 0 {
			return ""
		}
		if t, ok := parseTS(rows[0].StartedAt); ok {
			return agoText(t)
		}
		return ""
	}
	return ago(""), ago("failed")
}

// backupBytes is the size of the saved copies of the project's own files.
func (a *app) backupBytes(slug string) int64 {
	if a.store == nil {
		return 0
	}
	var n int64
	rows, _ := a.store.Backups("", 200)
	for _, b := range rows {
		if fi, err := os.Stat(b.BackupPath); err == nil && b.Scope == slug {
			n += fi.Size()
		}
	}
	return n
}

// diskSplitOf measures the parts of a project directory's disk use. It is
// nil when neither part can be read.
func diskSplitOf(dir string, backups int64) *diskSplit {
	ws := machine.DirSize(filepath.Join(dir, ".sortie", "workspaces"))
	db := machine.DirSize(filepath.Join(dir, ".sortie", "workflows"))
	if ws < 0 && db < 0 {
		return nil
	}
	return &diskSplit{Workspaces: max(ws, 0), Databases: max(db, 0), Backups: backups}
}

// withFileSettings adds the agent kind, model and polling interval read
// from the workflow files for the keys central's config does not set. A
// value that differs between files is listed per file.
func withFileSettings(rows []cfgRow, files []wfFile) []cfgRow {
	for _, k := range []struct {
		key string
		of  func(wfFile) string
	}{
		{"kind", func(f wfFile) string { return f.Agent }},
		{"model", func(f wfFile) string { return f.Model }},
		{"polling_interval_ms", func(f wfFile) string { return f.Polling }},
	} {
		if slices.ContainsFunc(rows, func(r cfgRow) bool { return r.Key == k.key }) {
			continue
		}
		var values, perFile []string
		for _, f := range files {
			if v := k.of(f); v != "" {
				values = append(values, v)
				perFile = append(perFile, f.Name+": "+v)
			}
		}
		switch {
		case len(values) == 0:
		case slices.Min(values) == slices.Max(values):
			rows = append(rows, cfgRow{k.key, values[0], "workflow"})
		default:
			rows = append(rows, cfgRow{k.key, strings.Join(perFile, ", "), "workflow"})
		}
	}
	return rows
}

// projectBackups are the backups of the project's own files and the newest
// few of the central file, which holds its entry.
func (a *app) projectBackups(slug string) []backupRow {
	var out []backupRow
	own, _ := a.store.Backups("", 200)
	for _, b := range own {
		if b.Scope == slug {
			out = append(out, backupRow{b, filepath.Base(b.Path)})
		}
	}
	central, _ := a.store.Backups(a.configPath, 5)
	for _, b := range central {
		out = append(out, backupRow{b, filepath.Base(b.Path)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// configRows is the effective settings of a project. sortie-loop reads
// them from that repository's own .sortie/config.yaml, which is the file
// the loops run with; there is no fleet cascade to report an origin from.
func (a *app) configRows(res fleetconfig.Resolved) []cfgRow {
	slug := res.Repo.Repo
	workflows := "all installed"
	if len(res.Workflows) > 0 {
		workflows = strings.Join(res.Workflows, ", ")
	}
	rows := []cfgRow{
		{"repo", slug, ""},
		{"path", res.Dir, ""},
		{"workflows", workflows, ""},
	}
	cfg, err := config.Load(res.Dir)
	if err != nil {
		return append(rows, cfgRow{"config", err.Error(), ""})
	}
	rows = append(rows,
		cfgRow{"tracker", cfg.Tracker, ""},
		cfgRow{"milestone", cfg.Milestone, ""},
	)
	if cfg.Assignee != nil {
		rows = append(rows, cfgRow{"assignee", *cfg.Assignee, ""})
	}
	if cfg.Sleep.Enabled {
		rows = append(rows, cfgRow{"sleep idle", cfg.Sleep.IdleTimeout.String(), ""})
	}
	if cfg.Cleanup.Enabled {
		rows = append(rows, cfgRow{"cleanup", cfg.Cleanup.Command, ""})
	}
	for _, name := range sortedKeys(cfg.Budgets) {
		b := cfg.Budgets[name]
		rows = append(rows, cfgRow{"budget " + name, fmtTokens(b.MaxTokens) + " tokens / " + b.Period, ""})
	}
	return rows
}

// sortedKeys returns a map's keys in order, so the table does not shuffle
// between renders.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (a *app) projectPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("owner") + "/" + r.PathValue("name")
	v, ok := a.detail(r, a.pageView(r, "projects", r.PathValue("name")), slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	execPage(w, "project", v)
}

func (a *app) apiProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("owner") + "/" + r.PathValue("name")
	v, ok := a.detail(r, a.data("", "", requestHost(r), a.loopURLTemplate), slug)
	if !ok {
		apiError(w, http.StatusNotFound, "no such project")
		return
	}
	if v.Loops == nil {
		v.Loops = []rollup.Loop{}
	}
	writeJSON(w, http.StatusOK, v.projectDetail)
}
