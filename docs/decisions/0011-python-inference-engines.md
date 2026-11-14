# ADR-0011: Python/PyTorch inference engines run on nodes as their own loopback services

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** owner (decisions delegated for epic E11, issue #506)

## Context
Epic E11 (#504) adds on-demand music generation with ACE-Step 1.5. ACE-Step is a PyTorch program, not a llama.cpp model: the router of [ADR-0002](0002-llama-router-mode.md) cannot serve it, and it has no Go or C++ port. Until now every program on a node was either the Go agent ([ADR-0001](0001-agent-in-go.md), which rejects Python *for the agent*) or a static or compiled native binary (llama.cpp, whisper.cpp, piper, kiwix-tools, pmtiles).

A Python runtime brings three risks that matter for the ark:
1. **Network at runtime.** `pip install`, Hugging Face downloads at first use and model auto-updates all break [ADR-0006](0006-standalone-offline.md) and AGENTS.md rule 3.
2. **Reproducibility.** A wheel set that resolves differently tomorrow cannot be rebuilt from the kit with the network cable pulled.
3. **Unbounded execution.** [ADR-0010](0010-the-agent-keeps-the-node-complete.md) lets the agent install only from a closed, verified allowlist. A Python install must fit inside that rule, not around it.

## Decision
1. **The agent stays Go.** A Python engine is never imported into, linked into or spawned as a child of the agent. It runs as its **own systemd unit**, `localai-<engine>.service`, as user `localai`, listening on **loopback only** (a canonical port in [overview.md](../architecture/overview.md) §3.4). The agent drives it over HTTP (a small protocol the repo owns, the "shim") or a CLI. For ACE-Step the unit is `localai-music.service`, port 8082, and the shim is `engines/acestep/` (package `localai_music_shim`), specified in [music.md](../specs/music.md).
2. **Pinned venv, built offline.** Each engine's runtime is a virtual environment at `/opt/localai/<engine>/<version>/venv` with the symlink `/opt/localai/<engine>/current`, the same `<version>`/`current` layout as the other tools. It is built **only** from a vendored wheel bundle in the rebuild kit (`kit/engines/<engine>/<version>/<engine>-<version>-<backend>.tar.zst`), with `pip install --no-index` against the unpacked bundle and hashes required. The bundle holds every wheel, the shim and a lock file; nothing is resolved at install time.
3. **No runtime downloads.** The unit's environment sets the offline switches of every library it loads (`HF_HUB_OFFLINE=1`, `TRANSFORMERS_OFFLINE=1`, no telemetry). Model weights come only from the verified content root (`/srv/localai/music/<model-id>/`, synced by [ark-sync.md](../specs/ark-sync.md) like any model). The unit has no network beyond loopback (`IPAddressDeny=any` with `IPAddressAllow=localhost`). An engine that tries to reach the internet fails at once; it does not wait.
4. **Joins the ADR-0010 allowlist.** "Install the runtime of engine `<engine>` from its kit bundle" is one more row of the closed ensure table (`internal/ensure/allowlist.go`). The bundle is verified against the kit's own record right before it is unpacked; a bad checksum installs nothing (`LAI-KIT-001`). The step is idempotent (an installed matching version changes nothing), journaled and audited like the others. It runs only when the profile or `node.yaml` includes a model of that engine.
5. **Per-backend wheel sets.** A bundle is built per accelerator backend, because PyTorch wheels are. **CUDA first.** ROCm (needed for the unified-memory node) is a later issue (#517). Until then a ROCm or CPU-only node does not get the engine, and its models are not loadable there (`LAI-LLM-043`).
6. **Engine-specific facts are isolated.** Everything that depends on the real ACE-Step program (the call itself, its dtype choices, measured memory) sits in one module of the shim (`backend.py`) and one catalog entry, both pinned by the spike (#505).

## Consequences
- **Good:** a PyTorch model can run on a node without changing the agent's language, build or trust model. The engine is stopped and started like any unit. The same pattern serves future engines (GPU text-to-speech).
- **Good:** a node rebuilt offline from the AI-ARK drive gets the exact same venv: the wheel bundle and its lock are in the kit, and the build uses no network ([rebuild-kit.md](../specs/rebuild-kit.md)).
- **Cost: kit size.** A CUDA PyTorch wheel set is several gigabytes per backend and version. The kit keeps one version per backend (plus the previous one while a new one is verified), and only profiles that include a music model pull it.
- **Cost: security updates.** Python wheels get security fixes more often than our static binaries. Updating means building a new bundle, pinning it in the catalog and letting sync and ensure roll it out, the same path as any kit tool. There is no `pip upgrade` on a node.
- **Cost: a second runtime to debug.** Engine errors surface through the shim's `/health` and the agent's doctor codes ([music.md](../specs/music.md) §8), so a non-expert never reads a Python traceback.
- **Not changed:** llama.cpp stays the model swapper for LLMs ([ADR-0002](0002-llama-router-mode.md)). The agent is still one static Go binary.

## Alternatives considered
- **Run ACE-Step inside the agent (cgo, embedded Python):** rejected. It breaks the static binary, `CGO_ENABLED=0` and the offline agent rebuild of [ADR-0001](0001-agent-in-go.md), and a PyTorch crash would take the agent down with it.
- **Containers (Docker or Podman with an NVIDIA runtime):** rejected. A second daemon and an image store to keep offline, the container toolkit per driver version, and a much larger kit. A venv plus a systemd unit gives the isolation we need with tools already on every node.
- **`pip install` from PyPI at install time:** rejected. It needs the network and does not reproduce; it fails the ark's first rule.
- **Conda or micromamba environments:** rejected for now. A packed environment would work offline, but it adds a second package manager to the kit; a wheel bundle on the system Python is enough.
- **Drive the upstream Gradio app as the engine:** rejected as the interface the agent drives. It is a UI, not a stable API, and by default it loads assets from the internet. It may be offered as an optional advanced UI: loopback only, off by default ([music.md](../specs/music.md) §4.9).
