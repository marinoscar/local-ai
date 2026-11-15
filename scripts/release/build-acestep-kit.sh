#!/usr/bin/env bash
# build-acestep-kit.sh: make the music runtime's kit item kit-engine-acestep-<backend> (rebuild-kit.md
# section 3.1, music.md section 3.1, #511). It runs at release time WITH internet, on a maintainer's
# machine, never on a node: a node gets the files by `aictl ark sync` and installs them offline with
# `pip install --no-index --require-hashes` (the ensure step engine.acestep).
#
# Inputs (engines/acestep/kit/): <backend>.pins, every package pinned with ==, and <backend>.env, the git
# commit of ACE-Step, the extra wheel index and the python-build-standalone tarball.
#
# It writes to --out:
#   requirements.lock      every pin with the sha256 of its wheel, plus ace-step, nano-vllm and the shim
#   release/               the files to publish on the release acestep-<version>: requirements.lock and the
#                          three wheels that are not on any index (built here from the pinned commit)
#   ark-files.yaml         the `files:` list of the catalog item: each wheel at its origin (PyPI, the PyTorch
#                          index), the release files and the interpreter, each with its sha256 and its path
#                          in the item (<version>/wheels/<file>, <version>/requirements.lock, <version>/python/<file>)
#   kit/<version>/         the same tree a node gets from sync, then checked with a real offline install
#                          (--no-check skips that; it needs about 8 GB)
#
# Usage: scripts/release/build-acestep-kit.sh --backend cuda --python PY [--out DIR] [--no-check]
#   --python is a Python of the version the runtime uses (3.12), to resolve and build the wheels.
# Exit: 0 made, 1 a step failed, 2 usage.
set -euo pipefail

backend="" python="" outdir="dist/acestep" check=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --backend) backend="${2:-}"; shift 2 ;;
    --python) python="${2:-}"; shift 2 ;;
    --out) outdir="${2:-}"; shift 2 ;;
    --no-check) check=0; shift ;;
    -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "usage: $0 --backend cuda --python PY [--out DIR] [--no-check]" >&2; exit 2 ;;
  esac
done
[[ "$backend" == "cuda" || "$backend" == "vulkan" || "$backend" == "cpu" ]] || { echo "build-acestep-kit: --backend must be cuda, vulkan or cpu" >&2; exit 2; }
[[ -n "$python" ]] && "$python" -c 'import sys; sys.exit(sys.version_info[:2] != (3, 12))' 2>/dev/null \
  || { echo "build-acestep-kit: --python must be a Python 3.12" >&2; exit 2; }

root="$(git rev-parse --show-toplevel)"
kitsrc="$root/engines/acestep/kit"
pins="$kitsrc/$backend.pins" env="$kitsrc/$backend.env"
[[ -f "$pins" && -f "$env" ]] || { echo "build-acestep-kit: no $backend.pins or $backend.env in engines/acestep/kit" >&2; exit 2; }
# shellcheck disable=SC1090
source "$env"
: "${RUNTIME_VERSION:?} ${ACESTEP_GIT:?} ${ACESTEP_COMMIT:?} ${NANOVLLM_SUBDIR:?} ${PYTHON_URL:?} ${PYTHON_SUMS_URL:?}"
version="$RUNTIME_VERSION"
release_url="https://github.com/marinoscar/local-ai/releases/download/acestep-$version"

rm -rf "$outdir" && mkdir -p "$outdir/release" "$outdir/kit/$version/wheels" "$outdir/kit/$version/python"
out="$(cd "$outdir" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
index_args=()
[[ -n "${EXTRA_INDEX_URL:-}" ]] && index_args=(--extra-index-url "$EXTRA_INDEX_URL")

echo "… Resolving $(grep -cv '^#' "$pins") pinned wheels"
grep -v '^#' "$pins" >"$work/pins.txt"
"$python" -m pip install --quiet --dry-run --ignore-installed --no-deps --only-binary=:all: \
  --target "$work/none" "${index_args[@]}" --report "$work/report.json" -r "$work/pins.txt"

echo "… Building ace-step and nano-vllm at ${ACESTEP_COMMIT:0:12}, and the shim"
"$python" -m pip wheel --quiet --no-deps -w "$out/release" "git+$ACESTEP_GIT@$ACESTEP_COMMIT"
"$python" -m pip wheel --quiet --no-deps -w "$out/release" "git+$ACESTEP_GIT@$ACESTEP_COMMIT#subdirectory=$NANOVLLM_SUBDIR"
"$python" -m pip wheel --quiet --no-deps -w "$out/release" "$root/engines/acestep"

echo "… Getting the interpreter"
pyfile="$("$python" -c 'import sys, urllib.parse; print(urllib.parse.unquote(sys.argv[1].rsplit("/", 1)[1]))' "$PYTHON_URL")"
curl -fsSL "$PYTHON_URL" -o "$out/kit/$version/python/$pyfile"
curl -fsSL "$PYTHON_SUMS_URL" -o "$work/python.sums"
want="$(awk -v f="$pyfile" '$2 == f || $2 == "*"f {print $1}' "$work/python.sums")"
got="$(sha256sum "$out/kit/$version/python/$pyfile" | cut -d' ' -f1)"
[[ -n "$want" && "$want" == "$got" ]] || { echo "build-acestep-kit: $pyfile does not match upstream's SHA256SUMS" >&2; exit 1; }

echo "… Downloading the wheels (the index does not always give a hash: each file is hashed here)"
"$python" -m pip download --quiet --no-deps --only-binary=:all: "${index_args[@]}" \
  -d "$out/kit/$version/wheels" -r "$work/pins.txt"

echo "… Writing the lock and the catalog's file list"
"$python" - "$work/report.json" "$out" "$version" "$release_url" "$PYTHON_URL" "$pyfile" "$got" <<'PY'
import hashlib, json, os, sys, urllib.parse
report, out, version, release_url, py_url, py_file, py_sum = sys.argv[1:]
wheels = os.path.join(out, "kit", version, "wheels")
def sha_of(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()
lock, files = [], []
for it in json.load(open(report))["install"]:
    meta, info = it["metadata"], it["download_info"]
    name = urllib.parse.unquote(info["url"].rsplit("/", 1)[1])
    path = os.path.join(wheels, name)
    if not os.path.isfile(path):
        sys.exit(f"build-acestep-kit: pip download did not fetch {name}")
    sha = sha_of(path)
    given = info.get("archive_info", {}).get("hashes", {}).get("sha256")
    if given and given != sha:
        sys.exit(f"build-acestep-kit: {name} does not match the index's sha256")
    lock.append(f'{meta["name"]}=={meta["version"]} --hash=sha256:{sha}')
    files.append((info["url"], sha, f"{version}/wheels/{name}"))
rel = os.path.join(out, "release")
for whl in sorted(os.listdir(rel)):
    sha = sha_of(os.path.join(rel, whl))
    dist, ver = whl.split("-")[:2]
    lock.append(f'{dist.replace("_", "-")}=={ver} --hash=sha256:{sha}')
    files.append((f"{release_url}/{urllib.parse.quote(whl)}", sha, f"{version}/wheels/{whl}"))
lock.sort(key=str.lower)
text = "# The music runtime " + version + ": install with pip install --no-index --require-hashes.\n" + "\n".join(lock) + "\n"
open(os.path.join(out, "requirements.lock"), "w").write(text)
open(os.path.join(rel, "requirements.lock"), "w").write(text)
lock_sha = hashlib.sha256(text.encode()).hexdigest()
# The lock first: an item's version is the first 12 digits of its first file's sha256.
files.sort(key=lambda f: f[2])
files.insert(0, (f"{release_url}/requirements.lock", lock_sha, f"{version}/requirements.lock"))
files.append((py_url, py_sum, f"{version}/python/{py_file}"))
with open(os.path.join(out, "ark-files.yaml"), "w") as f:
    for url, sha, name in files:
        f.write(f'        - {{url: "{url}", sha256: "{sha}", name: "{name}"}}\n')
print(f"{len(files)} files")
PY

echo "… Making the kit tree a node would sync"
cp "$out/requirements.lock" "$out/kit/$version/requirements.lock"
cp "$out"/release/*.whl "$out/kit/$version/wheels/"

if [[ "$check" == 1 ]]; then
  echo "… Checking: an offline install from the kit tree with its own interpreter, then the shim's self-test"
  tar -xzf "$out/kit/$version/python/$pyfile" -C "$work"
  PIP_NO_INDEX=1 "$work/python/bin/python3" -m pip install --quiet --no-index --find-links "$out/kit/$version/wheels" \
    --require-hashes -r "$out/kit/$version/requirements.lock"
  HF_HUB_OFFLINE=1 "$work/python/bin/python3" -m localai_music_shim --self-test
fi
echo "$out/ark-files.yaml"
echo "$(du -sb "$out/kit/$version" | cut -f1) bytes in the item"
