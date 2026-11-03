#!/usr/bin/env bash
# catalog-tag.sh: create the catalog release tags (CAT-04). The catalog release workflow is the ONLY
# tagger of catalog-* tags (docs/architecture/overview.md section 3.8). Run catalog-version-guard.sh first.
#
#   catalog-tag.sh [--repo DIR] [--remote NAME] [--sha COMMIT] [--dry-run] <CATALOG_VERSION>
#
# Creates the annotated tag catalog-<V> at COMMIT (default HEAD), refusing to touch one that already
# exists, moves the annotated tag catalog-stable to the same commit, and pushes both to the remote
# (default origin) in ONE atomic push: either both refs change or neither does. catalog-stable is the
# only ref this script ever force-pushes. --dry-run creates the tags locally and pushes nothing.
# Needs git user.name and user.email (the workflow sets the Actions bot).
#
# Exit: 0 done, 1 refused or the push failed (nothing on the remote changed), 2 usage error.
set -euo pipefail
export LC_ALL=C

usage() { sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }

repo="" remote="origin" sha="HEAD" dry=0 version=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)    repo="${2:-}"; [[ -n "$repo" ]] || usage; shift 2 ;;
    --remote)  remote="${2:-}"; [[ -n "$remote" ]] || usage; shift 2 ;;
    --sha)     sha="${2:-}"; [[ -n "$sha" ]] || usage; shift 2 ;;
    --dry-run) dry=1; shift ;;
    -*) usage ;;
    *) [[ -z "$version" ]] || usage; version="$1"; shift ;;
  esac
done
[[ -n "$version" ]] || usage
[[ "$version" =~ ^[0-9]{4}\.[0-9]{2}\.[0-9]{2}-[1-9][0-9]{0,8}$ ]] || { echo "ERROR: '$version' is not YYYY.MM.DD-N" >&2; exit 2; }

if [[ -z "$repo" ]]; then repo="$(git rev-parse --show-toplevel)"; fi
g() { git -C "$repo" "$@"; }
tag="catalog-$version"

commit="$(g rev-parse --verify --quiet "$sha^{commit}")" || { echo "ERROR: $sha is not a commit" >&2; exit 2; }
if g rev-parse --verify --quiet "refs/tags/$tag" >/dev/null; then
  echo "ERROR: tag $tag already exists; nothing was changed" >&2
  exit 1
fi

g tag -a -m "Catalog $version" "$tag" "$commit"
g tag -f -a -m "Catalog $version (moving tag: the newest catalog release)" catalog-stable "$commit"
echo "tagged $tag and catalog-stable at $commit"

if [[ $dry -eq 1 ]]; then
  echo "dry run: nothing pushed"
  exit 0
fi
if ! g push --atomic "$remote" "refs/tags/$tag:refs/tags/$tag" "+refs/tags/catalog-stable:refs/tags/catalog-stable"; then
  echo "ERROR: the push to $remote failed; no remote ref changed (git push --atomic)" >&2
  exit 1
fi
echo "pushed $tag and moved catalog-stable on $remote"
