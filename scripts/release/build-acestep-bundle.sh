#!/usr/bin/env bash
# build-acestep-bundle.sh: build the music engine's wheel bundle, the kit item kit-engine-acestep-<backend>
# (rebuild-kit.md section 3.1, music.md section 3.1). It runs at release time WITH internet (CI or a
# maintainer's machine), never on a node: a node installs the bundle offline with
# `pip install --no-index --require-hashes` (the ensure step engine.acestep).
#
# The bundle acestep-<version>-<backend>.tar.zst holds:
#   wheels/              every wheel of requirements.lock, plus the shim's own wheel (engines/acestep)
#   requirements.lock    the pinned requirements with a sha256 per wheel, the shim's line added
#
# Usage: scripts/release/build-acestep-bundle.sh --version V --backend cuda --lock FILE [--out DIR] [--python PY]
#   --lock is the pinned, hashed requirements of ACE-Step for that backend (TODO(#505): produced by the spike).
# Exit: 0 built (prints the bundle path and its sha256), 1 a build step failed, 2 usage.
set -euo pipefail

version="" backend="" lock="" outdir="dist" python="python3"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="${2:-}"; shift 2 ;;
    --backend) backend="${2:-}"; shift 2 ;;
    --lock) lock="${2:-}"; shift 2 ;;
    --out) outdir="${2:-}"; shift 2 ;;
    --python) python="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "usage: $0 --version V --backend cuda --lock FILE [--out DIR]" >&2; exit 2 ;;
  esac
done
[[ "$version" =~ ^[0-9][0-9A-Za-z.+-]{0,63}$ ]] || { echo "build-acestep-bundle: --version is required (for example 1.5.0)" >&2; exit 2; }
[[ "$backend" == "cuda" || "$backend" == "vulkan" || "$backend" == "cpu" ]] || { echo "build-acestep-bundle: --backend must be cuda, vulkan or cpu" >&2; exit 2; }
[[ -f "$lock" ]] || { echo "build-acestep-bundle: --lock FILE is required" >&2; exit 2; }
command -v zstd >/dev/null 2>&1 || { echo "build-acestep-bundle: zstd is not installed" >&2; exit 1; }

root="$(git rev-parse --show-toplevel)"
shim="$root/engines/acestep"
[[ -f "$shim/pyproject.toml" || -f "$shim/setup.py" ]] || { echo "build-acestep-bundle: $shim is not a Python package" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bundle/wheels" "$outdir"

# 1. Every pinned wheel, checked against its hash on the way in.
"$python" -m pip download --only-binary=:all: --require-hashes -r "$lock" -d "$work/bundle/wheels"
# 2. The shim's own wheel, and its hash appended to the lock.
"$python" -m pip wheel --no-deps -w "$work/shimwheel" "$shim"
shimwhl="$(ls "$work"/shimwheel/*.whl)"
cp "$shimwhl" "$work/bundle/wheels/"
cp "$lock" "$work/bundle/requirements.lock"
name="$(basename "$shimwhl")"
sum="$(sha256sum "$shimwhl" | cut -d' ' -f1)"
printf './wheels/%s --hash=sha256:%s\n' "$name" "$sum" >>"$work/bundle/requirements.lock"
# 3. A reproducible tarball: sorted names, fixed owner and time.
out="$outdir/acestep-$version-$backend.tar.zst"
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime='2000-01-01 00:00Z' \
  -C "$work/bundle" -cf - wheels requirements.lock | zstd -19 -q -o "$out" -f
echo "$out $(sha256sum "$out" | cut -d' ' -f1)"
