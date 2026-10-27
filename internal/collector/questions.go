package collector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	config "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/github"
	"github.com/kinged007/jigz/internal/questions"
	"github.com/kinged007/jigz/internal/store"
)

// Labels the resume reads and writes (plans/jigz/questions.md section 1).
const (
	NeedsHuman = "needs-human"
	AgentDone  = "agent:done"
)

// Problem kinds the scanner adds to those of the parser
// (plans/jigz/questions.md section 2.4).
const (
	// ResumeFailed is an item whose resume stopped at a failed call; the
	// next pass retries it.
	ResumeFailed = "resume_failed"
	// ParkedAgain is an answered item whose needs-human was added after
	// its latest answer.
	ParkedAgain = "parked_again"
)

// errNoToken is the status of a project without a token.
var errNoToken = errors.New("no GitHub token")

const commentsPerPage = 100

var triggerRe = regexp.MustCompile(`^agent:[A-Za-z0-9._/-]+$`)

// IsTrigger reports whether label is a trigger: an agent: label other than
// agent:done.
func IsTrigger(label string) bool {
	return triggerRe.MatchString(label) && label != AgentDone
}

// HasTrigger reports whether labels hold a trigger.
func HasTrigger(labels []string) bool { return slices.ContainsFunc(labels, IsTrigger) }

// ItemProblem is one problem of a scanned item, stored as a JSON string in
// question_items.problems. Kind is a questions.ProblemKind, ResumeFailed or
// ParkedAgain.
type ItemProblem struct {
	Kind       string `json:"kind"`
	QuestionID string `json:"question_id,omitempty"`
	CommentURL string `json:"comment_url,omitempty"`
	Author     string `json:"author,omitempty"`
	Message    string `json:"message"`
}

// Problems decodes the problems of a stored item. An entry that is not an
// encoded ItemProblem is returned as its message.
func Problems(it store.QuestionItem) []ItemProblem {
	out := make([]ItemProblem, 0, len(it.Problems))
	for _, raw := range it.Problems {
		var p ItemProblem
		if json.Unmarshal([]byte(raw), &p) != nil {
			p = ItemProblem{Message: raw}
		}
		out = append(out, p)
	}
	return out
}

// Malformed reports whether problems hold a malformed question block, which
// stops the resume.
func Malformed(ps []ItemProblem) bool {
	return slices.ContainsFunc(ps, func(p ItemProblem) bool {
		return p.Kind == string(questions.MalformedQuestion) || p.Kind == string(questions.MalformedBlock)
	})
}

// ItemScan is one item read from GitHub and parsed.
type ItemScan struct {
	Item   store.QuestionItem
	Result questions.Result
	Status questions.Status
}

// ready reports whether the item meets the first three conditions of
// rule 2: it carries needs-human, every blocking question has an
// authorized answer, and the parse found no problem that stops a resume.
func (sc ItemScan) ready() bool {
	return slices.Contains(sc.Item.Labels, NeedsHuman) && sc.Status.CanResume()
}

// Scanner finds agent questions and answers on GitHub and resumes parked
// items whose blocking questions are answered (plans/jigz/questions.md
// sections 2.1 and 2.4). Its only writes are label changes. Fill the
// exported fields before use.
type Scanner struct {
	Store    *store.Store
	Settings func() config.GitHub
	Targets  func() []Target
	// Questions reports whether a project has github.questions on. Nil
	// scans every target.
	Questions func(project string) bool
	// Qualifier is added to the search of step 1, such as
	// label:runner:<name>.
	Qualifier string
	// SearchOnly skips step 2: items with a recent run are not candidates
	// unless the search finds them.
	SearchOnly bool
	// OnResume is called once per resumed item with the trigger it now
	// carries.
	OnResume func(project string, number int, trigger string)
	Now      func() time.Time

	// passMu keeps passes from overlapping.
	passMu  sync.Mutex
	mu      sync.Mutex
	clients map[string]*github.Client
	last    time.Time
	status  map[string]EnrichStatus
	items   map[string]map[int]bool
	events  map[string]eventsCache
}

// eventsCache is the latest needs-human labeled time of one item and the
// ETag of the events page it was read from.
type eventsCache struct {
	etag string
	at   time.Time
}

func (s *Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// gh returns the client for the configured API root. Each root keeps its
// own back-off and token logins.
func (s *Scanner) gh() *github.Client {
	base := s.Settings().BaseURL()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[base]
	if !ok {
		if s.clients == nil {
			s.clients = map[string]*github.Client{}
		}
		c = &github.Client{BaseURL: base, Now: s.Now}
		s.clients[base] = c
	}
	return c
}

// Status returns the outcome of the last pass over a project.
func (s *Scanner) Status(project string) EnrichStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status[project]
}

// Target returns the target of project.
func (s *Scanner) Target(project string) (Target, bool) {
	for _, t := range s.Targets() {
		if t.Project == project {
			return t, true
		}
	}
	return Target{}, false
}

// Items returns the stored rows of the items the last pass, or a later
// rescan, read, ordered by project and number.
func (s *Scanner) Items() ([]store.QuestionItem, error) {
	s.mu.Lock()
	type key struct {
		project string
		number  int
	}
	var keys []key
	for p, ns := range s.items {
		for n := range ns {
			keys = append(keys, key{p, n})
		}
	}
	s.mu.Unlock()
	slices.SortFunc(keys, func(a, b key) int {
		if c := strings.Compare(a.project, b.project); c != 0 {
			return c
		}
		return a.number - b.number
	})
	var out []store.QuestionItem
	for _, k := range keys {
		it, err := s.Store.QuestionItem(k.project, k.number)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

func (s *Scanner) remember(project string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]map[int]bool{}
	}
	if s.items[project] == nil {
		s.items[project] = map[int]bool{}
	}
	s.items[project][n] = true
}

// Run checks every few seconds whether a pass is due, until ctx is
// cancelled.
func (s *Scanner) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		s.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once runs a pass when questions_interval has passed.
func (s *Scanner) Once(ctx context.Context) {
	g := s.Settings()
	s.mu.Lock()
	due := s.last.IsZero() || s.now().Sub(s.last) >= g.QuestionPassInterval()
	s.mu.Unlock()
	if due {
		s.Pass(ctx)
	}
}

// Pass scans every target whose project has github.questions on.
func (s *Scanner) Pass(ctx context.Context) {
	s.passMu.Lock()
	defer s.passMu.Unlock()
	s.mu.Lock()
	s.last = s.now()
	s.mu.Unlock()
	for _, t := range s.Targets() {
		if ctx.Err() != nil {
			return
		}
		if s.Questions != nil && !s.Questions(t.Project) {
			continue
		}
		st := s.project(ctx, t)
		s.mu.Lock()
		if s.status == nil {
			s.status = map[string]EnrichStatus{}
		}
		s.status[t.Project] = st
		s.mu.Unlock()
	}
}

// ghItem is an issue or pull request as the search and issue routes return
// it.
type ghItem struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	PullRequest       *json.RawMessage `json:"pull_request"`
	Body              string           `json:"body"`
	AuthorAssociation string           `json:"author_association"`
	User              struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

func (g ghItem) item(project, etag string) store.QuestionItem {
	it := store.QuestionItem{
		Project: project, Number: g.Number, IsPR: g.PullRequest != nil, Title: g.Title, URL: g.HTMLURL,
		IssueETag: etag, Labels: []string{}, Assignees: []string{},
		Body: g.Body, Author: g.User.Login, AuthorAssociation: g.AuthorAssociation,
	}
	if !g.CreatedAt.IsZero() {
		it.CreatedAt = g.CreatedAt.UTC().Format(time.RFC3339)
	}
	for _, l := range g.Labels {
		it.Labels = append(it.Labels, l.Name)
	}
	for _, a := range g.Assignees {
		it.Assignees = append(it.Assignees, a.Login)
	}
	return it
}

type ghComment struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	AuthorAssociation string    `json:"author_association"`
	Body              string    `json:"body"`
	HTMLURL           string    `json:"html_url"`
	CreatedAt         time.Time `json:"created_at"`
}

func (s *Scanner) project(ctx context.Context, t Target) EnrichStatus {
	now := s.now()
	st := EnrichStatus{At: now, OK: true}
	fail := func(err error) EnrichStatus {
		st.OK, st.Err = false, err.Error()
		return st
	}
	if t.Token == "" {
		return fail(errNoToken)
	}
	if until, limited := s.gh().LimitedUntil(t.Project); limited {
		return fail(&github.RateLimitError{Until: until})
	}
	login, err := s.gh().User(ctx, t.Project, t.Token)
	if err != nil {
		return fail(err)
	}
	cands, err := s.candidates(ctx, t, now)
	if err != nil {
		return fail(err)
	}
	keep := map[int]bool{}
	for _, c := range cands {
		if ctx.Err() != nil {
			return st
		}
		keep[c.Number] = true
		sc, err := s.scan(ctx, t, login, c, false)
		if errors.Is(err, github.ErrNotFound) {
			continue
		}
		if err != nil {
			return fail(err)
		}
		if sc.ready() {
			if err := s.resume(ctx, t, login, sc); err != nil {
				return fail(err)
			}
		}
	}
	if err := s.dropStale(t.Project, keep); err != nil {
		return fail(err)
	}
	return st
}

// candidates lists the open items to scan: those labelled needs-human
// that match Qualifier, and, unless SearchOnly is set, those with a run
// within questions_window.
func (s *Scanner) candidates(ctx context.Context, t Target, now time.Time) ([]store.QuestionItem, error) {
	query := fmt.Sprintf("repo:%s state:open label:%s", t.Project, NeedsHuman)
	if s.Qualifier != "" {
		query += " " + s.Qualifier
	}
	q := url.QueryEscape(query)
	resp, err := s.gh().Get(ctx, t.Project, t.Token, "/search/issues?q="+q+"&per_page=100", "")
	if err != nil {
		return nil, err
	}
	var sr struct {
		Items []ghItem `json:"items"`
	}
	if err := json.Unmarshal(resp.Body, &sr); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	var out []store.QuestionItem
	seen := map[int]bool{}
	for _, g := range sr.Items {
		seen[g.Number] = true
		it := g.item(t.Project, "")
		if prev, err := s.Store.QuestionItem(t.Project, g.Number); err == nil {
			it.IssueETag = prev.IssueETag
		}
		out = append(out, it)
	}
	if s.SearchOnly {
		return out, nil
	}
	runs, err := s.Store.RunItems(t.Project)
	if err != nil {
		return nil, err
	}
	since := now.Add(-s.Settings().QuestionWindow())
	for _, r := range runs {
		if seen[r.Number] {
			continue
		}
		if at, err := time.Parse(time.RFC3339, r.Latest); err == nil && at.Before(since) {
			continue
		}
		seen[r.Number] = true
		it, open, err := s.issue(ctx, t, r.Number, true)
		if errors.Is(err, github.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if open {
			out = append(out, it)
		}
	}
	return out, nil
}

// issue reads one item's labels and assignees. With cached set it sends the
// stored ETag and, on a 304, returns the stored row as open.
func (s *Scanner) issue(ctx context.Context, t Target, n int, cached bool) (store.QuestionItem, bool, error) {
	prev, err := s.Store.QuestionItem(t.Project, n)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return store.QuestionItem{}, false, err
	}
	etag := ""
	if cached && err == nil && prev.CreatedAt != "" {
		etag = prev.IssueETag
	}
	resp, err := s.gh().Get(ctx, t.Project, t.Token, fmt.Sprintf("/repos/%s/issues/%d", t.Project, n), etag)
	if err != nil {
		return store.QuestionItem{}, false, err
	}
	if resp.NotModified {
		return prev, true, nil
	}
	var g ghItem
	if err := json.Unmarshal(resp.Body, &g); err != nil {
		return store.QuestionItem{}, false, fmt.Errorf("issue %d: %w", n, err)
	}
	return g.item(t.Project, resp.ETag), g.State == "open", nil
}

// dropStale clears the stored questions of items of project that are no
// longer candidates.
func (s *Scanner) dropStale(project string, keep map[int]bool) error {
	qs, err := s.Store.Questions(store.QuestionFilter{Project: project})
	if err != nil {
		return err
	}
	done := map[int]bool{}
	for _, q := range qs {
		if keep[q.Number] || done[q.Number] {
			continue
		}
		done[q.Number] = true
		it, err := s.Store.QuestionItem(project, q.Number)
		if err != nil {
			return err
		}
		it.Problems = nil
		if err := s.Store.ReplaceQuestions(it, nil); err != nil {
			return err
		}
	}
	s.mu.Lock()
	for n := range s.items[project] {
		if !keep[n] {
			delete(s.items[project], n)
		}
	}
	s.mu.Unlock()
	return nil
}

// scan reads the comments of it, parses them and stores the result. Without
// force it sends the stored comments ETag and skips an item whose comments
// and fields are unchanged, unless the stored item may be ready to resume;
// the returned scan is then empty.
func (s *Scanner) scan(ctx context.Context, t Target, login string, it store.QuestionItem, force bool) (ItemScan, error) {
	prev, err := s.Store.QuestionItem(t.Project, it.Number)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ItemScan{}, err
	}
	stored := err == nil
	etag := ""
	if stored && !force {
		etag = prev.CommentsETag
	}
	comments, cetag, notModified, err := s.comments(ctx, t, it.Number, etag)
	if err != nil {
		return ItemScan{}, err
	}
	if notModified {
		if sameItem(prev, it) && !s.mayResume(prev) {
			s.remember(t.Project, it.Number)
			return ItemScan{}, nil
		}
		if comments, cetag, _, err = s.comments(ctx, t, it.Number, ""); err != nil {
			return ItemScan{}, err
		}
	}
	comments = append(bodyComment(it), comments...)
	res := questions.Parse(comments, authorized(t.Project, login, it.Assignees))
	st := questions.Check(res)
	it.CommentsETag = cetag
	it.ScannedAt = s.now().UTC().Format(time.RFC3339)
	it.Problems = encodeProblems(res, st)
	if err := s.Store.ReplaceQuestions(it, storeQuestions(res)); err != nil {
		return ItemScan{}, err
	}
	saved, err := s.Store.QuestionItem(t.Project, it.Number)
	if err != nil {
		return ItemScan{}, err
	}
	s.remember(t.Project, it.Number)
	return ItemScan{Item: saved, Result: res, Status: st}, nil
}

// bodyComment is the item's description as a comment by the item's author,
// created when the item was. An edit of the description does not change
// that time, so an answer posted before the edit still answers a question
// added by it.
func bodyComment(it store.QuestionItem) []questions.Comment {
	if strings.TrimSpace(it.Body) == "" {
		return nil
	}
	created, _ := time.Parse(time.RFC3339, it.CreatedAt)
	return []questions.Comment{{
		IsBody: true, Author: it.Author, AuthorAssociation: it.AuthorAssociation,
		URL: it.URL, Created: created, Body: it.Body,
	}}
}

func sameItem(prev, it store.QuestionItem) bool {
	return prev.Title == it.Title && prev.URL == it.URL && prev.IsPR == it.IsPR &&
		prev.Body == it.Body && prev.Author == it.Author && prev.AuthorAssociation == it.AuthorAssociation &&
		slices.Equal(prev.Labels, it.Labels) && slices.Equal(prev.Assignees, it.Assignees)
}

// comments reads every comment of item n. The ETag applies to the first
// page; a 304 there returns notModified and no comments. The returned ETag
// is empty when the comments fill more than one page, because a new
// comment on a later page does not change the first page's ETag.
func (s *Scanner) comments(ctx context.Context, t Target, n int, etag string) ([]questions.Comment, string, bool, error) {
	var out []questions.Comment
	first := ""
	for page := 1; ; page++ {
		if page == 2 {
			first = ""
		}
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=%d", t.Project, n, commentsPerPage)
		send := etag
		if page > 1 {
			path += "&page=" + strconv.Itoa(page)
			send = ""
		}
		resp, err := s.gh().Get(ctx, t.Project, t.Token, path, send)
		if err != nil {
			return nil, "", false, err
		}
		if resp.NotModified {
			return nil, etag, true, nil
		}
		if page == 1 {
			first = resp.ETag
		}
		var cs []ghComment
		if err := json.Unmarshal(resp.Body, &cs); err != nil {
			return nil, "", false, fmt.Errorf("comments of %d: %w", n, err)
		}
		for _, c := range cs {
			out = append(out, questions.Comment{
				ID: c.ID, Author: c.User.Login, AuthorAssociation: c.AuthorAssociation,
				URL: c.HTMLURL, Created: c.CreatedAt, Body: c.Body,
			})
		}
		if len(cs) < commentsPerPage {
			return out, first, false, nil
		}
	}
}

// authorized is the author filter of plans/jigz/questions.md section 2.2:
// the token account, the repository owner, authors GitHub reports as
// OWNER, MEMBER or COLLABORATOR, and the item's assignees.
func authorized(project, login string, assignees []string) func(questions.Comment) bool {
	owner, _, _ := strings.Cut(project, "/")
	return func(c questions.Comment) bool {
		switch c.AuthorAssociation {
		case "OWNER", "MEMBER", "COLLABORATOR":
			return true
		}
		same := func(name string) bool { return name != "" && strings.EqualFold(name, c.Author) }
		return same(login) || same(owner) || slices.ContainsFunc(assignees, same)
	}
}

func encodeProblems(res questions.Result, st questions.Status) []string {
	out := []string{}
	for _, p := range append(slices.Clone(res.Problems), st.Problems...) {
		out = append(out, encodeProblem(ItemProblem{Kind: string(p.Kind), QuestionID: p.QuestionID, CommentURL: p.CommentURL, Author: p.Author, Message: p.Message}))
	}
	return out
}

func encodeProblem(p ItemProblem) string {
	b, _ := json.Marshal(p)
	return string(b)
}

func storeQuestions(res questions.Result) []store.Question {
	out := make([]store.Question, 0, len(res.Questions))
	for _, q := range res.Questions {
		sq := store.Question{
			QID: q.ID, Audience: q.Audience, Blocking: q.Blocking, Resume: q.Resume,
			Question: q.Text, Suggested: q.Suggested, AskedCommentURL: q.CommentURL, AskedCommentID: q.CommentID,
			AskedAt: q.Asked.UTC().Format(time.RFC3339), AskedCount: q.AskedCount,
			IgnoredAnswers: []store.IgnoredAnswer{},
		}
		if a := q.Current(); a != nil {
			sq.Answer, sq.AnsweredBy, sq.AnswerCommentURL = a.Text, a.Author, a.CommentURL
			sq.AnsweredAt = a.Created.UTC().Format(time.RFC3339)
		}
		for _, a := range q.Ignored {
			sq.IgnoredAnswers = append(sq.IgnoredAnswers, store.IgnoredAnswer{Author: a.Author, CommentURL: a.CommentURL})
		}
		out = append(out, sq)
	}
	return out
}

// mayResume reports whether a stored item may be ready to resume, so a
// pass reads it in full even when GitHub reports it unchanged: a resume
// that failed is retried. An item parked again after its last answer stays
// so until its comments or labels change.
func (s *Scanner) mayResume(prev store.QuestionItem) bool {
	if !slices.Contains(prev.Labels, NeedsHuman) {
		return false
	}
	for _, p := range Problems(prev) {
		switch questions.ProblemKind(p.Kind) {
		case ParkedAgain, questions.MalformedQuestion, questions.MalformedBlock, questions.ResumeConflict:
			return false
		}
	}
	qs, err := s.Store.Questions(store.QuestionFilter{Project: prev.Project, Number: prev.Number})
	if err != nil {
		return true
	}
	blocking := false
	for _, q := range qs {
		if q.Blocking {
			blocking = true
			if q.AnsweredAt == "" {
				return false
			}
		}
	}
	return blocking
}

// resume runs the resume of plans/jigz/questions.md section 2.4 on an item
// that meets the first three conditions of rule 2. A failed GitHub call is
// stored as the item's ResumeFailed problem; only a store error is
// returned.
func (s *Scanner) resume(ctx context.Context, t Target, login string, sc ItemScan) error {
	n := sc.Item.Number
	labeled, err := s.labeledAt(ctx, t, n)
	if err != nil {
		return s.addProblem(sc, ResumeFailed, "resume failed: read events: "+err.Error())
	}
	if !answeredAfter(sc.Result, labeled) {
		return s.addProblem(sc, ParkedAgain, "parked again after the last answer")
	}
	it, _, err := s.issue(ctx, t, n, false)
	if err != nil {
		return s.addProblem(sc, ResumeFailed, "resume failed: read item: "+err.Error())
	}
	fresh, err := s.scan(ctx, t, login, it, true)
	if err != nil {
		return s.addProblem(sc, ResumeFailed, "resume failed: read comments: "+err.Error())
	}
	if !fresh.ready() {
		return nil
	}
	if !answeredAfter(fresh.Result, labeled) {
		return s.addProblem(fresh, ParkedAgain, "parked again after the last answer")
	}
	trigger, err := s.relabel(ctx, t, fresh)
	if err != nil {
		return s.addProblem(fresh, ResumeFailed, "resume failed: "+err.Error())
	}
	item := fresh.Item
	item.Labels = slices.DeleteFunc(slices.Clone(item.Labels), func(l string) bool { return l == AgentDone || l == NeedsHuman })
	if !slices.Contains(item.Labels, trigger) {
		item.Labels = append(item.Labels, trigger)
	}
	item.IssueETag = ""
	if err := s.Store.ReplaceQuestions(item, storeQuestions(fresh.Result)); err != nil {
		return err
	}
	if s.OnResume != nil {
		s.OnResume(t.Project, n, trigger)
	}
	return nil
}

// addProblem stores the item of sc with one more problem.
func (s *Scanner) addProblem(sc ItemScan, kind, msg string) error {
	it := sc.Item
	it.Problems = append(slices.Clone(it.Problems), encodeProblem(ItemProblem{Kind: kind, Message: msg}))
	return s.Store.ReplaceQuestions(it, storeQuestions(sc.Result))
}

// answeredAfter reports whether an authorized answer to a blocking question
// was created strictly after t, both at one-second precision.
func answeredAfter(res questions.Result, t time.Time) bool {
	t = t.Truncate(time.Second)
	for _, q := range res.Questions {
		if a := q.Current(); q.Blocking && a != nil && a.Created.Truncate(time.Second).After(t) {
			return true
		}
	}
	return false
}

const eventsPerPage = 100

// labeledAt returns the time of the latest labeled event for needs-human on
// item n, zero when there is none. It reads every events page, sending the
// cached ETag on the first. The ETag is cached only when the events fit on
// one page, because a new event on a later page does not change the first
// page's ETag.
func (s *Scanner) labeledAt(ctx context.Context, t Target, n int) (time.Time, error) {
	key := t.Project + "#" + strconv.Itoa(n)
	s.mu.Lock()
	cached, ok := s.events[key]
	s.mu.Unlock()
	var latest time.Time
	first := ""
	for page := 1; ; page++ {
		path := fmt.Sprintf("/repos/%s/issues/%d/events?per_page=%d", t.Project, n, eventsPerPage)
		send := ""
		if page == 1 && ok {
			send = cached.etag
		}
		if page > 1 {
			path += "&page=" + strconv.Itoa(page)
			first = ""
		}
		resp, err := s.gh().Get(ctx, t.Project, t.Token, path, send)
		if err != nil {
			return time.Time{}, err
		}
		if resp.NotModified {
			return cached.at, nil
		}
		if page == 1 {
			first = resp.ETag
		}
		var es []struct {
			Event string `json:"event"`
			Label struct {
				Name string `json:"name"`
			} `json:"label"`
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(resp.Body, &es); err != nil {
			return time.Time{}, fmt.Errorf("events of %d: %w", n, err)
		}
		for _, e := range es {
			if e.Event == "labeled" && strings.EqualFold(e.Label.Name, NeedsHuman) && e.CreatedAt.After(latest) {
				latest = e.CreatedAt
			}
		}
		if len(es) < eventsPerPage {
			break
		}
	}
	s.mu.Lock()
	if s.events == nil {
		s.events = map[string]eventsCache{}
	}
	s.events[key] = eventsCache{etag: first, at: latest}
	s.mu.Unlock()
	return latest, nil
}

// relabel adds the item's resume trigger unless it carries a trigger, then
// removes agent:done and last needs-human, with one DELETE each and a 404
// meaning already gone. It returns the trigger the item now carries.
// agent:done outranks the trigger and needs-human keeps the item out of the
// dispatch query, so a failure midway dispatches nothing. Each call changes
// only its own label, so the calls are idempotent and another writer's
// labels are kept.
func (s *Scanner) relabel(ctx context.Context, t Target, sc ItemScan) (string, error) {
	n := sc.Item.Number
	trigger := sc.Status.Resume
	if i := slices.IndexFunc(sc.Item.Labels, IsTrigger); i >= 0 {
		trigger = sc.Item.Labels[i]
	} else {
		body, err := json.Marshal(map[string][]string{"labels": {trigger}})
		if err != nil {
			return "", err
		}
		if _, err := s.gh().Post(ctx, t.Project, t.Token, fmt.Sprintf("/repos/%s/issues/%d/labels", t.Project, n), body); err != nil {
			return "", fmt.Errorf("add %s: %w", trigger, err)
		}
	}
	for _, l := range []string{AgentDone, NeedsHuman} {
		name := strings.ReplaceAll(url.PathEscape(l), ":", "%3A")
		err := s.gh().Delete(ctx, t.Project, t.Token, fmt.Sprintf("/repos/%s/issues/%d/labels/%s", t.Project, n, name))
		if err != nil && !errors.Is(err, github.ErrNotFound) {
			return "", fmt.Errorf("remove %s: %w", l, err)
		}
	}
	return trigger, nil
}
