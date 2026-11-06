# area:cli

## Paths

`cmd/jig/*.go` (package main), `cmd/jig/scripts/cleanup.sh`.

## Purpose

The `jig` command. `parseArgs` in `main.go` switches on the subcommand name. `setup` writes `.jigz/` in a target repo. `start`, `join` and `leave` run or connect loops. `stop`, `restart`, `status`, `cleanup`, `wake`, `pause`, `resume`, `budget`, `reload` and `validate` go through `runRemote`. `agent-exec` is the agent command the engine runs.

## Entry points

- `run()`, `parseArgs()` and `usage()` (help text) in `cmd/jig/main.go`
- `runRemote` in `cmd/jig/remote.go`
- `runValidate` in `cmd/jig/validate_cmd.go`
- `runSetup` in `cmd/jig/setup.go`

## Conventions

- The dashboard is read-only. Every control is a CLI command; there are no dashboard write routes.
- Names on disk use `jigz`. Executables, processes, services and env vars use `jig` and `JIG_*`.
- User-facing text calls the engine "the engine".
- Document every new env var in `docs/` next to its feature.
- A new subcommand: add it to `parseArgs` and `usage()` in `main.go`, to `docs/usage.md`, and add a test in `cmd/jig/*_test.go`. Tests call the functions directly, with `t.TempDir()` and `t.Setenv`.

## Pitfalls

- `cmd/jig` is one large package. A new top-level name can collide with an existing one; grep first.
- Tests that start child processes are slow; `go test ./...` takes about 70s.
- `release_config_test.go` ties `install.sh` to `.goreleaser.yaml`.
- The acceptance test (`make acceptance`, about 8 minutes) is not part of `go test ./...`.

## Focused checks

`go test ./cmd/jig/...`. The full gate is in `AGENTS.md`.
