// Package hubclient is the daemon's client for the hub protocol v0: the HTTPS
// requests (enroll, config, status, runs) and the WebSocket command channel.
// Errors name the method and route only; hub text in them is redacted, and no
// header value is ever part of an error or log line.
package hubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/hubproto"
)

const (
	basePath        = "api/daemon/v0"
	maxResponseSize = 32 << 20
	defaultTimeout  = 30 * time.Second
	redacted        = "[redacted]"
)

var (
	// ErrRevoked matches an *Error for a 401 or 403 answer: the credential
	// is missing, rotated out or revoked.
	ErrRevoked = errors.New("hub: credential refused")
	// ErrUnsupportedVersion matches an *Error for a 400 unsupported_version
	// answer; Error.Supported names the range the hub serves.
	ErrUnsupportedVersion = errors.New("hub: protocol version not supported")
)

// Client talks to one hub. Set the fields before the first call; a Client is
// safe for concurrent use after that.
type Client struct {
	// URL is the hub's base URL as given to `jig join` (https://host).
	URL string
	// DaemonID and Credential come from enrollment; Enroll does not use them.
	DaemonID   string
	Credential string
	// Version is the jig version, sent as Jig-Version.
	Version string
	// Secrets are further values removed from error text and log lines,
	// such as the tracker token.
	Secrets []string
	// HTTP is the client for requests. Default: 30 s timeout.
	HTTP *http.Client
	// Logf receives log lines. Default: none.
	Logf func(format string, args ...any)

	// Sleep, Now and Rand replace the clock and the jitter source in tests.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
	Rand  func() float64
}

// Error is a failed hub request.
type Error struct {
	Method string
	Route  string
	// Status is the HTTP status, 0 when no response arrived.
	Status int
	// Code and Supported are from the hub's error body when it had one.
	Code      hubproto.ErrorCode
	Supported *hubproto.Supported
	retry     bool
	text      string
	err       error
}

func (e *Error) Error() string { return e.text }

func (e *Error) Unwrap() error { return e.err }

func (e *Error) Is(target error) bool {
	switch target {
	case ErrRevoked:
		return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
	case ErrUnsupportedVersion:
		return e.Status == http.StatusBadRequest && e.Code == hubproto.ErrUnsupportedVersion
	}
	return false
}

// Retryable reports whether err is worth retrying with back-off: a network
// failure, a 5xx answer or a response that does not fit the protocol. A 401,
// 403, any other 4xx and a cancelled context are not.
func Retryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.retry
}

// Redact replaces the credential and Secrets in s.
func (c *Client) Redact(s string) string {
	return redact(s, append([]string{c.Credential}, c.Secrets...))
}

func redact(s string, secrets []string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, redacted)
		}
	}
	return s
}

// Enroll exchanges the one-time token in req for a daemon id and credential.
// It sends neither Authorization nor Jig-Daemon-Id.
func (c *Client) Enroll(ctx context.Context, req hubproto.EnrollRequest) (hubproto.EnrollResponse, error) {
	var resp hubproto.EnrollResponse
	if err := req.Validate(); err != nil {
		return resp, c.fail("POST", "enroll", false, err, req.Token)
	}
	_, _, err := c.do(ctx, "POST", "enroll", req, nil, &resp, req.Token)
	return resp, err
}

// GetConfig fetches the config. With etag set it sends If-None-Match, and a
// 304 answer returns a nil config and etag unchanged. Otherwise it returns
// the config and its ETag.
func (c *Client) GetConfig(ctx context.Context, etag string) (*hubproto.ConfigResponse, string, error) {
	h := http.Header{}
	if etag != "" {
		h.Set("If-None-Match", etag)
	}
	var cfg hubproto.ConfigResponse
	status, rh, err := c.do(ctx, "GET", "config", nil, h, &cfg)
	if err != nil {
		return nil, "", err
	}
	if status == http.StatusNotModified {
		return nil, etag, nil
	}
	if rh.Get("ETag") == "" {
		return nil, "", c.fail("GET", "config", true, errors.New("response has no ETag"))
	}
	return &cfg, rh.Get("ETag"), nil
}

// PostStatus sends a heartbeat and returns the next interval and the hub's
// config ETag.
func (c *Client) PostStatus(ctx context.Context, req hubproto.StatusRequest) (hubproto.StatusResponse, error) {
	var resp hubproto.StatusResponse
	if err := req.Validate(); err != nil {
		return resp, c.fail("POST", "status", false, err)
	}
	_, _, err := c.do(ctx, "POST", "status", req, nil, &resp)
	return resp, err
}

// PostRuns sends 1 to 100 run events and returns the ids the hub accepted.
func (c *Client) PostRuns(ctx context.Context, events []hubproto.RunEvent) ([]string, error) {
	req := hubproto.RunsRequest{Events: events}
	if err := req.Validate(); err != nil {
		return nil, c.fail("POST", "runs", false, err)
	}
	var resp hubproto.RunsResponse
	_, _, err := c.do(ctx, "POST", "runs", req, nil, &resp)
	return resp.Accepted, err
}

// Retry runs op until it succeeds, returns an error that is not Retryable,
// or ctx ends, sleeping with exponential back-off and full jitter between
// attempts.
func (c *Client) Retry(ctx context.Context, op func(context.Context) error) error {
	b := c.backoff()
	for {
		err := op(ctx)
		if err == nil || !Retryable(err) {
			return err
		}
		d := b.Next()
		c.logf("%v; retrying in %s", err, d)
		if err := c.sleep(ctx, d); err != nil {
			return err
		}
	}
}

func (c *Client) endpoint(route string) (*url.URL, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid hub URL")
	}
	u = u.JoinPath(basePath, route)
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u, nil
}

func (c *Client) header(enroll bool) http.Header {
	h := http.Header{}
	h.Set("Jig-Version", c.Version)
	if !enroll {
		h.Set("Authorization", "Bearer "+c.Credential)
		h.Set("Jig-Daemon-Id", c.DaemonID)
	}
	return h
}

// do sends one request. A 2xx body is decoded strictly into out and
// validated; 304 is returned as is.
func (c *Client) do(ctx context.Context, method, route string, body any, extra http.Header, out interface{ Validate() error }, secrets ...string) (int, http.Header, error) {
	u, err := c.endpoint(route)
	if err != nil {
		return 0, nil, c.fail(method, route, false, err, secrets...)
	}
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, c.fail(method, route, false, err, secrets...)
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, c.fail(method, route, false, err, secrets...)
	}
	req.Header = c.header(route == "enroll")
	for k, v := range extra {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, c.fail(method, route, false, ctx.Err(), secrets...)
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, nil, c.fail(method, route, true, err, secrets...)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return 0, nil, c.fail(method, route, true, err, secrets...)
	}
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return resp.StatusCode, resp.Header, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		err := dec.Decode(out)
		if err == nil {
			err = out.Validate()
		}
		if err != nil {
			return 0, nil, c.fail(method, route, true, fmt.Errorf("invalid response: %w", err), secrets...)
		}
		return resp.StatusCode, resp.Header, nil
	}
	return 0, nil, c.statusError(method, route, resp.StatusCode, data, secrets...)
}

// httpClient returns c.HTTP, or the default client, with redirects refused:
// a followed redirect would resend the enrollment token or the credential to
// another URL, so a 3xx is returned as a status error.
func (c *Client) httpClient() *http.Client {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &noRedirect
}

func (c *Client) statusError(method, route string, status int, body []byte, secrets ...string) *Error {
	e := &Error{Method: method, Route: route, Status: status, retry: status >= 500}
	var eb hubproto.ErrorBody
	text := fmt.Sprintf("hub: %s %s: status %d", method, route, status)
	if json.Unmarshal(body, &eb) == nil && eb.Code != "" {
		e.Code, e.Supported = eb.Code, eb.Supported
		text += fmt.Sprintf(" %s: %s", eb.Code, eb.Message)
	}
	e.text = redact(text, append([]string{c.Credential}, append(c.Secrets, secrets...)...))
	return e
}

func (c *Client) fail(method, route string, retry bool, err error, secrets ...string) *Error {
	text := fmt.Sprintf("hub: %s %s: %v", method, route, err)
	return &Error{
		Method: method, Route: route, retry: retry, err: err,
		text: redact(text, append([]string{c.Credential}, append(c.Secrets, secrets...)...)),
	}
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf("%s", c.Redact(fmt.Sprintf(format, args...)))
	}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) backoff() *Backoff {
	r := c.Rand
	if r == nil {
		r = rand.Float64
	}
	return &Backoff{Min: BackoffMin, Max: BackoffMax, Rand: r}
}

// Back-off bounds from the protocol (1.2).
const (
	BackoffMin = time.Second
	BackoffMax = 60 * time.Second
)

// Backoff yields exponential delays with full jitter: attempt n waits a
// random time in [0, min(Max, Min*2^n)].
type Backoff struct {
	Min, Max time.Duration
	Rand     func() float64
	n        int
}

// Next returns the delay before the next attempt.
func (b *Backoff) Next() time.Duration {
	ceil := b.Max
	if b.n < 32 {
		ceil = min(b.Max, b.Min<<b.n)
	}
	b.n++
	return time.Duration(b.Rand() * float64(ceil))
}

// Reset starts the sequence again at Min.
func (b *Backoff) Reset() { b.n = 0 }
