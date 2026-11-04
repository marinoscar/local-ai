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
  set_phase "$id" running ""
  "phase_${fn}" || rc=$?
  local dur=$((SECONDS - start))
  case "$rc" in
    0)
      set_phase "$id" "done" ""
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
    local mode="repair"
    if ((OPT_UNINSTALL)); then mode="uninstall"; fi
    CUR_TAG="P0 $mode"
    say "NOT IMPLEMENTED YET: --$mode does nothing in this version of the installer (owner: $REPAIR_UNINSTALL_OWNER)."
    say "NOTHING WAS CHANGED."
    return 0
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

# Run only when executed, not when a test sources the file to call its functions.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  set -Eeuo pipefail
  umask 077
  main "$@"
fi
