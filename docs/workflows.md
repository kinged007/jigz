# Workflows

Each loop is one `WORKFLOW.*.md` file in `.jigz/workflows/`, named by its
stem (`issues`, `prs`, `triage`). The loop polls GitHub with the file's
query, claims a matching item, and hands it to the agent with the prompt
of the first matching dispatch rule. How items move between loops is in
[concepts.md](concepts.md#chaining-loops); the config that generates
these files is in [configuration.md](configuration.md#fleet-and-work).

## Generated vs hand-written

`work.<slug>` entries in `.jigz/config.yaml` generate `WORKFLOW.<slug>.md`
(or `WORKFLOW.<slug>-*.md` split loops) at setup, start, and reload. A
generated document carries a `# jig:generated` marker with a digest;
`jig validate` re-renders and fails on drift, i.e. a hand edit inside a
managed document. Set `managed: false` on the entry and jig neither
generates nor overwrites the document (`validate` reports it as
unmanaged). A managed entry owns `WORKFLOW.<slug>.md` and
`WORKFLOW.<slug>-*.md`: setup does not install the shipped
`WORKFLOW.<slug>.md` for it, and generation removes an owned document
it did not produce (after a split or regrouping) when it is generated or
an unedited shipped copy. An edited or hand-written one is kept with a
note, still runs as a loop, and fails `jig validate` (`loops`). Documents
of other configured slugs, such as `issues-fast` next to `issues`, are
never removed. `jig setup` never overwrites any existing workflow or prompt
file, and while a legacy `WORKFLOW.plan.md`/`build.md`/`review.md`/
`review-fix.md`/`merge.md` file exists, the consolidated
`WORKFLOW.issues.md`/`WORKFLOW.prs.md` pair is not written.

Custom loops are hand-written: drop in `WORKFLOW.triage.md` and it starts
as a loop named `triage` on the next run, backed by a `work.triage`
entry and its labels (full example below). Missing prompt files are
stubbed at generation (`# triage.md — Fill in the prompt for this rule.`)
for the owner to fill in.

## Custom loop example

A triage loop that labels new issues for the build loop. Config (loads
and splits clean under `jig validate`):

```yaml
# .jigz/config.yaml
labels:
- {name: "agent:triage", color: "fbca04", description: "Trigger: triage this issue"}
- {name: "agent:build", color: "fbca04", description: "Trigger: implement this issue"}
work:
  triage:
    query_filter: "label:agent:triage,in-progress -label:needs-human"
    agent: coder
    rules:
    - { name: triage, match: { labels: [agent:triage] }, prompt: prompts/triage.md }
```

`jig setup` (or the next start/reload) writes
`.jigz/workflows/WORKFLOW.triage.md` from that entry — frontmatter
excerpt of what generation produces:

```md
<!-- .jigz/workflows/WORKFLOW.triage.md (generated, do not hand-edit) -->
---
tracker:
  kind: github
  api_key: $JIG_TRACKER_TOKEN
  project: owner/repo
  query_filter: "label:agent:triage,in-progress -label:needs-human"
  active_states: [agent:triage, in-progress]
  in_progress_state: in-progress
  handoff_state: agent:done
  handoff_evidence: off
  terminal_states: [closed]
polling:
  interval_ms: 60000
db_path: ../data/jigz-triage.db
workspace:
  root: $JIG_WORKSPACES/triage
  retention_days: 30
agent:
  kind: claude-code
  command: /path/to/jig agent-exec coder
  max_turns: 10
dispatch:
  rules:
    - name: triage
      match:
        labels: ["agent:triage"]
      template: prompts/triage.md
  default:
    template: prompts/default.md
---
```

Prefer hand-writing the document instead? Set `managed: false` on the
entry and jig leaves the file alone (`validate` reports it as
unmanaged).

Narrow the query per loop instead with `filters:` (re-read every run,
no setup re-run): `filters: { triage: "label:agent:triage -label:needs-human" }`.
A `filters:` entry wins over the shipped default; `work.<slug>.query_filter`
wins over both; `assignee:@me` is always appended.

## Frontmatter reference

Keys jig reads (validates or overrides) vs keys only the engine reads:

| Key | Shipped value | Who reads it | Can you change it |
|---|---|---|---|
| `tracker.kind` | `github` / `github-pr` | jig (gating, loop type, generation) | via `work.<slug>.tracker.kind` |
| `tracker.query_filter` | issues / prs label lists | jig (overridden at launch by `SORTIE_TRACKER_QUERY_FILTER`) | via `work.<slug>.query_filter` or `filters:` |
| `tracker.active_states` | trigger labels + `in-progress` | engine (gate 1.27+) | derived from the rules at generation |
| `tracker.in_progress_state` | `in-progress` | config-level key of the same name | via `tracker:` / `work.<slug>.tracker` |
| `tracker.handoff_state` | `agent:done` | jig (display) + engine (backstop transition) | via `tracker:` / `work.<slug>.tracker` |
| `tracker.handoff_evidence` | `off` | engine | hand edit (unmanaged) only |
| `tracker.terminal_states` | `[closed]` | jig (parsed) | hand edit only |
| `tracker.comments.*` | `false` | engine | hand edit only |
| `polling.interval_ms` | `60000` | jig (dashboard column) + engine | via `work.<slug>.polling_interval_ms` |
| `db_path` | `jigz-issues.db` | jig (opens it for history) + engine | derived at generation (`../data/jigz-<slug>.db`) |
| `workspace.root` | `$JIG_WORKSPACES/<loop>` | jig sets the env; engine checks out there | via `JIG_WORKSPACES` env |
| `workspace.retention_days` | `30` | jig (`cleanup.max_age_days` must be under it) | hand edit only |
| `hooks.*` | clone / heal / push scripts | engine | via top-level or `work.<slug>.hooks` |
| `agent.kind`, `agent.command` | `claude-code`, `claude` | jig (capability gating, standby rewrites) | via the fleet agent the loop runs as |
| `agent.max_turns`, `agent.max_concurrent_agents` | `10`, `1` | jig (parsed, shown) | via `work.<slug>.max_turns` / `max_concurrent_agents` |
| `agent.*_timeout_ms`, `stop_grace_ms`, `max_retry_backoff_ms` | timeouts | jig (parsed) | hand edit only |
| `pi:` / `claude-code:` model blocks | `sonnet` / `claude-haiku-5-5` | jig reads the block named by `agent.kind` | via the fleet agent's `model:` |
| `effort` | — (not shipped) | engine only; rejected as a config key | per-rule engine blocks, hand edit only |
| `dispatch.rules[].name` | `plan`, `build`, … | jig (must be unique per entry) | rule names are yours |
| `dispatch.rules[].match.labels` | trigger labels | jig (first match wins; split-loop overlap check) | rule labels are yours |
| `dispatch.rules[].template` | `./prompts/*.md` | jig (missing files are stubbed) | prompt paths are yours |
| `dispatch.rules[].next` | `build-chain` (plan-build) | jig (must name a later rule of the same entry) | chaining is yours |
| `dispatch.rules[].stage` | `stage-build` (build-chain) | jig (staged rules carry no `match`) | stage chains are yours |
| `dispatch.default.template` | `prompts/default.md` | engine (safety net for query-matching items no rule matches) + jig (stubbed at generation) | generated |

Only the block named by `agent.kind` is read; the other runtime block
stays for documentation (its engine warning is filtered from `validate`
output). Generated documents point `agent.command` at the absolute jig path plus
`agent-exec <agent>` (the engine does not expand `$VAR` there) so the
supervisor can fail over between fleet agents live.

## Prompts

Each rule's `template` is a Markdown file under `.jigz/workflows/`,
usually in `prompts/`. The engine renders it with the issue context and
runs the result as the agent's instructions:

| Variable | Meaning |
|---|---|
| `{{ .issue.identifier }}` | `owner/repo#123`-style id, also in `$SORTIE_ISSUE_IDENTIFIER` |
| `{{ .issue.title }}` | Issue or PR title |
| `{{ .issue.description }}` | Issue or PR body |
| `{{ .issue.labels }}` | Label list |
| `{{ .issue.url }}` | Item URL |
| `{{ .issue.comments[].author }}`, `{{ .issue.comments[].body }}` | Comment thread |
| `$SORTIE_TRACKER_PROJECT` | `owner/repo`, for `gh --repo` calls |
| `$SORTIE_WORKSPACE` | The loop's worktree for this item |
| `$JIG_WORKSPACES` | Worktree root (set per loop by jig) |

Example rule prompt (`prompts/triage.md`):

```md
You are triaging {{ .issue.identifier }}: {{ .issue.title }}.

Read the issue and its comments with
`gh issue view {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT --comments`.
Decide: plan it (label `agent:plan`), build it now (label `agent:build`),
or park it (`needs-human` with a comment saying what is missing).

Move labels with `gh issue edit`, keep `in-progress` until you are done,
and end by removing your trigger and adding `agent:done`.
```

## Validation

`jig validate [--json]` checks, in order: the config (strict keys and
model rules), the engine profile per work entry (older than 1.27.0
fails; unknown versions warn), generation drift per loop, `loops` (an owned document that
generation does not produce fails), `prompts` (a
rule prompt of a managed entry that is missing or still the generated
stub fails), `cleanup`,
`budgets`, a token warning, `max_usd` warnings, then each installed
workflow through the engine's own `validate` with capability gating.
Both the human and `--json` output start with the repo root and the
exact `.jigz/config.yaml` path, so a failure says which checkout holds
the config to fix.

- engine too old → fail, no loop starts;
- `tracker.kind` the engine did not register (e.g. `github-pr` on an
  engine without it) → `disabled`, not an error; the loop stays offline;
- `agent.kind` the engine did not register → fail, with no fallback agent.

See [the engine](install.md#the-engine) for pinning and capability
detection.

Evidence status: Confirmed against `cmd/jig/workflows/`, `cmd/jig/generate.go`, `internal/gen`, and `internal/config` unless noted.
