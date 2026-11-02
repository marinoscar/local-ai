#!/usr/bin/env bash
# agent-ci.sh: the Go pipeline for agent/ (ADR-0001, rebuild-kit.md). Used by CI and by hand.
#
#   scripts/agent-ci.sh check   go vet, go test -race, staticcheck (pinned), vendored deps are current
#   scripts/agent-ci.sh build   static linux/amd64 and linux/arm64 binaries into agent/dist/, asserted static
#   scripts/agent-ci.sh all     check, then build
#
# While agent/go.mod does not exist yet (before AGENT-01), every command prints a notice and exits 0.
# Env: SKIP_STATICCHECK=1 skips staticcheck (hermetic tests); OUT_DIR overrides agent/dist.
#      STATICCHECK_VERSION pins the tool (default below).
set -euo pipefail

STATICCHECK_VERSION="${STATICCHECK_VERSION:-2025.1.1}"
ARCHES=(amd64 arm64)

root="$(git rev-parse --show-toplevel)"
cd "$root"
cmd="${1:-}"

if [[ ! -f agent/go.mod ]]; then
  echo "• agent/go.mod not found: nothing to check yet (AGENT-01 creates the module). Skipping."
  exit 0
fi
command -v go >/dev/null 2>&1 || { echo "agent-ci: Go is required" >&2; exit 2; }

do_check() {
  cd "$root/agent"
  echo "== go vet"
  go vet ./...
  echo "== go test -race"
  go test -race ./...
  if [[ "${SKIP_STATICCHECK:-0}" != "1" ]]; then
    echo "== staticcheck $STATICCHECK_VERSION"
    go run "honnef.co/go/tools/cmd/staticcheck@$STATICCHECK_VERSION" ./...
  fi
  echo "== vendored dependencies are committed and current"
  go mod vendor
  go mod verify >/dev/null 2>&1 || true
  cd "$root"
  if [[ -n "$(git status --porcelain -- agent/go.mod agent/go.sum agent/vendor)" ]]; then
    echo "ERROR agent/: 'go mod vendor' changed files. Run it and commit the result:" >&2
    git status --porcelain -- agent/go.mod agent/go.sum agent/vendor | sed 's/^/  /' >&2
    exit 1
  fi
  echo "✓ agent checks passed"
}

do_build() {
  local out="${OUT_DIR:-$root/agent/dist}"
  mkdir -p "$out"
  cd "$root/agent"
  [[ -d cmd/aiagent ]] || { echo "ERROR agent/cmd/aiagent not found: nothing to build" >&2; exit 1; }
  local mod_flag=()
  [[ -d vendor ]] && mod_flag=(-mod=vendor)
  for arch in "${ARCHES[@]}"; do
    local bin="$out/aiagent-linux-$arch"
    echo "== build linux/$arch (CGO_ENABLED=0)"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build "${mod_flag[@]}" -trimpath -buildvcs=false \
      -ldflags "-s -w -buildid=" -o "$bin" ./cmd/aiagent
    # Static assertion: `file` works for both arches; ldd only for the one this machine can run.
    if ! file "$bin" | grep -q 'statically linked'; then
      echo "ERROR $bin is not statically linked: $(file -b "$bin")" >&2
      exit 1
    fi
    if [[ "$arch" == "$(go env GOHOSTARCH)" ]] && command -v ldd >/dev/null 2>&1; then
      if ldd "$bin" 2>&1 | grep -vqE 'not a dynamic executable|statically linked'; then
        echo "ERROR $bin is a dynamic executable" >&2
        exit 1
      fi
    fi
    echo "  ok  $(basename "$bin")  $(sha256sum "$bin" | cut -d' ' -f1 | cut -c1-12)…"
  done
  echo "✓ static binaries in $out"
}

case "$cmd" in
  check) do_check ;;
  build) do_build ;;
  all)   do_check; do_build ;;
  *) echo "usage: $0 {check|build|all}" >&2; exit 2 ;;
esac
