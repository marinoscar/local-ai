#!/usr/bin/env bash
# test-agent-ci.sh: prove scripts/agent-ci.sh in a throwaway repo with a tiny Go module (offline).
# Covers: skip without agent/, green path, failing test, stale vendor (red), static amd64+arm64 builds,
# reproducible builds. staticcheck is skipped here (it needs the network); CI runs it for real.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"
SRC="$PWD/scripts/agent-ci.sh"
command -v go >/dev/null 2>&1 || { echo "go not installed: skipping agent-ci tests"; exit 0; }
pass=0; failn=0
t() { # t <name> <expected-exit> <output-fragment or ""> cmd...
  local name="$1" want="$2" frag="$3"; shift 3
  local out rc; out="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq $want ]] && { [[ -z "$frag" ]] || grep -qF -- "$frag" <<<"$out"; }; then
    pass=$((pass + 1)); echo "  ok   $name"
  else
    failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want, fragment: ${frag:-none})"; sed 's/^/    | /' <<<"$out" | tail -15
  fi
}

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
export GOFLAGS=-mod=mod GOPROXY=off GOTOOLCHAIN=local SKIP_STATICCHECK=1 GIT_CONFIG_GLOBAL=/dev/null
repo="$work/repo"; mkdir -p "$repo/scripts" && cp "$SRC" "$repo/scripts/"
cd "$repo"
git init -q . && git config user.email t@example.test && git config user.name t
commit() { git add -A && git commit -qm "$1"; }
run() { ( cd "$repo" && "$@" ); }
unset GOFLAGS   # -mod=mod above only mattered for git init; real runs use defaults

echo "== no agent/ yet"
commit init
t "skips with a notice and exit 0" 0 "Skipping" run scripts/agent-ci.sh all

echo "== healthy module"
mkdir -p agent/cmd/aiagent agent/internal/x
printf 'module example.test/agent\n\ngo 1.22\n' >agent/go.mod
mkdir -p agent/internal/app
printf 'package app\n\nvar (\n\tVersion = "0.0.0-dev"\n\tCommit  = "none"\n\tBuiltAt = "unknown"\n)\n' >agent/internal/app/version.go
printf 'package main\n\nimport (\n\t"fmt"\n\n\t"example.test/agent/internal/app"\n)\n\nfunc main() { fmt.Println(app.Version, app.Commit, app.BuiltAt) }\n' >agent/cmd/aiagent/main.go
printf 'package x\n\nfunc Add(a, b int) int { return a + b }\n' >agent/internal/x/x.go
printf 'package x\n\nimport "testing"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal("bad")\n\t}\n}\n' >agent/internal/x/x_test.go
(cd agent && go mod vendor >/dev/null 2>&1); commit module
t "check passes" 0 "agent checks passed" run scripts/agent-ci.sh check
t "build makes static amd64 and arm64" 0 "static binaries" run scripts/agent-ci.sh build
t "artifact aiagent-linux-amd64 exists" 0 "" test -x "$repo/agent/dist/aiagent-linux-amd64"
t "artifact aiagent-linux-arm64 exists" 0 "" test -x "$repo/agent/dist/aiagent-linux-arm64"
a="$(sha256sum "$repo/agent/dist/aiagent-linux-amd64" | cut -d' ' -f1)"
OUT_DIR="$work/second" run scripts/agent-ci.sh build >/dev/null 2>&1
b="$(sha256sum "$work/second/aiagent-linux-amd64" 2>/dev/null | cut -d' ' -f1)"
t "two builds are byte-identical" 0 "" test -n "$a" -a "$a" = "$b"
t "default build reports the dev version" 0 "0.0.0-dev none unknown" "$repo/agent/dist/aiagent-linux-amd64"
rm -rf agent/dist
SOURCE_DATE_EPOCH=1700000000 AGENT_VERSION=1.2.3 run scripts/agent-ci.sh build >/dev/null 2>&1
t "AGENT_VERSION injects version, commit and a deterministic build time" 0 "1.2.3 $(git rev-parse --short=7 HEAD) 2023-11-14T22:13:20Z" "$repo/agent/dist/aiagent-linux-amd64"
rm -rf agent/dist

t "repro: two builds are identical" 0 "builds are reproducible" run scripts/agent-ci.sh repro

echo "== gofmt turns it red"
printf 'package x\n\nfunc Add(a,b int) int {return a+b}\n' >agent/internal/x/x.go
t "unformatted file -> exit 1" 1 "gofmt would change" run scripts/agent-ci.sh check
git checkout -q agent/internal/x/x.go

echo "== failing test turns it red"
printf 'package x\n\nfunc Add(a, b int) int { return a - b }\n' >agent/internal/x/x.go
t "go test failure -> exit 1" 1 "" run scripts/agent-ci.sh check
git checkout -q agent/internal/x/x.go

echo "== stale vendor turns it red"
mkdir -p dep && printf 'module example.test/dep\n\ngo 1.22\n' >dep/go.mod && printf 'package dep\n\nfunc Name() string { return "dep" }\n' >dep/dep.go
printf 'module example.test/agent\n\ngo 1.22\n\nrequire example.test/dep v0.0.0\n\nreplace example.test/dep => ../dep\n' >agent/go.mod
printf 'package main\n\nimport (\n\t"fmt"\n\n\t"example.test/dep"\n)\n\nfunc main() { fmt.Println(dep.Name()) }\n' >agent/cmd/aiagent/main.go
commit "use dep without vendoring"
t "go.mod changed, vendor not updated -> exit 1" 1 "'go mod vendor' changed files" run scripts/agent-ci.sh check
git reset -q --hard HEAD   # undo what `go mod vendor` left behind
git clean -qfd agent
(cd agent && go mod vendor >/dev/null 2>&1); commit "vendor dep"
t "after vendoring it is green" 0 "agent checks passed" run scripts/agent-ci.sh check
t "vendored build is static" 0 "static binaries" run scripts/agent-ci.sh build

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
