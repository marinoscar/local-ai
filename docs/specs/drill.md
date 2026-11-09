# Spec: Drill (monthly offline-readiness self-test)

> **Status:** approved design · **Epic:** E9 · **Code:** `agent/internal/drill/` (checks: `drill/checks/`), `agent/internal/netx/`, `agent/internal/offlinescan/`, `agent/internal/api/drill.go`, unit templates `agent/internal/setup/units/`, test media `agent/internal/drill/media/` (embedded) · **Doctor area:** `LAI-DRL`
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
| **Run** | One execution: id `drl-YYYYMMDDTHHMMSSZ-<4hex>`, trigger `scheduled | manual | cloud | manual_plug` (a cloud `POST /drill` is not in the job list today, so `cloud` is reserved; `manual_plug` marks a manual "pull the plug" entry recorded with `aictl drill record-manual`, §4.6, which has no checks and whose id is `drl-YYYYMMDDTHHMMSSZ-<4hex>-manual`, the time being when the drill was done) |
| **Network fence** | For the run's duration the agent's outbound HTTP transport refuses every non-loopback dial, so any check that secretly needs the internet fails |

## 3. Interfaces

### 3.1 CLI and API
| Command | API | Notes |
|---|---|---|
| `aictl drill [--quick] [--only <check-id>…] [--json] [--wait]` | `POST /api/v1/drill` → `202 {run_id}` | `--quick` skips full-hash verification and model loads other than the default (≈ 2 min) |
| `aictl drill --scheduled` | same, `trigger: scheduled` | used by the unit; a skipped run prints `i Drill skipped: <reason>.` and exits 0 |

| `aictl drill show [run-id] [--json]` | `GET /api/v1/drill` (last) | human summary or JSON |
| `aictl drill record-manual --result pass\|fail [--notes "<text>"] [--at <time>]` (admin) | `POST /api/v1/drill/manual` → `201 {run_id}` | stores the §4.6 record, §3.1 below |
| `aictl drill --cancel` (and SIGTERM to a waiting `aictl drill`) | `DELETE /api/v1/drill` → `202 {"run_id"}` (`404 LAI-AGT-021` when no drill runs) | aborts the run; the agent restores state (§4.3) and marks the run `aborted` (as in §5) |

Exit codes ([aictl.md](aictl.md) §3.4 is authoritative): `0` = drill `pass` (or a `--scheduled` run skipped), `8` = `warn`, `9` = `fail`, `6` = a manual run refused by a precondition (§4.1), `3` = agent not reachable. Handlers, engine and API are DRILL-01; the CLI surface (`aictl drill`, `drill show`, `drill record-manual`) is AICTL-10.

Request:
```json
{"quick": false, "only": [], "trigger": "manual"}
```
`409 LAI-DRL-002` if a drill or an ark run is active; `409 LAI-DRL-003` in survival mode; `409 LAI-DRL-004` when llama is busy and `409 LAI-DRL-005` on a low battery (§4.1). `trigger` is `manual` (the default) or `scheduled`; `cloud` and `manual_plug` are not requested over this route. `only` entries select a check by id and, at a dot boundary, the checks under it (`drl.stt` selects `drl.stt.en` and `drl.stt.es`); an entry that selects nothing is `400 LAI-AGT-020` (`details.reason: unknown_check`, `details.unknown`). The body is optional (every field defaults).

`202 {"run_id"}` means the run started; progress follows `drill.*` events and `GET /drill?run_id=` answers while it runs (the in-flight document has `state: running`, §3.4). A **scheduled** run that a precondition holds back is also `202`, with `{"run_id", "state": "skipped", "code", "reason", "retry_at"?}` and no events: the skipped document (§3.4) is written before the answer, so `aictl drill --scheduled` prints `i Drill skipped: <reason>.` and exits 0 without waiting. `retry_at` is when the agent tries again (§4.1) and is absent when it will not.

`POST /api/v1/drill/manual` (admin: an operator without the PIN grant is `403 LAI-SEC-002`; audited as `drill.record_manual` with `target.result` and `target.run_id`, never the notes) records the manual drill of §4.6. Body `{"result": "pass|fail", "notes": "<up to 2000 characters>", "at": "<RFC 3339>"}`: `result` is required, `notes` and `at` are optional, and `at` is when the drill was done (default now). Answer `201 {"run_id": "drl-…-<4hex>-manual"}`. `400 LAI-AGT-020` with `details.field` and `details.reason`: `result` `invalid_result`; `notes` `notes_too_long` (counted in characters) or `invalid_text` (not UTF-8, or a control character other than a line break or a tab); `at` `invalid_at` (not RFC 3339), `in_the_future` (more than 5 minutes ahead of the node's clock) or `too_old` (over a year back: the retention keeps 24 documents). It needs `Content-Type: application/json`. It is allowed while a drill runs, and it never starts one, never touches `state.json`'s `drill.last_run_at` or `last_result` (those describe runs of the drill itself), and never moves `latest.json`. The document is the §3.4 shape with `trigger: manual_plug`, `overall` `pass` or `fail`, `started_at` and `finished_at` equal to `at`, `survival_ready: false`, empty `checks`, zero `counts`, no `restored` block and the text in `notes` (a field only this document has; it stays on the node, §4.5).

### 3.2 Files
- `/var/lib/localai/drill/<run-id>.json`: results (§3.4), localai 0640, written atomically. The newest 24 are kept; of the skipped runs (§4.1) only the newest one, so a month of retries never pushes a real result out.
- `/var/lib/localai/drill/latest.json`: relative symlink to the last finished run (`pass`, `warn`, `fail` or `aborted`). A skipped run and a `manual_plug` entry (§4.6, stored as `<run-id>.json` with the `-manual` id) never move it. When the link is missing or dangling, `GET /drill` falls back to the newest finished document; the document `latest.json` names is never pruned.
- `/var/lib/localai/drill/<run-id>.partial`: the checkpoint of the run in flight, rewritten after every check. Crash recovery (§4.3) turns it into the aborted run's document; a successful run removes it.
- Embedded test media (`go:embed`, CC0 or self-made, licence noted in `media/README.md`):
  - `en.wav`: 16 kHz mono, about 4 s, a speaker saying "The quick brown fox jumps over the lazy dog." (expected text stored next to it),
  - `es.wav`: "El veloz murciélago hindú comía feliz cardillo y kiwi.",
  - `seven.png`: 512×512, white background, a large black digit **7** and a red circle in the top-left corner. Shipped by DRILL-03 in `agent/internal/drill/media/`; `gen_seven.go` draws it from the standard library (CC0, `media/README.md`), and `media_test.go` pins the size and the content.

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
`overall` = `fail` if any check fails, else `warn` if any warns, else `pass`; a run whose restore failed (`restored.ok: false`) is `fail`, and one with `network_fence.blocked_dials > 0` is at least `warn`. `survival_ready` = at least one check of the **critical set** (§4.2, marked ★) ran and every one of them passed or warned. A critical check that was skipped counts against it, except one skipped by `--quick`; so `--only` and `--quick` runs answer for the checks they ran, and a run that ran no critical check is never ready.

A run that did not complete has the same document plus `state` (`skipped`, `aborted`, or `running` for the in-flight snapshot `GET` serves while the run is active), and, for the first two, `code`, a plain English `reason` and, when a skipped scheduled run will be retried, `retry_at`. `overall` is then `skipped`, `aborted` or `running`, `checks` holds what finished, and `survival_ready` is false. A completed run has no `state`, so its document is exactly the example above. The `network_fence` block also carries `code: LAI-DRL-091` and `callers` when a dial was refused.

## 4. Behaviour

### 4.1 Preconditions (scheduled runs skip and exit 0; manual runs refuse with the code, exit 6)
| Condition | Code |
|---|---|
| mode is `survival` (never disturb an emergency) | `LAI-DRL-003` |
| an ark sync/verify/import run is active | `LAI-DRL-002` |
| llama busy: any slot processing (router `/slots` per loaded model) or a chat request in the last 10 minutes | `LAI-DRL-004` (scheduled: retried every 2 h for up to 48 h, then runs anyway as `--quick`) |
| on battery below 50 % | `LAI-DRL-005` |
`offline` mode is fine: the drill needs no network. A node heading to survival counts as in survival. A router that cannot answer is not busy.

The first failing row in table order wins. A **scheduled** run is recorded as a skipped document (§3.4) and the retry schedule below is persisted in `state.json` (`drill.deferred`: `code`, `first_at`, `next_retry_at`, `deadline_at`, `attempts`, `quick`, `only`), so an agent restart keeps it; the agent calls the retry on a one-minute tick, never systemd. Survival, battery and another drill running are not retried.

| Held back by | Retried | When the window ends |
|---|---|---|
| `LAI-DRL-002` (a sync, verify or import run) | daily | after 7 days the attempt is recorded as `given up`; the 45-day alert (`LAI-DRL-006`) covers a node that never gets its drill |
| `LAI-DRL-004` (llama busy) | every 2 h, the last attempt at 48 h | the drill runs anyway as `--quick`, without asking whether llama is busy (the other rows still apply) |

A run that starts (a retry, or a manual run that is not `--quick`) ends the wait. While a drill is active, `Running` tells the sync engine to refuse a start (sync and drill never overlap, §4.1 table).

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

**As built (DRILL-05, #117): checks 17, 18, 20 and 21.** Code: `agent/internal/drill/checks/` (`kit.go`, `power.go`, `clock.go`), the daemon wiring in `agent/internal/app/drill_checks_kit.go`. Where the behaviour is finer than the table:
- **Check 17.** It runs [rebuild-kit.md](rebuild-kit.md) §4.4 steps 1 to 5 through the readiness service's verifier (`kitready.Source.Verify`) in `quick` mode on the internal root and on every mounted drive that has a `kit/` folder. Nothing is re-hashed here (check 4 hashes; the quick pass still checks every manifest, file presence and size, and the signature of the agent binaries), and the build dry run is left to check 18. **Fail** (`LAI-DRL-070`): this computer's own parts are missing or inconsistent on the internal root (`LAI-KIT-040`, `042`, from `VerifyReport.NodeProblems()`), a kit file is damaged or the signature fails (`LAI-KIT-001`, `044`), or the internal root has no kit. **Warn**: a drive lacks parts of other computers of the lab (`LAI-KIT-041`, `LabProblems()`); any other problem of a drive's kit (its own finding's code, since a drive's gap is not this computer's); the internal root's CUDA toolkit newer than the driver (`LAI-KIT-043`). A missing prebuilt build is optional and never a warning of the drill. With no drive mounted the lab-level part is skipped (`roots_checked: 1`); a drive without a `kit/` folder is listed in `details.skipped`. The timeout is 60 s, not 30 s: a quick pass reads every manifest of a big kit. Details are flat: `kit_overall` (the worst colour of the roots checked), `roots_checked`, `missing` (the parts that are not ok, `debs (LAI-KIT-040); prebuilt (warning)`, a drive's parts prefixed with its id), `codes` (the findings' codes), `skipped`, and `node_facts: unknown` when node.yaml and os-release gave too little to judge this computer's needs.
- **Check 18.** The offline preflight of `rebuild-llama --dry-run` and of `rebuild-whisper --dry-run` (`build.Manager.PreflightReport` with `FromKit`, `DryRun` and `Offline`, the apt simulation of the kit) runs in the agent's own process: no `aictl` subprocess and no compile. The request is offline and reads the kit's roots only, so the network fence has nothing to refuse; the programs it runs are read-only (`cmake --version`, `apt-get -s install`, `nvidia-smi`) and every one of them runs with `Env.SubprocessEnv` (no proxy variable, a dead `HTTPS_PROXY`), through a runner wrapper in `drill_checks_kit.go`. Whisper is part of the check only while the speech service is switched on in node.yaml (`whisper_skipped` says why it is not). A failed preflight is `fail` `LAI-DRL-071`; `llama_failed` / `whisper_failed` list each failed preflight check as `LAI-KIT-0nn <what was found>` (cut to fit 500 characters). The timeout is 120 s.
- **Check 20.** The laptop rule is the installer's (`harden.IsLaptop`: a battery, or a chassis type of 8, 9, 10, 11, 14, 30, 31 or 32); any other computer skips (`reason: not_a_laptop`). It reuses the installer's doctor checks (`harden.Hardener.Verify`: `LAI-INS-040` to `044`) and the charger reading of the drill's power source (`ac_online`). **Warn** (`LAI-DRL-080`; the check never fails): any hardening check reports a problem, the charger is not connected, or the hardening was never applied on this laptop (`hardening: not_applied`: the doctor checks say nothing then, and the drill treats a laptop that will sleep when the lid closes as the problem it is). While a reboot is pending after an install, the logind check cannot be judged: details say `LAI-INS-040: reboot_required` and `LAI-INS-070: pending` and it is not a problem. Details: one `ok` / `problem` / `reboot_required` per `LAI-INS-040`..`044`, `hardening`, `ac_online`.
- **Check 21.** The newest `verified_at` is read from the content index (the manifests of the servable copies on the mounted roots, memory only). The clock may be up to 5 minutes behind it (`ClockSkewTolerance`: content is verified by several computers whose clocks were set by hand); more than that is `warn` `LAI-DRL-081`. With nothing verified yet, or no index, the check passes with only `now` in its details. Details are `{now, newest_verified_at}` in RFC 3339.

Every check has a timeout (model load 300 s, completions 120 s, others 30 s). A timed-out check, and a check that panics, fails with its own code (the check declares it) and the drill continues; the framework never waits on a check that ignores its context, it cancels it and goes on. A check can end the main phase (`drl.fence` does when the fence is broken): the checks after it are recorded as `skip` (`details.reason: run_stopped`), the state is restored and the post-restore checks still run. `drl.restore` (22) is a post-restore check: it runs after the restore and reads its result. Checks never mutate content (no downloads, no deletes); check 4 may cause quarantine only through ark-sync's normal verify path.

**As built (DRILL-03, #100): checks 5, 6, 7 and 22.** Code: `agent/internal/drill/checks/` (`llm.go`, `vlm.go`, `fit.go`, `restorecheck.go`, shared `models.go`), the daemon wiring in `agent/internal/app/drill_checks_models.go`. Where the behaviour is finer than the table:
- **Which models.** Check 5 is one check per model of `profile.survival.models` that is *loadable* here (the static fit rules plus a verified copy on a root), in the profile's order, duplicates once. A model that is not loadable gets no check; check 7 reports it. Check 6 is one check per loadable model with the `vision` role. With no such model a single `drl.vlm.photo` is recorded as `skip` (`details.reason: no_vision_model`), not critical, so the document says the photo test did not apply.
- **`--quick`.** Only the survival default (`profile.survival.default_model`) is tested by checks 5 and 6. The others are recorded as `skip` (`reason: quick`), which does not count against `survival_ready`. A node whose default is not a vision model therefore does not run the photo test in a `--quick` run.
- **Loading.** Every load is `Orchestrator.LoadWith` with `Wait`, `UnloadOthers` (so `models_max` is respected one model after another), the system actor and a wait for the model-operations lock (`router.DefaultLockWait`), exactly like a survival load. A load that fails (for any reason, including out of memory) is `fail` `LAI-DRL-020` with `details.load_code` (the router's `LAI-LLM-...` code); the next model continues and the restore still runs. Check 6 also uses `LAI-DRL-020` when the vision model does not load. The check timeout is twice the configured load timeout (a model on an ark drive gets double), plus two completions (120 s each).
- **Check 5 answer.** `temperature 0`, `max_tokens 16`, no streaming, and `chat_template_kwargs.enable_thinking: false` when the catalog says `client_hints.reasoning` (the same switch the Portal's Talk uses). The answer passes when it contains `READY`, case-insensitive. A wrong answer, or an error, is asked once more; the second failure is `LAI-DRL-022` (`details.reason` is `wrong_answer` or `no_answer`).
- **Speed.** Generation tokens per second from the completion's `timings` (the server's `predicted_per_second`, else `predicted_n` over `predicted_ms`), never wall time with the load. The floor is 2 tok/s when the node's backend is `cpu`, else 8; a lower speed is `warn` `LAI-DRL-021`. When the server sends no timings the speed cannot be judged: the check passes with `details.speed_known: false`.
- **Check 6 answer.** `max_tokens 16`, one attempt. The answer passes when it contains the digit `7`; `seven` or `siete` as a word without the digit is `warn`; anything else is `fail`. Both the warning and the failure carry `LAI-DRL-023` (the table names no code for the warning).
- **Check 7.** A model fits when `Env.Loadable` says so (static rules, files present). The dynamic rules (llama-router.md §4.3.3) are evaluated as if nothing were loaded, crediting the RAM of the models loaded now, and a shortage is only noted in `details.tight`: the router goes ahead when the shortage is other programs' memory, and check 5's real load is the proof. `pass` when every survival model fits; `warn` (`LAI-DRL-024`) when some do and some do not (`details.fallback_used` says whether the survival-entry loop would reach a model that does not fit, so a later one stands in); `fail` (`LAI-DRL-024`) when none fits or the profile lists none. It needs no router: the fit rules read the catalog, the profile and a hardware sample.
- **Check 22.** It reads the result of the restore (`restored.ok`) and also lists the models loaded *now*; the models must be the restored ones.
- **Chat endpoint.** `router.Client.Complete` (`agent/internal/router/complete.go`) sends the question to the router with the same allow list and key handling as every router call; the key is never in an error or a detail.
- Details stay flat (strings, numbers, booleans); lists are joined with `, `.

**As built (DRILL-04, #114).** Checks 3, 4, 8 to 16 and 19 are in `agent/internal/drill/checks/` (one file per area, `RegisterContent` adds them to the registry) and the daemon builds their ports in `agent/internal/app/drill_checks_content.go`. The embedded recordings are `agent/internal/drill/media/` (self-made with Piper, licences in its README) and the normalised WER is `drill.WER`. Where the behaviour differs from, or adds to, the table above:

- *How the checks reach the agent.* Not a dial to `127.0.0.1:7070`: a request goes through the agent's own HTTP handler **in process**, with the context of a socket peer of uid 0 (the internal admin of §6). The same routes, middleware and handlers answer as for a user, nothing leaves the process (the fence has nothing to refuse), no token exists, no cookie or session is created, and the node need not be in survival mode for `/chat/` to open. A check never sends a credential.
- *3 `drl.files.manifest`.* Each readable root (ready, read-only or unclean; a drive that is not mounted is simply not there) is read with `ark.ScanRoot`, which validates every MANIFEST and requires every listed file to exist with the recorded size, and is judged against the root's readiness report: a wanted item that is tier 1 and not usable (missing, skipped, failed, quarantined, or gone since the report) fails `LAI-DRL-011`; a stale item, an orphan, a missing item of tier 2 or 3, or a directory with a damaged manifest warns (no code). No mounted internal root fails.
- *4 `drl.files.hash`.* The set that is always hashed is taken from the internal root's readiness report: survival models, the whisper and piper models, tier-1 maps, `map-assets`, tier-1 ZIMs and the kit parts this node needs (a quick kit verification names them; nothing is hashed twice). Every other copy, on any mounted root, is hashed only when its `last_reverified_at` is older than `sync.reverify_days` (30 when unset), oldest first. The pass runs through `verify.Reverifier`, so a mismatch is quarantined, restored from `.trash` or excluded by ark-sync's rules, and the check fails `LAI-DRL-012`; the items the budget did not reach are listed in `details.skipped` with `reason: time_limit` and the result is `warn` with `LAI-DRL-012`. The check's own timeout is the 90 minute budget plus 10. An item the report calls missing is not hashed (check 3 reports it), and a node with nothing to hash records `skip` with `reason: nothing_to_hash`.
- *8 `drl.kiwix.search`.* The request adds `format=xml` (the OpenSearch RSS the Portal's own search reads) and counts the `<item>` elements; an HTML results page is read too. A tier-2 or tier-3 failure warns with `LAI-DRL-030`. The timeout is 120 s, not 30 s: a library has about a dozen books and a full-text search of a large ZIM on a slow disk takes seconds. No book in the library records `skip` (`reason: no_books`).
- *9 `drl.portal.firstaid`.* The links are the `first_aid` list of `GET /api/v1/portal/status?lang=en`; a link that is not available, or whose URL does not answer 200, warns with `LAI-SVC-109`. A catalog without links records `skip`.
- *10 to 12.* The recordings are posted as `multipart/form-data` (`audio`, `language=auto`), as the Portal's Talk does. Right words in the wrong language warn `LAI-DRL-031`. A synthesis that decodes but is too short, too long or silent fails `LAI-DRL-034`; the fallback warning (`LAI-DRL-033`) applies only to a valid WAV. Check 12 warns with the code of the leg that failed (`LAI-DRL-034`, `LAI-DRL-032`) or `LAI-DRL-031` when the words do not come back.
- *13 `drl.maps.tile`.* The default map is the catalog's `default` entry, else the first. The header and every directory and tile read are `Range` requests to `/maps/tiles/<id>.pmtiles` (a route that answers 200 to a range request fails), decoded with `maps.ParseHeader` and `maps.FetchTile`. The zoom is 10 lowered to the map's maxzoom or raised to its minzoom. The font file is `fonts/Noto Sans Regular/0-255.pbf` and the sprite is `sprites/v4/light.json`, which must be valid JSON.
- *15 `drl.portal`.* The Portal status is valid when `schema_version` is 1, `node_id` and `mode` are set and `generated_at` parses. `/chat/` must be an HTML page. `details.failed` names which of `portal`, `status`, `chat` and `chat_props` failed and why.
- *16 `drl.docs`.* Besides the four pages of the table, every page `docs/index.json` lists must exist and be non-empty (a missing or damaged `index.json` fails). The warning is `LAI-SYN-062` when the docs item's `catalog_version` is not the running catalog's, or when the survival card still shows an unfilled `<NAME>` placeholder (the names are in `details.placeholders`, never a value), else `LAI-CLI-014` when only the PDF is missing.
- *19 `drl.services`.* It reads the supervisor's records and its deep checks: a service that is `degraded` or `down`, or a deep problem, warns with the code of the first problem; `disabled`, `missing_content` and `starting` are not findings.
- *Not covered by CI.* The integration run against the full CPU stack inside `unshare -n` (whisper tiny, a piper voice, a test ZIM, a tiny pmtiles) needs the real programs and models; the CI tests inject each fault of §9.2 into fakes of the services and into real manifests, directories and the real re-verifier, and prove the daemon wiring against the daemon rig. The measured WER of the Spanish recording with whisper tiny is about 0.44 (a `warn`); small and larger models are expected to pass.

### 4.3 State capture and restore
Before check 5, the drill records `loaded_models`, the default model and whether whisper is running. Model checks load and unload as needed. At the end (also on abort, SIGTERM or a panic in a check, via `defer`), it:
1. unloads drill-loaded models that weren't loaded before,
2. reloads the previously loaded models in their original order and waits for `ready` (300 s each),
3. resets the default model,
4. restarts whisper if it was stopped.
The run start sets `state.json: drill.in_progress` (`{"run_id", "started_at", "captured": {loaded_models, default_model, whisper_running}}`, [agent.md](agent.md) §3.5) and the end clears it. If the agent restarts mid-drill, it finds `drill.in_progress` and performs the restore on start. Check 22 verifies the result; failure raises `LAI-DRL-090` (red) because the node might be left without its normal brain. A restore that errors makes the document `restored.ok: false`, the run `fail` and carries `LAI-DRL-090`; the marker is cleared after one attempt, or every start would retry forever. If the capture itself fails nothing is changed and nothing is restored, and the document says `restored.ok: false` the same way. The restore runs under a context an abort cannot cancel, and it is idempotent: the agent can crash after it and before the marker is cleared.

Recovery (`Recover`, called once at startup): when `drill.in_progress` is non-null the restore runs with the recorded `captured`, the run's document is its last checkpoint finished as `aborted` (`reason: the agent restarted during the drill`), and the marker is cleared. When the run's final document is already on disk, only the marker is cleared. A run stopped by `Abort` or by the agent stopping (`Shutdown`) does the same restore on its way out and is `aborted` too; an aborted run is not a drill result (`last_run_at` and `last_result` are not written).

**As built (DRILL-03, #100): the restore.** Code: `agent/internal/drill/restore.go` (`StateRestorer`, over the `ModelOps` and `WhisperUnit` seams; the daemon implements them over the orchestrator and systemd). Differences and details:
- The engine captures before the *first* check, not before check 5, and persists the snapshot in `drill.in_progress` before anything is loaded. The snapshot is `Resident` (the loaded models, in the order of `state.models.desired_loaded`, most recently used first, then any other loaded model by id), the recorded default model (`state.models.default`, `""` when none) and whether `localai-whisper.service` is active.
- `Restore` works from the snapshot alone, so it serves the end of a run, an abort, a panic and `Recover`. It unloads what is loaded and was not captured, loads the captured models that are not loaded in their original order **without eviction** (the room was made by the unloads, and an eviction would unload a model of the snapshot), resets the default when it was set and moved, starts whisper when it was running and is not, then reads everything back. Every step runs even when an earlier one failed; the errors are joined and the result is `restored.ok: false`. The models are compared as sets; a default that was `""` is left alone, since no model operation can clear a default and the drill never changes one.
- A reload waits for the router's load timeout (`llama.load_timeout_seconds`, 300 s by default, doubled on an ark drive), which is the "300 s each" of this section.

### 4.4 Network fence
While a run is active, the agent's shared outbound `http.Transport` uses a `DialContext` that allows only loopback and Unix sockets and records every refused dial (`network_fence.blocked_dials`, with host names in the details). The cloud link is paused for the drill's duration (heartbeats resume after; a heartbeat gap of up to the drill's length is normal and the cloud shows "running drill" from the last heartbeat before it). Sync can't run concurrently (§4.1). Subprocesses started by checks get no proxy and an environment with `HTTPS_PROXY=http://127.0.0.1:9` (a closed port) so a tool that tries the network fails quickly. Any `blocked_dials > 0` turns the run `warn` with `LAI-DRL-091` and names the caller, because it means some feature tried to reach the internet.

**As built (DRILL-02, #99).** Code: `agent/internal/netx` (the fence, the shared transport, `DeadProxyEnv`), `agent/internal/offlinescan` (the Go port of the §4.11 scan), `agent/internal/drill/checks` (`NetFence`, check 1 `Fence`, check 2 `OfflineLint`), wired in `agent/internal/app/drill_checks_fence.go`. Differences and details to know:
1. **One door.** The "single shared transport" is `netx.Shared()`, plus `netx.NewTransport(caller, inner)` for a client that needs its own timeouts, TLS or keep-alive settings; every transport and every raw dial (`netx.DialFor`, `netx.Wrap`) shares one dial wrapper and one fence state (`netx.Default`). `netx.UnixTransport` talks to a Unix socket and is never fenced. The guard test `netx/guard_test.go` fails the build on `http.DefaultTransport`, `http.DefaultClient`, `http.Get`/`Head`/`Post`/`PostForm`, an `http.Transport` literal, an `http.Client` literal without a `Transport` (it would ride on the default one), `net.Dialer`, `net.Dial`/`DialTimeout` to anything but a literal `unix` network, `tls.Dial`, in any non-test file outside `netx`. Exempt, each with its reason in the test: `internal/cli` (aictl is its own process), `internal/setup` and `cmd/aiagent/setup` (the installer), `internal/router/migrate/host` (a one-off migration probe) and `internal/modes/nethttp.go`, the connectivity prober, which uses the internet only to measure whether it is reachable and would otherwise report `none` for the length of every drill.
2. **Refusal.** While a fence is held a dial is allowed to a loopback IP literal, to the name `localhost`, and to a Unix socket. Everything else, including every other host name, is refused by the address string before any DNS lookup; for `localhost`, a hook on the real connect also refuses an address that does not resolve to loopback. The fence is reference counted (`Acquire`/`Release`, a deferred release); a nested hold keeps it on and each holder's report counts only what happened since its own `Acquire`. The count is exact; at most 128 distinct destinations are listed. Turning the fence on also closes the idle pooled connections of the `netx` transports, so a connection opened before the drill cannot carry a request without a dial for the fence to see (a request already in flight keeps its connection).
3. **Caller attribution.** `netx.WithCaller(ctx, name)` on a request, or the name a transport or `netx.Client(name)` was made with when the request names none. The report entries are `host:port (caller)`, with ` xN` when the same destination was refused N times; they are `network_fence.callers`.
4. **Check 1.** The probe is a `HEAD https://huggingface.co:443/` through the agent transport with the context marked `netx.WithProbe`: the fence refuses it but does not count it, so a healthy fence leaves `blocked_dials` at 0. Any result other than a fence refusal fails the check and stops the main phase (`run_stopped` for the rest; the restore and the post-restore checks still run): the request got through, it failed for another reason, or no fence is held (then nothing is dialed). The table gave check 1 no doctor code on failure; it carries `LAI-DRL-091`.
5. **`blocked_dials > 0`.** Besides `network_fence.code` and `callers`, the `drl.fence` row (when it ran) becomes `warn` with `LAI-DRL-091` and gains the flat details `{"blocked_count": 1, "blocked": "huggingface.co:443 (sync.resolver)"}` (a `; `-joined string, at most 500 characters). A `--only` run without check 1 has only the `network_fence` block.
6. **Check 2.** Details are flat: `models_ini`, `library_xml`, `whisper_env`, `portal_bundle` (each `ok`, `N finding(s)`, `absent`, `not_linted`, `unparseable`, `unreadable` or `no_model`), `books`, `bundle_files`, `remote_count` and, on a failure, `remote` (at most 500 characters, naming each setting: `models.ini line 8 [model-id] hf-repo`, `library.xml book /srv/... (does not exist)`, `whisper.env WHISPER_MODEL ... (does not exist)`, `portal bundle assets/app.js:1: <url>`). A file that is not there is `absent`, not a failure (a node with no whisper yet has nothing to point anywhere); a file that is there and cannot be parsed is noted, not failed. In `models.ini` a key is a remote setting when it is `hf`, `hf-*`, the short Hugging Face flags, `*-url`, `dr` or `docker-repo`, and any value that starts with `http://`, `https://`, `ftp://` or `hf://` is one too; a value is never copied into the details (it may carry a token). The bundle scan is `offlinescan.ScanFS(ui.Dist())`, whose output `offlinescan` tests against `scripts/check-offline-assets.mjs` on its fixtures (and live, when node is installed).
7. **Subprocess environment.** `Env.SubprocessEnv(base)` (`netx.DeadProxyEnv`) drops every `*_PROXY` variable in any letter case and sets `HTTP_PROXY` and `HTTPS_PROXY` to `http://127.0.0.1:9`. The checks of this slice start no subprocess; the checks that do (18) use it.

### 4.5 Reporting and alerting
- Results are written to `/var/lib/localai/drill/` and returned by `GET /api/v1/drill`.
- Events: `drill.started`, `drill.check_done {id, result}`, `drill.done {run_id, overall, survival_ready, trigger?, notice?}` (listed in [agent.md](agent.md) §3.10). `trigger` is the run's trigger. `notice` (`{"en", "es"}`) is present only when a **scheduled** run completed (pass, warn or fail), never for a manual, aborted or skipped run, so a client that shows a notification when `notice` is present shows exactly the ones below. It is sent before the event leaves, so a client that reacts to it already finds the alert and the snapshot block of the run.
- Alerts: `overall: fail` raises `LAI-DRL-001` (error) with the failing check ids; `warn` raises it as a warning. Cleared by the next passing run. No drill in 45 days raises `LAI-DRL-006` (warning). Both are fast doctor checks over an in-memory view of the documents (`drill.Reporter`, loaded from disk at startup), so a restart derives them again and a check never touches the disk:
  - `drl.last_result` looks at the last **completed** run (pass, warn or fail, any trigger except `manual_plug`). Its details are `run_id`, `overall`, `finished_at`, `failed_check_ids` and `failed_checks` (the doctor codes of the checks that failed or warned, plus `LAI-DRL-091` and `LAI-DRL-090` when the run carries them). A completed run, even `--quick` or `--only`, replaces the previous result, so the next passing one clears the alert. An **aborted** run (§4.3), a skipped one and a `manual_plug` entry change nothing: they are neither a result nor a pass.
  - `drl.freshness` raises `LAI-DRL-006` when the last completed run of any trigger except `manual_plug` ended 45 days ago or more (`details.days`, `details.since`). On a node that never ran a drill, the days count from `drill.watch_since` of `state.json` (agent.md §3.5), which the reporter stamps once at the first start.
  - The doctor manager evaluates them every 60 s; the reporter's `OnChange` hook lets the wiring tick it at once when a run ends.
- The desktop shows a notification after a scheduled drill, from `drill.done.notice` (the strings are `drill.notify.pass` and `drill.notify.problems` of `agent/internal/i18n/{en,es}.json`): "Monthly check finished: everything works offline." / "Revisión mensual terminada: todo funciona sin internet." or "Monthly check found problems. Open Status." / "La revisión mensual encontró problemas. Abra Estado."
- Cloud ([cloud-app.md](cloud-app.md) §3.6, which owns the schema): the next state snapshot carries `drill: {run_id, last_run_at, result, survival_ready, failed_checks, failed_check_ids, results}`, where `results` is this spec's full results document (§3.4), sent once after the run (answers and transcripts included only as the fixed test strings; no user data is ever involved). The reporter is the cloud link's `DrillSource`. Mapping to cloud `DrillResultsV1` (cloud-app.md owns it, and it is strict): the agent **drops `node_id`**, `notes` and every other field the schema does not list; `catalog_version` is `null` when empty; `restored` is `{loaded_models: [], default_model: null, ok: true}` for a run without a restorer; a check's `details` keep only strings (cut to 500 characters), numbers, booleans and null, so a list or an object a check might add never leaves the node; lists and strings are held to the schema's limits (100 checks, 50 failed codes and ids). `failed_checks` and `failed_check_ids` list the checks that failed or warned (`failed_checks` also the run-level `LAI-DRL-090` and `LAI-DRL-091`). The results go out once: the link marks them sent only when the cloud answered `200`, so a snapshot that was not accepted carries them again. A `manual_plug` entry (§4.6) is **never** sent as `results`: when it is newer than the last completed run the block carries its `run_id`, its `last_run_at` (the time of the drill) and its `result` (`pass|fail`), the last automatic run's `survival_ready` (`false` when there never was one) and empty `failed_checks` and `failed_check_ids`; when the run is newer, the run's own values. An aborted or skipped run is never the block. The EAB heartbeat itself carries no drill block.
- The cloud link is paused for the whole run (§4.4); the daemon wiring puts `drill.PausingFence{Inner, Link}` where the engine takes its `Fence`: `Pause` before the first check and `Resume` after the last one and on every exit.
- READY.json is not changed by the drill, except through check 4's normal verify path.

### 4.6 Manual quarterly "pull the plug" drill
Published as Part B of the appendix "Monthly and quarterly checks" of [runbooks/01-survival-mode.md](../runbooks/01-survival-mode.md#appendix-monthly-and-quarterly-checks) (English and Spanish, linked from its Part 9 and its Part 10 paper log; [runbook 08](../runbooks/08-scheduled-sync.md) Part 6 points to it) and rendered with that page in `/docs/`; each step there has the steps below, "What you should see" and "If it doesn't work" (B1 to B17). Takes about 45 minutes. Do it every 3 months, by someone who is **not** the person who set up the node, if possible.

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
17. Record the result: `aictl drill record-manual --result pass|fail --notes "…"` (stores a manual entry in `/var/lib/localai/drill/` with `trigger: manual_plug`, §2 and §3.1; reported to the cloud as in §4.5), or write it on the paper log kept with the card.

Anything that failed: note the doctor code shown and follow its fix, or [runbooks/07-troubleshooting.md](../runbooks/07-troubleshooting.md).

## 5. Failure handling and edge cases
| Situation | Behaviour | Code |
|---|---|---|
| Node in survival mode at drill time | skip (scheduled) / refuse | `LAI-DRL-003` |
| Sync running | skip, retry next day (timer is monthly, so `Persistent` + an agent-side retry daily for 7 days, then give up) | `LAI-DRL-002` |
| User chatting | defer, retry every 2 h up to 48 h, then run `--quick` | `LAI-DRL-004` |
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
Logs (component `drill`): `check_id`, `result`, `ms`, `code`, with the `run_id`. Events §4.5: a skipped scheduled run publishes none; a run stopped by `Abort`, by `Shutdown` or by recovery ends with `drill.done` with `overall: aborted`. The gauges `localai_drill_last_*` are set by a completed run only. The agent has no Prometheus endpoint in phase 1 (agent.md §7), so the engine hands the gauges to a `Metrics` hook that the daemon wiring routes (counters in `GET /status`, the heartbeat). Metrics: `localai_drill_last_overall` (0 pass / 1 warn / 2 fail), `localai_drill_last_finished_timestamp`, `localai_drill_check_ms{check}`.

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
| LAI-DRL-091 | warning | Something tried to use the internet during the drill | Offline use may be affected. Test the feature named in the details (`aictl drill show`) with the network unplugged. | El uso sin conexión puede verse afectado. Pruebe sin red la función nombrada en los detalles (`aictl drill show`). |

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
6. **DRILL-06 Reporting, alerting and the manual drill page** (1 d). Scope: alerts `LAI-DRL-001/006`, desktop notification strings, snapshot `drill` block (cloud-app.md §3.6) with the §4.5 mapping (`node_id` dropped, `manual_plug` entries without `results`), the §4.6 checklist in the appendix of runbook 01, and the route `POST /drill/manual` (agent.md §3.8.28); the CLI `aictl drill record-manual` is in AICTL-10 ([aictl.md](aictl.md) §10). Acceptance: fake cloud receives the summary; runbook dry-run on ai-n3.

## 11. Open questions (with proposed defaults)
1. ~~**New events and command.**~~ Resolved (2026-11-02): agent.md §3.10 lists `drill.started`, `drill.check_done`, `drill.done`; agent.md §3.8.28 adds `POST /drill/manual`; aictl.md §4.17 adds `aictl drill show` and `aictl drill record-manual`.
2. **Cloud-triggered drills.** *Default:* not now (no `localai.drill.run` job type); the cloud only displays results.
