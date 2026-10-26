# Working in Jigz

## What it is

`jig` is a Go CLI that wraps the engine and a web dashboard. It sets up config, labels and workflow files in a target repo, then launches one loop process per workflow (poll → claim issue/PR → hand to `pi` agent → record in SQLite). No agent ships in this repo.

## How to run checks

Go 1.26.0 is required (`go.mod` floor from `golang.org/x/sys`).

```sh
gofmt -l cmd internal   # must be empty
go vet ./...
go test ./...           # ~70s; engine-dependent test is skipped if no engine binary is on PATH
go build ./...          # cross-build is exercised in CI
```

`make verify` runs all of the above plus the acceptance test (`make test` and `make acceptance` run the two parts separately). The acceptance test (`cmd/jig/acceptance_test.go`, build tag `acceptance`, about 8 minutes) runs the pinned engine with its `mock` agent under the real `jig` binary against the in-memory fake GitHub in `internal/fakegithub`. `TestAcceptanceConnected` runs `jig daemon` against the fake hub in `internal/fakehub`. `TestAcceptanceUpstreamEngine` repeats the issue workflow on a second engine release v1.29.0, selected through `machine.yaml`, and checks that the PR loop is disabled. Both need their engine from `JIG_ENGINE_BIN` (default engine only) or the share directory, and download it (network on first run) when neither has it. It fails, not skips, when the engine is missing or reports another version. Plain `go test ./...` does not run it and does not need the engine.

CI (`.github/workflows/ci.yml`) runs all of the above on `push` to `main` and on every PR, plus a cross-build matrix (linux/darwin × amd64/arm64).

## Branch / PR workflow

- Do not push to `main`. Create a branch and open a PR.
- One PR per concern; keep commits focused.
- CI must pass before merge. Use `gh pr create` / `gh pr view`.

## Skills

The repo uses the `pi` agent. Any new workflows or prompts belong in `.jigz/workflows/` (installed from the binary at `setup` time; do not edit the installed copy in a target repo by hand without re-running `setup` on upgrade).

## Permissions / notes

- This repo is self-contained. Do not commit secrets (API keys, tokens, .env); use .env.example as the template if needed.
- No secrets in commits. Do not copy `.env` files or tokens into docs.
- The engine binary is fetched at install time and not vendored here.

## Naming

The project and brand is `Jigz` (repository `kinged007/jigz`, Go module `github.com/kinged007/jigz`). The command is `jig`.

- Names that identify data on disk or in the repository use `jigz`: the repository directory `.jigz/`, the per-user directories `jigz` under the config, state, data and cache directories (for example `~/.config/jigz/hub.json`), the workflow databases `jigz-<name>.db`, and `.gitignore` entries.
- Executables, processes, services and environment variables use `jig`: the command `jig` and its subcommands, the background service `jigd` (runs `jig daemon`; systemd unit `jigd.service`, launchd label `io.github.kinged007.jigd`), and the environment variables `JIG_*`.
- New code, docs, environment variables and file names follow these two rules.
- Do not introduce the engine's name in user-facing strings. User-facing text calls it "the engine". The engine's own `SORTIE_*` variables are set only where the code passes them to the engine.
- Environment variables are documented in `README.md` next to the feature they configure.
