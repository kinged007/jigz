# Hub: vision and roadmap

Status: draft for discussion (2026-10-21)

The hub (product name pending) is the control centre of the system
described in `plans/system/overview.md`. It plans, assigns, governs,
verifies, and measures work; the git provider holds the record, and
`sortie-loop` daemons on members' machines do the execution. These
documents move to the hub repository when it exists.

Features by area: `plans/hub/features.md`. Contract with daemons:
`plans/system/hub-protocol.md`.

## 1. Goal

A control centre for teams of humans and their agents across many
projects:

- product and project management done by agents, with humans approving
  and steering;
- work assigned to members and routed to their own machines;
- questions and approvals that reach the right person or agent;
- a change counts as done only with evidence from the product in use;
- outcomes measured per project, agent runtime, model, and prompt version.

Software projects first. The core model is domain-neutral so that other
kinds of projects can be added later.

## 2. Scope

| In the hub | Not in the hub |
| ---------- | -------------- |
| Organizations, members, sign-in | Running agents (`sortie-loop` and sortie) |
| Projects and provider sync | Agent credentials and subscriptions |
| Product and project management, manager sessions | Workspaces, sandboxes, preview hosting |
| Assignment and routing to daemons | Machine-local working hours and account pools |
| Inbox, questions, approvals | Data of record for issues and PRs (provider) |
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
| Proposal | a planned feature or change from a manager session, before approval |
| Daemon | an enrolled `sortie-loop` instance: member, runner label, status |
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
- SSO through OIDC or SAML for organizations that need it (phase 8).
- Audit log export and retention settings (phase 5).
- A member enrolls daemons from the hub (one-time token, then
  `sortie-loop join`).
- A public read-only view (roadmap, progress, aggregate metrics) for
  building in public; all actions need sign-in.

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

### 4.5 Product manager and project manager

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

Release discipline: a milestone defines the scope; work-in-progress limits
per sprint; a freeze window before a release; feature flags for unfinished
work; a release readiness gate (evidence for every item, staging verified,
changelog and migration notes, sign-off by autonomy level).

Each role can be filled by an agent, a human, or both. When no agent is
configured, its questions and approvals go to the humans subscribed to the
role.

### 4.6 Manager sessions

- A member chats with the product or project manager of a project in the
  hub while other work runs.
- The session runs on a daemon chosen as manager host, as Claude Code or
  `pi` with that member's subscription, in a checkout of the project. It
  uses its own subagents for research.
- The hub relays messages over the daemon's connection
  (`hub-protocol.md` 2.2). First version: headless CLI turns with session
  resume. Later: ACP.
- The hub's MCP server gives the session tools: read work items,
  milestones, runs, and the inbox; create proposals; answer questions.
  Writes to the provider go through the hub as staged outputs.
- An approved proposal becomes a spec with acceptance criteria and work
  items in a milestone.

### 4.7 Inbox and questions

- Threads with kinds note, question, report, escalation, approval request;
  references to work items, runs, PRs, previews.
- Questions are parsed from provider comments (`hub-protocol.md` 4) and
  open a thread addressed to the product or project role.
- The role's agent and subscribed humans can both reply. The hub writes
  the chosen answer back to the issue and clears `needs-answer`.
- Approvals (plans, merges, releases) are inbox messages with actions.
- Limits on agent messages: per thread, per hour, loop detection, token
  budget per thread.
- Daily digest of open questions and approvals.

### 4.8 Verification

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

### 4.9 Governance and decisions

- Autonomy matrix (project × action type, L0–L4), protected areas,
  budgets, circuit breakers, kill switch, audit log.
- Decision primitive with rule, LLM-judge, and binary-model (for example
  Jev) backends; shadow mode before enforcement; every decision recorded.
  Model decisions gate only reversible, low-stakes actions; evidence decides
  whether a change works.

### 4.10 Context engineering

- The hub delivers context to agents through workflow prompts (pulled by
  daemons as config) and MCP tools (project brief, relevant decisions,
  memory, previous attempts).
- Memory: project knowledge (curated in the UI), run summaries, agent
  lessons; reviewed writes.
- Prompt sections in a fixed order for provider caching; outcome
  measurement per prompt and context version.

### 4.11 Self-improvement

A retrospective agent groups failures by cause and proposes changes to
prompts, workflows, and context (as PRs at L2). Later, a harness
improvement loop replays recorded work items against a proposed change
and adopts it only when outcomes improve (idea from Harness²).

## 5. Roadmap

Each phase ends with a usable result, a green acceptance suite, and a
public write-up. The hub tests against a fake daemon generated from the
protocol schemas, so it does not wait for `sortie-loop` releases.

### Phase 0: Repository and harness

- Repository, name, license decision, README.
- Delivery harness (`plans/system/delivery-method.md` section 5).
- GitHub App sign-in with organization check; organizations and members.
- Protocol v0 schemas and the fake daemon.

### Phase 1: Walking skeleton

- Add a GitHub project; provider sync of issues, PRs, labels, assignees.
- Enroll a daemon (fake, then real `sortie-loop`).
- Assign an issue to a member → runner label → daemon runs it → run
  events → shown on the work item.
- Fleet view. One E2E test covers the path.

### Phase 2: Questions, inbox, approvals

- Question parsing, threads by role, answers written back, `needs-answer`.
- Approvals in the inbox; daily digest.

### Phase 3: Verification and previews

- Evidence records, evaluator stage, done gate.
- Preview list per work item.
- Authenticating proxy for previews; sharing with reviewers outside the
  team.
- The hub's own repository is managed by the hub at L2.

### Phase 4: Manager sessions

- Chat with product and project managers through a manager-host daemon.
- Hub MCP server; proposals → specs → work items.

### Phase 5: Planning and governance

- Issue and PR triage, milestones, sprints, assignment by capacity,
  release readiness gate, schedules.
- Autonomy matrix, decision primitive in shadow mode.
- Organization budgets and team schedules; audit log export and retention.

### Phase 6: Outcomes and readiness

- Outcome metrics per project, runtime, model, prompt version, kept as
  long history with a retention setting; comparison across projects,
  models, and prompt versions.
- Readiness: static pillars, live checks, outcome correlation.

### Phase 7: Context and self-improvement

- Context and memory through MCP, prompt versioning, retrospective agent.

### Phase 8: More providers and domains

- GitLab, Gitea, Jira; SSO; harness improvement loop; extension API; a second
  domain pack from real demand.

## 6. Open decisions

1. Product name.
2. Stack: Go, SQLite or Postgres, server-rendered UI (reuses `sortie-loop`
   experience), or another.
3. License and commercial model (`plans/system/overview.md` section 7).
4. How a manager host is chosen: per project, per organization, or by
   availability.

## 7. Backlog

Not scheduled.

- Hosted runners: the hub starts a sandbox per work item, with no member
  daemon, paid by the hub operator as a SaaS offering. It uses the
  operator's own inference access, so it does not conflict with principle
  6 (the hub never holds members' agent credentials).
