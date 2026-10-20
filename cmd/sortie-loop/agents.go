package main

import (
	"net/http"
	"os"
	"sort"

	fleetconfig "github.com/kinged007/sortie-loop/internal/fleetconfig"
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
	execPage(w, "agents", v)
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
