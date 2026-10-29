---
# jig:generated v1 skeleton=prs engine=kinged007/sortie@v1.28.1 sha256=ca33e867d2f6a32d8c1c2634fcdc97a6afe4b6554beb0dc00d40423c3ac927c7
tracker:
  kind: github-pr
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
  query_filter: "label:agent:review -label:needs-human"
  active_states: [agent:review, in-progress]
  in_progress_state: in-progress
  handoff_state: agent:done
  handoff_evidence: off
  terminal_states: [closed]
  comments:
    on_completion: false

polling:
  interval_ms: 60000

db_path: ../data/jigz-prs.db

workspace:
  root: $JIG_WORKSPACES/prs
  retention_days: 30

hooks:
  # Hooks run with a restricted env (PATH/HOME plus the vars the CLI and
  # the engine set), so the repo URL is built from the tracker project
  # the engine already has.
  after_create: |
    git init -q . 2>/dev/null || true
    # A real remote, not ref-only fetches: the fix rule pushes back and the
    # merge rule fetches the base branch.
    git remote add origin "https://github.com/$SORTIE_TRACKER_PROJECT.git" 2>/dev/null || true
    # No --depth: a shallow PR root plus a shallow base root share no
    # common ancestor, which breaks `git merge` / merge-base in the merge rule.
    git fetch origin "+pull/$SORTIE_ISSUE_IDENTIFIER/head:pr-$SORTIE_ISSUE_IDENTIFIER"
    git checkout -q "pr-$SORTIE_ISSUE_IDENTIFIER"
  before_run: |
    git rev-parse --git-dir >/dev/null 2>&1 || exit 0
    # Heal any git operation an earlier attempt or the agent left in flight.
    # An unresolved index makes every later checkout fail, and the retry
    # reuses this directory, so the poison would persist forever.
    git rebase --abort 2>/dev/null || true
    git merge --abort 2>/dev/null || true
    git cherry-pick --abort 2>/dev/null || true
    git reset --hard >/dev/null
    # The workspace is shared by the review, fix and merge rules and
    # created once, so a later rule starts from the current PR head.
    git remote get-url origin >/dev/null 2>&1 || git remote add origin "https://github.com/$SORTIE_TRACKER_PROJECT.git"
    if [ -f .git/shallow ]; then git fetch --unshallow origin 2>/dev/null || true; fi
    git checkout -q "pr-$SORTIE_ISSUE_IDENTIFIER" 2>/dev/null || true
    git fetch origin "pull/$SORTIE_ISSUE_IDENTIFIER/head" 2>/dev/null && git merge --ff-only FETCH_HEAD >/dev/null 2>&1 || true
  timeout_ms: 60000

agent:
  kind: claude-code
  command: /usr/local/bin/jig agent-exec reviewer
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
    - name: review
      match:
        labels: ["agent:review"]
      template: prompts/review.md
  default:
    template: prompts/default.md
---
