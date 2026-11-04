#!/usr/bin/env bash
# check-no-logind-restart.sh: fail if installers/ or agent/ contains a command that restarts, reloads, stops or
# signals systemd-logind (installer.md section 4.3 P4 step 1, INST-03). Restarting logind froze the login on the
# reference laptop and needed a hard power-off; the lid settings apply after a reboot instead.
#
# usage: check-no-logind-restart.sh [root]     (root defaults to the repository root; tests pass a fixture)
set -uo pipefail
root="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
dirs=()
for d in installers agent; do [[ -d "$root/$d" ]] && dirs+=("$root/$d"); done
((${#dirs[@]} > 0)) || { echo "check-no-logind-restart: nothing to scan under $root"; exit 0; }

# restart|try-restart|reload|stop|kill|signal words followed (anywhere later on the line) by systemd-logind,
# and the process killers aimed at it.
pattern='((try-)?restart|reload(-or-restart)?|stop)[[:space:]]+(-[^ ]+[[:space:]]+)*systemd-logind|kill[^#]*systemd-logind|(pkill|killall)[^#]*logind'
hits="$(grep -rnE --exclude-dir=vendor --exclude-dir=node_modules --exclude-dir=testdata --exclude=*_test.go -- "$pattern" "${dirs[@]}" 2>/dev/null || true)"
if [[ -n "$hits" ]]; then
  echo "LAI-INS-040: these lines would restart, reload or signal systemd-logind. Never do that; reboot instead:" >&2
  sed "s|^$root/||" <<<"$hits" >&2
  exit 1
fi
echo "check-no-logind-restart: ok"
