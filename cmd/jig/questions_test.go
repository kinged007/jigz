package main

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/fakegithub"
	fleetconfig "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/store"
)

const qProject = "acme/app"

type questionsRig struct {
	a    *app
	fake *fakegithub.Server
}

func newQuestionsRig(t *testing.T) *questionsRig {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &questionsRig{fake: fakegithub.New("bot", "tok")}
	r.fake.Logf = t.Logf
	r.fake.AddRepo(qProject, "agent:plan", "agent:build", "agent:done", "needs-human")
	srv := httptest.NewServer(r.fake)
	t.Cleanup(srv.Close)
	r.a = &app{sizes: newRepoSizes(), stateDir: t.TempDir(), store: st,
		github: fleetconfig.GitHub{APIURL: srv.URL}}
	r.a.questions = r.a.newScanner(st)
	r.a.questions.Targets = func() []collector.Target { return []collector.Target{{Project: qProject, Token: "tok"}} }
	t.Cleanup(func() {
		if u := r.fake.Unexpected(); len(u) > 0 {
			t.Errorf("unexpected requests: %v", u)
		}
	})
	return r
}

func (r *questionsRig) pass() { r.a.questions.Pass(context.Background()) }

// parked seeds an item with needs-human and agent:done and comments from
// the token account.
func (r *questionsRig) parked(bodies ...string) int {
	n := r.fake.Add(fakegithub.Item{Repo: qProject, Title: "Pick a colour", Labels: []string{"needs-human", "agent:done"}})
	for _, b := range bodies {
		r.fake.AddComment(qProject, n, fakegithub.Comment{Author: "bot", Association: "OWNER", Body: b})
	}
	return n
}

// parkQuestion seeds a parked item with the bodies and a second parked
// item without questions, then runs a pass.
func parkQuestion(t *testing.T, r *questionsRig, bodies ...string) int {
	t.Helper()
	n := r.parked(bodies...)
	r.parked("please review")
	r.pass()
	return n
}

func question(id, resume string) string {
	return questionAttrs(id, "product", "true", resume)
}

func questionAttrs(id, audience, blocking, resume string) string {
	return `<!-- agent-question id="` + id + `" audience="` + audience + `" blocking="` + blocking + `" resume="` + resume + `" -->
**Question:** Which colour for ` + id + `?

**Suggested answer:** Blue.
<!-- /agent-question -->`
}

func answer(id, text string) string {
	return `<!-- agent-answer id="` + id + `" -->
` + text + `
<!-- /agent-answer -->`
}

// send sends a request and returns the response.
func (r *questionsRig) send(method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	req.Host = "127.0.0.1"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.a.routes(newHostGuard("127.0.0.1")).ServeHTTP(rec, req)
	return rec
}

func (r *questionsRig) audit(t *testing.T) []store.AuditEntry {
	t.Helper()
	rows, err := r.a.store.Audit(50)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestWriteRoutesAreGone(t *testing.T) {
	r := newQuestionsRig(t)
	parkQuestion(t, r, question("q-1", "agent:plan"))
	for _, path := range []string{"/questions/answer", "/questions/retry", "/questions/remove-labels", "/questions/refresh", "/questions/other"} {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			if rec := r.send(method, path); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", method, path, rec.Code)
			}
		}
	}
	if rec := r.send(http.MethodPost, "/questions"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /questions = %d, want 405", rec.Code)
	}
	for _, q := range r.fake.Requests() {
		if q.Method != http.MethodGet {
			t.Errorf("request sent: %s %s", q.Method, q.Path)
		}
	}
}

func TestQuestionsPageIsReadOnly(t *testing.T) {
	r := newQuestionsRig(t)
	parkQuestion(t, r, question("q-1", "agent:plan"))
	raw := getPage(t, r.a, "/questions")
	for _, bad := range []string{`method="post"`, `method="POST"`, `name="token"`, "/questions/answer", "/questions/refresh"} {
		if strings.Contains(raw, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	body := html.UnescapeString(raw)
	for _, want := range []string{
		"<!-- agent-answer id=\"q-1\" -->\nBlue.\n<!-- /agent-answer -->",
		"Which colour for q-1?",
		`href="https://github.com/acme/app/issues/1"`,
		`href="https://github.com/acme/app/issues/1#issuecomment-1"`,
		`data-copy="answer-0"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestQuestionsPageFilters(t *testing.T) {
	r := newQuestionsRig(t)
	r.parked(
		questionAttrs("q-1", "product", "true", "agent:plan"),
		questionAttrs("q-2", "project", "false", "agent:plan"),
		questionAttrs("q-3", "product", "true", "agent:plan"),
		answer("q-3", "Green."),
	)
	r.pass()
	for _, tc := range []struct {
		path   string
		want   []string
		absent []string
	}{
		{"/questions", []string{"Which colour for q-1?", "Which colour for q-2?"}, []string{"Which colour for q-3?"}},
		{"/questions?audience=product", []string{"Which colour for q-1?"}, []string{"Which colour for q-2?"}},
		{"/questions?audience=project", []string{"Which colour for q-2?"}, []string{"Which colour for q-1?"}},
		{"/questions?blocking=true", []string{"Which colour for q-1?"}, []string{"Which colour for q-2?"}},
		{"/questions?blocking=false", []string{"Which colour for q-2?"}, []string{"Which colour for q-1?"}},
		{"/questions?project=" + qProject, []string{"Which colour for q-1?", "Which colour for q-2?"}, nil},
		{"/questions?project=other/repo", nil, []string{"Which colour for q-1?", "Which colour for q-2?"}},
		{"/questions?state=answered", []string{"Which colour for q-3?", "Answer by bot:"}, []string{"Which colour for q-1?", "Which colour for q-2?"}},
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
	if strings.Contains(getPage(t, r.a, "/questions?state=answered"), "agent-answer id") {
		t.Error("an answered question shows an answer template")
	}
}

func TestQuestionsOffHidesAndSkips(t *testing.T) {
	r := newQuestionsRig(t)
	r.parked(question("q-1", "agent:plan"))
	off := false
	r.a.github.Questions = &off
	r.pass()
	body := getPage(t, r.a, "/questions")
	if !strings.Contains(body, "Questions are off (github.questions: false)") || strings.Contains(body, "Which colour") {
		t.Errorf("page = %s", body)
	}
	var got struct {
		Enrich    bool  `json:"enrich"`
		Questions []any `json:"questions"`
	}
	if err := json.Unmarshal([]byte(getPage(t, r.a, "/api/v1/questions")), &got); err != nil || got.Enrich || len(got.Questions) != 0 {
		t.Errorf("api = %+v, %v", got, err)
	}
	if reqs := r.fake.Requests(); len(reqs) != 0 {
		t.Errorf("requests = %v", reqs)
	}
}

func TestQuestionsNoToken(t *testing.T) {
	r := newQuestionsRig(t)
	r.a.questions.Targets = func() []collector.Target { return []collector.Target{{Project: qProject}} }
	if body := getPage(t, r.a, "/questions"); !strings.Contains(body, "no GitHub token") {
		t.Errorf("page lacks the status before a pass: %s", body)
	}
	r.pass()
	if reqs := r.fake.Requests(); len(reqs) != 0 {
		t.Errorf("requests = %v", reqs)
	}
	if body := getPage(t, r.a, "/questions"); !strings.Contains(body, "no GitHub token") {
		t.Errorf("page lacks the status: %s", body)
	}
}

func TestQuestionsPageEmpty(t *testing.T) {
	r := newQuestionsRig(t)
	r.parked("please review")
	r.pass()
	if body := getPage(t, r.a, "/questions"); !strings.Contains(body, "No questions match these filters.") {
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

func TestQuestionsPageIgnoredAnswerAndProblems(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"), answer("q-7", "orphan"))
	r.fake.AddComment(qProject, n, fakegithub.Comment{Author: "mallory", Association: "NONE", Body: answer("q-1", "Red.")})
	r.pass()
	body := getPage(t, r.a, "/questions")
	for _, want := range []string{"ignored: mallory", "Items needing attention", `href="https://github.com/acme/app/issues/1#issuecomment-`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q: %s", want, body)
		}
	}
	if !strings.Contains(html.UnescapeString(body), `id="q-1" -->`) {
		t.Error("ignored answer closed the question")
	}
}

func TestQuestionsPageAnswered(t *testing.T) {
	r := newQuestionsRig(t)
	r.parked(question("q-1", "agent:plan"), question("q-2", "agent:plan"), answer("q-1", "Green."))
	r.pass()
	if body := getPage(t, r.a, "/questions"); strings.Contains(body, "Answer by") {
		t.Error("an open question shows an answer")
	}
	if body := getPage(t, r.a, "/questions?state=answered"); !strings.Contains(body, "<b>Answer by bot:</b> Green.") {
		t.Errorf("answered question lacks the answer and its author: %s", body)
	}
}

func TestQuestionsAPI(t *testing.T) {
	r := newQuestionsRig(t)
	n := parkQuestion(t, r, question("q-1", "agent:plan"))
	var got struct {
		Enrich    bool          `json:"enrich"`
		Questions []apiQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(getPage(t, r.a, "/api/v1/questions")), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Enrich || len(got.Questions) != 1 {
		t.Fatalf("api = %+v", got)
	}
	q := got.Questions[0]
	if q.Number != n || q.QID != "q-1" || q.State != "open" || !q.Blocking || q.Audience != "product" || q.Title != "Pick a colour" || q.Resume != "agent:plan" {
		t.Errorf("question = %+v", q)
	}
	if err := json.Unmarshal([]byte(getPage(t, r.a, "/api/v1/questions?audience=project")), &got); err != nil || len(got.Questions) != 0 {
		t.Errorf("audience filter = %+v, %v", got.Questions, err)
	}
}

func TestResumeIsAudited(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"), answer("q-1", "Green."))
	r.pass()
	if got := r.fake.LabelRequests(qProject, n); len(got) != 3 {
		t.Fatalf("label requests = %v", got)
	}
	rows := r.audit(t)
	if len(rows) != 1 || rows[0].Action != "resume" || rows[0].Project != qProject || rows[0].Target != "#1" || !strings.Contains(rows[0].Result, "added agent:plan") {
		t.Errorf("audit = %+v", rows)
	}
	r.pass()
	if len(r.audit(t)) != 1 {
		t.Error("a second pass audited again")
	}
	if got := r.labelsOf(n); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels = %v", got)
	}
}

func (r *questionsRig) labelsOf(n int) []string {
	it, _ := r.fake.Get(qProject, n)
	return it.Labels
}

func TestQuestionsSettingsFollowQuestionsKey(t *testing.T) {
	off := writeRepoConfig(t, "repo: o/a\ngithub:\n  questions: false\n  api_url: http://off.example\n")
	on := writeRepoConfig(t, "repo: o/b\ngithub:\n  api_url: http://on.example\n")
	if g, ok := pickGitHub([]string{off, on}, fleetconfig.GitHub.QuestionsOn); !ok || g.BaseURL() != "http://on.example" {
		t.Errorf("pickGitHub = %+v, %v; want the project with questions on", g, ok)
	}
}
