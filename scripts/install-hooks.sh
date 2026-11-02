#!/usr/bin/env bash
# install-hooks.sh: install the git pre-commit hook that runs the redaction check.
# Idempotent: safe to run any number of times. See AGENTS.md "Local setup".
#
# Usage: scripts/install-hooks.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

hooks_dir="$(git rev-parse --git-path hooks)"
hook="$hooks_dir/pre-commit"
marker="# local-ai redaction hook (installed by scripts/install-hooks.sh)"

mkdir -p "$hooks_dir"
desired="$(cat <<HOOK
#!/usr/bin/env bash
$marker
# Blocks commits that contain lab inventory or secrets (docs/architecture/security-model.md §5).
root="\$(git rev-parse --show-toplevel)"
exec "\$root/scripts/check-redaction.sh" --staged
HOOK
)"

if [[ -f "$hook" ]]; then
  if [[ "$(cat "$hook")" == "$desired" ]]; then
    echo "✓ pre-commit hook already installed: $hook"
    exit 0
  fi
  if ! grep -qF "$marker" "$hook"; then
    cp -p "$hook" "$hook.bak"
    echo "• existing pre-commit hook was not ours; saved as $hook.bak"
  fi
fi

printf '%s\n' "$desired" >"$hook"
chmod +x "$hook"
echo "✓ installed pre-commit hook: $hook"
echo "  it runs: scripts/check-redaction.sh --staged"
