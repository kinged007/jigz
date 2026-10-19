// Package github is a read-only client for the few GitHub REST endpoints
// central uses to enrich run history. It only sends GET requests.
//
// Each call names a key, the project the token belongs to, so rate limit
// back-off applies per token. Error values never contain the token.
package github

import (
	"context"
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
const UserAgent = "sortie-loop"

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
}

// Response is a successful reply. A 304 has NotModified set and no body.
type Response struct {
	Body        []byte
	ETag        string
	NotModified bool
}

// Error is a reply with a status other than 200 or 304.
type Error struct{ Status int }

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
	if t, limited := c.LimitedUntil(key); limited {
		return Response{}, &RateLimitError{t}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
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
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("GitHub request failed: %w", urlError(err))
	}
	defer resp.Body.Close()
	limited := c.noteLimits(key, resp)
	switch resp.StatusCode {
	case http.StatusNotModified:
		return Response{NotModified: true, ETag: etag}, nil
	case http.StatusOK:
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
