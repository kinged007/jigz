# Checklist: Configurable assignee scope (#78)

- [x] CHK001 Is AC-1 measurable? Yes: filter string for `assignees: [alice]` asserted in `config_test.go` (T002).
- [x] CHK002 Is AC-2 measurable? Yes: filter equality with today's `assignee:@me` output asserted (T003).
- [x] CHK003 Is AC-3 measurable? Yes, once T001 fixes the syntax: filter form asserted (T004); engine verdict recorded before implementing.
- [x] CHK004 Is AC-4 measurable? Yes: override-loop vs inheriting-loop filters asserted (T005, T010).
- [x] CHK005 Is AC-5 measurable? Yes: load error naming key and value asserted (T006).
- [x] CHK006 Is AC-6 measurable? Yes: `Env` contents asserted for set and unset cases (T007, T013).
- [x] CHK007 Is AC-7 measurable? Yes: known-key load plus existing singular-key rejection asserted (T008).
- [x] CHK008 Is AC-8 measurable? Yes: docs sections named by file and anchor lines (T015); verified by review.
- [x] CHK009 Does every FR have a task? FR-001 T002/T008/T009; FR-002 T003/T009; FR-003 T004/T009; FR-004 T005/T010; FR-005 T006/T009/T010; FR-006 T007/T013; FR-007 T015; FR-008 T001/T004/T014.
- [x] CHK010 Does every task name an AC or polish purpose? Yes: each line cites its AC or `polish`.
- [x] CHK011 Can parallel tasks run without conflicts? Yes: T002–T008 touch three test files but distinct test functions; T009–T013 touch different source files; T014 is conditional on T001.
- [x] CHK012 Are all named files/functions confirmed? Yes: `config.go` FilterFor/Env/removedKeys, `work.go` workFile/validateTracker, `supervise.go:249`, `main.go:362`, `fakegithub/query.go` assignee case, `docs/configuration.md` sections — all confirmed by grep.
- [x] CHK013 Does the plan avoid scope creep? Yes: no `assignee:*` watching, no GitHub user-existence validation, no migration (per spec Out of Scope); T014 is conditional.
- [x] CHK014 Is the multi-user syntax risk contained? Yes: T001 verifies against the engine first; assumption (comma-joined `JIG_ASSIGNEE`, engine OR form) is explicit and documented in T015.
