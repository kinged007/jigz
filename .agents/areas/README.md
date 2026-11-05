# Area context

Context for agents working on an item with an `area:<name>` label. Read `.agents/areas/<name>.md` first and explore further only when the task needs more. The full quality gate is in `AGENTS.md`. An item without an `area:` label: read `AGENTS.md`, find the code the task touches and note the area you worked in.

| Label | Paths | Purpose |
| --- | --- | --- |
| `area:cli` | `cmd/jig/*.go`, `cmd/jig/scripts` | The `jig` command: argument parsing, setup, start, stop, status, validate, reload, cleanup, budget, wake, join. |
| `area:config` | `internal/config`, `internal/gen`, `internal/machine`, `internal/plan`, `internal/engine`, `internal/fleetconfig`, `internal/dirs`, `cmd/jig/workflows` | Config loading and the generation of engine documents; shipped documents and prompts. |
| `area:supervisor` | `cmd/jig/{supervise,gate,daemon,join,sleep,budget,agentexec}.go`, `internal/{supervise,service,hubclient,hubproto,hubstate,source,fakehub}`, `schemas/hub-protocol` | Starting, watching and stopping loop processes; hours, budgets, pools; the hub protocol. |
| `area:dashboard` | `cmd/jig` handlers, `cmd/jig/templates/*.html`, `internal/{collector,store,rollup,runlive,loopsrc,github,questions,host}` | The read-only web dashboard and its data sources. |
| `area:docs` | `docs/`, `README.md`, `CONTRIBUTING.md`, `ROADMAP.md`, `SECURITY.md`, `plans/` | Documentation of current behavior. |
