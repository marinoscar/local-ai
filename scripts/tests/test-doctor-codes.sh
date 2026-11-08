#!/usr/bin/env bash
# test-doctor-codes.sh: run scripts/check-doctor-codes.sh against fixture roots in scripts/tests/doctor/.
# Each case is a mini repo. Expected exit code and message fragments are listed below.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"
CHECK=scripts/check-doctor-codes.sh
FIX=scripts/tests/doctor
pass=0; failn=0

run() { # run <case> <expected-exit> <fragment>...
  local name="$1" want="$2"; shift 2
  local out rc
  out="$("$CHECK" --root "$FIX/$name" 2>&1)"; rc=$?
  local okc=1
  [[ $rc -eq $want ]] || okc=0
  for frag in "$@"; do grep -qF -- "$frag" <<<"$out" || { okc=0; echo "    missing fragment: $frag"; }; done
  if [[ $okc -eq 1 ]]; then pass=$((pass + 1)); echo "  ok   $name (exit $rc)"; else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want)"; echo "$out" | sed 's/^/    | /'; fi
}

run good 0 "Doctor codes consistent (2 registry codes, codes.go not present yet)"
run codes-go-good 0 "codes.go checked"
run duplicate 1 "LAI-CAT-001 is defined twice in the registry"
run unknown-area 1 'unknown area prefix "ZZZ"'
run mismatch 1 "LAI-CAT-001 severity differs" "LAI-CAT-010 meaning differs"
run missing 1 "LAI-CAT-003 is in the spec table but missing from the registry" "LAI-CAT-002 is in the registry but missing from its owner spec table"
run undefined-ref 1 "LAI-CAT-099 is not defined in the registry" "LAI-XXX-000 is not defined in the registry"
run wrong-section 1 "listed in the LAI-AGT section but its area is CAT"
run wrong-owner 1 "defined in gateway.md, which does not own area CAT"
run bad-range 1 "LAI-CAT-030..039 is a range outside a documented family"
run codes-go-drift 1 "LAI-CAT-001 is defined twice in codes.go" "LAI-CAT-077 is in codes.go but not in the registry" "LAI-CAT-010 is in the registry but missing from codes.go" "hard-coded LAI code literal outside codes.go"
run codes-go-no-es 1 "LAI-CAT-001 has no Spanish (ES) title in codes.go" "LAI-CAT-010 has no Spanish (ES) fix in codes.go"
run codes-go-no-entry 1 "LAI-CAT-010 has no table entry in codes.go"
run codes-go-severity 1 'LAI-CAT-001 severity in codes.go is "critical" but the registry says "error"'
run codes-go-action 1 "LAI-CAT-001 is listed twice in the action table" 'LAI-CAT-001 action in codes.go is "service.restart:kiwix" but the action table' 'LAI-CAT-010 has action "ark.import" in codes.go but the action table'

echo "== real repo =="
out="$("$CHECK" 2>&1)"; rc=$?
if [[ $rc -eq 0 ]]; then pass=$((pass + 1)); echo "  ok   repo is consistent: $out"; else failn=$((failn + 1)); echo "  FAIL repo:"; echo "$out" | sed 's/^/    | /'; fi

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
