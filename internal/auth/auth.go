// Package auth provides password login, signed session cookies, CSRF checks
// and a login rate limit for sortie-central.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	CookieName    = "sortie_session"
	CSRFHeader    = "X-CSRF-Token"
	CSRFField     = "csrf"
	DefaultTTL    = 12 * time.Hour
	maxFailures   = 5
	failureWindow = 15 * time.Minute
	// sweepAbove is the number of tracked IPs above which fail drops the
	// entries whose failures have all left the window.
	sweepAbove = 256
)

type Config struct {
	// Password empty means the app is open.
	Password   string
	PublicRead bool
	Secret     []byte
	TTL        time.Duration
	// TrustedProxy makes X-Forwarded-Proto and X-Forwarded-For trusted.
	TrustedProxy bool
	Now          func() time.Time
	// OnEvent is called on login and logout; result is "ok" or "fail".
	OnEvent func(actor, action, result string)
	// LoginPage renders the login form. Nil uses a plain built-in form.
	// The status code is already written when it is called.
	LoginPage func(w http.ResponseWriter, data LoginData)
}

// LoginData is what a login page renders.
type LoginData struct {
	Next  string
	Error string
	// PublicRead is whether anonymous visitors may read, so the page can
	// offer to continue without signing in.
	PublicRead bool
}

type Auth struct {
	cfg        Config
	publicRead atomic.Bool
	mu         sync.Mutex
	// failures holds the failure times inside the window per client IP.
	failures map[string][]time.Time
}

// Info describes the request's access state for handlers and templates.
type Info struct {
	Authenticated bool
	CSRF          string
	PasswordSet   bool
	PublicRead    bool
	// Actor names the client for audit rows: its IP, or "open" when no
	// password is set.
	Actor string
}

type ctxKey struct{}

func New(cfg Config) *Auth {
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	a := &Auth{cfg: cfg, failures: map[string][]time.Time{}}
	a.publicRead.Store(cfg.PublicRead)
	return a
}

// SetPublicRead changes whether anonymous requests may read.
func (a *Auth) SetPublicRead(v bool) { a.publicRead.Store(v) }

// LoadSecret returns env when set, else the key stored in <stateDir>/session.key,
// creating it on first use.
func LoadSecret(stateDir, env string) ([]byte, error) {
	if env != "" {
		return []byte(env), nil
	}
	path := filepath.Join(stateDir, "session.key")
	if b, err := os.ReadFile(path); err == nil {
		key := strings.TrimSpace(string(b))
		if key == "" {
			return nil, errors.New("auth: empty " + path)
		}
		return []byte(key), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(raw)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return nil, err
	}
	return []byte(key), nil
}

// Session returns the access state set by Protect. Without Protect it reports
// an open, authenticated request.
func Session(r *http.Request) Info {
	if i, ok := r.Context().Value(ctxKey{}).(Info); ok {
		return i
	}
	return Info{Authenticated: true, Actor: "open"}
}

func (a *Auth) sign(msg string) string {
	m := hmac.New(sha256.New, a.cfg.Secret)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *Auth) csrfFor(payload string) string {
	return a.sign("csrf:" + payload)[:32]
}

func (a *Auth) newCookieValue() (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	payload := hex.EncodeToString(id) + "." + strconv.FormatInt(a.cfg.Now().Add(a.cfg.TTL).Unix(), 10)
	return payload + "." + a.sign(payload), nil
}

// parse validates a cookie value and returns its payload.
func (a *Auth) parse(value string) (payload string, ok bool) {
	i := strings.LastIndexByte(value, '.')
	if i < 0 {
		return "", false
	}
	payload, sig := value[:i], value[i+1:]
	if !hmac.Equal([]byte(sig), []byte(a.sign(payload))) {
		return "", false
	}
	_, expStr, found := strings.Cut(payload, ".")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if !found || err != nil || a.cfg.Now().Unix() >= exp {
		return "", false
	}
	return payload, true
}

func (a *Auth) info(r *http.Request) Info {
	i := Info{PasswordSet: a.cfg.Password != "", PublicRead: a.publicRead.Load(), Actor: "open"}
	if !i.PasswordSet {
		i.Authenticated = true
		return i
	}
	i.Actor = a.clientIP(r)
	if c, err := r.Cookie(CookieName); err == nil {
		if payload, ok := a.parse(c.Value); ok {
			i.Authenticated = true
			i.CSRF = a.csrfFor(payload)
		}
	}
	return i
}

func (a *Auth) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return a.cfg.TrustedProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (a *Auth) setCookie(w http.ResponseWriter, r *http.Request, value string, expires time.Time) {
	c := &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   a.secure(r),
	}
	if value == "" {
		c.MaxAge = -1
	} else {
		c.Expires = expires
	}
	http.SetCookie(w, c)
}

func (a *Auth) clientIP(r *http.Request) string {
	if a.cfg.TrustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// blocked reports whether ip is rate limited and, if so, the wait in seconds.
func (a *Auth) blocked(ip string) (bool, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	recent := a.failures[ip][:0]
	for _, t := range a.failures[ip] {
		if now.Sub(t) < failureWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(a.failures, ip)
		return false, 0
	}
	a.failures[ip] = recent
	if len(recent) < maxFailures {
		return false, 0
	}
	wait := int((failureWindow - now.Sub(recent[0])).Seconds()) + 1
	return true, wait
}

func (a *Auth) fail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	if len(a.failures) > sweepAbove {
		for k, ts := range a.failures {
			if now.Sub(ts[len(ts)-1]) >= failureWindow {
				delete(a.failures, k)
			}
		}
	}
	a.failures[ip] = append(a.failures[ip], now)
}

func (a *Auth) reset(ip string) {
	a.mu.Lock()
	delete(a.failures, ip)
	a.mu.Unlock()
}

func (a *Auth) event(r *http.Request, action, result string) {
	if a.cfg.OnEvent != nil {
		a.cfg.OnEvent(a.clientIP(r), action, result)
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// originOK rejects a request whose Origin, or Referer when there is no
// Origin, names a host other than the request Host.
func originOK(r *http.Request) bool {
	ref := r.Header.Get("Origin")
	if ref == "" {
		ref = r.Header.Get("Referer")
	}
	if ref == "" {
		return true
	}
	u, err := url.Parse(ref)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

func csrfOK(r *http.Request, token string) bool {
	got := r.Header.Get(CSRFHeader)
	if got == "" {
		got = r.PostFormValue(CSRFField)
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// SafeNext returns next when it is a same-origin relative path, else "/".
func SafeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return next
}

func isAPI(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api"
}

func deny(w http.ResponseWriter, r *http.Request, code int, msg string) {
	if isAPI(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"error":` + strconv.Quote(msg) + `}` + "\n"))
		return
	}
	http.Error(w, msg, code)
}

// Protect enforces access control around next and serves /login and /logout.
func (a *Auth) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := a.info(r)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, info))
		if !info.PasswordSet {
			next.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/login":
			a.login(w, r, info)
			return
		case "/logout":
			a.logout(w, r, info)
			return
		}
		if safeMethod(r.Method) {
			if info.Authenticated || info.PublicRead {
				next.ServeHTTP(w, r)
				return
			}
			if isAPI(r) {
				deny(w, r, http.StatusUnauthorized, "login required")
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if !info.Authenticated {
			deny(w, r, http.StatusUnauthorized, "login required")
			return
		}
		if !originOK(r) || !csrfOK(r, info.CSRF) {
			deny(w, r, http.StatusForbidden, "csrf check failed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in</title></head><body>
<form method="post" action="/login">
<h1>Sign in</h1>
{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}
<input type="hidden" name="next" value="{{.Next}}">
<label>Password <input type="password" name="password" autofocus required></label>
<button type="submit">Sign in</button>
</form></body></html>
`))

func (a *Auth) renderLogin(w http.ResponseWriter, code int, next, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	data := LoginData{Next: next, Error: msg, PublicRead: a.publicRead.Load()}
	if a.cfg.LoginPage != nil {
		a.cfg.LoginPage(w, data)
		return
	}
	_ = loginTmpl.Execute(w, data)
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request, info Info) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		a.renderLogin(w, http.StatusOK, SafeNext(r.URL.Query().Get("next")), "")
	case http.MethodPost:
		if !originOK(r) {
			deny(w, r, http.StatusForbidden, "origin check failed")
			return
		}
		next := SafeNext(r.PostFormValue("next"))
		ip := a.clientIP(r)
		if blocked, wait := a.blocked(ip); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(wait))
			a.event(r, "login", "fail")
			a.renderLogin(w, http.StatusTooManyRequests, next, "Too many attempts. Try again later.")
			return
		}
		pw := r.PostFormValue("password")
		if subtle.ConstantTimeCompare([]byte(pw), []byte(a.cfg.Password)) != 1 {
			a.fail(ip)
			a.event(r, "login", "fail")
			a.renderLogin(w, http.StatusUnauthorized, next, "Wrong password.")
			return
		}
		value, err := a.newCookieValue()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		a.reset(ip)
		a.setCookie(w, r, value, a.cfg.Now().Add(a.cfg.TTL))
		a.event(r, "login", "ok")
		http.Redirect(w, r, next, http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request, info Info) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !originOK(r) || (info.Authenticated && !csrfOK(r, info.CSRF)) {
		deny(w, r, http.StatusForbidden, "csrf check failed")
		return
	}
	a.setCookie(w, r, "", time.Time{})
	a.event(r, "logout", "ok")
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
