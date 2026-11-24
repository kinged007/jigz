# Checklist: restart a stuck issue in one action (#72)

- [x] CHK001 Is AC-1 testable with one CLI command and one dashboard control producing identical effects?
- [x] CHK002 Is AC-2 observable (no parked/retry hold, no stale claim, trigger applied, loop re-polls without manual steps)?
- [x] CHK003 Is AC-3 measurable (two runs yield exactly one pending re-queue, no double claim)?
- [x] CHK004 Does AC-4 cover both half-applied label states explicitly?
- [x] CHK005 Does AC-5 require one-line reason plus proof of no change (labels, state, no wake) for all three refusal cases?
- [x] CHK006 Do AC-6 surfaces carry the same user + timestamp from one supervisor-owned event store?
- [x] CHK007 Is AC-7 sequenced (down → request → start → applied once → cleared)?
- [x] CHK008 Does AC-8 forbid engine DB writes and require the plan to name the mechanism used?
- [x] CHK009 Does AC-9 name all three docs (docs/usage.md, docs/dashboard.md, docs/concepts.md)?
- [x] CHK010 Does every FR have at least one task (FR-001→T007/T008, FR-002→T006, FR-003→T003, FR-004→T003, FR-005→T002, FR-006→T004/T009, FR-007→T005, FR-008→T006, FR-009→T001/T006, FR-010→T011-T013)?
- [x] CHK011 Does every task name an AC or a polish purpose, with parallel marks only on independent files?
- [x] CHK012 Does the plan touch only confirmed files (ls/grep verified: main.go, wake_cmd.go, budget_cmd.go, supervise.go, status.go, hide.go, web.go, rundetail.go, templates/run.html, store.go, rollup.go, github.go)?
- [x] CHK013 Is the dashboard write a single documented exception with the same refusal path as the CLI?
- [x] CHK014 Is out of scope guarded (no attempt-limit/backoff change, AC-10 task)?
