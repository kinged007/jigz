# Hub protocol

Status: draft v0 (2026-10-21)

The contract between a connected `jig` daemon and the hub, and the
conventions both products share on the git provider. Both agent streams
implement against this document. Changes follow `overview.md` section 6.

JSON Schemas for every message are added under `schemas/hub-protocol/v0/`
when implementation starts; until then this document is the reference.

## 1. Transport and auth

- The daemon opens every connection. The hub never connects to a daemon.
  No inbound ports on the daemon's machine.
- HTTPS for requests; one WebSocket per daemon for commands and agent
  sessions.
- Base path `/api/daemon/v0/`.
- Enrollment exchanges a one-time token for a daemon credential (bearer
  token). The credential can be rotated and revoked from the hub.
- Every request carries the daemon id and its `jig` version.
  The hub rejects unsupported protocol versions with an error that names
  the supported range.

## 2. Messages

### 2.1 Daemon → hub (HTTPS)

| Request | Body | Response |
| ------- | ---- | -------- |
| `POST enroll` | one-time token, daemon name, host info (OS, arch, CPU, memory), `jig` version, sortie version | daemon id, credential, runner label |
| `GET config` (with ETag) | none | projects (provider, repo, enabled workflows per project, query filter additions per project), workflow and prompt files with versions, label conventions, member identity |
| `POST status` (heartbeat, every 30 s) | status document: loops per project (project, workflow, state: stopped, sleeping, or running; running, retrying), capacity, working hours and whether inside them now, agent accounts (name, runtime, available or exhausted until), budgets, last error | next heartbeat interval; config ETag (daemon re-fetches when it changes) |
| `POST runs` (batch) | run events: started, finished, outcome class, usage (tokens, cost if known), work item reference, links (branch, PR, comment) | accepted ids |
| `POST previews` | work item reference, URL, expiry, access kind | preview id |
| `DELETE previews/{id}` | none | none |

Run outcome classes are an enumeration: `succeeded`, `failed_agent`,
`failed_workspace`, `failed_limit` (agent account limit), `cancelled`,
`timeout`, `handoff_withheld`. The daemon maps sortie's errors to these;
the hub never parses sortie error text.

### 2.2 Hub → daemon (WebSocket)

| Command | Effect on the daemon |
| ------- | -------------------- |
| `start`, `stop`, `drain`, `restart_when_idle` | per loop or all loops |
| `pause`, `resume` | stop or resume claiming new work |
| `wake {project, workflow}` | start or wake that loop; the daemon stops it when it is drained and idle for a configured time |
| `reload_config` | fetch config and apply when loops are idle |
| `session.open` | start a conversation session (runtime, project, agent name and instructions, MCP config, session id) |
| `session.message` | send a user message to a session |
| `session.close` | end a session |
| `task.run` | run a one-shot session (agent, project, instructions); the result is returned on the socket |

Daemon → hub on the same socket: command acknowledgements and results,
`session.output` (streamed text and tool-call summaries) per session, and
the result of each `task.run`.

## 3. Routing labels

- Runner label: `runner:<name>`, one per daemon, created in each project
  by the hub. `<name>` is short and unique per organization (labels are
  limited to 50 characters on GitHub).
- Only the hub writes runner labels.
- A connected daemon adds its runner label to every workflow's query
  filter as a `label:` search qualifier. Prompts that change labels must
  not remove it.

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
