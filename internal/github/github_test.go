package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGetSendsAReadOnlyAuthenticatedConditionalRequest(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	resp, err := c.Get(context.Background(), "p", "tok", "/repos/a/b/pulls/1", `"old"`)
	if err != nil || resp.ETag != `"v1"` || string(resp.Body) != "{}" {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	if got.Method != http.MethodGet || got.URL.Path != "/repos/a/b/pulls/1" {
		t.Errorf("request = %s %s", got.Method, got.URL.Path)
	}
	for h, want := range map[string]string{"Authorization": "Bearer tok", "User-Agent": UserAgent, "If-None-Match": `"old"`} {
		if got.Header.Get(h) != want {
			t.Errorf("%s = %q, want %q", h, got.Header.Get(h), want)
		}
	}
}

func TestNotModifiedIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotModified) }))
	defer srv.Close()
	resp, err := (&Client{BaseURL: srv.URL}).Get(context.Background(), "p", "t", "/x", `"e"`)
	if err != nil || !resp.NotModified || resp.ETag != `"e"` {
		t.Errorf("resp = %+v, %v", resp, err)
	}
}

func TestErrorStatusesAreReported(t *testing.T) {
	for _, code := range []int{401, 403, 404, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		_, err := (&Client{BaseURL: srv.URL}).Get(context.Background(), "p", "t", "/x", "")
		srv.Close()
		var e *Error
		if !errors.As(err, &e) || e.Status != code {
			t.Errorf("%d: err = %v", code, err)
		}
	}
}

func TestRateLimitBacksOffUntilReset(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(30*time.Minute).Unix(), 10))
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Now: func() time.Time { return now }}
	if _, err := c.Get(context.Background(), "p", "t", "/x", ""); err != nil {
		t.Fatal(err)
	}
	var rl *RateLimitError
	if _, err := c.Get(context.Background(), "p", "t", "/x", ""); !errors.As(err, &rl) || hits != 1 {
		t.Fatalf("second call = %v after %d hits, want a rate limit error without a request", err, hits)
	}
	if _, err := c.Get(context.Background(), "other", "t", "/x", ""); err != nil {
		t.Errorf("another key is limited too: %v", err)
	}
	now = now.Add(31 * time.Minute)
	if _, err := c.Get(context.Background(), "p", "t", "/x", ""); err != nil {
		t.Errorf("after the reset: %v", err)
	}
}

func TestRetryAfterBacksOff(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Now: func() time.Time { return now }}
	var rl *RateLimitError
	if _, err := c.Get(context.Background(), "p", "t", "/x", ""); !errors.As(err, &rl) || !rl.Until.Equal(now.Add(time.Minute)) {
		t.Fatalf("err = %v", err)
	}
	if _, limited := c.LimitedUntil("p"); !limited {
		t.Error("not limited after Retry-After")
	}
}

func TestTransportErrorsDoNotCarryTheToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := &Client{BaseURL: url}
	_, err := c.Get(context.Background(), "p", "secret-token", "/repos/a/b", "")
	if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), url) {
		t.Errorf("err = %v", err)
	}
	if _, err := c.Post(context.Background(), "p", "secret-token", "/repos/a/b/comments", []byte(`{}`)); err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), url) {
		t.Errorf("Post err = %v", err)
	}
	if err := c.Delete(context.Background(), "p", "secret-token", "/repos/a/b/labels/x"); err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), url) {
		t.Errorf("Delete err = %v", err)
	}
}

func TestPostSendsJSONWithTheSameHeaders(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	resp, err := (&Client{BaseURL: srv.URL}).Post(context.Background(), "p", "tok", "/repos/a/b/issues/1/comments", []byte(`{"body":"hi"}`))
	if err != nil || string(resp.Body) != `{"id":1}` {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/repos/a/b/issues/1/comments" {
		t.Errorf("request = %s %s", got.Method, got.URL.Path)
	}
	if string(body) != `{"body":"hi"}` {
		t.Errorf("body = %q", body)
	}
	for h, want := range map[string]string{
		"Authorization":        "Bearer tok",
		"User-Agent":           UserAgent,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"Content-Type":         "application/json",
	} {
		if got.Header.Get(h) != want {
			t.Errorf("%s = %q, want %q", h, got.Header.Get(h), want)
		}
	}
}

func TestDeleteTreatsNotFoundAsErrNotFound(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	err := (&Client{BaseURL: srv.URL}).Delete(context.Background(), "p", "tok", "/repos/a/b/issues/1/labels/needs-human")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var ge *Error
	if !errors.As(err, &ge) || ge.Status != http.StatusNotFound {
		t.Errorf("err = %v, want *Error 404", err)
	}
	if got.Method != http.MethodDelete {
		t.Errorf("request = %s", got.Method)
	}
}

func TestDeleteNoContentIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := (&Client{BaseURL: srv.URL}).Delete(context.Background(), "p", "tok", "/x"); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestPostValidationErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Validation Failed"}`))
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL}).Post(context.Background(), "p", "tok", "/x", []byte(`{}`))
	var ge *Error
	if !errors.As(err, &ge) || ge.Status != http.StatusUnprocessableEntity {
		t.Fatalf("err = %v, want *Error 422", err)
	}
}

func TestBackOffBlocksAllMethods(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Now: func() time.Time { return now }}
	if _, err := c.Get(context.Background(), "p", "t", "/x", ""); err == nil {
		t.Fatal("Get succeeded")
	}
	var rl *RateLimitError
	if _, err := c.Post(context.Background(), "p", "t", "/x", []byte(`{}`)); !errors.As(err, &rl) {
		t.Errorf("Post err = %v, want *RateLimitError", err)
	}
	if err := c.Delete(context.Background(), "p", "t", "/x"); !errors.As(err, &rl) {
		t.Errorf("Delete err = %v, want *RateLimitError", err)
	}
}

func TestUserIsCachedPerKey(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/user" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"login":"octobot"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	for i := 0; i < 2; i++ {
		login, err := c.User(context.Background(), "p", "tok")
		if err != nil || login != "octobot" {
			t.Fatalf("User = %q, %v", login, err)
		}
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
	if login, err := c.User(context.Background(), "q", "tok"); err != nil || login != "octobot" || hits != 2 {
		t.Errorf("User(q) = %q, %v, hits = %d", login, err, hits)
	}
}

func TestWritesDoNotFollowRedirects(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	_, err := c.Post(context.Background(), "p", "t", "/moved", []byte(`{}`))
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusMovedPermanently {
		t.Errorf("post err = %v, want 301", err)
	}
	err = c.Delete(context.Background(), "p", "t", "/moved")
	if !errors.As(err, &e) || e.Status != http.StatusMovedPermanently {
		t.Errorf("delete err = %v, want 301", err)
	}
	if hits != 2 {
		t.Errorf("server hit %d times, want 2", hits)
	}
}
