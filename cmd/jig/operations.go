package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/loopsrc"
	"github.com/kinged007/jigz/internal/rollup"
)

// opsLoop is one loop row under its project.
type opsLoop struct {
	L       *rollup.Loop
	Figures loopFigures
	State   string
	// Class is the chip class for State.
	Class string
	// Hint is the restart count and last exit code from the status
	// document, empty when there is nothing to report.
	Hint string
	// Budget is this period's spend against the workflow's cap, and
	// BudgetPct the width of its bar. Empty when the workflow sets none.
	Budget    string
	BudgetPct int
	// Paused marks a workflow whose budget holds it, and PausedBy why.
	Paused   bool
	PausedBy string
	// OfflineReason is why the machine gate, capability detection or the
	// work entry keeps this loop down; ETA its estimated start. Runtime
	// is the agent runtime the machine gate selected and Agent the agent
	// the work entry selected. Primary is the first candidate and
	// FallbackReason the trigger of the standby switch, set while the
	// loop runs a non-primary agent.
	OfflineReason  string
	ETA            string
	Runtime        string
	Agent          string
	Primary        string
	FallbackReason string
}

// cleanupView is a project's last cleanup run.
type cleanupView struct {
	Ok       bool
	DryRun   bool
	At, Ago  string
	ExitCode int
	Paths    int
}

// opsProject is one project row of the Operations table with its loops.
// The sleep and cleanup fields are set only when the project's status
// document says the loop supports them.
type opsProject struct {
	repoView
	Loops    []opsLoop
	Retrying int
	Status   *loopsrc.StatusInfo
	// Sleep is "sleeping", "awake" or empty when sleep is not enabled.
	Sleep     string
	SleepNote string
	CanWake   bool
	// Cleanup is the last cleanup run, set only when the project supports
	// cleanup and has a record. CanCleanup is whether the dry-run button
	// is offered.
	Cleanup    *cleanupView
	CanCleanup bool
}

// agoText renders how long ago a time was.
func agoText(t time.Time) string {
	return since(t.UTC().Format(time.RFC3339)) + " ago"
}

func localText(t time.Time) string { return at(t.UTC().Format(time.RFC3339)) }

// loopHint describes a workflow's restarts and last exit for a loop row.
func loopHint(w loopsrc.WorkflowInfo) string {
	var parts []string
	if w.RestartCount > 0 {
		parts = append(parts, fmt.Sprintf("restarted %d time%s", w.RestartCount, pluralS(w.RestartCount)))
	}
	if w.LastExitCode != nil && *w.LastExitCode != 0 {
		parts = append(parts, fmt.Sprintf("last exit code %d", *w.LastExitCode))
	}
	return strings.Join(parts, ", ")
}

func restartNote(w loopsrc.WorkflowInfo) string {
	if w.RestartCount == 0 {
		return ""
	}
	return fmt.Sprintf(", restarted %d time%s", w.RestartCount, pluralS(w.RestartCount))
}

// workflowChip maps a status-document workflow state to the text and
// chip class of a loop row. A child that exited with a non-zero code is
// failed; one that exited with code 0 or no code is only exited.
func workflowChip(w loopsrc.WorkflowInfo) (state, class string) {
	switch {
	case w.Failed():
		return "failed", "failed"
	case w.State == "exited":
		return "exited", "stopped"
	case w.State == "stopping" || w.State == "draining":
		return w.State, "draining"
	}
	return w.State, w.State
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// loopRow decides what one loop shows as its state. A sleeping project's
// loops are sleeping, not stopped.
func loopRow(l *rollup.Loop, p *opsProject) opsLoop {
	row := opsLoop{L: l, State: "stopped", Class: "stopped"}
	w, hasDoc := loopsrc.WorkflowInfo{}, false
	if p.Status != nil {
		w, hasDoc = p.Status.Workflow(l.Loop)
	}
	switch {
	case p.Status != nil && p.Status.Sleeping:
		row.State, row.Class = "sleeping", "sleeping"
	case l.Live:
		row.State, row.Class = "running", "running"
	case hasDoc && p.Status.Liveness != loopsrc.Stale && w.State != "":
		row.State, row.Class = workflowChip(w)
	}
	if p.Status != nil && p.Status.Gate != nil {
		row.Runtime = p.Status.Gate.Runtime
	}
	if hasDoc {
		row.OfflineReason = w.OfflineReason
		row.Agent = w.Agent
		row.Primary = w.Primary
		row.FallbackReason = w.FallbackReason
		if w.ETA != nil {
			row.ETA = localText(*w.ETA)
		}
		row.Hint = loopHint(w)
		if b := w.Budget; b != nil {
			row.Paused, row.PausedBy = b.Paused, b.PausedBy
			if b.Cap > 0 {
				row.Budget = commas(b.TokensUsed) + " / " + commas(b.Cap) + " tokens"
				row.BudgetPct = int(float64(b.TokensUsed) / float64(b.Cap) * 100)
				if row.BudgetPct > 100 {
					row.BudgetPct = 100
				}
			}
		}
	}
	return row
}

// opsProjects groups the sorted loops under the sorted projects. Sleep,
// wake and cleanup details come from the loop source when it is an
// Operator and the project reports the capability.
func (a *app) opsProjects(d pageData) []opsProject {
	op, _ := a.src().(loopsrc.Operator)
	var out []opsProject
	for _, r := range d.Repos {
		p := opsProject{repoView: r}
		if op != nil {
			if st, err := op.Status(r.Slug); err == nil {
				p.Status = &st
				p.applyStatus(op.ProjectCapabilities(r.Slug))
			}
		}
		for i := range d.Fleet.Loops {
			if l := &d.Fleet.Loops[i]; l.Repo == r.Slug {
				row := loopRow(l, &p)
				row.Figures = d.Figures(*l)
				p.Loops = append(p.Loops, row)
				p.Retrying += l.Retrying
			}
		}
		out = append(out, p)
	}
	return out
}

// applyStatus fills the sleep and cleanup fields the capabilities allow.
func (p *opsProject) applyStatus(c loopsrc.Capabilities) {
	st := p.Status
	if c.Sleep && st.Sleep.Enabled {
		switch {
		case st.Sleeping:
			p.Sleep = "sleeping"
			p.SleepNote = "since " + sinceOrDash(st.Sleep.SleepingSince)
			if st.Sleep.NextWake != nil {
				p.SleepNote += ", next wake " + localText(*st.Sleep.NextWake)
			}
		case st.Liveness == loopsrc.Live:
			p.Sleep = "awake"
		}
	}
	p.CanWake = c.Wake && st.Sleeping
	p.CanCleanup = c.Cleanup
	if c.Cleanup && st.Cleanup != nil {
		cl := st.Cleanup
		p.Cleanup = &cleanupView{
			Ok: cl.ExitCode == 0, DryRun: cl.DryRun, At: localText(cl.LastRunAt), Ago: agoText(cl.LastRunAt),
			ExitCode: cl.ExitCode, Paths: len(cl.Paths),
		}
	}
}

func sinceOrDash(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return localText(*t)
}

// AnyStatus reports whether any project has a status document, which
// decides whether the Last cleanup column and the capability banner show.
func (v opsView) AnyStatus() bool {
	for _, p := range v.Projects {
		if p.Status != nil {
			return true
		}
	}
	return false
}

// AnyCleanup reports whether any project supports cleanup.
func (v opsView) AnyCleanup() bool {
	for _, p := range v.Projects {
		if p.CanCleanup {
			return true
		}
	}
	return false
}
