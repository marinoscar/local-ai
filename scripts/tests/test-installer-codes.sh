#!/usr/bin/env bash
# test-installer-codes.sh: scripts/check-installer-codes.sh (issue #436). Each case copies the real codes.go and
# installers/install-node.sh into a throwaway root, changes one thing, and checks the verdict. Offline.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2
CHECK="$PWD/scripts/check-installer-codes.sh"
pass=0; failn=0
w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT

t() { # t <name> <want-exit> <fragment|""> cmd...
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -15; fi
}
# mkroot <name>: a fresh copy of the two files the check reads.
mkroot() {
  local r="$w/$1"
  mkdir -p "$r/agent/internal/doctor" "$r/installers"
  cp agent/internal/doctor/codes.go "$r/agent/internal/doctor/"
  cp installers/install-node.sh "$r/installers/"
  printf '%s' "$r"
}

t "the repository is in sync" 0 "check-installer-codes: ok (" "$CHECK"

r="$(mkroot spanish-changed)"
sed -i 's/ES: "Menos de 8 GB de RAM"/ES: "Menos de 8 GB de memoria"/' "$r/agent/internal/doctor/codes.go"
t "a Spanish title changed in codes.go is drift" 1 "differ from agent/internal/doctor/codes.go" "$CHECK" --root "$r"
t "...the diff names the new text" 1 "Menos de 8 GB de memoria" "$CHECK" --root "$r"
t "--write regenerates the block" 0 "wrote the LAI-INS block" "$CHECK" --root "$r" --write
t "...after which the check passes" 0 "check-installer-codes: ok" "$CHECK" --root "$r"
t "...and the installer carries the new text" 0 "" grep -qF "'Menos de 8 GB de memoria'" "$r/installers/install-node.sh"
t "...and stays executable" 0 "" test -x "$r/installers/install-node.sh"

r="$(mkroot hand-edit)"
sed -i "s/'Wait for it to finish.'/'Wait.'/" "$r/installers/install-node.sh"
t "a hand edit of the generated block is drift" 1 "never edit the generated block by hand" "$CHECK" --root "$r"

r="$(mkroot english-fix)"
sed -i 's/Fix:      Msg{EN: "Reboot when convenient."/Fix:      Msg{EN: "Reboot now."/' "$r/agent/internal/doctor/codes.go"
t "an English fix changed in codes.go is drift" 1 "Reboot now." "$CHECK" --root "$r"

r="$(mkroot unknown-code)"
printf '\n# LAI-INS-099 is not a real code\n' >>"$r/installers/install-node.sh"
t "an installer code with no codes.go entry fails" 1 "LAI-INS-099 is used but has no entry in codes.go" "$CHECK" --root "$r"

r="$(mkroot no-block)"
sed -i '/^# BEGIN GENERATED ins-codes/d' "$r/installers/install-node.sh"
t "a missing block fails" 1 "must hold exactly one" "$CHECK" --root "$r"

r="$(mkroot bad-escape)"
sed -i 's/ES: "Espere a que termine."/ES: "Espere\\ta que termine."/' "$r/agent/internal/doctor/codes.go"
t "an escape the generator cannot copy is refused" 2 'unsupported escape' "$CHECK" --root "$r"

t "unknown argument: exit 2" 2 "usage:" "$CHECK" --frob

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
