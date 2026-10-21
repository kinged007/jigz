# Working hours and agent account pool

Design for roadmap sections 5.1 and 5.2 (Phase 1). The settings code is in
`internal/machine`.

## Machine file

The optional file `<os.UserConfigDir()>/sortie-loop/machine.yaml` (Linux:
`$XDG_CONFIG_HOME` or `~/.config`) belongs to the person on the machine and
is shared by the loops of every repository on it. A missing file means no
working-hours limit and no pool; loops behave as without this feature.

```yaml
hours:
  timezone: Europe/Madrid     # IANA name; empty = local time
  end: finish                 # finish | stop; default finish
  windows:
    - days: [mon, tue, wed, thu, fri]
      start: "09:00"
      end: "18:00"            # an end earlier than start wraps past midnight
pool:
  - name: claude-work
    runtime: claude-code      # claude-code | pi
    command: claude           # real CLI; default "claude" or "pi" by runtime
    env:
      CLAUDE_CONFIG_DIR: ~/.claude-work
    hours:                    # optional; same shape as hours, without `end`
      windows: [...]
    cost_class: subscription  # free-text label shown in the dashboard
  - name: pi-api
    runtime: pi
    env:
      ANTHROPIC_API_KEY: ...
limits:
  default_reset: 1h           # used when the CLI message has no reset time
  patterns: []                # extra case-insensitive regexes
```

Rules:

- Without windows (or without `hours`) the hours are always open.
- Days are `mon` to `sun`. A window that wraps past midnight belongs to the
  day it starts. `start` equal to `end` is invalid.
- Entry names are non-empty and unique. Validation errors name the field,
  for example `pool[1].runtime: unknown runtime "codex"`.
- Entry `env` values expand a leading `~/` and `$VAR` or `${VAR}` from the
  process environment when `agent-exec` starts the CLI.
- The file can hold API keys. Loading warns, and does not fail, when the
  file is readable by group or others.

### hours.end

- `finish`: when the machine window closes, loops claim nothing new. Each
  loop is stopped once it has no running session.
- `stop`: loops are stopped when the window closes. The engine aborts the
  turn. The issue keeps its in-progress label and is dispatched again when
  the next window opens.

## Pool state

`<state dir>/sortie-loop/pool.json`, where the state dir is
`$XDG_STATE_HOME` or `~/.local/state`. For each entry name it stores
`exhausted_until` (RFC 3339), `reason` (the limit message, at most 200
characters) and `last_used`. Updates are read-modify-write under an
exclusive `flock` on `pool.json.lock`. A missing file means nothing is
exhausted. Entries whose names are not in the config are ignored.

## Selection

- An entry is available at time t when `exhausted_until <= t` and its own
  hours are open at t.
- `Pick(runtime, t)` returns the first available entry of the runtime in
  config order. `agent-exec` uses it and then records `last_used`.
- `ActiveRuntime(t)` returns the runtime of the first available entry in
  config order, or an empty string when none is available. The supervisor
  uses it.
- `NextChange(t)` returns the earliest later time at which a machine window
  or entry window opens or closes, or an exhaustion ends. It is the
  supervisor's wake time.

## Usage-limit detection

Input is one output line of the real CLI and the runtime.

- `claude-code` stdout: a JSON object with `"type":"result"` and
  `"is_error":true`. The text is its `result` and `subtype`.
- `pi` stdout: an object with `"stopReason":"error"` at any depth. The text
  is its `errorMessage`.
- stderr: the text is the whole line.
- Stdout lines that are not JSON, or are not error events, are ignored.

The text is a usage limit when it matches, case-insensitively, `usage
limit`, `limit reached`, `limit exhausted`, `insufficient_quota`, `quota
exceeded`, `out of credits`, or a configured pattern. Transient errors such
as `overloaded`, 529, `rate_limit`, 429 and `api_retry` do not match.

The reset time is, in order:

1. a `|<unix seconds>` suffix in the future;
2. `resets <clock>` where the clock is `3pm`, `3:30pm` or `15:00`, with an
   optional zone in parentheses such as `(Europe/Madrid)`. The result is the
   next occurrence after now in that zone, else the machine hours timezone,
   else local time;
3. now plus `limits.default_reset`.

## Supervisor

Per tick the supervisor computes whether the machine window is open and
`ActiveRuntime`.

- Window closed, or no runtime available: loops are not started. With
  `finish`, each running loop is stopped once it has no running session;
  entries waiting for a retry do not count, because the engine persists
  them and they resume after a restart. With `stop`, loops are stopped
  immediately.
- When the pool is not empty, each loop's environment gets
  `SORTIE_AGENT_KIND=<runtime>` and
  `SORTIE_AGENT_COMMAND=<absolute sortie-loop path> agent-exec <runtime>`.
  A change of `ActiveRuntime` recycles each loop once it has no running
  session.
- Shipped workflows carry both a `pi:` and a `claude-code:` block. The
  engine reads only the block named by `agent.kind`.
- An empty pool leaves the environment and behaviour unchanged.

## agent-exec

`sortie-loop agent-exec <runtime> [args...]`:

1. Pick an available entry. If there is none, print a message on stderr and
   exit 75.
2. Run the entry's command with the args. The environment is the process
   environment plus the entry env. The working directory and stdio are
   unchanged.
3. Copy stdout line by line, unchanged and unbuffered. Forward signals.
   Exit with the child's exit code.
4. When a limit is detected, mark the entry exhausted until the reset time.

A limit hit fails one engine session. The engine posts a failure comment and
retries after about 10 seconds. The retry picks the next available entry.

## Known limitation

With `max_concurrent_agents` above 1, a loop can claim new work between the
window close and the moment it is stopped.

## Provider terms

Unattended use of a subscription account can be restricted by the provider.
Checking the terms is the responsibility of the member.
