#!/bin/sh
# Remove old per-item workspaces whose git tree is clean.
#
# Installed by `jig setup` (edit freely; an existing file is never
# overwritten). jig runs this through `sh -c` in the repo root on the
# cleanup schedule (cleanup.command in .jigz/config.yaml). Environment:
#
#   JIG_CLEANUP_WORKSPACE_ROOT  absolute path of .jigz/workspaces
#   JIG_CLEANUP_MAX_AGE_DAYS    remove checkouts older than this many days
#   JIG_CLEANUP_DRY_RUN         1 = report only, remove nothing
#
# Contract with jig: print `removed: <path>` on stdout for every removed
# (or would-be-removed) checkout; other lines land in the run's output
# tail. Up to 200 removed paths are recorded and shown in the dashboard's
# Last cleanup column; a non-zero exit marks the run failed there too.
#
# Safety: a checkout is removed only when it is older than
# JIG_CLEANUP_MAX_AGE_DAYS, holds a git work tree, and
# `git status --porcelain` is empty. Anything else is kept and reported
# as `kept: <path> (<reason>)`, so uncommitted agent work is never
# thrown away.
set -u
root="${JIG_CLEANUP_WORKSPACE_ROOT:?JIG_CLEANUP_WORKSPACE_ROOT is not set}"
max="${JIG_CLEANUP_MAX_AGE_DAYS:?JIG_CLEANUP_MAX_AGE_DAYS is not set}"
dry="${JIG_CLEANUP_DRY_RUN:-0}"
[ -d "$root" ] || { echo "no workspaces at $root; nothing to do"; exit 0; }
command -v git >/dev/null 2>&1 || { echo "git is not on PATH; removing nothing" >&2; exit 1; }
failed="${TMPDIR:-/tmp}/jig-cleanup-failed.$$"
trap 'rm -f "$failed"' EXIT INT TERM
# Two levels down: <loop>/<checkout>. A running loop's checkouts are
# young, so the age gate already skips them; the clean-tree gate keeps
# anything with uncommitted work.
find "$root" -mindepth 2 -maxdepth 2 -type d -mtime +"$max" -print | sort |
while IFS= read -r dir; do
  if [ ! -e "$dir/.git" ]; then
    echo "kept: $dir (not a git checkout)"
    continue
  fi
  if ! out=$(git -C "$dir" status --porcelain 2>/dev/null); then
    echo "kept: $dir (git status failed)"
    continue
  fi
  if [ -n "$out" ]; then
    echo "kept: $dir (dirty worktree)"
    continue
  fi
  if [ "$dry" = "1" ]; then
    echo "removed: $dir"
  elif rm -rf "$dir"; then
    echo "removed: $dir"
  else
    echo "failed: $dir" >&2
    echo x >>"$failed"
  fi
done
[ -s "$failed" ] && exit 1
exit 0
