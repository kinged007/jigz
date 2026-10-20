---
tracker:
  kind: github-pr
  api_key: $SORTIE_TRACKER_API_KEY
  project: $SORTIE_TRACKER_PROJECT
  # Exclusion enforced client-side by the adapter; the label
  # clause rides along for visibility (the github-pr adapter lists via
  # /pulls, which runs no search syntax — active_states below is what
  # actually matches the label). Assignee scope is appended by
  # sortie-loop config at launch, not here. This loop shares the
  # agent:build trigger with the build loop: on an issue it means implement,
  # on a PR it means apply the posted review finding.
  query_filter: "label:agent:build,in-progress -label:agent:review -label:agent:merge -label:needs-human"
  # `todo` first so a PR carrying only agent:build derives a state other
  # than in-progress and the claim actually lands (see the build workflow).
  active_states: [todo, in-progress]
  in_progress_state: in-progress
  # Backstop only: the agent removes agent:build and in-progress and adds
  # agent:done, or agent:review when the fix wants another pass.
  handoff_state: agent:done
  handoff_evidence: off
  # Nothing is terminal: agent:done does not close the PR, and terminal
  # states would close it on transition.
  terminal_states: []
  comments:
    on_completion: false

polling:
  interval_ms: 60000

db_path: .sortie-review-fix.db

workspace:
  root: $SORTIE_LOOP_WORKSPACES/review-fix
  retention_days: 30

hooks:
  after_create: |
    git init -q . 2>/dev/null || true
    # Step 3 of the prompt pushes the fix commit back; that needs a remote.
    git remote add origin "$SORTIE_LOOP_CLONE_URL" 2>/dev/null || true
    git fetch --depth 1 origin "+pull/$SORTIE_ISSUE_IDENTIFIER/head:pr-$SORTIE_ISSUE_IDENTIFIER"
    git checkout -q "pr-$SORTIE_ISSUE_IDENTIFIER"
  # Workspaces created before the remote existed still have none
  # (after_create only runs once); add it idempotently so the push in
  # Step 3 works on reused workspaces too.
  before_run: |
    git rev-parse --git-dir >/dev/null 2>&1 || exit 0
    # Heal any git operation an earlier attempt or the agent left in flight.
    # An unresolved index makes every later checkout fail, and the retry
    # reuses this directory, so the poison would persist forever.
    git rebase --abort 2>/dev/null || true
    git merge --abort 2>/dev/null || true
    git cherry-pick --abort 2>/dev/null || true
    git reset --hard >/dev/null
    git remote get-url origin >/dev/null 2>&1 || git remote add origin "$SORTIE_LOOP_CLONE_URL"
    if [ -f .git/shallow ]; then git fetch --unshallow origin 2>/dev/null || true; fi
  timeout_ms: 60000

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

# Alternative agent: Claude Code (sonnet for this loop). To switch, comment
# out the active `agent:` and `pi:` blocks above and uncomment below.
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
#  model: "sonnet"

dispatch:
  default:
    template: ./prompts/review-fix.md
---

{{/* Review-fix loop: watches github-pr for PRs labeled `agent:build`
     (the same trigger the build loop uses on issues). The agent checks out
     the PR head, applies the posted review feedback, and pushes to the
     same branch. It then routes the PR on: agent:build, in-progress and
     agent:done off, agent:review on, so a fresh review runs. Blocked work
     keeps the trigger and adds needs-human, which the loop's
     -label:needs-human exclusion drops. This keeps the review loop
     read-only: it never edits code, it only routes. */}}
Review-fix loop routing only — the prompt comes from the dispatch template.
