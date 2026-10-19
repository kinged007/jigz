package edit

// Central is the fleet-wide part of sortie-central.yaml.
type Central struct {
	StateDir     string `f:"state_dir" label:"State directory" hint:"Where project state, central.db and backups live. Empty uses ~/.local/state/sortie-central." group:"Central"`
	Assignee     string `f:"defaults.assignee" label:"Default assignee" hint:"@me picks issues assigned to the token owner. Empty leaves sortie-loop's own handling." group:"Defaults for every project"`
	Milestone    string `f:"defaults.milestone" label:"Default milestone" group:"Defaults for every project"`
	TokenEnv     string `f:"defaults.token_env" label:"Token variable" hint:"The name of the environment variable holding the GitHub token, never the token itself." group:"Defaults for every project"`
	PublicRead   *bool  `f:"access.public_read" label:"Public read" hint:"True lets signed-out visitors view pages. Every change still needs the password. Applied at once." group:"Access"`
	TrustedProxy *bool  `f:"access.trusted_proxy" label:"Trust the reverse proxy" hint:"True trusts X-Forwarded-Proto and X-Forwarded-For. Only set it behind a proxy. Needs a restart of central." group:"Access"`
	GitHubEnrich *bool  `f:"github.enrich" label:"Enrich from GitHub" hint:"True reads pull requests from GitHub with each project's token to show branch, PR link and PRs opened and merged. Read only. Applied at once." group:"GitHub enrichment"`
	GitHubEvery  string `f:"github.interval" label:"Pass interval" hint:"How often GitHub is read, such as 10m. At least 1m. Empty uses 10m." group:"GitHub enrichment"`
	GitHubDetail *bool  `f:"github.details" label:"Read commits and checks" hint:"True also reads the commits, changed files and check runs of each pull request, with up to two more requests per pull request. Only used when enrichment is on. Applied at once." group:"GitHub enrichment"`
	GitHubAPI    string `f:"github.api_url" label:"API URL" hint:"Empty uses https://api.github.com. Set it for GitHub Enterprise Server." group:"GitHub enrichment"`
}

// Agent is the agent settings of one scope: the defaults, one project, or
// one workflow override. The paths are relative to that scope's mapping.
type Agent struct {
	Kind            string `f:"agent.kind" label:"Agent kind" hint:"The sortie agent adapter." opts:"pi,claude-code,codex,cursor" group:"Agent"`
	Command         string `f:"agent.command" label:"Command" group:"Agent"`
	Model           string `f:"agent.model" label:"Model" group:"Agent"`
	MaxTurns        *int   `f:"agent.max_turns" label:"Max turns" group:"Agent"`
	MaxConcurrent   *int   `f:"agent.max_concurrent_agents" label:"Max concurrent agents" group:"Agent"`
	TurnTimeout     *int   `f:"agent.turn_timeout_ms" label:"Turn timeout" hint:"Milliseconds." group:"Timeouts"`
	ReadTimeout     *int   `f:"agent.read_timeout_ms" label:"Read timeout" hint:"Milliseconds." group:"Timeouts"`
	StallTimeout    *int   `f:"agent.stall_timeout_ms" label:"Stall timeout" hint:"Milliseconds." group:"Timeouts"`
	StopGrace       *int   `f:"agent.stop_grace_ms" label:"Stop grace" hint:"Milliseconds." group:"Timeouts"`
	RetryBackoff    *int   `f:"agent.max_retry_backoff_ms" label:"Retry backoff, maximum" hint:"Milliseconds." group:"Backoff and polling"`
	PollingInterval *int   `f:"polling_interval_ms" label:"Polling interval" hint:"How often a loop asks the tracker for work, in milliseconds." group:"Backoff and polling"`
}

// Project is one entry of repos.
type Project struct {
	Repo      string            `f:"repo" label:"Repository" hint:"owner/name" group:"Project"`
	Path      string            `f:"path" label:"Local path" hint:"Where the project's .sortie directory lives. Empty uses the state directory." group:"Project"`
	Enabled   *bool             `f:"enabled" label:"Enabled" hint:"Empty means enabled. A disabled project is never started." group:"Project"`
	Workflows []string          `f:"workflows" label:"Workflows" hint:"Comma separated. Empty starts every installed workflow." group:"Project"`
	TokenEnv  string            `f:"token_env" label:"Token variable" hint:"The name of the environment variable holding the GitHub token, never the token itself." group:"Tracker"`
	Assignee  string            `f:"assignee" label:"Assignee" group:"Tracker"`
	Milestone string            `f:"milestone" label:"Milestone" group:"Tracker"`
	Filters   map[string]string `f:"filters" label:"Filters" hint:"One per line, as workflow: query." group:"Tracker"`
}

// Rule is one dispatch rule of a workflow file.
type Rule struct {
	Name     string   `f:"name" label:"Name"`
	Labels   []string `f:"match.labels" label:"Match labels" hint:"Comma separated"`
	Agent    string   `f:"agent" label:"Agent kind"`
	Template string   `f:"template" label:"Prompt template" hint:"A path relative to the workflow file"`
	Model    string   `f:"model" at:"{agent|agent.kind}.model" needs:"1.26.0" label:"Model" hint:"Overrides the model for this rule"`
	Effort   string   `f:"effort" at:"{agent|agent.kind}.effort" needs:"1.26.0" label:"Effort"`
	Stage    string   `f:"stage" needs:"1.27.0" label:"Stage" hint:"The label that selects this rule. Not allowed together with match"`
	Next     string   `f:"next" needs:"1.27.0" label:"Next" hint:"The rule that runs after this one succeeds"`
}

// Engine versions that add rule fields. A form hides the fields of the
// versions the target engine lacks.
const (
	EngineRuleSettings = "1.26.0"
	EngineStages       = "1.27.0"
)

// Workflow is the part of a workflow file's front matter this editor
// changes. Everything else in the front matter is kept as it is. Rules
// come last so the agent kind is written before the rule settings that
// follow it.
type Workflow struct {
	QueryFilter    string   `f:"tracker.query_filter" label:"Query filter" hint:"The tracker search that picks the issues this workflow handles." group:"Tracker"`
	HandoffState   string   `f:"tracker.handoff_state" label:"Handoff state" hint:"The state an issue moves to when its run succeeds." group:"States"`
	TerminalStates []string `f:"tracker.terminal_states" label:"Terminal states" hint:"Comma separated. Issues in these states are closed." group:"States"`
	AgentKind      string   `f:"agent.kind" label:"Agent kind" hint:"The sortie agent adapter." opts:"pi,claude-code,codex,cursor" group:"Agent"`
	Model          string   `f:"agent.model" at:"{agent.kind}.model" label:"Model" hint:"Set in the block named after the agent kind. Set the kind first." group:"Agent"`
	MaxTurns       *int     `f:"agent.max_turns" label:"Max turns" group:"Agent"`
	MaxConcurrent  *int     `f:"agent.max_concurrent_agents" label:"Max concurrent agents" group:"Agent"`
	PollingMS      *int     `f:"polling.interval_ms" label:"Polling interval" hint:"How often the loop asks the tracker for work, in milliseconds." group:"Polling"`
	RetentionDays  *int     `f:"workspace.retention_days" label:"Workspace retention" hint:"Days a finished workspace is kept." group:"Workspace"`
	Rules          []Rule   `f:"dispatch.rules" label:"Dispatch rules"`
}
