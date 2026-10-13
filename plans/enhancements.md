# Enhancements

Status: draft

Four changes to `sortie-loop` (idle sleep, workflow consolidation, workspace
cleanup, spend controls), a summary of two features that belong in
`sortie-central` (dashboard control plane, form-based editing), and the
requests `sortie-central` makes of `sortie-loop`. The Sortie engine is not
changed. One request (L7, cost per run) is deferred until the engine records
cost.
Statements about Sortie engine behaviour come from summarised documentation and are
marked "to verify against docs before implementing". Unverified items are
listed under Open questions.

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

`sortie-loop` exposes no HTTP API beyond the status endpoint in L1. If L1 is
implemented as a file, it exposes none.

## 1. Idle sleep and scheduled wake

**Goal.** Stop the engine children when there is no work, and start them
again later. An idle project costs no engine processes.

**Design.**

- `sortie-loop` runs no tracker queries. The tracker may change, and no
per-tracker adapters are built. Sleep depends only on engine state.
- The supervisor (`supervise.go`) stops all engine children after
`idle_timeout` with no running work and no retrying work. Running and
retrying counts come from `GET /api/v1/state` on each child (to verify
against docs before implementing).
- Wake triggers:
  - `wake_interval` elapsed.
  - A manual trigger: `sortie-loop wake` (L2), which `sortie-central` also
  calls from its dashboard button.
- While sleeping, new issues and PRs wait until the next wake.
- `sleep.enabled: false` keeps always-on behaviour. It is for users who do
not want to wait for agents to start. The default is `false`.
- Interaction with remote control: `stop` ends the supervisor. `restart`
stops children and starts them again, clearing sleep. A manual wake while
awake does nothing.
- Status fields (L1): `awake`, `sleeping since <time>`, and next scheduled
wake.

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

**Goal.** Replace the six workflow files with two. Process count drops from
6 to 2.


| File                 | Tracker   | Replaces                        |
| -------------------- | --------- | ------------------------------- |
| `WORKFLOW.issues.md` | github    | plan, build                     |
| `WORKFLOW.prs.md`    | github-pr | review, review-fix, merge, test |


**Engine facts (to verify against docs before implementing).**

- `dispatch.rules[]` has `name`, `match`, optional `agent`, `template`,
per-agent-kind settings (model, effort), `stage`, `next`. `match` keys are
ANDed; list entries are ORed. First match wins. `dispatch.default` covers
no match.
- A rule with `stage` and `next` forms a chain. On success the engine swaps
the stage label and the next poll dispatches the next rule.
- Each stage starts a new session. Stages share one workspace directory per
issue.
- One successor per stage. No branching, no back-edges, no cycles. Only
success advances. A blocked stage parks. Failures retry the same stage.
- `max_sessions` and `max_tokens` sum across stages.
`dispatch.max_consecutive_hops` limits hops.
- `agent.*` budgets and caps are not overridable per rule. Budgets are not
supported per stage.

### Issue workflow


| Rule         | Match                     | Stage   | Next    |
| ------------ | ------------------------- | ------- | ------- |
| `plan`       | label `agent:plan`        | `plan`  | none    |
| `plan-build` | label `agent:plan-build`  | `plan`  | `build` |
| `build`      | label `agent:build`       | `build` | none    |
| `docs`       | label `docs`              | `docs`  | none    |


- `agent:plan` is plan only. The plan stage posts the plan, and the issue is
handed off with `agent:done` for a human.
- `agent:plan-build` runs plan then build with no human approval.
- `agent:build` dispatches the build stage directly.
- The plan stage always posts the plan as a comment on the issue.
- The build stage always reads the issue's recent comments before starting,
including the plan comment and any later human comments. Both the plan and
build prompt templates state this requirement.
- Per-stage model and effort use the per-kind settings of each rule, for
example a stronger model for `plan`.

### PR workflow

- Dispatch rules are label-driven only, with no stages or chains. Engine
chains cannot branch, and review-fix and merge are separate triggers.
- `review`, `review-fix`, and `merge` map to three rules selected by
labels: `agent:review`, `agent:build`, `agent:merge`.
- The PM agent or a human applies the next label.

### New workflows

- **Documentation enhancement:** the `docs` rule above. It updates docs for
merged changes and opens a PR.
- **Dependency and chore (optional suggestion):** label `chore`, bumps
dependencies, runs tests, opens a PR.
- **Test coverage (optional suggestion):** label `coverage`, adds tests for
uncovered code, opens a PR.

### Terminal states

Terminal states trigger workspace cleanup. `handoff_state` must not be an
active or terminal state (to verify against docs before implementing).


| Workflow | Active                          | Handoff      | Terminal   |
| -------- | ------------------------------- | ------------ | ---------- |
| issues   | open issue with a trigger label | `agent:done` | `[closed]` |
| prs      | open PR with a trigger label    | `agent:done` | `[]`       |


### Budgets

Budgets apply per workflow file, not per rule or stage. Budget controls are
described in section 4.

### Migration

- Labels stay as they are.
- `setup` never removes or overwrites existing workflow files, because they
are likely modified. It only adds new files that do not exist. The
`setup --json` result (L11) lists each skipped existing workflow file.
- Removing the old six `WORKFLOW.*.md` files is a manual step. After the two
new files are in place and validated, the user deletes the old files and
removes their engine children from the supervisor configuration.
- CI runs `sortie validate` on both new files.

**Acceptance criteria.**

- Two engine children run instead of six.
- An `agent:plan-build` issue reaches build without a human step.
- An `agent:plan` issue stops after the plan comment.
- The build stage reads the issue comments before starting.
- Each PR trigger label dispatches the matching rule.
- `setup` leaves existing workflow files unchanged.
- `sortie validate` passes in CI.

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
```

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
- It prints one removed path per line on stdout.
- Exit 0 is success. Any other exit is a failed run.

**Run log.**

- Log every run with start time, duration, exit code, and the paths the
script printed.
- Record the last run time and result.
- Status fields (L1): last cleanup run time, duration, exit code, and the
paths removed.

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
workflow.
- Dollar caps apply only when the engine records dollar cost per run. The
plan does not define input, output, or cache rates per model and does not
compute dollars from rates. `max_usd` is optional and is ignored with a
startup warning unless cost data exists.
- Runs where `tokens_measured` is 0 are counted as unmeasured and reported.
Their tokens are not estimated.
- The supervisor reads `run_history` from each workflow DB every
`poll_interval`.
- At `warn_percent` of `max_tokens` it sends a notification.
- At the cap it applies `on_exceed: pause`: the supervisor stops that
workflow's engine child. Other workflows keep running.
- The child stays stopped until period rollover or a manual override.
`sortie-loop pause <workflow>`, `resume <workflow>`, and
`budget override <workflow>` (L2) are the manual controls.
- State (period start, tokens used, unmeasured run count, paused flag,
override) is stored in a small SQLite file owned by `sortie-loop`, so it
survives restarts.
- Overshoot is bounded by `poll_interval` plus the tokens of runs in flight
when the cap is hit. The cap is not a hard limit.
- Periods: `weekly` uses the ISO week, starting Monday. `monthly` uses the
calendar month. Boundaries use `timezone`.
- The engine's `budget_exhausted` field and `budget_hold_notices` exist.
How they relate to this control is investigated at implementation time.
- Status fields (L1), per workflow: period, tokens used, cap, unmeasured run
count, paused flag, and override.

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
- A warning is sent at `warn_percent`.
- At the cap, the workflow's engine child is stopped.
- The child restarts at rollover and on manual override.
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
| L1  | Status document: one machine-readable status per supervisor, from `sortie-loop status --json` and the same JSON served on a localhost endpoint or as a file referenced from the registry entry. Fields: version, engine version, tested schema versions (L4), pid, start time, repo; per workflow child: name, port, pid, state, restart count, last exit code; sleep state (awake, sleeping since, next wake); budget state per workflow (period, used, cap, unmeasured run count, paused, override); last cleanup run (time, duration, exit code, paths); config hash of what each child runs. | `sortie-loop` | C2, C3, C6 |
| L2  | Control commands beyond signals: `sortie-loop wake`, `pause <workflow>`, `resume <workflow>`, `budget override <workflow>`, `cleanup [--dry-run]`. Each returns a JSON result and exits non-zero on failure. Signals stay for stop and restart. | `sortie-loop` | C3, C6 |
| L3  | Registry entry additions: workflow file path, loop type, `sortie-loop` version, supervisor start time, and status endpoint, added to the existing `repo_name`, `updated_at`, `pid`, and `loops` (port and `db_path`). | `sortie-loop` | C1 |
| L4  | DB compatibility: `sortie-central` pins the supported `schema_migrations` versions, detects columns with `PRAGMA table_info`, opens the DBs read-only, and hides a feature when its column is missing. `sortie-loop` lists the schema versions it was tested against, next to the engine version, in the L1 document. | `sortie-loop`, `sortie-central` | C4, C5, C6 |
| L5  | Run attributes derived by `sortie-central` (table below). | `sortie-central` | C4, C5 |
| L6  | Run timeline derived by `sortie-central` (below). | `sortie-central` | C5 |
| L7  | Cost per run: the engine records dollar cost per run when the adapter reports it, with a flag for estimated or reported. Deferred until the engine provides cost. Tokens remain the unit. Same item as the `max_usd` open question. | `engine (deferred)` | C6 |
| L8  | Run log access: `sortie-loop` writes engine stdout and stderr to `.sortie/logs/<workflow>.log` with size rotation. The directory does not exist today. | `sortie-loop` | C5 |
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
- The L8 logs.

There are no per-event token deltas. A full event stream needs an engine
change and is an open question.

## 8. Phasing and dependencies


| Phase | Item                                              | Depends on                                  |
| ----- | ------------------------------------------------- | ------------------------------------------- |
| 1     | Cleanup (3); L1, L3; L2 `cleanup`                 | none                                        |
| 2     | Idle sleep (1); L2 `wake`; L1 sleep fields        | `/api/v1/state` verified                    |
| 3     | Workflow consolidation (2)                        | engine facts verified                       |
| 4     | Spend controls (4); L2 `pause`, `resume`, `budget override`; L1 budget fields | `budget_exhausted` semantics |
| 5     | L8; L4 tested schema versions in the L1 document  | L1                                          |
| 6     | L9, L10, L11; L12 (optional)                      | 1, 2, 3, 4 (rules and workflow files), L1   |
| 7     | sortie-central UI (5, 6)                          | 1-6                                         |

L1 and L3 are built first, in phase 1 alongside cleanup. The L1 fields for
sleep and budget are added in phases 2 and 4 with the features they report.
L2 commands are added with the feature each controls. L8 follows, then
L9-L11. L5 and L6 are built in `sortie-central`. L7 is not scheduled.


## 9. Open questions

- Does the engine enforce `workspace.retention_days`?
- What are the semantics of `budget_exhausted` and `budget_hold_notices`,
and how do they relate to the per-workflow token cap?
- Can the engine record dollar cost per run? This is required for
`max_usd` and is request L7, which is deferred.
- Does `rule_name` equal the stage name once chains run?
- Are past-run model history and a full event stream needed enough to justify
an engine change later?
- Is the branch still available after cleanup deletes workspaces?
- Is the status document (L1) a localhost endpoint or a file referenced from
the registry entry? Chosen at implementation.
- Windows control parity (L12) is optional and depends on whether Windows is
a target.
- Password and TLS exposure details for a public `sortie-central`
deployment.
- Engine facts marked "to verify": dispatch rules, chains, and
`/api/v1/state` fields.
