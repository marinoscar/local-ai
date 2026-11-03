#!/usr/bin/env bash
# test-release-node.sh: scripts/release/{build-src-tarball,stamp-installer,assemble-node-release,offline-build-check}.sh
# in a throwaway repository with a tiny Go module (one vendored dependency) and throwaway minisign keys that are
# generated here and never committed. Needs go, git, GNU tar and minisign; the offline-build part also needs root or
# passwordless sudo (`unshare -n`) and is skipped without it. Offline: no network is used.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2
SRC="$PWD"
for tool in go git minisign tar gzip; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool not installed: skipping release-node tests"; exit 0; }
done
tar --version | head -1 | grep -q 'GNU tar' || { echo "GNU tar not installed: skipping release-node tests"; exit 0; }
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -10; fi
}

w="$(mktemp -d)"; chmod 755 "$w"
trap 'rm -rf "$w"' EXIT
export HOME="$w/home" GOPATH="$w/gopath" GOCACHE="$w/gocache" TMPDIR="$w/tmp" GOPROXY=off GOTOOLCHAIN=local GIT_CONFIG_GLOBAL=/dev/null
mkdir -p "$HOME" "$TMPDIR"
unset GOFLAGS

gen() { printf '%s\n%s\n' "$3" "$3" | minisign -G -p "$1" -s "$2" -f >/dev/null 2>&1; }
gen "$w/a.pub" "$w/a.key" "pw-alpha-test"
gen "$w/b.pub" "$w/b.key" "pw-bravo-test"

# ---- the fixture repository ------------------------------------------------------------------------------------
repo="$w/repo"; mkdir -p "$repo/scripts/release" "$repo/installers" && cd "$repo" || exit 1
cp "$SRC"/scripts/check-redaction.sh "$SRC"/scripts/release-sign.sh scripts/
cp "$SRC"/scripts/release/*.sh scripts/release/
git init -q . && git config user.email t@example.test && git config user.name t
mkdir -p agent/cmd/aiagent agent/internal/app agent/dep
printf 'module example.test/dep\n\ngo 1.22\n' >agent/dep/go.mod
printf 'package dep\n\nfunc Name() string { return "dep" }\n' >agent/dep/dep.go
printf 'module example.test/agent\n\ngo 1.22\n\nrequire example.test/dep v0.0.0\n\nreplace example.test/dep => ./dep\n' >agent/go.mod
printf 'package app\n\nvar Version = "0.0.0-dev"\n' >agent/internal/app/version.go
cat >agent/cmd/aiagent/main.go <<'GO'
package main

import (
	"fmt"

	"example.test/agent/internal/app"
	"example.test/dep"
)

func main() { fmt.Printf("aiagent %s (%s)\n", app.Version, dep.Name()) }
GO
(cd agent && go mod vendor >/dev/null 2>&1)
cat >installers/install-node.sh <<'SH'
#!/usr/bin/env bash
# fixture installer: three tokens, and a script that detects "not stamped" with a split literal
LAI_RELEASE_TAG="@@LAI_RELEASE_TAG@@"
LAI_RELEASE_VERSION="@@LAI_RELEASE_VERSION@@"
LAI_MINISIGN_PUBKEY="@@LAI_MINISIGN_PUBKEY@@"
if [[ "$LAI_RELEASE_VERSION" == "@@LAI_RELEASE_VERSION""@@" ]]; then echo unstamped; exit 3; fi
case "${1:-}" in
  --version) echo "install-node $LAI_RELEASE_VERSION ($LAI_RELEASE_TAG)" ;;
  --print-key) echo "$LAI_MINISIGN_PUBKEY" ;;
esac
SH
chmod +x installers/install-node.sh scripts/release/*.sh scripts/*.sh
git add -A && git commit -qm "fixture" && git tag node-v0.0.1-rc.1
EPOCH="$(git log -1 --format=%ct)"
TAG=node-v0.0.1-rc.1; VER=0.0.1-rc.1
BUILD="scripts/release/build-src-tarball.sh"
mkdir -p "$w/out1" "$w/out2"
run() { ( cd "$repo" && "$@" ); }

echo "== build-src-tarball"
t "builds from the tag" 0 "local-ai-$TAG-src.tar.gz" run $BUILD --tag $TAG --out "$w/out1"
tb="$w/out1/local-ai-$TAG-src.tar.gz"
listing="$(tar -tzf "$tb" 2>/dev/null)"
t "carries the module, vendor/ and the vendored dependency" 0 "" bash -c 'grep -qx agent/go.mod <<<"$1" && grep -qx agent/vendor/modules.txt <<<"$1" && grep -qx agent/vendor/example.test/dep/dep.go <<<"$1"' _ "$listing"
t "no top-level folder (extracts into the build directory)" 0 "" bash -c '! grep -q "^local-ai" <<<"$1"' _ "$listing"
t "entries are sorted bytewise" 0 "" bash -c 'sed "s|/\$||" <<<"$1" | LC_ALL=C sort -c' _ "$listing"
vlist="$(tar -tzvf "$tb" 2>/dev/null)"
t "owner 0:0 and one fixed mtime for every entry" 0 "" bash -c 'awk "{print \$2}" <<<"$1" | sort -u | grep -qx 0/0 && [ "$(awk "{print \$4\" \"\$5}" <<<"$1" | sort -u | wc -l)" = 1 ]' _ "$vlist"
t "mtime is the commit time" 0 "$(date -u -d "@$EPOCH" +%Y-%m-%d)" bash -c 'echo "$1"' _ "$vlist"
t "modes are normalised (0644/0755 only)" 0 "" bash -c '[ -z "$(awk "{print \$1}" <<<"$1" | sort -u | grep -vxE "[-d]rw[-x]r-[-x]r-[-x]")" ]' _ "$vlist"
t "gzip header has no name or time" 0 "" bash -c '[ "$(od -An -tx1 -j4 -N4 "$1" | tr -d " ")" = 00000000 ]' _ "$tb"
sum1="$(sha256sum "$tb" | cut -d' ' -f1)"
t "building twice gives the same sha256" 0 "" bash -c '(cd "$1" && scripts/release/build-src-tarball.sh --tag "$2" --out "$3" >/dev/null) && [ "$(sha256sum "$3/local-ai-$2-src.tar.gz" | cut -d" " -f1)" = "$4" ]' _ "$repo" $TAG "$w/out2" "$sum1"
rm -rf "$w/out2"; mkdir "$w/out2"
t "a different umask gives the same sha256" 0 "" bash -c 'umask 077; (cd "$1" && scripts/release/build-src-tarball.sh --tag "$2" --out "$3" >/dev/null) && [ "$(sha256sum "$3/local-ai-$2-src.tar.gz" | cut -d" " -f1)" = "$4" ]' _ "$repo" $TAG "$w/out2" "$sum1"
rm -rf "$w/out2"; mkdir "$w/out2"
t "a fresh clone in another folder gives the same sha256" 0 "" bash -c 'git clone -q "$1" "$2/clone" && cd "$2/clone" && scripts/release/build-src-tarball.sh --tag "$3" --out "$2/o" >/dev/null && [ "$(sha256sum "$2/o/local-ai-$3-src.tar.gz" | cut -d" " -f1)" = "$4" ]' _ "$repo" "$w/out2" $TAG "$sum1"
t "SOURCE_DATE_EPOCH overrides the mtime" 0 "2001-09-09" bash -c 'cd "$1" && SOURCE_DATE_EPOCH=1000000000 scripts/release/build-src-tarball.sh --tag "$2" --out "$3" >/dev/null && tar -tzvf "$3/local-ai-$2-src.tar.gz" | head -1' _ "$repo" $TAG "$w/out2"
rm -rf "$w/out2"; mkdir "$w/out2"
t "a malformed tag is a usage error" 2 "not node-v<semver>" run $BUILD --tag v1.0.0 --out "$w/out2"
t "an unknown ref is a usage error" 2 "does not exist" run $BUILD --tag node-v9.9.9 --out "$w/out2"
t "a dry run takes --ref instead of the tag" 0 "local-ai-node-v9.9.9-src.tar.gz" run $BUILD --tag node-v9.9.9 --ref HEAD --out "$w/out2"

echo "== build-src-tarball: the checks that stop a bad package"
git -C "$repo" checkout -q -b stale
printf '// changed\n' >>"$repo/agent/vendor/example.test/dep/dep.go"
git -C "$repo" commit -qam "stale vendor"
t "a stale committed vendor/ is refused" 1 "is stale" run $BUILD --tag node-v9.9.9 --ref HEAD --out "$w/out2"
git -C "$repo" checkout -q -f - && git -C "$repo" branch -q -D stale

git -C "$repo" checkout -q -b leak
printf 'host %s\n' "$(printf '%s.%s' 192.168 1.9)" >"$repo/agent/notes.txt"
git -C "$repo" add -A && git -C "$repo" commit -qm "leak"
t "a redaction hit in the assembled tree is refused" 1 "redaction check failed" run $BUILD --tag node-v9.9.9 --ref HEAD --out "$w/out2"
git -C "$repo" checkout -q - && git -C "$repo" branch -q -D leak

git -C "$repo" checkout -q -b ui
mkdir -p "$repo/agent/ui" && printf '{"name":"ui"}\n' >"$repo/agent/ui/package.json"
git -C "$repo" add -A && git -C "$repo" commit -qm "ui project"
t "agent/ui without a built dist is refused" 1 "build it first" run $BUILD --tag node-v9.9.9 --ref HEAD --out "$w/out2"
mkdir -p "$w/dist/assets" && printf '<html></html>\n' >"$w/dist/index.html" && printf 'x\n' >"$w/dist/assets/app.js"
t "a missing dist folder is refused" 1 "missing or empty" run $BUILD --tag node-v9.9.9 --ref HEAD --out "$w/out2" --ui-dist "$w/emptyish"
mkdir -p "$w/emptyish" && printf 'x\n' >"$w/emptyish/a.js"
t "a dist without index.html is refused" 1 "no index.html" run $BUILD --tag node-v9.9.9 --ref HEAD --out "$w/out2" --ui-dist "$w/emptyish"
t "the built UI is packed" 0 "" run $BUILD --tag node-v9.9.9 --ref HEAD --out "$w/out2" --ui-dist "$w/dist"
t "agent/ui/dist/index.html is in the tarball" 0 "agent/ui/dist/index.html" bash -c 'tar -tzf "$1" | grep ui/dist/index.html' _ "$w/out2/local-ai-node-v9.9.9-src.tar.gz"
usum="$(sha256sum "$w/out2/local-ai-node-v9.9.9-src.tar.gz" | cut -d' ' -f1)"
run $BUILD --tag node-v9.9.9 --ref HEAD --out "$w/out2" --ui-dist "$w/dist" >/dev/null 2>&1
t "same UI build, same sha256" 0 "" test "$usum" = "$(sha256sum "$w/out2/local-ai-node-v9.9.9-src.tar.gz" | cut -d' ' -f1)"
git -C "$repo" checkout -q - && git -C "$repo" branch -q -D ui

echo "== stamp-installer"
STAMP="scripts/release/stamp-installer.sh"
t "stamps tag, version and key" 0 "stamped" run $STAMP --in installers/install-node.sh --out "$w/stamped.sh" --tag $TAG --pubkey "$w/a.pub"
t "the stamped installer reports the release version" 0 "install-node $VER ($TAG)" "$w/stamped.sh" --version
key="$(sed -n 2p "$w/a.pub")"
t "the stamped installer carries the key of the file" 0 "$key" "$w/stamped.sh" --print-key
t "the stamped installer is executable" 0 "" test -x "$w/stamped.sh"
t "the split-literal 'unstamped' check survives stamping" 0 "" grep -qF '@@LAI_RELEASE_VERSION""@@' "$w/stamped.sh"
t "no token is left" 0 "" bash -c '! grep -qE "@@LAI_[A-Z_]+@@" "$1"' _ "$w/stamped.sh"
t "the unstamped template says so" 3 "unstamped" "$repo/installers/install-node.sh"
sed 's/^LAI_MINISIGN_PUBKEY=.*$/LAI_MINISIGN_PUBKEY=""/' "$repo/installers/install-node.sh" >"$w/nokey.sh"
t "a template without the key token is refused" 1 "no @@LAI_MINISIGN_PUBKEY@@ token" run $STAMP --in "$w/nokey.sh" --out "$w/x.sh" --tag $TAG --pubkey "$w/a.pub"
{ cat "$repo/installers/install-node.sh"; echo 'echo @@LAI_SOMETHING_NEW@@'; } >"$w/unknown.sh"
t "an unknown token is refused" 1 "unknown @@LAI_" run $STAMP --in "$w/unknown.sh" --out "$w/x.sh" --tag $TAG --pubkey "$w/a.pub"
{ cat "$repo/installers/install-node.sh"; echo 'if then'; } >"$w/syntax.sh"
t "a stamped script with a syntax error is refused" 1 "syntax error" run $STAMP --in "$w/syntax.sh" --out "$w/x.sh" --tag $TAG --pubkey "$w/a.pub"
t "a missing public key explains the owner action" 2 "Owner action pending" run $STAMP --in installers/install-node.sh --out "$w/x.sh" --tag $TAG --pubkey "$w/nope.pub"
printf 'untrusted comment: minisign encrypted secret key\nRWRTY0Iy\n' >"$w/notpub"
t "a secret-key-looking file is refused" 2 "" run $STAMP --in installers/install-node.sh --out "$w/x.sh" --tag $TAG --pubkey "$w/notpub"
t "a malformed tag is refused" 2 "not node-v<semver>" run $STAMP --in installers/install-node.sh --out "$w/x.sh" --tag 1.2.3 --pubkey "$w/a.pub"
t "no output file is left after a refusal" 0 "" test ! -e "$w/x.sh"

echo "== assemble-node-release (the dry-run path, signing with a throwaway key)"
ASM="scripts/release/assemble-node-release.sh"
mkdir -p "$w/bin" && printf 'amd64 binary\n' >"$w/bin/aiagent-linux-amd64" && printf 'arm64 binary\n' >"$w/bin/aiagent-linux-arm64"
rel="$w/rel"
asm() { ( cd "$repo" && MINISIGN_SECRET_KEY="$(cat "$w/a.key")" MINISIGN_PASSWORD="pw-alpha-test" RELEASE_TAG=$TAG scripts/release/assemble-node-release.sh "$@" ); }
t "assembles, signs and verifies" 0 "signature valid" asm --tag $TAG --bin-dir "$w/bin" --tarball "$tb" --pubkey "$w/a.pub" --out "$rel" --sign
t "the six assets, nothing else" 0 "" bash -c '[ "$(ls -1 "$1" | tr "\n" " ")" = "SHA256SUMS SHA256SUMS.minisig aiagent-linux-amd64 aiagent-linux-arm64 install-node.sh local-ai-$2-src.tar.gz " ]' _ "$rel" $TAG
t "SHA256SUMS lists the binaries, the installer and the tarball" 0 "" bash -c '[ "$(awk "{print \$2}" "$1/SHA256SUMS" | tr "\n" " ")" = "aiagent-linux-amd64 aiagent-linux-arm64 install-node.sh local-ai-$2-src.tar.gz " ] && grep -qE "^[0-9a-f]{64}  aiagent-linux-amd64$" "$1/SHA256SUMS"' _ "$rel" $TAG
t "minisign -Vm SHA256SUMS passes with the public key" 0 "" minisign -Vm "$rel/SHA256SUMS" -p "$w/a.pub"
t "sha256sum -c SHA256SUMS passes" 0 "" bash -c 'cd "$1" && sha256sum -c SHA256SUMS'  _ "$rel"
mkdir -p "$w/consumer" && cp "$rel/SHA256SUMS" "$rel/aiagent-linux-arm64" "$w/consumer/"
t "a consumer with one file passes sha256sum -c --ignore-missing" 0 "" bash -c 'cd "$1" && sha256sum -c --ignore-missing SHA256SUMS'  _ "$w/consumer"
t "install-node.sh from the release reports the release version" 0 "install-node $VER ($TAG)" "$rel/install-node.sh" --version
t "install-node.sh carries the very key that verifies SHA256SUMS" 0 "$key" "$rel/install-node.sh" --print-key
t "the signed trusted comment names the tag" 0 "local-ai $TAG" cat "$rel/SHA256SUMS.minisig"
rm -rf "$w/rel2"
t "a secret that is not the committed key's pair fails here" 1 "SIGNATURE INVALID" bash -c 'cd "$1" && MINISIGN_SECRET_KEY="$(cat "$2/b.key")" MINISIGN_PASSWORD=pw-bravo-test scripts/release/assemble-node-release.sh --tag "$3" --bin-dir "$2/bin" --tarball "$4" --pubkey "$2/a.pub" --out "$2/rel2" --sign' _ "$repo" "$w" $TAG "$tb"
rm -rf "$w/rel3"
t "a missing installer is an error" 1 "must carry install-node.sh" run $ASM --tag $TAG --bin-dir "$w/bin" --tarball "$tb" --pubkey "$w/a.pub" --out "$w/rel3" --installer "$w/none.sh"
rm -rf "$w/rel3"
t "the dry run may ship five assets" 0 "no install-node.sh" run $ASM --tag $TAG --bin-dir "$w/bin" --tarball "$tb" --pubkey "$w/a.pub" --out "$w/rel3" --installer "$w/none.sh" --allow-missing-installer
rm -rf "$w/rel3"
t "a tarball named for another tag is refused" 2 "must be named" run $ASM --tag node-v9.9.9 --bin-dir "$w/bin" --tarball "$tb" --pubkey "$w/a.pub" --out "$w/rel3"
t "a missing binary is refused" 1 "aiagent-linux-arm64 not found" bash -c 'mkdir -p "$1/b" && cp "$1/bin/aiagent-linux-amd64" "$1/b/" && cd "$2" && scripts/release/assemble-node-release.sh --tag "$3" --bin-dir "$1/b" --tarball "$4" --pubkey "$1/a.pub" --out "$1/rel4"' _ "$w" "$repo" $TAG "$tb"
t "an existing SHA256SUMS is not overwritten" 2 "already holds" run $ASM --tag $TAG --bin-dir "$w/bin" --tarball "$tb" --pubkey "$w/a.pub" --out "$rel"

echo "== offline-build-check (unshare -n)"
OFF="scripts/release/offline-build-check.sh"
if [[ "$(id -u)" -eq 0 ]] || { command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; }; then
  t "builds offline and reports the version" 0 "reports $VER" run $OFF --tarball "$tb" --version $VER
    mkdir -p "$w/nostamp" && tar -xzf "$tb" -C "$w/nostamp" && sed -i 's/^var Version/const Version/' "$w/nostamp/agent/internal/app/version.go"
  (cd "$w/nostamp" && tar -czf "$w/nostamp.tar.gz" .)
  t "a build that ignores the version stamp fails the version check" 1 "expected '$VER'" run $OFF --tarball "$w/nostamp.tar.gz" --version $VER
  mkdir -p "$w/broken" && tar -xzf "$tb" -C "$w/broken" && rm -rf "$w/broken/agent/vendor/example.test"
  (cd "$w/broken" && tar -czf "$w/broken.tar.gz" .)
  t "a module that is not vendored fails with LAI-KIT-030" 1 "LAI-KIT-030" run $OFF --tarball "$w/broken.tar.gz" --version $VER
else
  echo "  skip offline build (needs root or passwordless sudo for unshare -n)"
  t "without root the script says why" 2 "needs root or passwordless sudo" run $OFF --tarball "$tb" --version $VER
fi
t "a version with the node-v prefix is refused" 2 "no node-v prefix" run $OFF --tarball "$tb" --version node-v$VER
t "a missing tarball is refused" 2 "not found" run $OFF --tarball "$w/none.tar.gz" --version $VER

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
