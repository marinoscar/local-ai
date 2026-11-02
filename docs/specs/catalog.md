# Spec: Catalog (models, ark content, profiles)

> **Status:** approved design · **Epics:** E2 (models), E3 (content) · **Code:** `agent/internal/catalog/` · **Data:** `catalog/` · **Doctor area:** `LAI-CAT`
> Canonical names: [overview.md](../architecture/overview.md) §3.

The catalog is **data, not code**. It says what exists (models and content), what each node class should keep (profiles), and how to verify every byte. Every node stores the catalog version it is using in `/etc/localai/catalog/`, so a node offline for 10 years still knows exactly what it has and why.

## 1. Files and distribution
```
catalog/
  models.yaml           LLMs, VLMs, speech models
  ark.yaml              Kiwix ZIMs, map extracts, rebuild-kit items, docs bundle
  profiles/
    small-8gb.yaml      8 GB VRAM CUDA laptops (ai-n3)
    cuda-12gb.yaml      12–16 GB CUDA (ai-n2)
    max-128gb.yaml      large unified-memory nodes (ai-n1)
    cpu-only.yaml       any machine without a usable GPU (emergency fallback)
    ark-drive-full.yaml everything, for the portable AI-ARK drive
  CATALOG_VERSION       single line: YYYY.MM.DD-N
```

**Source of truth:** the `catalog/` directory on `main` in this repo. Changes are made with a PR, and CI runs `aictl catalog lint catalog/` (§6).

**How nodes get it**, in priority order:
1. **Enrolled and online:** the cloud serves the current approved catalog at `GET /api/nodes/{id}/localai/catalog` (cloud-app.md §3.6). The cloud imports it from the repo release, and an admin can override it there.
2. **Not enrolled, online:** `https://raw.githubusercontent.com/marinoscar/local-ai/<tag>/catalog/…`. The tag is `catalog-<CATALOG_VERSION>`, and `node.yaml: catalog.channel` names it (default `stable`). **`stable` resolution:** the agent lists `GET https://api.github.com/repos/marinoscar/local-ai/git/matching-refs/tags/catalog-` (unauthenticated; the 60 requests/h limit is ample for nightly use) and takes the highest `catalog-<YYYY.MM.DD-N>` tag (compared field by field as numbers). If the API call fails (rate limit, outage), it falls back to the moving tag `catalog-stable` through raw.githubusercontent.com. The catalog release workflow (CAT-04) is the **only** tagger: one run tags `catalog-<V>` for catalog **and** docs together and moves `catalog-stable` to the same commit.
3. **Offline:** the copy in `/etc/localai/catalog/`, or `<ark>/localai/kit/catalog/` from the ark drive (written by the `repo_docs` resolver, [ark-sync.md](ark-sync.md) §4.9.8).

The agent reports where the active catalog came from as `GET /api/v1/catalog` `source` ([agent.md](agent.md) §3.8.21): `cloud` (1), `github` (2), `offline` (3, the `/etc/localai/catalog/` copy kept from an earlier refresh), `kit` (loaded at startup from `<root>/kit/catalog/` because `/etc/localai/catalog/` was missing or invalid, agent.md §4.1 step 8), or `test` (a draft catalog installed with `aictl catalog use <dir> --test`, [aictl.md](aictl.md) §4.11.4; it carries a `TEST` marker file and stays until `aictl catalog restore` or the next successful `catalog refresh`). A test catalog is how a maintainer runs `aictl bench` on a draft model entry before the PR.

A new catalog is accepted only if `aictl catalog lint` passes on it (the agent runs the same validator). Rejected catalogs are logged as `LAI-CAT-002` and the previous one is kept.

## 2. `models.yaml`

```yaml
schema_version: 1
models:
  - id: qwen3.5-9b-q4km                  # [a-z0-9][a-z0-9.\-]{2,63}; unique; == router preset name == OpenAI model id
    display_name: "Qwen 3.5 9B"          # short, shown in UIs
    summary: "Fast general assistant. Good for chat, writing and simple code."   # plain language, ≤ 120 chars
    summary_es: "Asistente general rápido. Bueno para conversar, escribir y código simple."   # optional translations: summary_<lang>
    engine: llama                        # llama | whisper | piper
    roles: [chat, reasoning, coder]      # chat | reasoning | coder | vision | embedding | stt | tts
    languages: [en, es, multi]
    license: apache-2.0                  # SPDX id or "custom:<url>"
    survival_tier: 1                     # 1 = essential, 2 = useful, 3 = nice-to-have (sync priority & portal ordering)
    source:
      type: huggingface                  # huggingface | url
      repo: ggml-org/Qwen3.5-9B-GGUF     # huggingface only
      revision: 0123456789abcdef0123456789abcdef01234567   # REQUIRED: pinned 40-hex commit; never "main"
    files:
      - kind: weights                    # weights | mmproj | draft | chat_template | whisper | piper_onnx | piper_config
        name: Qwen3.5-9B-Q4_K_M.gguf     # path within repo (huggingface) or URL basename
        url: null                        # required when source.type == url
        size_bytes: 5870000000
        sha256: "<64 lowercase hex>"     # REQUIRED; for HF this equals the LFS oid
    requirements:
      backends: [cuda, vulkan, cpu]      # where it is allowed to run
      min_vram_mib: 6400                 # full offload at llama.ctx; 0 for cpu-only/whisper/piper
      min_ram_mib: 8192                  # system RAM needed (cpu path or unified memory)
      min_disk_bytes: 5900000000         # usually sum(files.size_bytes)
    llama:                               # engine == llama only; becomes the router preset (llama-router.md §4.2)
      ctx: 32768
      n_gpu_layers: 99                   # "all" | integer
      flash_attn: "on"                   # on | off | auto
      parallel: 1
      speculative:                       # optional
        type: draft-mtp                  # draft-mtp | draft
        n_max: 3
        draft_file_kind: null            # draft → refers to files[kind=draft]; draft-mtp with built-in head → null
        draft_ngl: all
      mmproj_file_kind: null             # vision → "mmproj"
      chat_template: null                # optional override: a builtin template name, or the file kind `chat_template` (files[kind=chat_template])
      extra_args: []                     # list of raw llama-server args, e.g. ["--jinja"]; lint rejects host/port/api-key/model args
      env: {}                            # e.g. {GGML_VK_SUBALLOCATION_BLOCK_SIZE: "4294967296"}
    client_hints:                        # used by `aictl key show` snippets and the gateway /v1/models
      max_output_tokens: 8192
      reasoning: true                    # emits reasoning_content; clients need larger max_tokens
```

**Rules (lint-enforced):**
- `id` is unique across `models.yaml` **and** `ark.yaml`.
- `source.revision` must be a 40-hex commit. Floating refs are rejected, because offline reproducibility depends on it.
- Every `files[*]` entry has `sha256` and `size_bytes`.
- `engine: llama` requires a `llama` block. `roles` containing `vision` requires `llama.mmproj_file_kind` and a matching file. `speculative.type: draft` requires a `draft` file and `speculative.draft_file_kind: draft`. `chat_template: chat_template` requires a `files[kind=chat_template]` entry.
- Every `llama` field has a fixed mapping to a router preset key or the environment drop-in ([llama-router.md](llama-router.md) §3.5.1). Legacy model-name aliases are **not** catalog data; they live in `node.yaml: llama.legacy_aliases` ([agent.md](agent.md) §3.4).
- `extra_args` must not contain `--host`, `--port`, `--api-key*`, `-m`, `--model`, `--models-*`, `--alias` or `-hf`. The router controls these (llama-router.md §4.2).
- `min_vram_mib` must be measured, not guessed. A PR that adds a model must include the measured value in its description: `nvidia-smi` or `radeontop` while the model is loaded at `ctx`, plus a 10% margin.

### 2.1 Initial model set (to be finalized in CAT-02 "Populate initial models")
| id (proposed) | Engine | Role | Target profiles | Notes |
|---|---|---|---|---|
| `qwen3.5-9b-q4km` | llama | chat/reasoning/coder | small-8gb, cuda-12gb, cpu-only (slow), ark-drive-full | Running on ai-n3 today (`~/models/qwen3.5-9b-mtp/Qwen3.5-9B-Q4_K_M.gguf`, built-in MTP head) |
| `qwen3.6-35b-a3b-q4km` | llama | chat/reasoning | cuda-12gb (partial offload), max-128gb | Running on ai-n2 today. Exact file and flags must be recorded from the node, because they are not documented yet |
| `qwen3-coder-next-q4km` | llama | coder | max-128gb | Running on ai-n1 today (about 48.4 GB, ctx 131072) |
| `qwen3.8-27b-q4km` (+ MTP draft) | llama | reasoning | max-128gb | Documented in [source/ms-s1-max-llamacpp-runbook.md](../source/ms-s1-max-llamacpp-runbook.md) |
| small VLM (e.g. a Qwen-VL / Gemma-vision GGUF with mmproj that fits 8 GB) | llama | vision | all GPU profiles | Must answer "what plant / injury / part is this" from photos. Choose the best that fits 8 GB at ctx ≥ 8192 |
| larger VLM | llama | vision | cuda-12gb, max-128gb | |
| tiny fallback LLM (≤ 4B, Q4) | llama | chat | cpu-only, all profiles as tier-1 backup | Must run on CPU at ≥ 5 tok/s on the i7-10850H |
| embedding model (small GGUF) | llama | embedding | all | For future offline RAG over ZIMs/manuals |
| `whisper-small` / `whisper-medium` / `whisper-large-v3-turbo` multilingual | whisper | stt | small-8gb: small, others: medium/large-v3-turbo | English and Spanish |
| `piper-en-medium`, `piper-es-medium` | piper | tts | all | One English and one Spanish voice minimum. The upstream voice (e.g. `en_US-lessac-medium`, `es_MX-<voice>-medium` or `es_ES-…`) is only in the file names |

**Speech model ids** are lowercase slugs like every model id: `whisper-<size>` (`whisper-small`, `whisper-medium`, `whisper-large-v3-turbo`) and `piper-<lang>-<quality>` (`piper-en-medium`, `piper-es-medium`). The id stays stable when the voice behind it changes. An `engine: piper` model must have exactly one `piper_onnx` file and one `piper_config` file (the `.onnx` and its `.onnx.json`); there is no speaker field, because the initial voices are single-speaker ([companion-services.md](companion-services.md) §4.3.2). An `engine: whisper` model has exactly one `whisper` file.

## 3. `ark.yaml`

```yaml
schema_version: 1
content:
  - id: wikipedia-en-nopic
    kind: kiwix                          # kiwix | maps | kit | docs
    display_name: "Wikipedia (English, text only)"
    summary: "All of English Wikipedia without pictures."
    survival_tier: 1
    languages: [en]
    license: "CC BY-SA 4.0"
    source:
      type: kiwix                        # kiwix | url | pmtiles_extract | apt_closure | git_mirror | github_release | repo_docs
      # kiwix: resolves the newest dated file matching the book name by parsing the download.kiwix.org directory listing at base_url (ark-sync.md §4.9.2)
      book: wikipedia_en_all_nopic       # Kiwix book name (without date)
      base_url: https://download.kiwix.org/zim/wikipedia/
      version_policy: latest             # latest | pinned
      pinned: null                       # {file: wikipedia_en_all_nopic_2026-09.zim, sha256: ...} when pinned
      checksum: sidecar                  # sidecar = <url>.sha256 published by Kiwix ; pinned = use pinned.sha256
    expected_size_bytes: 50000000000     # approximate; used for planning; actual comes from HEAD Content-Length
    refresh_days: 90                     # don't re-download newer versions more often than this

  - id: osm-central-america
    kind: maps
    display_name: "Offline map: Costa Rica & Central America"
    summary: "Streets, towns, rivers and roads. Works without internet."
    survival_tier: 1
    source:
      type: pmtiles_extract              # runs `pmtiles extract <planet_url> <out> --bbox=... --maxzoom=...`
      planet_url_pattern: "https://build.protomaps.com/{date}.pmtiles"   # {date} = newest available build (YYYYMMDD)
      bbox: [-92.5, 7.0, -77.0, 18.6]    # minLon, minLat, maxLon, maxLat
      maxzoom: 15
      checksum: computed                 # sha256 computed after extract + `pmtiles verify` must pass
    expected_size_bytes: 2500000000
    refresh_days: 180

  - id: map-assets
    kind: maps
    display_name: "Map styles and fonts"
    source:
      type: github_release               # assets needed to render maps offline (style json, glyphs, sprites)
      repo: protomaps/basemaps-assets
      tag: "<pinned tag>"
      asset: "<asset name>"
      sha256: "<64 hex>"
      extract: true                      # optional (github_release | url): unpack the verified archive and store it as a directory item (ark-sync.md §4.9.4)

  - id: kit-llama-cpp
    kind: kit
    display_name: "llama.cpp source (rebuild kit)"
    source:
      type: git_mirror
      url: https://github.com/ggml-org/llama.cpp.git
      also_tarball_of: pinned            # pinned = the commit in node.yaml llama.commit ; also produces src/llama.cpp-<commit>.tar.gz

  - id: kit-debs-resolute-amd64          # kit-debs-<codename>-<arch>; one item per codename/arch in use
    kind: kit
    source:
      type: apt_closure
      codename: resolute                 # Ubuntu 26.04 LTS, what the current nodes run. MUST match each node's codename (rebuild-kit.md §3.3 pins)
      arch: amd64
      packages: [build-essential, cmake, ninja-build, pkg-config, ccache, git,          # = rebuild-kit.md §4.1.4 + nvidia-cuda-toolkit
                 libvulkan-dev, glslc, spirv-headers, vulkan-tools, mesa-vulkan-drivers,
                 curl, jq, ca-certificates, iw, network-manager, pciutils, usbutils, minisign, zstd, udisks2,
                 openssh-server, python3, tailscale, nvidia-cuda-toolkit]
      extra_repos:                       # optional: third-party apt repos added to the private apt tree (ark-sync.md §4.9.7)
        - name: tailscale
          uri: https://pkgs.tailscale.com/stable/ubuntu
          suite: resolute
          components: [main]
          key: tailscale-archive-keyring.gpg   # a key file embedded in the agent (rebuild-kit.md §3.4); never fetched at sync time
        - name: nvidia-cuda
          uri: https://developer.download.nvidia.com/compute/cuda/repos/<ubuntu-version-no-dot>/x86_64/
          suite: /
          components: []
          key: nvidia-cuda-archive-keyring.gpg

  - id: kit-nvidia-resolute-amd64-595-open   # kit-nvidia-<codename>-<arch>-<branch>; CUDA nodes only
    kind: kit
    source:
      type: apt_closure
      codename: resolute
      arch: amd64
      packages: [nvidia-driver-595-open, nvidia-utils-595]   # full list: rebuild-kit.md §4.1.5
      extra_repos: []

  - id: places-central-america           # offline place search (companion-services.md §4.4.4)
    kind: maps
    display_name: "Place names: Central America"
    survival_tier: 1
    license: "CC BY 4.0"
    source:
      type: url
      files:                             # url items: either `url` + `sha256` (one file) or a `files` list
        - {url: "https://download.geonames.org/export/dump/CR.zip", sha256: null}
        - {url: "https://download.geonames.org/export/dump/PA.zip", sha256: null}
        # … NI, HN, SV, GT, BZ
      version_policy: latest             # latest | pinned
      checksum: computed                 # pinned (default for url) | computed | sidecar
      trust: tofu                        # REQUIRED with checksum: computed on a url item: trust on first use (ark-sync.md §4.9.5)
    expected_size_bytes: 40000000
    refresh_days: 180

  - id: wikimed-en
    kind: kiwix
    survival_tier: 1
    source: {type: kiwix, book: mdwiki_en_all_maxi, base_url: "https://download.kiwix.org/zim/other/", version_policy: latest, checksum: sidecar}   # book name verified in CAT-03
    portal_links:                        # optional, kiwix items only: first-aid links of the Survival Portal (survival-portal.md §3.4)
      - key: bleeding                    # stable key, unique across the catalog; also the icon name
        title: {en: "Severe bleeding", es: "Sangrado grave"}
        article: "Bleeding"              # path inside the ZIM, as kiwix-serve shows it after /content/<book>/
        es_item: wikimed-es              # optional: Spanish kiwix item to use when the UI is Spanish
        es_article: "Hemorragia"         # required when es_item is set
        order: 10

  - id: docs-bundle
    kind: docs
    source:
      type: repo_docs                    # renders docs/ (runbooks + specs) to HTML+PDF via `aictl docs render`
```

**Rules:** `id` follows the same format as models. A `kiwix` item with `version_policy: latest` must have `refresh_days ≥ 30`. A `pmtiles_extract` item must have a bbox with min < max. A `kit` or `docs` item is always survival tier 1.

**Source types** (each has exactly one resolver in [ark-sync.md](ark-sync.md) §4.9; adding a type means adding a resolver there):
| `source.type` | File | Resolver |
|---|---|---|
| `huggingface` | models.yaml | ark-sync.md §4.9.1 |
| `url` | models.yaml, ark.yaml | ark-sync.md §4.9.5 |
| `kiwix` | ark.yaml | ark-sync.md §4.9.2 |
| `pmtiles_extract` | ark.yaml | ark-sync.md §4.9.3 |
| `github_release` | ark.yaml | ark-sync.md §4.9.4 |
| `git_mirror` | ark.yaml | ark-sync.md §4.9.6 |
| `apt_closure` | ark.yaml | ark-sync.md §4.9.7 |
| `repo_docs` | ark.yaml | ark-sync.md §4.9.8 (also writes `kit/catalog/`: catalog files, `CATALOG_VERSION`, `pins.json`, from the same tag tarball in the same item install; rebuild-kit.md §3.2) |

**Optional fields** (lint-enforced):
- `extract: true` (`github_release` and `url` only): the verified archive (`.tar.gz`, `.tar.xz` or `.zip`) is unpacked and stored as a directory item. `map-assets` uses it.
- `extra_repos` (`apt_closure` only): list of `{name, uri, suite, components, key}`. `key` must name a keyring embedded in the agent ([rebuild-kit.md](rebuild-kit.md) §3.4); lint rejects unknown key names.
- `checksum` on `url` items: `pinned` (default; `sha256` required per file), `sidecar` (`sidecar_url` names a `SHA256SUMS`-style file; used by `kit-ubuntu-iso-<ver>`, rebuild-kit.md §4.1.7) or `computed`. `checksum: computed` requires `trust: tofu`, and lint allows `tofu` only on `kind: maps` data items such as `places-central-america`.
- `portal_links` (`kiwix` only): `key` unique across the catalog, `article` non-empty, `es_item` (if set) must be an existing `kiwix` item and then `es_article` is required.
- Kit item ids: `kit-debs-<codename>-<arch>` and `kit-nvidia-<codename>-<arch>-<branch>`. `<codename>` is the Ubuntu codename of the nodes that use the item (`resolute` = 26.04 LTS today); lint checks that the id matches `source.codename`/`source.arch`. A node whose codename has no item gets `LAI-KIT-042`.

### 3.1 Initial content set (finalized in CAT-03 "Populate initial ark content")
Kiwix books (exact book names verified against the Kiwix OPDS catalog during implementation):
- **Tier 1 (all profiles):**
  - Wikipedia EN: `wikipedia-en-nopic` (small-8gb) and `wikipedia-en-maxi` (cuda-12gb, max-128gb, ark-drive-full)
  - WikiMed / mdwiki (medical, EN)
  - WikiHow EN
  - iFixit EN
  - the Kiwix "post-disaster" / zimgit survival collections (water, first aid, food, power)
  - Wikipedia ES (nopic): `wikipedia-es-nopic`
- **Tier 2:**
  - Wikibooks EN
  - Wikivoyage EN/ES
  - Stack Exchange (diy, electronics, cooking, gardening, outdoors, ham radio, superuser, askubuntu, unix, stackoverflow-nopic)
  - Project Gutenberg (EN, subset)
  - Khan Academy / LibreTexts where available
  - WikiMed ES
- **Tier 3:** Wiktionary EN/ES, TED, and the remaining large collections (ark drive only).

Maps:
- `osm-central-america` (tier 1, all profiles)
- `osm-north-america` (tier 3, ark drive only)
- `map-assets` (tier 1)

Kit: everything in [rebuild-kit.md](rebuild-kit.md), tier 1.

## 4. Profiles (`profiles/<id>.yaml`)

```yaml
schema_version: 1
id: small-8gb
display_name: "Small GPU laptop (8 GB VRAM)"
match:                       # used by installer & `aictl profile suggest`; see the selection rule below
  backends: [cuda]
  vram_mib: {min: 7000, max: 11999}
  ram_mib: {min: 12000}
router:
  models_max: 1              # llama --models-max; lint: ≥ 1 (LAI-CAT-018)
  memory_margin_mib: 512     # llama --models-memory-margin
  autoload: false            # lint: must be false (LAI-CAT-018); the unit always passes --no-models-autoload and the agent loads explicitly (llama-router.md §4.7)
storage:
  target: internal           # internal | ark_drive
  budget_bytes: 220000000000 # max bytes this profile may occupy in the content root
  min_free_bytes: 25000000000 # never let the filesystem drop below this
include:
  models: [qwen3.5-9b-q4km, <small-vlm-id>, <tiny-fallback-id>, <embedding-id>, whisper-small, piper-en-medium, piper-es-medium]
  content: [wikipedia-en-nopic, wikimed-en, wikihow-en, ifixit-en, survival-collection, wikipedia-es-nopic,
            osm-central-america, map-assets, places-central-america, kit-llama-cpp, kit-debs-resolute-amd64, kit-nvidia-resolute-amd64-595-open,
            kit-agent-binaries, kit-agent-source, kit-go-toolchain, kit-whisper-cpp, kit-kiwix-tools, kit-pmtiles, kit-piper,
            kit-desktop, docs-bundle]
default_model: qwen3.5-9b-q4km
survival:                    # see architecture/modes.md §3
  models: [qwen3.5-9b-q4km, <small-vlm-id>]
  default_model: qwen3.5-9b-q4km
  require_services: [kiwix, whisper]
  portal_language: auto
```

**Profile selection** (installer and `aictl profile suggest`): node profiles (every profile with a `match`) are evaluated in lexical order of profile id, with `cpu-only` always last; the first whose `match` holds wins. Lint (`LAI-CAT-018`) rejects two node profiles whose `match` ranges overlap (same backend and intersecting `vram_mib` and `ram_mib` ranges), so the order only matters for `cpu-only`. `match.vram_mib` compares the same memory figure as the fit rules' `G_total` ([llama-router.md](llama-router.md) §4.3.1): dedicated VRAM on discrete GPUs, usable GTT (unified memory) on unified-memory nodes.

`ark-drive-full.yaml` sets `storage.target: ark_drive`, has no `match` (it is never auto-selected for a node), and an `include` list that is the **union of all profiles plus every tier 1–3 item**. With it, the drive can seed or rebuild any node class.

**Planning rule:** when the sum of included sizes exceeds `budget_bytes`, the sync planner drops items in reverse priority (tier 3 before tier 2 before tier 1, and within a tier the later list position goes first). It reports them as `skipped: budget` in `READY.json`, never as an error.

**Node overrides** live in `node.yaml` (agent.md §3.4):
```yaml
profile: small-8gb
profile_overrides:
  add_models: []
  remove_models: []
  add_content: [stackexchange-electronics]
  remove_content: []
  budget_bytes: null
```

## 5. Agent catalog behaviour
- The agent loads and validates the catalog at start and at each refresh. The **effective plan** for a content root is: profile include + overrides, filtered by `requirements.backends`/VRAM fit (for loadability) and by the budget.
- Models that don't fit this node's VRAM may still be **stored** (when the profile includes them, so the drive or node can hand them on). They are marked `loadable: false` with a reason in the API.
- `GET /api/v1/catalog` returns the merged view, with state per item (agent.md §3.8).

## 6. Lint (`aictl catalog lint <dir>`)
- Exit 0 means valid. Exit 2 means invalid, with one line per problem: `<file>:<yaml-path>  LAI-CAT-0NN  <message>` (two spaces between fields; the [aictl.md](aictl.md) §4.11.1 format, e.g. `catalog/models.yaml:models[3].source.revision  LAI-CAT-011  revision must be a 40-hex commit, got "main"`). `--json` is defined in aictl.md §4.11.1.
- It checks the schema (all the rules above), uniqueness, cross-references (profile includes exist, file kinds referenced exist), sha256 format and revision format.
- The same validator runs before any install: the agent's refresh (§1), `aictl catalog restore`, and `aictl catalog use <dir> --test` (aictl.md §4.11.4), which refuses with exit 2 on any problem.
- `--online` additionally HEAD-checks every URL/HF file and compares `Content-Length` with `size_bytes` and the HF LFS oid with `sha256`. CI runs this weekly, not per PR.

## 7. Doctor codes (LAI-CAT)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-CAT-001 | error | Catalog file can't be parsed | The catalog file is damaged. Run `aictl catalog restore`, which copies the last good one from the ark kit |
| LAI-CAT-002 | warning | New catalog failed validation, previous kept | Nothing to do. Report it to the maintainer |
| LAI-CAT-003 | error | The profile in node.yaml doesn't exist | Run `aictl profile suggest`, then `aictl profile set <id>` |
| LAI-CAT-004 | error | The profile references an unknown model/content id | Catalog bug. Fix it in the repo |
| LAI-CAT-005 | warning | A model doesn't fit this node (VRAM/RAM/backend) | Choose a smaller model. `aictl models --loadable` lists the ones that fit |
| LAI-CAT-010 | error | Lint: schema problem (unknown key, missing required field, wrong type, `schema_version` not 1) | Fix the YAML at the reported path |
| LAI-CAT-011 | error | Lint: pinning problem (`source.revision` not 40-hex, `sha256` not 64 hex, `size_bytes` missing) | Fix the YAML at the reported path |
| LAI-CAT-012 | error | Lint: bad id format or duplicate id across models and ark | Fix the YAML at the reported path |
| LAI-CAT-013 | error | Lint: model file rules (llama block missing, vision without mmproj, draft, chat_template file, piper needs `piper_onnx` + `piper_config`, whisper needs one `whisper` file) | Fix the YAML at the reported path |
| LAI-CAT-014 | error | Lint: forbidden `extra_args` or invalid `llama.env` key | Fix the YAML at the reported path |
| LAI-CAT-015 | error | Lint: ark item rules (kiwix `refresh_days`, bbox, kit/docs tier 1, kit id ↔ codename/arch) | Fix the YAML at the reported path |
| LAI-CAT-016 | error | Lint: optional field misused (`extract`, `extra_repos`/unknown key name, `checksum`/`trust`/`tofu`, `url` vs `files`) | Fix the YAML at the reported path |
| LAI-CAT-017 | error | Lint: `portal_links` rule broken | Fix the YAML at the reported path |
| LAI-CAT-018 | error | Lint: profile rule broken (`router.models_max` ≥ 1, `router.autoload` false, `default_model`/`survival.*` inside `include`, `ark-drive-full` has no `match`, overlapping `match` ranges) | Fix the YAML at the reported path |
| LAI-CAT-019 | error | Lint: `CATALOG_VERSION` missing or not `YYYY.MM.DD-N`, or wrong file layout | Fix the YAML at the reported path |

Cross-reference failures (a profile includes an unknown id) are `LAI-CAT-004` in lint too.

## 9. Test plan
- **Unit (table-driven, `agent/internal/catalog`):** one failing fixture per lint rule, asserting its code (`LAI-CAT-010`…`019`, `004`) and `<file>:<yaml-path>`; a valid fixture catalog passes with exit 0; strict decoding rejects an unknown key (`LAI-CAT-010`); `--json` output matches aictl.md §4.11.1; profile selection for the three lab nodes' hardware plus a no-GPU machine (each gets its documented profile, `cpu-only` last); overlapping `match` ranges rejected.
- **Install:** a rejected catalog leaves `/etc/localai/catalog/` byte-identical (`LAI-CAT-002`); an interrupted install (killed between write and rename) leaves the previous catalog; a damaged file at startup falls back to `<root>/kit/catalog/` (`source: kit`).
- **CI:** `aictl catalog lint catalog/` on every PR; `--online` weekly.

## 10. Implementation breakdown
1. **CAT-01 Schema, loader and lint** (2 d). Depends on: AGENT-01. Scope: `agent/internal/catalog/`: Go structs for `models.yaml`, `ark.yaml`, `profiles/*.yaml` and `CATALOG_VERSION` (§2–§4, including the optional fields `extract`, `extra_repos`, `checksum`/`trust`, multi-file `url`, `portal_links`, `chat_template` file kind), strict decoding (unknown keys rejected), every lint rule of §2–§4 as `LAI-CAT-010..019` with `<file>:<yaml-path>` locations, cross-references (profile includes, file kinds, `es_item`), `aictl catalog lint [--online] [--json] <dir>` (§6), the §1 source priority and atomic install into `/etc/localai/catalog/` with `LAI-CAT-001/002`, CI job running lint on `catalog/` per PR. Owns the linter **and** the `aictl catalog lint` command ([aictl.md](aictl.md) §10 AICTL-07 only verifies output conformance). Acceptance: the §9 test plan; table-driven tests with one failing fixture per rule; `--json` output matches [aictl.md](aictl.md) §4.11.1; a rejected catalog leaves the previous one in place.
2. **CAT-02 Populate initial models** (2 d). Depends on: CAT-01, ROUTER-10. Scope: the §2.1 set in `catalog/models.yaml` with pinned HF revisions, sha256 and sizes; `min_vram_mib` **measured** on each node class with `aictl bench` (ai-n1 Vulkan 128 GB, ai-n2 CUDA 12 GB, ai-n3 CUDA 8 GB) plus 10 %; the exact ai-n2 35B-A3B flags captured by the migration preflight ([llama-router.md](llama-router.md) §11); whisper and piper entries. Acceptance: `aictl catalog lint --online catalog/` passes; each PR description carries the bench output; the three running models are present with the sha256 of the files on the nodes.
3. **CAT-03 Populate initial ark content** (1.5 d). Depends on: CAT-01, SYNC-05, SYNC-09, SYNC-10, SYNC-11 (the dry-run acceptance needs the planner and the kiwix/pmtiles, url/github_release and repo_docs resolvers). Scope: the §3.1 Kiwix books with book names **verified against the Kiwix OPDS catalog** (`https://library.kiwix.org/catalog/v2/entries`), maps (`osm-central-america`, `osm-north-america`, `map-assets`), `places-central-america`, `docs-bundle`, first-aid `portal_links` with article paths checked in the real ZIMs ([survival-portal.md](survival-portal.md) §3.4), `expected_size_bytes` and `refresh_days`. Kit items are KIT-01. Acceptance: lint passes; a dry-run sync on ai-n3 (`aictl ark sync --dry-run`) resolves every item; every `portal_links` article opens in kiwix-serve.
4. **CAT-04 Catalog distribution** (1 d). Depends on: CAT-01. Scope: release workflow that tags `catalog-<CATALOG_VERSION>` when `catalog/CATALOG_VERSION` changes on `main` (lint must pass), the moving `catalog-stable` tag, `catalog.channel: stable` resolution for unenrolled nodes (§1: GitHub matching-refs API, `catalog-stable` fallback), and the contract consumed by the cloud import ([cloud-app.md](cloud-app.md) §4.6, CLOUD-10). Acceptance: a version bump on a test branch produces a tag; an unenrolled node refreshes from it; the cloud imports the same tag.
5. **CAT-05 Profile suggest and fit** (1 d). Depends on: CAT-01, AGENT-10, ROUTER-02. Scope: `match` evaluation (§4 selection rule: lexical id order, `cpu-only` last, `ark-drive-full` never auto-selected, GTT on unified memory), `aictl profile suggest` / `aiagent setup profile-suggest`, effective plan (profile include + `profile_overrides`, §4–§5), `loadable` + reason from the fit rules, `LAI-CAT-003/004/005`. Acceptance: table tests for the three lab nodes' hardware (each suggests its documented profile); `GET /profile` shows `match_reasons`; a model that doesn't fit is `loadable: false` with `LAI-CAT-005`.
