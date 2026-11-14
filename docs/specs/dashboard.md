# Spec: Dashboard (Manage this computer) and in-app help

> **Status:** approved design · **Epic:** E10 (#492) · **Decision:** [ADR-0011](../decisions/0011-full-control-from-the-app.md) · **Code:** `agent/ui/src/portal/` (the same SPA as the Survival Portal), `apps/desktop/src/main/` (menu, tray, privileged buttons) · **Doctor area:** none new (each screen shows the codes of the routes it calls)
> Canonical names: [overview.md](../architecture/overview.md) §3. Modes: [modes.md](../architecture/modes.md). API: [agent.md](agent.md) §3.8. Portal: [survival-portal.md](survival-portal.md). Desktop: [desktop.md](desktop.md). Rules: [ADR-0009](../decisions/0009-optimized-for-humans.md).

---

## 1. Purpose and scope

The person who must operate Local AI in a disaster may never have used a terminal and may not know the admin PIN. Everything an operator can do with `aictl` must therefore be possible from the ai-desktop app, by reading the screen and pressing buttons, offline, in English or Spanish ([ADR-0011](../decisions/0011-full-control-from-the-app.md)).

The Dashboard is **not a second app**. It is the Survival Portal SPA with two additions:
1. **Home in every mode.** `/` answers `302 /portal`, the Portal's Home (the placeholder `agent/ui/index.html` is removed; [agent.md](agent.md) §3.11). The status line is shown in every mode, and the Manage row for an operator session in every mode, survival included (the person who must run the ark in an emergency uses the app's operator session). A Portal session (a browser without the token) sees Home exactly as [survival-portal.md](survival-portal.md) §4.1.1.
2. **Manage this computer**, a row on Home that opens six screens: **Mode**, **Offline content**, **AI models**, **Health**, **System**, and **Help**.

In scope: the screens, the REST route behind every button, the PIN policy, the in-app guide, the language default, the desktop entry points (menu, tray), and the tests.
Non-goals: cloud and lab features (they stay in the desktop's Lab tab and the cloud app), new agent capabilities (every button uses an existing route or the desktop's fixed-argv privileged actions).

## 2. Concepts

| Term | Meaning |
|---|---|
| **Operator session** | The full session the desktop creates from the agent token (desktop.md §4.1), or a session from a one-time UI code. Everything on these screens needs it. |
| **Portal session** | The restricted cookie of survival mode for a browser without the token ([survival-portal.md](survival-portal.md) §3.3). It sees Home, Library, Maps, Manuals, Status and Help; the Manage screens show "Open this in the Local AI app to change settings." |
| **PIN action** | A button that deletes something or changes a lab-only setting: it asks the admin PIN (`POST /api/v1/pin/verify`) first, after a sentence that says what will be lost. |
| **Privileged action** | A root-only task with no REST route (agent update, kit install, rebuild, backup restore, change PIN). In ai-desktop it runs a fixed argv through `pkexec` ([desktop.md](desktop.md) §4.2 "Buttons instead of commands"); in a plain browser the button shows the command to give to an expert. |

## 3. Interfaces

### 3.1 Routes of the SPA
| Path | Screen | Needs |
|---|---|---|
| `/` , `/portal` | Home | nothing (Portal session in survival) |
| `/portal/manage/mode` | Mode | operator session |
| `/portal/manage/content` | Offline content and drives | operator session |
| `/portal/manage/models` | AI models | operator session |
| `/portal/manage/health` | Health | operator session |
| `/portal/manage/system` | System | operator session |
| `/portal/help` | Help and guide | nothing |

### 3.2 Buttons and the routes they call
Auth levels are those of [agent.md](agent.md) §3.8 (route table). "PIN" means the button asks the PIN first.

| Screen | Button | Call | PIN |
|---|---|---|---|
| Mode | **Use the internet (online)** / **Stay offline** | `POST /mode {target}` | no |
| Mode, Home banner, Status | **Start survival mode** | `POST /mode/survival` | no |
| Mode, Home banner, Status | **Leave survival mode** | `POST /mode {target: "online"}` (or `offline`, chosen in the dialog) | **no** for an operator session (ADR-0011); a Portal session sees "Open this in the Local AI app" |
| Offline content | **Sync now** / **Stop** | `POST /ark/sync` / `DELETE /ark/sync` | no |
| Offline content | **Check content** | `POST /ark/verify` | no |
| Offline content | **Copy from the AI-ARK drive** | `POST /ark/import` (safe default) | no |
| Offline content | **Eject safely** (per drive) | `POST /ark/drives/{root_id}/eject` | no |
| Offline content | **Install what's missing** | `POST /ensure` | no (confirming a compile or restart: PIN) |
| Offline content | **Remove content not in the plan** | `POST /ark/prune` | PIN |
| Offline content | **Prepare a new drive** | `POST /ark/drives/{root_id}/init` | PIN |
| Offline content | **Open this drive** (a connected drive that is not mounted) | `POST /ark/drives/{root_id}/mount` | no |
| AI models | **Use this brain** | `POST /models/{id}/load` | no |
| AI models | **Unload** | `POST /models/{id}/unload` | no |
| AI models | **Make default** | `PUT /models/default` | no |
| AI models | **Download** | `POST /models/{id}/download` | no |
| AI models | **Delete** | `DELETE /models/{id}` | PIN |
| Health | **Run a full check** | `GET /doctor` | no |
| Health | each problem's fix | the alert's `action` (survival-portal.md §4.1.8) | as the action |
| Health | **Show recent events** | `GET /logs` | no |
| Health | **Run a practice drill** / **Stop** | `POST /drill` / `DELETE /drill`; result `GET /drill` | no |
| System | **Restart** (per service) | `POST /services/{name}/restart` | no |
| System | **Get the newest catalog** | `POST /catalog/refresh` | no |
| System | **Update Local AI** | privileged: `pkexec /opt/localai/bin/aiagent ctl update --yes` | no (system password) |
| System | **Check the rebuild kit** | privileged: `pkexec /opt/localai/bin/aiagent ctl kit verify` | no (system password) |
| System | **Save settings to a file** / **Restore settings** | privileged: `aiagent ctl backup-config` / `--restore` | restore: PIN |
| System | **Change the admin PIN** | privileged: `aiagent ctl pin set` | system password |
| Help | **What this app can do**, **Emergency steps**, **Manuals** | embedded pages; `/docs/` for the manuals | no |

### 3.3 Desktop entry points
- The ⋮ menu gains **Help** (opens `/portal/help`) and **Manage this computer** (opens `/portal/manage/mode`).
- The tray gains **Help** and **Leave survival mode** (no PIN, ADR-0011) while in survival.
- The top bar's mode chip becomes a button that opens the Mode screen.

## 4. Behaviour

### 4.1 Home (all modes)
```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ LOCAL AI · ONLINE                       [ English | Español ]   [A A+ A++]    14:05   ⚡ 72 %  │
│ ● Ready. Brain: Qwen 3.5 9B · Library ✓ · Maps ✓ · Voice ✓        [ Help ]                   │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│   (the eight big buttons of survival-portal.md §4.1.1)                                          │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│ MANAGE THIS COMPUTER   [Mode] [Offline content] [AI models] [Health] [System]                 │
│ FIRST AID  [CPR] [Choking] [Severe bleeding] [Burns] [Broken bone] [Snake bite] [More …]  (F) │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```
- **State first** (ADR-0009): the status line says green "Ready", amber "Ready, with problems" or red "Not ready", and when it is not green, the **one next thing** with its button (the first alert's action, else **Run a full check**).
- In survival mode the header says SURVIVAL and a **Leave survival mode** button sits at the right of the status line (operator session only).
- `M` opens Manage (the Mode screen), `H` opens Help; `Esc` returns Home from every screen. Each Manage screen starts with a row of tabs (Mode, Offline content, AI models, Health, System, Help), so moving between them never needs Home.
- **As built (#497).** Code: `agent/ui/src/portal/manage/` (`ManageFrame`, `ManageLocked`, `ConfirmDialog`, `PinDialog` + `usePin`, `useAction`, `FailureNote`, one `<Name>Screen.tsx` per screen, `screens.tsx` the dispatcher), the gate in `view.ts` (`manage-locked` unless `session` is `full`), the Manage row in `screens/Home.tsx`, the next step and Help in `components/StatusLine.tsx`. The PIN grant is kept in memory only, until 30 s before it expires (`manage/pin.ts`). The Playwright fake keeps each screen's routes in `e2e/fake-manage/<name>.mjs`.

### 4.2 Mode
Three large choices with one sentence each: **Online** ("Uses the internet to update content and talk to the lab"), **Offline** ("No internet. Everything here keeps working"), **Survival** ("Emergency screen with the basics. Starts at once"). The current one is marked. Choosing one opens a confirmation that says what changes (modes.md §1) and, for leaving survival, the destination (Online or Offline). No PIN for an operator session.

**As built (#494).** `manage/ModeScreen.tsx` reads `GET /mode` every 10 s, every 2 s while `transition` is set (then the choices wait and a line says "Changing to … This can take a few minutes"); `suggest_survival` shows the no-internet hint. Survival uses `POST /mode/survival {}`, the others `POST /mode {target}` (`manage/mode.ts`). Leaving survival, from this screen or from the status line's **Leave survival mode** button (survival mode, operator session only), opens one dialog (`manage/LeaveSurvival.tsx`) with Online / Offline preselected from `connectivity` (Online only when it is `full`); a Portal session gets no button. A refusal shows its code and fix (`409 LAI-AGT-022` while another change runs).

### 4.3 Offline content
Top: the readiness of each mounted root in one line ("Internal disk: ready" / "98 % ready, 1 item missing") and the last sync. Then:
- A list grouped by kind (Maps, Library, AI models, Speech, Rebuild kit), each item with its status in plain words (`verified` → "Ready", `missing` → "Not downloaded yet", `quarantined` → "Damaged copy set aside; it will be downloaded again", `blocked` → "Skipped until a new version is published"), its size, and its doctor code when there is one.
- **Sync now** (online only; offline shows "Needs the internet"), with progress per item and **Stop**.
- **Check content**, **Install what's missing**.
- **Drives**: each connected AI-ARK drive with its readiness, **Copy from this drive**, **Eject safely** (the result says when it is safe to unplug), **Open this drive** for one that is connected but not mounted, and **Prepare a new drive** for an empty one (PIN; says what it does: it adds `localai/` and `ROOT.json`, deletes nothing, and is refused on a drive whose `localai/` holds files, [ark-sync.md](ark-sync.md) §3.5; erasing a drive is `aictl ark format`, §4.17.2, not this button).
- **Remove content not in the plan** (PIN): a dry run first, then a confirmation that lists every item that would be deleted, its size, and the copies kept because they are the last good one.
- **As built (#496).** Code: `agent/ui/src/portal/manage/ContentScreen.tsx` (the screen), `content.ts` (the plain-word rules: groups, item and drive states, the readiness line, why a button is disabled, the eject outcome) and `contentApi.ts` (the route of each button), unit-tested in `content.test.ts` and `contentApi.test.ts`; Playwright `e2e/manage-content.spec.ts` over `e2e/fake-manage/content.mjs`. Data: `GET /ark` (each root's READY: `root.kind`, `overall`, `last_sync.finished_at`, `items[]` with `id`, `kind`, `status`, `reason`, `code`, `bytes`, and `problems[]` with `item` and `title`), `GET /ark/drives` (`root_id`, `mount`, `label`, `size_bytes`, `free_bytes`, `state`, `ready.overall`, `problems[]` with `title`, `fix`, `code`) and `GET /ark/sync` (404 or 503 = no run yet), reread every 2 s while a run is active or a drive is ejecting, else every 10 s. Groups: READY `kind` `maps` → Maps, `kiwix` → Library, `model` → AI models except ids starting with `whisper-` or `piper-` → Speech, `kit` → Rebuild kit, `docs` → Manuals, anything else → Other. READY has no `blocked` status: an item is shown as "Skipped until a new version is published" when the last ended run lists it in `items[]` with `reason: "blocked"` (ark-sync.md §4.5). The readiness line counts `verified` and `stale` as present and leaves orphans out; it never says 100 % while an item is missing. **Sync now** sends `{"trigger": "manual"}` and is disabled outside online mode ("Needs the internet"); **Stop** is `DELETE /ark/sync` for any run (sync, check or copy); **Check content** sends `{}`; **Copy from this drive** sends `{"from_root": <root_id>}`, and is offered only for a `ready` or `read_only` drive. **Eject safely** sends `{"force": false}`; after the `202` the screen watches `GET /ark/drives` until the drive is gone or `not_mounted` ("you can unplug the drive now"), or back in a mounted state (not ejected, `LAI-ARK-009`, after at most 2 minutes). A `409 LAI-ARK-009` lists `details.blockers` by name, and a `model` blocker offers **Eject anyway** (`force: true`) after a confirmation. **Prepare a new drive** and **Remove content not in the plan** send the PIN grant header; prune sends `{"orphans": true, "trash": true, "dry_run": …}`, the dry run first. **Install what's missing** is the Portal's `EnsureCard`. Every disabled button says why under it.

### 4.4 AI models
A row per model: name, size, state (loaded, downloaded, not downloaded), "survival" tag. Buttons per §3.2. Delete explains "This removes the model from this computer. You can download it again later with the internet, or copy it from the AI-ARK drive." then asks the PIN.
- **As built (#498).** Code: `agent/ui/src/portal/manage/ModelsScreen.tsx` over the rules in `manage/models.ts` (unit tests `models.test.ts`). State first: a line says which brain is in use and which starts with the computer. The list is `GET /models` (all engines), re-read every 10 s and every 1.5 s while a load, a download or another button runs. Rows: brains on this computer or in its plan first (in use, then on disk, then not downloaded), the rest of the catalog under a closed **More models** fold, speech models (whisper, piper) last with only **Download** and **Delete**. `status` reads `loaded`/`sleeping` "In use", `loading` "Loading …", `stored`/`unloaded` "On this computer", `failed` "On this computer. It could not start last time.", `absent` "Not downloaded". The **Survival brain** tag is `brain.choices[].survival` of the Portal status (the profile's `survival.models`); **Default** is `default`; "Fits this computer" unless `loadable_reason.code` is `LAI-CAT-005`, whose message is shown. A row shows only the buttons that apply (Use this brain or Unload, Make default, Download when absent, Delete when present); a disabled one names its reason under the buttons (`aria-describedby`): too big, the AI service is down (`LAI-LLM-004`), files not checked (`LAI-LLM-008`), already the default, download first, unload first, "Needs the internet" (any mode but online), another task or download running. **Use this brain** and **Unload** confirm first; the load is `POST /models/{id}/load {"wait": false, "unload_others": true}`, then the list is polled until the model is in use: `failed`, or `loading` that ended otherwise, or no `loading` seen in 30 s is `LAI-LLM-009`, more than 300 s is `LAI-LLM-010`. **Download** confirms with the size, posts `{}` and follows `GET /ark/sync` for the answer's `sync_run_id` (the item's `bytes_done`/`bytes_total` as a percent; a run that is not this one shows "Downloading …"), ending when the model is `present` or the run ends (an item `failed`, or `skipped` with a code, is the failure with that code). **Delete** shows the sentence above and the size, then the PIN (`usePin`), then `DELETE /models/{id}?root=internal&keep_last=true` with the grant: it removes the copy on this computer's own disk only, never the AI-ARK drive's. When that is the last verified copy (`409 LAI-SYN-044`) a second confirmation ("This is the last copy … you can only get it back with the internet") sends `keep_last=false`, as `aictl delete --allow-last-copy` does; an answer with an empty `deleted` says the model was only on a drive. Every failure is `FailureNote` (code, message, fix) under its row. The Playwright fake is `e2e/fake-manage/models.mjs` (`POST /__models?fail=<op>`).

### 4.5 Health
**Run a full check** shows the doctor's checks: passed count, then each problem with its code, title, fix and fix button. **Show recent events** lists the last 50 log lines as sentences (time, what, code), never raw JSON. **Run a practice drill** runs the drill ([drill.md](drill.md)) with progress and shows the last result.

### 4.6 System
Versions (app, agent, catalog), services with state and **Restart**, updates (agent, catalog), rebuild kit status and **Check the rebuild kit**, settings backup and restore, and **Change the admin PIN**. Each privileged button explains that the computer will ask for the administrator (login) password.

### 4.7 Help and the guide
Embedded in the bundle (works offline), both languages, reachable from Home, the desktop menu and the tray:
1. **What this app can do**: one paragraph per Home and Manage button, with its exact label.
2. **Emergency steps**: the four steps of the survival card (start survival, chat/library/maps, first aid, power) and the emergency number.
3. **Common tasks**, step by step with button names: go offline, start and leave survival mode, update content, copy from the AI-ARK drive, eject the drive, check health, update the app.
4. **Manuals**: the rendered runbooks at `/docs/` (survival-portal.md §4.1.7).
Never a terminal command in steps 1 to 3; a command may appear only in a closed "For an expert" box.

**As built (#495).** `screens/Help.tsx` is the guide, at `/portal/help` for every session and with the agent down: In an emergency (four steps, the emergency number, the last resort), What this app can do (one line per Home and Manage button), Step by step (one closed card per task: start and leave survival mode, go offline, update the content, copy from and eject the AI-ARK drive, check health, update Local AI, what the colours mean), Manuals (a button when they are installed) and the closed **For an expert** box with the typed commands and the library address `http://127.0.0.1:7070/library/` (the old tip pointed at port 8888, which the app blocks). The order of lines is `manage/guide.ts`; `guide.test.ts` fails if a terminal command, `sudo` or the old address appears outside that box, or if the button names drift from the screens' labels. The desktop's ⋮ menu has **Help** and **Manage this computer**, the tray has **Help** (they load `/portal/help` and `/portal/manage/mode` in the main window).

### 4.8 Language
English by default for the app and the Portal unless the person chose a language; Spanish one tap away; the choice is remembered (desktop `settings.lang`, Portal `prefs`).

**As built (#501).** ai-desktop's `resolveLang` maps `auto` (the default `settings.lang`) to English whatever the OS locale; the Portal's `resolveLang` and the map viewer's `pickLang` no longer read `navigator.language`. An explicit node setting (`portal.language`, the profile's `survival.portal_language`) still applies until the person taps a language, and a tap is remembered (`settings.lang`, `lai.portal.lang`).

## 5. Failure handling and edge cases
| Situation | Behaviour |
|---|---|
| A Manage screen opened with only a Portal session | Shows "Open this in the Local AI app to change settings." and the Help button |
| A button whose route answers an error | The doctor code, the one-sentence fix and the fix button of the code; the screen stays usable |
| Online-only button while offline or in survival | Disabled with the reason ("Needs the internet") |
| The agent is down | The desktop's agent-down page (desktop.md §4.2); Help still opens (embedded) |
| PIN wrong | "That PIN is not right." with the remaining tries (agent.md §3.8.3) |
| Privileged action without pkexec (plain browser) | The button shows the command under "For an expert" |

## 6. Security considerations
- Leaving survival without the PIN is allowed for operator principals only (ADR-0011); the Portal session and the cloud still need it. The refusal stays audited (`mode.change` `denied`).
- Deletes, drive init and prune keep the PIN (AGENTS.md rule 3).
- No new routes or privileges: the SPA calls the existing API with the existing auth; privileged actions are the desktop's fixed argv, never built from page input.

## 7. Test plan
- Unit (Vitest): every screen renders in both languages; the route table of §3.2 (each button calls its route with the right body); PIN dialog before every PIN action; disabled-with-reason rules; the guide has no terminal command outside "For an expert".
- Agent (Go): `POST /mode` out of survival with an operator bearer, an operator session, a socket peer (allowed) and a Portal session, no credential, the cloud (refused, `pin_required`).
- E2E (Playwright, fake agent): Home in online mode shows Manage; leave survival from Home without a PIN; Sync now/Stop; Eject; model delete asks the PIN.
- Manual on a node (rule 4): a first-time user does every task of §3.2 only with the app, offline; the PR records it.

## 8. Implementation breakdown
| Issue | Scope |
|---|---|
| #493 | This spec and ADR-0011 |
| #494 | Leave survival and switch modes in the window, no PIN for the app |
| #495 | Help and the guide; desktop menu and tray entries |
| #496 | Offline content and drives screen |
| #497 | Home in every mode (remove the placeholder) |
| #498 | AI models screen |
| #499 | Health screen |
| #500 | System screen |
| #501 | English by default |

## 9. Copy (all user-visible strings of these screens)

Every string of the Manage screens, Home's additions and the guide lives in `agent/ui/src/portal/i18n/manage/<name>.{en,es}.json`; the tables below are those files, one per screen, and `i18n.test.ts` fails when they differ. `node agent/ui/scripts/manage-copy.mjs <name>` prints a table from its files. The Portal's own strings stay in [survival-portal.md](survival-portal.md) §4.9.

### 9.1 Shared (Home, frame, PIN)
<!-- copy:common -->
| Key | English | Español |
|---|---|---|
| `manage.title` | Manage this computer | Administrar esta computadora |
| `manage.mode` | Mode | Modo |
| `manage.content` | Offline content | Contenido sin conexión |
| `manage.models` | AI models | Modelos de IA |
| `manage.health` | Health | Salud |
| `manage.system` | System | Sistema |
| `manage.help` | Help | Ayuda |
| `manage.locked` | Open this in the Local AI app to change settings. | Abra esto en la aplicación Local AI para cambiar la configuración. |
| `manage.lockedWhy` | This screen changes how the computer works, so it needs the Local AI app on this computer. | Esta pantalla cambia cómo funciona la computadora, así que necesita la aplicación Local AI de esta computadora. |
| `manage.back` | Back to Home | Volver al inicio |
| `manage.working` | Working … | Trabajando … |
| `manage.done` | Done. | Listo. |
| `manage.failed` | That did not work: {message} | No funcionó: {message} |
| `manage.needsInternet` | Needs the internet | Necesita internet |
| `manage.confirm` | Yes, do it | Sí, hacerlo |
| `manage.expert` | For an expert | Para un experto |
| `home.mode.online` | Local AI · Online | IA Local · En línea |
| `home.mode.offline` | Local AI · Offline | IA Local · Sin conexión |
| `home.next` | Next step: | Siguiente paso: |
| `home.runCheck` | Run a full check | Hacer una revisión completa |
| `home.leaveSurvival` | Leave survival mode | Salir del modo supervivencia |
| `pin.title` | Admin PIN needed | Se necesita el PIN de administrador |
| `pin.why` | This removes something from this computer, so it needs the admin PIN. | Esto borra algo de esta computadora, así que necesita el PIN de administrador. |
| `pin.label` | Admin PIN | PIN de administrador |
| `pin.ok` | Continue | Continuar |
| `pin.wrong` | That PIN is not right. {n} tries left. | Ese PIN no es correcto. Quedan {n} intentos. |
| `pin.wrongNoCount` | That PIN is not right. | Ese PIN no es correcto. |
| `pin.locked` | Too many wrong tries. Wait {s} seconds and try again. | Demasiados intentos incorrectos. Espere {s} segundos y vuelva a intentarlo. |
| `pin.notSet` | No admin PIN is set on this computer. An expert can set one in the System screen. | Esta computadora no tiene PIN de administrador. Un experto puede ponerlo en la pantalla Sistema. |
<!-- /copy -->

### 9.2 Mode
<!-- copy:mode -->
| Key | English | Español |
|---|---|---|
| `mode.title` | Mode | Modo |
| `mode.lead` | Choose how this computer works. You can change it again at any time. | Elija cómo funciona esta computadora. Puede cambiarlo otra vez cuando quiera. |
| `mode.online` | Online | En línea |
| `mode.online.what` | Uses the internet to update content and talk to the lab. | Usa internet para actualizar el contenido y hablar con el laboratorio. |
| `mode.offline` | Offline | Sin conexión |
| `mode.offline.what` | No internet. Everything on this computer keeps working. | Sin internet. Todo lo que hay en esta computadora sigue funcionando. |
| `mode.survival` | Survival | Supervivencia |
| `mode.survival.what` | Emergency screen with the basics. Starts at once. | Pantalla de emergencia con lo básico. Empieza de inmediato. |
| `mode.current` | In use now | En uso ahora |
| `mode.choose` | Use {mode} | Usar {mode} |
| `mode.changing` | Changing to {mode} … This can take a few minutes. You can keep using the computer. | Cambiando a {mode} … Puede tardar unos minutos. Puede seguir usando la computadora. |
| `mode.changed` | Done. The computer is now {mode}. | Listo. La computadora ahora está en {mode}. |
| `mode.warnings` | Finished with notes: | Terminó con notas: |
| `mode.suggest` | There has been no internet for more than 30 minutes. Survival mode keeps the basics ready. | No hay internet desde hace más de 30 minutos. El modo supervivencia deja lo básico listo. |
| `mode.confirm.title` | Change to {mode}? | ¿Cambiar a {mode}? |
| `mode.confirm.online` | The computer will use the internet again: content updates at night, and the lab can see it. | La computadora vuelve a usar internet: el contenido se actualiza de noche y el laboratorio puede verla. |
| `mode.confirm.offline` | The computer stops using the internet. Chat, library, maps and voice keep working. | La computadora deja de usar internet. El chat, la biblioteca, los mapas y la voz siguen funcionando. |
| `mode.confirm.survival` | The emergency screen opens full screen with the basics: chat, library, maps, first aid. The survival brain is loaded. | La pantalla de emergencia se abre en pantalla completa con lo básico: chat, biblioteca, mapas, primeros auxilios. Se carga el cerebro de supervivencia. |
| `mode.leave.title` | Leave survival mode? | ¿Salir del modo supervivencia? |
| `mode.leave.lead` | The emergency screen closes and the normal screen comes back. Nothing is deleted. | La pantalla de emergencia se cierra y vuelve la pantalla normal. No se borra nada. |
| `mode.leave.where` | Change to: | Cambiar a: |
| `mode.leave.online` | Online (there is internet) | En línea (hay internet) |
| `mode.leave.offline` | Offline (no internet yet) | Sin conexión (todavía no hay internet) |
| `mode.leave.go` | Leave survival mode | Salir del modo supervivencia |
| `mode.busy` | Another change is still running. Wait for it to finish, then try again. | Todavía hay otro cambio en curso. Espere a que termine y vuelva a intentarlo. |
| `mode.portalOnly` | To leave survival mode, use the Local AI app on this computer. | Para salir del modo supervivencia, use la aplicación Local AI de esta computadora. |
<!-- /copy -->

### 9.3 Offline content
<!-- copy:content -->
| Key | English | Español |
|---|---|---|
| `content.title` | Offline content and drives | Contenido sin conexión y discos |
| `content.lead.ok` | Everything is here and checked. It works without the internet. | Todo está aquí y revisado. Funciona sin internet. |
| `content.lead.noRoot` | No disk with content is ready. Plug in the AI-ARK drive, or run Install what's missing. | No hay ningún disco con contenido listo. Conecte el disco AI-ARK o use Instalar lo que falta. |
| `content.lead.nextSync` | Some content is missing. Next step: press Sync now. | Falta parte del contenido. Siguiente paso: presione Sincronizar ahora. |
| `content.lead.nextCopy` | Some content is missing. Next step: press Copy from this drive. | Falta parte del contenido. Siguiente paso: presione Copiar de este disco. |
| `content.lead.nextConnect` | Some content is missing. Next step: connect to the internet, or plug in the AI-ARK drive. | Falta parte del contenido. Siguiente paso: conéctese a internet o conecte el disco AI-ARK. |
| `content.ready.title` | Is it ready? | ¿Está listo? |
| `content.root.internal` | Internal disk | Disco interno |
| `content.root.drive` | AI-ARK drive ({id}) | Disco AI-ARK ({id}) |
| `content.root.ready` | {name}: ready | {name}: listo |
| `content.root.readyWarn` | {name}: ready, with a warning | {name}: listo, con un aviso |
| `content.root.partOne` | {name}: {pct} % ready, 1 item missing | {name}: {pct} % listo, falta 1 elemento |
| `content.root.partMany` | {name}: {pct} % ready, {n} items missing | {name}: {pct} % listo, faltan {n} elementos |
| `content.lastSync` | Last sync: {when} | Última sincronización: {when} |
| `content.lastSync.never` | No sync yet | Todavía no se ha sincronizado |
| `content.run.title` | Update and check | Actualizar y revisar |
| `content.run.none` | Nothing has run yet. | Todavía no se ha hecho nada. |
| `content.run.syncing` | Downloading the newest content … | Descargando el contenido más nuevo … |
| `content.run.verifying` | Checking the content … | Revisando el contenido … |
| `content.run.importing` | Copying from the AI-ARK drive … | Copiando del disco AI-ARK … |
| `content.run.stopping` | Stopping … What is half done is kept and continues next time. | Deteniendo … Lo que quedó a medias se guarda y continúa la próxima vez. |
| `content.run.ok` | The last run finished well. | La última ejecución terminó bien. |
| `content.run.partial` | The last run finished, but {n} items did not work. The older copies are kept. | La última ejecución terminó, pero {n} elementos no funcionaron. Se guardan las copias anteriores. |
| `content.run.partialOne` | The last run finished, but 1 item did not work. The older copy is kept. | La última ejecución terminó, pero 1 elemento no funcionó. Se guarda la copia anterior. |
| `content.run.partialNoCount` | The last run finished, but some items were left for next time. The older copies are kept. | La última ejecución terminó, pero algunos elementos quedaron para la próxima vez. Se guardan las copias anteriores. |
| `content.run.cancelled` | The last run was stopped. It continues where it left off next time. | La última ejecución se detuvo. La próxima vez continúa donde quedó. |
| `content.run.skipped` | The last run did not start. | La última ejecución no empezó. |
| `content.run.failed` | The last run did not work. | La última ejecución no funcionó. |
| `content.run.when` | Finished: {when} | Terminó: {when} |
| `content.progress` | {done} of {total} items, {pct} % | {done} de {total} elementos, {pct} % |
| `content.eta` | About {time} left | Faltan unos {time} |
| `content.active.resolving` | finding the newest version | buscando la versión más nueva |
| `content.active.downloading` | downloading | descargando |
| `content.active.verifying` | checking | revisando |
| `content.active.installing` | putting in place | instalando |
| `content.active.waiting` | waiting | esperando |
| `content.active.bytes` | {done} of {total} | {done} de {total} |
| `content.sync` | Sync now | Sincronizar ahora |
| `content.sync.started` | The download started. You can keep using the computer. | La descarga empezó. Puede seguir usando la computadora. |
| `content.stop` | Stop | Detener |
| `content.stop.done` | Stopping. Nothing is lost; it continues next time. | Deteniendo. No se pierde nada; continúa la próxima vez. |
| `content.verify` | Check content | Revisar el contenido |
| `content.verify.started` | The check started. | La revisión empezó. |
| `content.verify.what` | Check content reads every copy on this computer and the drives and sets aside a damaged one. It works without the internet. | Revisar el contenido lee cada copia en esta computadora y en los discos y aparta las dañadas. Funciona sin internet. |
| `content.whyLine` | {button}: {why} | {button}: {why} |
| `content.why.busy` | Wait until the current work finishes, or press Stop. | Espere a que termine el trabajo actual, o presione Detener. |
| `content.why.unclean` | This drive needs a repair first. | Primero hay que reparar este disco. |
| `content.why.empty` | This drive is empty. Prepare it first. | Este disco está vacío. Prepárelo primero. |
| `content.why.notMounted` | Open the drive first. | Primero abra el disco. |
| `content.why.notMountedEject` | The drive is not open, so you can unplug it now. | El disco no está abierto, así que puede desconectarlo ahora. |
| `content.why.ejecting` | The drive is being ejected. | El disco se está expulsando. |
| `content.why.unusable` | Local AI cannot use this drive. | Local AI no puede usar este disco. |
| `content.items.title` | What is on each disk | Qué hay en cada disco |
| `content.items.on` | {name} ({n} items) | {name} ({n} elementos) |
| `content.items.empty` | Nothing is planned for this disk. | No hay nada planeado para este disco. |
| `content.group.maps` | Maps | Mapas |
| `content.group.library` | Library | Biblioteca |
| `content.group.models` | AI models | Modelos de IA |
| `content.group.speech` | Speech | Voz |
| `content.group.kit` | Rebuild kit | Kit de reconstrucción |
| `content.group.docs` | Manuals | Manuales |
| `content.group.other` | Other | Otros |
| `content.item.verified` | Ready | Listo |
| `content.item.stale` | Ready; a newer version is not downloaded yet | Listo; todavía no se descargó una versión más nueva |
| `content.item.missing` | Not downloaded yet | Todavía no se ha descargado |
| `content.item.quarantined` | Damaged copy set aside; it will be downloaded again | Copia dañada apartada; se descargará de nuevo |
| `content.item.blocked` | Skipped until a new version is published | Se omite hasta que se publique una versión nueva |
| `content.item.failed` | The download did not work; it will be tried again | La descarga no funcionó; se intentará de nuevo |
| `content.item.orphan` | Not in the plan; it can be removed to free space | No está en el plan; se puede quitar para liberar espacio |
| `content.item.skipped` | Skipped this time | Se omitió esta vez |
| `content.item.skippedBudget` | Skipped: it does not fit in the space set aside | Se omitió: no cabe en el espacio reservado |
| `content.item.skippedDisk` | Skipped: the disk is too full | Se omitió: el disco está demasiado lleno |
| `content.item.skippedTime` | Not reached yet; the next sync continues | Todavía no se alcanzó; la próxima sincronización continúa |
| `content.item.unknown` | Unknown state | Estado desconocido |
| `content.drives.title` | AI-ARK drives | Discos AI-ARK |
| `content.drives.none` | No AI-ARK drive is connected. Plug one in to copy content from it or to keep a copy on it. | No hay ningún disco AI-ARK conectado. Conecte uno para copiar contenido de él o guardar una copia en él. |
| `content.drive.name` | {label}, {size} | {label}, {size} |
| `content.drive.noLabel` | Drive without a name | Disco sin nombre |
| `content.drive.free` | Free space: {free} | Espacio libre: {free} |
| `content.drive.ready` | Ready | Listo |
| `content.drive.readyUnchecked` | Ready; not checked yet | Listo; todavía sin revisar |
| `content.drive.readyWarn` | Ready, with a warning | Listo, con un aviso |
| `content.drive.incomplete` | Not complete. Sync now fills it. | Incompleto. Sincronizar ahora lo completa. |
| `content.drive.readOnly` | Read only: content can be copied from it, not to it | Solo lectura: se puede copiar contenido de él, no hacia él |
| `content.drive.unclean` | Needs a repair before it can be used | Necesita una reparación antes de usarse |
| `content.drive.notMounted` | Connected but not open | Conectado pero no abierto |
| `content.drive.ejecting` | Ejecting … | Expulsando … |
| `content.drive.uninitialized` | Empty: not prepared for Local AI yet | Vacío: todavía no está preparado para Local AI |
| `content.drive.unusable` | Cannot be used | No se puede usar |
| `content.copy` | Copy from this drive | Copiar de este disco |
| `content.copy.started` | Copying started. Nothing on this computer is deleted. | La copia empezó. No se borra nada de esta computadora. |
| `content.mount` | Open this drive | Abrir este disco |
| `content.mount.done` | The drive is open. | El disco está abierto. |
| `content.eject` | Eject safely | Expulsar con seguridad |
| `content.eject.started` | Ejecting. Do not unplug the drive yet. | Expulsando. Todavía no desconecte el disco. |
| `content.eject.waiting` | Wait: the drive is finishing. Do not unplug it yet. | Espere: el disco está terminando. Todavía no lo desconecte. |
| `content.eject.safe` | {name}: you can unplug the drive now. | {name}: ya puede desconectar el disco. |
| `content.eject.notDone` | The drive could not be ejected. Close what uses it and press Eject safely again. | No se pudo expulsar el disco. Cierre lo que lo usa y presione Expulsar con seguridad otra vez. |
| `content.eject.blockers` | Still using the drive: {names} | Todavía usan el disco: {names} |
| `content.eject.force` | Eject anyway | Expulsar de todos modos |
| `content.eject.forceTitle` | Eject the drive anyway? | ¿Expulsar el disco de todos modos? |
| `content.eject.force1` | The brain running from this drive will be stopped. | Se detendrá el cerebro que funciona desde este disco. |
| `content.eject.force2` | You can choose a brain again in AI models after that. | Después puede elegir un cerebro otra vez en Modelos de IA. |
| `content.init` | Prepare a new drive | Preparar un disco nuevo |
| `content.init.confirmTitle` | Prepare {name} for Local AI? | ¿Preparar {name} para Local AI? |
| `content.init.confirm1` | This makes the drive an AI-ARK drive: it adds a localai folder and a label file to it. | Esto convierte el disco en un disco AI-ARK: le agrega una carpeta localai y un archivo de etiqueta. |
| `content.init.confirm2` | Nothing already on the drive is deleted. A drive that already has a localai folder with files is refused. | No se borra nada de lo que ya hay en el disco. Si el disco ya tiene una carpeta localai con archivos, se rechaza. |
| `content.init.confirm3` | Afterwards, Sync now or Copy fills it. It needs the admin PIN. | Después, Sincronizar ahora o Copiar lo llenan. Necesita el PIN de administrador. |
| `content.init.confirmOk` | Yes, prepare it | Sí, prepararlo |
| `content.init.pinWhy` | Preparing a drive changes it, so it needs the admin PIN. | Preparar un disco lo cambia, así que necesita el PIN de administrador. |
| `content.init.done` | The drive is ready for Local AI. Press Sync now to fill it. | El disco está listo para Local AI. Presione Sincronizar ahora para llenarlo. |
| `content.prune.title` | Free up space | Liberar espacio |
| `content.prune.what` | Removes content that is no longer in the plan and old copies that a newer one replaced. The last good copy of anything is always kept. | Quita el contenido que ya no está en el plan y las copias viejas que una más nueva reemplazó. Siempre se guarda la última copia buena de todo. |
| `content.prune` | Remove content not in the plan | Quitar el contenido que no está en el plan |
| `content.prune.pinWhy` | This deletes content from this computer, so it needs the admin PIN. First you will see exactly what would be removed. | Esto borra contenido de esta computadora, así que necesita el PIN de administrador. Primero verá exactamente qué se quitaría. |
| `content.prune.checked` | Checked what can be removed. | Se revisó qué se puede quitar. |
| `content.prune.nothing` | Nothing to remove: everything here is in the plan. | No hay nada que quitar: todo lo que hay está en el plan. |
| `content.prune.confirmTitle` | Remove this content? | ¿Quitar este contenido? |
| `content.prune.confirm1` | This deletes {n} items ({size}) listed below. | Esto borra {n} elementos ({size}) de la lista de abajo. |
| `content.prune.confirmOne` | This deletes 1 item ({size}) listed below. | Esto borra 1 elemento ({size}) de la lista de abajo. |
| `content.prune.confirm2` | They cannot be brought back without the internet or an AI-ARK drive that has them. | No se pueden recuperar sin internet o sin un disco AI-ARK que los tenga. |
| `content.prune.kindOrphan` | not in the plan | no está en el plan |
| `content.prune.kindTrash` | old copy | copia vieja |
| `content.prune.kept` | Kept, because it is the only good copy: {names} | Se guarda, porque es la única copia buena: {names} |
| `content.prune.confirmOk` | Yes, remove it | Sí, quitarlo |
| `content.prune.done` | Done. Space freed: {size}. | Listo. Espacio liberado: {size}. |
<!-- /copy -->

### 9.4 AI models
<!-- copy:models -->
| Key | English | Español |
|---|---|---|
| `models.title` | AI models | Modelos de IA |
| `models.lead` | The brains (AI models) of this computer. Choose which one answers, which one starts with the computer, and download or remove them. | Los cerebros (modelos de IA) de esta computadora. Elija cuál responde, cuál arranca con la computadora, y descárguelos o bórrelos. |
| `models.inUseNow` | In use now: {name} | En uso ahora: {name} |
| `models.noneInUse` | No brain is in use. Chat will not answer until you press Use this brain on one of them. | No hay ningún cerebro en uso. El chat no responderá hasta que presione Usar este cerebro en uno de ellos. |
| `models.defaultIs` | Starts with the computer: {name} | Arranca con la computadora: {name} |
| `models.noDefault` | No brain is set to start with the computer. Press Make default on one of them. | Ningún cerebro arranca con la computadora. Presione Hacer predeterminado en uno de ellos. |
| `models.listFailed` | The list of models could not be read. | No se pudo leer la lista de modelos. |
| `models.retry` | Try again | Intentar de nuevo |
| `models.empty` | No AI model is known on this computer. | Esta computadora no conoce ningún modelo de IA. |
| `models.more` | More models (not planned for this computer): {n} | Más modelos (no previstos para esta computadora): {n} |
| `models.speech` | Speech models (for Talk) | Modelos de voz (para Hablar) |
| `models.state.inUse` | In use | En uso |
| `models.state.loading` | Loading … | Cargando … |
| `models.state.onComputer` | On this computer | En esta computadora |
| `models.state.failed` | On this computer. It could not start last time. | En esta computadora. No pudo iniciar la última vez. |
| `models.state.absent` | Not downloaded | Sin descargar |
| `models.state.downloading` | Downloading … | Descargando … |
| `models.state.downloadingPct` | Downloading … {pct} % | Descargando … {pct} % |
| `models.size` | Size: {size} | Tamaño: {size} |
| `models.fits` | Fits this computer | Cabe en esta computadora |
| `models.tooBig` | Too big for this computer: {reason} | Demasiado grande para esta computadora: {reason} |
| `models.tag.default` | Default | Predeterminado |
| `models.tag.survival` | Survival brain | Cerebro de supervivencia |
| `models.tag.speech` | Speech | Voz |
| `models.use` | Use this brain | Usar este cerebro |
| `models.unload` | Unload | Detener |
| `models.makeDefault` | Make default | Hacer predeterminado |
| `models.download` | Download | Descargar |
| `models.delete` | Delete | Borrar |
| `models.why.tooBig` | Too big for this computer | Demasiado grande para esta computadora |
| `models.why.serviceDown` | The AI service is not running. Open Health and run a full check. | El servicio de IA no está funcionando. Abra Salud y haga una revisión completa. |
| `models.why.notChecked` | Its files are not checked yet. Open Offline content and check the content. | Sus archivos todavía no se revisaron. Abra Contenido sin conexión y revise el contenido. |
| `models.why.cannotStart` | This computer cannot start it now | Esta computadora no puede iniciarlo ahora |
| `models.why.busy` | Wait for the current task to finish | Espere a que termine la tarea en curso |
| `models.why.isDefault` | This is already the default | Ya es el predeterminado |
| `models.why.downloadFirst` | Download it first | Descárguelo primero |
| `models.why.unloadFirst` | It is in use. Press Unload first. | Está en uso. Presione Detener primero. |
| `models.why.downloadRunning` | Wait for the current download to finish | Espere a que termine la descarga en curso |
| `models.confirmUse.title` | Use {name}? | ¿Usar {name}? |
| `models.confirmUse.line1` | The brain in use now stops, and questions in progress stop too. | El cerebro en uso se detiene, y las preguntas en curso también. |
| `models.confirmUse.line2` | Starting a brain takes about a minute. | Iniciar un cerebro tarda cerca de un minuto. |
| `models.confirmUnload.title` | Unload {name}? | ¿Detener {name}? |
| `models.confirmUnload.line1` | Chat, Photo and Talk will not answer until a brain is in use again. | Chat, Foto y Hablar no responderán hasta que haya otra vez un cerebro en uso. |
| `models.confirmDownload.title` | Download {name}? | ¿Descargar {name}? |
| `models.confirmDownload.line1` | It needs {size} of disk space and the internet. | Necesita {size} de espacio en disco e internet. |
| `models.confirmDownload.line2` | It can take a long time. You can keep using the computer while it downloads. | Puede tardar mucho. Puede seguir usando la computadora mientras se descarga. |
| `models.confirmDelete.title` | Delete {name}? | ¿Borrar {name}? |
| `models.confirmDelete.line1` | This removes the model from this computer. You can download it again later with the internet, or copy it from the AI-ARK drive. | Esto borra el modelo de esta computadora. Puede descargarlo de nuevo más tarde con internet, o copiarlo desde el disco AI-ARK. |
| `models.confirmDelete.line2` | It frees {size}. | Libera {size}. |
| `models.confirmLast.title` | This is the last copy | Esta es la última copia |
| `models.confirmLast.line1` | There is no other checked copy of {name} on this computer or on a connected AI-ARK drive. | No hay otra copia revisada de {name} en esta computadora ni en un disco AI-ARK conectado. |
| `models.confirmLast.line2` | If you delete it, you can only get it back with the internet. | Si la borra, solo podrá recuperarla con internet. |
| `models.confirmLast.ok` | Delete the last copy | Borrar la última copia |
| `models.pinWhy` | Deleting {name} needs the admin PIN. | Borrar {name} necesita el PIN de administrador. |
| `models.working.use` | Starting {name} … this takes about a minute. | Iniciando {name} … tarda cerca de un minuto. |
| `models.done.use` | {name} is in use. | {name} está en uso. |
| `models.done.unload` | {name} was unloaded. | {name} se detuvo. |
| `models.done.default` | {name} will start each time the computer starts. | {name} arrancará cada vez que arranque la computadora. |
| `models.done.downloadStarted` | The download of {name} started. | Empezó la descarga de {name}. |
| `models.done.download` | {name} is downloaded and checked. | {name} está descargado y revisado. |
| `models.done.delete` | {name} was removed from this computer. | {name} se borró de esta computadora. |
| `models.loadFailed` | {name} could not start. The previous brain was started again. | {name} no pudo iniciar. Se volvió a iniciar el cerebro anterior. |
| `models.loadFailedFix` | Choose a smaller brain, or open Health and run a full check. | Elija un cerebro más pequeño, o abra Salud y haga una revisión completa. |
| `models.loadTimeout` | {name} did not start in 5 minutes. | {name} no inició en 5 minutos. |
| `models.downloadFailed` | The download of {name} did not finish. | La descarga de {name} no terminó. |
| `models.downloadFailedFix` | Check the internet and press Download again. Offline content shows more. | Revise internet y presione Descargar otra vez. Contenido sin conexión muestra más. |
| `models.notOnDisk` | {name} is not on this computer's own disk (only on a drive). Nothing was removed. | {name} no está en el disco propio de esta computadora (solo en un disco externo). No se borró nada. |
<!-- /copy -->

### 9.5 Health
<!-- copy:health -->
| Key | English | Español |
|---|---|---|
| `health.title` | Health | Salud |
<!-- /copy -->

### 9.6 System
<!-- copy:system -->
| Key | English | Español |
|---|---|---|
| `system.title` | System | Sistema |
<!-- /copy -->

### 9.7 Help and the guide
<!-- copy:guide -->
| Key | English | Español |
|---|---|---|
| `guide.lead` | Everything this app can do, and how, step by step. It works without internet. | Todo lo que esta aplicación puede hacer, y cómo, paso a paso. Funciona sin internet. |
| `guide.nav` | Help sections | Secciones de la ayuda |
| `guide.emergency.title` | In an emergency | En una emergencia |
| `guide.emergency.1` | **Plug in the charger** if you can. Keep the computer plugged in when possible. | **Conecte el cargador** si puede. Mantenga la computadora conectada cuando sea posible. |
| `guide.emergency.2` | Press **Start survival mode** (at the top of Home, or in **Mode**). The emergency screen opens with the basics. | Presione **Iniciar modo supervivencia** (arriba en el inicio, o en **Modo**). Se abre la pantalla de emergencia con lo básico. |
| `guide.emergency.3` | Use **Chat**, **Library**, **Maps** and **First aid** (press **F** on Home). Everything works without internet. | Use **Conversar**, **Biblioteca**, **Mapas** y **Primeros auxilios** (presione **F** en el inicio). Todo funciona sin internet. |
| `guide.emergency.4` | If something says it has a problem, press the button next to it. Every problem has a code like **LAI-SVC-001** and one sentence that says what to do. | Si algo dice que tiene un problema, presione el botón que está al lado. Cada problema tiene un código como **LAI-SVC-001** y una frase que dice qué hacer. |
| `guide.can.title` | What this app can do | Lo que esta aplicación puede hacer |
| `guide.can.chat` | **Chat**: type a question and the AI answers. It knows a lot about health, repairs, food and water. | **Conversar**: escriba una pregunta y la IA responde. Sabe mucho de salud, reparaciones, comida y agua. |
| `guide.can.photo` | **Ask about a photo**: take or choose a photo and ask about it (a plant, a wound, a label). | **Preguntar por una foto**: tome o elija una foto y pregunte sobre ella (una planta, una herida, una etiqueta). |
| `guide.can.talk` | **Talk**: speak your question and hear the answer read aloud. | **Hablar**: diga su pregunta en voz alta y escuche la respuesta. |
| `guide.can.library` | **Library**: Wikipedia, medical books and how-to guides, all on this computer. | **Biblioteca**: Wikipedia, libros de medicina y guías prácticas, todo en esta computadora. |
| `guide.can.maps` | **Maps**: an offline map with place search. | **Mapas**: un mapa sin conexión con búsqueda de lugares. |
| `guide.can.manuals` | **Manuals**: the printable instructions for this computer, including the Survival card. | **Manuales**: las instrucciones para imprimir de esta computadora, incluida la Tarjeta de supervivencia. |
| `guide.can.status` | **Status and battery**: is everything working, how much battery is left, and what is wrong. | **Estado y batería**: si todo funciona, cuánta batería queda y qué está mal. |
| `guide.can.brain` | **Switch brain**: choose another AI model, for example a faster one or one that understands photos. | **Cambiar cerebro**: elija otro modelo de IA, por ejemplo uno más rápido o uno que entiende fotos. |
| `guide.can.firstaid` | **First aid**: CPR, choking, bleeding, burns and more. It is not a doctor: if any phone works, call the emergency number. | **Primeros auxilios**: RCP, atragantamiento, sangrado, quemaduras y más. No es un médico: si algún teléfono funciona, llame al número de emergencias. |
| `guide.can.mode` | **Mode**: Online (uses the internet), Offline (no internet) or Survival (the emergency screen). Leaving survival mode needs no PIN. | **Modo**: En línea (usa internet), Sin conexión (sin internet) o Supervivencia (la pantalla de emergencia). Salir del modo supervivencia no necesita PIN. |
| `guide.can.content` | **Offline content**: what is on this computer, updating it from the internet, copying from the AI-ARK drive, and removing the drive safely. | **Contenido sin conexión**: qué hay en esta computadora, actualizarlo desde internet, copiar desde el disco AI-ARK y retirar el disco con seguridad. |
| `guide.can.models` | **AI models**: which AI models are on this computer, which one is in use, and downloading or removing them. | **Modelos de IA**: qué modelos de IA hay en esta computadora, cuál está en uso, y descargarlos o quitarlos. |
| `guide.can.health` | **Health**: a full check of the computer, recent events, and a practice drill. | **Salud**: una revisión completa de la computadora, los eventos recientes y un simulacro de práctica. |
| `guide.can.system` | **System**: versions, restarting a part that is stuck, updating Local AI, and the rebuild kit. | **Sistema**: versiones, reiniciar una parte que se trabó, actualizar Local AI y el kit de reconstrucción. |
| `guide.can.pin` | Only removing content or AI models asks for the **admin PIN**. Everything else works without it. | Solo quitar contenido o modelos de IA pide el **PIN de administrador**. Todo lo demás funciona sin él. |
| `guide.tasks.title` | Step by step | Paso a paso |
| `guide.task.survivalOn.title` | Start survival mode | Iniciar el modo supervivencia |
| `guide.task.survivalOn.1` | On Home, press **Start survival mode** (in the blue band at the top). | En el inicio, presione **Iniciar modo supervivencia** (en la franja azul de arriba). |
| `guide.task.survivalOn.2` | Wait a moment. The emergency screen opens full screen. | Espere un momento. La pantalla de emergencia se abre en pantalla completa. |
| `guide.task.survivalOff.title` | Leave survival mode (when the emergency is over) | Salir del modo supervivencia (cuando pase la emergencia) |
| `guide.task.survivalOff.1` | At the top right, press **Leave survival mode**. | Arriba a la derecha, presione **Salir del modo supervivencia**. |
| `guide.task.survivalOff.2` | Choose **Online** if there is internet, or **Offline** if there is not. | Elija **En línea** si hay internet, o **Sin conexión** si no hay. |
| `guide.task.survivalOff.3` | Press **Leave survival mode**. No PIN is needed. The normal screen comes back. | Presione **Salir del modo supervivencia**. No necesita PIN. Vuelve la pantalla normal. |
| `guide.task.offline.title` | Stop using the internet (go offline) | Dejar de usar internet (pasar a sin conexión) |
| `guide.task.offline.1` | On Home, press **Mode**. | En el inicio, presione **Modo**. |
| `guide.task.offline.2` | Press **Use Offline**, read what changes, and press **Use Offline** again. | Presione **Usar Sin conexión**, lea qué cambia y presione otra vez **Usar Sin conexión**. |
| `guide.task.update.title` | Update the content (needs internet) | Actualizar el contenido (necesita internet) |
| `guide.task.update.1` | On Home, press **Offline content**. | En el inicio, presione **Contenido sin conexión**. |
| `guide.task.update.2` | Press **Sync now**. The list shows what is downloading. You can keep using the computer. | Presione **Sincronizar ahora**. La lista muestra lo que se está descargando. Puede seguir usando la computadora. |
| `guide.task.update.3` | It also happens by itself every night while the computer is Online. | También ocurre solo cada noche mientras la computadora está En línea. |
| `guide.task.import.title` | Copy everything from the AI-ARK drive | Copiar todo desde el disco AI-ARK |
| `guide.task.import.1` | Plug in the AI-ARK drive and wait one minute. | Conecte el disco AI-ARK y espere un minuto. |
| `guide.task.import.2` | On Home, press **Offline content**. The drive appears under **AI-ARK drives**. | En el inicio, presione **Contenido sin conexión**. El disco aparece en **Discos AI-ARK**. |
| `guide.task.import.3` | Press **Copy from this drive**. Nothing on this computer is deleted. | Presione **Copiar de este disco**. No se borra nada de esta computadora. |
| `guide.task.eject.title` | Remove the AI-ARK drive safely | Retirar el disco AI-ARK con seguridad |
| `guide.task.eject.1` | On Home, press **Offline content**. | En el inicio, presione **Contenido sin conexión**. |
| `guide.task.eject.2` | Next to the drive, press **Eject safely**. | Junto al disco, presione **Expulsar con seguridad**. |
| `guide.task.eject.3` | Unplug it only when the screen says it is safe. | Desconéctelo solo cuando la pantalla diga que es seguro. |
| `guide.task.health.title` | Check that everything works | Revisar que todo funcione |
| `guide.task.health.1` | On Home, press **Health**, then **Run a full check**. | En el inicio, presione **Salud** y luego **Hacer una revisión completa**. |
| `guide.task.health.2` | Each problem shows a code, one sentence that says what to do, and often a button that does it. | Cada problema muestra un código, una frase que dice qué hacer y muchas veces un botón que lo hace. |
| `guide.task.appUpdate.title` | Update Local AI (needs internet) | Actualizar Local AI (necesita internet) |
| `guide.task.appUpdate.1` | On Home, press **System**, then **Update Local AI**. | En el inicio, presione **Sistema** y luego **Actualizar Local AI**. |
| `guide.task.appUpdate.2` | The computer asks for its login password. Type it and wait until it says it is done. | La computadora pide su contraseña de inicio de sesión. Escríbala y espere hasta que diga que terminó. |
| `guide.task.colors.title` | What the colours at the top mean | Qué significan los colores de arriba |
| `guide.task.colors.1` | **Green ✓ Ready**: everything works. | **Verde ✓ Listo**: todo funciona. |
| `guide.task.colors.2` | **Amber ! Ready, with problems**: the basics work; **Next step** says what to do. | **Ámbar ! Listo, con problemas**: lo básico funciona; **Siguiente paso** dice qué hacer. |
| `guide.task.colors.3` | **Red ✗ Not ready**: chat or the library does not work. Press the button under **Next step**, or restart the computer and wait 2 minutes. | **Rojo ✗ No está listo**: el chat o la biblioteca no funcionan. Presione el botón de **Siguiente paso**, o reinicie la computadora y espere 2 minutos. |
| `guide.manuals.title` | Manuals | Manuales |
| `guide.manuals.lead` | The full printed instructions for this computer, in English and Spanish. | Las instrucciones completas para imprimir de esta computadora, en inglés y español. |
| `guide.manuals.open` | Open the manuals | Abrir los manuales |
| `guide.expert.title` | For an expert | Para un experto |
| `guide.expert.lead` | The same tasks typed in a terminal (Ctrl + Alt + T). Nobody else needs this. | Las mismas tareas escritas en una terminal (Ctrl + Alt + T). Nadie más necesita esto. |
| `guide.expert.library` | If the app cannot open, the library is also at http://127.0.0.1:7070/library/ in a web browser on this computer. | Si la aplicación no abre, la biblioteca también está en http://127.0.0.1:7070/library/ en un navegador de esta computadora. |
| `guide.back` | Back to Home | Volver al inicio |
<!-- /copy -->

## 10. Open questions

None.
