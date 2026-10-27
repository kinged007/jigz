# Jigz: features

Status: draft for discussion (2026-10-21)

"exists" means implemented on `main`. A
number in brackets is the phase in `plans/jigz/vision-and-roadmap.md`
section 6.

## 1. Setup and config

- `setup` (interactive and `--json`): config scaffold, labels, workflow and
  prompt install, env file (`setup.go`, `embed.go`, `bin.go`)
  — exists
- Repo config and fleet config with validation (`internal/config`,
  `internal/fleetconfig`) — exists; ignored in connected mode
- Query filters with milestone and assignee scope (`config.go`) — exists
- `validate [--json]` (`validate.go`) — exists
- Engine resolution, download with checksum (`internal/engine`), one resolver for the CLI, plan, supervisor and dashboard — exists
- Configurable engine repository and version (`engine:` in machine.yaml, `JIG_ENGINE_REPO`, `JIG_ENGINE_VERSION`); upstream supported — exists
- Capability detection (engine version, tracker and agent kinds), one result for supervisor, validate, dashboard and status document (`internal/engine/probe.go`, `gate.go`, `internal/plan/gating.go`) — exists

## 2. Supervision

- One sortie process per workflow; restart, reload, restart when idle
  (`supervise.go`, `internal/supervise`, `reload.go`) — exists
- Child logs (`childlog.go`, `logs.go`) — exists
- Idle sleep and wake (`sleep.go`, `wake_cmd.go`) — exists
- Budgets with pause and override (`budget.go`, `budget_cmd.go`) — exists
- Workspace cleanup (`cleanup.go`) — exists
- Status document (`status.go`) — exists
- `status` lists the other live runs and the dashboard host; `stop` and `restart` list them and warn when the run serves the dashboard (`instances.go`) — exists
- Working hours per machine and per pool entry (`internal/machine`,
  `cmd/jig/gate.go`) — exists
- Agent account pool, `agent-exec` wrapper, runtime failover
  (`agentexec.go`, `agents.go`) — exists

## 3. Dashboard

- Operations, runs, run detail, live run, usage, metrics, workflows,
  pipeline, projects, agents, settings, alerts (`web.go`, `pages.go` and
  page files) — exists
- The dashboard is read-only: it has no route, form or button that starts,
  stops, restarts, edits or changes anything; loops are controlled with the
  CLI (`stop`, `restart`, `reload`, `wake`, `cleanup`, `pause`, `resume`,
  `budget override`, `setup`) — exists
- Backups page lists saved copies of files, without restore — exists
- `dashboard.hide.pages` and `dashboard.hide.sections` in `machine.yaml`
  hide pages and sections; a hidden page or section answers 404 on every
  route that serves it (`hide.go`, `internal/machine/dashboard.go`). This is
  the built-in protection for a dashboard that is reachable beyond localhost — exists
- Remove authentication; document local, private-network, and proxy
  deployment [0]
- `--host` flag, default 127.0.0.1 [0]
- Per-loop engine HTTP servers are internal: bound to loopback where the
  engine allows it, never linked or shown [0]
- History store and collector (`internal/store`, `internal/collector`) —
  exists
- JSON API (`api.go`) — exists
- Run data through sortie's HTTP API; SQLite only for history (`internal/collector/api.go`, `internal/rollup`) — exists
- Outcome classes of the hub protocol, mapped in one adapter (`internal/store/outcome.go`) — exists
- Disabled-feature notices from capability detection (`capabilities.go`, `alerts.go`) — exists
- Read-only Questions page: questions across repositories, problems,
  links to GitHub, copyable `agent-answer` template [3] ([plan](questions.md))
- Question scanner resumes a parked item when every blocking question has
  an authorized answer posted after the latest `needs-human` label event
  (dashboard and `jig daemon`); `github.questions` switches it per project;
  reads up to 100 parked items per project per pass [3] ([plan](questions.md))
- Working hours and account pool status (`gate.go`, `agents.go`) — exists
- Preview links on runs [5]

## 4. Unite

- Several repositories on one machine in one dashboard (registry,
  `--unite`) — exists

## 5. Workflows and prompts

- Issue workflow: plan, plan-build chain, build, docs — exists
- PR workflow: review, review-fix, merge (needs `github-pr`, fork only) —
  exists
- `agent-question` and `agent-answer` format in prompts; blocking
  questions use `needs-human`; prompts consider only comments from
  authorized authors — exists ([plan](questions.md))
- Preview stage or command [5]

## 6. Connected mode

- `join`, credential storage — exists
- Config pull and apply when idle — exists
- Heartbeat, run events, command channel — exists
- Config source for the supervisor: local files or hub — exists
- Loops per (project, workflow) from the hub config — exists
- On-demand loop start and stop on `wake`; fallback search for items with
  the runner label — exists
- Default branch used for agent PRs — exists
- Runner label in query filters — exists
- Offline operation with last config — exists
- No local dashboard in connected mode — exists
- Status document fields match the hub protocol's status message — exists
- Agent session host, internal to the hub, not documented for users [6]
- Conversations: one session per (agent, project), idle timeout [6]
- One-shot tasks (`task.run`) with the result returned on the socket [6]

## 7. Workspace modes

- Local — exists
- SSH workers — deferred
- Sandbox provider through hooks or wrapper — deferred
