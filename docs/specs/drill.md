# Spec: Drill (monthly offline-readiness self-test)

> **Status:** approved design · **Epic:** E9 · **Code:** `agent/internal/drill/`, test media `agent/internal/drill/media/` (embedded) · **Doctor area:** `LAI-DRL`
> Canonical names: [overview.md](../architecture/overview.md) §3. Depends on: [ark-sync.md](ark-sync.md), [companion-services.md](companion-services.md), [survival-portal.md](survival-portal.md), [rebuild-kit.md](rebuild-kit.md), [llama-router.md](llama-router.md).

---

## 1. Purpose and scope

A node that hasn't been tested offline isn't an ark. The drill proves, every month and automatically, that everything a person needs with no internet still works: files are intact, each survival brain answers (including a photo question for the vision model), the library searches, speech goes both ways, maps render, manuals and the rebuild kit are present, and llama.cpp could be rebuilt. It does this **without disconnecting the network and without changing the mode**. A manual quarterly "pull the plug" drill covers what software can't simulate.

In scope: the automated check list, how it avoids the network, how it restores state, the results schema, reporting and alerting, the units, and the manual checklist.

Non-goals: benchmarking (only coarse speed floors), testing the cloud or the gateway, stress tests, fixing problems automatically (the drill reports; doctor fixes say what to do).

## 2. Concepts
| Term | Meaning |
|---|---|
| **Check** | One test with an id `drl.<area>.<name>`, a result `pass | warn | fail | skip`, a duration, details and a doctor code when not `pass` |
| **Run** | One execution: id `drl-YYYYMMDDTHHMMSSZ-<4hex>`, trigger `scheduled | manual | cloud | manual_plug` (a cloud `POST /drill` is not in the job list today, so `cloud` is reserved; `manual_plug` marks a manual "pull the plug" entry recorded with `aictl drill record-manual`, §4.6, which has no checks) |
| **Network fence** | For the run's duration the agent's outbound HTTP transport refuses every non-loopback dial, so any check that secretly needs the internet fails |

## 3. Interfaces

### 3.1 CLI and API
| Command | API | Notes |
|---|---|---|
| `aictl drill [--quick] [--only <check-id>…] [--json] [--wait]` | `POST /api/v1/drill` → `202 {run_id}` | `--quick` skips full-hash verification and model loads other than the default (≈ 2 min) |
| `aictl drill --scheduled` | same, `trigger: scheduled` | used by the unit; a skipped run prints `i Drill skipped: <reason>.` and exits 0 |

| `aictl drill show [run-id] [--json]` | `GET /api/v1/drill` (last) | human summary or JSON |
| `aictl drill --cancel` (and SIGTERM to a waiting `aictl drill`) | `DELETE /api/v1/drill` → `202 {"run_id"}` (`404 LAI-AGT-021` when no drill runs) | aborts the run; the agent restores state (§4.3) and marks the run `aborted` (as in §5) |

Exit codes ([aictl.md](aictl.md) §3.4 is authoritative): `0` = drill `pass` (or a `--scheduled` run skipped), `8` = `warn`, `9` = `fail`, `6` = a manual run refused by a precondition (§4.1), `3` = agent not reachable. Handlers, engine and API are DRILL-01; the CLI surface (`aictl drill`, `drill show`, `drill record-manual`) is AICTL-10.

Request:
```json
{"quick": false, "only": [], "trigger": "manual"}
```
`409 LAI-DRL-002` if a drill or an ark run is active; `409 LAI-DRL-003` in survival mode.

### 3.2 Files
- `/var/lib/localai/drill/<run-id>.json`: results (§3.4), localai 0640. The last 24 are kept.
- `/var/lib/localai/drill/latest.json`: symlink to the last finished run.
- Embedded test media (`go:embed`, CC0 or self-made, licence noted in `media/README.md`):
  - `en.wav`: 16 kHz mono, about 4 s, a speaker saying "The quick brown fox jumps over the lazy dog." (expected text stored next to it),
  - `es.wav`: "El veloz murciélago hindú comía feliz cardillo y kiwi.",
  - `seven.png`: 512×512, white background, a large black digit **7** and a red circle in the top-left corner.

### 3.3 systemd units
`/etc/systemd/system/localai-drill.timer`:
```ini
[Unit]
Description=Local AI monthly offline drill (timer)
Documentation=https://github.com/marinoscar/local-ai/blob/main/docs/specs/drill.md

[Timer]
# First Sunday of each month, 04:30 local time (after the nightly sync window)
OnCalendar=Sun *-*-01..07 04:30:00
RandomizedDelaySec=30min
Persistent=true
Unit=localai-drill.service

[Install]
WantedBy=timers.target
```
`/etc/systemd/system/localai-drill.service`:
```ini
[Unit]
Description=Local AI offline drill (asks the agent to run one scheduled drill and waits)
Documentation=https://github.com/marinoscar/local-ai/blob/main/docs/specs/drill.md
Requires=localai-agent.service
After=localai-agent.service localai-sync.service

[Service]
Type=oneshot
User=root
ExecStart=/usr/local/bin/aictl drill --scheduled --wait
# Exit codes: aictl.md §3.4. 0 = pass, or skipped (busy, survival, battery); 8 = warn -> still success;
# 9 = fail and 3 = agent not reachable -> the unit fails, so it shows in `systemctl --failed`.
SuccessExitStatus=8
TimeoutStartSec=3h
KillMode=mixed
TimeoutStopSec=60s
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
```
`aictl` is only a client; on SIGTERM it calls `DELETE /api/v1/drill` (§3.1) to abort the drill, which then restores state (§4.3). The installer enables and starts the timer with `systemctl enable --now localai-sync.timer localai-drill.timer` ([installer.md](installer.md) §4.3 P9).

### 3.4 Results schema
```json
{
  "schema_version": 1,
  "run_id": "drl-20261101T043512Z-9c1e",
  "trigger": "scheduled",
  "node_id": "ai-n3",
  "agent_version": "0.4.0",
  "catalog_version": "2026.11.02-1",
  "mode": "online",
  "quick": false,
  "started_at": "2026-11-01T04:35:12Z",
  "finished_at": "2026-11-01T05:21:40Z",
  "overall": "warn",
  "survival_ready": true,
  "counts": {"pass": 27, "warn": 2, "fail": 0, "skip": 1},
  "checks": [
    {"id": "drl.llm.answer.qwen3.5-9b-q4km", "area": "llm", "result": "pass", "ms": 41230,
     "details": {"load_ms": 28400, "tokens_per_s": 31.2, "answer": "READY"}},
    {"id": "drl.vlm.photo.<small-vlm-id>", "area": "vision", "result": "pass", "ms": 63120,
     "details": {"answer": "7", "load_ms": 35100}},
    {"id": "drl.stt.es", "area": "speech", "result": "warn", "code": "LAI-DRL-031", "ms": 2900,
     "details": {"wer": 0.33, "text": "el veloz murciélago hindú comía feliz cardillo y kiwi", "language": "es"}},
    {"id": "drl.kit.rebuild_dry_run", "area": "kit", "result": "pass", "ms": 1800}
  ],
  "restored": {"loaded_models": ["qwen3.5-9b-q4km"], "default_model": "qwen3.5-9b-q4km", "ok": true},
  "network_fence": {"enabled": true, "blocked_dials": 0}
}
```
`overall` = `fail` if any check fails, else `warn` if any warns, else `pass`. `survival_ready` = every check in the **critical set** (§4.2, marked ★) passed or warned.

## 4. Behaviour

### 4.1 Preconditions (scheduled runs skip and exit 0; manual runs refuse with the code, exit 6)
| Condition | Code |
|---|---|
| mode is `survival` (never disturb an emergency) | `LAI-DRL-003` |
| an ark sync/verify/import run is active | `LAI-DRL-002` |
| llama busy: any slot processing (router `/slots` per loaded model) or a chat request in the last 10 minutes | `LAI-DRL-004` (scheduled: retried every 2 h for up to 48 h, then runs anyway as `--quick`) |
| on battery below 50 % | `LAI-DRL-005` |
`offline` mode is fine: the drill needs no network.

### 4.2 Checks (in order)
★ = critical set for `survival_ready`.

| # | Id | What | Pass | Warn | Fail |
|---|---|---|---|---|---|
| 1 | `drl.fence` | turn on the network fence; try a dial to `huggingface.co:443` through the agent transport | dial refused by the fence | — | dial succeeded (fence broken; the run aborts) |
| 2 | `drl.config.offline_lint` ★ | static scan: `/etc/localai/models.ini` has no `hf-repo`, `model-url` or `http` values; `/var/lib/localai/kiwix/library.xml` paths are local files that exist; `whisper.env` model exists; the Portal/maps bundle has no external URLs (same scan as survival-portal.md §4.11, run on the embedded files) | all local | — | any remote reference (`LAI-DRL-010`) |
| 3 | `drl.files.manifest` ★ | for every item on every mounted root: MANIFEST present and valid, files present with the right sizes | all | stale or orphan items | a tier-1 item missing (`LAI-DRL-011`) |
| 4 | `drl.files.hash` ★ (skipped in `--quick`) | full sha256 of the survival models, whisper and piper models, the tier-1 map, `map-assets`, the kit parts for this node, and of tier-1 ZIMs on the internal root; other items only if their `last_reverified_at` is older than `reverify_days` (time budget 90 min; leftovers `skip`) | all match | budget ran out | any mismatch (`LAI-DRL-012`, and ark-sync quarantines per its rules) |
| 5 | `drl.llm.answer.<id>` ★ (one per loadable model in `profile.survival.models`; `--quick`: only the default) | load the model (respecting `models_max`: one after the other, unloading as the router needs), then chat completion `temperature 0`, `max_tokens 16`, prompt "Reply with exactly one word: READY" (thinking disabled if `client_hints.reasoning`) | answer contains `READY` (case-insensitive) and generation ≥ the profile's speed floor (cpu-only 2 tok/s, else 8 tok/s) | slower than the floor (`LAI-DRL-021`) | load fails (`LAI-DRL-020`) or wrong answer twice (`LAI-DRL-022`) |
| 6 | `drl.vlm.photo.<id>` ★ (each vision model in the survival list) | with the model loaded, send `seven.png` and "What digit is written in this image? Answer with the digit only." | answer contains `7` | answer mentions seven in words only | no answer / wrong (`LAI-DRL-023`) |
| 7 | `drl.llm.fit` | compute the survival-entry plan (modes.md §2.2 step 5) without switching: every survival model fits per [llama-router.md](llama-router.md) §4.3 | fits | a fallback model would be used | nothing fits (`LAI-DRL-024`) |
| 8 | `drl.kiwix.search` ★ | for each tier-1 kiwix book: `GET /library/search?books.name=<name>&pattern=water&pageLength=1` via the agent proxy | ≥ 1 result each | a tier-2 book fails | a tier-1 book fails or kiwix down (`LAI-DRL-030`) |
| 9 | `drl.portal.firstaid` | each available `portal_links` URL returns 200 | all | some missing (`LAI-SVC-109`) | — |
| 10 | `drl.stt.en`, `drl.stt.es` ★ | `POST /speech/stt` with `en.wav` / `es.wav` (`language: auto`) | WER ≤ 0.25 and correct language | WER ≤ 0.5 (`LAI-DRL-031`) | error or WER > 0.5 (`LAI-DRL-032`) |
| 11 | `drl.tts.<lang>` ★ (`en`, `es`) | `POST /speech/tts` "Test of the emergency voice." / "Prueba de la voz de emergencia." | WAV decodes, 0.8–8 s, RMS > −40 dBFS, `X-LocalAI-Fallback` absent | fallback voice used (`LAI-DRL-033`) | error (`LAI-DRL-034`) |
| 12 | `drl.speech.roundtrip` | synthesize the English sentence of check 11, transcribe it | WER ≤ 0.3 | ≤ 0.6 | — (warn only; informative) |
| 13 | `drl.maps.tile` ★ | read the default map header; fetch the tile at z=10 containing the map center via `GET /maps/tiles/<id>.pmtiles` with Range (directory lookup done in Go); also `GET /maps/assets/fonts/Noto Sans Regular/0-255.pbf` and the sprite JSON | tile non-empty (gzip or MVT), glyph and sprite 200 | assets missing (`LAI-SVC-031`) | no map or tile fetch fails (`LAI-DRL-040`) |
| 14 | `drl.maps.search` | `/maps/search?q=San José` | ≥ 1 result | index missing (`LAI-SVC-032`) | — |
| 15 | `drl.portal` ★ | `GET /portal` 200 with the CSP header; `GET /api/v1/portal/status` valid; `/chat/` returns the WebUI and `/chat/props` returns 200 (the drill uses an internal admin session, since the node isn't in survival) | all | — | any fails (`LAI-DRL-050`) |
| 16 | `drl.docs` ★ | on the internal root: `<root>/docs/index.html`, `<root>/docs/01-survival-mode.html`, `<root>/docs/06-rebuild-a-node-offline.html` (the flat `html` paths of `docs/index.json`, agent.md §3.8.29) and `<root>/docs/survival-card-<node-id>.html`, rendered for the current catalog version | all, plus `survival-card-<node-id>.pdf` | rendered for an older catalog or with placeholders (`LAI-SYN-062`), or the survival-card PDF missing (`LAI-CLI-014`, no Chromium) | an HTML file missing (`LAI-DRL-060`) |
| 17 | `drl.kit.present` ★ | [rebuild-kit.md](rebuild-kit.md) §4.4 (Verification) steps 1–5 (step 1 hashing reuses check 4) | complete | lab-level parts missing on a drive (`LAI-KIT-041`) | this node's parts missing or inconsistent (`LAI-DRL-070`) |
| 18 | `drl.kit.rebuild_dry_run` ★ | `aictl rebuild-llama --dry-run` (and `rebuild-whisper --dry-run`) | exit 0 | — | non-zero (`LAI-DRL-071`), details list the failing preflight checks |
| 19 | `drl.services` | companion-services health deep checks not already covered | all `ok` | `degraded` | — |
| 20 | `drl.power` (laptops) | hardening checks `LAI-INS-040…044` and AC present | all | any (`LAI-DRL-080`) | — |
| 21 | `drl.clock` | clock plausible (not before the newest `verified_at`) | ok | wrong (`LAI-DRL-081`) | — |
| 22 | `drl.restore` ★ | restore state (§4.3) and verify it | restored | — | not restored (`LAI-DRL-090`) |

Every check has a timeout (model load 300 s, completions 120 s, others 30 s). A timed-out check fails with its own code and the drill continues. Checks never mutate content (no downloads, no deletes); check 4 may cause quarantine only through ark-sync's normal verify path.

### 4.3 State capture and restore
Before check 5, the drill records `loaded_models`, the default model and whether whisper is running. Model checks load and unload as needed. At the end (also on abort, SIGTERM or a panic in a check, via `defer`), it:
1. unloads drill-loaded models that weren't loaded before,
2. reloads the previously loaded models in their original order and waits for `ready` (300 s each),
3. resets the default model,
4. restarts whisper if it was stopped.
The run start sets `state.json: drill.in_progress` (`{"run_id", "started_at", "captured": {loaded_models, default_model, whisper_running}}`, [agent.md](agent.md) §3.5) and the end clears it. If the agent restarts mid-drill, it finds `drill.in_progress` and performs the restore on start. Check 22 verifies the result; failure raises `LAI-DRL-090` (red) because the node might be left without its normal brain.

### 4.4 Network fence
While a run is active, the agent's shared outbound `http.Transport` uses a `DialContext` that allows only loopback and Unix sockets and records every refused dial (`network_fence.blocked_dials`, with host names in the details). The cloud link is paused for the drill's duration (heartbeats resume after; a heartbeat gap of up to the drill's length is normal and the cloud shows "running drill" from the last heartbeat before it). Sync can't run concurrently (§4.1). Subprocesses started by checks get no proxy and an environment with `HTTPS_PROXY=http://127.0.0.1:9` (a closed port) so a tool that tries the network fails quickly. Any `blocked_dials > 0` turns the run `warn` with `LAI-DRL-091` and names the caller, because it means some feature tried to reach the internet.

### 4.5 Reporting and alerting
- Results are written to `/var/lib/localai/drill/` and returned by `GET /api/v1/drill`.
- Events: `drill.started`, `drill.check_done {id, result}`, `drill.done {run_id, overall, survival_ready}` (listed in [agent.md](agent.md) §3.10).
- Alerts: `overall: fail` raises `LAI-DRL-001` (red) with the failing check ids; `warn` raises it as a warning. Cleared by the next passing run. No drill in 45 days raises `LAI-DRL-006`.
- The desktop shows a notification after a scheduled drill: "Monthly check finished: everything works offline." / "Revisión mensual terminada: todo funciona sin internet." or "Monthly check found problems. Open Status." / "La revisión mensual encontró problemas. Abra Estado."
- Cloud ([cloud-app.md](cloud-app.md) §3.6, which owns the schema): the next state snapshot carries `drill: {run_id, last_run_at, result, survival_ready, failed_checks, failed_check_ids, results}`, where `results` is this spec's full results document (§3.4), sent once after the run (answers and transcripts included only as the fixed test strings; no user data is ever involved). Mapping to cloud `DrillResultsV1` (cloud-app.md owns it): the agent **drops `node_id`** (implicit in the node route). A `manual_plug` entry (§4.6) is **never** sent as `results`: it has no checks, no restore and no fence, so the snapshot carries only `last_run_at` and `result` (`pass|fail`) for it, plus `run_id`; the automated fields keep the last automated run's values. The EAB heartbeat itself carries no drill block.
- READY.json is not changed by the drill, except through check 4's normal verify path.

### 4.6 Manual quarterly "pull the plug" drill
Published in [runbooks/08-scheduled-sync.md](../runbooks/08-scheduled-sync.md) Part 6 (linked from runbook 01's Part 10 paper log) and rendered as a page in `/docs/`. Takes about 45 minutes. Do it every 3 months, by someone who is **not** the person who set up the node, if possible.

1. Note the time. Run `aictl drill` first and check it passes (`aictl drill show`).
2. **Unplug the network cable and turn Wi-Fi off** (top-right menu → Wi-Fi → Off). On the MSI, also check it doesn't boot into Windows by itself.
3. **Unplug the charger** (laptops). Write down the battery percentage.
4. **Restart the computer.** Log in. What you should see: the desktop app opens by itself, or you can open it from the dock.
5. Press **Survival mode** in the app. What you should see: the Portal fills the screen within 2 minutes; the status line says "Ready" or lists problems with codes.
6. **Chat:** press **1**, ask "How do I treat a minor burn?" Expect an answer in under 30 seconds.
7. **Photo:** press **2**, take a photo of any object or choose one from the Pictures folder, ask "What is this?" Expect a sensible description (switching brain is OK).
8. **Talk:** press **3**, ask a question aloud in Spanish, then in English. Expect a spoken answer in the same language.
9. **Library:** press **4**, search "water purification". Open a result. Then **F** → *Severe bleeding*: the article opens.
10. **Maps:** press **5**, search for your town, press **Nearest: hospital**.
11. **Manuals:** press **6**, open "Survival mode"; print the Survival card (or check the printed one is current).
12. **Status:** press **7**; write down the battery % and estimated time left. Compare with step 3 to check the battery estimate.
13. **Ark drive:** plug in the AI-ARK drive. What you should see: Status shows "Ark drive connected". Open `START-HERE.html` on the drive from the file manager; it opens with no internet.
14. **Without the app:** close the app, open a browser at `http://127.0.0.1:7070/portal`. It works the same.
15. **Rebuild check (optional, 30 min, admin):** `sudo aictl rebuild-llama --from-kit --no-activate`. It must finish without internet.
16. Plug the charger and the network back in. In the Dashboard, leave survival mode (needs the PIN). Check the mode shows **online** and the cloud shows the node again within a minute.
17. Record the result: `aictl drill record-manual --result pass|fail --notes "…"` (stores a manual entry in `/var/lib/localai/drill/` with `trigger: manual_plug`, §2; reported to the cloud as in §4.5), or write it on the paper log kept with the card.

Anything that failed: note the doctor code shown and follow its fix, or [runbooks/07-troubleshooting.md](../runbooks/07-troubleshooting.md).

## 5. Failure handling and edge cases
| Situation | Behaviour | Code |
|---|---|---|
| Node in survival mode at drill time | skip (scheduled) / refuse | `LAI-DRL-003` |
| Sync running | skip, retry next day (timer is monthly, so `Persistent` + an agent-side retry daily for 7 days) | `LAI-DRL-002` |
| User chatting | defer up to 48 h, then `--quick` | `LAI-DRL-004` |
| On battery < 50 % | skip | `LAI-DRL-005` |
| Model load OOM | check fails, next model continues, restore still runs | `LAI-DRL-020` |
| A check hangs | its timeout fails it; the run continues | — |
| Agent crash mid-drill | restore on next start; run marked `aborted` | `LAI-DRL-090` if restore fails |
| Fence detects a dial | run `warn` | `LAI-DRL-091` |
| No drill for 45 days (timer disabled, node always busy) | alert | `LAI-DRL-006` |
| Drive not plugged in | drive checks are `skip`, not fail (the internal root must stand alone) | — |

## 6. Security considerations
- The drill uses an internal admin context (no token needed inside the agent) and only the same code paths as users; it never runs shell commands from config.
- Test media are embedded and fixed; no user data is used or sent.
- The network fence only restricts; it can't open anything.
- Results contain no secrets (model ids, timings, fixed test strings).

## 7. Observability
Logs (component `drill`): `check_id`, `result`, `ms`, `code`. Events §4.5. Metrics: `localai_drill_last_overall` (0 pass / 1 warn / 2 fail), `localai_drill_last_finished_timestamp`, `localai_drill_check_ms{check}`.

## 8. Doctor codes (shown in the desktop; English and Spanish)
| Code | Sev | Meaning | Fix (en) | Fix (es) |
|---|---|---|---|---|
| LAI-DRL-001 | warning (error when the drill fails) | The last drill found problems | Open Status, or run `aictl drill show`, and follow each fix. | Abra Estado, o ejecute `aictl drill show`, y siga cada solución. |
| LAI-DRL-002 | info | The drill waited because a sync was running | Nothing to do. It runs later. | Nada que hacer. Se ejecutará después. |
| LAI-DRL-003 | info | The drill doesn't run in survival mode | Nothing to do. | Nada que hacer. |
| LAI-DRL-004 | info | The drill waited because the AI was in use | Nothing to do. | Nada que hacer. |
| LAI-DRL-005 | info | The drill skipped: battery below 50 % | Plug in the charger. | Conecte el cargador. |
| LAI-DRL-006 | warning | No drill in 45 days | Run `aictl drill`. Check that `localai-drill.timer` is enabled. | Ejecute `aictl drill`. Revise que `localai-drill.timer` esté activado. |
| LAI-DRL-010 | error | A setting still points to the internet | Run `aictl doctor`; the details name the setting. | Ejecute `aictl doctor`; los detalles indican la configuración. |
| LAI-DRL-011 | error | Essential content is missing | Run `aictl ark sync` with internet, or `aictl ark import`. | Ejecute `aictl ark sync` con internet, o `aictl ark import`. |
| LAI-DRL-012 | error | Stored files failed their check | Run `aictl ark verify --full`. | Ejecute `aictl ark verify --full`. |
| LAI-DRL-020 | error | A survival brain couldn't start | Run `aictl doctor`; choose a smaller brain if it doesn't fit. | Ejecute `aictl doctor`; elija un cerebro más pequeño si no cabe. |
| LAI-DRL-021 | warning | A brain is slower than expected | Check the computer is plugged in and not overheating. | Revise que esté conectado y no se esté calentando. |
| LAI-DRL-022 | error | A brain gave wrong answers to a simple test | Run `aictl ark verify --item <model>`; report it if it repeats. | Ejecute `aictl ark verify --item <modelo>`; repórtelo si se repite. |
| LAI-DRL-023 | error | The photo brain didn't read the test image | Run `aictl ark verify --item <model>`; report it. | Ejecute `aictl ark verify --item <modelo>`; repórtelo. |
| LAI-DRL-024 | error | Survival brains don't fit on this computer | Change the profile's survival models (`aictl profile`). | Cambie los modelos de supervivencia del perfil (`aictl profile`). |
| LAI-DRL-030 | error | Library search doesn't work | Run `aictl services restart kiwix`. | Ejecute `aictl services restart kiwix`. |
| LAI-DRL-031 | warning | Speech recognition is less accurate than expected | Nothing urgent. A bigger whisper model helps. | Nada urgente. Un modelo whisper más grande ayuda. |
| LAI-DRL-032 | error | Speech recognition failed | Run `aictl services restart whisper`. | Ejecute `aictl services restart whisper`. |
| LAI-DRL-033 | warning | A language has no voice of its own | Install a voice for it with `aictl ark sync`. | Instale una voz con `aictl ark sync`. |
| LAI-DRL-034 | error | The voice failed | Run `aictl doctor`. | Ejecute `aictl doctor`. |
| LAI-DRL-040 | error | Maps don't work | Run `aictl ark verify --item <map>`. | Ejecute `aictl ark verify --item <mapa>`. |
| LAI-DRL-050 | error | The Portal or chat page doesn't work | Run `sudo install-node.sh --repair`. | Ejecute `sudo install-node.sh --repair`. |
| LAI-DRL-060 | warning | Manuals are missing | Run `aictl ark sync --item docs-bundle`. | Ejecute `aictl ark sync --item docs-bundle`. |
| LAI-DRL-070 | error | The rebuild kit is incomplete for this computer | Run `aictl kit status` and sync the missing parts. | Ejecute `aictl kit status` y sincronice lo que falta. |
| LAI-DRL-071 | error | llama.cpp could not be rebuilt offline | Run `aictl rebuild-llama --dry-run` and fix what it lists. | Ejecute `aictl rebuild-llama --dry-run` y corrija lo indicado. |
| LAI-DRL-080 | warning | Laptop power settings aren't right | Run `sudo install-node.sh --repair`, then reboot. | Ejecute `sudo install-node.sh --repair` y reinicie. |
| LAI-DRL-081 | warning | The clock looks wrong | Set the date and time in Settings. | Ajuste la fecha y hora en Configuración. |
| LAI-DRL-090 | error | The drill couldn't restore the normal brain | Press **Switch brain** and choose the usual one. | Presione **Cambiar cerebro** y elija el habitual. |
| LAI-DRL-091 | warning | Something tried to use the internet during the drill | Report the details; offline use may be affected. | Reporte los detalles; el uso sin internet puede verse afectado. |

## 9. Test plan
### 9.1 Unit
- Each check against fakes (router, whisper, piper, kiwix, ark): pass, warn and fail paths and their codes.
- WER computation (normalised: lowercase, no punctuation, accents folded) with fixtures.
- Fence: loopback allowed, Unix allowed, public IP and hostname refused and counted.
- Restore: snapshots with 0, 1 and 3 loaded models; abort at every check; restart recovery from `state.json: drill.in_progress`.
- Preconditions and scheduled retry/defer logic.

### 9.2 Integration
- The full drill in CI against the CPU stack (tiny llama model, tiny VLM if one exists small enough, else the vision check is `skip` with a note; whisper tiny; a piper voice; test ZIM; tiny pmtiles) inside `unshare -n`: overall `pass`, `blocked_dials: 0`.
- Inject faults one at a time (corrupt a model byte, stop kiwix, remove assets, break `models.ini` with an `hf-repo`) and assert the right check fails with the right code.

### 9.3 Manual on ai-n3
1. `aictl drill --wait` → about 30–50 min (two survival models with a load each on 8 GB); `aictl drill show` → all pass, `restored.ok: true`.
2. `aictl drill --quick --wait` → under 3 minutes.
3. Do the §4.6 manual drill once and record it.

## 10. Implementation breakdown
1. **DRILL-01 Drill framework, results, API and units** (1.5 d). Scope: run lifecycle, preconditions, check registry with timeouts, results file and retention, `state.json: drill.in_progress`, `POST/GET /drill` and their handlers (the engine and API behind `aictl drill [show]`; the CLI surface itself is AICTL-10, aictl.md §10), unit templates (`SuccessExitStatus=8`, skipped = exit 0), events. Acceptance: fake checks produce the §3.4 document; `systemd-analyze verify`.
2. **DRILL-02 Network fence and offline lint** (1 d). Scope: §4.4 transport fence, subprocess env, check 2 static scan. Acceptance: unit tests; injected `hf-repo` fails check 2.
3. **DRILL-03 State capture/restore and model checks** (1.5 d). Depends on: ROUTER-05. Scope: §4.3, checks 5–7 with embedded `seven.png`. Acceptance: restore tests incl. crash recovery.
4. **DRILL-04 Content and service checks** (1.5 d). Scope: checks 3, 4, 8–16, 19 with embedded WAVs and WER. Acceptance: fault-injection integration tests.
5. **DRILL-05 Kit and power checks** (0.5 d). Depends on: KIT-03. Scope: checks 17, 18, 20, 21. Acceptance: fixtures.
6. **DRILL-06 Reporting, alerting and the manual drill page** (1 d). Scope: alerts `LAI-DRL-001/006`, desktop notification strings, snapshot `drill` block (cloud-app.md §3.6) with the §4.5 mapping (`node_id` dropped, `manual_plug` entries without `results`), the §4.6 checklist in runbook 08 Part 6, and the route `POST /drill/manual` (agent.md §3.8.28); the CLI `aictl drill record-manual` is in AICTL-10 ([aictl.md](aictl.md) §10). Acceptance: fake cloud receives the summary; runbook dry-run on ai-n3.

## 11. Open questions (with proposed defaults)
1. ~~**New events and command.**~~ Resolved (2026-11-02): agent.md §3.10 lists `drill.started`, `drill.check_done`, `drill.done`; agent.md §3.8.28 adds `POST /drill/manual`; aictl.md §4.17 adds `aictl drill show` and `aictl drill record-manual`.
2. **Cloud-triggered drills.** *Default:* not now (no `localai.drill.run` job type); the cloud only displays results.
