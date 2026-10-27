# Jigz roadmap

An ordered view of what is implemented and what is planned. Detailed plans
live in `plans/`; this document sequences the work and tracks status.

- `[x]` — implemented. Items on `main` are marked done without a note; items
  on a feature branch name the branch.
- `[ ]` — not implemented.

Status verified against `main` at `a7c7d4c` (2026-10-27) and the
`feat/engine-interface` branch. Sources: `plans/jigz/vision-and-roadmap.md`
(the phase plan), `plans/jigz/features.md`, `plans/jigz/enhancements.md`,
`plans/jigz/handoff.md`, `plans/jigz/production-readiness.md`,
`plans/jigz/engine-interface.md`, `plans/system/`, README.

The hub product has its own roadmap:
[plans/hub/ROADMAP.md](plans/hub/ROADMAP.md).

## 1. Shipped

### Setup and config

- [x] `jig setup` (interactive and `--json`): config scaffold, labels,
  workflow and prompt install; never overwrites an edited file, reports
  differences instead
- [x] Repo config `.jigz/config.yaml` with validation; per-loop query-filter
  overrides re-read on every run
- [x] `jig validate [--json]`: config checks plus engine validation per
  workflow file
- [x] Engine resolution and download with checksum; configurable engine
  repository and version (`engine:` in `machine.yaml`, `JIG_ENGINE_REPO`,
  `JIG_ENGINE_VERSION`)
- [x] Capability detection (engine version, tracker and agent kinds) gating
  features per workflow, shared by supervisor, validate, dashboard and
  status document

### Supervision and CLI

- [x] One loop process per workflow file; supervisor with run registry and
  ordered shutdown
- [x] `jig start` (background run), `stop`/`restart` (with `--when-idle`),
  `status [--json]`, `reload`, `wake`
- [x] `pause`/`resume` per workflow; spend controls with token caps and
  periods; `budget pause|resume|override`
- [x] Idle sleep and scheduled wake; workspace cleanup command and interval
  runner (`jig cleanup`)
- [x] Child logs per loop (rotated); versioned status document
- [x] Working hours per machine and per pool entry (`machine.yaml`), with no
  new claims outside a window
- [x] Agent account pool with `jig agent-exec`: entry selection, usage-limit
  detection, exhausted-until tracking, runtime failover (acceptance-covered)
- [x] Every loop runs with the engine server off

### Dashboard (read-only)

- [x] Pages: operations, runs, run detail, live run (events, tokens,
  delivery, session, agent log, engine log), usage, metrics, workflows,
  pipeline, projects, agents, settings, alerts, backups
- [x] Read-only by design: no route, form or button changes anything;
  loops are controlled with the CLI
- [x] JSON API under `/api/v1/` (fleet, projects, runs, usage, metrics,
  audit, questions)
- [x] Hide pages and sections through `machine.yaml`; loopback bind by
  default with `--host` to move it; no authentication
- [x] United dashboard across repositories (`--unite`)
- [x] History store and collector (run data read through the engine HTTP
  API, SQLite kept for history), outcome classes mapped in one adapter

### Questions

- [x] `agent-question`/`agent-answer` format in the prompts; a blocking
  question escalates the item to `needs-human`; prompts consider only
  authorized authors
- [x] Question scanner (dashboard and `jig daemon`) resumes a parked item
  when every blocking question has an authorized answer
- [x] Read-only Questions page across repositories with links to GitHub and
  a copyable answer template

### Connected mode

- [x] `jig join <hub-url> <token>` / `jig leave`; credential storage with
  strict file modes
- [x] `jig daemon` / `jigd` user service (systemd unit, launchd agent)
- [x] Config pull with ETag, applied when loops are idle; last config used
  when the hub is unreachable
- [x] One loop per (project, workflow) from the hub config; on-demand start
  and stop on `wake`; fallback search for items with the runner label
- [x] Heartbeat status document, batched run events, command channel
  (`start`, `stop`, `drain`, `restart_when_idle`, `pause`, `resume`, `wake`,
  `reload_config`)
- [x] Runner label routing (`runner:<name>` in every loop's query filter);
  no local dashboard in connected mode
- [x] Hub protocol v0: JSON Schemas, Go types, HTTPS + WebSocket client, and
  an in-memory fake hub for acceptance tests

### Workflows and prompts

- [x] Issue workflow: plan, plan-build chain, build, docs
- [x] PR workflow: review, review-fix, merge
- [x] Label-driven chaining between loops (build → review → review-fix →
  merge)

### Engine integration and release engineering

- [x] Configurable engine target, including upstream releases (acceptance
  runs the issue workflow on upstream v1.29.0)
- [x] Acceptance suite (`make verify`): real `jig` binary with a scripted
  agent against an in-memory fake GitHub; connected-mode daemon test;
  upstream-engine test
- [x] CI: format/vet/test, acceptance job, cross-build matrix
  (linux/darwin × amd64/arm64); GoReleaser releases with SBOM

## 2. Phase status

Phase names from `plans/jigz/vision-and-roadmap.md` section 6.

| Phase | Status |
| --- | --- |
| 0 Finish and fix (united dashboard, no auth, `--host`, acceptance harness) | done |
| 1 Working hours and agent account pool | done |
| 2 Engine independence | done, except the upstream PRs (external, see 3.7) |
| Rename to Jigz | done |
| 3 Questions | done |
| 4 Connected mode | done |
| 5 Previews | not started |
| 6 Agent session host | not started |
| Deferred: workspace modes | not scheduled |

## 3. Roadmap

Ordered by practical build sequence: finish work in flight first (it changes
the config model everything else sits on), then reliability fixes that
affect every run, then small independent improvements, then the remaining
features of the phase plan. Reorder freely; only item 1 has real
sequencing constraints (its phases share one branch and one config model).

### 1. Configuration interface — in progress

Jigz owns all user-facing configuration in one file (`.jigz/config.yaml`)
and generates the engine's workflow documents from it; supervision runs
against the agents' availability. Plan: `plans/jigz/engine-interface.md`.
Branch `feat/engine-interface`; phases 0 to 2 are done there, the rest is
open.

- [x] Phase 0: documentation fix (README model-configuration claims)
- [x] Phase 1: config model and loader (single-file config, resolved work
  entries)
- [x] Phase 2: hours and offline handling (per-loop hours, offline reason
  and estimated start)
- [ ] Phase 3: document generation (workflow documents from the config)
- [ ] Phase 4: fallback (pool fallback order into loop supervision)
- [ ] Phase 5: contract tests and CI
- [ ] Phase 6: cleanup (remove legacy config paths and patching)
- [ ] Phase 7: model-unavailable trigger

### 2. Reliability: loop crash containment

- [ ] Restart a loop that exits instead of ending the run. Today one
  crashed loop stops every other loop (README "Known limitations").

### 3. Upgrade UX: workflow file versioning

- [ ] Stamp workflow files with the binary version that wrote them, so an
  upgrade can offer a new version of a file the user has not edited (README
  "Known limitations").

### 4. Prompt quality

- [ ] PR reviews from a maintainer's stance: the review prompt should ask
  whether a maintainer of the repo would approve the PR (code style,
  conventions, fit) — `plans/NOTES.md`.

### 5. Previews (phase 5)

Plan: `plans/jigz/vision-and-roadmap.md` section 5.4. The hub protocol's
preview endpoints are reserved and answered `unsupported` until this lands.

- [ ] Preview stage or command per project (the project's preview command
  plus a tunnel, or a sandbox provider URL)
- [ ] Long, random, expiring URLs, listed on the run and the work item in
  the dashboard
- [ ] Register previews with the hub when connected; remove on expiry
  (`POST previews`, `DELETE previews/{id}` in `plans/system/hub-protocol.md`)

### 6. Agent session host (phase 6)

Plan: `plans/jigz/vision-and-roadmap.md` section 5.6. Internal to the hub;
not documented in the user docs. Protocol commands `session.*` and
`task.run` are reserved and answered `unsupported` until this lands.

- [ ] Conversations: agent sessions in a checkout of the project, headless
  turns with session resume; one session per (agent, project); idle timeout
- [ ] One-shot tasks (`task.run`), result returned on the socket
- [ ] Messages and output relayed over the command channel
- [ ] Same account pool and working hours as the loops; sessions never push
  to protected branches

### 7. Hardening backlog

Known gaps recorded in `plans/jigz/handoff.md` section 6. Not scheduled;
pick up opportunistically.

- [ ] Usage-limit detection: recognize limits that do not reach the
  engine's error output; configurable limit patterns in `pool.json`
- [ ] Re-probe engine capabilities when the pin changes, not only at
  supervisor start
- [ ] Pool runtimes and shipped workflow blocks for `codex` and `opencode`
  (both need `agent.kind` and `agent.command` set today)
- [ ] GitHub enrichment in the collector (PR timeline, commits, check
  runs) is never triggered; wire it up
- [ ] Configurable collector copy interval (60 s fixed today)
- [ ] Detect a replaced engine database whose run ids exceed the stored
  last run id
- [ ] Suppress the PR-workflow-off alert for projects started with
  `JIG_ONLY=issues`
- [ ] Backfill `workflow_file` on the API path, not only the SQLite path
- [ ] Acceptance scenario for the PR workflow on the default engine
- [ ] Review leftovers: pid reuse making a stale status document look
  running; temp files after SIGKILL; status write failures only reaching
  stderr; `--repo` ignored by `status`, `stop`, `restart`

### 8. Blocked on external work

- [ ] Upstream releases carry the `pi` agent adapter and the `github-pr`
  tracker (upstream PRs in progress, owned by the user). Until both are
  released upstream, the pinned fork stays the default engine.
  Blocks: full engine independence (phase 2 tail).
- [ ] Cost per run: enforce `workflows.<name>.budget.max_usd`. Blocked on
  the engine recording cost per run (`plans/jigz/enhancements.md` L7).
- [ ] Per-run transcript in history. Blocked on the engine writing one
  (`plans/jigz/enhancements.md` L8).
- [ ] Stricter, configurable author filter for question answers (issue
  #20). Waiting on a decision.

### 9. Optional and deferred

- [ ] Windows support (parity work L12, `plans/jigz/enhancements.md`;
  optional, ~60% of commands are file-based already; the engine releases a
  `.zip`, the installer reads `tar.gz`)
- [ ] SSH workers as a workspace mode (deferred)
- [ ] Sandbox provider (Vercel Sandbox or Daytona) through hooks or a
  wrapper command (deferred)
- [ ] Fleet dashboard control plane and form-based editing — separate
  repository (`plans/jigz/enhancements.md` items 5–6)

## 4. Open decisions

1. Default agent runtime for new users: `claude-code` or `pi`
   (`plans/jigz/vision-and-roadmap.md` section 7).
2. Issue #20: stricter, configurable author filter for question answers.
3. Repository visibility: going public, and what to do about the author
   identity in the git history (`plans/jigz/production-readiness.md`).
