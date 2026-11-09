#!/usr/bin/env bash
# check-deb-installs.sh: prove that apt can install a deb on every distribution a node may run, not only on the CI
# runner's (docs/specs/desktop.md section 4.11). It runs `apt-get install --dry-run ./<deb>` in a clean container of each
# image, so a dependency that one release renamed or dropped (policykit-1, gone from Ubuntu 25.04 and later, issue #129)
# fails CI instead of failing `sudo apt install` on a node.
#
#   check-deb-installs.sh <deb> [image ...]
#
# Images default to the Ubuntu releases a node may run (docs/specs/installer.md section 3.3: 24.04 and 26.04). Needs
# docker and network access to the image registry and the apt mirrors (CI only; nodes never run this). Prints one
# `ok`/`FAIL` line per image and apt's reason for a failure. An image whose package lists cannot be downloaded is an
# environment error, never reported as an uninstallable deb.
#
# Exit: 0 installable everywhere, 1 not installable on at least one image, 2 usage or environment error.
set -euo pipefail

DEFAULT_IMAGES=(ubuntu:24.04 ubuntu:26.04)

usage() { sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
[[ $# -ge 1 ]] || usage
[[ "$1" != "-h" && "$1" != "--help" ]] || usage
deb="$1"; shift
[[ -f "$deb" && "$deb" == *.deb ]] || { echo "check-deb-installs: $deb is not a .deb file" >&2; exit 2; }
command -v docker >/dev/null || { echo "check-deb-installs: docker is not installed" >&2; exit 2; }
images=("$@"); [[ ${#images[@]} -gt 0 ]] || images=("${DEFAULT_IMAGES[@]}")

dir="$(cd "$(dirname "$deb")" && pwd)"; name="$(basename "$deb")"
failed=0; envfail=0
for image in "${images[@]}"; do
  # Exit 3 from inside the container: apt has no package lists (mirror unreachable), so nothing was proved either way.
  script="apt-get update -qq >/dev/null 2>&1; ls /var/lib/apt/lists/ | grep -q _Packages || exit 3; apt-get install -y --dry-run /deb/$name"
  rc=0; out="$(docker run --rm --network host -e DEBIAN_FRONTEND=noninteractive -v "$dir:/deb:ro" "$image" sh -c "$script" 2>&1)" || rc=$?
  if [[ $rc -eq 0 ]]; then
    echo "ok   $image: $name installs"
  elif [[ $rc -eq 3 ]]; then
    echo "ERROR $image: apt could not download package lists (mirror unreachable); nothing was checked"
    envfail=1
  else
    echo "FAIL $image: apt cannot install $name"
    printf '%s\n' "$out" | grep -E -A6 'Unsatisfied|unmet|Depends|E: |Error' | sed 's/^/     /' | head -n 20
    failed=1
  fi
done
[[ $failed -eq 0 ]] || exit 1
[[ $envfail -eq 0 ]] || exit 2
exit 0
