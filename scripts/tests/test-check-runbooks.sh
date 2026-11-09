#!/usr/bin/env bash
# test-check-runbooks.sh: run scripts/check-runbooks.sh against a throwaway repo root with a FAKE aictl/aiagent
# (canned help text), one runbook per rule, then against the real repository with the real binaries built from
# agent/. The real run is skipped when Go is missing (CHECK_RUNBOOKS_SKIP_REAL=1 skips it too).
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2
CHECK="$PWD/scripts/check-runbooks.sh"
pass=0; failn=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# ---- the fake binaries -------------------------------------------------------------------------------------------
mkdir -p "$tmp/bin"
cat >"$tmp/bin/aictl" <<'FAKE'
#!/usr/bin/env bash
# Fake aictl: only `help [command...]`.
[[ "${1:-}" == "help" ]] || exit 9
shift
case "$*" in
  "")
    cat <<'EOT'
Commands:
  ark                            10 s   Show the ark.
  ark prune          [admin]     3 min  Delete content.
  ark sync                       -      Download.
  doctor                         1 min  Run the health checks.
  mode                           20 min Show the mode.
  restart                        2 min  Restart a service.
  services                       10 s   List the services.
  services restart               2 min  Restart a service.

Marks: [local] [root]

Global flags (before the command name):
  --json              Machine output
  -y, --yes           Answer yes
  --lang en|es        Language
EOT
    ;;
  "ark prune") echo "Usage: aictl ark prune [--root <id>]... [--orphans] [--dry-run] [--yes]"; echo "  -orphans"; ;;
  "ark sync") echo "Usage: aictl ark sync [--root <id>]... [--wait] [--scheduled] | --cancel"; ;;
  "ark") echo "Usage: aictl ark"; echo "Subcommands:"; echo "  prune"; echo "  sync"; ;;
  doctor) echo "Usage: aictl doctor [--json]"; ;;
  mode) echo "Usage: aictl mode [online|offline|survival] [--no-wait]"; ;;
  restart) echo "Usage: aictl restart <llama|kiwix|whisper> [--force]"; ;;
  services) echo "Usage: aictl services"; ;;
  "services restart") echo "Usage: aictl services restart <name>"; ;;
  *) echo "No help for $*" >&2; exit 2 ;;
esac
FAKE
cat >"$tmp/bin/aiagent" <<'FAKE'
#!/usr/bin/env bash
case "$*" in
  help|"")
    cat <<'EOT'
usage:
  aiagent serve                      run the daemon
  aiagent ctl [args...]              run the CLI (same as aictl)
  aiagent migrate [llama-qwen] [...] legacy migration
  aiagent setup <step> [flags]       internal installer steps
EOT
    ;;
  setup)
    cat <<'EOT'
usage: aiagent setup <step> [flags]
  node-yaml    write node.yaml
  harden       laptop hardening
EOT
    ;;
  "migrate --help") echo "usage: aiagent migrate [llama-qwen] [--dry-run] [--rollback]"; exit 2 ;;
  "setup harden --help") echo "usage: aiagent setup harden [--check] [--revert]"; exit 2 ;;
  *) exit 2 ;;
esac
FAKE
chmod +x "$tmp/bin/aictl" "$tmp/bin/aiagent"

# ---- the throwaway repository ---------------------------------------------------------------------------------
repo="$tmp/repo"
mkdir -p "$repo/docs/specs" "$repo/docs/architecture" "$repo/docs/runbooks" "$repo/scripts"
cat >"$repo/docs/specs/doctor-codes.md" <<'EOT'
# Registry
| Code | Severity | Meaning | Fix |
|---|---|---|---|
| LAI-SVC-001 | error | Library empty | Plug the drive |
| LAI-INS-040 | warning | Lid | Reboot |
| LAI-INS-044 | warning | SSH | Check |
EOT
cat >"$repo/docs/architecture/overview.md" <<'EOT'
# Overview

## 3. Canonical names (single source of truth)

### 3.2 Filesystem
| Path | Owner | Contents |
|---|---|---|
| `/opt/localai/bin/aiagent` | root | The agent |
| `/usr/local/bin/aictl` | symlink | CLI |
| `/etc/localai/node.yaml` | root | Node settings |
| `/etc/localai/catalog/` | root | The catalog |
| `/var/lib/localai/` | localai | State. `state.json`, `sync/` |
| `/srv/localai/` | localai | Content root |

### 3.3 Content root layout
```
<root>/
  READY.json                         readiness
  models/<model-id>/<file>.gguf      weights
  docs/                              rendered runbooks
```

## 4. Key flows
EOT

n=0
run() { # run <name> <expected-exit> <fragment>... ; the runbook text comes on stdin
  local name="$1" want="$2"; shift 2
  n=$((n + 1))
  local rb="docs/runbooks/case.md" out rc okc=1
  cat >"$repo/$rb"
  rm -f "$repo/scripts/check-runbooks.baseline"
  [[ -z "${BASELINE_TEXT:-}" ]] || printf '%s\n' "$BASELINE_TEXT" >"$repo/scripts/check-runbooks.baseline"
  out="$("$CHECK" --root "$repo" --bin-dir "$tmp/bin" "$rb" 2>&1)"; rc=$?
  [[ $rc -eq $want ]] || okc=0
  local frag
  for frag in "$@"; do grep -qF -- "$frag" <<<"$out" || { okc=0; echo "    missing fragment: $frag"; }; done
  if [[ $okc -eq 1 ]]; then pass=$((pass + 1)); echo "  ok   $name (exit $rc)"
  else failn=$((failn + 1)); echo "  FAIL $name (exit $rc, want $want)"; sed 's/^/    | /' <<<"$out"; fi
}

echo "== fake binaries"
run good 0 "Runbooks true" <<'EOT'
# Good

1. **Check.** Run `aictl doctor --json` and `sudo aictl --lang es ark sync --root ark-1 --wait`.

   ```
   sudo aictl --json ark prune --orphans --dry-run | tee out.txt
   aictl restart llama --force
   /opt/localai/bin/aiagent ctl doctor
   sudo aiagent migrate llama-qwen --dry-run
   sudo aiagent setup harden --check
   ```

   - **What you should see:** a list. Files: `/etc/localai/node.yaml`, `/etc/localai/catalog/models.yaml`, `/var/lib/localai/sync/x`, `/srv/localai/docs/01-x.html`, `<root>/docs/01-x.html`.
   - **If it doesn't work:** `LAI-SVC-001`, or a family `LAI-INS-040..044`.
2. **Next.** Do `aictl mode survival`.
   - **What you should see:** done.
   - **If it doesn't work:** see 07.
EOT
run unknown-command 1 '`aictl bogus` is not an aictl command' '`aictl ark foo` is not an aictl command' <<'EOT'
# T
Type `aictl bogus` or `aictl ark foo`.
EOT
run unknown-flag 1 'the flag --status is not listed by `aictl help ark sync`' 'the flag --allow-last-copy is not listed by `aictl help ark prune`' 'the flag --nope is not listed by `aictl help doctor`' <<'EOT'
# T
Run `aictl ark sync --status`, `aictl ark prune --allow-last-copy` and:

```
aictl doctor \
  --nope
```
EOT
run flag-ends-at-next-command 0 "Runbooks true" <<'EOT'
# T
Run `aictl restart llama`. If it fails again, run `aictl doctor --json`. Then `aictl ark sync --scheduled`.
EOT
run alternation 1 '`aictl ark nope` is not an aictl command' <<'EOT'
# T
Use `aictl ark prune|sync` or `aictl ark sync|nope`.
EOT
run aiagent-unknown 1 '`aiagent frobnicate` is not in `aiagent help`' '`aiagent setup nope` is not in `aiagent setup`' 'the flag --zzz is not in the aiagent usage text' <<'EOT'
# T
`aiagent frobnicate`, `aiagent setup nope`, `aiagent migrate --zzz`, and `aiagent ctl ark sync --bad`.
EOT
run doctor-codes 1 'LAI-XXX-000 is a placeholder' 'LAI-SVC-999 is not defined in the registry' 'the range LAI-INS-040..049' <<'EOT'
# T
Codes: LAI-XXX-000, LAI-SVC-999, LAI-INS-040..049, but LAI-SVC-001 is fine.
EOT
run numbering 1 'step 4 follows step 2' 'step 3 follows step 1' <<'EOT'
# T
1. a
   - **What you should see:** x
   - **If it doesn't work:** y
2. b
   - **What you should see:** x
   - **If it doesn't work:** y
4. c
   - **What you should see:** x
   - **If it doesn't work:** y

## Next

1. restart is fine
   - **What you should see:** x
   - **If it doesn't work:** y
3. jump
EOT
run missing-expect 1 'without "What you should see" and "If it doesn'"'"'t work"' 'without "If it doesn'"'"'t work"' <<'EOT'
# T

1. **A step** with nothing after it.

## Part 2

2. **Half.**
   - **What you should see:** something.
EOT
run adjacent-steps-share 0 "Runbooks true" <<'EOT'
# T

1. Press **1**.
2. Press **2**.
   - **What you should see:** the page.
   - **If it doesn't work:** try again.
EOT
run spanish-ok 0 "Runbooks true" <<'EOT'
# T

1. Presione **1**.
   - **Lo que debería ver:** la página.
   - **Si no funciona:** intente otra vez.
EOT
run exempt-step 0 "Runbooks true" <<'EOT'
# T

1. **Buy a drive.** <!-- runbook-lint: no-expect -->

## Part 2

<!-- runbook-lint: no-expect-section (advice) -->

1. Advice.
2. More advice.
EOT
BASELINE_TEXT='docs/runbooks/case.md|Old part|1' run baseline-tolerates 0 "Runbooks true" <<'EOT'
# T

## Old part

1. **Old step** with nothing after it.
EOT
BASELINE_TEXT='docs/runbooks/case.md|Old part|1' run baseline-new-still-fails 1 'numbered step without' <<'EOT'
# T

## Old part

1. **Old step** with nothing after it.

## New part

1. **New step** with nothing after it.
EOT
BASELINE_TEXT='docs/runbooks/case.md|Old part|1' run baseline-stale 1 'stale baseline entry "docs/runbooks/case.md|Old part|1"' <<'EOT'
# T

## Old part

1. **Old step.**
   - **What you should see:** a.
   - **If it doesn't work:** b.
EOT
run paths 1 '/etc/localai/nope is not a canonical path' '/opt/localai/bin/zzz is not a canonical path' '/var/lib/localai/zzz is not a canonical path' 'no "kit2" folder' 'rendered manuals are flat' <<'EOT'
# T
Files: `/etc/localai/nope`, `/opt/localai/bin/zzz`, `/var/lib/localai/zzz`, `<root>/kit2/x`.
The manual is on the drive: `localai/docs/runbooks/06-rebuild.html` and `<root>/docs/runbooks/06-rebuild.html`.
A link [x](/etc/localai/ignored) is not checked.
EOT
run lost-line-break 1 'two sentences are glued together' 'a list item is glued to the text before it' <<'EOT'
# T
Keep it plugged in.Yellow after 30 days.
   - Do this so it stays up to date.   - **Next** thing.
EOT
echo "== usage errors"
n=$((n + 1))
"$CHECK" --root "$repo" --bin-dir "$tmp/nosuchdir" docs/runbooks/case.md >/dev/null 2>&1; rc=$?
if [[ $rc -eq 2 ]]; then pass=$((pass + 1)); echo "  ok   missing binaries (exit 2)"; else failn=$((failn + 1)); echo "  FAIL missing binaries (exit $rc, want 2)"; fi
n=$((n + 1))
"$CHECK" --nope >/dev/null 2>&1; rc=$?
if [[ $rc -eq 2 ]]; then pass=$((pass + 1)); echo "  ok   unknown option (exit 2)"; else failn=$((failn + 1)); echo "  FAIL unknown option (exit $rc, want 2)"; fi
n=$((n + 1))
cp "$repo/docs/runbooks/case.md" "$repo/docs/runbooks/x.md" 2>/dev/null
rm -f "$repo/scripts/check-runbooks.baseline"
printf '# T\n\n## Part\n\n1. **No answers.**\n' >"$repo/docs/runbooks/x.md"; rm -f "$repo/docs/runbooks/case.md"
"$CHECK" --root "$repo" --bin-dir "$tmp/bin" --write-baseline >/dev/null 2>&1
if grep -qxF 'docs/runbooks/x.md|Part|1' "$repo/scripts/check-runbooks.baseline" && "$CHECK" --root "$repo" --bin-dir "$tmp/bin" >/dev/null 2>&1; then
  pass=$((pass + 1)); echo "  ok   --write-baseline makes the run pass"
else failn=$((failn + 1)); echo "  FAIL --write-baseline"; fi

echo "== real repository"
if [[ "${CHECK_RUNBOOKS_SKIP_REAL:-0}" == "1" ]]; then
  echo "  skip (CHECK_RUNBOOKS_SKIP_REAL=1)"
elif ! command -v go >/dev/null 2>&1 && [[ ! -x /usr/local/go/bin/go ]]; then
  echo "  skip (Go is not installed)"
else
  out="$("$CHECK" 2>&1)"; rc=$?
  if [[ $rc -eq 0 ]]; then pass=$((pass + 1)); echo "  ok   the runbooks are true: $out"
  else failn=$((failn + 1)); echo "  FAIL real runbooks:"; sed 's/^/    | /' <<<"$out"; fi
fi

echo; echo "passed: $pass  failed: $failn"
[[ $failn -eq 0 ]]
