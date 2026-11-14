#!/usr/bin/env bash
# check-doctor-codes.sh: keep doctor codes (LAI-<AREA>-<NNN>) consistent. Offline, bash + grep + awk.
# Spec: docs/specs/doctor-codes.md §1.6. Registry: docs/specs/doctor-codes.md §2.
#
# Fails (exit 1) when:
#  1. a code row in an owner spec's doctor table is missing from the registry, or the reverse;
#     or its severity or meaning differ between the two;
#  2. a LAI-AAA-NNN string anywhere in README/AGENTS/CLAUDE.md, docs/, agent/, apps/, packages/,
#     installers/ is not defined in the registry (this also catches placeholders such as LAI-XXX-000);
#  3. a code is defined twice, or its area prefix is unknown, or it sits in the wrong registry
#     section or in a spec that does not own its area;
#  4. (only once agent/internal/doctor/codes.go exists) codes.go differs from the registry, defines
#     a code twice, a registry code has no table entry with a non-empty Spanish (`ES:`) title and
#     fix, an entry's Severity differs from the first word of the registry severity, or a non-test
#     Go file outside codes.go hard-codes a "LAI-..." literal;
#  5. a range (LAI-AAA-NNN..MMM) is used but is not a documented family (FAMILIES below);
#  6. the action table (registry section 1.7) lists a code twice, or (with codes.go) an entry's
#     `Action: "..."` differs from the action the table lists for the code, or is not listed there.
#
# codes.go layout read by rule 4: a const block `AGT001 = "LAI-AGT-001"` and a table of entries
# `ID: AGT001,` / `Severity: Warning,` / `Title: Msg{EN: "...", ES: "..."},` / `Fix: Msg{...},`,
# one field per line, and an optional `Action: "<action>",` line (see agent/internal/doctor/codes.go).
# The action table (rule 6) is the table of the registry section headed `### 1.7`: rows
# `| `<action>` | ... | <codes> |`, the codes in the last cell.
#
# Usage: scripts/check-doctor-codes.sh [--root DIR]     (DIR defaults to the git toplevel; tests use fixtures)
# Exit:  0 consistent, 1 problems (printed as `ERROR <file>:<line>: ...`), 2 usage error.
set -euo pipefail

root=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) root="${2:-}"; [[ -n "$root" ]] || { echo "usage: $0 [--root DIR]" >&2; exit 2; }; shift 2 ;;
    -h|--help) sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "usage: $0 [--root DIR]" >&2; exit 2 ;;
  esac
done
[[ -n "$root" ]] || root="$(git rev-parse --show-toplevel)"
cd "$root"

REG="docs/specs/doctor-codes.md"
[[ -f "$REG" ]] || { echo "ERROR $REG:0: registry not found under $root" >&2; exit 1; }

# Area prefixes (doctor-codes.md §1.1) and the spec file(s) that own each (§1.3).
# Format: AREA=file[,file...]; a file may carry a numeric block "file:050-069" meaning it owns only that block.
OWNERS="AGT=agent.md NET=agent.md HW=agent.md SEC=agent.md CLI=aictl.md LLM=llama-router.md CAT=catalog.md \
SYN=ark-sync.md ARK=ark-sync.md SVC=companion-services.md,survival-portal.md,music.md:150-179 CLD=cloud-app.md,agent.md:050-069 \
GW=gateway.md DSK=desktop.md INS=installer.md KIT=rebuild-kit.md DRL=drill.md"
# Documented families (ranges allowed in prose), doctor-codes.md §1.2/§1.6.
FAMILIES="LAI-CAT-010..019 LAI-CLD-050..069 LAI-INS-040..044"
CODES_GO="agent/internal/doctor/codes.go"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Extract table rows `| LAI-AAA-NNN | sev | meaning | ...` as TSV: file line code sev meaning section.
extract() { # extract <file>
  awk -v F="$1" '
    function trim(s) { gsub(/^[ \t]+|[ \t]+$/, "", s); gsub(/[ \t]+/, " ", s); return s }
    /^### / { sect = ""; if (match($0, /LAI-[A-Z]+/)) sect = substr($0, RSTART + 4, RLENGTH - 4) }
    /^\| *LAI-[A-Z]+-[0-9][0-9][0-9] *\|/ {
      n = split($0, c, "|")
      printf "%s\t%d\t%s\t%s\t%s\t%s\n", F, NR, trim(c[2]), trim(c[3]), trim(c[4]), sect
    }' "$1"
}

extract "$REG" >"$tmp/reg.tsv"

# The action table of registry section 1.7, as TSV: line code action (one line per listed code).
awk '
  /^### / { inact = ($0 ~ /^### 1\.7 /); next }
  /^## / { inact = 0; next }
  inact && /^\| *`[a-z][a-z.:]*` *\|/ {
    match($0, /`[a-z][a-z.:]*`/); act = substr($0, RSTART + 1, RLENGTH - 2)
    n = split($0, c, "|"); last = c[n - 1]
    while (match(last, /LAI-[A-Z]+-[0-9][0-9][0-9]/)) {
      printf "%d\t%s\t%s\n", NR, substr(last, RSTART, RLENGTH), act
      last = substr(last, RSTART + RLENGTH)
    }
  }' "$REG" >"$tmp/actions.tsv"
: >"$tmp/spec.tsv"
for f in docs/specs/*.md; do
  [[ "$f" == "$REG" ]] && continue
  extract "$f" >>"$tmp/spec.tsv"
done

# Mentions anywhere in the tree.
paths=()
for p in README.md AGENTS.md CLAUDE.md docs agent apps packages installers; do [[ -e "$p" ]] && paths+=("$p"); done
: >"$tmp/mentions.txt"; : >"$tmp/ranges.txt"
if [[ ${#paths[@]} -gt 0 ]]; then
  grep -rnIoE --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=vendor --exclude-dir=worktrees \
    '\bLAI-[A-Z]+-[0-9]{3}\b' "${paths[@]}" >"$tmp/mentions.txt" 2>/dev/null || true
  grep -rnIoE --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=vendor --exclude-dir=worktrees \
    '\bLAI-[A-Z]+-[0-9]{3}\.\.[0-9]{3}\b' "${paths[@]}" >"$tmp/ranges.txt" 2>/dev/null || true
fi

# codes.go (only when it exists).
: >"$tmp/gocodes.txt"; : >"$tmp/golit.txt"
has_go=0
if [[ -f "$CODES_GO" ]]; then
  has_go=1
  grep -noE '"LAI-[A-Z]+-[0-9]{3}"' "$CODES_GO" | tr -d '"' | sed "s|^|$CODES_GO:|" >"$tmp/gocodes.txt" || true
  if [[ -d agent ]]; then
    grep -rnE --include='*.go' --exclude='*_test.go' --exclude-dir=vendor '"LAI-[A-Z]+-[0-9]{3}"' agent 2>/dev/null \
      | grep -v "^$CODES_GO:" | cut -d: -f1,2 >"$tmp/golit.txt" || true
  fi
  # The table entries: `C name code line` for each constant, `E name line severity title_es fix_es`
  # for each entry (ID: NAME, ... Severity: X, ... Title: Msg{EN: "..", ES: ".."}, Fix: Msg{...}).
  awk '
    /^[ \t]*[A-Za-z][A-Za-z0-9_]*[ \t]*=[ \t]*"LAI-[A-Z]+-[0-9][0-9][0-9]"/ {
      name = $1; match($0, /"LAI-[A-Z]+-[0-9][0-9][0-9]"/)
      printf "C\t%s\t%s\t%d\n", name, substr($0, RSTART + 1, RLENGTH - 2), NR; next
    }
    function flush() { if (cur != "") printf "E\t%s\t%d\t%s\t%d\t%d\t%s\n", cur, line, sev, tes, fes, act }
    /^[ \t]*ID:[ \t]*[A-Za-z][A-Za-z0-9_]*,/ { flush(); cur = $2; sub(/,$/, "", cur); line = NR; sev = ""; tes = 0; fes = 0; act = ""; next }
    /^[ \t]*Action:[ \t]*"/ { match($0, /"[^"]*"/); act = substr($0, RSTART + 1, RLENGTH - 2); next }
    /^[ \t]*Severity:/ { v = $2; sub(/,$/, "", v); sev = tolower(v); next }
    /^[ \t]*Title:/ { if ($0 ~ /ES:[ \t]*"[^"]/) tes = 1; next }
    /^[ \t]*Fix:/   { if ($0 ~ /ES:[ \t]*"[^"]/) fes = 1; next }
    END { flush() }
  ' "$CODES_GO" >"$tmp/goentries.tsv"
fi

awk -v owners="$OWNERS" -v families="$FAMILIES" -v hasgo="$has_go" -v reg="$REG" -v tmp="$tmp" -v CODES_GO_LOC="$CODES_GO" '
  function area(code,   p) { split(code, p, "-"); return p[2] }
  function num(code,   p)  { split(code, p, "-"); return p[3] + 0 }
  function err(loc, msg)   { print "ERROR " loc ": " msg; bad++ }
  function base(path,   p, n) { n = split(path, p, "/"); return p[n] }
  function owned(a, file, n,   i, k, spec, f, blk, lo, hi) {
    if (!(a in own)) return 0
    k = split(own[a], spec, ",")
    for (i = 1; i <= k; i++) {
      f = spec[i]; blk = ""
      if (index(f, ":")) { blk = substr(f, index(f, ":") + 1); f = substr(f, 1, index(f, ":") - 1) }
      if (f != base(file)) continue
      if (blk == "") return 1
      split(blk, r, "-"); if (n >= r[1] + 0 && n <= r[2] + 0) return 1
    }
    return 0
  }
  BEGIN {
    FS = "\t"
    n = split(owners, o, " ")
    for (i = 1; i <= n; i++) { split(o[i], kv, "="); own[kv[1]] = kv[2] }
    n = split(families, fm, " "); for (i = 1; i <= n; i++) fam[fm[i]] = 1

    # --- registry
    while ((getline line < (tmp "/reg.tsv")) > 0) {
      split(line, f, "\t")
      code = f[3]; a = area(code)
      if (!(a in own)) err(f[1] ":" f[2], code " has an unknown area prefix \"" a "\" (see doctor-codes.md section 1.1)")
      if (code in rsev) err(f[1] ":" f[2], code " is defined twice in the registry (first at line " rline[code] ")")
      else { rsev[code] = f[4]; rmean[code] = f[5]; rline[code] = f[2] }
      if (f[6] != "" && f[6] != a) err(f[1] ":" f[2], code " is listed in the LAI-" f[6] " section but its area is " a)
    }
    # --- owner spec tables
    while ((getline line < (tmp "/spec.tsv")) > 0) {
      split(line, f, "\t")
      code = f[3]; a = area(code); loc = f[1] ":" f[2]
      if (code in sloc) { err(loc, code " is defined twice in owner specs (also at " sloc[code] ")"); continue }
      sloc[code] = loc
      if (!(a in own)) { err(loc, code " has an unknown area prefix \"" a "\""); continue }
      if (!owned(a, f[1], num(code))) err(loc, code " is defined in " base(f[1]) ", which does not own area " a " (owner: " own[a] ")")
      if (!(code in rsev)) { err(loc, code " is in the spec table but missing from the registry (" reg ")"); continue }
      if (rsev[code] != f[4])  err(loc, code " severity differs: spec \"" f[4] "\" vs registry \"" rsev[code] "\" (" reg ":" rline[code] ")")
      if (rmean[code] != f[5]) err(loc, code " meaning differs from the registry (" reg ":" rline[code] "): spec \"" f[5] "\" vs registry \"" rmean[code] "\"")
    }
    for (c in rsev) if (!(c in sloc)) err(reg ":" rline[c], c " is in the registry but missing from its owner spec table")

    # --- the action table (section 1.7): one action per code
    while ((getline line < (tmp "/actions.tsv")) > 0) {
      split(line, f, "\t")
      if (f[2] in ract) { err(reg ":" f[1], f[2] " is listed twice in the action table (also at line " raline[f[2]] ")"); continue }
      ract[f[2]] = f[3]; raline[f[2]] = f[1]
    }

    # --- mentions anywhere
    while ((getline line < (tmp "/mentions.txt")) > 0) {
      i = match(line, /:[0-9]+:LAI-/); if (!i) continue
      loc = substr(line, 1, RSTART + RLENGTH - 5); code = substr(line, RSTART + RLENGTH - 4)
      sub(/:$/, "", loc)
      if (!(code in rsev)) err(loc, code " is not defined in the registry (placeholders are not allowed)")
    }
    # --- ranges
    while ((getline line < (tmp "/ranges.txt")) > 0) {
      i = match(line, /:[0-9]+:LAI-/); if (!i) continue
      loc = substr(line, 1, RSTART + RLENGTH - 5); rng = substr(line, RSTART + RLENGTH - 4)
      split(rng, p, "\\.\\."); split(p[1], q, "-")
      if (!(rng in fam)) err(loc, rng " is a range outside a documented family (" families ")")
      else if (p[2] + 0 < q[3] + 0) err(loc, rng " ends before it starts")
    }
    # --- codes.go
    if (hasgo) {
      while ((getline line < (tmp "/gocodes.txt")) > 0) {
        i = match(line, /:[0-9]+:LAI-/); if (!i) continue
        loc = substr(line, 1, RSTART + RLENGTH - 5); code = substr(line, RSTART + RLENGTH - 4)
        if (code in goseen) err(loc, code " is defined twice in codes.go (first at " goseen[code] ")")
        else goseen[code] = loc
        if (!(code in rsev)) err(loc, code " is in codes.go but not in the registry")
      }
      for (c in rsev) if (!(c in goseen)) err(reg ":" rline[c], c " is in the registry but missing from codes.go")
      # table entries: each registry code needs one, with a severity and a Spanish title and fix
      while ((getline line < (tmp "/goentries.tsv")) > 0) {
        split(line, f, "\t")
        if (f[1] == "C") gname[f[2]] = f[3]
        else ent[f[2]] = f[3] "\t" f[4] "\t" f[5] "\t" f[6] "\t" f[7]
      }
      for (nm in ent) if (!(nm in gname)) { split(ent[nm], g, "\t"); err("agent/internal/doctor/codes.go:" g[1], "table entry " nm " is not a constant defined in codes.go") }
      for (nm in gname) {
        c = gname[nm]
        if (!(c in rsev)) continue
        if (!(nm in ent)) { err(goseen[c], c " has no table entry in codes.go (needs Severity, and Title and Fix with a Spanish ES text)"); continue }
        split(ent[nm], g, "\t")
        split(rsev[c], rs, " ")
        if (g[2] != rs[1]) err(CODES_GO_LOC ":" g[1], c " severity in codes.go is \"" g[2] "\" but the registry says \"" rs[1] "\"")
        if (g[3] != 1) err(CODES_GO_LOC ":" g[1], c " has no Spanish (ES) title in codes.go")
        if (g[4] != 1) err(CODES_GO_LOC ":" g[1], c " has no Spanish (ES) fix in codes.go")
        if (g[5] != "" && !(c in ract)) err(CODES_GO_LOC ":" g[1], c " has action \"" g[5] "\" in codes.go but the action table (" reg " section 1.7) lists none for it")
        else if (c in ract && g[5] != ract[c]) err(CODES_GO_LOC ":" g[1], c " action in codes.go is \"" g[5] "\" but the action table (" reg ":" raline[c] ") says \"" ract[c] "\"")
      }
      while ((getline line < (tmp "/golit.txt")) > 0)
        err(line, "hard-coded LAI code literal outside codes.go; use the constant defined in codes.go")
    }
    if (bad > 0) { printf "✗ %d doctor-code problem(s).\n", bad > "/dev/stderr"; exit 1 }
    nrs = 0; for (c in rsev) nrs++
    printf "✓ Doctor codes consistent (%d registry codes%s).\n", nrs, hasgo ? ", codes.go checked" : ", codes.go not present yet"
  }' /dev/null
