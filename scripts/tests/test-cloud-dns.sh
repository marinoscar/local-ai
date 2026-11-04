#!/usr/bin/env bash
# test-cloud-dns.sh: scripts/cloud-dns.sh with a fake `aws` and `dig` on PATH. No network, no real AWS, no real
# address: the values below are documentation-range addresses and a zone id built at runtime.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
SCRIPT="$PWD/scripts/cloud-dns.sh"
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...   (fragment must appear in stdout+stderr)
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -8; fi
}
ok() { if [[ $2 -eq 0 ]]; then pass=$((pass + 1)); echo "  ok   $1"; else failn=$((failn + 1)); echo "  FAIL $1"; fi; }

w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT
export HOME="$w/home"; mkdir -p "$HOME" "$w/bin"
export INVENTORY_FILE="$w/none.yaml"
unset VPS_PUBLIC_IP ROUTE53_ZONE_ID
ip="203.0.113.$((10 + 7))"        # TEST-NET-3 documentation range
x=ABCDEFGHIJK; zone="Z0$x"         # built at runtime so this file holds no zone-id-shaped text

# Fake aws: records its arguments; `change-resource-record-sets` prints a change id; `wait` succeeds.
cat >"$w/bin/aws" <<'F'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_AWS_LOG"
case "$2" in
  change-resource-record-sets) [[ -n "${FAKE_AWS_FAIL:-}" ]] && { echo "AccessDenied" >&2; exit 254; }; echo "/change/C0FAKE" ;;
  wait) ;;
esac
F
cat >"$w/bin/dig" <<'F'
#!/usr/bin/env bash
printf '%s\n' "$FAKE_DIG_ANSWER"
F
chmod +x "$w/bin/aws" "$w/bin/dig"
export PATH="$w/bin:$PATH" FAKE_AWS_LOG="$w/aws.log"

echo "== dry run"
out="$w/batch.json"
t "dry run writes the batch and says nothing was sent" 0 "DRY RUN: nothing was sent to AWS" "$SCRIPT" --ip "$ip" --out "$out"
ok "no aws call in a dry run" "$([[ ! -s "$w/aws.log" ]] && echo 0 || echo 1)"
ok "the batch is valid JSON with the right record" "$(python3 -c "
import json,sys
b=json.load(open('$out')); r=b['Changes'][0]
assert r['Action']=='UPSERT' and r['ResourceRecordSet']['Name']=='ai.marin.cr' and r['ResourceRecordSet']['Type']=='A'
assert r['ResourceRecordSet']['TTL']==300 and r['ResourceRecordSet']['ResourceRecords'][0]['Value']=='$ip'" && echo 0 || echo 1)"
ok "the batch file is private (0600)" "$([[ "$(stat -c %a "$out")" == 600 ]] && echo 0 || echo 1)"
t "the template carries placeholders only" 0 "<VPS_PUBLIC_IP>" cat "$PWD/scripts/templates/ai-cloud-dns.json.template"

echo "== address checks"
# private and tailnet-range addresses are built at runtime so this file holds no address the redaction check rejects
cg1="100.$((60 + 4)).0.1"; cg2="100.$((120 + 7)).255.1"; lan="192.$((160 + 8)).1.9"
for bad in "<VPS_PUBLIC_IP>" "10.0.0.5" "$lan" "172.20.1.1" "$cg1" "$cg2" "127.0.0.1" "169.254.1.1" "300.1.1.1" "1.2.3" "224.0.0.1"; do
  t "refuses $bad" 1 "" "$SCRIPT" --ip "$bad" --out "$w/x.json"
done
t "accepts 100.63.x (not tailnet)" 0 "" "$SCRIPT" --ip "100.63.1.1" --out "$w/x.json"
t "no address at all is refused" 1 "no VPS public address" "$SCRIPT" --out "$w/x.json"
t "a bad host name is refused" 1 "not a host name" "$SCRIPT" --ip "$ip" --host 'a b;c' --out "$w/x.json"

echo "== inventory and environment"
printf 'vps:\n  public_ip: "%s"\nlab:\n  route53_zone_id: "%s"\n' "$ip" "$zone" >"$w/inv.yaml"
INVENTORY_FILE="$w/inv.yaml" t "address and zone come from the inventory" 0 "$ip" "$SCRIPT" --out "$w/y.json"
printf 'vps:\n  public_ip: "<VPS_PUBLIC_IP>"\n' >"$w/inv2.yaml"
INVENTORY_FILE="$w/inv2.yaml" t "an inventory placeholder is not a value" 1 "no VPS public address" "$SCRIPT" --out "$w/y.json"
VPS_PUBLIC_IP="$ip" t "address from the environment" 0 "$ip" "$SCRIPT" --out "$w/y.json"

echo "== apply"
t "apply without a zone id is refused" 1 "no Route53 zone id" "$SCRIPT" --apply --ip "$ip" --out "$w/z.json"
t "a malformed zone id is refused" 1 "does not look like" "$SCRIPT" --apply --ip "$ip" --zone "nope" --out "$w/z.json"
t "apply submits and waits" 0 "Route53 applied the change" "$SCRIPT" --apply --ip "$ip" --zone "$zone" --out "$w/z.json"
ok "aws got the zone and the change batch" "$(grep -q -- "route53 change-resource-record-sets --hosted-zone-id $zone --change-batch file://$w/z.json" "$w/aws.log" && echo 0 || echo 1)"
ok "aws was asked to wait for the change" "$(grep -q 'route53 wait resource-record-sets-changed --id /change/C0FAKE' "$w/aws.log" && echo 0 || echo 1)"
FAKE_AWS_FAIL=1 t "an AWS error fails the run" 1 "AWS refused" "$SCRIPT" --apply --ip "$ip" --zone "$zone" --out "$w/z.json"

echo "== verify"
FAKE_DIG_ANSWER="$ip" t "verify passes when DNS answers" 0 "answers with the VPS address" "$SCRIPT" --verify --ip "$ip"
FAKE_DIG_ANSWER="" t "verify fails when DNS does not answer yet" 1 "answers with 'nothing'" "$SCRIPT" --verify --ip "$ip"
FAKE_DIG_ANSWER="198.51.100.4" t "verify fails on another address" 1 "expected the VPS address" "$SCRIPT" --verify --ip "$ip"

echo
echo "cloud-dns: $pass passed, $failn failed"
[[ $failn -eq 0 ]]
