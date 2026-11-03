# Spec: Ark sync (content roots, ark drive, planner, downloader, verifier, readiness)

> **Status:** approved design · **Epic:** E3 · **Code:** `agent/internal/ark/` (roots, drive detection, readiness), `agent/internal/sync/` (planner, resolvers, downloader, journal), `agent/internal/sync/resolve/<type>.go` · **Doctor areas:** `LAI-SYN` (sync), `LAI-ARK` (ark drive and content roots)
> Canonical names: [overview.md](../architecture/overview.md) §3. Catalog schemas: [catalog.md](catalog.md). Modes: [modes.md](../architecture/modes.md). API routes: [agent.md](agent.md) §3.8. CLI: [aictl.md](aictl.md).

---

## 1. Purpose and scope

Ark sync keeps every **content root** on a node (the internal disk, plus any mounted AI-ARK drive) filled with exactly the models and content its profile asks for. Every byte is verified against a known sha256, and sync **never** deletes the last verified copy of anything. It also writes a readiness report (`READY.json`) that tells a human, the desktop, the Portal and the cloud whether the node could survive with no internet today.

In scope:
- Content roots: discovery, identity, layout, the "best copy" lookup used by other components.
- Ark drive detection, read-only and unclean handling, initialization and safe eject.
- The planner: profile, overrides, budget, `min_free_bytes`, tier ordering, dry run.
- The per-item state machine, resumable downloads, streaming sha256, atomic swap.
- One resolver per catalog source type.
- The journal, concurrency, bandwidth limit, retries, disk-full handling.
- Retention, `.trash`, quarantine and periodic re-verification.
- `MANIFEST.json` and `READY.json` schemas and the green/yellow/red rules.
- `aictl ark import` (drive → internal, works offline).
- Extras written to the drive: `inventory.local.yaml`, `START-HERE.html`.
- `localai-sync.timer` and `localai-sync.service`.
- Reporting to the cloud.
- Formatting a new AI-ARK drive.

Non-goals:
- **No peer-to-peer sync** between nodes (ADR-0006). A node gets content from the internet or from a drive plugged into it.
- **No deduplication across roots** by hard links or reflinks. Each root is a self-contained copy that must survive the loss of the others.
- **No encryption** of the drive (security-model.md T9: unencrypted by design).
- **No partial ZIM or partial model use.** An item is usable only when fully verified.
- **No editing of the catalog.** The planner consumes it ([catalog.md](catalog.md)).
- What goes *in* the rebuild kit is defined in [rebuild-kit.md](rebuild-kit.md). This spec only downloads and verifies kit items like any other item.
- Rendering docs is `aictl docs render` ([aictl.md](aictl.md) §docs). Sync only schedules it (the `repo_docs` resolver).

## 2. Concepts and glossary

| Term | Meaning |
|---|---|
| **Content root** | A directory with the layout of overview.md §3.3. `internal` is `/srv/localai`. An **ark drive root** is `<mount>/localai` on an ext4 filesystem labelled `AI-ARK` |
| **Root id** | `internal`, or `ark-<first 8 hex of the filesystem UUID, lowercase>`, e.g. `ark-3f9c0a12`. Stable for the life of a filesystem. Shown in UIs as "Internal disk" or "Ark drive (3f9c0a12)" |
| **Item** | One catalog entry (a model from `models.yaml` or a content entry from `ark.yaml`) as it applies to one root |
| **Version** | A concrete, immutable set of files for an item: an HF revision, a dated ZIM, a planet build date, a git commit, a release tag or an apt closure hash. Written as a short string (`version` in MANIFEST) |
| **Verified copy** | An item version whose `MANIFEST.json` has `state: verified` and whose files all match their sha256. Only verified copies are served, loaded or counted as "present" |
| **Plan** | The ordered list of actions the planner computes for one root |
| **Run** | One execution of sync over one or more roots (scheduled, manual, cloud job, or import). Has a run id `run-YYYYMMDDTHHMMSSZ-<4 hex>` |
| **Best copy** | For an item, the verified copy that other components should use: internal first, then ark drives in mount order (§4.2) |

## 3. Interfaces

### 3.1 Files and directories

| Path | Written by | Purpose |
|---|---|---|
| `<root>/ROOT.json` | sync (on root init) | Root identity (§3.2) |
| `<root>/READY.json` | sync (end of every run, and on every verify/import) | Readiness report (§4.12) |
| `<root>/<item-dir>/MANIFEST.json` | sync (the commit point of a swap) | Per-item manifest (§4.11) |
| `<root>/.staging/<item-id>/<version>/<file>.part` | downloader | Partial download, same filesystem as the target |
| `<root>/.staging/<item-id>/<version>/<file>` | downloader | Fully downloaded, verified, waiting for swap |
| `<root>/.staging/quarantine/<item-id>/<UTC-timestamp>/` | verifier | Files that failed verification, plus `reason.json` |
| `<root>/.trash/<item-id>/<version>/` | swapper | The previous version, kept until retention allows deletion (§4.10) |
| `/var/lib/localai/sync/items.json` | journal | Current state of every (root, item) pair. Atomic write (tmp + `fsync` + rename + `fsync` of the directory); the previous good file is kept as `items.json.bak` (§4.5.2) |
| `/var/lib/localai/sync/runs/<run-id>.jsonl` | journal | Append-only event log of one run. Last 60 runs kept |
| `/var/lib/localai/sync/apt/<codename>-<arch>/` | `apt_closure` resolver | Private apt state (lists, cache config). Never `/var/lib/apt` |
| `/var/lib/localai/sync/lock` | agent | `flock` guard so that two agent processes can't sync at once |
| `<mount>/START-HERE.html` | sync (drive roots only) | §4.15 |
| `<mount>/inventory.local.yaml` | sync (drive roots only, optional) | §4.15 |

**Item directory by kind** (`<item-dir>` above):

| Catalog entry | `<item-dir>` |
|---|---|
| `models.yaml`, `engine: llama` | `models/<model-id>/` |
| `models.yaml`, `engine: whisper` | `speech/whisper/<model-id>/` |
| `models.yaml`, `engine: piper` | `speech/piper/<model-id>/` |
| `ark.yaml`, `kind: kiwix` | `kiwix/<content-id>/` |
| `ark.yaml`, `kind: maps` | `maps/<content-id>/`, except `map-assets`, which is `maps/assets/` |
| `ark.yaml`, `kind: kit` | `kit/<subdir>/`, where `<subdir>` comes from [rebuild-kit.md](rebuild-kit.md) §Layout (e.g. `kit/src/`, `kit/debs/resolute-amd64/`) |
| `ark.yaml`, `kind: docs` | `docs/` |

Modes: directories 0755, files 0644, owner root. Services that read content (`localai-llama`, `localai-kiwix`, `localai-whisper`) run as `localai` and only need read access, so numeric uid differences between machines (the `localai` uid differs per node) never matter. On root init the agent `chown`s only the top directory `<root>` to the local `localai` user, not recursively. `.staging/` and `.trash/` are 0700 root.

### 3.2 `ROOT.json`

```json
{
  "schema_version": 1,
  "root_id": "ark-3f9c0a12",
  "kind": "ark_drive",
  "profile": "ark-drive-full",
  "created_at": "2026-11-02T18:04:11Z",
  "created_by": {"node_id": "ai-n3", "agent_version": "0.4.0"},
  "label": "AI-ARK",
  "notes": ""
}
```
- `kind` ∈ `internal | ark_drive`.
- `profile` is the profile this root is planned with. Internal roots always use `node.yaml: profile` and ignore this field (it's informational). Drive roots use this field, default `ark-drive-full`. It is changed with `aictl ark drives set-profile <root-id> <profile>` (PIN).
- If `ROOT.json` is missing on a drive that has a `localai/` directory with content, the agent recreates it with defaults and logs `LAI-ARK-010` (info). The defaults are the drive's label, `sync.ark_drive.default_profile`, this node and agent version, and the current time. Only `ROOT.json` is written: the layout is never created on a drive except by `aictl ark init`. A read-only drive is not written; it is served with the defaults in memory and the file is recreated when the drive is writable. A `localai/` directory that is empty counts as no `localai/` (uninitialized).
- Rules of the file (`agent/internal/ark/rootjson.go`): `schema_version` must be `1`; `kind` must be `internal` or `ark_drive`; `root_id` must be `internal` or `ark-<8 lowercase hex>` and fit `kind`; `created_at` is RFC 3339. Keys the agent does not know (also inside `created_by`) are kept when it rewrites the file. The agent writes the known keys in the order above, then the unknown ones sorted by name, through the atomic writer (temp file, `fsync`, rename, `fsync` of the directory), mode 0644.
- The root id always comes from the filesystem UUID, never from the file: a stale `root_id` after `tune2fs -U random` is logged and ignored. The UUID may be the 36-character dashed form or any 8 to 32 hex digits (dashes dropped, first 8 digits kept); a UUID with a non-hex character, fewer than 8 digits or all zeros gives no root id and the drive is not used as a root.
- A damaged `ROOT.json` on a drive (not JSON, wrong type, impossible value) or one with a newer `schema_version` is never rewritten: the drive's root is `unusable` and the error says why. On the internal root the file is informational, so a damaged one is renamed to `ROOT.json.damaged-<UTC time>` and replaced by the defaults, and a newer schema is left alone.

### 3.3 node.yaml keys owned by this spec

`node.yaml` is defined in [agent.md](agent.md) §3.4. The `sync:` block is owned here; agent.md must include it verbatim.

```yaml
sync:
  enabled: true                 # false = scheduled runs exit "skipped: disabled"
  concurrency: 2                # max simultaneous network downloads (all roots together), 1..4
  per_root_writers: 1           # max simultaneous writers per root; drives are slow on parallel writes
  bandwidth_limit: "0"          # "0" = unlimited, else e.g. "20MiB/s", "100Mbit/s"
  allow_on_battery: false       # scheduled runs skip when on battery
  verify_reread: true           # re-read the file from disk after download before staging (§4.5)
  reverify_days: 30             # every verified item is re-hashed at least this often
  reverify_budget_minutes: 90   # max time per scheduled run spent on re-verification
  trash_days: 14                # how long a superseded version is kept (§4.10)
  quarantine_days: 7
  max_run_hours: 12             # scheduled run stops gracefully after this
  hf_token_file: /etc/localai/hf-token   # optional; used only for huggingface.co requests
  ark_drive:
    enabled: true               # false = drives are detected and served but never written
    default_profile: ark-drive-full
    copy_inventory: true        # §4.15
```

Validation errors in this block are `LAI-AGT` config errors (agent.md). Unknown keys are rejected.

### 3.4 CLI (summary; full syntax in [aictl.md](aictl.md) §ark)

| Command | API | Notes |
|---|---|---|
| `aictl ark` / `aictl ark status [--json]` | `GET /ark` | READY.json summary per root |
| `aictl ark drives [--json]` | `GET /ark/drives` | Detected drives, including unusable ones and why |
| `aictl ark sync [--root <id>]... [--item <id>]... [--dry-run] [--json] [--allow-offline-mode] [--scheduled] [--wait]` | `POST /ark/sync`, then `GET /ark/sync` or `/events` | `--scheduled` is used only by `localai-sync.service` (§3.7) |
| `aictl ark sync --cancel` | `DELETE /ark/sync` | Graceful: `.part` files stay |
| `aictl ark verify [--root <id>]... [--item <id>]... [--full] [--wait]` | `POST /ark/verify` | Re-hash. `--full` ignores `reverify_days` and hashes everything |
| `aictl ark import [--from <root-id>] [--item <id>]... [--add-to-profile] [--catalog] [--dry-run] [--wait]` | `POST /ark/import` | §4.14 |

**Exit codes** ([aictl.md](aictl.md) §3.4 is authoritative): with `--wait` or `--scheduled`, `0` = the run finished and every item is verified or up to date, `8` = finished with warnings (some items failed or were skipped; last good copies kept), `9` = finished with errors (run `failed` or aborted). A `--scheduled` run that is **skipped** in preflight (§4.3) prints `i Sync skipped: <reason>.` and exits `0`. Exit `3` only ever means "agent not reachable".
| `aictl ark eject <root-id>` | `POST /ark/drives/{root_id}/eject` | §4.1.5 |
| `aictl ark init <mount-path\|device\|root-id>` | `POST /ark/drives/{root_id}/init` | Creates `localai/` and `ROOT.json` on an empty AI-ARK filesystem. The CLI resolves a mount path or device to the drive's `root_id` with `GET /ark/drives` first (every drive with a filesystem UUID is listed with its `root_id`, including `uninitialized` ones) |
| `aictl ark mount <root-id>` | `POST /ark/drives/{root_id}/mount` | Mounts a detected but unmounted AI-ARK filesystem (`LAI-ARK-005`) |
| `aictl ark drives set-profile <root-id> <profile>` | `PUT /ark/drives/{root_id}/profile` (PIN) | Writes `ROOT.json` `profile` |
| `aictl ark prune [--root <id>] [--orphans] [--trash] [--dry-run] [--yes]` | `POST /ark/prune` (PIN) | Deletes orphans and trash. Never deletes the last verified copy (§4.10 R1/R6); there is no override flag |

### 3.5 HTTP API (shapes; routes from agent.md §3.8)

**`POST /api/v1/ark/sync`** request:
```json
{"roots": ["internal", "ark-3f9c0a12"], "items": [], "dry_run": false, "allow_offline_mode": false, "trigger": "manual"}
```
- `roots` empty or absent = every writable mounted root. `items` empty = the whole plan.
- `trigger` ∈ `manual | scheduled | cloud | mode_online | install | verify | import`. Set by the caller (the CLI sets `manual` or `scheduled`; the cloud job handler sets `cloud`). `verify` and `import` are set by the agent itself for runs started by `POST /ark/verify` and `POST /ark/import` (they share the run slot and appear in `GET /ark/sync` and READY `last_sync`); the cloud snapshot `sync.last_run` maps them like any other trigger (§4.16).
- Response `202`:
```json
{"run_id": "run-20261102T031500Z-a1f3", "state": "running", "events": "/api/v1/events?types=sync.*"}
```
- `409` with `LAI-SYN-005` if a run is already active (body includes the active `run_id`).
- `409` with `LAI-SYN-010` in survival mode; `409` with `LAI-SYN-011` in offline mode unless `allow_offline_mode: true`.
- With `dry_run: true`, the response is `200` with the plan (§4.4.4) and nothing is written. The body is a **JSON array with one plan per root**, in the order of the request (every writable root, internal first, when `roots` is empty; a read-only root can be named and is planned with every non-keep action `skip (read_only)`). `items` limits the rows shown (an id that is in no plan is `400 LAI-AGT-020`, `details.field: "items"`); the budget, planned and peak figures still describe the whole plan. A dry run takes no run slot and is allowed in every mode: in offline and survival mode it plans without the network and says so (`offline: true`) instead of refusing with `LAI-SYN-010/011`. A root that is not mounted is `400 LAI-AGT-020` (`details.field: "roots"`), no writable root is `409 LAI-ARK-001`, and a profile the catalog lacks is `422 LAI-CAT-003`. Until the run engine (SYNC-07) is wired a request without `dry_run` answers `503 LAI-AGT-024` (`details.reason: "sync_engine_not_ready"`).

**`GET /api/v1/ark/sync`** → the active or last run:
```json
{
  "run_id": "run-20261102T031500Z-a1f3",
  "trigger": "scheduled",
  "state": "running",
  "started_at": "2026-11-02T03:15:02Z",
  "finished_at": null,
  "roots": ["internal", "ark-3f9c0a12"],
  "totals": {"items": 31, "done": 22, "skipped": 6, "failed": 0, "bytes_total": 61234567890, "bytes_done": 40211223344, "rate_bps": 18874368, "eta_s": 1105},
  "active": [
    {"root": "ark-3f9c0a12", "item": "wikipedia-en-maxi", "state": "downloading", "version": "2026-10", "bytes_done": 61200000000, "bytes_total": 110000000000, "rate_bps": 18874368, "attempt": 1}
  ]
}
```
`state` ∈ `running | cancelling | done | cancelled | failed | skipped`. `skipped` carries `reason` (`offline | mode | battery | disabled | no_roots`).

**`DELETE /api/v1/ark/sync`** → `202 {"run_id": "...", "state": "cancelling"}`. Every active transfer stops at the next 1 MiB boundary, checkpoints and exits. Idempotent.

**`GET /api/v1/ark`** → `{"roots": [<READY.json of each mounted root>]}` (§4.12).

**`GET /api/v1/ark/drives`**:
```json
{
  "drives": [
    {
      "root_id": "ark-3f9c0a12",
      "device": "/dev/sdb1",
      "mount": "/media/operator/AI-ARK",
      "fstype": "ext4",
      "label": "AI-ARK",
      "size_bytes": 2000398934016,
      "free_bytes": 812345678901,
      "state": "ready",
      "read_only": false,
      "fs_errors": 0,
      "initialized": true,
      "profile": "ark-drive-full",
      "ready": {"overall": "yellow", "generated_at": "2026-11-01T04:02:10Z"},
      "problems": []
    },
    {
      "root_id": null,
      "device": "/dev/sdc1",
      "mount": "/media/operator/AI-ARK1",
      "fstype": "exfat",
      "label": "AI-ARK",
      "state": "unusable",
      "problems": [{"code": "LAI-ARK-002", "title": "This drive is not formatted as ext4"}]
    }
  ]
}
```
Drive `state` ∈ `ready | read_only | unclean | not_mounted | unusable | ejecting | uninitialized`. `root_id` is derived from the filesystem UUID (§2), so it is set for every drive whose UUID blkid can read, including `uninitialized` and `not_mounted` ones; it is `null` only when there is no usable UUID (e.g. an `unusable` non-ext4 filesystem).

**`POST /api/v1/ark/verify`** request `{"roots": [], "items": [], "full": false}` → `202 {"run_id": ...}`. It runs as a run of trigger `verify`, sharing the run slot with sync (one at a time).

**`POST /api/v1/ark/import`** request:
```json
{"from_root": "ark-3f9c0a12", "items": [], "add_to_profile": false, "catalog": false, "dry_run": false}
```
→ `202 {"run_id": ...}` or `200` plan with `dry_run`. Allowed in **every mode**, including survival, because it uses no network. `from_root` absent and several drives mounted → `409 LAI-ARK-015` with `details.roots: [<root_id>…]` (the CLI checks first and exits 2 with the same list).

**`POST /api/v1/ark/drives/{root_id}/eject`** body `{"force": false}` (body optional; `force: true` = "eject anyway": unload models whose only copy is on this drive instead of refusing) → `202 {"state": "ejecting"}`, then the event `drive.unmounted` with `{"root_id", "safe_to_unplug": true}`; or `409` with `LAI-ARK-009` and `details.blockers: [{"kind": "process|model|sync", "name", "pid"}]`.

**`POST /api/v1/ark/drives/{root_id}/init`** body `{}` → `200` with the drive object of `GET /ark/drives` (state now `ready`). Only for a drive in state `uninitialized`; any other state → `409 LAI-AGT-020` with `details.state` (the drive's current state). Refused if `<mount>/localai` already exists.

**`POST /api/v1/ark/drives/{root_id}/mount`** body `{}` → `200` with the drive object once mountinfo shows the mount. Only for `not_mounted`; other states → `409 LAI-AGT-020` with `details.state`.

**`PUT /api/v1/ark/drives/{root_id}/profile`** (admin, PIN) body `{"profile": "<profile-id>"}` → `200 {"root_id", "profile"}`; an unknown profile id → `422 LAI-CAT-003`.

**`POST /api/v1/ark/prune`** (admin, PIN) request:
```json
{"roots": ["internal"], "orphans": true, "trash": false, "dry_run": false}
```
Response `200`:
```json
{"deleted": [{"root": "internal", "item": "osm-north-america", "version": "20260501", "bytes": 11200000000, "kind": "orphan"}],
 "refused": [{"root": "internal", "item": "gutenberg-en", "reason": "last_verified_copy"}],
 "freed_bytes": 11200000000}
```
`roots` empty = every writable mounted root. With `dry_run: true` nothing is deleted and `deleted` lists what would be. An orphan that is the only verified copy on any mounted root is always listed in `refused` (R6).

### 3.6 Events (on `GET /api/v1/events`)

| Event | Data |
|---|---|
| `sync.progress` | `{run_id, root, item, state, bytes_done, bytes_total, rate_bps}`, at most 1 per item per second |
| `sync.item_done` | `{run_id, root, item, result: verified|skipped|failed|quarantined, version, reason?, code?}` |
| `sync.done` | `{run_id, state, totals, ready: {<root_id>: overall}}` |
| `drive.mounted` | `{root_id, mount, state, read_only}` |
| `drive.unmounted` | `{root_id, safe_to_unplug: bool}` (`false` means a surprise removal) |
| `alert.raised` / `alert.cleared` | Generic, with `code` (agent.md) |

Consumers: [llama-router.md](llama-router.md) regenerates `models.ini` on `sync.item_done` for a llama model, and on `drive.*`. [companion-services.md](companion-services.md) regenerates the Kiwix library and the whisper env on the same events.

### 3.7 systemd units

`/etc/systemd/system/localai-sync.timer`:
```ini
[Unit]
Description=Local AI nightly ark sync (timer)
Documentation=https://github.com/marinoscar/local-ai/blob/main/docs/specs/ark-sync.md

[Timer]
OnCalendar=*-*-* 03:15:00
RandomizedDelaySec=45min
AccuracySec=1min
Persistent=true
Unit=localai-sync.service

[Install]
WantedBy=timers.target
```

`/etc/systemd/system/localai-sync.service`:
```ini
[Unit]
Description=Local AI ark sync (asks the agent to run one scheduled sync and waits for it)
Documentation=https://github.com/marinoscar/local-ai/blob/main/docs/specs/ark-sync.md
Requires=localai-agent.service
After=localai-agent.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=root
ExecStart=/usr/local/bin/aictl ark sync --scheduled --wait
# Exit codes: aictl.md §3.4. 0 = finished OK, or the whole run was skipped (offline/mode/battery/disabled);
# 8 = finished with warnings (some items failed or skipped, last good copies kept) -> still success;
# 9 = finished with errors, 3 = agent not reachable -> the unit fails, so it shows in `systemctl --failed`.
SuccessExitStatus=8
# The agent stops the run itself after sync.max_run_hours; this is a backstop.
TimeoutStartSec=13h
# On stop/timeout, aictl receives SIGTERM and sends DELETE /api/v1/ark/sync (graceful cancel).
KillMode=mixed
KillSignal=SIGTERM
TimeoutStopSec=60s
Nice=10
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=
```
Notes:
- `aictl` is only a client. All I/O happens inside the agent, so the agent itself lowers the priority of its sync workers. Each worker goroutine calls `runtime.LockOSThread()` and then `ioprio_set(IOPRIO_WHO_PROCESS, tid, IOPRIO_CLASS_IDLE)` and `setpriority(PRIO_PROCESS, tid, 10)` for its thread. Workers for manual runs use `IOPRIO_CLASS_BE` level 7 instead, so a person waiting gets results.
- `aictl ark sync --scheduled --wait` prints one line per `sync.item_done` and a final summary, so `journalctl -u localai-sync` shows what happened.
- The installer enables and starts both timers (`systemctl enable --now localai-sync.timer localai-drill.timer`, [installer.md](installer.md) §4.3 P9).

## 4. Behaviour

### 4.1 Content roots

#### 4.1.1 Internal root
- Path `/srv/localai`, root id `internal`, created by the installer. Profile from `node.yaml: profile` plus `profile_overrides`.
- It is always "mounted". If `/srv/localai` is missing or not a directory, the agent recreates it, empty, and raises `LAI-ARK-020` (doctor severity `warning`; the READY root colour is red because every tier-1 item is now missing, §4.12.2).
- It must be on a local filesystem. If `statfs` reports a network filesystem (nfs, cifs, fuse.sshfs), raise `LAI-ARK-021` and treat the root as read-only.
- **Checks** (`agent/internal/ark/internal.go`) run at agent start and every 6 hours, in this order:
  1. `statfs` on the root (on the nearest existing parent when the root is missing). Network means NFS (`0x6969`), SMB/CIFS (`0xFF534D42`, `0xFE534D42`) or any FUSE (`0x65735546`). Then nothing is written at all, not even a missing directory, and the root is `read_only`. A `statfs` that fails counts as local.
  2. Missing, or not a directory: whatever is there (a file, a dangling symlink) is **renamed** to `<path>.replaced-<UTC time>`, never deleted, and the root is recreated empty with the layout of overview.md §3.3. A symlink to a directory counts as a directory (an operator may point `/srv/localai` at a bigger disk). `LAI-ARK-020` is raised by the check that recreated it and stays raised until a sync or import has refilled the root.
  3. Otherwise missing layout directories are created (silently), `.staging/` and `.trash/` are set to 0700, and a missing `ROOT.json` is written with `kind: internal`.
  The layout helper creates only directories, with explicit modes (the umask does not matter), and `chown`s the top directory to `localai` only when it created it. A file where a layout directory belongs is an error and is never replaced. Any failure (read-only filesystem, disk full, permission) leaves the root `read_only` (`unusable` when the directory itself does not exist) and the next check tries again.

#### 4.1.2 Ark drive
**Detection** (in `agent/internal/ark/detect.go`):
1. The agent opens `/proc/self/mountinfo` and `poll(2)`s it for `POLLPRI|POLLERR`. The kernel signals every mount-table change this way. A fallback timer re-reads it every 30 s, in case a container or kernel quirk hides the signal.
2. On each change it parses every line (fields: mount id, parent id, `major:minor`, root, mount point, mount options, optional fields, `-`, fstype, source, super options). Octal escapes in paths (`\040` for a space) are decoded.
3. Candidate mounts: any with mount point not under `/proc`, `/sys`, `/dev`, `/run/user/*/gvfs`, `/snap`. For each candidate whose `major:minor` is not in the cache, run `blkid -p -o export <source>` (timeout 5 s) and read `LABEL`, `UUID`, `TYPE`. Cache results by `major:minor` + `source` until the mount disappears.
4. A mount is an **ark drive** if `LABEL=AI-ARK` exactly (case-sensitive; also `AI-ARK` followed by nothing else). Labels like `AI-ARK2` are not ark drives. Use the same label on every drive; the UUID tells them apart.
5. Unmounted drives: every 30 s (and on udev-free systems only by this poll) run `blkid -t LABEL=AI-ARK -o device`. Any device listed that is not mounted is reported as `state: not_mounted` with `LAI-ARK-005`. The agent does **not** auto-mount. The UI offers **Mount**, which calls `udisksctl mount --no-user-interaction -b <device>` (as root it mounts at `/media/root/AI-ARK`, which is fine: any mount point is allowed, ADR-0005).

**Checks per ark drive**, in order; the first failing check sets the state:
| Check | How | Result |
|---|---|---|
| fstype | `TYPE` from blkid **and** the mountinfo fstype | not `ext4` → `unusable`, `LAI-ARK-002`. The drive is never read or written. Plain text in the UI: "This drive must be formatted as ext4. See *Formatting a new AI-ARK drive*." |
| duplicate UUID | another mounted ark drive has the same UUID | second one → `unusable`, `LAI-ARK-006` (a cloned drive: give it a new UUID with `tune2fs -U random`) |
| read-only | mount options contain `ro` (per-mount) or super options contain `ro` | `read_only`, `LAI-ARK-003`. Served and importable, never written |
| errors | `/sys/fs/ext4/<dev-name>/errors_count` > 0 (dev-name is the basename of the source, e.g. `sdb1`; for dm devices resolve `/sys/dev/block/<maj:min>/dm/name`) | `unclean`, `LAI-ARK-004`. Treated as **read-only** until fixed; served, but every item is re-verified before it is served (§4.13) |
| initialized | `<mount>/localai/ROOT.json` exists | missing and `<mount>/localai` absent or empty → `uninitialized` (`LAI-ARK-010`). The UI offers **Set up this drive** (`aictl ark init`), which creates `localai/` and `ROOT.json`. An uninitialized drive is never written without that explicit action |
| writable | create and remove `<mount>/localai/.staging/.probe` | failure → `read_only` with `LAI-ARK-003` (detail: errno) |

**Ordering.** Drives are ordered by mount time (mountinfo mount id ascending), then root id. This order is the "best copy" order (§4.2).

**Mounted state.** For a `ready` drive, the agent emits `drive.mounted`, adds the root to the active set, loads its manifests (no hashing), and computes READY for it. Serving from it starts at once: models on the drive become loadable, and ZIMs join the merged Kiwix library ([companion-services.md](companion-services.md) §kiwix-serve).

#### 4.1.3 Unclean filesystems
An ext4 filesystem mounted after an unplug without eject may have `errors_count` 0 but a dirty journal; the kernel replays the journal on mount, so that case is fine. `errors_count > 0` means real corruption was detected. The agent:
1. marks the root read-only (`unclean`),
2. raises `LAI-ARK-004` with the fix "Unmount the drive and run `sudo fsck.ext4 -f /dev/<device>`. Then plug it in again.",
3. never runs `fsck` itself (it needs the filesystem unmounted, and a non-expert must not be surprised by a long repair),
4. schedules a **serve-time verification**: before a model on that root is loaded or a ZIM is added to the library, its files are re-hashed. Items that fail are marked `quarantined` in memory (the drive is read-only, so nothing moves on disk).

#### 4.1.4 Surprise removal
When a drive root disappears from mountinfo without an eject:
1. Emit `drive.unmounted` with `safe_to_unplug: false`, and raise `LAI-ARK-007` (warning, auto-cleared after 24 h or the next clean mount).
2. Cancel any transfer that writes to that root. Its journal entries stay at their last checkpoint, so the next time the drive is mounted the transfer resumes. A `.part` file may be shorter than its checkpoint after power loss; §4.6 handles that.
3. Tell the router and the companion services (via the `drive.unmounted` event) to switch to other copies. A model loaded from the drive keeps running until it touches unmapped pages; the router may crash it, which [llama-router.md](llama-router.md) handles by reloading from the best remaining copy.

#### 4.1.5 Safe eject
`aictl ark eject <root-id>`, the desktop **Eject** button, or `POST /ark/drives/{root_id}/eject`:
1. Set the drive to `ejecting`. New writes to it are refused.
2. Cancel transfers to it (graceful, checkpointed), and wait up to 30 s for them to stop.
3. Ask the router to unload models whose files come from that root, unless another verified copy exists, in which case the router reloads from that copy. If a model with no other copy is loaded and `force` is not set, fail with `LAI-ARK-009` ("The brain *X* is running from this drive. Switch brain first, or eject anyway.").
4. Regenerate the merged Kiwix library without that root and wait for kiwix-serve to drop the files (`companion-services.md` restarts `localai-kiwix` if it still holds files open after 10 s, checked through `/proc/<pid>/fd`).
5. Regenerate `whisper.env` if the whisper model came from that root, and restart `localai-whisper`.
6. Check that no process holds files on the mount: walk `/proc/*/fd` and `/proc/*/cwd` for paths under the mount (as root). Blockers are listed by process name in the `LAI-ARK-009` details.
7. `syncfs(2)` on the mount, then `umount <mount>` (via `umount(2)`; on `EBUSY` retry 3 times, 2 s apart).
8. `udisksctl power-off --no-user-interaction -b <whole-disk device>` (best effort; some USB bridges don't support it).
9. Emit `drive.unmounted` with `safe_to_unplug: true`. The UI shows "You can unplug the drive now." / "Ya puede desconectar el disco."

### 4.2 Best copy lookup

Other components never walk directories. They call `ark.BestCopy(itemID) (Copy, error)`, which returns the root id, absolute file paths and the MANIFEST. Rules:
1. Consider only verified copies on mounted roots whose state is not `ejecting`.
2. Prefer the copy whose version equals the version recorded in the **internal** root's MANIFEST, if any. Then prefer the **newest version** (by the version's sortable key: date for kiwix and maps, sync time for others).
3. Among copies of the same version, prefer `internal`, then drives in mount order.
4. On an `unclean` root, a copy counts only after its serve-time verification has passed.
5. No copy → `ErrNotPresent`. The caller maps it to its own doctor code (for example `LAI-LLM-008` for a model).

`BestCopy` is cheap (in-memory index rebuilt after each item swap, mount or unmount) and safe for concurrent use.

How it is built (`agent/internal/ark/bestcopy.go`, `itemdir.go`):
- `ark.ItemDir` is the one function that maps an item to its directory (§3.1 table); the index scans only those directories (`docs`, `maps/*`, `models/*`, `speech/whisper/*`, `speech/piper/*`, `kiwix/*`, and the directories of `kit/` down to three levels), so `kit/config/`, `kit/MANIFEST.json` (the kit's own index), `.staging` and `.trash` are never items (R9). A manifest that sits in a directory `ItemDir` would not choose for its item is misplaced and counts as missing.
- A copy exists only if its MANIFEST is valid and every listed file is there with the recorded size (a directory item needs its `SHA256SUMS`). Nothing is hashed. Otherwise the item is left out of the index with `LAI-SYN-052`; a directory without a manifest is just not present (a half-swapped directory looks like this on purpose, §4.5.1).
- The index is an immutable snapshot swapped atomically. `BestCopy` reads memory only, so it never waits for a slow or unplugged drive. `Rebuild(roots)` (start, mount, unmount) reads the roots concurrently and is bounded by its context; `Refresh(root, dir)` re-reads one item directory after a swap; `SetRootState` excludes an `ejecting` root at once without reading it.
- Ties: the same sort key with different versions goes to the greater version string, so the order is total. "Drives in mount order" is the mountinfo mount id (`Root.MountID`), then the root id.
- Rule 4: `MarkServeVerified(root, item, version)` is the flag the verification layer sets once a copy on an `unclean` root has passed its serve-time check. It belongs to that exact version, survives rebuilds of the same copy, and is dropped when the root is unmounted or the version changes. An unverified copy on an unclean root is invisible, including as "the internal root's version" for rule 2. Hashing is not done here (§4.13).
- `Copies(item)` returns every servable copy, best first, for callers that need to see all of them (retention counting other verified copies, `local_copy` sourcing). A directory item's `Copy.Files` is empty; its content is the tree under `Copy.Dir`.

### 4.3 Run lifecycle

```
trigger ──► preflight ──► catalog refresh ──► plan (per root) ──► execute ──► reverify slice ──► READY + extras ──► report
              │                │                    │
              └─ skip ◄────────┴─ (offline: use     └─ dry-run: return the plan, stop
                 (reason)         the local catalog)
```

**Preflight** (in this order; the first failure ends the run as `skipped`; `aictl ark sync --scheduled` then prints the reason and exits `0`, [aictl.md](aictl.md) §3.4):
| Check | Scheduled | Manual / cloud |
|---|---|---|
| `sync.enabled` is false | skip `disabled` | proceed (a person asked) |
| mode is `survival` | skip `mode` (`LAI-SYN-002`) | refuse `LAI-SYN-010` |
| mode is `offline` | skip `mode` (`LAI-SYN-002`) | refuse `LAI-SYN-011`, unless `allow_offline_mode` |
| on battery (`/sys/class/power_supply/*/online` of type `Mains` is 0 and a battery exists) and not `allow_on_battery` | skip `battery` (`LAI-SYN-003`) | proceed with a warning |
| connectivity `none` (modes.md §4) | skip `offline` (`LAI-SYN-001`) | proceed; resolvers fail fast |
| no writable roots | skip `no_roots` | refuse `LAI-ARK-001` |

**Catalog refresh** follows [catalog.md](catalog.md) §1 (cloud → GitHub tag → local). A failed refresh is not fatal: the run continues with the current catalog and logs the catalog code (`LAI-CAT-002` when the new catalog is rejected; a fetch failure is logged with the next source tried).

**Execute** runs the plans of all roots through one scheduler (§4.7). **Reverify slice** spends up to `reverify_budget_minutes` re-hashing the items with the oldest `last_reverified_at` (§4.13). Then READY.json is written for every root that was touched, the drive extras are written (§4.15), and the run is reported (§4.16).

A **scheduled** run that is still active after `max_run_hours` is cancelled gracefully and ends `done` with the remaining items `skipped: time_limit`. They resume the next night.

### 4.4 Planner

The planner is a pure function, `plan.Plan(in PlanInput) RootPlan`, with no I/O and no clock (it reads `in.Now`). Everything it needs is gathered first (`plan.Gatherer`, the only part that touches the disk, behind interfaces), which keeps it table-testable. Code: `agent/internal/sync/plan/` (`plan.go` and `types.go` pure, `gather.go`, `dryrun.go`, `render.go`).

```go
type PlanInput struct {
    Root       RootInfo                    // id, kind, path, profile id, fs total/free bytes, read_only
    Catalog    *catalog.Catalog
    Profile    *catalog.Profile
    Overrides  *ProfileOverrides           // = catalog.Overrides; internal root: node.yaml profile_overrides; drive root: nil
    Node       NodeInfo                    // backend, vram, ram, arch, codename (for kit item filtering)
    Present    map[string]manifest.Manifest // verified copies on this root, by item id
    Partial    map[string]PartState        // journal: .part bytes and staged files, with the version they are for
    Trash      []TrashEntry                // per superseded version: size, trashed-at, eligible to purge
    Resolved   map[string]Resolved         // resolver output (version, sort key, size); absent = not resolved
    OtherRoots map[string][]RootCopy       // verified copies on the other roots, best first (local-copy sourcing)
    Mode       string                      // online | offline | survival (informational; offline and survival forbid the network)
    Offline    bool                        // the network may not be used (no connectivity, or an import)
    Now        time.Time
}
```

`plan.PlanAll(inputs)` plans several roots of one run and sequences the downloads between them (§4.4.3, "Several roots"). The gatherer fills `Resolved` from the resolvers when the node is online and from `plan.FromCatalog` (what the catalog itself pins) for every item no resolver answered; the resolver registry (`plan.Resolvers`, by catalog source type) is empty until SYNC-09 to SYNC-11 register theirs.

#### 4.4.1 Desired set
1. `desired = profile.include.models ++ profile.include.content` (in that order, keeping list order). This combined list is the **list position** used for tie-breaks, as catalog.md §4 requires.
2. Apply overrides: append `add_models` then `add_content` (in their order) if not already present; remove `remove_models` and `remove_content`. `budget_bytes` from overrides, if not null, replaces the profile budget.
3. **Kit filtering.** A `kit` item that is specific to a codename or arch (an `apt_closure` source, for example `kit-debs-resolute-amd64`) is kept on an internal root only if it matches this node's codename and arch ([rebuild-kit.md](rebuild-kit.md) §3.2); an unknown node value filters nothing. Drive roots keep all of them. A filtered item has no row: its id is listed in the plan's `not_for_node`, and a verified copy of it that is present becomes an orphan.
4. Unknown ids (not in the catalog, in the profile or in `add_*`) are dropped with `LAI-CAT-004` (a removal of an unknown id is a no-op).
5. Models that can't run on this node (`requirements` vs `Node`) are **kept** (catalog.md §5: stored, marked `loadable: false`).

#### 4.4.2 Priority order
Sort the desired set by:
1. **Pinned class first:** items of kind `kit` and `docs` are always tier 1 and are planned before everything else. They are small and are what makes rebuilding possible. They are **never** dropped for budget. If they don't fit, the run reports `LAI-SYN-042` (red).
2. `survival_tier` ascending (1, 2, 3). Items without a tier count as tier 2.
3. Items in `profile.survival.models` before other models of the same tier.
4. List position ascending.

Dropping for budget happens in **reverse** of this order: the last item is dropped first. (`catalog.EffectivePlan` of catalog.md §4, the estimate behind `GET /profile`, drops from the end until the total fits and counts a missing tier as 3; this planner decides item by item, first fit, as §4.4.3 says, and counts a missing tier as 2.)

#### 4.4.3 Sizing and budget
For each item, in priority order, compute:
- `final_bytes`: the size of the target version. From the resolver if resolved (exact sizes from `Content-Length`, LFS size or the release asset), else from the catalog (`sum(files.size_bytes)` or `expected_size_bytes`), else from the present copy.
- `present_bytes`: the size of the verified copy on this root, if any (0 otherwise).
- `need_bytes`: bytes still to write = `final_bytes − partial bytes already in .staging for that version` (0 if the target version is the present one).

Two separate limits apply:
- **Budget** (`budget_bytes`): limits the **steady state**, meaning the sum of `final_bytes` over the planned items. Trash, quarantine and staging do not count. Running sum `used`, in priority order.
- **Free space** (`min_free_bytes`): limits the **transient** state. A download may start only if `fs_free − need_bytes ≥ min_free_bytes` at that moment. It is re-checked while downloading (§4.8).

The per-item decision (`run.decide`), in priority order. `refresh due` and `pinned` are defined below the block:
```
if item is present and verified at the target version:            action = keep
elif item present at older version and refresh not due:            action = keep (reason refresh_days)
elif target unresolved (offline, resolver failed):
      present ? keep (reason unresolved) : skip (reason unresolved)
elif no local copy of the target exists and the network is not allowed:   (same as unresolved)
elif root is read-only:                                            action = skip (reason read_only)
elif not kit/docs and used + final_bytes > budget:
      if present: keep the present version (reason budget, update skipped) ; used += present_bytes
      else:       skip (reason budget)
elif fs_free - need_bytes < min_free even after purging eligible trash:  action = skip (reason disk)
else:
      action = present ? update : download ; used += final_bytes
      source = local_copy if OtherRoots has a verified copy of the target version, else network
```
Every `keep` and every `skip` of an item that is present adds `present_bytes` to `used` (the old copy stays and weighs in the steady state). The decision is first fit in priority order: a smaller lower-priority item can still fit after a larger one was skipped, but a higher-priority item is always considered first. A read-only root turns every action that is not `keep` into `skip (read_only)`.

"Refresh due" means: the target version differs from the present one, **and** either the item is a model or **pinned** (reason `catalog_pin`: a catalog change is a deliberate decision, so it is always applied), or it has `refresh_days` and the present copy's `downloaded_at` is older than `refresh_days` (reason `refresh_due`). An item without `refresh_days` that is not pinned is never refreshed. **Pinned** content is content whose source names its version in the catalog: a pinned kiwix file, a `url` that is not `version_policy: latest`, a release tag, a git mirror, the rendered docs. The sources that follow "latest" (kiwix latest, the planet extract, the apt closure) are not pinned and are refreshed by `refresh_days`.

**Free space and trash.** The disk check runs per item against a simulated free space that shrinks by `need_bytes` for every download or update planned so far (old versions stay in `.trash` until the new one verifies). When `fs_free − need_bytes < min_free_bytes`, **eligible** trash (§4.10 R3: the live replacement is verified) is used first, oldest first, and only as much as is needed (`purge_bytes` on the row); if it still does not fit, or the trash is ineligible, the item is `skip (disk)` with `LAI-SYN-040` (a present copy is kept). Smaller items may still fit afterwards. The planner never purges anything; the engine does, through `retention.CanDelete`, which re-checks R1 at that moment.

**Sizing and sources.** `final_bytes` of a keep is the size on disk; of a skip, the target size (0 when nothing knows it). `need_bytes` subtracts the `.part` and staged bytes only when the journal's version is the target version. `Resolved` is taken as given; the gatherer fills it from `plan.FromCatalog` for what the catalog pins (a model's revision `[:12]`, a pinned kiwix file's `YYYY-MM`, a pinned url's sha256 `[:12]`, a release tag, `catalog-<catalog version>` for the docs) and leaves the rest unresolved. A resolver that exists and fails leaves the item unresolved (it is not replaced by the catalog pin). Local copies work in every mode; for an import (§4.14) the caller sets `Offline`, fills `Resolved` from the drive's MANIFESTs and `OtherRoots` with the drive's copies, so every download is a local copy and an item the drive lacks is `skip (unresolved)`.

**Limits of a root.** Internal: `budget_bytes` and `min_free_bytes` of the profile, the budget replaced by a positive `profile_overrides.budget_bytes`. Drive: `min_free` is 2 % of the filesystem size (at least 5 GB) and the budget is the filesystem size less that; the profile's figures are not used.

**Several roots.** `PlanAll` makes the first root (in the order given) that is to fetch a version from the network the only one that does; every later root that would fetch the same version gets `source: local_copy`, `from_root` and `after` set to that root, so nothing is downloaded twice and the copy waits for the first download. Each plan is still valid read on its own.

**Orphans.** A verified copy on this root that is not in the desired set gets `action = orphan` (rows after the desired items, by id, no position). It is kept, counted in READY as `orphan`, and reported with `LAI-SYN-051` if orphans total more than 5 % of the budget (strictly more). Only `aictl ark prune --orphans` deletes them (§4.10). The planner has no delete action at all: nothing in a plan removes a verified copy.

**Survival-model guard.** If a model in `profile.survival.models` ends up `skip: budget`, the plan is still valid, but READY is red for that root and `LAI-SYN-041` is raised as an **error** with the item named (the other items skipped for budget are one `LAI-SYN-041` warning). This is a profile bug; the profile owner must fix the budget. On a drive root the other budget skips raise `LAI-ARK-011` (info) instead of the warning.

**Findings.** The plan carries the doctor codes it raises, in this order: `LAI-CAT-004` (dropped unknown ids), `LAI-SYN-042` (error: a kit or docs item crossed the budget or could not start for disk), `LAI-SYN-041`, `LAI-SYN-040` (items skipped for disk), `LAI-SYN-051`, `LAI-ARK-011`. The human text prints each with its fix.

#### 4.4.4 Dry-run output
`aictl ark sync --dry-run` (human form; golden files in `agent/internal/sync/plan/testdata/golden/`, English and Spanish, produced by `plan.Render(plan, lang)` and reproducible from the JSON alone):
```
Plan for root "internal" (/srv/localai) · profile small-8gb · catalog 2026.11.02-1
Budget 80.0 GB · planned 68.7 GB · free now 290.0 GB · keep free ≥ 25.0 GB

  #  ITEM                         TIER  ACTION    VERSION              SIZE      NOTE
  1  kit-llama-cpp                k     keep      tag-kit-llama-cpp    0.5 GB
  2  kit-debs-resolute-amd64      k     update    closure-b            3.1 GB    refresh due (31 d); was closure-a; old copy kept in .trash
  3  docs-bundle                  k     download  catalog-2026.11.02-1 0.1 GB
  5  model-beta-vision            1     download  rev-beta-0002        5.0 GB
  7  model-gamma-tiny             2     download  rev-gamma-0003       1.0 GB    from ark-3f9c0a12 (local copy)
  9  books-alpha-en               3     skip      -                    30.0 GB   unresolved
 10  maps-alpha                   3     skip      20261001             12.0 GB   budget
 --  osm-north-america            -     orphan    20260501             11.2 GB   not in profile; run `aictl ark prune --orphans`

Notes:
  [warning] LAI-SYN-041  Items skipped because the budget is full (a survival model, if named): maps-alpha
    Fix: Raise `budget_bytes` or remove other content in `node.yaml`.

Downloads: 6 items, 59.2 GB from the internet, 1.0 GB local copy.
Peak disk use during sync: 177.9 GB (free will not drop below 229.8 GB).
Nothing was changed (dry run).
```
(abridged; the golden file lists every row.) Sizes are GB of 10^9 bytes with one decimal, rounded half up. Columns are as wide as their longest cell (minimums 27, 4, 8 and 20 for item, tier, action and version), two spaces apart except one between VERSION and SIZE; a line has no trailing spaces. The `Notes:` block appears only when there is something to say: no network used, read-only root, kit items left out for this node, and every doctor code the plan raises with its fix. A skipped row shows the target version and size when known, `-` otherwise. The note says why: `budget`, `unresolved (offline)`, `not enough free disk space`, `read-only root`, `refresh due (N d)`, `catalog pin changed`, `from <root> (local copy[, after it finishes])`, `frees N GB of old versions in .trash`, and for a keep `newer version available; refresh in N d`, `kept; newest version unknown`, `update skipped: budget`.

`--json` (and the body of `POST /ark/sync` with `dry_run`, an array of these, one per root) returns:
```json
{
  "root": "internal",
  "path": "/srv/localai",
  "profile": "small-8gb",
  "catalog_version": "2026.11.02-1",
  "mode": "online",
  "budget_bytes": 80000000000,
  "planned_bytes": 68700000000,
  "fs_free_bytes": 290000000000,
  "min_free_bytes": 25000000000,
  "peak_bytes": 177900000000,
  "free_floor_bytes": 229800000000,
  "trash_bytes": 47000000000,
  "items": [
    {"pos": 2, "id": "kit-debs-resolute-amd64", "kind": "kit", "tier": "k", "action": "update",
     "from_version": "closure-a", "to_version": "closure-b", "final_bytes": 3100000000,
     "need_bytes": 3100000000, "source": "network", "reason": "refresh_due", "age_days": 31, "refresh_days": 30},
    {"pos": 10, "id": "maps-alpha", "kind": "kiwix", "tier": 3, "action": "skip",
     "to_version": "20261001", "final_bytes": 12000000000, "reason": "budget"}
  ],
  "findings": [{"code": "LAI-SYN-041", "severity": "warning", "items": ["maps-alpha"]}]
}
```
`tier` is `"k"` for the pinned kit/docs class and absent for an orphan. `action` ∈ `keep | download | update | skip | orphan`. `reason` ∈ `refresh_days | refresh_due | budget | disk | unresolved | time_limit | read_only | backend | catalog_pin` (`time_limit` is the run's, `backend` the profile's; the planner produces the rest). `source` ∈ `network | local_copy`, with `from_root` (the root a local copy is taken from) and `after` (set when that root is itself being synced in the same run). Keys added to the first draft of this shape: `path`, `mode`, `offline` and `read_only` (omitted when false), `free_floor_bytes` (the lowest free space the run reaches after an item starts; the free space itself when nothing starts), `trash_bytes` (everything in `.trash`, eligible or not), `not_for_node` (kit ids filtered out), `findings` (always present), and per item `age_days`, `refresh_days` and `purge_bytes`. **Peak** is what the root holds now (verified copies including orphans, the trash, staged bytes) plus every `need_bytes` still to write; old versions stay in `.trash` until the new one verifies, so nothing is subtracted. The JSON is deterministic: the same input gives the same bytes. A dry run resolves items over the network when connectivity allows (HEAD/metadata only, no body downloads, at most 8 at a time, 5 s each); offline it plans with what the catalog pins and marks the rest unresolved.

### 4.5 Item state machine

One state machine instance per (root, item, target version). States are persisted in `items.json` and every transition is appended to the run journal **before** the side effect it announces (write-ahead).

```
                ┌────────────────────────── cancel / crash: stays, resumes later ───────────────┐
                ▼                                                                               │
 planned ──► resolving ──► downloading ──► verifying ──► staged ──► swapping ──► swapped ──► verified
    │            │              │  ▲            │                      │             │
    │            │              │  └─ retry ────┘ (re-read mismatch     │             └─ post-swap check
    │            │              │      after a resumed download:       │                fails → failed
    │            │              │      restart once from 0)            │
    │            ▼              ▼                ▼                      ▼
    │         failed ◄──── failed (permanent) quarantined ◄── sha256 mismatch
    │            │
    └──► skipped (budget | disk | unresolved | time_limit | read_only)
```

| State | Entry action | Exit condition |
|---|---|---|
| `planned` | — | scheduler picks it |
| `resolving` | resolver fetches metadata (§4.9) | version + files + expected sha256 known → `downloading`; or permanent error → `failed` |
| `downloading` | per file: open `<file>.part` (create or append), HTTP GET with `Range` (§4.6), stream to disk and to sha256 | all files complete and streaming digest = expected → `verifying`; streaming digest ≠ expected → `quarantined`; permanent HTTP error → `failed`; retries exhausted → `failed` (retryable next run); ENOSPC → §4.8 |
| `verifying` | `fsync` each file, `posix_fadvise(DONTNEED)`, then re-read and hash (if `verify_reread`) | all match → rename `<file>.part` → `<file>` inside staging → `staged`; mismatch → `quarantined` |
| `staged` | — | swapper picks it (immediately, unless the item is in use in a way that blocks swap, see below) |
| `swapping` | journal records the list of planned renames; executes them one by one (§4.5.1) | all renames done → write `MANIFEST.json` (tmp + fsync + rename + fsync dir) → `swapped` |
| `swapped` | `stat` every file: size equals expected and inode equals the staged inode | ok → `verified`; else `failed` with `LAI-SYN-026` |
| `verified` | emit `sync.item_done`, update BestCopy index | terminal |
| `quarantined` | move offending files to `.staging/quarantine/<item>/<ts>/` with `reason.json` (expected vs actual sha, URL, bytes, resolver) | terminal for this run. Next run starts the item from scratch. 2 quarantines in a row for the same version → `LAI-SYN-020` becomes red and the version is blocked until the catalog changes |
| `failed` | record `code`, `attempts`, `last_error` | terminal for this run; `.part` kept if the error is retryable |
| `skipped` | record reason | terminal |

**Content validators** (extension point of `verifying`). After the hashes match (or, for `checksum: computed`, after the hash is computed), the state machine calls every validator registered for the item: `sync.RegisterValidator(match func(catalog.Item) bool, v func(ctx, stagedDir string, item catalog.Item) error)`. Validators read only the staged files. An error is a verification failure: the item goes to `quarantined` with `reason.json` `{"validator": "<name>", "error": "…"}` and the validator's code. Registered validators: `pmtiles verify` for maps extracts (§4.9.3, `LAI-SYN-023`), the ZIM internal checksum (§4.9.2, `LAI-SYN-028`) and the GeoNames place-index plausibility check for `places-*` items ([companion-services.md](companion-services.md) §4.4.4). This is how `trust: tofu` items get a content check in place of a pinned hash.

**Local-copy source** (planner `source = local_copy`, and import): `downloading` copies from the other root's verified files instead of HTTP, with the same `.part`, checkpoint and streaming hash logic. The expected sha256 is the catalog value (or, for computed checksums, the source root's MANIFEST value).

**Swap blocking.** Renaming a file that a process holds open is safe on Linux (the open file keeps its inode). So swaps never wait for the router or kiwix. After the swap, `sync.item_done` tells them to pick up the new version (router: at the next load; kiwix: library regenerated).

#### 4.5.1 Swap procedure (atomic per file, committed by MANIFEST)
For an item with target dir `D` and staging dir `S`:
1. Journal: `{"ev":"swap_plan","renames":[["S/a.gguf","D/a.gguf"],...],"trash":[["D/old.gguf",".trash/<id>/<oldver>/old.gguf"]]}`.
2. `mkdir -p D` and `.trash/<id>/<oldver>/`.
3. If `D/MANIFEST.json` exists, rename it first to `.trash/<id>/<oldver>/MANIFEST.json`. From this moment readers see no manifest, so the item counts as not present on this root. This is deliberate: a half-swapped directory must never look verified.
4. For each old file that is not in the new version, or that the new version replaces: `rename(D/f, .trash/<id>/<oldver>/f)`.
5. For each new file: `rename(S/f, D/f)`.
6. For directory items (`docs/`, `maps/assets/`, kit directories with many files): `renameat2(S/tree, D, RENAME_EXCHANGE)` when `D` exists, then rename the exchanged old tree into `.trash`. This makes the directory switch atomic.
7. Write the new `D/MANIFEST.json` (tmp, fsync, rename, fsync `D`). **This is the commit point.**
8. Journal `{"ev":"swap_done"}`.

Crash recovery for `swapping`: replay the planned renames, skipping any whose source no longer exists and whose destination exists with the expected size; then do steps 7–8. If a destination is missing and its source is missing too, the item goes to `failed` (`LAI-SYN-026`), and the previous version is restored from `.trash` (§4.10 rule R4).

#### 4.5.2 Journal files (`agent/internal/sync/journal`)
- **`items.json`**: `{"schema_version": 1, "updated_at": "…", "items": {"<root_id>/<item_id>": {…}}}`, keys sorted. A record has `state`, `version` (the target), `attempts`, `last_error` and `code` (a doctor code), `partial_bytes`, `checkpoints` (per file: `offset`, `sha_state`, `etag`, `last_modified`, `url`; headers are never stored), `consecutive_failed_runs`, `updated_at` and the open bag `extras` (`extras.inventory` for SYNC-14). Unknown keys are kept on rewrite. Every change is written through at once: the previous good file is copied to `items.json.bak` (best effort, and a damaged file never replaces a good backup), then the new content replaces `items.json` atomically.
- **Open** (agent start): leftover temp files are swept; a damaged or foreign-schema `items.json` is renamed to `items.json.damaged-<UTC time>` (never deleted); then `items.json.bak` is tried, and when that is unusable too the records are rebuilt from the MANIFESTs of the mounted roots. A restored or rebuilt state is reconciled with the manifests, written out, and reported as `LAI-SYN-050`. A node with no file, no backup and no manifest is simply new and raises nothing. A read error (as opposed to damage) fails the open without touching the file. The backup is one write behind by design, so reconcile makes the manifests win: the manifest is the commit point of a swap.
- **Reconcile** (also callable by the engine): a manifest with no record gets a `verified` one; a record for the manifest's version that is not yet `verified` becomes `verified` (the journal was a step behind a swap that committed); a `verified` record whose version differs takes the manifest's; a `verified` record on a scanned root that has no manifest any more is dropped. Records in other states, and records of roots that were not scanned (an unplugged drive), are left alone.
- **Run logs** `runs/<run-id>.jsonl`, run id `run-YYYYMMDDTHHMMSSZ-<4 hex>`: one compact JSON line per event, `O_APPEND`, `fsync` before `Append` returns, the newest 60 files kept (the only deletion the journal does, and only of its own logs). Every event has `ev` and `at`; `state`, `ckpt`, `swap_plan` and `swap_done` also carry `root`, `item` and `version` so that a replay knows whose they are. `ckpt` events are written every 64 MiB.
- **Write-ahead**: `Append` returns nil only after the line is on disk, and the caller performs the side effect after that, never before. If `Append` fails the caller must not perform the side effect; the line may still be on disk (written, fsync failed), which replay tolerates. A failed append never swallows the next one: the next line starts on a fresh line, and a torn line is skipped on read.
- **Replay**: on start, after `items.json` is open and before the new run starts, the newest run log is replayed (`Replay`): per item, the last `state` event's `to` is where the item was going, the last `ckpt` per file is the resume point, and a `swap_plan` without a later `swap_done` is a swap to finish (§4.5.1). The intents are written into `items.json` (`ApplyIntents`; a record that is `verified` for its version is left alone, since `verified` is terminal and the manifest decided it). The engine resumes each non-terminal intent after checking the disk, because an intended state may be one whose side effect never happened. Replaying twice is harmless.
- **Unknown staging**: `.staging/<item-id>/<version>` with no record for that item at that version in a state other than `verified` (and not `.staging/quarantine`) is junk that was never verified. The journal lists such paths (`UnknownStaging`); deleting them is retention's job (§4.10).

### 4.6 Download and resume

The HTTP client:
- Go `net/http`, HTTP/1.1 and HTTP/2, TLS 1.2+ with system roots (`/etc/ssl/certs/ca-certificates.crt`).
- `User-Agent: localai-agent/<version> (+https://github.com/marinoscar/local-ai)`.
- Timeouts: dial 15 s, TLS handshake 15 s, response headers 30 s. **Stall timeout:** if no body bytes arrive for 60 s, the request is aborted and retried.
- Redirects are followed (max 10). The `Authorization` header (HF token) is sent **only** to `huggingface.co` and is dropped on any cross-host redirect (Go's default behaviour; covered by a test).
- Proxy: none. The agent unit sets no proxy environment ([agent.md](agent.md) §3.6), so downloads go direct; supporting an outbound proxy would be a change to agent.md first.

Per file:
1. If `<file>.part` exists and the journal has a checkpoint `{offset, sha_state, etag, last_modified, url}` for it:
   - truncate the `.part` to `offset` (it may be longer than the checkpoint after a crash; it may also be **shorter** after power loss, in which case restart the file from 0),
   - restore the sha256 state from `sha_state` (Go's `crypto/sha256` digest implements `encoding.BinaryMarshaler`; the marshalled state is base64 in the journal).
2. Send `GET` with `Range: bytes=<offset>-` and `If-Range: <etag or last_modified>` when resuming.
3. Response handling:
   | Status | Meaning | Action |
   |---|---|---|
   | `206` with matching `Content-Range` | resume works | append |
   | `200` while resuming | the server ignored Range, or `If-Range` failed because the file changed | truncate to 0, reset the hash, log `LAI-SYN-032` (info), continue from 0 |
   | `416` | offset ≥ size | if offset == expected size, treat as complete; else truncate to 0 and retry |
   | `401`, `403` | auth | HF: `LAI-SYN-034`; others: permanent `LAI-SYN-030` |
   | `404`, `410` | gone | permanent `LAI-SYN-030`. For kiwix "latest", re-resolve once (the file may have been replaced by a newer date) |
   | `408`, `429`, `5xx`, network errors, stalls | transient | retry (§4.8) |
4. Stream through a 1 MiB buffer: write to `.part`, update the hash, update the token bucket.
5. Every 64 MiB, and on cancel: `fdatasync` the `.part`, marshal the hash state, and append a checkpoint to the journal (`{"ev":"ckpt","file":...,"offset":...,"sha_state":"..."}`) and update `items.json`. A checkpoint is also written whenever a download stops with a `.part` on disk (a transient failure, a disk skip, the end of the file), so a retry or the next run resumes at the exact offset and not up to 64 MiB earlier.
6. At EOF: compare the byte count to the expected size (`LAI-SYN-027` if different, which counts as a mismatch → quarantine) and the digest to the expected sha256.

#### 4.6.1 Library contract (`agent/internal/sync/download`)

The downloader is a library that the engine (SYNC-07) and the resolvers call; it owns no state machine. `Downloader.Fetch(ctx, FileJob, Checkpointer) (Result, error)` downloads one file into `<root>/.staging/<item>/<version>/<file>.part` and returns it **verified** (size and sha256), or an `*Error`. The HTTP client, clock, filesystem, free-space probe, bucket and trash purger are injected (`Options`); a `Source` replaces HTTP for local copies (§4.5) and tests.

- **Checkpoints.** `Checkpointer` is `Load`/`Save`/`Clear` per file. `JournalCheckpointer` is the journal's: `Save` appends the `ckpt` event first (write-ahead) and then writes `Item.Checkpoints` and `PartialBytes` (the sum of the offsets) through `Items.Update`; the record must exist at the job's version (the engine journals `downloading` first). A checkpoint stores the URL **without query or fragment**, the ETag and Last-Modified, and never a header. A failed `Save` is logged and the download goes on; the previous point stays valid.
- **When a checkpoint is not trusted** the file restarts from 0 (no `LAI-SYN-032`, that code is only for a server that ignores Range): the `.part` is shorter than the offset, the URL differs, the offset is beyond the expected size, or the marshalled hash state does not cover exactly `offset` bytes. A `.part` with no checkpoint is truncated and restarted. A checkpoint at the expected size needs no request: the digest is checked at once.
- **Responses.** `206` must start at the requested offset and keep the saved ETag, else the file restarts from 0 immediately (at most 3 times per call, then it counts as a transient failure). `416` completes the file when the offset equals the expected size or the size in `Content-Range: bytes */N`, else restarts from 0. A server that announces another size than expected, or sends more bytes than expected, is a mismatch (`LAI-SYN-027`) before the bytes are written. Requests carry `Accept-Encoding: identity`, because offsets and sizes count wire bytes.
- **Errors** (`Error.Kind`): `mismatch` (size or digest; the `.part` is left for the engine to quarantine and its checkpoint is cleared; `LAI-SYN-020`, `-027`, or `-070` for a local copy), `permanent` (`LAI-SYN-030`, or `-034` for 401/403 from Hugging Face; also a bad TLS certificate or a redirect to a non-https URL, which are never retried), `exhausted` (`LAI-SYN-031`, or `-034` after a Hugging Face rate limit that never lifted; `.part` and checkpoint kept), `disk` (`LAI-SYN-040`, `Result.Skipped = "disk"`; kept), `root_fault` (below). A cancelled context returns `ctx.Err()` after a checkpoint, once the data already read (at most one 1 MiB block) is written.
- **Retry-After** replaces the computed backoff for that retry (a longer value than 15 minutes fails the file at once with `LAI-SYN-031`, without waiting). It does not extend the six retries.
- **Disk.** `EnsureSpace` is the pre-start hook (`fs_free − need ≥ min_free`, else `SpaceMaker.Purge` for the amount short, else `LAI-SYN-040`); `Fetch` repeats it for the rest of each file at every attempt and every 256 MiB. On `ENOSPC`/`EDQUOT` the partial block is truncated away, the file is checkpointed at the last good offset, the trash purged, and the write tried once more; if there is still no room the file is skipped. `Result.PurgedBytes` tells the caller to raise `LAI-SYN-043`.
- **Root faults.** `EIO` raises `LAI-ARK-014` and `EROFS` `LAI-ARK-003`; both stop all further writes to that root (every `Fetch` on it returns the fault at once) until `ClearRootFault`, and call `Options.OnRootFault(rootID, err, readOnly)` once so the caller re-runs the drive checks. A permission error on the root is `LAI-ARK-001` and does not latch.
- **Never deletes.** The downloader's filesystem interface has no remove; it truncates its own `.part`, and `Commit` (or `FileJob.Commit`) renames a verified `.part` to its final name inside staging and clears its checkpoint. The engine still owns the re-read, the quarantine and the swap.
- **Local copy.** `LocalSource` reads a verified file of another root through the same `.part`, checkpoint and hash code, bypasses the bucket (§4.7), never writes to the source, and reports a missing, unreadable or non-matching source as `LAI-SYN-070`.

### 4.7 Scheduler, concurrency and bandwidth

- One run at a time per node (`LAI-SYN-005`), guarded in memory and by `flock` on `/var/lib/localai/sync/lock`.
- **Network downloads:** at most `sync.concurrency` (default 2) at once over all roots.
- **Writers per root:** at most `sync.per_root_writers` (default 1) items in `downloading`, `verifying` or `swapping` per root. A USB drive gets one sequential writer, which is fastest for spinning and cheap SSD drives.
- **Same item for several roots:** download from the network once, to the root with the fastest disk (internal), then copy locally to the others (`source = local_copy`, after the first copy is verified). The planner marks the dependent copies `after: <root>`.
- **Order:** items are started in planner priority order across roots, interleaved round-robin between roots, so a big tier-1 ZIM for the drive doesn't starve the internal disk's tier-1 model.
- **Subprocess resolvers** (`pmtiles extract`, `git`, `apt-get update`) count as one network download slot each. At most one `pmtiles extract` runs at a time.
- **Bandwidth limit** (`sync.bandwidth_limit`): one token bucket (`golang.org/x/time/rate` is not used; a 40-line in-house bucket keeps dependencies minimal) shared by every in-process download, with a burst of 1 MiB. Subprocesses (`pmtiles`, `git`) are throttled through a **throttling proxy**: the agent starts an HTTP CONNECT proxy on `127.0.0.1:0` (an ephemeral port, alive only for that subprocess) that draws from the same bucket, and passes `HTTPS_PROXY=http://127.0.0.1:<port>` to the child (for git: `-c http.proxy=…`). With an unlimited bandwidth setting no proxy is started.
- **Limit syntax** (`throttle.ParseLimit`): `0` is unlimited; otherwise a number and a unit per second: `B`, `KB`, `MB`, `GB` (decimal), `KiB`, `MiB`, `GiB` (binary), `kbit`, `Mbit`, `Gbit` (decimal bits, so `100Mbit/s` is 12.5 MB/s). A unit ending in a capital `B` is bytes and one ending in `bit` is bits, so `20mb/s` is refused as ambiguous. The bucket takes its tokens after each block is written; local copies bypass it. A request larger than the burst is taken in burst-sized pieces, and the rate can change at run time (a config reload).
- **Proxy rules** (`throttle.StartProxy`): only `CONNECT <host>:443` where the host is in the allowlist; any other method, port or host is `403` and is never dialled; at most 64 tunnels; the bucket is drawn on the target-to-client direction. With an unlimited bucket `StartProxy` returns an empty address and a no-op stop, and the child runs direct. It stops with its context or its stop function, closing every tunnel.
- Progress events are rate-limited to 1/s/item.

### 4.8 Retries, backoff and disk full

**Retries for transient errors**, per file and per run:
- Backoff: 5 s, 15 s, 45 s, 2 min, 5 min, 10 min (6 retries, so 7 attempts), each with ±20 % jitter. `Retry-After` on `429`/`503` is honoured if it is ≤ 15 min; a longer value marks the item `failed` for this run (`LAI-SYN-031`).
- Progress resets the counter: if a retry made ≥ 256 MiB of progress since the previous failure, the next backoff starts again at 5 s.
- After the retries are exhausted, the item is `failed` with `LAI-SYN-031`, the `.part` and checkpoint are kept, and the next run resumes it.
- An item that fails in **3 consecutive runs** raises `LAI-SYN-035` (warning; red if tier 1).

**Disk full and min_free:**
- Before starting an item: `fs_free − need_bytes ≥ min_free_bytes` must hold (internal: `profile.storage.min_free_bytes`; drive: 2 % of the filesystem size, minimum 5 GB). If it doesn't, first purge **eligible** trash (§4.10, oldest first) until it does. If it still doesn't, the item is `skipped: disk` with `LAI-SYN-040`, and the scheduler tries the next item that fits (smaller items may still fit).
- While downloading, every 256 MiB, re-check `fs_free ≥ min_free_bytes`. Other processes can fill the disk. If it is violated: checkpoint, pause the item, purge eligible trash, re-check; if it is still violated, the item is `skipped: disk` (the `.part` is kept).
- On `ENOSPC` or `EDQUOT` from `write`: checkpoint at the last good offset (truncate the `.part` to it), then follow the same steps. The run never deletes a `.part` to make space for another item, and never deletes a verified copy to make space.
- On `EIO` or `EROFS` on a drive root: stop all writes to that root, raise `LAI-ARK-014` (I/O errors) or re-run the drive checks (it was probably remounted read-only by `errors=remount-ro`).

### 4.9 Resolvers

A resolver turns a catalog item into a concrete version: `Resolve(ctx, item) (Resolved, error)` where
```go
type Resolved struct {
    Version   string            // short, sortable where possible
    SortKey   string            // e.g. "2026-10" or the RFC3339 sync time
    Files     []ResolvedFile    // name, url (or local producer), size, sha256 (or "" when computed), sha256_source
    Producer  string            // "http" | "pmtiles_extract" | "git" | "apt" | "render"
    Meta      map[string]string // recorded in MANIFEST.source
}
```
Resolvers are pure network readers; they never write into a root. Producers that are not simple HTTP downloads (pmtiles, git, apt, render) run inside `downloading` and write only into `.staging`.

#### 4.9.1 `huggingface` (models.yaml `source.type: huggingface`)
- URL per file: `https://huggingface.co/<repo>/resolve/<revision>/<path>` where `revision` is the pinned 40-hex commit (catalog.md requires it). Path segments are URL-escaped, slashes kept.
- Metadata: `HEAD` the resolve URL **without following redirects**. Huggingface answers `302` with:
  - `X-Linked-Etag: "<sha256>"` (the LFS oid, quoted) and `X-Linked-Size: <bytes>` for LFS files,
  - `X-Repo-Commit: <40 hex>`,
  - `Location:` the CDN URL.
  For a non-LFS small file (for example a JSON config), the answer is `200` with `ETag` = git blob sha1; then the expected sha256 is the catalog value only.
- Checks:
  - `X-Repo-Commit` must equal the pinned revision, else `LAI-SYN-021`.
  - `X-Linked-Etag` (without quotes) must equal the catalog `sha256`, else `LAI-SYN-021` (red: the catalog and the source disagree, which means a catalog bug or tampering). The item is not downloaded.
  - `X-Linked-Size` must equal the catalog `size_bytes`, else `LAI-SYN-021`.
- Fallback when headers are missing (proxy, API change): `GET https://huggingface.co/api/models/<repo>/tree/<revision>?recursive=1` and read `lfs.oid` and `lfs.size` for the path.
- Download: `GET` the resolve URL and follow the redirect. Range works on the CDN. `sha256_source: "catalog+hf_lfs"`.
- Version: `revision[:12]`. SortKey: the catalog version in which the revision first appeared (from MANIFEST history), else the sync time.
- Token: if `sync.hf_token_file` exists, send `Authorization: Bearer <token>` to `huggingface.co` only. A `401` without a token → `LAI-SYN-034` ("This model needs a Hugging Face login. Add a token to /etc/localai/hf-token.").

#### 4.9.2 `kiwix` (ark.yaml `source.type: kiwix`)
- `version_policy: pinned`: file and sha256 come from `pinned`. URL = `base_url + pinned.file`.
- `version_policy: latest`:
  1. `GET <base_url>` (an Apache/mirrorbrain directory listing). Parse every `href` matching `^<book>_(\d{4}-\d{2})\.zim$` (`book` from the catalog, regex-escaped). Ignore `.zim.torrent`, `.zim.meta4` and `.zim.sha256` entries.
  2. Pick the newest `YYYY-MM`. If none match → `LAI-SYN-033`.
  3. Checksum (`checksum: sidecar`): `GET <base_url><file>.sha256` from `download.kiwix.org` itself (never from a mirror; the request is sent with redirects disabled, and a redirect is an error). The body is `<64 hex>  <filename>`; the hex must be 64 lowercase hex chars and the filename must match. Else `LAI-SYN-022`.
  4. Size: `HEAD <base_url><file>` (redirects followed) → `Content-Length`.
- Download: `GET <base_url><file>`, following the mirror redirect. Mirrors support Range. If a mirror returns a wrong size or a `200` on Range twice, the next attempt adds `?mirrorlist`-free direct download by sending the request with `Accept: application/octet-stream` to `download.kiwix.org` and refusing redirects (slower, but deterministic).
- `refresh_days`: the planner keeps the present version if it is younger than `refresh_days` (§4.4.3).
- Version: `YYYY-MM`. SortKey: same. `sha256_source: "kiwix_sidecar"`.
- After the swap, the ZIM's own internal checksum can additionally be checked with `kiwix-manage`-free means: the agent reads the ZIM header and verifies the MD5 checksum stored at `checksumPos` (the ZIM format's built-in integrity check). This runs during `verifying` for kiwix items and is cheap because the file is being re-read anyway (`LAI-SYN-028` on mismatch).

#### 4.9.3 `pmtiles_extract` (ark.yaml `source.type: pmtiles_extract`)
- Tool: `/opt/localai/pmtiles/current/pmtiles` (go-pmtiles release binary, installed by the installer from the kit or GitHub, see [rebuild-kit.md](rebuild-kit.md) §Contents). Missing → `LAI-SYN-061`.
- Find the newest planet build: for `k = 0..30`, `date = (today UTC − k days)` as `YYYYMMDD`, `HEAD planet_url_pattern{date}` until a `200` arrives. If none → `LAI-SYN-033`. Version = that date.
- Size estimate (for planning): `pmtiles extract <planet_url> /dev/null --bbox=<minLon>,<minLat>,<maxLon>,<maxLat> --maxzoom=<z> --dry-run`, parse the "tiles to extract" byte total from stdout (fallback: `expected_size_bytes`).
- Produce: `pmtiles extract <planet_url> <staging>/<id>.pmtiles.part --bbox=… --maxzoom=<z> --download-threads=4 --overfetch=0.05` with the throttling proxy (§4.7). Timeout: 6 h. The CLI can't resume, so an interrupted extract restarts from scratch; the journal records `resumable: false`. Exit status ≠ 0 → retry per §4.8 (max 2 attempts per run).
- Then `pmtiles verify <staging>/<id>.pmtiles.part` must exit 0 (`LAI-SYN-023` otherwise → quarantine), and `pmtiles show --header-json` must report `tile_type: mvt` and a bounding box that contains the catalog bbox to within 0.01°.
- Checksum: `computed`. The sha256 is computed during `verifying` and recorded with `sha256_source: "computed_after_verify"`. Copies to other roots and imports are verified against that recorded value.

#### 4.9.4 `github_release` (ark.yaml `source.type: github_release`)
- `GET https://api.github.com/repos/<repo>/releases/tags/<tag>` (with `Accept: application/vnd.github+json`). Find the asset with `name == asset`. Missing → `LAI-SYN-033`.
- If the asset JSON has `digest: "sha256:<hex>"`, it must equal the catalog `sha256` (`LAI-SYN-021` otherwise).
- Download `browser_download_url`. Verify against the catalog `sha256`. `sha256_source: "catalog+github_digest"` or `"catalog"`.
- Archives: if `extract: true` is set on the item ([catalog.md](catalog.md) §3), the verified archive is extracted into `.staging/<id>/<ver>/tree/` and swapped as a directory item. `map-assets` uses this (the archive holds `fonts/` and `sprites/`). Extraction rejects absolute paths, `..`, symlinks pointing outside the tree, and device files.
- Rate limit: unauthenticated GitHub API allows 60 requests/hour; one run needs about 10. A `403` with `X-RateLimit-Remaining: 0` is transient (retry after `X-RateLimit-Reset`, capped at 15 min, else `failed`).
- Version: `tag`.

#### 4.9.5 `url` (generic)
- `url` and pinned `sha256` from the catalog. `HEAD` for size. Download and verify. `sha256_source: "catalog"`.
- An item may list several files (`source.files[]`, catalog.md §3); each is resolved and downloaded the same way.
- `checksum: sidecar` (`sidecar_url`): the expected sha256 comes from a `SHA256SUMS`-style file fetched over HTTPS from the origin (used by `kit-ubuntu-iso-<ver>`; its extra GPG check is in [rebuild-kit.md](rebuild-kit.md) §4.1.7). `sha256_source: "sidecar"`.
- `checksum: computed` with `version_policy: latest` is allowed only for items with `trust: tofu`, which catalog.md §3 allows only on `kind: maps` data items (the GeoNames place index of companion-services.md). Then `sha256_source: "computed_tofu"` and READY shows the item with `trust: tofu`. **Version** of such an item: `HEAD` every file, concatenate per file `<url>\n<ETag>\n<Last-Modified>\n<Content-Length>\n` in catalog order, and take the first 12 hex of the sha256 of that string. It is deterministic and changes when the origin changes any file; an unchanged version means "already current" (no download).
- Version of a pinned `url` item: the first 12 hex of its (first) `sha256`; `sidecar` items: the sidecar's file name or release string as given in the catalog.
- `extract: true` works exactly as for `github_release` (§4.9.4): the verified archive is unpacked into `.staging/<id>/<ver>/tree/` with the same path-safety rules and swapped as a directory item.

#### 4.9.6 `git_mirror` (ark.yaml `source.type: git_mirror`)
- Target: `kit/git/<name>.git` where `<name>` is the URL basename without `.git` (e.g. `kit/git/llama.cpp.git`). Producer: `/usr/bin/git` (missing → `LAI-SYN-061`).
- Environment: `GIT_TERMINAL_PROMPT=0`, `GIT_CONFIG_NOSYSTEM=1`, `HOME=/var/lib/localai/sync` and `-c protocol.file.allow=never -c core.hooksPath=/dev/null`.
- First time: `git clone --mirror --no-tags <url> <staging>/<name>.git`, then `git -C … fetch --tags`. Then directory-swap into `kit/git/`.
- Update: mirrors are append-mostly and big (llama.cpp ≈ 1 GB), so an update copies nothing. It fetches **in place**: `git -C kit/git/<name>.git remote update --prune`. Git's own ref updates are atomic per ref, and objects are written before refs, so a crash leaves a valid repository. The item's MANIFEST is updated after the fetch (commit point).
- Verify: `git -C … fsck --connectivity-only --no-dangling` exit 0, and for `also_tarball_of: pinned`, `git cat-file -e <commit>^{commit}` for the commit in `node.yaml: llama.commit` (and, on drive roots, for every `llama.commit` recorded in `kit/catalog/pins.json`, see [rebuild-kit.md](rebuild-kit.md) §Pins). Failure → `LAI-SYN-024`.
- Tarball: `git -C … archive --format=tar --prefix=<name>-<commit>/ <commit> | gzip -n -9 > <staging>/src/<name>-<commit>.tar.gz.part`. `gzip -n` keeps it reproducible. The sha256 is computed and recorded (`computed_after_verify`). The tarball is its own file in `kit/src/` and swapped like a file.
- Version: the commit of the mirror's `HEAD` after fetch, `[:12]`.

#### 4.9.7 `apt_closure` (ark.yaml `source.type: apt_closure`)
Goal: the full set of `.deb` files needed to install `packages` on a freshly installed Ubuntu `<codename>`/`<arch>`, without root, without touching the system's apt state, and without a chroot.

1. **Private apt tree** `A=/var/lib/localai/sync/apt/<codename>-<arch>`:
   ```
   A/etc/apt/sources.list.d/ubuntu.sources     deb822; generated
   A/etc/apt/sources.list.d/extra.sources      third-party repos the item lists (tailscale, NVIDIA CUDA), generated
   A/etc/apt/trusted.gpg.d/                    keyrings copied from /usr/share/keyrings/ubuntu-archive-keyring.gpg + the extra repos' keys (shipped inside the agent binary, see rebuild-kit.md §Keys)
   A/etc/apt/preferences.d/                    empty
   A/var/lib/apt/lists/                        downloaded indexes
   A/var/cache/apt/archives/partial/           unused (we download ourselves)
   A/var/lib/dpkg/status                       EMPTY file (pretend nothing is installed)
   ```
   `ubuntu.sources` for `amd64`:
   ```
   Types: deb
   URIs: http://archive.ubuntu.com/ubuntu
   Suites: <codename> <codename>-updates <codename>-security
   Components: main restricted universe multiverse
   Architectures: amd64
   Signed-By: A/etc/apt/trusted.gpg.d/ubuntu-archive-keyring.gpg
   ```
   For `arm64` the URI is `http://ports.ubuntu.com/ubuntu-ports`. (HTTP is fine: apt verifies the signed `InRelease` and every index hash.)
2. **Common options** `O`:
   ```
   -o Dir=A -o Dir::State::status=A/var/lib/dpkg/status
   -o Dir::Etc::sourcelist=/dev/null -o Dir::Etc::sourceparts=A/etc/apt/sources.list.d
   -o Dir::Etc::trustedparts=A/etc/apt/trusted.gpg.d -o Dir::Etc::preferencesparts=A/etc/apt/preferences.d
   -o APT::Architecture=<arch> -o APT::Architectures=<arch>
   -o Debug::NoLocking=1 -o APT::Sandbox::User=<invoking user>
   -o Acquire::Languages=none -o APT::Install-Recommends=false
   ```
   These run as an unprivileged user (the agent drops to `localai` for these subprocesses), which proves the "no root" property.
3. `apt-get O update` (timeout 15 min; throttled through the proxy with `-o Acquire::http::Proxy=…`). Failure → `LAI-SYN-060`.
4. `apt-get O --print-uris --yes install <packages…>`. Because the status file is empty, apt computes the **complete** closure from nothing, including base libraries. That's a superset of what a fresh install needs, which is what we want: it installs on any base system of that codename. Unsatisfiable → `LAI-SYN-060` with apt's message.
5. Parse each line `'<uri>' <filename> <size> SHA256:<hex>` (apt prints the strongest hash; only `SHA256:` is accepted, else `LAI-SYN-060`). These hashes come from the signed indexes, so they are trustworthy (`sha256_source: "apt_signed_index"`).
6. Version = the first 12 hex of the sha256 of the sorted `name=version` list. If it equals the present version, the item is `keep`.
7. Download each `.deb` with the normal downloader into `<staging>/pool/`. Unchanged debs (same filename and sha256 as in the present version) are hard-linked from the present tree instead of downloaded (same filesystem, so a hard link is safe and atomic).
8. Generate the local repository index without `dpkg-dev`: for each package, `apt-cache O show <name>=<version>` prints the full control stanza from the signed index. Rewrite `Filename:` to `pool/<filename>`, drop `Description-md5`, and concatenate into `Packages`; also write `Packages.gz` (gzip -n). Write `PACKAGES.lock` (sorted `name=version sha256`) and `README.txt` (how to use it, see [rebuild-kit.md](rebuild-kit.md) §Offline apt).
9. Directory-swap the tree into `kit/debs/<codename>-<arch>/`.

Third-party packages (tailscale, NVIDIA) come from the `extra.sources` entries. Each item lists them in its `extra_repos` field ([catalog.md](catalog.md) §3). Recommended packages are excluded to keep size down; the kit item's `packages` list must name what's needed.

#### 4.9.8 `repo_docs` (ark.yaml `source.type: repo_docs`)
- Source: the repo tree at tag `catalog-<CATALOG_VERSION>`, as `https://codeload.github.com/marinoscar/local-ai/tar.gz/refs/tags/catalog-<CATALOG_VERSION>`; if `kit/git/local-ai.git` exists on any root, `git archive` from the mirror instead (works offline).
- Checksum: `computed` (the tarball is not published with a hash). The trust anchor is HTTPS to GitHub plus the tag name. The tarball sha256 is recorded.
- Produce: extract `docs/` and `inventory.example.yaml` into staging, then run `aictl docs render --src <staging>/src/docs --out <staging>/tree --inventory <path>` ([aictl.md](aictl.md) §docs). `<path>` is `/etc/localai/inventory.local.yaml` if present, else `<mount>/inventory.local.yaml` of a mounted drive, else none (placeholders stay, and READY marks docs `yellow` with `LAI-SYN-062`).
- Also produce **`kit/catalog/`** from the same tarball, in the same item install: `catalog/models.yaml`, `catalog/ark.yaml`, `catalog/profiles/`, `CATALOG_VERSION`, plus `pins.json` (rebuild-kit.md §3.2). It is staged next to the docs tree and swapped together with it (one MANIFEST lists both); lint must pass on it first (`LAI-CAT-002` otherwise, and the old `kit/catalog/` is kept). This is the copy the agent falls back to at startup (`source: kit`, [catalog.md](catalog.md) §1).
- Verify: `tree/index.html` exists, and every runbook listed in `docs/README.md` has an HTML file, including the survival card HTML rendered from `docs/runbooks/00-survival-card.md` ([survival-portal.md](survival-portal.md) §4.12). A missing **HTML** file fails verification. A missing survival-card **PDF** does not: `aictl docs render` makes PDFs only where Chromium is installed (`LAI-CLI-014`), so the item verifies and READY shows docs `yellow` with `LAI-CLI-014`.
- Directory swap into `<root>/docs/` (and `<root>/kit/catalog/`).
- Version: `catalog-<CATALOG_VERSION>` plus `+inv` if real values were rendered.
- **Privacy:** rendered docs with real values are written only to local roots. They are never uploaded anywhere, and the cloud report includes only the version string.

### 4.10 Retention

Rules, enforced by one function `retention.CanDelete(path) (bool, reason)` that every deleting code path must call (tests assert there is no other `os.Remove`/`os.RemoveAll` in `internal/sync` and `internal/ark` outside it):

- **R1. Never delete the last verified copy.** A file that belongs to a verified copy of an item version may be deleted only if, after the deletion, at least one other verified copy of **the same item** (any version) exists on a mounted root **and** that copy has passed a re-verify within the last 24 h (or was verified within that time). `.trash` entries count as copies for this rule only if their MANIFEST is intact and they were verified.
- **R2. Superseded versions go to `.trash`, not to deletion.** The swap moves the old version to `.trash/<id>/<oldver>/` (§4.5.1).
- **R3. Trash is purged** when either (a) its age exceeds `trash_days` (14), or (b) space is needed (§4.8), oldest first. In both cases only if R1 allows it given the **live** copy (meaning the replacement is verified). Space-need purges also require that the replacement has been verified after its swap (`verified` state, not just `swapped`).
- **R4. Restore on failure.** If a live copy fails re-verification (bit rot) or a swap fails, and `.trash` holds a verified older version, that version is moved back into place (reverse swap) and marked `verified` after a re-hash. READY shows the item as `stale` (older version) rather than missing.
- **R5. Quarantine** is not a copy (its files failed verification). It is deleted after `quarantine_days` (7), or first when space is needed.
- **R6. Orphans** (verified copies not in the profile) are deleted only by `aictl ark prune --orphans` (PIN in the UI, root in the CLI, confirmation `--yes` or interactive "type DELETE"), and only when R1 allows it **across the node**: if the orphan is the only verified copy of that item on any mounted root, prune always refuses for that item (`refused` with `reason: "last_verified_copy"`) and says so ("This is the only copy of *X* on this computer; it was kept."). There is no override flag: sync and prune never delete the last verified copy (AGENTS.md rule 3). To free that space, first copy the item to another root (for example `aictl ark sync --root <drive>`), then prune.
- **R7. Model deletion** via `DELETE /api/v1/models/{id}` (agent.md §3.8.12) follows R1 for the root(s) named, one root at a time, through `retention.CanDelete`; `--keep-last` is the default. A copy R1 refuses is kept and reported (`last_verified_copy`); when that leaves nothing deleted the route answers `409 LAI-SYN-044`. Only an explicit `keep_last=false` (`aictl delete --allow-last-copy`, a second confirmation, admin) skips R1 for that call; the cloud's `model.delete` never does. The engine interface the route calls is `modelsapi.SyncEngine` (`DeleteModel`, `StartModelDownload`; agent.md §3.8.32).
- **R8. Read-only roots** are never modified, so retention doesn't apply to them.
- **R9. Paths without a MANIFEST are not items.** `kit/config/` (config backups, [rebuild-kit.md](rebuild-kit.md) §3.1) and any other unknown path are never planned, verified, counted as orphans or deleted by sync or prune.

### 4.11 `MANIFEST.json`

Written at `<root>/<item-dir>/MANIFEST.json`. Example for a model:
```json
{
  "schema_version": 1,
  "item_id": "qwen3.5-9b-q4km",
  "item_kind": "model",
  "engine": "llama",
  "catalog_version": "2026.11.02-1",
  "version": "0123456789ab",
  "sort_key": "2026-11-02T03:41:10Z",
  "state": "verified",
  "source": {
    "type": "huggingface",
    "repo": "ggml-org/Qwen3.5-9B-GGUF",
    "revision": "0123456789abcdef0123456789abcdef01234567"
  },
  "files": [
    {
      "kind": "weights",
      "path": "Qwen3.5-9B-Q4_K_M.gguf",
      "size_bytes": 5870000000,
      "sha256": "<64 lowercase hex>",
      "sha256_source": "catalog+hf_lfs",
      "url": "https://huggingface.co/ggml-org/Qwen3.5-9B-GGUF/resolve/0123456789abcdef0123456789abcdef01234567/Qwen3.5-9B-Q4_K_M.gguf"
    }
  ],
  "total_bytes": 5870000000,
  "downloaded_at": "2026-11-02T03:39:55Z",
  "verified_at": "2026-11-02T03:41:10Z",
  "last_reverified_at": "2026-11-02T03:41:10Z",
  "trust": "pinned",
  "synced_by": {"node_id": "ai-n3", "agent_version": "0.4.0", "run_id": "run-20261102T031500Z-a1f3"},
  "history": [
    {"version": "fedcba987654", "verified_at": "2026-08-01T03:20:00Z", "superseded_at": "2026-11-02T03:41:10Z"}
  ]
}
```
Example for a kiwix ZIM (only the differing fields):
```json
{
  "item_id": "wikipedia-en-nopic", "item_kind": "kiwix", "version": "2026-10", "sort_key": "2026-10",
  "source": {"type": "kiwix", "book": "wikipedia_en_all_nopic", "base_url": "https://download.kiwix.org/zim/wikipedia/", "version_policy": "latest"},
  "files": [{"kind": "zim", "path": "wikipedia_en_all_nopic_2026-10.zim", "size_bytes": 48712345678,
             "sha256": "<64 hex>", "sha256_source": "kiwix_sidecar", "zim_md5_ok": true}],
  "trust": "sidecar"
}
```
Directory items (docs, map-assets, kit trees) list a `tree_sha256` (sha256 of the sorted `"<sha256>  <relpath>\n"` lines of every file) instead of each file, plus `file_count`; the per-file list lives in `<item-dir>/SHA256SUMS` (format of `sha256sum`).

Field rules:
- `state` is always `verified` on disk. Other states live only in `items.json`. A MANIFEST with another state is invalid (`LAI-SYN-052`).
- `trust` ∈ `pinned` (catalog sha matched source) · `sidecar` (kiwix sidecar) · `signed_index` (apt) · `computed` (pmtiles/git/docs) · `tofu` (§4.9.5).
- `history` keeps the last 10 versions (metadata only).
- Unknown fields are preserved on rewrite (forward compatibility for a node running an older agent on a newer drive).
- The code (`agent/internal/sync/manifest`) reads strictly and writes deterministically. Invalid, all `LAI-SYN-052`: not JSON or a wrong type; `schema_version` missing or not `1` (a newer file is never rewritten; the schema is checked before the other fields); `state` other than `verified`; an `item_id` or `version` that is not one plain path element; an empty `item_kind`, `sort_key` or `source.type`; a model without `engine`; an unknown `trust`; a missing `downloaded_at`, `verified_at` or `last_reverified_at`; a file with an empty kind, an unsafe or duplicate `path` (absolute, `..`, `MANIFEST.json`, `SHA256SUMS`), a negative size or a `sha256` that is not 64 lowercase hex digits; both a file list and a `tree_sha256`, or neither; a directory item without `file_count`.
- Write order is the spec order (`schema_version`, `item_id`, `item_kind`, `engine` when set, `catalog_version`, `version`, `sort_key`, `state`, `source`, `files` or `tree_sha256` and `file_count`, `total_bytes`, the three timestamps, `trust`, `synced_by`, `history`), then unknown keys sorted by name; timestamps are RFC 3339 UTC to the second; the file is indented by two spaces. `source` is `type` first, then its other keys sorted. `history` is newest first and is cut to 10 when written (a longer one written by another agent is still read).
- `tree_sha256` is the sha256 of the lines `"<sha256>  <relpath>\n"` of every file sorted bytewise as whole lines (so by hash first); the order of the files does not matter. `SHA256SUMS` is written sorted by path, in the format of `sha256sum` (`sha256sum`'s binary marker is read too), and before the manifest, so the commit point finds it.
- On load the manifest is cheap-checked, never hashed: every listed file must exist as a regular file with the recorded size (a directory item needs `SHA256SUMS`), else the item is missing (`LAI-SYN-052`).

### 4.12 Readiness report

`<root>/READY.json`, regenerated at the end of every run that touches the root, after verify and import, at mount, and at least every 6 h by the agent (ages change even when nothing else does).

#### 4.12.1 Schema and example
```json
{
  "schema_version": 1,
  "generated_at": "2026-11-02T04:02:10Z",
  "generated_by": {"node_id": "ai-n3", "agent_version": "0.4.0"},
  "root": {
    "id": "internal",
    "kind": "internal",
    "path": "/srv/localai",
    "profile": "small-8gb",
    "catalog_version": "2026.11.02-1",
    "fs_total_bytes": 502000000000,
    "fs_free_bytes": 105300000000,
    "budget_bytes": 220000000000,
    "used_bytes": 186400000000,
    "trash_bytes": 41200000000,
    "read_only": false
  },
  "overall": "yellow",
  "survival_capable": true,
  "last_sync": {"run_id": "run-20261102T031500Z-a1f3", "trigger": "scheduled", "finished_at": "2026-11-02T04:02:09Z", "state": "done", "age_days": 0},
  "last_full_reverify_at": "2026-10-20T04:10:00Z",
  "counts": {"verified": 24, "stale": 1, "missing": 0, "skipped": 3, "failed": 0, "quarantined": 0, "orphan": 1},
  "items": [
    {"id": "qwen3.5-9b-q4km", "kind": "model", "tier": 1, "survival": true, "status": "verified",
     "version": "0123456789ab", "verified_at": "2026-11-02T03:41:10Z", "last_reverified_at": "2026-11-02T03:41:10Z",
     "checked_current_at": "2026-11-02T03:20:00Z", "age_days": 0, "freshness": "green", "bytes": 5870000000, "trust": "pinned"},
    {"id": "wikipedia-en-nopic", "kind": "kiwix", "tier": 1, "status": "stale", "version": "2026-07",
     "reason": "update failed: retries exhausted", "code": "LAI-SYN-031", "age_days": 124, "freshness": "green", "bytes": 47800000000},
    {"id": "stackexchange-electronics", "kind": "kiwix", "tier": 2, "status": "skipped", "reason": "budget", "freshness": "yellow"}
  ],
  "problems": [
    {"code": "LAI-SYN-031", "severity": "warning", "item": "wikipedia-en-nopic", "title": "An update couldn't be downloaded. The older copy is still here and works."}
  ]
}
```
Item `status` ∈ `verified | stale | missing | skipped | failed | quarantined | orphan`:
- `verified`: the planned version is present and verified.
- `stale`: an older verified version is present; the planned newer one isn't (update failed or was skipped for budget).
- `missing`: no verified copy, and it should be here.
- `skipped`: no copy, and the planner skipped it (budget, disk, unresolved).
- `failed` / `quarantined`: no verified copy; the last attempt failed.
- `orphan`: present but not in the profile.

**Clock wrong.** When the agent's clock check fails (`now` is earlier than the newest `verified_at` on any root, `LAI-AGT-012`), every item's `freshness` is **omitted**, root-level age rules are not applied, and the report carries `"clock_wrong": true` at the top level (it is `false` or absent otherwise; cloud `ReadyReportV1` has the same optional field). UIs show the age columns grey with "clock wrong". `overall` is then computed from status and non-age rules only.

`checked_current_at` is the last time a run resolved the item and confirmed the present version is the one wanted. `age_days` is days since `checked_current_at` (or since `verified_at` if never checked).

#### 4.12.2 Colour rules

Per item `freshness` (by `age_days`):
| Kind | green | yellow | red |
|---|---|---|---|
| model (llama, whisper, piper) | ≤ 180 d | > 180 d | never by age (weights don't rot) |
| kiwix | ≤ 180 d | > 180 d | never by age |
| maps | ≤ 365 d | > 365 d | never by age |
| kit `debs`/`nvidia` closures | ≤ 60 d | > 60 d | codename or arch ≠ this node's (internal root only, §4.4.1; `LAI-KIT-042`) |
| other kit (sources, toolchains, binaries) | ≤ 90 d | > 90 d | llama.cpp tarball missing for `node.yaml: llama.commit` |

| docs | catalog version current and rendered with inventory | older catalog, or placeholders not filled (`LAI-SYN-062`) | missing |

Kit rows also use the **kit verify report** ([rebuild-kit.md](rebuild-kit.md) §4.4), which sync refreshes after any kit item changes and at the 6-hourly READY refresh: kit parts missing for this node (`LAI-KIT-040`) → red on the internal root, yellow on a drive; parts missing for another pinned lab node on a drive (`LAI-KIT-041`) → yellow; CUDA toolkit not matching this node's driver (`LAI-KIT-043`) → yellow; kit for a different Ubuntu codename (`LAI-KIT-042`) → red. These problems appear in READY `problems[]` with their codes.

Per item status colour:
| Status | tier 1 or survival model or kit/docs | tier 2 | tier 3 |
|---|---|---|---|
| verified | (freshness) | (freshness) | (freshness) |
| stale | yellow | yellow | green |
| missing, failed, quarantined | **red** | yellow | yellow |
| skipped | **red** (only possible via `LAI-SYN-041`/`042`) | yellow | green |
| orphan | green | green | green |

Root-level rules (in addition):
| Condition | Colour |
|---|---|
| drive root: `last_sync.age_days` > 30 | yellow (`LAI-ARK-008`) |
| drive root: `last_sync.age_days` > 90 | red (`LAI-ARK-008`) |
| internal root: `last_sync.age_days` > 14 while mode was `online` most of that time | yellow (`LAI-SYN-004`) |
| any item not re-verified in 2 × `reverify_days` | yellow (`LAI-SYN-025`, "not re-checked") |
| `fs_free_bytes` < `min_free_bytes` | yellow (`LAI-SYN-040`) |
| root `unclean` | red (`LAI-ARK-004`) |

`overall` = red if any item or root rule is red, else yellow if any is yellow, else green.

`survival_capable` = true iff, on this root **or across all mounted roots for the node-level view**, every model in `profile.survival.models` that is loadable on this node has a verified copy, at least one whisper model and one piper voice per Portal UI language (`en` and `es`, the languages of [survival-portal.md](survival-portal.md) §4.9) are verified, at least one tier-1 kiwix item is verified, and the tier-1 map plus `map-assets` are verified. The node-level value is reported by `GET /api/v1/status` and `/portal/status`.

### 4.13 Re-verification (bit rot)

- Every scheduled run spends up to `reverify_budget_minutes` re-hashing items in order of oldest `last_reverified_at`. With the defaults, a 600 GB drive at 150 MB/s is fully re-checked about every 2 weeks if plugged in nightly, and within `reverify_days` for internal disks.
- `aictl ark verify --full` hashes everything now.
- A mismatch on a verified copy: raise `LAI-SYN-025` (red for tier 1), move the bad files to quarantine (writable roots), apply R4 (restore from trash if possible), mark the item `missing` or `stale`, and schedule a re-download (or local copy) in the next run. On read-only roots the item is excluded from BestCopy in memory.
- Re-verification reads with `posix_fadvise(SEQUENTIAL)` then `DONTNEED`, so it doesn't evict the page cache of a loaded model.

### 4.14 Import (`aictl ark import`)

Copies verified items from a drive root to the internal root. No network is used, so it works in every mode (including survival) and with no catalog refresh.
1. Source root: `--from` or the only mounted drive (several drives and no `--from` → `409 LAI-ARK-015` listing them, §3.5).
2. Catalog: if `<drive>/localai/kit/catalog/CATALOG_VERSION` is newer than the node's and `--catalog` is given, validate it with the catalog linter and install it into `/etc/localai/catalog/` first (`LAI-CAT-002` if invalid; the import continues with the old catalog).
3. Plan: the normal planner (`plan.Plan`) for the internal root with `Offline` set, `Resolved` filled from the **drive's MANIFESTs** (not the network) and `OtherRoots` holding the drive's copies, so every download/update is `source = local_copy`. Items not on the drive are `skip: unresolved`. Budget and `min_free_bytes` apply.
4. `--item <id>`: import only those. If an item isn't in the internal profile, `--add-to-profile` appends it to `node.yaml: profile_overrides.add_models|add_content` (PIN in the UI); without it, the import refuses that item with a message.
5. The copy runs through the state machine (§4.5) with streaming sha256 against the drive MANIFEST sha256, **and** against the catalog sha256 where the catalog pins one. A drive copy that no longer matches its own MANIFEST is reported as `LAI-SYN-070` (and quarantined in memory for that drive).
6. Copy speed is limited only by disks; `ionice` idle class does not apply to imports (a person is waiting).
7. At the end, READY.json is rewritten for the internal root, and the router and companion services are notified.

`--dry-run` prints the plan like §4.4.4, with the source column set to the drive id.

### 4.15 Drive extras

After every run that touches a drive root (and at `aictl ark init`), the agent writes, at the **drive top level** (the mount point, not inside `localai/`):

**`START-HERE.html`** (0644): a single self-contained HTML file (inline CSS, no scripts, no external URLs), generated from a template embedded in the agent (`agent/internal/ark/starthere.html.tmpl`). Bilingual, English first, then Spanish. Content:
1. Title: "AI-ARK drive. Start here." / "Disco AI-ARK. Empiece aquí."
2. Drive status: overall colour, `survival_capable`, last sync date, catalog version, and the node that last wrote it (from READY.json at write time).
3. "If you have a working AI computer: plug this drive in. The computer finds it by itself. Open the Local AI app, or a browser at http://127.0.0.1:7070." (and ES).
4. "If the computer has no AI software: open the rebuild guide." Relative link `localai/docs/06-rebuild-a-node-offline.html` (rendered docs are flat: `<root>/docs/<slug>.html`, the `html` paths of `docs/index.json`, agent.md §3.8.29).
5. Links (relative) to `localai/docs/index.html`, the survival runbook (`localai/docs/01-survival-mode.html`), the survival card (`localai/docs/survival-card-<node-id>.pdf`, or `.html` when no PDF was rendered), and `localai/READY.json`.
6. "Emergency, without the app" section: how to read the library with only the kiwix-tools from the kit (`localai/kit/tools/kiwix-tools/…`, command `./kiwix-serve --port 8888 --library localai/kiwix/library.xml`, then open `http://127.0.0.1:8888`).
7. A table of contents: one row per item (display name, version, size, status), from READY.json.

It is written with tmp + rename. Paths in it are relative, so it works from any mount point and in any browser.

**`inventory.local.yaml`** (0600 root): if `sync.ark_drive.copy_inventory` is true and `/etc/localai/inventory.local.yaml` exists:
- Compare sha256 of both files. Equal → nothing to do.
- The drive has no file → copy (tmp + fsync + rename, mode 0600).
- Both exist and differ → the newer `mtime` wins only if the other side is unchanged since the last copy (the agent stores the last copied sha256 in `items.json` under `extras.inventory`). If both changed since the last copy (a real conflict), keep both: write the node's file as `inventory.local.yaml.<node-id>.<UTC-ts>.conflict` on the drive, leave the drive file alone, and raise `LAI-ARK-012`.
- The drive copy is the one `aictl docs render` uses on nodes without their own inventory (§4.9.8).
- Never reported to the cloud or logged (only `present: true/false`).

### 4.16 Reporting to the cloud

When the node is enrolled and in `online` mode ([cloud-app.md](cloud-app.md) §3.6):
- The schema of everything sent is owned by [cloud-app.md](cloud-app.md) §3.6 (`LocalaiStateSnapshotV1`); this section only says what this spec contributes. The EAB heartbeat carries only the `localai.ark_*` / `localai.sync_*` summary keys in `vitals.extra` ([agent.md](agent.md) §4.8.3).
- Each state snapshot carries the `ark` block (per root: `id`, `kind`, tiers, counts, `overall`, `survival_capable`, `last_sync_at`, bytes used/free) and the `sync` block (`running`, `run_id`, `progress_pct`, `last_run`).
- After each run (and whenever a root's READY.json is regenerated), the next snapshot carries the full READY.json of every touched root in `ark.roots[].ready`, minus `root.path` (mount paths contain the desktop user name) and minus `generated_by.node_id` (implicit). `root.path` is stripped for the internal root too, for one simple rule.
- A `localai.ark.sync` or `localai.ark.verify` job settles **when the run starts**, with the [cloud-app.md](cloud-app.md) §3.7 result `{"sync_run_id": "run-…", "started": true, "plan": {…dry-run totals…}}`; progress and the final outcome reach the cloud only through the next snapshots (`sync` block and READY). A job that cannot start (preflight refusal, run already active) settles as `failure` with the refusal code (`LAI-SYN-010`, `LAI-SYN-011`, `LAI-SYN-005`, …).
- **Snapshot mappings.** Per root, `tiers.t1|t2|t3` = `ok` when every planned item of that tier is `verified`, `missing` when none is verified (or present as `stale`), `partial` otherwise; `not_planned` when the profile plans no item of that tier. `sync.last_run.result` from the last run's `state`: `done` with 0 failed and 0 skipped-for-time → `ok`; `done` with failures or `skipped: time_limit` items → `partial`; `failed` or `cancelled` → `failed` (with `error_code`); `skipped` → `skipped` with `skip_reason` = the preflight reason. `last_run` covers runs of every trigger, including `verify` and `import`.
- Nothing about `inventory.local.yaml` contents or rendered docs is sent.
- Reports that can't be delivered are not queued. The next snapshot simply carries the current state.

### 4.17 Formatting a new AI-ARK drive

> **WARNING: these commands ERASE the whole drive.** Choosing the wrong device erases the wrong disk, including the computer's own disk. Read the device name twice. Unplug every other USB disk first.
>
> **The ark drive is not encrypted.** Anyone who finds it can read everything on it, including `inventory.local.yaml`. That is on purpose, so it can be used in an emergency. Don't store personal secrets on it.

Use a USB 3 SSD of at least 1 TB (2 TB recommended for the `ark-drive-full` profile). Spinning disks work but sync and model loading are slower.

1. Plug in **only** the new drive. List disks:
   ```
   lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS,FSTYPE,LABEL
   ```
   What you should see: a disk with `TRAN` = `usb` and the size of your new drive, for example `sdb  1.8T  Portable SSD  usb`. The computer's own disk is usually `nvme0n1` or `sda` with mount points like `/`. **Never pick that one.** Write down the name, for example `sdb`. Below, replace `sdX` with it.
2. Unmount anything the desktop auto-mounted from it:
   ```
   sudo umount /dev/sdX?* 2>/dev/null; true
   ```
3. Erase old partition tables and signatures:
   ```
   sudo wipefs --all /dev/sdX
   ```
   What you should see: lines like `/dev/sdX: 8 bytes were erased at offset …`, or nothing if the drive was blank.
4. Create one partition that fills the drive:
   ```
   sudo parted --script /dev/sdX mklabel gpt mkpart AI-ARK ext4 1MiB 100%
   ```
5. Format it as ext4 with the label `AI-ARK` (`-m 0` gives back the 5 % reserved for root, which a data drive doesn't need; `-E lazy_itable_init=0` makes the first sync faster):
   ```
   sudo mkfs.ext4 -L AI-ARK -m 0 -E lazy_itable_init=0,lazy_journal_init=0 /dev/sdX1
   ```
   What you should see: `Creating filesystem with … blocks` and `Writing superblocks and filesystem accounting information: done`.
6. Check:
   ```
   sudo blkid /dev/sdX1
   ```
   What you should see: `LABEL="AI-ARK"` and `TYPE="ext4"`.
7. Mount it. Either unplug and replug it (the desktop mounts it at `/media/<you>/AI-ARK`), or:
   ```
   udisksctl mount -b /dev/sdX1
   ```
8. Set it up for Local AI:
   ```
   aictl ark drives
   aictl ark init <root-id shown above>
   ```
   What you should see: the drive state changes from `uninitialized` to `ready`.
9. Fill it (needs internet, takes many hours the first time; leave it overnight, plugged into power):
   ```
   aictl ark sync --root <root-id> --wait
   ```

If it doesn't work: `blkid` shows `TYPE="exfat"` or `ntfs` → step 5 didn't run on the right partition; repeat from step 1. See [runbooks/07-troubleshooting.md](../runbooks/07-troubleshooting.md).

Don't use exFAT, NTFS or FAT32: they can't store Unix permissions, so the rebuild kit wouldn't work (ADR-0005).

## 5. Failure handling and edge cases

| Situation | Behaviour | Code |
|---|---|---|
| Internet drops mid-download | transfer stalls, 60 s stall timeout, retries with backoff, then `failed` (resumable); run continues with other items that may use local copies | `LAI-SYN-031` |
| Node switched to offline mode during a run | run cancelled gracefully (modes.md §2.1 step 3); `.part` kept | `LAI-SYN-002` |
| Agent killed or power lost mid-download | on restart, `items.json` says `downloading`; resume from the last checkpoint (truncate `.part` to checkpoint; if shorter, restart file) | — |
| Power lost mid-swap | replay the swap plan (§4.5.1); no MANIFEST means not present until replay finishes | `LAI-SYN-026` if unrecoverable |
| `items.json` corrupt or missing | restore it from `items.json.bak`, else rebuild it from MANIFESTs on all roots, and reconcile with them (§4.5.2); `.staging` content without journal entries is treated as unknown and deleted (it was never verified) | `LAI-SYN-050` |
| MANIFEST present but a file is missing or a size differs | item treated as missing; re-download; R4 restore if trash has a copy | `LAI-SYN-052` |
| Streaming sha256 mismatch | quarantine; next run starts fresh; twice in a row → version blocked | `LAI-SYN-020` |
| HF LFS oid ≠ catalog sha256 | item not downloaded; red | `LAI-SYN-021` |
| Kiwix `.sha256` missing or malformed | item not downloaded; previous version kept | `LAI-SYN-022` |
| `pmtiles verify` fails | quarantine | `LAI-SYN-023` |
| pmtiles CLI or git or apt-get missing | item `failed`; others continue | `LAI-SYN-061` |
| Kiwix directory listing has no dated file | `failed`, keep present | `LAI-SYN-033` |
| Server ignores Range | restart from 0 for that file | `LAI-SYN-032` (info) |
| HF 401/429 | 401: needs token; 429: transient with Retry-After | `LAI-SYN-034` |
| Disk below `min_free_bytes` | purge eligible trash, else skip item; never delete verified copies | `LAI-SYN-040` |
| Survival model skipped for budget | plan proceeds; READY red | `LAI-SYN-041` |
| Kit or docs item can't fit | READY red | `LAI-SYN-042` |
| Trash purged to make room | info in run log | `LAI-SYN-043` |
| Re-verify mismatch (bit rot) | quarantine, R4 restore, re-fetch next run | `LAI-SYN-025` |
| apt closure unsatisfiable (package renamed in new codename) | item `failed`, previous closure kept, red if older than 60 d | `LAI-SYN-060` |
| Docs rendered without inventory | docs yellow | `LAI-SYN-062` |
| Docs rendered without the survival-card PDF (no Chromium) | docs yellow, item still verified | `LAI-CLI-014` |
| Drive with label AI-ARK but exFAT/NTFS | `unusable`, never touched | `LAI-ARK-002` |
| Drive mounted read-only | served and importable, not written | `LAI-ARK-003` |
| Drive has ext4 errors | read-only + serve-time verification; tell user to fsck | `LAI-ARK-004` |
| Drive plugged in, not mounted | UI offers Mount | `LAI-ARK-005` |
| Two drives with the same UUID | second ignored | `LAI-ARK-006` |
| Drive pulled without eject | transfers checkpointed, services switch copies | `LAI-ARK-007` |
| Drive last synced > 30 d / > 90 d | yellow / red | `LAI-ARK-008` |
| Import with several drives and no `--from` | refused, drives listed | `LAI-ARK-015` |
| Eject blocked by open files or a running model | refuse, list blockers | `LAI-ARK-009` |
| New AI-ARK drive with no `localai/` | `uninitialized`, offer Set up | `LAI-ARK-010` |
| Drive smaller than the ark-drive-full plan | planner drops by tier (normal budget logic with budget = fs size − min_free) | `LAI-ARK-011` (info) |
| inventory.local.yaml conflict | both kept | `LAI-ARK-012` |
| EIO during writes to the drive | stop writing to that root | `LAI-ARK-014` |
| `/srv/localai` missing | recreated empty; READY root red, doctor warning | `LAI-ARK-020` |
| Content root on a network filesystem | read-only | `LAI-ARK-021` |
| Clock wrong (e.g. RTC battery dead, year 2000) | ages computed from `now` could be negative or huge: negative ages clamp to 0; if `now` < the newest `verified_at` on any root, age-based colours are suppressed: `freshness` omitted and `clock_wrong: true` in READY (§4.12.2), shown grey "clock wrong", and `LAI-AGT-012` is raised by agent.md | `LAI-AGT-012` |
| Catalog removes an item that is present | becomes `orphan`, never auto-deleted | `LAI-SYN-051` |
| Catalog changes a pinned sha256 for the same file name | new version planned; old goes to trash after the new one verifies | — |
| Two agents (e.g. one in a chroot) | second fails to get the flock | `LAI-SYN-005` |
| Sync started while a drive is `ejecting` | that root is excluded | — |

## 6. Security considerations

- **Integrity (T7).** No file is served, loaded or counted unless its sha256 matches. Expected hashes come from the catalog pin, cross-checked with the source's own published value where one exists (HF LFS oid, GitHub asset digest, Kiwix sidecar, apt signed indexes). `trust: tofu` and `computed` are labelled as such in READY and the UI.
- **Kiwix sidecar from origin only.** The `.sha256` is fetched from `download.kiwix.org` with redirects refused, so a malicious mirror can't serve both a bad file and a matching hash.
- **Tokens.** The HF token is sent only to `huggingface.co` and never logged. The journal never stores headers.
- **Subprocess safety.** `git`, `apt-get` and `pmtiles` run with a fixed argv (no shell), a scrubbed environment, `HOME` set to the sync dir, and (apt, pmtiles) dropped to the `localai` user. Git hooks and file-protocol submodules are disabled.
- **Archive extraction** rejects path traversal, absolute paths, symlinks out of the tree, hard links and device nodes.
- **Drive trust.** A drive may come from anywhere. Content on a drive is used only through its MANIFEST and only if its sha256 matches the **catalog** pin where one exists. For `computed`/`tofu` items, a drive copy is trusted as much as the drive itself; the UI says "from drive (not checked against the catalog)". Executables in `kit/` are never executed by the agent automatically; the rebuild commands verify them first ([rebuild-kit.md](rebuild-kit.md) §Verification).
- **inventory.local.yaml** is 0600 on the drive. The drive is unencrypted by design (T9), which the formatting section warns about.
- **Deletion** is centralised in `retention.CanDelete`, which enforces R1 and is unit-tested exhaustively.
- **Mount handling.** The agent never mounts or formats drives on its own. `aictl ark init` only creates a directory and a JSON file.
- **Throttling proxy** listens on loopback only, accepts only CONNECT to hosts in the item's allowlist (from its catalog URLs), and dies with the subprocess.

## 7. Observability

**Logs** (slog JSON, component `sync` or `ark`). Common keys: `run_id`, `root`, `item`, `version`, `state`, `attempt`, `bytes`, `rate_bps`, `code`, `err`. Examples:
```json
{"level":"INFO","msg":"item state","component":"sync","run_id":"run-20261102T031500Z-a1f3","root":"internal","item":"qwen3.5-9b-q4km","from":"downloading","to":"verifying","bytes":5870000000}
{"level":"WARN","msg":"checksum mismatch","component":"sync","run_id":"…","root":"ark-3f9c0a12","item":"wikimed-en","expected":"9f2c…e1","actual":"77ab…09","code":"LAI-SYN-020"}
{"level":"INFO","msg":"drive mounted","component":"ark","root":"ark-3f9c0a12","mount":"/media/operator/AI-ARK","state":"ready"}
```
Hashes are logged as `first4…last2`. URLs are logged without query strings.

**Events:** §3.6.

**Metrics** (exposed in `/api/v1/status` under `ark.metrics` and, if agent.md adds a Prometheus text endpoint, with these names):
| Metric | Type | Labels |
|---|---|---|
| `localai_sync_bytes_total` | counter | `root`, `source` (network, local_copy) |
| `localai_sync_items_total` | counter | `root`, `result` |
| `localai_sync_run_seconds` | gauge (last run) | `trigger` |
| `localai_ark_root_overall` | gauge 0/1/2 (green/yellow/red) | `root` |
| `localai_ark_root_free_bytes` | gauge | `root` |
| `localai_ark_reverify_age_days_max` | gauge | `root` |

## 8. Doctor codes

User-visible fixes are shown in the desktop and, for `LAI-ARK` codes, in the Portal status strip, so both languages are given.

| Code | Sev | Meaning | Fix (en) | Fix (es) |
|---|---|---|---|---|
| LAI-SYN-001 | info | Scheduled sync skipped: no internet | Nothing to do. Sync will try again tonight. | Nada que hacer. Se intentará otra vez esta noche. |
| LAI-SYN-002 | info | Sync skipped or stopped because the node is in offline or survival mode | Nothing to do. Switch to online mode to sync again. | Nada que hacer. Cambie a modo en línea para sincronizar. |
| LAI-SYN-003 | info | Scheduled sync skipped: running on battery | Plug in the charger. Sync runs at night when plugged in. | Conecte el cargador. La sincronización corre de noche con el cargador conectado. |
| LAI-SYN-004 | warning | No successful sync for more than 14 days while online | Check the internet connection, then run `aictl ark sync`. | Revise la conexión a internet y ejecute `aictl ark sync`. |
| LAI-SYN-005 | warning | Another sync is already running | Wait for it to finish, or cancel it with `aictl ark sync --cancel`. | Espere a que termine, o cancélela con `aictl ark sync --cancel`. |
| LAI-SYN-010 | warning | Manual sync refused in survival mode | Leave survival mode first (needs the PIN). | Primero salga del modo supervivencia (requiere el PIN). |
| LAI-SYN-011 | warning | Manual sync in offline mode needs confirmation | Run again with `--allow-offline-mode`, or switch to online mode. | Ejecute otra vez con `--allow-offline-mode`, o cambie a modo en línea. |
| LAI-SYN-020 | warning | A download didn't match its checksum and was quarantined | Nothing to do: it will be downloaded again. If it repeats, report it. | Nada que hacer: se descargará de nuevo. Si se repite, repórtelo. |
| LAI-SYN-021 | warning | The source's checksum or size disagrees with the catalog | The catalog or the source is wrong. Report it to the maintainer. The old copy is kept. | El catálogo o la fuente están mal. Repórtelo. Se conserva la copia anterior. |
| LAI-SYN-022 | warning | Kiwix checksum file missing or unreadable | Nothing to do. It will be retried next run; the old copy is kept. | Nada que hacer. Se reintentará; se conserva la copia anterior. |
| LAI-SYN-023 | warning | Map extract failed `pmtiles verify` | Nothing to do: it will be rebuilt next run. | Nada que hacer: se reconstruirá en la próxima sincronización. |
| LAI-SYN-024 | warning | A source mirror is damaged or missing the pinned commit | Run `aictl ark sync --item <id>`. If it repeats, delete the mirror with `aictl ark prune`. | Ejecute `aictl ark sync --item <id>`. Si se repite, borre el espejo con `aictl ark prune`. |
| LAI-SYN-025 | error | A stored file no longer matches its checksum (disk damage), or wasn't re-checked in time | Run `aictl ark verify --full`. If files keep failing, the disk may be dying: copy the ark to another drive. | Ejecute `aictl ark verify --full`. Si siguen fallando, el disco puede estar dañándose: copie el arca a otro disco. |
| LAI-SYN-026 | error | A swap couldn't complete | Run `aictl ark verify`. The previous version was restored if it existed. | Ejecute `aictl ark verify`. Se restauró la versión anterior si existía. |
| LAI-SYN-027 | warning | Downloaded size differs from the expected size | Nothing to do: it will be downloaded again. | Nada que hacer: se descargará de nuevo. |
| LAI-SYN-028 | warning | ZIM internal checksum failed | Nothing to do: it will be downloaded again. | Nada que hacer: se descargará de nuevo. |
| LAI-SYN-030 | warning | The server refused or no longer has the file (403/404/410) | Update the catalog (`aictl catalog refresh`). If it persists, report it. | Actualice el catálogo (`aictl catalog refresh`). Si sigue, repórtelo. |
| LAI-SYN-031 | warning | Download failed after all retries | Check the internet connection. It resumes where it stopped next run. | Revise la conexión. Continuará donde quedó en la próxima sincronización. |
| LAI-SYN-032 | info | Server doesn't support resume; the file restarted from zero | Nothing to do. | Nada que hacer. |
| LAI-SYN-033 | warning | Couldn't find the newest version at the source | Nothing to do; it retries next run. If it persists, report it. | Nada que hacer; se reintenta. Si sigue, repórtelo. |
| LAI-SYN-034 | warning | Hugging Face needs a login or is rate-limiting | Put a Hugging Face token in `/etc/localai/hf-token`, or wait an hour. | Ponga un token de Hugging Face en `/etc/localai/hf-token`, o espere una hora. |
| LAI-SYN-035 | error | The same item failed in 3 runs in a row | Run `aictl doctor` and read the item's last error. | Ejecute `aictl doctor` y lea el último error del elemento. |
| LAI-SYN-040 | warning | Not enough free disk space | Free space on the disk, or lower the profile budget. Old versions in `.trash` are removed automatically when safe. | Libere espacio en el disco o baje el presupuesto del perfil. Las versiones viejas se borran solas cuando es seguro. |
| LAI-SYN-041 | warning (error when a survival model is skipped) | Items skipped because the budget is full (a survival model, if named) | Raise `budget_bytes` or remove other content in `node.yaml`. | Suba `budget_bytes` o quite otro contenido en `node.yaml`. |
| LAI-SYN-042 | error | The rebuild kit or manuals don't fit | Free disk space. Without the kit this computer can't be rebuilt offline. | Libere espacio. Sin el kit esta computadora no se puede reconstruir sin internet. |
| LAI-SYN-043 | info | Old versions were deleted to make room | Nothing to do. | Nada que hacer. |
| LAI-SYN-044 | warning | The last verified copy was kept; nothing was deleted | This is the only verified copy on this computer. Copy it to another drive first (`aictl ark sync --root <drive>`), or delete it anyway with `aictl delete <model> --allow-last-copy`. | Es la única copia verificada de este equipo. Cópiela primero a otro disco (`aictl ark sync --root <disco>`), o bórrela igualmente con `aictl delete <modelo> --allow-last-copy`. |
| LAI-SYN-050 | info | Sync journal was damaged and rebuilt | Nothing to do. | Nada que hacer. |
| LAI-SYN-051 | info | Content no longer in the profile is using disk space | Run `aictl ark prune --orphans` if you don't need it. | Ejecute `aictl ark prune --orphans` si no lo necesita. |
| LAI-SYN-052 | warning | An item's MANIFEST is missing or damaged | Run `aictl ark verify`; the item will be fetched again. | Ejecute `aictl ark verify`; el elemento se descargará otra vez. |
| LAI-SYN-060 | warning | Couldn't compute the package list for the rebuild kit | Report it: a package name in the catalog probably changed. The old package set is kept. | Repórtelo: probablemente cambió el nombre de un paquete. Se conserva el conjunto anterior. |
| LAI-SYN-061 | error | A tool sync needs is missing (pmtiles, git, apt-get) | Run `sudo install-node.sh --repair`. | Ejecute `sudo install-node.sh --repair`. |
| LAI-SYN-062 | warning | Manuals were made without the real lab values | Copy `inventory.local.yaml` to `/etc/localai/` or plug in the ark drive, then sync. | Copie `inventory.local.yaml` a `/etc/localai/` o conecte el disco del arca, y sincronice. |
| LAI-SYN-070 | error | A drive copy no longer matches its own checksum | That copy is damaged. Sync the drive on a computer with internet, or use another drive. | Esa copia está dañada. Sincronice el disco en una computadora con internet, o use otro disco. |
| LAI-ARK-001 | error | No writable content root (no drive and internal read-only) | Plug in an AI-ARK drive, or run `aictl doctor`. | Conecte un disco AI-ARK, o ejecute `aictl doctor`. |
| LAI-ARK-002 | error | A drive labelled AI-ARK is not ext4 | Reformat it as ext4 (see "Formatting a new AI-ARK drive"). This erases it. | Formatéelo como ext4 (vea "Formatear un disco AI-ARK nuevo"). Esto lo borra. |
| LAI-ARK-003 | warning | The ark drive is read-only | It can be read but not updated. Unplug it safely and plug it in again. | Se puede leer pero no actualizar. Desconéctelo con seguridad y conéctelo otra vez. |
| LAI-ARK-004 | error | The ark drive's filesystem has errors | Eject it, then run `sudo fsck.ext4 -f /dev/<device>` and plug it in again. | Expúlselo, ejecute `sudo fsck.ext4 -f /dev/<dispositivo>` y conéctelo otra vez. |
| LAI-ARK-005 | warning | An AI-ARK drive is plugged in but not mounted | Press **Mount** in the app, or open it in the file manager. | Presione **Montar** en la aplicación, o ábralo en el explorador de archivos. |
| LAI-ARK-006 | error | Two drives have the same ID (a cloned drive) | Unplug one. Give it a new ID with `sudo tune2fs -U random /dev/<device>`. | Desconecte uno. Dele un nuevo ID con `sudo tune2fs -U random /dev/<dispositivo>`. |
| LAI-ARK-007 | warning | The ark drive was unplugged without ejecting | Next time press **Eject** first. Plug it in again to let sync check it. | La próxima vez presione **Expulsar** antes. Conéctelo otra vez para revisarlo. |
| LAI-ARK-008 | warning (error after 90 days) | The ark drive hasn't been updated for more than 30 days (red after 90) | Plug it into a computer with internet overnight. | Conéctelo a una computadora con internet durante la noche. |
| LAI-ARK-009 | warning | The drive can't be ejected: files are in use | Close the listed programs or switch brain, then eject again. | Cierre los programas indicados o cambie de cerebro, y expulse otra vez. |
| LAI-ARK-010 | info | A new AI-ARK drive needs to be set up | Press **Set up this drive**, or run `aictl ark init`. | Presione **Preparar este disco**, o ejecute `aictl ark init`. |
| LAI-ARK-011 | info | The drive is too small for everything; lower tiers were skipped | Nothing to do, or use a bigger drive. | Nada que hacer, o use un disco más grande. |
| LAI-ARK-012 | warning | The lab inventory on the drive and on this computer both changed | Compare the two files and keep the right one. The other is saved as `.conflict`. | Compare los dos archivos y quédese con el correcto. El otro se guardó como `.conflict`. |
| LAI-ARK-014 | error | The drive reported read/write errors | Eject it and try another USB port or cable. If it repeats, replace the drive. | Expúlselo y pruebe otro puerto o cable USB. Si se repite, cambie el disco. |
| LAI-ARK-015 | warning | Several ark drives are connected; choose one | Run the import again with `--from <root-id>` (see `aictl ark drives`). | Ejecute la importación otra vez con `--from <root-id>` (vea `aictl ark drives`). |
| LAI-ARK-020 | warning | The internal content folder was missing and was recreated empty | Run `aictl ark sync`, or `aictl ark import` from a drive. | Ejecute `aictl ark sync`, o `aictl ark import` desde un disco. |
| LAI-ARK-021 | warning | The content folder is on a network disk; it is used read-only | Move `/srv/localai` to a local disk. | Mueva `/srv/localai` a un disco local. |

## 9. Test plan

### 9.1 Unit (table-driven, `go test ./internal/sync/... ./internal/ark/...`)
| Area | Cases |
|---|---|
| mountinfo parser | octal escapes; optional fields; bind mounts; `ro` in mount vs super options; overlay/tmpfs ignored |
| drive classification | ext4+AI-ARK → ready; exfat+AI-ARK → unusable; `AI-ARK2` → not ark; duplicate UUID; `errors_count=3` → unclean; missing ROOT.json with/without content |
| planner | budget exact fit; budget off by one byte; tier ordering; list-position tie-break; survival models first; kit/docs never dropped; kit codename filtering; overrides add/remove/budget; refresh_days not due; pinned change always applied; unresolved offline with/without present; orphans; peak computation; local_copy sourcing; unknown ids; read-only root; drive limits; staged bytes; never a delete action; determinism; golden human text (en, es) and JSON, and the text rendered from the JSON; `POST /ark/sync` dry run writes nothing under the roots or the state directory |
| min_free | start blocked → trash purge makes room; trash ineligible (replacement unverified) → skip disk |
| state machine | every transition; crash at each state → resume correct; cancel at each state |
| downloader (with `httptest`) | 206 resume; 200 on Range → restart; 416 at full size; If-Range mismatch; stall timeout; 429 with Retry-After; 404 permanent; redirect drops Authorization cross-host; checkpoint restore of sha256 state equals uninterrupted hash; `.part` longer and shorter than checkpoint; truncated body; 5xx then success; retries exhausted (7 attempts); progress resets the backoff; checksum and size mismatch; redirect to http refused; no secret in logs or errors; local copy; throttle (virtual clock: two downloads within ±10 % of the limit over 10 s) and proxy refusals |
| ENOSPC | fake filesystem returns ENOSPC at byte N → checkpoint at last good offset, item skipped, `.part` kept |
| resolvers | HF headers (match, oid mismatch, size mismatch, commit mismatch, missing headers → tree API); kiwix listing parse (multiple dates, torrents ignored, none); sidecar malformed; sidecar redirect refused; pmtiles newest-date probe; GitHub digest mismatch; apt `--print-uris` parsing (MD5-only line rejected); Packages stanza rewrite |
| swap | per-file renames; directory RENAME_EXCHANGE; crash between each rename → replay; MANIFEST is the last write |
| manifest and journal | round trip with unknown keys; history cap; every invalid manifest (§4.11); a fault at each step of the atomic writes (open, write, chmod, fsync, close, rename, directory sync, killed process, disk full, read-only); damaged, missing and foreign-schema `items.json`; restore from `.bak`; rebuild from manifests; reconcile; crash between journal append and side effect; torn run-log line; BestCopy preference matrix and unclean gating; concurrent rebuild under `-race` |
| retention | `CanDelete` matrix: last copy, copy on unmounted drive, copy not re-verified in 24 h, trash with unverified replacement, orphan only copy, quarantine |
| READY colours | every row of §4.12.2; clock-wrong suppression; survival_capable |
| inventory copy | equal; drive missing; one side changed; both changed → conflict |
| START-HERE | template renders with no `http` URLs except `127.0.0.1` text; relative links |

Static check (CI): a test greps `internal/sync` and `internal/ark` for `os.Remove`, `os.RemoveAll`, `unix.Unlink` outside `retention/` and fails if any is found.

### 9.2 Integration (fakes)
- `fakehf` server (resolve redirects + LFS headers + CDN with Range), `fakekiwix` (directory listing + mirror redirect + sidecar), `fakegithub` (releases API), a tiny planet `.pmtiles` fixture served with Range, and a local bare git repo. Run a full sync into two temp roots (one on a loop-mounted ext4 image when the test runs as root in CI's privileged job; otherwise two temp dirs) and assert READY is green.
- Kill the agent (SIGKILL) at random byte offsets 50 times; restart; the run must finish with identical files and no leftover `.staging` entries.
- Disk-full: a 2 GB loop ext4 image, a plan of 3 GB; assert skip, no verified copy deleted.
- apt_closure: run against a local mirror fixture (a few `.deb`s with signed `InRelease` made with a test key) to exercise the private-dir options without the real archive.
- Import: sync to "drive" temp root, then import into an empty internal root with the network transport disabled (the test transport panics on any dial).

### 9.3 Manual on a real node (ai-n3)
1. Dry run:
   ```
   aictl ark sync --dry-run
   ```
   Expect: the plan table for `internal`, nothing written (`ls /srv/localai/.staging` unchanged).
2. Sync one small item and watch it:
   ```
   aictl ark sync --item piper-es-medium --wait
   cat /srv/localai/speech/piper/piper-es-medium/MANIFEST.json | jq .state
   ```
   Expect `"verified"`.
3. Resume: start `aictl ark sync --item wikimed-en --wait`, unplug the network after ~1 GB, wait for `LAI-SYN-031`, plug it back, run again. Expect the log line `resume offset=…` and no restart from zero.
4. Drive: plug in a freshly formatted AI-ARK drive. `aictl ark drives` → `uninitialized`. `aictl ark init <root-id>` → `ready`. Run `aictl ark sync --root <root-id> --item docs-bundle --wait`, then open `/media/<user>/AI-ARK/START-HERE.html` in a browser with networking off: it renders, links work.
5. Eject: `aictl ark eject <root-id>` → "You can unplug the drive now."; `lsblk` shows it unmounted.
6. Read-only: `sudo mount -o remount,ro /media/<user>/AI-ARK` → `aictl ark drives` shows `read_only` and `LAI-ARK-003`.
7. Import offline: `aictl mode offline`, then `aictl ark import --from <root-id> --item whisper-small --dry-run`, then without `--dry-run`. Expect success with no network.
8. Bit rot: copy a small item's file, flip one byte with `printf '\x00' | sudo dd of=<file> bs=1 seek=1000 conv=notrunc`, run `aictl ark verify --item <id>`. Expect `LAI-SYN-025`, the file quarantined, READY red/yellow, and the next sync restores it.

## 10. Implementation breakdown

1. **SYNC-01 Content roots and ROOT.json** (1 d). Scope: `internal/ark/roots.go`: root type, root id derivation, ROOT.json read/write, internal root creation and checks (`LAI-ARK-020/021`). Acceptance: unit tests for id derivation and ROOT.json round-trip; internal root recreated when missing.
2. **SYNC-02 Ark drive detection** (1.5 d). Depends on: SYNC-01. Scope: mountinfo poll (POLLPRI + 30 s fallback), parser, blkid wrapper, classification (ext4, duplicate UUID, ro, errors_count, uninitialized, writable probe), unmounted-drive scan, `GET /ark/drives`, `drive.*` events. Acceptance: table tests from §9.1; manual step 4/6 on ai-n3.
3. **SYNC-03 Safe eject, init and mount** (1 d). Depends on: SYNC-02. Scope: eject sequence §4.1.5 with blocker discovery via `/proc/*/fd` and the `force` flag, init, Mount via udisksctl, `PUT …/profile`, the routes and bodies of §3.5 (CLI wiring is AICTL-06: `init <mount-path|device|root-id>` resolves the root id via `GET /ark/drives`). Acceptance: eject refuses with a loaded drive-only model; succeeds otherwise; `LAI-ARK-009` lists blockers.
4. **SYNC-04 MANIFEST, items.json journal and BestCopy index** (1.5 d). Depends on: SYNC-01. Scope: schemas §4.11, write-ahead journal, `items.json` atomic writes, rebuild-from-manifests (`LAI-SYN-050`), BestCopy rules §4.2. Acceptance: crash-replay tests; BestCopy preference tests.
5. **SYNC-05 Planner and dry run** (2 d). Depends on: SYNC-04, CAT-01. Scope: pure `Plan()` per §4.4, human and JSON dry-run output, `POST /ark/sync {dry_run}`. Acceptance: every planner row of §9.1 passes; output matches the §4.4.4 golden files.
6. **SYNC-06 Downloader with resume, streaming sha256 and bandwidth limit** (2 d). Depends on: SYNC-04. Scope: HTTP client rules §4.6, checkpoints with marshalled sha256 state, token bucket, retries/backoff §4.8, stall timeout, ENOSPC/min_free handling. Acceptance: httptest suite and SIGKILL-resume integration test pass.
7. **SYNC-07 Item state machine, run lifecycle, verify-reread, swap and quarantine** (2 d). Depends on: SYNC-05, SYNC-06. Scope: §4.5 incl. RENAME_EXCHANGE directory swaps, crash replay and the content-validator hook; quarantine with `reason.json`; the **run lifecycle** of §4.3 (preflight, scheduler, run slot, non-dry-run `POST/GET/DELETE /ark/sync`, `sync.*` events, worker ioprio/nice of §3.7). Acceptance: crash-at-every-state tests; MANIFEST last-write test.
8. **SYNC-08 Retention and prune** (1 d). Depends on: SYNC-07. Scope: `retention.CanDelete` (R1–R9), trash purge, quarantine expiry, `POST /ark/prune` (§3.5) and `aictl ark prune`, the static "no other delete" test. Acceptance: matrix test; prune always refuses the last verified copy (no override).
9. **SYNC-09 Resolvers: huggingface, url, github_release** (1.5 d). Depends on: SYNC-06. Scope: §4.9.1, 4.9.4, 4.9.5 (incl. the computed-version rule and `extract` for `url`), archive extraction safety. Acceptance: fake-server tests incl. mismatch codes `LAI-SYN-021/033/034`.
10. **SYNC-10 Resolvers: kiwix and pmtiles_extract** (1.5 d). Depends on: SYNC-06. Scope: §4.9.2 (listing, sidecar from origin, ZIM MD5 check), §4.9.3 (date probe, dry-run size, extract via throttling proxy, verify). Acceptance: fixtures pass; `pmtiles verify` failure quarantines.
11. **SYNC-11 Resolvers: git_mirror, apt_closure, repo_docs** (2 d). Depends on: SYNC-06. Scope: §4.9.6–4.9.8, subprocess sandboxing, Packages generation, docs render hook, `kit/catalog/` production, HTML-only docs verify. Acceptance: local fixtures pass; `apt_closure` proven to run as an unprivileged user with no writes outside its dir.
12. **SYNC-12 Readiness report** (1 d). Depends on: SYNC-04, SYNC-05. Scope: READY.json §4.12, colour rules (incl. `clock_wrong` and kit-verify rows), `survival_capable`, 6-hourly refresh, `GET /ark`. Acceptance: every colour row tested; golden READY files.
13. **SYNC-13 Re-verify and import** (1.5 d). Depends on: SYNC-07, SYNC-12. Scope: §4.13 budgeted re-verify and `POST /ark/verify`; §4.14 import with network transport disabled, `--add-to-profile`, `--catalog`, `LAI-ARK-015`; triggers `verify`/`import`. Acceptance: bit-rot manual test; import integration test with dial-panic transport.
14. **SYNC-14 Drive extras, units and cloud report** (1 d). Depends on: SYNC-12. Scope: START-HERE template, inventory copy with conflict rule, `localai-sync.timer/.service` templates for the installer, `aictl ark sync --scheduled --wait` exit codes (0 / 8 / 9, skipped = 0, `SuccessExitStatus=8`) and SIGTERM cancel, snapshot `ark`/`sync` blocks and the §4.16 mappings (cloud-app.md §3.6) and start-time job results (cloud-app.md §3.7). Acceptance: START-HERE offline render test; `systemd-analyze verify` on the units; fake cloud receives READY minus drive paths.

## 11. Open questions (with proposed defaults)

1. ~~**New agent routes.**~~ Resolved (2026-11-02): agent.md §3.8 (3.8.20a) lists `POST /ark/drives/{root_id}/{eject|init|mount}`, `PUT /ark/drives/{root_id}/profile` and `POST /ark/prune`; aictl.md §4.10 maps the commands to them.
2. ~~**Catalog extensions**~~ Resolved (2026-11-02): catalog.md §3 now defines `extract`, `extra_repos`, `checksum: computed` + `trust: tofu` (maps data items only) and multi-file `url` items.
3. **Kiwix "latest" without a catalog pin** relies on the HTTPS sidecar alone. *Default:* accept (T7 is satisfied by origin-only sidecar fetch); revisit if Kiwix publishes signed checksums.
4. **Drive min_free default** (2 %, minimum 5 GB). *Default:* as stated; adjustable later via `ROOT.json` if needed.
