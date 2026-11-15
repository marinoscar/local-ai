#!/usr/bin/env bash
# check-cli-version-bump.sh: fail when a change to the cloud CLI (laictl) does not raise its version.
#
# Usage: scripts/check-cli-version-bump.sh <base-ref>
#
# Every fix or improvement to the CLI ships with a higher apps/cloud/apps/cli/package.json version (AGENTS.md,
# Versions), so `laictl --version` always tells which CLI a server runs. A CLI change is any change under
# apps/cloud/apps/cli/ other than its tests and README, any change to apps/cloud/packages/shared/ (the identity and
# version files the CLI reads), and a new version of the platform CLI packages in the lockfile.
set -euo pipefail

base="${1:?usage: check-cli-version-bump.sh <base-ref>}"
cd "$(git rev-parse --show-toplevel)"
APP="apps/cloud"

lock_version() { # lock_version <rev> <lockfile key>
  git show "$1:$APP/package-lock.json" 2>/dev/null \
    | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const p=JSON.parse(s).packages[process.argv[1]];process.stdout.write(p&&p.version||"")}catch{}})' "$2"
}
cli_version() { git show "$1:$APP/apps/cli/package.json" 2>/dev/null | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{process.stdout.write(JSON.parse(s).version||"")}catch{}})'; }

reasons=()
while IFS= read -r f; do
  case "$f" in
    "$APP"/apps/cli/*.test.ts|"$APP"/apps/cli/src/*.test.ts|"$APP"/apps/cli/src/*/*.test.ts|"$APP"/apps/cli/README.md) ;;
    "$APP"/apps/cli/package.json) ;;   # judged by the version comparison itself
    "$APP"/apps/cli/*|"$APP"/packages/shared/*) reasons+=("$f") ;;
  esac
done < <(git diff --name-only "$base"...HEAD)
for pkg in platform-cli platform-infra; do
  key="node_modules/@marinoscar/$pkg"
  [[ "$(lock_version "$base" "$key")" == "$(lock_version HEAD "$key")" ]] || reasons+=("@marinoscar/$pkg version in package-lock.json")
done
# Any other dependency of the CLI workspace.
if ! diff <(git show "$base:$APP/apps/cli/package.json" 2>/dev/null | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const j=JSON.parse(s);console.log(JSON.stringify([j.dependencies,j.bin,j.files]))}catch{}})') \
            <(git show "HEAD:$APP/apps/cli/package.json" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const j=JSON.parse(s);console.log(JSON.stringify([j.dependencies,j.bin,j.files]))}catch{}})') >/dev/null; then
  reasons+=("$APP/apps/cli/package.json dependencies")
fi

if [[ ${#reasons[@]} -eq 0 ]]; then
  echo "= no CLI change; no version bump needed."
  exit 0
fi

before="$(cli_version "$base")"; after="$(cli_version HEAD)"
if [[ -n "$before" ]] && [[ "$(printf '%s\n%s\n' "$before" "$after" | sort -V | tail -1)" == "$before" ]]; then
  echo "::error::The CLI changed but its version did not go up ($before -> $after). Raise \"version\" in $APP/apps/cli/package.json (and its package-lock.json entry) — AGENTS.md, Versions. Changed:" >&2
  printf '  %s\n' "${reasons[@]}" >&2
  exit 1
fi
echo "✓ the CLI changed and its version went up: $before -> $after"
