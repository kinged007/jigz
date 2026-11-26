# Feature Specification: Dismiss a dashboard alert for the occurrence shown

**Issue**: #83  **Status**: Draft  **Areas**: area:dashboard, area:docs

## User Scenarios and Testing

### User Story 1 - Dismiss an alert that needs no action (Priority: P1)

A person watching the Operations page sees an alert they have assessed and decided needs nothing done (for example a parked item they understand, or a disk warning they accept). They dismiss it, and it stays hidden on every later view of that same occurrence, including after a dashboard restart.

**Independent test**: raise a parked-item alert, dismiss it through the page control, reload the page and restart the dashboard; the alert stays hidden each time.

**Acceptance scenarios**:

1. Given an alert is shown on the Operations page, When the person dismisses it, Then the next render hides it.
2. Given an alert was dismissed, When the dashboard restarts, Then the alert is still hidden.
3. Given an alert was dismissed, When the person opens the collapsed dismissed-alerts area, Then the occurrence is listed with its dismissal time.

### User Story 2 - A later occurrence of the same problem alerts again (Priority: P1)

A person dismissed an earlier occurrence. The problem goes away and comes back, or the item loops around and is held again. The new occurrence is shown as a fresh alert; the old dismissal never hides it. Identity is the occurrence time, not the message text.

**Independent test**: dismiss occurrence 1 of a held item, then create occurrence 2 of the same kind for the same item; occurrence 2 is shown while occurrence 1 stays hidden.

**Acceptance scenarios**:

1. Given item X was held at time A and that alert was dismissed, When item X is held again at time B, Then an alert for time B is shown.
2. Given item X's held alert was dismissed, When another item Y is held, Then Y's alert is shown.
3. Given a warning cleared and returned (for example disk use dropping below then rising above the threshold), When it returns, Then it is shown again as a new occurrence.

### User Story 3 - Restore a dismissed alert (Priority: P2)

A person dismissed an alert and later wants it back (conditions changed, or the dismissal was a mistake). They restore it from the dismissed-alerts area, and the alert is shown again while its cause still holds.

**Independent test**: dismiss an alert, restore it through the undo control, reload; the alert is shown again.

**Acceptance scenarios**:

1. Given a dismissed occurrence is listed in the dismissed area, When the person restores it, Then the alert is shown again on the next render.
2. Given a dismissed occurrence whose cause has cleared, When the person restores it, Then no alert is shown (there is nothing to show), and the record reflects that the dismissal was withdrawn.

## Requirements

- **FR-001**: The system MUST offer a dismiss control on each alert shown on the Operations page, working without client-side scripting.
- **FR-002**: A dismissal MUST hide only the exact occurrence dismissed (alert kind plus subject plus occurrence time). It MUST NOT hide other occurrences of the same kind, other subjects, or later occurrences of the same subject.
- **FR-003**: Dismissals MUST be stored durably in the dashboard's own per-machine state, so they survive a dashboard restart, and MUST apply only to the dashboard that recorded them.
- **FR-004**: For held items (parked issues and budget-held issues), the occurrence identity MUST be the engine's own recorded timestamp for that hold, so restarting the loop machinery does not turn an undismissed alert into a new one.
- **FR-005**: For alerts whose sources carry no occurrence timestamp, the system MUST derive the occurrence from the first sighting of the current continuous run of that alert: sightings are recorded by a background pass that does not depend on page views; when the alert stops being computed the run is closed; a reappearance after a closed run gets a new occurrence.
- **FR-006**: The occurrence of a failure-streak alert is [NEEDS CLARIFICATION: the start of the current streak (oldest consecutive failure), so the dismissal holds while the same streak keeps failing, versus the newest failed run, so each new failure in the streak re-alerts].
- **FR-007**: The system MUST list every dismissed occurrence, with its dismissal time, in a collapsed area on the Operations page, each with a restore control; restoring MUST show the alert again while its cause still holds. This is a single hide/restore pair, not an acknowledge workflow.
- **FR-008**: The system MUST refuse a dismissal for an alert occurrence it is not currently computing, so a stale page cannot pre-hide a future occurrence.
- **FR-009**: Dismissing or restoring MUST NOT change what any loop does: no writes to loop state, supervisor status, or the tracker.
- **FR-010**: The dismiss and restore actions MUST carry the same write-action protections as the dashboard's existing restart action (same request method conventions, forgery protection, and host checks); every other page and route MUST remain without write effects.
- **FR-011**: When the dashboard's state storage is unavailable, dismissal MUST be refused with an error status and alerts MUST stay shown; the dashboard MUST NOT crash.
- **FR-012**: The user documentation MUST describe dismissal, the occurrence rule with the park/dismiss/re-park example, the per-dashboard scope, and the caveat that serving the dashboard on a non-loopback address exposes dismissal to anyone who can reach it; the read-only claim MUST be narrowed to everything except dismissal, restore, and the existing restart action.

## Acceptance Criteria

- **AC-1**: Each alert on the Operations page carries a working dismiss control (covers FR-001).
- **AC-2**: A dismissed alert stays hidden on the next render and after a dashboard restart (covers FR-001, FR-003).
- **AC-3**: In the park/dismiss/re-park sequence, occurrence 1 dismissed stays hidden while occurrence 2 of the same kind and item is shown (covers FR-002).
- **AC-4**: Dismissing a held alert for one item at one time hides neither the same item held at another time nor another held item (covers FR-002).
- **AC-5**: A warning that clears for at least one background sighting pass and returns is shown again; the earlier dismissal does not hide it (covers FR-005).
- **AC-6**: Restarting the loop machinery without changing hold state introduces no new alert identities: undismissed alerts stay shown as the same occurrence and dismissed ones stay hidden (covers FR-004).
- **AC-7**: Dismissed occurrences are listed with dismissal times and a restore control; after restore the alert is shown again while its cause holds (covers FR-007).
- **AC-8**: A dismissal submitted for an occurrence the dashboard is not currently computing is refused and hides nothing (covers FR-008).
- **AC-9**: Dismissing and restoring change no loop behaviour: loop state, supervisor status, and tracker state are byte-identical before and after (covers FR-009).
- **AC-10**: All write protection checks for the existing restart action also guard dismiss/restore; every other route still rejects non-read methods, and no page offers a submitting control except dismiss, restore, and restart (covers FR-010).
- **AC-11**: With state storage unavailable, a dismissal attempt fails with an error status, alerts remain shown, and the dashboard keeps serving (covers FR-011).
- **AC-12**: The documentation states the occurrence rule with the park/dismiss/re-park example, the per-dashboard scope, the exposed-binding caveat, and the narrowed read-only claim (covers FR-012).
- **AC-13**: The repository's quality gate (format check, vet, tests, build) passes.

## Edge Cases

- The same occurrence dismissed twice: the second dismissal is accepted idempotently or refused; either way exactly one hidden record results and nothing else changes.
- Dismiss and restore submitted at the same time for one occurrence: one wins deterministically; the record ends hidden or restored, never duplicated or corrupt.
- Restore of an occurrence whose cause already cleared: nothing is shown; the withdrawal of the dismissal is still recorded.
- Message text changes while the occurrence is unchanged (for example a disk percentage moving within the same continuous warning run): identity follows the occurrence time, so the dismissal still applies.
- A failure streak continues with further failures after a dismissal: whether the person is re-alerted follows the FR-006 decision.
- A dismissed warning flapping exactly at the threshold: each clear-then-return cycle observed by the background pass is a new occurrence and is shown.
- Two dashboards on one machine: each keeps its own dismissal set; dismissing on one does not hide the alert on the other.
- Dashboard served on a non-loopback address: dismissal is reachable by anyone who can reach the dashboard; this is documented, not prevented.
- Table-style tests and no-dashboard modes with no state storage open: dismissal is unavailable per FR-011.

## Out of Scope

- Acknowledging versus dismissing, notification channels, e-mail, or per-person seen-by records; the dashboard has no user identity and the recorded actor is the machine.
- A command-line interface for alerts.
- Sharing dismissals between dashboards on one machine or with any central instance, which keeps its own state.
- Suppressing alerts by kind, project, or threshold through configuration.
- Fixing the wrong destination of the held-alert link; that is a separate bug issue.

## Clarifications

- Q: Sequencing after the restart-action and classify-and-park work, reusing the existing write-action handling, and keeping the held-alert link fix separate -> A: Yes: build on the current development head reusing the existing POST and forgery handling; keep changes separate from the held-alert link fix and rebase onto it if it merges first (chosen by person, the repository owner, in issue comments).
- Q: Dismissal scope is per dashboard on this machine, and an exposed binding exposes dismissal -> A: Yes, documented per FR-012 (chosen by person, the repository owner, via the issue description).
- Q: Failure-streak occurrence identity (start of streak versus newest failure) -> A: pending; see the blocking question on the issue and [NEEDS CLARIFICATION] in FR-006.
