package main

import (
	"context"
	"log"
	"time"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/config"
	fleetconfig "github.com/kinged007/jigz/internal/fleetconfig"
	machine "github.com/kinged007/jigz/internal/host"
	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/plan"
	"github.com/kinged007/jigz/internal/rollup"
	"github.com/kinged007/jigz/internal/store"
)

// startHistory opens the history database and starts the collector. The returned
// function stops the collector, waits for it, and closes the database. A
// database that cannot be opened is logged and leaves the fleet running
// without history.
func (a *app) startHistory() (stop func()) {
	st, err := store.Open(a.stateDir)
	if err != nil {
		log.Printf("history: %v; run history is unavailable", err)
		return func() {}
	}
	a.store = st
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c := &collector.Collector{
		Store:    st,
		Source:   a.src(),
		Model:    a.configuredModel,
		Kind:     a.workflowKind,
		Discover: func() []rollup.Loop { return loopsrc.Discover(a.projectRoots()) },
		Host:     machine.New(a.stateDir).Read,
	}
	a.enricher = &collector.Enricher{Store: st, Settings: a.githubSettings, Targets: a.githubTargets}
	a.questions = newQuestionsState(&collector.Scanner{Store: st, Settings: a.githubSettings, Targets: a.githubTargets})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	enrichDone := make(chan struct{})
	go func() {
		defer close(enrichDone)
		a.enricher.Run(ctx)
	}()
	questionsDone := make(chan struct{})
	go func() {
		defer close(questionsDone)
		a.questions.scanner.Run(ctx)
	}()
	return func() {
		cancel()
		wait := time.After(10 * time.Second)
		for _, ch := range []chan struct{}{done, enrichDone, questionsDone} {
			select {
			case <-ch:
			case <-wait:
				// A pass is still reading; the process is exiting anyway.
				return
			}
		}
		st.Close()
	}
}

// configuredModel is the model a workflow is configured with, "" when the
// config sets none or the project is unknown.
func (a *app) configuredModel(project, workflow string) string {
	for _, r := range a.projects() {
		if r.Repo.Repo == project {
			return r.FileSettings(workflow)["model"]
		}
	}
	return ""
}

// workflowKind is store.KindIssue or store.KindPR for a workflow, from the
// tracker kind in its file, "" when the project is unknown or the file
// cannot be read.
func (a *app) workflowKind(project, workflow string) string {
	for _, r := range a.projects() {
		if r.Repo.Repo == project {
			switch plan.TrackerKind(r, workflow) {
			case "github":
				return store.KindIssue
			case "github-pr":
				return store.KindPR
			}
			return ""
		}
	}
	return ""
}

// githubSettings is the github block of the projects' .jigz/config.yaml,
// read on each call so an edit applies without a restart. A --unite page
// spans several projects but runs one enricher and one scanner: the first
// project by slug that turns enrich on supplies the whole block, else the
// first project that has a readable block. a.github applies only when no
// project config can be read.
func (a *app) githubSettings() fleetconfig.GitHub {
	var dirs []string
	for _, r := range a.projects() {
		dirs = append(dirs, r.Dir)
	}
	if g, ok := githubFrom(dirs); ok {
		return g
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.github
}

// githubFrom picks the github block of the first dir that enables it, else
// of the first dir with a readable config.
func githubFrom(dirs []string) (fleetconfig.GitHub, bool) {
	var first *fleetconfig.GitHub
	for _, dir := range dirs {
		g, err := config.LoadGitHub(dir)
		if err != nil {
			continue
		}
		if g.Enrich {
			return g, true
		}
		if first == nil {
			first = &g
		}
	}
	if first == nil {
		return fleetconfig.GitHub{}, false
	}
	return *first, true
}

// githubOn reports whether GitHub enrichment is turned on.
func (a *app) githubOn() bool { return a.githubSettings().Enrich }

// githubTargets lists each project with the token it resolves to.
func (a *app) githubTargets() []collector.Target {
	projects := a.projects()
	out := make([]collector.Target, 0, len(projects))
	for _, r := range projects {
		out = append(out, projectTarget(r))
	}
	return out
}

// projectTarget resolves a project's token the way its loops do (JIG_TOKEN,
// then token: in .jigz/config.yaml, then GITHUB_TOKEN and GH_TOKEN), and
// falls back to the environment alone when the config cannot be read.
func projectTarget(r fleetconfig.Resolved) collector.Target {
	t := collector.Target{Project: r.Repo.Repo, Token: r.Token(), TokenEnv: r.TokenEnv}
	if r.TokenEnv == "" && r.Dir != "" {
		if tok, err := config.LoadToken(r.Dir); err == nil && tok != "" {
			t.Token = tok
		}
	}
	return t
}

// githubStatus is the last enrichment outcome of a project as the pages
// show it, "" when enrichment is off.
func (a *app) githubStatus(project string) string {
	if !a.githubOn() || a.enricher == nil {
		return ""
	}
	return a.enricher.Status(project).Text()
}
