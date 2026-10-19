package github

import (
	"context"
	"errors"
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
	_, err := (&Client{BaseURL: url}).Get(context.Background(), "p", "secret-token", "/repos/a/b", "")
	if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), url) {
		t.Errorf("err = %v", err)
	}
}
