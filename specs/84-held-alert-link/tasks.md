# Tasks (#84)

## Phase 1 — Setup and tests first

- [ ] T001 [P] [AC-4] internal/rollup — confirm actual hold-row column names for start date + display id against a loop DB/fixture; record finding in the implementation PR.
- [ ] T002 [AC-5] cmd/jig/alerts_held_test.go — add failing test: every alert href resolves to a view containing its subject (>=1 held + >=1 streak).
- [ ] T003 [AC-4] internal/rollup — add failing test: hold tables without the newer columns still read identity + reason with no error.

## Phase 2 — Implementation

- [ ] T004 [AC-1/AC-2/AC-4] internal/rollup/rollup.go — extend `Held` + `readHeld` with hold start and display id, tolerant of missing columns via existing `columns()` probe.
- [ ] T005 [AC-4] internal/rollup/schema.go — keep `requiredColumns` unchanged unless the alert/page provably needs the new columns.
- [ ] T006 [AC-1/AC-2] cmd/jig/templates/projects.html — add Held block to the `project` template fed by existing `Rows` loop data (loop, identifier, reason, hold start, GitHub link).
- [ ] T007 [AC-1/AC-2/AC-3] cmd/jig/alerts.go — point `heldAlerts` hrefs at the Held-block anchors and name the destination in the alert text.

## Phase 3 — Polish

- [ ] T008 [P] [AC-7] docs/dashboard.md — describe the Held block and what each held alert opens.
- [ ] T009 [P] [AC-6] quality gate — `readonly_test.go` passes unchanged; `gofmt -l cmd internal` empty; `go vet ./...`, `go test ./...`, `go build ./...` pass; smoke: serve dashboard, follow one held link on a project page.
