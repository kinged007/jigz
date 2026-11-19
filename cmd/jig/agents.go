package main

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/engine"
	fleetconfig "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/machine"
	"github.com/kinged007/jigz/internal/plan"
)

// agentColumn is one shown knob. The editors list the rest.
type agentColumn struct{ Key, Label string }

var agentColumns = []agentColumn{
	{"kind", "kind"}, {"command", "command"}, {"model", "model"}, {"max_turns", "max_turns"},
	{"max_concurrent_agents", "concurrency"}, {"turn_timeout_ms", "turn timeout"},
	{"max_retry_backoff_ms", "backoff max"}, {"polling_interval_ms", "poll every"},
}

// agentCell is the value in force and where it comes from. File is the
// workflow file's own value when Jigz sets a different one.
type agentCell struct{ Value, Origin, File string }

type agentRow struct {
	Label, Note string
	Group       bool
	Cells       []agentCell
}

type agentsView struct {
	pageData
	Columns []agentColumn
	Span    int
	Rows    []agentRow
	Fleets  []fleetView
}

// fleetView is one repository's configured fleet: operating hours, the
// agents with their availability, and the pools with their members.
type fleetView struct {
	Repo      string
	Error     string
	Operating string
	Agents    []fleetAgentRow
	Pools     []fleetPoolRow
}

type fleetAgentRow struct {
	Name    string
	Runtime string
	Model   string
	Hours   string
	State   string
	Reason  string
	Loops   []string
}

type fleetPoolRow struct {
	Name   string
	Agents []string
}

func (a *app) agents(w http.ResponseWriter, r *http.Request) {
	v := agentsView{pageData: a.pageView(r, "agents", "Agents"), Columns: agentColumns}
	v.Span = len(agentColumns) + 1
	for _, p := range a.projects() {
		slug := p.Repo.Repo
		v.Rows = append(v.Rows, agentRow{Label: slug, Group: true})
		names := p.Workflows
		if len(names) == 0 {
			names = installedWorkflows(p)
			sort.Strings(names)
		}
		for _, n := range names {
			file, note := a.workflowKnobs(p, n)
			row := agentRow{Label: n, Note: note}
			for _, col := range agentColumns {
				row.Cells = append(row.Cells, agentCellFor(file[col.Key]))
			}
			v.Rows = append(v.Rows, row)
		}
		if f := fleetViewFor(p, a.gate(), time.Now()); f.Repo != "" {
			v.Fleets = append(v.Fleets, f)
		}
	}
	execPage(w, "agents", v)
}

// fleetViewFor describes one repository's configured fleet: operating
// hours, every agent with its availability, and the pools with their
// members. A repository without a config file is skipped.
func fleetViewFor(p fleetconfig.Resolved, g engine.Gate, now time.Time) fleetView {
	v := fleetView{Repo: p.Repo.Repo}
	cfg, err := config.Load(p.Dir)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	if len(cfg.OperatingHours.Windows) == 0 {
		v.Operating = "24/7"
	} else {
		v.Operating = strings.Join(windowTexts(cfg.OperatingHours), "; ") + ", end " + endOrFinish(cfg.OperatingHours.End)
	}
	var st machine.State
	if sp := machine.StatePath(p.Dir); sp != "" {
		st, _ = machine.LoadState(sp)
	}
	refs := cfg.UnresolvedRefs()
	served := map[string][]string{}
	for slug, res := range cfg.Resolve() {
		names := res.Agents
		if len(names) == 0 {
			for _, r := range res.Rules {
				names = append(names, r.Agents...)
			}
		}
		for _, n := range names {
			served[n] = append(served[n], slug)
		}
	}
	for _, name := range sortedKeys(cfg.Agents) {
		a := cfg.Agents[name]
		row := fleetAgentRow{Name: name, Runtime: a.Runtime, Model: a.Model, Hours: "always", State: "available"}
		if len(a.Schedule.Windows) > 0 {
			row.Hours = strings.Join(windowTexts(a.Schedule), "; ")
		}
		switch {
		case a.Runtime != "" && g.RuntimeOff(a.Runtime) != "":
			row.State, row.Reason = "unavailable", g.RuntimeOff(a.Runtime)
		case len(refs.Agents[name]) > 0:
			row.State, row.Reason = "unavailable", "unresolved references: "+strings.Join(refs.Agents[name], ", ")
		case now.Before(st.Agents[name].ExhaustedUntil):
			row.State, row.Reason = "exhausted", exhaustionText(st.Agents[name])
		case !a.Schedule.Open(now):
			row.State = "outside working hours"
		}
		row.Loops = served[name]
		sort.Strings(row.Loops)
		v.Agents = append(v.Agents, row)
	}
	for _, name := range sortedKeys(cfg.Pools) {
		v.Pools = append(v.Pools, fleetPoolRow{Name: name, Agents: cfg.Pools[name]})
	}
	return v
}

func exhaustionText(es machine.EntryState) string {
	if es.Reason == "" {
		return "usage limit, usable again " + es.ExhaustedUntil.Format(time.RFC3339)
	}
	return es.Reason + ", usable again " + es.ExhaustedUntil.Format(time.RFC3339)
}
func windowTexts(h machine.Hours) []string {
	var out []string
	for _, w := range h.Windows {
		out = append(out, strings.Join(w.Days, ",")+" "+w.Start+"-"+w.End)
	}
	return out
}

// agentCellFor turns a workflow file's knob value into a cell. The value
// comes from the file alone: Jigz has no fleet cascade, so there
// is nothing for it to override.
func agentCellFor(value string) agentCell {
	if value == "" {
		return agentCell{}
	}
	return agentCell{Value: value, Origin: "file"}
}

func (a *app) workflowKnobs(p fleetconfig.Resolved, name string) (map[string]string, string) {
	raw, err := os.ReadFile(a.workflowPath(p, name))
	if err != nil {
		return nil, "file not readable"
	}
	wf, err := plan.ParseWorkflow(string(raw))
	if err != nil {
		return nil, "front matter not readable"
	}
	return wf.Knobs, ""
}
