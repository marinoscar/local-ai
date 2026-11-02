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
#     a code twice, or a non-test Go file outside codes.go hard-codes a "LAI-..." literal;
#  5. a range (LAI-AAA-NNN..MMM) is used but is not a documented family (FAMILIES below).
#
# Not checked yet: the Spanish (`es`) text in codes.go. AGENT-11 adds that check with the codes.go layout.
#
# Usage: scripts/check-doctor-codes.sh [--root DIR]     (DIR defaults to the git toplevel; tests use fixtures)
# Exit:  0 consistent, 1 problems (printed as `ERROR <file>:<line>: ...`), 2 usage error.
set -euo pipefail

root=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) root="${2:-}"; [[ -n "$root" ]] || { echo "usage: $0 [--root DIR]" >&2; exit 2; }; shift 2 ;;
    -h|--help) sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
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
SYN=ark-sync.md ARK=ark-sync.md SVC=companion-services.md,survival-portal.md CLD=cloud-app.md,agent.md:050-069 \
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
fi

awk -v owners="$OWNERS" -v families="$FAMILIES" -v hasgo="$has_go" -v reg="$REG" -v tmp="$tmp" '
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
      while ((getline line < (tmp "/golit.txt")) > 0)
        err(line, "hard-coded LAI code literal outside codes.go; use the constant defined in codes.go")
    }
    if (bad > 0) { printf "✗ %d doctor-code problem(s).\n", bad > "/dev/stderr"; exit 1 }
    nrs = 0; for (c in rsev) nrs++
    printf "✓ Doctor codes consistent (%d registry codes%s).\n", nrs, hasgo ? ", codes.go checked" : ", codes.go not present yet"
  }' /dev/null
