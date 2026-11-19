# Jigz: product vision

Status: draft, 2026-11-19. Implementation sequence and status: [ROADMAP.md](ROADMAP.md).
Engine contract: [plans/jigz/engine-interface.md](plans/jigz/engine-interface.md).

## The bet

Issues live in GitHub and should stay there. What costs a team its day is the
work around them: starting agents, watching them, paying for them, recovering
them, and keeping every repository's setup identical. Jigz takes that work.

Jigz is the control plane for a fleet of coding agents on one machine: it
supervises the engine, holds the workflows, prompts, budgets, schedules and
accounts, and shows all of it in a dashboard. A person labels an issue and
walking away is the normal case; a project configured once manages itself.

## Who it is for

| User | Job | What they need from Jigz |
| --- | --- | --- |
| Solo maintainer | Clear a backlog on evenings and weekends | One setup, runs unattended, spend capped |
| Small team | More throughput without new process | A PR waiting each morning, visible cost |
| Platform / security | Allow agents on company repositories | Credentials on our machine, audit trail, hard stops |
| Operator of many repos | Keep every project configured the same | Copy a bundle, update it, trust public-repo defaults |

Out of scope: hosted SaaS, replacing GitHub as the tracker, replacing the human
reviewer.

## Where we are

The engine, supervision, budgets, account pools and dashboard exist. Three
things pull in the opposite direction of "set it up and let it manage itself":

1. **The dashboard is read-only.** Every operation — start, stop, pause,
   restart, validate, budget, wake — is a CLI command. Whoever runs the fleet
   keeps a terminal open, and the status pages it does have stop short of the
   action the page is about.
2. **Recoverable failures become manual work.** An agent that is not
   authenticated, or a usage limit hit, is retried until the attempt limit and
   then parked. Recovery means removing the label and applying it again, which
   does not always take. This is the single most repeated complaint.
3. **Configuration does not travel.** `.jigz/config.yaml`, prompts and helper
   scripts are gitignored, so moving a working setup to a second repository, or
   updating it after a fix, is manual copying.

The engine already supports tracker kinds `file`, `gitea`, `github`,
`github-pr`, `gitlab`, `jira`, `linear` ([plans/jigz/engine-interface.md](plans/jigz/engine-interface.md)),
so issue-driven work is the subset Jigz currently exposes, not the whole engine.

Two decisions taken: setup defaults to `claude-code` and the runtime stays
configurable; the dashboard stays without authentication, designed for a
private network or a reverse proxy.

## The next four quarters

Ordered. Each quarter has one question and one exit criterion.

### Q1 — Can one bad run be recovered in one click?

Make recoverable failures self-healing and the rest cheap to recover.

- Classify a failed attempt before retrying: unauthenticated agent, usage
  limit, engine crash, transient network. Non-retryable classes park the
  issue and notify once instead of burning attempts.
- Usage limits move to the next account in the pool and resume when the
  window resets; authentication failures name the missing login in one line.
- `jig issue restart <id>` and a dashboard button: clear the parked and retry
  state, remove and re-apply the claim in one action, wake the loop. Works
  when the label apply previously failed.
- Fold the fixes already proven on branches into one recovery path.

Exit criterion: a stuck run is recovered from the dashboard in under a
minute, with no label edit in GitHub.

### Q2 — Can the dashboard run the fleet?

Dashboard becomes the control plane; the CLI stays as the scriptable surface.

- Every operation the CLI offers is reachable in the dashboard and calls the
  same code path: start, stop, restart `--when-idle`, pause, resume, budget
  override, validate, reload, wake, restart issue.
- Edit workflow files and prompts in place: validate before write, keep the
  existing backup, show a conflict when the file changed under the editor.
- Schedules, agents and account pools get pages that show state and change it.
- No authentication by design: bind localhost or a private interface by
  default, refuse a public bind without an explicit flag, document the proxy
  and private-network deployments, and require a POST with a CSRF token for
  every write.

Exit criterion: a week of fleet operation finishes without a `jig` command
being typed for an operational task.

### Q3 — Can a project manage itself?

Runs that do not start from a GitHub issue, per
[plans/jigz/scheduled-work.md](plans/jigz/scheduled-work.md).

- Scheduled entries in the config: cron, an optional shell `check`, an optional
  prompt. A negative `check` records a skipped tick and starts no agent.
- Issue-free agent runs through the engine's `file` tracker with a synthetic
  issue; direct agent invocation as the fallback if the engine cannot carry
  them.
- The manager workflows this makes possible: a daily standup report over work
  completed and pending, a project-manager agent that labels and assigns
  issues, a stale-issue sweep, a release-notes draft.
- Same budgets, working hours, permissions and approvals as loops; overlap is
  skipped, never queued.

Exit criterion: a repository runs a week of scheduled and requested work it
configured itself, with no new labels added.

### Q4 — Can a setup be shared, updated, and trusted?

- Export a configuration bundle: config, prompts, workflow files, helper
  scripts, a version and a checksum. Secrets are refused, not included.
- Import into another project; update an existing one by diff, keeping local
  edits. A bundle records what it wrote so the next update can show drift.
- Public-repository defaults built into generated workflows and validated:
  claim only from an allowlisted author or a chosen label, never merge
  without an explicit merge label, and require a human for anything the
  project did not opt into.

Exit criterion: a second repository is running from a bundle exported from
the first, and updating it after a prompt fix is one command.

## Horizon

Recorded, not scheduled.

- **Other trackers.** GitLab, Jira, Gitea and Linear through the engine's
  tracker kinds. The work model is the same; the claim query and write-back
  differ. Not urgent; the engine already carries the kinds.
- **Previews.** A run's result as a URL on the PR.
- **Hub.** Several machines and repositories under one dashboard, with the
  session host and issue-free task runs.

## Non-goals

Decisions, not gaps.

- **No hosted service.** Agents run where the code and credentials are.
- **No authentication layer.** The dashboard is for a private network or a
  proxy; adding accounts is a worse answer than a smaller attack surface.
- **No second implementation.** A dashboard action and its CLI command share
  one function.
- **No autopilot merge.** A person labels the merge, or the project config
  names what may merge. Approval stays explicit.
- **No tracker of our own.** GitHub first; other trackers arrive as engine
  capability, never as a new Jigz data model.
- **No hosted bundle registry.** Bundles travel as files or git.

## Security rules that always apply

- Public repositories: the default claim filter requires an allowlisted author,
  and the default merge requires an explicit label. Never "any issue, any PR".
- Bundles and generated files never carry tokens; import refuses a bundle that
  contains secret-looking values.
- The dashboard binds to a private interface; a public bind is explicit,
  warned about in the UI, and documented as requiring a proxy.
- Prompts and workflows are the user's policy. Jigz makes the safe form the
  default and validates the dangerous ones; it cannot promise behaviour the
  user's prompt does not describe.

## How we measure

Signals the product changes, all available locally:

| Metric | Target direction |
| --- | --- |
| Operational actions taken from the dashboard | Up, most of them |
| Time to recover a stuck issue | Under one minute, no label edit |
| Runs ended by a non-retryable failure parked without notice | Zero |
| Projects started from a bundle | Up |
| Unattended days per installation | Up |

No telemetry. Numbers come from the run history and the dashboard's own
counters.

## Signposts to revisit

- Recoveries still require the CLI weeks after Q1. Then the dashboard's write
  path is the problem, not failure classification: do Q2's write path first.
- Scheduled runs cannot be carried by the engine's `file` tracker. Then the
  direct-invocation fallback becomes the primary design, and Q3 grows.
- Users keep one repository and never share a bundle. Then bundles stay a
  documented recipe, and Q4 moves to tracker compatibility.
- Most parked issues are parked because the prompt asked for something the
  agent cannot do. Then prompts, not recovery, are the next quarter.
