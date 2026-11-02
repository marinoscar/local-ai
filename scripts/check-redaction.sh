#!/usr/bin/env bash
# check-redaction.sh: fail if tracked (or staged) files contain lab inventory or secrets.
# Rules: docs/architecture/security-model.md §5. Used by pre-commit and CI.
#
# Usage: scripts/check-redaction.sh            # scan tracked + untracked (non-ignored) files
#        scripts/check-redaction.sh --staged   # scan staged content only (pre-commit)
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

if [[ "${1:-}" == "--staged" ]]; then
  mapfile -t files < <(git diff --cached --name-only --diff-filter=ACM)
else
  mapfile -t files < <(git ls-files --cached --others --exclude-standard)
fi
# Never scan these (they define the patterns themselves).
skip='^(scripts/check-redaction\.sh|\.gitleaks\.toml)$'

patterns=(
  '\b100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]{1,3}\.[0-9]{1,3}\b'   # tailnet IPs
  '\btail[0-9a-f]{6}\b'                                                         # tailnet name
  '\bZ0[0-9A-Z]{10,}\b'                                                         # Route53 zone id
  '\b(lat|nod|lai|pat)_[A-Za-z0-9_-]{20,}'                                      # tokens
  '\b192\.168\.[0-9]{1,3}\.[0-9]{1,3}\b'                                        # LAN IPs
)
# Literal values from the local (git-ignored) inventory, if present.
if [[ -f inventory.local.yaml ]]; then
  while IFS= read -r v; do
    [[ -n "$v" && "$v" != \<* && ${#v} -ge 6 ]] && patterns+=("$(printf '%s' "$v" | sed 's/[.[\*^$()+?{|]/\\&/g')")
  done < <(grep -E '^\s*(owner_email|tailnet|route53_zone_id|hostname|public_ip|tailnet_ip|lan_ip|fs_uuid):' inventory.local.yaml \
           | sed -E 's/^[^:]+:\s*"?([^"#]*)"?.*$/\1/' | sed 's/[[:space:]]*$//')
fi

fail=0
for f in "${files[@]}"; do
  [[ -f "$f" ]] || continue
  [[ "$f" =~ $skip ]] && continue
  for p in "${patterns[@]}"; do
    if grep -nIE -- "$p" "$f" >/dev/null 2>&1; then
      grep -nIE -- "$p" "$f" | sed "s|^|LEAK $f:|" | sed -E 's/(LEAK [^:]+:[0-9]+:).*/\1 matches a forbidden pattern (value hidden)/'
      fail=1
    fi
  done
done
if [[ $fail -ne 0 ]]; then
  echo "✗ Redaction check failed. Replace values with <PLACEHOLDERS> (see inventory.example.yaml)." >&2
  exit 1
fi
echo "✓ Redaction check passed (${#files[@]} files)."
