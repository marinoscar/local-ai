#!/usr/bin/env bash
# test-release-sign.sh: scripts/release-sign.sh with throwaway key pairs generated here (never committed).
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"
SIGN="$PWD/scripts/release-sign.sh"
if ! command -v minisign >/dev/null 2>&1; then echo "minisign not installed: skipping release-sign tests"; exit 0; fi
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -8; fi
}
w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT
export TMPDIR="$w/tmp"; mkdir -p "$TMPDIR"
gen() { printf '%s\n%s\n' "$3" "$3" | minisign -G -p "$1" -s "$2" -f >/dev/null 2>&1; }
gen "$w/a.pub" "$w/a.key" "pw-alpha-test"
gen "$w/b.pub" "$w/b.key" "pw-bravo-test"
rel="$w/rel"; mkdir -p "$rel"; printf 'one\n' >"$rel/aiagent-linux-arm64"; printf 'two\n' >"$rel/aiagent-linux-amd64"
export RELEASE_TAG=node-v0.0.0-test
signA() { MINISIGN_SECRET_KEY="$(cat "$w/a.key")" MINISIGN_PASSWORD="pw-alpha-test" "$SIGN" sign "$rel"; }

echo "== sums"
t "writes sorted '<sha256>  <name>' lines" 0 "wrote" "$SIGN" sums "$rel" aiagent-linux-arm64 aiagent-linux-amd64
t "format and order" 0 "" bash -c "head -1 '$rel/SHA256SUMS' | grep -qE '^[0-9a-f]{64}  aiagent-linux-amd64\$' && tail -1 '$rel/SHA256SUMS' | grep -qE '^[0-9a-f]{64}  aiagent-linux-arm64\$' && [ \$(wc -l <'$rel/SHA256SUMS') = 2 ]"
t "refuses a path outside the dir" 2 "directly inside" "$SIGN" sums "$rel" ../x
t "a failed sums leaves the previous SHA256SUMS intact" 0 "" test -s "$rel/SHA256SUMS"

echo "== sign and verify"
out="$(signA 2>&1)"; rc=$?
t "sign succeeds" 0 "" test $rc -eq 0
t "secrets never printed" 0 "" bash -c '! grep -qE "pw-alpha-test|RWR|untrusted comment" <<<"$1"' _ "$out"
t "no temp key file left behind" 0 "" test -z "$(ls -A "$TMPDIR")"
t "verify ok with the right key" 0 "signature valid" "$SIGN" verify "$rel" "$w/a.pub"
t "verify fails with another key" 1 "SIGNATURE INVALID" "$SIGN" verify "$rel" "$w/b.pub"

echo "== tampering"
cp "$rel/aiagent-linux-amd64" "$w/keep"; printf 'evil\n' >"$rel/aiagent-linux-amd64"
t "changed binary -> checksum failure" 1 "does not match" "$SIGN" verify "$rel" "$w/a.pub"
cp "$w/keep" "$rel/aiagent-linux-amd64"
cp "$rel/SHA256SUMS" "$w/sums.keep"; sed -i 's/^[0-9a-f]\{4\}/0000/' "$rel/SHA256SUMS"
t "changed SHA256SUMS -> signature failure" 1 "SIGNATURE INVALID" "$SIGN" verify "$rel" "$w/a.pub"
cp "$w/sums.keep" "$rel/SHA256SUMS"
t "restored files verify again" 0 "signature valid" "$SIGN" verify "$rel" "$w/a.pub"

echo "== failures are safe"
bad="$(MINISIGN_SECRET_KEY="$(cat "$w/a.key")" MINISIGN_PASSWORD="wrong-password-xyz" "$SIGN" sign "$rel" 2>&1)"; rc=$?
t "wrong password -> exit 1" 0 "" test $rc -eq 1
t "no signature file left after a failed sign" 0 "" test ! -e "$rel/SHA256SUMS.minisig"
t "wrong password is not echoed" 0 "" bash -c '! grep -q "wrong-password-xyz" <<<"$1"' _ "$bad"
t "missing secret key -> exit 2" 2 "MINISIGN_SECRET_KEY is not set" env -u MINISIGN_SECRET_KEY MINISIGN_PASSWORD=x "$SIGN" sign "$rel"
t "missing password -> exit 2" 2 "MINISIGN_PASSWORD is not set" env -u MINISIGN_PASSWORD MINISIGN_SECRET_KEY=x "$SIGN" sign "$rel"
t "missing public key explains the owner action" 2 "Owner action pending" "$SIGN" verify "$rel" "$w/nope.pub"

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
