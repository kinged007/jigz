# Engine-interface handoff (phase 3a: `internal/gen`)

## Status

`internal/gen` is built directly on `feat/engine-interface`
(uncommitted). It compiles; `gofmt`, `go vet`, `go build ./...` are
clean. The test suite is green: 6/6 tests pass. Nothing is merged.
Next step is to merge `--no-ff` into `feat/engine-interface` and
dispatch phase 3b.

```sh
git status --short   # M internal/config/work.go, M plans/jigz/engine-interface*.md, ?? internal/gen/
go test ./internal/gen/ -count=1
```

2026-10-29: all six tests pass; goldens regenerated after the
fixture/skeleton fixes below. Real-engine check on the fork
(`sortie-kinged007+sortie-1.28.1 validate --format json`): the shipped
renders, the Example 1 goldens and a split loop all report
`{"valid":true}` with empty warnings. Owner decisions recorded:
generator stays on `jig agent-exec` while shipped says `claude`
(shipped-diff allowlists command + runtime blocks until 3b rewires
the CLI); `defaultFixture` stays Example 1 verbatim and a separate
`shippedFixture` (5 + 3 rules) feeds `TestShippedDiff`.

## Failing tests (exact)

- `TestRenderDefault` (`gen_test.go:113`): all four goldens differ.
  Regenerate only after the fixture/skeleton fixes below, then
  `go test ./internal/gen/ -update` and review the diff.
- `TestLoopSplitting` (`gen_test.go:173`): same cause; plus queries carry
  `label:in-progress -label:needs-human` style narrowing while the plan
  wants disjoint combined queries + narrowed `active_states`.
- `TestProfile` (`profile_test.go:58,68,81`):
  - `ProfileFor("", "")` returns name `""`; test wants `"fork"`.
  - `ProfileFor("sortie-ai/sortie", "v1.29.0")` returns `""`; test wants
    `"upstream"`. Exact matches return no name.
  - `ProfileFor("", "v1.29.0")` errors
    `unsupported engine kinged007/sortie@v1.29.0`: version equals newest
    (`v1.29.0`) but repo differs, so the newer-than-newest fallback misses.
- `TestSplitStages` (`profile_test.go:126,128`):
  - planner loop errors but without the `replacing the label with` prompt
    fix the test wants (`Validate` says `hand the item over by changing
    its labels instead`).
  - builder loop renders clean, test wants an error. The `next` lives in
    the *other* loop, so per-loop `Validate` cannot see it.
- `TestRenderNegatives` (`profile_test.go:139,153`):
  - unknown agent (`agent: missing`) fails in `config.Load`
    (`work.issues.agent: unknown agent or pool`), never reaching `Render`.
  - unknown model (`model: x`, mislabeled "unknown tracker kind") renders
    nil error: neither `Load` nor `Render` rejects it.
- `TestShippedDiff` (`shipped_test.go:73`, 225 diff lines, `/tmp/shipped.log`):
  generated output differs from `cmd/jig/workflows/WORKFLOW.{issues,prs}.md`
  on nearly every block. Byte diffs: `/tmp/gen-issues.md`,
  `/tmp/gen-prs.md` vs the shipped files.

## Root causes

1. Fixture is not section 3.5 Example 1. `defaultFixture` (`gen_test.go`)
   lacks `operating_hours: {}`, the Madrid reviewer schedule, per-entry
   `active_states`, and uses single-rule queries. Shipped queries list all
   trigger labels
   (`label:agent:plan,agent:plan-build,agent:build,agent:docs,in-progress -label:needs-human`).
2. Skeleton is not the shipped shape. `skeleton.tmpl`/`render.go` emit:
   own comment text, block-style `active_states`, `terminal_states:
   [done, cancelled]` (shipped: `[closed]` with backstop/handoff comments),
   `comments` block shape, `interval_ms: 5000` (shipped: 60000),
   `$JIG_TRACKER_TOKEN`/literal `owner/repo` (shipped:
   `$SORTIE_TRACKER_API_KEY`/`$SORTIE_TRACKER_PROJECT`),
   `template: prompts/...` (shipped: `./prompts/...`), per-rule `agent:`,
   multi-line labels, one runtime block with a "only the block" comment
   (shipped: both `pi:` and `claude-code:` blocks + pool-override comment),
   no trailing `{{/* ... */}}` body comment.
3. Test/code contract mismatches (ProfileFor names, cross-group `next`
   message + check level, negatives expecting `Render` errors for `Load`
   failures) as listed above.

## Fix order

1. `profile.go`: return `"fork"`/`"upstream"` names on exact match; fix the
   newer-than-newest fallback for same-version-different-repo
   (`ProfileFor("", "v1.29.0")` must not error).
2. Decide cross-group `next`: validate at split level (all loops of the
   entry together) so both loops error, or fix the test to expect only the
   planner error. Either way, make `Validate`'s message contain
   `replacing the label with` (section 3.7 wording).
3. `TestRenderNegatives`: expect the `Load` error for unknown agent
   (or construct `Loop` by hand to test `Render`); decide where unknown
   model is rejected (`validateModel` exists in `internal/config/work.go`).
4. Fixture: make `defaultFixture` section 3.5 Example 1 verbatim (Madrid
   schedule, `operating_hours: {}`, no top-level `active_states`).
5. Skeleton/render: align to shipped files (item 2 above), keeping the
   marker line (`# jig:generated v1 skeleton=<kind> engine=<repo>@<version>
   sha256=<hex>`) and the `db_path` move as the only allowed diffs.
6. `go test ./internal/gen/ -update`, review every golden diff, run the
   real-engine check: rendered docs must `validate {"valid":true}` with no
   warnings on the fork engine (section 7.2 needs empty warnings).

## Open plan tensions (do not paper over)

- Section 4.6 says the generator writes
  `command: /usr/local/bin/jig agent-exec <agent>`; shipped docs say
  `command: claude` with a `machine.yaml` pool-override comment. If byte
  equality (section 4.1) holds, the shipped docs change too, or 4.6 does.
- Section 4.1 says Example 1 (one rule per entry) corresponds to the shipped
  docs; shipped `issues` has five rules (plan, plan-build, build, docs,
  build-chain) and `prs` has three. The fixture that reproduces the shipped
  files is bigger than Example 1.
- `agent.command` stays a single string (mapping rejected by the engine);
  standby agents are regeneration at idle (section 6.3).

## Map

- `internal/gen/profile.go` (releases, `ProfileFor`, `Supports*`),
  `loop.go` (`Split`, `combine`, `activeStates`),
  `render.go` (`Render`, `Validate`, `stringOf`),
  `skeleton.tmpl`, `defaults.go` (tracker-kind hooks; prs has no
  `after_run`), `file.go` (`Check`, `AtomicWrite`, `DocPath`, `Managed`,
  `Manifest`), `*_test.go`, `testdata/golden/fork-1.28.1/`.
- `internal/config/work.go`: `Resolve()`, `StringOrRef.Unresolved()`,
  `UnresolvedRefs()`, `validateModel`; only change there is `Unresolved()`.
- Engines: `/home/coder/.local/share/jigz/sortie-kinged007+sortie-1.28.1`
  (fork, has `github-pr`+`pi`) and `.../sortie-sortie-ai+sortie-1.29.0`
  (upstream, neither). Real-engine validation used fork + `validate`,
  with `HOME`/`JIG_WORKSPACES`/`JIG_TRACKER_TOKEN` set.
- Plan: `plans/jigz/engine-interface.md` section 12 (status) + `engine-interface-notes.md`
  (code map, probe/fixture findings). Queued after green: 3b CLI wiring
  (setup/validate/reload/start, drift + `.bak`, query-change drain),
  phase 4 fallback, 5 contract tests + CI, 6 cleanup, 7.
- Keep chunks small: `coding-manual` cap is `maxTokens: 16384`; stay inside
  `internal/gen`, one file per step.
