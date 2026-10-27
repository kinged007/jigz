# Jigz: plan for the Jigz-to-engine configuration interface

Status: draft for discussion (2026-10-27)

Plans how Jigz owns all user-facing configuration and generates the engine's
workflow files from it. Background: `plans/jigz/vision-and-roadmap.md`
(sections 4 and 5), `plans/jigz/working-hours-and-pool.md`,
`plans/jigz/production-readiness.md`. No code changes are part of this
document. Section 10 lists the decisions the user has to make before
implementation starts.

## 1. Goal and non-goals

### Goal

A user configures agents, schedules, runtimes, fallbacks and workflows in
Jigz files only. Jigz translates that configuration into the engine's
`WORKFLOW.*.md` files and environment. The user does not edit engine files,
and a new engine release can be adopted by changing Jigz, with automated tests
showing what breaks.

Five capabilities:

1. A working schedule per agent. One agent works Monday to Friday in business
   hours, another works all the time.
2. A runtime per agent (`claude-code`, `pi`).
3. Fallbacks: another account, model or runtime when the first is unusable.
4. Several workflows per repository, each with its own prompts and settings.
5. Automated tests that fail when an engine release changes something Jigz
   depends on.

### Non-goals

- Changing the engine. Every item is built from what the supported engines
  already accept.
- Mid-session handoff between agents. A fallback applies when a session
  starts.
- A new scheduler for issues. Schedules decide when a loop may run; the
  engine still decides which issue it runs.
- Connected mode design. The generator takes a plain Go struct as input so a
  hub config source can fill it later (`vision-and-roadmap.md` section 5.5);
  the hub side is not planned here.
- Changing CI workflows. Section 7.7 lists requests; the user decides.
- Hosting or accounts for the agents' providers. Provider terms stay the
  member's responsibility.

## 2. Current state

### 2.1 Configuration homes

| File | Scope | Read by | Contents |
| --- | --- | --- | --- |
| `<user config dir>/jigz/machine.yaml` | Machine | `internal/machine` (`machine.Load`) | `hours`, `pool`, `limits`, `engine`, `connected`, `dashboard.hide` |
| `<repo>/.jigz/config.yaml` | Repository | `internal/config` (`config.Load`, strict keys) | `repo`, `token`, `milestone`, `assignee`, `filters`, `labels`, `cleanup`, `sleep`, `workflows.<name>.budget`, `github` |
| `<repo>/.jigz/workflows/WORKFLOW.<name>.md` and `prompts/*.md` | Repository | the engine | engine front matter, dispatch rules, prompts |
| `jigz.yaml` | Fleet | `internal/fleetconfig` (`fleetconfig.Load`) | `repos`, per-repo `workflows`, `agent` knobs, `workflow_overrides` |

`fleetconfig.Load` has no caller outside tests. `internal/plan` (`Setup`,
`Validate`, `Apply`, `ApplySettings`) is also used only by its tests. The
dashboard uses `fleetconfig.Resolved` as a data type only
(`cmd/jig/projects.go`, `cmd/jig/workflows.go`).

### 2.2 What the shipped workflows contain

`cmd/jig/workflows/WORKFLOW.issues.md` and `WORKFLOW.prs.md` are complete
engine files: tracker kind and filters, `active_states`, handoff and terminal
states, `db_path` (`jigz-issues.db`, `jigz-prs.db`), `workspace.root`,
`hooks` (clone, branch, push, rebase), an `agent:` block (kind
`claude-code`, command `claude`, `max_turns: 10`, timeouts), a `pi:` block
(`model: "sonnet"`), a `claude-code:` block (`model: "claude-haiku-5-5"`),
and `dispatch.rules` that point at `./prompts/*.md`. The two files differ
only in tracker kind, filter, labels, hooks, rules and names. Prompts are in
`cmd/jig/workflows/prompts/` (`plan`, `build`, `docs`, `review`,
`review-fix`, `merge`).

The loop name is the file stem (`listWorkflows` in `cmd/jig/validate.go`).
The loop type comes from `tracker.kind` (`loopTypeOf` in `cmd/jig/status.go`).
The query filter is overridden at launch by `SORTIE_TRACKER_QUERY_FILTER`
from `config.FilterFor` (`cmd/jig/supervise.go` `childEnv`); known loop names
`issues` and `prs` have default filters, any other name gets
`-label:needs-human` only (`internal/config/config.go` `defaultFilters`).

### 2.3 Installation and ownership

`syncWorkflows` (`cmd/jig/embed.go`) writes a missing file and never
changes an existing one, because installed files are hand-edited. Files
carry no version stamp, so an upgraded binary does not update them
(README, "Known limitations"). `legacyWorkflows` blocks the consolidated
files while the old per-role files exist.

### 2.4 Hours, pool and runtime

- `hours:` in `machine.yaml` is one machine-wide gate
  (`internal/machine/hours.go` `Hours.Open`, `Hours.Next`).
- `gateEval` and `gateDecide` (`cmd/jig/gate.go`) compute one `gateView` for
  the machine and apply it to every loop. There is no per-loop or
  per-workflow schedule. The only per-entry hours are the pool entries'
  `hours`, which decide whether an account is usable
  (`internal/machine/pool.go` `Available`).
- The runtime is one value for all loops: `ActiveRuntime` is the runtime of
  the first available pool entry. `agentEnv` sets `SORTIE_AGENT_KIND` and
  `SORTIE_AGENT_COMMAND=<jig> agent-exec <runtime>` when the pool is not
  empty. A change of runtime recycles each loop when it has no running
  session (`gateRecycleIdle`).
- Without a pool, the runtime is `agent.kind` in the workflow file.
- Model: a literal `model:` line in the runtime block of each workflow file.

### 2.5 Fallbacks

Only account failover exists. `agent-exec` (`cmd/jig/agentexec.go`) picks the
first available pool entry of the runtime, detects usage-limit text in the
CLI output, and marks the entry exhausted until the reset time. The engine's
retry then picks the next entry. When no entry of the active runtime is
available, `ActiveRuntime` moves to the next runtime in pool order and loops
are recycled. There is no model fallback. The README states there is no
fallback agent when the engine lacks an agent kind
(`internal/engine/gate.go` `Gate.Agent`).

### 2.6 Engine coupling today

| Area | Where | Notes |
| --- | --- | --- |
| Pin | `internal/engine/engine.go` `DefaultRepo`, `DefaultTag` | `kinged007/sortie` `v1.28.1`; `machine.yaml` `engine:` or `JIG_ENGINE_*` selects another |
| Capability probe | `internal/engine/probe.go`, `gate.go` | version from `--version`; tracker and agent kinds read from `registered: [...]` in the `validate` errors for an unknown kind. `MinVersion` 1.27.0 |
| Database schema | `internal/rollup/schema.go` | `OldestSchema` 18, `EngineReleases` {1.28.1: 38, 1.29.0: 43}, `requiredColumns` for six tables |
| Live sessions | `cmd/jig/livelog.go` | parses engine log lines by `msg`: `issue dispatched`, `retried issue dispatched`, `turn completed`, `worker exiting`, `worker run failed, scheduling retry`, `tick completed`; fields `issue_id`, `attempt`, `running` |
| CLI | `cmd/jig/unite.go` (argv `--env-file <file> --port 0 <workflow>`), `validate --format json` | |
| Acceptance | `cmd/jig/acceptance_test.go` (build tag `acceptance`) | `TestAcceptance` (pinned fork), `TestAcceptanceUpstreamEngine` (upstream v1.29.0, PR loop disabled), `TestAcceptanceConnected`, plus `TestAcceptanceRunningSession` |

Nothing tests the content of generated workflow files across engine
releases, the engine database layout beyond `InspectSchema` on databases the
acceptance run produces, or the log-line text per release.

### 2.7 Engine surface observed in this session

Checked by running the cached binaries
(`~/.local/share/jigz/sortie-kinged007+sortie-1.28.1`,
`sortie-sortie-ai+sortie-1.29.0`):

| Fact | v1.28.1 (fork) | v1.29.0 (upstream) |
| --- | --- | --- |
| `--version` | `sortie 1.28.1 (commit e586c71)` | `sortie 1.29.0 (commit c899197)` |
| Registered tracker kinds | file, gitea, github, github-pr, gitlab, jira, linear | file, gitea, github, gitlab, jira, linear |
| Registered agent kinds | agent-client-protocol, claude-code, codex, copilot-cli, mock, opencode, pi | agent-client-protocol, claude-code, codex, copilot-cli, mock, opencode |
| `--help` run options | `--dry-run`, `--env-file`, `--log-level`, `--log-format`, `--host`, `--port` (0 disables) | same `--env-file` and `--port` lines |
| Shipped `WORKFLOW.issues.md` validates | yes, one warning for the unused `pi:` block | yes, same warning |
| Unknown top-level key | warning (`unknown_key`) | warning (`unknown_key`) |
| `agent.kind: pi` | accepted | error `agent_adapter` |

Consequences for the generator:

- Neither engine has a front matter key for schedules, so the engine cannot
  enforce per-agent hours. The key would be ignored with a warning.
- Only the block named by `agent.kind` is read; the other runtime block is
  reported as an unknown key. The generator should emit only the block for
  the runtime in use, which also removes the `dropRuntimeBlockWarnings`
  workaround in `cmd/jig/validate.go`.
- The set of `SORTIE_*` override variables differs between releases. A
  string extraction from the two binaries (not a documented list) suggests
  that v1.29.0 has cost-cap variables (`SORTIE_AGENT_MAX_COST_USD`, daily and
  monthly variants), `SORTIE_AGENT_MAX_TOKENS` and a token warning percent,
  and that v1.28.1 has `SORTIE_AGENT_MAX_SESSIONS` and
  `SORTIE_PI_MCP_SERVERS`. This is unverified. Each release's accepted keys
  must be confirmed before the generator emits them (section 7.1).

### 2.8 Discrepancy to fix: `JIG_MODEL`

README (environment variable table, near line 946) states that `jig` sets
`JIG_MODEL` and `JIG_MODEL_<WORKFLOW>` from the `model` setting of a
repository in `jigz.yaml`. The code does not do this. `fleetconfig.Resolved.Env`
and `ModelVar` produce those variables, but no code path outside tests loads
`jigz.yaml` or calls them. The shipped workflows hold literal model values and
do not reference `$JIG_MODEL`. The README text describes a feature that does
not exist. This is a bug in the documentation; section 8 phase 0 fixes it.

### 2.9 Other groundwork

- `internal/config` already has per-workflow settings under
  `workflows.<name>` (budget), per-workflow `filters`, and `labels`.
- `plan.ApplySettings` patches knob lines and the model line in workflow
  front matter by line edits, to keep comments. It also prunes its own
  model reference when a model is removed. It is unused outside tests.

## 3. Target configuration model

### 3.1 Concepts

- **Agent**: a named profile of runtime, model, schedule and fallback chain.
  An agent drives one or more workflows.
- **Workflow**: one loop, one engine process, one `WORKFLOW.<name>.md`, one
  database, one workspace root. A workflow has one agent.
- **Account**: a pool entry in `machine.yaml`. Accounts hold credentials and
  stay machine-local.

An agent is bound at workflow level, not at dispatch-rule level. The engine
reads one adapter kind and one model block per process, so two stages that need
different agents must be two workflows chained by labels (for example
`planner` triggered by `agent:plan` and `builder` triggered by `agent:build`).
The shipped `plan-build` chain (`next: build-chain`) runs in one process and
therefore with one agent.

### 3.2 Where each concern lives

| Concern | Home | Reason |
| --- | --- | --- |
| Agent profiles (runtime, model, effort, schedule, fallback) | `.jigz/config.yaml`, key `agents` | Repository policy, can be tracked in git (setup already supports selective tracking with `token: ""`), contains no secrets, and the hub supplies project config in connected mode. `config.Load` already re-reads it at each child start |
| Workflows (kind, agent, triggers, prompts, filters, budget, polling, turns) | `.jigz/config.yaml`, key `workflows` | `workflows.<name>.budget` already exists there, so the map is extended rather than added |
| Accounts, credentials, usage-limit patterns | `machine.yaml` `pool`, `limits` | Secrets and logins belong to the person on the machine (`vision-and-roadmap.md` principle 3) |
| Machine-wide hours | `machine.yaml` `hours` | Stays the outer ceiling on every agent (section 5.2) |
| Engine repository and version | `machine.yaml` `engine` | Unchanged |
| Prompts | `.jigz/workflows/prompts/*.md` | User-owned files, referenced by path from the workflow config |
| Fleet file `jigz.yaml` | none | Not used by any command; see decision 7 |

Strict keys in `internal/config` stay: unknown keys are errors, with the
field path in the message.

### 3.3 Reference shape

```yaml
# .jigz/config.yaml
agents:
  <name>:
    runtime: claude-code        # claude-code | pi
    model: claude-sonnet-5-5    # passed to the runtime unvalidated
    # effort: not accepted yet, see section 3.7
    schedule:                   # omitted = always open
      timezone: Europe/Madrid   # IANA name; empty = local time
      end: finish               # finish | stop; default finish
      windows:
        - days: [mon, tue, wed, thu, fri]
          start: "09:00"
          end: "18:00"          # an end earlier than start wraps past midnight
    fallback:                   # ordered; tried after the agent's own settings
      - runtime: claude-code
        model: claude-haiku-5-5
      - runtime: pi
        model: sonnet
    accounts: []                # optional pool entry names; empty = any entry of the runtime

workflows:
  <name>:
    kind: issues                # issues | prs; selects the shipped skeleton
    agent: <agent name>
    triggers: [agent:build]     # labels that start this workflow
    prompts:                    # rule name -> prompt file, relative to .jigz/workflows
      build: prompts/build.md
    filter: ""                  # extra query filter text, appended to the label clauses
    polling_interval_ms: 60000
    max_turns: 10
    max_concurrent_agents: 1
    budget: { period: monthly, max_tokens: 5000000 }   # existing keys
    managed: true               # false = Jigz never writes this workflow file
```

Schedule syntax reuses `machine.Hours` and `machine.Window`
(`internal/machine/config.go`) so there is one parser and one validator.
Rules from `working-hours-and-pool.md` apply unchanged: days `mon` to `sun`,
a wrapping window belongs to the day it starts, `start` equal to `end` is
invalid.

Defaults are not restated in Jigz (`fleetconfig` follows the same rule): an
unset knob is left out of the generated file and the engine applies its own
default.

### 3.4 Example 1: business-hours reviewer and 24/7 builder

```yaml
# .jigz/config.yaml
agents:
  reviewer:
    runtime: claude-code
    model: claude-sonnet-5-5
    schedule:
      timezone: Europe/Madrid
      end: finish
      windows:
        - { days: [mon, tue, wed, thu, fri], start: "09:00", end: "18:00" }
  builder:
    runtime: claude-code
    model: claude-haiku-5-5

workflows:
  issues:
    kind: issues
    agent: builder
  prs:
    kind: prs
    agent: reviewer
```

The `prs` loop claims nothing outside Monday to Friday 09:00 to 18:00
(Europe/Madrid) and stops once its last session ends. The `issues` loop runs
all the time. `machine.yaml` may add a machine-wide `hours` block; the
reviewer then works only when both are open.

### 3.5 Example 2: two runtimes with a fallback chain

```yaml
# .jigz/config.yaml
agents:
  builder:
    runtime: claude-code
    model: claude-sonnet-5-5
    fallback:
      - { runtime: claude-code, model: claude-haiku-5-5 }
      - { runtime: pi, model: sonnet }
  reviewer:
    runtime: pi
    model: sonnet
    accounts: [pi-api]

workflows:
  issues: { kind: issues, agent: builder }
  prs:    { kind: prs,    agent: reviewer }
```

```yaml
# machine.yaml
pool:
  - { name: claude-work, runtime: claude-code, env: { CLAUDE_CONFIG_DIR: ~/.claude-work } }
  - { name: claude-spare, runtime: claude-code, env: { CLAUDE_CONFIG_DIR: ~/.claude-spare } }
  - { name: pi-api, runtime: pi, env: { ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY}" } }
```

The builder uses `claude-sonnet-5-5` on `claude-work`, then on `claude-spare`
when the first account is exhausted (existing pool behaviour), then the
cheaper model on either account, then `pi`. The reviewer uses only `pi-api`.
The `prs` workflow needs the `github-pr` tracker; on the upstream engine it
is disabled with the existing reason text, whatever the agent is.

### 3.6 Example 3: a second issue workflow with its own prompt

```yaml
agents:
  builder:  { runtime: claude-code, model: claude-sonnet-5-5 }
  night-docs:
    runtime: claude-code
    model: claude-haiku-5-5
    schedule:
      timezone: UTC
      windows: [ { days: [mon, tue, wed, thu, fri, sat, sun], start: "22:00", end: "06:00" } ]

workflows:
  issues:
    kind: issues
    agent: builder
    triggers: [agent:plan, agent:plan-build, agent:build]
  docs-night:
    kind: issues
    agent: night-docs
    triggers: [agent:docs]
    prompts:
      docs: prompts/docs-night.md
```

`docs-night` generates `WORKFLOW.docs-night.md` with its own database
(`jigz-docs-night.db`) and workspace root. `issues` no longer lists
`agent:docs`. Validation rejects two workflows of the same kind in one
repository that share a trigger label (section 4.5).

### 3.7 Unknowns in the model

- `effort`: no engine front matter key and no Jigz mechanism is known. The
  `claude` CLI may accept an effort option, and the `pi` CLI may not. The key
  is not accepted until a runtime-specific delivery mechanism is verified;
  until then the validator rejects it. Decision 10.
- How the engine's `claude-code` adapter passes `model:` to `claude`
  (README says `claude --model`, unvalidated). A model change applies at loop
  start; whether the engine reloads workflow files on change is not verified,
  so a change is applied by recycling the loop (section 6.3).

## 4. Generation pipeline

### 4.1 Overview

```
inputs: .jigz/config.yaml, machine.yaml (runtime and account checks),
        embedded skeletons, engine profile
  -> resolve: one Resolved struct per workflow
  -> render:  one .jigz/workflows/WORKFLOW.<name>.md per workflow
  -> check:   sortie validate on the rendered text, then atomic replace
```

Rendering is `text/template` over embedded skeletons, one per `kind`
(`issues`, `prs`), taken from today's shipped files. The result is parsed as
YAML in the test and in `jig validate`, and passed to `sortie validate`
before it replaces a file. Line patching (`plan.ApplySettings`) is not used:
a template makes golden tests exact, and comments in generated files belong to
the skeleton.

### 4.2 Generated and user-owned files

| File | Owner | Written by |
| --- | --- | --- |
| `.jigz/config.yaml` | User | user, `jig setup` for the first copy only |
| `.jigz/workflows/WORKFLOW.<name>.md` | Jigz, when it has the marker | generator |
| `.jigz/workflows/WORKFLOW.<name>.md` without marker | User | nobody |
| `.jigz/workflows/prompts/*.md` | User | `jig setup` writes a missing shipped prompt, never changes an existing one |
| Env file, `SORTIE_*` environment | Jigz | `writeEnvFile`, `childEnv` (unchanged) |

### 4.3 Ownership marker and drift

The first line inside the front matter of a generated file is a YAML
comment:

```
---
# jig:generated v1 skeleton=issues engine-profile=1.28.1 sha256=<hex>
```

`sha256` is the digest of the file with the marker line removed. Rules:

- Marker present and digest matches: the file is as generated. Setup and
  reload may replace it.
- Marker present and digest differs: the file was edited by hand (drift).
  Jigz does not overwrite it. `jig validate` reports an error naming the
  file, with the diff against the generator output; `jig setup --force
  <name>` overwrites it and keeps a `.bak` copy.
- No marker: the file is user-owned. Jigz reads its `tracker.kind` for the
  loop type as it does now and never writes it.
- Files are written to a temporary name in the same directory and renamed, so
  a loop never reads a partial file.

### 4.4 Commands

- `jig setup`: creates `.jigz/config.yaml` when missing (as now), generates
  the workflow files, writes missing prompts, syncs labels (labels become the
  union of the built-in list and every workflow's triggers).
- `jig validate`: adds checks to the current set (`cmd/jig/validate.go`):
  config keys and cross references (agent exists, workflow names are valid
  file stems, triggers do not overlap, accounts exist in the pool), a dry
  generation compared with the files on disk (drift, outdated), the engine
  profile check, then the existing `sortie validate` of every file. Output
  keeps the current JSON shape and adds check names.
- `jig reload` (`cmd/jig/reload.go`): the supervisor receives the reload
  signal, regenerates, validates the result with the engine, and replaces the
  files only when every file validates. A failure keeps the running loops and
  the old files and is shown as an alert. Changed loops are recycled when they
  have no running session (the mechanism of `gateRecycleIdle`).
- `jig start`: runs the same generation before it starts loops; a failure
  stops the start with the error, as a config error does now.

### 4.5 Validation rules for several workflows

- Workflow names are file stems: letters, digits and `-`; `issues` and `prs`
  keep their shipped meaning when they have no `kind`.
- Two workflows of the same `kind` must not share a trigger label.
  Otherwise both loops claim the same item. The generator derives
  `query_filter` and `active_states` from `triggers` plus `in-progress`, the
  same shape as `defaultFilters` in `internal/config/config.go`, so
  `FilterFor` gets the filter from the resolved workflow rather than the
  hard-coded map.
- `db_path` is `jigz-<name>.db`, `workspace.root` is
  `$JIG_WORKSPACES/<name>`. Both are derived from the name.
- A `prs` workflow is allowed only when the engine profile lists the
  `github-pr` tracker; otherwise the workflow is `disabled` with the
  existing reason (no error).

### 4.6 Migration of existing installs

Installed workflow files have no marker, so they are user-owned and keep
working. To move them to generated files:

1. A table of the digests of every shipped file version (`WORKFLOW.issues.md`,
   `WORKFLOW.prs.md`, from git history) is compiled into the binary. A file
   that equals a known shipped version is adopted automatically at the next
   `jig setup`: it is regenerated from the default config and receives the
   marker.
2. A file that differs is not changed. `jig setup` prints the diff between
   it and the generator output for the default config, and the list of
   settings that `jig migrate <name>` could take over (model, `max_turns`,
   polling interval, timeouts). `jig migrate <name>` writes those values to
   `.jigz/config.yaml` and regenerates; whatever cannot be expressed (custom
   hooks, custom dispatch rules) stops the migration and the file stays
   user-owned with `managed: false`.
3. `managed: false` is the permanent escape hatch. It is the documented way
   to run a hand-written workflow, subject to decision 2.

### 4.7 Engine version differences

An engine profile is compiled data, one entry per supported release:

```
type Profile struct {
    Repo, Version string
    Trackers, Agents []string
    Schema int                // database schema at the end of migrations
    EnvOverrides map[string]bool   // SORTIE_* names the engine accepts
    FrontMatter  map[string]bool   // block.key pairs the generator may emit
}
```

`rollup.EngineReleases` and `engine.MinVersion` are folded into this table.
The profile of the running engine comes from the capability probe (version
line) matched to the table.

- Required feature missing (tracker kind or agent kind): the workflow is
  disabled with the existing reason text (`Gate.TrackerOff`, `Gate.Agent`).
- Optional key missing: if the user set it, validation fails with a message
  naming the key and the engine; if only the skeleton uses it, the generator
  omits it.
- Engine version not in the table and newer than the newest entry: the
  newest profile is used, and `validate` and the dashboard show a warning
  that the release has no contract tests.
- Engine version older than `MinVersion`: no loop starts (unchanged).

## 5. Scheduling design

### 5.1 Where it is enforced

The engine has no schedule key (section 2.7), so enforcement is in the
supervisor, per loop, in `gateDecide`. The supervisor already stops idle loops
and starts held loops on each 2-second tick (`gateStep`), recycles loops on a
runtime change, and reports a `hold` reason per workflow in the status
document. A per-loop gate adds one input to that path.

Changes:

- `gateLoop` gains the loop's agent schedule (`machine.Hours`) and the agent's
  fallback chain (section 6).
- `gateEval` takes the loop's inputs: `Open = machineHoursOpen &&
  scheduleOpen && accountAvailable(chain)`. A new reason `gateSchedule` is
  reported when only the schedule is closed.
- `gateDecide` no longer computes one view for all loops; it computes one view
  per loop (cheap: pure functions of config, pool state and time) and keeps
  the action table as is.
- `startable` and `agentEnv` use the per-loop view, so a loop held by its
  schedule is not started by startup, wake or restart.
- The status document `gate` object gains per-workflow `schedule_open`,
  `next_change` and `hold`. The machine-level `gate` object stays for
  compatibility.

### 5.2 Interaction with machine-wide hours

A loop is open only when the machine window and the agent schedule are both
open. The agent schedule cannot extend work past the machine window.

End behaviour:

- The reason that closed the gate decides the behaviour. If the machine
  window closed, `machine.yaml` `hours.end` applies. If only the agent
  schedule closed, `agents.<name>.schedule.end` applies. If both closed, the
  machine setting applies.
- `finish`: the loop is stopped when it has no running session
  (`gateStopIdle`). `stop`: the loop is stopped at once (`gateStopNow`); the
  issue keeps its in-progress label and is dispatched again at the next window.

### 5.3 Interaction with the other controls

| Control | Interaction |
| --- | --- |
| Pool account hours | Unchanged. An account is usable when it is not exhausted and its own hours are open. A loop needs an open schedule and a usable account in its chain |
| Pool exhaustion | `gatePool` stays a separate reason. A schedule closed and a pool exhausted show the schedule first |
| Idle sleep | A held loop has no session and is not busy. When every loop is idle or held the sleep rules stop the children as now. At wake, `startable` evaluates each loop's view, so a loop outside its schedule stays held |
| Budget pause | `gateLoop.paused` keeps its position: a paused loop is never started by the gate. A closed schedule still stops an up loop |
| Manual pause (`isPaused`) | Same as budget pause |
| Wake time | `NextChange` per loop is the earliest of the machine window change, the schedule change and the pool change; it is reported per workflow |
| `max_concurrent_agents` above 1 | The known limitation stays: a loop can claim new work between the window close and the stop |

### 5.4 Edge cases to test

- A window that wraps past midnight and crosses a daylight saving change
  (`Hours.Next` iterates days with `time.Date`, which resolves skipped hours).
- A schedule in a different timezone than the machine window.
- Config reload that removes a schedule while the loop is held: the loop
  starts at the next tick.
- A schedule that closes while a retry is pending: the engine persists
  retries, so the loop resumes after the next window (existing rule for
  `hours.end: finish`).

## 6. Fallback design

### 6.1 Terms

A **step** is `{runtime, model, account set}`. An agent has an ordered list of
steps: the agent's own settings first, then `fallback` entries. Within a
step, account selection stays in `agent-exec` (first available entry of the
runtime that is in the step's `accounts` list or, when the list is empty, any
entry of the runtime).

### 6.2 Triggers

| Trigger | Detection | Scope | In first release |
| --- | --- | --- | --- |
| Usage limit | `agent-exec` output matching (existing `limits` patterns) | Account, with reset time | Yes (exists) |
| All accounts of the step's runtime unavailable | `machine.Config.Pick` returns none | Step | Yes |
| Runtime not registered in the engine | `Gate.Agent` / `RuntimeOff` | Step, permanent | Yes (skip at plan time) |
| Model unavailable | CLI error text such as "model not found" | Step, with cooldown | Only after the message formats of both CLIs are captured as fixtures (unknown today) |
| Repeated agent failure | N consecutive `failed_agent` runs of the loop | Step, cooldown | No; most failures come from the task or the prompt, not the agent. Decision 4 |

### 6.3 Chain semantics

- The supervisor computes each loop's current step: the first step that is
  usable (runtime registered, an account available, not in cooldown). It
  returns to a higher step as soon as that step is usable again.
- A change of step recycles the loop once it has no running session. This is
  the existing runtime-change mechanism generalised from "one runtime for all
  loops" to "one step per loop". It is needed because the engine reads
  `agent.kind` and the model at process start.
- The step's runtime and model reach the engine through the loop
  environment: `SORTIE_AGENT_KIND`, `SORTIE_AGENT_COMMAND=<jig> agent-exec
  <runtime>` (as now) and a model variable, `JIG_MODEL`, that the generated
  workflow references as `model: $JIG_MODEL` inside the block of the runtime
  in use. This makes the README claim about `JIG_MODEL` true for the generated
  files (section 2.8) and keeps the model out of the file text, so a model
  fallback does not rewrite files.
- A session that fails on a usage limit is retried by the engine and
  `agent-exec` picks the next account of the current step (unchanged). When
  the step has no account left, the supervisor moves to the next step when the
  loop is idle; the retry that follows runs in the new step.
- No usable step: the gate reason is `gatePool` for the loop; nothing starts.

### 6.4 State

- Account exhaustion stays in `pool.json` (`exhausted_until`, `reason`,
  `last_used`), written under `flock`.
- Step cooldown (for the model trigger, when added) is a new top-level map
  `steps` in the same file keyed by `<runtime>/<model>` with `unhealthy_until`
  and `reason`, with the same locking. Entries with no matching config are
  ignored, as for accounts.
- The current step per loop is computed, not stored. After a restart the same
  inputs give the same answer, as for the gate today.

### 6.5 Observability

- Status document per workflow: `agent`, `step` (index and `runtime/model`),
  `primary` (bool), and `fallback_reason` (text of the trigger and the time
  the primary becomes usable again).
- Dashboard Agents page: each agent with its chain, the current step per
  workflow, cooldowns and reset times; the Operations page raises one alert
  while a workflow runs on a non-primary step.
- Runs already record the adapter and model (`run_history.agent_adapter`,
  `session_metadata.model_name`), so the Runs pages show which step ran a
  session. A test checks that a run on a fallback step shows its own model.
- Log line on every step change, in the form of the gate lines in
  `gate.go` (`jig: agent "builder" moved to step 2 (claude-code/claude-haiku-5-5): ...`).

## 7. Engine-compatibility test strategy

### 7.1 Supported engine matrix

One table in code (the profile list of section 4.7) is the single list of
supported releases. Every test below iterates over it, so adding a release
adds it to all tests. Initial entries: `kinged007/sortie` v1.28.1 (default)
and `sortie-ai/sortie` v1.29.0. Tests that need a binary use the engine
resolution already in `internal/engine` (`JIG_ENGINE_BIN`, share directory,
download) and are in two groups:

- Fixture tests: no engine binary, run in `go test ./...`.
- Binary tests: build tag `contract`, run each supported engine binary
  (about 30 seconds in total; `validate`, `--help`, `--dry-run`, no agent
  runs).

### 7.2 Golden files of generated workflows

- `internal/gen/testdata/golden/<profile>/<case>.md`, one set per supported
  release, for these cases: default `issues`, default `prs`, a schedule only,
  a `pi` agent, a model override, two workflows in one repository, a custom
  prompt reference, a disabled `prs` on the upstream profile.
- The test renders each case with the profile and compares bytes. `go test
  -update` rewrites the files; a reviewer reads the diff.
- A `contract` test runs `sortie validate --format json` of every golden file
  with the matching engine and requires `valid: true`, and requires no
  `unknown_key` warning (the unused-block warning of section 2.7 disappears
  because only one runtime block is emitted).
- A negative golden set: configs that must fail validation (unknown key,
  trigger overlap, agent not defined, `prs` with no `github-pr`) with the
  exact error text.

### 7.3 Database schema contract

- `internal/rollup/testdata/schema/<version>.sql`: the `sqlite_master`
  statements and `schema_migrations` rows from a database created by that
  release, recorded by a script (`make schema-fixture ENGINE=<spec>`) that
  starts the engine with the `mock` agent against `internal/fakegithub`, as
  the acceptance test does, and dumps the schema without data.
- The test builds an empty database from each fixture, inserts one row per
  table Jigz reads using the columns in `requiredColumns`, and runs
  `InspectSchema` and the collector queries; it requires no notice and the
  expected rows.
- A second test checks that a fixture with one required column removed
  produces the `Missing` entry and that only the dependent view degrades.
- A test checks that `EngineReleases` equals the set of fixtures, so a
  release cannot be listed without a fixture.

### 7.4 Log-line contract

- `cmd/jig/testdata/enginelog/<version>.log`: lines recorded from a real run
  of each release in text log format (the format `livelog.go` parses),
  covering one session start, a retry start, a turn, a normal end, a failed
  end with a retry, and a poll tick.
- The test replays each file into `liveTracker` and asserts the sessions,
  attempts and the `unaccounted` handling.
- A `contract` test searches each engine binary for the `msg` literals
  listed in `livelog.go`, which fails quickly when a release renames one. The
  log recorded in the acceptance run is the authoritative check (7.6).
- Message fields (`issue_id`, `attempt`, `running`) are asserted from the
  recorded files only.

### 7.5 CLI and capability contract

- Flags: for each engine, `--help` output contains `--env-file`, `--port`
  and the line "0 to disable"; `sortie --dry-run --port 0 --env-file <f>
  <workflow>` against a golden workflow exits 0 without binding a port.
- Version line: `engine.Probe` parses the `--version` output of each release.
- Registered kinds: `Probe` returns the tracker and agent kinds in the
  matrix for each release (for example the upstream list has no `pi` and no
  `github-pr`). This also pins the `registered: [...]` message format that
  `parseRegistered` depends on.
- `validate --format json`: the shape (`valid`, `errors[]`, `warnings[]`
  with `check` and `message`) is asserted on a deliberately invalid file.

### 7.6 Acceptance matrix

Acceptance stays the end-to-end proof and is not multiplied by the number of
generated-file variants. One scenario set per engine:

| Scenario | Fork v1.28.1 | Upstream v1.29.0 |
| --- | --- | --- |
| Generated issues workflow handed off through the mock agent | yes | yes |
| Generated prs workflow | yes | disabled with reason |
| Per-agent schedule closes while a session runs (`finish`) and then reopens, using a controllable clock where the supervisor allows it | yes | no |
| Fallback: first account exhausted, then step change | yes | no |
| Recorded engine log written to `testdata` on request (`-record`) | yes | yes |

Schedule and fallback behaviour is tested against the supervisor's fake
clock in unit tests (`gate_test.go`, `supervise_test.go`); acceptance covers
only one end-to-end case of each, on the default engine.

### 7.7 CI cost and requests

Acceptance is about 3 minutes by the comment in `.github/workflows/ci.yml`
and about 8 minutes by `AGENTS.md`; the job timeout is 15 minutes. The two
numbers differ and the actual time is not measured here (unknown). The new
tests add:

| Addition | Added time | Where |
| --- | --- | --- |
| Fixture tests (golden, schema, log replay) | seconds | `go test ./...` |
| Binary contract tests | about 30 s plus download time | new job or step |
| Acceptance scenarios for schedule and fallback | about 1 to 2 minutes | existing job |

Requests (any CI workflow change needs user approval; none is made here):

1. Add a `contract` job running `go test -tags contract ./...` in parallel to
   `check` and `acceptance`; it downloads both engines (about 25 MB each, as
   the existing job does) and caches them.
2. Split `acceptance` into two parallel jobs (default engine, upstream
   engine) with `-run` selecting `TestAcceptance$` plus the running-session
   and connected tests on one, `TestAcceptanceUpstreamEngine` on the other,
   so wall time is the longer job, not the sum.
3. Skip `acceptance` when a pull request changes only `plans/`, `*.md` or
   `README.md` (`paths-ignore`), while `check` and `build` stay required.
4. Add a scheduled weekly job that runs the binary contract tests against the
   newest release of each engine repository (non-blocking, opens an issue on
   failure) to learn about a new release before a bump is wanted.

### 7.8 Checklist: adding an engine release

1. Download the release; record `--version`, `--help`, registered kinds.
2. Run `make schema-fixture ENGINE=<repo>@<tag>`; commit the schema fixture
   and the log recording; add the release to `rollup.EngineReleases` data in
   the profile table with its schema number.
3. Confirm the accepted front matter keys and `SORTIE_*` variables from the
   release notes and by `validate` runs; update the profile's `FrontMatter`
   and `EnvOverrides`.
4. Run `go test -update` for the golden set; review each golden diff.
5. Run the `contract` tests and the acceptance scenarios against the release.
6. Update README (engine section and the version table) and the default pin
   if the release becomes the default.
7. Open one pull request containing only the release addition.

## 8. Phased delivery plan

Each phase is one or more small pull requests that ship without the later
ones. Every phase includes its tests and README update.

| Phase | Scope | Acceptance criteria | Tests |
| --- | --- | --- | --- |
| 0 | Documentation fix: correct the README `JIG_MODEL` rows (section 2.8). No behaviour change | README matches the code | Link and text check by review |
| 1 | Per-agent schedules. `agents.<name>.schedule` and `workflows.<name>.agent` in `.jigz/config.yaml`; per-loop gate in `gateDecide`; per-workflow `hold` and `next_change` in the status document; Agents page row for each agent | A loop outside its schedule starts nothing and stops idle; `end: stop` stops at once; machine hours still bound it; budget pause and sleep behave as in section 5.3. No file generation | Table tests in `gate_test.go` (wrap, DST, machine and agent both closed, end precedence, paused loop, held loop restart); config tests for strict keys and error paths; one acceptance case on the default engine |
| 2 | Contract test harness without the generator: engine profile table (folding `rollup.EngineReleases` and `MinVersion`), schema fixtures, log fixtures, flag and probe tests under `contract` tag | Adding a fake release to the table without fixtures fails a test; both supported releases pass | The tests of section 7.3 to 7.5; the CI requests 7.7 are made at this point |
| 3 | Generator for `kind: issues` and `prs` (template, marker, digest, drift check, atomic write), `jig validate` checks, adoption of unmodified shipped files, `managed: false` | The generator output for the default config equals the shipped files except for the marker; golden files pass on both engines; a hand-edited marked file is reported and not overwritten | Golden set (7.2), drift tests, adoption test using the shipped digests, `sortie validate` of each golden file under `contract` |
| 4 | Per-agent runtime and model: generated files reference `$JIG_MODEL` in the runtime block in use, supervisor sets runtime and model per loop (single step, no fallback); only one runtime block is emitted | Two loops run two runtimes; changing the model in config and running `jig reload` recycles the loop when idle | Unit tests for env per loop; acceptance case with two agents on the default engine |
| 5 | Several workflows per repository: `kind`, `triggers`, `prompts`, derived `query_filter`, `db_path`, `workspace.root`; `FilterFor` reads the resolved workflow; label sync from triggers; overlap validation; `jig migrate` | Example 3 runs; two workflows with a shared trigger fail validation; `docs-night` has its own database and workspace | Golden cases (two workflows, custom prompt), validation negatives, status document and Workflows page tests |
| 6 | Fallback chains (limit and no-account triggers): steps, per-loop step selection, recycle on step change, status and dashboard fields, alert while on a fallback step | Example 2 behaves as section 3.5 describes; the primary returns when usable | Supervisor tests on the fake clock and pool state; `agent-exec` tests with the account list; one acceptance case |
| 7 | Optional triggers, after fixtures exist: model unavailable (needs captured CLI messages), repeated failure (if accepted in decision 4) | As decision 4 settles | Fixture tests of the messages |
| 8 | Remove unused code per decision 7 | `go build`, vet and tests pass; no package imports the removed code | Existing suite |

Phase 1 is first because it needs no generation and no engine change: only
the supervisor and the config reader change, and it answers the first
request directly.

## 9. Risks

| Risk | Effect | Mitigation |
| --- | --- | --- |
| Generated files hide an engine option a user needs | Users ask to edit the file | `managed: false`; a `raw` passthrough block is not planned (KISS, decision 2) |
| Two workflows claim the same item | Duplicate work | Trigger overlap validation (4.5); assignee scope stays in the filter |
| Engine renames a log message or a column | Live view or history stops working | Sections 7.3 and 7.4; degraded notices already exist in `rollup` |
| Unverified engine key lists (section 2.7) | Generator emits a key an engine rejects | Profile step 3 of the checklist; `validate` of every golden file on that engine |
| `effort` and model-unavailable detection depend on CLI behaviour that is not captured | Features ship wrong | Not accepted until fixtures exist (3.7, 6.2) |
| Subscription terms for unattended use and for fallbacks between accounts | Account restrictions | Documentation stays; the member is responsible |
| A step change recycles loops often if limits flap | Repeated restarts | Recycle only when idle; cooldown on a step before returning to a higher one |
| Hub config in connected mode needs the same model | Two sources drift | Single resolved struct used by the generator; hub source fills it later |
| CI time | Slower merges | Section 7.7 requests; fixture tests are the bulk of new coverage |
| Shipped prompts change in a new Jigz release but user prompts do not | Users run stale prompts | Prompts are user-owned (4.2); a later `jig prompts diff` is not planned here |
| Large rewrite of an existing config path | Regressions in `childEnv` | Phases 3 to 5 keep `childEnv` output byte-identical for default configs; a test asserts it |

## 10. Open decisions for the user

1. **Config home.** Where do agent profiles and workflow settings live?
   - A: `.jigz/config.yaml` for agents and workflows; `machine.yaml` for
     accounts, machine hours and engine pin (section 3.2).
   - B: everything in `machine.yaml`.
   - C: the fleet file `jigz.yaml`.
   - Recommendation: A. It reuses the strict repository config that the
     supervisor already reloads, keeps secrets out of tracked files, and
     matches how connected mode supplies project config.
2. **Hand-editing generated workflows.**
   - A: a file with the marker is not edited by hand; drift is an error;
     `managed: false` per workflow is the supported opt-out.
   - B: hand edits stay supported and Jigz merges around them.
   - C: no opt-out; all workflows are generated.
   - Recommendation: A. B needs a merge of YAML and comments that cannot be
     tested exhaustively; C blocks custom hooks.
3. **Engine targets.**
   - A: support the fork v1.28.1 and upstream v1.29.0 through one profile
     table, adding a profile per release; keep at most the two newest per
     repository.
   - B: upstream only, after it has the `pi` adapter and `github-pr`.
   - C: fork only.
   - Recommendation: A. It matches the current acceptance tests, and the
     profile table makes B or C a deletion later.
4. **Fallback semantics.**
   - A: triggers are usage limit and no usable account; step change when the
     loop is idle; the primary step returns when usable.
   - B: A plus repeated-failure trigger.
   - C: A plus handing a running session to the next step.
   - Recommendation: A first. Add model-unavailable after fixtures exist.
     B hides prompt and task failures behind agent changes; C needs engine
     support.
5. **Agent granularity.**
   - A: one agent per workflow; different agents per stage mean separate
     workflows chained by labels.
   - B: ask the engine for per-dispatch-rule agents.
   - Recommendation: A. The engine runs one adapter per process.
6. **Schedule and machine hours.**
   - A: intersection; the agent schedule cannot extend the machine window.
   - B: an agent schedule overrides the machine window.
   - Recommendation: A. The machine window is the person's limit for the
     computer.
7. **The unused `jigz.yaml` loader and `internal/plan`.**
   - A: delete `fleetconfig.Load`, `plan.Setup/Validate/Apply`,
     `ApplySettings` and the model-reference pruning in phase 8, keeping
     `fleetconfig.Resolved` only if the dashboard still needs it.
   - B: build the new model on them.
   - C: leave them.
   - Recommendation: A. They target a fleet file nothing reads, patch
     workflow files by line edits that the template generator replaces, and
     keep a README claim alive that is not true (section 2.8).
8. **The `JIG_MODEL` README claim.**
   - A: correct the README now (phase 0); the variable becomes real in
     phase 4 for generated files.
   - B: leave it until phase 4.
   - Recommendation: A.
9. **CI changes (requests, section 7.7).** Approve adding a `contract` job,
   splitting `acceptance` into two parallel jobs, `paths-ignore` for
   documentation-only pull requests, and a weekly canary job against new
   engine releases?
   - Recommendation: approve 1 and 2 with phase 2; 3 and 4 optional.
10. **Effort setting.**
    - A: leave `effort` out until a delivery mechanism per runtime is
      verified.
    - B: accept it now and pass it to `agent-exec` as a CLI argument.
    - Recommendation: A.
