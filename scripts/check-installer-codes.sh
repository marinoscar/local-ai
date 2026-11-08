#!/usr/bin/env bash
# check-installer-codes.sh: keep the installer's LAI-INS-* sentences equal to agent/internal/doctor/codes.go.
# Spec: docs/specs/installer.md section 8. Issue #436. Offline, bash + awk + diff.
#
# The installer runs before aiagent exists, so it cannot ask the agent for a code's sentences. Instead
# installers/install-node.sh carries a GENERATED block (the function ins_text) with the English and Spanish title
# and fix of every LAI-INS code, copied from codes.go, the one source. This script makes that block:
#   --write   regenerate the block in place from codes.go (run it after a LAI-INS entry changes in codes.go);
#   default   check only: exit 1 when the block differs from what codes.go gives, is missing, or the installer
#             uses a LAI-INS code that has no entry in codes.go.
# The copy is exact except for one rendering rule: a Markdown code span (`cmd`) is printed as 'cmd', the way the
# installer quotes commands on a terminal.
#
# Usage: scripts/check-installer-codes.sh [--write] [--root DIR]   (DIR defaults to the git toplevel; tests use fixtures)
# Exit:  0 equal (or written), 1 drift, 2 usage error or unreadable input.
set -euo pipefail

root=""; write=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --write) write=1; shift ;;
    --root) root="${2:-}"; [[ -n "$root" ]] || { echo "usage: $0 [--write] [--root DIR]" >&2; exit 2; }; shift 2 ;;
    -h|--help) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "usage: $0 [--write] [--root DIR]" >&2; exit 2 ;;
  esac
done
[[ -n "$root" ]] || root="$(git rev-parse --show-toplevel)"
codes="$root/agent/internal/doctor/codes.go"
inst="$root/installers/install-node.sh"
for f in "$codes" "$inst"; do [[ -r "$f" ]] || { echo "check-installer-codes: cannot read $f" >&2; exit 2; }; done

readonly BEGIN_MARK='# BEGIN GENERATED ins-codes'
readonly END_MARK='# END GENERATED ins-codes'
export LC_ALL=C
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# 1. The block codes.go gives. Entries are one field per line (codes.go header): `ID: INS001,`, then
#    `Title: Msg{EN: "...", ES: "..."},` and `Fix: Msg{EN: "...", ES: "..."},`.
awk -v begin="$BEGIN_MARK" -v end="$END_MARK" '
  function golit(s, key,   i, n, c, out) {
    i = index(s, key ": \"")
    if (i == 0) { bad = bad "\n" code ": no " key " string in: " s; return "" }
    i += length(key) + 3; n = length(s); out = ""
    for (; i <= n; i++) {
      c = substr(s, i, 1)
      if (c == "\\") {
        i++; c = substr(s, i, 1)
        if (c != "\"" && c != "\\") bad = bad "\n" code ": unsupported escape \\" c " (only \\\" and \\\\)"
        out = out c; continue
      }
      if (c == "\"") return out
      out = out c
    }
    bad = bad "\n" code ": unterminated string"; return ""
  }
  function shq(s,   i, n, c, out) {   # backticks become single quotes, then a bash single-quoted word
    gsub(/`/, "\047", s)
    out = "\047"; n = length(s)
    for (i = 1; i <= n; i++) { c = substr(s, i, 1); out = out (c == "\047" ? "\047\\\047\047" : c) }
    return out "\047"
  }
  function emit(kind, lang, text) { printf "    \047%s %s %s\047) printf \047%%s\047 %s ;;\n", code, kind, lang, shq(text) }
  /^[[:space:]]*ID:[[:space:]]/ {
    code = ""
    if (match($0, /INS[0-9][0-9][0-9]/)) code = "LAI-INS-" substr($0, RSTART + 3, 3)
    next
  }
  code != "" && /^[[:space:]]*Title:/ { t_en = golit($0, "EN"); t_es = golit($0, "ES"); next }
  code != "" && /^[[:space:]]*Fix:/ {
    f_en = golit($0, "EN"); f_es = golit($0, "ES")
    if (t_en == "" || t_es == "" || f_en == "" || f_es == "") bad = bad "\n" code ": empty title or fix"
    emit("title", "en", t_en); emit("title", "es", t_es); emit("fix", "en", f_en); emit("fix", "es", f_es)
    n++; code = ""; next
  }
  BEGIN {
    print begin " (scripts/check-installer-codes.sh --write; source: agent/internal/doctor/codes.go). Do not edit."
    print "# ins_text <LAI-INS-code> <title|fix>: the sentence of the code in $UI_LANG (en or es); exit 1 if unknown."
    print "ins_text() {"
    print "  case \"$1 $2 ${UI_LANG:-en}\" in"
  }
  END {
    print "    *) return 1 ;;"
    print "  esac"
    print "}"
    print end
    if (bad != "") { print "check-installer-codes: codes.go could not be read:" bad > "/dev/stderr"; exit 2 }
    if (n == 0) { print "check-installer-codes: codes.go has no LAI-INS entries" > "/dev/stderr"; exit 2 }
  }
' "$codes" >"$tmp/want"

# 2. The block the installer carries, and the installer without it.
awk -v begin="$BEGIN_MARK" -v end="$END_MARK" -v blk="$tmp/have" -v rest="$tmp/rest" '
  index($0, begin) == 1 { inb = 1; nb++ }
  inb { print > blk } !inb { print > rest }
  index($0, end) == 1 { inb = 0 }
  END { if (nb != 1 || inb) exit 3 }
' "$inst" || { echo "check-installer-codes: $inst must hold exactly one '$BEGIN_MARK' ... '$END_MARK' block" >&2; exit 1; }

if ((write)); then
  awk -v begin="$BEGIN_MARK" -v end="$END_MARK" -v want="$tmp/want" '
    index($0, begin) == 1 { while ((getline l < want) > 0) print l; skip = 1; next }
    skip && index($0, end) == 1 { skip = 0; next }
    !skip { print }
  ' "$inst" >"$tmp/new"
  cat "$tmp/new" >"$inst"   # keep the file mode and owner
  echo "check-installer-codes: wrote the LAI-INS block of installers/install-node.sh from codes.go"
  exit 0
fi

rc=0
if ! diff -u --label "installers/install-node.sh (generated block)" --label "from agent/internal/doctor/codes.go" \
  "$tmp/have" "$tmp/want" >"$tmp/diff"; then
  echo "ERROR installers/install-node.sh: the LAI-INS sentences differ from agent/internal/doctor/codes.go." >&2
  echo "Fix: run scripts/check-installer-codes.sh --write (never edit the generated block by hand)." >&2
  cat "$tmp/diff" >&2
  rc=1
fi
# Every LAI-INS code the installer prints must have sentences (else ins_text has nothing to say in Spanish).
while IFS= read -r c; do
  grep -qF "'$c title en'" "$tmp/want" || { echo "ERROR installers/install-node.sh: $c is used but has no entry in codes.go" >&2; rc=1; }
done < <(grep -oE 'LAI-INS-[0-9]{3}' "$tmp/rest" | sort -u)
((rc == 0)) && echo "check-installer-codes: ok ($(grep -c "title en'" "$tmp/want") LAI-INS codes, English and Spanish)"
exit "$rc"
