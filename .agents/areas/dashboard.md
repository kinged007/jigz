# area:dashboard

## Paths

- Handlers and pages in `cmd/jig`: `web.go`, `pages.go`, `api.go`, `charts.go`, `runs.go`, `rundetail.go`, `runparts.go`, `runlive.go`, `livelog.go`, `metrics.go`, `usage.go`, `agents.go`, `logs.go`, `timeline.go`, `workflows.go`, `settings.go`, `projects.go`, `questions.go`, `pipeline.go`, `unite.go`, `hide.go`.
- `cmd/jig/templates/*.html` (embedded, `html/template`).
- `internal/collector` (copies runs and samples into `history.db`), `internal/store` (history database), `internal/rollup` (reads loop databases), `internal/runlive`, `internal/loopsrc`, `internal/github`, `internal/questions`, `internal/host`.

## Purpose

A read-only web view of loops, runs, usage, questions and host load, plus the shared `--unite` view.

## Entry points

`cmd/jig/web.go`, `cmd/jig/dashboard.go`, `internal/store/store.go`, `internal/rollup/schema.go`.

## Conventions

- The dashboard writes nothing to GitHub or to loops. `readonly_test.go` fails when a write route exists. Workflow databases are only read.
- Store schema changes are new entries appended to the `migrations` list in `internal/store/store.go`. Never edit an existing entry.
- Reads are bounded. `internal/runlive` stays inside the directories it is given.
- `internal/rollup/schema.go` lists the engine releases and their schema versions. A newer unknown schema is not read. Add a release only with its fixture (`make schema-fixture`).

## Pitfalls

- Templates are parsed at start, so a template error shows up in tests only if a test renders that page.
- Questions follow the resume rule in `docs/configuration.md` (authorized authors only).

## Focused checks

```sh
go test ./internal/collector/... ./internal/store/... ./internal/rollup/... ./internal/runlive/... ./internal/questions/...
go test ./cmd/jig/ -run 'Web|Dashboard|Runs|Usage|Questions|Readonly|Livelog'
```

The full gate is in `AGENTS.md`.
