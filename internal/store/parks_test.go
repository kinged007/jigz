package store

import "testing"

func TestParkIssueIsExactlyOnce(t *testing.T) {
	s := open(t)
	p := IssuePark{Project: "o/a", Workflow: "build", IssueID: "7", Class: "agent-unauthenticated", Fix: "log in", Reason: "Invalid API key", TS: "t"}
	if ok, err := s.ParkIssue(p); err != nil || !ok {
		t.Fatalf("ParkIssue = %v, %v, want true, nil", ok, err)
	}
	if ok, err := s.ParkIssue(p); err != nil || ok {
		t.Fatalf("second ParkIssue = %v, %v, want false, nil", ok, err)
	}
	got, found, err := s.ActivePark("o/a", "build", "7")
	if err != nil || !found || got.Class != p.Class || got.Fix != p.Fix {
		t.Fatalf("ActivePark = %+v, %v, %v, want the park", got, found, err)
	}
	if _, found, _ := s.ActivePark("o/a", "build", "8"); found {
		t.Error("another issue reports a park it does not have")
	}
}

func TestClearIssueParkIsIdempotent(t *testing.T) {
	s := open(t)
	if ok, err := s.ClearIssuePark("o/a", "build", "7"); err != nil || ok {
		t.Fatalf("clear of nothing = %v, %v, want false, nil", ok, err)
	}
	p := IssuePark{Project: "o/a", Workflow: "build", IssueID: "7", Class: "missing-model", Fix: "configure a valid model", TS: "t"}
	if _, err := s.ParkIssue(p); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClearIssuePark("o/a", "build", "7"); err != nil || !ok {
		t.Fatalf("clear = %v, %v, want true, nil", ok, err)
	}
	if ok, err := s.ClearIssuePark("o/a", "build", "7"); err != nil || ok {
		t.Fatalf("second clear = %v, %v, want false, nil", ok, err)
	}
	if _, found, _ := s.ActivePark("o/a", "build", "7"); found {
		t.Error("cleared park still reads as active")
	}
	// A failure after the restart parks again.
	if ok, err := s.ParkIssue(p); err != nil || !ok {
		t.Fatalf("re-park = %v, %v, want true, nil", ok, err)
	}
}

func TestClearIssueParksCoversEveryWorkflow(t *testing.T) {
	s := open(t)
	for _, w := range []string{"build", "plan"} {
		if _, err := s.ParkIssue(IssuePark{Project: "o/a", Workflow: w, IssueID: "7", Class: "c", TS: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.ClearIssueParks("o/a", "7")
	if err != nil || n != 2 {
		t.Fatalf("cleared = %d, %v, want 2, nil", n, err)
	}
	if parks, _ := s.ActiveParks(); len(parks) != 0 {
		t.Fatalf("active parks = %+v, want none", parks)
	}
}
