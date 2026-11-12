# Concepts

## What is what

**Jigz** (this repo) is a small Go wrapper. It does setup (config,
labels, workflow files) and then launches one loop process per workflow,
plus a web dashboard. It contains no agent itself.

**The engine** does the heavy lifting inside each loop: it polls the
tracker on a timer, claims a matching item, clones the repo into a scratch
workspace, hands the item to an agent, and records the run in a local
SQLite database. `claude-code` is a standard agent kind that every
supported engine registers, so the generated workflows run without extra
setup. The one feature the workflows need that an engine may lack is the
`github-pr` tracker (which treats pull requests as work items). The `pi`
agent adapter is optional: an engine only needs it when a workflow or
agent uses `pi`.

**The agent** is Claude Code (`claude`), a coding agent that runs in a terminal the same
way you would: it reads files, edits code, runs checks, and shells out
to the `gh` CLI to read comments, open PRs, and move labels. The `pi`
agent is supported too; see
[Customizing agents and workflows](customization.md#customizing-agents-and-workflows).
## Names

| Name | What it is | Where it appears |
| --- | --- | --- |
| `Jigz` | The project and brand. | Repository `kinged007/jigz`, Go module `github.com/kinged007/jigz`. |
| `jig` | The command. Each loop is one `jig <root>` process per workflow. | Shell, `jig` subcommands, `JIG_*` environment variables. |
| `jigz` | The name of data on disk and in the repository. | `.jigz/`, `~/.config/jigz/machine.yaml`, per-user state, data and cache directories `jigz`, `jigz-<name>.db`, `.gitignore` entries. |
| The engine | The external binary that polls, claims items and runs agents. It is pinned and not vendored. | `JIG_ENGINE_*` variables, [The engine](install.md#the-engine). |
| The dashboard | The web page that `jig` serves for a repo. `--no-dashboard` turns it off. | [Dashboard](dashboard.md#dashboard). |

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
2. **Assignee scope** — every query ends in `assignee:@me`,
   meaning only items assigned to the token owner are picked up. This
   keeps several people (or bots) from fighting over one backlog.
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
`agent:done`, and a person labels an approved PR `agent:merge`.
## Labels

The `labels:` list that `jig setup` writes into a new `.jigz/config.yaml`
holds fifteen labels, one role each. Setup creates exactly the labels listed
in that file; a config without a `labels:` key creates none. The six triggers
are applied by a person or a chained agent; P0–P3 are a
person's triage call; the rest are set by the agents themselves.

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
| `backlog` | grey | Deferred by a person; no loop should touch it |
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
`needs-human` (see [Questions](configuration.md#questions)).

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
agent applies `agent:merge`: an approved PR waits for a person to label it.

The chained label must land on the item the next loop watches: the `prs`
loop watches PRs, so label the PR, not the issue it came from. `.jigz/workflows/prompts/review.md` Step 4 and
`review-fix.md` Step 5 carry these forms.
## The loops

The config that `jig setup` writes defines two work entries, `issues` and
`prs`. Setup generates one `WORKFLOW.*.md` file for each (each runs as its
own loop process) with one dispatch rule per trigger label:

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

`setup` and the loop startup never change or remove an existing prompt
file. The old `plan`, `build`, `review`, `review-fix` and `merge` loops
and the `issues` and `prs` loops would process the same issues, so only
one set may exist. To migrate:

1. Delete the five old files (`WORKFLOW.plan.md`, `WORKFLOW.build.md`,
   `WORKFLOW.review.md`, `WORKFLOW.review-fix.md`, `WORKFLOW.merge.md`),
   and their `.sortie-<name>.db` files if you do not need the run history.
2. Add `issues` and `prs` entries to `work:`, each with a `query_filter`
   and `rules`, and rerun `jig setup` to generate the two new files.
3. In `.jigz/config.yaml`, rename old loop names to `issues` or `prs` in
   the `filters:` key, and move each `workflows:<name>.budget` block to
   `work:<name>.budget`. The keys `milestone`, `assignee` and `workflows`
   are no longer accepted: there is no milestone scope, the assignee
   scope is always `assignee:@me`, and budgets live under
   `work.<slug>.budget`.
4. Existing installs keep their old `prompts/plan.md` and `prompts/build.md`,
   because setup never overwrites a file. Diff them against the shipped
   copies or delete them and rerun `setup`. An old `build.md` that does not
   remove `agent:plan-build` in its finishing step leaves that label on the
   issue, so the issue is dispatched again. `setup` prints a note when
   either file differs from the shipped copy.
5. Add the `agent:plan-build` and `agent:docs` entries to the `labels:` list of an
   existing `.jigz/config.yaml` (copy them from the list above), then
   rerun `setup`. `setup` only creates the labels in that list.

The loop set is the `work:` entries of `.jigz/config.yaml`. A run starts
a loop for each `work:` key that has a `WORKFLOW.<key>.md` in
`.jigz/workflows/`, named by the key. A `WORKFLOW.*.md` file without a
`work:` entry is not started. To add a loop, add a `work.<slug>` entry
(e.g. `triage`) with a `query_filter` and `rules`, then rerun setup to
generate its `WORKFLOW.<slug>.md`.

`JIG_ONLY=issues` narrows the run to the named loops, for a
supervisor that drives several repos and wants a different set per
repo. Unset (the default) runs every configured loop. A name that
matches no configured workflow is fatal rather than silently dropped, so
a typo cannot leave that loop's work unwatched.
