// Package config resolves the loop's runtime settings from .sortie/config.yaml
// with environment overrides, and derives the repo identity from git.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

// Config holds the resolved runtime settings for one loop run.
type Config struct {
	Repo      string `yaml:"repo"`
	Token     string `yaml:"token"`
	Milestone string `yaml:"milestone"`
	// Assignee restricts every loop to items assigned to this user.
	// Empty = no restriction. Setup writes "@me" (the token owner).
	Assignee *string `yaml:"assignee"`

	Dir       string // repo root (sortie dir is Dir/.sortie)
	SortieBin string // resolved sortie binary path
	EnvFile   string // path to generated env file passed via --env-file
	Tracker   string // tracker project, always == Repo
	// Filters overrides the default per-loop query filters by loop name
	// (the WORKFLOW.*.md stem). Unknown loops get defaultFilter; known
	// loops fall back to their shipped default when unset.
	Filters map[string]string `yaml:"filters"`
	// Labels are the GitHub labels setup ensures exist (create-or-edit;
	// labels absent here are left alone). Empty = DefaultLabels.
	Labels     []Label `yaml:"labels"`
	Workspaces string  // .sortie/workspaces under Dir
	Cleanup    Cleanup
	Sleep      Sleep
	// Budgets are the per-workflow spend caps by loop name. An entry with
	// MaxTokens 0 is disabled.
	Budgets map[string]Budget
}

// Budget caps the tokens one workflow may use per period.
type Budget struct {
	Period       string
	Location     *time.Location
	MaxTokens    int64
	WarnPercent  int
	PollInterval time.Duration
	MaxUSD       float64
}

type budgetFile struct {
	Period       string  `yaml:"period"`
	Timezone     string  `yaml:"timezone"`
	MaxTokens    int64   `yaml:"max_tokens"`
	WarnPercent  int     `yaml:"warn_percent"`
	OnExceed     string  `yaml:"on_exceed"`
	PollInterval string  `yaml:"poll_interval"`
	MaxUSD       float64 `yaml:"max_usd"`
}

const (
	defaultWarnPercent  = 80
	defaultBudgetPoll   = time.Minute
	defaultBudgetTZName = "UTC"
)

func (f budgetFile) resolve(name string) (Budget, error) {
	key := "workflows." + name + ".budget."
	b := Budget{
		Period: f.Period, MaxTokens: f.MaxTokens, WarnPercent: f.WarnPercent,
		PollInterval: defaultBudgetPoll, MaxUSD: f.MaxUSD,
	}
	if b.WarnPercent == 0 {
		b.WarnPercent = defaultWarnPercent
	}
	if f.MaxTokens < 0 {
		return b, fmt.Errorf("%smax_tokens must not be negative", key)
	}
	if f.MaxUSD < 0 {
		return b, fmt.Errorf("%smax_usd must not be negative", key)
	}
	if b.WarnPercent < 1 || b.WarnPercent > 100 {
		return b, fmt.Errorf("%swarn_percent must be between 1 and 100", key)
	}
	if f.OnExceed != "" && f.OnExceed != "pause" {
		return b, fmt.Errorf("%son_exceed: %q is not supported (only pause)", key, f.OnExceed)
	}
	switch f.Period {
	case "weekly", "monthly":
	case "":
		if f.MaxTokens > 0 {
			return b, fmt.Errorf("%speriod is required when max_tokens is set (weekly or monthly)", key)
		}
	default:
		return b, fmt.Errorf("%speriod: %q is not weekly or monthly", key, f.Period)
	}
	tz := f.Timezone
	if tz == "" {
		tz = defaultBudgetTZName
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return b, fmt.Errorf("%stimezone: %q is not an IANA time zone", key, tz)
	}
	b.Location = loc
	if f.PollInterval != "" {
		v, err := time.ParseDuration(f.PollInterval)
		if err != nil || v <= 0 {
			return b, fmt.Errorf("%spoll_interval: %q is not a positive duration (for example 1m)", key, f.PollInterval)
		}
		b.PollInterval = v
	}
	return b, nil
}

// CheckBudgets fails when workflows: names a loop that has no
// WORKFLOW.<name>.md file.
func (c *Config) CheckBudgets() error {
	names := make([]string, 0, len(c.Budgets))
	for n := range c.Budgets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(c.Dir, ".sortie", "workflows", "WORKFLOW."+n+".md")); err != nil {
			return fmt.Errorf("workflows.%s: no WORKFLOW.%s.md in .sortie/workflows", n, n)
		}
	}
	return nil
}

// Cleanup configures the workspace cleanup command.
type Cleanup struct {
	Enabled    bool
	Interval   time.Duration
	Command    string
	MaxAgeDays int
	DryRun     bool
	Timeout    time.Duration
}

type cleanupFile struct {
	Enabled    bool   `yaml:"enabled"`
	Interval   string `yaml:"interval"`
	Command    string `yaml:"command"`
	MaxAgeDays int    `yaml:"max_age_days"`
	DryRun     bool   `yaml:"dry_run"`
	Timeout    string `yaml:"timeout"`
}

// Sleep configures idle sleep: the supervisor stops the engine children
// after IdleTimeout without work and starts them again after WakeInterval.
type Sleep struct {
	Enabled      bool
	IdleTimeout  time.Duration
	WakeInterval time.Duration
}

type sleepFile struct {
	Enabled      bool   `yaml:"enabled"`
	IdleTimeout  string `yaml:"idle_timeout"`
	WakeInterval string `yaml:"wake_interval"`
}

func (f sleepFile) resolve() (Sleep, error) {
	s := Sleep{Enabled: f.Enabled}
	for _, d := range []struct {
		key, val string
		dst      *time.Duration
	}{{"idle_timeout", f.IdleTimeout, &s.IdleTimeout}, {"wake_interval", f.WakeInterval, &s.WakeInterval}} {
		if d.val == "" {
			if s.Enabled {
				return s, fmt.Errorf("sleep.%s is required when sleep.enabled is true", d.key)
			}
			continue
		}
		v, err := time.ParseDuration(d.val)
		if err != nil || v <= 0 {
			return s, fmt.Errorf("sleep.%s: %q is not a positive duration (for example 15m)", d.key, d.val)
		}
		*d.dst = v
	}
	return s, nil
}

const defaultCleanupTimeout = 10 * time.Minute

func (f cleanupFile) resolve() (Cleanup, error) {
	c := Cleanup{
		Enabled: f.Enabled, Command: strings.TrimSpace(f.Command),
		MaxAgeDays: f.MaxAgeDays, DryRun: f.DryRun, Timeout: defaultCleanupTimeout,
	}
	for _, d := range []struct {
		key, val string
		dst      *time.Duration
	}{{"interval", f.Interval, &c.Interval}, {"timeout", f.Timeout, &c.Timeout}} {
		if d.val == "" {
			continue
		}
		v, err := time.ParseDuration(d.val)
		if err != nil || v <= 0 {
			return c, fmt.Errorf("cleanup.%s: %q is not a positive duration (for example 6h)", d.key, d.val)
		}
		*d.dst = v
	}
	if !c.Enabled {
		return c, nil
	}
	if c.Command == "" {
		return c, errors.New("cleanup.command is required when cleanup.enabled is true")
	}
	if c.Interval == 0 {
		return c, errors.New("cleanup.interval is required when cleanup.enabled is true")
	}
	if c.MaxAgeDays <= 0 {
		return c, errors.New("cleanup.max_age_days must be a positive number of days when cleanup.enabled is true")
	}
	return c, nil
}

// CheckCleanup fails when cleanup is enabled and max_age_days is not
// less than workspace.retention_days in a workflow file. Files without
// retention_days are ignored.
func (c *Config) CheckCleanup() error {
	if !c.Cleanup.Enabled {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(c.Dir, ".sortie", "workflows", "WORKFLOW.*.md"))
	if err != nil {
		return err
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		days, ok := retentionDays(raw)
		if ok && c.Cleanup.MaxAgeDays >= days {
			return fmt.Errorf("cleanup.max_age_days (%d) must be less than workspace.retention_days (%d) in %s",
				c.Cleanup.MaxAgeDays, days, filepath.Base(f))
		}
	}
	return nil
}

// retentionDays reads workspace.retention_days from a workflow's front
// matter, the block between the first two --- lines.
func retentionDays(workflow []byte) (int, bool) {
	lines := strings.Split(string(workflow), "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return 0, false
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "---" {
			break
		}
		if val, ok := strings.CutPrefix(line, "retention_days:"); ok {
			val, _, _ = strings.Cut(val, " #")
			n, err := strconv.Atoi(strings.TrimSpace(val))
			return n, err == nil
		}
	}
	return 0, false
}

// Label is one GitHub label managed by `jig setup`.
type Label struct {
	Name        string `yaml:"name"`
	Color       string `yaml:"color"`
	Description string `yaml:"description"`
}

// DefaultLabels are the labels for the shipped loops, seeded into a new
// config.yaml by setup. A custom loop adds its entries here, then setup
// creates them on GitHub (tracker support is GitHub-only).
//
// Three roles, one colour each: a yellow trigger starts a loop, in-progress
// is the claim an agent holds while it works, agent:done is the state an
// agent leaves behind when it finishes, and needs-human escalates to a
// person. Every name is prefixed agent: except those that describe the
// item or the person's decision rather than the agent.
var DefaultLabels = []Label{
	{"agent:plan", "fbca04", "Trigger: write an implementation plan for this issue"},
	{"agent:plan-build", "fbca04", "Trigger: plan this issue, then implement it with no human step"},
	{"agent:build", "fbca04", "Trigger: implement this issue, or apply review feedback on this PR"},
	{"agent:review", "fbca04", "Trigger: review this PR"},
	{"agent:merge", "fbca04", "Trigger: merge this PR"},
	{"agent:docs", "fbca04", "Trigger: update the docs for merged changes and open a PR"},
	{"in-progress", "5319e7", "State: claimed by an agent"},
	{"agent:done", "0e8a16", "State: agent finished"},
	{"needs-human", "d73a4a", "Escalation: agent needs a person"},
	{"united-into", "c5def5", "State: work folded into another issue's fix; that issue is the one to read"},
	{"backlog", "ededed", "Deferred by a person; do not dispatch"},
	{"P0", "d73a4a", "Priority: highest, dispatch before everything else"},
	{"P1", "f9826c", "Priority: high"},
	{"P2", "fbca04", "Priority: normal"},
	{"P3", "ededed", "Priority: lowest"},
}

// Load reads .sortie/config.yaml under dir (repo root), applies environment
// overrides, and derives git-based defaults. The config file must exist:
// a fresh checkout needs `jig setup` first, and running without
// it would silently fall back to defaults (wrong labels, no milestone)
// and dispatch on items the operator never scoped.
func Load(dir string) (*Config, error) {
	c := &Config{Dir: dir, Filters: map[string]string{}, Cleanup: Cleanup{Timeout: defaultCleanupTimeout}}
	if dir == "" {
		return nil, errors.New("config dir must not be empty")
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".sortie", "config.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no .sortie/config.yaml in %s (run `jig setup` first)", dir)
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if len(raw) > 0 {
		var file struct {
			Repo      string            `yaml:"repo"`
			Token     string            `yaml:"token"`
			Milestone string            `yaml:"milestone"`
			Assignee  *string           `yaml:"assignee"`
			Filters   map[string]string `yaml:"filters"`
			Labels    []Label           `yaml:"labels"`
			Cleanup   cleanupFile       `yaml:"cleanup"`
			Sleep     sleepFile         `yaml:"sleep"`
			Workflows map[string]struct {
				Budget budgetFile `yaml:"budget"`
			} `yaml:"workflows"`
		}
		if err := yaml.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("parse .sortie/config.yaml: %w", err)
		}
		c.Repo, c.Token, c.Milestone = file.Repo, file.Token, file.Milestone
		c.Assignee = file.Assignee
		c.Filters = file.Filters
		c.Labels = file.Labels
		cl, err := file.Cleanup.resolve()
		if err != nil {
			return nil, err
		}
		c.Cleanup = cl
		sl, err := file.Sleep.resolve()
		if err != nil {
			return nil, err
		}
		c.Sleep = sl
		c.Budgets = map[string]Budget{}
		for name, w := range file.Workflows {
			b, err := w.Budget.resolve(name)
			if err != nil {
				return nil, err
			}
			c.Budgets[name] = b
		}
	}
	if c.Filters == nil {
		c.Filters = map[string]string{}
	}
	if c.Labels == nil {
		c.Labels = DefaultLabels
	}
	if v := os.Getenv("JIG_REPO"); v != "" {
		c.Repo = v
	}
	if v := os.Getenv("JIG_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("JIG_MILESTONE"); v != "" {
		c.Milestone = v
	}
	if v := os.Getenv("GH_MILESTONE"); v != "" && c.Milestone == "" {
		c.Milestone = v
	}
	if c.Repo == "" {
		slug, err := detectRepo(dir)
		if err != nil {
			return nil, err
		}
		c.Repo = slug
	}
	c.Repo = NormalizeRepo(c.Repo)
	if c.Repo == "" || !strings.Contains(c.Repo, "/") {
		return nil, fmt.Errorf("expected owner/name, got %q (set repo: in .sortie/config.yaml or JIG_REPO)", c.Repo)
	}
	if c.Token == "" {
		if v := os.Getenv("GITHUB_TOKEN"); v != "" {
			c.Token = v
		} else if v := os.Getenv("GH_TOKEN"); v != "" {
			c.Token = v
		}
	}
	c.Tracker = c.Repo
	c.Workspaces = filepath.Join(dir, ".sortie", "workspaces")
	return c, nil
}

// defaultFilters are the label constraints for the shipped loops,
// mirroring each workflow's query_filter so the SORTIE_TRACKER_QUERY_FILTER
// env override (which replaces it) preserves the positive label clauses.
// The github-pr loops need them: the adapter sends a non-empty filter to
// GitHub search and lists from /pulls, without label clauses, when it is
// empty. A new
// WORKFLOW.*.md loop with no filters: entry gets defaultFilter (same
// scope, no label constraint), so it starts but matches nothing until
// the workflow's active_states or a filters: override narrows it.
//
// Every filter matches its triggers plus in-progress, so an item whose
// agent died mid-run is picked up again by the same loop. The issue and PR
// loops each list items of one kind, so they do not claim each other's
// items; the order of a workflow's dispatch rules settles an item that
// carries several triggers.
var defaultFilters = map[string]string{
	"issues": "label:agent:plan,agent:plan-build,agent:build,agent:docs,in-progress -label:needs-human",
	"prs":    "label:agent:review,agent:build,agent:merge,in-progress -label:needs-human",
}

// DefaultLabelFilter returns the shipped query filter for a loop with no
// milestone or assignee scope. The workflow files carry the same string, and
// the env override injected at launch replaces it; both must agree or the
// file would say one thing and the running loop another.
func DefaultLabelFilter(name string) string { return defaultFilters[name] }

const defaultFilter = "-label:needs-human"

// FilterFor returns the query filter for a loop: the filters: override
// when set, else the shipped default for known loops, else defaultFilter.
// Milestone and assignee scope apply to every filter including overrides.
// Default assignee scope is @me (the token owner); empty = no restriction.
func (c *Config) FilterFor(name string) string {
	base := c.Filters[name]
	if base == "" {
		base = defaultFilters[name]
		if base == "" {
			base = defaultFilter
		}
	}
	return withScope(withMilestone(base, c.Milestone), c.assignee())
}

func (c *Config) assignee() string {
	if v, ok := os.LookupEnv("JIG_ASSIGNEE"); ok {
		return v
	}
	if c.Assignee != nil {
		return *c.Assignee
	}
	return "@me"
}

// Env returns the environment for one engine subprocess.
// The token travels as SORTIE_TRACKER_API_KEY so it overrides
// tracker.api_key through the engine's curated env layer.
func (c *Config) Env() []string {
	return []string{
		"SORTIE_TRACKER_PROJECT=" + c.Tracker,
		"SORTIE_TRACKER_API_KEY=" + c.Token,
		"JIG_WORKSPACES=" + c.Workspaces,
	}
}

// CheckToken reports an error when no token resolved. An empty token
// reaches the engine as SORTIE_TRACKER_API_KEY= and surfaces there as an
// authentication failure partway into a run, with the loop already
// claimed and the agent mid-work. Failing here names the cause instead.
func (c *Config) CheckToken() error {
	if c.Token != "" {
		return nil
	}
	return errors.New("no GitHub token found. Set one of JIG_TOKEN, token: in .sortie/config.yaml, GITHUB_TOKEN, or GH_TOKEN (see the GitHub token section of the README)")
}

func withScope(base, assignee string) string {
	if assignee == "" {
		return base
	}
	if base == "" {
		return "assignee:" + assignee
	}
	return base + " assignee:" + assignee
}

func withMilestone(base, milestone string) string {
	if milestone == "" {
		return base
	}
	if base == "" {
		return `milestone:"` + milestone + `"`
	}
	return base + ` milestone:"` + milestone + `"`
}

// detectRepo derives owner/name from the git remote in dir.
func detectRepo(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", errors.New("cannot detect repo from git remote (not a git checkout or no origin remote? set repo: in .sortie/config.yaml or JIG_REPO)")
	}
	return NormalizeRepo(strings.TrimSpace(string(out))), nil
}

// NormalizeRepo reduces a git remote URL to its owner/name slug, so the
// same parser serves config, setup, and the display name.
func NormalizeRepo(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "github.com"); i >= 0 {
		s = s[i+len("github.com"):]
	}
	s = strings.Trim(s, "/:")
	if strings.HasPrefix(s, "git@") {
		s = strings.TrimPrefix(s, "git@")
	}
	if strings.Count(s, "/") > 1 {
		parts := strings.Split(s, "/")
		s = strings.Join(parts[len(parts)-2:], "/")
	}
	return s
}
