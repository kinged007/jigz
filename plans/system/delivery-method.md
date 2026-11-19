# Delivery method

Status: draft for discussion (2026-10-20)

How one developer and a set of agents build the hub without the project
becoming too large to manage. This method is also the first product of the
hub: the hub runs its own development with it, and later offers it to other
projects.

## 1. Why previous attempts failed

Observed in earlier attempts at this project and in agent work generally:

1. Scope grew faster than the ability to verify it. Many partly finished
   features, none proven end to end.
2. "Done" was the builder's claim. Agents report "it works, tests pass",
   and real use fails (for example, login does not work).
3. Tests checked units and mocks, not the built product in use.
4. Abstractions for future cases (any domain, any executor, plugins) were
   designed before one concrete case worked.
5. Context was lost between sessions: decisions were re-litigated, and
   agents re-discovered the codebase each time.
6. Modules were large and coupled, so no agent (or human) could hold one
   change in context.

Each rule below addresses one or more of these.

## 2. Rules

### 2.1 Evidence defines done

A change is done when there is evidence from the built product, produced by
something other than the agent that wrote the change.

- Every feature has acceptance scenarios written before the build, in
  user terms ("a new user signs up, logs in, creates a project, sees it
  listed").
- Acceptance scenarios run as browser and API tests against the built
  binary with a real database and real auth, not against the dev server and
  not with the hub's own components mocked.
- A verifier agent, separate from the builder, runs the scenarios on a
  preview environment and attaches evidence to the PR: test report,
  Playwright trace, screenshots, and the URL it tested.
- A PR without evidence is not reviewed.

### 2.2 Walking skeleton first

Build the thinnest path through every layer before deepening any layer:
login → create project → define a workflow → a trigger creates a work item
→ an agent runs → the result and its evidence appear in the hub. Every
later feature extends a working path; the skeleton's E2E test stays green.

### 2.3 One preview environment per PR

`make preview` starts the built hub with seeded data, a local tracker
(a fake GitHub API), and a deterministic fake agent. CI runs the
acceptance suite against it on every PR. The same command works on a
laptop, so humans and agents test the same thing.

Gitea is not used as the tracker because the shipped workflows use the
`github` and `github-pr` trackers with GitHub search syntax, which Gitea
lacks, and the engine has no Gitea PR tracker, so Gitea would test
different workflows.

### 2.4 Deterministic fake agent

An agent runtime that follows a script: writes given files, runs given
commands, opens a PR, fails on request, runs slowly on request. It makes
orchestration tests (claims, retries, timeouts, budgets, approvals)
reproducible and free. Real models run only in a small nightly suite.

### 2.5 Exploratory QA agent

A scheduled agent uses the deployed preview through a browser with a list
of user journeys, records what it did, and files issues with reproduction
steps and traces. It finds what scripted tests do not cover.

### 2.6 Dogfood gate

The hub manages its own repository from the first usable version. If a
change breaks the hub, the hub's own development stops, so breakage is
found within a day.

### 2.7 Concrete before general

- Build for software projects on GitHub with one agent runtime first.
- Name core types generically (work item, change, artifact, run) but add no
  configuration, plugin point, or second implementation until a second
  concrete case exists.
- Keep a "not now" list in the repo. New ideas go there, not into the
  current phase.

### 2.8 Small modules with contracts

- Few modules, each with one responsibility, its own `AGENTS.md`, and a
  size limit an agent can read in one session.
- Boundaries are schemas (JSON Schema or OpenAPI) with contract tests on
  both sides.
- Architecture rules are enforced by lint (allowed imports between
  modules), not by documentation.

### 2.9 Written memory for agents

- Decisions are ADRs; agents read them before proposing changes in the
  area.
- Each module's `AGENTS.md` lists its commands, invariants, and known
  traps.
- After each merged change the builder updates the relevant `AGENTS.md` if
  something was learned. A weekly cleanup agent removes stale entries.

### 2.10 One feature in flight

The PM agent keeps work in progress at one feature (one walking-skeleton
extension) at a time, with its tasks in parallel. The next feature starts
when the current one has evidence and is merged.

## 3. Roles

| Role | Does | Does not |
| ---- | ---- | -------- |
| Owner (human) | approves specs and releases; weekly acceptance session using the product | write routine code |
| Product agent | writes feature specs and acceptance scenarios from the roadmap and owner notes | build |
| PM agent | splits specs into tasks, orders them, keeps WIP at one feature, writes the weekly status | build or verify |
| Test agent | turns acceptance scenarios into failing tests before the build | change product code |
| Builder agents | make the failing tests pass within one module | mark their own work done |
| Verifier agent | runs scenarios on the preview, attaches evidence, rejects on failure | fix the code it verifies |
| QA agent | explores the preview, files issues | fix |
| Cleanup agent | weekly: stale docs, dead code, drifted `AGENTS.md` | add features |

Separation of duties is the main control: the agent that builds a change
never decides whether it works.

## 4. Feature lifecycle

1. Owner adds an idea (inbox message or `proposal` issue).
2. Product agent writes a spec with acceptance scenarios. Owner approves or
   edits.
3. PM agent creates tasks under the feature milestone.
4. Test agent adds failing acceptance and contract tests.
5. Builders implement, one task per PR.
6. CI: format, lint, typecheck, unit, contract, acceptance on preview.
7. Verifier attaches evidence. Review agent checks the diff.
8. Merge (human or agent, by autonomy level).
9. Dogfood: the change runs in the hub that manages this repo.
10. Weekly: owner acceptance session; QA agent findings become issues.

## 5. Harness

The tools and files that make the method executable in the repo:

- `make verify` runs everything CI runs, locally, in one command.
- `make preview` (2.3) and the fake agent (2.4).
- Logs and traces of the preview are readable by agents (files or a local
  endpoint), so a verifier can explain a failure.
- `AGENTS.md` at the root (map of modules, rules) and per module.
- `docs/adr/`, `plans/`, `NOT-NOW.md`.
- Lint rules for module boundaries.
- PR template with an evidence section that CI checks is filled.
- Lint error messages state how to fix the violation, so an agent can act
  on them without asking.
- Feature list per milestone (JSON): each acceptance criterion has a
  `passes` flag that only the evaluator may change; builders may not edit
  acceptance tests.
- Each agent session starts by running the walking-skeleton E2E test, so a
  broken baseline is found before new work starts.

## 6. Sources

- OpenAI, "Harness engineering: leveraging Codex in an agent-first world"
  (2026-02-11): https://openai.com/index/harness-engineering/
- Anthropic, "Effective harnesses for long-running agents" (2025-11-26):
  https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents
- Anthropic, "Harness design for long-running apps" (2026-03-24):
  https://www.anthropic.com/engineering/harness-design-long-running-apps
- Playwright Test Agents: https://playwright.dev/docs/test-agents
- GitHub Agentic Workflows security architecture (safe outputs):
  https://github.blog/2026/03/09/under-the-hood-security-architecture-of-github-agentic-workflows/
- Harness² (google-research): https://github.com/google-research/harness2
