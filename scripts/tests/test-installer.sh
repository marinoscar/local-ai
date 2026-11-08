#!/usr/bin/env bash
# test-installer.sh: installers/install-node.sh (driver skeleton, INST-01). Offline; never touches the system:
# every run uses LAI_INSTALLER_DEV=1 with a throwaway state folder and log, or --dry-run. Needs bash, flock, sed.
# The stamping test also needs minisign and is skipped without it.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2
SRC="$PWD"
INST="$SRC/installers/install-node.sh"
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -10; fi
}
w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT
export TMPDIR="$w/tmp"; mkdir -p "$TMPDIR"
fakecred="nod_$(printf 'FAKEFAKEFAKE')1234"       # generated fake credential, never a real one
fakehex="$(printf 'ab%.0s' $(seq 32))"             # 64 hex characters
# Dev hooks of the INST-06 phases (honoured only with LAI_INSTALLER_DEV=1): never the real agent, aictl or /etc.
# Fakes stand in for the agent, aictl, systemctl and journalctl: they log their arguments to $FAKE_LOG and record stdin.
FB="$w/fakebin"; mkdir -p "$FB"
export FAKE_LOG="$w/fake.log" FAKE_STDIN_DIR="$w/fakestdin"; mkdir -p "$FAKE_STDIN_DIR"
cat >"$FB/aiagent" <<'FAKE'
#!/bin/sh
echo "aiagent $*" >>"$FAKE_LOG"
[ "$2" = "--help" ] && exit 0
if [ -n "${FAKE_AIAGENT_RC:-}" ] && { [ -z "${FAKE_AIAGENT_STEP:-}" ] || [ "$2" = "$FAKE_AIAGENT_STEP" ]; }; then echo "${FAKE_AIAGENT_MSG:-}"; exit "$FAKE_AIAGENT_RC"; fi
echo "fake aiagent $2 ok"
FAKE
cat >"$FB/aictl" <<'FAKE'
#!/bin/sh
echo "aictl $*" >>"$FAKE_LOG"
cat >"$FAKE_STDIN_DIR/$1-$2"
if [ "$1 $2" = "kit export-build" ] && [ -n "${FAKE_EXPORT_RC:-}" ]; then echo "LAI-KIT-049 boom"; exit "$FAKE_EXPORT_RC"; fi
if [ -n "${FAKE_AICTL_RC:-}" ]; then echo "${FAKE_AICTL_MSG:-failed} $(cat "$FAKE_STDIN_DIR/$1-$2")"; exit "$FAKE_AICTL_RC"; fi
echo "fake aictl $1 $2 ok"
FAKE
printf '#!/bin/sh
echo "systemctl $*" >>"$FAKE_LOG"
' >"$FB/systemctl"
printf '#!/bin/sh
echo "journalctl $*" >>"$FAKE_LOG"; echo "fake journal line"
' >"$FB/journalctl"
chmod +x "$FB"/*
export PATH="$FB:$PATH"
export LAI_ETC_DIR="$w/etc-localai"
export LAI_HEALTH_URL="file://$w/health" LAI_HEALTH_WAIT=2
echo ok >"$w/health"; mkdir -p "$LAI_ETC_DIR"

# shellcheck source=scripts/tests/test-installer-p5p6-fixtures.sh
source "$SRC/scripts/tests/test-installer-p5p6-fixtures.sh"

# A hand-stamped copy (fake version and key) for runs that must not be refused as "unstamped".
STAMPED="$w/install-node.fake-stamped.sh"
sed -e 's|@@LAI_RELEASE_TAG@@|node-v9.9.9-test|g' -e 's|@@LAI_RELEASE_VERSION@@|9.9.9-test|g' \
    -e 's|@@LAI_MINISIGN_PUBKEY@@|RWTESTTESTTESTTESTTESTTESTTESTTESTTESTTESTTESTTESTTEST|g' "$INST" >"$STAMPED"; chmod +x "$STAMPED"

# INST-05 fixtures: fake llama.cpp root, node.yaml, pins, and fake aictl/aiagent/systemctl/ss/curl (no root, no network).
LL="$w/llama"; mkdir -p "$LL/builds" "$LL/src" "$w/llfake" "$w/homes" "$w/lsystemd"
C40="f3586e66b$(printf '0%.0s' $(seq 31))"; C40B="a1b2c3d4e$(printf '1%.0s' $(seq 31))"
printf 'node_id: ai-n4\nllama:\n  backend: cpu\n  commit: %s\n' "$C40" >"$w/node.yaml"
mkdir -p "$w/fx/llama.cpp-$C40"; : >"$w/fx/llama.cpp-$C40/CMakeLists.txt"; tar -czf "$w/fx.tar.gz" -C "$w/fx" "llama.cpp-$C40"
cat >"$w/llfake/aictl" <<'EOS'
#!/bin/sh
echo "aictl $*" >>"$FAKE_LOG"
[ "$1 $2" = "kit install-debs" ] && exit 0   # P6 offline (INST-04)
[ "${FAKE_AICTL_FAIL:-0}" = 1 ] && { echo "cmake: error: boom"; exit 1; }
while [ $# -gt 0 ]; do case "$1" in --commit) c=$2;; --backend) b=$2;; --source) s=$2;; esac; shift; done
[ -f "$s/CMakeLists.txt" ] || { echo "no source at $s"; exit 1; }
d="$LAI_INSTALL_LLAMA_ROOT/builds/$(echo "$c" | cut -c1-9)-$b"; mkdir -p "$d/bin"
printf '#!/bin/sh\necho "version: 1 (%s)"\n' "$(echo "$c" | cut -c1-7)" >"$d/bin/llama-server"; chmod +x "$d/bin/llama-server"
EOS
printf '#!/bin/sh\necho "aiagent $*" >>"$FAKE_LOG"\necho "migrating..."\nexit "${FAKE_AIAGENT_RC:-0}"\n' >"$w/llfake/aiagent"
printf '#!/bin/sh\n[ "$1" = is-active ] && exit "${FAKE_QWEN_INACTIVE:-0}"\nexit 0\n' >"$w/llfake/systemctl"
printf '#!/bin/sh\n[ -n "$FAKE_SS" ] && echo "LISTEN 0 4096 127.0.0.1:8080 0.0.0.0:*"\nexit 0\n' >"$w/llfake/ss"
printf '#!/bin/sh\necho "curl $*" >>"$FAKE_LOG"\nwhile [ $# -gt 0 ]; do [ "$1" = -o ] && o=$2; shift; done\ncp "%s" "$o"\n' "$w/fx.tar.gz" >"$w/llfake/curl"
chmod +x "$w/llfake/"*
export FAKE_LOG="$w/fake.log"
llhooks=(LAI_INSTALLER_DEV=1 LAI_INSTALL_LLAMA_ROOT="$LL" LAI_INSTALL_NODE_YAML="$w/node.yaml" LAI_INSTALL_PINS="$w/pins.json"
  LAI_INSTALL_AICTL="$w/llfake/aictl" LAI_INSTALL_AIAGENT="$w/llfake/aiagent" LAI_INSTALL_SYSTEMCTL="$w/llfake/systemctl"
  LAI_INSTALL_SS="$w/llfake/ss" LAI_INSTALL_CURL="$w/llfake/curl" LAI_INSTALL_HOME_ROOT="$w/homes")
# ---- INST-02 fixtures: a fake machine (rootfs + fake commands), a fake release and a fake ark drive. No root needed.
REAL_MS=0; command -v minisign >/dev/null 2>&1 && REAL_MS=1   # before the fake minisign is put on PATH
I2_FX="$w/i2-fx"; I2_FB="$w/i2-fakebin"; I2_REL="$w/i2-rel"; I2_ARK="$w/i2-ark"; I2_IROOT="$w/i2-root"; mkdir -p "$I2_FB" "$I2_REL"
i2_mkfx() { # i2_mkfx <dir> <os-release id> <version> <codename> [meminfo kB] [chassis]
  mkdir -p "$1/etc" "$1/proc" "$1/sys/class/dmi/id"
  printf 'ID=%s\nVERSION_ID="%s"\nVERSION_CODENAME=%s\n' "$2" "$3" "$4" >"$1/etc/os-release"
  printf 'MemTotal:       %s kB\n' "${5:-16384000}" >"$1/proc/meminfo"; echo "${6:-3}" >"$1/sys/class/dmi/id/chassis_type"
}
i2_mkfx "$I2_FX" ubuntu 24.04 noble; i2_mkfx "$w/i2-fx-debian" debian 12 bookworm; i2_mkfx "$w/i2-fx-2510" ubuntu 25.10 questing
i2_mkfx "$w/i2-fx-2604" ubuntu 26.04 resolute; i2_mkfx "$w/i2-fx-lowram" ubuntu 24.04 noble 4000000; i2_mkfx "$w/i2-fx-laptop" ubuntu 24.04 noble 16384000 10
mkdir -p "$w/i2-fx-laptop/sys/class/power_supply/BAT0"; echo Battery >"$w/i2-fx-laptop/sys/class/power_supply/BAT0/type"
echo Discharging >"$w/i2-fx-laptop/sys/class/power_supply/BAT0/status"
i2_mkfx "$w/i2-fx-amd" ubuntu 24.04 noble; mkdir -p "$w/i2-fx-amd/sys/class/drm/card0/device"; echo 0x1002 >"$w/i2-fx-amd/sys/class/drm/card0/device/vendor"
echo 536870912 >"$w/i2-fx-amd/sys/class/drm/card0/device/mem_info_vram_total"; echo 34359738368 >"$w/i2-fx-amd/sys/class/drm/card0/device/mem_info_gtt_total"
i2_fake() { printf '#!/bin/bash\n%s\n' "$2" >"$I2_FB/$1"; chmod +x "$I2_FB/$1"; }
i2_fake dpkg 'case "$1" in --print-architecture) echo "${FAKE_ARCH:-amd64}" ;; *) exec /usr/bin/dpkg "$@" ;; esac'
i2_fake systemd-detect-virt 'echo "${FAKE_VIRT:-none}"; [ "${FAKE_VIRT:-none}" = none ] && exit 1 || exit 0'
i2_fake lspci 'case "${FAKE_GPU:-none}" in nvidia|broken) echo "01:00.0 VGA compatible controller [0300]: NVIDIA Corporation TU104 [10de:1e84] (rev a1)" ;; esac'
i2_fake nvidia-smi '[ "${FAKE_GPU:-none}" = nvidia ] || exit 9; echo "NVIDIA GeForce RTX 2080, 8192 MiB, 550.54, 7.5"'
i2_fake ss 'printf "%s" "${FAKE_SS:-}"'
i2_fake df 'echo "     Avail"; echo "${FAKE_AVAIL:-900000000000}"'
i2_fake mokutil 'echo "SecureBoot disabled"'
i2_fake ubuntu-drivers 'echo "ubuntu-drivers $*" >>"${FAKE_CALLS:-/dev/null}"'
# The fake minisign accepts a signature file that holds "SIG:<public key>:<sha256 of the signed file>".
i2_fake minisign '[ "$1" = -Vm ] || { PATH="${PATH#'"$I2_FB"':}"; exec minisign "$@"; }   # key generation and signing go to the real tool
f=""; k=""; x=""; while [ $# -gt 0 ]; do case "$1" in -Vm) f="$2"; shift ;; -P) k="$2"; shift ;; -x) x="$2"; shift ;; esac; shift; done
[ "$(cat "$x" 2>/dev/null)" = "SIG:$k:$(sha256sum "$f" | cut -d" " -f1)" ]'
cat >"$w/i2-fake-agent" <<'AGENT'
#!/bin/bash
# fake aiagent: version, and the setup steps used by the installer. Calls are logged to $FAKE_CALLS.
echo "aiagent $*" >>"${FAKE_CALLS:-/dev/null}"
case "$1" in
  version) echo "aiagent 9.9.9-test (fake)" ;;
  setup) case "$2" in
    preflight) exit "${FAKE_PREFLIGHT_RC:-0}" ;;
    catalog) echo "installed catalog fake-1" ;;
    profile-suggest)
      p="small-8gb"; j=0; prev=""
      for a in "$@"; do [ "$prev" = --profile ] && p="$a"; [ "$a" = --json ] && j=1; prev="$a"; done
      if [ "$j" = 1 ]; then printf '{\n  "profile": "%s",\n  "mismatch": %s,\n  "mismatch_code": "LAI-CAT-005",\n  "mismatch_message": "Profile is meant for other hardware.",\n  "min_free_bytes": %s,\n  "budget_bytes": 100\n}\n' "$p" "${FAKE_MISMATCH:-false}" "${FAKE_MINFREE:-1000}"
      else echo "Profile: Small GPU ($p)"; echo "  survival models (always kept for emergencies): Model A"; fi ;;
    *) exit 2 ;; esac ;;
  *) exit 2 ;;
esac
AGENT
chmod +x "$w/i2-fake-agent"
I2_PUBKEY="RWTESTTESTTESTTESTTESTTESTTESTTESTTESTTESTTESTTESTTEST"
i2_mkrel() { # i2_mkrel <dir> [agent source]: agent files, SHA256SUMS, fake signature, installer
  local d="$1" a="${2:-$w/i2-fake-agent}"; mkdir -p "$d"
  cp "$a" "$d/aiagent-linux-amd64"; cp "$a" "$d/aiagent-linux-arm64"; cp "$STAMPED" "$d/install-node.sh"
  (cd "$d" && sha256sum aiagent-linux-amd64 aiagent-linux-arm64 install-node.sh >SHA256SUMS \
    && printf 'SIG:%s:%s' "$I2_PUBKEY" "$(sha256sum SHA256SUMS | cut -d' ' -f1)" >SHA256SUMS.minisig)
}
i2_mkrel "$I2_REL"
i2_mkark() { # i2_mkark <dir>: a drive with the kit for noble/amd64
  local k="$1/localai/kit"; mkdir -p "$k/bin" "$k/installers" "$k/catalog" "$k/debs/noble-amd64" "$k/nvidia/noble-amd64-595-open"
  mkdir -p "$k/src"; cp "$w/fx.tar.gz" "$k/src/llama.cpp-$C40B.tar.gz"   # the llama.cpp source P7 looks for (INST-05 fixture)
  echo '{}' >"$1/localai/ROOT.json"; echo 2026.11.02-1 >"$k/catalog/CATALOG_VERSION"; echo '{}' >"$k/debs/noble-amd64/MANIFEST.json"
  echo '{}' >"$k/nvidia/noble-amd64-595-open/MANIFEST.json"
  i2_mkrel "$1/rel-src"; cp "$1"/rel-src/aiagent-linux-* "$1"/rel-src/SHA256SUMS "$1"/rel-src/SHA256SUMS.minisig "$k/bin/"
  cp "$1/rel-src/install-node.sh" "$k/installers/"; rm -rf "$1/rel-src"
}
i2_mkark "$I2_ARK"
echo ok >"$w/i2-internet-ok"
# Every run below sees the fake machine unless a test overrides a variable (the hooks are read only with LAI_INSTALLER_DEV=1).
export PATH="$I2_FB:$PATH" SUDO_USER=tester LAI_FAKE_ROOT="$I2_FX" LAI_FAKE_EUID=0 LAI_FAKE_YEAR=2026 LAI_INSTALL_ROOT="$I2_IROOT" \
  LAI_INTERNET_URL="file://$w/i2-internet-ok" LAI_RELEASE_BASE_URL="file://$I2_REL" FAKE_CALLS="$w/i2-calls.log"
# dry <args...>: a dry run of the template in a throwaway folder
dry() { env "${llhooks[@]}" LAI_INSTALL_NODE_YAML="$w/none.yaml" LAI_INSTALL_STATE_DIR="$w/state" "$INST" --dry-run --log "$w/dry.log" "$@"; }
# real <args...>: a real run of the skeleton (dev hooks only: state and log in $w)
real() { env "${llhooks[@]}" LAI_INSTALL_STATE_DIR="$w/rstate" LAI_INSTALL_SYSTEMD_DIR="$w/systemd" "$STAMPED" --log "$w/real.log" "$@"; }
# dryv / realv VAR=value... -- <args...>: the same with extra environment (fake machine variants)
dryv() { local e=(); while [[ $1 != -- ]]; do e+=("$1"); shift; done; shift; setsid -w env "${e[@]}" "${llhooks[@]}" LAI_INSTALL_NODE_YAML="$w/none.yaml" LAI_INSTALL_STATE_DIR="$w/state" "$STAMPED" --dry-run --log "$w/i2-dryv.log" "$@"; }
realv() { local e=(); while [[ $1 != -- ]]; do e+=("$1"); shift; done; shift; setsid -w env "${e[@]}" "${llhooks[@]}" LAI_INSTALL_STATE_DIR="$w/i2-vstate" LAI_INSTALL_SYSTEMD_DIR="$w/i2-vsystemd" "$STAMPED" --log "$w/i2-realv.log" "$@"; }

echo "== arguments"
t "help exits 0" 0 "Usage:" "$INST" --help
t "template says it is unstamped and stops" 2 "never stamped" "$INST" --log "$w/x.log"
t "valid flags are all accepted" 0 "==> [15/15]" dry --node-id ai-n4 --profile desk-small --backend cpu --cloud-url https://ai.example.test \
  --no-migrate --yes --pin-file "$INST" --inventory "$INST" --keep-inventory --no-hardening --no-ssh --no-polkit --no-tailscale \
  --no-sync --serve-pattern A --reboot --enroll-file "$INST"
mkdir -p "$w/localai/kit"   # P6 offline needs a kit folder on the drive
t "from-ark, desktop-user and agent-sha256" 0 "source: ark:$I2_ARK" dryv -- --from-ark "$I2_ARK" --desktop-user root --agent-sha256 "$fakehex"
t "repair is parsed (dry run)" 0 "[dry-run] would run: aictl doctor" dry --repair
t "uninstall with its modifiers is parsed (dry run)" 0 "Summary:" dry --uninstall --purge-config --purge-content --revert-hardening
t "unknown flag -> 2" 2 "unknown argument '--bogus'" dry --bogus
t "positional argument -> 2" 2 "unknown argument" dry extra
t "flag without value -> 2" 2 "needs a value" dry --node-id
for bad in ai-n0 ai-n12345 ai-n01 n1 AI-N1 ai-n; do
  t "node id '$bad' -> 2" 2 "--node-id" dry --node-id "$bad"
done
for good in ai-n1 ai-n9999 ai-n42; do t "node id '$good' ok" 0 "" dry --node-id "$good"; done
t "bad backend -> 2" 2 "--backend" dry --backend tpu
t "bad serve pattern -> 2" 2 "--serve-pattern" dry --serve-pattern C
t "bad agent-sha256 (short) -> 2" 2 "64 lowercase hex" dry --agent-sha256 abc123
t "bad agent-sha256 (upper case) -> 2" 2 "64 lowercase hex" dry --agent-sha256 "${fakehex^^}"
t "missing desktop user -> 2" 2 "does not exist" dry --desktop-user no-such-user-lai
t "missing from-ark folder -> 2" 2 "not a folder" dry --from-ark "$w/nope"
t "missing enroll-file -> 2" 2 "not a file" dry --enroll-file "$w/nope"
t "cloud url with credentials -> 2" 2 "--cloud-url" dry --cloud-url "https://user:pw@example.test"
t "migrate and no-migrate conflict -> 2" 2 "conflict" dry --migrate --no-migrate
t "hardening and no-hardening conflict -> 2" 2 "conflict" dry --hardening --no-hardening
t "repair and uninstall conflict -> 2" 2 "conflict" dry --repair --uninstall
t "purge without uninstall -> 2" 2 "only work with --uninstall" dry --purge-config
t "keep-inventory without inventory -> 2" 2 "--keep-inventory" dry --keep-inventory
t "enroll without a tty -> 2 with the hint" 2 "use --enroll-file" setsid -w env LAI_INSTALLER_DEV=1 "$INST" --dry-run --log "$w/x.log" --enroll
t "enroll and enroll-file conflict -> 2" 2 "conflict" dry --enroll --enroll-file "$INST"
out="$(dry "$fakecred" 2>&1)"; rc=$?
t "a credential as an argument is refused (exit 2)" 0 "" test $rc -eq 2
t "the refusal says why" 0 "never accepted as an argument" grep -F "never accepted as an argument" <<<"$out"
t "the refused credential is not echoed" 0 "" bash -c '! grep -q "$1" <<<"$2"' _ "$fakecred" "$out"
t "token-looking flags are refused" 2 "never accepted as an argument" dry --token abc
t "non-root real run without dev hooks -> 3" 3 "LAI-INS-003" bash -c 'if [ "$(id -u)" = 0 ]; then echo "LAI-INS-003 (skipped as root)"; exit 3; fi; exec "$1" --log "$2" </dev/null' _ "$STAMPED" "$w/x.log"

echo "== scrub"
scrub_of() { bash -c 'source "$1"; printf "%s\n" "$2" | scrub' _ "$INST" "$1"; }
for p in nod lat lai pat; do
  t "scrubs ${p}_ tokens" 0 "<redacted:${p}_…wxyz>" scrub_of "key ${p}_AbCdEf123456wxyz end"
done
t "scrubs a 64-hex string" 0 "<redacted:hex…abab>" scrub_of "sha $fakehex ok"
t "scrubs a longer hex run" 0 "<redacted:hex…abab>" scrub_of "${fakehex}${fakehex}"
t "scrubs two tokens in one mixed line" 0 "<redacted:nod_…1234> and <redacted:pat_…9999> and <redacted:hex…abab>" scrub_of "$fakecred and pat_zz9999 and $fakehex"
t "leaves ordinary text and short hex alone" 0 "node ai-n4 plat_form deadbeef" scrub_of "node ai-n4 plat_form deadbeef"
t "the token never survives" 0 "" bash -c '! bash -c "source \"\$1\"; printf \"%s\n\" \"\$2\" | scrub" _ "$1" "$2" | grep -q "FAKEFAKEFAKE"' _ "$INST" "$fakecred"

echo "== dry run changes nothing"
mkdir -p "$w/tmp"; before="$(find "$w" -mindepth 1 -not -path "$w/tmp*" -not -name 'dry.log' -not -name 'x.log' | sort | md5sum)"
dryout="$(dry --node-id ai-n9 2>&1)"; rc=$?
t "dry run exits 0" 0 "" test $rc -eq 0
t "prints all 15 phase headers" 0 "" bash -c 'for n in $(seq 1 15); do grep -q "^==> \[$n/15\]" <<<"$1" || exit 1; done' _ "$dryout"
# Implemented: P1-P3 (INST-02), P5, P6 (INST-04), P7, P8 (INST-05), P9, P10, P12, P13 (INST-06), P11, P14, P15 (INST-07); STUBS_IN_DRY_RUN phases are still stubs. Lower this as items land.
STUBS_IN_DRY_RUN=1
t "every phase that is not implemented says so" 0 "" test "$(grep -c '^NOT IMPLEMENTED YET: this phase' <<<"$dryout")" = "$STUBS_IN_DRY_RUN"
t "phases 7 and 8 are implemented, not stubs" 0 "" bash -c '! sed -n "/^==> \[7\/15\]/,/^==> \[9\/15\]/p" <<<"$1" | grep -q "NOT IMPLEMENTED"' _ "$dryout"
t "says the installer is incomplete" 0 "" grep -q "THIS INSTALLER IS INCOMPLETE" <<<"$dryout"
t "uses the [dry-run] marker" 0 "" grep -q '^\[dry-run\] would record P1 as' <<<"$dryout"
t "no state folder, state.json or lock was created" 0 "" test ! -e "$w/state"
after="$(find "$w" -mindepth 1 -not -path "$w/tmp*" -not -name 'dry.log' -not -name 'x.log' | sort | md5sum)"
t "nothing else under the work folder changed" 0 "" test "$before" = "$after"

echo "== log"
t "log is 0600" 0 "" test "$(stat -c %a "$w/dry.log")" = 600
t "log lines are '<UTC ts> [<phase tag>] <message>'" 0 "" bash -c '! grep -qvE "^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z \[P[0-9]+ [a-z_0-9 ]+\] " "$1"' _ "$w/dry.log"
t "log names phase P6 packages" 0 "" grep -q ' \[P6 packages\] ' "$w/dry.log"
: >"$w/rot.log"; head -c $((10 * 1024 * 1024 + 10)) /dev/zero | tr '\0' 'x' >"$w/rot.log"
t "a log over 10 MB is rotated to .1" 0 "" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$2" "$1" --dry-run --log "$3" >/dev/null && test -f "$3.1" && test "$(stat -c %s "$3")" -lt 100000' _ "$INST" "$w/state" "$w/rot.log"
t "-v traces into the log only and scrubs" 0 "" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$2" "$1" --dry-run -v --log "$3" >"$4" 2>&1; ! grep -q "^+" "$4"; grep -q "^+" "$3"; ! grep -q FAKEFAKEFAKE "$3"' _ "$INST" "$w/state" "$w/v.log" "$w/v.out"

echo "== state.json (real run of the skeleton, dev hooks)"
mkdir -p "$w/systemd"
realout="$(real --node-id ai-n9 2>&1)"; rc=$?
t "real skeleton run exits 0" 0 "" test $rc -eq 0
t "kind is fresh" 0 "kind: fresh" grep -F "kind: fresh" <<<"$realout"
s="$w/rstate/state.json"
t "state.json exists, 0600" 0 "" test "$(stat -c %a "$s")" = 600
t "state.json is valid JSON when python is present" 0 "" bash -c 'command -v python3 >/dev/null || exit 0; python3 -I -c "import json,sys; d=json.load(open(sys.argv[1])); assert len(d[\"phases\"])==15" "$1"' _ "$s"
# Phases whose real implementation has landed (INST-02: P1-P3; INST-04: P5, P6; INST-05: P7, P8; INST-07: P11, P14 under the skeleton's defaults). Every other phase
# is still a stub and must be recorded as not-implemented; add a phase id here when its item is merged.
IMPLEMENTED="P1 P2 P3 P5 P6 P7 P8 P9 P10 P11 P12 P13 P14"
t "every phase has status, version, finished_at, details" 0 "" bash -c '[ "$(grep -cE "\"status\":\"(not-implemented|done)\",\"version\":\"9.9.9-test\",\"finished_at\":\"20[0-9-]*T[0-9:]*Z\",\"details\":" "$1")" = 15 ]' _ "$s"
t "a stub is never recorded as done" 0 "" bash -c 'for i in $(seq 1 15); do case " $2 " in *" P$i "*) continue ;; esac; grep -q "\"P$i\": .*\"status\":\"done\"" "$1" && exit 1; done; exit 0' _ "$s" "$IMPLEMENTED"
t "P1, P2 and P3 are recorded as done" 0 "" bash -c 'for p in P1 P2 P3; do grep -q "\"$p\": {[^}]*\"status\":\"done\"" "$1" || exit 1; done' _ "$s"
t "atomic: no temp file left behind" 0 "" test -z "$(find "$w/rstate" -name '.state.*')"
t "lock file exists after a real run" 0 "" test -f "$w/rstate/lock"
rerun="$(real --node-id ai-n9 2>&1)"
t "second run is a re-run" 0 "kind: re-run" grep -F "kind: re-run" <<<"$rerun"
touch "$w/systemd/llama-qwen.service"
t "legacy llama-qwen.service means migration" 0 "kind: migration" bash -c 'env "${@:5}" LAI_INSTALL_STATE_DIR="$2" LAI_INSTALL_SYSTEMD_DIR="$3" "$1" --yes --log "$4" 2>&1' _ "$STAMPED" "$w/rstate" "$w/systemd" "$w/real.log" "${llhooks[@]}"
rm -f "$w/systemd/llama-qwen.service"
t "state load reads what state write wrote" 0 "" bash -c 'source "$1"; STATE_FILE="$2"; state_load; [ "${PH_STATUS[P1]}" = done ] && [ "${PH_STATUS[P6]}" = done ] && [ "${PH_STATUS[P7]}" = done ] && [ "${PH_STATUS[P9]}" = done ] && [ "${#PH_STATUS[@]}" = 15 ]' _ "$INST" "$s"
# A phase that is done and whose check passes reports "already done" (the hook later items use).
t "idempotency: a done phase with a passing check says 'already done'" 0 "already done" bash -c '
  source "$1"; DRY_RUN=1; STATE_FILE="$2/none"; PH_STATUS[P1]=done; check_preflight() { return 0; }
  run_phase 1 "${PHASE_TABLE[0]}"' _ "$INST" "$w"
t "a failing phase marks failed, exits 1 and resumes later" 1 "FAILED (exit 7)" bash -c '
  source "$1"; set -Eeuo pipefail; DRY_RUN=1; STATE_FILE="$2/none"; LOG_OPEN=; phase_preflight() { return 7; }
  run_phase 1 "${PHASE_TABLE[0]}"' _ "$INST" "$w"

echo "== INST-06: P9 agent/node.yaml/units, P10 API key, P12 admin PIN, P13 enrollment (fakes, dev hooks)"
n6=0
# r6 <args...>: a real run of the stamped copy against the fakes, in its own state folder. Output to $w/r6.out.
h6=(env "${llhooks[@]}" LAI_INSTALL_AIAGENT="$FB/aiagent" LAI_INSTALL_AICTL="$FB/aictl" LAI_INSTALL_LLAMA_ROOT="$w/llama6")  # INST-05 fixtures, with this item's fakes and llama root
r6() { n6=$((n6 + 1)); : >"$FAKE_LOG"; rm -rf "${FAKE_STDIN_DIR:?}"/*; mkdir -p "$w/sd6"
  "${h6[@]}" LAI_INSTALL_STATE_DIR="$w/s6-$n6" LAI_INSTALL_SYSTEMD_DIR="$w/sd6" "$STAMPED" --log "$w/r6-$n6.log" --no-tailscale --no-sync "$@" >"$w/r6.out" 2>&1; }
l6() { cat "$w/r6-$n6.log"; }
pin8=73190846; pinf="$w/pin.txt"; printf '%s\n' "$pin8" >"$pinf"
credf="$w/cred.txt"; printf '%s\n' "$fakecred" >"$credf"
rm -rf "$LAI_ETC_DIR"; mkdir -p "$LAI_ETC_DIR" "$w/llama6/builds/${C40:0:9}-cpu/bin"
printf '#!/bin/sh\necho "version: 1 (%s)"\n' "${C40:0:7}" >"$w/llama6/builds/${C40:0:9}-cpu/bin/llama-server"; chmod +x "$w/llama6/builds/${C40:0:9}-cpu/bin/llama-server"

r6 --node-id ai-n9 --profile desk-small --backend cpu --serve-pattern A --inventory "$INST" --pin-file "$pinf" --enroll-file "$credf" --no-ssh; rc=$?
t "full run with the fakes exits 0" 0 "" test $rc -eq 0
t "P9 passes node.yaml flags to aiagent setup node-yaml" 0 "aiagent setup node-yaml --node-id ai-n9 --profile desk-small --backend cpu --serve-pattern A --inventory $INST" cat "$FAKE_LOG"
t "P9 runs aiagent setup units" 0 "aiagent setup units --backup-dir" cat "$FAKE_LOG"
t "P9 restarts the agent, then waits for health" 0 "systemctl restart localai-agent.service" cat "$FAKE_LOG"
t "P9 says the agent is healthy" 0 "the agent is healthy" cat "$w/r6.out"
t "P9 activates the build P7 made for this node" 0 "builds/${C40:0:9}-cpu" bash -c 'readlink "$1/current"' _ "$w/llama6"
t "P9 starts localai-llama after activating it" 0 "systemctl start localai-llama.service" cat "$FAKE_LOG"
t "P9 saves the active build in the kit (aictl kit export-build)" 0 "aictl kit export-build" cat "$FAKE_LOG"
t "...after the model server was started" 0 "" bash -c 'a=$(grep -n "systemctl start localai-llama" "$1" | head -1 | cut -d: -f1); b=$(grep -n "aictl kit export-build" "$1" | head -1 | cut -d: -f1); [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]' _ "$FAKE_LOG"
t "P10 runs aiagent setup api-key" 0 "aiagent setup api-key" cat "$FAKE_LOG"
t "P12 pipes the PIN to aictl pin set --pin-stdin" 0 "aictl pin set --pin-stdin" cat "$FAKE_LOG"
t "P12 delivered the PIN on stdin" 0 "$pin8" cat "$FAKE_STDIN_DIR/pin-set"
t "the PIN is not in any argument" 0 "" bash -c '! grep -q "$1" "$2"' _ "$pin8" "$FAKE_LOG"
t "the PIN is not on the screen" 0 "" bash -c '! grep -q "$1" "$2"' _ "$pin8" "$w/r6.out"
t "the PIN is not in the log" 0 "" bash -c '! grep -q "$1" "$2"' _ "$pin8" "$w/r6-$n6.log"
t "P13 enrolls with the credential on stdin only" 0 "aictl cloud enroll --url https://ai.marin.cr --credential-stdin" cat "$FAKE_LOG"
t "the credential reached aictl's stdin" 0 "$fakecred" cat "$FAKE_STDIN_DIR/cloud-enroll"
t "the credential is not in any argument, screen or log" 0 "" bash -c '! grep -q FAKEFAKEFAKE "$1" "$2" "$3"' _ "$FAKE_LOG" "$w/r6.out" "$w/r6-$n6.log"
t "state.json records P9 P10 P12 P13 as done" 0 "" bash -c 'for p in P9 P10 P12 P13; do grep "\"$p\"" "$1" | grep -q "\"status\":\"done\"" || exit 1; done' _ "$w/s6-$n6/state.json"

# -v must not put the PIN or credential in the log through set -x
r6 -v --node-id ai-n9 --pin-file "$pinf" --enroll-file "$credf"; rc=$?
t "-v run exits 0" 0 "" test $rc -eq 0
t "-v: the PIN is not in the log" 0 "" bash -c '! grep -q "$1" "$2"' _ "$pin8" "$w/r6-$n6.log"
t "-v: the credential is not in the log" 0 "" bash -c '! grep -q FAKEFAKEFAKE "$1"' _ "$w/r6-$n6.log"
t "-v: the trace itself is in the log (so the test means something)" 0 "" grep -q '^+ phase_agent' "$w/r6-$n6.log"

# P9 failures
echo >"$w/health"; rm -f "$w/health"
r6 --node-id ai-n9; rc=$?
t "agent never healthy -> exit 1" 0 "" test $rc -eq 1
t "agent never healthy -> LAI-INS-034 with the journal" 0 "LAI-INS-034" cat "$w/r6.out"
t "agent never healthy -> journalctl -u localai-agent -n 50 was shown" 0 "journalctl -u localai-agent -n 50" cat "$FAKE_LOG"
echo ok >"$w/health"
FAKE_AIAGENT_RC=1 FAKE_AIAGENT_STEP=node-yaml FAKE_AIAGENT_MSG="ERROR LAI-INS-020: this computer already has the node id ai-n4" r6 --node-id ai-n9; rc=$?
t "a different node id -> exit 1 with LAI-INS-020" 0 "LAI-INS-020" bash -c 'test '"$rc"' -eq 1 && cat "$1"' _ "$w/r6.out"
t "a different node id stops before the units are written" 0 "" bash -c '! grep -q "setup units" "$1"' _ "$FAKE_LOG"
FAKE_AIAGENT_RC=1 FAKE_AIAGENT_STEP=units FAKE_AIAGENT_MSG="boom" r6 --node-id ai-n9; rc=$?
t "units failure -> exit 1, agent not restarted" 0 "" bash -c 'test '"$rc"' -eq 1 && ! grep -q "restart localai-agent" "$1"' _ "$FAKE_LOG"

# The export is best effort: the build is already in use
FAKE_EXPORT_RC=1 r6 --node-id ai-n9; rc=$?
t "a failing kit export is a warning, not a failed install" 0 "WARNING: the build is in use but could not be saved in the kit" bash -c 'test '"$rc"' -eq 0 && cat "$1"' _ "$w/r6.out"
t "...and it names the command to try later" 0 "sudo aictl kit export-build" cat "$w/r6.out"
t "export_builds with no active build says so and runs nothing" 0 "nothing to save in the kit yet" bash -c 'source "$1"; DEV=1; export LAI_INSTALLER_DEV=1; LAI_INSTALL_LLAMA_ROOT="$2"; LAI_INSTALL_AICTL="$3"; inst06_env; say() { echo "$*"; }; : >"$FAKE_LOG"; export_builds && ! grep -q "export-build" "$FAKE_LOG"' _ "$INST" "$w/llama6-empty" "$FB/aictl"

# P10..P13 behaviour
echo hash >"$LAI_ETC_DIR/admin-pin"
r6 --node-id ai-n9 --pin-file "$pinf"; rc=$?
t "PIN already set: not asked, not changed" 0 "the admin PIN is already set" bash -c 'test '"$rc"' -eq 0 && cat "$1"' _ "$w/r6.out"
t "PIN already set: aictl pin set not called" 0 "" bash -c '! grep -q "pin set" "$1"' _ "$FAKE_LOG"
rm -f "$LAI_ETC_DIR/admin-pin"
printf '12\n' >"$w/badpin"
r6 --node-id ai-n9 --pin-file "$w/badpin"; rc=$?
t "bad PIN file -> warning LAI-INS-060, install continues (exit 0)" 0 "LAI-INS-060" bash -c 'test '"$rc"' -eq 0 && cat "$1"' _ "$w/r6.out"
t "bad PIN file: aictl pin set not called" 0 "" bash -c '! grep -q "pin set" "$1"' _ "$FAKE_LOG"
: >"$FAKE_LOG"
t "no tty and no file -> skip with LAI-INS-060 (exit 0)" 0 "LAI-INS-060" bash -c '"${@:5}" LAI_INSTALL_STATE_DIR="$2" LAI_INSTALL_SYSTEMD_DIR="$3" setsid -w "$1" --log "$4" --no-tailscale --no-sync --node-id ai-n9 </dev/null 2>&1' _ "$STAMPED" "$w/s6-notty" "$w/sd6" "$w/notty.log" "${h6[@]}"
FAKE_AICTL_RC=1 FAKE_AICTL_MSG="cannot store PIN" r6 --node-id ai-n9 --pin-file "$pinf"; rc=$?
t "aictl pin set failing -> warning, exit 0" 0 "LAI-INS-060" bash -c 'test '"$rc"' -eq 0 && cat "$1"' _ "$w/r6.out"
t "aictl output that echoes the PIN is masked" 0 "<pin>" cat "$w/r6.out"
t "...and the PIN itself never shows" 0 "" bash -c '! grep -q "$1" "$2" "$3"' _ "$pin8" "$w/r6.out" "$w/r6-$n6.log"
touch "$LAI_ETC_DIR/cloud-credential"
r6 --node-id ai-n9 --enroll-file "$credf"; rc=$?
t "already enrolled -> skipped" 0 "already enrolled" bash -c 'test '"$rc"' -eq 0 && cat "$1"' _ "$w/r6.out"
t "already enrolled: aictl cloud enroll not called" 0 "" bash -c '! grep -q "cloud enroll" "$1"' _ "$FAKE_LOG"
rm -f "$LAI_ETC_DIR/cloud-credential"
FAKE_AICTL_RC=1 FAKE_AICTL_MSG="LAI-CLD-053 the cloud said no" r6 --node-id ai-n9 --enroll-file "$credf"; rc=$?
t "enroll failure does not fail the install" 0 "" test $rc -eq 0
t "enroll failure shows the code and the retry command" 0 "sudo aictl cloud enroll --url https://ai.marin.cr --credential-stdin" cat "$w/r6.out"
t "enroll failure keeps the cloud's code" 0 "WARNING LAI-CLD-053" cat "$w/r6.out"
t "enroll failure: the credential is not shown" 0 "" bash -c '! grep -q FAKEFAKEFAKE "$1" "$2"' _ "$w/r6.out" "$w/r6-$n6.log"
printf 'not-a-credential\n' >"$w/badcred"
r6 --node-id ai-n9 --enroll-file "$w/badcred"; rc=$?
t "a malformed credential -> warning, install continues, aictl not called" 0 "" bash -c 'test '"$rc"' -eq 0 && grep -q "WARNING LAI-CLD-051" "$2" && ! grep -q "cloud enroll" "$1"' _ "$FAKE_LOG" "$w/r6.out"
r6 --node-id ai-n9; rc=$?
t "no --enroll: enrollment is skipped quietly" 0 "enrollment not requested" bash -c 'test '"$rc"' -eq 0 && cat "$1"' _ "$w/r6.out"

# migration: the llama build is activated by P8, so P9 does not do it
t "migration_ran follows MIGRATION_RAN of P8" 0 "" bash -c 'source "$1"; MIGRATION_RAN=1; migration_ran && { MIGRATION_RAN=0; ! migration_ran; }' _ "$INST"
t "an active llama build is left alone" 0 "already active" bash -c 'source "$1"; DEV=1; export LAI_INSTALLER_DEV=1; LAI_INSTALL_LLAMA_ROOT="$2"; inst06_env; say() { echo "$*"; }; mkdir -p "$2/builds/abc123-cuda/bin"; printf "#!/bin/sh\n" >"$2/builds/abc123-cuda/bin/llama-server"; chmod +x "$2/builds/abc123-cuda/bin/llama-server"; ln -sfn builds/abc123-cuda "$2/current"; activate_llama' _ "$INST" "$w/llama6b"

# check_ hooks
t "check_api_key: true for 64 hex, false for junk" 0 "" bash -c 'source "$1"; DEV=1; LAI_ETC_DIR="$2"; inst06_env; printf "%s\n" "$3" >"$2/api-key"; check_api_key || exit 1; echo junk >"$2/api-key"; ! check_api_key' _ "$INST" "$LAI_ETC_DIR" "$fakehex"
t "check_pin follows /etc/localai/admin-pin" 0 "" bash -c 'source "$1"; DEV=1; LAI_ETC_DIR="$2"; inst06_env; rm -f "$2/admin-pin"; ! check_pin || exit 1; echo x >"$2/admin-pin"; check_pin' _ "$INST" "$LAI_ETC_DIR"
rm -f "$LAI_ETC_DIR/admin-pin" "$LAI_ETC_DIR/api-key"

# dry run: aiagent steps get --dry-run, nothing mutating runs, no secret is read
: >"$FAKE_LOG"
dryout="$("${h6[@]}" LAI_INSTALL_STATE_DIR="$w/dstate6" "$INST" --dry-run --log "$w/dry6.log" --node-id ai-n9 --pin-file "$pinf" --enroll-file "$credf" 2>&1)"; rc=$?
t "dry run exits 0" 0 "" test $rc -eq 0
t "dry run: every aiagent call carries --dry-run" 0 "" bash -c 'grep "^aiagent setup [a-z-]* " "$1" | grep -v -e "--dry-run" | wc -l | grep -qx 0' _ "$FAKE_LOG"
t "dry run: systemctl was never called" 0 "" bash -c '! grep -q "^systemctl" "$1"' _ "$FAKE_LOG"
t "dry run: aictl was never called" 0 "" bash -c '! grep -q "^aictl" "$1"' _ "$FAKE_LOG"
t "dry run says it would save the build in the kit" 0 "would run: $FB/aictl kit export-build" bash -c 'echo "$1"' _ "$dryout"
t "dry run says what it would do for P9, P12, P13" 0 "" bash -c 'grep -q "would run: systemctl restart localai-agent.service" <<<"$1" && grep -q "would set the admin PIN" <<<"$1" && grep -q "would enroll with" <<<"$1"' _ "$dryout"
t "dry run: no PIN or credential on screen or in the log" 0 "" bash -c '! grep -qE "$1|FAKEFAKEFAKE" <<<"$2" "$3"' _ "$pin8" "$dryout" "$w/dry6.log"
t "dry run with no agent installed still exits 0 and says would run" 0 "[dry-run] would run: $w/none/aiagent setup node-yaml" bash -c 'LAI_INSTALL_AIAGENT="$2/none/aiagent" LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$2/dstate7" "$1" --dry-run --log "$2/dry7.log" --node-id ai-n9 2>&1' _ "$INST" "$w"

echo "== lock"
mkdir -p "$w/lk"; : >"$w/lk/lock"
flock "$w/lk/lock" sleep 20 & holder=$!
sleep 0.5
t "second run exits 1 with LAI-INS-004 (dry run)" 1 "LAI-INS-004" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$2" "$1" --dry-run --log "$3"' _ "$INST" "$w/lk" "$w/lk.log"
t "second run exits 1 with LAI-INS-004 (real)" 1 "LAI-INS-004" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$2" "$1" --log "$3"' _ "$STAMPED" "$w/lk" "$w/lk.log"
pkill -P "$holder" 2>/dev/null; kill "$holder" 2>/dev/null; wait "$holder" 2>/dev/null
t "after the holder exits the lock is free" 0 "" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$2" "$1" --dry-run --log "$3" >/dev/null' _ "$INST" "$w/lk" "$w/lk.log"

echo "== dry-run helpers"
h() { bash -c 'source "$1"; DRY_RUN=1; STATE_DIR="$2/hstate"; shift 2; eval "$1"' _ "$INST" "$w" "$1"; }
t "run_or_say prints and does not run" 0 "[dry-run] would run: touch $w/ran" h "run_or_say touch '$w/ran'"
t "run_or_say did not run it" 0 "" test ! -e "$w/ran"
t "write_file_or_diff: new file is shown, not written" 0 "[dry-run] would create $w/new.conf (mode 0644, owner root:root)" h "echo hello | write_file_or_diff '$w/new.conf' 0644 root:root"
t "write_file_or_diff did not write it" 0 "" test ! -e "$w/new.conf"
echo old >"$w/exist.conf"
t "write_file_or_diff: changed file shows a diff" 0 "[dry-run]   +new" h "echo new | write_file_or_diff '$w/exist.conf' 0600 root:root"
t "write_file_or_diff: identical content is 'already up to date'" 0 "already up to date" h "echo old | write_file_or_diff '$w/exist.conf' 0600 root:root"
t "write_file_or_diff left the file alone" 0 "" test "$(cat "$w/exist.conf")" = old
t "svc_or_say shows the unit and verb" 0 "[dry-run] would run: systemctl restart localai-agent" h "svc_or_say restart localai-agent"
t "svc_or_say refuses to restart logind" 2 "refusing to restart systemd-logind" bash -c 'source "$1"; DRY_RUN=1; svc_or_say restart systemd-logind' _ "$INST"
t "svc_or_say refuses unknown verbs" 2 "unsupported verb" h "svc_or_say frobnicate x"
mkdir -p "$w/bin"
printf '#!/bin/sh\nprintf "Inst fake-pkg-a (1.0 Ubuntu)\\nInst fake-pkg-b (2.0 Ubuntu)\\n"\n' >"$w/bin/apt-get"; chmod +x "$w/bin/apt-get"
t "apt_or_say lists the packages apt would install" 0 "[dry-run] would install 2 package(s) with apt-get: fake-pkg-a fake-pkg-b" bash -c 'export PATH="$2:$PATH"; source "$1"; DRY_RUN=1; apt_or_say lai-test-no-such-pkg' _ "$INST" "$w/bin"
t "apt_or_say skips what is installed" 0 "already installed: bash" bash -c 'source "$1"; DRY_RUN=1; apt_or_say bash' _ "$INST"

echo "== uninstall and repair (INST-08; fake root, fake systemctl/tailscale/aictl/userdel/groupdel)"
FB="$w/fakebin"; mkdir -p "$FB"; CALLS="$w/calls.log"
for c in tailscale aictl userdel groupdel; do
  printf '#!/bin/sh\necho "%s $*" >>"%s"\nexit 0\n' "$c" "$CALLS" >"$FB/$c"; chmod +x "$FB/$c"
done
cat >"$FB/systemctl" <<FAKE
#!/bin/sh
# records the call and, for the llama-qwen start, what the disk looked like at that moment
echo "systemctl \$*" >>"$CALLS"
case "\$*" in
  *"enable --now llama-qwen"*)
    k="\$LAI_INSTALL_ROOT/etc/llama-qwen/api-key"
    if [ -f "\$k" ] && [ ! -L "\$k" ]; then echo "STATE key-real-file" >>"$CALLS"; fi
    if [ -d "\$LAI_INSTALL_ROOT/etc/localai" ]; then echo "STATE etc-localai-present" >>"$CALLS"; fi
    if [ -d "\$LAI_INSTALL_ROOT/opt/localai" ]; then echo "STATE opt-localai-present" >>"$CALLS"; fi
    if [ -f "\$LAI_INSTALL_ROOT/etc/systemd/system/llama-qwen.service" ]; then echo "STATE unit-restored" >>"$CALLS"; fi ;;
esac
if [ -n "\$FAKE_SYSTEMCTL_FAIL" ]; then case "\$*" in *"\$FAKE_SYSTEMCTL_FAIL"*) exit 1;; esac; fi
exit 0
FAKE
chmod +x "$FB/systemctl"
FR="$w/fr"
mkroot() { # mkroot [legacy]
  rm -rf "$FR" "$w/answers"; : >"$CALLS"
  mkdir -p "$FR/etc/localai" "$FR/var/lib/localai/install" "$FR/srv/localai/models" "$FR/opt/localai/bin" "$FR/opt/localai/installers" \
    "$FR/etc/systemd/system/localai-llama.service.d" "$FR/usr/local/bin" "$FR/usr/local/sbin" "$FR/etc/polkit-1/rules.d" "$FR/etc/llama-qwen"
  printf 'KEYKEYKEY\n' >"$FR/etc/localai/api-key"; chmod 600 "$FR/etc/localai/api-key"
  printf 'node_id: ai-n4\ntailscale:\n  serve_pattern: B\n' >"$FR/etc/localai/node.yaml"
  printf 'nodcred\n' >"$FR/etc/localai/cloud-credential"
  : >"$FR/srv/localai/models/m.gguf"; : >"$FR/opt/localai/bin/aiagent"
  printf '#!/bin/sh\n' >"$FR/opt/localai/installers/install-node.sh"; chmod 755 "$FR/opt/localai/installers/install-node.sh"
  ln -s /opt/localai/installers/install-node.sh "$FR/usr/local/sbin/install-node.sh"
  ln -s /opt/localai/bin/aiagent "$FR/usr/local/bin/aictl"
  : >"$FR/etc/polkit-1/rules.d/50-localai-operators.rules"
  for u in agent llama kiwix whisper; do : >"$FR/etc/systemd/system/localai-$u.service"; done
  : >"$FR/etc/systemd/system/localai-sync.timer"
  ln -s /etc/localai/api-key "$FR/etc/llama-qwen/api-key"
  printf '{}\n' >"$FR/var/lib/localai/install/state.json"
  if [[ "${1:-}" == legacy ]]; then
    mkdir -p "$FR/var/lib/localai/migration/llama-qwen"; printf '[Service]\nExecStart=/bin/true\n' >"$FR/var/lib/localai/migration/llama-qwen/llama-qwen.service"
    ln -s /dev/null "$FR/etc/systemd/system/llama-qwen.service"
  fi
}
# unenv: the environment of a run on the fake root. un <answers...> -- <args...> feeds the prompts in order.
unenv() { PATH="${UN_PATH:-$FB}:$PATH" LAI_INSTALLER_DEV=1 LAI_INSTALL_ROOT="$FR" LAI_INSTALL_ANSWERS="$w/answers" LAI_INSTALL_SELF="$STAMPED" \
    LAI_INSTALL_STATE_DIR="$FR/var/lib/localai/install" LAI_INSTALL_SYSTEMD_DIR="$FR/etc/systemd/system" env "${llhooks[@]}" "$@"; }
un() {
  local ans=()
  while [[ "$1" != "--" ]]; do ans+=("$1"); shift; done; shift
  : >"$w/answers"; [[ ${#ans[@]} -eq 0 ]] || printf '%s\n' "${ans[@]}" >"$w/answers"
  unenv "$STAMPED" --log "$w/un.log" "$@"
}
calls_has() { grep -qF -- "$1" "$CALLS"; }
calls_lacks() { ! grep -qE -- "$1" "$CALLS"; }

mkroot
t "uninstall: answering n aborts with exit 4, nothing changed" 4 "Not confirmed" un n -- --uninstall
t "  ...and everything is still there" 0 "" test -e "$FR/opt/localai/bin/aiagent" -a -e "$FR/etc/systemd/system/localai-agent.service"
t "uninstall: --yes does not answer the question (no answer = exit 4)" 4 "Not confirmed" un -- --uninstall --yes
t "  ...nothing called systemctl" 0 "" test ! -s "$CALLS"
t "uninstall: plain run exits 0" 0 "Local AI has been removed" un y -- --uninstall
t "  unit files are gone" 0 "" test -z "$(ls "$FR"/etc/systemd/system/localai-* 2>/dev/null)"
t "  /opt/localai (incl. installers/) is gone" 0 "" test ! -e "$FR/opt/localai"
t "  aictl link, sbin install-node.sh link, polkit rule are gone" 0 "" test ! -L "$FR/usr/local/bin/aictl" -a ! -L "$FR/usr/local/sbin/install-node.sh" -a ! -e "$FR/etc/polkit-1/rules.d/50-localai-operators.rules"
t "  /etc/localai, /var/lib/localai and /srv/localai survive" 0 "" test -f "$FR/etc/localai/api-key" -a -f "$FR/etc/localai/node.yaml" -a -d "$FR/var/lib/localai" -a -f "$FR/srv/localai/models/m.gguf"
t "  the phase record is dropped so a reinstall re-runs" 0 "" test ! -e "$FR/var/lib/localai/install/state.json"
t "  units disabled, as in the spec" 0 "" calls_has "systemctl disable --now localai-sync.timer"
t "  daemon-reload ran" 0 "" calls_has "systemctl daemon-reload"
t "  unenrolled (cloud credential present)" 0 "" calls_has "aictl cloud unenroll --yes"
t "  Tailscale Serve pattern B listener removed" 0 "" calls_has "tailscale serve --https=443 off"
t "  user and group kept without --purge-config" 0 "" calls_lacks '^(userdel|groupdel)'

mkroot
t "purge-config needs DELETE: a wrong word aborts (exit 4)" 4 "you did not type DELETE" un y delete -- --uninstall --purge-config
t "  ...and nothing was removed" 0 "" test -d "$FR/etc/localai" -a -e "$FR/opt/localai/bin/aiagent"
t "purge-config: --yes does not skip the DELETE prompt" 4 "you did not type DELETE" un y -- --uninstall --purge-config --yes
t "purge-config with DELETE removes config and state" 0 "config and state were deleted" un y DELETE -- --uninstall --purge-config
t "  /etc/localai and /var/lib/localai are gone, content stays" 0 "" test ! -e "$FR/etc/localai" -a ! -e "$FR/var/lib/localai" -a -f "$FR/srv/localai/models/m.gguf"
t "  userdel localai and groupdel localai-operators ran" 0 "" bash -c 'grep -q "^userdel localai" "$1" && grep -q "^groupdel localai-operators" "$1"' _ "$CALLS"

mkroot
t "purge-content asks for DELETE and shows the size" 0 "of models and content" un y DELETE -- --uninstall --purge-content
t "  /srv/localai is gone, /etc/localai stays" 0 "" test ! -e "$FR/srv/localai" -a -d "$FR/etc/localai"
mkroot
t "purge-content with a wrong word aborts (exit 4)" 4 "you did not type DELETE" un y no -- --uninstall --purge-content
t "  ...content intact" 0 "" test -f "$FR/srv/localai/models/m.gguf"
t "both purges: DELETE is asked twice and both trees go" 0 "config and state were deleted" un y DELETE DELETE -- --uninstall --purge-config --purge-content
t "  all three trees are gone" 0 "" test ! -e "$FR/etc/localai" -a ! -e "$FR/srv/localai" -a ! -e "$FR/var/lib/localai"

mkroot legacy
out="$(un y DELETE -- --uninstall --restore-legacy --purge-config 2>&1)"; rc=$?
t "restore-legacy + purge-config exits 0" 0 "" test $rc -eq 0
t "  llama-qwen was started" 0 "" calls_has "systemctl enable --now llama-qwen.service"
t "  at that moment the key was already a real file" 0 "" calls_has "STATE key-real-file"
t "  at that moment /etc/localai and /opt/localai still existed (started before steps 6-7)" 0 "" bash -c 'grep -q "STATE etc-localai-present" "$1" && grep -q "STATE opt-localai-present" "$1"' _ "$CALLS"
t "  at that moment the unit was already restored" 0 "" calls_has "STATE unit-restored"
t "  the unit content came from the migration backup" 0 "" grep -q "ExecStart=/bin/true" "$FR/etc/systemd/system/llama-qwen.service"
t "  the mask link was replaced by a real file" 0 "" test ! -L "$FR/etc/systemd/system/llama-qwen.service"
t "  /etc/llama-qwen/api-key is a real 0600 file with the key, after /etc/localai is gone" 0 "" bash -c '[ ! -L "$1" ] && [ -f "$1" ] && [ "$(stat -c %a "$1")" = 600 ] && grep -q KEYKEYKEY "$1" && [ ! -e "$2" ]' _ "$FR/etc/llama-qwen/api-key" "$FR/etc/localai"
t "  unmask ran before enable" 0 "" bash -c 'u=$(grep -n "unmask llama-qwen" "$1" | head -1 | cut -d: -f1); e=$(grep -n "enable --now llama-qwen" "$1" | head -1 | cut -d: -f1); [ -n "$u" ] && [ "$u" -lt "$e" ]' _ "$CALLS"
t "  Tailscale Serve is left alone" 0 "" calls_lacks '^tailscale'
t "  the summary says it was restored" 0 "" grep -q "old llama-qwen service was restored" <<<"$out"
mkroot legacy
: >"$w/answers"; printf 'y\nDELETE\n' >"$w/answers"
t "restore-legacy start failure exits 1 with LAI-INS-080" 1 "LAI-INS-080" env FAKE_SYSTEMCTL_FAIL="enable --now llama-qwen" "$(command -v env)" PATH="$FB:$PATH" LAI_INSTALLER_DEV=1 LAI_INSTALL_ROOT="$FR" LAI_INSTALL_ANSWERS="$w/answers" LAI_INSTALL_STATE_DIR="$FR/var/lib/localai/install" LAI_INSTALL_SYSTEMD_DIR="$FR/etc/systemd/system" "$STAMPED" --log "$w/un.log" --uninstall --restore-legacy --purge-config
t "  ...nothing under /etc/localai or /opt/localai was removed" 0 "" test -f "$FR/etc/localai/api-key" -a -e "$FR/opt/localai/bin/aiagent"
mkroot
t "restore-legacy without a saved copy warns and goes on" 0 "no saved llama-qwen.service" un y -- --uninstall --restore-legacy
t "  ...no llama-qwen start was attempted" 0 "" calls_lacks 'llama-qwen'
mkroot legacy; rm -f "$FR/etc/localai/api-key"
t "restore-legacy with the key missing: LAI-INS-080 before any change" 1 "LAI-INS-080" un y -- --uninstall --restore-legacy
t "  ...everything still installed" 0 "" test -e "$FR/opt/localai/bin/aiagent"
t "restore-legacy with purge-content conflicts (exit 2)" 2 "conflict" un y -- --uninstall --restore-legacy --purge-content
mkroot
sed -i "s/serve_pattern: B/serve_pattern: A/" "$FR/etc/localai/node.yaml"
t "tailscale pattern A removes the tcp listener" 0 "" un y -- --uninstall
t "  ...the --tcp=18080 one" 0 "" calls_has "tailscale serve --tcp=18080 off"
mkroot
t "revert-hardening without the agent program warns and carries on" 0 "Cannot revert the laptop hardening" un y -- --uninstall --revert-hardening
t "revert-hardening is called when INST-03 defines it" 0 "HARDENING-REVERTED" bash -c 'source "$1"; revert_hardening() { echo HARDENING-REVERTED; }; declare -F revert_hardening >/dev/null && revert_hardening' _ "$INST"

echo "== uninstall safety guards"
sg() { bash -c 'source "$1"; LOG_OPEN=; DRY_RUN=0; shift; safe_rm_rf "$@"' _ "$INST" "$@"; }
mkdir -p "$w/g/opt/localai" "$w/g/home/x"
t "guard: an empty path is refused (LAI-INS-081)" 1 "LAI-INS-081" sg "$w/g" ""
t "guard: / is refused" 1 "LAI-INS-081" sg "$w/g" "/"
t "guard: /home is refused" 1 "LAI-INS-081" sg "$w/g" "/home/x"
t "guard: a relative path is refused" 1 "LAI-INS-081" sg "$w/g" "opt/localai"
t "guard: /opt/localai/../x is refused" 1 "LAI-INS-081" sg "$w/g" "/opt/localai/../x"
t "guard: nothing was deleted by the refusals" 0 "" test -d "$w/g/opt/localai" -a -d "$w/g/home/x"
t "guard: an allowed tree is removed" 0 "" bash -c 'source "$1"; LOG_OPEN=; DRY_RUN=0; safe_rm_rf "$2" /opt/localai && test ! -e "$2/opt/localai"' _ "$INST" "$w/g"

echo "== uninstall dry run"
mkroot legacy
before="$(find "$FR" -printf '%p %m %l\n' | sort | md5sum)"
dryun() { unenv "$STAMPED" --dry-run --log "$w/un.log" "$@"; }
out="$(dryun --uninstall --restore-legacy --purge-config --revert-hardening 2>&1)"
t "dry-run uninstall says what it would remove" 0 "" grep -qF -- "[dry-run] would run: rm -rf -- $FR/opt/localai" <<<"$out"
t "  would ask for DELETE but does not" 0 "" grep -q "would ask you to type DELETE" <<<"$out"
t "  the fake root is identical afterwards" 0 "" test "$before" = "$(find "$FR" -printf '%p %m %l\n' | sort | md5sum)"
t "  no state-changing command really ran (only reads)" 0 "" calls_lacks 'disable|enable|unmask|daemon-reload|userdel|groupdel|tailscale|unenroll'

echo "== repair"
mkroot
printf 'OLD MODIFIED\n' >"$FR/opt/localai/installers/install-node.sh"; chmod 777 "$FR/opt/localai/installers/install-node.sh"
rm -f "$FR/usr/local/sbin/install-node.sh"
secret_before="$(cat "$FR"/etc/localai/* | sha256sum)"
out="$(un -- --repair 2>&1)"; rc=$?
t "repair exits 0 (the fake aictl doctor is clean)" 0 "" test $rc -eq 0
t "  the modified installed install-node.sh is replaced by this copy, mode 0755" 0 "" bash -c 'cmp -s "$1" "$2" && [ "$(stat -c %a "$2")" = 755 ]' _ "$STAMPED" "$FR/opt/localai/installers/install-node.sh"
t "  the old copy was backed up first" 0 "" test -n "$(find "$FR/var/lib/localai/install/backup" -name install-node.sh 2>/dev/null)"
t "  the deleted /usr/local/sbin symlink is back" 0 "" test "$(readlink "$FR/usr/local/sbin/install-node.sh")" = /opt/localai/installers/install-node.sh
t "  aictl doctor ran at the end" 0 "" calls_has "aictl doctor"
t "  identity, secrets and content are unchanged" 0 "" test "$secret_before" = "$(cat "$FR"/etc/localai/* | sha256sum)" -a -f "$FR/srv/localai/models/m.gguf"
t "  phases ran in repair mode" 0 "REPAIR_MODE=1" bash -c 'source "$1"; REPAIR_MODE=1; echo REPAIR_MODE=$REPAIR_MODE' _ "$INST"
snap() { find "$FR" -not -path '*/backup*' -not -name state.json -not -name '.state.*' -printf '%p %m %l\n' | sort | md5sum; }
before="$(snap)"
un -- --repair >/dev/null 2>&1
t "repair is idempotent (second run changes no file, mode or link)" 0 "" test "$before" = "$(snap)"
t "  second run says the installer copy is already up to date" 0 "already up to date" un -- --repair
t "repair does not trust 'done' in state.json: all 15 phases run again" 0 "==> [15/15]" un -- --repair
mkdir -p "$w/fb2"; printf '#!/bin/sh\nexit 3\n' >"$w/fb2/aictl"; chmod +x "$w/fb2/aictl"
failing_doctor() { UN_PATH="$w/fb2" unenv "$STAMPED" --log "$w/un.log" --repair; }
t "repair: a failing aictl doctor makes repair exit 1" 1 "still reports problems" failing_doctor
mkroot
printf 'OLD\n' >"$FR/opt/localai/installers/install-node.sh"; b="$(md5sum <"$FR/opt/localai/installers/install-node.sh")"
t "repair --dry-run shows the diff" 0 "[dry-run] would change" dryun --repair
t "  ...and changed nothing" 0 "" test "$b" = "$(md5sum <"$FR/opt/localai/installers/install-node.sh")"
cp "$STAMPED" "$FR/opt/localai/installers/install-node.sh"
t "repair run from the installed copy itself says it cannot judge it" 0 "uses the installed copy itself" unenv env -u LAI_INSTALL_SELF "$FR/opt/localai/installers/install-node.sh" --log "$w/un.log" --repair
i2_mkark "$w/ark"; printf 'KITCOPY\n' >"$w/ark/localai/kit/installers/install-node.sh"   # a full drive whose SHA256SUMS does not list the installer
(cd "$w/ark/localai/kit/bin" && grep -v ' install-node.sh$' SHA256SUMS >s && mv s SHA256SUMS && printf 'SIG:%s:%s' "$I2_PUBKEY" "$(sha256sum SHA256SUMS | cut -d' ' -f1)" >SHA256SUMS.minisig)
t "repair from the installed copy takes the kit copy with --from-ark" 0 "" unenv env -u LAI_INSTALL_SELF "$FR/opt/localai/installers/install-node.sh" --log "$w/un.log" --from-ark "$w/ark" --repair
t "  ...and installed it" 0 "" grep -q KITCOPY "$FR/opt/localai/installers/install-node.sh"

echo "== INST-07: P11 tailscale, P14 content, P15 verify and summary (fakes only)"
# A private PATH: symlinks to the few real tools the phases use plus fakes that only log. No real tailscale,
# systemctl, apt, curl or aictl can ever run.
tools="$w/tools"; fk="$w/fk"; mkdir -p "$tools" "$fk"
for c in bash sed grep cat cmp mktemp date dirname basename tr tail head cut id stat mv chmod chown cp mkdir rm diff env sleep wc awk install ls true false; do
  p="$(command -v "$c" 2>/dev/null)" && [[ -x "$p" ]] && ln -sf "$p" "$tools/$c"
done
mkfake() { printf '#!/bin/bash\n%s\n' "$2" >"$fk/$1"; chmod +x "$fk/$1"; }
mkfake systemctl 'echo "systemctl $*" >>"$FAKELOG"; exit 0'
mkfake curl 'echo "curl $*" >>"$FAKELOG"; out=""; while [ $# -gt 0 ]; do [ "$1" = --output ] && out="$2"; shift; done; [ -n "$out" ] && echo fake >"$out"; exit ${FAKE_CURL_RC:-0}'
mkfake apt-get 'echo "apt-get $*" >>"$FAKELOG"; exit 0'
mkfake dpkg-query 'exit 1'
mkfake dpkg 'if [ "$1" = --print-architecture ]; then echo amd64; exit 0; fi; echo "dpkg $*" >>"$FAKELOG"; exit ${FAKE_DPKG_RC:-0}'
mkfake aictl 'echo "aictl $*" >>"$FAKELOG"; if [ "$1 $2 $3" = "ark sync --dry-run" ]; then echo "Plan: 12 items"; echo "Total to download: 186 GB"; exit ${FAKE_PLAN_RC:-0}; fi; exit ${FAKE_AICTL_RC:-0}'
mkfake aiagent 'echo "aiagent $*" >>"$FAKELOG"
case "$2" in
  serve-check) if [ "${FAKE_SERVE_MISMATCH:-0}" = 1 ]; then echo "ERROR LAI-INS-050: serve pattern A but inventory says B" >&2; exit 1; fi; echo "${FAKE_PATTERN:-B}" ;;
  verify) echo "  ok       agent: fake"; exit ${FAKE_VERIFY_RC:-0} ;;
  summary) echo "FAKE SUMMARY $*" ;;
esac'
printf '#!/bin/bash\necho "tailscale $*" >>"$FAKELOG"\nif [ "$1 $2" = "status --json" ]; then echo "{\\"BackendState\\": \\"${FAKE_TS_STATE:-NeedsLogin}\\"}"; fi\nexit 0\n' >"$fk/tailscale"; chmod +x "$fk/tailscale"
cp "$fk/tailscale" "$w/tailscale.real"; rm -f "$fk/tailscale"       # tailscale is installed by the phase in these tests
osrel="$w/os-release"; printf 'VERSION_CODENAME=noble\n' >"$osrel"
FAKELOG="$w/fake.log"; export FAKELOG
# ph <script> [VAR=value ...]: run a snippet with the installer sourced, fakes on PATH and state in $w/pstate.
ph() {
  local script="$1"; shift
  : >"$FAKELOG"
  env -i PATH="$tools:$fk" HOME="$w" TMPDIR="$TMPDIR" FAKELOG="$FAKELOG" LAI_INSTALLER_DEV=1 LAI_INSTALLER_AIAGENT="$fk/aiagent" LAI_INSTALLER_AICTL="$fk/aictl" LAI_INSTALLER_TAILSCALE="$fk/tailscale" \
    LAI_INSTALL_NODE_YAML="$w/node.yaml" LAI_INSTALL_TS_KEYRING="$w/ts.gpg" LAI_INSTALL_TS_LIST="$w/ts.list" LAI_INSTALL_OS_RELEASE="$osrel" \
    LAI_INSTALL_INVENTORY_DEST="$w/kept-inventory.yaml" LAI_INSTALLER_COUNTDOWN=0 LAI_INSTALLER_LAPTOP=1 w="$w" fk="$fk" "$@" \
    bash -c 'source "$1"; set -Eeuo pipefail; STATE_DIR="$2/pstate"; STATE_FILE="$STATE_DIR/state.json"; CUR_PHASE_ID=P11; eval "$3"' _ "$INST" "$w" "$script"
}
nots() { ! grep -qE 'tailscale (serve|funnel)' "$FAKELOG"; }

# P11
t "P11 --no-tailscale is skipped, nothing runs" 0 "skipped: --no-tailscale" ph 'OPT_NO_TAILSCALE=1; phase_tailscale'
t "P11 skipped ran no command" 0 "" test ! -s "$FAKELOG"
t "P11 online: installs from the signed apt repo, enables tailscaled" 0 "This computer is not on the tailnet yet" ph 'OPT_NO_TAILSCALE=0; rm -f "$w/ts.gpg" "$w/ts.list"; phase_tailscale' w="$w"
t "P11 key URL uses the codename and noarmor.gpg" 0 "" grep -qF "https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg" "$FAKELOG"
t "P11 list URL uses the codename and tailscale-keyring.list" 0 "" grep -qF "https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list" "$FAKELOG"
t "P11 runs apt-get install -y tailscale" 0 "" grep -q 'apt-get install -y --no-install-recommends tailscale' "$FAKELOG"
t "P11 runs systemctl enable --now tailscaled" 0 "" grep -qF "systemctl enable --now tailscaled" "$FAKELOG"
t "P11 never pipes curl into a shell" 0 "" bash -c '! grep -qE "\| *(sh|bash)" "$1" && ! grep -qE "install.sh" "$1"' _ "$FAKELOG"
cp "$w/tailscale.real" "$fk/tailscale"
t "P11 already installed: no repo work" 0 "Tailscale is already installed" ph 'OPT_NO_TAILSCALE=0; phase_tailscale'
t "P11 logged-out tailscale: not joined" 0 "not on the tailnet yet" ph 'OPT_NO_TAILSCALE=0; phase_tailscale'
t "P11 running tailscale: joined" 0 "already on the tailnet" ph 'OPT_NO_TAILSCALE=0; phase_tailscale' FAKE_TS_STATE=Running
t "P11 pattern from the agent is recorded" 0 "serve pattern: B" ph 'OPT_NO_TAILSCALE=0; phase_tailscale'
ph 'OPT_NO_TAILSCALE=0; OPT_NODE_ID=ai-n4; OPT_SERVE_PATTERN=A; OPT_INVENTORY="$INST_PATH"; phase_tailscale' INST_PATH="$INST" >/dev/null 2>&1
t "P11 serve-check got the node id, pattern and inventory" 0 "" bash -c 'grep -F "serve-check" "$1" | grep -qF -- "--node-id ai-n4" && grep -F serve-check "$1" | grep -qF -- "--serve-pattern A" && grep -F serve-check "$1" | grep -qF -- "--inventory $2"' _ "$FAKELOG" "$INST"
mkdir -p "$w/pstate"
t "P11 mismatch with the inventory: exit 1 with LAI-INS-050" 1 "LAI-INS-050" ph 'OPT_NO_TAILSCALE=0; phase_tailscale' FAKE_SERVE_MISMATCH=1
t "P11 mismatch marks the phase failed in state.json" 0 "" grep -q '"P11": {"name":"tailscale","status":"failed"' "$w/pstate/state.json"
ph 'OPT_NO_TAILSCALE=0; phase_tailscale' FAKE_TS_STATE=Running >/dev/null 2>&1
t "P11 never ran tailscale serve or funnel" 0 "" nots
t "P11 dry run: shows the commands, runs none of them" 0 "[dry-run] would run: systemctl enable --now tailscaled" ph 'OPT_NO_TAILSCALE=0; DRY_RUN=1; rm -f "$w/ts.gpg" "$w/ts.list"; rm -f "$fk/tailscale"; phase_tailscale' w="$w" fk="$fk"
t "P11 dry run changed nothing (no curl, no systemctl, no apt, no dpkg)" 0 "" bash -c '! grep -qE "^(curl|systemctl|apt-get install|apt-get update|dpkg -i)" "$1"' _ "$FAKELOG"
cp "$w/tailscale.real" "$fk/tailscale"
mkdir -p "$w/ark1/localai/kit/debs/noble-amd64"
t "P11 offline without the kit deb: LAI-INS-030, exit 1" 1 "LAI-INS-030" ph 'OPT_NO_TAILSCALE=0; OPT_FROM_ARK="$A"; rm -f "$fk/tailscale"; phase_tailscale' A="$w/ark1" fk="$fk"
cp "$w/tailscale.real" "$fk/tailscale"
: >"$w/ark1/localai/kit/debs/noble-amd64/tailscale_1.0_amd64.deb"
t "P11 offline installs the kit deb with dpkg" 0 "Installing Tailscale from the ark drive" ph 'OPT_NO_TAILSCALE=0; OPT_FROM_ARK="$A"; rm -f "$fk/tailscale"; phase_tailscale' A="$w/ark1" fk="$fk"
t "P11 offline used dpkg -i and no curl" 0 "" bash -c 'grep -q "^dpkg -i .*tailscale_1.0_amd64.deb" "$1" && ! grep -q "^curl" "$1"' _ "$FAKELOG"
cp "$w/tailscale.real" "$fk/tailscale"
t "P11 check hook: installed and active -> already satisfied" 0 "" ph 'OPT_NO_TAILSCALE=0; check_tailscale'
t "P11 check hook: not installed -> not satisfied" 0 "" ph 'OPT_NO_TAILSCALE=0; rm -f "$fk/tailscale"; ! check_tailscale'
cp "$w/tailscale.real" "$fk/tailscale"

# P14
t "P14 --no-sync is skipped" 0 "skipped: --no-sync" ph 'OPT_NO_SYNC=1; phase_content'
t "P14 skipped started nothing" 0 "" test ! -s "$FAKELOG"
t "P14 online: summary of the plan, then the sync" 0 "Will download 186 GB over the next hours" ph 'OPT_NO_SYNC=0; phase_content'
t "P14 online ran the dry-run plan first and then the sync" 0 "" bash -c '[ "$(grep -n "^aictl ark sync" "$1" | head -1 | cut -d: -f2-)" = "aictl ark sync --dry-run" ] && grep -qx "aictl ark sync" "$1"' _ "$FAKELOG"
t "P14 prints how to check progress" 0 "Check progress with: aictl ark sync (status) or the desktop app." ph 'OPT_NO_SYNC=0; phase_content'
t "P14 content line carries the size" 0 "sync running (186 GB planned)   check: aictl ark sync" ph 'OPT_NO_SYNC=0; phase_content; say "LINE=$CONTENT_LINE"; echo "$CONTENT_LINE"'
t "P14 a plan that fails does not fail the install" 0 "The download could not be planned now" ph 'OPT_NO_SYNC=0; phase_content' FAKE_PLAN_RC=1
t "P14 a sync that cannot start does not fail the install" 0 "The sync could not be started now" ph 'OPT_NO_SYNC=0; phase_content' FAKE_AICTL_RC=1
t "P14 dry run plans but does not start the sync" 0 "[dry-run] would run: $fk/aictl ark sync" ph 'OPT_NO_SYNC=0; DRY_RUN=1; phase_content'
t "P14 dry run never ran the real sync" 0 "" bash -c '! grep -qx "aictl ark sync" "$1"' _ "$FAKELOG"
mkdir -p "$w/ark2/localai"; printf '{"schema_version":1,"root_id":"ark-test0001","kind":"ark_drive"}\n' >"$w/ark2/localai/ROOT.json"
t "P14 offline imports from the drive root id" 0 "Importing the content from the ark drive" ph 'OPT_NO_SYNC=0; OPT_FROM_ARK="$A"; phase_content' A="$w/ark2"
t "P14 offline ran aictl ark import --from <id> --catalog" 0 "" grep -qx "aictl ark import --from ark-test0001 --catalog" "$FAKELOG"
t "P14 offline never calls ark sync" 0 "" bash -c '! grep -q "ark sync" "$1"' _ "$FAKELOG"
t "P14 offline without ROOT.json explains and goes on" 0 "no readable ROOT.json" ph 'OPT_NO_SYNC=0; OPT_FROM_ARK="$A"; phase_content' A="$w/ark1"

# P15
printf 'schema_version: 1\n' >"$w/inv.yaml"
t "P15 --keep-inventory copies the inventory with mode 0600" 0 "" ph 'OPT_KEEP_INVENTORY=1; OPT_INVENTORY="$I"; STATE_DIR="$w/pstate"; i07_keep_inventory; [ "$(stat -c %a "$w/kept-inventory.yaml")" = 600 ] && cmp "$I" "$w/kept-inventory.yaml"' I="$w/inv.yaml" w="$w"
t "P15 --keep-inventory again says already up to date" 0 "already up to date" ph 'OPT_KEEP_INVENTORY=1; OPT_INVENTORY="$I"; i07_keep_inventory' I="$w/inv.yaml"
t "P15 --keep-inventory dry run copies nothing" 0 "[dry-run] would run: install" ph 'OPT_KEEP_INVENTORY=1; OPT_INVENTORY="$I"; DRY_RUN=1; rm -f "$w/kept-inventory.yaml"; i07_keep_inventory; [ ! -e "$w/kept-inventory.yaml" ]' I="$w/inv.yaml" w="$w"
t "P15 runs verify, then prints the summary" 0 "FAKE SUMMARY setup summary" ph 'OPT_NODE_ID=ai-n4; OPT_HARDENING=auto; phase_verify'
t "P15 summary gets laptop, user, skip and content flags" 0 "" bash -c 'grep "summary" "$1" | grep -qF -- "--laptop" && grep "summary" "$1" | grep -qF -- "--node-id ai-n4"' _ "$FAKELOG"
t "P15 passes the desktop user and the cloud line" 0 "--user someone" ph 'OPT_NODE_ID=ai-n4; OPT_DESKTOP_USER=someone; OPT_ENROLL_FILE=x; CONTENT_LINE="sync running"; phase_verify'
t "P15 no-tailscale is passed to the summary" 0 "--skip-tailscale" ph 'OPT_NODE_ID=ai-n4; OPT_NO_TAILSCALE=1; phase_verify'
t "P15 a joined tailnet is passed to the summary" 0 "--tailnet-joined" ph 'OPT_NODE_ID=ai-n4; phase_verify' FAKE_TS_STATE=Running
t "P15 a failing verify stops the install (exit 1)" 1 "The install is NOT finished" ph 'OPT_NODE_ID=ai-n4; phase_verify || exit 1' FAKE_VERIFY_RC=1
t "P15 --reboot with a reboot needed counts down, then reboots" 0 "Rebooting in 0 seconds" ph 'OPT_NODE_ID=ai-n4; OPT_REBOOT=1; REBOOT_REQUIRED=1; phase_verify'
t "P15 reboot ran systemctl reboot" 0 "" grep -qx "systemctl reboot" "$FAKELOG"
t "P15 dry run never reboots" 0 "[dry-run] would reboot" ph 'OPT_NODE_ID=ai-n4; OPT_REBOOT=1; REBOOT_REQUIRED=1; DRY_RUN=1; phase_verify'
t "P15 dry run ran no reboot" 0 "" bash -c '! grep -qx "systemctl reboot" "$1"' _ "$FAKELOG"
t "P15 no --reboot means no reboot" 0 "" ph 'OPT_NODE_ID=ai-n4; OPT_REBOOT=0; REBOOT_REQUIRED=1; phase_verify'
t "P15 no reboot happened" 0 "" bash -c '! grep -qx "systemctl reboot" "$1"' _ "$FAKELOG"
t "P15 without the agent installed (dry run) says so" 0 "the agent is not installed yet" ph 'OPT_NODE_ID=ai-n4; DRY_RUN=1; rm -f "$fk/aiagent"; phase_verify' fk="$fk"
t "P15 without the agent installed (real run) is not implemented, never done" 10 "so the checks and the summary cannot run" ph 'OPT_NODE_ID=ai-n4; rm -f "$fk/aiagent"; phase_verify; exit $?' fk="$fk"
t "tailscale serve and funnel were never invoked in any INST-07 test" 0 "" nots
echo "== INST-05: P7 llama.cpp build or reuse, P8 migration hand-off"
# llrun <shell code>: source the installer with the fakes and run the code (state in $w/lstate, units in $w/lsystemd)
llrun() { env "${llhooks[@]}" "${LLENV[@]}" bash -c 'source "$1"; STATE_DIR="$2"; SYSTEMD_DIR="$3"; STATE_FILE="$2/state.json"; shift 3; eval "$*"' _ "$INST" "$w/lstate" "$w/lsystemd" "$@"; }
# llenvrun <VAR=val...> -- <shell code>: same, with extra environment for the fakes
llenvrun() { local -a e=(); while [[ "$1" != -- ]]; do e+=("$1"); shift; done; shift; env "${llhooks[@]}" "${e[@]}" bash -c 'source "$1"; STATE_DIR="$2"; SYSTEMD_DIR="$3"; shift 3; eval "$*"' _ "$INST" "$w/lstate" "$w/lsystemd" "$@"; }
LLENV=(); : >"$FAKE_LOG"
printf '{\n  "schema_version": 1,\n  "llama_cpp": [\n    {"commit": "%s", "nodes": ["ai-n9"]},\n    {"commit": "%s", "nodes": ["ai-n1", "ai-n2"]}\n  ],\n  "other": [{"x": "%s"}]\n}\n' "$C40B" "$C40" "$C40B" >"$w/pins.json"
NONE="$w/none.yaml"
echo "-- commit resolution"
t "node.yaml llama.commit wins" 0 "$C40" llrun 'llama_resolve && echo $LLAMA_COMMIT'
t "pins.json: this node id picks its own entry" 0 "$C40B" llenvrun LAI_INSTALL_NODE_YAML="$NONE" -- 'OPT_NODE_ID=ai-n9; llama_resolve; echo $LLAMA_COMMIT'
t "pins.json: node id in the second entry" 0 "$C40 pins" llenvrun LAI_INSTALL_NODE_YAML="$NONE" -- 'OPT_NODE_ID=ai-n2; llama_resolve; echo "$LLAMA_COMMIT ${LLAMA_COMMIT_FROM:24:4}"'
t "pins.json: unknown node falls back to the first entry" 0 "$C40B" llenvrun LAI_INSTALL_NODE_YAML="$NONE" -- 'OPT_NODE_ID=ai-n7; llama_resolve; echo $LLAMA_COMMIT'
t "no commit anywhere -> not resolved" 0 "unresolved" llenvrun LAI_INSTALL_NODE_YAML="$NONE" LAI_INSTALL_PINS="$w/nope.json" -- 'llama_resolve || echo unresolved'
mkdir -p "$w/ark/localai/kit/catalog" "$w/ark/localai/kit/src"
printf '{"llama_cpp": [{"commit": "%s", "nodes": ["ai-n4"]}]}' "$C40B" >"$w/ark/localai/kit/catalog/pins.json"
t "offline: the kit's pins.json is read before the installed catalog" 0 "$C40B" llenvrun LAI_INSTALL_NODE_YAML="$NONE" -- "OPT_FROM_ARK='$w/ark'; OPT_NODE_ID=ai-n4; llama_resolve; echo \$LLAMA_COMMIT"
t "backend: --backend wins" 0 "cuda" llrun 'OPT_BACKEND=cuda; llama_resolve; echo $LLAMA_BACKEND'
t "backend: defaults to cpu" 0 "cpu" llrun 'llama_resolve; echo $LLAMA_BACKEND'
echo "-- reuse"
b9="$LL/builds/${C40:0:9}-cpu"; rm -rf "$LL/builds/"* "$LL/src/"* "$LL/current"
t "no commit, real run -> exit 1 LAI-INS-032" 1 "LAI-INS-032" llenvrun LAI_INSTALL_NODE_YAML="$NONE" LAI_INSTALL_PINS="$w/nope.json" -- 'phase_llama'
t "first run builds with the shared builder" 0 "build finished" llrun 'OPT_YES=1; phase_llama'
t "aictl got commit, backend, the one source folder, --no-activate" 0 "" grep -qF "aictl rebuild-llama --commit $C40 --backend cpu --source $LL/src/$C40 --no-activate" "$FAKE_LOG"
t "source is in the one source location, tree is 0755" 0 "" bash -c '[ -f "$1/CMakeLists.txt" ] && [ "$(stat -c %a "$1")" = 755 ]' _ "$LL/src/$C40"
t "nothing refers to /var/lib/localai/build" 0 "" bash -c '! grep -q "/var/lib/localai/build" "$1"' _ "$FAKE_LOG"
t "current was not touched" 0 "" test ! -e "$LL/current"
: >"$FAKE_LOG"
t "a good existing build is reused: already built" 0 "already built and working" llrun 'phase_llama'
t "reuse did not call the builder" 0 "" test ! -s "$FAKE_LOG"
t "check_llama passes for a good build" 0 "" llrun 'check_llama'
printf '#!/bin/sh\necho "version: 1 (deadbee)"\n' >"$b9/bin/llama-server"
t "check_llama fails when the smoke test fails" 1 "" llrun 'check_llama'
t "a build whose smoke test fails is rebuilt" 0 "it will be rebuilt" llrun 'OPT_YES=1; phase_llama'
echo "-- source order"
rm -rf "$LL/builds/"* "$LL/src/"*
repo="$w/homes/lab/llama.cpp"; mkdir -p "$repo"
g() { git -C "$repo" -c user.name=t -c user.email=t@example.test "$@"; }
g init -q; echo 'cmake_minimum_required(VERSION 3.14)' >"$repo/CMakeLists.txt"; g add CMakeLists.txt; g commit -q -m x; HC="$(g rev-parse HEAD)"
printf 'node_id: ai-n4\nllama:\n  commit: %s\n' "$HC" >"$w/node-home.yaml"
LLENV=(LAI_INSTALL_NODE_YAML="$w/node-home.yaml")
t "1. ~/llama.cpp with the commit is the source (log says so)" 0 "your existing checkout" llrun "OPT_DESKTOP_USER=lab; OPT_YES=1; OPT_FROM_ARK='$w/ark'; phase_llama"
t "   exported to the one source location; the checkout is untouched" 0 "" bash -c '[ -f "$1/src/$2/CMakeLists.txt" ] && [ -d "$3/.git" ] && [ ! -e "$3/build-cuda" ]' _ "$LL" "$HC" "$repo"
rm -rf "$LL/builds/"* "$LL/src/"*
printf 'node_id: ai-n4\nllama:\n  commit: %s\n' "$C40" >"$w/node-home.yaml"
cp "$w/fx.tar.gz" "$w/ark/localai/kit/src/llama.cpp-$C40.tar.gz"; : >"$FAKE_LOG"
t "2. commit not in ~/llama.cpp: the kit tarball, offline" 0 "kit tarball" llrun "OPT_DESKTOP_USER=lab; OPT_YES=1; OPT_FROM_ARK='$w/ark'; phase_llama"
t "   top folder dropped, source in the one location" 0 "" test -f "$LL/src/$C40/CMakeLists.txt"
t "   offline never calls curl" 0 "" bash -c '! grep -q "^curl" "$1"' _ "$FAKE_LOG"
rm -rf "$LL/builds/"* "$LL/src/"*
t "3. online with no checkout and no kit: GitHub tarball, sha256 recorded" 0 "sha256 llama.cpp-$C40.tar.gz" llrun 'OPT_YES=1; phase_llama'
t "   the URL is the pinned commit's archive" 0 "" grep -qF "https://github.com/ggml-org/llama.cpp/archive/$C40.tar.gz" "$FAKE_LOG"
rm -rf "$LL/builds/"* "$LL/src/"*; rm -f "$w/ark/localai/kit/src/"*; : >"$FAKE_LOG"
t "offline with no tarball -> exit 1 LAI-INS-032 and a fix" 1 "re-sync the drive" llrun "OPT_FROM_ARK='$w/ark'; phase_llama"
t "   and no download was tried" 0 "" test ! -s "$FAKE_LOG"
echo "-- build failure and dry run"
rm -rf "$LL/builds/"* "$LL/src/"*
t "build failure -> exit 1, LAI-INS-032" 1 "LAI-INS-032" llenvrun FAKE_AICTL_FAIL=1 LAI_INSTALL_NODE_YAML="$w/node-home.yaml" -- 'OPT_YES=1; phase_llama'
t "   the message names build.log" 1 "build.log" llenvrun FAKE_AICTL_FAIL=1 LAI_INSTALL_NODE_YAML="$w/node-home.yaml" -- 'OPT_YES=1; phase_llama'
t "   current untouched after a failed build" 0 "" test ! -e "$LL/current"
rm -rf "$LL/builds/"* "$LL/src/"*; : >"$FAKE_LOG"; before="$(find "$LL" | sort | md5sum)"
t "dry run says it would build with the shared builder" 0 "[dry-run] would run: $w/llfake/aictl rebuild-llama" llrun 'DRY_RUN=1; phase_llama'
t "dry run created nothing and called nothing" 0 "" bash -c '[ "$(find "$1" | sort | md5sum)" = "$2" ] && [ ! -s "$3" ]' _ "$LL" "$before" "$FAKE_LOG"
LLENV=()
echo "-- P8 migration"
llq="$w/lsystemd/llama-qwen.service"; rm -f "$llq"; : >"$FAKE_LOG"
t "no llama-qwen.service: nothing to migrate" 0 "nothing to migrate" llrun 'phase_migration'
t "   check_migration passes (already done)" 0 "" llrun 'check_migration'
echo '[Service]' >"$llq"
t "unit present: check_migration says not done" 1 "" llrun 'check_migration'
t "unit present: runs 'aiagent setup migrate' and shows its output" 0 "migrating..." llrun 'OPT_YES=1; phase_migration'
t "   called exactly as 'setup migrate'" 0 "" grep -qx "aiagent setup migrate" "$FAKE_LOG"
t "rolled back (exit 20) -> exit 1 LAI-INS-033" 1 "LAI-INS-033" llenvrun FAKE_AIAGENT_RC=20 -- 'OPT_YES=1; phase_migration'
t "   message says the old service is running again" 1 "running again" llenvrun FAKE_AIAGENT_RC=20 -- 'OPT_YES=1; phase_migration'
t "other failure (exit 1) -> LAI-INS-033" 1 "LAI-INS-033" llenvrun FAKE_AIAGENT_RC=1 -- 'OPT_YES=1; phase_migration'
t "not root (exit 77) -> exit 3, LAI-INS-003" 3 "LAI-INS-003" llenvrun FAKE_AIAGENT_RC=77 -- 'OPT_YES=1; phase_migration'
: >"$FAKE_LOG"
t "no confirmation -> exit 4 and nothing run" 4 "Not confirmed" llenvrun A=1 -- 'phase_migration </dev/null'
t "   the migration was not started" 0 "" test ! -s "$FAKE_LOG"
t "--no-migrate with llama-qwen active -> exit 1, LAI-INS-031" 1 "LAI-INS-031" llrun 'OPT_MIGRATE=no; phase_migration'
t "--no-migrate, llama-qwen stopped and port free -> skipped" 0 "migration skipped" llenvrun FAKE_QWEN_INACTIVE=3 -- 'OPT_MIGRATE=no; phase_migration'
t "--no-migrate, stopped but something listens on 8080 -> LAI-INS-031" 1 "LAI-INS-031" llenvrun FAKE_QWEN_INACTIVE=3 FAKE_SS=1 -- 'OPT_MIGRATE=no; phase_migration'
t "dry run shows what it would run" 0 "[dry-run] would run: $w/llfake/aiagent setup migrate" llrun 'DRY_RUN=1; phase_migration'
t "   and called nothing" 0 "" test ! -s "$FAKE_LOG"
rm -f "$llq"; ln -s /dev/null "$llq"
t "a masked llama-qwen (already migrated) is not migrated again" 0 "nothing to migrate" llrun 'phase_migration'
t "--migrate forces the step even when the unit is gone" 0 "migration finished" llrun 'OPT_MIGRATE=yes; OPT_YES=1; phase_migration'
rm -f "$llq"

echo "== P4 laptop hardening (INST-03; the real work is tested in Go with fakes)"
# A fake agent: logs its arguments, writes hardening.json like the real one, exits as told.
fakeagent="$w/fake-aiagent"
cat >"$fakeagent" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_ARGS_LOG"
case " $* " in
  *" --check "*) exit "${FAKE_CHECK_RC:-0}" ;;
esac
echo "step 1: ignore lid, power key and idle (logind drop-in): done"
if [[ "${FAKE_REBOOT:-0}" == 1 && " $* " != *" --dry-run "* && " $* " != *" --revert "* ]]; then
  sd=""; while (($#)); do [[ "$1" == --state-dir ]] && sd="$2"; shift; done
  printf '{\n  "reboot_required": true\n}\n' >"$sd/hardening.json"
fi
exit "${FAKE_RC:-0}"
FAKE
chmod +x "$fakeagent"
export FAKE_ARGS_LOG="$w/agent.args"
# shellcheck disable=SC2120  # it is passed through t(), which shellcheck cannot see
hphase() { # hphase <args...>: run only phase P4 of the installer with the fake agent
  bash -c 'source "$1"; shift; DEV=1; LAI_INSTALL_HARDEN_AGENT="$1"; shift; STATE_DIR="$1"; shift; STATE_FILE="$STATE_DIR/state.json"; LOG_OPEN=; mkdir -p "$STATE_DIR"
    parse_args "$@"; CUR_PHASE_ID=""; run_phase 4 "${PHASE_TABLE[3]}"; echo "REBOOT_REQUIRED=$REBOOT_REQUIRED"' _ "$INST" "$fakeagent" "$w/hstate" "$@"
}
: >"$FAKE_ARGS_LOG"
t "--no-hardening skips and never calls the agent" 0 "Laptop hardening is off" hphase --no-hardening
t "  ... the agent was not called" 0 "" test ! -s "$FAKE_ARGS_LOG"
t "dry run without the agent shows the command" 10 "[dry-run] would run: /nonexistent/aiagent setup harden --state-dir" bash -c '
  source "$1"; DEV=1; DRY_RUN=1; LAI_INSTALL_HARDEN_AGENT=/nonexistent/aiagent; STATE_DIR="$2"; STATE_FILE="$2/s.json"; LOG_OPEN=; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""
  phase_hardening' _ "$INST" "$w/hstate"
t "a real run without the agent is loud, not done" 10 "NOT IMPLEMENTED YET" bash -c '
  source "$1"; DEV=1; DRY_RUN=0; LAI_INSTALL_HARDEN_AGENT=/nonexistent/aiagent; STATE_DIR="$2"; LOG_OPEN=; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""
  phase_hardening' _ "$INST" "$w/hstate"
rm -rf "$w/hstate"; : >"$FAKE_ARGS_LOG"
t "auto mode calls setup harden with the state dir" 0 "phase P4 laptop hardening: done" hphase
t "  ... arguments" 0 "setup harden --state-dir $w/hstate" grep -F -- "setup harden --state-dir $w/hstate" "$FAKE_ARGS_LOG"
t "  ... auto mode does not force" 0 "" bash -c '! grep -q -- "--hardening" "$1"' _ "$FAKE_ARGS_LOG"
: >"$FAKE_ARGS_LOG"
t "--hardening --no-ssh --desktop-user are passed on" 0 "done" hphase --hardening --no-ssh --desktop-user root
t "  ... flags" 0 "--hardening --no-ssh --desktop-user root" grep -F -- "--hardening --no-ssh --desktop-user root" "$FAKE_ARGS_LOG"
rm -rf "$w/hstate"
out="$(FAKE_REBOOT=1 hphase 2>&1)"
t "  ... in the phase details" 0 "reboot_required=true" grep -F "reboot_required=true" "$w/hstate/state.json"
t "  ... and the output says reboot, not restart logind" 0 "logind settings apply after a reboot" grep -F "logind settings apply after a reboot" <<<"$out"
rm -rf "$w/hstate"; : >"$FAKE_ARGS_LOG"
t "dry run passes --dry-run to the agent" 0 "--dry-run" bash -c '
  source "$1"; DEV=1; DRY_RUN=1; LAI_INSTALL_HARDEN_AGENT="$2"; STATE_DIR="$3"; LOG_OPEN=; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""
  phase_hardening; cat "$FAKE_ARGS_LOG"' _ "$INST" "$fakeagent" "$w/hstate"
t "agent failure fails the phase with the repair hint" 1 "sudo install-node.sh --repair" env FAKE_RC=1 bash -c '
  source "$1"; set -Eeuo pipefail; DEV=1; DRY_RUN=0; LAI_INSTALL_HARDEN_AGENT="$2"; STATE_DIR="$3"; STATE_FILE="$3/s.json"; LOG_OPEN=; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""
  run_phase 4 "${PHASE_TABLE[3]}"' _ "$INST" "$fakeagent" "$w/hstate"
t "check_hardening: passes when the agent verifies" 0 "" env FAKE_CHECK_RC=0 bash -c 'source "$1"; DEV=1; LAI_INSTALL_HARDEN_AGENT="$2"; STATE_DIR="$3"; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""; check_hardening' _ "$INST" "$fakeagent" "$w/hstate"
t "check_hardening: fails when the agent finds a problem" 1 "" env FAKE_CHECK_RC=1 bash -c 'source "$1"; DEV=1; LAI_INSTALL_HARDEN_AGENT="$2"; STATE_DIR="$3"; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""; check_hardening' _ "$INST" "$fakeagent" "$w/hstate"
: >"$FAKE_ARGS_LOG"
t "revert_hardening calls the agent with --revert" 0 "" bash -c 'source "$1"; DEV=1; DRY_RUN=0; LAI_INSTALL_HARDEN_AGENT="$2"; STATE_DIR="$3"; LOG_OPEN=; revert_hardening' _ "$INST" "$fakeagent" "$w/hstate"
t "  ... arguments" 0 "setup harden --revert" grep -F -- "setup harden --revert" "$FAKE_ARGS_LOG"
t "the installer never restarts logind (CI grep)" 0 "ok" "$SRC/scripts/check-no-logind-restart.sh"

echo "== release stamping"
if [[ $REAL_MS == 1 ]]; then
  printf '%s\n%s\n' pw-test pw-test | minisign -G -p "$w/a.pub" -s "$w/a.key" -f >/dev/null 2>&1
  stamped="$w/install-node.stamped.sh"
  t "stamp-installer accepts the template" 0 "stamped" "$SRC/scripts/release/stamp-installer.sh" --in "$INST" --out "$stamped" --tag node-v1.2.3 --pubkey "$w/a.pub"
  t "no @@ token is left" 0 "" bash -c '! grep -E "@@LAI_[A-Z_]+@@" "$1"' _ "$stamped"
  t "stamped --version" 0 "install-node.sh 1.2.3 (node-v1.2.3)" bash "$stamped" --version
  t "stamped dry run (no dev hooks) prints the version and the key" 0 "verification key: $(sed -n 2p "$w/a.pub" | cut -c1-12)..." bash "$stamped" --dry-run --log "$w/s.log"
  # The real release path: the assemble script with a throwaway key and fixture assets.
  mkdir -p "$w/bin2" "$w/rel"
  printf 'a\n' >"$w/bin2/aiagent-linux-amd64"; printf 'b\n' >"$w/bin2/aiagent-linux-arm64"
  printf 'src\n' >"$w/local-ai-node-v1.2.3-src.tar.gz"
  t "assemble-node-release stamps, signs and verifies with this installer" 0 "install-node.sh" env MINISIGN_SECRET_KEY="$(cat "$w/a.key")" MINISIGN_PASSWORD=pw-test \
    "$SRC/scripts/release/assemble-node-release.sh" --tag node-v1.2.3 --bin-dir "$w/bin2" --tarball "$w/local-ai-node-v1.2.3-src.tar.gz" \
    --pubkey "$w/a.pub" --out "$w/rel" --installer "$INST" --sign
  t "the released install-node.sh is the stamped file" 0 "" bash -c 'bash "$1/install-node.sh" --version | grep -q "1.2.3" && ! grep -qE "@@LAI_[A-Z_]+@@" "$1/install-node.sh"' _ "$w/rel"
else
  echo "  skip minisign not installed: stamping tests skipped"
fi

# shellcheck source=scripts/tests/test-installer-p5p6.sh
source "$SRC/scripts/tests/test-installer-p5p6.sh"

echo "== INST-02 P1 preflight (fake machine, no root)"
I2_RA=(--node-id ai-n9 --desktop-user "$(id -un)")
i2_fresh() { rm -rf "$w/i2-vstate" "$w/i2-vsystemd" "$I2_IROOT"; mkdir -p "$w/i2-vsystemd"; : >"$FAKE_CALLS"; }
I2_SS_PY=$'LISTEN 0 1 127.0.0.1:7070 0.0.0.0:* users:(("python3",pid=1,fd=1))\n'
I2_SS_OURS=$'LISTEN 0 1 127.0.0.1:7070 0.0.0.0:* users:(("aiagent",pid=1,fd=1))\nLISTEN 0 1 0.0.0.0:8080 0.0.0.0:* users:(("llama-qwen",pid=2,fd=1))\n'
I2_SS_NGINX=$'LISTEN 0 1 0.0.0.0:8080 0.0.0.0:* users:(("nginx",pid=3,fd=1))\n'
t "good machine: preflight passes (cpu)" 0 "Preflight passed: backend cpu, 16000 MiB RAM" dryv -- "${I2_RA[@]}"
t "NVIDIA GPU: cuda with VRAM and compute capability" 0 "NVIDIA GPU: NVIDIA GeForce RTX 2080, 8192 MiB, compute capability 7.5" dryv FAKE_GPU=nvidia -- "${I2_RA[@]}"
t "NVIDIA GPU: backend cuda" 0 "Preflight passed: backend cuda" dryv FAKE_GPU=nvidia -- "${I2_RA[@]}"
t "AMD unified memory uses the GTT (vulkan)" 0 "GPU (vulkan): vendor 0x1002, 32768 MiB usable" dryv LAI_FAKE_ROOT="$w/i2-fx-amd" -- "${I2_RA[@]}"
t "--backend overrides detection" 0 "backend forced to vulkan" dryv -- "${I2_RA[@]}" --backend vulkan
t "Ubuntu 26.04 is supported" 0 "Ubuntu 26.04 (resolute)" dryv LAI_FAKE_ROOT="$w/i2-fx-2604" -- "${I2_RA[@]}"
i2_fresh
t "debian is refused: exit 3, LAI-INS-001" 3 "LAI-INS-001" realv LAI_FAKE_ROOT="$w/i2-fx-debian" -- "${I2_RA[@]}"
t "a refusal changed nothing on the system" 0 "" test ! -e "$I2_IROOT"
t "Ubuntu 25.10 (interim) is refused" 3 "Only Ubuntu 24.04 and 26.04 LTS are supported" realv LAI_FAKE_ROOT="$w/i2-fx-2510" -- "${I2_RA[@]}"
t "refusal says NOTHING WAS CHANGED" 3 "NOTHING WAS CHANGED" realv LAI_FAKE_ROOT="$w/i2-fx-debian" -- "${I2_RA[@]}"
t "unsupported architecture -> 3" 3 "LAI-INS-001" realv FAKE_ARCH=i386 -- "${I2_RA[@]}"
t "container -> 3, LAI-INS-002" 3 "LAI-INS-002" realv FAKE_VIRT=docker -- "${I2_RA[@]}"
t "not run with sudo -> 3, LAI-INS-003" 3 "LAI-INS-003" realv LAI_FAKE_EUID=1000 -- "${I2_RA[@]}"
t "root without a desktop user -> 3, LAI-INS-003" 3 "Pass --desktop-user" realv SUDO_USER= -- --node-id ai-n9
t "sudo from root without a desktop user -> 3" 3 "LAI-INS-003" realv SUDO_USER=root -- --node-id ai-n9
t "a dry run as a normal user only warns" 0 "a real run would stop here with LAI-INS-003" dryv LAI_FAKE_EUID=1000 -- "${I2_RA[@]}"
I2_allout="$(realv LAI_FAKE_ROOT="$w/i2-fx-debian" FAKE_VIRT=lxc FAKE_SS="$I2_SS_PY" -- "${I2_RA[@]}" 2>&1)"
t "every problem is reported together (001, 002, 014)" 0 "" bash -c 'for c in LAI-INS-001 LAI-INS-002 LAI-INS-014; do grep -q "FAIL $c" <<<"$1" || exit 1; done' _ "$I2_allout"
t "each problem carries a fix sentence" 0 "" bash -c 'grep -q "fix: Run the installer on the real machine." <<<"$1"' _ "$I2_allout"
t "a foreign program on 7070 -> exit 1, LAI-INS-014 naming it" 1 "port 7070 is used by python3" dryv FAKE_SS="$I2_SS_PY" -- "${I2_RA[@]}"
t "a holder a normal user cannot see only warns" 0 "run as root to see which program" dryv LAI_FAKE_EUID=1000 FAKE_SS=$'LISTEN 0 1 0.0.0.0:8080 0.0.0.0:*\n' -- "${I2_RA[@]}"
t "our own services and llama-qwen on 8080 are fine" 0 "ports 7070, 8080, 8081 and 8888 are free or ours" dryv FAKE_SS="$I2_SS_OURS" -- "${I2_RA[@]}"
t "a foreign program on 8080 -> exit 1" 1 "port 8080 is used by nginx" dryv FAKE_SS="$I2_SS_NGINX" -- "${I2_RA[@]}"
t "too little disk -> exit 1, LAI-INS-013" 1 "LAI-INS-013" dryv FAKE_AVAIL=1000000000 -- "${I2_RA[@]}"
t "too little disk with --yes only warns" 0 "WARN LAI-INS-013" dryv FAKE_AVAIL=1000000000 -- "${I2_RA[@]}" --yes
t "low RAM only warns (LAI-INS-012)" 0 "WARN LAI-INS-012" dryv LAI_FAKE_ROOT="$w/i2-fx-lowram" -- "${I2_RA[@]}"
t "no internet -> exit 1, LAI-INS-015 suggests --from-ark" 1 "Connect to the internet, or use --from-ark" dryv LAI_INTERNET_URL=file:///nonexistent-lai -- "${I2_RA[@]}"
t "an ark install does not need the internet" 0 "ark drive $I2_ARK has the kit for noble/amd64" dryv LAI_INTERNET_URL=file:///nonexistent-lai -- "${I2_RA[@]}" --from-ark "$I2_ARK"
I2_ARK2="$w/i2-ark2"; i2_mkark "$I2_ARK2"; rm -rf "$I2_ARK2/localai/kit/debs" "$I2_ARK2/localai/ROOT.json"
t "ark missing parts -> exit 1, LAI-INS-016 lists them" 1 "missing: localai/ROOT.json, localai/kit/debs/noble-amd64/" dryv -- "${I2_RA[@]}" --from-ark "$I2_ARK2"
t "a wrong clock only warns (LAI-INS-017) and the install goes on" 0 "WARN LAI-INS-017" dryv LAI_FAKE_YEAR=1970 -- "${I2_RA[@]}"
t "laptop on battery warns about the charger" 0 "Plug in the charger before continuing" dryv LAI_FAKE_ROOT="$w/i2-fx-laptop" -- "${I2_RA[@]}"
t "a legacy llama-qwen service is noticed" 0 "will be migrated" bash -c 'mkdir -p "$1"; printf "[Service]\nExecStart=/x/llama-server\n" >"$1/llama-qwen.service"; LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$2" LAI_INSTALL_SYSTEMD_DIR="$1" "$3" --dry-run --log "$4" --node-id ai-n9 --desktop-user "$(id -un)" 2>&1' _ "$w/i2-legsys" "$w/state" "$STAMPED" "$w/i2-leg.log"
t "broken NVIDIA driver: dry run explains the offer" 0 "[dry-run] would ask: install the NVIDIA driver now?" dryv FAKE_GPU=broken -- "${I2_RA[@]}"
t "broken NVIDIA driver: warning LAI-INS-010" 0 "WARN LAI-INS-010" dryv FAKE_GPU=broken -- "${I2_RA[@]}"
i2_fresh
t "driver offer accepted online: installs and asks for a reboot" 0 "REBOOT the computer now" realv FAKE_GPU=broken -- "${I2_RA[@]}" --yes
t "ubuntu-drivers install --gpgpu was run" 0 "" grep -qx "ubuntu-drivers install --gpgpu" "$FAKE_CALLS"
i2_fresh
t "driver offer declined (no terminal): carries on as cpu" 0 "Carrying on without the GPU" realv FAKE_GPU=broken -- "${I2_RA[@]}"
t "declining ran no driver install" 0 "" test ! -s "$FAKE_CALLS" -o "$(grep -c ubuntu-drivers "$FAKE_CALLS")" = 0
i2_fresh
t "offline driver offer: left to the packages step, no ubuntu-drivers" 0 "installed by the packages step" realv FAKE_GPU=broken -- "${I2_RA[@]}" --yes --from-ark "$I2_ARK"
t "offline driver offer ran no ubuntu-drivers" 0 "" bash -c '! grep -q ubuntu-drivers "$1"' _ "$FAKE_CALLS"

echo "== INST-02 P2 stage artifacts"
i2_fresh
t "online install stages the agent" 0 "staged from release node-v9.9.9-test" realv -- "${I2_RA[@]}"
t "the agent is in place, 0755" 0 "" test "$(stat -c %a "$I2_IROOT/opt/localai/bin/aiagent")" = 755
t "aictl links to the agent" 0 "" test "$(readlink "$I2_IROOT/usr/local/bin/aictl")" = /opt/localai/bin/aiagent
t "the installer is copied, 0755, same bytes as the release" 0 "" bash -c 'test "$(stat -c %a "$1/opt/localai/installers/install-node.sh")" = 755 && cmp -s "$1/opt/localai/installers/install-node.sh" "$2/install-node.sh"' _ "$I2_IROOT" "$I2_REL"
t "install-node.sh matches SHA256SUMS" 0 "" bash -c 'cd "$2" && grep " install-node.sh$" SHA256SUMS | sed "s| install-node.sh| $1/opt/localai/installers/install-node.sh|" | sha256sum -c --quiet' _ "$I2_IROOT" "$I2_REL"
t "/usr/local/sbin/install-node.sh links to it" 0 "" test "$(readlink "$I2_IROOT/usr/local/sbin/install-node.sh")" = /opt/localai/installers/install-node.sh
t "the catalog step ran against the release" 0 "" grep -q "^aiagent setup catalog --from release" "$FAKE_CALLS"
t "the first install makes no aiagent.prev" 0 "" test ! -e "$I2_IROOT/opt/localai/bin/aiagent.prev"
t "state records P2 done" 0 "" grep -q '"P2": {[^}]*"status":"done"' "$w/i2-vstate/state.json"
t "re-run: stage is already done" 0 "already done" realv -- "${I2_RA[@]}"
I2_REL2="$w/i2-rel2"; i2_mkrel "$I2_REL2"; echo "# a newer agent" >>"$I2_REL2/aiagent-linux-amd64"
(cd "$I2_REL2" && sha256sum aiagent-linux-amd64 aiagent-linux-arm64 install-node.sh >SHA256SUMS && printf 'SIG:%s:%s' "$I2_PUBKEY" "$(sha256sum SHA256SUMS | cut -d' ' -f1)" >SHA256SUMS.minisig)
rm -rf "$w/i2-vstate"
t "a newer agent replaces it" 0 "staged from release" realv LAI_RELEASE_BASE_URL="file://$I2_REL2" -- "${I2_RA[@]}"
t "aiagent.prev keeps the previous binary" 0 "" cmp -s "$I2_IROOT/opt/localai/bin/aiagent.prev" "$I2_REL/aiagent-linux-amd64"
t "aiagent is the new binary and aictl is still linked" 0 "" bash -c 'cmp -s "$1/opt/localai/bin/aiagent" "$2/aiagent-linux-amd64" && test -L "$1/usr/local/bin/aictl"' _ "$I2_IROOT" "$I2_REL2"
rm -rf "$w/i2-vstate"
t "re-run with the same binary keeps aiagent.prev" 0 "is already this version" realv LAI_RELEASE_BASE_URL="file://$I2_REL2" -- "${I2_RA[@]}"
t "aiagent.prev was not overwritten" 0 "" cmp -s "$I2_IROOT/opt/localai/bin/aiagent.prev" "$I2_REL/aiagent-linux-amd64"

i2_resign() { (cd "$1" && printf 'SIG:%s:%s' "$I2_PUBKEY" "$(sha256sum SHA256SUMS | cut -d' ' -f1)" >SHA256SUMS.minisig); }
I2_BADSIG="$w/i2-rel-badsig"; i2_mkrel "$I2_BADSIG"; echo "tampered" >>"$I2_BADSIG/SHA256SUMS"
I2_BADBIN="$w/i2-rel-badbin"; i2_mkrel "$I2_BADBIN"; echo "tampered" >>"$I2_BADBIN/aiagent-linux-amd64"
I2_BADINST="$w/i2-rel-badinst"; i2_mkrel "$I2_BADINST"; echo "# tampered" >>"$I2_BADINST/install-node.sh"
I2_NOINST="$w/i2-rel-noinst"; i2_mkrel "$I2_NOINST"; grep -v " install-node.sh$" "$I2_NOINST/SHA256SUMS" >"$I2_NOINST/s" && mv "$I2_NOINST/s" "$I2_NOINST/SHA256SUMS"; i2_resign "$I2_NOINST"
I2_NOAGENT="$w/i2-rel-noagent"; i2_mkrel "$I2_NOAGENT"; grep -v " aiagent-linux-amd64$" "$I2_NOAGENT/SHA256SUMS" >"$I2_NOAGENT/s" && mv "$I2_NOAGENT/s" "$I2_NOAGENT/SHA256SUMS"; i2_resign "$I2_NOAGENT"
for v in badsig:"the signature of SHA256SUMS is wrong" badbin:"does not match SHA256SUMS" badinst:"install-node.sh does not match SHA256SUMS" \
         noinst:"SHA256SUMS does not list install-node.sh" noagent:"SHA256SUMS does not list aiagent-linux-amd64"; do
  i2_fresh
  t "${v%%:*}: LAI-INS-021, exit 1" 1 "${v#*:}" realv LAI_RELEASE_BASE_URL="file://$w/i2-rel-${v%%:*}" -- "${I2_RA[@]}"
  t "${v%%:*}: nothing placed in /opt/localai/bin" 0 "" test ! -e "$I2_IROOT/opt/localai/bin"
done
t "a failed stage is recorded as failed" 0 "" grep -q '"P2": {[^}]*"status":"failed"' "$w/i2-vstate/state.json"
i2_fresh
t "download failure -> LAI-INS-015" 1 "could not download" realv LAI_RELEASE_BASE_URL="file:///nonexistent-lai" -- "${I2_RA[@]}"

i2_fresh
t "offline install works with networking 'disabled'" 0 "staged from ark $I2_ARK" realv LAI_RELEASE_BASE_URL=file:///nonexistent-lai LAI_INTERNET_URL=file:///nonexistent-lai -- "${I2_RA[@]}" --from-ark "$I2_ARK"
t "offline: agent and installer placed from the kit" 0 "" bash -c 'cmp -s "$1/opt/localai/bin/aiagent" "$2/localai/kit/bin/aiagent-linux-amd64" && cmp -s "$1/opt/localai/installers/install-node.sh" "$2/localai/kit/installers/install-node.sh" && test -L "$1/usr/local/sbin/install-node.sh"' _ "$I2_IROOT" "$I2_ARK"
t "offline: the catalog step uses the ark" 0 "" grep -q "^aiagent setup catalog --from ark --ark $I2_ARK" "$FAKE_CALLS"
I2_ARK3="$w/i2-ark3"; i2_mkark "$I2_ARK3"; echo tampered >>"$I2_ARK3/localai/kit/bin/aiagent-linux-amd64"
i2_fresh
t "offline: a tampered agent on the drive -> LAI-INS-021" 1 "LAI-INS-021" realv -- "${I2_RA[@]}" --from-ark "$I2_ARK3"
t "offline: nothing placed" 0 "" test ! -e "$I2_IROOT/opt/localai/bin"
I2_ARK4="$w/i2-ark4"; i2_mkark "$I2_ARK4"; echo "# x" >>"$I2_ARK4/localai/kit/bin/SHA256SUMS"
t "offline: a tampered SHA256SUMS -> LAI-INS-021" 1 "the signature of SHA256SUMS is wrong" realv -- "${I2_RA[@]}" --from-ark "$I2_ARK4"
# The layout `aictl ark sync` produces (issue #360): the release's signed list in kit/bin/release/, the installer
# beside the binaries, no kit/installers/ folder.
I2_ARK5="$w/i2-ark5"; i2_mkark "$I2_ARK5"
mkdir -p "$I2_ARK5/localai/kit/bin/release"; mv "$I2_ARK5"/localai/kit/bin/SHA256SUMS* "$I2_ARK5/localai/kit/bin/release/"
mv "$I2_ARK5/localai/kit/installers/install-node.sh" "$I2_ARK5/localai/kit/bin/"; rmdir "$I2_ARK5/localai/kit/installers"
i2_fresh
t "offline: a drive in the layout of aictl ark sync installs" 0 "staged from ark $I2_ARK5" realv -- "${I2_RA[@]}" --from-ark "$I2_ARK5"
t "offline: ...agent and installer placed from kit/bin" 0 "" bash -c 'cmp -s "$1/opt/localai/bin/aiagent" "$2/localai/kit/bin/aiagent-linux-amd64" && cmp -s "$1/opt/localai/installers/install-node.sh" "$2/localai/kit/bin/install-node.sh" && test -L "$1/usr/local/sbin/install-node.sh"' _ "$I2_IROOT" "$I2_ARK5"
I2_ARK6="$w/i2-ark6"; i2_mkark "$I2_ARK6"
mkdir -p "$I2_ARK6/localai/kit/bin/release"; mv "$I2_ARK6"/localai/kit/bin/SHA256SUMS* "$I2_ARK6/localai/kit/bin/release/"
echo "# x" >>"$I2_ARK6/localai/kit/bin/release/SHA256SUMS"
i2_fresh
t "offline: a tampered release/SHA256SUMS -> LAI-INS-021" 1 "the signature of SHA256SUMS is wrong" realv -- "${I2_RA[@]}" --from-ark "$I2_ARK6"
t "offline: ...and nothing placed" 0 "" test ! -e "$I2_IROOT/opt/localai/bin"
I2_ARK7="$w/i2-ark7"; i2_mkark "$I2_ARK7"; rm "$I2_ARK7/localai/kit/bin/SHA256SUMS.minisig"
mkdir -p "$I2_ARK7/localai/kit/bin/release"; cp "$I2_ARK5/localai/kit/bin/release/SHA256SUMS" "$I2_ARK7/localai/kit/bin/release/"
i2_fresh
t "offline: release/ with a list but no signature -> LAI-INS-016" 1 "LAI-INS-016" realv -- "${I2_RA[@]}" --from-ark "$I2_ARK7"
i2_fresh
t "dry run offline: verifies for real, places nothing" 0 "[dry-run] would run: install -m 0755" dryv -- "${I2_RA[@]}" --from-ark "$I2_ARK"
t "dry run offline: nothing under the install root" 0 "" test ! -e "$I2_IROOT"
t "dry run offline: the catalog step is a dry run" 0 "" grep -q "setup catalog --from ark --ark $I2_ARK --dry-run" "$FAKE_CALLS"
t "dry run offline: a bad drive still fails" 1 "LAI-INS-021" dryv -- "${I2_RA[@]}" --from-ark "$I2_ARK3"
t "dry run online: says what it would download" 0 "[dry-run] would download aiagent-linux-amd64" dryv -- "${I2_RA[@]}"
i2_fresh
t "arm64 uses the arm64 binary" 0 "aiagent-linux-arm64 matches SHA256SUMS" realv FAKE_ARCH=arm64 -- "${I2_RA[@]}"

# minisign cannot be installed: the --agent-sha256 fallback (online only). PATH holds the fakes but no minisign.
I2_LEAN="$w/i2-lean"; mkdir -p "$I2_LEAN"
for c in bash sed awk cmp cp mkdir install ln mv rm mktemp tr cat date head tail readlink sha256sum curl dirname basename id stat flock sleep find cut grep sort wc env dpkg-query touch chmod chown diff uname setsid; do
  pth="$(PATH=/usr/bin:/bin command -v "$c" || true)"; [[ -z "$pth" ]] || ln -sf "$pth" "$I2_LEAN/$c"
done
for f in dpkg systemd-detect-virt lspci nvidia-smi ss df mokutil; do ln -sf "$I2_FB/$f" "$I2_LEAN/$f"; done
ln -sf "$w/llfake/systemctl" "$I2_LEAN/systemctl"   # the INST-05 fake systemctl (P9 restarts the agent)
# shellcheck disable=SC2154  # p56 is assigned by the sourced test-installer-p5p6-fixtures.sh
ln -sf "$p56/bin/apt-get" "$I2_LEAN/apt-get"   # the INST-04 fake apt-get
I2_RELSHA="$(sha256sum "$I2_REL/aiagent-linux-amd64" | cut -d' ' -f1)"
i2_fresh
t "no minisign + --agent-sha256: the agent is checked against it" 0 "checking the agent against the sha256 you gave" realv PATH="$I2_LEAN" -- "${I2_RA[@]}" --agent-sha256 "$I2_RELSHA"
t "fallback: the agent is placed" 0 "" test -x "$I2_IROOT/opt/localai/bin/aiagent"
t "fallback: the installer copied is the running script" 0 "" cmp -s "$I2_IROOT/opt/localai/installers/install-node.sh" "$STAMPED"
i2_fresh
t "no minisign + wrong --agent-sha256 -> LAI-INS-021" 1 "does not match --agent-sha256" realv PATH="$I2_LEAN" -- "${I2_RA[@]}" --agent-sha256 "$fakehex"
t "no minisign and no --agent-sha256 -> LAI-INS-021" 1 "minisign is not available" realv PATH="$I2_LEAN" -- "${I2_RA[@]}"
t "no minisign: nothing placed" 0 "" test ! -e "$I2_IROOT/opt/localai/bin"

if [[ $REAL_MS == 1 ]]; then
  echo "== INST-02 P2 with the real minisign"
  printf '%s\n%s\n' pw-test pw-test | minisign -G -p "$w/i2-r.pub" -s "$w/i2-r.key" -f >/dev/null 2>&1
  I2_RK="$(sed -n 2p "$w/i2-r.pub")"
  I2_RSTAMP="$w/i2-install-node.realkey.sh"; sed "s|$I2_PUBKEY|$I2_RK|g" "$STAMPED" >"$I2_RSTAMP"; chmod +x "$I2_RSTAMP"
  I2_RELR="$w/i2-rel-real"; i2_mkrel "$I2_RELR"; cp "$I2_RSTAMP" "$I2_RELR/install-node.sh"
  (cd "$I2_RELR" && sha256sum aiagent-linux-amd64 aiagent-linux-arm64 install-node.sh >SHA256SUMS && printf 'pw-test\n' | minisign -S -s "$w/i2-r.key" -m SHA256SUMS -x SHA256SUMS.minisig >/dev/null 2>&1)
  mkdir -p "$w/i2-fakebin-real"; cp -a "$I2_FB"/. "$w/i2-fakebin-real/"; rm -f "$w/i2-fakebin-real/minisign"
  I2_RPATH="$w/i2-fakebin-real:${PATH#"$I2_FB":}"
  i2_realr() { env PATH="$I2_RPATH" "${llhooks[@]}" LAI_RELEASE_BASE_URL="file://$1" LAI_INSTALL_STATE_DIR="$w/i2-vstate" LAI_INSTALL_SYSTEMD_DIR="$w/i2-vsystemd" "$I2_RSTAMP" --log "$w/i2-realv.log" "${I2_RA[@]}"; }
  i2_fresh
  t "real minisign: a good signature installs" 0 "signature of SHA256SUMS is good" i2_realr "$I2_RELR"
  I2_RELT="$w/i2-rel-real-bad"; cp -a "$I2_RELR" "$I2_RELT"; echo "tampered" >>"$I2_RELT/SHA256SUMS"
  i2_fresh
  t "real minisign: a tampered SHA256SUMS -> LAI-INS-021" 1 "LAI-INS-021" i2_realr "$I2_RELT"
  t "real minisign: nothing placed" 0 "" test ! -e "$I2_IROOT/opt/localai/bin"
fi

echo "== INST-02 P3 profile"
i2_fresh
t "profile is chosen and printed in plain words" 0 "Profile chosen: small-8gb (backend cpu)" realv -- "${I2_RA[@]}"
t "the survival models are shown" 0 "survival models (always kept for emergencies): Model A" realv -- "${I2_RA[@]}"
t "--profile overrides the suggestion" 0 "Profile chosen: max-128gb" realv -- "${I2_RA[@]}" --profile max-128gb
t "the catalog and backend are passed to the agent" 0 "" grep -q "^aiagent setup profile-suggest --backend cpu --catalog $I2_IROOT/etc/localai/catalog --json" "$FAKE_CALLS"
t "a profile that does not fit asks (no terminal: not confirmed -> exit 4)" 4 "Not confirmed: Use the profile" realv FAKE_MISMATCH=true -- "${I2_RA[@]}" --profile max-128gb
t "a profile that does not fit: warning LAI-CAT-005 is shown" 4 "LAI-CAT-005" realv FAKE_MISMATCH=true -- "${I2_RA[@]}" --profile max-128gb
t "--yes confirms a mismatch" 0 "Profile chosen: max-128gb" realv FAKE_MISMATCH=true -- "${I2_RA[@]}" --profile max-128gb --yes
t "profile needs more disk than there is -> LAI-INS-013, exit 1" 1 "the disk is too small for the profile 'small-8gb'" realv FAKE_MINFREE=900000000000 -- "${I2_RA[@]}"
t "...but --yes caps the budget instead" 0 "Profile chosen: small-8gb" realv FAKE_MINFREE=900000000000 -- "${I2_RA[@]}" --yes
t "the agent preflight runs with the profile's free space" 0 "" grep -q "^aiagent setup preflight --min-free-bytes 1000 --backend cpu --desktop-user " "$FAKE_CALLS"
t "an agent preflight failure stops the install (exit 1)" 1 "the agent's preflight failed (exit 1)" realv FAKE_PREFLIGHT_RC=1 -- "${I2_RA[@]}"
t "an agent preflight refusal exits 3" 3 "This computer is not supported" realv FAKE_PREFLIGHT_RC=3 -- "${I2_RA[@]}"
t "dry run online: profile step says what it would do" 0 "[dry-run] would run: aiagent setup profile-suggest --json" dryv -- "${I2_RA[@]}"
t "dry run offline: the staged agent really suggests a profile" 0 "Profile chosen: small-8gb" dryv -- "${I2_RA[@]}" --from-ark "$I2_ARK"

echo "== guided mode (#429): no arguments on a terminal asks instead of requiring flags"
G="$w/guide"; mkdir -p "$G/media/u" "$G/empty-media"
# g <answers> [VAR=value ...]: run the stamped installer with NO arguments; the answers are the "terminal" input.
g() {
  local answers="$1"; shift
  printf '%b' "$answers" >"$G/in"; : >"$G/out"; : >"$G/rec"
  env LAI_INSTALLER_DEV=1 LAI_INSTALLER_GUIDE=1 LAI_INSTALLER_GUIDE_IN="$G/in" LAI_INSTALLER_GUIDE_OUT="$G/out" \
    LAI_INSTALLER_GUIDE_RECORD="$G/rec" LAI_INSTALLER_MEDIA="$G/empty-media" LAI_INSTALL_NODE_YAML="$G/none.yaml" \
    LAI_INSTALL_STATE_DIR="$G/state" "$@" bash "$STAMPED"
}
gout() { cat "$G/out"; }
grec() { cat "$G/rec"; }

t "defaults: preview then 'not now' leaves nothing changed (exit 4)" 4 "" g 'ai-n4\n\n\n\n'
t "...the four questions were asked" 0 "4/4  Preview" gout
t "...the one-line equivalent is shown" 0 "--node-id ai-n4 --cloud-url https://ai.marin.cr --enroll" gout
t "...the preview is a dry run of those flags" 0 "preview: --node-id ai-n4 --cloud-url https://ai.marin.cr --enroll --dry-run" grec
t "...and the real install did not run" 1 "" grep -q '^run:' "$G/rec"
t "...it says nothing was changed" 0 "Nothing was changed" gout
t "no cloud, preview, then yes: runs the real install with the flags" 0 "" g 'ai-n4\nn\ny\ny\n'
t "...run line" 0 "run: --node-id ai-n4" grec
t "...no --enroll when the cloud was declined" 1 "" grep -q -- '--enroll' "$G/rec"
t "a wrong name is explained and asked again" 0 "" g 'bob\nAI-N7\nn\nn\ny\n'
t "...the explanation" 0 'is not a node name. It is ai-n and a number' gout
t "...upper case is accepted as the same name" 0 "run: --node-id ai-n7" grec
t "five wrong names: exit 2, nothing changed" 2 "" g 'a\nb\nc\nd\ne\n'
t "...says so" 0 "No valid name was given. Nothing was changed." gout
t "no answer at all (end of input): exit 2" 2 "" g ''
mkdir -p "$G/media/u/AI-ARK/localai"
t "a connected AI-ARK drive is offered, cloud off by default" 0 "" g 'ai-n4\n\n\nn\ny\n' LAI_INSTALLER_MEDIA="$G/media"
t "...installs from the drive" 0 "run: --node-id ai-n4 --from-ark $G/media/u/AI-ARK" grec
t "...and the drive was named" 0 "An AI-ARK drive is connected: $G/media/u/AI-ARK" gout
printf 'node:\n  id: ai-n3\n' >"$G/node.yaml"
t "an existing node name is the suggestion" 0 "" g '\nn\nn\ny\n' LAI_INSTALL_NODE_YAML="$G/node.yaml"
t "...kept" 0 "run: --node-id ai-n3" grec
t "...and explained" 0 "already set up as ai-n3" gout
t "a different name than the existing one is warned about" 0 "" g 'ai-n5\nn\nn\ny\n' LAI_INSTALL_NODE_YAML="$G/node.yaml"
t "...LAI-INS-020 named" 0 "LAI-INS-020" gout
t "a failing preview stops with its exit code and changes nothing" 1 "" g 'ai-n4\nn\ny\n' LAI_INSTALLER_GUIDE_PREVIEW_RC=1
t "...explained" 0 "The preview stopped with a problem" gout
t "a non-answer to yes/no is asked again" 0 "" g 'ai-n4\nmaybe\nn\nn\ny\n'
t "...with a hint" 0 "Please answer y (yes) or n (no)." gout
t "an argument means no questions" 0 "" bash -c '! env LAI_INSTALLER_DEV=1 LAI_INSTALLER_GUIDE=1 LAI_INSTALLER_GUIDE_IN=/dev/null LAI_INSTALLER_GUIDE_OUT="$1" bash "$2" --version >/dev/null 2>&1 || ! grep -q "Node name" "$1"' _ "$G/out2" "$STAMPED"
: >"$G/out3"
t "LAI_INSTALLER_GUIDED=1 (the second run) never asks again" 0 "" bash -c 'env LAI_INSTALLER_DEV=1 LAI_INSTALLER_GUIDE=1 LAI_INSTALLER_GUIDED=1 LAI_INSTALLER_GUIDE_IN=/dev/null LAI_INSTALLER_GUIDE_OUT="$1" LAI_INSTALL_STATE_DIR="$3" timeout 60 bash "$2" >/dev/null 2>&1; ! grep -q "Node name" "$1"' _ "$G/out3" "$STAMPED" "$G/state3"
t "without a terminal and without the dev hook, no questions (help text documents guided mode)" 0 "asks what it needs (guided mode)" bash "$STAMPED" --help

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
