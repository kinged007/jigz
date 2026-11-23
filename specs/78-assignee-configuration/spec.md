# Feature Specification: Configurable assignee scope

**Issue**: #78  **Status**: Draft  **Areas**: config

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

## Requirements

- **FR-001**: The system MUST accept an assignee configuration in `.jigz/config.yaml` [NEEDS CLARIFICATION: exact key name and shape — `assignee` vs `assignees`, single value vs list].
- **FR-002**: The system MUST default an empty or absent assignee configuration to `@me` (the token owner), preserving current behavior.
- **FR-003**: The system MUST scope every loop's tracker query to items assigned to any one of the configured users (OR semantics across the listed users).
- **FR-004**: The system MUST apply the assignee scope [NEEDS CLARIFICATION: globally for all loops vs overridable per work entry].
- **FR-005**: The system MUST reject an invalid assignee configuration at config load with an error naming the key and the offending value.
- **FR-006**: The system MUST expose the configured assignee value to prompt files [NEEDS CLARIFICATION: environment variable vs template field, and its exact name].
- **FR-007**: The system MUST document the new configuration key, its default, and the prompt-file variable in the configuration docs.

## Acceptance Criteria

- **AC-1**: A config with assignees set to `alice` produces loop filters that match items assigned to `alice` and exclude items assigned only to others (covers FR-001, FR-003).
- **AC-2**: A config with no assignee key produces the same loop filters as today, scoped to `assignee:@me` (covers FR-002).
- **AC-3**: A config whose assignee scope lists two users produces filters matching items assigned to either user (covers FR-003).
- **AC-4**: A config with an invalid assignee value fails `jig setup` / config load with an error naming the key and value (covers FR-005).
- **AC-5**: A prompt referencing the documented assignee variable resolves to the configured value, or `@me` when unconfigured (covers FR-006).
- **AC-6**: The configuration docs describe the key, the empty-means-`@me` default, and the prompt-file variable (covers FR-007).
- **AC-7**: The previously-reserved `assignee` key no longer errors as an unknown key once configured in the approved shape (covers FR-001).

## Edge Cases

- Empty list vs absent key: both mean `@me`; a list containing only blank entries is invalid.
- A value of `@me` mixed with named users: matching covers the token owner plus each named user.
- Usernames are matched case-insensitively, as GitHub logins are case-insensitive.
- Existing configs without the key load unchanged; no migration rewrites user files.

## Out of Scope

- Per-work-entry assignee overrides (unless Q2 decides otherwise).
- Watching unassigned items or items assigned to anyone (`assignee:*` semantics).
- Validating that a named user exists on GitHub at config load time.

## Clarifications

- (open) Q1, Q2, Q3 below.
