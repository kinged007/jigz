# Jigz: Phase 3 plan (Questions)

Status: draft for discussion (2026-10-23)

Implements Phase 3 of `plans/jigz/vision-and-roadmap.md` (section 5.3).
Work is verified as `plans/system/delivery-method.md` section 2.1
describes: the acceptance test in section 4 is written before the build,
and a lane other than the builder reviews each package.

## 1. Scope and rules

Agents ask questions in `agent-question` blocks and read answers from
`agent-answer` blocks (`plans/system/hub-protocol.md` section 4): HTML
comment markers inside an issue or PR comment (sample: kinged007/jigz#14).
The dashboard lists open questions and posts answers through the answer
path (2.5); the hub follows the same rules.

Phase 3 reads issue and PR conversation comments only (section 5,
"Later"). The dashboard has no authentication: anyone who can reach it
can post answers as the token account; `sameOrigin` stops only
cross-site form posts (README task in WP8). Engine behaviour follows the
pinned engines: `kinged007/sortie` v1.28.1 and, in the upstream
acceptance test, `sortie-ai/sortie` v1.29.0 (WP0).

A blocking question uses the existing label `needs-human`, which both
shipped workflows already exclude from dispatch
(`cmd/jig/workflows/WORKFLOW.issues.md` line 11, `WORKFLOW.prs.md`
line 12). A trigger label is an `agent:*` label other than `agent:done`.

1. Every escalation to `needs-human` that waits for an answer carries at
   least one blocking `agent-question` block in its comment. An escalation
   with no question (for example, a PR for a person to review) has none.
2. The answer path removes both `needs-human` and `agent:done` when the
   item has at least one blocking `agent-question` and every blocking
   question has an answer from an authorized author (2.2). It never
   removes either label on an item with no blocking question.
3. An escalating agent posts blocking question blocks, each with
   `resume="<trigger>"`, and adds `needs-human`; the run is `succeeded`.
   At handoff the engine adds `agent:done` and removes every other state
   label (the trigger, `in-progress`, any terminal label); `needs-human`
   stays. A parked item has `needs-human` and `agent:done`, no trigger.
4. The answer path removes the two labels and adds the `resume` trigger
   (2.5) unless a trigger is present; it never adds `in-progress`. The
   next poll dispatches a new run (attempt 1). The dashboard's first scan
   can come after the engine removed the trigger, so the block records it.
5. A non-blocking question does not stop the work: the agent proceeds with
   its suggested answer and records the assumption.

## 2. Design

### 2.1 Finding open questions

The dashboard reads GitHub only in `internal/collector/enrich.go`, through
the GET-only client in `internal/github/github.go`, when `github.enrich`
is set; it never reads issue comments. Each project uses its own tracker
token (`collector.Target`, built in `cmd/jig/history.go`). A new scanner,
`internal/collector/questions.go`, runs beside the enricher with the same
client, `Target` list and `github.api_url`, and also only when
`github.enrich` is set. One pass per project:

1. `GET /search/issues?q=repo:<slug> state:open label:needs-human` (one
   call; covers issues and PRs). These items are candidates.
2. Items with a run in the store within `questions_window` are also
   candidates, so non-blocking questions on items that are not parked are
   found. For a candidate not in the search result, `GET
   /repos/<slug>/issues/<n>` with the stored ETag reads its labels and
   assignees.
3. For each candidate: `GET /repos/<slug>/issues/<n>/comments?per_page=100`
   with the stored ETag. PR conversation comments use the same route.
   Review comments and review bodies are not read.

The scanner reads the token account's login once per token with
`GET /user`, for the author filter (2.2) and the answer path. Two keys in
the `github:` block of the fleet config (`GitHub` type in
`internal/fleetconfig/config.go`) are Go durations validated like
`github.interval`: `questions_interval`, the time between passes
(default `2m`, minimum `1m`), and `questions_window`, how far back step 2
looks (default `168h`, 7 days). A pass also runs on the page's Refresh
button and before every answer is posted. Rate use: one search call per
project per pass (limit 30 per minute per token) plus one or two calls
per candidate; a 304 does not count against the REST limit. 10 parked
items use about 330 requests per hour of a token's 5000 at the default
interval. The scanner shares the per-token back-off with the enricher.

### 2.2 Parse rules

A pure package, `internal/questions` (`parse.go`), takes a comment list
(id, author, author association, body, created, URL) and returns
questions, answers and problems. Rules:

- A question block starts with `<!-- agent-question ... -->` and ends with
  `<!-- /agent-question -->`. An answer block uses `agent-answer`.
- Attributes are `name="value"` pairs in any order. Question: `id`,
  `audience`, `blocking` required; `resume` required when blocking,
  optional otherwise. Answer: `id` required, `by` optional. `id` matches
  `q-[A-Za-z0-9_-]{1,64}`; `audience` is `product` or `project`;
  `blocking` is `true` or `false`; `resume` is an `agent:` label other
  than `agent:done`.
- The text is the `**Question:**` paragraph (required) and the
  `**Suggested answer:**` paragraph (optional).
- Markers inside fenced code blocks are ignored, so a comment that quotes
  the format does not create a question.
- Malformed: an unknown or missing attribute value (including a blocking
  question without `resume`), a start without an end, a nested start, or
  a bad id. A malformed block is a problem with a link to its comment,
  not a question. The answer path never removes labels on an item with
  any malformed question block (rule 2 cannot be checked).
- Duplicate question ids on one item: the latest block (by comment order)
  defines the text, audience, blocking flag and `resume`; the page shows
  "asked N times". One answer with that id answers it.
- The item's resume trigger is the `resume` of its latest blocking
  question. If the open blocking questions name different values, that is
  a problem and the answer path removes no labels.
- Several answers to one id: the latest is current; all are shown. An
  answer whose id matches no question is reported as a problem only.
- Edited and deleted comments: each pass parses the current bodies.

The parser returns every answer with its author and author association.
Its caller, the scanner, keeps only answers from authorized authors: the
token account (2.1), the repository owner, authors with association
`OWNER`, `MEMBER` or `COLLABORATOR`, and the item's assignees. Other
answers do not answer a question; the page shows each as "ignored:
<author>". The filter is in one place so it can become configurable. A
stricter mode (token account only) and the same filter for what prompts
read are tracked in kinged007/jigz#20.

### 2.3 Storage

A new migration in `internal/store/store.go` adds two tables, with access
code in `internal/store/questions.go`:

- `question_items`: project, number, is_pr, title, url, labels,
  assignees, first_labels, issue_etag, comments_etag, scanned_at,
  problems (JSON). One row per scanned item.
- `questions`: project, number, qid, audience, blocking, resume, question,
  suggested, asked_comment_url, asked_at, asked_count, answer, answered_by,
  answer_comment_url, answered_at, ignored_answers (JSON list of author
  and comment URL). Primary key (project, number, qid).

`question_items.first_labels` (JSON list) holds the item's labels from
the first pass that finds a question on it; later passes do not change
it. It is the fallback for an old blocking block without `resume` (2.4)
and may already lack the trigger. The rest is a cache of GitHub: each
pass replaces an item's rows (`github_cache` holds one row per item).

### 2.4 Questions page

`GET /questions`, a nav entry after Runs in `layout.html`, template
`cmd/jig/templates/questions.html`, handler `cmd/jig/questions.go`.

- When `github.enrich` is off, the page shows only "Set github.enrich to
  see questions".
- One row per open question: project, item link, title, audience,
  blocking, age, question text, suggested answer, link to the comment,
  and "ignored: <author>" for each unauthorized answer (2.2).
- Filters as query parameters: `audience=product|project`,
  `blocking=true|false`, `project=<slug>`, `state=open|answered`.
- Answer form per question: an empty text area, a button "Use suggested
  answer" that fills it, and Submit; it carries project, number, qid and
  a one-time token.
- Item problems (malformed blocks, unknown-id answers, labels not
  removed, differing `resume` values) are listed above, with the link.
- An item with a malformed question block has a button "Remove labels".
  After a confirmation, `POST /questions/remove-labels` runs the label
  calls of 2.5 step 4 with the answer path's checks (`sameOrigin`,
  one-time token, per-item mutex); `a.audit` records it. It adds a valid
  `resume`, else the trigger in `first_labels`, else one the person picks
  in the confirmation from the project's `agent:*` labels.
- `GET /api/v1/questions` in `cmd/jig/api.go` returns the same data as
  JSON (used by the acceptance test).

### 2.5 Answer write path

`POST /questions/answer`, behind the existing `sameOrigin` check in
`cmd/jig/web.go`, recorded with `a.audit`.

1. Check the one-time token. A used token returns the first submit's
   result (redirect to the same notice), so a double click or a
   resubmitted form posts once. A per-item mutex serialises answers.
2. Rescan the item's comments. If the question is gone, show "question no
   longer exists". If it already has an answer, show it and ask the user
   to confirm a second answer.
3. `POST /repos/<slug>/issues/<n>/comments` with
   `<!-- agent-answer id="<qid>" by="<login>" -->` (the token account's
   login, 2.1), the text, and `<!-- /agent-answer -->`. The server adds
   the markers. A text that contains `<!-- ` is rejected so it cannot
   close or open a block.
4. Rescan the item's comments and labels and apply rule 2. If it holds,
   send `DELETE /repos/<slug>/issues/<n>/labels/needs-human`, then
   `DELETE .../labels/agent%3Adone` (colon URL-encoded); a 404 means the
   label is already gone and counts as success. Then `POST .../labels`
   adds the item's resume trigger (2.2), unless the item already carries
   a trigger (a person added one). Each call changes only its own label,
   so another writer's change to other labels is kept. GitHub has no
   PATCH for labels; `PUT .../labels` replaces the whole set and is not
   used.
5. If step 3 fails nothing changed; the form is shown again with the
   text and the error. If step 4 fails, the page says "answer posted,
   labels not removed" with the error and a Retry button for step 4 only.

`internal/github/github.go` gains `Post` and `Delete` beside `Get`, using
the same headers, back-off and error rules; the package comment changes
from read-only. The token is the project's tracker token, which the
engine already uses to write labels and comments, so no new scope is
needed (a fine-grained token needs Issues: read and write).

### 2.6 Prompt changes

Files: `cmd/jig/workflows/prompts/plan.md`, `review.md`, `build.md`,
`review-fix.md`, `docs.md`. `merge.md` and `quick.md` escalate for a
person to review, not for an answer, and get no block.

- Each of the five gets a short "Questions" section with the block format,
  the audience values, and rules 1, 3 and 5.
- Where the prompt escalates for an answer, it writes at least one
  blocking block with `resume`, then adds only `needs-human`
  (`gh issue edit <n> --add-label needs-human`). The engine's handoff
  then adds `agent:done` and removes the trigger and `in-progress`; the
  result is the same if the agent moves the item to `agent:done` first.
- `resume` is the trigger of the prompt's dispatch rule, as found in
  `{{ .issue.labels }}` (the labels at dispatch): `agent:plan` or
  `agent:plan-build` in `plan.md`, `agent:build` in `build.md` (always,
  also in the `build-chain` stage) and `review-fix.md`, `agent:docs` in
  `docs.md`, `agent:review` in `review.md`. The routing comment
  in `WORKFLOW.prs.md` is corrected to this handoff behaviour.
- Before acting, the prompt reads all comments with
  `gh issue view <n> --comments` or `gh pr view <n> --comments` (or
  `gh api repos/<slug>/issues/<n>/comments --paginate`), not from
  `.issue.comments`, which is empty on a first run. It applies each
  `agent-answer` and treats an unanswered non-blocking question as
  answered with its suggestion.
- An installed project gets the new prompts by re-running `setup`; the
  existing drift warning in `setup` reports the difference.

## 3. Work packages

Lanes: `feature`, `tests` (pi, general Go); `ui` (pi, templates and the
page); `complex` (claude opus); `review2` (claude sonnet, read-only);
`mechanical` (small explicit edits). Every brief names its files and
includes a page check where a page changes (Go-side HTTP fetches; obscura
refuses loopback and Playwright is not installed in this repo). The lead
runs the final gate, since parallel workers can break each other's
`go test` (`plans/jigz/handoff.md` section 8). Checks: `gofmt -l cmd
internal` empty, `go vet ./...` (also with `-tags acceptance`),
`go test ./...`, `go build ./...`.

| WP | Lane | Files owned | Depends on |
| -- | ---- | ----------- | ---------- |
| 0 | complex (read-only), done | none | none |
| 1 | complex | `internal/questions/*` | 0 |
| 2 | feature | `internal/github/*`, `internal/store/store.go`, `internal/store/questions.go`, `internal/store/questions_test.go`, `internal/fleetconfig/config.go`, `internal/fleetconfig/config_test.go` | 0 |
| 3 | tests | `internal/fakegithub/*` | 0 |
| 4 | complex | `internal/collector/questions.go`, `internal/collector/questions_test.go`, `cmd/jig/questions.go`, `cmd/jig/questions_test.go`, `cmd/jig/history.go`, `cmd/jig/api.go`, `cmd/jig/web.go` | 1, 2, 3 + review |
| 5 | ui | `cmd/jig/templates/questions.html`, `cmd/jig/templates/layout.html`, `cmd/jig/templates/icons.html`, `cmd/jig/questions_page_test.go` | 4 |
| 6 | feature | the five prompt files, `cmd/jig/workflows/WORKFLOW.prs.md`, `cmd/jig/embed_test.go` | 0 |
| 7 | tests | `cmd/jig/acceptance_test.go` | 3; runs green after 4, 5 |
| 8 | mechanical | `README.md`, `plans/jigz/features.md`, `plans/jigz/handoff.md` | 7 |

WP1, WP2, WP3 and WP6 run in parallel. WP7 is written in parallel with
WP4 (test before build) and must fail until WP4 lands.

**WP0. Engine research. Done.** Read at the `kinged007/sortie` v1.28.1
and `sortie-ai/sortie` v1.29.0 tags. Items marked "inferred" were not
read at the tags. Findings:

1. Exit code 0 is recorded `succeeded`, whatever the agent did to
   labels; a parked item is not a failed run. Withheld handoff evidence
   forces `failed`; `handoff_evidence` is off in the shipped workflows.
   `needs_person` is set only when the agent asks for input through the
   agent protocol (`ErrTurnInputRequired`), not by a label.
2. At handoff the engine adds `agent:done`, then removes every other
   state label: all active labels (the trigger, and `in-progress`, which
   is in `active_states`) and any terminal label. Other labels, such as
   `needs-human`, are not touched (`internal/scm/github/tracker.go:740`,
   `internal/issuekit/transition.go:57-86`).
3. The handoff state outranks active states: an item with a trigger and
   `agent:done` is in state `agent:done` and is not dispatched.
4. Claims are in memory, released at a successful handoff; backoff is for
   failure retries. Inferred: a stored handoff record never blocks dispatch.
5. An item with a trigger and without `agent:done` is dispatched at the
   next poll as a new run (attempt 1), with no backoff.
6. No engine setting blocks dispatch by label other than `query_filter`
   and `active_states`; a query exclusion cancels nothing. Moving an item
   to the handoff state while its worker runs cancels the worker at once
   (status `cancelled`, not failed).
7. Poll dispatch does not fetch comments; `.issue.comments` is empty on
   a first run. Inferred: a retry fetches all comment pages. The rendered
   prompt is stored nowhere.

**WP1. Parser.** `internal/questions/parse.go` and table tests for every
rule in 2.2, including fenced code, nested and unterminated blocks,
duplicate ids, unknown-id answers, attribute order, and the sample from
kinged007/jigz#14. Acceptance: tests pass; no I/O in the package.
**Review pass (review2) on WP1 before WP4 starts.**

**WP2. GitHub writes, storage and config.** `Post` and `Delete` in
`internal/github` with tests on an `httptest` server (headers, back-off,
404 and 422 errors, token never in error text). Migration and store
functions for 2.3 with tests (replace an item's rows, `first_labels`
kept across passes, list with filters, schema version bump). The two
config keys of 2.1 with default and validation tests. Acceptance: tests
pass; existing store and config tests pass.

**WP3. Fake GitHub.** The list in section 4, with unit tests for each
addition; unknown routes, parameters and qualifiers are still rejected.
**Review pass (review2) on WP2 and WP3 together before WP4 starts.**

**WP4. Scanner and answer path.** Scanner per 2.1 and the author filter
per 2.2, wiring in `cmd/jig/history.go`, handlers and the answer path
per 2.5, the remove-labels handler per 2.4, JSON API. Tests against
`fakegithub`: rule 2 in all cases (no blocks, one of two answered, all
answered, malformed block, labels already removed); two `DELETE` calls,
404 as success, no `PUT`; trigger added from `resume`, not when one is
present; differing `resume` values remove no labels; `in-progress` never
added; unauthorized answers ignored; `by` is the token login; no scan
with `github.enrich` off; double submit posts once; comment failure
changes nothing; label failure is retryable; remove-labels only on an
item with a malformed block, falling back to `first_labels`, then the
person's choice; audit entries written.
**Review pass (review2) on WP4 before WP5 starts**, aimed at rule 2, the
author filter, the label calls and the double-submit handling.

**WP5. Page.** Template, nav, filters, answer form, problems list,
ignored answers, "Remove labels" with its confirmation, the
`github.enrich` message, empty state. Render tests for each filter, an
answer notice and the `github.enrich` message; a Go-side fetch of
`/questions` on a running instance.

**WP6. Prompts.** Changes per 2.6. Acceptance: `embed_test.go` checks
that each of the five prompts contains the block format with `resume`,
the escalation adds `needs-human`, no prompt relies on keeping its
trigger while parked, and the prompt reads comments with
`gh issue view` or `gh pr view` and `--comments`; `merge.md` and
`quick.md` are unchanged. A review pass (review2) checks the five prompts
for consistency with `hub-protocol.md` section 4.

**WP7. Acceptance test.** Section 4. `make acceptance` passes; the new scenario fails on `main`.

**WP8. Docs.** README label table; a Questions section with the
`github.enrich` requirement and the two config keys; an access note: the
dashboard has no authentication and binds to 127.0.0.1 by default;
opening it to other hosts is the user's decision; use a private network
or a reverse proxy with authentication. Mark Phase 3 items in
`features.md` as existing after WP7 passes; handoff update; final
review2 pass over the whole diff before the PR.

## 4. Acceptance test

A new scenario in `TestAcceptance` (`cmd/jig/acceptance_test.go`),
`f_blocking_question_stops_and_answer_resumes`. The mock agent writes no
comments and edits no labels, so the test adds the comment and the
`needs-human` label that an agent writes. The test's dashboard has
`github.enrich` set and `github.api_url` at the fake.

1. Seed issue A: label `agent:plan`, assigned to the token owner, and one
   comment with a blocking `agent-question` (id `q-1`, audience
   `product`, `resume="agent:plan"`).
2. A is dispatched (`+in-progress`), the run succeeds, and the engine
   writes `+agent:done`, `-agent:plan` and `-in-progress`. The test then
   adds `needs-human` through the fake. A has `needs-human` and
   `agent:done` and no `agent:plan`.
3. Seed issue B: labels `agent:plan`, `agent:done`, `needs-human`, a
   comment with no question block.
4. Dispatch stops: over at least three polls, no new `+in-progress`
   label event on A or B.
5. `POST /questions/refresh`; `GET /api/v1/questions` lists `q-1` on A as
   open and blocking, and nothing for B.
6. Answer: fetch `/questions`, read the form token, `POST
   /questions/answer` for `q-1`; submit the same form a second time.
7. The fake shows on A one new comment containing `<!-- agent-answer
   id="q-1" by="<token login>"`, one `DELETE` each for `needs-human` and
   `agent:done`, one `POST` adding `agent:plan`, their events, and no
   label `PUT`. A has `agent:plan` and neither of the other two.
8. Resume: A gets `+in-progress` and reaches its handoff again
   (`+agent:done`). `GET /api/v1/runs` lists a second `succeeded` run of
   A with `attempt` 1.
9. B keeps `needs-human` and `agent:done`; no label write for B.
10. `Unexpected()` is empty.

The fake must support, in addition to what it has:

- Comments with any author, a seeded `author_association`, `updated_at`
  and `html_url`; `ETag` on the comments list and 304 on a matching
  `If-None-Match`.
- Assignees on a seeded issue, in search results and in
  `GET /repos/<owner>/<repo>/issues/<n>` (`ETag`, 304). `GET /user`
  returns the token account's login.
- Existing and unchanged: `POST .../labels` adds labels; `DELETE
  .../labels/<name>` removes one, 404 when absent; both record events.
  The engine's handoff removals use this `DELETE` route, so the fake
  needs no addition for them; a call on another route is rejected and
  fails step 10. Search with `repo:`, `state:open`, `label:`; any other
  route, including `PUT .../labels`, is rejected.

## 5. Risks and open questions

Risks:

- WP0 items marked "inferred" were not read at the tags; the acceptance
  test on the pinned engines covers section 4 steps 2, 4 and 8.
- An item with malformed blocks stays parked until a person removes the
  labels ("Remove labels" in 2.4, or on GitHub). An old blocking block
  without `resume` is malformed; its item relies on `first_labels`, which
  may already lack the trigger; then the page asks the person to choose
  the trigger from the project's `agent:*` labels before removing labels.
- On a public repository anyone can post an `agent-answer`. The
  dashboard ignores unauthorized answers (2.2); agents read every answer
  until kinged007/jigz#20 is done.
- An agent sees an answer only if its prompt reads comments through
  `gh`. The mock agent runs no `gh`; WP6 checks the prompts instead.

Open questions for the repo owner:

None.

Later: questions in PR review bodies and inline review comments, after
the issue path works.
