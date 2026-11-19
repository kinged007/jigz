# area:docs

## Paths

`docs/*.md` (concepts, install, usage, configuration, workflows, dashboard, customization), `README.md`, `CONTRIBUTING.md`, `ROADMAP.md`, `SECURITY.md`, `plans/`.

## Purpose

Describe what the code does now. `plans/` holds design notes; `docs/` describes current behavior. Check every command, flag, key, label, path and env var against the code (`cmd/jig`, `internal/config`) before writing it.

## Entry points

`docs/README.md` (index), `docs/concepts.md`, `docs/configuration.md`, `docs/workflows.md`.

## Conventions

- Plain, literal prose. No metaphors. No edit history ("previously", "now instead").
- User-facing text calls the engine "the engine" and does not name it.
- Environment variables are documented next to the feature they configure, with an overview in `README.md`.
- Names on disk use `jigz`. Commands, services and env vars use `jig` and `JIG_*`.
- A new page is added to `docs/README.md`. Links use relative paths.

## Pitfalls

- The shipped prompts and the label list in `.jigz/config.yaml` are described in `docs/concepts.md` and `docs/workflows.md`. Keep them in step with the code.
- The label contract line in `docs/concepts.md` is checked by readers; do not weaken it.

## Focused checks

No docs tooling exists. Check relative links with `grep -rn '](' docs README.md` and `ls`. When the change also touches Go files (for example help text), run the checks in `AGENTS.md`.
