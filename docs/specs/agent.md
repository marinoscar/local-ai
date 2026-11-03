# Spec: Node agent (`aiagent`)

> **Status:** approved design · **Epic:** E1 · **Code:** `agent/` (`agent/cmd/aiagent`, `agent/internal/*`, `agent/ui`, `agent/api/openapi.yaml`) · **Doctor areas:** `LAI-AGT`, `LAI-NET`, `LAI-HW` (and `LAI-SEC`, see §8.4; agent-side cloud link codes `LAI-CLD-050..069`, see §8.5)
> Canonical names: [overview.md](../architecture/overview.md) §3. Modes: [modes.md](../architecture/modes.md). Security: [security-model.md](../architecture/security-model.md). Decisions: [ADR-0001](../decisions/0001-agent-in-go.md), [ADR-0002](../decisions/0002-llama-router-mode.md), [ADR-0003](../decisions/0003-outbound-only-cloud-link.md), [ADR-0006](../decisions/0006-standalone-offline.md).

## 1. Purpose and scope

`aiagent` is the one long-running program on every AI node. It is the **single source of truth on the node**. Everything else (the `aictl` CLI, the local UI, the Survival Portal, ai-desktop, the cloud) reads and changes node state only through the agent.

The agent:
1. owns the node configuration (`/etc/localai/node.yaml`) and runtime state (`/var/lib/localai/state.json`);
2. serves the **REST API** `/api/v1/*` on `127.0.0.1:7070` and on `unix:/run/localai/agent.sock`;
3. serves the embedded **local UI** (`/`), the **Survival Portal** (`/portal`) and the proxies `/chat/`, `/library/`, `/maps/`, `/docs/`;
4. implements the **mode state machine** of [modes.md](../architecture/modes.md);
5. supervises `localai-llama.service`, `localai-kiwix.service` and `localai-whisper.service` through systemd, and runs piper per request;
6. orchestrates model load/unload through the llama-server router ([llama-router.md](llama-router.md));
7. hosts the **ark sync engine** ([ark-sync.md](ark-sync.md)) and the **drill** ([drill.md](drill.md));
8. detects hardware (GPU, VRAM, RAM, disk, battery, thermal);
9. manages **Tailscale Serve** listeners for port 8080, and only those;
10. runs the **cloud link**, an EAB worker-node client ([ADR-0003](../decisions/0003-outbound-only-cloud-link.md));
11. updates itself from signed GitHub releases;
12. runs **doctor** checks and raises alerts with doctor codes.

The same binary is also the CLI: invoked as `aictl` it behaves as described in [aictl.md](aictl.md).

### Non-goals
- **No inference code.** The agent never loads a model itself. llama-server, whisper.cpp and piper do the work.
- **No LAN listener.** Nothing binds to a non-loopback address ([ADR-0006](../decisions/0006-standalone-offline.md)).
- **No inbound cloud control.** The cloud never connects to the agent. It only hands out jobs the agent pulls.
- **No shell execution on behalf of a remote party.** Every external command the agent runs has a fixed argv built in code (§6).
- **No multi-node logic.** One agent manages one node.
- **No chat UI.** `/chat/` proxies the llama.cpp WebUI.
- **Content download logic** is specified in [ark-sync.md](ark-sync.md); **companion service units** in [companion-services.md](companion-services.md); **Portal UX** in [survival-portal.md](survival-portal.md). This spec defines only the hooks the agent provides for them.

## 2. Concepts and glossary

| Term | Meaning |
|---|---|
| **Principal** | Who is calling the API: `anonymous`, `portal` (a Portal session cookie `lai_portal`, issued only in survival mode, narrow scope: [survival-portal.md](survival-portal.md) §3.3), `operator` (bearer token, session cookie, or a socket peer in `localai-operators`), `admin` (socket peer uid 0, or an operator request carrying a valid PIN grant), `cloud` (a job claimed by the cloud link), `system` (timers, startup) |
| **PIN grant** | Short-lived (5 min) opaque string returned by `POST /pin/verify`; sent as `X-LocalAI-Pin-Grant` to upgrade an operator request to admin |
| **Session** | Server-side record referenced by the `localai_session` cookie, used by the browser UI |
| **UI code** | 6-digit one-time code that turns into a session in a plain browser |
| **Content root** | `/srv/localai` (internal) or `<ark-drive-mount>/localai` (drive). Layout in overview §3.3 |
| **Transition** | An in-progress mode change, recorded step by step in `state.json` so it can resume |
| **Operation** | A mutating action (load, unload, sync, rotate, mode change, update). Every operation has an `op_id` (`op_` + 12 base32 chars), shows up in logs, events and the audit log |
| **Alert** | An active problem with a doctor code. Raised by periodic checks, cleared when the check passes |
| **Link** | The cloud link: the EAB worker-node client loop |
| **Server node id** | The UUID EAB assigns to the worker-node row. Different from the lab node id (`ai-n3`), which is the EAB node **name** |

## 3. Interfaces

### 3.1 Process model and binary

One static Go binary, `CGO_ENABLED=0`, `linux/amd64` and `linux/arm64`, installed at `/opt/localai/bin/aiagent`; `/usr/local/bin/aictl` is a symlink to it.

**Multi-call dispatch** (`agent/cmd/aiagent/main.go`), decided on `filepath.Base(os.Args[0])`. Only the exact basename `aictl` selects the CLI; **any other name is the agent** (`aiagent`, or a release file name such as `aiagent-linux-amd64`):

| argv[0] basename | argv[1] | Behaviour |
|---|---|---|
| `aictl` | anything | CLI ([aictl.md](aictl.md)); `os.Args[1:]` are CLI args |
| `aiagent` | `serve` | Run the daemon (what `localai-agent.service` executes) |
| `aiagent` | `ctl` | CLI with `os.Args[2:]` (fallback when the symlink is missing, e.g. during recovery) |
| `aiagent` | `version [--json]` | Print `aiagent <semver> (<git-sha>, <goos>/<goarch>, built <rfc3339>)`. `--json` prints `{"version","commit","os","arch","built_at"}`. Used by self-update verification |
| `aiagent` | `migrate llama-qwen [--dry-run] [--rollback] [--accept-catalog-flags] [--restore-files]` | Legacy migration ([llama-router.md](llama-router.md) §4.9). Root only. Run by the installer or by hand |
| `aiagent` | `update-rollback-check` | Internal; run by the transient rollback timer (§4.10) |
| `aiagent` | none / unknown | Print usage, exit 2 |

Inside `serve` the daemon is one process with these long-lived goroutines ("workers"). Each is started by `internal/app` and stopped through a shared `context.Context`:

| Worker | Package | Cadence |
|---|---|---|
| HTTP server (TCP) + HTTP server (unix socket) | `internal/api` | — |
| State store writer | `internal/config` | on change |
| Event bus | `internal/events` | — |
| Hardware sampler | `internal/hardware` | every 10 s (GPU, RAM, thermal, battery); disk every 60 s |
| Service watcher | `internal/services` | D-Bus signals, or 5 s poll |
| Router watcher | `internal/router` | `GET /models` every 5 s + `/models/sse` subscription |
| Mode engine | `internal/modes` | on request / resume at start |
| Connectivity prober | `internal/modes` | every 30 s, online mode only ([modes.md](../architecture/modes.md) §4) |
| Tailscale reconciler | `internal/modes` (`tailscale.go`) | on mode entry, then every 5 min in online mode |
| Cloud link (heartbeat, claim, state push) | `internal/cloud` | 15 s / 3 s / 60 s (§4.8) |
| Sync engine | `internal/sync` | on request or timer ([ark-sync.md](ark-sync.md)) |
| Drive watcher | `internal/sync` | `/proc/self/mountinfo` poll every 5 s ([ark-sync.md](ark-sync.md) §4.1.2) |
| Alert manager + periodic checks | `internal/doctor` | fast checks every 60 s |
| Watchdog pinger | `internal/app` | `WATCHDOG=1` every 20 s if the main loop is healthy |

### 3.2 Package layout

```
agent/
  go.mod                       module github.com/marinoscar/local-ai/agent
  vendor/                      go mod vendor (committed; offline rebuild, ADR-0001). Absent while the module has no
                               third-party dependency: `go mod vendor` deletes it, and CI fails on any difference from its output
  cmd/aiagent/main.go          multi-call dispatch (§3.1)
  api/openapi.yaml             OpenAPI 3.1, the wire contract (§4.14)
  api/spec.go                  package apispec: //go:embed openapi.yaml, operation lookup for the tests (§4.14)
  ui/                          React + Vite source of the local UI and Portal; `npm run build` → ui/dist (embedded).
                               The Vite scaffold, ui/embed.go and the Playwright/axe CI harness are created by PORTAL-04
                               if absent (survival-portal.md §10)
  ui/embed.go                  //go:embed dist  (package ui)
  internal/
    app/                       wiring: start/stop order (§4.1, §4.12), sd_notify, watchdog
    api/                       net/http mux, middleware (auth, host/origin, request id, body limits, error envelope),
                               handlers per route group, SSE endpoint, reverse proxies (/chat /library /maps /docs);
                               types.go (wire types), schema/ (JSON Schema subset validator), apitest/ (response
                               recorder), openapi_test.go (§4.14)
                               api/proxy (loopback reverse-proxy helper), api/static (path safety, Range files),
                               api/templates (bilingual error page): §3.11.4
    statusapi/                 GET /status, GET and PUT /profile, GET /catalog, POST /catalog/refresh (§3.8.31): the status
                               builder over small view interfaces, the model merge of §3.8.7 (`ModelMerger`), the catalog
                               store. Its own package because it composes catalog, hardware and router, and `hardware`
                               imports `api` (api -> catalog -> hardware -> api would be a cycle)
    modelsapi/                 GET /models, GET and DELETE /models/{id}, POST /models/{id}/download, GET /key, POST
                               /key/rotate (§3.8.32): `Service` (List, Get, Download, Delete, ShowKey, RotateKey, shared
                               with the cloud executors), the sync-engine and cloud-push seams. Its own package for the
                               same reason as `statusapi` (it composes router and catalog)
    config/                    node.yaml load/validate/write (yaml.v3 node tree, comments preserved),
                               secret file helpers (perm checks)
    state/                     state.json store (§3.5): single writer, atomic writes over an `FS` seam, .bak,
                               corruption fallback; stdlib only. state/fake: in-memory FS with failure and kill injection
    catalog/                   catalog load + lint (shared with `aictl catalog lint`, catalog.md §6), merged views
    router/                    llama-server router client, models.ini generator, fit rules, load/unload orchestrator,
                               single-mode unit writer, build manager, bench (llama-router.md)
    sync/                      ark sync engine, drive detection, READY.json, import, verify (ark-sync.md)
    modes/                     mode state machine + transitions, connectivity prober, tailscale serve manager
    services/                  systemd manager interface: dbus + systemctl implementations, journald tail
    cloud/                     EAB worker-node client: register, heartbeat, claim, renew, result, failure, state push,
                               job decoders and dispatch (§4.8)
    hardware/                  GPU (nvidia-smi, AMD sysfs, rocm-smi/radeontop optional), RAM, disk, battery, thermal, OOM,
                               sampler, LAI-HW checks, GET /hardware (§4.4). hardware/fake: scripted Sampler.
                               Fixtures in hardware/testdata/ (a package-local testdata, like config's)
    svc/                       companion services (companion-services.md): kiwix/, whisper/ (STT proxy), piper/ (TTS exec), health/
    maps/                      /maps/* handlers: viewer, pmtiles Range serving, search (companion-services.md §3.5)
    portal/                    portal status, Portal session, /chat/ proxy, i18n strings en/es (survival-portal.md)
    doctor/                    check registry, code table (en/es), alert manager, drill hooks (drill.md)
    events/                    in-process pub/sub bus, ring buffer for SSE replay
    auth/                      tokens, sessions, UI codes, PIN (argon2id), peer creds, grants, rate limiters; implements
                               `api.Authenticator` and owns the routes of §3.8.2 and §3.8.3 (`auth` imports `api`, so the
                               handlers cannot live in `api`); `auth/fake`: Portal session store, group lookup (§3.9.5)
    audit/                     append-only audit log writer
    update/                    self-update: GitHub release client, SHA256SUMS, minisign verify, rollback
    logx/                      slog setup, secret-scrubbing handler
    execx/                     the ONLY package allowed to call os/exec: fixed-argv runner with timeouts, output caps
    cli/                       aictl implementation (aictl.md)
    setup/                     `aiagent setup …` steps used by the installer (installer.md §4.3)
    setup/units/               systemd unit templates embedded in the binary (//go:embed): localai-agent.service (§3.6),
                               localai-sync.{service,timer}, localai-drill.{service,timer}, localai-kiwix.service,
                               localai-whisper.service; `aiagent setup units` writes them (localai-llama.service is
                               generated by internal/router instead)
  testdata/                    fakes' fixtures (nvidia-smi outputs, sysfs trees, tailscale JSON, router /models JSON)
```

Rules:
- Only `internal/execx` imports `os/exec`. A linter test (`internal/execx/guard_test.go`) greps the tree and fails otherwise.
- `execx` API: `Runner.Run(ctx, Cmd) (Result, error)` with `Cmd{Path, Args, Timeout, MaxOut, Env, Stdin}`. `Path` must be absolute and clean; `Timeout` and `MaxOut` are required (> 0); shell binaries (`sh`, `bash`, ...) are refused, so there is no `sh -c`. A nil `Env` means `PATH=/usr/sbin:/usr/bin:/sbin:/bin` only. The child gets its own process group, which is killed on timeout. `stdout` and `stderr` are capped separately at `MaxOut` (first bytes kept, `Truncated` set). A non-zero exit code is a **result**, not an error; `ErrTimeout`, `ErrInvalid` and context cancellation are errors. `Lookup(name)` resolves inside the fixed `PATH` only and refuses relative names with a directory part, `..` and absolute paths outside it.
- The import guard parses imports (not text), covers test files and sub-packages (`execx/fake` included), and skips `vendor/`.
- Every external dependency (systemd, router, tailscale, GPU tools, cloud, GitHub, clock, filesystem roots) sits behind an interface with a fake in `internal/<pkg>/fake`.
- Allowed third-party modules (vendored): `gopkg.in/yaml.v3`, `github.com/godbus/dbus/v5`, `golang.org/x/crypto` (argon2, blake2b for minisign), `golang.org/x/sys/unix` (statfs, peer creds), `golang.org/x/term` (no-echo PIN prompt), `github.com/yuin/goldmark` (`aictl docs render`). All are pure Go. Adding any other module needs an ADR.

### 3.3 Files the agent reads and writes

| Path (overview §3.2) | Agent access | Notes |
|---|---|---|
| `/etc/localai/node.yaml` | read; write on `PUT /profile`, `cloud enroll`, migration | Atomic write, previous copy kept as `node.yaml.bak` |
| `/etc/localai/api-key` | read; write on rotate | Never logged. 0600 root:root |
| `/etc/localai/agent-token` | read (re-read on mtime change); create if absent | 0640 root:localai-operators |
| `/etc/localai/cloud-credential` | read; write on enroll; delete on unenroll | 0600 root:root |
| `/etc/localai/admin-pin` | read (verify) | written by `aictl pin set` (root, in-process) |
| `/etc/localai/hf-token` | read by sync | optional |
| `/etc/localai/models.ini` | write (generated) | [llama-router.md](llama-router.md) §4.2 |
| `/etc/localai/catalog/` | read; replace atomically on refresh; re-read when `CATALOG_VERSION` mtime changes (checked every 10 s, so `aictl catalog restore` needs no API call) | [catalog.md](catalog.md) §1 |
| `/etc/systemd/system/localai-llama.service` and `localai-llama.service.d/50-env.conf` | write (generated) | [llama-router.md](llama-router.md) §3 |
| `/var/lib/localai/state.json` (+ `.bak`) | read/write | §3.5 |
| `/var/lib/localai/audit.jsonl` (+ `.1`…`.5`) | append | §4.11 |
| `/var/lib/localai/cloud/link.json`, `cloud/inflight.json` | read/write | §4.8 |
| `/var/lib/localai/update/` | write | §4.10 staging |
| `/run/localai/agent.sock` | create | root:localai-operators 0660 |
| `<content-root>/**` | read; write through sync | [ark-sync.md](ark-sync.md) |

All writes of files the agent owns use `WriteFileAtomic(path, data, mode, uid, gid)`: write `path.tmp-<rand>` in the same directory (`O_EXCL`, 0600), `chown`/`chmod`, `fsync`, `rename`, `fsync` the directory. The ownership and mode are set **before** the `fsync` so that the metadata is durable together with the data. A failure before the `rename` removes the temp file and leaves `path` untouched; a failed directory `fsync` is reported as an error (the new file is in place but not known to be durable). `uid` or `gid` < 0 leaves that owner unchanged. The state store uses `state.WriteFileAtomic(fsys, path, …)`, the same steps over its `FS` interface so that tests can inject failures and a kill at every step; `config.WriteFileAtomic` (AGENT-02) must keep the same steps.

Details of the helper (`internal/config/atomic.go`):
- `config.WriteFileAtomic` takes the same steps as `state.WriteFileAtomic` and adds a real-filesystem `FS` seam (`config.FS`, with the fault-injecting `config/fake.FaultFS`). A short write is a failure. The temp file is created `0600` and gets its final mode before the `fsync`, so a secret is never briefly readable.
- Any failure before the rename removes the temp file and leaves the target untouched. A process that dies mid-write (kill -9) cannot clean up, so the start of `aiagent serve` removes leftover `<file>.tmp-*` files (`config.SweepTemps`) before it reads anything.
- If only the final directory `fsync` fails, the rename has happened: the call returns `config.ErrNotDurable` and the new content is in place.
- Rewriting `node.yaml` (`config.Store.Update`) validates the result first (an invalid result writes nothing and returns the problems), saves the previous file as `node.yaml.bak` **only when that file was itself valid** (so `.bak` is always the last good copy, even after a human broke the file), then replaces `node.yaml` with its old mode and owner. A rewrite that changes no byte is skipped.
- Secret files (`api-key`, `cloud-credential`, `admin-pin`, `agent-token`): `config.CheckSecret` / `ReadSecretFile` / `EnforceSecret` compare mode (no bit beyond the policy's) and owner with the policy, refuse a symlink (chmod would follow it), and report each difference so start-up can fix it and raise `LAI-SEC-009`.

### 3.4 `node.yaml` schema

`/etc/localai/node.yaml`, root:localai 0640. Created by the installer ([installer.md](installer.md)); edited by humans rarely; rewritten by the agent only through `internal/config` (which preserves comments and key order using the yaml.v3 node tree).

**Complete example (ai-n3):**
```yaml
schema_version: 1

node:
  id: ai-n3                         # REQUIRED. ^ai-n[1-9][0-9]{0,3}$. Never reused. Also the EAB node name.
  display_name: "HP ZBook (ai-n3)"  # shown in UIs; default = id
  public_host: ai-n3.marin.cr       # informational; used in `aictl key snippets`; null if not published

profile: small-8gb                  # REQUIRED. catalog/profiles/<id>.yaml (catalog.md §4)
profile_overrides:                  # catalog.md §4 "Node overrides"
  add_models: []
  remove_models: []
  add_content: []
  remove_content: []
  budget_bytes: null                # null = use profile storage.budget_bytes

catalog:
  channel: stable                   # stable | catalog-<CATALOG_VERSION> (pin) ; catalog.md §1
  base_url: https://raw.githubusercontent.com/marinoscar/local-ai   # used when not enrolled

llama:
  mode: router                      # router | single   (llama-router.md §4.8)
  build: /opt/localai/llama.cpp/current   # informational; the unit always uses current/bin/llama-server
  commit: f3586e66b                 # llama.cpp commit the node should run; rebuild-llama builds this
  backend: cuda                     # cuda | vulkan | cpu
  cuda_arch: null                   # e.g. "75"; null = auto-detect from nvidia-smi compute_cap (llama-router.md §4.10)
  load_timeout_seconds: 300         # per model load (30..3600)
  legacy_aliases:                   # written by migration; extra router aliases so old clients keep working
    qwen3.5-9b-q4km: [qwen3.5-9b]
  extra_env: {}                     # node-level env for llama-server, e.g. {GGML_VK_SUBALLOCATION_BLOCK_SIZE: "4294967296"}
                                    # keys must match ^(GGML|LLAMA|CUDA)_[A-Z0-9_]+$ ; LLAMA_ARG_* is refused

tailscale:
  manage: true                      # false = the agent never runs `tailscale serve` (doctor still reports)
  serve_pattern: B                  # A = --tcp=18080 ; B = --https=443 ; none = not published

cloud:
  url: https://ai.marin.cr          # https only (http://127.0.0.1:<port> allowed only with dev_insecure: true)
  dev_insecure: false
  allow:                            # node-side job allowlist, names WITHOUT the "localai." prefix (security-model.md §4)
    [status.refresh, model.load, model.unload, model.set_default, model.download, model.delete,
     ark.sync, ark.verify, mode.offline, key.rotate, service.restart, logs.tail, catalog.refresh, agent.update]
  concurrency: 1                    # EAB concurrency (1..4); local ops serialize anyway (§4.12)
  poll:                             # the cloud's handshake (GET …/localai/config) overrides these when present
    heartbeat_seconds: 15           # 5..60
    claim_seconds: 3                # 1..30
    state_seconds: 60               # 15..600 (plus immediate push on change)

# ---- blocks owned by other specs (included verbatim; their spec defines validation) ----
sync:                               # owned by ark-sync.md §3.3
  enabled: true                 # false = scheduled runs exit "skipped: disabled"
  concurrency: 2                # max simultaneous network downloads (all roots together), 1..4
  per_root_writers: 1           # max simultaneous writers per root; drives are slow on parallel writes
  bandwidth_limit: "0"          # "0" = unlimited, else e.g. "20MiB/s", "100Mbit/s"
  allow_on_battery: false       # scheduled runs skip when on battery
  verify_reread: true           # re-read the file from disk after download before staging
  reverify_days: 30             # every verified item is re-hashed at least this often
  reverify_budget_minutes: 90   # max time per scheduled run spent on re-verification
  trash_days: 14                # how long a superseded version is kept
  quarantine_days: 7
  max_run_hours: 12             # scheduled run stops gracefully after this
  hf_token_file: /etc/localai/hf-token   # optional; used only for huggingface.co requests
  ark_drive:
    enabled: true               # false = drives are detected and served but never written
    default_profile: ark-drive-full
    copy_inventory: true

kiwix:                              # owned by companion-services.md §3.1
  enabled: true
  threads: 4
whisper:
  enabled: true
  commit: "<40-hex whisper.cpp commit>"
  backend: auto
  device: auto
  model: auto
  threads: auto
  max_audio_seconds: 120
piper:
  enabled: true
  version: "2023.11.14-2"
  length_scale: 1.05
  default_voice: {en: auto, es: auto}
maps:
  enabled: true
  default_map: auto
  gps: auto
  default_center: {lat: 9.93, lon: -84.08, zoom: 8}

portal:                             # owned by survival-portal.md §3.4
  language: auto
  emergency_number: "911"
  kiosk: false
# ---- end of blocks owned by other specs ----

update:
  repo: marinoscar/local-ai         # GitHub repo of agent releases
  channel: stable                   # stable | prerelease
  allow_downgrade: false

ui:
  language: auto                    # auto | en | es  (auto = Accept-Language, then OS locale)

ports:                              # canonical values (overview §3.4). Changing them is unsupported on lab nodes;
  agent: 7070                       # they exist so tests and dev machines can run side by side.
  llama: 8080
  kiwix: 8888
  whisper: 8081

logging:
  level: info                       # debug | info | warn | error
```

**Validation rules** (`config.Validate`; violations are `LAI-AGT-002` with one `details.problems[]` entry each `{path, message, code?}`; `code` is set only when a more specific code exists, e.g. `LAI-CLD-062` for `cloud.url`):

| Field | Rule | Default if absent |
|---|---|---|
| `schema_version` | must be `1` | — (required) |
| `node.id` | `^ai-n[1-9][0-9]{0,3}$` | — (required) |
| `profile` | must exist in the loaded catalog, else `LAI-CAT-003` (the agent still starts) | — (required) |
| `llama.mode` | `router` \| `single` | `router` |
| `llama.backend` | `cuda` \| `vulkan` \| `cpu` | detected (§4.6): nvidia → cuda, amd → vulkan, else cpu |
| `llama.commit` | `^[0-9a-f]{7,40}$` | value from `current/BUILD.json` |
| `llama.cuda_arch` | `^[0-9]{2,3}(;[0-9]{2,3})*$` or null | null |
| `llama.legacy_aliases` | keys are catalog model ids; values `[a-z0-9][a-z0-9.\-_:]{1,63}`; an alias must not equal any catalog id | `{}` |
| `llama.extra_env` | key regex above; value ≤ 256 printable chars | `{}` |
| `llama.load_timeout_seconds` | integer 30..3600 | `300` |
| `tailscale.serve_pattern` | `A` \| `B` \| `none` | `none` |
| `cloud.url` | `https://` URL without path, or `http://127.0.0.1:<port>` with `dev_insecure: true`. A non-https URL is reported as a `details.problems[]` entry with `code: "LAI-CLD-062"` inside the `LAI-AGT-002` report (§8.5) | `https://ai.marin.cr` |
| `cloud.allow` | each entry ∈ the 14 known job names (§4.8.6); unknown entries are a validation error | all 14 |
| `cloud.concurrency` | 1..4 | 1 |
| `cloud.poll.heartbeat_seconds` | integer 5..60 (the cloud handshake overrides it, clamped to the same range) | `15` |
| `cloud.poll.claim_seconds` | integer 1..30 (same override rule) | `3` |
| `cloud.poll.state_seconds` | integer 15..600 (same override rule) | `60` |
| `ui.language` | `auto` \| `en` \| `es` | `auto` |
| `ports.*` | 1024..65535, all distinct | canonical values |

**Block ownership.** `sync` belongs to [ark-sync.md](ark-sync.md) §3.3; `kiwix`, `whisper`, `piper`, `maps` to [companion-services.md](companion-services.md) §3.1; `portal` to [survival-portal.md](survival-portal.md) §3.4. Their validation rules live there and their errors are reported as `LAI-AGT-002` with the block path; this spec only parses them into typed structs owned by those packages. The sync **schedule** is not a `node.yaml` key: it is the `OnCalendar=` of `localai-sync.timer` ([ark-sync.md](ark-sync.md) §3.7, nightly 03:15 with a 45 min random delay); an operator changes it with a systemd drop-in.

Unknown top-level keys are a **warning** (logged, `LAI-AGT-002` severity `warning`), never a failure, so an older agent can read a newer file. An unknown key **inside** a known block (`llama.moed`) is an error: a typo must not be silently ignored.

**Parsing rules** (`internal/config`; the table above does not repeat them):
- Each entry of `details.problems[]` also carries `severity` (`error` blocks loading, `warning` loads anyway) and `line` (1-based, when known). A value of the wrong type is a problem at its path (`llama.load_timeout_seconds: must be a whole number`), never a crash; a repeated key is an error.
- An explicit `null` (or an empty value) means "the default"; it clears the optional `llama.cuda_arch`, `node.public_host` and `profile_overrides.budget_bytes`. `cloud.allow: []` is valid and allows no job.
- `tailscale.manage` defaults to `false` when absent (the agent never runs `tailscale serve` unless the file says so). `node.display_name` defaults to `node.id`. `llama.backend` and `llama.commit` are filled from hardware detection and `current/BUILD.json` through hooks when absent.
- `cloud.url`: one trailing `/` is accepted and stripped; a path, query, fragment or user info is an error; `http://127.0.0.1:<port>` needs its port. Anything else that is not `https://` is `LAI-CLD-062`.
- `llama.legacy_aliases` maps a catalog model id to a list of aliases. The two catalog-dependent checks (the key is a catalog id, no alias equals a catalog id) run only when a catalog with at least one model is loaded, so an empty catalog never invalidates `node.yaml`. A `profile` missing from the catalog is a non-blocking `LAI-CAT-003` warning: the file loads.
- The owned blocks are kept as raw YAML and validated by the validator their package registers (`config.Hooks.Blocks`); a problem path is prefixed with the block name. Until a package registers, its block is parsed and kept, not validated.
- The safe defaults are `cloud.url: ""` (no cloud, nothing allowed), `tailscale.manage: false`, canonical ports, `node.id: "unconfigured"`.

**Hot reload.** The agent polls `node.yaml` every 10 s by mtime and size; a changed mtime with an identical sha256 is a touch and is ignored. On a change it parses and validates. An invalid edited file is ignored: the last valid config stays in effect and `LAI-AGT-002` is reported until a valid edit clears it. A file deleted while the agent runs also keeps the last config and reports `LAI-AGT-001`. Fields are classified as follows:
- **Live** (applied the moment the file is accepted): `profile*`, `cloud.allow`, `cloud.poll`, `cloud.concurrency`, `sync.*`, `ui.*`, `portal.*`, `logging.level`, `tailscale.*` (re-reconcile), `llama.legacy_aliases` (regenerate the INI), `llama.load_timeout_seconds`, `catalog.*`, `update.*`, `node.display_name`, `node.public_host`, and the companion blocks `kiwix`, `whisper`, `piper`, `maps` (their packages decide what a change means).
- **Agent restart** (`LAI-AGT-016`, target `agent`): `node.id`, `ports.*`, `cloud.url`. The effective config keeps the value the agent started with, so no component sees these change under it; the file's value is reported as pending until the file matches the running agent again. This also holds when the agent started on the safe defaults and the file is fixed later: `node.id` and `cloud.url` wait for a restart.
- **Llama restart** (`LAI-AGT-016`, details target `llama`, **never automatic**): `llama.extra_env`, `llama.mode`, `llama.build`, `llama.commit`, `llama.backend`, `llama.cuda_arch`. The new value is in the effective config at once so the router can regenerate the unit and drop-in; it reaches llama only when the operator runs `aictl restart llama`.

### 3.5 `state.json` schema

`/var/lib/localai/state.json`, root:localai 0640. Only the state store goroutine writes it, after every change, atomically, keeping the previous good version as `state.json.bak`. Sub-trees owned by other specs are marked; the agent stores them opaquely.

```json
{
  "schema_version": 1,
  "written_at": "2026-11-02T15:04:05Z",
  "agent_version": "0.1.0",

  "mode": "online",
  "mode_changed_at": "2026-11-01T22:10:00Z",
  "mode_transition": null,
  "pending_mode": null,

  "agent": {"recent_starts": ["2026-11-02T06:00:01Z"]},

  "models": {
    "default": "qwen3.5-9b-q4km",
    "desired_loaded": ["qwen3.5-9b-q4km"],
    "last_used": {"qwen3.5-9b-q4km": "2026-11-02T14:59:00Z"},
    "last_load": {"id": "qwen3.5-9b-q4km", "op_id": "op_7k2m9q4r8t1c", "result": "ok", "load_ms": 7100, "at": "2026-11-02T08:00:12Z"}
  },

  "llama": {
    "build": "f3586e66b-cuda",
    "previous_build": null,
    "ini_sha256": "<64 hex of generated models.ini>",
    "unit_sha256": "<64 hex of generated unit>"
  },

  "key": {"fingerprint": "ab12…9f3c", "rotated_at": null, "cloud_synced": true},

  "tailscale": {"applied_pattern": "B", "applied_at": "2026-11-01T22:10:02Z"},

  "connectivity": {"value": "full", "since": "2026-11-02T06:00:00Z", "none_since": null},

  "migration": {"llama_qwen": {"state": "done", "step": "M10", "at": "2026-11-02T07:58:00Z"}},

  "update": {"last": {"result": "ok", "from": "0.0.9", "to": "0.1.0", "at": "2026-11-01T10:00:00Z"}},

  "sync":  {"_owner": "ark-sync.md", "last_run_at": "…", "last_success_at": "…", "last_result": "ok"},
  "drill": {"_owner": "drill.md", "last_run_at": "…", "last_result": "pass", "in_progress": null, "watch_since": "…"},
  "maps":  {"_owner": "companion-services.md", "manual_location": null}
}
```

`mode_transition`, when non-null:
```json
{"op_id": "op_3h8x2k1m0p7w", "from": "online", "target": "survival", "step": "survival_models",
 "started_at": "2026-11-02T15:00:00Z", "attempts": {"tailscale_off": 1, "survival_models": 2},
 "warnings": [{"code": "LAI-LLM-020", "message": "The preferred model failed to start; using the next one."}],
 "actor": {"kind": "anonymous", "via": "portal"}}
```

`mode_transition.skip_models` (additive, written only by a survival transition that resumed) lists the models `survival_models` must not try: the one whose load was `in_progress` at each restart of this transition, accumulated so that a second crash on another model does not bring the first back (§4.2.4). `pending_mode` is `"survival"` while a `POST /mode/survival` received before startup step 12 (or during shutdown), or a survival request that preempts a running transition, waits to run (§4.1 step 10, §3.8.16, §4.2.3); it is cleared when that transition starts. `agent.recent_starts` holds the start times of the last 10 agent starts (newest last), used for `LAI-AGT-014` (§4.1).

`tailscale.applied_pattern` is the pattern of **our** listener the last read of the serve configuration showed (`"A"` or `"B"`), or `""` when none of ours is published (after a verified removal, or a read that found none); it is left unchanged when tailscale could not be read. `applied_at` is when that value last changed (`null` with `""`). `connectivity.since` is when the reported value last changed and `none_since` when the outage that made it `none` began (§4.5); `connectivity.checked_at` of `GET /status` is not stored: it is `ConnectivityProber.LastCheckedAt()`.

`drill.deferred` (owned by [drill.md](drill.md) §4.1) is absent or `{"code": "LAI-DRL-002|004", "quick": false, "only": [], "first_at": "…", "next_retry_at": "…", "deadline_at": "…", "attempts": 1}` while a scheduled drill waits for a retry; `drill.last_run_at` and `drill.last_result` describe the last completed run (not a skipped or aborted one, and not a `manual_plug` entry); `drill.watch_since` is stamped once, the first time the drill reporter starts, and is where the 45-day alert `LAI-DRL-006` counts from on a node that never ran a drill ([drill.md](drill.md) §4.5). `drill.in_progress` (owned by [drill.md](drill.md) §4.3) is `null` or `{"run_id": "drl-…", "started_at": "…", "captured": {"loaded_models": […], "default_model": "…", "whisper_running": true}}` while a drill runs; an agent that starts and finds it non-null performs the drill restore. `maps.manual_location` (owned by [companion-services.md](companion-services.md) §4.4.5) is `null` or `{"lat": 9.93, "lon": -84.08, "label": "Home", "set_at": "…"}`.

`update.last` records the outcome of the most recent self-update (`ok` | `rolled_back` | `failed`). An update **in progress** is not kept here but in the marker file `/var/lib/localai/update/pending.json` (§4.10), so that `sudo aictl update` (which runs in its own process) never writes `state.json` concurrently with the agent:
```json
{"from": "0.1.0", "to": "0.2.0", "started_at": "…", "deadline": "…", "boot_attempts": 0,
 "job": {"job_id": "<uuid>", "claim_token": "<uuid>"}}
```

**Load rules:**
1. Parse `state.json`. It is **corrupt** only on a parse error or when `schema_version` is missing or < 1. A corrupt file, or a missing `state.json` while `state.json.bak` exists, makes the agent load `state.json.bak` and raise `LAI-AGT-003` (warning). If the backup is also bad or absent (and `state.json` was corrupt), start with defaults **and `mode: offline`** (the safe choice: nothing exposed, nothing pulled) and raise `LAI-AGT-003` (critical). The bad file is kept as `state.json.corrupt-<rfc3339 UTC>` (`-2`, `-3` … appended when that name exists; a bad `state.json.bak` is kept as `state.json.bak.corrupt-<rfc3339 UTC>`). A `state.json` that is missing while `state.json.bak` is also bad counts as "backup also bad": defaults, offline, critical. A `state.json` that exists but cannot be read (`EIO`, `EACCES`) is treated like a corrupt one, except that it is not renamed. Both files missing = a fresh node (rule 3, no code).
2. `schema_version` greater than 1 (written by a newer agent, then rolled back) is **not** corruption: the file is loaded best-effort (known fields used), `LAI-AGT-003` (warning) is raised with `details.schema_version`, and unknown keys are preserved on rewrite (a newer agent's fields survive a downgrade). The loaded `schema_version` is written back unchanged (a rewrite never downgrades the number). A newer agent may add fields but must never change the JSON type of an existing one (a field of the wrong type is a parse error, so the file is corrupt). Unknown keys are always preserved, at the top level and inside every object the agent types (`models`, `models.last_load`, `mode_transition`, its `warnings[]` and `actor`, `llama`, `key`, `tailscale`, `connectivity`, `migration.*`, `update`, `update.last`, `agent`); the sub-trees owned by other specs (`sync`, `drill`, `maps`) are stored as raw JSON, so everything inside them survives. Numbers inside preserved keys keep their exact text.
3. Defaults for a fresh node: `mode: online`, `models.default: null` (profile default applies), everything else empty.
4. A `mode` that is not `online`, `offline` or `survival` (or is missing) in an otherwise valid file is not corruption but is never applied: the store uses `offline` and raises `LAI-AGT-003` (warning, `details.invalid_mode`).

**Store behaviour** (`internal/state`):
- `Open(path, opts)` never fails because of the files: every damage degrades as above and is reported through `Findings()`. It writes nothing (the first mutation writes the file) and removes `state.json.tmp-*` and `state.json.bak.tmp-*` leftovers of a killed write.
- `Mutate(fn)` runs `fn` on a deep copy; an error from `fn` (or a panic, or a state that cannot be serialised) discards the change and writes nothing. Mutations are applied one at a time in arrival order by a single writer goroutine. `Snapshot()` returns a deep copy and never blocks on disk I/O.
- Writes are **group-committed**: mutations that queue up while a write is in flight share the next one, and a mutation that changes nothing writes nothing. `MutateSync(fn)` ends its batch (it is never merged with mutations that arrive after it) and writes even if nothing changed; the mode engine records every transition step and `persist` with it (§4.2.2). Both return only after the `fsync` of the write that contains the change. `Flush()` writes pending state (after a failed write or a recovery) and `Close()` makes a last attempt.
- Every write sets `written_at` (UTC) and `agent_version`, keeps the previous good file as `state.json.bak` (a hard link renamed over the old backup, so the `rename` is atomic and no data is copied; an atomic copy where hard links are impossible), then replaces `state.json` with `WriteFileAtomic` (0640, owner root:localai). The backup is never replaced by a file the store did not load or write successfully itself: after a recovery from `.bak` the first write leaves it alone.
- **Unwritable directory** (any write error, typically `ENOSPC`, `EROFS`, `EACCES`): `Mutate` and `MutateSync` still return `nil`, the new state lives in memory and every reader keeps working, `Findings()` reports `LAI-AGT-004` (critical, `details.error`, `details.path`, `details.since`) and `Writable()` is false. The next mutation, `Flush` or `Close` retries the whole current state; success clears the finding. `Options.OnFindings` is called when the set of findings changes.
- `Findings()` returns kinds, not codes: `KindDamaged` is `LAI-AGT-003` and `KindUnwritable` is `LAI-AGT-004`. The code table lives only in `internal/doctor/codes.go`; the mapping from kind to code is `internal/doctor/checks_agt.go` (checks `agt.state_intact` and `agt.state_writable`, §4.13.1); `scripts/check-doctor-codes.sh` forbids a code literal anywhere else. `LAI-AGT-003` details: `source` (`bak`, `defaults` or `file`), `reason`, `schema_version`, `invalid_mode`, `corrupt_copy`.

### 3.6 `localai-agent.service`

Written by the installer (`aiagent setup units`, from the template embedded at `agent/internal/setup/units/localai-agent.service`, §3.2); owned by this spec.

```ini
[Unit]
Description=Local AI node agent (aiagent)
Documentation=https://github.com/marinoscar/local-ai/blob/main/docs/specs/agent.md
After=local-fs.target network.target tailscaled.service
# No Requires/Wants on tailscaled or network-online: the agent must start with no network at all.
StartLimitIntervalSec=600
StartLimitBurst=20

[Service]
Type=notify
NotifyAccess=main
ExecStart=/opt/localai/bin/aiagent serve
Restart=always
RestartSec=2
TimeoutStartSec=90
TimeoutStopSec=30
WatchdogSec=60
RuntimeDirectory=localai
RuntimeDirectoryMode=0755
UMask=0027
LimitNOFILE=65536
StandardOutput=journal
StandardError=journal
SyslogIdentifier=aiagent
# Moderate hardening. The agent is root by design (overview §3.1): it manages units, /etc/localai and tailscale serve.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=read-only
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectClock=yes
RestrictSUIDSGID=yes
LockPersonality=yes
RestrictRealtime=yes

[Install]
WantedBy=multi-user.target
```

Notes: `ProtectHome=read-only` is safe because the daemon never writes under `/home`; the legacy migration, which moves files out of `~/models`, runs as `aiagent migrate` from a root shell, not inside the daemon. `localai-llama.service` does **not** depend on the agent: inference keeps working while the agent restarts or is broken.

### 3.7 HTTP API conventions

- Base path `/api/v1`. Same routes on TCP `127.0.0.1:<ports.agent>` and on `unix:/run/localai/agent.sock`.
- Requests and responses are `application/json; charset=utf-8`, except SSE (`text/event-stream`), STT upload (`multipart/form-data`) and TTS (`audio/wav`).
- **Mutating requests must send `Content-Type: application/json`** (even with an empty body `{}`); otherwise `415 LAI-AGT-020`. This forces a CORS preflight for any cross-origin browser request, which the agent never answers (§3.9.4).
- Body limit 1 MiB (STT: 25 MiB). Over the limit: `413 LAI-AGT-025`. Exception: the `/chat/` reverse proxy (§3.11) accepts up to 32 MiB per request (photos in the Portal, [survival-portal.md](survival-portal.md) §4.4).
- JSON decoding is strict: unknown fields → `400 LAI-AGT-020` with `details.field`.
- Times are RFC 3339 UTC with `Z`. Sizes are bytes (integers). Durations are `_ms` or `_s` suffixed integers.
- Every response carries `X-Request-Id: req_<12 base32>` (echoes a valid incoming `X-Request-Id`). Mutating responses also carry `X-LocalAI-Op-Id` when an operation was created.
- `Accept-Language: es` (or `ui.language: es`) localizes `message` and `fix` in error envelopes, alerts, doctor results and Portal strings. Codes and field names never change. Either one is enough: the language is Spanish when `ui.language` is `es` or when `Accept-Language` prefers Spanish (by q-value, among the languages the agent has), English otherwise; `ui.language` is read for every error, so a hot reload applies at once.
- Idempotency: repeating a request whose effect is already in place answers `200` with a flag (for example `already_loaded: true`), not an error.
- **Content roots** in request and response bodies (`roots`, `root`, `from_root`) accept either the root's path (`/srv/localai`, `/media/<user>/AI-ARK/localai`) or its root id (`internal`, `ark-<8 hex>`, [ark-sync.md](ark-sync.md) §2); responses carry both (`path` and `id`).
- Long operations: with `wait: true` the call blocks until done (server write timeout is per route, §3.8 table "Timeout"). With `wait: false` it answers `202 Accepted` and progress arrives on `/events`.

**Error envelope** (every non-2xx response, except SSE after the stream started):
```json
{
  "error": {
    "code": "LAI-LLM-009",
    "message": "The model qwen3.6-35b-a3b-q4km failed to start. The previous model was started again.",
    "fix": "Run `aictl logs --unit llama` to see why, or choose a smaller model with `aictl models --loadable`.",
    "details": {"model": "qwen3.6-35b-a3b-q4km", "exit_code": 1, "rolled_back": true, "op_id": "op_7k2m9q4r8t1c"}
  }
}
```

`code` is always a doctor code. `fix` is one plain-language sentence (doctor-codes.md). `details` is an object (possibly empty) whose keys are documented per route.

**Status code map:**

| HTTP | Default code | When |
|---|---|---|
| 400 | `LAI-AGT-020` | malformed JSON, unknown field, invalid value |
| 401 | `LAI-SEC-001` / `LAI-SVC-110` | missing/invalid bearer, cookie or socket peer / a Portal session used where it is not allowed (§3.9.1 step 5) |
| 403 | `LAI-SEC-002` / `LAI-SEC-006` / `LAI-SEC-007` / `LAI-SEC-013` | admin needed / bad Host / bad Origin / socket peer not an operator |
| 404 | `LAI-AGT-021` | unknown route (also any path that is not in clean form: nothing redirects), model id, service name, content id |
| 405 | `LAI-AGT-020` | the path exists under another method (`Allow` header and `details.allow` list them), and every `OPTIONS` (§3.9.4) |
| 409 | `LAI-AGT-022` / `LAI-AGT-023` | another operation in progress / not allowed in current mode |
| 413 | `LAI-AGT-025` | body too large |
| 415 | `LAI-AGT-020` | wrong content type |
| 422 | domain code | understood but refused by a rule (e.g. `LAI-CAT-005` doesn't fit, `LAI-LLM-008` files missing) |
| 429 | `LAI-SEC-004` | PIN or UI code rate limit; `Retry-After` header set |
| 500 | `LAI-AGT-099` | bug. `details.request_id` always present |
| 502 | `LAI-LLM-004` / `LAI-SVC-*` | upstream (router, kiwix, whisper) not answering |
| 503 | `LAI-AGT-024` | agent starting up or shutting down; `Retry-After: 5` |
| 504 | `LAI-LLM-010` | upstream timeout; a handler that ran past its route deadline and returned the context error answers `LAI-AGT-099` with `details.reason: "deadline_exceeded"` and `details.request_id` |

### 3.8 Route reference

Legend for **Auth**: `none` = anonymous allowed; `op` = operator; `portal` = the restricted **Portal session** cookie `lai_portal`, issued only in survival mode by `GET /portal` ([survival-portal.md](survival-portal.md) §3.3; §3.9.1 step 5); `admin` = admin (socket uid 0, or operator + valid PIN grant); `admin*` = admin, or a socket peer in `localai-operators` (modes.md §2.4). **Timeout** is the server-side handler deadline.

| Method | Path | Auth | Timeout | Section |
|---|---|---|---|---|
| GET | /health | none | 2 s | 3.8.1 |
| POST | /session | op (bearer only) | 5 s | 3.8.2 |
| POST | /session/code | none | 5 s | 3.8.2 |
| DELETE | /session | op (cookie) | 5 s | 3.8.2 |
| POST | /ui-code | admin* | 5 s | 3.8.2 |
| POST | /pin/verify | op | 5 s | 3.8.3 |
| GET | /status | op | 5 s | 3.8.4 |
| GET | /portal/status | none | 5 s | 3.8.5 |
| GET | /hardware | op | 10 s | 3.8.6 |
| GET | /models ; /models/{id} | op | 5 s | 3.8.7 |
| POST | /models/{id}/load | op, or portal (restricted: `loadable` models only, body forced, [survival-portal.md](survival-portal.md) §3.3) | load_timeout × (n+2) + 30 s | 3.8.8 |
| POST | /models/{id}/unload | op | 90 s | 3.8.9 |
| PUT | /models/default | op | 5 s | 3.8.10 |
| POST | /models/{id}/download | op | 10 s | 3.8.11 |
| DELETE | /models/{id} | admin | 60 s | 3.8.12 |
| GET | /key | op | 5 s | 3.8.13 |
| POST | /key/rotate | admin | 15 min | 3.8.14 |
| GET | /mode | op | 5 s | 3.8.15 |
| POST | /mode | op (admin* to leave survival) | 20 min if `wait` else 5 s | 3.8.15 |
| POST | /mode/survival | none | 5 s | 3.8.16 |
| GET | /ark ; /ark/drives | op | 10 s | 3.8.17 |
| POST/GET/DELETE | /ark/sync | op | 10 s | 3.8.18 |
| POST | /ark/verify | op | 10 s | 3.8.19 |
| POST | /ark/import | op | 10 s | 3.8.20 |
| POST | /ark/drives/{root_id}/{eject\|init\|mount} ; PUT /ark/drives/{root_id}/profile ; POST /ark/prune | op ; admin ; admin | 60 s | 3.8.20a |
| GET | /catalog ; POST /catalog/refresh | op | 5 s / 120 s | 3.8.21 |
| GET | /services ; POST /services/{name}/{action} | op | 5 s / 120 s | 3.8.22 |
| GET | /logs | op | 10 s | 3.8.23 |
| GET | /events | op, or portal (types limited to `mode.*`, `model.*`, `service.state`, `drive.*`, `alert.*`) | none (stream) | 3.8.24, §3.10 |
| POST | /speech/stt ; /speech/tts ; GET /speech/voices | op or portal | 120 s / 120 s / 5 s | 3.8.25 |
| GET | /cloud ; POST /cloud/enroll ; DELETE /cloud ; POST /cloud/resend-key | op ; admin ; admin ; admin | 5 s / 30 s / 15 s / 15 s | 3.8.26 |
| GET | /doctor | op | 60 s | 3.8.27 |
| POST ; GET ; DELETE | /drill (`GET ?run_id=`) | op | 10 s | 3.8.28 |
| POST | /drill/manual | admin | 5 s | 3.8.28 |
| GET | /docs/index | op | 5 s | 3.8.29 |
| GET ; PUT | /profile | op ; admin | 5 s / 30 s | 3.8.30 |

#### 3.8.1 `GET /health`
No auth. Never touches upstreams; answers from memory. Used by systemd checks, the desktop splash, the installer and self-update.
```json
200 {"ok": true, "version": "0.1.0", "mode": "online", "starting": false}
```
`starting: true` until the startup sequence (§4.1) step 17 completes. Mutating routes open earlier, after step 12 (llama reconciled; §4.1 step 10): between step 12 and 17 `starting` is still `true` but mutations are accepted, so clients must not treat `starting: true` as "refuses everything" (they rely on `503 LAI-AGT-024` instead).

`ok` is `true` whenever the agent answers. The state store never fails to open: a damaged `state.json` falls back to the backup or to defaults with mode `offline`, and an unwritable directory keeps the state in memory (§3.5, §5). Both are reported as alerts (`LAI-AGT-003`, `LAI-AGT-004`) in `/status` and the doctor, not by `/health`, whose job is "the agent is alive and answering". The key stays in the body so that clients keep one shape; the status is always `200`.

#### 3.8.2 Sessions and UI codes
`POST /session` (header `Authorization: Bearer lat_…`, body `{}`) →
```
204 No Content
Set-Cookie: localai_session=<43 chars base64url>; Path=/; HttpOnly; SameSite=Strict; Max-Age=604800
```
`POST /session` is allowed with a bearer token only; any other operator credential (a session cookie, a socket peer) answers `401 LAI-SEC-001` with `details.reason: "bearer_required"`. Audit event `session.create` (`target.method: "bearer"`).

`POST /session/code` body `{"code": "482913"}` → same `204` + cookie. Errors: `401 LAI-SEC-008` (wrong/expired/used code), `429 LAI-SEC-004` after 5 failures in 5 min (global, not per client; `Retry-After`, `details.retry_after_s`, `details.locked: false`). While the window is full every attempt is refused with `429`, a correct code included, so the guesses stay bounded; the window frees when its oldest failure is 5 minutes old, and a bearer-authenticated `POST /session` is not behind the limiter. A body that is not exactly six digits (or has no `code`, or a non-string one) is `400 LAI-AGT-020` and is **not** counted: it cannot be a guess. Audit event `session.create` (actor `anonymous` via `ui_code`, `target.method: "ui_code"`).

`DELETE /session` → `204` and `Set-Cookie: localai_session=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0`. It needs the session cookie itself (a bearer or socket caller answers `401 LAI-SEC-001`, `details.reason: "session_cookie_required"`), and, being a cookie request, the CSRF rule. It also revokes the PIN grants earned with that session.

`POST /ui-code` body `{}` →
```json
200 {"code": "482913", "expires_at": "2026-11-02T15:09:05Z"}
```
At most 3 unexpired codes exist; minting a 4th invalidates the oldest (expired codes do not count). The code is never written to the audit log or the journal: audit event `ui_code.mint` records only who minted. Responses that carry a secret (`/session*` cookies, `/ui-code`, `/pin/verify`) send `Cache-Control: no-store`.

#### 3.8.3 `POST /pin/verify`
Body `{"pin": "4821"}` (4–8 digits). →
```json
200 {"grant": "pg_<43 chars base64url>", "expires_at": "2026-11-02T15:09:05Z"}
```
Errors: `401 LAI-SEC-003` (wrong PIN; `details.remaining_attempts`, the failures the 5-minute window still allows), `429 LAI-SEC-004` (`Retry-After` seconds, `details.retry_after_s`, `details.locked`), `409 LAI-SEC-005` (no PIN set; fix: `sudo aictl pin set`), `400 LAI-AGT-020` (no `pin` field, a non-string value, an unknown field). The grant is bound to the principal that obtained it (session id, or "bearer" + token fingerprint, or the socket uid) and is reusable until it expires (a fixed 5 minutes, not sliding). A PIN that is a string but malformed (empty, letters, 3 or 9 digits) is an ordinary wrong PIN: `401`, counted, and it costs the full argon2 time.

`409 LAI-SEC-005` is also the answer when `/etc/localai/admin-pin` exists but cannot be used (empty, not an argon2id PHC string, a symlink or directory; `details` stays empty and the journal says why); `sudo aictl pin set` rewrites it. It is not a failure and does not touch the limiter. The file is read, and its permissions enforced (`LAI-SEC-009` in the journal when fixed), on **every** verification, so `aictl pin set` applies at once.

Audit events: `pin.verify.ok`; `pin.verify.fail` with `code` `LAI-SEC-003` and `target.remaining_attempts` for a wrong PIN, and once more with result `denied` and code `LAI-SEC-004` on the request whose wrong PIN started the 1-hour lockout. Refusals while the window is full or the PIN is locked are not audited one by one (an attacker could otherwise fill the disk); the access log has them. The PIN, the hash and the grant are never audited or logged.

#### 3.8.4 `GET /status`
The full node snapshot. Also the body pushed to the cloud (§4.8.4), minus `alerts[].details` and `hardware.gpus[].processes`.
```json
200 {
  "node": {"id": "ai-n3", "display_name": "HP ZBook (ai-n3)", "hostname": "hp-linux", "profile": "small-8gb",
           "agent_version": "0.1.0", "backend": "cuda", "llama_mode": "router", "llama_build": "f3586e66b-cuda",
           "catalog_version": "2026.11.02-1", "uptime_s": 86400, "time": "2026-11-02T15:04:05Z"},
  "mode": {"value": "online", "since": "2026-11-01T22:10:00Z", "transition": null},
  "connectivity": {"value": "full", "checked_at": "2026-11-02T15:04:00Z", "none_since": null},
  "hardware": {"gpu": "NVIDIA GeForce RTX 2070 with Max-Q Design", "vram_total_mib": 8192, "vram_used_mib": 6791,
               "ram_total_mib": 14336, "ram_available_mib": 5120, "gpu_temp_c": 61, "cpu_temp_c": 72,
               "power": {"source": "ac", "battery_percent": 100, "charging": false}},
  "services": [
    {"name": "llama",   "unit": "localai-llama.service",   "active": "active", "sub": "running", "since": "2026-11-02T08:00:05Z", "restarts": 0, "healthy": true},
    {"name": "kiwix",   "unit": "localai-kiwix.service",   "active": "active", "sub": "running", "since": "2026-11-02T08:00:05Z", "restarts": 0, "healthy": true},
    {"name": "whisper", "unit": "localai-whisper.service", "active": "inactive", "sub": "dead", "since": null, "restarts": 0, "healthy": false}
  ],
  "models": {"default": "qwen3.5-9b-q4km", "loaded": ["qwen3.5-9b-q4km"], "loading": [], "present": 5, "loadable": 4, "models_max": 1},
  "ark": {"roots": [{"path": "/srv/localai", "kind": "internal", "status": "partial", "ready_pct": 82, "updated_at": "2026-11-02T03:12:00Z"}],
          "drive": {"mounted": false}, "sync": {"state": "idle", "last_success_at": "2026-11-02T03:12:00Z"}},
  "cloud": {"enrolled": true, "state": "linked", "url": "https://ai.marin.cr", "last_heartbeat_at": "2026-11-02T15:03:55Z"},
  "tailscale": {"manage": true, "pattern": "B", "applied": true, "backend_state": "Running"},
  "alerts": [{"code": "LAI-HW-012", "severity": "warning", "title": "Running on battery",
              "fix": "Plug in the charger. A laptop server stops when the battery runs out.", "since": "2026-11-02T08:00:30Z", "details": {"battery_percent": 64}}]
}
```

Rules of the body (AGENT-14, [§3.8.31](#3831-internalstatusapi-and-internalportal-seams-agent-14)):
- It is built from cached values only (hardware sample, router watcher view, services watcher, alert manager, Tailscale manager, state, config), never from a call to an upstream, so it answers within 5 s while the router or Tailscale hang. A subsystem that is not available has its empty state and the route still answers `200`: `services: []`, `alerts: []`, `models` with empty lists and `default: null`, `ark: {"roots": [], "drive": {"mounted": false}, "sync": {"state": "idle", "last_success_at": null}}`, `cloud: {"enrolled": false, "state": "unenrolled", "url": null, "last_heartbeat_at": null}`, `tailscale` from `node.yaml` (`applied: false`, `backend_state: null`), `mode` from `state.json`, else `offline` with `since: null`, `connectivity.value: "not_checked"`, `hardware.gpu: null` and `power.source: "unknown"`.
- `node.llama_build` is the build id of `state.json` (`llama.build`), `null` when none is recorded; `node.catalog_version` is the installed catalog's version, `null` for an empty catalog. `mode.since` is `null` on a fresh node.
- `hardware` also carries `gpus[]` (`index`, `vendor`, `name`, `vram_total_mib`, `vram_used_mib`, `temp_c`, `util_pct`, `throttling`, `processes`); `vram_*` are the sums over the GPUs, `gpu` the first GPU's name and `gpu_temp_c` the hottest. `hardware.power` is the whole `power` object of §3.8.6 (`time_remaining_s`, `profile` included).
- `models`: `loaded` and `loading` list the models whose router status is `loaded` and `loading`; `present` counts the models with a complete verified copy on a mounted root; `loadable` the models whose static fit passes (§3.8.7 `loadable`); `models_max` is the profile's `router.models_max` (at least 1); `default` the effective default (§3.8.7).
- `?counters=1` adds `node.counters` (`0` or absent leaves it out; any other value is `400 LAI-AGT-020`, `details.field: "counters"`): an object the producers fill (§7); without a producer it is `{}`.
- `alerts[].title` and `fix` are in the request's language (`Accept-Language`, `ui.language`, §3.7).
- The state pushed to the cloud (§4.8.4) is the same body built with `forCloud`: no `alerts[].details`, no `hardware.gpus[].processes`, English titles, no counters.

#### 3.8.5 `GET /portal/status`
No auth. Reduced and localized for the Survival Portal. **The schema and example are owned by [survival-portal.md](survival-portal.md) §3.5** (`brain.loaded`/`choices`, `capabilities`, `alerts`, …); this spec does not repeat them. It contains no paths, keys, hostnames or ids beyond model ids and display names. Ownership: AGENT-14 provides the route, the 2 s response cache and a minimal builder; PORTAL-01 implements the capability model and the full §3.5 schema (§10).
Route (`internal/portal`): no auth, 5 s; the body is cached for 2 s per language and session kind (so a request is never older than 2 s, and a burst of Portal tabs costs one build). Language, first match wins: `?lang=en|es` (any other value is `400 LAI-AGT-020`, `details.field: "lang"`); `portal.language` when it is `en` or `es` (`auto` and anything else skip this step); the profile's `survival.portal_language` when `en` or `es`; the request's language (`Accept-Language`, else `ui.language`, §3.7); English. `session` is `portal` for a Portal cookie, `full` for an operator or admin credential, `none` otherwise. The response carries `Cache-Control: no-store`. A builder error is `500 LAI-AGT-099` (the text never reaches the client) and is not cached.
Minimal builder (until PORTAL-01): `schema_version`, `generated_at`, `node_id`, `mode` (the committed mode), `language`, `emergency_number` (`portal.emergency_number`, default `911`), `session`, `power` (`on_ac`, `battery_pct`, `minutes_left`, `state` one of `discharging`, `charging`, `full`, `no_battery`; `low` is on battery under 20 %, `critical` on battery under 10 %), `clock_ok` (false when the clock is earlier than the agent's build time), `brain.loaded` and `brain.loading` (models the router reports and the catalog knows: `id`, `display_name`, `roles`, `vision`; a LLAMA_CACHE model is left out), `brain.choices: []`, `capabilities.chat` (`ready` with a loaded brain, `degraded` with `reason: "loading"` while one loads, else `unavailable` with `code: LAI-SVC-106`), `first_aid: []` and `alerts` (`code`, `severity`, `title`, `fix` in the language, never `details`). Redaction is by construction (the types have no field for a path, address, host name or token) and by a table test over a node full of them.

#### 3.8.6 `GET /hardware`
Auth: operator; deadline 10 s. Query `?fresh=1` forces a new sample (rate-limited to one per 2 s): a request inside the 2 s window of the previous forced sample is not an error, it gets the cached view; any other value of `fresh` than `1` or `0` is `400 LAI-AGT-020`. A forced sample is capped at 5 s whatever the probes do, so the route answers within its deadline even with a hung `nvidia-smi` (that GPU section comes back `stale: true`). A sampler that has not sampled yet is asked to once. The limit applies to API clients only: `aictl bench` samples GPU memory **in-process** every 250 ms through the `internal/hardware` sampler interface and does not use this route ([llama-router.md](llama-router.md) §4.13).
```json
200 {
  "sampled_at": "2026-11-02T15:04:00Z",
  "gpus": [{"index": 0, "vendor": "nvidia", "name": "NVIDIA GeForce RTX 2070 with Max-Q Design", "backend": "cuda",
            "compute_cap": "7.5", "driver": "595.99.02", "unified_memory": false,
            "vram_total_mib": 8192, "vram_used_mib": 6791, "vram_used_by_others_mib": 210,
            "temp_c": 61, "util_pct": 0, "power_w": 1.46, "throttling": [],
            "processes": [{"pid": 4242, "name": "llama-server", "vram_mib": 6581}]}],
  "ram": {"total_mib": 14336, "available_mib": 5120, "swap_total_mib": 4096, "swap_free_mib": 4000},
  "disks": [{"path": "/srv/localai", "role": "internal_root", "fs": "ext4", "total_bytes": 342000000000, "free_bytes": 293000000000},
            {"path": "/var/lib/localai", "role": "state", "fs": "ext4", "total_bytes": 342000000000, "free_bytes": 293000000000}],
  "power": {"source": "ac", "battery_percent": 100, "charging": false, "time_remaining_s": null, "profile": "performance"},
  "thermal": {"cpu_temp_c": 72, "cpu_throttle_events": 0, "zones": [{"type": "x86_pkg_temp", "temp_c": 72}]},
  "cpu": {"model": "Intel(R) Core(TM) i7-10850H CPU @ 2.70GHz", "cores": 12, "load1": 0.4}
}
```
`disks[].role` ∈ `internal_root` (`/srv/localai`), `drive_root` (a mounted ark drive content root), `state` (`/var/lib/localai`), `opt` (`/opt/localai`), `etc` (`/etc/localai`); one entry per role (per drive for `drive_root`), even when several share a filesystem. `gpus` is `[]` on a CPU-only node. GPU UUIDs and serial numbers are **never** collected (redaction rule).
Value rules: `sampled_at` is when the fast probes last completed (UTC). Any section (`gpus[]` entry, `ram`, `disks[]` entry, `power`, `thermal`, `cpu`) whose last probe failed carries `"stale": true` and the previous values; the key is absent otherwise. Values the machine cannot give are `null`: GPU `compute_cap`, `driver`, `temp_c`, `util_pct`, `power_w`, `vram_used_by_others_mib` (AMD without DRM memory accounting); `power.battery_percent` (no battery), `power.time_remaining_s` (not discharging), `power.profile` (no `powerprofilesctl`); `thermal.cpu_temp_c` (no sensor). `power.source` is `ac`, `battery` or `unknown`. `throttling` lists `thermal` and/or `power_brake`. On a unified-memory GPU `vram_total_mib` and `vram_used_mib` are the fit values of §4.4.2 (GTT size, VRAM plus GTT used). The route is implemented by `hardware.Register` in `internal/hardware` (not in `internal/api`, so `api` never imports `hardware`).

#### 3.8.7 `GET /models`, `GET /models/{id}`
Catalog models (all engines) merged with local state. Query `?loadable=1` filters to `loadable: true`; `?engine=llama`.
```json
200 {"models_max": 1, "default": "qwen3.5-9b-q4km", "models": [
  {"id": "qwen3.5-9b-q4km", "display_name": "Qwen 3.5 9B", "summary": "Fast general assistant…", "engine": "llama",
   "roles": ["chat", "reasoning", "coder"], "survival_tier": 1, "in_profile": true, "size_bytes": 5868826976,
   "present": true, "verified": true,
   "locations": [{"root": "/srv/localai", "complete": true, "verified_at": "2026-11-02T03:10:00Z"}],
   "loadable": true, "loadable_reason": null,
   "status": "loaded", "loaded": true, "default": true, "aliases": ["qwen3.5-9b"],
   "requirements": {"backends": ["cuda", "vulkan", "cpu"], "min_vram_mib": 6400, "min_ram_mib": 8192},
   "last_load_ms": 7100},
  {"id": "qwen3.6-35b-a3b-q4km", "display_name": "Qwen 3.6 35B A3B", "engine": "llama", "in_profile": false,
   "present": false, "verified": false, "locations": [],
   "loadable": false, "loadable_reason": {"code": "LAI-CAT-005", "message": "Needs 11.0 GB of graphics memory; this computer has 8.0 GB."},
   "status": "absent", "loaded": false, "default": false, "aliases": []}
]}
```
`status` ∈ `absent` (no complete copy), `stored` (present, not in router), `unloaded`, `loading`, `loaded`, `sleeping`, `failed` (router reported `failed: true` last time), `n/a` (non-llama engines). `loadable_reason.code` ∈ `LAI-CAT-005` (fit/backend), `LAI-LLM-008` (files missing/unverified), `LAI-LLM-004` (router down). `GET /models/{id}` returns one object, plus `"files": [{"kind","name","size_bytes","sha256","path"}]` and `"preset": "<INI section text>"`; unknown id → `404 LAI-AGT-021`.

`?loadable` is `1`/`true` (keep `loadable: true`) or `0`/`false`/absent (keep all); `?engine` is `llama`, `whisper` or `piper`; any other value is `400 LAI-AGT-020` with `details.field`. The two filters combine. The merge reads cached values only (the router watcher's last poll, the hardware sample, the roots' manifests), so the route never waits for an upstream service. In `GET /models/{id}`, `files[].path` is the file's absolute path on the first root that holds a complete copy (`""` when the model is on no mounted root), and `preset` is the model's section of `models.ini` as the generator renders it (including `load-on-startup = true` for the default), `""` when the router has no section for it: not a llama model, no complete copy, or it does not fit this node. A model that would load if the router answered still has its section.

#### 3.8.8 `POST /models/{id}/load`
Body `{"wait": true, "unload_others": true}` (both optional, defaults shown). Orchestration: [llama-router.md](llama-router.md) §4.4.
```json
200 {"id": "qwen3.5-9b-q4km", "status": "loaded", "already_loaded": false, "load_ms": 7100,
     "unloaded": ["small-vlm-q4km"], "op_id": "op_7k2m9q4r8t1c"}
202 {"id": "qwen3.5-9b-q4km", "status": "loading", "op_id": "op_7k2m9q4r8t1c"}          (wait: false)
```
The response may add `warning: {code, message}` (`LAI-HW-005`, `LAI-HW-006`: the load went ahead although other programs hold much of the memory). A Portal session's body is ignored and forced to `wait: false` ([survival-portal.md](survival-portal.md) §3.3). The handler is `internal/api/models_ops.go` over the orchestrator of [llama-router.md](llama-router.md) §4.4.1.
Errors: `404 LAI-AGT-021`; `409 LAI-AGT-022` (another model operation running; `details.op_id` of the running one); `422 LAI-CAT-005` (does not fit, backend unsupported, or engine is not llama); `422 LAI-LLM-008` (files missing/unverified); `409 LAI-LLM-011` (`unload_others: false` and no room); `502 LAI-LLM-004`; `500 LAI-LLM-009` / `504 LAI-LLM-010` (failed/timed out, previous restored, `details.rolled_back: true`); `500 LAI-LLM-012` (rollback also failed).

#### 3.8.9 `POST /models/{id}/unload`
Body `{}`. → `200 {"id": "…", "status": "unloaded", "already_unloaded": false, "op_id": "…"}`. Unloading the default model is allowed (the default stays recorded). In `survival` mode, unloading the last loaded LLM answers `409 LAI-AGT-023` unless the body has `"force": true`. A model that would not stop makes the agent restart llama (llama-router.md §4.5): the answer is still `200`, with `"restarted": true` and `"restored": [...]`.

#### 3.8.10 `PUT /models/default`
Body `{"id": "qwen3.5-9b-q4km"}` → `200 {"default": "qwen3.5-9b-q4km", "previous": "…", "ini_regenerated": true}`. The model must be `present` and `loadable` (`422` otherwise). Sets `state.models.default`, regenerates `models.ini` (`load-on-startup`), reloads the router preset list (`GET /models?reload=1`; this does not unload a running unchanged model). Does **not** load the model.

#### 3.8.11 `POST /models/{id}/download`
Body `{"roots": ["/srv/localai"]}` (optional; default = internal root, plus the drive if its profile includes the model). → `202 {"id": "…", "sync_run_id": "run-20261102T031500Z-a1f3", "queued": true}` (`sync_run_id` uses the ark-sync run id format `run-<ts>-<hex>`, §3.8.18). Delegates to the sync engine with `items: [id]` ([ark-sync.md](ark-sync.md)). Errors: `404 LAI-AGT-021` (unknown model); `400 LAI-AGT-020` (a root that is empty, or more than 16 roots, or one the engine does not know: `details.field: "roots"`); `409 LAI-SYN-010` in survival (even with `allow_offline_mode`); `409 LAI-AGT-023` in offline mode unless `"allow_offline_mode": true` (`details.requires`); `409 LAI-SYN-005` when a sync run is already active (`details.run_id`); `422 LAI-ARK-001` when no mounted root is writable. The route queues the run and answers at once: the transfer, its sha256 verification, the atomic swap, disk-full handling and the `sync.*` progress events are the sync engine's ([ark-sync.md](ark-sync.md) §4.6, §4.8). A node heading to a mode counts as being in it.

#### 3.8.12 `DELETE /models/{id}` (admin)
Query `?root=/srv/localai` (optional, default all roots) and `?keep_last=true` (default `true`). Refuses to delete a loaded model (`409 LAI-AGT-022`, fix "Unload it first"). With `keep_last=true` the sync engine never removes a copy whose removal would leave no verified copy on any mounted root ([ark-sync.md](ark-sync.md) §4.10 R1, R7), one root at a time: the copies it may delete are deleted (a model on two roots loses one copy and keeps the other, listed in `kept` with the reason `last_verified_copy`), and when nothing could be deleted for that reason the answer is `409 LAI-SYN-044` (`details.kept`). A root that holds no copy is listed in `kept` with the reason `not_present`; a model that is nowhere is `200` with an empty `deleted` (a repeated delete is not an error). → `200 {"id": "…", "deleted": [{"root": "/srv/localai", "bytes": 5868826976}], "op_id": "…"}` (optional `kept: [{root, reason}]`). A model the router holds in memory (`loaded`, `loading`, `sleeping`) is refused with `details.reason: "loaded"`; another model operation holding `modelOpsMu` is `409 LAI-AGT-022` with the holder's `details.op_id`; an unknown root is `400 LAI-AGT-020` (`details.field: "root"`). The delete runs under `modelOpsMu` (kind `delete`, its op id is the response's `op_id`), so it never overlaps a load, an unload, a reload or a llama restart. When a llama model lost copies the route regenerates `models.ini` afterwards (llama-router.md §4.2, lock released first); a problem there is `warnings: [{code, message}]` and never fails the delete. Audit `model.delete` (`ok`, `fail` or `denied` with the code; target `model`, `root`, `roots`, `keep_last`). `keep_last=false` is for an admin who accepted the loss (`aictl delete --allow-last-copy`, after a second confirmation); the cloud's `model.delete` command always sends `true`.

#### 3.8.13 `GET /key`
The only route that returns the full llama key ([security-model.md](../architecture/security-model.md) §2). Writes audit `key.show` with the principal and the key's fingerprint (never the key) before it answers, and the response is `Cache-Control: no-store`. A key file that is missing or is not 64 lowercase hex digits is `500 LAI-SEC-010` (`details.reason`: `unreadable` or `malformed`, audited as a failure) and nothing of the key is returned. `models` lists the llama models that are `loadable` here; the snippets name the default model when it is among them, else the first.
```json
200 {
  "key": "<64 lowercase hex>",
  "fingerprint": "ab12…9f3c",
  "base_urls": {"local": "http://127.0.0.1:8080/v1",
                "tailnet": "https://hp-linux.<TAILNET>.ts.net/v1",
                "public": "https://ai-n3.marin.cr/v1"},
  "models": ["qwen3.5-9b-q4km", "small-vlm-q4km"],
  "snippets": {
    "curl": "curl -s http://127.0.0.1:8080/v1/chat/completions -H \"Authorization: Bearer $KEY\" -H 'Content-Type: application/json' -d '{\"model\":\"qwen3.5-9b-q4km\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}'",
    "env": "OPENAI_BASE_URL=http://127.0.0.1:8080/v1\nOPENAI_API_KEY=<key>",
    "opencode": {"provider": {"local-llama": {"npm": "@ai-sdk/openai-compatible", "options": {"baseURL": "http://127.0.0.1:8080/v1", "apiKey": "{file:~/.config/opencode/llama-api-key}"},
                 "models": {"qwen3.5-9b-q4km": {"limit": {"context": 32768, "output": 8192}}}}}},
    "openai_python": "from openai import OpenAI\nclient = OpenAI(base_url=\"http://127.0.0.1:8080/v1\", api_key=KEY)"
  }
}
```
`tailnet` is computed at runtime from `tailscale status --json` (`Self.DNSName`) and the pattern (A → `http://<dnsname>:18080/v1`, B → `https://<dnsname>/v1`), `null` when pattern is `none` or tailscale is unavailable. `public` is `https://<node.public_host>/v1` or `null`. Snippets embed `$KEY`/`<key>` placeholders, never the key itself; the client substitutes.

#### 3.8.14 `POST /key/rotate` (admin)
Body `{"restart": true}`. Steps: generate 32 random bytes → 64 hex; write `/etc/localai/api-key` atomically (0600); restart `localai-llama`; restore `models.desired_loaded`; push the new key to the cloud (§4.8.5); update `state.key`. →
```json
200 {"fingerprint": "c0de…77aa", "rotated_at": "2026-11-02T15:10:00Z", "cloud_synced": true,
     "restored_models": ["qwen3.5-9b-q4km"],
     "clients_to_update": ["OpenCode on this computer: ~/.config/opencode/llama-api-key",
                           "Any script that uses the old key directly"],
     "op_id": "op_…"}
```
`"restart": false` writes the new key without restarting llama: llama keeps serving with the old key until its next restart, the response has `restored_models: []`, and alert `LAI-AGT-016` (restart needed, `details.service: "llama"`) is raised. Default `true`. Gateway and Open WebUI clients need no change (the gateway receives the key from the cloud). If the llama restart fails, the **new key stays** (the old one is assumed leaked) and the response is `500 LAI-LLM-001` with `details.key_rotated: true` (and `details.fingerprint`, the live key's); any other failure after the key was written (the router not answering during the restore, `502 LAI-LLM-004`) carries the same two details. Audit `key.rotate`.

The write happens **under `modelOpsMu`**: with `restart: true` the route calls `Orchestrator.RestartWith` with a `Prepare` step that writes the key file (atomically, 0600 root:root) and records `state.key` once the lock is held and before the unit is restarted, so a busy lock (`409 LAI-AGT-022`, the holder's `details.op_id`) leaves the old key, the state and the cloud untouched and no model operation can run between the write and the restart; with `restart: false` the route holds the lock (kind `rotate`) only for the write. A write that fails (no space, read-only disk) is `500 LAI-AGT-099` with `details.reason: "key_write_failed"` and `key_rotated: false`; the old key is still in place and llama is not restarted. The cloud push (§4.8.5) runs after the lock is released, only in online mode and only when the node is linked (`cloud_synced: false` otherwise, and a failed push is retried by the link); `key.rotated {fingerprint}` is published after the push, so the router watcher re-reads the key only once llama has restarted. The caller's own router client re-reads the key file at once, so with `restart: false` the agent's own router calls are rejected (`LAI-LLM-005`) until llama is restarted, exactly like every other client of the old key; that is what `LAI-AGT-016` asks the operator to do, and the alert is withdrawn by the next rotation with a restart.

#### 3.8.15 `GET /mode`, `POST /mode`
```json
GET 200 {"mode": "online", "since": "…", "transition": null,
         "allowed_targets": ["offline", "survival"], "leave_requires_admin": false,
         "connectivity": "full", "suggest_survival": false}
```
`since` is when the mode was committed (`mode_changed_at`), `null` on a node that never changed mode. While a transition runs, `transition` is `{"op_id": "op_…", "from": "online", "target": "survival", "step": "survival_models", "started_at": "…", "warnings": [{"code", "message"}]}` (the steps' warnings so far); `mode` is still the old mode until `persist`. `allowed_targets` is the other two modes (every transition is allowed in every direction), `leave_requires_admin` is true in survival, `connectivity` is `state.connectivity.value` (`not_checked` when empty), and `suggest_survival` is true when the mode is `online` and connectivity has been `none` for more than 30 minutes ([modes.md](../architecture/modes.md) §4; never automatic).
`POST /mode` body `{"target": "offline", "wait": false, "reapply": false}`. `reapply: true` re-runs every step of the current mode's transition even when `target` equals the current mode (aictl `--reapply`, §4.2.3); without it a same-target request answers `200 {already: true}`. →
```json
202 {"op_id": "op_…", "from": "online", "target": "offline", "transition": {"step": "link_stop", "started_at": "…"}}
200 {"op_id": "op_…", "from": "online", "mode": "offline", "warnings": []}                        (wait: true)
200 {"mode": "offline", "already": true}                                                          (target == current)
```
Errors: `403 LAI-SEC-002` leaving survival without admin* (`details.pin_required: true`; the refusal is audited as `mode.change` `denied`); `409 LAI-AGT-022` another transition running (`details.op_id`, `details.target`; exception: a request for `survival` preempts, §4.2.3), which is also the answer to a `wait: true` request whose transition survival preempted (`details.preempted: true`, `details.op_id` the survival operation); `503 LAI-AGT-024` while the agent starts or stops (the gate, and the engine when it is not accepting: only `POST /mode/survival` is queued, §3.8.16). Cloud principals may only request `offline` (enforced in §4.8.6, not here). "Leaving survival" is judged on the mode the node is heading to: a request for another mode while a survival transition runs needs admin* too.

#### 3.8.16 `POST /mode/survival`
No auth, loopback only, still subject to Host/Origin checks (§3.9.4). Body `{}` or `{"reapply": true}` (Content-Type still required; `reapply` as in §3.8.15). → `202 {"op_id": "…", "target": "survival"}` or `200 {"mode": "survival", "already": true}`. Before startup step 12 (§4.1 step 10) and during shutdown (§4.12) the request is **queued**: it is persisted as `state.json: pending_mode: "survival"` (so a crash does not lose it) and answered `202 {"op_id": "…", "target": "survival", "queued": true}`; it runs right after step 12 (or on the next start). Never refuses for any other reason.

#### 3.8.17 `GET /ark`, `GET /ark/drives`
Shapes owned by [ark-sync.md](ark-sync.md) §3.5 (authoritative; summary here): `GET /ark` → `{"roots": [<READY.json of each mounted root>]}` (ark-sync.md §4.12.1); `GET /ark/drives` → `{"drives": [{"root_id", "device", "mount", "fstype", "label", "size_bytes", "free_bytes", "state", "read_only", "fs_errors", "initialized", "profile", "ready": {"overall", "generated_at"}, "problems": [...]}]}`.

#### 3.8.18 `/ark/sync`
`POST` body `{"roots": [], "items": [], "dry_run": false, "allow_offline_mode": false, "trigger": "manual"}` → `202 {"run_id": "run-20261102T031500Z-a1f3", "state": "running", "events": "/api/v1/events?types=sync.*"}`, or for `dry_run` → `200` with an array of plans, one per root (never in `409 LAI-SYN-010/011`: a dry run plans without the network in `offline` and `survival`, and writes nothing). `GET` → the active or last run (`404 LAI-AGT-021` before the first run); `DELETE` → `202 {"run_id", "state": "cancelling"}`, or `202` with `state: "idle"` and the last run's id when nothing is running. A scheduled run that preflight ends answers `202` with `state: "skipped"`, `reason` and `code`. Schemas and errors (`LAI-SYN-*`) are owned by [ark-sync.md](ark-sync.md) §3.5; the agent only routes. For a real run, in `survival`: `409 LAI-SYN-010`; in `offline` without `allow_offline_mode`: `409 LAI-SYN-011`; a run already active: `409 LAI-SYN-005`.

#### 3.8.19 `POST /ark/verify` — body `{"roots": [], "items": [], "full": false}` → `202 {"run_id": "run-…"}` ([ark-sync.md](ark-sync.md) §3.5).
#### 3.8.20 `POST /ark/import` — body `{"from_root": "ark-3f9c0a12", "items": [], "add_to_profile": false, "catalog": false, "dry_run": false}` (`from_root` is a root id) → `202 {"run_id": "run-…"}`. Works in every mode (no network). [ark-sync.md](ark-sync.md) §3.5, §4.14.

#### 3.8.20a Drive management and prune
Expansions of the brief's table requested by [ark-sync.md](ark-sync.md) §11 (shapes and rules owned there): `POST /ark/drives/{root_id}/eject` (stop using the drive, sync and flush, unmount), `POST /ark/drives/{root_id}/init` (create `localai/` + `ROOT.json` on an empty AI-ARK filesystem), `POST /ark/drives/{root_id}/mount` (mount a detected but unmounted AI-ARK filesystem), `PUT /ark/drives/{root_id}/profile` (admin; `{"profile": "ark-drive-full"}`), `POST /ark/prune` (admin; orphans and trash, never the last verified copy).

#### 3.8.21 `GET /catalog`, `POST /catalog/refresh`
`GET` → `{"version": "2026.11.02-1", "source": "cloud|github|offline|kit|test", "fetched_at": "…", "models": [ …as /models… ], "content": [ {"id", "kind", "display_name", "present", "verified", "in_profile", "size_bytes", "status"} ], "profiles": [ {"id", "display_name", "include": {"models": ["…"], "content": ["…"]}, "budget_bytes"} ]}`. `profiles` lists every profile of the installed catalog with the ids it includes (catalog.md §4), so `aictl profile set` can show what a profile keeps, with sizes from `models` and `content`, before it asks ([aictl.md](aictl.md) §4.12); additive, like the other fields.
`source`: `cloud` (fetched from the cloud), `github` (fetched from GitHub when not enrolled), `offline` (the installed copy from an earlier fetch or an ark import, [catalog.md](catalog.md) §1), `kit` (loaded at startup from `<root>/kit/catalog/` because `/etc/localai/catalog/` was missing or invalid, §4.1 step 8), `test` (a draft catalog installed with `aictl catalog use <dir> --test`, [aictl.md](aictl.md) §4.11.4, recognised by the `TEST` marker file in `/etc/localai/catalog/`).
`POST /catalog/refresh` body `{}` → `200 {"version": "…", "previous": "…", "changed": true, "source": "cloud"}`. In offline/survival modes: `409 LAI-AGT-023` (refresh needs network; `aictl catalog restore` works offline). Rejected new catalog: `422 LAI-CAT-002` (previous kept).
AGENT-14 details ([§3.8.31](#3831-internalstatusapi-and-internalportal-seams-agent-14)): `models` are the rows of §3.8.7 without `files` and `preset`. `content[].status` is the item status of READY.json ([ark-sync.md](ark-sync.md) §4.12.1: `verified`, `stale`, `missing`, `skipped`, `failed`, `quarantined`, `orphan`), or `absent` when no mounted root lists the item; `present` is true for a verified or stale copy and `verified` only for `verified`; `in_profile` follows the effective plan (§3.8.30), so an item the budget dropped is not in the profile. `fetched_at` is when the catalog was installed (the mtime of `CATALOG_VERSION`), `null` when unknown. A node whose catalog did not load (`LAI-CAT-001`, the empty catalog) answers `version: ""`, empty lists and `source: "offline"`. `POST /catalog/refresh` is refused with `409 LAI-AGT-023` (`details.mode`) when the mode the node is heading to (`Info.Effective`) is not `online`, before any source is asked; a fetch that no source could deliver (`no_source`, `fetch_failed`) is `502 LAI-CLD-013` with `details.reason`, the previous catalog kept (the transport error is logged, never returned); a rejected catalog is `422 LAI-CAT-002` with `details.version`, `details.problem_count` and the first ten `details.problems[]` (`code`, `file`, `path`, `message`). After a successful refresh the catalog store is replaced, and when the version changed the router presets are regenerated (§3.8.31); a problem there is a `warnings[]` entry of the answer (`code`, `message`), never an error. Audit `catalog.refresh` (`ok` with `version`, `previous`, `source`; `fail` with the code).

#### 3.8.22 `GET /services`, `POST /services/{name}/{action}`
`name` ∈ `llama`, `kiwix`, `whisper`; `action` ∈ `start`, `stop`, `restart`. Auth `op`; the routes are added to the registry by `api.RegisterServices` (AGENT-09; `internal/app` calls it, AGENT-20).

`GET` returns `{"services": [...]}`: the `services` entries of `/status` plus `enabled`, `last_exit` and the health `state` of [companion-services.md](companion-services.md) §4.6 (`ok`, `starting`, `degraded`, `down`; §4.3.3 says when each applies). Only `name` and `unit` are always present; the per-service fields of companion-services.md §4.6 (`last_check`, `detail`, `books`, `model`, `device`, `voices`, `maps`, `problems`) are owned by that spec. `since` is when the unit became active and is `null` while it is not running; `last_exit` is how the main process last ended and is `null` when it never exited. The list is built from the service watcher's cache; a service the watcher has not reported yet is read from systemd first, so the answer is never empty. With the companion health supervisor (SVC-01, `ServicesDeps.Health`, [companion-services.md](companion-services.md) §4.6) wired, its records replace the watcher's rows of kiwix and whisper (all six states of §4.6, `problems` always an array of `{code, severity}` with the primary problem first) and add piper and maps, which have `unit: null` and no `active`, `sub`, `restarts` or `enabled`; the order is llama, kiwix, whisper, piper, maps. `GET /status` `services` rows carry the same `state`.
```json
200 {"services": [
  {"name": "llama", "unit": "localai-llama.service", "active": "active", "sub": "running", "since": "2026-11-02T08:00:05Z",
   "restarts": 0, "healthy": true, "state": "ok", "enabled": true, "last_exit": {"code": 0, "at": "2026-11-02T07:59:58Z"}},
  {"name": "whisper", "unit": "localai-whisper.service", "active": "inactive", "sub": "dead", "since": null,
   "restarts": 0, "healthy": false, "state": "down", "enabled": true, "last_exit": null}]}
```

`POST` takes an optional body, `{}` or `{"force": true}` (strict: an unknown field is `400 LAI-AGT-020`; `force` only matters for stopping `llama` and is ignored elsewhere), and waits at most 60 s **in total** (the systemd job and the wait for the final state): until the unit is `active` after a start or restart, and `inactive` (or `failed`: the process exited non-zero on SIGTERM, it is not running) after a stop. A start or restart whose unit lands in `failed` or `inactive` does not wait out the minute. Every action gets an `op_id` (header `X-LocalAI-Op-Id` and body) and is audited as `service.start|stop|restart` (`target.service`, `target.unit`, `target.force` when set; `result` `ok`, `fail` with the code, or `denied`). The route deadline is 120 s: 60 s for the unit plus, for `llama`, the router wait and the model restore.
```json
200 {"name": "llama", "active": "active", "restored_models": ["qwen3.5-9b-q4km"], "op_id": "op_7k2m9q4r8t1c"}
```
- **`llama`.** Every action runs through the `LlamaRestarter` hook of `internal/api`, implemented by ROUTER-06 (`router.NewLlamaHook`, [llama-router.md](llama-router.md) §4.11.1): it takes `modelOpsMu` without blocking (§4.9; busy → `409 LAI-AGT-022` with `details.op_id` of the holder, and the unit is not touched), runs the unit action, and after a start or restart waits for the router's `/health` and restores `models.desired_loaded`, returning the ids it restored. Until ROUTER-06 is wired the handler runs the action alone and answers `restored_models: []`. `kiwix` and `whisper` do not use the hook and always answer `restored_models: []`.
- **Survival.** Stopping `llama` in survival mode without `"force": true` is `409 LAI-AGT-023` (`details.mode: "survival"`, `details.requires: "force"`), nothing runs, and the refusal is audited as `denied`. Only `llama` is protected: stopping `kiwix` or `whisper` in survival is allowed, and so are starting and restarting `llama`.
- **Errors.** Unknown `name` → `404 LAI-AGT-021` (`details.kind: "service"`). Unknown `action` → `400 LAI-AGT-020` (`details.field: "action"`, `details.reason: "unknown_action"`, `details.allowed`). A start or restart that left the unit not running (systemd job `failed`, `systemctl` exit non-zero, the unit landing in `failed`, or no `active` within the minute) → `502` with the service's "not running" code: `LAI-LLM-001` for `llama`, `LAI-SVC-002` for `kiwix`, `LAI-SVC-011` for `whisper`, and `details` `{service, unit, action, reason, active, sub, op_id}` where `reason` is `job_failed`, `timeout`, `wrong_state` or `unavailable` (systemd could not be asked). A stop that did not stop is `500 LAI-AGT-099` with the same details. An allowlist violation (§4.3.1) is `500 LAI-AGT-099`.
- Starting a unit that is running, or stopping one that is stopped, is a no-op that answers `200` (systemd's own idempotence), not an error.

#### 3.8.23 `GET /logs`
Query: `unit` ∈ `agent|llama|kiwix|whisper|sync|drill` (required; the short name, never a unit file name), `lines` 1..2000 (default 200), `since` (RFC 3339, optional; a `+` in an offset must be written `%2B`). Auth `op`; registered by `api.RegisterLogs`. Implementation: `journalctl -u <unit> -n <lines> [--since <t>] -o json --no-pager` through `execx` with a 5 s timeout and 4 MiB output cap (§4.3.5); each `MESSAGE` passes the scrubber (§4.11) **after** it is decoded, never on the raw line. →
```json
200 {"unit": "localai-llama.service", "lines": [{"at": "2026-11-02T08:00:12Z", "priority": 6, "message": "llama_server: model loaded"}], "truncated": false}
```
Short name → unit: `agent` → `localai-agent.service`, `llama`, `kiwix`, `whisper`, `sync`, `drill` → `localai-<name>.service`. `lines` is the number of entries (oldest first); `at` is the entry's realtime timestamp with one-second precision; `truncated` is `true` when the 4 MiB cap cut the output (the partial last line is dropped).

A bad query is `400 LAI-AGT-020` with `details.field` and `details.reason`: `unit` (`missing`, `unknown_unit`; `details.allowed` lists the six names), `lines` (`not_an_integer`, `out_of_range` with `details.min` and `details.max`; `0`, `2001`, `-1` and an empty value all fail), `since` (`not_rfc3339`). Nothing from the query reaches `journalctl` except through these checks: the unit is looked up in the table above, `lines` is an integer, and `since` is parsed and re-written as `YYYY-MM-DD HH:MM:SS UTC` in its own argv element (journalctl's parser does not take every RFC 3339 form). A `journalctl` timeout is `504 LAI-AGT-099` (`details.reason: "journalctl_timeout"`), any other failure `500 LAI-AGT-099` (`details.reason: "journalctl_failed"`, `details.exit_code`).

#### 3.8.24 `GET /events`
SSE, §3.10.

#### 3.8.25 Speech
Owned by [companion-services.md](companion-services.md) §3.4 (request/response schemas, limits, errors); the agent only routes them.
- `POST /speech/stt`: multipart audio (field and format rules in companion-services.md), proxied to whisper-server on `127.0.0.1:<ports.whisper>` (`POST /inference`). → `200 {"text": "¿Cómo purifico agua?", "language": "es", …}`.
- `POST /speech/tts`: `{"text", "voice"?, "language"?, "speed"?}` → `200 audio/wav` produced by exec'ing piper (fixed argv). Concurrency and queue limits → `429 LAI-SVC-015`.
- `GET /speech/voices` (an expansion of the brief's table, requested by companion-services.md §11) → `{"voices": [{"id", "language", "display_name", "default"}]}`.
Auth: `op` or `portal`.

#### 3.8.26 Cloud link
`GET /cloud` →
```json
200 {"enrolled": true, "url": "https://ai.marin.cr", "state": "linked", "server_node_id": "<uuid>", "name": "ai-n3",
     "since": "…", "last_heartbeat_at": "…", "last_claim_at": "…", "last_error": null, "credential": "nod_…a1b2",
     "eligible_types": ["localai.status.refresh", "…"], "server_features": ["node_host_vitals"],
     "state_transport": "endpoint", "inflight": null, "key_synced": true}
```
`state_transport` ∈ `endpoint` (snapshots go to `POST …/localai/state`, §4.8.4) | `capabilities` (fallback while the cloud lacks that endpoint: the snapshot rides in the EAB capabilities report).
`POST /cloud/enroll` (admin) body `{"url": "https://ai.marin.cr", "credential": "nod_…"}` → writes the credential (0600), sets `cloud.url` in `node.yaml`, registers synchronously (10 s). →
`200 {"state": "linked", "server_node_id": "<uuid>", "reattached": false}`; network failure → `200 {"state": "backoff", "last_error": "…"}` (credential kept, link retries); `401` from the cloud → `422 LAI-CLD-051` and the credential is **not** kept. In offline/survival: credential is stored, `state: "stopped"`. Audit `cloud.enroll`.
`DELETE /cloud` (admin) → deregister (3 s, errors ignored), delete the credential and `cloud/link.json` → `200 {"enrolled": false}`. Audit `cloud.unenroll`.
`POST /cloud/resend-key` (admin; an expansion of the brief's route table, needed by [cloud-app.md](cloud-app.md) §3.6) body `{}` → pushes the llama key now (§4.8.5) → `200 {"key_synced": true, "fingerprint": "ab12…9f3c"}`; not linked → `409 LAI-AGT-023`.
Details fixed by AGENT-17 (§4.8.9): `GET /cloud` always sends every member (null or empty when not known); not enrolled it is `{"enrolled": false, "state": "unenrolled", "url": <node.yaml cloud.url>, …nulls}`. `credential` is the credential masked as `nod_…` plus its last four characters, never the credential. `POST /cloud/enroll` answers `state: "backoff"` (with `last_error`, credential kept) for a network failure and also for a registration the cloud refused with `400` or `403` (`LAI-CLD-054`, retried in an hour); a non-https `url` is `422 LAI-CLD-062`; a malformed credential (not `nod_` plus token characters) or a missing field is `400 LAI-AGT-020`; a second enroll or unenroll while one runs is `409 LAI-AGT-022`. `POST /cloud/resend-key` answers `200 {"key_synced": false, "fingerprint": …}` when the push did not go through (the link keeps retrying), `409 LAI-AGT-023` only when the node is not linked or not online. Every `/cloud` mutation needs `Content-Type: application/json` (§3.7), `DELETE /cloud` included.

#### 3.8.27 `GET /doctor`
Query `?area=LLM` (repeatable), `?fast=1` (only checks marked fast, ≤ 5 s total). Runs checks concurrently (max 4 at once), each with its own timeout (default 10 s). →
```json
200 {"ran_at": "…", "duration_ms": 2310,
     "summary": {"ok": 41, "info": 1, "warning": 2, "error": 0, "critical": 0},
     "results": [{"check": "hw.battery", "code": "LAI-HW-012", "severity": "warning", "title": "Running on battery",
                  "fix": "Plug in the charger. A laptop server stops when the battery runs out.", "details": {"battery_percent": 64}}],
     "passed": ["agt.config", "llm.router_health", "net.loopback_only", "…"]}
```
Only non-ok results appear in `results`. Severity order: `info < warning < error < critical`.

- `results` is sorted worst first (severity, then check id, then code); `passed` is sorted by check id. Both are always arrays, never `null`. `ran_at` is UTC to the second. `summary.ok` is the number of checks that **passed** (the length of `passed`: a check passes when it returns no problem result); the other counters count problem results by severity. `details` is omitted when empty.
- `?area=` is repeatable and also accepts a comma-separated list; it is case-insensitive and matches the **check's** area, which need not be the area of every code it returns. `?fast=1` (also `true`) runs only checks with `fast: true` and bounds the **whole run** at 5 s: a fast check still running then is abandoned like one that timed out. An area that is not one of the area prefixes of [doctor-codes.md](doctor-codes.md) §1.3, or a `fast` value that is not `0`/`1`/`true`/`false`, is `400 LAI-AGT-020` with `details.param` (`area` or `fast`).
- `title` and `fix` follow `Accept-Language` or `ui.language` like error envelopes (§3.7); a result whose check supplied its own text keeps it untranslated. Codes, check ids and field names never change. The response is `Cache-Control: no-store`.
- The route is a `GET`, so it answers while the agent is starting (§3.7). A Portal session is refused (auth `op`).
- Registration: `api.RegisterDoctor(reg, api.DoctorOptions{Registry: doctor.DefaultRegistry})` (AGENT-20 wires it, like `api.RegisterEvents`). The body type is `doctor.Report`, aliased as `api.DoctorResponse`.

#### 3.8.28 Drill
`POST /drill` body `{"quick": false, "only": [], "trigger": "manual"}` ([drill.md](drill.md) §3.1; `trigger: scheduled` is what `localai-drill.service` sends) → `202 {"run_id": "drl-…"}`; `409 LAI-DRL-002` if a drill or an ark run is active, `409 LAI-DRL-003` in survival, `409 LAI-DRL-004` when llama is busy, `409 LAI-DRL-005` on a low battery; a **scheduled** run a precondition holds back is `202 {"run_id", "state": "skipped", "code", "reason", "retry_at"?}` instead of a 409 ([drill.md](drill.md) §3.1, §4.1); `400 LAI-AGT-020` for an unknown `trigger` or an `only` entry that matches no check. The body is optional. `GET /drill` → the last results document ([drill.md](drill.md) §3.4); `GET /drill?run_id=<id>` → that run (`404 LAI-AGT-021` if it is no longer kept). `DELETE /drill` aborts the running drill → `202 {"run_id"}` (the agent restores state, [drill.md](drill.md) §4.3); `404 LAI-AGT-021` when no drill runs. `POST /drill` and `DELETE /drill` are audited as `drill.start` and `drill.abort` (`target.trigger`, `target.quick`, `target.run_id`). Registration: `api.RegisterDrill(reg, api.DrillDeps{Service: drill.NewService(engine)})` (AGENT-20 wires it; the engine is `drill.New`).
`POST /drill/manual` (admin; an expansion of the brief's table, requested by [drill.md](drill.md) §4.6 step 17 and §11) body `{"result": "pass|fail", "notes": "≤ 2000 chars", "at": "<RFC 3339>?"}` → `201 {"run_id": "drl-<time>-<4hex>-manual"}`: stores a results document with `trigger: manual_plug` and no `checks` in `/var/lib/localai/drill/`. `400 LAI-AGT-020` (`details.field`, `details.reason`) for a `result` that is not `pass` or `fail`, `notes` over 2000 characters or with control characters, and an `at` that is not RFC 3339, is more than 5 minutes ahead of the clock or is over a year back; `403 LAI-SEC-002` for an operator without the PIN grant; `415` without `Content-Type: application/json`; `503 LAI-AGT-024` while the agent stops. Audit `drill.record_manual` (`target.result`, `target.run_id`; never the notes). Details, the document and the reporting rules (alerts, the snapshot block, the `drill.done` notice) are owned by [drill.md](drill.md) §3.1 and §4.5. Registration: the same `api.RegisterDrill` as above (`api.DrillService` has a fourth method, `Manual`, which `drill.NewService(engine)` implements with `Engine.RecordManual`). AGENT-20 also wires the drill's reporting: `drill.NewReporter(drill.ReporterOptions{Results, State, Clock, OnChange})` and `Load()` once at startup, passed to the engine as `Deps.Observer`, to the cloud snapshot builder as `Drill`, and to `drill.RegisterAlertChecks(doctor.DefaultRegistry, reporter, nil)`; the engine's fence is `drill.PausingFence{Inner: fence, Link: <cloud link Pause/Resume>}`.

#### 3.8.29 `GET /docs/index`
```json
200 {"root": "/srv/localai/docs", "rendered_at": "…", "docs": [
  {"slug": "01-survival-mode", "title": "Survival mode", "title_es": "Modo de supervivencia", "kind": "runbook", "html": "/docs/01-survival-mode.html", "pdf": "/docs/01-survival-mode.pdf"}]}
```
Source: the first content root with `docs/index.json` (written by `aictl docs render`, [aictl.md](aictl.md) §4.22), internal root first. Empty list if none. `root` and `rendered_at` are `null` when no root has an index; `title_es` and `pdf` are `null` (or omitted) for a document without one. `kind` is one of `card` (the node's survival card, always first), `runbook`, `spec`, `architecture`, `decision`, `other`; the Portal's Manuals list is meant to show `card` and `runbook` only. The agent's `DocEntry` (`internal/api/docs.go`) does not forward `kind` yet: add it when the Portal needs the filter; until then the unknown key is ignored. An `index.json` that cannot be read or parsed (or is over 4 MiB) gives `"docs": []` with `root` set and a warning in the log, never an error. Entries without a `slug`, or whose `html` is not a `/docs/...` link with no `..`, are dropped, and a `pdf` outside `/docs/` is blanked, so a hand-edited index cannot make the Portal link outside the mount. The roots are read on every request, so a drive that is mounted or ejected is noticed without a restart.

#### 3.8.30 `GET /profile`, `PUT /profile` (admin)
`GET` →
```json
200 {"profile": "small-8gb", "display_name": "Small GPU laptop (8 GB VRAM)", "overrides": { "...": "node.yaml profile_overrides" },
     "suggested": "small-8gb", "match_reasons": ["backend cuda", "vram 8192 MiB in 7000..11999"],
     "effective": {"models": ["qwen3.5-9b-q4km", "…"], "content": ["wikipedia-en-nopic", "…"], "planned_bytes": 180000000000, "budget_bytes": 220000000000}}
```
`effective.models` and `effective.content` are what the plan keeps after the budget (reverse-priority drops, [catalog.md](catalog.md) §4 "Planning rule"); they are never filtered by hardware fit. The body also carries `effective.skipped`: `[{"kind": "content", "id": "…", "reason": "budget", "size_bytes": 0}]`, empty when nothing was dropped. Built by `catalog.Catalog.BuildProfileView`; for an unknown profile in `node.yaml` the route answers `LAI-CAT-003` and may still show `suggested` and `match_reasons`.
`PUT` body `{"profile": "cuda-12gb", "profile_overrides": null}` (either field optional; `null` overrides = unchanged). Validates (`422 LAI-CAT-003/004`), writes `node.yaml`, regenerates `models.ini` + router reload, recomputes plan. Does not start a sync. → `200 { …same as GET… }`. Audit `profile.set`.
AGENT-14 details: `profile` and `profile_overrides` may be absent or `null` (both unchanged; a body of `{}` changes nothing and writes nothing); `profile_overrides` as an object replaces the whole block (the five keys, with empty lists and `budget_bytes: null` for what it leaves out). An empty `profile` or a `budget_bytes` that is not positive is `400 LAI-AGT-020` (`details.field`). The request is validated against the installed catalog with `Catalog.BuildProfileView` **before** anything is written: an unknown profile is `422 LAI-CAT-003` and an unknown id `422 LAI-CAT-004` (`details.profile`, `details.unknown[]` of `field` and `id`, and the `suggested` profile with its `match_reasons`); `GET /profile` answers the same `422` for a `node.yaml` that names a profile or id the catalog lacks, so a broken node can still be told what fits. The write goes through `config.Store.Update` key by key (`profile`, `profile_overrides.*`: comments and key order stay, the previous file is `node.yaml.bak`) and is skipped when nothing changes. `suggested` is `null` when no profile fits. `effective.skipped` is always present (an empty list when the budget dropped nothing). The router reload (below) runs after the write; its problems are `warnings[]` in the answer (`code`, `message`), because `node.yaml` is already written and the next regeneration retries. The route is `admin`: an operator without the PIN grant gets `403 LAI-SEC-002`. Audit `profile.set`: `ok`, or `fail` with the code for a `422`; target `profile`, `previous`.

#### 3.8.31 `internal/statusapi` and `internal/portal` seams (AGENT-14)
The routes of §3.8.4, §3.8.21 and §3.8.30 are built by `statusapi.Routes(Deps)` / `statusapi.Register(reg, Deps)` and `GET /portal/status` by `portal.Route(RouteOptions)` / `portal.Register`; the daemon wiring (AGENT-20) adds them to the server's registry, nothing registers at init time. Every dependency is a small interface with a fake in the tests; all are optional except `Config` (`config.Store`), `Catalog` (`statusapi.CatalogStore`) and `Refresher` (`*catalog.Fetcher`).

| View | Implemented by | Empty state |
|---|---|---|
| `HardwareView.Snapshot()` | `hardware.Sampler` | zero snapshot |
| `ModeView.Info()` | `*modes.Engine` | `state.json` mode, else `offline` |
| `ServiceView.Snapshot(ctx)` | `*services.Watcher` | `[]` |
| `ModelView.Models(lang)` | `*statusapi.ModelMerger` (catalog, presence per root, static fit, router view) | no rows |
| `ArkView.Ark()`, `ContentView.ContentState(id)` | the ark slice (SYNC) | no roots, idle; every item `absent` |
| `CloudView.Cloud()` | the cloud link (AGENT-17) | not enrolled |
| `TailscaleView.Status()` | `*modes.TailscaleManager` | `node.yaml` values, not applied |
| `AlertView.Active()` | `*doctor.Manager` | `[]` |
| `CounterView.Counters()` | the counters of §7 | `{}` |

- **`CatalogStore`** holds the catalog the agent runs with, its source and `fetched_at`. Startup fills it from `catalog.Fetcher.Startup` (`statusapi.InstalledAt(loaded.Dir)` gives `fetched_at`); a successful `POST /catalog/refresh` replaces it; the router, the sync planner and the routes read it, so they see one catalog. The `CATALOG_VERSION` mtime watch of §3.3 (`aictl catalog use --test`, `catalog restore`) calls `Set` after `catalog.Load`.
- **`StatusBuilder.Build(ctx, forCloud, BuildOptions{Lang, Counters})`** is the body of §3.8.4; `Deps.Builder()` returns it for the cloud link (AGENT-17), which pushes `Build(ctx, true, …)`.
- **`ModelMerger`** is the merge of §3.8.7 (`loadable` with the `LAI-CAT-005`/`LAI-LLM-008` reasons from `router.Env.Loadable`, `LAI-LLM-004` when the watcher's last poll failed, `status` from the router view, `default` from `router.EffectiveDefault`, `last_load_ms` from `state.models.last_load`); `GET /models` (AGENT-15) reuses it instead of merging again.
- **`Regenerator`** regenerates `models.ini` and reloads the router under the reload-safety rule (llama-router.md §4.2). `statusapi.RouterRegenerator{Generator, Inputs}` adapts `router.Generator`; `Inputs` assembles `router.Inputs` from the config store, the catalog store, the hardware sample, the state store and the mounted roots, the same function every trigger of a regeneration uses (ROUTER-05). Findings and an `ApplyErr` of the `router.Result` become warnings; an error becomes a generic warning (`LAI-AGT-099`, `LAI-AGT-022` for a deadline).
- Counters: `?counters=1` passes the producers' map through; the keys named by §7 are `requests_by_class`, `model_loads_ok`, `model_loads_failed`, `model_load_mean_ms`, `mode_transitions`, `cloud_claims`, `cloud_failures`, `sync_bytes`.

#### 3.8.32 `internal/modelsapi` (AGENT-15)
The routes of §3.8.7 (`GET`), §3.8.11 to §3.8.14 are built by `modelsapi.New(Deps)` and `(*Service).Register(reg)` (or `modelsapi.Register(reg, Deps)`); the daemon wiring (AGENT-20) adds them to the server's registry, nothing registers at init time. They are a package of their own, like `statusapi`, because they compose the catalog, the router and the sync engine (`internal/router` imports `internal/api`). Every behaviour is a method of `*Service` (`List`, `Get`, `Download`, `Delete`, `ShowKey`, `RotateKey`) returning typed results and `*api.Error` values; the HTTP handlers only decode and encode, and the cloud executors (AGENT-18) call the same methods with `audit.Cloud(jobID)`, so they get the same checks and audit lines.

| Dependency | Implemented by | Without it |
|---|---|---|
| `Config`, `Catalog`, `Models` | `config.Store`, `statusapi.CatalogStore`, `statusapi.ModelMerger` (the merge of §3.8.7, shared with `/catalog` and `/status`) | required |
| `Sync` (`SyncEngine`: `StartModelDownload`, `DeleteModel`) | the sync engine (SYNC-05 to SYNC-08); its typed errors are `RunActiveError`, `UnknownRootError`, `ErrNoWritableRoot` | required |
| `Lock` (`TryAcquire`) | `*router.OpLock`, the `modelOpsMu` shared with the orchestrator and the generator | required |
| `Keys` (`router.KeySource`) | `*router.FileKey` (told to `Invalidate` after a rotation) | required |
| `Restarter` (`RestartWith`) | `*router.Orchestrator` | required |
| `Pusher` (`Push(ctx) error`; `ErrCloudSkipped` when not linked) | the cloud link client (AGENT-17) | `cloud_synced: false` |
| `Mode`, `Tailnet`, `State`, `Regenerator`, `Publisher`, `Alerts`, `Audit` | `*modes.Engine`, `*modes.TailscaleManager`, `*state.Store`, `statusapi.RouterRegenerator`, `*events.Bus`, the alert manager, `*audit.Log` | online, no tailnet URL, no `state.key`, no regeneration, no event, no alert, no audit line |

The key is read by `ShowKey` and written by `RotateKey` and by nothing else: the logs, the audit lines, the events, `state.key` and every error carry the fingerprint (`ab12…9f3c`, `router.KeyFingerprint`: the first four and the last four characters). After a rotation the log scrubber is told the new key (`logx.SetCurrentKey`).

### 3.9 Authentication and request checks

#### 3.9.1 Middleware order (every request, TCP and socket)
1. **Request id** assigned.
2. **Host check** (TCP only): `Host` must be exactly `127.0.0.1:<ports.agent>` or `localhost:<ports.agent>`. Otherwise `403 LAI-SEC-006` (plain JSON, no redirect). Socket requests skip this check.
3. **Origin check** (TCP only): if `Origin` is present it must be `http://127.0.0.1:<port>`, `http://localhost:<port>` or `localai://app`; otherwise `403 LAI-SEC-007`. If `Origin` is absent but `Sec-Fetch-Site` is present, it must be `same-origin` or `none`. Requests with neither header (curl, aictl over TCP) pass.
4. **Route match** → required auth level from §3.8. `OPTIONS` is answered `405` here, for every path, before matching (§3.9.4); a path that is not in clean form (`//`, `.`, `..`) is `404`, never a redirect. An unmatched path is `404 LAI-AGT-021`, or `405` when the path exists under another method.
5. **Principal resolution** (first match wins):
   - socket peer: `SO_PEERCRED` → uid/gid/pid. uid 0 → `admin`. Else, if the uid's groups (`os/user.LookupId` + `GroupIds`) include `localai-operators` → `operator` (`admin*` satisfied). Else `403 LAI-SEC-013`.
   - `Authorization: Bearer lat_…`: constant-time compare against the token file (cached; re-read when its mtime changes) → `operator`.
   - cookie `localai_session`: session lookup → `operator` (sessions are only created from an operator credential).
   - cookie `lai_portal`: Portal session lookup → `portal`; allowed only on the routes and with the restrictions of [survival-portal.md](survival-portal.md) §3.3 (else `401 LAI-SVC-110`). All Portal sessions are dropped when the mode leaves survival.
   - none → `anonymous`.
6. **PIN grant** (`X-LocalAI-Pin-Grant`): if present and valid for this principal → `admin`. An invalid/expired grant on an admin route → `403 LAI-SEC-002` (`details.reason: "grant_expired"`).
7. Auth decision: anonymous on non-`none` route → `401 LAI-SEC-001`; operator on admin route → `403 LAI-SEC-002` with fix "Enter the admin PIN" (`details.pin_required: true`).
8. **CSRF rule:** for cookie-authenticated mutating requests, `Origin` (or `Sec-Fetch-Site: same-origin`) is **required**, not merely allowed. **Portal sessions are stricter:** a mutating request authenticated by `lai_portal` must carry `Origin` equal to one of the three allowed values of step 3; `Sec-Fetch-Site` alone is not enough ([survival-portal.md](survival-portal.md) §3.3). Operator sessions (`localai_session`) keep the `Origin`-or-`Sec-Fetch-Site` rule.
9. Body limits and content-type checks (§3.7).

**How `internal/auth` resolves steps 5 to 7** (§3.9.5 has the API):
- **Principal resolution runs for every request**, and the route's level is compared afterwards, so the access log and the audit record know the caller even for a refusal.
- **Socket.** On the unix socket the peer alone decides: a bearer token or cookie sent over it is ignored. A peer whose `SO_PEERCRED` could not be read is `403 LAI-SEC-013` (`details.reason: "peer_unknown"`), like a uid that is neither 0 nor a member of `localai-operators` (`details.uid`); this applies to every route, level `none` included. Membership is looked up with `os/user` (`LookupGroup` + `LookupId` + `GroupIds`, primary group included) and remembered per uid for 30 s; a lookup failure counts as "not a member" (fail closed) and is logged.
- **A credential that does not check out is skipped, not refused.** A wrong, empty or non-`lat_` bearer, an unknown session cookie and an unknown Portal cookie resolve to `anonymous`: a route at level `none` still works (a stale token in a script must not break `POST /mode/survival`), and any other route answers `401 LAI-SEC-001`. Order: bearer, then `localai_session`, then `lai_portal`; a good credential later in the order is not shadowed by a bad one earlier.
- **`LAI-SEC-012` and `LAI-SVC-110` are answers for routes that need authentication.** A cookie naming an expired session (remembered for 24 h after it expired, only to tell it from an unknown one) gets `401 LAI-SEC-012` instead of `-001` on such a route; on a `none` route the caller is anonymous. A known `lai_portal` cookie that the `PortalSessions` hook does not allow for the request, or a Portal principal on a route that does not list `portal`, gets `401 LAI-SVC-110` on such a route; on a `none` route it is anonymous. An unknown Portal cookie is anonymous (`-001`).
- **PIN grant (step 6).** Only an operator can be lifted: a valid grant makes the principal `admin` (`Via: "pin"`), keeping its credential, so step 8 still applies. A grant that is unknown, expired or earned by another principal is the same thing, "invalid", and matters only on a route the operator cannot otherwise use: `403 LAI-SEC-002` with `details.reason: "grant_expired"` (never a hint that the grant exists for someone else). On any other route an invalid grant is ignored. Without a grant, an operator on an admin route gets `403 LAI-SEC-002` with `details.pin_required: true`. A grant does not turn an anonymous caller into anything. `admin*` is satisfied by an operator on the socket, so over TCP an operator needs a grant for `/ui-code`.
- **Revocation.** Grants die with the session that earned them (`DELETE /session`), and every bearer grant dies when a re-read finds a rotated agent token. After `aictl token rotate` the old token still works until the agent re-reads the file: at most 10 s, or at once on the next failed compare (so the new token works at once).

#### 3.9.2 Tokens and secrets
| Item | Format | Generation | Storage |
|---|---|---|---|
| Agent token | `lat_` + base64url(32 random bytes) (43 chars) | agent at start if `/etc/localai/agent-token` is absent or empty (`LAI-SEC-011`), or `aictl token rotate` | file, 0640 root:localai-operators, trailing newline tolerated |
| Session id | base64url(32 bytes) | `POST /session*` | memory only (lost on agent restart; the desktop re-creates it on 401) |
| Session TTL | idle 12 h, absolute 7 d | — | — |
| UI code | 6 decimal digits (crypto/rand, uniform) | `POST /ui-code` | memory; single use; 5 min |
| PIN | 4–8 digits | `aictl pin set` | argon2id (`m=64 MiB, t=3, p=1`, 16-byte salt, 32-byte key), PHC string format in `/etc/localai/admin-pin` |
| PIN grant | `pg_` + base64url(32 bytes) | `POST /pin/verify` | memory; 5 min; bound to principal |

**PIN rate limit:** 5 failures in a sliding 5-minute window (global) → `429` until the window frees. 20 consecutive failures → 1 h lockout and alert `LAI-SEC-004` (critical). A success resets the consecutive counter. Verification always costs the argon2 time (no fast path on malformed input) to avoid timing oracles.

Precisely (AGENT-06): verifications run one at a time under a mutex (argon2 needs 64 MiB, and the limit is exact under parallel requests). While the window is full or the PIN is locked, the request is refused with `429` **before any argon2 work**, even if the PIN is right; `Retry-After` is the time until the oldest failure is 5 minutes old (window) or the lockout ends. The failure that is the 20th consecutive starts the lockout and is itself answered `429` with `Retry-After: 3600` and `details.locked: true`; when the hour is over the limiter starts afresh (empty window, counter 0). A missing or unusable PIN file (`409 LAI-SEC-005`) is not a failure. The lockout is memory only: an agent restart clears it (accepted: the limiter is about online brute force over loopback; an attacker who can restart the agent is root). The hash parameters are read from the PHC string, within bounds (`m` 8 KiB to 1 GiB, `t` 1 to 16, `p` 1 to 16, salt at least 8 bytes, key 16 to 128 bytes), so a file written by a later `aictl pin set` with other costs still verifies; `auth.HashPIN` always writes `m=65536,t=3,p=1`.

**Login-code rate limit:** the same window (5 failures in 5 minutes, global), no consecutive counter and no lockout; a success does not clear it. Anyone who can reach the port can therefore keep the code login closed by sending 5 wrong codes every 5 minutes; the bearer token, the socket and the PIN are unaffected, and the Portal needs none of them. This is accepted: the alternative is a per-client limit that a loopback attacker does not respect.

#### 3.9.3 Permission matrix (summary)
`none`: `/health`, `/portal/status`, `/mode/survival`, `/session/code`, the Portal page and assets, `/chat/` (survival mode only, §3.11.1), `/library/`, `/maps/` (including `/maps/catalog.json`, `/maps/tiles/*`, `/maps/assets/*`, `/maps/search`, `/maps/nearest` and `GET/PUT/DELETE /maps/whereami`), `/docs/`. `portal` (in addition to `none`): `/chat/*`, `/speech/stt|tts|voices`, `POST /models/{id}/load` (restricted), `GET /events` (restricted types) — [survival-portal.md](survival-portal.md) §3.3. `admin`: `DELETE /models/{id}`, `PUT /ark/drives/{root_id}/profile`, `POST /ark/prune`, `POST /drill/manual`, `/key/rotate`, `/cloud/enroll`, `DELETE /cloud`, `/cloud/resend-key`, `PUT /profile`. `admin*`: `/ui-code`, leaving survival via `POST /mode`. Everything else: `op`.

The local UI (served at `/`, `/assets/*`) is a static bundle and loads without auth; its API calls need the session.

#### 3.9.1a `internal/api` seams (AGENT-05)
The server is an `app.Worker` (`api.New(api.Options{...})`, `Name`, `Start`, `Stop`). Later issues plug into it through these seams; nothing else in the package needs to change.

- **Routes.** `api.Route{Method, Pattern, Auth []Level, Timeout, Handler, GateExempt, MaxBody, MediaTypes, Absolute}`. `Pattern` is a `net/http` pattern relative to `/api/v1` (`/models/{id}/load`, read with `r.PathValue`); `Absolute` marks the full paths of §3.11 (`/portal`, `/chat/`). `Auth` lists the accepted levels (`op, or portal` is `{AuthOp, AuthPortal}`); levels are `none`, `op`, `portal`, `admin`, `admin*`. `Timeout` is the §3.8 "Timeout" column: a deadline on the request context and on the connection, `0` for streams (SSE). `GateExempt` is set only on `POST /mode/survival`. `MaxBody` (default 1 MiB, `api.SpeechMaxBody` 25 MiB) and `MediaTypes` (default `application/json`, STT `multipart/form-data`) override the §3.7 defaults. Routes go into an `api.Registry` (`Add` validates all or none and refuses duplicates); `api.Register` adds to `api.DefaultRegistry` at init time. `api.Routes()` lists every route (the built-in `GET /health` plus the default registry, sorted by path then method); it is what AGENT-07 compares with `openapi.yaml`.
- **Handlers.** A route handler is an `http.Handler`; `api.HandlerFunc` adapts `func(w, r) error`. `api.WriteError(w, r, err)` writes the envelope (an `*api.Error` keeps its status and code; `*http.MaxBytesError` is `413`; any other error is `500 LAI-AGT-099`), `api.NewError(code, status, details)` builds one (`WithMessage` sets a message and fix of its own for a domain code), `api.WriteJSON` writes a success body, `api.SetOpID` sets `X-LocalAI-Op-Id`, `api.DecodeJSON` / `api.DecodeJSONOrEmpty` decode strictly (`details.reason` is one of `unknown_field`, `wrong_type`, `malformed_json`, `empty_body`, `trailing_data`; the first two add `details.field`). `api.InfoFrom(ctx)` gives the request id, transport, socket peer (`PeerCred{UID, GID, PID}`, `nil` on TCP and when `SO_PEERCRED` failed), language, matched route and principal. A handler must honor `r.Context()`.
- **Auth (steps 5 to 7).** `api.Authenticator.Authenticate(r, route) (Principal, error)` is the hook AGENT-06 implements (`auth.Service`, §3.9.5). It runs for every matched route, including level `none`, and returns a `Principal{Kind, Credential, Via, UID, TokenFingerprint, SessionID, PortalSessionID}` (the last four are for handlers and audit: `UID` is the socket peer's, valid when `Credential` is `socket`; `TokenFingerprint` is `lat_…last4`; `SessionID` is a hash of the cookie, never the cookie; `PortalSessionID` is the Portal store's id) or an error to answer (an `*api.Error` is written as is). The server records the returned principal on the request **before** it answers an error, so the access log shows who was refused. `api.WithRequestInfo(ctx, info)` lets authenticator and handler unit tests run without the server. The server then re-checks `Principal.Satisfies(route.Auth)` and refuses a principal that does not satisfy it (`401 LAI-SEC-001` anonymous, `401 LAI-SVC-110` Portal, `403 LAI-SEC-002` otherwise), so the server fails closed. `Credential` (`socket`, `bearer`, `session`, `portal`) tells step 8 whether the CSRF rule applies. The default is `DenyAll` (only level `none` works); `AllowAll` and `AuthFunc` are for tests.
- **Gate and flag.** `Server.SetMutationsOpen(true)` opens the mutating-route gate (AGENT-20, end of startup step 12); `Server.SetStarting(false)` clears the `/health` flag (step 17). Both start closed and set. `Stop` closes the gate and closes the channel returned by `Stopping()`.
- **Step 8 on the socket.** The CSRF rule depends on the credential, not on the transport: a cookie credential on the socket still needs a valid `Origin`. A bearer token or a socket peer never does.
- **Listeners.** `Start` returns `nil` when at least one listener is up and a `*api.ListenError` when both failed (fatal, §4.1 step 10); `BindErrors()` returns the two `*api.BindError` (`Code` `LAI-AGT-006` or `LAI-AGT-005`) for the alerts. The Host and Origin checks follow the port actually bound, which equals `ports.agent` in production.

#### 3.9.4 CORS
The agent never sends `Access-Control-Allow-*` headers and answers `OPTIONS` with `405`. Cross-origin browser calls therefore fail closed. ai-desktop loads the UI from `http://127.0.0.1:7070` itself (same origin) and only uses `localai://app` for its own shell pages ([desktop.md](desktop.md)).

#### 3.9.5 `internal/auth` API (AGENT-06)
- **Wiring** (AGENT-20): `svc := auth.New(auth.Config{...})`, `svc.EnsureToken()` at start (creates the token file when absent or empty and returns `created = true`: raise the `LAI-SEC-011` alert; the file is 0640, owned by root and the `localai-operators` group when it exists), `auth.Register(reg, svc)` for the routes of §3.8.2 and §3.8.3 (`svc.Routes()` lists them), `api.Options{Auth: svc}`. `Config` carries the paths (defaults `/etc/localai/agent-token`, `/etc/localai/admin-pin`), the `config.FS`, a clock, the group lookup (`GroupLookup`, default `os/user`), the Portal hook, the `Auditor` (`*audit.Log` satisfies it), an `AlertFunc` for the PIN lockout and a logger (`component=auth`). Tests override the file policies (`TokenPolicy`, `PINPolicy`) so an unprivileged user can run them.
- **Shared helpers**: `auth.GenerateToken()` (`lat_` + base64url of 32 bytes, for `aictl token rotate`), `auth.HashPIN(pin)` (PHC string, refuses anything but 4 to 8 digits, for `aictl pin set`), `auth.VerifyPIN(phc, pin)`, `auth.TokenFingerprint(token)`, `auth.Binding(principal)` (the key a grant is bound to: `session:<id>`, `bearer:<fingerprint>` or `uid:<n>`).
- **Stores** (all memory only, all take a `Clock`): `TokenStore` (cached, mtime checked at most every 10 s, re-read after a failed compare; a file that cannot be read or is empty keeps the cached token and logs once), `Sessions` (cookie value = 32 random bytes; the store keeps only its sha256; capped at 256 live sessions, least recently used evicted), `UICodes` (codes compared in constant time against every live code, no early exit), `Grants` (sha256 of the grant; capped at 1024), `PIN` (§3.9.2).
- **Portal hook.** `auth.PortalSessions` is `Lookup(cookie) (id, ok)`, `Allowed(r) bool` and `DropAll()`. AGENT-06 consults it at resolution step 5 (fourth bullet) and ships a fake (`auth/fake.Portal`); the store, the cookie issue in `GET /portal`, the scope allowlist, the Origin rule, the rate limits and `DropAll` on leaving survival are PORTAL-02. With no hook configured the `lai_portal` cookie is ignored.
- **Secrets never leave**: no token, cookie, code, grant, PIN or hash is logged, audited or put in an error; credentials appear as a fingerprint. Every secret comparison is `subtle.ConstantTimeCompare`. The tests scan the journal and the audit file for every secret of a full flow.

### 3.10 Server-sent events (`GET /events`)

- Content type `text/event-stream`, `Cache-Control: no-store`, `X-Accel-Buffering: no`.
- Query `?types=model.*,mode.changed` (comma-separated, may repeat; each pattern is an exact type or ends in a single `*`; `*` alone is everything). Default: all. A pattern with other characters (anything but `a-z 0-9 _ - .`, or a `*` that is not last) → `400 LAI-AGT-020`, `details.param: "types"`. `resync` and `shutdown` always pass the filter.
- Auth `op` (any operator, admin included) or `portal`. For a Portal principal the requested patterns are **intersected** with `mode.*`, `model.*`, `service.state`, `drive.*`, `alert.*`: the rest are dropped silently, never refused, so `?types=*` and no `types` yield exactly the allowed set, and a request for only forbidden types (`sync.*`) is a valid stream that carries nothing but pings. The same restriction applies to the replay. Nothing outside the set is ever sent to a Portal session.
- No handler timeout (the route has `Timeout: 0`) and no `503` while starting (a `GET`). The server's write timeout does not apply: the handler sets a 10 s deadline per write instead, so a client that stopped reading is cut off after 10 s and its goroutine ends.
- Each event:
```
id: 1042
event: model.loaded
data: {"id":1042,"type":"model.loaded","at":"2026-11-02T15:04:05Z","node":"ai-n3","op_id":"op_7k2m9q4r8t1c","data":{"model":"qwen3.5-9b-q4km","load_ms":7100}}

```
- `id` is a per-process monotonic integer starting at 1; the bus keeps the last 1000 events. `at` is UTC to the second. `op_id` is omitted when the event belongs to no operation. `node` is `node.id`.
- **Replay.** A new client (no `Last-Event-ID`) gets live events only. A client reconnecting with `Last-Event-ID: <n>` gets every buffered event after `n`, in order and without a gap or a duplicate when the replay hands over to live events, filtered like the live stream. A missing, non-numeric, negative or oversized header means "no replay"; it never fails the request.
- **`resync`** means "re-read `/status`". Its frame carries only its payload as `data` (`{"reason":"gap"}` or `{"reason":"restart"}`), not the envelope. Three cases:
  - `n` is older than the buffer (event `n+1` was evicted): first a synthetic `resync` with `reason: "gap"` and **no `id:` line** (so it does not move the client's `Last-Event-ID`), then everything the buffer holds.
  - `n` is **greater** than the newest id (the client saw ids from a previous agent process): a synthetic `resync` with `reason: "restart"` (no `id:` line), then live events.
  - After an agent restart ids restart at 1 and the agent publishes `resync` (`reason: "restart"`) at startup step 17 (§4.1). It is a real ring event with an id, delivered live to the clients already connected, and found in the replay of a client that reconnects with an older id. A client from a previous process whose `Last-Event-ID` is *below* that event's id therefore sees it; one whose id is above it cannot be told apart from a current one (ids carry no process marker), which is why clients re-read `/status` after any reconnect that begins with a `resync` and after every agent restart they notice through `/health`.
- Keepalive comment `: ping` every 15 s. **Slow clients** are disconnected, never waited for: each subscriber has its own queue of at most 256 undelivered live events (the replay is not counted); the 257th closes the stream and counts in `dropped_clients`. The client reconnects with `Last-Event-ID` and gets a replay or a `resync`. Publishing never blocks, whatever the clients do, and the bus lock is never held while writing to a connection.
- On shutdown (§4.12 step 7): `event: shutdown` (`data: {}`, no `id:` line) then close. A request that arrives while the agent is stopping gets `503 LAI-AGT-024`.

| Type | `data` | Emitted by |
|---|---|---|
| `mode.changed` | `{from, to, op_id, warnings:[{code,message}]}` | modes |
| `mode.step` | `{target, step, state: started\|done\|failed, code?}` | modes |
| `model.loading` | `{model, unloading:[ids], op_id}`, plus `progress` (the router's `progress.value`, at most once per second) on the updates sent while the load runs ([llama-router.md](llama-router.md) §4.2) | router |
| `model.loaded` | `{model, load_ms}` | router |
| `model.unloaded` | `{model, reason: request\|evicted\|rollback\|crash\|preset_changed\|drive_removed}` (`preset_changed`: the INI section changed and the router dropped the model, [llama-router.md](llama-router.md) §4.2 step 8; `drive_removed`: its files were on an ark drive that was unplugged, llama-router.md §5) | router |
| `model.failed` | `{model, code, message, rolled_back, restored:[ids]}` | router |
| `model.default_changed` | `{model, previous}` | router |
| `sync.progress` | `{run_id, root, item, state, bytes_done, bytes_total, rate_bps}` (≤ 1/s per item); shape owned by [ark-sync.md](ark-sync.md) §3.6 | sync |
| `sync.item_done` | `{run_id, root, item, result: verified\|skipped\|failed\|quarantined, version, reason?, code?}` ([ark-sync.md](ark-sync.md) §3.6) | sync |
| `sync.done` | `{run_id, state, totals, ready: {<root_id>: overall}}` ([ark-sync.md](ark-sync.md) §3.6) | sync |
| `service.state` | `{name, unit, from, to, active, sub, healthy, code?}` (`from`/`to` are the previous/new health states of [companion-services.md](companion-services.md) §4.6; `code` is set when unhealthy; `unit` is `null` for piper and maps; one event per health-state transition; same shape in both specs) | services |
| `drive.mounted` / `drive.unmounted` | `{root_id, mount, state, read_only}` / `{root_id, safe_to_unplug}` ([ark-sync.md](ark-sync.md) §3.6) | sync |
| `alert.raised` / `alert.cleared` | `{code, severity, title, fix}` | doctor |
| `cloud.state` | `{state, previous, last_error}` | cloud |
| `hardware.power` | `{source, battery_percent, charging}` (on change; `battery_percent` is `null` without a battery) | hardware |
| `key.rotated` | `{fingerprint}` | router |
| `update.state` | `{state, from, to}` | update |
| `drill.started` | `{run_id, trigger, quick}` | drill ([drill.md](drill.md) §4.5) |
| `drill.check_done` | `{run_id, id, result: pass\|warn\|fail\|skip, code?}` | drill |
| `drill.done` | `{run_id, overall, survival_ready, trigger?, notice?: {en, es}}` (`notice` only for a scheduled run that completed: the desktop notification text, [drill.md](drill.md) §4.5) | drill |
| `resync`, `shutdown` | see above | bus |

#### 3.10.1 `internal/events` and the handler (AGENT-08)

- **Bus.** `events.New(events.Options{Node, Capacity, SubscriberBuffer, Now})` (defaults 1000 and 256). `Publish(type, opID string, data any) error` accepts only the struct registered for the type (`events.ModelLoaded` for `events.TypeModelLoaded`, a value or a pointer, `events.Types()` lists them): an unknown type, a wrong or free-form payload, or `resync`/`shutdown` is refused with an error and counted in `rejected`, so a publisher cannot invent a shape. Callers log the error and carry on. `Resync(reason)` publishes the ring `resync` (`events.ResyncRestart` at step 17). `Subscribe(ctx, filter, lastID) (<-chan Event, error)` implements the replay rules above; the channel closes when `ctx` ends, when the subscriber is dropped for being slow, or on `Close`. `Close()` ends every subscription and makes `Subscribe` fail with `ErrClosed`; `Publish` stays harmless afterwards. `Counters()` → `{published, dropped_clients, rejected, subscribers}` for `/status?counters=1`. `ParseFilter`, `NewFilter`, `Filter.Match`, `Filter.Restrict` and `PortalTypes()` implement the `types` rules.
- **Wiring (AGENT-20).** The routes are registered before the `Server` exists, so the handler takes the stop channel as a function: `api.RegisterEvents(reg, api.EventsOptions{Bus: bus, Stopping: func() <-chan struct{} { return srv.Stopping() }})`, then `api.New(api.Options{Registry: reg, ...})`. At step 9 the agent creates the bus; at step 17 it calls `bus.Resync(events.ResyncRestart)`; at shutdown the server stops first (every stream sends `shutdown` and returns), then `bus.Close()`.
- **Secrets.** Event payloads are the typed structs of `internal/events/types.go`; a test publishes one example per type and asserts `logx.ScrubString` leaves the JSON unchanged. A new event type needs a struct, an entry in the registry and an example in that test.

### 3.11 Non-API paths and reverse proxies

| Path | Auth | Target | Details |
|---|---|---|---|
| `/` , `/assets/*` | none (data needs session) | embedded `ui/dist` | SPA; unknown non-API paths fall back to `index.html`. In survival mode `/` answers `302 /portal` |
| `/portal`, `/portal/*` | none | embedded `ui/dist` (portal entry); in survival mode `GET /portal` also sets the Portal session cookie `lai_portal` | [survival-portal.md](survival-portal.md) §3.1, §3.3 |
| `/chat/` | session or Portal session, **or** none while mode = survival | `http://127.0.0.1:<ports.llama>/` | key injection, §3.11.1 |
| `/library/` | none | `http://127.0.0.1:<ports.kiwix>/library/` | §3.11.2 |
| `/maps/` | none | embedded MapLibre viewer + local tiles | §3.11.2 |
| `/maps/catalog.json`, `/maps/tiles/<content-id>.pmtiles`, `/maps/assets/<path>` | none | best copy on the content roots (Range) | [companion-services.md](companion-services.md) §3.5 |
| `/maps/search`, `/maps/nearest` | none | in-memory place index | [companion-services.md](companion-services.md) §4.4.4 |
| `GET` / `PUT` / `DELETE /maps/whereami` | none (`PUT`/`DELETE` need a same-origin `Origin`) | GPS / manual position | [companion-services.md](companion-services.md) §4.4.5 |
| `/docs/` | none | files under `<root>/docs/` | §3.11.3 |

All proxies: `httputil.ReverseProxy` with a `Rewrite` (not a `Director`: `Rewrite` strips the client's forwarding headers before the mount's code runs) that pins scheme/host to the loopback target, strips hop-by-hop headers, `X-Forwarded-*` and the client's credentials (`Authorization`, `X-Api-Key`, `Cookie`; §3.11.4), and flushes immediately (`FlushInterval: -1`, needed for streaming tokens). Timeouts, forwarded headers, body limit and the upstream-down status are **per-mount options**; a mount uses the defaults (600 s response timeout, no `X-Forwarded-*` added, 1 MiB body, upstream down → `502`) unless its spec says otherwise:

| Mount | Timeout | Adds `X-Forwarded-Prefix` | Body limit | Upstream down |
|---|---|---|---|---|
| `/chat/` | no total cap for streams; idle timeout 10 min ([survival-portal.md](survival-portal.md) §4.4) | yes (`/chat`) | 32 MiB (§3.7) | `502` page, `LAI-SVC-102` |
| `/library/` | 600 s | no ([companion-services.md](companion-services.md) §3.5) | 1 MiB (only `GET`/`HEAD`) | `503` page, `LAI-SVC-002` |

Upstream down → the status above (`502` unless the mount's spec says otherwise) with an HTML page (for browsers) that shows the doctor code and fix in the UI language.

#### 3.11.1 `/chat/` (llama WebUI with key injection)
**Specified in [survival-portal.md](survival-portal.md) §4.4 (authoritative)**; implemented in `internal/portal`. Summary of the contract the rest of the agent relies on:
- Enabled when `mode == survival`, or with a full session, or with a Portal session; otherwise `403` with a bilingual page (`LAI-SVC-101`). In survival mode anonymous access needs **no** cookie at all (auth `none`, §3.9.3): the Portal cookie only matters outside survival, where `/chat/*` needs an operator session or a Portal session.
- Strips `/chat`, removes client `Authorization`/`X-Api-Key`/`Cookie`/`X-Forwarded-*`, injects `Authorization: Bearer <llama key>` (held in memory, re-read on rotate), sets `Host: 127.0.0.1:8080`.
- Works under a prefix because the llama WebUI (`tools/ui` at `f3586e66b`) uses relative paths (`paths.relative: true`).
- `POST /models/load` and `POST /models/unload` from the WebUI are **intercepted** and executed through the agent's own orchestrator ([llama-router.md](llama-router.md) §4.4), so the WebUI's model picker obeys the fit rules and the model lock. Router downloads/deletes, `/slots` writes, `/cors-proxy*` and `/tools*` are refused (`LAI-SVC-104`).
- Streaming is flushed immediately; upstream down → `502` page (`LAI-SVC-102`).

#### 3.11.2 `/maps/` and `/library/`
Routes, Range serving of `.pmtiles`, assets, search and `whereami` are specified in [companion-services.md](companion-services.md) §3.5 (authoritative); `/library/` proxies to kiwix-serve, which runs with `--urlRootLocation=/library` so paths pass through unchanged (upstream down → `503` page, `LAI-SVC-002`). The agent-wide rules still apply: only `GET`/`HEAD` (plus the same-origin `PUT/DELETE /maps/whereami`), path cleaning with `filepath.Clean` + root prefix check + `EvalSymlinks` re-check, and `http.ServeContent` for Range (`206`, `Accept-Ranges: bytes`).

#### 3.11.3 `/docs/`
Static files from the first content root with `docs/index.json`; `index.html` for `/docs/` (and for a folder: `/docs/sub/` and `/docs/sub` serve `sub/index.html`); same path-safety rules as maps; `Content-Security-Policy: default-src 'self'; img-src 'self' data:` on HTML (the other global HTML headers stay; other files get `X-Content-Type-Options: nosniff` only). That CSP has no `style-src`, so **inline `<style>` is blocked**: `aictl docs render` ([aictl.md](aictl.md) §4.22) must write the printable CSS as a file next to the pages and link it, not embed it in each page. A request for something that is not there answers `404` `LAI-AGT-021` as the bilingual page for a browser and the envelope otherwise; with no rendered docs at all `details.reason` is `docs_not_rendered`.

**Global headers** on every HTML response the agent serves itself: `Content-Security-Policy: default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: SAMEORIGIN`. (Proxied upstream HTML keeps upstream CSP, adding `X-Content-Type-Options`.) **Exception:** `/portal`, `/portal/*` and `/maps/` (the viewer page and its assets) do **not** use this global CSP; they send the header defined in [survival-portal.md](survival-portal.md) §4.11 (authoritative for both; [companion-services.md](companion-services.md) §4.4.3 references it).

#### 3.11.4 The shared plumbing (AGENT-16)
Packages: `internal/api/proxy` (the helper), `internal/api/static` (path safety and file serving; imports nothing of the agent), and in `internal/api` the SPA, `/docs/` and error-page code. None of it is wired into `internal/app`: a mount builds its handler and adds its routes with `api.Register` (AGENT-20 wires them).

**Mounting a proxy.** `proxy.New(proxy.Options{...}) http.Handler` is registered as one `api.Route` per method with `Absolute: true`, level `none` (the mount's own rule, such as the chat enabling rule, runs inside its handler), `Timeout: 0` (the proxy owns the deadlines; a route deadline would cut a stream) and `MaxBody` at least the mount's `BodyLimit` (the route's limit applies first: chat needs 32 MiB on the route as well). `/library/` registers `GET` only. A mutating method still needs a JSON content type and, for cookie credentials, the CSRF rule of §3.9.1 steps 8 and 9.

| `Options` field | Meaning |
|---|---|
| `Target` | `http://127.0.0.1:<port>`; `New` **panics** on any other host (LAN, DNS, `localhost`, IPv6, `0.0.0.0`), scheme, user info or query. The connection is dialled to that address whatever the request says, and `HTTP_PROXY` is never read |
| `StripPrefix` | `"/chat"`: `/chat/props` becomes `/props`, `/chat` becomes `/`. Empty keeps the path (`/library/`). The request must be under the prefix on a segment boundary, in decoded and escaped form (`/chat%2Fprops` is not under `/chat/`), else `404`; a `..` segment or NUL is `404` and never forwarded. Query and escaping are preserved |
| `SetForwardedPrefix` | adds `X-Forwarded-Prefix: <StripPrefix>` |
| `StripRequestHeaders`, `InjectHeaders` | extra headers to drop; a function run **last** on the upstream request (chat sets `Authorization: Bearer <key>`), so a client cannot remove or replace what it sets, not even by naming it in `Connection` |
| `BodyLimit` | default 1 MiB; over it (declared or streamed) is `413` `LAI-AGT-025` and the upstream never gets the whole body |
| `ResponseTimeout` | default 600 s for the whole exchange; negative means no cap (streams). `IdleTimeout` (0: none) is the longest the exchange may go with no byte moving either way, before and during the response (chat: 10 min). Both are disarmed once a WebSocket is upgraded |
| `MethodPolicy`, `PathPolicy` | `MethodPolicy` returns an `*api.Error` to refuse (`proxy.ReadOnly`: `405` with `Allow: GET, HEAD`); `PathPolicy` returns `Allow`, `Deny` (`403` with the given code) or `Intercept` (a handler serves the **original** request instead; the policy sees a copy whose path is the upstream's). Method policy runs first |
| `DropSetCookie`, `FrameHeaders` | drop upstream `Set-Cookie`; add `X-Frame-Options: SAMEORIGIN` and `Referrer-Policy: no-referrer`. `X-Content-Type-Options: nosniff` is always added and the upstream's CSP is always kept |
| `DownStatus`, `DownCode` | `502` (default) or `503`; the doctor code of the page (default `LAI-LLM-004`; chat `LAI-SVC-102`, library `LAI-SVC-002`) |

Always, for every mount: hop-by-hop headers (including those named in `Connection`), `Forwarded`, `X-Forwarded-*`, `X-Real-Ip`, `Via` and the client's `Authorization`, `Proxy-Authorization`, `X-Api-Key` and `Cookie` are removed, so the agent's own session cookies and an operator's token never reach llama, kiwix or anything behind them (the mount adds the credential it needs through `InjectHeaders`); `Host` is the upstream's; a `Location` that points at the upstream or at a path of it is rewritten to stay inside the mount; compression passes through untouched. A WebSocket upgrade (`Upgrade: websocket` only) is tunnelled. The proxy never logs a header, a header value, the query string or a body; its one log line per failure carries `code`, `reason`, `req_id`, `method`, `path`.

**Failures** (before any response byte): upstream down, refused or closed → `DownStatus` with `DownCode`, `details.reason: upstream_down`; total or idle timeout → `504` with the same code, `details.reason: upstream_timeout` or `upstream_idle`; client gone → nothing written. A timeout after the response started cuts the connection, so the client sees the stream break rather than a clean end. The answer is the bilingual HTML page for a browser (`Accept` lists `text/html`) and the JSON envelope for everything else (§3.7); the page carries the global HTML headers, loads nothing (inline CSS, no font, no script, no image) and names neither the upstream address nor any key.

**`internal/api/static`.** `SafeJoin(root, rel)` takes the path below the mount, decoded and relative, and returns the real path or an error. Refused: an absolute path, NUL or any control character, a backslash, `%2e` `%2f` `%5c` `%00` in any case, `.` and `..` segments, empty segments (`a//b`), any segment starting with `.` (so `.git`, `.env`), a result outside the root after `filepath.Clean`, and a result outside the **real** root after `EvalSymlinks` (a symbolic link is followed only if its target is inside the root; the root itself may be a link; a link to a dotfile is refused; a loop is not found). Every refusal is the same `404` `LAI-AGT-021` as a missing file (`api.StaticError`). `ServeFile` answers `GET` and `HEAD` only (else `405`), refuses anything that is not a regular file, supports one byte range (`206`), refuses several ranges and an unsatisfiable one with `416` and `Content-Range: bytes */<size>` (never a multipart answer), and sets `Accept-Ranges: bytes`, `X-Content-Type-Options: nosniff`, a weak `ETag`, `Last-Modified` and `Cache-Control: no-cache` (unless the caller set one). Content types come from a built-in table (no system MIME database needed). Files are opened per request and closed when the response ends. The window between `SafeJoin` and the open (a link swapped in between) is accepted: the content roots are writable by root and the agent only.

**SPA routes.** `api.SPARoutes(SPAOptions{FS, Mode, IndexFile, PortalFile, PortalHook})`: `GET /` (and every GET path nothing else claims, except under `/api/`) is the Dashboard entry with the global HTML headers; a file at the top of the dist is served as itself; in survival mode `GET /` is `302 /portal`. `GET /assets/*` and `GET /portal/assets/*` read `assets/` of the dist, are cached for a year (`immutable`) and answer `404`, never the index, for a missing name. `GET /portal` and `/portal/*` serve the Portal entry with the strict CSP of [survival-portal.md](survival-portal.md) §4.11 (`api.PortalContentSecurityPolicy`); `PortalHook` runs on `GET /portal` only (PORTAL-02 issues `lai_portal` there; nil is a no-op). The dist layout the helper expects (PORTAL-04 may change the names through the options): `index.html`, `portal.html`, `assets/`. A missing entry is a bad build: `503` with a built-in page (`LAI-SVC-100`) whose buttons go to `/chat/`, `/library/`, `/maps/` and `/docs/`. Under `/api/` an unknown path stays `404 LAI-AGT-021` and a known path with another method stays `405`: the server never lets the `/` catch-all answer there and ignores it when it lists `Allow`.

**Docs routes.** `api.DocsRoutes(DocsOptions{Roots, Logger})`: `GET /docs/` (level `none`) and `GET /api/v1/docs/index` (level `op`, 5 s), as §3.8.29 and §3.11.3.

## 4. Behaviour

### 4.1 Startup sequence (`aiagent serve`)

Each step has a timeout; a failing step degrades (raises an alert) instead of aborting, unless marked **fatal**. Survival must remain reachable even with broken config.

| # | Step | Timeout | On failure |
|---|---|---|---|
| 1 | Check `os.Geteuid() == 0` | — | **fatal**, exit 1, log `LAI-AGT-011` |
| 2 | Set up slog (JSON → stdout/journald, scrubber, level from env `LOCALAI_LOG_LEVEL` until config is read) | — | — |
| 3 | Load `node.yaml` (§3.4) | 1 s | missing → `LAI-AGT-001`, invalid → `LAI-AGT-002`. Continue with **safe defaults**: no cloud, `tailscale.manage: false`, ports canonical, `node.id: "unconfigured"` |
| 4 | Open state store (§3.5) | 1 s | `LAI-AGT-003` (fallback rules in §3.5). Not writable → `LAI-AGT-004`; keep state in memory |
| 5 | Ensure dirs: `/var/lib/localai/{sync,drill,cloud,update}` (localai 0750), `/run/localai` exists (systemd RuntimeDirectory) | 1 s | `LAI-AGT-004` |
| 6 | Ensure agent token (§3.9.2), check secret file modes (`api-key` 0600, `cloud-credential` 0600, `admin-pin` 0600, `agent-token` 0640) | 1 s | wrong mode → fix it and raise `LAI-SEC-009` (warning) |
| 7 | Open audit log | — | `LAI-AGT-013`; audit goes to journald only |
| 8 | Load catalog `/etc/localai/catalog/` → else `<root>/kit/catalog/` of any mounted root → else empty catalog | 5 s | `LAI-CAT-001` |
| 9 | Start event bus, hardware sampler (first sample synchronously, 5 s cap), service watcher, drive watcher | 5 s | per-subsystem alerts |
| 10 | **Start listeners**: TCP `127.0.0.1:<ports.agent>` (loopback only; any other address is refused) and the unix socket (replace a stale socket file; `chown root:localai-operators`, `chmod 0660`). The socket is bound in a private `0700` directory next to its path, given its owner and mode there, and renamed into place, so it is never reachable with the default permissions. A live socket (another process answers on it), a path that is not a socket, a missing `localai-operators` group and a failing `chown` are all `LAI-AGT-005` and remove nothing. API answers immediately; mutating routes answer `503 LAI-AGT-024` until step 12 has completed, except `POST /mode/survival`, which is queued: persisted as `state.json: pending_mode` and answered `202 {"op_id","target":"survival","queued":true}` (§3.8.16) | 2 s | TCP bind fails → `LAI-AGT-006`, socket fails → `LAI-AGT-005`. If **both** fail: **fatal** (exit 1, systemd restarts) |
| 11 | `sd_notify("READY=1")` and start the watchdog pinger | — | — |
| 12 | **Reconcile llama** ([llama-router.md](llama-router.md) §4.6): regenerate `models.ini`/unit/drop-in if inputs changed; if the unit file changed → `daemon-reload`; if INI changed and router up → `GET /models?reload=1`; if router down and unit enabled → start it. **When step 12 ends (success or failure), the mutating-route gate opens** and a queued `pending_mode: "survival"` starts (it preempts step 13's normal policy) | 30 s | `LAI-LLM-*` alerts |
| 13 | **Resume or apply mode** (§4.2.4): if `state.mode_transition != null` → resume it (`LAI-AGT-007`); else apply the policy of `state.mode` (online → start Tailscale reconcile, link, prober; offline/survival → make sure our serve listeners are off, link stays stopped) | background | per step |
| 14 | **Restore models** (after the router is healthy, up to 120 s wait): if mode = survival and no LLM is loaded → apply the survival model set; else ensure `models.default` (or profile default) and the rest of `models.desired_loaded` are loaded (bounded by `models_max`). The router may already have loaded the default by itself (`load-on-startup`), which counts | background | `LAI-LLM-013` |
| 15 | **Pending self-update?** If `/var/lib/localai/update/pending.json` exists → run the post-update health check (§4.10 step 9) | 60 s | rollback |
| 16 | Start the doctor periodic checks, connectivity prober (online only), sync timer hook, drill hook | — | — |
| 17 | Clear `starting` flag (`/health` `starting: false`, §3.8.1); emit `resync` to SSE clients; log `msg=agent.started` with version, mode, node id, duration | — | — |

**Restart-loop detection (`LAI-AGT-014`).** Right after step 4 the agent appends the current time to `state.json: agent.recent_starts` (keeps the newest 10). If the list then holds more than 4 starts within the last 60 minutes (i.e. more than 3 restarts in the last hour), it raises `LAI-AGT-014` (warning, `details.starts_last_hour`). The alert clears when a start finds ≤ 4 entries in the last hour. No systemd query is needed, so this works the same when systemd's own `StartLimitBurst` is not reached.

Steps 12–16 run concurrently after step 11, except that 14 waits for 12, and 13 must have finished its Tailscale-off step before the link can start.

### 4.2 Mode engine

The state machine is defined in [modes.md](../architecture/modes.md) and is authoritative for states, triggers and policies; this section specifies the implementation and is **authoritative for the step names and step lists** (§4.2.1; modes.md links here).

#### 4.2.1 Steps
Each transition is an ordered list of **named, idempotent steps**. Each step function has the signature `func(ctx, *Transition) (warnings []Warning, err error)` and must be safe to re-run from scratch.

| Step name | Used by targets | Action | Per-step timeout | Failure behaviour |
|---|---|---|---|---|
| `link_stop` | offline, survival | Stop cloud link (§4.8.8), including final deregister (3 s, errors ignored) | 15 s | warn `LAI-AGT-008`, continue |
| `sync_cancel` | offline, survival | Cancel running sync gracefully; `.part` files kept | 30 s | warn, continue |
| `prober_stop` | offline, survival | Stop connectivity prober; set `connectivity: not_checked` | 1 s | — |
| `tailscale_off` | offline, survival | Remove **our** serve listeners (§4.7.3) | 20 s | if `tailscaled` is not running, nothing is exposed: ok. Otherwise warn `LAI-NET-004`, continue, and retry every 60 s in the background until success while the mode is offline/survival |
| `survival_models` | survival | Load profile `survival.models` in order, as many as `models_max` allows; slot 1 = first that loads; each with load timeout 300 s; a failure falls back to the next (`LAI-LLM-020`); none loads → `LAI-LLM-021` (critical) | 300 s × n | warn, continue |
| `survival_services` | survival | `start` kiwix and whisper (and any in `survival.require_services`) | 60 s | warn `LAI-SVC-*`, continue |
| `tailscale_on` | online | Apply the configured pattern (§4.7.2) | 30 s | `LAI-NET-003` (not logged in) / `LAI-NET-004`; continue |
| `link_start` | online | Start cloud link if the credential exists | 1 s | — |
| `prober_start` | online | Start connectivity prober | 1 s | — |
| `persist` | all | `state.mode = target`, `mode_changed_at = now`, `mode_transition = null` | 1 s | if the state store is not writable, keep in memory and raise `LAI-AGT-004` |
| `emit` | all | Publish `mode.changed`; audit `mode.change` | — | — |
| `sync_catchup` | online | If `sync.last_success_at` older than 24 h, schedule a sync in 5 min (cancellable) | — | — |

Step lists:
- → `offline`: `link_stop, sync_cancel, prober_stop, tailscale_off, persist, emit`
- → `survival`: `link_stop, sync_cancel, prober_stop, tailscale_off, survival_models, survival_services, persist, emit`
- → `online`: `tailscale_on, link_start, prober_start, persist, emit, sync_catchup`

Survival entry never fails as a whole: every step error becomes a warning in `mode_transition.warnings` and in the `mode.changed` event, and the Portal shows them.

#### 4.2.2 Recording
Before running step *k*, the engine writes `mode_transition.step = <name of k>` and increments `attempts[<name>]` (fsync'd), together with every warning collected so far. `persist` is recorded like any step and then clears `mode_transition` in the same write that sets `mode`. A step emits `mode.step` events (started/done/failed). Before the first step, accepting a request writes the transition with `step` set to the first step and no attempt counted, so a request answered `202` is already durable. The steps after `persist` (`emit`, `sync_catchup`) have no transition left to record in: they run unrecorded, are not resumed, and a crash between `persist` and `emit` loses the `mode.changed` event and the catch-up schedule, never the mode (clients re-read `/status` after the reconnect's `resync`; the nightly sync still runs). A step skipped by the 3-attempt rule is recorded too (the `step` moves on, the counter does not change, the `LAI-AGT-008` warning is written), so the skip survives another crash.

#### 4.2.3 Concurrency and preemption
- One transition at a time (`modeMu`). A second `POST /mode` while one runs answers `409 LAI-AGT-022`, **except** a request for `survival`: it sets `preempt = survival`; the running transition stops after its current step finishes (max one step), and the survival transition starts with `from = <original mode>`. Steps already done are idempotent, so nothing is lost. The accepted survival request is written as `state.pending_mode` before the request is answered (a crash before the running transition stops still runs it: §4.1 step 12 drains it), and is cleared when the survival transition's first record is written. A second survival request while one is running or pending joins it (same `op_id`, `Joined`). A request whose `wait` caller's transition was preempted gets `409 LAI-AGT-022` with `details.preempted`. Shutdown (§4.12) does not start a pending preemptor: its `pending_mode` is already on disk for the next start.
- Mode transitions and model operations share `modelOpsMu` only inside `survival_models` (it takes the lock like any load).
- Requests for the current mode answer `200 {already: true}` and do nothing (no re-apply). To re-apply, send `"reapply": true` in `POST /mode` / `POST /mode/survival` (§3.8.15; CLI `aictl mode <same> --reapply`): the engine runs the full step list of the current mode as a new transition (same `from` and `target`), with the usual recording and preemption rules.

#### 4.2.4 Resume at startup
```
state.mode_transition == nil  → apply policy of state.mode (no step replay, just the "steady-state" effects:
                                online: tailscale reconcile + link + prober; offline/survival: tailscale_off only)
state.mode_transition != nil  → log LAI-AGT-007 {target, step, attempts}
                                for step in steps(target) starting AT the recorded step:
                                    if attempts[step] >= 3: skip with warning LAI-AGT-008 ("step skipped after 3 tries")
                                    else run step
```
Rationale: a step that crashed the agent three times (for example a load that OOM-kills the box) must not cause a boot loop. Before resuming a survival transition the engine also checks `state.models.last_load`: if the previous boot died during a load of model X, X is skipped in `survival_models` (a `LAI-LLM-020` warning says so; X joins `mode_transition.skip_models`, §3.5). Details the pseudo-code leaves out: `persist` is never skipped (it is the commit); a recorded step the agent does not know, or one that comes after `persist`, restarts the transition at its first step (every step is idempotent); a recorded target that is not a mode is dropped and the policy of `state.mode` applied; a second `Resume` while something runs does nothing. The steady-state application (first branch) is a run like a transition: it holds the one-transition lock, so a request during it answers `409` and a survival request preempts it, but it writes no `mode_transition`, publishes no `mode.step` and audits nothing.

#### 4.2.5 Mode policies enforced outside transitions
- Cloud link, connectivity prober and scheduled sync check `mode == online` at every iteration (not just at start).
- The Tailscale reconciler runs only in online mode. In offline/survival it runs once at start (`tailscale_off`) and on every agent start.
- `/chat/` is anonymous only in survival (§3.11.1).
- `GET /` redirects to `/portal` in survival.

#### 4.2.6 `internal/modes` and its seams (AGENT-12)
`modes.New(Deps, Options)` returns the `Engine`. Lifecycle, driven by AGENT-20: `Open()` at the end of startup step 12 (starts accepting requests and runs a queued survival request), `Resume(ctx)` at step 13 (§4.2.4), `Shutdown(ctx)` in §4.12 step 4 (the running step may finish until `ctx` ends, then it is abandoned; the recorded step stays for the next start), `WaitIdle(ctx)` for tests and for waiting on a resume. `Request(ctx, target, RequestOptions{Wait, Reapply, Actor})` returns a `Result` (`Already`, `Queued`, `Joined`, `Done` with `Warnings`, `Preempted`, `Interrupted`) or `*BusyError`, `ErrNotAccepting`, `ErrInvalidTarget`; it never uses `ctx` for the transition itself, so a client that disconnects cannot stop one. A survival request while the engine is not accepting (before `Open`, during `Shutdown`) is recorded as `pending_mode` and answered `Queued`, under the op id `Open` later runs it with (the id is kept in memory, so a queued request that waits across a restart runs under a new id and the actor `system`/`pending_mode`).

Policy for the other subsystems: `Current()` (committed mode), `Online()` (the mode the node is *heading to* is online: false from the first step of a transition away from online, true from the first step of one to online, so a link that `link_start` starts is not told to idle until `persist`), `Subscribe()` (a `Change` per commit, buffered, oldest dropped, never blocks), `HTTP()` (`RedirectRootToPortal`, `ChatAnonymous`; true only in survival; consumed by AGENT-16), `Info()` (what `GET /mode` shows).

Seams, each optional (a nil one makes its step a no-op), each with a scriptable fake in `modes/fake`: `LinkController{Stop, Start}` (AGENT-17/18), `SyncController{CancelGraceful, ScheduleCatchup}`, `Prober{Start, Stop}` (AGENT-13; `prober_stop` also sets `connectivity: not_checked`), `TailscaleController{On, Off}` (AGENT-13; the engine serializes the two and retries `Off` in the background every 60 s while the node is not online when it returns an error or a `LAI-NET-004`/`LAI-NET-006` warning, ending the retry when it succeeds or `tailscale_on` begins), `ModelLoader{LoadSurvivalSet(ctx, skip)}` (ROUTER-05/06, takes `modelOpsMu`), `ServiceStarter` (`*services.Controller`; starts kiwix, whisper and `Deps.SurvivalServices()`), `PortalSessions{DropAll}` (called by `persist` when the mode leaves survival), plus the state store, the event bus and the audit log. Step timeouts come from `DefaultTimeouts` (overridable per engine; `survival_models` is a 15-minute backstop, the loader enforces each model's 300 s); the timeout is a timer on the engine's `Clock`, a step that ignores its context is abandoned, and a step that errors, panics or times out becomes a warning (`LAI-AGT-008`, or the step's own code: `LAI-NET-004`, `LAI-LLM-021`, the service's `LAI-SVC-*`/`LAI-LLM-001`) and the transition goes on. The routes live in `internal/api` (`RegisterMode`, `ModeDeps{Engine, Audit}`, the `ModeEngine` interface); wiring is AGENT-20. What no fake can prove is left to the owner on a node (needs-hardware): rows 6 to 9 of §9.3, plus `aictl mode online` from survival as a `localai-operators` user over the socket (no PIN) and over TCP with the bearer token (`403 LAI-SEC-002`, `pin_required`), and integration scenario 2 and 3 of §9.2, which need AGENT-20's `internal/app` harness (the engine half of scenario 3 is the kill-at-every-write test).

### 4.3 Service supervision

#### 4.3.1 Interface
```go
type Manager interface {
    State(ctx context.Context, unit string) (UnitState, error)      // ActiveState, SubState, MainPID, NRestarts, ExecMainStatus, ExecMainExitTimestamp, ActiveEnterTimestamp, UnitFileState
    Start(ctx context.Context, unit string) error                   // mode "replace", waits for job completion
    Stop(ctx context.Context, unit string) error
    Restart(ctx context.Context, unit string) error
    Enable(ctx context.Context, unit string) error
    Disable(ctx context.Context, unit string) error
    Mask(ctx context.Context, unit string) error
    Unmask(ctx context.Context, unit string) error
    DaemonReload(ctx context.Context) error
    Subscribe(ctx context.Context, units []string) (<-chan UnitState, error)
}
```
**Allowlist:** `localai-llama.service`, `localai-kiwix.service`, `localai-whisper.service`, `localai-sync.service`, `localai-sync.timer`, `localai-drill.service`, `localai-drill.timer`, `localai-agent.service` (restart only, used by self-update), `localai-agent-rollback.timer`/`.service` (stop only), `llama-qwen.service` (migration only), `tailscaled.service` (State only). Any other unit name → programming error (panic in tests, `LAI-AGT-099` in production).

The allowlist is **per unit and per verb**, enforced in one wrapper (`services.NewGuard`) that every implementation sits behind, so the D-Bus and systemctl paths cannot differ:

| Unit | Verbs allowed |
|---|---|
| `localai-llama.service`, `localai-kiwix.service`, `localai-whisper.service`, `localai-sync.service`, `localai-sync.timer`, `localai-drill.service`, `localai-drill.timer` | `State`, `Subscribe`, `Start`, `Stop`, `Restart`, `Enable`, `Disable` |
| `localai-agent.service` | `Restart` only (so not even `State`) |
| `localai-agent-rollback.timer`, `localai-agent-rollback.service` | `Stop` only |
| `llama-qwen.service` | `State`, `Start`, `Stop`, `Enable`, `Disable`, `Mask`, `Unmask` (the migration and its rollback, [llama-router.md](llama-router.md) §4.9; `enable --now` is `Enable` then `Start`) |
| `tailscaled.service` | `State` only |
| anything else (other names, empty, a name with a path or a space) | nothing |

`DaemonReload` names no unit and is always allowed. A violation is a `*services.ViolationError` whose `Code()` is `LAI-AGT-099` (the API answers it `500`); `services.PanicOnViolation()` makes the guard panic instead, which tests use so a bug cannot hide behind a swallowed error. `Subscribe` checks every unit it is given.

#### 4.3.2 Implementations
1. **D-Bus (default)** — `github.com/godbus/dbus/v5` (vendored; only `internal/services/dbus_real.go` imports it, behind the `services.Bus` seam) on the system bus, `org.freedesktop.systemd1`: `Manager.StartUnit/StopUnit/RestartUnit(name, "replace")` returns a job path; wait for `JobRemoved` with that path (result must be `done`, else a `*services.JobError` carrying the result string; the result `timeout`, or no `JobRemoved` within 60 s, also satisfies `errors.Is(err, services.ErrTimeout)`). The signal feed is opened **before** the call, so a job that finishes at once is not missed. `Manager.Reload`, `EnableUnitFiles([name], false, true)`, `DisableUnitFiles([name], false)`, `MaskUnitFiles([name], false, true)`, `UnmaskUnitFiles([name], false)`; each of these four is followed by a `Reload`, as systemd requires and as `systemctl enable` does by itself, so both backends behave the same. State via `Manager.LoadUnit` (it works for a unit that is not loaded and gives a not-found stub, inactive/dead, for one with no file; `GetUnit` fails for both) and `org.freedesktop.DBus.Properties.GetAll` on the unit object (`org.freedesktop.systemd1.Unit`, plus `.Service` for a `.service`: `MainPID`, `NRestarts`, `ExecMainStatus`, `ExecMainExitTimestamp`). Changes via `PropertiesChanged` signal matches on the unit object paths (the path is computed: `/org/freedesktop/systemd1/unit/` plus the unit name with every byte outside `[A-Za-z0-9]` written `_` and two lower-case hex digits); a signal re-reads the unit and sends the state only when it differs from the last one. The connection calls `Manager.Subscribe` once, because systemd sends its signals only to a client that did, and it is dialed with a check that systemd answers on it (a container with a bare `dbus-daemon` is "not reachable").
2. **systemctl fallback** — used when the system bus is not reachable at start (rescue shells, containers in CI). Fixed argv through `execx`: `systemctl --no-pager --no-ask-password <verb> <unit>` (`daemon-reload` has no unit); state via `systemctl show <unit> -p ActiveState,SubState,MainPID,NRestarts,ExecMainStatus,ExecMainExitTimestamp,ActiveEnterTimestamp,UnitFileState`, run with `TZ=UTC` and `LC_ALL=C` in its environment so the timestamps (`Mon 2026-11-02 08:00:05 UTC`, optionally with microseconds) parse the same everywhere; an unset timestamp or a missing property is zero. Subscribe = poll every 5 s, sending the state at once and then only changes; a failed poll keeps the last state. Timeout 60 s per call (start/stop/restart), 10 s otherwise. A non-zero exit is a `*services.CommandError` (stderr scrubbed, first 512 bytes), an `execx` timeout is `ErrTimeout`.
3. **Failover (`services.NewAuto`).** The Manager the agent runs with. At start it dials D-Bus once (bounded by the 10 s call timeout) and picks D-Bus when that works, systemctl otherwise. **The D-Bus connection is re-dialed on loss:** when a call finds the connection gone, the call is repeated through systemctl at once, and while the connection is down every call goes through systemctl; a new dial is tried at most every 30 s, on the next call. A job that failed (`*JobError`) is the call's own failure and does not cause a failover. `Subscribe` of this Manager outlives an outage: while D-Bus is down it polls through systemctl, and when the connection is back it subscribes to the signals again; the state of every unit is re-read at each switch and only changes are sent.

The chosen implementation is logged at start and at every change as the attribute `services.backend=dbus|systemctl` of the message `services backend selected` (with `reason`; losing a working D-Bus connection is a warning).

#### 4.3.3 Health beyond systemd
`healthy` in the service list means "active and answering". The probes are one HTTP `GET` on loopback that must answer exactly `200` (a redirect is not followed, nothing is cached, the connection is not reused), with the port from `ports.llama`, `ports.kiwix` and `ports.whisper` (restart-only keys, read once):

| Service | URL | Timeout | Every |
|---|---|---|---|
| `llama` | `http://127.0.0.1:<ports.llama>/health` | 2 s | 15 s |
| `kiwix` | `http://127.0.0.1:<ports.kiwix>/library/catalog/v2/entries?count=1` | 3 s | 30 s |
| `whisper` | `http://127.0.0.1:<ports.whisper>/` | 3 s | 30 s |

kiwix and whisper follow [companion-services.md](companion-services.md) §4.6, which is authoritative for them (a bare TCP connect to whisper would say "the port is open", not "it answers"). All services are also probed at once when a unit **becomes active** (a new `ActiveEnterTimestamp`, which covers a systemd auto-restart). A stopped unit is not probed.

**Health state** (`state` of `GET /services`, `from` and `to` of `service.state`, [companion-services.md](companion-services.md) §4.6; the watcher reports these four, `missing_content` and `disabled` belong to the companion services' own supervisor, `internal/svc/health`, which replaces the watcher for kiwix and whisper once wired, [companion-services.md](companion-services.md) §4.6):
- `ok`: active and the last probe passed.
- `starting`: `activating`, or active and not probed yet, or active and failing within 60 s of the unit becoming active (failures in the first minute do not count).
- `degraded`: active, past that minute, and the probe fails, for less than a minute.
- `down`: not running (`inactive`, `failed`, `deactivating`), or active and failing continuously for ≥ 60 s. In the second case the state carries the service's "not answering" code.

A unit **active but unhealthy for ≥ 60 s** raises the service's code once, with the alert manager hook (`AlertSink.Raise`, AGENT-11; `details` `{unit, unhealthy_s}`), and withdraws it (`Clear`) when the probe passes again, when the unit stops, or when it restarts: `LAI-LLM-004` for llama, and for kiwix and whisper the service's "isn't running" code from [companion-services.md](companion-services.md) §8, `LAI-SVC-002` and `LAI-SVC-011` (that table has no finer one). The minute is counted from the first failed probe (or the start of the unit, whichever is later), so a unit that never answers is `down` with its code within about 75 s of starting. A probe is dated when it started.

**The watcher** (`services.Watcher`, an `app.Worker` named `services`) subscribes to the three units once at start (`Manager.Subscribe`), runs the probes (a 15 s tick; each service is probed when its own interval has passed), keeps `restarts` (`NRestarts`) and `last_exit` (`ExecMainStatus` and `ExecMainExitTimestamp`), serves `Snapshot` to `GET /services` and `GET /status`, and publishes `service.state` ([§3.10](#310-server-sent-events-get-events); payload `events.ServiceState`) whenever the health state, `active` or `sub` changes. The first observation after start publishes nothing: `/status` carries the snapshot. `code` is set while the state is `down` because of an unanswered probe. `Publisher` is `Publish(typ, opID, data)`, so `*events.Bus` plugs in as it is.

#### 4.3.4 Writing unit files
Generated unit files (llama) are written with `WriteFileAtomic` (root 0644), then `DaemonReload`. The agent never edits unit files it did not generate, except during migration, which only *moves* `llama-qwen.service` aside (llama-router.md §4.9). `internal/services` only runs verbs; the writers are `internal/router` (llama) and `aiagent setup units` (companions), and they call `Manager.DaemonReload` after a write.

#### 4.3.5 Journal tail (`services.Journal`)
`Journal.Tail(ctx, unit, lines, since)` backs `GET /logs` (§3.8.23), the `logs.tail` cloud job (AGENT-18) and OOM detection (AGENT-10). It runs `journalctl -u <unit> -n <lines> [--since <t>] -o json --no-pager` through `execx` (5 s timeout, 4 MiB cap; `--since` and its value are two argv elements, the value written `YYYY-MM-DD HH:MM:SS UTC`) and returns the entries oldest first with `Truncated` set when the cap cut the output (the partial last line is then dropped). `unit` must be one of the six units of the `/logs` table (else a `*ViolationError`) and `lines` within 1..2000 (else `ErrTailRange`); a non-zero exit is a `*JournalError`, a timeout `ErrTimeout`.

Each output line is one JSON object. The reader takes `__REALTIME_TIMESTAMP` (microseconds, a string or a number), `PRIORITY` (`"6"`, clamped to 0..7, `6` when absent) and `MESSAGE`, and **decodes every field before the scrubber runs**: `MESSAGE` is a string, **or an array of byte values** (journald writes that for a value that is not valid UTF-8 or holds control bytes; decoded and made valid UTF-8), **or an array of those** (a field that occurs twice; joined with a newline), **or `null`** (a value over 4 KiB without `--all`; empty). `logx.ScrubString` then runs on the decoded text. It must not run on the raw line: a `Bearer` match runs to the next whitespace, and on a JSON line that swallows the closing quote and every field after it (§4.11). A line that is not a JSON object (`-- No entries --`) or has no usable timestamp is skipped.

#### 4.3.6 `internal/services` and its seams
`Manager`, `NewGuard`, `NewDBus` over a `Bus`, `NewSystemctl` over an `execx.Runner`, `NewAuto(ctx, dial, runner, opts...)` with `DialSystemBus` as the production `Dialer`, `Journal`, `Watcher` (`WatcherOptions`: `Manager`, `Prober`, `Ports`, `Publisher`, `Alerts`, `Clock`, `Only`: limits it to the named services, `["llama"]` once the companion health supervisor of `internal/svc/health` owns kiwix and whisper), `Controller` (`Do(ctx, svc, action)`: the verb plus the wait of §3.8.22; a failure is an `*ActionError` with `Reason` `job_failed`, `timeout`, `wrong_state` or `unavailable` and the unit's last state), `Probes`/`HTTPProber`, `ParseShow`, `ParseJournal`, `UnitPath`. Fakes: `services/fake` (`Manager` with `Fail`, `Freeze` and `Set`; `Bus` with `JobResult`, `NeverFinish`, `Emit` and `Lose`), `execx/fake` for systemctl and journalctl. The routes live in `internal/api` (`RegisterServices`, `RegisterLogs`, with the `ServiceStatusSource`, `ServiceActions`, `LlamaRestarter`, `LogTailer` and `AuditWriter` interfaces); wiring all of it is AGENT-20.

### 4.4 Hardware detection (`internal/hardware`)

All probes run through `execx` (fixed argv, 3 s timeout, 1 MiB output cap) or read files. Results are cached; the sampler refreshes GPU/RAM/CPU/thermal/battery every 10 s and disks every 60 s. A probe failure keeps the last value with `stale: true` and never blocks the API.

**How it is built** (AGENT-10, `internal/hardware`):
- **Seams.** Files are read through an `fs.FS` rooted at `/` (paths without the leading slash; tests pass `os.DirFS("testdata/...")` or `fstest.MapFS`), programs through `execx.Runner` plus a `LookPath` hook. `Options` also takes `Statfs`, `Now`, `DriveRoots`, `ProfileStorage`, `ConfiguredBackend` and `OnPowerChange`, all optional. Fixtures live in `internal/hardware/testdata/` (`nvidia-smi/`, `sysfs/<scenario>/`, `proc/<scenario>/`, `journal/`, `upower/`, `lspci/`, `rocm-smi/`, `radeontop/`); a scenario directory stands for `/`, so it holds `proc/`, `sys/`, `dev/` and `etc/` folders. Fixtures contain invented values only.
- **Rounds.** Each sampling round runs its probes concurrently, each under its own deadline, and the round as a whole is capped at 5 s (`SampleCap`). A probe that has not answered by the cap, or that failed, keeps its last value with `stale: true` on its section (`gpus[]`, `ram`, `disks[]`, `power`, `thermal`, `cpu`; the key is absent when the value is fresh). The first sample is taken synchronously at startup (`Prime`, step 9 of §4.1) under the same cap.
- **Consumers.** Other packages depend on the `hardware.Sampler` interface (`Snapshot()`, `Refresh(ctx)`, `SampleGPUs(ctx)`), never on the concrete service; `hardware/fake` is the scripted implementation for their tests. `Snapshot` carries the derived values the router needs: `FitMemory(backend)` (`G_total`), `OthersMiB(backend)` (`G_others`), `UnifiedMemory(backend)`, plus the agent's own `Self` CPU and RSS for heartbeat vitals. `SampleGPUs` is the in-process read `aictl bench` calls every 250 ms.
- **Findings.** After every round the evaluator turns the snapshot into the `LAI-HW-*` results (and `LAI-LLM-027`). Each threshold keeps its own in-memory hysteresis state: raised after N consecutive samples, cleared only after the value is back past the re-arm margin; only a completed round advances the counters, and a stale section keeps its last verdict. `Service.Checks()` returns one fast check per group (`hw.gpu`, `hw.memory`, `hw.disk`, `hw.thermal`, `hw.power`, `hw.oom`) for the doctor registry (AGENT-11), each returning results in the shape of §3.8.4 with the English text of §8.3.
- **Wiring.** `hardware.Register(reg, sampler, opts)` adds `GET /hardware` to an `api.Registry`; `app.go` calls it, and passes `Service.DetectBackend` as config's `DetectBackend` hook and the config's `llama.backend` as `ConfiguredBackend`.

#### 4.4.1 GPU — NVIDIA
- Presence: `/proc/driver/nvidia/version` exists **or** `nvidia-smi` on PATH (`/usr/bin/nvidia-smi`).
- Sample: `nvidia-smi --query-gpu=index,name,memory.total,memory.used,memory.free,temperature.gpu,utilization.gpu,power.draw,compute_cap,driver_version,clocks_event_reasons.active --format=csv,noheader,nounits`. Recent drivers renamed `clocks_throttle_reasons.*` to `clocks_event_reasons.*` (the old name is a deprecated alias); if nvidia-smi rejects the field (non-zero exit with "is not a valid field"), the sampler retries once with `clocks_throttle_reasons.active` and remembers the working name for the process lifetime. Parse CSV (trim, `[N/A]` → null). The reasons field is a hex bitmask: map `0x20` SW thermal, `0x40` HW thermal, `0x80` HW power brake → `throttling: ["thermal"…]`.
- Per-process VRAM: `nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv,noheader,nounits`. `vram_used_by_others_mib` = used − sum(llama-server processes under the `localai-llama` cgroup, matched by reading `/proc/<pid>/cgroup`). Graphics clients (Xorg, gnome-shell) don't appear in compute-apps; their usage is `memory.used − sum(compute apps)`, which is counted as "others". `vram_used_by_others_mib` > 25 % of `G_total` (the GPU memory used for fit, [llama-router.md](llama-router.md) §4.3.1) for 3 consecutive samples, cleared at ≤ 23 % → `LAI-HW-005` (warning; AMD: same rule on the fit memory).
  - `processes[]` lists every compute process (`name` is the base name of the executable). With **one** GPU they are that GPU's; with several, a process cannot be attributed (the GPU uuid and bus id are never queried), so `processes` is `[]` and the llama-server memory is spread over the GPUs in proportion to their use when computing each `vram_used_by_others_mib`. If the compute-apps query fails or prints `[N/A]`, only this detail is lost (the sample stays valid, `processes` is empty, a hidden memory value counts as 0).
  - `throttling` names are `thermal` (bits `0x20` or `0x40`) and `power_brake` (`0x80`); other bits (idle, application clocks, SW power cap) are not throttling. `compute_cap`, `driver` and `power_w` are `null` when nvidia-smi says `[N/A]` or `[Not Supported]`.
  - A malformed row fails the whole sample (the last value stays, marked stale): a half-read table is never reported as truth.
- `nvidia-smi` failing (non-zero exit, timeout, malformed output) while `/proc/driver/nvidia/version` exists, three samples in a row → `LAI-HW-002`; the first good sample clears it. An installed `nvidia-smi` without the driver file that fails is simply "no NVIDIA GPU".
- Never query `uuid`, `serial`, `pci.bus_id` (redaction).

#### 4.4.2 GPU — AMD
- Presence: `/sys/class/drm/card*/device/vendor` == `0x1002` and the card has `mem_info_vram_total`.
- Memory (bytes, sysfs): `mem_info_vram_total`, `mem_info_vram_used`, `mem_info_gtt_total`, `mem_info_gtt_used`. Utilization: `gpu_busy_percent`. Temperature: `device/hwmon/hwmon*/temp1_input` (millidegrees, label `edge`). Name: `device/product_name` if present, else the `lspci -mm -s <slot>` model string, else `"AMD GPU"`.
- **Unified memory rule:** if `vram_total < 4 GiB` and `gtt_total ≥ 16 GiB` → `unified_memory: true` (Strix Halo: ~2 GiB UMA + ~61 GiB GTT). Then the "GPU memory" used for fit rules is `gtt_total` (Vulkan RADV allocates from GTT) and `used` is `vram_used + gtt_used`. The wire fields carry those fit values: `vram_total_mib` is the GTT size and `vram_used_mib` the sum on a unified node, the card's VRAM otherwise (GTT not counted).
- `index` is the position of the card among the AMD cards (NVIDIA indexes come from nvidia-smi, so on a hybrid laptop both vendors have an index 0; `vendor` tells them apart). On a hybrid laptop the `cuda` backend fits on the NVIDIA GPUs only and `vulkan` on the AMD GPUs (the NVIDIA ones when there is no AMD GPU).
- `vram_used_by_others_mib` = used − what the processes in the `localai-llama` cgroup hold, summed from `/proc/<pid>/fdinfo/*` of the pids in `/sys/fs/cgroup/system.slice/localai-llama.service/cgroup.procs` (`drm-driver: amdgpu`, `drm-memory-vram`, plus `drm-memory-gtt` on a unified node; one count per `drm-client-id`; with several AMD cards, matched by `drm-pdev` to the card's `PCI_SLOT_NAME`). No cgroup file means llama is not running (all use is "others"); llama running but no fdinfo accounting (an older kernel) gives `null`, which the router and `LAI-HW-005` treat as unknown (0 and skipped).
- Optional enrichers if installed, used only to fill what sysfs did not give: `rocm-smi --showmeminfo vram --json` (used VRAM when `mem_info_vram_used` is unreadable), `radeontop -d - -l 1` (utilization when `gpu_busy_percent` is unreadable). Absent tools are not errors. The `lspci` model string is the 4th quoted field of `lspci -mm` (nothing else of its output is kept).
- Device nodes `/dev/kfd` and `/dev/dri/renderD*` must exist (`LAI-HW-003`), and the `localai` user must be a member of `video` and `render` in `/etc/group` (`LAI-HW-004`; a group the distribution does not have, or an unreadable `/etc/group`, is not reported). Both checks run only when an AMD card is present.
- Vulkan device sanity (doctor only, not the sampler): `vulkaninfo --summary` (10 s); a device list containing only `llvmpipe` → `LAI-HW-018`.

#### 4.4.3 Backend detection (used when `llama.backend` is absent, and by `aictl profile suggest`)
NVIDIA present → `cuda`; else AMD present → `vulkan`; else `cpu`. If `node.yaml` says `cuda` but no NVIDIA GPU is found → `LAI-HW-017`; the same code is raised when it says `vulkan` and no GPU of either vendor exists (Vulkan runs on NVIDIA too, so `vulkan` on an NVIDIA-only node is valid). `cpu` is never a mismatch; an empty or unknown value is config validation's business.
"NVIDIA present" for detection is the driver file `/proc/driver/nvidia/version`, or an installed `nvidia-smi` whose GPU query lists at least one GPU (so a leftover binary does not make an AMD node `cuda`). The detection needs no sampler round: `Service.DetectBackend()` (and the package function `hardware.DetectBackend()`) answer on demand, which is what config's `DetectBackend` hook calls before the sampler has started. `LAI-HW-017` is evaluated only after the first sample.

#### 4.4.4 RAM, CPU
`/proc/meminfo`: `MemTotal`, `MemAvailable`, `SwapTotal`, `SwapFree` (kB → MiB). Thresholds (3 consecutive samples, re-armed when the value is back 2 points on the good side): `MemAvailable` < 5 % of `MemTotal` → `LAI-HW-006`; swap used (`SwapTotal − SwapFree`) > 50 % of `SwapTotal` (only when `SwapTotal` > 0) → `LAI-HW-016`. `/proc/loadavg` (first field → `load1`). `/proc/cpuinfo` first `model name` (ARM: `Model`, else `Hardware`), count of `processor`. Agent self: `/proc/self/stat` (utime+stime, fields counted after the last `)` because the command name may contain spaces; delta over elapsed time, `USER_HZ` 100 → `cpu_percent` of one core, 0 on the first sample), `/proc/self/status` `VmRSS`. `MemTotal` and `MemAvailable` are required: a `/proc/meminfo` without them is a failed probe (stale), not a zero reading.
"Consecutive" counts sampling rounds, not evaluations: an OOM check or a service verdict between two rounds does not advance a counter. `LAI-HW-005` clears at ≤ 23 % (2 points back), `LAI-HW-006` at ≥ 7 % available, `LAI-HW-016` at ≤ 48 % used.

#### 4.4.5 Disk
`syscall.Statfs` (the standard library: the module has no `golang.org/x/sys` dependency to vendor) on: `/srv/localai` (`internal_root`), each mounted content root (`drive_root`, from the drive watcher through the `DriveRoots` hook), `/var/lib/localai` (`state`), `/opt/localai` (`opt`), `/etc/localai` (`etc`). `free_bytes = Bavail × Bsize`. Filesystem type from `/proc/self/mountinfo` (longest mount point containing the path; octal escapes such as `\040` decoded). A path that has never been measurable (the directory does not exist) is left out of `disks`; one that fails after working keeps its last value with `stale: true`. Thresholds: free < profile `storage.min_free_bytes` on the internal root → `LAI-HW-007` (skipped while the `ProfileStorage` hook has no profile); any of these filesystems < 5 % free → `LAI-HW-008`, cleared at ≥ 7 %. Disk findings are evaluated after each disk round (every 60 s, and on `?fresh=1`).

#### 4.4.6 Battery and AC
Primary: `/sys/class/power_supply/*/type`. `Mains` → `online` (1 = AC); a `USB*` supply (USB-C and USB PD chargers report as `USB` on current kernels) that is online counts as AC too; a supply with `scope` = `Device` (a wireless mouse or headset) is ignored. `Battery` (the first one that is `present`) → `capacity` (%), `status` (`Charging`, `Discharging`, `Full`, `Not charging`), `energy_now`/`power_now` (µWh/µW) or `charge_now`/`current_now` for time remaining (`energy_now / power_now × 3600` when discharging). `source = ac` if any Mains is online, `battery` if a battery is discharging, else `unknown`. Fallback when sysfs is missing or a battery has no usable `capacity` (not a number in 0..100): `upower -i <path>` for the first `upower -e` entry containing `battery_` (the path must match `/org/freedesktop/UPower/devices/<name>` before it is passed on; parse `percentage`, `state`, `time to empty` in seconds, minutes, hours or days). `discharging` → `source: battery`; `charging`, `fully-charged` and `pending-charge` → `source: ac`. A machine with no power data at all (a desktop or VM without `power_supply`, no `upower`) reports `source: unknown`, `battery_percent: null` and is not an error; a desktop with only a Mains supply reports `ac`. `time_remaining_s` is `null` unless discharging with a non-zero rate (a negative `current_now` is taken as a magnitude). Power profile: `powerprofilesctl get` (optional, one lowercase token; absent or unexpected output → `profile: null`; ≠ `performance` → `LAI-HW-015` info on AC only).
Thresholds with hysteresis (re-arm 5 points above): on battery → `LAI-HW-012`; < 20 % → `LAI-HW-013`; < 10 % → `LAI-HW-014` (critical). `LAI-HW-013` and `LAI-HW-014` apply only while `source` is `battery` (a charging or plugged-in battery is not an alarm); both clear when AC returns, 013 at ≥ 25 %, 014 at ≥ 15 %; while 014 is raised, 013 is not reported (it stays latched underneath). `hardware.power` fires on any change of source, percent or charging, never for the first observation. These also become EAB node `events` (§4.8.3).

#### 4.4.7 Thermal
CPU temperature = max of: `/sys/class/thermal/thermal_zone*/temp` whose `type` is `x86_pkg_temp`, `TCPU`, `cpu-thermal` or `k10temp`; and hwmon `name` ∈ {`coretemp`, `k10temp`, `zenpower`} `temp*_input`. Falls back to `acpitz` (only when no CPU sensor above exists; `thermal.zones` lists the sensors the value was taken from, one entry per hwmon device = its hottest `temp*_input`). Readings outside 1 to 150 °C (a dead sensor reports 0 or a large negative number) are ignored; a machine with no usable sensor reports `cpu_temp_c: null` and is not an error. Throttle count: `/sys/devices/system/cpu/cpu*/thermal_throttle/package_throttle_count` (the largest over the CPUs is `cpu_throttle_events`; a delta between samples > 0 → throttling; the first sample is the baseline). A GPU with a non-empty `throttling` list also counts. Thresholds: GPU ≥ 87 °C → `LAI-HW-009` (cleared below 82 °C); CPU ≥ 95 °C → `LAI-HW-010` (cleared below 90 °C); throttling seen in 3 consecutive samples → `LAI-HW-011` (cleared by the first sample without throttling).

#### 4.4.8 OOM detection
The service watcher reads `ExecMainStatus`/`Result` of `localai-llama`; `Result=oom-kill` or a journal line `Out of memory: Killed process <pid> (llama-server)` (checked via `journalctl -k -n 200 -o json --since=<last check>` every 60 s) → `LAI-LLM-027`.
Details: `--since` is passed as one argument, `YYYY-MM-DD HH:MM:SS UTC`, the end of the previous successful check (the first check looks back one minute: kills before the agent started are not news). A failed or timed-out query (or no `journalctl`) leaves the window open, so the next check covers the gap. Records are parsed one JSON object per line; a line that is not JSON (a truncated last line), has a binary `MESSAGE`, or names a process other than `llama-server` is skipped. `Memory cgroup out of memory: Killed process …` counts like the plain spelling. A kill is reported once, whatever the overlap between two windows (deduplicated by pid and timestamp). The service watcher feeds `Result=oom-kill` through `ObserveServiceResult`. Because a kill is an event, `LAI-LLM-027` stays raised for 15 minutes after the latest one, then clears.

### 4.5 Connectivity prober
Exactly as [modes.md](../architecture/modes.md) §4: every 30 s in online mode, 3 s timeouts, in order: `HEAD <cloud.url>/api/health` → `full`; `HEAD https://huggingface.co` → `internet`; `tailscale status --json` `BackendState == "Running"` and at least one peer `Online` → `tailnet`; else `none`. The prober uses a dedicated `http.Client` with no proxy env and `DisableKeepAlives`. `state.connectivity.none_since` is set when the value first becomes `none`; after 30 min the UI banner flag `suggest_survival: true` appears in `GET /mode` and alert `LAI-NET-011` (info) is raised. Never changes mode.

Problem codes from the same probes (raised while the condition lasts, cleared on the next good result): the result is `internet` (the cloud check failed by connect error, TLS error or timeout, but huggingface.co answered) → `LAI-NET-012` (info); the cloud check failed because the lookup of the `cloud.url` host returned a DNS error (`*net.DNSError`, not found or server failure) → `LAI-NET-013` (warning). Both can be active at once only if the DNS failure is specific to the cloud host.

#### 4.5.1 `internal/modes/prober.go` (AGENT-13)
`modes.NewProber(ProberOptions)` returns the `ConnectivityProber`, which is the engine's `Prober` seam (`Start`, `Stop`; both idempotent, neither blocks) and the source of the `net.connectivity` doctor check. Details the paragraphs above leave open:

- **Passes.** `Start` runs one pass at once and then one per 30 s tick; every pass first asks `Engine.Online()` (§4.2.5), so a transition away from online silences it before `prober_stop`. A pass stops at the first check that succeeds: a `full` pass sends no huggingface request, a `tailnet` check runs only when both HEAD checks failed. A 2xx answer makes the cloud check succeed (a redirect does not); 2xx or 3xx makes the internet check succeed. The client reports a redirect instead of following it.
- **Hysteresis.** A worse value (`full` > `internet` > `tailnet` > `none`) is reported only after **two consecutive passes** agree on a worse result (`DownAfter`); a better value, and the first pass after `Start`, are reported at once. One dropped packet therefore never flips the node to `none` or fires `LAI-NET-011`'s clock. `none_since` is the time of the **first failing pass** of the streak (the evidence), not of the second; `since` is the time of the report. A `Start` that finds a recorded value (an agent restart while online) continues from it, so `none_since` survives a restart.
- **No cloud configured** (`cloud.url` empty or not an http/https URL, the safe defaults): the cloud check is skipped, a working internet reads `internet`, and `LAI-NET-012` is **not** raised (there is no cloud that fails to answer).
- **`LAI-NET-013`** follows the cloud host's lookup, as this section says (a `*net.DNSError` from the cloud check), also when huggingface.co answers; like the value it needs two consecutive passes. DNS broken everywhere gives `none` and `LAI-NET-013` together.
- **`Stop`** cancels the pass in flight, waits up to 0.5 s, and drops that pass's write by generation, so the `not_checked` that the engine's `prober_stop` records is never overwritten by a late result. A stopped prober reports no problem codes. Offline and survival never probe (§4.2.5).
- **`net.connectivity`** (fast, no I/O) reports from what the prober already holds: `LAI-NET-011` when the recorded value is `none` and `none_since` is more than 30 minutes ago, `LAI-NET-012`, `LAI-NET-013`.

### 4.6 Model operations
Implemented in `internal/router` and specified in [llama-router.md](llama-router.md) §4 (fit rules, preset generation, load/unload orchestration, rollback, boot default, single mode, builds). The agent API handlers, the cloud job executors and the mode engine all call the same `router.Orchestrator` methods.

### 4.7 Tailscale Serve management (`internal/modes/tailscale.go`)

#### 4.7.1 What "our listeners" means
Only listeners that forward to **our llama port** (`127.0.0.1:<ports.llama>`) on the pattern ports:

| Pattern | Our listener, as seen in `tailscale serve status --json` |
|---|---|
| A | `TCP["18080"].TCPForward == "127.0.0.1:8080"` |
| B | `TCP["443"].HTTPS == true` **and** `Web["<host>:443"].Handlers["/"].Proxy == "http://127.0.0.1:8080"` |

Anything else in the serve config (other ports, other paths, other targets) is **foreign** and is never modified. The agent never runs `tailscale serve reset`, `tailscale down`, `tailscale logout` or `tailscale funnel` on ports it does not own.

#### 4.7.2 Apply (online mode, `tailscale.manage: true`)
```
status := tailscale status --json            (3 s)  → BackendState
   not installed            → LAI-NET-001, stop
   tailscaled not running   → LAI-NET-002, stop (retry next reconcile)
   BackendState != Running  → LAI-NET-003 ("not logged in"), stop
cfg := tailscale serve status --json        (3 s)
want := pattern from node.yaml
if want == none: ensure neither A nor B "our" listener exists (remove ours), done
if our listener for `want` exists: ok (record state.tailscale.applied_pattern)
elif the port for `want` is used by a FOREIGN listener: LAI-NET-005, do not touch, stop
else run:
   A: tailscale serve --bg --tcp=18080 tcp://127.0.0.1:8080
   B: tailscale serve --bg --https=443 http://127.0.0.1:8080
   (10 s timeout) → re-read cfg and verify; mismatch → LAI-NET-004
if our listener for the OTHER pattern also exists (e.g. ai-n3 today has both): LAI-NET-005 (warning,
   "an extra listener exists"), do NOT remove it automatically; fix text names the exact off command.
if cfg.AllowFunnel has "<host>:443" or ":18080" true → LAI-NET-009 (critical) and turn it off for that port:
   tailscale funnel --https=443 off   (only for our port)
```
The funnel flag of a pattern A port is turned off with `tailscale funnel --tcp=18080 off`; a Funnel flag on a port where our `/` handler shares the port with other handlers ("mixed", 4.7.5) is reported as `LAI-NET-009` (`details.turned_off: false`, the command in `details.command`) and left alone, and a flag on a port that serves nothing of ours is someone else's decision and is not reported.

Pattern B note: the first HTTPS request after enabling can take ~20 s while Tailscale provisions the certificate. The verify step does not do an HTTPS request; doctor `net.serve_reachable` (manual/explicit only) does, with a 30 s timeout, and reports `LAI-NET-008` (info) on timeout.

#### 4.7.3 Remove (offline/survival, or `serve_pattern: none`)
For each of A and B, if **our** listener exists:
- A: `tailscale serve --tcp=18080 off`
- B: `tailscale serve --https=443 off`

Then re-read and verify none of ours remain (`LAI-NET-006` if one does). Because privacy is the point of offline mode, removal handles **both** patterns regardless of `serve_pattern` (an operator might have changed the pattern since the listener was created). Foreign listeners are reported (`LAI-NET-005`) but left alone. "Foreign listener" here means one that forwards to our llama port without being ours (for example `TCP["9999"].TCPForward == "127.0.0.1:8080"`, or `Web["<host>:8443"]` proxying to it): a listener to some other target is not reported at all. `details.command` names the exact off command of each (`sudo tailscale serve --tcp=9999 off`).

Outcomes: a verified removal is silent (`state.tailscale.applied_pattern` becomes `""`); a removal command that ran but left one of ours in the re-read is `LAI-NET-006` (critical, `details.commands` names the survivors, `details.reason` the failed command); a status or serve read that fails or times out, or a re-read that cannot be done, is `LAI-NET-004` (cannot tell). `LAI-NET-004` and `LAI-NET-006` are the warnings the engine retries on every 60 s (§4.2.6). With `serve_pattern: none` in **online** mode the same removal runs, a residual listener is `LAI-NET-004` (the node is not offline, so `-006` would be wrong), and a missing, stopped or logged-out Tailscale is not a problem. `tailscaled` not running, or `tailscale` not installed, during an offline or survival removal is success (§5).

Known gap: a `tailscaled` that is down when `tailscale_off` runs keeps its saved serve configuration, and restarting it re-publishes whatever was saved. The reconciler is off in offline and survival, so nothing re-checks until the next agent start (which runs `tailscale_off` again). The unit order (§3.6, `After=tailscaled.service`) and the start-time policy narrow this; the doctor's `net.serve_state` shows the last pass only. Closing it needs a periodic read-only offline check, which this spec does not require.

#### 4.7.4 Drift
Every 5 min in online mode the reconciler re-reads the serve config. Missing listener → re-apply and raise `LAI-NET-007` (info, auto-cleared on success). All commands run as root via `execx` with `PATH` fixed to `/usr/sbin:/usr/bin:/sbin:/bin` (the `Env` of every command is exactly that one entry).

`LAI-NET-007` is raised by the pass that restores the listener and cleared by the next pass that finds nothing to restore (the findings of every pass replace those of the previous one), so it stays visible for one reconcile period. A restore is *drift* only when `state.tailscale.applied_pattern` already held the wanted pattern: the first apply after an offline period (`""`) is not.

#### 4.7.5 `internal/modes/tailscale*.go` (AGENT-13)
`modes.NewTailscaleManager(TailscaleOptions)` returns the `TailscaleManager`: the engine's `TailscaleController` (`On`, `Off`), the reconciler (`Reconcile`, plus `Start`/`Stop`/`Name`, an `app.Worker` that runs `Reconcile` every 5 min and does nothing unless `Engine.Online()`, checked **after** taking the manager's lock so a pass that waited for `Off` never re-publishes the node), the source of the `net.serve_state` and `net.serve_reachable` doctor checks (`ServeResults`, `CheckServeReachable`) and the tailnet check of the prober (`TailnetUp`). `Status()` is the `tailscale` block of `GET /status` (cached, no process) and `TailnetURL(ctx)` the `tailnet` base URL of `GET /key` (§3.8.13). The wiring (AGENT-20) calls `Reconcile` when `tailscale.*` changes in `node.yaml` (§3.4, "Live"). Rules the pseudo-code above leaves open:

- **Classification of a port.** *Ours* is exactly §4.7.1; for pattern B additionally the `host:443` entry must hold **only** the `/` handler (a proxy to `127.0.0.1:<ports.llama>`, with or without a trailing slash, `localhost` accepted) and `TCP["443"]` must be a plain HTTPS terminator; a TCP forward with `TerminateTLS` is not ours. *Mixed*: our `/` handler beside other handlers on 443. `tailscale serve --https=443 off` removes the whole port, other people's handlers included, so a mixed port is never applied to and never removed: apply answers `LAI-NET-005` (`details.blocked_by: "mixed"`), an offline removal answers `LAI-NET-006` (`details.shared_port: true`, no command: the operator removes the one handler by hand). *Foreign*: anything else on the port (`blocked_by: "foreign"`). Web entries are matched by `<DNSName-without-dot>:443`; with no DNS name known any `:443` entry is looked at.
- **`tailscale.manage: false`** never runs `tailscale serve` or `tailscale funnel`. It still reads, so `aictl doctor` reports: `LAI-NET-001/002/003` (for a pattern other than `none`), `LAI-NET-005` for a blocked port, the other pattern and foreign listeners, `LAI-NET-009` with `turned_off: false`, and in `Off` `LAI-NET-006` with `details.commands` for each of ours still published (the engine then retries the read every 60 s, harmlessly).
- **Reads and errors.** `status --json` and `serve status --json` get 3 s, commands that change something 10 s, each capped at 1 MiB. Output that is not a JSON object is an error, never "empty" (empty output and `null` do mean no configuration). `tailscaled` down is recognised by the CLI's message (`failed to connect to local tailscaled`, a missing or refused socket); any other failure or a timeout is `LAI-NET-004` with `details.reason` (a short phrase, never stderr or the tailnet name). A cancelled context is the only error `On` and `Off` return.
- **Logged out.** `On` answers `LAI-NET-003` for any `BackendState` other than `Running` (`NeedsLogin`, `Stopped`...); `Off` removes ours whatever the state, because the saved configuration comes back when the node does.
- **Details, not secrets.** Results carry ports, patterns and commands, never the tailnet name; the commands are the argv with `sudo tailscale` in front (`details.command`, or `details.commands` for several). Transition warnings are the code's English title plus `Run: <command>`; informational results (`LAI-NET-007`) are not warnings.
- **`net.serve_state` is cached and fast**: it returns the findings of the last pass (`On`, `Off` or `Reconcile`), so it reports nothing before the first pass and never spawns a process. `net.serve_reachable` is explicit-only: one `GET /health` on `https://<dnsname>` (pattern A: `http://<dnsname>:18080`) with a 30 s budget; any HTTP answer is reachable, a timeout is `LAI-NET-008`, any other failure `LAI-NET-004` (`details.reason`).
- **Test double.** `modes/fake.TailscaleCLI` is an `execx.Runner` over an in-memory serve configuration (fixtures in `agent/testdata/tailscale/`): it records every argv, answers `status --json`, `serve status --json` and the serve and funnel commands the agent uses, and can be scripted to fail, time out, or exit 0 without changing anything (a partial apply). `Unexpected()` lists every command outside that set, so a test of the ownership rules can assert that `serve reset`, `down` and `logout` never ran.

### 4.8 Cloud link client (`internal/cloud`)

The agent is an EAB worker node. Protocol reference: EnterpriseAppBase `docs/specs/worker-nodes.md` (register/reattach, heartbeat and vitals, claim, lease and claim token, result and failure, route allowlist). This section is the Go client's contract; conformance tests run against `internal/cloud/fake`, a fake EAB server written from that spec.

#### 4.8.1 State machine
```
  unenrolled ──enroll──► registering ──2xx──► linked ◄──────────► backoff
      ▲                     │  ▲                │  │  net/5xx/429     (success returns)
      │                  401│  │404/403 on      │  └──403 on claim──► disabled ──claim ok──► linked
      │                     ▼  │heartbeat       │
      │                   revoked ◄──401 on any call
      │                     │ (hourly register probe; new enroll → registering)
      │
  DELETE /cloud from any state ──► unenrolled
  mode → offline/survival from any enrolled state ──► stopped ──mode → online──► registering
```

| From | Event | To | Side effects |
|---|---|---|---|
| unenrolled | credential written (enroll) or found at start, mode online | registering | — |
| unenrolled | credential found at start, mode offline/survival | stopped | — |
| registering | register 200 | linked | store `server_node_id`, handshake (`GET …/localai/config`), start heartbeat + claim + state push |
| registering | network error / 5xx / 429 | registering (backoff timer) | `last_error` |
| registering | 401 | revoked | `LAI-CLD-051` |
| registering | 400 / 403 | registering (retry in 1 h) | `LAI-CLD-054` |
| linked | 3 consecutive heartbeat failures, or a claim failure | backoff | backoff per loop |
| backoff | next successful call | linked | clear `LAI-CLD-053` |
| linked / backoff | 403 on claim | disabled | `LAI-CLD-052`; heartbeat every 300 s, claim re-checked on each heartbeat |
| disabled | claim answers 200 again | linked | clear alert, heartbeat back to 15 s |
| linked / backoff / disabled | 404 on heartbeat, or first 403 on heartbeat | registering | — |
| any enrolled | 401 on any call | revoked | stop loops, `LAI-CLD-051`, register probe hourly |
| revoked | hourly register returns 200, or new enroll | linked / registering | clear alert |
| any enrolled | mode → offline/survival | stopped | §4.8.8 |
| stopped | mode → online | registering | — |
| any | `DELETE /cloud` | unenrolled | deregister (3 s), delete credential + `link.json` |

| State | Loops running | Exposed in `GET /cloud.state` |
|---|---|---|
| `unenrolled` | none | `unenrolled` |
| `stopped` | none (mode policy) | `stopped` |
| `registering` | register retry | `registering` |
| `linked` | heartbeat, claim, state push | `linked` |
| `backoff` | the failing loop retries with backoff; others continue if they succeed | `backoff` (`last_error`) |
| `disabled` | heartbeat (300 s) + state push; claim re-checked on each heartbeat | `disabled` (`LAI-CLD-052`) |
| `revoked` | none except hourly register probe | `revoked` (`LAI-CLD-051`) |

Every state change emits `cloud.state` and is logged (`component=cloud`).

#### 4.8.2 Register
`POST {cloud.url}/api/nodes/register`, `Authorization: Bearer nod_…`, 10 s timeout:
```json
{"name": "ai-n3", "hostname": "hp-linux", "platform": "linux/amd64", "cliVersion": "aiagent/0.1.0",
 "eligibleTypes": ["localai.status.refresh", "localai.model.load", "…one per cloud.allow entry…"],
 "concurrency": 1,
 "capabilities": {"localai": {"schema": 1, "agent_version": "0.1.0", "backend": "cuda", "profile": "small-8gb",
                  "llama_build": "f3586e66b-cuda", "llama_mode": "router", "models_max": 1,
                  "gpu": "NVIDIA GeForce RTX 2070 with Max-Q Design", "vram_total_mib": 8192, "ram_total_mib": 14336,
                  "catalog_version": "2026.11.02-1", "serve_pattern": "B", "public_host": "ai-n3.marin.cr"}}}
```
- `name` = `node.id`: register is idempotent on `(owner, name)` and answers `200 {node, reattached, serverFeatures}`. The agent stores `node.id` (the server UUID) as `server_node_id` in `/var/lib/localai/cloud/link.json` (root 0600) together with `url`, `registered_at`, `server_features`.
- `reattached: false` while `link.json` already had a different `server_node_id` → log info "new server row" and replace it.
- If the response's `lastHeartbeatAt` is within 60 s and `hostname` differs from ours → `LAI-CLD-061` (warning: "another machine is using the id ai-n3"); continue (last writer wins, per EAB).
- `eligibleTypes` = `"localai." + name` for each `cloud.allow` entry; recomputed and re-registered when `cloud.allow` changes (hot reload).
- Responses: `200` → `linked`; `401` → `revoked`; `400` → `LAI-CLD-054` (log the server's `details`; retry after 1 h; it means our body is wrong); `403` → `LAI-CLD-054`; network/5xx/429 → backoff (§4.8.7).
- Register runs at link start, after `404` on a heartbeat (node deleted by an admin or pruned), and after `403` on a heartbeat (wrong owner, once; a second `403` → `LAI-CLD-054`).
- The `capabilities.localai` object also carries `"allow": [<cloud.allow entries>]` so the cloud UI can tell which commands the node accepts ([cloud-app.md](cloud-app.md) §5, `LAI-CLD-057` hint).

**Handshake.** Right after a successful register, and whenever any `/api/nodes/{id}/localai/*` route answers `404`, the agent calls `GET /api/nodes/{id}/localai/config` ([cloud-app.md](cloud-app.md) §3.6) and stores the answer in `link.json`:

| Field | Use |
|---|---|
| `features` (`state.v1`, `commands.v1`, `catalog.v1`, `key.v1`) | which app routes exist |
| `commandPlane` (`localai` \| `eab`) | which claim routes to use (§4.8.6) |
| `heartbeatIntervalS` | overrides `cloud.poll.heartbeat_seconds` |
| `stateMinIntervalS`, `stateMaxIntervalS` | debounce / maximum interval of the state push (§4.8.4) |
| `claimWaitMaxMs` | long-poll wait for the `localai` command plane |
| `keyReported` | `false` → push the key (§4.8.5) |
| `serverTime` | clock check: skew > 5 min → `LAI-AGT-012` |

A `404` on `config` itself means an older cloud without the Local AI node routes: `commandPlane = eab`, no `state.v1`/`key.v1` (state goes through the capabilities fallback, `LAI-CLD-056`), re-checked every hour. Responses of the app routes use EAB's `{data, meta}` envelope; the client unwraps `data`, and reads our code from `details.code` on errors.

#### 4.8.3 Heartbeat (every `heartbeatIntervalS`, default 15 s)
`POST /api/nodes/{server_node_id}/heartbeat`, 10 s timeout:
```json
{"status": "online", "concurrency": 1,
 "capabilities": { "localai": { "…same as register…" } },
 "vitals": {
   "cpuPercent": 1.2, "rssBytes": 41943040, "stateDirFreeBytes": 293000000000, "stateDirTotalBytes": 342000000000,
   "slotsUsed": 0, "slotsTotal": 1, "uptimeSeconds": 86400, "cliVersion": "aiagent/0.1.0",
   "counters": {"claims": 12, "emptyPolls": 28000, "claimFailures": 3, "succeeded": 11, "failed": 1, "rateLimited": 0,
                "leaseRenewals": 40, "leaseRenewFailures": 0, "heartbeatFailures": 2, "watchdogTrips": 0},
   "extra": {
     "localai.models_loaded": 1, "localai.vram_used_mib": 6791, "localai.vram_total_mib": 8192,
     "localai.sync_running": 0, "localai.alerts_active": 1,
     "localai.gpu_temp_c": 61, "localai.gpu_util_pct": 0, "localai.cpu_temp_c": 72, "localai.ram_available_mib": 5120,
     "localai.models_loadable": 4, "localai.llama_up": 1, "localai.ark_internal_free_gib": 273,
     "localai.ark_drive_mounted": 0, "localai.sync_age_h": 12, "localai.alerts_critical": 0, "localai.catalog_version": 20261102.1},
   "host": {"cpuPercent": 7.5, "cpuCount": 12, "loadAvg1": 0.4, "loadAvg5": 0.5, "loadAvg15": 0.6,
            "memTotalBytes": 15032385536, "memFreeBytes": 1073741824, "memAvailableBytes": 5368709120,
            "osRelease": "Ubuntu 26.04.1 LTS", "arch": "x86_64", "uptimeSeconds": 172800},
   "power": {"source": "ac", "batteryPercent": 100, "charging": false}
 },
 "events": [{"type": "power.source_changed", "severity": "info", "at": "2026-11-02T15:00:00Z", "attrs": {"source": "ac"}}]}
```
Rules:
- **`vitals.extra`** is exactly the 16 keys above (the EAB maximum is 16; keys match `^[a-z][a-z0-9_.]{1,47}$`; values are finite numbers within ±1e15). The first five are the ones [cloud-app.md](cloud-app.md) §3.6 registers for display; the other 11 are **required too** and show on the EAB fleet page by key until the cloud registers labels (do not trim the list). `localai.catalog_version` encodes `YYYY.MM.DD-N` as `YYYYMMDD.N`. A value that cannot be computed is **omitted**, never sent as null. The set is fixed in code (`cloud/vitals.go`) with a test asserting count ≤ 16 and the key regex.
- `rssBytes`/`cpuPercent` are the agent process; `heapUsedBytes`/`heapLimitBytes`/`eventLoopDelayP99Ms`/`nodeVersion`/`pgDumpVersion` are not sent.
- **`host`, `power` and `events`** are sent only after the server advertised `node_host_vitals` in `serverFeatures` (register or heartbeat response). All numbers are clamped to the EAB bounds before sending (`cpuPercent` 0–100, `timeRemainingSeconds` ≤ 7 days, `osRelease` ≤ 128 chars, …).
- **Events** (≤ 50 per heartbeat, queue of 200, oldest dropped, re-queued after a failed heartbeat): EAB power events (`power.source_changed`, `power.battery_low` < 20 %, `power.battery_critical` < 10 %, `power.battery_recovered`), `node.sleep_resumed` (wall-clock gap > 3 heartbeat intervals), `node.disk_low` (state dir < 5 %), plus ours: `localai.alert.raised` / `localai.alert.cleared` (`attrs: {code}`; severity: info→info, warning/error→warning, critical→critical), `localai.model.loaded` / `localai.model.failed` (`attrs: {model}`), `localai.mode.changed` (`attrs: {to}`). Hysteresis as in EAB (the first observation is a baseline).
- `capabilities` is sent on every heartbeat (≤ 4 KiB; with the state fallback active it also carries the reduced snapshot, §4.8.4).
- **Fallbacks:** a `400` whose details mention `extra` → resend immediately without `extra` and stop sending `extra` for the life of the process (`LAI-CLD-055`, info). Any other `400` → resend without `vitals` and stop sending vitals for the process life (`LAI-CLD-055`, warning).
- `401` → `revoked`; `403` → re-register once; `404` → re-register; network/5xx/429 → backoff; each failure increments `counters.heartbeatFailures`.
- Response `serverFeatures` is stored. While the link is `disabled` the heartbeat interval becomes 300 s ([cloud-app.md](cloud-app.md) §5).

#### 4.8.4 State snapshot push
The rich Local AI state goes to **`POST /api/nodes/{server_node_id}/localai/state`** ([cloud-app.md](cloud-app.md) §3.6; mounted under `/api/nodes/` so the `nod_` allowlist covers it, ADR-0003).
- **Body:** exactly `LocalaiStateSnapshotV1` as defined in [cloud-app.md](cloud-app.md) §3.6 (that schema is authoritative; the Go mirror is `internal/cloud/state.go`, a strict struct with a test that marshals a full snapshot and validates it against a JSON Schema exported from the cloud's Zod schema, kept in `agent/internal/cloud/testdata/state.v1.schema.json`). The agent **builds** it from its internal views; it never forwards API bodies as they are. `boot_id` is a fresh UUID per agent process; `seq` starts at 0 and increments per snapshot.
- **Mapping notes:** model `status` maps `loaded`/`loading` as is, `failed` as is, everything else (`absent`, `stored`, `unloaded`, `sleeping`, `n/a`) → `unloaded`; roots are reported by root id (`internal`, `ark-<8 hex>`, [ark-sync.md](ark-sync.md) §2); alert severity `error` → `critical` (the cloud enum has no `error`; the same for `ready.problems[].severity`); `tailscale.serve_pattern` is sent from `node.yaml` as is (`A`, `B` or `none`). The `ark` block includes, per root, the full READY.json in `ark.roots[].ready` (minus `root.path` and `generated_by.node_id`) whenever its `generated_at` changed since the last accepted snapshot ([ark-sync.md](ark-sync.md) §4.16); the `drill` block carries the last drill summary plus, once after each run, the full results document ([drill.md](drill.md) §4.5). The exact rules are in [cloud-app.md](cloud-app.md) §3.6 "Mapping rules".
- **Never** included: keys, tokens, tailnet IPs/names, LAN IPs, paths under `/home`. The agent runs its own scrubber over every string before sending; a hit drops that field and logs `LAI-CLD-058` (warning) instead of sending.
- **When:** on every relevant change (events `model.*`, `mode.*`, `service.state`, `sync.*`, `alert.*`, `drive.*`, `key.rotated`) debounced to `stateMinIntervalS` (default 5 s), and at least every `stateMaxIntervalS` (default 60 s). Max body 256 KiB. If larger, the agent trims in the order of [cloud-app.md](cloud-app.md) §3.6: first the drive-root `items[]` (READY item lists of ark drive roots, then of the internal root), then the `models` list (to the profile's models plus the loaded and loading ones). Trimming keeps only the READY items that are not `verified` (the READY `counts` stay whole). The snapshot has no truncation flag (every object of the schema is strict, so a flag would be refused with `400`); the cut is logged (`component=cloud`, the steps and the final size, never values). As a last resort, when the body is still over the limit, the READY documents and then the drill results are dropped (`ready` and `results` null), which the cloud treats like an unchanged report.
- **Answers:** `200 {data: {accepted, seq}}` → ok. `409` with `details.code LAI-CLD-008, reason stale_seq` → ignored. `400` → log the Zod path (never the value), keep the snapshot builder running, next change sends a fresh one. `404`/`405` → re-run the handshake; if `state.v1` is absent use the **capabilities fallback**: a reduced snapshot (≤ 32 KiB: mode, models loaded/default, service states, ark tiers, alert codes) in heartbeat `capabilities.localai.state`, `LAI-CLD-056` (info), re-probe hourly.

#### 4.8.5 Llama key push
`POST /api/nodes/{server_node_id}/localai/key` body `{"key": "<64 hex>", "fingerprint": "ab12…9f3c", "rotated_at": "…"}` ([cloud-app.md](cloud-app.md) §3.6; stored as `('localai_node_llama_key', '<node-id>')`: purpose `localai_node_llama_key`, name `<node-id>`, the address [security-model.md](../architecture/security-model.md) §2 names; EAB names may not contain `:`). Sent: at enroll; after every key rotation **before** the rotate command's result is reported; whenever the handshake says `keyReported: false`; on `POST /cloud/resend-key` (`aictl cloud resend-key`); and on link start while `state.key.cloud_synced == false`. HTTPS only (never with `dev_insecure`). `2xx` → `cloud_synced = true`. Failure → retry every 5 min while linked; `LAI-CLD-060` (warning) after 30 min unsynced. Only the fingerprint is ever logged.

#### 4.8.6 Command plane: claim, renew, settle
Two wire-compatible planes, chosen by the handshake's `commandPlane` ([cloud-app.md](cloud-app.md) §3.6/§3.12):

| | `localai` (interim, default while the EAB seam is pending) | `eab` (after the seam ships) |
|---|---|---|
| Claim | `POST /api/nodes/{id}/localai/commands/claim` `{"types": [cloud.allow entries, **no** prefix], "limit": 1, "waitMs": min(25000, claimWaitMaxMs)}` — **long poll** | `POST /api/nodes/{id}/claim` `{"types": ["localai.<entry>", …], "limit": <free slots>}` — plain poll |
| HTTP timeout | `waitMs + 10 s` | 10 s |
| Empty answer | claim again immediately (after ≥ 250 ms) | sleep `cloud.poll.claim_seconds` (3 s) |
| Renew | `…/localai/commands/{cmdId}/renew` | `…/jobs/{jobId}/renew` |
| Result | `…/localai/commands/{cmdId}/result` | `…/jobs/{jobId}/result` |
| Failure | `…/localai/commands/{cmdId}/failure` | `…/jobs/{jobId}/failure` |

The response shape is EAB's in both cases: `jobs: [{job: {id, type, attempts, …}, params, claimToken, renewIntervalMs, leaseExpiresAt?, traceparent}]`.

```
loop while state ∈ {linked, backoff(heartbeat only)} and mode == online:
    if len(inflight) >= cloud.concurrency: wait for a slot or ctx; continue
    resp := claim(plane)
        401 → revoked; 403 → disabled (re-check on every heartbeat); 404 → handshake / re-register
        net/5xx/429 → claim backoff (§4.8.7)
    for each assignment in resp.jobs: go execute(assignment)
    pace per the table above
```
`execute(a)`:
1. **Policy check:** `a.job.type` must be `localai.<name>` with `<name>` in `cloud.allow` **and** in the known set below. Otherwise `failure {"error": "LAI-CLD-057: this node does not allow <type>"}`.
2. **Decode params strictly** (`DisallowUnknownFields`, then validation) against the params schema of [cloud-app.md](cloud-app.md) §3.7 (authoritative). Invalid → `failure {"error": "LAI-CLD-058: <field path>: <problem>"}`.
3. Persist `{job_id, type, plane, claim_token, lease_expires_at, started_at}` to `/var/lib/localai/cloud/inflight.json` (needed for `agent.update` and crash diagnostics).
4. **Renew** every `a.renewIntervalMs` (default 20 000 ms): body `{claimToken}`, with the key **omitted** when the assignment had none or `null` (never `"claimToken": null`). Renew failures are non-fatal (`leaseRenewFailures++`); on `409` log `LAI-CLD-059` and stop renewing, but let the local operation finish cleanly.
5. **Run** through the same internal method as the REST route, principal `cloud` (`actor: {kind: cloud, job_id}` in audit), deadline = the type's lease cap.
6. **Settle:** success → `result {"type": a.job.type, "result": {…}, "claimToken"?}`; error → `failure {"error": "<doctor code>: <message>", "claimToken"?}` (≤ 2000 chars; the cloud splits the leading code). **Commands are at most once: the agent never retries after a `409`** and never re-runs a command after a crash (an `inflight.json` entry found at start is reported as `failure "LAI-AGT-017: the agent restarted during the command"` if its lease may still be valid, else discarded). Network error on settle → retry 3× (1 s, 5 s, 15 s), then drop.
7. Remove the inflight entry; push a fresh state snapshot.

**Job types** — params and results are defined by [cloud-app.md](cloud-app.md) §3.7 `COMMANDS` (authoritative); this table maps each to the agent operation:

| Type (`localai.` +) | Params (summary) | Agent operation | Notes |
|---|---|---|---|
| `status.refresh` | `{}` | build + push snapshot now; re-push key if `cloud_synced` is false | result `{seq, reported_at}` |
| `model.load` | `{model_id, wait: true, unload_others, set_default}` | `POST /models/{id}/load` (wait) then, if `set_default`, `PUT /models/default` | result adds `vram_used_mib` (from hardware after load) and `is_default` |
| `model.unload` | `{model_id}` | unload | `was_loaded` |
| `model.set_default` | `{model_id}` | `PUT /models/default` | |
| `model.download` | `{model_id, roots?}` | queue download (roots are root ids) | `already_present: true` and `sync_run_id: null` when nothing to do |
| `model.delete` | `{model_id, roots, confirm}` | `DELETE /models/{id}` per root with keep-last; the agent re-checks `confirm == model_id` | result lists `deleted_roots` and `kept_roots` with reasons |
| `ark.sync` | `{roots?, items?, dry_run}` | start sync; returns at start | `started: false` + the running run id when one is in progress |
| `ark.verify` | `{roots?, items?}` | start verify | |
| `mode.offline` | `{reason?}` | **settle the result first** (`{previous_mode, mode: "offline"}`), then run the → offline transition | the link stops as part of it; it is never subject to the §4.8.8 step 2 in-flight rule (already settled) |
| `key.rotate` | `{}` | `POST /key/rotate`; push the key (§4.8.5) **before** settling; the key itself is never in the result | `cloud_synced: false` if the push failed |
| `service.restart` | `{service}` | `POST /services/{name}/restart` | |
| `logs.tail` | `{unit, lines ≤ 500}` | `GET /logs` | result `lines` is an array of strings (`"<rfc3339> <message>"`), scrubbed |
| `catalog.refresh` | `{}` | `POST /catalog/refresh` (fetches `GET …/localai/catalog`) | `accepted: false` + `rejected_code` (`LAI-CAT-002`) when lint fails |
| `agent.update` | `{version}` | self-update (§4.10) | result `{from_version, to_version, status}` with `status` ∈ `updated` \| `rolled_back` \| `already_current` ([cloud-app.md](cloud-app.md) §3.7); settled **after** the new binary passes its health check (`updated`), or by the restored old binary (`rolled_back`); in offline/survival the job fails with `LAI-AGT-023`. The decoder is AGENT-18, the executor and settle-after-health AGENT-19 (§10) |

The cloud cannot request `survival`, `online`, PIN, token, enroll/unenroll, profile changes, content deletes or arbitrary commands: those types do not exist.

#### 4.8.7 Backoff
Per loop (register, handshake, heartbeat, claim, state push), independent. Full-jitter exponential: `delay = rand(5 s, min(300 s, 5 s × 2^failures))` (5 s → 5 min, [cloud-app.md](cloud-app.md) §5). `429` honours `Retry-After` if larger. Reset on the first success. After 3 consecutive heartbeat failures the link state is `backoff`; after 5 min of failures `LAI-CLD-053` (warning) is raised and cleared on success. `revoked` re-probes every hour. Backoff never changes mode and never affects local features.

#### 4.8.8 Offline stop (`link_stop` step, agent shutdown, unenroll)
1. Stop the claim loop (an in-progress long poll is cancelled; no new claims).
2. In-flight command: `mode.offline` has already settled and is excluded from this rule. Any other in-flight command: wait up to 10 s for it to finish; if still running, `failure {"error": "LAI-CLD-063: the node is going offline"}` (3 s; `LAI-CLD-063` is agent-owned, §8.5 — the cloud-owned `LAI-CLD-034` is only the cloud's own refusal before enqueueing) and cancel it if cancellable (sync start, logs, status); a model load in progress continues locally.
3. Push a last snapshot (3 s, errors ignored), `POST /heartbeat {"status": "offline"}` (3 s, errors ignored), then `POST /api/nodes/{id}/deregister` (3 s, errors ignored). On **agent shutdown** (not a mode change) the deregister is skipped: the node is only restarting and re-registers on start.
4. State → `stopped` (mode change) or unchanged (shutdown).

#### 4.8.9 `internal/cloud` and its seams (AGENT-17)
`cloud.New(Deps)` returns the `*cloud.Link`; the daemon wiring (AGENT-20) builds it and nothing starts at init time. One `*Link` fills five seams: `modes.LinkController` (`Start`, `Stop`, plus `Shutdown` for an agent shutdown), `statusapi.CloudView`, `catalog.CloudSource` (`FetchCatalog`), the key pusher of the key routes (`Push(ctx) error`, AGENT-15) and `api.CloudLink` (the four `/cloud` routes, built by `api.CloudRoutes`). Every dependency is a small interface with a fake in the tests; the fake cloud is `internal/cloud/fake` (a TLS server with scriptable answers, a manual clock). The tests never sleep: every wait is a timer of the injected `Clock` or a channel.

| Dependency | Implemented by | When absent |
|---|---|---|
| `Status` (`StatusSource.Build(ctx, forCloud, opts)`) | `*statusapi.StatusBuilder` (`statusapi.Deps.Builder()`): the heartbeat vitals, the capabilities and the snapshot all read the node through it, never a second time through the subsystems | required |
| `Snapshot.Models`, `.Catalog`, `.Ark`, `.Sync`, `.Drill` | `statusapi.ModelView`, `*statusapi.CatalogStore`, and the ark and drill slices (`ArkSource` gives each root's tiers and its READY.json bytes, `SyncSource` the sync run, `DrillSource` the last drill with its results document) | empty list or null block |
| `Keys` (`KeySource.Key()`) | `cloud.FileKeySource` (reads `/etc/localai/api-key`, rotation time from `state.key.rotated_at`) | no key push |
| `Claim` (`ClaimLoop`: `Start`, `Stop`, optional `Probe`) | the claim loop, `*cloud.Claimer` (AGENT-18, §4.8.10) | the link runs without a command plane |
| `Inflight`, `Hardware`, `Host`, `Events`, `Bus`, `Alerts`, `Online` | the claim loop, `*hardware.Service`, `cloud.ProcHost` (`/proc`), `*events.Bus`, `*events.Bus`, `*doctor.Manager`, `(*modes.Engine).Online` | no slots, hardware, events or alerts; `Online` absent means online |

- **Lifecycle.** `Start` reads `/etc/localai/cloud-credential`: none is `unenrolled`; one while the mode is not online is `stopped`; otherwise the supervisor goroutine registers, runs the handshake and then the loops (heartbeat, state push, key, event feed, `cloud.allow` watcher) and the claim loop. `Stop` and `Shutdown` follow §4.8.8 (the claim loop first, then the loops, then the last snapshot, `{"status": "offline"}` and the deregister, 3 s each, errors ignored). Start, Stop, Shutdown, enroll and unenroll run one at a time (a context-aware semaphore of the link; nothing else waits on it). No other lock is held across a call to the cloud.
- **The register phase** has its own backoff (§4.8.7). `400` and `403` wait an hour (`LAI-CLD-054`), `401` is `revoked` (`LAI-CLD-051`) with an hourly register probe. A heartbeat `404` or the first `403` registers again; a second `403` after registering raises `LAI-CLD-054` and the heartbeat retries hourly. A handshake `404` is `commandPlane = eab` with the capabilities fallback (`LAI-CLD-056`, repeated hourly).
- **`backoff` and `disabled`.** Three failed heartbeats, or a claim failure reported through `Link.ClaimResult(err)`, move `linked` to `backoff`; the next successful heartbeat or `ClaimResult(nil)` returns it to `linked` and clears `LAI-CLD-053`. The heartbeat and claim loops are independent and the last result applied wins: a heartbeat answer applied after a claim failure (its request was sent before the failure) is a successful call and returns the link to `linked`. A state push failure, a key push failure or a catalog failure never changes the link state (each has its own retry). `ClaimResult` also implements the claim rows of the state table (401 revoked, 403 `disabled` with `LAI-CLD-052`, 404 register again). While `disabled` the heartbeat period is 300 s and each heartbeat calls the claim loop's optional `Probe`. `LAI-CLD-053` is raised when failures have lasted 5 minutes.
- **State push.** One snapshot at link start, then on each trigger event debounced to `stateMinIntervalS` and at least every `stateMaxIntervalS` (the handshake's values, else 5 s and `cloud.poll.state_seconds`); a failed push backs off on its own. `Link.PushNow` is the `status.refresh` command (it fails with `ErrNoStateEndpoint` while the capabilities fallback is on). A READY document or drill result is marked as sent only when the cloud answered `200` (`Built.Commit`), so an unaccepted one goes out again.
- **Heartbeat events** are queued from the event bus (`localai.*`, mapped as in §4.8.3) and from the host observer, which runs on every heartbeat: the first sample of each signal is a baseline, battery bands have hysteresis (low below 20 %, critical below 10 %, recovered at 25 % or more, or at 20 % or more on mains), `node.disk_low` fires below 5 % free and re-arms at 10 %. They are queued always and sent only after `node_host_vitals`.
- **Key push.** `Link.Push(ctx)` (the rotation path) answers nil when the cloud holds the key; on failure it leaves `state.key.cloud_synced` false, returns the error and wakes the retry loop. `cloud_synced` is the last thing written after a push, so a reader that sees it true sees the push logged and `LAI-CLD-060` cleared. The key is read from the key source for each push, never kept; with an address that is not https, or a cloud without `key.v1`, the push is not attempted (`LAI-CLD-060` after 30 minutes).
- **Catalog.** `FetchCatalog` always asks for the full bundle (an `If-None-Match` could answer 304 for a catalog the operator has since replaced locally, which the refresher cannot tell); `FetchCatalogIfChanged(etag)` is the conditional form. Every file's sha256 is verified; `404 LAI-CLD-013` is `ErrNoApprovedCatalog`, so the refresher falls back to GitHub.
- **Interface for the claim loop (AGENT-18).** `Link.Session()` (client, server node id, command plane, `claimWaitMaxMs`, `cloud.allow`, concurrency; only while linked, backing off or disabled), `Link.ClaimResult(err)`, `Link.ClaimDisabled()`, `Link.NoteCommandAuth(err)` (a 401 seen on renew, result or failure), `Link.Counters()` and `Link.PushNow`.
- **Scrubbing.** Every string of a snapshot or of the capabilities goes through `logx.ScrubString` plus a check for `/home/…` paths and LAN addresses; a hit zeroes the field and logs `LAI-CLD-058` with the field paths only. Error answers of the cloud are logged as status, `details.code` and `details.reason`; their message is scrubbed and cut to 200 characters, and the body of the key route is never logged.

#### 4.8.10 The command plane in code (AGENT-18)
`cloud.NewClaimer(ClaimerDeps)` builds the claim loop; it is the link's `Deps.Claim` (`Start`, idempotent `Stop`, `Probe`) and `Claimer.Inflight` is the link's `Deps.Inflight`. The link and the claimer need each other, so the wiring (AGENT-20) creates the claimer, passes it to `cloud.New`, then calls `Claimer.Bind(link)`; a loop without a link does nothing. The handlers are `jobs.Handlers(jobs.Deps)` of `internal/cloud/jobs` (one strict decoder and one executor per job name, behind `cloud.Handler`), passed to `cloud.NewRegistry`. Nothing registers at init time and `app.go` is untouched until AGENT-20.

- **What a node can run is the intersection of three sets:** the 14 names of `config.CloudJobNames()`, `cloud.allow`, and the handlers the registry holds. A job type whose dependency the wiring leaves out (`jobs.Deps` field `nil`) is not in the registry, so it is refused with `LAI-CLD-057` exactly like a type missing from `cloud.allow`, and the claim never asks for it. `agent.update` is always registered (see below).
- **Loop.** One claim per pass: the localai plane runs **one** command at a time whatever `cloud.concurrency` says (the cloud hands a node one running command there, [cloud-app.md](cloud-app.md) §4.4), the EAB plane up to `cloud.concurrency` and claims `limit = free slots`. Not online, no session or nothing allowed that the build can run: the loop waits 5 s; link `disabled`, `unenrolled`, `revoked` or `registering`: it waits for a wake-up (30 s fallback), and while `disabled` the heartbeat's `Probe` makes one claim without a long poll. Every claim outcome goes through `Link.ClaimResult` and the counters; a failed claim waits `Backoff` (§4.8.7, `Retry-After` honoured); a success resets it. Every pass waits on a timer or on the network, so the loop never spins. A cancelled long poll (`Stop`) is not a failure.
- **Assignments are untrusted.** An id that does not match `^[A-Za-z0-9._:-]{1,128}$` is dropped (nothing could be settled for it), a second delivery of an id already in flight is ignored, more assignments than free slots are refused with `LAI-CLD-058` without running, and a `renewIntervalMs` outside 5 to 60 s is clamped.
- **Decoding** (`cloud.DecodeParams`, then the type's own checks): the params must be an object of at most 64 KiB (`null` or none counts as `{}`); a key that is not a field is `unknown field` (compared exactly, not case-insensitively); a `null` value and a value of the wrong type are rejected; ids, roots, units and services are matched against the patterns of [cloud-app.md](cloud-app.md) §3.7 and the closed tables (`services.LogUnit`, the three service names). The failure text is `LAI-CLD-058: <path>: <problem>` with the path cut to 64 printable characters; nothing from the cloud is echoed raw. `model.delete` is decoded with `confirm == model_id` enforced by the agent. No decoder exists for survival, online, PIN, token, enroll, profile, content delete or any command.
- **Run.** The handler's context ends at the type's hard cap (lease × 10; `model.load` 600 s; `agent.update` 900 s), measured from the start of the run and independent of the claim loop's own context, so stopping the loop never cancels a model operation. A panic in a handler is recovered and settles `LAI-AGT-099`. An error becomes `failure "<code>: <message>"`: a typed error of the operation keeps its doctor code and plain-language message (`router.OpError`, `*api.Error`), a `LAI-AGT-022` busy answer of a delete, a key rotation or a service restart is retried every second for up to 60 s (§4.9; load, unload and default pass `LockWait: router.DefaultLockWait` to the orchestrator), an error without a code is `LAI-AGT-099` with a generic text (the raw text may name paths and stays in the agent log). A result over 1 MiB or one the cloud answers `400` to settles `LAI-AGT-099` / `LAI-CLD-012` instead.
- **Lease.** The renewal loop starts before the run and is awaited before the settle, so no renewal follows a result. A `409` stops it (`LAI-CLD-059`) and `leaseLost` is noted; the operation finishes and its outcome is offered to the cloud once. Renewal failures count (`leaseRenewFailures`) and the next tick tries again; a `401` goes to `Link.NoteCommandAuth`.
- **Settle exactly once.** A per-command latch is taken by whoever settles first: the run, the policy or decode refusal, or the stop path (`LAI-CLD-063`); the loser drops its outcome. Retries (1 s, 5 s, 15 s) happen only for a request that got no answer, a `429` or a `5xx`; every other answer ends the command (`409` is never retried, and a `401` also tells the link). The failure text is scrubbed (`logx.ScrubString`) and cut to 2000 characters. `mode.offline` returns its result with an `After` step: the result is settled first and only then the offline transition runs (the transition stops the link, whose claim part does not wait for a settled command).
- **`inflight.json`** is a versioned file (`{"version": 1, "jobs": [...]}`, root 0600, written atomically) holding every command in flight, each entry `{job_id, type, plane, claim_token?, lease_expires_at, started_at}`; the lease is refreshed on each renewal and the entry removed when the command settles. The claim token is the cloud's lease handle for that one command and nothing else is a credential; it is never logged and never part of `GET /cloud`'s in-flight record (`{job_id, type, plane, started_at, lease_expires_at}` of the oldest command, ties broken by id). A file that cannot be written does not stop a command, except one that says it needs the file (`agent.update`), which is refused with `LAI-AGT-099`. At the first start of the loop in a process each entry is handled once: entries for which `ClaimerDeps.Recover` returns true are left to the hook, an `agent.update` entry without a hook stays untouched (AGENT-19 owns it), an entry whose lease may still be valid is failed with `LAI-AGT-017` (never re-run) and any other entry is dropped; an unreadable file is discarded.
- **Stop** (§4.8.8): `Claimer.Stop(ctx)` cancels the loop, then waits up to 10 s (injectable clock) for every command that has not settled; for those still running it settles `LAI-CLD-063` (3 s) and cancels the ones whose handler is `Cancellable()` (status refresh, ark sync and verify, model download, logs, `mode.offline`'s own pre-settle work); a model load, unload, delete, key rotation, service restart or catalog refresh carries on locally and its late outcome is dropped. A `ctx` that ends first returns its error and leaves the commands alone.
- **Audit and counters.** Every command that reaches a decision writes one `cloud.job` line (`target.type`, `result` ok, fail or denied, the doctor code, `actor {kind: cloud, job_id}`); operations the handler calls write their own lines with the same actor. `claims`, `succeeded`, `failed`, `emptyPolls`, `rateLimited`, `claimFailures`, `leaseRenewals` and `leaseRenewFailures` feed the heartbeat counters.
- **Seam for AGENT-19.** `jobs.Deps.AgentUpdate` (`jobs.AgentUpdater{Update(ctx, cloud.Job, version) (cloud.Outcome, error)}`) is the executor of `agent.update`; without it the type fails with `LAI-AGT-023`. A successful update returns `Outcome{Defer: true}`: the claim loop settles nothing, keeps the inflight entry and audits `deferred`; the new process settles it after its health check with `Claimer.SettleResult` or `SettleFailure` (they use the persisted plane and claim token, remove the entry whatever the cloud answers and return `ErrNotLinked`, leaving the entry, while the link cannot talk). `ClaimerDeps.Recover` is the startup hook that takes such entries over.
- **Executors** call the same internal methods as the routes, with `audit.Cloud(jobID)`: `router.Orchestrator.LoadWith/UnloadWith/SetDefault`, `modelsapi.Service.Get/Download/Delete/RotateKey` (`Delete` always with `KeepLast: true`; `SYN-044` and a loaded model become `kept_roots` reasons instead of failures), `api.ServicesDeps.RunAction`, `statusapi.Deps.RefreshCatalog` (a `LAI-CAT-002` lint failure is the result `accepted: false`, not a failure), the journal reader, the mode engine and the ark engine's start calls (`jobs.Ark`, `StartSync`/`StartVerify`; a run already active answers `started: false` with its id; a dry run answers `started: false` and `sync_run_id: "dry-run"`). `jobs/assert.go` pins the real types to the interfaces. `model.download` answers `already_present` when the model is verified on every named root (the internal root when none is named); `model.load` reads `vram_used_mib` from a fresh hardware sample and `is_default` from the status. Each result is checked in the tests against the cloud's result schema (`cloud/fake.ValidateResult`).

### 4.9 Concurrency model and locks (§4.12 covers shutdown)

| Lock | Protects | Holders | Contention behaviour |
|---|---|---|---|
| `modeMu` | mode transitions | mode engine | `409 LAI-AGT-022` (survival preempts) |
| `modelOpsMu` | router load/unload/default/delete/key rotate/llama restart/build switch | router orchestrator, `survival_models`, key rotate, service restart llama | non-blocking try; busy → `409 LAI-AGT-022` with the holder's `op_id`. Cloud jobs wait up to 60 s for it before failing |
| sync run lock | one sync/verify/import at a time | sync engine | `409` (`LAI-SYN-*`, ark-sync.md) |
| state store | `state.json` | single writer goroutine; readers get copies | — |
| config | `node.yaml` | `config.Store` (RWMutex) | — |

No lock is held across an HTTP call to the cloud.

### 4.10 Self-update (`internal/update`)

Triggered only by `sudo aictl update [--version X]` (root; the CLI runs steps 1–8 **in its own process** with the same `internal/update` code, so no API route is needed) or by the cloud job `agent.update` (the agent runs the same steps). Never automatic.

Release layout (GitHub release of `update.repo`, tag `node-v<semver>`, [overview.md](../architecture/overview.md) §3.8):
```
aiagent-linux-amd64
aiagent-linux-arm64
SHA256SUMS                 "<sha256>  aiagent-linux-amd64\n<sha256>  aiagent-linux-arm64\n"
SHA256SUMS.minisig         minisign signature of SHA256SUMS
```
The same release also carries `install-node.sh` (version and public key stamped in) and `local-ai-node-v<semver>-src.tar.gz` (agent source for the rebuild kit); `SHA256SUMS` lists them too, one line each, so every consumer can run `sha256sum -c --ignore-missing`. Self-update reads only the lines it needs ([rebuild-kit.md](rebuild-kit.md) §3.2, [release-signing.md](../architecture/release-signing.md)).

Steps:
1. **Resolve.** `GET https://api.github.com/repos/<repo>/releases/tags/node-v<version>` or, without a version, `GET …/releases?per_page=30` and pick the highest semver tag `node-v*` (`prerelease: false` unless `update.channel: prerelease`). Refuse a version ≤ current unless `allow_downgrade` (`LAI-AGT-015` info "already up to date"). 30 s timeout. Offline/survival mode, or no internet → refused with `LAI-AGT-023`. There is no HTTP route for update: `aictl update` prints the code and exits **6** ([aictl.md](aictl.md) §3.4, §4.27); a cloud `agent.update` job fails with `LAI-AGT-023`.
2. **Download** `SHA256SUMS`, `SHA256SUMS.minisig` and the binary for `runtime.GOARCH` into `/var/lib/localai/update/<version>/` (root 0700), with resume and a 200 MiB cap.
3. **Verify signature.** Parse the minisign signature (untrusted comment, base64 sig with algorithm `Ed` or `ED`, key id, trusted comment, global signature). The key id must match one of the **embedded public keys** (`internal/update/keys.go`, a list so keys can be rotated across releases). Verify with `crypto/ed25519` (for `ED`, over the BLAKE2b-512 hash of the file, `golang.org/x/crypto/blake2b`) and the global signature over `sig || trusted_comment`. Failure → `LAI-AGT-010` (critical), delete the staging dir.
4. **Verify checksum** of the binary against its `SHA256SUMS` line. Failure → `LAI-AGT-010`.
5. **Smoke test** the staged binary: `<staged> version --json` (5 s). It must report the expected version and arch.
6. **Install:** copy current binary to `/opt/localai/bin/aiagent.prev` (overwrite); write staged → `/opt/localai/bin/aiagent.new` (0755), fsync, `rename` → `/opt/localai/bin/aiagent`.
7. **Arm rollback:** write the marker `/var/lib/localai/update/pending.json` = `{from, to, started_at, deadline: now+120 s, boot_attempts: 0, job?}` (root 0600, fsync); then start a transient timer:
   `systemd-run --unit=localai-agent-rollback --on-active=120s --timer-property=AccuracySec=1s /opt/localai/bin/aiagent.prev update-rollback-check`
8. **Restart self:** settle nothing yet (a cloud job keeps its lease; the inflight file has the claim token); `systemctl --no-block restart localai-agent.service`.
9. **Post-update health check** (new process, startup step 15): increments `boot_attempts` in the marker; requires within 60 s: both listeners up, state store writable, `/health` from itself, router reachable or at least the llama unit unchanged in state, cloud register OK **if** the link was linked before (else skipped). Success → `state.update.last = {result: ok, …}`, delete the marker, `systemctl stop localai-agent-rollback.timer`, audit `agent.update.ok`, settle the cloud job (`result {"from_version": "0.1.0", "to_version": "0.2.0", "status": "updated"}`, the [cloud-app.md](cloud-app.md) §3.7 shape) using the persisted claim token, emit `update.state`.
10. **Rollback** (by the new process on failed health, by `update-rollback-check` when the timer fires and the marker still exists, or when `boot_attempts ≥ 3`): copy `aiagent.prev` → `aiagent.new`, `rename` → `aiagent`, rewrite the marker as `{rolled_back: true, …}`, `systemctl restart localai-agent.service`. The restored old agent sees `rolled_back: true` in the marker, records `state.update.last = {result: rolled_back}`, deletes the marker, raises `LAI-AGT-009` (error), settles the cloud job as failure (`LAI-AGT-009`; the restored agent reports `{from_version, to_version, status: "rolled_back"}` in the failure details) and emits `update.state`.

The previous binary is always kept as `aiagent.prev` so `cp` recovery stays trivial (ADR-0001).

**Details (AGENT-19, `internal/update`, `internal/cloud/jobs/agent_update.go`).** What the code does where the steps above leave a choice:
- **Library.** `update.Runner` has every outside dependency as a field (`Source` with the HTTP client, `FS`, `Exec` (execx), `Clock`, `Mode`, `Audit`, `Events`, `State`, `Alerts`, `Probe`). `Check` (steps 0 and 1), `Update` (steps 0 to 8), `Startup` (steps 9 and 10, new or restored process), `RollbackCheck` (the timer) and `WaitForUpdate` (the CLI waiting for `/health`) are what the agent job and `aictl update` call; `update.VerifyDir(fs, dir, keys, opts)` is the offline check of `aictl verify-release` and the kit verification, `update.Verify(keys, sigFile, msg)` the single minisign verifier, `update.ParseSums` the strict `SHA256SUMS` parser. `internal/update/updatetest` holds the test doubles (runtime-generated signer, signed release, fake GitHub over TLS); no key material is committed.
- **Step 0, mode.** An update never runs unless the mode is `online` (`LAI-AGT-023`, nothing is requested); a `Runner` without a `Mode` source refuses (`LAI-AGT-099`). A reachable-network failure on the API request is `LAI-AGT-023` too (no internet), a certificate that does not verify is `LAI-AGT-009`.
- **Step 1.** The version is a strict parse (`1.2.3` or `1.2.3-rc.1`, no `v`, no build metadata, no leading zeros) and the tag is `node-v<version>`; the version of the answer must equal the one asked for. A malformed version is `LAI-AGT-020`, a version that was not released (or is a draft) `LAI-AGT-021`, an update already pending (marker present, not older than its deadline plus 10 minutes) `LAI-AGT-022`. The same version as the running one is always `already_current` (reason `same_version`); a lower one is `already_current` (reason `downgrade_refused`) unless `update.allow_downgrade`. An explicit version may be a pre-release whatever the channel says. A running version that is not a semver (a development build) counts as older than any release.
- **Step 2.** Only `https` addresses, built from the validated `update.repo` and the parsed tag (never from an address in an API answer); redirects only to `github.com`, `api.github.com` and `*.githubusercontent.com`, only over https, at most five. Caps: `SHA256SUMS` 1 MiB, the signature 4 KiB, the binary 200 MiB (also for a chunked answer). A file arrives as `<name>.part` and is renamed when complete; an interrupted download keeps its `.part` and the next run resumes it with `Range` (a server that answers 200, 416 or a `Content-Range` that does not start where asked makes the download start over). The staging directory is `0700`; staging directories of other versions are removed at the start of a run and the staged copy after a successful install. A release that does not list one of the three files, or does not serve it (404), is `LAI-AGT-010`.
- **Steps 3 to 5.** Nothing of `SHA256SUMS` is read before the signature has verified. With no key embedded (`agent/internal/kit/keys/minisign.pub` not committed yet) every update fails closed with `LAI-AGT-010`. The embedded list is every `minisign*.pub` file of that directory, so the bridge key of a rotation is a second file ([release-signing.md](../architecture/release-signing.md) section 5). A failed signature or checksum deletes the staging directory. A staged binary that does not run, exits non-zero, reports another version, architecture or OS, is refused with `LAI-AGT-009` before anything is replaced (the installed agent is untouched, so "the previous version" is still running).
- **Steps 6 to 8.** The installed agent must be a regular file. `aiagent.prev` is written through `aiagent.prev.tmp` and renamed, `aiagent.new` is `0755` and fsynced before it is renamed over `aiagent`. Arming first stops a rollback timer left by an earlier update (`systemctl stop localai-agent-rollback.timer` and `.service`, failures ignored), then runs the `systemd-run` argv above. If the guard cannot be armed, or `systemctl --no-block restart localai-agent.service` is refused, the previous binary is put back, the marker deleted and the update fails with `LAI-AGT-009`: a new binary never runs without its guard. The restart is `--no-block` so the agent is not waiting for its own death; no llama unit is touched.
- **Marker.** `pending.json` also carries `reason` (`health`, `boot_attempts` or `timer`) once it says `rolled_back`; a marker that cannot be parsed is removed (by `Update` and by the new process), and by `update-rollback-check` it restores `aiagent.prev`.
- **Step 9.** `Startup` runs the probe (`Runner.Probe`, wired by AGENT-20: listeners, state store, `/health`, llama unit, cloud register) every 2 s for at most 60 s on the injected clock; `boot_attempts` is incremented and written before the probe, so a crash inside it counts; the third attempt rolls back without probing. A process whose version is not the marker's `to` while the marker is live and not `rolled_back` (a manual `cp aiagent.prev aiagent`, an update that did not take) is treated as the restored agent. A shutdown during the check decides nothing (marker kept). `Startup` does not settle the cloud command: it records `state.update.last`, deletes the marker, stops the timer, audits (`agent.update.install` when the restart is asked for, then `agent.update.ok`, `agent.update.rolled_back` or `agent.update.fail`), emits `update.state` (`checking`, `downloading`, `verifying`, `installing`, `restarting`, `ok`, `rolled_back`, `failed`) and raises `LAI-AGT-009` (restored agent), and returns the decision with the job of the marker. `jobs.AgentUpdate` settles from it: `PostStart` (startup step 15) and `Recover` (`ClaimerDeps.Recover`) both end in one settle per command through `Claimer.SettleResult` or `SettleFailure`, retried every 15 s while `ErrNotLinked`, until the lease is a minute past its end (the entry then stays for the next start). `Recover` waits (up to 3 minutes of the injected clock) for the decision; with none it uses `state.update.last` when it is newer than the entry (a crash after the outcome was recorded), else the command fails with `LAI-AGT-017`. The failure text of a rollback ends with the details `{"from_version","to_version","status":"rolled_back"}` as compact JSON, because the failure body of the command plane carries one text.
- **Step 10.** `update-rollback-check` (`aiagent.prev update-rollback-check`, run by the timer) prints `none` and exits 0 without a marker, restores and restarts with a marker (`rolled_back`), or only asks for the restart again when the marker already says `rolled_back` (`restart_retried`); exit 1 with the doctor code when it cannot.
- **`aictl update` (AICTL-09)** runs `Check`/`Update` with `Mode` read from the agent or `state.json`, `State` nil (the CLI never writes `state.json`), `Actor` `audit.CLIRoot()`, then `WaitForUpdate(ctx, update.FetchHealth(nil, base), from, to, 120s)`: `WaitUpdated` (`/health` reports the target version and `starting: false`), `WaitRolledBack` (the old version answers again and the marker is gone: `LAI-AGT-009`), or `LAI-AGT-009` on timeout.

### 4.11 Logging and audit

**Logging.** `log/slog` with `slog.NewJSONHandler(os.Stdout)` (journald captures stdout; `SyslogIdentifier=aiagent`). journald adds the timestamp, so the handler omits `time` (`ReplaceAttr`). Standard keys:

| Key | Meaning |
|---|---|
| `level`, `msg` | slog |
| `component` | `api`, `modes`, `router`, `sync`, `cloud`, `services`, `hardware`, `update`, `doctor`, `auth`, `audit` |
| `req_id`, `op_id`, `job_id` | correlation |
| `code` | doctor code when logging a problem |
| `model`, `unit`, `mode`, `target`, `step`, `root`, `item` | domain |
| `duration_ms`, `http_status`, `method`, `path` | timing / access log (access log at `debug` except mutating routes at `info`) |
| `principal` | `anonymous`, `operator:bearer`, `operator:session`, `operator:uid=1000`, `admin:uid=0`, `admin:pin`, `cloud`, `system` |

**Scrubbing.** A wrapping `slog.Handler` (`logx.Scrub`) rewrites every string attribute and the message:
- keys matching `(?i)(^|_)(token|key|apikey|secret|pin|password|passwd|credential|authorization|cookie|grant)(_|$)` → value replaced by `redact(value)`. The match is on **whole snake_case words** (so `pinned`, `spinner`, `monkey`, `keyboard` are not redacted), and these keys are exempt even when a word matches: keys ending in `_synced` (e.g. `key_synced`, `cloud_synced`), keys containing `fingerprint` (e.g. `api_key_fingerprint`), and keys ending in `_sha256` (subject to the 64-hex rule below). Keys are compared after lower-casing and replacing `-`/`.` with `_`;
- any substring matching `(lat|nod|lai|pat|pg)_[A-Za-z0-9_-]{20,}` → `<prefix>_…<last4>`;
- `Bearer\s+\S+` → `Bearer …<last4>`;
- 64-hex `\b[0-9a-f]{64}\b` → `<first4>…<last4>` (the llama key format; sha256 digests are logged under keys ending in `sha256`, which are exempt **only** when the value is not equal to the current key);
- addresses in the Tailscale CGNAT range (100.64/10, [security-model.md](../architecture/security-model.md) §5) → `<tailnet-ip>`;
- the tailnet name (`tail[0-9a-f]{6}`) → `<TAILNET>`.
The same function scrubs `/logs` output and `logs.tail` results. A table-driven test feeds every credential type through it.

**`internal/logx` API (AGENT-04).**
- `logx.New(w io.Writer, level *slog.LevelVar) *slog.Logger` builds the JSON handler (no `time` key) wrapped in `logx.Scrub`. It stores the initial level, from env `LOCALAI_LOG_LEVEL` (`debug|info|warn|error`, default `info`; an invalid value falls back to `info` and logs one warning), into `level`; `logx.SetLevel(level, name)` applies `logging.level` on hot reload and refuses an invalid name without changing anything.
- `logx.ScrubString(s)` applies the substring rules (idempotent, returns `s` itself when nothing matches, about 1 µs for 200 bytes). `logx.Redact(v)` renders `prefix…last4` (a value of 8 characters or fewer becomes `…`). `logx.SensitiveKey(key)` is the key rule. `logx.SetCurrentKey(hex)` tells the scrubber the live llama key.
- The key rule redacts a value of **any kind** (an integer under `pin` too), as `redact(fmt of the value)`. String values, error values, byte slices and the strings inside structured values (a struct or map is serialized to JSON, so the key rule also reaches its fields) go through the substring rules.
- The `*sha256` exemption skips only the 64-hex rule, and never for a value that **contains** the live key (not only one equal to it), so `sha256:<key>` is still scrubbed.
- A `Bearer` match ends where another `Bearer` word starts, so `Bearer a;b=Bearer c` never leaves the second credential in clear (the plain `\S+` would swallow it).
- Scrubbing does not add boundaries the rules above do not have: `localai_` followed by 20 or more token characters matches the token rule, by design (a false positive costs a log line, a miss costs a secret). Callers that scrub a whole journald JSON line (`/logs`) should scrub the decoded string fields, not the raw line, because a `Bearer` match runs to the next whitespace and may eat a closing quote.

**`internal/audit` API (AGENT-04).**
- `audit.Open(path, opts...) (*Log, error)` always returns a usable `*Log`. On an open failure the error is an `*audit.Error` with `Code == "LAI-AGT-013"`, the log is degraded (journald only), and every later `Write` retries the open, so it recovers by itself when space returns. `Log.Err()` is that failure, or `nil` while healthy; the doctor (AGENT-11) reads it to raise or clear `LAI-AGT-013`.
- `Log.Write(audit.Event) error` mirrors the record to slog (`component=audit`, level `info`, or `warn` when `result` is not `ok`), then appends one line with one `write(2)` and an `fsync`. A write or fsync failure returns an `*audit.Error` (`LAI-AGT-013`); the record is already in the journal. A partial line left by a full disk is never glued to the next record (the next write starts with a newline).
- Every record has every key of the example above (`code`, `uid`, `job_id`, `op_id` and `via` are `null` when absent). `at` is UTC with one-second precision. Free-text fields are scrubbed and capped at 256 bytes, a `target` value under a secret-looking key is redacted, and a record over 4096 bytes loses its `target` (it then carries `{"truncated": "..."}`), so a line is never cut and never exceeds one atomic append.
- Rotation (agent only) shifts under the write lock: remove `.5`, rename `.4` to `.5` ... `audit.jsonl` to `.1`, reopen. A failed rotation is logged with `LAI-AGT-013` and retried after one minute; appending continues. `audit.WithoutRotation()` is the in-process `aictl` writer: it never renames or removes anything.
- Helper constructors for the actors: `Anonymous()`, `Portal()`, `Operator(via)`, `Admin(via)`, `Cloud(jobID)`, `System()`, `CLIRoot()` (uid 0), with `WithUID`, `WithJobID` and `WithCredential(secret)` (sets `fingerprint` to `logx.Redact(secret)`).

**Audit log.** `/var/lib/localai/audit.jsonl`, root 0600, append-only (`O_APPEND|O_WRONLY|O_CREATE`), one JSON object per line, fsync after each write; also mirrored to slog with `component=audit`. Rotated at 10 MiB to `.1` … `.5`. **Writers:** the agent, and root-only `aictl` commands that run in-process without the agent (`pin set`, `token rotate`, `rebuild-*`, `update`, `backup-config --restore`, `catalog use|restore`). A CLI writer opens the file with `O_APPEND|O_WRONLY|O_CREATE` (0600) and writes each record with **one `write(2)` of the whole line** (≤ 4 KiB, so appends from the two processes never interleave), then fsyncs. Only the agent rotates; a CLI writer never renames the file (if rotation happens between its open and write, its line lands in `.1`, which is acceptable).

`actor.kind` ∈ `anonymous`, `operator`, `admin`, `portal`, `cloud`, `system`, `cli-root` (an in-process `aictl` command run as root, with `uid: 0` and no agent principal).
```json
{"at": "2026-11-02T15:04:05Z", "action": "key.show", "result": "ok", "code": null,
 "actor": {"kind": "operator", "via": "bearer", "uid": null, "job_id": null, "fingerprint": "lat_…9xQz"},
 "target": {"model": null}, "op_id": null, "req_id": "req_4f2k9m1q8z0c"}
```
Audited actions: `key.show`, `key.rotate`, `model.load`, `model.unload`, `model.delete`, `model.set_default`, `mode.change`, `pin.verify.ok`, `pin.verify.fail`, `pin.set`, `token.rotate`, `ui_code.mint`, `session.create`, `cloud.enroll`, `cloud.unenroll`, `cloud.job` (every executed job with type and result), `profile.set`, `agent.update.*`, `service.*`, `catalog.refresh`, `catalog.restore`, `catalog.use_test`, `config.backup`, `config.restore`, `migration.*`, `llama.build.switch`, `drill.*` (`drill.start`, `drill.abort`, `drill.record_manual`).

### 4.12 Graceful shutdown (SIGTERM / SIGINT)
1. `sd_notify("STOPPING=1")`; set `stopping`; new mutating requests → `503 LAI-AGT-024` (`POST /mode/survival` is still accepted and recorded as a pending transition in `state.json`, so it runs at next start).
2. Stop periodic workers (prober, reconciler, doctor ticks, hardware sampler).
3. Cloud link: stop per §4.8.8 (shutdown variant), max 13 s.
4. Mode transition in progress: let the current step finish, max 10 s; the recorded step stays in `state.json` for resume.
5. Sync: cancel gracefully (journal flushed, `.part` kept), max 10 s.
6. Model operation in progress: do not abort the router call; the router keeps running independently. `state.models.last_load` keeps `result: "interrupted"`.
7. SSE: `Server.Stop` closes the `Stopping()` channel, every open `/events` stream writes `event: shutdown` and returns (a client that stopped reading is cut off by its 10 s write deadline), then `http.Server.Shutdown` with 5 s; then close the socket listener and remove the socket file. `Bus.Close()` runs after the server stopped, so no subscriber goroutine outlives the process's shutdown.
8. Flush state store and audit log. Exit 0.
Total budget 25 s (< `TimeoutStopSec=30`). Child services (llama, kiwix, whisper) are **not** stopped: inference survives agent restarts.

### 4.13 Doctor framework (`internal/doctor`)
- A **check** is `{id: "llm.router_health", area: "LLM", fast: bool, timeout, run(ctx) []Result}`. Results carry a code from the code table (`internal/doctor/codes.go`: code → severity, title/fix in `en` and `es`), plus `details`.
- Fast checks run every 60 s and drive **alerts**: a problem result raises (or keeps) the alert; a pass clears it (`alert.raised`/`alert.cleared`). Slow checks run only on `GET /doctor`, `aictl doctor` and the drill.
- `codes.go` is the machine copy of the registry; `scripts/check-doctor-codes.sh` (CI) asserts every code used in Go exists in [doctor-codes.md](doctor-codes.md) and in the owning spec's doctor-codes section (§8 in most specs, §7 in [catalog.md](catalog.md); the table in [doctor-codes.md](doctor-codes.md) §1.3 names each), and vice versa. `codes.go` holds **every registry code of every area** (doctor-codes.md §1.6 rule 4), not only the agent's.
- A check that exceeds its timeout or panics (recovered by the runner) produces one result `LAI-AGT-099` (error) with `details.check` (the check id) and `details.reason` (`timeout` or `panic: <first line>`); the other checks still run and the run never fails as a whole.

#### 4.13.1 `internal/doctor` implementation (AGENT-11)
- **Layering.** `internal/doctor` imports only `internal/events` and `internal/state`; every other package (`config`, `catalog`, `audit`, `auth`, `cli`, `hardware`, `api`) imports it for its codes, so it cannot import them back. Their `Code…` constants are **aliases** of the doctor constants (`config.CodeNodeYAMLMissing = doctor.AGT001`; the constant name is the area and the number). `internal/doctor/codes.go` is the one file that spells a `"LAI-…"` literal. Tests that need a code outside the table build it (`"LAI-" + area + "-998"`), because the script rejects an unknown literal anywhere under `agent/`.
- **Code table.** `doctor.Lookup(code) (Code, bool)`, `doctor.Text(code, lang) (title, fix string, ok bool)` (`lang` is `en` or `es`, anything else is English), `doctor.Codes()`, `doctor.Emittable(code)` (false for a retired code or one outside the table). Each entry has `ID`, `Severity` (the default; `Retired` for a retired code), `Title` and `Fix` as `Msg{EN, ES}`. The title is the registry meaning and the fix the registry fix; the title stays at most 120 characters, so for the two registry rows over that limit (`LAI-CAT-013`, `LAI-CAT-018`) the title in `codes.go` is a shortened form (the registry text is unchanged). `codes_test.go` asserts: every registry code is present with the same severity, titles at most 120 characters, a non-empty `es` text different from the English one for every entry, and that the Spanish fix keeps every backticked command of the English one.
- **Check.** `doctor.Check{ID, Area, Fast, Timeout, Run}`; `ID` is `<area>.<name>` in lower case, `Area` one of the area prefixes. `doctor.Register(Check)` panics on a bad or duplicate check (it adds to `doctor.DefaultRegistry`); `Registry.Add` returns the error. `Result{Check, Code, Severity, Title, Fix, Details}`: a result without a `Code` is "ok". The runner fills what is empty: the check id, the default severity of the code, and `Title`/`Fix` from the table in the language of the run. A check sets `Severity` to escalate or relax (`LAI-AGT-003` is critical when defaults were used).
- **Runner.** `Registry.Run(ctx, RunOptions{Areas, FastOnly, Budget, Lang})`. At most 4 checks run at once (`MaxConcurrent`); each gets its own context with its timeout (default 10 s). A check that times out, panics (`details.reason: "panic: <first line>"`) or returns a code outside the table (`details.reason: "unknown code <code>"`) becomes one `LAI-AGT-099` result with `details.check`; the others still run. A check that ignores its context is **abandoned** at its timeout and its slot is freed, but it is not started again while the abandoned run is still going: until it returns it keeps reporting `LAI-AGT-099` `timeout`, so a stuck check cannot pile up goroutines.
- **Alert manager.** `doctor.NewManager(ManagerOptions{Registry, Bus, Interval})` implements the app worker interface (`Start` runs a pass at once, then every `Interval`, default 60 s; `Stop` cancels a pass in progress, which then changes nothing). `Tick(ctx)` is one pass of the **fast** checks. A problem result raises the alert of its code (`alert.raised`); an alert already active is kept without a new event (details refresh, `since` stays) unless its severity changed, which publishes `alert.raised` again with the new severity; a code no check reports any more clears (`alert.cleared`). Several checks may report one code: it stays while any does. `Active() []Alert` (worst first, then oldest) serves `/status`, `/portal/status`, the heartbeat and the state snapshot; `Alert.In(lang)` localizes `title` and `fix` (events and `Active()` carry English). Event payloads are `events.Alert` and never carry `details`.
- **Event-driven alerts.** `Raise(code, details, WithSeverity(s)) error` and `Clear(code)` serve alerts that come from an event rather than a check (`LAI-AGT-016`, `LAI-SEC-009`). `Raise` refuses a code that is not emittable (`ErrUnknownCode`) or a bad severity. An alert has two holds, the checks and `Raise`; it stays active while either holds it, so a clean pass never clears an alert raised by hand and `Clear` never clears one a check still reports. Alerts live in memory only: after a restart the first pass derives them again (and event-driven ones are raised again by their source).
- **Built-in checks.** `doctor.AgentChecks(AgentSources{Config, State, Audit})` (and `RegisterAgentChecks`) return fast `AGT` checks for the sources given: `agt.config` (the node.yaml condition `LAI-AGT-001`/`002`/`LAI-CAT-003`, with `details.problems[]`, and `LAI-AGT-016` for pending restarts), `agt.state_writable` (`state.KindUnwritable` → `LAI-AGT-004`), `agt.state_intact` (`state.KindDamaged` → `LAI-AGT-003`, severity from the finding: critical when defaults were used), `agt.audit_writable` (`audit.Log.Err() != nil` → `LAI-AGT-013`). `config.Store.DoctorStatus()`, `state.Store.Findings()` and `audit.Log.Err()` are the sources. Other issues register their checks with `doctor.Register` from the package that owns the domain (the hardware service's `Checks()` is wrapped into `doctor.Check`s by AGENT-20).
- **Wiring (AGENT-20).** `mgr := doctor.NewManager(doctor.ManagerOptions{Bus: bus})` and `doctor.RegisterAgentChecks(nil, sources)` at startup step 16; `api.RegisterDoctor(reg, api.DoctorOptions{})` with the route registry; `mgr.Stop` with the other periodic workers (§4.12 step 2); `mgr.Raise`/`Clear` from the config reload (`LAI-AGT-016`) and the secret-file permission fix (`LAI-SEC-009`).

### 4.14 OpenAPI-first workflow
1. A change to any `/api/v1` route starts with an edit to **this spec** (§3.8) and to `agent/api/openapi.yaml` (OpenAPI 3.1) in the same PR. The YAML is hand-written; it is the wire contract.
2. Go request/response types are hand-written in `internal/api/types.go` with `json` tags that mirror the schema names.
3. Conformance test `internal/api/openapi_test.go`:
   - loads `openapi.yaml` (embedded by `agent/api/spec.go`, parsed with yaml.v3), walks the registered mux routes (`api.Routes()` returns method+pattern+auth level) and fails on any route missing from the spec or any spec path missing from the mux. A third side is the route table of §3.8: the test parses it (a method column such as `POST/GET/DELETE` or `GET ; POST`, a path column such as `/models ; /models/{id}`, `/cloud ; POST /cloud/enroll`, a brace group `{eject\|init\|mount}`, notes in parentheses, one auth value or one per route) and fails when its routes or auth levels differ from the document, so the table keeps that notation. Routes outside `/api/v1` (`Route.Absolute`: the SPA, `/portal`, `/chat/`, `/library/`, `/maps/`, `/docs/`) are not in the document. The test also compares, for every registered route, the timeout (when `x-localai-timeout` is an exact `N s`, `N min` or `none`), `GateExempt` (`x-localai-gate-exempt: true`) and the request media types;
   - asserts the `x-localai-auth` extension on each operation equals the route's auth level. The value is one level (`none|op|portal|admin|admin*`) or a **list** for routes that accept several principals (`[op, portal]` for `POST /models/{id}/load`, `GET /events`, `/speech/*`); an optional free-text `x-localai-auth-note` documents restrictions the level cannot express (e.g. `POST /mode`: "admin* to leave survival"; Portal restrictions per [survival-portal.md](survival-portal.md) §3.3);
   - **planned routes:** an operation marked `x-localai-status: planned` is documented but not implemented yet (it lands in a later issue). The bijection test skips it on the mux side, and **fails** if a route marked `planned` is registered in the mux. The marker is the allowlist of the bijection and it only shrinks: a planned operation names the issue that implements it in `x-localai-issue` (`AGENT-14`, `ROUTER-05`, `SYNC-03`, ...), and **the PR of that issue registers the routes and deletes both `x-localai-status` and `x-localai-issue` from their operations** (`TestPlannedOperations` logs what is still planned, by issue). "Registered" means in `api.Routes()` or returned by a route constructor such as `api.EventsRoute` or `auth.Service.Routes` (which AGENT-20 wires into the server's registry); `internal/api/openapi_wired_test.go` (an external test package, so that it can import the packages that import `internal/api`) lists the constructors in `implementedRoutes`, and a PR whose routes come from a constructor adds it there with fakes for its dependencies;
   - **operation rules:** every operation declares `x-localai-auth`, `x-localai-timeout` (informational) and the headers of §3.7 (`X-Request-Id` on every response, `X-LocalAI-Op-Id` on the 2xx of a mutating one, `Retry-After` on a 429); the error statuses its kind can answer (403 and 500 always, 401 unless `none`, and for a mutating operation 400, 413, 415 and, unless it is gate-exempt, 503), each with `#/components/schemas/ErrorEnvelope`; its path parameters; and, for the one gate-exempt route `POST /mode/survival`, `x-localai-gate-exempt: true`. Every `LAI-...` code the file names exists in [doctor-codes.md](doctor-codes.md). Every `examples:` entry validates against its schema, and the Go wire types of `types.go` mirror the schemas of the same name (a field on one side only fails);
   - every handler test records its response bodies (`apitest.Record`, or `apitest.RecordResult` for a `httptest.ResponseRecorder`); at the end of the test the in-repo JSON Schema validator (`internal/api/schema`; 2020-12 subset: `type`, `properties`, `required`, `enum`, `const`, `items`, `additionalProperties`, `oneOf`, local `$ref`, `format` `date-time` and `binary`, `pattern`, `minimum`, `maximum`, `minLength`, `maxLength`, `minItems`, `maxItems`; any other keyword in the document fails the test) checks each recorded body against the response schema for that status, and that the status is documented (a `405` is answered by routing for any method a path does not serve, so it needs no entry). Error responses (any status of 400 or more, also for a path no operation serves) must match `#/components/schemas/ErrorEnvelope`. `apitest.RecordStream` checks only the status and headers of an SSE response, whose body never ends. In `internal/api` and `internal/auth` the shared test helpers do the recording (`expectError` and `openStream` in `internal/api`, `env.do` in `internal/auth`, which records the documented routes and every error), so a new handler test gets the check by using them, and a package that builds routes does the same in its own helper (`apitest.Serves` tells it whether a request is documented);
4. TS types for the UI, desktop and cloud are generated with `openapi-typescript` into `packages/ui/src/api/agent.gen.ts` (`npm run gen:agent-api`); CI regenerates and fails on a diff. If `packages/ui/` does not exist yet, the first PR that needs it (AGENT-07, DSK-01 or CLOUD-01, whichever lands first) creates the minimal package: `packages/ui/package.json` with `"name": "@localai/ui"` and the `gen:agent-api` script, plus `packages/ui/brand/icon.svg` ([desktop.md](desktop.md) §3.1). CLOUD-11 owns the shared components. AGENT-07 created it (`package.json` with a pinned `openapi-typescript`, `package-lock.json`, `tsconfig.json`, `brand/icon.svg`, the committed `agent.gen.ts`); the `api-types` job of `.github/workflows/agent.yml` runs `npm ci`, regenerates, fails on `git diff` and type-checks the result.
5. Breaking changes (removing/renaming fields, tightening enums) require a new `/api/v2` route or a deprecation window of one minor release in which both shapes are served; additive changes are free.

## 5. Failure handling and edge cases

| Situation | Behaviour | Code |
|---|---|---|
| `node.yaml` missing | Start with safe defaults; Portal and survival work; cloud and Tailscale untouched | `LAI-AGT-001` |
| `node.yaml` edited with a syntax error while running | Ignore the edit, keep last valid config, alert | `LAI-AGT-002` |
| `state.json` corrupt | Use `.bak`; else defaults with `mode: offline` | `LAI-AGT-003` |
| `/var/lib/localai` full or read-only | Keep state in memory; API works; alert critical | `LAI-AGT-004` |
| Port 7070 taken by another program | Socket still works (aictl), UI unavailable, alert | `LAI-AGT-006` |
| Agent killed during `→ survival` at `survival_models` | Resume at that step on start; the model that was loading when it died is skipped | `LAI-AGT-007`, `LAI-LLM-020` |
| A transition step crashes the agent 3 times | Skip the step with a warning, finish the transition | `LAI-AGT-008` |
| tailscaled down during `→ offline` | Nothing exposed; step ok; reconciler off | — |
| `tailscale serve … off` fails while tailscaled runs | Mode still becomes offline; retry every 60 s; critical-looking alert so the operator notices | `LAI-NET-004` |
| Foreign listener on 443 (e.g. someone serves another app) | Do not touch it; do not apply pattern B | `LAI-NET-005` |
| Funnel enabled on our port | Turn funnel off for that port; alert critical | `LAI-NET-009` |
| Cloud credential revoked | `revoked`; local features unaffected; hourly probe | `LAI-CLD-051` |
| Cloud sends unknown job type | `failure` with policy error; nothing executed | `LAI-CLD-057` |
| Cloud sends `model.delete` but `cloud.allow` lacks it | Same as above (EAB will not even offer it: it is not in `eligibleTypes`) | `LAI-CLD-057` |
| Lease lost mid-load (laptop slept) | Load completes locally; settle answers 409 → dropped; snapshot shows the truth | `LAI-CLD-059` |
| Two machines configured as `ai-n3` | Both work locally; cloud row flips; warning | `LAI-CLD-061` |
| Cloud unreachable for hours | Backoff 5 s → 5 min; warning after 5 min; local unaffected | `LAI-CLD-053` |
| Self-update binary signature bad | Abort, nothing replaced | `LAI-AGT-010` |
| New agent binary crashes at start | systemd restarts it; `boot_attempts ≥ 3` or rollback timer at 120 s restores `.prev` | `LAI-AGT-009` |
| System clock far wrong (no RTC battery, offline) | Everything local works; TLS to cloud fails; alert explains | `LAI-AGT-012` |
| `nvidia-smi` hangs | 3 s timeout; last value marked stale; alert after 3 failures | `LAI-HW-002` |
| Browser page from the internet POSTs to `127.0.0.1:7070/api/v1/mode/survival` | Origin check `403`; non-JSON content type `415`; no CORS | `LAI-SEC-007` |
| DNS-rebinding page uses `Host: evil.example` | `403` | `LAI-SEC-006` |
| PIN brute force | 5 / 5 min, then 1 h lockout after 20 | `LAI-SEC-004` |
| Agent token file deleted | Regenerate at next start (or on next read failure); desktop re-reads on 401 | `LAI-SEC-011` |
| Socket peer is a user not in `localai-operators` (perm 0660 normally prevents connect) | `403` | `LAI-SEC-013` |
| SSE client stalls | Disconnected after 256 buffered events | — |
| Two `POST /models/{id}/load` at once | Second gets `409` with the first's `op_id` | `LAI-AGT-022` |
| Agent restarts while llama is loading a model | Router continues; agent reads status on start and re-attaches to the `loading` model for the desired set | — |

## 6. Security considerations
- **Runs as root** by design (overview §3.1). Mitigations: loopback-only listeners; Host/Origin/CSRF checks; strict JSON decoding; no CORS; every exec through `execx` with fixed argv (never `sh -c`), absolute binary paths resolved at start from a fixed `PATH`, argument values validated against allowlists (unit names, model ids `^[a-z0-9][a-z0-9.\-]{2,63}$`, content ids, numeric ranges); path parameters resolved with `filepath.Clean` + root prefix check + `EvalSymlinks` re-check; systemd hardening in §3.6.
- **Secrets**: never logged (scrubber), never in `/status`, heartbeats, events, results or error messages; shown as `prefix…last4`. `GET /key` is audited and `no-store`. The llama key is sent to the cloud only over HTTPS through §4.8.5.
- **Cloud is untrusted** (T4): closed job set, strict params, allowlist, no survival, no shell, no PIN/token/enroll operations, size caps on results.
- **Anonymous routes** (survival exception) are read-only except `POST /mode/survival`, which only moves the node to a *less* exposed state.
- **`/chat/` key injection** is limited to survival mode or a session, loopback only, and blocks model-management paths.
- **Self-update** requires minisign signature + sha256 + smoke test + health-checked rollback.
- **Timing:** token, grant and session comparisons use `subtle.ConstantTimeCompare`.
- **Resource limits:** body caps, SSE buffer cap, piper concurrency cap, `/hardware?fresh` rate limit, log tail caps.

## 7. Observability
- **Logs:** slog JSON to journald, keys in §4.11. `journalctl -u localai-agent -o cat | jq` is the supported way to read them.
- **Events:** §3.10.
- **Metrics:** no Prometheus endpoint in phase 1 (nothing must listen beyond 7070). Counters are exposed in `GET /status` (`node.counters` — added only when `?counters=1`) and in the heartbeat `vitals.counters`/`vitals.extra`: requests by status class, model loads ok/failed, mean load ms per model, transitions, cloud claims/failures, sync bytes.
- **Audit:** §4.11.
- **Doctor:** §4.13, codes in §8.

## 8. Doctor codes

The registry is [doctor-codes.md](doctor-codes.md); the tables below are authoritative for their areas. Severity: `info`, `warning`, `error`, `critical`.

### 8.1 `LAI-AGT` — agent core and config
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-AGT-001 | error | `/etc/localai/node.yaml` is missing | Restore it from the backup: `sudo cp /etc/localai/node.yaml.bak /etc/localai/node.yaml`, or run the installer again |
| LAI-AGT-002 | error | `node.yaml` cannot be read or has invalid values (warning for unknown keys) | Fix the line named in the details, or restore `node.yaml.bak`. The last good settings stay in use until then |
| LAI-AGT-003 | warning (critical when defaults and offline mode were used) | The state file was damaged; the agent used the backup or started in offline mode | Check the mode with `aictl status` and set it again with `aictl mode <mode>` |
| LAI-AGT-004 | critical | The agent cannot write its state (`/var/lib/localai` full or read-only) | Free disk space (`aictl ark status` shows what uses it) and restart the agent: `sudo systemctl restart localai-agent` |
| LAI-AGT-005 | error | The agent socket `/run/localai/agent.sock` could not be created | Restart the agent: `sudo systemctl restart localai-agent` |
| LAI-AGT-006 | error | Port 7070 is used by another program, so the local UI is unavailable | Find it with `sudo ss -ltnp 'sport = :7070'`, stop it, then restart the agent |
| LAI-AGT-007 | info | A mode change was interrupted and has been resumed | Nothing to do |
| LAI-AGT-008 | warning | One step of a mode change failed or was skipped | Open `aictl doctor` to see which part failed. The mode itself is active |
| LAI-AGT-009 | error | An agent update failed and the previous version was restored | Nothing is broken. Report the version in the details to the maintainer |
| LAI-AGT-010 | critical | A downloaded update failed its signature or checksum check and was discarded | Do not install it by hand. Report it to the maintainer |
| LAI-AGT-011 | critical | The agent is not running as root | Start it only through systemd: `sudo systemctl start localai-agent` |
| LAI-AGT-012 | warning | The computer's clock looks wrong | Set the correct date and time in system settings. Offline features still work |
| LAI-AGT-013 | warning | The audit log cannot be written | Free disk space in `/var/lib/localai` |
| LAI-AGT-014 | warning | The agent restarted more than 3 times in the last hour | Run `aictl logs --unit agent` and report the last error |
| LAI-AGT-015 | info | An agent update is available (or: already up to date) | Run `sudo aictl update` when convenient |
| LAI-AGT-016 | info | A settings change needs a restart to take effect | Run `sudo systemctl restart localai-agent` (or `aictl restart llama` when the details say so) |
| LAI-AGT-017 | warning | A cloud command was interrupted because the agent restarted | Run the command again from ai.marin.cr if it is still needed |
| LAI-AGT-020 | error | The request was not valid | Check the command or the value named in the details |
| LAI-AGT-021 | error | The thing asked for does not exist (model, service, content, route) | Check the name with `aictl models` or `aictl status` |
| LAI-AGT-022 | warning | Another operation is already running | Wait for it to finish (`aictl status`), then try again |
| LAI-AGT-023 | warning | This action is not allowed in the current mode | Switch mode first (`aictl mode online`), or add the option named in the details |
| LAI-AGT-024 | info | The agent is starting or stopping | Wait a few seconds and try again |
| LAI-AGT-025 | error | The request or upload is too large | Use a smaller file |
| LAI-AGT-099 | error | Internal agent error | Run `aictl logs --unit agent` and report the request id in the details |

### 8.2 `LAI-NET` — network and Tailscale
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-NET-001 | warning | Tailscale is not installed (online mode cannot publish this node) | Install Tailscale ([runbooks/05-add-a-node.md](../runbooks/05-add-a-node.md)) or set `tailscale.serve_pattern: none` |
| LAI-NET-002 | warning | The Tailscale service is not running | Run `sudo systemctl start tailscaled` |
| LAI-NET-003 | warning | Tailscale is not logged in | Run `sudo tailscale up` and sign in. Local features keep working |
| LAI-NET-004 | error | Tailscale Serve could not be changed for port 8080 | Run `tailscale serve status` and the command in the details by hand, then `aictl doctor` |
| LAI-NET-005 | warning | Another Tailscale Serve listener uses our port, or an extra listener for port 8080 exists | Remove the extra listener with the exact command in the details (e.g. `sudo tailscale serve --tcp=18080 off`) |
| LAI-NET-006 | critical | This node is still published on the tailnet while in offline or survival mode | Run `sudo tailscale serve --https=443 off` and `sudo tailscale serve --tcp=18080 off` |
| LAI-NET-007 | info | The Tailscale Serve listener was missing and has been restored | Nothing to do |
| LAI-NET-008 | info | The tailnet HTTPS certificate is still being issued | Wait one minute and try again |
| LAI-NET-009 | critical | Tailscale Funnel (public internet exposure) was enabled for this node's model port; it has been turned off | Never enable Funnel on AI nodes. Check who changed it |
| LAI-NET-010 | critical | A Local AI service is listening on a network address, not only on this computer | Run `aictl doctor` for the service name and restart it: `aictl restart <service>` |
| LAI-NET-011 | info | No internet for 30 minutes | Everything here still works. Switch to Survival mode if this is an emergency: `aictl mode survival` |
| LAI-NET-012 | info | The internet works but the cloud (ai.marin.cr) does not answer | Nothing to do on this node. Local features work |
| LAI-NET-013 | warning | Names cannot be looked up (DNS), so downloads and the cloud fail | Check the network connection or router |
| LAI-NET-014 | warning | Wi-Fi power saving is on, which makes a laptop server drop off the network | Run the laptop setup again: [runbooks/09-laptop-as-server.md](../runbooks/09-laptop-as-server.md) |

### 8.3 `LAI-HW` — hardware
| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-HW-001 | error | No usable GPU was found, but this node is set up for one | Check that the graphics driver is installed (`nvidia-smi` or `vulkaninfo --summary`). The CPU-only models still work |
| LAI-HW-002 | error | The NVIDIA driver is not answering (`nvidia-smi` failed) | Restart the computer. If it persists, reinstall the driver ([runbooks/06-rebuild-a-node-offline.md](../runbooks/06-rebuild-a-node-offline.md)) |
| LAI-HW-003 | error | The AMD GPU device files are missing (`/dev/kfd`, `/dev/dri/renderD*`) | Restart the computer; check that the `amdgpu` driver loads |
| LAI-HW-004 | error | The `localai` user cannot use the GPU (not in `video`/`render`) | Run `sudo usermod -aG video,render localai` and `aictl restart llama` |
| LAI-HW-005 | warning | Other programs are using a lot of graphics memory | Close games or other GPU programs, or log out of the desktop session |
| LAI-HW-006 | warning | The computer is almost out of memory (RAM) | Close other programs or load a smaller model |
| LAI-HW-007 | warning | Free disk space is below the profile's safety limit | Remove files you don't need, or move content to the ark drive |
| LAI-HW-008 | critical | A disk used by Local AI is more than 95% full | Free space now: delete large personal files or old models (`aictl delete <model>`) |
| LAI-HW-009 | warning | The GPU is very hot (87 °C or more) | Make sure the vents are not blocked and the laptop is on a hard surface |
| LAI-HW-010 | warning | The CPU is very hot (95 °C or more) | Same as above; clean the fans if dusty |
| LAI-HW-011 | warning | The computer is slowing down to cool itself (throttling) | Improve airflow; answers will be slower until it cools |
| LAI-HW-012 | warning | Running on battery | Plug in the charger. A laptop server stops when the battery runs out |
| LAI-HW-013 | warning | Battery below 20% | Plug in the charger soon |
| LAI-HW-014 | critical | Battery below 10% | Plug in the charger now, or save your work |
| LAI-HW-015 | info | The power profile is not "performance" | Run `powerprofilesctl set performance` |
| LAI-HW-016 | warning | The computer is using a lot of swap, which makes everything slow | Load a smaller model or close other programs |
| LAI-HW-017 | error | The configured backend does not match the hardware (e.g. CUDA set, no NVIDIA GPU) | Run `aictl profile suggest` and fix `llama.backend` in `node.yaml` |
| LAI-HW-018 | error | Vulkan only sees a software renderer (llvmpipe), so models run on the CPU | Install the Mesa Vulkan drivers (`mesa-vulkan-drivers`) and restart |
| LAI-HW-019 | error | The GPU's CUDA architecture could not be detected or is not supported by this build | Set `llama.cuda_arch` in `node.yaml` and run `sudo aictl rebuild-llama` |
| LAI-HW-020 | warning | The laptop will sleep when the lid is closed | Run the laptop setup again: [runbooks/09-laptop-as-server.md](../runbooks/09-laptop-as-server.md) |

### 8.4 `LAI-SEC` — security, keys, PIN
security-model.md is an architecture document without a code table; **this table is authoritative for `LAI-SEC`**.

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-SEC-001 | error | Not signed in: the agent token or session is missing or wrong | Use `aictl` as a member of the `localai-operators` group, or sign in with a code from `aictl ui-code` |
| LAI-SEC-002 | warning | This action needs the admin PIN | Enter the admin PIN when asked, or run the command with `sudo` |
| LAI-SEC-003 | warning | Wrong PIN | Try again. After 5 wrong tries you must wait 5 minutes |
| LAI-SEC-004 | warning (critical at lockout) | Too many wrong PIN or login-code attempts | Wait the time shown. Forgotten PIN: `sudo aictl pin set` |
| LAI-SEC-005 | error | No admin PIN has been set | Run `sudo aictl pin set` |
| LAI-SEC-006 | error | Request refused: wrong address (Host header) | Open `http://127.0.0.1:7070` exactly |
| LAI-SEC-007 | error | Request refused: it came from another website | Open the page from `http://127.0.0.1:7070` or the ai-desktop app |
| LAI-SEC-008 | warning | The login code is wrong, used, or expired | Get a new code with `aictl ui-code` |
| LAI-SEC-009 | warning | A secret file had unsafe permissions; they were fixed | Nothing to do. If it happens again, check who changed `/etc/localai` |
| LAI-SEC-010 | critical | The llama API key file is missing or not 64 hex characters | Run `sudo aictl key rotate` |
| LAI-SEC-011 | warning | The agent token was missing and a new one was created | Restart ai-desktop; scripts must re-read `/etc/localai/agent-token` |
| LAI-SEC-012 | info | Your session expired | Sign in again |
| LAI-SEC-013 | error | Your user is not allowed to use the agent | Ask an admin to run `sudo usermod -aG localai-operators <user>`, then log out and in |
| LAI-SEC-014 | critical | llama-server answers without the API key | Run `aictl restart llama`; if it persists, run `aictl doctor` and report it |
| LAI-SEC-015 | warning | The old key path `/etc/llama-qwen/api-key` is not a link to the current key | Run `sudo aiagent migrate llama-qwen` again (it only fixes the link) |

### 8.5 `LAI-CLD-050..069` — agent-side cloud link client
The `LAI-CLD` area belongs to [cloud-app.md](cloud-app.md). The block **050–069 is reserved for the agent's link client** and defined here.

| Code | Sev | Meaning | Fix (plain language) |
|---|---|---|---|
| LAI-CLD-050 | info | This node is not connected to the cloud | To connect it: `sudo aictl cloud enroll --url https://ai.marin.cr` |
| LAI-CLD-051 | error | The cloud rejected this node's credential (revoked or wrong) | Create a new node credential at ai.marin.cr and run `sudo aictl cloud enroll` again |
| LAI-CLD-052 | warning | An operator disabled this node in the cloud | Re-enable it at ai.marin.cr (Nodes page). Local features work |
| LAI-CLD-053 | warning | The cloud cannot be reached right now | Nothing to do; the node retries by itself. Local features work |
| LAI-CLD-054 | error | The cloud refused this node's registration | Check `aictl logs --unit agent` for the details and report them |
| LAI-CLD-055 | info | The cloud is older than this agent; some details are not sent | Update the cloud app |
| LAI-CLD-056 | info | The cloud has no node-state endpoint yet; a reduced status is sent | Update the cloud app |
| LAI-CLD-057 | warning | The cloud asked for an action this node does not allow | Nothing to do. To allow it, edit `cloud.allow` in `node.yaml` |
| LAI-CLD-058 | warning | The cloud sent an invalid command | Report it to the maintainer |
| LAI-CLD-059 | info | A cloud command finished after the cloud stopped waiting for it | Nothing to do; the cloud shows the current state |
| LAI-CLD-060 | warning | The cloud does not have this node's current API key yet | Check the connection; the agent retries every 5 minutes |
| LAI-CLD-061 | warning | Another computer is using this node id | Give each computer its own id in `node.yaml` (`node.id`) |
| LAI-CLD-062 | error | The cloud address must start with https:// | Fix `cloud.url` in `node.yaml` |
| LAI-CLD-063 | info | A cloud command was stopped because the node is going offline | Nothing to do. Run the command again from ai.marin.cr after the node is back online |

`LAI-CLD-062` is reported inside the `LAI-AGT-002` validation report (`details.problems[].code`, §3.4) when `node.yaml` has it. `LAI-CLD-063` is the agent's failure reason for an in-flight cloud command interrupted by `link_stop` (§4.8.8); the cloud-owned `LAI-CLD-034` keeps its [cloud-app.md](cloud-app.md) meaning (refused before enqueueing because the node is offline/survival).

## 9. Test plan

### 9.1 Unit tests (table-driven, `go test ./...`)
| Package | Cases |
|---|---|
| `config` | node.yaml: full example parses; each validation rule (one bad value per case, `path` and `line` checked; `LAI-CLD-062` inside the `LAI-AGT-002` report); unknown top-level key → warning, unknown key inside a block → error; missing file → safe defaults + `LAI-AGT-001`; comment-preserving rewrite round-trip (golden files; `Update` refuses an invalid result and writes nothing; `.bak` holds the previous valid file); hot reload with a fake clock: valid edit applied within one poll, invalid edit ignored and last valid config kept (`LAI-AGT-002`), a touch ignored, restart-only fields (`node.id`, `ports.*`, `cloud.url`) keep the running value and report `LAI-AGT-016`, llama env change reports it with target `llama`, deleted file keeps the config (`LAI-AGT-001`); secret files: a `0644` api-key is flagged, a symlink refused; `recent_starts` restart-loop rule (5 starts in 60 min → `LAI-AGT-014`); atomic write leaves no temp files after a simulated crash between write and rename |
| `state` | Over `state/fake` (failure and kill injection) unless noted. Load: fresh node; valid file; corrupt → `.bak` (warning) → defaults with `offline` (critical) with the corrupt copy kept (name, collision); missing with `.bak` present → `.bak`; missing with a bad `.bak` → defaults; missing or `< 1` `schema_version`, wrong field type, empty, binary, trailing garbage → corrupt; `schema_version: 2` loaded best-effort with warning; unknown `mode` → offline; unreadable file; stray temp files swept. Unknown keys preserved at every level and in `sync`/`drill`/`maps`, numbers byte-exact, `schema_version` not downgraded. Write: `written_at`/`agent_version` set; `.bak` is the previous version (with and without hard links) and survives the first write after a recovery; no-op mutation writes nothing, `MutateSync` does; operation order write, `fsync`, rename, directory `fsync`; a kill after every possible operation of a write (unfsynced bytes dropped on reboot) loads the old or the new state, never defaults, and leaves no temp file; `ENOSPC`/`EROFS`/`EACCES`/`EIO` at each step → state in memory, no error, `LAI-AGT-004`, retry on the next mutation, `Flush` or `Close`; read-only real directory. Concurrency (`-race`): no lost mutation, `Snapshot` shares nothing with the store, group commit with a sync boundary. Real filesystem: round trip, 0640, `.bak`, recovery |
| `auth` | Fake clock, fake group lookup, real temp-dir files with an own-uid policy, and the real `api.Server` (also over real TCP and unix listeners). Bearer ok/wrong/empty/no-prefix/other scheme/truncated/longer; a stale bearer on a `none` route is anonymous; missing token file created with `LAI-SEC-011`, mode 0640, empty/whitespace file regenerated, trailing newline and CRLF tolerated, unsafe mode fixed (`LAI-SEC-009`), symlink refused, create failure reported; token file mtime reload (≤ 10 s) and re-read after a failed compare, unreadable/deleted/emptied file keeps the cached token and logs once, rotation revokes bearer grants; session idle (12 h) and absolute (7 d) expiry at the boundary instants, use restarts the idle timer, expired → `LAI-SEC-012` on a route that needs auth and anonymous on `none`, tombstones, bounded store, delete revokes grants; UI code 6 uniform digits (leading zeros), single use, 5 min expiry, 4th code evicts oldest, expired codes do not count, 5 failures → `429` with `Retry-After` (a correct code too), malformed codes not counted; PIN argon2id with the exact parameters (PHC parsed and re-derived independently), PHC rejection table, malformed PIN still costs one argon2 computation, 5/5 min → `429` before any work, 20 consecutive → 1 h lockout with the critical `LAI-SEC-004` alert and one audit record, success resets the counter, parallel guesses cannot beat the limit, verifications serialized, missing/unusable file → `409` and no failure, file re-read and permissions enforced on every verify; grant format, 5 min fixed expiry, reusable, bound to the principal (session A's grant rejected for bearer B, session B, socket uid B), revoked with the session; socket peer uid 0 → admin, operator group → operator, other and unreadable peer → 403 `LAI-SEC-013` on every route, group lookup cached 30 s and failing closed; audit events `session.create`, `ui_code.mint`, `pin.verify.ok\|fail` (against the real `audit.Log`); no token, cookie, code, grant, PIN or hash in the journal or the audit file |
| `api` middleware | Host header matrix (`127.0.0.1:7070` ok, `localhost:7070` ok, `127.0.0.1` without port 403, `evil:7070` 403, socket ignores Host); Origin matrix incl. `localai://app`, `null`, `http://127.0.0.1:7071`; cookie POST without Origin → 403; text/plain POST → 415; unknown JSON field → 400; body > 1 MiB → 413; OPTIONS → 405 and no CORS headers; the full Host, Origin, CSRF and content-type matrices; request id echo and replacement; panic → 500 with `details.request_id` and a connection abort when the response had started; the mutating-route gate and `/health` while `starting`; unclean paths 404 without redirect; 405 with `Allow`; es localization; strict decode reasons; route deadline; access-log levels; listener tests over real sockets (TCP and unix: peer credentials equal this process, socket `0660`, fake `chown` hook, stale/live/foreign socket file, port in use, both fail, graceful `Stop` with an in-flight request) |
| `api` routes | every route in §3.8: auth level (anonymous/operator/admin/admin*), happy path body validated against openapi.yaml (recorded with `apitest.Record`, §4.14), each documented error status |
| `api` openapi | `openapi_test.go` (§4.14): the document parses and uses only supported validation; refs and examples validate; per-operation auth, status and header rules; doctor codes exist; the §3.8 table, the document and `api.Routes()` are the same set (two negative fixtures: a route on the mux only, a route in the document only; planned-but-registered fails; auth, timeout, gate and media-type mismatches fail); the Go wire types mirror the schemas; the recorded `/health` and every error of the status map conform. `schema` validator: each keyword with a passing and a failing instance, and `CheckSchema` refusing unsupported keywords |
| `api/static` | `SafeJoin` table: ok paths, `..`, encoded traversal (`%2e%2e`, `%2E%2E%2f`, `%5c`, `%00`), NUL and control characters, backslash, absolute, double slash, dotfiles and dot directories, symlink file and directory escapes (absolute, relative, a sibling that shares the root's name prefix), links that stay inside, link to a dotfile, loop, missing root, root that is a link; `ServeFile`: `200`, `206` single, suffix and open-ended ranges, `416` for several or unsatisfiable ranges (envelope headers), `HEAD`, `405`, directory and device are `404`, validators and `304`, content-type table, no descriptor left open; a `.pmtiles` Range over real TCP |
| `api/proxy` | Fake loopback upstreams: `New` panics table (every non-loopback or malformed target); prefix stripping, query and escaping, base path, paths outside the mount; header stripping (client `Authorization`, `Cookie`, `X-Api-Key`, `X-Forwarded-*`, `Forwarded`, names listed in `Connection`), injected header seen upstream and not removable through `Connection`, `Host` pinned; streaming flush per chunk; WebSocket upgrade past the timeouts; body limit `413` (declared, streamed, exact, default 1 MiB, 32 MiB); upstream down, closed or refused → page for browsers / envelope otherwise, `502` and `503`, English and Spanish; slow backend (total and idle timeout `504`, a moving stream is not cut, a stalled one is, the total cap applies to a moving one); `ReadOnly`, `Deny`, `Intercept`; `Set-Cookie`, frame headers, upstream CSP kept, compression pass-through; `Location` rewriting; and a scan proving no injected key, client key, cookie, query token or body text reaches a log or a response |
| `api` pages | error page negotiation and escaping, templates load nothing external; SPA: survival `302`, fallback, `/api/` never shadowed, assets `404` not index, portal CSP and hook, bad build page; `/docs/`: root selection (internal first, skip, per request), CSP on HTML only, folder index, Range, `405`, `/docs` redirect, traversal / encoded / NUL / dotfile / symlink attempts over a raw TCP connection, index endpoint shape, corrupt index; the mount scenarios of §9.2 (6) and (7) over the real server |
| `modes` | Over `modes/fake`, a real state store on `state/fake.MemFS` and a manual clock. The 6 directed pairs: exact step order, events, audit, committed state. Kill-at-every-write: the goroutine is killed before and after each of the synchronous state writes of each pair (88 kill points), the filesystem drops unsynced bytes, a new engine resumes and re-runs exactly the steps from the recorded one (or applies the steady-state policy when none is recorded); a kill mid-write loads the old or the new record, never defaults. 3-attempt skip for every step of every target (and `persist` never skipped), earlier warnings and attempts kept, an unknown step, an invalid target, a second `Resume`. Crash-skip of the `in_progress` model and its accumulation across restarts. Survival preempts offline and online mid-transition after at most one step (durable as `pending_mode`, the waiting caller told it was preempted, a second survival request joins), never refused whatever runs; `409` for any other target; survival queued before `Open` and during shutdown, drained at `Open` under the same op id, a stale `pending_mode` cleared; shutdown lets the current step finish, abandons it after the budget, leaves the record. Survival completes when every step errors, panics or times out (manual clock; a step that ignores its context is abandoned); `persist` is one write (no write ever shows the new mode with a transition, or the old mode with none); an unwritable store keeps the mode in memory with `LAI-AGT-004`; Portal sessions dropped only on leaving survival; `tailscale_off` retry (60 s, stops on success, on going online and on shutdown, never overlaps `On`); `Online()`, `Subscribe` (slow subscriber), `HTTP()`, `Info`; 24 concurrent requests under `-race` never overlap two transitions |
| `api` mode | the three routes through the real middleware and the real engine: shapes validated against `openapi.yaml` (`since` null on a fresh node, `transition` in flight), auth levels, `wait`, `already`, `reapply`, strict body, `409` with the running op id, preempted wait, leaving survival (bearer and session `403` with `pin_required` and a `denied` audit record, socket operator and admin allowed), `POST /mode/survival` anonymous with actor `anonymous`/`portal`, bad Host `403`, bad Origin `403`, wrong Content-Type `415`, queued before the gate opens and while stopping |
| `modes/tailscale` | over `modes/fake.TailscaleCLI` and the fixtures of `testdata/tailscale/` (none, A only, B only, A+B (ai-n3 today), foreign 443, foreign 18080, funnel on, mixed 443, extra listeners): classification; apply and remove produce the exact argv with the fixed `PATH`; `reset`, `down`, `logout` never run; A+B with `serve_pattern: B` is `LAI-NET-005` naming `sudo tailscale serve --tcp=18080 off` and removes nothing; `Off` removes both patterns for every `serve_pattern` and only ours beside foreign ones; a residual listener is `LAI-NET-006`; `manage: false` runs no `serve` or `funnel` argv; failure paths (not installed, daemon down, logged out, status and serve timeouts, garbage output, failing command, partial apply, failing verify read, cancelled context); drift re-apply with `LAI-NET-007` raised once and auto-cleared; the reconciler loop on a manual clock; `TailnetURL`, `Status`, `net.serve_reachable`; through the real engine (offline removes, online applies, a stuck `Off` is retried) |
| `modes/prober` | each result class (full, internet, tailnet, none) from a fake HTTP doer and a fake tailnet check, with the requests made and the checks skipped; cloud URL variants; hysteresis and `none_since` on a manual clock; 30 minutes of `none` gives `LAI-NET-011` (not at exactly 30) and `GET /mode` `suggest_survival`; `LAI-NET-012` and `LAI-NET-013`; no probing outside online mode; start, stop and restart of the loop; a pass in flight at `Stop` never overwrites `not_checked`; resume from the recorded value; through the real engine; the dedicated client (no proxy, no keep-alive, redirects reported) |
| `auth` (portal hook) | AGENT-06 only: the principal-resolution hook calls a `PortalSessions` interface (fake in tests); unknown cookie → anonymous. The Portal session tests themselves (`lai_portal` accepted only on the survival-portal.md §3.3 routes, Origin required for Portal mutations, all Portal sessions dropped on leaving survival) belong to **PORTAL-02** ([survival-portal.md](survival-portal.md) §9) |
| `cloud` | against `cloud/fake`: register 200 reattached true/false, 401, 400; handshake `config` (both planes, 404 → eab + capabilities fallback, `keyReported: false` → key push, clock skew); `localai` plane long poll with `waitMs` and immediate re-claim; snapshot `boot_id`/`seq`, 409 stale_seq ignored, strict schema conformance with `state.v1.schema.json`, scrubber drop; heartbeat 15 s cadence (fake clock); `extra` exactly ≤ 16 keys matching regex; 400-with-extra fallback; host/power only after `node_host_vitals`; claim → execute → result with claimToken; assignment without token → body omits key (never null); renew 409 → stop renewing, operation completes; result 409 → dropped; disallowed type → failure; strict params; backoff sequence with jitter bounds and Retry-After; 401 → revoked; 403 claim → disabled; 404 heartbeat → re-register; offline stop sends heartbeat offline + deregister; mode.offline settles before transition; agent.update settles after health |
| `hardware` | nvidia-smi CSV fixtures (normal, `[N/A]`, 2 GPUs, throttle bitmask, driver mismatch, malformed row, `clocks_event_reasons` rejected → old field name remembered); compute apps matched to the `localai-llama` cgroup (v1 and v2 spellings, child cgroups, similar names); AMD sysfs trees (dedicated, Strix Halo unified with DRM fdinfo, duplicate client ids counted once, two cards matched by PCI slot, rocm-smi/radeontop/lspci fallbacks, absent tools); device nodes and groups; battery sysfs (AC, charging, discharging with energy_now/power_now, charge_now/current_now, negative current, zero rate, USB-C charger, peripheral battery, absent battery, unreadable capacity, missing → upower fallback, hostile upower path); thermal zones (Intel, AMD hwmon, acpitz only, none, dead sensors); meminfo, cpuinfo, loadavg, `/proc/self` and mountinfo parsers incl. malformed input; hysteresis and 3-sample rules for every threshold (battery 20/10 with the 5-point band, RAM, swap, others' VRAM, temperatures, throttling incl. the CPU counter, disks, `LAI-HW-007` with and without a profile, `LAI-HW-002` after 3 failures), stale sections keep their verdict, only a round advances a counter; probe failure → last value with `stale: true` and recovery; hung `nvidia-smi` and a runner that ignores its deadline stay inside the round cap; `hardware.power` only on change and not for the first observation; OOM journal parser (other process, binary and truncated lines, cgroup spelling), window and dedupe, service verdict, hold window; `DetectBackend` and `LAI-HW-017`; argv assertion (no `uuid`, `serial`, `pci.bus_id`); `GET /hardware` through the full middleware chain (401 anonymous, `?fresh=1` once per 2 s with a fake clock, bad `fresh` → 400, answers within the deadline with a hung `nvidia-smi`) |
| `logx` | scrubber on every token family, Bearer, 64-hex, tailnet IP and name; key-name redaction on whole words (`api_key`, `pin`, `agent-token` redacted; `pinned`, `spinner`, `monkey` not); exemptions `*_synced`, `*fingerprint*`, `*_sha256`; `*_sha256` exemption does not cover the live key; nested groups, `With` attributes, errors, byte slices, structs; no `time` key; level change at runtime; the scanners agree with the regular expressions above on random input |
| `events` | Bus: ids monotonic from 1; the ring keeps exactly the last 1000; replay table (none, up to date, buffered, evicted → `resync gap` first, newer than the newest → `resync restart`, filtered); `Publish` refuses unknown, reserved and wrong-struct payloads; filters (exact, wildcard, multiple, default all, invalid pattern, `resync`/`shutdown` always pass); Portal restriction by intersection; slow subscriber dropped at the 257th queued event while the publishers never wait; replay not counted against the limit; context cancel and `Close` leave no goroutine; 50 concurrent subscribers under `-race`; gapless replay-to-live hand-over while publishing; every payload type has a typed example and `logx.ScrubString` leaves it unchanged. Handler (`api`, real server): headers and exact wire frame, type filter and `400` on a bad pattern, `Last-Event-ID` replay and reconnect, Portal sees only its types (also on replay), `: ping` at 15 s on a fake clock, client disconnect frees the subscription and ticker, blocked writer → slow client dropped, write deadline cuts off a stalled write, `shutdown` then close with `Server.Stop` not held up, `503` while stopping |
| `audit` | line shape equals the example above; one `write(2)` and one `fsync` per record (fake FS records both); mode `0600` (a looser existing file is fixed); rotation at the threshold keeps exactly `.1`…`.5` and drops `.5` first; open failure → `LAI-AGT-013`, record still on slog, recovery on the next write; full disk and failing `fsync` (fake); partial line never glued to the next record; `WithoutRotation` never renames; two writers (agent and CLI) on one file never interleave; secrets never reach the file or the mirror; line cap 4096 |
| `update` | minisign `Ed` and `ED` valid; wrong key id; tampered SHA256SUMS; tampered binary; version smoke mismatch; rollback paths: health fail, boot_attempts ≥ 3, timer fires |
| `services` | Over `services/fake` (Manager, Bus) and `execx/fake`, with a manual clock. D-Bus: fake job results `done` → success, `failed`/`canceled`/`dependency` → `JobError` with the result string, systemd `timeout` and a silent bus → `ErrTimeout`, a connection lost mid-job → `ErrBusLost`, a `JobRemoved` of another job ignored, one that arrives before the call returns not missed; unit-file calls (`Enable`/`Disable`/`Mask`/`Unmask`) with the exact flags then a `Reload`, no reload after a failure; `State` from `GetAll` (a timer is never asked for `.Service`; a unit with no file is inactive/dead); `Subscribe` (matches added once, initial states, a `PropertiesChanged` re-reads and sends changes only, ends when the bus is lost); `UnitPath` escaping. systemctl: exact argv golden for every verb and for `show`, timeouts (60 s start/stop/restart, 10 s others), `TZ=UTC LC_ALL=C` env, `systemctl show` parser fixtures (`testdata/systemctl-show-*.txt`: active, failed, not found, timer, old systemd, microseconds) and malformed output, non-zero exit → `CommandError` with scrubbed stderr, polling subscribe. Allowlist: the full unit × verb table, `PanicOnViolation`, `ViolationError` code `LAI-AGT-099`, the inner Manager never sees a refused call. `Auto`: D-Bus chosen, systemctl chosen when the dial fails, `services.backend` logged, failover when the connection drops, no failover on a job failure, re-dial after the interval, subscription survives an outage. Journal: argv golden, `since` in its own element, fixtures (`testdata/journal-*.jsonl`: string and byte-array `MESSAGE`, no `PRIORITY`, `null`, repeated field, junk lines, no entries), scrubbing on decoded fields with a `lat_` token built at run time, cap → partial line dropped and `Truncated`, `lines`/unit refusals, exit/timeout/cancel errors. Health: probe table, `httptest` (200, 503, redirect, 201, slow, closed port). Watcher: snapshot, state events (first observation silent), probe on start and per interval (15 s vs 30 s), `starting`/`degraded`/`down` with the one-minute rules, alert raised once and cleared, restart resets health, publishes on the real `events.Bus`. Controller: each action reaches its state, waits through `activating`/`deactivating`/auto-restart, every failure reason, cancellation |
| `api` services and logs | `GET /services` golden body and through the real watcher; auth (anonymous 401, Portal 401, socket operator ok); every action with audit record, op id header and manager calls; body strictness; unknown name 404, unknown action 400; stop `llama` in survival (no force 409 `LAI-AGT-023` and `denied` audit, force ok, kiwix/whisper/other modes unaffected); the `LlamaRestarter` hook (restored models, busy 409 `LAI-AGT-022` with the holder's op id, an API error passed through, absent hook → `[]`); failure mapping (502 per service code, `wrong_state`, `timeout`, stop 500, violation 500); es localization; audit failure does not fail the action. `GET /logs`: golden body, unit mapping and defaults, `lines` 0/2001/non-integer/empty → 400, 1 and 2000 ok, bad unit → 400, `since` forms, journal failures (504, 500), end to end with the fixture (no token in the response, valid JSON) and the cap |
| `statusapi` | the five routes through the real middleware over fakes of every view and a real config store on a temp `node.yaml`, every response recorded against `openapi.yaml` (§4.14). `/status`: the full body; every view unavailable (200 with the empty states); `?counters=` (absent, 0, 1, invalid); Spanish alerts; a mode transition; `Build(forCloud)` drops `details` and `processes`. `/profile`: GET body and suggestion; unknown profile and unknown id (422 with details, also for GET); PUT writes `node.yaml` with comments kept and a `.bak`, audits `profile.set`, regenerates once; overrides replace, an absent block keeps; no-op bodies write nothing; refused bodies (unknown profile or id, empty profile, zero or negative budget, unknown field, wrong type, malformed, empty) write, reload and audit nothing but the 422 failures; operator 403, anonymous and Portal 401, wrong content type 415; reload warnings and errors become warnings. `/catalog`: merged rows, content statuses, profiles, every source value, the empty catalog, no views; refresh changed, unchanged and first; offline, survival and a transition to survival 409 before the fetcher is asked; lint rejection 422 (ten problems at most, previous kept, audited), no source and fetch failure 502 without the transport error, a cut-off deadline; request rules. `ModelMerger`: every status, router down, default (state, profile, survival), locations in root order, `last_load_ms`, language. `TestTypesMirrorSchemas` for the wire types |
| `portal` | the route over the real middleware, recorded against `openapi.yaml`: the minimal body; power table (no battery, discharging, low at 19, not low at 20, critical, charging, full, plugged-in low, minutes left); chat capability states; language table (nothing, `Accept-Language`, `portal.language`, query, `auto`, unknown value, profile language); invalid `lang` 400; session kinds; the 2 s cache (served, expired, one entry per language and session, errors not cached); builder error without its text; `clock_ok`; no views at all; **redaction table**: a node full of paths, addresses, host names, tokens, drive names and alert details, for an anonymous, a Portal and an operator request |
| `execx` | guard test: no `os/exec` import outside `execx` |

### 9.2 Integration tests (fakes, `go test -tags integration ./...`)
- `internal/app` boots the full agent against: fake systemd (dbus fake), fake router (HTTP server mimicking `/health`, `/models`, `/models/load|unload`, `/models/sse` with scripted statuses), fake tailscale (script returning fixture JSON, records argv), fake cloud (EAB fake), temp dirs for `/etc/localai`, `/var/lib/localai`, `/srv/localai`. Scenarios:
  1. Fresh start → token created, listeners up, `/health` ok, models restored.
  2. Online → survival → online with the cloud fake observing deregister and re-register.
  3. Kill (SIGKILL) during `survival_models`; restart; transition resumes and completes.
  4. Cloud `model.load` job → router fake load → result posted; `/status` and state push reflect it.
  5. Self-update from a fake GitHub server (signed with a test key) → restart simulated → health ok → job settled; second run with a broken binary → rollback.
  6. `/chat/` proxy (survival-portal.md §4.4): survival anonymous ok, online anonymous 403 page, injected Authorization seen by the fake upstream, WebUI `POST /models/load` intercepted and run through the orchestrator, `/tools*` refused.
  7. `/maps/tiles/*.pmtiles` Range request returns 206 with correct bytes.

### 9.3 Manual tests on a real node (ai-n3; the owner runs them)
| # | Command | Expected |
|---|---|---|
| 1 | `systemctl status localai-agent --no-pager` | `active (running)`, `Status:` empty, no restarts |
| 2 | `curl -s http://127.0.0.1:7070/api/v1/health` | `{"ok":true,"version":"…","mode":"online","starting":false}` |
| 3 | `curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:7070/api/v1/status` | `401` |
| 4 | `curl -s -H "Authorization: Bearer $(cat /etc/localai/agent-token)" http://127.0.0.1:7070/api/v1/status \| jq .mode` | `{"value":"online",…}` |
| 5 | `curl -s -o /dev/null -w '%{http_code}\n' -H 'Host: evil.example:7070' http://127.0.0.1:7070/api/v1/health` | `403` |
| 6 | `aictl mode offline && tailscale serve status` | serve shows no listener for 8080; `aictl status` mode offline |
| 7 | `aictl mode online && tailscale serve status` | pattern B listener back (`https://hp-linux.<TAILNET>.ts.net -> http://127.0.0.1:8080`) |
| 8 | `aictl mode survival`, then open `http://127.0.0.1:7070/` in a browser | redirected to the Portal; Chat opens without a key prompt |
| 9 | `sudo kill -9 $(pidof aiagent)` during step 8's transition, wait 10 s | agent back; `journalctl -u localai-agent \| grep LAI-AGT-007`; mode survival |
| 10 | `journalctl -u localai-agent -o cat \| grep -E 'lat_[A-Za-z0-9_-]{20,}\|[0-9a-f]{64}'` | no output |
| 11 | Cloud UI → node ai-n3 → Load model | model loads within one poll; audit line `cloud.job` in `/var/lib/localai/audit.jsonl` |
| 12 | Unplug AC | within 10 s `aictl status` shows `LAI-HW-012`; the cloud fleet page shows `power.source_changed` |
| 13 | `nvidia-smi --query-gpu=memory.total,memory.used,temperature.gpu --format=csv,noheader,nounits`, then `curl -s -H "Authorization: Bearer $(cat /etc/localai/agent-token)" 'http://127.0.0.1:7070/api/v1/hardware?fresh=1' \| jq '.gpus[0] \| {vram_total_mib,vram_used_mib,temp_c}'` | the two agree (memory exactly, temperature within 2 °C); no `uuid`, `serial` or bus id anywhere in the body |
| 14 | `cat /sys/class/power_supply/*/capacity`, then `curl … /api/v1/hardware \| jq .power` | `battery_percent` equals the first battery's `capacity`; `source` is `ac` with the charger in, `battery` after unplugging (and `time_remaining_s` is a number) |
| 15 | While llama-server has a model loaded: `curl … /api/v1/hardware \| jq '.gpus[0] \| {vram_used_mib,vram_used_by_others_mib,processes}'`; open a GPU program (a game, `glmark2`) and repeat | `vram_used_by_others_mib` is the desktop's share and grows by what the new program uses; `processes` lists llama-server with its VRAM |
| 16 | Make `nvidia-smi` fail without touching the driver: `printf '#!/bin/sh\nexit 9\n' > /tmp/fail-smi && chmod +x /tmp/fail-smi && sudo mount --bind /tmp/fail-smi /usr/bin/nvidia-smi`; wait 30 s; then `sudo umount /usr/bin/nvidia-smi` | `LAI-HW-002` appears after 3 failed samples; the last GPU values stay with `"stale": true`; `GET /hardware?fresh=1` keeps answering in under 2 s; after the `umount` the alert clears within 10 s |
| 17 | Load a model that does not fit in RAM plus VRAM until the kernel kills llama-server, then `journalctl -k \| grep 'Out of memory'` | the journal line names `(llama-server)`; within 60 s `LAI-LLM-027` is raised and stays about 15 minutes |
| 18 | On ai-n1 (Strix Halo, the owner runs it): `curl … /api/v1/hardware \| jq '.gpus[0] \| {unified_memory,vram_total_mib}'` | `unified_memory: true` and `vram_total_mib` close to the GTT size from `cat /sys/class/drm/card*/device/mem_info_gtt_total` (÷ 1048576) |
| 19 | `journalctl -u localai-agent -o cat \| grep services.backend` | one line with `services.backend=dbus` (AGENT-09, issue #19, needs-hardware) |
| 20 | `curl -s -H "Authorization: Bearer $(cat /etc/localai/agent-token)" http://127.0.0.1:7070/api/v1/services \| jq '.services[] \| {name,active,healthy,state,enabled}'` | three rows; `llama` and `kiwix` `active`, `healthy: true`, `state: "ok"`; a stopped `whisper` is `inactive`, `healthy: false`, `state: "down"` |
| 21 | `curl -s -X POST -H "Authorization: Bearer $(cat /etc/localai/agent-token)" -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:7070/api/v1/services/kiwix/restart`, then `systemctl show localai-kiwix -p ActiveState,NRestarts` | `200 {"name":"kiwix","active":"active","restored_models":[],"op_id":"op_…"}` in a few seconds; the unit is `active`; an audit line `service.restart` in `/var/lib/localai/audit.jsonl` with the same `op_id` |
| 22 | `curl -s -H "Authorization: Bearer $(cat /etc/localai/agent-token)" 'http://127.0.0.1:7070/api/v1/logs?unit=llama&lines=5' \| jq '.lines \| length'` | `5`; the same call with `lines=0`, `lines=2001` and `unit=sshd` answers `400` |
| 23 | `sudo systemctl kill -s SIGKILL localai-kiwix`, wait 10 s, then call the services route again | systemd restarted it (`Restart=on-failure`): `restarts` is 1 more, `last_exit` is not `null`; `journalctl -u localai-agent \| grep service.state` shows the change (the D-Bus subscription saw it without the 15 s probe) |

## 10. Implementation breakdown

1. **AGENT-01 Agent skeleton and multi-call binary** — `cmd/aiagent` dispatch (§3.1), `internal/app` start/stop wiring, `aiagent version --json`, Makefile targets for amd64/arm64 static builds, `go mod vendor`. *Accept:* `aiagent version`, `aictl --help` via symlink and `aiagent ctl --help` work; `CGO_ENABLED=0` build reproducible; `execx` guard test in place. *Deps:* none.
2. **AGENT-02 Config store: node.yaml** — schema structs, validation table §3.4, defaults, comment-preserving atomic writer with `.bak`, hot reload. *Accept:* §9.1 config cases (incl. `llama.load_timeout_seconds`, `cloud.poll.*` ranges and `LAI-CLD-062` inside the `LAI-AGT-002` report). *Deps:* AGENT-01.
3. **AGENT-03 State store: state.json** — single-writer store, atomic write, `.bak`, corruption fallback to offline, `.bak` when `state.json` is missing, best-effort load of a newer `schema_version`, unknown-key preservation (§3.5). *Accept:* §9.1 state cases. *Deps:* AGENT-01.
4. **AGENT-04 Logging, scrubber and audit log** — `logx` JSON handler, scrubbing rules §4.11, `audit` writer with rotation. *Accept:* scrubber table test; audit lines fsync'd and rotated at 10 MiB. *Deps:* AGENT-01.
5. **AGENT-05 HTTP server, middleware and error envelope** — TCP + unix socket listeners, request id, Host/Origin/CSRF checks, content-type/body limits, strict JSON, error envelope and status map §3.7, `/health`. *Accept:* §9.1 middleware matrix. *Deps:* AGENT-02, AGENT-04.
6. **AGENT-06 Auth: tokens, sessions, UI codes, PIN, peer creds, grants** — §3.9; `/session*`, `/ui-code`, `/pin/verify`. Implements only the Portal-session **hook** in principal resolution (the Portal session itself and its tests are PORTAL-02). *Accept:* §9.1 auth cases; argon2 parameters as specified. *Deps:* AGENT-05.
7. **AGENT-07 OpenAPI document and conformance test** — `agent/api/openapi.yaml` for all §3.8 routes, `x-localai-auth`, schema-subset validator, route/spec bijection test with `x-localai-status: planned` and list-valued `x-localai-auth` (§4.14), TS generation script; creates the minimal `packages/ui/` if absent. *Accept:* test fails when a route or field is added on one side only. *Deps:* AGENT-05.
8. **AGENT-08 Event bus and SSE endpoint** — ring buffer 1000, `Last-Event-ID` replay, `resync`, filters, keepalive, slow-client cut-off. *Accept:* SSE tests incl. reconnect. *Deps:* AGENT-05.
9. **AGENT-09 systemd manager (D-Bus + systemctl fallback) and journald tail** — interface §4.3.1, allowlist, subscribe, `/services`, `/logs`. *Accept:* fake-dbus tests; manual `aictl services` on ai-n3. *Deps:* AGENT-05.
10. **AGENT-10 Hardware sampler** — NVIDIA, AMD (unified rule), RAM/CPU, disk, battery (sysfs + upower fallback), thermal, OOM detection, `/hardware`, `LAI-HW-*` checks. *Accept:* fixture tests §9.1; values on ai-n3 match `nvidia-smi` and `/sys`. *Deps:* AGENT-05.
11. **AGENT-11 Doctor framework and alert manager** — check registry, `codes.go` (en/es), periodic fast checks, `/doctor`, alert events; `scripts/check-doctor-codes.sh`. *Accept:* every **registry** code (all areas, [doctor-codes.md](doctor-codes.md) §1.6 rule 4) present in `codes.go`; check timeout/panic → `LAI-AGT-099`; CI script passes. *Deps:* AGENT-08.
12. **AGENT-12 Mode engine with resumable transitions** — steps §4.2.1, recording, preemption, resume, policies, `/mode`, `/mode/survival`. *Accept:* §9.1 modes cases + integration scenario 3. *Deps:* AGENT-03, AGENT-08, AGENT-09.
13. **AGENT-13 Tailscale Serve manager and connectivity prober** — §4.7 (ownership rules, apply/remove/drift/funnel), §4.5 prober, `LAI-NET-*` checks. *Accept:* fixture tests incl. A+B; manual steps 6–7. *Deps:* AGENT-12.
14. **AGENT-14 Status, portal status, profile and catalog routes** — `/status`, `/portal/status` (en/es), `/profile` GET/PUT, `/catalog`, `/catalog/refresh` wiring to `internal/catalog`. For `/portal/status` this item owns the route, the cache and a minimal builder; PORTAL-01 owns the capability model and the full [survival-portal.md](survival-portal.md) §3.5 schema. *Accept:* bodies validate against openapi.yaml; portal status contains no paths/keys. *Deps:* AGENT-07, AGENT-10, AGENT-12.
15. **AGENT-15 Model and key routes** — `GET /models`, `GET /models/{id}`, `POST /models/{id}/download`, `DELETE /models/{id}`, `/key`, `/key/rotate` wired to `internal/router` (ROUTER-03…05), their `openapi.yaml` entries and route-conformance tests, audit of key reads. The `POST /models/{id}/load|unload` and `PUT /models/default` handlers, orchestrator methods and their status mapping are **ROUTER-05** ([llama-router.md](llama-router.md) §10); this item registers nothing for them beyond the shared mux conventions. *Accept:* route tests with fake router; `GET /key` audited and `no-store`. *Deps:* AGENT-14, ROUTER-03, ROUTER-04, ROUTER-05.
16. **AGENT-16 Proxy and static-path plumbing** — shared reverse-proxy helper (pinned loopback target, header stripping, flush, timeouts, error pages), `/docs/` static serving, path-safety helpers and global CSP headers, used by the `/chat/` proxy (PORTAL-03) and `/library/`+`/maps/` (SVC-04, SVC-09). *Accept:* integration scenarios 6–7; WebUI loads under `/chat/` on ai-n3. *Deps:* AGENT-05, AGENT-06.
17. **AGENT-17 Cloud link: register, heartbeat, state and key push** — §4.8.1–4.8.5, `link.json`, the strict `LocalaiStateSnapshotV1` builder (incl. `ark.roots[].ready`, `drill.results`, severity and `serve_pattern` mapping; cloud-app.md §3.6), vitals + extra (16 keys), host/power/events gating, fallbacks, backoff, `/cloud` routes, enroll/unenroll. *Accept:* §9.1 cloud cases (fake EAB). *Deps:* AGENT-12, AGENT-14.
18. **AGENT-18 Cloud link: claim loop and job executors** — §4.8.6–4.8.8, allowlist, strict params for the 14 types, lease renewal, settle rules, inflight file, offline stop. *Accept:* §9.1 cloud job cases + integration scenario 4 (the `internal/cloud` half is `TestScenarioCloudModelLoadIsReflectedInTheStatePush`; the half over the real router, `/status` and listeners is AGENT-20's `internal/app`). The `agent.update` **decoder** is here, with the `jobs.AgentUpdater` seam and an executor that fails with `LAI-AGT-023` until AGENT-19 supplies one; the executor and settle-after-health are AGENT-19. Shipped (§4.8.10). *Deps:* AGENT-15, AGENT-17.
19. **AGENT-19 Self-update with rollback** — §4.10 (release resolve, minisign, sha256, smoke, install, transient rollback timer, post-update health). Also the `agent.update` job executor and its settle-after-health (result `{from_version, to_version, status}`). *Accept:* §9.1 update cases + integration scenario 5 (the `internal/cloud/jobs` half is `TestScenarioSelfUpdateFromTheCloud` and `TestScenarioBrokenReleaseIsRolledBack`; the half over the real listeners and `/health` is AGENT-20's `internal/app`, which wires `jobs.Deps.AgentUpdate`, `ClaimerDeps.Recover`, `AgentUpdate.Bind` and `AgentUpdate.PostStart` at step 15). Shipped (§4.10 details). *Deps:* AGENT-09, AGENT-17, AGENT-18.
20. **AGENT-20 Startup sequence, graceful shutdown, systemd unit** — §4.1 ordering and degradation, sd_notify READY/WATCHDOG/STOPPING, §4.12, `localai-agent.service` §3.6 (installer ships it) and the embedded unit templates `agent/internal/setup/units/` (§3.2); mutating-route gate after step 12, `starting` cleared at step 17, persisted `pending_mode`, `LAI-AGT-014` restart-loop rule. *Accept:* boot with missing node.yaml still serves the Portal; SIGTERM completes < 25 s with llama still running. *Deps:* AGENT-12, AGENT-13, AGENT-17.

## 11. Open questions
1. **Does EAB let the app mount `POST /api/nodes/{id}/localai/state` and `/localai/key` under the `nod_` allowlist?** Default: yes (ADR-0003); until the seam lands the agent uses the capabilities fallback (§4.8.4) and the key push stays pending (`LAI-CLD-060`), which blocks gateway use of that node's key. The cloud-app writer must confirm.
2. **llama WebUI under a path prefix.** Verified by build config (`paths.relative: true`) but not yet by a browser test on the real node; if absolute API paths appear in a future WebUI, the fallback is to start llama-server with `--api-prefix` — rejected for now because it would change the public URL. Default: keep the `/chat/` prefix proxy and pin the llama.cpp commit until the test passes.
3. ~~**Schema gaps with cloud-app.md's `LocalaiStateSnapshotV1`.**~~ Resolved (2026-11-02): cloud-app.md §3.6 added `none` to `tailscale.serve_pattern`, and both specs say the agent maps alert severity `error` → `critical`.
4. **Claim cost on the `eab` plane.** EAB's claim is a plain poll; at 3 s per node that is ~29 k empty polls a day per node. The interim `localai` plane long-polls (25 s) and has no such cost. Default: keep 3 s on the `eab` plane and revisit with the cloud's metrics.
