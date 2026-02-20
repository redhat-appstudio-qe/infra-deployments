#!/usr/bin/env bash
# render-diff.sh — Compute and display the kustomize render delta for
# components affected by the current branch's changes.
#
# Usage:
#   ./hack/render-diff.sh [flags]
#
# This wrapper builds the render-diff binary (using Go's build cache for
# fast no-op rebuilds) and runs it with the repository root auto-detected.
# All flags are forwarded to the binary.
#
# Examples:
#   ./hack/render-diff.sh                          # default: diff against merge-base with main
#   ./hack/render-diff.sh --base-ref origin/main   # explicit base ref
#   ./hack/render-diff.sh --color                  # force colored output
#   ./hack/render-diff.sh --open                   # open diffs in $DIFFTOOL
#   ./hack/render-diff.sh --output-dir ./diffs     # write .diff files to directory
#
# Requires: Go toolchain (https://go.dev/dl/)

set -euo pipefail

# Auto-detect repository root
REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"

# Remember the caller's working directory so relative paths (e.g. --output-dir .)
# resolve correctly even though we cd into infra-tools/ to build.
CALLER_DIR="$(pwd)"

# Check for Go toolchain
if ! command -v go &>/dev/null; then
    echo "Error: Go toolchain not found. Install Go from https://go.dev/dl/" >&2
    exit 1
fi

# Build the binary (Go's build cache makes this ~1s when source is unchanged)
cd "$REPO_ROOT/infra-tools"
go build -ldflags "-X main.version=$(git rev-parse --short HEAD)" \
    -o bin/render-diff ./cmd/render-diff

# Return to the caller's directory so relative paths work as expected
cd "$CALLER_DIR"

# Run with auto-detected repo root, forwarding all arguments
exec "$REPO_ROOT/infra-tools/bin/render-diff" --repo-root="$REPO_ROOT" "$@"
