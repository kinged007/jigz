# Jigz: product vision

Status: draft, 2026-11-19. Ordered delivery plan: [ROADMAP.md](ROADMAP.md).
Architecture and engine contract: [plans/jigz/vision-and-roadmap.md](plans/jigz/vision-and-roadmap.md).

## The bet

A team's backlog is already in GitHub. The work of moving it — planning,
coding, reviewing, merging — is what costs people their day. Jigz moves that
work to agents that run on hardware the team already owns, while the team
keeps the two decisions that matter: what gets worked on, and what merges.

The product bet is that agents are only trusted when a person can see what
they did and stop them. Every feature below follows from that.

## Who it is for

| User | Job | What they need from Jigz |
| --- | --- | --- |
| Solo maintainer | Clear a backlog on evenings and weekends | Set up once, walk away, spend nothing |
| Small team | Raise throughput without new process | A PR waiting for review each morning |
| Platform / security | Allow AI agents on company repos | Credentials on our machine, audit trail, hard stops |

Out of scope: hosted SaaS, competing trackers, replacing a human reviewer.

## Where we are

`jig setup` plus one labeled issue is enough to get a working PR. The loops,
budgets, working hours, account pools, dashboard and connected mode exist.
What stands between the product and daily use is not capability but trust and
first-run cost:

1. **One crashed loop ends the run.** A single exit stops every other loop.
2. **Upgrades fight the user.** Prompt and workflow files are not
   version-stamped, so upgrades cannot offer new versions without overwriting
   local edits.
3. **The first hour is the hardest.** Setup, tokens, engine download and agent
   CLI logins are four separate failure surfaces before the first PR exists.
4. **Proof requires reading.** There is no preview of what an agent built, so
   a reviewer must trust the diff plus a screenshot they take themselves.

## The next four quarters

Each quarter answers one question. Ship in the order given; each builds on
the one before.

### Q1 — Does it keep running?

Make an unattended week the normal case, not a good day.

- Restart a loop that exits, with backoff and a visible count, instead of
  ending the run.
- Version-stamp workflow and prompt files so an upgrade can offer a new
  version and keep local edits.
- Fail with one message that names the fix: missing token scope, missing CLI,
  engine checksum mismatch.

Exit criteria: a machine runs a week with no manual restart, and every stop
that still happens says why in the dashboard.

### Q2 — Is the first PR cheap?

Cut the path from `install.sh` to a merged PR to under fifteen minutes of
human attention.

- A guided first run: one command checks token, CLI and engine, then reports
  what is missing instead of failing mid-loop.
- A starter workflow that matches the repo's language and test command.
- Label documentation generated from the labels actually created.

Exit criteria: a new user following only the README gets a PR opened and
closed without opening an issue in our tracker.

### Q3 — Can I see what it built?

Previews (ROADMAP item 6). A run's result is a URL, not a description.

- Per-project preview command plus a tunnel, or a sandbox provider URL.
- Long, random, expiring URLs on the run page and on the GitHub PR.
- Cleanup on expiry, and registration with the hub in connected mode.

Exit criteria: a reviewer opens the PR and follows one link to the running
change.

### Q4 — Can it do the work I did not label?

Move from label-driven to intent-driven without giving up control.

- A prompt or an issue comment can queue work, with the same claim, budget
  and working-hours rules as a label.
- Questions route to the person who wrote the code, not to the last actor.
- Scheduled work: "prepare the release notes on Monday" is a scheduled entry,
  not a cron job the user maintains.

Exit criteria: a repository runs a week on scheduled and requested work with
no new labels added, and the board still shows why each run started.

## What we will not do

These are decisions, not gaps.

- **No hosted service.** Agents run where the code and credentials already
  are. Fine to be harder to try; not fine to be insecure.
- **No new trackers.** GitHub only, until GitHub support is complete.
- **No autopilot merge.** A person labels the merge, or the project config
  names a branch where the agent may merge. Approval stays explicit.
- **No dashboard analytics.** Metrics stay local and recent. Long history is
  not a differentiator.
- **No prompt marketplace.** Workflow files are the extension point.

## How we measure

Only signals the product changes, all available locally:

| Metric | Target direction |
| --- | --- |
| Time from `install.sh` to first PR opened | Down, under 15 minutes of human attention |
| Runs that end in a merged PR or an explicit close | Up |
| Runs needing a human to intervene | Down |
| Unattended days per installation | Up |
| Loop restarts required per week | Down |

No telemetry. Numbers come from the run history the dashboard already keeps,
and from what users tell us in issues.

## Signposts to revisit

The vision above assumes agents stay useful mostly when a person reviews.
If any of these hold after a quarter, the assumptions are wrong and the plan
above should change:

- Merged PRs from runs sit unreviewed for days because nobody trusts the
  diff. Then trust work (Q3) moves ahead of throughput work (Q4).
- Most runs are abandoned by a human, not failed by the agent. Then the
  problem is intent, not capability: move question routing and scheduled work
  earlier.
- Users run Jigz on one repository and one label path. Then setup is still
  the product: stay in Q1-Q2 and stop adding surfaces.
