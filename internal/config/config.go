// Package config resolves the loop's runtime settings from .jigz/config.yaml
// with environment overrides, and derives the repo identity from git.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"gopkg.in/yaml.v3"

	fleetconfig "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/machine"
)

// Config holds the resolved runtime settings for one loop run.
type Config struct {
	Repo  string `yaml:"repo"`
	Token string `yaml:"token"`

	Dir       string // repo root (sortie dir is Dir/.jigz)
	SortieBin string // resolved sortie binary path
	EnvFile   string // path to generated env file passed via --env-file
	Tracker   string // tracker project, always == Repo
	// Filters overrides the default per-loop query filters by loop name
	// (the WORKFLOW.*.md stem). Unknown loops get defaultFilter; known
	// loops fall back to their shipped default when unset. A work
	// entry's query_filter wins over these.
	Filters map[string]string `yaml:"filters"`
	// Labels are the GitHub labels setup ensures exist (create-or-edit;
	// labels absent here are left alone). Empty = DefaultLabels.
	Labels     []Label `yaml:"labels"`
	Workspaces string  // .jigz/workspaces under Dir
	Cleanup    Cleanup
	Sleep      Sleep
	// GitHub is the dashboard's GitHub enrichment and question scanning.
	GitHub fleetconfig.GitHub

	// Engine is the engine: section. Empty fields mean the engine
	// defaults (kinged007/sortie v1.28.1).
	Engine machine.Engine
	// OperatingHours gates every loop. No windows means 24/7.
	OperatingHours machine.Hours
	// TrackerDefaults and Hooks are the defaults every work entry may
	// override with its tracker: and hooks: blocks.
	TrackerDefaults Tracker
	Hooks           Hooks
	// Agents and Pools are the fleet; both share one name space. Work is
	// the work: section keyed by slug.
	Agents map[string]Agent
	Pools  map[string][]string
	Work   map[string]Work
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

func (f budgetFile) resolve(key string) (Budget, error) {
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

// Budgets returns the resolved budget of every work entry that has one,
// keyed by work slug. An entry with MaxTokens 0 is disabled.
func (c *Config) Budgets() map[string]Budget {
	out := map[string]Budget{}
	for slug, w := range c.Work {
		if w.Budget != nil {
			out[slug] = *w.Budget
		}
	}
	return out
}

// CheckBudgets fails when a work entry with a budget names a slug that
// has no WORKFLOW.<slug>.md file.
func (c *Config) CheckBudgets() error {
	names := make([]string, 0, len(c.Work))
	for n := range c.Work {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if c.Work[n].Budget == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(c.Dir, ".jigz", "workflows", "WORKFLOW."+n+".md")); err != nil {
			return fmt.Errorf("work.%s: no WORKFLOW.%s.md in .jigz/workflows", n, n)
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
	files, err := filepath.Glob(filepath.Join(c.Dir, ".jigz", "workflows", "WORKFLOW.*.md"))
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

// configFile is the document of .jigz/config.yaml.
type configFile struct {
	Repo    string             `yaml:"repo"`
	Token   string             `yaml:"token"`
	Filters map[string]string  `yaml:"filters"`
	Labels  []Label            `yaml:"labels"`
	Cleanup cleanupFile        `yaml:"cleanup"`
	Sleep   sleepFile          `yaml:"sleep"`
	GitHub  fleetconfig.GitHub `yaml:"github"`

	Engine         machine.Engine      `yaml:"engine"`
	OperatingHours machine.Hours       `yaml:"operating_hours"`
	Tracker        Tracker             `yaml:"tracker"`
	Hooks          Hooks               `yaml:"hooks"`
	Agents         map[string]Agent    `yaml:"agents"`
	Pools          map[string][]string `yaml:"pools"`
	Work           map[string]workFile `yaml:"work"`
}

// removedKeys are the keys the single-file model replaces. The error
// names the replacement where one exists.
var removedKeys = map[string]string{
	"milestone": "milestone is not configurable",
	"assignee":  "the assignee scope is always assignee:@me",
	"workflows": "workflows.<name>.budget moves to work.<slug>.budget",
}

// parseFile decodes the config file and rejects unknown keys at every
// level, naming the field path of the key.
func parseFile(raw []byte) (configFile, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return configFile{}, fmt.Errorf("parse .jigz/config.yaml: %w", err)
	}
	if root.Kind != 0 && len(root.Content) > 0 {
		doc := root.Content[0]
		if doc.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(doc.Content); i += 2 {
				key := doc.Content[i].Value
				if repl, ok := removedKeys[key]; ok {
					return configFile{}, fmt.Errorf("parse .jigz/config.yaml: unknown key %q (%s)", key, repl)
				}
			}
		}
		if err := checkKeys(doc, reflect.TypeOf(configFile{}), ""); err != nil {
			return configFile{}, fmt.Errorf("parse .jigz/config.yaml: %w", err)
		}
	}
	var file configFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return configFile{}, fmt.Errorf("parse .jigz/config.yaml: %w", err)
	}
	return file, nil
}

// Load reads .jigz/config.yaml under dir (repo root), applies environment
// overrides, and derives git-based defaults. The config file must exist:
// a fresh checkout needs `jig setup` first, and running without
// it would silently fall back to defaults (wrong labels, no query
// scope) and dispatch on items the operator never scoped.
func Load(dir string) (*Config, error) {
	c := &Config{Dir: dir, Filters: map[string]string{}, Cleanup: Cleanup{Timeout: defaultCleanupTimeout}}
	if dir == "" {
		return nil, errors.New("config dir must not be empty")
	}
	MigrateRepoDir(dir)
	raw, err := os.ReadFile(filepath.Join(dir, ".jigz", "config.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no .jigz/config.yaml in %s (run `jig setup` first)", dir)
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if len(raw) > 0 {
		file, err := parseFile(raw)
		if err != nil {
			return nil, err
		}
		if err := file.GitHub.Validate(); err != nil {
			return nil, err
		}
		c.GitHub = file.GitHub
		c.Repo, c.Token = file.Repo, file.Token
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
		c.Engine = file.Engine
		c.OperatingHours = file.OperatingHours
		c.TrackerDefaults = file.Tracker
		c.Hooks = file.Hooks
		c.Agents = file.Agents
		c.Pools = file.Pools
		c.Work = map[string]Work{}
		for slug, wf := range file.Work {
			w, err := wf.resolve(slug)
			if err != nil {
				return nil, err
			}
			c.Work[slug] = w
		}
		if err := c.validateModel(); err != nil {
			return nil, err
		}
	}
	if c.Filters == nil {
		c.Filters = map[string]string{}
	}
	if c.Labels == nil {
		c.Labels = DefaultLabels
	}
	if c.Agents == nil {
		c.Agents = map[string]Agent{}
	}
	if c.Pools == nil {
		c.Pools = map[string][]string{}
	}
	if c.Work == nil {
		c.Work = map[string]Work{}
	}
	if v := os.Getenv("JIG_REPO"); v != "" {
		c.Repo = v
	}
	if v := os.Getenv("JIG_TOKEN"); v != "" {
		c.Token = v
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
		return nil, fmt.Errorf("expected owner/name, got %q (set repo: in .jigz/config.yaml or JIG_REPO)", c.Repo)
	}
	if c.Token == "" {
		if v := os.Getenv("GITHUB_TOKEN"); v != "" {
			c.Token = v
		} else if v := os.Getenv("GH_TOKEN"); v != "" {
			c.Token = v
		}
	}
	c.Tracker = c.Repo
	c.Workspaces = filepath.Join(dir, ".jigz", "workspaces")
	return c, nil
}

// LoadToken returns the token Load would take from JIG_TOKEN or the token:
// key of dir's .jigz/config.yaml, "" when neither is set. Unlike Load it
// does not migrate directories or run git, so the dashboard can call it on
// every pass.
func LoadToken(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ".jigz", "config.yaml"))
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}
	file, err := parseFile(raw)
	if err != nil {
		return "", err
	}
	if v := os.Getenv("JIG_TOKEN"); v != "" {
		return v, nil
	}
	return file.Token, nil
}

// LoadGitHub reads only the github block of dir's .jigz/config.yaml. It is
// the cheap read the dashboard repeats so a config edit applies without a
// restart.
func LoadGitHub(dir string) (fleetconfig.GitHub, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ".jigz", "config.yaml"))
	if err != nil {
		return fleetconfig.GitHub{}, fmt.Errorf("read config: %w", err)
	}
	file, err := parseFile(raw)
	if err != nil {
		return fleetconfig.GitHub{}, err
	}
	if err := file.GitHub.Validate(); err != nil {
		return fleetconfig.GitHub{}, err
	}
	return file.GitHub, nil
}

// MigrateRepoDir moves a repo-local `.sortie/` directory to `.jigz/`
// so an install made before the rename keeps its config, workflows,
// logs, workspaces and generated env. Idempotent: when `.jigz` already
// exists, a leftover `.sortie` is left alone. Fails are silent here;
// callers that need the config surface the missing file themselves.
// ponytail: rename is enough for now, merge when both hold files.
func MigrateRepoDir(dir string) {
	old := filepath.Join(dir, ".sortie")
	cur := filepath.Join(dir, ".jigz")
	if _, err := os.Stat(cur); err == nil {
		return
	}
	_ = os.Rename(old, cur)
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
// query_filter or filters override. The workflow files carry the same
// string, and the env override injected at launch replaces it; both must
// agree or the file would say one thing and the running loop another.
func DefaultLabelFilter(name string) string { return defaultFilters[name] }

const defaultFilter = "-label:needs-human"

// FilterFor returns the query filter for a loop: the work entry's
// query_filter when set (it is the complete tracker query), else the
// filters: override, else the shipped default for known loops, else
// defaultFilter. Jigz appends the assignee scope at launch, so every
// filter carries assignee:@me.
func (c *Config) FilterFor(name string) string {
	base := c.Work[name].QueryFilter
	if base == "" {
		base = c.Filters[name]
	}
	if base == "" {
		base = defaultFilters[name]
		if base == "" {
			base = defaultFilter
		}
	}
	return base + " assignee:@me"
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
	return errors.New("no GitHub token found. Set one of JIG_TOKEN, token: in .jigz/config.yaml, GITHUB_TOKEN, or GH_TOKEN (see the GitHub token section of the README)")
}

// detectRepo derives owner/name from the git remote in dir.
func detectRepo(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", errors.New("cannot detect repo from git remote (not a git checkout or no origin remote? set repo: in .jigz/config.yaml or JIG_REPO)")
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
