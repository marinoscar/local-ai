#!/usr/bin/env bash
# openwebui-cutover.sh: READ-ONLY helper for moving Open WebUI (chat.marin.cr) to the one gateway connection.
# Run on the VPS, as root, by the lab owner. Spec: docs/specs/gateway.md Appendix A.1.
# Runbook: docs/runbooks/02-chat-and-api-keys.md Part 5.
#
# It never writes the NEW settings: those are changed in the Admin Panel (preferred) or with the SQL block of
# Appendix A.1, typed by the owner, so the lab key never passes through here. It writes the backup file, and `restore`
# (the rollback) puts the backed-up settings back.
#
# Usage: scripts/openwebui-cutover.sh <command>
#   backup [--force]   Save the three connection settings to the backup file (mode 600). Refuses to overwrite an
#                      existing backup unless --force, so a second run cannot destroy the good copy.
#   inspect            Show the stored connections: URL, whether a key is set (never the key) and the config entry.
#   check              Exit 0 only when the gateway URL is the one lab connection and no per-node URL
#                      (https://ai-nN.marin.cr...) is left. This is the acceptance check of the cutover.
#   verify             Ask the gateway for /v1/models with the key in the open-webui container's environment and
#                      print only the HTTP code (200 is good). Needs OPENAI_API_KEY in that container to be the
#                      lai_ service key (set it in apps/openwebui/.env, then recreate the container).
#   restore --yes      ROLLBACK: write the three settings from the backup file back, in one transaction (needs --yes).
#                      Restart Open WebUI afterwards. The backup holds the old keys; they are sent on stdin, never on a
#                      command line.
#   -h, --help         This text.
#
# Environment (optional): OWUI_PG_CONTAINER (infra-postgres), OWUI_PG_USER (admin), OWUI_DB (open_webui),
#   OWUI_CONTAINER (open-webui), OWUI_BACKUP (/root/openwebui-connections.backup.txt),
#   GATEWAY_URL (https://ai.marin.cr/v1).
# Exit: 0 ok, 1 a check failed or Open WebUI's schema is not the one the spec describes, 2 usage error.
set -euo pipefail

PG_CONTAINER="${OWUI_PG_CONTAINER:-infra-postgres}"
PG_USER="${OWUI_PG_USER:-admin}"
DB="${OWUI_DB:-open_webui}"
OWUI_CONTAINER="${OWUI_CONTAINER:-open-webui}"
BACKUP="${OWUI_BACKUP:-/root/openwebui-connections.backup.txt}"
GATEWAY_URL="${GATEWAY_URL:-https://ai.marin.cr/v1}"
KEYS_SQL="select key, value from config where key in ('openai.api_base_urls','openai.api_keys','openai.api_configs');"

die() { echo "openwebui-cutover: $*" >&2; exit 1; }
usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }
SCHEMA_HINT="Open WebUI's config table is not the key/value table the spec describes (the schema changes between releases). Use the Admin Panel steps instead: docs/runbooks/02-chat-and-api-keys.md Part 5"

query() { docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$DB" -Atc "$1"; }

# value_of <key> <rows>: the JSON after the first `|` of the row `<key>|<json>` (the JSON may itself contain `|`).
value_of() {
  local row
  row="$(grep -m1 -F "$1|" <<<"$2" || true)"
  [[ -n "$row" ]] || return 1
  printf '%s\n' "${row#*|}"
}

cmd_backup() {
  local force=0
  [[ "${1:-}" == "--force" ]] && { force=1; shift; }
  [[ $# -eq 0 ]] || { usage >&2; exit 2; }
  if [[ -e "$BACKUP" && $force -eq 0 ]]; then
    die "$BACKUP already exists. It is your good copy: keep it. (--force replaces it)"
  fi
  # The backup holds API keys: create it private before anything is written (the umask is tightened for this one file).
  local tmp
  tmp="$(umask 077; mktemp "${BACKUP}.XXXXXX")"
  # shellcheck disable=SC2064
  trap "rm -f '$tmp'" EXIT
  query "$KEYS_SQL" >"$tmp" || die "$SCHEMA_HINT"
  [[ -s "$tmp" ]] || die "$SCHEMA_HINT"
  chmod 600 "$tmp"
  mv -f "$tmp" "$BACKUP"
  trap - EXIT
  echo "ok: backup saved to $BACKUP (mode 600, it contains keys: never copy it into a chat or a repository)"
}

# load_rows: fills ROWS and the three values, or dies with the schema hint.
load_rows() {
  need jq
  ROWS="$(query "$KEYS_SQL")" || die "$SCHEMA_HINT"
  URLS="$(value_of openai.api_base_urls "$ROWS")" || die "$SCHEMA_HINT"
  jq -e 'type == "array"' <<<"$URLS" >/dev/null 2>&1 || die "$SCHEMA_HINT"
  KEYS="$(value_of openai.api_keys "$ROWS" || echo '[]')"
  CONFIGS="$(value_of openai.api_configs "$ROWS" || echo '{}')"
}

cmd_inspect() {
  load_rows
  echo "Connections stored in Open WebUI (index: URL, key set?):"
  jq -r --argjson keys "$KEYS" \
    'to_entries[] | "  \(.key): \(.value)  key=\(if (($keys[.key] // "") | tostring | length) > 2 then "set" else "EMPTY" end)"' <<<"$URLS"
  local nu nk nc
  nu="$(jq 'length' <<<"$URLS")"; nk="$(jq 'length' <<<"$KEYS")"; nc="$(jq 'length' <<<"$CONFIGS")"
  echo "openai.api_configs: $CONFIGS"
  if ! [[ "$nu" == "$nk" && "$nu" == "$nc" ]]; then
    echo "WARNING: the three lists have different lengths (urls=$nu keys=$nk configs=$nc). They must be index-aligned: write all three together." >&2
  fi
}

cmd_check() {
  load_rows
  local bad=0 n_gw old
  n_gw="$(jq --arg g "${GATEWAY_URL%/}" '[.[] | select(rtrimstr("/") == $g)] | length' <<<"$URLS")"
  if [[ "$n_gw" == "1" ]]; then echo "ok   the gateway ($GATEWAY_URL) is configured once"
  else echo "FAIL the gateway ($GATEWAY_URL) is configured $n_gw times (want 1)" >&2; bad=1; fi
  old="$(jq -r '.[] | select(test("^https?://ai-n[0-9]+\\.marin\\.cr(/|$)"))' <<<"$URLS")"
  if [[ -z "$old" ]]; then echo "ok   no per-node connection is left"
  else echo "FAIL per-node connections still present (delete them in Admin Panel -> Settings -> Connections):" >&2; while IFS= read -r line; do echo "       $line" >&2; done <<<"$old"; bad=1; fi
  local nu nk nc
  nu="$(jq 'length' <<<"$URLS")"; nk="$(jq 'length' <<<"$KEYS")"; nc="$(jq 'length' <<<"$CONFIGS")"
  if [[ "$nu" == "$nk" && "$nu" == "$nc" ]]; then echo "ok   urls, keys and configs are index-aligned ($nu each)"
  else echo "FAIL the lists differ in length (urls=$nu keys=$nk configs=$nc)" >&2; bad=1; fi
  return "$bad"
}

cmd_restore() {
  [[ "${1:-}" == "--yes" && $# -eq 1 ]] || die "restore replaces the live settings with $BACKUP. Run: $0 restore --yes"
  need jq
  [[ -r "$BACKUP" ]] || die "no backup at $BACKUP"
  local rows k v tag sql=""
  # shellcheck disable=SC2016  # a literal SQL dollar-quote tag, not a shell expression
  tag='$owui_restore$'
  rows="$(cat "$BACKUP")"
  for k in openai.api_base_urls openai.api_keys openai.api_configs; do
    v="$(value_of "$k" "$rows")" || die "$BACKUP has no $k row. Nothing was changed"
    jq -e . <<<"$v" >/dev/null 2>&1 || die "the $k value in $BACKUP is not one line of JSON. Nothing was changed. Restore it in the Admin Panel"
    [[ "$v" != *"$tag"* ]] || die "the $k value contains the quoting tag. Nothing was changed"
    sql+="UPDATE config SET value = ${tag}${v}${tag}::json WHERE key = '${k}';"$'\n'
  done
  # The SQL (it holds the old keys) goes on stdin, never on a command line.
  printf 'BEGIN;\n%sCOMMIT;\n' "$sql" | docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$DB" -v ON_ERROR_STOP=1 >/dev/null \
    || die "the restore failed and was rolled back. $SCHEMA_HINT"
  echo "ok: the three settings are back as they were in the backup. Now: docker compose restart open-webui (in /opt/infra/apps/openwebui), wait 20 to 40 s"
}

cmd_verify() {
  local code
  # The key is expanded INSIDE the container; it is never printed and never on this shell's command line.
  code="$(docker exec "$OWUI_CONTAINER" sh -c 'curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $OPENAI_API_KEY" '"${GATEWAY_URL%/}"'/models')" \
    || die "could not run curl inside the $OWUI_CONTAINER container. Is it running? (docker ps)"
  code="${code//[$'\r\n']/}"
  echo "$code"
  case "$code" in
    200) echo "ok: the gateway accepts the key in the container's environment" >&2 ;;
    401) echo "FAIL: 401. OPENAI_API_KEY in the container is not a valid lai_ key (LAI-GW-010). Put the service key in apps/openwebui/.env and recreate the container" >&2; return 1 ;;
    000|502|503) echo "FAIL: $code. Open WebUI may still be starting (wait 20 to 40 s) or the gateway is unreachable (LAI-GW-051)" >&2; return 1 ;;
    *) echo "FAIL: unexpected HTTP $code. See docs/runbooks/07-troubleshooting.md" >&2; return 1 ;;
  esac
}

[[ $# -ge 1 ]] || { usage >&2; exit 2; }
need docker
case "$1" in
  backup)  shift; cmd_backup "$@" ;;
  inspect) shift; [[ $# -eq 0 ]] || { usage >&2; exit 2; }; cmd_inspect ;;
  check)   shift; [[ $# -eq 0 ]] || { usage >&2; exit 2; }; cmd_check ;;
  restore) shift; cmd_restore "$@" ;;
  verify)  shift; [[ $# -eq 0 ]] || { usage >&2; exit 2; }; cmd_verify ;;
  -h|--help) usage ;;
  *) usage >&2; exit 2 ;;
esac
