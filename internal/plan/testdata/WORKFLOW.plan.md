---
tracker:
  kind: github
  api_key: $SORTIE_TRACKER_API_KEY
  project: $SORTIE_TRACKER_PROJECT
  query_filter: "label:agent:plan,in-progress -label:agent:build -label:needs-human"
  # Assignee scope is appended by Jigz config at launch, not here.
  # ponytail: `todo` must stay first. Open issues carrying no state label
  # derive active_states[0] as their state, so without a leading non-target
  # entry the dispatch-time in-progress transition is a silent no-op and
  # the `in-progress` label never lands on the issue. `todo` is a
  # derivation fallback, not a label: no issue ever carries it.
  active_states: [todo, in-progress]
  in_progress_state: in-progress
  # Backstop only: the agent removes agent:plan and in-progress and adds
  # agent:done itself. If it forgot or its gh call failed, this transition
  # adds agent:done on a clean exit. agent:done is not an active state, so
  # nothing re-dispatches a planned issue.
  handoff_state: agent:done
  # -label:needs-human keeps escalated issues out (search path honors it).
  # Plan writes no code, so work-evidence is always undeterminable;
  # `off` skips the capture and the per-run log line. Handoff proceeds.
  handoff_evidence: off
  # Nothing is terminal: agent:done does not close the issue, and terminal
  # states would close it on transition.
  terminal_states: []
  comments:
    on_completion: false
    on_failure: false

polling:
  interval_ms: 60000

db_path: .sortie-plan.db

workspace:
  root: $JIG_WORKSPACES/plan
  retention_days: 30

# ponytail: plan-gating is enforced by the build loop's query_filter
# (`-label:agent:plan`), not here — dispatch.match has no negation.
# The plan loop stays a separate file so it can carry its own model.

agent:
  kind: pi
  command: pi
  max_turns: 10
  max_concurrent_agents: 1
  turn_timeout_ms: 3600000
  read_timeout_ms: 120000
  stall_timeout_ms: 300000
  stop_grace_ms: 5000
  max_retry_backoff_ms: 120000

pi:
  model: ""

# Alternative agent: Claude Code (opus for this loop). To switch, comment out
# the active `agent:` and `pi:` blocks above and uncomment below.
#agent:
#  kind: claude-code
#  command: claude
#  max_turns: 10
#  max_concurrent_agents: 1
#  turn_timeout_ms: 3600000
#  read_timeout_ms: 120000
#  stall_timeout_ms: 300000
#  stop_grace_ms: 5000
#  max_retry_backoff_ms: 120000
#claude-code:
#  model: "opus"

dispatch:
  default:
    template: ./prompts/plan.md
server:
  host: 127.0.0.1
---

{{/* Plan loop: issues labeled `agent:plan`. Prompt lives in
     prompts/plan.md (via dispatch default above): research the issue,
     post the plan as an issue comment, write no code. The agent removes
     agent:plan and adds agent:done; a human reviews the plan and labels
     the issue `agent:build` to trigger development. The body below never
     renders; it documents the routing. */}}
Plan loop routing only — the prompt comes from the dispatch template.
