#!/usr/bin/env bash
# test-openwebui-cutover.sh: scripts/openwebui-cutover.sh with a fake `docker` on PATH. No VPS, no database, no real key:
# the fake prints canned `psql -Atc` rows, records every call, and the key-like values are built at run time.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
SCRIPT="$PWD/scripts/openwebui-cutover.sh"
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...   (fragment must appear in stdout+stderr)
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -8; fi
}
ok() { if [[ $2 -eq 0 ]]; then pass=$((pass + 1)); echo "  ok   $1"; else failn=$((failn + 1)); echo "  FAIL $1"; fi; }

w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT
mkdir -p "$w/bin"
export OWUI_BACKUP="$w/backup.txt"
secret_a="sk-$(python3 -I -c 'import secrets; print(secrets.token_hex(12))')"   # a made-up key, to prove it is never printed
secret_b="lai_$(python3 -I -c 'import secrets; print(secrets.token_urlsafe(32)[:43])')"

# Fake docker. `exec <container> psql ...` prints $FAKE_ROWS; `exec <container> sh -c ...` prints $FAKE_CODE.
cat >"$w/bin/docker" <<'F'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_DOCKER_LOG"
[[ "$1" == "exec" ]] || exit 1
if [[ "$2" == "-i" ]]; then cat >"$FAKE_STDIN_LOG"; [[ -n "${FAKE_PSQL_FAIL:-}" ]] && exit 1; exit 0; fi
case "$3" in
  psql) [[ -n "${FAKE_PSQL_FAIL:-}" ]] && { echo 'ERROR:  relation "config" does not exist' >&2; exit 1; }; [[ -n "${FAKE_ROWS:-}" ]] && printf '%s\n' "$FAKE_ROWS"; exit 0 ;;
  sh) printf '%s\n' "${FAKE_CODE:-200}"; exit 0 ;;
esac
exit 1
F
chmod +x "$w/bin/docker"
export PATH="$w/bin:$PATH" FAKE_DOCKER_LOG="$w/docker.log" FAKE_STDIN_LOG="$w/stdin.sql"

before_rows="openai.api_base_urls|[\"https://ai-n1.marin.cr/v1\",\"https://api.openai.com/v1\",\"https://ai-n2.marin.cr\",\"https://ai-n3.marin.cr/v1\"]
openai.api_keys|[\"$secret_a\",\"$secret_a\",\"$secret_a\",\"$secret_a\"]
openai.api_configs|{\"0\":{\"enable\":true},\"1\":{\"enable\":true},\"2\":{\"enable\":true},\"3\":{\"enable\":true}}"
after_rows="openai.api_base_urls|[\"https://api.openai.com/v1\",\"https://ai.marin.cr/v1\"]
openai.api_keys|[\"$secret_a\",\"$secret_b\"]
openai.api_configs|{\"0\":{\"enable\":true,\"prefix_id\":\"\",\"model_ids\":[]},\"1\":{\"enable\":true,\"prefix_id\":\"\",\"model_ids\":[]}}"
misaligned_rows="openai.api_base_urls|[\"https://api.openai.com/v1\",\"https://ai.marin.cr/v1\"]
openai.api_keys|[\"$secret_a\"]
openai.api_configs|{\"0\":{},\"1\":{}}"
twice_rows="openai.api_base_urls|[\"https://ai.marin.cr/v1\",\"https://ai.marin.cr/v1/\"]
openai.api_keys|[\"$secret_b\",\"$secret_b\"]
openai.api_configs|{\"0\":{},\"1\":{}}"

echo "== usage"
t "no command" 2 "Usage:" "$SCRIPT"
t "help" 0 "READ-ONLY" "$SCRIPT" --help
t "unknown command" 2 "Usage:" "$SCRIPT" nope

echo "== backup"
FAKE_ROWS="$before_rows" t "backup writes the file" 0 "mode 600" "$SCRIPT" backup
[[ "$(stat -c %a "$OWUI_BACKUP")" == "600" ]]; ok "backup file mode is 600" $?
[[ "$(cat "$OWUI_BACKUP")" == "$before_rows" ]]; ok "backup holds the three rows as stored" $?
FAKE_ROWS="$after_rows" t "a second backup is refused" 1 "already exists" "$SCRIPT" backup
[[ "$(cat "$OWUI_BACKUP")" == "$before_rows" ]]; ok "the good backup was not overwritten" $?
FAKE_ROWS="$after_rows" t "--force replaces it" 0 "" "$SCRIPT" backup --force
rm -f "$OWUI_BACKUP"
FAKE_PSQL_FAIL=1 t "a different schema stops the backup with the UI hint" 1 "Admin Panel" "$SCRIPT" backup
[[ ! -e "$OWUI_BACKUP" ]]; ok "no backup file is left after a failure" $?
ls "$w"/backup.txt.* >/dev/null 2>&1; [[ $? -ne 0 ]]; ok "no temporary file is left" $?
grep -q -- "-Atc" "$FAKE_DOCKER_LOG"; ok "the spec's psql -Atc form is used" $?

echo "== inspect"
out="$(FAKE_ROWS="$before_rows" "$SCRIPT" inspect 2>&1)"
grep -qF "0: https://ai-n1.marin.cr/v1  key=set" <<<"$out"; ok "inspect lists each URL with 'key=set'" $?
! grep -qF -- "$secret_a" <<<"$out"; ok "inspect never prints a key" $?
FAKE_ROWS="$misaligned_rows" t "inspect warns about index misalignment" 0 "index-aligned" "$SCRIPT" inspect
FAKE_PSQL_FAIL=1 t "inspect with another schema" 1 "Admin Panel" "$SCRIPT" inspect

echo "== check"
FAKE_ROWS="$before_rows" t "before the cutover: fails and names the per-node URLs" 1 "https://ai-n2.marin.cr" "$SCRIPT" check
FAKE_ROWS="$before_rows" t "before the cutover: the gateway is missing" 1 "configured 0 times" "$SCRIPT" check
FAKE_ROWS="$after_rows" t "after the cutover: passes" 0 "no per-node connection is left" "$SCRIPT" check
FAKE_ROWS="$misaligned_rows" t "misaligned lists fail" 1 "differ in length" "$SCRIPT" check
FAKE_ROWS="$twice_rows" t "the gateway twice fails" 1 "configured 2 times" "$SCRIPT" check
out="$(FAKE_ROWS="$after_rows" "$SCRIPT" check 2>&1)"
! grep -qF -- "$secret_b" <<<"$out"; ok "check never prints a key" $?
GATEWAY_URL="https://other.example.test/v1" FAKE_ROWS="$after_rows" t "GATEWAY_URL is honoured" 1 "other.example.test" "$SCRIPT" check

echo "== verify"
FAKE_CODE=200 t "200 passes" 0 "accepts the key" "$SCRIPT" verify
FAKE_CODE=401 t "401 points at LAI-GW-010" 1 "LAI-GW-010" "$SCRIPT" verify
FAKE_CODE=502 t "502 says wait" 1 "20 to 40 s" "$SCRIPT" verify
FAKE_CODE=418 t "other codes fail" 1 "unexpected HTTP 418" "$SCRIPT" verify
grep -F 'sh -c' "$FAKE_DOCKER_LOG" | grep -qF '$OPENAI_API_KEY'; ok "the key is expanded inside the container, not here" $?
grep -F 'sh -c' "$FAKE_DOCKER_LOG" | grep -qF 'https://ai.marin.cr/v1/models'; ok "verify asks the gateway's /v1/models" $?
! grep -qF -- "$secret_a" "$FAKE_DOCKER_LOG"; ok "no key was ever on a docker command line" $?

echo "== restore (rollback)"
printf '%s\n' "$before_rows" >"$OWUI_BACKUP"
t "restore needs --yes" 1 "restore --yes" "$SCRIPT" restore
t "restore with an extra word is refused" 1 "restore --yes" "$SCRIPT" restore --yes now
t "restore writes the backup back" 0 "back as they were" "$SCRIPT" restore --yes
grep -q '^BEGIN;' "$w/stdin.sql" && grep -q '^COMMIT;' "$w/stdin.sql"; ok "the restore is one transaction" $?
[[ "$(grep -c '^UPDATE config SET value' "$w/stdin.sql")" == "3" ]]; ok "all three settings are written together" $?
grep -qF -- "$secret_a" "$w/stdin.sql"; ok "the old keys travel on stdin" $?
! grep -qF -- "$secret_a" "$FAKE_DOCKER_LOG"; ok "no key on any docker command line" $?
grep -qF 'https://ai-n3.marin.cr/v1' "$w/stdin.sql"; ok "the old per-node URLs are restored" $?
printf 'openai.api_base_urls|not json\n' >"$OWUI_BACKUP"
t "a damaged backup changes nothing" 1 "Nothing was changed" "$SCRIPT" restore --yes
printf '%s\n' "$before_rows" >"$OWUI_BACKUP"
FAKE_PSQL_FAIL=1 t "a failing psql is reported" 1 "rolled back" "$SCRIPT" restore --yes
rm -f "$OWUI_BACKUP"
t "no backup file" 1 "no backup" "$SCRIPT" restore --yes

echo; echo "openwebui-cutover: $pass passed, $failn failed"
[[ $failn -eq 0 ]]
