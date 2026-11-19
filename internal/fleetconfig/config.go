// Package config holds the dashboard's per-repository DTO and the github
// block. Per-repository files under .jigz/ are the source of truth;
// there is no fleet-wide cascade.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Agent holds the agent settings one repository's loops run with. A
// nil field is unset and left out of generated files and the child
// environment, so the engine applies its own shipped default.
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

// Resolved is one repository's settings: what to run, where, and what
// environment its loops need.
type Resolved struct {
	Repo Repo
	// Dir is where this repository's .jigz/ goes.
	Dir string
	// Workflows is the loop set. Empty means every installed workflow.
	Workflows []string
	// Milestone and Assignee scope the tracker query.
	// Assignee nil means "leave it to Jigz".
	Milestone string
	Assignee  *string
	// TokenEnv is the environment variable holding the
	// tracker token, empty to fall back to the engine's own lookup.
	TokenEnv string
	// Agent is the agent settings.
	Agent Agent
	// PollingIntervalMS is the poll interval, nil when
	// unset.
	PollingIntervalMS *int
	// Knobs is the repository-level knobs, normalized.
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
