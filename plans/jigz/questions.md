# Jigz: Phase 3 plan (Questions)

Status: draft for discussion (2026-10-23)

Implements Phase 3 of `plans/jigz/vision-and-roadmap.md` (section 5.3).
Work is verified as `plans/system/delivery-method.md` section 2.1
describes: the acceptance test in section 4 is written before the build,
and a lane other than the builder reviews each package.

## 1. Scope and rules

Agents ask questions in `agent-question` blocks and read answers from
`agent-answer` blocks (`plans/system/hub-protocol.md` section 4). Both are
HTML comment markers inside an issue or PR comment, not labels. A sample
is in GitHub issue kinged007/jigz#14. The dashboard lists open questions
and posts answers through the answer path (section 2.5); the hub follows
the same rules.

A blocking question uses the existing label `needs-human`, which both
shipped workflows already exclude from dispatch
(`cmd/jig/workflows/WORKFLOW.issues.md` line 11, `WORKFLOW.prs.md`
line 12). A trigger label is an `agent:*` label other than `agent:done`.

1. Every escalation to `needs-human` that waits for an answer carries at
   least one blocking `agent-question` block in its comment. An escalation
   with no question (for example, a PR that a person must review) has no
   block.
2. The answer path removes both `needs-human` and `agent:done` in one
   operation when the item has at least one blocking `agent-question` and
   every blocking question has an `agent-answer`. It never removes either
   label on an item with no blocking question.
3. An agent that escalates with a blocking question keeps its trigger,
   posts the comment with the question blocks, and adds `needs-human`.
   The run is recorded `succeeded`, and the engine adds `agent:done` on
   exit. Once both labels are removed, the next poll dispatches the item
   as a new run (attempt 1).
4. If the item has no trigger, the answer path restores it from the
   labels stored at first sight (2.3). If neither shows a trigger, it
   posts the answer, removes the two labels, and reports that the item
   needs a trigger label to resume. It never adds `in-progress`.
5. A non-blocking question does not stop the work: the agent proceeds with
   its suggested answer and records the assumption.

## 2. Design

### 2.1 Finding open questions

Today the dashboard reads GitHub only in `internal/collector/enrich.go`,
through the GET-only client in `internal/github/github.go`. Enrichment is
off unless `github.enrich` is set and never reads issue comments. Each
project uses its own tracker token (`collector.Target`, built in
`cmd/jig/history.go`).

A new scanner, `internal/collector/questions.go`, runs in the dashboard
process beside the enricher and uses the same client, `Target` list and
`github.api_url`. One pass per project:

1. `GET /search/issues?q=repo:<slug> state:open label:needs-human` (one
   call; covers issues and PRs). These items are candidates.
2. Items with a run in the last 7 days (from the run history in the store)
   are also candidates, so non-blocking questions on items that are not
   parked are found.
3. For each candidate: `GET /repos/<slug>/issues/<n>/comments?per_page=100`
   with the stored ETag. PR conversation comments use the same route.
   Review comments and review bodies are not read (open question 4).

Cadence: a pass every 2 minutes, on the page's Refresh button, and
before every answer is posted. Rate use: one search call per project per
pass (limit 30 per minute per token) plus one comments call per
candidate; a 304 does not count against the REST limit. 10 parked items
use about 330 requests per hour of a token's 5000. The scanner shares the
client's per-token back-off with the enricher. Whether the scanner runs
when `github.enrich` is off is open question 1.

### 2.2 Parse rules

A pure package, `internal/questions` (`parse.go`), takes a comment list
(id, author, author association, body, created, URL) and returns
questions, answers and problems. Rules:

- A question block starts with `<!-- agent-question ... -->` and ends with
  `<!-- /agent-question -->`. An answer block uses `agent-answer`.
- Attributes are `name="value"` pairs in any order. Question: `id`,
  `audience`, `blocking` required. Answer: `id` required, `by` optional.
  `id` matches `q-[A-Za-z0-9_-]{1,64}`; `audience` is `product` or
  `project`; `blocking` is `true` or `false`.
- The text is the `**Question:**` paragraph (required) and the
  `**Suggested answer:**` paragraph (optional).
- Markers inside fenced code blocks are ignored, so a comment that quotes
  the format does not create a question.
- Malformed: an unknown or missing attribute value, a start without an
  end, a nested start, or a bad id. A malformed block is reported as a
  problem with a link to its comment. It is not a question. The answer
  path never removes labels on an item with any malformed question block
  (rule 2 then cannot be checked), and the page says why.
- Duplicate question ids on one item: the latest block (by comment order)
  defines the text, audience and blocking flag; the page shows "asked N
  times". One answer with that id answers it.
- Several answers to one id: the latest is current; all are shown. An
  answer whose id matches no question is reported as a problem only.
- Edited and deleted comments: each pass parses the current bodies.

Whose answers count is open question 3. The parser keeps the author
association so the rule can be applied in one place.

### 2.3 Storage

A new migration in `internal/store/store.go` adds two tables, with access
code in `internal/store/questions.go`:

- `question_items`: project, number, is_pr, title, url, labels,
  first_labels, comments_etag, scanned_at, problems (JSON). One row per
  scanned item.
- `questions`: project, number, qid, audience, blocking, question,
  suggested, asked_comment_url, asked_at, asked_count, answer, answered_by,
  answer_comment_url, answered_at. Primary key (project, number, qid).

`question_items.first_labels` (JSON list) holds the item's labels from
the first pass that finds a question on it; later passes do not change
it. The answer path reads the trigger from it (rule 4). Apart from
`first_labels`, the tables are a cache of GitHub: each pass replaces an
item's rows. `github_cache` holds one row per item, so questions get
their own table.

### 2.4 Questions page

`GET /questions`, a nav entry after Runs in
`cmd/jig/templates/layout.html`, template
`cmd/jig/templates/questions.html`, handler `cmd/jig/questions.go`.

- One row per open question: project, item link, title, audience,
  blocking, age, question text, suggested answer, link to the comment.
- Filters as query parameters: `audience=product|project`,
  `blocking=true|false`, `project=<slug>`, `state=open|answered`.
- Answer form per question: an empty text area, a button "Use suggested
  answer" that fills it, and Submit; it carries project, number, qid and
  a one-time token.
- Item problems (malformed blocks, unknown-id answers, labels not
  removed, no trigger to resume) are listed above, with the item link.
- `GET /api/v1/questions` in `cmd/jig/api.go` returns the same data as
  JSON (used by the acceptance test).

### 2.5 Answer write path

`POST /questions/answer`, behind the existing `sameOrigin` check in
`cmd/jig/web.go`, recorded with `a.audit`.

1. Check the one-time token. A token already used returns the result of
   the first submit (redirect to the same notice), so a double click or a
   resubmitted form posts once. A per-item mutex serialises answers to
   one item.
2. Rescan the item's comments. If the question is gone, show "question no
   longer exists". If it already has an answer, show it and ask the user
   to confirm a second answer.
3. `POST /repos/<slug>/issues/<n>/comments` with
   `<!-- agent-answer id="<qid>" by="<by>" -->`, the text, and
   `<!-- /agent-answer -->`. The text is posted as typed; the markers
   are added by the server. A text that contains `<!-- ` is rejected so it
   cannot close or open a block.
4. Rescan the item's comments and labels and apply rule 2. If it holds,
   send the current labels without `needs-human` and `agent:done`, plus
   the triggers from `first_labels` if none is present (rule 4), in one
   `PUT /repos/<slug>/issues/<n>/labels`. If that set equals the current
   set, nothing is sent and the step succeeds.
5. Failures: if step 3 fails nothing changed; the form is shown again
   with the text and the error. If step 4 fails the answer is posted; the
   page says "answer posted, labels not removed" with the error and a
   Retry button that repeats step 4 only.

`internal/github/github.go` gains `Post` and `Put` beside `Get`, using
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
  blocking block, keeps its trigger, and adds only `needs-human`
  (`gh issue edit <n> --add-label needs-human`).
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
`mechanical` (small explicit edits). Every brief names its files, includes
a page check where a page changes (Go-side HTTP fetches; obscura refuses
loopback and Playwright is not installed in this repo), and the lead runs
the final gate because parallel workers can break each other's
`go test` (`plans/jigz/handoff.md` section 8).

Checks: `gofmt -l cmd internal` empty, `go vet ./...` (also with
`-tags acceptance`), `go test ./...`, `go build ./...`.

| WP | Lane | Files owned | Depends on |
| -- | ---- | ----------- | ---------- |
| 0 | complex (read-only), done | none | none |
| 1 | complex | `internal/questions/*` | 0 |
| 2 | feature | `internal/github/*`, `internal/store/store.go`, `internal/store/questions.go`, `internal/store/questions_test.go` | 0 |
| 3 | tests | `internal/fakegithub/*` | 0 |
| 4 | complex | `internal/collector/questions.go`, `internal/collector/questions_test.go`, `cmd/jig/questions.go`, `cmd/jig/questions_test.go`, `cmd/jig/history.go`, `cmd/jig/api.go`, `cmd/jig/web.go` | 1, 2, 3 + review |
| 5 | ui | `cmd/jig/templates/questions.html`, `cmd/jig/templates/layout.html`, `cmd/jig/templates/icons.html`, `cmd/jig/questions_page_test.go` | 4 |
| 6 | feature | the five prompt files, `cmd/jig/embed_test.go` | 0 |
| 7 | tests | `cmd/jig/acceptance_test.go` | 3; runs green after 4, 5 |
| 8 | mechanical | `README.md`, `plans/jigz/features.md`, `plans/jigz/handoff.md` | 7 |

WP1, WP2, WP3 and WP6 run in parallel. WP7 is written in parallel with
WP4 (test before build) and must fail until WP4 lands.

**WP0. Engine research. Done.** Read at sortie upstream `main` commit
`b0425be9`, which lies between v1.28.1 and v1.29.0. Behaviour at the
pinned tags is inferred from that checkout, not read. Findings:

1. Exit code 0 is recorded `succeeded`, whatever the agent did to
   labels; a parked item is not a failed run. `needs_person` is set only
   when the agent asks for input through the agent protocol
   (`ErrTurnInputRequired`), not by a label or `.sortie/status`, and is
   counted as stopped.
2. On a clean exit, if an active label (`active_states`: the trigger or
   `in-progress`) remains, the engine writes `agent:done` by issue id, so
   `-label:needs-human` in the query does not prevent it. With no active
   label it skips the handoff, releases the claim and schedules no retry.
3. The handoff state outranks active states: trigger plus `agent:done`
   is state `agent:done`, which is not dispatched.
4. Claims are in memory and released at a successful handoff; the stored
   handoff record does not gate dispatch; backoff is for failure retries.
5. With an active label present and `agent:done` absent, the next poll
   dispatches the item as a new run (attempt 1), with no backoff.
6. A query exclusion cancels nothing. During a run the engine re-reads
   the issue by id and keeps an item still in an active state. An item
   that leaves all active states is cancelled (`cancelled`) after two
   consecutive readings.
7. Poll dispatch does not fetch comments; `.issue.comments` is empty on
   a first run. A retry fetches all comment pages. The rendered prompt is
   stored nowhere.

**WP1. Parser.** `internal/questions/parse.go` and table tests for every
rule in 2.2, including fenced code, nested and unterminated blocks,
duplicate ids, unknown-id answers, attribute order, and the sample from
kinged007/jigz#14. Acceptance: tests pass; no I/O in the package.
**Review pass (review2) on WP1 before WP4 starts.**

**WP2. GitHub writes and storage.** `Post` and `Put` in
`internal/github` with tests on an `httptest` server (headers, back-off,
404 and 422 errors, token never in error text). Migration and store
functions for 2.3 with tests (replace an item's rows, `first_labels`
kept across passes, list with filters, schema version bump). Acceptance:
tests pass; existing store tests pass.

**WP3. Fake GitHub.** See section 4 for the list. Acceptance: unit tests
in `internal/fakegithub` for each addition; unknown routes, parameters
and qualifiers are still rejected.
**Review pass (review2) on WP2 and WP3 together before WP4 starts.**

**WP4. Scanner and answer path.** Scanner per 2.1, wiring in
`cmd/jig/history.go`, handlers and the answer path per 2.5, JSON API.
Tests against `fakegithub`: rule 2 in all cases (no blocks, one of two
answered, all answered, malformed block, labels already removed); one
`PUT` removes both labels; trigger restored from `first_labels`; missing
trigger reported; `in-progress` never added; double submit posts once;
comment failure changes nothing; label failure is retryable; audit
entry written.
**Review pass (review2) on WP4 before WP5 starts**, aimed at rule 2, the
label set and the double-submit handling.

**WP5. Page.** Template, nav, filters, answer form, problems list, empty
state. Render tests for each filter and for a posted answer notice; a
Go-side fetch of `/questions` on a running instance.

**WP6. Prompts.** Changes per 2.6. Acceptance: `embed_test.go` checks
that each of the five prompts contains the block format, the escalation
keeps the trigger and adds `needs-human`, and the prompt reads comments
with `gh issue view` or `gh pr view` and `--comments`; `merge.md` and
`quick.md` are unchanged. A review pass (review2) checks the five prompts
for consistency with `hub-protocol.md` section 4.

**WP7. Acceptance test.** Section 4. `make acceptance` passes; the new scenario fails on `main`.

**WP8. Docs.** README label table and a Questions section; mark Phase 3
items in `features.md` as existing only after WP7 passes; handoff update.
Final review2 pass over the whole diff before the PR.

## 4. Acceptance test

A new scenario in `TestAcceptance` (`cmd/jig/acceptance_test.go`),
`f_blocking_question_stops_and_answer_resumes`. The mock agent writes no
comments and edits no labels, so the test adds the comment and the
`needs-human` label that an agent writes. The engine writes `agent:done`.

1. Seed issue A: label `agent:plan`, assigned to the token owner, and one
   comment with a blocking `agent-question` (id `q-1`, audience
   `product`).
2. A is dispatched (`+in-progress`), the run succeeds, and the engine
   writes `+agent:done`. The test then adds `needs-human` through the
   fake. A has `agent:plan`, `agent:done` and `needs-human`.
3. Seed issue B: labels `agent:plan`, `agent:done`, `needs-human`, a
   comment with no question block.
4. Dispatch stops: over at least three polls, no new `+in-progress`
   label event on A or B.
5. `POST /questions/refresh`; `GET /api/v1/questions` lists `q-1` on A as
   open and blocking, and nothing for B.
6. Answer: fetch `/questions`, read the form token, `POST
   /questions/answer` for `q-1`; submit the same form a second time.
7. The fake shows one new comment on A containing
   `<!-- agent-answer id="q-1"`, one label `PUT` on A, and the events
   `-needs-human` and `-agent:done`. `agent:plan` is still on A.
8. Resume: A gets `+in-progress` and reaches its handoff again
   (`+agent:done`). `GET /api/v1/runs` lists a second `succeeded` run of
   A with `attempt` 1.
9. B keeps `needs-human` and `agent:done`; no label write for B.
10. `Unexpected()` is empty.

The fake must support, in addition to what it has:

- Comments with any author, a seeded `author_association`, `updated_at`
  and `html_url`; `ETag` on the comments list and 304 on a matching
  `If-None-Match`.
- `PUT /repos/<owner>/<repo>/issues/<n>/labels`: replaces the item's
  labels and records one label event per label removed or added.
- Existing and unchanged: the engine's label writes (`POST .../labels`)
  stay on the item and are returned by later reads, so its `agent:done`
  is seen by the dashboard and the next poll; search with `repo:`,
  `state:open`, `label:`; rejection of any other route or qualifier.

The dashboard in the test points `github.api_url` at the fake.

## 5. Risks and open questions

Risks:

- WP0 read an upstream checkout, not the pinned tags. The acceptance test
  runs the pinned engine and checks steps 2, 4 and 8 on it.
- The label `PUT` replaces the full set, so another writer's change
  between the rescan and the `PUT` (2.5 step 4) is overwritten. The
  per-item mutex covers only the dashboard's own writes.
- An agent that drops its trigger leaves the answer path dependent on
  `first_labels` to restore it (rule 4).
- The dashboard has no authentication (Phase 0). Anyone who can reach it
  can post answers as the project's token account. The `sameOrigin` check
  stops cross-site form posts only.
- On a public repository anyone can post an `agent-answer` that an agent
  then follows. GitHub shows the token's account as the dashboard's
  answer author; `by` is the only record of who answered.
- Agents may write malformed blocks; such items stay parked until a person
  removes the labels by hand. The problems list makes this visible.
- An agent sees an answer only if its prompt reads comments through
  `gh`. The mock agent runs no `gh`, so the acceptance test cannot show
  it; WP6 checks that the prompts contain the command.

Open questions for the repo owner:

1. Does the question scanner run when `github.enrich` is off? It is the
   only way the page has data.
2. What does the answer form put in `by`: a fixed `jig dashboard`, a
   free-text name field, or the client address that the audit log uses?
3. Which comment authors may answer: anyone, or only authors with
   association `OWNER`, `MEMBER` or `COLLABORATOR`? The same rule then
   applies in the prompts.
4. Are questions in PR review bodies and inline review comments in scope,
   or only conversation comments?
5. Is a 2-minute scan interval acceptable, and is the 7-day window for
   items without `needs-human` the right one?
6. Should the page offer to remove `needs-human` and `agent:done` by hand
   on an item with malformed blocks, or leave that to GitHub?
