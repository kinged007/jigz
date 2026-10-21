# sortie-loop: handoff

Status: 2026-10-21. Read this first in a new session working on
`sortie-loop`. The hub is a separate stream; its plans are in `plans/hub/`
and it has no code yet.

## 1. Objective

Build `sortie-loop` as a free, open-source tool that is complete without
the hub, following `plans/sortie-loop/vision-and-roadmap.md` section 6.
Phase 0 is done except the merge. Phase 1 is next.

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
- The manager session host (roadmap Phase 6) is internal: not in the user
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
- Connected mode runs without the local dashboard.
- Workspace modes (SSH, sandbox) are deferred. Hosted runners are a hub
  backlog item.
- The acceptance harness uses an in-memory fake GitHub API
  (`internal/fakegithub`), not Gitea (reason in `delivery-method.md` 2.3).

Open decisions (do not decide them; ask):

- Product names for `sortie-loop` and the hub. Candidates discussed:
  Jig + Shiftwork, Stint + Rota, Peg + Pegboard, Plumb + Level,
  Caliper + Muster. `shift` cannot be a command name (bash builtin).
- Default agent runtime for new users: `claude-code` or `pi`.
- Whether CI runs the acceptance test (downloads the engine, about 70 s).

## 4. State of the work

Branch `feat/united-dashboard`, 13 commits ahead of `main`, not pushed, no
PR open. Commits from this session:

| Commit | Change |
| ------ | ------ |
| `ae57e5a` | plans split into system, sortie-loop, and hub; product boundary |
| `fbee073` | schema gate accepts engine schema 38; PR filter comments corrected |
| `bbfa5b1` | authentication removed; per-loop engine servers not linked; `--loop-url-template` and `loop_bind_host` removed; wildcard bind label; unknown paths return 404; add-project wizard and project removal routed |
| `79825f4` | plans name the fake GitHub API |
| `7d1e0be` | acceptance harness and `make verify` |

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
- The acceptance test (`cmd/sortie-loop/acceptance_test.go`, tag
  `acceptance`) runs the real `sortie-loop` binary and engine v1.28.1 with
  the `mock` agent. Scenarios:
  - an `agent:plan` issue is claimed and handed off;
  - `needs-human` and other-assignee issues are not dispatched;
  - the dashboard API reports the run from the schema 38 database.

  It fails if the fake GitHub API receives an unknown route, parameter, or
  search qualifier.
- Dashboard pages were loaded in a browser after the auth removal; a
  foreign Origin or Host gets 403.

## 6. Outstanding work

In order of the roadmap:

1. Merge `feat/united-dashboard` into `main` through a PR (the user does
   this, or asks for it).
2. Engine pin enforcement (Phase 2 item, small): when the pinned engine is
   not installed, `resolveSortieBin` (`cmd/sortie-loop/bin.go`) uses a
   `sortie` on `PATH` before downloading the pin. This machine therefore
   runs 1.27.0 from `~/.local/bin/sortie`. Proposed fix: download the pin
   first; fall back to `PATH` with a warning only if the download fails.
3. Phase 1: working hours and agent account pool (roadmap section 6).
   Usage-limit scenarios need a fake agent command behind
   `agent.command`, because the engine's `mock` agent cannot produce
   usage-limit errors.

Known gaps, not scheduled:

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

## 7. Processes on this machine

A real `sortie-loop` instance with its `sortie` children runs on ports
7677 to 7679, using the registry in `~/.cache/sortie-loop/`. Do not stop
it or write to its registry. Tests isolate `HOME` and `XDG_*` to temporary
directories; keep doing that.

## 8. Lessons for routing work

- Research before building when an external system is involved: the
  research ruled out Gitea before any harness code was written.
- Include a browser check of the affected pages in the implementation
  brief. Render tests missed a wrong bind label and three missing routes.
- Fakes must reject what they do not know; otherwise a test passes while
  the engine makes calls nobody checked.
- Parallel workers need disjoint files; a worker's `go test` can fail on
  another worker's half-finished edit, so the lead runs the final gate.

## 9. Next action

Ask the user which of these to start, in this order of recommendation:

1. Engine pin enforcement (section 6, item 2).
2. Phase 1: working hours and account pool.
3. Adding the acceptance test to CI (needs approval).
