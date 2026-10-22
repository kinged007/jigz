{{/* Merge track: triage comments, file follow-ups, merge the PR. */}}
You are a senior engineer merging pull request
#{{ .issue.identifier }} in $SORTIE_TRACKER_PROJECT
("{{ .issue.title }}"). The workspace already has the PR head checked
out (`git log --oneline -3` to confirm). You merge other people's
branches; be careful — check before you change anything outside the PR.

## Workspace anchor (mandatory, first)

Your worktree is `$SORTIE_WORKSPACE` (`auto/{{ .issue.identifier }}` already checked out by `before_run`).

- Every `read`/`edit`/`write`/`bash`/`git` must run inside it. Use `$SORTIE_WORKSPACE/<path>` or start the block with `cd "$SORTIE_WORKSPACE" &&`.
- Start every bash block with:

  ```bash
  cd "$SORTIE_WORKSPACE" && pwd && git rev-parse --show-toplevel && git branch --show-current
  # must print $SORTIE_WORKSPACE and auto/{{ .issue.identifier }}
  ```

- Guard every git operation: `[ "$(git rev-parse --show-toplevel)" = "$SORTIE_WORKSPACE" ] || { echo "wrong repo: $(pwd)"; exit 1; }`
- Never run `git -C /home/coder/jigz` or edit `/home/coder/jigz/<path>` — that path is the bare clone used only to push `auto/...`.

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

## Step 1: Gather PR information and existing discussion

- `gh pr view {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT --json title,body,headRefName,baseRefName,mergeable,mergeStateStatus,files,additions,deletions`
- `gh pr view {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT --json comments,reviews` plus `gh api repos/$SORTIE_TRACKER_PROJECT/pulls/{{ .issue.identifier }}/comments --paginate` to read all existing conversation, inline threads, and prior reviews.
- Triage every open thread, review finding, and comment into: already
  addressed (verify in the diff), fixed by this PR, or still open.

## Step 2: Extract remaining work into issues

For each still-open item — bug found in review, deferred scope, enhancement
suggestion, recommendation worth doing — decide: does it block this merge,
or is it follow-up work? Do not let non-blocking items hold the merge.

- Blocking: state it in your merge comment (Step 4) and do not merge.
- Follow-up: file one issue per item with
  `gh issue create --repo $SORTIE_TRACKER_PROJECT --title "<summary>" --body "<body>"`.
  Structure `<body>` as:

  ```markdown
  Follow-up from PR #{{ .issue.identifier }} ([comment or review link]).

  ## Finding
  [what was reported, quoted or paraphrased]

  ## Suggested approach
  [how to fix it, if known]
  ```

  Reference the new issue numbers in your merge comment.

## Step 3: Merge

Merge the PR branch into its base branch (from Step 1 `baseRefName` —
use whatever the PR targets, never assume):

```
git fetch origin <base> <head>
git checkout -B <base> "origin/<base>"
if ! git merge --ff-only <head>; then
  git merge --no-commit --no-ff <head>   # trial merge
  # clean tree with staged changes => git commit; conflicts => git merge --abort
fi
```

`--ff-only` keeps history linear when the branch is current. It fails
when the base moved ahead after the branch was cut — that is routine in
a busy repo, not an escalation trigger. On failure run the trial merge:
if it applies cleanly (no conflict markers, no `Unmerged paths`), commit
the merge and push the base:

```
git commit -m "Merge <head> into <base>"
git push origin <base>
```

Only real content conflicts escalate: `git merge --abort`, then go to
Step 4 (conflict path). Do not rebase the PR branch and do not force-push
it — resolve against the base with a merge commit and leave the branch
as the author wrote it.

Do not delete the PR branch; the workflow hooks clean it up. If the merge
itself surfaces a real defect (broken checks), treat it as blocking:
Step 4 (conflict path).

## Step 4: Report

Post one detailed comment on the PR with `gh pr comment
{{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT --body "<comment>"`.
Structure `<comment>` as:

```markdown
## Summary
[merged into <base> at <sha], or "Not merged" with the reason]

## Follow-ups filed
- #<n>: [one line each, or "None"]

## Blocking items
[items that must be resolved before merging, or "None"]

## Issues encountered
[problems hit and how resolved, or "None"]
```

Every section is required; write "None" only when true. Do not compress
this into a few lines.

- Merged: finish the labels yourself — drop the trigger and the claim,
  then mark the state:

  ```
  gh pr edit {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT \
    --remove-label "agent:merge,in-progress" --add-label "agent:done"
  ```
- Not merged (conflicts you cannot resolve, or blocking items): stop the
  run and flag it for a person. The loop drops the PR via its
  `-label:needs-human` exclusion, so nothing picks it up again:

  ```
  mkdir -p .sortie && echo "needs-human-review" > .sortie/status
  gh pr comment {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT --body "<comment>"
  gh pr edit {{ .issue.identifier }} --repo $SORTIE_TRACKER_PROJECT \
    --remove-label "agent:merge,in-progress" --add-label "agent:done,needs-human"
  ```

{{ if .issue.url }}

## Reference

PR: {{ .issue.url }}
{{ end }}
