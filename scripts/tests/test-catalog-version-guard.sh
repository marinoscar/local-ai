#!/usr/bin/env bash
# test-catalog-version-guard.sh: scripts/catalog-version-guard.sh and scripts/catalog-tag.sh against
# throwaway git repositories (a bare "remote" and a clone), never the real repository and no network.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
GUARD="$PWD/scripts/catalog-version-guard.sh"
TAG="$PWD/scripts/catalog-tag.sh"
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...   (fragment must appear in stdout+stderr)
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -8; fi
}

w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT
export HOME="$w/home"; mkdir -p "$HOME"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME="Person A" GIT_AUTHOR_EMAIL="person.a@example.test" GIT_COMMITTER_NAME="Person A" GIT_COMMITTER_EMAIL="person.a@example.test"

# newrepo <dir>: a repository with one commit and a catalog/CATALOG_VERSION
newrepo() {
  git init -q -b main "$1"
  mkdir -p "$1/catalog"; printf '2026.10.01-1\n' >"$1/catalog/CATALOG_VERSION"
  git -C "$1" add -A; git -C "$1" commit -q -m "catalog"
}
# tagsfor <dir> <tag>...: lightweight tags on HEAD
tagsfor() { local d="$1"; shift; for x in "$@"; do git -C "$d" tag "$x"; done; }
setv() { printf '%s' "$2" >"$1/catalog/CATALOG_VERSION"; } # setv <dir> <exact content>

echo "== guard: accepted versions"
r="$w/r1"; newrepo "$r"
t "no catalog tag yet" 0 "latest existing: none" "$GUARD" --repo "$r"
tagsfor "$r" catalog-2026.11.02-1 catalog-stable
setv "$r" $'2026.11.02-2\n'
t "same day, higher N" 0 "catalog-2026.11.02-2 may be tagged" "$GUARD" --repo "$r"
setv "$r" $'2026.11.03-1\n'
t "later day, lower N" 0 "" "$GUARD" --repo "$r"
setv "$r" $'2026.11.03-1\r\n'
t "a CRLF line ending is accepted (the linter accepts it)" 0 "" "$GUARD" --repo "$r"
setv "$r" '2026.11.03-1'
t "no trailing newline" 0 "" "$GUARD" --repo "$r"
t "--version overrides the file" 0 "catalog-2027.01.01-1" "$GUARD" --repo "$r" --version 2027.01.01-1
t "--file reads another file" 0 "catalog-2026.12.01-1" bash -c "printf '2026.12.01-1\n' >'$w/v.txt'; '$GUARD' --repo '$r' --file '$w/v.txt'"
r2="$w/r2"; newrepo "$r2"; tagsfor "$r2" catalog-2026.11.02-9
t "N is numeric: 10 is greater than 9" 0 "" "$GUARD" --repo "$r2" --version 2026.11.02-10
r3="$w/r3"; newrepo "$r3"; tagsfor "$r3" catalog-2026.11.09-3 catalog-2026.01.31-12
t "day 10 is greater than day 09" 0 "" "$GUARD" --repo "$r3" --version 2026.11.10-1
t "the year wins over month and day" 0 "" "$GUARD" --repo "$r3" --version 2027.01.01-1
t "a leap day is a real date" 0 "" "$GUARD" --repo "$r3" --version 2028.02.29-1

echo "== guard: refused versions"
r4="$w/r4"; newrepo "$r4"; tagsfor "$r4" catalog-2026.11.02-1 catalog-2026.11.03-2 catalog-stable
t "the tag already exists" 1 "catalog-2026.11.03-2 already exists" "$GUARD" --repo "$r4" --version 2026.11.03-2
t "an older tag already exists" 1 "already exists" "$GUARD" --repo "$r4" --version 2026.11.02-1
t "lower than the latest tag, and says which" 1 "not greater than the latest tag catalog-2026.11.03-2" "$GUARD" --repo "$r4" --version 2026.11.02-5
t "lower N the same day" 1 "not greater" "$GUARD" --repo "$r4" --version 2026.11.03-1
t "an older year" 1 "not greater" "$GUARD" --repo "$r4" --version 2025.12.31-9
# GITHUB_ACTIONS is cleared: under Actions the guard also prints a ::error:: annotation on stdout
# (the next case), and the CI runner sets it for this script.
t "the error goes to stderr, nothing to stdout" 0 "" env -u GITHUB_ACTIONS bash -c "[ -z \"\$('$GUARD' --repo '$r4' --version 2026.11.03-2 2>/dev/null)\" ]"
t "a workflow annotation in Actions" 1 "::error title=catalog version guard::" env GITHUB_ACTIONS=true "$GUARD" --repo "$r4" --version 2026.11.03-2
r5="$w/r5"; newrepo "$r5"; tagsfor "$r5" catalog-2026.11.02-9
t "9 is not greater than 9 (equal: the tag exists)" 1 "already exists" "$GUARD" --repo "$r5" --version 2026.11.02-9
t "8 is not greater than 9" 1 "not greater" "$GUARD" --repo "$r5" --version 2026.11.02-8

echo "== guard: tags that are not catalog releases"
r6="$w/r6"; newrepo "$r6"; tagsfor "$r6" catalog-stable catalog-notes catalog-2026.11.9-3 catalog-2030.01.01-0 catalog-2026.02.30-1 catalog-2026.11.02-1-rc1 node-v1.0.0 catalog-2026.11.02-1
out="$("$GUARD" --repo "$r6" --version 2026.11.02-2 2>&1)"; rc=$?
t "stray tags do not block a valid version" 0 "" test $rc -eq 0
t "each stray tag is warned about" 0 "" bash -c 'for x in catalog-notes catalog-2026.11.9-3 catalog-2030.01.01-0 catalog-2026.02.30-1 catalog-2026.11.02-1-rc1; do grep -qF "ignoring tag '"'"'$x'"'"'" <<<"$1" || exit 1; done' _ "$out"
t "catalog-stable and a foreign tag are not warned about" 0 "" bash -c '! grep -qE "catalog-stable|node-v1" <<<"$1"' _ "$out"
t "a stray tag with a higher look does not raise the bar" 0 "" "$GUARD" --repo "$r6" --version 2026.11.02-2
r7="$w/r7"; newrepo "$r7"; tagsfor "$r7" catalog-stable
t "catalog-stable alone is no release" 0 "latest existing: none" "$GUARD" --repo "$r7" --version 2026.10.01-1

echo "== guard: malformed versions"
bad() { t "refuses '$1'" 1 "CATALOG_VERSION must be one line YYYY.MM.DD-N" "$GUARD" --repo "$r7" --version "$1"; }
bad ""; bad "2026.11.02"; bad "2026.11.02-0"; bad "2026.11.02-01"; bad "2026.13.01-1"; bad "2026.00.10-1"; bad "2026.02.30-1"
bad "2027.02.29-1"; bad "2026.04.31-1"; bad "2026.11.2-1"; bad "26.11.02-1"; bad "v2026.11.02-1"; bad "2026.11.02-1 "; bad " 2026.11.02-1"
bad "2026.11.02-1234567890"; bad "2026-11-02-1"; bad $'2026.11.02-1\n2026.11.03-1'
setv "$r7" $'2026.11.02-1\n\n'
t "a file with an extra blank line is refused" 1 "must be one line" "$GUARD" --repo "$r7"
setv "$r7" ''
t "an empty file is refused" 1 "must be one line" "$GUARD" --repo "$r7"

echo "== guard: usage and environment"
t "missing file is a usage error" 2 "cannot read" "$GUARD" --repo "$r7" --file "$w/nope"
t "unknown option" 2 "catalog-version-guard.sh" "$GUARD" --nope
t "--file and --version together" 2 "not both" "$GUARD" --repo "$r7" --file "$w/v.txt" --version 2026.11.02-1
mkdir -p "$w/notrepo"
t "not a git repository" 2 "not a git repository" "$GUARD" --repo "$w/notrepo" --version 2026.11.02-1
t "--repo needs a value" 2 "" "$GUARD" --repo

echo "== tag: creates catalog-<V> and moves catalog-stable"
git init -q --bare -b main "$w/remote.git"
git clone -q "$w/remote.git" "$w/work" 2>/dev/null
mkdir -p "$w/work/catalog"; printf '2026.11.02-1\n' >"$w/work/catalog/CATALOG_VERSION"
git -C "$w/work" add -A; git -C "$w/work" commit -q -m "first catalog"; git -C "$w/work" push -q origin main
c1="$(git -C "$w/work" rev-parse HEAD)"
t "first tag" 0 "pushed catalog-2026.11.02-1" "$TAG" --repo "$w/work" 2026.11.02-1
rem() { git -C "$w/remote.git" "$@"; }
t "the tag exists on the remote" 0 "" bash -c "[ \"\$(git -C '$w/remote.git' rev-parse 'refs/tags/catalog-2026.11.02-1^{commit}')\" = '$c1' ]"
t "catalog-stable points at the same commit" 0 "" bash -c "[ \"\$(git -C '$w/remote.git' rev-parse 'refs/tags/catalog-stable^{commit}')\" = '$c1' ]"
t "both are annotated tags" 0 "" bash -c "[ \"\$(git -C '$w/remote.git' cat-file -t refs/tags/catalog-2026.11.02-1)\" = tag ] && [ \"\$(git -C '$w/remote.git' cat-file -t refs/tags/catalog-stable)\" = tag ]"
t "the annotation names the version" 0 "Catalog 2026.11.02-1" git -C "$w/remote.git" tag -n1 catalog-2026.11.02-1

printf '2026.11.03-1\n' >"$w/work/catalog/CATALOG_VERSION"
git -C "$w/work" commit -q -am "second catalog"; git -C "$w/work" push -q origin main
c2="$(git -C "$w/work" rev-parse HEAD)"
t "the guard accepts the bump (tags fetched from the remote)" 0 "" bash -c "git -C '$w/work' fetch -q --tags --force origin && '$GUARD' --repo '$w/work'"
t "second tag" 0 "pushed catalog-2026.11.03-1" "$TAG" --repo "$w/work" 2026.11.03-1
t "catalog-stable moved to the new commit" 0 "" bash -c "[ \"\$(git -C '$w/remote.git' rev-parse 'refs/tags/catalog-stable^{commit}')\" = '$c2' ]"
t "the first tag did not move" 0 "" bash -c "[ \"\$(git -C '$w/remote.git' rev-parse 'refs/tags/catalog-2026.11.02-1^{commit}')\" = '$c1' ]"
t "exactly the two release tags and the moving tag" 0 "" bash -c "[ \"\$(git -C '$w/remote.git' tag --list | sort | tr '\n' ' ')\" = 'catalog-2026.11.02-1 catalog-2026.11.03-1 catalog-stable ' ]"

echo "== tag: refusals leave the remote alone"
stable_before="$(rem rev-parse refs/tags/catalog-stable)"
t "an existing local tag is refused" 1 "already exists; nothing was changed" "$TAG" --repo "$w/work" 2026.11.03-1
t "stable did not move" 0 "" test "$(rem rev-parse refs/tags/catalog-stable)" = "$stable_before"
# the remote has catalog-2026.11.04-1 that this clone never fetched: the push must be rejected whole
git clone -q "$w/remote.git" "$w/other" 2>/dev/null
printf '2026.11.04-1\n' >"$w/other/catalog/CATALOG_VERSION"; git -C "$w/other" commit -q -am "other"; git -C "$w/other" push -q origin main
git -C "$w/other" tag -a -m other catalog-2026.11.04-1; git -C "$w/other" push -q origin refs/tags/catalog-2026.11.04-1
other_tag="$(rem rev-parse refs/tags/catalog-2026.11.04-1)"
git -C "$w/work" pull -q --no-tags origin main
t "a tag the remote already has (not fetched here) is rejected by the push" 1 "no remote ref changed" "$TAG" --repo "$w/work" 2026.11.04-1
t "atomic: catalog-stable did not move either" 0 "" test "$(rem rev-parse refs/tags/catalog-stable)" = "$stable_before"
t "atomic: the other tag is untouched" 0 "" test "$(rem rev-parse refs/tags/catalog-2026.11.04-1)" = "$other_tag"
t "a malformed version is a usage error" 2 "is not YYYY.MM.DD-N" "$TAG" --repo "$w/work" 2026.11.4-1
t "no version is a usage error" 2 "catalog-tag.sh" "$TAG" --repo "$w/work"
t "an unknown commit is a usage error" 2 "is not a commit" "$TAG" --repo "$w/work" --sha deadbeefdeadbeef 2026.12.01-1
t "--sha tags another commit" 0 "dry run" "$TAG" --repo "$w/work" --sha "$c1" --dry-run 2026.12.01-1
t "dry run tagged locally" 0 "" bash -c "[ \"\$(git -C '$w/work' rev-parse 'refs/tags/catalog-2026.12.01-1^{commit}')\" = '$c1' ]"
t "dry run pushed nothing" 1 "" git -C "$w/remote.git" rev-parse --verify --quiet refs/tags/catalog-2026.12.01-1
t "dry run did not move the remote stable" 0 "" test "$(rem rev-parse refs/tags/catalog-stable)" = "$stable_before"

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
