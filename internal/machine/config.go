// Package machine holds the per-machine settings shared by every loop on the
// machine: working hours, the agent account pool and usage-limit handling.
// The settings file is optional; a missing file means no hours limit and no
// pool.
package machine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"gopkg.in/yaml.v3"

	"github.com/kinged007/jigz/internal/dirs"
)

// Runtimes are the agent kinds a pool entry can use.
var Runtimes = []string{"claude-code", "pi"}

// DefaultCommand is the executable a runtime runs when a pool entry or
// agent names no command.
func DefaultCommand(runtime string) string {
	return map[string]string{"claude-code": "claude", "pi": "pi"}[runtime]
}

const (
	EndFinish = "finish"
	EndStop   = "stop"

	defaultReset = time.Hour

	DefaultIdleTimeout      = 15 * time.Minute
	DefaultFallbackInterval = 5 * time.Minute
	minConnectedDuration    = time.Minute
)

// Connected tunes the connected-mode daemon. Empty fields mean the defaults.
type Connected struct {
	IdleTimeout      string `yaml:"idle_timeout"`
	FallbackInterval string `yaml:"fallback_interval"`

	idle     time.Duration
	fallback time.Duration
}

// Idle is how long a loop may be idle before the daemon stops it.
func (c Connected) Idle() time.Duration { return c.idle }

// Fallback is the poll interval used while the hub connection is down.
func (c Connected) Fallback() time.Duration { return c.fallback }

func (c *Connected) validate() error {
	var err error
	if c.idle, err = connectedDuration("connected.idle_timeout", c.IdleTimeout, DefaultIdleTimeout); err != nil {
		return err
	}
	c.fallback, err = connectedDuration("connected.fallback_interval", c.FallbackInterval, DefaultFallbackInterval)
	return err
}

func connectedDuration(key, v string, def time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < minConnectedDuration {
		return 0, fmt.Errorf("%s: %q is not a duration of at least 1m (for example 15m)", key, v)
	}
	return d, nil
}

// Config is the parsed and validated machine file.
type Config struct {
	Hours  Hours   `yaml:"hours"`
	Pool   []Entry `yaml:"pool"`
	Limits Limits  `yaml:"limits"`
	Engine Engine  `yaml:"engine"`
	// Connected tunes the connected-mode daemon.
	Connected Connected `yaml:"connected"`
	// Dashboard holds the dashboard settings.
	Dashboard Dashboard `yaml:"dashboard"`
	// Warnings are non-fatal findings from Load, for example file permissions.
	Warnings []string `yaml:"-"`
	// Unsupported holds the runtimes the engine cannot run. Their pool
	// entries are never picked.
	Unsupported map[string]bool `yaml:"-"`
}

// Engine selects the sortie engine release: a GitHub repository and a
// release tag. Empty fields mean the built-in defaults.
type Engine struct {
	Repo    string `yaml:"repo"`
	Version string `yaml:"version"`
}

var (
	engineRepoRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)
	engineVersionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
)

// ValidateEngineRepo checks an engine repository of the form owner/name.
func ValidateEngineRepo(repo string) error {
	if !engineRepoRe.MatchString(repo) || strings.HasSuffix(repo, "/.") || strings.HasSuffix(repo, "/..") {
		return fmt.Errorf("engine repository %q is not of the form owner/name", repo)
	}
	return nil
}

// ValidateEngineVersion checks an engine release tag of the form vX.Y.Z.
func ValidateEngineVersion(version string) error {
	if !engineVersionRe.MatchString(version) {
		return fmt.Errorf("engine version %q is not of the form vX.Y.Z", version)
	}
	return nil
}

func (e Engine) validate() error {
	if e.Repo != "" {
		if err := ValidateEngineRepo(e.Repo); err != nil {
			return fmt.Errorf("engine.repo: %w", err)
		}
	}
	if e.Version != "" {
		if err := ValidateEngineVersion(e.Version); err != nil {
			return fmt.Errorf("engine.version: %w", err)
		}
	}
	return nil
}

// LoadEngine reads only the engine block of the machine file, so a problem in
// the hours or pool settings does not stop the engine from being resolved.
// A missing file gives the zero Engine.
func LoadEngine(path string) (Engine, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Engine{}, nil
	}
	if err != nil {
		return Engine{}, err
	}
	var doc struct {
		Engine Engine `yaml:"engine"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return Engine{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := doc.Engine.validate(); err != nil {
		return Engine{}, fmt.Errorf("%s: %w", path, err)
	}
	return doc.Engine, nil
}

// Hours is a set of weekly windows. No windows means always open.
type Hours struct {
	Timezone string `yaml:"timezone"`
	// End applies to the machine hours only: "finish" (default) or "stop".
	End     string   `yaml:"end"`
	Windows []Window `yaml:"windows"`
	loc     *time.Location
}

// Window is a time range on the listed days. An End earlier than Start wraps
// past midnight and belongs to the day it starts.
type Window struct {
	Days  []string `yaml:"days"`
	Start string   `yaml:"start"`
	End   string   `yaml:"end"`

	days     [7]bool // indexed by time.Weekday
	startMin int
	endMin   int
}

// Entry is one agent account in the pool.
type Entry struct {
	Name      string            `yaml:"name"`
	Runtime   string            `yaml:"runtime"`
	Command   string            `yaml:"command"`
	Env       map[string]string `yaml:"env"`
	Hours     Hours             `yaml:"hours"`
	CostClass string            `yaml:"cost_class"`
}

// Limits tunes usage-limit detection.
type Limits struct {
	DefaultReset string   `yaml:"default_reset"`
	Patterns     []string `yaml:"patterns"`

	reset    time.Duration
	patterns []*regexp.Regexp
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// DefaultPath returns <user config dir>/jigz/machine.yaml.
func DefaultPath() (string, error) {
	dir, err := dirs.Config()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "machine.yaml"), nil
}

// Load reads and validates the machine file. A missing file gives an empty
// Config with defaults.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	c := &Config{}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s is readable by group or others and may hold API keys; run chmod 600", path))
		}
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) validate() error {
	if err := c.Engine.validate(); err != nil {
		return err
	}
	if err := c.Connected.validate(); err != nil {
		return err
	}
	if err := c.Dashboard.validate(); err != nil {
		return err
	}
	if err := c.Hours.Validate("hours"); err != nil {
		return err
	}
	switch c.Hours.End {
	case "":
		c.Hours.End = EndFinish
	case EndFinish, EndStop:
	default:
		return fmt.Errorf("hours.end: %q is not finish or stop", c.Hours.End)
	}
	seen := map[string]bool{}
	for i := range c.Pool {
		e := &c.Pool[i]
		key := fmt.Sprintf("pool[%d].", i)
		if e.Name == "" {
			return fmt.Errorf("%sname is required", key)
		}
		if seen[e.Name] {
			return fmt.Errorf("%sname: %q is used twice", key, e.Name)
		}
		seen[e.Name] = true
		if !slices.Contains(Runtimes, e.Runtime) {
			return fmt.Errorf("%sruntime: unknown runtime %q (use %s)", key, e.Runtime, strings.Join(Runtimes, " or "))
		}
		if e.Command == "" {
			e.Command = DefaultCommand(e.Runtime)
		}
		if e.Hours.End != "" {
			return fmt.Errorf("%shours.end: only the machine hours have an end mode", key)
		}
		if err := e.Hours.Validate(key + "hours"); err != nil {
			return err
		}
	}
	return c.Limits.validate()
}

// Validate resolves the timezone and parses the windows, so Open and
// Next can be used. Error messages are prefixed with key.
func (h *Hours) Validate(key string) error {
	h.loc = time.Local
	if h.Timezone != "" {
		loc, err := time.LoadLocation(h.Timezone)
		if err != nil {
			return fmt.Errorf("%s.timezone: %q is not an IANA time zone", key, h.Timezone)
		}
		h.loc = loc
	}
	for i := range h.Windows {
		if err := h.Windows[i].validate(fmt.Sprintf("%s.windows[%d]", key, i)); err != nil {
			return err
		}
	}
	return nil
}

func (w *Window) validate(key string) error {
	if len(w.Days) == 0 {
		return fmt.Errorf("%s.days: at least one day is required", key)
	}
	for _, d := range w.Days {
		wd, ok := dayNames[strings.ToLower(d)]
		if !ok {
			return fmt.Errorf("%s.days: %q is not one of mon..sun", key, d)
		}
		w.days[wd] = true
	}
	var err error
	if w.startMin, err = parseClock(w.Start); err != nil {
		return fmt.Errorf("%s.start: %q is not HH:MM", key, w.Start)
	}
	if w.endMin, err = parseClock(w.End); err != nil {
		return fmt.Errorf("%s.end: %q is not HH:MM", key, w.End)
	}
	if w.startMin == w.endMin {
		return fmt.Errorf("%s: start and end must differ", key)
	}
	return nil
}

func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (l *Limits) validate() error {
	l.reset = defaultReset
	if l.DefaultReset != "" {
		d, err := time.ParseDuration(l.DefaultReset)
		if err != nil || d <= 0 {
			return fmt.Errorf("limits.default_reset: %q is not a positive duration (for example 1h)", l.DefaultReset)
		}
		l.reset = d
	}
	for i, p := range l.Patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return fmt.Errorf("limits.patterns[%d]: %v", i, err)
		}
		l.patterns = append(l.patterns, re)
	}
	return nil
}

// ExpandedEnv returns the entry env as sorted KEY=VALUE pairs with a leading
// "~/" and $VAR or ${VAR} expanded from the process environment.
func (e Entry) ExpandedEnv() []string {
	out := make([]string, 0, len(e.Env))
	for k, v := range e.Env {
		out = append(out, k+"="+ExpandEnv(v))
	}
	sort.Strings(out)
	return out
}

// ExpandEnv expands a leading "~/" (or a lone "~") and $VAR or ${VAR}.
func ExpandEnv(v string) string {
	if v == "~" || strings.HasPrefix(v, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			v = home + v[1:]
		}
	}
	return os.ExpandEnv(v)
}
