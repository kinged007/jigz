# Production readiness

Making `sortie-loop` installable and usable by people outside this
machine: legal clarity, a one-command install on macOS and Linux, signed
releases, and a `go install` path.

## Decisions taken

- **License: Apache-2.0, copied verbatim from `sortie`.** The tool drives
  the engine and ships alongside it, so it carries the same terms.
- **The engine is the `kinged007/sortie` fork, not upstream
  `sortie-ai/sortie`.** The shipped workflows need two pieces upstream
  does not ship: the **pi agent adapter** (the agents run in a terminal)
  and the **github-pr tracker** (review and merge loops watch pull
  requests, not issues). `SORTIE_ENGINE_REPO` points the installer at a
  different build of the same engine for anyone who needs one; whatever
  it points at must still carry both adapters.
- **The engine version is pinned**, not resolved to "latest". The
  workflows are written against specific engine behaviour, so an
  unpinned engine would change under them. `SORTIE_ENGINE_VERSION`
  overrides the pin.
- **The fork's upstream divergence is tracked, not fixed here.** The fork
  sits behind `sortie-ai/sortie`; rebasing it is separate work.

## Support matrix

| Target | `sortie-loop` binary | Engine release asset |
|---|---|---|
| linux/amd64 | builds | yes |
| linux/arm64 | builds | yes |
| darwin/amd64 | builds | yes |
| darwin/arm64 | builds | yes |
| windows | not built | released as `.zip`, no installer path |

All four targets compile with `CGO_ENABLED=0`; the SQLite driver is pure
Go, so no cross-compilation toolchain is needed.

## Phases

### 1. Engine releases — done

The fork inherited upstream's `.goreleaser.yaml`, including two publish
targets that point at the upstream project:

```yaml
release:
  github:
    owner: sortie-ai      # a fork tag would publish to upstream
homebrew_casks:
  - repository:
      owner: sortie-ai    # a cask push to upstream's tap
      name: homebrew-tap
```

Both were repointed at `kinged007`, and the release workflow now cuts a
full asset set: `sortie_<version>_<os>_<arch>.tar.gz` for the four
targets, plus `checksums.txt` (sha256) and SBOMs. Earlier releases were
cut by hand and carried a single `sortie-linux-amd64` asset with no
checksums.

### 2. Engine resolution in the binary — done

The only code that fetched the engine lived in `install.sh`, so anyone
arriving through `go install` — which never runs that script — hit a dead
end telling them to rerun a script they did not have. Fetching moved
into `cmd/sortie-loop/engine.go`.

Resolution order, first hit wins:

1. `SORTIE_BIN` — absolute path to an engine binary
2. `sortie` beside the `sortie-loop` binary
3. `sortie` on `PATH`
4. the cache, for the pinned `engineTag`
5. download

`SORTIE_ENGINE_URL` with `SORTIE_ENGINE_SHA256` bypasses 4 and 5 with a
direct archive download.

The engine lands at `$XDG_DATA_HOME/sortie-loop/sortie-<version>`
(`~/.local/share/sortie-loop` when unset — the directory `install.sh`
already populates, so either installer reuses the other's download). A
cached file is trusted; it is verified when written, and the directory is
the user's own. Downloads stream to a temp file, are hashed while
writing, and are moved into place only after the digest matches, so an
interrupted or tampered download never becomes the cached engine. Only
the `sortie` entry is extracted, so a crafted archive cannot write
outside the destination.

`engineTag` holds the release tag, and the version in asset and cache
names is that tag without its leading `v` — the form GoReleaser writes
into asset names. `--dump-version` reports it, which is how `install.sh`
names the shared file, so both installers agree on one cache filename.
This changed the local filename from `sortie-1.24.0+pi.github-pr.1` to
`sortie-1.24.0-pi.github-pr.1`; the previous file is left behind as an
unused cache entry.

`SORTIE_ENGINE_URL` without `SORTIE_ENGINE_SHA256` downloads an
unverified binary and says so on stderr. Windows is unsupported: the
release ships a `.zip` and the installer reads `tar.gz`.

### 3. The engine is installed onto PATH

**A working install leaves a bare `sortie` on the user's PATH.** The
engine is linked into a directory already on `PATH` under its plain
name, so `sortie validate` and `sortie stats` work in any new shell
without the version in the path and without an alias.

- The link goes in the directory holding the `sortie-loop` binary when
  that directory is on `PATH` — the `go install` layout, where one `PATH`
  entry covers both tools — and otherwise in `~/.local/bin`.
- The link is a symlink to the versioned file, refreshed whenever the
  pinned version changes, replaced atomically so a concurrent shell never
  sees it missing.
- An existing `sortie` that `sortie-loop` did not install is left alone
  and reported; the engine still works by absolute path.
- When the chosen directory is not on `PATH`, the installer says which
  directory to add rather than failing.
- `resolveSortieBin` finds the linked engine on the next run, so the
  install is self-reinforcing.

`sortie-loop setup` installs the engine, so this works for people who
only ever run `go install`.

The symlink lives in one function that takes the target directory, so
the test suite exercises the real code against a temp directory instead
of the developer's home.

### 4. `install.sh` is a downloader — done

It compiled from source, which is what made it fragile: piped through
`curl | sh`, `$0` is `sh`, so the script inferred its source directory
from the current working directory. Run from inside any Go module,
`[ -f "$SRC/go.mod" ]` succeeded and the script built *that* project's
`./cmd/sortie-loop` instead of fetching `sortie-loop`.

It now downloads the release binary and verifies it against
`checksums.txt`, taking the source directory from `$0` only when `$0`
carries a directory. The Go toolchain is not needed to install, and
`--from-source` is the explicit opt-in for building from a checkout —
refusing outright when piped, rather than guessing. A missing or
incomplete `checksums.txt` is an error, not a skip, and the install
refuses when neither `sha256sum` nor `shasum` is available rather than
skipping verification.

The engine block is gone: `sortie-loop setup` installs the engine. The
`--sortie-bin` flag went with it, since `SORTIE_BIN` covers the same
need.

### 5. Argument parsing — done

`sortie-loop setup <dir> --repo owner/name` ignored `<dir>` and operated
on the current working directory, writing `.sortie/` and rewriting
`.gitignore` in the wrong repository. The cause was the last-non-flag-arg
rule: the `setup` branch skipped `--repo` but not the value after it, so
`owner/name` became the directory, and `setup.go` hid the symptom with
`if dir == flag { dir = "." }`. Putting the directory after `--repo`
worked, which is the form the help text showed, which is why it survived.

One `parseArgs` now serves every invocation. A flag that takes a value
consumes it whether it is attached with `=` or given as the next
argument, so a slug can never be read as a directory. The directory is
the last bare argument and may sit anywhere, so both orders work. The
two hand-rolled loops are gone, and with them the `os.Args[i]` index
confusion in the run path that worked only by accident. `--version` was
added at the same time, and the duplicated usage line removed.

### 6. Process lifecycle — done

A goroutine per child called `syscall.Kill(0, syscall.SIGTERM)` followed
by `os.Exit(1)` when any loop child exited. Three consequences:

- `SIGTERM` to process group 0 reached the user's shell and every sibling
  process, not just the loops.
- `os.Exit` skipped the deferred cleanup, so the registry entry survived
  pointing a `--unite` dashboard at a dead run, and the surviving
  children were never reaped.
- A crash during startup — after some loops were already running and
  before others — orphaned the running ones, because the `fatal` call
  that reported it also called `os.Exit`.

`fatal` now reports through a panic that `run` recovers, so the deferred
cleanup runs on every error path. `main` is `os.Exit(run())`. A child
exiting ends the run the same way a signal does: the children are
signalled directly rather than by process group, cleanup runs, and the
exit code is 1. No automatic restart of a crashed loop until someone
hits that.

### 7. CI and releases — done

- `ci.yml`: `gofmt -l`, `go vet`, `go test`, and a four-target build
  matrix. The engine is deliberately **not** installed in CI —
  `TestSortieValidate` runs whatever `sortie` it finds, so a stock engine
  would fail it for the wrong reason. It prefers the fork and skips when
  no engine is present, which is the correct CI state.
- `release.yml` and `.goreleaser.yaml` producing
  `sortie-loop_<version>_<os>_<arch>.tar.gz` for linux/darwin ×
  amd64/arm64, plus `checksums.txt` and SBOMs — the layout `install.sh`
  expects. The config carries no publish target pointing outside this
  repository, which is the trap the engine fork inherited.
- `sortie-loop` had no version of its own: `version.go` held only the
  engine pin, and `--dump-version` printed it. A `version` variable is
  now stamped by `-X main.version`, `--version` reports the tool and the
  engine it drives, and `--dump-version` keeps reporting the engine pin
  because `install.sh` uses it.

### 8. Public docs

- `LICENSE` (Apache-2.0), `SECURITY.md`, `CONTRIBUTING.md`. The
  security document matters most: this tool runs autonomous agents with a
  write-scoped token, and the token scopes, what the agent can reach, and
  what a fork means for trust all belong in writing.
- README install section rewritten around `curl | sh` and `go install`,
  with a platform table and the fork rationale.
- **Known limitations** section. The `github-pr` adapter parses `label:`,
  `assignee:` and `-label:` but not `milestone:`, so milestone scoping
  does not filter PR loops. This is currently filed under Roadmap; it is
  a correctness gap users hit on day one.
- The `go >= 1.24` claim in `install.sh` and the README is unreachable.
  `golang.org/x/sys v0.48.0` requires go 1.26.0 and
  `modernc.org/sqlite v1.58.0` requires 1.25.0, so the floor is 1.26.0
  with current pins. Document the real floor; optionally pin `x/sys` down
  to reach 1.25.

## Smaller fixes

- `normalizeSlug` (`repo.go`) was a byte-identical copy of
  `normalizeRepo` (`config.go`). The config one is exported as
  `NormalizeRepo` and the copy is gone.
- `bin.go` carried two doc comments merged into one block, leaving
  `ensureSortieLink` with no doc of its own. Both functions went with
  the rewrite.
- `setup` overwrote the colour and description of 13 GitHub labels
  without asking, including labels a team may already use. It now reads
  the current label and confirms before changing one, reusing the same
  prompt as an edited workflow file.
- No token check at startup. An unresolved token became
  `SORTIE_TRACKER_API_KEY=` in the child's environment and surfaced as a
  confusing failure inside the engine. `Config.CheckToken` now fails
  before anything starts, naming all four sources.
- `openRO` built a SQLite DSN by concatenation, so a repository path
  containing `?`, `#` or `%` misparsed and the loop's own history read
  as empty. The DSN is now a `url.URL`.
- `discoverWorkflows` calls `syncWorkflows` on every start, so merely
  running the tool writes files into the working tree. This stays: it is
  what makes a fresh checkout work with no setup, and it never
  overwrites an edited file.
- `sortieBin()` in the test helper looked for `sortie-fork` only, so
  `TestSortieValidate` skipped on any normally installed machine. It now
  also checks `PATH`, and the test runs and passes.

## Still open

- **The fork diverges and nothing re-syncs it.** `kinged007/sortie` is 18
  commits ahead of `sortie-ai/sortie` main and 0 behind — a superset, not
  a stale fork — but no automation merges upstream, so it will drift.
  v1.26.0 also closed the `milestone:` gap: the github-pr tracker now
  forwards `query_filter` to the search API like the issue path does,
  rather than parsing three clauses client-side.
- **The fork had no installable release** — resolved by fork v1.26.0,
  a GoReleaser release with `checksums.txt` and all four platforms, and
  identical to fork `main`. The pin is now v1.26.0 and the default path
  installs and runs it. `engineReleaseHint` stays: a bare 404 is
  unhelpful, and the fork could regress the same way.
- **Repository metadata.** Description, homepage and topics are empty,
  and the repository is still private. Metadata is a `gh repo edit` away;
  going public is a decision, not a task.
- Git history exposes the author identity of 40 commits. Decide before
  the repository goes public.
- **First release.** `install.sh` needs one `sortie-loop` release tagged
  before it can install anything; until then `--from-source` is the only
  working path, and the fork's engine release must exist for the pinned
  `engineTag` to resolve.
- One uncommitted change (`cmd/sortie-loop/pm-agent.md`, +42/-4) to
  commit or discard.
