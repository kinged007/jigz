# Engine interface: code map for the implementation phases

Companion to `engine-interface.md`. Records where the current code lives so
implementation briefs can point at exact files. Line numbers are from
`feat/engine-interface` at 2026-10-27 and drift as the work lands.

## Configuration

- `internal/config/config.go` — the `.jigz/config.yaml` loader.
  `Config` struct around line 36: `Repo`, `Token`, `Milestone`, `Assignee`
  (`*string`), `Dir`, `SortieBin`, `EnvFile`, `Tracker`, `Filters` (map),
  `Labels` (`[]Label`), `Workspaces`, `Cleanup`, `Sleep`, `Budgets` (map),
  `GitHub`. Strict keys via `yaml.Decoder.KnownFields(true)` in `Load`,
  `LoadToken`, `LoadGitHub`. `FilterFor` (line 516) returns `filters[name]`,
  else `defaultFilters[name]`, else `defaultFilter`, with milestone and
  assignee scope applied. `defaultFilters` (line 499) holds the `issues` and
  `prs` queries. The workspace root is `.jigz/workspaces`, expanded from
  `$JIG_WORKSPACES` (around line 428).
- `internal/config/strict_test.go` — table of invalid keys, `loadersOn(body)`
  runs `Load`, `LoadToken`, `LoadGitHub`.
- `internal/machine/config.go` (line 74) — `machine.yaml` schema: `Hours`,
  `Pool` (`[]Entry`), `Limits`, `Connected`, `Dashboard`, `Engine`, `Warnings`,
  `Unsupported`.
- `internal/machine/hours.go` — `Hours{Timezone, Windows, End}`, `Window{Days
  [7]bool, Start, End}`. `Hours.Open(t)` handles wrapping windows; `Hours.Next(t)`
  returns the earliest change after `t` (scans 8 days, zero when none).
- `internal/machine/pool.go` — `Entry{Name, Runtime, Command, Hours, Env}`,
  `Entry.ExpandedEnv()`, `Entry.Available(st, t)`, `Config.Pick(st, runtime, t)`,
  `Config.ActiveRuntime(st, t)`.
- `internal/machine/state.go` — `EntryState{ExhaustedUntil, Reason, LastUsed}`,
  `StatePath()` is `<XDG_STATE_HOME or ~/.local/state>/jigz/pool.json`,
  `UpdateState(path, fn)` takes a flock, `MarkExhausted` writes the exhaustion.
- `internal/machine/dashboard.go` — `Dashboard{Hide{Pages, Sections}}` and
  `LoadDashboard(path)` (reads only the `dashboard` block, strict inside it).
- `internal/fleetconfig/config.go` — `Resolved` (line 269): `Repo`, `Dir`,
  `Workflows`, `Milestone`, `Assignee`, `TokenEnv`, `Agent`, `PollingIntervalMS`,
  `Knobs`, `Overrides`. `Load(path)` (line 414) reads `jigz.yaml` with strict
  keys. `Env()` builds the cascade env (sets `JIG_ONLY`, `JIG_MODEL`, `JIG_TOKEN`,
  `SORTIE_*`). `ModelVar(workflow)` returns `JIG_MODEL` or `JIG_MODEL_<WORKFLOW>`.
  Removed in phase 6.
- `internal/plan/` — `plan.go` `Apply(r config.Resolved) error` (line 424,
  installs workflows, syncs labels, writes `.env`/`.gitignore`), `Setup`
  (line 484) wraps `RunSetup` (line 494), `ApplySettings` (line 59) does
  line-level YAML edits and prunes stale model refs, `Validate` (line 526) runs
  `sortie validate` per file and returns `Diagnostics`. `workflow.go`
  `ParseWorkflow(text)` (line 78) reads front matter into `Workflow{TrackerKind,
  QueryFilter, HandoffState, TerminalStates, PollingMS, AgentKind, Model, Knobs,
  Template, Rules}` and `Rule{Name, Labels, Agent, Template, Stage, Next}`.
  `gating.go` `GateLoop(engine.Gate, text, env) LoopGate` (line 27). Removed in
  phase 6.

## CLI

- `cmd/jig/validate.go` — `listWorkflows` (line 75) scans `.jigz/workflows/`
  for `WORKFLOW.*.md` and returns `[]loopDef`; `dropRuntimeBlockWarnings`
  (line 149) filters `sortie validate` output through
  `plan.IsRuntimeBlockWarning`; `collectValidateChecks` (line 27) runs config
  load, cleanup, budget and token checks, then per-workflow validation.
- `cmd/jig/embed.go` — `syncWorkflows` (line 46) writes missing
  `WORKFLOW.*.md` + `prompts/*.md` from `//go:embed workflows` (line 14), keeps
  existing edits (never overwrites), returns legacy files. `ensureGitignore`
  (line 603) writes `.jigz/` and `/jig` and removes granular entries from the
  old scheme.
- `cmd/jig/setup.go` — `runSetup` (line 19): resolve root, load repo slug,
  create `config.yaml` from template, `syncWorkflows`, warn legacy, sync the PM
  prompt, resolve the engine binary, `ensureGitignore`, load config (strict),
  `labelSync` through the `gh` CLI.
- `cmd/jig/gate.go` — `gateEval` (line 67) `(cfg, st, now) gateView`;
  `gateDecide` (line 85) `(cfg, st, now, loops) (gateView, []gateAct)` with
  `StopIdle`, `StopNow`, `Start`, `RecycleIdle`; `gateView` (line 36)
  `{Open, HoursOpen, Runtime, Next, Reason}`; `gateStatus` (line 137)
  `{Open, HoursOpen, Runtime, NextChange, Hold}`; `gateStep` (line 344) runs on
  the 2-second ticker (line 305), refreshes `machine.yaml` and `pool.json`
  (every 10 s), and executes the actions.
- `cmd/jig/supervise.go` — `childEnv(name)` (line 181) returns `(envFile, env,
  runtime, error)`: loads config, resolves the engine binary, sets `SORTIE_*`,
  writes `.env`. `SORTIE_TRACKER_QUERY_FILTER` comes from `cfg.FilterFor(name)`;
  `SORTIE_AGENT_KIND` and `SORTIE_AGENT_COMMAND` (`"jig agent-exec <runtime>"`)
  are set from the pool entry. `supervisor.noSession(name)` (line 56) is true
  when the loop age is at least `drainSettle` (20 s) and no session is active.
- `cmd/jig/agentexec.go` — `runAgentExec` (line 15): parse runtime and args,
  load `machine.yaml` and `pool.json`, `cfg.Pick`, expand `env`, start
  `entry.Command`, relay I/O, scan stderr for usage-limit text per runtime
  (line 126), call `MarkExhausted`.
- `cmd/jig/status.go` — `workflowStatus` (line 45) with `Name`, `WorkflowFile`,
  `LoopType`, `PID`, `State`, `Hold`, `Sessions`, `SessionsKnown`; `noteGate`
  (line 444) maps `gateView` to `gateStatus`. States: `running`, `idle`, `held`,
  `disabled`, `off`.
- `cmd/jig/unite.go` — `dbPathFor(root, file)` (line 264) reads `db_path:` from
  the front matter as plain text and joins relative paths to
  `root/.jigz/workflows/`.
- `cmd/jig/livelog.go` — `liveTracker` (line 29); message literals:
  `issue dispatched`, `retried issue dispatched`, `turn completed`,
  `worker exiting`, `worker run failed, scheduling retry`, `tick completed`.
  `tickDropAfter` is 5 s (line 19). `snapshot()` (line 50) returns sessions and
  `ticked && unaccounted == 0`.

## Engine coupling

- `internal/engine/engine.go` — `DefaultRepo = "kinged007/sortie"`,
  `DefaultTag = "v1.28.1"`, `Binary = "sortie"` (lines 29–33). Resolution
  order: `JIG_ENGINE_BIN`, `JIG_ENGINE_URL` + `JIG_ENGINE_SHA256`, cache at
  `Spec.CachePath(dir)` (`<dir>/sortie-<owner>+<repo>-<version>`), then GitHub
  release download. `Ensure(spec)` fetches and links into `~/.local/bin`.
  `DataDir()` is `$XDG_DATA_HOME/jigz` or `~/.local/share/jigz`.
- `internal/engine/probe.go` — `Capabilities{Bin, Version, Trackers, Agents,
  Err}`; `Probe(bin)` is cached per path+mtime+size; `parseRegistered(message)`
  (line 65) pulls kinds out of `registered: [...]`; `parseKinds` (line 82)
  parses `sortie validate` JSON errors (`tracker_adapter`, `agent_adapter`).
- `internal/rollup/schema.go` — `OldestSchema = 18` (line 14),
  `EngineReleases = []EngineRelease{{"1.28.1", 38}, {"1.29.0", 43}}` (line 26),
  `SupportedSchemas = []int{18, 35, 38, 43}` (line 32), `requiredColumns` map
  (line 51) over `run_history`, `aggregate_metrics`, `session_metadata`,
  `parked_issues`, `retry_entries`, `budget_hold_notices`. `InspectSchema(db)`
  (line 108) returns `Schema{Notice}` with missing tables and columns.
- `internal/loopsrc/discover.go` — `Discover(roots map[string]string) []rollup.Loop`
  scans `root/.jigz/workflows/` for `WORKFLOW.*.md` and `jigz-*.db`, not
  recursive.

## Shipped documents

`cmd/jig/workflows/WORKFLOW.issues.md` and `WORKFLOW.prs.md`. Front-matter
keys: `tracker{kind, api_key, project, query_filter, active_states,
in_progress_state, handoff_state, handoff_evidence, terminal_states,
comments{on_completion, on_failure}}`, `polling{interval_ms}`, `db_path`,
`workspace{root, retention_days}`, `hooks{after_create, before_run, after_run,
timeout_ms}`, `agent{kind, command, max_turns, max_concurrent_agents,
turn_timeout_ms, read_timeout_ms, stall_timeout_ms, stop_grace_ms,
max_retry_backoff_ms}`, `pi{model}`, `claude-code{model}`,
`dispatch{default{template}, rules[{name, match{labels}, agent, template,
stage, next}]}`. Models are literal lines (`model: "sonnet"`,
`model: "claude-haiku-5-5"`), not `$JIG_MODEL`.

## Test and CI patterns

- Table-driven subtests with `t.Run`, `t.TempDir()`, `t.Setenv`,
  `os.WriteFile`. Representative: `cmd/jig/gate_test.go` (`gateCfg` writes
  YAML to a temp file and calls `machine.Load`), `cmd/jig/supervise_test.go`
  (`newTestSup` isolates XDG dirs), `internal/machine/machine_test.go`
  (`isolate`, `load`), `internal/config/strict_test.go`.
- `internal/fakegithub` — HTTP handler for the github and github-pr tracker
  routes: `New(user, token)`, `Add(Item) int`, `AddComment`, `Get`, `SetClock`,
  `LabelEvents`, `Unexpected`.
- `internal/fakehub` — HTTP handler for the hub daemon protocol v0.
- `cmd/jig/acceptance_test.go` (build tag `acceptance`): `TestAcceptance`
  (line 76, pinned engine + `mock` agent, scenarios a_plan, a2_no_server,
  b_excluded, c_dashboard, d_hours, e_usage_limit, f_question),
  `TestAcceptanceUpstreamEngine` (line 737, upstream v1.29.0, scenarios
  a_handoff_pr_disabled, b_dashboard), `TestAcceptanceConnected` (line 945,
  fakehub). `acceptanceEngine(t)` (line 1467) resolves the binary;
  `buildLoop(t)` (line 1498) compiles `jig`.
- `.github/workflows/ci.yml` — `check` (gofmt, vet, test), `acceptance`
  (`go test -tags acceptance -run '^TestAcceptance'`), `build` (4-way
  cross-build matrix). No engine caching; acceptance downloads on demand.
- `Makefile` — `verify` (gofmt, vet, vet -tags acceptance, build, test,
  acceptance), `test`, `acceptance`. There is no `schema-fixture` target yet.
- Existing testdata: `cmd/jig/testdata/` (example.yaml, runpage.js,
  WORKFLOW.*.md), `internal/rollup/testdata/schema18.sql`.

## Engine findings (probed 2026-10-27)

Recorded by running the two cached binaries in `~/.local/share/jigz/`. These
settle the items section 2.7 and section 7 marked "to verify with a fixture".
The phase 5 contract tests turn them into assertions.

### Registered kinds

| | v1.28.1 (fork `kinged007/sortie`) | v1.29.0 (upstream `sortie-ai/sortie`) |
| --- | --- | --- |
| Trackers | `file, gitea, github, github-pr, gitlab, jira, linear` | `file, gitea, github, gitlab, jira, linear` |
| Agents | `agent-client-protocol, claude-code, codex, copilot-cli, mock, opencode, pi` | `agent-client-protocol, claude-code, codex, copilot-cli, mock, opencode` |

Confirmed by the `__probe__` document technique of `internal/engine/probe.go`.
Section 2.7 is correct.

### `validate --format json` shape

```json
{"valid": false,
 "errors":   [{"severity":"error","check":"tracker_adapter","message":"..."}],
 "warnings": [{"severity":"warning","check":"unknown_key","message":"bogus_top: unknown top-level key \"bogus_top\""},
              {"severity":"warning","check":"unknown_sub_key","message":"agent.bogus_agent_key: unknown key \"bogus_agent_key\" in section \"agent\""}]}
```

Unknown front-matter keys are **warnings**, not errors: `valid` stays `true`
and the checks are `unknown_key` (top level) and `unknown_sub_key` (inside a
section). A document with a rule `agent: pi` on the upstream binary gives
`{"valid":false,"errors":[{"severity":"error","check":"config.dispatch.rules[0].agent",
"message":"unknown agent kind \"pi\""}]}`. The phase 5 contract test must
require `valid: true` AND an empty `warnings` list, not just `valid: true`.

### Flags

Both binaries: `--env-file PATH  Path to .env file for config overrides`,
`--port PORT  HTTP server port, 0 to disable (default: 7678)`, `-dumpversion`,
`-V/--version`. Section 7.5 is satisfied by both.

### `agent.command`: `$VAR` is not expanded

`agent.command: /bin/sh -c 'echo PROBE_VAR_VALUE=$PROBE_MARKER > /tmp/ep/out.txt'`
reaches the runtime with `$PROBE_MARKER` literal (`sh` reports
`PROBE_VAR_VALUE=$PROBE_MARKER: 1: Syntax error: Unterminated quoted string`).
The engine does not expand `$VAR` in `agent.command`, and the quoting is not
passed through intact. Consequences for section 4.6:

- The `$JIG_BIN` fallback is dropped. The generator writes the absolute path
  of `jig` taken at generation time.
- `agent.command` must be a single argv-bearing value that does not rely on
  shell quoting. Generate `agent.command` as the absolute `jig` path plus the
  agent name, with no shell metacharacters.

### `max_concurrent_agents: 0` does not drain (answers section 5.3 and section 9)

Ran the engine with a `mock` agent against a scratch tracker and varied
`max_concurrent_agents`, counting the `tick completed` fields:

| setting | candidates | first tick |
| --- | --- | --- |
| `1` | 2 | `dispatched=1 running=1` |
| `0` | 2 | `dispatched=2 running=2` |
| `0` | 3 | `dispatched=3 running=3` |

`0` means **no limit**, not "claim nothing". There is no document value that
stops new claims while running sessions finish. Section 5.3's mitigation ("the
manager sets `max_concurrent_agents: 0` when a drain starts") is not available.

Replacement: drain is `finish` semantics only — `gateStopIdle` stops the loop
when no session is active. With `max_concurrent_agents > 1` a draining loop can
claim new items until it becomes idle; that is already the documented README
limitation and stays a known limitation. Section 9's risk "claims between close
and stop with several workers" keeps its effect and its mitigation changes to
"document the limitation; use `stop` end behaviour when it matters".

### Log literals are stable across both binaries

`cmd/jig/livelog.go`'s six literals all appear in both binaries:
`issue dispatched`, `retried issue dispatched`, `turn completed`,
`worker exiting`, `worker run failed, scheduling retry`, `tick completed`.
Near-misses exist and must not be matched: `turn complete`, `worker exit`,
`session completed`, `worker run failed` (without the retry suffix).
`tick done` and `turn finished` do not appear.

### Dispatch log carries the model (answers section 6.4)

```
issue dispatched" issue_id=1 issue_identifier=1 agent_kind=claude-code
  rule_name=plan model=claude-haiku-5-5 effort=""
```

Every dispatch line carries `agent_kind`, `rule_name`, `model` and `effort`.
Section 6.4's "a test checks that a run after a change shows its own model" is
supported directly by the log, and `session_metadata.model_name` /
`run_history.agent_adapter` are the database side of the same fact.

## Runtime fixture findings (2026-10-27)

Answers the three items sections 2.7, 4.5 and 5.4 marked "to verify with a
fixture". Full evidence in `/tmp/fixture-report.md`, harness in `/tmp/fixtures/`
(copy of `internal/fakegithub` plus stub commands that record their argv).
Both binaries behave identically on all three.

### No per-runtime-block `command` (settles section 4.5 item 5)

`claude-code: {command: ...}` validates with **zero warnings** — the key is not
even reported as unknown — but it is never executed. The marker file shows
`agent.command` always runs. With `agent.command` absent the engine falls back
to the kind's default binary on `PATH` (the real `claude`), not to the block's
command. Same on upstream.

Consequences:

- The generator never emits `command:` inside a runtime block. `agent.command`
  is the only lever that selects the executable.
- Section 4.5 item 5 is settled against merging: rules of different runtimes
  cannot share one loop, because there is no per-kind command or environment.
  Separate loops per differing candidate list (section 4.5 item 2) is the only
  path and is no longer provisional.

### `claude-code.effort` reaches the CLI (settles section 2.7)

With `effort: high` in the `claude-code:` block, the child argv contains
`--effort high` between `--model` and `--tools`; the child environment carries
only `HOME`, `PATH`, `PWD`. Without the key there is no `--effort` argument and
the dispatch line reads `effort=""`. Same on upstream.

Decision 2 ("effort is left out until delivery to each CLI is verified") is
now half satisfied: delivery to `claude-code` is proven as `--effort <value>`;
delivery to `pi` is not. The generator still emits no `effort`, because
`effort` lives in the runtime block and so is per agent kind, not per rule.
Enabling it is an owner decision and needs the `pi` delivery proof too.

### A query-matching, rule-non-matching item is NOT skipped (settles section 4.5 item 3)

`tracker.query_filter: "label:todo"`, one rule matching `labels:
["agent:plan"]`. Issue 2 has only `todo`. It is dispatched anyway:

```
msg="issue dispatched" issue_id=2 rule_name=default
msg="tick completed" candidates=1 dispatched=1 dispatched_by_rule=0
  dispatched_by_default=1 dispatched_by_fallback=0
```

With `dispatch.default` removed it is still dispatched through the fallback
path (`rule_name=""`, `dispatched_by_fallback=1`). Same on upstream.

Consequence: the rule `match.labels` clause does **not** filter what a loop
claims. Two loops of one work entry that share a `query_filter` each claim
every active query-matching item. Loop splitting must fold each group's label
clauses into its own combined query and `active_states`, as section 4.5 item 3
already requires — that narrowing is the only mechanism that prevents
double-claiming, so it is mandatory and not a refinement. Validation must
reject a split whose combined queries overlap.

Open detail, low priority: which template the fallback path renders when no
`dispatch.default` exists.
