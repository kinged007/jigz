# Tasks: Configurable assignee scope (#78)

## Phase 1 — Setup and tests first

- [ ] T001 internal/fakegithub/query.go — confirm the engine-accepted query form for one and for several assignees (engine docs/schema or live query); record the verdict in the implementation commit message
- [ ] T002 [P] [AC-1] internal/config/config_test.go — failing test: `assignees: [alice]` produces filters scoped to `assignee:alice`
- [ ] T003 [P] [AC-2] internal/config/config_test.go — failing test: no assignee key produces today's `assignee:@me` filters
- [ ] T004 [P] [AC-3] internal/config/config_test.go — failing test: two users produce a filter matching either (form per T001)
- [ ] T005 [P] [AC-4] internal/config/work_test.go — failing test: loop-level override replaces global for that loop only; empty override inherits global
- [ ] T006 [P] [AC-5] internal/config/config_test.go — failing test: blank/invalid assignee fails load with error naming key and value
- [ ] T007 [P] [AC-6] internal/config/config_test.go — failing test: `Env` exposes `JIG_ASSIGNEE` with configured value, `@me` when unset
- [ ] T008 [P] [AC-7] internal/config/strict_test.go — failing test: `assignees` loads as a known key; singular `assignee` still rejected

## Phase 2 — Implementation

- [ ] T009 [AC-1,AC-2,AC-3] internal/config/config.go — add top-level `assignees`, scope-rendering helper, load-time validation; `FilterFor` appends the resolved scope
- [ ] T010 [AC-4,AC-5] internal/config/work.go — add per-work-entry `assignees` override with inherit-on-empty resolution and validation
- [ ] T011 [AC-1,AC-2,AC-3,AC-4] cmd/jig/supervise.go — use the resolved per-loop scope instead of hardcoded `assignee:@me`
- [ ] T012 [AC-1,AC-2,AC-3,AC-4] cmd/jig/main.go — use the resolved per-loop scope instead of hardcoded `assignee:@me`
- [ ] T013 [AC-6] internal/config/config.go — add `JIG_ASSIGNEE` to `Env`
- [ ] T014 [AC-3] internal/fakegithub/query.go — only if T001 requires it: align multi-assignee OR semantics and extend its tests

## Phase 3 — Polish

- [ ] T015 [AC-8] docs/configuration.md — document the key, `@me` default, per-loop override, `JIG_ASSIGNEE`, and the multi-user query form
- [ ] T016 [polish] smoke test — `go build ./...` then run built `jig validate` on a fixture config with `assignees` and a loop override; confirm reported filters
- [ ] T017 [polish] quality gate — `gofmt -l cmd internal`, `go vet ./...`, `go test ./...`, `go build ./...`
