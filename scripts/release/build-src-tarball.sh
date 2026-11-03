#!/usr/bin/env bash
# build-src-tarball.sh: build local-ai-<tag>-src.tar.gz, the agent source the rebuild kit compiles offline.
# Spec: docs/specs/rebuild-kit.md sections 3.1, 3.2 and 4.3.4 (kit-agent-source, `aictl rebuild-agent`).
#
#   build-src-tarball.sh --tag node-v<semver> --out <dir> [--ref <git-ref>] [--ui-dist <dir>]
#
# Contents: `git archive` of <ref> (default refs/tags/<tag>), then `go mod vendor` (agent/vendor/), then the
# BUILT local UI (agent/ui/dist/). Node.js and npm are not in the kit, so the UI ships built. The archive has no
# top-level folder: extract it into the build directory (rebuild-kit.md section 4.3.4).
#
# Reproducible: entries sorted bytewise, owner 0:0, every mtime = SOURCE_DATE_EPOCH (default: the commit time of
# <ref>), modes normalised (files 0644 or 0755, folders 0755), GNU tar format, `gzip -n`. The same ref and the
# same UI build give a byte-identical file.
#
# Before packaging it fails (never warns) when:
#   - the committed agent/vendor/ (or go.mod, go.sum) is not what `go mod vendor` produces now;
#   - agent/ui/package.json exists at <ref> but <ui-dist> (default agent/ui/dist) is missing, empty or has no index.html;
#   - scripts/check-redaction.sh flags anything in the assembled tree (vendor/ is skipped, as in the repo scan).
# When agent/ui does not exist yet at <ref> (the UI epic has not landed) a notice is printed and the UI part is skipped.
#
# Output: <dir>/local-ai-<tag>-src.tar.gz; the last line printed is its sha256 and name.
# Exit: 0 ok, 1 a check failed, 2 usage or environment error.
set -euo pipefail

usage() { sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "build-src-tarball: $1" >&2; exit "${2:-1}"; }
need() { command -v "$1" >/dev/null 2>&1 || die "'$1' is required" 2; }

tag=""; out=""; ref=""; ui_dist=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 || usage ;;
    --out) out="${2:-}"; shift 2 || usage ;;
    --ref) ref="${2:-}"; shift 2 || usage ;;
    --ui-dist) ui_dist="${2:-}"; shift 2 || usage ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[[ -n "$tag" && -n "$out" ]] || usage
[[ "$tag" =~ ^node-v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "tag '$tag' is not node-v<semver>" 2
need git; need go; need tar; need gzip; need sha256sum; need find; need sort
tar --version 2>/dev/null | head -1 | grep -q 'GNU tar' || die "GNU tar is required (reproducible options)" 2

root="$(git rev-parse --show-toplevel 2>/dev/null)" || die "run inside the repository" 2
cd "$root"
[[ -n "$ref" ]] || ref="refs/tags/$tag"
git rev-parse --verify --quiet "$ref^{commit}" >/dev/null || die "ref '$ref' does not exist (pass --ref for a dry run)" 2
epoch="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct "$ref")}"
[[ "$epoch" =~ ^[0-9]+$ ]] || die "SOURCE_DATE_EPOCH '$epoch' is not a number" 2
[[ -n "$ui_dist" ]] || ui_dist="$root/agent/ui/dist"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
name="local-ai-$tag-src.tar.gz"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
stage="$work/tree"; mkdir "$stage"

echo "== git archive $ref"
git archive --format=tar "$ref" | tar -xf - -C "$stage" --no-same-owner
[[ -f "$stage/agent/go.mod" ]] || die "agent/go.mod does not exist at $ref: nothing to package"

echo "== go mod vendor"
mkdir "$work/orig"
for f in go.mod go.sum; do
  if [[ -f "$stage/agent/$f" ]]; then cp "$stage/agent/$f" "$work/orig/$f"; fi
done
if [[ -d "$stage/agent/vendor" ]]; then cp -a "$stage/agent/vendor" "$work/orig/vendor"; fi
(cd "$stage/agent" && env -u GOFLAGS go mod vendor)
for f in go.mod go.sum; do
  if [[ -f "$work/orig/$f" ]] && ! cmp -s "$work/orig/$f" "$stage/agent/$f"; then
    die "agent/$f at $ref is not what 'go mod vendor' needs: run it in agent/ and commit the result"
  fi
done
if [[ -d "$work/orig/vendor" ]] && ! diff -rq "$work/orig/vendor" "$stage/agent/vendor" >/dev/null; then
  die "agent/vendor/ at $ref is stale: run 'cd agent && go mod vendor' and commit the result"
fi
if grep -qE '[[:space:]]v[0-9]+\.[0-9]+\.[0-9]+' "$stage/agent/go.mod"; then   # the module has requirements
  [[ -f "$stage/agent/vendor/modules.txt" ]] || die "go mod vendor produced no agent/vendor/modules.txt"
fi

echo "== local UI"
if [[ -f "$stage/agent/ui/package.json" ]]; then
  [[ -d "$ui_dist" && -n "$(ls -A "$ui_dist" 2>/dev/null)" ]] \
    || die "agent/ui exists at $ref but $ui_dist is missing or empty: build it first (cd agent/ui && npm ci && npm run build)"
  [[ -f "$ui_dist/index.html" ]] || die "$ui_dist has no index.html: is it a UI build?"
  rm -rf "$stage/agent/ui/dist"
  cp -a "$ui_dist" "$stage/agent/ui/dist"
  echo "  ok  agent/ui/dist ($(find "$stage/agent/ui/dist" -type f | wc -l) files)"
else
  echo "  note agent/ui does not exist at $ref (UI epic not landed): the tarball carries no UI build"
fi

echo "== redaction check on the assembled tree"
# The tree has no repository of its own, so give the checker a throwaway index kept outside the tree.
# `add -f` makes ignored files (a gitignored dist/) count too.
git init -q "$work/scan" >/dev/null
export GIT_DIR="$work/scan/.git" GIT_WORK_TREE="$stage"
(cd "$stage" && git add -f -A >/dev/null 2>&1 && INVENTORY_FILE="$root/inventory.local.yaml" "$root/scripts/check-redaction.sh") \
  || die "redaction check failed on the assembled source tree; nothing was packaged"
unset GIT_DIR GIT_WORK_TREE

echo "== pack (sorted, fixed mtime $epoch, gzip -n)"
(cd "$stage" && find . -mindepth 1 -print0 | sed -z 's|^\./||' | LC_ALL=C sort -z) >"$work/list"
tmp="$out/.$name.$$"
tar --format=gnu --null --no-recursion --owner=0 --group=0 --numeric-owner \
    --mtime="@$epoch" --mode='u+rw,a+rX,go-w' -C "$stage" -T "$work/list" -cf - | gzip -n -9 >"$tmp"
mv -f "$tmp" "$out/$name"

echo "== check the result"
listing="$(tar -tzf "$out/$name")"
for must in agent/go.mod agent/cmd/aiagent/main.go; do
  grep -qxF "$must" <<<"$listing" || { rm -f "$out/$name"; die "the tarball does not contain $must"; }
done
if [[ -f "$stage/agent/vendor/modules.txt" ]]; then
  grep -qxF agent/vendor/modules.txt <<<"$listing" || { rm -f "$out/$name"; die "the tarball does not contain agent/vendor/modules.txt"; }
fi
if [[ -f "$stage/agent/ui/package.json" ]]; then
  grep -qxF agent/ui/dist/index.html <<<"$listing" || { rm -f "$out/$name"; die "the tarball does not contain agent/ui/dist/index.html"; }
fi
echo "✓ $out/$name"
(cd "$out" && sha256sum "$name")
