# Plan: restart a stuck issue in one action (#72)

Source: `specs/72-stuck-issue-restart/spec.md` (Status: Clarified; product answers in issue comments confirm all five open points).

## Approach

Add one restart path owned by the supervisor and call it from both surfaces:

1. New CLI subcommand `jig issue restart <number> [--json]` in `cmd/jig/main.go` (`parseArgs`, `usage()`), following the `wake_cmd.go` / `budget_cmd.go` pattern (parse args, JSON result struct, one-line refusal on error).
2. One supervisor-owned restart operation: validate refusal rules, release claim, re-apply trigger label through `internal/github.Client` (same path/credentials as the loop, FR-008), record a restart event in supervisor-owned history (`internal/store` `events` table, new `kind`), queue a durable pending-restart request when the supervisor is down, wake the loop (`wakeSupervisor` pattern in `cmd/jig/wake_cmd.go`).
3. Parked/retry clearing via supervisor-owned state the loop honors (not direct engine DB writes, FR-009): a pending-restart record the gate/claim path checks before dispatch, so `parked_issues` / `retry_entries` (read today in `internal/rollup/rollup.go`) are bypassed for that issue on next poll. The change notes state this choice and the engine gap (no supported engine clear operation used).
4. Dashboard: one `POST` restart route on the run page (`cmd/jig/hide.go` `routeTable`, handler near `cmd/jig/rundetail.go`, button in `cmd/jig/templates/run.html`), calling the same operation with the same refusal rules; guarded by the existing `sameOrigin` check in `cmd/jig/web.go`. Document as the single narrow write exception; update `cmd/jig/readonly_test.go` expectations accordingly.
5. Refusals (FR-005, product precision): mid-run = supervisor run registry holds an active run for the issue; live-claimed = live session owns the claim; foreign = trigger belongs to another loop. One-line reason, no label/state change, no wake.

Why this shape: both surfaces share one operation (identical effects and refusals, AC-1/AC-5); supervisor-owned state avoids engine DB writes (AC-8) and gives down-durability plus the visible event (AC-6/AC-7) for free.

## Files to change

- `cmd/jig/main.go` — add `issue restart` subcommand parsing and `usage()` text.
- `cmd/jig/issue_restart.go` (new) — CLI entry, `--json` result struct, calls supervisor operation; one-line refusal output.
- `cmd/jig/issue_restart_test.go` (new) — CLI parsing, JSON shape, idempotency, refusal cases.
- `cmd/jig/supervise.go`, `cmd/jig/status.go` (`statusDoc`) — pending-restart request storage, apply-once on start, run-registry check for mid-run refusal.
- `internal/store/store.go` — new migration appending a restart-event kind/columns to `events` (never edit existing migrations); accessor for restart events.
- `cmd/jig/rundetail.go`, `cmd/jig/templates/run.html`, `cmd/jig/hide.go`, `cmd/jig/web.go` — dashboard restart control (`POST`), run-page event rendering, route registration.
- `cmd/jig/readonly_test.go`, `cmd/jig/runevent_test.go` — update read-only expectations for the single POST exception; restart-event rendering test.
- `internal/rollup/rollup.go` — honor supervisor restart state when computing held/parked display for the restarted issue (read-only change).
- `internal/github/github.go` (use only) — label changes via existing `Client` methods.
- `docs/usage.md`, `docs/dashboard.md`, `docs/concepts.md` — describe the action and the state it clears.

## Data and contract changes

- `internal/store` `events` table: one appended migration adding restart-event support (new `kind` value, actor/timestamp in `detail` JSON). No existing migration edited. Survives loop restarts.
- Pending-restart request: new supervisor-owned durable record (file under the repo `.jigz/` state or `events`-adjacent store table; implementer picks one, documents it). Applied once on next supervisor start, then cleared; refusal recorded with reason if no longer restartable.
- CLI contract: `jig issue restart <number> [--json]`; JSON carries the same who/when as the dashboard event. Dashboard contract: one `POST` route with same-origin guard; refusal is a one-line reason.
- No engine schema change, no direct writes to `parked_issues` / `retry_entries`, no attempt-limit or backoff change (AC-10). No new dependency.

## Test plan

Write failing tests first, per AC:

- AC-1: CLI restart and dashboard POST on a parked fixture produce identical state effects.
- AC-2: after restart, no parked/retry hold, no stale claim, trigger label applied (via fake tracker path), loop woken/re-polled.
- AC-3: restart twice in a row yields one pending re-queue, no double claim.
- AC-4: restart from trigger-without-claim and claim-without-trigger both succeed.
- AC-5: mid-run, live-claimed, and foreign-loop restarts each refuse with one line and change nothing (labels, state, no wake).
- AC-6: run page shows restart event with user + timestamp; `--json` carries the same values.
- AC-7: supervisor down → issue restart → start → applied exactly once with event; cleared afterwards.
- AC-8: test asserts no SQL write path targets engine `parked_issues` / `retry_entries`; change notes name the supervisor-owned mechanism.
- AC-9: docs assertions are manual (headings present, commands/flags verified against code).
- AC-10: attempt-limit/backoff code untouched (`git diff --stat` shows no retry-schedule files).
- Smoke test: build once (`go build ./...`), then `jig issue restart --help` (or `jig --help` showing the subcommand) plus one dashboard `GET` of the run page rendering without template error.

## Risks

- Dashboard write exception weakens the read-only guarantee; contained by one POST route, same-origin guard, same refusal rules, and `readonly_test.go` updated to pin the exception list.
- Concurrent double restart could double-claim; contained by single pending-request record with apply-once semantics.
- Tracker label failure mid-sequence could leave half-applied state; operation must be re-runnable (FR-003/FR-004) and report which step failed.
- No supported engine clear operation may exist; fallback is supervisor-owned state the loop honors, noted in the change.

## Rollback

Revert the branch (single PR). The `events` migration is append-only and harmless if left applied; pending-restart records are inert without the gate check. No engine data is written, so no data repair is needed.
