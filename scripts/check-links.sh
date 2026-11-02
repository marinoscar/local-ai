#!/usr/bin/env bash
# check-links.sh: verify relative links and #anchors in every *.md file (offline).
# `--external` also fetches https:// links (weekly job). Implementation: scripts/linkcheck (Go, stdlib only).
#
# Usage: scripts/check-links.sh [--external]
# Exit:  0 ok, 1 broken links (printed as `BROKEN <file>:<line> -> <target>`), 2 error.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
command -v go >/dev/null 2>&1 || { echo "check-links: Go is required (see AGENTS.md Tech stack)" >&2; exit 2; }
cd "$root/scripts/linkcheck"
go run . --root "$root" "$@"
