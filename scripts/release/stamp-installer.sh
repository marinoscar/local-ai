#!/usr/bin/env bash
# stamp-installer.sh: stamp the release version and the minisign public key into install-node.sh.
# Spec: docs/specs/installer.md (section "Release stamping"), docs/specs/rebuild-kit.md section 3.4.
#
#   stamp-installer.sh --in <installers/install-node.sh> --out <file> --tag node-v<semver> --pubkey <minisign.pub>
#
# The template carries three tokens; each is replaced everywhere it occurs:
#   @@LAI_RELEASE_TAG@@      the release tag            node-v0.1.0
#   @@LAI_RELEASE_VERSION@@  the tag without node-v     0.1.0   (what `aiagent version` reports)
#   @@LAI_MINISIGN_PUBKEY@@  line 2 of minisign.pub     RW...   (the argument of `minisign -V -P`)
# A template that lacks any of the three is refused, so an installer that cannot verify its download or does not
# know its own version is never shipped. A script that must detect "not stamped" splits the token in two
# (`"@@LAI_RELEASE_TAG""@@"`) so this script does not replace it.
#
# The public key comes from the one committed file (agent/internal/kit/keys/minisign.pub); after stamping, the
# key found in <out> is compared with the file again. <out> is written 0755, atomically, with no timestamps.
#
# Exit: 0 ok, 1 the template or the result is wrong, 2 usage or environment error.
set -euo pipefail

usage() { sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "stamp-installer: $1" >&2; exit "${2:-1}"; }

in=""; out=""; tag=""; pub=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --in) in="${2:-}"; shift 2 || usage ;;
    --out) out="${2:-}"; shift 2 || usage ;;
    --tag) tag="${2:-}"; shift 2 || usage ;;
    --pubkey) pub="${2:-}"; shift 2 || usage ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[[ -n "$in" && -n "$out" && -n "$tag" && -n "$pub" ]] || usage
[[ -f "$in" ]] || die "template '$in' not found" 2
[[ -f "$pub" ]] || die "public key '$pub' not found. Owner action pending: commit the minisign public key (docs/architecture/release-signing.md)" 2
[[ "$tag" =~ ^node-v([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$ ]] || die "tag '$tag' is not node-v<semver>" 2
version="${BASH_REMATCH[1]}"

# minisign.pub: "untrusted comment: ..." then the base64 key ('Ed' + 8-byte key id + 32-byte key = 56 chars).
[[ "$(wc -l <"$pub")" -le 2 && "$(head -1 "$pub")" == "untrusted comment:"* ]] || die "'$pub' is not a minisign public key file" 2
key="$(sed -n 2p "$pub")"
[[ "$key" =~ ^RW[A-Za-z0-9+/]{54}$ ]] || die "line 2 of '$pub' is not a minisign Ed25519 public key" 2

for token in LAI_RELEASE_TAG LAI_RELEASE_VERSION LAI_MINISIGN_PUBKEY; do
  grep -qF "@@${token}@@" "$in" || die "the template has no @@${token}@@ token (docs/specs/installer.md, Release stamping)"
done

tmp="$(mktemp "$(dirname "$out")/.stamp.XXXXXX")" || die "cannot write next to '$out'" 2
trap 'rm -f "$tmp"' EXIT
sed -e "s|@@LAI_RELEASE_TAG@@|$tag|g" \
    -e "s|@@LAI_RELEASE_VERSION@@|$version|g" \
    -e "s|@@LAI_MINISIGN_PUBKEY@@|$key|g" "$in" >"$tmp"

if grep -qE '@@LAI_[A-Z_]+@@' "$tmp"; then die "an unknown @@LAI_..@@ token is left in the template"; fi
bash -n "$tmp" || die "the stamped script has a syntax error"
for v in "$tag" "$version" "$key"; do
  grep -qF -- "$v" "$tmp" || die "the stamped values are missing from the result"
done
# One source of truth: the key in the stamped script is the key in the committed file.
grep -qF -- "$(sed -n 2p "$pub")" "$tmp" || die "the stamped key differs from $pub"

chmod 0755 "$tmp"
mv -f "$tmp" "$out"
echo "✓ stamped $out ($tag, key ${key:0:12}…)"
