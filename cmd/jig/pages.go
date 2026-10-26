package main

import (
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	fleetconfig "github.com/kinged007/jigz/internal/fleetconfig"
)

// projects returns one config per registered repository, in name order.
// The registry is the source of truth here: Jigz has no fleet
// config file, and a repo started after the page came up must appear
// without a restart.
func (a *app) projects() []fleetconfig.Resolved {
	var out []fleetconfig.Resolved
	for _, r := range a.repos() {
		out = append(out, fleetconfig.Resolved{
			Repo:      fleetconfig.Repo{Repo: r.Slug},
			Dir:       r.Dir,
			Workflows: r.Loops,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo.Repo < out[j].Repo.Repo })
	return out
}

// installedWorkflows lists the workflow names installed for a project.
func installedWorkflows(r fleetconfig.Resolved) []string {
	files, _ := filepath.Glob(filepath.Join(r.Dir, ".jigz", "workflows", "WORKFLOW.*.md"))
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "WORKFLOW."), ".md"))
	}
	return out
}

func (a *app) pageView(r *http.Request, nav, title string) pageData {
	d := a.dataFor(r, "", "")
	d.Nav, d.Title = nav, title
	return d
}

func execPage(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("%s: %v", name, err)
	}
}
