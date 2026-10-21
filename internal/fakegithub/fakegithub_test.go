package fakegithub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const tok = "secret"

func newServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := New("me", tok)
	s.Logf = t.Logf
	s.AddRepo("o/r", "agent:plan", "in-progress")
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts
}

func call(t *testing.T, ts *httptest.Server, method, path, body string, hdr ...string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out), resp.Header
}

func names(t *testing.T, body string) []string {
	t.Helper()
	var labels []struct{ Name string }
	if err := json.Unmarshal([]byte(body), &labels); err != nil {
		t.Fatalf("labels %q: %v", body, err)
	}
	out := []string{}
	for _, l := range labels {
		out = append(out, l.Name)
	}
	return out
}

func TestAuthRequired(t *testing.T) {
	_, ts := newServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/repos/o/r/labels", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", resp.StatusCode)
	}
}

func TestLabelEndpoints(t *testing.T) {
	s, ts := newServer(t)
	n := s.Add(Item{Repo: "o/r", Title: "t", Labels: []string{"agent:plan"}})

	code, body, _ := call(t, ts, "POST", "/repos/o/r/issues/1/labels", `{"labels":["in-progress"]}`)
	if code != 200 || !reflect.DeepEqual(names(t, body), []string{"agent:plan", "in-progress"}) {
		t.Fatalf("add: %d %s", code, body)
	}
	code, body, _ = call(t, ts, "DELETE", "/repos/o/r/issues/1/labels/"+url.PathEscape("agent:plan"), "")
	if code != 200 || !reflect.DeepEqual(names(t, body), []string{"in-progress"}) {
		t.Fatalf("remove: %d %s", code, body)
	}
	if code, _, _ = call(t, ts, "DELETE", "/repos/o/r/issues/1/labels/agent:plan", ""); code != 404 {
		t.Errorf("removing an absent label: %d, want 404", code)
	}
	code, body, _ = call(t, ts, "GET", "/repos/o/r/issues/1/labels?per_page=100", "")
	if code != 200 || !reflect.DeepEqual(names(t, body), []string{"in-progress"}) {
		t.Errorf("list: %d %s", code, body)
	}

	var got []string
	for _, e := range s.LabelEvents("o/r", n) {
		got = append(got, map[bool]string{true: "+", false: "-"}[e.Added]+e.Label)
	}
	if want := []string{"+in-progress", "-agent:plan"}; !reflect.DeepEqual(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
}

func TestAddLabelCreatesRepoLabel(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t"})
	call(t, ts, "POST", "/repos/o/r/issues/1/labels", `{"labels":["fresh"]}`)
	_, body, _ := call(t, ts, "GET", "/repos/o/r/labels?per_page=100", "")
	if !reflect.DeepEqual(names(t, body), []string{"agent:plan", "in-progress", "fresh"}) {
		t.Errorf("repo labels %s", body)
	}
}

func TestComments(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t"})
	code, _, _ := call(t, ts, "POST", "/repos/o/r/issues/1/comments", `{"body":"hello"}`)
	if code != 201 {
		t.Fatalf("post: %d", code)
	}
	_, body, _ := call(t, ts, "GET", "/repos/o/r/issues/1/comments?per_page=50", "")
	var list []struct {
		Body string
		User struct{ Login string }
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 1 || list[0].Body != "hello" || list[0].User.Login != "me" {
		t.Errorf("list %s (%v)", body, err)
	}
	if code, _, _ = call(t, ts, "POST", "/repos/o/r/issues/1/comments", `{}`); code != 422 {
		t.Errorf("empty comment: %d, want 422", code)
	}
}

func TestIssueETag(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t"})
	_, _, h := call(t, ts, "GET", "/repos/o/r/issues/1", "")
	etag := h.Get("ETag")
	if code, _, _ := call(t, ts, "GET", "/repos/o/r/issues/1", "", "If-None-Match", etag); code != 304 {
		t.Errorf("unchanged: %d, want 304", code)
	}
	call(t, ts, "POST", "/repos/o/r/issues/1/labels", `{"labels":["x"]}`)
	if code, _, _ := call(t, ts, "GET", "/repos/o/r/issues/1", "", "If-None-Match", etag); code != 200 {
		t.Errorf("after a label write: %d, want 200", code)
	}
}

func TestSearchAndPagination(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "one", Labels: []string{"agent:plan"}, Assignees: []string{"me"}})
	s.Add(Item{Repo: "o/r", Title: "blocked", Labels: []string{"agent:plan", "needs-human"}, Assignees: []string{"me"}})
	s.Add(Item{Repo: "o/r", Title: "pr", PR: true, Labels: []string{"agent:plan"}, Assignees: []string{"me"}})
	s.Add(Item{Repo: "o/r", Title: "two", Labels: []string{"agent:plan"}, Assignees: []string{"me"}})

	q := url.Values{"q": {"repo:o/r type:issue state:open label:agent:plan,agent:build -label:needs-human assignee:@me"}, "per_page": {"1"}}
	code, body, h := call(t, ts, "GET", "/search/issues?"+q.Encode(), "")
	if code != 200 {
		t.Fatalf("search: %d %s", code, body)
	}
	var res struct {
		TotalCount int `json:"total_count"`
		Items      []struct{ Title string }
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil || res.TotalCount != 2 || len(res.Items) != 1 || res.Items[0].Title != "one" {
		t.Fatalf("first page %s (%v)", body, err)
	}
	next := h.Get("Link")
	if !strings.Contains(next, `rel="next"`) {
		t.Fatalf("no next link: %q", next)
	}
	link := next[1:strings.Index(next, ">")]
	u, _ := url.Parse(link)
	_, body, h = call(t, ts, "GET", u.RequestURI(), "")
	if err := json.Unmarshal([]byte(body), &res); err != nil || len(res.Items) != 1 || res.Items[0].Title != "two" || h.Get("Link") != "" {
		t.Errorf("second page %s (%v) link %q", body, err, h.Get("Link"))
	}

	q.Set("q", "repo:o/r type:pr state:open label:agent:plan")
	_, body, _ = call(t, ts, "GET", "/search/issues?"+q.Encode(), "")
	if json.Unmarshal([]byte(body), &res); res.TotalCount != 1 || res.Items[0].Title != "pr" {
		t.Errorf("pr search %s", body)
	}
}

func TestIssuesListSkipsNothingButClosed(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "open"})
	s.Add(Item{Repo: "o/r", Title: "pr", PR: true})
	s.Add(Item{Repo: "o/r", Title: "closed", State: "closed"})
	_, body, _ := call(t, ts, "GET", "/repos/o/r/issues?state=open&sort=created&direction=asc&per_page=50", "")
	var list []struct {
		Title string
		PR    *struct{} `json:"pull_request"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 2 || list[1].PR == nil || list[0].PR != nil {
		t.Errorf("issues %s (%v)", body, err)
	}
	_, body, _ = call(t, ts, "GET", "/repos/o/r/pulls?state=open&sort=created&direction=asc&per_page=50", "")
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 1 || list[0].Title != "pr" {
		t.Errorf("pulls %s (%v)", body, err)
	}
}

func TestPatchState(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t"})
	if code, _, _ := call(t, ts, "PATCH", "/repos/o/r/issues/1", `{"state":"closed","state_reason":"completed"}`); code != 200 {
		t.Fatalf("patch: %d", code)
	}
	if it, _ := s.Get("o/r", 1); it.State != "closed" {
		t.Errorf("state %q", it.State)
	}
}

func TestUnsupportedIsLoud(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t"})
	for _, tc := range []struct{ method, path string }{
		{"GET", "/search/issues?q=" + url.QueryEscape("repo:o/r draft")},
		{"GET", "/search/issues?q=" + url.QueryEscape("repo:o/r sort:updated")},
		{"GET", "/repos/o/r/issues?labels=x"},
		{"PUT", "/repos/o/r/issues/1/labels"},
		{"GET", "/repos/o/r/issues/1/timeline"},
	} {
		if code, _, _ := call(t, ts, tc.method, tc.path, "{}"); code != http.StatusUnprocessableEntity {
			t.Errorf("%s %s: %d, want 422", tc.method, tc.path, code)
		}
	}
	if got := len(s.Unexpected()); got != 5 {
		t.Errorf("Unexpected has %d entries, want 5: %v", got, s.Unexpected())
	}
}

func TestMissingResourcesAre404(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t"})
	for _, p := range []string{"/repos/o/r/issues/9", "/repos/x/y/labels", "/repos/o/r/issues/1/parent", "/repos/o/r/pulls/1"} {
		if code, _, _ := call(t, ts, "GET", p, ""); code != 404 {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	if code, body, _ := call(t, ts, "GET", "/repos/o/r/issues/1/dependencies/blocked_by?per_page=50", ""); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("blocked_by: %d %s", code, body)
	}
	if got := s.Unexpected(); len(got) != 0 {
		t.Errorf("unexpected: %v", got)
	}
}
