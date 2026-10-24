// Package service installs and removes the jigd user service: a systemd
// user unit on Linux and a launchd agent on macOS. Commands run through an
// injected Runner.
package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// UnitName is the systemd unit name without the suffix.
	UnitName = "jigd"
	// Label is the launchd label.
	Label = "io.github.kinged007.jigd"
)

// DefaultPath is the daemon's PATH when Spec.Path is empty.
const DefaultPath = "/usr/local/bin:/usr/bin:/bin"

// ErrNoSession means no systemd user session is reachable.
var ErrNoSession = errors.New("no systemd user session is available")

// Runner runs one command and returns its combined output.
type Runner func(name string, args ...string) (string, error)

// Spec is what the service runs.
type Spec struct {
	// Exe is the absolute path of the jig binary.
	Exe string
	// Path is the PATH value the daemon runs with; empty is DefaultPath.
	Path string
	// Env holds further KEY=value variables the daemon runs with.
	Env []string
	// LogPath is the daemon log, used by launchd.
	LogPath string
}

// Manager installs and removes the service for one user.
type Manager struct {
	GOOS string
	Home string
	UID  int
	User string
	Run  Runner
}

// Result describes an install.
type Result struct {
	// LingerHint is the command that keeps the service running after
	// logout, set on Linux when linger is off.
	LingerHint string
}

// FilePath returns the unit or plist path.
func (m *Manager) FilePath() (string, error) {
	switch m.GOOS {
	case "linux":
		return filepath.Join(m.Home, ".config", "systemd", "user", UnitName+".service"), nil
	case "darwin":
		return filepath.Join(m.Home, "Library", "LaunchAgents", Label+".plist"), nil
	}
	return "", fmt.Errorf("service: unsupported OS %s", m.GOOS)
}

// Installed reports whether the unit or plist file exists.
func (m *Manager) Installed() bool {
	path, err := m.FilePath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// Preflight reports ErrNoSession when the service cannot be installed here.
func (m *Manager) Preflight() error {
	switch m.GOOS {
	case "linux":
		if _, err := m.Run("systemctl", "--user", "show-environment"); err != nil {
			return ErrNoSession
		}
	case "darwin":
	default:
		return fmt.Errorf("service: unsupported OS %s", m.GOOS)
	}
	return nil
}

// Install writes the service file and starts the service. An existing
// service is replaced and restarted.
func (m *Manager) Install(s Spec) (Result, error) {
	var res Result
	if err := s.check(); err != nil {
		return res, err
	}
	path, err := m.FilePath()
	if err != nil {
		return res, err
	}
	replacing := m.Installed()
	switch m.GOOS {
	case "linux":
		if err := m.Preflight(); err != nil {
			return res, err
		}
		if err := writeFile(path, RenderUnit(s)); err != nil {
			return res, err
		}
		if err := m.must("systemctl", "--user", "daemon-reload"); err != nil {
			return res, err
		}
		if err := m.must("systemctl", "--user", "enable", "--now", UnitName); err != nil {
			return res, err
		}
		if replacing {
			if err := m.must("systemctl", "--user", "restart", UnitName); err != nil {
				return res, err
			}
		}
		out, err := m.Run("loginctl", "show-user", m.User, "--property=Linger")
		if err == nil && strings.TrimSpace(out) == "Linger=no" {
			res.LingerHint = "loginctl enable-linger " + m.User
		}
	case "darwin":
		domain := fmt.Sprintf("gui/%d", m.UID)
		if replacing {
			m.Run("launchctl", "bootout", domain+"/"+Label)
		}
		if err := writeFile(path, RenderPlist(s)); err != nil {
			return res, err
		}
		if err := m.must("launchctl", "bootstrap", domain, path); err != nil {
			return res, err
		}
	}
	return res, nil
}

// Remove stops the service and deletes its file. It succeeds when the
// service is not installed.
func (m *Manager) Remove() error {
	path, err := m.FilePath()
	if err != nil {
		return err
	}
	if !m.Installed() {
		return nil
	}
	switch m.GOOS {
	case "linux":
		m.Run("systemctl", "--user", "disable", "--now", UnitName)
		if err := os.Remove(path); err != nil {
			return err
		}
		m.Run("systemctl", "--user", "daemon-reload")
	case "darwin":
		m.Run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", m.UID, Label))
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) must(name string, args ...string) error {
	if _, err := m.Run(name, args...); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func (s Spec) check() error {
	if !filepath.IsAbs(s.Exe) {
		return fmt.Errorf("service: executable path %q is not absolute", s.Exe)
	}
	for _, v := range append([]string{s.Exe, s.Path, s.LogPath}, s.Env...) {
		if strings.ContainsAny(v, "\n\r\x00") {
			return errors.New("service: path contains a control character")
		}
	}
	for _, kv := range s.Env {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			return fmt.Errorf("service: %q is not KEY=value", kv)
		}
	}
	return nil
}

// env returns the daemon's environment as KEY=value, PATH first.
func (s Spec) env() []string {
	path := s.Path
	if path == "" {
		path = DefaultPath
	}
	return append([]string{"PATH=" + path}, s.Env...)
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// RenderUnit returns the systemd user unit.
func RenderUnit(s Spec) string {
	return fmt.Sprintf(`[Unit]
Description=Jigz daemon (jigd)

[Service]
ExecStart=%s daemon
Restart=on-failure
%sKillMode=mixed
TimeoutStopSec=120

[Install]
WantedBy=default.target
`, unitQuote(s.Exe), unitEnv(s.env()))
}

// unitEnv returns one Environment= line per assignment. systemd expands
// specifiers and C escapes in Environment= but not $, so only % and the
// quoting characters are escaped, and an assignment with any of them or
// with whitespace is quoted whole.
func unitEnv(env []string) string {
	var b strings.Builder
	for _, kv := range env {
		kv = strings.ReplaceAll(kv, "%", "%%")
		if strings.ContainsAny(kv, " \t\"'\\") {
			kv = strings.ReplaceAll(kv, `\`, `\\`)
			kv = strings.ReplaceAll(kv, `"`, `\"`)
			kv = `"` + kv + `"`
		}
		b.WriteString("Environment=" + kv + "\n")
	}
	return b.String()
}

// unitQuote quotes an ExecStart word for a systemd unit when it needs it.
func unitQuote(v string) string {
	v = strings.ReplaceAll(v, "%", "%%")
	if !strings.ContainsAny(v, " \t\"'\\$") {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "$", "$$")
	return `"` + v + `"`
}

// RenderPlist returns the launchd agent plist.
func RenderPlist(s Spec) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
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
%s	</dict>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, Label, xmlEscape(s.Exe), plistEnv(s.env()), xmlEscape(s.LogPath), xmlEscape(s.LogPath))
}

func plistEnv(env []string) string {
	var b strings.Builder
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlEscape(k), xmlEscape(v))
	}
	return b.String()
}

func xmlEscape(v string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(v))
	return b.String()
}
