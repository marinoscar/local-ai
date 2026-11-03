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

**Install and fetch (`internal/catalog`).** `Install(dir, newDir)` lints `newDir`, copies it to `catalog.new/` next to the live directory (every file fsynced), lints the copy, removes `catalog.prev/`, renames `catalog/` to `catalog.prev/` and `catalog.new/` to `catalog/`. A catalog that fails lint is a rejection (`LAI-CAT-002`) and leaves the live directory byte-identical. A crash between the two renames leaves no `catalog/`; `Recover` (run by `Install` and by the agent at start) moves `catalog.prev/` back, so a node never loses its last good catalog. A symlink or other special file in the source is an install error. The `TEST` marker is written only by `Install` with the test option (`aictl catalog use --test`) and is never copied from a source directory.

The cloud and GitHub sources sit behind two small interfaces and deliver a **bundle** (the files of the catalog directory with their content, the shape of the cloud body in [cloud-app.md](cloud-app.md) §3.6). A bundle is written to a temporary directory only after each `sha256` matches, every path is one of `CATALOG_VERSION`, `models.yaml`, `ark.yaml`, `profiles/<id>.yaml`, there are no duplicates and the total is at most 2 MB. A source that fails or delivers a damaged bundle is skipped and the next one is tried; the first intact bundle is linted and installed, and if it fails lint the refresh stops with `LAI-CAT-002` instead of falling back to a lower-priority source. Startup (agent.md §4.1 step 8) loads `/etc/localai/catalog/` (`offline`, or `test` with a `TEST` marker), else the first valid `<root>/kit/catalog/` (`kit`, with `LAI-CAT-001` raised for the live copy), else an empty catalog.

**Release tags and the version rule (CAT-04).** The workflow `.github/workflows/catalog-release.yml` is the only tagger of `catalog-*` tags. On a push to `main` that changes `catalog/CATALOG_VERSION` it builds the agent from that commit, runs `aiagent ctl catalog lint catalog/` and `scripts/catalog-version-guard.sh`, then `scripts/catalog-tag.sh` creates the annotated tag `catalog-<CATALOG_VERSION>` and moves the annotated tag `catalog-stable` to the same commit in one atomic push (`catalog-stable` is the only ref ever force-pushed). A lint failure creates no tag and leaves `catalog-stable` where it was. The guard fails the job, and nothing is overwritten, when `CATALOG_VERSION` is not one line `YYYY.MM.DD-N` (a real date, `N` ≥ 1 without a leading zero), when `catalog-<V>` already exists, or when `V` is not greater than the highest existing `catalog-<YYYY.MM.DD-N>` tag (year, month, day and `N` compared as numbers, so `2026.11.02-10` > `2026.11.02-9`; the same rule as [cloud-app.md](cloud-app.md) §4.6). `catalog-stable` and tags that are not `catalog-<valid version>` are ignored by the guard. The workflow needs only the default `GITHUB_TOKEN` (`contents: write`); because GitHub does not start workflows for tags pushed with it, the signed draft release of `release-catalog.yml` runs only when the repository also holds the optional secret `CATALOG_RELEASE_TOKEN` (a token allowed to push tags). Tags serve nodes and the cloud either way. Two bumps that land back to back may leave the older one untagged (GitHub keeps only the newest queued run); the newer version supersedes it.

**GitHub source (`internal/catalog/fetch_github.go`, `catalog.NewGitHub(catalog.base_url)`).** It implements `GitHubSource` and is used by `Fetcher.Refresh` only when the node is not enrolled (or the cloud failed). Everything is HTTPS, with no proxy from the environment, no token and no cookie; a redirect to `http` is refused. Steps:
1. **Channel.** `catalog.channel` is `stable` (also when empty) or `catalog-<CATALOG_VERSION>` (a pin; no tag listing). Anything else is `LAI-CAT-007` before any request.
2. **`stable`.** `GET <api>/repos/<owner>/<repo>/git/matching-refs/tags/catalog-?per_page=100` (10 s timeout, `Link: rel="next"` pages followed on the same host, at most 10 pages). Only `ref` is used: `catalog-stable` and refs that are not `catalog-<valid version>` are ignored and the highest version wins. If the listing fails for any reason (403 or 429 rate limit, 5xx, timeout, an answer that is not a list, no catalog tag, more than 10 pages) the moving tag `catalog-stable` is used instead. `<owner>/<repo>` is the path of `catalog.base_url`; the API is `https://api.github.com` and is only used when `base_url` is on `raw.githubusercontent.com` (a mirror resolves `stable` to `catalog-stable` directly).
3. **Files.** `GET <base_url>/<tag>/catalog/<file>` (30 s timeout each) for `CATALOG_VERSION`, `models.yaml`, `ark.yaml` and every `profiles/<id>.yaml`. Raw GitHub cannot list a directory, so the profile ids come from `GET <api>/repos/<owner>/<repo>/contents/catalog/profiles?ref=<tag>`; when that call fails the five profiles of the layout above are fetched, and a missing one then fails the fetch (a profile added later is picked up on the next successful listing, never half-installed). Each file is limited to what is left of the 2 MB bundle limit.
4. **Integrity.** All files arrive before anything is handed on; one failure discards them all. A body shorter than its declared length is truncation. For a tag `catalog-<V>` the content of `CATALOG_VERSION` must equal `<V>`. Each file's `sha256` is computed from what arrived and `bundleSha256` follows [cloud-app.md](cloud-app.md) §3.6, so the same tag imported by the cloud has the same digest. The bundle then goes through the normal path (stage, lint, `Install`); a catalog that arrives whole but fails lint is `LAI-CAT-002`.
5. **Failures** are a `*FetchError` with a doctor code and the previous catalog kept: `LAI-CAT-006` (unreachable, timeout, rate limit, HTTP error), `LAI-CAT-007` (the pinned or `stable` tag has no catalog, or the channel is malformed), `LAI-CAT-008` (truncated, over the limit, a file of the tag missing, `CATALOG_VERSION` different from the tag). `errors.As(err, &fe)` through the joined error of `Fetcher.Refresh` gives the code; the agent logs it, while `POST /catalog/refresh` keeps answering a fetch no source could deliver with `502 LAI-CLD-013` ([agent.md](agent.md) §3.8.21). One refresh makes one attempt per request: the nightly sync is the retry.

**Cloud import contract** ([cloud-app.md](cloud-app.md) §4.6). For a tag `catalog-<V>` the repository archive `https://github.com/<repo>/archive/refs/tags/catalog-<V>.tar.gz` holds `catalog/CATALOG_VERSION`, `catalog/models.yaml`, `catalog/ark.yaml` and `catalog/profiles/<id>.yaml` (the layout above, nothing else under `catalog/`), and `CATALOG_VERSION` is the single line `<V>`, equal to the tag suffix. The import extracts only `catalog/**`, refuses a version that already exists (`409`) and so computes the same per-file `sha256` and `bundleSha256` as a node that fetched the tag from GitHub. The same archive also carries `docs/` at that commit: one tag serves the catalog and the docs ([overview.md](../architecture/overview.md) §3.8).

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
- `engine: llama` also requires a `files` entry of kind `weights`. `source.revision` is required for `source.type: huggingface`; a `url` source has no commit to pin, and a revision given there must still be 40-hex. A `url` source needs an `http(s)` `url` on every file. `files[*].name` is a relative path without `..` segments (the sync engine joins it to a directory). `license` is an SPDX expression or `custom:<http(s) url>`. `summary` and every `summary_<lang>` are at most 120 characters. `llama.env` keys are environment variable names and values are one line.
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

**More rules enforced by lint** (all `LAI-CAT-015` unless noted). `kind` and `source.type` agree: `kiwix` uses `kiwix`; `maps` uses `pmtiles_extract`, `url` or `github_release`; `kit` uses `git_mirror`, `apt_closure`, `url`, `github_release` or `repo_docs` (the last only for `kit-catalog`, the kit copy of the catalog, [rebuild-kit.md](rebuild-kit.md) §3.2); `docs` uses `repo_docs`. A `kiwix` item with `version_policy: pinned` or `checksum: pinned` needs the `pinned` block. A `bbox` has four numbers inside longitude -180..180 and latitude -90..90. A key that the source type does not use is a schema problem (`LAI-CAT-010`), except the optional fields `extract`, `extra_repos`, `checksum`, `trust`, `sidecar_url`, `url` and `files`, whose misuse is `LAI-CAT-016`. `survival_tier` may be left out: `Tier()` then returns 1 for `kit` and `docs` items and 3 for the rest (the first to be dropped by the budget planner). A `kit` or `docs` item that declares a tier must declare 1.

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

**How selection is computed** (CAT-05, `catalog.Suggest`, `Catalog.Suggest`; pure, the hardware snapshot is passed in):
- The facts compared are the backend (the snapshot's detected backend, `cuda`, `vulkan` or `cpu`), `G_total` for that backend (`Snapshot.FitMemory`) against `match.vram_mib`, and `MemTotal` against `match.ram_mib`. A ranged bound is inclusive and a missing bound is open; an empty `backends` list or a missing range accepts anything.
- **Missing data never matches.** A `ram_mib` range does not hold when the RAM total is unknown (0), and a `vram_mib` range does not hold on a GPU backend whose memory is unknown (0), even when the range has only a `max`. On the `cpu` backend `G_total` is a real 0 and the range is compared as is. A snapshot with nothing sampled is a CPU box.
- **Order and ties.** Node profiles are sorted by id (byte order) and `cpu-only` is taken out and tried last, so a catalog whose ranges overlap (which lint forbids) still gives one answer, whatever the order of the files. `ark-drive-full` is skipped even if it carries a `match`.
- **`cpu-only` is a true catch-all.** When no other profile holds, `cpu-only` is chosen even if its own `match` does not (for example a machine with a 4 GB GPU, or one whose RAM is unknown), with the single reason `no other profile fits this hardware`. When its own `match` holds, its conditions are the reasons. A catalog with no `cpu-only` and no match gives no suggestion (empty id).
- **Reasons** are one plain string per condition the `match` states, in this order: `backend cuda`, `vram 8192 MiB in 7000..11999`, `ram 14336 MiB in 12000..` (`min..max`, an open side left empty). A `match` that states nothing gives `no hardware requirements`.
- A node whose profile does not match its hardware keeps it; the suggestion is reported, never applied.

Every profile except `ark-drive-full` needs a `match` block (`LAI-CAT-018`). `cpu-only` is exempt from the overlap rule: it is the catch-all that is evaluated last, so a machine with a GPU too small for any other profile still lands on it. An `ark-drive-full` that carries a `match` is rejected, and so is a `match` range with `min` above `max`.

`ark-drive-full.yaml` sets `storage.target: ark_drive`, has no `match` (it is never auto-selected for a node), and an `include` list that is the **union of all profiles plus every tier 1–3 item**. With it, the drive can seed or rebuild any node class.

**Planning rule:** when the sum of included sizes exceeds `budget_bytes`, the sync planner drops items in reverse priority (tier 3 before tier 2 before tier 1, and within a tier the later list position goes first). It reports them as `skipped: budget` in `READY.json`, never as an error.

**How the plan is computed** (CAT-05, `Catalog.EffectivePlan`; pure):
- Selection: `models = include.models ∪ add_models − remove_models` and `content = include.content ∪ add_content − remove_content`, each in first-seen order (the include order, then the added ids) without duplicates. A removed id wins over an added one; removing an id that is not included is a no-op.
- Validation: an unknown profile id is `LAI-CAT-003`; any id in the profile's `include` or in the four override lists that the catalog does not have is `LAI-CAT-004` (every one is listed, with the field it was written in). A `budget_bytes` override that is not positive is refused (no doctor code: the shape of `node.yaml` is config validation's).
- Budget: `budget_bytes` is the override when set, else `storage.budget_bytes`. The items are the kept models, then the kept content. A model weighs the sum of its `files[].size_bytes`; a content item weighs `expected_size_bytes`. An item of unknown size (0) adds nothing and is never dropped (dropping it frees nothing). The tier of a model is `survival_tier` (an unset tier counts as 3), the tier of a content item is its `survival_tier`, else 1 for `kit` and `docs`, else 3.
- Drops: while the total exceeds the budget, drop the next item in reverse priority: tier 3 before tier 2 before tier 1; within a tier the later list position goes first, and content (listed after models) goes before a model of the same tier. Dropping stops as soon as the total fits. A total exactly equal to the budget drops nothing.
- Result: `models` and `content` are what is **kept**, `planned_bytes` is their size, `budget_bytes` the budget used, and `skipped` lists the dropped items in drop order (`{kind, id, reason: "budget", size_bytes}`). `effective.models` is never filtered by whether a model fits this node's hardware: a model that cannot load is still stored; only `loadable` reflects fit (section 5).
- `Catalog.BuildProfileView` builds the `GET /profile` body (`profile`, `display_name`, `overrides` with empty lists, `suggested`, `match_reasons`, `effective`). For an unknown profile or id it returns the error together with the partial view (`suggested` and `match_reasons` still filled), so the caller can say what would fit.

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
- `loadable` for every catalog model comes from `router.Env.Loadable` / `LoadableAll` (`agent/internal/router/loadable.go`), which wrap the static fit rules ([llama-router.md](llama-router.md) §4.3.2) and return the reason (`LAI-CAT-005` for backend, graphics memory or RAM, `LAI-LLM-008` for missing files) with the plain-language message. It lives in the router package, not here, because `router` imports `catalog`. Every engine is judged; for `whisper` and `piper` the graphics-memory rule is skipped (their `min_vram_mib` is 0 by the catalog rules, and a stray value is ignored), so the backend and memory rules apply.
- `GET /api/v1/catalog` returns the merged view, with state per item (agent.md §3.8).

## 6. Lint (`aictl catalog lint <dir>`)
- Exit 0 means valid. Exit 2 means invalid, with one line per problem: `<file>:<yaml-path>  LAI-CAT-0NN  <message>` (two spaces between fields; the [aictl.md](aictl.md) §4.11.1 format, e.g. `catalog/models.yaml:models[3].source.revision  LAI-CAT-011  revision must be a 40-hex commit, got "main"`), then `✗ N problems found.`; a valid catalog prints `✓ Catalog <version> is valid (...)`. Everything goes to stdout; stderr is only for usage errors. A problem about a whole file has an empty yaml-path, so its line starts `<file>:  LAI-CAT-0NN`. The file is the directory as typed plus the relative name; in `--json` it is relative to the directory. Lines are ordered `CATALOG_VERSION`, `models.yaml`, `ark.yaml`, `profiles/*` and by line inside a file. `--json` is defined in aictl.md §4.11.1 and never prints anything to stderr.
- It checks the schema (all the rules above), uniqueness, cross-references (profile includes exist, file kinds referenced exist), sha256 format and revision format.
- **Strict decoding** works on the YAML node tree: an unknown key, a wrong type (a number where a string is expected: quote it), a duplicate key, a YAML alias, a second document in one file, an empty file or a missing required field is `LAI-CAT-010` with the path of the node. `summary_<lang>` keys are the only open key family. A missing, unreadable, oversized (> 4 MB) or non-regular file, a stray file in `profiles/` (anything but `<id>.yaml`), a missing or empty `profiles/` and a profile whose `id` differs from its file name are `LAI-CAT-019`. Other files in the directory (`TEST`, `pins.json`) are ignored. When a file has schema problems, the rules that need its content are skipped for the entries concerned and cross-references to it are not checked, so one mistake is reported once.
- The same validator runs before any install: the agent's refresh (§1), `aictl catalog restore`, and `aictl catalog use <dir> --test` (aictl.md §4.11.4), which refuses with exit 2 on any problem.
- `--online` runs only when the offline rules pass. It then HEAD-checks every URL: each Hugging Face file (`<endpoint>/<repo>/resolve/<revision>/<name>`, comparing `X-Linked-Size` with `size_bytes` and `X-Linked-Etag`, the LFS oid, with `sha256`), each `url` model file (`Content-Length` against `size_bytes`), and, for reachability only, each ark `url` and `files` entry, `github_release` asset, kiwix `base_url` and `git_mirror` repository. Every finding is `LAI-CAT-011` (the pinned value no longer matches reality, or the URL cannot be fetched; a network failure says "could not check" so a re-run tells a flake from rot). CI runs this weekly, not per PR.

## 7. Doctor codes (LAI-CAT)
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-CAT-001 | error | Catalog file can't be parsed | The catalog file is damaged. Run `aictl catalog restore`, which copies the last good one from the ark kit |
| LAI-CAT-002 | warning | New catalog failed validation, previous kept | Nothing to do. Report it to the maintainer |
| LAI-CAT-003 | error | The profile in node.yaml doesn't exist | Run `aictl profile suggest`, then `aictl profile set <id>` |
| LAI-CAT-004 | error | The profile references an unknown model/content id | Catalog bug. Fix it in the repo |
| LAI-CAT-005 | warning | A model doesn't fit this node (VRAM/RAM/backend) | Choose a smaller model. `aictl models --loadable` lists the ones that fit |
| LAI-CAT-006 | warning | Couldn't download the catalog from GitHub | The node keeps its current catalog. Check the internet connection, then run `aictl catalog refresh` |
| LAI-CAT-007 | warning | The catalog version named in node.yaml does not exist on GitHub | Set `catalog.channel` in node.yaml to `stable` or to an existing `catalog-<version>` |
| LAI-CAT-008 | warning | The catalog from GitHub was incomplete or inconsistent, so nothing was installed | The node keeps its current catalog. Run `aictl catalog refresh` again; if it repeats, report it to the maintainer |
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

The constants live in `agent/internal/catalog/codes.go` until AGENT-11 creates `internal/doctor/codes.go` (which must hold every registry code of every area, doctor-codes.md §1.6) and the en/es texts.

## 9. Test plan
- **Unit (table-driven, `agent/internal/catalog`):** every lint rule has a failing case with its code and `<file>:<yaml-path>` (`LAI-CAT-010`…`019`, `004`), built by editing one valid placeholder catalog (`testdata/valid`) so the table shows exactly what each rule rejects and asserts the number of problems (no cascades); a second table lists edits that must stay valid. Strict decoding rejects an unknown key (`LAI-CAT-010`) in all three file kinds; the issue's fixtures (`models_max: 0`, `autoload: true`, overlapping `match` ranges, a piper model with only `piper_onnx`, a `portal_links` entry with `article_es`) are cases. The CLI tests hold the golden human lines and the exact `--json` bytes ([aictl.md](aictl.md) §4.11.1). Profile selection, the effective plan and `loadable` are CAT-05's tests (below); here only the overlap rule is covered.
- **Profile match, plan and loadable (CAT-05, table-driven, no I/O):** `testdata/hw/{ai-n1,ai-n2,ai-n3,cpu}.json` are `GET /hardware` bodies of the three lab node classes and a no-GPU box; each suggests its documented profile with the exact reasons (ai-n1 matches on its GTT, and the same card seen with only 2 GiB of dedicated VRAM falls to `cpu-only`). Cases: both bounds of every range (min, min-1, max, max+1), `cpu-only` last even when its own match would hold for a GPU node, `ark-drive-full` never chosen, overlapping profiles in any list order giving the lexical first, missing hardware data (unknown RAM, a GPU with unknown memory, the zero snapshot), a GPU too small for any profile landing on `cpu-only`. The plan: add, remove, add-and-remove of one id, duplicates, budget at the boundary, one byte over, every tier boundary of the drop order, a budget override, an unknown profile (`LAI-CAT-003`) and unknown ids in each list (`LAI-CAT-004`), `GET /profile` JSON keys. `loadable`: the three lab nodes plus cpu with llama, whisper and piper models, the exact message of the spec example, unified-memory reserve, missing files, an empty snapshot.
- **Install:** a rejected catalog leaves `/etc/localai/catalog/` byte-identical (`LAI-CAT-002`); the install is killed at every filesystem operation and, after `Recover`, the live directory loads and is the old or the new catalog; a damaged file at startup falls back to `<root>/kit/catalog/` (`source: kit`); a tampered bundle is skipped.
- **Online:** an `httptest` server answers HEAD like Hugging Face (redirect with `X-Linked-Size` and `X-Linked-Etag`), with size mismatch, oid mismatch, 404 and a network failure.
- **GitHub source (CAT-04, `httptest` TLS fake of the API and raw hosts, table-driven, no sleeps):** `stable` with several tags (`catalog-stable` ignored wherever it sits, `-10` > `-9`, day 10 > day 09, a year beating month and day, stray, malformed and foreign refs ignored, several API pages); every listing failure (403 with no quota, 429, 403 with `Retry-After`, plain 403, 404, 500, 503, not JSON, an object, no catalog tag, no API configured, an `http` API, a `Link` to another host, an endless listing, a hung server on an injected transport) ends in `catalog-stable`; a stale `catalog-stable` is used as it is while the listing is down and loses to the highest tag while it is up; a pin never lists; malformed channels make no request; profile discovery (an extra profile, a stray file, the listing down, a documented profile missing); the fetch failures (404 of the tag, 404 of one file, 429, 502, truncated body, declared and undeclared oversize, a `CATALOG_VERSION` that differs from the tag, hung server, caller cancel) each with its code and reason and no bundle; `http` base URLs and redirects refused, https redirects followed, the proxy variables ignored, no `Authorization` or `Cookie` header; through `Fetcher.Refresh` the previous catalog stays byte-identical (and no staging or `catalog.prev` is left) after every failure, lint failure is `LAI-CAT-002`, and the bundle digest equals the cloud's rule.
- **Release scripts (`scripts/tests/test-catalog-version-guard.sh`, throwaway git repositories):** the guard accepts the first version and every greater one (same day higher `N`, later day lower `N`, `-10` after `-9`, leap day, CRLF) and refuses an existing tag, a lower or equal version and every malformed form, with stray tags warned about and ignored; the tag script creates annotated `catalog-<V>` and `catalog-stable` at one commit and moves only the latter, and a push the remote rejects changes neither ref (atomic).
- **CI:** `aictl catalog lint catalog/` on every PR (`.github/workflows/catalog-lint.yml`); `--online` weekly.

## 10. Implementation breakdown
1. **CAT-01 Schema, loader and lint** (2 d). Depends on: AGENT-01. Scope: `agent/internal/catalog/`: Go structs for `models.yaml`, `ark.yaml`, `profiles/*.yaml` and `CATALOG_VERSION` (§2–§4, including the optional fields `extract`, `extra_repos`, `checksum`/`trust`, multi-file `url`, `portal_links`, `chat_template` file kind), strict decoding (unknown keys rejected), every lint rule of §2–§4 as `LAI-CAT-010..019` with `<file>:<yaml-path>` locations, cross-references (profile includes, file kinds, `es_item`), `aictl catalog lint [--online] [--json] <dir>` (§6), the §1 source priority and atomic install into `/etc/localai/catalog/` with `LAI-CAT-001/002`, CI job running lint on `catalog/` per PR. Owns the linter **and** the `aictl catalog lint` command ([aictl.md](aictl.md) §10 AICTL-07 only verifies output conformance). Acceptance: the §9 test plan; table-driven tests with one failing fixture per rule; `--json` output matches [aictl.md](aictl.md) §4.11.1; a rejected catalog leaves the previous one in place.
2. **CAT-02 Populate initial models** (2 d). Depends on: CAT-01, ROUTER-10. Scope: the §2.1 set in `catalog/models.yaml` with pinned HF revisions, sha256 and sizes; `min_vram_mib` **measured** on each node class with `aictl bench` (ai-n1 Vulkan 128 GB, ai-n2 CUDA 12 GB, ai-n3 CUDA 8 GB) plus 10 %; the exact ai-n2 35B-A3B flags captured by the migration preflight ([llama-router.md](llama-router.md) §11); whisper and piper entries. Acceptance: `aictl catalog lint --online catalog/` passes; each PR description carries the bench output; the three running models are present with the sha256 of the files on the nodes.
3. **CAT-03 Populate initial ark content** (1.5 d). Depends on: CAT-01, SYNC-05, SYNC-09, SYNC-10, SYNC-11 (the dry-run acceptance needs the planner and the kiwix/pmtiles, url/github_release and repo_docs resolvers). Scope: the §3.1 Kiwix books with book names **verified against the Kiwix OPDS catalog** (`https://library.kiwix.org/catalog/v2/entries`), maps (`osm-central-america`, `osm-north-america`, `map-assets`), `places-central-america`, `docs-bundle`, first-aid `portal_links` with article paths checked in the real ZIMs ([survival-portal.md](survival-portal.md) §3.4), `expected_size_bytes` and `refresh_days`. Kit items are KIT-01. Acceptance: lint passes; a dry-run sync on ai-n3 (`aictl ark sync --dry-run`) resolves every item; every `portal_links` article opens in kiwix-serve.
4. **CAT-04 Catalog distribution** (1 d). Depends on: CAT-01. Scope: release workflow that tags `catalog-<CATALOG_VERSION>` when `catalog/CATALOG_VERSION` changes on `main` (lint must pass), the moving `catalog-stable` tag, `catalog.channel: stable` resolution for unenrolled nodes (§1: GitHub matching-refs API, `catalog-stable` fallback), and the contract consumed by the cloud import ([cloud-app.md](cloud-app.md) §4.6, CLOUD-10). Acceptance: a version bump on a test branch produces a tag; an unenrolled node refreshes from it; the cloud imports the same tag.
5. **CAT-05 Profile suggest and fit** (1 d). Depends on: CAT-01, AGENT-10, ROUTER-02. Scope: `match` evaluation (§4 selection rule: lexical id order, `cpu-only` last, `ark-drive-full` never auto-selected, GTT on unified memory), `aictl profile suggest` / `aiagent setup profile-suggest`, effective plan (profile include + `profile_overrides`, §4–§5), `loadable` + reason from the fit rules, `LAI-CAT-003/004/005`. Acceptance: table tests for the three lab nodes' hardware (each suggests its documented profile); `GET /profile` shows `match_reasons`; a model that doesn't fit is `loadable: false` with `LAI-CAT-005`.
