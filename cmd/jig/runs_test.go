package main

import (
	"net/url"
	"testing"
)

func TestParseRunQueryHonorsIssue(t *testing.T) {
	q := parseRunQuery(url.Values{"project": {"acme/app"}, "workflow": {"build"}, "issue": {"21"}})
	if q.Issue != "21" {
		t.Fatalf("issue = %q, want 21", q.Issue)
	}
	if f := q.filter(); f.Q != "21" {
		t.Errorf("filter Q = %q, want 21 so the park alert link filters the runs page", f.Q)
	}
	if v := q.values(); v.Get("issue") != "21" {
		t.Errorf("values issue = %q, want 21 so paging keeps the filter", v.Get("issue"))
	}
	if !q.Filtered() {
		t.Error("Filtered = false, want true when only issue is set")
	}
}
