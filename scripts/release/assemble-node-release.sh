#!/usr/bin/env bash
# assemble-node-release.sh: lay out the six assets of a node release (tag node-v<semver>) in one folder.
# Layout consumed by the agent's self-update, the installer and the rebuild kit: docs/specs/agent.md section 4.10,
# docs/specs/rebuild-kit.md section 3.1, docs/architecture/release-signing.md.
#
#   assemble-node-release.sh --tag node-v<semver> --bin-dir <dir> --tarball <src.tar.gz> --pubkey <minisign.pub>
#                            --out <dir> [--installer <install-node.sh>] [--allow-missing-installer] [--sign]
#
#   <out>/aiagent-linux-amd64  aiagent-linux-arm64   copied from --bin-dir
#   <out>/install-node.sh                            the template stamped by stamp-installer.sh (version + key)
#   <out>/local-ai-<tag>-src.tar.gz                  copied from --tarball (its name must match the tag)
#   <out>/SHA256SUMS                                 all of the above, sorted (scripts/release-sign.sh sums)
#   <out>/SHA256SUMS.minisig                         with --sign: scripts/release-sign.sh sign, then verify against
#                                                    --pubkey (a wrong or stale secret fails here, not on a node)
#
# --sign reads MINISIGN_SECRET_KEY and MINISIGN_PASSWORD from the environment (release-sign.sh); never from a file
# in the repository. --installer defaults to installers/install-node.sh. Without --allow-missing-installer a missing
# installer is an error; the dry run passes the flag while INST-01 has not landed, and then ships five assets.
#
# Exit: 0 ok, 1 a check or the signing failed, 2 usage or environment error.
set -euo pipefail

usage() { sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "assemble-node-release: $1" >&2; exit "${2:-1}"; }

tag=""; bindir=""; tarball=""; pub=""; out=""; installer=""; allow_missing=0; sign=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 || usage ;;
    --bin-dir) bindir="${2:-}"; shift 2 || usage ;;
    --tarball) tarball="${2:-}"; shift 2 || usage ;;
    --pubkey) pub="${2:-}"; shift 2 || usage ;;
    --out) out="${2:-}"; shift 2 || usage ;;
    --installer) installer="${2:-}"; shift 2 || usage ;;
    --allow-missing-installer) allow_missing=1; shift ;;
    --sign) sign=1; shift ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[[ -n "$tag" && -n "$bindir" && -n "$tarball" && -n "$pub" && -n "$out" ]] || usage
[[ "$tag" =~ ^node-v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "tag '$tag' is not node-v<semver>" 2
[[ -f "$pub" ]] || die "public key '$pub' not found. Owner action pending: commit the minisign public key (docs/architecture/release-signing.md)" 2
[[ "$(basename "$tarball")" == "local-ai-$tag-src.tar.gz" ]] || die "the tarball must be named local-ai-$tag-src.tar.gz, got $(basename "$tarball")" 2
[[ -f "$tarball" ]] || die "tarball '$tarball' not found" 2

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
[[ -n "$installer" ]] || installer="$root/installers/install-node.sh"

mkdir -p "$out"
[[ ! -e "$out/SHA256SUMS" ]] || die "$out already holds a SHA256SUMS: use an empty folder" 2

files=()
for arch in amd64 arm64; do
  [[ -f "$bindir/aiagent-linux-$arch" ]] || die "$bindir/aiagent-linux-$arch not found"
  cp "$bindir/aiagent-linux-$arch" "$out/aiagent-linux-$arch"
  chmod 0755 "$out/aiagent-linux-$arch"
  files+=("aiagent-linux-$arch")
done

if [[ -f "$installer" ]]; then
  "$here/stamp-installer.sh" --in "$installer" --out "$out/install-node.sh" --tag "$tag" --pubkey "$pub"
  files+=(install-node.sh)
elif [[ $allow_missing -eq 1 ]]; then
  echo "  note $installer does not exist yet (INST-01): this release carries no install-node.sh"
else
  die "$installer does not exist: a node release must carry install-node.sh (INST-01)"
fi

cp "$tarball" "$out/local-ai-$tag-src.tar.gz"
files+=("local-ai-$tag-src.tar.gz")

"$here/../release-sign.sh" sums "$out" "${files[@]}"
if [[ $sign -eq 1 ]]; then
  "$here/../release-sign.sh" sign "$out"
  "$here/../release-sign.sh" verify "$out" "$pub"
fi
echo "assets in $out:"
(cd "$out" && ls -1)
