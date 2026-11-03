#!/usr/bin/env bash
# offline-build-check.sh: prove the agent source tarball builds with NO network, the way `aictl rebuild-agent` does
# (docs/specs/rebuild-kit.md section 4.3.4). Used by the node release workflow and by hand.
#
#   offline-build-check.sh --tarball <local-ai-node-v<semver>-src.tar.gz> --version <semver> [--go <go-binary>]
#
# It extracts the tarball into a fresh folder and runs the section 4.3.4 build inside a network namespace
# (`unshare -n`: only a loopback device), with GOTOOLCHAIN=local, GOFLAGS=-mod=vendor, CGO_ENABLED=0 and
# GOPROXY=off, so anything that is not vendored fails fast. Then `aiagent.new version` must report <semver>.
# The namespace is asserted to be offline before the build starts.
#
# Needs root or passwordless sudo (`unshare -n`). --go defaults to `go` on PATH; GOTOOLCHAIN=local means the
# Go you pass is the Go that builds (the kit's pinned toolchain in production, setup-go in CI).
#
# Exit: 0 ok, 1 the build or the version check failed (a missing vendored module is LAI-KIT-030 at rebuild time),
#       2 usage or environment error.
set -euo pipefail

usage() { sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "offline-build-check: $1" >&2; exit "${2:-1}"; }

tarball=""; version=""; gobin=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tarball) tarball="${2:-}"; shift 2 || usage ;;
    --version) version="${2:-}"; shift 2 || usage ;;
    --go) gobin="${2:-}"; shift 2 || usage ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done
[[ -n "$tarball" && -n "$version" ]] || usage
[[ -f "$tarball" ]] || die "tarball '$tarball' not found" 2
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "version '$version' is not a semver (no node-v prefix)" 2
[[ -n "$gobin" ]] || gobin="$(command -v go || true)"
[[ -n "$gobin" && -x "$gobin" ]] || die "no Go toolchain: pass --go <binary> or put go on PATH" 2
gobin="$(readlink -f "$gobin")"
command -v unshare >/dev/null 2>&1 || die "'unshare' is required" 2

priv=()
if [[ "$(id -u)" -ne 0 ]]; then
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then priv=(sudo -n)
  else die "creating a network namespace needs root or passwordless sudo" 2; fi
fi

work="$(mktemp -d)"
trap '"${priv[@]}" rm -rf "$work"' EXIT
tree="$work/local-ai"; mkdir -p "$tree"
tar -xzf "$tarball" -C "$tree" --no-same-owner
[[ -f "$tree/agent/go.mod" && -d "$tree/agent/cmd/aiagent" ]] || die "the tarball has no agent/go.mod and agent/cmd/aiagent"
mod="$(awk '/^module /{print $2; exit}' "$tree/agent/go.mod")"
[[ -n "$mod" ]] || die "cannot read the module path from agent/go.mod"

# The spec's `-o aiagent.new ./agent/cmd/aiagent` has no go.mod at the tree root; the module is agent/.
run_offline() { "${priv[@]}" unshare -n -- "$@"; }

echo "== the network namespace is offline"
ifaces="$(run_offline sh -c "tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' '")" || die "unshare -n failed" 2
[[ "$ifaces" == "lo" ]] || die "expected only the loopback device in the namespace, got: $(tr '\n' ' ' <<<"$ifaces")" 2
echo "  ok  only lo"

echo "== build offline (GOPROXY=off, -mod=vendor, GOTOOLCHAIN=local)"
if ! (cd "$tree/agent" && run_offline env -i PATH="$PATH" HOME="$work/home" \
        GOCACHE="$work/gocache" GOPATH="$work/gopath" \
        GOTOOLCHAIN=local GOFLAGS=-mod=vendor CGO_ENABLED=0 GOPROXY=off \
        "$gobin" build -trimpath -ldflags "-s -w -X $mod/internal/app.Version=$version" \
        -o ../aiagent.new ./cmd/aiagent) 2>"$work/build.err"; then
  sed 's/^/  | /' "$work/build.err" | tail -15 >&2
  die "offline build failed. LAI-KIT-030: the agent source is not fully vendored (run 'go mod vendor' and release again)"
fi
[[ -x "$tree/aiagent.new" ]] || die "the build produced no aiagent.new"

echo "== aiagent.new version"
line="$("$tree/aiagent.new" version)" || die "aiagent.new version failed"
echo "  $line"
reported="$(awk '{print $2}' <<<"$line")"
[[ "$reported" == "$version" ]] || die "aiagent.new reports '$reported', expected '$version'"
echo "✓ offline build from $(basename "$tarball") reports $version"
