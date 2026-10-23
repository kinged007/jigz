# Contributing

## Build and check

```sh
go build ./... && go vet ./... && gofmt -l cmd internal && go test ./...
```

Go 1.26.0 or newer. CI runs the same commands, plus a build matrix for
linux and darwin on amd64 and arm64. Windows is not a target.

## Tests

`go test ./...`, no framework and no external services. Tests that need an
engine find one on `PATH` or in the cache and skip when there is none, so
CI does not have to install the engine. The workflow-validation test is the
exception: it runs the pinned engine when one is present, because
the shipped workflows are only valid against an engine that has the `pi`
adapter and the `github-pr` tracker.

`release_config_test.go` asserts the contract between `install.sh` and
`.goreleaser.yaml`. If you change either asset name, change both.

## Commits

Conventional Commits. One concern per commit — a behavioural fix and a
docs rewrite in the same commit make a bisect useless.

## Engine

Jigz wraps the engine; it is not the engine. Changes to the engine belong
in the engine's repository, not here; this repository only sets the
default release. Users can select another release in `machine.yaml` or
with `JIG_ENGINE_REPO` and `JIG_ENGINE_VERSION`.

Bumping `DefaultTag` in `internal/engine/engine.go` is the whole change,
but it is not free: the workflows are written against known engine
behaviour, so a bump needs the engine's `validate` command run against the
new engine with the shipped workflows. `go test ./...` covers that when an
engine is on `PATH`.

## Reporting

Bugs and features: GitHub issues. Security: see
[SECURITY.md](SECURITY.md) — that is a private channel, not a public
issue.
