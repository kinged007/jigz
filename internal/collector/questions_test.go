package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	// resumes records the OnResume calls as "project#number trigger".
	resumes []string
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
	r.Scanner = r.newScanner(st)
	t.Cleanup(func() {
		if u := r.fake.Unexpected(); len(u) > 0 {
			t.Errorf("unexpected requests: %v", u)
		}
	})
	return r
}

// newScanner returns a scanner over the rig's fake and settings that
// records its resumes in r.resumes.
func (r *qRig) newScanner(st *store.Store) *Scanner {
	return &Scanner{
		Store: st, Settings: func() config.GitHub { return r.cfg }, Now: func() time.Time { return r.now },
		Targets: func() []Target { return []Target{{Project: qRepo, Token: "tok"}} },
		OnResume: func(project string, n int, trigger string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.resumes = append(r.resumes, fmt.Sprintf("%s#%d %s", project, n, trigger))
		},
	}
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

func TestScanQuestionsOffMakesNoRequests(t *testing.T) {
	r := newQRig(t)
	n := r.add("needs-human", "agent:done")
	r.comment(n, "bot", "OWNER", qblock("q-1", true, "agent:plan"))
	r.comment(n, "bot", "OWNER", ablock("q-1", "a"))
	r.Questions = func(project string) bool { return project != qRepo }
	r.Pass(context.Background())
	r.Once(context.Background())
	if got := r.fake.Requests(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
	if !r.Status(qRepo).At.IsZero() {
		t.Error("a pass ran with questions off")
	}
}

func TestScanRunsWithEnrichOff(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
	r.cfg.Enrich = false
	r.Once(context.Background())
	if st := r.Status(qRepo); !st.OK {
		t.Fatalf("status = %+v", st)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels = %v, want the item resumed", got)
	}
}

func TestScanWithoutTokenReportsIt(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
	r.Targets = func() []Target { return []Target{{Project: qRepo, TokenEnv: "MY_TOKEN"}} }
	r.Pass(context.Background())
	if got := r.fake.Requests(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
	if st := r.Status(qRepo); st.OK || st.Err != "no GitHub token" {
		t.Errorf("status = %+v, want no GitHub token", st)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"needs-human", "agent:done"}) {
		t.Errorf("labels = %v", got)
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

// recent stores a run of item n so step 2 makes it a candidate.
func (r *qRig) recent(t *testing.T, n int) {
	t.Helper()
	if err := r.st.AddRuns([]store.Run{ghRun(int64(n), qRepo, "issues", store.KindIssue, fmt.Sprint(n), r.now.Format(time.RFC3339))}); err != nil {
		t.Fatal(err)
	}
}

func (r *qRig) problemKinds(t *testing.T, n int) []string {
	t.Helper()
	it, err := r.st.QuestionItem(qRepo, n)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range Problems(it) {
		out = append(out, p.Kind)
	}
	return out
}

func (r *qRig) resumeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.resumes)
}

var resumeWrites = []string{
	"POST /repos/acme/app/issues/1/labels",
	"DELETE /repos/acme/app/issues/1/labels/agent%3Adone",
	"DELETE /repos/acme/app/issues/1/labels/needs-human",
}

func TestResumeRule2(t *testing.T) {
	const unterminated = "<!-- agent-question id=\"q-3\" audience=\"product\" blocking=\"true\" resume=\"agent:plan\" -->\n**Question:** open"
	cases := []struct {
		name   string
		labels []string
		bodies []string
		// person is an answer to q-1 from eve, who is not authorized.
		person bool
		resume bool
	}{
		{name: "all answered", bodies: []string{qblock("q-1", true, "agent:plan"), qblock("q-2", true, "agent:plan"), ablock("q-1", "a"), ablock("q-2", "b")}, resume: true},
		{name: "no blocks", bodies: []string{"please review"}},
		{name: "non-blocking only", bodies: []string{qblock("q-1", false, ""), ablock("q-1", "a")}},
		{name: "one of two answered", bodies: []string{qblock("q-1", true, "agent:plan"), qblock("q-2", true, "agent:plan"), ablock("q-1", "a")}},
		{name: "malformed block", bodies: []string{qblock("q-1", true, "agent:plan"), ablock("q-1", "a"), noResume}},
		{name: "unterminated block", bodies: []string{qblock("q-1", true, "agent:plan"), ablock("q-1", "a"), unterminated}},
		{name: "differing resume", bodies: []string{qblock("q-1", true, "agent:plan"), qblock("q-2", true, "agent:build"), ablock("q-1", "a"), ablock("q-2", "b")}},
		{name: "answer before ask", bodies: []string{ablock("q-1", "early"), qblock("q-1", true, "agent:plan")}},
		{name: "only an unauthorized answer", bodies: []string{qblock("q-1", true, "agent:plan")}, person: true},
		{name: "no needs-human", labels: []string{"agent:done"}, bodies: []string{qblock("q-1", true, "agent:plan"), ablock("q-1", "a")}},
		{name: "labels already removed", labels: []string{"agent:plan"}, bodies: []string{qblock("q-1", true, "agent:plan"), ablock("q-1", "a")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newQRig(t)
			labels := c.labels
			if labels == nil {
				labels = []string{"needs-human", "agent:done"}
			}
			n := r.add(labels...)
			for _, b := range c.bodies {
				r.comment(n, "bot", "OWNER", b)
			}
			if c.person {
				r.comment(n, "eve", "NONE", ablock("q-1", "a"))
			}
			r.recent(t, n)
			r.Pass(context.Background())
			if st := r.Status(qRepo); !st.OK {
				t.Fatalf("status = %+v", st)
			}
			if _, err := r.st.QuestionItem(qRepo, n); err != nil {
				t.Fatalf("item not scanned: %v", err)
			}
			if !c.resume {
				if got := r.labelWrites(n); len(got) != 0 {
					t.Errorf("label writes = %v, want none", got)
				}
				if got := r.labels(t, n); !slices.Equal(got, labels) {
					t.Errorf("labels = %v, want %v", got, labels)
				}
				if r.resumeCount() != 0 {
					t.Errorf("OnResume called: %v", r.resumes)
				}
				return
			}
			if got := r.labelWrites(n); !slices.Equal(got, resumeWrites) {
				t.Errorf("label writes = %v, want %v", got, resumeWrites)
			}
			if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
				t.Errorf("labels = %v", got)
			}
		})
	}
}

func TestResumeWritesOnlyLabels(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
	r.Pass(context.Background())
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
	for _, q := range r.fake.Requests() {
		if q.Method == http.MethodPost && strings.HasSuffix(q.Path, "/comments") {
			t.Errorf("comment posted: %s", q.Path)
		}
	}
	noPut(t, r.fake)
	if !slices.Equal(r.resumes, []string{"acme/app#1 agent:plan"}) {
		t.Errorf("OnResume calls = %v", r.resumes)
	}
	it, err := r.st.QuestionItem(qRepo, n)
	if err != nil || len(it.Problems) != 0 || !slices.Equal(it.Labels, []string{"agent:plan"}) {
		t.Errorf("stored item = %+v, %v", it, err)
	}

	r.now = r.now.Add(5 * time.Minute)
	r.Pass(context.Background())
	if len(r.labelWrites(n)) != len(resumeWrites) || r.resumeCount() != 1 {
		t.Errorf("a second pass wrote again: %v, resumes %v", r.labelWrites(n), r.resumes)
	}
}

func TestResumeKeepsATriggerAPersonAdded(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
	r.fake.AddLabelAsPerson(qRepo, n, "agent:build", "alice")
	r.Pass(context.Background())
	if got := r.labelWrites(n); !slices.Equal(got, resumeWrites[1:]) {
		t.Errorf("label writes = %v, want only the deletes", got)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:build"}) {
		t.Errorf("labels = %v", got)
	}
	if !slices.Equal(r.resumes, []string{"acme/app#1 agent:build"}) {
		t.Errorf("OnResume calls = %v", r.resumes)
	}
}

func TestResumeTreats404AsSuccess(t *testing.T) {
	r := newQRig(t)
	n := r.add("needs-human")
	r.comment(n, "bot", "OWNER", qblock("q-1", true, "agent:plan"))
	r.comment(n, "bot", "OWNER", ablock("q-1", "a"))
	r.Pass(context.Background())
	var statuses []int
	for _, q := range r.fake.LabelRequests(qRepo, n) {
		statuses = append(statuses, q.Status)
	}
	if !slices.Equal(statuses, []int{200, 404, 200}) {
		t.Errorf("label statuses = %v, want the add, a 404 for agent:done and the needs-human delete", statuses)
	}
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels = %v", got)
	}
	if got := r.problemKinds(t, n); len(got) != 0 || r.resumeCount() != 1 {
		t.Errorf("problems = %v, resumes = %v", got, r.resumes)
	}
}

func TestResumeEventsCheck(t *testing.T) {
	r := newQRig(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r.fake.SetClock(func() time.Time { return clock })
	n := r.add("agent:done")
	r.comment(n, "bot", "OWNER", qblock("q-1", true, "agent:plan"))
	clock = clock.Add(time.Second)
	r.comment(n, "alice", "COLLABORATOR", ablock("q-1", "old answer"))
	clock = clock.Add(2 * time.Second)
	r.fake.AddLabelAsPerson(qRepo, n, "needs-human", "bot")

	r.Pass(context.Background())
	if got := r.labelWrites(n); len(got) != 0 {
		t.Fatalf("label writes = %v, want none for an answer older than needs-human", got)
	}
	if got := r.problemKinds(t, n); !slices.Equal(got, []string{ParkedAgain}) {
		t.Errorf("problems = %v, want parked again", got)
	}

	before := len(r.fake.Requests())
	r.Pass(context.Background())
	for _, q := range r.fake.Requests()[before:] {
		if strings.HasSuffix(q.Path, "/events") || (strings.HasPrefix(q.Path, "/repos/") && q.Status != http.StatusNotModified) {
			t.Errorf("unchanged parked item read again: %s %d", q.Path, q.Status)
		}
	}
	if got := r.problemKinds(t, n); !slices.Equal(got, []string{ParkedAgain}) {
		t.Errorf("problems after an unchanged pass = %v", got)
	}

	clock = clock.Add(5 * time.Second)
	r.comment(n, "alice", "COLLABORATOR", ablock("q-1", "new answer"))
	r.Pass(context.Background())
	if got := r.labelWrites(n); !slices.Equal(got, resumeWrites) {
		t.Errorf("label writes = %v, want %v for an answer newer than needs-human", got, resumeWrites)
	}
	if got := r.problemKinds(t, n); len(got) != 0 {
		t.Errorf("problems after resume = %v", got)
	}
}

func TestResumeEventsSameSecondIsNotNewer(t *testing.T) {
	r := newQRig(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r.fake.SetClock(func() time.Time { return clock })
	n := r.add("agent:done")
	r.comment(n, "bot", "OWNER", qblock("q-1", true, "agent:plan"))
	clock = clock.Add(time.Second)
	r.fake.AddLabelAsPerson(qRepo, n, "needs-human", "bot")
	clock = clock.Add(500 * time.Millisecond)
	r.comment(n, "alice", "COLLABORATOR", ablock("q-1", "a"))
	r.Pass(context.Background())
	if got := r.labelWrites(n); len(got) != 0 {
		t.Errorf("label writes = %v, want none for an answer in the second needs-human was added", got)
	}
}

func TestResumeFailureIsRetried(t *testing.T) {
	cases := map[string]struct {
		fail string
		want []string
	}{
		"events read":        {"GET /issues/1/events", []string{"needs-human", "agent:done"}},
		"trigger add":        {"POST /issues/1/labels", []string{"needs-human", "agent:done"}},
		"agent:done delete":  {"DELETE /labels/agent%3Adone", []string{"needs-human", "agent:done", "agent:plan"}},
		"needs-human delete": {"DELETE /labels/needs-human", []string{"needs-human", "agent:plan"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newQRig(t)
			n := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
			r.fail[c.fail] = 1
			r.Pass(context.Background())
			if got := r.labels(t, n); !slices.Equal(got, c.want) {
				t.Errorf("labels = %v, want %v", got, c.want)
			}
			it, err := r.st.QuestionItem(qRepo, n)
			if err != nil {
				t.Fatal(err)
			}
			if ps := Problems(it); len(ps) != 1 || ps[0].Kind != ResumeFailed || !strings.HasPrefix(ps[0].Message, "resume failed: ") {
				t.Errorf("problems = %+v, want resume failed", ps)
			}
			if r.resumeCount() != 0 {
				t.Errorf("OnResume called after a failure: %v", r.resumes)
			}
			r.Pass(context.Background())
			if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
				t.Errorf("labels after retry = %v", got)
			}
			if got := r.problemKinds(t, n); len(got) != 0 {
				t.Errorf("problems after retry = %v", got)
			}
			if !slices.Equal(r.resumes, []string{"acme/app#1 agent:plan"}) {
				t.Errorf("OnResume calls = %v", r.resumes)
			}
			noPut(t, r.fake)
		})
	}
}

func TestTwoScannersEndWithTheSameLabels(t *testing.T) {
	r := newQRig(t)
	n := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	other := r.newScanner(st)

	// The other scanner reads the item before this one resumes it, and
	// writes on that stale read afterwards.
	ctx := context.Background()
	it, _, err := other.issue(ctx, r.target(), n, false)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := other.scan(ctx, r.target(), "bot", it, true)
	if err != nil || !stale.ready() {
		t.Fatalf("stale scan = %+v, %v", stale, err)
	}
	r.Pass(ctx)
	if _, err := other.relabel(ctx, r.target(), stale); err != nil {
		t.Errorf("second writer failed: %v", err)
	}
	other.Pass(ctx)
	if got := r.labels(t, n); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels = %v", got)
	}

	m := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
	var wg sync.WaitGroup
	for _, s := range []*Scanner{r.Scanner, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Pass(ctx)
		}()
	}
	wg.Wait()
	if got := r.labels(t, m); !slices.Equal(got, []string{"agent:plan"}) {
		t.Errorf("labels after concurrent passes = %v", got)
	}
	noPut(t, r.fake)
}

func TestSearchQualifierAndSearchOnly(t *testing.T) {
	r := newQRig(t)
	r.Qualifier = "label:runner:m1"
	r.SearchOnly = true
	mine := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
	r.fake.AddLabelAsPerson(qRepo, mine, "runner:m1", "bot")
	other := r.parked(qblock("q-1", true, "agent:plan"), ablock("q-1", "a"))
	running := r.add("agent:build")
	r.comment(running, "bot", "OWNER", qblock("q-2", false, ""))
	r.recent(t, running)
	r.Pass(context.Background())
	var queries []string
	for _, q := range r.fake.Requests() {
		if q.Path == "/search/issues" {
			v, _ := url.ParseQuery(q.Query)
			queries = append(queries, v.Get("q"))
		}
		if strings.Contains(q.Path, fmt.Sprintf("/issues/%d", running)) {
			t.Errorf("an item outside the search was read: %s", q.Path)
		}
	}
	if !slices.Equal(queries, []string{"repo:acme/app state:open label:needs-human label:runner:m1"}) {
		t.Errorf("search queries = %v", queries)
	}
	if got := r.labels(t, mine); !slices.Equal(got, []string{"runner:m1", "agent:plan"}) {
		t.Errorf("labels of the routed item = %v", got)
	}
	if got := r.labelWrites(other); len(got) != 0 {
		t.Errorf("an item without the runner label was written: %v", got)
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

const noResume = "<!-- agent-question id=\"q-9\" audience=\"product\" blocking=\"true\" -->\n**Question:** Old block?\n<!-- /agent-question -->"

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
