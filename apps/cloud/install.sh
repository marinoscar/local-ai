#!/usr/bin/env bash
# Install this app's CLI from source:
#
#   curl -fsSL https://raw.githubusercontent.com/marinoscar/local-ai/main/install.sh | bash
#
# Fetched and run BEFORE the repository exists on disk, so it cannot read
# packages/shared/identity.json: the CLI name and the repository below are
# literal targets of scripts/rename.mjs. Re-run it to update; set
# INSTALL_UNINSTALL=1 to remove the CLI again.
#
# Environment:
#   INSTALL_REPO     git URL to clone (default below)
#   INSTALL_REF      branch or tag (default main)
#   INSTALL_BIN_DIR  where the shim goes (default ~/.local/bin)
set -euo pipefail

CLI_NAME="laictl"
REPO="${INSTALL_REPO:-https://github.com/marinoscar/local-ai.git}"
REF="${INSTALL_REF:-main}"
HOME_DIR="$HOME/.$CLI_NAME"
APP_DIR="$HOME_DIR/app"
BIN_DIR="${INSTALL_BIN_DIR:-$HOME/.local/bin}"
SHIM="$BIN_DIR/$CLI_NAME"

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

if [[ "${INSTALL_UNINSTALL:-}" == "1" ]]; then
  rm -rf "$APP_DIR" "$SHIM"
  say "Removed $CLI_NAME ($APP_DIR, $SHIM). Your settings in $HOME_DIR are kept."
  exit 0
fi

command -v git >/dev/null || die "git is required"
command -v node >/dev/null || die "Node.js 24 or later is required"
major="$(node -p 'process.versions.node.split(".")[0]')"
(( major >= 24 )) || die "Node.js 24 or later is required (found $(node -v))"

mkdir -p "$HOME_DIR" "$BIN_DIR"
if [[ -d "$APP_DIR/.git" ]]; then
  say "Updating $APP_DIR ($REF)"
  git -C "$APP_DIR" fetch --depth 1 origin "$REF"
  git -C "$APP_DIR" checkout -q FETCH_HEAD
else
  say "Cloning $REPO ($REF)"
  git clone --depth 1 --branch "$REF" "$REPO" "$APP_DIR"
fi

say "Building the CLI"
(cd "$APP_DIR" && npm ci --workspace=cli --include-workspace-root --no-audit --no-fund && npm run build --workspace=cli)

cat > "$SHIM" <<SHIM
#!/usr/bin/env bash
exec node "$APP_DIR/apps/cli/dist/cli.js" "\$@"
SHIM
chmod +x "$SHIM"

say "Installed $CLI_NAME $("$SHIM" --version)"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say "Add $BIN_DIR to your PATH to run $CLI_NAME." ;;
esac
