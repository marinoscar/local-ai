#!/usr/bin/env bash
# desktop-kit-snippet.sh: the `kit-desktop` item of catalog/ark.yaml for one desktop release, ready to paste (the
# kit-desktop-bump PR template, docs/specs/desktop.md section 4.11 step 5). It prints YAML on stdout and nothing else.
#
#   desktop-kit-snippet.sh --tag desktop-v<semver> --dir <downloaded release folder> [--pubkey <minisign.pub>]
#
# <dir> is the release as downloaded (`gh release download <tag> -D <dir>`). With --pubkey the signature of SHA256SUMS
# and every file are checked first (scripts/release-sign.sh verify), and nothing is printed when that fails: never pin a
# file you have not verified. Without --pubkey only SHA256SUMS is checked against the files, and a warning is printed
# on stderr. The pins are read from the files you hold, never typed.
#
# Exit: 0 ok, 1 a check failed, 2 usage or environment error.
set -euo pipefail

usage() { sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "desktop-kit-snippet: $1" >&2; exit "${2:-1}"; }

tag=""; dir=""; pub=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag) tag="${2:-}"; shift 2 || usage ;;
    --dir) dir="${2:-}"; shift 2 || usage ;;
    --pubkey) pub="${2:-}"; shift 2 || usage ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[[ -n "$tag" && -n "$dir" ]] || usage
[[ "$tag" =~ ^desktop-v([0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$ ]] || die "tag '$tag' is not desktop-v<semver>" 2
version="${BASH_REMATCH[1]}"
[[ -d "$dir" ]] || die "folder '$dir' not found" 2
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

deb="ai-desktop_${version}_amd64.deb"
appimage="ai-desktop-${version}-x86_64.AppImage"
for f in "$deb" "$appimage" SHA256SUMS; do
  [[ -f "$dir/$f" ]] || die "$dir/$f not found"
done

if [[ -n "$pub" ]]; then
  "$here/../release-sign.sh" verify "$dir" "$pub" >&2 || die "the release does not verify against $pub: nothing printed"
else
  echo "desktop-kit-snippet: WARNING no --pubkey given, the minisign signature was NOT checked" >&2
  (cd "$dir" && sha256sum --quiet -c SHA256SUMS >/dev/null 2>&1) || die "a file does not match its SHA256SUMS line: nothing printed"
fi

base="https://github.com/marinoscar/local-ai/releases/download/${tag}"
sha_deb="$(sha256sum "$dir/$deb" | cut -d' ' -f1)"
sha_app="$(sha256sum "$dir/$appimage" | cut -d' ' -f1)"
size_deb="$(wc -c <"$dir/$deb" | tr -d ' ')"
size_app="$(wc -c <"$dir/$appimage" | tr -d ' ')"
# expected_size_bytes is rounded up for growth (catalog.md section 3.1), to the next 1,000,000
total=$(( size_deb + size_app ))
expected=$(( (total + 999999) / 1000000 * 1000000 ))

cat <<YAML
  - id: kit-desktop
    kind: kit
    display_name: "ai-desktop ${version} (rebuild kit)"
    summary: "The ai-desktop app (.deb and AppImage), to reinstall the app on a node with no internet."
    summary_es: "La aplicación ai-desktop (.deb y AppImage), para reinstalarla en un nodo sin internet."
    survival_tier: 1
    source:
      type: url
      files:
        - {url: "${base}/${deb}", sha256: "${sha_deb}"}
        - {url: "${base}/${appimage}", sha256: "${sha_app}"}
      version_policy: pinned
      checksum: pinned
    expected_size_bytes: ${expected}
YAML
