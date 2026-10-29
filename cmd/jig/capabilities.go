package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/machine"
	"github.com/kinged007/jigz/internal/plan"
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
	var runtimes []string
	seenRuntime := map[string]bool{}
	for _, p := range a.projects() {
		for _, wf := range installedWorkflows(p) {
			if text, err := os.ReadFile(a.workflowPath(p, wf)); err == nil {
				files = append(files, loopFile{p.Repo.Repo, wf, string(text)})
			}
		}
		if cfg, err := config.Load(p.Dir); err == nil {
			for _, name := range sortedKeys(cfg.Agents) {
				rt := cfg.Agents[name].Runtime
				if !seenRuntime[name+rt] {
					seenRuntime[name+rt] = true
					runtimes = append(runtimes, rt+"|"+name)
				}
			}
		}
	}
	return capAlerts(a.gate(), files, os.Environ(), runtimes)
}

// capAlerts has one alert for each distinct reason a feature is off.
func capAlerts(g engine.Gate, files []loopFile, env, runtimes []string) []alert {
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
		}
	}
	for _, rt := range machine.Runtimes {
		var names []string
		for _, rn := range runtimes {
			if r, n, ok := strings.Cut(rn, "|"); ok && r == rt {
				names = append(names, n)
			}
		}
		if off := g.RuntimeOff(rt); off != "" && len(names) > 0 {
			sort.Strings(names)
			add("warn", "Pool", fmt.Sprintf("agents %s use runtime %s, which the engine does not register", strings.Join(names, ", "), rt))
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
