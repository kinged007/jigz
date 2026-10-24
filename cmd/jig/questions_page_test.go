package main

import (
	"strings"
	"testing"
)

func parkQuestion(t *testing.T, r *questionsRig, bodies ...string) int {
	t.Helper()
	n := r.parked(bodies...)
	r.parked("please review")
	location(t, r.post(t, "/questions/refresh", nil))
	return n
}

func questionAttrs(id, audience, blocking, resume string) string {
	return `<!-- agent-question id="` + id + `" audience="` + audience + `" blocking="` + blocking + `" resume="` + resume + `" -->
**Question:** Which colour for ` + id + `?

**Suggested answer:** Blue.
<!-- /agent-question -->`
}

func TestQuestionsPageFilters(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(
		questionAttrs("q-1", "product", "true", "agent:plan"),
		questionAttrs("q-2", "project", "false", "agent:plan"),
		questionAttrs("q-3", "product", "true", "agent:plan"),
	)
	r.parked("please review")
	location(t, r.post(t, "/questions/refresh", nil))
	for _, tc := range []struct {
		path   string
		want   []string
		absent []string
	}{
		{"/questions", []string{"Which colour for q-1?", "Which colour for q-2?", "Which colour for q-3?", "Blue.", "Use suggested answer"}, nil},
		{"/questions?audience=product", []string{"Which colour for q-1?", "Which colour for q-3?"}, []string{"Which colour for q-2?"}},
		{"/questions?audience=project", []string{"Which colour for q-2?"}, []string{"Which colour for q-1?", "Which colour for q-3?"}},
		{"/questions?blocking=true", []string{"Which colour for q-1?", "Which colour for q-3?"}, []string{"Which colour for q-2?"}},
		{"/questions?blocking=false", []string{"Which colour for q-2?"}, []string{"Which colour for q-1?", "Which colour for q-3?"}},
		{"/questions?project=" + qProject, []string{"Which colour for q-1?", "Which colour for q-2?", "Which colour for q-3?"}, nil},
		{"/questions?project=other/repo", nil, []string{"Which colour for q-1?", "Which colour for q-2?", "Which colour for q-3?"}},
		{"/questions?state=open", []string{"Which colour for q-1?", "Which colour for q-2?", "Which colour for q-3?"}, nil},
		{"/questions?state=answered", nil, []string{"Which colour for q-1?", "Which colour for q-2?", "Which colour for q-3?"}},
	} {
		body := getPage(t, r.a, tc.path)
		for _, w := range tc.want {
			if !strings.Contains(body, w) {
				t.Errorf("GET %s lacks %q", tc.path, w)
			}
		}
		for _, a := range tc.absent {
			if strings.Contains(body, a) {
				t.Errorf("GET %s shows %q", tc.path, a)
			}
		}
	}
	body := getPage(t, r.a, "/questions?audience=project&blocking=false&project="+qProject+"&state=open")
	for _, want := range []string{`<option value="project" selected>`, `<option value="false" selected>`, `value="acme/app"`, `<option value="open" selected>`} {
		if !strings.Contains(body, want) {
			t.Errorf("filter form lacks %q", want)
		}
	}
	// Answering q-1 keeps the labels: q-3 is still blocking and open. The
	// refresh then stores q-1 answered next to q-2 and q-3 open.
	location(t, r.post(t, "/questions/answer", answerForm(n, "q-1", "Green.", r.token(actAnswer, n))))
	location(t, r.post(t, "/questions/refresh", nil))
	if body := getPage(t, r.a, "/questions?state=answered"); !strings.Contains(body, "Which colour for q-1?") || strings.Contains(body, "Which colour for q-2?") || strings.Contains(body, "Which colour for q-3?") {
		t.Error("answered state does not show only q-1")
	}
	if body := getPage(t, r.a, "/questions?state=open"); !strings.Contains(body, "Which colour for q-2?") || !strings.Contains(body, "Which colour for q-3?") || strings.Contains(body, "Which colour for q-1?") {
		t.Error("open state does not show only q-2 and q-3")
	}
}

func TestQuestionsPageNotice(t *testing.T) {
	r := newQuestionsRig(t)
	parkQuestion(t, r, question("q-1", "agent:plan"))
	body := getPage(t, r.a, "/questions?msg=hello+there")
	if !strings.Contains(body, "hello there") {
		t.Errorf("page lacks the notice: %s", body)
	}
}

func TestQuestionsPageEnrichOff(t *testing.T) {
	r := newQuestionsRig(t)
	r.a.github.Enrich = false
	body := getPage(t, r.a, "/questions")
	if !strings.Contains(body, "Set github.enrich to see questions") {
		t.Errorf("page lacks the enrich message: %s", body)
	}
}

func TestQuestionsPageEmpty(t *testing.T) {
	r := newQuestionsRig(t)
	r.parked("please review")
	location(t, r.post(t, "/questions/refresh", nil))
	body := getPage(t, r.a, "/questions")
	if !strings.Contains(body, "No questions match these filters.") {
		t.Errorf("page lacks the empty state: %s", body)
	}
}

func TestQuestionsPageAskedCount(t *testing.T) {
	r := newQuestionsRig(t)
	parkQuestion(t, r, question("q-1", "agent:plan"))
	if body := getPage(t, r.a, "/questions"); strings.Contains(body, "times</span>") {
		t.Error("a question asked once shows an asked count")
	}
	r2 := newQuestionsRig(t)
	parkQuestion(t, r2, question("q-1", "agent:plan"), question("q-1", "agent:plan"))
	if body := getPage(t, r2.a, "/questions"); !strings.Contains(body, "asked 2 times") {
		t.Errorf("page lacks the asked count: %s", body)
	}
}

func TestQuestionsPageAnswered(t *testing.T) {
	r := newQuestionsRig(t)
	// q-2 stays open and blocking, so the item keeps needs-human and is
	// scanned again after the answer.
	n := parkQuestion(t, r, question("q-1", "agent:plan"), question("q-2", "agent:plan"))
	if body := getPage(t, r.a, "/questions"); strings.Contains(body, "Answer by") {
		t.Error("an open question shows an answer")
	}
	location(t, r.post(t, "/questions/answer", answerForm(n, "q-1", "Green.", r.token(actAnswer, n))))
	location(t, r.post(t, "/questions/refresh", nil))
	if body := getPage(t, r.a, "/questions?state=answered"); !strings.Contains(body, "<b>Answer by bot:</b> Green.") {
		t.Errorf("answered question lacks the answer and its author: %s", body)
	}
}
