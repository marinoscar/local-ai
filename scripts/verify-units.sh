#!/usr/bin/env bash
# verify-units.sh: run `systemd-analyze verify` on the generated localai-llama.service files and on
# the embedded localai-drill.service and localai-drill.timer templates.
# Spec: docs/specs/llama-router.md section 3.2 to 3.4 (ROUTER-04); docs/specs/drill.md section 3.3 (DRILL-01).
#
# The files checked are the golden files of the unit generator (agent/internal/router/testdata/golden):
# `go test ./internal/router` proves the generator renders exactly those bytes, so verifying them
# verifies the generator's output. Checked: the router unit (ai-n3 and ai-n1 profiles), the single-mode
# unit, and the router unit together with the environment drop-in.
#
# systemd-analyze verify wants the program of ExecStart to exist and be executable. When the real
# path is absent this script installs an empty stub there (needs root, or sudo) and removes it on
# exit; an existing file is never touched. Nothing is installed in a unit directory and no systemctl
# command runs: the files are verified from a temporary directory.
#
# Usage: scripts/verify-units.sh        Exit: 0 clean, 1 a file failed or printed a diagnostic,
#                                             2 systemd-analyze is missing.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
golden="$root/agent/internal/router/testdata/golden"
bin=/opt/localai/llama.cpp/current/bin/llama-server
units="$root/agent/internal/setup/units"
aictl=/usr/local/bin/aictl

command -v systemd-analyze >/dev/null 2>&1 || { echo "verify-units: systemd-analyze not found" >&2; exit 2; }

sudo=""
if [[ "$(id -u)" -ne 0 ]]; then sudo="sudo"; fi

tmp="$(mktemp -d)"
created=()
cleanup() {
  rm -rf "$tmp"
  # remove, deepest first, only what this run created
  for ((i=${#created[@]}-1; i>=0; i--)); do
    $sudo rm -rf -- "${created[$i]}" 2>/dev/null || true
  done
}
trap cleanup EXIT

for stub in "$bin" "$aictl"; do
  if [[ ! -e "$stub" ]]; then
    # The first missing component below an existing directory: removed as a whole on exit.
    d="$stub"
    while [[ ! -e "$(dirname "$d")" ]]; do d="$(dirname "$d")"; done
    created+=("$d")
    $sudo mkdir -p "$(dirname "$stub")"
    $sudo install -m 0755 /dev/null "$stub"
  fi
done

fail=0
check() { # name, unit file, optional drop-in file
  local name="$1" unit="$2" dropin="${3:-}"
  local dir="$tmp/$name"
  mkdir -p "$dir"
  cp "$unit" "$dir/localai-llama.service"
  if [[ -n "$dropin" ]]; then
    mkdir -p "$dir/localai-llama.service.d"
    cp "$dropin" "$dir/localai-llama.service.d/50-env.conf"
  fi
  local out
  if out="$(systemd-analyze verify "$dir/localai-llama.service" 2>&1)" && [[ -z "$out" ]]; then
    echo "ok   $name"
  else
    echo "FAIL $name" >&2
    echo "$out" | sed 's/^/     /' >&2
    fail=1
  fi
}

check router-ai-n3 "$golden/localai-llama.router.service"
check router-ai-n1 "$golden/localai-llama.router.ai-n1.service"
check single-ai-n3 "$golden/localai-llama.single.service"
check router-with-dropin "$golden/localai-llama.router.ai-n1.service" "$golden/50-env.conf"

# The drill service and its timer are verified together; the service Requires the agent unit, which
# lives in another issue, so a stub of it stands in inside the temporary directory.
check_drill() {
  local dir="$tmp/drill"
  mkdir -p "$dir"
  cp "$units/localai-drill.service" "$units/localai-drill.timer" "$dir/"
  printf '[Service]\nExecStart=%s\n' "$aictl" >"$dir/localai-agent.service"
  local out
  if out="$(systemd-analyze verify "$dir/localai-drill.service" "$dir/localai-drill.timer" 2>&1)" && [[ -z "$out" ]]; then
    echo "ok   drill service and timer"
  else
    echo "FAIL drill service and timer" >&2
    echo "$out" | sed 's/^/     /' >&2
    fail=1
  fi
}
check_drill

exit "$fail"
