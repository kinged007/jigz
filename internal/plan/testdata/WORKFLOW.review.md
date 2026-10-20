---
tracker:
  kind: github-pr
  api_key: $SORTIE_TRACKER_API_KEY
  project: $SORTIE_TRACKER_PROJECT
  # Strictly: PRs assigned to the token owner carrying agent:review
  # (assignee scope is appended by sortie-loop config at launch, enforced
  # client-side by the adapter); the label
  # clause rides along for visibility (the github-pr adapter lists via
  # /pulls, which runs no search syntax — active_states below is what
  # actually matches the label). `in-progress` lets the same loop pick a
  # review back up after a crash; `-label:agent:build` and
  # `-label:agent:merge` keep a fix or merge orphan out. -label:needs-human
  # keeps escalated PRs out, matched client-side by the adapter.
  query_filter: "label:agent:review,in-progress -label:agent:build -label:agent:merge -label:needs-human"
  # `todo` first so a PR that carries only agent:review derives a state
  # other than in-progress and the claim actually lands (see the dev
  # workflow); it is a derivation fallback, never a label.
  active_states: [todo, in-progress]
  in_progress_state: in-progress
  # Backstop only: the agent removes agent:review and in-progress and adds
  # agent:done (or agent:build when it wants the fix loop to run).
  handoff_state: agent:done
  handoff_evidence: off
  # Nothing is terminal: agent:done does not close the PR, and terminal
  # states would close it on transition.
  terminal_states: []
  comments:
    on_completion: false

polling:
  interval_ms: 60000

db_path: .sortie-review.db

workspace:
  root: $SORTIE_LOOP_WORKSPACES/review
  retention_days: 30

hooks:
  after_create: |
    git init -q . 2>/dev/null || true
    git fetch --depth 1 origin "pull/$SORTIE_ISSUE_IDENTIFIER/head:pr-$SORTIE_ISSUE_IDENTIFIER" 2>/dev/null || git fetch --depth 1 "$SORTIE_LOOP_CLONE_URL" "pull/$SORTIE_ISSUE_IDENTIFIER/head:pr-$SORTIE_ISSUE_IDENTIFIER"
    git checkout -q "pr-$SORTIE_ISSUE_IDENTIFIER"
  timeout_ms: 60000

agent:
  kind: pi
  command: pi
  max_turns: 3
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
#  max_turns: 3
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
    template: ./prompts/review.md
server:
  host: 127.0.0.1
---

{{/* Review loop: label a PR `agent:review` to get a three-pass review.
     The prompt lives in prompts/review.md (via dispatch default above).
     The agent routes the PR on the verdict it just posted: agent:review,
     in-progress and agent:done off, agent:build on when there are
     findings to fix, agent:done alone when the review is clean. */}}
Review loop routing only — the prompt comes from the dispatch template.
