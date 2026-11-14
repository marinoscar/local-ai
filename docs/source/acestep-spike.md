> **Spike findings (archive, redacted).** Issue #505 (epic #504), run on 2026-11-14 on **ai-n2** (`msi-linux`, profile `cuda-12gb`) by a coding agent the owner delegated the run to.
> No IPs, tailnet names, tokens or emails. This file records what was measured; the maintained contract is [`docs/specs/music.md`](../specs/music.md) and the code is [`engines/acestep/`](../../engines/acestep/).
> ai-n3 (`small-8gb`, Turing fp16) was **not** part of this run.

# ACE-Step 1.5 spike on ai-n2

**Verdict: go for ai-n2 (12 GB CUDA), with both CPU offloads on.** A 60 s song with vocals takes about 20 s; the GPU peaks at about 10.4 GiB and holds 5 GiB while idle; generation works with no network; the Python API is a clean headless interface. Three things the spec did not expect are listed under [Surprises](#surprises) and are handled in `backend.py`.

## 1. Node and baseline

| Item | Value |
|---|---|
| GPU | NVIDIA GeForce RTX 4080 Laptop GPU, 12282 MiB (PyTorch sees 11.60 GiB), compute 8.9 (bf16) |
| Driver / CUDA (driver API) | 595.99.02 / 13.2 |
| CUDA runtime used | 12.8 (torch 2.10.0+cu128 wheels) |
| OS / system Python | Ubuntu 26.04.1 LTS, Python 3.14.4 only |
| Python for the venv | 3.12.16, python-build-standalone `20261110` (`cpython-3.12.16+20261110-x86_64-unknown-linux-gnu-install_only.tar.gz`, checked against the release's SHA256SUMS) |
| RAM / free disk | 30.5 GB / 188 GB in the home folder |
| Agent | updated during the run: rc.4 → rc.9 → **0.1.0-rc.10** (signed `aictl update`; rc.10 is the first release with `aictl workloads`) |
| Loaded at the start | `qwen3.6-35b-a3b-q4km` (default), 11202 MiB in `llama-server`; no workloads defined |
| Restored at the end | the same model loaded, 11223 MiB used, `aictl models` identical to the start |

ACE-Step 1.5 requires Python `>=3.11,<3.13`; Ubuntu 26.04 has neither, so the kit bundle (#511) must carry its own interpreter or wheels for one the node has. Disk: interpreter 230 MB, venv 8.1 GB (149 packages), weights 9.4 GiB.

## 2. What was installed

- **Code:** `github.com/ACE-Step/ACE-Step-1.5` at commit `ca1e85fe9430179831e6bc6be790c332190a3866` (2026-08-29), package `ace-step` 1.5.0, **MIT**. Its vendored `acestep/third_parts/nano-vllm` 0.2.0 (the LM backend) is **MIT**.
- **Install** (no `uv`): `python3.12 -m venv venv`, then `pip install --extra-index-url https://download.pytorch.org/whl/cu128 torch==2.10.0+cu128 torchvision==0.25.0+cu128 torchaudio==2.10.0+cu128`, `pip install ./src/acestep/third_parts/nano-vllm`, `pip install --extra-index-url …/cu128 ./src`. No `flash-attn` is needed on Python 3.12 (nano-vllm declares it only for 3.11 and falls back to SDPA). `torch.cuda.is_available()` → `True`.
- **System dependencies:** none beyond the NVIDIA driver. **No ffmpeg** on the node, and none is needed: samples are read with `soundfile`, whose wheel bundles libsndfile 1.2.2; WAV, FLAC, Ogg Vorbis and MP3 were each decoded (20.0 s at 48 kHz). Only AAC/M4A would fall back to ffmpeg, and the spec does not accept them.
- The full `pip freeze` is at the end of this file.

## 3. Weights

Hugging Face `ACE-Step/Ace-Step1.5`, revision **`19671f406d603126926c1b7e2adc169acbcade22`** (last modified 2026-02-03), not gated. License tag **MIT**. The bundled text encoder is a copy of `Qwen/Qwen3-Embedding-0.6B`, which upstream is **Apache-2.0**. The LM `acestep-5Hz-lm-1.7B` has no separate license tag (it ships inside the MIT repo).

28 files, **10,092,102,593 bytes**. Every LFS file's sha256 equals the oid Hugging Face publishes for this revision. The sha256 of each file is in the findings comment on issue #505 (this repo allows 64-hex values only as `sha256:` pins in the catalog, so #511 copies them from there into `catalog/models.yaml`). Paths are relative to the repo root (the catalog's `<root>/music/<model-id>/`):

| File | Bytes |
|---|---:|
| `.gitattributes` | 1740 |
| `Qwen3-Embedding-0.6B/added_tokens.json` | 707 |
| `Qwen3-Embedding-0.6B/chat_template.jinja` | 4116 |
| `Qwen3-Embedding-0.6B/config.json` | 1359 |
| `Qwen3-Embedding-0.6B/merges.txt` | 1671853 |
| `Qwen3-Embedding-0.6B/model.safetensors` | 1191586416 |
| `Qwen3-Embedding-0.6B/special_tokens_map.json` | 613 |
| `Qwen3-Embedding-0.6B/tokenizer.json` | 11423705 |
| `Qwen3-Embedding-0.6B/tokenizer_config.json` | 5404 |
| `Qwen3-Embedding-0.6B/vocab.json` | 2776833 |
| `README.md` | 5496 |
| `acestep-5Hz-lm-1.7B/added_tokens.json` | 2217787 |
| `acestep-5Hz-lm-1.7B/chat_template.jinja` | 4168 |
| `acestep-5Hz-lm-1.7B/config.json` | 1385 |
| `acestep-5Hz-lm-1.7B/merges.txt` | 1671853 |
| `acestep-5Hz-lm-1.7B/model.safetensors` | 3708521528 |
| `acestep-5Hz-lm-1.7B/special_tokens_map.json` | 1824199 |
| `acestep-5Hz-lm-1.7B/tokenizer.json` | 24321939 |
| `acestep-5Hz-lm-1.7B/tokenizer_config.json` | 14072925 |
| `acestep-5Hz-lm-1.7B/vocab.json` | 2776833 |
| `acestep-v15-turbo/config.json` | 1968 |
| `acestep-v15-turbo/configuration_acestep_v15.py` | 13130 |
| `acestep-v15-turbo/model.safetensors` | 4787825604 |
| `acestep-v15-turbo/modeling_acestep_v15_turbo.py` | 96036 |
| `acestep-v15-turbo/silence_latent.pt` | 3841215 |
| `config.json` | 1968 |
| `vae/config.json` | 425 |
| `vae/diffusion_pytorch_model.safetensors` | 337431388 |

The repo ships Python code (`acestep-v15-turbo/configuration_acestep_v15.py`, `modeling_acestep_v15_turbo.py`) and a pickle (`acestep-v15-turbo/silence_latent.pt`). ACE-Step replaces the two `.py` files with its own package's copy at load (see Surprises), so the code that runs is the pinned package's, not the repo's.

## 4. The headless interface

ACE-Step offers a Gradio UI (`acestep`), a REST server (`acestep-api`) and a **Python API**. The shim uses the Python API in-process, because it gives progress, a cancel point and exact control of every file, with no second server:

```python
from acestep.handler import AceStepHandler
from acestep.llm_inference import LLMHandler
from acestep.inference import GenerationParams, GenerationConfig, generate_music

os.environ["ACESTEP_CHECKPOINTS_DIR"] = ckpt            # the writable overlay, see Surprises
dit = AceStepHandler()
dit.initialize_service(project_root=work, config_path="acestep-v15-turbo", device="cuda",
                       offload_to_cpu=True, offload_dit_to_cpu=True)
llm = LLMHandler()
llm.initialize(checkpoint_dir=ckpt, lm_model_path="acestep-5Hz-lm-1.7B", backend="vllm",
               device="cuda", offload_to_cpu=True)

params = GenerationParams(caption=tags, lyrics=lyrics, vocal_language="es", duration=60.0,
                          inference_steps=8, shift=3.0, seed=42)
# instrumental:            lyrics="[Instrumental]", instrumental=True
# "sounds like" (sample):  reference_audio="/work/…/sample.wav"
# "same song"  (sample):   task_type="cover", src_audio="/work/…/sample.wav", audio_cover_strength=0.7
cfg = GenerationConfig(batch_size=1, use_random_seed=False, seeds=[42], audio_format="wav")
result = generate_music(dit, llm, params, cfg, save_dir=out, progress=callback)
# result.success, result.error, result.audios[0]["path"], result.audios[0]["sample_rate"]
```

- **Progress:** `callback(value, desc=…)` with `value` in 0..1: 0.1 (LM metadata), 0.5 (LM audio codes), 0.51–0.8 (diffusion, estimated every 0.5 s from a background thread), 0.8 (decode), 0.99 (saving). The LM phases give no updates for about 6 s.
- **Cancel:** there is no cancel API. The shim raises from the progress callback; `generate_music` catches every exception and returns `success=False`, and the shim reports `cancelled`. Measured: a cancel settles in **about 6 s** (the next progress call), the output folder is removed.
- **Output:** `audio_format="wav"` writes **WAV, 48 kHz, stereo, 16-bit PCM** (192,000 bytes per second; a 60 s song is 11,520,044 bytes), named `<uuid>.wav` in `save_dir`. The shim renames it to `out-<i>.wav`. This matches the spec's disk-guard formula.
- **Turbo settings:** 8 steps, `shift=3.0`; ACE-Step forces guidance to 1.0 for turbo.
- **dtype:** ACE-Step picks it: bf16 on this card (fp16 with eager attention on pre-Ampere). It has no switch for CUDA, so the shim accepts only `MUSIC_DTYPE=auto` with the real engine.
- **Languages:** `acestep.constants.VALID_LANGUAGES` (50 codes, including `en` and `es`); the shim fails a task in another language with "the model does not sing this language".

## 5. Measurements

Every run is one 60 s song unless noted, `batch_size=1`, seeds fixed. "Peak" is the highest `nvidia-smi memory.used` sampled once a second during the run (the PyTorch peak is close to it and sometimes above, because one-second sampling misses short spikes). The LLM was unloaded first (16 MiB used, no process).

**Cold start** (process start to ready, both models loaded): 18.5–19.6 s (five starts, including the shim). GPU memory when ready: **5129 MiB** with both offloads, 9699 MiB with CPU offload only, 10887 MiB with none.

| Run | No offload | CPU offload | **CPU + DiT offload (chosen)** |
|---|---|---|---|
| Spanish, 2 verses + chorus, no sample, run 1 | 17.1 s · 11841 MiB | 14.3 s · 10831 MiB | **21.3 s · 10121 MiB** |
| run 2 | 10.8 s · 11723 MiB | 12.9 s · 11003 MiB | **20.1 s · 10291 MiB** |
| run 3 | 10.6 s · 11685 MiB | 13.4 s · 10841 MiB | **19.9 s · 10285 MiB** |
| Same lyrics + 20 s clip, "sounds like" (reference) | **out of memory** | 14.7 s once, **out of memory** once | **20.2 s · 10293 MiB** (repeat: 21.9 s, 20.7 s) |
| Same lyrics + 20 s clip, "same song" (cover, strength 1.0) | **out of memory** | 4.2 s / 4.5 s · 11155 MiB | **6.4 s · 10101 MiB** (output is 20 s, see Surprises) |
| English instrumental | 10.5 s · 11815 MiB | 12.8 s · 11183 MiB | **19.7 s · 10441 MiB** |
| Spanish, **240 s** (spec maximum) | — | — | **43.1 s · 10833 MiB** |

The clip is 20 s of a I–V–vi–IV progression rendered with numpy for this test, so it is free of rights.

**Through the shim** (`MUSIC_BACKEND=acestep` on `127.0.0.1:8082`, curl, the §3.4 protocol): ready in 18.6 s and 19.6 s, `/health` `{"status": "ready", "dtype": "bf16", "vram_mib": 5121}`. Two 60 s variations ≈ 38.5 s; cover from the 20 s sample 6.6–7.6 s; "sounds like" 30 s 16.6–16.8 s; cancel of a running task settled in 6.2 s with its folder removed; validation `400`s, a `404`, the fifth task `429`; a language it does not sing → `failed`. SIGTERM: the process exits in 1.8 s and **all GPU memory is freed** (16 MiB used, no compute process).

**Stop:** after every run the GPU went back to 16 MiB with no python process in `nvidia-smi`.

## 6. Offline

- Unprivileged network namespaces are blocked on this kernel (`apparmor_restrict_unprivileged_userns=1`) and the spike used no root, so egress was blocked inside the process instead: `HF_HUB_OFFLINE=1`, `TRANSFORMERS_OFFLINE=1`, proxies pointing at a closed local port, and a guard loaded before ACE-Step that refuses and logs every non-loopback `connect` and DNS lookup.
- Result: a Spanish song (21.0 s) and a "sounds like" song (20.3 s) generated; **the guard recorded no network attempt at all**. The weights were read from a read-only folder.
- The real block is the unit's `IPAddressDeny=any` (spec §3.2); this run shows ACE-Step does not need it to work, not that the unit works.
- **Optional Gradio UI** (`acestep --server-name 127.0.0.1 --port 7860`, analytics off, no model loaded): it bound to loopback only and made no outbound connection. Its fonts and CSS are local (`static/fonts/…`), but the page head has two `preconnect` hints to Google Fonts and **one external script** (`cdnjs.cloudflare.com/…/iframeResizer.contentWindow.min.js`). Offline, a browser fails those three requests. This is the "may look broken offline" case spec §4.9 already accepts for this expert-only tool.

## Surprises

1. **ACE-Step writes into the model folder.** At every load it compares the `.py` files in `acestep-v15-turbo/` with its package's copy and overwrites them when they differ (they do at these pins). With the weights read-only, as they are under `ProtectSystem=strict`, the load fails with `PermissionError`. **Fix in `backend.py`:** at load the shim builds a writable overlay, `MUSIC_WORK_DIR/_model/`, with a symlink to every file of `MUSIC_MODEL_DIR` except the `.py` files, which it copies, and points `ACESTEP_CHECKPOINTS_DIR` there. The verified files are never written. The overlay is rebuilt at each load (about 1 ms). The agent's start-up clean-up of `work/` must leave `_model` alone or accept that it is rebuilt (#510).
2. **12 GB is tight.** The LM backend sizes its KV cache from the free memory, so with no offload the card is about 97% full once loaded, and encoding a sample runs out of memory. CPU offload alone fails "sounds like" about half the time. **Both offloads** (`offload_to_cpu`, `offload_dit_to_cpu`, upstream's own recommendation for an 8–12 GB card) passed every case, including 240 s, at the cost of 7–10 s per song. System RAM was not a limit on this 30.5 GB node.
3. **ACE-Step logs the lyrics.** At INFO it logs the whole LM prompt (caption and lyrics), and `logger.exception` (which it calls on a cancel) prints loguru's "diagnose" traceback with **local variables**, including the shim's request. Both would reach the journal, against spec §6. **Fix in `backend.py`:** loguru keeps WARNING and above only, one line per record, no traceback (`backtrace=False`, `diagnose=False`, a format that never appends the exception), and any line carrying a lyric line or the tags of the running song is dropped; stdout during a song is discarded. Verified on the real shim log: no lyric or tag word, no traceback.
4. **"Same song" keeps the sample's length.** A cover is as long as the sample (20 s here), whatever `duration_s` asks for. A 60 s sample gives a 60 s cover.
5. **"Sounds like" has no strength.** `reference_audio` is a global timbre/style reference without a strength knob; `strength` only applies to "same song" (`audio_cover_strength`). The shim ignores it for "sounds like".

## Recommendation

- **ai-n2 (12 GB, Ada): go.** Defaults: turbo DiT + 1.7B LM, bf16 (`MUSIC_DTYPE=auto`), 8 steps, `shift` 3.0, both CPU offloads, one task at a time, `batch_size` 1 per variation. Keep the 240 s limit (measured: 43 s, 10.8 GiB). The UI can say "about 20 seconds per minute of song, plus 20 seconds to start".
- **Catalog (#511):** `min_vram_mib` 10900 (240 s peak 10833 MiB rounded up), `min_ram_mib` 8192 as a first estimate (not measured: the offloaded DiT and text encoder are about 6 GB of weights; #511 should measure it), `min_disk_bytes` 10,092,102,593 for the weights, plus about 8.4 GB for the runtime. Every weight file and size is in §3; the hashes are in the issue #505 findings comment. Driver: 595.99.02 / CUDA 13.2 worked with the cu128 wheels; NVIDIA's documented minimum for CUDA 12.8 is driver 570.26; older drivers were not tested.
- **ai-n3 (8 GB, Turing):** not measured. The 5.1 GiB idle and 10.3 GiB peak here suggest it needs the 0.6B LM (a separate repo, `ACE-Step/acestep-5Hz-lm-0.6B`) or INT8 quantization, and fp16. It needs its own run before it gets the model.

## pip freeze

<details><summary>149 packages (Python 3.12.16)</summary>

```
absl-py==2.5.1
accelerate==1.15.0
ace-step @ git+https://github.com/ACE-Step/ACE-Step-1.5@ca1e85fe9430179831e6bc6be790c332190a3866
aiofiles==24.1.0
aiohappyeyeballs==2.7.1
aiohttp==3.14.5
aiosignal==1.4.0
annotated-doc==0.0.6
annotated-types==0.8.0
anyio==4.16.0
attrs==26.1.0
brotli==1.2.1
certifi==2026.10.23
cffi==2.1.1
charset-normalizer==3.5.2
click==8.5.1
contourpy==1.4.0
cryptography==50.0.2
cuda-bindings==12.9.4
cuda-pathfinder==1.9.0
cycler==0.12.1
diffusers==0.39.0
diskcache==5.6.3
einops==0.8.2
einx==0.4.3
fastapi==0.163.0
ffmpy==1.0.0
filelock==4.2.10
fonttools==4.68.0
frozendict==2.5.1
frozenlist==1.8.0
fsspec==2026.9.0
gradio==6.2.0
gradio_client==2.0.2
groovy==0.1.2
grpcio==1.85.1
h11==0.16.0
hf-xet==1.8.0
httpcore==1.0.9
httpcore2==2.16.0
httptools==0.9.0
httpx==0.28.1
httpx2==2.16.0
huggingface_hub==0.36.2
idna==3.20
importlib_metadata==10.2.0
Jinja2==3.1.6
kiwisolver==1.5.1
lazy-importer==0.1.0
lightning==2.6.6
lightning-utilities==0.15.3
llvmlite==0.50.0
loguru==0.8.0
lycoris_lora==4.1.3
Markdown==3.11.3
markdown-it-py==4.3.0
MarkupSafe==3.0.4
matplotlib==3.11.2
mdurl==0.1.2
modelscope==1.40.3
modelscope-hub==0.7.1
mpmath==1.3.0
multidict==7.2.1
nano-vllm @ git+https://github.com/ACE-Step/ACE-Step-1.5@ca1e85fe9430179831e6bc6be790c332190a3866#subdirectory=acestep/third_parts/nano-vllm
networkx==3.7
numba==0.68.1
numpy==2.5.4
nvidia-cublas-cu12==12.8.4.1
nvidia-cuda-cupti-cu12==12.8.90
nvidia-cuda-nvrtc-cu12==12.8.93
nvidia-cuda-runtime-cu12==12.8.90
nvidia-cudnn-cu12==9.10.2.21
nvidia-cufft-cu12==11.3.3.83
nvidia-cufile-cu12==1.13.1.3
nvidia-curand-cu12==10.3.9.90
nvidia-cusolver-cu12==11.7.3.90
nvidia-cusparse-cu12==12.5.8.93
nvidia-cusparselt-cu12==0.7.1
nvidia-nccl-cu12==2.27.5
nvidia-nvjitlink-cu12==12.8.93
nvidia-nvshmem-cu12==3.4.5
nvidia-nvtx-cu12==12.8.90
opentelemetry-api==1.45.2
orjson==3.13.0
packaging==26.3
pandas==2.3.3
peft==0.21.3
pillow==12.3.0
propcache==0.5.4
protobuf==7.36.2
psutil==7.2.2
pycparser==3.12
pydantic==2.14.3
pydantic_core==2.50.4
pydub==0.25.1
Pygments==2.21.0
pyparsing==3.3.3
python-dateutil==4.0.0
python-dotenv==1.2.4
python-multipart==0.0.33
pytorch-lightning==2.6.6
pytorch-wavelets==1.3.0
pytz==2026.6
PyWavelets==1.10.0
PyYAML==6.0.3
regex==2026.11.9
requests==2.34.2
rich==15.0.0
safehttpx==0.1.7
safetensors==0.8.0
scipy==1.18.1
semantic-version==2.10.0
setuptools==71.1.0
shellingham==1.5.4
six==1.17.0
soundfile==0.14.0
starlette==0.52.1
sympy==1.14.0
tensorboard==2.21.0
tensorboard-data-server==0.7.2
tokenizers==0.22.2
toml==0.10.2
tomlkit==0.13.3
torch==2.10.0+cu128
torch-einops-utils==0.2.6
torchao==0.16.0+cu128
torchaudio==2.10.0+cu128
torchcodec==0.17.0
torchmetrics==1.9.0
torchvision==0.25.0+cu128
tqdm==4.70.1
transformers==4.57.6
triton==3.6.0
truststore==0.10.5
typer==0.29.1
typer-slim==0.24.0
typing-inspection==0.4.5
typing_extensions==4.16.0
tzdata==2026.6
urllib3==2.8.0
uvicorn==0.54.0
uvloop==0.23.0
vector-quantize-pytorch==1.34.0
watchfiles==1.3.0
websockets==17.4
Werkzeug==3.1.9
xxhash==4.1.0
yarl==1.25.1
zipp==4.1.2
```

</details>
