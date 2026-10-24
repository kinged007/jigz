# Hub protocol

Status: draft v0 (2026-10-24)

The contract between a connected `jig` daemon and the hub, and the
conventions both products share on the git provider. Both agent streams
implement against this document. Changes follow `overview.md` section 6.

JSON Schemas (draft 2020-12) for every message are in
`schemas/hub-protocol/v0/`, with valid and invalid examples. This
document and the schemas change in the same PR; where they differ, the
change is not done.

## 1. Transport and auth

- The daemon opens every connection. The hub never connects to a daemon.
  No inbound ports on the daemon's machine.
- HTTPS for requests; one WebSocket per daemon for commands and agent
  sessions.
- Base path `/api/daemon/v0/`. The path carries the protocol version.
- Enrollment exchanges a one-time token for a daemon credential (bearer
  token). The credential can be rotated and revoked from the hub.

Headers on every request and on the WebSocket upgrade:

| Header | Value | On `POST enroll` |
| ------ | ----- | ---------------- |
| `Authorization` | `Bearer <credential>` | absent |
| `Jig-Daemon-Id` | daemon id from enrollment | absent |
| `Jig-Version` | the daemon's `jig` version | present |

Request and response bodies are JSON (`Content-Type: application/json`).
Unknown fields are an error on both sides in v0.

### 1.1 Errors

Every error response has the body of `error.schema.json`:
`code`, `message`, and `supported` when `code` is `unsupported_version`.

| Status | `code` | Meaning | Daemon behaviour |
| ------ | ------ | ------- | ---------------- |
| 400 | `invalid` | body or arguments do not match the schema | log, do not retry the same body |
| 400 | `unsupported_version` | the path's protocol version is not served; `supported` names the range (`{"min": 0, "max": 0}`, major versions) | log, retry every 10 min |
| 401 | `unauthorized` | missing, unknown or rotated-out credential | revoked-credential behaviour |
| 403 | `forbidden` | revoked credential or daemon | revoked-credential behaviour |
| 404 | `not_found` | unknown route or id | log |
| 503 | `unavailable` | hub down or in maintenance | run on stored config, retry |
| 500 | `internal` | hub fault | retry with back-off |

`message` is for logs. The daemon never parses it and passes it through
its redaction function before logging it.

Revoked-credential behaviour (401 or 403 on any request or on the
WebSocket upgrade): the daemon drains every loop, stops the fallback
search, logs one line that names `jig join`, and retries the request
every 10 min. It does not delete its credential.

### 1.2 Reconnect and timing

- WebSocket: exponential back-off from 1 s to 60 s with full jitter,
  reset after 60 s connected. The daemon sends a ping every 30 s and
  closes the socket after 90 s without a pong.
- Heartbeats and run batches run on their own schedules over HTTPS,
  whether or not the socket is connected.
- No hello message: the headers on the upgrade identify the daemon. The
  first message on the socket is a command from the hub.

## 2. Messages

### 2.1 Daemon → hub (HTTPS)

| Request | Body | Response | Schemas |
| ------- | ---- | -------- | ------- |
| `POST enroll` | one-time token, daemon name, host info (OS, arch, CPU count, memory), `jig` version, engine version, tracker login | daemon id, credential, runner label | `enroll.request`, `enroll.response` |
| `GET config` (with `If-None-Match`) | none | 200 with `ETag`: projects, workflow and prompt files, labels, member; 304 when unchanged | `config.response` |
| `POST status` (heartbeat, every 30 s or the returned interval) | status document | next heartbeat interval, config ETag | `status.request`, `status.response` |
| `POST runs` (batch, at most 100 events) | run events | accepted event ids | `runs.request`, `runs.response` |
| `POST previews` | work item reference, URL, expiry, access kind | preview id | Phase 5 |
| `DELETE previews/{id}` | none | none | Phase 5 |

**Enroll.** `token`, `name`, `host` (`os`, `arch` as Go reports them,
`cpus`, `memory_bytes`, 0 where the daemon cannot read it),
`jig_version`, `engine_version`, `tracker_login` (the login of the
tracker token on the machine). The hub refuses with `invalid`, and does
not use up the token, when `tracker_login` is not the login of the
member the token was issued for. The response has `daemon_id`,
`credential` and `runner_label` (`runner:<name>`). The enrollment token
is used once; the daemon does not store it.

**Config.** `member.login` is the member's login on the provider; every
loop uses it as its `assignee`. `projects` lists, per project:
`provider` (`github`), `repo` (`owner/name`), `workflows` (enabled
workflow names, each the stem of a `WORKFLOW.<name>.md` file in
`files`) and `filter_additions` (search qualifiers appended to each
workflow's query filter, may be empty). `files` holds the workflow and
prompt files: `path` relative to `.jigz/workflows/`, `sha256` of the
content (the file's version), `content`. `labels` lists the label
definitions (`name`, `color`, `description`) the hub creates in each
project; the daemon creates no labels.

The config does not carry idle time, fallback interval, budgets or
cleanup settings. Idle time and fallback interval are machine settings
(`machine.yaml` `connected.idle_timeout`, default 15m, and
`connected.fallback_interval`, default 5m). Budgets and cleanup are not
set by the hub in v0.

**Status.** One document per heartbeat:

- `time`: when the daemon built it.
- `paused`: whether the daemon is paused (2.2).
- `loops`: one entry per (project, workflow) in the applied config:
  - `project`, `workflow`.
  - `state`: `running` (the loop's child process runs), `sleeping` (the
    child runs and its engine is stopped by idle sleep), `stopped` (no
    child).
  - `detail` (optional): `paused`, `held` (outside working hours or held
    by a budget), `disabled`, `exited` (the child ended on its own),
    `stopping`.
  - `reason` (optional): free text for `detail`, such as the hold text.
  - `running`, `retrying`: counts of work items from the engine.
  - `files`: `path` and `sha256` of every file applied to the loop.
  - `budget` (optional): `period`, `max_tokens`, `used_tokens`.
- `capacity`: `max_sessions` (the sum of `max_concurrent_agents` over
  the enabled loops) and `running_sessions` (agent sessions running now).
- `hours`: `open` (inside working hours now; `true` when no hours are
  set) and `next_change` (optional).
- `accounts`: agent accounts by `name` and `runtime`, `available`, and
  `exhausted_until` when not available. No credentials.
- `last_error` (optional): the latest validation, child or hub error,
  redacted.

The response has `interval_seconds` (next heartbeat) and `config_etag`;
the daemon fetches config when the ETag differs from the stored one.

**Runs.** `events`, each with:

- `id`: unique per daemon; the hub ignores an id it has accepted before
  and returns it again in `accepted`.
- `type`: `started` (once per loop, work item and attempt) or `finished`.
- `workflow`, `item` (`project`, `number`, `kind`: `issue` or `pr`),
  `attempt`, `started_at`.
- `finished` events also carry `finished_at`, `outcome` (an outcome
  class) and `usage` (`input_tokens`, `output_tokens`, `total_tokens`,
  `measured`: whether the agent reported the counts, `cost_usd` when
  known).
- `links` (optional): `branch`, `pr`, `comment`. Every link is optional;
  a `jig` daemon sends `branch` (`auto/<number>`) for issue runs.

The response lists the `accepted` ids. The daemon resends events that
are not accepted.

Run outcome classes are an enumeration: `succeeded`, `failed_agent`,
`failed_workspace`, `failed_limit` (agent account limit), `cancelled`,
`timeout`, `handoff_withheld`. The daemon maps sortie's errors to these;
the hub never parses sortie error text.

### 2.2 Hub → daemon (WebSocket)

Text frames, one JSON message per frame. A command (`ws.command`) has
`id` (unique per connection), `type` and `args` (an object, `{}` when
the type takes none). The daemon answers each
command with one acknowledgement (`ws.ack`) carrying the same `id`:

```json
{"id": "c-1", "type": "wake", "args": {"project": "acme/widgets", "workflow": "issues"}}
{"id": "c-1", "type": "ack", "ok": true}
{"id": "c-2", "type": "ack", "ok": false, "error": {"code": "unsupported", "message": "session.open"}}
```

Error codes in an acknowledgement:

- `unsupported`: the daemon does not implement the type. A v0 `jig`
  daemon answers this for `session.*`, `task.run` and any type not in
  the table.
- `invalid`: the arguments do not fit the type (unknown project or
  workflow, missing or extra fields).
- `failed`: the command was valid and the daemon could not carry it out;
  `message` says why.

| Command | `args` | Effect on the daemon |
| ------- | ------ | -------------------- |
| `start`, `stop`, `drain`, `restart_when_idle` | optional `project`, and `workflow` only with `project` (default: all) | start, stop, drain, or restart when idle the matching loops |
| `pause` | `{}` | drain every loop and ignore `wake`, `start` and fallback search results until `resume`; the flag survives a daemon restart |
| `resume` | `{}` | clear the pause flag and run the fallback search |
| `wake` | `project`, `workflow` | start that loop, or wake it when it is sleeping; the daemon stops it after it has been idle for the machine's idle time |
| `reload_config` | `{}` | fetch config and apply it to each loop when that loop is idle |
| `session.open` | Phase 6, any object | start a conversation session (runtime, project, agent name and instructions, MCP config, session id) |
| `session.message` | Phase 6 | send a user message to a session |
| `session.close` | Phase 6 | end a session |
| `task.run` | Phase 6 | run a one-shot session (agent, project, instructions); the result is returned on the socket |

An acknowledgement with `ok: true` means the daemon accepted the command,
not that the loops have reached the new state; the next heartbeat shows
that. The daemon logs each command by id and type only.

Daemon → hub on the same socket: command acknowledgements, and in Phase
6 `session.output` (streamed text and tool-call summaries) per session
and the result of each `task.run`.

### 2.3 Loop selection

The daemon builds each loop's query filter as

```
<workflow tracker.query_filter> <project filter_additions> label:runner:<name>
```

with `assignee` set to `member.login`. Without hub contact, every
fallback interval (every minute while the hub is unreachable) the
daemon runs one provider search for open items with its runner label in
its projects and wakes the loops whose filters match a result.

## 3. Routing labels

- Runner label: `runner:<name>`, one per daemon, created in each project
  by the hub. `<name>` is short and unique per organization (labels are
  limited to 50 characters on GitHub).
- Only the hub writes runner labels.
- A connected daemon adds its runner label to every workflow's query
  filter as a `label:` search qualifier. Prompts that change labels must
  not remove it.
- Agents do not add the runner label to the pull requests they open.
  When the hub syncs a project, it adds to each agent pull request the
  runner label of the issue the pull request was opened for.

## 4. Question and answer format

Agents write questions inside a comment (plan, review, or any other) in
this form:

```markdown
<!-- agent-question id="q-<short-id>" audience="product|project" blocking="true|false" resume="agent:<stage>" -->
**Question:** ...
**Suggested answer:** ...
<!-- /agent-question -->
```

Example start marker:
`<!-- agent-question id="q-7f3a" audience="project" blocking="true" resume="agent:plan" -->`.

- `audience`: `product` (scope, priority, user-facing behaviour) or
  `project` (technical approach, implementation, order of work).
- `resume`: the trigger label that dispatched the run, an `agent:*` label
  other than `agent:done`. Required when `blocking="true"`, optional
  otherwise. A blocking question without it is malformed.
- A blocking question escalates the item to the `needs-human` label. Every
  escalation to `needs-human` that waits for an answer carries at least one
  blocking question in its comment. An escalation with no question (for
  example, a PR that a person must review) has no block.
- At handoff the engine adds `agent:done` and removes every other state
  label: the trigger, `in-progress` and any terminal label. It does not
  touch `needs-human`. A parked item therefore carries `needs-human` and
  `agent:done` and no trigger; `resume` records the trigger.

Answers are comments in this form, written by the hub, by `jig`,
or by a human:

```markdown
<!-- agent-answer id="q-<short-id>" by="<user or agent role>" -->
...
<!-- /agent-answer -->
```

- When an item has at least one blocking question and every blocking
  question has an answer, the writer of the last answer removes both
  `needs-human` and `agent:done`, one `DELETE` call per label, so no
  other label is overwritten, then adds the label named by `resume` unless
  the item already carries a trigger. The next poll dispatches the item as
  a new run. Neither label is removed from an item that has no blocking
  question.
- Prompts instruct agents to read answers before acting and to treat an
  unanswered non-blocking question as answered with its suggestion.

## 5. Shared labels

| Label | Meaning |
| ----- | ------- |
| `agent:<stage>` (plan, plan-build, build, docs, review, merge) | requests a workflow stage |
| `in-progress` | claimed by a running agent |
| `agent:done` | the agent finished its stage |
| `needs-human` | escalated to a person, or waiting for an answer to a blocking question; excluded from dispatch |
| `runner:<name>` | routed to one daemon (section 3) |

The full set and colours are defined by `jig setup`; the hub uses
the same definitions.

## 6. Versioning

- `v0` may change without notice while both products are pre-release.
- From `v1`: additive changes within a major version; removals and
  meaning changes need a new major version, and the hub supports the
  previous major version for at least one release.
- The schemas for a major version are in `schemas/hub-protocol/v<N>/`.
  `jig` holds the canonical copy; Go types in `internal/hubproto` are
  written by hand and a contract test checks them against the schemas.
