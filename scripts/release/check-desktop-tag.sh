#!/usr/bin/env bash
# check-desktop-tag.sh: step 1 of the desktop release (docs/specs/desktop.md section 4.11). The tag must be exactly
# `desktop-v` + the version in apps/desktop/package.json, and that version must be a semver.
#
#   check-desktop-tag.sh --tag desktop-v<semver> [--package <package.json>]
#   check-desktop-tag.sh --dry-run               [--package <package.json>]   (derives the tag from the package version)
#
# On success prints, one per line, `tag=...`, `version=...` and `prerelease=...` (`--prerelease` when the version has a
# pre-release part such as -rc.1, empty otherwise), ready to be appended to $GITHUB_OUTPUT. Messages go to stderr.
# --package defaults to apps/desktop/package.json at the repository root.
#
# Exit: 0 ok, 1 the tag is not well formed or does not match package.json, 2 usage or environment error.
set -euo pipefail

usage() { sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
fail() { echo "check-desktop-tag: $1" >&2; exit "${2:-1}"; }

tag=""; dry=0; pkg=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 || usage ;;
    --dry-run) dry=1; shift ;;
    --package) pkg="${2:-}"; shift 2 || usage ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[[ ( -n "$tag" && $dry -eq 0 ) || ( -z "$tag" && $dry -eq 1 ) ]] || usage
[[ -n "$pkg" ]] || pkg="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/apps/desktop/package.json"
[[ -f "$pkg" ]] || fail "$pkg not found" 2

# The top-level "version" key (two-space indent); nested objects are indented deeper.
version="$(sed -n 's/^  "version": "\([^"]*\)",\{0,1\}[[:space:]]*$/\1/p' "$pkg" | head -n 1)"
[[ -n "$version" ]] || fail "no top-level \"version\" in $pkg" 2

semver='^([0-9]+\.[0-9]+\.[0-9]+)(-[0-9A-Za-z.-]+)?$'
[[ "$version" =~ $semver ]] || fail "the version '$version' in $pkg is not a semver"
pre="${BASH_REMATCH[2]}"

if [[ $dry -eq 1 ]]; then
  tag="desktop-v$version"
else
  [[ "$tag" == desktop-v* ]] || fail "tag '$tag' does not start with desktop-v"
  [[ "${tag#desktop-v}" == "$version" ]] \
    || fail "tag '$tag' does not match apps/desktop/package.json: the tag says '${tag#desktop-v}', package.json says '$version'. Fix the version in a PR and tag again with a new tag (a pushed tag is never moved)."
fi

echo "tag=$tag"
echo "version=$version"
if [[ -n "$pre" ]]; then echo "prerelease=--prerelease"; else echo "prerelease="; fi
