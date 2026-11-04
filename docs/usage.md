# Usage

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
`validate`), Setup (`setup`), run options (`--host`, `--dashboard-port`,
`--no-dashboard`, `--unite`) and Other (`--version`, `--dump-version`).

`setup` confirms before changing a label whose colour or description
differs from the shipped one. It never changes an existing workflow file.

`jig status [--json] [repo-root]` reports the run serving the
repo-root: version, pid, state, and per workflow its state (`running`,
`draining`, `offline`, `paused`, `sleeping`, `exited`, `stopping` or
`disabled`), pid, restart count, last exit code, the agent it runs
with, its running sessions, and, when it is not running, the offline
reason and the estimated start time. `--json` prints the status document
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

Each run records the dashboard address it serves, or the shared address it
joined with `--unite`, in its registry
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
cleanup command once (see [Workspace cleanup](configuration.md#workspace-cleanup)).

`jig wake [--json] [repo-root]` starts the engine loops of a run
that is sleeping (see [Idle sleep](configuration.md#idle-sleep)). `--json` prints `woke`,
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

### Selecting loops

`JIG_ONLY` is a comma-separated list of `WORKFLOW.*.md` stems. Unset runs
every installed loop; a name that matches no installed workflow is fatal,
so a typo cannot leave that loop's work unwatched.

```sh
JIG_ONLY=issues jig            # only the issues loop (plan, build, docs)
JIG_ONLY=prs jig start         # only the PR loop, in the background
JIG_ONLY=issues,prs jig        # both shipped loops, same as unset
JIG_ONLY=triage jig            # a custom WORKFLOW.triage.md loop only
```

### Dashboard flags

```sh
jig --unite                    # join the shared dashboard, or start it on 7677
jig --dashboard-port=8080      # force the dashboard onto port 8080 (fatal if busy)
jig --no-dashboard             # no dashboard page (also disables the question scanner)
jig --host=0.0.0.0             # listen on all interfaces; anyone who can reach it can view it
```

With `--unite`, the first run serves the dashboard (default
`http://127.0.0.1:7677`, bound to loopback) and later runs detect it and
join instead of starting a second page, so every registered repository
appears in one place. Without `--unite` the page shows only its own
repository. The dashboard is read-only and has no authentication: use it
locally, on a private network, or behind your own secure proxy. See
[Dashboard](dashboard.md#dashboard).

Evidence status: Confirmed against `cmd/jig` unless noted.
