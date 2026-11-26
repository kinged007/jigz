# Feature Specification: Restart a stuck issue in one action

**Issue**: #72  **Status**: Clarified  **Areas**: area:cli, area:supervisor, area:dashboard, area:docs

## User Scenarios and Testing

### User Story 1 - Restart a parked issue from the command line (Priority: P1)
An operator whose issue exhausted its attempts and is parked runs one restart command for that issue number, so the issue becomes pickable again without manual label surgery.
**Independent test**: Park an issue by driving it to the attempt limit (or by reproducing the unauthenticated-agent session from the issue), run the single restart command, and confirm the issue is re-queued and the command reports what it did.
**Acceptance scenarios**:
1. Given an issue parked after the attempt limit, When the operator runs the restart action for that issue number, Then the parked and retry state is cleared, any claim is released, the trigger label is present, the loop is woken, and the command reports each effect.
2. Given a parked issue, When the operator runs the restart action twice in a row, Then the issue remains restartable and is not claimed twice.

### User Story 2 - Restart from the dashboard and see what happened (Priority: P1)
An operator looking at the run page for a stuck issue presses the restart control there, so the same recovery happens without using the terminal, and the page shows who triggered the restart and when.
**Independent test**: From the run page of a parked issue, trigger the restart control and confirm the run page shows a restart event with the triggering user and timestamp, matching the machine-readable command output.
**Acceptance scenarios**:
1. Given the run page of a parked issue, When the operator triggers the restart control, Then the same state clearing, claim release, trigger re-application, and loop wake occur as from the command line.
2. Given a completed restart, When the operator views the run page and the machine-readable command output, Then both show the same restart event with who triggered it and when.

### User Story 3 - Refuse an unsafe restart with a clear reason (Priority: P2)
An operator who mistakes a healthy or foreign issue for a stuck one runs the restart action, so the system refuses with a one-line reason instead of disturbing live work.
**Independent test**: Attempt restart against a mid-run issue, an issue claimed by a live session, and an issue belonging to another loop, and confirm each is refused with a one-line reason and no state changes.
**Acceptance scenarios**:
1. Given an issue mid-run or claimed by a live session, When the operator runs the restart action, Then the action is refused with a one-line reason and nothing is changed.
2. Given an issue that belongs to another loop, When the operator runs the restart action, Then the action is refused with a one-line reason and nothing is changed.

## Requirements

- **FR-001**: The system MUST provide a single restart action for a stuck issue that is available both from the command line (taking the issue number) and from the dashboard run page.
- **FR-002**: The restart action MUST clear the parked state and the retry state for that issue, release any claim on it, ensure the trigger label is applied, and wake the loop so the issue is picked up again.
- **FR-003**: The restart action MUST be idempotent: repeated invocations MUST NOT produce duplicate claims or leave the issue unrestartable.
- **FR-004**: The restart action MUST succeed from either half-applied label state: trigger present without claim, or claim present without trigger.
- **FR-005**: The restart action MUST refuse with a one-line reason, changing nothing, when the issue is mid-run, already claimed by a live session, or belongs to another loop.
- **FR-006**: The restart action MUST record a visible restart event carrying who triggered it and when, shown on the dashboard run page and included in the machine-readable command output.
- **FR-007**: The restart action MUST be durable across loop restarts: a restart requested while the loop supervisor is down MUST be applied on the next start.
- **FR-008**: The restart action MUST perform label changes through the same tracker path and credentials the loop uses, so permissions and rate limits match normal operation.
- **FR-009**: The restart action MUST NOT write the engine's internal state directly; it MUST clear parked and retry state through a supported engine operation or through supervisor-owned state that the loop honors.
- **FR-010**: User documentation MUST describe how to restart a stuck issue from the command line and the dashboard, and the concepts documentation MUST explain what state the restart clears.

## Acceptance Criteria

- **AC-1**: One command and one dashboard control restart the same stuck issue with the same effects (covers FR-001).
- **AC-2**: After restart, the issue shows no parked state, no remaining retry state, no stale claim, the trigger label applied, and the loop woken so the issue is re-polled without further manual steps (covers FR-002, FR-008).
- **AC-3**: Running the restart action twice in succession leaves exactly one pending re-queue: the issue is still restartable and is not double-claimed (covers FR-003).
- **AC-4**: Restart succeeds both when the trigger label is present without a claim and when a claim remnant is present without the trigger label (covers FR-004).
- **AC-5**: Restart of a mid-run issue, an issue claimed by a live session, or an issue belonging to another loop is refused, prints a single-line reason, changes no labels or state, and does not wake the loop for that issue (covers FR-005).
- **AC-6**: The dashboard run page shows the restart as an event with the triggering user and timestamp, and the machine-readable command output carries the same user and timestamp (covers FR-006).
- **AC-7**: A restart requested while the supervisor is down is applied exactly once on the next start: stopping the supervisor, issuing the restart, starting again, and observing the issue re-queued with its restart event (covers FR-007).
- **AC-8**: Parked and retry state is cleared without direct writes to the engine's internal store, and the implementation notes state which supported mechanism was used (covers FR-009).
- **AC-9**: The user and concepts documentation describe the restart action and the state it clears (covers FR-010).
- **AC-10**: The attempt limit and retry backoff behavior are unchanged by this feature (guards the out-of-scope boundary).

## Edge Cases

- Restart requested for an issue number that does not exist or is not an issue: refuse with a one-line reason, change nothing.
- Restart requested for an issue that is already healthy and pickable: succeed or refuse, but never duplicate the claim; the chosen behavior is documented.
- Two operators restart the same issue at the same time: exactly one re-queue results, both callers see a consistent report.
- The tracker call to change labels fails (permissions, rate limit, network): report which step failed, leave the issue restartable, keep the pending request where the down-supervisor guarantee requires it.
- The supervisor restarts between the label change and the wake: the pending request is applied once, not twice, on the next start.
- The dashboard control is used by someone without tracker write permission: refuse with a one-line reason consistent with the command-line refusal.
- The restart event is recorded even when the subsequent re-run fails again: the event history still shows who restarted it and when.

## Out of Scope

- Automatic recovery or reclassification without a person (reserved for the classification work referenced as #64 and follow-ups).
- Changing the attempt limit or retry backoff values or schedule.
- New retry policies, new escalation labels, or changes to which loop owns which trigger.
- Bulk restart of many issues at once (this feature restarts one issue per action).

## Clarifications

- Q: The dashboard is read-only today and every control is a command-line command. How is the dashboard restart control allowed? -> A: Allow one narrow exception: the dashboard control triggers the same restart path as the command, subject to the same refusal rules, and documents the exception (chosen by agent as non-blocking assumption; product confirms).
- Q: Which mechanism clears parked and retry state: a supported engine operation or supervisor-owned state the loop honors? -> A: Either is acceptable, direct writes to the engine store are not; the plan chooses and the change notes state the choice (chosen by agent as non-blocking assumption; project confirms).
- Q: What exactly counts as mid-run, live-claimed, or belonging to another loop for refusal? -> A: Mid-run means the claim marker is held by an active run; live-claimed means a live session owns it; foreign means the issue's trigger belongs to a different loop than the one asked to restart it (chosen by agent as non-blocking assumption; project confirms).
- Q: Where does the restart event live so the run page and machine-readable output agree? -> A: Supervisor-owned history that both surfaces read; the event carries triggering user and timestamp and survives loop restarts (chosen by agent as non-blocking assumption; project confirms).
- Q: What does durability while the supervisor is down mean when the issue changes meanwhile? -> A: The request is stored durably, applied once on next start, then cleared; if the issue is no longer restartable at apply time it is refused with a one-line reason and the outcome is reported (chosen by agent as non-blocking assumption; project confirms).
