#!/usr/bin/env bash
# test-desktop-release.sh: the desktop release tooling (DSK-12, docs/specs/desktop.md section 4.11) with throwaway minisign
# keys generated here and never committed, fake assets built here, and no network:
#   scripts/release/check-desktop-tag.sh        tag vs apps/desktop/package.json
#   scripts/release/assemble-desktop-release.sh SHA256SUMS + minisign, latest-linux.yml consistency
#   scripts/release/desktop-release-notes.sh    release notes
#   scripts/release/desktop-kit-snippet.sh      the kit-desktop catalog item, read from a verified download
#   scripts/release/check-deb-installs.sh       apt can install the deb on every node Ubuntu (a fake docker here)
#   .github/workflows/*.yml                     every workflow parses; desktop-release.yml keeps its safety properties
#   .github/PULL_REQUEST_TEMPLATE/kit-desktop-bump.md
# Needs bash, openssl, python3 with PyYAML (the lint part is skipped with a message when PyYAML is missing) and minisign.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2
ROOT="$PWD"
for tool in minisign openssl sha256sum python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool not installed: skipping desktop-release tests"; exit 0; }
done
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -10; fi
}
w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT
export TMPDIR="$w/tmp"; mkdir -p "$TMPDIR"

TAGCHK="$ROOT/scripts/release/check-desktop-tag.sh"
ASM="$ROOT/scripts/release/assemble-desktop-release.sh"
NOTES="$ROOT/scripts/release/desktop-release-notes.sh"
SNIP="$ROOT/scripts/release/desktop-kit-snippet.sh"

# ---- check-desktop-tag.sh ---------------------------------------------------------------------------------------
echo "== check-desktop-tag"
pkg() { printf '{\n  "name": "ai-desktop",\n  "version": "%s",\n  "dependencies": {\n    "version": "9.9.9"\n  }\n}\n' "$1" >"$w/package.json"; }
pkg 0.1.0
t "a matching tag passes and prints tag, version and an empty prerelease" 0 "version=0.1.0" "$TAGCHK" --tag desktop-v0.1.0 --package "$w/package.json"
t "a stable version is not a pre-release" 0 "" bash -c "'$TAGCHK' --tag desktop-v0.1.0 --package '$w/package.json' | grep -qx 'prerelease='"
t "a tag that differs from package.json fails" 1 "does not match apps/desktop/package.json" "$TAGCHK" --tag desktop-v0.1.1 --package "$w/package.json"
t "the failure tells the owner to use a new tag" 1 "tag again with a new tag" "$TAGCHK" --tag desktop-v0.2.0 --package "$w/package.json"
t "a tag of another product fails" 1 "does not start with desktop-v" "$TAGCHK" --tag node-v0.1.0 --package "$w/package.json"
t "a tag without the v fails" 1 "does not start with desktop-v" "$TAGCHK" --tag desktop-0.1.0 --package "$w/package.json"
t "a nested \"version\" key is not taken for the package version" 1 "does not match" "$TAGCHK" --tag desktop-v9.9.9 --package "$w/package.json"
pkg 0.1.0-rc.1
t "a pre-release tag matches" 0 "prerelease=--prerelease" "$TAGCHK" --tag desktop-v0.1.0-rc.1 --package "$w/package.json"
t "the release candidate does not match the stable tag" 1 "does not match" "$TAGCHK" --tag desktop-v0.1.0 --package "$w/package.json"
t "--dry-run derives the tag from package.json" 0 "tag=desktop-v0.1.0-rc.1" "$TAGCHK" --dry-run --package "$w/package.json"
pkg 1.2
t "a package.json version that is not a semver fails" 1 "not a semver" "$TAGCHK" --tag desktop-v1.2 --package "$w/package.json"
pkg 0.1.0
t "--tag and --dry-run together are a usage error" 2 "" "$TAGCHK" --tag desktop-v0.1.0 --dry-run --package "$w/package.json"
t "neither is a usage error" 2 "" "$TAGCHK" --package "$w/package.json"
t "a missing package.json is an environment error" 2 "not found" "$TAGCHK" --tag desktop-v0.1.0 --package "$w/none.json"
t "the real package.json has a top-level version" 0 "version=" "$TAGCHK" --dry-run

# ---- fixtures: fake assets with a consistent latest-linux.yml and throwaway keys --------------------------------------
VER=0.1.0-rc.1; TAG=desktop-v$VER
DEB="ai-desktop_${VER}_amd64.deb"; APP="ai-desktop-${VER}-x86_64.AppImage"
sha512b64() { openssl dgst -sha512 -binary "$1" | base64 -w0; }
mkbuild() { # mkbuild <dir> [version-in-yml]
  local d="$1" yv="${2:-$VER}"
  mkdir -p "$d"
  head -c 3000 /dev/urandom >"$d/$DEB"; head -c 5000 /dev/urandom >"$d/$APP"
  {
    echo "version: $yv"
    echo "files:"
    echo "  - url: $APP"; echo "    sha512: $(sha512b64 "$d/$APP")"; echo "    size: $(wc -c <"$d/$APP")"; echo "    blockMapSize: 133044"
    echo "  - url: $DEB"; echo "    sha512: $(sha512b64 "$d/$DEB")"; echo "    size: $(wc -c <"$d/$DEB")"
    echo "path: $APP"; echo "sha512: $(sha512b64 "$d/$APP")"; echo "releaseDate: '2026-01-01T00:00:00.000Z'"
  } >"$d/latest-linux.yml"
}
gen() { printf '%s\n%s\n' "$3" "$3" | minisign -G -p "$1" -s "$2" -f >/dev/null 2>&1; }
pwa="$(openssl rand -hex 8)"; pwb="$(openssl rand -hex 8)"
gen "$w/a.pub" "$w/a.sec" "$pwa"; gen "$w/b.pub" "$w/b.sec" "$pwb"
mkbuild "$w/build"
asm() { MINISIGN_SECRET_KEY="$(cat "$w/a.sec")" MINISIGN_PASSWORD="$pwa" RELEASE_TAG="$TAG" "$ASM" "$@"; }

# ---- assemble-desktop-release.sh --------------------------------------------------------------------------------------
echo "== assemble-desktop-release"
t "signs and verifies against the matching public key" 0 "signature valid" asm --tag $TAG --dir "$w/build" --pubkey "$w/a.pub" --out "$w/out" --sign
t "the folder has exactly the five release assets" 0 "" bash -c "[ \"\$(cd '$w/out' && ls -1 | LC_ALL=C sort | tr '\n' ' ')\" = 'SHA256SUMS SHA256SUMS.minisig $APP $DEB latest-linux.yml ' ] || [ \"\$(cd '$w/out' && ls -1 | sort | tr '\n' ' ')\" = \"\$(printf '%s\n' SHA256SUMS SHA256SUMS.minisig $APP $DEB latest-linux.yml | sort | tr '\n' ' ')\" ]"
t "SHA256SUMS lists the three payload files and not itself" 0 "" bash -c "[ \$(wc -l <'$w/out/SHA256SUMS') = 3 ] && grep -q ' $DEB\$' '$w/out/SHA256SUMS' && grep -q ' $APP\$' '$w/out/SHA256SUMS' && grep -q ' latest-linux.yml\$' '$w/out/SHA256SUMS' && ! grep -q SHA256SUMS '$w/out/SHA256SUMS'"
t "minisign -V and sha256sum -c pass on the result" 0 "" bash -c "cd '$w/out' && minisign -Vm SHA256SUMS -p '$w/a.pub' >/dev/null && sha256sum --quiet -c SHA256SUMS"
t "a different public key does not verify the result" 1 "" minisign -Vm "$w/out/SHA256SUMS" -x "$w/out/SHA256SUMS.minisig" -p "$w/b.pub"
t "the signed trusted comment names the tag" 0 "$TAG" bash -c "cat '$w/out/SHA256SUMS.minisig'"
t "the output never contains the password or the secret key" 0 "" bash -c "! grep -rqE '$pwa|untrusted comment: minisign encrypted' '$w/out'"
t "an existing SHA256SUMS in the output folder is refused" 2 "already holds" asm --tag $TAG --dir "$w/build" --pubkey "$w/a.pub" --out "$w/out"
t "a secret that is not the committed key's pair fails at assembly time" 1 "SIGNATURE INVALID" asm --tag $TAG --dir "$w/build" --pubkey "$w/b.pub" --out "$w/out2" --sign
t "without --sign nothing is signed" 0 "" bash -c "'$ASM' --tag $TAG --dir '$w/build' --pubkey '$w/a.pub' --out '$w/out3' >/dev/null && [ -f '$w/out3/SHA256SUMS' ] && [ ! -e '$w/out3/SHA256SUMS.minisig' ]"
t "a tag that is not desktop-v<semver> is refused" 2 "not desktop-v" "$ASM" --tag node-v0.1.0 --dir "$w/build" --pubkey "$w/a.pub" --out "$w/o4"
t "a missing public key is refused with the owner action" 2 "Owner action pending" "$ASM" --tag $TAG --dir "$w/build" --pubkey "$w/none.pub" --out "$w/o4"
mkbuild "$w/stale-ver" 0.0.9
for f in "$DEB" "$APP"; do cp "$w/build/$f" "$w/stale-ver/$f"; done
t "a latest-linux.yml for another version is refused" 1 "says version '0.0.9'" "$ASM" --tag $TAG --dir "$w/stale-ver" --pubkey "$w/a.pub" --out "$w/o5"
cp -r "$w/build" "$w/rebuilt"; printf 'x' >>"$w/rebuilt/$DEB"
t "a deb rebuilt after the yml was written is refused" 1 "different sha512 for $DEB" "$ASM" --tag $TAG --dir "$w/rebuilt" --pubkey "$w/a.pub" --out "$w/o6"
cp -r "$w/build" "$w/nodeb"; rm "$w/nodeb/$DEB"
t "a missing deb is refused" 1 "not found" "$ASM" --tag $TAG --dir "$w/nodeb" --pubkey "$w/a.pub" --out "$w/o7"
cp -r "$w/build" "$w/noyml"; rm "$w/noyml/latest-linux.yml"
t "a missing latest-linux.yml is refused" 1 "not found" "$ASM" --tag $TAG --dir "$w/noyml" --pubkey "$w/a.pub" --out "$w/o8"
cp -r "$w/build" "$w/oldname"; mv "$w/oldname/$APP" "$w/oldname/ai-desktop-$VER.AppImage"
t "an AppImage with another name than electron-builder's is refused" 1 "not found" "$ASM" --tag $TAG --dir "$w/oldname" --pubkey "$w/a.pub" --out "$w/o9"

# ---- desktop-release-notes.sh -----------------------------------------------------------------------------------------
echo "== desktop-release-notes"
notes="$("$NOTES" --tag $TAG --dir "$w/out")"
has() { grep -qF -- "$2" <<<"$1"; }
t "a pre-release says so" 0 "" has "$notes" "Pre-release"
t "the notes show the install command" 0 "" has "$notes" "sudo apt install ./$DEB"
t "the notes show the offline kit install command" 0 "" has "$notes" "sudo apt install <root>/kit/desktop/$DEB"
t "the notes show minisign and sha256sum checks" 0 "" bash -c 'grep -qF "minisign -Vm SHA256SUMS" <<<"$1" && grep -qF "sha256sum -c SHA256SUMS" <<<"$1" && grep -qF "aictl verify-release" <<<"$1"' _ "$notes"
deb_sum="$(sha256sum "$w/out/$DEB" | cut -d' ' -f1)"
row="| \`$DEB\` | 3000 | \`$deb_sum\` |"
t "the notes list each asset with its size and sha256" 0 "" has "$notes" "$row"
stable="$w/stable-out"; mkdir -p "$stable"
head -c 10 /dev/urandom >"$stable/ai-desktop_0.2.0_amd64.deb"; head -c 10 /dev/urandom >"$stable/ai-desktop-0.2.0-x86_64.AppImage"; echo v >"$stable/latest-linux.yml"
(cd "$stable" && sha256sum ai-desktop_0.2.0_amd64.deb ai-desktop-0.2.0-x86_64.AppImage latest-linux.yml >SHA256SUMS)
stable_notes="$("$NOTES" --tag desktop-v0.2.0 --dir "$stable")"
t "the notes of a stable release have no pre-release line" 1 "" has "$stable_notes" "Pre-release"
t "and still have the title" 0 "" has "$stable_notes" "# ai-desktop 0.2.0"
t "notes need the assembled folder" 1 "not found" "$NOTES" --tag $TAG --dir "$w/build"
t "notes refuse a tag of another product" 2 "not desktop-v" "$NOTES" --tag node-v0.1.0 --dir "$w/out"

# ---- desktop-kit-snippet.sh -------------------------------------------------------------------------------------------
echo "== desktop-kit-snippet"
t "verifies with the public key and prints the kit-desktop item" 0 "id: kit-desktop" "$SNIP" --tag $TAG --dir "$w/out" --pubkey "$w/a.pub"
snippet="$("$SNIP" --tag $TAG --dir "$w/out" --pubkey "$w/a.pub" 2>/dev/null)"
t "the snippet is valid YAML with the tag, both assets, the sha256 of each and a size" 0 "" env SNIPPET="$snippet" DEB="$DEB" APP="$APP" TAG="$TAG" DEBSUM="$deb_sum" python3 -c '
import os, sys, yaml
item = yaml.safe_load(os.environ["SNIPPET"])[0]
files = item["source"]["files"]
assert item["id"] == "kit-desktop" and item["kind"] == "kit" and item["source"]["type"] == "url", item
assert item["source"]["version_policy"] == "pinned" and item["source"]["checksum"] == "pinned"
assert [f["url"].rsplit("/", 1)[1] for f in files] == [os.environ["DEB"], os.environ["APP"]]
assert all("/releases/download/" + os.environ["TAG"] + "/" in f["url"] for f in files)
assert files[0]["sha256"] == os.environ["DEBSUM"] and len(files[1]["sha256"]) == 64
assert item["expected_size_bytes"] >= 8000 and item["expected_size_bytes"] % 1000000 == 0
'
t "without --pubkey it warns that the signature was not checked" 0 "NOT checked" "$SNIP" --tag $TAG --dir "$w/out"
cp -r "$w/out" "$w/tampered"; printf 'x' >>"$w/tampered/$DEB"
out="$("$SNIP" --tag $TAG --dir "$w/tampered" --pubkey "$w/a.pub" 2>/dev/null)"; rc=$?
t "a tampered download prints nothing and fails" 0 "" bash -c "[ $rc -eq 1 ] && [ -z \"\$1\" ]" _ "$out"
out="$("$SNIP" --tag $TAG --dir "$w/out" --pubkey "$w/b.pub" 2>/dev/null)"; rc=$?
t "another key prints nothing and fails" 0 "" bash -c "[ $rc -eq 1 ] && [ -z \"\$1\" ]" _ "$out"
out="$("$SNIP" --tag $TAG --dir "$w/tampered" 2>/dev/null)"; rc=$?
t "without a key a file that does not match SHA256SUMS still prints nothing" 0 "" bash -c "[ $rc -eq 1 ] && [ -z \"\$1\" ]" _ "$out"

# ---- the PR template --------------------------------------------------------------------------------------------------
echo "== kit-desktop-bump template"
TPL=.github/PULL_REQUEST_TEMPLATE/kit-desktop-bump.md
t "the template exists" 0 "" test -s "$TPL"
for word in "Tag" "Asset" "sha256" "Size" "desktop-kit-snippet.sh" "kit-desktop" "catalog lint" "pubkey"; do
  t "the template mentions: $word" 0 "" grep -qi -- "$word" "$TPL"
done
t "the template has no 64-hex string (a pin comes from the script, never from the template)" 1 "" grep -E '[0-9a-f]{64}' "$TPL"

# ---- check-deb-installs.sh (a fake docker on PATH: exit codes and messages, no containers, no network) ----------------
echo "== check-deb-installs"
DEBCHK="$ROOT/scripts/release/check-deb-installs.sh"
mkdir -p "$w/fakebin" "$w/debs"; : >"$w/debs/ai-desktop_9.9.9_amd64.deb"
# The fake docker answers per image: ubuntu:ok installs, ubuntu:bad is missing a dependency, ubuntu:nomirror has no lists.
cat >"$w/fakebin/docker" <<'FAKE'
#!/usr/bin/env bash
for a in "$@"; do case "$a" in
  ubuntu:ok) echo "Inst ai-desktop"; exit 0 ;;
  ubuntu:bad) echo " ai-desktop : Depends: policykit-1 but it is not installable"; echo "E: Unable to correct problems"; exit 100 ;;
  ubuntu:nomirror) exit 3 ;;
esac; done
exit 99
FAKE
chmod +x "$w/fakebin/docker"
FD="PATH=$w/fakebin:$PATH"
t "no arguments is a usage error" 2 "" "$DEBCHK"
t "a file that is not a .deb is refused" 2 "is not a .deb" env "$FD" "$DEBCHK" "$w/debs/missing.deb"
t "installable on every image passes" 0 "ok   ubuntu:ok" env "$FD" "$DEBCHK" "$w/debs/ai-desktop_9.9.9_amd64.deb" ubuntu:ok ubuntu:ok
t "a dependency apt cannot satisfy fails and shows apt's reason" 1 "Depends: policykit-1" env "$FD" "$DEBCHK" "$w/debs/ai-desktop_9.9.9_amd64.deb" ubuntu:ok ubuntu:bad
t "an unreachable mirror is an environment error, not a pass" 2 "mirror unreachable" env "$FD" "$DEBCHK" "$w/debs/ai-desktop_9.9.9_amd64.deb" ubuntu:ok ubuntu:nomirror
t "a real failure outranks an unreachable mirror" 1 "FAIL ubuntu:bad" env "$FD" "$DEBCHK" "$w/debs/ai-desktop_9.9.9_amd64.deb" ubuntu:nomirror ubuntu:bad
t "the default images are the node Ubuntu releases" 0 "" grep -qx 'DEFAULT_IMAGES=(ubuntu:24.04 ubuntu:26.04)' "$DEBCHK"
t "the deb depends on pkexec, with policykit-1 only as a fallback" 0 "" grep -qF "'pkexec | policykit-1'" "$ROOT/apps/desktop/electron-builder.yml"

# ---- workflow lint ----------------------------------------------------------------------------------------------------
echo "== workflow lint"
if python3 -c 'import yaml' 2>/dev/null; then
  t "every workflow and issue/PR template YAML parses (a colon-space in a step name has broken one before)" 0 "" python3 - <<'PY'
import glob, sys, yaml
bad = []
for path in sorted(glob.glob(".github/workflows/*.y*ml") + glob.glob(".github/ISSUE_TEMPLATE/*.y*ml")):
    try:
        data = yaml.safe_load(open(path))
    except Exception as e:
        bad.append(f"{path}: {e}")
        continue
    if not isinstance(data, dict):
        bad.append(f"{path}: not a mapping")
    elif path.startswith(".github/workflows/"):
        for name, job in (data.get("jobs") or {}).items():
            for i, step in enumerate(job.get("steps") or []):
                if not isinstance(step, dict) or not ("uses" in step or "run" in step):
                    bad.append(f"{path}: job {name} step {i + 1} has neither uses nor run")
if bad:
    print("\n".join(bad)); sys.exit(1)
PY
  t "desktop-release.yml keeps its safety properties" 0 "" python3 - <<'PY'
import re, sys, yaml
path = ".github/workflows/desktop-release.yml"
text = open(path).read()
wf = yaml.safe_load(text)
on = wf.get("on", wf.get(True))
errors = []
def need(cond, msg):
    if not cond: errors.append(msg)
need(on["push"]["tags"] == ["desktop-v*"], "must trigger on tags desktop-v* only")
need("workflow_dispatch" in on, "must have workflow_dispatch (the dry run)")
need("branches" not in on["push"], "must not trigger on branch pushes")
jobs = wf["jobs"]
need(set(jobs) == {"prepare", "build", "package-dry-run", "release"}, f"unexpected jobs {sorted(jobs)}")
# the signing secrets are exposed only to the release job
for name, job in jobs.items():
    body = yaml.dump(job)
    if name != "release":
        need("secrets." not in body, f"job {name} reads secrets")
        for line in body.splitlines():
            if "MINISIGN_SECRET_KEY=" in line:
                need("dry.key" in line, f"job {name} feeds MINISIGN_SECRET_KEY from something other than the throwaway key")
rel = jobs["release"]
need(rel.get("environment") == "release", "release job must use the `release` environment")
need("workflow_dispatch" not in str(rel.get("if")) and "push" in str(rel.get("if")), "release job must run for tag pushes only")
need("dry_run == 'false'" in str(rel.get("if")), "release job must be off for dry runs")
need(rel.get("permissions", {}).get("contents") == "write", "release job needs contents: write")
need(wf["permissions"] == {"contents": "read"}, "top-level permissions must be read-only")
for name in ("prepare", "build", "package-dry-run"):
    need("permissions" not in jobs[name] or jobs[name]["permissions"].get("contents") != "write", f"job {name} must not write")
need(jobs["package-dry-run"].get("if") == "needs.prepare.outputs.dry_run == 'true'", "dry-run job condition")
# nothing publishes outside the release job, and nothing pushes or moves a tag
for name, job in jobs.items():
    if name == "release": continue
    for step in job.get("steps", []):
        run = step.get("run", "")
        need("gh release" not in run and "gh api -X" not in run, f"job {name} publishes")
need(not re.search(r"git (push|tag)\b", text), "the workflow must never push or create a tag")
need("--force" not in text and "force-with-lease" not in text, "no forced operations")
steps = [s for s in jobs["release"]["steps"] if "run" in s]
runs = "\n".join(s["run"] for s in steps)
need("--draft" in runs and "draft=false" in runs, "release must be created as a draft and then published")
need("--prerelease" in runs or "PRERELEASE" in runs, "release must carry the prerelease flag")
need("--latest=false" in runs, "a desktop release must not become the repository's latest release")
need("release-sign.sh verify" in runs, "the published assets must be verified")
need("check-desktop-tag.sh" in "\n".join(s.get("run", "") for s in jobs["prepare"]["steps"]), "prepare must run the tag check")
# every action is pinned to a commit SHA
for name, job in jobs.items():
    for step in job.get("steps", []):
        use = step.get("uses")
        if use: need(re.search(r"@[0-9a-f]{40}$", use), f"{use} is not pinned to a SHA")
# the build keeps the steps of desktop-ci
ci = yaml.safe_load(open(".github/workflows/desktop-ci.yml"))
ci_runs = "\n".join(s.get("run", "") for s in ci["jobs"]["desktop"]["steps"])
rel_runs = "\n".join(s.get("run", "") for s in jobs["build"]["steps"])
for cmd in ("npm ci", "node node_modules/electron/install.js", "npm run typecheck", "npm test", "npm run build", "node scripts/dist.mjs --linux --x64", "node scripts/packaged-app.mjs verify", "playwright test", "dpkg -I", "dpkg-deb -c", "check-deb-installs.sh"):
    need(cmd in ci_runs, f"desktop-ci no longer has `{cmd}` (update this test and desktop-release.yml together)")
    need(cmd in rel_runs, f"the release build lacks `{cmd}` that desktop-ci runs")
need("apt-get install" in rel_runs and "xvfb-run" in rel_runs and "test-copy" in rel_runs, "the release build installs the deb and runs the packaged e2e under xvfb")
if errors:
    print("\n".join(errors)); sys.exit(1)
PY
else
  echo "  skip workflow lint (python3 has no PyYAML)"
fi

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
