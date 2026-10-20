package main

import (
	"errors"
	"net/http"
	"os"

	"github.com/kinged007/sortie-loop/internal/loopsrc"
)

// logView is one workflow's child log, as the page renders it.
type logView struct {
	pageData
	Project, Workflow string
	Lines             []string
	// Why says why there is no tail to show.
	Why string
}

// loopLog renders the tail of a workflow's child log. The log belongs to
// the workflow, not to one run, so it is reachable while the loop is up.
func (a *app) loopLog(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("owner") + "/" + r.PathValue("name")
	workflow := r.PathValue("workflow")
	v := logView{pageData: a.pageView(r, "workflows", "Log: "+workflow), Project: project, Workflow: workflow}
	op, ok := a.src().(loopsrc.Operator)
	switch {
	case !ok || !op.ProjectCapabilities(project).Logs:
		v.Why = "this project's loops write no status document, so there is no log to read"
	default:
		lines, err := op.LogTail(project, workflow, logLines)
		switch {
		case errors.Is(err, os.ErrNotExist):
			v.Why = "the log file does not exist yet"
		case err != nil:
			v.Why = err.Error()
		default:
			v.Lines = lines
		}
	}
	execPage(w, "logs", v)
}
