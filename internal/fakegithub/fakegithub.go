// Package fakegithub is an in-memory GitHub REST API for acceptance tests.
// It implements the issue, pull request, label, comment and search routes
// that the sortie engine's github and github-pr trackers call, and nothing
// else. A request outside that set, or a search qualifier it does not
// evaluate, is answered with an error and listed by Unexpected, so a
// change in what the engine sends fails a test instead of passing
// unevaluated.
package fakegithub

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Comment is one issue or pull request comment. Association is the
// author_association GitHub reports for the author; empty means NONE.
// Updated empty means Created.
type Comment struct {
	ID          int64
	Author      string
	Association string
	Body        string
	Created     time.Time
	Updated     time.Time
}

// Item is an issue or, when PR is set, a pull request. Both share one
// number sequence per repository, as on GitHub.
type Item struct {
	Repo      string
	Number    int
	Title     string
	Body      string
	State     string
	PR        bool
	Labels    []string
	Assignees []string
	Milestone string
	Comments  []Comment
	Created   time.Time
	Updated   time.Time

	version int
}

// LabelEvent is one label write the server accepted.
type LabelEvent struct {
	Repo   string
	Number int
	Label  string
	Added  bool
	At     time.Time
}

// Request is one request the server received.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   string
	Status int
}

// Server is the fake API. It is an http.Handler.
type Server struct {
	// Logf, when set, receives a line for every request the server rejects
	// as unsupported.
	Logf func(format string, args ...any)

	user, token string
	mux         *http.ServeMux

	mu       sync.Mutex
	repos    map[string]*repo
	requests []Request
	events   []LabelEvent
	nextComm int64

	// umu guards unexpected on its own, because reject runs inside
	// handlers that hold mu.
	umu        sync.Mutex
	unexpected []string
}

type repo struct {
	labels []string
	items  []*Item
}

// New returns a server that accepts the bearer token and treats user as the
// owner of that token, which is who assignee:@me means.
func New(user, token string) *Server {
	s := &Server{user: user, token: token, repos: map[string]*repo{}, mux: http.NewServeMux()}
	r := "/repos/{owner}/{repo}"
	s.mux.HandleFunc("GET "+r+"/labels", s.listRepoLabels)
	s.mux.HandleFunc("GET "+r+"/issues", s.listIssues)
	s.mux.HandleFunc("GET "+r+"/issues/{n}", s.getIssue)
	s.mux.HandleFunc("PATCH "+r+"/issues/{n}", s.patchItem)
	s.mux.HandleFunc("GET "+r+"/issues/{n}/comments", s.listComments)
	s.mux.HandleFunc("POST "+r+"/issues/{n}/comments", s.addComment)
	s.mux.HandleFunc("GET "+r+"/issues/{n}/labels", s.listItemLabels)
	s.mux.HandleFunc("POST "+r+"/issues/{n}/labels", s.addLabels)
	s.mux.HandleFunc("DELETE "+r+"/issues/{n}/labels/{name}", s.removeLabel)
	s.mux.HandleFunc("GET "+r+"/issues/{n}/dependencies/blocked_by", s.noBlockers)
	s.mux.HandleFunc("GET "+r+"/issues/{n}/parent", s.noParent)
	s.mux.HandleFunc("GET "+r+"/pulls", s.listPulls)
	s.mux.HandleFunc("GET "+r+"/pulls/{n}", s.getPull)
	s.mux.HandleFunc("PATCH "+r+"/pulls/{n}", s.patchItem)
	s.mux.HandleFunc("GET /search/issues", s.search)
	s.mux.HandleFunc("GET /user", s.getUser)
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.reject(w, r, "unsupported route") })
	return s
}

// AddRepo creates a repository with the given labels.
func (s *Server) AddRepo(slug string, labels ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[strings.ToLower(slug)] = &repo{labels: slices.Clone(labels)}
}

// Add stores it as the next issue or pull request of its repository and
// returns its number. State defaults to open. Labels it carries are added
// to the repository's label list.
func (s *Server) Add(it Item) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repo(it.Repo)
	if r == nil {
		panic("fakegithub: unknown repository " + it.Repo)
	}
	it.Number = len(r.items) + 1
	if it.State == "" {
		it.State = "open"
	}
	now := time.Now().UTC()
	it.Created, it.Updated = now, now
	it.Labels = slices.Clone(it.Labels)
	for _, l := range it.Labels {
		r.ensureLabel(l)
	}
	r.items = append(r.items, &it)
	return it.Number
}

// AddComment stores a comment on an existing issue or pull request as if
// GitHub had accepted it, and returns its ID. Created zero means now.
func (s *Server) AddComment(slug string, n int, c Comment) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.item(slug, n)
	if it == nil {
		panic(fmt.Sprintf("fakegithub: no item %s#%d", slug, n))
	}
	s.nextComm++
	c.ID = s.nextComm
	if c.Created.IsZero() {
		c.Created = time.Now().UTC()
	}
	it.Comments = append(it.Comments, c)
	it.touch()
	return c.ID
}

// SetAssignees replaces the assignees of a seeded issue or pull request.
func (s *Server) SetAssignees(slug string, n int, assignees ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.item(slug, n)
	if it == nil {
		panic(fmt.Sprintf("fakegithub: no item %s#%d", slug, n))
	}
	it.Assignees = slices.Clone(assignees)
	it.touch()
}

// Get returns a copy of one issue or pull request, or false when there is none.
func (s *Server) Get(slug string, n int) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.item(slug, n)
	if it == nil {
		return Item{}, false
	}
	c := *it
	c.Labels = slices.Clone(it.Labels)
	c.Assignees = slices.Clone(it.Assignees)
	c.Comments = slices.Clone(it.Comments)
	return c, true
}

// Requests returns every request received so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// LabelEvents returns the accepted label additions and removals of one
// item, in order.
func (s *Server) LabelEvents(slug string, n int) []LabelEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []LabelEvent
	for _, e := range s.events {
		if strings.EqualFold(e.Repo, slug) && e.Number == n {
			out = append(out, e)
		}
	}
	return out
}

// Unexpected lists the requests and search qualifiers the server rejected
// as unsupported.
func (s *Server) Unexpected() []string {
	s.umu.Lock()
	defer s.umu.Unlock()
	return slices.Clone(s.unexpected)
}

func (s *Server) repo(slug string) *repo { return s.repos[strings.ToLower(slug)] }

func (s *Server) item(slug string, n int) *Item {
	r := s.repo(slug)
	if r == nil || n < 1 || n > len(r.items) {
		return nil
	}
	return r.items[n-1]
}

func (r *repo) ensureLabel(name string) {
	if !slices.ContainsFunc(r.labels, func(l string) bool { return strings.EqualFold(l, name) }) {
		r.labels = append(r.labels, name)
	}
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	switch {
	case r.Header.Get("Authorization") != "Bearer "+s.token:
		writeError(rec, http.StatusUnauthorized, "Bad credentials")
	default:
		s.mux.ServeHTTP(rec, r)
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{r.Method, r.URL.Path, r.URL.RawQuery, string(body), rec.status})
	s.mu.Unlock()
}

// reject answers a request the fake does not implement and remembers it.
func (s *Server) reject(w http.ResponseWriter, r *http.Request, why string) {
	msg := fmt.Sprintf("%s %s?%s: %s", r.Method, r.URL.Path, r.URL.RawQuery, why)
	s.umu.Lock()
	s.unexpected = append(s.unexpected, msg)
	s.umu.Unlock()
	if s.Logf != nil {
		s.Logf("fakegithub: unsupported: %s", msg)
	}
	writeError(w, http.StatusUnprocessableEntity, "fakegithub: "+why)
}

// checkParams rejects a query parameter the handler does not evaluate.
func (s *Server) checkParams(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for k := range r.URL.Query() {
		if !slices.Contains(allowed, k) {
			s.reject(w, r, "unsupported query parameter "+k)
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

// target resolves the {owner}/{repo}/{n} of a request. The caller holds
// s.mu and has already answered when it returns nil.
func (s *Server) target(w http.ResponseWriter, r *http.Request) (*repo, string, *Item) {
	slug := r.PathValue("owner") + "/" + r.PathValue("repo")
	rp := s.repo(slug)
	if rp == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return nil, slug, nil
	}
	if r.PathValue("n") == "" {
		return rp, slug, nil
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	it := s.item(slug, n)
	if err != nil || it == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return nil, slug, nil
	}
	return rp, slug, it
}

func labelObjects(names []string) []map[string]string {
	out := make([]map[string]string, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]string{"name": n})
	}
	return out
}

func itemJSON(it *Item) map[string]any {
	assignees := make([]map[string]string, 0, len(it.Assignees))
	for _, a := range it.Assignees {
		assignees = append(assignees, map[string]string{"login": a})
	}
	kind := "issues"
	if it.PR {
		kind = "pull"
	}
	m := map[string]any{
		"id":         int64(it.Number) + 1000,
		"number":     it.Number,
		"title":      it.Title,
		"body":       it.Body,
		"state":      it.State,
		"html_url":   fmt.Sprintf("https://github.com/%s/%s/%d", it.Repo, kind, it.Number),
		"labels":     labelObjects(it.Labels),
		"assignees":  assignees,
		"created_at": it.Created.Format(time.RFC3339),
		"updated_at": it.Updated.Format(time.RFC3339),
	}
	if it.PR {
		m["pull_request"] = map[string]any{}
	}
	return m
}

// page writes one page of items, with a Link header when more follow.
func page[T any](w http.ResponseWriter, r *http.Request, all []T) []T {
	q := r.URL.Query()
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per < 1 {
		per = 30
	}
	per = min(per, 100)
	pg, _ := strconv.Atoi(q.Get("page"))
	if pg < 1 {
		pg = 1
	}
	start := min((pg-1)*per, len(all))
	end := min(start+per, len(all))
	if end < len(all) {
		next := url.Values{}
		for k, v := range q {
			next[k] = v
		}
		next.Set("page", strconv.Itoa(pg+1))
		u := url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawQuery: next.Encode()}
		w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"next\"", u.String()))
	}
	return all[start:end]
}

func (s *Server) listRepoLabels(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, _, _ := s.target(w, r)
	if rp == nil {
		return
	}
	if !s.checkParams(w, r, "per_page", "page") {
		return
	}
	writeJSON(w, http.StatusOK, labelObjects(page(w, r, rp.labels)))
}

// sorted returns the matching items ordered by creation, newest first when
// the order parameter says desc.
func sorted(items []*Item, keep func(*Item) bool, order string) []*Item {
	var out []*Item
	for _, it := range items {
		if keep(it) {
			out = append(out, it)
		}
	}
	if order == "desc" {
		slices.Reverse(out)
	}
	return out
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, false)
}

func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, true)
}

// list serves /issues, which holds issues and pull requests, and /pulls,
// which holds pull requests only.
func (s *Server) list(w http.ResponseWriter, r *http.Request, pullsOnly bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, _, _ := s.target(w, r)
	if rp == nil {
		return
	}
	if !s.checkParams(w, r, "state", "sort", "direction", "per_page", "page") {
		return
	}
	q := r.URL.Query()
	if v := q.Get("sort"); v != "" && v != "created" {
		s.reject(w, r, "unsupported sort "+v)
		return
	}
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		s.reject(w, r, "unsupported state "+state)
		return
	}
	items := sorted(rp.items, func(it *Item) bool {
		return (!pullsOnly || it.PR) && (state == "all" || it.State == state)
	}, q.Get("direction"))
	out := []map[string]any{}
	for _, it := range page(w, r, items) {
		out = append(out, itemJSON(it))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checkParams(w, r, "q", "sort", "order", "per_page", "page") {
		return
	}
	q := r.URL.Query()
	if v := q.Get("sort"); v != "" && v != "created" {
		s.reject(w, r, "unsupported sort "+v)
		return
	}
	query, err := ParseQuery(q.Get("q"))
	if err != nil {
		s.reject(w, r, err.Error())
		return
	}
	var all []*Item
	for _, rp := range s.repos {
		all = append(all, rp.items...)
	}
	slices.SortFunc(all, func(a, b *Item) int {
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		if c := strings.Compare(a.Repo, b.Repo); c != 0 {
			return c
		}
		return a.Number - b.Number
	})
	matched := sorted(all, func(it *Item) bool { return query.Match(it, s.user) }, q.Get("order"))
	items := []map[string]any{}
	for _, it := range page(w, r, matched) {
		items = append(items, itemJSON(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_count": len(matched), "incomplete_results": false, "items": items,
	})
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, it := s.target(w, r)
	if it == nil {
		return
	}
	w.Header().Set("ETag", etag(it))
	if r.Header.Get("If-None-Match") == etag(it) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, itemJSON(it))
}

func etag(it *Item) string {
	return fmt.Sprintf(`"%x"`, sha1.Sum([]byte(fmt.Sprint(it.Number, it.version))))
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	if !s.checkParams(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"login": s.user})
}

func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, it := s.target(w, r)
	if it == nil {
		return
	}
	if !it.PR {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, itemJSON(it))
}

func (s *Server) patchItem(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, it := s.target(w, r)
	if it == nil {
		return
	}
	var body struct {
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		s.reject(w, r, "unsupported patch body: "+err.Error())
		return
	}
	if body.State != "open" && body.State != "closed" {
		s.reject(w, r, "unsupported state "+body.State)
		return
	}
	it.State = body.State
	it.touch()
	writeJSON(w, http.StatusOK, itemJSON(it))
}

func (it *Item) touch() {
	it.version++
	it.Updated = time.Now().UTC()
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, it := s.target(w, r)
	if it == nil {
		return
	}
	if !s.checkParams(w, r, "per_page", "page") {
		return
	}
	if r.Header.Get("If-None-Match") == etag(it) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag(it))
	out := []map[string]any{}
	for _, c := range page(w, r, it.Comments) {
		out = append(out, commentJSON(it, c))
	}
	writeJSON(w, http.StatusOK, out)
}

func commentJSON(it *Item, c Comment) map[string]any {
	kind := "issues"
	if it.PR {
		kind = "pull"
	}
	updated := c.Updated
	if updated.IsZero() {
		updated = c.Created
	}
	assoc := c.Association
	if assoc == "" {
		assoc = "NONE"
	}
	return map[string]any{
		"id": c.ID, "user": map[string]string{"login": c.Author},
		"author_association": assoc,
		"body":               c.Body,
		"html_url":           fmt.Sprintf("https://github.com/%s/%s/%d#issuecomment-%d", it.Repo, kind, it.Number, c.ID),
		"created_at":         c.Created.Format(time.RFC3339),
		"updated_at":         updated.Format(time.RFC3339),
	}
}

func (s *Server) addComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, it := s.target(w, r)
	if it == nil {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body == "" {
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	s.nextComm++
	c := Comment{ID: s.nextComm, Author: s.user, Association: "OWNER", Body: body.Body, Created: time.Now().UTC()}
	it.Comments = append(it.Comments, c)
	it.touch()
	writeJSON(w, http.StatusCreated, commentJSON(it, c))
}

func (s *Server) listItemLabels(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, it := s.target(w, r)
	if it == nil {
		return
	}
	if !s.checkParams(w, r, "per_page", "page") {
		return
	}
	writeJSON(w, http.StatusOK, labelObjects(page(w, r, it.Labels)))
}

// addLabels adds labels, creating any the repository lacks, as GitHub does.
func (s *Server) addLabels(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, slug, it := s.target(w, r)
	if it == nil {
		return
	}
	var body struct {
		Labels []string `json:"labels"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		s.reject(w, r, "unsupported label body: "+err.Error())
		return
	}
	for _, l := range body.Labels {
		rp.ensureLabel(l)
		if !containsFold(it.Labels, l) {
			it.Labels = append(it.Labels, l)
			s.events = append(s.events, LabelEvent{slug, it.Number, l, true, time.Now()})
		}
	}
	it.touch()
	writeJSON(w, http.StatusOK, labelObjects(it.Labels))
}

func (s *Server) removeLabel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, slug, it := s.target(w, r)
	if it == nil {
		return
	}
	name := r.PathValue("name")
	i := slices.IndexFunc(it.Labels, func(l string) bool { return strings.EqualFold(l, name) })
	if i < 0 {
		writeError(w, http.StatusNotFound, "Label does not exist")
		return
	}
	s.events = append(s.events, LabelEvent{slug, it.Number, it.Labels[i], false, time.Now()})
	it.Labels = slices.Delete(it.Labels, i, i+1)
	it.touch()
	writeJSON(w, http.StatusOK, labelObjects(it.Labels))
}

func (s *Server) noBlockers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, it := s.target(w, r); it != nil {
		writeJSON(w, http.StatusOK, []any{})
	}
}

func (s *Server) noParent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, it := s.target(w, r); it != nil {
		writeError(w, http.StatusNotFound, "Not Found")
	}
}
