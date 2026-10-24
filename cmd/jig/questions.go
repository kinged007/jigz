package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/store"
)

// tokenTTL is how long an unused form token is accepted, and how long a
// used one keeps answering with its first result.
const tokenTTL = 24 * time.Hour

// questionsState is the Questions page's state: the scanner, the one-time
// form tokens and the per-item locks of the answer path.
type questionsState struct {
	scanner *collector.Scanner

	mu     sync.Mutex
	tokens map[string]*formToken
	locks  map[string]*sync.Mutex
}

// formToken is issued with one form for one item. done is the redirect
// target of the first submit, "" while unused.
type formToken struct {
	item   string
	issued time.Time
	done   string
}

func newQuestionsState(s *collector.Scanner) *questionsState {
	return &questionsState{scanner: s, tokens: map[string]*formToken{}, locks: map[string]*sync.Mutex{}}
}

func itemKey(project string, n int) string { return project + "#" + strconv.Itoa(n) }

// issue returns a new token for a form on item.
func (q *questionsState) issue(item string) string {
	b := make([]byte, 16)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	for k, t := range q.tokens {
		if now.Sub(t.issued) > tokenTTL {
			delete(q.tokens, k)
		}
	}
	q.tokens[tok] = &formToken{item: item, issued: now}
	return tok
}

// token returns the token issued for item, or nil.
func (q *questionsState) token(tok, item string) *formToken {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.tokens[tok]
	if t == nil || t.item != item || time.Since(t.issued) > tokenTTL {
		return nil
	}
	return t
}

func (q *questionsState) finish(t *formToken, location string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t.done = location
}

// lock serialises the answer path per item and returns the unlock.
func (q *questionsState) lock(item string) func() {
	q.mu.Lock()
	m := q.locks[item]
	if m == nil {
		m = &sync.Mutex{}
		q.locks[item] = m
	}
	q.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// questionRow is one question on the page, with the token of its answer
// form.
type questionRow struct {
	store.Question
	Title string
	URL   string
	Token string
}

// questionItem is a scanned item with problems. Token is the token of
// its Remove labels or Retry form.
type questionItem struct {
	store.QuestionItem
	Problems  []collector.ItemProblem
	Malformed bool
	Token     string
}

// answerDraft is an answer form shown again: after a failed post, or to
// confirm a second answer (Existing set).
type answerDraft struct {
	Project  string
	Number   int
	QID      string
	Text     string
	Token    string
	Existing string
}

// removeConfirm asks before remove-labels. Trigger is the label that will
// be added; when it is empty and Choices is not, the person picks one.
type removeConfirm struct {
	Project string
	Number  int
	Token   string
	Trigger string
	Choices []string
}

type questionsView struct {
	pageData
	Enrich    bool
	Filter    store.QuestionFilter
	Questions []questionRow
	Items     []questionItem
	Draft     *answerDraft
	Remove    *removeConfirm
	// Retry is the item whose answer was posted but whose labels were not
	// changed, with a token for the Retry form.
	Retry *questionItem
}

// questionFilter reads the page's filters. Unknown values are dropped, and
// the state defaults to open.
func questionFilter(q url.Values) store.QuestionFilter {
	f := store.QuestionFilter{Project: q.Get("project"), State: "open"}
	if v := q.Get("audience"); v == "product" || v == "project" {
		f.Audience = v
	}
	if v := q.Get("blocking"); v == "true" || v == "false" {
		f.Blocking = v
	}
	if q.Get("state") == "answered" {
		f.State = "answered"
	}
	return f
}

func (a *app) questionsOn() bool { return a.questions != nil && a.githubOn() }

// questionsData collects the questions and problem items for f. With
// tokens set every form gets a token.
func (a *app) questionsData(f store.QuestionFilter, tokens bool) ([]questionRow, []questionItem, error) {
	if !a.questionsOn() || a.store == nil {
		return nil, nil, nil
	}
	items, err := a.questions.scanner.Items()
	if err != nil {
		return nil, nil, err
	}
	byKey := map[string]store.QuestionItem{}
	var withProblems []questionItem
	for _, it := range items {
		byKey[itemKey(it.Project, it.Number)] = it
		ps := collector.Problems(it)
		if len(ps) == 0 || f.Project != "" && it.Project != f.Project {
			continue
		}
		qi := questionItem{QuestionItem: it, Problems: ps, Malformed: collector.Malformed(ps)}
		if tokens {
			qi.Token = a.questions.issue(itemKey(it.Project, it.Number))
		}
		withProblems = append(withProblems, qi)
	}
	qs, err := a.store.Questions(f)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]questionRow, 0, len(qs))
	for _, q := range qs {
		row := questionRow{Question: q}
		if q.IgnoredAnswers == nil {
			row.IgnoredAnswers = []store.IgnoredAnswer{}
		}
		if it, ok := byKey[itemKey(q.Project, q.Number)]; ok {
			row.Title, row.URL = it.Title, it.URL
		} else if it, err := a.store.QuestionItem(q.Project, q.Number); err == nil {
			row.Title, row.URL = it.Title, it.URL
		}
		if tokens {
			row.Token = a.questions.issue(itemKey(q.Project, q.Number))
		}
		rows = append(rows, row)
	}
	return rows, withProblems, nil
}

func (a *app) questionsPage(w http.ResponseWriter, r *http.Request) {
	a.renderQuestions(w, r, nil, nil, "")
}

// renderQuestions renders the page with an optional draft or confirmation.
// errMsg overrides the err query parameter.
func (a *app) renderQuestions(w http.ResponseWriter, r *http.Request, draft *answerDraft, remove *removeConfirm, errMsg string) {
	q := r.URL.Query()
	if errMsg == "" {
		errMsg = q.Get("err")
	}
	v := questionsView{pageData: a.dataFor(r, q.Get("msg"), errMsg), Enrich: a.questionsOn(), Filter: questionFilter(q), Draft: draft, Remove: remove}
	v.Nav, v.Title = "questions", "Questions"
	var err error
	if v.Questions, v.Items, err = a.questionsData(v.Filter, true); err != nil {
		a.fail(w, "questions", err)
		return
	}
	if n, err := strconv.Atoi(q.Get("number")); err == nil && q.Get("retry") != "" && a.questionsOn() {
		project := q.Get("retry")
		if it, err := a.store.QuestionItem(project, n); err == nil {
			v.Retry = &questionItem{QuestionItem: it, Problems: collector.Problems(it), Token: a.questions.issue(itemKey(project, n))}
		}
	}
	if page.Lookup("questions") != nil {
		execPage(w, "questions", v)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := questionsPlaceholder.Execute(w, v); err != nil {
		log.Printf("questions: %v", err)
	}
}

// questionsPlaceholder renders the page until templates/questions.html
// defines "questions".
var questionsPlaceholder = template.Must(template.New("questions-placeholder").Parse(`<!doctype html>
<title>Questions</title>
{{with .Msg}}<p class="msg">{{.}}</p>{{end}}{{with .Err}}<p class="err">{{.}}</p>{{end}}
{{if not .Enrich}}<p>Set github.enrich to see questions</p>{{else}}
<form method="post" action="/questions/refresh"><button>Refresh</button></form>
{{with .Draft}}<form method="post" action="/questions/answer">{{if .Existing}}<p>Already answered: {{.Existing}}</p><input type="hidden" name="confirm" value="1">{{end}}
<input type="hidden" name="project" value="{{.Project}}"><input type="hidden" name="number" value="{{.Number}}"><input type="hidden" name="qid" value="{{.QID}}"><input type="hidden" name="token" value="{{.Token}}">
<textarea name="text">{{.Text}}</textarea><button>Submit</button></form>{{end}}
{{with .Remove}}<form method="post" action="/questions/remove-labels"><input type="hidden" name="confirm" value="1">
<input type="hidden" name="project" value="{{.Project}}"><input type="hidden" name="number" value="{{.Number}}"><input type="hidden" name="token" value="{{.Token}}">
{{if .Trigger}}<p>Remove needs-human and agent:done, add {{.Trigger}}</p>{{else}}<select name="trigger">{{range .Choices}}<option>{{.}}</option>{{end}}</select>{{end}}<button>Remove labels</button></form>{{end}}
{{with .Retry}}<form method="post" action="/questions/retry"><input type="hidden" name="project" value="{{.Project}}"><input type="hidden" name="number" value="{{.Number}}"><input type="hidden" name="token" value="{{.Token}}"><button>Retry</button></form>{{end}}
{{range .Items}}<div class="item"><a href="{{.URL}}">{{.Project}}#{{.Number}}</a> {{.Title}}<ul>{{range .Problems}}<li>{{.Message}}{{with .CommentURL}} <a href="{{.}}">comment</a>{{end}}</li>{{end}}</ul>
{{if .Malformed}}<form method="post" action="/questions/remove-labels"><input type="hidden" name="project" value="{{.Project}}"><input type="hidden" name="number" value="{{.Number}}"><input type="hidden" name="token" value="{{.Token}}"><button>Remove labels</button></form>{{end}}</div>{{end}}
{{range .Questions}}<div class="question"><a href="{{.URL}}">{{.Project}}#{{.Number}}</a> {{.Title}} {{.QID}} {{.Audience}} blocking={{.Blocking}}
<p>{{.Question.Question}}</p>{{with .Suggested}}<p class="suggested">{{.}}</p>{{end}}{{range .IgnoredAnswers}}<p>ignored: {{.Author}}</p>{{end}}
<form method="post" action="/questions/answer"><input type="hidden" name="project" value="{{.Project}}"><input type="hidden" name="number" value="{{.Number}}"><input type="hidden" name="qid" value="{{.QID}}"><input type="hidden" name="token" value="{{.Token}}">
<textarea name="text"></textarea><button>Submit</button></form></div>{{end}}
{{end}}`))

// questionForm is the common part of the answer path's forms.
type questionForm struct {
	project string
	number  int
	key     string
	target  collector.Target
}

// readQuestionForm checks the form's item and returns it, or writes the
// redirect and returns false.
func (a *app) readQuestionForm(w http.ResponseWriter, r *http.Request) (questionForm, bool) {
	fail := func(msg string) (questionForm, bool) {
		http.Redirect(w, r, "/questions?err="+query(msg), http.StatusSeeOther)
		return questionForm{}, false
	}
	if err := r.ParseForm(); err != nil {
		return fail("bad form")
	}
	if !a.questionsOn() {
		return fail("Set github.enrich to see questions")
	}
	f := questionForm{project: r.FormValue("project")}
	n, err := strconv.Atoi(r.FormValue("number"))
	if err != nil || n < 1 {
		return fail("bad item number")
	}
	f.number, f.key = n, itemKey(f.project, n)
	t, ok := a.questions.scanner.Target(f.project)
	if !ok {
		return fail(fmt.Sprintf("unknown project %q", f.project))
	}
	f.target = t
	return f, true
}

// useToken returns the form's token, or redirects: to the first result
// when the token was used, else to an error.
func (a *app) useToken(w http.ResponseWriter, r *http.Request, f questionForm) (*formToken, bool) {
	t := a.questions.token(r.FormValue("token"), f.key)
	switch {
	case t == nil:
		http.Redirect(w, r, "/questions?err="+query("this form has expired; reload the page"), http.StatusSeeOther)
		return nil, false
	case t.done != "":
		http.Redirect(w, r, t.done, http.StatusSeeOther)
		return nil, false
	}
	return t, true
}

// done records the result of a token's first submit and redirects to it.
func (a *app) done(w http.ResponseWriter, r *http.Request, t *formToken, f questionForm, msg string, err error, retry bool) {
	loc := "/questions?msg=" + query(msg)
	if err != nil {
		loc = "/questions?err=" + query(msg+": "+err.Error())
	}
	if retry {
		loc += "&retry=" + query(f.project) + "&number=" + strconv.Itoa(f.number)
	}
	a.questions.finish(t, loc)
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

// resumeNote describes step 4's outcome.
func resumeNote(res collector.Resumed) string {
	switch {
	case res.Removed && res.Added != "":
		return fmt.Sprintf("removed %s and %s, added %s", collector.NeedsHuman, collector.AgentDone, res.Added)
	case res.Removed:
		return fmt.Sprintf("removed %s and %s; a trigger was already set", collector.NeedsHuman, collector.AgentDone)
	case !res.Scan.Status.Blocking:
		return "labels unchanged: the item has no blocking question"
	case res.Scan.Status.Malformed:
		return "labels unchanged: the item has a malformed question block"
	case len(res.Scan.Status.Problems) > 0:
		return "labels unchanged: " + res.Scan.Status.Problems[0].Message
	}
	return "labels unchanged: a blocking question is still open"
}

// questionsAnswer is the answer write path of plans/jigz/questions.md
// section 2.5.
func (a *app) questionsAnswer(w http.ResponseWriter, r *http.Request) {
	f, ok := a.readQuestionForm(w, r)
	if !ok {
		return
	}
	defer a.questions.lock(f.key)()
	t, ok := a.useToken(w, r, f)
	if !ok {
		return
	}
	qid, text := r.FormValue("qid"), strings.TrimSpace(r.FormValue("text"))
	target := fmt.Sprintf("#%d %s", f.number, qid)
	draft := &answerDraft{Project: f.project, Number: f.number, QID: qid, Text: text, Token: r.FormValue("token")}
	if _, err := collector.AnswerBody(qid, "x", text); err != nil {
		a.renderQuestions(w, r, draft, nil, err.Error())
		return
	}
	sc, err := a.questions.scanner.ScanItem(r.Context(), f.target, f.number)
	if err != nil {
		a.renderQuestions(w, r, draft, nil, "answer not posted: "+err.Error())
		return
	}
	q := sc.Question(qid)
	if q == nil {
		a.done(w, r, t, f, "question no longer exists", nil, false)
		return
	}
	if cur := q.Current(); cur != nil && r.FormValue("confirm") == "" {
		draft.Existing = cur.Text
		a.renderQuestions(w, r, draft, nil, "")
		return
	}
	if err := a.questions.scanner.Answer(r.Context(), f.target, f.number, qid, text); err != nil {
		a.audit(r, "answer", f.project, target, "answer not posted: "+err.Error())
		a.renderQuestions(w, r, draft, nil, "answer not posted: "+err.Error())
		return
	}
	res, err := a.questions.scanner.Resume(r.Context(), f.target, f.number)
	if err != nil {
		a.audit(r, "answer", f.project, target, "answer posted, labels not removed: "+err.Error())
		a.done(w, r, t, f, "answer posted, labels not removed", err, true)
		return
	}
	note := "answer posted; " + resumeNote(res)
	a.audit(r, "answer", f.project, target, note)
	a.done(w, r, t, f, note, nil, false)
}

// questionsRetry repeats step 4 of the answer path for one item.
func (a *app) questionsRetry(w http.ResponseWriter, r *http.Request) {
	f, ok := a.readQuestionForm(w, r)
	if !ok {
		return
	}
	defer a.questions.lock(f.key)()
	t, ok := a.useToken(w, r, f)
	if !ok {
		return
	}
	target := fmt.Sprintf("#%d", f.number)
	res, err := a.questions.scanner.Resume(r.Context(), f.target, f.number)
	if err != nil {
		a.audit(r, "retry-labels", f.project, target, "labels not removed: "+err.Error())
		a.done(w, r, t, f, "labels not removed", err, true)
		return
	}
	note := resumeNote(res)
	a.audit(r, "retry-labels", f.project, target, note)
	a.done(w, r, t, f, note, nil, false)
}

// questionsRemoveLabels removes needs-human and agent:done from an item
// with a malformed question block and adds a trigger, after a
// confirmation (plans/jigz/questions.md section 2.4).
func (a *app) questionsRemoveLabels(w http.ResponseWriter, r *http.Request) {
	f, ok := a.readQuestionForm(w, r)
	if !ok {
		return
	}
	defer a.questions.lock(f.key)()
	t, ok := a.useToken(w, r, f)
	if !ok {
		return
	}
	s := a.questions.scanner
	if r.FormValue("confirm") == "" {
		sc, err := s.ScanItem(r.Context(), f.target, f.number)
		if err != nil {
			a.renderQuestions(w, r, nil, nil, "labels not removed: "+err.Error())
			return
		}
		if !sc.Status.Malformed {
			a.done(w, r, t, f, "labels not removed", collector.ErrNotMalformed, false)
			return
		}
		c := &removeConfirm{Project: f.project, Number: f.number, Token: r.FormValue("token"), Trigger: collector.RemoveTrigger(sc)}
		if c.Trigger == "" && !collector.HasTrigger(sc.Item.Labels) {
			if c.Choices, err = s.Triggers(r.Context(), f.target); err != nil {
				a.renderQuestions(w, r, nil, nil, "labels not removed: "+err.Error())
				return
			}
		}
		a.renderQuestions(w, r, nil, c, "")
		return
	}
	target := fmt.Sprintf("#%d", f.number)
	res, err := s.RemoveLabels(r.Context(), f.target, f.number, r.FormValue("trigger"))
	switch {
	case errors.Is(err, collector.ErrNeedTrigger):
		a.renderQuestions(w, r, nil, nil, "labels not removed: "+err.Error())
		return
	case err != nil:
		a.audit(r, "remove-labels", f.project, target, "labels not removed: "+err.Error())
		a.done(w, r, t, f, "labels not removed", err, false)
		return
	}
	note := resumeNote(res)
	a.audit(r, "remove-labels", f.project, target, note)
	a.done(w, r, t, f, note, nil, false)
}

// questionsRefresh runs a scan pass now.
func (a *app) questionsRefresh(w http.ResponseWriter, r *http.Request) {
	if !a.questionsOn() {
		http.Redirect(w, r, "/questions?err="+query("Set github.enrich to see questions"), http.StatusSeeOther)
		return
	}
	a.questions.scanner.Pass(r.Context())
	http.Redirect(w, r, "/questions?msg="+query("questions refreshed"), http.StatusSeeOther)
}
