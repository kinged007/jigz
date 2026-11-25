# Feature Specification: Held alert link shows the held item

**Issue**: #84  **Status**: Clarified  **Areas**: dashboard

## User Scenarios and Testing

### User Story 1 - Open a held alert and see the held item (Priority: P1)

A person watching the Operations page sees a held alert (parked issue or budget hold) and follows its Open link to investigate. The destination shows that held item: which loop holds it, which issue it is, why it is held, and when the hold started. This serves the Q1 exit criterion of recovering a stuck run from the dashboard in under a minute.

**Independent test**: with a dashboard backed by data containing one parked issue and one budget hold, open each held alert's link and confirm the landing view names the held issue, its loop, its reason, and when the hold started.

**Acceptance scenarios**:
1. Given a parked issue exists on a loop, When the user follows the Parked issue alert's link, Then the landing view shows that issue's identifier, its loop, its reason, and when parking started.
2. Given a budget hold notice exists on a loop, When the user follows the Budget hold alert's link, Then the landing view shows that issue's identifier, its loop, its reason, and when the hold started.
3. Given a held alert, When the user reads the alert text, Then the text says what the link shows and where it leads.

### User Story 2 - Existing alert links keep resolving (Priority: P2)

A person follows any other alert link (for example a failure streak) and still lands on a view that shows the alert's subject, so fixing the held link does not break the links that already work.

**Independent test**: with data containing one failure streak and one held item, follow each alert link and confirm each landing view mentions that alert's subject.

**Acceptance scenarios**:
1. Given a failure streak alert, When the user follows its link, Then the landing view is filtered so the streaked item is reachable and named.

## Requirements

- **FR-001**: A held alert's link MUST land on a view that shows that held item: the loop, the issue, the reason, and when the hold started.
- **FR-002**: The alert text MUST describe what the link shows, so the user knows what to expect at the destination.
- **FR-003**: The held-item data exposed to the view MUST include the hold start date and the human-readable issue identifier alongside the existing identity and reason.
- **FR-004**: Reading held items MUST tolerate engine databases that lack the newer columns or tables, degrading to what is available instead of failing or hiding all held data.
- **FR-005**: The dashboard MUST stay read-only: no new write path to a loop database, status document, or GitHub, and no route that changes stored state.
- **FR-006**: The user-facing dashboard documentation MUST describe where held items are shown and what each held alert link opens.

## Acceptance Criteria

- **AC-1**: Following a Parked issue alert link shows the parked issue with its loop, issue identifier, reason, and hold start (covers FR-001, FR-003).
- **AC-2**: Following a Budget hold alert link shows the budget-held issue with its loop, issue identifier, reason, and hold start (covers FR-001, FR-003).
- **AC-3**: The alert text names what the link shows (covers FR-002).
- **AC-4**: With an engine database missing the newer hold columns, the alerts and the held view still render from the available identity and reason without error (covers FR-004).
- **AC-5**: An automated test asserts that every alert link resolves to a view containing that alert's subject, covering at least one held alert and one streak alert (covers FR-001).
- **AC-6**: The existing read-only dashboard test suite passes unchanged in behaviour: no new write route and no write to loop data, status, or GitHub (covers FR-005).
- **AC-7**: The dashboard documentation describes the held view (or the new link target) and what each held alert opens (covers FR-006).
- **AC-8**: Format check, static analysis, tests, and build all pass (covers all FRs as a quality gate).

## Edge Cases

- No held items: the held view or block is absent or states there is nothing held; no empty or broken link is emitted.
- Hold tables absent (older engine database): no held alerts and no held view entries, without errors.
- Newer columns absent but tables present: identity and reason still shown; date and display identifier fall back to what is available.
- Several held items across loops of one project: each item is listed under its own loop and each alert reaches its item.
- Held issue identifier that never ran (no run history to cross-reference): the item is still shown from the hold record alone.

## Out of Scope

- Dismissing alerts, stored alert state, and any write route: tracked in #83.
- Changing what the engine records about parked issues or budget holds: the dashboard only reads.
- Recovery actions on held items (unpark, retry, restart): only showing the item so the existing one-action recovery flow can proceed.

## Clarifications

- No open clarification points: the issue text settles the design direction (preferred: a Held block on the project view listing both hold kinds per loop with rows linking to the tracked item; acceptable alternative: retarget the link to a view that does show the item and adjust the alert text). The plan stage chooses between these two within the acceptance criteria above.
