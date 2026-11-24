# Tasks: restart a stuck issue in one action (#72)

## Phase 1 — Setup and tests first

- [ ] T001 [P] [AC-8] internal/rollup/rollup.go — failing test pinning read-only access to `parked_issues` / `retry_entries` (no write path).
- [ ] T002 [P] [AC-5] cmd/jig/issue_restart_test.go — failing refusal tests: mid-run, live-claimed, foreign loop (one-line reason, nothing changed, no wake).
- [ ] T003 [P] [AC-3, AC-4] cmd/jig/issue_restart_test.go — failing idempotency tests: double restart, trigger-without-claim, claim-without-trigger.
- [ ] T004 [P] [AC-6] cmd/jig/runevent_test.go — failing test: restart event renders on run page with user + timestamp, matching `--json` shape.
- [ ] T005 [P] [AC-7] cmd/jig/supervise-level test — failing test: pending restart stored while down, applied once on start, then cleared.

## Phase 2 — Implementation

- [ ] T006 [AC-2, AC-8] supervisor-owned pending-restart state + apply-once gate (cmd/jig/supervise.go, cmd/jig/status.go) — validate, clear parked/retry hold, release claim, re-apply trigger via internal/github.Client, wake loop.
- [ ] T007 [AC-1, AC-2, AC-5] cmd/jig/issue_restart.go + cmd/jig/main.go — `jig issue restart <number> [--json]`, one-line refusals, report of effects.
- [ ] T008 [AC-6, AC-1, AC-5] dashboard POST restart route + run-page event (cmd/jig/hide.go, cmd/jig/rundetail.go, cmd/jig/templates/run.html, cmd/jig/web.go guard) — same operation and refusal rules as CLI.
- [ ] T009 [AC-6, AC-7] internal/store/store.go — appended `events` migration + restart-event accessor (actor, timestamp; survives restarts).
- [ ] T010 [AC-2] internal/rollup/rollup.go — honor supervisor restart state in held/parked display (read-only).

## Phase 3 — Polish (docs, smoke test, quality gate)

- [ ] T011 [P] [AC-9] docs/usage.md — document `jig issue restart` (command, flags, output, refusals).
- [ ] T012 [P] [AC-9] docs/dashboard.md — document dashboard restart control and the narrow write exception.
- [ ] T013 [P] [AC-9] docs/concepts.md — explain what parked/retry/claim/trigger state the restart clears.
- [ ] T014 [AC-1] smoke test — `go build ./...` then CLI help shows subcommand and run page renders.
- [ ] T015 [AC-10] quality gate — run repo checks (AGENTS.md: `gofmt -l cmd internal`, `go vet ./...`, `go test ./...`, `go build ./...`); confirm no retry-schedule change; update readonly expectations minimally.
