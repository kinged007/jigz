package collector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/fakegithub"
	config "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/questions"
	"github.com/kinged007/jigz/internal/store"
)

const qRepo = "acme/app"

type qRig struct {
	*Scanner
	fake *fakegithub.Server
	st   *store.Store
	cfg  config.GitHub
	now  time.Time

	mu sync.Mutex
	// fail answers requests matching "METHOD path-suffix" with 500, as many
	// times as the value says.
	fail map[string]int
	// pageOneUnchanged answers 304 to a comments request with an ETag, as
	// GitHub does when only a later page changed.
	pageOneUnchanged bool
}

func newQRig(t *testing.T) *qRig {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &qRig{fake: fakegithub.New("bot", "tok"), st: st, now: time.Now().UTC()}
	r.fake.Logf = t.Logf
	r.fake.AddRepo(qRepo, "agent:plan", "agent:build", "agent:done", "needs-human", "in-progress")
	r.fail = map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		path := req.URL.EscapedPath()
		if r.pageOneUnchanged && strings.HasSuffix(path, "/comments") && req.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		for k, n := range r.fail {
			method, suffix, _ := strings.Cut(k, " ")
			if n > 0 && req.Method == method && strings.HasSuffix(path, suffix) {
				r.fail[k]--
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
		}
		r.fake.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	r.cfg = config.GitHub{Enrich: true, APIURL: srv.URL}
	r.Scanner = &Scanner{
		Store: st, Settings: func() config.GitHub { return r.cfg }, Now: func() time.Time { return r.now },
		Targets: func() []Target { return []Target{{Project: qRepo, Token: "tok"}} },
	}
	t.Cleanup(func() {
		if u := r.fake.Unexpected(); len(u) > 0 {
			t.Errorf("unexpected requests: %v", u)
		}
	})
	return r
}

func (r *qRig) target() Target { return Target{Project: qRepo, Token: "tok"} }

func (r *qRig) add(labels ...string) int {
	return r.fake.Add(fakegithub.Item{Repo: qRepo, Title: "T", Labels: labels})
}

func (r *qRig) comment(n int, author, assoc, body string) {
	r.fake.AddComment(qRepo, n, fakegithub.Comment{Author: author, Association: assoc, Body: body})
}

func qblock(id string, blocking bool, resume string) string {
	attrs := fmt.Sprintf(`id="%s" audience="product" blocking="%t"`, id, blocking)
	if resume != "" {
		attrs += fmt.Sprintf(` resume="%s"`, resume)
	}
	return fmt.Sprintf("<!-- agent-question %s -->\n**Question:** Which %s?\n\n**Suggested answer:** This one.\n<!-- /agent-question -->", attrs, id)
}

func ablock(id, text string) string {
	return fmt.Sprintf("<!-- agent-answer id=\"%s\" -->\n%s\n<!-- /agent-answer -->", id, text)
}

func (r *qRig) labels(t *testing.T, n int) []string {
	t.Helper()
	it, ok := r.fake.Get(qRepo, n)
	if !ok {
		t.Fatalf("no item %d", n)
	}
	return it.Labels
}

// labelWrites lists the label requests of item n as "METHOD path".
func (r *qRig) labelWrites(n int) []string {
	var out []string
	for _, q := range r.fake.LabelRequests(qRepo, n) {
		out = append(out, q.Method+" "+q.Path)
	}
	return out
}

func (r *qRig) stored(t *testing.T, f store.QuestionFilter) []store.Question {
	t.Helper()
	qs, err := r.st.Questions(f)
	if err != nil {
		t.Fatal(err)
	}
	return qs
}

func noPut(t *testing.T, fake *fakegithub.Server) {
	t.Helper()
	for _, q := range fake.Requests() {
		if q.Method == http.MethodPut {
			t.Errorf("PUT sent: %s", q.Path)
		}
	}
}

func TestScanOffMakesNoRequests(t *testing.T) {
	r := newQRig(t)
	n := r.add("needs-human", "agent:done")
	r.comment(n, "bot", "OWNER", qblock("q-1", true, "agent:plan"))
	r.cfg.Enrich = false
	r.Pass(context.Background())
	r.Once(context.Background())
	if got := r.fake.Requests(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
	if !r.Status(qRepo).At.IsZero() {
		t.Error("a pass ran with enrich off")
	}
}

func TestScanFindsParkedQuestion(t *testing.T) {
	r := newQRig(t)
	n := r.add("needs-human", "agent:done")
	r.comment(n, "bot", "OWNER", qblock("q-1", true, "agent:plan"))
	parkedNoQuestion := r.add("needs-human", "agent:done")
	r.comment(parkedNoQuestion, "bot", "OWNER", "please review")
	r.Pass(context.Background())
	if st := r.Status(qRepo); !st.OK {
		t.Fatalf("status = %+v", st)
	}
	qs := r.stored(t, store.QuestionFilter{})
	if len(qs) != 1 || qs[0].Number != n || qs[0].QID != "q-1" || !qs[0].Blocking || qs[0].Resume != "agent:plan" ||
		qs[0].Question != "Which q-1?" || qs[0].Suggested != "This one." || qs[0].AnsweredAt != "" {
		t.Fatalf("questions = %+v", qs)
	}
	it, err := r.st.QuestionItem(qRepo, n)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(it.Labels, []string{"needs-human", "agent:done"}) || it.CommentsETag == "" {
		t.Errorf("item = %+v", it)
	}
	items, err := r.Items()
	if err != nil || len(items) != 2 {
		t.Errorf("items = %v, %v; want both scanned items", items, err)
	}
	if got := r.labelWrites(n); len(got) != 0 {
		t.Errorf("a scan wrote labels: %v", got)
	}
}

func TestScanAuthorFilter(t *testing.T) {
	r := newQRig(t)
	n := r.add("needs-human", "agent:done")
	r.fake.SetAssignees(qRepo, n, "helper")
	body := ""
	for _, id := range []string{"q-bot", "q-owner", "q-collab", "q-member", "q-assignee", "q-stranger", "q-contrib"} {
		body += qblock(id, false, "") + "\n\n"
	}
	r.comment(n, "bot", "NONE", body)
	r.comment(n, "bot", "NONE", ablock("q-bot", "token account"))
	r.comment(n, "acme", "NONE", ablock("q-owner", "repo owner"))
	r.comment(n, "carol", "COLLABORATOR", ablock("q-collab", "collaborator"))
	r.comment(n, "mike", "MEMBER", ablock("q-member", "member"))
	r.comment(n, "helper", "NONE", ablock("q-assignee", "assignee"))
	r.comment(n, "eve", "NONE", ablock("q-stranger", "stranger"))
	r.comment(n, "dan", "CONTRIBUTOR", ablock("q-contrib", "contributor"))
	r.comment(n, "eve", "NONE", qblock("q-eve", true, "agent:plan"))
	r.Pass(context.Background())
	got := map[string]store.Question{}
	for _, q := range r.stored(t, store.QuestionFilter{}) {
		got[q.QID] = q
	}
	for _, id := range []string{"q-bot", "q-owner", "q-collab", "q-member", "q-assignee"} {
		if got[id].AnsweredAt == "" {
			t.Errorf("%s: answer not counted: %+v", id, got[id])
		}
	}
	for id, author := range map[string]string{"q-stranger": "eve", "q-contrib": "dan"} {
		q := got[id]
		if q.AnsweredAt != "" || len(q.IgnoredAnswers) != 1 || q.IgnoredAnswers[0].Author != author {
			t.Errorf("%s: want ignored answer from %s, got %+v", id, author, q)
		}
	}
	if _, ok := got["q-eve"]; ok {
		t.Error("a question from an unauthorized author was stored")
	}
}

func TestScanReadsRecentRunsAndUsesETags(t *testing.T) {
	r := newQRig(t)
	recent := r.add("agent:build")
	r.comment(recent, "bot", "OWNER", qblock("q-1", false, ""))
	old := r.add("agent:build")
	r.comment(old, "bot", "OWNER", qblock("q-2", false, ""))
	if err := r.st.AddRuns([]store.Run{
		ghRun(1, qRepo, "issues", store.KindIssue, fmt.Sprint(recent), r.now.Add(-time.Hour).Format(time.RFC3339)),
		ghRun(2, qRepo, "issues", store.KindIssue, fmt.Sprint(old), r.now.Add(-200*time.Hour).Format(time.RFC3339)),
	}); err != nil {
		t.Fatal(err)
	}
	r.Pass(context.Background())
	qs := r.stored(t, store.QuestionFilter{})
	if len(qs) != 1 || qs[0].Number != recent || qs[0].Blocking {
		t.Fatalf("questions = %+v", qs)
	}
	for _, q := range r.fake.Requests() {
		if strings.Contains(q.Path, fmt.Sprintf("/issues/%d", old)) {
			t.Errorf("item outside the window was read: %s", q.Path)
		}
	}
	before := len(r.fake.Requests())
	r.now = r.now.Add(5 * time.Minute)
	r.Once(context.Background())
	var statuses []int
	for _, q := range r.fake.Requests()[before:] {
		if strings.HasPrefix(q.Path, "/repos/") {
			statuses = append(statuses, q.Status)
		}
	}
	if !slices.Equal(statuses, []int{304, 304}) {
		t.Errorf("second pass item reads = %v, want the issue and its comments answered 304", statuses)
	}
	if len(r.stored(t, store.QuestionFilter{})) != 1 {
		t.Error("an unchanged item lost its question")
	}

	r.cfg.QuestionsWindow = "30m"
	r.now = r.now.Add(5 * time.Minute)
	r.Pass(context.Background())
	if qs := r.stored(t, store.QuestionFilter{}); len(qs) != 0 {
		t.Errorf("questions of an item outside the window kept: %+v", qs)
	}
}

// parked seeds an item the way an escalating agent and the engine's
// handoff leave it, with the given comments from the token account.
func (r *qRig) parked(bodies ...string) int {
	n := r.add("needs-human", "agent:done")
	for _, b := range bodies {
		r.comment(n, "bot", "OWNER", b)
	}
	return n
}

func (r *qRig) resume(t *testing.T, n int) Resumed {
	t.Helper()
	res, err := r.Resume(context.Background(), r.target(), n)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

var resumeWrites = []string{
	"POST /repos/acme/app/issues/1/labels",
	"DELETE /repos/acme/app/issues/1/labels/agent%3Adone",
	"DELETE /repos/acme/app/issues/1/labels/needs-human",
}

func TestRule2AllAnsweredResumes(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"), qblock("q-2", true, "agent:plan"), ablock("q-1", "a"), ablock("q-2", "b"))
	res := r.resume(t, n)
	if !res.Removed || res.Added != "agent:plan" {
		t.Errorf("result = %+v", res)
	}
	if got := r.labelWrites(n); !slices.Equal(got, resumeWrites) {
		t.Errorf("label writes = %v, want %v", got, resumeWrites)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels = %v", got)
	}
	for _, e := range r.fake.LabelEvents(qRepo, n) {
		if e.Added && e.Label == "in-progress" {
			t.Error("in-progress was added")
		}
	}
	noPut(t, r.fake)
	it, _ := r.st.QuestionItem(qRepo, n)
	if Malformed(Problems(it)) || len(it.Problems) != 0 {
		t.Errorf("problems after resume = %v", it.Problems)
	}
}

func TestRule2KeepsLabels(t *testing.T) {
	cases := map[string][]string{
		"no blocks":           {"please review"},
		"non-blocking only":   {qblock("q-1", false, ""), ablock("q-1", "a")},
		"one of two answered": {qblock("q-1", true, "agent:plan"), qblock("q-2", true, "agent:plan"), ablock("q-1", "a")},
		"malformed block":     {qblock("q-1", true, "agent:plan"), ablock("q-1", "a"), "<!-- agent-question id=\"q-2\" audience=\"product\" blocking=\"true\" -->\n**Question:** No resume?\n<!-- /agent-question -->"},
		"unterminated block":  {qblock("q-1", true, "agent:plan"), ablock("q-1", "a"), "<!-- agent-question id=\"q-3\" audience=\"product\" blocking=\"true\" resume=\"agent:plan\" -->\n**Question:** open"},
		"differing resume":    {qblock("q-1", true, "agent:plan"), qblock("q-2", true, "agent:build"), ablock("q-1", "a"), ablock("q-2", "b")},
		"answer before ask":   {ablock("q-1", "early"), qblock("q-1", true, "agent:plan")},
		"unauthorized answer": {qblock("q-1", true, "agent:plan")},
	}
	for name, bodies := range cases {
		t.Run(name, func(t *testing.T) {
			r := newQRig(t)
			n := r.parked(bodies...)
			if name == "unauthorized answer" {
				r.comment(n, "eve", "NONE", ablock("q-1", "a"))
			}
			res := r.resume(t, n)
			if res.Removed {
				t.Errorf("labels removed: %+v", res)
			}
			if got := r.labelWrites(n); len(got) != 0 {
				t.Errorf("label writes = %v", got)
			}
			if got := r.labels(t, n); !slices.Equal(got, []string{"needs-human", "agent:done"}) {
				t.Errorf("labels = %v", got)
			}
		})
	}
}

func TestRule2LabelsAlreadyRemoved(t *testing.T) {
	r := newQRig(t)
	n := r.add("in-progress")
	r.comment(n, "bot", "OWNER", qblock("q-1", true, "agent:plan"))
	r.comment(n, "bot", "OWNER", ablock("q-1", "a"))
	res := r.resume(t, n)
	if !res.Removed || res.Added != "agent:plan" {
		t.Errorf("result = %+v", res)
	}
	var statuses []int
	for _, q := range r.fake.LabelRequests(qRepo, n) {
		statuses = append(statuses, q.Status)
	}
	if !slices.Equal(statuses, []int{200, 404, 404}) {
		t.Errorf("label statuses = %v, want the add and two 404 deletes", statuses)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"in-progress", "agent:plan"}) {
		t.Errorf("labels = %v", got)
	}
}

func TestRule2KeepsATriggerAPersonAdded(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
	if _, err := r.gh().Post(context.Background(), qRepo, "tok", "/repos/acme/app/issues/1/labels", []byte(`{"labels":["agent:build"]}`)); err != nil {
		t.Fatal(err)
	}
	res := r.resume(t, n)
	if !res.Removed || res.Added != "" {
		t.Errorf("result = %+v", res)
	}
	if got := r.labelWrites(n); !slices.Equal(got, append([]string{"POST /repos/acme/app/issues/1/labels"}, resumeWrites[1:]...)) {
		t.Errorf("label writes = %v", got)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:build"}) {
		t.Errorf("labels = %v", got)
	}
}

func TestAnswerPostsAsTokenAccount(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"))
	if err := r.Answer(context.Background(), r.target(), n, "q-1", "  Use the blue one.  "); err != nil {
		t.Fatal(err)
	}
	it, _ := r.fake.Get(qRepo, n)
	last := it.Comments[len(it.Comments)-1].Body
	want := "<!-- agent-answer id=\"q-1\" by=\"bot\" -->\nUse the blue one.\n<!-- /agent-answer -->"
	if last != want {
		t.Errorf("comment = %q, want %q", last, want)
	}
	sc, err := r.ScanItem(context.Background(), r.target(), n)
	if err != nil {
		t.Fatal(err)
	}
	if q := sc.Question("q-1"); q == nil || q.Current() == nil || q.Current().By != "bot" || !sc.Status.CanResume() {
		t.Errorf("scan = %+v", sc)
	}
}

func TestAnswerBodyRefusesBreakingText(t *testing.T) {
	for _, text := range []string{"", "  ", "x <!-- y", "<!-- /agent-answer -->", "```\nunclosed fence"} {
		if _, err := AnswerBody("q-1", "bot", text); !errors.Is(err, ErrAnswerText) {
			t.Errorf("%q: err = %v, want ErrAnswerText", text, err)
		}
	}
	if _, err := AnswerBody("q-1", "bot", "```\ncode\n```\nfine"); err != nil {
		t.Errorf("closed fence refused: %v", err)
	}
}

const noResume = "<!-- agent-question id=\"q-9\" audience=\"product\" blocking=\"true\" -->\n**Question:** Old block?\n<!-- /agent-question -->"

func TestRemoveLabelsNeedsMalformedBlock(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"))
	if _, err := r.RemoveLabels(context.Background(), r.target(), n, "agent:plan"); !errors.Is(err, ErrNotMalformed) {
		t.Errorf("err = %v, want ErrNotMalformed", err)
	}
	if got := r.labelWrites(n); len(got) != 0 {
		t.Errorf("label writes = %v", got)
	}
}

func TestRemoveLabelsUsesValidResume(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"), noResume)
	res, err := r.RemoveLabels(context.Background(), r.target(), n, "agent:build")
	if err != nil || res.Added != "agent:plan" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels = %v", got)
	}
}

func TestRemoveLabelsFallsBackToFirstLabels(t *testing.T) {
	r := newQRig(t)
	n := r.add("agent:build", "in-progress")
	r.comment(n, "bot", "OWNER", qblock("q-1", false, ""))
	if err := r.st.AddRuns([]store.Run{ghRun(1, qRepo, "issues", store.KindIssue, fmt.Sprint(n), r.now.Format(time.RFC3339))}); err != nil {
		t.Fatal(err)
	}
	r.Pass(context.Background())
	r.fake.EngineHandoff(qRepo, n, "agent:build", "in-progress")
	r.comment(n, "bot", "OWNER", noResume)
	res, err := r.RemoveLabels(context.Background(), r.target(), n, "agent:plan")
	if err != nil || res.Added != "agent:build" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:build"}) {
		t.Errorf("labels = %v", got)
	}
}

func TestRemoveLabelsAsksForATrigger(t *testing.T) {
	r := newQRig(t)
	n := r.parked(noResume)
	if _, err := r.RemoveLabels(context.Background(), r.target(), n, ""); !errors.Is(err, ErrNeedTrigger) {
		t.Fatalf("err = %v, want ErrNeedTrigger", err)
	}
	if got := r.labelWrites(n); len(got) != 0 {
		t.Errorf("label writes = %v", got)
	}
	choices, err := r.Triggers(context.Background(), r.target())
	if err != nil || !slices.Equal(choices, []string{"agent:plan", "agent:build"}) {
		t.Fatalf("triggers = %v, %v", choices, err)
	}
	res, err := r.RemoveLabels(context.Background(), r.target(), n, "agent:build")
	if err != nil || res.Added != "agent:build" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:build"}) {
		t.Errorf("labels = %v", got)
	}
}

func TestProblemsRoundTrip(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"), noResume, ablock("q-7", "x"))
	r.Pass(context.Background())
	it, err := r.st.QuestionItem(qRepo, n)
	if err != nil {
		t.Fatal(err)
	}
	ps := Problems(it)
	var kinds []string
	for _, p := range ps {
		kinds = append(kinds, p.Kind)
		if p.CommentURL == "" {
			t.Errorf("problem without link: %+v", p)
		}
	}
	if !Malformed(ps) || !slices.Equal(kinds, []string{string(questions.MalformedQuestion), string(questions.UnknownAnswer)}) {
		t.Errorf("problems = %+v", ps)
	}
}

func TestRule2FailureMidwayIsReported(t *testing.T) {
	cases := map[string]struct {
		fail string
		want []string
	}{
		"trigger add":        {"POST /issues/1/labels", []string{"needs-human", "agent:done"}},
		"agent:done delete":  {"DELETE /labels/agent%3Adone", []string{"needs-human", "agent:done", "agent:plan"}},
		"needs-human delete": {"DELETE /labels/needs-human", []string{"needs-human", "agent:plan"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newQRig(t)
			n := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
			r.fail[c.fail] = 1
			res, err := r.Resume(context.Background(), r.target(), n)
			if err == nil || res.Removed {
				t.Fatalf("result = %+v, %v; want an error", res, err)
			}
			if got := r.labels(t, n); !slices.Equal(got, c.want) {
				t.Errorf("labels = %v, want %v", got, c.want)
			}
			it, err := r.st.QuestionItem(qRepo, n)
			if err != nil {
				t.Fatal(err)
			}
			if ps := Problems(it); len(ps) != 1 || ps[0].Kind != LabelsNotRemoved {
				t.Errorf("problems = %+v, want labels not removed", ps)
			}
			res = r.resume(t, n)
			if !res.Removed {
				t.Errorf("retry = %+v", res)
			}
			if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
				t.Errorf("labels after retry = %v", got)
			}
		})
	}
}

func TestScanReadsEveryCommentPage(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"))
	for i := 0; i < commentsPerPage; i++ {
		r.comment(n, "bot", "OWNER", fmt.Sprintf("note %d", i))
	}
	r.Pass(context.Background())
	if qs := r.stored(t, store.QuestionFilter{}); len(qs) != 1 || qs[0].AnsweredAt != "" {
		t.Fatalf("questions = %+v", qs)
	}
	r.comment(n, "bot", "OWNER", ablock("q-1", "late"))
	r.pageOneUnchanged = true
	r.Pass(context.Background())
	if qs := r.stored(t, store.QuestionFilter{}); len(qs) != 1 || qs[0].Answer != "late" {
		t.Errorf("answer on the second page missed: %+v", qs)
	}
}

func TestRemoveLabelsUsesFirstLabelsOfMalformedOnlyItem(t *testing.T) {
	r := newQRig(t)
	n := r.add("agent:build", "needs-human")
	r.comment(n, "bot", "OWNER", noResume)
	r.Pass(context.Background())
	r.fake.EngineHandoff(qRepo, n, "agent:build")
	res, err := r.RemoveLabels(context.Background(), r.target(), n, "")
	if err != nil || res.Added != "agent:build" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:build"}) {
		t.Errorf("labels = %v", got)
	}
}
