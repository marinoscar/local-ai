#!/usr/bin/env bash
# install-node.sh: turn an Ubuntu machine into a Local AI node, online or offline from an AI-ARK drive.
# Spec: docs/specs/installer.md (this file is the TEMPLATE of section 3.6; releases ship a stamped copy).
#
#   sudo bash install-node.sh --node-id ai-n4 [--enroll | --enroll-file <path>] [--dry-run] [...]
#   sudo bash install-node.sh --from-ark /media/<user>/AI-ARK
#   bash install-node.sh --help
#
# This file is the driver skeleton (INST-01): flags, scrubbed log, phase state, dry run, lock, exit codes.
# Phases that later items fill in are named no-op phases that say so out loud; they never pretend to work.
#
# Exit: 0 success (maybe "reboot required"), 1 a phase failed, 2 bad arguments, 3 preflight refused, 4 user aborted.
#
# Secrets: a credential or PIN is never an argument. They are read from /dev/tty or from a file, kept in shell
# variables only (never exported), and every line that reaches the screen or the log passes through scrub().

# shellcheck disable=SC2034,SC2317,SC2329
# (SC2034: options are consumed by phases added by later items. SC2317/SC2329: the dry-run helpers and the
#  phase functions are called indirectly, by name, by those same phases.)

# Stamped by scripts/release/stamp-installer.sh (installer.md section 3.6). The tokens below are replaced
# everywhere they occur; the comparison in main() splits one in two so a checkout is detected as "never stamped".
INSTALLER_TAG="@@LAI_RELEASE_TAG@@"
INSTALLER_VERSION="@@LAI_RELEASE_VERSION@@"
INSTALLER_MINISIGN_PUBKEY="@@LAI_MINISIGN_PUBKEY@@"

# ---------------------------------------------------------------------------------------------------------------
# Constants and defaults
# ---------------------------------------------------------------------------------------------------------------
readonly NODE_ID_RE='^ai-n[1-9][0-9]{0,3}$'
readonly DEFAULT_LOG=/var/log/localai-install.log
readonly DEFAULT_STATE_DIR=/var/lib/localai/install
readonly LOG_MAX_BYTES=$((10 * 1024 * 1024))
readonly PHASE_TOTAL=15

# Phase table (installer.md section 4.3). P0 (arguments and log) is the driver itself; P1..P15 are listed here.
# Fields: id|function suffix|display name|owner item and issue.
readonly -a PHASE_TABLE=(
  "P1|preflight|preflight|INST-02, issue #148"
  "P2|stage|stage artifacts|INST-02, issue #148"
  "P3|profile|profile|INST-02, issue #148"
  "P4|hardening|laptop hardening|INST-03, issue #116"
  "P5|users_dirs|users, groups and directories|INST-04, issue #149"
  "P6|packages|packages|INST-04, issue #149"
  "P7|llama|llama.cpp build or reuse|INST-05, issue #151"
  "P8|migration|migration|INST-05, issue #151"
  "P9|agent|agent, node.yaml, units|INST-06, issue #153"
  "P10|api_key|API key|INST-06, issue #153"
  "P11|tailscale|tailscale|INST-07, issue #154"
  "P12|pin|admin PIN|INST-06, issue #153"
  "P13|enroll|cloud enrollment|INST-06, issue #153"
  "P14|content|initial content|INST-07, issue #154"
  "P15|verify|verify and summary|INST-07, issue #154"
)
readonly REPAIR_UNINSTALL_OWNER="INST-08, issue #155"

# Test and development hooks. They are honoured ONLY when LAI_INSTALLER_DEV=1, so a node install never reads them.
DEV=0
[[ "${LAI_INSTALLER_DEV:-0}" == "1" ]] && DEV=1

# Options (filled by parse_args)
OPT_NODE_ID=""; OPT_PROFILE=""; OPT_BACKEND=""; OPT_FROM_ARK=""; OPT_ENROLL=0; OPT_ENROLL_FILE=""
OPT_CLOUD_URL="https://ai.marin.cr"; OPT_MIGRATE="auto"; DRY_RUN=0; OPT_YES=0; OPT_PIN_FILE=""; OPT_INVENTORY=""
OPT_KEEP_INVENTORY=0; OPT_HARDENING="auto"; OPT_NO_SSH=0; OPT_NO_POLKIT=0; OPT_NO_TAILSCALE=""; OPT_NO_SYNC=0
OPT_SERVE_PATTERN=""; OPT_REBOOT=0; OPT_REPAIR=0; OPT_UNINSTALL=0; OPT_PURGE_CONFIG=0; OPT_PURGE_CONTENT=0
OPT_REVERT_HARDENING=0; OPT_RESTORE_LEGACY=0; LOG="$DEFAULT_LOG"; OPT_DESKTOP_USER=""; OPT_AGENT_SHA256=""
OPT_VERBOSE=0; OPT_HELP=0; OPT_VERSION=0

# Runtime state
STATE_DIR="$DEFAULT_STATE_DIR"; STATE_FILE=""; LOCK_FILE=""; SYSTEMD_DIR=/etc/systemd/system
CUR_TAG="P0 args"; CUR_PHASE_ID=""; LAST_OUTPUT=(); XTRACE_PID=""; STATE_TMP=""; LOG_OPEN=""
declare -A PH_STATUS=() PH_VERSION=() PH_FINISHED=() PH_DETAILS=()
INSTALL_KIND="fresh"; STUBS_RUN=0

# ---------------------------------------------------------------------------------------------------------------
# scrub: remove secrets from anything that reaches the screen or the log (installer.md section 4.6).
# Reads stdin, writes stdout. A secret becomes <redacted:prefix...last4>.
# ---------------------------------------------------------------------------------------------------------------
scrub() {
  sed -u -E \
    -e 's/[0-9a-fA-F]{60,}([0-9a-fA-F]{4})/<redacted:hex…\1>/g' \
    -e 's/(^|[^A-Za-z0-9])(nod_|lat_|lai_|pat_)[A-Za-z0-9_-]*([A-Za-z0-9_-]{4})/\1<redacted:\2…\3>/g'
}

# ---------------------------------------------------------------------------------------------------------------
# Logging. Screen: the message. Log: "<UTC ts> [<phase tag>] <message>", scrubbed.
# ---------------------------------------------------------------------------------------------------------------
now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }

log_only() {
  [[ -n "$LOG_OPEN" ]] || return 0
  local line
  while IFS= read -r line; do
    printf '%s [%s] %s\n' "$(now_utc)" "$CUR_TAG" "$line" | scrub >>"$LOG"
  done <<<"$*"
}

say() {
  printf '%s\n' "$*" | scrub
  log_only "$*"
}

# leave <exit>: a deliberate exit; clears the ERR trap first (bash would otherwise report "exit N" as a failure).
leave() {
  trap - ERR
  exit "$1"
}

# fail <exit> <doctor-code> <cause and one-sentence fix>: the way a refusal leaves the script.
fail() {
  local rc="$1" code="$2"
  shift 2
  say "ERROR $code: $*"
  say "More: docs/specs/doctor-codes.md ($code)"
  leave "$rc"
}

# usage_error <message>: exit 2 before anything is changed.
usage_error() {
  printf 'install-node.sh: %s\n' "$*" | scrub >&2
  printf 'Try: install-node.sh --help\n' >&2
  leave 2
}

on_error() {
  local rc=$?
  local line="$1" cmd="$2"
  trap - ERR
  say "FAILED in $CUR_TAG at line $line: '$cmd' exited $rc"
  if ((${#LAST_OUTPUT[@]} > 0)); then
    say "last ${#LAST_OUTPUT[@]} lines of its output:"
    local l
    for l in "${LAST_OUTPUT[@]}"; do say "  | $l"; done
  fi
  say "No doctor code is registered for an unexpected failure: read the log at $LOG and report it."
  if [[ -n "$CUR_PHASE_ID" ]]; then
    set_phase "$CUR_PHASE_ID" failed "exit $rc at line $line" || true
  fi
  leave 1
}

on_signal() {
  trap - INT TERM
  say "Aborted by the user (signal). Nothing is half-written; re-run to resume."
  leave 4
}

cleanup() {
  local rc=$?
  if [[ -n "$XTRACE_PID" ]]; then
    set +x
    exec 4>&-
    wait "$XTRACE_PID" 2>/dev/null || true
  fi
  [[ -z "$STATE_TMP" ]] || rm -f "$STATE_TMP"
  return "$rc"
}

# run_logged <cmd...>: run a command, send its combined output through the log line by line, keep the last
# 50 lines for on_error. Returns the command's exit code (carried as a final marker line).
run_logged() {
  LAST_OUTPUT=()
  local line rc=0 seen=0
  while IFS= read -r line; do
    if [[ "$line" == __LAI_RC__=* ]]; then
      rc="${line#__LAI_RC__=}"
      seen=1
      continue
    fi
    say "  $line"
    LAST_OUTPUT+=("$line")
    ((${#LAST_OUTPUT[@]} <= 50)) || LAST_OUTPUT=("${LAST_OUTPUT[@]:1}")
  done < <(
    "$@" 2>&1
    echo "__LAI_RC__=$?"
  )
  ((seen)) || rc=1
  return "$rc"
}

# ---------------------------------------------------------------------------------------------------------------
# Dry-run helpers (installer.md section 4.2). A phase changes the system ONLY through these (or through an
# `aiagent setup <step>` call that passes --dry-run on). Checks (reading) run for real.
# ---------------------------------------------------------------------------------------------------------------
quote_cmd() {
  local out="" a
  for a in "$@"; do out+="$(printf '%q ' "$a")"; done
  printf '%s' "${out% }"
}

# run_or_say <cmd...>
run_or_say() {
  if ((DRY_RUN)); then
    say "[dry-run] would run: $(quote_cmd "$@")"
    return 0
  fi
  say "run: $(quote_cmd "$@")"
  local rc=0
  run_logged "$@" || rc=$?
  return "$rc"
}

# write_file_or_diff <path> <mode> <owner[:group]>   (new content on stdin)
# Unchanged content is left alone ("already up to date"). A replaced file is first copied to
# <state>/backup/<UTC-ts>/<original path> (installer.md section 3.4).
write_file_or_diff() {
  local path="$1" mode="$2" owner="$3" tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/lai-new.XXXXXX")"
  cat >"$tmp"
  if [[ -e "$path" ]] && cmp -s "$tmp" "$path"; then
    say "already up to date: $path"
    rm -f "$tmp"
    return 0
  fi
  if ((DRY_RUN)); then
    if [[ -e "$path" ]]; then
      say "[dry-run] would change $path (mode $mode, owner $owner), diff:"
      diff -u "$path" "$tmp" | tail -n +3 | head -n 60 | sed 's/^/[dry-run]   /' | scrub || true
    else
      say "[dry-run] would create $path (mode $mode, owner $owner), content:"
      head -n 60 "$tmp" | sed 's/^/[dry-run]   + /' | scrub
    fi
    rm -f "$tmp"
    return 0
  fi
  if [[ -e "$path" ]]; then
    [[ -n "${BACKUP_TS:-}" ]] || BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)"
    local bdir
    bdir="$STATE_DIR/backup/$BACKUP_TS$(dirname "$path")"
    mkdir -p "$bdir"
    cp -p "$path" "$bdir/"
    say "backed up $path to $bdir/"
  fi
  mkdir -p "$(dirname "$path")"
  local dest_tmp
  dest_tmp="$(mktemp "$(dirname "$path")/.lai-install.XXXXXX")"
  cat "$tmp" >"$dest_tmp"
  chmod "$mode" "$dest_tmp"
  chown "$owner" "$dest_tmp" || { rm -f "$tmp" "$dest_tmp"; return 1; }
  mv -f "$dest_tmp" "$path"
  rm -f "$tmp"
  say "wrote $path (mode $mode, owner $owner)"
}

# svc_or_say <verb> [unit]   (verbs: enable disable start stop restart reload mask unmask daemon-reload)
# Never restarts, stops or reloads systemd-logind: the laptop lid settings take effect at the next boot (P4).
svc_or_say() {
  local verb="$1" unit="${2:-}"
  case "$verb" in
    enable|disable|start|stop|restart|reload|mask|unmask)
      [[ -n "$unit" ]] || { say "svc_or_say: $verb needs a unit"; return 2; } ;;
    daemon-reload) ;;
    *) say "svc_or_say: unsupported verb '$verb'"; return 2 ;;
  esac
  case "$unit" in
    systemd-logind|systemd-logind.service)
      case "$verb" in
        restart|stop|reload) say "refusing to $verb $unit (never restart logind; reboot instead)"; return 2 ;;
      esac ;;
  esac
  if [[ -n "$unit" ]]; then
    run_or_say systemctl "$verb" "$unit"
  else
    run_or_say systemctl "$verb"
  fi
}

# apt_or_say <package...>: install what is missing. Dry run: apt-get -s, summarised.
apt_or_say() {
  local pkgs=("$@") missing=() p
  ((${#pkgs[@]} > 0)) || return 0
  for p in "${pkgs[@]}"; do
    # shellcheck disable=SC2016
    dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'install ok installed' || missing+=("$p")
  done
  if ((${#missing[@]} == 0)); then
    say "already installed: ${pkgs[*]}"
    return 0
  fi
  if ((DRY_RUN)); then
    local planned="" n=0
    if command -v apt-get >/dev/null 2>&1; then
      planned="$(apt-get -s install --no-install-recommends "${missing[@]}" 2>/dev/null | awk '/^Inst /{printf "%s ", $2}' || true)"
      n="$(wc -w <<<"$planned")"
    fi
    if ((n > 0)); then
      say "[dry-run] would install $n package(s) with apt-get: ${planned% }"
    else
      say "[dry-run] would install with apt-get: ${missing[*]}"
    fi
    return 0
  fi
  run_or_say env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${missing[@]}"
}

# confirm <question>: yes with --yes, else ask on the terminal; "no" or no answer is exit 4 (user aborted).
confirm() {
  if ((OPT_YES)); then
    say "assume yes: $1"
    return 0
  fi
  local answer=""
  if { : </dev/tty; } 2>/dev/null; then
    printf '%s [y/N] ' "$1" >/dev/tty
    read -r answer </dev/tty || true
  fi
  [[ "$answer" == [yY] || "$answer" == [yY][eE][sS] ]] || { say "Not confirmed: $1"; leave 4; }
}

# ---------------------------------------------------------------------------------------------------------------
# Phase state: /var/lib/localai/install/state.json  (one phase per line; written atomically: tmp + mv).
#   "P6": {"name":"packages","status":"done","version":"0.1.0","finished_at":"<UTC>","details":"..."}
# status: running done failed not-implemented
# ---------------------------------------------------------------------------------------------------------------
json_escape() {
  local s="$1"
  s="${s//\\/\\\\}"; s="${s//\"/\\\"}"; s="${s//$'\n'/ }"; s="${s//$'\t'/ }"; s="${s//$'\r'/ }"
  printf '%s' "$s"
}

state_load() {
  PH_STATUS=(); PH_VERSION=(); PH_FINISHED=(); PH_DETAILS=()
  [[ -f "$STATE_FILE" ]] || return 0
  local line id
  while IFS= read -r line; do
    id="$(sed -n -E 's/^ *"(P[0-9]+)": \{.*/\1/p' <<<"$line")"
    [[ -n "$id" ]] || continue
    PH_STATUS[$id]="$(sed -n -E 's/.*"status":"([^"]*)".*/\1/p' <<<"$line")"
    PH_VERSION[$id]="$(sed -n -E 's/.*"version":"([^"]*)".*/\1/p' <<<"$line")"
    PH_FINISHED[$id]="$(sed -n -E 's/.*"finished_at":"([^"]*)".*/\1/p' <<<"$line")"
    PH_DETAILS[$id]="$(sed -n -E 's/.*"details":"(.*)"\}.*/\1/p' <<<"$line")"
  done <"$STATE_FILE"
}

state_write() {
  ((DRY_RUN)) && return 0
  mkdir -p "$STATE_DIR"
  STATE_TMP="$(mktemp "$STATE_DIR/.state.XXXXXX")"
  {
    printf '{\n  "schema": 1,\n  "installer_version": "%s",\n  "updated_at": "%s",\n  "phases": {\n' \
      "$(json_escape "$INSTALLER_VERSION")" "$(now_utc)"
    local entry id name first=1
    for entry in "${PHASE_TABLE[@]}"; do
      id="${entry%%|*}"
      [[ -n "${PH_STATUS[$id]:-}" ]] || continue
      name="$(cut -d'|' -f3 <<<"$entry")"
      ((first)) || printf ',\n'
      first=0
      printf '    "%s": {"name":"%s","status":"%s","version":"%s","finished_at":"%s","details":"%s"}' \
        "$id" "$(json_escape "$name")" "${PH_STATUS[$id]}" "$(json_escape "${PH_VERSION[$id]:-}")" \
        "${PH_FINISHED[$id]:-}" "$(json_escape "${PH_DETAILS[$id]:-}")"
    done
    printf '\n  }\n}\n'
  } >"$STATE_TMP"
  chmod 0600 "$STATE_TMP"
  mv -f "$STATE_TMP" "$STATE_FILE"
  STATE_TMP=""
}

# set_phase <id> <status> [details]
set_phase() {
  local id="$1" status="$2" details="${3:-}"
  PH_STATUS[$id]="$status"; PH_VERSION[$id]="$INSTALLER_VERSION"; PH_DETAILS[$id]="$details"
  case "$status" in
    running) PH_FINISHED[$id]="" ;;
    *) PH_FINISHED[$id]="$(now_utc)" ;;
  esac
  if ((DRY_RUN)); then
    [[ "$status" == running ]] || say "[dry-run] would record $id as '$status' in $STATE_FILE"
  else
    state_write
  fi
}

# ---------------------------------------------------------------------------------------------------------------
# Phases. For each phase <x> there are two functions:
#   check_<x>  idempotency hook: return 0 when the phase is already satisfied (cheap, read-only), else non-zero.
#   phase_<x>  do the work through the dry-run helpers; return 0 done, 10 not implemented yet, else failed.
# A later item replaces the stub of its phases (redefine both functions after define_phase_stubs, or delete the
# table row's stub by defining the functions in its own section); the runner, state and logging stay as they are.
# ---------------------------------------------------------------------------------------------------------------
readonly PHASE_NOT_IMPLEMENTED=10

# not_implemented <owner>: the explicit named no-op. Always loud; never counts as done.
not_implemented() {
  say "NOT IMPLEMENTED YET: this phase does nothing in this version of the installer (owner: $1)."
  return "$PHASE_NOT_IMPLEMENTED"
}

# One stub pair per table row, so the registry and the functions cannot drift apart.
define_phase_stubs() {
  local entry fn owner
  for entry in "${PHASE_TABLE[@]}"; do
    fn="$(cut -d'|' -f2 <<<"$entry")"
    owner="$(cut -d'|' -f4 <<<"$entry")"
    eval "check_${fn}() { return 1; }"
    eval "phase_${fn}() { not_implemented '${owner}'; }"
  done
}
define_phase_stubs

# ===============================================================================================================
# INST-05 (issue #151): P7 llama.cpp build or reuse, P8 migration hand-off. Spec: installer.md section 4.3 P7/P8.
# Dev hooks (LAI_INSTALLER_DEV=1 only): LAI_INSTALL_LLAMA_ROOT, LAI_INSTALL_NODE_YAML, LAI_INSTALL_PINS,
# LAI_INSTALL_AICTL, LAI_INSTALL_AIAGENT, LAI_INSTALL_SYSTEMCTL, LAI_INSTALL_SS, LAI_INSTALL_CURL,
# LAI_INSTALL_HOME_ROOT. P7 reads these variables if an earlier phase set them: OPT_BACKEND (or DETECTED_BACKEND)
# and DETECTED_CUDA_ARCH.
# ===============================================================================================================
llama_init_paths() {
  LLAMA_ROOT=/opt/localai/llama.cpp; NODE_YAML=/etc/localai/node.yaml; PINS_INSTALLED=/etc/localai/catalog/pins.json
  AICTL=/usr/local/bin/aictl; AIAGENT=/opt/localai/bin/aiagent; SYSTEMCTL=systemctl; SS_BIN=ss; CURL_BIN=curl
  HOME_ROOT=""
  if ((DEV)); then
    LLAMA_ROOT="${LAI_INSTALL_LLAMA_ROOT:-$LLAMA_ROOT}"; NODE_YAML="${LAI_INSTALL_NODE_YAML:-$NODE_YAML}"
    PINS_INSTALLED="${LAI_INSTALL_PINS:-$PINS_INSTALLED}"; AICTL="${LAI_INSTALL_AICTL:-$AICTL}"
    AIAGENT="${LAI_INSTALL_AIAGENT:-$AIAGENT}"; SYSTEMCTL="${LAI_INSTALL_SYSTEMCTL:-$SYSTEMCTL}"
    SS_BIN="${LAI_INSTALL_SS:-$SS_BIN}"; CURL_BIN="${LAI_INSTALL_CURL:-$CURL_BIN}"; HOME_ROOT="${LAI_INSTALL_HOME_ROOT:-}"
  fi
}

# llama_fail <exit> <code> <message>: record the phase as failed, then leave with the doctor code.
llama_fail() {
  local rc="$1" code="$2"
  shift 2
  [[ -z "$CUR_PHASE_ID" ]] || set_phase "$CUR_PHASE_ID" failed "$code" || true
  fail "$rc" "$code" "$*"
}

# node_yaml_get <block> <key>: a scalar one level down in node.yaml ("llama:" then "  commit: abc"); empty if absent.
node_yaml_get() {
  [[ -r "$NODE_YAML" ]] || return 0
  awk -v b="$1" -v k="$2" '
    /^[^ #\t]/ { inb = ($0 ~ ("^" b ":")) }
    inb && $1 == k ":" { v = $2; gsub(/^["\047]|["\047]$/, "", v); print v; exit }' "$NODE_YAML"
}

# pins_commit <pins.json> <node-id>: the llama_cpp commit pinned for this node (else the first entry); empty if none.
# The entries of "llama_cpp" are flat objects, so no JSON tool is needed (jq is not installed on a fresh node).
pins_commit() {
  local file="$1" node="$2" flat obj c first=""
  [[ -r "$file" ]] || return 0
  flat="$(tr -d '\n' <"$file" | sed -n -E 's/.*"llama_cpp"[[:space:]]*:[[:space:]]*\[//p' | sed -E 's/\}[[:space:]]*\].*/}/')"
  while IFS= read -r obj; do
    c="$(sed -n -E 's/.*"commit"[[:space:]]*:[[:space:]]*"([0-9a-f]{7,40})".*/\1/p' <<<"$obj")"
    [[ -n "$c" ]] || continue
    [[ -n "$first" ]] || first="$c"
    if [[ -n "$node" ]] && grep -q "\"$node\"" <<<"$obj"; then printf '%s\n' "$c"; return 0; fi
  done < <(grep -o '{[^{}]*}' <<<"$flat")
  printf '%s\n' "$first"
}

# llama_resolve: sets LLAMA_COMMIT, LLAMA_BACKEND, LLAMA_BUILD_DIR, LLAMA_SRC, LLAMA_COMMIT_FROM. Returns 1 when no commit.
llama_resolve() {
  llama_init_paths
  local node="${OPT_NODE_ID:-}" c="" from="" kitpins=""
  [[ -n "$node" ]] || node="$(sed -n -E 's/^node_id:[[:space:]]*"?([^"# ]*).*/\1/p' "$NODE_YAML" 2>/dev/null | head -n1)"
  c="$(node_yaml_get llama commit)"; [[ -z "$c" ]] || from="node.yaml (llama.commit)"
  if [[ -z "$c" && -n "$OPT_FROM_ARK" ]]; then
    kitpins="$OPT_FROM_ARK/localai/kit/catalog/pins.json"
    c="$(pins_commit "$kitpins" "$node")"; [[ -z "$c" ]] || from="the kit's pins.json"
  fi
  if [[ -z "$c" ]]; then c="$(pins_commit "$PINS_INSTALLED" "$node")"; [[ -z "$c" ]] || from="the installed catalog's pins.json"; fi
  LLAMA_COMMIT="$c"; LLAMA_COMMIT_FROM="$from"
  LLAMA_BACKEND="${OPT_BACKEND:-${DETECTED_BACKEND:-}}"
  [[ -n "$LLAMA_BACKEND" ]] || LLAMA_BACKEND="$(node_yaml_get llama backend)"
  case "$LLAMA_BACKEND" in cuda|vulkan|cpu) ;; *) LLAMA_BACKEND=cpu ;; esac
  [[ -n "$c" ]] || return 1
  LLAMA_BUILD_DIR="$LLAMA_ROOT/builds/${c:0:9}-$LLAMA_BACKEND"
  LLAMA_SRC="$LLAMA_ROOT/src/$c"
}

# llama_build_ok <build-dir> <commit>: llama-server exists and its --version names the first 7 hex of the commit.
llama_build_ok() {
  local bin="$1/bin/llama-server" out
  [[ -x "$bin" ]] || return 1
  out="$(timeout 30 "$bin" --version 2>&1)" || true
  grep -qF -- "${2:0:7}" <<<"$out"
}

check_llama() {
  llama_resolve || return 1
  llama_build_ok "$LLAMA_BUILD_DIR" "$LLAMA_COMMIT"
}

# llama_home_checkout: prints the desktop user's ~/llama.cpp when it holds the commit, else nothing.
llama_home_checkout() {
  local user="${OPT_DESKTOP_USER:-}" home="" repo
  [[ -n "$user" ]] || return 0
  if [[ -n "$HOME_ROOT" ]]; then home="$HOME_ROOT/$user"; else home="$(getent passwd "$user" | cut -d: -f6)"; fi
  repo="$home/llama.cpp"
  [[ -d "$repo" ]] || return 0
  if "${LLAMA_AS_USER[@]}" git -C "$repo" cat-file -e "$LLAMA_COMMIT^{commit}" 2>/dev/null; then printf '%s\n' "$repo"; fi
}

# llama_flatten_src <extracted-dir> <final-dir>: drop a single top-level folder, set 0755, move into place.
llama_flatten_src() {
  local tmp="$1" dest="$2" n top
  n="$(find "$tmp" -mindepth 1 -maxdepth 1 | wc -l)"
  top="$(find "$tmp" -mindepth 1 -maxdepth 1 -type d | head -n1)"
  rm -rf "$dest"
  if [[ ! -f "$tmp/CMakeLists.txt" && "$n" -eq 1 && -n "$top" ]]; then mv "$top" "$dest"; rmdir "$tmp"; else mv "$tmp" "$dest"; fi
  chmod 0755 "$dest"
  [[ -f "$dest/CMakeLists.txt" ]] || { rm -rf "$dest"; return 1; }
}

# llama_prepare_source: put the source of $LLAMA_COMMIT into $LLAMA_SRC (the only source location, llama-router.md
# section 4.10.1). Order: already there, the desktop user's ~/llama.cpp, the kit tarball, GitHub (online only).
llama_prepare_source() {
  local c="$LLAMA_COMMIT" src="$LLAMA_SRC" tmp repo tarball="" url sum
  LLAMA_AS_USER=(); if ((!DEV)) && [[ -n "${OPT_DESKTOP_USER:-}" ]]; then LLAMA_AS_USER=(sudo -u "$OPT_DESKTOP_USER"); fi
  if [[ -f "$src/CMakeLists.txt" ]]; then say "source already in place: $src"; return 0; fi
  tmp="$src.tmp"
  repo="$(llama_home_checkout)"
  if [[ -n "$repo" ]]; then
    say "source: your existing checkout $repo has commit ${c:0:9}; exporting it to $src (nothing in it is changed or deleted)"
    if ((DRY_RUN)); then
      say "[dry-run] would run: git -C $repo archive ${c:0:9} | tar -x -C $tmp, then move it to $src"
      return 0
    fi
    rm -rf "$tmp"; mkdir -p "$tmp"
    say "run: git -C $repo archive $c | tar -x -C $tmp"
    # shellcheck disable=SC2310
    if ! (umask 022; set -o pipefail; "${LLAMA_AS_USER[@]}" git -C "$repo" archive "$c" | tar -x -C "$tmp"); then
      rm -rf "$tmp"
      llama_fail 1 LAI-INS-032 "could not export commit ${c:0:9} from $repo. Re-run and the installer will try the kit or the internet instead; if it repeats, run 'sudo install-node.sh --repair'."
    fi
  else
    if [[ -n "$OPT_FROM_ARK" ]]; then
      tarball="$OPT_FROM_ARK/localai/kit/src/llama.cpp-$c.tar.gz"
      if [[ ! -f "$tarball" ]]; then
        llama_fail 1 LAI-INS-032 "the AI-ARK drive has no llama.cpp source for commit ${c:0:9} (looked for $tarball) and this is an offline install. Fix: run 'aictl kit verify' on a node that has internet, re-sync the drive, then re-run."
      fi
      say "source: kit tarball $tarball (offline)"
      sum="$(sha256sum "$tarball" | cut -d' ' -f1)"; say "sha256 $(basename "$tarball") $sum"
    else
      url="https://github.com/ggml-org/llama.cpp/archive/$c.tar.gz"
      say "source: downloading $url (online)"
      if ((DRY_RUN)); then
        say "[dry-run] would download $url, record its sha256, extract it to $src"
        return 0
      fi
      mkdir -p "$LLAMA_ROOT/src"
      tarball="$LLAMA_ROOT/src/.download-$c.tar.gz"
      if ! run_or_say "$CURL_BIN" -fL --retry 3 --connect-timeout 15 -o "$tarball" "$url"; then
        rm -f "$tarball"
        llama_fail 1 LAI-INS-032 "downloading llama.cpp ${c:0:9} from GitHub failed. Check the internet connection and re-run, or use --from-ark with a drive that has the source."
      fi
      sum="$(sha256sum "$tarball" | cut -d' ' -f1)"; say "sha256 llama.cpp-$c.tar.gz $sum"
    fi
    if ((DRY_RUN)); then
      say "[dry-run] would extract $tarball to $src"
      return 0
    fi
    rm -rf "$tmp"; mkdir -p "$tmp"
    say "run: tar -xzf $tarball -C $tmp"
    # shellcheck disable=SC2310
    if ! (umask 022; tar -xzf "$tarball" -C "$tmp"); then
      rm -rf "$tmp"; [[ "$tarball" != "$LLAMA_ROOT"/src/.download-* ]] || rm -f "$tarball"
      llama_fail 1 LAI-INS-032 "the llama.cpp source file is damaged and could not be unpacked. Re-sync the drive or re-run to download it again."
    fi
    [[ "$tarball" != "$LLAMA_ROOT"/src/.download-* ]] || rm -f "$tarball"
  fi
  # shellcheck disable=SC2310
  llama_flatten_src "$tmp" "$src" || llama_fail 1 LAI-INS-032 "the unpacked source in $src has no CMakeLists.txt, so it is not llama.cpp. It was removed; re-run."
  say "source ready: $src"
}

phase_llama() {
  if ! llama_resolve && ((DRY_RUN)); then
    # The catalog (and its pins.json) is installed in phase 2, which a dry run does not do.
    say "[dry-run] cannot tell yet which llama.cpp commit this node needs (the catalog is installed in phase 2); a real run would reuse a good build or build it here"
    return 0
  fi
  # shellcheck disable=SC2310
  llama_resolve || llama_fail 1 LAI-INS-032 "no llama.cpp commit is known for this node: there is no llama.commit in $NODE_YAML and no pins.json in the kit or in $PINS_INSTALLED. Re-run after the catalog is installed (phase 2), or use --from-ark."
  say "llama.cpp commit ${LLAMA_COMMIT:0:9} for backend $LLAMA_BACKEND (from $LLAMA_COMMIT_FROM)"
  # shellcheck disable=SC2310
  if llama_build_ok "$LLAMA_BUILD_DIR" "$LLAMA_COMMIT"; then
    say "already built and working: $LLAMA_BUILD_DIR (nothing to rebuild)"
    return 0
  fi
  [[ ! -e "$LLAMA_BUILD_DIR/bin/llama-server" ]] || say "the existing build in $LLAMA_BUILD_DIR does not pass its test; it will be rebuilt"
  llama_prepare_source
  local args=(rebuild-llama --commit "$LLAMA_COMMIT" --backend "$LLAMA_BACKEND" --source "$LLAMA_SRC" --no-activate --yes)
  if [[ "$LLAMA_BACKEND" == cuda && -n "${DETECTED_CUDA_ARCH:-}" ]]; then args+=(--cuda-arch "$DETECTED_CUDA_ARCH"); fi
  say "Building llama.cpp can take up to 3 hours on a slow computer. Please leave this window open."
  if ! run_or_say "$AICTL" "${args[@]}"; then
    llama_fail 1 LAI-INS-032 "building llama.cpp failed. Read $LLAMA_BUILD_DIR/build.log (or the newest failed folder under $LLAMA_ROOT/builds/); the old service, if any, keeps running; then run 'sudo install-node.sh --repair'."
  fi
  if ((!DRY_RUN)); then
    say "build finished: $LLAMA_BUILD_DIR (it is switched on in phase 8 or 9, not here)"
  fi
}

# ---- P8 migration ---------------------------------------------------------------------------------------------
# llama-qwen.service is present as a real unit file (after the migration it is masked: a symlink to /dev/null).
llama_qwen_present() {
  [[ -f "$SYSTEMD_DIR/llama-qwen.service" && ! -L "$SYSTEMD_DIR/llama-qwen.service" ]]
}

# llama_qwen_holds_8080: the old service is running, or something listens on 8080 while the unit still exists.
llama_qwen_holds_8080() {
  llama_qwen_present || return 1
  "$SYSTEMCTL" is-active --quiet llama-qwen 2>/dev/null && return 0
  "$SS_BIN" -Hltn 'sport = :8080' 2>/dev/null | grep -q ':8080'
}

check_migration() {
  llama_init_paths
  ! llama_qwen_present
}

phase_migration() {
  llama_init_paths
  MIGRATION_RAN=0
  if [[ "$OPT_MIGRATE" == no ]]; then
    # shellcheck disable=SC2310
    if llama_qwen_holds_8080; then
      llama_fail 1 LAI-INS-031 "the old llama-qwen service is still using port 8080 and --no-migrate was given, so the new service cannot start. Re-run without --no-migrate."
    fi
    say "migration skipped (--no-migrate); llama-qwen is not using port 8080"
    return 0
  fi
  # shellcheck disable=SC2310
  if [[ "$OPT_MIGRATE" != yes ]] && ! llama_qwen_present; then
    say "no old llama-qwen.service found: nothing to migrate"
    return 0
  fi
  say "Migrating the old llama-qwen service to the new one. The port (8080), the key and the address stay the same."
  if ((DRY_RUN)); then
    if ((EUID == 0)) && [[ -x "$AIAGENT" ]]; then
      run_logged "$AIAGENT" setup migrate --dry-run || llama_fail 1 LAI-INS-033 "the migration dry run found a problem (see the lines above). Nothing was changed."
    else
      say "[dry-run] would run: $(quote_cmd "$AIAGENT" setup migrate) (its own dry run needs root and the installed agent, so it was not run)"
    fi
    return 0
  fi
  confirm "Replace the old llama-qwen service with the new Local AI service now? (The old one is put back automatically if anything goes wrong.)"
  local rc=0
  run_or_say "$AIAGENT" setup migrate || rc=$?
  case "$rc" in
    0) MIGRATION_RAN=1; say "migration finished" ;;
    20) llama_fail 1 LAI-INS-033 "the migration failed and was rolled back: the old llama-qwen service is running again, as before. Read the lines above and report them." ;;
    77) llama_fail 3 LAI-INS-003 "aiagent setup migrate must run as root. Run the installer with sudo." ;;
    *) llama_fail 1 LAI-INS-033 "the migration stopped (exit $rc) and could not be finished. Read the lines above: they say whether the old llama-qwen service was put back. Report them." ;;
  esac
}
# ===============================================================================================================

# INST-04 (issue #149): P5 users, groups and directories, P6 packages and companion programs.
# Spec: docs/specs/installer.md section 4.3 P5 and P6. These definitions replace the stubs above.
# Everything that needs root-owned state is done by `aiagent setup dirs` / `aiagent setup binaries` (Go, tested
# with fakes); this block only calls them, passes --dry-run through, and turns a failure into a doctor code.
# ===============================================================================================================

# The package groups of section 4.3 P6. installers/packages/*.list holds the same names; test-installer.sh
# checks that the two agree. Backend groups are added for the detected backend only.
readonly -a INST04_PKGS_BASE=(ca-certificates curl jq git iw network-manager pciutils usbutils minisign zstd udisks2)
readonly -a INST04_PKGS_BUILD=(build-essential cmake ninja-build pkg-config ccache)
readonly -a INST04_PKGS_VULKAN=(libvulkan-dev glslc spirv-headers vulkan-tools mesa-vulkan-drivers)
readonly -a INST04_PKGS_CUDA=(nvidia-cuda-toolkit)
readonly -a INST04_TOOLS=(kiwix-tools pmtiles piper)

# inst04_bin <aiagent|aictl>: where the program staged by P2 lives. Dev hooks only with LAI_INSTALLER_DEV=1.
inst04_bin() {
  # With the dev hooks on, a test that does not name a fake gets a path that does not exist, so no test ever runs
  # the real programs of the machine it happens to run on.
  if ((DEV)); then
    case "$1" in
      aiagent) printf '%s' "${LAI_INSTALL_AIAGENT:-/nonexistent/dev/aiagent}" ;;
      aictl) printf '%s' "${LAI_INSTALL_AICTL:-/nonexistent/dev/aictl}" ;;
    esac
    return 0
  fi
  case "$1" in
    aiagent) printf '%s' /opt/localai/bin/aiagent ;;
    aictl) printf '%s' /usr/local/bin/aictl ;;
  esac
}

inst04_opt_dir() {
  if ((DEV)); then printf '%s' "${LAI_INSTALL_OPT_DIR:-/nonexistent/dev/opt}"; else printf '%s' /opt/localai; fi
}

inst04_catalog_dir() {
  if ((DEV)); then printf '%s' "${LAI_INSTALL_CATALOG_DIR:-/nonexistent/dev/catalog}"; else printf '%s' /etc/localai/catalog; fi
}

# inst04_backend: --backend, else what P1 detected (DETECTED_BACKEND), else cpu.
inst04_backend() { printf '%s' "${OPT_BACKEND:-${DETECTED_BACKEND:-cpu}}"; }

# inst04_packages: the names to install, one per line: base, build, then the backend's.
inst04_packages() {
  printf '%s\n' "${INST04_PKGS_BASE[@]}" "${INST04_PKGS_BUILD[@]}"
  case "$(inst04_backend)" in
    vulkan) printf '%s\n' "${INST04_PKGS_VULKAN[@]}" ;;
    cuda) printf '%s\n' "${INST04_PKGS_CUDA[@]}" ;;
  esac
}

# inst04_run_agent <bin-kind> <args...>: run one of our own programs. A dry run passes --dry-run to the step
# (the step only reads) when the program is already there; if P2 has not staged it yet (a dry run never does),
# the call is only described. A real run without the program is a failure.
# Returns the program's exit code; sets LAST_OUTPUT (run_logged).
inst04_run_agent() {
  local kind="$1" bin
  shift
  bin="$(inst04_bin "$kind")"
  if ((DRY_RUN)) && ((EUID != 0 && !DEV)); then
    # `aiagent setup` and `aictl kit` are root only (exit 77), so an unprivileged dry run only describes the call.
    say "[dry-run] would run: $kind $*"
    return 0
  fi
  if [[ ! -x "$bin" ]]; then
    if ((DRY_RUN)); then
      say "[dry-run] would run: $kind $* (the program is not installed yet; phase P2 puts it at $bin)"
      return 0
    fi
    say "The program $bin is missing, so this step cannot run. Phase P2 (stage artifacts) must finish first."
    return 1
  fi
  local rc=0
  if ((DRY_RUN)); then
    say "[dry-run] checking with: $kind $* --dry-run"
    run_logged "$bin" "$@" --dry-run || rc=$?
  else
    say "run: $kind $*"
    run_logged "$bin" "$@" || rc=$?
  fi
  return "$rc"
}

# --- P5 users, groups and directories ---------------------------------------------------------------------------

inst04_dirs_args() {
  INST04_ARGS=(setup dirs)
  if ((OPT_NO_POLKIT)); then INST04_ARGS+=(--no-polkit); fi
  if [[ -n "$OPT_DESKTOP_USER" ]]; then INST04_ARGS+=(--desktop-user "$OPT_DESKTOP_USER"); fi
  if [[ -n "$OPT_NODE_ID" ]]; then INST04_ARGS+=(--node-id "$OPT_NODE_ID"); fi
  if [[ -n "$OPT_PROFILE" ]]; then INST04_ARGS+=(--profile "$OPT_PROFILE"); fi
}

# check_users_dirs: satisfied when `aiagent setup dirs --check` finds nothing to change.
check_users_dirs() {
  local bin
  bin="$(inst04_bin aiagent)"
  [[ -x "$bin" ]] || return 1
  inst04_dirs_args
  "$bin" "${INST04_ARGS[@]}" --check >/dev/null 2>&1
}

phase_users_dirs() {
  inst04_dirs_args
  if ! inst04_run_agent aiagent "${INST04_ARGS[@]}"; then
    fail 1 LAI-INS-035 "could not create the localai user, the folders or the permissions (lines above). Fix what they name, then re-run: sudo install-node.sh"
  fi
  if [[ -n "$OPT_DESKTOP_USER" ]] && ((!DRY_RUN)); then
    say "NOTE: log out and back in as $OPT_DESKTOP_USER so the new group localai-operators applies to you."
  fi
  return 0
}

# --- P6 packages --------------------------------------------------------------------------------------------------

# inst04_apt_fail <what>: LAI-INS-030 with apt's last 20 lines.
inst04_apt_fail() {
  local n=${#LAST_OUTPUT[@]} from=0 i
  if ((n > 20)); then from=$((n - 20)); fi
  say "The last lines apt printed:"
  for ((i = from; i < n; i++)); do say "  | ${LAST_OUTPUT[i]}"; done
  fail 1 LAI-INS-030 "installing the packages failed at: $1. Read the apt messages above, fix what they name, then re-run: sudo install-node.sh"
}

inst04_packages_online() {
  local -a pkgs
  mapfile -t pkgs < <(inst04_packages)
  say "Installing the programs this node needs from the internet (apt): ${pkgs[*]}"
  if ! run_or_say apt-get update; then inst04_apt_fail "apt-get update"; fi
  if ! apt_or_say "${pkgs[@]}"; then inst04_apt_fail "apt-get install"; fi
}

# Offline: `aictl kit install-debs` is the one implementation of the spec's temporary-source install. It re-verifies
# every .deb against SHA256SUMS and PACKAGES.lock first, adds /etc/apt/sources.list.d/localai-ark.list, installs
# with only that source enabled, and removes the source file whatever happens (rebuild-kit.md section 4.1.4).
inst04_packages_offline() {
  local root="$OPT_FROM_ARK/localai" list
  local -a pkgs
  mapfile -t pkgs < <(inst04_packages)
  list="$(IFS=,; printf '%s' "${pkgs[*]}")"
  if [[ ! -d "$root/kit" ]]; then
    fail 1 LAI-INS-016 "the ark drive has no kit folder at $root/kit. Sync the drive on a computer with internet first."
  fi
  say "Installing the programs this node needs from the ark drive (no internet): ${pkgs[*]}"
  if ! inst04_run_agent aictl kit install-debs --root "$root" --packages "$list"; then
    inst04_apt_fail "aictl kit install-debs"
  fi
}

inst04_binaries() {
  local -a args=(setup binaries)
  if [[ -n "$OPT_FROM_ARK" ]]; then
    args+=(--kit "$OPT_FROM_ARK/localai/kit")
  else
    if ((DRY_RUN)) && [[ ! -f "$(inst04_catalog_dir)/ark.yaml" ]]; then
      say "[dry-run] would run: aiagent setup binaries --online (the catalog is not installed yet; phase P2 puts it in $(inst04_catalog_dir))"
      return 0
    fi
    args+=(--online --catalog "$(inst04_catalog_dir)")
  fi
  say "Installing kiwix-tools, pmtiles and piper from verified files..."
  if ! inst04_run_agent aiagent "${args[@]}"; then
    fail 1 LAI-INS-035 "could not install kiwix-tools, pmtiles and piper (lines above). Fix what they name, then re-run: sudo install-node.sh"
  fi
}

# check_packages: every package is installed and the three companion programs have a current version.
check_packages() {
  local p t
  while IFS= read -r p; do
    # shellcheck disable=SC2016
    dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'install ok installed' || return 1
  done < <(inst04_packages)
  for t in "${INST04_TOOLS[@]}"; do
    [[ -e "$(inst04_opt_dir)/$t/current" ]] || return 1
  done
  return 0
}

phase_packages() {
  if [[ -n "$OPT_FROM_ARK" ]]; then
    inst04_packages_offline
  else
    inst04_packages_online
  fi
  inst04_binaries
  return 0
}
# ===============================================================================================================
# end of INST-04
# ===============================================================================================================

# =============================================================================================================
# BEGIN P4 laptop hardening (INST-03, issue #116). Spec: installer.md section 4.3 P4.
# All the work is `aiagent setup harden` (Go, tested with fakes); this block only calls it. It never restarts
# systemd-logind: the lid settings apply after a reboot, which is why reboot_required is recorded.
# =============================================================================================================
REBOOT_REQUIRED=0

# harden_bin: the agent binary (P2 puts it at /opt/localai/bin/aiagent). Dev hook: LAI_INSTALL_AIAGENT.
harden_bin() {
  # With the dev hooks on, the real binary is never used (a test must not harden the machine it runs on).
  if ((DEV)); then printf '%s' "${LAI_INSTALL_AIAGENT:-/nonexistent/lai-dev-no-aiagent}"; else printf '%s' /opt/localai/bin/aiagent; fi
}

# harden_args <mode...>: the arguments shared by every call.
harden_args() {
  HARDEN_ARGS=(setup harden "$@" --state-dir "$STATE_DIR")
  [[ "$OPT_HARDENING" != yes ]] || HARDEN_ARGS+=(--hardening)
  ((!OPT_NO_SSH)) || HARDEN_ARGS+=(--no-ssh)
  [[ -z "$OPT_DESKTOP_USER" ]] || HARDEN_ARGS+=(--desktop-user "$OPT_DESKTOP_USER")
}

# Satisfied when hardening is off, or the agent verifies every LAI-INS-040..044 check (a pending reboot is fine).
check_hardening() {
  [[ "$OPT_HARDENING" != no ]] && [[ -x "$(harden_bin)" ]] || return 1
  harden_args --check
  "$(harden_bin)" "${HARDEN_ARGS[@]}" >/dev/null 2>&1
}

phase_hardening() {
  if [[ "$OPT_HARDENING" == no ]]; then
    say "Laptop hardening is off (--no-hardening): nothing changed."
    return 0
  fi
  local bin rc=0
  bin="$(harden_bin)"
  harden_args
  ((!DRY_RUN)) || HARDEN_ARGS+=(--dry-run)
  if [[ ! -x "$bin" ]]; then
    ((!DRY_RUN)) || say "[dry-run] would run: $(quote_cmd "$bin" "${HARDEN_ARGS[@]}")"
    # The agent is staged by P2 (INST-02, not implemented yet); until it is, say so loudly and never count this as done.
    say "The agent program $bin is not installed yet, so the laptop cannot be hardened."
    not_implemented "INST-02, issue #148 (stages the agent)"
    return
  fi
  say "run: $(quote_cmd "$bin" "${HARDEN_ARGS[@]}")"
  run_logged "$bin" "${HARDEN_ARGS[@]}" || rc=$?
  if ((rc != 0)); then
    say "Laptop hardening did not finish (see the LAI-INS-04x lines above). Fix the cause, then run: sudo install-node.sh --repair"
    return "$rc"
  fi
  if ((!DRY_RUN)) && grep -q '"reboot_required": true' "$STATE_DIR/hardening.json" 2>/dev/null; then
    REBOOT_REQUIRED=1
    PHASE_DETAILS_OUT="reboot_required=true: logind settings apply after a reboot (LAI-INS-070); details in $STATE_DIR/hardening.json"
    say "A reboot is needed to finish the lid settings (logind settings apply after a reboot). The installer does not restart logind."
  fi
}

# revert_hardening: for --uninstall --revert-hardening (INST-08 calls it). SSH, packages, users stay.
revert_hardening() {
  local bin rc=0
  bin="$(harden_bin)"
  HARDEN_ARGS=(setup harden --revert --state-dir "$STATE_DIR")
  ((!DRY_RUN)) || HARDEN_ARGS+=(--dry-run)
  if [[ ! -x "$bin" ]]; then
    say "Cannot revert the laptop hardening: $bin is missing."
    return 1
  fi
  say "run: $(quote_cmd "$bin" "${HARDEN_ARGS[@]}")"
  run_logged "$bin" "${HARDEN_ARGS[@]}" || rc=$?
  if ((!DRY_RUN)) && grep -q '"reboot_required": true' "$STATE_DIR/hardening.json" 2>/dev/null; then REBOOT_REQUIRED=1; fi
  return "$rc"
}
# END P4 laptop hardening
# =============================================================================================================

# run_phase <index 1..15> <table entry>
run_phase() {
  local n="$1" entry="$2" id fn name rc=0 start=$SECONDS
  IFS='|' read -r id fn name _ <<<"$entry"
  CUR_PHASE_ID="$id"; CUR_TAG="$id $fn"
  say "==> [$n/$PHASE_TOTAL] $name"
  if [[ "${PH_STATUS[$id]:-}" == "done" ]] && "check_${fn}"; then
    say "already done"
    CUR_PHASE_ID=""
    return 0
  fi
  PHASE_DETAILS_OUT=""   # a phase may set this; it is recorded as the details of "done"
  set_phase "$id" running ""
  "phase_${fn}" || rc=$?
  local dur=$((SECONDS - start))
  case "$rc" in
    0)
      set_phase "$id" "done" "${PHASE_DETAILS_OUT:-}"
      say "phase $id $name: done in ${dur}s" ;;
    "$PHASE_NOT_IMPLEMENTED")
      set_phase "$id" not-implemented "not implemented yet in installer $INSTALLER_VERSION"
      STUBS_RUN=$((STUBS_RUN + 1))
      say "phase $id $name: not implemented, skipped in ${dur}s" ;;
    *)
      set_phase "$id" failed "exit $rc"
      say "phase $id $name: FAILED (exit $rc) in ${dur}s. Re-run to resume at this phase."
      leave 1 ;;
  esac
  CUR_PHASE_ID=""
}

# ---------------------------------------------------------------------------------------------------------------
# Arguments (installer.md section 3.2). Unknown flag, bad value or conflicting flags: exit 2, nothing changed.
# A credential is never an argument: there is no flag for one, and anything that looks like one is refused.
# ---------------------------------------------------------------------------------------------------------------
usage() {
  cat <<'USAGE'
Usage: sudo bash install-node.sh [options]

  --node-id <ai-nN>        node id, e.g. ai-n4 (ai-n followed by 1..9999)
  --profile <id>           catalog profile id
  --backend <cuda|vulkan|cpu>
  --from-ark <mount>       offline install from the AI-ARK drive mounted at <mount>
  --enroll                 ask for the node credential (nod_...) on the terminal, hidden
  --enroll-file <path>     read the credential from a file (first line, or /dev/stdin)
  --cloud-url <url>        default https://ai.marin.cr
  --migrate | --no-migrate force or skip the llama-qwen migration
  --dry-run                print every action as "[dry-run] would ..." and change nothing
  --yes                    assume yes to confirmations
  --pin-file <path>        admin PIN from a file (first line, 4-8 digits)
  --desktop-user <name>    primary desktop user (default: SUDO_USER)
  --agent-sha256 <hex>     64 lowercase hex sha256 of the agent binary (online fallback check)
  --inventory <path>       real inventory file; --keep-inventory copies it to /etc/localai/
  --hardening | --no-hardening
  --no-ssh --no-polkit --no-tailscale --no-sync
  --serve-pattern <A|B>
  --reboot                 reboot at the end if needed
  --repair                 repair an existing install
  --uninstall              remove the install (with --purge-config --purge-content --revert-hardening --restore-legacy)
  --log <path>             default /var/log/localai-install.log
  -v                       shell trace into the log only
  --version, -h, --help

A credential is never accepted as an argument. Exit: 0 ok, 1 a phase failed, 2 bad arguments,
3 preflight refused, 4 user aborted. Docs: docs/specs/installer.md
USAGE
}

parse_args() {
  local val
  while (($# > 0)); do
    case "$1" in
      --node-id|--profile|--backend|--from-ark|--enroll-file|--cloud-url|--pin-file|--inventory|--serve-pattern|--log|--desktop-user|--agent-sha256)
        if (($# < 2)) || [[ "$2" == --* ]]; then usage_error "$1 needs a value"; fi
        val="$2"
        case "$1" in
          --node-id) [[ "$val" =~ $NODE_ID_RE ]] || usage_error "--node-id '$val' must look like ai-n1 .. ai-n9999"; OPT_NODE_ID="$val" ;;
          --profile) [[ "$val" =~ ^[a-z0-9][a-z0-9._-]*$ ]] || usage_error "--profile '$val' is not a catalog profile id"; OPT_PROFILE="$val" ;;
          --backend) [[ "$val" == cuda || "$val" == vulkan || "$val" == cpu ]] || usage_error "--backend must be cuda, vulkan or cpu"; OPT_BACKEND="$val" ;;
          --from-ark) [[ -d "$val" ]] || usage_error "--from-ark: '$val' is not a folder. Mount the AI-ARK drive first"; OPT_FROM_ARK="${val%/}" ;;
          --enroll-file) [[ "$val" == /dev/stdin || -f "$val" ]] || usage_error "--enroll-file: '$val' is not a file"; OPT_ENROLL_FILE="$val" ;;
          --cloud-url)
            [[ "$val" =~ ^https?://[^[:space:]@]+$ ]] || usage_error "--cloud-url must be http(s)://host with no user:password in it"
            OPT_CLOUD_URL="${val%/}" ;;
          --pin-file) [[ "$val" == /dev/stdin || -f "$val" ]] || usage_error "--pin-file: '$val' is not a file"; OPT_PIN_FILE="$val" ;;
          --inventory) [[ -f "$val" ]] || usage_error "--inventory: '$val' is not a file"; OPT_INVENTORY="$val" ;;
          --serve-pattern) [[ "$val" == A || "$val" == B ]] || usage_error "--serve-pattern must be A or B"; OPT_SERVE_PATTERN="$val" ;;
          --log) LOG="$val" ;;
          --desktop-user) id -u "$val" >/dev/null 2>&1 || usage_error "--desktop-user: user '$val' does not exist"; OPT_DESKTOP_USER="$val" ;;
          --agent-sha256) [[ "$val" =~ ^[0-9a-f]{64}$ ]] || usage_error "--agent-sha256 must be 64 lowercase hex characters"; OPT_AGENT_SHA256="$val" ;;
          *) usage_error "internal: unhandled flag $1" ;;
        esac
        shift 2 ;;
      --enroll) OPT_ENROLL=1; shift ;;
      --migrate) [[ "$OPT_MIGRATE" != no ]] || usage_error "--migrate and --no-migrate conflict"; OPT_MIGRATE=yes; shift ;;
      --no-migrate) [[ "$OPT_MIGRATE" != yes ]] || usage_error "--migrate and --no-migrate conflict"; OPT_MIGRATE=no; shift ;;
      --dry-run) DRY_RUN=1; shift ;;
      --yes) OPT_YES=1; shift ;;
      --keep-inventory) OPT_KEEP_INVENTORY=1; shift ;;
      --hardening) [[ "$OPT_HARDENING" != no ]] || usage_error "--hardening and --no-hardening conflict"; OPT_HARDENING=yes; shift ;;
      --no-hardening) [[ "$OPT_HARDENING" != yes ]] || usage_error "--hardening and --no-hardening conflict"; OPT_HARDENING=no; shift ;;
      --no-ssh) OPT_NO_SSH=1; shift ;;
      --no-polkit) OPT_NO_POLKIT=1; shift ;;
      --no-tailscale) OPT_NO_TAILSCALE=1; shift ;;
      --no-sync) OPT_NO_SYNC=1; shift ;;
      --reboot) OPT_REBOOT=1; shift ;;
      --repair) OPT_REPAIR=1; shift ;;
      --uninstall) OPT_UNINSTALL=1; shift ;;
      --purge-config) OPT_PURGE_CONFIG=1; shift ;;
      --purge-content) OPT_PURGE_CONTENT=1; shift ;;
      --revert-hardening) OPT_REVERT_HARDENING=1; shift ;;
      --restore-legacy) OPT_RESTORE_LEGACY=1; shift ;;
      -v) OPT_VERBOSE=1; shift ;;
      --version) OPT_VERSION=1; shift ;;
      -h|--help) OPT_HELP=1; shift ;;
      nod_*|lat_*|lai_*|pat_*|--*token*|--*password*|--*secret*|--*credential*)
        usage_error "a credential is never accepted as an argument. Use --enroll (hidden prompt) or --enroll-file" ;;
      *) usage_error "unknown argument '$1'" ;;
    esac
  done
  validate_combination
}

validate_combination() {
  if ((OPT_ENROLL)) && [[ -n "$OPT_ENROLL_FILE" ]]; then
    usage_error "--enroll and --enroll-file conflict: pick one"
  fi
  if ((OPT_ENROLL)) && ! { : </dev/tty; } 2>/dev/null; then
    usage_error "--enroll needs a terminal (/dev/tty) and none is available: use --enroll-file <path> instead"
  fi
  if ((OPT_REPAIR && OPT_UNINSTALL)); then usage_error "--repair and --uninstall conflict"; fi
  if ((OPT_PURGE_CONFIG || OPT_PURGE_CONTENT || OPT_REVERT_HARDENING || OPT_RESTORE_LEGACY)) && ((!OPT_UNINSTALL)); then
    usage_error "--purge-config, --purge-content, --revert-hardening and --restore-legacy only work with --uninstall"
  fi
  if ((OPT_KEEP_INVENTORY)) && [[ -z "$OPT_INVENTORY" && -z "$OPT_FROM_ARK" ]]; then
    usage_error "--keep-inventory needs --inventory (or --from-ark)"
  fi
  if [[ -z "$OPT_INVENTORY" && -n "$OPT_FROM_ARK" ]]; then OPT_INVENTORY="$OPT_FROM_ARK/inventory.local.yaml"; fi
  # --no-tailscale: off when enrolling, otherwise on (installer.md section 3.2).
  if [[ -z "$OPT_NO_TAILSCALE" ]]; then
    if ((OPT_ENROLL)) || [[ -n "$OPT_ENROLL_FILE" ]]; then OPT_NO_TAILSCALE=0; else OPT_NO_TAILSCALE=1; fi
  fi
  if [[ -z "$OPT_DESKTOP_USER" && -n "${SUDO_USER:-}" && "${SUDO_USER:-}" != root ]]; then OPT_DESKTOP_USER="$SUDO_USER"; fi
  return 0
}

# ---------------------------------------------------------------------------------------------------------------
# P0: log, lock, kind of run
# ---------------------------------------------------------------------------------------------------------------
open_log() {
  local dir
  dir="$(dirname "$LOG")"
  [[ -d "$dir" ]] || usage_error "the log folder '$dir' does not exist: pass --log <path>"
  if [[ -f "$LOG" ]]; then
    local size
    size="$(stat -c %s "$LOG" 2>/dev/null || echo 0)"
    if ((size > LOG_MAX_BYTES)); then mv -f "$LOG" "$LOG.1"; fi
  fi
  { : >>"$LOG"; } 2>/dev/null || usage_error "cannot write the log '$LOG': run with sudo, or pass --log <path>"
  chmod 0600 "$LOG" 2>/dev/null || true
  LOG_OPEN=1
  if ((OPT_VERBOSE)); then
    exec 4> >(scrub >>"$LOG")
    XTRACE_PID=$!
    BASH_XTRACEFD=4
    set -x
  fi
}

take_lock() {
  LOCK_FILE="$STATE_DIR/lock"
  if ((DRY_RUN)); then
    # A dry run must not create anything: lock only when the lock file is already there.
    if [[ -e "$LOCK_FILE" ]]; then
      exec 9<"$LOCK_FILE"
    else
      say "[dry-run] would create the lock $LOCK_FILE (it does not exist yet)"
      return 0
    fi
  else
    mkdir -p "$STATE_DIR"
    exec 9>>"$LOCK_FILE"
  fi
  flock -n 9 || fail 1 LAI-INS-004 "another installer is running (lock $LOCK_FILE). Wait for it to finish."
}

phase0() {
  CUR_TAG="P0 args"
  local source="online" kind="fresh"
  if [[ -n "$OPT_FROM_ARK" ]]; then source="ark:$OPT_FROM_ARK"; fi
  state_load
  if ((${#PH_STATUS[@]} > 0)); then kind="re-run"; fi
  if [[ -e "$SYSTEMD_DIR/llama-qwen.service" ]]; then kind="migration"; fi
  INSTALL_KIND="$kind"
  say "Local AI node installer $INSTALLER_VERSION ($INSTALLER_TAG)"
  say "source: $source"
  say "started: $(now_utc)"
  say "kind: $kind"
  say "log: $LOG (0600)"
  say "verification key: ${INSTALLER_MINISIGN_PUBKEY:0:12}..."
  if ((DRY_RUN)); then say "[dry-run] DRY RUN: nothing will be changed except this log"; fi
  say "options: node-id=${OPT_NODE_ID:-auto} profile=${OPT_PROFILE:-auto} backend=${OPT_BACKEND:-detect} cloud-url=$OPT_CLOUD_URL migrate=$OPT_MIGRATE hardening=$OPT_HARDENING serve-pattern=${OPT_SERVE_PATTERN:-auto} no-tailscale=$OPT_NO_TAILSCALE no-sync=$OPT_NO_SYNC no-ssh=$OPT_NO_SSH no-polkit=$OPT_NO_POLKIT reboot=$OPT_REBOOT yes=$OPT_YES enroll=$((OPT_ENROLL || ${#OPT_ENROLL_FILE} > 0)) desktop-user=${OPT_DESKTOP_USER:-none}"
}

# ===============================================================================================================
# INST-08 (issue #155): --uninstall and --repair   (installer.md sections 4.4 and 4.5)
# Test hooks (honoured only with LAI_INSTALLER_DEV=1): LAI_INSTALL_ROOT is a prefix put in front of every
# absolute path below (a fake root folder), LAI_INSTALL_ANSWERS is a file whose lines answer the prompts,
# LAI_INSTALL_SELF is the path this script pretends to be. systemctl, tailscale, aictl, userdel, groupdel and
# dconf are found through PATH, so a test puts fakes first.
# ===============================================================================================================
REPAIR_MODE=0       # 1 while --repair runs: phases must verify and fix, never change identity, secrets or content
ANSWERS_OPEN=""
UNINSTALL_NOTES=()  # the "what was kept / what was left" lines of the summary
readonly UNINSTALL_UNITS=(localai-sync.timer localai-drill.timer localai-whisper localai-kiwix localai-llama localai-agent)
readonly UNINSTALL_UNIT_FILES=(localai-sync.service localai-sync.timer localai-drill.service localai-drill.timer
  localai-whisper.service localai-kiwix.service localai-llama.service localai-agent.service
  localai-agent-rollback.service localai-agent-rollback.timer)
readonly LEGACY_BACKUP_DIR=/var/lib/localai/migration/llama-qwen

lai_root() { if ((DEV)); then printf '%s' "${LAI_INSTALL_ROOT:-}"; fi; }
root_owner() { if ((DEV)); then printf '%s:%s' "$(id -u)" "$(id -g)"; else printf 'root:root'; fi; }

# ask_line <prompt>: the answer goes to ANSWER. From /dev/tty (or the dev answers file). No terminal = empty answer.
ask_line() {
  ANSWER=""
  if ((DEV)) && [[ -n "${LAI_INSTALL_ANSWERS:-}" ]]; then
    if [[ -z "$ANSWERS_OPEN" ]]; then exec 8<"$LAI_INSTALL_ANSWERS"; ANSWERS_OPEN=1; fi
    IFS= read -r ANSWER <&8 || true
    say "$1 [answer read from the test answers file]"
  elif { : </dev/tty; } 2>/dev/null; then
    printf '%s ' "$1" >/dev/tty
    IFS= read -r ANSWER </dev/tty || true
    log_only "asked: $1"
  else
    say "No terminal to ask on: $1"
  fi
}

# confirm_strict <question>: y/yes only. --yes does NOT answer it (installer.md section 3.2). Exit 4 otherwise.
confirm_strict() {
  if ((DRY_RUN)); then say "[dry-run] would ask: $1 [y/N]"; return 0; fi
  ask_line "$1 [y/N]"
  [[ "$ANSWER" == [yY] || "$ANSWER" == [yY][eE][sS] ]] || { say "Not confirmed. Nothing was changed."; leave 4; }
}

# confirm_delete <what to delete and why>: the person must type DELETE (upper case). --yes does NOT answer it.
confirm_delete() {
  if ((DRY_RUN)); then say "[dry-run] would ask you to type DELETE to confirm: $1"; return 0; fi
  ask_line "$1 Type DELETE (capital letters) to confirm, anything else cancels:"
  [[ "$ANSWER" == DELETE ]] || { say "Not confirmed (you did not type DELETE). Nothing was changed."; leave 4; }
}

# safe_rm_rf <fake-root prefix> <path>: only these four trees, never an empty or relative path.
safe_rm_rf() {
  local ur="$1" rel="$2"
  case "$rel" in
    /opt/localai|/etc/localai|/var/lib/localai|/srv/localai) ;;
    *) fail 1 LAI-INS-081 "refusing to delete '$ur$rel': it is not one of the four Local AI folders. Nothing was deleted." ;;
  esac
  if [[ -z "$rel" || "$rel" != /* || "$rel" == "/" ]]; then
    fail 1 LAI-INS-081 "refusing to delete an empty or root path. Nothing was deleted."
  fi
  if [[ ! -e "$ur$rel" && ! -L "$ur$rel" ]]; then say "already gone: $ur$rel"; return 0; fi
  run_or_say rm -rf -- "$ur$rel"
}

rm_file_or_say() { # rm_file_or_say <path>
  if [[ -e "$1" || -L "$1" ]]; then run_or_say rm -f -- "$1"; else say "already gone: $1"; fi
}

# The legacy unit is saved by the migration (llama-router.md 4.9.2 M9). Prints the file to restore, or nothing.
legacy_unit_source() {
  local ur="$1"
  if [[ -f "$ur$LEGACY_BACKUP_DIR/llama-qwen.service" ]]; then
    printf '%s' "$ur$LEGACY_BACKUP_DIR/llama-qwen.service"
  elif [[ -f "$SYSTEMD_DIR/llama-qwen.service" && ! -L "$SYSTEMD_DIR/llama-qwen.service" ]]; then
    printf '%s' "$SYSTEMD_DIR/llama-qwen.service"   # not migrated, or already restored: it is still there
  fi
}

# Steps 5 (a) (b) (c). Nothing under /etc/localai is touched here; the caller removes it only afterwards.
restore_legacy() {
  local ur="$1" src="$2" key_src="$1/etc/localai/api-key" key_dst="$1/etc/llama-qwen/api-key" tmp
  CUR_TAG="P0 restore-legacy"
  say "Putting the old llama-qwen service back (same port 8080, same key)."
  # (a) the key first: a real file, so it survives the removal of /etc/localai.
  if [[ -L "$key_dst" || ! -e "$key_dst" ]]; then
    if ((DRY_RUN)); then
      say "[dry-run] would replace the link $key_dst with a real copy of $key_src (root, mode 0600)"
    else
      mkdir -p "$(dirname "$key_dst")"
      tmp="$(mktemp "$(dirname "$key_dst")/.api-key.XXXXXX")"
      cp -- "$key_src" "$tmp" || { rm -f "$tmp"; fail 1 LAI-INS-080 "could not copy the key to $key_dst. Nothing under /etc/localai was removed."; }
      chmod 0600 "$tmp"
      chown "$(root_owner)" "$tmp" || { rm -f "$tmp"; fail 1 LAI-INS-080 "could not set the owner of the key copy. Nothing under /etc/localai was removed."; }
      mv -f -- "$tmp" "$key_dst"
      say "wrote $key_dst as a real file (mode 0600)"
    fi
  else
    say "already a real file: $key_dst"
  fi
  # (b) unmask (removes the mask link), restore the unit and its drop-ins, reload.
  svc_or_say unmask llama-qwen.service || true
  if [[ "$src" != "$SYSTEMD_DIR/llama-qwen.service" ]]; then
    if ((DRY_RUN)); then
      say "[dry-run] would restore $src to $SYSTEMD_DIR/llama-qwen.service (mode 0644)"
    else
      mkdir -p "$SYSTEMD_DIR"
      rm -f -- "$SYSTEMD_DIR/llama-qwen.service"
      cp -- "$src" "$SYSTEMD_DIR/llama-qwen.service"
      chmod 0644 "$SYSTEMD_DIR/llama-qwen.service"
      say "restored $SYSTEMD_DIR/llama-qwen.service"
      if [[ -d "$(dirname "$src")/llama-qwen.service.d" && ! -e "$SYSTEMD_DIR/llama-qwen.service.d" ]]; then
        cp -r -- "$(dirname "$src")/llama-qwen.service.d" "$SYSTEMD_DIR/llama-qwen.service.d"
        say "restored the drop-in folder llama-qwen.service.d"
      fi
    fi
  fi
  svc_or_say daemon-reload || fail 1 LAI-INS-080 "systemctl daemon-reload failed. Nothing under /etc/localai was removed."
  # (c) start it
  run_or_say systemctl enable --now llama-qwen.service || fail 1 LAI-INS-080 "the old llama-qwen service did not start. Nothing under /etc/localai was removed: read 'journalctl -u llama-qwen -n 50'."
  say "llama-qwen is enabled and started. Clients keep working with the same port and key."
}

do_uninstall() {
  local ur f legacy_src="" pattern="" size="" reboot_needed=0 step_rc=0
  ur="$(lai_root)"
  CUR_TAG="P0 uninstall"
  # Refuse impossible combinations before anything is touched.
  if ((OPT_RESTORE_LEGACY && OPT_PURGE_CONTENT)); then
    usage_error "--restore-legacy and --purge-content conflict: the old service reads its models from /srv/localai (the migration moved them there)"
  fi
  if ((OPT_RESTORE_LEGACY)); then
    legacy_src="$(legacy_unit_source "$ur")"
    if [[ -z "$legacy_src" ]]; then
      say "WARNING: --restore-legacy found no saved llama-qwen.service (looked in $ur$LEGACY_BACKUP_DIR/ and $SYSTEMD_DIR/)."
      say "         There is nothing to restore, so the old service will NOT be put back. The rest of the uninstall goes on."
      UNINSTALL_NOTES+=("the old llama-qwen service was not restored: no saved copy was found")
    elif [[ ! -f "$ur/etc/localai/api-key" ]]; then
      fail 1 LAI-INS-080 "the key $ur/etc/localai/api-key is missing, so the old service could not keep its key. Nothing was changed."
    fi
  fi

  # 1. confirm (and the DELETE prompts, all up front, so a "no" leaves everything as it was)
  confirm_strict "This removes Local AI from this computer. Models and content in /srv/localai are kept unless you add --purge-content. Continue?"
  if ((OPT_PURGE_CONFIG)); then
    confirm_delete "This also deletes /etc/localai and /var/lib/localai: the node id, API key, agent token, PIN and cloud credential. They cannot be recovered."
  fi
  if ((OPT_PURGE_CONTENT)); then
    size="$(du -sh -- "$ur/srv/localai" 2>/dev/null | cut -f1 || true)"
    confirm_delete "This also deletes /srv/localai (${size:-unknown size} of models and content). Ark drives are never touched."
  fi

  # 2. unenroll, best effort, 10 s
  if [[ -f "$ur/etc/localai/cloud-credential" ]]; then
    if command -v aictl >/dev/null 2>&1; then
      run_or_say timeout 10 aictl cloud unenroll --yes || say "Could not tell the cloud (best effort). The node record can be removed on the cloud's Nodes page."
    else
      say "Enrolled, but aictl is not available: the cloud still lists this node; remove it on the cloud's Nodes page."
    fi
  else
    say "not enrolled with the cloud"
  fi

  # 3. stop and disable the units; remove our Tailscale Serve listener unless the old service takes over
  CUR_TAG="P0 uninstall units"
  for f in "${UNINSTALL_UNITS[@]}"; do
    run_or_say systemctl disable --now "$f" || say "  (could not disable $f: it may not be installed; going on)"
  done
  if ((!OPT_RESTORE_LEGACY)); then
    if command -v tailscale >/dev/null 2>&1; then
      pattern="$(sed -n -E 's/^[[:space:]]*serve_pattern:[[:space:]]*"?([AB])"?.*/\1/p' "$ur/etc/localai/node.yaml" 2>/dev/null | head -n 1 || true)"
      case "$pattern" in
        A) run_or_say tailscale serve --tcp=18080 off || say "  (tailscale serve --tcp=18080 off failed; going on)" ;;
        B) run_or_say tailscale serve --https=443 off || say "  (tailscale serve --https=443 off failed; going on)" ;;
        *) say "Tailscale Serve pattern unknown (no node.yaml): left as it is" ;;
      esac
    else
      say "tailscale not installed: no Serve listener to remove"
    fi
  else
    say "Tailscale Serve left as it is: the old service keeps using it"
  fi

  # 4. remove the unit files
  for f in "${UNINSTALL_UNIT_FILES[@]}"; do rm_file_or_say "$SYSTEMD_DIR/$f"; done
  if [[ -d "$SYSTEMD_DIR/localai-llama.service.d" ]]; then run_or_say rm -rf -- "$SYSTEMD_DIR/localai-llama.service.d"; fi
  svc_or_say daemon-reload

  # 5. restore the legacy service BEFORE the agent and /etc/localai go
  if ((OPT_RESTORE_LEGACY)) && [[ -n "$legacy_src" ]]; then
    restore_legacy "$ur" "$legacy_src"
    UNINSTALL_NOTES+=("the old llama-qwen service was restored and started (same port 8080 and key)")
  fi

  # 9. hardening (INST-03, issue #116 provides revert_hardening). Done before step 6 because it needs the agent
  # binary and the install state (backups) that steps 6 and 7 remove.
  if ((OPT_REVERT_HARDENING)); then
    CUR_TAG="P0 uninstall hardening"
    if declare -F revert_hardening >/dev/null; then
      revert_hardening || step_rc=$?
      if ((step_rc != 0)); then say "WARNING: reverting the laptop hardening failed (exit $step_rc); the rest is done."; fi
      if [[ "${REBOOT_REQUIRED:-0}" == 1 ]]; then reboot_needed=1; fi
    else
      say "WARNING: --revert-hardening is not available in this version of the installer (owner: INST-03, issue #116). Nothing was reverted."
      UNINSTALL_NOTES+=("the laptop hardening was NOT reverted (not available in this version)")
    fi
  else
    UNINSTALL_NOTES+=("kept the laptop hardening (use --revert-hardening to undo it)")
  fi

  # 6. the program files
  CUR_TAG="P0 uninstall files"
  safe_rm_rf "$ur" /opt/localai
  rm_file_or_say "$ur/usr/local/bin/aictl"
  rm_file_or_say "$ur/usr/local/sbin/install-node.sh"
  rm_file_or_say "$ur/etc/polkit-1/rules.d/50-localai-operators.rules"

  # 7. config and state
  if ((OPT_PURGE_CONFIG)); then
    safe_rm_rf "$ur" /etc/localai
    safe_rm_rf "$ur" /var/lib/localai
    UNINSTALL_NOTES+=("config and state were deleted (/etc/localai, /var/lib/localai)")
  else
    # The phase record says "done" for things that are now gone: drop it so a reinstall re-runs every phase.
    rm_file_or_say "$STATE_FILE"
    UNINSTALL_NOTES+=("kept /etc/localai and /var/lib/localai: a reinstall reuses the node id, API key, agent token and PIN")
  fi

  # 8. content
  if ((OPT_PURGE_CONTENT)); then
    safe_rm_rf "$ur" /srv/localai
    UNINSTALL_NOTES+=("content was deleted (/srv/localai)")
  else
    UNINSTALL_NOTES+=("kept /srv/localai (models and content); ark drives were not touched")
  fi

  # 10. user and group only with --purge-config
  if ((OPT_PURGE_CONFIG)); then
    run_or_say userdel localai || say "  (userdel localai: not removed; it may not exist)"
    run_or_say groupdel localai-operators || say "  (groupdel localai-operators: not removed; it may not exist)"
  else
    UNINSTALL_NOTES+=("kept the localai user and the localai-operators group")
  fi

  CUR_TAG="P0 uninstall summary"
  if ((DRY_RUN)); then say "[dry-run] done: nothing was changed."; fi
  say "Local AI has been removed from this computer. Summary:"
  for f in "${UNINSTALL_NOTES[@]}"; do say "  - $f"; done
  if ((reboot_needed)); then say "  - reboot required to finish reverting the laptop settings (LAI-INS-070)"; fi
  return 0
}

# Keep /opt/localai/installers/install-node.sh and its /usr/local/sbin symlink healthy (spec 4.5).
repair_installer_files() {
  local ur="$1" dest link target=/opt/localai/installers/install-node.sh self src="" cur_target
  dest="$ur$target"; link="$ur/usr/local/sbin/install-node.sh"
  CUR_TAG="P0 repair installer"
  self="${BASH_SOURCE[0]}"
  if ((DEV)) && [[ -n "${LAI_INSTALL_SELF:-}" ]]; then self="$LAI_INSTALL_SELF"; fi
  if [[ "$(readlink -f -- "$self")" != "$(readlink -f -- "$dest" 2>/dev/null || echo "$dest")" ]]; then
    src="$self"
  elif [[ -n "$OPT_FROM_ARK" && -f "$OPT_FROM_ARK/localai/kit/installers/install-node.sh" ]]; then
    src="$OPT_FROM_ARK/localai/kit/installers/install-node.sh"
  fi
  if [[ -n "$src" ]]; then
    write_file_or_diff "$dest" 0755 "$(root_owner)" <"$src"
  else
    say "This run uses the installed copy itself, so it cannot tell if it was changed. To refresh it, run the installer from the release download or from the ark drive with --from-ark."
    if [[ ! -e "$dest" ]]; then
      say "WARNING: $dest is missing and there is no other copy to restore it from. Run this repair from the ark drive: sudo bash <drive>/localai/kit/installers/install-node.sh --from-ark <drive> --repair"
    fi
  fi
  if ((!DRY_RUN)) && [[ -f "$dest" && "$(stat -c %a "$dest")" != 755 ]]; then chmod 0755 "$dest"; say "fixed the mode of $dest"; fi
  cur_target="$(readlink -- "$link" 2>/dev/null || true)"
  if [[ "$cur_target" == "$target" ]]; then
    say "already correct: $link -> $target"
  else
    if ((!DRY_RUN)); then mkdir -p "$(dirname "$link")"; fi
    run_or_say ln -sfn -- "$target" "$link"
  fi
}

do_repair() {
  local ur entry i=0 id rc=0
  ur="$(lai_root)"
  REPAIR_MODE=1
  CUR_TAG="P0 repair"
  say "Repair: checking and fixing this install. Your node id, keys, PIN, cloud link and content are not changed."
  repair_installer_files "$ur"
  for entry in "${PHASE_TABLE[@]}"; do
    i=$((i + 1))
    id="${entry%%|*}"
    PH_STATUS[$id]=""   # force the phase to run: in repair mode "already done" is not trusted
    run_phase "$i" "$entry"
  done
  CUR_TAG="P0 repair doctor"
  if ((STUBS_RUN > 0)); then
    say "NOTE: $STUBS_RUN of $PHASE_TOTAL phases are not implemented in this version, so they repaired nothing."
  fi
  if ((DRY_RUN)); then say "[dry-run] would run: aictl doctor"; say "[dry-run] done: nothing was changed."; return 0; fi
  if command -v aictl >/dev/null 2>&1; then
    run_or_say aictl doctor || rc=$?
    if ((rc != 0)); then
      say "aictl doctor still reports problems (exit $rc). Each line above names its LAI code and the fix; see docs/runbooks/07-troubleshooting.md."
      return 1
    fi
    say "Repair finished and aictl doctor is clean."
  else
    say "aictl is not installed here, so the final health check was skipped."
  fi
  return 0
}
# ===============================================================================================================
# end INST-08
# ===============================================================================================================

main() {
  parse_args "$@"
  if ((OPT_HELP)); then usage; return 0; fi
  # Detect "never stamped" without the stamper rewriting this very comparison (it only sees whole tokens).
  local unstamped=0
  if [[ "$INSTALLER_VERSION" == "@@LAI_RELEASE_VERSION""@@" ]]; then unstamped=1; fi
  if ((OPT_VERSION)); then
    if ((unstamped)); then echo "install-node.sh (template, not stamped)"; else echo "install-node.sh $INSTALLER_VERSION ($INSTALLER_TAG)"; fi
    return 0
  fi
  if ((unstamped)); then
    if ((DEV && DRY_RUN)); then
      INSTALLER_VERSION="0.0.0-unstamped"; INSTALLER_TAG="unstamped"; INSTALLER_MINISIGN_PUBKEY="RW-unstamped-key"
    else
      printf 'install-node.sh: this copy was never stamped with a release version and key, so it cannot verify anything.\n' >&2
      printf 'Download install-node.sh from a node release (docs/specs/installer.md section 3.6) and run that.\n' >&2
      leave 2
    fi
  fi

  if ((DEV)); then
    STATE_DIR="${LAI_INSTALL_STATE_DIR:-$DEFAULT_STATE_DIR}"
    SYSTEMD_DIR="${LAI_INSTALL_SYSTEMD_DIR:-$SYSTEMD_DIR}"
  fi
  STATE_FILE="$STATE_DIR/state.json"
  if ((EUID != 0 && !DRY_RUN && !DEV)); then
    printf 'ERROR LAI-INS-003: not run as root. Run "sudo bash install-node.sh ..." from the desktop user terminal.\n' >&2
    leave 3
  fi

  trap 'on_error $LINENO "$BASH_COMMAND"' ERR
  trap on_signal INT TERM
  trap cleanup EXIT
  open_log
  take_lock
  phase0

  if ((OPT_REPAIR || OPT_UNINSTALL)); then
    if ((OPT_UNINSTALL)); then do_uninstall; else do_repair; fi
    return $?
  fi

  local i=0 entry
  for entry in "${PHASE_TABLE[@]}"; do
    i=$((i + 1))
    run_phase "$i" "$entry"
  done
  CUR_TAG="P15 summary"
  if ((STUBS_RUN > 0)); then
    say "THIS INSTALLER IS INCOMPLETE: $STUBS_RUN of $PHASE_TOTAL phases are not implemented yet and did nothing."
    say "No node was installed or configured by this run."
  fi
  if ((DRY_RUN)); then say "[dry-run] done: nothing was changed. Exit 0 means every implemented step would succeed."; fi
  return 0
}

# ===============================================================================================================
# INST-07 (issue #154): P11 Tailscale, P14 initial content, P15 verify and summary.
# These definitions replace the stubs created by define_phase_stubs (this block runs after it, at source time).
# Spec: docs/specs/installer.md section 4.3 P11, P14, P15.
# ===============================================================================================================

# i07_dev <ENV_NAME> <default>: the value of a development hook when LAI_INSTALLER_DEV=1, else the default.
i07_dev() {
  local name="$1" def="$2"
  if ((DEV)) && [[ -n "${!name:-}" ]]; then printf '%s' "${!name}"; else printf '%s' "$def"; fi
}
# i07_fail <doctor-code> <message>: record the running phase as failed (so a re-run resumes here), then exit 1.
i07_fail() {
  [[ -z "${CUR_PHASE_ID:-}" ]] || set_phase "$CUR_PHASE_ID" failed "$1" || true
  fail 1 "$@"
}
# In a development/test run (LAI_INSTALLER_DEV=1) the real agent, aictl and tailscale are never used unless a hook
# names them, so a test can not touch the machine it runs on.
i07_aiagent() { if ((DEV)); then printf '%s' "${LAI_INSTALLER_AIAGENT:-/nonexistent/aiagent}"; else printf '%s' /opt/localai/bin/aiagent; fi; }
i07_aictl() { if ((DEV)); then printf '%s' "${LAI_INSTALLER_AICTL:-/nonexistent/aictl}"; else printf '%s' aictl; fi; }
i07_tailscale() { if ((DEV)); then printf '%s' "${LAI_INSTALLER_TAILSCALE:-/nonexistent/tailscale}"; else printf '%s' tailscale; fi; }
i07_node_yaml() { i07_dev LAI_INSTALL_NODE_YAML /etc/localai/node.yaml; }
i07_have_agent() { [[ -x "$(i07_aiagent)" ]]; }

# Facts shared between the three phases of this item (a re-run recomputes them; nothing depends on memory).
TS_JOINED=0          # tailscale status --json says BackendState Running
TS_SERVE_PATTERN=""  # "A", "B" or "" (none recorded yet); P9 writes it to node.yaml tailscale.serve_pattern
CONTENT_LINE=""      # the "Content:" line of the summary
CLOUD_LINE=""        # a phase 13 implementation may set this; otherwise P15 derives it from the options
BRAIN_LINE=""        # a phase 9 implementation may set this; otherwise the Go default is printed
REBOOT_REQUIRED=0    # any phase may set 1 when a reboot is needed

# i07_inventory_args: "--inventory <file>" when a real inventory was given and exists (else the agent looks
# at /etc/localai/inventory.local.yaml itself).
i07_inventory_args() {
  if [[ -n "$OPT_INVENTORY" && -f "$OPT_INVENTORY" ]]; then printf '%s\n' --inventory "$OPT_INVENTORY"; fi
}

# i07_tailnet_joined: 0 when `tailscale status --json` shows BackendState Running (read only, always real).
i07_tailnet_joined() {
  command -v "$(i07_tailscale)" >/dev/null 2>&1 || return 1
  local js
  js="$("$(i07_tailscale)" status --json 2>/dev/null || true)"
  grep -Eq '"BackendState"[[:space:]]*:[[:space:]]*"Running"' <<<"$js"
}

# ---- P11 Tailscale -----------------------------------------------------------------------------------------
check_tailscale() {
  ((OPT_NO_TAILSCALE == 1)) && return 0
  command -v "$(i07_tailscale)" >/dev/null 2>&1 || return 1
  systemctl is-active --quiet tailscaled 2>/dev/null
}

# i07_codename / i07_arch: where the signed apt repo and the kit debs are chosen.
i07_codename() {
  local f
  f="$(i07_dev LAI_INSTALL_OS_RELEASE /etc/os-release)"
  # shellcheck disable=SC1090
  (. "$f" 2>/dev/null && printf '%s' "${VERSION_CODENAME:-}") || true
}

# i07_install_tailscale: online = official signed apt repo (never "curl | sh"); offline = the kit deb.
i07_install_tailscale() {
  if command -v "$(i07_tailscale)" >/dev/null 2>&1; then
    say "Tailscale is already installed."
    return 0
  fi
  local codename arch
  codename="$(i07_codename)"
  arch="$(dpkg --print-architecture 2>/dev/null || echo amd64)"
  [[ -n "$codename" ]] || i07_fail LAI-INS-030 "cannot tell the Ubuntu release name (VERSION_CODENAME). Fix /etc/os-release, then re-run."
  if [[ -n "$OPT_FROM_ARK" ]]; then
    local dir="$OPT_FROM_ARK/localai/kit/debs/$codename-$arch" debs=()
    if [[ -d "$dir" ]]; then
      local f
      for f in "$dir"/tailscale_*.deb; do [[ -f "$f" ]] && debs+=("$f"); done
    fi
    if ((${#debs[@]} == 0)); then
      i07_fail LAI-INS-030 "the ark drive has no Tailscale package for $codename-$arch (looked in $dir). Sync the drive on a computer with internet, or use --no-tailscale."
    fi
    say "Installing Tailscale from the ark drive: ${debs[0]##*/}"
    run_or_say dpkg -i "${debs[0]}" || i07_fail LAI-INS-030 "installing the Tailscale package from the ark drive failed. Read the messages above, fix, and re-run."
  else
    local keyring list base="https://pkgs.tailscale.com/stable/ubuntu"
    keyring="$(i07_dev LAI_INSTALL_TS_KEYRING /usr/share/keyrings/tailscale-archive-keyring.gpg)"
    list="$(i07_dev LAI_INSTALL_TS_LIST /etc/apt/sources.list.d/tailscale.list)"
    say "Adding the official Tailscale apt repository for $codename."
    if [[ ! -s "$keyring" ]]; then
      run_or_say curl -fsSL --output "$keyring" "$base/$codename.noarmor.gpg" ||
        i07_fail LAI-INS-030 "downloading the Tailscale signing key failed. Check the internet connection, then re-run."
    else
      say "already present: $keyring"
    fi
    if [[ ! -s "$list" ]]; then
      run_or_say curl -fsSL --output "$list" "$base/$codename.tailscale-keyring.list" ||
        i07_fail LAI-INS-030 "downloading the Tailscale apt source failed. Check the internet connection, then re-run."
    else
      say "already present: $list"
    fi
    run_or_say apt-get update || i07_fail LAI-INS-030 "apt-get update failed. Read the messages above, fix, and re-run."
    apt_or_say tailscale || i07_fail LAI-INS-030 "installing Tailscale failed. Read the apt messages above, fix, and re-run."
  fi
}

# i07_check_serve_pattern: decides the pattern and stops with LAI-INS-050 when it contradicts the inventory.
i07_check_serve_pattern() {
  local agent args=() out rc=0
  agent="$(i07_aiagent)"
  if ! i07_have_agent; then
    TS_SERVE_PATTERN="${OPT_SERVE_PATTERN:-}"
    say "The agent is not installed yet, so the serve pattern is not checked against the inventory now (${TS_SERVE_PATTERN:-none given})."
    return 0
  fi
  args=(setup serve-check --node-yaml "$(i07_node_yaml)")
  [[ -z "$OPT_NODE_ID" ]] || args+=(--node-id "$OPT_NODE_ID")
  [[ -z "$OPT_SERVE_PATTERN" ]] || args+=(--serve-pattern "$OPT_SERVE_PATTERN")
  local inv_args=() l
  while IFS= read -r l; do [[ -z "$l" ]] || inv_args+=("$l"); done < <(i07_inventory_args)
  out="$("$agent" "${args[@]}" "${inv_args[@]}" 2>&1)" || rc=$?
  if ((rc != 0)); then
    say "$out"
    set_phase "$CUR_PHASE_ID" failed "LAI-INS-050 serve pattern mismatch" || true
    fail 1 LAI-INS-050 "the Tailscale serve pattern does not match the inventory. Re-run with --serve-pattern set to the value in the inventory."
  fi
  [[ "$out" == none ]] && out=""
  TS_SERVE_PATTERN="$out"
  say "serve pattern: ${TS_SERVE_PATTERN:-none} (recorded in node.yaml; the installer never runs tailscale serve, the agent does in online mode)"
}

phase_tailscale() {
  if ((OPT_NO_TAILSCALE == 1)); then
    say "skipped: --no-tailscale (Tailscale is online-only; this node can still run fully offline)."
    return 0
  fi
  i07_install_tailscale
  run_or_say systemctl enable --now tailscaled
  if i07_tailnet_joined; then
    TS_JOINED=1
    say "This computer is already on the tailnet."
  else
    TS_JOINED=0
    say "This computer is not on the tailnet yet. The installer never logs in: the summary at the end shows the one command to run."
  fi
  i07_check_serve_pattern
  return 0
}

# ---- P14 Initial content -----------------------------------------------------------------------------------
check_content() { return 1; }   # starting a sync or import twice is harmless: the agent refuses a second run

# i07_planned_size <dry-run output>: the last "<n> <unit>" size on a line that talks about the download.
i07_planned_size() {
  grep -iE 'download|total|plan' <<<"$1" | grep -oE '[0-9]+([.][0-9]+)? ?([KMGT]i?B)' | tail -n 1 || true
}

phase_content() {
  if ((OPT_NO_SYNC)); then
    CONTENT_LINE="not started (--no-sync). Start it with: aictl ark sync"
    say "skipped: --no-sync."
    return 0
  fi
  if [[ -n "$OPT_FROM_ARK" ]]; then
    local rootjson="$OPT_FROM_ARK/localai/ROOT.json" id=""
    [[ ! -f "$rootjson" ]] || id="$(sed -n -E 's/.*"root_id"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "$rootjson" | head -n 1)"
    if [[ -z "$id" ]]; then
      CONTENT_LINE="not started (the ark drive has no root id). Start it with: aictl ark import --catalog"
      say "The ark drive has no readable ROOT.json, so the content import was not started. Later run: aictl ark import --catalog"
      return 0
    fi
    run_or_say "$(i07_aictl)" ark import --from "$id" --catalog ||
      { CONTENT_LINE="import did not start. Try: aictl ark import --from $id --catalog"; say "The import could not be started now (the install goes on). Later run: aictl ark import --from $id --catalog"; return 0; }
    CONTENT_LINE="import running from the ark drive   check: aictl ark import"
    say "Importing the content from the ark drive in the background."
  else
    local plan="" size="" rc=0
    if command -v "$(i07_aictl)" >/dev/null 2>&1; then
      plan="$("$(i07_aictl)" ark sync --dry-run 2>&1)" || rc=$?
      if ((rc != 0)); then
        say "$plan"
        CONTENT_LINE="not started (could not plan the download). Try again with: aictl ark sync"
        say "The download could not be planned now (the install goes on). Later run: aictl ark sync"
        return 0
      fi
      size="$(i07_planned_size "$plan")"
    else
      say "aictl is not installed yet, so the download cannot be planned now."
    fi
    if [[ -n "$size" ]]; then
      say "Will download $size over the next hours"
    else
      say "Will download the planned content over the next hours"
    fi
    run_or_say "$(i07_aictl)" ark sync ||
      { CONTENT_LINE="did not start. Try: aictl ark sync"; say "The sync could not be started now (the install goes on). Later run: aictl ark sync"; return 0; }
    CONTENT_LINE="sync running${size:+ ($size planned)}   check: aictl ark sync"
  fi
  say "Check progress with: aictl ark sync (status) or the desktop app."
  return 0
}

# ---- P15 Verify and summary --------------------------------------------------------------------------------
check_verify() { return 1; }    # always re-run: the summary reflects the machine as it is now

# i07_is_laptop: --hardening / --no-hardening decide; otherwise a battery or a portable chassis type.
i07_is_laptop() {
  case "$OPT_HARDENING" in yes) return 0 ;; no) return 1 ;; esac
  if ((DEV)) && [[ -n "${LAI_INSTALLER_LAPTOP:-}" ]]; then [[ "$LAI_INSTALLER_LAPTOP" == 1 ]]; return; fi
  local b
  for b in /sys/class/power_supply/BAT*; do [[ -e "$b" ]] && return 0; done
  local ct
  ct="$(cat /sys/class/dmi/id/chassis_type 2>/dev/null || true)"
  [[ "$ct" == 8 || "$ct" == 9 || "$ct" == 10 || "$ct" == 14 ]]
}

# i07_keep_inventory: --keep-inventory copies the real inventory to /etc/localai/inventory.local.yaml (root 0600).
i07_keep_inventory() {
  ((OPT_KEEP_INVENTORY)) || return 0
  local dest
  dest="$(i07_dev LAI_INSTALL_INVENTORY_DEST /etc/localai/inventory.local.yaml)"
  if [[ -z "$OPT_INVENTORY" || ! -f "$OPT_INVENTORY" ]]; then
    say "WARNING: --keep-inventory was given but there is no inventory file to keep (looked for '${OPT_INVENTORY:-none}')."
    return 0
  fi
  if [[ -f "$dest" ]] && cmp -s "$OPT_INVENTORY" "$dest"; then
    say "already up to date: $dest"
    return 0
  fi
  local owner=root:root
  ((DEV)) && owner="$(id -u):$(id -g)"
  run_or_say install -D -m 0600 -o "${owner%%:*}" -g "${owner##*:}" "$OPT_INVENTORY" "$dest"
}

phase_verify() {
  i07_keep_inventory
  local agent inv_args=() l
  agent="$(i07_aiagent)"
  while IFS= read -r l; do [[ -z "$l" ]] || inv_args+=("$l"); done < <(i07_inventory_args)
  local common=(--node-yaml "$(i07_node_yaml)")
  [[ -z "$OPT_NODE_ID" ]] || common+=(--node-id "$OPT_NODE_ID")
  [[ -z "$OPT_SERVE_PATTERN" ]] || common+=(--serve-pattern "$OPT_SERVE_PATTERN")

  if ! i07_have_agent; then
    if ((DRY_RUN)); then
      say "[dry-run] would run: aiagent setup verify, then aiagent setup summary (the agent is not installed yet, so nothing can be shown)."
      return 0
    fi
    say "The agent is not installed (phase 9 did not put it in place), so the checks and the summary cannot run."
    return "$PHASE_NOT_IMPLEMENTED"
  fi

  say "Checking the install:"
  local out rc=0
  out="$("$agent" setup verify "${common[@]}" "${inv_args[@]}" 2>&1)" || rc=$?
  say "$out"
  if ((rc != 0)); then
    say "The post-install checks found a problem (see the line with LAI-INS above). The install is NOT finished: fix it and re-run."
    return 1
  fi

  local laptop=0 sumargs=()
  i07_is_laptop && laptop=1
  if [[ -e /var/run/reboot-required || -e "$STATE_DIR/reboot-required" ]] || ((REBOOT_REQUIRED)) ||
    { ((laptop)) && [[ "${PH_STATUS[P4]:-}" == "done" ]]; }; then
    REBOOT_REQUIRED=1
  fi
  sumargs=(setup summary "${common[@]}" "${inv_args[@]}")
  ((laptop)) && sumargs+=(--laptop)
  ((REBOOT_REQUIRED)) && sumargs+=(--reboot-required)
  [[ -z "$OPT_DESKTOP_USER" ]] || sumargs+=(--user "$OPT_DESKTOP_USER")
  ((OPT_NO_TAILSCALE == 1)) && sumargs+=(--skip-tailscale)
  i07_tailnet_joined && sumargs+=(--tailnet-joined)
  [[ -z "$CONTENT_LINE" ]] || sumargs+=(--content "$CONTENT_LINE")
  [[ -z "$BRAIN_LINE" ]] || sumargs+=(--brain "$BRAIN_LINE")
  if [[ -n "$CLOUD_LINE" ]]; then
    sumargs+=(--cloud "$CLOUD_LINE")
  elif ((OPT_ENROLL)) || [[ -n "$OPT_ENROLL_FILE" ]]; then
    sumargs+=(--cloud "enrollment requested at $OPT_CLOUD_URL   check: aictl cloud status")
  fi
  rc=0
  out="$("$agent" "${sumargs[@]}" 2>&1)" || rc=$?
  say ""
  say "$out"
  ((rc == 0)) || { say "Printing the summary failed (exit $rc); the install itself is complete. Run: sudo aiagent setup summary"; return 0; }

  if ((OPT_REBOOT && REBOOT_REQUIRED)); then
    local secs
    secs="$(i07_dev LAI_INSTALLER_COUNTDOWN 10)"
    say "Rebooting in $secs seconds. Press Ctrl-C to cancel."
    if ((DRY_RUN)); then
      say "[dry-run] would reboot after the countdown"
    else
      sleep "$secs"
      run_or_say systemctl reboot
    fi
  fi
  return 0
}

# Run only when executed, not when a test sources the file to call its functions.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  set -Eeuo pipefail
  umask 077
  main "$@"
fi
