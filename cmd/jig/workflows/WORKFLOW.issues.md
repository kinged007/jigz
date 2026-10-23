---
tracker:
  kind: github
  api_key: $SORTIE_TRACKER_API_KEY
  project: $SORTIE_TRACKER_PROJECT
  # Comma is GitHub search OR; the explicit `(label:a OR label:b)` form
  # returns zero results from /search/issues. in-progress re-picks an issue
  # whose agent died before finishing, and keeps a chained issue active
  # between its plan and build stages.
  # Assignee scope is appended by Jigz config at launch, not here.
  query_filter: "label:agent:plan,agent:plan-build,agent:build,agent:docs,in-progress -label:needs-human"
  # 1.27+: an open issue runs only when it carries a label from
  # active_states. Triggers alone would not satisfy the gate, so every
  # dispatch trigger is also an active state. in-progress re-picks a run
  # that was interrupted and keeps a chained issue active between its plan
  # and build stages.
  active_states: [agent:plan, agent:plan-build, agent:build, agent:docs, in-progress]
  in_progress_state: in-progress
  # Backstop only: the agent moves the issue itself (removes its trigger and
  # in-progress, adds agent:done). If it forgot or its gh call failed, this
  # transition adds agent:done on a clean exit. agent:done is not an active
  # state, so nothing re-dispatches a finished issue.
  handoff_state: agent:done
  handoff_evidence: off
  # A closed issue is terminal, which triggers workspace cleanup.
  # agent:done is not terminal: it does not close the issue.
  terminal_states: [closed]
  comments:
    on_completion: false
    on_failure: false

polling:
  interval_ms: 60000

db_path: jigz-issues.db

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
  model: "sonnet"

# Both runtime blocks are present; the engine reads only the one named by
# agent.kind. The agent account pool in machine.yaml overrides agent.kind and
# agent.command at run time.
claude-code:
  model: "sonnet"

# The first matching rule wins, so an issue carrying both agent:plan and
# agent:build is planned first. agent:plan-build runs build.md, which posts
# the plan comment before implementing when none exists.
dispatch:
  rules:
    - name: plan
      match:
        labels: ["agent:plan"]
      template: ./prompts/plan.md
    - name: plan-build
      match:
        labels: ["agent:plan-build"]
      template: ./prompts/plan.md
      next: build-chain
    - name: build
      match:
        labels: ["agent:build"]
      template: ./prompts/build.md
    - name: docs
      match:
        labels: ["agent:docs"]
      template: ./prompts/docs.md
    - name: build-chain
      stage: stage-build
      template: ./prompts/build.md
---

{{/* Issue workflow: plan, build and docs rules. Prompts live in
     prompts/. The body below never renders; it documents the routing. */}}
Issue workflow routing only — the prompt comes from the dispatch template.
