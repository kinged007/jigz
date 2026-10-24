package hubclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinged007/jigz/internal/fakehub"
	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/wsclient"
)

const (
	enrollToken = "enroll-CANARY-0b6c"
	sha         = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func enrollReq(token string) hubproto.EnrollRequest {
	return hubproto.EnrollRequest{
		Token: token, Name: "ci-1", Host: hubproto.Host{OS: "linux", Arch: "amd64", CPUs: 4},
		JigVersion: "1.2.3", EngineVersion: "1.28.1", TrackerLogin: "octo",
	}
}

func config(content string) hubproto.ConfigResponse {
	return hubproto.ConfigResponse{
		Member:   hubproto.Member{Login: "octo"},
		Projects: []hubproto.ConfigProject{{Provider: "github", Repo: "acme/widgets", Workflows: []string{"issues"}}},
		Files:    []hubproto.ConfigFile{{Path: "WORKFLOW.issues.md", SHA256: sha, Content: content}},
		Labels:   []hubproto.Label{},
	}
}

// enrolled starts a fake hub and returns a client enrolled with it.
func enrolled(t *testing.T) (*fakehub.Server, *Client) {
	t.Helper()
	hub := fakehub.New(enrollToken)
	ts := httptest.NewServer(hub)
	t.Cleanup(ts.Close)
	c := &Client{URL: ts.URL, Version: "1.2.3"}
	resp, err := c.Enroll(context.Background(), enrollReq(enrollToken))
	if err != nil {
		t.Fatal(err)
	}
	c.DaemonID, c.Credential = resp.DaemonID, resp.Credential
	if resp.RunnerLabel != "runner:ci-1" {
		t.Fatalf("runner label %q", resp.RunnerLabel)
	}
	t.Cleanup(func() {
		if u := hub.Unexpected(); len(u) > 0 {
			t.Errorf("fake hub saw protocol errors: %q", u)
		}
	})
	return hub, c
}

func status() hubproto.StatusRequest {
	return hubproto.StatusRequest{
		Time: time.Now(), Loops: []hubproto.Loop{}, Accounts: []hubproto.Account{},
		Capacity: &hubproto.Capacity{}, Hours: &hubproto.Hours{Open: true},
	}
}

func TestConfigETag(t *testing.T) {
	hub, c := enrolled(t)
	ctx := context.Background()
	first := hub.SetConfig(config("a"))

	cfg, etag, err := c.GetConfig(ctx, "")
	if err != nil || cfg == nil || etag != first || cfg.Files[0].Content != "a" {
		t.Fatalf("first fetch: %v %v %q", cfg, err, etag)
	}
	cfg, etag2, err := c.GetConfig(ctx, etag)
	if err != nil || cfg != nil || etag2 != etag {
		t.Fatalf("304 fetch: cfg %v err %v etag %q", cfg, err, etag2)
	}

	second := hub.SetConfig(config("b"))
	sr, err := c.PostStatus(ctx, status())
	if err != nil || sr.ConfigETag != second || sr.IntervalSeconds != 30 {
		t.Fatalf("status: %+v %v", sr, err)
	}
	cfg, etag3, err := c.GetConfig(ctx, etag)
	if err != nil || cfg == nil || etag3 != second || cfg.Files[0].Content != "b" {
		t.Fatalf("changed fetch: %v %v %q", cfg, err, etag3)
	}
}

func TestPostRuns(t *testing.T) {
	hub, c := enrolled(t)
	fin := time.Now()
	events := []hubproto.RunEvent{
		{ID: "r-1", Type: hubproto.EventStarted, Workflow: "issues", Item: hubproto.Item{Project: "acme/widgets", Number: 7, Kind: hubproto.KindIssue}, Attempt: 1, StartedAt: fin},
		{ID: "r-2", Type: hubproto.EventFinished, Workflow: "issues", Item: hubproto.Item{Project: "acme/widgets", Number: 7, Kind: hubproto.KindIssue}, Attempt: 1, StartedAt: fin, FinishedAt: &fin, Outcome: hubproto.OutcomeSucceeded, Usage: &hubproto.Usage{}},
	}
	got, err := c.PostRuns(context.Background(), events)
	if err != nil || strings.Join(got, ",") != "r-1,r-2" || len(hub.Runs()) != 2 {
		t.Fatalf("accepted %v err %v", got, err)
	}
	if _, err := c.PostRuns(context.Background(), nil); err == nil || Retryable(err) {
		t.Fatalf("empty batch: %v", err)
	}
}

func TestRevoked(t *testing.T) {
	hub, c := enrolled(t)
	hub.SetConfig(config("a"))
	hub.Revoke()
	_, _, err := c.GetConfig(context.Background(), "")
	if !errors.Is(err, ErrRevoked) || Retryable(err) {
		t.Fatalf("GetConfig after revoke: %v", err)
	}
	if _, err := c.PostStatus(context.Background(), status()); !errors.Is(err, ErrRevoked) {
		t.Fatalf("PostStatus after revoke: %v", err)
	}
	if _, err := c.Connect(context.Background()); !errors.Is(err, ErrRevoked) || Retryable(err) {
		t.Fatalf("Connect after revoke: %v", err)
	}
	err = c.Commands(context.Background(), func(context.Context, hubproto.Command) *hubproto.AckError { return nil })
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("Commands after revoke: %v", err)
	}
}

func TestErrorClasses(t *testing.T) {
	var mu sync.Mutex
	var code int
	var body string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	defer ts.Close()
	c := &Client{URL: ts.URL, Version: "1", DaemonID: "d", Credential: "cred"}
	for _, tc := range []struct {
		status            int
		body              string
		retry, revoked, v bool
	}{
		{500, `{"code":"internal","message":"x"}`, true, false, false},
		{503, `{"code":"unavailable","message":"x"}`, true, false, false},
		{502, `bad gateway`, true, false, false},
		{403, `{"code":"forbidden","message":"x"}`, false, true, false},
		{404, `{"code":"not_found","message":"x"}`, false, false, false},
		{400, `{"code":"unsupported_version","message":"x","supported":{"min":0,"max":0}}`, false, false, true},
		{200, `{"interval_seconds":30,"config_etag":"e","extra":1}`, true, false, false},
	} {
		mu.Lock()
		code, body = tc.status, tc.body
		mu.Unlock()
		_, err := c.PostStatus(context.Background(), status())
		if err == nil || Retryable(err) != tc.retry || errors.Is(err, ErrRevoked) != tc.revoked || errors.Is(err, ErrUnsupportedVersion) != tc.v {
			t.Errorf("status %d: err %v retryable %v", tc.status, err, Retryable(err))
		}
	}
	ts.Close()
	if _, err := c.PostStatus(context.Background(), status()); !Retryable(err) {
		t.Errorf("network failure not retryable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.PostStatus(ctx, status()); err == nil || Retryable(err) {
		t.Errorf("cancelled context retryable: %v", err)
	}
}

func TestRedirectsRefused(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer target.Close()
	for _, code := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		ts := httptest.NewServer(http.RedirectHandler(target.URL+"/v1/enroll", code))
		c := &Client{URL: ts.URL, Version: "1"}
		_, err := c.Enroll(context.Background(), enrollReq(enrollToken))
		ts.Close()
		if err == nil || Retryable(err) {
			t.Errorf("%d: err %v", code, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("redirect followed %d times", n)
	}
}

// TestNoSecretInErrors uses a hub that echoes every header and the request
// body into its error messages.
func TestNoSecretInErrors(t *testing.T) {
	const cred, tracker = "cred-CANARY-91f2", "ghp_CANARY_77aa"
	var code atomic.Int32
	echo := func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		r.Header.Write(&b)
		r.Write(&b)
		msg, _ := json.Marshal(b.String() + " " + tracker)
		w.WriteHeader(int(code.Load()))
		fmt.Fprintf(w, `{"code":"internal","message":%s}`, msg)
	}
	ts := httptest.NewServer(http.HandlerFunc(echo))
	defer ts.Close()

	var logs []string
	var mu sync.Mutex
	c := &Client{
		URL: ts.URL, Version: "1", DaemonID: "d", Credential: cred, Secrets: []string{tracker},
		Logf:  func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() },
		Sleep: func(context.Context, time.Duration) error { return errors.New("stop") },
	}
	var errs []error
	for _, st := range []int32{400, 401, 500} {
		code.Store(st)
		_, err := c.Enroll(context.Background(), enrollReq(enrollToken))
		errs = append(errs, err)
		_, _, err = c.GetConfig(context.Background(), "")
		errs = append(errs, err)
		_, err = c.PostStatus(context.Background(), status())
		errs = append(errs, err)
	}
	bad := enrollReq(enrollToken)
	bad.Name = ""
	_, err := c.Enroll(context.Background(), bad)
	errs = append(errs, err)
	errs = append(errs, c.Retry(context.Background(), func(ctx context.Context) error {
		_, err := c.PostStatus(ctx, status())
		return err
	}))
	errs = append(errs, c.Commands(context.Background(), nil))
	c.URL = "http://user:" + cred + "@127.0.0.1:1/"
	_, err = c.PostStatus(context.Background(), status())
	errs = append(errs, err)

	mu.Lock()
	defer mu.Unlock()
	texts := logs
	for _, err := range errs {
		if err == nil {
			t.Fatal("expected an error")
		}
		texts = append(texts, err.Error())
	}
	if len(logs) == 0 {
		t.Fatal("expected log lines")
	}
	for _, s := range texts {
		for _, secret := range []string{cred, tracker, enrollToken} {
			if strings.Contains(s, secret) {
				t.Errorf("secret %q in %q", secret, s)
			}
		}
	}
}

func TestRetrySetDown(t *testing.T) {
	hub, c := enrolled(t)
	hub.SetConfig(config("a"))
	hub.SetDown(true)
	var slept []time.Duration
	c.Rand = func() float64 { return 1 }
	c.Sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		if len(slept) == 3 {
			hub.SetDown(false)
		}
		return nil
	}
	var cfg *hubproto.ConfigResponse
	err := c.Retry(context.Background(), func(ctx context.Context) error {
		var err error
		cfg, _, err = c.GetConfig(ctx, "")
		return err
	})
	if err != nil || cfg == nil {
		t.Fatalf("retry: %v", err)
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}; fmt.Sprint(slept) != fmt.Sprint(want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
}

func TestBackoffBounds(t *testing.T) {
	for _, r := range []float64{0, 0.5, 1} {
		b := &Backoff{Min: BackoffMin, Max: BackoffMax, Rand: func() float64 { return r }}
		ceil := BackoffMin
		for i := range 40 {
			d := b.Next()
			if want := time.Duration(r * float64(ceil)); d != want {
				t.Fatalf("rand %v attempt %d: %s, want %s", r, i, d, want)
			}
			ceil = min(ceil*2, BackoffMax)
		}
		b.Reset()
		if d := b.Next(); d != time.Duration(r*float64(BackoffMin)) {
			t.Fatalf("after reset: %s", d)
		}
	}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestCommandChannel covers the ack round trip, unsupported commands,
// reconnect with back-off after SetDown, and the reset after 60 s connected.
func TestCommandChannel(t *testing.T) {
	hub, c := enrolled(t)
	clk := &clock{now: time.Unix(1000, 0)}
	sleeps, release := make(chan time.Duration), make(chan struct{})
	c.Now = clk.Now
	c.Rand = func() float64 { return 1 }
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		select {
		case sleeps <- d:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var mu sync.Mutex
	var got []hubproto.Command
	handler := func(_ context.Context, cmd hubproto.Command) *hubproto.AckError {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, cmd)
		if cmd.Type == hubproto.CmdStop {
			return &hubproto.AckError{Message: "cannot stop"}
		}
		return nil
	}
	hub.SetDown(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Commands(ctx, handler) }()

	expectSleep := func(want time.Duration) {
		t.Helper()
		select {
		case d := <-sleeps:
			if d != want {
				t.Fatalf("back-off %s, want %s", d, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no back-off of %s", want)
		}
	}
	expectSleep(time.Second)
	release <- struct{}{}
	expectSleep(2 * time.Second)
	hub.SetDown(false)
	release <- struct{}{}
	waitFor(t, "socket", hub.Connected)

	ack, err := hub.Send(hubproto.Command{Type: hubproto.CmdWake, Args: json.RawMessage(`{"project":"acme/widgets","workflow":"issues"}`)})
	if err != nil || !ack.OK || ack.Error != nil {
		t.Fatalf("wake ack %+v %v", ack, err)
	}
	ack, err = hub.Send(hubproto.Command{ID: "x-9", Type: hubproto.CmdStop})
	if err != nil || ack.ID != "x-9" || ack.OK || ack.Error.Code != hubproto.AckFailed || ack.Error.Message != "cannot stop" {
		t.Fatalf("stop ack %+v %v", ack, err)
	}
	ack, err = hub.Send(hubproto.Command{Type: hubproto.CmdSessionOpen, Args: json.RawMessage(`{"runtime":"x"}`)})
	if err != nil || ack.OK || ack.Error.Code != hubproto.AckUnsupported || ack.Error.Message != "session.open" {
		t.Fatalf("session.open ack %+v %v", ack, err)
	}
	mu.Lock()
	if len(got) != 2 || got[0].Type != hubproto.CmdWake || got[1].Type != hubproto.CmdStop {
		t.Fatalf("handler saw %+v", got)
	}
	mu.Unlock()

	// Short connection: the back-off keeps growing.
	hub.SetDown(true)
	expectSleep(4 * time.Second)
	hub.SetDown(false)
	release <- struct{}{}
	waitFor(t, "socket after short connection", hub.Connected)

	// Connected for over 60 s: the back-off starts again at 1 s.
	clk.add(61 * time.Second)
	hub.SetDown(true)
	expectSleep(time.Second)
	hub.SetDown(false)
	release <- struct{}{}
	waitFor(t, "socket after reset", hub.Connected)
	if _, err := hub.Send(hubproto.Command{Type: hubproto.CmdPause}); err != nil {
		t.Fatal(err)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Commands returned %v", err)
	}
}

// TestCommandAcks sends messages the fake hub cannot send: unknown types,
// bad arguments, unknown fields and a message without id.
func TestCommandAcks(t *testing.T) {
	acks := make(chan string, 10)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cred" || r.Header.Get("Jig-Daemon-Id") != "d" || r.Header.Get("Jig-Version") != "1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		conn, err := wsclient.Accept(w, r)
		if err != nil {
			return
		}
		defer conn.Close(wsclient.StatusNormal, "")
		for _, m := range []string{
			`{"type":"wake"}`,
			`{"id":"a","type":"self.destruct","args":{}}`,
			`{"id":"b","type":"wake","args":{"project":"acme/widgets"}}`,
			`{"id":"c","type":"pause","args":{},"extra":1}`,
			`{"id":"d","type":"reload_config","args":{}}`,
		} {
			if err := conn.Write(r.Context(), []byte(m)); err != nil {
				return
			}
		}
		for range 4 {
			msg, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			acks <- string(msg)
		}
	}))
	defer ts.Close()
	c := &Client{URL: ts.URL, Version: "1", DaemonID: "d", Credential: "cred"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Commands(ctx, func(context.Context, hubproto.Command) *hubproto.AckError { return nil })

	want := []string{
		`{"id":"a","type":"ack","ok":false,"error":{"code":"unsupported","message":"self.destruct"}}`,
		`"id":"b","type":"ack","ok":false,"error":{"code":"invalid"`,
		`"id":"c","type":"ack","ok":false,"error":{"code":"invalid"`,
		`{"id":"d","type":"ack","ok":true}`,
	}
	for _, w := range want {
		select {
		case a := <-acks:
			if !strings.Contains(a, w) {
				t.Errorf("ack %s, want %s", a, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no ack for %s", w)
		}
	}
}
