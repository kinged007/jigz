# Plan: Stop-when-idle survives reload during drain (#76)

Spec: `specs/76-stop-when-idle-reload/spec.md` (existing spec; requirements taken from it, not redefined here).
Base: `origin/dev`, not this worktree's `main`-based checkout. Per the OWNER triage comment, #65
(squash `8fe0661` on `dev`) already contains the FR-001 fix; this was verified against `origin/dev`
(`git show FETCH_HEAD:cmd/jig/gate.go`). If the plan finds nothing missing beyond the test, the issue
is closed as fixed by #65 plus the new test.

## Approach

The defect was `recycleDrain` unconditionally setting `p.recycle = true` on a proc that
`stop --when-idle` had already marked stopping with `recycle = false`, so `onExit` restarted the loop
and `finished()` (`exitWhenEmpty && len(s.procs) == 0`) never became true. On `origin/dev` this is
already fixed: `recycleDrain` in `cmd/jig/gate.go` returns early (`false`) for a loop that is
`stopping && !recycle`, and the `reload()` query-change and env-change paths only log/act when it
returns `true`. `onExit` already restarts only when `p.recycle && s.startable(...)`, so a
non-recycled stopping loop exits and `finished()` becomes true with no further prod change.

What is still open is FR-003 / AC-3: `TestReloadDoesNotRecycleStoppingLoops` only asserts the
`recycle` flag. No test runs a wait-for-idle stop, applies a query change, and asserts the process
exits. So the work is: rebase onto `origin/dev`, verify the guard is present, and add one
supervisor-level regression test that exercises the full sequence through `onSignal(sigStopIdle)`,
`reload()` with a changed `query_filter`, and `onExit`, asserting the run ends. The existing
`TestReloadWithAChangedRepoRewritesTheEnvFileAndRecyclesLoops` and
`TestReloadWithAnUnchangedConfigRecyclesNothing` already cover AC-2 (normal restart) and must keep
passing unchanged. No new helpers, no prod refactor, no config or schema change.

## Files to change

- `cmd/jig/supervise_test.go` — add `TestStopWhenIdleSurvivesQueryChangeReload` next to
  `TestAStopWhenIdleEndsTheRunOnlyWhenEmpty`: build a supervisor with one running loop over a config
  with a `work.<loop>.query_filter` (pattern from `gate_test.go` `workPlan` / `fallback_test.go`),
  drive `onSignal(sigStopIdle)`, rewrite the config with a different `query_filter`, call `reload()`,
  assert `recycle` is still false, then drive `onExit` and assert it reports done (process would exit).
  Also assert the no-stop baseline (query change with no stop still sets drain+recycle) only if not
  already covered by the existing recycle test; otherwise reuse it.
- `cmd/jig/gate.go`, `cmd/jig/supervise.go` — no change expected. Change only if verification against
  `origin/dev` shows the early-return guard missing (e.g. rebase drift); then re-apply the
  `stopping && !recycle` early return in `recycleDrain` and keep the `bool` return contract.

## Data and contract changes

None. No public interfaces, schemas, persisted data, config keys, or dependencies change.
`recycleDrain`'s `bool` return already exists on `origin/dev`.

## Test plan

- New regression test first (AC-3 / FR-003): stop-when-idle + query-change reload → `onExit` done.
  It also proves AC-1 (no restart of the stopping loop, stop completes with exit).
- AC-2: existing `TestReloadWithAChangedRepoRewritesTheEnvFileAndRecyclesLoops` (query/env change with
  no stop drains and restarts); run it, do not modify it.
- Focused suite: `go test ./cmd/jig/ -run 'Reload|StopWhenIdle|Recycle|Supervise'` (area focused checks).
- Full quality gate from `AGENTS.md`: `gofmt -l cmd internal` (empty), `go vet ./...`,
  `go test ./...` (~70s), `go build ./...`. Acceptance (`make acceptance`, ~8 min) is not required
  for this change.
- Smoke test: `go build -o /tmp/jig ./cmd/jig && /tmp/jig --help` (built artifact runs).

## Risks

- Worktree is based on `main`; `origin/dev` reload also handles env-change recycling and a rewritten
  `reload()`. Rebasing may surface conflicts in `supervise.go`/`supervise_test.go` — resolve by keeping
  `dev`'s reload and adding only the new test.
- A query-change reload during a stop for a *non-running* loop sets `gateHeld`; `gateStep` does not
  start held loops while `exitWhenEmpty`, but the new test should cover the running-loop case from the
  issue sequence and not expand scope to gate-held races.
- Timing-flaky drain tests: use the existing fake-clock/`newTestSup` pattern, never real sleeps or
  child processes.

## Rollback

Revert the single test commit (or the guard commit, if one was needed) with `git revert`; no data
migration and no config change make rollback safe. The supervisor behavior returns to the pre-#65 hang
only if both this and the #65 fix are reverted.
