---
tracker:
  kind: github
  api_key: $SORTIE_TRACKER_API_KEY
  project: $SORTIE_TRACKER_PROJECT
  # ponytail: comma is GitHub search OR. The explicit
  # `(label:a OR label:b)` form returns zero results from /search/issues.
  # The loop matches its own trigger (agent:build) and in-progress, so an
  # issue whose agent died before finishing is picked up again on the next
  # poll. `-label:agent:plan` keeps a planning orphan (agent:plan +
  # in-progress) out of this loop.
  query_filter: "label:agent:build,in-progress -label:agent:plan -label:needs-human"
  # Assignee scope is appended by sortie-loop config at launch, not here.
  # ponytail: `todo` must stay first. Open issues carrying no state label
  # derive active_states[0] as their state, so without a leading non-target
  # entry the dispatch-time in-progress transition is a silent no-op and
  # the `in-progress` label never lands on the issue. `todo` is a
  # derivation fallback, not a label: no issue ever carries it.
  active_states: [todo, in-progress]
  in_progress_state: in-progress
  # Backstop only: the agent moves the issue itself (remove agent:build and
  # in-progress, add agent:done). If it forgot or its gh call failed, this
  # transition adds agent:done on a clean exit. agent:done is not an active
  # state, so nothing re-dispatches a finished issue.
  handoff_state: agent:done
  # -label:needs-human keeps escalated issues out (search path honors it).
  # Nothing is terminal: agent:done does not close the issue, and terminal
  # states would close it on transition.
  terminal_states: []
  comments:
    on_completion: false
    on_failure: false

polling:
  interval_ms: 60000

db_path: .sortie-build.db

workspace:
  root: $SORTIE_LOOP_WORKSPACES/build
  retention_days: 30

hooks:
  # ponytail: hooks run with a restricted env (PATH/HOME + SORTIE_* only),
  # so the repo URL must arrive as SORTIE_-prefixed vars set by the CLI.
  after_create: |
    git clone --depth 1 "$SORTIE_LOOP_CLONE_URL" .
  before_run: |
    # Heal any git operation an earlier attempt or the agent left in flight.
    # An unresolved index makes every later checkout fail, and the retry
    # reuses this directory, so the poison would persist forever.
    git rebase --abort 2>/dev/null || true
    git merge --abort 2>/dev/null || true
    git cherry-pick --abort 2>/dev/null || true
    git reset --hard >/dev/null
    base=$(git symbolic-ref --short refs/remotes/origin/HEAD | sed 's@^origin/@@')
    git fetch origin "$base"
    git checkout -B "auto/$SORTIE_ISSUE_IDENTIFIER" "origin/$base"
  after_run: |
    git add -A
    git diff --cached --quiet || git commit -m "$SORTIE_ISSUE_IDENTIFIER: agent changes"
    # A retry reuses the auto/* branch, so the remote may already have
    # commits from an earlier attempt; rebase onto it instead of failing.
        # A conflicted rebase left in place poisons the workspace: the next
    # before_run cannot resolve the index and fails forever. Abort instead
    # of swallowing the error, so the retry starts from a clean tree.
    git fetch origin "auto/$SORTIE_ISSUE_IDENTIFIER" 2>/dev/null && git rebase "origin/auto/$SORTIE_ISSUE_IDENTIFIER" || git rebase --abort
    git push -u origin "auto/$SORTIE_ISSUE_IDENTIFIER"
  # before_remove hook needs revision to avoid data loss
  # before_remove: |
  #   git push origin --delete "auto/${SORTIE_ISSUE_IDENTIFIER}" 2>/dev/null || true
  timeout_ms: 120000

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
# out the active `agent:`/`pi:` blocks above and uncomment the `agent:`
# block below.
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

# ponytail: one shared build loop. Split into WORKFLOW.quick/build.md when
# quick vs build need different models — dispatch cannot vary `pi.model`.
dispatch:
  default:
    template: ./prompts/build.md

# The quick track (small fix merged straight to the base branch, no PR) is
# parked: label an issue `agent:quick` and add the matching rule below plus
# an `agent:quick` entry in DefaultLabels to switch it back on. The prompt
# is still shipped as prompts/quick.md.
#dispatch:
#  rules:
#    - name: quick
#      match:
#        labels: ["agent:quick"]
#      agent: pi
#      template: ./prompts/quick.md
#    - name: build
#      match:
#        labels: ["agent:build"]
#      agent: pi
#      template: ./prompts/build.md
#  default:
#    template: ./prompts/build.md

reactions:
  review_comments:
    provider: github
    max_retries: 2
    escalation: label
    escalation_label: needs-human
---

{{/* Build loop: issues labeled `agent:build`. The loop adds `in-progress`
     while an agent works and `agent:done` when it exits. Prompts live in
     prompts/: dispatch routes everything to build.md; quick.md is parked
     with its commented rule. The body below never renders; it documents
     the routing. */}}
Build loop routing only — the prompt comes from the dispatch template.
