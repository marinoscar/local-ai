#!/bin/sh
# A fake `pmtiles` for CI (the real go-pmtiles release binary is not needed to test the resolver).
# It understands the three commands the sync agent runs:
#
#   pmtiles extract <url> <output> --bbox=... --maxzoom=... [--dry-run] [--download-threads=N] [--overfetch=F]
#   pmtiles verify <file>
#   pmtiles show --header-json <file>
#
# It never touches the network. `extract` copies tiny.pmtiles (next to this script) to <output>.
# Every call is appended to calls.log next to the script (argv, HTTPS_PROXY, HOME), so a test can see
# exactly what the agent ran. The behaviour comes from the first line of the file "mode" next to the
# script (default "ok"):
#
#   ok               everything works
#   extract_fail     extract exits 1 with a message on stderr
#   extract_fail_once  the first extract exits 1, later ones work (counter file "extracts")
#   no_output        extract exits 0 but writes nothing
#   slow             extract sleeps 60 s first (timeout and cancel tests)
#   verify_fail      verify exits 1
#   wrong_type       show reports tile_type "png"
#   small_bbox       show reports a bounding box smaller than the requested one
#   dry_unparseable  extract --dry-run prints nothing useful
here=$(dirname "$0")
mode=ok
[ -f "$here/mode" ] && mode=$(head -n 1 "$here/mode")
{
  printf 'argv:'
  for a in "$@"; do printf ' [%s]' "$a"; done
  printf ' proxy=[%s] home=[%s] pwd_ok=1\n' "$HTTPS_PROXY" "$HOME"
} >> "$here/calls.log"

cmd=$1
[ $# -gt 0 ] && shift
case "$cmd" in
extract)
  url=$1
  out=$2
  shift 2
  dry=0
  for a in "$@"; do
    [ "$a" = "--dry-run" ] && dry=1
  done
  if [ "$dry" = 1 ]; then
    if [ "$mode" = dry_unparseable ]; then
      echo "nothing to see"
      exit 0
    fi
    echo "Region tiles 41234, result tile entries 41234"
    echo "fetching 3 dirs, 17 chunks, 17 requests"
    echo "tiles to extract: 4.2 MB"
    exit 0
  fi
  case "$mode" in
  extract_fail)
    echo "Error: could not fetch the planet header from $url" >&2
    exit 1
    ;;
  extract_fail_once)
    if [ ! -f "$here/extracts" ]; then
      echo 1 > "$here/extracts"
      echo "Error: connection reset by peer" >&2
      exit 1
    fi
    ;;
  slow)
    sleep 60
    ;;
  no_output)
    exit 0
    ;;
  esac
  cp "$here/tiny.pmtiles" "$out" || exit 1
  echo "Completed in 1.2s with 4 download threads"
  ;;
verify)
  f=$1
  [ -f "$f" ] || { echo "Error: cannot open $f" >&2; exit 1; }
  if [ "$mode" = verify_fail ]; then
    echo "Error: tile 5/10/12 is out of order" >&2
    exit 1
  fi
  echo "Completed verify in 10ms."
  ;;
show)
  f=""
  for a in "$@"; do
    case "$a" in -*) ;; *) f=$a ;; esac
  done
  [ -f "$f" ] || { echo "Error: cannot open $f" >&2; exit 1; }
  type=mvt
  minlon=-925000000
  minlat=70000000
  maxlon=-770000000
  maxlat=186000000
  [ "$mode" = wrong_type ] && type=png
  if [ "$mode" = small_bbox ]; then
    minlon=-900000000
    maxlat=150000000
  fi
  # What the real pmtiles 1.31.2 prints (issue #484): degrees in a "bounds" array, west, south, east, north.
  deg() { awk -v v="$1" 'BEGIN { printf "%.7g", v / 1e7 }'; }
  printf '{\n    "tile_compression": "gzip",\n    "tile_type": "%s",\n    "minzoom": 0,\n    "maxzoom": 15,\n    "bounds": [\n        %s,\n        %s,\n        %s,\n        %s\n    ],\n    "center": [\n        -84.75,\n        12.8,\n        0\n    ]\n}\n' \
    "$type" "$(deg "$minlon")" "$(deg "$minlat")" "$(deg "$maxlon")" "$(deg "$maxlat")"
  ;;
*)
  echo "fake pmtiles: unknown command $cmd" >&2
  exit 2
  ;;
esac
