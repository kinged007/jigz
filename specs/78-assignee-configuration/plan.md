# Plan: Configurable assignee scope (#78)

Spec: `specs/78-assignee-configuration/spec.md` (Status: Clarified; all owner questions answered, recorded in spec).

## Approach

Centralize the assignee scope in `internal/config` and reuse it at every
filter-building site. Today ` assignee:@me` is hardcoded in three places
(`Config.FilterFor` in `internal/config/config.go:539`,
`cmd/jig/supervise.go:249`, `cmd/jig/main.go:362`); all three must render
the resolved scope instead. Design:

- Top-level `assignees:` list of GitHub usernames on the config file and
  resolved `Config`; empty/absent means `@me` (no behavior change).
- Per-work-entry `assignees:` override (`work.<slug>.assignees`); a set
  non-empty list replaces the global scope for that loop only, nil/empty
  inherits the global scope. There is no "unscoped" filter.
- One helper (e.g. in `internal/config`) renders the scope fragment:
  single user `assignee:<name>`, empty `@me`, multi-user in the
  engine-accepted form verified by task T001 below.
- Validate usernames at config load (GitHub login rules, `@me` allowed,
  blank entries rejected) with errors naming the key and offending value.
- Expose the resolved scope value to prompts/agents as `JIG_ASSIGNEE` in
  `Config.Env` (comma-joined for several users, `@me` when unconfigured),
  so it flows through the existing `cfg.Env(token)` call sites
  (supervise, validate, app).
- Keep the singular `assignee` entry in `removedKeys`; the new key is the
  plural `assignees`, a different key, so the existing strict-key test
  stays valid.

Why this shape: it follows the existing `tracker`/`hooks` pattern (global
defaults + per-work overlay), keeps one rendering site so the three
filter builders cannot drift, and reuses the `Env` plumbing prompts and
hooks already consume.

## Files to change

- `internal/config/config.go` — add `Assignees` to `configFile` and
  `Config`; scope helper; `FilterFor` uses per-loop scope; `Env` adds
  `JIG_ASSIGNEE`; load-time validation; wire global into resolved config.
- `internal/config/work.go` — add `assignees` to `workFile`/`Work`,
  inherit-or-replace resolution, validation via `validateTracker`-adjacent
  helper.
- `internal/config/strict.go` — no change expected (`assignees` becomes a
  known yaml key); keep `assignee` (singular) in `removedKeys`.
- `cmd/jig/supervise.go:249`, `cmd/jig/main.go:362` — replace hardcoded
  `" assignee:@me"` with the resolved scope for that loop.
- `internal/fakegithub/query.go` — only if T001 shows the engine accepts a
  multi-term form the fake parses as AND; align fake OR semantics with the
  engine and extend its tests.
- `docs/configuration.md` — document `assignees`, the `@me` default, the
  per-loop override (keys table ~line 231), `JIG_ASSIGNEE` (env sections
  ~lines 342-373, 717), and the multi-user query form.
- Tests next to the code: `internal/config/config_test.go`,
  `internal/config/work_test.go` (or `work_keys_test.go`),
  `internal/config/strict_test.go`, plus fake-github tests if touched.

## Data and contract changes

- Config schema: new optional top-level `assignees: [string]` and optional
  `work.<slug>.assignees: [string]`. Backward compatible: absent key loads
  exactly as today. Forward note: old binaries reject the new key as an
  unknown key.
- Process environment: new `JIG_ASSIGNEE` variable on engine subprocesses
  (value `@me` or comma-joined usernames). Additive; no existing variable
  changes.
- Tracker query strings now carry `assignee:<users>` instead of always
  `assignee:@me`. No persisted data or DB schema changes. No new
  dependencies.

## Test plan

Write failing tests first, one per AC, in `internal/config` (and
`internal/fakegithub` if touched):

- AC-1: config with `assignees: [alice]` → `FilterFor` yields
  `... assignee:alice`.
- AC-2: config without the key → filter identical to today
  (`assignee:@me`).
- AC-3: two users → filter matches items assigned to either (form per
  T001 verdict).
- AC-4: global `alice` + loop override `bot-x` → that loop scopes to
  `bot-x`, others to `alice`; empty override inherits global.
- AC-5: blank/invalid entry → load error naming key and value.
- AC-6: `Env` contains `JIG_ASSIGNEE=<configured>`; `@me` when unset.
- AC-7: config with `assignees` loads (no unknown-key error); singular
  `assignee` still rejected with the existing message.
- AC-8: docs review (manual).
- Smoke test: `go build ./...`, then run the built `jig validate` (the
  repo's documented config check, `cmd/jig/validate.go`) against a
  fixture config using `assignees` plus a per-loop override, and confirm
  the reported loop filters carry the expected scope.

## Risks

- Multi-user query syntax is engine-defined, not yet confirmed; a wrong
  guess silently over- or under-claims items. Mitigation: T001 verifies
  against the engine (docs/schema or live query) before implementation;
  `internal/fakegithub` (AND across `assignee:` terms today) may need the
  same fix or tests will encode the wrong semantics.
- Three filter-building sites must stay in sync; a missed site leaves one
  path on `@me`. Mitigation: single helper, and grep for `assignee:@me`
  in review.
- `JIG_ASSIGNEE` multi-value serialization (comma-joined) is a new
  convention prompt authors must follow; documented in `docs/`.

## Rollback

Revert the change as one unit (config + call sites + docs). Configs using
`assignees` fail fast on the old binary with `unknown key "assignees"`,
so rolling back the binary requires removing the key from deployed
configs first; rolling forward needs no migration.
