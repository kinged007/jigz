package main

import (
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/store"
)

func TestParkAlertsNameTheClassAndFix(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.ParkIssue(store.IssuePark{
		Project: "acme/app", Workflow: "build", IssueID: "21",
		Class: "agent-unauthenticated", Fix: "log the agent back in (run /login)", TS: "t",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ParkIssue(store.IssuePark{
		Project: "other/repo", Workflow: "build", IssueID: "3",
		Class: "missing-model", Fix: "configure a valid model", TS: "t",
	}); err != nil {
		t.Fatal(err)
	}
	got := parkAlerts(st, map[string]bool{"acme/app": true})
	if len(got) != 1 {
		t.Fatalf("alerts = %+v, want one for the known project only", got)
	}
	a := got[0]
	if a.Sev != "err" || a.Kind != "Non-retryable failure" {
		t.Errorf("alert = %+v, want an err Non-retryable failure", a)
	}
	for _, want := range []string{"acme/app/build#21", "agent-unauthenticated", "log the agent back in (run /login)"} {
		if !strings.Contains(a.Msg, want) {
			t.Errorf("alert message = %q, want it to contain %q", a.Msg, want)
		}
	}
	if !strings.Contains(a.Href, "/runs?") || !strings.Contains(a.Href, "issue=21") {
		t.Errorf("alert href = %q, want the runs page for issue 21", a.Href)
	}
}

func TestParkAlertsAreEmptyWithoutParks(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := parkAlerts(st, map[string]bool{"acme/app": true}); len(got) != 0 {
		t.Errorf("alerts = %+v, want none", got)
	}
}
