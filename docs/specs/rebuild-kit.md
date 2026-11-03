# Spec: Rebuild kit

> **Status:** approved design · **Epic:** E5 · **Code:** `agent/internal/kit/` (verification, builders), `catalog/ark.yaml` (kit items) · **Doctor area:** `LAI-KIT`
> Canonical names: [overview.md](../architecture/overview.md) §3. Download and verification mechanics: [ark-sync.md](ark-sync.md). Building llama.cpp (source location, cmake flags, timeout, smoke test, install, switch and post-switch check): [llama-router.md](llama-router.md) §4.10, which is authoritative; this spec only adds the offline source, preflight and prebuilt extensions. CLI flags: [aictl.md](aictl.md) §4.23. Installer: [installer.md](installer.md).

---

## 1. Purpose and scope

The rebuild kit is everything, besides an Ubuntu install medium, needed to rebuild a working node **with no internet**: source code, toolchains, prebuilt binaries, Ubuntu packages, GPU driver packages, the catalog and the manuals. It lives at `<root>/kit/` on every content root (a per-node subset internally, everything on the ark drive) and is downloaded, verified and refreshed by ark sync like any other content.

In scope: the exact layout and contents; how each part is refreshed; the CUDA toolkit size problem; `aictl rebuild-llama` (and its siblings for whisper and the agent); verification; and the honest list of what can't be rebuilt offline.

Non-goals: building Ubuntu itself; replacing the Ubuntu installer; keeping a full Ubuntu archive mirror; cross-compiling GPU builds for other machines (prebuilt binaries are exported only by the node that built them).

## 2. Concepts

| Term | Meaning |
|---|---|
| **Kit item** | A catalog `ark.yaml` entry with `kind: kit` (always tier 1, catalog.md §3) |
| **Per-node kit** | The subset of kit items an internal root keeps: those matching this node's codename, arch and backend |
| **Pins** | `kit/catalog/pins.json`: the commits and versions every node in the lab currently uses, so the drive keeps sources for all of them |
| **Prebuilt** | A node's own built llama.cpp/whisper.cpp binaries, exported into the kit as tarballs (not from the catalog; produced locally) |

## 3. Interfaces

### 3.1 Layout (§Layout)
```
<root>/kit/
  README.txt                                   plain-text guide: what is here, where to start (generated)
  MANIFEST.json                                index of kit items present (generated from the per-item manifests)
  catalog/                                     item kit-catalog, written by the repo_docs resolver (ark-sync.md §4.9.8) from the same
                                               tag tarball as docs/ (copy of catalog/ at CATALOG_VERSION)
    models.yaml  ark.yaml  profiles/*.yaml  CATALOG_VERSION
    pins.json                                  §Pins (written by the kit, not part of the item: not in its MANIFEST)
    MANIFEST.json
  installers/                                  item kit-agent-binaries (same GitHub release)
    install-node.sh                            the offline installer entry point; once installed it lives at
                                               /opt/localai/installers/install-node.sh (+ /usr/local/sbin symlink, installer.md P2)
  bin/                                         item kit-agent-binaries
    aiagent-linux-amd64  aiagent-linux-arm64
    SHA256SUMS  SHA256SUMS.minisig  minisign.pub
    MANIFEST.json
  src/                                         tarballs, one file each, reproducible (gzip -n)
    llama.cpp-<commit>.tar.gz                  from kit-llama-cpp (git_mirror, also_tarball_of: pinned)
    whisper.cpp-<commit>.tar.gz                from kit-whisper-cpp
    local-ai-<agent-tag>-src.tar.gz            from kit-agent-source: agent source WITH vendor/ AND prebuilt agent/ui/dist/
    MANIFEST.json
  git/
    llama.cpp.git/   whisper.cpp.git/   local-ai.git/       bare mirrors (git_mirror items)
  toolchains/
    go<ver>.linux-amd64.tar.gz  go<ver>.linux-arm64.tar.gz  (kit-go-toolchain; url, pinned sha256 from go.dev/dl JSON)
  tools/
    kiwix-tools/kiwix-tools_linux-x86_64-<ver>.tar.gz   (+ aarch64)       kit-kiwix-tools
    pmtiles/go-pmtiles_<ver>_Linux_x86_64.tar.gz       (+ arm64)        kit-pmtiles
    piper/piper_linux_x86_64.tar.gz                    (+ aarch64)       kit-piper
  desktop/
    ai-desktop_<ver>_amd64.deb   ai-desktop-<ver>-x86_64.AppImage   SHA256SUMS   kit-desktop (github_release)
  debs/<codename>-<arch>/                      kit-debs-<codename>-<arch> (apt_closure)
    pool/*.deb  Packages  Packages.gz  PACKAGES.lock  README.txt  MANIFEST.json
  nvidia/<codename>-<arch>-<branch>/           kit-nvidia-<codename>-<arch>-<branch> (apt_closure) §Driver packages
    pool/*.deb  Packages  Packages.gz  PACKAGES.lock  README.txt  MANIFEST.json
  cuda/<ver>-<arch>/                           optional kit-cuda-redist-<ver>-<arch> (§CUDA, option B; the redistributables do not depend on the Ubuntu release, so no codename)
  prebuilt/
    llama.cpp-<commit9>-<backend>-<codename>-<arch>[-sm<cc>].tar.gz      exported by nodes (§4.2)
    whisper.cpp-<commit9>-<backend>-<codename>-<arch>[-sm<cc>].tar.gz
    MANIFEST.json
  iso/                                         tier 3, drive only: kit-ubuntu-iso
    ubuntu-<ver>-desktop-amd64.iso  SHA256SUMS  SHA256SUMS.gpg
  config/<node-id>/                            NOT a sync item (§Config backups): written only by `aictl backup-config --to-ark`
    localai-config-<node-id>-<UTC-ts>.tar.gz   0600 root; contains secrets unless --no-secrets
```
Where each directory's `MANIFEST.json` comes from, and which directories hold more than one item's files, is §3.2.1.
**Config backups** (§Config backups): `kit/config/` holds the node config backups of [aictl.md](aictl.md) §4.25. It has no MANIFEST, so sync, verify, prune and READY ignore it ([ark-sync.md](ark-sync.md) §4.10 R9); the last 5 backups per node are kept by `aictl backup-config` itself. Because the drive is unencrypted by design ([security-model.md](../architecture/security-model.md) §1 T9), the command warns before writing secrets there.
The **rendered docs** are not duplicated in the kit: they are `<root>/docs/` (item `docs-bundle`, [ark-sync.md](ark-sync.md) §4.9.8), and `README.txt` points to them.

### 3.2 Kit items (catalog `ark.yaml`)
| Item id | Source type | Refresh | Profiles |
|---|---|---|---|
| `kit-catalog` | `repo_docs` variant: the `repo_docs` resolver ([ark-sync.md](ark-sync.md) §4.9.8) writes `kit/catalog/` (`models.yaml`, `ark.yaml`, `profiles/`, `CATALOG_VERSION`, `MANIFEST.json`) from the same `catalog-<V>` tag tarball as `docs/`, as its own item (§3.2.1); `pins.json` of the release is then merged with the node entries of §3.3 | every catalog change | all |
| `kit-agent-binaries` | `github_release` (`marinoscar/local-ai`, tag = the agent version in use; assets `aiagent-linux-*`, `SHA256SUMS*`, `install-node.sh`) | when the agent self-updates | all |
| `kit-agent-source` | `github_release` (asset `local-ai-<tag>-src.tar.gz`, built by the release CI: `git archive` + `go mod vendor` + `npm ci && npm run build` for the UI) | with the agent | all |
| `kit-llama-cpp` | `git_mirror` + tarball of pinned commits | mirror: every run (cheap fetch); tarball: when a pin changes | all |
| `kit-whisper-cpp` | `git_mirror` + tarball | same | all |
| `kit-local-ai-repo` | `git_mirror` (`https://github.com/marinoscar/local-ai.git`) | every run | ark-drive-full |
| `kit-go-toolchain` | `url` (pinned per Go version listed in `agent/go.mod` `toolchain`) | when `go.mod` changes | all (own arch internally) |
| `kit-kiwix-tools`, `kit-pmtiles`, `kit-piper` | `url` / `github_release`, pinned | when the catalog pin changes | all |
| `kit-desktop` | `github_release` (`marinoscar/local-ai`, desktop tag) | with each desktop release | all with a desktop |
| `kit-debs-<codename>-<arch>` | `apt_closure` | `refresh_days: 30` | own codename/arch internally; all on the drive |
| `kit-nvidia-<codename>-<arch>-<branch>` | `apt_closure` | `refresh_days: 30` | CUDA nodes; all on the drive |
| `kit-cuda-redist-<ver>-<arch>` | `url` set (optional, §CUDA option B) | when pinned | off by default |
| `kit-ubuntu-iso-<ver>` | `url` + sidecar `SHA256SUMS` (tier 3) | each point release | ark-drive-full only |

The catalog sizes (`expected_size_bytes`) are approximate and only used for planning:

| Part | Approx. size |
|---|---|
| sources + mirrors (llama.cpp, whisper.cpp, local-ai) | 1.5–2.5 GB |
| Go toolchains (2 arches) | 0.15 GB |
| agent binaries, tools, piper, desktop | 0.6 GB |
| debs closure per codename/arch (incl. Vulkan/Mesa, build tools, tailscale) | 1–2 GB |
| NVIDIA driver closure per branch | 0.4–0.6 GB |
| CUDA toolkit (Ubuntu `nvidia-cuda-toolkit` closure) | 2.5–4 GB |
| prebuilt binaries per node | 0.05–0.5 GB each |
| Ubuntu ISO | 6 GB |

#### 3.2.1 Item directories and manifests (§Item directories)
Every kit item has exactly one **item directory** below `kit/`, the directory that holds its `MANIFEST.json` (ark-sync.md §3.1, §4.11); every file the manifest lists is inside it. `agent/internal/kit` (`kit.LayoutOf`) is the one table; the planner puts the result in the `dir` of each plan row (ark-sync.md §4.4.4) and `ark.ItemDir` places it.

| Kit item | Item directory | Also writes files into |
|---|---|---|
| `kit-catalog` | `kit/catalog/` | |
| `kit-agent-binaries` | `kit/bin/` | `kit/installers/` (`install-node.sh`, a copy of the one in `bin/`, §Open questions 3) |
| `kit-agent-source` | `kit/src/` | |
| any `git_mirror` item (`kit-llama-cpp`, `kit-whisper-cpp`, `kit-local-ai-repo`) | `kit/git/<name>.git/`, `<name>` the URL basename without `.git` (ark-sync.md §4.9.6) | `kit/src/` when `also_tarball_of` is set (below) |
| `kit-go-toolchain` | `kit/toolchains/` | |
| `kit-kiwix-tools`, `kit-pmtiles`, `kit-piper` | `kit/tools/kiwix-tools/`, `kit/tools/pmtiles/`, `kit/tools/piper/` | |
| `kit-desktop` | `kit/desktop/` | |
| `kit-debs-<codename>-<arch>` | `kit/debs/<codename>-<arch>/` | |
| `kit-nvidia-<codename>-<arch>-<branch>` | `kit/nvidia/<codename>-<arch>-<branch>/` | |
| `kit-cuda-redist-<ver>-<arch>` | `kit/cuda/<ver>-<arch>/` | |
| `kit-ubuntu-iso-<ver>` | `kit/iso/` (one ISO series at a time) | |

Rules:
- **No two kit items share an item directory.** `kit.CheckLayout` fails a catalog that does, and a kit id the table cannot place (a fork's extra item) gets no directory: the plan lists it with an empty `dir` and the downloader refuses it.
- **Companion files.** `kit/src/` is the item directory of `kit-agent-source` and its `MANIFEST.json` lists only the agent source tarball. The source tarball of each pinned commit (`llama.cpp-<commit>.tar.gz`) is written by the git mirror's resolver into `kit/src/` beside it, and recorded (file name, size, sha256) in the mirror item's own MANIFEST under `source.tarballs`; `aictl kit verify --part src` checks them from there (`LAI-KIT-001`). `kit/installers/install-node.sh` is recorded the same way in the manifest of `kit-agent-binaries`. The index (§3.6) and `kit verify` therefore never need a manifest that lists files outside its own directory.
- **Several assets per release.** The catalog's `github_release` names one `asset`. `kit-agent-source` is one asset and is a `github_release`. `kit-agent-binaries` (two binaries, the signed checksums, the installer), `kit-desktop` (a `.deb`, an AppImage), `kit-go-toolchain`, `kit-kiwix-tools`, `kit-pmtiles` and `kit-piper` (one file per CPU type) are `url` items with a `files` list: one pinned sha256 per file, the release download URL carrying the tag.
- **`kit-catalog` is a kit item with a `repo_docs` source** (lint accepts that combination for kit items; catalog.md §3). It produces only `kit/catalog/`; the `docs-bundle` item produces only `docs/`; both read the same `catalog-<V>` tarball (ark-sync.md §4.9.8).
- **`kit/MANIFEST.json` and `kit/README.txt` are not items.** They are generated (§3.6); `kit/config/` and `kit/prebuilt/` have no manifest of this kind and are ignored by the index.

### 3.3 Pins (§Pins)
`kit/catalog/pins.json`, written by the cloud into the catalog release (or, unenrolled, by each node into its own roots and merged on the drive):
```json
{
  "schema_version": 1,
  "updated_at": "2026-11-02T00:00:00Z",
  "llama_cpp": [{"commit": "f3586e66b…(40 hex)", "nodes": ["ai-n1", "ai-n2", "ai-n3"]}],
  "whisper_cpp": [{"commit": "<40 hex>", "nodes": ["ai-n3"]}],
  "agent": [{"tag": "node-v0.4.0", "nodes": ["ai-n3"]}],
  "codenames": [{"codename": "resolute", "arch": "amd64", "nodes": ["ai-n1", "ai-n2", "ai-n3"]}],
  "nvidia_branches": [{"branch": "595-open", "codename": "resolute", "arch": "amd64", "nodes": ["ai-n2", "ai-n3"]}]
}
```
The drive keeps a source tarball for **every** pinned commit; an internal root keeps the one for its own node. Pins are merged on the drive: each node that syncs the drive adds its own entries and never removes others' (entries older than 180 days without a node reporting them are dropped).

**Code:** `kit.Pins`, `kit.ParsePins`, `kit.ReadPins`, `kit.WritePins`, `kit.Report`, `kit.Merge`, `kit.UpdatePins` (`agent/internal/kit/pins.go`).
- **Location:** `<root>/kit/catalog/pins.json`, next to the catalog copy but **not part of the `kit-catalog` item**: the kit writes it after the item's swap and the item's MANIFEST does not list it, so merging a node's entries never invalidates the item. The catalog release carries a `pins.json` too (cloud-written); the repo_docs resolver installs it and the kit then merges the node entries into it.
- **Shape:** the example above, plus an optional `reported_at` (RFC 3339 UTC) on every entry: the last time any node reported it. An entry without one ages from the file's `updated_at`. Lists are sorted (commit, tag, codename and arch, branch), node lists are sorted without duplicates, keys are written in the order shown, two-space indent. Valid values: a commit is 40 lowercase hex digits, a codename is a lowercase Ubuntu codename, `arch` is `amd64` or `arm64`, a node id is `[a-z0-9][a-z0-9._-]*`, every entry names at least one node, no entry appears twice. Unknown top-level keys are kept on rewrite; `schema_version` other than 1 is refused and never rewritten.
- **Merge** (`kit.Merge(a, b, now)`): the union of every entry (one entry per commit, tag, codename and arch, or branch, codename and arch; nodes united, the later `reported_at`), minus entries whose `reported_at` is more than 180 days before `now` (exactly 180 days is kept). It never removes an entry because another node stopped using it: a node that moves to a new commit leaves its old entry to age out. It is commutative, idempotent and associative for a fixed `now` (tested over every list); an entry with no time at all (undated entry in an undated file) never expires.
- **Node report:** `kit.Report(NodePins, now)` builds a node's own entries (its llama.cpp and whisper.cpp commits, agent tag, codename and arch, NVIDIA branch); `kit.UpdatePins` reads the root's file (missing is empty), merges and writes it atomically.
- **Damaged file:** a `pins.json` that does not parse, fails validation or is from a newer schema is reported as `LAI-KIT-045` and left exactly as it is (never overwritten by a merge); the next catalog release's copy replaces it.

### 3.4 Keys (§Keys)
Embedded in the agent binary (`agent/internal/kit/keys/`) and also copied to `kit/bin/`:
- `minisign.pub`: the Local AI release signing key (verifies `SHA256SUMS.minisig`). `agent/internal/kit/keys/minisign.pub` is its **only** committed copy: the release workflow verifies with it and stamps it into `install-node.sh`, and the agent embeds it ([release-signing.md](../architecture/release-signing.md)).
- `ubuntu-archive-keyring.gpg` (copied from the build machine's `/usr/share/keyrings/` at release time) for `apt_closure`.
- `tailscale-archive-keyring.gpg` and the NVIDIA CUDA repo key (`nvidia-cuda-archive-keyring.gpg`), for third-party `extra_repos` (these file names are what catalog.md §3 `extra_repos[].key` refers to).
Rotating a key is a catalog/agent release; old keys stay embedded for 2 years so older kits still verify.

### 3.5 CLI
| Command | Notes |
|---|---|
| `aictl kit` / `aictl kit status [--root <id>]` | which kit parts are present and verified for this node, and for every pinned node on a drive |
| `aictl kit verify [--root <id>] [--part <name>] [--full]` | §4.4. `--part` limits the run to one top-level kit part of §3.1: `catalog`, `installers`, `bin`, `src`, `git`, `toolchains`, `tools`, `desktop`, `debs`, `nvidia`, `cuda`, `prebuilt`, `iso`; an unknown name → exit 2 |
| `aictl kit install <tool>` | installs a tool from the kit into `/opt/localai` (`kiwix-tools`, `pmtiles`, `piper`, `agent`), verifying first. The per-tool install functions come from companion-services (SVC-02 kiwix-tools/pmtiles, SVC-08 piper); the CLI dispatcher is KIT-06 |
| `aictl kit install-debs [--root <id>] [--packages …] [--dry-run]` | installs packages from the kit's local apt repo (§Offline apt); verifies first, `--dry-run` changes nothing |
| `aictl kit export-build` | exports this node's current llama/whisper builds into `kit/prebuilt/` on every writable root (§4.2; `rebuild-llama` and `rebuild-whisper` also do it after they switch to a new build) |
| `aictl rebuild-llama …` | §4.3 |
| `aictl rebuild-whisper …` | same flags, for whisper.cpp ([companion-services.md](companion-services.md) §4.2.1, which owns the whisper build target, smoke and command, including `--from-kit` and `--dry-run`; this spec adds `--prebuilt` and export) |
| `aictl rebuild-agent [--from-kit] [--dry-run] [--yes]` | builds the agent from `kit/src/local-ai-<tag>-src.tar.gz` with the kit Go toolchain (§4.3.4) |

All `rebuild-*` and `kit install*` need root (they write `/opt/localai`). They run in-process in `aictl` ([aictl.md](aictl.md) §4.23–§4.23c, which owns the CLI syntax) and use the agent API only to restart the service and take the model lock when the agent runs; when it doesn't they use `systemctl` directly (a rebuild must work even when the agent doesn't).

### 3.6 Generated files: the index and the guide (§Index)
`<root>/kit/MANIFEST.json` and `<root>/kit/README.txt` are written by `agent/internal/kit` (`kit.BuildIndex`, `kit.WriteIndex`, `kit.Regenerate`) and refreshed by `kit.Refresher.AfterSwap`, the hook the sync engine calls after it swaps a kit item into a root (a no-op for items that are not kit items and for read-only roots). Wiring the hook into the engine is the app's job (STARTUP-WIRING); the package only provides it.

**`kit/MANIFEST.json`** is an index, not an item manifest. It starts with `"schema": "localai.kit-index"` and has **no** `schema_version` key, so `kit.IsIndex(bytes)` tells it from an item MANIFEST, an item-manifest parser refuses it, and the scan that finds items (`ark.ScanRoot`) never looks at it (it only looks inside the directories of `kit/`). Keys, in order: `schema`, `index_version` (1), `generated_at` (UTC to the second), `root_id`, `catalog_version` (when known), `total_bytes`, `parts` (`{part, items, total_bytes}` per top-level part present, in the order of §3.1), `items` (one `{id, dir, part, version, catalog_version, total_bytes, files, tree_sha256 (directory items), trust, verified_at}` per kit item whose MANIFEST is valid, sorted by `dir`), `missing` (the ids of the kit items the caller expected, normally the kit items of the root's plan, that are not present or not valid; always present, possibly empty) and `invalid` (`{dir, code}` of every kit directory whose manifest is damaged, incomplete, not verified, from a newer schema or in the wrong place, `LAI-SYN-052`; always present). The same kit always gives the same bytes.

**`kit/README.txt`** is plain ASCII text, lines of at most 78 characters, built from the index only (no host path, no file name of a config backup): what the kit is, the written time and catalog version, **where to start** (the guide `../docs/06-rebuild-a-node-offline.html`, the manual index `../docs/index.html`, `aictl kit status`, `aictl kit verify`, `installers/install-node.sh`), one entry per part of §3.1 with what it holds and whether it is here, and a **needs attention** block that lists missing items and damaged directories. It says that an absent `iso/` is normal off the drive.

Writing: the index first, then the guide, each atomically (temp file, fsync, rename, fsync of the directory), mode 0644; `kit/` is never created by it. A failure is `LAI-KIT-046` (a warning: the kit itself is untouched and the next sync rewrites both).

## 4. Behaviour

### 4.1 Contents in detail (§Contents)

#### 4.1.1 Sources
- **llama.cpp**: bare mirror `kit/git/llama.cpp.git` plus a tarball per pinned commit. The tarball is enough to rebuild; the mirror allows moving to a different commit offline (for instance one that a newer model needs, if it was fetched before the internet went away).
- **whisper.cpp**: same.
- **Agent source**: a release asset made by CI with the vendored Go modules (`vendor/`) **and** the already-built UI (`agent/ui/dist/`), because Node.js and npm packages are not in the kit: rebuilding the agent offline needs only Go.
  The release workflow ([release-node.yml](../../.github/workflows/release-node.yml), KIT-02) builds `local-ai-node-v<semver>-src.tar.gz` with [`scripts/release/build-src-tarball.sh`](../../scripts/release/build-src-tarball.sh): `git archive` of the tag, `go mod vendor` (the committed `agent/vendor/` must already equal it), and the built UI. The archive has **no top-level folder** (extract it straight into the build directory, §4.3.4). It is reproducible (entries sorted bytewise, owner 0:0, every mtime the commit time, modes 0644/0755, `gzip -n`): building twice from one tag gives one sha256, which the workflow asserts. `scripts/check-redaction.sh` runs on the assembled tree before packaging. While `agent/ui/` does not exist yet (the survival portal epic), the tarball carries no UI build and says so; once `agent/ui/package.json` exists, a missing or empty `agent/ui/dist/` fails the release.
  A CI job (`offline-build`, [`scripts/release/offline-build-check.sh`](../../scripts/release/offline-build-check.sh)) extracts the tarball and runs the §4.3.4 build inside `unshare -n` with `GOPROXY=off`, then checks that `aiagent.new version` reports the release version; a module missing from `vendor/` is `LAI-KIT-030` there, before any node meets it.
- **local-ai repo mirror** (drive only): full history, including the docs and catalog sources.

#### 4.1.2 Toolchains
- Go: the exact version from `agent/go.mod` (`toolchain go1.x.y`), `linux-amd64` and `linux-arm64` tarballs from `https://go.dev/dl/` with the sha256 from the `https://go.dev/dl/?mode=json&include=all` listing pinned in the catalog.
- C/C++ (gcc, cmake, ninja, pkg-config, ccache) come from the Ubuntu debs closure.

#### 4.1.3 Binaries
- `aiagent` for both arches with `SHA256SUMS` and its minisign signature; `install-node.sh` of the same release.
- kiwix-tools, pmtiles (go-pmtiles), piper tarballs for both arches (versions pinned in the catalog).
- ai-desktop `.deb` and AppImage (amd64; arm64 when released).
- **Prebuilt llama.cpp and whisper.cpp** per node (§4.2): the fastest recovery when the OS and driver are unchanged.

#### 4.1.4 Ubuntu packages (§Offline apt)
`kit/debs/<codename>-<arch>/` is a flat local repository produced by the `apt_closure` resolver ([ark-sync.md](ark-sync.md) §4.9.7). Its package list (catalog) covers:
- build: `build-essential cmake ninja-build pkg-config ccache git`,
- Vulkan/Mesa: `libvulkan-dev glslc spirv-headers vulkan-tools mesa-vulkan-drivers`,
- tools: `curl jq ca-certificates iw network-manager pciutils usbutils minisign zstd udisks2 openssh-server python3`,
- CUDA (codenames with CUDA nodes): `nvidia-cuda-toolkit` (§CUDA option A),
- `tailscale` (from its repo),
- optional: `gpsd`.
Use offline (also what the installer does):
```bash
REPO=<root>/kit/debs/<codename>-<arch>
aictl kit verify --part debs            # re-hash every .deb against PACKAGES.lock first
echo "deb [trusted=yes] file:$REPO ./" | sudo tee /etc/apt/sources.list.d/localai-ark.list
sudo apt-get update -o Dir::Etc::sourcelist=/etc/apt/sources.list.d/localai-ark.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0
sudo apt-get install -y --no-install-recommends -o Dir::Etc::sourcelist=/etc/apt/sources.list.d/localai-ark.list -o Dir::Etc::sourceparts=- <packages>
sudo rm /etc/apt/sources.list.d/localai-ark.list
```
`aictl kit install-debs` wraps exactly this, in this order and with these additions ([aictl.md](aictl.md) §4.23c): the repositories it will list are verified before the source file exists (`SHA256SUMS`, the `.deb` files against `PACKAGES.lock`, the `Packages` index against the lock, no file apt could prefer over the index); apt's simulated plan must stay inside the verified lock and remove nothing; the source file is removed on every path. Versions in the closure may be **newer** than what's installed on an old system (the closure is taken from `-updates`/`-security`); apt upgrades the dependencies it needs, which is intended.

#### 4.1.5 NVIDIA driver packages (§Driver packages)
One `apt_closure` per `(codename, arch, driver branch)` in the pins, e.g. `kit-nvidia-resolute-amd64-595-open` with packages `nvidia-driver-595-open`, `nvidia-utils-595`, `libnvidia-compute-595`, `nvidia-dkms-595-open`, `dkms`, and `linux-headers-generic` plus the **prebuilt signed module packages for the current kernel flavours** (`linux-modules-nvidia-595-open-generic` and the meta package for the node's kernel series). Prebuilt signed modules matter: they work with Secure Boot and don't need DKMS to compile. DKMS is the fallback when the kernel is newer than the prebuilt modules, and needs the headers for that exact kernel (§5 limits).

#### 4.1.6 CUDA toolkit (§CUDA)
Building llama.cpp/whisper.cpp with CUDA needs `nvcc`, the CUDA runtime headers and cuBLAS. The Ubuntu `nvidia-cuda-toolkit` package (12.4 on the current nodes) pulls a closure of about 2.5–4 GB of debs (about 5–7 GB installed). Options:
| Option | Size | Pros | Cons |
|---|---|---|---|
| **A. Ubuntu `nvidia-cuda-toolkit` closure** (in `kit-debs`) | 2.5–4 GB per codename | same packages the nodes use today; apt handles everything; signed indexes | big; version tied to the Ubuntu release |
| **B. NVIDIA redistributable tarballs** (`developer.download.nvidia.com/compute/cuda/redist/`, only `cuda_nvcc`, `cuda_cudart`, `cuda_cccl`, `libcublas`, `cuda_crt`, pinned by `redistrib_<ver>.json` sha256s) | ~1.2–1.8 GB | smaller; version independent of Ubuntu; checksums published | needs manual `CUDAToolkit_ROOT` wiring; must match driver compatibility |
| **C. Prebuilt binaries only** | ~0.1 GB per node | tiny; fastest recovery | can't build a *different* commit; useless after a driver/OS change that needs a rebuild |

**Decision:** C always (every CUDA node exports its builds), plus **A** on every root that serves a CUDA node (internal budgets of 220 GB absorb 4 GB easily). B is defined as an optional item (`kit-cuda-redist-…`, off by default) for small drives; its builder support is specified in §4.3.2 so it can be turned on without code changes.

#### 4.1.7 Ubuntu ISO (tier 3)
`kit-ubuntu-iso-<ver>`: the desktop ISO of the codename the nodes use, from `https://releases.ubuntu.com/<ver>/`. Verified against `SHA256SUMS`; `SHA256SUMS.gpg` is checked with `gpgv` against the Ubuntu CD image signing key (embedded in the agent like the other keys) when present. Drive only. The runbook explains writing it to a USB stick (`dd` / Startup Disk Creator) — [runbooks/06-rebuild-a-node-offline.md](../runbooks/06-rebuild-a-node-offline.md).

#### 4.1.8 Catalog and docs
`kit/catalog/` is the catalog the root was planned with (so a drive is self-describing), plus `pins.json`. `aictl catalog restore` (catalog.md `LAI-CAT-001`) copies it back to `/etc/localai/catalog/`. The rendered docs are `<root>/docs/`.

### 4.2 Prebuilt export
After every successful build activation (installer, `rebuild-llama`, `rebuild-whisper`), the agent runs `aictl kit export-build`:
1. `tar -C /opt/localai/llama.cpp/builds/<build-dir> -cf - BUILD.json bin [lib] | gzip -n > <root>/kit/prebuilt/.staging/llama.cpp-<commit9>-<backend>-<codename>-<arch>[-sm<cc>].tar.gz` (`-sm<cc>` only for CUDA, e.g. `-sm75`).
2. Record in `kit/prebuilt/MANIFEST.json`: sha256, size, build flags, `llama-server --version` output, CUDA version (`nvcc --version`), driver version, kernel (`uname -r`), node id, built_at.
3. Swap in like any item (atomic rename), on every writable root. Old prebuilts for the same node and backend are kept (max 2).
Restoring a prebuilt: `aictl rebuild-llama --prebuilt <file>` verifies sha256, extracts to `builds/`, runs the smoke test (§4.3.3) and activates. It is offered first when the commit, backend, codename and arch match.

**What the code does (KIT-04).** The archive holds `BUILD.json`, `bin/` and, for a CUDA build with option B, `lib/` (the build's own `BUILD.json` is what makes the file self-describing and verifiable). `build.Request.Prebuilt` (with `PrebuiltSHA256`, the digest the kit's prebuilt MANIFEST records for the archive; `KIT-05` passes it) does the restore in `internal/router/build`: the archive is unpacked into the scratch directory `builds/.prebuilt.tmp` with the one safe extraction of §7 (so a traversal or a link that escapes is refused), then `BUILD.json` is validated (a known backend, a full 40-hex commit whose first 9 characters and backend are the build id, a CUDA architecture for cuda) and the sha256 of every program is recomputed and compared with the one it records. Any failure (a changed program, a missing or invalid `BUILD.json`, a program that is a link, an unsafe archive, a wrong archive digest) is `LAI-KIT-001`, and nothing was put in `builds/`. `--commit` and `--backend`, when given, must agree with the build. Only then the build takes the same path as a fresh one: relocatable check, smoke test (§4.3.3, which also proves the GPU and the glibc match), rename into place, activation with the 60 s / 180 s check and rollback (`LAI-LLM-019`), retention, node record and export. `BUILD.json` is never rewritten, so the exported bytes stay the proof.

**What the code does (KIT-05).** `internal/kit/prebuilt.go` is the kit side; the restore is the engine's (above).

- **The file.** `kit.PrebuiltName` is `<program>-<commit9>-<backend>-<codename>-<arch>[-sm<cc>].tar.gz`; `-sm` is only for CUDA, and several capabilities are joined with `_` (`-sm75_86`). The archive holds `BUILD.json` byte for byte, `bin/` (the programs `BUILD.json` records, in name order) and `lib/` when the build has one (regular files and links to a file of the same folder, nothing else). Every header has mode `0755`/`0644`, owner 0, no names and time 0, and the gzip header has no name or time (`gzip -n`), so the same build always gives the same bytes. It is written as `.staging/<name>.part`, read back and hashed, and renamed into place. An export is refused (`LAI-KIT-049`, nothing written) when a program is missing, is a link or is not the program `BUILD.json` describes by its sha256: a file that would not restore is never exported.
- **The list.** `kit/prebuilt/MANIFEST.json` is its own schema, not an item manifest (a prebuilt is not a sync item, §3.2.1): `{"schema": "localai.prebuilt-manifest", "schema_version": 1, "files": [...]}`, one record per file with `file`, `program`, `commit` (40 hex), `backend`, `codename`, `arch`, `cuda_arch`, `sha256`, `size_bytes`, `cmake_flags`, `version_output` (the smoke test's `--version` output from `BUILD.json`), `cuda_version` (`nvcc` of `BUILD.json`), `driver_version` (`nvidia-smi`, CUDA only), `kernel` (`uname -r`), `node_id` and `built_at`. It is written atomically (temp file, fsync, rename). A damaged list is replaced by the next export; files it recorded are then unrecorded and never offered.
- **Roots.** Every content root that is not mounted read-only (the internal one first) gets the file: the archive is made once, on the first root that can be written, and copied to the others with its digest checked. A root that cannot be written (`EROFS`, `EACCES`) is skipped and reported, not a failure; the export fails only when no root took it, or when one failed for another reason (disk full).
- **Two per program, node and backend.** After a write the newest two files of that program, node id and backend (by `built_at`, the file just written first) are kept. An older one is deleted only when two newer ones exist on disk, so the last copies are never removed, and a record whose file is gone is dropped. `retention.CanDelete` is not used: it protects verified copies of sync items and refuses any other path (R9), and `kit/prebuilt/` is written by the nodes and never read by sync.
- **Which file fits.** `kit.SelectPrebuilt` takes the newest recorded file whose program, backend, Ubuntu release and CPU architecture equal this computer's, whose compute capability equals its GPU's for CUDA, and whose commit matches the one asked for (a prefix of 7 to 40 characters; none asked accepts any). Nothing is approximated: a build for `noble` does not fit `resolute`. A file the kit lists but whose size differs is not offered.
- **`--prebuilt <file>`.** The file must be recorded in the `MANIFEST.json` beside it when it is in a folder called `prebuilt` (no list, a damaged list, no record or another size is `LAI-KIT-001` and nothing changes); a copy kept elsewhere is read from its name, with a warning that only the checksums inside it vouch for it. The computer's backend and compute capability come from the engine's own resolution (`--backend auto` is resolved first). A difference in release, architecture, backend, capability, program or the commit given with `--commit` is `LAI-KIT-048` (exit 1, nothing changed) and the message names each difference and the files the kit does have. Then the engine verifies the archive digest recorded in the list (`Request.PrebuiltSHA256`), `BUILD.json` and every program before anything is placed, runs the smoke test and activates with the same rollback as a build.
- **The offer.** Before it compiles anything, `rebuild-whisper` looks for a file that fits exactly and, at a terminal, asks whether to use it. `--yes`, `--json` and a missing terminal never choose it for the operator: the command prints the `--prebuilt <file>` line and builds as it was asked. `--source` and `--dry-run` skip it.
- **When it runs.** `rebuild-whisper` sets `Options.Exporter` (`newPrebuiltExporter` in `cmd_kit_export_build.go`); a failure is a warning because the build is already in use. `rebuild-llama` gets the same hook when its command surface lands (AICTL-09), with `kit.ProgramLlama`; the installer's first builds are exported by running `aictl kit export-build` after its last activation.

### 4.3 `aictl rebuild-llama` (§rebuild-llama)

Synopsis and flags are owned by [aictl.md](aictl.md) §4.23 (`--cuda-arch <list>`: semicolon-separated compute capabilities such as `75;86`; `--yes`; the global `--json`). The build itself (source location `/opt/localai/llama.cpp/src/<commit>/`, cmake flags including `-G Ninja`, 3 h timeout, smoke test, install, switch, post-switch check, `LAI-LLM-018`/`019`) is [llama-router.md](llama-router.md) §4.10; this section only adds the offline source, the preflight (`--dry-run`) and the prebuilt path. Defaults: commit = `node.yaml: llama.commit`; backend = node backend; cuda-arch = detected compute capability; source = kit tarball if present on any root, else the mirror (`git archive` from `kit/git/llama.cpp.git`), else (online only) GitHub; `--activate` on; keep 2 builds.

#### 4.3.1 Procedure
1. **Preflight** (also the whole of `--dry-run`, which prints each check with ✓/✗ and exits 0 only if all pass):
   - source available and its sha256 matches the one the kit recorded (`LAI-KIT-001`; where it is recorded: [below](#what-the-code-does-kit-04)); for the mirror, `git cat-file -e <commit>^{commit}` (`LAI-KIT-002`);
   - build tools: `gcc`, `g++`, `cmake ≥ 3.21`, `ninja`, `pkg-config` (`LAI-KIT-010`, fix: `aictl kit install-debs`);
   - backend deps: cuda → `nvcc` present and its version ≤ the driver's supported CUDA version (from `nvidia-smi` header "CUDA Version: X.Y") (`LAI-KIT-011`); vulkan → `glslc`, `libvulkan` headers, `vulkaninfo --summary` lists a non-llvmpipe device (`LAI-KIT-012`);
   - GPU driver loaded (cuda: `nvidia-smi` works; `LAI-KIT-013`);
   - disk: 5 GB free in `/opt/localai` (source tree under `llama.cpp/src/` and the `.tmp` build tree under `llama.cpp/builds/`) and 1 GB in `/var/lib/localai/build` (`LAI-KIT-014`);
   - RAM: jobs = min(`--jobs`, nproc, floor(MemAvailable_GiB / 2)), at least 1;
   - **offline simulation of missing packages**: if a tool is missing, `apt-get -s install` against the kit repo (§4.1.4 options) proves it can be installed offline; the dry run prints the exact `aictl kit install-debs --packages …` command.
2. **Extract** the chosen kit source (tarball, or `git archive <commit>` from `kit/git/llama.cpp.git`) into `/opt/localai/llama.cpp/src/<commit>/`, the only source location ([llama-router.md](llama-router.md) §4.10.1); an existing incomplete tree is removed first.
3. **Configure, build, install, smoke test** exactly as [llama-router.md](llama-router.md) §4.10.2–§4.10.3 steps 1–5 (flags, `-G Ninja`, `-DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF`, 3 h timeout, smoke `--version` containing the first 7 hex of the commit, move-aside name `<build-id>.old-<ts>`). Build failure → `LAI-LLM-018`; smoke failure → `LAI-LLM-019` (`current` untouched). The kit adds only the option B wiring of §4.3.2 and the unprivileged compile of §7.
4. **Activate** (unless `--no-activate`): [llama-router.md](llama-router.md) §4.10.3 step 6 (atomic `current` switch, restart through the agent or `systemctl` when the agent is down, post-switch check: `/health` within 60 s, then default model loaded and a 16-token completion, all within 180 s). Failure → switch back, restart, `LAI-LLM-019`.
5. **Record** `node.yaml: llama.commit` (if changed), export the prebuilt (§4.2), prune old builds beyond `--keep` (never the active one or the previous one).

#### 4.3.2 CUDA option B wiring
When `kit/cuda/<ver>-<arch>/` (the directory of §3.1 and `kit.LayoutOf`) exists for this machine's architecture with a CUDA version the driver supports, and `nvcc` is not installed, the builder extracts the redist tarballs to `/var/lib/localai/build/cuda-<ver>/` and adds `-DCUDAToolkit_ROOT=<that dir> -DCMAKE_CUDA_COMPILER=<that dir>/bin/nvcc`; the resulting binary needs `libcudart`/`libcublas` from the same tree at runtime, so they are copied into the build's `bin/../lib/`. The programs carry the run path `$ORIGIN/../lib` (`-DCMAKE_BUILD_RPATH`), so they find those libraries from wherever the build is moved to, with no `LD_LIBRARY_PATH` (the unit template's variable, if it sets one, is then redundant). A system `nvcc` always wins over the redistributable.

#### What the code does (KIT-04)
`internal/router/build` carries the offline half of §4.3 on the same `Manager` as ROUTER-08 ([llama-router.md](llama-router.md) §4.10.4); `internal/kit/source.go` is its lookup across the content roots. The CLI that parses the flags is AICTL-09; the engine takes them as `Request.FromKit`, `DryRun`, `Prebuilt` (§4.2) and `Offline`.

- **Source order.** `KitSources(stem, roots)` is the chain of `--from-kit`: the verified tarball `kit/src/<stem>-<commit>.tar.gz` of the first root that has a good one, then the mirror `kit/git/<stem>.git` of every root (`git archive <commit>`, run with `-c safe.directory=<repo>`); `ChainWithKit` adds the GitHub clone for a node that also builds online (`Request.Offline` drops it). A tree already extracted in `src/<commit>/` is used before any of them, a short commit finds a tarball or a tree by prefix only when exactly one matches, and every source is extracted into `src/<commit>.tmp` and renamed, so the only source location is `/opt/localai/llama.cpp/src/<commit>/`; nothing is built from `/var/lib/localai/build`. A source without the commit is `ErrNotAvailable` and the chain goes on; offline with nothing left is `LAI-KIT-002`.
- **Where the tarball's sha256 is recorded.** The git_mirror resolver records each tarball (path, size, sha256) in the mirror item's MANIFEST under `source.tarballs` ([ark-sync.md](ark-sync.md) §4.9.6), because `kit/src/MANIFEST.json` lists only the agent source (§3.2.1). The lookup reads both (`files` of `kit/src/MANIFEST.json` and `source.tarballs`), refuses a tarball that no manifest names, one named with a malformed digest, and one the two manifests name differently; a mismatch skips that root, and when no root has a good tarball and the mirror cannot deliver the commit the build ends with `LAI-KIT-001`. The digest is computed again over the bytes that are extracted, so a file replaced between the check and the extraction is refused too. The tarball's single top-level directory (`git archive --prefix`) is dropped.
- **The preflight** is `Manager.PreflightReport`: every check is run and reported (`Check{ID, Title, OK, Code, Detail}`), in the order `inputs`, `driver` (cuda; run first because the CUDA comparison needs its answer), `source`, `tools`, `backend`, `disk`, `jobs`, `packages`. The tool names are `gcc`, `g++`, `cmake` (≥ 3.21, from `cmake --version`), `ninja`, `pkg-config` (`LAI-KIT-010`; `git` joins them only when the chosen source needs it); cuda's `nvcc` (`nvcc --version`, release ≤ the driver's `CUDA Version`, or a kit redistributable the driver supports) is `LAI-KIT-011`; vulkan's `glslc`, `vulkaninfo`, `pkg-config --exists vulkan` for the headers and a non-`llvmpipe` device of `vulkaninfo --summary` are `LAI-KIT-012`; no answer from `nvidia-smi` is `LAI-KIT-013` (it is also reported when the CUDA architecture cannot be read because of it, next to `LAI-HW-019`); free space is read on the parent of the build root (5 GB) and on the scratch directory (1 GB, walking up to the nearest directory that exists), `LAI-KIT-014`; a filesystem that cannot be read does not fail the check. `Report.MissingTools`, `Packages` and `InstallCommand` give the exact `sudo aictl kit install-debs --packages cmake,ninja-build` (the list is comma separated, [aictl.md](aictl.md) §4.23c), and `Report.Err()` is the first failure as the error a build ends with. The offline simulation is the `AptSimulator` seam (`Simulate(ctx, packages)`), implemented by `aictl kit install-debs` (KIT-06); without one the check says it was not simulated.
- **`--dry-run`** (`Request.DryRun`) returns the report and changes nothing (no lock, no directory, no source extracted); the error is the first failed check. **`--from-kit`** runs the same preflight first, so a missing tool is `LAI-KIT-010` with the kit command rather than `LAI-LLM-018` with `apt install`, then builds from the kit chain. A build without `--from-kit` keeps the `LAI-LLM-018` prerequisite check of llama-router.md §4.10.4.
- **Unprivileged compile** (§7). With the default `Options.CompileUser` (`localai`; `CompileAsCaller` turns it off for tests and unprivileged callers) `builds/<id>.tmp` and its `bin/` are chowned to that user, configure and build run as `setpriv --reuid localai --regid localai --clear-groups <cmake> …` (errors and the log still name cmake), and as soon as the compile ends `chown -R root:root builds/<id>.tmp` runs, before anything is read or run from it and before the rename. Because the compile user owned the tree, root re-creates `bin/` (a planted link into the system cannot be followed), copies only regular files out of the build tree, and the smoke test and activation run only on root-owned files. A missing user or `setpriv`, or a failed chown, is `LAI-LLM-018` before anything is compiled.
- **Smoke test completion** (§4.3.3): `Options.SmokeModel` returns the smallest verified llama model (AICTL-09 wires it to the catalog); `llama-cli -m <model> -n 8 -p Hello -ngl 99 --no-warmup` (120 s) must exit 0 and print something, else `LAI-LLM-019`; with no model the step is skipped with a warning in `Result.Warnings`.
- **After a switch** `Options.NodeConfig.RecordLlamaCommit` writes `node.yaml: llama.commit` when the commit changed, and `Options.Exporter.ExportBuild(buildID, dir)` is the prebuilt export hook of §4.2 (KIT-05); a failure of either is a warning, never a rollback. A build that was rolled back, or not activated, is neither recorded nor exported.
- **Recipes.** `build.Recipe{Name, CloneURL, Programs, Tools}` (`LlamaRecipe`) is what the offline pieces take per program, so the whisper builder (SVC-05, KIT-05) reuses `KitSources`, `PreflightReport`, `ExtractArchive`, the redistributable wiring and the unprivileged compile with `rebuild-whisper`'s own recipe. The `Manager` takes the recipe: `LlamaRecipe` keeps the layout and activation of llama-router.md §4.10, and SVC-05 added `WhisperRecipe` on the same `Manager` (its flags, targets, scratch-directory source, smoke test, service, doctor codes `LAI-KIT-020` and `LAI-KIT-021` and audit action: [companion-services.md](companion-services.md) §4.2.1). `Recipe` also carries `Root`, `Service`, `Unit`, `Flags`, `Targets`, `ScratchSource`, `KitPreflight`, `AllowAuto`, `BuildFailedCode`, `UnusableCode`, `AuditAction` and `Keep`; `Options` adds `Defaults`, `Services`, `ServiceCheck`, `SmokeAudio` and `RecordCommit` for a recipe whose service is not llama.

#### 4.3.3 Smoke test (shared by all rebuilds)
- `--version` and `--list-devices` checks: [llama-router.md](llama-router.md) §4.10.3 step 3 (output contains the first 7 hex of the commit; expected device, not `llvmpipe`). The same rule applies to whisper.cpp builds with their own binaries.
- `bin/llama-cli -m <smallest verified llama model> -n 8 -p "Hello" -ngl 99 --no-warmup` (timeout 120 s) exits 0 and prints tokens. If no model is available locally, this step is skipped with a warning.

#### 4.3.4 Agent rebuild (`aictl rebuild-agent`)
1. Extract `kit/toolchains/go<ver>.linux-<arch>.tar.gz` to `/var/lib/localai/build/go/`.
2. Extract `kit/src/local-ai-<tag>-src.tar.gz` (`<tag>` is the full release tag, `node-v<semver>`) to `/var/lib/localai/build/local-ai-<tag>/`. The archive has no top-level folder, so the build directory holds `agent/`, `docs/`, … directly.
3. From `/var/lib/localai/build/local-ai-<tag>/agent/` (the Go module is `agent/`; the tree root has no `go.mod`): `GOTOOLCHAIN=local GOFLAGS=-mod=vendor CGO_ENABLED=0 GOPROXY=off /var/lib/localai/build/go/bin/go build -trimpath -ldflags "-s -w -X <module>/internal/app.Version=<semver>" -o ../aiagent.new ./cmd/aiagent`, where `<module>` is the `module` line of `agent/go.mod` and `<semver>` is the tag without `node-v` (no network: `GOPROXY=off` makes it fail fast if anything isn't vendored, `LAI-KIT-030`). The CI job `offline-build` runs exactly this command in `unshare -n` ([§4.1.1](#411-sources)).
4. `aiagent.new version` must report `<semver>` (the same string the release binaries report); then the same swap and rollback rules as self-update ([agent.md](agent.md) §4.10): keep `aiagent.prev`, restart, health check within 60 s.
An offline rebuild of the *same* tag produces a binary equivalent to the release; that's not needed for recovery (the release binary is in `kit/bin/`), but it proves the source is buildable, which the drill checks with `--dry-run` (toolchain and vendor present).

**What the code does (KIT-05).** `build.AgentBuilder` (`internal/router/build/agentbuild.go`) shares the hardened extraction, the unprivileged compile, the build lock and the report type with the program rebuilds; the lookup of the tarball and the toolchain is `kit.SourceFinder.FindAgentSource` and `FindGoToolchain` (`internal/kit/agentsrc.go`). The CLI is `aictl rebuild-agent` ([aictl.md](aictl.md) §4.23b).

- **Which source.** The newest `local-ai-<tag>-src.tar.gz` of the kit by semver (the tag is `node-v<semver>`; there is no flag for another one yet), found on any root, newest first. Its sha256 must equal the one the kit records for it (`kit/src/MANIFEST.json`, `files`); a toolchain's must equal the one in `kit/toolchains/MANIFEST.json`. A file that is not recorded or does not match is skipped for the next candidate; with none left it is `LAI-KIT-001`. The toolchain is the newest `go<ver>.linux-<arch>.tar.gz` for this CPU.
- **`--dry-run` is the checks and nothing else** (no scratch folder, no program run, no audit line): (1) the source is in the kit and matches its digest (`LAI-KIT-040` when absent, `LAI-KIT-001` when damaged); (2) the same for the toolchain; (3) one pass over the tarball with no disk finds `agent/go.mod` with a `module` line, `agent/cmd/aiagent`, `agent/vendor/modules.txt`, every module `go.mod` requires as a header of `modules.txt`, and a folder with files for every package it lists (`LAI-KIT-030` naming what is missing, so removing one vendored package is caught here); (4) the toolchain is at least the `toolchain` (else `go`) line of `go.mod` (`LAI-KIT-030`); (5) the compile user and `setpriv` exist (`LAI-KIT-020`); (6) 1 GB is free in the scratch folder (`LAI-KIT-014`). Exit 0 only if all pass.
- **The build.** The checks run again, then under `/var/lib/localai/build` (a lock of its own): the toolchain is unpacked to `go/` (first path component dropped) and the source to `local-ai-<tag>/`, each through a `.tmp` folder and with the digest computed again over the bytes that are extracted (a file replaced since the lookup is `LAI-KIT-001`); an unsafe archive is `LAI-KIT-001`. The tree is chowned to the compile user, `go build` runs as that user through `setpriv` and the tree is root's again right after. The command is the one above, run as `go build -C <tree>/agent -trimpath -ldflags "-s -w -X <module>/internal/app.Version=<semver>" -o <tree>/aiagent.new ./cmd/aiagent` (`execx` has no working directory, and `-C` is `cd`), with `GOTOOLCHAIN=local GOFLAGS=-mod=vendor CGO_ENABLED=0 GOPROXY=off GOENV=off` and `GOCACHE`, `GOPATH` and `HOME` inside the tree. A failure that says something is not vendored (`GOPROXY=off`, inconsistent vendoring, no module provides a package) is `LAI-KIT-030`; any other is `LAI-KIT-020` with the last 200 lines of the log. The log is `/var/lib/localai/build/agent-build.log`; the toolchain and the tree are removed afterwards, success or not.
- **The test.** `aiagent.new` must be a regular file of at most 200 MB and a static ELF program for this CPU (`debug/elf`: no interpreter, no shared library; nothing is run to find out); it is copied next to the agent, run once (`version --json`: the tag's version, `linux`, this architecture) and a failure of either is `LAI-KIT-021` with nothing replaced.
- **The swap.** The agent in use is copied to `aiagent.prev.tmp` and renamed to `aiagent.prev` (kept for good, ADR-0001), `aiagent.new` is renamed over `aiagent`, and `systemctl restart localai-agent.service` runs (not `--no-block`: this is `aictl`, not the agent). The check polls `GET /health` every 2 s on the injected clock until it reports the new version with `starting` false, for 60 s; if the agent did not answer before the restart, or `systemctl` fails and it did not (a computer rebuilt from scratch has no unit yet), the verified program is installed and the result says it was not restarted (a warning, exit 0). When the agent was running and the restart or the check fails, `aiagent.prev` is copied back over `aiagent`, the agent restarted again and the result is `LAI-KIT-021` with `rolled_back: true`; when the previous one cannot be put back the message says `sudo cp /opt/localai/bin/aiagent.prev /opt/localai/bin/aiagent`. Audit action `agent.build.switch` (target `tag`, `version`, `previous`, `rolled_back`).
- **Differences from the self-update of [agent.md](agent.md) §4.10.** The same files (`aiagent.new`, `aiagent.prev`), the same health rule and the same smoke test, but no `pending.json` and no transient rollback timer: those exist so that a new agent that cannot start is rolled back even though the process that installed it is gone, and here the process that installed it (`aictl`) waits for the answer and rolls back itself. If `aictl` is killed during that minute, recover by hand with the `cp` above.
- **CI.** The `agent-rebuild` workflow builds the source tarball with `scripts/release/build-src-tarball.sh` and runs `TestAgentRealBuild` inside `unshare -n`: the real `go build` with the vendored modules, the version the tag reports, the swap with `aiagent.prev` kept, and a second case in which the new agent never becomes healthy and is rolled back.

### 4.4 Verification (§Verification)
`aictl kit verify` (and the drill):
1. **Integrity:** re-hash every kit file against its MANIFEST (`--full`), or only files not re-verified in `reverify_days` (default). Uses ark-sync's verifier.
2. **Signatures:** `kit/bin/SHA256SUMS.minisig` verifies with the embedded key; `SHA256SUMS` matches the binaries.
3. **Completeness for this node** (`§Per-node kit`): present and verified are
   - the source tarball for `node.yaml: llama.commit` and `whisper.commit`,
   - the agent binary for this arch and the agent source for the running version,
   - the Go toolchain for this arch,
   - `kit-debs-<this codename>-<this arch>` (and `kit-nvidia-…-<this branch>` on CUDA nodes),
   - the CUDA toolkit packages (option A in the debs list) or option B tarballs on CUDA nodes,
   - kiwix-tools, pmtiles, piper for this arch,
   - a prebuilt for this node's current build (warning only if missing).
   Missing → `LAI-KIT-040` naming the parts.
4. **Completeness for the lab** (drive roots): the same for every entry in `pins.json` (`LAI-KIT-041`, yellow).
5. **Consistency:** the debs closure codename equals this node's codename (internal roots), else `LAI-KIT-042` (error, red: the kit is for a different Ubuntu release); `nvcc` version in the closure is supported by the installed driver (`LAI-KIT-043`).
6. **Buildability:** `aictl rebuild-llama --dry-run` passes (all preflight checks, with packages resolvable from the kit repo).
Result: a JSON report `{overall, parts:[{part, status, code?}]}` used by the drill and by READY.json: the kit rows of [ark-sync.md](ark-sync.md) §4.12.2 combine age and codename with this report's completeness and CUDA results (`LAI-KIT-040` red on the internal root and yellow on a drive, `LAI-KIT-041` yellow, `LAI-KIT-042` red on the internal root and yellow on a drive, `LAI-KIT-043` yellow).

## 5. Limits: what can't be rebuilt offline

Plain list, also printed in the rebuild runbook:
| Situation | Why it can't be fixed offline | Mitigation |
|---|---|---|
| **Kernel upgraded, NVIDIA module missing for it** (an update installed a new kernel, the kit has no prebuilt module for it, and no headers for that exact kernel) | DKMS needs the headers of the running kernel; the kit has only what was current at sync time | Boot the previous kernel from the GRUB "Advanced options" menu (Ubuntu keeps one or two); the kit's prebuilt modules match it. The runbook explains this first |
| **Ubuntu release upgraded** (e.g. 26.04 → 28.04) | The debs closure and driver packages are for the old codename; prebuilt binaries may link a newer glibc/libstdc++ | Don't do release upgrades on an ark node without internet. A drive synced after the upgrade fixes it |
| **CUDA toolkit newer than the driver** | `nvcc` builds code the old driver can't run ("CUDA driver version is insufficient") | The kit pins toolkit and driver from the same Ubuntu release; verify step 5 checks it |
| **A GPU newer than the toolkit** (new compute capability) | The old `nvcc` can't target it | Use the Vulkan or CPU backend; the kit includes Vulkan/Mesa |
| **A new model architecture** | Needs a newer llama.cpp than any commit in the mirror | None offline. The catalog only includes models that run on the pinned commit |
| **Secure Boot with DKMS-built modules** | Needs MOK enrollment at the next boot with a keyboard | Prefer the prebuilt signed module packages (§4.1.5); the runbook covers MOK |
| **Firmware/BIOS problems, dead disk, dead GPU** | Hardware | The ark drive + another machine + the ISO (tier 3) |
| **Desktop app on a different arch** | Only amd64 builds exist today | Use any browser at `http://127.0.0.1:7070` |
| **Agent UI changes** | Rebuilding the UI needs Node/npm, not in the kit | The agent source tarball contains the built UI; UI changes need internet |

## 6. Failure handling and edge cases

| Situation | Behaviour | Code |
|---|---|---|
| Source tarball hash mismatch | refuse; use mirror if it has the commit | `LAI-KIT-001` |
| Commit not in mirror | refuse | `LAI-KIT-002` |
| Build tools missing | dry run prints the offline install command | `LAI-KIT-010` |
| nvcc missing or too new for driver | refuse CUDA build; suggest vulkan/cpu | `LAI-KIT-011` |
| Vulkan only shows llvmpipe | refuse vulkan build | `LAI-KIT-012` |
| NVIDIA driver not loaded | refuse CUDA build; runbook: boot previous kernel | `LAI-KIT-013` |
| Not enough disk for the build | refuse | `LAI-KIT-014` |
| Compile error | keep logs ([llama-router.md](llama-router.md) §4.10) | `LAI-LLM-018` (`LAI-KIT-020` for whisper/agent kit builds) |
| Smoke test fails | `current` untouched | `LAI-LLM-019` (`LAI-KIT-021` for whisper builds) |
| New build fails after activation | rollback | `LAI-LLM-019` |
| Ready-made build for another Ubuntu release, CPU, backend, graphics chip or commit | refuse, name every difference and the files the kit has | `LAI-KIT-048` |
| Ready-made build the kit's list does not record, or whose size changed | refuse; nothing changed | `LAI-KIT-001` |
| New agent not healthy within 60 s | `aiagent.prev` put back and the agent restarted | `LAI-KIT-021` |
| A build cannot be exported (no writable root, disk full, not the program `BUILD.json` describes) | the build stays in use; a warning after a rebuild | `LAI-KIT-049` |
| Agent source not fully vendored | fails with GOPROXY=off | `LAI-KIT-030` |
| Kit incomplete for this node / for the lab | READY red / yellow | `LAI-KIT-040` / `041` |
| Kit debs for another codename | red (error) | `LAI-KIT-042` |
| Toolkit/driver mismatch | red | `LAI-KIT-043` |
| Signature check of agent binaries fails | refuse to install from the kit | `LAI-KIT-044` |
| Power loss mid-build | scratch dir is discarded on the next run; `current` never pointed at a partial build | — |

## 7. Security considerations
- Every kit artifact is verified before use: catalog pins, signed apt indexes, minisign for agent binaries, GitHub digests where available.
- The agent never executes anything from the kit automatically; only explicit root commands (`rebuild-*`, `kit install*`, the installer) do, and they verify first.
- Builds run as root (they install to `/opt`) but the compile itself runs as the `localai` user (the `/opt/localai/llama.cpp/builds/<build-id>.tmp/` tree is created owned by `localai` for the compile and `chown -R root:root` before the rename; scratch for whisper/agent builds stays in `/var/lib/localai/build`) (dropped with `setpriv --reuid localai --regid localai --clear-groups`), so a malicious build script can't write system files. Root takes the tree back before it reads, runs or renames anything from it, and never follows a link the compile left ([KIT-04 details](#what-the-code-does-kit-04)).
- Every archive of the kit is unpacked by one extraction (`build.ExtractArchive`) that refuses absolute names, `..`, hard links and special files, a symlink that is absolute, climbs out of the tree or is not in cleaned form (`a/../b` is how a chain of links escapes), any entry whose parent is a symlink, and a file or directory that would replace a symlink.
- Signing keys are embedded and also shipped in the kit for humans; the embedded copies are authoritative.

## 8. Doctor codes (shown in the desktop; English and Spanish)

| Code | Sev | Meaning | Fix (en) | Fix (es) |
|---|---|---|---|---|
| LAI-KIT-001 | error | A source package in the kit is damaged | Run `aictl kit verify --full`; sync the drive on a computer with internet. | Ejecute `aictl kit verify --full`; sincronice el disco con internet. |
| LAI-KIT-002 | error | The kit doesn't have the needed llama.cpp version | Sync the kit with internet, or choose a commit that is in the kit. | Sincronice el kit con internet, o elija una versión que esté en el kit. |
| LAI-KIT-010 | error | Build tools are missing | Run `sudo aictl kit install-debs`. | Ejecute `sudo aictl kit install-debs`. |
| LAI-KIT-011 | error | The CUDA compiler is missing or doesn't match the driver | Run `sudo aictl kit install-debs`, or rebuild with `--backend vulkan`. | Ejecute `sudo aictl kit install-debs`, o reconstruya con `--backend vulkan`. |
| LAI-KIT-012 | error | No real Vulkan graphics device was found | Install the graphics driver from the kit, or use `--backend cpu`. | Instale el controlador gráfico del kit, o use `--backend cpu`. |
| LAI-KIT-013 | error | The NVIDIA driver isn't loaded | Restart and choose the previous kernel in "Advanced options", or install the driver from the kit. | Reinicie y elija el kernel anterior en "Opciones avanzadas", o instale el controlador del kit. |
| LAI-KIT-014 | error | Not enough disk space to build | Free 5 GB and try again. | Libere 5 GB e intente otra vez. |
| LAI-KIT-020 | error | The build failed | Read the build log shown; run `aictl rebuild-llama --dry-run`. | Lea el registro mostrado; ejecute `aictl rebuild-llama --dry-run`. |
| LAI-KIT-021 | error | The new build doesn't work; the old one is still in use | Nothing breaks. Report the log. | Nada se dañó. Reporte el registro. |
| LAI-KIT-022 | retired (v0.1) | The new build failed after switching; the old one was restored (replaced by `LAI-LLM-019`) | No longer used. | Ya no se usa. |
| LAI-KIT-030 | error | The agent source in the kit isn't complete | Sync the kit with internet. | Sincronice el kit con internet. |
| LAI-KIT-040 | warning | The rebuild kit is missing parts for this computer | Run `aictl ark sync` with internet, or `aictl ark import` from a good drive. | Ejecute `aictl ark sync` con internet, o `aictl ark import` desde un buen disco. |
| LAI-KIT-041 | warning | The ark drive lacks kit parts for another lab computer | Plug the drive into that computer while online, or sync it here. | Conecte el disco a esa computadora con internet, o sincronícelo aquí. |
| LAI-KIT-042 | error | The kit is for a different Ubuntu version | Sync the kit after the upgrade, with internet. | Sincronice el kit después de la actualización, con internet. |
| LAI-KIT-043 | warning | The kit's CUDA version doesn't fit this driver | Sync the kit with internet; meanwhile use the prebuilt or Vulkan. | Sincronice el kit con internet; mientras tanto use el precompilado o Vulkan. |
| LAI-KIT-044 | critical | The kit's agent files failed their signature check | Don't install them. Use another drive and report it. | No los instale. Use otro disco y repórtelo. |
| LAI-KIT-045 | warning | The kit's list of pinned versions is damaged or newer than this agent | Nothing breaks. Sync the catalog with internet; it brings a fresh list. | Nada se dañó. Sincronice el catálogo con internet; trae una lista nueva. |
| LAI-KIT-046 | warning | The kit's index and guide could not be refreshed | The kit itself is fine. Run `aictl ark sync`; it rewrites them. | El kit está bien. Ejecute `aictl ark sync`; los reescribe. |
| LAI-KIT-047 | error | Installing from the kit failed | Nothing was replaced. Read the message, then run `aictl kit verify --full` and try again. | No se reemplazó nada. Lea el mensaje, ejecute `aictl kit verify --full` e intente otra vez. |
| LAI-KIT-048 | error | The ready-made build doesn't fit this computer | Nothing was changed. Build from the kit source instead: `sudo aictl rebuild-llama --from-kit` (or `rebuild-whisper`). | No se cambió nada. Compile desde el código fuente del kit: `sudo aictl rebuild-llama --from-kit` (o `rebuild-whisper`). |
| LAI-KIT-049 | error | A build could not be exported to the kit | The kit's other files are untouched. Free some space on the drive, then run `sudo aictl kit export-build` again. | Los demás archivos del kit no se tocaron. Libere espacio en el disco y ejecute `sudo aictl kit export-build` otra vez. |

## 9. Test plan

### 9.1 Unit
- Completeness rules (§4.4 step 3–5) for fixtures of ai-n1 (vulkan), ai-n2/n3 (cuda) and a drive with all pins.
- Pins merge (add, keep others, drop stale after 180 d; commutative, idempotent and associative; a damaged or newer file is never overwritten; every write step failing leaves the old file) and the pins file read/write.
- Kit layout (`kit.LayoutOf`: every §3.1 path, unknown ids, path traversal in a mirror URL, codename, arch or branch, no two items in one directory), the kit items of each profile (small-8gb, cuda-12gb, max-128gb, cpu-only, ark-drive-full), the dry-run plan of each into its directory (golden files in `agent/internal/kit/testdata/golden/plan-*.json`) and the planner's codename/arch filter.
- Index and guide (golden `index.json`, `README.txt`): items from fixture manifests, missing and invalid ones, regeneration after a swap, determinism, the index is not an item manifest.
- `rebuild-llama` preflight matrix with fake exec (tools present/missing, nvcc vs driver versions, llvmpipe only).
- Activation rollback state machine with a fake systemd and a failing health check.
- Prebuilt naming with and without `-sm`, the MANIFEST record fields, the reproducible archive, the keep-two rule per program, node and backend (never the last copies), read-only roots, a build that does not match its `BUILD.json`, the selection and refusal tables (release, CPU, backend, capability, commit; what the kit has is named), a file the list does not record, and the export → restore round trip for llama.cpp and whisper.cpp with a flipped byte refused.
- Agent rebuild with fake exec: happy path and swap, dry run (toolchain, source, digest, vendor, Go version, compile user, disk), compile and vendor failures, the static check, the start-up test, rollback (never healthy, old version, restart fails, no earlier agent), no agent running, the lock, cancellation, unsafe and replaced archives, and the real `agent/` module passing the completeness check.

### 9.2 Integration
- CI (amd64, no GPU): build llama.cpp `cpu` from a kit tarball with networking disabled (`unshare -n`), smoke test with a tiny GGUF, activation and rollback (inject a failing `llama-server` wrapper).
- `rebuild-agent` in `unshare -n` from a CI-built source tarball: binary version matches, swap, and rollback of a new agent that is never healthy (`agent-rebuild` workflow, `TestAgentRealBuild`).
- Offline apt: install `cmake` from a fixture kit repo in a clean container with no network.

### 9.3 Manual on ai-n3
1. `aictl kit status` → all parts for `resolute/amd64/cuda sm75` present.
2. `sudo aictl rebuild-llama --dry-run` → every check ✓.
3. Network off: `sudo aictl rebuild-llama --from-kit --no-activate` → build completes (~15–25 min), smoke test passes.
4. `sudo aictl rebuild-llama --from-kit` → activates; `curl -s http://127.0.0.1:8080/health` OK; `ls -l /opt/localai/llama.cpp/current`.
5. `aictl kit verify --full` → green.

## 10. Implementation breakdown

1. **KIT-01 Kit items and layout in the catalog** (1 d). Scope: all §3.2 entries in `catalog/ark.yaml` with pins and sizes; `pins.json` schema and merge; `kit/README.txt` and `kit/MANIFEST.json` generation. Acceptance: catalog lint passes; ark-sync plans them into the §3.1 layout.
2. **KIT-02 Release CI artifacts** (1 d). Scope: release job producing `aiagent-linux-*`, `SHA256SUMS`, minisign signature, `install-node.sh`, and `local-ai-<tag>-src.tar.gz` with `vendor/` and built UI. Acceptance: `rebuild-agent` in `unshare -n` works from the asset.
3. **KIT-03 Kit verification and status** (1.5 d). Depends on: KIT-01. Scope: §4.4 incl. per-node and lab completeness, codename and CUDA consistency, `aictl kit status|verify`. Acceptance: fixture tests for 3 node types.
4. **KIT-04 Offline extensions to `rebuild-llama`** (2 d). Depends on: ROUTER-08. Split: ROUTER-08 owns the build engine (configure, build, smoke, switch, post-switch check, rollback; [llama-router.md](llama-router.md) §4.10); AICTL-09 owns the `aictl rebuild-llama` command surface ([aictl.md](aictl.md) §4.23); this item adds the offline source resolution and extraction into `/opt/localai/llama.cpp/src/<commit>/`, `--from-kit`, `--prebuilt`, `--dry-run` kit preflight (§4.3.1 step 1), option B wiring and the unprivileged compile. It does not write a second builder. Acceptance: CI offline cpu build and rollback test; manual on ai-n3.
5. **KIT-05 `rebuild-whisper` kit flags, `rebuild-agent`, prebuilt export/restore** (1.5 d). Depends on: KIT-04. Scope: §4.2, §4.3.4, `aictl kit export-build`, `--prebuilt`; for `rebuild-whisper` only `--prebuilt` and export (SVC-05 owns the whisper build target, smoke, unit and the `aictl rebuild-whisper` command with `--from-kit` and `--dry-run`, [companion-services.md](companion-services.md) §10). Acceptance: export → restore round trip; agent rebuild offline.
6. **KIT-06 Offline apt and `kit install*`** (1 d). Scope: §4.1.4 `aictl kit install-debs`, the `aictl kit install <tool>` CLI dispatcher (calls the install functions provided by SVC-02 for kiwix-tools/pmtiles and SVC-08 for piper; `agent` from `kit/bin/`); driver closure items. AICTL-10 only adds `kit status|verify|export-build` and `verify-release`. Acceptance: container test with no network.

## 11. Open questions (with proposed defaults)

1. **CUDA option B by default on drives?** *Default:* no (option A on all roots serving CUDA nodes). Revisit if the drive budget gets tight.
2. **Who writes `pins.json` when not enrolled?** *Default:* each node adds itself when syncing a drive (§3.3); enrolled labs get it from the cloud catalog release.
3. **Signed checksums and the installer in an item directory.** `kit/bin/SHA256SUMS` (with its `.minisig`), `kit/desktop/SHA256SUMS` and `kit/iso/SHA256SUMS` are release files, but `manifest.ValidFilePath` reserves the name `SHA256SUMS` (and `MANIFEST.json`) in an item manifest's file list, because directory items keep their own `SHA256SUMS` there. Until this is settled the catalog lists these files in the `url` source and the resolver (SYNC-10) must store them under a name the manifest accepts or make the item a directory item; `aictl kit verify-release` (KIT-03) needs the signed bytes under their release names. *Default:* directory items for `kit-agent-binaries` and `kit-desktop`, with the signed release `SHA256SUMS` kept beside the item's own sums file by the resolver. Needs a decision before SYNC-10 and KIT-03.
4. **Real pins.** The agent, desktop, pmtiles and CUDA redistributable entries of `catalog/ark.yaml` need the upstream digests, and the repository's redaction check (`scripts/check-redaction.sh`) rejects any 64-hex string outside its fixtures, so a real `sha256` pin needs a path-scoped allowance for `catalog/*.yaml` first. Until then the kit items live in the fixture catalog of `agent/internal/kit/testdata/catalog/` with `@sha:<digit>@` tokens.
