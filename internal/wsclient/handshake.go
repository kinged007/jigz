package wsclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// Dial opens a client connection to a ws:// or wss:// URL, sending header with
// the upgrade request. Redirects and any status other than 101 fail. Errors
// name the host and path only, never header values or the query.
func Dial(ctx context.Context, rawURL string, header http.Header, opts Options) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("wsclient: invalid URL")
	}
	var useTLS bool
	switch u.Scheme {
	case "ws":
	case "wss":
		useTLS = true
	default:
		return nil, fmt.Errorf("wsclient: unsupported URL scheme %q", u.Scheme)
	}
	target := u.Host + u.Path
	port := u.Port()
	if port == "" {
		port = "80"
		if useTLS {
			port = "443"
		}
	}
	if opts.HandshakeTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.HandshakeTimeout)
		defer cancel()
	}

	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, fmt.Errorf("wsclient: dial %s: %w", target, err)
	}
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	c, err := handshake(ctx, nc, u, header, useTLS, opts)
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		nc.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("wsclient: handshake with %s: %w", target, err)
	}
	return c, nil
}

func handshake(ctx context.Context, nc net.Conn, u *url.URL, header http.Header, useTLS bool, opts Options) (*Conn, error) {
	if useTLS {
		cfg := &tls.Config{}
		if opts.TLSConfig != nil {
			cfg = opts.TLSConfig.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		tc := tls.Client(nc, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		nc = tc
	}

	var raw [16]byte
	rand.Read(raw[:])
	key := base64.StdEncoding.EncodeToString(raw[:])
	h := header.Clone()
	if h == nil {
		h = http.Header{}
	}
	for _, k := range []string{"Host", "Sec-WebSocket-Extensions", "Sec-WebSocket-Protocol"} {
		h.Del(k)
	}
	h.Set("Upgrade", "websocket")
	h.Set("Connection", "Upgrade")
	h.Set("Sec-WebSocket-Key", key)
	h.Set("Sec-WebSocket-Version", "13")
	var b bytes.Buffer
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\n", u.RequestURI(), u.Host)
	h.Write(&b)
	b.WriteString("\r\n")
	if _, err := nc.Write(b.Bytes()); err != nil {
		return nil, err
	}

	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, fmt.Errorf("redirect refused (status %d)", resp.StatusCode)
	case resp.StatusCode != http.StatusSwitchingProtocols:
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	case !hasToken(resp.Header, "Upgrade", "websocket") || !hasToken(resp.Header, "Connection", "upgrade"):
		return nil, errors.New("response is not a websocket upgrade")
	case resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key):
		return nil, errors.New("invalid Sec-WebSocket-Accept")
	case resp.Header.Get("Sec-WebSocket-Extensions") != "" || resp.Header.Get("Sec-WebSocket-Protocol") != "":
		return nil, errors.New("server selected an extension or subprotocol that was not requested")
	}
	nc.SetDeadline(time.Time{})
	return newConn(nc, br, true, opts), nil
}

// Accept upgrades a server request to a WebSocket connection with default
// options. On a bad request it writes the HTTP error response itself.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	reject := func(status int, msg string) (*Conn, error) {
		http.Error(w, msg, status)
		return nil, errors.New("wsclient: accept: " + msg)
	}
	if r.Method != http.MethodGet {
		return reject(http.StatusMethodNotAllowed, "method must be GET")
	}
	if !hasToken(r.Header, "Connection", "upgrade") || !hasToken(r.Header, "Upgrade", "websocket") {
		return reject(http.StatusBadRequest, "not a websocket upgrade")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		return reject(http.StatusUpgradeRequired, "unsupported websocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
		return reject(http.StatusBadRequest, "invalid Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return reject(http.StatusInternalServerError, "response does not support hijacking")
	}
	nc, brw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("wsclient: accept: %w", err)
	}
	nc.SetDeadline(time.Time{})
	fmt.Fprintf(brw.Writer, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", acceptKey(key))
	if err := brw.Writer.Flush(); err != nil {
		nc.Close()
		return nil, fmt.Errorf("wsclient: accept: %w", err)
	}
	return newConn(nc, brw.Reader, false, Options{}), nil
}

func hasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}
