package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var spec = Spec{Exe: "/usr/local/bin/jig", Path: "/usr/bin:/bin", LogPath: "/home/u/.local/state/jigz/jigd.log"}

func TestRenderUnit(t *testing.T) {
	want := `[Unit]
Description=Jigz daemon (jigd)

[Service]
ExecStart=/usr/local/bin/jig daemon
Restart=on-failure
Environment=PATH=/usr/bin:/bin
KillMode=mixed
TimeoutStopSec=120

[Install]
WantedBy=default.target
`
	if got := RenderUnit(spec); got != want {
		t.Errorf("unit:\n%s", got)
	}
}

func TestRenderUnitQuotes(t *testing.T) {
	got := RenderUnit(Spec{Exe: "/opt/my jig/jig", Path: "/a b:/bin"})
	for _, w := range []string{`ExecStart="/opt/my jig/jig" daemon`, `Environment="PATH=/a b:/bin"`} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
}

func TestRenderUnitEnvironment(t *testing.T) {
	got := RenderUnit(Spec{Exe: "/usr/local/bin/jig", Env: []string{
		"XDG_CONFIG_HOME=/home/u/cfg", `HOME=/home/a $b%c"d\e`,
	}})
	for _, w := range []string{
		"Environment=PATH=" + DefaultPath + "\n",
		"Environment=XDG_CONFIG_HOME=/home/u/cfg\n",
		`Environment="HOME=/home/a $b%%c\"d\\e"` + "\n",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	if strings.Contains(got, "$$") {
		t.Errorf("$ doubled in Environment=:\n%s", got)
	}
}

func TestRenderPlistEnvironment(t *testing.T) {
	got := RenderPlist(Spec{Exe: "/usr/local/bin/jig", Env: []string{"XDG_STATE_HOME=/s&t"}})
	want := "\t\t<key>PATH</key>\n\t\t<string>" + DefaultPath + "</string>\n\t\t<key>XDG_STATE_HOME</key>\n\t\t<string>/s&amp;t</string>\n"
	if !strings.Contains(got, want) {
		t.Errorf("plist environment:\n%s", got)
	}
}

func TestInstallRefusesABadEnvEntry(t *testing.T) {
	m := manager("linux", &recorder{})
	m.Home = t.TempDir()
	if _, err := m.Install(Spec{Exe: "/jig", Env: []string{"novalue"}}); err == nil {
		t.Error("entry without = accepted")
	}
}

func TestRenderPlist(t *testing.T) {
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>io.github.kinged007.jigd</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/jig</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ExitTimeOut</key>
	<integer>120</integer>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/usr/bin:/bin</string>
	</dict>
	<key>StandardOutPath</key>
	<string>/home/u/.local/state/jigz/jigd.log</string>
	<key>StandardErrorPath</key>
	<string>/home/u/.local/state/jigz/jigd.log</string>
</dict>
</plist>
`
	if got := RenderPlist(spec); got != want {
		t.Errorf("plist:\n%s", got)
	}
}

func TestPlistEscapes(t *testing.T) {
	if got := RenderPlist(Spec{Exe: "/a&b/jig", Path: "<p>"}); !strings.Contains(got, "/a&amp;b/jig") || !strings.Contains(got, "&lt;p&gt;") {
		t.Errorf("not escaped:\n%s", got)
	}
}

type recorder struct {
	calls []string
	fail  map[string]error
	out   map[string]string
}

func (r *recorder) run(name string, args ...string) (string, error) {
	c := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, c)
	return r.out[c], r.fail[c]
}

func manager(goos string, r *recorder) *Manager {
	return &Manager{GOOS: goos, Home: "", UID: 501, User: "u", Run: r.run}
}

func TestInstallAndRemoveLinux(t *testing.T) {
	r := &recorder{out: map[string]string{"loginctl show-user u --property=Linger": "Linger=no\n"}}
	m := manager("linux", r)
	m.Home = t.TempDir()
	res, err := m.Install(spec)
	if err != nil {
		t.Fatal(err)
	}
	if res.LingerHint != "loginctl enable-linger u" {
		t.Errorf("hint %q", res.LingerHint)
	}
	want := []string{
		"systemctl --user show-environment",
		"systemctl --user daemon-reload",
		"systemctl --user enable --now jigd",
		"loginctl show-user u --property=Linger",
	}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Errorf("install calls %v", r.calls)
	}
	data, err := os.ReadFile(filepath.Join(m.Home, ".config/systemd/user/jigd.service"))
	if err != nil || string(data) != RenderUnit(spec) {
		t.Fatalf("unit file: %v", err)
	}

	r.calls = nil
	if _, err := m.Install(spec); err != nil {
		t.Fatal(err)
	}
	if r.calls[3] != "systemctl --user restart jigd" {
		t.Errorf("replace calls %v", r.calls)
	}

	r.calls = nil
	if err := m.Remove(); err != nil {
		t.Fatal(err)
	}
	want = []string{"systemctl --user disable --now jigd", "systemctl --user daemon-reload"}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Errorf("remove calls %v", r.calls)
	}
	if m.Installed() {
		t.Error("unit file left")
	}
	r.calls = nil
	if err := m.Remove(); err != nil || len(r.calls) != 0 {
		t.Errorf("second remove: %v %v", err, r.calls)
	}
}

func TestInstallNoSession(t *testing.T) {
	r := &recorder{fail: map[string]error{"systemctl --user show-environment": errors.New("no bus")}}
	m := manager("linux", r)
	m.Home = t.TempDir()
	if _, err := m.Install(spec); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err %v", err)
	}
	if m.Installed() {
		t.Error("unit written without a session")
	}
}

func TestInstallAndRemoveDarwin(t *testing.T) {
	r := &recorder{}
	m := manager("darwin", r)
	m.Home = t.TempDir()
	if _, err := m.Install(spec); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(m.Home, "Library/LaunchAgents/io.github.kinged007.jigd.plist")
	if len(r.calls) != 1 || r.calls[0] != "launchctl bootstrap gui/501 "+plist {
		t.Errorf("calls %v", r.calls)
	}
	r.calls = nil
	if err := m.Remove(); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || r.calls[0] != "launchctl bootout gui/501/io.github.kinged007.jigd" {
		t.Errorf("calls %v", r.calls)
	}
	if m.Installed() {
		t.Error("plist left")
	}
}

func TestInstallRefusesRelativeExe(t *testing.T) {
	m := manager("linux", &recorder{})
	m.Home = t.TempDir()
	if _, err := m.Install(Spec{Exe: "jig"}); err == nil {
		t.Error("relative exe accepted")
	}
}
