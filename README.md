# Jigz

[![CI](https://github.com/kinged007/jigz/actions/workflows/ci.yml/badge.svg)](https://github.com/kinged007/jigz/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/kinged007/jigz)](https://github.com/kinged007/jigz/releases) [![Go](https://img.shields.io/badge/Go-1.26-blue)](go.mod)

Run autonomous coding agents against your GitHub issues and pull requests.

`jig` watches a repository's issues and pull requests. For each labeled item
it starts an AI coding agent that reads the issue, writes the code, and moves
the work forward: planning, implementing, reviewing and merging. You manage
the backlog by adding labels and assigning issues. GitHub is the only
supported tracker.

![The Jigz dashboard: fleet state, token budgets and usage](docs/images/dashboard-operations.png)

## Features

- **Your backlog drives the agents.** Label an issue and an agent works on it: plan, build, review and merge, without you starting each step. You review the results instead of running the tools.
- **Workflows are yours to define.** Jigz ships with a simple plan-build-review workflow as a starting point. It is meant to be customized fully to your own process.
- **Many triggers, many rules.** Jigz is triggered by GitHub labels. You can define as many triggers and workflow rules (loops) as you want.
- **A different agent per workflow.** Each workflow can use its own agent CLI and model; see [Supported agents](#supported-agents).
- **Spend and schedule controls.** Per-loop token budgets and working hours. Dollar budgets are planned.
- **One dashboard for several repos.** Run `jig --unite` in each repo to join every Jigz instance on the same machine into one dashboard.
- **Safe by default.** Only items assigned to the token owner are picked up, and `needs-human` parks an item until a person removes it.
- **Fleet support.** Agent accounts and pools with usage-limit failover.
- **Simple to run.** `jig setup`, then `jig` runs every loop in the foreground (or `jig start` in the background) with a read-only web dashboard.

## Quickstart

```sh
# once per machine (macOS/Linux): installs jig to ~/.local/bin/jig
curl -sSL https://raw.githubusercontent.com/kinged007/jigz/main/install.sh | sh
export GITHUB_TOKEN="ghp_..."   # or GH_TOKEN; needs Contents, Issues, Pull requests read/write

cd your-repo
jig setup     # writes .jigz/, generates the workflows, creates the labels in config.yaml, installs the engine
              # (jig setup --skip-build leaves out the workflows and labels)
jig           # run every loop in the foreground (Ctrl-C stops all)
```

Then, on GitHub: assign an issue to yourself and add the `agent:build` label.
The loop polls roughly every 60 seconds, claims the issue, works on a
`auto/<issue-number>` branch, and opens a PR labeled `agent:review`.
See [docs/usage.md](docs/usage.md) and
[docs/concepts.md](docs/concepts.md).

## Customize it for your team

`jig setup` writes a basic config and prompts that work out of the box. They
are a starting point. Adapt `.jigz/config.yaml` and the prompts in
`.jigz/workflows/prompts/` to the development process of your repositories and
organization: review rules, branch and commit conventions, test commands,
labels, budgets and working hours. `jig setup` never overwrites a config or
prompt file you have edited. See
[docs/configuration.md](docs/configuration.md),
[docs/workflows.md](docs/workflows.md#prompts) and
[docs/customization.md](docs/customization.md).

## Supported agents

Each loop runs one agent CLI. Claude Code is the default; switch with the
`runtime` of an agent in `.jigz/config.yaml`
([Agent runtimes](docs/configuration.md#agent-runtimes)).

| Agent CLI | Runtime | Default command |
|---|---|---|
| [Claude Code](https://code.claude.com/docs/en/cli) | `claude-code` | `claude` |
| [pi](https://github.com/badlogic/pi-mono) | `pi` | `pi` |
| Codex | `codex` | `codex app-server` |
| OpenCode | `opencode` | `opencode` |
| GitHub Copilot CLI | `copilot-cli` | `copilot` |

Dashboard transcripts and stdout-based usage-limit detection cover Claude Code
and pi; other agents are detected from stderr. See
[Customizing agents](docs/customization.md#agents-and-models).

## Requirements

- macOS or Linux, on Intel or Apple silicon. Windows is not supported.
- A GitHub token with `repo` scope (classic) or Contents, Issues, Pull requests read/write (fine-grained); see [the token setup](docs/install.md#github-token).
- The [gh CLI](https://cli.github.com/) on `PATH`.
- A coding agent CLI installed and logged in; Claude Code (`claude`) is the default. See [Supported agents](#supported-agents).
- Go 1.26.0 or newer only to build from source.

## How it works

Jigz polls GitHub for issues and pull requests that carry a trigger label.
The poll interval defaults to 60 seconds and can be increased
(`polling_interval_ms`, see [docs/configuration.md](docs/configuration.md)).
The workflow below is the default one that `jig setup` writes. It is a basic
starting point: the triggers, rules, prompts, agents and models can all be
changed.

```mermaid
flowchart TD
    A[Issue labeled agent:plan-build or agent:plan] --> B[plan: agent writes a plan comment]
    B -->|agent:plan| C[Person reviews the plan and adds agent:build]
    B -->|agent:plan-build| D
    C --> D[build: agent implements on auto/N branch and opens a PR labeled agent:review]
    D --> E[review: agent reviews the PR]
    E -->|findings| F[review-fix: agent applies feedback, PR labeled agent:build]
    F --> E
    E -->|clean| G[agent:done: waits for a person]
    G --> H[Person adds agent:merge]
    H --> I[merge: agent merges the PR]
```

### When an agent starts work

An item is picked up only when all of these hold:

1. It is assigned to the same GitHub user as the token that Jigz runs with.
2. It has a trigger label, for example `agent:build`.
3. It has no blocking label. By default `needs-human` blocks; the query of
   each loop is set in `query_filter` in `.jigz/config.yaml`.

When an agent claims an item it adds `in-progress`, works on an
`auto/<issue-number>` branch, and routes its output through labels. Because
the trigger stays on the item for the whole run, an interrupted loop finds it
again on the next poll. Details: [docs/concepts.md](docs/concepts.md).

## Dashboard

Every run serves a read-only web dashboard at `http://127.0.0.1:7677`. It shows
the state of each loop, token use against budgets, and the history of every
run.

![Runs per day and per-loop token budgets on the dashboard](docs/images/dashboard-runs.png)

See [docs/dashboard.md](docs/dashboard.md).

## Documentation

- [docs/concepts.md](docs/concepts.md) — labels, loops, and how agents pick up issues
- [docs/install.md](docs/install.md) — install, [the engine](docs/install.md#the-engine), [GitHub token](docs/install.md#github-token)
- [docs/usage.md](docs/usage.md) — running `jig`, `jig start`, status, logs, dashboard flags
- [docs/configuration.md](docs/configuration.md) — `.jigz/config.yaml` reference, budgets, hours, [questions](docs/configuration.md#questions), cleanup, sleep, machine file
- [docs/workflows.md](docs/workflows.md) — `WORKFLOW.*.md` parameters, generation, validation, prompts
- [docs/dashboard.md](docs/dashboard.md) — the read-only web dashboard, including `--unite`
- [docs/customization.md](docs/customization.md) — agents, models, auto-merge, agent identity

## Roadmap

Planned work is listed in [ROADMAP.md](ROADMAP.md). Where the product is heading is in [VISION.md](VISION.md).

## Contributing

Bug fixes and features go through GitHub issues and pull requests. Build,
test and commit instructions are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Support

- Questions about behavior: open a GitHub issue with the `jig` version (`jig --version`), the loop logs (`.jigz/logs/<workflow>.log`), and the workflow file involved.
- Security issues: do not open a public issue; see [SECURITY.md](SECURITY.md).

## License

Apache-2.0; see [LICENSE](LICENSE).
