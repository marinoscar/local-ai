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

# Never scan these: they define the patterns themselves, or are third-party source we do not author
# (agent/vendor/: pinned by go.sum and proven by CI's `go mod vendor` check; their license, AUTHORS and
# MAINTAINERS files name real people, which is not lab inventory).
skip='^(scripts/check-redaction\.sh|\.gitleaks\.toml|agent/vendor/.*)$'

# The catalog carries real upstream sha256 pins (decision for issue #251). In exactly these two files a
# 64-hex value is allowed when it is the value of a `sha256:` key; nothing else changes there: a bare
# 64-hex string, a token, an IP or an email in them is still a leak, and every other file stays strict.
catalog_pins='^catalog/(ark|models)\.yaml$'
cloud_locks='^apps/cloud/(apps/api/prisma/platform\.lock|infra/platform-infra\.lock\.json)$'   # generated lock files: sha256 digests of public files
lockfiles='(^|/)package-lock\.json$'   # npm `deprecated` text is third-party prose (may carry an author's email)

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
email_ok='(noreply@.*|[^@]+@example\.(test|com))'   # allowed: noreply@..., reserved example domains (RFC 2606)

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
  if [[ "$f" =~ $catalog_pins ]]; then
    sed -E -i 's/(sha256:[[:space:]]*"?)[0-9a-fA-F]{64}/\1<pin>/g' "$tmp"
  fi
  if [[ "$f" =~ $lockfiles ]]; then
    # Neutralise only the value of `"deprecated":` lines (line numbers are preserved).
    sed -E -i 's/^([[:space:]]*"deprecated":).*/\1 "<third-party text>"/' "$tmp"
  fi
  if [[ "$f" =~ $cloud_locks ]]; then
    # A quoted 64-hex value in the cloud app's generated lock files is a public digest, not a key.
    sed -E -i 's/"[0-9a-fA-F]{64}"/"<digest>"/g' "$tmp"
  fi
  # The RFC 1918 range itself (a network, not a host) is not a LAN address.
  sed -E -i 's#192\.168\.0\.0/16#<rfc1918-range>#g' "$tmp"
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
