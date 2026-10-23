# Jigz: handoff

Status: 2026-10-22, updated after the Jigz rename. Read this first in a new
session working on Jigz. The hub is a separate stream; its plans are in
`plans/hub/` and it has no code yet.

## 1. Objective

Build Jigz as a free, open-source tool that is complete without
the hub, following `plans/jigz/vision-and-roadmap.md` section 6.
Phase 0 is done except the merge. Phases 1 and 2 are complete in code,
tests, and docs. Neither has a release or release notes; releases are
deferred until the user asks. The rename to Jigz is done. The next steps
are the acceptance test in CI and Phase 3.

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
- The user approved running the acceptance test in CI (2026-10-22). It is
  not implemented yet.
- Releases are deferred until the user asks.

Open decisions (do not decide them; ask):

- Default agent runtime for new users: `claude-code` or `pi`.

## 4. State of the work

Branch `feat/rename-jigz` (renamed from `feat/united-dashboard`), 38 commits
ahead of `main`, not pushed, no PR open. Phase 1 (working hours, account pool,
`agent-exec`, runtime failover, dashboard pages) is in `internal/machine`,
`cmd/jig/gate.go` and `cmd/jig/agentexec.go`. Phase 2 commits:

| Commit | Change |
| ------ | ------ |
| `b69906d` | `internal/store/outcome.go` maps runs to the classes `succeeded`, `failed_agent`, `failed_workspace`, `failed_limit`, `cancelled`, `timeout`, `handoff_withheld`. The account-limit pattern is `machine.BuiltinLimit`. The CSV outcome column and the API `error_class.key` carry the class key. |
| `6940822` | `internal/engine` has one resolver with a configurable repository and version: the `engine:` block in `machine.yaml`, overridden by `JIG_ENGINE_REPO` and `JIG_ENGINE_VERSION`; `JIG_ENGINE_BIN` wins. Cache files are named `sortie-<owner>+<name>-<version>`. |
| `250d045` | `internal/collector/api.go` reads runs from `/api/v1/runs` (paged by run id, filtered by `workflow_file`), events from `/api/v1/state`, and rollup counts from `/api/v1/stats` `by_status`. A SQLite fallback serves stopped loops; its schema range is 18 to 43, with `rollup.EngineReleases` {1.28.1: 38, 1.29.0: 43}. Store migration 8 drops `workspace`, `template_id`, `api_requests`, `handoff_reset`, and `review_metadata`. A replaced engine database with a lower newest run id re-copies that workflow's runs. |
| `59d2ba2` | Capability detection (`internal/engine/probe.go`, `gate.go`, `internal/plan/gating.go`, `cmd/jig/capabilities.go`). The version comes from `-dumpversion`; kinds come from `validate` on a workflow with unknown kinds. Without `github-pr` the PR loop is disabled (state `disabled`, reason in `hold`). Without `pi` the shipped workflows run with `claude-code`. Engine below 1.27.0 runs no loops. An explicitly set unsupported agent is an error. Pool entries for a missing runtime are not picked. One alert is shown per disabled feature. Settings lists the kinds. The status document `engine_version` is the binary's version. |
| `01a0ad0` | The dashboard reads parked and budget-hold alerts from `/api/v1/state` while the engine runs. Settings shows the schema range. |
| `3accb2b` | `TestAcceptanceUpstreamEngine` runs upstream `sortie-ai/sortie` v1.29.0 selected through `machine.yaml`: the issue is handed off, the PR loop is disabled with no PR requests, `validate` passes, the alert and kinds are shown, and a run arrives through the API. The Makefile acceptance target runs both tests. |

Commits `f5d9b45`, `cb566b6`, `8bc988b` (hub plans), `1d5ce19`, and
`14919dd` (product names, rename schedule) are plan changes on the branch.

The rename to Jigz is in the working tree, not committed:

- The command package moved to `cmd/jig`, the module path is
  `github.com/kinged007/jigz`, and the plans moved to `plans/jigz/`.
- The tool-owned environment variables are `JIG_*`: `JIG_TOKEN`, `JIG_REPO`,
  `JIG_ONLY`, `JIG_CLONE_URL`, `JIG_WORKSPACES`, `JIG_MILESTONE`,
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

- `make verify` passed on `3accb2b`: gofmt, `go vet` (also with
  `-tags acceptance`), build, `go test ./...`, and both acceptance tests
  (`TestAcceptance` about 105 s, `TestAcceptanceUpstreamEngine` about 65 s;
  about 3 minutes in total). The acceptance tests download each engine
  (about 25 MB) on first run per machine and fail if the download fails.
- After the rename, on the working tree of 2026-10-22: `gofmt -l cmd
  internal` is empty, `go vet ./...` and `go test ./...` pass. The
  acceptance tests were not run: they download both engines and take about
  three minutes.
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

1. Merge `feat/rename-jigz` into `main` through a PR (the user does
   this, or asks for it).
2. Add the acceptance test to CI (approved).
3. Phase 3: Questions (roadmap section 6).
4. Phase 1 and Phase 2 releases and release notes: deferred; only when
   the user asks.

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
  `.jigz` and leaves the other alone. The workflow files a target
  repository installed before the rename still name the old variables,
  and `internal/config` sets those names alongside the new ones, so the
  loops keep running until `jig setup` refreshes the files.
- `plans/jigz/enhancements.md` and `production-readiness.md` refer
  to `plans/pm-agent.md`, which does not exist in the repository.
- Target repositories installed before Phase 1 have workflows without the
  `claude-code:` block; the runtime switch to claude-code needs
  `jig setup` re-run there.

## 7. Processes on this machine

A real instance of the old binary (`sortie-loop --host=0.0.0.0 --unite`)
serves its dashboard on port 7677 and runs engine 1.28.1 children on 7679
and 7680, using the registry in `~/.cache/sortie-loop/`. It predates the
rename and keeps writing there. The first run of the renamed binary moves
that directory to `~/.cache/jigz/`, which leaves the old instance without
a registry; stop it when the switch happens. Do not write to its registry
before that. Tests isolate `HOME` and `XDG_*` to temporary directories;
keep doing that.

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

## 9. Next action

1. Run `make verify`, which adds the acceptance tests to the checks that
   already pass (`gofmt`, `go vet`, `go test ./...`). The acceptance tests
   download both engines and take about three minutes.
2. Add the acceptance test to CI in `.github/workflows/ci.yml`. It needs
   network to download both engines and takes about 3 minutes. Consider
   caching the engines in `~/.local/share/jigz`, keyed on
   repository and version. Ask the user before adding any action or tool
   beyond what the workflow already uses.
3. Phase 3 (Questions).

The default agent runtime in section 3 remains open.
