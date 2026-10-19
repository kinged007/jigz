package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type env struct {
	a   *Auth
	h   http.Handler
	now time.Time
	ev  []string
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	e := &env{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	cfg.Secret = []byte("test-secret")
	cfg.Now = func() time.Time { return e.now }
	cfg.OnEvent = func(actor, action, result string) { e.ev = append(e.ev, action+":"+result) }
	e.a = New(cfg)
	e.h = e.a.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	return e
}

func (e *env) do(method, target string, body url.Values, mod func(*http.Request)) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.RemoteAddr = "10.0.0.1:1234"
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) login(t *testing.T, mod func(*http.Request)) *http.Cookie {
	t.Helper()
	w := e.do("POST", "/login", url.Values{"password": {"pw"}}, mod)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login code %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(c) }
}

func csrfOf(e *env, c *http.Cookie) string {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	return e.a.info(r).CSRF
}

func TestOpenMode(t *testing.T) {
	e := newEnv(t, Config{})
	for _, m := range []string{"GET", "POST", "DELETE"} {
		if w := e.do(m, "/api/v1/x", nil, nil); w.Code != 200 {
			t.Errorf("%s: %d", m, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	e.h.ServeHTTP(httptest.NewRecorder(), r)
	if i := Session(r); !i.Authenticated || i.PasswordSet {
		t.Errorf("info %+v", i)
	}
}

func TestPrivate(t *testing.T) {
	e := newEnv(t, Config{Password: "pw"})
	tests := []struct {
		name   string
		method string
		path   string
		want   int
		loc    string
	}{
		{"page redirect", "GET", "/projects?a=1", 303, "/login?next=%2Fprojects%3Fa%3D1"},
		{"api 401", "GET", "/api/v1/x", 401, ""},
		{"anon mutation 401", "POST", "/api/v1/x", 401, ""},
		{"login page open", "GET", "/login", 200, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := e.do(tc.method, tc.path, nil, nil)
			if w.Code != tc.want {
				t.Fatalf("code %d", w.Code)
			}
			if tc.loc != "" && w.Header().Get("Location") != tc.loc {
				t.Errorf("location %q", w.Header().Get("Location"))
			}
		})
	}
	if w := e.do("GET", "/api/v1/x", nil, nil); !strings.Contains(w.Body.String(), "error") {
		t.Errorf("api body %q", w.Body.String())
	}
}

func TestSessionAndCSRF(t *testing.T) {
	e := newEnv(t, Config{Password: "pw"})
	c := e.login(t, nil)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
		t.Errorf("cookie %+v", c)
	}
	tok := csrfOf(e, c)
	if tok == "" {
		t.Fatal("no csrf token")
	}
	tests := []struct {
		name string
		body url.Values
		mod  func(*http.Request)
		want int
	}{
		{"no csrf", nil, withCookie(c), 403},
		{"bad csrf", nil, func(r *http.Request) { r.AddCookie(c); r.Header.Set(CSRFHeader, "x") }, 403},
		{"header", nil, func(r *http.Request) { r.AddCookie(c); r.Header.Set(CSRFHeader, tok) }, 200},
		{"form field", url.Values{CSRFField: {tok}}, withCookie(c), 200},
		{"origin mismatch", nil, func(r *http.Request) {
			r.AddCookie(c)
			r.Header.Set(CSRFHeader, tok)
			r.Header.Set("Origin", "http://evil.test")
		}, 403},
		{"origin match", nil, func(r *http.Request) {
			r.AddCookie(c)
			r.Header.Set(CSRFHeader, tok)
			r.Header.Set("Origin", "http://"+r.Host)
		}, 200},
		{"referer mismatch", nil, func(r *http.Request) {
			r.AddCookie(c)
			r.Header.Set(CSRFHeader, tok)
			r.Header.Set("Referer", "http://evil.test/x")
		}, 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if w := e.do("POST", "/api/v1/x", tc.body, tc.mod); w.Code != tc.want {
				t.Errorf("code %d want %d", w.Code, tc.want)
			}
		})
	}
	if w := e.do("GET", "/", nil, withCookie(c)); w.Code != 200 {
		t.Errorf("authed GET %d", w.Code)
	}
}

func TestBadCookies(t *testing.T) {
	e := newEnv(t, Config{Password: "pw", TTL: time.Hour})
	c := e.login(t, nil)
	tampered := *c
	tampered.Value = strings.Replace(c.Value, ".", ".0", 1)
	forged := *c
	forged.Value = "abc.9999999999." + e.a.sign("abc.9999999999.")[:10]
	unsigned := *c
	unsigned.Value = "garbage"
	for name, ck := range map[string]*http.Cookie{"tampered": &tampered, "forged": &forged, "garbage": &unsigned} {
		if w := e.do("GET", "/api/v1/x", nil, withCookie(ck)); w.Code != 401 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	e.now = e.now.Add(2 * time.Hour)
	if w := e.do("GET", "/api/v1/x", nil, withCookie(c)); w.Code != 401 {
		t.Errorf("expired: %d", w.Code)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, Config{Password: "pw"})
	bad := url.Values{"password": {"nope"}}
	for i := 1; i <= 5; i++ {
		if w := e.do("POST", "/login", bad, nil); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	w := e.do("POST", "/login", bad, nil)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("6th: %d %q", w.Code, w.Header().Get("Retry-After"))
	}
	if w := e.do("POST", "/login", url.Values{"password": {"pw"}}, nil); w.Code != 429 {
		t.Errorf("correct password while blocked: %d", w.Code)
	}
	other := func(r *http.Request) { r.RemoteAddr = "10.0.0.2:1" }
	if w := e.do("POST", "/login", url.Values{"password": {"pw"}}, other); w.Code != 303 {
		t.Errorf("other ip: %d", w.Code)
	}
	e.now = e.now.Add(16 * time.Minute)
	if w := e.do("POST", "/login", url.Values{"password": {"pw"}}, nil); w.Code != 303 {
		t.Errorf("after window: %d", w.Code)
	}
	for i := 0; i < 4; i++ {
		e.do("POST", "/login", bad, nil)
	}
	if w := e.do("POST", "/login", url.Values{"password": {"pw"}}, nil); w.Code != 303 {
		t.Errorf("success before limit: %d", w.Code)
	}
	for i := 0; i < 5; i++ {
		if w := e.do("POST", "/login", bad, nil); w.Code != 401 {
			t.Errorf("counter not reset: %d", w.Code)
		}
	}
}

func TestPublicRead(t *testing.T) {
	e := newEnv(t, Config{Password: "pw", PublicRead: true})
	tests := []struct {
		method, path string
		want         int
	}{
		{"GET", "/", 200},
		{"GET", "/api/v1/x", 200},
		{"POST", "/api/v1/x", 401},
		{"DELETE", "/x", 401},
	}
	for _, tc := range tests {
		if w := e.do(tc.method, tc.path, nil, nil); w.Code != tc.want {
			t.Errorf("%s %s: %d want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t, Config{Password: "pw"})
	c := e.login(t, nil)
	tok := csrfOf(e, c)
	if w := e.do("POST", "/logout", nil, withCookie(c)); w.Code != 403 {
		t.Errorf("logout without csrf: %d", w.Code)
	}
	if w := e.do("GET", "/logout", nil, withCookie(c)); w.Code != 405 {
		t.Errorf("GET logout: %d", w.Code)
	}
	w := e.do("POST", "/logout", nil, func(r *http.Request) { r.AddCookie(c); r.Header.Set(CSRFHeader, tok) })
	if w.Code != 303 {
		t.Fatalf("logout: %d", w.Code)
	}
	var cleared bool
	for _, ck := range w.Result().Cookies() {
		if ck.Name == CookieName && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("cookie not cleared")
	}
	if got := strings.Join(e.ev, ","); got != "login:ok,logout:ok" {
		t.Errorf("events %s", got)
	}
}

func TestNext(t *testing.T) {
	tests := map[string]string{
		"/projects?a=1":      "/projects?a=1",
		"":                   "/",
		"https://evil.test/": "/",
		"//evil.test":        "/",
		"/\\evil.test":       "/",
		"evil":               "/",
		"/a\r\nb":            "/",
	}
	for in, want := range tests {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q want %q", in, got, want)
		}
	}
	e := newEnv(t, Config{Password: "pw"})
	w := e.do("POST", "/login", url.Values{"password": {"pw"}, "next": {"//evil.test"}}, nil)
	if w.Header().Get("Location") != "/" {
		t.Errorf("location %q", w.Header().Get("Location"))
	}
	w = e.do("POST", "/login", url.Values{"password": {"pw"}, "next": {"/x"}}, nil)
	if w.Header().Get("Location") != "/x" {
		t.Errorf("location %q", w.Header().Get("Location"))
	}
}

func TestSecureFlag(t *testing.T) {
	tests := []struct {
		name    string
		trusted bool
		mod     func(*http.Request)
		want    bool
	}{
		{"plain", true, nil, false},
		{"forwarded untrusted", false, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, false},
		{"forwarded trusted", true, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, true},
		{"forwarded http trusted", true, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, Config{Password: "pw", TrustedProxy: tc.trusted})
			if c := e.login(t, tc.mod); c.Secure != tc.want {
				t.Errorf("secure=%v want %v", c.Secure, tc.want)
			}
		})
	}
}

func TestLoginOrigin(t *testing.T) {
	e := newEnv(t, Config{Password: "pw"})
	w := e.do("POST", "/login", url.Values{"password": {"pw"}}, func(r *http.Request) { r.Header.Set("Origin", "http://evil.test") })
	if w.Code != 403 {
		t.Errorf("code %d", w.Code)
	}
}

func TestLoadSecret(t *testing.T) {
	if b, err := LoadSecret(t.TempDir(), "from-env"); err != nil || string(b) != "from-env" {
		t.Fatalf("env: %q %v", b, err)
	}
	dir := filepath.Join(t.TempDir(), "state")
	k1, err := LoadSecret(dir, "")
	if err != nil || len(k1) != 64 {
		t.Fatalf("create: %d %v", len(k1), err)
	}
	st, err := os.Stat(filepath.Join(dir, "session.key"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v %v", st, err)
	}
	k2, err := LoadSecret(dir, "")
	if err != nil || string(k1) != string(k2) {
		t.Errorf("reload differs: %v", err)
	}
}

func TestStaleFailuresAreSwept(t *testing.T) {
	e := newEnv(t, Config{Password: "pw"})
	for i := 0; i <= sweepAbove; i++ {
		e.a.fail("10.1." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250))
	}
	e.now = e.now.Add(failureWindow + time.Second)
	e.a.fail("10.9.9.9")
	if n := len(e.a.failures); n != 1 {
		t.Errorf("tracked IPs = %d, want 1 after the window passed", n)
	}
}

func TestSetPublicRead(t *testing.T) {
	e := newEnv(t, Config{Password: "pw"})
	if w := e.do("GET", "/x", nil, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("anonymous GET = %d, want redirect", w.Code)
	}
	e.a.SetPublicRead(true)
	if w := e.do("GET", "/x", nil, nil); w.Code != http.StatusOK {
		t.Errorf("anonymous GET after SetPublicRead = %d, want 200", w.Code)
	}
}

func TestLoginPageHook(t *testing.T) {
	e := newEnv(t, Config{Password: "pw", LoginPage: func(w http.ResponseWriter, d LoginData) {
		_, _ = w.Write([]byte("custom:" + d.Next))
	}})
	w := e.do("GET", "/login?next=/edit", nil, nil)
	if w.Body.String() != "custom:/edit" {
		t.Errorf("login body = %q", w.Body.String())
	}
}
