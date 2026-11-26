# Customization

The config and prompts that `jig setup` writes are a usable default, not a
fit for every team. Adapt `.jigz/config.yaml` and the prompts in
`.jigz/workflows/prompts/` to the development process of your repositories and
organization: review rules, branch and commit conventions, test commands,
labels, budgets and working hours. The sections below cover common changes.

## Agents and models

The shipped default is the `claude-code` agent with the `claude-haiku-5-5`
model. Every `WORKFLOW.*.md` carries a `claude-code:` block and a `pi:` block;
the engine reads only the block named by `agent.kind`.

- **Switch to `pi`:** edit the workflow files and set `agent.kind: pi` and
  `agent.command: pi`.
- **Use another model:** edit the `model:` line of the block that the agent
  kind names. The value is passed to `claude --model` unvalidated, so use only
  aliases your installed CLI accepts.

Generated documents point `agent.command` at `jig agent-exec <agent>`
instead, and the supervisor rewrites the kind, command and model when the
gate picks another fleet agent. See
[Working hours and agent accounts](configuration.md#working-hours-and-agent-accounts).

A fleet agent's or pool entry's `runtime` can be any agent kind the engine
registers. The pinned engine registers `agent-client-protocol`, `claude-code`,
`codex`, `copilot-cli`, `mock`, `opencode` and `pi`. A loop whose agent kind
the engine lacks does not start; see
[Engine capabilities](install.md#engine-capabilities).

Only `claude-code` and `pi` have a default `command`. Every other runtime
needs `command` set on the agent or pool entry. Usage-limit detection and
transcript reading cover `claude-code` and `pi` only, and no shipped adapter
block exists for the other kinds. The dashboard's token and model columns are
filled for both `claude-code` and `pi`.

## Auto-merge on a clean review

By default an approved PR waits for a person to label it `agent:merge`. To
automate build, review and merge on one repo, edit that repo's
`.jigz/workflows/prompts/review.md`. In Step 4, replace the clean-review
command so that it chains the merge loop instead of stopping:

```sh
gh pr edit {{ .issue.identifier }} --repo $JIG_TRACKER_PROJECT \
  --remove-label "agent:review,in-progress" --add-label "agent:merge"
```

The merge loop watches `label:agent:merge`, so it picks the PR up on the next
poll (about 60 seconds), triages comments, merges, and ends on `agent:done`.
Blocking findings and conflicts still stop the merge and escalate with
`needs-human`.

`setup` never overwrites an existing prompt, so this edit is kept.

## Agent identity on GitHub

GitHub shows the commit author name and email. Point the git identity of the
loop at the agent, in the target repo or globally:

```sh
git config --global user.name "jig-agent"
git config --global user.email "agent@example.com"
```

`user.name` is display-only. GitHub links a commit, and its avatar, to the
GitHub account whose verified email matches the author email. One account has
one avatar, so every agent that uses your email shows your personal avatar.
Options for a distinct avatar:

- A separate bot account per agent, with its own verified email and avatar.
- No GitHub account, plus a per-email avatar on gravatar.com. GitHub falls
  back to the Gravatar for unclaimed emails.
- Publishing the agent as a GitHub App (`name[bot]`), which has its own
  avatar.

The token you export is also the identity of the agent's `gh` calls; see
[GitHub token](install.md#github-token).
