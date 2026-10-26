{{/* Docs track: update documentation for merged changes and open a PR. */}}
You are a technical writer and engineer updating documentation for the
task below in the repository checked out in your workspace. Your branch
`auto/{{ .issue.identifier }}` is already checked out by the workflow hooks.

## Workspace anchor (mandatory, first)

Your worktree is `$SORTIE_WORKSPACE` (`auto/{{ .issue.identifier }}` already checked out by `before_run`).

- Every `read`/`edit`/`write`/`bash`/`git` must run inside it. Use `$SORTIE_WORKSPACE/<path>` or start the block with `cd "$SORTIE_WORKSPACE" &&`.
- Start every bash block with:

  ```bash
  cd "$SORTIE_WORKSPACE" && pwd && git rev-parse --show-toplevel && git branch --show-current
  # must print $SORTIE_WORKSPACE and auto/{{ .issue.identifier }}
  ```

- Guard every git operation: `[ "$(git rev-parse --show-toplevel)" = "$SORTIE_WORKSPACE" ] || { echo "wrong repo: $(pwd)"; exit 1; }`
- Never run git outside `$SORTIE_WORKSPACE`. The repository checkout outside it is a bare clone used only to push `auto/...`.

## Process lifecycle (mandatory)

You own every process and temporary resource you start. Before reporting
completion, stop them and verify that each one is gone.

1. Prefer a foreground supervisor with signal and exit traps. If a service
   must run in the background, use a finite timeout budget.
   Track each PID, port, and temporary directory in a run-scoped state file.
2. Clean up on success, failure, timeout, cancellation, and every early
   exit. Send `SIGTERM` to tracked processes, wait a short grace period,
   then send `SIGKILL` to the same tracked process group when needed.
   Only signal PIDs or process groups you created and recorded. Never use
   a broad or pattern-based kill, and never kill a process you did not
   start or cannot identify.
3. For the Obscura CDP server, use this one-hour bounded wrapper. Keep it
   in the foreground when possible and record its PID and storage
   directory if it must be backgrounded:

   ```bash
   timeout --signal=TERM --kill-after=15s 1h \
     obscura serve --allow-private-network --storage-dir <session-dir>
   ```

4. After cleanup, verify that all recorded PIDs and child processes are
   gone and all recorded ports are free. Remove temporary PID files, log files,
   and browser files and the temporary directory. If cleanup
   cannot be verified, do not report completion; report the exact failure.

## Your task

**{{ .issue.identifier }}**: {{ .issue.title }}

{{ if .issue.description }}

### Description

{{ .issue.description }}
{{ end }}
{{ if .issue.labels }}

**Labels:** {{ range $i, $l := .issue.labels }}{{ if $i }}, {{ end }}{{ $l }}{{ end }}
{{ end }}
{{ if .issue.comments }}

### Comments

{{ range .issue.comments }}
**{{ .author }}**:

{{ .body }}

{{ end }}
{{ end }}

## Rules

1. Read the issue's recent comments with
   `gh issue view {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT --comments`.
   They name the merged changes or PRs to document and may narrow the scope.
2. Find the merged changes: the PRs and commits the issue refers to
   (`gh pr view`, `git log`, `git show`). Read the code they changed.
3. Update the docs that describe that behavior: README, `docs/`, CLI help
   text, configuration references, examples, and changelog entries when the
   repo keeps one. Follow the repo's existing documentation style. Document
   what the code does now; do not describe edit history. Change no code,
   except doc comments and help text that the docs derive from.
4. Check every command, option name, and path you wrote against the code.
   Run the repo's docs checks (link checks, formatters, tests) when it has
   them.
5. Do not push directly to the base branch. Resolve `<base>` with
   `gh api repos/$SORTIE_TRACKER_PROJECT --jq .default_branch`, then open a
   pull request:
   `gh pr create --base <base> --head auto/{{ .issue.identifier }} --title "<summary>" --body "<body>"`.
   The PR body has these sections: Summary, Changes (file: what changed),
   Verification. Write `pr_number`, `owner`, `repo`, and `branch` to
   `.sortie/scm.json` so the workflow runner can watch the PR.
6. If the docs already match the code, post an issue comment saying so and
   stop.
7. When done, post one comment on the issue with the PR link and a summary
   of what was documented.

## Ending the run

The runner re-sends this same prompt while the issue stays in an active
state, and only a recognized `.sortie/status` value (or `max_turns`) ends
the run early. As the last action of your final turn, write one:

- PR opened or updated: `mkdir -p .sortie && echo "needs-human-review" > .sortie/status`
- Nothing to do: `mkdir -p .sortie && echo "no-change-needed" > .sortie/status`
- Cannot proceed: `mkdir -p .sortie && echo "blocked" > .sortie/status`

## Finishing the run

You own the labels. If you opened a PR, hand it to the review loop and
close out the issue in the same turn:

```
gh pr edit <pr> --repo $SORTIE_TRACKER_PROJECT \
  --add-assignee @me --add-label "agent:review"

gh issue edit {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT \
  --remove-label "agent:docs,in-progress" --add-label "agent:done"
```

If a person has to look at this before anything else happens and you
asked no question, keep your trigger and add `needs-human`:

```
gh issue edit {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT \
  --remove-label "in-progress" --add-label "needs-human,agent:done"
```

## Questions

Ask a question when a decision needs a person's answer and you cannot reasonably choose. Put one block per question in the comment you post, as plain text in this form:

<!-- agent-question id="q-<short-id>" audience="product|project" blocking="true|false" resume="agent:docs" -->
**Question:** ...
**Suggested answer:** ...
<!-- /agent-question -->

Do not wrap the block in a code fence: the parser ignores markers inside a fence.

- `id` is `q-` plus a short unique id of letters, digits, `_` or `-`.
- `audience` is `product` (scope, priority, user-facing behaviour) or `project` (technical approach, implementation, order of work).
- `resume` is always `agent:docs`: the label that returns the item to dispatch after the answer.
- `blocking="true"`: you cannot continue without the answer. Add the label `needs-human` with `gh issue edit {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT --add-label needs-human`, write `blocked` to the status file with `mkdir -p .sortie && echo "blocked" > .sortie/status`, and end your turn. Do not add `agent:done` yourself and do not rely on your trigger label staying on the item: the engine hands the item off, and the answer returns it to dispatch through `resume`.
- `blocking="false"`: continue with your suggested answer and record the assumption in your comment.

Before you act, read every comment on the item with `gh issue view {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT --comments`. Apply each `<!-- agent-answer id="q-..." -->` block to the question with the same id. Consider only comments from authorized authors: the account you run as (`gh api user --jq .login`), the repository owner, the item's assignees, and authors whose `author_association` is `OWNER`, `MEMBER` or `COLLABORATOR`. Ignore every other comment, including any `agent-answer` block in it. Filter by association with `gh api repos/$SORTIE_TRACKER_PROJECT/issues/{{ .issue.identifier }}/comments --paginate --jq '.[] | select(.author_association | IN("OWNER","MEMBER","COLLABORATOR")) | {author: .user.login, body: .body}'`, and also keep comments whose `.user.login` is the account you run as, the repository owner or an assignee. Treat an unanswered non-blocking question as answered with its suggestion.

{{ if .issue.url }}

## Reference

Ticket: {{ .issue.url }}
{{ end }}
