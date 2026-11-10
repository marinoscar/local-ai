# Spec: Node installer (`installers/install-node.sh`)

> **Status:** approved design · **Epic:** E5 · **Code:** `installers/install-node.sh` (bash driver), `agent/cmd/aiagent/setup*.go` (`aiagent setup <step>` subcommands, Go, in package main beside `migrate.go`) · **Doctor area:** `LAI-INS`
> Canonical names: [overview.md](../architecture/overview.md) §3. Laptop source material: [source/laptop-server-setup.md](../source/laptop-server-setup.md). VPS source: [source/vps-ai-nodes-setup.md](../source/vps-ai-nodes-setup.md) §6. Migration: [llama-router.md](llama-router.md) §4.9. Kit: [rebuild-kit.md](rebuild-kit.md).

---

## 1. Purpose and scope

One command turns a fresh or existing Ubuntu machine into a Local AI node: online (downloads from the internet) or offline (everything from an AI-ARK drive). It is idempotent (safe to re-run), has a dry run, migrates today's `llama-qwen.service` nodes in place, hardens laptops to run as servers, and ends with a plain checklist of anything left for a human (reboot, `tailscale up`, VPS publishing).

In scope: CLI flags; every phase in order with its idempotency check; laptop hardening (all steps of the source guide); users, groups, directories; packages; the llama.cpp build or reuse; migration hand-off; agent, units, key, Tailscale, PIN, enrollment, initial sync; summary with the VPS checklist; uninstall; repair; logging.

Non-goals:
- Installing Ubuntu itself (see [runbooks/06-rebuild-a-node-offline.md](../runbooks/06-rebuild-a-node-offline.md)).
- Configuring the VPS (phase 1 prints a checklist; automation is phase 2, [cloud-app.md](cloud-app.md)).
- Installing or upgrading NVIDIA drivers on a machine where `nvidia-smi` already works.
- Choosing models (the profile does).
- Anything on non-Ubuntu systems.

## 2. Concepts

| Term | Meaning |
|---|---|
| **Driver** | The bash script. Small: argument parsing, logging, preflight, package installation, and calling `aiagent setup …` for everything else |
| **Setup step** | A Go subcommand `aiagent setup <step> [--dry-run] [--json]`, run as root (the read-only `preflight` and `profile-suggest` also run as a normal user), idempotent, testable. The same binary later runs as the agent. This keeps logic out of bash |
| **Source** | Where artifacts come from: `online` (GitHub releases, Ubuntu archive, HF via sync) or `ark` (`--from-ark <mount>`) |
| **Phase state** | `/var/lib/localai/install/state.json`: per phase `{status, version, finished_at, details}` |

## 3. Interfaces

### 3.1 Invocation
Online one-liner (as shown by the cloud **Add node** dialog, [cloud-app.md](cloud-app.md)):
```bash
curl -fsSL https://github.com/marinoscar/local-ai/releases/download/<agent-tag>/install-node.sh -o install-node.sh
sha256sum install-node.sh        # compare with the value shown in the cloud dialog
sudo bash install-node.sh --node-id ai-n4 --cloud-url https://ai.marin.cr --enroll
```
`--enroll` asks for the `nod_` credential on `/dev/tty` with hidden input, so it never appears in argv, `ps` or shell history.
Offline:
```bash
sudo bash /media/<user>/AI-ARK/localai/kit/installers/install-node.sh --from-ark /media/<user>/AI-ARK
```
(The script is never piped straight into `bash` from the network: it's downloaded, checked, then run.)

#### 3.1.1 Guided mode (no arguments on a terminal)
[ADR-0009](../decisions/0009-optimized-for-humans.md) rules 1 and 2, issue #429. `sudo bash install-node.sh` with **no arguments** (or with only `--lang <en|es>`), when stdout is a terminal and `/dev/tty` can be read, asks the choices that matter instead of requiring flags. Any other argument, no terminal, or `LAI_INSTALLER_GUIDED=1` skips it, so automation is unchanged. It runs after `--help`/`--version` and the stamp check, and before the root check (it tells a non-root user the exact `sudo` line and exits 3).

1. **Node name.** The id in the existing `node.yaml` is the suggestion (Enter keeps it). Otherwise there is no default. A bad answer is explained ("ai-n and a number, for example ai-n4") and asked again. Upper case is accepted. After 5 bad answers, or at the end of input, it exits 2. A name different from the existing one is warned about (`LAI-INS-020`, which P9 still enforces).
2. **Source.** A mounted AI-ARK drive (`/media/*/AI-ARK` with `localai/`) is offered: "Install from the drive? (Y/n)" → `--from-ark`. With no drive it says everything is downloaded, and how to install offline instead.
3. **Cloud.** "Connect now? (Y/n)" online, `(y/N)` from a drive → `--cloud-url <url> --enroll`. The credential is still asked later, hidden, by P13.
4. **Preview.** "First show what would be done, without changing anything? (recommended) (Y/n)".

Then it shows the choices and **the same choices as one typed line**. With the preview it runs itself with those flags plus `--dry-run`, then asks "Install for real now? (y/N)". Without it, it asks "Start the installation now? (y/N)". A No exits 4 with "Nothing was changed". A failing preview exits with the preview's code. A Yes runs `bash install-node.sh <flags>` again with `LAI_INSTALLER_GUIDED=1`, and from there everything is the normal path (§4.3). Yes/no answers accept y/yes/s/si and n/no; anything else is asked again, up to three times, then the default is taken. Every question, hint, summary line and error of the guided mode is in English or Spanish (issue #436, §8 Language): the language is `--lang`, else the locale; the Spanish hints read `(S/n)` / `(s/N)`. The preview and the final run always get `--lang <the language in use>`, so they speak the same language as the questions. Dev hooks (with `LAI_INSTALLER_DEV=1` only): `LAI_INSTALLER_GUIDE`, `LAI_INSTALLER_GUIDE_IN`/`_OUT`, `LAI_INSTALLER_MEDIA`, `LAI_INSTALLER_GUIDE_RECORD`, `LAI_INSTALLER_GUIDE_PREVIEW_RC`. Tests: `scripts/tests/test-installer.sh`, section "guided mode".

### 3.2 Flags
| Flag | Default | Meaning |
|---|---|---|
| `--node-id <ai-nN>` | from existing `node.yaml`; else from `--inventory` matching this machine's hostname; else **required** | Lab node id. Must match `^ai-n[1-9][0-9]{0,3}$` ([agent.md](agent.md) §3.4). Refuses to change an existing different id (`LAI-INS-020`) |
| `--profile <id>` | `aiagent profile suggest` result | Catalog profile id |
| `--backend <cuda\|vulkan\|cpu>` | detected | Override GPU backend detection |
| `--from-ark <mount>` | unset (online) | Offline install from the drive at `<mount>` (its `localai/` root) |
| `--enroll` | off | Enroll with the cloud. Prompts `Node credential (nod_…):` on `/dev/tty` with echo off (works even when stdin is not a terminal). The credential is **never** accepted as an argument. No `/dev/tty` → exit 2 with "use `--enroll-file`" |
| `--enroll-file <path>` | unset | Read the credential from a file (first line; or `/dev/stdin`). For automation; the file should be 0600 and deleted afterwards |
| `--cloud-url <url>` | `https://ai.marin.cr` | Cloud base URL |
| `--migrate` | auto (if `llama-qwen.service` or `llama-coder-next.service` exists) | Force the migration phase; `--no-migrate` skips it (then the installer refuses to continue while an old llama service holds port 8080, `LAI-INS-031`) |
| `--dry-run` | off | Print every action with `[dry-run]` and change nothing (§4.2) |
| `--yes` | off | Assume yes to confirmations (not to destructive uninstall prompts) |
| `--pin-file <path>` | unset | Read the admin PIN from a file (first line, 4–8 digits); else prompt on a TTY |
| `--desktop-user <name>` | `SUDO_USER` | The primary desktop user (added to `localai-operators`, used for GNOME settings and `~/llama.cpp` reuse). Required when run directly as root without `SUDO_USER` (P1, `LAI-INS-003`). The user must exist |
| `--agent-sha256 <hex>` | unset | 64-hex sha256 of `aiagent-linux-<arch>` as shown by the cloud's add-node dialog. Used only online, as the fallback check when `minisign` can't be installed (P2, else `LAI-INS-021`) |
| `--inventory <path>` | `<ark>/inventory.local.yaml` if `--from-ark`, else unset | Real inventory to pre-fill node data and the VPS checklist. Copied to `/etc/localai/inventory.local.yaml` only with `--keep-inventory` |
| `--no-hardening` / `--hardening` | auto (on for laptops) | Laptop hardening phase |
| `--no-ssh` | off | Skip installing/enabling OpenSSH server |
| `--no-polkit` | off | Don't install the polkit rule that lets `localai-operators` start/restart the agent without the admin password (P5; used by ai-desktop's **Start agent**, [desktop.md](desktop.md) §4.2) |
| `--no-tailscale` | off when enrolling, else on | Skip Tailscale installation |
| `--serve-pattern <A\|B>` | from inventory, else `B` | Recorded in `node.yaml: tailscale.serve_pattern` |
| `--no-sync` | off | Don't start the initial sync/import |
| `--reboot` | off | Reboot at the end if a reboot is required |
| `--repair` | — | §4.5 |
| `--uninstall` | — | §4.4 |
| `--log <path>` | `/var/log/localai-install.log` | Log file |
| `-v` | off | Verbose (shell trace into the log only) |
| `--lang <en\|es>` | from the locale: the first of `LC_ALL`, `LC_MESSAGES`, `LANG` that is set; `es`, `es_*`, `es.*` → `es`, anything else → `en` | Language of the screen and log messages (§8 Language). Any other value is exit 2. `sudo` keeps `LANG` on Ubuntu, so a Spanish desktop gets Spanish without the flag |
| `--keep-inventory` | off | Copy `--inventory` to `/etc/localai/inventory.local.yaml` (needs `--inventory` or `--from-ark`) |
| `--purge-config`, `--purge-content`, `--revert-hardening`, `--restore-legacy` | off | Only with `--uninstall` (§4.4); otherwise exit 2 |
| `--version`, `-h`, `--help` | — | Print the stamped version (§3.6) / the usage, exit 0 |

Argument rules: an unknown flag or a positional argument, a flag without its value, `--node-id` outside the regex, `--agent-sha256` that is not 64 lowercase hex, a missing `--desktop-user`, `--from-ark` folder, `--enroll-file`, `--pin-file` or `--inventory`, a `--cloud-url` that is not `http(s)://host` or carries `user:password@`, `--migrate` with `--no-migrate`, `--hardening` with `--no-hardening`, `--repair` with `--uninstall`, and `--enroll` together with `--enroll-file` are all exit 2. Anything shaped like a credential (`nod_…`, `--token`, `--password` …) is refused with exit 2 and is scrubbed in the message.

Exit codes: `0` success (maybe with "reboot required"), `1` a phase failed (message names the phase and the doctor code), `2` bad arguments, `3` preflight refused (unsupported system), `4` user aborted.

### 3.3 Supported systems
- Ubuntu LTS `24.04` (noble) and `26.04` (resolute), `amd64` or `arm64`. Other versions → `LAI-INS-001` (exit 3). Interim releases are refused.
- systemd as PID 1. Not in a container (`systemd-detect-virt --container` → none), `LAI-INS-002`.

### 3.4 Files written
Everything in overview.md §3.2, plus `/var/lib/localai/install/state.json`, `/var/log/localai-install.log`, `/etc/systemd/logind.conf.d/99-server.conf`, `/etc/NetworkManager/conf.d/wifi-powersave-off.conf`, `/etc/NetworkManager/dispatcher.d/90-wifi-powersave-off`, and the units of overview.md §3.5. Backups of anything replaced go to `/var/lib/localai/install/backup/<UTC-ts>/<original path>`.

### 3.5 `aiagent setup` steps (internal CLI, owned here)
`aiagent setup <step> [--dry-run] [--json] [step flags]`. Steps that change the system are root only (exit 77 otherwise); the read-only `preflight` and `profile-suggest` run as anyone (`preflight` reports `LAI-INS-003` itself when it is not root). Not part of the public `aictl` surface ([aictl.md](aictl.md) should list it under "internal").
| Step | Does |
|---|---|
| `preflight` | system, GPU, RAM, disk, ports, legacy detection → JSON report |
| `profile-suggest` | catalog `match` rules → profile id |
| `dirs` | users, groups, directories and modes, polkit rule (§4.3 P5) |
| `node-yaml` | writes `/etc/localai/node.yaml` if absent; merges flags into an existing one without changing `node_id` |
| `catalog` | installs the catalog from the release or the kit into `/etc/localai/catalog/` |
| `harden` | the laptop hardening (§4.3 P4) |
| `binaries` | installs kiwix-tools, pmtiles, piper from the kit/online tarballs (verified) |
| `units` | writes all unit files from templates embedded in the binary (package `agent/internal/setup/units/`, one `*.service`/`*.timer` template per unit, [agent.md](agent.md) §3.2); `daemon-reload`; enable |
| `api-key` | creates or reuses the llama key |
| `migrate` | runs `aiagent migrate --yes` (every pending migration; today `llama-qwen`, [llama-router.md](llama-router.md) §4.9), adds timestamped `[setup migrate]` log lines, `--dry-run` is passed on. Exit 0 done or nothing pending, 1 failed or refused, 2 bad usage, **20 failed and rolled back** (the old service runs again; the installer maps it to `LAI-INS-033`), 77 not root. `--json` prints `{"step","dry_run","ok","rolled_back","exit"}` |
| `serve-check` | P11: decides the Tailscale serve pattern from `--serve-pattern`, `node.yaml` and the inventory; prints `A`, `B` or `none`; a contradiction with the inventory prints `LAI-INS-050` and exits 1 (reads only) |
| `verify` | post-install checks that can pass before a reboot: the agent answers (`LAI-INS-034`), the serve pattern matches the inventory (`LAI-INS-050`), the admin PIN exists (`LAI-INS-060`), a reboot is needed (`LAI-INS-070`); exit 1 on an error or critical result (reads only) |
| `summary` | prints the summary and the VPS checklist |

### 3.6 Release stamping

`installers/install-node.sh` in the repository is a **template**. The node release workflow ([rebuild-kit.md](rebuild-kit.md) §3.2, [`scripts/release/stamp-installer.sh`](../../scripts/release/stamp-installer.sh)) replaces three tokens, everywhere they occur, and ships the result as the release asset `install-node.sh`:

| Token | Replaced by | Used for |
|---|---|---|
| `@@LAI_RELEASE_TAG@@` | the release tag, `node-v0.1.0` | the download URL of the release assets (P2) |
| `@@LAI_RELEASE_VERSION@@` | the tag without `node-v`, `0.1.0` | the version `install-node.sh --version` prints; the agent version it installs |
| `@@LAI_MINISIGN_PUBKEY@@` | line 2 of `agent/internal/kit/keys/minisign.pub` (starts with `RW`) | `minisign -Vm SHA256SUMS -P <key>` (P2) |

The release fails if the template lacks any of the three, if a token the stamper does not know is left, or if the stamped script does not pass `bash -n`; the key in the stamped script is compared with the committed file again after stamping. A script that must detect "this copy was never stamped" writes the token split in two (`"@@LAI_RELEASE_VERSION""@@"`), which the stamper leaves alone. Run from a checkout the template is unstamped and must say so and stop, not guess a version.

## 4. Behaviour

### 4.1 Driver skeleton
The shape below is what `installers/install-node.sh` does; the exact code lives in the script.
```bash
#!/usr/bin/env bash
set -Eeuo pipefail                  # set only when the file is executed; a test may `source` it to call functions
umask 077
LOG=/var/log/localai-install.log    # --log; rotated to .1 above 10 MB before it is opened
trap 'on_error $LINENO "$BASH_COMMAND"' ERR
say() { …; }                        # one message -> scrub -> screen AND "<UTC ts> [P6 packages] <message>" in the log
run_logged cmd…                     # a command's combined output goes through say(); the last 50 lines are kept for on_error
run_phase n entry                   # prints "==> [n/15] <name>", runs check_<x> / phase_<x>, records state.json
```
Messages are scrubbed line by line by `say()` (there is no global `exec > >(scrub | tee …)` redirection: it cannot give each log line its phase tag and it races with process exit). `-v` sends `set -x` to the log only, through its own scrub process. The phase table (`PHASE_TABLE`: id, function suffix, name, owner item) is the one registry of phases P1..P15; P0 is the driver itself.

A phase that a later item has not built yet is an **explicit named no-op**: `phase_<x>` prints `NOT IMPLEMENTED YET: … (owner: INST-0N, issue #N)`, returns 10, and `state.json` records `not-implemented` (never `done`). A run that skipped any phase ends with `THIS INSTALLER IS INCOMPLETE`. `--repair` and `--uninstall` are parsed and validated, then print the same notice and change nothing until INST-08.

Refusals leave through `fail <exit> <doctor-code> <message>`; bad arguments through `usage_error` (exit 2, before anything is touched). `Ctrl-C` or `SIGTERM` exits 4. An unexpected command failure runs `on_error` (phase, line, command, exit code, last 50 output lines) and exits 1; no doctor code is registered for "unexpected", the message points at the log.

Test hooks: with `LAI_INSTALLER_DEV=1` (never set on a node) the script reads `LAI_INSTALL_STATE_DIR` and `LAI_INSTALL_SYSTEMD_DIR` and, for the INST-06 phases (besides the INST-05 hooks for the program and llama paths), `LAI_ETC_DIR`, `LAI_HEALTH_URL` and `LAI_HEALTH_WAIT` (and `aiagent setup` reads `LAI_SETUP_ROOT` as the filesystem root), may run as non-root, and an unstamped template may do a `--dry-run`. Without it an unstamped copy refuses to run (exit 2, §3.6) and a real run needs root (exit 3, `LAI-INS-003`). P1 to P3 also read `LAI_FAKE_ROOT` (prefix for `/etc/os-release`, `/proc`, `/sys`), `LAI_FAKE_EUID`, `LAI_FAKE_YEAR`, `LAI_INSTALL_ROOT` (prefix for `/opt/localai`, `/usr/local`, `/etc/localai`), `LAI_RELEASE_BASE_URL` (the release download folder; `file://` allowed) and `LAI_INTERNET_URL`; with `LAI_INSTALL_ROOT` set the driver passes `--dir`/`--catalog` to `aiagent setup catalog`/`profile-suggest`. Without it an unstamped copy refuses to run (exit 2, §3.6) and a real run needs root (exit 3, `LAI-INS-003`).

`state.json` (§4.1 of this spec, `schema: 1`) is rewritten atomically (temp file in the same folder, then `mv`), mode 0600, one phase per line so a shell can read it back without `jq`:
```json
{
  "schema": 1, "installer_version": "0.1.0", "updated_at": "2026-11-02T18:04:11Z",
  "phases": {
    "P6": {"name":"packages","status":"done","version":"0.1.0","finished_at":"2026-11-02T18:04:11Z","details":""}
  }
}
```
`status` is `running`, `done`, `failed` or `not-implemented`. A dry run never writes it (and never creates the lock file or the state folder).

Each phase is a bash function `phase_<name>` that first checks whether it is already satisfied (idempotency check), prints "already done" if so, and otherwise acts. A phase that fails stops the script with its doctor code; re-running resumes at that phase (earlier phases are checked again, cheaply).

### 4.2 Dry run
`--dry-run` runs every check for real (preflight, detection, what exists) but replaces every change with a `[dry-run] would …` line: package installs list the packages (`apt-get -s install` output summarised), file writes show path, mode and a diff against the existing file, service actions show the unit and verb. `aiagent setup … --dry-run` steps do the same. The summary at the end lists what would happen, the reboot requirement, and the VPS checklist. Exit code 0 if everything would succeed.

### 4.3 Phases (in order)

#### P0. Arguments and log
Parse flags, refuse unknown ones (exit 2), open the log (0600 root), print the version of the script, the source (`online` or `ark:<mount>`), the start time, and whether this is a fresh install, a re-run or a migration.

#### P1. Preflight
Checks (all of them run; then the script reports every failure at once):
| Check | How | Fail |
|---|---|---|
| root via sudo | `EUID == 0`; `SUDO_USER` set and not root → the **primary desktop user**. Run directly as root without `SUDO_USER` → ask for `--desktop-user <name>` | exit 3, `LAI-INS-003` |
| Ubuntu version | `/etc/os-release` `ID=ubuntu`, `VERSION_ID` ∈ {24.04, 26.04}, `VERSION_CODENAME` recorded | `LAI-INS-001` |
| arch | `dpkg --print-architecture` ∈ {amd64, arm64} | `LAI-INS-001` |
| not a container | `systemd-detect-virt --container` | `LAI-INS-002` |
| GPU | NVIDIA: `lspci -nn` class `0300/0302` vendor `10de`; then `nvidia-smi --query-gpu=name,memory.total,driver_version,compute_cap --format=csv,noheader`. AMD/Intel: `vulkaninfo --summary` (if installed) shows a non-`llvmpipe` device; sysfs `/sys/class/drm/card*/device/vendor` (`0x1002` AMD, `0x8086` Intel) and `mem_info_vram_total`/`mem_info_gtt_total` for AMD. Result: backend `cuda`, `vulkan` or `cpu`, VRAM MiB, compute capability | NVIDIA GPU present but `nvidia-smi` fails → `LAI-INS-010` (warning): online, offer `ubuntu-drivers install --gpgpu` (asks, reboot needed); offline, offer the kit driver packages ([rebuild-kit.md](rebuild-kit.md) §Driver packages). Declining → backend `cpu` |
| Secure Boot | `mokutil --sb-state` (info only) | if enabled and the NVIDIA module isn't loaded → `LAI-INS-011` note about MOK enrollment |
| RAM | `/proc/meminfo` `MemTotal` | < 8 GiB → `LAI-INS-012` warning (cpu-only profile only) |
| disk | `df -B1 --output=avail /srv` (or `/` if `/srv` is on `/`) | < profile `min_free_bytes` + 20 GB → `LAI-INS-013` (fail unless `--yes`, which caps the budget) |
| ports | `ss -Hltn` on 127.0.0.1:7070, :8888, :8081 free, or held by our own units; :8080 held only by `llama-qwen`, `llama-coder-next` or `localai-llama` | `LAI-INS-014` |
| legacy | `/etc/systemd/system/llama-qwen.service` or `llama-coder-next.service` exists → migration needed; record the first one's ExecStart | — |
| internet (online only) | `curl -fsS --max-time 5 -o /dev/null https://github.com` | `LAI-INS-015`: suggest `--from-ark` |
| ark (offline) | `<mount>/localai/ROOT.json` and `kit/` exist; `kit/catalog/CATALOG_VERSION` readable; required kit items for this codename/arch/backend present and verified per MANIFEST ([rebuild-kit.md](rebuild-kit.md) §3.2, §4.4) | `LAI-INS-016` listing what's missing |
| clock | `timedatectl show -p NTPSynchronized`; year ≥ 2026 | warning `LAI-INS-017` (TLS and ages depend on it) |
| power | laptop on battery | warning: "Plug in the charger before continuing" |

Laptop detection: `/sys/class/dmi/id/chassis_type` ∈ {8, 9, 10, 14, 31, 32} **or** a `/sys/class/power_supply/*` with `type=Battery`. The driver sets `IS_LAPTOP` and `ON_BATTERY` for P4.

How P1 is implemented (decisions of INST-02):
- **The agent is not on the machine yet at P1**, so the driver runs these checks natively in bash (`phase_preflight`), with exactly the commands above, and lists every problem before it stops. The Go step `aiagent setup preflight [--json] [--from-ark <mount>] [--desktop-user <name>] [--yes] [--min-free-bytes <n>] [--backend <b>]` (package `agent/internal/setup/detect` plus `agent/cmd/aiagent/setup`) implements the same table. It exits 0, 1 (a check failed) or 3 (`LAI-INS-001/002/003`), prints a table or, with `--json`, the report (`schema localai.preflight/1`: `ok`, `exit`, `system`, `gpu`, one `checks[]` row of `id`, `status`, `code`, `message`, `fix` per check). P3 runs it once the agent is staged, as the authority: it adds the kit check (`kit.Verifier`, quick mode, the findings about this computer) and the disk check against the chosen profile's `min_free_bytes`. A dry run as a normal user only warns about it.
- **Thresholds.** RAM below 8 GB means `MemTotal` under 7629 MiB (an 8 GB computer reports about 7.6 GiB). The disk need is `min_free_bytes` + 20 GB; P1 checks the 20 GB floor, P3 the profile.
- **A refusal** (`LAI-INS-001/002/003`) exits 3 and records P1 as failed; nothing on the system was touched. Run as a normal user, a real run gets `LAI-INS-003`; a dry run only warns, so the plan can be previewed.
- **The ark check** in bash is presence: `ROOT.json`, `kit/catalog/CATALOG_VERSION`, `kit/bin/aiagent-linux-<arch>`, the signed `SHA256SUMS` and `SHA256SUMS.minisig` (in `kit/bin/release/`, where `aictl ark sync` puts them, else beside the binaries), `kit/installers/install-node.sh` (else `kit/bin/install-node.sh`, which sync does write), `kit/debs/<codename>-<arch>/MANIFEST.json`, and `kit/nvidia/<codename>-<arch>-*/` when the backend is cuda. The hash verification against the MANIFESTs is the Go check at P3, and P2 verifies the files it uses.
- **The NVIDIA driver offer** (`LAI-INS-010`). Online, after the question, `ubuntu-drivers install --gpgpu` runs, the phase is recorded as failed ("reboot, then run again") and the installer exits 0 asking for the reboot. Offline, the kit's driver packages are installed by the packages step ([§4.3 P6](#p6-packages)); P1 only records the wish, the run continues as `cpu`, and the summary says to reboot and re-run. Declining always continues as `cpu`.

#### P2. Stage artifacts
Get the agent binary first (most later steps run it):
- **Online:** download `aiagent-linux-<arch>`, `SHA256SUMS`, `SHA256SUMS.minisig` from the GitHub release of the installer's own version (the version is embedded in the script). Verify: `minisign -Vm SHA256SUMS -P <public key embedded in the script>` (install the `minisign` apt package first if missing), then `sha256sum -c --ignore-missing SHA256SUMS`. If `minisign` can't be installed, fall back to the sha256 the cloud dialog showed (`--agent-sha256 <hex>`), else fail `LAI-INS-021`.
- **Offline:** copy from `<ark>/localai/kit/bin/` and verify against the release's `SHA256SUMS` in `kit/bin/release/` (or beside the binaries on a drive made by hand) and its `SHA256SUMS.minisig` with the embedded key (`minisign` comes from the kit debs).
- Place it at `/opt/localai/bin/aiagent.new`, check `aiagent.new version` runs, then atomically rename to `/opt/localai/bin/aiagent` (keeping `aiagent.prev`). Symlink `/usr/local/bin/aictl → /opt/localai/bin/aiagent`.
- **Install the installer itself** (so doctor fixes like `sudo install-node.sh --repair` work verbatim): copy the running, verified `install-node.sh` of the same release (online: the release asset, checked against `SHA256SUMS`; offline: `<ark>/localai/kit/installers/install-node.sh`, [rebuild-kit.md](rebuild-kit.md) §3.1) to `/opt/localai/installers/install-node.sh` (root 0755) and symlink `/usr/local/sbin/install-node.sh → /opt/localai/installers/install-node.sh` (`/usr/local/sbin` is in sudo's `secure_path`). Paths: [overview.md](../architecture/overview.md) §3.2.
- Install the catalog: `aiagent setup catalog --from <release|ark> [--ark <mount>] [--dir <folder>] [--dry-run]`: the catalog is linted, installed atomically with the previous one kept (`catalog.Install`), and a version that is already installed is left alone. In a dry run it still fetches and lints.

How P2 is implemented (decisions of INST-02):
- Files are copied or downloaded to a temporary folder, verified there, and only then placed. A tampered `SHA256SUMS` signature, an agent or installer whose hash differs, or a name that `SHA256SUMS` does not list is `LAI-INS-021` (exit 1) with nothing in `/opt/localai/bin`. The hash comparison reads the listed hash of the exact file name (equivalent to `sha256sum -c --ignore-missing`).
- The installer copy must be listed in `SHA256SUMS` online. On the drive it is verified when listed; if the drive's `SHA256SUMS` does not list it (a drive made by hand), a warning says so and the kit copy is used. `aictl ark sync` puts the release's own signed list in `kit/bin/release/` ([rebuild-kit.md](rebuild-kit.md) §3.2.1), and it lists the installer, so a synced drive is verified in full. The installer is read from `kit/installers/install-node.sh`, else from `kit/bin/install-node.sh`.
- `minisign` offline comes from the kit's `minisign_*.deb` only if its sha256 appears in the repository's `PACKAGES.lock`; without it, offline fails with `LAI-INS-021`. Online, `--agent-sha256` is the fallback when `minisign` cannot be installed: the agent is checked against it and the installer copied is the running script.
- `aiagent.prev` is made only when the binary changes, so a re-run with the same agent keeps the existing `.prev`. The smoke test (`aiagent.new version`) runs before the rename.
- A dry run online only prints what it would download (no network); offline it copies and verifies for real into a temporary folder and runs `aiagent setup catalog --dry-run` with the verified copy.

#### P3. Profile
`aiagent setup profile-suggest [--json] [--profile <id>] [--backend <b>] [--catalog <dir>] [--from-ark <mount>]` (catalog.md §4 `match`, `cpu-only` last; reads the installed catalog, or the kit's copy on the drive when none is installed yet). The JSON (`schema localai.profile-suggest/1`) carries `profile`, `display_name`, `suggested`, `reasons`, `override`, `mismatch`, `mismatch_code`, `mismatch_message`, `backend`, `vram_mib`, `ram_mib`, `budget_bytes`, `min_free_bytes`, `default_model`, `survival_models[]` and `catalog_version`. The driver prints the plain-words form, asks to confirm a mismatch (`--yes` confirms; no answer exits 4), checks the disk against `min_free_bytes` and then runs `aiagent setup preflight` (above). `--profile` overrides; a profile whose `match` doesn't fit the detected hardware asks for confirmation (`LAI-CAT-005` warning). Print the profile name, budget and survival models in plain words.

#### P4. Laptop hardening (§Laptop hardening)
Only for laptops (or `--hardening`). Each step is ported from [source/laptop-server-setup.md](../source/laptop-server-setup.md) §2, with an idempotency check and a verification from its §3.

1. **logind: ignore lid, power key and idle.** Write `/etc/systemd/logind.conf.d/99-server.conf` (0644) with exactly:
   ```ini
   [Login]
   HandleLidSwitch=ignore
   HandleLidSwitchExternalPower=ignore
   HandleLidSwitchDocked=ignore
   HandlePowerKey=ignore
   HandleSuspendKey=ignore
   HandleHibernateKey=ignore
   IdleAction=ignore
   ```
   If the file exists with identical content: skip. If different: back it up and replace.
   **Never restart `systemd-logind`.** Restarting it while a graphical session is logged in froze the login on the reference machine and needed a hard power-off. The script does **not** run `systemctl restart systemd-logind`, `systemctl kill -s HUP systemd-logind`, or anything else that touches logind. It sets `reboot_required = true` with reason "logind settings apply after a reboot". `scripts/check-no-logind-restart.sh` (a CI step of `installer-ci`, tested by `scripts/tests/test-no-logind-restart.sh`) fails if `installers/` or `agent/` contains `restart systemd-logind`, `kill .* systemd-logind` and the like; the Go code also refuses to run any command that names `systemd-logind`.
   Verify (after reboot, by `aictl doctor`): `systemd-analyze cat-config systemd/logind.conf | grep -E 'Lid|IdleAction|Key'` shows the values (`LAI-INS-040`).
2. **Mask sleep targets:** `systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target` (masking doesn't restart anything and is safe live). Verify: `systemctl is-enabled …` prints `masked` four times (`LAI-INS-041`).
3. **GNOME idle suspend off** (only if GNOME is installed, `gsettings` exists):
   - For the desktop user, if their session bus exists (`/run/user/<uid>/bus`; the agent runs these as that user with the same environment instead of calling `sudo`, which is equivalent):
     ```bash
     sudo -u "$DESKTOP_USER" DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$UID_DESKTOP/bus \
       gsettings set org.gnome.settings-daemon.plugins.power sleep-inactive-ac-type 'nothing'
     sudo -u "$DESKTOP_USER" DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$UID_DESKTOP/bus \
       gsettings set org.gnome.settings-daemon.plugins.power sleep-inactive-battery-type 'nothing'
     ```
   - Always also write a system default so new users and sessions without a bus get it: `/etc/dconf/profile/user` (ensure it contains `user-db:user` and `system-db:local`), `/etc/dconf/db/local.d/00-localai-power`:
     ```ini
     [org/gnome/settings-daemon/plugins/power]
     sleep-inactive-ac-type='nothing'
     sleep-inactive-battery-type='nothing'
     ```
     then `dconf update`. (No lock file: the user can still change it.)
   - If no session bus was available, the summary says: "Log in once as <user>; GNOME idle suspend is off by default, but check Settings → Power."
   - Verify: `gsettings get …` prints `'nothing'` twice (`LAI-INS-042`). The screen still blanks and locks after about 5 minutes; that's harmless.
4. **Performance power profile:** if `powerprofilesctl` exists: `powerprofilesctl set performance`; verify `powerprofilesctl get` → `performance` and, on Intel, `/sys/devices/system/cpu/cpu0/cpufreq/energy_performance_preference` → `performance` (`scaling_governor` reading `powersave` with `intel_pstate` is normal; don't "fix" it). power-profiles-daemon keeps the choice across reboots (`LAI-INS-043`).
5. **Wi-Fi power save off, three layers** (only if a `wl*` interface exists; find it with `iw dev | awk '/Interface/{print $2}'`):
   - (a) `/etc/NetworkManager/conf.d/wifi-powersave-off.conf`:
     ```ini
     [connection]
     wifi.powersave = 2
     ```
     then `nmcli general reload conf`. The name sorts after Ubuntu's `default-wifi-powersave-on.conf` (which sets 3), so ours wins.
   - (b) `/etc/NetworkManager/dispatcher.d/90-wifi-powersave-off` (root:root 0755):
     ```sh
     #!/bin/sh
     # Keep Wi-Fi power save off (server use). Installed by Local AI.
     IFACE="$1"; ACTION="$2"
     case "$IFACE" in
       wl*) case "$ACTION" in up|dhcp4-change|reapply) /usr/sbin/iw dev "$IFACE" set power_save off ;; esac ;;
     esac
     ```
   - (c) now: `iw dev <iface> set power_save off` for each `wl*` interface.
   - Verify: `iw dev <iface> get power_save` → `Power save: off` (`LAI-INS-044`); doctor re-checks after reconnects and reboots. Note in the summary: "Wi-Fi can still stall; an Ethernet cable is strongly recommended for a server laptop."
6. **Remote access:** unless `--no-ssh`: `apt-get install -y openssh-server`, `systemctl enable --now ssh`. Tailscale is P10. If Tailscale is installed: `tailscale set --ssh=false` (plain OpenSSH is used). `ufw`: left as it is; the summary says whether it's active and recommends deciding on purpose (the reference machine had it inactive).
7. **Housekeeping checks** (report only, never changed): unattended-upgrades `Automatic-Reboot` (kernel updates wait for a manual reboot; recommended to keep it off), AC online (`/sys/class/power_supply/AC*/online` = 1, else "Plug in: a discharging battery means the server dies when it empties"). Printed checklist items for things software can't set: BIOS **"Power on after AC restore"**, battery charge limit, Ethernet.
8. **Reboot requirement:** if step 1 changed anything, `reboot_required = true` (reason "logind settings apply after a reboot"), recorded in `<state dir>/hardening.json` (mode 0600, with `applied`, `applied_at`, `desktop_user` and the summary lines for P15) and in the details of P4 in `state.json`. The flag clears itself once the machine has booted after `applied_at`; until then the doctor reports `LAI-INS-070` instead of `LAI-INS-040` (which only means something after a reboot). Replaced files are backed up to `<state dir>/backup/<UTC-ts>/<original path>` (root 0700). The installer never reboots unless `--reboot` is given, and then only at the very end after printing the summary and a 10 s countdown (Ctrl-C cancels).
9. **Lid test instructions** are printed in the summary (from the source §5.1), with the one-line logger and the expected "no gaps" result.

The installer calls `aiagent setup harden [--dry-run] [--json] [--hardening] [--no-ssh] [--desktop-user <name>] [--state-dir <dir>]` (runs only on laptops unless `--hardening`; a battery or a laptop DMI chassis type means laptop). `aiagent setup harden --check` verifies without changing anything (exit 1 if a `LAI-INS-040`..`044` finding exists; a pending `LAI-INS-070` is not a failure) and is the P4 idempotency check; `--revert` is §4.4 step 9. The same checks are registered with the agent doctor as `ins.logind` (040, 070), `ins.sleep_targets` (041), `ins.gnome_idle` (042), `ins.power_profile` (043) and `ins.wifi_powersave` (044); they report nothing on a node where the hardening was not applied or was reverted.

#### P5. Users, groups and directories
`aiagent setup dirs`:
- `groupadd --system localai-operators` if missing.
- `useradd --system --user-group --home-dir /var/lib/localai --no-create-home --shell /usr/sbin/nologin localai` if missing (the folder is created below with its exact owner and mode, so `useradd` must not create it); `usermod -aG video,render localai` for the groups that exist on this Ubuntu (a missing `render` group is skipped with a note).
- `usermod -aG localai-operators <desktop user>`; note in the summary that the user must log out and back in for the group to apply.
- Create, with owner and mode exactly as overview.md §3.2: `/opt/localai/{bin,installers,llama.cpp/builds,llama.cpp/src,whisper.cpp/builds,piper,kiwix-tools,pmtiles}` (root 0755), `/etc/localai` (root:localai 0750), `/etc/localai/catalog` (root:localai 0750), `/var/lib/localai/{sync,drill,cloud,kiwix,llama-cache}` (localai 0750), `/var/lib/localai/{install,build}` (root 0700), `/var/lib/localai/update` (root 0700), `/var/lib/localai/bench` (root 0755), `/var/lib/localai/migration` (root 0700; `aiagent migrate` creates `migration/llama-qwen/` under it), `/opt/localai/installers` (root 0755), `/srv/localai` (localai 0755) with the layout dirs of §3.3 and its `ROOT.json` (`kind: internal`).
- The installer creates every one of these directories itself (the agent's own startup also creates `update/` if it is missing, agent.md §4.1, but the installer does not rely on that).
- `/var/lib/localai` itself is `localai` 0750 (the installer's own `install/` state folder inside it stays root 0700). The layout directories below `/srv/localai` are created the way the agent's own startup creates them (`ark.CreateLayout`: root-owned, 0755; `.trash` and `.staging` 0700); only `/srv/localai` itself is owned by `localai`. An existing `ROOT.json` of the internal root is kept; a damaged one is moved aside to `ROOT.json.damaged-<UTC>` (never deleted) and rewritten.
- Existing paths with a different owner/mode are corrected (logged with old values). A file where a directory belongs is an error (`LAI-INS-035`), never deleted.
- `aiagent setup dirs --dry-run` prints every create/chown/chmod with old and new values and changes nothing; `--check` is the same and exits 1 if anything would change (the installer's "already done?" test). Without `--dry-run` it needs root (exit 77); a dry run only reads, so it works unprivileged. Flags: `--no-polkit`, `--desktop-user`, `--node-id`, `--profile`, `--json`. Failure: `LAI-INS-035`.
- Unless `--no-polkit`: write `/etc/polkit-1/rules.d/50-localai-operators.rules` (root 0644), which allows members of `localai-operators` to run only `start` and `restart` on `localai-agent.service` without a password (nothing else, no other unit):
  ```js
  polkit.addRule(function(action, subject) {
    if (action.id == "org.freedesktop.systemd1.manage-units" &&
        action.lookup("unit") == "localai-agent.service" &&
        (action.lookup("verb") == "start" || action.lookup("verb") == "restart") &&
        subject.isInGroup("localai-operators")) {
      return polkit.Result.YES;
    }
  });
  ```
  ai-desktop's `systemctl start --no-ask-password localai-agent.service` then succeeds without a prompt; without the rule it falls back to `pkexec` ([desktop.md](desktop.md) §4.2). `--uninstall` removes the file.

#### P6. Packages
Lists (exact names checked per codename at implementation):
| Group | Packages |
|---|---|
| base | `ca-certificates curl jq git iw network-manager pciutils usbutils minisign zstd udisks2` |
| build | `build-essential cmake ninja-build pkg-config ccache` |
| vulkan backend | `libvulkan-dev glslc spirv-headers vulkan-tools mesa-vulkan-drivers` |
| cuda backend | `nvidia-cuda-toolkit` (Ubuntu package; see [rebuild-kit.md](rebuild-kit.md) §CUDA for alternatives) |
| optional | `openssh-server` (P4), `tailscale` (P10), `gpsd` (never by default) |

- **Online:** `apt-get update` then `DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends <list>`. Already-installed packages are left at their version (no upgrades).
- **Offline (implementation):** the installer calls `aictl kit install-debs --root <mount>/localai --packages <list>` ([rebuild-kit.md](rebuild-kit.md) §4.1.4, [aictl.md](aictl.md) §4.23c), which is the single implementation of the steps below: it re-verifies every `.deb`, writes the temporary source, installs with only that source enabled, and removes the source file whatever happens. The package list passed is the group list of this section for the detected backend (so the offline and online installs install the same names). `--dry-run` passes `--dry-run` to it.
- **Offline (what that does):** add a temporary source `/etc/apt/sources.list.d/localai-ark.list` containing `deb [trusted=yes] file:<mount>/localai/kit/debs/<codename>-<arch> ./`, run `apt-get update -o Dir::Etc::sourcelist=/etc/apt/sources.list.d/localai-ark.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0`, install with the same `-o` options so only the kit repo is used, then remove the source file. `[trusted=yes]` is acceptable because every `.deb` was verified against Ubuntu's signed indexes at sync time and is re-verified against `PACKAGES.lock` before this step ([rebuild-kit.md](rebuild-kit.md) §Offline apt).
- Then `aiagent setup binaries` installs kiwix-tools, pmtiles and piper from verified tarballs ([companion-services.md](companion-services.md) §4.1.1, §4.3.1): offline `aiagent setup binaries --kit <mount>/localai/kit` (each tarball is hashed against its `MANIFEST.json` first; the same code as `aictl kit install <tool>`), online `aiagent setup binaries --online --catalog /etc/localai/catalog` (downloads only the `https` URLs that the catalog `kit-kiwix-tools`, `kit-pmtiles` and `kit-piper` items pin with a sha256, hashes the file before extracting, never installs an unpinned download). `--tools a,b` limits the set; `--dry-run` checks the kit files (or lists the pinned URLs) and installs nothing. Each tool lands in `/opt/localai/<tool>/<version>/`, `current` is repointed atomically and the previous version is kept. A failure carries the tool's own code (`LAI-KIT-001`, `LAI-KIT-040`, `LAI-SVC-003`, `LAI-SVC-020`) or `LAI-INS-015` (download failed) or `LAI-INS-035`.
- The package names are in the script and, one per line, in `installers/packages/{base,build,vulkan,cuda}.list`; a test keeps the two equal. The backend comes from `--backend`, else what P1 detected, else `cpu`.
- Failure: `LAI-INS-030` with apt's last 20 lines, then (issue #437) one plain line naming the likely cause with its fix when apt's output shows it: `No space left on device` (the disk is full: free space) or `Hash Sum mismatch` / `not a Debian format archive` / `error processing archive` (a package from the kit is damaged: copy the kit again from the AI-ARK drive). Nothing is added when neither fits; the fix of the code covers both. English and Spanish.

#### P7. llama.cpp build or reuse
The commit is `node.yaml: llama.commit` if present, else the pinned commit in the catalog's `kit/catalog/pins.json` ([rebuild-kit.md](rebuild-kit.md) §Pins). If neither names one and a legacy llama unit is installed (and not `--no-migrate`), the phase builds nothing and says so: the migration builds the engine itself in P8 ([llama-router.md](llama-router.md) §4.9.2 M1). Then:
1. If `/opt/localai/llama.cpp/builds/<commit[:9]>-<backend>/bin/llama-server` exists and its smoke test passes → reuse it (done).
2. Else, **reuse the source** where possible: if the desktop user has `~/llama.cpp` (as on ai-n1/n2/n3 today) and `git -C ~/llama.cpp cat-file -e <commit>^{commit}` succeeds, build from a `git worktree`-free export of it (`git -C ~/llama.cpp archive <commit> | tar -x -C /opt/localai/llama.cpp/src/<commit>`), which also reuses ccache. Otherwise use the kit tarball (offline) or download the tarball from GitHub (`https://github.com/ggml-org/llama.cpp/archive/<commit>.tar.gz`, sha256 recorded), extracted to the same `/opt/localai/llama.cpp/src/<commit>/`, the only source location ([llama-router.md](llama-router.md) §4.10.1).
3. Build with `aictl rebuild-llama --commit <commit> --backend <backend> --source /opt/localai/llama.cpp/src/<commit> --no-activate` ([llama-router.md](llama-router.md) §4.10, [aictl.md](aictl.md) §4.23), so the installer and the offline rebuild share one builder. The old build dirs under `~/llama.cpp/build-cuda` and `build-vulkan` are **not** used as install targets (they link shared libraries by absolute path) and are never deleted.
4. Activation (`current` symlink) happens in P8 (migration) or P9.

Implementation (INST-05): the commit is 7 to 40 hex; with `--from-ark` the kit's `kit/catalog/pins.json` is read before the installed catalog's, and a node id that is in no entry falls back to the first entry. A source tree already in `src/<commit>/` is used as it is. The kit tarball is expected at `<ark>/localai/kit/src/llama.cpp-<commit>.tar.gz` (its single top folder is dropped); with `--from-ark` and no tarball the phase fails `LAI-INS-032` and never tries the internet. The builder is called with `--yes` (the installer is the consent; without it the builder would wait for a terminal answer inside the installer's log pipe). A failed build is `LAI-INS-032` and the message names `build.log`. In `--dry-run`, when no commit can be resolved yet (the catalog is installed by P2, which a dry run skips), P7 says so and succeeds.
Failure: `LAI-INS-032` with the CMake log path.

#### P8. Migration
Runs if `llama-qwen.service` or `llama-coder-next.service` exists (or `--migrate`; the migration itself picks the one that serves, [llama-router.md](llama-router.md) §4.9.1). The installer only calls `aiagent migrate --yes` (through `aiagent setup migrate`, which adds the installer's logging; the installer has already asked the person) and shows its output; all rules (model import into `/srv/localai/models/<id>/` by hash match, key reuse from `/etc/llama-qwen/api-key`, unit swap, `llama-qwen` disable + mask, symlinking the legacy key path, rollback) are in [llama-router.md](llama-router.md) §4.9. Port 8080, the key and the public URL stay the same. `llama-qwen.service` counts as present only as a real unit file (after a migration it is a masked symlink to `/dev/null`, so a re-run finds nothing to migrate). The installer asks for one yes (`--yes` skips it) before a real migration. With `--no-migrate` the phase is skipped, unless one of them is active or something listens on :8080 while a unit exists (`LAI-INS-031`). In `--dry-run` as root with the agent installed, `aiagent setup migrate --dry-run` runs for real; otherwise the line it would run is printed. If the migration fails, its rollback restores `llama-qwen.service` running as before and the installer stops with `LAI-INS-033`.

#### P9. Agent, node.yaml, units
1. `aiagent setup node-yaml --node-id … --profile … --backend … --serve-pattern … [--inventory …]`: writes `/etc/localai/node.yaml` (root:localai 0640) if missing; on re-runs only fills missing keys.
2. `aiagent setup units`: writes, from templates embedded in the binary (`agent/internal/setup/units/`), `localai-agent.service` ([agent.md](agent.md)), `localai-llama.service` ([llama-router.md](llama-router.md)), `localai-kiwix.service` and `localai-whisper.service` ([companion-services.md](companion-services.md) §3.3), `localai-sync.service/.timer` ([ark-sync.md](ark-sync.md) §3.7), `localai-drill.service/.timer` ([drill.md](drill.md) §3.3). Changed files are backed up. `systemctl daemon-reload`. `systemctl enable localai-agent localai-llama localai-kiwix localai-whisper`, then `systemctl enable --now localai-sync.timer localai-drill.timer` (starting a timer does not run its service at once; a scheduled run that cannot reach the agent exits 3 and the unit shows failed, so a broken agent is visible; skipped runs exit 0, [aictl.md](aictl.md) §3.4). The unit texts are copied verbatim from their owner specs, including `SuccessExitStatus=8` on `localai-sync.service` and `localai-drill.service` (exit 9 marks the unit failed; skipped runs exit 0).
3. `systemctl restart localai-agent` (or `start` on a fresh install), then wait up to 30 s for `curl -fsS http://127.0.0.1:7070/api/v1/health` (`LAI-INS-034`). The agent creates `/etc/localai/agent-token` on first start.
4. If no migration ran: activate the llama build (`current`) and start `localai-llama`.
5. Save the build in use in the kit: `aictl kit export-build` ([rebuild-kit.md](rebuild-kit.md) §4.2) packs it into `kit/prebuilt/` of the internal root and of every mounted, writable AI-ARK drive, so a later rebuild of this computer can restore it in seconds (`aictl rebuild-llama --prebuilt`). It runs after the last activation (P8's or step 4), because the installer builds with `--no-activate` and `rebuild-llama` only exports on activation. The build is already in use, so a failure (or a missing `aictl`) is a `WARNING` that names `sudo aictl kit export-build` for later, never a failed install; with no active build it says so and does nothing. `--dry-run` prints the line it would run.

Details (INST-06, `installers/install-node.sh` and `agent/cmd/aiagent/setup/`): `aiagent setup node-yaml` fills only the keys that are missing (`schema_version`, `node.id`, `node.public_host`, `profile`, `llama.backend`, `tailscale.serve_pattern`), keeps comments and `node.yaml.bak`, and refuses a different `--node-id` with `LAI-INS-020` (exit 1; the installer re-raises it). `--inventory` fills the profile, backend, serve pattern and public host of the matching node entry when the flag is absent; a flag always wins. `aiagent setup units` renders the llama unit in router mode from the profile's `router` block in `/etc/localai/catalog` (default router settings when the catalog is not installed yet; the agent regenerates the unit from the live profile later), writes `50-env.conf` only when `llama.extra_env` is not empty, and takes the kiwix thread count from `kiwix.threads`. Unchanged files are left alone and changed files are copied to `<state>/backup/<UTC-ts>/<path>`; `daemon-reload` runs only when a file changed. The agent is always restarted (`systemctl restart` also starts a stopped unit). "No migration ran" means P8 did not migrate (`MIGRATION_RAN` is not 1); P9 then points `current` at the build P7 made for this node's commit (else the newest finished build under `/opt/localai/llama.cpp/builds/`) when it points nowhere, and starts `localai-llama`; with no finished build it only warns. Every step is dry-run capable: the installer passes `--dry-run` on, and when the installed `aiagent` has no `setup` command (or none is installed) a dry run prints `[dry-run] would run: …` instead.

#### P10. API key
`aiagent setup api-key`: if `/etc/localai/api-key` exists (64 lowercase hex, root 0600), keep it. Else if `/etc/llama-qwen/api-key` exists (migration), it was already moved by P8. Else generate with `openssl rand -hex 32` under `umask 077`. Print only the masked form (`ab12…ef90`) and "`aictl key show` prints the full key."

Details: the key is 32 random bytes from the operating system (`crypto/rand`, the same strength as `openssl rand -hex 32`) written atomically as 64 lowercase hex plus a newline, root 0600. If `/etc/localai/api-key` exists it is kept (its mode is tightened to 0600); if it exists but is not 64 lowercase hex it is **left alone** and the step fails with a message to run `sudo aictl key rotate`. If only a valid `/etc/llama-qwen/api-key` exists, it is reused (copied), never regenerated.

#### P11. Tailscale
Unless `--no-tailscale`:
- **Install.** Online: add the official apt repo for the codename (`https://pkgs.tailscale.com/stable/ubuntu/<codename>.noarmor.gpg` to `/usr/share/keyrings/tailscale-archive-keyring.gpg`, and `<codename>.tailscale-keyring.list` to `/etc/apt/sources.list.d/tailscale.list`), then `apt-get install -y tailscale`. (Not `curl … | sh`.) Offline: from the kit debs (P6 list). `systemctl enable --now tailscaled`.
- **Login is interactive and is not done by the installer.** If `tailscale status --json` shows `BackendState != Running`, the summary prints:
  ```
  Join the tailnet (do this once, then come back):
    sudo tailscale up --ssh=false
  Open the URL it prints, sign in, and approve the machine "<hostname>".
  Check:  tailscale status      (shows this machine and the VPS "prod-server")
  ```
  The same three lines are printed by the summary (§P15) right after the TO DO list, only when the node is not on the tailnet and `--no-tailscale` was not given. The installer finds out with `tailscale status --json` (read only).
- **Serve pattern.** Recorded in `node.yaml: tailscale.serve_pattern` (`A` = `--tcp=18080`, `B` = `--https=443`, overview.md §3.4). The installer never runs `tailscale serve`; the agent applies it when the mode is `online` ([modes.md](../architecture/modes.md) §2.3). After migration of an existing node, the pattern must match what the VPS vhost expects (ai-n1: A; ai-n2/ai-n3: B); a mismatch with the inventory (the `--serve-pattern` flag or `node.yaml` against `nodes[].serve_pattern` of the inventory, checked by `aiagent setup serve-check`) is a hard error during install: the installer marks P11 failed and stops with `LAI-INS-050` (exit 1). When `aictl doctor` finds the same mismatch later, `LAI-INS-050` is reported as a warning (the node still works locally).

#### P12. Admin PIN
If `/etc/localai/admin-pin` is missing: read from `--pin-file`, or prompt on the TTY twice with `read -rs` ("Choose an admin PIN, 4 to 8 digits. You'll need it to delete things or leave survival mode."). Pass it to `aictl pin set --pin-stdin` (argon2id, security-model.md §2). No TTY and no file → skip with warning `LAI-INS-060` ("The PIN isn't set; run `sudo aictl pin set`"). Never logged.

Details: the prompt allows three tries (4 to 8 digits, the same twice); a `--pin-file` whose first line is not 4 to 8 digits, three bad tries, a missing `aictl`, or a failing `aictl pin set` all end as the `LAI-INS-060` warning (the install continues; the summary of INST-07 reads `PIN_SKIPPED=1`). The PIN lives only in a local shell variable, reaches `aictl` on its stdin, is masked as `<pin>` if `aictl` ever echoes it, and `set -x` (`-v`) is switched off around it so it cannot reach the log. A dry run never prompts and never reads the PIN.

#### P13. Cloud enrollment
If `--enroll`/`--enroll-file`: `aictl cloud enroll --url <cloud-url> --credential-stdin` with the credential on stdin (read earlier from `/dev/tty` or the file, held only in a shell variable, never written to the log). Failure (`LAI-CLD-051`/`053`/`054`/`062`, [agent.md](agent.md) §8.5) doesn't fail the install; the summary shows the error and the retry command. Already enrolled → skipped.

Details: without `--enroll`/`--enroll-file` the phase does nothing (and says so). A credential that does not match `nod_…`, a missing `aictl`, or a failed `aictl cloud enroll` all leave the install going: the shell variables `ENROLL_FAILED` (`LAI-CLD-nnn: message`; `LAI-CLD-051` when no valid credential was given, `LAI-CLD-054` for a missing `aictl`, else the code `aictl` printed) and `ENROLL_RETRY` (`sudo aictl cloud enroll --url <cloud-url> --credential-stdin`, never containing the credential) are set for the summary of INST-07. The credential is masked as `<credential>` if echoed and `set -x` is off around it.

#### P14. Initial content
Unless `--no-sync`:
- **Online:** `aictl ark sync --dry-run` (shown as a short summary: "Will download 186 GB over the next hours"; the size is the last size on a line of the plan that mentions download, total or plan), then `aictl ark sync` started in the background (the agent runs it; the installer doesn't wait).
- **Offline:** `aictl ark import --from <drive root id> --catalog` started in the background. The root id is `root_id` of `<mount>/localai/ROOT.json`.
- A plan or start that fails (agent not ready, no network) never fails the install: the installer says why and prints the command to run later, and the summary's `Content:` line says "not started".
- In both cases print "Check progress with: `aictl ark sync` (status) or the desktop app."

#### P15. Verify and summary
`aiagent setup verify` runs every `LAI-INS` doctor check (§8) that can pass before a reboot. Then `aiagent setup summary` prints (an error or critical result of `verify` stops the install: P15 fails and the summary is not printed; `--keep-inventory` first copies the inventory to `/etc/localai/inventory.local.yaml`, root:root 0600):
```
Local AI node ai-n4 is installed.        (profile small-8gb, backend cuda, agent 0.4.0)

  Brain:        qwen3.5-9b-q4km (loading in the background)
  Local UI:     http://127.0.0.1:7070          Survival Portal: http://127.0.0.1:7070/portal
  API key:      ab12…ef90   (full key: aictl key show)
  Content:      sync running (186 GB planned)   check: aictl ark sync
  Cloud:        enrolled at https://ai.marin.cr

TO DO
  [ ] REBOOT this computer (laptop power settings apply after a reboot).   sudo reboot
  [ ] Log out and back in, so <user> can use the desktop app (group localai-operators).
  [ ] Join the tailnet:  sudo tailscale up --ssh=false
  [ ] BIOS: turn on "Power on after AC restore".  Prefer an Ethernet cable over Wi-Fi.
  [ ] Publish the node on the VPS (checklist below).

VPS CHECKLIST for ai-n4 (run on the VPS as root; details: docs/source/vps-ai-nodes-setup.md §6)
  1. On this node, check llama answers:   curl -s http://127.0.0.1:8080/health
  2. DNS: Route 53 zone marin.cr (<ROUTE53_ZONE_ID>): A record ai-n4.marin.cr -> <VPS_PUBLIC_IP>, TTL 300
  3. From the VPS:  tailscale ping <machine>
       Pattern B:  curl -i --resolve <machine>.<TAILNET>.ts.net:443:<TAILNET_IP_AI_N4> https://<machine>.<TAILNET>.ts.net/health
       Pattern A:  curl -i http://<TAILNET_IP_AI_N4>:18080/health
  4. Bootstrap HTTP vhost /opt/infra/proxy/nginx/conf.d/ai-n4.marin.cr.conf (ACME webroot + redirect);
       docker exec proxy-nginx nginx -t && docker exec proxy-nginx nginx -s reload
  5. Certificate: certbot webroot for ai-n4.marin.cr (email <ADMIN_EMAIL>)
  6. Final HTTPS vhost: copy ai-n2.marin.cr.conf (Pattern B) or ai-n1.marin.cr.conf (Pattern A); change names, cert paths,
       proxy_pass IP and (B) proxy_ssl_name/Host. Keep Authorization passthrough, buffering off, timeouts 15s/600s/600s.
  7. Validate: unauthenticated /v1/models -> 401; with the key -> 200.
  8. Commit the vhost in marin-server-infra; optionally add the node to Open WebUI.
```
- Placeholders in the checklist are filled from `--inventory` (or `/etc/localai/inventory.local.yaml`) when available, because the terminal and the log are local. Without an inventory they are printed as placeholders. The same text, always **with placeholders**, is what this spec and the runbook show.
- `<machine>` is `hostname -s`. The pattern shown is the one recorded in P11; only that pattern's `curl` line (step 3) and only its vhost template (step 6) are printed, and both are printed when no pattern is known. Placeholders for one node's values (`<TAILNET_IP_AI_N4>`) stay placeholders when the inventory has no entry for this node.
- TO DO items are printed only when they apply: the reboot (laptop hardening applied or `/var/run/reboot-required`), the re-login (`--desktop-user`), the tailnet join, the BIOS note (laptops) and the VPS publication (not in phase 2). Laptops also get a "LAPTOP LID TEST" block after the tailnet text (heartbeat logger, 5 to 10 minutes with the lid closed, pass = no gaps and an unchanged `uptime -s`; source: [laptop-server-setup.md](../source/laptop-server-setup.md) §5.1) and the Ethernet note. The golden files `agent/internal/setup/summary/testdata/summary-ai-n4-{inventory,placeholders}.txt` show the exact text for a laptop with the reboot, the tailnet and the lid test pending.
- With `--reboot` and a reboot needed, the summary is followed by "Rebooting in 10 seconds. Press Ctrl-C to cancel." and `systemctl reboot`.
- In phase 2 the cloud automates steps 2–7 ([cloud-app.md](cloud-app.md)); the summary then says "The cloud will publish ai-n4.marin.cr automatically."

### 4.4 Uninstall
`sudo bash install-node.sh --uninstall [--purge-config] [--purge-content] [--revert-hardening] [--restore-legacy]`
1. Confirm: "This removes Local AI from this computer. Models and content in /srv/localai are kept unless you add --purge-content. Continue? [y/N]".
2. If enrolled: `aictl cloud unenroll` (best effort, 10 s).
3. `systemctl disable --now localai-sync.timer localai-drill.timer localai-whisper localai-kiwix localai-llama localai-agent`; remove our Tailscale Serve listeners (`tailscale serve --https=443 off` / `--tcp=18080 off` per pattern) unless `--restore-legacy`.
4. Remove the unit files, `daemon-reload`.
5. `--restore-legacy`: the unit to restore is `legacy_unit` of `/var/lib/localai/migration/llama-qwen/plan.json` (`llama-qwen.service` when absent). If its copy `/var/lib/localai/migration/llama-qwen/<unit>` exists (saved by the migration, [llama-router.md](llama-router.md) §4.9.2 M9; the installer never keeps its own copy), in this order: (a) replace the symlink `/etc/llama-qwen/api-key` with a real file, a copy of `/etc/localai/api-key` (root 0600), **before** anything under `/etc/localai` can be removed; (b) unmask and restore the unit to `/etc/systemd/system/<unit>` (the other saved legacy units are unmasked and restored too, not started), `daemon-reload`; (c) `systemctl enable --now <unit>`. Only then do steps 6–7 remove the agent and (with `--purge-config`) `/etc/localai`. Clients keep working (same port and key).
6. Remove `/opt/localai` (including `/opt/localai/installers/`), `/usr/local/bin/aictl`, `/usr/local/sbin/install-node.sh`, `/etc/polkit-1/rules.d/50-localai-operators.rules`.
7. `--purge-config`: remove `/etc/localai` and `/var/lib/localai` (asks to type `DELETE`). Without it they stay, so a reinstall keeps the node id, key, token and PIN.
8. `--purge-content`: remove `/srv/localai` (asks to type `DELETE` and shows its size). Never touches ark drives.
9. `--revert-hardening` (`aiagent setup harden --revert`): restore (from the oldest backup, i.e. what was there before Local AI) or remove the logind drop-in, `systemctl unmask` the sleep targets, remove the dconf file and run `dconf update`, remove the NM conf and dispatcher script; `reboot_required` if logind changed. (SSH, packages, users and groups are left.)
10. `userdel localai` and `groupdel localai-operators` only with `--purge-config`.
Exit 0 with a summary of what was kept.

Details of the implementation (INST-08): the confirmations (the `[y/N]` question and each `DELETE`) are all asked up front, before anything changes, and `--yes` answers none of them; a missing answer or no terminal is exit 4. `--restore-legacy` together with `--purge-content` is exit 2 (the migration moved the legacy models into `/srv/localai`, so deleting them would break the service being restored). If `--restore-legacy` finds no saved `llama-qwen.service`, the installer warns, says the old service is not restored, and continues; if the saved unit exists but `/etc/localai/api-key` does not, or the restore fails, it stops with `LAI-INS-080` before anything under `/etc/localai` is removed. Every recursive delete goes through a guard that accepts only `/opt/localai`, `/etc/localai`, `/var/lib/localai` and `/srv/localai` (`LAI-INS-081` otherwise). Without `--purge-config` the installer deletes only its own phase record (`state.json`) so that a reinstall re-runs every phase. `--revert-hardening` calls the INST-03 `revert_hardening` function and says plainly when it is not available.

### 4.5 Repair
`sudo bash install-node.sh --repair` re-runs every phase with checks in "verify and fix" mode:
- binaries are re-verified against SHA256SUMS (and replaced from the release or kit if wrong), including `/opt/localai/installers/install-node.sh` and its `/usr/local/sbin/install-node.sh` symlink,
- units are rewritten from the templates,
- directory modes are fixed,
- packages missing from the list are reinstalled,
- the llama build is smoke-tested and rebuilt if broken,
- `aictl doctor` is run at the end.
The installer copy and the symlink are repaired by the driver itself: the copy comes from the script that is running (or, when the running script is the installed one, from `<ark>/localai/kit/installers/install-node.sh` given with `--from-ark`; if neither exists the installer says it cannot judge the installed copy). Every other item is done by its phase: the driver forces all 15 phases to run (an "already done" record is not trusted) with `REPAIR_MODE=1`, and a phase in that mode must verify and fix without touching identity or secrets. A failing final `aictl doctor` makes `--repair` exit 1.
It never changes the node id, key, agent token, PIN, cloud credential, profile, or content. It is what doctor fixes like "Run `sudo install-node.sh --repair`" mean.

**Repair and the agent's own install pass share their code.** The idempotent steps that `--repair` re-runs for the tools and the packages (`aiagent setup binaries --kit`, `aictl kit install-debs`, [§4.3 P6](#p6-packages)) are the functions the agent's install engine calls by itself ([rebuild-kit.md](rebuild-kit.md) §4.5, [ADR-0010](../decisions/0010-the-agent-keeps-the-node-complete.md)): `kit.ToolInstaller.Install` and `kit.Apt.Run`. A node whose agent runs therefore needs `--repair` only for what is not on that list (users, directories, units, the llama build, the laptop hardening); the doctor fixes that used to say "run `--repair`" for a missing tool or package now lead with `aictl ensure` or the **Install what's missing** button. The installer itself adds no installer logic for this: it neither duplicates nor calls the engine.

### 4.6 Logs
- `/var/log/localai-install.log` (root 0600), appended. Every line: `2026-11-02T18:04:11Z [P6 packages] <message>`.
- Phase start and end lines with duration and result; on failure the failing command, exit code and the last 50 lines of its output.
- **Scrubbing** (the `scrub` filter, applied to everything that reaches the log or the screen): `nod_…`, `lat_…`, `lai_…`, `pat_…` tokens, any 64-hex string, and the PIN input (never echoed). Replaced by `<redacted:prefix…last4>`.
- `-v` adds `set -x` tracing to the log only (also scrubbed).
- The log is rotated by the installer itself when it exceeds 10 MB (renamed to `.1`, one generation).
- `/var/lib/localai/install/state.json` records each phase result for `aictl doctor` and for the cloud (the agent reports `install.version` and `reboot_required` in heartbeats).

## 5. Failure handling and edge cases

| Situation | Behaviour | Code |
|---|---|---|
| Unsupported Ubuntu or arch | exit 3 before any change | `LAI-INS-001` |
| Inside a container | exit 3 | `LAI-INS-002` |
| Not run with sudo / no desktop user | exit 3 | `LAI-INS-003` |
| NVIDIA GPU without working driver | offer driver install (reboot), or continue as cpu | `LAI-INS-010` |
| Secure Boot blocks the NVIDIA module | instructions for MOK enrollment at next boot | `LAI-INS-011` |
| Low RAM / low disk | warn / fail | `LAI-INS-012` / `013` |
| Port conflict | fail naming the process | `LAI-INS-014` |
| No internet online install | fail, suggest `--from-ark` | `LAI-INS-015` |
| Kit incomplete for this machine | fail, list missing items | `LAI-INS-016` |
| Node id differs from existing node.yaml | fail; use `--uninstall --purge-config` to change identity | `LAI-INS-020` |
| Agent binary signature/hash fails | fail | `LAI-INS-021` |
| apt failure | fail with apt output | `LAI-INS-030` |
| `--no-migrate` but llama-qwen holds :8080 | fail | `LAI-INS-031` |
| llama.cpp build fails | fail; the old unit (if any) keeps running | `LAI-INS-032` |
| Migration fails | rollback by llama-router, fail | `LAI-INS-033` |
| Agent doesn't become healthy | fail with `journalctl -u localai-agent -n 50` | `LAI-INS-034` |
| Users, folders, permissions or companion programs (kiwix-tools, pmtiles, piper) can't be set up | fail with the step's message (a `LAI-KIT-nnn` code too when the kit is damaged) | `LAI-INS-035` |
| Hardening check fails after reboot | doctor warning | `LAI-INS-040`…`044` |
| Serve pattern disagrees with inventory | install: fail (error); doctor later: warning | `LAI-INS-050` |
| No TTY for the PIN | skip with warning | `LAI-INS-060` |
| Reboot pending | summary and doctor | `LAI-INS-070` |
| Installer interrupted (Ctrl-C, power loss) | re-run resumes; each phase checks first | — |
| Second installer running | `flock /var/lib/localai/install/lock`; exit 1 "another install is running" | `LAI-INS-004` |

## 6. Security considerations

- The script is downloaded and hash-checked before running; the agent binary is verified with minisign (key embedded in the script) and SHA256SUMS; offline artifacts are verified against kit manifests and the same signature.
- The credential never appears in argv: `--enroll` reads it from `/dev/tty` (hidden), `--enroll-file` from a file, and it is passed to `aictl` on stdin. The cloud dialog uses `--enroll`.
- `umask 077` for the whole run; secrets never appear in the log (§4.6). The agent it calls (`aiagent setup …`, `aiagent migrate`, `aictl rebuild-llama`, the catalog install) sets its own umask to 022 when it runs as root, so what it shares with `localai` (builds and programs under `/opt/localai`, models under `/srv/localai`, `/etc/localai/catalog`) is readable; every secret is written with an explicit 0600/0640 mode, which a umask can only narrow.
- No `curl | sh` of third-party scripts (Tailscale is installed from its signed apt repo).
- The installer never opens ports, never enables `ufw` rules on its own, never runs `tailscale serve` or `funnel`, and never restarts logind.
- The offline `[trusted=yes]` apt source exists only during P6 and is limited to the kit repo whose files were re-verified.
- Backups of replaced system files are root 0700.

## 7. Observability

Log format §4.6. `aictl doctor` reads `state.json` and runs the `LAI-INS` checks. The agent's `/status` includes `install: {version, finished_at, reboot_required, hardening: {applied, verified}}`.

## 8. Doctor codes (installer output is terminal-only; English and Spanish)

**Language** (issue #436; [ADR-0009](../decisions/0009-optimized-for-humans.md) rule 7). The installer is the main tool of the offline rebuild ([runbook 06](../runbooks/06-rebuild-a-node-offline.md)), so it speaks English and Spanish: `--lang en|es`, default from the locale (§3.2). In Spanish these are translated: the whole guided mode (§3.1.1), the phase titles `==> [n/15] …` and the phase result lines, the preflight verdict, the end-of-run lines, and every `LAI-INS` message. A `LAI-INS` message in Spanish is the code, its Spanish title and fix, then the English cause (it names paths and values) as `Detalle (en inglés): …`:
```
ERROR LAI-INS-004: Se está ejecutando otro instalador.
  Qué hacer: Espere a que termine.
  Detalle (en inglés): another installer is running (lock /var/lib/localai/install/lock). Wait for it to finish.
Más información: docs/specs/doctor-codes.md (LAI-INS-004)
```
The English output is unchanged. Not translated yet: the `key: value` header lines, the per-check `ok` lines, the `--repair` and `--uninstall` dialogs, `--help`, and the end-of-install summary with its TO DO and VPS CHECKLIST, which `aiagent setup summary` prints (P15; a follow-up of #436).

**One source for the sentences.** The title and fix of every `LAI-INS` code, in English and Spanish, come from `agent/internal/doctor/codes.go`. The installer runs before `aiagent` exists, so it carries a generated copy: the `ins_text` function between `# BEGIN GENERATED ins-codes` and `# END GENERATED ins-codes` in `install-node.sh`. `scripts/check-installer-codes.sh --write` regenerates it (a Markdown code span becomes `'…'`); without `--write` the script fails when the copy differs from `codes.go` or when the installer uses a `LAI-INS` code that has no entry there. The installer CI workflow runs the check and its test, `scripts/tests/test-installer-codes.sh`. So a change to a `LAI-INS` entry in `codes.go` is followed by `--write` in the same pull request.


| Code | Sev | Meaning | Fix |
|---|---|---|---|
| LAI-INS-001 | error | Unsupported Ubuntu version or architecture | Install Ubuntu 24.04 or 26.04 LTS (amd64 or arm64). |
| LAI-INS-002 | error | Running inside a container | Run the installer on the real machine. |
| LAI-INS-003 | error | Not run with sudo from a normal user | Run `sudo bash install-node.sh …` from the desktop user's terminal. |
| LAI-INS-004 | error | Another installer is running | Wait for it to finish. |
| LAI-INS-010 | error | NVIDIA GPU found but its driver doesn't work | Accept the driver install and reboot, or continue without GPU. |
| LAI-INS-011 | warning | Secure Boot may block the NVIDIA driver | At the next boot choose "Enroll MOK" and type the password you set. |
| LAI-INS-012 | warning | Less than 8 GB of RAM | Only the cpu-only profile will work well. |
| LAI-INS-013 | error | Not enough free disk space | Free space or use `--yes` to accept a smaller budget. |
| LAI-INS-014 | error | A needed port is in use | Stop the program shown, then re-run. |
| LAI-INS-015 | error | No internet for an online install | Connect to the internet, or use `--from-ark <drive>`. |
| LAI-INS-016 | error | The ark drive lacks files for this computer | Sync the drive on a computer with internet first. |
| LAI-INS-017 | warning | The clock is wrong | Set the date and time, then re-run. |
| LAI-INS-020 | error | This computer already has a different node id | Keep the old id, or uninstall with `--purge-config` first. |
| LAI-INS-021 | critical | The agent download failed its signature check | Download again; if it repeats, don't install and report it. |
| LAI-INS-030 | error | Package installation failed | If the disk is full, free some space; if a package from the kit is broken, copy the kit again from the AI-ARK drive; then run the installer again. |
| LAI-INS-031 | error | The old llama-qwen service is still using port 8080 | Re-run without `--no-migrate`. |
| LAI-INS-032 | error | Building llama.cpp failed | Read `build.log` in the failed build folder under `/opt/localai/llama.cpp/builds/` (named in the message); run `sudo install-node.sh --repair`. |
| LAI-INS-033 | error | Migration failed and was rolled back | The old service is running again. Read the log and report it. |
| LAI-INS-034 | critical | The agent didn't start | Run `journalctl -u localai-agent -n 50` and `sudo install-node.sh --repair`. |
| LAI-INS-035 | error | Users, folders, permissions or companion programs could not be set up | Read the lines above, fix what they name (disk full, read-only disk, damaged ark drive), and re-run `sudo install-node.sh`. |
| LAI-INS-040 | warning | Lid/power-key settings aren't active | Reboot the computer (don't restart logind). |
| LAI-INS-041 | warning | Sleep targets aren't masked | Run `sudo install-node.sh --repair`. |
| LAI-INS-042 | warning | GNOME idle suspend is still on | Log in as the desktop user and run `sudo install-node.sh --repair`. |
| LAI-INS-043 | warning | Power profile isn't "performance" | Run `powerprofilesctl set performance`. |
| LAI-INS-044 | warning | Wi-Fi power save is on | Run `sudo install-node.sh --repair`; use Ethernet if possible. |
| LAI-INS-050 | error (warning in doctor) | Tailscale serve pattern doesn't match the inventory | Use `--serve-pattern` with the value in the inventory. |
| LAI-INS-060 | warning | The admin PIN wasn't set | Run `sudo aictl pin set`. |
| LAI-INS-070 | info | A reboot is needed to finish the install | Reboot when convenient. |
| LAI-INS-080 | error | Putting the old llama-qwen service back failed | Nothing under /etc/localai was removed. Read the message above, fix it, and run the same --uninstall command again. |
| LAI-INS-081 | error | The uninstaller refused to delete a path it does not own | Nothing was deleted. Report the log at /var/log/localai-install.log. |

## 9. Test plan

### 9.1 Unit
- `aiagent setup` steps are Go with fakes for exec, filesystem and systemd: table tests for `dirs` (create, fix modes), `node-yaml` (fresh, merge, id conflict), `units` (render, backup on change, idempotent), `harden` (file contents byte-exact to §4.3 P4, idempotency, no logind restart), `api-key` (keep, generate, format), `profile-suggest` (catalog match fixtures for ai-n1/n2/n3 hardware).
- Bash: `shellcheck -S style installers/install-node.sh` in CI; `bats` tests for argument parsing, `scrub` (all token patterns), dry-run output, the "no logind restart" grep.

### 9.2 Integration
- A CI job boots an Ubuntu 24.04 and a 26.04 VM image (cloud image + cloud-init, no GPU) and runs: `--dry-run` (no changes: compare a filesystem snapshot), a full online install with `--backend cpu --no-tailscale --no-sync --pin-file`, a re-run (every phase "already done"), `--repair`, `--uninstall --purge-config --purge-content`.
- Offline: the same VM with networking disabled and a disk image containing a minimal kit (`--from-ark`).
- Migration: a VM with a fake `llama-qwen.service` (a tiny HTTP server on :8080 requiring a key) → install → the same key works on :8080 through `localai-llama` (with a CPU model fixture).

### 9.3 Manual on a real node
1. ai-n3 dry run: `sudo bash install-node.sh --node-id ai-n3 --dry-run` → shows migration of `llama-qwen.service`, build of `f3586e66b` for `cuda` arch 75, hardening steps already applied (from the earlier manual setup) as "already done", no changes made.
2. Real run during a maintenance window (owner present), then `curl -s -H "Authorization: Bearer $(sudo cat /etc/localai/api-key)" http://127.0.0.1:8080/v1/models` → 200 with catalog ids; from the VPS, `https://ai-n3.marin.cr/v1/models` with the key → 200 (unchanged URL and key).
3. Reboot; `aictl doctor` → no `LAI-INS` problems; lid test from the summary passes.

## 10. Implementation breakdown

1. **INST-01 Driver skeleton, logging, flags, dry-run framework** (1 d). Scope: §3.2 flags, §4.1, §4.2, §4.6 scrub, flock, exit codes, `state.json`. Acceptance: bats tests; shellcheck clean.
2. **INST-02 Preflight and staging** (1.5 d). Depends on: INST-01. Scope: P1 checks, P2 agent download/verify (minisign + sha) online and offline, catalog install, `aiagent setup preflight`/`profile-suggest`. Acceptance: VM tests; signature failure test.
3. **INST-03 Laptop hardening** (1.5 d). Scope: P4 all steps with checks and doctor verifications `LAI-INS-040…044`; never touching logind; reboot flag; `--revert-hardening`. Acceptance: byte-exact files; VM test; manual lid test.
4. **INST-04 Users, dirs, packages, companion binaries** (1 d). Scope: P5 (incl. the optional polkit rule and `--no-polkit`), P6 online and offline local-repo install, `aiagent setup binaries`. Acceptance: modes match overview.md; offline VM install with no network.
5. **INST-05 llama.cpp build/reuse and migration hand-off** (1 d). Depends on: KIT-04, ROUTER-09. Scope: P7, P8. Acceptance: reuse of `~/llama.cpp` source; migration VM test with rollback.
6. **INST-06 Agent, units, key, PIN, enroll** (1 d). Scope: P9, P10, P12, P13. Acceptance: agent healthy; units pass `systemd-analyze verify`; PIN never in logs.
7. **INST-07 Tailscale, initial content, summary and VPS checklist** (1 d). Scope: P11 (incl. `LAI-INS-050` as error during install, warning in doctor), P14, P15 incl. inventory-filled vs placeholder output. Acceptance: golden summary text for ai-n4 with and without inventory.
8. **INST-08 Uninstall and repair** (1 d). Scope: §4.4, §4.5 incl. `--restore-legacy`. Acceptance: VM round trip install → uninstall → legacy service works again.

## 11. Open questions (with proposed defaults)

1. **Unattended upgrades and NVIDIA/kernel updates.** An automatic kernel or driver update can break CUDA until the next rebuild. *Default:* don't blacklist anything; the drill detects a mismatch ([drill.md](drill.md)) and `aictl rebuild-llama` fixes it. Revisit after the first incident.
2. ~~**`aiagent setup` naming.**~~ Resolved (2026-11-02): internal subcommand of the agent binary; [aictl.md](aictl.md) §4.28 lists it (and `aiagent migrate`) as internal and not stable.
