# Documentation

Start here. Newcomer path: concepts, then install, then usage.

- [concepts.md](concepts.md) — what `jig` does, the names, how loops claim issues, the labels, the `issues` and `prs` loops that `jig setup` scaffolds. Read this first.
- [install.md](install.md) — install, the engine binary, the GitHub token.
- [usage.md](usage.md) — run, `start`, `status`, loop selection, dashboard flags, control commands. Reproduce a run from here.
- [configuration.md](configuration.md) — `.jigz/config.yaml` and the machine file: every key, defaults, validation, and examples.
- [workflows.md](workflows.md) — `WORKFLOW.*.md` parameters, generated vs hand-written documents, validation, prompts, custom loops.
- [dashboard.md](dashboard.md) — the read-only web dashboard, including the shared `--unite` view.
- [customization.md](customization.md) — agents, models, auto-merge, agent identity.

Fast paths:

- To run one labelled issue: [install.md](install.md) → [usage.md](usage.md).
- To cap spend or limit hours: [configuration.md](configuration.md) (budgets, working hours).
- To add a loop: [workflows.md](workflows.md) (custom loops), then [configuration.md](configuration.md) (its `work:` entry and labels).

`plans/` holds design notes; these `docs/` pages describe current behavior.
