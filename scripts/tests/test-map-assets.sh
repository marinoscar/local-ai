#!/usr/bin/env bash
# test-map-assets.sh: scripts/release/build-map-assets.sh (issue #487) against a fake upstream git repository built
# here, with no network: the archive layout (fonts and sprites at the top, licences, SOURCE.txt), reproducibility
# (two builds are byte-identical), and the refusals (bad commit, a missing font folder, missing sprites).
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2
ROOT="$PWD"
for tool in git tar gzip sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool not installed: skipping map-assets tests"; exit 0; }
done
tar --version 2>/dev/null | head -1 | grep -q 'GNU tar' || { echo "GNU tar not installed: skipping map-assets tests"; exit 0; }
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -10; fi
}
w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT
BUILD="$ROOT/scripts/release/build-map-assets.sh"
g() { git -C "$w/up" -c user.name=t -c user.email=t@example.test "$@"; }

# A fake upstream: commit 1 holds the sprite licence (stands in for tangrams/icons), commit 2 the assets.
mkdir -p "$w/up"; git init -q "$w/up"
printf 'The MIT License (MIT)\n' >"$w/up/LICENSE.md"
g add -A; g commit -q -m icons; icons=$(g rev-parse HEAD)
for f in "Noto Sans Regular" "Noto Sans Medium" "Noto Sans Italic" "Noto Sans Devanagari Regular v1"; do
  mkdir -p "$w/up/fonts/$f"; printf 'pbf' >"$w/up/fonts/$f/0-255.pbf"; printf 'pbf' >"$w/up/fonts/$f/256-511.pbf"
done
printf 'OFL\n' >"$w/up/fonts/OFL.txt"
mkdir -p "$w/up/sprites/v4" "$w/up/sprites/v3"
for s in light.json light.png dark.json dark.png; do printf 's' >"$w/up/sprites/v4/$s"; done
printf 'old' >"$w/up/sprites/v3/light.json"
printf 'readme' >"$w/up/README.md"
g add -A; g commit -q -m assets; good=$(g rev-parse HEAD)
g rm -q -r "fonts/Noto Sans Medium"; g commit -q -m nofont; nofont=$(g rev-parse HEAD)
g checkout -q "$good"; g rm -q -r sprites/v4; g commit -q -m nosprites; nosprites=$(g rev-parse HEAD)
export MAP_ASSETS_ICONS_COMMIT="$icons"

echo "build-map-assets.sh"
t "builds from a pinned commit" 0 "map-assets-${good:0:7}.tar.gz" "$BUILD" --commit "$good" --out "$w/a" --source "$w/up"
t "a second build is byte-identical" 0 "" "$BUILD" --commit "$good" --out "$w/b" --source "$w/up"
t "the two builds match" 0 "" cmp "$w/a/map-assets-${good:0:7}.tar.gz" "$w/b/map-assets-${good:0:7}.tar.gz"
list="$(tar -tzf "$w/a/map-assets-${good:0:7}.tar.gz" 2>/dev/null)"
has() { grep -qxF -- "$1" <<<"$list"; }
t "fonts at the top, the three viewer fonts" 0 "" has "fonts/Noto Sans Italic/0-255.pbf"
t "Noto Sans Medium included" 0 "" has "fonts/Noto Sans Medium/256-511.pbf"
t "the font licence" 0 "" has "fonts/OFL.txt"
t "the v4 sprites" 0 "" has "sprites/v4/light.png"
t "every v4 sprite sheet" 0 "" has "sprites/v4/dark.json"
t "the sprite licence" 0 "" has "sprites/LICENSE.md"
t "SOURCE.txt" 0 "" has "SOURCE.txt"
t "no extra font" 1 "" has "fonts/Noto Sans Devanagari Regular v1/0-255.pbf"
t "no v3 sprites" 1 "" has "sprites/v3/light.json"
t "no README or top folder" 1 "" grep -qE '^(README.md|basemaps-assets)' <<<"$list"
owners="$(tar -tzvf "$w/a/map-assets-${good:0:7}.tar.gz" | awk '{print $2}' | sort -u)"
t "every entry is 0/0" 0 "" test "$owners" = "0/0"
t "SOURCE.txt names the commit" 0 "$good" tar -xzOf "$w/a/map-assets-${good:0:7}.tar.gz" SOURCE.txt
t "refuses a short commit" 2 "40-hex" "$BUILD" --commit "${good:0:7}" --out "$w/c" --source "$w/up"
t "refuses without --out" 2 "--out" "$BUILD" --commit "$good"
t "fails on a missing font folder" 1 "Noto Sans Medium" "$BUILD" --commit "$nofont" --out "$w/d" --source "$w/up"
t "fails on missing v4 sprites" 1 "sprites/v4" "$BUILD" --commit "$nosprites" --out "$w/e" --source "$w/up"

echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
