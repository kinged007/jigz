---
# jig:generated v1 skeleton=delivery-planner engine=kinged007/sortie@v1.28.1 sha256=804a7ce9be2828d2cf87b37893cdca51dcd3010400ea08bd554c30a39ebf9fda
tracker:
  kind: github
  api_key: $JIG_TRACKER_TOKEN
  project: owner/repo
  # A label query. The tracker evaluates it once at loop start; a change
  # needs a drain and restart.
  #
  # Agent, in-progress and human-escalation routing labels. Trigger labels
  # start work; in-progress keeps the loop from claiming its own running
  # work twice; needs-human parks an item for a person.
  #
  # The assignee scope is always @me; it is appended at launch, not stored
  # here.
  query_filter: "-label:needs-human label:agent:plan"
  active_states: [agent:plan, in-progress]
  in_progress_state: in-progress
  handoff_state: 
  handoff_evidence: off
  terminal_states: [closed]
  comments:
    on_completion: false
    on_failure: false

polling:
  interval_ms: 60000

db_path: ../data/jigz-delivery-planner.db

workspace:
  root: $JIG_WORKSPACES/delivery-planner
  retention_days: 30

hooks:
  # Hooks run with a restricted env (PATH/HOME plus the vars the CLI and
  # the engine set). Each script starts with a preamble that exports the
  # JIG_* names from the engine's values.
  after_create: |
    JIG_ISSUE_ID=$SORTIE_ISSUE_ID JIG_ISSUE_IDENTIFIER=$SORTIE_ISSUE_IDENTIFIER JIG_WORKSPACE=$SORTIE_WORKSPACE JIG_ATTEMPT=$SORTIE_ATTEMPT JIG_TRACKER_PROJECT=$SORTIE_TRACKER_PROJECT
    export JIG_ISSUE_ID JIG_ISSUE_IDENTIFIER JIG_WORKSPACE JIG_ATTEMPT JIG_TRACKER_PROJECT
    git clone --depth 1 "https://github.com/$JIG_TRACKER_PROJECT.git" .
  before_run: |
    JIG_ISSUE_ID=$SORTIE_ISSUE_ID JIG_ISSUE_IDENTIFIER=$SORTIE_ISSUE_IDENTIFIER JIG_WORKSPACE=$SORTIE_WORKSPACE JIG_ATTEMPT=$SORTIE_ATTEMPT JIG_TRACKER_PROJECT=$SORTIE_TRACKER_PROJECT
    export JIG_ISSUE_ID JIG_ISSUE_IDENTIFIER JIG_WORKSPACE JIG_ATTEMPT JIG_TRACKER_PROJECT
    # Heal any git operation an earlier attempt or the agent left in flight.
    # An unresolved index makes every later checkout fail, and the retry
    # reuses this directory, so the poison would persist forever.
    git rebase --abort 2>/dev/null || true
    git merge --abort 2>/dev/null || true
    git cherry-pick --abort 2>/dev/null || true
    git reset --hard >/dev/null
    base=$(git symbolic-ref --short refs/remotes/origin/HEAD | sed 's@^origin/@@')
    git fetch origin "$base"
    git checkout -B "auto/$JIG_ISSUE_IDENTIFIER" "origin/$base"
  after_run: |
    JIG_ISSUE_ID=$SORTIE_ISSUE_ID JIG_ISSUE_IDENTIFIER=$SORTIE_ISSUE_IDENTIFIER JIG_WORKSPACE=$SORTIE_WORKSPACE JIG_ATTEMPT=$SORTIE_ATTEMPT JIG_TRACKER_PROJECT=$SORTIE_TRACKER_PROJECT
    export JIG_ISSUE_ID JIG_ISSUE_IDENTIFIER JIG_WORKSPACE JIG_ATTEMPT JIG_TRACKER_PROJECT
    JIG_SELF_REVIEW_STATUS=$SORTIE_SELF_REVIEW_STATUS JIG_SELF_REVIEW_SUMMARY_PATH=$SORTIE_SELF_REVIEW_SUMMARY_PATH
    export JIG_SELF_REVIEW_STATUS JIG_SELF_REVIEW_SUMMARY_PATH
    git add -A
    git diff --cached --quiet || git commit -m "$JIG_ISSUE_IDENTIFIER: agent changes"
    # A stage that changed nothing (plan) pushes no branch.
    base=$(git symbolic-ref --short refs/remotes/origin/HEAD | sed 's@^origin/@@')
    [ "$(git rev-parse HEAD)" = "$(git rev-parse "origin/$base")" ] && exit 0
    # A retry reuses the auto/* branch, so the remote may already have
    # commits from an earlier attempt; rebase onto it instead of failing.
    # A conflicted rebase left in place poisons the workspace: the next
    # before_run cannot resolve the index and fails forever. Abort instead
    # of swallowing the error, so the retry starts from a clean tree.
    git fetch origin "auto/$JIG_ISSUE_IDENTIFIER" 2>/dev/null && git rebase "origin/auto/$JIG_ISSUE_IDENTIFIER" || git rebase --abort
    git push -u origin "auto/$JIG_ISSUE_IDENTIFIER"
  timeout_ms: 300000

agent:
  kind: claude-code
  command: /usr/local/bin/jig agent-exec planner
  max_turns: 10
  max_concurrent_agents: 1
  turn_timeout_ms: 3600000
  read_timeout_ms: 120000
  stall_timeout_ms: 300000
  stop_grace_ms: 5000
  max_retry_backoff_ms: 120000

# Only the block the agent kind names is emitted; the engine reports
# blocks for other kinds as unknown keys.
claude-code:
  model: "claude-sonnet-5-5"

# The first matching rule wins, so an item carrying two rule labels runs
# the earlier rule first. dispatch.default is the safety net: the engine
# dispatches a query-matching item that matches no rule, and this keeps
# that dispatch inside a prompt of ours.
dispatch:
  rules:
    - name: plan
      match:
        labels: ["agent:plan"]
      template: prompts/plan.md
  default:
    template: prompts/default.md
---
