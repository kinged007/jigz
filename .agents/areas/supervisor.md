# area:supervisor

## Paths

- `cmd/jig/supervise.go` (loop processes, signals, status document), `gate.go` (work-entry gate: operating hours, budget, agent pool), `daemon.go` (`jig daemon`, connected mode), `join.go`, `sleep.go` (idle sleep and `jig wake`), `budget.go`, `agentexec.go`.
- `internal/supervise`, `internal/service` (the `jigd` systemd unit and launchd agent).
- `internal/hubclient`, `internal/hubproto`, `internal/hubstate`, `internal/source`, `internal/fakehub`.
- `schemas/hub-protocol/v0`.

## Purpose

Start, watch and stop one engine process per workflow, enforce hours, budgets and pools, and talk to a hub (protocol v0).

## Entry points

`cmd/jig/supervise.go`, `cmd/jig/gate.go` (`gateDecide`), `cmd/jig/daemon.go`, `internal/supervise/supervise.go`.

## Conventions

- A loop is stopped by signalling the process that owns it. Only the process watching the loop decides when.
- Every spawned child is torn down on exit, error or Ctrl-C. No orphans.
- Hub errors name the method and route only and never contain header values or URL user info. Keep the redaction intact.
- Hub protocol: the Go types in `internal/hubproto` and the JSON Schemas in `schemas/hub-protocol/v0` change together. The contract test in `internal/hubproto` fails when they differ. `internal/fakehub` is the test hub.

## Pitfalls

- Signal handling differs per OS (`internal/supervise/signals_unix.go`, `signals_windows.go`).
- Tests must not leave processes behind.
- Timing-dependent tests take their clock and intervals from injected options; follow that pattern.
- `TestAcceptanceConnected` (acceptance tag) exercises the daemon against the fake hub.

## Focused checks

```sh
go test ./internal/supervise/... ./internal/hubclient/... ./internal/hubproto/... ./internal/service/...
go test ./cmd/jig/ -run 'Gate|Daemon|Supervise|Sleep|Budget|Join'
```

The full gate is in `AGENTS.md`.
