package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
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

	mu sync.Mutex
	// fail answers matching requests ("METHOD path-suffix") with 500.
	fail map[string]int
}

func newQuestionsRig(t *testing.T) *questionsRig {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &questionsRig{fake: fakegithub.New("bot", "tok"), fail: map[string]int{}}
	r.fake.Logf = t.Logf
	r.fake.AddRepo(qProject, "agent:plan", "agent:build", "agent:done", "needs-human")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		for k, n := range r.fail {
			method, suffix, _ := strings.Cut(k, " ")
			if n > 0 && req.Method == method && strings.HasSuffix(req.URL.EscapedPath(), suffix) {
				r.fail[k]--
				r.mu.Unlock()
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
		}
		r.mu.Unlock()
		r.fake.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	r.a = &app{sizes: newRepoSizes(), stateDir: t.TempDir(), store: st,
		github: fleetconfig.GitHub{Enrich: true, APIURL: srv.URL}}
	r.a.questions = newQuestionsState(&collector.Scanner{
		Store: st, Settings: r.a.githubSettings,
		Targets: func() []collector.Target { return []collector.Target{{Project: qProject, Token: "tok"}} },
	})
	t.Cleanup(func() {
		if u := r.fake.Unexpected(); len(u) > 0 {
			t.Errorf("unexpected requests: %v", u)
		}
	})
	return r
}

// parked seeds an item with needs-human and agent:done and comments from
// the token account.
func (r *questionsRig) parked(bodies ...string) int {
	n := r.fake.Add(fakegithub.Item{Repo: qProject, Title: "Pick a colour", Labels: []string{"needs-human", "agent:done"}})
	for _, b := range bodies {
		r.fake.AddComment(qProject, n, fakegithub.Comment{Author: "bot", Association: "OWNER", Body: b})
	}
	return n
}

func question(id, resume string) string {
	return `<!-- agent-question id="` + id + `" audience="product" blocking="true" resume="` + resume + `" -->
**Question:** Which colour for ` + id + `?

**Suggested answer:** Blue.
<!-- /agent-question -->`
}

func (r *questionsRig) token(n int) string { return r.a.questions.issue(itemKey(qProject, n)) }

// post sends a form and returns the response.
func (r *questionsRig) post(t *testing.T, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.a.routes(newHostGuard("127.0.0.1")).ServeHTTP(rec, req)
	return rec
}

func answerForm(n int, qid, text, token string) url.Values {
	return url.Values{"project": {qProject}, "number": {strconv.Itoa(n)}, "qid": {qid}, "text": {text}, "token": {token}}
}

func location(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect: %s", rec.Code, rec.Body.String())
	}
	loc, err := url.QueryUnescape(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func (r *questionsRig) comments(t *testing.T, n int) []string {
	t.Helper()
	it, _ := r.fake.Get(qProject, n)
	var out []string
	for _, c := range it.Comments {
		out = append(out, c.Body)
	}
	return out
}

func (r *questionsRig) labels(t *testing.T, n int) []string {
	t.Helper()
	it, _ := r.fake.Get(qProject, n)
	return it.Labels
}

func (r *questionsRig) labelWrites(n int) []string {
	var out []string
	for _, q := range r.fake.LabelRequests(qProject, n) {
		out = append(out, q.Method+" "+q.Path)
	}
	return out
}

func (r *questionsRig) audit(t *testing.T) []store.AuditEntry {
	t.Helper()
	rows, err := r.a.store.Audit(50)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAnswerPostsOnceAndResumes(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"))
	form := answerForm(n, "q-1", "Green.", r.token(n))
	first := location(t, r.post(t, "/questions/answer", form))
	if !strings.Contains(first, "answer posted") || !strings.Contains(first, "added agent:plan") {
		t.Errorf("first result = %q", first)
	}
	second := location(t, r.post(t, "/questions/answer", form))
	if second != first {
		t.Errorf("second submit = %q, want the first result %q", second, first)
	}
	cs := r.comments(t, n)
	if len(cs) != 2 || !strings.HasPrefix(cs[1], `<!-- agent-answer id="q-1" by="bot" -->`) {
		t.Errorf("comments = %q, want one answer from the token login", cs)
	}
	want := []string{
		"DELETE /repos/acme/app/issues/1/labels/needs-human",
		"DELETE /repos/acme/app/issues/1/labels/agent%3Adone",
		"POST /repos/acme/app/issues/1/labels",
	}
	if got := r.labelWrites(n); !slices.Equal(got, want) {
		t.Errorf("label writes = %v, want %v", got, want)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels = %v", got)
	}
	for _, q := range r.fake.Requests() {
		if q.Method == http.MethodPut {
			t.Errorf("PUT sent: %s", q.Path)
		}
	}
	rows := r.audit(t)
	if len(rows) != 1 || rows[0].Action != "answer" || rows[0].Project != qProject || !strings.Contains(rows[0].Result, "added agent:plan") {
		t.Errorf("audit = %+v", rows)
	}
}

func TestAnswerOneOfTwoKeepsLabels(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"), question("q-2", "agent:plan"))
	loc := location(t, r.post(t, "/questions/answer", answerForm(n, "q-1", "Green.", r.token(n))))
	if !strings.Contains(loc, "a blocking question is still open") {
		t.Errorf("result = %q", loc)
	}
	if got := r.labelWrites(n); len(got) != 0 {
		t.Errorf("label writes = %v", got)
	}
	if len(r.comments(t, n)) != 3 {
		t.Error("answer not posted")
	}
}

func TestAnswerCommentFailureChangesNothing(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"))
	r.fail["POST /issues/1/comments"] = 1
	tok := r.token(n)
	rec := r.post(t, "/questions/answer", answerForm(n, "q-1", "Green.", tok))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "answer not posted") || !strings.Contains(rec.Body.String(), "Green.") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	if len(r.comments(t, n)) != 1 || len(r.labelWrites(n)) != 0 {
		t.Fatalf("something changed: comments %q, label writes %v", r.comments(t, n), r.labelWrites(n))
	}
	loc := location(t, r.post(t, "/questions/answer", answerForm(n, "q-1", "Green.", tok)))
	if !strings.Contains(loc, "added agent:plan") || len(r.comments(t, n)) != 2 {
		t.Errorf("resubmit = %q, comments %d", loc, len(r.comments(t, n)))
	}
}

func TestAnswerLabelFailureIsRetryable(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"))
	r.fail["DELETE /labels/agent%3Adone"] = 1
	loc := location(t, r.post(t, "/questions/answer", answerForm(n, "q-1", "Green.", r.token(n))))
	if !strings.Contains(loc, "answer posted, labels not removed") || !strings.Contains(loc, "retry="+qProject) {
		t.Fatalf("result = %q", loc)
	}
	body := getPage(t, r.a, rec(loc))
	if !strings.Contains(body, `action="/questions/retry"`) {
		t.Errorf("page has no Retry form")
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:done"}) {
		t.Errorf("labels after failure = %v", got)
	}
	form := url.Values{"project": {qProject}, "number": {strconv.Itoa(n)}, "token": {r.token(n)}}
	loc = location(t, r.post(t, "/questions/retry", form))
	if !strings.Contains(loc, "added agent:plan") {
		t.Errorf("retry = %q", loc)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels after retry = %v", got)
	}
	if len(r.comments(t, n)) != 2 {
		t.Error("retry posted another answer")
	}
	var actions []string
	for _, e := range r.audit(t) {
		actions = append(actions, e.Action)
	}
	if !slices.Equal(actions, []string{"retry-labels", "answer"}) {
		t.Errorf("audit actions = %v", actions)
	}
}

// rec turns a redirect location into a request path.
func rec(loc string) string {
	u, _ := url.Parse(loc)
	return u.Path + "?" + u.Query().Encode()
}

func TestAnswerAskedTwiceNeedsConfirm(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:build"), `<!-- agent-answer id="q-1" -->
Red.
<!-- /agent-answer -->`, question("q-2", "agent:build"))
	tok := r.token(n)
	resp := r.post(t, "/questions/answer", answerForm(n, "q-1", "Green.", tok))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "Red.") {
		t.Fatalf("response = %d %s", resp.Code, resp.Body.String())
	}
	if len(r.comments(t, n)) != 3 {
		t.Fatal("answer posted without confirmation")
	}
	form := answerForm(n, "q-1", "Green.", tok)
	form.Set("confirm", "1")
	location(t, r.post(t, "/questions/answer", form))
	if len(r.comments(t, n)) != 4 {
		t.Error("confirmed answer not posted")
	}
}

func TestAnswerToMissingQuestion(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"))
	loc := location(t, r.post(t, "/questions/answer", answerForm(n, "q-5", "Green.", r.token(n))))
	if !strings.Contains(loc, "question no longer exists") || len(r.comments(t, n)) != 1 {
		t.Errorf("result = %q", loc)
	}
}

func TestAnswerRefusesBadTokenAndText(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"))
	other := r.fake.Add(fakegithub.Item{Repo: qProject, Title: "other"})
	for name, form := range map[string]url.Values{
		"no token":       answerForm(n, "q-1", "Green.", "nope"),
		"other item":     answerForm(n, "q-1", "Green.", r.token(other)),
		"marker in text": answerForm(n, "q-1", "<!-- /agent-answer -->", r.token(n)),
	} {
		resp := r.post(t, "/questions/answer", form)
		if resp.Code != http.StatusOK && resp.Code != http.StatusSeeOther {
			t.Errorf("%s: status %d", name, resp.Code)
		}
	}
	if len(r.comments(t, n)) != 1 {
		t.Errorf("comments = %q", r.comments(t, n))
	}
}

func TestQuestionsOffMakesNoRequests(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"))
	r.a.github.Enrich = false
	body := getPage(t, r.a, "/questions")
	if !strings.Contains(body, "Set github.enrich to see questions") {
		t.Errorf("page lacks the enrich message")
	}
	loc := location(t, r.post(t, "/questions/answer", answerForm(n, "q-1", "Green.", r.token(n))))
	if !strings.Contains(loc, "github.enrich") {
		t.Errorf("answer = %q", loc)
	}
	location(t, r.post(t, "/questions/refresh", url.Values{}))
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

func TestQuestionsAPIAfterRefresh(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"))
	r.parked("please review")
	location(t, r.post(t, "/questions/refresh", url.Values{}))
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
	if q.Number != n || q.QID != "q-1" || q.State != "open" || !q.Blocking || q.Audience != "product" || q.Title != "Pick a colour" {
		t.Errorf("question = %+v", q)
	}
	if err := json.Unmarshal([]byte(getPage(t, r.a, "/api/v1/questions?audience=project")), &got); err != nil || len(got.Questions) != 0 {
		t.Errorf("audience filter = %+v, %v", got.Questions, err)
	}
	if body := getPage(t, r.a, "/questions"); !strings.Contains(body, "Which colour for q-1?") || !strings.Contains(body, `name="token"`) {
		t.Errorf("page lacks the question form")
	}
}

const noResumeBlock = `<!-- agent-question id="q-9" audience="product" blocking="true" -->
**Question:** Old block?
<!-- /agent-question -->`

func TestRemoveLabelsConfirmsAndAsksForTrigger(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(noResumeBlock)
	tok := r.token(n)
	form := url.Values{"project": {qProject}, "number": {strconv.Itoa(n)}, "token": {tok}}
	resp := r.post(t, "/questions/remove-labels", form)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "<option>agent:build</option>") {
		t.Fatalf("confirmation = %d %s", resp.Code, resp.Body.String())
	}
	if len(r.labelWrites(n)) != 0 {
		t.Fatal("labels changed before confirmation")
	}
	form.Set("confirm", "1")
	form.Set("trigger", "agent:build")
	loc := location(t, r.post(t, "/questions/remove-labels", form))
	if !strings.Contains(loc, "added agent:build") {
		t.Errorf("result = %q", loc)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:build"}) {
		t.Errorf("labels = %v", got)
	}
	rows := r.audit(t)
	if len(rows) != 1 || rows[0].Action != "remove-labels" {
		t.Errorf("audit = %+v", rows)
	}
}

func TestRemoveLabelsRefusesWellFormedItem(t *testing.T) {
	r := newQuestionsRig(t)
	n := r.parked(question("q-1", "agent:plan"))
	form := url.Values{"project": {qProject}, "number": {strconv.Itoa(n)}, "token": {r.token(n)}, "confirm": {"1"}, "trigger": {"agent:plan"}}
	loc := location(t, r.post(t, "/questions/remove-labels", form))
	if !strings.Contains(loc, "no malformed question block") || len(r.labelWrites(n)) != 0 {
		t.Errorf("result = %q, writes %v", loc, r.labelWrites(n))
	}
}
