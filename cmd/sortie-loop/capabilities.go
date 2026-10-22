package main

import (
	"os"
	"slices"
	"strings"

	"github.com/kinged007/sortie-loop/internal/engine"
	"github.com/kinged007/sortie-loop/internal/machine"
	"github.com/kinged007/sortie-loop/internal/plan"
)

// gate is what the engine this dashboard resolves can run.
func (a *app) gate() engine.Gate {
	bin, err := a.sup.Plan().SortieBinPath()
	if err != nil {
		return engine.NewGate(engine.Capabilities{Err: err.Error()}, engine.Current())
	}
	return engine.Detect(bin)
}

// loopFile is one installed workflow file of a project.
type loopFile struct{ project, name, text string }

// capabilityAlerts are the alerts for the features the engine cannot run.
func (a *app) capabilityAlerts() []alert {
	var files []loopFile
	for _, p := range a.projects() {
		for _, wf := range installedWorkflows(p) {
			if text, err := os.ReadFile(a.workflowPath(p, wf)); err == nil {
				files = append(files, loopFile{p.Repo.Repo, wf, string(text)})
			}
		}
	}
	var pool []string
	if path, err := machine.DefaultPath(); err == nil {
		if cfg, err := machine.Load(path); err == nil {
			for _, e := range cfg.Pool {
				pool = append(pool, e.Runtime)
			}
		}
	}
	return capAlerts(a.gate(), files, os.Environ(), pool)
}

// capAlerts has one alert for each distinct reason a feature is off.
func capAlerts(g engine.Gate, files []loopFile, env, pool []string) []alert {
	var out []alert
	seen := map[string]bool{}
	add := func(sev, kind, msg string) {
		if !seen[kind+msg] {
			seen[kind+msg] = true
			out = append(out, alert{sev, kind, msg, "/settings"})
		}
	}
	if why := g.Unknown(); why != "" {
		add("warn", "Engine", "The engine's features could not be detected, so none is disabled: "+why)
		return out
	}
	for _, f := range files {
		lg := plan.GateLoop(g, f.text, env)
		switch {
		case lg.Problem && lg.Off == g.LoopsOff():
			add("err", "Engine", lg.Off)
		case lg.Problem:
			add("err", "Agent kind", f.project+"/"+f.name+": "+lg.Off+" The loop is not started.")
		case lg.Off != "":
			add("warn", "Feature off", lg.Off)
		case lg.Note != "":
			add("warn", "Agent fallback", lg.Note)
		}
	}
	for _, rt := range machine.Runtimes {
		if off := g.RuntimeOff(rt); off != "" && slices.Contains(pool, rt) {
			add("warn", "Pool", off)
		}
	}
	return out
}

// engineCapabilities are the Settings rows for the detected version and
// kinds.
func engineCapabilities(g engine.Gate) []pair {
	if why := g.Unknown(); why != "" {
		return []pair{{"engine features", "unknown: " + why}}
	}
	c := g.Caps
	return []pair{
		{"engine trackers", strings.Join(c.Trackers, ", ")},
		{"engine agents", strings.Join(c.Agents, ", ")},
	}
}
