# sortie-loop: vision and roadmap

Status: draft for discussion (2026-10-21)

Plans for `sortie-loop` as a product. Its place in the whole system is in
`plans/system/overview.md`; its contract with the hub is
`plans/system/hub-protocol.md`. Features by area:
`plans/sortie-loop/features.md`. Earlier plans in this folder
(`enhancements.md`, `production-readiness.md`, `united-dashboard.md`,
`central-requests-summary.md`) record work already done.

## 1. Goal

A free, open-source tool that sets up and runs sortie agents on a
machine, across many repositories, with a dashboard that shows and
controls everything. It is complete on its own:

- **Standalone:** one machine, labels and `assignee:@me` select the work.
- **United:** several repositories on one machine in one dashboard,
  without the hub.
- **Connected:** enrolled with a hub, it becomes the hub's daemon on that
  machine, without the local dashboard (`plans/system/overview.md`
  section 4).

The boundary with the hub is in `plans/system/overview.md` section 2.1.

## 2. Principles

1. Useful without the hub. Every connected-mode feature is optional.
2. Runs on a pinned sortie version, upstream or fork, with a documented
   minimum set of engine requirements (section 4).
3. Machine-local settings (agent accounts, working hours, workspace mode,
   budgets) are owned by the member on that machine.
4. Agent credentials never leave the machine.
5. One place maps engine output to `sortie-loop` types; the rest of the
   code does not parse engine data.
6. No upselling: the dashboard and docs show no gated features and do not
   promote the hub.
7. No authentication: the dashboard binds to 127.0.0.1 by default;
   `--host 0.0.0.0` opens it. Users deploy it locally, on a private
   network, or behind their own secure proxy. Same-origin checks on
   state-changing requests stay.
8. Dashboard scope: the current pages stay. Metrics remain local and
   recent. Long history and cross-project analytics are out of scope.

## 3. Current state

Exists today (details in `features.md`): setup and labels, config,
supervision of one sortie process per workflow, idle sleep and wake,
budgets, cleanup, validate, status document, dashboard (operations, runs,
usage, workflows, projects, agents, settings, alerts), form editing with
backups, history store, JSON API, and unite for
repositories on one machine (registry in `~/.cache/sortie-loop/`, ports
from 7678, dashboard on 7677).

The dashboard has authentication and an access warning. Phase 0 removes
them (principle 7).

Known problems found during planning:

- The SQLite reader accepts schema versions 18 and 35 only
  (`internal/rollup/schema.go:16`). The pinned engine v1.28.1 migrates
  databases to schema 38, so after the first run on it the dashboard reads
  no run history. `testedSchemaVersions` in `status.go:24` is also stale.
- Each loop's sortie process runs its own HTTP server and dashboard on its
  own port. These are internal and must not be exposed or shown to users.
- Comments in `WORKFLOW.prs.md`, `config.go`, and `supervise.go` describe
  client-side PR filtering; the fork's `github-pr` tracker sends the
  filter to GitHub search when it is non-empty.

## 4. Engine requirements

### 4.1 Required (upstream v1.28.1 and later)

| Requirement | Used for |
| ----------- | -------- |
| `sortie --env-file <file> --port <n> <workflow>` | starting loops |
| `sortie validate --format json`, `sortie --version` | validate, settings, capability detection |
| Env overrides `SORTIE_TRACKER_PROJECT`, `SORTIE_TRACKER_API_KEY`, `SORTIE_TRACKER_QUERY_FILTER`, `SORTIE_AGENT_*`, `SORTIE_POLLING_INTERVAL_MS` | config without editing workflow files |
| `SORTIE_*` variables passed to hooks; `SORTIE_WORKSPACE`, `SORTIE_ISSUE_IDENTIFIER` | workflow hooks and prompts |
| Front matter: `tracker` (states, handoff, comments), `polling`, `db_path`, `workspace`, `hooks`, `agent` | shipped workflows |
| `dispatch.rules` with `stage` and `next` (1.27.0) | plan → build chain |
| `tracker.kind: github` | issue workflow |
| `GET /api/v1/state` | live state |

### 4.2 Optional (fork only today)

| Feature | Needed for | Path to upstream |
| ------- | ---------- | ---------------- |
| `tracker.kind: github-pr` | PR workflow (review, review-fix, merge of any PR by label) | propose the tracker upstream (standalone package, about 700 lines) |
| `agent.kind: pi` | `pi` with measured tokens | use upstream `agent-client-protocol` with the `pi-acp` adapter; tokens are then unmeasured, which affects budgets |

Without the fork, the issue workflow runs with `claude-code`, `codex`, or
`opencode`, and the PR workflow is disabled.

### 4.3 Coupling to reduce

- Read run data through sortie's HTTP API (`/api/v1/runs`,
  `/runs/{id}/events`, `/events`; JSON Schema kept in sync upstream by a
  contract test) instead of its SQLite tables. Keep the SQLite reader only
  for history while no engine is running, with a schema range taken from
  the pinned version.
- Keep the mapping from engine errors to outcome classes in one adapter
  (`internal/store/outcome.go`), using the classes in
  `hub-protocol.md` 2.1.
- Capability detection at start: engine version and supported tracker and
  agent kinds decide which features are enabled. The dashboard states which
  features are disabled and why.
- Pin: engine repository and version are configurable. The default is the
  fork (`kinged007/sortie`) until upstream contains the `pi` adapter and
  the `github-pr` tracker; then the default moves to an upstream release.

## 5. Planned features

### 5.1 Working hours

- Windows per machine and per agent pool entry: days, hours, timezone.
- Outside a window: claim no new work; running sessions finish (optional:
  pause them).
- Built on the existing sleep, wake, pause, and resume.
- Shown in the dashboard; reported to the hub when connected.

### 5.2 Agent account pool

- An ordered list of entries: runtime, account (for example a separate
  config directory per logged-in subscription, or an API key), working
  hours, cost class.
- `sortie-loop agent-exec` is the agent command in workflows. It picks the
  first available entry for the runtime, runs the real CLI, detects
  usage-limit output, and marks the entry exhausted until its reset time.
  A session that fails on a limit is retried by sortie and gets the next
  entry.
- Runtime failover: each shipped workflow carries a block per runtime; the
  engine reads the block named by `SORTIE_AGENT_KIND`. `sortie-loop` sets
  that variable for each loop and recycles the loops when every entry of
  the active runtime is exhausted or outside its hours.
- Provider terms for unattended use of subscriptions are the member's
  responsibility; the documentation names the issue.

### 5.3 Questions

- Plan and review prompts write questions in the `agent-question` format
  (`hub-protocol.md` section 4); blocking questions add `needs-answer`.
- `needs-answer` is excluded by the default query filters.
- Dashboard page listing open questions across repositories; answering
  there writes an `agent-answer` comment and clears `needs-answer`.

### 5.4 Previews

- A workflow stage or command starts a preview: the project's own preview
  command (for example `make preview`) plus a tunnel, or a sandbox
  provider URL.
- URL is long, random, and expires; listed on the run and the work item
  in the dashboard; registered with the hub when connected.

### 5.5 Connected mode

- `sortie-loop join <hub-url> <token>`; credential stored locally.
- Config pulled from the hub (projects, workflows, prompts, labels);
  applied when loops are idle.
- Heartbeat with the status document; run events; command channel
  (`hub-protocol.md` section 2).
- Runner label added to every loop's query filter.
- Works on with the last config if the hub is unreachable.
- No local dashboard in connected mode; the hub provides the views and
  controls.
- The status document uses the same fields as the hub protocol's status
  message, so one format serves both.

### 5.6 Manager session host

- Internal feature used only by the hub. It is not documented in the
  user docs or shown in the CLI help.
- Runs product and project manager sessions for the hub: Claude Code or
  `pi`, headless turns with session resume, in a checkout of the project;
  later ACP.
- Relays messages and output over the command channel.

## 6. Roadmap

Each phase ends with a release, a green acceptance suite, and release
notes. The acceptance suite runs real sortie with its `mock` agent kind
and the real sortie-loop binary against an in-memory fake GitHub API, so
it needs no model and no real GitHub.

### Phase 0: Finish and fix

- Merge the united dashboard.
- Fix the schema gate and stale comments (section 3).
- Remove authentication from the dashboard and config; document the
  deployment expectation (local, private network, or behind the user's own
  secure proxy).
- `--host` flag for the dashboard; default 127.0.0.1.
- Per-loop sortie HTTP servers bound to loopback where the engine allows
  it, and never linked or shown in the dashboard.
- Acceptance harness: `make verify`, fake GitHub API, sortie `mock` agent.

### Phase 1: Working hours and account pool (5.1, 5.2)

- Working-hours config per machine and per pool entry (days, hours,
  timezone); no new claims outside a window; optional pause of running
  sessions.
- Agent pool config: ordered entries with runtime, account, hours, cost
  class.
- `sortie-loop agent-exec` wrapper: entry selection, usage-limit detection,
  exhausted-until tracking.
- Runtime failover: the active runtime is set by an environment switch
  (`SORTIE_AGENT_KIND`) on the same workflow; loops are recycled on
  exhaustion or end of hours.
- Dashboard: current window, pool entries and their state.
- Acceptance: a scripted limit failure moves the next run to the next
  entry; no claims happen outside a window.

### Phase 2: Engine independence (4.3)

- Capability detection at start; dashboard notices for disabled features.
- Run data read from sortie's HTTP API; SQLite reader kept for history,
  schema range taken from the pinned version.
- Engine errors mapped to the outcome classes of `hub-protocol.md` 2.1 in
  one adapter.
- Configurable engine repository and pin; upstream releases run the issue
  workflow with `claude-code`, `codex`, or `opencode`.
- Upstream PRs proposing the `pi` adapter and the `github-pr` tracker.

### Phase 3: Questions (5.3)

- Plan and review prompts write `agent-question` blocks; blocking
  questions add `needs-answer`.
- `needs-answer` label in `setup` and excluded in default query filters.
- Prompts read `agent-answer` comments before acting.
- Dashboard Questions page across repositories: list, filter by audience
  and blocking, answer (writes an `agent-answer` comment and clears
  `needs-answer`).
- Acceptance: a blocking question stops dispatch; answering it in the
  dashboard resumes it and the next run sees the answer.

### Phase 4: Connected mode (5.5)

- `sortie-loop join <hub-url> <token>`; credential stored locally.
- Config pull with ETag; applied when loops are idle.
- Heartbeat with the status document; batched run events; command channel
  over WebSocket.
- Runner label added to every loop's query filter.
- Last config kept and used when the hub is unreachable.
- No local dashboard in connected mode.
- Status document fields match the hub protocol's status message.
- Built and tested against a fake hub generated from the protocol schemas.

### Phase 5: Previews (5.4)

- Preview command per project (for example `make preview`) and a tunnel,
  or a sandbox provider URL.
- Long, random, expiring URLs; links on runs and work items in the
  dashboard; registered with the hub when connected; removed on expiry.

### Phase 6: Manager session host (5.6)

- Claude Code or `pi` sessions in a checkout of the project, headless turns
  with session resume.
- Messages and output relayed over the command channel; one session per
  project and role; idle timeout.
- Internal and undocumented for users.

### Deferred: workspace modes

Not scheduled.

- Local (exists), SSH workers through sortie's SSH worker extension, and a
  sandbox provider (Vercel Sandbox or Daytona) through hooks or a wrapper
  command.

## 7. Open decisions

1. Default agent runtime for new users: `claude-code` or `pi`.
2. Name of the daemon role: keep `sortie-loop`, or rename when the product
   name is chosen.
