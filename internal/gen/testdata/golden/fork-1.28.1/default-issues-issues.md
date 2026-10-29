---
# jig:generated v1 skeleton=issues engine=kinged007/sortie@v1.28.1 sha256=65df6a87ea8c541b65d250b3984eb59fe16029e1205ac650e0ab692b4e45c5bb
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
  query_filter: "label:agent:build,in-progress -label:needs-human"
  active_states: [agent:build, in-progress]
  in_progress_state: in-progress
  handoff_state: agent:done
  handoff_evidence: off
  terminal_states: [closed]
  comments:
    on_completion: false
    on_failure: false

polling:
  interval_ms: 60000

db_path: ../data/jigz-issues.db

workspace:
  root: $JIG_WORKSPACES/issues
  retention_days: 30

hooks:
  # Hooks run with a restricted env (PATH/HOME plus the vars the CLI and
  # the engine set), so the repo URL is built from the tracker project
  # the engine already has.
  after_create: |
    git clone --depth 1 "https://github.com/$SORTIE_TRACKER_PROJECT.git" .
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
    # A stage that changed nothing (plan) pushes no branch.
    base=$(git symbolic-ref --short refs/remotes/origin/HEAD | sed 's@^origin/@@')
    [ "$(git rev-parse HEAD)" = "$(git rev-parse "origin/$base")" ] && exit 0
    # A retry reuses the auto/* branch, so the remote may already have
    # commits from an earlier attempt; rebase onto it instead of failing.
    # A conflicted rebase left in place poisons the workspace: the next
    # before_run cannot resolve the index and fails forever. Abort instead
    # of swallowing the error, so the retry starts from a clean tree.
    git fetch origin "auto/$SORTIE_ISSUE_IDENTIFIER" 2>/dev/null && git rebase "origin/auto/$SORTIE_ISSUE_IDENTIFIER" || git rebase --abort
    git push -u origin "auto/$SORTIE_ISSUE_IDENTIFIER"
  timeout_ms: 120000

agent:
  kind: claude-code
  command: /usr/local/bin/jig agent-exec builder
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
  model: "claude-haiku-5-5"

# The first matching rule wins, so an item carrying two rule labels runs
# the earlier rule first. dispatch.default is the safety net: the engine
# dispatches a query-matching item that matches no rule, and this keeps
# that dispatch inside a prompt of ours.
dispatch:
  rules:
    - name: build
      match:
        labels: ["agent:build"]
      template: prompts/build.md
  default:
    template: prompts/default.md
---
