# Feature Specification: Stop-when-idle survives reload during drain

**Issue**: #76  **Status**: Draft  **Areas**: area:cli, area:supervisor

## User Scenarios and Testing

### User Story 1 - Graceful stop completes despite a mid-drain reload (Priority: P1)
An operator runs a graceful stop that waits for loops to go idle, then edits a loop's query filter and triggers a configuration reload while a loop is still finishing its current item. The stop must still finish and the process must exit instead of hanging forever.
**Independent test**: Start a loop with work in progress, begin a wait-for-idle stop, apply a query-filter change via reload, and observe the process exits once the in-flight item completes.
**Acceptance scenarios**:
1. Given a wait-for-idle stop is draining a loop that is mid-item, When a reload changes that loop's query filter, Then the loop is not restarted and the process exits once the loop goes idle.

### User Story 2 - Normal query-change restart keeps working (Priority: P1)
An operator edits a loop's query filter and reloads while no stop is in progress. The loop drains and restarts with the new query, as before.
**Independent test**: With the supervisor running normally (no stop requested), change a loop's query filter, reload, and observe the loop drains and restarts.
**Acceptance scenarios**:
1. Given no stop is in progress, When a reload changes a loop's query filter, Then that loop drains and restarts with the new query.

## Requirements

- **FR-001**: While a wait-for-idle stop is in progress, a reload that changes a loop's query filter MUST NOT restart a loop that is already stopping for the stop.
- **FR-002**: A reload that changes a loop's query filter while no stop is in progress MUST still drain and restart the affected loops.
- **FR-003**: A supervisor-level test MUST cover the sequence: begin wait-for-idle stop, apply a query-filter change, assert the process exits.

## Acceptance Criteria

- **AC-1**: A reload during a wait-for-idle stop does not restart a stopping loop, and the stop completes with the process exiting (covers FR-001).
- **AC-2**: A reload that changes a query still drains and restarts loops when no stop is in progress (covers FR-002).
- **AC-3**: A supervisor test starts a stop, applies a query change, and asserts the process exits (covers FR-003).

## Edge Cases

- The loop is mid-item when the stop begins: it finishes the in-flight item first, then exits without restarting.
- The loop is already idle when the stop begins: the reload does not bring it back.
- Several loops drain at once and only some have query changes: none of the stopping loops restart.
- A reload during a stop that changes nothing: the stop proceeds as before.
- A loop that is not running when the query changes during a stop stays held and is not started by the reload.
- The existing signal-based way out of the process keeps working regardless.

## Out of Scope

- Changes to what a query-filter reload does outside of a stop (restart semantics stay as they are).
- Changes to immediate (non-graceful) stop behavior.
- Changes to reload validation or to any other reload-triggered restart.
- Performance or observability improvements to the stop or reload paths.

## Clarifications

- None sought: the issue and the triage comment settle the intended precedence (an in-progress graceful stop wins over a query-change restart) and the required regression test.
