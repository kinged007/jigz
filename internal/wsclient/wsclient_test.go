package wsclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const secret = "Bearer s3cr3t-token-value"

type rawFrame struct {
	fin     bool
	op      byte
	masked  bool
	key     [4]byte
	raw     []byte
	payload []byte
}

func readRawFrame(r io.Reader) (rawFrame, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:2]); err != nil {
		return rawFrame{}, err
	}
	f := rawFrame{fin: b[0]&0x80 != 0, op: b[0] & 0x0F, masked: b[1]&0x80 != 0}
	n := int(b[1] & 0x7F)
	switch n {
	case 126:
		if _, err := io.ReadFull(r, b[:2]); err != nil {
			return f, err
		}
		n = int(binary.BigEndian.Uint16(b[:2]))
	case 127:
		if _, err := io.ReadFull(r, b[:8]); err != nil {
			return f, err
		}
		n = int(binary.BigEndian.Uint64(b[:8]))
	}
	if f.masked {
		if _, err := io.ReadFull(r, f.key[:]); err != nil {
			return f, err
		}
	}
	f.raw = make([]byte, n)
	if _, err := io.ReadFull(r, f.raw); err != nil {
		return f, err
	}
	f.payload = bytes.Clone(f.raw)
	if f.masked {
		mask(f.key, f.payload)
	}
	return f, nil
}

func (f rawFrame) closeCode() int {
	if f.op != opClose || len(f.payload) < 2 {
		return 0
	}
	return int(binary.BigEndian.Uint16(f.payload))
}

// encode builds a frame; masked frames use a fixed key.
func encode(fin bool, op byte, payload []byte, masked bool) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	c := &Conn{client: masked}
	f := c.frame(op, payload)
	f[0] = b0
	return f
}

// rawServer accepts one TCP connection, answers the upgrade (with a wrong
// Sec-WebSocket-Accept when badAccept is set) and runs fn on the connection.
func rawServer(t *testing.T, badAccept bool, fn func(nc net.Conn, br *bufio.Reader)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		br := bufio.NewReader(nc)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		accept := acceptKey(req.Header.Get("Sec-WebSocket-Key"))
		if badAccept {
			accept = acceptKey("other")
		}
		fmt.Fprintf(nc, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
		fn(nc, br)
	}()
	return "ws://" + ln.Addr().String() + "/ws"
}

func echoHandler(gotAuth chan<- string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gotAuth != nil {
			gotAuth <- r.Header.Get("Authorization")
		}
		c, err := Accept(w, r)
		if err != nil {
			return
		}
		for {
			p, err := c.Read(context.Background())
			if err != nil {
				return
			}
			if err := c.Write(context.Background(), p); err != nil {
				return
			}
		}
	}
}

func wsURL(s *httptest.Server) string {
	return "ws" + strings.TrimPrefix(s.URL, "http") + "/ws"
}

func TestEcho(t *testing.T) {
	gotAuth := make(chan string, 1)
	plain := httptest.NewServer(echoHandler(gotAuth))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(echoHandler(nil))
	defer tlsSrv.Close()

	tests := []struct {
		name string
		url  string
		opts Options
		msgs []string
	}{
		{"ws", wsURL(plain), Options{}, []string{"hello", "", strings.Repeat("a", 200), strings.Repeat("b", 70000)}},
		{"wss", wsURL(tlsSrv), Options{TLSConfig: tlsSrv.Client().Transport.(*http.Transport).TLSClientConfig}, []string{"secure", "héllo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := Dial(ctx, tt.url, http.Header{"Authorization": {secret}}, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(StatusNormal, "")
			for _, m := range tt.msgs {
				if err := c.Write(ctx, []byte(m)); err != nil {
					t.Fatal(err)
				}
				got, err := c.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != m {
					t.Fatalf("echo of %d bytes returned %d bytes", len(m), len(got))
				}
			}
		})
	}
	if got := <-gotAuth; got != secret {
		t.Fatalf("server got Authorization %q", got)
	}
}

func TestClientFramesMasked(t *testing.T) {
	frames := make(chan rawFrame, 2)
	url := rawServer(t, false, func(nc net.Conn, br *bufio.Reader) {
		for range 2 {
			f, err := readRawFrame(br)
			if err != nil {
				close(frames)
				return
			}
			frames <- f
		}
	})
	c, err := Dial(context.Background(), url, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(StatusNormal, "")
	var keys [][4]byte
	for range 2 {
		if err := c.Write(context.Background(), []byte("hello")); err != nil {
			t.Fatal(err)
		}
		f, ok := <-frames
		if !ok {
			t.Fatal("server read failed")
		}
		if !f.masked || !f.fin || f.op != opText {
			t.Fatalf("frame fin=%v op=%d masked=%v", f.fin, f.op, f.masked)
		}
		if string(f.raw) == "hello" || string(f.payload) != "hello" {
			t.Fatalf("raw %q payload %q", f.raw, f.payload)
		}
		keys = append(keys, f.key)
	}
	if keys[0] == keys[1] {
		t.Fatalf("two frames used the same mask key %x", keys[0])
	}
}

func TestReadFrames(t *testing.T) {
	text := func(fin bool, op byte, s string) []byte { return encode(fin, op, []byte(s), false) }
	tests := []struct {
		name      string
		readLimit int64
		frames    [][]byte
		wantType  MessageType
		want      string
		wantCode  int
	}{
		{name: "single", frames: [][]byte{text(true, opText, "hi")}, wantType: MessageText, want: "hi"},
		{name: "fragmented", frames: [][]byte{text(false, opText, "Hel"), text(false, opContinuation, "lo "), text(true, opContinuation, "world")}, wantType: MessageText, want: "Hello world"},
		{name: "fragmented with pong", frames: [][]byte{text(false, opText, "a"), text(true, opPong, ""), text(true, opContinuation, "b")}, wantType: MessageText, want: "ab"},
		{name: "binary", frames: [][]byte{encode(true, opBinary, []byte{0xff, 0x00}, false)}, wantType: MessageBinary, want: "\xff\x00"},
		{name: "utf-8 split across fragments", frames: [][]byte{encode(false, opText, []byte("\xc3"), false), encode(true, opContinuation, []byte("\xa9"), false)}, wantType: MessageText, want: "é"},
		{name: "oversized frame", readLimit: 10, frames: [][]byte{text(true, opText, strings.Repeat("x", 20))}, wantCode: StatusMessageTooBig},
		{name: "oversized message", readLimit: 10, frames: [][]byte{text(false, opText, "123456"), text(true, opContinuation, "789012")}, wantCode: StatusMessageTooBig},
		{name: "invalid utf-8", frames: [][]byte{encode(true, opText, []byte{0xff, 0xfe}, false)}, wantCode: StatusInvalidData},
		{name: "masked server frame", frames: [][]byte{encode(true, opText, []byte("hi"), true)}, wantCode: StatusProtocolError},
		{name: "unexpected continuation", frames: [][]byte{text(true, opContinuation, "x")}, wantCode: StatusProtocolError},
		{name: "missing continuation", frames: [][]byte{text(false, opText, "x"), text(true, opText, "y")}, wantCode: StatusProtocolError},
		{name: "reserved bits", frames: [][]byte{append([]byte{0xC1}, text(true, opText, "x")[1:]...)}, wantCode: StatusProtocolError},
		{name: "fragmented ping", frames: [][]byte{text(false, opPing, "")}, wantCode: StatusProtocolError},
		{name: "unknown opcode", frames: [][]byte{text(true, 0x3, "")}, wantCode: StatusProtocolError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := make(chan rawFrame, 1)
			url := rawServer(t, false, func(nc net.Conn, br *bufio.Reader) {
				for _, f := range tt.frames {
					nc.Write(f)
				}
				nc.SetReadDeadline(time.Now().Add(5 * time.Second))
				f, err := readRawFrame(br)
				if err != nil {
					close(reply)
					return
				}
				reply <- f
				nc.Write(encode(true, opClose, f.payload, false))
			})
			c, err := Dial(context.Background(), url, nil, Options{ReadLimit: tt.readLimit})
			if err != nil {
				t.Fatal(err)
			}
			typ, got, err := c.ReadMessage(context.Background())
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatal(err)
				}
				if typ != tt.wantType || string(got) != tt.want {
					t.Fatalf("got type %d %q, want type %d %q", typ, got, tt.wantType, tt.want)
				}
				if err := c.Close(StatusNormal, "done"); err != nil {
					t.Fatal(err)
				}
				if f := <-reply; f.closeCode() != StatusNormal {
					t.Fatalf("server got op %d code %d, want close 1000", f.op, f.closeCode())
				}
				return
			}
			var ce *CloseError
			if !errors.As(err, &ce) || ce.Code != tt.wantCode || ce.Remote {
				t.Fatalf("err = %v, want local close %d", err, tt.wantCode)
			}
			if f := <-reply; f.closeCode() != tt.wantCode {
				t.Fatalf("server got op %d code %d, want close %d", f.op, f.closeCode(), tt.wantCode)
			}
			if _, err2 := c.Read(context.Background()); err2 != err {
				t.Fatalf("second Read err = %v, want %v", err2, err)
			}
		})
	}
}

func TestPingBetweenFragments(t *testing.T) {
	pong := make(chan rawFrame, 1)
	url := rawServer(t, false, func(nc net.Conn, br *bufio.Reader) {
		nc.Write(encode(false, opText, []byte("frag-"), false))
		nc.Write(encode(true, opPing, []byte("p1"), false))
		nc.Write(encode(true, opContinuation, []byte("end"), false))
		nc.SetReadDeadline(time.Now().Add(5 * time.Second))
		f, err := readRawFrame(br)
		if err != nil {
			close(pong)
			return
		}
		pong <- f
	})
	c, err := Dial(context.Background(), url, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(StatusNormal, "")
	got, err := c.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "frag-end" {
		t.Fatalf("got %q", got)
	}
	f := <-pong
	if f.op != opPong || !f.masked || string(f.payload) != "p1" {
		t.Fatalf("server got op %d masked=%v payload %q, want masked pong p1", f.op, f.masked, f.payload)
	}
}

func TestCloseHandshake(t *testing.T) {
	tests := []struct {
		name         string
		serverCloses bool
	}{
		{"client initiates", false},
		{"server initiates", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverErr := make(chan error, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := Accept(w, r)
				if err != nil {
					serverErr <- err
					return
				}
				if tt.serverCloses {
					serverErr <- c.Close(StatusGoingAway, "going away")
					return
				}
				_, err = c.Read(context.Background())
				serverErr <- err
			}))
			defer srv.Close()
			c, err := Dial(context.Background(), wsURL(srv), nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			var clientErr error
			if tt.serverCloses {
				_, clientErr = c.Read(context.Background())
			} else {
				clientErr = c.Close(StatusNormal, "bye")
			}
			sErr := <-serverErr
			if time.Since(start) > 2*time.Second {
				t.Fatalf("close handshake took %v", time.Since(start))
			}
			initiatorErr, receiverErr := clientErr, sErr
			code, reason := StatusNormal, "bye"
			if tt.serverCloses {
				initiatorErr, receiverErr = sErr, clientErr
				code, reason = StatusGoingAway, "going away"
			}
			if initiatorErr != nil {
				t.Fatalf("Close returned %v", initiatorErr)
			}
			var ce *CloseError
			if !errors.As(receiverErr, &ce) || ce.Code != code || ce.Reason != reason || !ce.Remote {
				t.Fatalf("receiving side err = %v, want peer close %d %q", receiverErr, code, reason)
			}
		})
	}
}

func TestContextCancelUnblocksRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r)
		if err != nil {
			return
		}
		c.Read(context.Background())
	}))
	defer srv.Close()
	c, err := Dial(context.Background(), wsURL(srv), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err = c.Read(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Read returned after %v", d)
	}
	if err := c.Write(context.Background(), []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Write after cancel err = %v, want ErrClosed", err)
	}
}

func TestDialFailures(t *testing.T) {
	good := httptest.NewServer(echoHandler(nil))
	defer good.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", r.Header.Get("Authorization"))
		http.Error(w, "denied "+r.Header.Get("Authorization"), http.StatusUnauthorized)
	}))
	defer ok.Close()
	redirect := httptest.NewServer(http.RedirectHandler(good.URL+"/ws", http.StatusFound))
	defer redirect.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	ln.Close()

	tests := []struct {
		name   string
		url    string
		want   string
		status int
	}{
		{"non-101", wsURL(ok), "unexpected status 401", 401},
		{"redirect", wsURL(redirect), "redirect refused", 302},
		{"bad accept", rawServer(t, true, func(net.Conn, *bufio.Reader) {}), "invalid Sec-WebSocket-Accept", 0},
		{"connection refused", "ws://" + closedAddr + "/ws?token=" + secret, "dial", 0},
		{"unsupported scheme", "http://example.com/", "unsupported URL scheme", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := Dial(ctx, tt.url, http.Header{"Authorization": {secret}}, Options{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Fatalf("error contains the credential: %v", err)
			}
			var se *StatusError
			if errors.As(err, &se) != (tt.status != 0) || (se != nil && se.Code != tt.status) {
				t.Fatalf("err = %v, want StatusError %d", err, tt.status)
			}
		})
	}
}

func TestAcceptRejects(t *testing.T) {
	valid := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		r.Header.Set("Connection", "keep-alive, Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Version", "13")
		r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		return r
	}
	tests := []struct {
		name   string
		modify func(*http.Request)
		status int
	}{
		{"method", func(r *http.Request) { r.Method = http.MethodPost }, http.StatusMethodNotAllowed},
		{"no upgrade", func(r *http.Request) { r.Header.Del("Upgrade") }, http.StatusBadRequest},
		{"no connection token", func(r *http.Request) { r.Header.Set("Connection", "keep-alive") }, http.StatusBadRequest},
		{"version", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "8") }, http.StatusUpgradeRequired},
		{"bad key", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "short") }, http.StatusBadRequest},
		{"no hijacker", func(*http.Request) {}, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := valid()
			tt.modify(r)
			w := httptest.NewRecorder()
			if _, err := Accept(w, r); err == nil {
				t.Fatal("Accept succeeded")
			}
			if w.Code != tt.status {
				t.Fatalf("status %d, want %d", w.Code, tt.status)
			}
		})
	}
	if got := acceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("acceptKey = %q (RFC 6455 section 1.3 example)", got)
	}
}

func TestAcceptRejectsUnmaskedClientFrame(t *testing.T) {
	serverErr := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r)
		if err != nil {
			serverErr <- err
			return
		}
		_, err = c.Read(context.Background())
		serverErr <- err
	}))
	defer srv.Close()
	nc, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(nc, "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	nc.Write(encode(true, opText, []byte("hi"), false))
	f, err := readRawFrame(br)
	if err != nil {
		t.Fatal(err)
	}
	if f.masked || f.closeCode() != StatusProtocolError {
		t.Fatalf("got op %d masked=%v code %d, want unmasked close 1002", f.op, f.masked, f.closeCode())
	}
	var ce *CloseError
	if err := <-serverErr; !errors.As(err, &ce) || ce.Code != StatusProtocolError {
		t.Fatalf("server err = %v", err)
	}
}

func TestConcurrentWrites(t *testing.T) {
	srv := httptest.NewServer(echoHandler(nil))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, wsURL(srv), nil, Options{PingInterval: time.Millisecond, PongTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(StatusNormal, "")
	const writers, each = 8, 50
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				if err := c.Write(ctx, fmt.Appendf(nil, "w%d-%d", w, i)); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	seen := map[string]bool{}
	for range writers * each {
		p, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seen[string(p)] = true
	}
	wg.Wait()
	if len(seen) != writers*each {
		t.Fatalf("got %d distinct messages, want %d", len(seen), writers*each)
	}
}

func TestPongTimeoutCloses(t *testing.T) {
	url := rawServer(t, false, func(nc net.Conn, br *bufio.Reader) {
		io.Copy(io.Discard, br)
	})
	c, err := Dial(context.Background(), url, nil, Options{PingInterval: 10 * time.Millisecond, PongTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Read(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

func TestRemoteCloseCodes(t *testing.T) {
	tests := []struct {
		code       int
		wantRemote bool
	}{
		{1000, true}, {1001, true}, {1003, true}, {1007, true}, {1011, true},
		{1012, true}, {1013, true}, {1014, true}, {3000, true}, {4999, true},
		{999, false}, {1004, false}, {1005, false}, {1006, false}, {1015, false},
		{1016, false}, {2999, false}, {5000, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.code), func(t *testing.T) {
			reply := make(chan rawFrame, 1)
			url := rawServer(t, false, func(nc net.Conn, br *bufio.Reader) {
				nc.Write(encode(true, opClose, binary.BigEndian.AppendUint16(nil, uint16(tt.code)), false))
				nc.SetReadDeadline(time.Now().Add(5 * time.Second))
				f, err := readRawFrame(br)
				if err != nil {
					close(reply)
					return
				}
				reply <- f
			})
			c, err := Dial(context.Background(), url, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Read(context.Background())
			var ce *CloseError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want CloseError", err)
			}
			wantCode := tt.code
			if !tt.wantRemote {
				wantCode = StatusProtocolError
			}
			if ce.Code != wantCode || ce.Remote != tt.wantRemote {
				t.Fatalf("got code %d remote=%v, want %d remote=%v", ce.Code, ce.Remote, wantCode, tt.wantRemote)
			}
			if f := <-reply; f.closeCode() != wantCode {
				t.Fatalf("server got close %d, want %d", f.closeCode(), wantCode)
			}
		})
	}
}

func TestPingFloodAnsweredOffReadPath(t *testing.T) {
	const pings = 100
	frames := make(chan rawFrame, pings+1)
	url := rawServer(t, false, func(nc net.Conn, br *bufio.Reader) {
		var flood []byte
		for i := range pings {
			flood = append(flood, encode(true, opPing, fmt.Appendf(nil, "p%d", i), false)...)
		}
		flood = append(flood, encode(true, opText, []byte("after"), false)...)
		nc.Write(flood)
		nc.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			f, err := readRawFrame(br)
			if err != nil {
				close(frames)
				return
			}
			frames <- f
		}
	})
	c, err := Dial(context.Background(), url, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// With the write lock held, Read must still return.
	c.writeMu.Lock()
	read := make(chan string, 1)
	go func() {
		got, err := c.Read(context.Background())
		read <- fmt.Sprintf("%s %v", got, err)
	}()
	select {
	case got := <-read:
		c.writeMu.Unlock()
		if got != "after <nil>" {
			t.Fatalf("Read = %s", got)
		}
	case <-time.After(2 * time.Second):
		c.writeMu.Unlock()
		c.abort()
		t.Fatal("Read blocked on the write lock")
	}
	// After the first pong, a text marker ends the scan for extra pongs.
	var pongs []string
	for f := range frames {
		if f.op == opText {
			break
		}
		if f.op == opPong {
			pongs = append(pongs, string(f.payload))
			if len(pongs) == 1 {
				if err := c.Write(context.Background(), []byte("marker")); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if len(pongs) != 1 || pongs[0] != fmt.Sprintf("p%d", pings-1) {
		t.Fatalf("pongs = %q, want one pong for the latest ping", pongs)
	}
	c.abort()
}

func TestPongTimeoutIgnoresIdleReader(t *testing.T) {
	srv := httptest.NewServer(echoHandler(nil))
	defer srv.Close()
	c, err := Dial(context.Background(), wsURL(srv), nil, Options{PingInterval: 10 * time.Millisecond, PongTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(StatusNormal, "")
	time.Sleep(200 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Write(ctx, []byte("still open")); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Read(ctx); err != nil || string(got) != "still open" {
		t.Fatalf("Read = %q, %v", got, err)
	}
}

func TestDialDefaultHandshakeTimeout(t *testing.T) {
	old := defaultHandshakeTimeout
	defaultHandshakeTimeout = 100 * time.Millisecond
	defer func() { defaultHandshakeTimeout = old }()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		io.Copy(io.Discard, nc)
	}()
	errc := make(chan error, 1)
	go func() {
		_, err := Dial(context.Background(), "ws://"+ln.Addr().String()+"/ws", nil, Options{})
		errc <- err
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want deadline exceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dial did not time out")
	}
}
