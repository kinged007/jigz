# Jigz: Phase 4 plan (Connected mode)

Status: approved by delegation (2026-10-24)

Implements Phase 4 of `plans/jigz/vision-and-roadmap.md` (section 5.5)
against `plans/system/hub-protocol.md` (draft v0). Work is verified as
`plans/system/delivery-method.md` section 2.1 describes: the acceptance
test in section 4 is written before the build, and a lane other than the
builder reviews each package. Section 5 lists the design decisions.

## 1. Scope and rules

`jig join <hub-url> <token>` enrolls the machine with one hub, stores the
credential and installs the `jigd` service, which runs `jig daemon`. The
daemon pulls project config, runs one loop per (project, workflow) on
demand, reports status and runs, and takes commands over one WebSocket.

1. The hub is the only source of project config. The daemon does not
   read repo configs (`.jigz/config.yaml` in checkouts), `jigz.yaml` or
   the unite registry.
2. Machine-local settings stay local: `machine.yaml`, `pool.json`, the
   tracker token, the agent CLIs and their logins. The hub gets them as
   status, never as credentials.
3. Every loop's query filter contains `label:runner:<name>`.
4. The daemon opens every connection and listens on no port other than
   the internal engine loop ports on 127.0.0.1.
5. No local dashboard in connected mode (2.7).
6. Commands implemented: `start`, `stop`, `drain`, `restart_when_idle`,
   `pause`, `resume`, `wake`, `reload_config`. `session.open`,
   `session.message`, `session.close` and `task.run` are Phase 6;
   `POST previews` and `DELETE previews/{id}` are Phase 5.
7. Linux (systemd user unit) and macOS (launchd agent). No Windows.

## 2. Design

### 2.1 Config source

What the loop supervisor reads today (`cmd/jig/main.go`,
`cmd/jig/supervise.go`), all under one repo root:

- `.jigz/config.yaml` through `config.Load`: `repo`, `token` (or
  `JIG_TOKEN`, `GITHUB_TOKEN`, `GH_TOKEN`), `milestone`, `assignee`,
  `filters`, `sleep`, `cleanup`, `workflows.<name>.budget`. `Env()` gives
  `SORTIE_TRACKER_PROJECT`, `SORTIE_TRACKER_API_KEY`, `JIG_WORKSPACES`;
  `FilterFor(name)` gives the query filter.
- `.jigz/workflows/WORKFLOW.<name>.md` and `prompts/`, found by
  `discoverWorkflows` and narrowed by `JIG_ONLY`; `startOne` reads the
  workflow for the gate, loop type and config hash. `discoverWorkflows`
  also runs `syncWorkflows`, which writes the shipped workflows into the
  root and must never run on a root whose files come from the hub.
- `machine.yaml`, `pool.json` and the engine binary, the same in every
  mode.

So the supervisor needs a set of loops and, per loop, a root holding
those files. The abstraction, in `internal/source`:

```go
type Loop struct {
	Project  string // owner/name
	Workflow string // WORKFLOW.<name>.md stem
	Root     string // holds .jigz/config.yaml and .jigz/workflows/
	Hash     string // hash of the files under Root that the loop reads
}

type Source interface {
	Loops(ctx context.Context) ([]Loop, error)
}
```

- `source.Files`: one repo root, its installed workflows, `JIG_ONLY`.
  `jig <root>` uses it in place of `discoverWorkflows` plus
  `selectLoops`; `syncWorkflows` stays on that path. No behaviour change.
- `source.Hub`: reads the stored hub config (2.3), writes one root per
  (project, workflow) under `<data>/hub/loops/<owner>/<name>/<workflow>/`
  (`dirs.Data()`) and returns them. The supervisor code is unchanged.

One root per (project, workflow), not per project: status files and
registry entries are keyed by root (`statusPath`, `writeRegistry`), and
each workflow starts and stops on its own. Workspaces and the engine
database stay in the root across config changes. The hub source writes
`config.yaml` with `repo`, `assignee`, `filters.<workflow>` and `sleep`
(2.4) and no token, and the workflow and prompts as served.

### 2.2 Join, credentials and the service

`jig join <hub-url> <token> [--name <name>] [--no-service] [--yes]`:

1. The URL must be `https://`, or `http://` on a loopback host (fake hub,
   local hub development). A token of `-` is read from stdin, so it is
   not in the process list.
2. The tracker token comes from `JIG_TOKEN`, `GITHUB_TOKEN`, `GH_TOKEN`,
   else `gh auth token`, and is checked with `GET /user`
   (`internal/github` `User`).
3. `POST enroll`: token, name (default: host name), OS and arch
   (`runtime`), CPU count, memory (`internal/host`, 0 where unread), the
   `jig` version, the engine version (`engine.Configured`).
4. Writes `<config>/jigz/hub.json`: hub URL, daemon id, name, credential,
   runner label, tracker token. Directory 0700; file written as a 0600
   temporary file and renamed. `jig daemon` refuses a `hub.json` readable
   by group or others.
5. Installs and starts the service unless `--no-service`. A second
   `join` replaces `hub.json` and the unit after a prompt (`--yes`).

Linux: `~/.config/systemd/user/jigd.service`, `systemctl --user
daemon-reload`, `systemctl --user enable --now jigd`. Unit:
`ExecStart=<jig> daemon` (`os.Executable()`, symlinks resolved),
`Restart=on-failure`, `Environment=PATH=<PATH at join>` (agent CLIs,
`gh`, `git`), `KillMode=mixed` (only the daemon gets SIGTERM and stops
its children), `TimeoutStopSec=120` (child grace is 60 s),
`WantedBy=default.target`. If linger is off (`loginctl show-user`),
`join` prints the `loginctl enable-linger` command without running it.
Without a user systemd session, `join` stops and names `--no-service`.

macOS: `~/Library/LaunchAgents/<label>.plist` (decision 12):
`ProgramArguments` `[<jig>, daemon]`, `RunAtLoad`, `KeepAlive`
`{SuccessfulExit: false}`, `ExitTimeOut` 120, `PATH`, output to
`<state>/jigz/jigd.log`; `launchctl bootstrap gui/<uid> <plist>`. It runs
while the user is logged in.

`internal/service` renders both files and runs commands through an
injected runner. `jig daemon` runs in the foreground and logs to
`<state>/jigz/jigd.log` (`openRotatingLog`). `jig leave` (decision
13) removes the service and `hub.json` and keeps the loop roots.

### 2.3 Config pull

- `GET config` with `If-None-Match`. A 200 body and its ETag are stored
  in `<state>/jigz/hub/config.json` (0600) before applying; 304 changes
  nothing. Fetched at start, when a heartbeat response carries another
  ETag, and on `reload_config`.
- Apply per loop: write the new files to `<root>/.jigz.next/`, run
  `sortie validate --format json` on the workflow, and on success mark
  the loop pending. A stopped or sleeping loop gets the files at once
  (rename over `config.yaml` and `workflows/`, then SIGHUP so `sleep` is
  re-read). A running loop gets them when `loopQuiet` reports it idle,
  then `restart_when_idle` (SIGUSR2).
- An invalid workflow is not applied; the loop keeps its files and the
  error becomes `last_error`.
- A removed project or workflow is drained and stopped; its root stays.
- Hub unreachable: run on the stored config. No stored config: no loops,
  retry.
- The daemon creates no labels; the hub does (protocol 3 and 5).

### 2.4 Loops

Processes: `jigd` → `jig daemon` → one `jig --no-dashboard <root>` per
started loop → one engine process. The daemon manages children with
`internal/supervise.Supervisor`, which has no production caller today
and is keyed by a fleetconfig repository. WP6 keys it by
`<project>/<workflow>` with `source.Loop` values and drops its
materialization steps (`plan.Apply`, `jig setup`, `ApplySettings`).
Children get `JIG_REGISTRY_DIR=<state>/jigz/hub` (new, read by
`registryPath`), so their registry and status files are apart from the
unite registry, and `JIG_ENGINE_BIN` for the engine the daemon resolved.

Filter for (project, workflow), written as `filters.<workflow>`:

```
<workflow tracker.query_filter> <project filter additions> label:runner:<name>
```

with `assignee: <member login>` (decision 1). The hub source appends
the runner label last; a unit test fails on any filter without it.
Separate search qualifiers are ANDed, so it narrows the shipped OR list.

- Start: no loop runs; the fallback search runs once.
- `wake {project, workflow}`: start the child, or create its wake file
  (the `jig wake` path) when it is sleeping.
- Idle stop: each root has `sleep.enabled: true`, `idle_timeout: <idle
  time>`, `wake_interval: 24h`. The existing sleep code stops the engine
  process after the idle time; the 24 h wake is a backstop. Idle time
  and fallback interval are machine settings (decision 6).
- Fallback search: every fallback interval, and every minute while the
  hub is unreachable, one `GET /search/issues` per group of projects,
  `q=state:open label:runner:<name> repo:<a> repo:<b> ...`, groups kept
  under the 256-character query limit. Each result wakes the workflows
  whose filter labels (`plan.Workflow.FilterLabels`) and item kind fit.
  API base: the issues workflow's `tracker.endpoint`, else
  `https://api.github.com`; tracker token; `internal/github` back-off.
- `pause`: drain every loop (SIGUSR1) and ignore `wake`, `start` and
  fallback results until `resume`; the flag is kept in
  `<state>/jigz/hub/state.json` (decision 4). `resume` clears it and
  runs the fallback search.
- `start`, `stop`, `drain`, `restart_when_idle` take an optional project
  and workflow (default: all) and map to start, SIGTERM, SIGUSR1,
  SIGUSR2 of the child.

### 2.5 Status, run events and the command channel

Requests carry `Authorization: Bearer <credential>`, `Jig-Daemon-Id` and
`Jig-Version` (decision 8).

Heartbeat, `POST status` every 30 s or at the returned interval. Built
from each child's status document (`readStatusDoc(root)`), each running
loop's `/api/v1/state` (running, retrying), the machine gate (hours open,
next change), `pool.json` (entry name, runtime, available or exhausted
until) and each loop's budget. Loop state: `running`, `sleeping`, or
`stopped` (no child), plus `detail` (`paused`, `held`, `disabled`,
`exited`, `stopping`) and the `hold` text (decision 2). Capacity per
decision 7. `last_error`: the latest validation, child or hub
error, redacted (2.9). The same document goes to
`<state>/jigz/hub/status.json`; `jig status --json` with no root prints
it while `hub.json` exists. Per-root status documents are unchanged.

Run events: the daemon runs `collector.Collector` over the loop roots
into its own store, as `startHistory` does for the dashboard. New table
`hub_cursor (project, workflow, run_id)`. Every 30 s and at shutdown,
`POST runs` sends up to 100 finished runs past the cursor: work item
(project, number, kind), start and finish times, outcome class
(`internal/store/outcome.go`), tokens (input, output, total, measured),
no cost (the engine records none), branch `auto/<number>` for issue runs
(decision 9). The cursor moves past accepted ids only. A `started`
event is sent once per (loop, item, attempt) seen in `/api/v1/state`,
tracked in memory.

Command channel: one WebSocket to `/api/daemon/v0/ws` with the same
headers. Shape (decision 3):

```json
{"id": "c-1", "type": "wake", "args": {"project": "acme/widgets", "workflow": "issues"}}
{"id": "c-1", "type": "ack", "ok": true}
{"id": "c-2", "type": "ack", "ok": false, "error": {"code": "unsupported", "message": "session.open"}}
```

Phase 5 and 6 commands and unknown types get `unsupported`; bad
arguments get `invalid`. Each command is logged with id and type only.

Reconnect: exponential back-off from 1 s to 60 s with full jitter, reset
after 60 s connected; ping every 30 s, close after 90 s without pong.
Heartbeats and run batches run on their own schedules over HTTPS. A 401
or 403 drains every loop, stops the fallback search, logs one line
naming `jig join`, and retries every 10 min (decision 10). A 400
naming the supported protocol range is logged and retried every 10 min.

WebSocket client: `go.mod` has no WebSocket library and none is added
(decision 11). `internal/wsclient` implements the part of RFC 6455 the
daemon needs with the standard library only: client handshake over an
`net/http` upgrade (TLS through the transport), text frames, ping and
pong, close handshake, fragmentation, client-side masking, a read size
limit, and `context` cancellation. It also has a server-side `Accept`
(upgrade through `http.Hijacker`) that its tests and the fake hub use.
`github.com/coder/websocket` is a candidate replacement and needs user
approval.

### 2.6 Default branch

`cmd/jig/workflows` has no hard-coded `main` or `master`: `build.md` and
`docs.md` use `gh api repos/$SORTIE_TRACKER_PROJECT --jq
.default_branch`; the `WORKFLOW.issues.md` hooks read
`refs/remotes/origin/HEAD` (set by `git clone`); `merge.md`, `review.md`
and `review-fix.md` use the PR's `baseRefName`. Found, fixed in WP8:

- All seven prompts name the developer path `/home/coder/jigz` in their
  workspace rule. Replaced by "never run git outside
  `$SORTIE_WORKSPACE`".
- `quick.md` merges into the base branch and pushes it without a PR. No
  shipped workflow uses it (decision 14).

A test in `cmd/jig/embed_test.go` fails when a shipped file names
`main` or `master` as a branch (`origin/main`, `--base main`, ...) or a
home path.

### 2.7 No local dashboard

`jig daemon` starts no dashboard and no question scanner.

- The Questions page is not available for hub projects; the hub lists
  questions and writes answers (protocol 4). Prompts are the same in both
  modes. Until the hub writes answers, a parked item needs a person to
  write the `agent-answer` comment and change the labels on GitHub.
- The `github:` block (`enrich`, `api_url`, `questions_*`) is dashboard
  only; the hub source does not write it.
- A standalone or united run on the same machine keeps its dashboard and
  does not show the daemon's loops (separate registry; decision 15).

### 2.8 Protocol schemas, Go types and the fake hub

No schema exists; the protocol adds them under `schemas/hub-protocol/v0/`
"when implementation starts". WP0 writes JSON Schema 2020-12 files for
`enroll.request`, `enroll.response`, `config.response`,
`status.request`, `status.response`, `runs.request`, `runs.response`,
`ws.command`, `ws.ack` and `error`, with `examples/<message>.json`, in
one PR with the protocol text, reviewed by both streams first
(`overview.md` 6).

Go types are hand-written in `internal/hubproto`, with no generator and
no JSON Schema validator dependency; `github.com/santhosh-tekuri/jsonschema`
is a candidate for a test-only validator and needs user approval. A
contract test reads each schema with `encoding/json` and checks: every
property is a JSON field and the reverse; required properties have no
`omitempty`; enums (outcome classes, loop states, command types) equal
the Go constants; every example decodes with `DisallowUnknownFields` and
encodes to the same JSON (decision 5).

`internal/fakehub` follows `internal/fakegithub`: an in-memory
`http.Handler` with exactly the protocol routes and the socket (served
with `internal/wsclient`'s `Accept`). It
decodes bodies into `internal/hubproto` types with
`DisallowUnknownFields`, rejects a wrong bearer, missing headers and
unknown routes, and lists rejections in `Unexpected()`. Test API:
`New(enrollToken)`, `SetConfig(cfg)` (new ETag), `Send(cmd) (ack,
error)`, `Statuses()`, `Runs()`, `Enrollments()`, `Connected()`,
`SetDown(bool)` (503 everywhere, socket closed), `Revoke()`.

### 2.9 Security

- The enrollment token is used once and not stored. The credential and
  tracker token are only in `hub.json` (0600, directory 0700) and reach
  children through `JIG_TOKEN` and the existing 0600 env file.
- No secret in logs or errors: errors name method and route, never
  headers; `last_error` and logged hub text pass one redaction function
  for the credential, tracker token and enrollment token.
- The hub can run the commands in rule 6 and serve workflows and
  prompts. Workflow `hooks` are shell commands and prompts drive an agent
  with the member's logins and GitHub token, so enrolling gives the hub's
  operators code execution as the member's user. The README states it;
  the status reports the hash of every applied file (decision 16).
- No agent credentials, `machine.yaml` env values or tokens go to the
  hub; pool entries are reported by name and runtime.
- Phase 6 commands are rejected, so a hub cannot start a session.

## 3. Work packages

Lanes as in `plans/jigz/questions.md` section 3. The lead runs the final
gate: `gofmt -l cmd internal` empty, `go vet ./...` (also `-tags
acceptance`), `go test ./...`, `go build ./...` with the darwin
cross-build.

| WP | Lane | Files owned | Depends on |
| -- | ---- | ----------- | ---------- |
| 0 | complex | `plans/system/hub-protocol.md`, `schemas/hub-protocol/v0/*` | none |
| 1 | feature | `internal/hubproto/*` | 0 |
| 2 | feature | `internal/wsclient/*` | 1 |
| 3 | tests | `internal/fakehub/*`, `internal/fakegithub/query.go`, `internal/fakegithub/query_test.go` | 1, 2 |
| 4 | feature | `internal/hubclient/*` | 1, 2, 3 |
| 5 | complex | `internal/source/*`, `cmd/jig/embed.go` | 1 |
| 6 | complex | `internal/supervise/*`, `cmd/jig/daemon.go`, `cmd/jig/daemon_test.go`, `cmd/jig/unite.go`, `internal/store/store.go`, `internal/store/hub.go`, `internal/store/hub_test.go` | 4, 5 + review |
| 7 | feature | `cmd/jig/join.go`, `cmd/jig/join_test.go`, `internal/service/*`, `cmd/jig/main.go`, `cmd/jig/args_test.go`, `cmd/jig/status_cmd.go` | 4 |
| 8 | mechanical | `cmd/jig/workflows/prompts/*`, `cmd/jig/workflows/*quick*`, `cmd/jig/embed_test.go` | none |
| 9 | tests | `cmd/jig/acceptance_test.go` | 3; green after 6, 7 |
| 10 | mechanical | `README.md`, `plans/jigz/features.md`, `plans/jigz/handoff.md` | 9 |

WP8 starts at once. After WP0: WP1 and WP5 in parallel, then WP2, WP3 and
WP4 in sequence; WP6 and WP7 in parallel. WP9 is written beside WP4 and
fails until WP6 and WP7 land.

**WP0. Protocol and schemas.** Write decisions 1 to
10 and 18 into `hub-protocol.md`, and the schemas and examples of 2.8.
Research at the pinned engine tags: does the engine re-read its workflow
file while running; does the `github` tracker send `query_filter` to
search (the current acceptance test shows it on v1.28.1). Acceptance:
both streams approve.

**WP1, WP3.** Per 2.8, with unit tests per route and rejection; in
`internal/fakegithub`, several `repo:` terms are an OR, as on GitHub.

**WP2. WebSocket client.** Per 2.5. Unit tests against an in-process
server (`Accept`) and a conformance-style set: client frames masked,
fragmented messages reassembled, ping and pong between fragments,
oversized frame rejected by the read limit, close handshake in both
directions, context cancellation. **Review pass (review2) on WP1 to
WP3.**

**WP4. Hub client.** Per 2.5. Tests against the fake hub: 304, ETag
change, 401, back-off bounds with an injected clock, no secret in errors.

**WP5. Config source.** Per 2.1, 2.3, 2.4. Tests: runner label in every
filter, `.jigz.next` swap, invalid workflow kept out, removed project,
no `syncWorkflows` on a hub root, `jig <root>` unchanged.
**Review pass (review2) on WP4 and WP5 before WP6.**

**WP6. Daemon.** `runDaemon(args) int`: the reworked
`internal/supervise`, `JIG_REGISTRY_DIR`, wake, idle stop, fallback
search, command mapping, pause state, status document, run events with
`hub_cursor` (store migration), and stopping every child on SIGTERM.
Tests with the fake hub and a stub child binary. **Review pass (review2)**
on child cleanup, apply-when-idle and the filter.

**WP7. Join and service.** Per 2.2, dispatch and usage in `main.go`,
`jig status --json` in connected mode. Tests: unit and plist, refusals
(group-readable `hub.json`, non-loopback `http://`), token from stdin.

**WP8** per 2.6, including deleting `quick.md`. **WP9** per section 4; it
fails on `main`. **WP10:**
README connected mode section (join, service, linger, files and
permissions, what the hub can run on the machine, `jig leave`), no
comparison with other modes; `features.md`, handoff; final review2 pass.

## 4. Acceptance test

`TestAcceptanceConnected` in `cmd/jig/acceptance_test.go`, pinned default
engine, `mock` agent, set up as `TestAcceptance` (temporary `HOME` and
XDG directories, `JIG_ENGINE_BIN`, fake GitHub `acme/widgets` with token
owner `octo`) plus a fake hub. Hub config: project `acme/widgets`, the
issues workflow passed through `acceptanceWorkflow`, runner label
`runner:ci-1`, member `octo`. `machine.yaml`: idle time 30 s, fallback
interval 20 s.

1. `jig join <fakehub-url> - --no-service`, enrollment token on stdin,
   `JIG_TOKEN` set. One enrollment recorded with OS, arch, CPU count,
   `jig` and engine versions; `hub.json` 0600, its directory 0700.
2. Start `jig daemon`. Within 40 s: `GET config`, a heartbeat listing
   `acme/widgets`/`issues` as `stopped`, a connected socket; no engine
   process (`groupMembers`).
3. Seed A (`agent:plan`, `octo`, `runner:ci-1`), B (`agent:plan`,
   `octo`, no runner label), C (`agent:plan`, `octo`, `runner:other`).
4. `Send(wake acme/widgets issues)` acks `ok`. A is handed off
   (`assertHandedOff`); every engine search contains
   `label:runner:ci-1`; B and C get no label events.
5. `POST runs` carries A's run: `succeeded`, 100 input and 50 output
   tokens, work item `acme/widgets#A`; no later batch repeats it.
6. Within 90 s a heartbeat reports the loop `sleeping`; no engine process.
7. `SetDown(true)`; seed D (`agent:plan`, `octo`, `runner:ci-1`). Within
   60 s a search `state:open label:runner:ci-1 repo:acme/widgets` runs,
   the loop starts and D is handed off.
8. `SetDown(false)`; `SetConfig` adds the filter addition
   `-label:backlog`. After the next heartbeat the config is fetched; the
   next engine search has `-label:backlog` and `label:runner:ci-1`; the
   status shows the new workflow hash.
9. `pause`: the loop drains; seed E (`runner:ci-1`); no label event on E
   over two fallback intervals. `resume`: E is handed off.
10. `Send(session.open ...)` acks `ok: false`, code `unsupported`. The
    daemon process has no listening socket.
11. SIGTERM to the daemon: exit within 120 s, no child or engine left.
12. `gh.Unexpected()` and `hub.Unexpected()` are empty; the credential,
    enrollment token and `JIG_TOKEN` are in no log and in no request
    body the fake hub recorded.

The service install is covered by WP7 unit tests, not CI.

## 5. Risks and decisions

Risks: one `jig` and one engine process per started loop (idle stop
limits it to loops with work); an item with the runner label that
matches no enabled workflow is never claimed (logged once); if the
engine re-reads its workflow file during a run (WP0), a run claimed in
the 2 s between idle check and swap sees new files; hub-served hooks run
as the member (2.9); parked items need manual answers until the hub
writes them; enroll reports memory 0 on macOS (`internal/host`).

Decisions. 1 to 10 and 18 resolve gaps or conflicts between the roadmap,
the protocol and the code.

1. **Assignee.** The member login from config, checked against the tracker token's login at join.
2. **Loop states.** `stopped`, `sleeping`, `running` plus optional `detail` and `reason`; per-root status documents stay.
3. **Command envelope.** As in 2.5, with codes `unsupported`, `invalid`, `failed`.
4. **`pause`.** Drain all loops and ignore wakes until `resume`; the flag is persisted.
5. **Schemas.** Held here (WP0), with hand-written types and the 2.8 contract test; no validator dependency.
6. **Idle time, fallback interval.** `machine.yaml` `connected.idle_timeout` (15m) and `connected.fallback_interval` (5m).
7. **Capacity.** `max_sessions` (sum of `max_concurrent_agents`) and `running_sessions`.
8. **Id and version on requests.** Headers `Jig-Daemon-Id` and `Jig-Version`.
9. **Run links.** Optional; the branch only.
10. **Revoked credential.** As in 2.5.
11. **WebSocket.** `internal/wsclient`, standard library only; `github.com/coder/websocket` needs user approval.
12. **launchd label.** `io.github.kinged007.jigd`.
13. **`jig leave`.** Added.
14. **`quick.md`.** Deleted.
15. **Standalone beside the daemon.** Allowed; shared state is `machine.yaml`, `pool.json` and the port lock.
16. **Trust in hub-served files.** Accepted, documented, applied file hashes reported.
17. **Budgets and cleanup.** Not in hub config in Phase 4.
18. **Runner label on agent PRs.** The hub labels agent PRs on sync.

Code finding outside this scope: `internal/supervise.Supervisor` and the
`repos:` resolution in `internal/fleetconfig` have no production caller
(WP6 reuses the first).

### Engine facts

Read from the engine source at the pinned tags, `v1.28.1` (default,
`kinged007/sortie`, `internal/engine/engine.go` `DefaultTag`) and
`v1.29.0` (upstream, `TestAcceptanceUpstreamEngine`). Line numbers are
`v1.28.1`, then `v1.29.0` where they differ.

**The engine re-reads its workflow file while running.** Two paths:

- `workflow.Manager.Start` watches the workflow file's directory with
  `fsnotify` and reloads on a write or create of the file, after a 50 ms
  debounce (`internal/workflow/manager.go:323`, `:378`; `:402`, `:457`).
  `cmd/sortie/boot.go:194` (`:191`) starts it.
- Every poll tick runs a defensive `Reload()` in preflight before
  dispatch (`internal/orchestrator/orchestrator.go:927`; `:948`). It
  reads the workflow by path, so it also picks up a file whose directory
  was renamed over, which the watcher misses.

A reload re-reads the workflow and the prompt and partial files it names
(`manager.go:442`; `:499`). A failed reload keeps the last good config.
Agent settings are resolved once per attempt, so a reload reaches a claim
already held at its next attempt
(`internal/orchestrator/attempt_settings.go:23`; `:24`). For 2.3: files
swapped under a running engine take effect at its next tick, whether or
not the daemon restarts it.

**The `github` tracker sends `query_filter` to the search API.** With a
non-empty filter, `FetchCandidateIssues` calls `GET /search/issues` with
`q=repo:<owner>/<repo> type:issue state:open <query_filter>`
(`internal/scm/github/tracker.go:182`, `:239`, same lines at both tags);
with an empty filter it lists `/repos/{owner}/{repo}/issues` with no
filter. The `github-pr` tracker (`v1.28.1` only; `v1.29.0` has none)
does the same with `type:pr` (`internal/scm/githubpr/tracker.go:142`,
`:164`). The adapter reads `query_filter` when it is built
(`tracker.go:125`), once at boot (`cmd/sortie/boot.go:267`; `:265`), and
a workflow reload does not rebuild it. `jig` passes the filter as
`SORTIE_TRACKER_QUERY_FILTER` (`cmd/jig/supervise.go:201`), so a filter
change reaches the engine only when its process restarts.
