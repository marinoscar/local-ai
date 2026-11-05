#!/usr/bin/env bash
# assemble-desktop-release.sh: lay out the five assets of a desktop release (tag desktop-v<semver>) in one folder.
# Spec: docs/specs/desktop.md section 4.11; trust root and key custody: docs/architecture/release-signing.md.
#
#   assemble-desktop-release.sh --tag desktop-v<semver> --dir <electron-builder output> --pubkey <minisign.pub>
#                               --out <dir> [--sign]
#
#   <dir> holds what `node scripts/dist.mjs --linux --x64` wrote (apps/desktop/release):
#     ai-desktop_<v>_amd64.deb   ai-desktop-<v>-x86_64.AppImage   latest-linux.yml
#   <out>/ gets those three files, then
#     SHA256SUMS            the three above, sorted (scripts/release-sign.sh sums)
#     SHA256SUMS.minisig    with --sign: scripts/release-sign.sh sign, then verify against --pubkey (a wrong or stale
#                           secret fails here, not on a node)
#
# Before anything is copied the script checks that latest-linux.yml (what electron-updater reads) names this version
# and that its sha512 and size for the deb and the AppImage are those of the files, so a stale yml never ships.
# --sign reads MINISIGN_SECRET_KEY and MINISIGN_PASSWORD from the environment (release-sign.sh); never from a file in
# the repository.
#
# Exit: 0 ok, 1 a check or the signing failed, 2 usage or environment error.
set -euo pipefail

usage() { sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "assemble-desktop-release: $1" >&2; exit "${2:-1}"; }

tag=""; dir=""; pub=""; out=""; sign=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 || usage ;;
    --dir) dir="${2:-}"; shift 2 || usage ;;
    --pubkey) pub="${2:-}"; shift 2 || usage ;;
    --out) out="${2:-}"; shift 2 || usage ;;
    --sign) sign=1; shift ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[[ -n "$tag" && -n "$dir" && -n "$pub" && -n "$out" ]] || usage
[[ "$tag" =~ ^desktop-v([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$ ]] || die "tag '$tag' is not desktop-v<semver>" 2
version="${BASH_REMATCH[1]}"
[[ -d "$dir" ]] || die "folder '$dir' not found" 2
[[ -f "$pub" ]] || die "public key '$pub' not found. Owner action pending: commit the minisign public key (docs/architecture/release-signing.md)" 2
for tool in sha256sum openssl base64 awk; do command -v "$tool" >/dev/null 2>&1 || die "'$tool' is required" 2; done

deb="ai-desktop_${version}_amd64.deb"
appimage="ai-desktop-${version}-x86_64.AppImage"
yml="latest-linux.yml"
for f in "$deb" "$appimage" "$yml"; do
  [[ -f "$dir/$f" ]] || die "$dir/$f not found (electron-builder names are fixed: desktop.md section 3.3)"
done

# latest-linux.yml: the version, and for each file its sha512 (base64) and size.
yml_version="$(sed -n "s/^version: *['\"]\{0,1\}\([^'\"]*\)['\"]\{0,1\} *$/\1/p" "$dir/$yml" | head -n 1)"
[[ "$yml_version" == "$version" ]] || die "$yml says version '${yml_version:-none}', the tag says '$version'"
entry() { # entry <file> <field>: the sha512 or size listed for <file> under files:
  awk -v f="$1" -v field="$2" '
    /^  - url: / { cur = $0; sub(/^  - url: */, "", cur); next }
    cur == f && $1 == field ":" { print $2; exit }
  ' "$dir/$yml"
}
for f in "$deb" "$appimage"; do
  want_sha="$(entry "$f" sha512)"; want_size="$(entry "$f" size)"
  [[ -n "$want_sha" && -n "$want_size" ]] || die "$yml does not list $f with a sha512 and a size"
  got_sha="$(openssl dgst -sha512 -binary "$dir/$f" | base64 -w0)"
  got_size="$(wc -c <"$dir/$f" | tr -d ' ')"
  [[ "$got_sha" == "$want_sha" ]] || die "$yml has a different sha512 for $f than the file has (rebuilt after the yml was written?)"
  [[ "$got_size" == "$want_size" ]] || die "$yml says $f is $want_size bytes, the file is $got_size"
done

mkdir -p "$out"
[[ ! -e "$out/SHA256SUMS" ]] || die "$out already holds a SHA256SUMS: use an empty folder" 2
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for f in "$deb" "$appimage" "$yml"; do
  cp "$dir/$f" "$out/$f"
  chmod 0644 "$out/$f"
done
"$here/../release-sign.sh" sums "$out" "$deb" "$appimage" "$yml"
if [[ $sign -eq 1 ]]; then
  "$here/../release-sign.sh" sign "$out"
  "$here/../release-sign.sh" verify "$out" "$pub"
fi
echo "assets in $out:"
(cd "$out" && ls -1)
