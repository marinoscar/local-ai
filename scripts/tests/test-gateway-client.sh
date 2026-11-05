#!/usr/bin/env bash
# test-gateway-client.sh: scripts/gateway-client.sh against a throwaway local HTTP server that behaves like the
# gateway's /v1/models (401 LAI-GW-010 without a good key, a model list with one). No network, no real key: the
# test key is generated at run time.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
SCRIPT="$PWD/scripts/gateway-client.sh"
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...   (fragment must appear in stdout+stderr)
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -8; fi
}
ok() { if [[ $2 -eq 0 ]]; then pass=$((pass + 1)); echo "  ok   $1"; else failn=$((failn + 1)); echo "  FAIL $1"; fi; }

w="$(mktemp -d)"
srv_pid=""
trap '[[ -n "$srv_pid" ]] && kill "$srv_pid" 2>/dev/null; rm -rf "$w"' EXIT
export HOME="$w/home"; mkdir -p "$HOME"
unset LAI_BASE_URL LAI_KEY_FILE OPENCODE_CONFIG

# A valid-shaped token, built at run time (lai_ + 43 URL-safe characters).
good="lai_$(python3 -I -c 'import secrets; print(secrets.token_urlsafe(32)[:43])')"
[[ "${#good}" -eq 47 ]] || { echo "could not build a test token"; exit 1; }

# Fake gateway: GET /v1/models. Authorization is checked against the token in $FAKE_TOKEN.
cat >"$w/server.py" <<'PY'
import json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
TOKEN = os.environ["FAKE_TOKEN"]
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            body, status = {"error": {"message": "bad key", "type": "authentication_error", "code": "invalid_api_key", "localai_code": "LAI-GW-010"}}, 401
        elif os.environ.get("FAKE_MODE") == "empty":
            body, status = {"object": "list", "data": []}, 200
        else:
            body, status = {"object": "list", "data": [{"id": "qwen3.5-9b-q4km", "object": "model", "x-localai": {"state": "loaded"}}]}, 200
        raw = json.dumps(body).encode()
        self.send_response(status); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw))); self.end_headers(); self.wfile.write(raw)
srv = HTTPServer(("127.0.0.1", 0), H)
open(sys.argv[1], "w").write(str(srv.server_address[1]))
srv.serve_forever()
PY
FAKE_TOKEN="$good" python3 -I "$w/server.py" "$w/port" & srv_pid=$!
for _ in $(seq 1 50); do [[ -s "$w/port" ]] && break; sleep 0.1; done
[[ -s "$w/port" ]] || { echo "fake gateway did not start"; exit 1; }
LAI_BASE_URL="http://127.0.0.1:$(cat "$w/port")/v1"; export LAI_BASE_URL

echo "== usage"
t "no command" 2 "Usage:" "$SCRIPT"
t "help" 0 "store-key" "$SCRIPT" --help
t "unknown command" 2 "Usage:" "$SCRIPT" nope

echo "== store-key"
kf="$HOME/.config/opencode/localai-gateway-key"
t "empty input is refused" 1 "no key was given" bash -c "printf '' | '$SCRIPT' store-key"
t "a node-key shaped value is refused" 1 "does not look like a lab key" bash -c "printf 'abcdef0123456789' | '$SCRIPT' store-key"
[[ ! -e "$kf" ]]; ok "nothing was written by a refused key" $?
t "a good key is stored" 0 "readable only by you" bash -c "printf '%s\n' '$good' | '$SCRIPT' store-key"
[[ "$(stat -c %a "$kf")" == "600" ]]; ok "key file mode is 600" $?
[[ "$(cat "$kf")" == "$good" ]]; ok "key file holds the key" $?
out="$(printf '%s\n' "$good" | "$SCRIPT" store-key 2>&1)"
! grep -qF -- "$good" <<<"$out"; ok "the key is never printed" $?

echo "== opencode"
cfg="$HOME/.config/opencode/opencode.json"
t "creates the file when missing" 0 "provider localai-lab is in" "$SCRIPT" opencode
[[ "$(jq -r '.model' "$cfg")" == "localai-lab/qwen3-coder-next-q4km" ]]; ok "a new file defaults to the lab model" $?
[[ "$(jq -r '.provider["localai-lab"].options.apiKey' "$cfg")" == "{file:~/.config/opencode/localai-gateway-key}" ]]; ok "the key is a file reference" $?
[[ "$(jq -r '.provider["localai-lab"].options.baseURL' "$cfg")" == "$LAI_BASE_URL" ]]; ok "LAI_BASE_URL is honoured" $?
! grep -qF -- "$good" "$cfg"; ok "the config holds no key" $?
# An existing config keeps its other providers and its default model.
cat >"$cfg" <<'JSON'
{"model":"local-llama/qwen3.5-9b-q4km","share":"disabled","provider":{"local-llama":{"options":{"baseURL":"http://127.0.0.1:8080/v1"}}}}
JSON
t "merges into an existing file" 0 "backup:" "$SCRIPT" opencode
[[ "$(jq -r '.provider | keys | join(",")' "$cfg")" == "local-llama,localai-lab" ]]; ok "local-llama is kept next to localai-lab" $?
[[ "$(jq -r '.model, .share' "$cfg" | paste -sd,)" == "local-llama/qwen3.5-9b-q4km,disabled" ]]; ok "the default model and other settings are untouched" $?
ls "$HOME/.config/opencode"/opencode.json.bak-* >/dev/null 2>&1; ok "a backup was made" $?
t "run again is idempotent" 0 "" "$SCRIPT" opencode
[[ "$(jq -r '.provider | keys | length' "$cfg")" == "2" ]]; ok "still two providers" $?
t "--default switches the default model" 0 "" "$SCRIPT" opencode --default
[[ "$(jq -r '.model' "$cfg")" == "localai-lab/qwen3-coder-next-q4km" ]]; ok "default model is the lab's" $?
printf '{ // a comment\n "model": "x" }\n' >"$cfg"
before="$(cat "$cfg")"
t "a file with comments is refused untouched" 1 "not plain JSON" "$SCRIPT" opencode
[[ "$(cat "$cfg")" == "$before" ]]; ok "the commented file was not changed" $?

echo "== check"
t "check passes with the good key" 0 "ok   key -> 200" "$SCRIPT" check
t "check shows the model state" 0 '"state":"loaded"' "$SCRIPT" check
t "check proves the 401 without a key" 0 "no key -> 401 LAI-GW-010" "$SCRIPT" check
printf '%s\n' "lai_$(python3 -I -c 'import secrets; print(secrets.token_urlsafe(32)[:43])')" >"$kf"
t "check fails with a wrong key and shows the code" 1 "LAI-GW-010" "$SCRIPT" check
rm -f "$kf"
t "check fails with no key file" 1 "store-key" "$SCRIPT" check
printf '%s\n' "$good" >"$kf"; chmod 644 "$kf"
t "check warns about a loose key file" 0 "mode is 644" "$SCRIPT" check
chmod 600 "$kf"
LAI_BASE_URL="http://127.0.0.1:9/v1" t "check fails when the gateway is unreachable" 1 "FAIL" "$SCRIPT" check
# The key must not be on any command line while curl runs: the header comes from stdin.
grep -q -- "-K -" "$SCRIPT"; ok "the script passes the header on stdin (curl -K -)" $?

echo; echo "gateway-client: $pass passed, $failn failed"
[[ $failn -eq 0 ]]
