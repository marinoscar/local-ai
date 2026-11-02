> **Imported source (archive, redacted).** Original: marin-server-infra repo: docs/ai-nodes-setup.md (private infra repo)  
> Imported: 2026-11-02. Lab-specific values replaced with placeholders (see `inventory.example.yaml`).  
> This file is a historical record only. The maintained instructions are in [`docs/runbooks/`](../runbooks/) and [`docs/specs/`](../specs/).

# AI Inference Nodes — Public HTTPS Access via VPS, Tailscale, Nginx, and Let's Encrypt

**Service:** Home llama.cpp inference servers (OpenAI-compatible API), one public hostname per node  
**Hostname scheme:** `ai-n<N>.marin.cr` → node *N* (`https://ai-n<N>.marin.cr/v1`)  
**Infrastructure repository:** [`marinoscar/marin-server-infra`](https://github.com/marinoscar/marin-server-infra)  
**First deployed:** October 12, 2026 (as `localai.marin.cr`)  
**Renamed / multi-node:** October 25, 2026 (`localai.marin.cr` retired → `ai-n1.marin.cr`; `ai-n2.marin.cr` added; `ai-n3.marin.cr` DNS reserved); **October 31, 2026** (`ai-n3.marin.cr` published, hp-linux)

> **This is a growing fleet.** More nodes will be added over time. Every node follows the same pattern: a Route 53 A record `ai-n<N>.marin.cr` → VPS, one Nginx vhost `proxy/nginx/conf.d/ai-n<N>.marin.cr.conf`, one Let's Encrypt certificate, and a private Tailscale upstream. Use [Section 6 — Adding a new node](#6-adding-a-new-node) and keep the [node registry](#1-node-registry) up to date.
>
> **Scope:** Publishing **existing** home AI servers through the **existing** public VPS. Installing or tuning llama.cpp/models on the nodes is out of scope (see the llama.cpp runbook in [References](#14-references)).

---

## 1. Node registry

Keep this table as the single source of truth. Add a row **before** creating DNS for a new node.

| Node | Public URL | Tailnet host | Tailnet IP | Upstream (as proxied by Nginx) | Model alias | API key | Status |
|---|---|---|---|---|---|---|---|
| ai-n1 | `https://ai-n1.marin.cr/v1` | `ai-marin-linux.<TAILNET>.ts.net` (MINISFORUM) | `<TAILNET_IP_AI_N1>` | `http://<TAILNET_IP_AI_N1>:18080` (Tailscale Serve TCP 18080 → `127.0.0.1:8080`) | `qwen3-coder-next` | **Yes** (llama.cpp `--api-key`) | Operational |
| ai-n2 | `https://ai-n2.marin.cr/v1` | `msi-linux.<TAILNET>.ts.net` | `<TAILNET_IP_AI_N2>` | `https://<TAILNET_IP_AI_N2>:443` (Tailscale Serve HTTPS, SNI `msi-linux.<TAILNET>.ts.net`) | `qwen3.6-35b-a3b` (last observed) | **Yes** (enabled 2026-10-25) | Operational |
| ai-n3 | `https://ai-n3.marin.cr/v1` | `hp-linux.<TAILNET>.ts.net` | `<TAILNET_IP_AI_N3>` | `https://<TAILNET_IP_AI_N3>:443` (Tailscale Serve HTTPS, SNI `hp-linux.<TAILNET>.ts.net`) | `qwen3.5-9b` | **Yes** (llama.cpp API key) | Operational |

Each node has its **own** API key. Keys are never stored in this repository, in Nginx, or on the VPS (except inside the `.env` of a client app that uses the node, such as Open WebUI).

**Model aliases change** when a node is reloaded with a different model (ai-n1 moved from `qwen3.8-27b` to `qwen3-coder-next` on 2026-10-25). The live source of truth is always `GET /v1/models` with the node's key; update the alias column when you swap models.

All public hostnames resolve to the VPS public IPv4 **`<VPS_PUBLIC_IP>`**. Never publish a `100.x.x.x` Tailscale address in public DNS.

**Consumers**

| Client | Node(s) used | Where configured |
|---|---|---|
| Open WebUI (`chat.marin.cr`) | ai-n1, ai-n2, ai-n3 | Open WebUI DB `config` row `openai.api_base_urls` (and `apps/openwebui/.env` `OPENAI_API_BASE_URL` for fresh installs, ai-n1 only) — see [Section 7](#7-open-webui-connections) |

---

## 2. Architecture

```text
Remote client (Open WebUI, OpenCode, agents, curl)
    |
    | HTTPS :443   Authorization: Bearer <node API key>
    v
AWS Route 53 (zone marin.cr, <ROUTE53_ZONE_ID>)
    ai-n1.marin.cr, ai-n2.marin.cr, ai-n3.marin.cr ...  A -> <VPS_PUBLIC_IP>
    |
    v
Public Ubuntu VPS: <VPS_HOSTNAME>
    | Nginx (container proxy-nginx, network_mode: host)
    | One vhost + Let's Encrypt cert per node: conf.d/ai-n<N>.marin.cr.conf
    |
    | Tailscale tunnel (encrypted, tailnet only)
    +--------------------------------+---------------------------------+
    v                                v                                 v
ai-n1: ai-marin-linux            ai-n2: msi-linux                  ai-n3: hp-linux
 <TAILNET_IP_AI_N1>                    <TAILNET_IP_AI_N2>
 tailscale serve TCP 18080        tailscale serve HTTPS 443
   -> 127.0.0.1:8080                -> local llama.cpp
 llama-qwen.service               llama.cpp
 qwen3-coder-next (API key)       qwen3.6-35b-a3b (API key)
```

**TLS boundary:** client → VPS is HTTPS (Let's Encrypt, terminated by Nginx). VPS → node runs inside the encrypted Tailscale tunnel. For ai-n2 it is additionally HTTPS end-to-end with the node's Tailscale-issued certificate, verified by Nginx.

**Availability:** each node depends on the VPS, the node, Tailscale on both, the node's Tailscale Serve config, and llama.cpp. Nodes are independent — one node down does not affect the others.

**Existing VPS components reused (no changes needed):** `proxy/compose.yml`, `proxy/nginx/nginx.conf` (loads `conf.d/*.conf`), `proxy/letsencrypt/`, `proxy/webroot/`, `shared/renew-all-certs.sh`, `shared/check-cert-health.sh`.

---

## 3. Two supported upstream patterns

A node may expose llama.cpp to the tailnet in either way. Both are tailnet-only (`tailscale serve`, **not** `funnel`).

### Pattern A — Tailscale Serve TCP (used by ai-n1)

On the node:

```bash
sudo tailscale serve --bg --tcp=18080 tcp://127.0.0.1:8080
tailscale serve status
```

Nginx upstream (plain HTTP inside the tunnel):

```nginx
proxy_pass http://<TAILNET_IP>:18080;
proxy_set_header Host $host;
```

### Pattern B — Tailscale Serve HTTPS (used by ai-n2)

On the node (example; serves `https://<host>.<TAILNET>.ts.net` with a Tailscale cert):

```bash
sudo tailscale serve --bg --https=443 http://127.0.0.1:8080
tailscale serve status
```

Nginx upstream — use the **IP**, not the MagicDNS name, so Nginx never depends on MagicDNS when it starts (a failing name lookup at startup would take down **every** site on the proxy). SNI and `Host` carry the tailnet hostname, which Tailscale Serve needs for routing and the certificate check:

```nginx
proxy_pass https://<TAILNET_IP>:443;
proxy_ssl_server_name on;
proxy_ssl_name <host>.<TAILNET>.ts.net;
proxy_ssl_verify on;
proxy_ssl_verify_depth 3;
proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;
proxy_set_header Host <host>.<TAILNET>.ts.net;
```

How to tell which pattern a node uses, from the VPS:

```bash
tailscale ping <host>
curl -i http://<TAILNET_IP>:18080/health                          # Pattern A
curl -i --resolve <host>.<TAILNET>.ts.net:443:<TAILNET_IP> \
  https://<host>.<TAILNET>.ts.net/health                         # Pattern B
```

---

## 4. Current node configuration

### 4.1 ai-n1 — ai-marin-linux

- Vhost: `/opt/infra/proxy/nginx/conf.d/ai-n1.marin.cr.conf` (Pattern A).
- Certificate: `/etc/letsencrypt/live/ai-n1.marin.cr/` (container path). Issued 2026-10-25.
- Node service: `llama-qwen.service` (systemd, enabled), llama.cpp on `127.0.0.1:8080`, existing API key.
- Tailscale Serve: `sudo tailscale serve --bg --tcp=18080 tcp://127.0.0.1:8080` (tailnet only).
- Current model (2026-10-25): `qwen3-coder-next` — ~79.7B params, GGUF Q4_K_M, ~48.4 GB, `n_ctx` 131,072 (train 262,144), text in/out. llama.cpp build `b11577-cbe28bb3c`.
- Previous model (2026-10-12 → 2026-10-25): `qwen3.8-27b` — ~26.9B params, Q4_K_M, ~18.96 GB, `n_ctx` 65,536.
- Consumers: Open WebUI (`chat.marin.cr`) connection index 0.
- History: originally published as `localai.marin.cr` (2026-10-12). That hostname, its vhost, certificate and Route 53 record were **removed on 2026-10-25**. Any client still using it must switch to `https://ai-n1.marin.cr/v1`; the API key is unchanged because it is the same node.

Node-side checks (on `ai-marin-linux`):

```bash
sudo systemctl status llama-qwen --no-pager
curl http://127.0.0.1:8080/health
tailscale serve status
sudo journalctl -u llama-qwen -n 100 --no-pager
```

### 4.2 ai-n2 — msi-linux

- Vhost: `/opt/infra/proxy/nginx/conf.d/ai-n2.marin.cr.conf` (Pattern B).
- Certificate: `/etc/letsencrypt/live/ai-n2.marin.cr/`. Issued 2026-10-25.
- Upstream: Tailscale Serve HTTPS on `msi-linux.<TAILNET>.ts.net:443` (`<TAILNET_IP_AI_N2>`), certificate issued by Let's Encrypt through Tailscale; Nginx verifies it.
- Model (observed 2026-10-25, before the key was enabled): `qwen3.6-35b-a3b` — ~34.7B params, GGUF Q4_K_M, ~20.4 GB, `n_ctx` 65,536 (train 262,144), text in/out.
- **Authentication:** llama.cpp API key enabled on 2026-10-25. It was briefly published without a key (see [Section 9.1](#91-ai-n2-api-key-history)). Unauthenticated `GET /v1/models` now returns `401`.
- Consumers: Open WebUI (`chat.marin.cr`) connection index 2, added through the admin UI on 2026-10-25. Verified by a chat reply from `qwen3.6-35b-a3b`, which also confirms that model is still the one served.
- Node-side checks (on `msi-linux`):

```bash
tailscale serve status                      # expect https://msi-linux.<TAILNET>.ts.net (tailnet only) -> local llama.cpp
curl http://127.0.0.1:<LLAMA_PORT>/health   # <LLAMA_PORT> = target shown by tailscale serve status
```

The systemd unit name and launch flags on msi-linux are managed on the node and are not recorded here. Add them to this section when known.

### 4.3 ai-n3 — hp-linux

- Vhost: `/opt/infra/proxy/nginx/conf.d/ai-n3.marin.cr.conf` (Pattern B, copied from ai-n2).
- Certificate: `/etc/letsencrypt/live/ai-n3.marin.cr/`. Issued 2026-10-31, expires 2027-01-29.
- Upstream: Tailscale Serve HTTPS on `hp-linux.<TAILNET>.ts.net:443` (`<TAILNET_IP_AI_N3>`), forwarding to llama.cpp on `127.0.0.1:8080`; Nginx verifies the Tailscale-issued certificate.
- Model (observed 2026-10-31): `qwen3.5-9b` — `n_ctx` 32,768 (train 262,144), text in/out, llama.cpp build `b12023-f3586e66b`. Streams a `reasoning_content` thinking phase before the answer.
- **Authentication:** llama.cpp API key enabled before publishing. Unauthenticated or wrong-key requests return `401`.
- Consumers: Open WebUI (`chat.marin.cr`) connection index 3, added 2026-10-31 (see [Section 7](#7-open-webui-connections)).
- Note: the first HTTPS request after `tailscale serve --https=443` timed out while Tailscale provisioned the node's certificate; it worked ~20 s later.
- Node-side checks (on `hp-linux`):

```bash
tailscale serve status                      # expect https://hp-linux.<TAILNET>.ts.net -> http://127.0.0.1:8080
curl http://127.0.0.1:8080/health
```

---

## 5. Validation (any node)

Run from the VPS or any external machine:

```bash
dig +short ai-n<N>.marin.cr                     # expect <VPS_PUBLIC_IP>
curl -i https://ai-n<N>.marin.cr/health          # expect 200 {"status":"ok"}
curl -i https://ai-n<N>.marin.cr/v1/models       # expect 401 when the node has an API key
```

Authenticated model listing (keeps the key out of shell history):

```bash
read -rsp "Node API key: " LLAMA_API_KEY
echo

curl -sS https://ai-n<N>.marin.cr/v1/models \
  -H "Authorization: Bearer ${LLAMA_API_KEY}"

unset LLAMA_API_KEY
```

Streaming inference test (replace the model alias with the node's alias from the registry):

```bash
read -rsp "Node API key: " LLAMA_API_KEY
echo

curl -N https://ai-n<N>.marin.cr/v1/chat/completions \
  -H "Authorization: Bearer ${LLAMA_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<MODEL_ALIAS>",
    "messages": [{"role": "user", "content": "Respond with one short sentence confirming inference works."}],
    "stream": true,
    "max_tokens": 150
  }'

unset LLAMA_API_KEY
```

Expected: `data: ...` server-sent events ending with `data: [DONE]`.

**Recorded results (2026-10-25, from the VPS):**

| Check | ai-n1 | ai-n2 |
|---|---|---|
| DNS → `<VPS_PUBLIC_IP>` (authoritative Route 53) | ✅ | ✅ |
| Served certificate | `CN=ai-n1.marin.cr`, expires 2027-01-23, verify ok | `CN=ai-n2.marin.cr`, expires 2027-01-23, verify ok |
| `GET /health` over public HTTPS | 200 `{"status":"ok"}` | 200 `{"status":"ok"}` |
| `GET /v1/models` without key | 401 | 401 (after key enabled; 200 before) |
| `GET /v1/models` with key | 200 (Open WebUI's key) — `qwen3-coder-next` | not tested from the VPS (key held by operator) |
| Streaming `/v1/chat/completions` | ✅ SSE chunks ending `data: [DONE]` (sent from the `open-webui` container) | not tested from the VPS (key held by operator) |
| VPS → node over Tailscale | `tailscale ping ai-marin-linux` ok | `tailscale ping msi-linux` ok (~120 ms) |
| Open WebUI → node | ✅ 200 with its stored key | ✅ chat reply from `qwen3.6-35b-a3b` (operator test in the UI) |

> **Tip:** this VPS's local stub resolver (`127.0.0.53`) occasionally times out. If `curl` reports `Could not resolve host` while `dig @ns-427.awsdns-53.com ai-n<N>.marin.cr` works, test with `--resolve ai-n<N>.marin.cr:443:<VPS_PUBLIC_IP>`.

---

## 6. Adding a new node

Example below uses **ai-n3**. Substitute `<N>`, the tailnet host and IP. All VPS steps run as root on `<VPS_HOSTNAME>`.

### Step 1 — Prepare the node (on the node)

1. Join the node to the tailnet (`tailscale up`) and confirm llama.cpp answers locally: `curl http://127.0.0.1:8080/health`.
2. **Configure an API key** on llama.cpp (`--api-key` / `LLAMA_API_KEY`) **before** publishing it. Do not repeat ai-n2's temporary open state.
3. Expose it tailnet-only with [Pattern A or B](#3-two-supported-upstream-patterns), then `tailscale serve status`.

### Step 2 — Register the node and create DNS

Add the row to the [node registry](#1-node-registry). Then create the record in Route 53 (zone `marin.cr`, ID `<ROUTE53_ZONE_ID>`): `ai-n<N>.marin.cr`, type `A`, value `<VPS_PUBLIC_IP>`, TTL `300`, simple routing. Console, or AWS CLI with credentials that can edit the zone:

```bash
nano /root/ai-n3-dns.json
```

```json
{
  "Comment": "AI inference node ai-n3",
  "Changes": [
    {
      "Action": "UPSERT",
      "ResourceRecordSet": {
        "Name": "ai-n3.marin.cr",
        "Type": "A",
        "TTL": 300,
        "ResourceRecords": [{ "Value": "<VPS_PUBLIC_IP>" }]
      }
    }
  ]
}
```

```bash
aws route53 change-resource-record-sets \
  --hosted-zone-id <ROUTE53_ZONE_ID> \
  --change-batch file:///root/ai-n3-dns.json
dig +short ai-n3.marin.cr @ns-427.awsdns-53.com
```

(`ai-n3` already exists — skip this step for it.)

### Step 3 — Verify VPS → node over Tailscale

```bash
tailscale ping <host>
curl -i http://<TAILNET_IP>:18080/health      # Pattern A
# or, Pattern B:
curl -i --resolve <host>.<TAILNET>.ts.net:443:<TAILNET_IP> https://<host>.<TAILNET>.ts.net/health
```

Also confirm Nginx itself can reach it: `docker exec proxy-nginx wget -q -O- http://<TAILNET_IP>:18080/health` (Pattern A).

### Step 4 — Bootstrap HTTP vhost

```bash
nano /opt/infra/proxy/nginx/conf.d/ai-n3.marin.cr.conf
```

```nginx
server {
    listen 80;
    listen [::]:80;

    server_name ai-n3.marin.cr;

    location /.well-known/acme-challenge/ {
        root /var/www/certbot;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}
```

```bash
docker exec proxy-nginx nginx -t
docker exec proxy-nginx nginx -s reload

echo "ai-n3-ready" > /opt/infra/proxy/webroot/.well-known/acme-challenge/ai-n3-test
curl -fsS http://ai-n3.marin.cr/.well-known/acme-challenge/ai-n3-test
rm /opt/infra/proxy/webroot/.well-known/acme-challenge/ai-n3-test
```

### Step 5 — Issue the certificate

```bash
docker run --rm \
  -v /opt/infra/proxy/letsencrypt:/etc/letsencrypt \
  -v /opt/infra/proxy/webroot:/var/www/certbot \
  certbot/certbot:latest certonly \
  --webroot -w /var/www/certbot \
  -d ai-n3.marin.cr \
  --email <ADMIN_EMAIL> \
  --agree-tos \
  --no-eff-email

ls -l /opt/infra/proxy/letsencrypt/live/ai-n3.marin.cr/
grep -E "archive_dir|webroot_path" /opt/infra/proxy/letsencrypt/renewal/ai-n3.marin.cr.conf
```

The renewal file must show container paths (`/etc/letsencrypt/...`, `/var/www/certbot`). No per-node cron job: `shared/renew-all-certs.sh` and `shared/check-cert-health.sh` pick up every directory under `live/` automatically.

### Step 6 — Final HTTPS vhost

Copy the closest existing node as a template, then edit the names/IP:

```bash
cp /opt/infra/proxy/nginx/conf.d/ai-n1.marin.cr.conf /opt/infra/proxy/nginx/conf.d/ai-n3.marin.cr.conf   # Pattern A
# or
cp /opt/infra/proxy/nginx/conf.d/ai-n2.marin.cr.conf /opt/infra/proxy/nginx/conf.d/ai-n3.marin.cr.conf   # Pattern B

nano /opt/infra/proxy/nginx/conf.d/ai-n3.marin.cr.conf
```

Change: header comment, both `server_name` lines, both `ssl_certificate*` paths, `proxy_pass` IP, and (Pattern B) `proxy_ssl_name` / `Host`. Keep: `Authorization` passthrough, `proxy_buffering off`, `proxy_cache off`, `Connection ""`, `client_max_body_size 100m`, timeouts `15s/600s/600s`.

```bash
grep -n "ai-n\|proxy_pass\|proxy_ssl_name\|ts.net" /opt/infra/proxy/nginx/conf.d/ai-n3.marin.cr.conf
docker exec proxy-nginx nginx -t
docker exec proxy-nginx nginx -s reload
```

### Step 7 — Validate, document, commit

1. Run [Section 5](#5-validation-any-node). Unauthenticated `/v1/models` must return `401`.
2. Update: the [node registry](#1-node-registry) (status → Operational), [Section 4](#4-current-node-configuration), the [change log](#13-change-log), `SERVER.md`, the Services & Domains table in `CLAUDE.md`, the deployed-apps table in `docs/server-setup.md`, and the AI-node table in `docs/tailscale-setup.md` (section 7.2).
3. Optionally add the node to Open WebUI ([Section 7](#7-open-webui-connections)).
4. Commit:

```bash
cd /opt/infra
git add proxy/nginx/conf.d/ai-n3.marin.cr.conf docs/ai-nodes-setup.md SERVER.md CLAUDE.md docs/server-setup.md docs/tailscale-setup.md
git diff --cached --check
git diff --cached
git commit -m "Add AI node ai-n3"
git push origin main
```

---

## 7. Open WebUI connections

Open WebUI stores its OpenAI-compatible connections in its **database** after first boot (persistent config). Changing `OPENAI_API_BASE_URL` in `apps/openwebui/.env` affects fresh installs only.

- **Preferred:** chat.marin.cr → Admin Panel → Settings → Connections → OpenAI API → add `https://ai-n<N>.marin.cr/v1` with the node's key.
- **Inspect from the VPS:**

```bash
docker exec infra-postgres psql -U admin -d open_webui -Atc \
  "select key, value from config where key in ('openai.api_base_urls','openai.api_configs');"
```

Current stored connections (2026-10-31):

| Index | Base URL | Purpose |
|---|---|---|
| 0 | `https://ai-n1.marin.cr/v1` | ai-n1 (key in `openai.api_keys[0]`) |
| 1 | `https://api.openai.com/v1` | OpenAI (added in the UI, filtered model list) |
| 2 | `https://ai-n2.marin.cr` | ai-n2 (key in `openai.api_keys[2]`), added in the UI |
| 3 | `https://ai-n3.marin.cr/v1` | ai-n3 (key in `openai.api_keys[3]`), added by SQL on 2026-10-31 (no prefix ID, default API type) |

The ai-n2 entry has no `/v1` suffix. It still works because llama.cpp serves the OpenAI routes both with and without the `/v1` prefix (`/models`, `/chat/completions`). New connections should use `https://ai-n<N>.marin.cr/v1` for consistency.

Also present: an Ollama connection `http://host.docker.internal:11434` (`ollama.enable = true`). No Ollama server exists, and `host.docker.internal` does not resolve inside this container, so it only logs `Cannot connect to host host.docker.internal:11434` on every model refresh. It is safe to disable it under Admin → Settings → Connections → Ollama API.

`openai.api_base_urls`, `openai.api_keys` and `openai.api_configs` are **parallel, index-aligned** lists. Never edit one without the others; adding a node by SQL is error-prone, so use the admin UI.

**Adding a node to Open WebUI (UI steps):**

1. Sign in at `https://chat.marin.cr` as the admin.
2. Admin Panel → Settings → Connections → OpenAI API → **+** (add connection).
3. URL `https://ai-n<N>.marin.cr/v1`, auth Bearer, key = that node's API key. Optionally set a prefix ID (e.g. `n2`) so models with the same name on different nodes stay distinct.
4. Save, then confirm the node's model appears in the model picker.

**Repointing an existing connection by SQL** (what was done on 2026-10-25 for localai → ai-n1; the key stayed the same because it was the same node):

```bash
docker exec infra-postgres psql -U admin -d open_webui -Atc \
  "select value from config where key='openai.api_base_urls';" > /root/openwebui-api_base_urls.backup.json

docker exec infra-postgres psql -U admin -d open_webui -c \
  "update config set value = replace(value::text, 'https://localai.marin.cr/v1', 'https://ai-n1.marin.cr/v1')::json where key='openai.api_base_urls';"

cd /opt/infra/apps/openwebui
docker compose restart open-webui
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8324/health   # 200 after ~30 s
```

Open WebUI needs roughly 20–40 seconds after a restart before `chat.marin.cr` stops returning `502`. Also update `OPENAI_API_BASE_URL` in `apps/openwebui/.env` so a fresh install matches.

Verify the stored key works against a node without printing it:

```bash
docker exec open-webui sh -c 'curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $OPENAI_API_KEY" https://ai-n1.marin.cr/v1/models'
```

---

## 8. Certificate renewal and operations

Renewal is centralized (`shared/renew-all-certs.sh`, cron); see `docs/certificate-renewal-setup.md`. Served-certificate check for a node:

```bash
echo | openssl s_client -connect ai-n<N>.marin.cr:443 -servername ai-n<N>.marin.cr 2>/dev/null \
  | openssl x509 -noout -dates -subject -issuer
```

VPS checks for all nodes:

```bash
tailscale status
for n in 1 2; do curl -s -o /dev/null -w "ai-n$n %{http_code}\n" https://ai-n$n.marin.cr/health; done
docker exec proxy-nginx nginx -t
docker logs --tail 100 proxy-nginx
tail -n 100 /opt/infra/proxy/nginx/logs/error.log
```

For Pattern B nodes, the node's own Tailscale certificate renews on the node via Tailscale. If it ever lapses, Nginx returns `502` and `error.log` shows an `upstream SSL certificate verify error`.

---

## 9. Security and known limitations

### 9.1 ai-n2 API key history

ai-n2 was published on 2026-10-25 **before** its API key existed. For a short window, anyone could list models and run inference on msi-linux. The operator then enabled a llama.cpp API key the same day, and unauthenticated `/v1/models` now returns `401`. No Nginx change was needed, because the vhost already forwards `Authorization`.

**Rule for future nodes:** enable the API key on the node **before** step 4 of [Section 6](#6-adding-a-new-node). If a node must be published early, disable its vhost until the key is in place ([Section 10](#10-removal-and-rollback)).

To rotate a node's key: change it on the node, restart llama.cpp, update every consumer (e.g. Open WebUI → Admin → Connections), then verify `401` without and `200` with the new key.

### 9.2 Applies to all nodes

- Public client → VPS uses HTTPS. VPS → node uses the encrypted Tailscale tunnel. All Serve configs are tailnet-only (not Funnel). No home-router port forwarding.
- Nginx does **not** validate API keys itself; it forwards the client's `Authorization` header and llama.cpp enforces it. Unprotected routes such as `/health` stay public.
- `location /` forwards **every** path to llama.cpp. Consider allowlisting only the paths clients need.
- Recommended hardening, not yet done: Nginx rate/concurrency limits per node; Tailscale ACLs/grants so only the VPS (`prod-server`/`<VPS_HOSTNAME>`) can reach node listeners; check each node's real llama.cpp bind address (`sudo ss -lntp | grep 8080`).
- Never commit API keys, `.env` files, `proxy/letsencrypt/` or webroot runtime files.

---

## 10. Removal and rollback

**Temporarily disable one node's public access** (other nodes unaffected):

```bash
mv /opt/infra/proxy/nginx/conf.d/ai-n2.marin.cr.conf /opt/infra/proxy/nginx/ai-n2.marin.cr.conf.disabled
docker exec proxy-nginx nginx -t
docker exec proxy-nginx nginx -s reload
```

Restore with the reverse `mv`, then test and reload again. After disabling, requests to that hostname fall through to the default server. Check that this behaves as you expect.

**Permanently retire a node** (the procedure used for `localai.marin.cr` on 2026-10-25):

1. Move every client off it. Search with `grep -rIl "ai-n<N>.marin.cr" /opt` and check Open WebUI's stored connections ([Section 7](#7-open-webui-connections)).
2. Remove the vhost, then test and reload Nginx.
3. Delete the certificate so renewal and health checks stop tracking it:
   ```bash
   docker run --rm -v /opt/infra/proxy/letsencrypt:/etc/letsencrypt certbot/certbot:latest delete --cert-name ai-n<N>.marin.cr
   ```
4. Delete the Route 53 A record (console, or `change-resource-record-sets` with `"Action": "DELETE"` and the exact existing TTL/value).
5. Update the registry, `SERVER.md` and `CLAUDE.md`, then commit.

**Disable a node's tailnet listener** (on the node): `sudo tailscale serve --tcp=18080 off` (Pattern A) or `sudo tailscale serve --https=443 off` (Pattern B). The public hostname then returns `502`.

---

## 11. Troubleshooting

| Symptom | Likely cause | What to check |
|---|---|---|
| `dig ai-n<N>.marin.cr` empty / wrong | Route 53 record missing | Hosted zone `marin.cr`, A → `<VPS_PUBLIC_IP>`, stray AAAA |
| Cert issuance fails | Port 80, DNS, webroot | ACME test file curl, UFW, `nginx -t` |
| `502` Pattern A node | Listener off / node offline | `tailscale ping <host>`, `curl http://<IP>:18080/health`, `tailscale serve status` on node |
| `502` Pattern B node | Serve off, wrong SNI/Host, upstream cert invalid | `error.log` for `SSL`/`verify` errors; `curl --resolve <host>:443:<IP> https://<host>/health` |
| `504` | Slow/stalled model | llama.cpp logs on node, model load, timeouts |
| `401 Invalid API Key` | Missing/wrong bearer key | Client config; key is per node |
| `200` on `/v1/models` with no key | Node has no API key | Enable `--api-key` on the node ([9.1](#91-ai-n2-api-key-history)) |
| Open WebUI shows old/missing models | Node's model was swapped | Admin → Settings → Connections → refresh/verify; the alias comes from `/v1/models` |
| Streaming delayed | Buffering | `proxy_buffering off` in the vhost; `curl -N` |
| Works until node reboot | Serve or llama.cpp not persistent | `systemctl is-enabled <unit>`, `tailscale serve status` |
| Brief `000`/`502` right after reload | Nginx worker swap | Retry after a few seconds |

Do not fix reachability by opening llama.cpp ports on the router or the node's LAN. Fix Tailscale instead.

---

## 12. As-built Nginx vhosts (2026-10-25)

The files in `proxy/nginx/conf.d/` are authoritative. These copies are for reference and must match them.

### 12.1 `proxy/nginx/conf.d/ai-n1.marin.cr.conf` (Pattern A)

```nginx
# AI inference node 1 (ai-n1) — ai-marin-linux (MINISFORUM), llama.cpp
# Formerly published as localai.marin.cr. Runbook: docs/ai-nodes-setup.md
server {
    listen 80;
    listen [::]:80;

    server_name ai-n1.marin.cr;

    location /.well-known/acme-challenge/ {
        root /var/www/certbot;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;

    server_name ai-n1.marin.cr;

    ssl_certificate /etc/letsencrypt/live/ai-n1.marin.cr/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/ai-n1.marin.cr/privkey.pem;

    client_max_body_size 100m;

    location / {
        # ai-marin-linux over private Tailscale network
        # (tailscale serve --tcp=18080 -> 127.0.0.1:8080, plain HTTP inside the tailnet)
        proxy_pass http://<TAILNET_IP_AI_N1>:18080;

        proxy_http_version 1.1;

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Authorization $http_authorization;

        # Stream LLM tokens immediately
        proxy_buffering off;
        proxy_cache off;
        proxy_set_header Connection "";

        # Long-running inference requests
        proxy_connect_timeout 15s;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
    }
}
```

### 12.2 `proxy/nginx/conf.d/ai-n2.marin.cr.conf` (Pattern B)

```nginx
# AI inference node 2 (ai-n2) — msi-linux, llama.cpp
# Runbook: docs/ai-nodes-setup.md
server {
    listen 80;
    listen [::]:80;

    server_name ai-n2.marin.cr;

    location /.well-known/acme-challenge/ {
        root /var/www/certbot;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;

    server_name ai-n2.marin.cr;

    ssl_certificate /etc/letsencrypt/live/ai-n2.marin.cr/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/ai-n2.marin.cr/privkey.pem;

    client_max_body_size 100m;

    location / {
        # msi-linux over private Tailscale network
        # (tailscale serve HTTPS :443 with a Tailscale-issued certificate)
        # Tailscale IP used instead of the MagicDNS name so nginx never depends
        # on MagicDNS at startup; SNI/Host still carry the tailnet hostname.
        proxy_pass https://<TAILNET_IP_AI_N2>:443;

        proxy_ssl_server_name on;
        proxy_ssl_name msi-linux.<TAILNET>.ts.net;
        proxy_ssl_verify on;
        proxy_ssl_verify_depth 3;
        proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;

        proxy_http_version 1.1;

        # Tailscale Serve routes by hostname, so Host must be the tailnet name
        proxy_set_header Host msi-linux.<TAILNET>.ts.net;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Authorization $http_authorization;

        # Stream LLM tokens immediately
        proxy_buffering off;
        proxy_cache off;
        proxy_set_header Connection "";

        # Long-running inference requests
        proxy_connect_timeout 15s;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
    }
}
```

Directive notes (both nodes):

- `Authorization $http_authorization` forwards the client's bearer key. No key lives on the VPS.
- `proxy_buffering off` / `proxy_cache off` / `Connection ""` keep server-sent-event token streams flowing immediately.
- `proxy_read_timeout 600s` is the maximum gap between upstream reads, not a total time limit for a request.
- `client_max_body_size 100m` caps request bodies (long prompts / documents).

---

## 13. Change log

| Date | Change |
|---|---|
| 2026-10-12 | ai-marin-linux published as `localai.marin.cr` (Pattern A), Let's Encrypt cert, Route 53 A record. |
| 2026-10-25 | Route 53 A records created for `ai-n1`, `ai-n2`, `ai-n3` → `<VPS_PUBLIC_IP>` (zone `<ROUTE53_ZONE_ID>`, TTL 300). |
| 2026-10-25 | Certs issued for `ai-n1.marin.cr` and `ai-n2.marin.cr`. Vhosts `ai-n1.marin.cr.conf` (ai-marin-linux, Pattern A) and `ai-n2.marin.cr.conf` (msi-linux, Pattern B) deployed. |
| 2026-10-25 | Open WebUI repointed from `localai.marin.cr` to `ai-n1.marin.cr` (DB `config.openai.api_base_urls` index 0 and `.env`). |
| 2026-10-25 | `localai.marin.cr` retired: vhost removed, cert deleted (`certbot delete`), Route 53 record deleted. Runbook renamed `docs/localai-setup.md` → `docs/ai-nodes-setup.md`. |
| 2026-10-25 | ai-n2 API key enabled on msi-linux (unauthenticated `/v1/models` → 401). |
| 2026-10-25 | ai-n1 model observed as `qwen3-coder-next` (was `qwen3.8-27b`). |
| 2026-10-25 | ai-n2 added to Open WebUI as connection index 2 (admin UI); chat verified with `qwen3.6-35b-a3b`. |
| 2026-10-31 | ai-n3 (hp-linux, Pattern B) published: HTTP bootstrap vhost, cert issued (expires 2027-01-29), final HTTPS vhost. Verified: `/health` 200, `/v1/models` 401 without key and 200 with key (`qwen3.5-9b`), streaming chat ends `data: [DONE]`, HTTP→HTTPS 301, `check-cert-health.sh` OK. |
| 2026-10-31 | ai-n3 added to Open WebUI as connection index 3 (appended to `openai.api_base_urls`, `openai.api_keys`, `openai.api_configs` in one transaction; container restarted; stored key verified from inside the container: 200). |
| next | ai-n4 — create DNS, then follow [Section 6](#6-adding-a-new-node). |

---

## 14. References

- VPS server setup (`server-setup.md`) · Certificate renewal (`certificate-renewal-setup.md`) · Tailscale setup (`tailscale-setup.md`): these live in the VPS repository and are not archived here
- Open WebUI deployment: `apps/openwebui/DEPLOY.md`
- [Local AI llama.cpp setup runbook (node install)](https://gist.githubusercontent.com/marinoscar/8c39bd470eb5d29cb43771d6f58b2454/raw/7b76bade8b9e82a50d1b9ead288002bbb37fe021/ms-s1-max-local-ai-llamacpp-runbook.md)
