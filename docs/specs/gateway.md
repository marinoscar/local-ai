# Spec: Gateway (`https://ai.marin.cr/v1`)

> **Status:** approved design · **Epic:** E7 · **Code:** `apps/cloud/apps/api/src/localai/gateway/`, `apps/cloud/apps/web/src/localai/gateway/`, `apps/cloud/infra/nginx/app.d/locations/gateway.conf` · **Doctor area:** `LAI-GW`
> Canonical names: [overview.md](../architecture/overview.md) §3. Host app: [cloud-app.md](cloud-app.md). Threat model: [security-model.md](../architecture/security-model.md) §1 (T1, T2).

## 1. Purpose and scope

One OpenAI-compatible API for the whole lab. A client (Open WebUI, OpenCode, scripts, agents) uses **one URL and one key**:

- `https://ai.marin.cr/v1`, with a per-user `lai_…` key,
- the gateway picks a node that serves the requested `model`, calls that node's llama-server with the node's own key, and streams the answer back without buffering,
- it enforces per-key scopes, expiry and rate limits, and records usage **metadata only**.

**Non-goals**
- Not a model host. No inference runs on the VPS.
- No external providers (OpenAI, Anthropic). EAB's `ai` slice is off ([cloud-app.md](cloud-app.md) §3.2). Open WebUI keeps its own OpenAI connection if wanted.
- No prompt or response storage, no prompt logging, no caching of answers.
- Not available offline. A node in `offline`/`survival` is simply not routable. Local clients on a node keep using `http://127.0.0.1:8080/v1` with the node key ([agent.md](agent.md) §3.8.13).
- No cross-node load balancing of a single request, no request queueing beyond llama-server's own slots.
- `/v1/audio/speech` (TTS), `/v1/images/*`, `/v1/responses`, `/v1/assistants`, fine-tuning, files: not served (404 `endpoint_not_supported`).

## 2. Concepts

| Term | Meaning |
|---|---|
| **Gateway key** | `lai_` + 43 chars base64url (32 random bytes). Stored as sha256 hex. Owned by a user. Shown once |
| **Service key** | A gateway key with `kind = service`, issued by a `gateway:admin` for a system client (Open WebUI). May have no expiry and higher limits |
| **Candidate** | A (node, model) pair the gateway may send a request to |
| **Routable node** | `gatewayRoutable` of [cloud-app.md](cloud-app.md) §4.5: `health = healthy`, `mode = online`, `gatewayEnabled`, `phase = enrolled`, `servePattern != none`, and a stored key (`keyReportedAt != null`). Candidates the gateway marked `keyInvalid` (§4.3) are skipped in memory |
| **Model policy** | Per model: enabled, aliases, on-demand load, wait time, timeouts, preferred nodes (`localai_model_policies`) |
| **First byte** | The moment the gateway writes the response status line to the client. Before it, a failed attempt can be retried on another node; after it, never |

## 3. Interfaces

### 3.1 Routing of `/v1/` through nginx and Fastify

Edge (shared host proxy): declared by the app CLI, `proxy: { streamPaths: [{ path: '/v1/', timeoutSeconds: 1800 }] }` ([cloud-app.md](cloud-app.md) §3.13), so the edge vhost does not buffer `/v1/` and does not cut it at 60 s.

Inner nginx: `apps/cloud/infra/nginx/app.d/locations/gateway.conf` (included before `location /api`):
```nginx
# OpenAI-compatible gateway (gateway.md §3.1). /v1/x -> API /api/gateway/v1/x, unbuffered.
location /v1/ {
    proxy_pass http://api_upstream/api/gateway/v1/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Request-ID $request_id;
    proxy_set_header Connection '';
    proxy_buffering off;
    proxy_cache off;
    proxy_request_buffering on;
    chunked_transfer_encoding off;
    proxy_connect_timeout 10s;
    proxy_send_timeout 1800s;
    proxy_read_timeout 1800s;
    client_max_body_size 26m;
    include /etc/nginx/platform/security-headers.conf;
}
```
`/v1` (no slash) answers `301 /v1/`. Nothing else under `/v1/` is served by the web app.

API: the gateway routes are **raw Fastify routes** under `/api/gateway/v1/*`, registered in `apps/api/src/main.ts` by `registerGatewayRoutes(app)` after `NestFactory.create` and before `app.listen` (the same pattern as EAB's `registerPlatformDocs`). Services (`GatewayService`, `RoutingTable`, `KeyService`, `UsageRecorder`) are resolved from the Nest container with `app.get(...)`.

Why raw routes, and the NestJS caveats this avoids or must handle:
1. EAB's global `TransformInterceptor` wraps bodies in `{ data, meta }`, and `HttpExceptionFilter` rewrites errors into the EAB shape. OpenAI clients need the OpenAI shape. Raw routes bypass both.
2. EAB's `JwtAuthGuard` would try to read `Bearer lai_…` as a JWT and answer 401 in EAB format. Raw routes do their own auth (§4.1).
3. Nest middleware (`RequestIdMiddleware`) and `MaintenanceGuard` don't run. The gateway reads/creates `x-request-id` itself and asks `MaintenanceModeService.isActive()` per request (503 `maintenance`).
4. Fastify hooks registered as plugins (cookie, OTel request attributes) still run. No compression plugin may be registered for these paths.
5. Streaming uses `reply.hijack()` and writes to `reply.raw` (Node `ServerResponse`). After `hijack()`, Fastify sends nothing and handles no error; every code path must end the response itself.
6. Raw routes are not in `/api/docs`. The OpenAI surface is documented here and in `GET /v1/` (a small JSON index: `{ "object": "localai.gateway", "endpoints": [...], "docs": "https://github.com/marinoscar/local-ai/blob/main/docs/specs/gateway.md" }`).
7. EAB's conformance check "every Nest route has `@Auth()`" does not see raw routes. Our own test (`gateway-routes.spec.ts`) asserts every registered gateway route calls `authenticate()` first, except `GET /v1/` and `OPTIONS`.

### 3.2 Endpoints

| Method | Path (public) | Upstream path on the node | Body limit | Notes |
|---|---|---|---|---|
| GET | `/v1/` | — | — | index, no auth |
| GET | `/v1/models` | — (from the routing table) | — | §4.4 |
| GET | `/v1/models/{id}` | — | — | 404 `model_not_found` if not listed |
| POST | `/v1/chat/completions` | `/v1/chat/completions` | 8 MiB | stream and non-stream; vision (`image_url` data URIs) |
| POST | `/v1/completions` | `/v1/completions` | 8 MiB | stream and non-stream |
| POST | `/v1/embeddings` | `/v1/embeddings` | 8 MiB | non-stream only; model must have role `embedding` |
| POST | `/v1/audio/transcriptions` | `/v1/audio/transcriptions` | 25 MiB, multipart | routed only to models whose `input_modalities` include `audio` (llama-server's OpenAI transcription route). Not whisper.cpp: the node's whisper server is loopback-only and not exposed through Tailscale |
| OPTIONS | `/v1/*` | — | — | CORS preflight (§6) |
| any other | `/v1/*` | — | — | 404 `endpoint_not_supported` |

Request example:
```bash
curl https://ai.marin.cr/v1/chat/completions \
  -H "Authorization: Bearer $LAI_KEY" -H "Content-Type: application/json" \
  -d '{"model":"qwen3.5-9b-q4km","messages":[{"role":"user","content":"Hola"}],"stream":true}'
```
Response headers the gateway adds: `x-request-id`, `x-localai-node: ai-n3` (node ids are not secret), `x-localai-attempts: 1`, and on streams `cache-control: no-cache`, `x-accel-buffering: no`. Rate-limit headers on every authenticated response: `x-ratelimit-limit-requests`, `x-ratelimit-remaining-requests`, `x-ratelimit-limit-tokens` (daily budget), `x-ratelimit-remaining-tokens`.

### 3.3 Error format (OpenAI-compatible)

Every gateway error, before the first byte, is JSON with the status below:
```json
{ "error": {
    "message": "No online node serves the model 'qwen3-coder-next-q4km' right now. Try again later or pick another model.",
    "type": "server_error",
    "param": null,
    "code": "no_capacity",
    "localai_code": "LAI-GW-021",
    "request_id": "4f1c…"
} }
```

| Situation | HTTP | `type` | `code` | `localai_code` |
|---|---|---|---|---|
| Malformed JSON / body not an object | 400 | invalid_request_error | `invalid_json` | LAI-GW-001 |
| Missing or non-string `model` | 400 | invalid_request_error | `missing_model` | LAI-GW-002 |
| `stream: true` on embeddings, or an unsupported multipart field | 400 | invalid_request_error | `unsupported_parameter` | LAI-GW-003 |
| Body over the endpoint limit | 413 | invalid_request_error | `request_too_large` | LAI-GW-004 |
| No/malformed/unknown/revoked/expired key | 401 | authentication_error | `invalid_api_key` | LAI-GW-010 |
| Too many failed auth attempts from this IP | 429 | rate_limit_error | `auth_rate_limited` | LAI-GW-011 |
| Model outside the key's scope | 403 | permission_error | `model_not_allowed` | LAI-GW-012 |
| Key owner deactivated or lost `gateway:use` | 403 | permission_error | `key_owner_not_allowed` | LAI-GW-013 |
| Unknown model (no catalog id, no alias) or policy disabled | 404 | not_found_error | `model_not_found` | LAI-GW-020 |
| Known model, no routable node can serve it now | 503 | server_error | `no_capacity` | LAI-GW-021 |
| On-demand load started but not ready within `loadWaitS` | 503 | server_error | `model_loading` | LAI-GW-022 |
| All candidates queue-saturated (inflight ≥ 4 × slots) | 503 | server_error | `no_capacity` | LAI-GW-023 |
| Endpoint not served | 404 | not_found_error | `endpoint_not_supported` | LAI-GW-024 |
| RPM limit | 429 | rate_limit_error | `rate_limit_exceeded` | LAI-GW-030 |
| Concurrent stream/request limit | 429 | rate_limit_error | `concurrency_limit_exceeded` | LAI-GW-031 |
| Daily token budget used up | 429 | rate_limit_error | `daily_token_budget_exceeded` | LAI-GW-032 |
| Upstream connect/TLS failure on every attempt | 502 | api_error | `upstream_unreachable` | LAI-GW-040 |
| Upstream 5xx on every attempt | 502 | api_error | `upstream_error` | LAI-GW-041 |
| Upstream rejected the request (4xx, e.g. context too long) | upstream status (400/422) | invalid_request_error | upstream `code` or `upstream_rejected` | LAI-GW-042 |
| Upstream said 401 (our stored node key is wrong) on every attempt | 502 | api_error | `upstream_auth_failed` | LAI-GW-043 |
| First byte not received within the timeout | 504 | server_error | `upstream_timeout` | LAI-GW-044 |
| Node key unreadable (credential store) | 503 | server_error | `gateway_misconfigured` | LAI-GW-050 |
| Gateway disabled (`LOCALAI_GATEWAY_ENABLED=false`) or EAB maintenance | 503 | server_error | `gateway_unavailable` | LAI-GW-051 |
| Unexpected exception | 500 | server_error | `internal_error` | LAI-GW-052 |

429 and 503 carry `Retry-After` (seconds): RPM → seconds until the window frees a slot; concurrency → 2; budget → seconds until 00:00 UTC; `model_loading` → 15; `no_capacity` → 30.

Upstream 4xx JSON bodies already in OpenAI format (llama-server returns `{"error":{"code":400,"message":"…","type":"invalid_request_error"}}`) are passed with their status, `message` kept, `code` normalised to a string, and `localai_code: "LAI-GW-042"` added.

**After the first byte** (streams), an upstream failure or timeout is reported as one final SSE event, then the stream ends:
```
data: {"error":{"message":"The node stopped answering in the middle of the reply.","type":"server_error","code":"upstream_interrupted","localai_code":"LAI-GW-045"}}

data: [DONE]

```

### 3.4 OpenAI `/v1/models` response
```json
{ "object": "list",
  "data": [
    { "id": "qwen3.5-9b-q4km", "object": "model", "created": 1793620800, "owned_by": "localai",
      "x-localai": { "state": "loaded", "nodes": ["ai-n3"], "display_name": "Qwen 3.5 9B",
                     "roles": ["chat", "reasoning", "coder"], "input_modalities": ["text"],
                     "context_length": 32768, "max_output_tokens": 8192, "reasoning": true, "alias_of": null } },
    { "id": "qwen3.6-35b-a3b-q4km", "object": "model", "created": 1793620800, "owned_by": "localai",
      "x-localai": { "state": "loadable", "nodes": ["ai-n2"], "display_name": "Qwen 3.6 35B A3B",
                     "roles": ["chat", "reasoning"], "input_modalities": ["text"],
                     "context_length": 32768, "max_output_tokens": 8192, "reasoning": true, "alias_of": null,
                     "load_wait_s": 120 } },
    { "id": "lab-coder", "object": "model", "created": 1793620800, "owned_by": "localai",
      "x-localai": { "state": "loaded", "nodes": ["ai-n1"], "alias_of": "qwen3-coder-next-q4km", "display_name": "Lab coder" } }
  ] }
```
`created` = Unix seconds of the approved catalog's `approvedAt` (stable across calls). `display_name`, `roles`, `max_output_tokens`, `reasoning` come from the approved catalog (`client_hints`, [catalog.md](catalog.md) §2). `nodes` lists node ids only, never hosts or IPs.

The example is abbreviated: the real list is sorted (§4.4), has an entry for `qwen3-coder-next-q4km` itself, and an alias entry carries the same fields as its model. The exact output for the fixture lab is `apps/cloud/apps/api/test/fixtures/localai/gateway-models.json`. See the "As built (GW-05, #140)" notes under §4.4.

### 3.5 Upstream call

| Mode (`LOCALAI_GATEWAY_UPSTREAM`) | URL | TLS | Notes |
|---|---|---|---|
| `public` (**default**) | `https://<publicHost><path>` e.g. `https://ai-n3.marin.cr/v1/chat/completions` | public Let's Encrypt cert, verified | Goes through the VPS's own nginx node vhost (hairpin on the VPS). Reuses the vhosts, certs and Tailscale Serve exactly as today |
| `tailnet` | Pattern A: `http://<tailnetIp>:18080<path>`; Pattern B: `https://<tailnetIp>:443<path>` with SNI and `Host` = `<tailnetMachine>.<settings.gateway.tailnetSuffix>` | Pattern B verified against the system CA bundle | Skips nginx. The API container must reach tailnet addresses through the VPS host (test: `docker compose exec api wget -qO- http://<tailnetIp>:18080/health`). `tailnetSuffix` (`<TAILNET>.ts.net`) is a setting, never committed |

Request headers sent upstream: `Authorization: Bearer <node llama key>` (from the credential store, [cloud-app.md](cloud-app.md) §3.8), `Content-Type`, `Accept`, `X-Request-ID`, `User-Agent: localai-gateway/<version>`. **Never** the client's `Authorization`, cookies or other headers. Response headers forwarded to the client: `Content-Type` only.

HTTP client: undici `Agent` per upstream origin, keep-alive 30 s, `connections: 32`, `connect.timeout` 5 s, `headersTimeout` = first-byte timeout, `bodyTimeout` = idle timeout (§4.2).

### 3.6 Data model (`apps/cloud/apps/api/prisma/fragments/localai-gateway.prisma`)

```prisma
enum LocalaiGatewayKeyKind { user service }

model LocalaiGatewayKey {
  id               String                @id @default(uuid()) @db.Uuid
  userId           String                @map("user_id") @db.Uuid          // owner
  name             String                                                  // "OpenCode on ai-n3", "open-webui"
  kind             LocalaiGatewayKeyKind @default(user)
  tokenHash        String                @unique @map("token_hash")         // sha256 hex of the full lai_ token
  tokenPrefix      String                @map("token_prefix")              // "lai_Ab3dEf" (lai_ + 6 chars), display only
  allowedModels    String[]              @map("allowed_models")            // catalog ids or aliases; empty = all enabled models
  rpmLimit         Int                   @map("rpm_limit")                 // requests per rolling minute
  concurrentLimit  Int                   @map("concurrent_limit")          // simultaneous in-flight requests (streams included)
  dailyTokenBudget BigInt?               @map("daily_token_budget")        // null = unlimited (service keys only)
  expiresAt        DateTime?             @map("expires_at") @db.Timestamptz // null only for kind=service
  revokedAt        DateTime?             @map("revoked_at") @db.Timestamptz
  lastUsedAt       DateTime?             @map("last_used_at") @db.Timestamptz
  createdById      String?               @map("created_by_id") @db.Uuid
  createdAt        DateTime              @default(now()) @map("created_at") @db.Timestamptz
  user             User                  @relation("LocalaiGatewayKeyOwner", fields: [userId], references: [id], onDelete: Cascade)
  usage            LocalaiGatewayUsage[]
  @@index([userId])
  @@map("localai_gateway_keys")
}

model LocalaiGatewayUsage {
  id               String   @id @default(uuid()) @db.Uuid
  keyId            String?  @map("key_id") @db.Uuid
  userId           String?  @map("user_id") @db.Uuid
  requestId        String   @map("request_id")
  endpoint         String                                   // chat.completions | completions | embeddings | audio.transcriptions | models
  model            String?                                  // resolved catalog id
  requestedModel   String?  @map("requested_model")         // as sent (alias)
  nodeId           String?  @map("node_id")                 // "ai-n3"
  status           Int                                      // HTTP status sent (200 even if a mid-stream error happened)
  errorCode        String?  @map("error_code")              // LAI-GW-0NN
  stream           Boolean
  aborted          Boolean  @default(false)                 // client went away
  attempts         Int      @default(1)
  promptTokens     Int?     @map("prompt_tokens")
  completionTokens Int?     @map("completion_tokens")
  usageSource      String   @map("usage_source")            // upstream | estimated | none
  ttfbMs           Int?     @map("ttfb_ms")
  durationMs       Int      @map("duration_ms")
  createdAt        DateTime @default(now()) @map("created_at") @db.Timestamptz
  key              LocalaiGatewayKey? @relation(fields: [keyId], references: [id], onDelete: SetNull)
  @@index([keyId, createdAt])
  @@index([createdAt])
  @@map("localai_gateway_usage")
}

model LocalaiGatewayUsageDaily {
  keyId            String   @map("key_id") @db.Uuid
  day              DateTime @db.Date                         // UTC day
  requests         Int      @default(0)
  promptTokens     BigInt   @default(0) @map("prompt_tokens")
  completionTokens BigInt   @default(0) @map("completion_tokens")
  @@id([keyId, day])
  @@map("localai_gateway_usage_daily")
}

model LocalaiModelPolicy {
  modelId          String   @id @map("model_id")             // catalog id
  enabled          Boolean  @default(true)
  aliases          String[]                                  // e.g. ["lab-coder"]; globally unique (checked in service + test)
  onDemandLoad     Boolean  @default(false) @map("on_demand_load")
  loadWaitS        Int      @default(120) @map("load_wait_s")          // 10..600
  firstByteTimeoutS Int?    @map("first_byte_timeout_s")               // null = settings default
  preferredNodes   String[] @map("preferred_nodes")                    // node ids in order of preference
  updatedById      String?  @map("updated_by_id") @db.Uuid
  updatedAt        DateTime @updatedAt @map("updated_at") @db.Timestamptz
  @@map("localai_model_policies")
}

extend model User { localaiGatewayKeys LocalaiGatewayKey[] @relation("LocalaiGatewayKeyOwner") }
```
`LocalaiGatewayKey` is registered as user-owned data (purge on user deletion; export lists name, prefix, dates, never the hash). Models without a policy row behave as `enabled: true, onDemandLoad: false`.

Audit actions: `localai.gateway.key_created`, `.key_revoked`, `.key_updated` (meta: prefix, name, limits, allowedModels), `localai.gateway.policy_updated`.

**As built (GW-01, #136).** Fragment `apps/cloud/apps/api/prisma/fragments/localai-gateway.prisma` (the listing above, verbatim, with the three rules the SQL cannot enforce written in its header comment); migration `20261104215031_add_localai_gateway`; no raw-SQL index. Where the code lives and what to know:

- **User-owned registration.** `src/localai/gateway/gateway.user-data.ts` declares `LocalaiGatewayKey` (`ownerField: 'userId'`, purge `delete`, export `include`) and `localai.ownership.ts` spreads it into the app's registry. `userId` has `onDelete: Cascade`, and the user-data manifest files the model under the `credentials` category. `createdById` is a plain column with no foreign key (it records who issued a service key), so it is not an actor field.
- **Export.** The platform's `user-data` export lists exactly `id`, `name`, `kind`, `tokenPrefix`, `allowedModels`, `rpmLimit`, `concurrentLimit`, `dailyTokenBudget`, `expiresAt`, `revokedAt`, `lastUsedAt`, `createdAt` (`GATEWAY_KEY_EXPORT_FIELDS`). `exportOmit` is `tokenHash`, `userId`, `createdById`; `tokenHash` is also on the platform's always-redacted list, so it can never reach a file. `toGatewayKeyExport` is the same shape as a pure function, typed without `tokenHash`; a unit test fails when a column is added to the table without a decision about the export.
- **Ownership.** `LocalaiGatewayKey` is kind `user`; `LocalaiGatewayUsage`, `LocalaiGatewayUsageDaily` and `LocalaiModelPolicy` are `system` (deployment-wide, no `org_id`, no row-level security). The usage tables are not user-owned in the registry's sense because `localai_gateway_usage.user_id` is a plain column: after a user purge their usage rows stay, with `key_id` NULL and the old `user_id` kept as an opaque id (metadata only, no prompt text), and the daily rollup is untouched.
- **Audit constants.** `src/localai/gateway/gateway.audit.ts` exports `AUDIT_GATEWAY_KEY_CREATED`, `AUDIT_GATEWAY_KEY_REVOKED`, `AUDIT_GATEWAY_KEY_UPDATED`, `AUDIT_GATEWAY_POLICY_UPDATED` (the permanent strings above), the list `GATEWAY_AUDIT_ACTIONS` and the target types `localai_gateway_key` and `localai_model_policy`. The audit page's filter (`audit/audit-actions.ts`) reads them, so the strings exist once.
- **Tests.** Unit: `gateway.audit.spec.ts`, `gateway.user-data.spec.ts`, `test/localai/ownership.spec.ts`; `runPlatformConformance` (`test/conformance.spec.ts`) covers the registry. Real SQL: `test/localai/gateway-model.db.spec.ts` (duplicate token hash, duplicate (key, day), the policy defaults, a user purge through the platform's `UserPurgeRunner` and a plain user delete, and the export through the platform's own `user-data` source).

### 3.7 Configuration

Environment (only what must exist before the UI): `LOCALAI_GATEWAY_ENABLED` (`true`), `LOCALAI_GATEWAY_UPSTREAM` (`public` | `tailnet`, default `public`).

Settings namespace `localai` (admin UI). The `gateway.*` keys below and the model policies (`PUT /api/localai/gateway/models/:modelId`, §3.8) are writable with **`gateway:admin`**: `PUT /api/localai/settings` checks the permission per key prefix ([cloud-app.md](cloud-app.md) §3.9), so a gateway admin does not need `lab:admin`, and `lab:admin` alone cannot change `gateway.*`:

| Key | Default | Meaning |
|---|---|---|
| `gateway.defaults.user` | `{ rpm: 60, concurrent: 4, dailyTokens: 2000000, expiresInDays: 90 }` | New user key |
| `gateway.defaults.service` | `{ rpm: 600, concurrent: 16, dailyTokens: null, expiresInDays: null }` | New service key |
| `gateway.maxUserKeyDays` | 365 | Longest expiry a user may choose |
| `gateway.timeouts` | `{ connectMs: 5000, firstByteMs: 180000, idleMs: 120000, totalMs: 1800000, nonStreamTotalMs: 900000 }` | §4.2 |
| `gateway.maxAttempts` | 3 | Distinct nodes tried before the first byte |
| `gateway.queueFactor` | 4 | Saturation = inflight ≥ factor × slots |
| `gateway.corsOrigins` | `[]` | Browser origins allowed (§6) |
| `gateway.usageRetentionDays` | 90 | Per-request rows; daily rollups kept forever |
| `gateway.tailnetSuffix` | null | `<TAILNET>.ts.net`, only for `tailnet` mode |

### 3.8 Key management API and UI

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/api/localai/gateway/keys` | gateway:use (own); gateway:admin `?all=1` | List (prefix, name, scopes, limits, expiry, last used) |
| POST | `/api/localai/gateway/keys` | gateway:use; `kind: service`, `ownerUserId`, limits above defaults → gateway:admin | `{ name, allowedModels?, expiresInDays?, kind?, ownerUserId?, limits? }` → `201 { data: { key: { … }, token: "lai_…" } }`, token shown once |
| PATCH | `/api/localai/gateway/keys/:id` | owner (name, allowedModels narrowing); gateway:admin (limits, widen) | |
| DELETE | `/api/localai/gateway/keys/:id` | owner or gateway:admin | Revoke; effective on the next request (no cache of positive auth longer than 10 s, and revocation evicts the cache entry) |
| GET | `/api/localai/gateway/models` | gateway:use | Models with policy + live state (same source as `/v1/models`, plus unlisted disabled ones for admins) |
| PUT | `/api/localai/gateway/models/:modelId` | gateway:admin | Policy upsert |
| GET | `/api/localai/gateway/usage?keyId=&from=&to=&groupBy=day\|model\|node` | own; gateway:admin all | Aggregates from the daily table (+ per-request rows for ≤ 7-day ranges) |

Web pages (`/gateway/keys`, `/gateway/usage`, `/gateway/models`):
```
┌ API keys ───────────────────────────────────────────────────────── [Create key] ┐
│ Name               Key           Models     Limits            Expires   Last used │
│ OpenCode laptop    lai_Ab3dEf…   all        60 rpm · 4 · 2M/d 2027-01-31 2 min ago│ [Revoke]
│ open-webui (svc)   lai_Q9xk2L…   all        600 rpm · 16 · ∞  never     now      │ [Revoke]
└──────────────────────────────────────────────────────────────────────────────────┘
 Create → dialog: name, models (multi-select, empty = all), expiry → shows the key ONCE with
 [Copy] and ready snippets (curl, OpenCode, Open WebUI) and "Store it now; it can't be shown again."
┌ Usage ─ last 30 days ─ group by [day ▾] ─────────────────────────────────────────┐
│ 2026-11-02  ███████████ 412 req · 1.2M tokens   (ai-n1 61 % · ai-n2 30 % · ai-n3 9 %) │
└──────────────────────────────────────────────────────────────────────────────────┘
┌ Models (admin) ───────────────────────────────────────────────────────────────────┐
│ qwen3-coder-next-q4km  ● loaded ai-n1   enabled ✔  aliases [lab-coder]  on-demand ✖  │
│ qwen3.6-35b-a3b-q4km   ○ loadable ai-n2 enabled ✔  on-demand ✔ wait 120 s  prefer [ai-n2] │
└──────────────────────────────────────────────────────────────────────────────────┘
```

**As built (GW-02, #137).** Code: API `apps/cloud/apps/api/src/localai/gateway/` (`gateway.token.ts`, `key-auth-cache.ts`) and `gateway/keys/` (`key.service.ts`, `key-rules.ts`, `keys.controller.ts`, `keys.schemas.ts`, `key.store.ts` with `prisma-key.store.ts`, `known-models.ts`, `keys.module.ts`); settings `localai.settings.ts`, `settings.controller.ts`, `settings.service.ts`; web `apps/cloud/apps/web/src/localai/gateway/` (`KeysPage.tsx`, `CreateKeyDialog.tsx`, `KeySnippets.tsx`, `keySnippets.ts`). Where this differs from the table above, this wins.

- **Token helper (shared with GW-03).** `gateway.token.ts` is the one place a token is made and hashed (`token-verify.ts` of GW-03 imports its hash and pattern, so minting and verifying cannot drift): `mintGatewayToken()` returns `{ token, tokenHash, tokenPrefix }`; `hashGatewayToken(token)` is sha256 hex of the whole token including `lai_`; `isWellFormedGatewayToken()` and `GATEWAY_TOKEN_PATTERN` (`^lai_[A-Za-z0-9_-]{43}$`) are what `/v1` authentication checks before it asks the database. The prefix is `lai_` plus the first 6 characters (10 characters).
- **Routes.** All four need `gateway:use`; a viewer is refused by the guard before anything is read. Responses are `{ data: { keys } }` (list), `{ data: { key, token } }` (create, 201), `{ data: key }` (patch, revoke). A key is a `KeyDto`: `id, name, kind, tokenPrefix, allowedModels, limits { rpm, concurrent, dailyTokens }, expiresAt, revokedAt, lastUsedAt, createdAt, status (active | expired | revoked), ownerUserId`, and `owner { email, displayName }` for a gateway administrator only. There is no `tokenHash` in any response.
- **List.** Your own non-revoked keys, newest first. `?all=1` (gateway:admin, else 403) lists every owner's keys; `?includeRevoked=1` adds revoked ones.
- **Create rules** (`key-rules.ts`, one table in `key-rules.spec.ts`). `kind: service`, an `ownerUserId` other than yourself, or any limit above `gateway.defaults.user` need `gateway:admin` (403). A user key can never be unlimited or immortal, whoever creates it: `limits.dailyTokens: null` and `expiresInDays: null` are 400, and `expiresInDays` is 1 to `gateway.maxUserKeyDays` for every user key (the default is `gateway.defaults.user.expiresInDays`, cut to the maximum). A service key takes `gateway.defaults.service` and may have `null` for both. `allowedModels` entries must be an id of the approved catalog or an id or alias of a model policy at write time (400 naming the unknown ones); duplicates are dropped. An owner that does not exist is 400.
- **Update rules.** The owner may rename and narrow the models (the new set is a subset of the old; an empty old set means all, so any list narrows it; narrowing to an entry the key already had needs no catalog lookup). Any `limits` field, even a lower one, and any widening need `gateway:admin` (403). `dailyTokens: null` on a user key is 400. A revoked key cannot be changed (409).
- **Visibility.** A key that is not yours does not exist for you unless you are a gateway administrator: PATCH and DELETE answer 404, not 403.
- **Revoke.** Sets `revokedAt` and keeps the row. A second DELETE is 200 with the same key, writes no second audit row and keeps the first `revokedAt`. Update and revoke evict the key from the `/v1` authenticator's 10 s cache through `KeyAuthCache.evict(tokenHash)` (`key-auth-cache.ts`, token `GATEWAY_KEY_AUTH_CACHE`, bound in `KeysModule` to `GatewayAuthenticator.evict` of GW-03); `gateway-keys.db.spec.ts` proves a cached key is refused on the very next request after DELETE. A failing eviction is logged and never fails the change (the entry lives 10 s at most).
- **Audit.** `localai.gateway.key_created` (meta `prefix, name, kind, ownerUserId, rpm, concurrent, dailyTokens, expiresAt, allowedModels`), `key_updated` (`prefix, name, changed, rpm, concurrent, dailyTokens, allowedModels`), `key_revoked` (`prefix, name, kind, ownerUserId`). Written after the change, scalar meta only, never the token or the hash; a failed audit write is logged and does not undo the change.
- **No body logging.** The platform's logging interceptor writes one `METHOD url - Nms` line and this app's Fastify instance has request logging off, so no route logs a body; `logLevel: 'warn'` is therefore not set per route. `gateway-keys-routes.spec.ts` captures the logger during a create and asserts the token is absent.
- **Web.** `/gateway/keys` (gate `gateway:use`) is the first page of a new **API keys** navigation destination that owns `/gateway`, so the Usage and Models pages join it. Five destinations overflow the bottom bar by one: the shell keeps the first three (Home, Lab, API keys) and puts Settings and Administration behind "More" (the registries test now allows five). The table has Name, Key (`lai_Ab3dEf…`), Models, Limits (`60 rpm · 4 · 2M/d`), Expires and Last used, and **Revoke** behind a confirmation. **Create key** takes a name, models (free text with suggestions from the models loaded on nodes; empty = all) and an expiry (1 to `maxUserKeyDays`, default from the settings); a gateway administrator may also choose **Service key**, which can have no expiry. Then the show-once panel: the key in a box, **Copy**, "Store it now; it can't be shown again." and three snippets (curl, OpenCode, Open WebUI). The token is held only in the dialog's state: the page mounts the dialog while it is open, Escape and the backdrop do not close it while the key is shown, only **I have stored it** does, and nothing is written to `localStorage` or `sessionStorage`. No snippet contains the key (`$LAI_KEY`, `{file:~/.config/opencode/localai-gateway-key}`, "the key shown above"). The URL in the snippets is the page's own origin plus `/v1`. Limits are not editable in the page (the API supports it for `gateway:admin`; a later item adds the control).
- **Tests.** Unit: `gateway.token.spec.ts`, `key-rules.spec.ts` (the permission matrix), `key.service.spec.ts` (over an in-memory store), `known-models.spec.ts`, `localai.settings.spec.ts`, `settings.schemas.spec.ts`. HTTP over a mocked database: `test/localai/gateway-keys-routes.spec.ts`. Real SQL and real sessions: `test/localai/gateway-keys.db.spec.ts`. Web: `KeysPage.test.tsx` (including axe), `keySnippets.test.ts`, `format.test.ts`, `navigation.test.ts`.

**As built (GW-02, #137): the settings of section 3.7 and their route.** The `localai` namespace is registered by `apps/cloud/apps/api/src/localai/localai.settings.ts` (imported first by `platform/platform.ts`, before `SettingsModule.forRoot()`), with every key of this section and of [cloud-app.md](cloud-app.md) §3.10. It is stored nested (`system_settings.global.value.localai.gateway.defaults.user`); a key is read from the nested value, else the dotted key, else the default, and a value that fails its schema reads as the default. `GET /api/localai/settings` (lab:read) returns `{ settings: { "<dotted key>": value } }`, without `gateway.tailnetSuffix` unless the caller holds `lab:admin` or `gateway:admin`. `PUT /api/localai/settings` takes a flat map of dotted keys (an unknown key is 400), needs `lab:admin` or `gateway:admin` to be called at all, then checks the permission per key: `gateway.*` needs `gateway:admin`, every other key `lab:admin`, a mixed body needs both (403 naming what is missing); the values are then validated with the key's schema (400 naming the key). The write goes through `SystemSettingsService.patchSettings`, so it is versioned and audited as `system_settings:patch`. The platform's own `PUT`/`PATCH /api/system-settings` refuse the `localai` namespace (400), so `system_settings:write` is not a second door past the per-prefix rule; `GET /api/system-settings` still shows it.

## 4. Behaviour

### 4.1 Request pipeline

```
request ─► request id ─► maintenance/enabled? ─► CORS (OPTIONS ends here)
        ─► authenticate (lai_ → sha256 → key row, cache 10 s) ─► owner active + gateway:use?
        ─► body limit + parse ─► resolve model (alias → id; policy enabled?) ─► scope check
        ─► rate limits: RPM window, concurrency semaphore (held until the response ends), daily budget
        ─► select candidate (§4.3) ─► attempt loop (§4.2) ─► passthrough ─► usage record (async)
```
- Key parse: `^lai_[A-Za-z0-9_-]{43}$`; anything else → 401 without a DB query. Lookup by `tokenHash` (unique index). Refuse if `revokedAt`, `expiresAt <= now`, owner `isActive = false`, or owner lacks `gateway:use` (permissions computed by EAB's `PrincipalFactory` for the owner, cached 60 s).
- Failed-auth limiter: per client IP (`X-Real-IP` from the inner nginx), 30 failures/min → 429 LAI-GW-011 for 5 min.
- `lastUsedAt` is updated fire-and-forget, at most once per minute per key.
- Limits are **in-memory per API replica** (one replica on the VPS). RPM: sliding window of the last 60 s of request timestamps per key. Concurrency: counter per key, released in `finally` when the response ends or aborts. Daily budget: `used = daily row (prompt+completion) + in-memory not-yet-flushed`; refuse when `used >= budget`. A request that starts under budget is allowed to finish (it may overshoot).

**As built (GW-03, #138).** Code in `apps/cloud/apps/api/src/localai/gateway/`: `routes.ts` (`registerGatewayRoutes(app)`, called from `createApp()` in `src/main.ts`, and `registerGatewayRoutesOn(instance, deps)` for specs), `errors.ts` (the one builder, every §3.3 row), `auth.ts` + `auth-cache.ts` + `token-verify.ts` + `auth.prisma.ts`, `failed-auth-limiter.ts`, `request-id.ts`, `cors.ts`, `body.ts`, `index-route.ts`, `request-log.ts`, `gateway-settings.ts`, `gateway-core.module.ts`. What differs from, or sharpens, the text above:
- **The pipeline is one `onRequest` hook of an encapsulated Fastify scope mounted at `/api/gateway/v1`**, so it runs before Fastify reads a body: an anonymous client never makes the gateway buffer 8 MiB (it gets 401, not 413). A route is authenticated unless declared `public` (only `GET /v1/`); `OPTIONS` is answered by the hook itself. The catch-all (`endpoint_not_supported`, LAI-GW-024) authenticates first like every route, so an anonymous probe learns nothing. `gateway-routes.spec.ts` enumerates the registered routes, proves each refuses a keyless request, proves `authenticate()` runs before the handler, and fails when a route is added that answers without a key.
- **Maintenance**: the platform's `MaintenanceModeService` has `isEnabled()` (whether traffic is held back at all, whoever asks), not `isActive()`; the gateway uses it. A failing lookup is logged and treated as "not in maintenance", so a database blip cannot turn the gateway off by itself.
- **CORS headers are applied right after the request id**, so an allowed origin also sees them on the 503/401/429 answers (a browser needs them to read the error); the preflight still ends after the enabled/maintenance check, as in the pipeline above. A preflight for an unlisted origin is a bare `204` with no `Access-Control-*` header. The `gateway.corsOrigins` schema (`GatewayCorsOriginsSchema`, exact `http(s)://host[:port]` origins, `*` refused) is exported from `cors.ts` for the settings issue to register; until the `localai` namespace exists the value is read from the stored system settings row (nested or dotted form), cached 10 s, and an invalid value means "no origins".
- **Retry-After**: the table's values, plus 30 s for LAI-GW-050 and LAI-GW-051 (the spec gave none for these 503s). LAI-GW-011 carries the seconds left of the block.
- **Inference endpoints are stubs** (503 `gateway_unavailable`, message "This gateway endpoint is not available yet.") behind a `GatewayHandlers` object that GW-05, GW-06 and GW-10 replace (all three have); the inference routes already parse the body (`parseInferenceBody()`: LAI-GW-001, LAI-GW-002) before the handler. `stream: true` on embeddings (LAI-GW-003) is left to the proxy item.
- **Body errors never quote the body**: Fastify's parse errors are mapped by their code only, because Node's `JSON.parse` message can contain a slice of the input. Fastify's `logLevel: 'warn'` and header-free serializers are set on every gateway route; the per-request line is built from the fixed field list of §7 (`request-log.ts`), a 5xx as a warning.
- **The failed-auth limiter blocks the whole address**, a valid key included, as specified. The address is the inner nginx's `X-Real-IP` (`$remote_addr`), which on the VPS is the shared host proxy's address unless the inner nginx is given the real client address (`real_ip_header`); until then one noisy client can lock every client out for 5 minutes. Verify this at deploy (#92) and, if needed, add a `set_real_ip_from` include under `infra/nginx/app.d/server/`.
- **The index also answers** on `/api/gateway/v1` without the trailing slash (Fastify's prefix rule); the public `/v1` is redirected by nginx with a relative `Location: /v1/` (`absolute_redirect off`, so no wrong scheme behind the TLS edge).
- **nginx 413**: a body over `client_max_body_size 26m` is answered by `gateway.conf` itself in the gateway error shape (LAI-GW-004, the nginx `$request_id` as `request_id`), not nginx's HTML page.
- The key lookup is by the unique `token_hash` and the stored hash is compared again in constant time; positive results are cached 10 s by `tokenHash` (`GatewayAuthenticator.evict(tokenHash)` is the hook the revoke path calls), the owner's effective permissions (`PrincipalFactory`) 60 s by user id (`evictOwner(userId)`).

### 4.2 Attempts, streaming and timeouts

```
attempt(i) on candidate c:
  connect ── fail/timeout(connectMs) ───────────────► retryable
  send body
  wait headers ── timeout(firstByteMs) ─────────────► retryable (no bytes sent yet)
  status 5xx / 502/503/504 ─────────────────────────► retryable (drain ≤ 64 KiB, discard)
  status 401 ───────────────────────────────────────► retryable, mark c.keyInvalid, alert LAI-GW-043
  status 404/400 with "model not found|not loaded|unloaded" ──► retryable, mark (c.node, model) unloaded until next snapshot
  status other 4xx ─────────────────────────────────► final: pass error through (LAI-GW-042)
  status 200 ───────────────────────────────────────► FIRST BYTE: commit, no more retries
retry: next candidate not yet tried, while i < maxAttempts; mark c suspect for 30 s
```
- **Body:** the gateway parses the JSON (it needs `model` and `stream`), then re-serialises it with two changes only: `model` replaced by the resolved catalog id; for `stream: true` on chat/completions, `stream_options.include_usage = true` is set (llama-server honours it). The client's own `stream_options` are kept otherwise.
- **Non-stream:** read the upstream body fully (cap 32 MiB; over → 502 LAI-GW-041), parse `usage`, send with the upstream status. Total timeout `nonStreamTotalMs`.
- **Stream (SSE passthrough):**
  1. On upstream 200: `reply.hijack()`; `reply.raw.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache', 'x-accel-buffering': 'no', 'x-request-id': id, 'x-localai-node': node, … })`; `reply.raw.flushHeaders()`.
  2. For each upstream chunk: feed an SSE event splitter (splits on blank lines `\n\n` / `\r\n\r\n`). Each **complete** event is written immediately with `reply.raw.write()`; a partial event waits only for its own remaining bytes. Nothing else is held back. If `write()` returns false, `await once(reply.raw, 'drain')` (backpressure propagates to undici, which pauses the upstream socket).
  3. Each event is inspected without copying the stream: if it is the usage-only chunk (`"choices":[]` with `usage`), record `usage`; forward it only if the client asked for `include_usage`, otherwise drop it. `data: [DONE]` is forwarded and ends the loop.
  4. `bodyTimeout` (`idleMs`) between chunks and a `totalMs` timer: on expiry, abort upstream, write the §3.3 mid-stream error event and `[DONE]`, end.
- **Client abort → upstream abort:** one `AbortController` per request. `reply.raw.on('close', () => { if (!reply.raw.writableFinished) controller.abort() })`. The abort cancels the undici request and closes the upstream socket; llama-server notices the closed connection and stops generating (frees the slot). The usage row gets `aborted: true`. Same for non-stream requests.
- **Usage when missing** (abort, mid-stream error, old node): `completionTokens` = number of content-bearing delta events seen (llama emits about one token per event), `promptTokens` = null, `usageSource: "estimated"`. Embeddings use `usage.prompt_tokens`. Transcriptions record `usageSource: "none"` and count 0 tokens.
- **Token accounting:** budget uses `promptTokens + completionTokens` from `usage` (OpenAI fields `prompt_tokens`, `completion_tokens`; `total_tokens` is ignored if inconsistent).

**As built (GW-06, #141).** Code in `apps/cloud/apps/api/src/localai/gateway/`: `gateway.service.ts` (`GatewayService.handle(endpoint, ctx)`, the non-stream path), `attempts.ts` (`AttemptLoop`, `TableCandidateSource`, the hook and port interfaces), `proxy-request.ts` (`ProxyRequest`: the abort controller, the total timer, the one `settle()`), `body-rewrite.ts`, `upstream/client.ts` (`UpstreamClient`), `upstream/errors.ts` (classifier, aggregation, 4xx normalisation), `usage/usage-sink.ts`, `limits.ts`, `model-resolver.ts`, `stream-handler.ts`, `proxy-settings.ts`, `handlers.ts`, `gateway-proxy.module.ts`; the fake node is `test/fakes/fake-llama.ts` and the integration spec `test/localai/gateway.e2e.spec.ts` (real HTTP servers). Where the build sharpens or differs from the text above:
- **Seams for the items that build on this one.** Each is a Nest provider token with a default in `GatewayProxyModule`; the later item rebinds ONE token: `GATEWAY_MODEL_RESOLVER` (GW-05), `GATEWAY_LIMITS` (GW-08; `admit()` runs after the scope check and before selection, throws LAI-GW-030/031/032, returns a ticket that is settled once when the response ends and may add `x-ratelimit-*` headers), `GATEWAY_USAGE_SINK` (GW-08; one `UsageRecord` per request, the columns of `localai_gateway_usage` and nothing else), `GATEWAY_STREAM_HANDLER` (GW-07; receives the prepared `ProxyRequest` and the `AttemptLoop`; GW-06 shipped it answering `stream: true` on chat/completions and completions with 400 LAI-GW-003, GW-07 binds `StreamProxy`; `stream: true` on embeddings stays 400 LAI-GW-003), `GATEWAY_CANDIDATE_SOURCE` (GW-09; the on-demand load wraps `TableCandidateSource`, which answers step 4 only on the first attempt), `GATEWAY_ATTEMPT_HOOKS` (`beforeAttempt`, `onAttemptFailed`, `onFirstByte`; observers, a throwing hook never fails a request) and `UPSTREAM_TRANSPORT` (GW-10; chosen by `LOCALAI_GATEWAY_UPSTREAM`, see the GW-10 notes below).
- **Model resolution is a port.** `GatewayModelResolver.resolve(requested, key)` (`model-resolver.ts`) returns the catalog id, the model policy (`preferredNodes`, `onDemandLoad`, `loadWaitS`, `firstByteTimeoutS`), the catalog roles and whether the model is in the approved catalog, or throws. `CatalogModelResolver` (`model-resolver.catalog.ts`) binds it to GW-05's `ModelResolver.resolve` (alias to catalog id, approved catalog, policy enabled: unresolved is 404 LAI-GW-020) followed by `assertKeyAllows` (the section 4.4 scope rule; outside the scope is **403 LAI-GW-012** for an inference request, while `GET /v1/models/{id}` answers 404 for the same case). An embeddings request for a model whose catalog roles do not include `embedding` is 404 LAI-GW-020; a catalog entry with no roles is not refused.
- **Timeouts are per undici agent.** undici ignores `headersTimeout` and `bodyTimeout` on a single request, so `UpstreamClient` keeps one `Agent` per origin AND timeout set (`connectMs`, `firstByteMs` after the model policy's `firstByteTimeoutS` override, `idleMs`), capped at 64. undici's timers fire up to about 500 ms late for values of 1 s and more; the settings floor is 1 s.
- **Marks per failure.** 5xx, connect, TLS and first-byte timeout mark the node suspect for 30 s; 401 marks it `keyInvalid` (no suspicion: it is already out of the selection until `localai.node.key_reported`); "model not loaded" marks only (node, model) unloaded, not the node. A node whose key was removed since the last rebuild counts as a 401 without a call. An unreadable credential store ends the request at once with 503 LAI-GW-050 (every node would fail the same way). Redirects are not followed and count as a 5xx.
- **Error aggregation** (`aggregateFailures`): the category with the most attempts wins (connect or TLS 040, 5xx 041, 401 043, first-byte timeout 044, "model not loaded" on every node 503 LAI-GW-021); a tie goes to 043, then 044, then 041, then 040, then 021. After the first attempt, "no candidate left" reports the failures, never 404 or LAI-GW-023.
- **4xx normalisation.** The upstream status and `message` (at most 1000 characters) are kept; `code` is the upstream code when it is a safe string, else `upstream_rejected` (so llama-server's numeric `400` becomes `upstream_rejected`); `param` is kept when a safe string; a body that is not that shape gives the defaults. At most 64 KiB of an error body is read; a 5xx body is drained and discarded.
- **Client abort and total timeout.** One `AbortController` per request carries a typed reason: `client` (the socket closed before the response finished: upstream aborted, usage `aborted: true`, status 499, no response body) or `total` (`nonStreamTotalMs`, also while the body is being read: 504 LAI-GW-044). A non-stream body that breaks off after the headers is 502 LAI-GW-041 (idle timeout 504 LAI-GW-044): the first 2xx status is the commit, there is no retry after it.
- **Usage of a non-stream answer** is `usage.prompt_tokens` and `usage.completion_tokens` (embeddings: completion 0), `usageSource: "upstream"`; an answer without a usable `usage` is `"none"` with null tokens. Errors are recorded too, with `status` and `errorCode`. A rejected request that never reached a node has `attempts: 1` in the record.
- **Headers.** `x-localai-node` and `x-localai-attempts` are set on answers and on errors from the node loop; only the upstream `Content-Type` is forwarded. The new dependency is `undici` (already in the lockfile through jsdom), now a direct dependency of `apps/api`.

**As built (GW-07, #142).** Code in `apps/cloud/apps/api/src/localai/gateway/stream/`: `stream-proxy.ts` (`StreamProxy`, the `StreamProxyHandler` bound to `GATEWAY_STREAM_HANDLER`), `sse-splitter.ts` (`SseSplitter`), `usage-from-events.ts` (`StreamUsageTracker`, `estimateUsage`); the fake node is `test/fakes/fake-llama-sse.ts` and the specs are `stream/*.spec.ts` (splitter fuzz, tracker), `test/localai/gateway-stream.e2e.spec.ts` (real sockets) and `test/localai/gateway-nginx.e2e.spec.ts` (through nginx). It reuses GW-06's `ProxyRequest`, `AttemptLoop` and body rewrite unchanged; the only edits to GW-06 files are `totalMs` on `ProxyRequest.timeouts` (set by `GatewayService`) and the provider binding. Where the build sharpens or differs from the text above:
- **Same event, two codes.** An idle timeout, the total timeout and a node that breaks or ends without `[DONE]` all send the exact section 3.3 bytes (`localai_code` LAI-GW-045 in the event). The usage record tells them apart: `errorCode` LAI-GW-044 for the two timeouts, LAI-GW-045 for a broken node, a body that ends without `[DONE]` and an event larger than 1 MiB; `status` is 200 and `aborted` false in all of them. The event is followed by `[DONE]` and the end of the response.
- **`totalMs` bounds the whole request, attempts included.** The timer starts before the attempt loop. If it fires before the first byte the answer is the ordinary JSON 504 LAI-GW-044; after it, the error event. A client that leaves before the first byte is 499 with `aborted: true` (as in the non-stream path); one that leaves mid-stream is recorded with `status` 200, `aborted: true`, `errorCode` null.
- **The idle timer is the proxy's own, as well as undici's.** `idleMs` is measured from the response head and restarted on every chunk; it is stopped while the client is the slow side (the proxy has paused the node body and waits for `drain`), so a slow reader is never cut by it. undici's `bodyTimeout` (same value, per agent) stays as the transport's own guard and does not fire while its parser is paused.
- **Backpressure is event-driven, not an `await once(...)`.** The upstream body is consumed in flowing mode; when `write()` returns false the body is paused and resumed on `drain`, which is what stops undici reading the node's socket. Events that were in the same chunk are still written (at most one chunk is ever queued). Proof: the backpressure case of `gateway-stream.e2e.spec.ts` (the node's `write()` returns false and makes no progress while the client reads nothing).
- **Usage.** Any event carrying a `usage` object (`"usage":{`) is parsed once and recorded (`usage: null` on every chunk, as OpenAI-style nodes send it, is ignored); it is dropped only when it is the usage-only chunk (`"choices":[]`) and the client did not set `stream_options.include_usage`. Content-bearing events for the estimate are those with a non-empty `delta.content`, `text` or `delta.reasoning_content` (the last because llama-server streams a thinking model's tokens there; the text above names only the first two). `[DONE]` ends the stream; with no `[DONE]` the stream counts as interrupted, even if the body closed cleanly. A completions stream gets no forced `include_usage`, so its usage is always `estimated` unless the client asks for the chunk itself.
- **Framing.** `SseSplitter` works on bytes and returns each event with its blank line (`\n\n`, `\r\n\r\n` or a mix), as a view of the incoming chunk when the event lies inside it; it keeps a copy of only the unfinished tail, capped at 1 MiB per event. A bare `\r` is not a line end (llama-server never sends one).
- **End of a clean stream.** The client's response is ended at `[DONE]`; the node's body is then read for up to 250 ms more (the chunked terminator) so the keep-alive connection returns to the pool, and is dropped otherwise. The node's `inflight` count and the limits ticket are released before that wait.
- **Headers.** The head is `reply.getHeaders()` (request id, CORS, `x-ratelimit-*`, `x-localai-node`, `x-localai-attempts`) plus `content-type: text/event-stream; charset=utf-8`, `cache-control: no-cache`, `x-accel-buffering: no`. `reply.hijack()` skips Fastify's `onSend` hooks, so no compression applies; the request log line is still written by the `onResponse` hook when the response finishes.
- **Known gap: no request log line when the client aborts** (stream or non-stream 499): Fastify's `onResponse` hook does not run for a response that never finished. The usage record is still produced. Closing it means a `close` fallback in `routes.ts`.
- **Through nginx.** `gateway-nginx.e2e.spec.ts` starts a local `nginx` process with a generated config that includes the app's real `gateway.conf` (only the platform's `security-headers.conf` include is dropped) in front of the gateway scope; it is skipped, with the reason in its name, on a machine without an `nginx` binary, and does not use docker compose. Not covered here: the manual check on the lab (`curl -N`, Ctrl-C, `journalctl -u localai-llama`) and the 200-stream load test (GW-12).
**As built (GW-10, #145).** Code in `apps/cloud/apps/api/src/localai/gateway/`: `transcriptions-route.ts` (the upload reader, the re-encoder, the audio check, the handler), `upstream/targets.ts` (the target builders and the two `TargetResolver`s), `upstream/tailnet-suffix.ts` (the `gateway.tailnetSuffix` reader), `upstream/transport.factory.ts` (the mode switch), and small edits to `attempts.ts`, `upstream/client.ts`, `gateway.service.ts` (`handleUpload`), `proxy-request.ts` and `routes.ts`. Tests: `upstream/targets.spec.ts`, `upstream/tailnet-tls.spec.ts` (a local TLS server; the CA and certificates are made with the `openssl` CLI at run time and the CA is injected into the client for the test), `transcriptions-route.spec.ts`, `attempts.spec.ts` (eligibility) and `test/localai/transcriptions.e2e.spec.ts`.
- **Reading the upload.** `@fastify/multipart` is registered in a child scope of the gateway scope that holds only this route. If the application already registered the plugin for its own uploads (`src/main.ts` does when the storage slice is on), the decorator is inherited and a second registration would throw, so the child skips it; either way `request.parts({ limits })` is called with the endpoint's own limits (file 25 MiB, one file, 10 other fields, 256 KiB per field value), which win over the application's. The file is read into memory (never to disk) because a retry on another node needs the same bytes; the order of the parts does not matter, so a file that arrives before `model` is simply buffered. Fastify's `bodyLimit` for the route is **26 MiB** (25 MiB + 1 MiB of framing and fields, the same headroom as nginx's `client_max_body_size 26m`): with exactly 25 MiB it would refuse a legitimate 25 MiB file, since Fastify checks `Content-Length` before reading. One byte more than 25 MiB of file is refused by the file limit: 413 LAI-GW-004, and so is a request over 26 MiB.
- **Field rules.** Allowed: `file`, `model`, `language`, `prompt`, `response_format`, `temperature`, `timestamp_granularities[]` (the only one that may repeat). Any other field, a file under another name, a second file, a repeated field, more than 10 fields or a value over 256 KiB is 400 LAI-GW-003 (`param` names the field when it is a short plain name). `stream` is among the refused (the transcription answer is never streamed). A missing or blank `model` is 400 LAI-GW-002. A body that is not multipart, is malformed, or has no `file` part is 400 LAI-GW-001 with a message that says so (the code string stays `invalid_json`: the registry has no code for it, and none was invented).
- **Re-encoding.** The upstream body is a new multipart body with a new random boundary (checked against every part): the same fields in the same order with `model` replaced by the resolved catalog id, then the file part with its bytes untouched, its filename made safe for a header line (no quotes, backslashes or control characters, ASCII, at most 200 characters) and its own `Content-Type` (`application/octet-stream` when absent or malformed). Headers upstream are the same five as for JSON, with the new `Content-Type`; `Accept` is `application/json`. The answer is forwarded with the node's `Content-Type` (`response_format=text|srt|vtt` is plain text).
- **Audio models.** After the model is resolved and before the limits admit the request, `audioCapabilityOf(RoutingTable)` answers: some healthy candidate of the model reports `audio` (go on), candidates exist and none does (404 LAI-GW-020, so a bad request does not use a rate-limit slot), or no healthy candidate (go on: the loop answers 503 LAI-GW-021 or 023 as for any model). A candidate that does not report `audio` is skipped by the attempt loop (below), so a model served by a text-only node and an audio node goes to the audio node. A node that reports no modalities is not an audio node.
- **Eligibility in the attempt loop.** `AttemptRequest.eligible(candidate)` and `UpstreamTransport.routable(node)` (both optional) skip a candidate **without it being an attempt**: it is added to `tried` for the source only, so the source offers the next one, `x-localai-attempts` counts real attempts, and nothing is acquired, marked or logged as a failure. If every candidate is skipped on the first attempt the answer is the source's (503 LAI-GW-021, or 404 LAI-GW-020 for an unknown model). `AttemptRequest.contentType` carries the multipart type with its boundary. The call to the transport now also carries `route` (`servePattern`, `tailnetIp`, `tailnetMachine` of the candidate), which only the tailnet mode reads.
- **Usage.** `endpoint: "audio.transcriptions"`, `usageSource: "none"`, `promptTokens: 0`, `completionTokens: 0` on an answer; an error is recorded with its status and code and null tokens, as for every endpoint. Nothing of the upload (bytes, filename, prompt) or of the transcript reaches a log line, a usage row, a header or an error (`transcriptions.e2e.spec.ts` puts a marker in the audio, the prompt and the transcript and searches every output).
- **Tailnet mode.** `LOCALAI_GATEWAY_UPSTREAM=tailnet` selects `TailnetTargets` when the provider is built (restart to change; switching back to `public` needs nothing else, no data depends on the mode). Pattern A: `http://<tailnetIp>:18080<path>`, no `Host` override. Pattern B: `https://<tailnetIp>:443<path>` with SNI and `Host` set to `<tailnetMachine>.<gateway.tailnetSuffix>`; the certificate is verified against the system CA bundle for that name, so a certificate for another name is a TLS failure, classified `tls`, retried on another node and, if every node fails, LAI-GW-040. The undici agent is per origin AND TLS server name AND timeout set. Each piece is validated before it is used (address: an IP literal without a zone id, IPv6 in brackets; machine: one DNS label; suffix: `<tailnet>.ts.net`, the pattern of the setting's schema), and the target is built only from `localai_nodes` and the setting, never from the request. The test hooks `UpstreamClientOptions.targets`/`tlsCa` and `TailnetTargets` `ports` exist for the specs; production passes none. Pattern A needs only the address; Pattern B needs the address, the machine and the suffix. A node that lacks what its pattern needs is **left out of the candidates** (`routable()` is false) and a warning names the node id and a reason word (`no_tailnet_ip`, `invalid_tailnet_ip`, `no_tailnet_machine`, `invalid_tailnet_machine`, `no_tailnet_suffix`) once; if the node is fixed and breaks again it is warned again. (The issue lists "missing `tailnetIp`/`tailnetMachine` or unset `tailnetSuffix`" for every node; Pattern A never uses the machine or the suffix, so excluding an A node for them would only hide a working node.) The address, the machine and the suffix appear in no log line, error or header (an error names the node id only).
- **`gateway.tailnetSuffix`** was registered by GW-02 (null default, `^[a-z0-9-]{1,63}\.ts\.net$`, hidden from readers who are not `lab:admin` or `gateway:admin`); GW-10 reads it through `PrismaTailnetSuffixReader` (the stored `localai` object, nested or dotted, cached 10 s; a missing, malformed or unreadable value is "unset"). Pattern A nodes do not read it.
- **`UpstreamResponse.destroy()`** now ignores the "aborted" error undici emits when an unread body is destroyed (nobody listens to it any more).
- **Not done here:** the API container reaching tailnet addresses through the VPS host is an owner step (the test plan of #145), not code; the health probe stays on the public host (#139).

### 4.3 Routing table and selection

`RoutingTable` (in memory) is rebuilt from the DB on start, on every `localai.node.state` and `localai.node.key_reported` event ([cloud-app.md](cloud-app.md) §4.3), and every 10 s as a safety net:
```
candidates[modelId] = [ { nodeId, publicHost, servePattern, tailnetIp, tailnetMachine, priority,
                          status (loaded|loadable), slots, ctx,
                          inflight (live counter), suspectUntil, ewmaTtfbMs } ]
built from localai_nodes WHERE gatewayRoutable  ⋈  localai_node_models   (gatewayRoutable = routable && keyReportedAt != null, cloud-app.md §4.5)
  status = loaded              if node_models.status = 'loaded'
  status = loadable            if present && verified && loadable && status = 'unloaded'
```
A **probe** runs every 30 s per routable node: `GET https://<publicHost>/health` (no key, 3 s timeout). Three consecutive failures → node suspect until the next success and alert `endpoint.down` (LAI-CLD-027).

**Selection** for model `m`:
1. `loaded = candidates[m] where status=loaded && !suspect && !keyInvalid`.
2. If `loaded` is non-empty, order by: `priority` asc → position in `policy.preferredNodes` (absent = last) → `inflight / slots` asc → `ewmaTtfbMs` asc → `nodeId`. If the best has `inflight >= queueFactor × slots`, try the next; if all are saturated → 503 LAI-GW-023. The attempt loop walks this order.
3. Else, if `policy.onDemandLoad`: choose among `status=loadable` candidates, ordered by `priority` → preference → fewest loaded models with `inflight > 0`. Skip a node if loading `m` would unload a model that currently has `inflight > 0` (never evict a busy model; the agent's `unload_others` would otherwise kill live streams). "Would unload" is computed from the node's last snapshot: `loadedCount` = models with `status = loaded` on that node, `models_max` = `llama.models_max`; the load unloads others when `loadedCount + 1 > models_max`, and then **every** other loaded model is unloaded (`unload_others: true`), so the node is skipped if any of them has `inflight > 0`. When `loadedCount + 1 ≤ models_max` nothing is unloaded and the node is never skipped for this reason. Enqueue `localai.model.load` `{ model_id: m, unload_others: <loadedCount + 1 > models_max> }` with `source: "gateway"` (dedup collapses concurrent requests into one command; [cloud-app.md](cloud-app.md) §4.4). Wait for the routing table to show `m` loaded on that node, up to `policy.loadWaitS` (the client receives nothing meanwhile). Ready → continue at step 1. Timeout → 503 LAI-GW-022 with `Retry-After: 15` (the load keeps going; the next request will likely succeed).
4. Else: if `m` is in the approved catalog and some node has it present → 503 LAI-GW-021; otherwise 404 LAI-GW-020.

`inflight` counts requests from the moment a candidate is chosen until the response ends. `ewmaTtfbMs` uses α = 0.2 over successful first bytes.

**As built (GW-04, #139).** Code: `apps/cloud/apps/api/src/localai/gateway/routing/` (`GatewayRoutingModule`, registered in `localai.module.ts`). Pure parts: `build-candidates.ts` (rows to candidates, using the cloud's `deriveHealth` and `isGatewayRoutable`), `select.ts` (steps 1, 2 and 4). Stateful parts: `routing-table.ts` (`RoutingTable`), `probe.ts` (`EndpointProbe`), `node-key.provider.ts` (`NodeKeyProvider`). Where the build differs from, or fills a gap in, the text above:
- **Result shape.** `RoutingTable.select(modelId, policy, tried)` returns `{ kind: 'candidates', candidates }` (loaded, healthy, untried, **unsaturated** candidates in order; each carries its live `inflight`, `suspectUntil` and `ewmaTtfbMs`), `{ kind: 'saturated' }` (503 LAI-GW-023) or `{ kind: 'none_loaded' }` (the caller goes on to the on-demand path, then to `RoutingTable.unavailable(modelId, inApprovedCatalog)`, which answers step 4: 503 LAI-GW-021 when the model is in the approved catalog and some node that is not retired has it `present` (routable or not), else 404 LAI-GW-020). The caller supplies `inApprovedCatalog`; the table does not read the catalog.
- **Ordering.** A candidate with no measured first byte sorts before a measured one at equal load (otherwise a node that never answered would never be tried and so never measured). Suspect, `keyInvalid` and already-tried nodes are not eligible; if the loaded set is non-empty but all of it is saturated the answer is `saturated`, not `none_loaded`.
- **Live state.** Kept outside the rebuilt candidates, so a rebuild cannot reset it: per node `suspectUntil` (a failed attempt, 30 s, a longer existing suspicion is kept), `probeSuspect` (the probe, until its next success) and `keyInvalid` (until `localai.node.key_reported`); per (node, model) `inflight` and `ewmaTtfbMs`; per node the `unloaded` marks (until that node's next `localai.node.state`). A marked model counts as `loadable` when it is present, verified and loadable, and is left out otherwise. State of a node that no longer exists is dropped at a rebuild unless a request is still in flight on it.
- **Rebuilds** are coalesced (one at a time, any number of triggers during one schedule exactly one more). The first one runs at application bootstrap; the 10 s safety rebuild and the 30 s probe run only when `LOCALAI_GATEWAY_ENABLED` is not `false` and `NODE_ENV` is not `test`. A failed read keeps the previous table. `gateway.queueFactor` is read on each rebuild from the stored `localai` settings (nested or dotted key, 1 to 64, default 4).
- **On-demand primitives for GW-09.** `loadableCandidates(modelId, policy)` (healthy, key-valid nodes only; ordered by priority, preference, fewest loaded models with `inflight > 0`, node id; each carries the node's loaded models with their `inflight` for the never-evict-a-busy-model rule; `models_max` is not in the table, the caller reads it from the node's snapshot) and `waitForLoaded(modelId, nodeId, timeoutMs, signal?)` (resolves `false` on timeout, abort or shutdown).
- **Probe.** One run at a time, so a slow tick never stacks; it never delays a request, which reads the suspect flag from memory. Reasons are a fixed set (`timeout`, `connect`, `status_<code>`); a redirect counts as a failure. `endpoint.down` is raised once per outage through `EndpointAlertsPort` (bound to `AlertsService.raiseExternal` / `clearExternal`) and cleared by the next success; the first success of a node after start also looks for an alert a previous process left. A node that leaves the table (offline or survival mode, retired, key removed) is forgotten: its probe suspicion is lifted and its `endpoint.down` is cleared, because silence is then expected and the node's own alerts say what is wrong.
- **Node key.** `NodeKeyProvider.get(nodeId)` delegates to `NodeKeyService.getKey` (credential `localai_node_llama_key`, cached at most 60 s, dropped on `localai.node.key_reported`); an unreadable store or a decrypt failure throws `GatewayNodeKeyError` (`code: 'LAI-GW-050'`, `status: 503`) without the cause. A missing key is `null`.
- **Lab header.** `RoutingTable.routableNodeCount()` is the number of nodes the table admits (`gatewayRoutable`); #85 can consume it. `keyInvalidNodes()` serves the `localai.gateway.keys` doctor check.

### 4.4 `/v1/models`

The union, over enabled models (policy `enabled` and present in the approved catalog), of: models with ≥ 1 loaded routable candidate (`state: loaded`), plus models with on-demand load on and ≥ 1 loadable routable candidate (`state: loadable`). Each alias is a separate entry with `alias_of`. Filtered by the key's `allowedModels`, with this rule (also applied when authorising a request, §4.1): an entry that is a **catalog id** allows that id **and all its aliases**; an entry that is an **alias** allows only that alias (the underlying id and its other aliases stay hidden: they are not listed, and `GET /v1/models/{id}` for them is `404 model_not_found`, LAI-GW-020; an inference request that names them is outside the key's scope, `403 model_not_allowed`, LAI-GW-012, as §3.3 says). Sorted: loaded first, then by id. Every listed id is requestable with that key at that moment, which is why loadable models are listed only when on-demand load is on.

**As built (GW-05, #140).** Code in `apps/cloud/apps/api/src/localai/gateway/`: `models-route.ts` (the two handlers, wired into `GatewayHandlers` by `registerGatewayRoutes`), `models/listing.ts` (`ModelListing.build/list/find`, and the pure `buildModelList`), `models/resolve.ts` (`resolveModel`, `ModelResolver.resolve`, `keyAllows`, `assertKeyAllows`, `scopeAllows`, `ModelDirectoryProvider`), `models/catalog-hints.ts` (`parseModelsYaml`, `ApprovedCatalogReader`), `models/policy.ts` (the read side of `localai_model_policies`), `models/models.module.ts` (`GatewayModelsModule`). Where the build differs from, or fills a gap in, the text above:
- **Reading the approved catalog.** `models.yaml` is parsed with the `yaml` package once per approved version (a new `apps/api` dependency). A cheap head query (version and `approvedAt`, no files) runs at most every 5 s (`CATALOG_HEAD_TTL_MS`); the file text is read only when the approved version changes. A failing read serves the last good catalog (warning with the error name only); with none cached the request is a 500 LAI-GW-052. The stored policies are cached the same way (`POLICY_TTL_MS`, 5 s); `ModelDirectoryProvider.invalidate()` drops both caches and is the hook GW-09's policy writes and an approval should call. A catalog whose `models.yaml` cannot be read gives an empty model set (nothing listed, nothing resolves) and a warning.
- **`created`** is `floor(approvedAt / 1000)` of the approved version, so it is stable across calls and changes only when a different version is approved.
- **Fields.** `display_name`, `roles`, `max_output_tokens` and `reasoning` come from the catalog (`max_output_tokens` is `null` when the catalog gives none or `0`, which an embedding model carries; `reasoning` is `false` when absent). `context_length` is the largest `ctx` the nodes in the entry report, else the catalog's `llama.ctx`, else `null`. `input_modalities` is not in the catalog: it is the union of what the serving nodes report, in the order `text`, `image`, `audio`, `video`, and `["text"]` when none reported any (`inputModalities` is now part of the routing `Candidate`).
- **`nodes`** are the healthy nodes of the state shown: the loaded ones for `loaded`, the loadable ones for `loadable`. A model loaded on one node and only loadable on another is `loaded` and names the loaded node.
- **"Routable now"** is `RoutingTable.healthyCandidates(modelId)`: the candidates that are not suspect and not `keyInvalid` (step 1 of the selection), with a model marked unloaded counted as `loadable`. Queue saturation does not hide a model.
- **Alias entries** carry the same `x-localai` fields as their model, with `alias_of` set and a `display_name` made from the alias (`lab-coder` becomes `Lab coder`; an alias has no catalog entry). An alias whose name is also a catalog id, or that a lower model id already owns, is not an alias (policy writes keep aliases globally unique; this is the fallback).
- **Scope** is `scopeAllows` / `keyAllows(key, requested, resolved)`, one rule for the list, `GET /v1/models/{id}` and (GW-06) the authorisation of an inference request. `assertKeyAllows` raises 403 LAI-GW-012. `GET /v1/models/{id}` answers 404 LAI-GW-020 for anything that is not in that key's list, whatever the reason (unknown, disabled, not routable now, out of scope).
- **`resolveModel`** (alias to id; policy enabled; id in the approved catalog) returns `null` for an unknown or disabled name, which the caller answers as 404 LAI-GW-020; it returns the policy and the catalog hints so GW-06 and GW-09 need no second lookup.
- No usage row is written for `endpoint: "models"` here; GW-08 records it for every endpoint. The log record's `requested_model` is set for `GET /v1/models/{id}`.

### 4.5 Usage recording

`UsageRecorder` queues rows in memory and flushes every 2 s or at 100 rows: one `createMany` into `localai_gateway_usage` plus an upsert-increment per (key, UTC day) into `localai_gateway_usage_daily`. It never blocks or fails a response; a flush error is retried once, then the batch is dropped and counted (`gateway.usage_dropped` log line, Doctor warning LAI-GW-060 if > 0 in the last hour). A daily server job `localai.gateway.usage_purge` deletes rows older than `usageRetentionDays`.

**Never recorded:** messages, prompts, completions, embeddings input, audio, tool arguments, the request body's size beyond bytes, client IP (only in the failed-auth limiter, in memory).

## 5. Failure handling and edge cases

| Situation | Behaviour | Code |
|---|---|---|
| Node goes offline mid-stream | Mid-stream error event + `[DONE]`; usage `estimated` | LAI-GW-045 |
| Node unreachable before first byte | Next candidate; node suspect 30 s | LAI-GW-040 if all fail |
| Stored node key wrong (node rotated, report failed) | 401 upstream → next candidate; alert; node re-reports key on refresh | LAI-GW-043 |
| Model unloaded on the node but the table still says loaded | Upstream "model not loaded" → mark, retry next / on-demand path | — |
| Two requests trigger a load at once | Dedup: one command; both wait on the same table change | — |
| Load would evict a busy model | That node skipped; if none left → 503 no_capacity | LAI-GW-021 |
| Client disconnects while waiting for a load | Request aborted; the load command continues | — |
| Client sends a huge base64 image | 413 at Fastify `bodyLimit` (8 MiB) or nginx (26 MiB) | LAI-GW-004 |
| Client asks `n > 1` | Passed through; llama-server decides | — |
| Key revoked while a stream is open | The stream finishes; next request 401 | — |
| Budget crossed mid-stream | Stream finishes; the next request 429 | LAI-GW-032 |
| API restart during streams | Streams cut (TCP close); clients retry | — |
| Credential store unreadable | 503 for every model | LAI-GW-050 |
| Clock: budget day rolls over during a request | Tokens counted on the day the request finished | — |

## 6. Security considerations

- **Never log bodies.** The gateway's logger has no access to the parsed body; the per-request log line is built from a fixed field list. Fastify's request logging for `/api/gateway/*` is set to `logLevel: 'warn'` and its serializers drop headers. A test sends a body containing a marker string and asserts it is absent from all captured logs and from the usage table.
- **Keys:** sha256 at rest; raw value only in the creation response; `Authorization` never forwarded upstream; node keys never leave the API process except to the node.
- **CORS:** by default none (`Access-Control-*` headers never sent): Open WebUI calls the gateway server-side and OpenCode/curl are not browsers. If `gateway.corsOrigins` lists origins, the gateway answers preflight for exactly those, with `Access-Control-Allow-Methods: GET, POST, OPTIONS`, `Access-Control-Allow-Headers: authorization, content-type`, `Access-Control-Max-Age: 600`, and **no** `Allow-Credentials` (keys are bearer tokens; cookies are never used on `/v1`). `*` is refused by the settings schema.
- **Body size limits:** 8 MiB JSON, 25 MiB multipart (the file part; the route's Fastify `bodyLimit` is 26 MiB, the same headroom as nginx, see the GW-10 notes; multipart parsed with `@fastify/multipart` registered only on the transcription route with `limits: { fileSize: 25 MiB, files: 1, fields: 10 }`), nginx `client_max_body_size 26m`.
- **SSRF:** upstream hosts come only from `localai_nodes` (lab:admin data), never from the request.
- **Abuse (T1):** keys required on every inference path; RPM, concurrency and token budgets per key; failed-auth limiter per IP.
- **Least privilege:** `gateway:use` holders can only manage their own keys; service keys and limit increases need `gateway:admin`.

## 7. Observability

- **Log line per request** (info; warn for 5xx): `request_id, key_prefix, user_id, endpoint, model, requested_model, node_id, attempts, status, stream, aborted, ttfb_ms, duration_ms, prompt_tokens, completion_tokens, usage_source, error_code`.
- **Log line per attempt failure** (warn): `request_id, node_id, attempt, reason (connect|tls|timeout|status_5xx|status_401|model_not_loaded), upstream_status`.
- **Doctor checks:** `localai.gateway.enabled` (LAI-GW-051), `localai.gateway.routable` (warn when zero routable nodes, LAI-GW-021), `localai.gateway.keys` (warn on any node in the table with `keyInvalid`, LAI-GW-043), `localai.gateway.usage` (LAI-GW-060).
- **UI:** Usage page; Lab overview header "Gateway: ● 2 nodes" ([cloud-app.md](cloud-app.md) §3.11).

## 8. Doctor codes (`LAI-GW`)

| Code | Sev | Meaning | Plain-language fix |
|---|---|---|---|
| LAI-GW-001 | error | The request body is not valid JSON | Fix the request body; it must be a JSON object |
| LAI-GW-002 | error | The request has no `model` | Add `"model": "<id>"`; `GET /v1/models` lists the ids |
| LAI-GW-003 | error | A parameter isn't supported on this endpoint | Remove it (for example `stream` on embeddings) |
| LAI-GW-004 | error | The request is too large | Send smaller images or audio (limit 8 MB JSON, 25 MB audio) |
| LAI-GW-010 | error | The API key is missing, wrong, revoked or expired | Create a new key at ai.marin.cr → API keys and use it as `Authorization: Bearer lai_…` |
| LAI-GW-011 | warning | Too many wrong keys from this address | Wait 5 minutes and use a valid key |
| LAI-GW-012 | error | This key may not use that model | Use an allowed model, or ask the admin to widen the key |
| LAI-GW-013 | error | The key's owner can no longer use the gateway | Ask the admin to restore access |
| LAI-GW-020 | error | Unknown model | Pick an id from `GET /v1/models` |
| LAI-GW-021 | error | No lab computer can run that model right now | Try later, pick another model, or ask the admin to load it |
| LAI-GW-022 | info | The model is being loaded | Wait about a minute and try again |
| LAI-GW-023 | warning | Every computer serving that model is busy | Try again in a few seconds |
| LAI-GW-024 | error | That endpoint isn't offered | Use chat/completions, completions, embeddings or audio/transcriptions |
| LAI-GW-030 | warning | Too many requests per minute for this key | Slow down, or ask the admin for a higher limit |
| LAI-GW-031 | warning | Too many requests at the same time for this key | Wait for a running request to finish |
| LAI-GW-032 | warning | Today's token allowance for this key is used up | Wait until 00:00 UTC, or ask the admin for a larger budget |
| LAI-GW-040 | error | The gateway couldn't reach the node | Admin: check the node is online and `https://ai-nN.marin.cr/health` answers |
| LAI-GW-041 | error | The node returned an error | Try again; admin: check the node's llama logs |
| LAI-GW-042 | error | The node refused the request (for example the prompt is longer than the model's context) | Shorten the prompt or follow the error message |
| LAI-GW-043 | critical | The cloud's copy of a node's API key is wrong | Admin: press "Refresh status" on that node, or run `sudo aictl cloud resend-key` on it |
| LAI-GW-044 | error | The node didn't start answering in time | Try again; long prompts on big models can take minutes |
| LAI-GW-045 | error | The node stopped in the middle of the answer | Ask again |
| LAI-GW-050 | critical | The gateway can't read the stored node keys | Admin: see LAI-CLD-032 |
| LAI-GW-051 | warning | The gateway is turned off or the site is in maintenance | Try later; admin: check `LOCALAI_GATEWAY_ENABLED` and maintenance mode |
| LAI-GW-052 | error | Unexpected gateway error | Try again; admin: check the API log for the request id |
| LAI-GW-060 | warning | Some usage records couldn't be saved | Admin: check the database; requests themselves were not affected |

## 9. Test plan

### Unit (Jest)
| Area | Cases |
|---|---|
| Key parsing/auth | malformed → 401 without DB call; revoked; expired; `expiresAt null` on service key valid; owner inactive; owner lost `gateway:use`; cache evicted on revoke |
| Limits | RPM window edges (60th ok, 61st 429 with correct `Retry-After`); concurrency released on abort and on error; budget refuses at exactly `used == budget` |
| Selection | table-driven over priority, preference, inflight/slots, ewma, suspect, keyInvalid, saturation; on-demand skips nodes that would evict busy models |
| SSE splitter | events split across chunks at every byte offset; `\r\n` variants; usage-only chunk captured and dropped/forwarded per `include_usage`; `[DONE]` |
| Error mapping | every row of §3.3 produces the exact JSON and status |
| `/v1/models` | union + loadable only with on-demand; aliases; scope filter; no host/IP fields |
| Body rewrite | `model` replaced; `include_usage` injected; other fields byte-for-byte equal after parse/serialise |

### Integration (fake upstream llama servers, Fastify inject + a real local HTTP server)
- `gateway.e2e.spec.ts`: two fake nodes (one slow, one failing). Non-stream success; stream success with tokens arriving 50 ms apart and the client receiving each within 20 ms (proves no buffering); first node 503 → retried on second (`x-localai-attempts: 2`); first node fails after first byte → mid-stream error event, no retry; client abort mid-stream → fake upstream sees its socket closed within 200 ms; first-byte timeout → 504; upstream 401 → retry + `keyInvalid`.
- On-demand: model loadable on fake node; gateway enqueues `localai.model.load`; test flips the fake snapshot to loaded; request completes; with no flip → 503 LAI-GW-022 after `loadWaitS`.
- Logs/usage: a marker string in the prompt never appears in captured logs or DB rows.
- Through nginx: the inner nginx container + API: stream arrives incrementally through `location /v1/`.

### Load testing
Script `apps/cloud/apps/api/test/load/gateway-load.k6.js` (k6, run manually, never in CI):
- Fake-upstream mode (`LOCALAI_GATEWAY_FAKE_UPSTREAM=1` test build pointing at a local echo server emitting 300 tokens at 20 ms): ramp to 200 concurrent streams for 5 min. Pass: gateway adds ≤ 5 ms p95 per event, API RSS < 600 MB, 0 errors, all streams complete.
- Real lab (owner, off-hours): 8 concurrent streams per node for 10 min with a service key. Pass: no 5xx from the gateway itself, per-node inflight matches llama `--parallel`, Open WebUI still usable.
- Record results in the PR.

### Manual
1. `curl -s https://ai.marin.cr/v1/models -H "Authorization: Bearer $LAI_KEY" | jq '.data[].id'` → lists loaded ids.
2. Without a key → `401` with `"localai_code":"LAI-GW-010"`.
3. Streaming: `curl -N … "stream":true` shows tokens arriving one by one.
4. Press Ctrl-C mid-stream; on the node, `journalctl -u localai-llama -n 20` shows the request cancelled.
5. Unload the model on its node from the Lab, repeat 1: id is gone (or `loadable` if on-demand).

## 10. Implementation breakdown

1. **GW-01 Gateway data model** — Scope: §3.6 fragment, migration, ownership/user-owned registration, audit actions. AC: migration clean; conformance passes; user deletion purges keys. Deps: CLOUD-04.
2. **GW-02 Key management API + UI** — Scope: §3.8 routes, show-once token, defaults from settings, API keys page with snippets, and the `GET/PUT /api/localai/settings` route with its per-key-prefix permission check ([cloud-app.md](cloud-app.md) §3.9). AC: token returned once; viewer 403; non-admin cannot create a service key or raise limits. Deps: GW-01.
3. **GW-03 Raw route scaffold, auth and error mapping** — Scope: `registerGatewayRoutes`, request id, maintenance/enabled checks, key auth + cache, failed-auth limiter, §3.3 error builder, `GET /v1/`, nginx `gateway.conf`, edge `streamPaths`. AC: every §3.3 auth/validation row tested; route-auth test; `/v1/` reachable through the inner nginx. Deps: GW-01.
4. **GW-04 Routing table + health probe** — Scope: §4.3 table, event-driven rebuild, 10 s safety rebuild, probe, suspect marking. AC: table-driven selection tests; rebuild within 1 s of a state event in a db test. Deps: CLOUD-06, CLOUD-07.
5. **GW-05 `/v1/models`** — Scope: §3.4, §4.4, aliases, scope filter. AC: fixture lab produces the exact JSON of §3.4. Deps: GW-04.
6. **GW-06 Non-stream proxy with retries** — Scope: chat/completions, completions, embeddings non-stream; upstream client (public mode); attempt loop; usage capture. AC: integration retry cases; client `Authorization` never forwarded (asserted at the fake upstream). Deps: GW-03, GW-04.
7. **GW-07 Streaming SSE passthrough** — Scope: hijack, splitter, backpressure, include_usage injection, abort propagation, idle/total timeouts, mid-stream error event. AC: no-buffering timing test; abort test; splitter fuzz test. Deps: GW-06.
8. **GW-08 Rate limits and budgets** — Scope: RPM, concurrency, daily budget, headers, `Retry-After`; usage recorder + daily rollup + purge job; the Usage API (`GET /api/localai/gateway/usage`, §3.8) and Usage page; the four gateway Doctor checks of §7 (`localai.gateway.enabled`, `.routable`, `.keys`, `.usage`). AC: limit unit tests; usage rows match requests in an integration run; no body content in DB. Deps: GW-06.
9. **GW-09 On-demand load + model policies** — Scope: policy API/page, on-demand path, busy-eviction guard, wait/timeout. AC: integration on-demand cases. Deps: GW-05, CLOUD-08.
10. **GW-10 Audio transcriptions + tailnet upstream mode** — Scope: multipart route limited to audio-capable models; `tailnet` upstream mode with SNI. AC: fake upstream receives the multipart unchanged; tailnet mode unit-tested with a local TLS server. Deps: GW-06.
11. **GW-11 Open WebUI and client cutover** — Scope: service key, Open WebUI steps (Appendix A.1, copied into the runbook), OpenCode/curl snippets in the key dialog; verifies requests appear on the Usage page (built in GW-08). AC: owner confirms Open WebUI lists lab models via one connection; old per-node connections removed. Deps: GW-07, GW-08.
12. **GW-12 Load testing harness** — Scope: k6 script, fake-upstream test build flag, results template. AC: fake-upstream run meets the §9 targets on the owner's machine. Deps: GW-07.

## Appendix A. Client wiring

### A.1 Open WebUI (`chat.marin.cr`): one connection instead of one per node

Preferred (UI):
1. At ai.marin.cr → API keys → **Create key**: name `open-webui`, kind **service** (admin), models: all. Copy the `lai_…` key.
2. Sign in to `https://chat.marin.cr` as admin → **Admin Panel → Settings → Connections → OpenAI API**.
3. Click **+**: URL `https://ai.marin.cr/v1`, auth **Bearer**, key = the `lai_…` key, prefix ID empty. Save. Click the refresh icon: the lab models appear.
4. Delete the connections `https://ai-n1.marin.cr/v1`, `https://ai-n2.marin.cr` and `https://ai-n3.marin.cr/v1` (trash icon on each). Keep `https://api.openai.com/v1` if it is used. Save.
5. Open a new chat, pick `qwen3.5-9b-q4km` (or any listed id), send "hola". What you should see: an answer, and in ai.marin.cr → Usage a request on the `open-webui` key.
6. Update `OPENAI_API_BASE_URL=https://ai.marin.cr/v1` in `apps/openwebui/.env` (marin-server-infra) so a fresh install matches.

Fallback (database), only if the UI is broken. The three settings are **index-aligned lists**; back up first:
```bash
docker exec infra-postgres psql -U admin -d open_webui -Atc \
  "select key, value from config where key in ('openai.api_base_urls','openai.api_keys','openai.api_configs');" \
  > /root/openwebui-connections.backup.txt
chmod 600 /root/openwebui-connections.backup.txt
```
Open WebUI's config schema changes between releases, so the DB path is **inspect, then write all three values together**. Read the backup, decide the new lists (`["https://api.openai.com/v1","https://ai.marin.cr/v1"]`, or only the gateway), keep the OpenAI key at its index, and renumber `openai.api_configs` (keys `"0"`, `"1"`, each `{"enable": true, "prefix_id": "", "model_ids": []}`). The gateway key is typed, never stored in shell history:
```bash
read -rs GW_KEY   # paste the lai_ key (not echoed)
OPENAI_KEY_JSON="$(docker exec infra-postgres psql -U admin -d open_webui -Atc \
  "select value::jsonb->1 from config where key='openai.api_keys';")"   # 1 = index of the api.openai.com entry today; check the backup
docker exec -i infra-postgres psql -U admin -d open_webui -v ON_ERROR_STOP=1 <<SQL
BEGIN;
UPDATE config SET value = '["https://api.openai.com/v1","https://ai.marin.cr/v1"]'::json WHERE key = 'openai.api_base_urls';
UPDATE config SET value = json_build_array('${OPENAI_KEY_JSON}'::json, '${GW_KEY}') WHERE key = 'openai.api_keys';
UPDATE config SET value = '{"0":{"enable":true,"prefix_id":"","model_ids":[]},"1":{"enable":true,"prefix_id":"","model_ids":[]}}'::json
 WHERE key = 'openai.api_configs';
COMMIT;
SQL
unset GW_KEY OPENAI_KEY_JSON
```
Then `docker compose restart open-webui` in `/opt/infra/apps/openwebui` and wait 20–40 s ([source/vps-ai-nodes-setup.md](../source/vps-ai-nodes-setup.md) §7). Verify without printing the key:
```bash
docker exec open-webui sh -c 'curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $OPENAI_API_KEY" https://ai.marin.cr/v1/models'
```

### A.2 OpenCode (`~/.config/opencode/opencode.json`)
```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "localai-lab/qwen3-coder-next-q4km",
  "provider": {
    "localai-lab": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Local AI lab (ai.marin.cr)",
      "options": {
        "baseURL": "https://ai.marin.cr/v1",
        "apiKey": "{file:~/.config/opencode/localai-gateway-key}",
        "timeout": 600000
      },
      "models": {
        "qwen3-coder-next-q4km": { "name": "Qwen3 Coder Next (lab)", "limit": { "context": 131072, "output": 8192 } },
        "qwen3.5-9b-q4km": { "name": "Qwen 3.5 9B (lab)", "limit": { "context": 32768, "output": 8192 } }
      }
    }
  }
}
```
Store the key: `install -m 600 /dev/stdin ~/.config/opencode/localai-gateway-key` and paste it, then Ctrl-D. Keep the existing `local-llama` provider (`http://127.0.0.1:8080/v1`) on nodes: it works offline; the lab provider does not.

### A.3 curl and Python
Same key file as A.2 (`~/.config/opencode/localai-gateway-key`, mode 600), so one stored key serves every client.
```bash
export LAI_KEY="$(cat ~/.config/opencode/localai-gateway-key)"
curl -s https://ai.marin.cr/v1/models -H "Authorization: Bearer $LAI_KEY" | jq '.data[] | {id, state: .["x-localai"].state}'
curl -N https://ai.marin.cr/v1/chat/completions -H "Authorization: Bearer $LAI_KEY" -H "Content-Type: application/json" \
  -d '{"model":"qwen3.5-9b-q4km","stream":true,"messages":[{"role":"user","content":"Name three edible plants in Costa Rica."}]}'
curl -s https://ai.marin.cr/v1/embeddings -H "Authorization: Bearer $LAI_KEY" -H "Content-Type: application/json" \
  -d '{"model":"<embedding-id>","input":"agua potable"}' | jq '.data[0].embedding | length'
```
```python
import os
from openai import OpenAI
client = OpenAI(base_url="https://ai.marin.cr/v1", api_key=open(os.path.expanduser("~/.config/opencode/localai-gateway-key")).read().strip())
for chunk in client.chat.completions.create(model="qwen3.5-9b-q4km", stream=True, messages=[{"role": "user", "content": "Hola"}]):
    print(chunk.choices[0].delta.content or "", end="", flush=True) if chunk.choices else None
```

## 11. Open questions

| Question | Proposed default |
|---|---|
| Budget day boundary UTC or Costa Rica time? | UTC (simple, documented in the error message); revisit if confusing. |
| Should `/v1/models` also show models only present on offline nodes? | No: every listed id must be requestable now. |
