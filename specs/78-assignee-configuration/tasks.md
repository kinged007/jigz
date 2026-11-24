# Tasks: Configurable assignee scope (#78)

## Phase 1 — Setup and tests first

- [x] T001 internal/fakegithub/query.go — confirm the engine-accepted query form for one and for several assignees (engine docs/schema or live query); record the verdict in the implementation commit message
- [x] T002 [P] [AC-1] internal/config/config_test.go — failing test: `assignees: [alice]` produces filters scoped to `assignee:alice`
- [x] T003 [P] [AC-2] internal/config/config_test.go — failing test: no assignee key produces today's `assignee:@me` filters
- [x] T004 [P] [AC-3] internal/config/config_test.go — failing test: two users produce a filter matching either (form per T001)
- [x] T005 [P] [AC-4] internal/config/work_test.go — failing test: loop-level override replaces global for that loop only; empty override inherits global
- [x] T006 [P] [AC-5] internal/config/config_test.go — failing test: blank/invalid assignee fails load with error naming key and value
- [x] T007 [P] [AC-6] internal/config/config_test.go — failing test: `Env` exposes `JIG_ASSIGNEE` with configured value, `@me` when unset
- [x] T008 [P] [AC-7] internal/config/strict_test.go — failing test: `assignees` loads as a known key; singular `assignee` still rejected

## Phase 2 — Implementation

- [x] T009 [AC-1,AC-2,AC-3] internal/config/config.go — add top-level `assignees`, scope-rendering helper, load-time validation; `FilterFor` appends the resolved scope
- [x] T010 [AC-4,AC-5] internal/config/work.go — add per-work-entry `assignees` override with inherit-on-empty resolution and validation
- [x] T011 [AC-1,AC-2,AC-3,AC-4] cmd/jig/supervise.go — use the resolved per-loop scope instead of hardcoded `assignee:@me`
- [x] T012 [AC-1,AC-2,AC-3,AC-4] cmd/jig/main.go — use the resolved per-loop scope instead of hardcoded `assignee:@me`
- [x] T013 [AC-6] internal/config/config.go — add `JIG_ASSIGNEE` to `Env`
- [ ] T014 [AC-3] internal/fakegithub/query.go — only if T001 requires it: align multi-assignee OR semantics and extend its tests (not required: T001 found comma-separated OR in a single term already supported, including per-value @me; see implementation commit message)

## Phase 3 — Polish

- [x] T015 [AC-8] docs/configuration.md — document the key, `@me` default, per-loop override, `JIG_ASSIGNEE`, and the multi-user query form
- [x] T016 [polish] smoke test — `go build ./...` then run built `jig validate` on a fixture config with `assignees` and a loop override; confirm reported filters
- [x] T017 [polish] quality gate — `gofmt -l cmd internal`, `go vet ./...`, `go test ./...`, `go build ./...`
