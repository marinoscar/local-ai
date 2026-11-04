# test-installer-p5p6-fixtures.sh: fakes for P5/P6 (INST-04, issue #149). SOURCED early by test-installer.sh (needs
# $w). It puts a fake apt-get, aiagent and aictl in front of the real ones for every later test run, so no test can
# reach the apt, aiagent or aictl of the machine it runs on. No root, no systemd, no network.

p56="$w/p56"; mkdir -p "$p56/bin" "$p56/dq" "$p56/opt" "$p56/cat"
# Fake programs. Each logs its arguments; a *_RC variable makes it fail.
cat >"$p56/aiagent" <<'EOF'
#!/bin/sh
echo "$*" >>"$P56_LOG/aiagent.log"
echo "fake aiagent: $*"
exit "${FAKE_AGENT_RC:-0}"
EOF
cat >"$p56/aictl" <<'EOF'
#!/bin/sh
echo "$*" >>"$P56_LOG/aictl.log"
echo "fake aictl: $*"
exit "${FAKE_AICTL_RC:-0}"
EOF
cat >"$p56/bin/apt-get" <<'EOF'
#!/bin/sh
echo "$*" >>"$P56_LOG/apt.log"
case "$*" in
  -s*) printf 'Inst fake-a (1 x)\nInst fake-b (2 x)\n'; exit 0 ;;
esac
if [ -n "$FAKE_APT_FAIL" ]; then
  i=1; while [ $i -le 30 ]; do echo "apt noise line $i"; i=$((i + 1)); done
  echo "E: Unable to locate package fake" >&2
  exit 100
fi
echo "apt ok: $*"
EOF
cat >"$p56/dq/dpkg-query" <<'EOF'
#!/bin/sh
for last; do :; done
case "${FAKE_INSTALLED:-none}" in
  all) printf 'install ok installed'; exit 0 ;;
  *) exit 1 ;;
esac
EOF
chmod +x "$p56/aiagent" "$p56/aictl" "$p56/bin/apt-get" "$p56/dq/dpkg-query"
export P56_LOG="$p56"
# From here on every test run inherits the fakes, so no test can reach the real apt, aiagent or aictl of the machine.
export LAI_INSTALL_AIAGENT="$p56/aiagent" LAI_INSTALL_AICTL="$p56/aictl" LAI_INSTALL_OPT_DIR="$p56/opt" LAI_INSTALL_CATALOG_DIR="$p56/cat"
export PATH="$p56/bin:$PATH"
p56reset() { rm -f "$p56/aiagent.log" "$p56/aictl.log" "$p56/apt.log"; }
# p56 <snippet>: the installer sourced with dev hooks and the fakes first on PATH.
p56() {
  env PATH="$p56/dq:$PATH" LAI_INSTALLER_DEV=1 LAI_INSTALL_AIAGENT="$p56/aiagent" LAI_INSTALL_AICTL="$p56/aictl" \
    LAI_INSTALL_OPT_DIR="$p56/opt" LAI_INSTALL_CATALOG_DIR="$p56/cat" P56_LOG="$p56" \
    bash -c 'source "$1"; eval "$2"' _ "$INST" "$1"
}
loglines() { cat "$p56/$1.log" 2>/dev/null; }
