# area:config

## Paths

- `internal/config`: `.jigz/config.yaml` loading, `work.go` (rules and resolution), `strict.go` (unknown-key check), `DefaultLabels` in `config.go`.
- `internal/gen`: renders `WORKFLOW.*.md` from the resolved config; `skeleton.tmpl`, `profile.go` (one row per engine release).
- `internal/machine`: `machine.yaml` (hours, pool, limits).
- `internal/plan`, `internal/engine` (engine binary resolution, `DefaultTag`), `internal/fleetconfig`, `internal/dirs`.
- `cmd/jig/workflows`: shipped documents and prompts installed by `setup`.
- `cmd/jig/generate.go`.

## Purpose

Turn config into one engine document per loop. Rules with different agents become separate loops, `WORKFLOW.<slug>-<agent>.md`.

## Entry points

`internal/config/config.go`, `internal/config/work.go`, `internal/gen/skeleton.tmpl`, `cmd/jig/generate.go`.

## Conventions

- Config keys are strict; an unknown key is an error. A new key needs a yaml tag, a default and a test in `internal/config`.
- Generated documents carry a `jig:generated` digest header. Never hand-edit them.
- Golden files in `internal/gen/testdata/golden` change only when the generator changes. Regenerate with `go test ./internal/gen/ -update` and review the diff. `TestShippedDiff` compares the render with `cmd/jig/workflows/WORKFLOW.*.md`.
- `cmd/jig/labels_test.go` limits the labels the shipped prompts may name. Prompts are Go templates run with `missingkey=error`.
- Engine bump: changing `DefaultTag` needs a profile row in `internal/gen/profile.go` and the schema fixture (`make schema-fixture`); see CONTRIBUTING.md.

## Pitfalls

- A config change usually needs a docs change in `docs/configuration.md` (area:docs).

## Focused checks

`go test ./internal/config/... ./internal/gen/... ./internal/machine/... ./internal/engine/...`. The full gate is in `AGENTS.md`.
