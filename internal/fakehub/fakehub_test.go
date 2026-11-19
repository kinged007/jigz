package fakehub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kinged007/jigz/internal/hubproto"
)

const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type harness struct {
	t   *testing.T
	hub *Server
	ts  *httptest.Server
	id  string
	cr  string
	// ctx bounds every socket call, so a missing message fails the test
	// instead of hanging it.
	ctx context.Context
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	hub := New("enroll-secret")
	ts := httptest.NewServer(hub)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return &harness{t: t, hub: hub, ts: ts, ctx: ctx}
}

// recv returns the next value from ch, or fails the test after 10 s.
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func (h *harness) do(method, path, body string, hdr map[string]string) (*http.Response, string) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.ts.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (h *harness) auth() map[string]string {
	return map[string]string{"Authorization": "Bearer " + h.cr, "Jig-Daemon-Id": h.id, "Jig-Version": "1.2.3"}
}

func enrollBody(token string) string {
	b, _ := json.Marshal(hubproto.EnrollRequest{
		Token: token, Name: "ci-1", Host: hubproto.Host{OS: "linux", Arch: "amd64", CPUs: 4},
		JigVersion: "1.2.3", EngineVersion: "v1.28.1", TrackerLogin: "octo",
	})
	return string(b)
}

func (h *harness) enroll() {
	h.t.Helper()
	resp, body := h.do("POST", base+"enroll", enrollBody("enroll-secret"), map[string]string{"Jig-Version": "1.2.3"})
	if resp.StatusCode != 200 {
		h.t.Fatalf("enroll: %d %s", resp.StatusCode, body)
	}
	var er hubproto.EnrollResponse
	if err := json.Unmarshal([]byte(body), &er); err != nil || er.Validate() != nil {
		h.t.Fatalf("enroll response %s: %v", body, err)
	}
	h.id, h.cr = er.DaemonID, er.Credential
}

func testConfig() hubproto.ConfigResponse {
	return hubproto.ConfigResponse{
		Member:   hubproto.Member{Login: "octo"},
		Projects: []hubproto.ConfigProject{{Provider: "github", Repo: "acme/widgets", Workflows: []string{"issues"}}},
		Files:    []hubproto.ConfigFile{{Path: "WORKFLOW.issues.md", SHA256: sha, Content: "x"}},
		Labels:   []hubproto.Label{},
	}
}

func statusBody() string {
	b, _ := json.Marshal(hubproto.StatusRequest{
		Time: time.Now().UTC(), Capacity: &hubproto.Capacity{}, Hours: &hubproto.Hours{Open: true},
		Loops:    []hubproto.Loop{{Project: "acme/widgets", Workflow: "issues", State: hubproto.LoopStopped, Files: []hubproto.FileHash{}}},
		Accounts: []hubproto.Account{},
	})
	return string(b)
}

func runsBody(ids ...string) string {
	var req hubproto.RunsRequest
	for _, id := range ids {
		req.Events = append(req.Events, hubproto.RunEvent{
			ID: id, Type: hubproto.EventStarted, Workflow: "issues", Attempt: 1, StartedAt: time.Now().UTC(),
			Item: hubproto.Item{Project: "acme/widgets", Number: 1, Kind: hubproto.KindIssue},
		})
	}
	b, _ := json.Marshal(req)
	return string(b)
}

func TestEnroll(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	en := h.hub.Enrollments()
	if len(en) != 1 || en[0].Name != "ci-1" || en[0].Host.CPUs != 4 || en[0].Token != "" {
		t.Fatalf("enrollments = %+v", en)
	}
	if h.hub.Credential() != h.cr {
		t.Error("credential mismatch")
	}
	if len(h.hub.Bodies()) != 0 {
		t.Error("enroll body was recorded")
	}
	resp, _ := h.do("POST", base+"enroll", enrollBody("enroll-secret"), map[string]string{"Jig-Version": "1"})
	if resp.StatusCode != 401 {
		t.Errorf("second enroll = %d, want 401", resp.StatusCode)
	}
	if len(h.hub.Unexpected()) != 1 {
		t.Errorf("unexpected = %v", h.hub.Unexpected())
	}
}

func TestEnrollRejections(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		hdr    map[string]string
		status int
	}{
		{"wrong token", enrollBody("nope"), map[string]string{"Jig-Version": "1"}, 401},
		{"unknown field", `{"token":"enroll-secret","extra":1}`, map[string]string{"Jig-Version": "1"}, 400},
		{"invalid body", `{"token":"enroll-secret"}`, map[string]string{"Jig-Version": "1"}, 400},
		{"missing version", enrollBody("enroll-secret"), nil, 400},
		{"carries bearer", enrollBody("enroll-secret"), map[string]string{"Jig-Version": "1", "Authorization": "Bearer x"}, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			resp, _ := h.do("POST", base+"enroll", c.body, c.hdr)
			if resp.StatusCode != c.status {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.status)
			}
			if len(h.hub.Unexpected()) != 1 || len(h.hub.Enrollments()) != 0 {
				t.Errorf("unexpected = %v, enrollments = %v", h.hub.Unexpected(), h.hub.Enrollments())
			}
		})
	}
}

func TestConfigETag(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	etag := h.hub.SetConfig(testConfig())
	resp, body := h.do("GET", base+"config", "", h.auth())
	if resp.StatusCode != 200 || resp.Header.Get("ETag") != etag {
		t.Fatalf("config = %d etag %q", resp.StatusCode, resp.Header.Get("ETag"))
	}
	var cfg hubproto.ConfigResponse
	if err := json.Unmarshal([]byte(body), &cfg); err != nil || cfg.Member.Login != "octo" {
		t.Fatalf("body %s: %v", body, err)
	}
	hdr := h.auth()
	hdr["If-None-Match"] = etag
	resp, body = h.do("GET", base+"config", "", hdr)
	if resp.StatusCode != 304 || body != "" {
		t.Errorf("matching If-None-Match = %d %q, want 304", resp.StatusCode, body)
	}
	if next := h.hub.SetConfig(testConfig()); next == etag {
		t.Error("SetConfig reused the ETag")
	}
	resp, _ = h.do("GET", base+"config", "", hdr)
	if resp.StatusCode != 200 {
		t.Errorf("stale If-None-Match = %d, want 200", resp.StatusCode)
	}
	if u := h.hub.Unexpected(); len(u) != 0 {
		t.Errorf("unexpected = %v", u)
	}
}

func TestConfigNotSet(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	if resp, _ := h.do("GET", base+"config", "", h.auth()); resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if len(h.hub.Unexpected()) != 1 {
		t.Errorf("unexpected = %v", h.hub.Unexpected())
	}
}

func TestStatus(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	etag := h.hub.SetConfig(testConfig())
	h.hub.SetInterval(7)
	resp, body := h.do("POST", base+"status", statusBody(), h.auth())
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
	var sr hubproto.StatusResponse
	if err := json.Unmarshal([]byte(body), &sr); err != nil || sr.Validate() != nil || sr.ConfigETag != etag || sr.IntervalSeconds != 7 {
		t.Errorf("response %s: %v", body, err)
	}
	st := h.hub.Statuses()
	if len(st) != 1 || st[0].Loops[0].Project != "acme/widgets" {
		t.Errorf("statuses = %+v", st)
	}
	if len(h.hub.Bodies()) != 1 {
		t.Errorf("bodies = %d, want 1", len(h.hub.Bodies()))
	}
}

func TestRuns(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	resp, body := h.do("POST", base+"runs", runsBody("e1", "e2"), h.auth())
	if resp.StatusCode != 200 {
		t.Fatalf("runs = %d %s", resp.StatusCode, body)
	}
	var rr hubproto.RunsResponse
	if err := json.Unmarshal([]byte(body), &rr); err != nil || strings.Join(rr.Accepted, ",") != "e1,e2" {
		t.Errorf("accepted %s: %v", body, err)
	}
	h.do("POST", base+"runs", runsBody("e2"), h.auth())
	if runs := h.hub.Runs(); len(runs) != 3 || runs[2].ID != "e2" {
		t.Errorf("runs = %+v", runs)
	}
}

func TestBodyRejections(t *testing.T) {
	for _, c := range []struct{ name, route, body string }{
		{"status unknown field", "status", strings.Replace(statusBody(), `"paused"`, `"bogus":1,"paused"`, 1)},
		{"status invalid", "status", `{}`},
		{"status trailing data", "status", statusBody() + "{}"},
		{"runs unknown field", "runs", `{"events":[],"x":1}`},
		{"runs empty", "runs", `{"events":[]}`},
		{"runs not json", "runs", `nope`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			resp, body := h.do("POST", base+c.route, c.body, h.auth())
			var eb hubproto.ErrorBody
			if resp.StatusCode != 400 || json.Unmarshal([]byte(body), &eb) != nil || eb.Code != hubproto.ErrInvalid {
				t.Errorf("status = %d body %s", resp.StatusCode, body)
			}
			if len(h.hub.Unexpected()) != 1 || len(h.hub.Statuses()) != 0 || len(h.hub.Runs()) != 0 {
				t.Errorf("unexpected = %v", h.hub.Unexpected())
			}
		})
	}
}

func TestHeaderAndRouteRejections(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		mod    func(map[string]string)
		status int
	}{
		{"wrong bearer", "POST", "status", func(m map[string]string) { m["Authorization"] = "Bearer wrong" }, 401},
		{"no bearer", "POST", "status", func(m map[string]string) { delete(m, "Authorization") }, 401},
		{"missing daemon id", "POST", "status", func(m map[string]string) { delete(m, "Jig-Daemon-Id") }, 400},
		{"wrong daemon id", "POST", "status", func(m map[string]string) { m["Jig-Daemon-Id"] = "d-9" }, 400},
		{"missing version", "POST", "status", func(m map[string]string) { delete(m, "Jig-Version") }, 400},
		{"unknown route", "POST", "nothing", func(map[string]string) {}, 404},
		{"wrong method", "GET", "status", func(map[string]string) {}, 404},
		{"ws wrong bearer", "GET", "ws", func(m map[string]string) { m["Authorization"] = "Bearer wrong" }, 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.enroll()
			hdr := h.auth()
			c.mod(hdr)
			resp, _ := h.do(c.method, base+c.path, statusBody(), hdr)
			if resp.StatusCode != c.status {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.status)
			}
			if len(h.hub.Unexpected()) != 1 || len(h.hub.Statuses()) != 0 {
				t.Errorf("unexpected = %v", h.hub.Unexpected())
			}
		})
	}
}

func TestSetDown(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.hub.SetConfig(testConfig())
	conn := h.dial()
	waitConnected(t, h.hub, true)

	h.hub.SetDown(true)
	if h.hub.Connected() {
		t.Error("socket still connected while down")
	}
	if _, _, err := conn.Read(h.ctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Errorf("client read after SetDown: %v, want close 1001", err)
	}
	for _, rt := range [][2]string{{"GET", "config"}, {"POST", "status"}, {"POST", "runs"}, {"GET", "ws"}, {"POST", "enroll"}, {"GET", "other"}} {
		resp, body := h.do(rt[0], base+rt[1], "", h.auth())
		var eb hubproto.ErrorBody
		if resp.StatusCode != 503 || json.Unmarshal([]byte(body), &eb) != nil || eb.Code != hubproto.ErrUnavailable {
			t.Errorf("%v while down = %d %s", rt, resp.StatusCode, body)
		}
	}
	if _, err := h.hub.Send(hubproto.Command{Type: hubproto.CmdPause}); err == nil {
		t.Error("Send succeeded while down")
	}
	h.hub.SetDown(false)
	if resp, _ := h.do("GET", base+"config", "", h.auth()); resp.StatusCode != 200 {
		t.Errorf("config after recovery = %d", resp.StatusCode)
	}
	if u := h.hub.Unexpected(); len(u) != 0 {
		t.Errorf("unexpected = %v", u)
	}
}

func TestRevoke(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.hub.SetConfig(testConfig())
	conn := h.dial()
	waitConnected(t, h.hub, true)

	h.hub.Revoke()
	if h.hub.Connected() {
		t.Error("socket still connected after Revoke")
	}
	if _, _, err := conn.Read(h.ctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Errorf("client read after Revoke: %v, want close 1001", err)
	}
	for _, rt := range [][2]string{{"GET", "config"}, {"POST", "status"}, {"GET", "ws"}} {
		if resp, _ := h.do(rt[0], base+rt[1], statusBody(), h.auth()); resp.StatusCode != 401 {
			t.Errorf("%v after revoke = %d, want 401", rt, resp.StatusCode)
		}
	}
	if u := h.hub.Unexpected(); len(u) != 0 {
		t.Errorf("a revoked credential is expected traffic, got %v", u)
	}
}

func (h *harness) dial() *websocket.Conn {
	h.t.Helper()
	hdr := http.Header{}
	for k, v := range h.auth() {
		hdr.Set(k, v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.ts.URL, "http")+base+"ws", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func waitConnected(t *testing.T, hub *Server, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for hub.Connected() != want {
		if time.Now().After(deadline) {
			t.Fatalf("Connected() != %v after 5s", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSendAckRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	if _, err := h.hub.Send(hubproto.Command{Type: hubproto.CmdPause}); err == nil {
		t.Fatal("Send without a socket succeeded")
	}
	conn := h.dial()
	waitConnected(t, h.hub, true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			_, msg, err := conn.Read(h.ctx)
			if err != nil {
				t.Error(err)
				return
			}
			var cmd hubproto.Command
			if err := json.Unmarshal(msg, &cmd); err != nil || cmd.Validate() != nil {
				t.Errorf("command %s: %v", msg, err)
				return
			}
			ack := hubproto.Ack{ID: cmd.ID, Type: "ack", OK: true}
			if cmd.Type == hubproto.CmdSessionOpen {
				ack = hubproto.Ack{ID: cmd.ID, Type: "ack", Error: &hubproto.AckError{Code: hubproto.AckUnsupported, Message: "session.open"}}
			}
			data, _ := json.Marshal(ack)
			conn.Write(h.ctx, websocket.MessageText, data)
		}
	}()

	ack, err := h.hub.Send(hubproto.Command{Type: hubproto.CmdWake, Args: json.RawMessage(`{"project":"acme/widgets","workflow":"issues"}`)})
	if err != nil || !ack.OK || ack.ID == "" {
		t.Fatalf("wake ack = %+v, %v", ack, err)
	}
	ack, err = h.hub.Send(hubproto.Command{ID: "x-9", Type: hubproto.CmdSessionOpen, Args: json.RawMessage(`{"a":1}`)})
	if err != nil || ack.ID != "x-9" || ack.OK || ack.Error.Code != hubproto.AckUnsupported {
		t.Fatalf("session.open ack = %+v, %v", ack, err)
	}
	recv(t, done, "the client reader")
	if u := h.hub.Unexpected(); len(u) != 0 {
		t.Errorf("unexpected = %v", u)
	}
}

func TestSendRejectsInvalidCommand(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.dial()
	waitConnected(t, h.hub, true)
	if _, err := h.hub.Send(hubproto.Command{Type: hubproto.CmdWake}); err == nil {
		t.Error("wake without arguments was sent")
	}
}

func TestBadSocketMessages(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	conn := h.dial()
	waitConnected(t, h.hub, true)
	for _, msg := range []string{`{"id":"zz","type":"ack","ok":true}`, `{"id":"a","type":"ack","ok":true,"x":1}`, `not json`} {
		conn.Write(h.ctx, websocket.MessageText, []byte(msg))
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.hub.Unexpected()) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if u := h.hub.Unexpected(); len(u) != 3 {
		t.Errorf("unexpected = %v, want 3 entries", u)
	}
}

func TestSocketPendingFailsWhenClientCloses(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	conn := h.dial()
	waitConnected(t, h.hub, true)
	go func() {
		conn.Read(h.ctx)
		conn.Close(websocket.StatusNormalClosure, "")
	}()
	if _, err := h.hub.Send(hubproto.Command{Type: hubproto.CmdPause}); err == nil {
		t.Error("Send succeeded although the client closed without an ack")
	}
	waitConnected(t, h.hub, false)
}

func TestBodiesHoldNoEnrollToken(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.do("POST", base+"status", statusBody(), h.auth())
	h.do("POST", base+"runs", runsBody("e1"), h.auth())
	for _, b := range h.hub.Bodies() {
		if bytes.Contains([]byte(b), []byte("enroll-secret")) || strings.Contains(b, h.cr) {
			t.Errorf("recorded body holds a secret: %s", b)
		}
	}
	if len(h.hub.Bodies()) != 2 {
		t.Errorf("bodies = %d, want 2", len(h.hub.Bodies()))
	}
}

func TestReconnectWhilePending(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	conn1 := h.dial()
	waitConnected(t, h.hub, true)

	got1 := make(chan hubproto.Command, 1)
	end1 := make(chan error, 1)
	go func() {
		for {
			_, msg, err := conn1.Read(h.ctx)
			if err != nil {
				end1 <- err
				return
			}
			var cmd hubproto.Command
			json.Unmarshal(msg, &cmd)
			got1 <- cmd
		}
	}()
	err1 := make(chan error, 1)
	go func() {
		_, err := h.hub.Send(hubproto.Command{ID: "old", Type: hubproto.CmdPause})
		err1 <- err
	}()
	recv(t, got1, "the first command")

	h.hub.mu.Lock()
	sk1 := h.hub.sock
	h.hub.mu.Unlock()
	conn2 := h.dial()
	// Dial returns once the upgrade is answered, which can be before the
	// hub replaces its socket.
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.hub.mu.Lock()
		replaced := h.hub.sock != nil && h.hub.sock != sk1
		h.hub.mu.Unlock()
		if replaced {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the hub did not replace the socket")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cmd2 := make(chan hubproto.Command, 1)
	go func() {
		_, msg, err := conn2.Read(h.ctx)
		if err != nil {
			return
		}
		var cmd hubproto.Command
		json.Unmarshal(msg, &cmd)
		cmd2 <- cmd
	}()
	res2 := make(chan error, 1)
	go func() {
		ack, err := h.hub.Send(hubproto.Command{ID: "new", Type: hubproto.CmdResume})
		if err == nil && !ack.OK {
			err = errors.New("not ok")
		}
		res2 <- err
	}()
	cmd := recv(t, cmd2, "the command on the replacement socket")
	if cmd.ID != "new" {
		t.Fatalf("second socket received %+v", cmd)
	}

	if err := recv(t, end1, "the end of the replaced socket"); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Errorf("replaced socket read: %v, want close 1001", err)
	}
	if err := recv(t, err1, "the pending Send on the replaced socket"); err == nil {
		t.Error("command pending on the replaced socket did not fail")
	}
	late, _ := json.Marshal(hubproto.Ack{ID: "old", Type: "ack", OK: true})
	conn1.Write(h.ctx, websocket.MessageText, late)
	time.Sleep(50 * time.Millisecond)
	if !h.hub.Connected() {
		t.Fatal("the replacement socket was dropped")
	}
	ack, _ := json.Marshal(hubproto.Ack{ID: "new", Type: "ack", OK: true})
	if err := conn2.Write(h.ctx, websocket.MessageText, ack); err != nil {
		t.Fatal(err)
	}
	if err := recv(t, res2, "the Send on the replacement socket"); err != nil {
		t.Errorf("command on the replacement socket: %v", err)
	}
}

func TestConfigIfNoneMatchForms(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	etag := h.hub.SetConfig(testConfig())
	for _, c := range []struct {
		header string
		status int
	}{
		{"W/" + etag, 304},
		{`"other", ` + etag, 304},
		{`"a",W/` + etag + `,"b"`, 304},
		{"*", 304},
		{`"other"`, 200},
		{"", 200},
	} {
		hdr := h.auth()
		if c.header != "" {
			hdr["If-None-Match"] = c.header
		}
		if resp, _ := h.do("GET", base+"config", "", hdr); resp.StatusCode != c.status {
			t.Errorf("If-None-Match %q = %d, want %d", c.header, resp.StatusCode, c.status)
		}
	}
}

func TestEnrollTokenCheckedBeforeBody(t *testing.T) {
	h := newHarness(t)
	hdr := map[string]string{"Jig-Version": "1"}
	if resp, _ := h.do("POST", base+"enroll", `{"token":"nope","bogus":1}`, hdr); resp.StatusCode != 401 {
		t.Errorf("wrong token with a bad body = %d, want 401", resp.StatusCode)
	}
	if resp, _ := h.do("POST", base+"enroll", `{"token":"enroll-secret"}`, hdr); resp.StatusCode != 400 {
		t.Errorf("right token with a bad body = %d, want 400", resp.StatusCode)
	}
	h.enroll()
}

func TestFailRuns(t *testing.T) {
	h := newHarness(t)
	h.enroll()
	h.hub.FailRuns(429, 1)
	resp, _ := h.do("POST", base+"runs", runsBody("e1"), h.auth())
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("first = %d Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if len(h.hub.Runs()) != 0 {
		t.Error("failed batch was recorded")
	}
	h.hub.FailRuns(503, 2)
	for range 2 {
		if resp, _ := h.do("POST", base+"runs", runsBody("e1"), h.auth()); resp.StatusCode != 503 {
			t.Errorf("status = %d, want 503", resp.StatusCode)
		}
	}
	if resp, _ := h.do("POST", base+"runs", runsBody("e1"), h.auth()); resp.StatusCode != 200 {
		t.Errorf("after the failures = %d, want 200", resp.StatusCode)
	}
	if len(h.hub.Runs()) != 1 || len(h.hub.Unexpected()) != 0 {
		t.Errorf("runs = %d, unexpected = %v", len(h.hub.Runs()), h.hub.Unexpected())
	}
}
