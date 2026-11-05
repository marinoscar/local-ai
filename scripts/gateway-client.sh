#!/usr/bin/env bash
# gateway-client.sh: connect one computer to the lab gateway (https://ai.marin.cr/v1).
# Spec: docs/specs/gateway.md Appendix A.2 and A.3. Runbook: docs/runbooks/02-chat-and-api-keys.md Parts 3 and 4.
#
# Usage: scripts/gateway-client.sh <command> [options]
#   store-key            Read a lai_ key (typed hidden, or piped on stdin) and write the key file, mode 600.
#   opencode [--default] Add or refresh the `localai-lab` provider in the OpenCode config. Other providers are kept
#                        (the node's `local-llama` one too). A backup of the old file is made first. --default also
#                        makes localai-lab/<first model> the default model.
#   check                Probe the gateway: no key must answer 401 LAI-GW-010; the stored key must list the models.
#   -h, --help           This text.
#
# Environment (all optional):
#   LAI_BASE_URL    gateway base URL            (default https://ai.marin.cr/v1)
#   LAI_KEY_FILE    where the key is stored     (default ~/.config/opencode/localai-gateway-key)
#   OPENCODE_CONFIG the OpenCode config file    (default ~/.config/opencode/opencode.json)
#
# The key is never printed, never put on a command line (curl reads its header from stdin), and never written
# anywhere but LAI_KEY_FILE. Needs bash, curl and jq.
# Exit: 0 ok, 1 a step failed (the message says which and what to do), 2 usage error.
set -euo pipefail

BASE_URL="${LAI_BASE_URL:-https://ai.marin.cr/v1}"
KEY_FILE="${LAI_KEY_FILE:-$HOME/.config/opencode/localai-gateway-key}"
CONFIG="${OPENCODE_CONFIG:-$HOME/.config/opencode/opencode.json}"
# shellcheck disable=SC2088  # the literal text `~/...` is what OpenCode's {file:...} reference holds
DEFAULT_KEY_FILE_REF='~/.config/opencode/localai-gateway-key'
DEFAULT_BASE_URL='https://ai.marin.cr/v1'

die() { echo "gateway-client: $*" >&2; exit 1; }
usage() { sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

# The localai-lab provider block of gateway.md Appendix A.2. scripts/tests/gateway-docs.test.mjs proves it is equal to the
# spec and to docs/templates/gateway/opencode.localai-lab.json.
provider_block() {
  cat <<'JSON'
{
  "npm": "@ai-sdk/openai-compatible",
  "name": "Local AI lab (ai.marin.cr)",
  "options": {
    "baseURL": "https://ai.marin.cr/v1",
    "apiKey": "{file:~/.config/opencode/localai-gateway-key}",
    "timeout": 600000
  },
  "models": {
    "qwen3-coder-next-q4km": { "name": "Qwen3 Coder Next (lab)", "limit": { "context": 131072, "output": 8192 } },
    "qwen3.5-9b-q4km": { "name": "Qwen 3.5 9B (lab)", "limit": { "context": 32768, "output": 8192 } }
  }
}
JSON
}

# A lai_ token is `lai_` plus 43 URL-safe characters (docs/specs/gateway.md, GW-02 token helper).
well_formed() { [[ "$1" =~ ^lai_[A-Za-z0-9_-]{43}$ ]]; }

cmd_store_key() {
  local key=""
  if [[ -t 0 ]]; then
    read -rsp "Paste the lai_ key (it will not show), then press Enter: " key
    echo >&2
  else
    IFS= read -r key || true
  fi
  key="${key//[$'\r\n\t ']/}"
  [[ -n "$key" ]] || die "no key was given. Create one at https://ai.marin.cr -> API keys -> Create key"
  well_formed "$key" || die "that does not look like a lab key (it starts with lai_ and has 47 characters). A node key does not work on the gateway"
  local dir tmp
  dir="$(dirname "$KEY_FILE")"
  mkdir -p "$dir"
  tmp="$(umask 077; mktemp "$dir/.gateway-key.XXXXXX")"
  # shellcheck disable=SC2064
  trap "rm -f '$tmp'" EXIT
  printf '%s\n' "$key" >"$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" "$KEY_FILE"
  trap - EXIT
  echo "ok: the key is stored in $KEY_FILE (readable only by you)"
}

cmd_opencode() {
  local make_default=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --default) make_default=1; shift ;;
      *) echo "usage: $0 opencode [--default]" >&2; exit 2 ;;
    esac
  done
  need jq
  local block
  block="$(provider_block)"
  # Point the block at this run's base URL and key file when they are not the defaults.
  block="$(jq --arg u "$BASE_URL" --arg f "{file:${KEY_FILE/#$HOME/\~}}" \
    '.options.baseURL = $u | .options.apiKey = $f' <<<"$block")"
  [[ "$BASE_URL" == "$DEFAULT_BASE_URL" && "${KEY_FILE/#$HOME/\~}" == "$DEFAULT_KEY_FILE_REF" ]] || \
    echo "note: using base URL $BASE_URL and key file ${KEY_FILE/#$HOME/\~} instead of the lab defaults" >&2
  local first
  first="$(jq -r '.models | keys_unsorted[0]' <<<"$block")"

  mkdir -p "$(dirname "$CONFIG")"
  local out
  if [[ -f "$CONFIG" ]]; then
    jq -e 'type == "object"' "$CONFIG" >/dev/null 2>&1 || \
      die "$CONFIG is not plain JSON (comments or a trailing comma?). Nothing was changed. Add the block from docs/templates/gateway/opencode.localai-lab.json by hand"
    local backup
    backup="$CONFIG.bak-$(date -u +%Y%m%dT%H%M%SZ)"
    cp -p "$CONFIG" "$backup"
    echo "backup: $backup"
    out="$(jq --argjson p "$block" --arg first "$first" --argjson dflt "$make_default" \
      '.["$schema"] //= "https://opencode.ai/config.json"
       | .provider = ((.provider // {}) + {"localai-lab": $p})
       | if $dflt == 1 then .model = ("localai-lab/" + $first) else . end' "$CONFIG")"
  else
    out="$(jq -n --argjson p "$block" --arg first "$first" \
      '{"$schema": "https://opencode.ai/config.json", "model": ("localai-lab/" + $first), "provider": {"localai-lab": $p}}')"
  fi
  (umask 077; printf '%s\n' "$out" >"$CONFIG.tmp.$$") && mv -f "$CONFIG.tmp.$$" "$CONFIG"
  echo "ok: provider localai-lab is in $CONFIG"
  [[ -f "$KEY_FILE" ]] || echo "next: store the key with: $0 store-key"
}

# curl with the Authorization header read from stdin, so the key is never in the process list.
auth_curl() { # auth_curl <keyfile> <curl args...>
  local kf="$1"; shift
  printf 'header = "Authorization: Bearer %s"\n' "$(tr -d '\r\n' <"$kf")" | curl -K - "$@"
}

cmd_check() {
  need curl; need jq
  local failed=0 body code
  # 1. Without a key the gateway must refuse with LAI-GW-010.
  body="$(mktemp)"
  # shellcheck disable=SC2064
  trap "rm -f '$body'" EXIT
  code="$(curl -sS --max-time 20 -o "$body" -w '%{http_code}' "$BASE_URL/models" || true)"
  if [[ "$code" == "401" ]] && [[ "$(jq -r '.error.localai_code // empty' "$body" 2>/dev/null)" == "LAI-GW-010" ]]; then
    echo "ok   no key -> 401 LAI-GW-010"
  else
    echo "FAIL no key -> HTTP $code (want 401 LAI-GW-010). Is $BASE_URL the gateway? See docs/runbooks/07-troubleshooting.md" >&2
    failed=1
  fi
  # 2. With the stored key the models must list.
  if [[ ! -r "$KEY_FILE" ]]; then
    echo "FAIL no key file at $KEY_FILE. Run: $0 store-key" >&2
    failed=1
  else
    local mode
    mode="$(stat -c '%a' "$KEY_FILE" 2>/dev/null || stat -f '%Lp' "$KEY_FILE")"
    [[ "$mode" == "600" ]] || echo "warn the key file mode is $mode; run: chmod 600 $KEY_FILE" >&2
    code="$(auth_curl "$KEY_FILE" -sS --max-time 20 -o "$body" -w '%{http_code}' "$BASE_URL/models" || true)"
    if [[ "$code" == "200" ]]; then
      echo "ok   key -> 200, models:"
      jq -c '.data[] | {id, state: .["x-localai"].state}' "$body" | sed 's/^/       /'
      [[ "$(jq '.data | length' "$body")" -gt 0 ]] || { echo "warn no model is listed: no node serves one right now (LAI-GW-021 on a request)" >&2; }
    else
      local lc
      lc="$(jq -r '.error.localai_code // empty' "$body" 2>/dev/null || true)"
      echo "FAIL key -> HTTP $code ${lc:+($lc) }- look the code up in docs/specs/gateway.md section 8" >&2
      failed=1
    fi
  fi
  return "$failed"
}

[[ $# -ge 1 ]] || { usage >&2; exit 2; }
case "$1" in
  store-key) shift; [[ $# -eq 0 ]] || { usage >&2; exit 2; }; cmd_store_key ;;
  opencode)  shift; cmd_opencode "$@" ;;
  check)     shift; [[ $# -eq 0 ]] || { usage >&2; exit 2; }; cmd_check ;;
  -h|--help) usage ;;
  *) usage >&2; exit 2 ;;
esac
