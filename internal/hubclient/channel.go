package hubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/wsclient"
)

const (
	pingInterval = 30 * time.Second
	pongTimeout  = 90 * time.Second
	// resetAfter is how long a socket must stay up before the back-off
	// starts again at BackoffMin.
	resetAfter = 60 * time.Second
	ackTimeout = 10 * time.Second
)

// supported are the command types a v0 daemon carries out. Every other type,
// including session.* and task.run, is answered with unsupported.
var supported = []hubproto.CommandType{
	hubproto.CmdStart, hubproto.CmdStop, hubproto.CmdDrain, hubproto.CmdRestartWhenIdle,
	hubproto.CmdPause, hubproto.CmdResume, hubproto.CmdWake, hubproto.CmdReloadConfig,
}

// wsclient reports a refused upgrade only in its error text.
var upgradeStatusRe = regexp.MustCompile(`unexpected status (\d{3})`)

// Handler carries out a supported, valid command. It returns nil to
// acknowledge with ok, or the error for the acknowledgement (code invalid
// for unknown projects or workflows, failed otherwise). It runs on the
// receive loop, so it must return promptly.
type Handler func(ctx context.Context, cmd hubproto.Command) *hubproto.AckError

// Connect opens the command socket once, with the credential headers.
func (c *Client) Connect(ctx context.Context) (*wsclient.Conn, error) {
	u, err := c.endpoint("ws")
	if err != nil {
		return nil, c.fail("GET", "ws", false, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil, c.fail("GET", "ws", false, fmt.Errorf("unsupported URL scheme %q", u.Scheme))
	}
	conn, err := wsclient.Dial(ctx, u.String(), c.header(false), wsclient.Options{
		PingInterval:     pingInterval,
		PongTimeout:      pongTimeout,
		HandshakeTimeout: defaultTimeout,
	})
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		return nil, c.fail("GET", "ws", false, ctx.Err())
	}
	if m := upgradeStatusRe.FindStringSubmatch(err.Error()); m != nil {
		status, _ := strconv.Atoi(m[1])
		return nil, c.statusError("GET", "ws", status, nil)
	}
	return nil, c.fail("GET", "ws", true, err)
}

// Commands keeps the command socket open and answers every command through
// h until ctx ends or a connect fails with an error that is not Retryable
// (such as ErrRevoked), which it returns. Between attempts it waits with
// exponential back-off and full jitter, reset after a socket stayed up for
// 60 s.
func (c *Client) Commands(ctx context.Context, h Handler) error {
	b := c.backoff()
	for {
		conn, err := c.Connect(ctx)
		if err == nil {
			start := c.now()
			c.logf("hub: command channel connected")
			err = c.serve(ctx, conn, h)
			if c.now().Sub(start) >= resetAfter {
				b.Reset()
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !Retryable(err) {
			return err
		}
		d := b.Next()
		c.logf("%v; reconnecting in %s", err, d)
		if err := c.sleep(ctx, d); err != nil {
			return err
		}
	}
}

// serve reads commands until the socket ends. The returned error is always
// Retryable unless ctx ended.
func (c *Client) serve(ctx context.Context, conn *wsclient.Conn, h Handler) error {
	defer conn.Close(wsclient.StatusNormal, "")
	for {
		msg, err := conn.Read(ctx)
		if err != nil {
			return c.fail("GET", "ws", ctx.Err() == nil, err)
		}
		ack, ok := c.handle(ctx, msg, h)
		if !ok {
			continue
		}
		data, err := json.Marshal(ack)
		if err != nil {
			return c.fail("GET", "ws", true, err)
		}
		wctx, cancel := context.WithTimeout(ctx, ackTimeout)
		err = conn.Write(wctx, data)
		cancel()
		if err != nil {
			return c.fail("GET", "ws", ctx.Err() == nil, err)
		}
	}
}

// handle returns the acknowledgement for one message, or false when the
// message has no id to answer.
func (c *Client) handle(ctx context.Context, msg []byte, h Handler) (hubproto.Ack, bool) {
	var head struct {
		ID   string               `json:"id"`
		Type hubproto.CommandType `json:"type"`
	}
	if json.Unmarshal(msg, &head) != nil || head.ID == "" {
		c.logf("hub: command without id ignored")
		return hubproto.Ack{}, false
	}
	c.logf("hub: command %q type %q", head.ID, head.Type)
	ack := func(code hubproto.AckCode, message string) (hubproto.Ack, bool) {
		a := hubproto.Ack{ID: head.ID, Type: hubproto.AckMessageType, OK: code == ""}
		if code != "" {
			a.Error = &hubproto.AckError{Code: code, Message: message}
		}
		return a, true
	}
	if !slices.Contains(supported, head.Type) {
		return ack(hubproto.AckUnsupported, string(head.Type))
	}
	var cmd hubproto.Command
	dec := json.NewDecoder(bytes.NewReader(msg))
	dec.DisallowUnknownFields()
	err := dec.Decode(&cmd)
	if err == nil {
		err = cmd.Validate()
	}
	if err != nil {
		return ack(hubproto.AckInvalid, err.Error())
	}
	if e := h(ctx, cmd); e != nil {
		if e.Code == "" {
			e.Code = hubproto.AckFailed
		}
		return ack(e.Code, e.Message)
	}
	return ack("", "")
}
