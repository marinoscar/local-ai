#!/usr/bin/env bash
# build-map-assets.sh: build map-assets-<short>.tar.gz, the fonts and sprites the offline map viewer needs.
# Spec: docs/specs/catalog.md section 3 (map-assets), docs/specs/companion-services.md section 4.4 (Map assets).
#
#   build-map-assets.sh --commit <40-hex upstream commit> --out <dir> [--source <git dir>]
#
# Upstream protomaps/basemaps-assets publishes no releases, and GitHub's generated branch archives are not
# promised to stay byte-identical, so a sha256 pin cannot point at them (issue #487). This script takes the
# files at a pinned upstream COMMIT through git (every object is checked against the commit hash) and packs
# exactly what the viewer reads, at the top of the archive (the item is unpacked as-is, ark-sync.md 4.9.4):
#   fonts/Noto Sans Regular/*.pbf, fonts/Noto Sans Medium/*.pbf, fonts/Noto Sans Italic/*.pbf, fonts/OFL.txt
#   sprites/v4/*            (the light, dark, white, grayscale and black sheets at 1x and 2x)
#   sprites/LICENSE.md      (MIT notice of tangrams/icons, from which the sprites derive; pinned commit below)
#   SOURCE.txt              (where every file came from)
#
# Reproducible: entries sorted bytewise, owner 0:0, every mtime = the upstream commit time, modes 0644/0755,
# GNU tar format, `gzip -n`. The same commit gives a byte-identical file.
#
# --source <git dir>: use an existing clone that holds the commits (tests, offline rebuilds) instead of fetching.
# Output: <dir>/map-assets-<short>.tar.gz; the last line printed is its sha256 and name.
# Exit: 0 ok, 1 a check failed, 2 usage or environment error.
set -euo pipefail

usage() { sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "build-map-assets: $1" >&2; exit "${2:-1}"; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required" 2; }

UPSTREAM=https://github.com/protomaps/basemaps-assets.git
ICONS=https://github.com/tangrams/icons.git
ICONS_COMMIT="${MAP_ASSETS_ICONS_COMMIT:-92510779634f4a006c61ea70e50cb8c52c765a81}" # tests may override; tangrams/icons master, LICENSE.md (MIT, Mapzen 2017)
FONTS=("Noto Sans Regular" "Noto Sans Medium" "Noto Sans Italic")

commit="" out="" source=""
while [ $# -gt 0 ]; do
  case "$1" in
    --commit) commit="${2:-}"; shift 2 ;;
    --out) out="${2:-}"; shift 2 ;;
    --source) source="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "build-map-assets: unknown argument $1" >&2; usage ;;
  esac
done
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "--commit must be a full 40-hex commit" 2
[ -n "$out" ] || die "--out is required" 2
need git; need tar; need gzip; need sha256sum; need find; need sort
tar --version 2>/dev/null | head -1 | grep -q 'GNU tar' || die "GNU tar is required (reproducible options)" 2
mkdir -p "$out"
out="$(cd "$out" && pwd)"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# fetch <url> <commit> <dir>: a checkout of exactly <commit>, verified by git.
fetch() {
  local url="$1" want="$2" dir="$3"
  git init -q "$dir"
  if [ -n "$source" ]; then
    git -C "$dir" fetch -q "$source" "$want" 2>/dev/null || git -C "$dir" fetch -q "$source" 2>/dev/null || true
  fi
  git -C "$dir" cat-file -e "$want^{commit}" 2>/dev/null || git -C "$dir" fetch -q --depth 1 "$url" "$want" ||
    die "cannot fetch $want from $url" 1
  git -C "$dir" -c advice.detachedHead=false checkout -q "$want"
  [ "$(git -C "$dir" rev-parse HEAD)" = "$want" ] || die "checkout of $want did not land on it" 1
}

echo "== fetch protomaps/basemaps-assets at $commit"
fetch "$UPSTREAM" "$commit" "$work/up"
echo "== fetch tangrams/icons at $ICONS_COMMIT (sprite licence)"
fetch "$ICONS" "$ICONS_COMMIT" "$work/icons"

stage="$work/stage"
mkdir -p "$stage/fonts" "$stage/sprites"
for f in "${FONTS[@]}"; do
  [ -d "$work/up/fonts/$f" ] || die "upstream has no font folder 'fonts/$f' at $commit" 1
  n=$(find "$work/up/fonts/$f" -maxdepth 1 -type f -name '*.pbf' | wc -l)
  [ "$n" -gt 0 ] || die "upstream font folder 'fonts/$f' has no .pbf files" 1
  cp -r "$work/up/fonts/$f" "$stage/fonts/"
done
[ -f "$work/up/fonts/OFL.txt" ] || die "upstream has no fonts/OFL.txt" 1
cp "$work/up/fonts/OFL.txt" "$stage/fonts/OFL.txt"
if [ ! -f "$work/up/sprites/v4/light.json" ] || [ ! -f "$work/up/sprites/v4/light.png" ]; then
  die "upstream has no sprites/v4/light.{json,png}" 1
fi
cp -r "$work/up/sprites/v4" "$stage/sprites/v4"
cp "$work/icons/LICENSE.md" "$stage/sprites/LICENSE.md"

epoch="$(git -C "$work/up" log -1 --format=%ct)"
cat >"$stage/SOURCE.txt" <<EOF
Map styles and fonts for the Local AI offline map viewer (catalog item map-assets).

fonts/ and sprites/v4/ come from https://github.com/protomaps/basemaps-assets
  commit $commit
  fonts: SIL Open Font License 1.1 (fonts/OFL.txt)
  sprites: derived from tangrams/icons, MIT licence (sprites/LICENSE.md,
           https://github.com/tangrams/icons commit $ICONS_COMMIT)

Built by scripts/release/build-map-assets.sh in https://github.com/marinoscar/local-ai
EOF

echo "== pack (sorted, fixed mtime $epoch, gzip -n)"
(cd "$stage" && find . -mindepth 1 -printf '%P\0' | LC_ALL=C sort -z) >"$work/list"
name="map-assets-${commit:0:7}.tar.gz"
tmp="$out/.$name.tmp"
tar --format=gnu --null --no-recursion --owner=0 --group=0 --numeric-owner \
    --mtime="@$epoch" --mode='u+rw,a+rX,go-w' -C "$stage" -T "$work/list" -cf - | gzip -n -9 >"$tmp"
mv -f "$tmp" "$out/$name"
files=$(tr -cd '\0' <"$work/list" | wc -c)
echo "== $files entries, $(du -h "$out/$name" | cut -f1)"
(cd "$out" && sha256sum "$name")
