# Dashboard

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
  and the page reads the last 1 MiB of the file, at most the last 300
  events. Each entry is shown as a message, a thinking block (collapsed), a
  tool call, a tool result or an error. Messages, notifications and thinking
  are rendered as Markdown (headings, lists, code blocks, tables, quotes,
  bold, italic, inline code and `http`, `https` and `mailto` links) by the
  page script without loading anything from the network. HTML in the text is
  shown as text. Tool results are rendered as Markdown only when they contain
  Markdown such as a code fence, bold text or a link, and otherwise stay
  plain text. Text longer than 4000 bytes is cut in the list and shows how
  many characters are missing with a Show full button. The button loads the
  whole text from `GET /runs/<owner>/<repo>/<workflow>/<id>/agent-log/event`
  (up to 256 KiB, with a note when more is cut). The event is named by its
  position in the transcript or by the sequence number of its event row, never
  by a path. The page keeps open blocks and expanded events when it refreshes.
  The engine shortens long notification and turn messages to 200 or 500
  characters in its event record; these events are marked "Truncated by the
  engine" and cannot be expanded.
- Engine log: the lines of the workflow log that name the issue. The log loads
  when the Engine log tab opens, and the Reload button and the refresh of a
  running run read it again.

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
of `machine.yaml` lists the pages and sections to hide:

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
| `agent_log` | The agent log tab, the agent steps in the events list, and the endpoints behind them (the polling route and the full-text route), on the run detail page |
| `session` | The session id, workspace directory and resume command on the run detail page |
| `log_tail` | The engine log tab, its milestones in the events list, and the endpoint behind them, on the run detail page |
| `audit_log` | The audit log on Operations and project pages, `GET /api/v1/audit` |
| `effective_config` | The effective configuration table on project pages |

The header load figures (`/status`) are always served. Hiding `projects`
does not remove the project names that Operations, Runs and the fleet API
show; hide those pages too when project names are sensitive.
