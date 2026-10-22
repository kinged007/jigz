# Strategy critique

Status: draft for discussion (2026-10-20)

A review of the system plan (`plans/system/overview.md`, the hub and
`sortie-loop` plans) against the market as of October 2026, an estimate of where the build effort goes, and the
risks. Market facts come from research on 2026-10-20 and carry a source.
Statements marked "view" are judgement.

## 1. Market facts

### 1.1 Vendors ship the execution layer

| Capability | Who ships it | Source |
| ---------- | ------------ | ------ |
| Issue/PR-triggered background agents | GitHub Copilot, Claude Code, Codex, Cursor, Devin, Jules | vendor docs |
| Scheduled agent tasks | Claude Code scheduled tasks (no approval gate), Cursor Automations, Jules, GitHub Agentic Workflows | https://code.claude.com/docs/en/routines |
| Outbound-connected self-hosted runners | Claude Code (Team/Enterprise beta), Cursor (2026-09), Devin Outposts, Copilot on ARC | https://code.claude.com/docs/en/self-hosted-environments, https://cursor.com/blog/self-hosted-machines |
| Pluggable sandboxes (Vercel, E2B, Daytona, Modal, Cloudflare) | Anthropic Managed Agents, Cursor, Devin | as above |
| Multi-agent mission control | GitHub Agent HQ, Codex agents view | https://github.blog/news-insights/company-news/welcome-home-agents/ |
| Coordinator over parallel sessions | Claude Code Projects (beta) | https://code.claude.com/docs/en/claude-projects |

In every vendor product the control plane and inference stay in the
vendor's cloud and the runner serves only that vendor's agent.

### 1.2 Open-source control planes and orchestrators

| Project | What it is | Stars |
| ------- | ---------- | ----- |
| Paperclip | self-hosted, domain-neutral "agent company": companies, goals, org chart, heartbeats, budgets, tickets with atomic checkout, approvals, routines; adapters for Claude Code, Codex, Cursor, Gemini, OpenCode, Pi and others; MIT, Node + Postgres | 105k |
| OpenHands | agent platform | 91k |
| OpenAI Symphony | Linear board → Codex runs with "proof of work" before a human lands the PR | 28k |
| Harness² (google-research) | research code: improves the harness (prompts, guardrails, memory, skills) around a fixed model without ground truth | 14k |
| GitHub Agentic Workflows | markdown workflows compiled to Actions; read-only agent; "safe outputs" performed by deterministic jobs | 5.4k |
| sortie (`sortie-ai/sortie`) | Go engine: trackers GitHub, GitLab, Gitea, Jira, Linear, file; agents Claude Code, Codex, Copilot CLI, OpenCode, ACP, mock; HTTP API, SSE events, Prometheus, webhook notifier | 205 |

Paperclip's README lists memory as partial and work queues and
bring-your-own tracker as not started. It has no outcome verification;
reviews say its governance catches structural problems, not bad output.

### 1.3 Standards and components

- MCP (Linux Foundation AAIF): the standard for agent ↔ tool.
- A2A v1.0 (March 2026, AAIF since 2026-08-17): agent ↔ agent; agent
  cards, task lifecycle, auth; 150+ organisations. IBM's Agent
  Communication Protocol merged into it.
- ACP, Agent Client Protocol (Zed): editor/client ↔ agent; 44 agents
  listed; stdio today, HTTP/WebSocket transport is an active RFD. Sortie
  supports it.
- Jev (TypeSafe AI, 1.14 on 2026-10-14): hosted, proprietary decision
  model; its "Noul" interface returns the probability that a yes/no
  proposition is true; typed numbers only, no rationale; calibrated over
  many answers, not per answer; no published benchmarks.
  https://docs.typesafe.ai/concepts/system-one
- Readiness: Factory Agent Readiness (8 pillars, 5 levels, proprietary);
  several small MIT scanners (for example `napetrov/agentready`,
  `harrydaihaolin/agent-readiness`). All are static.

### 1.4 Harness engineering

- OpenAI (2026-02): about 1M lines with no hand-written code. Short
  `AGENTS.md` as a map into `docs/`; layered architecture enforced by
  custom linters whose messages tell the agent how to fix; per-worktree
  logs, metrics, traces, and browser access for agents; background agents
  that remove drift. https://openai.com/index/harness-engineering/
- Anthropic (2025-11, 2026-03): a feature list where agents may only flip a
  `passes` flag; one feature per session; an end-to-end browser run at the
  start of each session; planner, generator, and a separate evaluator that
  drives the live app. A solo run took 20 minutes and $9 and the core
  feature did not work; the full harness took 6 hours and $200 and worked.
  https://www.anthropic.com/engineering/harness-design-long-running-apps

## 2. Niche

**View:** the gap in the market matches the main problem in this project.
Everyone can run agents; almost no one can prove the result works.

Recommended position: **a self-hosted, vendor-neutral control plane where
work is done only when there is evidence, across many projects and any
agent.**

Differentiators, in order:

1. Verified delivery: acceptance criteria, separate evaluator, evidence,
   done gate. Paperclip, vendor stacks, and Symphony stop at "PR opened"
   or "human lands it".
2. Outcomes: which agent, model, prompt, and context package works best on
   each project, measured from accepted work.
3. Governance: autonomy matrix, recorded decisions with rule and model
   backends, staged outputs, budgets, audit.
4. Readiness with live checks and outcome correlation, built in.
5. Context engineering owned by the control plane.

Versus Paperclip: same domain-neutral shape and agent adapters; the hub
adds verification, outcomes, readiness, and context engineering, and uses
existing trackers (GitHub first), which Paperclip lists as not started.

Target user: solo developers and small teams with several projects, more
than one agent, and no enterprise contract (vendor self-hosting is
Team/Enterprise only).

README sentence: "Plan, delegate, and verify agent work across all your
projects, with any agent, on your own machines."

## 3. Where the effort goes

Ranked by effort to build correctly, for one developer with agents.

| Rank | Area | Why it is costly | Avoidable? | Recommendation |
| ---- | ---- | ---------------- | ---------- | -------------- |
| 1 | Verification (evidence, evaluator, previews, fake agent, E2E) | Needs a reproducible environment per change, seeded data, stable browser tests, and an evaluator that is strict but not flaky. Per project it repeats. | No. It is the product and the fix for the main failure. | Build first, on the hub itself, in phases 0–2. |
| 2 | Executor and daemon (claiming, retries, cancel, workspaces, agent adapters, remote workers, sandboxes, credentials) | Distributed lifecycle with partial failure. Sortie has 3,195 commits, 43 migrations, and 38 ADRs for this. | Yes. Sortie runs the agents; `sortie-loop` becomes the daemon; the hub routes work with runner labels. | Do not build an executor. Upstream PRs for missing engine features. |
| 3 | Context engineering and memory | Easy to build, hard to prove it helps; needs outcome evals to tell good from bad. | Partly. | After outcomes exist (phase 8); keep only what measurably helps. |
| 4 | Manager agents (product, PM) making good decisions | Open-ended prompt and policy work; quality is hard to measure. | No, but its scope can be limited. | Narrow outputs (specs, tasks, sprint plans) with human approval; measure. |
| 5 | Domain-neutral model | Cheap as naming; very costly as machinery built before a second domain exists. | Yes. | Generic names, software pack only. |
| 6 | Dashboard UI | Never finished; each feature needs screens. | No. | Server-rendered, reuse `sortie-loop` pages and patterns. |
| 7 | Extension API | A stable public API is a long-term commitment. | Yes, until there is demand. | Internal module API first. |
| 8 | Protocol integration (MCP, A2A, ACP) | Specs still change. | Partly. | MCP server early; ACP through sortie; A2A in phase 6. |

Direct answers:

- The sortie replacement is the largest avoidable cost. Building it would
  take months and add nothing users can see.
- A separate executor daemon manager is the same cost under another name.
  `sortie-loop` already supervises sortie; enrollment, reporting, and
  runner-label filters make it the daemon.
- Claiming races between machines are solved by routing, not locking:
  the assignee separates members, and the hub, as the only writer of
  runner labels, separates one member's machines.
- The largest unavoidable cost is verification. It is also the
  differentiator, so effort spent there is not wasted.

## 4. Keep sortie long term

Recommendation: yes, as the execution layer.

What it provides: tracker interface with a contract test suite, six agent
kinds including ACP and a mock agent, workspaces with hooks, retries, an
SSE event stream, a webhook notifier, Prometheus metrics, and an HTTP API
with a JSON Schema kept in sync by a contract test.

What the hub needs from it:

| Need | How | Where |
| ---- | --- | ----- |
| Pull work | existing trackers (GitHub, GitLab, Gitea, Jira, Linear) filtered by labels, assignee, runner label | exists |
| Run events to `sortie-loop` | `webhook` notifier and `/api/v1/events` | exists |
| Cancel, pause, resume | issues #1385, #1386 | upstream PR |
| Auth on its API | issue #1389 | upstream PR |
| Stable API contract | issue #1443 | upstream discussion |
| Sandboxes | hooks or a wrapper command around the agent | `sortie-loop` scripts |

Risks and mitigations:

- Bus factor 1 (about 97% of commits by one maintainer) and weekly
  releases. Mitigation: keep the fork additive (today it adds only the
  `pi` adapter and a `github-pr` tracker; 31 ahead, 128 behind upstream
  main), merge upstream regularly, use only the HTTP API. The code and
  docs are good enough to own the fork if upstream stops.
- Extensions are compile-time only. Acceptable: the fork builds its own
  binary already.
- One process per workflow file, each with its own SQLite. Acceptable at
  the target scale; revisit if a project needs more than a few workflows.

## 5. Critique of the plan

### 5.1 Is it worth it?

- Portfolio and build in public: yes. Verified delivery is a specific,
  demonstrable claim; the hub building itself with published evidence is
  content no vendor produces.
- Market leadership as a general agent company: no; Paperclip has that
  framing and 105k stars. Leadership in verified, measured delivery for
  small teams: possible, because no one occupies it yet.

### 5.2 Paths to avoid

| Path | Reason |
| ---- | ------ |
| Own executor, daemon manager, or sandbox layer | Sortie and providers cover it; vendors give it away |
| Domain-neutral machinery before a second pack | The failure mode of previous attempts |
| Agent ↔ agent chat as a feature | No demand evidence; agents coordinate through artifacts; token cost |
| Model decisions on correctness | Calibration is per population, not per answer; evidence decides |
| A dashboard as the differentiator | Sortie, Paperclip, and Agent HQ have dashboards |
| Custom protocols | MCP, A2A, ACP exist |

### 5.3 Risks

- Scope: the feature catalog is large. The delivery method (one feature in
  flight, walking skeleton, `NOT-NOW.md`) is the control. Breaking it
  repeats previous attempts.
- Verification cost per project: previews and evaluators must be cheap to
  set up for other projects, or only the hub benefits. The readiness scan
  should report what a project needs for verification.
- Proprietary components: Jev is hosted and closed. Keep it as one backend
  of the decision primitive, never required.
- Vendor terms: running subscription agent CLIs unattended may conflict
  with provider terms; default to API keys.
- Security: the control plane can start agents with write tokens. Auth
  from the first commit, scoped per-job tokens, staged outputs.
- Commercial model: a hosted, paid hub weakens the "self-hosted" claim.
  Keep it true where it matters: members' agent credentials always
  stay on members' machines, execution runs on members' machines by
  default (hosted runners in the backlog use the operator's own inference
  access), `sortie-loop` and the protocol are open source, and the hub is
  also available to self-host.
- Fork dependence: `sortie-loop` must run on upstream sortie releases;
  fork-only features (`pi` adapter, `github-pr` tracker) are optional.

## 6. View: where agentic development is going

- Execution becomes a commodity bundled with each vendor's model.
- Specialized agents multiply; systems that can use any of them through
  MCP, A2A, and ACP gain value as the number grows.
- The bottlenecks are specification and verification. Value goes to
  whatever reduces human review per accepted change without raising the
  defect rate.
- Teams use several agents; each vendor measures only its own. A neutral
  layer for policy, cost, and outcomes is needed.
- Harness engineering becomes the main engineering skill: context,
  constraints, feedback, and evaluation around fixed models. Systems that
  improve their own harness from evidence (Harness²) are the next step.
- Usage-based billing spreads; cost per accepted outcome becomes a tracked
  number.

## 7. Content plan

- Phase 0: "Why another control plane: agents say done, evidence decides"
  (this document, edited).
- Phase 1: walking skeleton demo.
- Phase 2: "The hub now builds itself", with the first evidence records.
- Phase 4: first outcome report comparing agents on the hub's own work;
  readiness scores of popular open-source repos.
- Monthly: metrics of the hub building itself (accepted work items, share
  done by agents, cost per accepted item, evaluator rejections, failure
  post-mortems).
