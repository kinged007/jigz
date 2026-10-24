// Package fakehub is an in-memory hub for acceptance and client tests. It
// serves exactly the daemon routes of the hub protocol v0 (enroll, config,
// status, runs and the WebSocket command channel) and nothing else. Bodies
// are decoded into internal/hubproto types with unknown fields refused and
// validated. A request that breaks the protocol is answered with an error
// and listed by Unexpected, so a client bug fails a test instead of passing.
package fakehub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/wsclient"
)

const (
	base        = "/api/daemon/v0/"
	ackTimeout  = 10 * time.Second
	defaultBeat = 30
	minBeat     = 5
)

// Server is the fake hub. It is an http.Handler.
type Server struct {
	enrollToken string

	mu          sync.Mutex
	enrolled    bool
	daemonID    string
	credential  string
	revoked     bool
	down        bool
	config      *hubproto.ConfigResponse
	etag        string
	configGen   int
	interval    int
	enrollments []hubproto.EnrollRequest
	statuses    []hubproto.StatusRequest
	runs        []hubproto.RunEvent
	bodies      []string
	sock        *socket
	nextCmd     int
	failRuns    int
	failStatus  int

	umu        sync.Mutex
	unexpected []string
}

// socket is one accepted command connection. It owns the commands sent on it.
type socket struct {
	conn    *wsclient.Conn
	pending map[string]chan hubproto.Ack
	closed  chan struct{}
}

// New returns a hub that accepts enrollment with enrollToken, once.
func New(enrollToken string) *Server {
	return &Server{enrollToken: enrollToken, interval: defaultBeat}
}

// SetConfig makes cfg the served config under a new ETag and returns the
// ETag. It panics when cfg is not a valid config message.
func (s *Server) SetConfig(cfg hubproto.ConfigResponse) string {
	if err := cfg.Validate(); err != nil {
		panic("fakehub: SetConfig: " + err.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configGen++
	s.config = &cfg
	s.etag = `"cfg-` + strconv.Itoa(s.configGen) + `"`
	return s.etag
}

// SetInterval sets the heartbeat interval returned by status (default 30,
// minimum 5 seconds).
func (s *Server) SetInterval(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interval = max(seconds, minBeat)
}

// FailRuns makes the next n runs requests answer status (429 or 503, with
// Retry-After: 1) without recording their events.
func (s *Server) FailRuns(status, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failStatus, s.failRuns = status, n
}

// Statuses returns every status document received, in order.
func (s *Server) Statuses() []hubproto.StatusRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.statuses)
}

// Runs returns every run event received, in order. An event sent again is
// listed again, although it is accepted either way.
func (s *Server) Runs() []hubproto.RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.runs)
}

// Enrollments returns the accepted enroll requests.
func (s *Server) Enrollments() []hubproto.EnrollRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.enrollments)
}

// Bodies returns the body of every request after enrollment, so a test can
// assert that no secret was sent. The enroll body is left out because it
// carries the enrollment token by design.
func (s *Server) Bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bodies)
}

// Credential returns the credential issued by enrollment, or "".
func (s *Server) Credential() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credential
}

// Connected reports whether a command socket is open.
func (s *Server) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sock != nil
}

// SetDown makes every request answer 503 and closes the socket while down.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	s.down = down
	s.mu.Unlock()
	if down {
		s.closeSocket()
	}
}

// Revoke makes the issued credential fail with 401 and closes the socket.
func (s *Server) Revoke() {
	s.mu.Lock()
	s.revoked = true
	s.mu.Unlock()
	s.closeSocket()
}

// Unexpected lists the requests and socket messages that broke the protocol.
func (s *Server) Unexpected() []string {
	s.umu.Lock()
	defer s.umu.Unlock()
	return slices.Clone(s.unexpected)
}

// Send writes cmd to the socket and returns the daemon's acknowledgement.
// An empty ID is replaced by a generated one and nil Args by {}.
func (s *Server) Send(cmd hubproto.Command) (hubproto.Ack, error) {
	s.mu.Lock()
	sk := s.sock
	if cmd.ID == "" {
		s.nextCmd++
		cmd.ID = "c-" + strconv.Itoa(s.nextCmd)
	}
	if cmd.Args == nil {
		cmd.Args = json.RawMessage("{}")
	}
	if sk == nil {
		s.mu.Unlock()
		return hubproto.Ack{}, errors.New("fakehub: no socket connected")
	}
	if _, dup := sk.pending[cmd.ID]; dup {
		s.mu.Unlock()
		return hubproto.Ack{}, fmt.Errorf("fakehub: command id %q already pending", cmd.ID)
	}
	ch := make(chan hubproto.Ack, 1)
	sk.pending[cmd.ID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(sk.pending, cmd.ID)
		s.mu.Unlock()
	}()

	if err := cmd.Validate(); err != nil {
		return hubproto.Ack{}, fmt.Errorf("fakehub: invalid command: %w", err)
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return hubproto.Ack{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ackTimeout)
	defer cancel()
	if err := sk.conn.Write(ctx, data); err != nil {
		return hubproto.Ack{}, err
	}
	select {
	case ack := <-ch:
		return ack, nil
	case <-sk.closed:
		select {
		case ack := <-ch:
			return ack, nil
		default:
		}
		return hubproto.Ack{}, errors.New("fakehub: socket closed before the ack")
	case <-ctx.Done():
		return hubproto.Ack{}, errors.New("fakehub: timed out waiting for the ack")
	}
}

func (s *Server) closeSocket() {
	s.mu.Lock()
	sk := s.sock
	s.sock = nil
	s.mu.Unlock()
	if sk != nil {
		go sk.conn.Close(wsclient.StatusGoingAway, "")
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	route := r.Method + " " + r.URL.Path

	s.mu.Lock()
	down, revoked, cred, id := s.down, s.revoked, s.credential, s.daemonID
	s.mu.Unlock()
	if down {
		writeError(w, http.StatusServiceUnavailable, hubproto.ErrUnavailable, "hub is down")
		return
	}

	handler := s.routes()[route]
	if handler == nil {
		s.reject(w, r, http.StatusNotFound, hubproto.ErrNotFound, "unknown route")
		return
	}
	if r.Header.Get("Jig-Version") == "" {
		s.reject(w, r, http.StatusBadRequest, hubproto.ErrInvalid, "missing Jig-Version")
		return
	}
	if route == "POST "+base+"enroll" {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Jig-Daemon-Id") != "" {
			s.reject(w, r, http.StatusBadRequest, hubproto.ErrInvalid, "enroll carries credentials")
			return
		}
		handler(w, r, body)
		return
	}

	bearer := r.Header.Get("Authorization")
	switch {
	case cred == "" || bearer != "Bearer "+cred:
		s.reject(w, r, http.StatusUnauthorized, hubproto.ErrUnauthorized, "wrong bearer")
		return
	case revoked:
		writeError(w, http.StatusUnauthorized, hubproto.ErrUnauthorized, "credential revoked")
		return
	case r.Header.Get("Jig-Daemon-Id") != id:
		s.reject(w, r, http.StatusBadRequest, hubproto.ErrInvalid, "missing or wrong Jig-Daemon-Id")
		return
	}
	s.mu.Lock()
	s.bodies = append(s.bodies, string(body))
	s.mu.Unlock()
	handler(w, r, body)
}

type routeFunc func(w http.ResponseWriter, r *http.Request, body []byte)

func (s *Server) routes() map[string]routeFunc {
	return map[string]routeFunc{
		"POST " + base + "enroll": s.enroll,
		"GET " + base + "config":  s.getConfig,
		"POST " + base + "status": s.postStatus,
		"POST " + base + "runs":   s.postRuns,
		"GET " + base + "ws":      s.socket,
	}
}

type validator interface{ Validate() error }

// decode reads body into v with unknown fields refused, then validates it.
// On failure it answers 400 and records the rejection.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, body []byte, v validator) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil && dec.More() {
		err = errors.New("trailing data")
	}
	if err == nil {
		err = v.Validate()
	}
	if err != nil {
		s.reject(w, r, http.StatusBadRequest, hubproto.ErrInvalid, "invalid body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request, body []byte) {
	var tok struct {
		Token string `json:"token"`
	}
	json.Unmarshal(body, &tok)
	s.mu.Lock()
	refused := s.enrolled || tok.Token != s.enrollToken
	s.mu.Unlock()
	if refused {
		s.reject(w, r, http.StatusUnauthorized, hubproto.ErrUnauthorized, "enrollment token refused")
		return
	}
	var req hubproto.EnrollRequest
	if !s.decode(w, r, body, &req) {
		return
	}
	s.mu.Lock()
	if s.enrolled {
		s.mu.Unlock()
		s.reject(w, r, http.StatusUnauthorized, hubproto.ErrUnauthorized, "enrollment token refused")
		return
	}
	s.enrolled = true
	s.daemonID = "d-1"
	s.credential = randomHex()
	req.Token = ""
	s.enrollments = append(s.enrollments, req)
	resp := hubproto.EnrollResponse{DaemonID: s.daemonID, Credential: s.credential, RunnerLabel: "runner:" + req.Name}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	cfg, etag := s.config, s.etag
	s.mu.Unlock()
	if cfg == nil {
		s.reject(w, r, http.StatusNotFound, hubproto.ErrNotFound, "no config set")
		return
	}
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// etagMatches implements the weak comparison of If-None-Match.
func etagMatches(header, etag string) bool {
	for v := range strings.SplitSeq(header, ",") {
		v = strings.TrimSpace(v)
		if v == "*" || strings.TrimPrefix(v, "W/") == etag {
			return true
		}
	}
	return false
}

func (s *Server) postStatus(w http.ResponseWriter, r *http.Request, body []byte) {
	var req hubproto.StatusRequest
	if !s.decode(w, r, body, &req) {
		return
	}
	s.mu.Lock()
	s.statuses = append(s.statuses, req)
	resp := hubproto.StatusResponse{IntervalSeconds: s.interval, ConfigETag: s.etag}
	s.mu.Unlock()
	if resp.ConfigETag == "" {
		resp.ConfigETag = `"none"`
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) postRuns(w http.ResponseWriter, r *http.Request, body []byte) {
	s.mu.Lock()
	fail := s.failRuns > 0
	if fail {
		s.failRuns--
	}
	status := s.failStatus
	s.mu.Unlock()
	if fail {
		w.Header().Set("Retry-After", "1")
		writeError(w, status, hubproto.ErrUnavailable, "fakehub: runs failure injected")
		return
	}
	var req hubproto.RunsRequest
	if !s.decode(w, r, body, &req) {
		return
	}
	resp := hubproto.RunsResponse{Accepted: make([]string, 0, len(req.Events))}
	for _, e := range req.Events {
		resp.Accepted = append(resp.Accepted, e.ID)
	}
	s.mu.Lock()
	s.runs = append(s.runs, req.Events...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) socket(w http.ResponseWriter, r *http.Request, _ []byte) {
	conn, err := wsclient.Accept(w, r)
	if err != nil {
		s.note(r, "websocket upgrade: "+err.Error())
		return
	}
	sk := &socket{conn: conn, pending: map[string]chan hubproto.Ack{}, closed: make(chan struct{})}
	s.mu.Lock()
	old := s.sock
	s.sock = sk
	s.mu.Unlock()
	if old != nil {
		go old.conn.Close(wsclient.StatusGoingAway, "replaced")
	}
	s.readAcks(sk)
}

// readAcks delivers acknowledgements to Send until the connection ends.
func (s *Server) readAcks(sk *socket) {
	conn := sk.conn
	defer func() {
		s.mu.Lock()
		if s.sock == sk {
			s.sock = nil
		}
		s.mu.Unlock()
		close(sk.closed)
		conn.Close(wsclient.StatusNormal, "")
	}()
	for {
		msg, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		var ack hubproto.Ack
		dec := json.NewDecoder(bytes.NewReader(msg))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ack); err != nil {
			s.noteMsg("invalid ack: " + err.Error())
			continue
		}
		if err := ack.Validate(); err != nil {
			s.noteMsg("invalid ack: " + err.Error())
			continue
		}
		s.mu.Lock()
		ch := sk.pending[ack.ID]
		delete(sk.pending, ack.ID)
		s.mu.Unlock()
		if ch == nil {
			s.noteMsg("ack for unknown command " + ack.ID)
			continue
		}
		ch <- ack
	}
}

func (s *Server) reject(w http.ResponseWriter, r *http.Request, status int, code hubproto.ErrorCode, why string) {
	s.note(r, why)
	writeError(w, status, code, "fakehub: "+why)
}

func (s *Server) note(r *http.Request, why string) {
	s.noteMsg(fmt.Sprintf("%s %s: %s", r.Method, r.URL.Path, why))
}

func (s *Server) noteMsg(msg string) {
	s.umu.Lock()
	s.unexpected = append(s.unexpected, msg)
	s.umu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code hubproto.ErrorCode, msg string) {
	writeJSON(w, status, hubproto.ErrorBody{Code: code, Message: msg})
}

func randomHex() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
