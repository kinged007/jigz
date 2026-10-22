package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// budgetCmdWait is how long the pause, resume and budget override commands
// wait for the supervisor to report the change.
var budgetCmdWait = 5 * time.Second

type budgetCmdResult struct {
	Workflow string `json:"workflow"`
	Changed  bool   `json:"changed"`
	Paused   bool   `json:"paused"`
	Override bool   `json:"override"`
	Error    string `json:"error,omitempty"`
}

func findWorkflow(doc statusDoc, name string) (workflowStatus, bool) {
	for _, w := range doc.Workflows {
		if w.Name == name {
			return w, true
		}
	}
	return workflowStatus{}, false
}

func budgetPaused(w workflowStatus) bool { return w.State == "paused" }

func budgetOverridden(w workflowStatus) bool {
	return w.Budget != nil && w.Budget.Override && !(w.Budget.Paused && w.Budget.PausedBy == "budget")
}

// controlWorkflow sends a pause, resume or override request to the
// supervisor of root and waits for its status document to show the change.
// A workflow that is already in the requested state is left alone.
func controlWorkflow(root, action, name string) (budgetCmdResult, error) {
	res := budgetCmdResult{Workflow: name}
	if _, err := runningEntry(root); err != nil {
		return res, err
	}
	doc, err := readStatusDoc(root)
	if err != nil {
		return res, fmt.Errorf("status document: %w", err)
	}
	w, ok := findWorkflow(doc, name)
	if !ok {
		names := make([]string, 0, len(doc.Workflows))
		for _, x := range doc.Workflows {
			names = append(names, x.Name)
		}
		return res, fmt.Errorf("no workflow %q (have: %s)", name, strings.Join(names, ", "))
	}
	fill := func(w workflowStatus) {
		res.Paused, res.Override = budgetPaused(w), w.Budget != nil && w.Budget.Override
	}
	fill(w)
	var done func(workflowStatus) bool
	switch action {
	case "pause":
		done = func(w workflowStatus) bool {
			return budgetPaused(w) && (w.Budget == nil || w.Budget.PausedBy != "budget")
		}
	case "resume":
		done = func(w workflowStatus) bool { return !budgetPaused(w) }
	case "override":
		if w.Budget == nil || w.Budget.Cap == 0 {
			return res, fmt.Errorf("workflow %q has no budget (set workflows.%s.budget in .sortie/config.yaml)", name, name)
		}
		done = budgetOverridden
	default:
		return res, fmt.Errorf("unknown action %q", action)
	}
	if done(w) {
		return res, nil
	}
	if err := requestBudgetAction(root, action, name); err != nil {
		return res, err
	}
	for deadline := time.Now().Add(budgetCmdWait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if d, err := readStatusDoc(root); err == nil {
			if w, ok := findWorkflow(d, name); ok && done(w) {
				res.Changed = true
				fill(w)
				return res, nil
			}
		}
	}
	return res, fmt.Errorf("the supervisor did not apply %s within %s", action, budgetCmdWait)
}

// runBudgetCmd runs `pause <workflow>`, `resume <workflow>` and
// `budget override <workflow>` against the Jigz serving a repo-root.
func runBudgetCmd(cmd string, args []string) {
	action, label := cmd, cmd
	if cmd == "budget" {
		label = "budget override"
		if len(args) == 0 || args[0] != "override" {
			fatal(fmt.Errorf("usage: jig budget override <workflow> [--json] [repo-root]"))
		}
		action, args = "override", args[1:]
	}
	var bare []string
	asJSON := false
	for i, a := range args {
		switch {
		case a == "--json":
			asJSON = true
		case a == "--repo":
			continue
		case i > 0 && args[i-1] == "--repo":
			continue
		case strings.HasPrefix(a, "-"):
			fatal(fmt.Errorf("unknown flag %q", a))
		default:
			bare = append(bare, a)
		}
	}
	if len(bare) == 0 || len(bare) > 2 {
		fatal(fmt.Errorf("usage: jig %s <workflow> [--json] [repo-root]", label))
	}
	dir := "."
	if len(bare) == 2 {
		dir = bare[1]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fatal(err)
	}
	res, err := controlWorkflow(abs, action, bare[0])
	if err != nil {
		res.Error = err.Error()
	}
	if asJSON {
		out, _ := json.Marshal(res)
		fmt.Println(string(out))
	}
	if err != nil {
		fatal(err)
	}
	if !asJSON {
		switch {
		case !res.Changed:
			fmt.Printf("jig: %s: %s: nothing to do\n", abs, bare[0])
		case action == "pause":
			fmt.Printf("jig: %s: %s paused\n", abs, bare[0])
		case action == "resume":
			fmt.Printf("jig: %s: %s resumed\n", abs, bare[0])
		default:
			fmt.Printf("jig: %s: %s budget overridden\n", abs, bare[0])
		}
	}
}
