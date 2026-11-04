#!/usr/bin/env bash
# cloud-dns.sh: create or update the Route53 "A" record of the cloud app (ai.marin.cr -> the VPS public address).
# The owner runs this on their own machine; it is a DRY RUN unless you pass --apply.
# docs/runbooks/10-lab-operations.md Part 9; docs/specs/cloud-app.md section 3.13.
#
# Usage:
#   scripts/cloud-dns.sh                 # dry run: write the change batch, print what --apply would run (no AWS call)
#   scripts/cloud-dns.sh --apply         # run `aws route53 change-resource-record-sets` and wait for it
#   scripts/cloud-dns.sh --verify        # check that DNS answers with the VPS address (uses dig; no AWS call)
#   Options: --ip ADDR  --zone ID  --host NAME (default ai.marin.cr)  --out FILE (default ~/ai-cloud-dns.json)
#
# The address and the zone id are read, in this order, from the options, from VPS_PUBLIC_IP / ROUTE53_ZONE_ID,
# and from the git-ignored inventory.local.yaml (fields public_ip, route53_zone_id). Real values are never
# written into the repository: the batch file goes outside it (mode 0600) and the template holds placeholders.
# Exit: 0 ok, 1 failed, 2 usage error.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="$REPO/scripts/templates/ai-cloud-dns.json.template"
HOST="ai.marin.cr"
IP="${VPS_PUBLIC_IP:-}"
ZONE="${ROUTE53_ZONE_ID:-}"
OUT="${HOME:-/tmp}/ai-cloud-dns.json"
INVENTORY="${INVENTORY_FILE:-$REPO/inventory.local.yaml}"
mode=dry

die() { echo "cloud-dns: $*" >&2; exit 1; }
usage() { sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) mode=apply; shift ;;
    --verify) mode=verify; shift ;;
    --ip) IP="${2:?--ip needs an address}"; shift 2 ;;
    --zone) ZONE="${2:?--zone needs a zone id}"; shift 2 ;;
    --host) HOST="${2:?--host needs a name}"; shift 2 ;;
    --out) OUT="${2:?--out needs a file}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

# inv_value <field>: the first value of a field in the owner's inventory, or nothing. Placeholders (<...>) are not values.
inv_value() {
  [[ -f "$INVENTORY" ]] || return 0
  grep -E "^[[:space:]]*-?[[:space:]]*$1:" "$INVENTORY" | head -1 \
    | sed -E 's/^[^:]+:[[:space:]]*"?([^"#]*)"?.*$/\1/; s/[[:space:]]*$//' | grep -v '^<' || true
}
[[ -n "$IP" ]] || IP="$(inv_value public_ip)"
[[ -n "$ZONE" ]] || ZONE="$(inv_value route53_zone_id)"

[[ "$HOST" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$ ]] || die "'$HOST' is not a host name"
[[ -n "$IP" ]] || die "no VPS public address: pass --ip, set VPS_PUBLIC_IP, or fill public_ip in inventory.local.yaml"

# The address must be a plain, public IPv4 address: never a placeholder, a LAN address, a tailnet (100.64/10)
# address or loopback. A wrong address here would point the whole cloud at the wrong machine.
[[ "$IP" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]] || die "'$IP' is not an IPv4 address (still a <PLACEHOLDER>?)"
o1=$((10#${BASH_REMATCH[1]})); o2=$((10#${BASH_REMATCH[2]})); o3=$((10#${BASH_REMATCH[3]})); o4=$((10#${BASH_REMATCH[4]}))
(( o1 <= 255 && o2 <= 255 && o3 <= 255 && o4 <= 255 )) || die "'$IP' is not an IPv4 address (a part is above 255)"
if (( o1 == 0 || o1 == 10 || o1 == 127 || o1 >= 224 )) || (( o1 == 100 && o2 >= 64 && o2 <= 127 )) \
   || (( o1 == 169 && o2 == 254 )) || (( o1 == 172 && o2 >= 16 && o2 <= 31 )) || (( o1 == 192 && o2 == 168 )); then
  die "'$IP' is not a public address (private, tailnet, loopback or reserved range)"
fi

if [[ $mode == verify ]]; then
  command -v dig >/dev/null 2>&1 || die "dig is not installed (Ubuntu: sudo apt install dnsutils)"
  got="$(dig +short "$HOST" A | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
  if [[ "$got" == "$IP" ]]; then echo "✓ $HOST answers with the VPS address."; exit 0; fi
  echo "✗ $HOST answers with '${got:-nothing}', expected the VPS address. DNS can take a few minutes (TTL 300): wait and run again." >&2
  exit 1
fi

[[ -f "$TEMPLATE" ]] || die "missing $TEMPLATE"
umask 077
mkdir -p "$(dirname "$OUT")"
sed -e "s|<CLOUD_HOST>|$HOST|g" -e "s|<VPS_PUBLIC_IP>|$IP|g" "$TEMPLATE" >"$OUT"
chmod 600 "$OUT"
if grep -q '<[A-Z_]*>' "$OUT"; then die "the change batch still has a placeholder: $OUT"; fi
echo "Change batch written to $OUT (mode 0600; contains your real address, keep it out of the repository):"
sed 's/^/    /' "$OUT"

if [[ $mode == dry ]]; then
  echo
  echo "DRY RUN: nothing was sent to AWS. To create or update the record, run:"
  echo "    scripts/cloud-dns.sh --apply"
  echo "which runs:"
  echo "    aws route53 change-resource-record-sets --hosted-zone-id <ROUTE53_ZONE_ID> --change-batch file://$OUT"
  [[ -n "$ZONE" ]] || echo "(no zone id found yet: pass --zone, set ROUTE53_ZONE_ID, or fill route53_zone_id in inventory.local.yaml)"
  exit 0
fi

[[ -n "$ZONE" ]] || die "no Route53 zone id: pass --zone, set ROUTE53_ZONE_ID, or fill route53_zone_id in inventory.local.yaml"
[[ "$ZONE" =~ ^Z[0-9A-Z]{8,32}$ ]] || die "'$ZONE' does not look like a Route53 zone id (it starts with Z)"
command -v aws >/dev/null 2>&1 || die "the aws command is not installed or not on PATH"
id="$(aws route53 change-resource-record-sets --hosted-zone-id "$ZONE" --change-batch "file://$OUT" \
      --query 'ChangeInfo.Id' --output text)" || die "AWS refused the change (check your credentials and the zone id)"
echo "✓ Change submitted ($id). Waiting for Route53 to apply it (up to a minute)..."
aws route53 wait resource-record-sets-changed --id "$id" || die "the change did not finish; check it in the Route53 console"
echo "✓ Route53 applied the change. Now run: scripts/cloud-dns.sh --verify"
