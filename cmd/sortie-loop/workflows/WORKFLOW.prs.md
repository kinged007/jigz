---
tracker:
  kind: github-pr
  api_key: $SORTIE_TRACKER_API_KEY
  project: $SORTIE_TRACKER_PROJECT
  # A non-empty query_filter is sent to GitHub search, which applies the
  # label and exclusion clauses; active_states below still decides which
  # labels make a PR a candidate. Assignee scope is appended by sortie-loop
  # config at launch, not here. in-progress re-picks a run that was interrupted. agent:build means
  # implement on an issue (WORKFLOW.issues.md) and apply the posted review
  # findings on a PR.
  query_filter: "label:agent:review,agent:build,agent:merge,in-progress -label:needs-human"
  # 1.27+: a PR runs only when it carries a label from active_states. Every
  # dispatch trigger is listed there so a PR labeled only with its trigger
  # is still a candidate. in-progress re-picks an interrupted run.
  active_states: [agent:review, agent:build, agent:merge, in-progress]
  in_progress_state: in-progress
  # Backstop only: the agent removes its trigger and in-progress and adds
  # agent:done (or the next trigger: agent:review after a fix, agent:build
  # after a review with findings; plus needs-human when it cannot proceed).
  handoff_state: agent:done
  handoff_evidence: off
  # A closed PR is terminal, which triggers workspace cleanup.
  # agent:done is not terminal: it does not close the PR.
  terminal_states: [closed]
  comments:
    on_completion: false

polling:
  interval_ms: 60000

db_path: .sortie-prs.db

workspace:
  root: $SORTIE_LOOP_WORKSPACES/prs
  retention_days: 30

hooks:
  after_create: |
    git init -q . 2>/dev/null || true
    # A real remote, not ref-only fetches: the fix rule pushes back and the
    # merge rule fetches the base branch.
    git remote add origin "$SORTIE_LOOP_CLONE_URL" 2>/dev/null || true
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
    git remote get-url origin >/dev/null 2>&1 || git remote add origin "$SORTIE_LOOP_CLONE_URL"
    if [ -f .git/shallow ]; then git fetch --unshallow origin 2>/dev/null || true; fi
    git checkout -q "pr-$SORTIE_ISSUE_IDENTIFIER" 2>/dev/null || true
    git fetch origin "pull/$SORTIE_ISSUE_IDENTIFIER/head" 2>/dev/null && git merge --ff-only FETCH_HEAD >/dev/null 2>&1 || true
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
  model: "sonnet"

# Both runtime blocks are present; the engine reads only the one named by
# agent.kind. The agent account pool in machine.yaml overrides agent.kind and
# agent.command at run time.
claude-code:
  model: "sonnet"

# The first matching rule wins, so a PR carrying two trigger labels runs the
# earlier rule.
dispatch:
  rules:
    - name: review
      match:
        labels: ["agent:review"]
      template: ./prompts/review.md
    - name: review-fix
      match:
        labels: ["agent:build"]
      template: ./prompts/review-fix.md
    - name: merge
      match:
        labels: ["agent:merge"]
      template: ./prompts/merge.md
---

{{/* PR workflow: review, review-fix and merge rules selected by trigger
     label. Prompts live in prompts/. The agent routes the PR on: review
     adds agent:build when there are findings to fix and agent:done when
     clean, review-fix adds agent:review for another pass, merge finishes
     with agent:done. Blocked work keeps its trigger and adds needs-human,
     which the query_filter exclusion drops. The body below never renders;
     it documents the routing. Auto-review default true, opt-out via SORTIE_AUTO_REVIEW=false or label no-auto-review. */}}
PR workflow routing only — the prompt comes from the dispatch template.
