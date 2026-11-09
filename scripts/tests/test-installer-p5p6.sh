# test-installer-p5p6.sh: P5 (users, groups, directories) and P6 (packages, companion programs) of
# installers/install-node.sh (INST-04, issue #149). SOURCED late by test-installer.sh, which provides t(), $INST,
# $STAMPED, $SRC, $w, pass and failn; the fakes come from test-installer-p5p6-fixtures.sh.

echo "== P5/P6 users, directories, packages (INST-04)"
echo "-- package lists"
t "cpu backend: base and build only" 0 "build-essential" p56 'OPT_BACKEND=cpu; inst04_packages | tr "\n" " "'
t "cpu backend: no vulkan or cuda packages" 0 "" p56 'OPT_BACKEND=cpu; ! inst04_packages | grep -qE "vulkan|cuda"'
t "no backend given: cpu" 0 "" p56 '! inst04_packages | grep -qE "vulkan|cuda"'
t "vulkan backend adds the vulkan group" 0 "mesa-vulkan-drivers" p56 'OPT_BACKEND=vulkan; inst04_packages | tr "\n" " "'
t "vulkan backend has no cuda" 0 "" p56 'OPT_BACKEND=vulkan; ! inst04_packages | grep -q cuda'
t "cuda backend adds nvidia-cuda-toolkit" 0 "nvidia-cuda-toolkit" p56 'OPT_BACKEND=cuda; inst04_packages | tr "\n" " "'
t "the detected backend of P1 is used when --backend is absent" 0 "libvulkan-dev" p56 'DETECTED_BACKEND=vulkan; inst04_packages | tr "\n" " "'
t "--backend wins over the detected one" 0 "" p56 'OPT_BACKEND=cpu; DETECTED_BACKEND=cuda; ! inst04_packages | grep -q cuda'
for grp in base:BASE build:BUILD vulkan:VULKAN cuda:CUDA; do
  f="${grp%%:*}"; v="${grp##*:}"
  t "installers/packages/$f.list agrees with the script" 0 "" p56 "diff <(printf '%s\n' \"\${INST04_PKGS_$v[@]}\") '$SRC/installers/packages/$f.list'"
done

echo "-- P6 online"
p56reset
t "online dry run: describes apt, installs nothing" 0 "[dry-run] would run: apt-get update" p56 'DRY_RUN=1; OPT_BACKEND=cpu; phase_packages'
t "online dry run: summarises the install" 0 "[dry-run] would install" p56 'DRY_RUN=1; OPT_BACKEND=cpu; phase_packages'
t "online dry run never ran apt-get update or install" 0 "" bash -c '! grep -qE "^(update|install)" "$1"' _ "$p56/apt.log"
t "online dry run: binaries step is only described when the catalog is not installed" 0 "would run: aiagent setup binaries --online" p56 'DRY_RUN=1; phase_packages'
p56reset
FAKE_INSTALLED=none t "online real: apt-get update, then install, then binaries" 0 "" p56 'OPT_BACKEND=cpu; phase_packages'
t "online real: update ran first" 0 "update" bash -c 'head -1 "$1"' _ "$p56/apt.log"
t "online real: install without recommends" 0 "install -y --no-install-recommends" grep -F "install -y --no-install-recommends" "$p56/apt.log"
t "online real: no upgrade" 0 "" bash -c '! grep -qE "upgrade|dist-upgrade" "$1"' _ "$p56/apt.log"
t "online real: binaries from the catalog" 0 "setup binaries --online --catalog $p56/cat" grep -F "setup binaries" "$p56/aiagent.log"
p56reset
FAKE_INSTALLED=all t "packages already installed: apt is not asked to install" 0 "already installed" p56 'OPT_BACKEND=cpu; phase_packages'
t "already installed: no apt install call" 0 "" bash -c '! grep -q "install" "$1" 2>/dev/null' _ "$p56/apt.log"
p56reset
t "apt failure -> LAI-INS-030" 1 "LAI-INS-030" env FAKE_INSTALLED=none FAKE_APT_FAIL=1 P56_LOG="$p56" bash -c 'PATH="$1/bin:$PATH" LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$1/aiagent"; export PATH LAI_INSTALLER_DEV LAI_INSTALL_AIAGENT; source "$2"; phase_packages' _ "$p56" "$INST"
out="$(env FAKE_INSTALLED=none FAKE_APT_FAIL=1 P56_LOG="$p56" bash -c 'PATH="$1/bin:$PATH" LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$1/aiagent"; export PATH LAI_INSTALLER_DEV LAI_INSTALL_AIAGENT; source "$2"; phase_packages' _ "$p56" "$INST" 2>&1)"
t "apt failure prints at most the last 20 lines of apt" 0 "" bash -c 'n=$(grep -c "^  | apt noise line" <<<"$1"); [ "$n" -le 20 ] && [ "$n" -ge 1 ]' _ "$out"
t "apt failure shows the error line" 0 "Unable to locate package fake" grep -F "Unable to locate package fake" <<<"$out"
t "apt failure stops before the binaries" 0 "" bash -c '! test -s "$1"' _ "$p56/aiagent.log"
t "apt failure with an unknown error names no cause" 0 "" bash -c '! grep -q "Most likely cause" <<<"$1"' _ "$out"
run030() { env FAKE_INSTALLED=none FAKE_APT_FAIL=1 FAKE_APT_ERR="$1" P56_LOG="$p56" bash -c 'PATH="$1/bin:$PATH" LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$1/aiagent"; export PATH LAI_INSTALLER_DEV LAI_INSTALL_AIAGENT; UI_LANG="$3"; source "$2"; UI_LANG="$3"; phase_packages' _ "$p56" "$INST" "$2" 2>&1; }
t "LAI-INS-030: a full disk is named, with its fix" 0 "the disk is full" grep -F "Most likely cause: the disk is full" <<<"$(run030 'E: Write error - write (28: No space left on device)' en)"
t "LAI-INS-030: a full disk, in Spanish" 0 "el disco está lleno" grep -F "Causa más probable: el disco está lleno" <<<"$(run030 'E: Write error - write (28: No space left on device)' es)"
t "LAI-INS-030: a damaged package is named, with its fix" 0 "damaged" grep -F "Most likely cause: a package from the kit is damaged" <<<"$(run030 'E: Failed to fetch  Hash Sum mismatch' en)"
t "LAI-INS-030: a damaged package, in Spanish" 0 "dañado" grep -F "Causa más probable: un paquete del kit está dañado" <<<"$(run030 'dpkg-deb: error: file is not a Debian format archive' es)"

echo "-- P6 offline"
ark="$w/ark"; mkdir -p "$ark/localai/kit"
p56reset
t "offline real: uses aictl kit install-debs with the explicit list" 0 "" p56 "OPT_FROM_ARK='$ark'; OPT_BACKEND=cpu; phase_packages"
t "offline: root is the drive's content root" 0 "kit install-debs --root $ark/localai --packages ca-certificates,curl,jq,git" grep -F "kit install-debs" "$p56/aictl.log"
t "offline: the list includes the build group" 0 "build-essential,cmake" grep -F "build-essential,cmake" "$p56/aictl.log"
t "offline: apt-get is never called by the installer" 0 "" bash -c '! test -s "$1"' _ "$p56/apt.log"
t "offline: no --dry-run on a real run" 0 "" bash -c '! grep -q -- "--dry-run" "$1"' _ "$p56/aictl.log"
t "offline: binaries come from the kit" 0 "setup binaries --kit $ark/localai/kit" grep -F "setup binaries" "$p56/aiagent.log"
p56reset
t "offline dry run passes --dry-run to the checker" 0 "" p56 "DRY_RUN=1; OPT_FROM_ARK='$ark'; phase_packages"
t "offline dry run: install-debs got --dry-run" 0 "--dry-run" grep -F "install-debs" "$p56/aictl.log"
t "offline dry run: binaries step got --dry-run" 0 "--dry-run" grep -F "setup binaries" "$p56/aiagent.log"
t "offline: no kit folder -> LAI-INS-016" 1 "LAI-INS-016" p56 "OPT_FROM_ARK='$w/emptyark'; phase_packages"
p56reset
t "offline: a failing install-debs -> LAI-INS-030" 1 "LAI-INS-030" env FAKE_AICTL_RC=1 P56_LOG="$p56" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_AICTL="$1/aictl"; export LAI_INSTALLER_DEV LAI_INSTALL_AICTL; source "$2"; OPT_FROM_ARK="$3"; phase_packages' _ "$p56" "$INST" "$ark"
t "offline: a failing install-debs stops before the binaries" 0 "" bash -c '! test -s "$1"' _ "$p56/aiagent.log"
t "offline: a failing binaries step -> LAI-INS-035" 1 "LAI-INS-035" env FAKE_AGENT_RC=1 P56_LOG="$p56" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$1/aiagent" LAI_INSTALL_AICTL="$1/aictl"; export LAI_INSTALLER_DEV LAI_INSTALL_AIAGENT LAI_INSTALL_AICTL; source "$2"; OPT_FROM_ARK="$3"; phase_packages' _ "$p56" "$INST" "$ark"
t "offline real run with aictl missing fails with LAI-INS-030" 1 "LAI-INS-030" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_AICTL="$1/nope"; export LAI_INSTALLER_DEV LAI_INSTALL_AICTL; source "$2"; OPT_FROM_ARK="$3"; phase_packages' _ "$p56" "$INST" "$ark"

echo "-- P5 users, groups, directories"
p56reset
t "P5 calls aiagent setup dirs with the options" 0 "" p56 "OPT_DESKTOP_USER=alice; OPT_NODE_ID=ai-n4; OPT_PROFILE=desk-small; phase_users_dirs"
t "P5 passes the desktop user, node id and profile" 0 "setup dirs --desktop-user alice --node-id ai-n4 --profile desk-small" grep -F "setup dirs" "$p56/aiagent.log"
t "P5 without --no-polkit does not pass it" 0 "" bash -c '! grep -q -- "--no-polkit" "$1"' _ "$p56/aiagent.log"
p56reset
t "P5 passes --no-polkit" 0 "" p56 "OPT_NO_POLKIT=1; phase_users_dirs"
t "P5 --no-polkit reaches the step" 0 "setup dirs --no-polkit" grep -F "setup dirs" "$p56/aiagent.log"
t "P5 tells the desktop user to log out and in" 0 "log out and back in as alice" p56 "OPT_DESKTOP_USER=alice; phase_users_dirs"
p56reset
t "P5 dry run output" 0 "[dry-run] checking with: aiagent setup dirs --dry-run" p56 "DRY_RUN=1; phase_users_dirs"
t "P5 dry run: the step got --dry-run" 0 "setup dirs --dry-run" grep -F "setup dirs" "$p56/aiagent.log"
t "P5 dry run: no log-out reminder (nothing changed)" 0 "" p56 "DRY_RUN=1; OPT_DESKTOP_USER=alice; ! phase_users_dirs | grep -q 'log out'"
t "P5 dry run with the program not installed yet is only described" 0 "[dry-run] would run: aiagent setup dirs" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$1/none"; export LAI_INSTALLER_DEV LAI_INSTALL_AIAGENT; source "$2"; DRY_RUN=1; phase_users_dirs' _ "$p56" "$INST"
t "P5 real run without the program -> LAI-INS-035" 1 "LAI-INS-035" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$1/none"; export LAI_INSTALLER_DEV LAI_INSTALL_AIAGENT; source "$2"; phase_users_dirs' _ "$p56" "$INST"
t "P5 failing step -> LAI-INS-035" 1 "LAI-INS-035" env FAKE_AGENT_RC=1 P56_LOG="$p56" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$1/aiagent"; export LAI_INSTALLER_DEV LAI_INSTALL_AIAGENT; source "$2"; phase_users_dirs' _ "$p56" "$INST"

echo "-- idempotency checks"
t "check_users_dirs: satisfied when --check says nothing to change" 0 "" p56 "check_users_dirs"
t "check_users_dirs: not satisfied when --check exits 1" 1 "" env FAKE_AGENT_RC=1 P56_LOG="$p56" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$1/aiagent"; export LAI_INSTALLER_DEV LAI_INSTALL_AIAGENT; source "$2"; check_users_dirs' _ "$p56" "$INST"
t "check_users_dirs: not satisfied without the program" 1 "" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$1/none"; export LAI_INSTALLER_DEV LAI_INSTALL_AIAGENT; source "$2"; check_users_dirs' _ "$p56" "$INST"
t "check_users_dirs asked the step with --check" 0 "--check" grep -F -- "--check" "$p56/aiagent.log"
t "check_packages: not satisfied when packages are missing" 1 "" env FAKE_INSTALLED=none PATH="$p56/dq:$PATH" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_OPT_DIR="$1/opt"; export LAI_INSTALLER_DEV LAI_INSTALL_OPT_DIR; source "$2"; check_packages' _ "$p56" "$INST"
t "check_packages: not satisfied without the companion programs" 1 "" env FAKE_INSTALLED=all PATH="$p56/dq:$PATH" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_OPT_DIR="$1/opt"; export LAI_INSTALLER_DEV LAI_INSTALL_OPT_DIR; source "$2"; check_packages' _ "$p56" "$INST"
for tool in kiwix-tools pmtiles piper; do mkdir -p "$p56/opt/$tool/1.0"; ln -sfn 1.0 "$p56/opt/$tool/current"; done
t "check_packages: satisfied when all is installed" 0 "" env FAKE_INSTALLED=all PATH="$p56/dq:$PATH" bash -c 'LAI_INSTALLER_DEV=1 LAI_INSTALL_OPT_DIR="$1/opt"; export LAI_INSTALLER_DEV LAI_INSTALL_OPT_DIR; source "$2"; check_packages' _ "$p56" "$INST"

echo "-- through the driver"
p56reset
# The whole driver would also run P7 and P8 (their own tests cover them), so P5 and P6 are run through run_phase.
drv() { env PATH="$p56/dq:$PATH" P56_LOG="$p56" FAKE_INSTALLED=all LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$p56/aiagent" LAI_INSTALL_AICTL="$p56/aictl" \
  LAI_INSTALL_OPT_DIR="$p56/opt" LAI_INSTALL_CATALOG_DIR="$p56/cat" bash -c '
    source "$1"; INSTALLER_VERSION=9.9.9-test; STATE_DIR="$2"; STATE_FILE="$2/state.json"; LOG="$3"; LOG_OPEN=1; : >>"$LOG"
    OPT_NODE_ID=ai-n4; OPT_BACKEND=cpu; OPT_NO_POLKIT=1; set -Eeuo pipefail
    mkdir -p "$STATE_DIR"; state_load
    run_phase 5 "${PHASE_TABLE[4]}"; run_phase 6 "${PHASE_TABLE[5]}"' _ "$STAMPED" "$w/p56state" "$w/p56.log"; }
drvout="$(drv 2>&1)"; rc=$?
t "driver: P5 and P6 run for real (fakes) and exit 0" 0 "" test $rc -eq 0
t "driver: P5 is done" 0 "phase P5 users, groups and directories: done" grep -F "phase P5 users, groups and directories: done" <<<"$drvout"
t "driver: P6 is done" 0 "phase P6 packages: done" grep -F "phase P6 packages: done" <<<"$drvout"
t "driver: state records P5 and P6 as done" 0 "" bash -c 'grep -E "\"P5\".*\"status\":\"done\"" "$1" && grep -E "\"P6\".*\"status\":\"done\"" "$1"' _ "$w/p56state/state.json"
t "driver: --no-polkit reached the step" 0 "--no-polkit" grep -F "setup dirs" "$p56/aiagent.log"
t "driver: a re-run skips both phases (checks pass)" 0 "already done" drv
