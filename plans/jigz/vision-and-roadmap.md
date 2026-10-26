# Jigz: vision and roadmap

Status: draft for discussion (2026-10-21)

Plans for Jigz as a product. Its place in the whole system is in
`plans/system/overview.md`; its contract with the hub is
`plans/system/hub-protocol.md`. Features by area:
`plans/jigz/features.md`. Earlier plans in this folder
(`enhancements.md`, `production-readiness.md`, `united-dashboard.md`,
`dashboard-requests-summary.md`) record work already done.

## 1. Goal

A free, open-source tool that sets up and runs sortie agents on a
machine, across many repositories, with a dashboard that shows and
controls everything. It is complete on its own:

- **Standalone:** one machine, labels and `assignee:@me` select the work.
- **United:** several repositories on one machine in one dashboard,
  without the hub.
- **Connected:** enrolled with a hub, it becomes the hub's runner on that
  machine, without the local dashboard (`plans/system/overview.md`
  section 4). The hub is the only source of project config.

The boundary with the hub is in `plans/system/overview.md` section 2.1.

## 2. Principles

1. Useful without the hub. Every connected-mode feature is optional.
2. Runs on a pinned sortie version, upstream or fork, with a documented
   minimum set of engine requirements (section 4).
3. Machine-local settings are owned by the member on that machine: agent
   CLIs and their logins (account pool), the tracker credential (the
   member's GitHub token), working hours, budgets, workspace mode and
   root, and the member's own tools and skills outside the project repo.
   In connected mode the hub is the only source of project config; local
   repo configs and the unite registry are ignored. Machine settings are
   in `machine.yaml` (`internal/machine`).
4. Agent credentials never leave the machine.
5. One place maps engine output to `jig` types; the rest of the
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
repositories on one machine (registry in `~/.cache/jigz/`,
dashboard on 7677).

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
| `tracker.kind: github-pr` | PR workflow (review, review-fix, merge of any PR by label) | upstream PR in progress (standalone package, about 700 lines) |
| `agent.kind: pi` | `pi` with measured tokens | upstream PR in progress; until it is released, the alternative is upstream `agent-client-protocol` with the `pi-acp` adapter, where tokens are unmeasured, which affects budgets |

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
- `jig agent-exec` is the agent command in workflows. It picks the
  first available entry for the runtime, runs the real CLI, detects
  usage-limit output, and marks the entry exhausted until its reset time.
  A session that fails on a limit is retried by sortie and gets the next
  entry.
- Runtime failover: each shipped workflow carries a block per runtime; the
  engine reads the block named by `SORTIE_AGENT_KIND`. `jig` sets
  that variable for each loop and recycles the loops when every entry of
  the active runtime is exhausted or outside its hours.
- Provider terms for unattended use of subscriptions are the member's
  responsibility; the documentation names the issue.

### 5.3 Questions

- Plan and review prompts write questions in the `agent-question` format
  (`hub-protocol.md` section 4); a blocking question escalates the item to
  `needs-human`, which the shipped workflows already exclude from dispatch.
- People answer on GitHub with an `agent-answer` comment, or through the
  hub, which posts it with the person's own token. When every blocking
  question on a parked item has an authorized answer, the question
  scanner (in the dashboard and in `jig daemon`) removes `needs-human`
  and `agent:done` and adds the trigger label named in the question's
  `resume` attribute.
- Read-only dashboard page listing questions across repositories, with
  links to GitHub and a copyable answer template; the dashboard writes
  nothing to GitHub.

### 5.4 Previews

- A workflow stage or command starts a preview: the project's own preview
  command (for example `make preview`) plus a tunnel, or a sandbox
  provider URL.
- URL is long, random, and expires; listed on the run and the work item
  in the dashboard; registered with the hub when connected.

### 5.5 Connected mode

- `jig join <hub-url> <token>`; credential stored locally.
- Runner mode: the daemon takes its project config only from the hub
  (projects, enabled workflows, workflow and prompt files with versions,
  query filter additions, labels); applied when loops are idle.
- Many repositories per daemon. The tracker takes one repository
  (`SORTIE_TRACKER_PROJECT`), so the daemon runs one sortie process per
  (project, workflow).
- Loops start on demand. The hub is the only writer of runner labels, so
  it knows when it routes an item and sends `wake {project, workflow}`.
  The daemon starts or wakes that loop and stops it when it is drained and
  idle for a configured time.
- Fallback: when the hub is unreachable, and periodically otherwise, the
  daemon runs one provider search for open items with its runner label
  across its configured projects and starts the matching loops. The last
  config is kept and used meanwhile.
- Agent PRs target the repository's default branch. Branch protection,
  merge rules, and promotion between branches are GitHub settings and the
  project's own CI, not hub or `jig` config. The shipped build and
  docs prompts resolve the default branch with
  `gh api repos/$SORTIE_TRACKER_PROJECT --jq .default_branch`; the issue
  workflow hooks read `origin/HEAD`; the merge prompt merges into the PR's
  `baseRefName`.
- Heartbeat with the status document (loops per project); run events;
  command channel (`hub-protocol.md` section 2).
- Runner label added to every loop's query filter.
- No local dashboard in connected mode; the hub provides the views and
  controls.
- The status document uses the same fields as the hub protocol's status
  message, so one format serves both.

### 5.6 Agent session host

- Internal feature used only by the hub. It is not documented in the
  user docs or shown in the CLI help.
- Two ways of running agents on a daemon:
  - Tracker-driven: sortie loops on issues and PRs. Used for all code
    changes.
  - Hub-driven: agent sessions started by the hub without an issue.
    Multi-turn conversations with any hub agent (product manager, project
    manager, or any other role the hub defines), and one-shot tasks
    triggered by schedule, event, or request in the hub.
- Rule: code changes go through an issue and a PR. Sessions do
  coordination and operations and never push to protected branches.
- Both ways use the same account pool and working hours.
- Sessions run Claude Code or `pi`, headless turns with session resume, in
  a checkout of the project; later ACP.
- Relays messages and output over the command channel.

## 6. Roadmap

Each phase ends with a release, a green acceptance suite, and release
notes. The acceptance suite runs real sortie with its `mock` agent kind
and the real `jig` binary against an in-memory fake GitHub API, so
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
- `jig agent-exec` wrapper: entry selection, usage-limit detection,
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
- Upstream PRs for the `pi` adapter and the `github-pr` tracker: in
  progress. Until both are merged and released upstream, the fork
  `kinged007/sortie` stays the default engine.

### Rename to Jigz

Done.

- The module path is `github.com/kinged007/jigz`. The binary is `jig`.
  The daemon runs as `jig daemon`, installed as the service `jigd`
  (systemd unit `jigd.service` on Linux, launchd label `jigd` on macOS).
- Release archive names and the install script use `jig`.
- The state directories are `~/.cache/jigz` (registry and status
  documents), `~/.local/share/jigz` (the engine) and `~/.local/state/jigz`
  (the pool state); `machine.yaml` is in `~/.config/jigz`. `internal/dirs`
  moves a directory left by an earlier install into place the first time
  it is looked up, so the registry, the run history and the settings
  survive the upgrade.
- GitHub repository renamed to Jigz. GitHub redirects the old URL.
- README and docs use the new names.
- Engine-owned names stay: `SORTIE_*` variables and the `sortie` binary;
  the scratch workspace keeps `.sortie/`. The repo-local directory is
  `.jigz/`, and `config.MigrateRepoDir` renames a repo's old `.sortie/`
  on first lookup.
- Acceptance: an existing install keeps its registry, history, and
  settings; `internal/dirs` has tests for the move and the merge.

### Phase 3: Questions (5.3)

- Plan and review prompts write `agent-question` blocks; a blocking
  question escalates the item to `needs-human` and names its `agent:*`
  trigger label in `resume`, because the engine removes the trigger at
  handoff.
- Prompts read `agent-answer` comments before acting.
- Read-only dashboard Questions page across repositories: list, filter by
  audience and blocking, link to GitHub, copy an answer template.
- The question scanner resumes a parked item (adds the `resume` trigger,
  removes `agent:done` and `needs-human`) when every blocking question
  has an authorized answer posted after the item was parked.
- Plan: [questions.md](questions.md).
- Acceptance: a blocking question stops dispatch; an `agent-answer`
  comment on GitHub from an authorized author resumes it and the next
  run sees the answer.

### Phase 4: Connected mode (5.5)

- `jig join <hub-url> <token>` stores the credential locally, then installs
  and starts the `jigd` service, which runs `jig daemon`.
- Config pull with ETag; applied when loops are idle.
- Config source for the supervisor: local files or the hub. This is the
  one new abstraction; it has two implementations. In connected mode local
  repo configs and the unite registry are ignored.
- Loops per (project, workflow) from the hub config.
- On-demand start and stop: `wake {project, workflow}` starts or wakes a
  loop; a drained loop stops after a configured idle time.
- Fallback search: one periodic provider search for open items with the
  daemon's runner label across its projects; starts the matching loops.
- Default-branch check: the shipped prompts and hooks use the repository's
  default branch for agent PRs; fix any that do not.
- Heartbeat with the status document (loops per project); batched run
  events; command channel over WebSocket.
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

### Phase 6: Agent session host (5.6)

- Conversations: Claude Code or `pi` sessions in a checkout of the
  project, headless turns with session resume; one session per
  (agent, project); idle timeout.
- One-shot tasks: one session per `task.run`; the result is returned on the
  socket.
- Messages and output relayed over the command channel
  (`session.*`, `task.run`).
- Same account pool and working hours as the loops; sessions never push to
  protected branches.
- Internal and undocumented for users.

### Deferred: workspace modes

Not scheduled.

- Local (exists), SSH workers through sortie's SSH worker extension, and a
  sandbox provider (Vercel Sandbox or Daytona) through hooks or a wrapper
  command.

## 7. Open decisions

1. Default agent runtime for new users: `claude-code` or `pi`.
