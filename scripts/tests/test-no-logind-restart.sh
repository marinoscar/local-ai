#!/usr/bin/env bash
# test-no-logind-restart.sh: tests scripts/check-no-logind-restart.sh against throwaway fixture trees.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2
CHK="$PWD/scripts/check-no-logind-restart.sh"
w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT
pass=0; failn=0
t() { # t <name> <want-exit> <file-content-line>
  local name="$1" want="$2" line="$3" d
  d="$w/$name"
  mkdir -p "$d/installers" "$d/agent/vendor/x"
  printf '%s\n' "$line" >"$d/installers/x.sh"
  local rc=0; "$CHK" "$d" >/dev/null 2>&1 || rc=$?
  if [[ $rc -eq $want ]]; then pass=$((pass + 1)); echo "  ok   $name"; else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want)"; fi
}
t restart 1 'systemctl restart systemd-logind'
t restart-service 1 'systemctl restart systemd-logind.service'
t try-restart 1 'systemctl try-restart systemd-logind'
t reload 1 'systemctl reload systemd-logind'
t kill-hup 1 'systemctl kill -s HUP systemd-logind'
t kill-bare 1 'kill -HUP $(pidof systemd-logind)'
t pkill 1 'pkill -HUP systemd-logind'
t clean 0 'systemctl mask sleep.target'
t mention-ok 0 '# never touch logind; reboot instead'
t status-ok 0 'systemctl status systemd-logind'
# vendor and test files are not scanned
echo 'systemctl restart systemd-logind' >"$w/clean/agent/vendor/x/y.sh"
echo 'x := "systemctl restart systemd-logind"' >"$w/clean/agent/z_test.go"
rc=0; "$CHK" "$w/clean" >/dev/null 2>&1 || rc=$?
if [[ $rc -eq 0 ]]; then pass=$((pass + 1)); echo "  ok   vendor and _test.go are skipped"; else failn=$((failn + 1)); echo "  FAIL vendor/_test skip"; fi
rc=0; "$CHK" >/dev/null 2>&1 || rc=$?
if [[ $rc -eq 0 ]]; then pass=$((pass + 1)); echo "  ok   the repository itself is clean"; else failn=$((failn + 1)); echo "  FAIL repository scan"; fi
echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
