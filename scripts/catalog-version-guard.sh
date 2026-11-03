#!/usr/bin/env bash
# catalog-version-guard.sh: the version rule of the catalog release workflow (CAT-04).
# Spec: docs/specs/catalog.md section 1 (release tags); cloud-app.md section 4.6.
#
#   catalog-version-guard.sh [--repo DIR] [--file PATH | --version V]
#
# Reads the new CATALOG_VERSION (default: catalog/CATALOG_VERSION under the repository, or --version V)
# and checks it against the existing catalog-* tags of the repository (git tag --list, so the caller
# must have fetched them: actions/checkout with fetch-depth 0). It fails when
#   - the version is not one line YYYY.MM.DD-N (a real calendar date, N >= 1, no leading zero), or
#   - the tag catalog-<V> already exists (a tag is never overwritten), or
#   - V is not greater than the highest existing catalog-<YYYY.MM.DD-N> tag. Year, month, day and N are
#     compared as numbers, so 2026.11.02-10 is greater than 2026.11.02-9. The moving tag catalog-stable
#     and any tag that is not catalog-<valid version> are ignored (the latter with a warning).
#
# Exit: 0 the version may be tagged, 1 the guard refused it, 2 usage or environment error.
# Output: "OK ..." on stdout; every problem as "ERROR: ..." on stderr (and as a ::error:: workflow
# annotation when GITHUB_ACTIONS=true). Pure bash and git; never touches the network.
set -euo pipefail
export LC_ALL=C

usage() { sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }

repo="" file="" version="" have_version=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)    repo="${2:-}"; [[ -n "$repo" ]] || usage; shift 2 ;;
    --file)    file="${2:-}"; [[ -n "$file" ]] || usage; shift 2 ;;
    --version) version="${2-}"; have_version=1; shift 2 ;;
    *) usage ;;
  esac
done
[[ -z "$file" || $have_version -eq 0 ]] || { echo "catalog-version-guard: use --file or --version, not both" >&2; exit 2; }

if [[ -z "$repo" ]]; then
  repo="$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "catalog-version-guard: not inside a git repository (use --repo)" >&2; exit 2; }
fi
git -C "$repo" rev-parse --git-dir >/dev/null 2>&1 || { echo "catalog-version-guard: $repo is not a git repository" >&2; exit 2; }

fail() { # fail <message>
  echo "ERROR: $1" >&2
  if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then echo "::error title=catalog version guard::$1"; fi
  exit 1
}

if [[ $have_version -eq 0 ]]; then
  [[ -n "$file" ]] || file="$repo/catalog/CATALOG_VERSION"
  [[ -f "$file" && -r "$file" ]] || { echo "catalog-version-guard: cannot read $file" >&2; exit 2; }
  # exactly one line; one trailing LF or CRLF is allowed (the linter allows it too)
  content="$(cat "$file"; printf x)"; content="${content%x}"
  content="${content%$'\n'}"; content="${content%$'\r'}"
  version="$content"
fi

version_re='^([0-9]{4})\.([0-9]{2})\.([0-9]{2})-([1-9][0-9]{0,8})$'

# valid_key <version>: prints a fixed-width sortable key; returns 1 when the version is not valid.
valid_key() {
  local v="$1" y m d n dim
  [[ "$v" =~ $version_re ]] || return 1
  y=$((10#${BASH_REMATCH[1]})); m=$((10#${BASH_REMATCH[2]})); d=$((10#${BASH_REMATCH[3]})); n=$((10#${BASH_REMATCH[4]}))
  ((m >= 1 && m <= 12)) || return 1
  case $m in
    4|6|9|11) dim=30 ;;
    2) if ((y % 4 == 0 && (y % 100 != 0 || y % 400 == 0))); then dim=29; else dim=28; fi ;;
    *) dim=31 ;;
  esac
  ((d >= 1 && d <= dim)) || return 1
  printf '%04d%02d%02d%09d' "$y" "$m" "$d" "$n"
}

shown="${version//[$'\r\n']/ }"
new_key="$(valid_key "$version")" || fail "CATALOG_VERSION must be one line YYYY.MM.DD-N (a real date, N >= 1, no leading zero), got '$shown'"

tags="$(git -C "$repo" tag --list 'catalog-*')"
latest_tag="" latest_key=""
while IFS= read -r tag; do
  [[ -n "$tag" && "$tag" != "catalog-stable" ]] || continue
  suffix="${tag#catalog-}"
  if ! key="$(valid_key "$suffix")"; then
    echo "WARN: ignoring tag '$tag': not catalog-<YYYY.MM.DD-N>" >&2
    continue
  fi
  if [[ "$suffix" == "$version" ]]; then
    fail "tag catalog-$version already exists; a catalog tag is never overwritten. Bump catalog/CATALOG_VERSION to a version greater than the latest tag."
  fi
  if [[ -z "$latest_key" || "$key" > "$latest_key" ]]; then latest_key="$key"; latest_tag="$tag"; fi
done <<<"$tags"

if [[ -n "$latest_key" && ! "$new_key" > "$latest_key" ]]; then
  fail "CATALOG_VERSION $version is not greater than the latest tag $latest_tag. Bump the date or the -N in catalog/CATALOG_VERSION."
fi

echo "OK catalog-$version may be tagged (latest existing: ${latest_tag:-none})"
