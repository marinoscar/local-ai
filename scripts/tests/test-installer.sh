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

# A hand-stamped copy (fake version and key) for runs that must not be refused as "unstamped".
STAMPED="$w/install-node.fake-stamped.sh"
sed -e 's|@@LAI_RELEASE_TAG@@|node-v9.9.9-test|g' -e 's|@@LAI_RELEASE_VERSION@@|9.9.9-test|g' \
    -e 's|@@LAI_MINISIGN_PUBKEY@@|RWTESTTESTTESTTESTTESTTESTTESTTESTTESTTESTTESTTESTTEST|g' "$INST" >"$STAMPED"; chmod +x "$STAMPED"
# dry <args...>: a dry run of the template in a throwaway folder
dry() { LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$w/state" "$INST" --dry-run --log "$w/dry.log" "$@"; }
# real <args...>: a real run of the skeleton (dev hooks only: state and log in $w)
real() { LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$w/rstate" LAI_INSTALL_SYSTEMD_DIR="$w/systemd" "$STAMPED" --log "$w/real.log" "$@"; }

echo "== arguments"
t "help exits 0" 0 "Usage:" "$INST" --help
t "template says it is unstamped and stops" 2 "never stamped" "$INST" --log "$w/x.log"
t "valid flags are all accepted" 0 "==> [15/15]" dry --node-id ai-n4 --profile desk-small --backend cpu --cloud-url https://ai.example.test \
  --no-migrate --yes --pin-file "$INST" --inventory "$INST" --keep-inventory --no-hardening --no-ssh --no-polkit --no-tailscale \
  --no-sync --serve-pattern A --reboot --enroll-file "$INST"
t "from-ark, desktop-user and agent-sha256" 0 "source: ark:$w" dry --from-ark "$w" --desktop-user root --agent-sha256 "$fakehex"
t "repair is parsed and says not implemented" 0 "NOT IMPLEMENTED YET: --repair" dry --repair
t "uninstall with its modifiers is parsed" 0 "NOT IMPLEMENTED YET: --uninstall" dry --uninstall --purge-config --purge-content --revert-hardening --restore-legacy
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
t "every phase says it is not implemented" 0 "" test "$(grep -c '^NOT IMPLEMENTED YET: this phase' <<<"$dryout")" = 15
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
t "every phase has status, version, finished_at, details" 0 "" bash -c '[ "$(grep -c "\"status\":\"not-implemented\",\"version\":\"9.9.9-test\",\"finished_at\":\"20[0-9-]*T[0-9:]*Z\",\"details\":\"not implemented" "$1")" = 15 ]' _ "$s"
t "a stub is never recorded as done" 0 "" bash -c '! grep -q "\"status\":\"done\"" "$1"' _ "$s"
t "atomic: no temp file left behind" 0 "" test -z "$(find "$w/rstate" -name '.state.*')"
t "lock file exists after a real run" 0 "" test -f "$w/rstate/lock"
rerun="$(real --node-id ai-n9 2>&1)"
t "second run is a re-run" 0 "kind: re-run" grep -F "kind: re-run" <<<"$rerun"
touch "$w/systemd/llama-qwen.service"
t "legacy llama-qwen.service means migration" 0 "kind: migration" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_STATE_DIR="$2" LAI_INSTALL_SYSTEMD_DIR="$3" "$1" --log "$4" 2>&1' _ "$STAMPED" "$w/rstate" "$w/systemd" "$w/real.log"
rm -f "$w/systemd/llama-qwen.service"
t "state load reads what state write wrote" 0 "" bash -c 'source "$1"; STATE_FILE="$2"; state_load; [ "${PH_STATUS[P6]}" = not-implemented ] && [ "${#PH_STATUS[@]}" = 15 ]' _ "$INST" "$s"
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

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
