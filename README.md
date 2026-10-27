# Jigz

[![CI](https://github.com/kinged007/jigz/actions/workflows/ci.yml/badge.svg)](https://github.com/kinged007/jigz/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/kinged007/jigz)](https://github.com/kinged007/jigz/releases) [![Go](https://img.shields.io/badge/Go-1.26-blue)](go.mod)

Run autonomous coding agents against your GitHub issues with one command.

`jig` watches a repo's issues and pull requests, and for each
labeled item spins up an AI coding agent that reads the issue, writes the
code, and moves the work forward — planning, implementing, reviewing, and
merging — mostly on its own. You manage the backlog by adding labels and
assigning issues; the agents do the rest. GitHub is the only supported
tracker.

## What is what

**Jigz** (this repo) is a small Go wrapper. It does setup (config,
labels, workflow files) and then launches one loop process per workflow,
plus a web dashboard. It contains no agent itself.

**The engine** does the heavy lifting inside each loop: it polls the
tracker on a timer, claims a matching item, clones the repo into a scratch
workspace, hands the item to an agent, and records the run in a local
SQLite database. `claude-code` is a standard agent kind that every
supported engine registers, so the shipped workflows run without extra
setup. The one feature the workflows need that an engine may lack is the
`github-pr` tracker (which treats pull requests as work items). The `pi`
agent adapter is optional: an engine only needs it when a workflow or
pool entry uses `pi`.

**The agent** is Claude Code (`claude`), a coding agent that runs in a terminal the same
way you would: it reads files, edits code, runs checks, and shells out
to the `gh` CLI to read comments, open PRs, and move labels. The `pi`
agent is supported too; see
[Customizing agents and workflows](#customizing-agents-and-workflows).

## Names

| Name | What it is | Where it appears |
| --- | --- | --- |
| `Jigz` | The project and brand. | Repository `kinged007/jigz`, Go module `github.com/kinged007/jigz`. |
| `jig` | The command. Each loop is one `jig <root>` process per workflow. | Shell, `jig` subcommands, `JIG_*` environment variables. |
| `jigz` | The name of data on disk and in the repository. | `.jigz/`, `~/.config/jigz/hub.json`, per-user state, data and cache directories `jigz`, `jigz-<name>.db`, `.gitignore` entries. |
| `jigd` | The background service that runs `jig daemon`; also the name under which the `jig` binary acts as `jig daemon`. | `jigd.service` (systemd), `io.github.kinged007.jigd` (launchd), `jigd.log`. |
| The engine | The external binary that polls, claims items and runs agents. It is pinned and not vendored. | `JIG_ENGINE_*` variables, [The engine](#the-engine). |
| The dashboard | The web page that `jig` serves for a repo. `--no-dashboard` turns it off. | [Dashboard](#dashboard). |
| The hub | A separate product that jig connects to. | [Connected mode](#connected-mode), `plans/system/hub-protocol.md`. |

Environment variables start with `JIG_`. Each is documented next to the feature it configures.

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
`.jigz/workspaces/<loop>/`, checks out a branch named
`auto/<issue-number>`, and renders a prompt template
(`.jigz/workflows/prompts/*.md`) with the issue title, body, labels,
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
| `needs-human` | red | Escalation: parks the item until a person removes the label |
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

Questions carry no label of their own: there is no `agent:question` or
`needs-answer` label. An agent asks with an HTML comment block in an
issue comment, and a blocking question parks the item by adding
`needs-human` (see [Questions](#questions)).

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
the PM prompt in `.jigz/pm-agent.md`, to label it.

The chained label must land on the item the next loop watches: the `prs`
loop watches PRs, so label the PR, not the issue it came from. `.jigz/workflows/prompts/review.md` Step 4 and
`review-fix.md` Step 5 carry these forms.

## The loops

Two workflows ship by default (each is one `WORKFLOW.*.md` file, each
runs as its own loop process). Each file has one dispatch rule per
trigger label:

| Loop | Rule | Watches | Does |
|------|------|---------|------|
| `issues` | `plan` | issues with `agent:plan` | Writes an implementation plan as an issue comment. The agent removes `agent:plan` and adds `agent:done`; a human reviews the plan and labels the issue `agent:build` to trigger development. |
| `issues` | `build` | issues with `agent:build` or `agent:plan-build` | Reads the issue's recent comments (the plan comment and later human comments), implements the change and opens a PR, then labels that PR `agent:review` and assigns it to the token owner. For `agent:plan-build` with no plan comment, it posts the plan first and implements in the same run. |
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
exists in `.jigz/workflows/`, `WORKFLOW.issues.md` and `WORKFLOW.prs.md`
are not written, because both sets would process the same issues. A
warning on stderr says so. To migrate:

1. Delete the five old files, and their `.sortie-<name>.db` files if you
   do not need the run history.
2. Rerun `jig setup` to install the two new files.
3. In `.jigz/config.yaml`, rename old loop names to `issues` or `prs` in
   the `filters:` and `workflows:` (budget) keys.
4. Existing installs keep their old `prompts/plan.md` and `prompts/build.md`,
   because setup never overwrites a file. Diff them against the shipped
   copies or delete them and rerun `setup`. An old `build.md` that does not
   remove `agent:plan-build` in its finishing step leaves that label on the
   issue, so the issue is dispatched again. `setup` prints a note when
   either file differs from the shipped copy.
5. Add the `agent:plan-build` and `agent:docs` entries to the `labels:` list of an
   existing `.jigz/config.yaml` (copy them from the list above), then
   rerun `setup`. `setup` only creates the labels in that list and prints a
   note for each shipped label that is missing from it.

The loop set is just every `WORKFLOW.*.md` in `.jigz/workflows/` —
drop in a new file (e.g. `WORKFLOW.triage.md`) and it starts as a loop
named by its stem on the next run. Per-loop label queries can be
narrowed via the `filters:` map in `.jigz/config.yaml` without
re-running setup.

`JIG_ONLY=issues` narrows the run to the named loops, for a
supervisor that drives several repos and wants a different set per
repo. Unset (the default) runs every installed loop. A name that
matches no installed workflow is fatal rather than silently dropped, so
a typo cannot leave that loop's work unwatched.

## Dashboard

Every run serves a dashboard (default `http://127.0.0.1:7677`, bound to
loopback). The dashboard is read-only: it shows state and history, and nothing on it starts, stops,
restarts, edits or changes anything. Loops are controlled with the `jig`
commands (`jig stop`, `jig restart`, `jig reload`, `jig wake`,
`jig cleanup`, `jig pause`, `jig resume`, `jig budget override`), and
projects are set up with `jig setup`.

It reads the shared registry and each repository's status document plus
run data from its loops, so the numbers survive restarts and a repository
that is not running still shows its last known state (see
[Run data](#run-data)). Every
3 seconds the changing parts are re-fetched as fragments, so the page
needs no build step and works as static HTML without scripting.

Pages: Operations (fleet, alerts, token charts, per-repository and
per-loop rows), Runs and Usage from the history database, Workflows (the
pipeline each repository's files describe, plus validation), Projects,
Agents (the settings each workflow file declares), Settings, Backups, and
each loop's child log.

### Run data

The dashboard reads each loop's SQLite database read-only: finished runs,
the all-time run counts, and the parked, retrying and budget-held issues.
Each pass copies the runs the history database does not have yet, matched
by the engine's run id. When two workflows share one engine database, only
the runs of each workflow's own file are copied to it. The history of a
stopped repository stays visible.

A running session is listed from the supervisor's reading of the loop's log,
because the database records a run only when it ends. Its tokens and model
appear after its first completed turn, when the database has them. The
dashboard shows no cost for a running session.

The database reader accepts the engine schema versions 18 to 43 (the
engine releases 1.28.1 and 1.29.0 write 38 and 43) when the tables and
columns it reads exist. A database written by a newer engine than the
newest known one is reported as a schema notice and not read.

A run shows the fields the engine reports for it: status, error, attempts,
turns, tokens, agent adapter, model, session id and workflow file. The
engine reports no workspace path, prompt template, API request count,
handoff counter reset or review metadata, so the dashboard does not show
them. If the engine database was replaced by a new one, whose newest run id
is lower than the last copied one, the stored runs of that workflow are
removed and copied again from the database.

Running loops in several repos at once? Pass `--unite`:

```sh
jig --unite    # in each repo
```

The first `--unite` run starts the dashboard; later ones detect it and
join instead of starting a second page. Without `--unite` the page shows
only its own repository; with it, every registered repository appears
with whatever process is supervising it.

A repository that stopped keeps its row with its last known state.
The dashboard answers only `GET` requests; any other method is refused.

### The run page

The page of a run (stored or running) shows the run's status, the attempts
of its issue, events, tokens, delivery, the session and the logs. The page of a
running agent is addressed as `/runs/<owner>/<repo>/<workflow>/live-<issue number>`
and fetches new data every 4 seconds while the run is running, from
`GET` routes under the same address. It stops fetching when the run is
finished and then shows the stored run.

While a run is in progress the page reads these sources on the machine that
runs the loop:

- Tokens and model: the engine database, from the end of the first turn of the
  session.
- Delivery: the files the agent writes in its workspace
  (branch, pull request number, status). The stored pull requests of the issue
  are shown when GitHub details are on.
- Session: the session id, the agent kind, the workspace directory and a
  command that reopens the session (`cd '<workspace>' && claude -r <id>` for
  `claude-code`, `cd '<workspace>' && pi --session <id>` for `pi`) with a
  Copy button. The command works only on the machine that has the workspace.
  Other agent kinds show the session id only.
- Agent log: the agent's own transcript, which the agent writes under
  `~/.claude/projects` (`claude-code`) or `~/.pi/agent/sessions` (`pi`)
  while it runs. Only files in the transcript directory of the run's
  workspace are read, symlinks that leave that directory are not followed,
  and the page reads the last 1 MiB of the file. Each entry is shown as a
  message, a thinking block (collapsed), a tool call, a tool result or an
  error, with text cut at 4000 bytes.
- Engine log: the lines of the workflow log that name the issue.

After the run the engine database has the run's event record, which the Agent
log shows in place of the transcript. A run with no event record falls back to
the transcript when it is still on this machine. A workspace that was cleaned
up shows no delivery files. A project that is not on this machine shows the
stored fields only.

The Agent log, the engine log and the session details can hold text the
agent read or wrote and paths of the machine. They are not redacted. Hide
them with the `agent_log`, `log_tail` and `session` sections
([Hiding pages and sections](#hiding-pages-and-sections)).

### Access

The dashboard has no authentication. It binds `127.0.0.1` by default, so
only processes on the same machine can reach it. `--host 0.0.0.0` (or
`--host ::`) listens on all interfaces, and `--host ADDR` binds one
address. A non-loopback bind prints a notice on stderr at startup.

Anyone who can reach the dashboard can view it. It shows project names,
run history, token use and log tails, and has no capability to change
anything. Use it locally, on a private network, or behind your own secure
proxy, for example an SSH tunnel, Tailscale, or a reverse proxy that
requires authentication.

The dashboard refuses a request whose `Host` header is not one it serves
and a non-GET request from another origin. Naming one interface
with `--host` limits the accepted `Host` values to that address and the
loopback names; a wildcard bind accepts any `Host`.

### Hiding pages and sections

The dashboard has no authentication, so hiding content is its only built-in
protection when it is reachable beyond localhost. The `dashboard.hide` block
of `machine.yaml` (see [Working hours and agent accounts](#working-hours-and-agent-accounts))
lists the pages and sections to hide:

```yaml
dashboard:
  hide:
    pages: [questions, settings, backups, logs]
    sections: [audit_log, log_tail, agent_log, session]
```

A hidden page is removed from the sidebar and from every link, and every
route that serves it answers `404`, including its JSON and CSV forms. A
hidden section is removed from the page that shows it, and a route that
serves only that section answers `404`. A `404` for a hidden page is the same
answer as for a path that does not exist. Nothing is hidden by default.
`jig` commands are not affected.

The setting is per machine because one dashboard can show several
projects (`--unite`) and has no per-project configuration. The dashboard
rereads `machine.yaml` when the file changes, so an edit applies without a
restart. If the `dashboard` block cannot be read after an edit, the last valid
setting stays in force. If it cannot be read when the dashboard starts, every
page answers `404`. An unknown page or section name, or a misspelled key, is
an error that lists the valid names.

Pages:

| Name | Hides |
|---|---|
| `operations` | Operations, its live fragment, `GET /api/v1/fleet`, `GET /api/v1/metrics` |
| `runs` | Runs, the CSV export, run detail pages, `GET /api/v1/runs` |
| `questions` | Questions, `GET /api/v1/questions` |
| `usage` | Usage, `GET /api/v1/usage` |
| `workflows` | Workflows |
| `projects` | Projects, project detail pages, `GET /api/v1/projects` |
| `agents` | Agents |
| `settings` | Settings |
| `backups` | Backups, and the backups list on project pages |
| `logs` | The child log page of a workflow |

Sections:

| Name | Hides |
|---|---|
| `agent_log` | The agent log tab, the agent steps in the events list, and the endpoint behind them, on the run detail page |
| `session` | The session id, workspace directory and resume command on the run detail page |
| `log_tail` | The engine log tab, its milestones in the events list, and the endpoint behind them, on the run detail page |
| `audit_log` | The audit log on Operations and project pages, `GET /api/v1/audit` |
| `effective_config` | The effective configuration table on project pages |

The header load figures (`/status`) are always served. Hiding `projects`
does not remove the project names that Operations, Runs and the fleet API
show; hide those pages too when project names are sensitive.

## Install

macOS and Linux, on Intel or Apple silicon.

```sh
curl -sSL https://raw.githubusercontent.com/kinged007/jigz/main/install.sh | sh
```

That downloads a release binary to `~/.local/bin/jig` and checks
it against the release's `checksums.txt`. No Go toolchain needed.
`./install.sh --prefix DIR` installs to `DIR/bin/jig` instead,
`--version TAG` pins a release, and `--from-source` builds from a
checkout.

Already have Go?

```sh
go install github.com/kinged007/jigz/cmd/jig@latest
```

Also requires the **gh CLI** — `setup` shells out to it, and the agent
prompts use it throughout.

### The engine

`jig` is the supervisor. The work is done by the **engine**, a separate
program that polls GitHub, claims work, and runs the agents. `jig setup`
downloads the pinned engine release, verifies it against that release's
`checksums.txt`, and links it onto your `PATH`, so the engine's own
commands (`validate`, `stats`) work in any shell. A fresh install needs no
separate engine step.

The engine is a GitHub release, chosen by a repository (`owner/name`) and a
version tag (`vX.Y.Z`), defaulting to `v1.28.1`, the release the shipped
workflows were written against. `jig --version` prints the tool version
and the engine release it resolves to, and the dashboard Settings page
shows that release next to the version the engine binary reports.

The workflows need one feature an engine may not ship: the **github-pr
tracker**, which treats pull requests as work items so a review loop can
watch a pull request rather than an issue. Every supported engine
registers the **claude-code** agent adapter, and the `pi` adapter is only
needed when a workflow or pool entry uses `pi`. A repository you configure
should carry the tracker; the next section says what is switched off when
it does not.

#### Engine capabilities

At start, `jig` asks the resolved engine binary for its version (its
version flag) and for the tracker and agent kinds it has registered. No
engine command lists the kinds, so the second part runs the engine's
`validate` command on a temporary workflow with an unknown tracker and agent
kind and reads the `registered: [...]` lists from its errors. The result is
cached per binary path, modification time and size for the life of the
process. The supervisor, `validate`, setup validation, the dashboard and the
status document use the same result. A binary that cannot be probed has an
unknown capability set: nothing is disabled, and the dashboard says that the
features could not be detected.

| The engine | What happens |
|---|---|
| Is older than 1.27.0, the oldest the shipped workflows run on | No loop starts. `validate` fails with the reason, and the dashboard shows an alert. |
| Has no `github-pr` tracker | The PR workflow loop does not start. `validate` reports it as disabled, not as an error. The workflow file stays installed. |
| Lacks the agent kind a workflow names (`claude-code` unless you set `agent.kind`) | The loop does not start. `validate` and the dashboard show an error. There is no fallback agent. Pool entries with a runtime the engine lacks are shown as unavailable and are not picked. |

Each disabled feature has one alert on the Operations page with the reason,
for example "PR workflow is off: engine v1.29.0 has no
github-pr tracker." The Settings page lists the registered tracker and agent
kinds, and a workflow that is not started has the state `disabled` and the
reason in the status document. The `engine_version` field of the status
document is the version the engine binary reports.

The repository and version are read in this order, first match wins:

1. `JIG_ENGINE_BIN`: use this binary. No repository or version applies.
2. The environment variables `JIG_ENGINE_REPO` and `JIG_ENGINE_VERSION`.
3. The `engine:` block of `machine.yaml` (see
   [Working hours and agent accounts](#working-hours-and-agent-accounts)).
4. The defaults.

Each of the repository and the version is resolved on its own. A release
needs one archive per platform (Linux and macOS, `amd64` and `arm64`),
named after the version, the OS and the architecture, and a
`checksums.txt`; the download is verified against it. An invalid value is
an error.

The setup command, the loops, `validate`, the dashboard and the supervisor
all resolve the engine the same way: `JIG_ENGINE_BIN`, then the engine installed
for the configured repository and version in `$XDG_DATA_HOME/jigz` (or
`~/.local/share/jigz`) or in `<prefix>/share/jigz` beside the
`jig` binary, then a verified download into the first of those. The
installed file is named after the repository and the version, so the same
version from two repositories is stored separately. If the download fails,
an engine binary beside the `jig` binary or on `PATH` is used with a
warning, and its version is not checked.

The version is fixed rather than resolved to "latest" on purpose: the
workflows are written against specific engine behaviour, and an engine that
changes under them breaks loops in ways that are hard to trace.

| Variable | Effect |
|---|---|
| `JIG_ENGINE_BIN` | use this engine binary |
| `JIG_ENGINE_REPO` | fetch the engine from this repository's releases (`owner/name`) |
| `JIG_ENGINE_VERSION` | fetch this version (`vX.Y.Z`) instead of the configured one |
| `JIG_ENGINE_URL` | fetch one archive from this URL (with `JIG_ENGINE_SHA256` to verify it) |

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
2. Expose it with one of (first set wins: `JIG_TOKEN`, then
   `token:` in `.jigz/config.yaml`, then `GITHUB_TOKEN`, then
   `GH_TOKEN`):
   ```sh
   export GITHUB_TOKEN="ghp_..."     # or GH_TOKEN; or JIG_TOKEN to override just the loop
   ```
   The `gh` CLI reads `GITHUB_TOKEN`/`GH_TOKEN` itself, so exporting
   one of those covers both the loop and the agent's `gh` calls. Never
   commit the token — `token:` in config is for local-only setups.
3. Verify: `gh auth status` should show the right account before you
   run `setup` (which syncs labels) or the loop.

## Use (inside any repo)

```sh
cd your-repo
jig setup    # writes .jigz/config.yaml, updates .gitignore, creates the labels,
             # installs the engine, and installs .jigz/pm-agent.md
             # (a project-manager prompt)
jig          # run every WORKFLOW.*.md loop in the foreground (Ctrl-C stops all)
jig start    # the same run in the background
```

`setup` detects `owner/name` from the git `origin` remote
(`--repo=owner/name` overrides). The loop re-detects it at every run, so
moving the checkout or forking needs no reconfiguration. A positional
argument selects the repository root and may sit anywhere on the line,
so `jig setup ./repo --repo owner/name` and
`jig setup --repo owner/name ./repo` do the same thing.

`jig start [options] [repo-root]` runs the same fleet in the background. It
starts this binary in its own session with the bare form and the same
options, appends its output to `<state>/jigz/<owner>-<name>.log` (the
repo directory name when the repo id has no owner), and waits up to 15 seconds
for the run to appear in the registry. It then prints the pid, the dashboard
address (unless `--no-dashboard`), the log path and the `jig status` and
`jig stop` commands. When a run already serves the repo-root it prints that
and exits 0 without starting another. When the run exits or does not register
in time, it prints the last log lines and exits non-zero. Stop the run with
`jig stop`.

`jig --help` lists every command in groups: Run, Control (`stop`, `restart`,
`status`, `wake`, `pause`, `resume`, `budget override`, `reload`, `cleanup`,
`validate`), Setup (`setup`, `join`, `leave`, `daemon`) and the options.

`setup` confirms before changing a label whose colour or description
differs from the shipped one. It never changes an existing workflow file.

`jig status [--json] [repo-root]` reports the run serving the
repo-root: version, pid, state, and per workflow its state, pid,
restart count and last exit code. `--json` prints the status document
with an added `liveness` field: `running` when the pid is alive, `stale`
when the document says running but the pid is dead, `exited` when the
run ended. The command reads the document from the repo-root and does not
need a registry entry. It exits non-zero when no document is found.

After the run's own state, `jig status` lists the other live runs in the
shared registry: repo, repo-root, pid and uptime. Entries of runs that
exited or whose pid is dead are not listed. With `--unite`, the run that
serves the shared dashboard is marked, and the dashboard address and
that run are printed. When the repo-root has no run, the list is still
printed before the error. The `--json` document has an `instances` array
with the same runs (`repo_name`, `root`, `pid`, `started_at`,
`serves_dashboard`, and `dashboard` when known).

Each run records the dashboard address it serves or joined in its registry
entry (`dashboard`, and `serves_dashboard` for the run that serves it).
`jig stop` and `jig restart` act only on the run for the repo-root. They
list the other live runs afterwards. When the stopped run serves the
dashboard, they first print a warning: the dashboard stops with that
run, and the other runs keep running without it. A later `--unite` run
starts a new dashboard. Repo-roots are keyed by their absolute path with
symlinks resolved, so a run started through a link is found from the real
path.

The supervisor writes the status document (schema version 1) to
`<user cache dir>/jigz/status/<hash>.json`, where `<hash>` is the
first 16 hex digits of the SHA-256 of the absolute repo-root. The registry
entry holds the same path as `status_path`. A clean exit removes the
document. A run that ends on a loop failure leaves it with state
`exited`, and the next supervisor for the same root overwrites it. In a workflow entry,
`restart_count` counts recycles and wakes only, because a child that exits without
being asked to ends the run. `last_exit_code` is `-1` when the child was
killed by a signal. `log_path` is the absolute path of the workflow's log
file.

A workflow entry has `sessions`, the agent sessions running now, each with
`issue_id`, `identifier`, `attempt`, `started_at` and `turns`. `sessions_known`
is false while the supervisor cannot account for every session of the child,
for example before the child has logged its first poll.

The status document has a top-level `cleanup` object once a cleanup has
run: `last_run_at`, `duration_ms`, `exit_code`, `dry_run` and `paths`.
`jig status` prints it as one line.

The status document has a top-level `sleep` object: `enabled`, `awake`,
`sleeping_since` and `next_wake` (RFC 3339 or `null`), and, when sleep is
enabled, `idle_since` (RFC 3339, or `null` after a busy observation). While
the run sleeps, the workflow entries have state `sleeping`.
`jig status` prints one `sleep:` line when sleep is enabled.

A workflow entry has a `budget` object when the workflow has a budget or is
paused: `period`, `period_start`, `tokens_used`, `cap`, `warn_percent`,
`unmeasured_runs`, `paused`, `paused_by` (`budget`, `manual` or empty),
`override` and `warned`. A paused workflow has state `paused`, which takes
precedence over `sleeping`.

### Child logs

The stdout and stderr of each engine child are written to
`<repo-root>/.jigz/logs/<workflow>.log`, interleaved in arrival order and
unchanged. They are still printed to the terminal of `jig`. The file
is opened for appending, so it keeps its content across child recycles and
supervisor restarts. When a write would take the file over 10 MiB, it is
renamed to `<workflow>.log.1`, `.1` becomes `.2`, `.2` becomes `.3`, and the
old `.3` is deleted. A file that is already over 10 MiB when a child starts
is rotated first. The limit and the 3 rotated files are fixed in the code.
If the log file cannot be opened or written, one warning is printed and the
child keeps running. The per-run agent transcript is not part of these logs.

`jig cleanup [--dry-run] [--json] [repo-root]` runs the configured
cleanup command once (see [Workspace cleanup](#workspace-cleanup)).

`jig wake [--json] [repo-root]` starts the engine loops of a run
that is sleeping (see [Idle sleep](#idle-sleep)). `--json` prints `woke`,
`was_sleeping` and `error`. When the run is awake or sleep is disabled it
does nothing, prints `was_sleeping: false` and exits 0. It exits non-zero
when no supervisor is running for the repo-root, or when the supervisor is
not awake 5 seconds after the request.

`jig pause <workflow> [--json] [repo-root]` stops the engine loop of
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
`--no-dashboard` disables the dashboard page. A positional arg selects the
repo root (default: current directory).

`JIG_ONLY=plan,build` starts only those loops; unset runs them
all. It applies to `jig` and `jig start`. This is how Jigz picks a per-repo workflow set.

## Layout in the target repo

```
.jigz/
  config.yaml      # repo:, token:, milestone:, cleanup:, sleep:, workflows: (all optional — see below)
  workflows/       # installed by setup from the binary; edit freely, loop runs these
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

`.jigz/config.yaml`. An unknown key at any level is an error that names the
file, the line and the key:

```yaml
repo: ""        # empty = detect from git remote; or pin owner/name
token: ""       # empty = JIG_TOKEN, then GITHUB_TOKEN / GH_TOKEN env
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

`cleanup:` in `.jigz/config.yaml` runs a command that removes old
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
**Question:** Should this ship in the first release?
**Suggested answer:** No; split the indexer into its own issue.
<!-- /agent-question -->
```

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

A person answers in one of three ways:

| Path | What the person posts | How the item resumes |
|---|---|---|
| Hub | The answer-block template, posted by the hub with the user's own GitHub token | The scanner (below) flips the labels automatically |
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
`github.questions` on and a token) and in `jig daemon` (the projects of its
loops). It resumes an item when all of these hold:

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

`workflows:` in `.jigz/config.yaml` caps the tokens one workflow may use
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

Env overrides: `JIG_REPO`, `JIG_TOKEN`,
`JIG_MILESTONE` (plus `GH_MILESTONE` as a legacy alias),
`JIG_ASSIGNEE` (overrides `assignee:`).

### Other environment variables

| Variable | Effect |
|---|---|
| `JIG_WORKSPACES` | Directory that holds the agent checkouts. `jig` sets it for each loop (`.jigz/workspaces` by default) and the shipped workflows use it as `workspace.root: $JIG_WORKSPACES/<loop>`. Set it by hand only when validating a workflow file with the engine directly. |
| `JIG_BIN` | Path of the `jig` binary that the supervisor starts for each loop. Without it, `jig` uses a `jig` binary next to the running one, then `jig` on `PATH`. |
| `JIG_REGISTRY_DIR` | Directory for the project registry and status files. Unset, they are kept in the shared user cache directory, so several `jig` processes on one machine see each other. `jig daemon` sets it for its loop children so their files stay apart from the shared ones. |
| `JIG_MODEL` | Model name the agent adapter receives. `jig` sets it from the `model` agent setting of a repository in `jigz.yaml`; a workflow file refers to it as `model: $JIG_MODEL`. |
| `JIG_MODEL_<WORKFLOW>` | Model for one workflow when `jigz.yaml` overrides `model` for that workflow, for example `JIG_MODEL_BUILD` for `WORKFLOW.build.md` and `JIG_MODEL_REVIEW` for `WORKFLOW.review.md`. `jig` sets these; `-` in a workflow name becomes `_`. |

### Working hours and agent accounts

The optional file `<user config dir>/jigz/machine.yaml` (Linux:
`$XDG_CONFIG_HOME/jigz/machine.yaml` or `~/.config/jigz/machine.yaml`)
applies to the loops of every repository on the machine. Without it, loops run
without an hours limit and without a pool. The Agents page of the dashboard
shows the hours and the pool.

```yaml
hours:
  timezone: Europe/Madrid     # IANA name; empty = local time
  end: finish                 # finish | stop; default finish
  windows:
    - days: [mon, tue, wed, thu, fri]
      start: "09:00"
      end: "18:00"            # an end earlier than start wraps past midnight
pool:
  - name: claude-work
    runtime: claude-code      # claude-code | pi
    command: claude           # default "claude" or "pi" by runtime
    env:
      CLAUDE_CONFIG_DIR: ~/.claude-work
    hours:                    # optional; windows only, same shape as above
      windows: []
    cost_class: subscription  # free-text label shown in the dashboard
  - name: pi-api
    runtime: pi
    env:
      ANTHROPIC_API_KEY: ...
limits:
  default_reset: 1h           # used when the CLI message has no reset time
  patterns: []                # extra case-insensitive regexes
engine:
  repo: ""               # owner/name of the engine's GitHub repository; empty = the default
  version: v1.28.1       # release tag, vX.Y.Z; empty = the default
dashboard:
  hide:
    pages: []            # pages to hide; see Hiding pages and sections
    sections: []         # page sections to hide
```

The `engine` block selects the engine release and is optional; an empty
field uses the default. `JIG_ENGINE_REPO` and `JIG_ENGINE_VERSION`
override it (see [The engine](#the-engine)). A problem in the hours or pool
settings does not stop the engine from being resolved.

Without windows the hours are always open. `env` values expand a leading `~/`
and `$VAR` or `${VAR}`. The file can hold API keys, so keep it readable by you
only (`chmod 600`); loading warns when it is not.

`end: finish` makes loops claim nothing new when the window closes and stops
each loop once it has no running session. `end: stop` stops the loops when the
window closes; the engine aborts the turn, the issue keeps its in-progress
label and is dispatched again when the next window opens.

With a pool, the supervisor starts each loop with the agent command set to
`jig agent-exec <runtime>`. `agent-exec` runs the CLI of the first
available entry of that runtime (not exhausted and inside its own hours), passes
stdout through unchanged and exits with the CLI's exit code. When the CLI output
reports a usage limit, the entry is marked exhausted until the reset time in
the message, or for `limits.default_reset`. The state is stored in
`<state dir>/jigz/pool.json`. Without an available entry, `agent-exec`
exits 75. When the active runtime changes, loops are restarted once they have
no running session.

A usage limit is usually detected by `agent-exec` during the engine's credential
check, which runs before each worker run. The run then fails before the turn
starts, the engine posts no failure comment, and it retries after about 10
seconds with the next available entry. A limit reached in the middle of a turn
fails that engine session: the engine posts a session-failure comment on
the issue and retries the same way.

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

## Connected mode

A machine can be enrolled with a hub. The hub supplies the project
config and sends commands. The machine runs the loops.

```sh
jig join https://hub.example.com <token>
```

Flags:

- `--name <name>`: the runner name sent to the hub. Default: the host
  name, cut to 40 characters.
- `--no-service`: enroll without installing the service. Run `jig daemon`
  yourself.
- `--yes`: replace an existing enrollment without asking.

A token of `-` is read from stdin, so it is not in the process list. The
hub URL must be `https://`; `http://` is accepted only for `localhost`
and loopback addresses.

`join` needs a tracker token, taken from `JIG_TOKEN`, `GITHUB_TOKEN`,
`GH_TOKEN`, or `gh auth token`, in that order. It checks the token with
`GET /user` and sends the login to the hub. `JIG_GITHUB_API_URL` sets the
API base for that check (default `https://api.github.com`). The
enrollment token is used once and not stored.

### What is stored

| File | Contents |
| ---- | -------- |
| `~/.config/jigz/hub.json` | hub URL, daemon id, name, credential, runner label, tracker token. File mode 0600, directory 0700. `jig daemon` refuses to start when the file is readable by group or others. |
| `~/.local/state/jigz/hub/config.json` | last config fetched from the hub and its ETag (0600) |
| `~/.local/state/jigz/hub/state.json` | the pause flag (0600) |
| `~/.local/state/jigz/hub/status.json` | the last status document sent (0600) |
| `~/.local/state/jigz/jigd.log` | daemon log |
| `~/.local/share/jigz/hub/loops/<owner>/<name>/<workflow>/` | one root per project and workflow: the files served by the hub, the loop's workspaces and its engine database |

`machine.yaml`, `pool.json`, the agent CLIs and their logins stay on the
machine. The hub receives their status (pool entry names and runtimes,
working hours), not the credentials.

### The service

`join` installs and starts a user service that runs `jig daemon`.

- Linux: systemd user unit `~/.config/systemd/user/jigd.service`. The
  unit records the `PATH` of the shell that ran `join`, so the agent CLIs,
  `gh` and `git` are found (`/usr/local/bin:/usr/bin:/bin` when it is
  empty), and `HOME` and `XDG_CONFIG_HOME`, `XDG_STATE_HOME`,
  `XDG_DATA_HOME` and `XDG_CACHE_HOME` when they are set, so the service
  reads the `hub.json` that `join` wrote. The launchd agent records the
  same variables. A user service stops at logout unless
  lingering is on; when it is off, `join` prints
  `loginctl enable-linger <user>` and does not run it.
- macOS: launchd agent `io.github.kinged007.jigd` in
  `~/Library/LaunchAgents/`. It runs while the user is logged in.
- Windows is not supported.

`jig daemon` runs the daemon in the foreground and logs to `jigd.log`. The
installed service runs it in the background. `jigd` is the same binary under
another name: an executable named `jigd` or `jigd.exe` (a symlink or copy of
`jig`) behaves as `jig daemon` with the arguments it is given. SIGTERM stops
every loop it started.

`jig leave` removes the service, `hub.json` and the daemon state in
`<state>/jigz/hub` (stored config, pause state, status document and
history database). The loop roots are kept. `join` removes the same
daemon state when the new enrollment has another hub URL or daemon id.
A daemon started by hand keeps running until you stop it.

`jig status --json` with no repo root prints the daemon's status
document while `hub.json` exists.

### Settings

In `machine.yaml`:

```yaml
connected:
  idle_timeout: 15m       # a loop's engine stops after this long without work
  fallback_interval: 5m   # how often the machine searches GitHub itself
```

Both are Go durations with a minimum of `1m`. The defaults are `15m` and
`5m`.

### How loops run

- The daemon reads no repo config (`.jigz/config.yaml`), `jigz.yaml` or
  the unite registry. The hub's config is the only source.
- It starts one loop per project and workflow, only when there is work.
  A loop stops its engine after `idle_timeout` without work.
- The runner label (`runner:<name>`) is added to every loop's query
  filter, so a loop picks up only items that carry it.
- The `wake` command starts a loop. Every `fallback_interval`, and every
  minute while the hub is unreachable, the daemon searches GitHub for
  open items with the runner label and starts the loops that fit them.
- A new config is applied to a loop when it is idle. An invalid workflow
  is not applied and is reported as the loop's last error.
- With the hub unreachable the daemon runs on the stored config. With no
  stored config it runs no loops and retries.
- When the hub rejects the credential, the loops are drained and the
  daemon retries every 10 minutes.
- The daemon listens on no port. It opens the connections: HTTPS to the
  hub and one WebSocket for commands. The WebSocket client is
  `github.com/coder/websocket`.

Commands implemented: `start`, `stop`, `drain`, `restart_when_idle`,
`pause`, `resume`, `wake`, `reload_config`. `pause` drains every loop and
ignores wakes until `resume`; the flag survives a restart. The commands
`session.*` and `task.run` and the preview endpoints are answered as
`unsupported`.

### What the hub can run on your machine

The workflow files and hooks the hub serves run on this machine as the
user who joined. Hooks are shell commands, and prompts drive an agent
that has your logins and your GitHub token. Enrolling a machine gives the
hub's operators code execution as that user. The status document lists
the hash of every applied file.

### No local dashboard

Connected mode has no local dashboard and no Questions page. Answers to
a parked item are written on GitHub. `jig daemon` runs the question
scanner over the projects of its loops and resumes answered items that
carry its runner label.

## Ad hoc: auto-merge on clean review

To fully automate build → review → merge on one repo (not a template
default), edit that repo's `.jigz/workflows/prompts/review.md` Step 4
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
git config --global user.name "jig-agent"
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
  `jig`, as a signal does. The remaining loops are stopped
  cleanly and the registry entry is dropped, but nothing restarts them.
- **Workflow files are not version-stamped.** A workflow file you have
  not edited is not updated when the binary is upgraded. Delete it and
  re-run `setup` to take the shipped version.
- **Windows is unsupported.** The engine releases a `.zip`; the
  installer reads `tar.gz`.

## Customizing agents and workflows

The shipped default is the `claude-code` agent with the
`claude-haiku-5-5` model. Every `WORKFLOW.*.md` carries a `claude-code:`
block and a `pi:` block; the engine reads only the block named by
`agent.kind`. To switch to `pi`, edit the workflow files: set
`agent.kind: pi` and `agent.command: pi`. To use another model, edit the
`model:` line of the block the agent kind names; the value passes
through to `claude --model` unvalidated, so only use aliases your
installed CLI accepts. The account pool in `machine.yaml` (see
[Working hours and agent accounts](#working-hours-and-agent-accounts))
overrides `agent.kind` and `agent.command` at run time, so the same
workflows can run with another runtime without touching the files. The
pinned engine only knows `pi` and `claude-code` (plus a few others);
other adapters need a newer or upstream engine binary, and a loop whose
agent kind the engine lacks does not start (see [Engine
capabilities](#engine-capabilities)). Setting `agent.kind` to `codex` or
`opencode` needs `agent.command` set as well (the shipped `command:
claude` would be passed to the adapter), and no pool runtime or shipped
adapter block exists for them. The dashboard's token/model columns populate
for either agent.

## Roadmap

- Restart a loop that exits instead of ending the run. Currently one
  crashed loop stops every other loop; see
  [Known limitations](#known-limitations).
- Stamp workflow files with the binary version that wrote them, so an
  upgrade can offer a new version of a file the user has not edited.
