# Scheduled work: design note

Status: draft for discussion (2026-10-27).

Scheduled work entries extend the `work:` model (`engine-interface.md`)
with clock-driven runs: a cron schedule, an optional shell `check` gate,
and an optional agent invocation. This is the machinery behind manager
agents, approval-gated side effects, and multi-area dispatch. It builds on
item 1 of the Jigz roadmap and starts after the config model lands.

## 1. Goal

Jigz runs git-based project management, not just code loops: scheduled
runs that inspect state cheaply (shell), invoke an agent only when there
is work, write results back to the tracker, and refuse gated side effects
without approval. The user defines areas, workflows, and labels, and
wires their own external tools; Jigz provides schedules, gates, and
permissions.

## 2. Non-goals

- External transport (email in/out, chat, publishing targets). The user
  configures a CLI tool or MCP server per project; Jigz never sends
  anything itself.
- Area, workflow, or label schemes. Document and validate conventions;
  never define them.
- Engine changes. Scheduled runs go through the engine's `file` tracker
  if it carries them, else through direct `agent-exec` invocation.

## 3. Model sketch

Per work entry in `.jigz/config.yaml`:

```yaml
work:
  inbox-sweep:
    schedule: "*/15 * * * *"   # cron; timezone: Europe/Madrid optional
    check: "mail --unread --since 1h"   # optional; non-zero exit = skip tick
    prompt_file: prompts/triage.md      # or inline prompt:; neither = shell-only
    approvals:
      send_email: human        # later: a named agent, e.g. pm
    tools: [read_email]        # allow-list; deny by default
```

- `check` runs first and must be fast. A negative check records a
  `skipped` tick and spawns no agent.
- No `check` means the agent runs on every tick; no prompt means the
  `check` exit code is the whole run (shell-only entry).
- `approvals` names side effects and who may grant them. The gated step
  refuses to run without a recorded approval.
- `tools` is the entry's allow-list. `send_email` absent means the agent
  cannot send, whatever the prompt says.

## 4. Run lifecycle

1. Tick fires on cron. Skip when a previous run of the entry is still
   active (overlap guard: skip, never queue).
2. Run `check` with a timeout. Non-zero exit records `skipped`; stop.
3. Invoke the agent with the entry's prompt (same `agent-exec` path as
   loops). No workspace checkout unless the prompt needs one.
4. Record the run with reason `scheduled` (or `skipped`); surface both in
   run history and the dashboard.
5. Tracker writes (create, label, assign, close) are the recorded outcome
   of a manager run; no PR is expected.
6. Approval-gated steps check for a recorded `agent-answer` approval from
   an authorized approver before acting.

## 5. Engine fit

The engine claims issues, so an issue-free run does not fit its dispatch.
Probe in order:

1. The engine's `file` tracker with a Jigz-managed JSON issue file (a
   synthetic `scheduled-tick` issue). No new code paths if it carries
   the run.
2. Fallback: Jigz invokes the agent directly for scheduled runs; only
   issue-driven work goes through the engine.

## 6. Open points

- Cron dialect and timezone default.
- `check` timeout, and where its output is visible (dashboard run page).
- Approval record: an `agent-answer` comment may be enough; confirm at
  implementation whether run state must carry it too.
- Overlap guard: skip is the default; whether queue is ever wanted.
- Shell-only entries: how the exit code maps to run outcome.
