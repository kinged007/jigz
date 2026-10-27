# Jigz: Phase 3 plan (Questions)

Status: draft for discussion (2026-10-26)

Implements Phase 3 of `plans/jigz/vision-and-roadmap.md` (section 5.3).
Work is verified as `plans/system/delivery-method.md` section 2.1
describes: the acceptance test in section 4 is written before the build,
and a lane other than the builder reviews each package.

## 1. Scope and rules

Agents ask questions in `agent-question` blocks and read answers from
`agent-answer` blocks (`plans/system/hub-protocol.md` section 4): HTML
comment markers inside an issue or PR comment or in the issue or PR
description (sample: kinged007/jigz#14). Agents post one comment per
escalation; a question in the description needs no second comment.
Answers are read from comments only: an `agent-answer` block in a
description is ignored.

`jig` monitors and orchestrates agents; people work on GitHub. A person
answers a question by posting an `agent-answer` comment on GitHub, or
through the hub, which posts the same comment with the person's own
GitHub token. The dashboard has no authentication, so it writes nothing
to GitHub: its Questions page is read-only (2.6). The question scanner
(2.1) resumes a parked item once its questions are answered (2.4).

Phase 3 reads the description and the conversation comments of issues and
PRs (section 5, "Later" lists what is not read). Engine behaviour follows the pinned engines: `kinged007/sortie`
v1.28.1 and, in the upstream acceptance test, `sortie-ai/sortie` v1.29.0
(2.8).

A blocking question uses the existing label `needs-human`, which both
shipped workflows exclude from dispatch
(`cmd/jig/workflows/WORKFLOW.issues.md` line 11, `WORKFLOW.prs.md`
line 12). A trigger label is an `agent:*` label other than `agent:done`.

1. Every escalation to `needs-human` that waits for an answer carries at
   least one blocking `agent-question` block in its comment. An escalation
   with no question (for example, a PR for a person to review) has none.
2. An item is ready to resume when all of these hold:
   - it carries `needs-human`;
   - it has at least one blocking question, and every blocking question
     has an answer from an authorized author (2.2);
   - it has no problem from the parse rules (2.2): no malformed block and
     no differing `resume` values;
   - at least one of those answers was posted after the latest time
     `needs-human` was added to the item (2.4). An item parked again
     without a new question, such as a PR escalated for review after its
     earlier questions were answered, is therefore not resumed.
3. An escalating agent posts blocking question blocks, each with
   `resume="<trigger>"`, and adds `needs-human`; the run is `succeeded`.
   At handoff the engine adds `agent:done` and removes every other state
   label (the trigger, `in-progress`, any terminal label); `needs-human`
   stays. A parked item has `needs-human` and `agent:done`, no trigger.
4. The scanner resumes a ready item (2.4); the next poll dispatches a
   new run (attempt 1). The trigger is gone by then, so the block records
   it in `resume`.
5. A non-blocking question does not stop the work: the agent proceeds with
   its suggested answer and records the assumption.

## 2. Design

### 2.1 Question scanner

`internal/collector/questions.go` (`Scanner`) reads GitHub through the
client in `internal/github/github.go`. It runs in two places:

- In the dashboard process (standalone and united mode), over every
  registered project whose `github.questions` is true. Each project uses
  the token its loops use (`projectTarget` in `cmd/jig/history.go`); a
  project without a token is not scanned and reports "no GitHub token".
  `jig --no-dashboard` runs no scanner.
- In `jig daemon` (connected mode), over the projects of its loops (2.5).

One pass per project:

1. `GET /search/issues?q=repo:<slug> state:open label:needs-human` (one
   call; covers issues and PRs), plus the daemon's runner label
   qualifier. These items are candidates.
2. Dashboard only: items with a run in the store within
   `questions_window` are also candidates, so non-blocking questions on
   items that are not parked are listed. For a candidate not in the
   search result, `GET /repos/<slug>/issues/<n>` with the stored ETag
   reads its labels and assignees.
3. For each candidate: `GET /repos/<slug>/issues/<n>/comments?per_page=100`
   with the stored ETag. PR conversation comments use the same route.
   Review comments and review bodies are not read. The description comes
   from the issue object of step 1 or 2 (search result, or the issue route
   with its stored ETag) and is cached in `question_items`; a changed
   description makes the item be read in full again.
4. For each candidate that meets the first three conditions of rule 2:
   the events check and the resume of 2.4.

`GET /user` gives the token account's login for the author filter (2.2).
Keys in the `github:` block of `.jigz/config.yaml` (`GitHub` type in
`internal/fleetconfig/config.go`, read by `config.LoadGitHub` with the
strict decoder): `questions` (boolean, default `true`) switches the
scanner, its resume and the Questions page for the project;
`github.enrich` switches only run-history enrichment.
`github.questions_interval` is the time between passes (default `2m`,
minimum `1m`); `github.questions_window` is how far back step 2 looks
(default `168h`). Rate use: one search call per project per pass plus one
or two calls per candidate, mostly 304s, which do not count against the
REST limit. The scanner shares the per-token back-off with the enricher.

Two scanners can work on one item (a dashboard and a daemon, or a scanner
and the hub). Every write is idempotent (2.4), so they do not conflict.

### 2.2 Parse rules

A pure package, `internal/questions` (`parse.go`), takes a comment list
(id, author, author association, body, created, URL) and returns
questions, answers and problems. The scanner passes the item's description
as the first entry, flagged as a description, with the item's author, the
author's `author_association` and the item's `created_at` as its time. It
is subject to every rule below, with one exception: its `agent-answer`
blocks are skipped without a problem. Because the time is `created_at`,
an edit of the description does not move the question forward in time;
an answer posted before the edit still answers a question the edit
added. Rules:

- Blocks run from `<!-- agent-question ... -->` to
  `<!-- /agent-question -->`, and likewise for `agent-answer`. Attributes
  are `name="value"` pairs in any order. Question: `id`, `audience`,
  `blocking` required; `resume` required when blocking. Answer: `id`
  required; `by` optional and only informational. `id` matches
  `q-[A-Za-z0-9_-]{1,64}`; `audience` is `product` or `project`;
  `resume` is an `agent:` label other than `agent:done`.
- The text is the `**Question:**` paragraph and the optional
  `**Suggested answer:**` paragraph. Markers inside fenced code blocks are
  ignored.
- Malformed (a problem with a link to its comment, not a question): an
  unknown or missing attribute value, a start without an end, a nested
  start, or a bad id.
- Duplicate question ids: the latest block defines the question; the page
  shows "asked N times". Several answers to one id: the latest is
  current. An answer whose id matches no question is a problem.
- The item's resume trigger is the `resume` of its latest blocking
  question; differing values among the open blocking questions are a
  problem. Each pass parses the current comment bodies.

The parser returns every answer with its author and author association.
The scanner keeps only answers from authorized authors: the token
account, the repository owner, authors with association `OWNER`, `MEMBER`
or `COLLABORATOR`, and the item's assignees. The author is the GitHub
account that posted the comment, or for the description the account that
opened the item; the `by` attribute is not checked. Other
answers do not answer a question; the page shows each as "ignored:
<author>". The filter is in one function (`authorized`) so it can become
configurable. The prompts apply the same set of authors to the comments
agents read (2.8). A stricter, configurable mode is tracked in
kinged007/jigz#20.

### 2.3 Storage

Tables in `internal/store/store.go`, access code in
`internal/store/questions.go`:

- `question_items`: project, number, is_pr, title, url, labels,
  assignees, issue_etag, comments_etag, scanned_at, problems (JSON), and
  the description with its author, author association and creation time
  (body, author, author_association, created_at). One row per scanned
  item. The column `first_labels` is not read or written;
  it stays because SQLite drops a column only by rebuilding the table.
- `questions`: project, number, qid, audience, blocking, resume, question,
  suggested, asked_comment_url, asked_comment_id (0 when the question is
  in the description; then asked_comment_url is the item URL), asked_at, asked_count, answer, answered_by,
  answer_comment_url, answered_at, ignored_answers (JSON list of author
  and comment URL). Primary key (project, number, qid).

The tables are a cache of GitHub: each pass replaces an item's rows. The
daemon keeps its own copy in its store (`store.Open(<state dir>)`).

### 2.4 Resume

The scanner runs these steps for an item that meets the first three
conditions of rule 2:

1. Events check: `GET /repos/<slug>/issues/<n>/events?per_page=100`, all
   pages, with the stored ETag on the first page. Take the `created_at` of
   the latest `labeled` event for `needs-human`. If no authorized answer
   to a blocking question was created strictly after it (both times have
   one-second precision), the item is not ready;
   the page shows "parked again after the last answer" (2.6). If the
   events cannot be read, the pass records the error and changes nothing.
2. Rescan the item's labels and comments without ETags and check rule 2
   again, so the labels are changed on current data.
3. `POST /repos/<slug>/issues/<n>/labels` adds the item's resume trigger
   (2.2), unless the item already carries a trigger.
4. `DELETE .../labels/agent%3Adone` (colon URL-encoded), then
   `DELETE .../labels/needs-human`. A 404 means the label is already gone
   and counts as success.

`agent:done` outranks the trigger and `needs-human` keeps the item out of
the dispatch query, so a failure at any call dispatches nothing. The
error is stored as the item's problem "resume failed: <error>" and the
next pass retries from step 1; a successful resume clears it. Each call
changes only its own label, so another writer's change to other labels
is kept. `PUT .../labels` replaces the whole set and is never used.

`Scanner.OnResume` records each resume: an `a.audit` entry in the
dashboard, a `jigd.log` line in the daemon. The calls use the project's
token, which the loops already use to write labels (Issues: read and
write), through `Post` and `Delete` in `internal/github`. The scanner
never adds `in-progress`.

### 2.5 Scanner in `jig daemon`

`cmd/jig/daemon.go` starts one `Scanner` with:

- Targets: the projects in the hub config that have at least one loop,
  with the tracker token from `hub.json`.
- Search qualifier: `label:runner:<name>` is added to step 1 of 2.1, so
  only items routed to this machine are candidates. Step 2 is skipped.
- Settings: `questions` on, `questions_interval` `2m`, and the API base of
  the fallback search (the issues workflow's `tracker.endpoint`, else
  `https://api.github.com`). The hub config has no `github:` block.
- Store: the daemon's store.

After a resume, the daemon wakes the workflows whose filter labels
(`plan.Workflow.FilterLabels`) contain the added trigger and whose item
kind fits, the same way it handles a fallback search result. The scanner
runs while the daemon is paused; the wake is then ignored, as `pause`
requires (`plans/jigz/connected-mode.md` 2.4).

### 2.6 Questions page

`GET /questions`, a nav entry after Runs in `layout.html`, template
`cmd/jig/templates/questions.html`, handler `cmd/jig/questions.go`. The
page has no form that posts and no route that writes.

- When no registered project has `github.questions` on, the page shows
  only "Questions are off (github.questions: false)".
- The time of the last pass per project and its error, if any; a project
  without a token shows "no GitHub token".
- One row per question: project, item link, title, audience, blocking,
  age, question text, suggested answer, link to the question comment on
  GitHub, the current answer with its author and link, and "ignored:
  <author>" with a link for each unauthorized answer.
- Filters as query parameters: `audience=product|project`,
  `blocking=true|false`, `project=<slug>`, `state=open|answered`.
- Per open question, an answer template in a read-only text area with a
  Copy button (client-side clipboard only) and a link to the item on
  GitHub, where the person posts it:

  ```markdown
  <!-- agent-answer id="<qid>" -->
  <suggested answer, or empty>
  <!-- /agent-answer -->
  ```

- Item problems are listed above the questions with a link to the item
  and the comment: malformed blocks, unknown-id answers, differing
  `resume` values, "resume failed: <error>", and "parked again after the
  last answer". A person fixes them on GitHub (edit the block, post a new
  answer, or change the labels).
- `GET /api/v1/questions` in `cmd/jig/api.go` returns the same data as
  JSON.

There is no refresh route. A rescan button would let anyone who reaches
the dashboard start a pass, which spends the token's rate limit and can
change labels. The page shows when the next pass is due.

### 2.7 Answering

On GitHub, a person posts a comment in the format of 2.6 on the item. The
comment author is checked by the author filter (2.2).

Through the hub, the hub posts the same comment with the logged-in user's
own GitHub token, so the comment author is that user and the same filter
applies. The hub may also run the resume of 2.4 with the same rules; the
calls are idempotent, so a scanner that runs after it changes nothing.

`jig` posts no comments.

### 2.8 Prompts and engine facts

The prompts `plan.md`, `review.md`, `build.md`, `review-fix.md` and
`docs.md` in `cmd/jig/workflows/prompts/` state the block format and
rules 1, 3 and 5, escalate with a blocking block and `needs-human` only,
and read all comments with `gh issue view` or `gh pr view --comments`
before acting. They consider only comments from authorized authors (2.2),
using `gh api ... --jq` with a filter on `author_association`.

Engine facts, read at the `kinged007/sortie` v1.28.1 and
`sortie-ai/sortie` v1.29.0 tags: exit code 0 is recorded `succeeded`
whatever the agent did to labels; at handoff the engine adds
`agent:done` and removes the trigger, `in-progress` and any terminal
label, not `needs-human` (`internal/scm/github/tracker.go:740`,
`internal/issuekit/transition.go:57-86`); `agent:done` outranks a
trigger; an item with a trigger and without `agent:done` is dispatched at
the next poll as attempt 1 with no backoff; poll dispatch does not fetch
comments, so `.issue.comments` is empty on a first run.

### 2.9 Security

- No dashboard route writes to GitHub. The questions routes are
  `GET /questions` and `GET /api/v1/questions`; a `POST` to any
  `/questions/...` path returns 404.
- Authorization is the GitHub identity of the comment author plus the
  author filter (2.2). A person can answer only as their own GitHub
  account, on GitHub or through the hub. kinged007/jigz#20 covers a
  stricter, configurable filter.
- The scanner's writes are label changes only (2.4), made with the token
  the machine already uses for its loops.
- On a public repository anyone can post an `agent-answer`. The scanner
  and the prompts ignore unauthorized answers.

## 3. Work packages

These packages change the implemented Phase 3 code to the design above.
Lanes: `feature`, `tests` (pi, general Go); `ui` (pi, templates and the
page); `complex` (claude opus); `review2` (claude sonnet, read-only);
`mechanical` (small explicit edits). The lead runs the final gate
(`plans/jigz/handoff.md` section 8): `gofmt -l cmd internal` empty,
`go vet ./...` (also with `-tags acceptance`), `go test ./...`,
`go build ./...`, `make acceptance`.

| WP | Lane | Files owned | Depends on |
| -- | ---- | ----------- | ---------- |
| 1 | tests | `internal/fakegithub/*` | none |
| 2 | complex | `internal/collector/questions.go`, `internal/collector/questions_test.go`, `internal/store/questions.go`, `internal/store/questions_test.go`, `internal/config/config.go`, `internal/config/config_test.go`, `internal/fleetconfig/config.go`, `internal/fleetconfig/config_test.go` | 1 |
| 3 | ui | `cmd/jig/questions.go`, `cmd/jig/questions_test.go`, `cmd/jig/questions_page_test.go`, `cmd/jig/templates/questions.html`, `cmd/jig/web.go`, `cmd/jig/api.go`, `cmd/jig/history.go` | 2 |
| 4 | complex | `cmd/jig/daemon.go`, `cmd/jig/daemon_test.go` | 2 |
| 5 | tests | `cmd/jig/acceptance_test.go` | 1; green after 2, 3 |
| 6 | mechanical | `README.md`, `plans/jigz/features.md`, `plans/jigz/handoff.md` | 5 |

Order: WP1, then WP2; WP3 and WP4 in parallel. WP5 is written beside WP2
and fails until WP2 and WP3 land. WP6 last.

**WP1. Fake GitHub.** `GET .../issues/<n>/events` returns the recorded
label events as GitHub does (`labeled`/`unlabeled`, `label.name`,
`created_at`), oldest first, paged with `Link`, with `ETag`/304.
`AddComment` defaults `created_at` to now. A test method adds a label as
a person would. Unit tests for each; `PUT .../labels` stays rejected.

**WP2. Scanner resume.** Step 4 of 2.1 and the resume of 2.4 in `Pass`;
`OnResume` hook; search qualifier and the switch that skips step 2;
"resume failed" and "parked again" problems, replacing the
`LabelsNotRemoved` problem. Remove `Answer`, `AnswerBody`,
`RemoveLabels`, `RemoveTrigger`, `Triggers`, `ErrNotMalformed`,
`ErrNeedTrigger`, `ErrAnswerText`, and the `first_labels` reads and
writes. The `questions` key (2.1) in the `GitHub` struct, default
`true`, accepted by the strict decoder in `config.LoadGitHub`; the
scanner gated on it, not on `enrich`. Tests against `fakegithub`:
rule 2 in all cases (no blocks, one of two answered, all answered,
malformed block, differing `resume`, only an unauthorized answer, no
`needs-human`, labels already removed); the events check both ways; call
order `POST` trigger, `DELETE agent%3Adone`, `DELETE needs-human`, no
`POST` when a trigger is present, 404 as success, no `PUT`, no comment,
no `in-progress`; a failed `DELETE` leaves "resume failed" and the next
pass completes it; two scanners on one item end with the same labels;
no pass for a project with `questions: false`; a pass with `enrich`
off and `questions` on (default); no pass and a "no GitHub token" status
without a token; `OnResume` once per resume. Config tests: default
`true`, `questions: false` parsed, an unknown key still rejected.

**Review pass (review2) on WP2** before WP3 and WP4 start, aimed at
rule 2, the events check, the call order and the author filter.

**WP3. Read-only dashboard.** Remove the routes
`POST /questions/answer`, `/questions/retry`, `/questions/remove-labels`,
`/questions/refresh`, their handlers, the form tokens (`formToken`,
`issue`, `useToken`) and the per-item locks. The page per 2.6 with the
answer template and Copy button, problems, last-pass time; `OnResume`
writes `a.audit` in `history.go`; `Targets` in `history.go` passes each
project's `questions` value, and the enrich-based block selection
(`githubFrom`) stays for enrichment only. Tests:
each removed route returns 404 and `POST /questions` 405; the page has
no `<form method="post">` and no `name="token"`, and shows for an open
question the template with its `qid` and suggested answer, the item link
and the question comment link; each filter, the "Questions are off"
message, the "no GitHub token" status,
problems with links, ignored answers, empty state; `GET
/api/v1/questions` keeps its fields; a Go-side fetch of `/questions` on a
running instance (obscura refuses loopback; Playwright is not installed
in this repo).

**WP4. Daemon scanner.** Per 2.5. Unit tests in `daemon_test.go` with
`fakegithub` and a stub child binary: an answered parked item with the
runner label is resumed and its workflow woken; an item without the
runner label, and an item in a project with no loop, get no write; a
resume while paused writes the labels and wakes nothing; the search
carries `label:runner:<name>`; the token is not logged. **Review pass
(review2) on WP4.**

**WP5. Acceptance test.** Section 4. `make acceptance` passes; the
rewritten scenario fails on `main`.

**WP6. Docs.** README Questions section: answer on GitHub with the
template; the scanner resumes the item; the `github.questions` key
(default `true`) and a token are required, `github.enrich` is not; in
standalone mode the dashboard must run; the daemon scans its
runner-label items; the dashboard writes nothing to GitHub. `features.md` entries
marked existing after WP5 passes; handoff update; final review2 pass over
the whole diff before the PR.

## 4. Acceptance test

Scenario `f_blocking_question_stops_and_answer_resumes` in
`TestAcceptance` (`cmd/jig/acceptance_test.go`). The mock agent writes no
comments and edits no labels, so the test adds through the fake the
comments and labels that agents and people write. The test's config
has `github.enrich` off, `github.questions` unset (default `true`),
`questions_interval: 1m` and `github.api_url` at the fake.

1. Seed issue A: label `agent:plan`, assigned to the token owner, one
   comment with a blocking `agent-question` (id `q-1`, audience
   `product`, `resume="agent:plan"`).
2. A is dispatched (`+in-progress`), the run succeeds, and the engine
   writes `+agent:done`, `-agent:plan` and `-in-progress`. The test adds
   `needs-human`. A has `needs-human` and `agent:done`, no `agent:plan`.
3. Seed issue B: labels `agent:plan`, `agent:done`, `needs-human`, a
   comment with no question block.
4. Seed issue C: labels `agent:done`; a comment with blocking question
   `q-2` (`resume="agent:plan"`); an answer to `q-2` from `alice`
   (association `COLLABORATOR`); then, at least two seconds later, the
   test adds `needs-human`.
5. Dispatch stops: over at least three polls, no new `+in-progress`
   label event on A, B or C.
6. `GET /api/v1/questions` (polled until the first pass) lists `q-1` on A
   as open and blocking, nothing for B, `q-2` on C as answered, and the
   "parked again" problem on C.
7. Unauthorized answer: a comment on A from `mallory` (association
   `NONE`) with `<!-- agent-answer id="q-1" -->`. After the next pass the
   API shows "ignored: mallory" on `q-1`; no label request for A.
8. Authorized answer: a comment on A from `alice` (association
   `COLLABORATOR`) answering `q-1`.
9. Within two passes (150 s), the fake shows for A one `POST` adding
   `agent:plan`, then one `DELETE` each for `agent:done` and
   `needs-human`, in that order, their events, and no label `PUT`. No
   comment on A was posted with the token. A has `agent:plan` and neither
   of the other two.
10. Resume: A gets `+in-progress` and reaches its handoff again
    (`+agent:done`). `GET /api/v1/runs` lists a second `succeeded` run of
    A with `attempt` 1.
11. B and C keep `needs-human` and `agent:done`; no label request for B
    or C.
12. `Unexpected()` is empty.

`TestAcceptanceConnected` is unchanged; the daemon scanner is covered by
the WP4 unit tests.

## 5. Risks and open points

Risks:

- A parked item is resumed only while a scanner runs: a dashboard with
  `github.questions` on and a token, or `jig daemon`. With
  `questions: false` or `jig --no-dashboard`, a person changes the labels
  on GitHub.
- Resume time is up to one `questions_interval` (2 minutes by default)
  after the answer. The acceptance scenario takes about two minutes
  longer.
- A scanner that stalls between its rescan (2.4 step 2) and its label
  calls for longer than a whole run (another resume, run and new parking)
  resumes an item with a new unanswered question. The calls follow the
  rescan directly.
- Answers added by editing an older comment have that comment's
  `created_at` and do not pass the events check; a person posts a new
  comment instead. The README states it.
- An agent sees an answer only if its prompt reads comments through
  `gh`. The mock agent runs no `gh`; `embed_test.go` checks the prompts.

Later: questions in PR review bodies and inline review comments, after
the issue path works.
