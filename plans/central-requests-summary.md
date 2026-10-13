# sortie-loop: what sortie-central can rely on

Details and request IDs (L1-L12) are in `plans/enhancements.md`, section 7.

## What sortie-loop is

`sortie-loop` is a supervisor that runs one Sortie engine child process per
workflow file in a repository. If one child exits, the supervisor stops the
whole run; automatic restart of a crashed child is not implemented. It is
controlled by signals today, and registers each running supervisor in a
registry entry.

## What sortie-loop provides

- L1 status document: `sortie-loop status --json`, also served on a localhost
endpoint or as a file referenced from the registry entry (chosen at
implementation). The file is `<user cache dir>/sortie-loop/status/<hash>.json`,
keyed by a hash of the repo root; the `status_path` field of the registry
entry is the supported way for central to find it. The document remains
after a failed run, with state `exited`. Proposed key fields (names are not final):
  - `version`, `engine_version`, `schema_versions_tested`, `pid`,
  `started_at`, `repo`
  - `workflows[]`: `name`, `port`, `pid`, `state`, `restart_count`,
  `last_exit_code`, `config_hash`
  - `sleep`: `awake`, `sleeping_since`, `next_wake`
  - `budget` per workflow: `period`, `used`, `cap`, `unmeasured_runs`,
  `paused`, `override`
  - `cleanup`: `last_run_at`, `duration`, `exit_code`, `paths_removed`
- L2 control commands: `wake`, `pause <workflow>`, `resume <workflow>`,
`budget override <workflow>`, `cleanup [--dry-run]`. Each prints a JSON result
and exits non-zero on failure.
- L3 registry additions: workflow file path, loop type, `sortie-loop` version,
supervisor start time, status endpoint.
- L8 child logs: `.sortie/logs/<workflow>.log`, size-rotated.
- L9 `sortie-loop validate [--json]`: runs `sortie validate` on all workflow
files and checks sleep, cleanup, and budget rules.
- L10 safe apply: `restart --when-idle` for workflow changes, and a reload for
repo config. The `config_hash` field shows what each child runs.
- L11 `sortie-loop setup --json`: reports what was created, skipped, and why.
- L12 (optional): L2 commands cover stop and restart on Windows.

## What central derives from the existing DBs

All workflow DBs (`.sortie/workflows/.sortie-<workflow>.db`, one hidden file per workflow) share one schema,
`schema_migrations` version 16. Open them read-only, pin the supported
versions, check columns with `PRAGMA table_info`, and hide a feature when its
column is missing.

| Field | Derivation |
| ----- | ---------- |
| Issue or PR number and URL | `run_history.display_identifier` (`owner/repo#number`) |
| Stage | `run_history.rule_name`. It is `default` on dispatched runs and empty on runs without dispatch. Rule names are expected to equal stage names; unconfirmed until a chain has run. |
| Model | `model` setting in the workflow file for the matching kind and rule. Configured, not observed. |
| Session count | `attempt` and `turns_completed` |
| Failure reason | `run_history.error` |
| Branch, PR URL | GitHub, by branch or issue; or `run_history.workspace` while the directory exists |
| Timeline | `run_history` (start, end, status, attempt) plus current-state tables plus GitHub label and comment changes plus L8 logs |

Schema facts:

- `run_history` has one row per attempt, with token columns
(`input_tokens`, `output_tokens`, `total_tokens`, `cache_read_tokens`,
`tokens_measured`). It has no stage, model, cost, branch, PR URL, or session
count column.
- `session_metadata` has one row per issue (`issue_id` is the primary key) and
is overwritten on each session. `model_name` from it is the latest session
only.
- `parked_issues`, `retry_entries`, `budget_hold_notices`,
`handoff_absence_resets`, and `reaction_fingerprints` show current state, not
history.

## Not available

- Cost in dollars. Tokens are the only unit. Token caps are enforced.
`max_usd` is ignored with a startup warning.
- Per-run model history for past attempts.
- A full event stream and per-event token deltas.

## What central displays

- Sleep state, and next wake (`sleep`), with a wake button (L2 `wake`).
- Budget state per workflow, including paused and override flags and the
unmeasured run count.
- Last cleanup run and the paths it removed.

## Access rules

- Central reads the DBs read-only.
- Controls use the L2 commands. Stop and restart use signals.
- `sortie-loop` has no HTTP API beyond the status endpoint.

## Implementation order (sortie-loop)

1. Cleanup, L1, L3, L2 `cleanup`.
2. Idle sleep, L2 `wake`, L1 sleep fields.
3. Workflow consolidation (two workflow files).
4. Spend controls, L2 `pause`, `resume`, `budget override`, L1 budget fields.
5. L8 logs and tested schema versions in L1.
6. L9, L10, L11, optional L12.

## Open questions affecting central

- Whether `rule_name` equals the stage name once chains run.
- Whether past-run model history and a full event stream justify an engine
change later.
- Whether the branch is available after cleanup deletes workspaces.
- Whether the status document is an endpoint or a file.
- Password and TLS exposure for a public central deployment.
