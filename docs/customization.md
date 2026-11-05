# Customization

## Ad hoc: auto-merge on clean review

To fully automate build → review → merge on one repo (not a template
default), edit that repo's `.jigz/workflows/prompts/review.md` Step 4
clean branch to chain the merge loop instead of stopping:

```
gh pr edit {{ .issue.identifier }} --repo $JIG_TRACKER_PROJECT \
  --remove-label "agent:review,in-progress" --add-label "agent:merge"
```

The merge loop watches `label:agent:merge`, so it picks the PR up on the
next poll (~60s), triages comments, merges, and ends on `agent:done`.
Blocking findings and conflicts still stop the merge and escalate via
`needs-human` — automation holds everywhere except where judgment is
required.
## Agent identity (name/avatar on GitHub)

The commit author name/email is what GitHub displays. Point the loop's git
identity at the agent — in the target repo or globally:

```sh
git config --global user.name "jig-agent"
git config --global user.email "agent@example.com"
```

`user.name` is display-only. GitHub links the commit (and its avatar) to the
GitHub account whose **verified email** matches the author email. One account
= one avatar, so every agent using your emails shows your personal avatar.
Options for a distinct face: a separate bot account per agent (own email +
avatar, verified there); or no GitHub account at all plus a per-email avatar
on gravatar.com (GitHub falls back to the Gravatar for unclaimed emails);
or publishing the agent as a GitHub App (`name[bot]`, its own avatar).
## Building

```sh
go build ./... && go vet ./... && go test ./...
```

Go 1.26.0 or newer. The floor comes from the dependencies:
`golang.org/x/sys` requires 1.26.0 and `modernc.org/sqlite` requires
1.25.0. `go install` needs the same version, so use a release binary if
yours is older.
## Customizing agents and workflows

The shipped default is the `claude-code` agent with the
`claude-haiku-5-5` model. Every `WORKFLOW.*.md` carries a `claude-code:`
block and a `pi:` block; the engine reads only the block named by
`agent.kind`. To switch to `pi`, edit the workflow files: set
`agent.kind: pi` and `agent.command: pi`. To use another model, edit the
`model:` line of the block the agent kind names; the value passes
through to `claude --model` unvalidated, so only use aliases your
installed CLI accepts. Generated documents point `agent.command` at
`jig agent-exec <agent>` instead, and the supervisor rewrites the kind,
command and model when the gate picks another fleet agent (see
[Working hours and agent accounts](configuration.md#working-hours-and-agent-accounts)).
A fleet agent's or pool entry's `runtime` can be any agent kind the
engine registers (the pinned engine registers `agent-client-protocol`,
`claude-code`, `codex`, `copilot-cli`, `mock`, `opencode` and `pi`). A loop
whose agent kind the engine lacks does not start (see [Engine
capabilities](install.md#engine-capabilities)). Only `claude-code` and `pi`
have a default `command`; every other runtime needs `command` set on the
agent or pool entry. Usage-limit detection and transcript reading cover
`claude-code` and `pi` only, and no shipped adapter block exists for the
other kinds. The dashboard's token/model columns populate for either agent.
