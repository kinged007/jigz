# Checklist: #73 plan quality

- [x] CHK001 Is each AC measurable without re-reading the code (parked state, notice count, handoff, retry, docs greppable)?
- [x] CHK002 Does every AC appear in at least one task (AC-1..AC-10 mapped in tasks.md)?
- [x] CHK003 Does every task name an AC or a polish purpose, with no orphan tasks?
- [x] CHK004 Does the plan reuse `BuiltinUnusable`, `BuiltinLimit`, `BuiltinModelError`, `IsUnusable`, `IsModelError` instead of a second message catalog?
- [x] CHK005 Is the storage choice (engine command vs Jigz-owned state, never direct engine-DB writes) explicit with the PR recording the decision?
- [x] CHK006 Is exactly-once (one alert + one run event, same text, no duplicates) enforced by a key rather than by timing?
- [x] CHK007 Does a non-retryable failure on one item leave the loop running for other items?
- [x] CHK008 Do this issue and #72 land on one restart recovery path, idempotent with no double-claim?
- [x] CHK009 Are `docs/workflows.md` and `docs/usage.md` both updated with every class, behaviour, and fix?
- [x] CHK010 Does the test plan cover the unknown-failure and overlapping-wording edge cases (unknown retries, limit wins)?
- [x] CHK011 Is the quality gate (`gofmt -l cmd internal`, `go vet ./...`, `go test ./...`, `go build ./...`) plus the `--help` smoke test sufficient with no new tooling?
- [x] CHK012 Are all named files and functions confirmed (`ls`/`grep`), with no invented paths?
