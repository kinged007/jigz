# Enhancements

Status: in progress

Four changes to `sortie-loop` (idle sleep, workflow consolidation, workspace
cleanup, spend controls), a summary of two features that belong in
`sortie-central` (dashboard control plane, form-based editing), and the
requests `sortie-central` makes of `sortie-loop`. The Sortie engine is not
changed. One request (L7, cost per run) is deferred until the engine records
cost.
Statements about Sortie engine behaviour come from summarised documentation and are
marked "to verify against docs before implementing". Unverified items are
listed under Open questions.

## Implementation status and handoff

State as of `1e64a17` on `main` (ahead of `origin/main` by 4 commits: `c329bc8`, `bed152c`, `b9e7eb2`, `1e64a17`).
Section numbers below refer to this document.

### State

| Item | State | Evidence | Note |
| ---- | ----- | -------- | ---- |
| 1. Idle sleep (section 1) | done | `6728935`, `0dc7e1e`, `efc71ab`, docs `3384ab2` | Disabled by default. |
| 3. Cleanup (section 3) | done | `7b8e9f7`, `6f08a10`, `e385697`, docs `2508689` | Config, interval runner, `cleanup` subcommand, last-run record. |
| 2. Workflow consolidation (section 2) | done | `53aba7c` | Two files. `agent:plan-build` is one session on the pinned engine; stage chains need an engine based on 1.27.0 or later. |
| 4. Spend controls (section 4) | done | `d132910` | Includes L1 budget fields. Relation to engine `budget_exhausted` investigated (see `/tmp/budget-investigation.md`); control reads `run_history` only. |
| L1 status document | done, partial | `18eacae`, `04ea228`, `b557387`, `bb5a8cf`, docs `43d3a79`, `e6724ee` | File and `sortie-loop status [--json]`. Sleep, cleanup, budget and `log_path` fields exist. Tested schema versions (L4) are not in the document. |
| L2 `wake` | done | `efc71ab` | |
| L2 `cleanup` | done | `e385697` | |
| L2 `pause`, `resume`, `budget override` | done | `d132910` | Part of spend controls. |
| L3 registry additions | done | `d636dac`, `b3e8f47` | `b3e8f47` adds registry locking and atomic writes. |
| L4 tested schema versions | not started | none | Central pins its own versions. |
| L8 child logs | done | `30a4112`, docs `f9f182e` | |
| L8 per-run transcript | not started | none | Depends on an open question (pi transcript file unverified). |
| L9 `validate` | done | `1e64a17` | `sortie-loop validate [--json]` checks repo config and runs `sortie validate` per workflow (read-only, no file creation). |
| L10 safe apply | done | `bed152c` + existing `restart --when-idle` | `reload` via SIGHUP for repo config; `restart --when-idle` for workflow changes. |
| L11 `setup --json` | done | `c329bc8` | Non-interactive JSON mode; auto-declines mismatched labels and pm-agent overwrites. |
| L12 Windows parity | not started | none | Optional. |
| L7 cost per run | deferred | none | Needs an engine change. |
| sortie-central (items 5, 6) | not started | separate repo | |

Budget semantics: investigated in `/tmp/budget-investigation.md` (engine `budget_exhausted`/`budget_hold_notices` is a per-issue lifetime cap; loop cap is per-workflow per-period). L9 validates loop caps only and ignores engine holds for pass/fail. No code change needed beyond docs.

### Next actions

1. Re-pin the fork to an engine release based on 1.27.0 or later, then
   switch `plan-build` to the stage chain in section 2 and add per-rule
   models. Optional.
2. L4 tested schema versions in the L1 document, and L8 per-run transcript (needs pi transcript file verification). Not blocked by L9-L11.
3. L12 only if Windows is a target.
4. Verify L9-L11 end-to-end with a live supervisor (status/reload/validate/--json) — unit tests only so far.

### Code map

Files are in `cmd/sortie-loop/` unless stated.

New since `3384ab2`: `validate.go` + `validate_cmd.go` (`validate` command), `reload.go` (reload client), `setup --json` branch in `setup.go`, SIGHUP reload in `supervise.go`, `parseArgs`/`usage`/`run` in `main.go`.

| Feature | Location |
| ------- | -------- |
| Status document, status file path, config hash | `status.go` |
| `status` subcommand | `status_cmd.go` |
| Supervisor, status publishing, `loopQuiet` | `supervise.go` |
| Registry, `lockStarts`, `lockRegistry` | `unite.go` |
| Child logs | `childlog.go` |
| Cleanup runner and file paths | `cleanup.go` |
| `cleanup` subcommand | `cleanup_cmd.go` |
| Sleep and wake logic | `sleep.go` |
| Spend controls: periods, token sums, state, enforcement | `budget.go` |
| `pause`, `resume`, `budget override` subcommands | `budget_cmd.go` |
| `wake` subcommand | `wake_cmd.go` |
| Remote control (`stop`, `restart`, signals) | `remote.go` |
| Subcommand dispatch and usage text | `main.go` |
| Config loading and validation (`sleep:`, `cleanup:`, `workflows:`) | `internal/config/config.go` |
| Workflow files and `setup` | `workflows/`, `embed.go`, `setup.go` |

Files and formats:

| Item | Rule |
| ---- | ---- |
| Status file | `<user cache dir>/sortie-loop/status/<hash>.json`. `<hash>` is the first 16 hex digits of the SHA-256 of the absolute repo root. |
| Wake request file | `<hash>.wake` next to the status file. |
| Budget state and requests | `<hash>.budget.json` (per workflow: `period_start`, `paused`, `paused_by`, `override`, `warned`) and one `<hash>.budget.request.<time>-<pid>` file per request from `pause`, `resume` and `budget override`, next to the status file. |
| Cleanup record and lock | `<hash>.cleanup.json` and `<hash>.cleanup.lock` next to the status file. |
| Registry and locks | `registry.json` in `<user cache dir>/sortie-loop/`, with `.lock` (port claim) and `.update.lock` (registry read-modify-write) beside it. |
| Child logs | `<repo>/.sortie/logs/<workflow>.log`, rotated at 10 MiB, 3 files kept. |
| Schema | `schema_version` is 1. Changes are additive: the top-level `cleanup` and `sleep` sections and the workflow field `log_path` were added without changing existing fields. |

### Decisions already made

- `sortie-loop` runs no tracker queries. Sleep depends only on engine state.
- Sleep is disabled by default.
- A paused workflow stays stopped across wakes and supervisor restarts.
- Spend controls use token caps only. There are no per-model rates and no
  dollar computation. `max_usd` is ignored unless the engine records cost.
- The project cap is the sum of the workflow caps.
- `setup` never removes or overwrites existing workflow files.
- Cleanup is a user script run at an interval. It has no tracker-dependent
  logic.
- `sortie-loop` exposes no HTTP API. State is a file and control is by
  commands and signals.
- `sortie-central` has one visibility setting and one password for the whole
  app.
- The Sortie engine is not changed.

Implementation facts:

- `restart_count` counts recycles and wakes only.
- `last_exit_code` is `-1` when the child was killed by a signal.
- A run that ends on a loop failure leaves the final status document with
  state `exited`. A clean exit removes it.
- A sleep stop is a requested stop, so no exit counts as a crash.
- A child whose state query fails counts as busy for sleep.
- `--no-server` runs never sleep, because they have no ports to query.
- `stop --when-idle` and `restart --when-idle` also wait on retrying work.

### Known gaps and unverified items

- No live end-to-end run with real engine children was done for status,
  logs, cleanup interval and timeout, or sleep and wake. Unit tests only.
- `sortie-loop status` shows no cleanup record when no supervisor has
  produced a status document.
- Review findings left unfixed:
  - Pid reuse can make a stale document look running.
  - Temp files are left after SIGKILL.
  - Status write failures only reach stderr.
  - `--repo <value>` is ignored by `status` and by `stop` and `restart`.
- The `/api/v1/state` shape (`counts.running`, `counts.retrying`, `running[]`,
  `retrying[]`) was checked against docs.sortie-ai.com. Dispatch rules and
  stage chains (section 2) are not verified.
- The cleanup script contract expects `removed: <path>` lines on stdout. The
  user's own script has not been checked against it.
- Whether `rule_name` equals the stage name once chains run is unverified.
  The docs show the dashboard's Rule and stage path as rule names joined by
  `->`. What `run_history.rule_name` stores is unknown.

### Working conventions

- Conventional Commits, with the `Co-Authored-By` trailer.
- Build only with `go build -o /tmp/<name>` or run `go test`. A compiled
  binary was once committed by mistake.
- Checks before each commit: `gofmt -l cmd internal` prints nothing,
  `go vet ./...`, `go test -count=1 ./...`, and
  `go test -race ./cmd/sortie-loop/`.
- One branch per feature, stacked on the previous one.
- Push only when the user asks. Fast-forward with
  `git push origin <branch>:main` after `git merge-base --is-ancestor
  origin/main <branch>` succeeds.
- `plans/pm-agent.md` is the user's untracked file and is never committed.
- The lead agent delegates execution to workers per
  `~/.claude/AGENT_ORCHESTRATION.md` and reviews diffs itself.
- An independent reviewer pass after a feature found a high-severity defect
  once. Repeat it for supervisor and registry changes.

### Next action

L9-L11 are done and merged. Next: L4/L8 (needs engine/doc verification) or end-to-end live-supervisor checks. Sortie-central items 5-6 remain in the other repo.

## 0. Scope and repo split


| Item                                 | Repo           |
| ------------------------------------ | -------------- |
| 1. Idle sleep and scheduled wake     | sortie-loop    |
| 2. Workflow consolidation and chains | sortie-loop    |
| 3. Retention and cleanup             | sortie-loop    |
| 4. Workflow spend controls           | sortie-loop    |
| 5. Dashboard control plane           | sortie-central |
| 6. Form-based editing                | sortie-central |


`sortie-central` is a separate repo. It interacts with `sortie-loop` as
follows (requests L1-L12 are listed in section 7):

- State: the L1 status document and the L3 registry entries.
- Control: the L2 commands. Signals stay for stop and restart.
- Run data: the workflow SQLite DBs, read-only, under the L4 compatibility rules.
  Derived fields follow L5 and L6.
- Applying changes: L9 validation and L10 safe apply.

`sortie-loop` exposes no HTTP API. The L1 status document is a file.

## 1. Idle sleep and scheduled wake

**Goal.** Stop the engine children when there is no work, and start them
again later. An idle project costs no engine processes.

**Design.**

- `sortie-loop` runs no tracker queries. The tracker may change, and no
per-tracker adapters are built. Sleep depends only on engine state.
- The supervisor (`supervise.go`) stops all engine children after
`idle_timeout` with no running work and no retrying work. Running and
retrying counts come from `GET /api/v1/state` on each child (verified
against the HTTP API reference: `counts.running`, `counts.retrying`, and
`running[]` and `retrying[]` arrays). The supervisor polls every 30 seconds (fixed). A
  child with running or retrying work, or one that cannot be queried, is
  busy. The idle clock restarts on every busy observation and whenever
  the children are started.
- Wake triggers:
  - `wake_interval` elapsed.
  - A manual trigger: `sortie-loop wake [--json]` (L2), which
  `sortie-central` also calls from its dashboard button. The command
  writes a request file next to the status document. The supervisor reads
  it on its 2-second tick, and the command waits up to 5 seconds for
  `sleep.awake`. Its JSON result has `woke`, `was_sleeping` and `error`.
- While sleeping, new issues and PRs wait until the next wake.
- `sleep.enabled: false` keeps always-on behaviour. It is for users who do
not want to wait for agents to start. The default is `false`.
- Interaction with remote control: `stop` ends the supervisor. `restart`
stops children and starts them again, clearing sleep. A manual wake while
awake does nothing.
- Status fields (L1): the top-level `sleep` object with `enabled`,
`awake`, `sleeping_since`, `next_wake` and, when enabled, `idle_since`.
Workflow entries have state `sleeping` while the run sleeps.

```yaml
sleep:
  enabled: false
  idle_timeout: 15m
  wake_interval: 1h
```

**Acceptance criteria.**

- With `sleep.enabled: true`, children stop after `idle_timeout` with no
running and no retrying work, and the supervisor stays running.
- Running or retrying work blocks sleep.
- `wake_interval` wakes the children.
- `sortie-loop wake` wakes the children.
- `restart` and `stop` behave as before when sleeping.
- The status document reports the sleep state.
- `sleep.enabled: false` keeps today's behaviour.

## 2. Workflow consolidation and chains

**Goal.** Replace the five workflow files with two. Process count drops from
5 to 2.


| File                 | Tracker   | Replaces                   |
| -------------------- | --------- | -------------------------- |
| `WORKFLOW.issues.md` | github    | plan, build                |
| `WORKFLOW.prs.md`    | github-pr | review, review-fix, merge  |


**Engine facts (verified against docs.sortie-ai.com and `sortie validate`).**

- `dispatch.rules[]` has `name`, `match`, optional `agent`, `template`,
a per-agent-kind settings block (model, effort), `stage`, `next`. `match` keys
are ANDed; list entries are ORed. First match wins. `dispatch.default` covers
no match. A catch-all rule must come after the non-staged rules.
- A staged rule carries `stage: <label>` and no `match`: the stage label
selects it. A rule with both is invalid. Two rules cannot share a stage
label. A stage label must differ from every state name and from the
escalation and parking labels.
- `next` names a staged rule. The first rule of a chain has no `stage` and
matches an ordinary label. `next` requires `tracker.handoff_state`, and
`next` links must not form a cycle.
- On success the engine adds the stage label and the next poll dispatches the
next rule. The last rule ends on `tracker.handoff_state`.
- Each stage starts a new session. Stages share one workspace directory per
issue.
- `max_sessions` and `max_tokens` sum across stages.
`dispatch.max_consecutive_hops` limits hops; its default is the larger of 10
and the longest chain.
- `agent.*` budgets and caps are not overridable per rule. Budgets are not
supported per stage.
- Stage chains, per-kind blocks inside a rule and, for the `github` tracker, a
non-empty `terminal_states` come with upstream engine 1.27.0. The pinned
fork engine (`engineTag`, `v1.26.0`) rejects a per-kind block in a rule
and ignores `stage` and `next` with a warning.

### Issue workflow

Implemented design, valid on the pinned engine:

| Rule    | Match                                  | Template           |
| ------- | -------------------------------------- | ------------------ |
| `plan`  | label `agent:plan`                     | `prompts/plan.md`  |
| `build` | label `agent:build` or `agent:plan-build` | `prompts/build.md` |
| `docs`  | label `agent:docs`                        | `prompts/docs.md`  |


- `agent:plan` is plan only. The plan is posted as an issue comment, and the
issue is handed off with `agent:done` for a human.
- `agent:build` runs the build prompt.
- `agent:plan-build` runs the build prompt, which posts the plan as an issue
comment first when the issue has none, then implements in the same session.
There is no human step.
- The plan prompt states that the plan is posted as an issue comment. The
build prompt states that the build reads the issue's recent comments (the
plan comment and later human comments) before starting.
- Rule order settles an issue with several triggers: `plan` is first, so an
issue with `agent:plan` and `agent:build` is planned first.
- `query_filter` is `label:agent:plan,agent:plan-build,agent:build,agent:docs,in-progress
-label:needs-human`, tracker `github`, `terminal_states: [closed]`, handoff
`agent:done`.
- One set of hooks (clone, `auto/<issue>` branch, commit and push) serves
every rule. The push is skipped when the run committed nothing, so a plan
run creates no branch.
- Per-rule model and effort are not available on the pinned engine. The
`pi.model` setting is workflow-wide.

Chain design for an engine that supports stages and per-rule blocks (not
shipped, because the pinned engine rejects it). A chain needs a separate
staged rule for the build after `plan-build`, because a staged rule cannot
carry `match`:

```yaml
dispatch:
  rules:
    - {name: plan,       match: {labels: ["agent:plan"]},       template: ./prompts/plan.md}
    - {name: plan-build, match: {labels: ["agent:plan-build"]}, template: ./prompts/plan.md, next: build-chain}
    - {name: build,      match: {labels: ["agent:build"]},      template: ./prompts/build.md}
    - {name: docs,       match: {labels: ["agent:docs"]},             template: ./prompts/docs.md}
    - {name: build-chain, stage: stage-build,                   template: ./prompts/build.md}
```

With it the plan prompt must not hand off when the issue carries
`agent:plan-build`, and each rule can carry its own `pi:` or `claude-code:`
block with a model. Adopting it requires re-pinning the fork to a release
based on engine 1.27.0 or later.

### PR workflow

- Dispatch rules are label-driven only, with no stages or chains. Engine
chains cannot branch, and review-fix and merge are separate triggers.
- `review`, `review-fix`, and `merge` are three rules selected by
labels: `agent:review`, `agent:build`, `agent:merge`, in that order.
- The PM agent or a human applies the next label.
- Workflow-wide settings are merged: `max_turns: 10` (review had 3), one
set of hooks for the three rules (merge's `after_create` with a full fetch
and an `origin` remote, the `before_run` heal and unshallow step, and a
fast-forward of the `pr-<n>` branch to the current PR head because the rules
share one workspace), `handoff_evidence: off`, `terminal_states: []`.
- `query_filter` is `label:agent:review,agent:build,agent:merge,in-progress
-label:needs-human`.

### New workflows

- **Documentation enhancement:** the `docs` rule above. It updates docs for
merged changes and opens a PR.
- **Dependency and chore (optional suggestion):** label `chore`, bumps
dependencies, runs tests, opens a PR.
- **Test coverage (optional suggestion):** label `coverage`, adds tests for
uncovered code, opens a PR.

### Terminal states

Terminal states trigger workspace cleanup. `handoff_state` must not be an
active or terminal state; `sortie validate` enforces this.


| Workflow | Active                          | Handoff      | Terminal   |
| -------- | ------------------------------- | ------------ | ---------- |
| issues   | open issue with a trigger label | `agent:done` | `[closed]` |
| prs      | open PR with a trigger label    | `agent:done` | `[]`       |


### Budgets

Budgets apply per workflow file, not per rule or stage. Budget controls are
described in section 4.

### Migration

- Labels `agent:plan-build` and `agent:docs` are added to `DefaultLabels`.
- `setup` and the loop startup never change or remove an existing workflow
or prompt file, and the overwrite prompt for them is gone.
- While any legacy file (`WORKFLOW.plan.md`, `WORKFLOW.build.md`,
`WORKFLOW.review.md`, `WORKFLOW.review-fix.md`, `WORKFLOW.merge.md`) exists
in `.sortie/workflows/`, `WORKFLOW.issues.md` and `WORKFLOW.prs.md` are not
written, because both sets would process the same issues. A warning on
stderr names the files. A fresh repo gets the two new files.
- Migration is manual: delete the five old files (and their DB files if the
history is not needed), rerun `setup`, and rename old loop names to `issues`
or `prs` in the `filters:` and `workflows:` keys of `.sortie/config.yaml`.
- Existing installs keep their old `prompts/plan.md` and `prompts/build.md`.
An old `build.md` does not remove `agent:plan-build`, so a finished
`agent:plan-build` issue would be dispatched again. `setup` prints a note
when either file differs from the shipped copy; the user diffs or deletes
them.
- `setup` creates only the labels in `labels:` of an existing
`.sortie/config.yaml`. The user adds `agent:plan-build` and `agent:docs` there;
`setup` prints a note for each shipped label missing from the list.
- The `setup --json` result (L11) lists each skipped workflow file.
- CI does not run `sortie validate`: the stock engine on a CI runner has
neither the `github-pr` tracker nor the `pi` agent. `TestSortieValidate`
runs it where a fork binary is available.

**Acceptance criteria.**

- Two engine children run instead of five.
- An `agent:plan-build` issue is planned and built with no human step.
- An `agent:plan` issue stops after the plan comment.
- The build prompt reads the issue comments before starting.
- Each PR trigger label dispatches the matching rule.
- `setup` leaves existing workflow files unchanged.
- `sortie validate` passes on both files with the pinned engine.

## 3. Retention and cleanup

**Goal.** `sortie-loop` removes old workspaces by running the user's cleanup
script. Cleanup does not depend on issue or tracker state.

**Design.**

- The supervisor runs `command` every `interval`, and on demand as
`sortie-loop cleanup [--dry-run]` (L2).
- `sortie-loop` passes the script its configuration through environment
variables.
- `workspace.retention_days` stays in every workflow as a secondary safety
net. When `cleanup.enabled` is true, `cleanup.max_age_days` must be less
than `workspace.retention_days` in every workflow. This is validated at
startup, and startup fails on a violation.

```yaml
cleanup:
  enabled: true
  interval: 6h
  command: ./scripts/cleanup.sh
  max_age_days: 14
  dry_run: false
  timeout: 10m
```

`command` runs through `sh -c` in the repo root. `timeout` defaults to
`10m`; on expiry the command and its process group are killed and the run
is a failure. The first run is one `interval` after startup. Runs never
overlap, including a manual run: the second one is skipped or rejected.
Captured stdout and stderr are limited to the last 4 KB.

**Command contract.**

- Arguments: none.
- Environment variables:
  - `SORTIE_CLEANUP_DRY_RUN`: `1` or `0`.
  - `SORTIE_CLEANUP_MAX_AGE_DAYS`: integer number of days.
  - `SORTIE_CLEANUP_WORKSPACE_ROOT`: workspace root directory.
  - `SORTIE_CLEANUP_DB_DIR`: directory holding the workflow SQLite DBs
  (`.sortie/workflows/`).
- With `SORTIE_CLEANUP_DRY_RUN=1` the script removes nothing and prints what
it would remove.
- It may print `removed: <path>` lines on stdout, one path per line. At
most 200 paths are recorded.
- Exit 0 is success. Any other exit is a failed run.

**Run log.**

- Log every run with start time, duration, exit code, and the paths the
script printed.
- Record the last run time and result in `<hash>.cleanup.json` next to the
status file, for scheduled and manual runs.
- Status fields (L1), in the top-level `cleanup` object: `last_run_at`,
`duration_ms`, `exit_code`, `dry_run`, `paths`.
- `sortie-loop cleanup [--dry-run] [--json]` prints `started_at`,
`duration_ms`, `exit_code`, `dry_run`, `command`, `output_tail`, `error`.

**Acceptance criteria.**

- The script runs every `interval` with the environment variables set.
- `--dry-run` and `dry_run: true` pass `SORTIE_CLEANUP_DRY_RUN=1`.
- A non-zero exit is logged as a failed run and does not stop the
supervisor.
- Startup fails when `max_age_days` is not less than `retention_days`.
- The status document reports the last run.

## 4. Workflow spend controls

**Goal.** Cap token use per workflow per week or month. The project cap is
the sum of the workflow caps. There is no separate project cap key.

**Facts from the sortie SQLite DBs in `.sortie/workflows/`.**

- `run_history` stores `input_tokens`, `output_tokens`, `total_tokens`,
`cache_read_tokens`, `tokens_measured`, `agent_adapter`, `rule_name`,
`status`, `started_at`, and `completed_at`.
- No table has a dollar cost column.
- The model name is only in `session_metadata.model_name`.
- `aggregate_metrics` holds totals.
- `budget_hold_notices` holds budget holds.

**Design.**

- The enforced control is a token cap: `max_tokens` per period per
workflow. The key under `workflows:` is the loop name (the
`WORKFLOW.<name>.md` stem). A name without a workflow file is a startup
error. A budget without `max_tokens` is off.
- Dollar caps apply only when the engine records dollar cost per run. The
plan does not define input, output, or cache rates per model and does not
compute dollars from rates. `max_usd` is optional and is ignored with a
startup warning.
- Tokens used are the sum of `total_tokens` in `run_history` for runs whose
`completed_at` (or `started_at`, when it is empty) is in the current period.
Runs where `tokens_measured` is 0 are counted as unmeasured and reported.
Their tokens are not estimated. A missing table or column counts as zero.
The engine writes timestamps as RFC3339 in UTC; a timestamp without a zone
is read as UTC. When a DB cannot be read, the last figures are kept and one
line is printed.
- The supervisor reads `run_history` read-only from each workflow DB every
`poll_interval`, on its 2-second tick.
- At `warn_percent` of `max_tokens` it prints one line to stderr per period
and sets `warned` in the status document.
- At the cap it applies `on_exceed: pause` (the only accepted value): the
supervisor stops that workflow's engine child as a requested stop, so the
exit does not end the run. Other workflows keep running. Idle sleep does
not start a paused workflow at wake.
- The child stays stopped until period rollover or a manual control.
`sortie-loop pause <workflow>`, `resume <workflow>`, and
`budget override <workflow>` (L2) are the manual controls. `budget override`
clears a budget pause and turns the cap off until the next period. `resume`
of a workflow that is at or over its cap behaves the same way, because the
loop would otherwise be paused again at the next poll. `pause` of a workflow
that is paused by its budget turns it into a manual pause. A manual pause is
kept across periods and restarts.
- A budget pause also ends at the next poll when the tokens used are below
the cap again, for example after `max_tokens` was raised and the supervisor
restarted.
- Each command writes its own request file
(`<hash>.budget.request.<time>-<pid>`) next to the status file. The
supervisor reads and deletes the files in name order on every 2-second tick,
also when sleep is disabled, and the command waits up to 5 seconds for the
status document to show the change.
- State (period start, paused, paused by, override, warned) is stored in a
JSON file `<hash>.budget.json` next to the status file, so it survives
restarts. Tokens used and unmeasured run count are recomputed from the DBs.
- Overshoot is bounded by `poll_interval` plus the tokens of runs in flight
when the cap is hit. The cap is not a hard limit.
- Periods: `weekly` uses the ISO week, starting Monday. `monthly` uses the
calendar month. Boundaries use `timezone` (IANA name, default UTC). At
rollover the override and the warning reset, and a budget pause ends.
- The engine's `budget_exhausted` field and `budget_hold_notices` are not
used. How they relate to this control is an open question.
- Status fields (L1), per workflow: state `paused` (above `sleeping`) and a
`budget` object with `period`, `period_start`, `tokens_used`, `cap`,
`warn_percent`, `unmeasured_runs`, `paused`, `paused_by`, `override` and
`warned`. The object is present when the workflow has a budget or is paused.

```yaml
workflows:
  issues:
    budget:
      period: weekly
      timezone: Africa/Johannesburg
      max_tokens: 20000000
      warn_percent: 80
      on_exceed: pause
      poll_interval: 1m
      # max_usd: 50   # ignored unless the engine records cost per run
```

**Acceptance criteria.**

- Tokens per period are computed per workflow from `run_history`.
- Runs with `tokens_measured` of 0 are reported as unmeasured.
- A warning is printed at `warn_percent`.
- At the cap, the workflow's engine child is stopped and the run continues.
- The child restarts at rollover and on `resume` or `budget override`.
- State survives a supervisor restart.
- `max_usd` produces a startup warning when no cost data exists.

## 5-6. sortie-central (summary)

A separate repo. This section lists scope only. The page layout follows
Warren (github.com/jayminwest/warren), which is a reference only.

**Pages.**

- Operations: overview of all projects and loops, sleep and pause state,
with a wake button per loop.
- Runs: list with filters by project, workflow, state, date.
- Run detail: stage timeline, events, tokens, budget, git delivery and PR
links.
- Usage and spend: per issue/run, per workflow and period, token caps with
progress.
- Workflows: dispatch rules and chains drawn as stages.
- Projects.
- Agents.

**Access.**

- Visibility is one setting for the whole app.
  - Public: anyone can view runs, costs, and all read pages without a
  password.
  - Not public: all pages are password-gated.
- Viewing or modifying settings (global config, agents, workflows, caps,
pause/resume) requires authentication when a password is set. When no
password is set, the app is fully open.
- The password is stored in the env file (`.env`). The entry is documented
in `.env.example`.
- Sessions use an encrypted, signed, HttpOnly, SameSite cookie with an
expiry.
- Login is rate limited. Mutations have CSRF protection.

**Form-based editors.** Agents, global config, and workflows. Form fields map
to YAML front matter. Each save:

1. Validates with `sortie validate`.
2. Shows a diff preview.
3. Writes a backup.
4. Applies by restart or reload, with rollback to the backup.

**Dependencies on sortie-loop.** `sortie-central` uses the requests in
section 7. The phase of `sortie-central` that needs each request is given in
the "Needed by" column.

## 7. Requests from sortie-central

The requests below come from the `sortie-central` plan. The "Needed by"
column refers to `sortie-central` phases C1-C8, which are defined in that
repo's plan.

No change to the Sortie engine is made. `sortie-central` reads run data from
the existing SQLite schema and derives what it needs (L4-L6). L7 is the only
engine request and is deferred.

| ID  | Request | Owner | Needed by |
| --- | ------- | ----- | --------- |
| L1  | Status document: one machine-readable status per supervisor, a file at `<user cache dir>/sortie-loop/status/<hash>.json`, keyed by a hash of the repo root and referenced from the registry entry as `status_path` (the supported way for central to find it); it remains after a failed run with state `exited`; read through `sortie-loop status --json`, which adds a `liveness` field. Fields: version, engine version, tested schema versions (L4), pid, start time, repo; per workflow child: name, port, pid, state, restart count, last exit code; sleep state (`sleep` object: enabled, awake, sleeping since, next wake, idle since); budget state per workflow (period, used, cap, unmeasured run count, paused, override); last cleanup run (`cleanup` object: `last_run_at`, `duration_ms`, `exit_code`, `dry_run`, `paths`); config hash of what each child runs. | `sortie-loop` | C2, C3, C6 |
| L2  | Control commands beyond signals: `sortie-loop wake [--json]`, `pause <workflow>`, `resume <workflow>`, `budget override <workflow>`, `cleanup [--dry-run]`. Each returns a JSON result and exits non-zero on failure. Signals stay for stop and restart. | `sortie-loop` | C3, C6 |
| L3  | Registry entry additions: workflow file path, loop type, `sortie-loop` version, supervisor start time, and the `status_path` of the status document, added to the existing `repo_name`, `updated_at`, `pid`, and `loops` (port and `db_path`). | `sortie-loop` | C1 |
| L4  | DB compatibility: `sortie-central` pins the supported `schema_migrations` versions, detects columns with `PRAGMA table_info`, opens the DBs read-only, and hides a feature when its column is missing. `sortie-loop` lists the schema versions it was tested against, next to the engine version, in the L1 document. | `sortie-loop`, `sortie-central` | C4, C5, C6 |
| L5  | Run attributes derived by `sortie-central` (table below). | `sortie-central` | C4, C5 |
| L6  | Run timeline derived by `sortie-central` (below). | `sortie-central` | C5 |
| L7  | Cost per run: the engine records dollar cost per run when the adapter reports it, with a flag for estimated or reported. Deferred until the engine provides cost. Tokens remain the unit. Same item as the `max_usd` open question. | `engine (deferred)` | C6 |
| L8  | Run log access: `sortie-loop` writes engine stdout and stderr to `.sortie/logs/<workflow>.log` with size rotation: the file is rotated at 10 MiB and 3 rotated files are kept (`.log.1` to `.log.3`). The terminal output is unchanged, and the status document has `log_path` per workflow. Implemented. The per-run transcript part of L8 is not implemented. | `sortie-loop` | C5 |
| L9  | `sortie-loop validate [--json]`: runs `sortie validate` on all workflow files and checks repo config and the sleep, cleanup, and budget rules (for example `cleanup.max_age_days` less than `workspace.retention_days`). | `sortie-loop` | C7 |
| L10 | Safe apply: `sortie-loop restart --when-idle` for workflow changes and a reload for repo config. Both return success or failure. The L1 status document carries the config hash of what each child runs. | `sortie-loop` | C7 |
| L11 | `sortie-loop setup --json`: non-interactive setup that reports what was created, skipped, and why (labels, workflow files, `.env.loop`), and fails clearly on a bad token, missing repo, or missing permissions. | `sortie-loop` | C8 |
| L12 | Windows control parity (optional): remote control by signals does not work on Windows. If Windows is a target, the L2 commands cover stop and restart. | `sortie-loop` | C3 |

### Existing schema (L4)

All six workflow DBs share one schema, `schema_migrations` version 16. Only
the dev and plan DBs contain rows.

- `run_history` has one row per attempt. Columns: `id`, `issue_id`,
`identifier` (bare number, for example `1`), `attempt`, `agent_adapter`,
`workspace` (per-run path), `started_at`, `completed_at`, `status`
(`succeeded` or `failed`), `error` (failure reason text), `workflow_file`,
`turns_completed`, `display_identifier` (`owner/repo#number`),
`review_metadata` (null in all rows), `rule_name`, `template_id` (prompt
path), `input_tokens`, `output_tokens`, `total_tokens`, `cache_read_tokens`
(a subset of input), and `tokens_measured`.
- `rule_name` is `default` on dispatched runs and empty on runs without
dispatch.
- `run_history` has no stage, model, cost, branch, PR URL, or session count
column.
- `session_metadata` has `issue_id` as its primary key. It holds one row per
issue, overwritten on each session. Columns include `model_name`,
`session_id`, token counts, `api_request_count`, and
`api_requests_measured`.
- `parked_issues`, `retry_entries`, `budget_hold_notices`,
`handoff_absence_resets`, and `reaction_fingerprints` are keyed by
`issue_id` and show current state, not history. `aggregate_metrics` holds
totals.

### Derived run attributes (L5)

| Field | Derived from |
| ----- | ------------ |
| Issue or PR number and URL | `display_identifier` (`owner/repo#number`) |
| Stage | `rule_name`. Rule names equal stage names. Not confirmed until a chain has run. |
| Model | `model` setting of the matching per-kind and per-rule block in the workflow file. This is the configured model, not the observed model. |
| Session count | `attempt` and `turns_completed` |
| Failure reason | `error` |
| Branch and PR URL | GitHub, by branch or issue; or the `workspace` path while it exists |
| Model history for past runs | Not available, because `session_metadata` is overwritten per issue |

### Derived run timeline (L6)

The timeline combines:

- `run_history`: start, end, status, and attempt per run.
- Current-state tables: parked, retry, and budget hold rows.
- GitHub label and comment changes.
- The L8 child logs (no per-run transcript).

There are no per-event token deltas. A full event stream needs an engine
change and is an open question.

## 8. Phasing and dependencies


| Phase | Item                                              | Depends on                                  | Status |
| ----- | ------------------------------------------------- | ------------------------------------------- | ------ |
| 1     | Cleanup (3); L1, L3; L2 `cleanup`                 | none                                        | done |
| 2     | Idle sleep (1); L2 `wake`; L1 sleep fields        | `/api/v1/state` verified                    | done |
| 3     | Workflow consolidation (2)                        | engine facts verified                       | done; stage chains wait for an engine re-pin |
| 4     | Spend controls (4); L2 `pause`, `resume`, `budget override`; L1 budget fields | none | done |
| 5     | L8; L4 tested schema versions in the L1 document  | L1                                          | L8 child logs done; L8 transcript and L4 tested schema versions not started |
| 6     | L9, L10, L11; L12 (optional)                      | 1, 2, 3, 4 (rules and workflow files), L1   | not started |
| 7     | sortie-central UI (5, 6)                          | 1-6                                         | not started |

L1 and L3 were built first, in phase 1 alongside cleanup. The L1 fields for
sleep were added in phase 2. The budget fields are added in phase 4 with the
feature they report. L2 commands are added with the feature each controls.
L8 follows, then L9-L11. L9-L11 can be done in parallel with phase 3. L5 and
L6 are built in `sortie-central`. L7 is not scheduled.


## 9. Open questions

- Does the engine enforce `workspace.retention_days`?
- What are the semantics of `budget_exhausted` and `budget_hold_notices`,
and how do they relate to the per-workflow token cap?
- Can the engine record dollar cost per run? This is required for
`max_usd` and is request L7, which is deferred.
- Does `run_history.rule_name` equal the rule name or the stage label once
chains run? The dashboard shows the stage path as rule names joined by `->`.
- Are past-run model history and a full event stream needed enough to justify
an engine change later?
- Does the pi adapter write a per-run transcript file in the workspace? This is unverified. Per-run transcript access (the rest of L8) depends on it.
- Is the branch still available after cleanup deletes workspaces?
- Windows control parity (L12) is optional and depends on whether Windows is
a target.
- Password and TLS exposure details for a public `sortie-central`
deployment.
