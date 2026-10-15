# sortie-loop

[![CI](https://github.com/kinged007/sortie-loop/actions/workflows/ci.yml/badge.svg)](https://github.com/kinged007/sortie-loop/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/kinged007/sortie-loop)](https://github.com/kinged007/sortie-loop/releases) [![Go](https://img.shields.io/badge/Go-1.26-blue)](go.mod)

Run autonomous coding agents against your GitHub issues with one command.

`sortie-loop` watches a repo's issues and pull requests, and for each
labeled item spins up an AI coding agent that reads the issue, writes the
code, and moves the work forward — planning, implementing, reviewing, and
merging — mostly on its own. You manage the backlog by adding labels and
assigning issues; the agents do the rest. GitHub is the only supported
tracker.

## What is what

**sortie-loop** (this repo) is a small Go wrapper. It does setup (config,
labels, workflow files) and then launches one loop process per workflow,
plus a web dashboard. It contains no agent itself.

**Sortie** (the engine, from the
[kinged007/sortie](https://github.com/kinged007/sortie) fork) does the
heavy lifting inside each loop: it polls GitHub on a timer, claims a
matching issue, clones the repo into a scratch workspace, hands the issue
text to an agent, and records the run in a local SQLite DB. The fork
exists because the loops need two pieces upstream lacks: the `pi` agent
adapter and the `github-pr` tracker (which treats pull requests as
work items).

**The agent** is `pi`, a coding agent that runs in a terminal the same
way you would: it reads files, edits code, runs checks, and shells out
to the `gh` CLI to read comments, open PRs, and move labels.

## How the agents pick up issues

There is no queue and no webhook. Every loop polls GitHub roughly every
60 seconds with a search query built from three parts:

1. **Label filter** — each loop watches its trigger labels plus
   `in-progress`. `prs` looks for
   `label:agent:review,agent:build,agent:merge,in-progress`, and `issues`
   for its own list (see Loops below). Adding a trigger label enqueues the
   item; the agent removing it plus adding `agent:done` takes it out of
   every loop. The `issues` loop lists issues and the `prs` loop lists PRs,
   so an item left mid-run by a crashed process is re-picked by the loop
   that started it. The loop's dispatch rules choose the job from the
   trigger label; the first matching rule wins.
2. **Assignee scope** — by default every query ends in `assignee:@me`,
   meaning only items assigned to the token owner are picked up. This
   keeps several people (or bots) from fighting over one backlog. Set
   `assignee: ""` in config for a shared backlog where anyone's issue
   is fair game.
3. **Escalation guard** — every query excludes `-label:needs-human`,
   so anything an agent flagged for a person stays parked until a
   human removes the label.

When a loop claims an issue it clones the repo into
`.sortie/workspaces/<loop>/`, checks out a branch named
`auto/<issue-number>`, and renders a prompt template
(`.sortie/workflows/prompts/*.md`) with the issue title, body, labels,
and comments. The agent follows that prompt: it re-reads the full
comment thread with `gh` (comments can override the description), makes
the change, runs the project's checks, pushes, and posts a structured
summary comment (Summary / Changes / Verification / Issues encountered).
State moves forward through labels — `build` opens a PR and labels it
`agent:review`, `review` sends findings back as `agent:build` or stops on
`agent:done`, and a person (or the PM prompt, see below) labels an
approved PR `agent:merge`.

## Labels

Fifteen labels, one role each. The six triggers are applied by a person, a
chained agent, or the PM prompt; P0–P3 are a person's triage call; the rest
are set by the agents themselves.

| Label | Colour | Role |
|-------|--------|------|
| `agent:plan` | yellow | Trigger: write an implementation plan for this issue |
| `agent:plan-build` | yellow | Trigger: plan this issue, then implement it with no human step |
| `agent:build` | yellow | Trigger: implement this issue, or apply review feedback on this PR |
| `agent:review` | yellow | Trigger: review this PR |
| `agent:merge` | yellow | Trigger: merge this PR |
| `agent:docs` | yellow | Trigger: update the docs for merged changes and open a PR |
| `in-progress` | purple | Claim: an agent is working on this right now |
| `agent:done` | green | State: the agent finished |
| `needs-human` | red | Escalation: a person has to look at this |
| `united-into` | blue | State: folded into another issue's fix; that issue is the one to read |
| `backlog` | grey | Deferred by a person; no loop and no PM should touch it |
| `P0` | red | Priority: dispatch before everything else |
| `P1` | orange | Priority: high |
| `P2` | yellow | Priority: normal |
| `P3` | grey | Priority: lowest |

The contract, in one line: **a trigger starts a loop, the loop adds
`in-progress` and keeps the trigger while it works, and the run ends with
`agent:done`** (plus `needs-human` when a person is needed). Because the
trigger stays on the item for the whole run, a loop that is interrupted
finds the item again on its next poll and finishes the job.

`needs-human` is an exclusion in every loop's query, so a flagged item is
parked until a person removes the label. `agent:done` is deliberately
neither an active state nor a terminal one: it marks the item finished
without closing it, and re-labelling the item with a trigger is how you
run it again.

### Chaining loops

The build, review, and review-fix prompts route their own output by
default: each drops its trigger and the claim, then applies the next
stage's trigger in the same command.

```sh
# build: the PR it just opened goes to the review loop
gh pr edit <n> --add-assignee @me --add-label "agent:review"
# review: findings to fix, hand them to a fix agent
gh pr edit <n> --remove-label "agent:review,in-progress,agent:done" --add-label "agent:build"
# review: clean, stop and wait for a person
gh pr edit <n> --remove-label "agent:review,in-progress" --add-label "agent:done"
# review-fix: fixed branch, send it back for another review
gh pr edit <n> --remove-label "agent:build,in-progress,agent:done" --add-label "agent:review"
```

`agent:done` keeps every loop away from an item, so a trigger meant for
the next stage is always added in the same command as its removal. No
agent applies `agent:merge`: an approved PR waits for a person, or for
the PM prompt in `.sortie/pm-agent.md`, to label it.

The chained label must land on the item the next loop watches: the `prs`
loop watches PRs, so label the PR, not the issue it came from. `.sortie/workflows/prompts/review.md` Step 4 and
`review-fix.md` Step 5 carry these forms.

## The loops

Two workflows ship by default (each is one `WORKFLOW.*.md` file, each
runs as its own loop process). Each file has one dispatch rule per
trigger label:

| Loop | Rule | Watches | Does |
|------|------|---------|------|
| `issues` | `plan` | issues with `agent:plan` | Writes an implementation plan as an issue comment. The agent removes `agent:plan` and adds `agent:done`; a human reviews the plan and labels the issue `agent:build` to trigger development. |
| `issues` | `build` | issues with `agent:build` or `agent:plan-build` | Reads the issue's recent comments (the plan comment and later human comments), implements the change and opens a PR, then labels that PR `agent:review` and assigns it to the token owner. For `agent:plan-build` with no plan comment, it posts the plan first and implements in the same run. (The parked `agent:quick` track merges straight to the base branch with no PR — see `prompts/quick.md`.) |
| `issues` | `docs` | issues with `agent:docs` | Updates the docs for merged changes and opens a PR. |
| `prs` | `review` | PRs with `agent:review` | Reviews the PR in three passes, then routes on its own verdict: `agent:build` when there are findings to fix, `agent:done` when the review is clean. |
| `prs` | `review-fix` | PRs with `agent:build` | Applies posted review feedback, pushes to the PR branch, then hands the PR back as `agent:review` for another pass. |
| `prs` | `merge` | PRs with `agent:merge` | Reads the PR plus all comments, files follow-up issues for leftovers, and merges into the PR's base branch. Conflicts end on `agent:done,needs-human` and the loop drops the PR. Uses `git merge --ff-only`, so a branch that drifted behind base stalls and escalates instead of rebasing. |

The same `agent:build` trigger runs `build` on an issue and `review-fix` on a
PR: the tracker kind decides which loop can see the item at all, and each
agent reads the item to know which job it is.

Workflow-wide settings are shared by the rules of one file: hooks,
`max_turns` and the other `agent:` keys. The `prs` workflow uses
`max_turns: 10` and one set of workspace hooks (full fetch of the PR head
with an `origin` remote) for all three rules. The `issues` workflow pushes
the `auto/<issue>` branch only when the run committed something.

### Migrating from the five-file layout

`setup` and the loop startup never change or remove an existing workflow
or prompt file. While any of `WORKFLOW.plan.md`, `WORKFLOW.build.md`,
`WORKFLOW.review.md`, `WORKFLOW.review-fix.md` or `WORKFLOW.merge.md`
exists in `.sortie/workflows/`, `WORKFLOW.issues.md` and `WORKFLOW.prs.md`
are not written, because both sets would process the same issues. A
warning on stderr says so. To migrate:

1. Delete the five old files, and their `.sortie-<name>.db` files if you
   do not need the run history.
2. Rerun `sortie-loop setup` to install the two new files.
3. In `.sortie/config.yaml`, rename old loop names to `issues` or `prs` in
   the `filters:` and `workflows:` (budget) keys.
4. Existing installs keep their old `prompts/plan.md` and `prompts/build.md`,
   because setup never overwrites a file. Diff them against the shipped
   copies or delete them and rerun `setup`. An old `build.md` that does not
   remove `agent:plan-build` in its finishing step leaves that label on the
   issue, so the issue is dispatched again. `setup` prints a note when
   either file differs from the shipped copy.
5. Add the `agent:plan-build` and `agent:docs` entries to the `labels:` list of an
   existing `.sortie/config.yaml` (copy them from the list above), then
   rerun `setup`. `setup` only creates the labels in that list and prints a
   note for each shipped label that is missing from it.

The loop set is just every `WORKFLOW.*.md` in `.sortie/workflows/` —
drop in a new file (e.g. `WORKFLOW.triage.md`) and it starts as a loop
named by its stem on the next run. Per-loop label queries can be
narrowed via the `filters:` map in `.sortie/config.yaml` without
re-running setup.

`SORTIE_LOOP_ONLY=issues` narrows the run to the named loops, for a
supervisor that drives several repos and wants a different set per
repo. Unset (the default) runs every installed loop. A name that
matches no installed workflow is fatal rather than silently dropped, so
a typo cannot leave that loop's work unwatched.

## Unified dashboard

Every run serves a small status page (default
`http://127.0.0.1:7677`, auto-refreshes every 5s). Each loop also gets
its own debug port starting at 7678. The page shows what is running
right now (issue, loop, turns, tokens, model) alongside all-time totals
read from each loop's SQLite DB, so numbers survive restarts.

Running loops in several repos at once? Pass `--unite`:

```sh
sortie-loop --unite          # in each repo
```

The first `--unite` run starts the dashboard; later ones detect it and
join instead of starting a second page. Every run registers its loops
in a shared registry file, so the one page rolls up all repos: a
per-repo summary plus the same running-now and token tables with a Repo
column. Without `--unite` the dashboard shows only its own repo.

## Install

macOS and Linux, on Intel or Apple silicon.

```sh
curl -sSL https://raw.githubusercontent.com/kinged007/sortie-loop/main/install.sh | sh
```

That downloads a release binary to `~/.local/bin/sortie-loop` and checks
it against the release's `checksums.txt`. No Go toolchain needed.
`./install.sh --prefix DIR` installs to `DIR/bin/sortie-loop` instead,
`--version TAG` pins a release, and `--from-source` builds from a
checkout.

Already have Go?

```sh
go install github.com/kinged007/sortie-loop/cmd/sortie-loop@latest
```

Also requires the **gh CLI** — `setup` shells out to it, and the agent
prompts use it throughout.

### The engine

`sortie-loop` is the supervisor. The work is done by the **engine**, a
`sortie` binary, which polls GitHub, claims work, and runs the agents.
`sortie-loop setup` downloads the pinned engine release, verifies it
against that release's `checksums.txt`, and links it onto your `PATH` as
`sortie`, so `sortie validate` and `sortie stats` work in any shell. A
fresh install needs no separate engine step.

The pinned version is the release the shipped workflows were written
against. Read it with `sortie-loop --dump-version`.

The engine comes from the [kinged007/sortie](https://github.com/kinged007/sortie)
fork, not from upstream `sortie-ai/sortie`. The fork exists because the
workflows need two things upstream does not ship: the **pi agent
adapter**, and the **github-pr tracker**, which treats pull requests as
work items so a review loop can watch a pull request rather than an
issue. See [SECURITY.md](SECURITY.md) for what running a fork means.

To use a different build of the same engine, set `SORTIE_ENGINE_REPO`
and, if its version differs, `SORTIE_ENGINE_VERSION`. To bring your own
binary, set `SORTIE_BIN`. The engine version is pinned rather than
resolved to "latest" on purpose: the workflows are written against
specific engine behaviour, and an engine that changes under them breaks
loops in ways that are hard to trace.

| Variable | Effect |
|---|---|
| `SORTIE_BIN` | use this engine binary |
| `SORTIE_ENGINE_REPO` | fetch the engine from this repository's releases |
| `SORTIE_ENGINE_VERSION` | fetch this version instead of the pin |
| `SORTIE_ENGINE_URL` | fetch one archive from this URL (with `SORTIE_ENGINE_SHA256` to verify it) |

### GitHub token

The token is the identity of both the loop and the agent: it
authenticates GitHub API polling, label changes, clones of private
repos, and every `gh` command the agent runs (reading issues, posting
comments, opening PRs). `@me` in the assignee scope means whoever owns
this token. Use a token for the account you want the work attributed
to — a separate bot account keeps agent commits, comments, and PRs off
your personal name.

1. Create it: `gh auth login`, or generate a token at
   https://github.com/settings/tokens with the `repo` scope (classic
   token is simplest; a fine-grained token needs Contents, Issues, and
   Pull requests read/write on the repos you loop over).
2. Expose it with one of (first set wins: `SORTIE_LOOP_TOKEN`, then
   `token:` in `.sortie/config.yaml`, then `GITHUB_TOKEN`, then
   `GH_TOKEN`):
   ```sh
   export GITHUB_TOKEN="ghp_..."     # or GH_TOKEN; or SORTIE_LOOP_TOKEN to override just the loop
   ```
   The `gh` CLI reads `GITHUB_TOKEN`/`GH_TOKEN` itself, so exporting
   one of those covers both the loop and the agent's `gh` calls. Never
   commit the token — `token:` in config is for local-only setups.
3. Verify: `gh auth status` should show the right account before you
   run `setup` (which syncs labels) or the loop.

## Use (inside any repo)

```sh
cd your-repo
sortie-loop setup    # writes .sortie/config.yaml, updates .gitignore, creates the labels,
                     # installs the engine, and installs .sortie/pm-agent.md
                     # (a project-manager prompt)
sortie-loop          # run every WORKFLOW.*.md loop (Ctrl-C stops all)
```

`setup` detects `owner/name` from the git `origin` remote
(`--repo=owner/name` overrides). The loop re-detects it at every run, so
moving the checkout or forking needs no reconfiguration. A positional
argument selects the repository root and may sit anywhere on the line,
so `sortie-loop setup ./repo --repo owner/name` and
`sortie-loop setup --repo owner/name ./repo` do the same thing.

`setup` confirms before changing a label whose colour or description
differs from the shipped one. It never changes an existing workflow file.

`sortie-loop status [--json] [repo-root]` reports the run serving the
repo-root: version, pid, state, and per workflow its state, port, pid,
restart count and last exit code. `--json` prints the status document
with an added `liveness` field: `running` when the pid is alive, `stale`
when the document says running but the pid is dead, `exited` when the
run ended. The command reads the document from the repo-root and does not
need a registry entry. It exits non-zero when no document is found.

The supervisor writes the status document (schema version 1) to
`<user cache dir>/sortie-loop/status/<hash>.json`, where `<hash>` is the
first 16 hex digits of the SHA-256 of the absolute repo-root. The registry
entry holds the same path as `status_path`. A clean exit removes the
document. A run that ends on a loop failure leaves it with state
`exited`, and the next supervisor for the same root overwrites it. In a workflow entry,
`restart_count` counts recycles and wakes only, because a child that exits without
being asked to ends the run. `last_exit_code` is `-1` when the child was
killed by a signal. `log_path` is the absolute path of the workflow's log
file.

The status document has a top-level `cleanup` object once a cleanup has
run: `last_run_at`, `duration_ms`, `exit_code`, `dry_run` and `paths`.
`sortie-loop status` prints it as one line.

The status document has a top-level `sleep` object: `enabled`, `awake`,
`sleeping_since` and `next_wake` (RFC 3339 or `null`), and, when sleep is
enabled, `idle_since` (RFC 3339, or `null` after a busy observation). While
the run sleeps, the workflow entries have state `sleeping`.
`sortie-loop status` prints one `sleep:` line when sleep is enabled.

A workflow entry has a `budget` object when the workflow has a budget or is
paused: `period`, `period_start`, `tokens_used`, `cap`, `warn_percent`,
`unmeasured_runs`, `paused`, `paused_by` (`budget`, `manual` or empty),
`override` and `warned`. A paused workflow has state `paused`, which takes
precedence over `sleeping`.

### Child logs

The stdout and stderr of each engine child are written to
`<repo-root>/.sortie/logs/<workflow>.log`, interleaved in arrival order and
unchanged. They are still printed to the terminal of `sortie-loop`. The file
is opened for appending, so it keeps its content across child recycles and
supervisor restarts. When a write would take the file over 10 MiB, it is
renamed to `<workflow>.log.1`, `.1` becomes `.2`, `.2` becomes `.3`, and the
old `.3` is deleted. A file that is already over 10 MiB when a child starts
is rotated first. The limit and the 3 rotated files are fixed in the code.
If the log file cannot be opened or written, one warning is printed and the
child keeps running. The per-run agent transcript is not part of these logs.

`sortie-loop cleanup [--dry-run] [--json] [repo-root]` runs the configured
cleanup command once (see [Workspace cleanup](#workspace-cleanup)).

`sortie-loop wake [--json] [repo-root]` starts the engine loops of a run
that is sleeping (see [Idle sleep](#idle-sleep)). `--json` prints `woke`,
`was_sleeping` and `error`. When the run is awake or sleep is disabled it
does nothing, prints `was_sleeping: false` and exits 0. It exits non-zero
when no supervisor is running for the repo-root, or when the supervisor is
not awake 5 seconds after the request.

`sortie-loop pause <workflow> [--json] [repo-root]` stops the engine loop of
one workflow and keeps it stopped, also across a supervisor restart.
`resume <workflow>` clears a manual or budget pause and starts the loop.
`budget override <workflow>` clears a budget pause, turns the cap off for
the rest of the current period and starts the loop. `<workflow>` is the
loop name, the `WORKFLOW.<name>.md` stem. `resume` of a budget pause also
turns the cap off for the rest of the period when the workflow is at or
over its cap, because the loop would be paused again at the next poll
otherwise. `pause` of a workflow that is paused by its budget turns it into
a manual pause, which is kept across periods and restarts. `--json` prints `workflow`,
`changed`, `paused`, `override` and `error`. A command that finds the
workflow already in the requested state does nothing, prints
`changed: false` and exits 0. It exits non-zero when no supervisor is
running, the workflow is unknown, `budget override` is used on a workflow
without a budget, or the status document does not show the change after
5 seconds. Each command writes its own request file next to the status
document (`<hash>.budget.request.<time>-<pid>`), which the supervisor reads
and deletes at its next 2-second tick, whatever the sleep settings are.

Flags: `--unite` joins (or starts) the shared dashboard,
`--dashboard-port=N` forces the dashboard onto port N,
`--no-dashboard` / `--no-server` disable the dashboard page and the
per-loop debug ports respectively. A positional arg selects the repo
root (default: current directory).

`SORTIE_LOOP_ONLY=plan,build` starts only those loops; unset runs them
all. This is how `sortie-central` picks a per-repo workflow set.

## Layout in the target repo

```
.sortie/
  config.yaml      # repo:, token:, milestone:, cleanup:, sleep:, workflows: (all optional — see below)
  workflows/       # installed by setup from the binary; edit freely, loop runs these
  .env.loop        # generated each run (resolved tracker, token, clone URL; mode 0600)
  workspaces/      # per-item agent checkouts (one subdir per loop)
  logs/            # engine child output, one <workflow>.log per loop
```

`setup` ignores `.sortie/` wholesale in `.gitignore` — run state
(env, workspaces, db files, local config) stays untracked. To share
loop config in git instead (keep `token: ""` and use env for secrets),
replace `.sortie/` with:

```gitignore
.sortie/*
!.sortie/config.yaml
!.sortie/workflows/
.sortie-*.db
```

`.sortie/config.yaml`:

```yaml
repo: ""        # empty = detect from git remote; or pin owner/name
token: ""       # empty = SORTIE_LOOP_TOKEN, then GITHUB_TOKEN / GH_TOKEN env
milestone: ""   # optional milestone title to restrict all loops to
assignee: "@me" # token owner only; "" = shared backlog
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

### Workspace cleanup

`cleanup:` in `.sortie/config.yaml` runs a command that removes old
workspaces:

```yaml
cleanup:
  enabled: false    # default
  interval: 6h      # required when enabled
  command: ./scripts/cleanup.sh   # required when enabled
  max_age_days: 14  # required when enabled
  dry_run: false
  timeout: 10m      # default; kills the command and its process group
```

When `enabled` is true, the supervisor runs `command` through `sh -c`
every `interval`, starting one interval after startup, in the repo root.
`sortie-loop` refuses to start when `command`, `interval` or
`max_age_days` is missing, or when `max_age_days` is not less than
`workspace.retention_days` in a workflow file that sets it.

The command gets these environment variables and no arguments:

- `SORTIE_CLEANUP_DRY_RUN`: `1` or `0`. With `1` the command removes
  nothing and only reports what it would remove.
- `SORTIE_CLEANUP_MAX_AGE_DAYS`: `cleanup.max_age_days`.
- `SORTIE_CLEANUP_WORKSPACE_ROOT`: `.sortie/workspaces` in the repo root.
- `SORTIE_CLEANUP_DB_DIR`: `.sortie/workflows`, which holds the workflow
  SQLite DBs.

Exit code 0 is success and anything else is a failed run. A failed run
is logged and does not stop the supervisor. The command may print
`removed: <path>` lines on stdout. Up to 200 of them are recorded as the
paths of the run. The last 4 KB of stdout and stderr are logged. Two
cleanups never run at the same time: a run that finds another one active
is skipped.

`sortie-loop cleanup` runs the command once with the same contract.
`--dry-run` sets `SORTIE_CLEANUP_DRY_RUN=1`. `--json` prints
`started_at`, `duration_ms`, `exit_code`, `dry_run`, `command`,
`output_tail` and `error`. It exits non-zero when the command fails or
none is configured.

Every run, scheduled or manual, writes its result to
`<hash>.cleanup.json` next to the status document.

### Idle sleep

`sleep:` in `.sortie/config.yaml` stops the engine children when there is
no work and starts them again later:

```yaml
sleep:
  enabled: false       # default
  idle_timeout: 15m    # required when enabled
  wake_interval: 1h    # required when enabled
```

Both values are Go durations and must be positive when `enabled` is true;
`sortie-loop` refuses to start otherwise. With `enabled: false` the loops
run all the time.

Every 30 seconds the supervisor asks each child's `GET /api/v1/state` on
`127.0.0.1` for its running and retrying work. A child with running or
retrying work is busy. A child that cannot be queried (connection error,
timeout, bad response) is also busy, so the supervisor never sleeps on an
error. When no child has been busy for `idle_timeout`, the supervisor stops
all children and keeps running. The idle time starts again at every busy
observation and whenever the children are started, so `idle_timeout`
should be larger than the polling interval of the workflows: a freshly
started child needs that long to find work. A run started with
`--no-server` has no state endpoint and never sleeps.

The children start again `wake_interval` after sleep began, or when
`sortie-loop wake` is run. A wake starts them the same way as startup,
with the config and workflow files now on disk. `sortie-loop wake` writes a
request file next to the status document, which the supervisor reads at
its next 2-second tick.

While the run sleeps, new issues and PRs wait until the next wake. Turn
sleep off if agents must start immediately. The supervisor, the cleanup
schedule and the status document keep running. `stop` ends the run.
`restart --when-idle` wakes the loops, and a restart without
`--when-idle` replaces the process as usual.

### Spend controls

`workflows:` in `.sortie/config.yaml` caps the tokens one workflow may use
per period. The key is the loop name (the `WORKFLOW.<name>.md` stem); a name
without a workflow file is an error at startup.

```yaml
workflows:
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

Env overrides: `SORTIE_LOOP_REPO`, `SORTIE_LOOP_TOKEN`,
`SORTIE_LOOP_MILESTONE` (plus `GH_MILESTONE` as a legacy alias),
`SORTIE_LOOP_ASSIGNEE` (overrides `assignee:`).

## Ad hoc: auto-merge on clean review

To fully automate build → review → merge on one repo (not a template
default), edit that repo's `.sortie/workflows/prompts/review.md` Step 4
clean branch to chain the merge loop instead of stopping:

```
gh pr edit {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT \
  --remove-label "agent:review,in-progress" --add-label "agent:merge"
```

The merge loop watches `label:agent:merge`, so it picks the PR up on the
next poll (~60s), triages comments, merges, and ends on `agent:done`.
Blocking findings and conflicts still stop the merge and escalate via
`needs-human` — automation holds everywhere except where judgment is
required.

## Agent identity (name/avatar on GitHub)

The commit author name/email is what GitHub displays. Point the loop's git
identity at the agent — in the target repo or globally:

```sh
git config --global user.name "sortie-agent"
git config --global user.email "agent@example.com"
```

`user.name` is display-only. GitHub links the commit (and its avatar) to the
GitHub account whose **verified email** matches the author email. One account
= one avatar, so every agent using your emails shows your personal avatar.
Options for a distinct face: a separate bot account per agent (own email +
avatar, verified there); or no GitHub account at all plus a per-email avatar
on gravatar.com (GitHub falls back to the Gravatar for unclaimed emails);
or publishing the agent as a GitHub App (`name[bot]`, its own avatar).

## Building

```sh
go build ./... && go vet ./... && go test ./...
```

Go 1.26.0 or newer. The floor comes from the dependencies:
`golang.org/x/sys` requires 1.26.0 and `modernc.org/sqlite` requires
1.25.0. `go install` needs the same version, so use a release binary if
yours is older.

## Known limitations

- **One crashed loop stops the run.** A loop process exiting ends
  `sortie-loop`, as a signal does. The remaining loops are stopped
  cleanly and the registry entry is dropped, but nothing restarts them.
- **Workflow files are not version-stamped.** A workflow file you have
  not edited is not updated when the binary is upgraded. Delete it and
  re-run `setup` to take the shipped version.
- **Windows is unsupported.** The engine releases a `.zip`; the
  installer reads `tar.gz`.

## Customizing agents and workflows

The shipped default is the `pi` agent. Every `WORKFLOW.*.md` also
documents a commented-out `claude-code` alternative (`sonnet`) — swap
the `agent:` block for the commented one, keeping the same `max_turns`
and timeouts. The `model:` value
passes through to `claude --model` unvalidated, so only use aliases
your installed CLI accepts. The pinned engine only knows `pi` and
`claude-code` (plus a few others); other adapters need a newer or
upstream engine binary. The dashboard's token/model columns populate
for either agent.

Sortie upstream references:

- [Claude Code adapter](https://docs.sortie-ai.com/reference/adapter-claude-code/) —
  `agent:` fields and the `claude-code:` extension block
  (`model`, `fallback_model`, `permission_mode`, costs)
- [WORKFLOW.md configuration reference](https://docs.sortie-ai.com/reference/workflow-config/) —
  full front-matter schema
- [Configure dispatch rules](https://docs.sortie-ai.com/guides/configure-dispatch-rules/) —
  routing plus the `dispatch.agent.missing_block` rule (every kind a
  rule names needs its own settings block)
- [Write a prompt template](https://docs.sortie-ai.com/guides/write-prompt-template/) —
  template variables and helpers for `prompts/*.md`
- [Control agent costs](https://docs.sortie-ai.com/guides/control-costs/) —
  turn caps, session caps, concurrency limits

## Roadmap

- Re-sync the engine fork periodically. `kinged007/sortie` is a superset
  of `sortie-ai/sortie` today — 18 commits ahead, 0 behind — but nothing
  merges upstream into it, so the two will drift.
- Restart a loop that exits instead of ending the run. Currently one
  crashed loop stops every other loop; see
  [Known limitations](#known-limitations).
- Stamp workflow files with the binary version that wrote them, so an
  upgrade can offer a new version of a file the user has not edited.
