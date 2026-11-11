# Configuration

```
.jigz/
  config.yaml      # repo:, token:, work:, agents:, cleanup:, sleep: (all optional — see below)
  workflows/       # WORKFLOW.<slug>.md for each work: entry (generated, or shipped when one exists); edit freely
  .env.loop        # generated each run (resolved tracker, token, clone URL; mode 0600)
  workspaces/      # per-item agent checkouts (one subdir per loop)
  logs/            # engine child output, one <workflow>.log per loop
```

`setup` ignores `.jigz/` wholesale in `.gitignore` — run state
(env, workspaces, db files, local config) stays untracked. To share
loop config in git instead (keep `token: ""` and use env for secrets),
replace `.jigz/` with:

```gitignore
# jig: track shared config, ignore run state
.jigz/*
!.jigz/config.yaml
!.jigz/workflows/
jigz-*.db
```

`.jigz/config.yaml`. An unknown key at any level is an error that names
the field path of the key (for example
`unknown key "work.issues.rules[0].effort"`):

```yaml
repo: ""        # empty = detect from git remote; or pin owner/name
token: ""       # empty = JIG_TOKEN, then GITHUB_TOKEN / GH_TOKEN env
# per-loop query-filter overrides, keyed by WORKFLOW.*.md stem
# (read on every run — no setup re-run needed):
#filters:
#  triage: "label:agent:triage -label:needs-human"
# GitHub labels setup ensures exist (create-or-edit; others left alone).
# A custom loop adds its labels here, then setup is re-run to create them.
labels:
- {name: "agent:plan", color: "fbca04", description: "Trigger: write an implementation plan for this issue"}
- {name: "agent:plan-build", color: "fbca04", description: "Trigger: plan this issue, then implement it with no human step"}
- {name: "agent:docs", color: "fbca04", description: "Trigger: update the docs for merged changes and open a PR"}
# ... (full default list seeded by setup)
```

`setup` is additive: it creates the labels listed here and updates their
colour and description, and leaves every other label in the repo alone —
including labels from an older scheme. Nothing is deleted for you.
Labels from the previous scheme (`agent:plan-needed`, `agent:quick`,
`agent:pr-fix`, `agent:needs-review`, `agent:reviewed`,
`agent:review-complete`, `agent:merged`, `backlog`, `review`, `done`) can
be removed from a repo with:

```sh
for l in agent:plan-needed agent:quick agent:pr-fix agent:needs-review \
         agent:reviewed agent:review-complete agent:merged backlog review done; do
  gh label delete "$l" --repo owner/name --yes
done
```

### Complete example

Everything in one file (all keys optional). This exact file loads and
resolves with `jig validate`. `jig setup` scaffolds the same shape:
`operating_hours`, the second agent, the pool, and the budget as
commented examples, and no `engine:` block — the engine default moves
with the binary, so only pin one in `engine:` to hold a version.

```yaml
# .jigz/config.yaml — unknown keys are an error, e.g. unknown key "work.issues.rules[0].effort".
repo: "owner/name"        # empty = detect from git remote at runtime
token: ""                # empty = JIG_TOKEN, then GITHUB_TOKEN / GH_TOKEN
filters:                 # per-loop query overrides, keyed by WORKFLOW.*.md stem
  triage: "label:agent:triage -label:needs-human"
labels:                   # setup creates/edits these; a custom loop adds its own here
- {name: "agent:plan", color: "fbca04", description: "Trigger: write an implementation plan for this issue"}
- {name: "agent:build", color: "fbca04", description: "Trigger: implement this issue, or apply review feedback on this PR"}
- {name: "agent:triage", color: "fbca04", description: "Trigger: triage this issue"}
operating_hours:          # gates every loop; omitted or empty = 24/7
  timezone: Europe/Madrid  # IANA name; empty = local time
  end: finish             # finish | stop; default finish
  windows:
    - days: [mon, tue, wed, thu, fri]
      start: "09:00"
      end: "18:00"        # an end earlier than start wraps past midnight
tracker:                  # defaults for every work entry; work.<slug>.tracker overrides
  kind: github
  api_key: { from_env: JIG_TRACKER_TOKEN }  # credential: reference only, never a literal
  project: owner/repo
  in_progress_state: in-progress
  handoff_state: agent:done
hooks:                    # defaults; work.<slug>.hooks overrides; timeout_ms >= 0; variables: see Hook environment
  timeout_ms: 60000
agents:                   # runtime is required: any agent kind the engine registers; model passes through unvalidated
  coder:
    runtime: claude-code
    model: claude-haiku-5-5
  night-owl:
    runtime: claude-code
    model: claude-haiku-5-5
    schedule:             # omitted = always available
      timezone: Europe/Madrid
      end: finish
      windows:
        - days: [mon, tue, wed, thu, fri]
          start: "18:00"
          end: "09:00"
    env:
      ANTHROPIC_OAUTH_TOKEN: { from_env: CLAUDE_SUB_1 }
pools:
  build-pool: [coder, night-owl]  # ordered fallback; agent and pool names share one space
work:
  issues:
    query_filter: "label:agent:plan,agent:plan-build,agent:build,agent:docs,in-progress -label:needs-human"
    agent: build-pool    # an agent, a pool, or an ordered list of names
    max_concurrent_agents: 1
    max_turns: 10
    polling_interval_ms: 60000
    budget:
      period: monthly    # weekly (ISO week, Monday start) | monthly (calendar month)
      timezone: UTC      # IANA name, default UTC; period boundaries use it
      max_tokens: 5000000  # 0 or absent turns the budget off
      warn_percent: 80   # 1-100, default 80
      on_exceed: pause   # only pause is accepted
      poll_interval: 1m  # default 1m
    rules:
    - { name: plan, match: { labels: [agent:plan] }, prompt: prompts/plan.md }
    - { name: plan-build, match: { labels: [agent:plan-build] }, prompt: prompts/plan.md, next: build-chain }
    - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
    - { name: docs, match: { labels: [agent:docs] }, prompt: prompts/docs.md }
    - { name: build-chain, stage: stage-build, prompt: prompts/build.md }
  prs:
    tracker: { kind: github-pr }  # overlay: kind, api_key, project, states, handoff_state
    query_filter: "label:agent:review,agent:build,agent:merge,in-progress -label:needs-human"
    agent: build-pool
    rules:
    - { name: review, match: { labels: [agent:review] }, prompt: prompts/review.md }
    - { name: review-fix, match: { labels: [agent:build] }, prompt: prompts/review-fix.md }
    - { name: merge, match: { labels: [agent:merge] }, prompt: prompts/merge.md }
cleanup:                  # workspace cleanup command; disabled unless enabled: true
  enabled: false
  interval: 6h           # required when enabled
  command: .jigz/scripts/cleanup.sh  # required when enabled; installed by setup, gets JIG_CLEANUP_* env
  max_age_days: 14       # required when enabled; must be < workspace.retention_days
  dry_run: false
  timeout: 10m           # default 10m
sleep:                    # stop engine children when idle; disabled unless enabled: true
  enabled: false
  idle_timeout: 15m      # required when enabled
  wake_interval: 1h      # required when enabled
github:                   # dashboard GitHub access; re-read on every pass, no restart needed
  enrich: false          # true = enrich run history with pull requests
  questions: true        # false = turn off the question scan and Questions page
  interval: 10m          # enrichment pass interval, at least 1m
  api_url: https://api.github.com  # set for GitHub Enterprise Server
  details: false         # also read commits, files, check runs (needs enrich)
  questions_interval: 2m  # question scan interval, at least 1m
  questions_window: 168h  # how far back a scan looks for stored runs
```

### Fleet and work

The same file holds the engine target, the operating hours, the tracker
and hook defaults, the agent fleet and the work entries
(`plans/jigz/engine-interface.md` has the design). Secrets are never
stored in the file: a credential is a reference to an environment
variable of the supervising process, `{from_env: NAME}`. A key whose
name ends in `TOKEN`, `KEY`, `SECRET` or `PASSWORD` (in any case) must
be a reference; a literal is rejected. An unresolved reference does not
fail the load; it makes the agent unavailable, and the unresolved names
are retrievable (`Config.UnresolvedRefs`). `max_concurrent_agents`
counts the running sessions of one work entry; `0` (or omitted) means
no limit.

```yaml
operating_hours: {}          # omitted or empty = 24/7; windows use days mon..sun and "HH:MM"
tracker:                     # defaults for every work entry; work.<slug>.tracker overrides
  kind: github
  api_key: { from_env: JIG_TRACKER_TOKEN }  # credential: reference only
  project: owner/repo                       # a reference or a plain string
hooks:                       # defaults; work.<slug>.hooks overrides; variables: see "Hook environment"
  after_create: ./hooks/create.sh
  before_run: ./hooks/before.sh
  after_run: ./hooks/after.sh
  timeout_ms: 60000
agents:
  builder:
    runtime: claude-code     # any agent kind the engine registers
    # command: claude        # split on whitespace, no shell; required only for agent-client-protocol and mock; see Agent runtimes
    model: claude-sonnet-5-5 # passed to the runtime unvalidated
    schedule:                # omitted = always available
      timezone: Europe/Madrid
      end: finish            # finish | stop
      windows: [{ days: [mon, tue, wed, thu, fri], start: "09:00", end: "18:00" }]
    env:                     # environment of the agent process
      ANTHROPIC_OAUTH_TOKEN: { from_env: CLAUDE_SUB_1 }
pools:
  build-pool: [builder]      # ordered fallback; agent and pool names share one space
work:
  issues:
    query_filter: "label:agent:build,in-progress -label:needs-human"  # the complete query
    budget: { period: monthly, max_tokens: 5000000 }
    agent: builder           # an agent, a pool, or an ordered list of names
    rules:
    - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
```

A work entry's `query_filter` is the complete tracker query and wins over
the `filters:` override and the shipped defaults; `assignee:@me` is
appended at launch. `max_concurrent_agents`, `max_turns`, `max_sessions` and
`polling_interval_ms` sit next to `budget`. A rule's `agent` may be a
name, a list of names, or `{use: ..., model: ..., schedule: ...}` to
override the fleet agent for that rule; `stage` and `next` are the
engine's stage chaining. Work names are letters, digits and `-`; rule
names within one entry are unique, and `next` names a later rule of the
same entry. Only `work:` entries are installed and run: no loop exists for a
slug that `work:` does not name. The
supervisor reads `work.<slug>.query_filter` and `work.<slug>.budget` at
launch and validates and resolves the remaining sections.

#### work.\<slug\> keys

| Key | Type | Default | Notes |
|---|---|---|---|
| `managed` | bool | `true` | `false` = the document is neither generated nor overwritten; `validate` reports it as unmanaged |
| `query_filter` | string | shipped default for the loop | The complete tracker query; wins over `filters:` and the shipped default; `assignee:@me` is appended at launch |
| `tracker` | mapping | top-level `tracker` | Overlay: `kind`, `api_key`, `project`, `active_states`, `in_progress_state`, `handoff_state` |
| `hooks` | mapping | top-level `hooks` | Overlay: `after_create`, `before_run`, `after_run`, `timeout_ms`. Scripts can read the `JIG_*` variables in [Hook environment](#hook-environment) |
| `max_concurrent_agents` | int | `1` in the generated document | `0`/absent renders `1` |
| `max_turns` | int | `10` in the generated document | `0`/absent renders `10` |
| `max_sessions` | int | none | Rendered as `agent.max_sessions` only when above `0`; `0`/absent renders nothing (the engine default, unlimited). See [Session cap](#session-cap) |
| `polling_interval_ms` | int | `60000` in the generated document | `0`/absent renders `60000` |
| `budget` | mapping | none | See [Spend controls](#spend-controls); the slug needs its `WORKFLOW.<slug>.md` |
| `partials` | list of strings | none | Rendered as `dispatch.partials` in every loop document of the entry. See [Partials](#partials) |
| `agent` | name, list, or mapping | none | An agent, a pool, or an ordered list; per-rule overrides below |
| `rules` | list | none | See below; `next` must name a later rule of the same entry |

Slugs match `^[A-Za-z0-9][A-Za-z0-9_-]*$`. `work.<slug>.budget` with no
`WORKFLOW.<slug>.md` (or `WORKFLOW.<slug>-*.md` split loop) is a startup
error, and the removed `workflows:` key is rejected with a pointer to
`work.<slug>.budget`.

#### Session cap

`max_sessions` is the engine's `agent.max_sessions`: the maximum number of
completed agent sessions per issue. The engine counts them from the run
history in the loop's own database (`jigz-<name>.db`), so each loop of a
split entry counts separately, and a session that ran in another loop
does not count. The running session is not counted. When an issue has
used the cap, the engine stops dispatching it, posts one comment on the
issue that names the cap and `agent.max_sessions` as the setting that
raises it, and holds the issue until the cap is raised above the count or
removed, or the issue leaves the active states.
The generated document also lists a `tracker_comment` entry in
`notifications` with the events `auto_merge.merged` and `budget.held`. The
engine's implicit budget-hold comment is deprecated and draws a validate
warning; the entry keeps that comment and the auto-merge comment posted.
Without `max_sessions` the document has no `notifications` key. Use it to bound
review-and-revise rounds on a PR: with one loop for the PR workflow and
`max_sessions: 4`, a PR gets at most four sessions in that loop.

#### Partials

`partials` lists files that contain only `{{ define }}` blocks. Every
prompt template of the loop can call them with
`{{ template "name" . }}`. Each entry is a path or glob that starts with
`prompts/`, has no `..` segment and no quote or backslash; the engine
resolves it relative to the directory of the `WORKFLOW.*.md` file
(`.jigz/workflows/`), so `prompts/partials/*.md` selects files of
`.jigz/workflows/prompts/partials/`. Entries are limited to the prompts
tree because jig hashes and copies only that tree: a change under
`prompts/` reloads the loop, and `jig validate` copies the tree next to
the generated documents so the engine finds the partials. A pattern that
selects a prompt template fails the engine load, so keep partials in a
directory of their own.

#### Rule agent forms

A rule's `agent` is a name, a list, or a mapping that overrides the fleet
agent for that rule:

```yaml
work:
  issues:
    agent: build-pool
    rules:
    - { name: build, match: { labels: [agent:build] }, prompt: prompts/build.md }
    # list: try coder first, then night-owl, for this rule only
    - { name: docs, match: { labels: [agent:docs] }, prompt: prompts/docs.md,
        agent: [coder, night-owl] }
    # mapping: same agent, different model and hours for this rule only
    - { name: plan, match: { labels: [agent:plan] }, prompt: prompts/plan.md,
        agent: { use: coder, model: claude-opus-4-5,
                 schedule: { windows: [{ days: [mon, tue], start: "09:00", end: "17:00" }] } } }
```

`use` is required in the mapping form; `model` passes through
unvalidated and `schedule` follows the same hours rules as
`operating_hours`.

#### Split loops

Rules with different agent sets become separate loops
(`WORKFLOW.<slug>-*.md`), each with its own narrowed query. A split
loop's query is the entry's `query_filter` with its `label:` terms
replaced by the match labels and the `stage` labels of the loop's own
rules; exclusions stay. Base labels such as `in-progress` are dropped,
because a loop dispatches every item its query lists, and a shared label
would list another loop's items. Stage labels are not added to
`active_states`. Every split loop needs its own `match.labels`, and no
label may repeat across loops:

```yaml
work:
  fast:
    agent: quick
    rules:
    - { name: quick-fix, match: { labels: [agent:quick] }, prompt: prompts/build.md, agent: quick }
    - { name: deep-fix, match: { labels: [agent:deep] }, prompt: prompts/build.md, agent: deep }
```

This renders `WORKFLOW.fast-quick.md` (query `-label:needs-human
label:agent:quick`) and `WORKFLOW.fast-deep.md` (`-label:needs-human
label:agent:deep`).
A shared label or a loop with no labels fails validation.

#### Tracker and hooks overlays

```yaml
work:
  prs:
    tracker: { kind: github-pr }   # everything else inherits the top-level tracker
    hooks:
      timeout_ms: 30000           # this entry only; scripts still inherit
```

#### Hook environment

Every hook script (`after_create`, `before_run`, `after_run`) starts with a
short generated preamble that exports the variables below. Use these
names in hook scripts, including scripts the config points at by path.

| Variable | Meaning | Hooks |
|---|---|---|
| `JIG_ISSUE_ID` | Stable tracker-internal id of the item | all |
| `JIG_ISSUE_IDENTIFIER` | Item identifier, for example `owner/repo#123` | all |
| `JIG_WORKSPACE` | Absolute path of the item's workspace | all |
| `JIG_ATTEMPT` | Current attempt number | all |
| `JIG_TRACKER_PROJECT` | Tracker project, `owner/repo` | all |
| `JIG_SELF_REVIEW_STATUS` | Self-review outcome: `disabled`, `passed`, `cap_reached` or `error` | `after_run` |
| `JIG_SELF_REVIEW_SUMMARY_PATH` | Path of the self-review summary; empty when self-review wrote none | `after_run` |

The engine's own names (`SORTIE_ISSUE_ID`, `SORTIE_ISSUE_IDENTIFIER`,
`SORTIE_WORKSPACE`, `SORTIE_ATTEMPT`, `SORTIE_TRACKER_PROJECT`,
`SORTIE_SELF_REVIEW_STATUS`, `SORTIE_SELF_REVIEW_SUMMARY_PATH`) still work
in hooks. Hooks run with a restricted environment: `PATH`, `HOME` and a few
other standard variables plus the engine's `SORTIE_*` variables. Other
variables of the supervisor are not passed.

Agent processes do not receive the item id, identifier or attempt; prompts
use `{{ .issue.identifier }}` instead. They receive `JIG_WORKSPACE` (their
working directory, set by `jig agent-exec`), `JIG_TRACKER_PROJECT` and
`JIG_WORKSPACES` (the workspace root).

```yaml
hooks:
  before_run: |
    git checkout -B "auto/$JIG_ISSUE_IDENTIFIER" origin/main
```

A hook can be a file path, for example `before_run: ./hooks/before.sh`.
The preamble runs first, so the file reads the same `JIG_*` variables.

### Workspace cleanup

`cleanup:` in `.jigz/config.yaml` runs a command that removes old
workspaces. `jig setup` installs `.jigz/scripts/cleanup.sh` (mode 0755,
never overwritten; edit freely) and the scaffold points at it:

```yaml
cleanup:
  enabled: false    # default
  interval: 6h      # required when enabled
  command: .jigz/scripts/cleanup.sh   # required when enabled
  max_age_days: 14  # required when enabled
  dry_run: false
  timeout: 10m      # default; kills the command and its process group
```

The shipped script removes per-item checkouts (two levels down:
`<loop>/<checkout>`) older than `max_age_days`, and only when the git
tree is clean: a checkout that is not a git work tree, whose `git
status` fails, or with uncommitted changes is kept and reported as
`kept: <path> (<reason>)`. Uncommitted agent work is never thrown away.
It prints `removed: <path>` for every removed checkout (dry run reports
without removing); anything else it prints lands in the run's output
tail. A non-zero exit fails the run. Up to 200 removed paths are
recorded.

When `enabled` is true, the supervisor runs `command` through `sh -c`
every `interval`, starting one interval after startup, in the repo root.
`jig` refuses to start when `command`, `interval` or
`max_age_days` is missing, or when `max_age_days` is not less than
`workspace.retention_days` in a workflow file that sets it.

The command gets these environment variables and no arguments:

- `JIG_CLEANUP_DRY_RUN`: `1` or `0`. With `1` the command removes
  nothing and only reports what it would remove.
- `JIG_CLEANUP_MAX_AGE_DAYS`: `cleanup.max_age_days`.
- `JIG_CLEANUP_WORKSPACE_ROOT`: `.jigz/workspaces` in the repo root.
- `JIG_CLEANUP_DB_DIR`: `.jigz/workflows`, which holds the workflow
  SQLite DBs.

Exit code 0 is success and anything else is a failed run. A failed run
is logged and does not stop the supervisor. The command may print
`removed: <path>` lines on stdout. Up to 200 of them are recorded as the
paths of the run. The last 4 KB of stdout and stderr are logged. Two
cleanups never run at the same time: a run that finds another one active
is skipped.

`jig cleanup` runs the command once with the same contract.
`--dry-run` sets `JIG_CLEANUP_DRY_RUN=1`. `--json` prints
`started_at`, `duration_ms`, `exit_code`, `dry_run`, `command`,
`output_tail` and `error`. It exits non-zero when the command fails or
none is configured.

Every run, scheduled or manual, writes its result to
`<hash>.cleanup.json` next to the status document.

Results and failures are visible without opening logs: the Operations
page has a Last cleanup column per project (ok/failed chip, dry-run
marker, time, exit code, path count), the project page repeats it under
its Ops lines, and `jig status` prints one `cleanup:` line (`--json`
includes the recorded paths). The full output tail stays in the
supervisor log; `jig cleanup --dry-run` previews a run safely.

### Idle sleep

`sleep:` in `.jigz/config.yaml` stops the engine children when there is
no work and starts them again later:

```yaml
sleep:
  enabled: false       # default
  idle_timeout: 15m    # required when enabled
  wake_interval: 1h    # required when enabled
```

Both values are Go durations and must be positive when `enabled` is true;
`jig` refuses to start otherwise. With `enabled: false` the loops
run all the time.

Every 30 seconds the supervisor checks each child for running sessions and
pending retries. It reads the running sessions from the child's log and the
retries from the workflow database. A child with a running session or a
pending retry is busy. A child whose sessions the supervisor cannot account
for, such as one that has not yet logged its first poll, is also busy, so
the supervisor never sleeps on a guess. A session whose end the log does not
show counts as running until the child's next poll finds none. When no
child has been busy for `idle_timeout`, the supervisor stops all children
and keeps running. The idle time starts again at every busy
observation and whenever the children are started, so `idle_timeout`
should be larger than the polling interval of the workflows: a freshly
started child needs that long to find work.

The children start again `wake_interval` after sleep began, or when
`jig wake` is run. A wake starts them the same way as startup,
with the config and workflow files now on disk. `jig wake` writes a
request file next to the status document, which the supervisor reads at
its next 2-second tick.

While the run sleeps, new issues and PRs wait until the next wake. Turn
sleep off if agents must start immediately. The supervisor, the cleanup
schedule and the status document keep running. `stop` ends the run.
`restart --when-idle` wakes the loops, and a restart without
`--when-idle` replaces the process as usual.

### GitHub settings

`github:` in `.jigz/config.yaml` lets the dashboard read GitHub with the
repo's token: pull requests for run history (enrichment), and agent
questions on the Questions page:

```yaml
github:
  enrich: false             # default; true turns on run-history enrichment
  questions: true           # default; false turns off the question scan, the resume and the Questions page for this project
  interval: 10m             # enrichment pass interval, at least 1m
  api_url: https://api.github.com   # default; set it for GitHub Enterprise Server
  details: false            # also read commits, changed files and check runs
  questions_interval: 2m    # question scan interval, at least 1m
  questions_window: 168h    # how far back a scan looks for stored runs
```

`enrich` and `questions` are independent. The dashboard reads the block on
each pass, so an edit applies without a restart. The token is resolved as
for the loops: `JIG_TOKEN`, then `token:`, then `GITHUB_TOKEN` and
`GH_TOKEN`. An unknown key or an invalid value in the block stops `jig`
from starting. A `--unite` page uses the block of the first project, by
slug, that sets `enrich: true`; `questions` is read per project.

### Questions

An agent asks in an issue or PR comment, or in the issue or PR
description, in an HTML comment block around the question text:

```
<!-- agent-question id="q-1" audience="project" blocking="true" resume="agent:plan" -->
**Question:**
1. Should this ship in the first release?
2. Should the indexer be a separate package?
**Suggested answer:**
1. No; split the indexer into its own issue.
2. Yes.
<!-- /agent-question -->
```

A comment holds one block. All questions go in it, numbered 1 to N, and the
suggested answers are numbered to match. The `Question` and `Suggested
answer` text each end at the first blank line, so the block has none.

`id` is required and matches `q-` followed by up to 64 letters, digits,
`_` or `-`. `audience` is `product` or `project`. `blocking` is `true` or
`false`; `resume` is the trigger label to add back when the item is
answered, any `agent:` label except `agent:done`, and is required on a
blocking question. A blocking question parks the item with `needs-human`;
a non-blocking question does not stop the work. Markers inside fenced
code blocks are ignored.

A question in the description follows the same rules as one in a comment.
It counts as asked at the time the item was created, and its author is the
account that opened the item, who must be authorized (see the resume
rule). Editing the description does not change that time. Answers are read
from comments only; an `agent-answer` block in a description is ignored.

The Questions page lists the questions of every project with
`github.questions` on. The dashboard is read-only: it writes nothing to
GitHub. Each open question has a text area filled with the suggested
answer, which the person can edit. The Copy button copies a complete
`agent-answer` comment (below) with that text, and "Open on GitHub" opens
the question's comment, or the item for a question in the description, in a
new tab to post it. The page has filters (project, audience, blocking, open
or answered) that apply as they are changed. With one project the project
filter is fixed. A countdown shows the time to the next scan, and the page
reloads once when it ends. A project with no token shows "no GitHub
token", and a failed scan shows its error.

A person answers in one of two ways:

| Path | What the person posts | How the item resumes |
|---|---|---|
| GitHub, plain text | Free text in a comment | The person flips the labels by hand: remove `needs-human` and add the trigger label. The agent reads the comment on its next run |
| GitHub, answer block | A comment with an `agent-answer` block that carries the question id (template below) | The scanner flips the labels automatically |

The answer block is:

```
<!-- agent-answer id="q-1" -->
No; split the indexer into its own issue.
<!-- /agent-answer -->
```

Plain text cannot be matched to a question id, so it never resumes an
item automatically. An answer added by editing an older comment keeps that
comment's creation time and does not count as new; post a new comment.

#### Resume rule

The question scanner runs in the dashboard (every registered project with
`github.questions` on and a token). It resumes an item when all of these hold:

- The item carries `needs-human`.
- It has at least one blocking question, every blocking question has an
  answer block from an authorized author, and no question block is
  malformed or disagrees with another on `resume`.
- At least one of those answers was posted after the latest time
  `needs-human` was added to the item. Times have one-second precision, so
  an answer posted in the same second does not count.

The scanner then adds the `resume` trigger, removes `agent:done`, and
removes `needs-human`, so the loop dispatches the item again on its next
poll. It changes only those labels and posts no comment. An item with a
problem (malformed block, answer to an unknown id, differing `resume`
values, a failed label call) stays parked and the page lists the problem
with a link to the comment; fix it on GitHub. The scanner does not run
under `jig --no-dashboard` or with `questions: false`; change the labels by
hand then.

Authorized authors are the token account, the repository owner, authors
whose association with the repository is `OWNER`, `MEMBER` or
`COLLABORATOR`, and the item's assignees. An answer from anyone else does
not count, and a question from anyone else is not listed; the page shows it as "ignored: <author>". Prompts tell agents
to consider only comments from the same authors.

The scanner reads up to 100 parked items per project per pass, which is
the size of one GitHub search page. Further parked items are not scanned
until others leave the list.

### Spend controls

`work:<slug>.budget` in `.jigz/config.yaml` caps the tokens one loop may
use per period. The key is the work slug (the `WORKFLOW.<name>.md` stem);
a name without a workflow file is an error at startup.

```yaml
work:
  issues:
    budget:
      period: weekly          # weekly (ISO week, Monday start) or monthly (calendar month)
      timezone: Africa/Johannesburg   # IANA name, default UTC; period boundaries use it
      max_tokens: 20000000    # 0 or absent turns the budget off
      warn_percent: 80        # default 80
      on_exceed: pause        # only pause is accepted
      poll_interval: 1m       # default 1m
      # max_usd: 50           # ignored with a startup warning: the engine records no cost
```

Every `poll_interval` the supervisor opens the workflow's SQLite DB
read-only and sums `total_tokens` in `run_history` for the runs completed in
the current period (started, when a run has no completion time). Runs with
`tokens_measured` 0 are counted as `unmeasured_runs`; their tokens are not
estimated. A missing DB, table or column counts as zero. The engine writes
timestamps as RFC3339 in UTC; a timestamp without a zone is read as UTC. When
a DB cannot be read, the supervisor prints one line and keeps the last
figures until the next successful poll.

At `warn_percent` the supervisor prints one line to stderr per period and
sets `warned` in the status document. At `max_tokens` it stops that
workflow's engine loop and keeps it stopped (`paused_by: budget`). Other
workflows keep running. The loop starts again at the next period, or on
`resume` or `budget override`, or at the next poll when the tokens used are
below the cap again (for example after `max_tokens` was raised and the
supervisor restarted). The cap is not a hard limit: runs in flight
when it is reached finish their tokens first, and use up to `poll_interval`
more. The pause survives a supervisor restart, and a workflow that is over
its cap is not started. Pause state is stored in `<hash>.budget.json` next
to the status document. Idle sleep does not start paused workflows at wake.
A manual pause is kept across periods; the override and the warning reset at
each period.

### Token and identity

The token is the identity of both the loop and the agent: it
authenticates GitHub API polling, label changes, clones of private
repos, and every `gh` command the agent runs (reading issues, posting
comments, opening PRs). `@me` in the assignee scope means whoever owns
this token. Use a token for the account you want the work attributed
to — a separate bot account keeps agent commits, comments, and PRs off
your personal name.

Precedence, first set wins:

| Order | Source | Notes |
|---|---|---|
| 1 | `JIG_TOKEN` | Overrides just the loop; the `gh` CLI does not read it |
| 2 | `token:` in `.jigz/config.yaml` | For local-only setups; never commit a token |
| 3 | `GITHUB_TOKEN` | Also read by the `gh` CLI, covering the agent's `gh` calls |
| 4 | `GH_TOKEN` | Same as above, last resort |

Exporting `GITHUB_TOKEN`/`GH_TOKEN` covers both the loop and the agent.
`JIG_TOKEN` covers only the loop, so the agent's `gh` calls still need
one of the other three. With none set, `jig` refuses to start.

```sh
# classic token: repo scope; or fine-grained: Contents, Issues,
# Pull requests read/write on the repos you loop over
export GITHUB_TOKEN="ghp_..."
gh auth status   # must show the account the work is attributed to
```

`JIG_REPO=owner/name` pins the repo id instead of detecting it from the
git `origin` remote (a `--repo` flag on `setup` does the same for that
run). `JIG_ONLY=issues` starts only those loops; see
[Selecting loops](usage.md#selecting-loops).

### Other environment variables

| Variable | Effect |
|---|---|
| `JIG_TOKEN` | Highest-precedence tracker token; loop only (the `gh` CLI does not read it). See [Token and identity](#token-and-identity). |
| `JIG_REPO` | Pins the repo id (`owner/name`) instead of detecting it from git `origin`. |
| `JIG_ONLY` | Comma-separated `WORKFLOW.*.md` stems to start; unset runs all. Unknown names are fatal. See [Selecting loops](usage.md#selecting-loops). |
| `JIG_WORKSPACES` | Directory that holds the agent checkouts. `jig` sets it for each loop (`.jigz/workspaces` by default) and the shipped workflows use it as `workspace.root: $JIG_WORKSPACES/<loop>`. Set it by hand only when validating a workflow file with the engine directly. |
| `JIG_BIN` | Path of the `jig` binary that the supervisor starts for each loop. Without it, `jig` uses a `jig` binary next to the running one, then `jig` on `PATH`. |
| `JIG_REGISTRY_DIR` | Directory for the project registry and status files. Unset, they are kept in the shared user cache directory, so several `jig` processes on one machine see each other. |
| `JIG_TRACKER_TOKEN` | The value `jig setup` seeds as `tracker.api_key: { from_env: JIG_TRACKER_TOKEN }`. Export it in the supervisor's environment or the loops stay offline. |
| `JIG_ENGINE_BIN` | Use this engine binary; beats every other engine setting. |
| `JIG_ENGINE_REPO` / `JIG_ENGINE_VERSION` | Fetch the engine from this repository's releases / at this version (`vX.Y.Z`) instead of the configured one. See [The engine](install.md#the-engine). |
| `JIG_ENGINE_URL` / `JIG_ENGINE_SHA256` | Fetch one engine archive from this URL (verified against the SHA-256; URL without it warns). |

### Working hours and agent accounts

Working hours and agent accounts live in the repository's
`.jigz/config.yaml`, so every repository has its own. Without a
schedule the hours are always open. The Agents page of the dashboard
shows the agents and the pools.

```yaml
operating_hours:                # gates every loop; omitted = 24/7
  timezone: Europe/Madrid       # IANA name; empty = local time
  end: finish                   # finish | stop; default finish
  windows:
    - days: [mon, tue, wed, thu, fri]
      start: "09:00"
      end: "18:00"               # an end earlier than start wraps past midnight

agents:
  claude-main:  { runtime: claude-code, model: claude-sonnet-5-5 }
  claude-spare: { runtime: claude-code, model: claude-sonnet-5-5,
                  env: { ANTHROPIC_OAUTH_TOKEN: { from_env: CLAUDE_SUB_2 } } }
  pi-api:       { runtime: pi, model: sonnet,
                  env: { ANTHROPIC_API_KEY: { from_env: ANTHROPIC_API_KEY } } }

  codex-api:    { runtime: codex, model: gpt-5.4,
                  env: { OPENAI_API_KEY: { from_env: OPENAI_API_KEY } } }

pools:
  build-pool: [claude-main, claude-spare, pi-api, codex-api]   # ordered fallback
```

#### Agent runtimes

`runtime` names an agent kind the engine registers. Generated documents run
every agent through `jig agent-exec`, which starts the agent's `command`, so
jig has its own default command per runtime. The engine's defaults are the same.
A `command` is split on whitespace and run without a shell; the engine's
arguments are appended.

| Runtime | Default `command` | Notes |
|---|---|---|
| `claude-code` | `claude` | Usage-limit detection and transcript reading are built in |
| `pi` | `pi` | Usage-limit detection and transcript reading are built in. Registered by the default engine only; the upstream engine profile lacks it |
| `codex` | `codex app-server` | |
| `opencode` | `opencode` | |
| `copilot-cli` | `copilot` | |
| `agent-client-protocol` | required | The engine has no default command either |
| `mock` | required | Test agent of the engine |

For every runtime, `model` is written to the settings block named after the
runtime. `jig agent-exec` works with all of them. Stdout-based usage-limit
detection and transcript viewing in the dashboard cover `claude-code` and
`pi` only; for other runtimes limits are detected from stderr lines.

A credential is always a reference to an environment variable of the
supervising process (`{ from_env: NAME }`); a literal secret is
rejected. An unset variable makes the agent unavailable, with the
reason shown.

The machine file (`<user config dir>/jigz/machine.yaml`) now carries
only the `engine:` block (which release to run; `JIG_ENGINE_REPO` and
`JIG_ENGINE_VERSION` override it, see [The engine](install.md#the-engine)) and
the `dashboard:` block (pages and sections to hide, see Hiding pages
and sections). It no longer holds hours or a pool.

Hours gate every loop on its own. A loop's candidates are the agents of
its work entry (`work.<slug>.agent`, falling back to the rule's own
list), evaluated in order together with the operating hours and each
candidate's own `schedule`: a candidate is available when the operating
hours and its hours are open, it is not marked exhausted and its
environment references resolve. The first available candidate is the
loop's agent, shown by `jig status` and the dashboard. A blocked loop
goes offline with the reason in the status document — outside
operating hours, `agent <name> is outside its working hours`, every
agent is exhausted, every agent has unresolved environment references,
or no agent runtime is registered on the engine — and an estimated
start: the earliest of the next working-hours start of its candidates,
their usage-limit resets and the next operating-hours window; without a
future input the estimate is empty. A paused loop is never started.

When the gate closes, the end behaviour is the `end` of the
`operating_hours` block when operating hours closed it, otherwise the
`schedule.end` of the candidate whose hours closed it. When every
candidate is exhausted, the loop stops at once, without waiting for
running sessions, because every retry would fail and use up one of the
issue's `agent.max_sessions`. In every other case the behaviour is `finish`.
The loop starts again at the earliest reset among its candidates; the
first candidate that is available then runs the loop.

With a pool, the supervisor starts each loop with the agent command set to
`jig agent-exec <agent>`. `agent-exec` runs the CLI of that fleet agent
with its `env` mapping resolved from the supervisor's environment, passes
stdout through unchanged and exits with the CLI's exit code. An unset
credential reference makes the agent unavailable and `agent-exec` exits
75 without running anything. When the CLI output
reports a usage limit, the agent is marked exhausted until the reset time in
the message (`resets 1:10am (UTC)` or an epoch after `|`), or one hour later
when the message carries none. The state is stored in
`<repo root>/.jigz/data/state.json`, created by `jig setup` and kept out
of commits by the `.jigz/` gitignore rule. A `pool.json` written by an
older jig in the per-user state directory is still read and never
written again. Without an available agent, `agent-exec`
exits 75. When the active agent changes, the loop document is rewritten
once the loop has no running session; the engine re-reads it live.
A bare runtime (`jig agent-exec <runtime>`) runs the first available
fleet agent with that runtime — same availability rules, no pool file.

Each loop runs its first candidate agent. When the gate picks another
candidate — a usage limit, closed hours, unresolved credentials or an
unregistered runtime — the supervisor rewrites the loop document for
that agent once the loop has no running session and the engine
re-reads it live; no restart follows. The return to the primary waits
until the pick survives consecutive ticks, so a flapping limit does
not rewrite the document twice. `jig status` shows the agent, the
primary and the fallback reason per loop, and the Operations page
alerts while a loop runs a standby agent. A document rendered for any
candidate agent is current: `jig validate` accepts it, and `setup`,
`start` and `reload` leave it unchanged.

A usage limit is recognised in these lines of the CLI output: an error
event of the runtime on stdout (for `claude-code`, a `result` event with
`is_error`; for `pi`, a message with `stopReason` `error`), a stderr line
with error wording, and a plain line on stdout or stderr that starts with
`You've hit your` (for example `You've hit your 5-hour limit · resets
1:10am (UTC)`). The text must contain a phrase such as `usage limit`,
`limit reached`, `hit your 5-hour limit`, `hit your weekly limit`,
`insufficient_quota`, `quota exceeded` or `out of credits`, or match a
pattern of the machine file's `limits` block. Agent prose inside
`assistant` events is never checked. The run history class
`failed_limit` uses the same phrases on the engine's error text, so a
limit that surfaces as a failed credential check is classed `failed_limit`.

The supervisor reads `state.json` every 2 seconds, so a limit reaches the
gate before the engine's first retry. Each engine run, including one that
`agent-exec` refuses with exit 75, is recorded in the workflow database and
counts toward `agent.max_sessions` of that issue; the count survives a loop
restart. Runs that fail on a limit therefore use up the budget unless the gate
stops the loop first.

A usage limit is usually detected by `agent-exec` during the engine's credential
check, which runs before each worker run. The run then fails before the turn
starts, the engine posts no failure comment, and it retries after about 10
seconds with the next available agent. A limit reached in the middle of a turn
fails that engine session: the engine posts a session-failure comment on
the issue and retries the same way. A message naming an unknown model
(`model not found`, `unknown model` and similar from either CLI) marks the
agent exhausted the same way, so a misconfigured model fails over to the
standby instead of retrying forever.

The dashboard, the CSV export and the JSON API class each finished run as
`succeeded`, `failed_agent`, `failed_workspace`, `failed_limit`, `cancelled`,
`timeout` or `handoff_withheld`. The class comes from the run status and the
engine's error text. A usage limit is recognised in that text when the agent
CLI reports it, or when `agent-exec` writes its limit line and the engine
includes it in the error. `failed_agent` covers every other failure.

Known limitation: with `max_concurrent_agents` above 1, a loop can claim new
work between the window close and the moment it is stopped.

Using a subscription account unattended can be restricted by the provider.
Checking the provider's terms is your responsibility.

### Machine file

`<user config dir>/jigz/machine.yaml` (usually `~/.config/jigz/machine.yaml`)
holds per-machine settings: which engine release to run and what the
dashboard hides. A missing file means defaults everywhere; unknown keys
are an error. `jig setup` creates it (mode 0600) with `dashboard.hide`
and `limits` live at their defaults and `engine:` commented out, so the
engine default moves with the binary:

```yaml
#engine: { repo: kinged007/sortie, version: v1.28.1 }  # pin a version here only to hold one
dashboard:
  hide:
    pages: [questions, backups]   # removed from the sidebar; their routes answer 404
    sections: [session]           # removed from the run page; nothing hidden by default
limits:
  default_reset: 1h               # exhausted-until when a limit message names no reset
  patterns: ["rate limit", "quota exceeded"]  # extra usage-limit regexps, case-insensitive
```

`JIG_ENGINE_REPO` and `JIG_ENGINE_VERSION` override the `engine:` block;
`JIG_ENGINE_BIN` beats both. The dashboard re-reads `machine.yaml` when
the file changes, so hiding edits apply without a restart.

Hideable pages (from `internal/machine/dashboard.go`):

| Name | Hides |
|---|---|
| `operations` | Operations page, its live fragment, `GET /api/v1/fleet`, `GET /api/v1/metrics` |
| `runs` | Runs list, CSV export, run detail pages, `GET /api/v1/runs` |
| `questions` | Questions page, `GET /api/v1/questions` |
| `usage` | Usage page, `GET /api/v1/usage` |
| `workflows` | Workflows page |
| `projects` | Projects list, project pages, `GET /api/v1/projects` |
| `agents` | Agents page |
| `settings` | Settings page |
| `backups` | Backups page and the backups list on project pages |
| `logs` | Child log page of a workflow |

Hideable sections:

| Name | Hides |
|---|---|
| `agent_log` | Agent log and agent events on the run detail page, and the endpoint behind them |
| `session` | Session id, workspace directory and resume command on the run detail page |
| `log_tail` | Log tail on the run detail page and the endpoint behind it |
| `audit_log` | Audit log on Operations and project pages, `GET /api/v1/audit` |
| `effective_config` | Effective configuration table on project pages |

`limits.patterns` extends the built-in usage-limit match (`internal/machine/limits.go`)
used by `jig agent-exec`: when the CLI output matches, the agent is marked
exhausted until the reset time in the message, or `default_reset` later.
A bad regexp fails the machine file load.

Evidence status: Confirmed against `internal/config`, `internal/gen`, and `internal/machine` unless noted.
