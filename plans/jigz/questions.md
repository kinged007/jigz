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
across repositories and posts answers.

A blocking question uses the existing label `needs-human`. Both shipped
workflows already exclude it from dispatch
(`cmd/jig/workflows/WORKFLOW.issues.md` line 11, `WORKFLOW.prs.md`
line 12), so installed projects need no workflow change and `setup`
creates no new label.

1. Every escalation to `needs-human` that waits for an answer carries at
   least one blocking `agent-question` block in its comment. An escalation
   with no question (for example, a PR that a person must review) has no
   block.
2. The dashboard clears `needs-human` only when the item has at least one
   blocking `agent-question` and every blocking question has an
   `agent-answer`. It never clears `needs-human` on an item without
   question blocks.
3. When an agent escalates it keeps its `agent:*` trigger label, so
   removing `needs-human` returns the item to dispatch
   (`cmd/jig/workflows/prompts/review-fix.md` line 138).
4. A non-blocking question does not stop the work: the agent proceeds with
   its suggested answer and records the assumption.

## 2. Design

### 2.1 Finding open questions

Today the dashboard reads GitHub only in `internal/collector/enrich.go`,
through the GET-only client in `internal/github/github.go`. Enrichment is
off unless `github.enrich` is set, reads PRs, issue timelines, commits and
check runs, and never reads issue comments. Each project uses its own
tracker token (`collector.Target`, built in `cmd/jig/history.go`).

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
   Review comments and review bodies are not read (open question 5).

Cadence: a pass every 2 minutes, and on demand from the page's Refresh
button and before every answer is posted. Rate use: one search call per
project per pass (search limit is 30 per minute per token), plus one
comments call per candidate. A 304 reply does not count against the
GitHub REST limit. A project with 10 parked items uses about 330 requests
per hour, well under the 5000 per hour of a token. The scanner shares the
client's per-token back-off, so a rate-limited token pauses both the
enricher and the scanner. Whether the scanner runs when `github.enrich` is
off is open question 2.

### 2.2 Parse rules

A pure package, `internal/questions` (`parse.go`), takes a comment list
(id, author, author association, body, created, URL) and returns
questions, answers and problems. Rules:

- A question block starts with `<!-- agent-question ... -->` and ends with
  `<!-- /agent-question -->`. An answer block uses `agent-answer`.
- Attributes are `name="value"` pairs in any order. Question: `id`,
  `audience`, `blocking` are required. Answer: `id` is required, `by` is
  optional.
- `id` matches `q-[A-Za-z0-9_-]{1,64}`. `audience` is `product` or
  `project`. `blocking` is `true` or `false`.
- The text is the `**Question:**` paragraph (required) and the
  `**Suggested answer:**` paragraph (optional).
- Markers inside fenced code blocks are ignored, so a comment that quotes
  the format does not create a question.
- Malformed: an unknown or missing attribute value, a start without an
  end, a nested start, or a bad id. A malformed block is reported as a
  problem with a link to its comment. It is not a question. An item with
  any malformed question block is never cleared automatically (rule 2
  then cannot be checked), and the page says why.
- Duplicate question ids on one item: the latest block (by comment order)
  defines the text, audience and blocking flag; the page shows "asked N
  times". One answer with that id answers it.
- Several answers to one id: the latest answer is the current one; all are
  shown.
- An answer whose id matches no question on the item is reported as a
  problem and has no other effect.
- Edited and deleted comments: each pass parses the current bodies.

Whose answers count is open question 4. The parser keeps the author
association so the rule can be applied in one place.

### 2.3 Storage

A new migration in `internal/store/store.go` adds two tables, with access
code in `internal/store/questions.go`:

- `question_items`: project, number, is_pr, title, url, labels,
  comments_etag, scanned_at, problems (JSON). One row per scanned item.
- `questions`: project, number, qid, audience, blocking, question,
  suggested, asked_comment_url, asked_at, asked_count, answer, answered_by,
  answer_comment_url, answered_at. Primary key (project, number, qid).

The existing `github_cache` table is keyed per item and holds PR fields;
questions are several rows per item and need their own key, so a new
table is simpler than widening it. The tables are a cache of GitHub: each
pass replaces an item's rows from the parsed comments, so nothing is lost
if they are dropped.

### 2.4 Questions page

`GET /questions`, a nav entry after Runs in
`cmd/jig/templates/layout.html`, template
`cmd/jig/templates/questions.html`, handler `cmd/jig/questions.go`.

- One row per open question: project, item link, title, audience,
  blocking, age, question text, suggested answer, link to the comment.
- Filters as query parameters: `audience=product|project`,
  `blocking=true|false`, `project=<slug>`, `state=open|answered`.
- Answer form per question: a text area prefilled with nothing, a button
  "Use suggested answer" that fills it, and Submit. The form carries the
  project, number, qid and a one-time token.
- Item problems (malformed blocks, answers for unknown ids, label not
  cleared) are shown above the list with the item link.
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
4. Rescan the item. Apply rule 2 to the fresh comments. If it holds and
   the item carries `needs-human`, send
   `DELETE /repos/<slug>/issues/<n>/labels/needs-human`. A 404 means it is
   already removed and counts as success.
5. Failures: if step 3 fails nothing changed; the form is shown again
   with the text and the error. If step 4 fails the answer is posted; the
   page says "answer posted, `needs-human` not cleared" with the error and
   a Retry button that repeats step 4 only.

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
  the audience values, and rules 1, 3 and 4.
- Where the prompt escalates to `needs-human` because it needs an answer,
  it writes at least one blocking block in that comment and keeps its
  trigger. The label command for this case is open question 1.
- Before acting, the prompt reads all comments (plan already renders
  `.issue.comments`; review reads them with `gh pr view --json comments`),
  applies each `agent-answer` to its question, and treats an unanswered
  non-blocking question as answered with its suggestion.
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
| 0 | complex (read-only) | none; findings to the lead | owner answers |
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

**WP0. Engine research.** Read the engine source (`git show` on the
pinned tag in `/home/coder/sortie`; do not change its working tree).
Answer: does an item that carries `agent:done` (the handoff state) and a
trigger get dispatched; does the engine dispatch again an item it already
handed off while the trigger stayed on; does the engine fetch comments
for `.issue.comments` on every run; does any run record or log contain
the rendered prompt. Acceptance: each answer cites file and line.

**WP1. Parser.** `internal/questions/parse.go` and table tests for every
rule in 2.2, including fenced code, nested and unterminated blocks,
duplicate ids, unknown-id answers, attribute order, and the sample from
kinged007/jigz#14. Acceptance: tests pass; no I/O in the package.
**Review pass (review2) on WP1 before WP4 starts.**

**WP2. GitHub writes and storage.** `Post` and `Delete` in
`internal/github` with tests on an `httptest` server (headers, back-off,
404 and 422 errors, token never in error text). Migration and store
functions for 2.3 with tests (replace an item's rows, list with filters,
schema version bump). Acceptance: tests pass; existing store tests pass.

**WP3. Fake GitHub.** See section 4 for the list. Acceptance: unit tests
in `internal/fakegithub` for each addition; unknown routes, parameters
and qualifiers are still rejected.
**Review pass (review2) on WP2 and WP3 together before WP4 starts.**

**WP4. Scanner and answer path.** Scanner per 2.1, wiring in
`cmd/jig/history.go`, handlers and the answer path per 2.5, JSON API.
Tests against `fakegithub`: rule 2 in all cases (no blocks, one of two
answered, all answered, malformed block present, label already removed),
double submit posts one comment, comment failure changes nothing, label
failure leaves a retryable state, audit entry written.
**Review pass (review2) on WP4 before WP5 starts**, aimed at rule 2 and
the double-submit handling.

**WP5. Page.** Template, nav, filters, answer form, problems list, empty
state. Render tests for each filter and for a posted answer notice; a
Go-side fetch of `/questions` on a running instance.

**WP6. Prompts.** Changes per 2.6. Acceptance: `embed_test.go` checks
that each of the five prompts contains the block format and the
escalation keeps the trigger; `merge.md` and `quick.md` are unchanged.
A review pass (review2) checks the five prompts for consistency with
`hub-protocol.md` section 4.

**WP7. Acceptance test.** Section 4. `make acceptance` passes; the new scenario fails on `main`.

**WP8. Docs.** README label table and a Questions section; mark Phase 3
items in `features.md` as existing only after WP7 passes; handoff update.
Final review2 pass over the whole diff before the PR.

## 4. Acceptance test

A new scenario in `TestAcceptance` (`cmd/jig/acceptance_test.go`),
`f_blocking_question_stops_and_answer_resumes`. The mock agent writes no
comments and edits no labels, so the test seeds the escalated state an
agent leaves.

1. Seed issue A: labels `agent:plan`, `needs-human` (plus `agent:done` if
   open question 1 keeps it), assigned to the token owner, and one comment
   with a blocking `agent-question` (id `q-1`, audience `product`).
2. Seed issue B: labels `agent:plan`, `needs-human`, a comment with no
   question block.
3. Dispatch stops: over at least three polls, no `+in-progress` label
   event on A or B.
4. `POST /questions/refresh`; `GET /api/v1/questions` lists `q-1` on A as
   open and blocking, and nothing for B.
5. Answer: fetch `/questions`, read the form token, `POST
   /questions/answer` for `q-1`; submit the same form a second time.
6. The fake shows exactly one new comment on A containing
   `<!-- agent-answer id="q-1"`, and a `-needs-human` label event on A.
7. Resume: A gets `+in-progress` and then reaches its handoff, as in
   scenario a.
8. The next run sees the answer: the engine's
   `GET /repos/<slug>/issues/<A>/comments` request comes after the answer
   comment in the fake's request log. If WP0 finds the rendered prompt in
   a run record or log, the test also asserts the answer text is there.
9. B still has `needs-human`, and the fake received no label DELETE for
   B.
10. `Unexpected()` is empty.

The fake must support, in addition to what it has:

- Comments with any author and an `author_association` field (seeded per
  comment); `updated_at` and `html_url` in comment JSON.
- `ETag` on the comments list and `If-None-Match` answered with 304 when
  the comments did not change.
- Existing and unchanged: 404 on DELETE of a label the item lacks;
  search with `repo:`, `state:open`, `label:`; rejection of any other
  route, parameter or qualifier.

The dashboard in the test points `github.api_url` at the fake.

## 5. Risks and open questions

Risks:

- The escalation commands in all prompts add `agent:done` with
  `needs-human`, and the prompts and README state that `agent:done` keeps
  every loop away from an item. If the engine skips an item with
  `agent:done`, removing `needs-human` alone does not resume it, and
  rule 3 does not hold. WP0 settles the engine behaviour.
- The dashboard has no authentication (Phase 0). Anyone who can reach it
  can post answers as the project's token account. The `sameOrigin` check
  stops cross-site form posts only.
- On a public repository anyone can comment, so anyone can post an
  `agent-answer` that an agent then follows.
- GitHub shows the token's account as the answer's author; `by` is the
  only record of who answered.
- Agents may write malformed blocks; such items stay parked until a person
  clears the label by hand. The problems list makes this visible.
- Prompt changes reach installed projects only after `setup` is re-run.
- The acceptance test cannot show that a real model follows an answer;
  it shows only that the engine reads the comment after it was posted.

Open questions for the repo owner:

1. When an agent escalates with a blocking question, does it still add
   `agent:done`? If yes, the dashboard must also remove `agent:done` when
   it clears `needs-human`, or the item does not return to dispatch.
2. Does the question scanner run when `github.enrich` is off? It is the
   only way the page has data.
3. What does the answer form put in `by`: a fixed `jig dashboard`, a
   free-text name field, or the client address that the audit log uses?
4. Which comment authors may answer: anyone, or only authors with
   association `OWNER`, `MEMBER` or `COLLABORATOR`? The same rule then
   applies in the prompts.
5. Are questions in PR review bodies and inline review comments in scope,
   or only conversation comments?
6. Is a 2-minute scan interval acceptable, and is the 7-day window for
   items without `needs-human` the right one?
7. Should the page offer to clear `needs-human` by hand on an item with
   malformed blocks, or leave that to GitHub?
