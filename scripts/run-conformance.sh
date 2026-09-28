#!/usr/bin/env bash
#
# Build the Go conformance harness (cmd/conformance) and run the official Avo Inspector
# conformance suite against it. The language-agnostic suite runner and mock server live in the
# spec repository (avohq/spec-first-inspector-server-sdk); this script fetches it at the pinned
# commit and points its runner at the built harness.
#
# Usage:
#   ./scripts/run-conformance.sh
#
# Environment overrides:
#   SPEC_DIR       use an existing local checkout of the spec repo instead of fetching one
#   SPEC_REPO_URL  git URL of the spec repo (default: the public avohq repo)
#   SPEC_COMMIT    commit to check out when fetching (default: spec 3.0.1)
#
# Requires go and node (>= 18).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SPEC_REPO_URL="${SPEC_REPO_URL:-https://github.com/avohq/spec-first-inspector-server-sdk.git}"
SPEC_COMMIT="${SPEC_COMMIT:-7b79318f8cf1fa6de0142c698e37bb5d18e2d678}"
HARNESS="$ROOT/.bin/avo-inspector-conformance"

echo "==> Building conformance harness"
(cd "$ROOT" && go build -o "$HARNESS" ./cmd/conformance)

if [ -z "${SPEC_DIR:-}" ]; then
  SPEC_DIR="$ROOT/.spec-repo"
  echo "==> Fetching spec repo @ $SPEC_COMMIT"
  if [ ! -d "$SPEC_DIR/.git" ]; then
    git init --quiet "$SPEC_DIR"
    git -C "$SPEC_DIR" remote add origin "$SPEC_REPO_URL"
  fi
  git -C "$SPEC_DIR" fetch --quiet --depth 1 origin "$SPEC_COMMIT"
  git -C "$SPEC_DIR" -c advice.detachedHead=false checkout --quiet --force FETCH_HEAD
fi
echo "    spec @ $(git -C "$SPEC_DIR" rev-parse --short HEAD 2>/dev/null || echo "$SPEC_DIR")"

echo "==> Running conformance suite"
node "$SPEC_DIR/conformance/runner/suite-runner.mjs" --harness "$HARNESS"
