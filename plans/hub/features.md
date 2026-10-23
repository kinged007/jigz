# Hub: features

Status: draft for discussion (2026-10-21)

The number in brackets is the phase in `plans/hub/vision-and-roadmap.md`
section 5. "later" items stay in `NOT-NOW.md` until a phase is assigned.

## 1. Organizations, members, access

- Organizations (multi-tenant data model) [0]
- GitHub App sign-in limited to an organization's members [0]
- Roles admin and member; all members see all projects [0]
- OAuth or OIDC sign-in for GitLab and Gitea [10]
- Public read-only view: roadmap, progress, aggregate metrics; organization setting, on or off [3]
- Audit log of all actions [1]
- Audit log export and retention settings [7]
- SSO through OIDC or SAML [10]

## 2. Projects and providers

- Add a project from GitHub; project brief (vision, constraints,
  priorities, protected areas) [1]
- Provider sync: issues, PRs, labels, assignees, milestones, comments, CI
  status; webhooks and periodic re-sync [1]
- Label setup in each project, same definitions as `jig setup` [1]
- Environments (dev, staging, prod) and URLs read from GitHub Environments
  and deployments through provider sync; shown on the milestone [3]
- No branch policy in the hub; agent PRs target the default branch [3]
- Separate issue tracker and code host per project [10]
- GitLab, Gitea, Jira [10]

## 3. Daemons

- Enrollment tokens; daemon credential rotation and revocation [1]
- Config served to daemons: projects, workflows, prompts, labels [1]
- Fleet view of every daemon of every member, including a solo member with
  several machines: status, loops, capacity, working hours, account
  availability, version, heartbeat [1]
- Routing by runner label; the hub is the only writer [1]
- Control commands per daemon or loop [1]
- Cost-aware routing to the member whose working window is open [7]
- Agent hosting: one assigned runner per agent, no fallback; runner offline state in the dashboard [4]

## 4. Agents and roles

- Agent entity: name, role description, instructions, runtime, MCP tools
  and permissions, projects, autonomy level, assigned runner [4]
- Product manager and project manager presets; other roles can be created
  [4]
- Product manager role: issue triage, external PR ranking, milestones,
  roadmap [7]
- Project manager role: sprints, work order, dependencies, assignment by
  capacity, quality ownership [7]
- Deterministic gates owned by a role, whoever fills it [7]
- Release readiness gate, freeze window, release proposals with changelog
  [7]
- Roles filled by an agent, a human, or both [2]

## 5. Agent sessions, tasks, and governed actions

- Chat with an agent; one session per (agent, project) on the agent's
  assigned runner [4]
- Claude Code or `pi`; headless CLI turns with session resume [4]; ACP
  [later]
- Hub MCP server: read tools, `send_message` to an agent or role [4]
- Proposals → specs with acceptance criteria → work items [4]
- Tasks: agent, project, instructions, approval level [5]
- Triggers: schedule (cron with timezone, missed-run policy, overlap
  guard, run now), provider event, request in chat or a channel, another
  agent [5]
- `task.run` on the assigned runner; result recorded [5]
- Governed actions: approval request tool, inbox approval, proceed message,
  recorded request, approval, and result [5]
- CI failures: `workflow_run` failure on watched branches, one re-run,
  then an issue created or updated and routed; optional agent task for log
  grouping [5]
- Exploratory QA agent as a scheduled task [5]

## 6. Inbox and questions

- Question parsing from provider comments [2]
- Threads addressed to roles; humans and agents both reply [2]
- Answers written back to the issue; `needs-human` cleared [2]
- Approvals as inbox messages with actions [2]
- Daily digest [2]; email and Slack delivery [later]
- Agent message limits and loop detection [4]
- Milestone review rounds as sprints in the same milestone; change
  requests grouped into dispatches [3]
- Round and milestone reports with evidence and environment links; per-member
  notification preferences [3]
- Channel connectors, Slack first; Telegram [later]; threads addressed to
  agents; per-channel agents and allowed actions [6]
- Digests, reports, and release announcements to channels [6]
- Guests identified by channel account; organization guest config and
  permissions; guests never approve merges or releases [6]

## 7. Verification and previews

- Acceptance criteria and evidence requirements on work items [3]
- Evaluator stage separate from the builder; done gate [3]
- Evidence records: test reports, traces, screenshots, URLs [3]
- Preview list per work item; expiry; access kind [3]
- Authenticating proxy for previews; sharing with reviewers outside the
  team [3]

## 8. Governance

- Kill switch, budgets per organization, project, member [1]
- Organization budgets and team schedules across members [7]
- Circuit breaker per workflow [2]
- Autonomy matrix L0–L4; protected areas [7]
- Decision primitive: rules, LLM judge, binary model; shadow and enforce;
  recorded [7]

## 9. Outcomes and readiness

- Run metrics: runs, failures, tokens, cost [1]
- Outcome metrics per project, runtime, model, prompt version: merge rate,
  rework, review rounds, CI pass rate, time to merge, cost per accepted
  item [8]
- Long history with a retention setting; comparison across projects,
  models, and prompt versions [8]
- Readiness: static pillars, live checks, outcome correlation, gaps as
  work items, report and badge [8]

## 10. Context and self-improvement

- Prompt and workflow versioning; versions recorded per run [1]
- Project memory, run summaries, agent lessons through MCP [9]
- Retrospective agent [9]
- Harness improvement loop with replay [10]

## 11. Extensions

- Internal module API [7]
- Published extension API [10]

## 12. Backlog

Not scheduled.

- Hosted runners: the hub starts a sandbox per work item, with no member
  daemon, paid by the hub operator as a SaaS offering; it uses the
  operator's own inference access.
