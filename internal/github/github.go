// Package github is a client for the few GitHub REST endpoints Jigz uses
// to enrich run history and to post answers. It sends GET, POST and DELETE
// requests.
//
// Each call names a key, the project the token belongs to, so rate limit
// back-off applies per token. Error values never contain the token.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// UserAgent is sent with every request, which GitHub requires.
const UserAgent = "Jigz"

const maxBody = 8 << 20

// Client sends requests to one API root.
type Client struct {
	// BaseURL is the API root without a trailing slash.
	BaseURL string
	// HTTP is the transport; nil uses a client with a 20 second timeout.
	HTTP *http.Client
	// Now is the clock; nil uses time.Now.
	Now func() time.Time

	mu    sync.Mutex
	until map[string]time.Time
	users map[string]string
}

// Response is a successful reply. A 304 has NotModified set and no body.
type Response struct {
	Body        []byte
	ETag        string
	NotModified bool
}

// Error is a reply with a status other than 200 or 304.
type Error struct{ Status int }

// ErrNotFound matches a 404 reply. A DELETE that reports it means the
// resource is already gone and counts as success for the caller.
var ErrNotFound = errors.New("not found")

// Is reports whether target is ErrNotFound and the reply was a 404.
func (e *Error) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

func (e *Error) Error() string {
	return fmt.Sprintf("GitHub answered %d %s", e.Status, http.StatusText(e.Status))
}

// RateLimitError means no request may be sent for this key until Until.
type RateLimitError struct{ Until time.Time }

func (e *RateLimitError) Error() string {
	return "GitHub rate limit reached, waiting until " + e.Until.UTC().Format("15:04:05") + " UTC"
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// LimitedUntil reports whether key is backing off, and until when.
func (c *Client) LimitedUntil(key string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.until[key]
	return t, ok && c.now().Before(t)
}

func (c *Client) backOff(key string, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.until == nil {
		c.until = map[string]time.Time{}
	}
	c.until[key] = t
}

// Get fetches path, which starts with "/", with token. A non-empty etag is
// sent as If-None-Match.
func (c *Client) Get(ctx context.Context, key, token, path, etag string) (Response, error) {
	return c.do(ctx, key, token, http.MethodGet, path, nil, etag)
}

// Post sends body as JSON to path, which starts with "/", with token.
func (c *Client) Post(ctx context.Context, key, token, path string, body []byte) (Response, error) {
	return c.do(ctx, key, token, http.MethodPost, path, body, "")
}

// Delete removes the resource at path, which starts with "/", with token.
// A 404 is reported as ErrNotFound: the resource is already gone.
func (c *Client) Delete(ctx context.Context, key, token, path string) error {
	_, err := c.do(ctx, key, token, http.MethodDelete, path, nil, "")
	return err
}

// User returns the login of the token's account, cached per key.
func (c *Client) User(ctx context.Context, key, token string) (string, error) {
	c.mu.Lock()
	login, ok := c.users[key]
	c.mu.Unlock()
	if ok {
		return login, nil
	}
	resp, err := c.Get(ctx, key, token, "/user", "")
	if err != nil {
		return "", err
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(resp.Body, &u); err != nil {
		return "", fmt.Errorf("GitHub response: %w", err)
	}
	if u.Login == "" {
		return "", errors.New("GitHub response: no login")
	}
	c.mu.Lock()
	if c.users == nil {
		c.users = map[string]string{}
	}
	c.users[key] = u.Login
	c.mu.Unlock()
	return u.Login, nil
}

func (c *Client) do(ctx context.Context, key, token, method, path string, body []byte, etag string) (Response, error) {
	if t, limited := c.LimitedUntil(key); limited {
		return Response{}, &RateLimitError{t}
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	if method != http.MethodGet {
		noRedirect := *hc
		noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		hc = &noRedirect
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("GitHub request failed: %w", urlError(err))
	}
	defer resp.Body.Close()
	limited := c.noteLimits(key, resp)
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return Response{NotModified: true, ETag: etag}, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return Response{}, fmt.Errorf("GitHub response: %w", err)
		}
		return Response{Body: body, ETag: resp.Header.Get("ETag")}, nil
	}
	if limited != nil && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) {
		return Response{}, limited
	}
	return Response{}, &Error{Status: resp.StatusCode}
}

// noteLimits starts a back-off when the reply says the limit is used up or
// carries Retry-After, and returns it.
func (c *Client) noteLimits(key string, resp *http.Response) *RateLimitError {
	var until time.Time
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if n, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			until = time.Unix(n, 0)
		}
	}
	if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n >= 0 {
		if t := c.now().Add(time.Duration(n) * time.Second); t.After(until) {
			until = t
		}
	}
	if until.IsZero() {
		return nil
	}
	c.backOff(key, until)
	return &RateLimitError{until}
}

// urlError drops the request URL from a transport error, which is all it
// adds to the underlying cause.
func urlError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
