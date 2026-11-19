package main

import (
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/rollup"
)

func writeChildDoc(t *testing.T, root string, w workflowStatus) {
	t.Helper()
	w.Name, w.WorkflowFile, w.PID = "plan", "WORKFLOW.plan.md", 4242
	if err := writeStatus(statusDoc{SchemaVersion: statusSchemaVersion, Repo: root, State: "running", Workflows: []workflowStatus{w}}); err != nil {
		t.Fatal(err)
	}
}

// A child whose engine has no server must not read as idle while it has
// work: the daemon would replace its engine mid-run.
func TestChildQuietReadsTheStatusDocument(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	session := rollup.Session{IssueID: "7", Identifier: "o/p#7", Attempt: 1, StartedAt: time.Now()}
	for _, c := range []struct {
		name        string
		w           workflowStatus
		idle, known bool
	}{
		{"nothing running", workflowStatus{State: "running", SessionsKnown: true}, true, true},
		{"a session", workflowStatus{State: "running", SessionsKnown: true, Sessions: []rollup.Session{session}}, false, true},
		{"sessions not accounted for", workflowStatus{State: "running"}, false, false},
		{"a stopping engine with a session", workflowStatus{State: "stopping", SessionsKnown: true, Sessions: []rollup.Session{session}}, false, true},
		{"an engine that is down", workflowStatus{State: "sleeping"}, true, true},
	} {
		writeChildDoc(t, root, c.w)
		if idle, known := childQuiet(root); idle != c.idle || known != c.known {
			t.Errorf("%s: idle=%v known=%v, want idle=%v known=%v", c.name, idle, known, c.idle, c.known)
		}
	}
	writeRetry(t, root)
	writeChildDoc(t, root, workflowStatus{State: "running", SessionsKnown: true})
	if idle, known := childQuiet(root); idle || !known {
		t.Errorf("a pending retry: idle=%v known=%v, want busy", idle, known)
	}
	if idle, known := childQuiet(t.TempDir()); idle || known {
		t.Errorf("no status document: idle=%v known=%v, want both false", idle, known)
	}
}
