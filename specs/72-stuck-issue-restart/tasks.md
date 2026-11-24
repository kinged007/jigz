# Tasks: restart a stuck issue in one action (#72)

## Phase 1 — Setup and tests first

- [x] T001 [P] [AC-8] cmd/jig/issue_restart_test.go — TestRestartWritesNoEngineParkedState pins read-only access to `parked_issues` / `retry_entries` (no write path anywhere outside tests/fixtures).
- [x] T002 [P] [AC-5] cmd/jig/issue_restart_test.go — refusal tests: mid-run, live-claimed, foreign loop, unknown issue, PR (one-line reason, no tracker writes).
- [x] T003 [P] [AC-3, AC-4] cmd/jig/issue_restart_test.go — idempotency tests: double restart, trigger-without-claim, claim-without-trigger.
- [x] T004 [P] [AC-6] cmd/jig/issue_restart_test.go (not runevent_test.go, which covers agent-log events) — restart event renders via runEvents with user + timestamp, matching the `--json` shape; run-page template renders the restart form.
- [x] T005 [P] [AC-7] cmd/jig/issue_restart_test.go — pending restart stored while down, applied once, then cleared; restartStep drops unreadable files and keeps unappliable ones.

## Phase 2 — Implementation

- [x] T006 [AC-2, AC-8] supervisor-owned pending-restart files + apply-once consume (cmd/jig/issue_restart.go `restartStep`, tick hook in cmd/jig/supervise.go, startup call in cmd/jig/main.go; no statusDoc change) — validate, release claim, re-apply trigger via internal/github.Client, record event, wake loop. Parked/retry rows are reconciled by the engine on re-poll, never written by Jigz.
- [x] T007 [AC-1, AC-2, AC-5] cmd/jig/issue_restart.go + cmd/jig/main.go — `jig issue restart <number> [--json]`, one-line refusals, report of effects.
- [x] T008 [AC-6, AC-1, AC-5] dashboard POST restart route + run-page button (cmd/jig/hide.go, cmd/jig/rundetail.go, cmd/jig/templates/run.html; cmd/jig/web.go needed no change — sameOrigin already guards every route) — same operation and refusal rules as CLI.
- [x] T009 [AC-6, AC-7] no migration needed: the existing `events` table already stores arbitrary kinds with actor/timestamp in the detail JSON, and the run page already renders observed events. Restart uses kind `restart`; no store change.
- [x] T010 [AC-2] dropped: no rollup change. The parked badge clears when the engine re-polls the re-queued issue and reconciles its own rows; Jigz cannot clear them without writing the engine DB (forbidden by AC-8). The restart event on the run page is the visible marker.

## Phase 3 — Polish (docs, smoke test, quality gate)

- [x] T011 [P] [AC-9] docs/usage.md — `### Restarting a stuck issue` documents command, flags, output, refusals, down behavior.
- [x] T012 [P] [AC-9] docs/dashboard.md — run-page Restart issue button and the single write exception to the read-only rule.
- [x] T013 [P] [AC-9] docs/concepts.md — parked recovery paragraph: what the restart clears, engine reconciliation, refusals.
- [x] T014 [AC-1] smoke test — `go build ./...`, `jig --help` lists `issue restart`, run-page template renders in TestRunPageOffersOnlyRestartControl.
- [x] T015 [AC-10] quality gate — `gofmt -l cmd internal`, `go vet ./...`, `go test ./...`, `go build ./...`; no retry-schedule change (`git diff --stat` shows no engine/retry files); readonly test comment names the single exception.
