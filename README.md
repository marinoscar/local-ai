# local-ai

**Run local AI for a home lab, and keep it working when the internet is gone.**

This repository is the runbook and the source tree for two jobs:

1. **Operate the Local AI lab.** GPU nodes run open models (coding, chat, vision, agents). You watch and control all of them from one website, `https://ai.marin.cr`, which also offers a single OpenAI-compatible endpoint at `https://ai.marin.cr/v1`.
2. **Be a doomsday "ark".** Every node, especially the laptops, can run **fully standalone with no internet**. Models, a Wikipedia-class library, maps, speech and a rebuild kit are already on disk. A non-expert can operate it from the **Local AI** desktop app.

If a change makes job 2 less reliable, it is wrong, even if it makes job 1 nicer ([AGENTS.md](AGENTS.md)).

---

## 🚨 In an emergency / En una emergencia

**English**
1. Plug in the charger, power on the computer and log in.
2. Open the **Local AI** app and press **Survival mode**.
3. If the app will not open, open a web browser and go to `http://127.0.0.1:7070`.

**Español**
1. Conecte el cargador, encienda la computadora e inicie sesión.
2. Abra la aplicación **Local AI** y presione **Modo supervivencia**.
3. Si la aplicación no abre, abra un navegador web y vaya a `http://127.0.0.1:7070`.

Full guide, printable, English and Spanish: [Survival mode runbook](docs/runbooks/01-survival-mode.md). One page to keep next to the computer: [Survival card](docs/runbooks/00-survival-card.md). You do not need the internet or the admin PIN to start.

> 🛠 **Status: designed, not built yet** (Survival Portal: E4, desktop app: E8). Until a node has been installed, these steps describe the design. See [Roadmap and status](#10-roadmap-and-status).

---

## Contents
[1 What you get](#1-what-you-get) · [2 How it works](#2-how-it-works) · [3 The three modes](#3-the-three-modes) · [4 The lab today](#4-the-lab-today) · [5 Quick start by role](#5-quick-start-by-role) · [6 Using the API](#6-using-the-api) · [7 Repository layout](#7-repository-layout) · [8 Documentation map](#8-documentation-map) · [9 Building and contributing](#9-building-and-contributing) · [10 Roadmap and status](#10-roadmap-and-status) · [11 Security note](#11-security-note) · [12 License and credits](#12-license-and-credits)

## 1. What you get

Everything in the first table works with **no internet**. Only the second table needs it.

| Capability | What it does | Works offline | Status |
|---|---|---|---|
| Chat and coding | Local LLMs, agentic work, the llama.cpp web chat | ✅ | 🛠 planned (E1, E2) |
| Vision | Ask questions about a photo | ✅ | 🛠 planned (E4) |
| Voice in and out | Speech-to-text (whisper.cpp) and text-to-speech (Piper) | ✅ | 🛠 planned (E4) |
| Offline library | Kiwix: Wikipedia, medical, how-to, repair and survival content | ✅ | 🛠 planned (E3, E4) |
| Offline maps | Map viewer with place search | ✅ | 🛠 planned (E4) |
| Manuals | These runbooks, rendered and printable, on the node | ✅ | 🛠 planned (E4) |
| Rebuild kit | Rebuild a node's inference software with no internet | ✅ | 🛠 planned (E5) |
| Survival Portal | Big-button home page for non-experts | ✅ | 🛠 planned (E4) |
| Monthly drill | Self-test that proves the node still works offline | ✅ | 🛠 planned (E9) |

| Online only | What it does | Status |
|---|---|---|
| Cloud control | See health, load or swap models, keys, ark readiness from `ai.marin.cr` | 🛠 planned (E6) |
| Gateway | One OpenAI-compatible API for the whole lab at `/v1` | 🛠 planned (E7) |
| Ark sync | Nightly download and checksum of models and content; never deletes the last good copy | 🛠 planned (E3) |

## 2. How it works

```
 INTERNET (may disappear at any time)
 ┌────────────────────────────────────────────────────────────┐
 │ VPS: nginx ─► ai.marin.cr ─► cloud app (control UI + /v1)  │
 │                    │            ▲                           │
 │                    │            │ outbound HTTPS only       │
 │                    └─ Tailscale ┼──────────────┐            │
 └─────────────────────────────────┼──────────────┼───────────┘
                                   │              ▼
 ┌─────────────────────────────────┴───────────────────────────┐
 │ NODE (Ubuntu, GPU)                      works fully offline │
 │  aiagent ── single source of truth on the node              │
 │   ├─ llama.cpp router (models)    ├─ Survival Portal / UI   │
 │   ├─ kiwix (library)              ├─ maps, manuals          │
 │   ├─ whisper + piper (speech)     └─ ark sync, drill, doctor│
 │  aictl (CLI)    Local AI desktop app (thin shell)           │
 │  internal disk + AI-ARK drive: models, library, maps, kit   │
 └─────────────────────────────────────────────────────────────┘
```

The detailed diagram, the components table and every canonical name (paths, ports, units, tokens, URLs) are in [overview.md](docs/architecture/overview.md). The key ideas:

- **The agent is the single source of truth.** `aiagent` is one static Go binary on every node; `aictl`, the local UI and the desktop app are clients of it ([ADR-0001](docs/decisions/0001-agent-in-go.md)).
- **Models swap with llama.cpp router mode.** The agent generates the presets, so a swap does not need a service rebuild ([ADR-0002](docs/decisions/0002-llama-router-mode.md)).
- **The cloud link is outbound only.** Nodes pull commands from the cloud; the cloud never connects in ([ADR-0003](docs/decisions/0003-outbound-only-cloud-link.md)).
- **The desktop app is a thin shell** over the agent's local UI, and a browser at `http://127.0.0.1:7070` always works as a fallback ([ADR-0004](docs/decisions/0004-desktop-thin-shell.md)).
- **The ark lives in two places:** a per-profile subset on the internal disk, and everything on the portable AI-ARK drive ([ADR-0005](docs/decisions/0005-ark-storage.md)).
- **Offline nodes are standalone.** No LAN mesh; each node must be able to live alone ([ADR-0006](docs/decisions/0006-standalone-offline.md)).
- **The repository is public and redacted.** Real lab values live only in a git-ignored file ([ADR-0007](docs/decisions/0007-public-repo-redaction.md)).
- **The cloud app is built on EnterpriseAppBase** ([ADR-0008](docs/decisions/0008-cloud-on-eab.md)).

## 3. The three modes

Each node is always in exactly one mode, and it survives a reboot. Details and the state machine: [modes.md](docs/architecture/modes.md).

- **`online`**: a normal lab member. Reachable through Tailscale, controlled by the cloud, syncing nightly.
- **`offline`**: private and standalone. Nothing leaves the machine; the local AI is fully usable.
- **`survival`**: `offline` plus everything needed in an emergency, with the Survival Portal as the home screen.

## 4. The lab today

| Node | Hardware class | Backend | Profile | Public endpoint |
|---|---|---|---|---|
| ai-n1 | MINISFORUM MS-S1 Max, 128 GB unified memory | Vulkan | `max-128gb` | `https://ai-n1.marin.cr` |
| ai-n2 | MSI Stealth 17 Studio, RTX 4080 Laptop 12 GB (dual-boot Windows) | CUDA | `cuda-12gb` | `https://ai-n2.marin.cr` |
| ai-n3 | HP ZBook Create G7, RTX 2070 Max-Q 8 GB | CUDA | `small-8gb` | `https://ai-n3.marin.cr` |

Today these run a hand-managed `llama-server`; migrating them to the agent is part of epic E2. Models served, serving patterns and known discrepancies: [lab-inventory.md](docs/architecture/lab-inventory.md). Addresses and identifiers are placeholders in this repository.

## 5. Quick start by role

Each item is a runbook written for someone who has never used a terminal. The runbooks show the commands; the specs define them.

- **I want to chat or get an API key** → [02 Chat and API keys](docs/runbooks/02-chat-and-api-keys.md)
  1. Open the app and use the chat, or
  2. run `aictl key show` to get the node's key.
- **I want to swap a model** → [03 Swap a model](docs/runbooks/03-swap-a-model.md)
  1. Pick the model in the app (or the cloud UI).
  2. Confirm; the agent reloads the router.
- **I want to set up an ark drive** → [04 AI-ARK drive](docs/runbooks/04-ark-drive.md)
  1. Buy and format the drive.
  2. Fill it with a sync, then eject it safely.
- **I want to add a node, or migrate an existing one** → [05 Add a node](docs/runbooks/05-add-a-node.md)
  1. Install the node software.
  2. Enroll it in the cloud and publish its endpoint.
- **My computer died; rebuild offline** → [06 Rebuild a node offline](docs/runbooks/06-rebuild-a-node-offline.md)
  1. Boot an Ubuntu USB stick.
  2. Install from the AI-ARK drive; no internet needed.
- **Something is broken** → [07 Troubleshooting](docs/runbooks/07-troubleshooting.md)
  1. Run `aictl doctor`.
  2. Look up the `LAI-…` code it prints.
- **What runs on a schedule** → [08 Scheduled sync](docs/runbooks/08-scheduled-sync.md)
- **A laptop must stay on with the lid closed** → [09 Laptop as server](docs/runbooks/09-laptop-as-server.md)
- **I run the lab** → [10 Lab operations](docs/runbooks/10-lab-operations.md)

New and unsure? Start at [00 Start here](docs/runbooks/00-START-HERE.md). All runbooks: [runbooks index](docs/runbooks/README.md).

## 6. Using the API

Through the lab gateway, with a gateway key (`$LAI_KEY` holds a key that starts with `lai_`):

```bash
curl -s https://ai.marin.cr/v1/models -H "Authorization: Bearer $LAI_KEY"
```

On a node, with the node's own key (`$LLAMA_API_KEY`), even with no internet:

```bash
curl -s http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $LLAMA_API_KEY"
```

Both are OpenAI-compatible, so any OpenAI client works. Setting up OpenCode and Open WebUI, Python and streaming: [02 Chat and API keys](docs/runbooks/02-chat-and-api-keys.md). Ports and URLs: [overview.md §3](docs/architecture/overview.md#3-canonical-names-single-source-of-truth).

## 7. Repository layout

```
AGENTS.md / CLAUDE.md       rules for every human and coding agent
inventory.example.yaml      placeholders for real lab values (real file: inventory.local.yaml, git-ignored)
docs/                       all documentation (see the map below)
catalog/                    models, ark content and node profiles as data            (E2, E3)
agent/                      Go module: aiagent daemon, aictl CLI, embedded local UI  (E1 to E5)
installers/                 install-node.sh, online and offline                       (E5)
packages/ui/                shared React components                                   (E4, E6, E8)
apps/cloud/                 ai.marin.cr, built on EnterpriseAppBase                   (E6, E7)
apps/desktop/               the Local AI Electron shell                               (E8)
scripts/                    repo tooling: redaction, links, doctor codes, hooks      (E0)
```

Only `docs/`, `scripts/` and the root files exist today.

## 8. Documentation map

| Folder | Contents |
|---|---|
| [docs/runbooks/](docs/runbooks/README.md) | Step-by-step operator procedures in plain language |
| [docs/architecture/](docs/architecture/overview.md) | Overview (**canonical names**), [modes](docs/architecture/modes.md), [security model](docs/architecture/security-model.md), [lab inventory](docs/architecture/lab-inventory.md), [release signing](docs/architecture/release-signing.md) |
| [docs/specs/](docs/README.md#specs) | One contract per component, plus the [doctor-code registry](docs/specs/doctor-codes.md) |
| [docs/decisions/](docs/decisions/README.md) | Architecture decision records (ADRs) |
| [docs/source/](docs/README.md) | Redacted archive of the original gists. History only |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Epics, issue map, dependency graph |

The component → spec → epic table is kept in [docs/README.md](docs/README.md#specs) so it cannot drift from this page.

## 9. Building and contributing

- **Issue first.** Every change is tracked by a GitHub issue under an epic. One issue = one branch `<type>/<issue#>-<slug>` = one PR that says `Closes #<issue>`. Commits follow Conventional Commits (`feat(agent): …`).
- **Redaction.** The repo is public. Run `scripts/install-hooks.sh` once per clone; it blocks commits that leak lab values. Check any time with `scripts/check-redaction.sh`. Details in [AGENTS.md](AGENTS.md#local-setup-once-per-clone).
- **Other checks before you push:** `scripts/check-links.sh` (every link and anchor resolves) and `scripts/check-doctor-codes.sh` (error codes match their specs). CI runs all of them.
- **Tech stack** (decided in the ADRs):

  | Component | Stack |
  |---|---|
  | Agent and CLI | Go, one static binary for linux/amd64 and linux/arm64 |
  | Local UI and Survival Portal | React and Vite, embedded in the agent, no network assets |
  | Cloud app and gateway | EnterpriseAppBase: NestJS, Prisma, PostgreSQL, React |
  | Desktop app | Electron, a thin shell |
  | Inference | llama.cpp `llama-server` in router mode |

- **Coding agents:** read [AGENTS.md](AGENTS.md), then your issue, then the spec sections it links. If the spec is wrong, fix it in the same PR.

## 10. Roadmap and status

**Design approved; implementation not started.** The foundation epic (E0) is in progress: its CI guardrails are in place. The first product epic (E1, the agent) has not begun. Update this section, and the 🛠 markers above, whenever an epic ships.

| Phase | Goal | Epics |
|---|---|---|
| 0 | Foundation: repo, CI guardrails, docs | [E0 #1](https://github.com/marinoscar/local-ai/issues/1) |
| 1 | Node agent and ark, fully offline | [E1 #2](https://github.com/marinoscar/local-ai/issues/2) agent and CLI · [E2 #3](https://github.com/marinoscar/local-ai/issues/3) models and router · [E3 #4](https://github.com/marinoscar/local-ai/issues/4) ark sync · [E4 #5](https://github.com/marinoscar/local-ai/issues/5) companion services and Portal · [E5 #6](https://github.com/marinoscar/local-ai/issues/6) installer and kit |
| 2 | Cloud and gateway | [E6 #7](https://github.com/marinoscar/local-ai/issues/7) cloud app · [E7 #8](https://github.com/marinoscar/local-ai/issues/8) gateway |
| 3 | Desktop | [E8 #9](https://github.com/marinoscar/local-ai/issues/9) desktop app |
| Throughout | Drills, QA, runbook verification | [E9 #10](https://github.com/marinoscar/local-ai/issues/10) |

Full issue map, waves and critical path: [ROADMAP.md](docs/ROADMAP.md). Live status: [GitHub Issues](https://github.com/marinoscar/local-ai/issues).

## 11. Security note

This repository is **public**. It contains placeholders such as `<TAILNET_IP_AI_N1>` and `<VPS_PUBLIC_IP>`, never real addresses, keys, tokens or zone IDs. The real values live only in `inventory.local.yaml`, which is git-ignored and exists on the owner's machines and the ark drive ([inventory.example.yaml](inventory.example.yaml), [security model §5](docs/architecture/security-model.md#5-redaction-public-repo)).

**If you find a leaked value:** do not paste it into an issue or a PR. Open a private report with GitHub **Security → Report a vulnerability** on this repository, and say which file and line. The owner will rotate the value and clean the history.

## 12. License and credits

**This repository has no license file yet.** The owner has not chosen one. Until one is added, all rights are reserved by the author.

Local AI builds on these projects. Check each project's own license before you redistribute anything, especially the **content** that goes on an ark drive:

| Project | Used for | License (upstream) |
|---|---|---|
| [llama.cpp](https://github.com/ggml-org/llama.cpp) | LLM inference and web chat | MIT |
| [whisper.cpp](https://github.com/ggml-org/whisper.cpp) | Speech-to-text | MIT |
| [Piper](https://github.com/rhasspy/piper) | Text-to-speech (each voice has its own license) | MIT |
| [Kiwix](https://kiwix.org) | Offline library | GPL-3.0 (kiwix-tools) |
| [Protomaps](https://protomaps.com) and [MapLibre](https://maplibre.org) | Offline map tiles and viewer | BSD-3-Clause |
| [OpenStreetMap](https://www.openstreetmap.org/copyright) | Map data | ODbL 1.0 |
| [GeoNames](https://www.geonames.org) | Offline place search | CC BY 4.0 |
| Wikipedia and other Kiwix content | Library content | mostly CC BY-SA 4.0, varies per ZIM |
| EnterpriseAppBase | Base of the cloud app | see its repository |

Model files have their own licenses too; the catalog records them per model.
