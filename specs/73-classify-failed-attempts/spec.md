# Feature Specification: Classify failed attempts and park non-retryable failures

**Issue**: #73  **Status**: Draft  **Areas**: area:supervisor, area:dashboard, area:docs

## User Scenarios and Testing

### User Story 1 - Learn why an item parked and what to fix (Priority: P1)

A person whose work item stopped with a failure no retry can fix (for example the agent is not logged in) sees a single notice that names the failure class and the one-line fix, instead of finding a silently parked item by scanning alerts.

**Independent test**: cause one run to fail with an unauthenticated-agent error and check that the item is parked Jigz-side and exactly one notice appears carrying the class and the fix.

**Acceptance scenarios**:
1. Given an item whose attempt fails because the agent is unauthenticated, When the failure is handled, Then the item is parked Jigz-side and exactly one notice names the class and the fix.
2. Given the same parked item, When the person checks the dashboard and the run history, Then the same single notice is visible in both places with identical class and fix text.

### User Story 2 - Keep retryable failures on their current path (Priority: P1)

A person whose run fails for a transient reason (usage limit reached, temporary network error) gets today's recovery behaviour: the work continues without manual action.

**Independent test**: cause one run to fail with a usage-limit error and one with a transient network error, and check the usage-limit item hands over and resumes while the network-error item retries.

**Acceptance scenarios**:
1. Given an attempt that fails because a usage limit was reached, When the failure is handled, Then the item is handed to the next available pool account and resumes at the reset time.
2. Given an attempt that fails with a transient network error, When the failure is handled, Then the item is retried under the existing attempt limit and backoff.

### User Story 3 - Restart a parked item in one action (Priority: P2)

A person who fixed the underlying cause (logged in, upgraded, configured the model) restarts the parked item with the single restart action and the item becomes runnable again.

**Independent test**: park an item through a non-retryable failure, run the restart action, and check the parked and retry state added here is cleared so the item can run again.

**Acceptance scenarios**:
1. Given an item parked by a non-retryable failure, When the restart action runs, Then the parked and retry state from this change is cleared and the item returns to the normal claim path.

## Requirements

- **FR-001**: The system MUST classify each failed attempt into one of at least these classes: agent unauthenticated, agent CLI or version unusable, missing model, usage limit, engine crash, transient network.
- **FR-002**: The system MUST treat agent unauthenticated, agent CLI or version unusable, and missing model as non-retryable.
- **FR-003**: The system MUST treat usage limit, engine crash, and transient network as retryable with today's behaviour (usage limit hands the item to the next pool account and resumes at the reset time; transient network error retries; engine crash keeps current retry behaviour).
- **FR-004**: The system MUST, on a non-retryable failure, park the item Jigz-side and record one notice; the park is advisory and leaves the engine's retry path unchanged.
- **FR-005**: The system MUST record exactly one notice per non-retryable failure, visible both as a dashboard alert and as one run event, carrying the failure class and a one-line fix (for example the login command or the upgrade instruction).
- **FR-006**: The system MUST keep the loop running and processing other items when one item fails non-retryably.
- **FR-007**: The restart action MUST clear the parked and retry state added by this change so a parked item returns to the normal claim path on the same recovery path as the existing stuck-issue restart.
- **FR-008**: The user documentation MUST list every failure class and state what each one does.

## Acceptance Criteria

- **AC-1**: A failed attempt caused by an unauthenticated agent, an unusable agent CLI or version, or a missing model is classified as non-retryable (covers FR-001, FR-002).
- **AC-2**: A failed attempt caused by a usage limit, an engine crash, or a transient network error is classified as retryable (covers FR-001, FR-003).
- **AC-3**: On a non-retryable failure the item ends parked Jigz-side with one notice; the loop keeps processing other items (covers FR-004).
- **AC-4**: A non-retryable failure produces exactly one dashboard alert and exactly one run event, both carrying the same failure class and one-line fix; repeated checks and loop cycles add no duplicates (covers FR-005).
- **AC-5**: A usage-limit failure hands the item to the next pool account and the item resumes at the reset time (covers FR-003).
- **AC-6**: A transient-network failure is retried under the existing attempt limit and backoff (covers FR-003).
- **AC-7**: While one item is parked for a non-retryable failure, other items in the same loop continue to be claimed and processed (covers FR-006).
- **AC-8**: Running the single restart action on an item parked by this change clears its parked and retry state and the item becomes claimable again; running it twice does not double-claim (covers FR-007).
- **AC-9**: The user documentation lists each failure class with its retry behaviour and, for non-retryable classes, the one-line fix (covers FR-008).
- **AC-10**: The repository quality gate passes (format check prints nothing, vet, tests, and build pass) (covers all FRs as regression guard).

## Edge Cases

- An attempt whose output matches more than one class (for example limit wording inside an authentication error): one class wins deterministically, with usage-limit wording taking precedence as established by the existing matchers.
- An unknown failure that matches no class: keeps today's retry behaviour and never parks silently without a notice.
- A non-retryable failure on the last item in the loop: the loop stays alive and idle rather than exiting.
- The fix instruction is a single line naming the action (log in, upgrade, configure the model) and does not expose credentials or tokens.
- Concurrent failures on several items: each item gets its own classification and at most one notice; one item's parking never blocks another's retry.

## Out of Scope

- Changing the attempt limit or retry backoff.
- Automatic recovery of a non-retryable failure without a person.
- Changing how the next claim is blocked for an unusable agent (already covered elsewhere).
- Any change to engine-owned retry or parking storage beyond what the plan stage selects as the supported mechanism.

## Clarifications

- None. No open points: the failure wordings are settled by the existing matchers the issue reuses, the retry and parking behaviour is set by the issue text, and the storage mechanism is a plan-stage decision the spec intentionally leaves open.
