#!/usr/bin/env bash
# check-runbooks.sh: keep the runbooks (docs/runbooks/*.md) true to the code and to the specs. Offline.
# Issues: #166 to #176 (the E9 runbook verification). It is the mechanical half of those dry runs; it proves
# nothing about a real node. Rules and fixes: docs/runbooks/README.md "Checked by a script".
#
# Fails (exit 1) when a runbook:
#  1. mentions `aictl <command> [subcommand]` (also `aiagent ctl ...`, `/opt/localai/bin/aiagent ctl ...`) that
#     is not in the Commands list of the real `aictl help`; or `aiagent <subcommand>` / `aiagent setup <step>`
#     that is not in `aiagent help` / `aiagent setup`;
#  2. uses a flag that `aictl help <command>` (or, for aiagent, its usage text) does not list, and that is not
#     one of the global flags (a flag is checked against the command it follows, and only inside a typed command);
#  3. mentions a LAI-AAA-NNN code that is not in the registry (docs/specs/doctor-codes.md), or a leftover
#     placeholder such as LAI-XXX-000; a range LAI-AAA-NNN..MMM needs both ends in the registry;
#  4. has a numbered list at the left margin that is not numbered consecutively (a list may restart at 1);
#  5. has a numbered step with no "What you should see" or no "If it doesn't work" (Spanish: "Lo que deberia ver",
#     "Si no funciona"). Steps written on adjacent lines count as one (the answer may follow the last of them).
#     Exemption, for a step list that has nothing to show or nothing that can go wrong (buying, reading):
#       <!-- runbook-lint: no-expect -->           in the step (or in its text block): that step only
#       <!-- runbook-lint: no-expect-section -->   anywhere in a section (until the next heading): all its steps
#  6. names an installed path under /opt/localai, /etc/localai, /var/lib/localai, /var/log, /srv/localai,
#     /usr/local/{bin,sbin} or /etc/systemd/system that docs/architecture/overview.md section 3 does not list
#     (a file below a listed directory is fine), a <root>/ or /srv/localai/ path whose first folder is not in
#     the content root layout (overview.md 3.3), or a rendered manual below docs/ in a sub-folder
#     (docs/runbooks/NN-name.html: the rendered files are flat, docs/NN-name.html).
#
# The binaries come from --bin-dir (containing `aictl` and `aiagent`, which tests fake), else they are built from
# agent/ the way scripts/agent-ci.sh builds (go build -mod=vendor ./cmd/aiagent, aictl a symlink to it).
#  7. lost a line break: a sentence glued to the next one ("...up to date.Yellow after...") or a list item glued
#     to the text before it ("text.   - **Next**") in running text (code and link targets are ignored).
#
#     Baseline (rule 5 only): steps written before this check existed are listed in scripts/check-runbooks.baseline,
#     one `<runbook>|<section heading>|<first step number>` per line. A step list in the baseline is tolerated; a
#     new or changed one is not. The list may only shrink: an entry whose step list now complies, or no longer
#     exists, is an error ("stale baseline entry"). Writing the real "What you should see" while following the
#     runbook on a node (issues #166 to #176) is what removes entries. `--write-baseline` rewrites the file.
#
# Usage: scripts/check-runbooks.sh [--root DIR] [--bin-dir DIR] [--baseline FILE] [--write-baseline] [RUNBOOK.md ...]
#   --root      repository root (default: the git toplevel; tests use fixtures)
#   --bin-dir   folder with the aictl and aiagent to read the help from (default: build them from agent/)
#   --baseline  the baseline file (default: scripts/check-runbooks.baseline under the root; may be absent)
#   --write-baseline  write the baseline from the current rule 5 findings, then exit 0
#   files       runbooks to check (default: docs/runbooks/*.md)
# Exit:  0 all true, 1 problems (printed as `ERROR <file>:<line>: ...`), 2 usage or tooling error.
set -euo pipefail

root=""
bindir=""
baseline=""
writebase=0
files=()
usage="usage: $0 [--root DIR] [--bin-dir DIR] [--baseline FILE] [--write-baseline] [FILE...]"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) root="${2:-}"; [[ -n "$root" ]] || { echo "$usage" >&2; exit 2; }; shift 2 ;;
    --bin-dir) bindir="${2:-}"; [[ -n "$bindir" ]] || { echo "$usage" >&2; exit 2; }; shift 2 ;;
    --baseline) baseline="${2:-}"; [[ -n "$baseline" ]] || { echo "$usage" >&2; exit 2; }; shift 2 ;;
    --write-baseline) writebase=1; shift ;;
    -h|--help) sed -n '2,50p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "$usage" >&2; exit 2 ;;
    *) files+=("$1"); shift ;;
  esac
done
[[ -n "$root" ]] || root="$(git rev-parse --show-toplevel)"
root="$(cd "$root" && pwd)"
cd "$root"

[[ -n "$baseline" ]] || baseline="scripts/check-runbooks.baseline"
explicit_files=0
[[ ${#files[@]} -gt 0 ]] && explicit_files=1
REG="docs/specs/doctor-codes.md"
OVERVIEW="docs/architecture/overview.md"
for f in "$REG" "$OVERVIEW"; do
  [[ -f "$f" ]] || { echo "ERROR $f:0: not found under $root" >&2; exit 2; }
done
if [[ ${#files[@]} -eq 0 ]]; then
  shopt -s nullglob
  files=(docs/runbooks/*.md)
  shopt -u nullglob
fi
[[ ${#files[@]} -gt 0 ]] || { echo "ERROR no runbooks found under $root/docs/runbooks" >&2; exit 2; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- the binaries ----------------------------------------------------------------------------------------------
if [[ -z "$bindir" ]]; then
  command -v go >/dev/null 2>&1 || {
    [[ -x /usr/local/go/bin/go ]] && PATH="$PATH:/usr/local/go/bin"
    command -v go >/dev/null 2>&1 || { echo "ERROR check-runbooks: Go is required to build aictl (or pass --bin-dir)" >&2; exit 2; }
  }
  [[ -d agent/cmd/aiagent ]] || { echo "ERROR agent/cmd/aiagent not found: nothing to build (or pass --bin-dir)" >&2; exit 2; }
  mkdir -p "$tmp/bin"
  mod_flag=()
  [[ -d agent/vendor ]] && mod_flag=(-mod=vendor)
  (cd agent && CGO_ENABLED=0 go build "${mod_flag[@]}" -trimpath -buildvcs=false -o "$tmp/bin/aiagent" ./cmd/aiagent) \
    || { echo "ERROR check-runbooks: building agent/cmd/aiagent failed" >&2; exit 2; }
  ln -s aiagent "$tmp/bin/aictl"
  bindir="$tmp/bin"
fi
AICTL="$bindir/aictl"
AIAGENT="$bindir/aiagent"
[[ -x "$AICTL" && -x "$AIAGENT" ]] || { echo "ERROR check-runbooks: $bindir must contain executable aictl and aiagent" >&2; exit 2; }

# --- what the real help says -----------------------------------------------------------------------------------
# data.tsv lines:  C <path>            a command (the words of the Commands list, marks removed)
#                  F <path> <flag>     a flag (dashes removed) named in `aictl help <path>`
#                  G <flag>            a global flag (dashes removed)
#                  A <sub>             an `aiagent` subcommand      S <step>   an `aiagent setup` step
#                  AF <flag>           a flag named in the aiagent usage text
#                  K <code>            a registry doctor code       P <regex>  a canonical installed path (regex)
#                  PD <regex>          a canonical directory (descendants allowed)   PA <path>  an ancestor path
#                  R <name>            a first-level name of the content root layout (overview.md 3.3)
data="$tmp/data.tsv"
: >"$data"

flags_of() { # flags_of <text on stdin>: one flag name (no dashes) per line
  grep -oE '(^|[^A-Za-z0-9_-])--?[A-Za-z][A-Za-z0-9-]*' | sed -E 's/^[^-]*-+//' | sort -u
}

"$AICTL" help >"$tmp/root-help.txt" 2>&1 || { echo "ERROR check-runbooks: '$AICTL help' failed:" >&2; sed 's/^/  /' "$tmp/root-help.txt" >&2; exit 2; }
awk '
  /^Commands:/ { on = 1; next }
  on && /^[^ ]/ { on = 0 }
  on && /^  [a-z]/ {
    line = substr($0, 3); n = split(line, w, /[ \t]+/); path = ""
    for (i = 1; i <= n; i++) { if (w[i] !~ /^[a-z][a-z0-9-]*$/) break; path = path (path == "" ? "" : " ") w[i] }
    if (path != "") print path
  }' "$tmp/root-help.txt" | sort -u >"$tmp/cmds.txt"
[[ -s "$tmp/cmds.txt" ]] || { echo "ERROR check-runbooks: no commands found in '$AICTL help'" >&2; exit 2; }
{
  while IFS= read -r p; do printf 'C\t%s\n' "$p"; done <"$tmp/cmds.txt"
  printf 'C\thelp\n'
  # Global flags: those listed under "Global flags:" plus the ones every command accepts.
  { awk '/^Global flags/ { on = 1; next } on && /^[^ ]/ { on = 0 } on { print }' "$tmp/root-help.txt"; echo "--timeout --help -h --version"; } \
    | flags_of | while IFS= read -r g; do printf 'G\t%s\n' "$g"; done
  # A command's flags: whatever its own help text names (usage line, body and the Flags list).
  while IFS= read -r p; do
    # shellcheck disable=SC2086 # the path is words on purpose
    "$AICTL" help $p 2>&1 | flags_of | while IFS= read -r f; do printf 'F\t%s\t%s\n' "$p" "$f"; done || true
  done <"$tmp/cmds.txt"
} >>"$data"

"$AIAGENT" help >"$tmp/agent-help.txt" 2>&1 || true
"$AIAGENT" setup >"$tmp/agent-setup.txt" 2>&1 || true
{
  # `aiagent` subcommands: the first word of each usage line "  aiagent <word>".
  sed -nE 's/^[[:space:]]+aiagent[[:space:]]+([a-z][a-z0-9-]*).*/\1/p' "$tmp/agent-help.txt" | sort -u | while IFS= read -r s; do printf 'A\t%s\n' "$s"; done
  # `aiagent setup` steps: the first word of each indented line of its usage.
  sed -nE 's/^[[:space:]]+([a-z][a-z0-9-]*)[[:space:]][[:space:]]+.*/\1/p' "$tmp/agent-setup.txt" | sort -u | while IFS= read -r s; do printf 'S\t%s\n' "$s"; done
  # Flags of aiagent: its usage text, the migrate and harden usage (their parsers refuse --help and print it).
  { cat "$tmp/agent-help.txt" "$tmp/agent-setup.txt"
    "$AIAGENT" migrate --help 2>&1 || true
    "$AIAGENT" setup harden --help 2>&1 || true; } | flags_of | while IFS= read -r f; do printf 'AF\t%s\n' "$f"; done
} >>"$data"

# --- the doctor-code registry ----------------------------------------------------------------------------------
grep -E '^\| *LAI-[A-Z]+-[0-9]{3} *\|' "$REG" | sed -E 's/^\| *(LAI-[A-Z]+-[0-9]{3}).*/K\t\1/' | sort -u >>"$data"

# --- canonical names (overview.md section 3) --------------------------------------------------------------------
awk '/^## 3\. Canonical names/ { on = 1; next } on && /^## / { on = 0 } on { print }' "$OVERVIEW" >"$tmp/section3.md"
[[ -s "$tmp/section3.md" ]] || { echo "ERROR $OVERVIEW:0: section '## 3. Canonical names' not found" >&2; exit 2; }
# Absolute paths named in the section's tables, one per line (brace groups {a,b} expanded one level).
# shellcheck disable=SC2016 # the backticks are markdown, not a command substitution
grep -oE '`/(opt|etc|var|srv|run|usr)/[^` ]*`' "$tmp/section3.md" | tr -d '`' | sort -u \
  | awk '{
      if (match($0, /\{[^}]*\}/)) {
        pre = substr($0, 1, RSTART - 1); post = substr($0, RSTART + RLENGTH)
        alts = substr($0, RSTART + 1, RLENGTH - 2); n = split(alts, a, ",")
        for (i = 1; i <= n; i++) print pre a[i] post
      } else print
    }' >"$tmp/canon.txt"
[[ -s "$tmp/canon.txt" ]] || { echo "ERROR $OVERVIEW:0: no installed paths found in section 3" >&2; exit 2; }
# shellcheck disable=SC2129 # appending to the data file from several places on purpose
awk '
  function rx(s) { gsub(/<[^>]*>/, "@", s); gsub(/[][.+?(){}^$|\\]/, "\\\\&", s); gsub(/@/, "[^/]*", s); return s }
  {
    p = $0
    # A directory below which anything is fine; the lab-wide folders only allow what section 3 names in them.
    if (p ~ /\/$/) { sub(/\/$/, "", p); if (p !~ /^\/(etc\/localai|var\/lib\/localai|srv\/localai|opt\/localai)$/) printf "PD\t%s\n", rx(p) }
    printf "P\t%s\n", rx(p)
    # every ancestor directory of a canonical path is a fine thing to mention
    q = p
    while (match(q, /\/[^\/]*$/) && RSTART > 1) { q = substr(q, 1, RSTART - 1); printf "PA\t%s\n", rx(q) }
  }' "$tmp/canon.txt" >>"$data"
# What the row of /var/lib/localai/ names inside it (`state.json`, `sync/`, `drill/`, ...).
# shellcheck disable=SC2016 # the backticks are markdown, not a command substitution
{ grep -F '| `/var/lib/localai/` |' "$tmp/section3.md" | grep -oE '`[a-z][a-z0-9._-]*/?`' || true; } | tr -d '`' \
  | while IFS= read -r n; do printf 'PD\t/var/lib/localai/%s\n' "${n%/}"; done >>"$data"
# Units: every localai-*.service / .timer named in section 3 lives in /etc/systemd/system.
# shellcheck disable=SC2016 # the backticks are markdown, not a command substitution
{ grep -oE '`localai-[a-z0-9-]+\.(service|timer)`' "$tmp/section3.md" || true; } | tr -d '`' | sort -u \
  | while IFS= read -r u; do printf 'P\t/etc/systemd/system/%s\n' "${u//./\\.}"; done >>"$data"
# Named exceptions (each documented where it is defined):
#  - /etc/localai/node.yaml.bak: the copy the agent keeps on every write (overview.md 3.2, row node.yaml).
#  - /opt/localai/bin/aictl: inside the cloud API image only (apps/cloud/apps/api/Dockerfile); on a node aictl is
#    /usr/local/bin/aictl.
printf 'P\t/etc/localai/node\\.yaml\\.bak\nP\t/opt/localai/bin/aictl\n' >>"$data"
# The first-level names of the content root layout (the fenced tree in 3.3).
awk '/^### 3\.3/ { on = 1; next } on && /^### / { on = 0 } on && /^  [A-Za-z.]/ { n = $1; sub(/\/.*$/, "", n); sub(/<.*$/, "", n); if (n != "") print n }' "$tmp/section3.md" \
  | sort -u | while IFS= read -r n; do printf 'R\t%s\n' "$n"; done >>"$data"
grep -qE '^R	' "$data" || { echo "ERROR $OVERVIEW:0: content root layout (3.3) not found" >&2; exit 2; }

# --- the checker --------------------------------------------------------------------------------------------------
cat >"$tmp/lint.awk" <<'AWK'
function err(ln, msg) { printf "ERROR %s:%d: %s\n", FNAME, ln, msg; bad++ }
function trimpunct(t) { gsub(/[,.:;)"'`]+$/, "", t); return t }

# ---- aictl / aiagent command checks ----------------------------------------------------------------------------
function is_flag(t) { return (t ~ /^\[?--?[A-Za-z]/) }
function flag_names(t, out,    n, parts, i, f, c) {
  # "[--root" "--a|--b" "--cuda-arch=75" -> names without dashes; returns the count
  gsub(/^[\[(]+/, "", t); n = split(t, parts, "|"); c = 0
  for (i = 1; i <= n; i++) {
    f = parts[i]
    if (f !~ /^--?[A-Za-z]/) continue
    sub(/^-+/, "", f); sub(/[^A-Za-z0-9-].*$/, "", f)
    if (f != "") out[++c] = f
  }
  return c
}
function check_flag(ln, cmdname, path, tok,    n, names, i) {
  n = flag_names(tok, names)
  for (i = 1; i <= n; i++) {
    if (cmdname == "aiagent") {
      if (!(names[i] in AF) && !(names[i] in G)) err(ln, "the flag --" names[i] " is not in the aiagent usage text (" cmdname (path == "" ? "" : " " path) ")")
    } else if (!((path SUBSEP names[i]) in F) && !(names[i] in G)) {
      err(ln, "the flag " (length(names[i]) == 1 ? "-" : "--") names[i] " is not listed by `aictl help" (path == "" ? "" : " " path) "`")
    }
  }
}
function flag_is_global(t,    names, n, i) {
  n = flag_names(t, names)
  for (i = 1; i <= n; i++) if (!(names[i] in G)) return 0
  return n > 0
}
function bare(t) { return (t ~ /^[a-z][a-z0-9-]*(\|[a-z][a-z0-9-]*)*$/) }
# check_aictl: tokens w[start..n] follow the word "aictl" (or "aiagent ctl")
function check_aictl(ln, w, start, n,    i, t, path, resolving, alts, na, j, cand, hasch, k, stopnext) {
  path = ""; resolving = 1; stopnext = 0
  for (i = start; i <= n; i++) {
    if (stopnext) return
    t = w[i]
    if (t ~ /^(\|\||\||&&|;|>|>>|<<<|2>|2>&1|&)$/ || t ~ /^#/) return
    if (t ~ /^[("'`$]*(\/[A-Za-z0-9_.-]+)*\/?(aictl|aiagent)[.,;:)`'"]*$/ && i > start) return          # the next command starts here
    stopnext = (t ~ /`/)                                                           # prose after a closing backtick is not an argument
    sub(/;$/, "", t); t = trimpunct(t); if (t == "") continue
    if (resolving) {
      if (t ~ /^--?(lang|socket|url|timeout)$/) { i++; continue }
      if (is_flag(t)) {
        if (flag_is_global(t)) continue
        check_flag(ln, "aictl", path, t); resolving = 0; continue
      }
      if (bare(t)) {
        na = split(t, alts, "|")
        if (na > 1) {
          for (j = 1; j <= na; j++) if (!(((path == "" ? "" : path " ") alts[j]) in C)) err(ln, "`aictl " (path == "" ? "" : path " ") alts[j] "` is not an aictl command")
          resolving = 0; continue
        }
        cand = (path == "" ? "" : path " ") t
        if (cand in C) { path = cand; continue }
        hasch = 0
        for (k in C) if (index(k, (path == "" ? "" : path " ")) == 1 && k != path && path != "") { hasch = 1; break }
        if (path == "" || hasch || (path in HASCHILD)) err(ln, "`aictl " cand "` is not an aictl command (not in `aictl help" (path == "" ? "" : " " path) "`)")
      }
      resolving = 0; continue
    }
    if (is_flag(t)) check_flag(ln, "aictl", path, t)
  }
}
function check_aiagent(ln, w, start, n,    i, t, sub1, step) {
  i = start
  while (i <= n && (w[i] ~ /^--?[A-Za-z]/)) i++      # no global flags in front of aiagent subcommands
  if (i > n) return
  sub1 = trimpunct(w[i])
  if (w[i] ~ /^(\|\||\||&&|;|>|>>)$/ || w[i] ~ /^#/) return
  if (!bare(sub1)) return
  if (sub1 == "ctl") { check_aictl(ln, w, i + 1, n); return }
  if (!(sub1 in A)) { err(ln, "`aiagent " sub1 "` is not in `aiagent help`"); return }
  i++
  if (sub1 == "setup" && i <= n && bare(trimpunct(w[i]))) {
    step = trimpunct(w[i])
    if (!(step in S)) err(ln, "`aiagent setup " step "` is not in `aiagent setup`")
    i++
  }
  for (; i <= n; i++) {
    t = w[i]
    if (t ~ /^(\|\||\||&&|;|>|>>|2>|2>&1)$/ || t ~ /^#/) return
    sub(/;$/, "", t); t = trimpunct(t)
    if (is_flag(t)) check_flag(ln, "aiagent", sub1, t)
  }
}
function scan_commands(ln, text,    n, w, i, t, base) {
  n = split(text, w, /[ \t]+/)
  for (i = 1; i <= n; i++) {
    t = w[i]; sub(/^[("'`$]+/, "", t); t = trimpunct(t)
    if (t !~ /^(\/[A-Za-z0-9_.-]+)*\/?(aictl|aiagent)$/) continue
    base = t; sub(/^.*\//, "", base)
    if (w[i] ~ /[A-Za-z0-9_.\/-](aictl|aiagent)/ && t !~ /\//) continue
    if (base == "aictl") check_aictl(ln, w, i + 1, n)
    else check_aiagent(ln, w, i + 1, n)
  }
}

# ---- doctor codes ---------------------------------------------------------------------------------------------
function check_codes(ln, text,    rest, code, endpart, area, num, endcode) {
  rest = text
  while (match(rest, /LAI-[A-Za-z]+-([0-9]+|N+)(\.\.[0-9]+)?/)) {
    code = substr(rest, RSTART, RLENGTH); rest = substr(rest, RSTART + RLENGTH)
    endpart = ""
    if (index(code, "..") > 0) { endpart = substr(code, index(code, "..") + 2); code = substr(code, 1, index(code, "..") - 1) }
    if (code ~ /^LAI-(XXX|AAA|YYY|ZZZ)-/ || code ~ /-(000|N+)$/) { err(ln, code " is a placeholder: use a real code from the registry (" REGFILE ")"); continue }
    if (!(code in K)) { err(ln, code " is not defined in the registry (" REGFILE ")"); continue }
    if (endpart != "") {
      endcode = substr(code, 1, length(code) - length(endpart)) endpart
      if (!(endcode in K)) err(ln, "the range " code ".." endpart ": " endcode " is not defined in the registry (" REGFILE ")")
    }
  }
}

# ---- installed paths ------------------------------------------------------------------------------------------
function canon_ok(p,    q, pat) {
  q = p
  for (pat in P) if (q ~ ("^" pat "$")) return 1
  for (pat in PD) if (q ~ ("^" pat "(/|$)")) return 1
  for (pat in PA) if (q ~ ("^" pat "/?$")) return 1
  return 0
}
function check_paths(ln, text,    rest, p, orig, first, r2, tail) {
  rest = text
  gsub(/\]\([^)]*\)/, "]", rest)                       # markdown link targets are checked by the link checker
  while (match(rest, /(^|[^A-Za-z0-9_.<>\/-])\/(opt\/localai|etc\/localai|var\/lib\/localai|var\/log\/localai|srv\/localai|usr\/local\/sbin|usr\/local\/bin|etc\/systemd\/system|run\/localai)[A-Za-z0-9_.<>{}*\/-]*/)) {
    p = substr(rest, RSTART, RLENGTH); rest = substr(rest, RSTART + RLENGTH)
    sub(/^[^\/]*/, "", p); p = trimpunct(p); orig = p
    if (p ~ /[{}*]/) continue                          # a brace or glob pattern in prose: not a single path
    if (p ~ /^\/etc\/localai-/) continue                # files of the VPS stack agent (cloud-app.md 4.10), not of a node
    gsub(/<[^>]*>/, "@", p)
    if (p ~ /^\/srv\/localai(\/|$)/) {
      tail = p; sub(/^\/srv\/localai\/?/, "", tail)
      if (tail != "") { first = tail; sub(/\/.*$/, "", first); if (!(first in R)) err(ln, orig " is not in the content root layout (overview.md 3.3): no \"" first "\" folder") }
      continue
    }
    if (p ~ /^\/(usr\/local\/(bin|sbin)|run\/localai)/ && p !~ /^\/usr\/local\/(bin|sbin)\/(aictl|install-node\.sh)$/) {
      if (p ~ /^\/usr\/local\//) continue              # other tools in /usr/local are not ours
    }
    if (!canon_ok(p)) err(ln, orig " is not a canonical path (overview.md section 3)")
  }
  # <root>/... and <ark-drive-mount>/localai/... and localai/docs/... forms
  rest = text; gsub(/\]\([^)]*\)/, "]", rest)
  while (match(rest, /<(root|ark-drive-mount\/localai|ark-drive-mount)>?\/[A-Za-z0-9_.<>{}*\/-]*|<root>\/[^ `]*/)) {
    p = substr(rest, RSTART, RLENGTH); rest = substr(rest, RSTART + RLENGTH)
    tail = p
    if (tail ~ /^<root>\//) sub(/^<root>\//, "", tail)
    else if (tail ~ /^<ark-drive-mount>\/localai\//) sub(/^<ark-drive-mount>\/localai\//, "", tail)
    else continue
    first = tail; sub(/\/.*$/, "", first); sub(/[^A-Za-z0-9._-].*$/, "", first)
    if (first != "" && !(first in R)) err(ln, trimpunct(p) " is not in the content root layout (overview.md 3.3): no \"" first "\" folder")
  }
}
# Rendered manuals are flat: docs/<name>.html, never docs/runbooks/<name>.html (or specs/).
function check_flat_anywhere(ln, text,    rest, p) {
  rest = text
  while (match(rest, /docs\/(runbooks|specs|architecture|decisions|source)\/[A-Za-z0-9_.<>-]+\.(html|pdf)/)) {
    p = substr(rest, RSTART, RLENGTH); rest = substr(rest, RSTART + RLENGTH)
    err(ln, "rendered manuals are flat (docs/<name>.html), not in a sub-folder: " p)
  }
}

# ---- lost line breaks ---------------------------------------------------------------------------------------------
function check_glue(ln, line,    t) {
  t = line
  gsub(/`[^`]*`/, "", t); gsub(/\]\([^)]*\)/, "]", t); gsub(/https?:\/\/[^ )>]*/, "", t); gsub(/<[^>]*>/, "", t)
  if (t ~ /[a-z0-9)][.!?][A-Z][a-z][a-z]/) err(ln, "two sentences are glued together (a lost line break or a missing space after the full stop)")
  else if (t ~ /[A-Za-z0-9.)]   +- \*\*/ || t ~ /[a-z.]   +- [A-Z]/) err(ln, "a list item is glued to the text before it (a lost line break)")
}

# ---- numbered steps ---------------------------------------------------------------------------------------------
function close_cluster(    miss) {
  if (!cl_open) return
  miss = ""
  if (!cl_see) miss = "\"What you should see\""
  if (!cl_dw) miss = miss (miss == "" ? "" : " and ") "\"If it doesn't work\""
  if (miss != "" && !cl_exempt) { nmiss++; mline[nmiss] = cl_first; msec[nmiss] = sect; mtxt[nmiss] = miss; mlast[nmiss] = cl_last; mkey[nmiss] = FNAME "|" sect_title "|" cl_num }
  cl_open = 0
}
function note_text(ln, line,    l) {
  l = tolower(line)
  if (l ~ /what you should see|lo que deber.*a ver/) cl_see = 1
  if (l ~ /if it doesn.t work|si no funciona/) cl_dw = 1
  if (l ~ /runbook-lint: *no-expect-section/) secexempt[sect] = 1
  else if (l ~ /runbook-lint: *no-expect/) cl_exempt = 1
}

function begin_file() {
  infence = 0; prevn = 0; sect = 0; sect_title = ""; cl_open = 0; bare_pending = 0; nmiss = 0; pending = ""; pendln = 0
  delete secexempt
}
function end_file(    i) {
  close_cluster()
  for (i = 1; i <= nmiss; i++) {
    if (secexempt[msec[i]]) continue
    found[mkey[i]] = 1
    if (WRITEBASE) { print mkey[i] > BASEOUT; continue }
    if (mkey[i] in BASE) continue
    err(mline[i], "numbered step" (mlast[i] > mline[i] ? "s" : "") " without " mtxt[i] " (add them; or, if the steps have nothing to show, <!-- runbook-lint: no-expect --> in the step or <!-- runbook-lint: no-expect-section --> in the section)")
  }
}

function process_code_line(ln, line) {
  # a typed command line inside a fenced block: join "\" continuations, ignore comment lines and the "$ " prompt
  if (pending != "") { line = pending " " line; ln = pendln; pending = "" }
  if (line ~ /\\[ \t]*$/) { sub(/\\[ \t]*$/, "", line); pending = line; pendln = ln; return }
  sub(/^[ \t]+/, "", line)
  if (line ~ /^#/) return
  sub(/^\$ /, "", line)
  scan_commands(ln, line)
}

BEGIN {
  while ((getline line < DATA) > 0) {
    n = split(line, f, "\t")
    if (f[1] == "C") { C[f[2]] = 1; np = split(f[2], pw, " "); if (np > 1) { par = pw[1]; for (q = 2; q < np; q++) par = par " " pw[q]; HASCHILD[par] = 1 } }
    else if (f[1] == "F") F[f[2] SUBSEP f[3]] = 1
    else if (f[1] == "G") G[f[2]] = 1
    else if (f[1] == "A") A[f[2]] = 1
    else if (f[1] == "S") S[f[2]] = 1
    else if (f[1] == "AF") AF[f[2]] = 1
    else if (f[1] == "K") K[f[2]] = 1
    else if (f[1] == "P") P[f[2]] = 1
    else if (f[1] == "PD") PD[f[2]] = 1
    else if (f[1] == "PA") PA[f[2]] = 1
    else if (f[1] == "R") R[f[2]] = 1
  }
  if (BASELINE != "") while ((getline line < BASELINE) > 0) {
    sub(/[ \t]+#.*$/, "", line)
    if (line ~ /^[ \t]*$/ || line ~ /^#/) continue
    BASE[line] = 1
  }
}

FNR == 1 { if (FNAME != "") end_file(); FNAME = FILENAME; CHECKED[FNAME] = 1; begin_file() }
{
  line = $0
  # fenced code
  if (line ~ /^[ \t]*(```|~~~)/) {
    if (!infence) { infence = 1 } else { infence = 0; pending = "" }
    if (cl_open) { cl_last = FNR }
    check_paths(FNR, line); check_codes(FNR, line)
    next
  }
  check_codes(FNR, line)
  check_paths(FNR, line)
  check_flat_anywhere(FNR, line)
  if (!infence) check_glue(FNR, line)
  if (infence) {
    process_code_line(FNR, line)
    if (cl_open) { note_text(FNR, line); cl_last = FNR; had_body = 1 }
    next
  }
  # inline code spans: the odd pieces between backticks
  np = split(line, pieces, "`")
  if (np > 1 && np % 2 == 1) for (pi = 2; pi <= np; pi += 2) scan_commands(FNR, pieces[pi])

  if (line ~ /^#+ /) { close_cluster(); sect++; sect_title = line; sub(/^#+ +/, "", sect_title); gsub(/\|/, "/", sect_title); bare_pending = 0; prevn_keep = prevn; next }
  if (line ~ /^---+[ \t]*$/) { close_cluster(); bare_pending = 0; next }

  if (match(line, /^[0-9]+\. /)) {
    num = substr(line, 1, RLENGTH - 2) + 0
    if (prevn > 0 && num != prevn + 1 && num != 1) err(FNR, "step " num " follows step " prevn " (numbered lists count 1, 2, 3, ...; a new list may restart at 1)")
    prevn = num
    # a step on the line right after a step with nothing of its own joins it into one cluster
    if (!(cl_open && bare_pending)) { close_cluster(); cl_open = 1; cl_see = 0; cl_dw = 0; cl_exempt = 0; cl_first = FNR; cl_num = num; had_body = 0 }
    cl_last = FNR; bare_pending = 1; had_body = 0
    note_text(FNR, line)
    next
  }
  if (cl_open) {
    if (line ~ /^[ \t]*$/) { next }
    if (line ~ /^[ \t]+/ || line ~ /^>/) { note_text(FNR, line); bare_pending = 0; cl_last = FNR; next }
    # a left-margin line that is not a step: the text block of the step ends here
    close_cluster(); bare_pending = 0
  }
  if (line ~ /runbook-lint: *no-expect-section/) secexempt[sect] = 1
}
END {
  if (FNAME != "") end_file()
  if (!WRITEBASE) for (k in BASE) {
    fn = k; sub(/\|.*$/, "", fn)
    if ((fn in CHECKED) && !(k in found)) err(1, "stale baseline entry \"" k "\": that step list now complies or no longer exists; remove it from " BASELINE)
  }
  exit (bad > 0)
}
AWK

base_arg=""""
[[ -f "$baseline" ]] && base_arg="$baseline"
if [[ $writebase -eq 1 ]]; then
  awk -v DATA="$data" -v REGFILE="$REG" -v WRITEBASE=1 -v BASEOUT="$tmp/newbase.txt" -f "$tmp/lint.awk" "${files[@]}" >"$tmp/out.txt" 2>"$tmp/awk.err" || true
  if [[ -s "$tmp/awk.err" ]]; then echo "ERROR check-runbooks: awk failed:" >&2; cat "$tmp/awk.err" >&2; exit 2; fi
  [[ $explicit_files -eq 0 ]] || { echo "ERROR --write-baseline checks every runbook: give no file names" >&2; exit 2; }
  mkdir -p "$(dirname "$baseline")"
  {
    echo "# Step lists that predate check-runbooks.sh rule 5 (see the header of scripts/check-runbooks.sh)."
    echo "# Format: <runbook>|<section heading>|<first step number>. Remove a line when its steps have a"
    echo "# \"What you should see\" and an \"If it doesn't work\" (writing them is part of the node dry run, issues #166 to #176)."
    [[ -f "$tmp/newbase.txt" ]] && sort -u "$tmp/newbase.txt"
  } >"$baseline"
  echo "check-runbooks: wrote $baseline ($( [[ -f "$tmp/newbase.txt" ]] && sort -u "$tmp/newbase.txt" | wc -l | tr -d ' ' || echo 0) entries)"
  exit 0
fi
awk -v DATA="$data" -v REGFILE="$REG" -v BASELINE="$base_arg" -f "$tmp/lint.awk" "${files[@]}" >"$tmp/out.txt" 2>"$tmp/awk.err" || true
if [[ -s "$tmp/awk.err" ]]; then echo "ERROR check-runbooks: awk failed:" >&2; cat "$tmp/awk.err" >&2; exit 2; fi
if [[ -s "$tmp/out.txt" ]]; then
  cat "$tmp/out.txt" >&2
  echo "check-runbooks: $(wc -l <"$tmp/out.txt" | tr -d ' ') problem(s) in ${#files[@]} runbook(s)" >&2
  exit 1
fi
echo "Runbooks true to the code and the specs (${#files[@]} files, $(grep -c '^C' "$data") commands, $(grep -c '^K' "$data") registry codes)"
