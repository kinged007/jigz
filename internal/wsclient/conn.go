// Package wsclient implements the part of RFC 6455 that the daemon's command
// channel needs, with the standard library only: a client Dial, a server
// Accept for tests and the fake hub, text and binary messages, fragmentation,
// ping and pong, the close handshake, a read size limit and context
// cancellation. Extensions (including compression) and subprotocols are not
// supported: Dial requests none and fails if the server selects one.
package wsclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// Close status codes (RFC 6455 section 7.4.1).
const (
	StatusNormal          = 1000
	StatusGoingAway       = 1001
	StatusProtocolError   = 1002
	StatusUnsupportedData = 1003
	StatusNoStatus        = 1005
	StatusInvalidData     = 1007
	StatusPolicyViolation = 1008
	StatusMessageTooBig   = 1009
	StatusInternalError   = 1011
)

// MessageType is the type of a data message.
type MessageType int

const (
	MessageText   MessageType = opText
	MessageBinary MessageType = opBinary
)

const (
	DefaultReadLimit    = 1 << 20
	defaultWriteTimeout = 10 * time.Second
	closeWait           = 5 * time.Second
	failWriteTimeout    = time.Second
)

// Options configures a connection. Zero values select the defaults.
type Options struct {
	// ReadLimit is the largest message Read accepts, in bytes. A larger
	// message closes the connection with 1009. Default DefaultReadLimit.
	ReadLimit int64
	// WriteTimeout bounds each frame write. Default 10 s.
	WriteTimeout time.Duration
	// PingInterval sends a ping at this interval when positive.
	PingInterval time.Duration
	// PongTimeout closes the connection when no pong has arrived for this
	// long, checked at each ping. Pongs are processed while Read runs.
	PongTimeout time.Duration
	// HandshakeTimeout bounds Dial when positive.
	HandshakeTimeout time.Duration
	// TLSConfig is used for wss URLs. ServerName defaults to the URL host.
	TLSConfig *tls.Config
}

// ErrClosed is returned by operations on a connection that is closed or
// closing.
var ErrClosed = errors.New("wsclient: connection closed")

// CloseError reports a connection ended by a close frame. Remote is true when
// the peer sent the close frame, false when this side closed it after a
// protocol violation.
type CloseError struct {
	Code   int
	Reason string
	Remote bool
}

func (e *CloseError) Error() string {
	who := "local"
	if e.Remote {
		who = "peer"
	}
	return fmt.Sprintf("wsclient: %s closed the connection: %d %s", who, e.Code, e.Reason)
}

// Conn is a WebSocket connection. Read must not be called concurrently with
// itself; Write and Close are safe to call from any goroutine.
type Conn struct {
	nc     net.Conn
	br     *bufio.Reader
	client bool
	opts   Options

	readMu  sync.Mutex
	readErr error

	writeMu   sync.Mutex
	closeSent bool

	closeRecv chan struct{}
	recvOnce  sync.Once
	done      chan struct{}
	doneOnce  sync.Once
	lastPong  atomic.Int64
}

func newConn(nc net.Conn, br *bufio.Reader, client bool, opts Options) *Conn {
	if opts.ReadLimit <= 0 {
		opts.ReadLimit = DefaultReadLimit
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}
	c := &Conn{
		nc:        nc,
		br:        br,
		client:    client,
		opts:      opts,
		closeRecv: make(chan struct{}),
		done:      make(chan struct{}),
	}
	c.lastPong.Store(time.Now().UnixNano())
	if opts.PingInterval > 0 {
		go c.keepalive()
	}
	return c
}

// abort closes the network connection without a close handshake.
func (c *Conn) abort() {
	c.doneOnce.Do(func() {
		close(c.done)
		c.nc.Close()
	})
}

func (c *Conn) keepalive() {
	t := time.NewTicker(c.opts.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		if c.opts.PongTimeout > 0 && time.Since(time.Unix(0, c.lastPong.Load())) > c.opts.PongTimeout {
			c.abort()
			return
		}
		if err := c.writeFrame(context.Background(), opPing, nil); err != nil {
			return
		}
	}
}

// Write sends p as one text message. p must be valid UTF-8. Cancelling ctx
// during the write closes the connection.
func (c *Conn) Write(ctx context.Context, p []byte) error {
	if !utf8.Valid(p) {
		return errors.New("wsclient: write: text message is not valid UTF-8")
	}
	return c.writeFrame(ctx, opText, p)
}

// Read returns the next data message. Cancelling ctx during the read closes
// the connection.
func (c *Conn) Read(ctx context.Context) ([]byte, error) {
	_, p, err := c.ReadMessage(ctx)
	return p, err
}

// ReadMessage returns the next data message and its type. Pings are answered
// and pongs recorded while it runs.
func (c *Conn) ReadMessage(ctx context.Context) (MessageType, []byte, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.readErr != nil {
		return 0, nil, c.readErr
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, fmt.Errorf("wsclient: read: %w", err)
	}
	stop := context.AfterFunc(ctx, c.abort)
	typ, p, err := c.readMessage()
	stop()
	if err != nil {
		var ce *CloseError
		switch {
		case errors.As(err, &ce):
		case ctx.Err() != nil:
			err = fmt.Errorf("wsclient: read: %w", ctx.Err())
		case c.isDone():
			err = ErrClosed
		}
		c.readErr = err
	}
	return typ, p, err
}

// Close sends a close frame with code and reason, waits up to 5 s for the
// peer's close frame, and closes the connection. A code of 0 or 1005 sends a
// close frame without a status.
func (c *Conn) Close(code int, reason string) error {
	err := c.writeFrame(context.Background(), opClose, closePayload(code, reason))
	if err == nil {
		c.waitPeerClose()
	}
	c.abort()
	if errors.Is(err, ErrClosed) {
		return nil
	}
	return err
}

func (c *Conn) waitPeerClose() {
	if c.readMu.TryLock() {
		defer c.readMu.Unlock()
		if c.readErr != nil {
			return
		}
		c.nc.SetReadDeadline(time.Now().Add(closeWait))
		for {
			if _, _, err := c.readMessage(); err != nil {
				break
			}
		}
		c.readErr = ErrClosed
		return
	}
	t := time.NewTimer(closeWait)
	defer t.Stop()
	select {
	case <-c.closeRecv:
	case <-c.done:
	case <-t.C:
	}
}

func (c *Conn) isDone() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *Conn) writeFrame(ctx context.Context, op byte, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("wsclient: write: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closeSent || c.isDone() {
		return ErrClosed
	}
	if op == opClose {
		c.closeSent = true
	}
	deadline := time.Now().Add(c.opts.WriteTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.nc.SetWriteDeadline(deadline)
	stop := context.AfterFunc(ctx, c.abort)
	defer stop()
	if _, err := c.nc.Write(c.frame(op, payload)); err != nil {
		// A partial frame leaves the stream unusable.
		c.abort()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fmt.Errorf("wsclient: write: %w", err)
	}
	return nil
}

func (c *Conn) frame(op byte, payload []byte) []byte {
	n := len(payload)
	buf := make([]byte, 0, 14+n)
	buf = append(buf, 0x80|op)
	var maskBit byte
	if c.client {
		maskBit = 0x80
	}
	switch {
	case n <= 125:
		buf = append(buf, maskBit|byte(n))
	case n <= 0xFFFF:
		buf = append(buf, maskBit|126)
		buf = binary.BigEndian.AppendUint16(buf, uint16(n))
	default:
		buf = append(buf, maskBit|127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(n))
	}
	if !c.client {
		return append(buf, payload...)
	}
	var key [4]byte
	rand.Read(key[:])
	buf = append(buf, key[:]...)
	start := len(buf)
	buf = append(buf, payload...)
	mask(key, buf[start:])
	return buf
}

func mask(key [4]byte, p []byte) {
	for i := range p {
		p[i] ^= key[i&3]
	}
}

type header struct {
	fin    bool
	op     byte
	masked bool
	key    [4]byte
	length int64
}

func (c *Conn) readMessage() (MessageType, []byte, error) {
	var typ MessageType
	var msg []byte
	for {
		h, err := c.readHeader()
		if err != nil {
			return 0, nil, err
		}
		if h.op >= opClose {
			payload, err := c.readPayload(h)
			if err != nil {
				return 0, nil, err
			}
			if err := c.control(h.op, payload); err != nil {
				return 0, nil, err
			}
			continue
		}
		switch {
		case h.op == opContinuation && typ == 0:
			return 0, nil, c.fail(StatusProtocolError, "unexpected continuation frame")
		case h.op != opContinuation && typ != 0:
			return 0, nil, c.fail(StatusProtocolError, "expected continuation frame")
		}
		if h.length > c.opts.ReadLimit-int64(len(msg)) {
			return 0, nil, c.fail(StatusMessageTooBig, "message exceeds the read limit")
		}
		p, err := c.readPayload(h)
		if err != nil {
			return 0, nil, err
		}
		if typ == 0 {
			typ = MessageType(h.op)
			msg = []byte{}
		}
		msg = append(msg, p...)
		if h.fin {
			if typ == MessageText && !utf8.Valid(msg) {
				return 0, nil, c.fail(StatusInvalidData, "text message is not valid UTF-8")
			}
			return typ, msg, nil
		}
	}
}

func (c *Conn) control(op byte, payload []byte) error {
	switch op {
	case opPing:
		c.writeFrame(context.Background(), opPong, payload)
	case opPong:
		c.lastPong.Store(time.Now().UnixNano())
	case opClose:
		code, reason := StatusNoStatus, ""
		if len(payload) == 1 {
			return c.fail(StatusProtocolError, "invalid close frame")
		}
		if len(payload) >= 2 {
			code = int(binary.BigEndian.Uint16(payload))
			if !validCloseCode(code) {
				return c.fail(StatusProtocolError, "invalid close code")
			}
			if !utf8.Valid(payload[2:]) {
				return c.fail(StatusInvalidData, "close reason is not valid UTF-8")
			}
			reason = string(payload[2:])
		}
		c.recvOnce.Do(func() { close(c.closeRecv) })
		c.writeFrame(context.Background(), opClose, closePayload(code, ""))
		c.abort()
		return &CloseError{Code: code, Reason: reason, Remote: true}
	}
	return nil
}

func (c *Conn) readHeader() (header, error) {
	var b [8]byte
	if _, err := io.ReadFull(c.br, b[:2]); err != nil {
		return header{}, c.readFailed(err)
	}
	h := header{fin: b[0]&0x80 != 0, op: b[0] & 0x0F, masked: b[1]&0x80 != 0}
	if b[0]&0x70 != 0 {
		return h, c.fail(StatusProtocolError, "reserved bits set")
	}
	switch h.op {
	case opContinuation, opText, opBinary, opClose, opPing, opPong:
	default:
		return h, c.fail(StatusProtocolError, "unknown opcode")
	}
	if h.masked == c.client {
		if c.client {
			return h, c.fail(StatusProtocolError, "masked frame from server")
		}
		return h, c.fail(StatusProtocolError, "unmasked frame from client")
	}
	n := int64(b[1] & 0x7F)
	switch n {
	case 126:
		if _, err := io.ReadFull(c.br, b[:2]); err != nil {
			return h, c.readFailed(err)
		}
		n = int64(binary.BigEndian.Uint16(b[:2]))
	case 127:
		if _, err := io.ReadFull(c.br, b[:8]); err != nil {
			return h, c.readFailed(err)
		}
		v := binary.BigEndian.Uint64(b[:8])
		if v>>63 != 0 {
			return h, c.fail(StatusProtocolError, "invalid frame length")
		}
		n = int64(v)
	}
	if h.op >= opClose && (!h.fin || n > 125) {
		return h, c.fail(StatusProtocolError, "invalid control frame")
	}
	if h.masked {
		if _, err := io.ReadFull(c.br, h.key[:]); err != nil {
			return h, c.readFailed(err)
		}
	}
	h.length = n
	return h, nil
}

func (c *Conn) readPayload(h header) ([]byte, error) {
	p := make([]byte, h.length)
	if _, err := io.ReadFull(c.br, p); err != nil {
		return nil, c.readFailed(err)
	}
	if h.masked {
		mask(h.key, p)
	}
	return p, nil
}

func (c *Conn) readFailed(err error) error {
	c.abort()
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return fmt.Errorf("wsclient: read: %w", err)
}

// fail sends a close frame with code, closes the connection and returns the
// matching error.
func (c *Conn) fail(code int, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), failWriteTimeout)
	c.writeFrame(ctx, opClose, closePayload(code, reason))
	cancel()
	c.abort()
	return &CloseError{Code: code, Reason: reason}
}

func closePayload(code int, reason string) []byte {
	if code == 0 || code == StatusNoStatus {
		return nil
	}
	if len(reason) > 123 {
		reason = reason[:123]
		for !utf8.ValidString(reason) {
			reason = reason[:len(reason)-1]
		}
	}
	return append(binary.BigEndian.AppendUint16(nil, uint16(code)), reason...)
}

func validCloseCode(code int) bool {
	return (code >= 1000 && code <= 1003) || (code >= 1007 && code <= 1011) || (code >= 3000 && code <= 4999)
}
