# Checklist (#84)

- [x] CHK001 Is each AC measurable by the test named for it (AC-1/AC-2 by Held-block + href test, AC-5 by resolve-every-href test)?
- [x] CHK002 Does every FR have a task (FR-001→T002/T006/T007, FR-002→T007, FR-003→T004, FR-004→T001/T003/T004/T005, FR-005→T009, FR-006→T008)?
- [x] CHK003 Does the plan avoid a new route and any write path, keeping `readonly_test.go` unchanged?
- [x] CHK004 Is the tolerant-reader behavior explicit (probe optional columns, never require them unless proven)?
- [x] CHK005 Is the anchor scheme collision-free across loops and hold kinds?
- [x] CHK006 Does the smoke test use only repo-documented commands (`AGENTS.md` gate + local dashboard page load)?
- [ ] CHK007 Do the hold-row column names match a real engine DB (verified in T001 before T004 lands)?
