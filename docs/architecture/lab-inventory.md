# Lab inventory (redacted)

> **Status:** current as of 2026-11-02. Real IPs and identifiers are placeholders. The real values are in `inventory.local.yaml`, which is never committed ([ADR-0007](../decisions/0007-public-repo-redaction.md)). Keep this file updated whenever a node is added or changed. The cloud app's **Lab** page becomes the live view once E6 ships.

## 1. Nodes

| Node id | Public endpoint | Machine (tailnet name) | Hardware | Backend | Profile | Serve pattern | Model served today | Status |
|---|---|---|---|---|---|---|---|---|
| ai-n1 | `https://ai-n1.marin.cr` | `ai-marin-linux` | MINISFORUM MS-S1 Max, 128 GB unified memory, Radeon 8060S | Vulkan | `max-128gb` | **A**: `tailscale serve --tcp=18080 tcp://127.0.0.1:8080`, nginx → `http://<TAILNET_IP_AI_N1>:18080` | `qwen3-coder-next` (Q4_K_M, about 48.4 GB, n_ctx 131072) | live |
| ai-n2 | `https://ai-n2.marin.cr` | `msi-linux` | MSI Stealth 17 Studio, RTX 4080 Laptop 12 GB, **dual-boot Windows** | CUDA | `cuda-12gb` | **B**: `tailscale serve --https=443 http://127.0.0.1:8080`, nginx → `https://<TAILNET_IP_AI_N2>:443` | `qwen3.6-35b-a3b` | live (offline whenever booted into Windows) |
| ai-n3 | `https://ai-n3.marin.cr` | `hp-linux` | HP ZBook Create G7, i7-10850H, 14 GiB usable RAM, RTX 2070 Max-Q 8 GB, Ubuntu 26.04.1, kernel 7.0 | CUDA (driver 595.99, CUDA 13.2, arch 75) | `small-8gb` | **B** (also a stray, undocumented `--tcp=18080` listener, to be removed at migration) | `qwen3.5-9b` (Q4_K_M, built-in MTP head, ctx 32768) | live |

### Planned (from the [blueprint](../source/ai-lab-blueprint.md))
| Planned id | Machine | Role |
|---|---|---|
| (agent-worker-01) | GEEKOM A8, no GPU | Agent and browser worker. Profile `cpu-only` if it ever serves models |
| (laptop-ubuntu-01) | HP EliteBook 840 G6, no GPU | Light worker. A `cpu-only` ark laptop candidate |

## 2. Per-node facts on each machine today (pre-migration)
Every node follows the same pattern from [source/ms-s1-max-llamacpp-runbook.md](../source/ms-s1-max-llamacpp-runbook.md):
- `llama-qwen.service`, running as the owner user, binds `127.0.0.1:8080`, with the key in `/etc/llama-qwen/api-key` via `LoadCredential`.
- llama.cpp is built under `~/llama.cpp/build-<backend>`.
- Models live under `~/models/<name>/`.

What differs:
| | ai-n1 | ai-n2 | ai-n3 |
|---|---|---|---|
| Build dir | `build-vulkan` | `build-cuda` (assumed, **verify**) | `build-cuda` (commit `f3586e66b`) |
| Speculative | separate MTP draft GGUF (`-md`, `--spec-draft-ngl all`, `--spec-draft-n-max 2`) | **not recorded, capture during migration** | built-in MTP head (`--spec-type draft-mtp --spec-draft-n-max 3`) |
| Extra env | `GGML_VK_SUBALLOCATION_BLOCK_SIZE=4294967296` (long context) | ? | none |
| Other | Cockpit on :9090 | unit and flags not recorded | OpenCode configured locally (`~/.config/opencode/opencode.json`) |

## 3. VPS (control plane)
| Item | Value |
|---|---|
| Tailnet name | `prod-server` (hostname `<VPS_HOSTNAME>`, tailnet IP `<TAILNET_IP_VPS>`, public IP `<VPS_PUBLIC_IP>`) |
| Proxy | `proxy-nginx` (nginx 1.27-alpine, `network_mode: host`), one vhost per node in `marin-server-infra/proxy/nginx/conf.d/ai-n<N>.marin.cr.conf` |
| DNS | Route53 zone `<ROUTE53_ZONE_ID>`, A records → `<VPS_PUBLIC_IP>`, TTL 300 |
| TLS | Let's Encrypt per hostname (certbot webroot in Docker), renewed by `shared/renew-all-certs.sh` |
| Open WebUI | `https://chat.marin.cr` (container bound to `127.0.0.1:8324`). Connections are index-aligned lists in its DB `config` table: [0] ai-n1/v1, [1] OpenAI, [2] ai-n2 (no `/v1` suffix), [3] ai-n3/v1. There is also a dead Ollama connection |
| New (E6) | `https://ai.marin.cr`: Local AI cloud app plus the `/v1` gateway |

## 4. Known documentation discrepancies (fixed by this repo)
| Discrepancy | Truth / action |
|---|---|
| The MS-S1 gist says ai-n1 serves `qwen3.8-27b` | ai-n1 serves `qwen3-coder-next`. The catalog keeps both |
| Infra `SERVER.md` and the Open WebUI docs list only ai-n1 and ai-n2 | ai-n3 was added as Open WebUI connection [3] on 2026-10-31. After E7, Open WebUI uses one gateway connection |
| ai-n2's unit and flags aren't recorded | Capture them during migration (issue in E2) |
| ai-n3 has an undocumented Tailscale `--tcp=18080` listener | Removed at migration. Pattern B only |
| The blueprint lists the ZBook as a "planned purchase" | It is ai-n3, live |
| The blueprint suggests `ai.marin.cr` should not be a raw public LLM endpoint | The gateway is authenticated (`lai_` keys), rate-limited and audited ([gateway.md](../specs/gateway.md)), which satisfies the blueprint's concern |
