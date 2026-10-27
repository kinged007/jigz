package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/fakegithub"
	"github.com/kinged007/jigz/internal/store"
)

var scriptRe = regexp.MustCompile(`(?s)<script>.*?</script>`)

func TestQuestionsPageHidesMarkers(t *testing.T) {
	r := newQuestionsRig(t)
	parkQuestion(t, r, question("q-1", "agent:plan"))
	visible := scriptRe.ReplaceAllString(getPage(t, r.a, "/questions"), "")
	if strings.Contains(visible, "agent-answer") {
		t.Error("the answer marker is visible outside the script")
	}
}

func TestQuestionsProjectFilterOneProject(t *testing.T) {
	r := newQuestionsRig(t)
	parkQuestion(t, r, question("q-1", "agent:plan"))
	body := getPage(t, r.a, "/questions?project=other/repo")
	if strings.Contains(body, `name="project"`) {
		t.Error("a single project has a dropdown")
	}
	if !strings.Contains(body, "<b>acme/app</b>") || !strings.Contains(body, "Which colour for q-1?") {
		t.Errorf("the only project is not applied: %s", body)
	}
}

func TestQuestionsProjectFilterSeveralProjects(t *testing.T) {
	r := newQuestionsRig(t)
	parkQuestion(t, r, question("q-1", "agent:plan"))
	r.a.questions.Targets = func() []collector.Target {
		return []collector.Target{{Project: "zeta/lib", Token: "tok"}, {Project: qProject, Token: "tok"}}
	}
	body := getPage(t, r.a, "/questions?project=zeta/lib")
	for _, want := range []string{
		`<select name="project">`, `<option value="acme/app" >acme/app</option>`, `<option value="zeta/lib" selected>zeta/lib</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Which colour for q-1?") {
		t.Error("the project filter did not apply")
	}
	if !strings.Contains(body, `class="btn reset"`) {
		t.Error("Reset is hidden with a project filter")
	}
	if body := getPage(t, r.a, "/questions?project=other/repo"); !strings.Contains(body, "Which colour for q-1?") {
		t.Error("an unknown project filter was applied")
	}
}

func TestQuestionsOpenLinkForDescription(t *testing.T) {
	r := newQuestionsRig(t)
	r.fake.Add(fakegithub.Item{
		Repo: qProject, Title: "Pick", Labels: []string{"needs-human"}, Author: "bot", Association: "OWNER",
		Body: question("q-9", "agent:plan"),
	})
	r.pass()
	body := getPage(t, r.a, "/questions")
	for _, want := range []string{
		`<a class="btn" href="https://github.com/acme/app/issues/1" target="_blank" rel="noopener noreferrer">Open on GitHub</a>`,
		`>description</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "#issuecomment-") {
		t.Error("a description question links to a comment")
	}
}

func TestQuestionsNoticesOnlyWhenWrong(t *testing.T) {
	r := newQuestionsRig(t)
	parkQuestion(t, r, question("q-1", "agent:plan"))
	if body := getPage(t, r.a, "/questions"); strings.Contains(body, `class="banner`) || strings.Contains(body, "last pass") {
		t.Errorf("a healthy scanner shows a notice: %s", body)
	}
}

func TestTrackerNameAndOpenURL(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/o/r/issues/1":      "GitHub",
		"https://git.github.example.com/o/r/1": "GitHub",
		"https://gitlab.com/o/r/-/issues/1":    "GitLab",
		"https://tracker.example.com/o/r/1":    "issue tracker",
		"":                                     "issue tracker",
	} {
		if got := trackerName(url); got != want {
			t.Errorf("trackerName(%q) = %q, want %q", url, got, want)
		}
	}
	item := "https://github.com/o/r/issues/1"
	for _, tc := range []struct {
		name string
		q    store.Question
		want string
	}{
		{"description", store.Question{AskedCommentURL: item}, item},
		{"comment url as returned", store.Question{AskedCommentID: 7, AskedCommentURL: item + "#issuecomment-7"}, item + "#issuecomment-7"},
		{"anchor added", store.Question{AskedCommentID: 7, AskedCommentURL: item}, item + "#issuecomment-7"},
		{"no comment url", store.Question{AskedCommentID: 7}, item + "#issuecomment-7"},
	} {
		if got := openURL(tc.q, item); got != tc.want {
			t.Errorf("%s: openURL = %q, want %q", tc.name, got, tc.want)
		}
	}
}
