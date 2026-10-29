// Package config resolves Jigz's settings from its YAML file:
// which repositories run, which of each repository's workflows start,
// and the agent settings they run with.
//
// Every optional knob is a pointer so "not configured" stays distinct
// from "configured to the zero value". An unset knob is left out of
// the generated per-repository files and the child environment
// entirely, so Jigz and the engine apply their own
// shipped default rather than one restated here. This package
// deliberately holds no defaults of its own: a default belongs to the
// thing it defaults, and restating it here is how the two drift.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/dirs"
)

// Agent holds the agent settings one repository's loops run with. A
// nil field is unset and inherits from Defaults, or from the engine's
// own default when neither sets it.
type Agent struct {
	// Kind selects the sortie agent adapter (pi, claude-code, ...).
	Kind    string `yaml:"kind"`
	Command string `yaml:"command"`
	// Model is the adapter's model. No agent has a model entry in the
	// engine's SORTIE_* override registry, so it reaches the adapter as
	// a $VAR reference in the workflow's adapter block instead of an
	// env var; see Env.
	Model string `yaml:"model"`

	MaxTurns            *int `yaml:"max_turns"`
	MaxConcurrentAgents *int `yaml:"max_concurrent_agents"`
	TurnTimeoutMS       *int `yaml:"turn_timeout_ms"`
	ReadTimeoutMS       *int `yaml:"read_timeout_ms"`
	StallTimeoutMS      *int `yaml:"stall_timeout_ms"`
	StopGraceMS         *int `yaml:"stop_grace_ms"`
	MaxRetryBackoffMS   *int `yaml:"max_retry_backoff_ms"`
}

// Defaults applies to every repository that does not override the
// knob.
type Defaults struct {
	Assignee  *string `yaml:"assignee"`
	Milestone string  `yaml:"milestone"`
	// TokenEnv is the environment variable holding the tracker token
	// for any repository that does not name its own. It is a variable
	// name, never a token: the value stays in the environment and is
	// never written to a file by this program.
	TokenEnv string `yaml:"token_env"`
	Agent    Agent  `yaml:"agent"`
	// PollingIntervalMS is how often a loop asks the tracker for work.
	PollingIntervalMS *int `yaml:"polling_interval_ms"`
}

// Repo is one managed repository.
type Repo struct {
	// Repo is the tracker identity, owner/name. Unlike Jigz's
	// per-repo config it is required here: the state directory is not a
	// git checkout, so there is no origin remote to detect it from.
	Repo string `yaml:"repo"`
	// Path is where this repository's .jigz/ lives. Empty means
	// <state_dir>/repos/<owner>/<name>. It holds only loop config and
	// run state; the agent clones the repository itself into the
	// workspaces, so no checkout is made.
	Path string `yaml:"path"`
	// Enabled defaults to true. A disabled repository is configured and
	// labelled but never started.
	Enabled *bool `yaml:"enabled"`
	// Workflows names the loops to start (plan, build, ...). Empty
	// starts every workflow installed in the repository.
	Workflows []string `yaml:"workflows"`
	// TokenEnv names an environment variable holding the GitHub token
	// for this repository, for a setup that loops over several bot
	// accounts. The token travels in the child environment and is
	// never written to a file.
	TokenEnv string `yaml:"token_env"`
	// Assignee and Milestone scope the tracker query, empty to leave
	// Jigz's own handling in place.
	Assignee  *string           `yaml:"assignee"`
	Milestone string            `yaml:"milestone"`
	Filters   map[string]string `yaml:"filters"`

	Agent             Agent `yaml:"agent"`
	PollingIntervalMS *int  `yaml:"polling_interval_ms"`

	// Overrides are per-workflow settings, keyed by workflow name, for
	// a repository whose loops do not all run the same way.
	Overrides map[string]Workflow `yaml:"workflow_overrides"`
}

// Workflow is one loop's settings within a repository.
type Workflow struct {
	Agent             Agent `yaml:"agent"`
	PollingIntervalMS *int  `yaml:"polling_interval_ms"`
}

// Settings are resolved knob values for one scope, normalized to
// strings. One map per scope is what makes the env/file partition below
// mechanical rather than a field-by-field decision repeated twice.
type Settings map[string]string

// knobEnv maps a workflow front-matter key to the SORTIE_* variable the
// engine reads it from. A key with an empty value has no engine entry
// and is file-only: model is the one today, reached as a $VAR reference
// inside the adapter block.
var knobEnv = map[string]string{
	"kind":                  "SORTIE_AGENT_KIND",
	"command":               "SORTIE_AGENT_COMMAND",
	"max_turns":             "SORTIE_AGENT_MAX_TURNS",
	"max_concurrent_agents": "SORTIE_AGENT_MAX_CONCURRENT_AGENTS",
	"turn_timeout_ms":       "SORTIE_AGENT_TURN_TIMEOUT_MS",
	"read_timeout_ms":       "SORTIE_AGENT_READ_TIMEOUT_MS",
	"stall_timeout_ms":      "SORTIE_AGENT_STALL_TIMEOUT_MS",
	"stop_grace_ms":         "SORTIE_AGENT_STOP_GRACE_MS",
	"max_retry_backoff_ms":  "SORTIE_AGENT_MAX_RETRY_BACKOFF_MS",
	"polling_interval_ms":   "SORTIE_POLLING_INTERVAL_MS",
	"model":                 "",
}

// repoSettings reads the repository-level knobs, skipping every unset
// one so it never reaches a file or the environment.
func repoSettings(a Agent, polling *int) Settings {
	s := Settings{}
	for k, v := range map[string]string{
		"kind":    a.Kind,
		"command": a.Command,
		"model":   a.Model,
	} {
		if v != "" {
			s[k] = v
		}
	}
	for k, v := range map[string]*int{
		"max_turns":             a.MaxTurns,
		"max_concurrent_agents": a.MaxConcurrentAgents,
		"turn_timeout_ms":       a.TurnTimeoutMS,
		"read_timeout_ms":       a.ReadTimeoutMS,
		"stall_timeout_ms":      a.StallTimeoutMS,
		"stop_grace_ms":         a.StopGraceMS,
		"max_retry_backoff_ms":  a.MaxRetryBackoffMS,
		"polling_interval_ms":   polling,
	} {
		if v != nil {
			s[k] = fmt.Sprint(*v)
		}
	}
	return s
}

// workflowSettings reads one workflow's own knobs.
func workflowSettings(w Workflow) Settings { return repoSettings(w.Agent, w.PollingIntervalMS) }

// FileSettings returns the knob values that must be written into one
// workflow's front matter: the repository value for every knob any
// workflow overrides, with this workflow's own value winning.
//
// The partition exists because the engine applies SORTIE_* over front
// matter, and that environment is per repository. A knob that any
// workflow overrides therefore cannot be set in the environment at all,
// or it would silently beat the override on every workflow.
func (r Resolved) FileSettings(workflow string) Settings {
	own := r.Overrides[workflow]
	out := Settings{}
	for k := range r.OverriddenKnobs() {
		if v, ok := r.Knobs[k]; ok {
			out[k] = v
		}
	}
	// A knob with no engine variable has nothing but the file to travel
	// in, so it is always written rather than only when overridden.
	for k, v := range r.Knobs {
		if knobEnv[k] == "" {
			out[k] = v
		}
	}
	for k, v := range own {
		out[k] = v
	}
	return out
}

// OverriddenKnobs is every knob any workflow in this repository
// overrides.
func (r Resolved) OverriddenKnobs() map[string]bool {
	out := map[string]bool{}
	for _, s := range r.Overrides {
		for k := range s {
			out[k] = true
		}
	}
	return out
}

// Access holds the keys of an access block from an older config. They are
// accepted so that the file still loads, and have no effect.
type Access struct {
	PublicRead   bool `yaml:"public_read"`
	TrustedProxy bool `yaml:"trusted_proxy"`
}

// GitHub is the github block: the optional enrichment of run history from
// the GitHub REST API, off unless Enrich is set, and the question scanner,
// on unless Questions is false.
type GitHub struct {
	Enrich bool `yaml:"enrich"`
	// Questions switches the question scanner, its resume and the
	// Questions page; nil means true. Read it with QuestionsOn.
	Questions *bool `yaml:"questions"`
	// Interval is a Go duration, DefaultGitHubInterval when empty.
	Interval string `yaml:"interval"`
	// APIURL is DefaultGitHubAPI when empty.
	APIURL string `yaml:"api_url"`
	// QuestionsInterval is a Go duration, DefaultQuestionsInterval when
	// empty. It is the time between question scan passes.
	QuestionsInterval string `yaml:"questions_interval"`
	// QuestionsWindow is a Go duration, DefaultQuestionsWindow when empty.
	// It is how far back a scan looks for stored runs.
	QuestionsWindow string `yaml:"questions_window"`
	// Details also reads commits, changed files and check runs of each
	// cached pull request. It has no effect unless Enrich is set.
	Details bool `yaml:"details"`
}

// QuestionsOn reports whether the question scanner runs for the project.
func (g GitHub) QuestionsOn() bool { return g.Questions == nil || *g.Questions }

// DetailsOn reports whether the extra pull request detail is read.
func (g GitHub) DetailsOn() bool { return g.Enrich && g.Details }

// Defaults of the github section.
const (
	DefaultGitHubInterval    = 10 * time.Minute
	MinGitHubInterval        = time.Minute
	DefaultGitHubAPI         = "https://api.github.com"
	DefaultQuestionsInterval = 2 * time.Minute
	MinQuestionsInterval     = time.Minute
	DefaultQuestionsWindow   = 168 * time.Hour
)

// PassInterval is the time between enrichment passes.
func (g GitHub) PassInterval() time.Duration {
	d, err := time.ParseDuration(g.Interval)
	if err != nil || d < MinGitHubInterval {
		return DefaultGitHubInterval
	}
	return d
}

// QuestionPassInterval is the time between question scan passes.
func (g GitHub) QuestionPassInterval() time.Duration {
	d, err := time.ParseDuration(g.QuestionsInterval)
	if err != nil || d < MinQuestionsInterval {
		return DefaultQuestionsInterval
	}
	return d
}

// QuestionWindow is how far back a scan looks for stored runs.
func (g GitHub) QuestionWindow() time.Duration {
	d, err := time.ParseDuration(g.QuestionsWindow)
	if err != nil || d <= 0 {
		return DefaultQuestionsWindow
	}
	return d
}

// BaseURL is the API root without a trailing slash.
func (g GitHub) BaseURL() string {
	if g.APIURL == "" {
		return DefaultGitHubAPI
	}
	return strings.TrimRight(g.APIURL, "/")
}

// Validate checks the block's durations and API URL.
func (g GitHub) Validate() error {
	if fe := g.validate(); fe != nil {
		return fe
	}
	return nil
}

func (g GitHub) validate() *FieldError {
	if g.Interval != "" {
		d, err := time.ParseDuration(g.Interval)
		if err != nil || d < MinGitHubInterval {
			return &FieldError{Path: "github.interval", Err: fmt.Errorf("github.interval %q is not a duration of at least %s, such as 10m", g.Interval, MinGitHubInterval)}
		}
	}
	if g.QuestionsInterval != "" {
		d, err := time.ParseDuration(g.QuestionsInterval)
		if err != nil || d < MinQuestionsInterval {
			return &FieldError{Path: "github.questions_interval", Err: fmt.Errorf("github.questions_interval %q is not a duration of at least %s, such as 2m", g.QuestionsInterval, MinQuestionsInterval)}
		}
	}
	if g.QuestionsWindow != "" {
		d, err := time.ParseDuration(g.QuestionsWindow)
		if err != nil || d <= 0 {
			return &FieldError{Path: "github.questions_window", Err: fmt.Errorf("github.questions_window %q is not a positive duration, such as 168h", g.QuestionsWindow)}
		}
	}
	if g.APIURL != "" {
		u, err := url.Parse(g.APIURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return &FieldError{Path: "github.api_url", Err: fmt.Errorf("github.api_url %q is not an http or https URL", g.APIURL)}
		}
	}
	return nil
}

// Config is a loaded Jigz config.
type Config struct {
	Access   Access   `yaml:"access"`
	GitHub   GitHub   `yaml:"github"`
	Defaults Defaults `yaml:"defaults"`
	Repos    []Repo   `yaml:"repos"`
	// StateDir is the root under which per-repository state lives.
	StateDir string `yaml:"state_dir"`
}

// Resolved is one repository's settings with the defaults cascade
// applied: what to run, where, and what environment its Jigz
// child needs.
type Resolved struct {
	Repo Repo
	// Dir is where this repository's .jigz/ goes.
	Dir string
	// Workflows is the resolved loop set. Empty means every installed
	// workflow, passed through as an unset JIG_ONLY.
	Workflows []string
	// Milestone and Assignee are the cascade-resolved tracker scope.
	// Assignee nil means "leave it to Jigz".
	Milestone string
	Assignee  *string
	// TokenEnv is the cascade-resolved environment variable holding the
	// tracker token, empty to fall back to the engine's own lookup.
	TokenEnv string
	// Agent is the cascade-resolved agent settings.
	Agent Agent
	// PollingIntervalMS is the cascade-resolved poll interval, nil when
	// unset.
	PollingIntervalMS *int
	// Knobs is the cascade-resolved repository-level knobs, normalized.
	Knobs Settings
	// Overrides holds each workflow's own knobs. A nil entry means the
	// workflow takes the repository values.
	Overrides map[string]Settings
}

// Enabled reports whether this repository should be started.
func (r Resolved) Enabled() bool { return r.Repo.Enabled == nil || *r.Repo.Enabled }

// FieldError is a validation failure that names the YAML path it belongs
// to, so an editor can show it next to the field.
type FieldError struct {
	Path string
	Err  error
}

func (e *FieldError) Error() string { return e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }

// validate rejects a repository Jigz cannot run. These are checked
// at load time so a typo in a config that supervises live issue
// backlogs surfaces immediately rather than at the first claim.
func (r *Repo) validate() error {
	slug := r.Repo
	if parts := strings.Split(slug, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("%q is not owner/name", slug)
	}
	if strings.ContainsAny(slug, " \t") {
		return fmt.Errorf("%q has whitespace", slug)
	}
	for _, w := range r.Workflows {
		if strings.TrimSpace(w) == "" {
			return fmt.Errorf("%s: empty workflow name", slug)
		}
	}
	// Workflow names are not checked against a known set: Jigz does
	// not ship the workflows, so it cannot know their names, and a
	// stale list here would reject a name Jigz still supports.
	// Jigz fails loudly on an unknown name at start.
	return nil
}

// Dir returns the directory holding this repository's .jigz/.
func (c *Config) Dir(r Repo) (string, error) {
	if r.Path == "" {
		state, err := c.stateDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(state, "repos", filepath.FromSlash(r.Repo)), nil
	}
	abs, err := filepath.Abs(expandHome(r.Path))
	if err != nil {
		return "", fmt.Errorf("%s: path: %w", r.Repo, err)
	}
	return abs, nil
}

// stateDir is the configured root, or the per-user default.
func (c *Config) stateDir() (string, error) {
	p := c.StateDir
	if p == "" {
		home, err := dirs.State()
		if err != nil {
			return "", fmt.Errorf("state_dir unset and no home directory: %w", err)
		}
		p = home
	}
	abs, err := filepath.Abs(expandHome(p))
	if err != nil {
		return "", fmt.Errorf("state_dir %q: %w", p, err)
	}
	return abs, nil
}

// StateDirPath is the state directory with the default and ~ applied.
func (c *Config) StateDirPath() (string, error) { return c.stateDir() }

// Resolve applies the defaults cascade to every repository. It fails on
// the first bad entry so a run never starts a partial fleet.
func (c *Config) Resolve() ([]Resolved, error) {
	out := make([]Resolved, 0, len(c.Repos))
	for _, r := range c.Repos {
		dir, err := c.Dir(r)
		if err != nil {
			return nil, err
		}
		// Only the selected workflows are installed, so the set on disk
		// is the set that starts; an empty selection installs them all.
		res := Resolved{
			Repo:              r,
			Dir:               dir,
			Workflows:         append([]string(nil), r.Workflows...),
			Milestone:         cmp(r.Milestone, c.Defaults.Milestone),
			TokenEnv:          cmp(r.TokenEnv, c.Defaults.TokenEnv),
			Assignee:          firstNonNil(r.Assignee, c.Defaults.Assignee),
			Agent:             mergeAgent(c.Defaults.Agent, r.Agent),
			PollingIntervalMS: firstNonNil(r.PollingIntervalMS, c.Defaults.PollingIntervalMS),
			Overrides:         map[string]Settings{},
		}
		res.Knobs = repoSettings(res.Agent, res.PollingIntervalMS)
		// A workflow's own knobs are layered over the repository's, so
		// an unset field in an override inherits instead of blanking.
		for name, w := range r.Overrides {
			own := workflowSettings(w)
			if len(own) == 0 {
				continue
			}
			res.Overrides[name] = own
		}
		out = append(out, res)
	}
	return out, nil
}

// Enabled returns the repositories that should be running.
func Enabled(all []Resolved) []Resolved {
	var out []Resolved
	for _, r := range all {
		if r.Enabled() {
			out = append(out, r)
		}
	}
	return out
}

// Token returns this repository's tracker token. token_env names the
// variable when a fleet loops over several bot accounts; otherwise the
// engine's own GITHUB_TOKEN/GH_TOKEN fallback applies. The token only
// ever travels in a process environment, never in a file.
func (r Resolved) Token() string {
	if name := r.TokenEnv; name != "" {
		return os.Getenv(name)
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		return v
	}
	return os.Getenv("GH_TOKEN")
}

// ValidateEnv returns the environment for a direct `sortie validate` of
// this repository's workflow files, on top of Env.
//
// The child process is Jigz, which derives the tracker project,
// token, clone URL and workspace root from its own config at launch.
// Validation calls the engine directly and so has to supply them
// itself: without them the workflow's $SORTIE_TRACKER_PROJECT and
// $JIG_WORKSPACES references expand to empty, and validation
// rejects a repository that would run correctly under the child.
func (r Resolved) ValidateEnv() ([]string, error) {
	env, err := r.Env()
	if err != nil {
		return nil, err
	}
	return append(env,
		"SORTIE_TRACKER_PROJECT="+r.Repo.Repo,
		"SORTIE_TRACKER_API_KEY="+r.Token(),
		"JIG_WORKSPACES="+filepath.Join(r.Dir, ".jigz", "workspaces"),
	), nil
}

// Env returns the environment additions for one repository's
// Jigz child. The child passes its own environment on to every
// loop it starts, so these reach the engine too. An unset knob
// contributes nothing and leaves the engine's default alone.
//
// A knob any workflow overrides is deliberately absent, because the
// engine applies the environment over front matter and that
// environment is shared by every loop: putting it here would silently
// beat the override on all of them. Those knobs are written into each
// workflow's file instead; see FileSettings.
func (r Resolved) Env() ([]string, error) {
	overridden := r.OverriddenKnobs()
	env := map[string]string{}
	for k, v := range r.Knobs {
		if overridden[k] {
			continue
		}
		if name := knobEnv[k]; name != "" {
			env[name] = v
		}
	}
	if len(r.Workflows) > 0 {
		env["JIG_ONLY"] = strings.Join(r.Workflows, ",")
	}
	if name := r.TokenEnv; name != "" {
		tok := r.Token()
		if tok == "" {
			return nil, fmt.Errorf("%s: token_env %s is unset or empty", r.Repo.Repo, name)
		}
		// The highest-precedence override in Jigz's config, so
		// the token stays in the environment and out of every file.
		env["JIG_TOKEN"] = tok
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out, nil
}

// mergeAgent fills r's unset fields from d. Set fields win, including
// a field set to zero, which is why the numeric knobs are pointers.
func mergeAgent(d, r Agent) Agent {
	m := r
	if m.Kind == "" {
		m.Kind = d.Kind
	}
	if m.Command == "" {
		m.Command = d.Command
	}
	if m.Model == "" {
		m.Model = d.Model
	}
	m.MaxTurns = firstNonNil(m.MaxTurns, d.MaxTurns)
	m.MaxConcurrentAgents = firstNonNil(m.MaxConcurrentAgents, d.MaxConcurrentAgents)
	m.TurnTimeoutMS = firstNonNil(m.TurnTimeoutMS, d.TurnTimeoutMS)
	m.ReadTimeoutMS = firstNonNil(m.ReadTimeoutMS, d.ReadTimeoutMS)
	m.StallTimeoutMS = firstNonNil(m.StallTimeoutMS, d.StallTimeoutMS)
	m.StopGraceMS = firstNonNil(m.StopGraceMS, d.StopGraceMS)
	m.MaxRetryBackoffMS = firstNonNil(m.MaxRetryBackoffMS, d.MaxRetryBackoffMS)
	return m
}

func firstNonNil[T any](v, fallback *T) *T {
	if v != nil {
		return v
	}
	return fallback
}

func cmp(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// Where a cascaded value was set.
const (
	OriginDefault  = "default"
	OriginRepo     = "repo"
	OriginWorkflow = "workflow"
)

// Knobs lists the agent settings in display order.
var Knobs = []string{
	"kind", "command", "model", "max_turns", "max_concurrent_agents",
	"turn_timeout_ms", "read_timeout_ms", "stall_timeout_ms", "stop_grace_ms",
	"max_retry_backoff_ms", "polling_interval_ms",
}

// Setting is one cascaded knob value and the scope that set it.
type Setting struct {
	Value  string
	Origin string
}

// Cascade returns the knob values in force for a scope after the defaults
// cascade, each with where it came from. An empty repo gives the defaults
// alone, and an empty workflow stops at the repository. Knobs set nowhere
// are absent: the engine's own default applies.
func (c *Config) Cascade(repo, workflow string) map[string]Setting {
	out := map[string]Setting{}
	layer := func(s Settings, origin string) {
		for k, v := range s {
			out[k] = Setting{Value: v, Origin: origin}
		}
	}
	layer(repoSettings(c.Defaults.Agent, c.Defaults.PollingIntervalMS), OriginDefault)
	for _, r := range c.Repos {
		if r.Repo != repo {
			continue
		}
		layer(repoSettings(r.Agent, r.PollingIntervalMS), OriginRepo)
		if w, ok := r.Overrides[workflow]; ok {
			layer(workflowSettings(w), OriginWorkflow)
		}
	}
	return out
}
