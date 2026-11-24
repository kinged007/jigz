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
`serve -refresh 15m` syncs and rebuilds in the background at that interval.
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

- Drag to orbit, wheel to zoom, click a node to focus it; `/` focuses the
  prompt, `Esc` clears.
- Prompt: free text over labels, titles and ids; `#123` for an item. Enter
  focuses the best match.
- Tabs filter node types (issues, pulls, code, people, topics, workflow).
- The bar under the graph replays history: nodes appear at their creation time.
- Panels: repository, label counts, inspector, neighbour radar, file tree
  (hotspots, or the files of the selected commit/PR/issue), event log, stage
  transition matrix, activity, the selected item's trajectory, keywords, flow
  metrics, and workflow variants.
- The URL hash keeps the selection and view (`#node=issue:1840&tab=workflow`).

## Limits

- Comment bodies are not stored; comments count as engagement only.
- Only first-parent history of the default branch is read, and only files
  present at its head become nodes.
- Canvas 2D draws every link each frame. It holds about 25k links at
  interactive rates on a desktop GPU; beyond that it needs WebGL.
