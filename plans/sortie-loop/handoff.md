# sortie-loop: handoff

Status: 2026-10-22. Read this first in a new session working on
`sortie-loop`. The hub is a separate stream; its plans are in `plans/hub/`
and it has no code yet.

## 1. Objective

Build `sortie-loop` as a free, open-source tool that is complete without
the hub, following `plans/sortie-loop/vision-and-roadmap.md` section 6.
Phase 0 is done except the merge. Phase 1 is complete in code, tests, and
docs; its release and release notes are not done. Phase 2 is next.

Read, in order:

1. `plans/system/overview.md`: parts, product boundary (2.1), modes,
   engine policy.
2. `plans/sortie-loop/vision-and-roadmap.md`: principles, engine
   requirements, planned features, roadmap.
3. `plans/sortie-loop/features.md`: existing and planned features by area.
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
- New tools or dependencies, and CI workflow changes, need the user's
  approval first.
- Global rules in `~/.claude/AGENT_ORCHESTRATION.md`: the main agent
  delegates exploration, edits, and test runs to workers and reports on
  delegation after each assignment.

## 3. Decisions in effect

- The product boundary table in `plans/system/overview.md` 2.1 decides
  what goes in `sortie-loop` and what goes in the hub.
- No cross-machine unite in `sortie-loop`; the hub's fleet view covers it.
- Connected mode is runner mode: the hub is the only source of project
  config, and there is no local dashboard.
- Workspace modes (SSH, sandbox) are deferred. Hosted runners are a hub
  backlog item.
- The acceptance harness uses an in-memory fake GitHub API
  (`internal/fakegithub`), not Gitea (reason in `delivery-method.md` 2.3).
- Machine-local settings (working hours, pool entries) are in `machine.yaml`
  in the user config directory. Pool state is in `pool.json` in the user
  state directory.
- Runtime failover sets `SORTIE_AGENT_KIND` and `SORTIE_AGENT_COMMAND` on
  one workflow, so the workflow switches agent runtime when the pool moves
  to another entry.
- `hours.end` is a config setting with the values `finish` or `stop`. The
  default is `finish`.

Open decisions (do not decide them; ask):

- Product names for `sortie-loop` and the hub. Candidates discussed:
  Jig + Shiftwork, Stint + Rota, Peg + Pegboard, Plumb + Level,
  Caliper + Muster. `shift` cannot be a command name (bash builtin).
- Default agent runtime for new users: `claude-code` or `pi`.
- Whether CI runs the acceptance test (downloads the engine, about 70 s).

## 4. State of the work

Branch `feat/united-dashboard`, 27 commits ahead of `main`, not pushed, no
PR open. Commits from this session:

| Commit | Change |
| ------ | ------ |
| `ae57e5a` | plans split into system, sortie-loop, and hub; product boundary |
| `fbee073` | schema gate accepts engine schema 38; PR filter comments corrected |
| `bbfa5b1` | authentication removed; per-loop engine servers not linked; `--loop-url-template` and `loop_bind_host` removed; wildcard bind label; unknown paths return 404; add-project wizard and project removal routed |
| `79825f4` | plans name the fake GitHub API |
| `7d1e0be` | acceptance harness and `make verify` |
| `9313e3e` | engine resolution: SORTIE_BIN, installed pin, download of the pin; a `sortie` beside the binary or on PATH only when the download fails, with a warning; one download attempt per process; unit tests disable the download in TestMain |
| `8aea1f4` | machine config package: working hours and an agent account pool |
| `b254060` | agent-exec: runs the next available pool account as the agent command |
| `b890abd` | supervisor gate on working hours; runtime switch from the pool; shipped workflows carry `pi:` and `claude-code:` blocks |
| `09376c6` | validate hides the other runtime's unknown_key warning |
| `c899be4` | dashboard: working hours, account pool, and gate holds; README section |
| `891c510` | acceptance scenarios d and e (section 5) |
| `220393e` | docs on the usage-limit cost through the engine's credential check |

Commits `f5d9b45`, `cb566b6`, and `8bc988b` (hub plans) are on the branch
from the hub stream, not from this work.

Outside this repository:

- The release `v1.28.1` of `kinged007/sortie` had no assets. The
  linux and darwin archives (amd64, arm64) and `checksums.txt` were built
  locally from the tag and uploaded. Windows archives are not attached,
  because the official ones are signed through SignPath.
- PR https://github.com/kinged007/sortie/pull/7 (open, not merged) makes
  the fork's Release workflow skip jobs that need upstream-only secrets,
  skip Windows signing, and leave Windows out of fork releases. It is not
  run yet; the workflow has a `dry_run` input for a test.

## 5. Checks

- `make verify` passes with no overrides: gofmt, `go vet` (also with
  `-tags acceptance`), build, `go test ./...`, and the acceptance test.
  The acceptance test takes about 104 s.
- The acceptance test (`cmd/sortie-loop/acceptance_test.go`, tag
  `acceptance`) runs the real `sortie-loop` binary and engine v1.28.1. Scenarios:
  - an `agent:plan` issue is claimed and handed off;
  - `needs-human` and other-assignee issues are not dispatched;
  - the dashboard API reports the run from the schema 38 database;
  - scenario d: no claims outside a working-hours window, and a claim after
    the window opens;
  - scenario e: a usage limit on pool entry A moves the retry to entry B;
    entry A stays exhausted until the reset epoch.

  It fails if the fake GitHub API receives an unknown route, parameter, or
  search qualifier.
- Dashboard pages were loaded in a browser after the auth removal; a
  foreign Origin or Host gets 403.

## 6. Outstanding work

In order of the roadmap:

1. Merge `feat/united-dashboard` into `main` through a PR (the user does
   this, or asks for it).
2. Phase 1 release: the roadmap says each phase ends with a release and
   release notes. Neither is done. Ask the user before releasing.
3. Phase 2: engine independence (roadmap section 6).

Phase 1 against the roadmap (section 6, Phase 1):

- Working hours per machine and per pool entry, no new claims outside a
  window, optional stop of running sessions: done (`internal/machine`,
  `cmd/sortie-loop/gate.go`; `hours.end: finish | stop`).
- Agent pool config with runtime, account env, hours, cost class: done
  (`machine.yaml`).
- `sortie-loop agent-exec`: entry selection, usage-limit detection,
  exhausted-until tracking in `pool.json`: done
  (`cmd/sortie-loop/agentexec.go`).
- Runtime failover by `SORTIE_AGENT_KIND` / `SORTIE_AGENT_COMMAND`; loops
  recycled when they have no running session: done.
- Dashboard: current window, pool entries and their state, gate holds on
  Operations: done; checked in a browser against an isolated instance.
- Acceptance: a scripted limit failure moves the next run to the next
  entry; no claims outside a window: done (scenarios d and e).
- Release and release notes: not done.
- Reporting hours and pool to the hub belongs to Phase 4 (connected mode).

Known gaps, not scheduled:

- Engine v1.28.1 runs a credential check through the agent command before
  each worker run, so a usage limit is usually found there: the run fails,
  posts no comment, and retries after about 10 s.
- A usage limit that occurs mid-turn posts a failure comment.
- With `max_concurrent_agents` greater than 1, a loop can claim work between
  window close and stop.
- Acceptance covers `end: finish` only; `end: stop` is covered by
  supervisor integration tests with a fake engine (`gate_test.go`,
  `supervise_test.go`).
- The agent-exec "no entry until" time uses the global next change, not a
  time per runtime.
- The limit detection patterns are not checked against real `claude` or
  `pi` output.
- The supervisor's closed-gate check makes one blocking state request per
  loop per tick, with a 3 s timeout.
- A dashboard started while offline keeps the PATH fallback engine until
  restart, because the download is attempted once per process.
- No acceptance scenario for the PR workflow (`github-pr`); the fake
  serves its routes and has unit tests.
- GitHub enrichment in the collector (PR timeline, commits, check runs)
  never runs: nothing sets `app.github`.
- The collector copies runs every 60 s and the interval is not
  configurable, so the dashboard scenario waits about a minute.
- When the dashboard was stopped by a signal during a test with a hung fake
  engine, its `validate` child processes were left running. Not checked
  with the real engine.
- About 113 Go lines still say "central" (the earlier name of the
  dashboard code).
- `plans/sortie-loop/enhancements.md` and `production-readiness.md` refer
  to `plans/pm-agent.md`, which does not exist in the repository.
- Target repositories installed before Phase 1 have workflows without the
  `claude-code:` block; the runtime switch to claude-code needs
  `sortie-loop setup` re-run there (AGENTS.md: installed workflows are
  refreshed by setup).

## 7. Processes on this machine

A real `sortie-loop` instance with its `sortie` children runs on ports
7677 to 7679, using the registry in `~/.cache/sortie-loop/`. It started
before the engine resolution fix (`9313e3e`) and still runs engine 1.27.0
until restarted. Do not stop it or write to its registry. Tests isolate
`HOME` and `XDG_*` to temporary directories; keep doing that.

## 8. Lessons for routing work

- Research before building when an external system is involved: the
  research ruled out Gitea before any harness code was written.
- Research the engine source before designing a wrapper: env overrides,
  retry, and the credential check changed the design and the docs.
- Include a browser check of the affected pages in the implementation
  brief. Render tests missed a wrong bind label and three missing routes.
- Fakes must reject what they do not know; otherwise a test passes while
  the engine makes calls nobody checked.
- Parallel workers need disjoint files; a worker's `go test` can fail on
  another worker's half-finished edit, so the lead runs the final gate.
- A worker hit a usage limit mid-task; resuming the same worker kept its
  progress.

## 9. Next action

Phase 1 is complete except its release. Ask the user about:

1. A Phase 1 release (release and release notes).
2. Phase 2: engine independence.
3. The acceptance test in CI (needs the user's approval).

Open decisions in section 3 remain open.
