#!/usr/bin/env bash
# cloud-deploy-branch.sh: build the flat deploy tree of the cloud app and publish it to the deploy branch.
#
# Why: EnterpriseAppBase's `appctl deploy` (our `laictl`) expects the app at the ROOT of a repository, and ours
# lives in apps/cloud of this monorepo, with packages/localai-ui a symlink to packages/ui. Until the upstream seam
# request SR-3 ships (docs/specs/cloud-app.md §3.12), the branch `deploy/cloud` holds apps/cloud as a flat tree with
# the symlink replaced by a real copy, and the VPS installs from that branch (§3.13).
#
# Usage:
#   scripts/cloud-deploy-branch.sh build   --out DIR            # build + check the tree only (no git writes)
#   scripts/cloud-deploy-branch.sh publish [--branch B] [--remote R] [--no-push] [--source-sha SHA]
#
# publish: builds the tree, fast-forwards branch B (default deploy/cloud) on remote R (default origin) with ONE
# commit "deploy: apps/cloud @ <short sha>", and pushes it. It is idempotent: an unchanged tree makes no commit.
#
# Safety rules (each is tested in scripts/tests/test-cloud-deploy-branch.sh):
#   * NEVER force-pushes. The push is a plain fast-forward of the commit built on the remote tip; if the remote
#     moved meanwhile the push is refused and the job fails.
#   * Only branches named deploy/cloud or deploy/cloud-<suffix> can be written (a typo cannot touch main).
#   * The tree has no symlink, no .env file (only *.example), and passes scripts/check-redaction.sh, run in the
#     same layout the files have in this repository (apps/cloud/...), so its path-based allowances apply.
#   * Every COPY source of the two Dockerfiles exists in the tree (nothing builds against a missing symlink target).
#   * An older source never overwrites a newer one: if the branch tip was built from a commit that has the
#     current source as an ancestor, the run is skipped.
#
# Env: DEPLOY_GIT_NAME / DEPLOY_GIT_EMAIL set the commit author (default: the github-actions bot).
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="apps/cloud"
BRANCH="deploy/cloud"
REMOTE="origin"
DO_PUSH=1
OUT=""
SOURCE_SHA=""
AUTHOR_NAME="${DEPLOY_GIT_NAME:-github-actions[bot]}"
AUTHOR_EMAIL="${DEPLOY_GIT_EMAIL:-41898282+github-actions[bot]@users.noreply.github.com}"

die() { echo "cloud-deploy-branch: $*" >&2; exit 1; }
usage() { sed -n '2,29p' "$0" | sed 's/^# \{0,1\}//'; }

cmd="${1:-}"; [[ -n "$cmd" ]] || { usage >&2; exit 2; }
shift
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) OUT="${2:?--out needs a directory}"; shift 2 ;;
    --branch) BRANCH="${2:?--branch needs a name}"; shift 2 ;;
    --remote) REMOTE="${2:?--remote needs a name}"; shift 2 ;;
    --no-push) DO_PUSH=0; shift ;;
    --source-sha) SOURCE_SHA="${2:?--source-sha needs a sha}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

branch_ok() { [[ "$1" =~ ^deploy/cloud(-[a-z0-9][a-z0-9-]*)?$ ]]; }

# check_sources: no symlink in apps/cloud may leave the repository (rsync -L would copy the target in), and none
# may dangle. node_modules is excluded from the tree, so it is not looked at.
check_sources() {
  local l target
  while IFS= read -r l; do
    target="$(realpath -m -- "$l")"
    [[ -e "$target" ]] || die "dangling symlink: ${l#"$REPO"/} (target missing)"
    case "$target" in "$REPO"/*) ;; *) die "symlink leaves the repository: ${l#"$REPO"/}" ;; esac
  done < <(find "$REPO/$APP_DIR" -path '*/node_modules' -prune -o -type l -print)
}

# build_tree <dest>: rsync -aL the app into dest and check the result.
build_tree() {
  local dest="$1"
  [[ -d "$REPO/$APP_DIR" ]] || die "$APP_DIR does not exist in $REPO"
  check_sources
  mkdir -p "$dest"
  # -L turns packages/localai-ui (a symlink to ../../../packages/ui) into a real directory. Filter order matters
  # (first match wins): .env*.example is kept (the deploy wizard reads infra/compose/.env.example), every other
  # .env* file is dropped.
  rsync -aL --delete \
    --exclude node_modules --exclude .git \
    --include '.env*.example' --exclude '.env*' \
    "$REPO/$APP_DIR/" "$dest/"
  verify_tree "$dest"
}

verify_tree() {
  local t="$1" bad
  bad="$(find "$t" -type l -print | head -5)"
  [[ -z "$bad" ]] || die "the deploy tree contains symlinks: $bad"
  bad="$(find "$t" -name '.env*' ! -name '*.example' -print | head -5)"
  [[ -z "$bad" ]] || die "the deploy tree contains an environment file: $bad"
  [[ -f "$t/packages/localai-ui/package.json" ]] || die "packages/localai-ui is not a real directory in the tree"
  [[ -f "$t/infra/compose/.env.example" ]] || die "infra/compose/.env.example is missing from the tree"
  check_docker_context "$t"
  check_redaction "$t"
}

# check_docker_context <tree>: every COPY source (not --from) in apps/*/Dockerfile exists in the tree. The images
# are built from the tree root (infra/compose/base.compose.yml: context ../..).
check_docker_context() {
  local t="$1" df src missing=0 line
  for df in "$t"/apps/api/Dockerfile "$t"/apps/web/Dockerfile; do
    [[ -f "$df" ]] || die "missing $(basename "$(dirname "$df")")/Dockerfile in the tree"
    while IFS= read -r line; do
      # COPY [--flag ...] src... dest  (skip multi-stage copies)
      read -r -a w <<<"$line"
      args=(); for x in "${w[@]:1}"; do [[ "$x" == --* ]] || args+=("$x"); done
      [[ ${#args[@]} -ge 2 ]] || continue
      for src in "${args[@]:0:${#args[@]}-1}"; do
        [[ -e "$t/${src%/}" ]] || { echo "cloud-deploy-branch: ${df#"$t"/}: COPY source '$src' is not in the deploy tree" >&2; missing=1; }
      done
    done < <(grep -E '^COPY[[:space:]]' "$df" | grep -vE -- '--from=')
  done
  [[ $missing -eq 0 ]] || die "a Dockerfile copies a path the deploy tree does not contain"
}

# check_redaction <tree>: run scripts/check-redaction.sh over the tree placed at apps/cloud/ of a scratch git repo,
# the same layout it has on main, so the checker's path-based allowances (generated lock files) apply unchanged.
check_redaction() {
  local t="$1" scan inv=""
  scan="$(mktemp -d)"
  git -C "$scan" init -q
  mkdir -p "$scan/$APP_DIR"
  cp -a "$t/." "$scan/$APP_DIR/"
  [[ -f "$REPO/inventory.local.yaml" ]] && inv="$REPO/inventory.local.yaml"
  if ! ( cd "$scan" && INVENTORY_FILE="${inv:-$scan/inventory.local.yaml}" "$REPO/scripts/check-redaction.sh" ); then
    rm -rf "$scan"
    die "the deploy tree failed the redaction check; nothing was committed"
  fi
  rm -rf "$scan"
}

case "$cmd" in
  build)
    [[ -n "$OUT" ]] || die "build needs --out DIR"
    [[ ! -e "$OUT" || -z "$(ls -A "$OUT")" ]] || die "--out must be empty or absent: $OUT"
    build_tree "$OUT"
    echo "✓ deploy tree built at $OUT"
    ;;
  publish)
    branch_ok "$BRANCH" || die "refusing to write branch '$BRANCH': only deploy/cloud or deploy/cloud-<suffix> are allowed"
    cd "$REPO"
    src_full="$(git rev-parse HEAD)"
    [[ -n "$SOURCE_SHA" ]] || SOURCE_SHA="$(git rev-parse --short=7 HEAD)"
    work="$(mktemp -d)"; wt="$work/wt"; tree="$work/tree"
    cleanup() { git -C "$REPO" worktree remove --force "$wt" >/dev/null 2>&1 || true; git -C "$REPO" worktree prune >/dev/null 2>&1 || true; rm -rf "$work"; }
    trap cleanup EXIT

    build_tree "$tree"

    have_remote=0
    if git ls-remote --exit-code --heads "$REMOTE" "$BRANCH" >/dev/null 2>&1; then
      have_remote=1
      git fetch --no-tags "$REMOTE" "+refs/heads/$BRANCH:refs/remotes/$REMOTE/$BRANCH" >/dev/null
    fi

    if [[ $have_remote -eq 1 ]]; then
      # Never let an older source replace a newer one (two queued runs).
      last="$(git log -1 --format=%s "refs/remotes/$REMOTE/$BRANCH" | sed -n 's|^deploy: apps/cloud @ \([0-9a-f]\{7,40\}\)$|\1|p')"
      if [[ -n "$last" ]] && last_full="$(git rev-parse --verify -q "${last}^{commit}")" \
         && [[ "$last_full" != "$src_full" ]] && git merge-base --is-ancestor "$src_full" "$last_full"; then
        echo "= $BRANCH was built from a newer source ($last); nothing to do."
        exit 0
      fi
      git worktree add --detach "$wt" "refs/remotes/$REMOTE/$BRANCH" >/dev/null
    else
      git worktree add --detach "$wt" HEAD >/dev/null
      git -C "$wt" checkout -q --orphan "deploy-build-$$"
      git -C "$wt" rm -rf -q --ignore-unmatch . >/dev/null
    fi

    # Replace the contents (everything except .git) with the tree.
    find "$wt" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
    cp -a "$tree/." "$wt/"
    git -C "$wt" add -A -f
    if [[ $have_remote -eq 1 ]] && git -C "$wt" diff --cached --quiet; then
      echo "= $BRANCH is already up to date; nothing to commit."
      exit 0
    fi
    # The environment form wins over any GIT_AUTHOR_* the caller exported, so the author is always the bot.
    GIT_AUTHOR_NAME="$AUTHOR_NAME" GIT_AUTHOR_EMAIL="$AUTHOR_EMAIL" \
    GIT_COMMITTER_NAME="$AUTHOR_NAME" GIT_COMMITTER_EMAIL="$AUTHOR_EMAIL" \
      git -C "$wt" commit -q -m "deploy: apps/cloud @ $SOURCE_SHA"
    echo "✓ committed deploy: apps/cloud @ $SOURCE_SHA ($(git -C "$wt" rev-parse --short HEAD))"

    if [[ $DO_PUSH -eq 1 ]]; then
      # A plain push of a fast-forward (or of the first commit). No --force, ever.
      git -C "$wt" push "$REMOTE" "HEAD:refs/heads/$BRANCH"
      echo "✓ pushed $BRANCH to $REMOTE"
    else
      git -C "$REPO" update-ref "refs/heads/deploy-build-preview" "$(git -C "$wt" rev-parse HEAD)"
      echo "= --no-push: commit kept locally as refs/heads/deploy-build-preview"
    fi
    ;;
  *) usage >&2; exit 2 ;;
esac
