package gen

// Default hook bodies per tracker kind. They are the bodies of the
// shipped cmd/jig/workflows/WORKFLOW.issues.md (github) and
// WORKFLOW.prs.md (github-pr), kept here so generated documents stand
// alone. A config that sets a hook overrides its default.
//
// The engine passes the tracker project and the issue id to the hooks
// in SORTIE_TRACKER_PROJECT and SORTIE_ISSUE_IDENTIFIER.

const defaultAfterCreateIssues = `git clone --depth 1 "https://github.com/$SORTIE_TRACKER_PROJECT.git" .
`

const defaultBeforeRunIssues = `# Heal any git operation an earlier attempt or the agent left in flight.
# An unresolved index makes every later checkout fail, and the retry
# reuses this directory, so the poison would persist forever.
git rebase --abort 2>/dev/null || true
git merge --abort 2>/dev/null || true
git cherry-pick --abort 2>/dev/null || true
git reset --hard >/dev/null
base=$(git symbolic-ref --short refs/remotes/origin/HEAD | sed 's@^origin/@@')
git fetch origin "$base"
git checkout -B "auto/$SORTIE_ISSUE_IDENTIFIER" "origin/$base"
`

const defaultAfterRunIssues = `git add -A
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
`

const defaultAfterCreatePRs = `git init -q . 2>/dev/null || true
# A real remote, not ref-only fetches: the fix rule pushes back and the
# merge rule fetches the base branch.
git remote add origin "https://github.com/$SORTIE_TRACKER_PROJECT.git" 2>/dev/null || true
# No --depth: a shallow PR root plus a shallow base root share no
# common ancestor, which breaks ` + "`git merge`" + ` / merge-base in the merge rule.
git fetch origin "+pull/$SORTIE_ISSUE_IDENTIFIER/head:pr-$SORTIE_ISSUE_IDENTIFIER"
git checkout -q "pr-$SORTIE_ISSUE_IDENTIFIER"
`

const defaultBeforeRunPRs = `git rev-parse --git-dir >/dev/null 2>&1 || exit 0
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
`

// defaultHooks fills the unset hooks of h with the tracker-kind bodies.
// The prs entry has no after_run: a review that changed nothing pushes
// no branch, so the hook stays empty.
func defaultHooks(kind string, h *hooksView) {
	if h.AfterCreate == "" {
		switch kind {
		case "github-pr":
			h.AfterCreate = defaultAfterCreatePRs
		default:
			h.AfterCreate = defaultAfterCreateIssues
		}
	}
	if h.BeforeRun == "" {
		switch kind {
		case "github-pr":
			h.BeforeRun = defaultBeforeRunPRs
		default:
			h.BeforeRun = defaultBeforeRunIssues
		}
	}
	if h.AfterRun == "" && kind != "github-pr" {
		h.AfterRun = defaultAfterRunIssues
	}
	if h.TimeoutMS == 0 {
		switch kind {
		case "github-pr":
			h.TimeoutMS = 60000
		default:
			h.TimeoutMS = 120000
		}
	}
}
