#!/usr/bin/env bash
# test-cloud-deploy-branch.sh: scripts/cloud-deploy-branch.sh against throwaway git repositories (a bare "remote"
# and a working repo that holds a miniature apps/cloud), never the real remote and no network. The last case builds
# the REAL apps/cloud (no push) to prove the tree is flat, redacted and that both Dockerfiles only copy what exists.
# Needs: git, rsync.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
REAL="$PWD"
pass=0; failn=0
t() { # t <name> <want-exit> <fragment|""> cmd...   (fragment must appear in stdout+stderr)
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then pass=$((pass + 1)); echo "  ok   $name"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -8; fi
}
ok() { # ok <name> <condition-exit-code>
  if [[ $2 -eq 0 ]]; then pass=$((pass + 1)); echo "  ok   $1"; else failn=$((failn + 1)); echo "  FAIL $1"; fi
}

w="$(mktemp -d)"; trap 'rm -rf "$w"' EXIT
export HOME="$w/home"; mkdir -p "$HOME"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME="Person A" GIT_AUTHOR_EMAIL="person.a@example.test" GIT_COMMITTER_NAME="Person A" GIT_COMMITTER_EMAIL="person.a@example.test"

# newsrc <dir> <bare-remote>: a source repository with a miniature apps/cloud, a symlinked packages/localai-ui and the
# real tooling scripts, cloned from / pushing to the bare remote.
newsrc() {
  local d="$1" bare="$2"
  git init -q --bare -b main "$bare"
  git init -q -b main "$d"
  git -C "$d" remote add origin "$bare"
  mkdir -p "$d/scripts" "$d/packages/ui/src" "$d/apps/cloud/apps/api" "$d/apps/cloud/apps/web" \
           "$d/apps/cloud/packages/shared" "$d/apps/cloud/infra/compose" "$d/apps/cloud/node_modules/dep"
  cp "$REAL/scripts/cloud-deploy-branch.sh" "$REAL/scripts/check-redaction.sh" "$d/scripts/"
  printf '{"name":"@localai/ui","version":"0.0.0"}\n' >"$d/packages/ui/package.json"
  printf 'export const a = 1;\n' >"$d/packages/ui/src/index.ts"
  ln -s ../../../packages/ui "$d/apps/cloud/packages/localai-ui"
  printf '{"name":"local-ai"}\n' >"$d/apps/cloud/package.json"
  printf '{"name":"@app/shared"}\n' >"$d/apps/cloud/packages/shared/package.json"
  printf '{"name":"api"}\n' >"$d/apps/cloud/apps/api/package.json"
  printf '{"name":"web"}\n' >"$d/apps/cloud/apps/web/package.json"
  printf 'FROM scratch\nCOPY package.json ./\nCOPY apps/api ./apps/api/\nCOPY packages/localai-ui/package.json ./packages/localai-ui/\nCOPY --from=build /x /y\n' >"$d/apps/cloud/apps/api/Dockerfile"
  printf 'FROM scratch\nCOPY --chown=1:1 apps/web ./apps/web\nCOPY packages/localai-ui ./packages/localai-ui/\n' >"$d/apps/cloud/apps/web/Dockerfile"
  printf 'APP_URL=https://ai.example.test\n' >"$d/apps/cloud/infra/compose/.env.example"
  printf 'SECRET=not-for-the-branch\n' >"$d/apps/cloud/infra/compose/.env"
  printf 'x\n' >"$d/apps/cloud/node_modules/dep/index.js"
  printf 'node_modules/\n.env\n' >"$d/.gitignore"
  git -C "$d" add -A -f ':!apps/cloud/infra/compose/.env' ':!apps/cloud/node_modules'
  git -C "$d" commit -q -m "src 1"
}
push_main() { git -C "$1" push -q origin main; }
remote_count() { git -C "$1" rev-list --count "$2" 2>/dev/null || echo 0; }

SCRIPT_REL="scripts/cloud-deploy-branch.sh"

echo "== build: the tree"
src="$w/src"; bare="$w/remote.git"; newsrc "$src" "$bare"; push_main "$src"
out="$w/out1"
t "build succeeds" 0 "deploy tree built" "$src/$SCRIPT_REL" build --out "$out"
ok "no symlink in the tree" "$([[ -z "$(find "$out" -type l)" ]] && echo 0 || echo 1)"
ok "packages/localai-ui is a real directory with its source" "$([[ -d "$out/packages/localai-ui" && ! -L "$out/packages/localai-ui" && -f "$out/packages/localai-ui/src/index.ts" ]] && echo 0 || echo 1)"
ok ".env is not copied, .env.example is" "$([[ ! -e "$out/infra/compose/.env" && -f "$out/infra/compose/.env.example" ]] && echo 0 || echo 1)"
ok "node_modules is not copied" "$([[ ! -e "$out/node_modules" ]] && echo 0 || echo 1)"
t "build refuses a non-empty --out" 1 "must be empty" "$src/$SCRIPT_REL" build --out "$out"

echo "== build: refusals"
rm -rf "$w/out2"; ln -s ../../../nowhere "$src/apps/cloud/packages/dangling"
t "a dangling symlink is refused" 1 "dangling symlink" "$src/$SCRIPT_REL" build --out "$w/out2"
rm "$src/apps/cloud/packages/dangling"
ln -s /etc "$src/apps/cloud/packages/escape"
t "a symlink leaving the repository is refused" 1 "leaves the repository" "$src/$SCRIPT_REL" build --out "$w/out2"
rm "$src/apps/cloud/packages/escape"
rm -rf "$w/out2"
printf 'COPY packages/missing ./x/\n' >>"$src/apps/cloud/apps/web/Dockerfile"
t "a Dockerfile COPY of a path outside the tree is refused" 1 "COPY source 'packages/missing'" "$src/$SCRIPT_REL" build --out "$w/out2"
git -C "$src" checkout -q -- apps/cloud/apps/web/Dockerfile
rm -rf "$w/out2"
ip="100.$((60 + 5)).7.9"   # a tailnet-range address, built at runtime so this file itself stays clean
printf 'host=%s\n' "$ip" >"$src/apps/cloud/leak.txt"
t "the redaction check fails the build on an injected address" 1 "LEAK" "$src/$SCRIPT_REL" build --out "$w/out2"
rm "$src/apps/cloud/leak.txt"; rm -rf "$w/out2"

echo "== publish: branch rules"
t "main is refused" 1 "only deploy/cloud" "$src/$SCRIPT_REL" publish --branch main
t "an arbitrary branch is refused" 1 "only deploy/cloud" "$src/$SCRIPT_REL" publish --branch feat/x
t "a lookalike branch is refused" 1 "only deploy/cloud" "$src/$SCRIPT_REL" publish --branch deploy/cloudx
t "a path trick is refused" 1 "only deploy/cloud" "$src/$SCRIPT_REL" publish --branch deploy/cloud/../main
ok "the script never force-pushes (static check)" "$(grep -vE '^[[:space:]]*#' "$src/$SCRIPT_REL" | grep -E 'push.*(--force|--mirror|[[:space:]]-f[[:space:]]|\+refs|\+HEAD)' >/dev/null && echo 1 || echo 0)"

echo "== publish: first run, idempotence, update"
export DEPLOY_GIT_NAME="github-actions[bot]" DEPLOY_GIT_EMAIL="bot@example.test"
t "first publish creates the branch" 0 "pushed deploy/cloud" "$src/$SCRIPT_REL" publish
ok "the branch is an orphan with one commit" "$([[ "$(remote_count "$bare" deploy/cloud)" == 1 ]] && echo 0 || echo 1)"
sha1="$(git -C "$src" rev-parse --short=7 HEAD)"
ok "the commit message names the source sha" "$([[ "$(git -C "$bare" log -1 --format=%s deploy/cloud)" == "deploy: apps/cloud @ $sha1" ]] && echo 0 || echo 1)"
ok "the commit author is the bot" "$([[ "$(git -C "$bare" log -1 --format=%an deploy/cloud)" == "github-actions[bot]" ]] && echo 0 || echo 1)"
ok "the branch tree has no symlink (mode 120000)" "$(git -C "$bare" ls-tree -r deploy/cloud | grep -q '^120000' && echo 1 || echo 0)"
ok "the branch has the real copy of packages/ui" "$(git -C "$bare" cat-file -e deploy/cloud:packages/localai-ui/src/index.ts 2>/dev/null && echo 0 || echo 1)"
ok "the branch has no .env" "$(git -C "$bare" cat-file -e deploy/cloud:infra/compose/.env 2>/dev/null && echo 1 || echo 0)"
ok "the branch has no node_modules" "$(git -C "$bare" ls-tree -r --name-only deploy/cloud | grep -q node_modules && echo 1 || echo 0)"
ok "the branch has no history of main (orphan)" "$(git -C "$bare" merge-base deploy/cloud main >/dev/null 2>&1 && echo 1 || echo 0)"

t "an unchanged tree makes no commit" 0 "already up to date" "$src/$SCRIPT_REL" publish
ok "still one commit after the idempotent run" "$([[ "$(remote_count "$bare" deploy/cloud)" == 1 ]] && echo 0 || echo 1)"
ok "no worktree is left behind" "$([[ "$(git -C "$src" worktree list | wc -l)" == 1 ]] && echo 0 || echo 1)"

tip1="$(git -C "$bare" rev-parse deploy/cloud)"
printf 'export const a = 2;\n' >"$src/packages/ui/src/index.ts"
git -C "$src" commit -q -am "src 2 (ui change only, through the symlink)"
t "a change in packages/ui is published as a fast-forward" 0 "pushed deploy/cloud" "$src/$SCRIPT_REL" publish
ok "the old tip is an ancestor of the new tip (history kept, nothing forced)" "$(git -C "$bare" merge-base --is-ancestor "$tip1" deploy/cloud && echo 0 || echo 1)"
ok "two commits now" "$([[ "$(remote_count "$bare" deploy/cloud)" == 2 ]] && echo 0 || echo 1)"
ok "the new copy is on the branch" "$([[ "$(git -C "$bare" show deploy/cloud:packages/localai-ui/src/index.ts)" == 'export const a = 2;' ]] && echo 0 || echo 1)"

echo "== publish: ordering and failure"
git -C "$src" checkout -q --detach HEAD~1
tip2="$(git -C "$bare" rev-parse deploy/cloud)"
t "an older source never replaces a newer one" 0 "newer source" "$src/$SCRIPT_REL" publish
ok "the branch tip did not move" "$([[ "$(git -C "$bare" rev-parse deploy/cloud)" == "$tip2" ]] && echo 0 || echo 1)"
git -C "$src" checkout -q main

printf 'host=%s\n' "$ip" >"$src/apps/cloud/leak.txt"
t "a leak stops publish before any commit" 1 "LEAK" "$src/$SCRIPT_REL" publish
ok "the branch tip did not move after the failed run" "$([[ "$(git -C "$bare" rev-parse deploy/cloud)" == "$tip2" ]] && echo 0 || echo 1)"
rm "$src/apps/cloud/leak.txt"

# A remote that moved between our fetch and our push must be refused, not overwritten: a pre-receive hook rejects
# non-fast-forwards the way a protected branch would, and the script must fail (never retry with --force).
printf 'export const a = 3;\n' >"$src/packages/ui/src/index.ts"; git -C "$src" commit -q -am "src 3"
printf '#!/bin/sh\nexit 1\n' >"$bare/hooks/pre-receive"; chmod +x "$bare/hooks/pre-receive"
t "a refused push fails the job" 1 "" "$src/$SCRIPT_REL" publish
ok "the branch tip did not move after the refused push" "$([[ "$(git -C "$bare" rev-parse deploy/cloud)" == "$tip2" ]] && echo 0 || echo 1)"
rm "$bare/hooks/pre-receive"

echo "== publish: scratch branch and --no-push"
t "deploy/cloud-test can be written" 0 "pushed deploy/cloud-test" "$src/$SCRIPT_REL" publish --branch deploy/cloud-test
ok "deploy/cloud-test exists and deploy/cloud is untouched" "$([[ "$(git -C "$bare" rev-parse deploy/cloud)" == "$tip2" ]] && git -C "$bare" rev-parse -q --verify deploy/cloud-test >/dev/null && echo 0 || echo 1)"
git -C "$src" update-ref -d refs/heads/deploy-build-preview 2>/dev/null
t "--no-push commits locally only" 0 "--no-push" "$src/$SCRIPT_REL" publish --branch deploy/cloud-nopush --no-push
ok "--no-push left the remote without that branch" "$(git -C "$bare" rev-parse -q --verify deploy/cloud-nopush >/dev/null && echo 1 || echo 0)"

echo "== the real apps/cloud"
rm -rf "$w/real"
t "the real tree builds flat, redacted, with every Dockerfile COPY source present" 0 "deploy tree built" "$REAL/scripts/cloud-deploy-branch.sh" build --out "$w/real"
ok "the real tree has no symlink" "$([[ -z "$(find "$w/real" -type l)" ]] && echo 0 || echo 1)"
ok "the real tree has the shared UI package as a real directory" "$([[ -f "$w/real/packages/localai-ui/package.json" && ! -L "$w/real/packages/localai-ui" ]] && echo 0 || echo 1)"
ok "the web image copies what its type check imports (lab fixture)" "$(grep -q 'apps/api/test/fixtures/localai' "$w/real/apps/web/Dockerfile" && [[ -f "$w/real/apps/api/test/fixtures/localai/lab-3-nodes.json" ]] && echo 0 || echo 1)"

echo
echo "cloud-deploy-branch: $pass passed, $failn failed"
[[ $failn -eq 0 ]]
