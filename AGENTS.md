# Working in sortie-loop

## What it is

`sortie-loop` is a Go CLI that wraps the `sortie` engine and a web dashboard. It sets up config, labels and workflow files in a target repo, then launches one loop process per workflow (poll → claim issue/PR → hand to `pi` agent → record in SQLite). No agent ships in this repo.

## How to run checks

Go 1.26.0 is required (`go.mod` floor from `golang.org/x/sys`).

```sh
gofmt -l cmd internal   # must be empty
go vet ./...
go test ./...           # ~70s; engine-dependent test is skipped if no `sortie` binary on PATH
go build ./...          # cross-build is exercised in CI
```

CI (`.github/workflows/ci.yml`) runs all of the above on `push` to `main` and on every PR, plus a cross-build matrix (linux/darwin × amd64/arm64).

## Branch / PR workflow

- Do not push to `main`. Create a branch and open a PR.
- One PR per concern; keep commits focused.
- CI must pass before merge. Use `gh pr create` / `gh pr view`.

## Skills

The repo uses the `pi` agent. Any new workflows or prompts belong in `.sortie/workflows/` (installed from the binary at `setup` time; do not edit the installed copy in a target repo by hand without re-running `setup` on upgrade).

## Permissions / notes

- This repo is self-contained. Do not commit secrets (API keys, tokens, .env); use .env.example as the template if needed.
- No secrets in commits. Do not copy `.env` files or tokens into docs.
- The engine (`sortie` binary) is fetched at install time and not vendored here.
