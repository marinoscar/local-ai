#!/usr/bin/env bash
# release-sign.sh: checksum and minisign-sign release files. Used by the release workflows and by hand.
# Trust root: docs/architecture/release-signing.md. Verified by agents/aictl with the embedded public key
# (docs/specs/agent.md section 4.10).
#
#   release-sign.sh sums   <dir> <file>...   write <dir>/SHA256SUMS ("<sha256>  <name>", sorted by name)
#   release-sign.sh sign   <dir>             sign <dir>/SHA256SUMS -> SHA256SUMS.minisig
#   release-sign.sh verify <dir> [pubkey]    check the signature, then every file in SHA256SUMS
#
# sign reads the secret key from $MINISIGN_SECRET_KEY (file contents) and the password from
# $MINISIGN_PASSWORD. The key goes to a mode-0600 temp file removed on exit; the password goes over
# stdin, never argv. Neither is ever printed. RELEASE_TAG fills the signed trusted comment.
# verify defaults to agent/internal/update/minisign.pub.
#
# Exit: 0 ok, 1 verification or signing failed, 2 usage or environment error.
set -euo pipefail

usage() { sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
need() { command -v "$1" >/dev/null 2>&1 || { echo "release-sign: '$1' is required" >&2; exit 2; }; }

cmd="${1:-}"; dir="${2:-}"
[[ -n "$cmd" && -n "$dir" && -d "$dir" ]] || usage
shift 2

case "$cmd" in
  sums)
    [[ $# -ge 1 ]] || usage
    need sha256sum
    tmp="$(mktemp "$dir/.SHA256SUMS.XXXXXX")"
    trap 'rm -f "$tmp"' EXIT
    (
      cd "$dir"
      for f in $(printf '%s\n' "$@" | LC_ALL=C sort); do
        [[ "$f" != */* && -f "$f" ]] || { echo "release-sign: '$f' must be a file directly inside $dir" >&2; exit 2; }
        sha256sum "$f"
      done
    ) >"$tmp"
    mv -f "$tmp" "$dir/SHA256SUMS"   # atomic: a failed run never leaves a partial SHA256SUMS
    echo "✓ wrote $dir/SHA256SUMS ($# files)"
    ;;

  sign)
    need minisign
    [[ -f "$dir/SHA256SUMS" ]] || { echo "release-sign: $dir/SHA256SUMS not found (run 'sums' first)" >&2; exit 2; }
    [[ -n "${MINISIGN_SECRET_KEY:-}" ]] || { echo "release-sign: MINISIGN_SECRET_KEY is not set" >&2; exit 2; }
    [[ -n "${MINISIGN_PASSWORD:-}" ]] || { echo "release-sign: MINISIGN_PASSWORD is not set" >&2; exit 2; }
    keyfile="$(umask 077; mktemp)"
    trap 'rm -f "$keyfile"' EXIT
    printf '%s\n' "$MINISIGN_SECRET_KEY" >"$keyfile"
    rm -f "$dir/SHA256SUMS.minisig"
    if ! printf '%s\n' "$MINISIGN_PASSWORD" \
        | minisign -S -s "$keyfile" -m "$dir/SHA256SUMS" -x "$dir/SHA256SUMS.minisig" \
                   -t "local-ai ${RELEASE_TAG:-release}" >/dev/null 2>&1; then
      rm -f "$dir/SHA256SUMS.minisig"
      echo "release-sign: signing failed (wrong password or malformed key; details hidden on purpose)" >&2
      exit 1
    fi
    echo "✓ signed $dir/SHA256SUMS -> SHA256SUMS.minisig"
    ;;

  verify)
    need minisign; need sha256sum
    pub="${1:-$(git rev-parse --show-toplevel 2>/dev/null || echo .)/agent/internal/update/minisign.pub}"
    [[ -f "$pub" ]] || { echo "release-sign: public key $pub not found. Owner action pending: generate the key pair (docs/architecture/release-signing.md)" >&2; exit 2; }
    [[ -f "$dir/SHA256SUMS" && -f "$dir/SHA256SUMS.minisig" ]] || { echo "release-sign: SHA256SUMS or SHA256SUMS.minisig missing in $dir" >&2; exit 1; }
    if ! minisign -V -p "$pub" -m "$dir/SHA256SUMS" -x "$dir/SHA256SUMS.minisig" >/dev/null 2>&1; then
      echo "release-sign: SIGNATURE INVALID for $dir/SHA256SUMS (wrong key, or the file was changed)" >&2
      exit 1
    fi
    if ! (cd "$dir" && sha256sum --quiet -c SHA256SUMS >/dev/null 2>&1); then
      echo "release-sign: a file does not match its SHA256SUMS line" >&2
      (cd "$dir" && sha256sum -c SHA256SUMS 2>&1 | grep -v ': OK$' | sed 's/^/  /' >&2) || true
      exit 1
    fi
    echo "✓ signature valid and all files match SHA256SUMS ($dir)"
    ;;

  *) usage ;;
esac
