# Jigz: plan for the Jigz-to-engine configuration interface (2026-10-27)

Plans how Jigz owns all user-facing configuration in one repository file and
generates the engine's workflow documents from it. Background:
`vision-and-roadmap.md`, `working-hours-and-pool.md`,
`production-readiness.md`. No code changes are part of this document. Section
10 lists the decisions, section 11 the open points.

## 1. Goal and non-goals

### Goal

A user configures agents, pools, working hours, fallbacks and work in one
file, `<repo>/.jigz/config.yaml`. Jigz generates the engine's workflow
documents (`WORKFLOW.*.md`) from it and supervises the loops against the
agents' availability. A new engine release is adopted by changing the engine
target in the config and running the tests, which show what breaks.

Terms: an **agent** is a named profile of runtime, model, working hours and
credential mapping. A **pool** is a named ordered list of agents (the fallback
order). **Work** is a named unit with a query, agents and dispatch rules. A
**loop** is one engine process running one **engine workflow document**; a work
entry produces one loop or several (section 4.5). `agents` and `pools` are the
fleet.

### Non-goals

- Changing the engine, mid-session handoff between agents (a change applies
  when the loop is idle), or a new issue scheduler: availability decides
  whether a loop runs, the engine decides which item it runs.
- The hub daemon's storage. The generator takes a plain Go struct so a hub
  source can fill it later (`vision-and-roadmap.md` section 5.5).
- Provider accounts and terms, which stay the member's responsibility.

## 2. Current state

### 2.1 Configuration homes

- `<user config dir>/jigz/machine.yaml` (`internal/machine`): `hours`, `pool`,
  `limits`, `engine`, `connected`, `dashboard.hide`.
- `<repo>/.jigz/config.yaml` (`internal/config`, strict keys): `repo`, `token`,
  `milestone`, `assignee`, `filters`, `labels`, `cleanup`, `sleep`,
  `workflows.<name>.budget`, `github`.
- `<repo>/.jigz/workflows/WORKFLOW.<name>.md` and `prompts/*.md`: read by the
  engine.
- `jigz.yaml` (`internal/fleetconfig`): `repos`, per-repo `workflows`, `agent`
  knobs, `workflow_overrides`.
- `<state dir>/jigz/pool.json` (`internal/machine/state.go`): account
  exhaustion, written under `flock`.

`fleetconfig.Load` and `internal/plan` (`Setup`, `Validate`, `Apply`,
`ApplySettings`) have no caller outside tests. The dashboard uses
`fleetconfig.Resolved` as a data type only (`cmd/jig/projects.go`,
`cmd/jig/workflows.go`).

### 2.2 What the shipped workflow documents contain

`cmd/jig/workflows/WORKFLOW.issues.md` and `WORKFLOW.prs.md` are complete
engine files: tracker settings, `db_path` (`jigz-issues.db`, relative to the
workflows directory `.jigz/workflows/`), `workspace.root` (`$JIG_WORKSPACES/<name>`, which is
`.jigz/workspaces/<name>`; `internal/config/config.go` line 428), `hooks`, an
`agent:` block (`claude-code`, `max_turns: 10`, `max_concurrent_agents: 1`), a
`pi:` and a `claude-code:` block with a model, and `dispatch.rules`. The
`plan-build` rule has `next: build-chain`; `build-chain` has
`stage: stage-build`.

The loop name is the file stem (`listWorkflows`, `cmd/jig/validate.go`) and the
type comes from `tracker.kind` (`loopTypeOf`, `cmd/jig/status.go`). The query is
overridden at launch by `SORTIE_TRACKER_QUERY_FILTER` from `config.FilterFor`
(`childEnv`, `cmd/jig/supervise.go`); `issues` and `prs` have default filters
(`defaultFilters`). `dbPathFor` (`cmd/jig/unite.go`) reads `db_path` from the
document, and `internal/loopsrc/discover.go` finds databases by `jigz-*.db`.

### 2.3 Installation and ownership

`syncWorkflows` (`cmd/jig/embed.go`) writes a missing file and never changes an
existing one; files carry no version stamp, so an upgraded binary does not
update them (README, "Known limitations").

### 2.4 Hours, pool, runtime and fallback

- `hours:` in `machine.yaml` is one machine-wide gate
  (`internal/machine/hours.go`). `gateEval` and `gateDecide`
  (`cmd/jig/gate.go`) compute one `gateView` for every loop; there is no
  per-loop schedule. Pool entries have `hours` that decide whether an account
  is usable (`internal/machine/pool.go` `Available`).
- The runtime is one value for all loops (`ActiveRuntime`). `agentEnv` sets
  `SORTIE_AGENT_KIND` and `SORTIE_AGENT_COMMAND=<jig> agent-exec <runtime>`,
  start-time overrides, so a change recycles each loop at idle
  (`gateRecycleIdle`). The model is a literal line in each document.
- Only account failover exists: `agent-exec` (`cmd/jig/agentexec.go`) picks the
  first available entry of the runtime, detects usage-limit text and marks the
  entry exhausted until the reset time; with none left, `ActiveRuntime` moves
  to the next runtime in pool order. There is no model fallback.

### 2.5 Live sessions and drain

`liveTracker` (`cmd/jig/livelog.go`) follows one loop's sessions from engine
log lines, keyed by issue id, so concurrent sessions are represented.
`tick(running)` records the engine's session count and keeps `unaccounted`.
`supervisor.noSession` is true when the loop has settled (`drainSettle`), the
tracker accounts for every session and none is active; `gateStopIdle` and
`gateRecycleIdle` use it. With `max_concurrent_agents` above 1 a draining loop
can claim new items until idle (README, known limitation).

### 2.6 Engine coupling today

Pin in `internal/engine/engine.go` (fork `kinged007/sortie` `v1.28.1`,
`MinVersion` 1.27.0); probe in `internal/engine/probe.go`; schema in
`internal/rollup/schema.go` (`OldestSchema` 18, `EngineReleases` {1.28.1: 38,
1.29.0: 43}, `requiredColumns`); log lines parsed by `cmd/jig/livelog.go`
(`issue dispatched`, `retried issue dispatched`, `turn completed`, `worker
exiting`, `worker run failed, scheduling retry`, `tick completed`); CLI use in
`cmd/jig/unite.go`; acceptance in `cmd/jig/acceptance_test.go`. Nothing tests
generated documents across releases, the database layout beyond
`InspectSchema` on acceptance databases, or log text per release.

### 2.7 Engine surface checked in this session

Checked by running the cached binaries (`sortie-kinged007+sortie-1.28.1`,
`sortie-sortie-ai+sortie-1.29.0` in `~/.local/share/jigz/`) and by extracting
strings from them.

| Fact | v1.28.1 (fork) | v1.29.0 (upstream) |
| --- | --- | --- |
| Tracker kinds | file, gitea, github, github-pr, gitlab, jira, linear | same without github-pr |
| Agent kinds | agent-client-protocol, claude-code, codex, copilot-cli, mock, opencode, pi | same without pi |
| `agent.kind: pi` | accepted | error `agent_adapter` |
| Rule key `agent: <kind>` (string) | accepted | accepted; `pi` gives `unknown agent kind` |
| Rule `agent:` as list or mapping | error `expected string` | same |
| Rule keys `model`, `effort` | error `unknown key` | same |
| `claude-code.effort` in the runtime block | accepted without warning | same |

Strings of v1.28.1 (not documented behaviour): `orchestrator.RuleAgent` shows
an agent resolved per rule; `run_history`, `retry_entries` and `parked_issues`
carry `rule_name` and `agent_kind`; `workflow.(*Manager).watch` and `.Reload`
exist, consistent with the engine re-reading the document while it runs.

Consequences for the generator:

- No engine front matter key holds working hours, so Jigz enforces them
  (section 5).
- The engine accepts an agent kind per rule inside one loop. Model, effort and
  command are not per rule: the model is in the runtime block of the kind
  (`claude-code:`, `pi:`). Section 4.5 states what follows.
- Only runtime blocks in use are read; others are reported as unknown keys.
  The generator emits only the blocks in use, which removes the
  `dropRuntimeBlockWarnings` workaround in `cmd/jig/validate.go`.
- The `SORTIE_*` override set differs between releases (strings suggest cost
  variables in v1.29.0 and `SORTIE_AGENT_MAX_SESSIONS` in v1.28.1; unverified).
  Each release's keys are confirmed before the generator emits them
  (section 7.8).
- Settled by probing (`engine-interface-notes.md`, "Engine findings"): `$VAR`
  is not expanded in `agent.command`, and shell quoting in `agent.command` is
  not passed through intact.
- Settled by runtime fixtures (`engine-interface-notes.md`, "Runtime fixture
  findings"): `command` in a runtime block validates silently but is never
  executed, so `agent.command` is the only lever; `claude-code.effort` reaches
  the CLI as `--effort <value>`. Nothing is left open here.

### 2.8 Discrepancy: `JIG_MODEL`

README (near line 946) states that `jig` sets `JIG_MODEL` and
`JIG_MODEL_<WORKFLOW>` from the `model` setting in `jigz.yaml`. No code path
outside tests loads `jigz.yaml` or calls `fleetconfig.Resolved.Env` and
`ModelVar`, and documents hold literal models, as generated ones will
(section 10, item 12).

## 3. Target configuration model

### 3.1 Location

All configuration, engine workflow documents and prompts are stored in
`<repo>/.jigz/`. Nothing is stored under the user's home configuration
directory. The hub daemon may differ; that is out of scope.

- `.jigz/config.yaml`: the single configuration file (`jig setup` writes the
  first copy only).
- `.jigz/workflows/WORKFLOW.<loop>.md`: documents, owned by Jigz when
  `managed: true` and by the user when `managed: false`.
- `.jigz/workflows/prompts/*.md`: prompts, owned by the user.
- `.jigz/data/` (databases, state), `.jigz/workspaces/<loop>/` and
  `.jigz/.env.loop`.

`machine.yaml` settings move into the file: `hours` becomes `operating_hours`,
`pool` becomes `agents` and `pools`, and `dashboard.hide`, `limits` and
`connected` become keys of the same file. A united dashboard (`--unite`) uses
the file of the repository where the first united dashboard is spawned
(section 10, item 10).

### 3.2 Sections of `.jigz/config.yaml`

One file with top-level sections. The repository section is `repo`, `token`,
`github`, `filters`, `labels`, `cleanup`, `sleep` (existing keys,
`internal/config`) plus `engine`, `operating_hours`, `tracker` and `hooks`.
The fleet sections are `agents` and `pools`; the work section is `work`.
`milestone` and `assignee` are removed: the assignee scope is always `@me`.

- `engine`: `repo` (default `kinged007/sortie`), `version` (default `v1.28.1`).
  One engine is supported until upstream supports `pi` and `github-pr`.
- `operating_hours`: `timezone`, `windows` (syntax of agent hours), `end`
  (`finish` or `stop`). Omitted means 24/7.
- `tracker`: defaults for every work entry: `kind`, `api_key` and `project`
  (references, section 3.3), `active_states`, `in_progress_state`,
  `handoff_state`. `hooks`: defaults `after_create`, `before_run`, `after_run`,
  `timeout_ms`.

An unknown key is an error with the field path. An unset engine knob is left
out of the generated document and the engine applies its default.

### 3.3 Fleet: agents and pools

```yaml
agents:
  <name>:
    runtime: claude-code         # claude-code | pi
    model: claude-sonnet-5-5     # passed to the runtime unvalidated
    schedule:                    # omitted = always available
      timezone: Europe/Madrid    # IANA name; empty = local time
      end: finish                # finish | stop; default finish
      windows:
        - days: [mon, tue, wed, thu, fri]
          start: "09:00"
          end: "18:00"           # an end earlier than start wraps past midnight
    env:                         # environment of the agent process
      CLAUDE_CONFIG_DIR: ~/.claude-work
      ANTHROPIC_OAUTH_TOKEN: { from_env: CLAUDE_SUB_1 }

pools:
  <name>: [<agent name>, <agent name>]   # order = fallback order
```

- `effort` is not accepted (section 10, item 2).
- Secrets are never stored in the file. A credential is a reference to an
  environment variable of the supervising process, `{ from_env: NAME }`. The
  validator rejects a plain string for a key ending in `TOKEN`, `KEY`, `SECRET`
  or `PASSWORD`. An unset variable makes the agent unavailable, with the
  reason shown. Two agents with one runtime and different token references
  switch subscriptions.
- Hours reuse `machine.Hours` and `machine.Window`: days `mon` to `sun`, a
  wrapping window belongs to the day it starts, `start` equal to `end` is
  invalid (`working-hours-and-pool.md`).
- Agents and pools share one name space.

### 3.4 Work

```yaml
work:
  <slug>:
    managed: true                 # false = Jigz never writes this document
    query_filter: "..."           # the complete tracker query
    tracker: { ... }              # optional overrides of the tracker defaults
    hooks:   { ... }              # optional overrides of the default hooks
    max_concurrent_agents: 1      # also max_turns, polling_interval_ms
    budget: { period: monthly, max_tokens: 5000000 }
    agent: <agent or pool name>   # default for rules without their own agent
    rules:
      - name: plan
        match: { labels: [agent:plan] }
        prompt: prompts/plan.md
        agent: <name, a list of names, or { use: <name>, model: ... }>
        next: build-chain         # engine stage chaining
      - name: build-chain
        stage: stage-build
        prompt: prompts/build.md
```

- A work entry is a generic unit: a name, a source (`query_filter`), agents
  and rules. A rule selects a prompt and an agent for the items it matches;
  the match syntax is the engine's (`match.labels` today).
- `query_filter` is the complete tracker query. Jigz appends `assignee:@me`
  through `SORTIE_TRACKER_QUERY_FILTER` at launch. The engine reads the query
  only at start (section 5.4).
- `agent` is a name from `agents` or `pools`, or an array of names (ordered
  fallback). A mapping with `use` overrides `model` or `schedule` of the
  fleet agent for this entry.
- `next` and `stage` are the engine's stages feature (section 4.5).
  `db_path` and `workspace.root` are generated from the slug (section 4.2).
  Slugs are file stems: letters, digits and `-`.

### 3.5 Example 1: business-hours reviewer and 24/7 builder

```yaml
repo: owner/repo
engine: { repo: kinged007/sortie, version: v1.28.1 }
operating_hours: {}               # omitted or empty = 24/7
tracker: { kind: github, api_key: { from_env: JIG_TRACKER_TOKEN }, project: owner/repo }

agents:
  reviewer:
    { runtime: claude-code, model: claude-sonnet-5-5,
      schedule: { timezone: Europe/Madrid, windows: [ { days: [mon, tue, wed, thu, fri], start: "09:00", end: "18:00" } ] } }
  builder: { runtime: claude-code, model: claude-haiku-5-5 }

work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: builder
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
  prs:
    tracker: { kind: github-pr }
    query_filter: "label:agent:review -label:needs-human"
    agent: reviewer
    rules:
      - { name: review, match: { labels: [agent:review] }, prompt: prompts/review.md }
```

The `prs` loop is offline outside Monday to Friday 09:00 to 18:00
(Europe/Madrid); `jig status` shows the reason and estimated start. The
`issues` loop runs all the time.

### 3.6 Example 2: two runtimes with a fallback pool

```yaml
agents:
  claude-main:  { runtime: claude-code, model: claude-sonnet-5-5, env: { ANTHROPIC_OAUTH_TOKEN: { from_env: CLAUDE_SUB_1 } } }
  claude-spare: { runtime: claude-code, model: claude-sonnet-5-5, env: { ANTHROPIC_OAUTH_TOKEN: { from_env: CLAUDE_SUB_2 } } }
  pi-api:       { runtime: pi, model: sonnet, env: { ANTHROPIC_API_KEY: { from_env: ANTHROPIC_API_KEY } } }
pools:
  build-pool: [claude-main, claude-spare, pi-api]
work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"
    agent: build-pool
    rules:
      - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
```

The loop starts on `claude-main`, moves to `claude-spare` and then `pi-api` on
usage limits (when idle), and returns to `claude-main` when it is usable. On
an engine without the `pi` kind, `pi-api` is skipped with the reason shown.

### 3.7 Example 3: rules with their own agents, an agent array and stages

```yaml
agents:
  planner: { runtime: claude-code, model: claude-sonnet-5-5, schedule: { timezone: UTC, windows: [ { days: [mon, tue, wed, thu, fri], start: "08:00", end: "17:00" } ] } }
  builder-a: { runtime: claude-code, model: claude-sonnet-5-5, env: { ANTHROPIC_OAUTH_TOKEN: { from_env: CLAUDE_SUB_1 } } }
  builder-b: { runtime: pi, model: sonnet, env: { ANTHROPIC_API_KEY: { from_env: ANTHROPIC_API_KEY } } }

work:
  delivery:
    query_filter: "label:agent:plan,agent:plan-build,agent:build,in-progress -label:needs-human"
    hooks: { timeout_ms: 300000 }   # overrides the default hooks
    rules:
      - { name: plan,       match: { labels: [agent:plan] },       prompt: prompts/plan.md,  agent: planner }
      - { name: build,      match: { labels: [agent:build] },      prompt: prompts/build.md, agent: [builder-a, builder-b] }
      - { name: plan-build, match: { labels: [agent:plan-build] }, prompt: prompts/plan.md,  agent: planner, next: build-chain }
      - { name: build-chain, stage: stage-build,                   prompt: prompts/build.md, agent: [builder-a, builder-b] }
```

Two candidate lists give two loops, `delivery-planner` and `delivery-builder`
(section 4.5). The `plan-build` chain crosses them, so validation rejects its
`next`; the prompt for `plan-build` items ends by replacing the label with
`agent:build`. With one candidate list for all rules the chain is generated as
written.

## 4. Generation pipeline

### 4.1 Overview

Jigz resolves `.jigz/config.yaml` and the engine profile into one struct per
loop, renders `.jigz/workflows/WORKFLOW.<loop>.md` with `text/template` over
a skeleton taken from today's shipped files, runs `sortie validate` on the
text, and replaces the file atomically. Line patching (`plan.ApplySettings`)
is not used: a template makes golden tests exact.

### 4.2 Names and paths

Loop name: the work slug, or `<slug>-<group>` when an entry has several loops.
Document: `.jigz/workflows/WORKFLOW.<loop>.md`. Database:
`.jigz/data/jigz-<loop>.db`. Workspace: `.jigz/workspaces/<loop>/<issue>`
(unchanged for `issues` and `prs`). State: `.jigz/data/state.json`.

The search path of `internal/loopsrc/discover.go` and the `.gitignore` entries
written by `jig setup` gain `.jigz/data/`; databases left under
`.jigz/workflows/` are ignored (section 4.8).

### 4.3 Managed and unmanaged documents

`managed: true` (default): Jigz generates the document from the config. The
first line inside the front matter is a comment
`# jig:generated v1 skeleton=<kind> engine=<repo>@<version> sha256=<hex>`; the
digest covers the file without that line.

- Digest matches: Jigz may replace the document when the config changes.
- Digest differs: the document has a hand edit. `jig validate` and `jig status`
  report drift with the diff against the generator output. The next generation
  that must change the document (config change, agent change, setup)
  overwrites the hand edit and keeps the previous file as `<name>.md.bak`.
  Hand edits to a managed document are overwritten or reported as drift and
  are not merged (section 10, item 13).

`managed: false`: Jigz neither generates nor overwrites the document. It reads
`tracker.kind` and the rule agents from it for status and for the manager;
The manager only starts and stops the loop; agent changes (section 6) are not
applied to it.

### 4.4 Commands

- `jig setup`: creates `.jigz/config.yaml` when missing, generates documents,
  writes missing prompts, syncs labels (built-in list plus rule labels).
- `jig validate`: config keys and cross references, a dry generation compared
  with the files on disk (drift), the engine profile check, then `sortie
  validate` of each document.
- `jig reload`: regenerates and validates; documents are replaced only when all
  validate, otherwise running loops stay and an alert is shown. `jig start`
  generates first. `jig status` shows state, offline reason and estimate, agent
  and drift per loop.

### 4.5 From work entries to loops

The engine takes an agent kind per rule (`agent: <kind>`, section 2.7) and one
runtime block, with the model, per kind. It does not take a model, effort,
command or list per rule.

1. Each rule resolves to an ordered candidate list of agents: the rule's
   `agent`, else the entry's `agent`; a pool expands to its agents.
2. Rules with the same candidate list form a group, and a group is one loop
   named after the slug. Rules with different lists are realised by separate
   loops named `<slug>-<group>`. This is the baseline because it needs one
   agent command, model and environment per process, which is verified.
3. The query of a separate loop is the work `query_filter` combined with the
   label clauses of its rules, and its `active_states` come from the same
   labels plus the in-progress state. This narrowing is the only thing that
   keeps loops of one entry from claiming the same item: a fixture shows the
   engine does **not** skip an item that matches the query but no rule — it
   dispatches it to `dispatch.default` or through the fallback path
   (`engine-interface-notes.md`). Validation rejects a split whose combined
   queries overlap.
4. Stages chain rules inside one document. When chained rules fall into
   different groups, validation rejects `next` and the first rule's prompt hands
   the item over by changing labels (example 3). Chained rules in one group are
   generated as written.
5. Merging rules of different runtimes into one loop is not possible: a
   fixture shows a `command` key in a runtime block is accepted but never
   executed, so there is no per-kind command or environment
   (`engine-interface-notes.md`). Separate loops per differing candidate list
   (item 2) is the only path.

### 4.6 Agent command and environment

The generated document holds the kind, command and runtime block of the active
agent:

```
agent:
  kind: claude-code
  command: /usr/local/bin/jig agent-exec builder-a
claude-code:
  model: "claude-sonnet-5-5"
```

`jig agent-exec <agent>` starts the runtime CLI with the agent's `env` mapping
resolved from the supervisor's environment and detects usage-limit text
(section 6.2). Kind and command are in the document, not in `SORTIE_AGENT_*`
overrides, so an agent change is a document change that the engine re-reads.
The command uses the absolute path of `jig` at generation time; the digest
includes it, so moving the binary is drift. The engine does not expand `$VAR`
in `agent.command` and does not pass shell quoting through intact (probed,
`engine-interface-notes.md`), so the generator writes a literal absolute path
and no shell metacharacters, and there is no `$JIG_BIN` fallback. The model is
a literal line; `JIG_MODEL` is unused.

### 4.7 Engine targets and profiles

The `engine` key selects the repository and release. A profile is compiled data
per supported release: repository, version, tracker kinds, agent kinds,
database schema number, accepted `SORTIE_*` names and front matter keys.
`rollup.EngineReleases` and `engine.MinVersion` are folded into this table, and
the capability probe matches the running engine to it.

A missing required feature (tracker or agent kind) takes the loop offline with
the existing reason text (`Gate.TrackerOff`, `Gate.Agent`). A missing optional
key fails validation when the user set it and is omitted otherwise. A version
newer than the newest entry uses the newest profile with a warning that the
release has no contract tests; a version older than `MinVersion` starts no
loop. Switching `engine` in the config and running all tests shows what
breaks.

### 4.8 Existing installs

There is no migration (section 10, item 11). Previous user configuration is
ignored: `machine.yaml` and `jigz.yaml` are not read, and the loader rejects
the old keys of `.jigz/config.yaml` (`milestone`, `assignee`, `workflows`) with
a message naming the replacement where one exists. The owner removes the old files and runs `jig
setup`, which writes the first config copy (section 3.1). Documents without a
marker are handled as drift (section 4.3); `jigz-<name>.db` files left under
`.jigz/workflows/` are not read.

## 5. Scheduling: availability and operating hours

### 5.1 The manager

The supervisor enforces availability per loop in `gateDecide`
(`cmd/jig/gate.go`) on the existing 2-second tick (`gateStep`), which already
stops idle loops, starts held loops and reports a `hold` reason per workflow.

For each loop, on each tick:

1. Take the candidate agents in order (section 4.5).
2. An agent is available when its working hours and days are open, it is not
   exhausted by a usage limit (`exhausted_until`), its `from_env` references
   resolve, and its runtime is registered in the engine.
3. Operating hours are a second-level constraint intersected with every
   agent's hours: the loop may run only while operating hours are open and the
   chosen agent is available.
4. Pick the first available candidate. If it is the current agent, do nothing;
   otherwise apply the change (section 6.3).
5. If none is available, or operating hours are closed, take the loop offline
   (section 5.2), record the reason and the estimated start time, and show both
   in `jig status` and on the dashboard.

The estimated start time is the earliest of: the next working-hours start of a
candidate, the earliest usage reset time of a candidate, and the next
operating-hours start. With no estimate the reason is shown alone.

### 5.2 Operating hours and offline handling

Operating hours default to 24/7, use the syntax of agent hours, and cannot be
extended by an agent. Outside them every loop goes offline. The reason that
closed the gate chooses the end behaviour: `operating_hours.end` when operating
hours closed, the agent's `schedule.end` when only its hours closed and no
later candidate is available. `finish` stops the loop when it has no running
session (`gateStopIdle`). `stop` stops it at once (`gateStopNow`); the item
keeps its in-progress label and is dispatched again when the loop starts.

### 5.3 Concurrent sessions per loop

The engine can run several workers per loop (`max_concurrent_agents`). Every
decision above is made per loop. With several sessions:

- Idle detection (`noSession`) keeps its meaning, no active session in the
  loop, and is the condition for every document change and every `finish`
  stop. A loop under continuous load may not become idle (section 9).
- `liveTracker` is keyed by issue id, and `tick(running)` with `unaccounted`
  already handle counts above 1. A loop whose tick reports more sessions than
  the tracker saw start is not known and is never treated as idle.
- A session record gains the agent name (from `agent-exec`); one loop has one
  agent at a time because changes happen at idle.
- With `finish` the loop can claim new items until idle. `max_concurrent_agents:
  0` is not a drain lever: probing the engine shows `0` means no limit, not
  "claim nothing" (`engine-interface-notes.md`), so no document value stops new
  claims while running sessions finish. Drain is `finish` semantics only
  (`gateStopIdle`).
- A usage limit hit by one session marks the agent exhausted; the other
  sessions of that agent fail on the same limit and the engine retries them.
- Status per loop: `sessions`, `agent`, `state` (running, draining, offline),
  `offline_reason`, `eta`, `drift`.

### 5.4 What needs a restart

The engine re-reads the document while it runs (section 2.7; live keys to verify
with a fixture), so most changes apply without a
restart. The query filter is read only at start: a change of `query_filter`, of
the rule labels that form it, or of the assignee scope needs a drain and
restart, which the manager performs on `jig reload` (stop at idle, start with
the new environment). Agent changes are also made at idle, which keeps sessions
and their agent attribution consistent.

### 5.5 Runtime state

`<repo>/.jigz/data/state.json`, under `flock` as `pool.json` is today, holds per
agent `exhausted_until`, `reason` and `last_used`, and per loop the current
agent, offline reason and estimated start. The current agent is recomputed
after a restart.

### 5.6 Other controls and edge cases

A paused loop (budget or manual) is never started by the manager; a closed gate
still stops a running loop. Idle sleep stops the children when every loop is
idle or offline. Loops of one work entry are evaluated independently. Tests
cover windows that wrap midnight across a daylight saving change, agent hours
and operating hours in different timezones, either closed alone, hours closing
with a retry pending, several sessions when the gate closes, and every
no-candidate case of section 5.1.

## 6. Fallback design

### 6.1 Terms

The candidate list of a loop is the rule agent array, the pool order, or the
single agent. The first candidate is the primary; the active agent is the first
available candidate.

### 6.2 Triggers

| Trigger | Detection | In first release |
| --- | --- | --- |
| Usage limit | `agent-exec` output matching the usage-limit patterns; the agent is exhausted until the reset time | Yes |
| No usable account | Credential reference unset, or every candidate unavailable | Yes |
| Runtime not registered in the engine | `Gate.Agent` / `RuntimeOff`; the agent is skipped | Yes |
| Model unavailable | CLI error text such as "model not found" | After fixtures with the messages of both CLIs exist |

Exhaustion is stored per agent in `state.json` (section 5.5).

### 6.3 Semantics

- The change happens when the loop is idle. The manager rewrites `agent.kind`,
  `agent.command` and the runtime block of a managed document to the new
  active agent and the engine re-reads it. A restart is needed only when the
  query changes (section 5.4).
- The primary returns when usable: the next evaluation finds it first and the
  same rewrite happens at the next idle moment.
- A session that fails on a usage limit is retried by the engine. The loop
  keeps its agent while sessions run; `agent-exec` marks the agent exhausted
  so the retry fails fast, and the manager changes agent at idle.
- No available agent: the loop goes offline (section 5.1).

### 6.4 Observability

Status per loop: `agent`, `primary`, `fallback_reason` (trigger and time the
primary is usable again). The Agents page shows each agent, pool and active
agent per loop; the Operations page alerts while a loop is on a non-primary
agent or offline. Runs record `run_history.agent_adapter` and
`session_metadata.model_name`; a test checks that a run after a change shows
its own model. Each change logs a gate-style line.

## 7. Engine-compatibility test strategy

### 7.1 Supported engine matrix

One table in code (the profile list of section 4.7) is the single list of
supported releases: `kinged007/sortie` v1.28.1 (target) and `sortie-ai/sortie`
v1.29.0 (target-switch checks). Every test iterates over it. Binaries come from
`internal/engine` (`JIG_ENGINE_BIN`, share directory, download). Fixture tests
need no binary and run in `go test ./...`. Binary tests carry the build tag
`contract` and run each engine (about 30 seconds; `validate`, `--help`,
`--dry-run`, no agent runs).

### 7.2 Golden files of generated documents

- `internal/gen/testdata/golden/<profile>/<case>.md`, one set per engine
  target: default `issues` and `prs`, a pool with fallback, a `pi` agent, rules
  with an agent array, a work entry split into two loops, a stage chain in one
  loop, hook and prompt overrides, `managed: false`, `prs` on the upstream
  profile (offline with reason). `go test -update` rewrites them.
- A `contract` test runs `sortie validate --format json` on every golden file
  with the matching engine and requires `valid: true` and no `unknown_key`.
- A negative set must fail with exact text: unknown key, plain secret,
  undefined agent, shared label, `next` across loops, `prs` without
  `github-pr`.

### 7.3 Database schema contract

`internal/rollup/testdata/schema/<version>.sql` holds the schema of a database
created by each release, recorded by `make schema-fixture ENGINE=<spec>` (the
engine with the `mock` agent against `internal/fakegithub`). The test builds a
database from each fixture, inserts a row per table Jigz reads using
`requiredColumns` (including `rule_name`, `agent_kind`, `agent_adapter`,
`configured_model`), and runs `InspectSchema` and the collector queries. A
fixture with a required column removed yields a `Missing` entry and degrades
only the dependent view. `EngineReleases` must equal the set of fixtures.

### 7.4 Log-line contract

`cmd/jig/testdata/enginelog/<version>.log` holds lines recorded from a real run
of each release: a session start, a retry start, two concurrent sessions, a
turn, a normal end, a failed end with a retry, and a poll tick with `running`
above 1. The test replays them into `liveTracker` and asserts sessions,
attempts and `unaccounted`. A `contract` test searches each engine binary for
the `msg` literals in `livelog.go`. A re-read fixture changes the agent
command in the document while the engine runs and checks that the next session
uses it (section 5.4).

### 7.5 CLI and capability contract

For each engine: `--help` contains `--env-file`, `--port` and "0 to disable";
`--dry-run --port 0 --env-file <f> <document>` on a golden document exits 0;
`engine.Probe` returns the version and registered kinds (pinning
`parseRegistered`); `validate --format json` has the documented shape. A
document with a rule `agent: <kind>` validates on each profile that registers
the kind and fails with `unknown agent kind` otherwise, which pins the
behaviour section 4.5 relies on.

### 7.6 Acceptance matrix

Acceptance stays the end-to-end proof and is not multiplied by generated
variants. On the fork v1.28.1: generated issues and prs documents through the
mock agent; agent hours closing during a session (loop offline with reason and
estimate, then started); fallback at idle and return to the primary; two
concurrent sessions with a drain that waits for both; a recorded engine log
(`-record`). On upstream v1.29.0: the issues scenario, the prs loop offline
with its reason, and the recorded log. Availability and fallback logic runs on
the supervisor's fake clock (`gate_test.go`, `supervise_test.go`).

### 7.7 CI changes (approved)

The owner approved these changes. Each is made in the phase that needs it
(section 8), as its own pull request touching `.github/workflows`, limited to
this scope:

1. A `contract` job running `go test -tags contract ./...`, in parallel to
   `check` and `acceptance`, with both engines cached.
2. `acceptance` split into two parallel jobs (default engine; upstream engine
   with `TestAcceptanceUpstreamEngine`).
3. `paths-ignore` for documentation-only pull requests (`plans/`, `*.md`).
4. A weekly canary job running the binary contract tests against the newest
   release of each engine repository (non-blocking, opens an issue on failure).

Acceptance time: about 3 minutes by `ci.yml`, 8 by `AGENTS.md` (unknown).

### 7.8 Checklist: adding an engine release

Record `--version`, `--help` and registered kinds; run `make schema-fixture
ENGINE=<repo>@<tag>` and commit the fixture and log recording; add the profile;
confirm accepted keys and `SORTIE_*` variables by `validate`; run `go test
-update` and review the golden diffs; run the `contract` tests and acceptance;
set `engine` in a test config to the release, run all tests and list what
breaks; update README. One pull request contains only the release addition.

## 8. Phased delivery plan

Each phase is one or more small pull requests that ship without the later
ones and includes its tests and README update.

| Phase | Scope | Acceptance criteria | Tests |
| --- | --- | --- | --- |
| 0 | Documentation fix: correct the README `JIG_MODEL` rows (section 2.8). No behaviour change | README matches the code | Review |
| 1 | Single-file config model and loader: `engine`, `operating_hours`, `tracker`, `hooks`, `agents`, `pools`, `work` in `internal/config`; strict keys; cross-reference validation; `from_env` handling and rejection of plain secrets; `jig validate` reports config errors. The loader still accepts the current keys, and loops still use the shipped documents | A valid file loads into one resolved struct; invalid files fail with field paths | Config tests for strict keys, error paths, name resolution, the three examples of section 3 |
| 2 | Agent hours, operating hours and offline handling: per-loop evaluation in `gateDecide`, estimated start time, `jig status` and dashboard fields, state in `.jigz/data/state.json`, concurrency changes of section 5.3 (no document generation; a work entry is bound to the existing document of the same name) | A loop outside agent or operating hours is offline with reason and estimate; `finish` and `stop` behave as in section 5.2; several sessions are handled | Table tests in `gate_test.go` (wrap, DST, both closed, end precedence, paused, several sessions, no candidate); one acceptance case |
| 3 | Generation of engine documents: templates, names and paths, marker and digest, drift, `managed: false`, loop splitting, atomic write, `jig setup`/`validate`/`reload`/`start` integration, query-change drain and restart, golden files for both engine targets | Output for the default config equals the shipped files except for the marker and paths; a hand edit in a managed document is reported as drift | Golden set (7.2), drift tests |
| 4 | Fallback: candidate lists, rule agent arrays, document rewrite at idle, return to the primary, status and dashboard fields, alert, stages across split loops | Example 2 and 3 behave as section 3 describes | Supervisor tests on the fake clock and state; one acceptance case |
| 5 | Contract tests and CI: engine profile table folding `rollup.EngineReleases` and `MinVersion`, schema fixtures, log fixtures, flag and probe tests under the `contract` tag, re-read fixture; the four approved CI changes (7.7), each in its own pull request | Adding a release to the table without fixtures fails a test; both profiles pass; switching `engine` in a test config reports breakage | Sections 7.3 to 7.5 |
| 6 | Cleanup: removal of `fleetconfig.Load`, `plan.Setup/Validate/Apply`, `ApplySettings`, the model-reference pruning and the `jigz.yaml` README text; machine-level reads of `machine.yaml` for hours and pool removed | `go build`, vet and tests pass; no package imports the removed code | Existing suite |
| 7 | Model-unavailable trigger after fixtures exist | As the fixtures show | Fixture tests of the messages |

Phase 2 needs the loader but no generation. Phase 3 carries the golden files;
the contract job and the other fixtures wait for phase 5.

## 9. Risks

| Risk | Effect | Mitigation |
| --- | --- | --- |
| A loop under continuous load never becomes idle | Agent changes and `finish` are delayed | Measure in acceptance; if needed a bounded wait, then a drain |
| Claims between close and stop with several workers | Work starts after the gate closed | No document value stops new claims (`max_concurrent_agents: 0` means no limit). Use `finish` and accept that a draining loop claims until idle; use `stop` when the gap matters. Document the limitation. |
| The engine's re-read scope is not proven | A live document change is ignored | The re-read fixture (7.4); restart the loop when a change is not applied |
| Per-kind commands unverified | Rules cannot share a loop | Separate loops (section 4.5) |
| Hand edits to a managed document are overwritten | Lost edits | `.bak` copy, drift report, `managed: false` |
| Two loops claim one item | Duplicate work | Label overlap validation, combined queries (section 4.5) |
| Engine renames a log message or a column | Live view or history breaks | Sections 7.3 and 7.4; degraded notices in `rollup` |
| Strict keys reject old files | Old files fail to load | Clear errors naming the new keys; `jig setup` writes the first copy (4.8) |
| Agent changes flap when limits flap | Repeated rewrites | Changes only at idle; cooldown before returning |

## 10. Decisions

Decided by the owner and applied throughout this document.

1. **Location.** Configuration, workflows and prompts are stored in
   `<repo>/.jigz/` only; nothing under `~/.config/jigz`. Operating hours, the
   agent pool and the engine target are repository configuration. The hub
   daemon may differ and is out of scope.
2. **One file.** `.jigz/config.yaml` holds the repository section, the fleet
   (`agents`, `pools`) and `work` (section 3). Assignee is always `@me`;
   milestone is not configurable. The engine target is the fork
   `kinged007/sortie` v1.28.1 until upstream supports `pi` and `github-pr`.
   `effort` is not in the model: it is a runtime-block key, so it is per agent
   kind and not per rule. Delivery to `claude-code` is proven
   (`--effort <value>`, `engine-interface-notes.md`); delivery to `pi` is not,
   so no `effort` is generated. Adding it back needs the `pi` proof. Secrets
   are environment variable references.
3. **Manager.** First available agent per loop; offline with reason and
   estimated start when none; document rewrite to a backup agent at idle;
   drain and restart on a query filter change (section 5).
4. **Hand edits.** `managed: false` stops generation and overwriting
   (section 4.3).
5. **Fallback.** Triggers are usage limit and no usable account; the change
   happens at idle; the primary returns when usable. Model-unavailable comes
   later.
6. **Agent granularity.** Agents are assigned per rule, including arrays;
   separate loops realise differing agents (section 4.5).
7. **Hours.** General operating hours (default 24/7) plus per-agent hours.
8. **Old code.** The `fleetconfig` loader and `internal/plan` settings
   patching are removed in phase 6.
9. **CI changes are approved** (section 7.7), each its own pull request.
10. **Dashboard settings and secret references.** There is one configuration
    file (item 2): `dashboard.hide`, `limits` and `connected` are keys of
    `.jigz/config.yaml`, and the `from_env` references live in that file and
    resolve from the supervising process's environment. A united dashboard
    (`--unite`) uses the configuration and location of the repository where
    the first united dashboard is spawned.
11. **No migration.** Previous users and configuration are ignored (section
    4.8); there is no `jig migrate` and no automatic conversion.
12. **README `JIG_MODEL`.** The claim is wrong and phase 0 corrects it
    (section 2.8); `JIG_MODEL` has no use in the target model.
13. **Conflicting edits.** The section 4.3 behaviour stands: drift is
    reported, and the next generation that must change the document
    overwrites the hand edit and keeps a `.bak`.

## 11. Still open

Nothing is open.

## 12. Implementation status

Maintained while the work is executed. Every entry names the branch or pull
request and the tests that gate it. The phase table of section 8 stays the
plan; this section records what is done.

### 12.1 Phase table

| Phase | Branch | Status | Notes |
| --- | --- | --- | --- |
| 0 Documentation fix | `feat/engine-interface` | done | README `JIG_MODEL` rows removed (a7c7d4c) |
| 1 Config model and loader | `feat/engine-interface` | done | merged 4c0ac2b; `internal/config/{work,strict}.go`, `Config.Resolve()`, `UnresolvedRefs()` |
| 2 Hours and offline handling | `feat/engine-interface` | done | merged 0a2ba54; `gateDecide` per loop, `.jigz/data/state.json`, `d2_offline_reason_and_estimate` |
| 3 Document generation | | not started | |
| 4 Fallback | | not started | |
| 5 Contract tests and CI | | not started | |
| 6 Cleanup | | not started | |
| 7 Model-unavailable trigger | | not started | |

### 12.2 Work log

- 2026-10-27: integration branch `feat/engine-interface` created from `main`.
  Each phase is built in its own git worktree branch and merged into the
  integration branch with `--no-ff` after review and the quality gate (`gofmt`,
  `go vet`, `go build`, `go test ./...`). Phase branches are kept so the work
  can be split into one pull request per phase, per AGENTS.md.
- 2026-10-27: phase 0 complete on `feat/engine-interface` (a7c7d4c). The README
  table of environment variables no longer lists `JIG_MODEL` and
  `JIG_MODEL_<WORKFLOW>`; the model is configured on the runtime block of the
  workflow document, as the agent section already states.
- 2026-10-27: `plans/jigz/engine-interface-notes.md` added as a code map for the
  implementation phases (file and line references of the current code).
- 2026-10-27: phase 1 dispatched to a coding-manual agent in a worktree (branch
  `feat/config-model`). In parallel, a coding-low agent probes the two cached
  engine binaries for the questions section 2.7 and section 7 leave open
  (`$VAR` expansion in `agent.command`, per-block `command`, `claude-code.effort`,
  `max_concurrent_agents: 0`, the `SORTIE_*` override set, registered kinds,
  rule key behaviour, front-matter keys, log literals, schema tables). The
  findings feed phases 3 to 5.
- 2026-10-27: phase 1 complete, merged `--no-ff` into `feat/engine-interface`.
  `.jigz/config.yaml` gains `engine`, `operating_hours`, `tracker`, `hooks`,
  `agents`, `pools`, `work`. `internal/config/strict.go` walks the YAML node
  tree against the struct's yaml tags so an unknown key reports its field
  path (the yaml package's `KnownFields` cannot). Credentials are
  `{from_env: NAME}` references; a literal is rejected for a key ending in
  `TOKEN`/`KEY`/`SECRET`/`PASSWORD`; `Config.UnresolvedRefs()` reports the
  unresolved references per agent. `milestone`, `assignee` and `workflows`
  are rejected with a message naming the replacement. `FilterFor` reads
  `work.<slug>.query_filter` and appends `assignee:@me`; budgets move to
  `work.<slug>.budget`; the `JIG_MILESTONE`/`GH_MILESTONE`/`JIG_ASSIGNEE`
  reads are dropped. `internal/source/hub.go` stops writing `assignee:`
  because the loader rejects it (the scope is unchanged: `@me` is the
  runner's login). Gate green: `gofmt` empty, `go vet` clean, `go build`
  clean, all 23 packages pass. Review: the strict-key checker is reflection
  over yaml tags, the only way to name the path of the offending key; tests
  cover 22 unknown-key cases across every nesting level, the three worked
  examples of section 3 asserting the resolved structure, secret literal
  rejection, and the removed-key messages.
- 2026-10-27: phase 2 complete, merged `--no-ff` into `feat/engine-interface`.
  `gateDecide` evaluates each loop on the 2-second tick against the candidate
  agents of its work entry (schedule open, not exhausted, `from_env` resolved,
  runtime registered in the engine), with operating hours as a second-level
  constraint. The first available candidate is the loop's agent; otherwise the
  loop is offline with a reason and an estimated start, the earliest of the
  next schedule start, the earliest exhaustion reset and the next
  operating-hours start. The reason that closed the gate picks the end mode:
  `operating_hours.end` when operating hours closed, else the blocked
  candidate's `schedule.end` when its hours alone closed, else `finish`.
  Runtime state moves to `<repo>/.jigz/data/state.json` under flock (per agent
  `exhausted_until`/`reason`/`last_used`, per loop agent, offline reason and
  ETA); the legacy XDG `pool.json` is read once and never written. A tick
  reporting more sessions than the tracker saw keeps the loop busy. `drift` is
  exposed on `workflowStatus` and left empty for phase 3. Gate green:
  `gofmt` empty, `go vet` and `go vet -tags acceptance` clean, `go build`
  clean, all 23 packages pass. Review: `gateDecide` matches section 5.1 and
  5.2 step for step; the tests assert exact values across the Europe/Berlin
  spring-forward jump and across Asia/Tokyo versus America/New_York, and cover
  end-mode precedence in both directions, paused loops, several sessions under
  `finish` and `stop`, all four no-candidate cases, and the ETA being the
  earliest of its three inputs.
- 2026-10-27: the first phase 1 dispatch died to a provider stall
  (`stall-watchdog-retry`) before producing output; no worktree was created and
  no code was written. Re-dispatched the same brief as `phase1-config-2`, with
  an instruction to keep the tree compiling and work through to the quality
  gate rather than stopping early. Baseline on `feat/engine-interface` at
  a7c7d4c is green: `gofmt` empty, `go vet` clean, `go build` clean, all 23
  packages pass `go test ./...`.
- 2026-10-27: the engine probe (coding-low) ran 84 tool calls and was then
  killed by the same provider stall before writing its report. Its findings
  were recovered from the transcript and recorded in
  `plans/jigz/engine-interface-notes.md` under "Engine findings": registered
  kinds per release, the `validate --format json` shape (unknown keys are
  warnings, not errors), the CLI flags, no `$VAR` expansion in
  `agent.command`, `max_concurrent_agents: 0` meaning no limit, the stability
  of the six `livelog.go` literals, and the dispatch line carrying
  `agent_kind`, `rule_name`, `model` and `effort`. Sections 4.6, 5.3 and 9 are
  updated to match; the `$JIG_BIN` fallback is dropped.
- 2026-10-27: runtime fixtures answered the three remaining open questions
  (`plans/jigz/engine-interface-notes.md`, "Runtime fixture findings"). A
  `command` key in a runtime block validates silently but is never executed,
  so section 4.5 item 5 rules out merging different runtimes into one loop and
  `agent.command` is the only lever. `claude-code.effort` reaches the CLI as
  `--effort <value>`, so decision 2 is half satisfied; `effort` stays out
  because it is per agent kind and `pi` delivery is unproven. An item that
  matches the query but no rule is dispatched through `dispatch.default` or
  the fallback path, so section 4.5 item 3's query narrowing is the only
  mechanism that prevents two loops of one entry claiming the same item, and
  validation must reject overlapping combined queries.

### 12.3 Deviations from the plan

None so far.

### 12.4 Interpretation recorded during execution

- "The loader still accepts the current keys" (phase 1) is read as: the keys
  that remain valid in the new model (`repo`, `token`, `github`, `filters`,
  `labels`, `cleanup`, `sleep`) keep working exactly as before. The keys that
  the new model replaces (`milestone`, `assignee`, `workflows`) are rejected
  with a message naming the replacement, as section 4.8 requires. Budgets live
  under `work.<slug>.budget`.
- Phase 1 loads and validates the new sections but does not change loop
  behaviour: loops still start from the shipped `WORKFLOW.*.md` documents and
  `FilterFor` still supplies the query override. The new `work` entries are
  bound to the existing document of the same name from phase 2 onward, and
  generate documents from phase 3 onward.

- 2026-10-27: the first phase 3a dispatch died on a provider billing error
  ("insufficient credits") after 59 tool calls of design reading and before
  any code was written. It left one out-of-scope commit that pointed
  `README.md` at a `ROADMAP.md` it never created; that commit is discarded
  (`git reset --hard` to the previous tip) because it is broken and outside
  the phase 3a scope. No generator code exists yet. The re-dispatch gets a
  tighter brief: the engine facts are stated as settled, it is told to write
  code early rather than read the whole tree, and it is told to stay inside
  `internal/gen`.
