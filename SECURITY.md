# Security

## What this tool does

Jigz runs autonomous coding agents against a GitHub repository.
Each loop watches for issues and pull requests carrying a trigger label,
clones the repository into a scratch workspace, and hands the item to a
coding agent. The agent reads files, edits code, runs commands, commits,
pushes branches, opens pull requests, and moves labels.

**The agent is a program that executes with your credentials.** Treat
enabling a loop as equivalent to giving a person shell access to your
machine and write access to your repository, because in effect that is
what it does.

## The trust model

Three things run on your machine, and you are trusting all of them.

1. **Jigz** (`jig`) — this tool. Sets up config, labels and workflows,
   then supervises one engine process per loop and serves a local
   dashboard.
2. **The engine** — a separate program. It polls GitHub, claims work,
   clones the repository, and hands each item to an agent. `jig` pins a
   specific release and verifies the download against that release's
   `checksums.txt`.
3. **The agent** — a coding agent such as [pi](https://github.com/badlogic/pi-mono).
   This is the component that reads your files and runs your commands.

The shipped workflows need two pieces the engine does not always ship:
the **pi agent adapter** and the **github-pr tracker** (which treats pull
requests as work items). The default engine release carries both. The
`engine:` block of `machine.yaml` and `JIG_ENGINE_REPO` select a
different release; whatever they point at must still carry both adapters.

## Token scope

One token is the identity of both the loop and the agent. It
authenticates GitHub API polling, label changes, clones of private
repositories, and every `gh` command the agent runs.

A loop that can read an issue can act on it, and a token that can push
to `main` means a misbehaving or confused agent can push to `main`.
Prefer the narrowest scope that works:

- **Fine-grained tokens** over classic ones. Grant access only to the
  repositories you loop over, with **Contents: read and write**, plus
  **Issues: read and write** and **Pull requests: read and write**.
  Omit everything else, and set a short expiry.
- **A separate bot account** keeps agent commits, comments and pull
  requests off your personal name, and lets you revoke access without
  affecting your own credentials.
- **Branch protection is your last line of defence.** Require review and
  require CI to pass, so a merged agent branch still cannot reach the
  default branch without a person.

The token is read from the first of `JIG_TOKEN`, `token:` in
`.sortie/config.yaml`, `GITHUB_TOKEN`, `GH_TOKEN`. It is written to
`.sortie/.env.loop` with mode `0600` and passed to engine processes in
their environment, so it is readable by your user account and appears in
`/proc/<pid>/environ`. Do not put a token in `token:` in a config file
you track in git.

## The dashboard

The dashboard has no authentication. It binds `127.0.0.1` by default, so
only processes on the same machine can reach it. `--host 0.0.0.0` listens
on all interfaces; anyone who can reach the port can then start and stop
agents and edit the configuration, and read issue titles and agent output
from GitHub. Use it locally, on a private network, or behind your own
secure proxy (an SSH tunnel, Tailscale, or a reverse proxy with
authentication).

The dashboard checks the `Host` header and refuses a state-changing
request from another origin. The engine server of each loop binds
`127.0.0.1` and is used internally by Jigz.

## Running the loops

The agent runs whatever the workflow tells it to, including the project's
own test and build commands. In practice:

- Give a loop a repository you are willing to have modified, not your
  only copy of important work.
- Start with a label and one issue. Watch what it does before queueing a
  backlog.
- Read the summary comments the agents post. `needs-human` means an
  agent stopped and wants you.
- Loops merge pull requests when labelled `agent:merge`. Nothing applies
  that label automatically; a person or the PM prompt does.

## Reporting a vulnerability

Report a security issue privately by opening a
[GitHub security advisory](https://github.com/kinged007/jigz/security/advisories/new)
on this repository rather than a public issue. Include the version, the
platform, and what an attacker would gain.
