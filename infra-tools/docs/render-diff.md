# render-diff

Computes the kustomize render delta for components affected by your
branch's changes. Shows exactly what will change in each environment
before merging.

## Building

```bash
cd infra-tools
make build
```

The binary is placed at `infra-tools/bin/render-diff`.

## Quick start

From anywhere in the repo:

```bash
./infra-tools/bin/render-diff
```

This auto-detects the repo root, computes the merge-base with main,
and prints colored unified diffs for all affected components.

## Flag reference

### Repository and ref selection

| Flag | Default | Description |
|------|---------|-------------|
| `--repo-root` | auto-detect | Path to the repository root. When omitted, detected via `git rev-parse --show-toplevel` from the current directory. Useful in CI where the checkout path is known. |
| `--base-ref` | merge-base with main | Git ref to compare against (branch, tag, or commit SHA). By default, computes `git merge-base HEAD main` so the diff reflects only your branch's changes. Use an explicit ref when comparing against a release branch or a specific commit. |
| `--overlays-dir` | `argo-cd-apps/overlays` | Path to the ArgoCD overlays directory, relative to repo root. Only change this if the repo uses a non-standard layout. |

### Output control

| Flag | Default | Description |
|------|---------|-------------|
| `--color` | `auto` | Color mode: `auto` (detect TTY), `always`, or `never`. Use `always` when piping to a pager that supports ANSI (e.g. `less -R`). |
| `--open` | off | Write base and head YAML into two temp directories and open them in `$DIFFTOOL` (or `git difftool --no-index --dir-diff`). Files are named after component and environment for easy identification. |
| `--output-dir` | — | Write per-component `.diff` files to this directory instead of stdout. Files are named like `components__foo__staging__staging.diff`. |
| `--output-mode` | `local` | Output format: `local` (unified diff to stdout), `ci-summary` (markdown for `GITHUB_STEP_SUMMARY`), `ci-comment` (PR comment markdown), `ci-artifact-dir` (raw `.diff` files to `--output-dir`). |
| `--log-file` | — | Write DEBUG-level logs to this file. INFO-level messages always go to stderr. |
| `--version` | — | Print version and exit. |

### CI-specific flags (used with `--output-mode ci-comment`)

| Flag | Default | Description |
|------|---------|-------------|
| `--pr-number` | — | PR number to post the comment on. |
| `--github-repo` | — | Repository in `owner/repo` format. |
| `--github-token` | `$GITHUB_TOKEN` | GitHub token for API access. |
| `--dry-run` | off | Print the comment markdown to stdout instead of posting to GitHub. |

## Local usage

### Colored diff to stdout (default)

```bash
./bin/render-diff
./bin/render-diff --color=always    # force color (e.g. when piping to less -R)
./bin/render-diff --color=never     # plain text
```

### Pipe to a diff viewer

```bash
./bin/render-diff | delta
./bin/render-diff | diffnav
```

### Open in a GUI diff tool (folder comparison)

```bash
DIFFTOOL=meld ./bin/render-diff --open
```

Creates two temp directories (base/ and head/) with YAML files named
after each component, then opens the diff tool for side-by-side folder
comparison. Supports any tool that accepts two directory arguments
(meld, Beyond Compare, kdiff3, etc.).

Without `$DIFFTOOL`, falls back to `git difftool --no-index --dir-diff`.

### Write .diff files to a directory

```bash
./bin/render-diff --output-dir ./my-diffs
ls ./my-diffs/
delta < ./my-diffs/components__foo__staging__staging.diff
```

### Comparing against a specific ref

```bash
./bin/render-diff --base-ref origin/release-1.0
./bin/render-diff --base-ref abc1234
```

By default, render-diff computes `git merge-base HEAD main` so the diff
only reflects your branch's changes — not everything that happened on
main since you branched. Use `--base-ref` to compare against a different
branch or commit, for example when working against a release branch
instead of main.

### Explicit repo root

```bash
./bin/render-diff --repo-root /path/to/infra-deployments
```

Normally auto-detected. Specify explicitly when running from outside
the repo tree or in CI where the checkout path is known.

## CI output modes

These are used by the GitHub Actions workflow but can be previewed locally.

### Job summary (markdown with collapsible diffs)

```bash
./bin/render-diff --output-mode ci-summary
```

Produces markdown with `<details>` blocks per component, suitable for
writing to `$GITHUB_STEP_SUMMARY`. Diffs over 50KB per component are
truncated with a pointer to the artifact.

### PR comment (summary table)

```bash
./bin/render-diff --output-mode ci-comment
./bin/render-diff --output-mode ci-comment --dry-run   # preview without posting
```

Generates a markdown table with component, environment, and +/- line
counts. When `--pr-number`, `--github-repo`, and `--github-token` are
provided, posts or updates the comment on the PR (using an HTML comment
marker for idempotent updates). Otherwise prints to stdout.

### Artifact directory (raw .diff files)

```bash
./bin/render-diff --output-mode ci-artifact-dir --output-dir /tmp/artifacts
```

Writes one `.diff` file per component/environment pair. These are
uploaded as GitHub Actions artifacts for download.

## Debug logging

```bash
./bin/render-diff --log-file /tmp/debug.log
cat /tmp/debug.log
```

Writes DEBUG-level messages (component matching details, build timings)
to the file while showing only INFO on stderr. Useful for diagnosing
why a component was or wasn't detected.

## Viewing .diff files

```bash
delta < file.diff          # syntax-highlighted
diffnav < file.diff        # interactive TUI with file tree
bat file.diff              # highlighted with bat
less file.diff             # plain viewer
```
