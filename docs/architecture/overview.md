# Architecture overview

> **Status:** approved design (2026-11-02). Implementation is tracked in [ROADMAP](../ROADMAP.md).
> This document is the **single source of truth for canonical names**: paths, ports, units, users, token prefixes and URLs. Every other doc links here instead of restating them.

## 1. Goals and non-goals

### Goals
1. **Operate the lab.** Every AI node is managed from one place, `https://ai.marin.cr`. You can see health, load or swap models, manage API keys, check ark readiness, switch modes, and add new nodes. The lab grows by adding nodes, and adding one is a guided, mostly automatic procedure.
2. **One API for all intelligence.** `https://ai.marin.cr/v1` is an OpenAI-compatible gateway. It routes each request by model name to a node that serves that model, and it uses per-user keys.
3. **Every node is a standalone ark.** With **no internet at all** (no DNS, no Tailscale, no cloud), every node can still:
   - chat, code and do agentic work with local LLMs,
   - understand images (vision LLM),
   - listen and speak (speech-to-text and text-to-speech),
   - search an offline library (Wikipedia, medical, how-to, repair, survival),
   - show offline maps,
   - show its own manuals,
   - rebuild its own inference software from a local kit.
4. **Non-experts can run it.** The `ai-desktop` app and the built-in **Survival Portal** are operable by anyone at the keyboard. Each dangerous action is protected by an admin PIN.
5. **Always ready.** Scheduled jobs keep models and content current while internet exists. They verify checksums and **never** delete the last good copy. A monthly drill proves the node still works offline.

### Non-goals
- **No LAN mesh in offline mode.** A node offline is standalone ([ADR-0006](../decisions/0006-standalone-offline.md)). The online lab still uses Tailscale between the VPS and the nodes.
- **No Windows or macOS.** All nodes run Ubuntu LTS. When the MSI node boots into Windows it is simply "offline" to the lab.
- **No re-implementation of chat UIs.** The llama.cpp built-in WebUI is the chat interface, and Kiwix is the library UI. We launch them; we don't rebuild them.
- **No training or fine-tuning.**

## 2. Components

```
                         INTERNET (may disappear at any time)
 ┌──────────────────────────────────────────────────────────────────────────────┐
 │ VPS (prod-server)                                                            │
 │  proxy-nginx ──► ai.marin.cr ──► Local AI cloud app (EAB: web + api + db)    │
 │      │                             ├─ Lab control UI (nodes, models, ark)    │
 │      │                             ├─ /v1 gateway (OpenAI-compatible)        │
 │      │                             └─ node plane /api/nodes/* (nod_ creds)   │
 │      ├─► ai-n1/n2/n3.marin.cr ──Tailscale──► node llama-server (per node)    │
 │      └─► chat.marin.cr (Open WebUI) ──► ai.marin.cr/v1                       │
 └──────────────▲───────────────────────────────────────────────────────────────┘
                │ outbound HTTPS only (agent → cloud): register, heartbeat,
                │ claim command jobs, report results   (ADR-0003)
 ┌──────────────┴───────────────────────────────────────────────────────────────┐
 │ AI NODE (Ubuntu, GPU)                                     works fully offline│
 │                                                                              │
 │  aiagent (root, 127.0.0.1:7070) ── the single source of truth on the node    │
 │   ├─ REST API /api/v1/*  (token auth)      ◄── aictl CLI                     │
 │   ├─ Local UI + Survival Portal  /          ◄── ai-desktop (Electron shell)  │
 │   ├─ Maps viewer /maps, Manuals /docs, Speech /api/v1/speech/*               │
 │   ├─ cloud link (EAB worker-node protocol)                                   │
 │   ├─ ark sync engine (systemd timer)                                         │
 │   └─ supervises:                                                             │
 │       localai-llama.service   llama-server ROUTER mode 127.0.0.1:8080        │
 │       localai-kiwix.service   kiwix-serve 127.0.0.1:8888                     │
 │       localai-whisper.service whisper.cpp server 127.0.0.1:8081              │
 │       piper (exec'd per request by the agent)                                │
 │                                                                              │
 │  tailscale serve (online mode only) ──► 127.0.0.1:8080                       │
 │  /srv/localai  (internal ark content)    /media/*/AI-ARK/localai (ark drive) │
 └──────────────────────────────────────────────────────────────────────────────┘
```

| Component | What it is | Spec | Epic |
|---|---|---|---|
| `aiagent` | Go daemon on every node. It owns config, services, models, sync, modes and the local UI. It runs as a **multi-call binary**: invoked as `aictl`, it acts as the CLI. | [agent.md](../specs/agent.md) | E1 |
| `aictl` | CLI client of the agent API, for humans and scripts | [aictl.md](../specs/aictl.md) | E1 |
| Catalog | YAML data describing models, ark content and node profiles | [catalog.md](../specs/catalog.md) | E2/E3 |
| Router | llama-server in router mode. Its presets are generated by the agent | [llama-router.md](../specs/llama-router.md) | E2 |
| Ark sync | Scheduled downloader, verifier and reporter for internal disk and the ark drive | [ark-sync.md](../specs/ark-sync.md) | E3 |
| Companion services | kiwix-serve, whisper.cpp, piper, maps | [companion-services.md](../specs/companion-services.md) | E4 |
| Survival Portal | Big-button offline home page, served by the agent | [survival-portal.md](../specs/survival-portal.md) | E4 |
| Installer | `install-node.sh`, online and offline (from the ark drive) | [installer.md](../specs/installer.md) | E5 |
| Rebuild kit | Everything needed to rebuild a node with no internet | [rebuild-kit.md](../specs/rebuild-kit.md) | E5 |
| Cloud app | `ai.marin.cr`, built on EnterpriseAppBase | [cloud-app.md](../specs/cloud-app.md) | E6 |
| Gateway | `ai.marin.cr/v1`, routing OpenAI-compatible traffic | [gateway.md](../specs/gateway.md) | E7 |
| ai-desktop | Electron shell over the agent UI, with an optional cloud link | [desktop.md](../specs/desktop.md) | E8 |
| Drill | Monthly offline self-test | [drill.md](../specs/drill.md) | E9 |

## 3. Canonical names (single source of truth)

### 3.1 Users and groups
| Name | Kind | Purpose |
|---|---|---|
| `root` | user | Runs `aiagent`. The agent must manage systemd units, `/etc/localai` and `tailscale serve`. All of its child services drop privileges |
| `localai` | system user + group | Runs `localai-llama`, `localai-kiwix` and `localai-whisper`, and owns `/srv/localai` and `/var/lib/localai`. Created with `useradd --system --home-dir /var/lib/localai --shell /usr/sbin/nologin localai`. Supplementary groups `video` and `render` give it GPU access |
| `localai-operators` | group | Humans (and the ai-desktop app running as them) allowed to read the agent token. The installer adds the primary desktop user |

### 3.2 Filesystem
| Path | Owner / mode | Contents |
|---|---|---|
| `/opt/localai/bin/aiagent` | root 0755 | The agent binary (static) |
| `/usr/local/bin/aictl` | symlink → `/opt/localai/bin/aiagent` | CLI (multi-call) |
| `/opt/localai/installers/install-node.sh` | root 0755 | The installer, copied here by every install/repair run so doctor fixes can run `sudo install-node.sh --repair` ([installer.md](../specs/installer.md)) |
| `/usr/local/sbin/install-node.sh` | symlink → `/opt/localai/installers/install-node.sh` | On root's `PATH` (and sudo's `secure_path`), so the fix text works verbatim |
| `/opt/localai/llama.cpp/builds/<commit>-<backend>/` | root 0755 | Built llama.cpp (`bin/llama-server`, `bin/llama-cli`, `bin/llama-bench`) |
| `/opt/localai/llama.cpp/current` | symlink | The active build. The unit always uses `current/bin/llama-server` |
| `/opt/localai/whisper.cpp/{builds/<commit>-<backend>,current}` | root 0755 | whisper.cpp builds |
| `/opt/localai/piper/{<version>,current}` | root 0755 | Piper TTS binary + its libs |
| `/opt/localai/kiwix-tools/{<version>,current}` | root 0755 | `kiwix-serve`, `kiwix-manage` (static release binaries) |
| `/opt/localai/pmtiles/{<version>,current}` | root 0755 | `pmtiles` CLI (go-pmtiles static release binary), used by sync for map extracts and `pmtiles verify` ([ark-sync.md](../specs/ark-sync.md) §4.9) |
| `/etc/localai/` | root:localai 0750 | Configuration |
| `/etc/localai/node.yaml` | root:localai 0640 | Node identity and settings ([agent.md](../specs/agent.md) §3.4). The previous version is kept as `node.yaml.bak` on every agent write |
| `/etc/localai/hf-token` | root:root 0600 | Optional Hugging Face token, used by sync only for `huggingface.co` ([ark-sync.md](../specs/ark-sync.md) §4.9.1) |
| `/etc/localai/api-key` | root:root 0600 | llama-server API key (64 hex). Injected with systemd `LoadCredential`. **Legacy path** `/etc/llama-qwen/api-key` becomes a symlink to it after migration |
| `/etc/localai/agent-token` | root:localai-operators 0640 | Local agent API token (`lat_…`) |
| `/etc/localai/cloud-credential` | root:root 0600 | EAB node credential (`nod_…`). Absent means the node is not enrolled |
| `/etc/localai/admin-pin` | root:root 0600 | argon2id hash of the admin PIN |
| `/etc/localai/models.ini` | root:localai 0640 | **Generated** router preset file. Never hand-edit it |
| `/etc/localai/catalog/` | root:localai 0750 | The catalog version the node is using (`models.yaml`, `ark.yaml`, `profiles/`). A `TEST` marker file means a draft catalog installed with `aictl catalog use <dir> --test` ([aictl.md](../specs/aictl.md) §4.11.4) |
| `/etc/localai/catalog.prev/` | root:localai 0750 | The catalog that `aictl catalog use --test` replaced; `aictl catalog restore` puts it back ([aictl.md](../specs/aictl.md) §4.11) |
| `/var/lib/localai/` | localai 0750 | State. `state.json` (mode, last sync, …), `sync/` (journal and item state; the `.part` files themselves live in `<root>/.staging/` so the final rename stays on one filesystem), `drill/` (results), `cloud/` (link state), `kiwix/` (merged library), `install/` (installer phase state), `build/` (scratch tree for offline rebuilds) |
| `/var/lib/localai/audit.jsonl` | root 0600 | Append-only audit log, rotated `.1`…`.5` ([agent.md](../specs/agent.md) §4.11) |
| `/var/lib/localai/update/` | root 0700 | Self-update staging and the in-progress marker `pending.json` ([agent.md](../specs/agent.md) §4.10) |
| `/var/lib/localai/llama-cache/` | localai 0750 | `LLAMA_CACHE` of llama-server, kept **empty** ([llama-router.md](../specs/llama-router.md) §3.1) |
| `/var/lib/localai/bench/` | root 0755 | `aictl bench` results ([llama-router.md](../specs/llama-router.md) §4.13) |
| `/var/lib/localai/build/` | root 0755 | Scratch of the kit builds: the unpacked CUDA redistributables and whisper.cpp source tree, and for `aictl rebuild-agent` the Go toolchain and the source tree (all removed after the build). Keeps `agent-build.log` and the lock `.agent-build.lock` ([rebuild-kit.md](../specs/rebuild-kit.md) §4.3.4) |
| `/var/lib/localai/migration/llama-qwen/` | root 0700 | Legacy-migration plan, progress and backups ([llama-router.md](../specs/llama-router.md) §4.9) |
| `/opt/localai/bin/aiagent.prev` | root 0755 | Previous agent binary, kept for rollback ([agent.md](../specs/agent.md) §4.10) |
| `/opt/localai/llama.cpp/src/<commit>/` | root 0755 | llama.cpp source trees used for builds ([llama-router.md](../specs/llama-router.md) §4.10) |
| `/etc/systemd/system/localai-llama.service.d/50-env.conf` | root 0644 | **Generated** environment drop-in for llama-server ([llama-router.md](../specs/llama-router.md) §3.3) |
| `/var/lib/localai/kiwix/library.xml` | localai 0644 | **Generated** merged Kiwix library over every mounted content root. `localai-kiwix` serves it ([companion-services.md](../specs/companion-services.md) §kiwix-serve) |
| `/etc/localai/whisper.env` | root:localai 0640 | **Generated** environment for `localai-whisper` (model path, threads, GPU on/off) ([companion-services.md](../specs/companion-services.md) §whisper) |
| `/etc/localai/inventory.local.yaml` | root:root 0600 | Optional. The real lab inventory on an owner node. Sync copies it to the ark drive ([ark-sync.md](../specs/ark-sync.md) §4.15). Never sent to the cloud |
| `/etc/polkit-1/rules.d/50-localai-operators.rules` | root 0644 | Optional polkit rule: `localai-operators` may `start`/`restart` `localai-agent.service` without a password ([installer.md](../specs/installer.md) §4.3 P5, used by [desktop.md](../specs/desktop.md) §4.2) |
| `/var/log/localai-install.log` | root:root 0600 | Installer log, appended on every run, secrets scrubbed ([installer.md](../specs/installer.md) §4.6) |
| `/srv/localai/` | localai 0755 | **Internal ark content root.** Layout in §3.3 |
| `<ark-drive-mount>/localai/` | localai 0755 | **Ark drive content root.** Same layout as `/srv/localai` |
| `<ark-drive-mount>/inventory.local.yaml` | 0600 | The real lab inventory (redacted docs can be rendered from it) |
| `<ark-drive-mount>/START-HERE.html` | 0644 | Offline entry point that opens the rendered runbooks |

### 3.3 Content root layout (identical on `/srv/localai` and the ark drive)
```
<root>/
  READY.json                         readiness report (ark-sync.md §4.12)
  ROOT.json                          root identity: kind, root id, profile, created_at (ark-sync.md §4.1)
  models/<model-id>/<file>.gguf      LLM / VLM weights (+ mmproj, + draft)
  models/<model-id>/MANIFEST.json    what was downloaded, sha256, catalog version, verified_at
  speech/whisper/<model-id>/...      whisper.cpp ggml models
  speech/piper/<voice-id>/...        piper .onnx + .onnx.json
  kiwix/<content-id>/<file>.zim      ZIM files
  kiwix/library.xml                  generated by kiwix-manage
  maps/<content-id>/<file>.pmtiles   Protomaps vector tiles
  maps/assets/                       style JSON, glyphs (fonts), sprites (all local)
  kit/                               rebuild kit (rebuild-kit.md); includes kit/installers/install-node.sh and kit/catalog/
  docs/                              rendered runbooks (HTML + PDF)
  .trash/                            superseded versions waiting for verified replacement (ark-sync.md §4.10)
  .staging/                          in-progress downloads (`.part`), staged files and `quarantine/` (ark-sync.md §4.5)
```

### 3.4 Network ports (all bound to `127.0.0.1` unless stated)
| Port | Listener | Notes |
|---|---|---|
| 8080 | `llama-server` router | **Unchanged from today**, so the existing Tailscale Serve and nginx vhosts keep working. API key required except `/health` |
| 7070 | `aiagent` | API `/api/v1/*`, local UI `/`, Survival Portal `/portal`, proxies `/chat/` (llama WebUI), `/library/` (Kiwix), maps `/maps/`, manuals `/docs/`. Token or session required on `/api/v1/*`, except the unauthenticated survival routes ([security-model.md](security-model.md) §3) |
| `unix:/run/localai/agent.sock` | `aiagent` | **Same API** over a Unix socket (root:localai-operators 0660), used by `aictl`. The agent reads the peer uid with `SO_PEERCRED`. uid 0 counts as **admin** (no PIN needed). Other operators need the PIN for destructive operations |
| 8888 | `kiwix-serve` | Library. No auth (loopback only) |
| 8081 | `whisper-server` (whisper.cpp) | Speech-to-text. No auth (loopback only). Normally called through the agent |
| 2947 | `gpsd` (optional, not installed by default) | Read-only GPS source for the maps "Where am I" ([companion-services.md](../specs/companion-services.md) §4.4.5). Not ours; the agent only connects to it |
| ephemeral (`127.0.0.1:0`) | sync throttling proxy | Created per run by the sync engine for subprocess downloads ([ark-sync.md](../specs/ark-sync.md) §4.7) |
| 443 (tailnet) | `tailscale serve --https=443 http://127.0.0.1:8080` | **Pattern B** (ai-n2, ai-n3). Online mode only |
| 18080 (tailnet) | `tailscale serve --tcp=18080 tcp://127.0.0.1:8080` | **Pattern A** (ai-n1). Online mode only |

Nothing listens on the LAN interface. Remote access only works in online mode, through Tailscale.

### 3.5 systemd units
| Unit | Runs as | Purpose |
|---|---|---|
| `localai-agent.service` | root | The agent. `Restart=always` |
| `localai-llama.service` | localai | llama-server router. It replaces `llama-qwen.service`, which is disabled and masked after migration ([llama-router.md](../specs/llama-router.md) §4.9) |
| `localai-kiwix.service` | localai | kiwix-serve over `kiwix/library.xml` of the active content root(s) |
| `localai-whisper.service` | localai | whisper.cpp server |
| `localai-sync.service` + `localai-sync.timer` | root (agent-triggered) | Nightly ark sync. The service is just `aictl ark sync --scheduled --wait` (skipped run → exit 0; `SuccessExitStatus=8` for "finished with warnings"; exit codes in [aictl.md](../specs/aictl.md) §3.4) |
| `localai-drill.service` + `localai-drill.timer` | root | Monthly non-destructive drill (`aictl drill --scheduled --wait`; same exit-code rules as sync) |
| `localai-agent-rollback.timer` + `.service` | root | **Transient** (created with `systemd-run` during a self-update, gone afterwards). Restores `aiagent.prev` if the new agent fails its health check ([agent.md](../specs/agent.md) §4.10) |

### 3.6 Identifiers, tokens, URLs
| Item | Format / value |
|---|---|
| Node id | `ai-n<N>` (`ai-n1`, `ai-n2`, …). Never reused |
| Model id | lowercase slug from the catalog, e.g. `qwen3.5-9b-q4km`. It is also the router preset name and the OpenAI `model` value served by the node |
| Content id | lowercase slug from `ark.yaml`, e.g. `wikipedia-en-nopic`, `osm-central-america` |
| Profile id | `small-8gb`, `cuda-12gb`, `max-128gb`, `cpu-only`, `ark-drive-full` |
| Local agent token | `lat_` + 43 chars base64url (32 random bytes) |
| Node cloud credential | `nod_…`, minted by EAB ([cloud-app.md](../specs/cloud-app.md)) |
| Gateway user key | `lai_` + 43 chars base64url, sha256 at rest ([gateway.md](../specs/gateway.md)) |
| llama-server API key | 64 lowercase hex (`openssl rand -hex 32`), one per node |
| Doctor/error code | `LAI-<AREA>-<NNN>`, see [doctor-codes.md](../specs/doctor-codes.md) |
| Cloud app | `https://ai.marin.cr` |
| Gateway | `https://ai.marin.cr/v1` |
| Per-node public endpoint | `https://ai-n<N>.marin.cr` (kept; [source/vps-ai-nodes-setup.md](../source/vps-ai-nodes-setup.md)) |
| Open WebUI | `https://chat.marin.cr` |
| Ark drive filesystem label | `AI-ARK` (ext4) |

### 3.7 Doctor code areas
| Area | Prefix | Owner spec |
|---|---|---|
| Agent core / config | `LAI-AGT` | agent.md |
| CLI | `LAI-CLI` | aictl.md |
| LLM / router | `LAI-LLM` | llama-router.md |
| Catalog / profile | `LAI-CAT` | catalog.md |
| Ark sync | `LAI-SYN` | ark-sync.md |
| Ark drive | `LAI-ARK` | ark-sync.md |
| Companion services | `LAI-SVC` | companion-services.md |
| Network / Tailscale | `LAI-NET` | agent.md |
| Cloud link | `LAI-CLD` | cloud-app.md |
| Gateway | `LAI-GW` | gateway.md |
| Desktop | `LAI-DSK` | desktop.md |
| Installer | `LAI-INS` | installer.md |
| Rebuild kit | `LAI-KIT` | rebuild-kit.md |
| Drill | `LAI-DRL` | drill.md |
| Security / keys / PIN | `LAI-SEC` | security-model.md |
| Hardware (GPU/disk/thermal/power) | `LAI-HW` | agent.md |

### 3.8 Cloud, gateway and desktop names
| Item | Value | Owner spec |
|---|---|---|
| Cloud app CLI (the renamed EAB `appctl`) | `laictl` (state `~/.laictl/`, env prefix `LAICTL_`) | cloud-app.md §3.1 |
| Cloud deploy branch (generated) | `deploy/cloud` | cloud-app.md §3.13 |
| Node link routes (cloud) | `/api/nodes/{worker-node-uuid}/localai/{config,state,catalog,key,commands/*}` | cloud-app.md §3.6 |
| Cloud UI API | `/api/localai/*` | cloud-app.md §3.9 |
| Gateway internal path | `/api/gateway/v1/*` (nginx maps public `/v1/` to it) | gateway.md §3.1 |
| Node llama key in the cloud credential store | purpose `localai_node_llama_key`, name `<node-id>` | cloud-app.md §3.8 |
| Command job types | `localai.<area>.<verb>` (closed list) | cloud-app.md §3.7 |
| Agent in-flight command file (survives self-update) | `/var/lib/localai/cloud/inflight.json` | agent.md §4.8.6 |
| Desktop appId / executable | `cr.marin.localai.desktop` / `ai-desktop` | desktop.md §3.3 |
| Desktop install dir (deb) | `/opt/ai-desktop/` (+ `/usr/bin/ai-desktop`) | desktop.md §3.3 |
| Desktop user data | `~/.config/ai-desktop/` | desktop.md §3.8 |
| Desktop autostart entry | `~/.config/autostart/ai-desktop.desktop` | desktop.md §4.5 |
| Desktop shell origin | `localai://app` | desktop.md §3.7 |
| Release tags | `node-v<semver>` (agent, installer, agent source; [agent.md](../specs/agent.md) §4.10), `desktop-v<semver>` (desktop), `catalog-<CATALOG_VERSION>` (catalog and docs together) plus the moving tag `catalog-stable` (always the newest `catalog-*`; the fallback for nodes when the GitHub tags API fails), both created only by the catalog-release workflow ([catalog.md](../specs/catalog.md) §1) | agent.md, desktop.md §4.11, catalog.md |
| Desktop packages | `ai-desktop_<version>_amd64.deb` / `ai-desktop_<version>_arm64.deb` (electron-builder default), `ai-desktop-<version>-x86_64.AppImage` | desktop.md §4.11 |
| VPS stack-agent unit (lives in `marin-server-infra`, not on nodes; phase 2) | `localai-vps-agent.service` | cloud-app.md §4.10 |

## 4. Key flows

### 4.1 Swap a model from the cloud (online)
1. An operator clicks **Load** on `qwen3.6-35b-a3b` for ai-n2 at ai.marin.cr.
2. The cloud queues a `localai.model.load` command for ai-n2 in its command plane ([cloud-app.md](../specs/cloud-app.md) §4.4; interim app-owned `localai_commands` table, [ADR-0003](../decisions/0003-outbound-only-cloud-link.md) implementation note).
3. ai-n2's agent, which is long-polling `…/localai/commands/claim`, receives it within a second or so, runs it through the same code path as `aictl load`, and reports the result.
4. The agent's next state snapshot (`LocalaiStateSnapshotV1`) carries the new model status. The UI updates, and the gateway routing table now sends `qwen3.6-35b-a3b` to ai-n2.

### 4.2 Swap a model locally (online or offline)
`aictl load <model-id>`, the desktop **Load** button and the Portal "Switch brain" all call `POST /api/v1/models/{id}/load` on the agent. The agent then:
1. checks fit ([llama-router.md](../specs/llama-router.md) §4.3),
2. unloads others if `models_max` would be exceeded,
3. calls the router load endpoint,
4. waits for `ready`,
5. records the default.

### 4.3 Nightly sync (online)
`localai-sync.timer` runs `aictl ark sync --scheduled`. The agent then:
1. runs the preflight: the run is skipped (and the reason recorded) when the mode is not online, there is no internet, or a laptop is on battery,
2. refreshes the catalog (from the cloud if enrolled, else from the `catalog-*` tag on GitHub, else the local copy),
3. computes the plan for each active content root (internal disk, plus the ark drive if mounted), including free space and budget,
4. downloads with resume into `<root>/.staging/` (`.part` files stay on the same filesystem as the target),
5. verifies sha256,
6. swaps the new version in atomically (the old one goes to `<root>/.trash/` until the new one is verified),
7. re-verifies a slice of older items (bit rot), then writes `READY.json`,
8. reports to the cloud in the next state snapshot.
Details: [ark-sync.md](../specs/ark-sync.md).

### 4.4 Doomsday: internet is gone
Nothing needs to happen. Local inference never depended on the internet. The cloud link shows "disconnected" and sync records "skipped: offline". For the non-expert, the operator (or anyone) presses **Survival mode** in ai-desktop, or opens `http://127.0.0.1:7070`, or runs `aictl mode survival`. The agent then:
1. stops all cloud and sync activity,
2. removes Tailscale Serve,
3. loads the profile's survival model set,
4. starts Kiwix and speech,
5. opens the Survival Portal.
See [modes.md](modes.md).

### 4.5 Add a node
In the cloud UI: **Add node**, which issues a `nod_` credential and an install one-liner. Run `install-node.sh` on the new machine, and the node appears. The VPS publishing steps (DNS, vhost, cert) are printed as a checklist in phase 1 and automated in phase 2. See [installer.md](../specs/installer.md) and [runbooks/05-add-a-node.md](../runbooks/05-add-a-node.md).

## 5. Trust boundaries (summary, details in [security-model.md](security-model.md))
- **Loopback.** Everything on the node listens on 127.0.0.1 only. The agent API still requires a token, because other local users and processes exist.
- **Tailnet.** Only `llama-server` is exposed, and only in online mode, through Tailscale Serve (never Funnel). It requires the node API key.
- **Public internet.** Only the VPS faces the internet. The nginx node vhosts pass the bearer token through to llama-server. The gateway authenticates `lai_` keys and uses the per-node key upstream.
- **Cloud → node control** happens only through jobs the agent **pulls** with its `nod_` credential. The cloud cannot reach the agent API.

## 6. Relationship to existing infrastructure
- **VPS side.** It lives in the private `marin-server-infra` repo, which is unchanged except for:
  - a new `ai.marin.cr` vhost (written by `appctl deploy`),
  - Open WebUI pointing at the gateway,
  - in phase 2, automated node vhost creation.

  The existing node vhost conventions are archived in [source/vps-ai-nodes-setup.md](../source/vps-ai-nodes-setup.md).
- **Node side.** Today's `llama-qwen.service` + `/etc/llama-qwen/api-key` + Tailscale Serve setup is **migrated in place**. The port, the key and the public URL stay the same, so clients (Open WebUI, OpenCode) keep working throughout.
- **Laptop hardening.** Lid, sleep, Wi-Fi power-save and power profile settings from [source/laptop-server-setup.md](../source/laptop-server-setup.md) are applied by the installer ([installer.md](../specs/installer.md) §4.3 P4). They are documented for humans in [runbooks/09-laptop-as-server.md](../runbooks/09-laptop-as-server.md).
