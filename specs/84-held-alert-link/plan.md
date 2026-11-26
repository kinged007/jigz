# Plan: Held alert link shows the held item (#84)

Spec: `specs/84-held-alert-link/spec.md` (Status: Clarified; no open clarifications).

## Approach

Use the issue's preferred option: add a "Held" block to the project page (`cmd/jig/templates/projects.html` `project` template) listing that project's loops' `parked_issues` and `budget_hold_notices` rows, and point each held alert's `Href` at an anchor inside that block.

Why this over retargeting to the runs list: a held issue may never have run, so no runs filter can guarantee to show it; the project page already receives the loops (`cmd/jig/projects.go` `detail` builds `projectView.Rows` from `rollup.Loop`), and an anchor keeps the fix to one page plus one link, with no new route (read-only preserved, `readonly_test.go` untouched).

Changes:

1. `internal/rollup/rollup.go` — extend `Held` (currently `ID`, `Reason`) with the hold-start timestamp and the human-readable display identifier from the same rows; extend `readHeld` (currently `SELECT issue_id, reason`) to select them only when the columns exist, probed with the existing `columns()` helper the way `readSessions` probes `dispatch_id`. Missing columns fall back to available identity + reason, no error.
2. `internal/rollup/schema.go` — leave `requiredColumns` entries for `parked_issues` / `budget_hold_notices` at `issue_id, reason` so older engine DBs stay usable; record new columns there only if implementation proves the alert and page cannot work without them.
3. `cmd/jig/templates/projects.html` — new Held block in the `project` template, grouped per loop, each row showing loop, identifier, reason, hold start, linking to the GitHub item. Reuses the `Rows`/`projLoop.L` data `detail` already supplies; no handler or route change.
4. `cmd/jig/alerts.go` — `heldAlerts` keeps one alert per kind but sets `Href` to `/projects/<repo>#<held-anchor>` and rewrites `Msg` to name what the link shows (e.g. "see Held below"). The `Failure streak` `/runs?` link (`alerts.go:160`) is the anchor pattern to follow and stays as-is.
5. Tests + docs (see Test plan).

## Files to change

- `internal/rollup/rollup.go` — `Held`, `readHeld`.
- `internal/rollup/schema.go` — `requiredColumns` only if proven required.
- `cmd/jig/templates/projects.html` — `project` template Held block.
- `cmd/jig/alerts.go` — `heldAlerts` href + text.
- `cmd/jig/alerts_held_test.go` (extend) + one new resolving-links test next to it.
- `internal/rollup/` tests next to `readHeld` for the tolerant reader.
- `docs/dashboard.md` — Held block / link targets.

## Data and contract changes

- `rollup.Held` gains two fields (hold start, display identifier). In-memory only; `Parked`/`BudgetHeld` are `json:"-"`, so no API/storage contract change and no migration.
- No new route, no write to loop DBs, status documents, or GitHub. Reads stay behind `openRO` / `OpenReadOnly`.
- No dependency or config change.

## Test plan

Write first, per AC:

- AC-1/AC-2: extend `alerts_held_test.go`-style coverage — loop DB with one parked row + one budget row carrying reason, display id, timestamp; assert the Held block rows and both alert hrefs contain the item.
- AC-3: assert alert `Msg` names the destination (e.g. contains "Held").
- AC-4: loop DBs with hold tables lacking the newer columns (and one with tables absent) still render alerts + page from identity + reason, no error.
- AC-5: new test asserting every alert href resolves to a view containing its subject — at least one held alert (project page Held block) and one streak alert (runs page). Quick loop: `go test ./cmd/jig -run Alert`.
- AC-6: `readonly_test.go` passes unchanged (no new route).
- AC-7: docs assertion is manual review of `docs/dashboard.md`.
- AC-8 / smoke test: `gofmt -l cmd internal` empty; `go vet ./...`, `go test ./...`, `go build ./...` pass (from `AGENTS.md`; CI `.github/workflows/ci.yml` runs the same plus cross-build). Smoke: build the binary, serve the dashboard locally, open one project page with a held item and follow the alert link.

## Risks

- Engine column names for hold start / display id may differ from the issue text (`parked_at`, `noticed_at`, `display_id`); implementer verifies against a real loop DB/fixture first and probes with `columns()` so a miss degrades instead of failing.
- Anchor collisions across loops/kinds — include loop + kind + issue id in the anchor.
- Template regressions — templates parse at start; the resolving-links test must render the project page so a bad template fails fast.

## Rollback

Revert the branch. No migration, no stored state, no data to repair; old binary renders the old page and old links.
