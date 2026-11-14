# Spec: Dashboard (Manage this computer) and in-app help

> **Status:** approved design · **Epic:** E10 (#492) · **Decision:** [ADR-0011](../decisions/0011-full-control-from-the-app.md) · **Code:** `agent/ui/src/portal/` (the same SPA as the Survival Portal), `apps/desktop/src/main/` (menu, tray, privileged buttons) · **Doctor area:** none new (each screen shows the codes of the routes it calls)
> Canonical names: [overview.md](../architecture/overview.md) §3. Modes: [modes.md](../architecture/modes.md). API: [agent.md](agent.md) §3.8. Portal: [survival-portal.md](survival-portal.md). Desktop: [desktop.md](desktop.md). Rules: [ADR-0009](../decisions/0009-optimized-for-humans.md).

---

## 1. Purpose and scope

The person who must operate Local AI in a disaster may never have used a terminal and may not know the admin PIN. Everything an operator can do with `aictl` must therefore be possible from the ai-desktop app, by reading the screen and pressing buttons, offline, in English or Spanish ([ADR-0011](../decisions/0011-full-control-from-the-app.md)).

The Dashboard is **not a second app**. It is the Survival Portal SPA with two additions:
1. **Home in every mode.** `/` serves the Portal's Home (the placeholder `agent/ui/index.html` is removed). In survival mode Home looks exactly as [survival-portal.md](survival-portal.md) §4.1.1; outside it, the status line and the Manage row are shown and the session-only buttons are unlocked for a full session.
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
- `M` opens Manage, `H` opens Help; `Esc` returns Home from every screen.

### 4.2 Mode
Three large choices with one sentence each: **Online** ("Uses the internet to update content and talk to the lab"), **Offline** ("No internet. Everything here keeps working"), **Survival** ("Emergency screen with the basics. Starts at once"). The current one is marked. Choosing one opens a confirmation that says what changes (modes.md §1) and, for leaving survival, the destination (Online or Offline). No PIN for an operator session.

### 4.3 Offline content
Top: the readiness of each mounted root in one line ("Internal disk: ready" / "98 % ready, 1 item missing") and the last sync. Then:
- A list grouped by kind (Maps, Library, AI models, Speech, Rebuild kit), each item with its status in plain words (`verified` → "Ready", `missing` → "Not downloaded yet", `quarantined` → "Damaged copy set aside; it will be downloaded again", `blocked` → "Skipped until a new version is published"), its size, and its doctor code when there is one.
- **Sync now** (online only; offline shows "Needs the internet"), with progress per item and **Stop**.
- **Check content**, **Install what's missing**.
- **Drives**: each connected AI-ARK drive with its readiness, **Copy from this drive**, **Eject safely** (the result says when it is safe to unplug), and **Prepare a new drive** (PIN; says that everything on the drive is erased).

### 4.4 AI models
A row per model: name, size, state (loaded, downloaded, not downloaded), "survival" tag. Buttons per §3.2. Delete explains "This removes the model from this computer. You can download it again later with the internet, or copy it from the AI-ARK drive." then asks the PIN.

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

### 4.8 Language
English by default for the app and the Portal unless the person chose a language; Spanish one tap away; the choice is remembered (desktop `settings.lang`, Portal `prefs`).

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
