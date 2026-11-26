# Tasks: #73 classify failed attempts and park non-retryable failures

## Setup and tests first

- [x] T001 [P] [AC-1] internal/machine — add failing classifier tests for non-retryable wordings (unauthenticated, CLI/version unusable, missing model; limit overlap goes to limit)
- [x] T002 [P] [AC-2] internal/machine — add failing classifier tests for retryable wordings (usage limit, engine crash, transient network) and unknown-keeps-retry
- [x] T003 [P] [AC-4] cmd/jig + internal/store — add failing exactly-once tests (one alert + one run event, no duplicates on repeat)
- [x] T004 [P] [AC-3,AC-7] cmd/jig + internal/supervise — add failing tests (non-retryable parks item with no further attempt; loop keeps processing others)
- [x] T005 [P] [AC-5,AC-6] internal/machine + cmd/jig — extend existing handoff/retry tests (limit hands over and resumes; transient retries under limit/backoff)
- [x] T006 [P] [AC-8] restart path shared with #72 — add failing tests (restart clears added state; second run is a no-op, no double-claim)

## Implementation

- [x] T007 [AC-1,AC-2] internal/machine/unusable.go + internal/machine/limits.go — add crash/transient matchers and one Classify with fixed precedence, reusing existing matchers
- [x] T008 [AC-3,AC-6,AC-7] cmd/jig/supervise.go + cmd/jig/gate.go + internal/supervise/supervise.go — classify before retry; per-item park for non-retryable, unchanged path for retryable
- [x] T009 [AC-4] cmd/jig/alerts.go + internal/collector/collector.go + internal/store/history.go — write one notice (alert + one run event, same class and fix) with (issue, class) idempotency
- [x] T010 [AC-3,AC-4] parking mechanism — use a supported engine command or Jigz-owned state (never write engine tables directly); record the choice in the PR
- [x] T011 [AC-8] restart path shared with #72 (cmd/jig/app.go + cmd/jig/operations.go area) — clear the added parked/retry state on one shared recovery path, idempotent

## Polish

- [x] T012 [P] [AC-9] docs/workflows.md — document each class with its retry behaviour and one-line fix
- [x] T013 [P] [AC-9] docs/usage.md — document each class with its retry behaviour and one-line fix
- [x] T014 [AC-10] quality gate — run `gofmt -l cmd internal`, `go vet ./...`, `go test ./...`, `go build ./...` and fix until green
- [x] T015 [AC-10] smoke test — `go build -o /tmp/jig ./cmd/jig && /tmp/jig --help`
