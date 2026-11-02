# ADR-0002: Model swapping uses llama.cpp router mode with agent-generated presets

- **Status:** Accepted
- **Date:** 2026-11-02

## Context
Today each node runs one `llama-qwen.service` with a hard-coded `-m <model>`, so swapping means editing the unit by hand. We need swapping from the cloud, the CLI and the desktop. The built-in llama.cpp WebUI should offer a model picker, and the public endpoint (`127.0.0.1:8080`, behind Tailscale Serve and nginx) must not change. The current llama.cpp build (commit `f3586e66b`, 2026-10-31) supports **router mode**:
- `--models-preset <ini>`, `--models-dir`, `--models-max`, `--models-memory-margin` and `--no-models-autoload`,
- `POST /models/load` and `POST /models/unload`,
- per-request routing on the `model` field.

## Decision
- `localai-llama.service` runs `llama-server` in **router mode** on `127.0.0.1:8080` with `--models-preset /etc/localai/models.ini`, the existing API key (`--api-key-file %d/llama-api-key`), `--models-max` from the profile, and `--no-models-autoload`.
- The agent **generates** `models.ini` from the catalog and the profile. Each model's flags (ctx, ngl, MTP/draft, mmproj, flash-attn) become that preset's section.
- The agent loads and unloads models **explicitly** through the router API. Autoload is off so that a stray request for a big model can't evict the survival model or OOM a laptop.
- `alias` = the catalog model id, so `/v1/models` and the WebUI dropdown show catalog ids.

## Consequences
- **Good:** swapping takes one API call and the port, key and URL stay unchanged. The WebUI gets a model dropdown for free. The gateway can route by model name.
- **Good:** an 8 GB laptop sets `models_max: 1`, while the 128 GB node can keep a coder model, a chat model and a vision model loaded at once.
- **Risk:** router mode is newer than single-model mode. Mitigation: the agent has a `single` fallback mode in `node.yaml` (`llama.mode: single`) that writes a classic unit with `-m <default model>`. The drill also exercises load/unload.
- **Note:** some args are overwritten by the router (host, port, api key, alias). The lint forbids them in `extra_args`.

## Alternatives considered
- **llama-swap:** an extra moving part, with its own config and proxy port. Router mode is now built in.
- **One systemd unit per model, swapped by the agent:** works, but there is no WebUI picker and no concurrent models, and every swap is a slower restart.
- **Ollama:** a different model store and no MTP/draft control. The existing nodes are tuned around llama.cpp.
