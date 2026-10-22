# United dashboard & control panel — plan

Status: implemented — branch `feat/united-dashboard`, uncommitted on top of `52e9003` — the dashboard serves the fleet dashboard's pages; `gofmt -l` empty, `go vet`/`go build`/`go test` green

Recreate the fleet dashboard and control panel inside `jig`, so every
`jig` run serves the same pages
whether it is one repo or `—-unite` (many repos). The extra binary
disappears; the supervisor you already run is the Fleet controller.

## 1. What exists today

### Jigz dashboard, before this change (`dashboard.go` + `dashboard.html`, now deleted)

- One HTML template, 5 s `<meta http-equiv=refresh>`.
- `dashData { Unite, Loops []loopStats, Issues []issueStats, Repos []repoRollup, All* floats, RunningN, Controls, Draining, Sleep }`.
- Stats: `readLoopDB` (RO WAL read of `aggregate_metrics` + `run_history` per workflow) + `fetchState` (`GET 127.0.0.1:$port/api/v1/state` → `Running[], Retrying, BudgetExhausted`) fanned out, unioned, sorted by `RepoName/Loop`. `readIssueStats` groups by `repo␀loop␀display_identifier`.
- Single-repo: `sup.endpoints()`; `Controls=!Unite`, `Draining` + `Sleep` wired, `POST /control` → `syscall.Kill(self, SIGTERM|SIGUSR1|SIGUSR2)` (same code path as CLI).
- Unite: `readRegistry()` (registry+TCP probe hybrid, ports `7678→7900`, stop after 3 empties, stale `>5m && !pidAlive` hidden, orphans as `unknown`) — `Controls=false`, read-only.
- No system metrics, no per-repo state table, no cost, no `picked_up`, no disk, no fleet table, no “Working on right now” detail, no edit/reload, no host guard, no fragment refresh.

### Fleet dashboard (`cmd/jig/dashboard.html` + `web.go` + `internal/{rollup,supervise,config,host}`)

- Two templates: `page` (full) + `live` + `status` fragments + `edit` + `confirm` (stop-all).
- Header: `edit config` + `reread config and apply` (behind `when loops are idle` checkbox) + `stop all` (confirm page). System metrics fragment (`memory/cpu/disk` + cores + clock) polled on its own 3 s tick — CPU is delta of `/proc/stat`, first sample shows `—`.
- Fleet row: `loops live / agents running / runs / failed / tokens`.
- Repositories table → one row/repo: `slug`, supervisor `State` (running/stopped/failed/invalid/disabled), `loops` list, `agents`, `tokens`, `detail` (error + diags + tail on failed/invalid) + line `pid … · 💾 disk … · 📁 dir`, per-row `start | stop | stop when idle → {stop now,cancel stop} | restart`.
- Loops table → `repo/loop/port/link/state(live|not answering)/working on(running[] plus turns/model + “N agents at once” + retrying)/runs/tokens/cost`.
- Per-repository rollup table.
- “Working on right now” → one row/busy agent, longest-running first: `repo/loop/issue/state/turns/tokens/agent·model/cost/picked up/running for`. Cost is `ActiveEstimatedCostUSD` split by token share (nil `→ —`, loopTokens==0 `→ 0`).
- Control server (`web.go: hostGuard + serve`): loopback-only bind, `Host` check (loopback names always OK; specific `--host` kept; `0.0.0.0/::` = wildcard — banner + log + check off), actions routed to `supervise.Supervisor` (not signals) except `stop all`. Live refresh: `fetch('/live')` + `fetch('/status')` every 3 s, clock updated locally, gone detection (`page not answering → banner + stop tick`, title rewrite).
- Data plane (`internal/rollup`): `Reader.Collect()` reads `~/.cache/jigz/registry.json` → live `endpoint{Repo,RepoName,Loop,Port,DBPath}`, skips stale dead entries, then per-loop `GET /api/v1/state` (3 s) + RO DB (`aggregate_metrics + run_history`); `pickedUp` query (`MIN(started_at)` since last `succeeded` on `display_identifier|identifier`). Fleet = sum across loops + by-repo. Icons: 3 inlined Lucide SVGs, pinned `12px`.

### Shared substrate already on `jig` (L1-L3)

- Registry: `~/.cache/jigz/registry.json` (`Entries[root]={RepoName,UpdatedAt,PID,Loops[]loopEndpoint{Repo,RepoName,Loop,Port,DBPath,WorkflowFile,LoopType}, Args, Version, StartedAt, StatusPath}`) under `registry.json.update.lock`, `writeFileAtomic`, `dropRegistry` on shutdown, `lockStarts` for port claim.
- Status doc: `~/.cache/jigz/status/<sha256(root)[:16]>.json` (`statusDoc{SchemaVersion:1,Version,EngineVersion,TestedSchemaVersions:[16],PID,Repo,RepoName,StartedAt,UpdatedAt,State,Workflows[]workflowStatus{Name,WorkflowFile,LoopType,Port,PID,State,StartedAt,RestartCount,LastExitCode,ConfigHash,LogPath,Budget},Cleanup,Sleep}`), `writeStatus` atomically, `liveness=(exited|running|stale)`.
- L2 controls as files/signals: `wake` (`.wake`), `pause|resume|budget override` (`.budget.request.*` file queue, `*.budget.json` state), `cleanup` (`.cleanup.json/.lock`), `reload` (`SIGHUP`), `validate`/`status` CLI.
- Engine live API: `GET :port/api/v1/state` `{counts{running,retrying}, active_estimated_cost_usd, running[{display_identifier,issue_identifier,state,turn_count,model_name,started_at,tokens.total_tokens}]}`. Assigned by `startLoopChild` under `lockStarts` with `waitLoopBound` (15 s, `probeLoopAPI`).

## 2. Goal — what “recreated here” means

One codebase, two modes, same pages:

- `jig [repo-root]` serves on `127.0.0.1:7677` (or `--dashboard-port N`) the same pages in both modes. A plain run scopes the tables to its own repository; `--unite` starts, or joins, the one dashboard that spans every repository in `registry.json`.
- All fleet dashboard controls are present, routed to the owning supervisor rather than to “self”:
  enterprisewise `—-unite`: wake, pause/resume/budget override per workflow, stop/stop-when-idle/restart per repo, reload/re-read config and apply, edit config in browser, stop-all. Per-repo dir size, per-loop log path, system load.
- No new daemon binary. No HTTP supervisor API. No new dependency. The page stays a `html/template` that degrades to static HTML without JS. A dead repo with a fresh DB still shows history from SQLite; a repo with `—no-server` still shows its `statusDoc` (file-backed) even though it has no ports.

## 3. Design rules (from `AGENTS.md` + the fleet dashboard’s `PLAN.md`)

- No defaults duplicated — every optional field stays `*T`; omission means the engine/supervisor default. Template helpers just format, they do not invent defaults.
- Secrets never hit a file from the dashboard — `token_env` stays env-only; edit surface is `/.sortie/config.yaml` (no `.env.loop` rewrite).
- `jig` validates workflows; the dashboard does not maintain a hardcoded workflow list.
- One code path for single and multi — `Unite` is a rendering flag, not a second server. Helpers (`commas`, `usd`, `since`, `at`, `pct`, `cpuPct`, `size`, `gib`, `agentLabel`) are shared between fragments.
- File+DB data plane stays file+DB — dashboard reads the same registry + status docs + WAL-visible RO SQLite + engine `GET /state`. No new IPC beyond signal/file-queue already implemented.

## 4. Data shape after the overhaul

### Go structs (all in `cmd/jig`)

```go
// Existing — keep, extend
type loopEndpoint { Repo, RepoName, Loop string; Port int; DBPath, WorkflowFile, LoopType string }

// Today’s loopStats gains cost + pickup; stays the unit for Loops table
type loopStats {
  Endpoint loopEndpoint; Live bool
  Running []runRow{ Identifier, State string; Turns int; Tokens int64; Model, StartedAt, PickUp string }
  Retrying, BudgetExhausted, SlotsFree int
  AllInput, AllOutput, AllTotal, AllCache int64; Runs,Succeeded,Failed int; RuntimeSec float64
  Cost *float64;  // from ActiveEstimatedCostUSD
  DBLive bool; DBError string
}

// New — maps directly to the fleet dashboard’s repoView + pageData
type repoView struct {
  Slug string; State string // running|stopped|failed|invalid|disabled|unknown
  Loops []string; Running int; Tokens int64; Observed int
  PID int; Args []string; Version, StatusPath string
  Err string; Diags struct{ Errors,Warnings []string }; Output []string
  Disk int64; DiskKnown bool; Dir string
  Sleep sleepStatus; // per-repo doc.Sleep, not global
}

type activeRun struct { // flattened fleet busy rows
  Repo, Loop string; Port int
  Identifier, State string; Turns int; Tokens int64
  Agent, Model string; PickedUp string; Cost *float64
}

type pageData struct {
  Now time.Time; Fleet Fleet; Repos []repoView; ConfigPath string
  Sys host.Metrics; Msg, Err, Edit string; Exposed bool
  Active []activeRun; Confirm, WhenIdle bool
  Host, LinkTemplate string
  // Unite-kept for title/badge: Unite bool (render branch), but
  // controls are no longer tied to it — every repoView renders actions
  Unite bool; GeneratedAt time.Time
}

type Fleet struct { // from rollup.Reader — reuse existing or inline CollectStats
  At time.Time; Loops []Loop; Repos []Repo
  Live, Running, Runs, Failed int; Tokens int64
}
type Loop struct { // thin mirror of rollup.Loop for template compat
  Repo, Loop string; Port, PID int; LoopType, DisplayPort string
  Live bool; Running []Run; Retrying int; Runs,Succeeded,Failed int
  Tokens, Input, Output int64; Cost *float64; DBError string
}
```

Sources per field:

- Fleet + Loops + Per-repo rollup: `Reader.Collect()` exactly as the fleet dashboard does (port probe → DB → HTTP, `known[port]` from registry, stale rule, sort). For single-repo we keep the fast path `sup.endpoints()` but normalize into the same shape so the template doesn’t branch.
- `repoView.State/Err/Diags/Output/PID/Args/StatusPath` — from `registry entry` + `statusDoc` (`liveness == running|stale|exited` → failure vs running; `doc.State` when present; `Args` + `Version` from registry; `Diags` from validate path if retained).
- `Disk` — `dirsize` walk of `Repo` root’s `.sortie` + workspaces, sampled every 10 min on a `time.Ticker`, cached in `map[Slug]int64` guarded by mutex (exactly as the fleet dashboard’s `sizes *probe`). Measured out-of-band, never in the render path.
- `Sleep` + per-workflow `Budget` — `statusDoc.Sleep` and `statusDoc.Workflows[].Budget` read from each repo’s status file (not probed).
- `Cost` — `statePayload.ActiveEstimatedCostUSD`, nil → `usd` prints `—`.
- `PickUp` — `pickedUp(db, identifier)` query (`MIN(started_at)` since last succeeded on `display_identifier|identifier`) — same SQL as the fleet dashboard.
- `agentLabel` — from `statusDoc` workflow `Budget.Agent`? No; from per-repo `doc.Workflows[].Name→config Agents map` built at collect time, exactly as the fleet dashboard’s `agents[repo/loop]`.
- `Sys.Metrics` — `internal/host.Reader` (see §7).

### Formatting helpers (the fleet dashboard’s `web.go` — verbatim or adapted)

`commas`, `usd` (nil→`—`, `≥100 → $0`, `≥1 → $0.00`, `>0 → $0.000`, else `$0.00`), `since` (two largest units), `at` (`2006-01-02 15:04 MST` local), `pct`, `cpuPct` (`-1→—`), `size` (KiB/MiB/→ GiB), `gib`, `agentLabel` (`claude-code→Claude`, `pi→Pi`, …).

## 5. Pages & routes

```
GET  /                  → page (full, includes fleet + repos + loops + per-repo rollup + active)
GET  /live              → live  fragment (everything below the header)
GET  /status            → status fragment (memory/cpu/disk + clock)
GET  /edit?repo=SLUG    → edit  (textarea with that repo’s .sortie/config.yaml)
POST /edit?repo=SLUG    → save (+ validate; failure re-renders with Err, success 303 → /edit)
POST /action            → start|stop|drain|restart per repo, stopall (confirm gated), apply
```

- `GET /` renders `page` with `pageData`. Header: `edit config` (single-repo: link to own repo; unite: per-repo edit links in the repos table) + `when loops are idle` checkbox (propagated as `when_idle=1` hidden on every `POST /action`) + status metrics.
- `GET /live` and `GET /status` render only those `{{define}}` fragments with the same `pageData` — client JS replaces `#live`/`#status` outerHTML.
- Auth/host: bind is still `hostGuard{addrs,wildcard}.allows(Host)` exactly as the fleet dashboard (loopback names always OK; `—-host` specific address keeps check; `0.0.0.0/::` = wildcard, banner + log). The “exposed” banner is the same `msg exposed` div. Default is `127.0.0.1`; no new flag required to start (`—-host` can be added later alongside `—-dashboard-port` without changing the default).
- Link building: `linkBuilder{Host, Template: —-loop-url-template}` with `{host},{port},{loop},{repo}` placeholders (same as the fleet dashboard’s `—-loop-url-template`). Dashboard port links use it; when no template, `http://{host}:{port}/`.

## 6. Controls — routing, not reimplementation

Every button is a `POST /action` handled by the dashboard process, which then acts on the owning supervisor identified by `repo slug → registry entry (root,PID)`.

| op | single-repo (`!Unite` or one row) | `—-unite` (per-repo row) |
|---|---|---|
| `start` | `sig=SIGTERM→Wait→Exec(restartArgv)` handled here; or `sup.Start(slug)` path if already running — keep existing `remote.go` helper but now callable from HTTP | `startRepo(root, args)` → `syscall.Exec` of `restartArgv(entry,root)` (unite dashboard cannot fork another repo silently; the request file model isn’t enough for a cold start). This is the only op that spawns. |
| `stop` | `syscall.Kill(PID, SIGTERM)` | `syscall.Kill(PID, SIGTERM)` where `PID=runningEntry(root).PID` |
| `drain` (`stop when idle`) | `syscall.Kill(PID, SIGUSR1)` | `syscall.Kill(PID, SIGUSR1)` |
| `restart` | `SIGUSR2` (recycle) | `syscall.Kill(PID, SIGUSR2)` |
| `wake` | `requestWake(root)` (write `.wake`) | `requestWake(root)` per repo |
| `pause <workflow>` | `controlWorkflow(root,"pause",wf)` (writes `.budget.request.*` file queue) | same, routable per repo |
| `resume <workflow>` | `controlWorkflow(root,"resume",wf)` | same |
| `budget override <workflow>` | `controlWorkflow(root,"override",wf)` | same |
| `cleanup [--dry-run]` | `runCleanupCmd` helper | per-repo POST with flag |
| `stopall` | confirm page (`confirm{Active,WhenIdle}`) then either `sup.StopAll()` + `shutdown` or `StopAllWhenIdle` + `WaitAll` | `—-unite` variant drains every live repo (`forEachRepo(PIDs)→SIGUSR1 or SIGTERM`), then dashboard process waits and banner says “fleet is gone” |
| `apply` (`reread config and apply`) | `reloadSupervisor(root, whenIdle)` → signal `SIGHUP` + poll `UpdatedAt`, `StartAll` for ready-stopped | per-repo apply (payload includes `repo`); or fleet-wide `apply` iterates repos |
| `edit` | read `filepath.Join(root,".sortie/config.yaml")` into textarea; save → `writeFileAtomic` then `reload` gate | per-repo `?repo=slug`; same validate-then-save path |

All L2 commands already have request-file semantics; HTTP just writes the same files/signals the same PIDs the CLI would. No new protocol. Failures surface as `Err` in the redirect (`?err=`) and log on supervisor.

## 7. Host metrics & disk sizing

- New package `internal/host` — copy from the fleet dashboard's `internal/host` (files: `host.go`, `host_linux.go`, `host_other.go`, `blocks*.go`, `cores*.go`, `dirsize*.go`). `host.Reader` keeps previous `/proc/stat` sample; `Read() Metrics{MemTotal,MemUsed,Cores,CPU(-1 until 2nd sample),DiskTotal,DiskUsed,DiskPath}`. Force-copied verbatim so behavior is identical (MemAvailable, `Bavail`, idle+iowait).
- Disk probe is by `state_dir` (here: `filepath.Dir(registryPath())`) so the figure tracks where `.sortie` actually fills up.
- Per-repo disk size: `dirsize(Repo+"/.sortie")` cached 10 min, like the fleet dashboard’s `sizes *probe`. Walk ignores `.git`, does not follow symlinks.

## 8. Template & frontend

- Base: fork `dashboard.html` from the fleet dashboard — render order becomes `page → bar(controls+status) → msg/err/exposed → live(fleet row → repos → loops → per-repo → active)` + `edit` + `confirm` templates in one file, driven by the helpers above.
- Styling: keep the fleet dashboard’s monospace layout (easier to extend) behind the same CSS vars; preserve current `jig` card look as a fallback behind a comment (do not delete until landing).
- JS: remove `<meta http-equiv=refresh>`. Add fragment polling from the fleet dashboard verbatim:
  - `timer = setInterval(tick, 3000)`; `tick()` = `fetch('/live').then(swap #live)+ local clock + fetch('/status')`.
  - `gone()` detection: a `fetch('/live')` that never resolves means the supervisor exited (stop-all). Once: `title='Jigz is not running'`, prepend `msg exposed` banner, `clearInterval(timer)`.
  - No build step; page degrades to static without JS.

## 9. Files & wiring (minimal diff, fewest files)

```
cmd/jig/
  app.go              → type app + fleetSupervisor (registry discovery, signals, detached start),
                        repoSizes with the 10-minute timer, projects() from the registry
  dashboard.go        → resolveRepo, statusDir, serveDashboard: builds the app, starts the
                        server and the history collector, signals self on quit
  web.go + 20 handlers→ the fleet dashboard's, imports rewritten to Jigz; /edit edits this repo's
                        .sortie/config.yaml
  templates/*.html    → the 16 template files, inlined icons, renamed to "Sortie Loop"
  dashboard_data.go   → openRO and fmtTokens (kept: budget reads and the status command use them)
  dashboard.html      → deleted
  status.go, supervise.go, unite.go, main.go ← unchanged
internal/
  host/ rollup/ loopsrc/ store/ collector/ fleetconfig/ auth/ edit/ plan/ github/ ← ported at 52e9003
  rollup/schema.go    → SupportedSchemas {18, 35}
cmd/jig/
  web_test.go         → hostGuard.allows, linkBuilder.Build, fragments over httptest, foreign Host
  control_test.go     → projectacts offers the right actions per state
  wake_cmd_test.go    → applyStatus marks a sleeping project and offers wake
```

No new module requirements. `modernc.org/sqlite` and `gopkg.in/yaml.v3` already pinned.

## 10. Phased build — all phases landed

Phase 0 — groundwork (no UX change) — **done at `52e9003`**
- `go vet`/`gofmt -l` baseline; `dashboard.html` snapshot kept via appended `{{define "page/live/status/edit/confirm"}}` + fallback `dashTmpl.Lookup("page")` logic.
- `internal/host` copied verbatim from the fleet dashboard (5 files), `internal/rollup`, `internal/loopsrc`, `internal/store`, `internal/collector`, `internal/fleetconfig`, `internal/auth`, `internal/edit`, `internal/plan`, `charts.go`, `templates/*.html` (16 files) landed.
- `dashboard_data.go` extended with `Cost *float64`, `PickUp`, `commas/usd/since/at/pct/cpuPct/size/gib/agentLabel/trunc/utc/liveHref` to match `dashboard.go` FuncMap; `dashboard.go` expanded to 1031 lines with `Fleet/FleetLoop/FleetRepo/activeRun/repoView/pageData/linkBuilder/hostGuard` + `collectFleet/buildRepos/pageForRequest`. `readRegistry`+`collectFleet` helpers usable by both single and unite paths — **done**, but `dashboard.go` still embeds single `dashboard.html`, not `templates/*.html` via `embed.FS`.

Phase 1 — fleet rollup & live data
- Upgrade `fetchState` to capture `ActiveEstimatedCostUSD`, thread through `loopStats.Cost`.
- Add `pickUp(db,id)` query; wire into `runRow.PickUp`.
- Replace `collectStats` with `rollup.Reader{}.Collect()`-equivalent that also reads WAL RO DBs; verify totals survive restarts.

Phase 2 — repos table + per-repo rollup + disk
- Introduce `repoView` + `pageData.Repos` built from `registry Entries` + `statusDoc` per slug; disk sampler goroutine.
- Render repos table (state badge, loops, agents, tokens, pid, disk, folder, detail) and per-repo rollup table. Keep single-repo badge as one row so template branch disappears.

Phase 3 — “Working on right now” + fleet summary + loops table
- Add `active(rollup.Fleet, agents)` with cost splitting + sort by `PickedUp`, `commas/usd/since/at` helpers, loops `cost` column, retrying badge.
- Ship the new `dashboard.html` as default (old markup kept behind comment for one release).

Phase 4 — controls (signal/file routed)
- Add `POST /action` with `repo` discriminant: `stop|drain|restart|wake|pause|resume|override|cleanup|stopall|apply`, each delegating to the owning PID/file the way the CLI does.
- `when loops are idle` checkbox + `stopAll` confirm page.
- Wire `Draining` per loop (`loopQuiet` drain badge) and `Sleep` badge with `wake` button.

Phase 5 — edit + host guard + links
- `GET/POST /edit?repo=SLUG` (read → textarea → atomic write → `validate` → `SIGHUP`/`restart --when-idle`; err stays on page).
- `hostGuard` + “exposed” banner + `—-host` / `—-loop-url-template` flags; `linkBuilder.Build()`.
- Fragment polling JS (`/live` + `/status`, 3 s, gone detection) replaces meta refresh.

Each phase lands as one PR, `go fmt -l` empty, `go vet ./...`, `go test ./...` (~70 s, `-race` for supervisor paths), `go build ./...` (cross matrix covered by CI).

## 11. Tests

- Unit (`testing` stdlib, no framework): helpers (`commas`, `usd`, `since`, `at`, `pct`, `cpuPct`, `size`, `gib`, `agentLabel`), `hostGuard.allows`, `linkBuilder.Build`, `active` sort/cost-split, `periodStart`/`readBudgetUsage` (already present), `probeStopEmpty` truncation, dirsize memoization.
- Table tests against `statusDoc` golden files for `liveness` and `Fleet.Repos` aggregation.
- Web tests: `net/http/httptest` for `GET /live` + `GET /status` fragment, `Host` 403 vs 200, `stopall` confirm gate.
- Race: `go test -race ./...` (supervisor state is `sync.Mutex` guarded; `host.Reader` has `sync.Mutex`).

## 12. What to deliberately not build

- Multi-host supervisor, systemd unit, history migration — same defer as the fleet dashboard.
- TLS/auth inside `jig` — loopback bind is the control; exposing beyond loopback without auth is the same banner the fleet dashboard shows, not a rewrite.
- Per-run transcript / per-event token deltas — still deferred (L8 needs engine transcripts).
- Project-level dollar caps — `max_usd` stays ignored with warning until engine emits cost per run.

## 13. Risks & mitigations

- 123-port ceiling + sparse scan gap — fleet render annotates `unknown` orphans with status file fallback so a high-port repo isn’t clipped by 3 empties.
- PID reuse (`stale` vs `running`) — document as known ceiling (already in `plans/jigz/enhancements.md: Known gaps`), do not add pidfile locking beyond existing `flock`.
- Dashboard process owns fleet’s stop-all but not per-repo start — cold `start` spawns a detached `jig` with the recorded argv, so it does not replace the dashboard process.

## 14. What is implemented

### Server and pages

`cmd/jig/app.go` holds `type app`, the state every route reads: the
fleet supervisor shim, the loop source, the host reader, the disk-size
cache, the history store and the editor pipeline. `cmd/jig/web.go`
and the twenty handler files next to it are the fleet dashboard's, with imports
rewritten. `cmd/jig/dashboard.go` builds the app and starts the
server; `dashboard.html` is gone, and `templates/*.html` (16 files, inlined
Lucide icons) is the only markup.

Routes: `GET / /live /status /projects /projects/{owner}/{name} /runs
/runs.csv /runs/{owner}/{name}/{workflow}/{id} /runs/{owner}/{name}/{workflow}/log
/workflows /agents /settings /edit /edit/jigz /edit/project/{owner}/{name}
/edit/agent/{scope...} /edit/workflow/{owner}/{name}/{workflow} /backups
/api/v1/{fleet,projects,runs,usage,metrics,audit}`, `POST
/action /edit/validate /edit/apply /backups/restore /projects/new
/projects/{owner}/{name}/remove`.

### Where the data comes from

`loopsrc.StatusDoc` with `Roots: a.projectRoots` is the source. It reads each
registered repository's status document, probes only a live, awake
supervisor's running workflows, and falls back to `rollup.Reader` (registry
plus read-only SQLite) for everything else. `projectRoots` maps slug to the
absolute root the status file is hashed from.

`app.projects()` builds `fleetconfig.Resolved` from `app.repos()` on every
call, so a repository that starts after the page came up appears without a
restart. There is no fleet config file: the registry is the only inventory.

### Controls

`fleetSupervisor` in `app.go` adapts registry discovery to the interface the
handlers expect. `Snapshot()` prefers the local `supervisor` for this
process's own repo and reads registry plus status documents for the rest.
`Stop`/`StopWhenIdle`/`Restart` send `SIGTERM`/`SIGUSR1`/`SIGUSR2` to the
registered PID, or to this process when the slug is its own. `Start` spawns
a detached `jig` with the recorded argv. `reload` sends `SIGHUP` to
every registered repo. `stop all` drains or stops every repo, waits, closes
`app.quit`, and the quit goroutine signals the process, which is what makes
`sup.run()` return and the teardown drop the registry entry.

### Differences from the fleet dashboard

- No login and no CSRF: the page is loopback-only unless `--host` moves it,
  and `--host 0.0.0.0`/`::` shows the exposed banner. `--host` with a
  specific address keeps the `Host` check.
- Only the workflow file editor exists: `/edit/workflow/{owner}/{name}`.
  The fleet, project and agent editors edit the fleet YAML, which
  Jigz does not have, so those routes are not registered.
- The wizard's project lifecycle is the fleet dashboard's, so project create/remove and
  config-file writes are reachable but write the fleet YAML shape this
  program does not read. Use `jig setup` in the target repo.
- Workflow validation runs `sortie validate` in the environment the loops
  run with (`.sortie/.env.loop`, else `config.Load(dir).Env()`), not the
  fleet cascade, which has no counterpart here.
- Budget lines on the loops table come from the status document's
  `budget` object: spend against the cap, plus a `paused` chip naming who
  paused it.
- `SupportedSchemas` is `{18, 35}`: 18 is the older engine, 35 the pinned
  one. A database at another version reports a schema notice and its
  figures stay empty rather than wrong.
- The history database is `history.db` in the same cache directory as the
  registry, not the fleet dashboard's own database.

### Verified

- `gofmt -l cmd internal` empty, `go vet ./...`, `go test ./...` green.
- `cmd/jig/web_test.go`: `hostGuard.allows` table, `linkBuilder.Build`
  (default, IPv6, template), `/`, `/live`, `/status` over `httptest` with an
  empty registry, a registered repo with a status document rendering on
  `/`, `/live` and `/projects`, and a foreign `Host` answered with 403.
- `control_test.go`: `projectacts` offers stop/drain/restart on a running
  repo and start/restart on a stopped one. `wake_cmd_test.go`:
  `applyStatus` marks a sleeping project and offers wake.
- Manual: a scratch repo served `/`, `/live`, `/status`; two repos under
  `--unite` appeared in one table (4 loops); `POST /action op=stopall`
  returned the final page, stopped the loops, and the process exited.

## 15. Definition of done

- `jig —-unite` on a machine running two repos shows both in the repos table, each loop’s port links correctly, tokens survive restarts, busy agents list is longest-first with agent·model + cost share + picked-up duration, memory/cpu/disk refresh every 3 s, stop-when-idle drains one loop at a time without losing work, pause/resume/budget override act per workflow, edit saves with diff and validation, and `go test ./... && go vet ./... && gofmt -l` is clean.
