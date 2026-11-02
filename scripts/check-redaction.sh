#!/usr/bin/env bash
# check-redaction.sh: fail if tracked (or staged) files contain lab inventory or secrets.
# Rules: docs/architecture/security-model.md §5. Used by the pre-commit hook and CI.
#
# Usage: scripts/check-redaction.sh            # scan tracked + untracked (non-ignored) files
#        scripts/check-redaction.sh --staged   # scan staged content only (pre-commit)
#        scripts/check-redaction.sh --file F   # scan one file, even a test fixture (used by tests)
#
# Exit: 0 clean, 1 leak found, 2 usage error.
# Output never contains the matched value: only `LEAK <file>:<line>: ...`.
#
# Env: INVENTORY_FILE overrides ./inventory.local.yaml (used by tests).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

FIXTURE_DIR="scripts/tests/redaction"
INVENTORY_FILE="${INVENTORY_FILE:-inventory.local.yaml}"

mode=tree
target=""
case "${1:-}" in
  "") ;;
  --staged) mode=staged ;;
  --file) mode=file; target="${2:-}"; [[ -n "$target" ]] || { echo "usage: $0 --file <path>" >&2; exit 2; } ;;
  -h|--help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  *) echo "usage: $0 [--staged | --file <path>]" >&2; exit 2 ;;
esac

case "$mode" in
  staged) mapfile -t files < <(git diff --cached --name-only --diff-filter=ACM) ;;
  file)   files=("$target") ;;
  *)      mapfile -t files < <(git ls-files --cached --others --exclude-standard) ;;
esac

# Never scan these: they define the patterns themselves.
skip='^(scripts/check-redaction\.sh|\.gitleaks\.toml)$'

# Directory of must-fail fixtures: skipped in tree/staged scans, but only when the
# marker file is present, and only for this one directory (a stray marker elsewhere
# must never disable scanning).
fixtures_active=0
[[ -f "$FIXTURE_DIR/.redaction-fixtures" ]] && fixtures_active=1

# Extended-regex patterns (security-model.md §5).
patterns=(
  '\b100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]{1,3}\.[0-9]{1,3}\b'   # tailnet IPs (CGNAT 100.64/10)
  '\btail[0-9a-f]{6}\b'                                                         # tailnet name
  '\bZ0[0-9A-Z]{10,}\b'                                                         # Route53 zone id
  '\b(lat|nod|lai|pat)_[A-Za-z0-9_-]{20,}'                                      # tokens (word boundary: localai_* is fine)
  '\b192\.168\.[0-9]{1,3}\.[0-9]{1,3}\b'                                        # LAN IPs
  '\b[0-9a-fA-F]{64}\b'                                                         # 64-hex strings (keys, hashes)
)
email_re='[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
email_ok='(noreply@.*|[^@]+@example\.test)'   # allowed: noreply@..., @example.test placeholders

# Literal values from the local (git-ignored) inventory, if present.
literals=()
if [[ -f "$INVENTORY_FILE" ]]; then
  while IFS= read -r v; do
    [[ -n "$v" && "$v" != \<* && ${#v} -ge 6 ]] && literals+=("$v")
  done < <(grep -E '^\s*-?\s*(owner_email|tailnet|route53_zone_id|hostname|public_ip|tailnet_ip|lan_ip|fs_uuid):' "$INVENTORY_FILE" \
           | sed -E 's/^[^:]+:[[:space:]]*"?([^"#]*)"?.*$/\1/' | sed 's/[[:space:]]*$//')
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
fail=0
scanned=0

report() { # report <file> <line-numbers...>
  local f="$1"; shift
  local n
  for n in "$@"; do echo "LEAK $f:$n: matches a forbidden pattern (value hidden)"; fail=1; done
}

for f in "${files[@]}"; do
  [[ "$f" =~ $skip ]] && continue
  if [[ $mode != file && $fixtures_active -eq 1 && "$f" == "$FIXTURE_DIR/"* ]]; then continue; fi
  if [[ $mode == staged ]]; then
    git show ":$f" >"$tmp" 2>/dev/null || continue
  else
    [[ -f "$f" ]] || continue
    cat -- "$f" >"$tmp"
  fi
  scanned=$((scanned + 1))
  lines=""
  for p in "${patterns[@]}"; do
    lines+=$'\n'"$(grep -nIE -e "$p" "$tmp" 2>/dev/null | cut -d: -f1 || true)"
  done
  lines+=$'\n'"$(grep -noIE -e "$email_re" "$tmp" 2>/dev/null | grep -vE "^[0-9]+:$email_ok\$" | cut -d: -f1 || true)"
  for v in "${literals[@]+"${literals[@]}"}"; do
    lines+=$'\n'"$(grep -nIF -e "$v" "$tmp" 2>/dev/null | cut -d: -f1 || true)"
  done
  mapfile -t nums < <(printf '%s\n' "$lines" | grep -E '^[0-9]+$' | sort -n -u || true)
  [[ ${#nums[@]} -gt 0 ]] && report "$f" "${nums[@]}"
done

if [[ $fail -ne 0 ]]; then
  echo "✗ Redaction check failed. Replace values with <PLACEHOLDERS> (see inventory.example.yaml)." >&2
  exit 1
fi
echo "✓ Redaction check passed ($scanned files)."
