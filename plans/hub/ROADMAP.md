# Hub roadmap

The hub (working name Shiftd) is the team product: organizations, members,
projects, agents, inbox, verification, outcomes, readiness. It lives in a
separate repository. Detailed plans: `vision-and-roadmap.md` and
`features.md` in this directory, with the system plans in `../system/`.
The Jigz roadmap is `../../ROADMAP.md`.

Status: nothing below is built. The Jigz side of the protocol (hub protocol
v0 schemas, daemon, fake hub) is shipped and tracked in the Jigz roadmap.
Each phase ends with a usable result, a green acceptance suite, and a
public write-up (`vision-and-roadmap.md` section 5).

## Phases

- [ ] Phase 0: repository and harness — repo, license, README, delivery
  harness, GitHub App sign-in with org check, protocol schemas and a fake
  daemon
- [ ] Phase 1: walking skeleton — add project, provider sync, enroll
  daemon, assign → runner label → run → events, fleet view, one end-to-end
  path
- [ ] Phase 2: questions, inbox, approvals — question parsing, threads by
  role, answers written back, `resume` trigger, approvals, daily digest
- [ ] Phase 3: verification and previews — acceptance criteria and
  evidence, evaluator stage, done gate, preview list, authenticating
  proxy, environments, milestone review rounds
- [ ] Phase 4: agents and sessions — agent entity and role presets,
  assigned runner, chat, hub MCP server with `send_message`, proposals →
  specs → work items (needs the agent session host in `../../ROADMAP.md`,
  item 3.6)
- [ ] Phase 5: tasks, triggers, governed actions — scheduled and event
  tasks, `task.run`, approval-gated actions, CI failure handling (needs
  the agent session host in `../../ROADMAP.md`, item 3.6)
- [ ] Phase 6: gateway and guests — channel connectors (Slack first),
  threads to agents, digests and announcements, guests
- [ ] Phase 7: planning and governance — triage, milestones, sprints,
  assignment by capacity, release readiness, autonomy matrix, decision
  primitive (shadow mode), org budgets, audit export
- [ ] Phase 8: outcomes and readiness — outcome metrics with long history
  and comparisons, readiness report (static checks, live checks, outcome
  correlation)
- [ ] Phase 9: context and self-improvement — project memory and lessons
  via MCP, prompt versioning per run, retrospective agent
- [ ] Phase 10: more providers and domains — GitLab, Gitea, Jira, SSO,
  harness improvement loop with replay, extension API
- [ ] Backlog: hosted runners (hub starts a sandbox per work item, no
  member daemon) — not scheduled

## Delivery method

`../system/delivery-method.md` asks for: evidence in every PR from a
verifier separate from the builder, one preview environment per PR, a
deterministic fake agent (partly exists in Jigz's acceptance suite), an
exploratory QA agent, a dogfood gate (the hub runs its own development
from the first usable version), and per-module `AGENTS.md` with
lint-enforced module boundaries.

## Open decisions

- Server stack, license, guest sign-in (`vision-and-roadmap.md` section 6).
