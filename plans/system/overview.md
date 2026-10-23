# System overview

Status: draft for discussion (2026-10-21)

The system has two products: Jigz (command `jig`; background service
`jigd`) and Shiftd, the hub, a web platform. It also has
two external dependencies: the sortie engine and a git provider. This
document describes how they fit together and the rules that let each
product be built by its own agent stream.

Document map:

| Path | Contents |
| ---- | -------- |
| `plans/system/overview.md` | this document |
| `plans/system/hub-protocol.md` | contract between a `jig` daemon and the hub, and shared conventions |
| `plans/system/delivery-method.md` | how both products are built by one developer and agents |
| `plans/system/strategy-critique.md` | market, niche, effort, risks |
| `plans/jigz/vision-and-roadmap.md`, `features.md` | `jig` plans |
| `plans/hub/vision-and-roadmap.md`, `features.md` | hub plans |

## 1. Goal of the whole system

Autonomous, verified software work across many projects, by teams of
humans who each bring their own agents. Work is planned and managed in the
hub, recorded in the git provider, and executed on each member's own
machines by `jig`.

## 2. Parts

| Part | Role | Distribution |
| ---- | ---- | ------------ |
| Git provider (GitHub first; GitLab, Gitea, Jira later) | record of issues, PRs, labels, assignees, milestones, comments | external |
| sortie engine | polls the provider, claims work, runs agent sessions | external, pinned version, fork or upstream |
| `jig` | sets up and supervises sortie on one machine; dashboard; optional runner for the hub | open source, free, usable alone |
| Hub | team, projects, agents and their tasks, assignment, inbox, gateway to channels, verification, outcomes, readiness, fleet view | separate product; possibly paid |

Responsibilities:

| Concern | Owner |
| ------- | ----- |
| Issues, PRs, labels, comments (data of record) | git provider |
| Running an agent on an issue or PR | sortie, started by `jig` |
| Machine-local settings: agent CLIs and their logins (account pool), the member's tracker credential, working hours, budgets, workspace mode and root, the member's own tools and skills outside the project repo | `jig`, on the member's machine |
| Running agents, tracker-driven: sortie loops on issues and PRs, used for all code changes | sortie, started by `jig` |
| Running agents, hub-driven: sessions started by the hub without an issue (conversations and one-shot tasks) | the agent session host in `jig`, on the agent's assigned runner |
| Agent credentials and subscriptions | `jig`, on the member's machine; never sent to the hub |
| Project config (projects, enabled workflows, prompts, labels, query filter additions) | local config files when standalone or united; the hub alone when connected |
| Assignment of work to members and machines | humans on the provider; the hub when connected |
| Planning, triage, milestones, sprints, releases | hub |
| Questions, approvals, inbox | hub; `jig` shows open questions locally |
| Evidence, outcomes, readiness | hub; `jig` shows run metrics locally |
| Preview environments | `jig` creates them; the hub lists them and shares them with reviewers |

Rule for both ways of running agents: code changes go through an issue and
a PR. Sessions do coordination and operations and never push to protected
branches. Both ways use the same account pool and working hours.

### 2.1 Product boundary

`jig` shows what one person's machines are doing. The hub shows
what the team is doing and whether the work is proven to work.

The hub holds what depends on team size, history, sharing with people
outside the team, admin controls, or coordination across members.
`jig` keeps everything about one machine.

| Feature area | `jig` | Hub |
| ------------ | ------------- | --- |
| Unite | several repositories on one machine in one dashboard; no cross-machine unite | several machines and members in one fleet view; a solo user with several machines uses the hub (free tier for one member, section 7) |
| Previews | create a preview; long random expiring URL | previews behind sign-in, shared with reviewers outside the team |
| Questions | local list across repositories; answering | routing to product and project manager agents, shared inbox, approvals, daily digest |
| Metrics | local, recent, per-machine run and usage numbers (the current dashboard pages) | long history; comparison across projects, models, and prompt versions; outcome analytics |
| Access | no authentication; bound to 127.0.0.1 by default; for local use, a private network, or behind the user's own secure proxy | sign-in, roles, SSO, audit export and retention |
| Budgets and working hours | per machine; work offline | organization budgets, team schedules, routing to the member whose working window is open |
| Agent account pool and failover | local only, free; credentials never leave the machine | none; the fleet view shows availability only |

No upselling: `jig` is complete as it is. Its dashboard, CLI, and
documentation show no locked, disabled, or "available in the hub"
features, no upgrade prompts, and no links to the hub. The hub is
promoted separately. The only hub-related surface in `jig` is the
`join` command and its documentation of connected mode.

## 3. Modes of `jig`

1. **Standalone.** One machine, its own config, its own dashboard. Work is
   selected by labels and `assignee:@me`.
2. **United.** Several `jig` instances, one per repository, shown
   and controlled in one dashboard on the same machine, without the hub
   (`plans/jigz/united-dashboard.md`).
3. **Connected (runner mode).** `jig` has enrolled with a hub. The
   hub is the only source of project config; local repo configs and the
   unite registry are ignored. The daemon runs without the local
   dashboard; the hub provides the views and controls. It pulls projects,
   enabled workflows, workflow and prompt files, labels, and query filter
   additions from the hub, runs one sortie process per (project,
   workflow), reports status and runs, accepts control commands, and
   filters work by its runner label (4.2). Local settings stay local and
   are reported to the hub as status: agent CLIs and their logins
   (account pool), the member's tracker credential, working hours,
   budgets, workspace mode and root, and the member's own tools and skills
   outside the project repo.

Loops start on demand. The hub sends `wake {project, workflow}` when it
routes an item; the daemon stops a loop that is drained and idle for a
configured time. When the hub is unreachable, and periodically otherwise,
the daemon searches the provider for open items with its runner label
across its projects and starts the matching loops. The last config is
kept.

## 4. How work flows when connected

### 4.1 Enrollment

1. A member signs in to the hub (GitHub App sign-in, limited to members of
   the configured GitHub organization).
2. The hub shows a one-time enrollment token.
3. On their machine: `jig join <hub-url> <token>`. The daemon
   receives an id, a credential, and its runner label, then pulls its
   config.

### 4.2 Assignment and routing

- The hub (its agents, or a human in the hub) assigns a work item to a member. Members can also assign on the provider
  directly; the hub sees it through provider sync.
- The hub picks one of the member's daemons by availability (online,
  working hours, agent accounts not exhausted, free capacity) and adds that
  daemon's runner label, `runner:<name>`.
- A connected daemon's query filter is always `assignee:<member>` plus its
  own runner label plus the workflow labels, also when the member has only
  one daemon. Two daemons never see the same item.
- The hub is the only writer of runner labels, so there is no race between
  daemons. A runner label is moved only while the item is not in progress.
- If no daemon of the member is available, the hub does not add a runner
  label and the item waits.
- Standalone mode does not use runner labels; a member who runs one
  standalone `jig` per project has no overlap.

### 4.3 Execution and reporting

- sortie runs the workflow on the item. `jig` chooses the agent
  account (account pool) and enforces working hours.
- `jig` reports status (heartbeat) and run events to the hub.
- If the change needs human review, `jig` creates a preview
  environment and registers its URL with the hub (4.6).

### 4.4 Questions from agents

- Plan and review prompts write questions in a marked block with an
  audience (`product` or `project`), a suggested answer, and whether the
  question blocks the work (format in `hub-protocol.md` section 4).
- A blocking question escalates the item to the `needs-human` label; the
  item is skipped until it is answered. The question block names the
  trigger label in `resume`, because at handoff the engine adds
  `agent:done` and removes the trigger.
- Standalone: the `jig` dashboard lists open questions.
- Connected: the hub opens an inbox thread addressed to the product or
  project manager role. The role's agent (if configured) and subscribed
  humans can both read and reply.
- The answer is posted to the issue as a marked comment. When every
  blocking question on the item has an answer, `needs-human` and
  `agent:done` are removed and the `resume` trigger label is added, which
  returns the item to dispatch. The next agent run reads the answer from the issue.
- Non-blocking questions: the agent proceeds with its suggested answer and
  records the assumption; a human can override it until the work starts.

### 4.5 Agent sessions

- Hub agents (product manager, project manager, and any other role the hub
  defines) run as Claude Code or `pi` sessions on the agent's one assigned
  runner, using that member's subscription. There is no fallback to other
  runners. Management agents are assumed to run on an always-on runner.
- The hub sends chat messages to the session over the daemon's connection
  (`session.*`) and streams replies back. First version: headless CLI
  turns with session resume. Later: Agent Client Protocol (ACP).
- The session runs in a checkout of the project and uses its own subagents
  for research. The hub provides an MCP server with tools over hub data
  (work items, milestones, inbox, proposals, `send_message`, approval
  requests).
- An approved proposal becomes a spec with acceptance criteria and then
  work items in a milestone.
- The agent session host is internal to connected mode. It is not
  documented in the `jig` user documentation or shown in its CLI
  help.

### 4.5.1 Tasks, governed actions, and CI failures

Hub-driven work with no issue runs as `task.run` on the agent's assigned
runner. Actions such as a release are requested by the agent, approved in
the hub inbox, and performed by the agent with the runner member's
credentials. Failed CI on watched branches is turned into issues by the
hub. Details are in `plans/hub/vision-and-roadmap.md` sections 4.7 to
4.9.

### 4.6 Previews

- `jig` starts the preview (local stack plus tunnel, or the
  sandbox provider's URL) and registers the URL and expiry with the hub.
- `jig` access: long, random, expiring URLs; provider access
  control (Cloudflare Access, Tailscale, Vercel protection) as an option.
- Hub access: the hub acts as an authenticating proxy, so previews are
  behind sign-in and can be shared with reviewers outside the team.

## 5. Engine policy

- `jig` runs on a pinned sortie version, fork or upstream. The pin
  and the binary are configurable.
- Requirements on sortie are kept to a documented minimum
  (`plans/jigz/vision-and-roadmap.md` section 4). Every requirement
  must be met by an upstream release; fork-only features are optional.
- The hub has no dependency on sortie. It talks only to `jig` and
  to the git provider.

## 6. Two agent streams

The products are built by separate agent streams that work independently.

- The only coupling is `hub-protocol.md`. It is versioned; a change to it
  is a separate PR that both streams review before either implements it.
- Each product tests against a fake of the other: `jig` against a
  fake hub, the hub against a fake daemon. Both are generated from the
  protocol's JSON Schemas.
- Each repository has its own `AGENTS.md` that points to its plan
  documents and to this document.
- `jig` is open source and is the canonical home of
  `plans/system/`. The hub repository keeps a copy with the source commit
  in its header; a CI check reports when the copy is older than the
  source.
- Each stream follows `delivery-method.md`.

## 7. Commercial model (idea, not decided)

- `jig` and the protocol stay open source.
- The hub is offered as a hosted service, a paid self-hosted license, or
  both.
- Free tier: one member, which covers a solo user with several machines.
- Consequences for the design now: the hub's data model has organizations
  from the first version (multi-tenant), and the hub never holds members'
  agent credentials, so a hosted hub does not pay for or store inference
  access.

Backlog (not scheduled):

- Hosted runners: the hub starts a sandbox per work item, with no member
  daemon, paid by the hub operator as a SaaS offering. It uses the
  operator's own inference access, so it does not conflict with the rule
  that the hub never holds members' agent credentials.
