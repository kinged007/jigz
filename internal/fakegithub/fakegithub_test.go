package fakegithub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
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

func eventStrings(events []LabelEvent) []string {
	var out []string
	for _, e := range events {
		prefix := "-"
		if e.Added {
			prefix = "+"
		}
		out = append(out, prefix+e.Label)
	}
	return out
}

func TestDeleteEncodedLabelName(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t", Labels: []string{"agent:done", "needs-human"}})
	code, body, _ := call(t, ts, "DELETE", "/repos/o/r/issues/1/labels/agent%3Adone", "")
	if code != 200 || !reflect.DeepEqual(names(t, body), []string{"needs-human"}) {
		t.Fatalf("delete agent%%3Adone: %d %s", code, body)
	}
	if !reflect.DeepEqual(eventStrings(s.LabelEvents("o/r", 1)), []string{"-agent:done"}) {
		t.Errorf("events %v", s.LabelEvents("o/r", 1))
	}
	if code, _, _ = call(t, ts, "DELETE", "/repos/o/r/issues/1/labels/agent%3Adone", ""); code != 404 {
		t.Errorf("missing label: %d, want 404", code)
	}
}

func TestEngineHandoff(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t", Labels: []string{"agent:plan", "in-progress", "needs-human"}})
	s.EngineHandoff("o/r", 1, "agent:plan", "in-progress")

	if it, _ := s.Get("o/r", 1); !reflect.DeepEqual(it.Labels, []string{"needs-human", "agent:done"}) {
		t.Errorf("labels %v", it.Labels)
	}
	want := []string{"+agent:done", "-agent:plan", "-in-progress"}
	if got := eventStrings(s.LabelEvents("o/r", 1)); !reflect.DeepEqual(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}

	_, body, _ := call(t, ts, "GET", "/repos/o/r/issues/1/labels?per_page=100", "")
	if !reflect.DeepEqual(names(t, body), []string{"needs-human", "agent:done"}) {
		t.Errorf("labels via GET %s", body)
	}
	for q, want := range map[string]int{
		"repo:o/r state:open label:agent:done -label:needs-human": 0,
		"repo:o/r state:open label:agent:done label:needs-human":  1,
	} {
		code, body, _ := call(t, ts, "GET", "/search/issues?"+url.Values{"q": {q}, "per_page": {"10"}}.Encode(), "")
		var res struct {
			TotalCount int `json:"total_count"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &res) != nil || res.TotalCount != want {
			t.Errorf("search %q: %d %s, want %d", q, code, body, want)
		}
	}
}

func TestLabelRequestSequence(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t", Labels: []string{"agent:done"}})
	s.Add(Item{Repo: "o/r", Title: "other", Labels: []string{"agent:done"}})
	call(t, ts, "DELETE", "/repos/o/r/issues/1/labels/agent%3Adone", "")
	call(t, ts, "POST", "/repos/o/r/issues/1/labels", `{"labels":["agent:plan","needs-human"]}`)
	call(t, ts, "PUT", "/repos/o/r/issues/1/labels", `{"labels":["agent:plan"]}`)
	call(t, ts, "DELETE", "/repos/o/r/issues/2/labels/agent%3Adone", "")
	s.Add(Item{Repo: "o/r", Title: "slash", Labels: []string{"a/b"}})
	call(t, ts, "DELETE", "/repos/o/r/issues/3/labels/a%2Fb", "")
	if got := s.LabelRequests("o/r", 3); len(got) != 1 || got[0].Path != "/repos/o/r/issues/3/labels/a%2Fb" || got[0].Status != 200 {
		t.Errorf("slash label requests = %+v", got)
	}

	var got []string
	for _, r := range s.LabelRequests("o/r", 1) {
		got = append(got, r.Method+" "+r.Path+" "+r.Body)
	}
	want := []string{
		"DELETE /repos/o/r/issues/1/labels/agent%3Adone ",
		"POST /repos/o/r/issues/1/labels {\"labels\":[\"agent:plan\",\"needs-human\"]}",
		"PUT /repos/o/r/issues/1/labels {\"labels\":[\"agent:plan\"]}",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requests %q, want %q", got, want)
	}
	if u := s.Unexpected(); len(u) != 1 || !strings.Contains(u[0], "PUT") {
		t.Errorf("unexpected %v", u)
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
		{"GET", "/user?x=1"},
	} {
		if code, _, _ := call(t, ts, tc.method, tc.path, "{}"); code != http.StatusUnprocessableEntity {
			t.Errorf("%s %s: %d, want 422", tc.method, tc.path, code)
		}
	}
	if got := len(s.Unexpected()); got != 6 {
		t.Errorf("Unexpected has %d entries, want 6: %v", got, s.Unexpected())
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

func TestGetUser(t *testing.T) {
	_, ts := newServer(t)
	code, body, _ := call(t, ts, "GET", "/user", "")
	if code != 200 || strings.TrimSpace(body) != `{"login":"me"}` {
		t.Errorf("user: %d %s", code, body)
	}
}

func TestSeededCommentFields(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t", PR: true})
	id := s.AddComment("o/r", 1, Comment{
		Author: "alice", Association: "CONTRIBUTOR", Body: "b",
		Created: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC),
		Updated: time.Date(2024, 5, 2, 11, 0, 0, 0, time.UTC),
	})
	_, body, _ := call(t, ts, "GET", "/repos/o/r/issues/1/comments?per_page=100", "")
	var list []struct {
		ID      int64 `json:"id"`
		User    struct{ Login string }
		Assoc   string `json:"author_association"`
		URL     string `json:"html_url"`
		Created string `json:"created_at"`
		Updated string `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("comments: %s", body)
	}
	got := list[0]
	if got.ID != id || got.User.Login != "alice" || got.Assoc != "CONTRIBUTOR" {
		t.Errorf("comment: %+v", got)
	}
	if got.Created != "2024-05-01T10:00:00Z" || got.Updated != "2024-05-02T11:00:00Z" {
		t.Errorf("times: %s %s", got.Created, got.Updated)
	}
	if want := fmt.Sprintf("https://github.com/o/r/pull/1#issuecomment-%d", id); got.URL != want {
		t.Errorf("html_url: %s, want %s", got.URL, want)
	}
}

func TestPostedComment(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t"})
	code, body, _ := call(t, ts, "POST", "/repos/o/r/issues/1/comments", `{"body":"hello"}`)
	if code != 201 {
		t.Fatalf("post: %d %s", code, body)
	}
	var got struct {
		User    struct{ Login string }
		Assoc   string `json:"author_association"`
		URL     string `json:"html_url"`
		Created string `json:"created_at"`
		Updated string `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.User.Login != "me" || got.Assoc != "OWNER" {
		t.Errorf("comment: %+v", got)
	}
	if got.Created != got.Updated {
		t.Errorf("times: %s %s", got.Created, got.Updated)
	}
	if want := "https://github.com/o/r/issues/1#issuecomment-"; !strings.HasPrefix(got.URL, want) {
		t.Errorf("html_url: %s, want prefix %s", got.URL, want)
	}
}

func TestCommentsETag(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t"})
	code, _, h := call(t, ts, "GET", "/repos/o/r/issues/1/comments", "")
	if code != 200 {
		t.Fatalf("comments: %d", code)
	}
	tag := h.Get("ETag")
	if tag == "" {
		t.Fatal("missing ETag")
	}
	if code, _, _ := call(t, ts, "GET", "/repos/o/r/issues/1/comments", "", "If-None-Match", tag); code != 304 {
		t.Errorf("cached: %d, want 304", code)
	}
	s.AddComment("o/r", 1, Comment{Author: "alice", Body: "b"})
	code, _, h = call(t, ts, "GET", "/repos/o/r/issues/1/comments", "", "If-None-Match", tag)
	if code != 200 {
		t.Errorf("after comment: %d, want 200", code)
	}
	if h.Get("ETag") == tag {
		t.Errorf("ETag did not change: %s", tag)
	}
}

func TestCommentsETagIgnoresLabelChangesAndTracksPage(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t", Labels: []string{"x"}})
	_, _, h := call(t, ts, "GET", "/repos/o/r/issues/1/comments?per_page=1", "")
	tag := h.Get("ETag")
	call(t, ts, "POST", "/repos/o/r/issues/1/labels", `{"labels":["y"]}`)
	if code, _, _ := call(t, ts, "GET", "/repos/o/r/issues/1/comments?per_page=1", "", "If-None-Match", tag); code != 304 {
		t.Errorf("after label change: %d, want 304", code)
	}
	if code, _, _ := call(t, ts, "GET", "/repos/o/r/issues/1/comments?per_page=2", "", "If-None-Match", tag); code != 200 {
		t.Errorf("other per_page: %d, want 200", code)
	}
	call(t, ts, "POST", "/repos/o/r/issues/1/comments", `{"body":"hi"}`)
	if code, _, _ := call(t, ts, "GET", "/repos/o/r/issues/1/comments?per_page=1", "", "If-None-Match", tag); code != 200 {
		t.Errorf("after comment: %d, want 200", code)
	}
}

func TestSetAssignees(t *testing.T) {
	s, ts := newServer(t)
	s.Add(Item{Repo: "o/r", Title: "t"})
	s.SetAssignees("o/r", 1, "me", "alice")
	_, body, _ := call(t, ts, "GET", "/repos/o/r/issues/1", "")
	var it struct {
		Assignees []struct{ Login string }
	}
	if err := json.Unmarshal([]byte(body), &it); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range it.Assignees {
		names = append(names, a.Login)
	}
	if !reflect.DeepEqual(names, []string{"me", "alice"}) {
		t.Errorf("assignees: %v", names)
	}
	q := url.Values{"q": {"repo:o/r assignee:alice"}, "per_page": {"10"}}
	code, body, _ := call(t, ts, "GET", "/search/issues?"+q.Encode(), "")
	var res struct {
		TotalCount int `json:"total_count"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &res) != nil || res.TotalCount != 1 {
		t.Errorf("search: %d %s", code, body)
	}
}
