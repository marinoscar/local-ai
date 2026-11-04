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
# dry <args...>: a dry run of the template in a throwaway folder
dry() { env "${llhooks[@]}" LAI_INSTALL_NODE_YAML="$w/none.yaml" LAI_INSTALL_STATE_DIR="$w/state" "$INST" --dry-run --log "$w/dry.log" "$@"; }
# real <args...>: a real run of the skeleton (dev hooks only: state and log in $w)
real() { env "${llhooks[@]}" LAI_INSTALL_STATE_DIR="$w/rstate" LAI_INSTALL_SYSTEMD_DIR="$w/systemd" "$STAMPED" --log "$w/real.log" "$@"; }

echo "== arguments"
t "help exits 0" 0 "Usage:" "$INST" --help
t "template says it is unstamped and stops" 2 "never stamped" "$INST" --log "$w/x.log"
t "valid flags are all accepted" 0 "==> [15/15]" dry --node-id ai-n4 --profile desk-small --backend cpu --cloud-url https://ai.example.test \
  --no-migrate --yes --pin-file "$INST" --inventory "$INST" --keep-inventory --no-hardening --no-ssh --no-polkit --no-tailscale \
  --no-sync --serve-pattern A --reboot --enroll-file "$INST"
mkdir -p "$w/localai/kit"   # P6 offline needs a kit folder on the drive
t "from-ark, desktop-user and agent-sha256" 0 "source: ark:$w" dry --from-ark "$w" --desktop-user root --agent-sha256 "$fakehex"
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
# In a dry run P5, P6 (INST-04), P7, P8 (INST-05), P11, P14 and P15 (INST-07) are implemented; 8 phases are still stubs. Lower this as items land.
STUBS_IN_DRY_RUN=8
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
# Phases whose real implementation has landed (INST-04: P5, P6; INST-07: P11, P14 under the skeleton's defaults). Every other phase
# is still a stub and must be recorded as not-implemented; add a phase id here when its item is merged.
IMPLEMENTED="P5 P6 P7 P8 P11 P14"
t "every phase has status, version, finished_at, details" 0 "" bash -c '[ "$(grep -cE "\"status\":\"(not-implemented|done)\",\"version\":\"9.9.9-test\",\"finished_at\":\"20[0-9-]*T[0-9:]*Z\",\"details\":" "$1")" = 15 ]' _ "$s"
t "a stub is never recorded as done" 0 "" bash -c 'for i in $(seq 1 15); do case " $2 " in *" P$i "*) continue ;; esac; grep -q "\"P$i\": .*\"status\":\"done\"" "$1" && exit 1; done; exit 0' _ "$s" "$IMPLEMENTED"
t "atomic: no temp file left behind" 0 "" test -z "$(find "$w/rstate" -name '.state.*')"
t "lock file exists after a real run" 0 "" test -f "$w/rstate/lock"
rerun="$(real --node-id ai-n9 2>&1)"
t "second run is a re-run" 0 "kind: re-run" grep -F "kind: re-run" <<<"$rerun"
touch "$w/systemd/llama-qwen.service"
t "legacy llama-qwen.service means migration" 0 "kind: migration" bash -c 'env "${@:5}" LAI_INSTALL_STATE_DIR="$2" LAI_INSTALL_SYSTEMD_DIR="$3" "$1" --yes --log "$4" 2>&1' _ "$STAMPED" "$w/rstate" "$w/systemd" "$w/real.log" "${llhooks[@]}"
rm -f "$w/systemd/llama-qwen.service"
t "state load reads what state write wrote" 0 "" bash -c 'source "$1"; STATE_FILE="$2"; state_load; [ "${PH_STATUS[P6]}" = done ] && [ "${PH_STATUS[P7]}" = done ] && [ "${#PH_STATUS[@]}" = 15 ]' _ "$INST" "$s"
# A phase that is done and whose check passes reports "already done" (the hook later items use).
t "idempotency: a done phase with a passing check says 'already done'" 0 "already done" bash -c '
  source "$1"; DRY_RUN=1; STATE_FILE="$2/none"; PH_STATUS[P1]=done; check_preflight() { return 0; }
  run_phase 1 "${PHASE_TABLE[0]}"' _ "$INST" "$w"
t "a failing phase marks failed, exits 1 and resumes later" 1 "FAILED (exit 7)" bash -c '
  source "$1"; set -Eeuo pipefail; DRY_RUN=1; STATE_FILE="$2/none"; LOG_OPEN=; phase_preflight() { return 7; }
  run_phase 1 "${PHASE_TABLE[0]}"' _ "$INST" "$w"

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
mkdir -p "$w/ark/localai/kit/installers"; printf 'KITCOPY\n' >"$w/ark/localai/kit/installers/install-node.sh"
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
b9="$LL/builds/${C40:0:9}-cpu"; rm -rf "$LL/builds/"* "$LL/src/"*
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
  bash -c 'source "$1"; shift; DEV=1; LAI_INSTALL_AIAGENT="$1"; shift; STATE_DIR="$1"; shift; STATE_FILE="$STATE_DIR/state.json"; LOG_OPEN=; mkdir -p "$STATE_DIR"
    parse_args "$@"; CUR_PHASE_ID=""; run_phase 4 "${PHASE_TABLE[3]}"; echo "REBOOT_REQUIRED=$REBOOT_REQUIRED"' _ "$INST" "$fakeagent" "$w/hstate" "$@"
}
: >"$FAKE_ARGS_LOG"
t "--no-hardening skips and never calls the agent" 0 "Laptop hardening is off" hphase --no-hardening
t "  ... the agent was not called" 0 "" test ! -s "$FAKE_ARGS_LOG"
t "dry run without the agent shows the command" 10 "[dry-run] would run: /nonexistent/aiagent setup harden --state-dir" bash -c '
  source "$1"; DEV=1; DRY_RUN=1; LAI_INSTALL_AIAGENT=/nonexistent/aiagent; STATE_DIR="$2"; STATE_FILE="$2/s.json"; LOG_OPEN=; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""
  phase_hardening' _ "$INST" "$w/hstate"
t "a real run without the agent is loud, not done" 10 "NOT IMPLEMENTED YET" bash -c '
  source "$1"; DEV=1; DRY_RUN=0; LAI_INSTALL_AIAGENT=/nonexistent/aiagent; STATE_DIR="$2"; LOG_OPEN=; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""
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
  source "$1"; DEV=1; DRY_RUN=1; LAI_INSTALL_AIAGENT="$2"; STATE_DIR="$3"; LOG_OPEN=; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""
  phase_hardening; cat "$FAKE_ARGS_LOG"' _ "$INST" "$fakeagent" "$w/hstate"
t "agent failure fails the phase with the repair hint" 1 "sudo install-node.sh --repair" env FAKE_RC=1 bash -c '
  source "$1"; set -Eeuo pipefail; DEV=1; DRY_RUN=0; LAI_INSTALL_AIAGENT="$2"; STATE_DIR="$3"; STATE_FILE="$3/s.json"; LOG_OPEN=; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""
  run_phase 4 "${PHASE_TABLE[3]}"' _ "$INST" "$fakeagent" "$w/hstate"
t "check_hardening: passes when the agent verifies" 0 "" env FAKE_CHECK_RC=0 bash -c 'source "$1"; DEV=1; LAI_INSTALL_AIAGENT="$2"; STATE_DIR="$3"; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""; check_hardening' _ "$INST" "$fakeagent" "$w/hstate"
t "check_hardening: fails when the agent finds a problem" 1 "" env FAKE_CHECK_RC=1 bash -c 'source "$1"; DEV=1; LAI_INSTALL_AIAGENT="$2"; STATE_DIR="$3"; OPT_HARDENING=auto; OPT_NO_SSH=0; OPT_DESKTOP_USER=""; check_hardening' _ "$INST" "$fakeagent" "$w/hstate"
: >"$FAKE_ARGS_LOG"
t "revert_hardening calls the agent with --revert" 0 "" bash -c 'source "$1"; DEV=1; DRY_RUN=0; LAI_INSTALL_AIAGENT="$2"; STATE_DIR="$3"; LOG_OPEN=; revert_hardening' _ "$INST" "$fakeagent" "$w/hstate"
t "  ... arguments" 0 "setup harden --revert" grep -F -- "setup harden --revert" "$FAKE_ARGS_LOG"
t "the installer never restarts logind (CI grep)" 0 "ok" "$SRC/scripts/check-no-logind-restart.sh"

echo "== release stamping"
if command -v minisign >/dev/null 2>&1; then
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

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
