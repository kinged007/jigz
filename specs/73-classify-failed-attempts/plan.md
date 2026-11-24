# Plan: Classify failed attempts and park non-retryable failures (#73)

Source: `specs/73-classify-failed-attempts/spec.md` (spec stage, no open points). This plan leaves the spec unchanged.

## Approach

Add one classifier in the `internal/machine` package that runs on each failed attempt before any retry, reusing the existing matchers (`BuiltinUnusable`/`IsUnusable` in `internal/machine/unusable.go`, `BuiltinLimit`, `BuiltinModelError`/`IsModelError` in `internal/machine/limits.go`). Two matcher sets do not exist yet and are added in the same style: engine-crash wording and transient-network wording. Precedence is fixed: usage-limit first (matches existing `IsUnusable` precedence), then unauthenticated / CLI-or-version unusable / missing model (non-retryable), then engine crash, then transient network (retryable). Anything matching nothing keeps today's retry behaviour and never parks silently.

Non-retryable outcome: stop further attempts for that item only, park the item, and record exactly one notice carrying the class plus a one-line fix (e.g. `run /login`, upgrade the engine, configure the model). The notice is visible in both places from one write: a dashboard alert through the existing `loopDBAlerts`/`heldAlerts` read path in `cmd/jig/alerts.go`, and one run event via the existing `store.Event` path (`internal/collector/collector.go` diff, `internal/store/history.go` `AddEvents`/`IssueEvents`). Exactly-once is enforced by an idempotency key per (issue, class) so repeated checks and loop cycles add no duplicates. The loop itself stays up and keeps processing other items.

Retryable outcome: no behaviour change. Usage-limit keeps the existing pool handoff and resume-at-reset; transient network and engine crash keep the existing attempt limit and backoff.

Storage decision (per the issue Notes): prefer a supported engine command or Jigz-owned state; never write the engine-owned `parked_issues`/`retry_entries` tables directly (they are read-only to the dashboard, and `readonly_test.go` fails on write routes). Default is Jigz-owned state in the state directory following the existing `internal/machine/state.go` + `pool.json` pattern; if a supported engine command for parking exists, use it instead. The implementer confirms which one with `jig --help` and engine docs and records the choice in the PR. The restart action from #72 clears whatever parked and retry state this adds, on one shared recovery path (coordination point: `fleetSupervisor.Restart` in `cmd/jig/app.go`, `restartNote` in `cmd/jig/operations.go`); clearing is idempotent and never double-claims.

## Files to change

- `internal/machine/unusable.go`, `internal/machine/limits.go`: add engine-crash and transient-network matchers in the existing `Builtin*` style; add one `Classify` entry point with the fixed precedence above. Reuses `BuiltinUnusable`, `BuiltinLimit`, `BuiltinModelError`, `IsUnusable`, `IsModelError` without changing their behaviour.
- `cmd/jig/supervise.go`, `cmd/jig/gate.go`, `internal/supervise/supervise.go`: call the classifier on a failed attempt before retry; non-retryable parks the item (per-item only, loop stays alive); retryable falls through to today's path.
- `cmd/jig/alerts.go`: surface the single notice through the existing `loopDBAlerts`/`heldAlerts` path (reads Jigz-owned state or the engine command result, never writes).
- `internal/collector/collector.go`, `internal/store/history.go`: record the single run event (`AddEvents`) with the same class and fix text; readable via `IssueEvents`.
- `internal/rollup/rollup.go`, `internal/rollup/schema.go`: read-only; only touched if the chosen parking mechanism needs a new read source, otherwise unchanged.
- Restart path shared with #72 (`cmd/jig/app.go` `fleetSupervisor.Restart`, `cmd/jig/operations.go` area): clear the parked and retry state this issue adds; idempotent.
- `docs/workflows.md`, `docs/usage.md`: list every failure class with its retry behaviour and, for non-retryable classes, the one-line fix.
- Tests next to the code, following `internal/machine/unusable_test.go` and `cmd/jig/alerts_held_test.go` patterns.

## Data and contract changes

- Public interfaces: one new classifier function in `internal/machine` (exact name chosen by implementer); no change to existing matcher signatures.
- Schemas: none on engine tables. If Jigz-owned state is used, one small state file under the existing state directory (same durability/locking conventions as `internal/machine/state.go`); exact shape chosen by implementer and documented in the PR.
- Persisted data: per-item parked flag plus one notice record keyed by (issue, class) for exactly-once; cleared by restart.
- Config and dependencies: none.

## Test plan (tests written first, per AC)

- AC-1: classifier unit tests — unauthenticated, CLI/version-unusable, and missing-model wordings classify non-retryable (limit wording still wins on overlap).
- AC-2: usage-limit, engine-crash, and transient-network wordings classify retryable; unknown wording keeps today's behaviour.
- AC-3: non-retryable failure on one item starts no further attempt for it and the item reads as parked.
- AC-4: exactly one dashboard alert and exactly one run event with identical class and fix; re-running checks and loop cycles adds nothing.
- AC-5: usage-limit failure hands to the next pool account and resumes at reset (existing handoff test extended).
- AC-6: transient-network failure retries under the existing limit and backoff (existing retry test extended).
- AC-7: one item parked non-retryably while other items in the same loop keep being claimed and processed.
- AC-8: restart on the parked item clears the added state and the item becomes claimable; running restart twice does not double-claim.
- AC-9: docs test — each class appears in both `docs/workflows.md` and `docs/usage.md` with behaviour and fix (grep-based, following the docs-link convention).
- AC-10: quality gate as regression guard.
- Smoke test: `go build -o /tmp/jig ./cmd/jig && /tmp/jig --help` (smallest real run of the built artifact).

## Risks

- Writing engine-owned tables directly would break the dashboard read-only contract; mitigated by the Jigz-owned-state default and the PR-recorded choice.
- Duplicate notices across fast refresh (`dbAlertTTL` reads every 10s) and loop cycles; mitigated by the (issue, class) idempotency key.
- Over-broad crash/network matchers misclassifying real failures as retryable or vice versa; mitigated by unit tests per wording and unknown-defaults-to-retry.
- Diverging recovery paths with #72; mitigated by sharing one clear function both land on.

## Rollback

Revert the single PR. Jigz-owned state files are additive and ignored when the code is absent; engine tables are never written by this change, so no migration or cleanup is needed. Restart behaviour returns to whatever #72 ships alone.
