# Node explorer (experiment)

A standalone prototype that turns a GitHub repository into a knowledge graph
and shows it in a 3D browser explorer. Issues, pull requests, commits, files,
directories, labels, inferred workflow stages, title keywords, Conventional
Commit scopes, milestones and people are nodes; the links between them come
from authorship, labels, timelines, cross references, merge commits and the
files each commit touches.

It is not wired into `jig`. It lives here to decide whether the idea belongs
in the dashboard.

## Run

```sh
cd experiments/explorer
go run . update -repo kinged007/saas-template   # sync, then build graph.json
go run . serve                                   # http://127.0.0.1:7878
```

`update` is `sync` followed by `build`; both can run on their own.
`serve -refresh 15m` syncs and rebuilds in the background at that interval and
turns on live updates in the page (see "Live updates"). Without `-refresh` the
page loads `graph.json` once and shows no live indicator.
The token comes from `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`.

Data is stored in `.explorer/<owner>__<name>/` (ignored by git):

| File         | Content                                                        |
| ------------ | -------------------------------------------------------------- |
| `github.db`  | SQLite: issues and PRs with their normalized timelines, sync watermarks |
| `repo.git`   | bare clone of the default branch                               |
| `graph.json` | the built graph, layout and panel statistics served to the UI  |

## How the sync limits API use

- Issues and PRs are read through GraphQL, 50 per request, each with its
  first 100 timeline events (labels, closes, cross references, merges,
  reviews, comments, assignments) in the same request. One page costs about
  1 point of the 5,000 per hour. The first full sync of saas-template
  (1,236 issues, 600 PRs) took 40 requests and 77 points.
- Both lists are ordered by `updatedAt` descending. A sync stops at the first
  item not updated since the last sync, so a repeat sync with no changes costs
  one request per kind. That ordering is the repository-wide "what changed"
  feed; closed items that nobody touched are never fetched again.
- Items whose timeline has more than 100 events get their remaining pages
  in follow-up requests.
- The cursor is saved after every page. A sync stopped by `-max-pages`,
  Ctrl-C or the rate-limit reserve (`-reserve`, default 500 points) resumes
  where it stopped. `-wait` sleeps until the limit resets instead of stopping.
  Secondary rate limits (403/429) are retried after `Retry-After`.
- Commits and file changes come from `git log` on a bare clone and cost no
  API calls. The token is passed to git through environment config, so it is
  not written into the clone or shown in the process list.

## What the build infers

- **Workflow stages.** No workflow is configured. A label is a stage when it
  is added at least 5 times and removed again in at least 35% of cases (an
  automation hands the item on by swapping labels). Labels that stay on
  (type, priority, area) are attributes. Each item gets a path:
  `opened`, each stage in the order it was added, then `merged`, `closed` or
  `not planned`. From all paths the build counts transitions, median time
  between stages, median dwell per stage, and the most common path variants.
- **Keywords.** TF-IDF over titles (weight 3) and bodies with code blocks,
  URLs and HTML comments removed. The vocabulary is ranked by how many titles
  use a term, because agent-written bodies share boilerplate. Each item links
  to its top 3 terms.
- **Scopes.** `type(scope): subject` titles of issues, PRs and commits link to
  scope nodes, which joins tracker items to code history.
- **Links.** PR `closingIssuesReferences`, cross references that close their
  target, `ConnectedEvent`, `ClosedEvent` closers, duplicates, merge commit
  SHAs, and `#123` references in commit subjects.
- **Layout.** A 3D force simulation (Barnes-Hut, d3-force rules) runs at build
  time, so the browser only renders.

## UI

The graph is on the left and the twelve panels are on the right (scrollable).

- Drag to orbit, wheel to zoom, Ctrl/Cmd/Shift-drag or middle-button drag to
  pan, click a node to focus it, `r` resets the view, `/` focuses the prompt,
  `Esc` clears.
- Each node type has its own colour and shape (legend below the graph;
  clicking a legend entry hides or shows that type). Open issues and PRs are
  filled, closed and merged ones are outlined.
- Prompt: free text over labels, titles and ids; `#123` for an item. Enter
  focuses the best match.
- Tabs filter node types (issues, pulls, code, people, topics, workflow).
- The timeline has a start handle (amber) and an end handle (cyan). Only items
  and commits created inside the window are shown, together with the labels,
  people, files and so on linked to them. The play button moves the end handle
  forward from the start handle.
- All panels are computed for the window, so they change while the replay
  runs: label counts, file hotspots, event log, stage matrix, activity,
  keywords, flow metrics and workflow variants.
- The inspector has an "Open in GitHub" button (new tab) for issues, PRs,
  commits, files, directories, labels, stages, scopes, keywords, milestones
  and people.
- The URL hash keeps the selection, view and window
  (`#node=issue:1840&tab=workflow&f=1790812800&t=1791763200`).

## Live updates

With `serve -refresh <interval>` the header shows `LIVE` and a countdown to the
next refresh (`SYNCING…` while it runs, `OFFLINE` when the server cannot be
reached; the tooltip shows the last sync error). The page polls `/status` every
3 seconds. When `graph.json` has been rewritten it fetches the new graph and
merges it into the running page:

- Nodes that already exist keep their position. New nodes start at the mean
  position of the nodes they link to.
- The camera, selection, search, tab, hidden node types, time window (the
  handles keep their dates; an end handle at `NOW` follows new data), replay
  and panel scroll positions are kept.
- A panel is rewritten only when its content changed, and a changed panel
  flashes once. The status line reports what was added and updated.

Layout drift: positions are never recomputed in the page, so after many
refreshes the picture can differ from a fresh load. Reload to get the full
layout.

## Limits

- Comment bodies are not stored; comments count as engagement only.
- Only first-parent history of the default branch is read, and only files
  present at its head become nodes.
- Canvas 2D draws every link each frame. It holds about 25k links at
  interactive rates on a desktop GPU; beyond that it needs WebGL.
