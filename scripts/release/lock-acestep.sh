#!/usr/bin/env bash
# lock-acestep.sh: write engines/acestep/requirements-<backend>.lock, the pinned and hashed wheel list of the music
# runtime that ark sync fills the kit item kit-engine-acestep-<backend> from (rebuild-kit.md section 3.2.2,
# music.md section 3.1). It runs on a maintainer's machine WITH internet, never on a node.
#
# For every `name==version` of engines/acestep/requirements-<backend>.in it picks the one wheel a node installs
# (linux x86_64, CPython 3.12: cp312, then abi3, then py3-none-any; manylinux up to glibc 2.39) and writes
#   name==version --hash=sha256:<sha256 of that wheel>
# The sha256 comes from the index itself: the PyPI JSON API (https://pypi.org/pypi/<name>/<version>/json) and, for
# a +cu128 version, the `#sha256=` of the PyTorch index page (https://download.pytorch.org/whl/cu128/<name>/).
# Nothing is guessed: a package whose index cannot be read is written as `# unresolved: name==version (<host>)`,
# and ark sync refuses a lock that still has such a line (LAI-SYN-060). It also writes the lines
#   # repo-tree-sha256: engines/acestep <sha256>
#   # build-requires: <names>
# The first is the tree hash of the shim's sources (every file of engines/acestep except tests/, *.in, *.lock and
# byte code; the "<sha256>  <path>" lines sorted bytewise and hashed, manifest.TreeSHA256), which ark sync checks on
# the shim it builds. The second copies the `# build-requires:` line of the .in: the build backends sync installs.
#
# Usage: scripts/release/lock-acestep.sh [--backend cuda] [--allow-missing] [--shim-only]
#   --allow-missing   write the lock even when an index could not be read (the unresolved lines say which)
#   --shim-only       recompute only the shim line of the existing lock (offline; after editing the shim)
# After it ran, put the printed sha256 into catalog/ark.yaml (kit-engine-acestep-<backend>, source.sha256) and bump
# catalog/CATALOG_VERSION. agent/internal/catalog's real-catalog test fails until the two agree.
# Exit: 0 written and complete, 3 written with unresolved packages (--allow-missing), 1 an index failed, 2 usage.
# The hosts it reads: pypi.org, download.pytorch.org. Nothing is downloaded but index metadata.
set -euo pipefail

backend="cuda" allow_missing=0 shim_only=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --backend) backend="${2:-}"; shift 2 ;;
    --allow-missing) allow_missing=1; shift ;;
    --shim-only) shim_only=1; shift ;;
    -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "usage: $0 [--backend cuda] [--allow-missing] [--shim-only]" >&2; exit 2 ;;
  esac
done
[[ "$backend" == "cuda" ]] || { echo "lock-acestep: only --backend cuda has a measured runtime (spike #505)" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "lock-acestep: python3 is required" >&2; exit 2; }
command -v sha256sum >/dev/null 2>&1 || { echo "lock-acestep: sha256sum is required" >&2; exit 2; }

root="$(cd "$(dirname "$0")/../.." && pwd)"
dir="$root/engines/acestep"
in="$dir/requirements-$backend.in"
lock="$dir/requirements-$backend.lock"
[[ -f "$in" ]] || { echo "lock-acestep: $in is missing" >&2; exit 2; }

# The shim's tree hash: the "<sha256>  <path>" lines of its files, sorted bytewise, hashed (manifest.TreeSHA256;
# the same rule as repoTreeFile in agent/internal/sync/resolve/pip_wheelhouse.go).
shim_sum="$(cd "$dir" && find . -type f ! -path './tests/*' ! -name '*.lock' ! -name '*.in' ! -name '*.pyc' ! -path '*/__pycache__/*' \
  | sed 's|^\./||' | LC_ALL=C sort | xargs -d '\n' sha256sum | LC_ALL=C sort | sha256sum | cut -d' ' -f1)"

if [[ "$shim_only" == 1 ]]; then
  [[ -f "$lock" ]] || { echo "lock-acestep: $lock does not exist yet (run without --shim-only)" >&2; exit 2; }
  tmp="$(mktemp "$lock.XXXXXX")"
  sed "s|^# repo-tree-sha256: engines/acestep .*|# repo-tree-sha256: engines/acestep $shim_sum|" "$lock" >"$tmp"
  chmod 0644 "$tmp"
  mv "$tmp" "$lock"
  echo "$(sha256sum "$lock" | cut -d' ' -f1)  engines/acestep/requirements-$backend.lock"
  exit 0
fi

tmp="$(mktemp "$lock.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
set +e
python3 -I - "$in" "$tmp" "$shim_sum" "$backend" <<'PY'
import json, re, sys, urllib.error, urllib.parse, urllib.request

src, out, shim_sum, backend = sys.argv[1:5]
PYPI = "https://pypi.org/pypi/{name}/{version}/json"
TORCH = "https://download.pytorch.org/whl/cu128/{name}/"
MAX_GLIBC = (2, 39)
UA = {"User-Agent": "local-ai lock-acestep"}

def norm(n):
    return re.sub(r"[-_.]+", "-", n).lower()

def glibc(plat):
    if plat == "any":
        return (0, 0)
    if not plat.endswith("x86_64"):
        return None
    if plat in ("linux_x86_64",):
        return (0, 1)
    m = re.match(r"manylinux_(\d+)_(\d+)_x86_64$", plat)
    if m:
        return (int(m.group(1)), int(m.group(2)))
    return {"manylinux1_x86_64": (2, 5), "manylinux2010_x86_64": (2, 12), "manylinux2014_x86_64": (2, 17)}.get(plat)

def rank(filename):
    """A sort key for a wheel a linux x86_64 CPython 3.12 installs (higher is better), or None."""
    if not filename.endswith(".whl"):
        return None
    parts = filename[:-4].split("-")
    if len(parts) not in (5, 6):
        return None
    pys, abis, plats = parts[-3].split("."), parts[-2].split("."), parts[-1].split(".")

    def cp_minor(tag):
        m = re.fullmatch(r"cp3(\d+)", tag)
        return int(m.group(1)) if m else None

    best = None
    for py in pys:
        for abi in abis:
            if abi == "cp312":
                ok, a = py == "cp312", 3
            elif abi == "abi3":
                ok, a = cp_minor(py) is not None and cp_minor(py) <= 12, 2
            elif abi == "none":
                ok, a = py in ("py3", "py312", "cp312"), 1
            else:
                ok = False
            if not ok:
                continue
            for plat in plats:
                g = glibc(plat)
                if g is None or g > MAX_GLIBC:
                    continue
                k = (a, g)
                best = k if best is None or k > best else best
    return best

def get(url, accept=None):
    req = urllib.request.Request(url, headers=UA)
    with urllib.request.urlopen(req, timeout=60) as r:
        return r.read()

def from_pypi(name, version):
    data = json.loads(get(PYPI.format(name=urllib.parse.quote(name), version=urllib.parse.quote(version))))
    cands = []
    for u in data.get("urls", []):
        if u.get("packagetype") != "bdist_wheel" or u.get("yanked"):
            continue
        k = rank(u["filename"])
        if k is not None:
            cands.append((k, u["filename"], u["digests"]["sha256"]))
    return cands

def from_torch(name, version):
    page = get(TORCH.format(name=norm(name))).decode("utf-8", "replace")
    cands = []
    for href, text in re.findall(r'<a[^>]*href="([^"]+)"[^>]*>([^<]+)</a>', page):
        fn = urllib.parse.unquote(text.strip())
        m = re.search(r"#sha256=([0-9a-f]{64})", href)
        if not m or not fn.endswith(".whl"):
            continue
        if norm(fn.split("-")[0]) != norm(name) or fn.split("-")[1] != version:
            continue
        k = rank(fn)
        if k is not None:
            cands.append((k, fn, m.group(1)))
    return cands

lines, missing, build_requires = [], [], ""
for raw in open(src, encoding="utf-8"):
    if raw.startswith("# build-requires:"):
        build_requires = raw.strip()
        continue
    s = raw.split("#", 1)[0].strip()
    if not s:
        continue
    name, sep, version = s.partition("==")
    if not sep or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", name) or not version:
        sys.exit(f"lock-acestep: {s!r} is not name==version")
    torch = "+cu" in version
    host = "download.pytorch.org" if torch else "pypi.org"
    try:
        cands = from_torch(name, version) if torch else from_pypi(name, version)
    except (urllib.error.URLError, OSError, ValueError) as exc:
        print(f"lock-acestep: {name}=={version}: {host} could not be read: {exc}", file=sys.stderr)
        missing.append(f"# unresolved: {name}=={version} ({host})")
        continue
    if not cands:
        print(f"lock-acestep: {name}=={version}: no wheel for linux x86_64 CPython 3.12 on {host}", file=sys.stderr)
        missing.append(f"# unresolved: {name}=={version} (no wheel on {host})")
        continue
    cands.sort(reverse=True)
    _, fn, sha = cands[0]
    lines.append(f"{name}=={version} --hash=sha256:{sha}")

with open(out, "w", encoding="utf-8") as f:
    f.write(f"# requirements-{backend}.lock: generated by scripts/release/lock-acestep.sh from requirements-{backend}.in. Do not edit.\n")
    f.write("# One wheel per package (linux x86_64, CPython 3.12), sha256 from the index. Installed offline with\n")
    f.write("# pip install --no-index --find-links <kit wheels> --require-hashes (music.md section 3.1).\n")
    f.write(f"# repo-tree-sha256: engines/acestep {shim_sum}\n")
    if build_requires:
        f.write(build_requires + "\n")
    for m in missing:
        f.write(m + "\n")
    for l in lines:
        f.write(l + "\n")
sys.exit(3 if missing else 0)
PY
rc=$?
set -e
if [[ $rc -ne 0 && $rc -ne 3 ]]; then
  echo "lock-acestep: failed (exit $rc), $lock unchanged" >&2
  exit 1
fi
if [[ $rc -eq 3 && "$allow_missing" != 1 ]]; then
  echo "lock-acestep: some packages are unresolved (see above); $lock unchanged (pass --allow-missing to write it anyway)" >&2
  exit 1
fi
chmod 0644 "$tmp"
mv "$tmp" "$lock"
trap - EXIT
echo "$(sha256sum "$lock" | cut -d' ' -f1)  engines/acestep/requirements-$backend.lock"
[[ $rc -eq 0 ]] || { echo "lock-acestep: written WITH unresolved packages: ark sync refuses it until they are resolved" >&2; exit 3; }
