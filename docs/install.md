# Install

macOS and Linux, on Intel or Apple silicon.

```sh
curl -sSL https://raw.githubusercontent.com/kinged007/jigz/main/install.sh | sh
```

That downloads a release binary to `~/.local/bin/jig` and checks
it against the release's `checksums.txt`. No Go toolchain needed.
`./install.sh --prefix DIR` installs to `DIR/bin/jig` instead,
`--version TAG` pins a release, and `--from-source` builds from a
checkout.

Already have Go?

```sh
go install github.com/kinged007/jigz/cmd/jig@latest
```

Also requires the **gh CLI** — `setup` shells out to it, and the agent
prompts use it throughout.

### The engine

`jig` is the supervisor. The work is done by the **engine**, a separate
program that polls GitHub, claims work, and runs the agents. `jig setup`
downloads the pinned engine release, verifies it against that release's
`checksums.txt`, and links it onto your `PATH`, so the engine's own
commands (`validate`, `stats`) work in any shell. A fresh install needs no
separate engine step.

The engine is a GitHub release, chosen by a repository (`owner/name`) and a
version tag (`vX.Y.Z`), defaulting to `v1.28.1`, the release the shipped
workflows were written against. `jig --version` prints the tool version
and the engine release it resolves to, and the dashboard Settings page
shows that release next to the version the engine binary reports.

The workflows need one feature an engine may not ship: the **github-pr
tracker**, which treats pull requests as work items so a review loop can
watch a pull request rather than an issue. Every supported engine
registers the **claude-code** agent adapter, and the `pi` adapter is only
needed when a workflow or agent uses `pi`. A repository you configure
should carry the tracker; the next section says what is switched off when
it does not.

#### Engine capabilities

At start, `jig` asks the resolved engine binary for its version (its
version flag) and for the tracker and agent kinds it has registered. No
engine command lists the kinds, so the second part runs the engine's
`validate` command on a temporary workflow with an unknown tracker and agent
kind and reads the `registered: [...]` lists from its errors. The result is
cached per binary path, modification time and size for the life of the
process. The supervisor, `validate`, setup validation, the dashboard and the
status document use the same result. A binary that cannot be probed has an
unknown capability set: nothing is disabled, and the dashboard says that the
features could not be detected.

| The engine | What happens |
|---|---|
| Is older than 1.27.0, the oldest the shipped workflows run on | No loop starts. `validate` fails with the reason, and the dashboard shows an alert. |
| Has no `github-pr` tracker | The PR workflow loop does not start. `validate` reports it as disabled, not as an error. The workflow file stays installed. |
| Lacks the agent kind a workflow names (`claude-code` unless you set `agent.kind`) | The loop does not start. `validate` and the dashboard show an error. There is no fallback agent. Pool entries with a runtime the engine lacks are shown as unavailable and are not picked. |

Each disabled feature has one alert on the Operations page with the reason,
for example "PR workflow is off: engine v1.29.0 has no
github-pr tracker." The Settings page lists the registered tracker and agent
kinds, and a workflow that is not started has the state `disabled` and the
reason in the status document. The `engine_version` field of the status
document is the version the engine binary reports.

The repository and version are read in this order, first match wins:

1. `JIG_ENGINE_BIN`: use this binary. No repository or version applies.
2. The environment variables `JIG_ENGINE_REPO` and `JIG_ENGINE_VERSION`.
3. The `engine:` block of `machine.yaml`.
4. The defaults.

Each of the repository and the version is resolved on its own. A release
needs one archive per platform (Linux and macOS, `amd64` and `arm64`),
named after the version, the OS and the architecture, and a
`checksums.txt`; the download is verified against it. An invalid value is
an error.

The setup command, the loops, `validate`, the dashboard and the supervisor
all resolve the engine the same way: `JIG_ENGINE_BIN`, then the engine installed
for the configured repository and version in `$XDG_DATA_HOME/jigz` (or
`~/.local/share/jigz`) or in `<prefix>/share/jigz` beside the
`jig` binary, then a verified download into the first of those. The
installed file is named after the repository and the version, so the same
version from two repositories is stored separately. If the download fails,
an engine binary beside the `jig` binary or on `PATH` is used with a
warning, and its version is not checked.

The version is fixed rather than resolved to "latest" on purpose: the
workflows are written against specific engine behaviour, and an engine that
changes under them breaks loops in ways that are hard to trace.

| Variable | Effect |
|---|---|
| `JIG_ENGINE_BIN` | use this engine binary |
| `JIG_ENGINE_REPO` | fetch the engine from this repository's releases (`owner/name`) |
| `JIG_ENGINE_VERSION` | fetch this version (`vX.Y.Z`) instead of the configured one |
| `JIG_ENGINE_URL` | fetch one archive from this URL (with `JIG_ENGINE_SHA256` to verify it) |

### GitHub token

The token is the identity of both the loop and the agent: it
authenticates GitHub API polling, label changes, clones of private
repos, and every `gh` command the agent runs (reading issues, posting
comments, opening PRs). `@me` in the assignee scope means whoever owns
this token. Use a token for the account you want the work attributed
to — a separate bot account keeps agent commits, comments, and PRs off
your personal name.

1. Create it: `gh auth login`, or generate a token at
   https://github.com/settings/tokens with the `repo` scope (classic
   token is simplest; a fine-grained token needs Contents, Issues, and
   Pull requests read/write on the repos you loop over).
2. Expose it with one of (first set wins: `JIG_TOKEN`, then
   `token:` in `.jigz/config.yaml`, then `GITHUB_TOKEN`, then
   `GH_TOKEN`):
   ```sh
   export GITHUB_TOKEN="ghp_..."     # or GH_TOKEN; or JIG_TOKEN to override just the loop
   ```
   The `gh` CLI reads `GITHUB_TOKEN`/`GH_TOKEN` itself, so exporting
   one of those covers both the loop and the agent's `gh` calls. Never
   commit the token — `token:` in config is for local-only setups.
3. Verify: `gh auth status` should show the right account before you
   run `setup` (which syncs labels) or the loop.
