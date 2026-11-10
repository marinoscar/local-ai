# Security model

> **Status:** approved design. Canonical paths and token formats are in [overview.md](overview.md) §3. Code areas: agent ([agent.md](../specs/agent.md)), cloud ([cloud-app.md](../specs/cloud-app.md)), gateway ([gateway.md](../specs/gateway.md)), desktop ([desktop.md](../specs/desktop.md)).

## 1. Threat model (what we defend against)
| # | Threat | Mitigation |
|---|---|---|
| T1 | Strangers on the internet using the GPU nodes for free | Every inference path needs a key: the node API key on `ai-nN.marin.cr`, or a `lai_` key on the gateway. Rate limits apply on the gateway, and later on the nginx node vhosts |
| T2 | A leaked node API key | A key is per node and rotates with one command (`aictl key rotate`). The cloud stores it encrypted and pushes the new key to the gateway |
| T3 | A leaked `nod_` cloud credential | EAB confines it to the `/api/nodes/*` allowlist. Revoking it in the cloud UI is immediate. The credential can only claim jobs for its own node |
| T4 | A malicious cloud command (compromised cloud) | The agent accepts only the job types it knows, with schema-validated params. It **never** executes shell commands from the cloud. Survival mode can't be triggered remotely. Each destructive command type is limited by a node-side policy in `node.yaml: cloud.allow` |
| T5 | Another local user or process on the node | The agent API is on loopback **and** needs a token. The token file is readable only by `localai-operators` |
| T6 | A non-expert accidentally destroying content | Destructive operations need confirmation and the PIN, and sync never deletes the last verified copy ([ark-sync.md](../specs/ark-sync.md) §4.10) |
| T7 | Tampered or corrupt downloads | Every artifact is verified by sha256 against the catalog. Checksums come from the source's own published value (HF LFS sha256, Kiwix `.sha256`) **and** are pinned in the catalog. A mismatch quarantines the file |
| T8 | Lab inventory published through this public repo | Redaction rules (§5), `scripts/check-redaction.sh` and gitleaks in CI |
| T9 | A stolen laptop | Out of scope for software. Recommend full-disk encryption (LUKS) at OS install ([runbooks/05-add-a-node.md](../runbooks/05-add-a-node.md)). The ark drive is unencrypted **by design**, so anyone can use it in an emergency. Don't store personal secrets on it beyond `inventory.local.yaml` |

## 2. Credentials inventory
| Credential | Where it lives | Who can read it | How it's created | Rotation |
|---|---|---|---|---|
| llama API key (64 hex) | `/etc/localai/api-key` (root 0600), injected with `LoadCredential=llama-api-key:/etc/localai/api-key` | root, plus llama-server via `%d/llama-api-key` | installer: `openssl rand -hex 32` (or reuse the existing `/etc/llama-qwen/api-key` on migration) | `aictl key rotate`: write the new key, restart llama, report it to the cloud (encrypted channel = the HTTPS node plane), print the clients to update |
| Agent token `lat_…` | `/etc/localai/agent-token` (root:localai-operators 0640) | root, operators, ai-desktop (running as an operator) | agent on first start, if absent | `aictl token rotate` (root). ai-desktop re-reads it on a 401 |
| Cloud credential `nod_…` | `/etc/localai/cloud-credential` (root 0600) | root (the agent) | cloud UI **Add node** shows it once, and `aictl cloud enroll` stores it | revoke in the cloud, then enroll again |
| Admin PIN | `/etc/localai/admin-pin` (argon2id; 4–8 digits) | root | set by the installer, or by `aictl pin set` (root) | `aictl pin set` (root). Forgotten PIN: [runbooks/07-troubleshooting.md](../runbooks/07-troubleshooting.md) |
| Gateway keys `lai_…` | cloud DB, stored as sha256 hashes, with the prefix kept for display | cloud only | the user issues it in the cloud UI, shown once | revoke and issue again |
| Node keys held by the cloud | EAB encrypted credential store (deployment scope), purpose `localai_node_llama_key`, name `<node-id>` (EAB names may not contain `:`; [cloud-app.md](../specs/cloud-app.md) §3.8) | cloud API (gateway) | reported by the agent at enroll and rotate time | follows the node |
| HF token (optional) | `/etc/localai/hf-token` (root 0600) | the agent (sync) | operator, optional, for rate limits: `sudo aictl ark hf-token set` (no-echo prompt or stdin, never an argument) | `sudo aictl ark hf-token set` again (root) |

**Rules:**
- No credential ever appears in logs, `aictl status`, heartbeats or error messages. Each is shown as `prefix…last4`, where the prefix is `lat_`, `nod_` or `lai_`, or the first 4 hex characters for the llama key.
- `aictl key show` is the **only** command that prints the full llama key. It requires the agent token and writes an audit event. The desktop's "Copy API key" button calls the same endpoint.
- The agent API rejects any request where the `Host` header isn't `127.0.0.1:7070` or `localhost:7070`, as a DNS-rebinding defence. It also rejects any browser request whose `Origin` is not same-origin or the ai-desktop `localai://app` origin (§3).

## 3. Agent API authentication
- `Authorization: Bearer lat_…` on every `/api/v1/*` route except `GET /api/v1/health`.
- The **local UI served by the agent** is a browser page. It authenticates with an **HttpOnly, SameSite=Strict session cookie**, which it gets in one of two ways:
  - **(a) From ai-desktop.** The desktop reads the token file and calls `POST /api/v1/session` with the bearer token. The cookie is set on the loopback origin.
  - **(b) From a plain browser.** On `http://127.0.0.1:7070/`, the page asks for a one-time **local login code**. The operator gets it with `aictl ui-code`, which prints a 6-digit code valid for 5 minutes, or reads it on the desktop. This lets the portal work in any browser if Electron is broken.
- **Survival exception (deliberate):** these routes work **without any auth** so that anyone at the keyboard can survive:
  - `GET /portal`
  - the Portal's static assets
  - the read-only status `GET /api/v1/portal/status`
  - `POST /api/v1/mode/survival`
  - the links to llama WebUI, Kiwix, maps and docs (`/library/`, `/maps/` including `/maps/search`, `/maps/nearest` and `GET/PUT/DELETE /maps/whereami`, `/docs/`)

  They are still loopback-only. The Portal's "Chat" link opens the llama WebUI. That WebUI needs the API key, so the Portal handles it with a loopback-only, key-injecting reverse proxy at `/chat/`. The proxy is **enabled only in `survival` mode** or after a session login. See [survival-portal.md](../specs/survival-portal.md) §4.4.
- **Portal session (survival only).** In survival mode, `GET /portal` also sets a restricted **Portal session** cookie (`lai_portal`, HttpOnly, SameSite=Strict, 12 h sliding). It is a separate principal (`portal`), not an operator session. It allows only:
  - `/chat/*` (through the key-injecting proxy),
  - speech: `POST /api/v1/speech/stt`, `POST /api/v1/speech/tts`, `GET /api/v1/speech/voices`,
  - `POST /api/v1/models/{id}/load` for models that are `loadable` only (body forced by the agent),
  - `GET /api/v1/events` filtered to `mode.*`, `model.*`, `service.state`, `drive.*`, `alert.*`,
  - the buttons of a fix ([doctor-codes.md](../specs/doctor-codes.md) §1.7): `POST /api/v1/ark/import` with its body replaced by the safe default (the first usable AI-ARK drive, the profile's items, no catalog, no `node.yaml` change; an import only copies verified items in and never deletes), 3 per hour; `POST /api/v1/services/kiwix/restart` and `POST /api/v1/services/whisper/restart`, 6 per hour together. Never `llama`, never a stop, never sync, prune or eject: those stay operator routes. Why this is safe: each is something a person at the keyboard needs in an emergency, each is idempotent or bounded by a run slot, and the worst a hostile page could do through it (it would also need the Portal cookie and a same-origin `Origin`) is restart a companion service a few times or copy content the profile already wants.

  Everything else answers `401 LAI-SVC-110`. Mutating requests also need a same-origin `Origin`. Every Portal session is dropped when the mode leaves survival. Details: [survival-portal.md](../specs/survival-portal.md) §3.3, [agent.md](../specs/agent.md) §3.9.

## 4. Cloud ↔ node
- **The agent always initiates.** It makes outbound HTTPS calls to `https://ai.marin.cr/api/nodes/*` with its `nod_` credential (EAB worker-node protocol). It never opens an inbound port.
- **Commands are jobs.** Each command type has a strict schema. The agent's allowlist (`node.yaml: cloud.allow`) defaults to:
  `[status.refresh, model.load, model.unload, model.set_default, model.download, model.delete, ark.sync, ark.verify, mode.offline, key.rotate, service.restart, logs.tail, catalog.refresh, agent.update, ensure.run]`.
  An operator can remove entries, for example `model.delete` or `agent.update` on the ark laptops.
- `agent.update` downloads a release binary from GitHub Releases. The agent verifies its sha256 against the `SHA256SUMS` signed in the release, and verifies the minisign signature with the public key embedded in the agent ([agent.md](../specs/agent.md) §4.10). The agent keeps the previous binary and rolls back if the new one fails its health check within 60 s. Who holds the signing key, how releases are made and how the key is rotated: [release-signing.md](release-signing.md).

## 5. Redaction (public repo)
- **Forbidden in git:** every value in `inventory.local.yaml`, all credentials, and any address in the Tailscale CGNAT range (100.64/10). That covers:
  - tailnet IPs,
  - the tailnet name `tail…`,
  - the VPS public IP and hostname,
  - the Route53 zone ID,
  - LAN IPs,
  - drive UUIDs,
  - email addresses (except `noreply@…` in commit trailers).
- **Allowed:** public hostnames under `marin.cr`, tailnet machine names, hardware model names, model ids, ports and paths.
- **Placeholders** are written `<UPPER_SNAKE_CASE>` and listed in [`inventory.example.yaml`](../../inventory.example.yaml).
- **Tooling:**
  - `scripts/check-redaction.sh` (pre-commit and CI) fails on:
    - any `100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.\d+\.\d+` address,
    - `tail[0-9a-f]{6}`,
    - `Z0[0-9A-Z]{10,}`,
    - 64-hex strings outside test fixtures, except a catalog pin (next bullet),
    - `\b(lat|nod|lai|pat)_[A-Za-z0-9_-]{20,}` (word boundary, so `localai_*` metric names are fine),
    - and, if `inventory.local.yaml` exists locally, any of its literal values.
  - gitleaks runs in CI with the default ruleset plus the same patterns (`.gitleaks.toml`).
  - **`.gitleaksignore`.** The repo-root file lists gitleaks fingerprints (`<commit>:<file>:<rule>:<line>`) of findings that are accepted because the commit is history we do not rewrite and the current tree is already clean (today: one test placeholder, a model id in a variable named `key`, in a spec of PR #395). It never replaces fixing the tree: an entry is added only after the fix commit, with a comment saying why, and `.gitleaks.toml` stays unchanged. Verify with `gitleaks detect --config .gitleaks.toml --log-opts="origin/main..HEAD"`.
  - **Placeholder claim token.** `.gitleaks.toml` allowlists exactly one invented UUID (listed in that file) that the cloud command-plane fixtures and specs use as a claim token. The wire contract requires a UUID and a shared JSON fixture cannot generate one, and the default ruleset flags it as a `generic-api-key` next to the key name `claimToken`. It is an exact-value allowance: any other high-entropy value in those files is still a finding. (Decision for issue #82.)
  - **Catalog sha256 pins.** The catalog must carry real upstream digests, so in exactly `catalog/ark.yaml` and `catalog/models.yaml` a 64-hex value is allowed when it is the value of a `sha256:` key. A bare 64-hex string, a token, an IP or an email in those files is still a leak, and every other file (including other files in `catalog/`) stays strict. A digest is public integrity data, not a credential; the rule is path- and key-scoped so a real key pasted there is still caught. `scripts/tests/test-redaction.sh` proves the limits. The default gitleaks ruleset does not flag these pins, so `.gitleaks.toml` is unchanged. (Decision for issue #251.)
  - **npm lockfile `deprecated` text.** In `package-lock.json` files only, the value of a `"deprecated":` line is third-party prose (a package author may put a contact address in it) and is not scanned. Every other line of a lockfile, and every other file, stays strict. `scripts/tests/test-redaction.sh` proves the limits. (Decision for issue #75.)
  - **Generated cloud lock files, the RFC 1918 range, `example.com`.** In exactly `apps/cloud/apps/api/prisma/platform.lock` and `apps/cloud/infra/platform-infra.lock.json`, a quoted 64-hex value is a public file digest, not a key. The literal range `192.168.0.0/16` (a network, not a host) is ignored everywhere; a `192.168.x.y` host still leaks. An address at `example.com` is allowed beside `example.test` (both reserved by RFC 2606). `scripts/tests/test-redaction.sh` proves the limits. (Decision for issue #75.)
  - Modes: no argument scans the tree, `--staged` scans the git index (pre-commit, installed by `scripts/install-hooks.sh`), `--file <path>` scans one file (tests). Output is `LEAK <file>:<line>` and never the matched value. Email addresses are also forbidden, except `noreply@…` and `…@example.test`.
  - `agent/vendor/` (third-party Go source, byte-for-byte what `go mod vendor` writes and CI verifies) is not scanned: the license, AUTHORS and MAINTAINERS files of a dependency name real people, and nothing there is ours.
  - Must-fail test fixtures live in `scripts/tests/redaction/` and are skipped by tree and staged scans only while `scripts/tests/redaction/.redaction-fixtures` exists. `scripts/tests/test-redaction.sh` proves each fixture fails and each look-alike passes; CI runs it.
- `aictl docs render` is the reverse operation. It fills placeholders from `inventory.local.yaml` into **untracked** output (`/srv/localai/docs`, plus the ark drive).
- **Placeholder → inventory mapping** (used by `aictl docs render`, [aictl.md](../specs/aictl.md) §4.22). There is no hand-maintained table. The renderer loads `inventory.example.yaml` (shipped in the docs tree) and pairs every scalar whose value is a placeholder `<UPPER_SNAKE>` with the **same YAML path** in `inventory.local.yaml` (list entries are matched by their `id` key, e.g. `nodes[id=ai-n1].tailnet_ip` ↔ `<TAILNET_IP_AI_N1>`; a list without `id`, such as `ark_drives`, is matched by `label`, then `name`). A placeholder with no counterpart in the local inventory stays unchanged (`LAI-CLI-013`). Inventory lookup order: `/etc/localai/inventory.local.yaml` → `<ark-drive>/inventory.local.yaml` → `./inventory.local.yaml`. A few placeholders come from node state, not the inventory (`<NODE_ID>`, `<EMERGENCY_NUMBER>`, `<BRAINS>` (the card template also writes `<BRAINS_INSTALLED>`), `<CATALOG_VERSION>`, `<RENDER_DATE>`); they are filled on the survival card only, because the other documents use some of the same names as prose.
- **Secret marker.** A field is secret when its name is listed in the top-level `secret_fields` list of `inventory.example.yaml` (any depth; today `owner_email, tailnet, route53_zone_id, hostname, public_ip, tailnet_ip, lan_ip, fs_uuid`; `scripts/check-redaction.sh` uses the same list). Independently of that list, the renderer has a fixed built-in secret list that always wins: every tailnet IP (`<TAILNET_IP_*>`), `<TAILNET>`, `<ROUTE53_ZONE_ID>`, `<VPS_PUBLIC_IP>`, `<VPS_HOSTNAME>`, LAN IPs (`<LAN_IP_*>`), and every email field. Secret placeholders are filled only in output written under `/srv/localai/docs` or the ark drive; the **survival card** refuses any secret placeholder ([survival-portal.md](../specs/survival-portal.md) §4.12). `<DESKTOP_USER>` comes from the per-node inventory field `nodes[id=<node>].desktop_user` (not secret), falling back to the primary desktop user recorded by the installer in `/var/lib/localai/install/state.json`.

## 6. Hardening backlog (tracked as issues)
- nginx rate limits on `ai-nN.marin.cr` vhosts, and Tailscale ACLs that allow only the VPS to reach node listeners. Both are listed as "not yet done" in [source/vps-ai-nodes-setup.md](../source/vps-ai-nodes-setup.md).
- The blueprint flags the VPS's public Postgres 5432 exposure ([source/ai-lab-blueprint.md](../source/ai-lab-blueprint.md)). The cloud app must use the private DB network and never depend on the public 5432.
