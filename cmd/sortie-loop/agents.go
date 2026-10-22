package main

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	fleetconfig "github.com/kinged007/sortie-loop/internal/fleetconfig"
	"github.com/kinged007/sortie-loop/internal/machine"
	"github.com/kinged007/sortie-loop/internal/plan"
)

// agentColumn is one shown knob. The editors list the rest.
type agentColumn struct{ Key, Label string }

var agentColumns = []agentColumn{
	{"kind", "kind"}, {"command", "command"}, {"model", "model"}, {"max_turns", "max_turns"},
	{"max_concurrent_agents", "concurrency"}, {"turn_timeout_ms", "turn timeout"},
	{"max_retry_backoff_ms", "backoff max"}, {"polling_interval_ms", "poll every"},
}

// agentCell is the value in force and where it comes from. File is the
// workflow file's own value when central sets a different one.
type agentCell struct{ Value, Origin, File string }

type agentRow struct {
	Label, EditURL, Note string
	Group                bool
	Cells                []agentCell
}

type agentsView struct {
	pageData
	Columns []agentColumn
	Span    int
	Rows    []agentRow
	Machine machineView
}

type poolRow struct {
	Name, Runtime, Command, CostClass, Hours string
	State, Class, Reason, LastUsed           string
}

// machineView is the machine file as the page shows it. Path is set and
// Present false when there is no file or it cannot be read.
type machineView struct {
	Path, Err  string
	Present    bool
	Warnings   []string
	Timezone   string
	Windows    []string
	End        string
	OpenNow    bool
	NextChange string
	Pool       []poolRow
}

func (a *app) agents(w http.ResponseWriter, r *http.Request) {
	v := agentsView{pageData: a.pageView(r, "agents", "Agents"), Columns: agentColumns}
	v.Span = len(agentColumns) + 2
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
			row := agentRow{Label: n, EditURL: "/edit/workflow/" + slug + "/" + n, Note: note}
			for _, col := range agentColumns {
				row.Cells = append(row.Cells, agentCellFor(file[col.Key]))
			}
			v.Rows = append(v.Rows, row)
		}
	}
	v.Machine = loadMachineView(time.Now())
	execPage(w, "agents", v)
}

func loadMachineView(now time.Time) machineView {
	var mv machineView
	path, err := machine.DefaultPath()
	if err != nil {
		mv.Err = err.Error()
		return mv
	}
	mv.Path = path
	if _, err := os.Stat(path); err != nil {
		return mv
	}
	cfg, err := machine.Load(path)
	if err != nil {
		mv.Err = err.Error()
		return mv
	}
	var state machine.State
	if sp, err := machine.StatePath(); err == nil {
		if state, err = machine.LoadState(sp); err != nil {
			mv.Err = err.Error()
		}
	}
	mv.Present, mv.Warnings = true, cfg.Warnings
	mv.Timezone = cfg.Hours.Timezone
	if mv.Timezone == "" {
		mv.Timezone = "local time"
	}
	mv.Windows = windowTexts(cfg.Hours)
	mv.End = cfg.Hours.End
	if mv.End == "" {
		mv.End = machine.EndFinish
	}
	mv.OpenNow = cfg.Hours.Open(now)
	mv.NextChange = "—"
	if next := cfg.NextChange(state, now); !next.IsZero() {
		mv.NextChange = localText(next)
	}
	for _, e := range cfg.Pool {
		row := poolRow{Name: e.Name, Runtime: e.Runtime, Command: e.Command, CostClass: e.CostClass,
			Hours: "always", State: "available", Class: "running"}
		if row.Command == "" {
			row.Command = e.Runtime
			if e.Runtime == "claude-code" {
				row.Command = "claude"
			}
		}
		if w := windowTexts(e.Hours); len(w) > 0 {
			row.Hours = strings.Join(w, "; ")
		}
		es := state[e.Name]
		switch {
		case now.Before(es.ExhaustedUntil):
			row.State, row.Class = "exhausted until "+localText(es.ExhaustedUntil), "failed"
			row.Reason = es.Reason
		case !e.Hours.Open(now):
			row.State, row.Class = "outside its hours", "paused"
		}
		if !es.LastUsed.IsZero() {
			row.LastUsed = agoText(es.LastUsed)
		}
		mv.Pool = append(mv.Pool, row)
	}
	return mv
}

func windowTexts(h machine.Hours) []string {
	var out []string
	for _, w := range h.Windows {
		out = append(out, strings.Join(w.Days, ",")+" "+w.Start+"-"+w.End)
	}
	return out
}

// agentCellFor turns a workflow file's knob value into a cell. The value
// comes from the file alone: sortie-loop has no fleet cascade, so there
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
