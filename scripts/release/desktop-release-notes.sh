#!/usr/bin/env bash
# desktop-release-notes.sh: the release notes of a desktop release (tag desktop-v<semver>), as Markdown on stdout.
# Spec: docs/specs/desktop.md section 4.11. Used by desktop-release.yml (gh release create --notes-file).
#
#   desktop-release-notes.sh --tag desktop-v<semver> --dir <assembled release folder>
#
# <dir> is the output of assemble-desktop-release.sh. The notes say how to install, how to check the download
# (SHA256SUMS and the minisign signature; the same check offline with `aictl verify-release`), and list each asset with
# its size and sha256 (the numbers a kit-desktop bump PR needs). Nothing secret is read or printed.
#
# Exit: 0 ok, 1 an asset is missing, 2 usage error.
set -euo pipefail

usage() { sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "desktop-release-notes: $1" >&2; exit "${2:-1}"; }

tag=""; dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 || usage ;;
    --dir) dir="${2:-}"; shift 2 || usage ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[[ -n "$tag" && -n "$dir" ]] || usage
[[ "$tag" =~ ^desktop-v([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$ ]] || die "tag '$tag' is not desktop-v<semver>" 2
version="${BASH_REMATCH[1]}"
pre="${BASH_REMATCH[2]}"

deb="ai-desktop_${version}_amd64.deb"
appimage="ai-desktop-${version}-x86_64.AppImage"
for f in "$deb" "$appimage" latest-linux.yml SHA256SUMS; do
  [[ -f "$dir/$f" ]] || die "$dir/$f not found"
done
sum_of() { awk -v f="$1" '$2 == f { print $1 }' "$dir/SHA256SUMS"; }

echo "# ai-desktop ${version}"
echo
if [[ -n "$pre" ]]; then
  echo "> **Pre-release.** For testing; it is not offered to anyone running a stable version."
  echo
fi
echo "The desktop app for the Local AI node agent. It works fully offline."
echo
echo "## Install"
echo
echo "    sudo apt install ./${deb}"
echo
echo "On a node with no internet, use the copy in the rebuild kit: \`sudo apt install <root>/kit/desktop/${deb}\`. The AppImage runs without installing: \`chmod +x ${appimage} && ./${appimage}\`."
echo
echo "## Check what you downloaded"
echo
echo "Linux installers are not code-signed. Their integrity comes from \`SHA256SUMS\`, which is signed with the same minisign key as the agent (\`SHA256SUMS.minisig\`; the public key is \`agent/internal/kit/keys/minisign.pub\` in the repository)."
echo
echo "    minisign -Vm SHA256SUMS -p minisign.pub"
echo "    sha256sum -c SHA256SUMS"
echo
echo "With the agent installed, one command does both, offline: \`aictl verify-release <folder>\`."
echo
echo "## Files"
echo
echo "| File | Size (bytes) | SHA-256 |"
echo "|---|---|---|"
for f in "$deb" "$appimage" latest-linux.yml; do
  size="$(wc -c <"$dir/$f" | tr -d ' ')"
  sum="$(sum_of "$f")"
  [[ -n "$sum" ]] || die "SHA256SUMS has no line for $f"
  echo "| \`$f\` | $size | \`$sum\` |"
done
echo
echo "\`latest-linux.yml\` is what the app's optional **Check for updates** reads (off by default; it makes no request until ticked). To put this release in the rebuild kit, open a PR from the \`kit-desktop-bump\` template."
