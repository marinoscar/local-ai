#!/usr/bin/env bash
# init-inventory.sh: create and verify the private inventory.local.yaml (owner machines and the ark drive).
# Why: the public docs use placeholders; the real values live only in this git-ignored file, and
# scripts/check-redaction.sh scans for its literal values (docs/architecture/security-model.md section 5).
#
#   scripts/init-inventory.sh           create it from inventory.example.yaml if missing (never overwrites),
#                                       set mode 0600, then run the checks below as a report
#   scripts/init-inventory.sh --check   verify only; exit 1 if anything is wrong (use after filling it in)
#
# Checks: the file exists; git ignores it; only the owner can read it (0600); no secret field still holds a
# <PLACEHOLDER> (fs_uuid is allowed until an AI-ARK drive exists); scripts/check-redaction.sh passes with the
# literal scan active. Values are never printed.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

FILE="${INVENTORY_FILE:-inventory.local.yaml}"
EXAMPLE="inventory.example.yaml"
check_only=0
case "${1:-}" in
  "") ;;
  --check) check_only=1 ;;
  -h|--help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *) echo "usage: $0 [--check]" >&2; exit 2 ;;
esac

problems=0
bad()  { echo "  ✗ $1"; problems=$((problems + 1)); }
good() { echo "  ✓ $1"; }

if [[ ! -f "$FILE" ]]; then
  if [[ $check_only -eq 1 ]]; then
    bad "$FILE does not exist. Run scripts/init-inventory.sh to create it."
    echo "✗ inventory not ready ($problems problem)."; exit 1
  fi
  [[ -f "$EXAMPLE" ]] || { echo "init-inventory: $EXAMPLE not found" >&2; exit 2; }
  (umask 077; cp "$EXAMPLE" "$FILE")
  echo "• created $FILE from $EXAMPLE (mode 0600)."
  echo "  Next: open it and replace every <PLACEHOLDER> with the real value (tailscale status shows the IPs)."
  echo "  Then run: scripts/init-inventory.sh --check"
fi

echo "== $FILE"
[[ -f "$FILE" ]] && good "exists"

if git check-ignore -q -- "$FILE"; then good "git ignores it"; else bad "git does NOT ignore $FILE: add it to .gitignore before anything else"; fi

mode="$(stat -c '%a' "$FILE")"
if [[ "$mode" == "600" ]]; then good "mode 0600"
elif [[ $check_only -eq 0 ]]; then chmod 600 "$FILE"; good "mode fixed to 0600 (was $mode)"
else bad "mode is $mode; run: chmod 600 $FILE"; fi

# Secret fields must be filled in. Field names: secret_fields in inventory.example.yaml.
fields='owner_email|tailnet|route53_zone_id|hostname|public_ip|tailnet_ip|lan_ip'
left="$(grep -nE "^[[:space:]]*-?[[:space:]]*($fields):[[:space:]]*\"?<" "$FILE" | cut -d: -f1 | tr '\n' ' ' || true)"
if [[ -z "${left// /}" ]]; then good "no secret field still holds a placeholder"
else
  n="$(wc -w <<<"$left")"
  if [[ $check_only -eq 1 ]]; then bad "$n secret field(s) still hold a <PLACEHOLDER> (lines: ${left% })"
  else echo "  • $n secret field(s) still hold a <PLACEHOLDER> (lines: ${left% }). Fill them in."; fi
fi
if grep -qE '^[[:space:]]*fs_uuid:[[:space:]]*"?<' "$FILE"; then echo "  • fs_uuid is still a placeholder: fine until an AI-ARK drive exists."; fi

if INVENTORY_FILE="$FILE" scripts/check-redaction.sh >/dev/null 2>&1; then good "check-redaction passes with the literal scan active"
else bad "check-redaction FAILS: a real value is in a tracked file. Run scripts/check-redaction.sh to see where (values are hidden)."; fi

echo "• Once an AI-ARK drive exists, copy this file to the drive root with mode 0600 (it must never leave your machines otherwise)."
if [[ $problems -gt 0 ]]; then echo "✗ inventory not ready ($problems problem(s))."; [[ $check_only -eq 1 ]] && exit 1; else echo "✓ inventory ready."; fi
