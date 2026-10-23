# Hub: vision and roadmap

Status: draft for discussion (2026-10-21)

Shiftd, the hub, is the control centre of the system
described in `plans/system/overview.md`. It plans, assigns, governs,
verifies, and measures work; the git provider holds the record, and
`jig` daemons on members' machines do the execution. These
documents move to the hub repository when it exists.

Features by area: `plans/hub/features.md`. Contract with daemons:
`plans/system/hub-protocol.md`.

## 1. Goal

A control centre for teams of humans and their agents across many
projects:

- management roles (product manager, project manager, and any other role
  the team defines) filled by agents, with humans approving and steering;
- work assigned to members and routed to their own machines;
- questions and approvals that reach the right person or agent;
- a change counts as done only with evidence from the product in use;
- outcomes measured per project, agent runtime, model, and prompt version.

Software projects first. The core model is domain-neutral so that other
kinds of projects can be added later.

## 2. Scope

| In the hub | Not in the hub |
| ---------- | -------------- |
| Organizations, members, sign-in | Running agents (`jig` and sortie) |
| Projects and provider sync | Agent credentials and subscriptions |
| Agents (product manager, project manager, other roles), their instructions, tools, and permissions | Workspaces, sandboxes, preview hosting |
| Tasks and triggers for hub-driven work | |
| Assignment and routing to daemons | Machine-local working hours and account pools |
| Inbox, questions, approvals | Data of record for issues and PRs (provider) |
| Gateway: channel connectors (Slack first) and guests | Branch policy, merge rules, promotion workflows (provider settings and project CI) |
| Verification records, outcomes, readiness | |
| Fleet view of daemons, all machines of all members | |
| Authenticating proxy for previews, shared with reviewers outside the team | |
| Long-history outcome analytics, organization budgets, team schedules | |

The hub has no dependency on sortie.

## 3. Principles

1. Evidence over claims.
2. Concrete before general: GitHub and one agent runtime first; generic
   names, no generic machinery until a second case exists.
3. The provider is the record; the hub keeps a synced copy and adds what
   the provider cannot hold (plans, inbox, evidence, outcomes).
4. Deterministic code decides; models advise through recorded decisions.
5. Every action by a human, agent, or daemon is recorded.
6. The hub never holds members' agent credentials.

## 4. Architecture

### 4.1 Core model

| Entity | Meaning |
| ------ | ------- |
| Organization | tenant: members, projects, daemons, budgets |
| Member | a person, identified by their provider account; role admin or member |
| Project | a repository (or tracker project) with a brief and autonomy settings |
| Milestone | release scope (product level) |
| Sprint | an ordered set of work inside a milestone (project level) |
| Work item | an issue or PR, synced from the provider, with acceptance criteria and evidence requirements |
| Agent | a named role with instructions, tools, and one assigned runner (4.5) |
| Task | hub-driven work with no issue: agent, project, instructions, approval level, triggers (4.7) |
| Proposal | a planned feature or change from an agent session, before approval |
| Environment | a deployment target (dev, staging, prod) read from the provider (4.10) |
| Channel | a connector to an external platform, with the agents and actions it allows (4.12) |
| Guest | a person without a provider account, identified by a channel account (4.12) |
| Daemon | an enrolled `jig` instance: member, runner label, status |
| Run | one agent execution reported by a daemon |
| Evidence | artifacts that prove acceptance criteria |
| Question | a parsed `agent-question` block and its answers |
| Message | an inbox message in a thread |
| Decision | a recorded gate result: rule or model, input, output |
| Preview | a URL registered by a daemon for a work item, with expiry |

Every row belongs to an organization from the first version.

### 4.2 Providers

- Capabilities, each provided separately: sign-in, issue tracker, code
  host, CI status. A project may use different providers for issues and
  code (for example Jira and Bitbucket).
- Sync: provider webhooks plus a periodic re-sync; the hub's copy is a
  cache, the provider wins on conflict.
- GitHub first (GitHub App for sign-in, sync, and the hub's own writes).
  GitLab, Gitea, and Jira after the GitHub path is complete.

### 4.3 Team and access

- Sign-in through the GitHub App, limited to members of the configured
  GitHub organization. Other providers through OAuth or OIDC later.
- Roles: admin and member. Every member sees every project.
- SSO through OIDC or SAML for organizations that need it (phase 10).
- Audit log export and retention settings (phase 7).
- A member enrolls daemons from the hub (one-time token, then
  `jig join`).
- A public read-only view (roadmap, progress, aggregate metrics) for
  building in public; all actions need sign-in. It is an organization
  setting, on or off (4.12).

### 4.4 Daemons and routing

- Fleet view: every daemon of every member, including a solo member with
  several machines: member, status, loops, capacity, working hours, agent
  account availability, version, last heartbeat. The view shows account
  availability only; credentials stay on the machine.
- Routing (`overview.md` 4.2): the hub picks one of the assignee's daemons
  by availability and adds its runner label. Only the hub writes runner
  labels. Items wait when no daemon of the assignee is available.
- Cost-aware routing: when an item has no fixed assignee, the hub prefers
  the member whose working window is open and whose agent accounts have
  capacity, by cost class.
- Control: start, stop, drain, pause, resume, reload, per daemon or loop.

### 4.5 Agents

An agent is an entity of the core model:

| Field | Meaning |
| ----- | ------- |
| Name | for example `product-manager` |
| Role description | what the agent is responsible for |
| Instructions | the agent's standing prompt |
| Runtime | Claude Code or `pi` |
| MCP tools and permissions | which hub MCP tools it may call |
| Projects | the projects it serves |
| Autonomy level | L0–L4 (4.15) |
| Assigned runner | the one daemon that hosts it (4.6) |

Product manager and project manager are presets. Any other role can be
created, for example a marketing manager or a scrum master.

| | Product manager | Project manager |
| --- | --- | --- |
| Question | what and why | how, when, by whom |
| Triage | issues and external PRs, ranked by fit with the project brief and the importance of the affected feature | — |
| Planning | milestones (release scope), roadmap | sprints, work order, dependencies |
| Assignment | — | to members and daemons, by capacity |
| Quality | — | owns it: checks evidence, rejects, re-queues, release readiness |
| Questions answered | `audience="product"` | `audience="project"` |

PR triage uses fixed signals first (CI status, size, conflicts, linked
issue, area, contributor history), then a model judgement of product fit;
a human confirms the ranked list.

Deterministic gates (quality, done gate, release readiness) are code. A
role owns each gate, whoever fills the role.

Release discipline: a milestone defines the scope; work-in-progress limits
per sprint; a freeze window before a release; feature flags for unfinished
work; a release readiness gate (evidence for every item, staging verified,
changelog and migration notes, sign-off by autonomy level).

Each role can be filled by an agent, a human, or both. When no agent is
configured, its questions and approvals go to the humans subscribed to the
role.

Agents communicate through inbox threads. The hub MCP tool `send_message`
sends a message to an agent or a role. The limits in 4.13 apply.

### 4.6 Agent sessions and hosting

- A member chats with an agent in the hub while other work runs. One
  session per (agent, project).
- Each agent has exactly one assigned runner. The session runs on that
  runner, as Claude Code or `pi` with the runner member's subscription,
  in a checkout of the project. It uses its own subagents for research.
- There is no fallback to other runners: management agents usually hold
  wider GitHub permissions through that member's credentials. When the
  runner is offline, the agent's work waits and the hub dashboard shows
  the runner as offline.
- The plan assumes management agents are assigned to an always-on runner,
  a server running `jig` in connected mode. A member's
  subscription then serves requests from other people. Provider terms for
  that are the organization's responsibility.
- The hub relays messages over the daemon's connection
  (`hub-protocol.md` 2.2: `session.*`). First version: headless CLI turns
  with session resume. Later: ACP.
- The hub's MCP server gives the session tools: read work items,
  milestones, runs, and the inbox; create proposals; answer questions;
  `send_message`; request approval (4.8).
- The session does coordination and operations. Code changes go through an
  issue and a PR. The session never pushes to protected branches.
- Writes to the provider by the hub's GitHub App are limited to hub
  bookkeeping: runner labels, answers to questions, issues the hub creates
  (4.9), and sync. Other provider actions are performed by the agent with
  the runner member's credentials after approval (4.8).
- An approved proposal becomes a spec with acceptance criteria and work
  items in a milestone.

### 4.7 Tasks and triggers

- A task is hub-driven work with no issue: agent, project, instructions,
  approval level.
- Triggers: a schedule (cron with timezone, missed-run policy, overlap
  guard, run now), a provider event, a request in chat or a channel, or
  another agent.
- A task runs as `task.run` on the agent's assigned runner
  (`hub-protocol.md` 2.2). The result is recorded and posted to the inbox.
- Examples: a daily standup by a scrum master, a morning CI review, a
  release.

### 4.8 Governed actions

Actions such as a release or the promotion of `dev` to `main`:

1. The agent requests approval through a hub MCP tool.
2. The request is an inbox approval, gated by the autonomy level.
3. On approval, the hub tells the agent's session on its assigned runner
   to proceed.
4. The agent performs the action with the runner member's credentials, for
   example `gh release create` or `gh workflow run`.
5. The hub records the request, the approval, and the result.

### 4.9 CI failures

- The GitHub App receives `workflow_run` completed events with conclusion
  failure on watched branches, configured per project (for example `dev`).
- Deterministic code re-runs the failed jobs once. On a second failure it
  creates an issue, or updates the open issue for the same workflow and
  job, labels it for the normal pipeline, and routes it.
- An optional agent task reads logs and groups failures. A scheduled CI
  review task can supplement it.
- PR CI failures stay with the review-fix workflow.

### 4.10 Branches and environments

- The hub has no branch policy. Branch protection, merge rules, and
  promotion workflows are GitHub settings and the project's CI.
- Agent PRs target the repository's default branch. A team that integrates
  into `dev` sets `dev` as the default branch.
- Environments (dev, staging, prod) and their URLs are read from GitHub
  Environments and deployments through provider sync. The hub does not
  define them. They are shown on the milestone for verification.

### 4.11 Milestone review rounds

- Milestone states: in development, in review, changes requested, accepted,
  released. "In review" and "changes requested" alternate until the
  milestone is accepted.
- In review: the product agent verifies on staging, then humans review.
- A review round is a sprint inside the same milestone. It does not create
  a new milestone.
- A member submits change requests in the inbox or a channel. The project
  manager groups them into dispatches: a parent issue with sub-issues, one
  PR per dispatch, run through the full pipeline.
- At the end of each round and of the milestone, the hub sends a report:
  work done, evidence, environment and preview links.
- Members set notification preferences, for example a report at the end of
  each round and of the milestone.

### 4.12 Gateway and guests

- Channel connectors carry inbox threads to and from external platforms.
  Slack first, Telegram later.
- An inbound message opens or continues a thread addressed to an agent.
  The agent's reply is posted back. Digests, reports, and release
  announcements go out to channels.
- Each channel config lists the agents it can reach and the actions it
  allows.
- Guests are people without a provider account, for example product or
  marketing staff on Slack. A guest is identified by their channel account.
- Organization config: guests on or off; guest permissions (submit
  requests, discuss with listed agents, receive updates, read the
  dashboard); public read-only view on or off.
- Guests never approve merges or releases.

### 4.13 Inbox and questions

- Threads with kinds note, question, report, escalation, approval request;
  references to work items, runs, PRs, previews.
- Questions are parsed from provider comments (`hub-protocol.md` 4) and
  open a thread addressed to the product or project role.
- The role's agent and subscribed humans can both reply. The hub writes
  the chosen answer back to the issue and clears `needs-human` when every
  blocking question on the item has an answer.
- Approvals (plans, merges, releases, governed actions) are inbox messages
  with actions.
- Limits on agent messages, including `send_message` between agents: per
  thread, per hour, loop detection, token budget per thread.
- Daily digest of open questions and approvals.

### 4.14 Verification

- Work items carry acceptance criteria and the evidence each needs.
- An evaluator stage, run by a different agent from the builder, produces
  the evidence: preview environment, seeded user, browser and API
  scenarios, trace, screenshots.
- Only the evaluator marks a criterion as passed; the done gate requires
  passing evidence for every criterion.
- Previews registered by daemons appear on the work item and in the inbox.
  Access: the hub is an authenticating proxy for previews, so reviewers
  outside the team see a preview after sign-in. Long random expiring URLs
  and provider access control remain the access kinds a daemon can register.

### 4.15 Governance and decisions

- Autonomy matrix (project × action type, L0–L4), protected areas,
  budgets, circuit breakers, kill switch, audit log.
- Decision primitive with rule, LLM-judge, and binary-model (for example
  Jev) backends; shadow mode before enforcement; every decision recorded.
  Model decisions gate only reversible, low-stakes actions; evidence decides
  whether a change works.

### 4.16 Context engineering

- The hub delivers context to agents through workflow prompts (pulled by
  daemons as config) and MCP tools (project brief, relevant decisions,
  memory, previous attempts).
- Memory: project knowledge (curated in the UI), run summaries, agent
  lessons; reviewed writes.
- Prompt sections in a fixed order for provider caching; outcome
  measurement per prompt and context version.

### 4.17 Self-improvement

A retrospective agent groups failures by cause and proposes changes to
prompts, workflows, and context (as PRs at L2). Later, a harness
improvement loop replays recorded work items against a proposed change
and adopts it only when outcomes improve (idea from Harness²).

## 5. Roadmap

Each phase ends with a usable result, a green acceptance suite, and a
public write-up. The hub tests against a fake daemon generated from the
protocol schemas, so it does not wait for `jig` releases.

### Phase 0: Repository and harness

- Repository, license decision, README.
- Delivery harness (`plans/system/delivery-method.md` section 5).
- GitHub App sign-in with organization check; organizations and members.
- Protocol v0 schemas and the fake daemon.

### Phase 1: Walking skeleton

- Add a GitHub project; provider sync of issues, PRs, labels, assignees.
- Enroll a daemon (fake, then real `jig`).
- Assign an issue to a member → runner label → daemon runs it → run
  events → shown on the work item.
- Fleet view. One E2E test covers the path.

### Phase 2: Questions, inbox, approvals

- Question parsing, threads by role, answers written back, `needs-human`
  cleared.
- Approvals in the inbox; daily digest.

### Phase 3: Verification and previews

- Evidence records, evaluator stage, done gate.
- Preview list per work item.
- Authenticating proxy for previews; sharing with reviewers outside the
  team.
- Environments from GitHub deployments, shown on the milestone.
- Milestone review rounds and reports (4.11).
- The hub's own repository is managed by the hub at L2.

### Phase 4: Agents and sessions

- Agent entity with the product manager and project manager presets;
  assigned runner; runner offline state in the dashboard.
- Chat with agents on their assigned runners.
- Hub MCP server including `send_message`; proposals → specs → work items.

### Phase 5: Tasks, triggers, and governed actions

- Tasks with schedule, provider event, request, and agent triggers;
  `task.run` on the assigned runner.
- Governed actions: approval request, inbox approval, proceed message,
  recorded result (4.8).
- CI failure handling: re-run once, then issue (4.9).

### Phase 6: Gateway and guests

- Channel connectors, Slack first; threads to and from agents; digests,
  reports, and announcements to channels.
- Guests and organization guest config.

### Phase 7: Planning and governance

- Issue and PR triage, milestones, sprints, assignment by capacity,
  release readiness gate, schedules.
- Autonomy matrix, decision primitive in shadow mode.
- Organization budgets and team schedules; audit log export and retention.

### Phase 8: Outcomes and readiness

- Outcome metrics per project, runtime, model, prompt version, kept as
  long history with a retention setting; comparison across projects,
  models, and prompt versions.
- Readiness: static pillars, live checks, outcome correlation.

### Phase 9: Context and self-improvement

- Context and memory through MCP, prompt versioning, retrospective agent.

### Phase 10: More providers and domains

- GitLab, Gitea, Jira; SSO; harness improvement loop; extension API; a second
  domain pack from real demand.

## 6. Open decisions

1. Stack: Go, SQLite or Postgres, server-rendered UI (reuses `jig`
   experience), or another.
2. License and commercial model (`plans/system/overview.md` section 7).
3. How a guest signs in to the dashboard, for example Sign in with Slack.

## 7. Backlog

Not scheduled.

- Hosted runners: the hub starts a sandbox per work item, with no member
  daemon, paid by the hub operator as a SaaS offering. It uses the
  operator's own inference access, so it does not conflict with principle
  6 (the hub never holds members' agent credentials).
