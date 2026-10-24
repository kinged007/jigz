package fakegithub

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/config"
)

func TestParseQueryShippedFilters(t *testing.T) {
	for _, loop := range []string{"issues", "prs"} {
		q := "repo:o/r type:issue state:open " + config.DefaultLabelFilter(loop) + ` milestone:"v 2" assignee:@me`
		parsed, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("%s: %v", loop, err)
		}
		var labels []string
		for _, tm := range parsed.terms {
			if tm.key == "label" && !tm.neg {
				labels = tm.values
			}
		}
		want := strings.Split(strings.TrimPrefix(strings.Fields(config.DefaultLabelFilter(loop))[0], "label:"), ",")
		if !reflect.DeepEqual(labels, want) {
			t.Errorf("%s: label values %q, want %q", loop, labels, want)
		}
	}
}

func TestParseQueryQuoting(t *testing.T) {
	q, err := ParseQuery(`label:"a b",c -label:"x:y z"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []term{
		{key: "label", values: []string{"a b", "c"}},
		{neg: true, key: "label", values: []string{"x:y z"}},
	}
	if !reflect.DeepEqual(q.terms, want) {
		t.Errorf("terms = %+v, want %+v", q.terms, want)
	}
}

func TestParseQueryRejects(t *testing.T) {
	for _, q := range []string{
		"label:a draft",
		"label:a no:assignee",
		"label:a sort:updated",
		"type:commit",
		"state:merged",
		`label:"open`,
	} {
		if _, err := ParseQuery(q); err == nil {
			t.Errorf("ParseQuery(%q) accepted an unsupported query", q)
		}
	}
}

func TestMatch(t *testing.T) {
	it := &Item{
		Repo: "o/r", State: "open", Labels: []string{"Agent:Plan", "in-progress", "a b"},
		Assignees: []string{"me"}, Milestone: "v2",
	}
	cases := []struct {
		q    string
		want bool
	}{
		{"repo:O/R type:issue state:open", true},
		{"type:pr", false},
		{"state:closed", false},
		{"label:agent:plan,agent:build", true},
		{"label:agent:build,agent:docs", false},
		{"label:agent:plan label:in-progress", true},
		{"label:agent:plan label:agent:build", false},
		{"-label:needs-human", true},
		{"-label:in-progress", false},
		{`label:"a b"`, true},
		{"assignee:@me", true},
		{"assignee:other", false},
		{"-assignee:other", true},
		{`milestone:"v2"`, true},
		{`milestone:"v3"`, false},
	}
	for _, c := range cases {
		q, err := ParseQuery(c.q)
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if got := q.Match(it, "me"); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.q, got, c.want)
		}
	}
}

func TestMatchSeveralRepoTermsAreOr(t *testing.T) {
	it := &Item{Repo: "o/a", State: "open", Labels: []string{"x"}}
	cases := []struct {
		q    string
		want bool
	}{
		{"repo:o/a repo:o/b", true},
		{"repo:o/b repo:O/A", true},
		{"repo:o/b repo:o/c", false},
		{"repo:o/b,o/a repo:o/c", true},
		{"repo:o/b repo:o/a label:x", true},
		{"repo:o/b repo:o/a label:y", false},
		{"repo:o/a repo:o/b -repo:o/a", false},
		{"repo:o/b repo:o/c -repo:o/c", false},
		{"state:open repo:o/a repo:o/b", true},
	}
	for _, c := range cases {
		q, err := ParseQuery(c.q)
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if got := q.Match(it, "me"); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.q, got, c.want)
		}
	}
	if _, err := ParseQuery("repo:o/a repo:o/b draft:true"); err == nil {
		t.Error("unknown qualifier accepted next to several repo terms")
	}
}
