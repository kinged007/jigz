---
tracker:
  kind: github-pr
  api_key: $SORTIE_TRACKER_API_KEY
  project: $SORTIE_TRACKER_PROJECT
  # Exclusion enforced client-side by the adapter; the label
  # clause rides along for visibility (the github-pr adapter lists via
  # /pulls, which runs no search syntax — active_states below is what
  # actually matches the label). Assignee scope is appended by
  # sortie-loop config at launch, not here. `in-progress` re-picks a merge
  # that was interrupted; `-label:agent:review` and `-label:agent:build`
  # keep review and fix orphans out (both loops claim with in-progress).
  query_filter: "label:agent:merge,in-progress -label:agent:build -label:agent:review -label:needs-human"
  # `todo` first so a PR carrying only agent:merge derives a state other
  # than in-progress and the claim actually lands (see the dev workflow).
  active_states: [todo, in-progress]
  in_progress_state: in-progress
  # Backstop only: the agent removes agent:merge and in-progress and adds
  # agent:done (plus needs-human when it could not merge).
  handoff_state: agent:done
  handoff_evidence: off
  # Nothing is terminal: agent:done does not close the PR, and terminal
  # states would close it on transition.
  terminal_states: []
  comments:
    on_completion: false

polling:
  interval_ms: 60000

db_path: .sortie-merge.db

workspace:
  root: $SORTIE_LOOP_WORKSPACES/merge
  retention_days: 30

hooks:
  after_create: |
    git init -q . 2>/dev/null || true
    # Real remote, not ref-only fetches: Step 3 of the prompt must be able
    # to `git fetch origin <base>`; without tracking refs it sees a stale
    # or missing base and falls back to the GitHub compare API.
    git remote add origin "$SORTIE_LOOP_CLONE_URL" 2>/dev/null || true
    # No --depth: a shallow PR root plus a shallow base root share no
    # common ancestor, which breaks `git merge` / merge-base in Step 3.
    git fetch origin "+pull/$SORTIE_ISSUE_IDENTIFIER/head:pr-$SORTIE_ISSUE_IDENTIFIER"
    git checkout -q "pr-$SORTIE_ISSUE_IDENTIFIER"
  # Workspaces created before the remote existed still have none
  # (after_create only runs once); add it idempotently so Step 3's
  # `git fetch origin <base>` works on reused workspaces too.
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
    # Legacy workspaces are shallow from the old after_create. A shallow
    # PR graft plus a shallow base root share no merge-base, so git merge
    # fails even when the compare API would succeed. Fill in the missing
    # history when present; harmless on non-shallow clones.
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
    template: ./prompts/merge.md
---

{{/* Merge loop: label an approved PR `agent:merge` to have the agent
     read the PR plus all comments/reviews, file follow-up issues for
     remaining findings, and merge the branch. The prompt lives in
     prompts/merge.md (via dispatch default above). The agent removes
     agent:merge and in-progress and adds agent:done; on conflicts it also
     adds needs-human and the loop drops the PR via its -label:needs-human
     exclusion. */}}
Merge loop routing only — the prompt comes from the dispatch template.
