#!/usr/bin/env bash
# test-redaction.sh: assert scripts/check-redaction.sh and scripts/install-hooks.sh behave.
# Fixtures: scripts/tests/redaction/{fail,pass}-*.txt. Run from anywhere in the repo.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

CHECK=scripts/check-redaction.sh
FIX=scripts/tests/redaction
pass=0; failn=0
ok()   { pass=$((pass + 1)); echo "  ok   $1"; }
bad()  { failn=$((failn + 1)); echo "  FAIL $1"; }
check() { if [[ "$1" == "0" ]]; then ok "$2"; else bad "$2"; fi; }

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
# Hermetic literal inventory used by the inventory-literal fixture; never the owner's real file.
inv="$scratch/inventory.test.yaml"
printf 'vps:\n  hostname: "zephyr-quokka-9931"\n' >"$inv"
no_inv="$scratch/does-not-exist.yaml"

echo "== fixtures =="
for f in "$FIX"/fail-*.txt; do
  name="$(basename "$f")"
  expect="$(grep -vcE '^(#|$)' "$f")"
  export INVENTORY_FILE="$no_inv"; [[ "$name" == fail-inventory-literal.txt ]] && export INVENTORY_FILE="$inv"
  out="$("$CHECK" --file "$f" 2>&1)"; rc=$?
  leaks="$(grep -c '^LEAK ' <<<"$out" || true)"
  [[ $rc -eq 1 && "$leaks" -eq "$expect" ]]; check $? "$name -> exit 1 with $expect LEAK line(s) (got rc=$rc, $leaks)"
  # The matched value must never be echoed (last field of each fixture line).
  leaked=0
  while IFS= read -r line; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    val="${line##* }"
    grep -qF -- "$val" <<<"$out" && leaked=1
  done <"$f"
  [[ $leaked -eq 0 ]]; check $? "$name -> matched values hidden in output"
done
for f in "$FIX"/pass-*.txt; do
  name="$(basename "$f")"
  INVENTORY_FILE="$no_inv" "$CHECK" --file "$f" >/dev/null 2>&1; check $? "$name -> exit 0"
done
unset INVENTORY_FILE

echo "== tree scan excludes the fixture directory =="
INVENTORY_FILE="$no_inv" "$CHECK" >/dev/null 2>&1; check $? "full scan of this repo is clean despite fail-*.txt fixtures"

echo "== staged scan, marker and hook (throwaway repo) =="
repo="$scratch/repo"
mkdir -p "$repo/scripts" && cp scripts/check-redaction.sh scripts/install-hooks.sh "$repo/scripts/"
(
  cd "$repo" || exit 1
  git init -q . && git config user.email t@example.test && git config user.name t
  export INVENTORY_FILE="$no_inv"
  rc=0
  fakeip="100.100.0.$((99))"   # built at runtime so this file itself stays clean
  # 1. staged leak is reported; staged content (not the working tree) is what counts
  printf 'x %s\n' "$fakeip" >leak.md && git add leak.md
  scripts/check-redaction.sh --staged >/dev/null 2>&1; [[ $? -eq 1 ]] || { echo "  FAIL staged leak not detected"; rc=1; }
  printf 'clean\n' >leak.md   # working tree now clean, index still dirty
  scripts/check-redaction.sh --staged >/dev/null 2>&1; [[ $? -eq 1 ]] || { echo "  FAIL scanned the working tree instead of the index"; rc=1; }
  git add leak.md           # now index clean
  scripts/check-redaction.sh --staged >/dev/null 2>&1; [[ $? -eq 0 ]] || { echo "  FAIL clean staged content rejected"; rc=1; }
  git commit -qm init
  # 2. marker only honoured in scripts/tests/redaction/
  mkdir -p scripts/tests/redaction other
  printf 'x %s\n' "$fakeip" >scripts/tests/redaction/fail-x.txt
  printf 'x %s\n' "$fakeip" >other/leak.txt
  : >scripts/tests/redaction/.redaction-fixtures
  : >other/.redaction-fixtures
  out="$(scripts/check-redaction.sh 2>&1)"; [[ $? -eq 1 ]] || { echo "  FAIL stray marker disabled scanning"; rc=1; }
  grep -q 'other/leak.txt' <<<"$out" || { echo "  FAIL other/leak.txt not reported"; rc=1; }
  grep -q 'fail-x.txt' <<<"$out" && { echo "  FAIL fixture dir was scanned despite marker"; rc=1; }
  rm other/leak.txt
  scripts/check-redaction.sh >/dev/null 2>&1; [[ $? -eq 0 ]] || { echo "  FAIL fixture dir not excluded"; rc=1; }
  rm scripts/tests/redaction/.redaction-fixtures
  scripts/check-redaction.sh >/dev/null 2>&1; [[ $? -eq 1 ]] || { echo "  FAIL fixture dir scanned without marker expected"; rc=1; }
  rm -rf scripts/tests other
  # 3. hook install: idempotent, rejects a leaking commit, accepts a clean one
  scripts/install-hooks.sh >/dev/null || { echo "  FAIL install-hooks"; rc=1; }
  second="$(scripts/install-hooks.sh)"; grep -q 'already installed' <<<"$second" || { echo "  FAIL install-hooks not idempotent"; rc=1; }
  [[ -x .git/hooks/pre-commit ]] || { echo "  FAIL hook not executable"; rc=1; }
  printf 'x %s\n' "$fakeip" >t.md && git add t.md
  git commit -qm t >/dev/null 2>&1; [[ $? -ne 0 ]] || { echo "  FAIL leaking commit was accepted"; rc=1; }
  printf 'fine\n' >t.md && git add t.md
  git commit -qm t >/dev/null 2>&1; [[ $? -eq 0 ]] || { echo "  FAIL clean commit rejected"; rc=1; }
  # 4. a foreign hook is backed up, not lost
  printf '#!/bin/sh\nexit 0\n' >.git/hooks/pre-commit
  scripts/install-hooks.sh >/dev/null; [[ -f .git/hooks/pre-commit.bak ]] || { echo "  FAIL foreign hook not backed up"; rc=1; }
  exit $rc
)
check $? "staged scan reads the index; marker scoped; hook installs, is idempotent, blocks leaks, backs up foreign hooks"

echo
echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
