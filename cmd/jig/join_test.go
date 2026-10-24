package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinged007/jigz/internal/fakehub"
	"github.com/kinged007/jigz/internal/hubstate"
	"github.com/kinged007/jigz/internal/service"
)

const (
	canaryEnroll  = "enroll-canary-7f3a"
	canaryTracker = "tracker-canary-91bc"
)

type joinRig struct {
	d     joinDeps
	out   *bytes.Buffer
	calls *[]string
	home  string
}

func newJoinRig(t *testing.T, stdin string) joinRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("JIG_ENGINE_VERSION", "")
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer "+canaryTracker {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"login":"octo"}`))
	}))
	t.Cleanup(gh.Close)
	var calls []string
	out := &bytes.Buffer{}
	d := joinDeps{
		stdin: strings.NewReader(stdin),
		out:   out,
		getenv: func(k string) string {
			return map[string]string{"JIG_TOKEN": canaryTracker, "PATH": "/usr/bin:/bin"}[k]
		},
		githubURL: gh.URL,
		exe:       func() (string, error) { return "/usr/local/bin/jig", nil },
		hostname:  func() (string, error) { return "box", nil },
		logPath:   func() (string, error) { return filepath.Join(home, "state", "jigd.log"), nil },
		service: &service.Manager{GOOS: "linux", Home: home, UID: 1000, User: "u", Run: func(name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return "", nil
		}},
	}
	return joinRig{d: d, out: out, calls: &calls, home: home}
}

func TestParseJoinArgs(t *testing.T) {
	o, err := parseJoinArgs([]string{"https://hub.example", "-", "--name", "laptop", "--no-service", "--yes"})
	if err != nil {
		t.Fatal(err)
	}
	want := joinOpts{hubURL: "https://hub.example", token: "-", name: "laptop", noService: true, yes: true}
	if o != want {
		t.Errorf("got %+v", o)
	}
	o, err = parseJoinArgs([]string{"--name=x", "https://h", "tok"})
	if err != nil || o.name != "x" || o.token != "tok" {
		t.Errorf("got %+v, %v", o, err)
	}
	for _, bad := range [][]string{{}, {"https://h"}, {"a", "b", "c"}, {"https://h", "t", "--bogus"}, {"https://h", "t", "--name"}} {
		if _, err := parseJoinArgs(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestParseArgsJoinLeave(t *testing.T) {
	for _, name := range []string{"join", "leave"} {
		c, err := parseArgs([]string{name, "https://h", "--yes"})
		if err != nil || c.remote != name || len(c.remoteArgs) != 2 {
			t.Errorf("%s: %+v, %v", name, c, err)
		}
	}
}

func TestCheckHubURL(t *testing.T) {
	for _, ok := range []string{"https://hub.example", "https://hub.example/", "http://127.0.0.1:8080", "http://localhost:9", "http://[::1]:80"} {
		if _, err := checkHubURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://hub.example", "http://10.0.0.5", "ftp://hub.example", "hub.example", "https://", "https://user:pw@hub.example", "https://hub.example/?a=b"} {
		_, err := checkHubURL(bad)
		if err == nil {
			t.Errorf("%s accepted", bad)
		} else if strings.Contains(err.Error(), "pw@") {
			t.Errorf("%s: error repeats user info: %v", bad, err)
		}
	}
}

func TestJoinNoServiceAgainstFakeHub(t *testing.T) {
	r := newJoinRig(t, canaryEnroll+"\n")
	hub := fakehub.New(canaryEnroll)
	srv := httptest.NewServer(hub)
	defer srv.Close()

	if err := joinRun(context.Background(), r.d, []string{srv.URL, "-", "--name", "laptop", "--no-service"}); err != nil {
		t.Fatal(err)
	}
	enr := hub.Enrollments()
	if len(enr) != 1 {
		t.Fatalf("enrollments %d", len(enr))
	}
	e := enr[0]
	if e.Name != "laptop" || e.TrackerLogin != "octo" || e.Host.OS == "" || e.Host.Arch == "" || e.Host.CPUs < 1 || e.JigVersion == "" || e.EngineVersion == "" {
		t.Errorf("enrollment %+v", e)
	}
	st, err := hubstate.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.HubURL != srv.URL || st.Name != "laptop" || st.DaemonID == "" || st.Credential == "" || st.RunnerLabel == "" || st.TrackerToken != canaryTracker {
		t.Errorf("state %+v", st)
	}
	path, _ := hubstate.Path()
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700)
	if len(*r.calls) != 0 {
		t.Errorf("service commands run with --no-service: %v", *r.calls)
	}
	out := r.out.String()
	for _, secret := range []string{canaryEnroll, canaryTracker, st.Credential} {
		if strings.Contains(out, secret) {
			t.Errorf("output contains a secret:\n%s", out)
		}
	}
	if u := hub.Unexpected(); len(u) != 0 {
		t.Errorf("unexpected hub requests: %v", u)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode %04o, want %04o", path, got, want)
	}
}

func TestJoinRefusesBadURLBeforeAnyRequest(t *testing.T) {
	r := newJoinRig(t, "")
	err := joinRun(context.Background(), r.d, []string{"http://hub.example", canaryEnroll, "--no-service"})
	if err == nil || strings.Contains(err.Error(), canaryEnroll) {
		t.Fatalf("err %v", err)
	}
	if hubstate.Exists() {
		t.Error("hub.json written")
	}
}

func TestJoinTrackerLoginMismatch(t *testing.T) {
	r := newJoinRig(t, "")
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		json.NewDecoder(req.Body).Decode(&body)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"code": "invalid", "message": "tracker_login does not match; token " + body["token"].(string) + " " + canaryTracker})
	}))
	defer hub.Close()
	err := joinRun(context.Background(), r.d, []string{hub.URL, canaryEnroll, "--no-service"})
	if err == nil {
		t.Fatal("join succeeded")
	}
	if strings.Contains(err.Error(), canaryEnroll) || strings.Contains(err.Error(), canaryTracker) {
		t.Errorf("error leaks a secret: %v", err)
	}
	if hubstate.Exists() {
		t.Error("hub.json written after refusal")
	}
}

func TestJoinTrackerTokenRejectedByGitHub(t *testing.T) {
	r := newJoinRig(t, "")
	r.d.getenv = func(k string) string { return map[string]string{"GH_TOKEN": "wrong-" + canaryTracker}[k] }
	hub := fakehub.New(canaryEnroll)
	srv := httptest.NewServer(hub)
	defer srv.Close()
	err := joinRun(context.Background(), r.d, []string{srv.URL, canaryEnroll, "--no-service"})
	if err == nil || strings.Contains(err.Error(), canaryTracker) {
		t.Fatalf("err %v", err)
	}
	if len(hub.Enrollments()) != 0 {
		t.Error("enrolled with an unverified tracker token")
	}
}

func TestTrackerTokenOrder(t *testing.T) {
	env := map[string]string{"GH_TOKEN": "c", "GITHUB_TOKEN": "b"}
	d := joinDeps{getenv: func(k string) string { return env[k] }, ghToken: func() (string, error) { return "gh\n", nil }}
	if v, _ := trackerToken(d); v != "b" {
		t.Errorf("got %q", v)
	}
	env["JIG_TOKEN"] = "a"
	if v, _ := trackerToken(d); v != "a" {
		t.Errorf("got %q", v)
	}
	d.getenv = func(string) string { return "" }
	if v, _ := trackerToken(d); v != "gh" {
		t.Errorf("got %q", v)
	}
	d.ghToken = func() (string, error) { return "", errors.New("no gh") }
	if _, err := trackerToken(d); err == nil {
		t.Error("no token accepted")
	}
}

func TestJoinInstallsServiceAndSecondJoinPrompts(t *testing.T) {
	r := newJoinRig(t, "")
	r.d.service.Run = func(name string, args ...string) (string, error) {
		c := name + " " + strings.Join(args, " ")
		*r.calls = append(*r.calls, c)
		if strings.HasPrefix(c, "loginctl") {
			return "Linger=no\n", nil
		}
		return "", nil
	}
	hub := fakehub.New(canaryEnroll)
	srv := httptest.NewServer(hub)
	defer srv.Close()

	if err := joinRun(context.Background(), r.d, []string{srv.URL, canaryEnroll}); err != nil {
		t.Fatal(err)
	}
	want := "systemctl --user show-environment|systemctl --user show-environment|systemctl --user daemon-reload|systemctl --user enable --now jigd|loginctl show-user u --property=Linger"
	if got := strings.Join(*r.calls, "|"); got != want {
		t.Errorf("calls %s", got)
	}
	if !strings.Contains(r.out.String(), "loginctl enable-linger u") {
		t.Errorf("no linger hint:\n%s", r.out.String())
	}
	unit, err := os.ReadFile(filepath.Join(r.home, ".config/systemd/user/jigd.service"))
	if err != nil || !strings.Contains(string(unit), "ExecStart=/usr/local/bin/jig daemon") {
		t.Fatalf("unit %q, %v", unit, err)
	}

	hub2 := fakehub.New(canaryEnroll)
	srv2 := httptest.NewServer(hub2)
	defer srv2.Close()
	r.d.stdin = strings.NewReader("n\n")
	err = joinRun(context.Background(), r.d, []string{srv2.URL, canaryEnroll})
	if err == nil || len(hub2.Enrollments()) != 0 {
		t.Fatalf("declined join: err %v, enrollments %d", err, len(hub2.Enrollments()))
	}
	st, _ := hubstate.Load()
	if st.HubURL != srv.URL {
		t.Error("hub.json replaced after a declined prompt")
	}

	r.d.stdin = strings.NewReader("y\n")
	if err := joinRun(context.Background(), r.d, []string{srv2.URL, canaryEnroll}); err != nil {
		t.Fatal(err)
	}
	if st, _ := hubstate.Load(); st.HubURL != srv2.URL {
		t.Error("hub.json not replaced after yes")
	}

	hub3 := fakehub.New(canaryEnroll)
	srv3 := httptest.NewServer(hub3)
	defer srv3.Close()
	r.d.stdin = strings.NewReader("")
	if err := joinRun(context.Background(), r.d, []string{srv3.URL, canaryEnroll, "--yes"}); err != nil {
		t.Fatal(err)
	}
	if st, _ := hubstate.Load(); st.HubURL != srv3.URL {
		t.Error("--yes did not replace hub.json")
	}
	if n := strings.Count(strings.Join(*r.calls, "|"), "systemctl --user restart jigd"); n != 2 {
		t.Errorf("restart count %d", n)
	}
}

func TestJoinNoSystemdSession(t *testing.T) {
	r := newJoinRig(t, "")
	r.d.service.Run = func(string, ...string) (string, error) { return "", errors.New("no bus") }
	hub := fakehub.New(canaryEnroll)
	srv := httptest.NewServer(hub)
	defer srv.Close()
	err := joinRun(context.Background(), r.d, []string{srv.URL, canaryEnroll})
	if err == nil || !strings.Contains(err.Error(), "--no-service") {
		t.Fatalf("err %v", err)
	}
	if len(hub.Enrollments()) != 0 || hubstate.Exists() {
		t.Error("enrolled without a session")
	}
}

func TestLeave(t *testing.T) {
	r := newJoinRig(t, "")
	hub := fakehub.New(canaryEnroll)
	srv := httptest.NewServer(hub)
	defer srv.Close()
	if err := joinRun(context.Background(), r.d, []string{srv.URL, canaryEnroll}); err != nil {
		t.Fatal(err)
	}
	loops := filepath.Join(r.home, "data", "jigz", "hub", "loops")
	if err := os.MkdirAll(loops, 0o755); err != nil {
		t.Fatal(err)
	}
	*r.calls = nil
	if err := leaveRun(r.d); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*r.calls, "|"); got != "systemctl --user disable --now jigd|systemctl --user daemon-reload" {
		t.Errorf("calls %s", got)
	}
	if hubstate.Exists() {
		t.Error("hub.json left")
	}
	if _, err := os.Stat(loops); err != nil {
		t.Errorf("loop roots removed: %v", err)
	}
	r.out.Reset()
	*r.calls = nil
	if err := leaveRun(r.d); err != nil || len(*r.calls) != 0 || !strings.Contains(r.out.String(), "not enrolled") {
		t.Errorf("second leave: %v %v %s", err, *r.calls, r.out.String())
	}
}

func TestStatusJSONWithoutRootUsesHubStatus(t *testing.T) {
	newJoinRig(t, "")
	if err := hubstate.Save(hubstate.State{HubURL: "https://h"}); err != nil {
		t.Fatal(err)
	}
	if _, err := hubStatusOutput(); err == nil {
		t.Error("missing status document accepted")
	}
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "jigz", "hub")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte("{\"paused\":false}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := hubStatusOutput(); err != nil || got != `{"paused":false}` {
		t.Errorf("got %q, %v", got, err)
	}
}
