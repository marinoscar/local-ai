#!/usr/bin/env bash
# test-init-inventory.sh: scripts/init-inventory.sh in a throwaway repo.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"
pass=0; failn=0
t() { local name="$1" want="$2" frag="$3"; shift 3; local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -8; fi; }
w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT; r="$w/repo"
mkdir -p "$r/scripts"; cp scripts/init-inventory.sh scripts/check-redaction.sh "$r/scripts/"; cp inventory.example.yaml .gitignore "$r/"
cd "$r" && git init -q . && git config user.email t@example.test && git config user.name t && git add -A && git commit -qm init
S=scripts/init-inventory.sh

t "--check fails when the file is missing" 1 "does not exist" $S --check
t "creates the file from the example" 0 "created inventory.local.yaml" $S
t "created with mode 0600" 0 "" test "$(stat -c %a inventory.local.yaml)" = 600
echo "# MARKER-keep-me" >>inventory.local.yaml
t "second run never overwrites" 0 "inventory ready" $S
t "…and the content is intact" 0 "" grep -q MARKER-keep-me inventory.local.yaml
t "--check fails while placeholders remain" 1 "still hold a <PLACEHOLDER>" $S --check
fake_email="owner$(printf '\100')zeta-example.net"   # built at runtime so this file stays clean
sed -i -e "s|<ADMIN_EMAIL>|$fake_email|" -e 's|"<TAILNET>"|"pangolin-net"|' -e 's|<ROUTE53_ZONE_ID>|ZQUOKKA12345|' -e 's|<VPS_HOSTNAME>|vps-quokka.zeta-example.net|' -e 's|<VPS_PUBLIC_IP>|203.0.113.45|' -e 's|<TAILNET_IP_[A-Z0-9_]*>|198.51.100.7|' -e 's|<LAN_IP_[A-Z0-9_]*>|10.9.8.7|' inventory.local.yaml
t "--check passes when filled (fs_uuid may stay a placeholder)" 0 "inventory ready" $S --check
out="$($S --check 2>&1)"
t "real values are never printed" 0 "" bash -c '! grep -qE "pangolin-net|ZQUOKKA12345|203\.0\.113|198\.51\.100|zeta-example" <<<"$1"' _ "$out"
chmod 644 inventory.local.yaml
t "--check fails on loose permissions" 1 "chmod 600" $S --check
t "default run repairs the mode" 0 "mode fixed to 0600" $S
echo "this repo mentions pangolin-net" >notes.md
t "a real value in a tracked file fails the literal scan" 1 "check-redaction FAILS" $S --check
rm notes.md
: >.gitignore
t "--check fails when git does not ignore the file" 1 "does NOT ignore" $S --check

echo; echo "passed: $pass  failed: $failn"; [[ $failn -eq 0 ]]
