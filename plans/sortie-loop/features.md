# sortie-loop: features

Status: draft for discussion (2026-10-21)

"exists" means implemented on `main` or on `feat/united-dashboard`. A
number in brackets is the phase in `plans/sortie-loop/vision-and-roadmap.md`
section 6.

## 1. Setup and config

- `setup` (interactive and `--json`): config scaffold, labels, workflow and
  prompt install, env file (`setup.go`, `wizard.go`, `embed.go`, `bin.go`)
  — exists
- Repo config and fleet config with validation (`internal/config`,
  `internal/fleetconfig`) — exists; ignored in connected mode
- Query filters with milestone and assignee scope (`config.go`) — exists
- `validate [--json]` (`validate.go`) — exists
- Engine resolution, pinned download with checksum (`engine.go`) — exists
- Configurable engine repository and pin; upstream supported [2]
- Capability detection (engine version, tracker and agent kinds) [2]

## 2. Supervision

- One sortie process per workflow; restart, reload, restart when idle
  (`supervise.go`, `internal/supervise`, `reload.go`) — exists
- Child logs (`childlog.go`, `logs.go`) — exists
- Idle sleep and wake (`sleep.go`, `wake_cmd.go`) — exists
- Budgets with pause and override (`budget.go`, `budget_cmd.go`) — exists
- Workspace cleanup (`cleanup.go`) — exists
- Status document (`status.go`) — exists
- Working hours per machine and per pool entry [1]
- Agent account pool, `agent-exec` wrapper, runtime failover [1]

## 3. Dashboard

- Operations, runs, run detail, live run, usage, metrics, workflows,
  pipeline, projects, agents, settings, alerts (`web.go`, `pages.go` and
  page files) — exists
- Form editing with backups (`editors.go`, `internal/edit`) — exists
- Remove authentication; document local, private-network, and proxy
  deployment [0]
- `--host` flag, default 127.0.0.1 [0]
- Per-loop engine HTTP servers are internal: bound to loopback where the
  engine allows it, never linked or shown [0]
- History store and collector (`internal/store`, `internal/collector`) —
  exists
- JSON API (`api.go`) — exists
- Run data through sortie's HTTP API; SQLite only for history [2]
- Outcome classes shared with the hub protocol [2]
- Disabled-feature notices from capability detection [2]
- Questions page with answering [3]
- Working hours and account pool status [1]
- Preview links on runs [5]

## 4. Unite

- Several repositories on one machine in one dashboard (registry, port
  claims, `--unite`) — exists

## 5. Workflows and prompts

- Issue workflow: plan, plan-build chain, build, docs — exists
- PR workflow: review, review-fix, merge (needs `github-pr`, fork only) —
  exists
- `agent-question` and `agent-answer` format in prompts; `needs-answer`
  label [3]
- Preview stage or command [5]

## 6. Connected mode

- `join`, credential storage [4]
- Config pull and apply when idle [4]
- Heartbeat, run events, command channel [4]
- Config source for the supervisor: local files or hub [4]
- Loops per (project, workflow) from the hub config [4]
- On-demand loop start and stop on `wake`; fallback search for items with
  the runner label [4]
- Default branch used for agent PRs [4]
- Runner label in query filters [4]
- Offline operation with last config [4]
- No local dashboard in connected mode [4]
- Status document fields match the hub protocol's status message [4]
- Agent session host, internal to the hub, not documented for users [6]
- Conversations: one session per (agent, project), idle timeout [6]
- One-shot tasks (`task.run`) with the result returned on the socket [6]

## 7. Workspace modes

- Local — exists
- SSH workers — deferred
- Sandbox provider through hooks or wrapper — deferred
