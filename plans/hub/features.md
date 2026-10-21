# Hub: features

Status: draft for discussion (2026-10-21)

The number in brackets is the phase in `plans/hub/vision-and-roadmap.md`
section 5. "later" items stay in `NOT-NOW.md` until a phase is assigned.

## 1. Organizations, members, access

- Organizations (multi-tenant data model) [0]
- GitHub App sign-in limited to an organization's members [0]
- Roles admin and member; all members see all projects [0]
- OAuth or OIDC sign-in for GitLab and Gitea [8]
- Public read-only view: roadmap, progress, aggregate metrics [3]
- Audit log of all actions [1]
- Audit log export and retention settings [5]
- SSO through OIDC or SAML [8]

## 2. Projects and providers

- Add a project from GitHub; project brief (vision, constraints,
  priorities, protected areas) [1]
- Provider sync: issues, PRs, labels, assignees, milestones, comments, CI
  status; webhooks and periodic re-sync [1]
- Label setup in each project, same definitions as `sortie-loop setup` [1]
- Separate issue tracker and code host per project [8]
- GitLab, Gitea, Jira [8]

## 3. Daemons

- Enrollment tokens; daemon credential rotation and revocation [1]
- Config served to daemons: projects, workflows, prompts, labels [1]
- Fleet view of every daemon of every member, including a solo member with
  several machines: status, loops, capacity, working hours, account
  availability, version, heartbeat [1]
- Routing by runner label; the hub is the only writer [1]
- Control commands per daemon or loop [1]
- Cost-aware routing to the member whose working window is open [5]
- Manager host selection [4]

## 4. Product and project management

- Product manager role: issue triage, external PR ranking, milestones,
  roadmap [5]
- Project manager role: sprints, work order, dependencies, assignment by
  capacity, quality ownership [5]
- Release readiness gate, freeze window, release proposals with changelog
  [5]
- Schedules: cron with timezone, missed-run policy, overlap guard, run now
  [5]
- Roles filled by an agent, a human, or both [2]

## 5. Manager sessions

- Chat with the product or project manager of a project [4]
- Sessions on a manager-host daemon with Claude Code or `pi` [4]
- Headless CLI turns with session resume [4]; ACP [later]
- Hub MCP server: read tools [4], staged write tools [5]
- Proposals → specs with acceptance criteria → work items [4]

## 6. Inbox and questions

- Question parsing from provider comments [2]
- Threads addressed to roles; humans and agents both reply [2]
- Answers written back to the issue; `needs-answer` cleared [2]
- Approvals as inbox messages with actions [2]
- Daily digest [2]; email and Slack delivery [later]
- Agent message limits and loop detection [4]

## 7. Verification and previews

- Acceptance criteria and evidence requirements on work items [3]
- Evaluator stage separate from the builder; done gate [3]
- Evidence records: test reports, traces, screenshots, URLs [3]
- Preview list per work item; expiry; access kind [3]
- Authenticating proxy for previews; sharing with reviewers outside the
  team [3]
- Exploratory QA agent on a schedule [5]

## 8. Governance

- Kill switch, budgets per organization, project, member [1]
- Organization budgets and team schedules across members [5]
- Circuit breaker per workflow [2]
- Autonomy matrix L0–L4; protected areas [5]
- Decision primitive: rules, LLM judge, binary model; shadow and enforce;
  recorded [5]

## 9. Outcomes and readiness

- Run metrics: runs, failures, tokens, cost [1]
- Outcome metrics per project, runtime, model, prompt version: merge rate,
  rework, review rounds, CI pass rate, time to merge, cost per accepted
  item [6]
- Long history with a retention setting; comparison across projects,
  models, and prompt versions [6]
- Readiness: static pillars, live checks, outcome correlation, gaps as
  work items, report and badge [6]

## 10. Context and self-improvement

- Prompt and workflow versioning; versions recorded per run [1]
- Project memory, run summaries, agent lessons through MCP [7]
- Retrospective agent [7]
- Harness improvement loop with replay [8]

## 11. Extensions

- Internal module API [5]
- Published extension API [8]

## 12. Backlog

Not scheduled.

- Hosted runners: the hub starts a sandbox per work item, with no member
  daemon, paid by the hub operator as a SaaS offering; it uses the
  operator's own inference access.
