# Feature Specification: Configurable assignee scope

**Issue**: #78  **Status**: Clarified  **Areas**: config

## User Scenarios and Testing

### User Story 1 - Monitor items assigned to configured users (Priority: P1)

An operator configures which GitHub users' assigned items the loops monitor, so a deployment can watch items assigned to team members or bots instead of only the token owner's items.
**Independent test**: Set the assignee configuration to another user, start a loop, and confirm it claims an issue assigned to that user and ignores one assigned only to someone else.
**Acceptance scenarios**:
1. Given the assignee configuration lists user `alice`, When an issue assigned to `alice` matches a loop's query, Then the loop claims it.
2. Given the assignee configuration lists user `alice`, When an issue assigned only to `bob` matches a loop's query, Then the loop does not claim it.

### User Story 2 - Empty configuration keeps current behavior (Priority: P1)

An operator who sets no assignee configuration gets today's behavior, so existing deployments keep working with no config change.
**Independent test**: Load a config with no assignee key and confirm loop filters still scope to the token owner.
**Acceptance scenarios**:
1. Given no assignee configuration, When a loop filter is built, Then it scopes to `assignee:@me`.

### User Story 3 - Prompt files reference the configured assignee value (Priority: P1)

A prompt author writes an instruction that creates or assigns an issue using the configured assignee value, so shipped and custom prompts stay correct when the operator changes the configuration in one place.
**Independent test**: Set the assignee configuration, render/run a prompt that references the exposed variable, and confirm the created or updated item carries the configured assignee.
**Acceptance scenarios**:
1. Given the assignee configuration is set, When a prompt references the exposed assignee variable, Then it resolves to the configured value.
2. Given no assignee configuration, When a prompt references the exposed assignee variable, Then it resolves to `@me`.

### User Story 4 - Individual loop overrides the global scope (Priority: P2)

An operator sets a global assignee scope but overrides it for one loop, so a single loop can watch a different user (for example a bot) while the rest watch the team.
**Independent test**: Set a global scope to `alice` and one loop's override to `bot-x`; confirm that loop claims `bot-x` items and ignores `alice`-only items while other loops do the reverse.
**Acceptance scenarios**:
1. Given a global scope of `alice` and a loop-level override of `bot-x`, When that loop's filter is built, Then it scopes to `bot-x` and not `alice`.
2. Given a global scope of `alice` and no override on another loop, When that loop's filter is built, Then it scopes to `alice`.

## Requirements

- **FR-001**: The system MUST accept a top-level `assignees` list of GitHub usernames in the loop configuration.
- **FR-002**: The system MUST default an empty or absent assignee configuration to `@me` (the token owner), preserving current behavior.
- **FR-003**: The system MUST scope every loop's tracker query to items assigned to any one of the configured users (OR semantics across the listed users).
- **FR-004**: The system MUST allow a per-loop assignee override; a loop-level value replaces the global scope for that loop only, and a loop without an override inherits the global scope.
- **FR-005**: The system MUST reject an invalid assignee configuration at config load with an error naming the key and the offending value.
- **FR-006**: The system MUST expose the configured assignee value to prompt files as an environment variable; when unconfigured it resolves to `@me`.
- **FR-007**: The system MUST document the new configuration key, its default, the per-loop override, and the prompt-file variable in the configuration docs.
- **FR-008**: The system MUST render the assignee scope using tracker-query syntax the underlying engine accepts for one and for several users.

## Acceptance Criteria

- **AC-1**: A config with assignees set to `alice` produces loop filters that match items assigned to `alice` and exclude items assigned only to others (covers FR-001, FR-003, FR-008).
- **AC-2**: A config with no assignee key produces the same loop filters as today, scoped to `assignee:@me` (covers FR-002).
- **AC-3**: A config whose assignee scope lists two users produces filters matching items assigned to either user (covers FR-003, FR-008).
- **AC-4**: A loop-level override replaces the global scope for that loop only; loops without an override keep the global scope (covers FR-004).
- **AC-5**: A config with an invalid assignee value fails config load with an error naming the key and value (covers FR-005).
- **AC-6**: A prompt referencing the documented assignee variable resolves to the configured value, or `@me` when unconfigured (covers FR-006).
- **AC-7**: The approved key shape no longer errors as an unknown key (covers FR-001).
- **AC-8**: The configuration docs describe the key, the empty-means-`@me` default, the per-loop override, the prompt-file variable, and the multi-user query form (covers FR-007, FR-008).

## Edge Cases

- Empty list vs absent key: both mean `@me`; a list containing only blank entries is invalid.
- A value of `@me` mixed with named users: matching covers the token owner plus each named user.
- Usernames are matched case-insensitively, as GitHub logins are case-insensitive.
- Existing configs without the key load unchanged; no migration rewrites user files.
- A loop-level override that is an empty list falls back to the global scope; there is no way to express "no assignee scope" — every filter carries an assignee qualifier.

## Out of Scope

- Watching unassigned items or items assigned to anyone (`assignee:*` semantics).
- Validating that a named user exists on GitHub at config load time.

## Clarifications

- Q: What key name and shape should the assignee configuration use? -> A: Top-level `assignees:` list of GitHub usernames (e.g. `assignees: ["alice", "bot-x"]`); empty/absent means `@me`. (chosen by person)
- Q: Should the assignee scope apply globally to all loops, or be overridable per work entry? -> A: Global scope with per-loop override; a loop-level value replaces the global for that loop. (chosen by person; owner amended the global-only suggestion)
- Q: How should prompt files reference the configured assignee value? -> A: Environment variable consistent with existing `JIG_*` vars already usable in prompts (e.g. `JIG_ASSIGNEE`). (chosen by person)
- Q: What query form must the generated filter use? -> A: Tracker-query syntax the underlying engine accepts for single and multiple assignees; verified against the engine rather than invented. (chosen by person)
