package main

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	fleetconfig "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/plan"
)

type wfRule struct{ Name, Match, Agent, Template, Stage, Next string }

type wfFile struct {
	Name, Kind, EditURL string
	// Status is valid, invalid or unknown; Detail is the first error, or
	// why the check did not run.
	Status, Detail string
	Warnings       int
	NotStarted     bool
	Filter, Label  string
	Agent, Model   string
	Template       string
	Handoff        string
	Polling        string
	Rules          []wfRule
	// HasStage and HasNext say whether the rules table needs those columns.
	HasStage, HasNext bool
	Chain             template.HTML
}

type wfProject struct {
	Slug, State string
	Pipeline    template.HTML
	Files       []wfFile
	Idle        []wfFile
}

type workflowsView struct {
	pageData
	Projects []wfProject
}

func (a *app) workflows(w http.ResponseWriter, r *http.Request) {
	v := workflowsView{pageData: a.pageView(r, "workflows", "Workflows")}
	for _, p := range a.projects() {
		wp := wfProject{Slug: p.Repo.Repo, State: a.sup.State(p.Repo.Repo)}
		names := installedWorkflows(p)
		sort.Strings(names)
		var all []wfFile
		for _, n := range names {
			f := a.workflowFile(p, n)
			f.NotStarted = len(p.Workflows) > 0 && !slices.Contains(p.Workflows, n)
			all = append(all, f)
			if f.NotStarted {
				wp.Idle = append(wp.Idle, f)
			} else {
				wp.Files = append(wp.Files, f)
			}
		}
		raw, _ := os.ReadFile(filepath.Join(p.Dir, ".jigz", "fleetconfig.yaml"))
		wp.Pipeline = pipelineOf(all, plan.RepoLabelOrder(string(raw)))
		v.Projects = append(v.Projects, wp)
	}
	execPage(w, "workflows", v)
}

// pipelineOf draws the files that carry a stage label as the project's
// pipeline. Stages follow the order of labelOrder; a label it does not
// list comes after the listed ones.
func pipelineOf(files []wfFile, labelOrder []string) template.HTML {
	byLabel := map[string][]pipeBox{}
	var labels []string
	handoff := map[string]string{}
	for _, f := range files {
		if f.Label == "" {
			continue
		}
		if _, ok := byLabel[f.Label]; !ok {
			labels = append(labels, f.Label)
			handoff[f.Label] = f.Handoff
		}
		byLabel[f.Label] = append(byLabel[f.Label], pipeBox{Name: f.Name, Label: f.Label, NotStarted: f.NotStarted})
	}
	rank := func(l string) int {
		if i := slices.Index(labelOrder, l); i >= 0 {
			return i
		}
		return len(labelOrder)
	}
	sort.SliceStable(labels, func(i, j int) bool {
		if ri, rj := rank(labels[i]), rank(labels[j]); ri != rj {
			return ri < rj
		}
		return labels[i] < labels[j]
	})
	var cols [][]pipeBox
	for _, l := range labels {
		cols = append(cols, byLabel[l])
	}
	if len(labels) == 0 {
		return ""
	}
	return pipelineSVG(cols, handoff[labels[len(labels)-1]])
}

func pollingText(ms int) string {
	switch {
	case ms <= 0:
		return ""
	case ms%1000 == 0:
		return fmt.Sprintf("%d s", ms/1000)
	}
	return fmt.Sprintf("%d ms", ms)
}

func (a *app) workflowFile(p fleetconfig.Resolved, name string) wfFile {
	path := a.workflowPath(p, name)
	f := wfFile{Name: name, EditURL: "/edit/workflow/" + p.Repo.Repo + "/" + name, Status: "unknown"}
	raw, err := os.ReadFile(path)
	if err != nil {
		f.Detail = err.Error()
		return f
	}
	wf, err := plan.ParseWorkflow(string(raw))
	if err != nil {
		f.Status, f.Detail = "invalid", "front matter: "+err.Error()
		return f
	}
	f.Kind = map[string]string{"github": "issue workflow", "github-pr": "pull request workflow"}[wf.TrackerKind]
	if f.Kind == "" {
		f.Kind = wf.TrackerKind
	}
	f.Filter, f.Label = wf.QueryFilter, wf.StageLabel()
	f.Agent, f.Model, f.Template = wf.AgentKind, wf.Model, wf.Template
	f.Handoff, f.Polling = wf.HandoffState, pollingText(wf.PollingMS)
	for _, r := range wf.Rules {
		f.Rules = append(f.Rules, wfRule{r.Name, strings.Join(r.Labels, ", "), r.Agent, r.Template, r.Stage, r.Next})
		f.HasStage = f.HasStage || r.Stage != ""
		f.HasNext = f.HasNext || r.Next != ""
	}
	if len(f.Rules) > 0 {
		f.Chain = chainSVG(f.Rules)
	}
	res, err := validateWorkflow(p.Dir, path)
	switch {
	case err != nil:
		f.Detail = err.Error()
	case res.Valid:
		f.Status = "valid"
	default:
		f.Status = "invalid"
		if len(res.Errors) > 0 {
			f.Detail = res.Errors[0].Message
		}
	}
	f.Warnings = len(res.Warnings)
	return f
}
