# Spec: Music generation (ACE-Step engine, song store, music API)

> **Status:** approved design · **Epic:** E11 (#504) · **Code:** `engines/acestep/` (the Python shim, package `localai_music_shim`), `agent/internal/music/` (queue, store, routes, cloud command), `agent/internal/music/engine` (the `acestep` engine adapter), `agent/ui/src/music/` · **Doctor area:** `LAI-SVC` (codes 150–179; 001–099 belong to [companion-services.md](companion-services.md), 100–149 to [survival-portal.md](survival-portal.md))
> Canonical names: [overview.md](../architecture/overview.md) §3. Decisions: [ADR-0011](../decisions/0011-python-inference-engines.md) (Python engines on nodes), [ADR-0012](../decisions/0012-on-demand-engines-one-gpu-budget.md) (one GPU budget, workloads). Load and unload: [llama-router.md](llama-router.md) §4.15. Routes: [agent.md](agent.md) §3.8.35, §3.8.36.
> **The spike (#505) has not run yet.** Every fact that depends on the real ACE-Step program (its Python call, measured memory, timings, file names, hashes) is marked `TODO(#505)` and lives in one module of the shim (`backend.py`) and one catalog entry. Nothing here invents a measured number.

---

## 1. Purpose and scope

A node can make songs on demand: a person writes lyrics, picks a style, optionally adds a short audio sample, and gets one to four songs back as audio files kept on the node. The engine is **ACE-Step 1.5**, a PyTorch program, run as its own loopback service and driven by the agent. Music is a **workload**: it holds most of a consumer GPU while loaded, so a person switches the computer to **Music** and back to **Coding** with one button.

In scope:
- `localai-music.service`: the runtime (venv), the environment file, the unit, readiness and the shim's HTTP protocol.
- The `acestep` catalog entry and where its files live.
- The agent's music API: generate (with a sample), operations, cancel, the song list, audio, delete; the queue and its limits.
- The song store, its retention and the disk guard.
- The cloud command `localai.music.generate` (the node side) and the cloud flow.
- The local UI page `/music/` and the optional upstream "advanced UI" (Gradio).
- Doctor codes `LAI-SVC-150` to `LAI-SVC-159` (block 150–179).

Non-goals:
- Training, fine-tuning or LoRA management.
- Editing songs (cut, mix, stems). A song is generated whole.
- Streaming audio while it is generated.
- Music in survival mode: it is never started there ([modes.md](../architecture/modes.md) §2.2).
- ROCm and CPU runtimes (later: #517). Until then music runs only on CUDA nodes.
- GPU text-to-speech engines (a future engine on the same adapter pattern).

## 2. Concepts

| Term | Meaning |
|---|---|
| **Engine** | The ACE-Step program with its weights, running in `localai-music.service` |
| **Shim** | Our small Python HTTP server around ACE-Step (`engines/acestep/`, `python -m localai_music_shim`). The agent talks only to the shim, never to ACE-Step itself |
| **Music model** | A catalog model with `engine: acestep`, `roles: [music]` ([catalog.md](catalog.md) §2.2) |
| **Loaded** | The engine is running with a music model and its `/health` says `ready` |
| **Request / operation** | One `POST /music/generate`; it has an `op_id` and produces `variations` songs. In the shim it is one **task** |
| **Variation** | One of up to 4 songs made from the same request with different seeds |
| **Sample** | An optional audio file the person adds. **"Sounds like this"** (`sample_mode: sounds_like`) borrows its style; **"Same song, my lyrics"** (`same_song`) keeps its melody and arrangement and sings the new lyrics over it |
| **Strength** | 0 to 1: how closely the song follows the sample |
| **Song store** | `/var/lib/localai/music/songs/<song-id>/`: the audio and its `meta.json` |
| **Workload `music`** | A workload whose set is a music model (catalog.md §4). Activating it unloads the coding or chat models to make room |

## 3. Interfaces

### 3.1 Runtime and install
- **Runtime:** `/opt/localai/acestep/<version>/venv` (a Python virtual environment with ACE-Step, its wheels and the shim), with `/opt/localai/acestep/current` a symlink to the active `<version>`. A `BUILD.json` next to `venv` records `{version, backend, python, lock_sha256, built_at}`.
- **Built offline only** from the kit item `kit/engines/acestep/<version>/acestep-<version>-<backend>.tar.zst` ([rebuild-kit.md](rebuild-kit.md) §3.1), backend `cuda` first. The bundle holds the wheels, the shim's wheel and `requirements.lock` with a hash per wheel. Install: verify the bundle against the kit's record, unpack to a scratch directory, `python3 -m venv <version>/venv`, `pip install --no-index --find-links <unpacked> --require-hashes -r requirements.lock`, write `BUILD.json`, run the smoke test (`python -m localai_music_shim --self-test`, which imports everything with no GPU work and no network), then move `current`. A failure leaves `current` as it was.
- **Who installs it:** the ensure engine ([rebuild-kit.md](rebuild-kit.md) §4.5, row `engine.acestep`) when the profile or `node.yaml` includes an `acestep` model, and the installer's repair ([installer.md](installer.md)). Never `pip` from the internet, never at run time.
- **Not installed** (no `current`, or `BUILD.json` names another backend): `LAI-SVC-150`; every `acestep` model is `loadable: false` with `LAI-LLM-043`. A driver too old for the bundle's CUDA (checked by the self-test and at load) is `LAI-SVC-159`.

### 3.2 Unit and environment file
`/etc/localai/music.env` (root:localai 0640) is **written by the agent** before every start of the engine (llama-router.md §4.15.1). It is never hand-edited.
```
MUSIC_PORT=8082
MUSIC_MODEL_ID=acestep-v15-turbo
MUSIC_MODEL_DIR=/srv/localai/music/acestep-v15-turbo
MUSIC_WORK_DIR=/var/lib/localai/music/work
MUSIC_DEVICE=cuda
MUSIC_DTYPE=auto
MUSIC_MAX_QUEUE=4
```
- `MUSIC_PORT` is `node.yaml` `ports.music` (agent.md §3.4). `MUSIC_MODEL_DIR` is the best verified copy of the model ([ark-sync.md](ark-sync.md) BestCopy): `<root>/music/<model-id>`, on the internal root or the ark drive. `MUSIC_DEVICE` is `cuda` (the only runtime today). `MUSIC_DTYPE` is `auto` (the shim picks the dtype the spike validates, `TODO(#505)`); `fp16`, `bf16` and `fp32` exist for tests. `MUSIC_MAX_QUEUE` is 4. There are no `node.yaml` keys for these in v1.

`/etc/systemd/system/localai-music.service` (written by the agent and `aiagent setup units`, root 0644, followed by `daemon-reload`; **never enabled**):
```ini
[Unit]
Description=Local AI music engine (ACE-Step)
Documentation=https://github.com/marinoscar/local-ai/blob/main/docs/specs/music.md
After=local-fs.target
ConditionPathExists=/opt/localai/acestep/current/venv/bin/python
ConditionPathExists=/etc/localai/music.env

[Service]
Type=simple
User=localai
Group=localai
SupplementaryGroups=video render
EnvironmentFile=/etc/localai/music.env
Environment=HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 HF_DATASETS_OFFLINE=1 GRADIO_ANALYTICS_ENABLED=False DO_NOT_TRACK=1 PYTHONDONTWRITEBYTECODE=1
ExecStart=/opt/localai/acestep/current/venv/bin/python -m localai_music_shim
Restart=no
TimeoutStartSec=30
TimeoutStopSec=20
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
LockPersonality=yes
IPAddressDeny=any
IPAddressAllow=localhost
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
ReadWritePaths=/var/lib/localai/music/work
```
- No `[Install]` section: the unit cannot be enabled by mistake; only the agent starts and stops it (agent.md §4.3.1 allowlist row, no `Enable`).
- `Restart=no`: a crash is handled by the adapter (llama-router.md §4.15.3), not by systemd, so a broken engine is not restarted in a loop that holds the GPU.
- No `PrivateDevices` and no `MemoryDenyWriteExecute`: CUDA needs `/dev/nvidia*` and PyTorch generates code at run time.
- `TimeoutStartSec=30` covers the process start only; readiness (the model loading) is the adapter's deadline.
- The engine reads the weights (read-only through `ProtectSystem=strict`) and writes only the work directory; the agent moves finished songs into the store.

### 3.3 Port
`127.0.0.1:8082` (overview §3.4), only while the engine runs. No auth: only the agent calls it. The agent never proxies it, and nothing else on the node should use it.

### 3.4 Shim protocol (agent ↔ shim, `http://127.0.0.1:8082`)
JSON over HTTP/1.1; every body is UTF-8 JSON; unknown fields in a request are rejected (`400`). The agent's client goes through `netx` with a loopback-only dialer (AGENTS.md tech stack).

| Method and path | Request | Answers |
|---|---|---|
| `GET /health` | — | `200 {"status": "loading"\|"ready"\|"error", "model_id": "…", "device": "cuda", "dtype": "fp16", "vram_mib": 1234 \| null, "error": null \| "…"}` |
| `POST /v1/tasks` | see below | `202 {"task_id": "…"}` · `429 {"error": "queue_full"}` · `400 {"error": "…"}` |
| `GET /v1/tasks/{id}` | — | `200 {"state": "queued"\|"running"\|"done"\|"failed"\|"cancelled", "progress": 0.0..1.0 \| null, "error": null \| "…", "outputs": [{"path": "/abs/file.wav", "duration_s": 60.1, "sample_rate": 48000, "mime": "audio/wav"}]}` · `404` |
| `POST /v1/tasks/{id}/cancel` | — | `200` (also for a task that already ended) · `404` |

`POST /v1/tasks` body:
```json
{"lyrics": "[verse]\n…", "tags": "folk, acoustic guitar, warm", "duration_s": 60, "language": "es",
 "seed": null, "variations": 2,
 "sample_path": null, "sample_mode": null, "strength": 0.5,
 "output_dir": "/var/lib/localai/music/work/<task-id>"}
```
- `seed` null means random. The agent always sends a concrete seed (it draws one when the person gave none) so it can record it; variation `i` uses `seed + i`.
- `sample_path` is an absolute path inside `MUSIC_WORK_DIR` (the shim refuses any other path, `400`); `sample_mode` is required when `sample_path` is set and must be null otherwise.
- `output_dir` must be inside `MUSIC_WORK_DIR` and must not exist yet; the shim creates it and writes `out-<i>.wav` there.
- Format: **WAV by default** (48 kHz unless the model says otherwise, `TODO(#505)`); the shim may add MP3 later, announced by `mime`.
- The queue holds at most `MUSIC_MAX_QUEUE` tasks (running plus queued); the next is `429`. One task runs at a time.
- `/health` `error` carries a short English reason for the agent's logs (for example `out of graphics memory`); it never carries lyrics.
- The ACE-Step call itself sits in **one** module, `engines/acestep/localai_music_shim/backend.py`, behind an interface with a fake backend for tests (it writes a short tone of the requested length). The real call is marked `TODO(#505)`.

### 3.5 Catalog entry (example)
No real entry ships until #505 provides the hashes (catalog.md §2.2). The complete example, also in the agent's catalog testdata:
```yaml
  - id: acestep-v15-turbo
    display_name: "ACE-Step 1.5 (music)"
    summary: "Makes songs from lyrics and a style. Can follow a short sample."
    summary_es: "Crea canciones a partir de una letra y un estilo. Puede seguir una muestra corta."
    engine: acestep
    roles: [music]
    languages: [en, es, multi]
    license: apache-2.0                     # TODO(#505): confirm the upstream license
    survival_tier: 3
    source:
      type: huggingface
      repo: <acestep-hf-repo-from-spike>
      revision: <40-hex-commit-from-spike>
    files:
      - kind: acestep
        name: acestep-v15-turbo/model.safetensors
        size_bytes: <size-from-spike>
        sha256: "<sha256-from-spike>"
      - kind: acestep
        name: acestep-v15-turbo/config.json
        size_bytes: <size-from-spike>
        sha256: "<sha256-from-spike>"
    requirements:
      backends: [cuda]
      min_vram_mib: <measured-by-spike>     # TODO(#505)
      min_ram_mib: <measured-by-spike>      # TODO(#505)
      min_disk_bytes: <sum-of-sizes>
```
The files land in `<root>/music/acestep-v15-turbo/acestep-v15-turbo/...` (catalog.md §2.2, overview §3.3). A node gets music by including the id in its profile or `profile_overrides.add_models`, and a workload in `node.yaml`:
```yaml
workloads:
  music:
    label: {en: "Music", es: "Música"}
    models: [acestep-v15-turbo]
```

### 3.6 Agent API
Routes, auth and status codes: [agent.md](agent.md) §3.8.35 and §3.8.36 (authoritative for the HTTP shape). The limits of `POST /music/generate` (multipart):

| Field | Rule | Default |
|---|---|---|
| `lyrics` | UTF-8 text, ≤ 8 KiB; section tags such as `[verse]`, `[chorus]`, `[bridge]` are passed as written; may be empty for an instrumental | `""` |
| `tags` | UTF-8 text, ≤ 512 bytes: the style, comma-separated words (`folk, acoustic guitar, warm`) | `""` |
| `duration_s` | integer 10 to 240 | 60 |
| `language` | two-letter code of the lyrics (`en`, `es`, ...); the shim refuses a language the model does not sing (`LAI-SVC-154`) | `en` |
| `seed` | integer 0 to 2147483647 | random |
| `variations` | integer 1 to 4 | 1 |
| `sample` | optional file: WAV, MP3, FLAC or Ogg (checked by its first bytes, not by its name), ≤ 20 MiB, ≤ 60 s | — |
| `sample_mode` | `sounds_like` \| `same_song`; only with `sample` | `sounds_like` |
| `strength` | number 0 to 1; only with `sample` | 1.0 for `same_song`, 0.5 for `sounds_like` |

Lyrics and tags cannot both be empty (`LAI-SVC-154`). A field out of range, an unknown field, a sample of another type or over the size is `400` or `413 LAI-SVC-154` with `details.field`. The sample's length is checked by the shim (it decodes it); a sample over 60 s is refused there and answered `400 LAI-SVC-154`.

### 3.7 Local UI (`/music/`)
The page of the agent's embedded UI (agent.md §3.11), built in `packages/ui` and used by the node UI and the desktop's Music window ([desktop.md](desktop.md)). English and Spanish. State first (ADR-0009):
1. **State line.** "Music is ready", "Music is starting (about a minute)", or "This computer is set for Coding. **Switch to Music**" (the button activates the `music` workload, agent.md §3.8.34). When music is not installed: `LAI-SVC-150` with **Install what's missing**. In survival mode: "Music is off in survival mode."
2. **Make a song.** Lyrics (a text area with a hint showing `[verse]` and `[chorus]`), Style (tags), Length (slider, 10 s to 4 min), Language, How many versions (1 to 4). **Add a sample** (optional) with two choices, **Sounds like this** and **Same song, my lyrics**, and a "How closely" slider (strength) under **More options**, with the seed. **Make the song** button.
3. **Progress.** One bar per request (`music.progress`), with **Cancel**.
4. **My songs.** Newest first: title, length, date, a play control (`<audio>` over `/api/v1/music/songs/{id}/audio`), **Download**, **Delete** (asks for confirmation, and the desktop asks for the admin PIN, as for deleting a model). Songs made from the cloud are marked "from ai.marin.cr".

The page loads no network asset (AGENTS.md rule 3).

**Labels** (English / Spanish; the runbook [12](../runbooks/12-make-music-and-switch-workloads.md) quotes them, so a change here changes it too): **Switch to Music** / **Cambiar a Música**; Lyrics / Letra; Style / Estilo; Length / Duración; Language / Idioma; How many versions / Cuántas versiones; **Add a sample** / **Agregar una muestra**; **Sounds like this** / **Suena como esto**; **Same song, my lyrics** / **La misma canción, mi letra**; More options / Más opciones; How closely / Qué tan parecido; **Make the song** / **Hacer la canción**; Requests / Solicitudes; **Cancel** / **Cancelar**; My songs / Mis canciones; **Download** / **Descargar**; **Delete** / **Borrar**; "Music is ready" / "La música está lista"; "Music is starting (about a minute)" / "La música está iniciando (cerca de un minuto)". The workload switcher box is "What this computer does" / "Lo que hace esta computadora"; the `aictl` menu entry is "Switch what this computer does" / "Cambiar lo que hace esta computadora".

## 4. Behaviour

### 4.1 Loading and unloading
The `acestep` engine adapter (llama-router.md §4.15) owns the engine's life: writing `music.env`, starting and stopping the unit, readiness, the GPU budget, eviction, rollback and the failure codes `LAI-LLM-040` to `LAI-LLM-043`. Music has no separate on/off switch: **music is on when a music model is loaded**. People do it with the `music` workload; `aictl load <music model>` and `POST /models/{id}/load` work too.

### 4.2 Readiness
`engine_state` (`GET /music/status`) is the adapter's view: `stopped` (unit inactive), `starting` (unit active, `/health` not `ready` yet), `ready`, `stopping`, `failed` (the last start failed or the engine crashed; cleared by the next load). The agent polls `/health` every 1 s while starting and every 10 s while ready; three failed polls in a row while ready count as a crash (llama-router.md §4.15.3).

### 4.3 The queue
- The agent keeps its own queue of operations, in front of the shim's: at most `MUSIC_MAX_QUEUE` (4) operations queued or running per node. The fifth `POST /music/generate` is `429 LAI-SVC-153`. One operation runs in the shim at a time; the others wait in order.
- A request is accepted only while the engine is `ready` (else `409 LAI-SVC-151`, with the hint to switch to the Music workload). The agent reserves the song ids at once and answers `202`.
- An operation that waits while the engine stops (a workload change, survival, an unload) ends `cancelled` (`LAI-SVC-158`).

### 4.4 Generating a song
For each operation, in order:
1. **Disk guard.** Free space on the filesystem of `/var/lib/localai/music` minus the expected size of the songs (`duration_s × variations × 192000` bytes for 48 kHz 16-bit stereo WAV, `TODO(#505)` if the model's format differs) must stay at or above the profile's `storage.min_free_bytes` ([catalog.md](catalog.md) §4). Otherwise `507 LAI-SVC-155` before anything is written; checked again before each song is moved into the store.
2. **Work directory.** `/var/lib/localai/music/work/<task-id>/` (localai 0750). The sample, if any, is written there as `sample.<ext>` (the agent counts its bytes while streaming the upload and stops at 20 MiB, `413 LAI-SVC-154`).
3. **Submit** `POST /v1/tasks`. `429` from the shim (it should not happen, the agent's queue is the same size) is retried after 2 s, five times, then `LAI-SVC-153`. `400` is `LAI-SVC-154` with the shim's reason in `details.reason`.
4. **Follow** `GET /v1/tasks/{id}` every 1 s; publish `music.progress {op_id, state, progress, song_ids}` at most once per second. `failed` → `LAI-SVC-152` (`details.engine_error`); the engine crashing (unit exits, `/health` gone) → `LAI-SVC-152`; `cancelled` → `LAI-SVC-158`.
5. **Store.** For each output, in order: hash it (sha256), move it into `/var/lib/localai/music/songs/<song-id>/audio.<ext>` (rename on one filesystem; copy, fsync and verify the hash otherwise), write `meta.json` (atomic), then publish. Then delete the work directory (also on failure and cancel: nothing is left behind in `work/`).
6. **Done.** `music.done {op_id, state: done, progress: 1, song_ids}`. If the shim returned fewer outputs than `variations`, the missing song ids are released and the operation is `done` with the songs it has, plus a warning in the operation's `error` field (`LAI-SVC-152`).

**Cancel** (`POST /music/ops/{op_id}/cancel`): a queued operation is removed; a running one is cancelled in the shim (`POST /v1/tasks/{id}/cancel`); the operation ends `cancelled` with `LAI-SVC-158` (severity info). Songs already stored for it stay.

**Timings.** How long a song takes depends on the GPU and the length; the spike measures it (`TODO(#505)`). The UI says "This usually takes a few minutes" until the measured figure replaces it.

### 4.5 Song store and retention
```
/var/lib/localai/music/
  songs/<song-id>/audio.wav        localai 0640
  songs/<song-id>/meta.json        localai 0640
  work/<task-id>/                  scratch; empty when nothing runs
```
`meta.json`:
```json
{"id": "…", "created_at": "2026-11-14T18:20:05Z", "title": "Walking home by the river",
 "lyrics": "[verse]\nWalking home by the river…", "tags": "folk, acoustic guitar, warm", "language": "en",
 "duration_s": 60.1, "model_id": "acestep-v15-turbo", "seed": 1234, "variation": 0,
 "mime": "audio/wav", "size_bytes": 11534380, "sha256": "<64 hex>",
 "sample": {"mode": "sounds_like", "strength": 0.5, "sha256": "<64 hex>", "size_bytes": 1048576} ,
 "origin": "local", "cloud_request_id": null, "op_id": "op_…"}
```
- The song id is chosen by the agent, sortable by time, and matches `^[A-Za-z0-9_-]{8,64}$`; clients treat it as opaque.
- **Retention: a song is kept until a person deletes it.** Nothing expires, sync never touches the store, and the store is not part of the ark drive. `DELETE /music/songs/{id}` (admin, agent.md §3.8.36) removes the directory.
- The sample itself is **not kept**: only its hash and size are recorded. The person's audio stays only as long as the song needs it.
- At agent start, a `work/` directory older than 24 h is removed (an interrupted operation), and a song directory without a valid `meta.json` is listed as damaged in `aictl doctor` (no code of its own: `LAI-AGT-099` details) and never deleted automatically.
- `GET /music/songs` reads the `meta.json` files (cached, refreshed when the directory changes). Lyrics are in `meta.json` for the person; the list and every log omit them.

### 4.6 Survival mode
The survival step `engines_stop` stops the engine before the survival models load (llama-router.md §4.15.4, agent.md §4.2.1, [modes.md](../architecture/modes.md) §2.2): queued and running songs end `cancelled` (`LAI-SVC-158`), the songs already made stay. A stop failure is `LAI-LLM-042` and survival goes on. In survival mode `POST /music/generate` is `409 LAI-SVC-151` (music is not loaded) and loading a music model is refused (`409 LAI-AGT-023`). The song list and playback still work.

### 4.7 Cloud flow (`localai.music.generate`)
The cloud side ([cloud-app.md](cloud-app.md) §3.7, §3.9) stores the sample in its storage slice and queues the command to one node. Payload (strict decoding on both sides, an unknown field is rejected):
```json
{"request_id": "<uuid>", "lyrics": "…", "tags": "…", "duration_s": 60, "language": "es", "seed": null, "variations": 2,
 "sample": {"url": "https://<signed GET>", "sha256": "<64 hex>", "mime": "audio/mpeg", "size_bytes": 812345},
 "sample_mode": "sounds_like", "strength": 0.5,
 "results": [{"put_url": "https://<signed PUT>"}, {"put_url": "https://<signed PUT>"}]}
```
On the node (`internal/music` command handler, agent.md §4.8.6):
1. Validate the payload with the limits of §3.6; `results` must have exactly `variations` entries and every URL must be `https`. Invalid → `LAI-CLD-058`.
2. If there is a `sample`: download it through `netx` (the agent's only outbound path) into the work directory, at most `size_bytes` and 20 MiB, and check its sha256. Any failure (network, status, size, hash) → `LAI-SVC-156`; nothing is generated.
3. Run the same local generate (§4.4) and wait for it. Its failures settle the command with their own code (`LAI-SVC-151`, `152`, `153`, `154`, `155`, `158`).
4. PUT each song to `results[i].put_url` (`Content-Type` = the song's `mime`, `Content-Length` set; up to 3 tries with backoff). A PUT that still fails → `LAI-SVC-157`; the songs stay on the node.
5. Keep the local copies with `origin: "cloud"` and `cloud_request_id`. Result `{"songs": [{"index": 0, "sha256": "…", "size_bytes": 11534380, "mime": "audio/wav", "duration_s": 60.1}]}`.

The command's lease covers the whole generation (cloud-app.md §3.7 gives the cap). Signed URLs are secrets for their lifetime: they are never logged, audited or stored (only the host name, for diagnostics). The cloud never reaches the node: the node pulls the command and the sample, and pushes the songs ([ADR-0003](../decisions/0003-outbound-only-cloud-link.md)). A node that is not on the `music` workload fails the command with `LAI-SVC-151`; the cloud page offers the workload button first.

### 4.8 Desktop
The desktop opens `/music/` in its Music child window and adds a top-bar **Music** button ([desktop.md](desktop.md)). In connected mode its Lab tab may also call the cloud's `music.generate` and `workload.activate` through its allowlisted proxy.

### 4.9 The upstream "advanced UI" (Gradio)
ACE-Step ships a Gradio web app. It is **optional, off by default, loopback only**:
- It is not part of the runtime the agent starts; the shim never launches it.
- A person who wants it runs it by hand on the node from the same venv, bound to `127.0.0.1` on a port they choose, with the analytics switched off (`GRADIO_ANALYTICS_ENABLED=False`), only while the music workload is **not** active (two copies of the model do not fit). The agent does not proxy it, the desktop does not open it, and the cloud never reaches it.
- It may try to fetch web assets from the internet; offline it may look broken. That is accepted for an expert tool and is the reason it is not offered to non-experts.
- Nothing in this spec depends on it; it has no doctor codes.

## 5. Failure handling and edge cases

| Situation | Behaviour | Code |
|---|---|---|
| Runtime missing or for another backend | Music models not loadable; the page shows **Install what's missing** | `LAI-SVC-150`, `LAI-LLM-043` |
| GPU driver or CUDA too old for the bundle | The load fails before the model is read; nothing else changes | `LAI-SVC-159` |
| Generate while no music model is loaded (including survival) | `409`; the UI offers **Switch to Music** | `LAI-SVC-151` |
| Engine crashes during a song | The operation fails, the model is marked unloaded, the engine is not restarted by itself | `LAI-SVC-152` |
| Fifth request while four are queued or running | `429` | `LAI-SVC-153` |
| Lyrics too long, empty lyrics and tags, bad type, sample too big or too long, language not sung | `400`/`413` with `details.field` | `LAI-SVC-154` |
| Disk below `storage.min_free_bytes` (before or during) | `507`; nothing half-written is kept | `LAI-SVC-155` |
| Disk full while moving a song (`ENOSPC`) | The partial file is removed, the operation fails | `LAI-SVC-155` |
| Cloud sample download fails or its hash differs | Command fails, nothing generated | `LAI-SVC-156` |
| Cloud upload of a song fails after retries | Command fails; songs kept locally with `origin: cloud` | `LAI-SVC-157` |
| A person cancels, a workload change or survival stops the engine | Operation `cancelled`; finished songs kept | `LAI-SVC-158` |
| Agent restarts during a song | The engine keeps running (it is its own unit) but the agent forgets the operation: at start the agent cancels every shim task, empties `work/`, and the operation is gone (its reserved song ids were never published) | — |
| The engine does not free GPU memory on stop | Warning; the next load re-measures | `LAI-LLM-042` |
| Two requests for different music models | The engine holds one; loading the other evicts the first (llama-router.md §4.15.1) | — |

## 6. Security considerations
- **Loopback only.** The shim listens on `127.0.0.1:8082`; the unit has `IPAddressDeny=any` with `IPAddressAllow=localhost`, so the engine cannot reach the internet even if a library tries. The agent never proxies the port.
- **No runtime downloads** ([ADR-0011](../decisions/0011-python-inference-engines.md)): the offline environment variables are set in the unit, weights come from the verified content root, the venv is built from the kit bundle with hashes.
- **Paths.** The shim accepts `sample_path` and `output_dir` only inside `MUSIC_WORK_DIR`, after resolving symlinks. The agent picks every path; nothing from a request becomes a path.
- **Privacy.** Lyrics, tags and sample bytes never appear in logs, audit lines, events, doctor details or the cloud state snapshot: only sizes, hashes and ids. Lyrics are kept only in the song's `meta.json` (localai 0640). The sample is deleted after the song is made.
- **Auth.** Every music route needs an operator session or token; deleting a song needs admin (the PIN in the desktop and the UI), like deleting a model. The Portal has no music routes.
- **Cloud.** The node only pulls (command, sample) and pushes (songs) over https through `netx`; signed URLs are never logged or stored.
- **Rights.** The UI and the runbook remind people to use only audio and lyrics they own or have the rights to use.

## 7. Observability
- Logs `component=music` with `op_id`, `song_id`, `model_id`, `phase`, `duration_ms`, `bytes`, `code`; never lyrics, tags or sample bytes. The shim logs to the journal (`aictl logs --unit music`), in English, with the same rule.
- Events: `music.progress`, `music.done` (agent.md §3.10); load and unload events are the router's `model.*` and `workload.*`.
- Counters (`/status?counters=1`): songs made, failed, cancelled, mean seconds per second of audio, queue high-water mark.
- Audit: `music.generate` (sizes, ids, `sample: true|false`, `origin`), `music.cancel`, `music.song.delete`.
- The cloud learns the node's music state from the loaded models and workloads already in the state snapshot; no song list is ever sent to the cloud.

## 8. Doctor codes (LAI-SVC, block 150–179)

Shown in the local UI, the desktop and the cloud, so both languages are given. The next free code of the block is 160.

| Code | Sev | Meaning | Fix (en) | Fix (es) |
|---|---|---|---|---|
| LAI-SVC-150 | error | Music runtime not installed | Press **Install what's missing**, or run `aictl ensure`. | Pulse **Instalar lo que falta**, o ejecute `aictl ensure`. |
| LAI-SVC-151 | warning | Music is not loaded (hint: switch to the Music workload) | Press **Switch to Music**, or run `aictl workloads activate music`. | Pulse **Cambiar a Música**, o ejecute `aictl workloads activate music`. |
| LAI-SVC-152 | error | Music engine crashed during a song | Press **Switch to Music** again and retry; if it repeats, run `aictl logs --unit music`. | Pulse **Cambiar a Música** otra vez y reintente; si se repite, ejecute `aictl logs --unit music`. |
| LAI-SVC-153 | warning | Music queue full | Wait for a song to finish, then try again. | Espere a que termine una canción y vuelva a intentarlo. |
| LAI-SVC-154 | warning | Sample or input rejected (type/size/length) | Use a WAV, MP3, FLAC or Ogg sample under 20 MB and 60 seconds, and keep the lyrics short. | Use una muestra WAV, MP3, FLAC u Ogg de menos de 20 MB y 60 segundos, y acorte la letra. |
| LAI-SVC-155 | error | Not enough disk space to save the song | Delete songs you no longer need, or free space on the disk, then try again. | Borre canciones que ya no necesite, o libere espacio en el disco, y vuelva a intentarlo. |
| LAI-SVC-156 | error | Cloud request: sample download or checksum failed | Send the request again from ai.marin.cr; if it repeats, upload the sample again. | Envíe la solicitud otra vez desde ai.marin.cr; si se repite, suba la muestra de nuevo. |
| LAI-SVC-157 | error | Cloud request: song upload failed | The song is kept on this computer; check the internet connection and send the request again. | La canción quedó en esta computadora; revise la conexión a internet y envíe la solicitud otra vez. |
| LAI-SVC-158 | info | Song cancelled | Nothing to do. | Nada que hacer. |
| LAI-SVC-159 | error | GPU driver/CUDA too old for the music runtime | Run `aictl doctor` to see the driver version, then update the graphics driver from the rebuild kit. | Ejecute `aictl doctor` para ver la versión del controlador y actualice el controlador gráfico desde el kit. |

Load and unload failures of the engine are `LAI-LLM-040` to `LAI-LLM-043` ([llama-router.md](llama-router.md) §8); workload codes are `LAI-LLM-050` to `LAI-LLM-053`.

## 9. Test plan

### 9.1 Unit
- **Shim (`engines/acestep/tests`, pytest, fake backend, no GPU, no network):** `/health` in each state; `POST /v1/tasks` accepted, every validation refusal (`400`), queue full at `MUSIC_MAX_QUEUE` (`429`), a path outside the work directory and a symlink escaping it refused; task states `queued` → `running` → `done` with outputs, `failed` with the backend's error, `cancelled` (queued and running); seeds per variation; `--self-test`; a test that fails if any module opens a non-loopback socket (socket patched).
- **Agent `internal/music` (fake shim over HTTP, fake clock, temp dirs):** every limit of §3.6 at its boundary; sample type sniffing (each type and a renamed text file), the 20 MiB stream cut; disk guard before and between songs, `ENOSPC` while moving; the queue (four accepted, fifth `429`, order, cancel queued and running); progress events at most once per second; shim `failed`, crash mid-song, fewer outputs than asked; the store (atomic `meta.json`, rename and cross-filesystem copy with hash check, list order, cursor, damaged directory, delete and repeated delete); start-up clean-up of `work/`; the log scrubber test proves no lyric line reaches a log or an audit line.
- **Cloud command:** strict decoding (unknown field, `results` length, `http` URL); sample download (status, size, hash mismatch → `LAI-SVC-156`); PUT retries and `LAI-SVC-157` with the songs kept; result shape; signed URLs absent from logs.
- **Adapter and workloads:** llama-router.md §9.1 (engine adapter and workload rows).

### 9.2 Integration
- The agent with the real shim on the fake backend (`python -m localai_music_shim` with its fake backend, in CI): switch to the `music` workload, generate two variations with a sample, list, play a Range, cancel one, delete one, switch back to `coding`; then enter survival and see the engine stopped first.
- `systemd-analyze verify` on the rendered unit (`scripts/verify-units.sh`).

### 9.3 Manual on a CUDA node (after #505)
1. `aictl workloads activate music` → the coding model is stopped, the engine starts (time recorded, `TODO(#505)`).
2. Make a 60 s song from invented lyrics, two variations; listen; download; delete (PIN asked in the desktop).
3. Add a 20 s sample of your own in each mode; the `same_song` result follows the sample's melody.
4. Pull the network cable; repeat step 2; it works.
5. Start survival mode while a song is running: the song is cancelled, the survival model loads, the mode is entered.
6. `aictl workloads activate coding` → the engine stops and its GPU memory is freed (`nvidia-smi`).

## 10. Implementation breakdown
1. **#508 Engine adapters** — llama-router.md §4.15 (the `acestep` adapter, `music.env`, the unit writer, survival `engines_stop`).
2. **#509 Workloads** — llama-router.md §4.16, agent.md §3.8.34, aictl.md §4.12a.
3. **#510 Music API** — §3.4 (the shim), §3.6, §4.3 to §4.7, agent.md §3.8.35 and §3.8.36, `LAI-SVC-151` to `LAI-SVC-158`.
4. **#511 Installer and kit** — §3.1, the wheel bundle ([rebuild-kit.md](rebuild-kit.md)), the ensure row, the catalog entry with the spike's hashes, `LAI-SVC-150`, `LAI-SVC-159`.
5. **#512 Local UI** — §3.7.
6. **#513 and #514 Cloud** — §4.7 and [cloud-app.md](cloud-app.md).
7. **#515 Desktop** — §4.8 and [desktop.md](desktop.md).
8. **#516 Runbook** — [runbooks/12-make-music-and-switch-workloads.md](../runbooks/12-make-music-and-switch-workloads.md).

## 11. Open questions (with proposed defaults)
1. **MP3 output.** Smaller files for the cloud. Default: WAV only until the shim can encode MP3 offline from the bundle.
2. **A song length cap per node.** A long song on a small GPU may not fit. Default: the global 240 s cap; the spike decides whether small cards need less (`TODO(#505)`).
3. **Keeping the sample.** Default: not kept (privacy and disk). Revisit if people ask to re-run a request.
