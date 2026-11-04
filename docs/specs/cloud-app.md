# Spec: Cloud app (`ai.marin.cr`)

> **Status:** approved design · **Epic:** E6 · **Code:** `apps/cloud/` (EAB starter layout; domain code in `apps/cloud/apps/api/src/localai/` and `apps/cloud/apps/web/src/localai/`) · **Doctor area:** `LAI-CLD`
> Canonical names: [overview.md](../architecture/overview.md) §3. Decisions: [ADR-0003](../decisions/0003-outbound-only-cloud-link.md), [ADR-0008](../decisions/0008-cloud-on-eab.md). Gateway (`/v1`) is a separate spec: [gateway.md](gateway.md).

## 1. Purpose and scope

`ai.marin.cr` is the lab control plane. It is an EnterpriseAppBase (EAB) app generated from the EAB starter and kept inside this monorepo under `apps/cloud`. It gives an operator one place to:

- see every node (health, mode, connectivity, loaded models, ark readiness, alerts),
- send commands to one node (load/unload/default/download/delete a model, sync/verify the ark, go offline, rotate the llama key, restart a service, tail logs, refresh the catalog, update the agent),
- edit and approve the catalog that enrolled nodes download,
- add a node with a guided wizard,
- get alerts (node stale, ark drive older than 30 days (critical after 90), sync failures, drill failures),
- read the audit trail.

It also hosts the gateway ([gateway.md](gateway.md)) and holds the node llama keys the gateway uses.

**Non-goals**
- The cloud never connects to a node's agent. All control is node-pulled ([ADR-0003](../decisions/0003-outbound-only-cloud-link.md)). The only cloud→node traffic is inference to llama-server through the gateway or the nginx node vhosts.
- The cloud is **not** needed for any node feature. A node that never talks to the cloud again must lose nothing except remote control and gateway membership.
- The cloud cannot put a node into `survival` and cannot bring a node back `online` after `mode.offline` ([modes.md](../architecture/modes.md) §2.4).
- No shell execution on nodes, ever. Every command is one closed type with a strict schema.
- No chat UI. Open WebUI (`chat.marin.cr`) and the llama WebUI cover that.
- No edits to platform code. A missing hook is an EAB seam request (§3.12).

## 2. Concepts and glossary

| Term | Meaning |
|---|---|
| **EAB** | EnterpriseAppBase, the `@marinoscar/platform-*` packages (NestJS 11 on Fastify, Prisma 7, Postgres 16, React 19 + MUI) |
| **Worker node** | EAB's `worker_nodes` row. Created by `POST /api/nodes/register` (idempotent on owner + name). Its uuid is the `{id}` in every `/api/nodes/{id}/…` path |
| **Local AI node** | Our `localai_nodes` row. Keyed by the lab node id (`ai-n<N>`). Linked 1:1 to a worker node whose `name` equals the node id |
| **Node credential** | EAB `nod_…` token. Confined by EAB to paths under `/api/nodes`. Minted by the add-node wizard and **bound** to one Local AI node (§4.2) |
| **Command** | One cloud→node action of a closed type (`localai.*`). Stored in `localai_commands`, claimed by the node, settled with a result or a failure (§4.4) |
| **State snapshot** | The node's rich state (models, ark, services, mode, alerts), pushed by the agent to `POST /api/nodes/{id}/localai/state` (§3.6) |
| **Approved catalog** | The one `localai_catalog_versions` row with `status = approved`. Served to nodes at `GET /api/nodes/{id}/localai/catalog` |
| **Seam request** | An issue filed in `marinoscar/EnterpriseAppBase` asking for an extension point. We work around it in app code until it ships |

## 3. Interfaces

### 3.1 Creating `apps/cloud` from the EAB starter

Run once, by the owner, on a workstation that has both checkouts. The EAB checkout is only read from; nothing is committed there.

```bash
# 1. Build the platform packages in the EAB checkout (create renders infra/ with them)
cd ~/git/EnterpriseAppBase
git pull --ff-only
npm ci
npm run build:packages

# 2. Generate the app. --dir must not exist yet and must be outside the EAB repo.
node scripts/new-project.mjs create \
  --dir ~/git/local-ai/apps/cloud \
  --name "Local AI" \
  --repo marinoscar/local-ai \
  --cli laictl \
  --theme "#2e7d32" \
  --tagline "Control room for the Local AI lab" \
  --license mit --holder "Oscar Marin"

# 3. The generator ran `git init` inside apps/cloud. apps/cloud belongs to THIS repo:
rm -rf ~/git/local-ai/apps/cloud/.git
test ! -e ~/git/local-ai/apps/cloud/.git && echo "nested .git removed"

# 4. Move the starter's workflow out (GitHub only reads the repo root's workflows)
mkdir -p ~/git/local-ai/.github/workflows
git -C ~/git/local-ai mv -f apps/cloud/.github/workflows/ci.yml .github/workflows/cloud-ci.yml 2>/dev/null \
  || mv ~/git/local-ai/apps/cloud/.github/workflows/ci.yml ~/git/local-ai/.github/workflows/cloud-ci.yml
rmdir ~/git/local-ai/apps/cloud/.github/workflows ~/git/local-ai/apps/cloud/.github 2>/dev/null || true

# 5. Install and verify
cd ~/git/local-ai/apps/cloud
npm install
npm run setup            # builds laictl and writes infra/compose/.env (git-ignored)
npm run platform:check && npm run typecheck && npm test && npm run build
```

Notes:
- `--cli laictl` names the app's EAB CLI. It is the starter's `appctl`, renamed so it cannot be confused with `aictl` on the nodes. Everywhere EAB docs say `appctl`, this app runs `laictl` (for example `laictl deploy install`). The CLI's state dir is `~/.laictl/` and its env prefix is `LAICTL_`.
- Platform package versions stay **exactly pinned** as the starter writes them. Renovate moves them as one group (the starter's `renovate.json`). Copy that rule into the repo-root `renovate.json` with `"matchFileNames": ["apps/cloud/**"]`.
- `apps/cloud/AGENTS.md` and `apps/cloud/CLAUDE.md` (from the starter) stay. They add EAB rules on top of the repo-root `AGENTS.md`. The repo root wins on conflict.
- `apps/cloud/infra/compose/.env` is git-ignored by the starter. Check that the repo-root `.gitignore` does not un-ignore it, and add `apps/cloud/infra/compose/.env*` with `!*.example` to the root `.gitignore` as a second guard.

- Generation facts found when CLOUD-01 was done (#75): the generator and `npm install` need **Node 24** (`engines`); Node 22's npm fails with `Cannot read properties of null (reading 'edgesOut')`. `npm test` needs the environment block of `cloud-ci.yml` exported (`JWT_SECRET`, `COOKIE_SECRET`, `POSTGRES_*`, `GOOGLE_*`, `INITIAL_ADMIN_EMAIL=admin@example.test`, `OTEL_ENABLED=false`). The generated `infra/compose/.env.example` carries `INITIAL_ADMIN_EMAIL=admin@example.com` (rendered and locked by `platform-infra`, so not edited); `check-redaction.sh` allows the reserved `example.com` domain, the digests of the two generated lock files and the literal `192.168.0.0/16` range ([security-model.md §5](../architecture/security-model.md)).
- The `cron-enqueue-only` conformance suite is skipped in `apps/cloud/apps/api/test/conformance.spec.ts` with a written reason, because the deleted notes archive job held the app's only `@Cron`. Re-enable it with `{ exempt: [], minCronFiles: 1 }` in the change that adds the first app `@Cron` (the alert evaluator, CLOUD-15).
- The installed migrations `add_notes`, `add_documents` and `add_android_app` stay (an installed migration is never edited). The first app migration, `add_localai` (CLOUD-04), also drops the `notes` and `documents` tables (done, #78).

**Remove the starter's samples** (same PR):
- `apps/api/src/notes/` (all files), `apps/api/prisma/fragments/notes.prisma`, the `NOTES_PERMISSIONS` entry in `apps/api/src/platform/permissions.ts`, the notes registrations in `apps/api/src/platform/registrations.ts`, `NotesModule` in `app.module.ts`, the notes pages and routes in `apps/web/src`, the notes tests.
- The `NOTES_WEBHOOK_CREDENTIAL_PURPOSE` in `apps/api/src/platform/credentials/credential-purposes.ts` (replaced in §3.8).
- The slices we disable below keep their binding code (so turning one on later is a one-line change), except `sharing`, `exports` and `android-app`, whose code is deleted for good with the README's "Remove a slice's code for good" steps (they carry sample records we would otherwise maintain).
- Generate the first app migration only after the Local AI fragments exist (`npm run prisma:migrate:dev -- --name add_localai`).

### 3.2 Slices (`apps/cloud/packages/shared/slices.json`)

```json
{
  "enabled": ["credentials", "email", "notifications"]
}
```
(`catalog` stays as the starter wrote it, minus the three deleted slices.)

| Slice | On? | Why |
|---|---|---|
| `credentials` | **on** | Encrypted store for node llama keys (§3.8) and, in phase 2, the Route53 key. Needs `SECRETS_ENCRYPTION_KEY` |
| `email` | **on** | Required by `notifications`. Transport (SES or SMTP) is set in `/admin/settings/email`, never in `.env` |
| `notifications` | **on** | Alert delivery: bell/inbox, Web Push, email (§4.9) |
| `storage` | off | No uploads in v1. Turn on together with `db-backup` once an S3 bucket exists (§11) |
| `sharing` | off, code deleted | No shared records |
| `ai` | off | EAB's AI slice brokers external providers (OpenAI etc.). Our `/v1` gateway is a different thing and must not be confused with it. Its `/api/ai/*` routes answer 404 |
| `db-backup` | off | Needs `storage` (§11) |
| `exports` | off, code deleted | Nothing to export |
| `onboarding` | off | One administrator; the wizard is our onboarding |
| `android-app` | off, code deleted | No Android client |
| `telemetry` | off | No GreptimeDB on the VPS today. The OTel SDK stays off (`OTEL_ENABLED=false`) |

The always-mounted core (identity, settings, jobs, worker-node credentials and plane, Doctor, user-data) is what we build on.

### 3.3 CI in the monorepo

`.github/workflows/cloud-ci.yml` is the starter's `ci.yml` with these changes:

```yaml
name: cloud-ci
on:
  push:
    branches: [main]
    paths: ['apps/cloud/**', 'packages/ui/**', 'catalog/**', '.github/workflows/cloud-ci.yml']
  pull_request:
    branches: [main]
    paths: ['apps/cloud/**', 'packages/ui/**', 'catalog/**', '.github/workflows/cloud-ci.yml']
defaults:
  run:
    working-directory: apps/cloud
jobs:
  ci:
    # … starter job unchanged, except:
    #   POSTGRES_DB: local_ai_test
    #   INITIAL_ADMIN_EMAIL: admin@example.test        (never a real address)
    #   setup-node: cache-dependency-path: apps/cloud/package-lock.json
    #   setup-go (go-version-file: agent/go.mod) and a step "Build aictl for catalog lint" before `npm ci`:
    #     in agent/: CGO_ENABLED=0 go build -mod=vendor -o "$RUNNER_TEMP/aiagent" ./cmd/aiagent
    #                ln -sf "$RUNNER_TEMP/aiagent" "$RUNNER_TEMP/aictl"
    #                echo "LOCALAI_AICTL_PATH=$RUNNER_TEMP/aictl" >> "$GITHUB_ENV"
    #     (needed by the catalog lint tests, §4.6), then a "Check aictl runs" step that
    #     runs `"$LOCALAI_AICTL_PATH" --help` and `catalog lint --help`
```

Rules:
- `aiagent` is a multi-call binary that picks its role from `argv[0]` ([agent.md](agent.md) §3.1), so the file `LOCALAI_AICTL_PATH` points at must be named `aictl`: the build writes `aiagent` and a symlink named `aictl` points at it. The build uses the vendored modules (`-mod=vendor`), so it needs no network beyond the Go toolchain download by `setup-go`.
- The root `renovate.json` carries the starter's platform group rule with `"matchFileNames": ["apps/cloud/**"]`, so every `@marinoscar/platform-*` package in `apps/cloud` moves in one pull request. The starter's own `apps/cloud/renovate.json` stays as the generated copy (Renovate reads only the repo root's), and the root file is what takes effect.
- `paths` filters keep agent-only PRs from running the cloud suite, and the reverse. A PR that touches `catalog/**` runs the cloud suite because the catalog import parses those files.
- The repo-wide `redaction` job (`scripts/check-redaction.sh`, gitleaks) runs on every PR regardless of paths. It already covers `apps/cloud/**`.
- `apps/cloud/package-lock.json` is committed. The repo root has no npm workspace that includes `apps/cloud`; it is its own npm root.
- `packages/ui` (shared React components, [ADR-0004](../decisions/0004-desktop-thin-shell.md)) is consumed through `apps/cloud/packages/localai-ui`, a committed **symlink** to `../../../packages/ui`, listed by the starter's `packages/*` workspace glob. The deploy branch dereferences it (§3.13).

### 3.4 Identity, allowlist and permissions

Sign-in is EAB's Google OAuth only. Environment (§3.13): `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`, `GOOGLE_CALLBACK_URL=https://ai.marin.cr/api/auth/google/callback`, `INITIAL_ADMIN_EMAIL=<ADMIN_EMAIL>`, `TENANCY_MODE=single`.

- `<ADMIN_EMAIL>` bypasses the allowlist and becomes the system `admin` plus `org_admin` of the default organization (EAB behaviour).
- Every other person must be on the **email allowlist** (the **Allowlist** tab of the packaged *Users & Allowlist* page, `/admin/settings/users`; the allowlist is content inside that page, not a route of its own). Entries are individual addresses, never a whole domain (EAB security note on pre-hijack). A new user joins the default org with EAB's default role `viewer`.
- The Google OAuth client's authorised redirect URI is exactly `https://ai.marin.cr/api/auth/google/callback`. The OAuth consent screen stays in "Testing" with the allowed test users, or "Internal"; it is never public.

**Local AI permissions** are declared in `apps/api/src/localai/localai.permissions.ts` and added to `app.permissions` in `apps/api/src/platform/permissions.ts`:

```ts
export const LOCALAI_PERMISSIONS = {
  LAB_READ:      { id: 'lab:read',      scope: 'org',    description: 'See nodes, models, ark readiness, alerts and command history',
                   defaultGrants: ['viewer', 'contributor', 'org_admin'] },
  LAB_OPERATE:   { id: 'lab:operate',   scope: 'org',    description: 'Send non-destructive commands to nodes (load, unload, sync, restart, logs)',
                   defaultGrants: ['contributor', 'org_admin'] },
  LAB_ADMIN:     { id: 'lab:admin',     scope: 'system', description: 'Add and retire nodes, approve catalogs, destructive commands, audit',
                   defaultGrants: ['admin'] },
  GATEWAY_USE:   { id: 'gateway:use',   scope: 'org',    description: 'Create own gateway keys and call https://ai.marin.cr/v1',
                   defaultGrants: ['contributor', 'org_admin'] },
  GATEWAY_ADMIN: { id: 'gateway:admin', scope: 'system', description: 'Manage every gateway key, model policies and usage',
                   defaultGrants: ['admin'] },
} as const;
```

| EAB role | Local AI meaning | lab:read | lab:operate | lab:admin | gateway:use | gateway:admin |
|---|---|---|---|---|---|---|
| `admin` (system) + `org_admin` | Lab owner | ✓ | ✓ | ✓ | ✓ | ✓ |
| `org_admin` only | Trusted operator | ✓ | ✓ | | ✓ | |
| `contributor` | Family member / power user | ✓ | ✓ | | ✓ | |
| `viewer` (default) | Can look, cannot touch | ✓ | | | | |

The five ids are permanent strings. They are exported by `LOCALAI_PERMISSIONS` and registered as the app's own batch in `PERMISSION_OPTIONS` (`app: { permissions: [LOCALAI_PERMISSIONS] }`), so the seed (`platformPermissionCatalog`) and the registries (`registerPlatformPermissions`) carry them and the EAB `identity` conformance suite checks every route's `@Auth` permission against them. Tests: `src/localai/localai.permissions.spec.ts` (ids, scopes and the 4 role rows by 5 permissions matrix against the seeded catalog) and `test/localai-rbac.spec.ts` (the real guards over HTTP through a test-only probe controller, one route per permission: a `viewer` gets 403 on a `lab:operate` route, a `contributor` gets 200). The probe lives in `test/`, never in `src/`; the real routes arrive with CLOUD-09, CLOUD-10 and GW-02.

**Environment.** `TENANCY_MODE=single`, `GOOGLE_CALLBACK_URL` and `INITIAL_ADMIN_EMAIL` are already declared once by the platform's `.env.example` (generated, so the committed defaults are the platform's: `single`, a localhost callback and an example address). `app.env.example` therefore declares no second copy, which the deploy wizard would list twice; it documents the values this deployment sets (§3.13 table) as prose, and the wizard writes them into `.env` at deploy time (CLOUD-18). The operator steps (adding a person, which role does what, never a whole domain) are in [runbook 10, Part 8](../runbooks/10-lab-operations.md#part-8-give-someone-access).

A `nod_` credential has no org, so it gets none of these (EAB: a node credential gets its owner's **system** grants only). Node routes therefore do not use these permissions; they use the binding guard (§4.2).

### 3.5 Data model (Prisma fragments)

File: `apps/cloud/apps/api/prisma/fragments/localai.prisma`. Then `npm run db:compose && npm run prisma:generate && npm run prisma:migrate:dev -- --name add_localai`. Every model is classified in `apps/api/src/localai/localai.ownership.ts` (EAB model-ownership registry) as app-owned; `localai_gateway_keys` (gateway.md) is user-owned and registered in the user-owned data registry with purge-on-user-delete.

**As built (CLOUD-04, #78).** The fragment is `apps/cloud/apps/api/prisma/fragments/localai.prisma`; the migration is `20261104120542_add_localai`. Differences from the listing below:

- **No relation to `WorkerNode` or `NodeCredential`.** `@marinoscar/platform-db@0.1.0-next.21` does not mark either model `// @extensible`, so `extend model WorkerNode` / `extend model NodeCredential` is refused by `platform db compose` (`NOT_EXTENSIBLE`) and Prisma cannot declare the relation. `workerNodeId` and `nodeCredentialId` are plain unique `uuid` columns with **no foreign key** (a hand-written constraint would be schema drift that `prisma migrate diff` reports). The binding guard (§4.2) re-derives and re-links both on every node contact, which is already how a pruned worker node heals (§5). When the platform marks the two models extensible (an EAB seam request, template `seam_request.yml`), a later migration adds the two `extend model` back-relations and the `onDelete: SetNull` foreign keys.
- **Ownership.** All five models are classified `system` (deployment-wide: no `org_id`, no row-level security) in `apps/cloud/apps/api/src/localai/localai.ownership.ts`, registered from `src/platform/registrations.ts`. The user columns (`created_by_id`, `approved_by_id`, `requested_by_id`) are registered as actor fields (`purge: 'detach'`, `export: 'exclude'`, matching `onDelete: SetNull`) and carry a `keep` decision in `src/platform/user-data/user-data.manifest.ts`. `acknowledged_by_id` on `localai_alerts` is a plain column with no foreign key, as listed.
- **Raw-SQL indexes.** The three partial unique indexes are appended to the migration SQL and listed in `apps/cloud/apps/api/prisma/platform.lock` under `rawSqlIndexes` (name plus the `pg_indexes.indexdef` text), which is this app's allowlist; `test/prisma/platform-db-conformance.spec.ts` (`runDbConformance`) checks it offline and `platform db drift` against a database.
- **Dropped starter tables.** The same migration drops `notes` and `documents` (the models left the composed schema in CLOUD-01) and runs inside the `app.rls_bypass` transaction form because `documents` is a row-level-security table; the `documents_org_isolation` entry left `platform.lock` `rlsPolicies` with it. `android_app_releases` stays (the `android-app` fragment is still composed).
- **Tests.** `test/localai/indexes.db.spec.ts` (the three indexes and the settings seed, in rolled-back transactions) and `test/localai/ownership.spec.ts`.

```prisma
// ---------------------------------------------------------------------------
// Local AI nodes. One row per lab node id (ai-n<N>). Extends EAB worker_nodes.
// ---------------------------------------------------------------------------
enum LocalaiNodePhase {
  pending     // created by the wizard, never contacted the cloud
  enrolled    // the bound credential has made at least one /localai call
  retired     // credential revoked, key deleted; row kept for audit/history
}

enum LocalaiServePattern {
  A           // tailscale serve --tcp=18080
  B           // tailscale serve --https=443
  none        // not published on the tailnet (node.yaml tailscale.serve_pattern: none)
}

model LocalaiNode {
  id                String               @id @default(uuid()) @db.Uuid
  nodeId            String               @unique @map("node_id")            // "ai-n3"; ^ai-n[1-9][0-9]{0,3}$ ; never reused
  displayName       String               @map("display_name")               // "HP ZBook (ai-n3)"
  hardware          String?                                                 // free text, from inventory
  backend           String?                                                 // vulkan | cuda | cpu (as last reported)
  publicHost        String               @map("public_host")                // "ai-n3.marin.cr"
  servePattern      LocalaiServePattern  @map("serve_pattern")
  tailnetMachine    String?              @map("tailnet_machine")            // "hp-linux" (may be shown)
  tailnetIp         String?              @map("tailnet_ip")                 // STORED, NEVER SHOWN except to lab:admin in Settings (masked); never logged, never in /v1 or snapshots to viewers
  profile           String?                                                 // last reported profile id
  mode              String?                                                 // online | offline | survival (last reported)
  connectivity      String?                                                 // full | internet | tailnet | none | not_checked
  agentVersion      String?              @map("agent_version")
  catalogVersion    String?              @map("catalog_version")            // what the node says it uses
  catalogPin        String?              @map("catalog_pin")                // if set, serve this version instead of the approved one
  gatewayEnabled    Boolean              @default(true) @map("gateway_enabled")   // false = drained from /v1 routing
  gatewayPriority   Int                  @default(100) @map("gateway_priority")   // lower is preferred
  phase             LocalaiNodePhase     @default(pending)
  workerNodeId      String?              @unique @map("worker_node_id") @db.Uuid      // EAB worker_nodes.id; plain column, NO foreign key (see "As built" below)
  nodeCredentialId  String?              @unique @map("node_credential_id") @db.Uuid  // the ONE credential bound to this node; plain column, NO foreign key
  lastState         Json?                @map("last_state") @db.JsonB       // last accepted snapshot (§3.6), verbatim
  lastStateAt       DateTime?            @map("last_state_at") @db.Timestamptz       // server receive time
  lastStateSeq      BigInt?              @map("last_state_seq")             // snapshot seq, monotonic per agent boot+counter
  keyHint           String?              @map("key_hint")                   // "ab12…wx9z" of the stored llama key
  keyReportedAt     DateTime?            @map("key_reported_at") @db.Timestamptz
  publishChecklist  Json                 @default("{}") @map("publish_checklist") @db.JsonB   // §4.8
  createdById       String?              @map("created_by_id") @db.Uuid
  createdAt         DateTime             @default(now()) @map("created_at") @db.Timestamptz
  updatedAt         DateTime             @updatedAt @map("updated_at") @db.Timestamptz
  retiredAt         DateTime?            @map("retired_at") @db.Timestamptz

  createdBy         User?                @relation("LocalaiNodeCreatedBy", fields: [createdById], references: [id], onDelete: SetNull)
  models            LocalaiNodeModel[]
  commands          LocalaiCommand[]
  alerts            LocalaiAlert[]

  @@map("localai_nodes")
}

extend model User {
  localaiNodesCreated   LocalaiNode[]           @relation("LocalaiNodeCreatedBy")
  localaiCommands       LocalaiCommand[]        @relation("LocalaiCommandRequestedBy")
  localaiCatalogCreated LocalaiCatalogVersion[] @relation("LocalaiCatalogCreatedBy")
  localaiCatalogApproved LocalaiCatalogVersion[] @relation("LocalaiCatalogApprovedBy")
}

// ---------------------------------------------------------------------------
// Per-node model state, projected from each accepted snapshot (§4.3).
// A table (not only JSONB) because the gateway routing table and the UI query it.
// ---------------------------------------------------------------------------
enum LocalaiModelStatus { loaded loading unloaded failed }

model LocalaiNodeModel {
  nodeId          String              @map("node_id") @db.Uuid       // LocalaiNode.id
  modelId         String              @map("model_id")               // catalog model id
  present         Boolean                                            // files on some content root
  verified        Boolean                                            // sha256-verified
  loadable        Boolean
  loadableReason  String?             @map("loadable_reason")        // e.g. "needs 6400 MiB VRAM, 8192 total, 7100 used"
  status          LocalaiModelStatus
  isDefault       Boolean             @default(false) @map("is_default")
  slots           Int                 @default(1)                    // llama --parallel for this preset
  roles           String[]                                           // from catalog: chat, coder, vision, embedding, stt…
  inputModalities String[]            @map("input_modalities")       // text, image, audio (as reported by router /models)
  ctx             Int?                                               // preset context length
  updatedAt       DateTime            @updatedAt @map("updated_at") @db.Timestamptz
  node            LocalaiNode         @relation(fields: [nodeId], references: [id], onDelete: Cascade)

  @@id([nodeId, modelId])
  @@index([modelId, status])
  @@map("localai_node_models")
}

// ---------------------------------------------------------------------------
// Catalog versions (§4.6).
// ---------------------------------------------------------------------------
enum LocalaiCatalogStatus { draft invalid valid approved superseded }
enum LocalaiCatalogSource { github_release upload editor }

model LocalaiCatalogVersion {
  id            String                @id @default(uuid()) @db.Uuid
  version       String                @unique                       // CATALOG_VERSION: YYYY.MM.DD-N
  source        LocalaiCatalogSource
  sourceRef     String?               @map("source_ref")             // "catalog-2026.11.02-1" or a commit sha, or the version it was edited from
  status        LocalaiCatalogStatus  @default(draft)
  files         Json                  @db.JsonB                      // { "models.yaml": {"sha256": "…", "content": "…"}, "profiles/small-8gb.yaml": {…}, … }
  bundleSha256  String                @map("bundle_sha256")          // sha256 over sorted "path\0sha256\n" lines; the ETag
  lintResult    Json?                 @map("lint_result") @db.JsonB  // aictl catalog lint --json output
  lintedAt      DateTime?             @map("linted_at") @db.Timestamptz
  notes         String?
  createdById   String?               @map("created_by_id") @db.Uuid
  approvedById  String?               @map("approved_by_id") @db.Uuid
  approvedAt    DateTime?             @map("approved_at") @db.Timestamptz
  createdAt     DateTime              @default(now()) @map("created_at") @db.Timestamptz
  createdBy     User?                 @relation("LocalaiCatalogCreatedBy", fields: [createdById], references: [id], onDelete: SetNull)
  approvedBy    User?                 @relation("LocalaiCatalogApprovedBy", fields: [approvedById], references: [id], onDelete: SetNull)

  @@index([status])
  @@map("localai_catalog_versions")
}
// Raw SQL in the migration (Prisma can't express it): at most one approved row.
//   CREATE UNIQUE INDEX localai_catalog_one_approved ON localai_catalog_versions ((true)) WHERE status = 'approved';

// ---------------------------------------------------------------------------
// Commands (interim command plane, §4.4). Not EAB jobs until seam SR-1 ships.
// ---------------------------------------------------------------------------
enum LocalaiCommandStatus { queued running succeeded failed expired cancelled }

model LocalaiCommand {
  id              String               @id @default(uuid()) @db.Uuid
  nodeId          String               @map("node_id") @db.Uuid      // LocalaiNode.id: THE target. Claimable only by that node
  type            String                                             // "localai.model.load" … (closed set, §3.7)
  params          Json                 @db.JsonB                     // validated with the type's params schema at enqueue
  status          LocalaiCommandStatus @default(queued)
  dedupKey        String?              @map("dedup_key")             // "<type>:<subject>" ; partial unique while active
  source          String               @default("ui")                // ui | api | gateway | system | desktop
  requestedById   String?              @map("requested_by_id") @db.Uuid
  attempts        Int                  @default(0)                   // claims so far (max 1 for most types, §4.4)
  claimToken      String?              @map("claim_token") @db.Uuid
  leaseExpiresAt  DateTime?            @map("lease_expires_at") @db.Timestamptz
  claimedAt       DateTime?            @map("claimed_at") @db.Timestamptz
  expiresAt       DateTime             @map("expires_at") @db.Timestamptz   // queued commands not claimed by then become `expired`
  finishedAt      DateTime?            @map("finished_at") @db.Timestamptz
  result          Json?                @db.JsonB                     // validated with the type's result schema
  error           Json?                @db.JsonB                     // { code: "LAI-…", message: "…", details?: {} }
  traceparent     String?
  createdAt       DateTime             @default(now()) @map("created_at") @db.Timestamptz
  node            LocalaiNode          @relation(fields: [nodeId], references: [id], onDelete: Cascade)
  requestedBy     User?                @relation("LocalaiCommandRequestedBy", fields: [requestedById], references: [id], onDelete: SetNull)

  @@index([nodeId, status, createdAt])
  @@index([status, expiresAt])
  @@map("localai_commands")
}
// Raw SQL in the migration:
//   CREATE UNIQUE INDEX localai_commands_active_dedup ON localai_commands (node_id, dedup_key)
//     WHERE status IN ('queued','running') AND dedup_key IS NOT NULL;

// ---------------------------------------------------------------------------
// Alerts (§4.9).
// ---------------------------------------------------------------------------
enum LocalaiAlertState { active cleared }

model LocalaiAlert {
  id             String            @id @default(uuid()) @db.Uuid
  nodeId         String?           @map("node_id") @db.Uuid          // null = lab-wide
  kind           String                                              // node.stale, ark.drive_stale, sync.failed, drill.failed, …
  code           String                                              // LAI-CLD-0NN
  severity       String                                              // info | warning | critical
  state          LocalaiAlertState @default(active)
  title          String                                              // plain language
  details        Json?             @db.JsonB
  firstRaisedAt  DateTime          @default(now()) @map("first_raised_at") @db.Timestamptz
  lastSeenAt     DateTime          @default(now()) @map("last_seen_at") @db.Timestamptz
  clearedAt      DateTime?         @map("cleared_at") @db.Timestamptz
  acknowledgedAt DateTime?         @map("acknowledged_at") @db.Timestamptz
  acknowledgedById String?         @map("acknowledged_by_id") @db.Uuid
  notifiedAt     DateTime?         @map("notified_at") @db.Timestamptz
  node           LocalaiNode?      @relation(fields: [nodeId], references: [id], onDelete: Cascade)

  @@index([state, severity])
  @@map("localai_alerts")
}
// Raw SQL: CREATE UNIQUE INDEX localai_alerts_one_active ON localai_alerts (coalesce(node_id,'00000000-0000-0000-0000-000000000000'::uuid), kind) WHERE state = 'active';
```

The three partial indexes are raw SQL in the app migration; list them in the app's raw-SQL index allowlist so the EAB tripwire accepts them. `localai_model_policies`, `localai_gateway_keys`, `localai_gateway_usage` and `localai_gateway_usage_daily` are defined in [gateway.md](gateway.md) §3.6 and live in `fragments/localai-gateway.prisma`.

**Audit trail.** Every state-changing action writes EAB's `audit_events` through `AuditService.record({ action, actorUserId, targetType, targetId, meta })`. Actions (permanent strings):

| action | targetType / targetId | meta (never secrets) |
|---|---|---|
| `localai.node.created` / `.updated` / `.retired` | `localai_node` / node id | changed fields (tailnet ip as `"<set>"`) |
| `localai.node.credential_minted` / `.credential_revoked` | `localai_node` / node id | `{ credentialId, tokenPrefix }` |
| `localai.node.enrolled` | `localai_node` / node id | `{ workerNodeId, agentVersion }` |
| `localai.node.binding_refused` | `localai_node` / claimed node id | `{ credentialPrefix, claimedNodeId }` (§4.2 step 5) |
| `localai.node.mode_changed` | `localai_node` / node id | `{ from, to }`; actor = the node (§4.3 step 5) |
| `localai.node.tailnet_revealed` | `localai_node` / node id | `{}` (Settings tab **Reveal**, §3.11) |
| `localai.node.key_reported` | `localai_node` / node id | `{ keyHint, reason: "enroll"|"rotate"|"refresh" }` |
| `localai.command.requested` / `.cancelled` | `localai_command` / command id | `{ nodeId, type, params }` (params never hold secrets) |
| `localai.command.settled` | `localai_command` / command id | `{ nodeId, type, status, errorCode? }` (result omitted for `logs.tail`) |
| `localai.catalog.imported` / `.linted` / `.approved` | `localai_catalog` / version | `{ source, sourceRef, valid }` |
| `localai.checklist.updated` | `localai_node` / node id | `{ step, state }` |
| `localai.alert.acknowledged` | `localai_alert` / alert id | `{ kind, nodeId }` |
| `localai.gateway.*` | see [gateway.md](gateway.md) §3.6 | |

### 3.6 Node-facing HTTP (the cloud link)

All node routes sit under `/api/nodes/{id}/localai/` so EAB's `nod_` allowlist (exact prefix `/api/nodes/`) admits them without any guard change. `{id}` is the **worker node uuid** returned by `POST /api/nodes/register`. Every route runs the binding guard (§4.2) first. Responses use EAB's `{ data, meta }` envelope; errors use EAB's error body with our code in `details.code`.

The agent also uses EAB's own node routes unchanged ([EAB worker-nodes.md](https://github.com/marinoscar/EnterpriseAppBase/blob/main/docs/specs/worker-nodes.md) is the contract; the agent's Go client is specified in [agent.md](agent.md) §4.8):

| EAB route | Agent usage |
|---|---|
| `POST /api/nodes/register` | At link start, exactly as [agent.md](agent.md) §4.8.2 (`name` = node id, `eligibleTypes` = `localai.<cloud.allow entry>`, `concurrency: 1`, `capabilities.localai`). While `commandPlane = localai`, no server handler offers `localai.*` types to EAB nodes, so EAB's claim drops them (harmless); the list still tells the cloud UI which commands the node allows (LAI-CLD-057 hint) |
| `POST /api/nodes/{id}/heartbeat` | Every **15 s**. `status: online`, `vitals` (host + power parts once `node_host_vitals` is advertised, plus `vitals.extra`: `localai.models_loaded`, `localai.vram_used_mib`, `localai.vram_total_mib`, `localai.sync_running` 0/1, `localai.alerts_active`; these 5 are shown by name, and the other 11 keys of [agent.md](agent.md) §4.8.3 are accepted and shown by key, so do not trim the agent's list) |
| `POST /api/nodes/{id}/deregister` | On `→ offline`/`→ survival` (3 s timeout, errors ignored, [modes.md](../architecture/modes.md) §2.1 step 2) |

EAB settings changed at first deploy (`/admin/settings/workers`, or seed): `nodes.staleHeartbeatSeconds = 45`, `nodes.offlineStaleMultiplier = 4` (offline after 3 min), `nodes.offlineRetentionDays = 3650` (a laptop offline for months must not be pruned; pruning would orphan `workerNodeId`, which the guard heals on the next contact anyway).

As built (CLOUD-04): `npm run prisma:seed` runs the app seed step `localai.node-settings` (`src/localai/localai.seed.ts`, passed to `seedPlatform` as an `extras` step). It writes these three keys into `system_settings.global.value.nodes` **only where the key is absent**, so a value an administrator set on `/admin/settings/workers` is never overwritten and a second run changes nothing. Stored keys win over the platform defaults (90 s, 4, 30 days) when settings are read, key by key, so existing deployments get the values on their next seed. `jobSecretBrokerEnabled` is not touched (the platform default, off, stays).

#### `GET /api/nodes/{id}/localai/config`
Handshake. The agent calls it after `register` and whenever it gets a 404 on another `/localai/` route.

```json
{ "data": {
    "nodeId": "ai-n3",
    "apiVersion": 1,
    "features": ["state.v1", "commands.v1", "catalog.v1", "key.v1"],
    "commandPlane": "localai",
    "heartbeatIntervalS": 15,
    "stateMinIntervalS": 5,
    "stateMaxIntervalS": 60,
    "claimWaitMaxMs": 25000,
    "keyReported": true,
    "serverTime": "2026-11-02T15:04:05.000Z"
  }, "meta": { "timestamp": "2026-11-02T15:04:05.000Z" } }
```
`commandPlane` is `localai` until SR-1/SR-2 ship (§3.12), then `eab`. The agent compares `serverTime` with its clock and raises `LAI-AGT-012` when skew > 5 min ([agent.md](agent.md) §4.8.3).

As built (CLOUD-05): the body is parsed through the strict `LocalaiConfigResponseSchema` before it is sent (`contracts/config.schema.ts`), `keyReported` is `keyReportedAt != null`, `serverTime` is the API's clock, and `heartbeatIntervalS`, `stateMinIntervalS`, `stateMaxIntervalS` and `claimWaitMaxMs` are constants (`LOCALAI_NODE_LINK_TIMING`), not settings. The exact body is the shared fixture `test/fixtures/localai/config.response.json`.

#### `POST /api/nodes/{id}/localai/state`
Body ≤ 256 KiB (route-level `bodyLimit`), `Content-Type: application/json`, exactly the `LocalaiStateSnapshotV1` schema below (the agent builds it from its own status, models and ark views; it does not forward those API bodies as they are). A `404`/`405` from an older cloud triggers the agent's capabilities fallback ([agent.md](agent.md) §4.8.4, `LAI-CLD-056`). The agent sends it on every relevant change (debounced to `stateMinIntervalS`) and at least every `stateMaxIntervalS`. Answers `200 { data: { accepted: true, seq } }`, or `409 { details: { code: "LAI-CLD-008", reason: "stale_seq" } }` when `seq` is not newer than `lastStateSeq` for the same `boot_id` (ignored by the agent), or `400` with Zod issues in `details`.

Zod schema (`apps/api/src/localai/contracts/state.schema.ts`; the Go agent mirrors it in `agent/internal/cloud/state.go`, [agent.md](agent.md) §4.8):

```ts
import { z } from 'zod';
export const ModelId   = z.string().regex(/^[a-z0-9][a-z0-9.\-]{2,63}$/);
export const ContentId = ModelId;
export const NodeIdStr = z.string().regex(/^ai-n[1-9][0-9]{0,3}$/);
export const RootId    = z.string().regex(/^(internal|ark-[0-9a-f]{8})$/);   // ark-sync.md §2 root id
export const RootKind  = z.enum(['internal', 'ark_drive']);
const Iso = z.string().datetime({ offset: true });
const Code = z.string().regex(/^LAI-[A-Z]{2,3}-\d{3}$/);
const Mib = z.number().int().min(0).max(16_777_216);
const Bytes = z.number().int().min(0).max(2 ** 53 - 1);

export const LocalaiStateSnapshotV1 = z.object({
  schema_version: z.literal(1),
  node_id: NodeIdStr,
  boot_id: z.string().uuid(),                      // new per agent process start
  seq: z.number().int().min(0),                    // +1 per snapshot within a boot
  reported_at: Iso,
  agent_version: z.string().max(64),
  mode: z.enum(['online', 'offline', 'survival']),
  mode_transition: z.object({ target: z.enum(['online', 'offline', 'survival']), step: z.string().max(64), started_at: Iso }).nullable(),
  connectivity: z.enum(['full', 'internet', 'tailnet', 'none', 'not_checked']),
  profile: z.string().max(64),
  catalog_version: z.string().regex(/^\d{4}\.\d{2}\.\d{2}-\d+$/).nullable(),
  llama: z.object({
    build: z.string().max(80),                     // "<commit>-<backend>", e.g. "f3586e66b-cuda"
    backend: z.enum(['cuda', 'vulkan', 'cpu']),
    router: z.enum(['up', 'down', 'starting', 'single_mode']),
    models_max: z.number().int().min(1).max(16),
  }).strict(),
  hardware: z.object({
    gpus: z.array(z.object({ name: z.string().max(128), vram_total_mib: Mib, vram_used_mib: Mib, temp_c: z.number().min(-50).max(150).nullable() }).strict()).max(8),
    ram_total_mib: Mib, ram_used_mib: Mib,
    cpu_temp_c: z.number().min(-50).max(150).nullable(),
    power: z.object({ source: z.enum(['ac', 'battery', 'unknown']), battery_percent: z.number().min(0).max(100).nullable() }).strict(),
  }).strict(),
  models: z.array(z.object({
    id: ModelId,
    present: z.boolean(), verified: z.boolean(),
    loadable: z.boolean(), loadable_reason: z.string().max(200).nullable(),
    status: z.enum(['loaded', 'loading', 'unloaded', 'failed']),
    default: z.boolean(),
    slots: z.number().int().min(1).max(64),
    ctx: z.number().int().min(0).max(4_194_304).nullable(),
    roles: z.array(z.string().max(32)).max(8),
    input_modalities: z.array(z.enum(['text', 'image', 'audio', 'video'])).max(4),
    roots: z.array(RootId).max(9),
  }).strict()).max(200),
  services: z.array(z.object({
    name: z.enum(['llama', 'kiwix', 'whisper', 'sync', 'drill']),
    active_state: z.enum(['active', 'inactive', 'failed', 'activating', 'deactivating', 'not_installed']),
    since: Iso.nullable(),
  }).strict()).max(8),
  ark: z.object({
    roots: z.array(z.object({
      id: RootId,
      present: z.boolean(),                           // ark_drive: is a drive mounted now
      ready_generated_at: Iso.nullable(),             // READY.json generated_at
      tiers: z.object({ t1: z.enum(['ok', 'partial', 'missing']), t2: z.enum(['ok', 'partial', 'missing']), t3: z.enum(['ok', 'partial', 'missing', 'not_planned']) }).strict(),
      items_missing: z.number().int().min(0), items_failed: z.number().int().min(0), items_skipped_budget: z.number().int().min(0),
      bytes_used: Bytes, bytes_free: Bytes,
      last_sync_at: Iso.nullable(), last_verify_at: Iso.nullable(),
      overall: z.enum(['green', 'yellow', 'red']),        // READY.json overall (ark-sync.md §4.12.2)
      survival_capable: z.boolean(),                       // READY.json survival_capable
      ready: ReadyReportV1.nullable(),                     // the full READY.json of this root (see below); null if the root has none yet
    }).strict()).max(9),
    drive_last_synced_at: Iso.nullable(),             // last successful sync TO an AI-ARK drive (survives unplugging)
  }).strict(),
  sync: z.object({
    running: z.boolean(),
    run_id: z.string().max(64).nullable(),
    progress_pct: z.number().min(0).max(100).nullable(),
    last_run: z.object({ id: z.string().max(64), finished_at: Iso, result: z.enum(['ok', 'partial', 'failed', 'skipped']), skip_reason: z.string().max(64).nullable(), error_code: Code.nullable() }).strict().nullable(),
  }).strict(),
  drill: z.object({
    run_id: z.string().max(64),                    // drill.md §3.4 run_id
    last_run_at: Iso,                              // = finished_at of the last run
    result: z.enum(['pass', 'warn', 'fail']),      // drill.md overall
    survival_ready: z.boolean(),
    failed_checks: z.array(Code).max(50),          // doctor codes of failed/warned checks
    failed_check_ids: z.array(z.string().max(96)).max(50),   // drill.md check ids, e.g. "drl.stt.es"
    results: DrillResultsV1.nullable(),            // full drill.md §3.4 document; sent in the first snapshot after a run, then null
  }).strict().nullable(),
  tailscale: z.object({ serve_pattern: z.enum(['A', 'B', 'none']), serve_active: z.boolean(), backend_state: z.string().max(32).nullable() }).strict(),
  alerts: z.array(z.object({ code: Code, severity: z.enum(['info', 'warning', 'critical']), title: z.string().max(200) }).strict()).max(50),
}).strict();
export type LocalaiStateSnapshotV1 = z.infer<typeof LocalaiStateSnapshotV1>;

// Declared above LocalaiStateSnapshotV1 in the real file.
// Full READY.json (ark-sync.md §4.12.1, authoritative for its fields) minus root.path
// (drive mount paths contain the desktop user name) and minus generated_by.node_id (implicit).
const ReadyItem = z.object({
  id: ContentId, kind: z.string().max(32), tier: z.number().int().min(1).max(3), survival: z.boolean().optional(),
  status: z.enum(['verified', 'stale', 'missing', 'skipped', 'failed', 'quarantined', 'orphan']),
  version: z.string().max(80).optional(), verified_at: Iso.optional(), last_reverified_at: Iso.optional(),
  checked_current_at: Iso.optional(), age_days: z.number().int().min(0).optional(),
  freshness: z.enum(['green', 'yellow', 'red']).optional(), bytes: Bytes.optional(),
  trust: z.enum(['pinned', 'sidecar', 'signed_index', 'computed', 'tofu']).optional(), reason: z.string().max(200).optional(), code: Code.optional(),
}).strict();
export const ReadyReportV1 = z.object({
  schema_version: z.literal(1),
  generated_at: Iso,
  generated_by: z.object({ agent_version: z.string().max(64) }).strict(),
  root: z.object({
    id: RootId, kind: RootKind, profile: z.string().max(64), catalog_version: z.string().max(32).nullable(),
    fs_total_bytes: Bytes, fs_free_bytes: Bytes, budget_bytes: Bytes, used_bytes: Bytes, trash_bytes: Bytes, read_only: z.boolean(),
  }).strict(),
  overall: z.enum(['green', 'yellow', 'red']),
  survival_capable: z.boolean(),
  last_sync: z.object({ run_id: z.string().max(64), trigger: z.string().max(32), finished_at: Iso, state: z.string().max(32), age_days: z.number().int().min(0) }).strict().nullable(),
  last_full_reverify_at: Iso.nullable(),
  counts: z.record(z.enum(['verified', 'stale', 'missing', 'skipped', 'failed', 'quarantined', 'orphan']), z.number().int().min(0)),
  items: z.array(ReadyItem).max(600),
  problems: z.array(z.object({ code: Code, severity: z.enum(['info', 'warning', 'critical']), item: ContentId.optional(), title: z.string().max(200) }).strict()).max(100),
  clock_wrong: z.boolean().optional(),           // ark-sync.md §5: the node clock is wrong; items carry no freshness and UIs show age grey
}).strict();
// Full drill results (drill.md §3.4, authoritative for its fields); only the fixed test strings appear in details.
export const DrillResultsV1 = z.object({
  schema_version: z.literal(1), run_id: z.string().max(64), trigger: z.string().max(32),
  agent_version: z.string().max(64), catalog_version: z.string().max(32).nullable(),
  mode: z.enum(['online', 'offline', 'survival']), quick: z.boolean(),
  started_at: Iso, finished_at: Iso, overall: z.enum(['pass', 'warn', 'fail']), survival_ready: z.boolean(),
  counts: z.object({ pass: z.number().int().min(0), warn: z.number().int().min(0), fail: z.number().int().min(0), skip: z.number().int().min(0) }).strict(),
  checks: z.array(z.object({ id: z.string().max(96), area: z.string().max(32), result: z.enum(['pass', 'warn', 'fail', 'skip']),
    code: Code.optional(), ms: z.number().int().min(0), details: z.record(z.string(), z.union([z.string().max(500), z.number(), z.boolean(), z.null()])).optional() }).strict()).max(100),
  restored: z.object({ loaded_models: z.array(ModelId).max(16), default_model: ModelId.nullable(), ok: z.boolean() }).strict(),
  network_fence: z.object({ enabled: z.boolean(), blocked_dials: z.number().int().min(0) }).strict(),
}).strict();
```

Mapping rules the agent applies when it builds the snapshot ([agent.md](agent.md) §4.8.4 implements them):
- **Alert severity:** the agent's `error` maps to `critical` (the cloud enum has no `error`); `info` and `warning` are sent as they are. The same mapping applies to `ready.problems[].severity`.
- **`tailscale.serve_pattern`:** sent from `node.yaml` (`A`, `B` or `none`); `none` means the node is not published and the gateway never routes to it.
- **`ark.roots[].ready`:** the root's full READY.json ([ark-sync.md](ark-sync.md) §4.12.1) without `root.path` and `generated_by.node_id`. It is sent whenever that root's `READY.json` `generated_at` changed since the last accepted snapshot, and otherwise `null` (the cloud keeps the last non-null value per root in `lastState`). If the body would exceed 256 KiB, the agent trims in this order ([agent.md](agent.md) §4.8.4 follows it): first `items[]` of drive roots to non-`verified` items, then `items[]` of the internal root the same way, and only then the `models` list (non-present models first).
- **`drill.results`:** the full drill results document ([drill.md](drill.md) §3.4), sent once in the first snapshot after a run finishes, otherwise `null`. The agent **drops `node_id`** from the drill document (implicit, as for READY; `DrillResultsV1` is strict). `trigger` is one of drill.md §2's `scheduled | manual | cloud`. A `manual_plug` record (pull-the-plug drill written by `aictl drill record-manual`) is **never** sent as `results` (it has no checks and no restore): it only updates `drill.last_run_at`, `drill.result`, `drill.survival_ready` (unchanged from the last automatic run) and `drill.run_id`, with `failed_checks`/`failed_check_ids` empty.
- **`ark.roots[].tiers`:** for each tier N, `tN = ok` if every tier-N item planned for that root is `verified`, `missing` if none is, else `partial`; `t3 = not_planned` when the root's profile plans no tier-3 items ([ark-sync.md](ark-sync.md) §4.16).
- **`sync.last_run.result`** from the run's final state ([ark-sync.md](ark-sync.md) §3.5): `done` with 0 failed items → `ok`; `done` with failed items or items left for time/budget → `partial`; `failed` or `cancelled` → `failed`; `skipped` → `skipped` with `skip_reason`. Runs of every trigger count, including `verify` and `import` (ark-sync.md trigger enum `manual | scheduled | cloud | mode_online | install | verify | import`); the trigger itself travels in `ready.last_sync.trigger`.
- **`localai.ark.sync` job result** is the start-time result of §3.7 (`{sync_run_id, started, plan}`), reported as soon as the run starts; progress and the final outcome arrive in later snapshots (`sync`, `ark`), never as a second job result.
- The EAB heartbeat itself carries only the `vitals.extra` summary keys ([agent.md](agent.md) §4.8.3); the `ark` and `drill` blocks travel only in this snapshot.

Rules: `node_id` must equal the bound node's id (else `400 LAI-CLD-004`). The snapshot never contains a key, a token, a tailnet IP, a LAN IP or a path under `/home`. The cloud re-checks with a scrubber regex (the redaction patterns of [security-model.md](../architecture/security-model.md) §5) and refuses the whole snapshot with `400 LAI-CLD-008 reason: "secret_like_value"` if any string matches.

**As built (CLOUD-06, #80).** Code: `contracts/state.schema.ts`, `contracts/scrubber.ts`, `node-link/state.service.ts` (with the pure `state.projection.ts`, the typed errors `state.errors.ts`, the no-op alert hook `state.hooks.ts`) and the route `POST state` in `node-link/node-link.controller.ts`. Differences and details to know:

- **The schema is the listing above, with one change.** `ReadyReportV1.counts` is `z.partialRecord(<status enum>, int)`: Zod 4's `z.record(<enum>, ...)` demands every enum key, and a READY.json lists only the statuses it has. `ReadyReportV1` and `DrillResultsV1` are exported and declared above the snapshot. The exact bodies are the shared fixtures `test/fixtures/localai/state.ai-n3.json` (the full snapshot of one node: online, two roots with one READY report and one `ready: null`, a drill with its full results, five models including a known-but-absent one), `state.lab-3-nodes.json` (an array of three snapshots: `ai-n1` online, `ai-n2` in offline mode on battery with `serve_pattern: none`, `ai-n3`) and `state.response.json` (the `200` body). The agent's `testdata/state.v1.schema.json` is the JSON Schema form of the same contract, and AGENT-17 replays these three files in its conformance tests.
- **Order of checks.** (1) the scrubber over the **raw** body, (2) Zod, (3) `node_id` against the bound node, (4) one transaction. The spec text above says parse then scrub; the scrubber runs first so that a secret-looking value in a field Zod would reject (an invalid enum value is echoed as `received` in a Zod issue) or in an unknown key is refused as `secret_like_value` and never travels back in the `400`. Issues are returned as `{ path, code }` only (`details.issues`, at most 20): never a message, a received value or an unknown key name (an `unrecognized_keys` issue names the object).
- **Scrubber.** Walks every string value **and every object key** (a drill check's `details` is a free-form record) of the raw body, iteratively with a depth cap of 24 (a deeper body is `400` `reason: "schema"`, issue `too_large`, never a stack overflow). Patterns: `lat_`/`nod_`/`pat_`/`lai_` plus 20 or more token characters (word boundary, so `localai_*` is fine), 64 hex characters, IPv4 in the tailnet range (second octet 64 to 127), RFC 1918 IPv4 (10/8, 172.16/12, 192.168/16) and `/home/` paths. An address is matched as a whole dotted quad, so a version such as `10.1.2.3.4` is not a hit. A hit refuses the whole snapshot with `400 LAI-CLD-008`, `details: { code, reason: "secret_like_value", path }`; the log line has the node id, the rule name and the JSON path (a free-text key shows as `<key>`), never the value.
- **Refusals.** `400 LAI-CLD-008` `reason: "schema"` (Zod), `400 LAI-CLD-008` `reason: "secret_like_value"`, `400 LAI-CLD-004` `reason: "node_id_mismatch"`, `409 LAI-CLD-008` `reason: "stale_seq"`. A node retired between the guard and the transaction is `403 LAI-CLD-035` `node_retired`. All are EAB's error body with the code in `details`.
- **Body limit 256 KiB** is a Fastify route option set by an `onRoute` hook (`node-link/node-link.body-limits.ts`, installed by `createApp()` in `src/main.ts` before the app initialises, because Nest has no per-route decorator for it). Over the limit Fastify answers `413` before the body is parsed or buffered, for a declared `content-length` and for a chunked body. A spec that builds the app itself must call `registerNodeLinkBodyLimits(...)` before `app.init()`.
- **Ordering and the row lock.** The transaction first touches the node row (`UPDATE localai_nodes SET updated_at = now() WHERE id = ...`, a Prisma `updateMany`), which takes the row lock, then reads `lastState`/`lastStateSeq`/`mode`/`phase`. Two snapshots of one node therefore serialize, and of eight identical concurrent `seq` exactly one is accepted. No raw SQL (the conformance suite allowlists raw SQL by file). Accept when the stored `lastState.boot_id` differs from `boot_id`, or when `seq` is greater than `lastStateSeq`; `lastStateSeq` is a `BigInt` column.
- **Storage.** `lastState` is the snapshot verbatim, except that a root whose `ready` is `null` takes the previous non-null `ready` of the same root id (carried over per root id, not per position; a root the agent stopped reporting is dropped). `lastStateAt` is the API's clock. `mode`, `connectivity`, `profile`, `agentVersion`, `catalogVersion` and `backend` (`llama.backend`) are copied to the columns.
- **Model projection.** Every model in `models[]` is a row of `localai_node_models` (a model that is known but not downloaded is a row with `present = false`); a stored row whose id is absent from the snapshot is deleted. Only rows whose projected fields changed are written, so `updated_at` means "last changed" and the event flag `modelsChanged` is true exactly when a row was created, changed or deleted.
- **After the commit**, outside the transaction: `localai.node.state` is published once per accepted snapshot on the event bus (`{ nodeId: "ai-n3", modelsChanged, modeChanged }`, `nodeId` is the lab node id). `modeChanged` is true when the stored `mode` column was set and differs; the very first snapshot is not a mode change. On a mode change the service writes the audit row `localai.node.mode_changed` (`targetType: localai_node`, `targetId` the lab node id, `meta: { from, to }`, `actorUserId` the owner of the node credential, the same actor as the binding guard's rows) and calls `LOCALAI_ALERT_REEVALUATOR.reevaluateNode(nodeId)`, a port with a no-op default that CLOUD-15 binds in `NodeLinkModule`. A failing bus, audit or hook is logged by error name and never refuses the accepted snapshot.
- **Never logged.** The body, any value from it, and the bearer. An accepted snapshot logs the node id, `seq`, the model count and the two flags at debug level.
- **Tests.** Unit: `contracts/state.schema.spec.ts`, `contracts/scrubber.spec.ts`, `node-link/state.projection.spec.ts`, `node-link/state.service.spec.ts`, `node-link/node-link.body-limits.spec.ts`; HTTP over a mocked database: `test/localai/node-link-state-routes.spec.ts`; real SQL: `test/localai/node-link-state.db.spec.ts` (kept apart from `node-link.db.spec.ts` so the sibling cloud issues can extend that file without conflicts). Secret-looking test strings are built at test time (`test/localai/state-fixtures.ts`), never literals.

#### `GET /api/nodes/{id}/localai/catalog`
Serves the approved catalog, or `catalogPin` if set. Honours `If-None-Match` (the `bundleSha256` as a strong ETag) → `304`.

```json
{ "data": {
    "version": "2026.11.02-1",
    "bundleSha256": "<64 hex>",
    "approvedAt": "2026-11-02T14:00:00.000Z",
    "files": [
      { "path": "CATALOG_VERSION", "sha256": "<64 hex>", "content": "2026.11.02-1\n" },
      { "path": "models.yaml",     "sha256": "<64 hex>", "content": "schema_version: 1\nmodels:\n  …" },
      { "path": "ark.yaml",        "sha256": "<64 hex>", "content": "…" },
      { "path": "profiles/small-8gb.yaml", "sha256": "<64 hex>", "content": "…" }
    ] } }
```
`404 { details: { code: "LAI-CLD-013", reason: "no_approved_catalog" } }` when none is approved: the agent falls back to priority 2/3 of [catalog.md](catalog.md) §1. The agent verifies each `sha256`, writes the files to a temp dir, runs its own lint and only then swaps `/etc/localai/catalog/` ([catalog.md](catalog.md) §1; a rejection is `LAI-CAT-002`).

As built (CLOUD-10, #84): the route is `apps/cloud/apps/api/src/localai/node-link/catalog.controller.ts` (`@NodeLinkAuth()`, declared by `CatalogModule`, which imports `NodeLinkModule` for the guard). `files` are in the order `CATALOG_VERSION`, `models.yaml`, `ark.yaml`, then `profiles/*` by name. The `ETag` (`"<bundleSha256>"`) and `Cache-Control: private, no-cache` are on the `200` and on the `304`; `If-None-Match` is compared strongly (a `W/` validator never matches, `*` matches, a list is searched), and a `304` has an empty body (the handler answers through the reply, because the platform's `{ data, meta }` interceptor would otherwise wrap even an empty result). A `catalogPin` is served whatever the version's status, with `approvedAt` `null` unless it was approved; a pin naming a version that does not exist is `404` with `reason: "catalog_pin_not_found"` (the approved catalog is never substituted for an explicit pin). The `404` body is `{ message, details: { code: "LAI-CLD-013", reason } }` with `reason` `no_approved_catalog` or `catalog_pin_not_found`. Because the ETag is the bundle digest, a pinned node and an unpinned node have different ETags and each gets its own `304`.

#### `POST /api/nodes/{id}/localai/key`
The agent reports its llama-server API key: at enrollment, after every key rotation (local `aictl key rotate` or the `localai.key.rotate` command), whenever the `config` handshake answers `keyReported: false`, and on `aictl cloud resend-key`. The call is idempotent.

```json
{ "key": "<64 lowercase hex>", "fingerprint": "ab12…9f3c", "rotated_at": "2026-11-02T15:04:05Z" }
```
Validated with `z.object({ key: z.string().regex(/^[0-9a-f]{64}$/), fingerprint: z.string().regex(/^[0-9a-f]{4}…[0-9a-f]{4}$/), rotated_at: Iso }).strict()`; the server recomputes the fingerprint from `key` and refuses a mismatch (`400`). Stored with `CredentialsService.setSecret('localai_node_llama_key', <nodeId>, key)` (§3.8). Answers `200 { data: { keyHint: "ab12…9z0f", storedAt } }`. The body is excluded from request logging by the route config (`logLevel: 'warn'` and a `preSerialization` that never echoes it) and the value never appears in an error message.

#### Command plane: `POST /api/nodes/{id}/localai/commands/…`
Wire shapes deliberately mirror EAB's `claim` / `renew` / `result` / `failure` (same field names, same 409 meaning), so the agent's EAB client is reused with a different path prefix ([agent.md](agent.md) §4.8).

`POST …/commands/claim`
```json
{ "types": ["status.refresh", "model.load", "model.unload", "model.set_default", "model.download", "ark.sync",
            "ark.verify", "mode.offline", "key.rotate", "service.restart", "logs.tail", "catalog.refresh"],
  "limit": 1, "waitMs": 25000 }
```
`types` is `node.yaml: cloud.allow` (names **without** the `localai.` prefix; the server prepends it). The example is an ark laptop whose operator removed `model.delete` and `agent.update`; the default `cloud.allow` is all 14 types, the list in [agent.md](agent.md) §3.4 and [security-model.md](../architecture/security-model.md) §4. `limit` is clamped to 1. `waitMs` is clamped to `[0, claimWaitMaxMs]`; the server holds the request until a matching command is queued for this node or the wait ends (long poll; §4.4). Response:
```json
{ "data": { "jobs": [ {
    "job": { "id": "6b3c…", "type": "localai.model.load", "attempts": 1, "createdAt": "2026-11-02T15:04:00.000Z" },
    "params": { "model_id": "qwen3.6-35b-a3b-q4km", "wait": true, "unload_others": true, "set_default": false },
    "claimToken": "1f0e…", "renewIntervalMs": 20000, "leaseExpiresAt": "2026-11-02T15:05:00.000Z", "traceparent": null } ] } }
```
An empty `jobs` array is the normal answer (same key as EAB's claim response).

`POST …/commands/{cmdId}/renew` `{ "claimToken": "1f0e…" }` → `200 { data: { leaseExpiresAt } }` or `409`.
`POST …/commands/{cmdId}/result` `{ "type": "localai.model.load", "claimToken": "1f0e…", "result": { … } }` → `200 { data: { status: "succeeded" } }`, `409` (lease lost), `400` (type mismatch or result fails the type's schema; Zod issues in `details`).
`POST …/commands/{cmdId}/failure` `{ "claimToken": "1f0e…", "error": "LAI-CAT-005: Model does not fit in free VRAM" }` (EAB's `nodeJobFailureSchema` shape: `error` is a string ≤ 2000 chars; `rateLimited`/`retryAfterMs` are accepted and ignored) → `200 { data: { status: "failed" } }` or `409`. The server splits a leading `^(LAI-[A-Z]{2,3}-\d{3}):\s*` into `localai_commands.error.code`, keeps the rest (scrubbed, ≤ 500 chars) as `error.message`; no code → `LAI-CLD-012`.

`409` always means "the server moved on; drop the work". The agent never retries a 409.

### 3.7 Command types: Zod schemas for params and result

File: `apps/api/src/localai/contracts/commands.ts`. One registry entry per type. The Go agent has a strict decoder per type ([agent.md](agent.md) §4.8.6) and executes each through the same code path as the agent API route named below. Unknown keys are rejected on both sides (`.strict()` / `DisallowUnknownFields`).

```ts
import { z } from 'zod';
import { ModelId, ContentId, RootId } from './state.schema';
const Iso = z.string().datetime({ offset: true });
const KeyHint = z.string().regex(/^[0-9a-f]{4}…[0-9a-f]{4}$/);
const RunId = z.string().min(1).max(64);
const Semver = z.string().regex(/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/);

export const COMMANDS = {
  'localai.status.refresh': {            // agent: push a fresh snapshot now (POST …/localai/state), re-report key if needed
    params: z.object({}).strict(),
    result: z.object({ seq: z.number().int().min(0), reported_at: Iso }).strict(),
  },
  'localai.model.load': {                // agent API: POST /models/{id}/load
    params: z.object({
      model_id: ModelId,
      wait: z.literal(true).default(true),          // the command always waits for ready (bounded by the lease, §4.4)
      unload_others: z.boolean().default(true),
      set_default: z.boolean().default(false),
    }).strict(),
    result: z.object({
      model_id: ModelId, status: z.literal('loaded'),
      load_ms: z.number().int().min(0),
      unloaded: z.array(ModelId).max(16),
      vram_used_mib: z.number().int().min(0).nullable(),
      is_default: z.boolean(),
    }).strict(),
  },
  'localai.model.unload': {              // POST /models/{id}/unload
    params: z.object({ model_id: ModelId }).strict(),
    result: z.object({ model_id: ModelId, status: z.literal('unloaded'), was_loaded: z.boolean() }).strict(),
  },
  'localai.model.set_default': {         // PUT /models/default
    params: z.object({ model_id: ModelId }).strict(),
    result: z.object({ model_id: ModelId, previous: ModelId.nullable() }).strict(),
  },
  'localai.model.download': {            // POST /models/{id}/download (queues on the sync engine; returns at once)
    params: z.object({ model_id: ModelId, roots: z.array(RootId).min(1).max(9).optional() }).strict(),
    result: z.object({
      model_id: ModelId,
      already_present: z.boolean(),
      sync_run_id: RunId.nullable(),                // null when already_present
      bytes_planned: z.number().int().min(0),
    }).strict(),
  },
  'localai.model.delete': {              // DELETE /models/{id}  (cloud = admin; the agent applies keep-last, ark-sync.md §4.10)
    params: z.object({
      model_id: ModelId,
      roots: z.array(RootId).min(1).max(9),
      confirm: z.string(),                          // must equal model_id (typed confirmation in the UI); checked by server AND agent
    }).strict().refine((p) => p.confirm === p.model_id, { message: 'confirm must equal model_id', path: ['confirm'] }),
    result: z.object({
      model_id: ModelId,
      deleted_roots: z.array(RootId).max(9),
      kept_roots: z.array(z.object({ root: RootId, reason: z.enum(['last_verified_copy', 'loaded', 'not_present']) }).strict()).max(9),
      freed_bytes: z.number().int().min(0),
    }).strict(),
  },
  'localai.ark.sync': {                  // POST /ark/sync (starts; progress arrives in snapshots)
    params: z.object({
      roots: z.array(RootId).min(1).max(9).optional(),
      items: z.array(ContentId).min(1).max(200).optional(),
      dry_run: z.boolean().default(false),
    }).strict(),
    result: z.object({
      sync_run_id: RunId,
      started: z.boolean(),                         // false = a run was already in progress; sync_run_id is that run
      plan: z.object({ items_to_download: z.number().int().min(0), bytes_to_download: z.number().int().min(0), items_skipped_budget: z.number().int().min(0) }).strict(),
    }).strict(),
  },
  'localai.ark.verify': {                // POST /ark/verify (starts)
    params: z.object({ roots: z.array(RootId).min(1).max(9).optional(), items: z.array(ContentId).min(1).max(200).optional() }).strict(),
    result: z.object({ verify_run_id: RunId, started: z.boolean(), items_planned: z.number().int().min(0) }).strict(),
  },
  'localai.mode.offline': {              // POST /mode {target: offline}; agent reports the result BEFORE dropping the link
    params: z.object({ reason: z.string().max(200).optional() }).strict(),
    result: z.object({ previous_mode: z.enum(['online', 'offline', 'survival']), mode: z.literal('offline') }).strict(),
  },
  'localai.key.rotate': {                // POST /key/rotate; the agent pushes the new key (§3.6, `POST …/key`) before reporting this result; `cloud_synced: false` means the push failed and will be retried (LAI-CLD-060)
    params: z.object({}).strict(),
    result: z.object({ fingerprint: KeyHint, rotated_at: Iso, cloud_synced: z.boolean() }).strict(),
  },
  'localai.service.restart': {           // POST /services/{name}/restart
    params: z.object({ service: z.enum(['llama', 'kiwix', 'whisper']) }).strict(),
    result: z.object({ service: z.enum(['llama', 'kiwix', 'whisper']), active_state: z.enum(['active', 'activating', 'failed']), restarted_at: Iso }).strict(),
  },
  'localai.logs.tail': {                 // GET /logs?unit=&lines=  (secrets scrubbed by the agent; the cloud scrubs again)
    params: z.object({
      unit: z.enum(['agent', 'llama', 'kiwix', 'whisper', 'sync', 'drill']),        // = localai-<unit>.service
      lines: z.number().int().min(1).max(500).default(200),
    }).strict(),
    result: z.object({
      unit: z.string().max(64),
      lines: z.array(z.string().max(2000)).max(500),
      truncated: z.boolean(),
    }).strict(),
  },
  'localai.catalog.refresh': {           // POST /catalog/refresh (fetches GET …/localai/catalog)
    params: z.object({}).strict(),
    result: z.object({
      catalog_version: z.string().regex(/^\d{4}\.\d{2}\.\d{2}-\d+$/),
      changed: z.boolean(),
      accepted: z.boolean(),
      rejected_code: z.string().regex(/^LAI-[A-Z]{2,3}-\d{3}$/).nullable(),
    }).strict(),
  },
  'localai.agent.update': {              // agent self-update (agent.md §4.10): GitHub release, sha256 + minisign, rollback
    params: z.object({ version: Semver }).strict(),
    result: z.object({
      from_version: Semver, to_version: Semver,
      status: z.enum(['updated', 'already_current', 'rolled_back']),
    }).strict(),
  },
} as const;
export type CommandType = keyof typeof COMMANDS;
```

Per-type policy (`apps/api/src/localai/commands/command-policy.ts`):

| Type | Permission | Lease (ms) | Hard cap | Queued TTL | Max attempts | Dedup subject | UI confirmation |
|---|---|---|---|---|---|---|---|
| `status.refresh` | lab:operate | 30 000 | 300 s | 2 min | 1 | `-` (one active per node) | none |
| `model.load` | lab:operate | 60 000 (renewed) | 600 s | 15 min | 1 | `model_id` | none |
| `model.unload` | lab:operate | 30 000 | 300 s | 15 min | 1 | `model_id` | none |
| `model.set_default` | lab:operate | 30 000 | 300 s | 15 min | 1 | `-` | none |
| `model.download` | lab:operate | 30 000 | 300 s | 24 h | 1 | `model_id` | shows planned size |
| `model.delete` | **lab:admin** | 60 000 | 600 s | 15 min | 1 | `model_id` | type the model id |
| `ark.sync` | lab:operate | 30 000 | 300 s | 24 h | 1 | `-` | dry-run preview offered |
| `ark.verify` | lab:operate | 30 000 | 300 s | 24 h | 1 | `-` | none |
| `mode.offline` | **lab:admin** | 30 000 | 300 s | 10 min | 1 | `-` | warns: "Only someone at the node can bring it back online" |
| `key.rotate` | **lab:admin** | 60 000 | 600 s | 10 min | 1 | `-` | lists clients that must update (Open WebUI uses the gateway, so usually none) |
| `service.restart` | lab:operate | 60 000 | 600 s | 10 min | 1 | `service` | none |
| `logs.tail` | lab:operate | 30 000 | 300 s | 2 min | 1 | `unit` | none |
| `catalog.refresh` | lab:operate | 60 000 | 600 s | 1 h | 1 | `-` | none |
| `agent.update` | **lab:admin** | 300 000 | 900 s | 24 h | 1 | `-` | shows from → to version |

**Hard cap** = the longest a claim may be renewed, counted from `claimed_at` (`hardCap` in §4.4 renew): lease × 10 by default, except `model.load` (600 s, a full load at maximum context) and `agent.update` (900 s: download, verify, restart and the 60 s health check). Renewing past the hard cap answers `409` and the sweeper then fails the command with `LAI-CLD-059`.

Commands are **at most once** (max attempts 1). A lost lease ends the command `failed` with `LAI-CLD-059`; the operator re-issues it. Rationale: replaying `model.load` or `mode.offline` after an unknown outcome is more surprising than asking.

### 3.8 Node llama keys in the EAB credential store

Purpose (`apps/api/src/platform/credentials/credential-purposes.ts`):
```ts
export const LOCALAI_NODE_LLAMA_KEY_PURPOSE: CredentialPurposeDef = {
  purpose: 'localai_node_llama_key',     // PERMANENT; no ':' allowed in a purpose
  owner: 'localai',
  label: 'Local AI node llama-server API key',
  tiers: ['system'],                     // deployment scope
};
export const APP_CREDENTIAL_PURPOSES = [LOCALAI_NODE_LLAMA_KEY_PURPOSE] as const;
```
Address: `(purpose = 'localai_node_llama_key', name = '<node-id>')`, e.g. `('localai_node_llama_key', 'ai-n3')`. [security-model.md](../architecture/security-model.md) §2 uses the same address (EAB credential names may not contain `:`).

- Written only by `POST /api/nodes/{id}/localai/key`. Read only by the gateway's upstream client (`CredentialsService.getSecret`, server-side, never from a controller) and by the add-node checklist's "gateway test" check (§4.8).
- `localai_nodes.keyHint` holds `first4…last4` for display.
- Retiring a node calls `deleteSecret`. Rotating overwrites. The gateway caches a decrypted key in memory for at most 60 s, and drops the cache entry on the `localai.node.key_reported` event.
- `SECRETS_ENCRYPTION_KEY` loss = every node key unreadable: the gateway answers `503 LAI-GW-050` and each node re-reports its key on its next `status.refresh` (the UI has a "Ask all nodes to re-send keys" button that queues `status.refresh` on every enrolled node). Back up `SECRETS_ENCRYPTION_KEY` in the owner's password manager and on the ark drive's `inventory.local.yaml`.

**As built (CLOUD-07, #81).** Code: `apps/cloud/apps/api/src/localai/keys/` (`node-key.service.ts`, `node-key.events.ts`, `resend-keys.service.ts`, `node-keys.module.ts`), the route in `node-link/key.controller.ts`, the wire contract in `contracts/key.schema.ts`, the purpose in `src/platform/credentials/credential-purposes.ts`. Details to know:

- **The route** carries `@NodeLinkAuth()` like every node route and answers `200` (not Nest's default `201`). The body is typed `unknown` and validated by `parseNodeKeyRequest` (strict schema, then the fingerprint recomputed from the key). A refusal is `400` with `details: { reason: "invalid" | "fingerprint_mismatch", issues: [{ path, code }] }`: **path and Zod code only**, never a received value (Zod's own messages and `received` fields are dropped), and it carries no Local AI doctor code. A malformed JSON body gets Fastify's generic message, which does not quote the input (pinned by a test).
- **No `logLevel: 'warn'` and no `preSerialization` hook.** The app has no request-body logger (Fastify request logging is off, the only logger is Nest's, and the logger is not Pino), so there is nothing to exclude the body from, and the answer is built from stored values (`keyHint`, `storedAt`) and parsed through the strict `NodeKeyResponseSchema`, which cannot carry a `key` field. The executable form of the rule is a set of tripwires that capture `process.stdout`, `process.stderr` and `console` while the whole flow runs (accepted, refused, malformed, wrong node, rotate, delete) and fail on any 64-hex string in a log, a response body, an error, an audit row or an event (`node-key-routes.spec.ts`, `node-key.db.spec.ts`, `node-key.service.spec.ts`). If a request logger is ever added (a Pino transport), it must redact `key` and `req.body` for `POST /api/nodes/*/localai/key` and these tests must keep passing with it on.
- **Store order.** `setSecret` first, then the cache entry is dropped, then `localai_nodes.keyHint` / `keyReportedAt` (server time = `storedAt`; `rotated_at` is validated and not stored), then the `localai.node.key_reported` audit row and event, both after the write and both unable to refuse a key that is already stored (a failure is logged by error name). The reason is `enroll` when the node has no `keyHint`, `rotate` when the new hint differs, `refresh` otherwise.
- **`NodeKeyService.getKey(nodeId)`** takes the lab node id, returns the plaintext or `null`, and throws `NodeKeyUnreadableError` (message: the node id only, no cause) when a key is stored but will not decrypt, which the gateway maps to `503 LAI-GW-050`. It caches the decrypted key for 60 s (`NODE_KEY_CACHE_TTL_MS`); a missing key and a failed read are never cached. The cache is dropped by this replica's own write, by `deleteKey`, and by the `localai.node.key_reported` event (so another replica's write also evicts it). The event payload is `{ nodeId, keyHint, reason }`, never the key.
- **`NodeKeyService.deleteKey(nodeId)`** deletes the secret, clears `keyHint` and `keyReportedAt`, drops the cache entry, is idempotent and publishes no event. The retire route (CLOUD-14) calls it.
- **Resend keys.** `POST /api/localai/nodes/resend-keys` (`lab:admin`, `nodes/nodes.controller.ts`) queues `status.refresh` with source `system` on every node whose phase is `enrolled` (never `pending`, never `retired`), in node-id order, and returns `200 { data: { commandIds } }`. It queues through the port `LOCALAI_COMMAND_ENQUEUER` (`commands/command-enqueuer.ts`). As built (CLOUD-08): `CommandEnqueuerModule` binds the token to `CommandsService` (`useExisting`; the interim `PrismaCommandEnqueuer` is gone), so the row is the one section 4.4 describes: type `localai.status.refresh`, dedup key `status.refresh:-`, a 2 minute queued TTL and the actor, the active row returned when `localai_commands_active_dedup` refuses a duplicate, and `localai.command.enqueued` published so a held claim wakes. The action writes no audit row of its own; each command row records `requestedById` and `source`.
- **Purpose owner.** The purpose names owner `localai`, so `test/conformance.spec.ts` declares it as an app owner (`credentials: { appOwners: ['localai'] }`); the platform's `credentials` conformance suite refuses an owner that is neither a platform slice nor declared.
- **Needs the `credentials` slice.** `NodeKeysModule` imports the platform's `CredentialsModule`, so `packages/shared/slices.json` must keep `credentials` enabled (it is).
- **Tests.** Unit: `contracts/key.schema.spec.ts`, `keys/node-key.service.spec.ts`, `keys/node-key.events.spec.ts`, `keys/resend-keys.service.spec.ts`, `commands/command-enqueuer.spec.ts`; HTTP over a mocked database with the real cipher: `test/localai/node-key-routes.spec.ts`; real SQL: `test/localai/node-key.db.spec.ts` (a separate file from `node-link.db.spec.ts` so parallel work does not collide). The request fixture `test/fixtures/localai/key.request.json` holds placeholders that each test replaces with a key built at runtime.

### 3.9 UI-facing HTTP (`/api/localai/*`)

Session or `pat_` auth via `@Auth({ permissions: [...] })` on every route; bodies validated with Zod. Path `:nodeId` is the lab node id (`ai-n3`).

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/api/localai/nodes` | lab:read | Cards for the overview: node, health (§4.5), mode, connectivity, loaded models, ark colour, alert counts |
| GET | `/api/localai/nodes/:nodeId` | lab:read | Detail: everything above + `lastState` (tailnet IP removed unless lab:admin and `?reveal=tailnet`) |
| POST | `/api/localai/nodes` | lab:admin | Wizard step 1: create a `pending` node, mint and bind a `nod_`, return it **once** + install one-liner (§4.8) |
| PATCH | `/api/localai/nodes/:nodeId` | lab:admin | `displayName`, `publicHost`, `servePattern`, `tailnetMachine`, `tailnetIp`, `gatewayEnabled`, `gatewayPriority`, `catalogPin` |
| POST | `/api/localai/nodes/:nodeId/credential` | lab:admin | Rotate: revoke the bound credential, mint a new one (shown once), bind it |
| POST | `/api/localai/nodes/:nodeId/retire` | lab:admin | Revoke credential, delete key, phase `retired`, cancel queued commands. Body `{ "confirm": "<nodeId>" }` |
| POST | `/api/localai/nodes/resend-keys` | lab:admin | "Ask all nodes to re-send keys" (§3.8, LAI-CLD-032): enqueues `status.refresh` (which re-reports the key, §3.7) on every enrolled node; returns `200 { data: { commandIds: ["…"] } }` (dedup rules apply, so an active one is reused) |
| GET | `/api/localai/nodes/:nodeId/checklist` | lab:admin | Publish checklist with rendered snippets (§4.8) |
| POST | `/api/localai/nodes/:nodeId/checklist/:step` | lab:admin | `{ "action": "mark_done" | "mark_todo" | "check" }` |
| GET | `/api/localai/nodes/:nodeId/commands?status=&page=` | lab:read | Command history |
| POST | `/api/localai/nodes/:nodeId/commands` | per type (§3.7) | `{ "type": "model.load", "params": {…} }` → `201 { data: Command }` or `200` with the active duplicate |
| GET | `/api/localai/commands/:id` | lab:read | One command (UI polls it every 2 s while active) |
| DELETE | `/api/localai/commands/:id` | lab:operate (lab:admin for admin types) | Cancel a `queued` command; `409` if already running |
| GET | `/api/localai/catalog/versions` | lab:read | List |
| GET | `/api/localai/catalog/versions/:version` | lab:read | Files + lint result + diff vs approved (`?diff=approved`) |
| POST | `/api/localai/catalog/import` | lab:admin | `{ "tag": "catalog-2026.11.02-1" }` → `202` and the import job (the draft appears when it finishes, then its lint is queued); `409` when that version exists (§4.6 As built) |
| POST | `/api/localai/catalog/versions` | lab:admin | `{ "baseVersion": "…", "files": [{path, content}] , "version": "2026.11.03-1", "notes": "…", "source": "editor" \| "upload" }` → `201` draft, lint queued (§4.6 As built) |
| POST | `/api/localai/catalog/versions/:version/lint` | lab:admin | Re-run lint |
| POST | `/api/localai/catalog/versions/:version/approve` | lab:admin | Only from `valid`; previous approved → `superseded`; optional `{ "notifyNodes": true }` queues `catalog.refresh` on each `healthy`/`degraded` enrolled node (§4.6 As built) |
| GET | `/api/localai/alerts?state=active` | lab:read | Alerts |
| POST | `/api/localai/alerts/:id/ack` | lab:operate | Acknowledge (stops repeat notifications; does not clear) |
| GET | `/api/localai/audit?nodeId=&action=&page=` | lab:admin | `audit_events` where `action LIKE 'localai.%'` |
| GET | `/api/localai/settings` ; PUT | lab:read ; per key prefix | The `localai` settings namespace (§3.10). `PUT` checks the permission **per key prefix**: `gateway.*` keys (and gateway model policies, [gateway.md](gateway.md) §3.7) need `gateway:admin`; every other key needs `lab:admin`. A body mixing prefixes needs both. Built in GW-02 ([gateway.md](gateway.md) §10). |

Command `POST` example:
```http
POST /api/localai/nodes/ai-n2/commands
{ "type": "model.load", "params": { "model_id": "qwen3.6-35b-a3b-q4km", "unload_others": true } }
```
```json
{ "data": { "id": "6b3c…", "nodeId": "ai-n2", "type": "localai.model.load", "status": "queued",
            "params": { "model_id": "qwen3.6-35b-a3b-q4km", "wait": true, "unload_others": true, "set_default": false },
            "expiresAt": "2026-11-02T15:19:00.000Z", "createdAt": "2026-11-02T15:04:00.000Z" } }
```
Refusals: `404` unknown node; `409 LAI-CLD-035` node is `retired` or `pending`; `422 LAI-CLD-034` node is in `offline`/`survival` mode per last snapshot (the command would expire unclaimed; the UI explains "This computer is in Offline mode. Only someone at it can switch it back."); `422` when `model_id` is not in the node's last snapshot `models` (download it first) except for `model.download`; `403` permission.

**As built (CLOUD-09, #83).** Code: `apps/cloud/apps/api/src/localai/commands/` (`commands.controller.ts`, `commands.service.ts` `request` and `cancel`, `commands-query.service.ts`, `commands-audit.listener.ts`, `command-refusals.ts`, `command-failure-fix.ts`, `command-notifier.ts`, `command-view.ts`, `command-api.schemas.ts`). Details and differences:

- **Permissions.** `POST` and `DELETE` are guarded by `anyPermissions: [lab:operate, lab:admin]` (a viewer is 403 before anything else), then `CommandsService` applies the EXACT permission of the command's type from `COMMAND_POLICY`: `lab:operate` for the ten operate types, `lab:admin` for `model.delete`, `mode.offline`, `key.rotate` and `agent.update`. `lab:admin` does not stand in for `lab:operate` (section 3.4 grants them separately; the lab owner holds both). `DELETE` needs the permission of the cancelled command's type (a type this build no longer knows needs `lab:admin`). `GET` routes need `lab:read`. The 403 body names the permission in `details.permission`.
- **Order of checks** in `request`: the type is in the closed registry (400) -> permission (403) -> params pass the type's strict schema (400, Zod issues in `details.issues`) -> node exists (404) -> node is `enrolled` (409 `LAI-CLD-035`, `details.phase`) -> last snapshot mode is not `offline`/`survival` (422 `LAI-CLD-034`, `details.mode`) -> model commands find the model on the node (422, `details.reason: "model_not_on_node"`) -> `enqueueForNode` (the CLOUD-08 insert and dedup). A node with no snapshot yet (mode `null`) takes commands.
- **"Not in the snapshot".** The check reads the `localai_node_models` projection of the last snapshot and requires the row to exist AND `present = true`: a model the snapshot lists but the node has not downloaded is refused too ("download it first"), since a catalog entry with `present: false` cannot be loaded, unloaded, made default or deleted. It applies to `model.load`, `model.unload`, `model.set_default` and `model.delete`; `model.download` is exempt.
- **Body.** `{ "type": "model.load" | "localai.model.load", "params": {} }`, strict; `params` defaults to `{}`. The answer always carries the prefixed type. `201` for a new command, `200` with the active duplicate. The command view is `{ id, nodeId (the ai-nN lab id), type, status, params, source, requestedById, attempts, expiresAt, createdAt, claimedAt, finishedAt, error }`; `GET /commands/:id` adds `result`. The history route leaves `result` out, orders newest first and answers `{ items, page, pageSize, total, totalPages }` (`page` from 1, `pageSize` 20 by default, at most 100; an unknown `status` or page is 400; the history of a retired node stays readable).
- **Source.** `ui` for a browser session or device token; `api` for a `pat_` caller; `desktop` only for a `pat_` caller that sends the header `X-LocalAI-Client: desktop` (a session cannot claim it). `CommandSource` gained `api` and `desktop`; the column is free text, so no migration.
- **Cancel.** One conditional write (`WHERE id = $id AND status = 'queued'`): 0 rows is 409 (`details.status` is the current status: `running`, `succeeded`, ...), so a claim that lands first wins. A cancelled row leaves the active statuses, which frees its dedup key. 404 for an unknown or non-uuid id.
- **Audit exactly once.** `localai.command.requested` is written by `CommandsService.request` for a NEW command only (an active duplicate writes nothing). Commands queued through the port `LOCALAI_COMMAND_ENQUEUER` (the resend-keys action; the catalog approval, which audits its own `requested` rows) do not pass through `request`, so no command is audited twice. `localai.command.cancelled` is written by `cancel`. `localai.command.settled` is written by `CommandsAuditListener`, which subscribes to the `localai.command.settled` bus event the command plane publishes once per settle (after the write committed): it reads the row (never `result`, so a `logs.tail` result cannot reach the audit table) and records `{ nodeId (the ai-nN id), type, status, errorCode? }` with the requester as actor. It is idempotent per command (it first looks for an existing settled row), so a redelivery or a second replica on the Postgres bus does not duplicate it; the check is a read, so two replicas racing in the same instant could still both write. The audit port takes scalar `meta` only, so `params` of `requested` and `cancelled` is a compact JSON string. Every audit write runs after the change it records and a failure is logged (error name only), never undone.
- **Notification.** For `failed` and `expired` commands that have a requester, the listener calls the port `LOCALAI_COMMAND_NOTIFIER` (`command-notifier.ts`) once with `{ recipientUserId, commandId, nodeId, nodeName, type, status, errorCode, fix, message }`, `message` being "{type} on {node} failed: {fix}" (type without the prefix, node by display name). The default is a no-op that logs ids and the code; CLOUD-15 registers `localai.command.failed` and binds the real notifier. The fix is `command-failure-fix.ts`: the registry fix for the codes the cloud stores itself (`LAI-CLD-012`, `015`, `030`, `057`, `058`, `059`) and for `LAI-CAT-005` and `LAI-LLM-011`; any other agent code shows the agent's own (scrubbed) message; neither gives a generic pointer to the node's page. The code is appended in parentheses.
- **Tests.** Unit: `commands.controller.spec.ts`, `command-refusals.spec.ts`, `command-failure-fix.spec.ts`, `commands-audit.listener.spec.ts`; service over the in-memory table: `test/localai/commands-request.spec.ts` (the 14-type by 3-role matrix, refusals and their order, dedup, audit, the cancel race); HTTP with the real guards over a mocked database: `test/localai/commands-api-routes.spec.ts`; real SQL, real sessions and PATs, a fake agent on the node routes: `test/localai/commands-api.db.spec.ts`.

### 3.10 Settings namespace `localai`

Registered before `SettingsModule.forRoot()` (`apps/api/src/localai/localai.settings.ts`), card in `apps/web/src/config/adminSections.tsx` gated on `lab:admin`.

| Key | Default | Meaning |
|---|---|---|
| `alerts.driveStaleDays` | 30 | Ark drive last synced longer ago than this → `ark.drive_stale` (warning) |
| `alerts.driveCriticalDays` | 90 | Ark drive last synced longer ago than this → `ark.drive_stale` escalates to critical (matches the node's READY red threshold, [ark-sync.md](ark-sync.md) §4.12.2) |
| `alerts.nodeStaleMinutes` | 10 | No accepted snapshot for this long while EAB health ≠ healthy → `node.stale` (offline/survival nodes excluded) |
| `alerts.nodeOfflineHours` | 24 | Worker node `offline` this long → `node.offline` (warning) |
| `alerts.catalogBehindDays` | 14 | Node on a catalog older than approved for this long |
| `alerts.repeatHours` | 24 | Re-notify an unacknowledged active alert after |
| `catalog.githubRepo` | `marinoscar/local-ai` | Source of `catalog-*` release tags |
| `agent.latestVersion` | null | Shown as "update available"; set from the agent releases page by an admin |
| `vps.stagingSuffix` | `""` | Phase 2 (§4.10) only: appended to the node label of a published host for tests, e.g. `-staging` → `ai-n3-staging.marin.cr`. Empty in production |
| `checklist.vpsPublicHostname` | `ai.marin.cr` | The name whose A record node hosts must match (§4.8 DNS check) |

### 3.11 UI pages (web, `apps/cloud/apps/web/src/localai/`)

Routes added to `App.tsx` inside the packaged shell; navigation entries in `apps/web/src/config/navigation`. All pages poll their API every 5 s while visible (no SSE in v1). Colours use MUI palette tokens; colour is never the only signal (each colour has a word and an icon). Shared node-card and status-chip components come from `@localai/ui` (`packages/ui`).

**Health and colour rules** (computed server-side in `GET /api/localai/nodes`, see §4.5):

| Chip | Green | Amber | Red | Grey |
|---|---|---|---|---|
| Health | EAB `healthy` and snapshot ≤ 2 min old | EAB `stale`, or snapshot 2–10 min old | EAB `offline` while mode was `online` | mode `offline`/`survival` (expected silence) or `pending` |
| Ark (per root) | t1 ok and t2 ok, drive ≤ 30 days | t1 ok, t2 partial/missing, or drive 31–90 days | t1 partial/missing, last sync failed, or drive > 90 days | no snapshot yet |
| Mode | online | offline | survival (shown purple-red with "Survival") | unknown |

**Lab overview** (`/lab`, lab:read)
```
┌ Local AI ─ Lab ─────────────────────────────────────────────── [Add node] (lab:admin) ┐
│ 3 nodes · 2 online · 1 offline mode · 2 alerts ▲                    Gateway: ● 2 nodes │
├───────────────────────────┬───────────────────────────┬────────────────────────────────┤
│ ai-n1  MS-S1 Max          │ ai-n2  MSI Stealth        │ ai-n3  HP ZBook                │
│ ● Healthy   ◆ Online      │ ● Healthy   ◆ Online      │ ○ Offline mode (expected)      │
│ Vulkan · 128 GB unified   │ CUDA · RTX 4080 12 GB     │ CUDA · RTX 2070 8 GB           │
│ Loaded:                   │ Loaded:                   │ Last seen 3 h ago              │
│  ▸ qwen3-coder-next-q4km★ │  ▸ qwen3.6-35b-a3b-q4km ★ │  ▸ qwen3.5-9b-q4km ★ (last)    │
│ VRAM ████████░░ 61 GB/96  │ VRAM █████████░ 11.2/12   │                                │
│ Ark: internal ● ready     │ Ark: internal ▲ tier 2    │ Ark: internal ● ready          │
│      drive    — none      │      drive    ✖ 41 days   │      drive    ● 6 days         │
│ Sync: ok 02:14 · Drill ✔  │ Sync: ✖ failed · Drill ✔  │ Sync: skipped (mode) · Drill ✔ │
│ agent 0.4.1 · cat 11.02-1 │ agent 0.4.1 · cat 11.02-1 │ agent 0.4.0 · cat 10.28-2 ▲    │
│ [Open]                    │ [Open]  2 alerts          │ [Open]                         │
└───────────────────────────┴───────────────────────────┴────────────────────────────────┘
 ★ = default model
```

**Node detail** (`/lab/nodes/:nodeId`), header plus six tabs:
```
┌ ← Lab   ai-n2 · MSI Stealth 17 ── ● Healthy ◆ Online ◇ connectivity: full ─────────────┐
│ profile cuda-12gb · agent 0.4.1 · llama f3586e66b-cuda · router up · models_max 1    │
│ [Refresh status]  [Go offline…] [Update agent 0.4.1 → 0.4.2…] (lab:admin)  4 s ago   │
├──[Models]──[Ark]──[Services]──[Logs]──[Keys]──[Settings]──────────────────────────────┤
│ Models                                                                                │
│ ┌────────────────────────────┬────────┬──────────┬──────────┬──────────────────────┐  │
│ │ Model                      │ State  │ On disk  │ Fits?    │ Actions              │  │
│ ├────────────────────────────┼────────┼──────────┼──────────┼──────────────────────┤  │
│ │ qwen3.6-35b-a3b-q4km ★     │ loaded │ ✔ verif. │ ✔        │ [Unload] [Default✓]  │  │
│ │ qwen3.5-9b-q4km            │ —      │ ✔ verif. │ ✔        │ [Load] [Set default] │  │
│ │ small-vlm-id               │ —      │ ✖        │ ✔        │ [Download 5.1 GB]    │  │
│ │ qwen3-coder-next-q4km      │ —      │ ✖        │ ✖ VRAM   │ (needs 49 GB)        │  │
│ └────────────────────────────┴────────┴──────────┴──────────┴──────────────────────┘  │
│ Running command: Load qwen3.5-9b-q4km … 42 s  [Cancel] (only while queued)            │
│ (lab:admin) row menu ⋮ → Delete files… (type the model id to confirm)                 │
└───────────────────────────────────────────────────────────────────────────────────────┘
```
- **Update agent…** (header, lab:admin): visible when `settings.agent.latestVersion` is set; enabled only when it is greater than the node's `agent_version` and the node is online and enrolled. Opens a confirmation "Update ai-n2 from 0.4.1 to 0.4.2? The agent restarts; models keep running.", then enqueues `agent.update` `{ version: <latestVersion> }` through `POST /api/localai/nodes/:nodeId/commands` (§3.7). This is the button LAI-CLD-029's fix names.
- **Ark tab:** one panel per root: tier chips, items missing/failed/skipped, bytes used/free bar, last sync and verify times, `drive_last_synced_at` with age; buttons **Sync now** (with **Preview (dry run)**), **Verify**; the last sync run result and error code with its plain fix (from [doctor-codes.md](doctor-codes.md)).
- **Services tab:** llama, kiwix, whisper, sync, drill: state chip, since, **Restart** (llama/kiwix/whisper only).
- **Logs tab:** unit picker (agent, llama, kiwix, whisper, sync, drill) + lines (50/200/500) + **Fetch** → queues `logs.tail`, shows a monospace viewer when it settles. A note: "Logs are cleaned of keys and tokens before they leave the computer." Results are kept 7 days (§4.4).
- **Keys tab:** llama key hint (`ab12…9z0f`), reported at, **Rotate key…** (lab:admin; confirmation lists what changes); node credential prefix, last used, **Replace credential…** (lab:admin) with the steps to re-enroll (`sudo aictl cloud enroll`). Never shows a full key.
- **Settings tab:** display name, public host, serve pattern, tailnet machine, tailnet IP (lab:admin only; masked `100.•••.•••.•••` with **Reveal**, which writes the audit event `localai.node.tailnet_revealed`), gateway enabled / priority, catalog pin, **Retire node…** (type the node id).

**Catalog** (`/lab/catalog`, lab:read; actions lab:admin)
```
┌ Catalog ──────────────────────────────────────────────────────── [Import release…] [New draft] ┐
│ Approved: 2026.11.02-1 (catalog-2026.11.02-1) · approved by Person A · 2 of 3 nodes on it     │
├──────────────┬──────────┬────────────┬─────────────┬────────────────────────────────────────────┤
│ Version      │ Source   │ Status     │ Lint        │                                            │
│ 2026.11.03-1 │ editor   │ draft      │ ✖ 2 errors  │ [Edit] [Lint] [Diff vs approved]           │
│ 2026.11.02-1 │ release  │ ● approved │ ✔           │ [Diff] [Push to nodes]                     │
│ 2026.10.28-2 │ release  │ superseded │ ✔           │ [Diff]                                     │
└──────────────┴──────────┴────────────┴─────────────┴────────────────────────────────────────────┘
 Editor: file tree (models.yaml, ark.yaml, profiles/*) │ YAML text area (monospace, no CDN editor) │
 lint panel: "LAI-CAT-011 models.yaml:models[3].source.revision must be a 40-hex commit"           │
 [Save draft] [Approve] (enabled only when status = valid)                                          │
```
The editor is a plain `<textarea>` with line numbers (no CodeMirror; the `telemetry` slice that brings it is off). Approving shows the diff and asks "Approve 2026.11.03-1 for all nodes?".

**Add-node wizard** (`/lab/nodes/new`, lab:admin) — see §4.8 for behaviour.
```
Step 1 of 3 · Describe the node
  Node id      [ai-n4      ]  (next free id suggested; never reused)
  Name         [Dell Precision (ai-n4)]
  Public host  [ai-n4.marin.cr]  (derived)
  Serve        (•) Pattern B  --https=443   ( ) Pattern A  --tcp=18080
  Tailnet name [dell-linux ]   Tailnet IP [100.___.___.___] (stored, admin-only)
  Profile hint [cuda-12gb ▾]  (installer confirms with `aictl profile suggest`)
                                                                    [Create node]
Step 2 of 3 · Install on the node
  ┌ Node credential (shown ONCE) ──────────────────────────────────────────────┐
  │ nod_••••••••••••••••••••••••••••••••••  [Copy]   Expires: never (revocable) │
  └─────────────────────────────────────────────────────────────────────────────┘
  On the new computer, open a terminal and run:
  ┌─────────────────────────────────────────────────────────────────────────────┐
  │ curl -fsSLO https://github.com/marinoscar/local-ai/releases/download/       │
  │   node-v0.4.1/install-node.sh                                               │
  │ sha256sum install-node.sh     # must print 3f9c…a1b2 (shown here)           │
  │ sudo bash install-node.sh --node-id ai-n4 \                                 │
  │   --cloud-url https://ai.marin.cr --enroll                    [Copy]         │
  └─────────────────────────────────────────────────────────────────────────────┘
  It asks for the credential above. Paste it and press Enter.
  Waiting for ai-n4 to contact the cloud …  ◌      (turns ✔ "Enrolled" automatically)
Step 3 of 3 · Publish it (VPS)                        [Publish automatically] (phase 2, disabled)
  [✔] 1. Node is on the tailnet (tailscale up)            mark done
  [ ] 2. DNS: ai-n4.marin.cr A → VPS                      [Check] [Show command]
  [ ] 3. Bootstrap HTTP vhost                             mark done [Show file]
  [ ] 4. Certificate issued                               [Check] [Show command]
  [ ] 5. Final HTTPS vhost (Pattern B)                    [Check] [Show file]
  [✔] 6. Node enrolled                                    (automatic)
  [✔] 7. llama key received by the cloud                  (automatic)
  [ ] 8. Gateway test: /v1/models through ai-n4           [Check]
```

**Alerts** (`/lab/alerts`, lab:read)
```
┌ Alerts ───────────────────────────────── filter: [Active ▾] [All nodes ▾] ───┐
│ ✖ critical  ai-n2  Ark drive not synced for 41 days     LAI-CLD-022   [Ack] │
│              Plug the AI-ARK drive into ai-n2 and press "Sync now".          │
│ ▲ warning   ai-n2  Last ark sync failed (LAI-SYN-031)   LAI-CLD-023   [Ack] │
│ ▲ warning   ai-n3  Not seen for 3 h (node is in Offline mode: no action)    │
│ ✖ critical  ai-n1  Monthly drill failed: LAI-DRL-020    LAI-CLD-024   [Ack] │
└──────────────────────────────────────────────────────────────────────────────┘
```

**Audit** (`/lab/audit`, lab:admin): table of time, actor (email or "node ai-n3"), action, target, summary; filters by node, action prefix, date range; rows link to the command or node.

#### As built: Lab overview API and page (CLOUD-11)

What `GET /api/localai/nodes` (lab:read) returns, and where the pieces live. Where this differs from the wireframe or the text above, this subsection wins.

- **Response.** `200 { data: { summary, nodes } }`. `nodes` holds one `NodeCardDto` per node that is not `retired`, in numeric id order (`ai-n2` before `ai-n10`). `summary` holds the header counts: `nodes` (listed), `online` (mode `online` and health `healthy` or `degraded`), `offlineMode` (mode `offline` or `survival`), `activeAlerts` (every active alert, including lab-wide ones that belong to no node) and `routable` (the "Gateway: N nodes" figure). A card never carries `tailnetIp`, a key hint or the raw snapshot. The Zod schema is `apps/api/src/localai/nodes/dto/node-card.ts`; the shared fixture `apps/api/test/fixtures/localai/lab-3-nodes.json` is proven against it by the API tests and rendered by the web tests.
- **Detail.** `GET /api/localai/nodes/:nodeId` returns the card fields plus `lastState` (the last snapshot, which the scrubber already keeps free of tailnet addresses). `tailnetIp` is present only for a holder of `lab:admin` who sends `?reveal=tailnet`; anyone else's flag is ignored. The reveal audit event `localai.node.tailnet_revealed` and its Settings UI belong to CLOUD-12 and are not written yet. A retired or unknown node answers `404`.
- **Where the logic lives.** `apps/api/src/localai/health/`: `derive-health.ts` (`deriveHealth`, `isRoutable`, `isGatewayRoutable`, exported for GW-04), `ark-colour.ts` (`arkColour`, `arkLabel`), `versions.ts` (the behind flags). All are pure and table-tested including the boundaries. The web app renders what the API returns and never recomputes it.
- **EAB health.** The stale window is `nodes.staleHeartbeatSeconds` from the platform's `nodes` settings (default 45 s). A node with no linked worker row counts as `offline`.
- **Thresholds.** `alerts.driveStaleDays` (30), `alerts.driveCriticalDays` (90) and `agent.latestVersion` are read from the `localai` namespace (§3.10) when it exists. Until the settings slice registers it, the documented defaults apply; a stored value that is not a positive integer falls back to its default field by field, and the critical threshold never falls below the stale one.
- **Ark rows.** One row per root in the snapshot: `internal` first, then drives by id. A root's kind comes from its id (`internal` or `ark-<8 hex>`). "Older than N days" counts whole days (30 d 23 h is still 30 days, so green). `drive_last_synced_at` is one value per node, so every `ark_drive` row uses it. When the snapshot has a drive time but no drive root, the card keeps one unplugged row (`rootId` `ark-drive`, `present: false`, "Drive not plugged in · last synced 6 days ago"). A node with no snapshot has one grey `internal` row, "no data yet". A plugged-in drive row is labelled by its age ("41 days"); the node's last sync result is shown on the internal row ("sync failed") and on the Sync line.
- **Wireframe note.** §4.7 makes `sync.last_run.result == failed` red for every root, so in the shared fixture ai-n2's internal root is red ("sync failed"), not amber "tier 2" as the illustrative wireframe draws it. The rule is authoritative; the wireframe is not changed.
- **`hardware`.** The card's `hardware` is the node's `hardware` column when set (for example "128 GB unified" on a shared-memory machine), otherwise the first GPU's name without the vendor prefix and the summed VRAM ("RTX 4080 12 GB"). The card line is `<backend> · <hardware>`. `vram` sums all GPUs of the snapshot.
- **Models.** `loadedModels` lists the models with status `loaded` (default first); `lastDefault` is the default model whatever its status. The web card shows `loadedModels` and the VRAM bar for `healthy` and `degraded` nodes, and "Last seen N ago" plus `lastDefault` with "(last)" for `quiet` and `down` nodes.
- **Behind flags.** `agentBehind` is false when either version is missing or not `major.minor.patch`. `catalogBehind` is false when the node reports no catalog or no catalog is approved yet; otherwise true when the node's version differs from the approved one.
- **Web.** Route `/lab` (`apps/web/src/localai/lab/LabOverviewPage.tsx`, gated on `lab:read`), and a `Lab` navigation destination (a destination, not a settings card) between Home and Settings. `useLabNodes.ts` polls every 5 s only while the tab is visible and refreshes when it becomes visible again. "Add node" (link to `/lab/nodes/new`, built in CLOUD-14) shows for `lab:admin` only; the empty state reads "No nodes yet". The components (`NodeCard`, `HealthChip`, `ModeChip`, `ArkChip`, `VramBar`, `ModelList`) are `packages/ui/src/lab/`, imported as `@localai/ui`; they take plain data and fetch nothing. Survival uses the theme's secondary colour. Their tests live with the web tests (`apps/web/src/localai/lab/`) because React and MUI are installed there; there is no Storybook in the repository, so no stories.
- **Build wiring.** `@localai/ui` is TypeScript source behind the committed symlink. `apps/web` sets `resolve.preserveSymlinks` (Vite and `tsconfig.json`) so the package finds this app's React and MUI, `server.fs.allow` lets the dev server read the link target, and Vitest inlines the package. The deploy branch dereferences the symlink (§3.13), so the web image builds from a real copy. `packages/ui` declares no `peerDependencies` and its own `tsconfig.json` checks only `src/api`: `agent.yml` installs and type-checks it standalone, without React.

### 3.12 EAB seam requests (file in `marinoscar/EnterpriseAppBase`, template `seam_request.yml`)

EAB today offers **no way to make a job claimable by only one node**. `JobClaimService.claim` filters by `type = ANY(eligibleTypes)`, status, and schedule only. Node routes are scoped to the credential's **owner**, not to one node, so one admin-owned `nod_` can act for every node that admin owns. `request.authCredential` for a node is `{ kind: 'node' }` without the credential id.

| Id | Ask | Why | Interim (this spec) |
|---|---|---|---|
| **SR-1** Targeted jobs | `JobsService.enqueue({ …, targetNodeId? })`; column `jobs.target_node_id uuid NULL` (FK `worker_nodes`, `ON DELETE SET NULL`→ job fails `target_gone`); claim adds `AND (target_node_id IS NULL OR target_node_id = $nodeId)` for nodes and `AND target_node_id IS NULL` for the server worker | Commands must run on exactly one node | App-owned command plane `localai_commands` (§4.4) |
| **SR-2** Credential→node binding | `request.authCredential = { kind: 'node', credentialId }`; optional `worker_nodes.credential_id` set on first register; register/heartbeat/claim refuse a different credential for a bound node (`403 node_credential_mismatch`) | A leaked node credential must not speak for other nodes | App-side binding guard re-derives the credential id from the bearer's sha256 (§4.2). EAB's own heartbeat/claim remain unbound; we never trust EAB health alone for routing (§4.5) |
| **SR-3** Deploy from a subdirectory | `appctl deploy install|update --app-dir apps/cloud` (compose, `.env.example`, version manifests resolved under it) | The app lives in a monorepo | Generated deploy branch `deploy/cloud` (§3.13) |

When SR-1 and SR-2 ship, the migration is a later work item (CLOUD-20, §10): each `COMMANDS` entry becomes a node-eligible EAB job handler (`nodeResultSchema` = the result schema, `persistNodeResult` writes `localai_commands.result`), the agent's `config.commandPlane` switches to `eab`, and `localai_commands` stays as the UI/audit view with a `jobId` column.

### 3.13 Deploy (`laictl deploy` to the VPS)

**DNS (Route53, zone `marin.cr`, `<ROUTE53_ZONE_ID>`)**, once:
```json
{ "Comment": "Local AI cloud app",
  "Changes": [ { "Action": "UPSERT", "ResourceRecordSet": {
      "Name": "ai.marin.cr", "Type": "A", "TTL": 300,
      "ResourceRecords": [ { "Value": "<VPS_PUBLIC_IP>" } ] } } ] }
```
```bash
aws route53 change-resource-record-sets --hosted-zone-id <ROUTE53_ZONE_ID> --change-batch file:///root/ai-cloud-dns.json
dig +short ai.marin.cr            # → <VPS_PUBLIC_IP>
```

**Deploy branch (interim for SR-3).** Workflow `.github/workflows/cloud-deploy-branch.yml`, triggered by `workflow_run` on the `cloud-ci` workflow (`types: [completed]`, `branches: [main]`; the job runs only when `github.event.workflow_run.conclusion == 'success'` and the run touched `apps/cloud/**` or `packages/ui/**`), plus `workflow_dispatch`:
1. `rsync -aL --delete --exclude node_modules --exclude .env apps/cloud/ "$RUNNER_TEMP/tree/"` (`-L` turns the `packages/localai-ui` symlink into a real copy).
2. Check out branch `deploy/cloud` (create orphan on first run) into a worktree, replace its contents with the tree, commit `deploy: apps/cloud @ <main short sha>` (author `github-actions[bot]`), push. Never force-push.
3. Run `scripts/check-redaction.sh` on the tree before committing.

**Install on the VPS** (owner, over SSH, as root):
```bash
mkdir -p /opt/infra/apps && cd /opt/infra/apps
git clone --branch deploy/cloud https://github.com/marinoscar/local-ai.git local-ai-cloud-src
cd local-ai-cloud-src && npm ci --workspace=cli && npm run build --workspace=cli
node apps/cli/dist/cli.js deploy doctor --domain ai.marin.cr
node apps/cli/dist/cli.js deploy install --domain ai.marin.cr --ref deploy/cloud --no-version-bump --name local-ai-cloud
# later updates:
node apps/cli/dist/cli.js deploy update --name local-ai-cloud --no-version-bump
```
`--no-version-bump` because the deploy branch is generated; versions are bumped on `main` in `apps/cloud/apps/*/package.json` by the release PR. The deploy writes the `ai.marin.cr` vhost into `/opt/infra/proxy/conf.d/`, issues the certificate and schedules renewal (EAB `publish`/`renewal` steps). The `marin-server-infra` repo records the new vhost ([overview.md](../architecture/overview.md) §6).

**Proxy paths.** In `apps/cloud/apps/cli/src/app.ts`:
```ts
proxy: { streamPaths: [{ path: '/v1/', timeoutSeconds: 1800 }] }   // gateway.md §4.2: edge must not buffer or cut at 60 s
```
and the inner nginx location `apps/cloud/infra/nginx/app.d/locations/gateway.conf` is in [gateway.md](gateway.md) §3.1. Node command long-polls (≤ 25 s) fit the default 60 s `location /api`.

**Environment** (`.env` written by the wizard from `infra/compose/.env.example`; app additions go in `infra/compose/app.env.example`):

| Variable | Value | Notes |
|---|---|---|
| `APP_URL` | `https://ai.marin.cr` | Device-flow `verificationUri` is built from it |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | from Google Cloud console | never committed |
| `GOOGLE_CALLBACK_URL` | `https://ai.marin.cr/api/auth/google/callback` | |
| `INITIAL_ADMIN_EMAIL` | `<ADMIN_EMAIL>` | |
| `TENANCY_MODE` | `single` | |
| `JWT_SECRET`, `COOKIE_SECRET` | generated by the wizard | |
| `SECRETS_ENCRYPTION_KEY` | generated; **back it up** (§3.8) | |
| `POSTGRES_HOST` / `PORT` / `DB` / `USER` / `PASSWORD` / `SSL` | the VPS Postgres on the private Docker network (`infra-postgres`), DB `local_ai`, role `local_ai_app` (NOSUPERUSER) | never the public 5432 ([security-model.md](../architecture/security-model.md) §6) |
| `JOBS_WORKER_MODE` | `all` | Phase 2 VPS stack-agent (§4.10) switches to `system` |
| `EVENT_BUS_ADAPTER` | `in-process` | one replica; `postgres` if ever scaled |
| `OTEL_ENABLED` | `false` | |
| `CORS_ORIGIN` | unset | same-origin; the gateway sets its own CORS ([gateway.md](gateway.md) §6) |
| `LOCALAI_AICTL_PATH` | `/opt/localai/bin/aictl` | inside the API image (§4.6) |
| `LOCALAI_GATEWAY_*` | see [gateway.md](gateway.md) §3.7 | |

The API Dockerfile (app-owned overlay `infra/compose/app.compose.yml` → `api` build arg) adds the static `aiagent` binary from the matching GitHub release, verified against its `SHA256SUMS`, at `/opt/localai/bin/aiagent` with symlink `/opt/localai/bin/aictl`.

## 4. Behaviour

### 4.1 Node lifecycle

```
 wizard POST /nodes              first bound /localai call          retire
 ┌─────────┐  (nod_ minted+bound)  ┌──────────┐                       ┌─────────┐
 │ pending │ ────────────────────► │ enrolled │ ────────────────────► │ retired │
 └─────────┘                       └──────────┘                       └─────────┘
      │  credential rotate: stays pending   │ credential rotate: stays enrolled;
      └────────── retire ──────────────────►│ the node must `aictl cloud enroll` again
```
`retired` is terminal: credential revoked (`DELETE /api/node-credentials/{id}` through `NodeCredentialService`), key deleted, queued commands `cancelled`, `gatewayEnabled=false`, row and history kept. A node id is never reused.

### 4.2 Binding guard (every `/api/nodes/{id}/localai/*` route)

`LocalaiNodeBindingGuard` (`apps/api/src/localai/node-link/binding.guard.ts`), after EAB's `JwtAuthGuard` admitted the request:

1. `request.authCredential?.kind === 'node'` else `403 LAI-CLD-004 reason: "node_credential_required"` (sessions and PATs cannot speak for a node).
2. `raw = Authorization header minus "Bearer "`; `hash = sha256(raw).hex`; `cred = nodeCredential.findUnique({ tokenHash: hash })`. (EAB stores `sha256(token)` hex in `node_credentials.token_hash`; this re-derivation is the SR-2 interim. A unit test pins the digest format against `NodeCredentialService`'s private `hash` by minting a credential through the service and looking it up.)
3. `wn = workerNode.findUnique({ id })`; `404` if missing; `403` if `wn.createdById !== request.user.id` (same semantics as EAB `assertOwnership`).
4. `ln = localaiNode.findUnique({ nodeId: wn.name })`; `404 LAI-CLD-004 reason: "unknown_localai_node"` if missing; `403 LAI-CLD-035` if `retired`.
5. `ln.nodeCredentialId !== cred.id` → `403 LAI-CLD-004 reason: "credential_bound_to_other_node"`; audit `localai.node.binding_refused` (meta: credential prefix, claimed node).
6. If `ln.workerNodeId !== wn.id`: update it (re-registration after prune). If `phase = pending`: set `enrolled`, audit `localai.node.enrolled`, raise notification `localai.node.enrolled`.
7. Attach `request.localaiNode = ln`.

The guard is the only path that loads `ln` for node routes; handlers receive the verified row.

**As built (CLOUD-05, #79).** Code: `apps/cloud/apps/api/src/localai/node-link/` (`binding.guard.ts`, the pure `binding.decision.ts`, `node-link.controller.ts`, `node-link.module.ts`); the handshake body schema is `contracts/config.schema.ts`. Differences and details to know:

- **Guard order.** Every controller under `/api/nodes/:id/localai` carries `@NodeLinkAuth()` (`node-link-auth.decorator.ts`), which is EAB's `@Auth()` followed by the binding guard, so the order (JwtAuthGuard first) cannot be written backwards. `test/localai/node-link-routes.spec.ts` discovers every route under that path and fails if one lacks either guard or has them in the wrong order. The guard also refuses (step 1) when `request.authCredential` is absent, so it cannot admit a request EAB's guard did not.
- **Step 2 refusals.** A bearer that is not `Bearer nod_...`, or an `authCredential` that is not `node`, is `403 LAI-CLD-004 node_credential_required`. A credential that is gone, revoked, expired or owned by a different user than the authenticated one (it can only happen in a race after EAB's check) is `401`, EAB's own answer, which the agent treats as `LAI-CLD-051`.
- **Step 3.** A `{id}` that is not a uuid is `404` (never reaches SQL). The `404`/`403` bodies are EAB's `assertOwnership` wording with `details.nodeId`; they carry no Local AI code.
- **Step 4 and 5, one addition.** A node whose `nodeCredentialId` is null (no credential bound yet) is refused `403 LAI-CLD-035` with reason `credential_not_bound` and audited as `localai.node.binding_refused`: an unbound node admits no credential (fail closed). The reasons the agent and UI may match on are `node_credential_required`, `unknown_localai_node`, `credential_bound_to_other_node`, `node_retired` and `credential_not_bound`.
- **Step 6 is one conditional write.** `updateMany({ where: { id, phase: <phase read>, nodeCredentialId: <this credential> }, data: { workerNodeId, phase: 'enrolled' } })`; the row is re-read when the count is 0 and the decision is taken again (at most three rounds, then `503`, never an admit). So two concurrent first calls produce one write, one `localai.node.enrolled` audit row and one notification, and a retire or credential rotation that lands between the read and the write refuses the call. Audit and notification run after the write, outside any transaction; if either fails the failure is logged (error name only) and the call is still admitted, and a failed `binding_refused` audit still refuses.
- **Audit** goes through the platform's `AUDIT_SINK` port (`audit_events`), actor = the credential's owner (the authenticated user). `binding_refused` meta is `{ credentialPrefix, claimedNodeId }` (the stored `tokenPrefix`, never the token); `enrolled` meta is `{ workerNodeId, agentVersion }`, where `agentVersion` is the node row's value, else the worker node's registered `cliVersion`.
- **Secrets.** The raw bearer is hashed once (`sha256` hex, the digest `NodeCredentialService` stores; pinned by a unit test that mints through the real service) and dropped: never logged, never in an error body, never compared in application code. The lookup is the unique-index probe on the digest; only ids are compared afterwards.
- **Notification hook.** `LOCALAI_NODE_NOTIFIER` (`node-link.notifier.ts`) is a typed port with a no-op default that only logs the node id. CLOUD-15 registers the `localai.node.enrolled` event (section 4.9) and rebinds the port in `NodeLinkModule`; the guard already calls it at the right moment.
- **The nodes slice is mounted.** The agent registers through EAB's `POST /api/nodes/register`, which exists only when `NodesModule.forRoot()` is composed; the starter did not mount it. `src/platform/platform.ts` now mounts it next to the jobs slice, and binds its two data-plane host ports (`NODE_OBJECT_STORE`, `NODE_JOB_INPUTS`) to inert adapters (`src/platform/nodes/nodes-host.ts`) that refuse, because Local AI moves no job bytes through object storage (section 6: no EAB job is node-eligible in v1). The fleet admin routes (`/api/admin/nodes`) and the `workers` settings namespace come with it; the web fleet page is not part of this issue.
- **Tests.** Unit: `node-link/binding.guard.spec.ts` (every decision, the race, the re-link, the digest format), `node-link.config.spec.ts`; HTTP over a mocked database: `test/localai/node-link-routes.spec.ts`; real SQL: `test/localai/node-link.db.spec.ts` (mint, bind, `register`, `config`, six concurrent first calls, wrong-node refusal, prune and re-register, retired, revoked). The handshake fixture is `test/fixtures/localai/config.response.json`.

### 4.3 State ingestion

On `POST …/state` (in one transaction):
1. Parse with `LocalaiStateSnapshotV1`; run the secret scrubber.
2. Order check: accept if `boot_id` differs from the stored one, or `seq > lastStateSeq`. Else `409 stale_seq`.
3. Update `localai_nodes`: `lastState` (verbatim JSON), `lastStateAt = now()`, `lastStateSeq`, `mode`, `connectivity`, `profile`, `agentVersion`, `catalogVersion`, `backend`.
4. Diff `models[]` into `localai_node_models` (upsert present ids, delete absent ids). Catalog `roles` come from the snapshot.
5. After commit: publish `localai.node.state` on the event bus `{ nodeId, modelsChanged: bool, modeChanged: bool }` (the gateway routing table listens, [gateway.md](gateway.md) §4.3; ≤ 7.5 kB payload). If `mode` changed, audit `localai.node.mode_changed` (actor = node) and re-evaluate alerts for this node.

### 4.4 Command plane

```
            enqueue (UI/API)                    claim (node long-poll)
  ┌────────┐ ──────────────► ┌────────┐ ───────────────────────────► ┌─────────┐
  │ (none) │                 │ queued │                              │ running │──renew (every 20 s)──┐
  └────────┘                 └────────┘                              └─────────┘◄─────────────────────┘
                               │   │ expiresAt passed (sweeper)        │   │   │
               DELETE (cancel) │   └──────────► expired                │   │   └─ lease passed (sweeper) ─► failed LAI-CLD-059
                               ▼                                       │   └── failure ──► failed
                           cancelled                                   └── result (valid) ──► succeeded
```
- **Enqueue** (`CommandsService.enqueue(nodeId, type, rawParams, actor, source)`): permission check (§3.7), node `enrolled` and not retired, `params = COMMANDS[type].params.parse(rawParams)` (Zod `400` with issues), dedup key = `"<type>:<subject>"` per the table; insert; on unique violation of `localai_commands_active_dedup` return the active row (`200`). `expiresAt = now() + queuedTTL`. Audit, then publish `localai.command.enqueued { nodeId }`.
- **Claim** (`POST …/commands/claim`): types requested ∩ known types (unknown names dropped with a warn log). Statement:
  ```sql
  WITH picked AS MATERIALIZED (
    SELECT id FROM localai_commands
     WHERE node_id = $1 AND status = 'queued' AND expires_at > now() AND type = ANY($2::text[])
     ORDER BY created_at ASC
     FOR UPDATE SKIP LOCKED
     LIMIT 1)
  UPDATE localai_commands c SET status = 'running', attempts = attempts + 1, claimed_at = now(),
         claim_token = gen_random_uuid(), lease_expires_at = now() + ($3::double precision * interval '1 millisecond')
    FROM picked p WHERE c.id = p.id
  RETURNING c.*;
  ```
  (`$3` is the type's lease, chosen by a second `CASE` over `type`, or a two-step: pick → compute lease → update in the same transaction.) If nothing is returned and `waitMs > 0`: subscribe to `localai.command.enqueued` for this node, wait until the event or `waitMs` (whichever first), then run the statement once more. A single in-flight long poll per node: a second concurrent claim from the same node gets an immediate empty answer (`details.reason: "claim_in_progress"` in a log line only). The request also ends early when the client disconnects.
  Also a node may hold at most one `running` command: if one is `running` with an unexpired lease, claim returns empty.
- **Renew:** `UPDATE … SET lease_expires_at = now() + lease WHERE id = $id AND node_id = $node AND status = 'running' AND claim_token = $tok AND lease_expires_at > now() AND claimed_at + hardCap > now()` (`hardCap` per type, §3.7 table); 0 rows → `409`.
- **Result:** same held-claim predicate; `type` must match; `result = COMMANDS[type].result.parse(body.result)` (`400` with issues, command stays running so the agent can't loop: the agent treats 400 as a bug, reports `failure` with `LAI-CLD-012`). Persist `status = succeeded`, `result`, `finishedAt`, clear lease. `logs.tail` lines are scrubbed again server-side. Side effects after commit: `model.set_default`/`model.load` none (the next snapshot carries the truth); `key.rotate` none (key already pushed); `mode.offline` sets `localai_nodes.mode = offline` optimistically.
- **Failure:** held-claim predicate; `status = failed`, `error = { code, message }`.
- **Sweeper** (`@Cron` every 30 s → enqueues EAB server job `localai.commands.sweep`, per EAB "crons only enqueue"): `queued` past `expiresAt` → `expired` (error `LAI-CLD-030`); `running` past `lease_expires_at` → `failed` (`LAI-CLD-059`); `logs.tail` results older than 7 days → `result = null`; commands older than 180 days deleted.
- **Ordering:** per node, FIFO by `created_at`. Across nodes, independent.
- The UI shows a command as "Waiting for ai-n2 (checks every few seconds)" while queued, "Running" while running, and the result or the error's plain-language fix when settled.

**As built (CLOUD-08, #82).** Code: `apps/cloud/apps/api/src/localai/` (`contracts/commands.ts`, `contracts/command-wire.ts`, `commands/*`, `node-link/commands.controller.ts`); the node side only. The shared examples for the agent's fake cloud are `test/fixtures/localai/claim.*.json`, `renew.*.json`, `result.*.json`, `failure.*.json` and `commands.examples.json` (one valid `params` and `result` per type). Differences and details to know:

- **Registry and policy.** `COMMANDS` is the section 3.7 block verbatim, importing `ModelId`, `ContentId` and `RootId` from `contracts/state.schema.ts`. `commands/command-policy.ts` is the section 3.7 table as data (permission, lease, `renewIntervalMs`, hard cap, queued TTL, max attempts 1, dedup subject, UI confirmation); a test pins every number against the table and the agent's `TypeLimits`. `renewIntervalMs` is the lease divided by three clamped to the agent's 5 to 60 s (10 s, 20 s, 60 s).
- **Dedup key** is `"<type>:<subject>"` with the type as the section 3.7 table names it (no `localai.` prefix) and `-` for a type with no subject (`model.load:qwen3.6-35b-a3b-q4km`, `status.refresh:-`). `CommandsService.enqueue` returns `{ command, created }` (`created: false` is the HTTP 200 of the UI route); a unique violation of `localai_commands_active_dedup` (told apart from any other P2002) re-reads the active row, retrying the insert if it settled in between. Permission checks, node-state refusals and audit are CLOUD-09 (section 3.9, "As built (CLOUD-09, #83)").
- **Events** (`commands/command-events.ts`): `localai.command.enqueued { nodeId }` after the insert and `localai.command.settled { nodeId, commandId, status }` (`succeeded`, `failed` or `expired`) after the settling write committed. `nodeId` is the `localai_nodes.id` uuid, not the `ai-nN` string. A failed publish is logged and never fails the request.
- **Claim.** `commands/claim.sql.ts` is the only raw SQL of the plane (allowlisted in `localai.raw-sql.ts` for the `user-owned-data` suite). One short transaction per attempt: it first locks the node's own row (`FOR NO KEY UPDATE`), which serializes the claimers of a node across API processes; then the section 4.4 pick (`FOR UPDATE SKIP LOCKED`, FIFO, unexpired, requested types) with one addition, `AND NOT EXISTS` a `running` command of the node with an unexpired lease, which is the "one running command per node" rule; then the update, with the lease chosen in code per type (the "two-step" the section allows) and `now` bound from the cloud's clock. No transaction or connection is held while the long poll waits.
- **Long poll.** The wake-up subscription is made before the first attempt (an enqueue between the attempt and the wait is not lost); the loop attempts again on every wake-up for this node until `waitMs` ends, not just once. One in-flight claim per node in the process (`claim_in_progress`, answered empty at once). The poll ends early, empty, when the response closes before it was written and on application shutdown (`beforeApplicationShutdown` aborts every held poll, so closing the server does not wait up to 25 s). A command claimed in the instant the client left stays `running` and is failed by the sweeper when its lease passes (`LAI-CLD-059`).
- **Clock.** Leases, expiries and hard caps use the cloud's clock through an injectable `LOCALAI_CLOCK` (the system clock in production), so one process computes every comparison; the section 4.4 text says `now()`, the effect is the same for one API process and tests can move time.
- **Renew** is read, compute, then one conditional write that repeats the held-claim predicate; the new lease is clamped to `claimed_at + hardCap`, so renewing late in the life of a command cannot extend it past the cap. At or after the cap the renewal is refused (409).
- **Settling.** Result and failure are one conditional `UPDATE` carrying the held-claim predicate; 0 rows is 409. A command id that is another node's or unknown is also 409 (existence is not revealed); an id that is not a uuid is 404. The claim token is kept on a node-settled row, so the very same post repeated (a lost response) answers 200 again and changes nothing; the sweeper clears it, so a late result, failure or renew after a sweep is always 409.
- **Error bodies** keep EAB's format with our data in `details`: 409 `{ code: "LAI-CLD-059", reason: "lease_lost" }`; 400 `{ reason: "invalid_body" | "result_type_mismatch" | "invalid_result", issues: [{ path, code, message }] }` (issues never echo a value); the result 400 leaves the command `running`. Claim, renew, result and failure bodies are strict Zod schemas except that `limit` and `waitMs` are clamped rather than refused and the failure body accepts and ignores `rateLimited` and `retryAfterMs`. A failure string is split by `^(LAI-[A-Z]{2,3}-\d{3}):\s*` and the message scrubbed (`commands/command-scrub.ts`, the agent's redaction rules) and cut to 500 characters; `logs.tail` result lines are scrubbed again.
- **Sweeper.** `commands/sweeper.task.ts` is the app's first scheduled task: it runs every 30 s and only calls `enqueueHousekeepingJob` for the queue job `localai.commands.sweep` (`commands/sweeper.job.ts`, server-only, profile `{ maxRuntimeMs: 60 000, maxAttempts: 1 }`, no organization). The job settles each row with its own conditional write (batches of 200, up to 10 batches per run), so a result that wins the race is never overwritten. The `cron-enqueue-only` conformance suite is enabled for the app (`{ exempt: [], minCronFiles: 1 }`).
- **Catalog approval** (CLOUD-10) queues `catalog.refresh` on its eligible nodes through the same port (`LOCALAI_COMMAND_ENQUEUER` -> `CommandsService`, source `catalog_approve`), so the dedup, the policy table's 1 h queued TTL and the `localai.command.enqueued` wake-up are the command plane's; the interim `CatalogRefreshQueue` is gone. Its wake-up carried the lab id (`ai-n3`) where the claim listens for the `localai_nodes.id` uuid, so it never woke a held claim; now it does (proved in `catalog.db.spec.ts`). The catalog service still writes the `localai.command.requested` audit row for the commands it queues.
- **Tests.** Unit: `contracts/commands.spec.ts` (every type's example, strictness, bounds, defaults, the wire fixtures), `commands/*.spec.ts` (policy table, lifecycle math with a fake clock, failure parser and scrubber, long poll, cron task); the whole state machine over an in-memory table in `test/localai/commands.state-machine.spec.ts`; HTTP over a mocked database in `test/localai/commands-routes.spec.ts`; real SQL in `test/localai/commands.db.spec.ts` (claim ordering, concurrency, wake-up within 200 ms, leases and hard caps per type, sweeper, dedup, the whole path with real `nod_` credentials).

### 4.5 Derived health (cloud view)

Computed at read time, never stored (same reasoning as EAB):
```
eab     = deriveNodeHealth(workerNode)                    // healthy | stale | offline (EAB, staleHeartbeatSeconds=45)
age     = now - lastStateAt
expectedSilence = lastState.mode in (offline, survival) || phase == pending
health  = expectedSilence                     ? "quiet"
        : eab == healthy && age <= 120 s      ? "healthy"
        : eab != offline && age <= 600 s      ? "degraded"
        :                                       "down"
routable (gateway) = health == "healthy" && mode == online && gatewayEnabled && phase == enrolled
                    && servePattern != none
gatewayRoutable    = routable && keyReportedAt != null                    // what the gateway routing table admits (gateway.md §2, §4.3)
```
`routable` drives the UI ("Gateway: ● 2 nodes", catalog notify); the gateway routes only to `gatewayRoutable` nodes, because it cannot call a node whose llama key it does not hold. The gateway additionally skips, in memory, any candidate it marked `keyInvalid` (it saw `401` with the stored key, [gateway.md](gateway.md) §4.3) until a new `localai.node.key_reported` event.
`routable` depends on the bound snapshot age, so a leaked credential faking EAB heartbeats for another node cannot make it routable.

### 4.6 Catalog import, lint and approval

- **Import** (`POST /api/localai/catalog/import { tag }`): server job `localai.catalog.import` downloads `https://github.com/<catalog.githubRepo>/archive/refs/tags/<tag>.tar.gz` (≤ 20 MB, 30 s timeout), extracts only `catalog/**` (path-traversal-safe, no symlinks), reads `CATALOG_VERSION`, refuses if that version already exists (`409`), stores files + sha256 + `bundleSha256`, `status = draft`, queues lint.
- **Editor / upload** (`POST /api/localai/catalog/versions`): files ≤ 2 MB total, paths matching `^(CATALOG_VERSION|models\.yaml|ark\.yaml|profiles/[a-z0-9-]+\.yaml)$`; `version` must be greater than every existing version (string compare of `YYYY.MM.DD-N` normalised), `CATALOG_VERSION` content is rewritten to it.
- **Lint** (server job `localai.catalog.lint`, server-only): write files to a fresh temp dir (EAB `job-temp`), run `execFile(LOCALAI_AICTL_PATH, ['catalog', 'lint', '--json', dir], { timeout: 30_000 })` (no shell). Exit 0 → `valid`; exit 2 → `invalid` with the JSON problem list; other → `draft` + `lintResult.error` and Doctor `LAI-CLD-033`. Requires `aictl catalog lint --json` to print `{"valid":bool,"problems":[{"code":"LAI-CAT-011","file":"models.yaml","path":"models[3].source.revision","message":"…"}]}` ([aictl.md](aictl.md) §catalog lint).
- **Approve:** only `valid` → `approved` (previous approved → `superseded`) in one transaction; the partial unique index guarantees one approved row. Optional `notifyNodes` queues `catalog.refresh` on each `routable`-or-`degraded` node.
- Nodes otherwise pick it up on their nightly sync (catalog refresh is step 1 of [overview.md](../architecture/overview.md) §4.3).

**As built (CLOUD-10, #84).** Code: `apps/cloud/apps/api/src/localai/catalog/` (`catalog.service.ts` is the state machine; `catalog.controller.ts`, `catalog-import.job.ts`, `catalog-lint.job.ts`, `catalog-linter.ts`, `tar-extract.ts`, `archive-fetcher.ts`, `bundle-hash.ts`, `version.ts`, `catalog-files.ts`, `node-eligibility.ts`). Differences and details to know:

- **Import is asynchronous.** `POST /import { tag }` checks that the tag is `catalog-<YYYY.MM.DD-N>` (a real date; `catalog-stable` and anything else is `400`), answers `409 { details: { reason: "version_exists" } }` at once when that version is already stored, and otherwise queues `localai.catalog.import` and answers `202 { data: { tag, version, jobId } }`. The draft appears when the job has downloaded and stored it (the UI polls `GET /versions`); the job queues the lint. A second request for the same version while its job is queued or running collapses onto that job (the job's `subjectId` is the version). The job fails, with a plain message in the job list, for a failed download (`LAI-CLD-013 ... (not_found | too_large | timeout | http_error | redirect_refused | network)`), a refused archive, a `CATALOG_VERSION` that differs from the tag, or a version created in the meantime; a re-run of an import that already stored the same tag is a no-op that only makes sure its lint was queued.
- **Download.** HTTPS only, hosts `github.com` and `codeload.github.com` only (the archive URL redirects between them; any other redirect target, an `http` one, a port or credentials in the URL is refused), at most 5 redirects, 20 MB (checked against `Content-Length` and again while streaming) and 30 s overall. `<catalog.githubRepo>` must be `<owner>/<repo>` (letters, digits, `.`, `_`, `-`) or the job refuses to build a URL. The `localai` settings namespace is not registered yet (GW-02), so the repository is read from `system_settings.global.value.localai` (nested `catalog.githubRepo` or the dotted key) and defaults to `marinoscar/local-ai`.
- **Extraction** is in memory (nothing is written to disk) and refuses the whole archive for: an absolute path, a `..` segment, a NUL or backslash in any entry's path; a symlink, hardlink or special file at or under `<top>/catalog/` (links elsewhere in the repository are ignored, the repository has some); a file under `catalog/` that is not one of the catalog paths, or listed twice; more than 2 MB of catalog files, 512 MB inflated or 200 000 entries; more than one top-level directory; a damaged header or gzip stream; no catalog files at all. pax extended headers (long paths), GNU long names and the pax global header GitHub writes are understood. File content must be UTF-8 without NUL (JSONB cannot hold U+0000) and is stored byte-exact (a BOM is kept), so the digests equal the ones a node computes from the same tag.
- **Digests.** Per-file `sha256` is of the UTF-8 bytes; `bundleSha256` is the agent's `BundleDigest` (sorted `"path\0sha256\n"` lines). `bundle-hash.spec.ts` pins a golden vector computed by the agent's own function.
- **Versions.** `YYYY.MM.DD-N` must be a real date and `N` ≥ 1 without a leading zero (at most 9 digits), compared field by field as numbers (`2026.11.02-10` is greater than `2026.11.02-9`), the same rule as the tag guard. A draft from the editor or an upload must be greater than **every** stored version, whatever its status (equal counts as not greater): `400 { details: { reason: "version_not_greater" } }`. `files` is the complete set of the new catalog (`baseVersion`, if given, must exist (`404`) and is only recorded as `sourceRef`); `CATALOG_VERSION` is added or overwritten with `<version>\n`; a path outside the layout or a duplicate is `400`; more than 2 MB of content (UTF-8 bytes, with the rewritten `CATALOG_VERSION`) is `413 { details: { reason: "catalog_too_large" } }`. `source` is `editor` unless the body says `upload`. `POST /versions` answers `201` with the draft's summary and queues its lint. Fastify's default 1 MiB body limit is lifted to 6 MiB for exactly this route (`catalog-body-limit.ts`, hooked in `createApp`) so the 2 MB rule is the service's, not the framework's.
- **Lint** runs in `localai.catalog.lint` (server-only, single attempt, 90 s ceiling). It writes the stored files to a private (`0700`) directory under the job temp directory (`jobTempPath()`, so the platform's janitor also sweeps leftovers), runs `execFile(LOCALAI_AICTL_PATH, ['catalog', 'lint', '--json', dir], { timeout: 30 s, maxBuffer: 1 MiB, env: { PATH } })` (no shell, no secrets in the child's environment) and always removes the directory. Exit 0 with `valid: true` is `valid`; exit 2 with `valid: false` is `invalid` and the problems (at most 500) are stored; **anything else leaves the version `draft`** with `lintResult.error = { code: "LAI-CLD-033", reason, message }` where `reason` is `aictl_missing` (path unset, ENOENT, EACCES), `timeout`, `aictl_failed` (another exit code, `exitCode` recorded) or `bad_output` (output that is not the documented JSON, or an exit code that contradicts its `valid`, or more than 1 MiB of output). The job returns normally in that case: the failure is a recorded outcome, not a reason to retry the same missing binary. The status is written only while the row is still `draft`, `invalid` or `valid`, so a lint that finishes after an approval never overwrites it. `POST /versions/:version/lint` queues a re-lint (`202 { data: { version, jobId } }`) for those three statuses and answers `409 not_lintable` for `approved` and `superseded`; files never change after creation, so a fixed catalog is a new version, not an edit.
- **Approve** (`200 { data: { version, supersedes, notified } }`) is one `$transaction`: the approved row (if any) becomes `superseded`, then the `valid` row becomes `approved` with `approvedById` and `approvedAt`. A version that is not `valid` is `409 { details: { reason: "not_valid", status } }`. Two approvals racing meet `localai_catalog_one_approved`: the loser's transaction rolls back and it answers `409 concurrent_approval`; the db spec approves six versions at once and proves exactly one approved row and that every `200` is either approved or superseded.
- **`notifyNodes`** runs after the commit and outside the transaction. An enrolled node is eligible when its derived health (section 4.5, `node-eligibility.ts`) is `healthy` or `degraded`; Offline or Survival mode, `pending`, `retired` and `down` nodes are skipped (their command would only expire, LAI-CLD-034). Each eligible node gets one `catalog.refresh` through the command plane's enqueue port (`LOCALAI_COMMAND_ENQUEUER`, CLOUD-08): row `type = "localai.catalog.refresh"`, `params = {}`, `dedup_key = "catalog.refresh:-"`, `source = "catalog_approve"`, `expires_at = now + 1 h`; an active one already on the node (the dedup index) is reported in `alreadyQueued`. Audit `localai.command.requested` follows each new row and the event `localai.command.enqueued { nodeId }` (the `localai_nodes.id`) wakes the node's held claim. The response's `notified` is `{ queued: [nodeId], alreadyQueued: [nodeId], skipped: n }` (`null` without `notifyNodes`).
- **Reads.** `GET /versions` lists every version newest first by version (numeric compare) without file contents, each with `status`, `bundleSha256`, `lintedAt`, `problemCount`, `lintError`, `notes`, `createdById`, `approvedById`, `approvedAt`. `GET /versions/:version` adds `files` (`path`, `sha256`, `content`), `lintResult` and, with `?diff=approved`, `diff: { against, files: [{ path, change: added | removed | changed | unchanged, baseSha256, sha256 }] }` (`against` is `null` when nothing is approved; line-level diff is the editor UI's, CLOUD-13). An unknown or malformed `:version` is `404`; any other `diff` value is `400`.
- **Audit** (`targetType = localai_catalog`, `targetId` = the version): `localai.catalog.imported` for every new version whatever its source (`{ source, sourceRef }`), `.linted` when a verdict or a recorded failure was written (`{ source, sourceRef, valid }`), `.approved` (`{ source, sourceRef, supersedes, notifyNodes, nodesNotified }`).
- **Image.** `apps/api/Dockerfile` has an `aiagent` stage that downloads `aiagent-linux-<arch>` and `SHA256SUMS` from the release `node-v<AIAGENT_VERSION>` of this repository, fails the build when the digest does not match or the file is not listed, installs it at `/opt/localai/bin/aiagent` with the symlink `/opt/localai/bin/aictl` and smoke-runs `aictl catalog lint --help`; the production stage copies `/opt/localai` and sets `LOCALAI_AICTL_PATH=/opt/localai/bin/aictl`. The version reaches the build through the overlay `infra/compose/app.aictl.compose.yml` (build arg from the `.env` variable `AIAGENT_VERSION`, declared in `app.env.example`). The development image has no checker, so a lint there ends as LAI-CLD-033.
- **Tests.** Unit: `version`, `bundle-hash` (golden vector), `tar-extract` (traversal, absolute paths, links, bombs, damage), `catalog-files`, `archive-fetcher`, `catalog-linter` (fake `aictl` scripts for exit 0, 2, other, missing, timeout, flood, contradictory output; argv, private directory, empty environment, cleanup), `catalog.service` (the state machine over an in-memory table), `node-eligibility`, `etag`, `catalog-diff`, `catalog-settings`. HTTP over the mocked app: `test/localai/catalog-routes.spec.ts` (RBAC matrix, validation, the node route 404/200/304/pin and its guard order). Real SQL and the real `aictl`: `test/localai/catalog.db.spec.ts` (needs `LOCALAI_AICTL_PATH`; skipped with a warning when unset locally, a failure when `CI` is set). The release archive is built at test time from the repository's own `catalog/` files (`test/localai/helpers/tar-builder.ts`), so the tests track the real schema; no binary fixture is committed.

### 4.7 Ark readiness colour (per root, from the snapshot)
```
red    if tiers.t1 != ok || items_failed > 0 || sync.last_run.result == failed
       || (root.kind == ark_drive && drive_last_synced_at older than alerts.driveCriticalDays)   // 90 d
amber  if tiers.t2 != ok || items_missing > 0
       || (root.kind == ark_drive && drive_last_synced_at older than alerts.driveStaleDays)      // 30 d
green  otherwise
```
A root of kind `ark_drive` uses the `root == ark_drive` rules above (one row per drive id). The last-seen drive row is shown even when unplugged (`present: false`) using `drive_last_synced_at`, labelled "Drive not plugged in · last synced 6 days ago".

### 4.8 Add-node wizard

1. **Create** (`POST /api/localai/nodes`): validate `nodeId` unused ever (including retired), `publicHost = <nodeId>.marin.cr` unless overridden; in one transaction create `localai_nodes(phase=pending)`, mint a node credential through EAB's `NodeCredentialService.createCredential(userId, { name: "localai-<nodeId>", expiresInDays: null })`, bind `nodeCredentialId`. Return `{ node, credential: { token: "nod_…", prefix }, install: { oneLiner, pinnedScriptUrl, scriptSha256 } }`. The raw token exists only in this response (EAB show-once).
2. **Install one-liner** uses the latest agent release tag (`settings.agent.latestVersion`, tag `node-v<version>`), never `main`:
   ```bash
   curl -fsSLO https://github.com/marinoscar/local-ai/releases/download/node-v0.4.1/install-node.sh
   sha256sum install-node.sh          # compare with scriptSha256 shown in the wizard
   sudo bash install-node.sh --node-id ai-n4 --cloud-url https://ai.marin.cr --enroll
   ```
   The script is downloaded from the release (the `kit-agent-binaries` assets, [rebuild-kit.md](rebuild-kit.md) §3.2), checked, then run; it is never piped into `bash` ([installer.md](installer.md) §3.1 owns this). `--enroll` makes the installer prompt for the credential on `/dev/tty` (hidden input) and run `aictl cloud enroll --credential-stdin` ([installer.md](installer.md) §3.2; [aictl.md](aictl.md) §4.18). The credential is never put on a command line.
3. **Enrolled** turns green when the guard (§4.2) sees the first bound call; **key received** when `POST …/key` lands. The page polls `GET /api/localai/nodes/:nodeId` every 3 s.
4. **Publish checklist** (`publishChecklist` JSON: `{ "<step>": { "state": "todo"|"done"|"failed", "checkedAt": "…", "detail": "…" } }`). Snippets are rendered server-side from the node row (Pattern A/B templates from [source/vps-ai-nodes-setup.md](../source/vps-ai-nodes-setup.md) §6, with the real tailnet IP and tailnet name filled in **only in the rendered page**, which only lab:admin can open; the templates in the repo keep placeholders).

   | Step | How it is completed | Automatic check (runs from the API container) |
   |---|---|---|
   | `tailnet` | mark done | — |
   | `dns` | Check | `dns.resolve4(publicHost)` equals `dns.resolve4(settings.checklist.vpsPublicHostname)` |
   | `bootstrap_vhost` | mark done (shows the HTTP vhost file + `nginx -t` + reload commands) | — |
   | `certificate` | Check (shows the certbot command) | TLS connect to `publicHost:443`, SNI = host; cert valid, issuer Let's Encrypt, ≥ 7 days left |
   | `https_vhost` | Check (shows the Pattern A/B file) | `GET https://<host>/health` → 200 and `GET https://<host>/v1/models` without key → 401 |
   | `enrolled` | automatic | phase = enrolled |
   | `key` | automatic | `keyReportedAt` not null |
   | `gateway_test` | Check | `GET https://<host>/v1/models` with the stored key → 200 and the snapshot's loaded model ids appear |

   Each check has a 10 s timeout, writes `state`, `checkedAt`, `detail` (plain text, no secrets) and an audit event. All 8 `done` → banner "ai-n4 is part of the lab" and `gatewayEnabled` stays as set.
5. **Phase-2 hook:** the button **Publish automatically** is present but disabled with the tooltip "Coming later: needs the VPS helper (§4.10)". When enabled it enqueues EAB job `localai.vps.publish_node` with `{ nodeId }`.

### 4.9 Alerts and notifications

`@Cron('*/5 * * * *')` enqueues server job `localai.alerts.evaluate`. For each non-retired node (and lab-wide rules), compute the desired active set; raise new (insert `active`), refresh `lastSeenAt` on still-true, clear the rest (`cleared`, `clearedAt`). Never raise for expected silence (§4.5).

| Kind | Code | Severity | Raised when | Cleared when |
|---|---|---|---|---|
| `node.stale` | LAI-CLD-020 | warning | health `degraded`/`down` for ≥ `alerts.nodeStaleMinutes` while last mode `online` | healthy again, or mode offline/survival |
| `node.offline` | LAI-CLD-021 | critical | EAB `offline` ≥ `alerts.nodeOfflineHours` while last mode `online` | heartbeat resumes |
| `ark.drive_stale` | LAI-CLD-022 | warning; critical after `alerts.driveCriticalDays` (90) | `drive_last_synced_at` older than `alerts.driveStaleDays` (30) (or null and the node's profile expects a drive: any node that ever reported an `ark_drive` root) | a newer drive sync |
| `sync.failed` | LAI-CLD-023 | warning | `sync.last_run.result == failed` | next run ok/partial |
| `drill.failed` | LAI-CLD-024 | critical | `drill.result == fail` (warning for `warn`) | next drill pass |
| `ark.tier1_missing` | LAI-CLD-025 | critical | any root `tiers.t1 != ok` | t1 ok |
| `key.missing` | LAI-CLD-026 | warning | enrolled ≥ 10 min and no stored key | key reported |
| `endpoint.down` | LAI-CLD-027 | warning | the gateway's health probe of `https://<publicHost>/health` failed 3 times in a row ([gateway.md](gateway.md) §4.3) | probe ok |
| `catalog.behind` | LAI-CLD-028 | info | node catalog older than approved for ≥ `alerts.catalogBehindDays` | matches |
| `agent.outdated` | LAI-CLD-029 | info | `agentVersion < settings.agent.latestVersion` | equal |
| `node.alerts.<code>` (e.g. `node.alerts.LAI-HW-020`) | (node's own code) | as reported | a node snapshot alert of severity `critical`; one alert row per code, so several codes can be active for one node under the `(node, kind)` unique index | gone from the snapshot |

**Notifications** (EAB notifications slice; registrations in `apps/api/src/localai/localai.notifications.ts`, registered through the notification manifest):

| Event key (permanent) | Channels | Default | Recipients | Body |
|---|---|---|---|---|
| `localai.alert.raised` | browser for every severity; email only for `critical` | on | holders of `lab:operate` | "{node}: {title}. {plain fix}. Open: https://ai.marin.cr/lab/alerts" |
| `localai.alert.cleared` | browser | on | holders of `lab:operate` | "{node}: {title} is resolved." |
| `localai.node.enrolled` | browser, email | on | holders of `lab:admin` | "{node} joined the lab." |
| `localai.command.failed` | browser | on | the requester | "{type} on {node} failed: {fix}" |

Notify after the alert write commits, outside the transaction (EAB dispatch rule). Re-notify an active unacknowledged alert every `alerts.repeatHours`. Channel rule: `critical` → browser + email; `warning` and `info` → browser only (users can change it in their notification preferences).

### 4.10 Later: automated node vhost/cert publishing (phase 2, not in v1)

A **VPS stack-agent**: an EAB worker node running on the VPS host itself (`laictl node start`, systemd unit `localai-vps-agent.service` in `marin-server-infra`, user `root` because it edits `/opt/infra/proxy`), registered as `vps-stack-agent` with `eligibleTypes: ["localai.vps.publish_node", "localai.vps.unpublish_node"]`. These two types are ordinary EAB node-eligible jobs (handler with `nodeResultSchema` + `persistNodeResult`); only this node lists them, so no targeting seam is needed. The executor (`apps/cloud/apps/cli/src/localai/vps-publish.executor.ts`, registered in `defaultExecutors()` of the app CLI) does, idempotently, with each step journalled in the result:
1. Route53 `UPSERT` `<host>` A `<VPS_PUBLIC_IP>` TTL 300, using an IAM key restricted to `route53:ChangeResourceRecordSets` on `<ROUTE53_ZONE_ID>` and `route53:GetChange`, stored in the credential store purpose `localai_route53` (system tier) and brokered per job with a `nodeSecretBroker`.
2. Wait for `INSYNC`, then `dig` the public resolvers.
3. Write the bootstrap HTTP vhost, `docker exec proxy-nginx nginx -t`, reload.
4. certbot webroot issue (same command as the runbook), verify renewal config paths.
5. Render the final Pattern A/B vhost from the node row (tailnet IP and name come in the job's brokered secret, not the params), `nginx -t`, reload; on failure restore the previous file and reload.
6. Validate `/health` 200 and unauthenticated `/v1/models` 401.
7. Result `{ host, steps: [{ name, status, detail }], certificateExpiresAt }`; `persistNodeResult` marks checklist steps 2–5 done.

`<host>` = `ai-n<N><vps.stagingSuffix>.marin.cr` (the host regex is `^ai-n[1-9][0-9]{0,3}(-[a-z0-9]{1,16})?\.marin\.cr$`; the suffix part is allowed only when it equals the configured `vps.stagingSuffix`); the certbot step uses the same `<host>`.

`localai.vps.unpublish_node` (`{ nodeId }`), idempotent, steps journalled the same way:
1. Remove the node's final vhost file (and any bootstrap vhost) from `/opt/infra/proxy`; `docker exec proxy-nginx nginx -t`, then reload (on `nginx -t` failure restore the files and fail).
2. Route53 `DELETE` of the `<host>` A record (skipped if absent).
3. Leave the certificate in place to expire (no revoke; renewal fails harmlessly once DNS is gone).
4. Result `{ host, steps: [...] }`; `persistNodeResult` marks checklist steps 2–5 `todo`.

Until then, [runbooks/05-add-a-node.md](../runbooks/05-add-a-node.md) is the manual path the wizard's checklist mirrors.

## 5. Failure handling and edge cases

| Situation | Behaviour | Code |
|---|---|---|
| Node credential revoked or unknown | EAB 401 on every route; agent stops the link loop, backs off 1 h, shows "Cloud link: credential rejected. Re-enroll with `sudo aictl cloud enroll`." | LAI-CLD-051 |
| Node `disabled` in `/admin/settings/workers` | EAB claim 403; agent keeps heartbeating every 5 min only | LAI-CLD-052 |
| Credential bound to another node | 403 from the binding guard; audit `localai.node.binding_refused`; agent shows the node id mismatch | LAI-CLD-004 |
| Cloud unreachable / DNS fails | Agent retries with jittered backoff 5 s → 5 min; nothing local changes | LAI-CLD-053 |
| Cloud 5xx three times in a row | Same backoff; Doctor shows it | LAI-CLD-053 |
| Clock skew > 5 min | Agent warns; commands still run (leases are server time) | LAI-AGT-012 |
| Snapshot fails schema or contains a secret-like string | 400; agent logs the Zod path (not the value) and keeps sending heartbeats | LAI-CLD-008 |
| Command type not in `node.yaml: cloud.allow` | Never claimed (not in `types`); the UI shows "ai-n3 does not accept this command (disabled on the node)" when the type is absent from the node's `capabilities`-reported allow list | LAI-CLD-057 |
| Params fail the agent's decoder (version skew) | Agent reports `failure` `LAI-CLD-058` with the field path | LAI-CLD-058 |
| Lease lost (node slept mid-command) | Sweeper fails it; a late `result` gets 409 and is dropped | LAI-CLD-059 |
| `model.load` does not fit | Agent `failure` with its own code (e.g. `LAI-CAT-005`, or `LAI-LLM-011` when there is no room without unloading); UI shows that code's fix | (agent code) |
| `mode.offline` | Agent posts result, then deregisters; UI marks the node "Offline mode (set from the cloud)"; further commands for it are refused with 422 | LAI-CLD-034 |
| `agent.update` restarts the agent mid-command | Agent persists `{job_id, type, claim_token, lease_expires_at}` to `/var/lib/localai/cloud/inflight.json` ([agent.md](agent.md) §4.8.6) before exec; the new process renews and reports. Lease 300 s | LAI-CLD-015 on rollback |
| Cloud older than agent (404 on `/localai/config`) | Agent keeps EAB heartbeat only and shows "Cloud needs an update" | LAI-CLD-056 |
| Two snapshots out of order | Older `seq` → 409, ignored | — |
| Worker node pruned by EAB, node returns | `register` creates a new worker node id; the guard re-links `workerNodeId` | — |
| `SECRETS_ENCRYPTION_KEY` missing/wrong | Credentials slice fails at boot (EAB); with a wrong key reads throw → gateway 503 per node; Doctor | LAI-CLD-032 |
| `aictl` missing in the API image | Lint job fails, version stays `draft`; Doctor | LAI-CLD-033 |
| Approving while a node is offline for months | It gets the new catalog whenever it reconnects; the agent's lint decides | — |
| Long-poll requests pile up after an API restart | Each node has one in flight; the agent's client timeout is `waitMs + 10 s` | — |
| Wizard abandoned | `pending` node with a live credential; Doctor warning after 7 days with "Retire or finish ai-n4" | LAI-CLD-031 |

## 6. Security considerations

- **Blast radius of a node credential:** EAB confines it to `/api/nodes/*`. Within that, the binding guard confines Local AI routes to one node. Remaining exposure until SR-2: EAB's own heartbeat/claim/register for other admin-owned worker nodes (display-only effects; no EAB jobs are node-eligible in v1, so claim yields nothing). Routing never trusts EAB health alone (§4.5).
- **Compromised cloud** (T4): can only queue the 14 closed command types with strict params. The node allowlist and its own decoder are the last line. The cloud can't run shell commands, can't enter survival, can't return a node online, and `model.delete` never removes the last verified copy (agent-enforced).
- **Secrets never stored in results or logs:** command results and snapshots are schema-closed and scrubbed; the key travels only on `POST …/key` over HTTPS and is stored encrypted. Request logging for `/api/nodes/*/localai/key` excludes bodies.
- **Tailnet IPs:** stored only in `localai_nodes.tailnet_ip`; returned only to lab:admin with `?reveal=tailnet` (audited); never in snapshots, never in the gateway's public responses, never in logs (the logger redaction list includes the field name).
- **PII:** the cloud stores user emails (EAB). Audit meta never includes prompt text. Gateway usage is metadata only ([gateway.md](gateway.md) §4.7).
- **Destructive UI actions** need lab:admin plus a typed confirmation. Retiring a node requires typing its id.
- **CSRF:** EAB session cookies + same-origin; all mutating routes are JSON-only (`content-type` enforced by Fastify) so form posts fail.
- **CSP:** EAB's nginx CSP (`script-src 'self'`) stays; the catalog editor adds no CDN.
- **Public repo:** every committed example uses placeholders; the VPS vhost snippets are rendered with real values only at runtime.

## 7. Observability

- **Logs** (EAB Nest logger, JSON): keys `node_id`, `worker_node_id`, `command_id`, `command_type`, `status`, `duration_ms`, `error_code`, `catalog_version`. Never: `apiKey`, `token`, `tailnet_ip`, `params` of `logs.tail` results.
- **Audit:** §3.5.
- **Event bus** (`EVENT_BUS`, in-process): `localai.node.state`, `localai.command.enqueued`, `localai.command.settled`, `localai.node.key_reported`, `localai.alert.changed`.
- **Doctor checks** (EAB Doctor, `/admin/settings/doctor`, one provider per check in `apps/api/src/localai/doctor/`): `localai.credentials` (LAI-CLD-032), `localai.aictl` (033), `localai.catalog.approved` (none approved → warn), `localai.nodes.pending` (031), `localai.commands.stuck` (030: any `queued` past expiry not swept within 5 min), `localai.nodes.keys` (026), `localai.settings.nodes` (warn if `nodes.offlineRetentionDays < 365` or `staleHeartbeatSeconds > 90`).
- **Metrics:** none exported in v1 (telemetry off). The Lab overview header shows counts; command durations are in the history table.

**As built (CLOUD-17, #91).** Code: `apps/cloud/apps/api/src/localai/doctor/` (`credentials`, `aictl`, `catalog-approved`, `nodes-pending`, `commands-stuck`, `nodes-keys`, `settings-nodes` as `*.check.ts`, the module `doctor.module.ts` registered in `localai.module.ts`, the shared code map `codes.ts`, `doctor-outcome.ts`, `doctor.constants.ts`). Details to know:

- **One category, `localai`.** All seven checks register themselves with the platform's `DoctorCheckRegistry` in `onModuleInit` and appear on `/admin/settings/doctor` (`GET /api/admin/doctor?category=localai` returns just these). A check's outcome maps the spec's `{ status, code, title, fix }` onto the platform contract: `status` is `pass`, `warn` (a `warning` code) or `fail` (an `error` or `critical` code); `detail` leads with the code's title and `[LAI-CLD-0NN]`, `remedy` is the code's fix text, and `data.code` carries the code string. `codes.ts` is the one map of titles and fixes (the alerts of CLOUD-18 import it); `codes.spec.ts` compares every entry with the row of [doctor-codes.md](doctor-codes.md).
- **`localai.credentials` is read-only, unlike the issue's sketch.** With a stored `localai_node_llama_key` it calls `getSecret` on the first one (by name) and looks only at whether the call resolved; with none stored it encrypts and decrypts a fixed sentinel in memory under the same purpose (no `credentials` row is written, so the issue's throwaway `__doctor_probe` set/get/delete is not done: a Doctor check may not write, and the platform's `doctor-read-only` contract would flag it). The in-memory round trip proves the encryption key is present and well formed, not that it is the key the stored values were written with; only a stored key proves that. A failure reports the error NAME, never its message.
- **`localai.aictl`** runs `aictl --json version` at `LOCALAI_AICTL_PATH` (the issue writes `aictl version --json`, which the real binary refuses as an unknown option: `--json` is a global flag and goes before the command, aictl.md 4.26) with an argv array, an environment of `PATH` only, a 5 s limit and a 64 KB output ceiling; "missing", "not executable" (including a directory), "timed out", "failed" and "unreadable output" are all `LAI-CLD-033` with the reason in `data.reason`. A working binary reports its version only when it is version-shaped.
- **`localai.catalog.approved`** warns when no row has status `approved`; it has no code of its own, and its remedy quotes `LAI-CLD-013` (what a node reports when it cannot download the catalog) as the hint.
- **`localai.nodes.pending`** names the nodes (`Retire or finish ai-n4`, at most five, then "and N more"): `phase = pending`, not retired, created more than 7 days ago.
- **`localai.commands.stuck`** counts `queued` commands whose `expires_at`, and `running` ones whose `lease_expires_at`, passed more than 5 minutes ago (the sweeper runs every 30 s).
- **`localai.nodes.keys`** times "enrolled for 10 minutes" from the newest `localai.node.enrolled` audit row of the node (the node row has no enrollment timestamp), or from the record's creation when that row is gone; a node that is `enrolled`, not retired, has `key_reported_at` null and passed that grace period is named.
- **`localai.settings.nodes`** reads the platform's `nodes` namespace (`SystemSettingsService.getNamespace('nodes')`, which never creates the row) and warns when `offlineRetentionDays < 365` or `staleHeartbeatSeconds > 90`; it has no `settingsPath` because the starter does not mount the worker-nodes page.
- **No output carries a secret.** Every string an outcome holds goes through `scrubText`, which replaces a 64-hex string, a `nod_`/`pat_`/`lai_` token and an IPv4 address; no outcome contains a node key, a digest or a tailnet address even when a node id or an error message does.
- **Tests.** Unit, one file per check (`*.check.spec.ts`: pass, warn and fail paths over a fake store, and the platform's `describeDoctorCheckConformance` for each), `doctor-tripwire.spec.ts` (a recording client that refuses writes sees none, a scan of the check sources for writes, bait values never returned), `codes.spec.ts`, `doctor-outcome.spec.ts`; HTTP over the whole app with a mocked database and the real cipher: `test/localai/doctor-routes.spec.ts` (seven checks listed, administrator required, `LOCALAI_AICTL_PATH=/nonexistent` gives `LAI-CLD-033`, an unreadable stored key gives `LAI-CLD-032`, nothing written); real SQL: `test/localai/doctor.db.spec.ts`.

## 8. Doctor codes (`LAI-CLD`)

Block allocation in the `LAI-CLD` area: **001–049** defined here (cloud-side checks and alerts, plus node-visible codes produced by the cloud's answers); **050–069** reserved for the agent's link client and defined in [agent.md](agent.md) §8.5 (not enrolled 050, credential rejected 051, disabled 052, unreachable 053, registration refused 054, vitals fallback 055, no state endpoint 056, command not allowed 057, invalid command 058, lease lost 059, key not synced 060, duplicate node id 061, non-https URL 062, command stopped because the node is going offline 063). This spec uses those codes where the cloud side shares the meaning (for example a lease lost on the cloud side is also `LAI-CLD-059`).

| Code | Sev | Meaning | Plain-language fix |
|---|---|---|---|
| LAI-CLD-004 | error | The credential belongs to a different node id | Check `node_id` in `/etc/localai/node.yaml`, or enroll with the credential made for this node |
| LAI-CLD-008 | warning | The cloud refused this computer's status report | Update the agent (`sudo aictl update`) or tell the admin; local use is not affected |
| LAI-CLD-012 | error | A command failed while running | Read the code in the command's error for the exact fix, then send it again |
| LAI-CLD-013 | warning | Couldn't download the catalog from the cloud | The node keeps its current catalog. It retries at the next sync |
| LAI-CLD-015 | error | An agent update failed and the old version was restored | Nothing is broken. Tell the admin; the update will be retried later |
| LAI-CLD-020 | warning | Node stopped reporting | Check the computer is on and connected. If it was moved to Offline mode on purpose, ignore this |
| LAI-CLD-021 | critical | Node offline for more than a day | Turn the computer on or check its network. If it is retired, retire it in the cloud |
| LAI-CLD-022 | warning (critical after 90 days) | The AI-ARK drive hasn't been synced for more than 30 days | Plug the AI-ARK drive into the node and press "Sync now" (or run `aictl ark sync`) |
| LAI-CLD-023 | warning | The last ark sync failed | Open the node's Ark tab, read the sync error code and follow its fix, then press "Sync now" |
| LAI-CLD-024 | critical (warning for a drill warn) | The monthly drill failed | Open the node in the cloud or run `aictl drill` on it and follow the failed check's fix |
| LAI-CLD-025 | critical | Essential (tier 1) ark content is missing | Press "Sync now" while online, or import from the AI-ARK drive (`aictl ark import`) |
| LAI-CLD-026 | warning | The cloud has no API key for this node, so the gateway can't use it | Press "Refresh status" on the node; if it persists run `sudo aictl cloud resend-key` on the node |
| LAI-CLD-027 | warning | The node's public address (ai-nN.marin.cr) is not answering | Check `tailscale serve status` on the node and the vhost on the VPS (runbook 05) |
| LAI-CLD-028 | info | The node uses an old catalog | Press "Refresh catalog" on the node |
| LAI-CLD-029 | info | A newer agent is available | Admin: Lab → node → Update agent |
| LAI-CLD-030 | warning | Commands are waiting longer than they should | Admin: check that the cloud's background jobs run (Admin → Jobs) |
| LAI-CLD-031 | warning | A node was added but never connected | Finish the install on that computer, or retire the node in the cloud |
| LAI-CLD-032 | critical | The cloud can't read its stored keys (encryption key missing or wrong) | Admin: restore `SECRETS_ENCRYPTION_KEY` from the backup, restart, then "Ask all nodes to re-send keys" |
| LAI-CLD-033 | error | The catalog checker (`aictl`) is missing on the cloud server | Admin: redeploy the cloud app (`laictl deploy update`) |
| LAI-CLD-034 | info | The node is in Offline or Survival mode, so it won't take cloud commands | Someone at the computer must switch it back to Online |
| LAI-CLD-035 | error | The node is retired or not enrolled yet | Finish enrolling it, or add a new node |

## 9. Test plan

### Unit (Jest, `apps/cloud/apps/api/src/localai/**/*.spec.ts`)
| Area | Cases |
|---|---|
| `COMMANDS` schemas | each type: a valid example parses; an unknown key fails; each bound (lines 0/501, 201 items, bad model id `Qwen`, `confirm` ≠ `model_id`) fails; defaults applied (`wait: true`, `lines: 200`) |
| State schema | full ai-n3 fixture parses; 201 models fails; `node_id` mismatch → 400; a tailnet-range IPv4 string (built at test time, never a literal in the repo) anywhere → `secret_like_value`; `"lat_" + 43 chars` → refused |
| Binding guard | session → 403; unknown node name → 404; other owner's worker node → 403; credential of ai-n1 on ai-n2 → 403 + audit; pending → enrolled transition once; worker id re-link; digest format matches `NodeCredentialService` |
| Command policy | permission per type (viewer 403 everywhere, contributor 403 on admin types); dedup collapse returns existing row; node offline mode → 422; model not in snapshot → 422 |
| Claim | FIFO; types filter; one running at a time; expired not claimed; long-poll wakes on event; long-poll returns empty on timeout; second concurrent claim returns empty |
| Lease | renew with wrong token → 409; renew after expiry → 409; hard cap; result after sweeper failed → 409 |
| Health | table over (eab health × age × mode × phase) → health/routable |
| Ark colour | table over tiers × drive age × last sync |
| Alerts | raise/refresh/clear; expected silence suppresses; one active per (node, kind); repeat notification after `repeatHours`; info never emails |
| Catalog | import rejects path traversal and symlinks; version monotonic; one approved (index); lint exit codes 0/2/other |

### Integration (`npm run test:db --workspace=api`, real Postgres, fake agent)
- `test/localai/node-link.db.spec.ts`: mint credential via wizard route, `register` (EAB route), `config`, `POST state`, `POST key` (then `CredentialsService.getSecret` returns it; logs captured contain no 64-hex), `GET catalog` 404 then 200 then 304.
- `test/localai/commands.db.spec.ts`: enqueue → claim (long-poll unblocks within 200 ms of enqueue) → renew → result → succeeded; failure path; sweeper expiry and lease loss; two concurrent claimers (two "agents" with the same credential) never get the same row.
- `test/localai/wizard.db.spec.ts`: create node returns token once; second GET never shows it; checklist checks with a fake DNS/TLS/HTTP resolver injected through a port.
- `test/localai/conformance.spec.ts`: EAB `runPlatformConformance` still passes with our fragments (ownership registry, user-owned data registry, permissions declared, no un-`@Auth` route except the node routes guarded by EAB's node allowlist).
- Agent conformance: `agent/internal/cloud/fake_cloud_test.go` (owned by agent.md) replays the JSON examples of §3.6–3.7 from `apps/cloud/apps/api/test/fixtures/localai/*.json`; both suites load the same fixture files.

### Manual (owner, after deploy)
1. `curl -s https://ai.marin.cr/api/health/ready` → `{"data":{"status":"ok",…}}`.
2. Sign in with `<ADMIN_EMAIL>` → Lab page shows "No nodes yet · Add node".
3. Add node `ai-n3` (migration of an existing node: same wizard; installer `--migrate`), run the one-liner on ai-n3, paste the credential. Expected within 30 s: card ai-n3 "Healthy · Online", loaded `qwen3.5-9b-q4km`, Keys tab shows a hint.
4. On ai-n3: `aictl cloud status` → `linked: yes, node ai-n3, last heartbeat <15 s`.
5. Lab → ai-n3 → Models → Unload, then Load `qwen3.5-9b-q4km`. Expected: command "Running", then "Done" within ~40 s; on ai-n3 `aictl models` shows it loaded.
6. Logs tab → `localai-llama`, 50 lines → lines appear; search them for the key's first 8 hex chars: not found.
7. With a credential minted for ai-n4 (pending), call `curl -H "Authorization: Bearer <ai-n4 nod_>" -X POST https://ai.marin.cr/api/nodes/<ai-n3 worker id>/localai/state -d '{}'` → `403` with `LAI-CLD-004`.
8. Lab → ai-n3 → Go offline… → confirm. Expected: ai-n3 local UI shows Offline mode; cloud card grey "Offline mode (set from the cloud)"; Load buttons disabled with the LAI-CLD-034 explanation. On ai-n3 `aictl mode online` restores it.
9. Catalog → Import `catalog-<version>` → lint ✔ → Approve with "push to nodes" → ai-n3 reports the new `catalog_version` within a minute.

## 10. Implementation breakdown

1. **CLOUD-01 Scaffold `apps/cloud` from the EAB starter** — Scope: §3.1 commands, nested `.git` removed, samples deleted, `slices.json` per §3.2, deleted slice code for sharing/exports/android-app, root `.gitignore` guard, `apps/cloud/packages/localai-ui` symlink. AC: `npm run platform:check && npm run typecheck && npm test && npm run build` pass in `apps/cloud`; `find apps/cloud -name .git` is empty; `/api/ai/*` and `/api/documents` return 404. Deps: none.
2. **CLOUD-02 Monorepo CI for the cloud** — Scope: `.github/workflows/cloud-ci.yml` (§3.3), Renovate rule with `matchFileNames`, `aictl` build step. AC: a PR touching only `agent/` does not run it; a PR touching `apps/cloud/` runs and passes; redaction job unaffected. Deps: CLOUD-01.
3. **CLOUD-03 Identity and permissions** — Scope: `LOCALAI_PERMISSIONS`, registration in `permissions.ts`, seed, `TENANCY_MODE=single`, allowlist doc in [runbooks/10-lab-operations.md](../runbooks/10-lab-operations.md). AC: seeded grants match the §3.4 matrix (test); viewer gets 403 on a `lab:operate` route; `<ADMIN_EMAIL>` placeholder only in committed files. Deps: CLOUD-01.
4. **CLOUD-04 Prisma fragments and migration** — Scope: §3.5 models, raw-SQL partial indexes and allowlist, ownership + user-owned registrations, EAB node settings seed (`staleHeartbeatSeconds=45`, `offlineRetentionDays=3650`). AC: `prisma:migrate:dev` clean; conformance suite passes; index uniqueness tests (one approved catalog, one active dedup, one active alert). Deps: CLOUD-01.
5. **CLOUD-05 Node binding guard + `config` handshake** — Scope: §4.2, `GET …/localai/config`, audit `binding_refused`, enrolled transition. AC: all guard unit cases of §9; digest-format test against `NodeCredentialService`. Deps: CLOUD-04.
6. **CLOUD-06 State snapshot ingestion** — Scope: `LocalaiStateSnapshotV1` (incl. `ReadyReportV1`, `DrillResultsV1`, `serve_pattern: none`), scrubber, seq ordering, `localai_node_models` projection, `localai.node.state` event, shared JSON fixtures for the agent. AC: §9 state cases; snapshot ≤ 256 KiB enforced; fixture files published under `test/fixtures/localai/`. Deps: CLOUD-05.
7. **CLOUD-07 Node keys in the credential store** — Scope: purpose `localai_node_llama_key`, `POST …/localai/key`, `keyHint`, log exclusion, "ask all nodes to re-send keys" action. AC: stored value round-trips; captured logs contain no 64-hex; retire deletes it. Deps: CLOUD-05.
8. **CLOUD-08 Command plane (node side)** — Scope: `COMMANDS` registry, claim (SKIP LOCKED + long-poll), renew, result, failure, sweeper job, per-type leases. AC: §9 claim/lease unit and `commands.db.spec.ts` integration pass; concurrent claimers never double-claim. Deps: CLOUD-05.
9. **CLOUD-09 Command API for the UI** — Scope: `POST/GET/DELETE` command routes, permission matrix §3.7, refusals (§3.9), audit `requested/cancelled/settled`, `localai.command.failed` notification hook. AC: permission table test; dedup returns 200 with active row; offline node → 422 LAI-CLD-034. Deps: CLOUD-08, CLOUD-03.
10. **CLOUD-10 Catalog import, lint, approve, serve** — Scope: §4.6 jobs, `aictl` in the API image, node `GET …/localai/catalog` with ETag/304, approve + notify nodes. AC: import of a real tag in a db test with a local tarball fixture; lint uses the real `aictl` in CI; 304 on matching ETag. Deps: CLOUD-04, CLOUD-05.
11. **CLOUD-11 Web: Lab overview** — Scope: `/lab` page, node cards, health/ark/mode chips (§3.11, §4.5, §4.7), shared components in `packages/ui`. AC: renders the 3-node fixture exactly as the wireframe's information; colour + word + icon on every chip; a11y lint passes. Deps: CLOUD-06.
12. **CLOUD-12 Web: Node detail tabs** — Scope: Models, Ark, Services, Logs, Keys, Settings tabs wired to command API; typed confirmations; tailnet IP reveal (audited). AC: Playwright test drives Load/Unload against a fake agent and sees "Done"; viewer sees no action buttons. Deps: CLOUD-09, CLOUD-11.
13. **CLOUD-13 Web: Catalog editor and approval** — Scope: list, diff, textarea editor, lint panel, approve dialog. AC: editing a draft and saving re-lints; Approve disabled unless `valid`. Deps: CLOUD-10, CLOUD-11.
14. **CLOUD-14 Add-node wizard** — Scope: create + mint + bind, show-once token, one-liner, enrolled/key auto-steps, checklist with checks behind a `NetworkProbe` port, rendered vhost snippets, disabled phase-2 button. AC: `wizard.db.spec.ts`; checks green against a fake probe; token never retrievable after step 2. Deps: CLOUD-05, CLOUD-07, CLOUD-11.
15. **CLOUD-15 Alerts and notifications** — Scope: evaluator job + cron, `localai_alerts`, notification registrations, Alerts page, Ack. AC: §9 alert cases; raising sends exactly one browser notification; ack stops repeats. Deps: CLOUD-06, CLOUD-11.
16. **CLOUD-16 Audit page** — Scope: `/api/localai/audit`, `/lab/audit`, filters, links. AC: every action in §3.5 appears after the integration flow; non-admin 403. Deps: CLOUD-09.
17. **CLOUD-17 Cloud Doctor checks** — Scope: §7 Doctor providers with LAI-CLD codes. AC: each check has a pass and a fail unit test; all appear on `/admin/settings/doctor`. Deps: CLOUD-07, CLOUD-08, CLOUD-10.
18. **CLOUD-18 Deploy to ai.marin.cr** — Scope: Route53 record, `cloud-deploy-branch.yml`, `proxy.streamPaths`, API image with `aictl`, env table in `app.env.example`, a "Deploying and updating the cloud" part in [runbooks/10-lab-operations.md](../runbooks/10-lab-operations.md), link the SR-1/SR-2/SR-3 upstream issues filed in CLOUD-01 from §3.12. AC: `laictl deploy install` succeeds on the VPS; manual test steps 1–2 pass; `deploy/cloud` branch has no symlink and passes redaction. Deps: CLOUD-01, CLOUD-02, CLOUD-03, CLOUD-04, CLOUD-05, CLOUD-06, CLOUD-07, CLOUD-08, CLOUD-09, CLOUD-10.
19. **CLOUD-19 (later) VPS stack-agent publishing** — Scope: §4.10 executor, job types, Route53 IAM credential + broker, enable the wizard button. AC: publishing a test host with `vps.stagingSuffix` set (e.g. `ai-n3-staging.marin.cr`) end to end, then unpublishing it; rollback restores the previous vhost on `nginx -t` failure. Deps: CLOUD-14, CLOUD-18.
20. **CLOUD-20 (later) Move commands onto EAB targeted jobs** — Scope: after SR-1 + SR-2 ship: handlers per type, `commandPlane: eab`, `localai_commands.jobId`. AC: agent conformance suite passes against both planes; no command lost during the switch (drain queued first). Deps: SR-1, SR-2, CLOUD-08.

## 11. Open questions

| Question | Proposed default |
|---|---|
| Back up the cloud DB with EAB `db-backup` (needs `storage` + an S3 bucket)? | **Off in v1.** The cloud is rebuildable: catalog is in git, nodes re-register, keys are re-sent on request. Lost on rebuild: gateway keys, usage history, audit. Revisit when an S3 bucket exists (turn on `storage` + `db-backup`, one line each in `slices.json`). |
| Should viewers see the Lab at all? | Yes (lab:read is harmless and helps family members check the ark is ready). |
| Notify by email for `warning` alerts? | Browser only for `warning`; email for `critical`. Implemented as the event's default channel set; users can change it in their notification preferences. |
