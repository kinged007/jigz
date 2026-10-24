# Jigz: handoff

Status: 2026-10-24, Phase 3 (Questions) implemented on the local branch
`feat/questions`: WP1 to WP8 done. Remaining: the final review and the
PR; the release is not requested. Read this first in a new
session working on Jigz. The hub is a separate stream; its plans are in
`plans/hub/` and it has no code yet.

## 1. Objective

Build Jigz as a free, open-source tool that is complete without
the hub, following `plans/jigz/vision-and-roadmap.md` section 6.
Phase 0 is merged into `main` and pushed to `github.com/kinged007/jigz`.
Phases 1 and 2 are complete in code, tests, and docs. Neither has a
release or release notes; releases are deferred until the user asks. The
rename to Jigz is done, including the repo directory `.sortie/` to
`.jigz/`. CI runs the acceptance test. Phase 3 (Questions) is implemented
on the local branch `feat/questions`; its plan is `plans/jigz/questions.md`.

Read, in order:

1. `plans/system/overview.md`: parts, product boundary (2.1), modes,
   engine policy.
2. `plans/jigz/vision-and-roadmap.md`: principles, engine
   requirements, planned features, roadmap.
3. `plans/jigz/features.md`: existing and planned features by area.
4. `plans/system/delivery-method.md`: how work is verified.
5. `plans/system/hub-protocol.md`: only when working on connected mode.

## 2. Constraints from the user

- Commit each finished, checked piece as a focused Conventional Commit
  (style: `fix(dashboard): ...`). End messages with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Do not push,
  open PRs, or touch `main` unless the user asks.
- Plain, literal prose in docs, comments, and chat. Docs describe the
  current design, not the history of decisions.
- No upselling: the dashboard, CLI, and docs show no locked or "available
  in the hub" features and no links to the hub.
- No authentication in the dashboard. It binds to 127.0.0.1 by default;
  `--host 0.0.0.0` opens it. The Host and Origin checks stay.
- Dashboard scope: the current pages stay; metrics stay local and recent.
  Long history, cross-machine views, and analytics belong to the hub.
- The agent session host (roadmap Phase 6) is internal: not in the user
  docs or the CLI help.
- Engine: the fork `kinged007/sortie` stays the default until upstream
  `sortie-ai/sortie` has the `pi` adapter and the `github-pr` tracker.
  The upstream PRs for both are in progress.
- New tools or dependencies, and CI workflow changes, need the user's
  approval first.
- No releases for now. Do not release unless the user asks.
- Global rules in `~/.claude/AGENT_ORCHESTRATION.md`: the main agent
  delegates exploration, edits, and test runs to workers and reports on
  delegation after each assignment.

## 3. Decisions in effect

- The product boundary table in `plans/system/overview.md` 2.1 decides
  what goes in Jigz and what goes in the hub.
- No cross-machine unite in Jigz; the hub's fleet view covers it.
- Connected mode is runner mode: the hub is the only source of project
  config, and there is no local dashboard.
- Workspace modes (SSH, sandbox) are deferred. Hosted runners are a hub
  backlog item.
- The acceptance harness uses an in-memory fake GitHub API
  (`internal/fakegithub`), not Gitea (reason in `delivery-method.md` 2.3).
- Machine-local settings (working hours, pool entries, engine selection)
  are in `machine.yaml` in the user config directory. Pool state is in
  `pool.json` in the user state directory.
- Runtime failover sets `SORTIE_AGENT_KIND` and `SORTIE_AGENT_COMMAND` on
  one workflow, so the workflow switches agent runtime when the pool moves
  to another entry.
- `hours.end` is a config setting with the values `finish` or `stop`. The
  default is `finish`.
- Names: Jigz is the product; the command is `jig`; the daemon runs as
  `jig daemon`, installed as service `jigd`; the module and the repository
  are `github.com/kinged007/jigz`; the user directories are `jigz` under
  the XDG roots (`~/.cache/jigz`, `~/.local/share/jigz`,
  `~/.local/state/jigz`, `~/.config/jigz`); the fleet config file is
  `jigz.yaml`; the log file is `jig.log`; the dashboard meta name is
  `jig-dashboard`. Shiftd for the hub. The rename is done
  (`plans/jigz/vision-and-roadmap.md` section 6, "Rename to Jigz").
- The engine keeps its own names: the `sortie` binary and the `SORTIE_*`
  variables it owns. The scratch workspace keeps its `.sortie/`
  (`workspacekit.SortieDir`); the repo-local directory is `.jigz/`. The
  user-facing documents do not name the engine; they say "the engine".
- The state directories moved from the `sortie-loop` names to the `jigz`
  names. `internal/dirs` moves a directory left by an earlier install into
  place the first time it is looked up, so the registry, the run history
  and the settings survive the upgrade. The pool state and the dashboard
  state were two directories before the rename and are one now, so the
  second one is merged into the first.
- Fields with no engine API source are not shown in the dashboard:
  workspace path, prompt template, API request count, handoff counter reset,
  and review metadata.
- Engine selection is machine-local (`engine:` in `machine.yaml`). The
  default stays `kinged007/sortie` v1.28.1 until upstream has `pi` and
  `github-pr`.
- The dashboard counts succeeded and failed runs by status name from
  `/api/v1/stats` `by_status`. The engine's `summary.failed` counts every
  non-success status except cancelled, needs_person, and budget_stopped.
- CI (`.github/workflows/ci.yml`) has a separate `acceptance` job that
  runs `make acceptance` on every push to `main` and every PR.
- The default agent runtime is `claude-code` with model
  `claude-haiku-5-5`. The shipped workflow files set `agent.kind:
  claude-code` and `command: claude`; the `pi:` block stays in them. A loop
  whose agent kind the engine lacks does not start; there is no fallback
  from `pi` to `claude-code`. Users switch runtime by editing `agent.kind`
  and `model:` in the workflow files, or through the pool in `machine.yaml`.
- The shipped issues and prs workflows use `db_path: jigz-<name>.db`.
  Discovery lists only `jigz-*.db` files. Legacy `.sortie-*.db` files are
  ignored and not migrated. `jig setup` and `jig setup --json` append
  `jigz-*.db` once to a selective `.gitignore` (one that re-includes parts
  of `.jigz/`).
- `needs-human` is the label for blocking questions. There is no
  `needs-answer` label.
- Releases are deferred until the user asks.

Open decisions (do not decide them; ask): none at the time of writing.

## 4. State of the work

`main` is pushed to `github.com/kinged007/jigz`. These PRs were merged by
squash, with CI green (check, acceptance, four builds):

- #13: workflow databases named `jigz-<name>.db`, discovery of
  `jigz-*.db` only, and the `.gitignore` append in `jig setup`.
- #15: the two fleetconfig example tests set `GH_TOKEN` themselves, because
  CI runners have no token.
- #16: the CI `acceptance` job.
- #17, #19, #21: plans. `needs-human` for blocking questions, engine
  findings, and `plans/jigz/questions.md` as the Phase 3 plan.
- #18: the default agent runtime `claude-code` with `claude-haiku-5-5`,
  and removal of the `pi` to `claude-code` fallback.

PR #12 and issue #14 are closed. Issue #20 is open: the scanner keeps
only answers from authorized authors; the prompts still trust every
comment. PR #10 is open and unrelated to this work.

`gh` defaults to `kinged007/jigz` in this clone (`gh repo set-default`). A
second remote, `upstream`, points to `sortie-ai/sortie`. Never push to it
or open PRs or issues against it.

Engine facts verified at the pinned tag (also in
`plans/jigz/questions.md`): at handoff the engine adds `agent:done` and
removes every other configured state label, including the trigger and
`in-progress`. Other labels, such as `needs-human`, stay. `agent:done`
outranks the trigger. A run that exits 0 is `succeeded`. Poll dispatch
does not pass comments to the prompt.

Phase 3 (Questions) is implemented on the local branch `feat/questions`:
14 commits on top of `main`, not pushed, no PR. The work packages (WP)
are in `plans/jigz/questions.md` section 3. Done and committed on the branch:

| WP | Change |
| -- | ------ |
| WP1 | `internal/questions` parser. An independent review found two blockers; both are fixed and tested. |
| WP2 | `internal/github` Post, Delete and User; `internal/store` tables `question_items` and `questions`; `internal/fleetconfig` keys `questions_interval` and `questions_window`. |
| WP3 | `internal/fakegithub`: comments with ETag, `/user`, assignees, label DELETE and POST, PUT rejected, and the `EngineHandoff` helper. |
| WP4 | `internal/collector/questions.go`: the scanner, the answer path (post the `agent-answer` comment, add the `resume` trigger, remove `agent:done` and `needs-human`), remove-labels for malformed blocks, and `cmd/jig/questions.go` with the `/questions` handlers and `/api/v1/questions`. |
| WP5 | The Questions page (`cmd/jig/templates/questions.html`, the nav entry in `layout.html`). |
| WP6 | The five prompts with the question block and `resume`, the prs workflow comment, and prompt tests. |
| WP7 | The acceptance scenario `f_blocking_question_stops_and_answer_resumes` in `cmd/jig/acceptance_test.go`; `make acceptance` passes. |
| WP8 | README (labels and the Questions section), `features.md` Phase 3 items, and this file. |

The worktrees `/home/coder/sortie-loop-wt/wp1`, `wp2`, `wp3` and `wp6`
(branches `wp/questions-1` and so on) can be removed after the PR.

Phase 1 (working hours, account pool,
`agent-exec`, runtime failover, dashboard pages) is in `internal/machine`,
`cmd/jig/gate.go` and `cmd/jig/agentexec.go`. Phase 2 commits:

| Commit | Change |
| ------ | ------ |
| `b69906d` | `internal/store/outcome.go` maps runs to the classes `succeeded`, `failed_agent`, `failed_workspace`, `failed_limit`, `cancelled`, `timeout`, `handoff_withheld`. The account-limit pattern is `machine.BuiltinLimit`. The CSV outcome column and the API `error_class.key` carry the class key. |
| `6940822` | `internal/engine` has one resolver with a configurable repository and version: the `engine:` block in `machine.yaml`, overridden by `JIG_ENGINE_REPO` and `JIG_ENGINE_VERSION`; `JIG_ENGINE_BIN` wins. Cache files are named `sortie-<owner>+<name>-<version>`. |
| `250d045` | `internal/collector/api.go` reads runs from `/api/v1/runs` (paged by run id, filtered by `workflow_file`), events from `/api/v1/state`, and rollup counts from `/api/v1/stats` `by_status`. A SQLite fallback serves stopped loops; its schema range is 18 to 43, with `rollup.EngineReleases` {1.28.1: 38, 1.29.0: 43}. Store migration 8 drops `workspace`, `template_id`, `api_requests`, `handoff_reset`, and `review_metadata`. A replaced engine database with a lower newest run id re-copies that workflow's runs. |
| `59d2ba2` | Capability detection (`internal/engine/probe.go`, `gate.go`, `internal/plan/gating.go`, `cmd/jig/capabilities.go`). The version comes from `-dumpversion`; kinds come from `validate` on a workflow with unknown kinds. Without `github-pr` the PR loop is disabled (state `disabled`, reason in `hold`). Engine below 1.27.0 runs no loops. A loop whose agent kind the engine lacks does not start and is reported as an error. Pool entries for a missing runtime are not picked. One alert is shown per disabled feature. Settings lists the kinds. The status document `engine_version` is the binary's version. |
| `01a0ad0` | The dashboard reads parked and budget-hold alerts from `/api/v1/state` while the engine runs. Settings shows the schema range. |
| `3accb2b` | `TestAcceptanceUpstreamEngine` runs upstream `sortie-ai/sortie` v1.29.0 selected through `machine.yaml`: the issue is handed off, the PR loop is disabled with no PR requests, `validate` passes, the alert and kinds are shown, and a run arrives through the API. The Makefile acceptance target runs both tests. |

The rename to Jigz is on `main`:

- The command package moved to `cmd/jig`, the module path is
  `github.com/kinged007/jigz`, and the plans moved to `plans/jigz/`.
- The tool-owned environment variables are `JIG_*`: `JIG_TOKEN`, `JIG_REPO`,
  `JIG_ONLY`, `JIG_WORKSPACES`, `JIG_MILESTONE`,
  `JIG_ASSIGNEE`, `JIG_BIN`, `JIG_ENGINE_REPO`, `JIG_ENGINE_VERSION`,
  `JIG_ENGINE_URL`, `JIG_ENGINE_SHA256`, `JIG_ENGINE_BIN`, `JIG_CLEANUP_*`,
  `JIG_MODEL`, `JIG_MODEL_BUILD`, `JIG_MODEL_REVIEW` and `JIG_TEST_*`. The
  `SORTIE_*` variables the engine reads keep their names.
- The shipped workflow files build the clone URL from the tracker project
  the engine already has, because a hook only sees the variables with the
  engine's prefix.
- The user directories are `jigz` (`~/.cache/jigz`, `~/.local/share/jigz`,
  `~/.local/state/jigz`, `~/.config/jigz`), and `internal/dirs` moves the
  old `sortie-loop` directories into them on first use.
- `grep -rni 'central' cmd internal` returns nothing, so the earlier name
  is gone from the code as well. `sortie-loop` appears only in
  `internal/dirs`, where it is the name of the directory the migration
  reads.

Outside this repository:

- The release `v1.28.1` of `kinged007/sortie` had no assets. The
  linux and darwin archives (amd64, arm64) and `checksums.txt` were built
  locally from the tag and uploaded. Windows archives are not attached,
  because the official ones are signed through SignPath.
- PR https://github.com/kinged007/sortie/pull/7 (open, not merged) makes
  the fork's Release workflow skip jobs that need upstream-only secrets,
  skip Windows signing, and leave Windows out of fork releases. It is not
  run yet; the workflow has a `dry_run` input for a test.
- `/home/coder/sortie` has refs `refs/upstream-tags/*` (upstream tags;
  the local tags v1.26.0, v1.27.0, and v1.28.1 point at fork commits). Its
  working tree is the user's `feat/pi-adapter` branch: read it only with
  `git show <ref>:<path>`.
- Upstream engine v1.29.0 is cached at
  `~/.local/share/jigz/sortie-sortie-ai+sortie-1.29.0`.

## 5. Checks

- CI on `main` is green after the merges of #13 to #21: check, acceptance,
  and the four cross-builds.
- `make verify` passed on `feat/questions` with no token set: gofmt,
  `go vet` (also with `-tags acceptance`), build, `go test ./...`, and both
  acceptance tests (about 3 minutes in total). The acceptance tests
  download each engine (about 25 MB) on first run per machine and fail if
  the download fails.
- `TestAcceptance` runs the real `jig` binary and engine v1.28.1.
  Scenarios: an `agent:plan` issue is claimed and handed off;
  `needs-human` and other-assignee issues are not dispatched; the dashboard
  API reports the run; no claims outside a working-hours window and a claim
  after it opens; a usage limit on pool entry A moves the retry to entry B
  and A stays exhausted until the reset epoch. It fails if the fake GitHub
  API receives an unknown route, parameter, or search qualifier.
- Browser checks: obscura refuses loopback addresses and Playwright is not
  installed for this Go repo. Page checks were done with Go-side HTTP
  fetches, in the acceptance tests and in an isolated instance on free
  ports.

## 6. Outstanding work

1. The final review pass over the Phase 3 diff on `feat/questions`
   (review2 in `plans/jigz/questions.md` section 3, WP8).
2. One PR for Phase 3 with the focused commits (the user asks for pushes
   and PRs). Then remove the worktrees under `/home/coder/sortie-loop-wt/`.
3. Phase 4 is not started.
4. Phase 1 and Phase 2 releases and release notes: deferred; only when
   the user asks.

Issue #20 (trust only comments from authorized authors) is open: the
scanner filters authors (`internal/collector/questions.go`), the prompts
do not.

The upstream PRs for the `pi` adapter and the `github-pr` tracker are in
progress and owned by the user.

Known gaps from Phase 2, not scheduled:

- `failed_limit` is recognised only when the limit text reaches the
  engine's error (agent result text, or stderr on early exit). A custom
  limit pattern in `pool.json` is not visible to the classifier.
- History holds raw error text for runs copied from SQLite and API
  sentences for runs copied from the API.
- Capabilities are probed once per supervisor process; a pin change needs
  a restart.
- `codex` and `opencode` run only with `agent.kind` and `agent.command`
  both set; there is no pool runtime, default command, or shipped block
  for them.
- A replacement engine database whose ids already exceed the stored last
  run id is not detected.
- The PR-workflow-off alert is also shown for projects started with
  `JIG_ONLY=issues`.
- `fillRunFiles` (the `workflow_file` backfill) runs only on the SQLite
  path.
- Existing installs download the default engine once more: the cache
  directory moved from `~/.local/share/sortie-loop` to
  `~/.local/share/jigz`, and the old `sortie-<version>` files are not
  removed. The directory itself is moved, not copied, so the download
  happens only for an engine that was never installed.

Known gaps from Phase 1, not scheduled:

- Engine v1.28.1 runs a credential check through the agent command before
  each worker run, so a usage limit is usually found there: the run fails,
  posts no comment, and retries after about 10 s. A usage limit that occurs
  mid-turn posts a failure comment.
- With `max_concurrent_agents` greater than 1, a loop can claim work between
  window close and stop.
- Acceptance covers `end: finish` only; `end: stop` is covered by
  supervisor integration tests with a fake engine.
- The agent-exec "no entry until" time uses the global next change, not a
  time per runtime.
- The limit detection patterns are not checked against real `claude` or
  `pi` output.
- The supervisor's closed-gate check makes one blocking state request per
  loop per tick, with a 3 s timeout.
- A dashboard started while offline keeps the PATH fallback engine until
  restart, because the download is attempted once per process.
- No acceptance scenario for the PR workflow (`github-pr`) on the fork
  engine; the fake serves its routes and has unit tests.
- GitHub enrichment in the collector (PR timeline, commits, check runs)
  never runs: nothing sets `app.github`.
- The collector copies runs every 60 s and the interval is not
  configurable.
- A dashboard stopped by a signal while a `validate` child hangs may leave
  the child running. Not checked with the real engine.
- A target repository installed before the directory rename keeps its
  files: `config.MigrateRepoDir` renames `.sortie/` to `.jigz/` on first
  lookup, when `.jigz` is missing. A repo with both directories keeps
  `.jigz` and leaves the other alone. The rename does not touch file
  contents, so a workflow file installed before it still reads
  `$SORTIE_LOOP_WORKSPACES` and `$SORTIE_LOOP_CLONE_URL`, which
  `internal/config` no longer sets. The workspace root then expands to
  empty and the engine fails its dispatch preflight with
  `workspace.root is not writable: /issues`. `jig setup` does not
  overwrite an existing `WORKFLOW.*.md`; it reports the file as
  differing from the shipped copy, and the fix is to delete the stale
  file and re-run setup, which restores the shipped copy. Local edits to
  a workflow file do not survive that, and the `pi:`/`claude-code:`
  `model:` value has to be reapplied.
- `plans/jigz/enhancements.md` and `production-readiness.md` refer
  to `plans/pm-agent.md`, which does not exist in the repository.
- Target repositories installed before Phase 1 have workflows without the
  `claude-code:` block; the runtime switch to claude-code needs
  `jig setup` re-run there.

## 7. Processes on this machine

No dashboard or loop process was listening on ports 7677 to 7680 on
2026-10-24. Both cache directories exist: `~/.cache/sortie-loop/` (before
the rename) and `~/.cache/jigz/`. Check with `ss -ltnp` before starting a
dashboard, because an earlier instance used 7677 with engine children on
7679 and 7680. Tests isolate `HOME` and `XDG_*` to temporary directories;
keep doing that.

Installed binaries: `~/.local/bin/jig` is a local build from 2026-10-23
and is older than `main`. There is no `make install` target; build it with
`go build -o ~/.local/bin/jig ./cmd/jig`. `~/.local/bin/sortie-loop` is
the binary from before the rename.

## 8. Lessons for routing work

- Research before building when an external system is involved: the
  research ruled out Gitea before any harness code was written.
- Research the engine source before designing a wrapper. It changed the
  design twice: env overrides, retry, and the credential check in Phase 1;
  and in Phase 2 the API has no machine-readable error kind, kinds are
  discoverable only through `validate` errors, and `/runs` `ended_since` is
  not a run cursor.
- Include a browser check of the affected pages in the implementation
  brief. Render tests missed a wrong bind label and three missing routes.
- Fakes must reject what they do not know; otherwise a test passes while
  the engine makes calls nobody checked.
- Parallel workers need disjoint files; a worker's `go test` can fail on
  another worker's half-finished edit, so the lead runs the final gate.
- Workers split by step left follow-ups across the boundary (`alerts.go`
  still read SQLite after the collector moved to the API). Brief shared
  consumers together, or plan a review pass for them.
- The lead splits shared files (README, `features.md`) into per-commit
  hunks; workers list which hunks are theirs.
- A worker hit a usage limit mid-task; resuming the same worker kept its
  progress.
- The delegate lanes are configured globally in
  `~/.config/delegate-skills/config.json`: feature, tests, ui, mechanical,
  docs and research on pi; complex and plan on claude opus; review on
  codex; review2 on claude sonnet, read-only. The `review` lane cannot run
  because `codex-delegate` is not installed. Claude lanes can edit files
  but cannot run commands in this container (bubblewrap loopback error),
  so code work that needs `go test` goes to pi lanes or subagents.
- A pi run can report `completed` with no edits when its reasoning uses
  the whole output limit (`stopReason: length`, empty `touchedFiles`).
  Check both fields on every result. Split large briefs into steps of one
  package each, give the exact text to insert, and tell the worker to
  write files early.
- A research lane with read-only tools has no `git`. Give it a path it can
  read, or use a subagent with a shell for `git show` on tags.
- A new migration breaks tests that assume "the last migration". Look for
  such tests when adding one.
- Independent review before dependent work found real blockers twice: the
  store cursor sharing in the rename, and the author checks in the parser.
  Keep it for security-relevant code.
- Parallel workers in worktrees with disjoint files, merged by
  cherry-pick, had no conflicts.

## 9. Next action

1. The final review pass over the Phase 3 diff on `feat/questions`
   (`plans/jigz/questions.md` section 3, WP8).
2. One PR for Phase 3.
