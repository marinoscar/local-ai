# Spec: Survival Portal

> **Status:** approved design · **Epic:** E4 · **Code:** `agent/ui/src/portal/` (React screens), `agent/internal/portal/` (status endpoint, Portal session, chat proxy), `docs/runbooks/00-survival-card.md` (card template) · **Doctor area:** `LAI-SVC` codes 100–149 (001–099 are [companion-services.md](companion-services.md))
> Canonical names: [overview.md](../architecture/overview.md) §3. Modes: [modes.md](../architecture/modes.md). Auth: [security-model.md](../architecture/security-model.md) §3. API: [agent.md](agent.md) §3.8.

---

## 1. Purpose and scope

The Survival Portal is the home screen a frightened non-expert sees in an emergency. It is served by the agent at `http://127.0.0.1:7070/portal`, works in any browser and in ai-desktop, needs no login to look at, and puts every offline capability of the node behind one of eight big buttons: **Chat, Ask about a photo, Talk, Library, Maps, Manuals, Status/Battery, Switch brain**. It also has a first-aid quick-links panel.

In scope:
- The full UX (wireframes for the home screen and each sub-screen), all copy in English and Spanish, accessibility rules.
- Behaviour when something is missing.
- The Portal session (what the Portal may do without a login, and only in survival mode).
- The `/chat/` key-injecting reverse proxy to the llama WebUI.
- The Talk flow (record → STT → chat → TTS → play) and the Photo flow.
- The first-aid panel (article ids configurable in `ark.yaml`).
- The unauthenticated status endpoint `GET /api/v1/portal/status`.
- The offline asset rule and how CI enforces it.
- The printable one-page Survival card.

Non-goals:
- Replacing the llama WebUI. **Chat** opens it (through `/chat/`); the Portal's own screens only cover Photo and Talk, which need big-button simplicity.
- Admin tasks (keys, profile, deletes, cloud). Those live in the Dashboard and need a session and the PIN.
- Network features of any kind.
- Medical advice beyond linking to library articles and a disclaimer.

## 2. Concepts

| Term | Meaning |
|---|---|
| **Portal** | The SPA at `/portal` (part of the embedded local UI bundle) |
| **Portal session** | A restricted cookie the agent issues automatically to any loopback browser that opens `/portal` **while the node is in survival mode** (§3.3) |
| **Full session** | The normal UI session (security-model.md §3: from ai-desktop or a one-time UI code) |
| **Brain** | Plain-language word for an LLM model in the Portal ("cerebro" in Spanish) |
| **Capability** | One of `chat`, `photo`, `talk`, `library`, `maps`, `manuals`, with a state `ready | degraded | unavailable` and a reason |

## 3. Interfaces

### 3.1 Routes served for the Portal

| Path | Auth | Notes |
|---|---|---|
| `GET /portal`, `/portal/*` | none | SPA (client-side routes `/portal/chat`, `/portal/photo`, `/portal/talk`, `/portal/library`, `/portal/maps`, `/portal/manuals`, `/portal/status`, `/portal/brain`, `/portal/first-aid`, `/portal/help` (built-in emergency page, §4.8)). In survival mode the response also sets the Portal session cookie |
| `GET /portal/assets/*` | none | Hashed static assets from `go:embed` |
| `GET /api/v1/portal/status` | none | §3.5 |
| `POST /api/v1/mode/survival` | none | Enter survival (modes.md §2.2) |
| `/chat/*` | survival mode (anonymous: no cookie needed), or full session, or Portal session | §4.4 |
| `/library/*`, `/maps/*`, `/docs/*` | none | [companion-services.md](companion-services.md) §3.5, agent.md |
| `POST /api/v1/speech/stt`, `POST /api/v1/speech/tts`, `GET /api/v1/speech/voices` | full session, or Portal session | |
| `POST /api/v1/models/{id}/load` | full session, or Portal session (restricted, §3.3) | |
| `GET /api/v1/events?types=…` | full session, or Portal session (restricted types) | |

`/` (the Dashboard) redirects to `/portal` when the mode is `survival` (modes.md §1, "Local UI home page").

### 3.2 Client-side storage
`localStorage` keys (no secrets ever): `lai.portal.lang` (`en|es`), `lai.portal.textScale` (`1|1.25|1.5`), `lai.portal.voice.<lang>`, `lai.portal.talk.autoSend` (bool). Chat history from the Photo and Talk screens is kept **in memory only** (cleared on reload) to avoid leaving private conversations on a shared computer; the WebUI keeps its own history in its own storage.

### 3.3 Portal session
Issued by `GET /portal` when `state.mode == survival` and the request passes the loopback Host check (security-model.md §2):
- Cookie `lai_portal=<43-char base64url of 32 random bytes>; Path=/; HttpOnly; SameSite=Strict; Max-Age=43200`. Stored server-side in memory with `issued_at`, `last_seen`; sliding expiry 12 h.
- **All Portal sessions are invalidated** when the mode leaves `survival`.
- Allowed with a Portal session (everything else returns `401` with `LAI-SVC-110`):
  | Route | Restriction |
  |---|---|
  | `/chat/*` | as the proxy allows (§4.4) |
  | `POST /api/v1/speech/stt`, `POST /api/v1/speech/tts`, `GET /api/v1/speech/voices` | none |
  | `POST /api/v1/models/{id}/load` | `id` must be `loadable: true`; the client body is ignored and replaced by `{"wait": false, "unload_others": U}` where `U = !CanKeepOthers(id, loaded)` from the fit API ([llama-router.md](llama-router.md) §4.3): other models stay loaded only when the fit rules say the target fits next to all of them |
  | `GET /api/v1/events` | `types` limited to `mode.*`, `model.*`, `service.state`, `drive.*`, `alert.*` (other requested patterns are silently dropped, not refused) |
- Every state-changing request (`POST`, `PUT`, `DELETE`) made with a Portal session also needs `Origin` equal to `http://127.0.0.1:7070`, `http://localhost:7070` or `localai://app` (CSRF defence). `Sec-Fetch-Site: same-origin` alone is **not** enough for Portal sessions (stricter than the operator-session rule of [agent.md](agent.md) §3.9.1 step 8, which accepts either). A missing or other `Origin` → `403`.
- `/chat/` in survival mode is anonymous (auth `none`, [agent.md](agent.md) §3.9.3/§3.11.1): no Portal cookie is needed, so the WebUI works even if cookies are blocked. Outside survival `/chat/*` needs a full session or a Portal session.
- Rate limits per Portal session: model loads 6/hour; STT 30/min; TTS 120/min. Over the limit → `429`: speech routes with `LAI-SVC-015`, model loads with `LAI-SVC-114`.

This extends the "survival exception" of security-model.md §3; agent.md must implement the scope (see §11).

### 3.4 Configuration

**Catalog field ([catalog.md](catalog.md) §3, "Optional fields"):** a `portal_links` list on a kiwix content item. It drives the first-aid panel:
```yaml
  - id: wikimed-en
    kind: kiwix
    ...
    portal_links:
      - key: bleeding                      # stable key; also the icon name
        title: {en: "Severe bleeding", es: "Sangrado grave"}
        article: "Bleeding"                # path inside the ZIM, as kiwix-serve shows it after /content/<book>/
        es_item: wikimed-es                # optional: Spanish book to use when the UI is Spanish
        es_article: "Hemorragia"
        order: 10
```
Initial first-aid keys (article paths to be verified against the actual ZIMs in CAT-03 "Populate initial ark content", [catalog.md](catalog.md) §10): `cpr`, `choking`, `bleeding`, `shock`, `burns`, `fracture`, `head_injury`, `allergic_reaction`, `snake_bite`, `dehydration` (oral rehydration), `heat_stroke`, `hypothermia`, `wound_infection`, `childbirth`, plus from the survival collection: `water_purification`, `food_safety`.

**node.yaml** (`portal:` block, owned here; agent.md must include it):
```yaml
portal:
  language: auto           # auto | en | es ; auto = profile.survival.portal_language, then the OS locale
  emergency_number: "911"  # shown on the home screen and the card; Costa Rica's number
  kiosk: false             # true = ai-desktop opens the Portal full screen even outside survival
```

### 3.5 Status endpoint (`GET /api/v1/portal/status`, no auth)

Returns only what a stranger at the keyboard needs. **Never** included: tokens, keys, IP addresses, hostnames other than the node id, file paths, user names, cloud details, drive UUIDs, logs.
```json
{
  "schema_version": 1,
  "generated_at": "2026-11-02T14:05:11Z",
  "node_id": "ai-n3",
  "mode": "survival",
  "language": "es",
  "emergency_number": "911",
  "session": "portal",
  "power": {"on_ac": false, "battery_pct": 72, "minutes_left": 145, "state": "discharging", "low": false, "critical": false},
  "clock_ok": true,
  "brain": {
    "loaded": [{"id": "qwen3.5-9b-q4km", "display_name": "Qwen 3.5 9B", "roles": ["chat","reasoning","coder"], "vision": false}],
    "loading": null,
    "choices": [
      {"id": "qwen3.5-9b-q4km", "display_name": "Qwen 3.5 9B", "summary": "Asistente general rápido…", "roles": ["chat"], "vision": false, "loadable": true, "survival": true, "speed_hint": "fast"},
      {"id": "<small-vlm-id>", "display_name": "<VLM name>", "summary": "Entiende fotos…", "roles": ["vision","chat"], "vision": true, "loadable": true, "survival": true, "speed_hint": "medium"}
    ]
  },
  "capabilities": {
    "chat":    {"state": "ready"},
    "photo":   {"state": "degraded", "reason": "needs_switch", "vision_model": "<small-vlm-id>"},
    "talk":    {"state": "ready", "stt": true, "tts_languages": ["en","es"]},
    "library": {"state": "ready", "books": [{"content_id": "wikimed-en", "title": "WikiMed (English)", "book": "mdwiki_en_all", "lang": "en"}]},
    "maps":    {"state": "ready", "search": true, "gps": false},
    "manuals": {"state": "ready", "items": [{"id": "01-survival-mode", "title": "Survival mode", "url": "/docs/01-survival-mode.html"}]}
  },
  "first_aid": [
    {"key": "bleeding", "title": "Sangrado grave", "url": "/library/content/mdwiki_es_all/Hemorragia", "available": true}
  ],
  "ark": {"overall": "yellow", "survival_capable": true, "drive_connected": true},
  "alerts": [
    {"code": "LAI-SVC-013", "severity": "info", "title": "El reconocimiento de voz usa el procesador.", "fix": "Nada que hacer."}
  ]
}
```
- `clock_ok` is `false` when the system clock is earlier than the agent binary's build time, or earlier than the newest `verified_at` in any mounted root's READY.json (a clock cannot go backwards past a time it has already seen); otherwise `true`.
- `capabilities.<x>.code` is present only when the state is caused by a problem. `needs_switch` is not a problem, so it carries no `code`. `LAI-SVC-106` ("No brain is running") is mapped here by the Portal status builder when no brain is loaded or loadable.
- `manuals.items[].url` is the `html` value of the item in `docs/index.json` ([agent.md](agent.md) §3.8.29), e.g. `/docs/01-survival-mode.html`.
- `session` tells the SPA what it may call: `none` (not survival, no cookie), `portal`, or `full`.
- `title` and `fix` strings are already in `language`. The SPA can override with `?lang=en|es`.
- Cached by the agent for 2 s. Cheap: no checks are run on request; it reads the supervisor's state.

## 4. Behaviour

### 4.1 UX and wireframes

Design target: **1366×768** with no scrolling on the home screen at 100 % text scale, and usable at 1024×600 (scrolls) and 1920×1080 (scales up). Black text on near-white (`#111` on `#FAFAF7`), one accent colour per button, and a 4 px focus ring. Base font 20 px; button labels 30 px bold; screen titles 36 px. Wireframes are ASCII at roughly 1366×768.

#### 4.1.1 Home
```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ LOCAL AI · SURVIVAL                     [ English | Español ]   [A A+ A++]    14:05   ⚡ 72 %  │
│ ● Ready. Brain: Qwen 3.5 9B · Library ✓ · Maps ✓ · Voice ✓       Emergency number: 911       │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│ ┌───────────────────┐ ┌───────────────────┐ ┌───────────────────┐ ┌───────────────────┐       │
│ │  [chat icon]      │ │  [camera icon]    │ │  [mic icon]       │ │  [book icon]      │       │
│ │  1  Chat          │ │  2  Ask about     │ │  3  Talk          │ │  4  Library       │       │
│ │  Type questions   │ │     a photo       │ │  Speak and listen │ │  Wikipedia,       │       │
│ │                   │ │  Take or choose   │ │                   │ │  medicine, how-to │       │
│ └───────────────────┘ └───────────────────┘ └───────────────────┘ └───────────────────┘       │
│ ┌───────────────────┐ ┌───────────────────┐ ┌───────────────────┐ ┌───────────────────┐       │
│ │  [map icon]       │ │  [manual icon]    │ │  [battery icon]   │ │  [brain icon]     │       │
│ │  5  Maps          │ │  6  Manuals       │ │  7  Status and    │ │  8  Switch brain  │       │
│ │  Offline map and  │ │  How to use this  │ │     battery       │ │  Choose the AI    │       │
│ │  place search     │ │  computer         │ │  72 %, 2 h 25 min │ │  model            │       │
│ └───────────────────┘ └───────────────────┘ └───────────────────┘ └───────────────────┘       │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│ FIRST AID  [CPR] [Choking] [Severe bleeding] [Burns] [Broken bone] [Snake bite] [More …]  (F) │
│ This is not a doctor. If any phone works, call 911.                                           │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```
- Buttons: 300×170 px, 4 columns × 2 rows, 24 px gaps. Number keys `1`–`8` open them; `F` focuses the first-aid row; `Esc` always returns home.
- The status line is one sentence built from `capabilities` (§4.2): green dot "Ready", amber "Ready, with problems — see Status", red "Not ready — see Status". Colour rule: **red** if `chat` or `library` is `unavailable`; else **amber** if any capability is not `ready`; else **green**.
- **Not in survival mode** (Portal opened from the Dashboard, or `kiosk: true`): a wide banner replaces the status line: "Survival mode is off. [ Start survival mode ]" (button calls `POST /api/v1/mode/survival`). Buttons that need a session (Chat, Photo, Talk, Switch brain) show a small lock and, when pressed without a full session, the dialog "To use this, start Survival mode or log in. [ Start survival mode ] [ Log in ]".
- The battery indicator turns amber at ≤ 20 % and red with a full-width banner at ≤ 10 % on battery ("Battery very low. Plug in now or the computer will turn off.").

#### 4.1.2 Chat (button 1)
Opens the llama WebUI full-page at `/chat/` in the same tab (ai-desktop: in its main window). A thin Portal bar is not injected (we don't modify the WebUI). The Portal shows first an interstitial for 2 s, or until a key is pressed:
```
┌──────────────────────────────────────────────────────────────────────────────┐
│  Opening the chat …                                                         │
│  Tip: to come back here, press the browser Back button or go to             │
│       127.0.0.1:7070/portal                                                 │
│  [ Open chat now ]                         [ Back ]                          │
└──────────────────────────────────────────────────────────────────────────────┘
```
If no brain is loaded, the interstitial instead says "No brain is running. [ Choose a brain ]" (→ Switch brain).

#### 4.1.3 Ask about a photo (button 2)
```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ ◄ Home (Esc)        ASK ABOUT A PHOTO                                  Brain: <VLM name>     │
├───────────────────────────────────────────┬──────────────────────────────────────────────────┤
│  ┌─────────────────────────────────────┐  │  Your question:                                  │
│  │                                     │  │  ┌────────────────────────────────────────────┐  │
│  │        [ photo preview ]            │  │  │ What is this plant? Is it safe to touch?   │  │
│  │                                     │  │  └────────────────────────────────────────────┘  │
│  └─────────────────────────────────────┘  │  Quick questions:                                │
│  [ Take photo ]  [ Choose file ]          │  [What is this?] [Is it dangerous?]               │
│                                           │  [How do I fix this?] [Read the text]             │
│                                           │                              [ Ask  (Enter) ]    │
│                                           ├──────────────────────────────────────────────────┤
│                                           │  Answer:                                          │
│                                           │  It looks like … (streams in)                     │
│                                           │  [ Read aloud ]  [ Ask another question ]         │
└───────────────────────────────────────────┴──────────────────────────────────────────────────┘
```
Flow (§4.6). If the loaded brain has no vision: a box above the question: "The current brain can't see photos. Switch to *<VLM name>*? It takes about a minute. [ Switch ]".

#### 4.1.4 Talk (button 3)
```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ ◄ Home (Esc)        TALK                     Language: [Auto | English | Español]   Voice ▾  │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                              │
│                        ┌──────────────────────────────────┐                                  │
│                        │     [ big microphone button ]    │                                  │
│                        │   Press SPACE or click to talk   │                                  │
│                        └──────────────────────────────────┘                                  │
│                        ▁▂▅▇▅▂▁  0:04 / 1:00   (while recording)                              │
│                                                                                              │
│  You said:  "How do I make water safe to drink?"                         [ Edit ] [ Redo ]   │
│  Answer:    "Boil it for one minute. If you are high up in the mountains, boil it for…"     │
│             [ Stop speaking ]  [ Repeat ]                                                    │
│                                                                                              │
│  Status: Listening … / Writing down what you said … / Thinking … / Speaking …                │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```

#### 4.1.5 Library (button 4)
```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ ◄ Home (Esc)        LIBRARY                                                                  │
│ Search all books: ┌──────────────────────────────────────────────────────┐ [ Search (Enter) ]│
│                   │ water purification                                    │                  │
│                   └──────────────────────────────────────────────────────┘                  │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│ Results (12)                                   │ Books                                       │
│ • Water purification — Wikipedia               │ [ Wikipedia (English) ]                     │
│ • Portable water purification — WikiHow        │ [ WikiMed (medicine) ]                      │
│ • Boiling water — Survival collection          │ [ WikiHow ]   [ iFixit (repairs) ]          │
│ …                                              │ [ Wikipedia (Español) ]                     │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```
- Search: `GET /library/search?pattern=<q>&books.filter.lang=<eng|spa>&format=xml&pageLength=25` (OpenSearch RSS from kiwix-serve, parsed in the browser). The language filter defaults to the UI language with a toggle "All languages".
- Opening a result or a book shows it inside the **reader** view: a Portal header bar ("◄ Back to results", "⌂ Home", text-size buttons) above a same-origin `<iframe src="/library/content/…">`. The iframe is same-origin through the agent proxy, so keyboard focus and the header work.

#### 4.1.6 Maps (button 5)
Opens `/maps/` ([companion-services.md](companion-services.md) §4.4.3) inside the same reader frame with the Portal header, so Home is one key away. Big controls: search, **Where am I**, **Nearest: hospital · clinic · water · town**.

#### 4.1.7 Manuals (button 6)
```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ ◄ Home (Esc)        MANUALS                                                                  │
│ [ 1. Survival mode — start here ]        [ 6. Rebuild this computer without internet ]       │
│ [ 7. Troubleshooting (error codes) ]     [ Print the Survival card ]                         │
│ [ All manuals … ]                                                                            │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```
Items come from `capabilities.manuals.items` (rendered runbooks under `/docs/`). They open in the reader frame. "Print the Survival card" opens the PDF (`/docs/survival-card-<node-id>.pdf`).

#### 4.1.8 Status and battery (button 7)
```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ ◄ Home (Esc)        STATUS AND BATTERY                                                       │
├──────────────────────────────────────────────┬───────────────────────────────────────────────┤
│ BATTERY  ███████████████░░░░░  72 %          │ WHAT WORKS                                    │
│ On battery. About 2 h 25 min left.           │ ✓ Chat        ✓ Library (7 books)             │
│ Tips: plug in when you can. Close the lid    │ ! Photo — needs a brain switch                │
│ only if it is plugged in.                    │ ✓ Talk        ✓ Maps (search ✓, GPS ✗)        │
│                                              │ ✓ Manuals                                     │
│ BRAIN  Qwen 3.5 9B (running)                 ├───────────────────────────────────────────────┤
│ MODE   Survival (no internet used)           │ PROBLEMS                                      │
│ ARK    ● Yellow — everything essential is    │ LAI-SVC-013  Voice recognition uses the       │
│        here. Ark drive connected.            │ processor. Nothing to do.                     │
│ CLOCK  Mon 2 Nov 2026, 14:05                 │                                               │
└──────────────────────────────────────────────┴───────────────────────────────────────────────┘
```
Each problem shows its code, a one-sentence title and the fix, in the UI language. There is no "Leave survival mode" button here (that is in the Dashboard and needs the PIN).

#### 4.1.9 Switch brain (button 8)
```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ ◄ Home (Esc)        SWITCH BRAIN                                                             │
│ Running now: Qwen 3.5 9B                                                                     │
├──────────────────────────────────────────────────────────────────────────────────────────────┤
│ ┌──────────────────────────────────────────────────────────────────────────────────────────┐ │
│ │ ● Qwen 3.5 9B            Fast general assistant. Good for chat and writing.  [ Running ] │ │
│ ├──────────────────────────────────────────────────────────────────────────────────────────┤ │
│ │ ○ <VLM name>   SEES PHOTOS   Understands photos. A bit slower.               [ Use this ]│ │
│ ├──────────────────────────────────────────────────────────────────────────────────────────┤ │
│ │ ○ <tiny model>  Very small. Works without graphics card. Slower answers.     [ Use this ]│ │
│ └──────────────────────────────────────────────────────────────────────────────────────────┘ │
│ Switching takes about a minute. Questions in progress will stop.                             │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```
Only `loadable: true` models are listed; survival models first. Pressing **Use this** shows a confirmation ("Switch to *X*? The current brain will stop.") then a progress view driven by `model.loading/loaded/failed` events ("Loading *X* … 35 s"). Failure → `LAI-SVC-107` with "The previous brain was restarted." (the agent reloads the previous default, llama-router.md).

#### 4.1.10 First aid (`F`, or "More …")
A grid of all `first_aid` entries (big buttons, two lines). Each opens the article in the reader. A permanent red-bordered note at the top: "This is not a doctor. If any phone works, call 911. Act fast in an emergency; read later." (`firstaid.disclaimer` + `firstaid.readLater`) Unavailable entries are hidden; if none are available, the panel says "The medical book is not installed. Plug in the ark drive." (`LAI-SVC-109`).

### 4.2 Capability states and behaviour when things are missing

The agent computes each capability for `/portal/status`; the SPA only displays.

| Capability | `ready` when | `degraded` (button works, with a note) | `unavailable` (button shows why) |
|---|---|---|---|
| chat | a brain is loaded and llama is `ok` | brain loading; or llama `degraded` | no loadable brain (`LAI-SVC-106`); llama down (`LAI-SVC-102`) |
| photo | a loaded brain has vision | a vision model is loadable but not loaded (`needs_switch`) | no vision model loadable (`LAI-SVC-040`) |
| talk | whisper `ok` and a voice for the UI language | no voice for UI language (answers in another voice); or no voice at all (text-only answers, `LAI-SVC-022`) | whisper `missing_content`/`down` (`LAI-SVC-010`/`011`); or no brain |
| library | kiwix `ok` with ≥ 1 book | `degraded` (a book skipped) | no books (`LAI-SVC-001`) or kiwix down (`LAI-SVC-002`) |
| maps | a map + assets | no assets (no labels) or no search (`LAI-SVC-031`/`032`) | no map (`LAI-SVC-030`) |
| manuals | docs present | docs rendered without inventory (`LAI-SYN-062`) | no docs; then the button opens a built-in page with the 10 most important instructions embedded in the Portal itself (§4.8) |

An **unavailable** button is still focusable and pressable. Pressing it opens a full-screen explanation: what's missing (plain words), the fix (one sentence), the doctor code, and a link to the right manual page. Example: "The library has no books. Plug in the AI-ARK drive and press **Import from drive**." Buttons are never hidden, so the layout never changes in a crisis.

If the agent's API stops responding while the Portal is open, the SPA shows a full-width banner "Lost contact with the AI service. Trying again …" and retries every 5 s; Library and Maps links still open (they fail separately if the agent is fully down). The survival runbook explains the direct Kiwix fallback at `http://127.0.0.1:8888/library/`.

### 4.3 Language
- `portal.language: auto` resolves: `profile.survival.portal_language` (if `en`/`es`) → the desktop user's locale reported by ai-desktop (`LANG`) → the browser's `navigator.language` → `en`.
- The toggle on the home screen switches instantly and is stored in `localStorage`.
- The chosen language is sent on every API request as `X-LocalAI-UI-Language` (used by TTS fallback and for `title`/`fix` strings).
- All copy is in `agent/ui/src/portal/i18n/{en,es}.json`. A CI test fails if a key exists in one file and not the other, or if any JSX text literal is not a translation key.

### 4.4 Chat proxy

`/chat/` is a loopback-only reverse proxy from the agent (`127.0.0.1:7070`) to the llama-server router (`127.0.0.1:8080`) that **injects the API key**, so the WebUI works without anyone typing a key, and the key never reaches the browser.

**Enabling rule** (evaluated per request): `mode == survival` **or** a valid full session **or** a valid Portal session. Otherwise `403` with a bilingual HTML page: "Chat is locked. Start Survival mode or log in." with buttons (`LAI-SVC-101`).

**Request handling** (`httputil.ReverseProxy` with a custom `Rewrite`):
1. `/chat` → `301 /chat/`. Strip the `/chat` prefix; forward the rest (path and query) to `http://127.0.0.1:8080`.
2. This works because the WebUI (llama.cpp `tools/ui`, verified at commit f3586e66b) is built with SvelteKit `paths.relative: true` and a hash router, and its API calls use relative paths (`./v1/chat/completions`) or `${base}`-prefixed paths. A drill check and an integration test load `/chat/` and assert that the WebUI's `/props` request goes to `/chat/props` (so a future WebUI change that breaks this is caught).
3. Remove from the request: `Authorization`, `X-Api-Key`, `Cookie`, `X-Forwarded-*`, `Forwarded`. Add `Authorization: Bearer <key>`. The key is read from `/etc/localai/api-key` at agent start and on `key rotate` (held in memory, never logged).
4. Set `Host: 127.0.0.1:8080`.
5. Methods `POST`, `PUT`, `PATCH`, `DELETE` require a same-origin `Origin` (or `localai://app`), else `403`.
6. **Path policy:**
   | Upstream path | Policy |
   |---|---|
   | `/`, static WebUI files, `/props`, `/v1/models`, `/models` (GET), `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/stream*`, `/tokenize`, `/detokenize`, `/apply-template`, `/health`, `/slots` (GET) | forwarded |
   | `POST /models/load` | **intercepted**: parsed `{"model": "<id>"}` and executed through the agent's own load path (fit rules, unload-others, same as `POST /api/v1/models/{id}/load`); returns a router-compatible `{"success": true}` or the agent's error. So the WebUI's model picker can't bypass the fit rules |
   | `POST /models/unload` | intercepted the same way |
   | `/models/download*`, `DELETE /models*`, `POST /slots/*`, `/cors-proxy*`, `/tools*` (MCP/tool execution) | `403` (`LAI-SVC-115`, "not allowed from the chat page") |
   | anything else | forwarded only if `GET`/`HEAD`, else `403` (`LAI-SVC-115`) |
7. Body limit 32 MiB (photos). No request timeout for streams; idle timeout 10 min.

**Response handling:** `FlushInterval: -1` (stream SSE immediately); WebSocket upgrades are passed through; `Set-Cookie` from upstream is dropped; `X-Frame-Options: SAMEORIGIN` and `Referrer-Policy: no-referrer` are added; the WebUI's own headers are otherwise unchanged. Upstream unreachable → `502` page "The AI isn't running. [ Switch brain ] [ Status ]" (`LAI-SVC-102`).

**What this means for security:** in survival mode, any local process can use `/chat/` without the key. That's accepted: survival means "anyone at the keyboard may use the AI", the listener is loopback-only, and only inference is exposed (no admin routes). Outside survival a session is required.

### 4.5 Talk flow

```
            SPACE / click                stop (SPACE, click, 60 s, or 1.5 s of silence)
   idle ───────────────────► recording ───────────────────────► converting
    ▲                            │ Esc                              │ decode + resample (browser)
    │                            ▼                                  ▼
    │                          idle                            transcribing ── error ──► idle (message)
    │                                                               │ POST /api/v1/speech/stt
    │                                                               ▼
    │          (autoSend off: wait for [Send] / Edit)          confirm ─────────────────► thinking
    │                                                                                    │ POST /chat/v1/chat/completions (stream)
    │        Esc / [Stop speaking]                                                       ▼
    └──────────────────────────────── speaking ◄── first sentence complete ──── streaming
                                         │  POST /api/v1/speech/tts per sentence, played in order
                                         └──── last sentence played ──► idle
```
1. **Record.** `navigator.mediaDevices.getUserMedia({audio: {channelCount: 1, echoCancellation: true, noiseSuppression: true}})`. `http://127.0.0.1` is a secure context, so this works without HTTPS. Record with `MediaRecorder` (`audio/webm;codecs=opus` in Chromium; `audio/ogg;codecs=opus` in Firefox). A live level meter uses an `AnalyserNode`. Auto-stop after 60 s, or after 1.5 s below −45 dBFS once speech was detected. Permission denied → `LAI-SVC-103` screen with "Allow the microphone" instructions for Chromium and Firefox.
2. **Convert (in the browser, no ffmpeg needed on the node).** Collect the blobs, `AudioContext.decodeAudioData()` them, render through an `OfflineAudioContext(1, ceil(duration × 16000), 16000)` to get 16 kHz mono float samples, and encode a PCM16 WAV in JS (44-byte header). Decode failure → `LAI-SVC-105`. If `MediaRecorder` is missing (very old browser), fall back to an `AudioWorklet` that captures raw PCM directly (`LAI-SVC-104` only if both are missing).
3. **Transcribe.** `POST /api/v1/speech/stt` with `audio` and `language` (`auto` or the selected one). Show "You said: …". With `autoSend` on (default), continue after 1.5 s unless the user presses **Edit** or **Redo**.
4. **Think.** `POST /chat/v1/chat/completions` with `model` = the first entry of `brain.loaded[].id` from `/portal/status` (no brain loaded → `LAI-SVC-106` screen with **Switch brain**), `stream: true`, `temperature: 0.5`, `max_tokens: 600`, the Talk system prompt (§4.7) in the transcript's language, and the last 6 turns of in-memory history. If the model's catalog `client_hints.reasoning` is true, also send `"chat_template_kwargs": {"enable_thinking": false}` (fast spoken answers) and ignore any `reasoning_content`.
5. **Speak, pipelined.** As tokens stream in, split on sentence ends (`. ! ? ¿ ¡ …` followed by space or newline, or 220 chars). Each complete sentence is sent to `POST /api/v1/speech/tts` (`language` = the transcript language) with at most 2 requests ahead of playback; the WAVs are played in order through one `HTMLAudioElement` queue. The text is shown as it streams.
6. **Stop.** `Esc` or **Stop speaking** aborts the fetches (`AbortController`) and clears the audio queue. **Repeat** replays the cached WAVs.
7. **Errors** at any step show the step, the code and a **Try again** button; the recording is kept so **Try again** doesn't need re-recording.
8. **No voice available** (`LAI-SVC-022`): answers are shown as large text only, with the note "Voice output is not installed; read the answer here."

### 4.6 Photo flow
1. **Get a photo.** `<input type="file" accept="image/*" capture="environment">` ("Take photo": opens the webcam on laptops via the browser's capture UI where supported; otherwise the file chooser) and a plain file chooser ("Choose file"). Drag and drop also works.
2. **Prepare (browser).** Decode with `createImageBitmap(file, {imageOrientation: "from-image"})` (respects EXIF rotation), downscale so the long side is ≤ 1024 px, encode JPEG at quality 0.85 with `canvas.toBlob`. Strip all metadata by construction (canvas re-encode drops EXIF, including GPS). Reject files over 30 MB or that fail to decode (`LAI-SVC-108`).
3. **Pick a brain.** If no loaded brain has vision: if `photo.reason == "needs_switch"`, offer the switch (Portal session → `POST /api/v1/models/{vision_model}/load`); wait for `model.loaded`.
4. **Ask.** `POST /chat/v1/chat/completions` with `model` = the id of the first loaded brain with `vision: true` (after a switch, `capabilities.photo.vision_model`), the format of [companion-services.md](companion-services.md) §4.5, the Photo system prompt (§4.7), `stream: true`. Show the answer streaming; **Read aloud** sends it to TTS in sentence chunks (as in Talk step 5).
5. Follow-up questions keep the same image in the conversation (sent again in the first user message), up to 4 turns.

### 4.7 Prompts (embedded, both languages)

Talk system prompt:
- **en:** "You are the voice assistant of an offline emergency computer. Answer in English. Use short, plain sentences that sound natural when read aloud. Do not use lists, tables, markdown or emojis. Give at most five sentences unless the person asks for more. If the question is about a medical emergency, first say to get professional help if at all possible, then give clear first steps."
- **es:** "Usted es el asistente de voz de una computadora de emergencia sin internet. Responda en español. Use oraciones cortas y sencillas que suenen naturales al leerse en voz alta. No use listas, tablas, markdown ni emojis. Dé como máximo cinco oraciones salvo que la persona pida más. Si la pregunta es sobre una emergencia médica, primero diga que busque ayuda profesional si es posible, y luego dé los primeros pasos claros."

Photo system prompt:
- **en:** "You help people understand photos on an offline emergency computer. Describe what you see before drawing conclusions. Say how sure you are. Never tell someone a plant, mushroom or animal is safe to eat or touch based only on a photo; explain the risks instead. For injuries, recommend professional help if possible and give basic first aid. Answer in English, in plain words."
- **es:** "Usted ayuda a entender fotos en una computadora de emergencia sin internet. Describa lo que ve antes de sacar conclusiones. Diga qué tan seguro está. Nunca diga que una planta, hongo o animal es seguro para comer o tocar solo por una foto; explique los riesgos. Para heridas, recomiende ayuda profesional si es posible y dé primeros auxilios básicos. Responda en español, con palabras sencillas."

The prompt language follows the detected transcript language (Talk) or the UI language (Photo).

### 4.8 Built-in emergency page
If `/docs/` is missing, the Manuals button opens `/portal/help`, embedded in the SPA (so it exists even with no content at all). It contains, in both languages: how to start survival mode, how to plug in and import from the AI-ARK drive, how to reach Kiwix directly, how to read a doctor code, the emergency number, and "If nothing works: restart the computer and wait 2 minutes." The text is part of the i18n files (`help.*` keys, §4.9). The page fetches nothing, so it works with no content roots and with the agent API down. The Manuals button opens it directly when `capabilities.manuals.state == "unavailable"`, and every unavailable-capability screen links to it.

### 4.9 Copy (all user-visible strings)

Keys are in `agent/ui/src/portal/i18n/`. Values below are the source of truth for the initial files.

| Key | English | Español |
|---|---|---|
| `app.title` | Local AI · Survival | IA Local · Supervivencia |
| `status.ready` | Ready. | Listo. |
| `status.readyProblems` | Ready, with problems. See Status. | Listo, con problemas. Vea Estado. |
| `status.notReady` | Not ready. See Status. | No está listo. Vea Estado. |
| `status.brain` | Brain: {name} | Cerebro: {name} |
| `status.emergency` | Emergency number: {n} | Número de emergencias: {n} |
| `banner.survivalOff` | Survival mode is off. | El modo supervivencia está apagado. |
| `banner.startSurvival` | Start survival mode | Iniciar modo supervivencia |
| `banner.lostContact` | Lost contact with the AI service. Trying again … | Se perdió contacto con el servicio de IA. Intentando otra vez … |
| `banner.batteryLow` | Battery low ({pct} %). Plug in when you can. | Batería baja ({pct} %). Conecte el cargador cuando pueda. |
| `banner.batteryCritical` | Battery very low. Plug in now or the computer will turn off. | Batería muy baja. Conecte el cargador ya o la computadora se apagará. |
| `btn.chat` | Chat | Conversar |
| `btn.chat.sub` | Type questions | Escriba preguntas |
| `btn.photo` | Ask about a photo | Preguntar por una foto |
| `btn.photo.sub` | Take or choose a photo | Tome o elija una foto |
| `btn.talk` | Talk | Hablar |
| `btn.talk.sub` | Speak and listen | Hable y escuche |
| `btn.library` | Library | Biblioteca |
| `btn.library.sub` | Wikipedia, medicine, how-to | Wikipedia, medicina, guías |
| `btn.maps` | Maps | Mapas |
| `btn.maps.sub` | Offline map and place search | Mapa sin internet y búsqueda de lugares |
| `btn.manuals` | Manuals | Manuales |
| `btn.manuals.sub` | How to use this computer | Cómo usar esta computadora |
| `btn.status` | Status and battery | Estado y batería |
| `btn.brain` | Switch brain | Cambiar cerebro |
| `btn.brain.sub` | Choose the AI model | Elija el modelo de IA |
| `firstaid.title` | First aid | Primeros auxilios |
| `firstaid.more` | More … | Más … |
| `firstaid.disclaimer` | This is not a doctor. If any phone works, call {n}. | Esto no es un médico. Si algún teléfono funciona, llame al {n}. |
| `firstaid.readLater` | Act fast in an emergency; read later. | En una emergencia actúe rápido; lea después. |
| `firstaid.missing` | The medical book is not installed. Plug in the ark drive. | El libro de medicina no está instalado. Conecte el disco del arca. |
| `common.home` | Home | Inicio |
| `common.back` | Back | Atrás |
| `common.tryAgain` | Try again | Intentar otra vez |
| `common.cancel` | Cancel | Cancelar |
| `common.readAloud` | Read aloud | Leer en voz alta |
| `common.stopSpeaking` | Stop speaking | Dejar de hablar |
| `common.repeat` | Repeat | Repetir |
| `common.code` | Code: {code} | Código: {code} |
| `lock.title` | To use this, start Survival mode or log in. | Para usar esto, inicie el modo supervivencia o inicie sesión. |
| `lock.login` | Log in | Iniciar sesión |
| `chat.opening` | Opening the chat … | Abriendo la conversación … |
| `chat.tip` | To come back here, press the browser Back button or go to 127.0.0.1:7070/portal | Para volver aquí, presione Atrás en el navegador o vaya a 127.0.0.1:7070/portal |
| `chat.openNow` | Open chat now | Abrir la conversación |
| `chat.noBrain` | No brain is running. | No hay ningún cerebro funcionando. |
| `chat.chooseBrain` | Choose a brain | Elegir un cerebro |
| `photo.title` | Ask about a photo | Preguntar por una foto |
| `photo.take` | Take photo | Tomar foto |
| `photo.choose` | Choose file | Elegir archivo |
| `photo.question` | Your question: | Su pregunta: |
| `photo.quick` | Quick questions: | Preguntas rápidas: |
| `photo.q.what` | What is this? | ¿Qué es esto? |
| `photo.q.danger` | Is it dangerous? | ¿Es peligroso? |
| `photo.q.fix` | How do I fix this? | ¿Cómo arreglo esto? |
| `photo.q.read` | Read the text | Lea el texto |
| `photo.ask` | Ask | Preguntar |
| `photo.answer` | Answer: | Respuesta: |
| `photo.another` | Ask another question | Hacer otra pregunta |
| `photo.needsSwitch` | The current brain can't see photos. Switch to {name}? It takes about a minute. | El cerebro actual no puede ver fotos. ¿Cambiar a {name}? Tarda cerca de un minuto. |
| `photo.switch` | Switch | Cambiar |
| `photo.none` | No brain that understands photos is installed on this computer. | En esta computadora no hay un cerebro que entienda fotos. |
| `photo.bad` | This photo can't be opened. Try another one. | No se puede abrir esta foto. Pruebe con otra. |
| `talk.title` | Talk | Hablar |
| `talk.press` | Press SPACE or click to talk | Presione ESPACIO o haga clic para hablar |
| `talk.listening` | Listening … | Escuchando … |
| `talk.writing` | Writing down what you said … | Escribiendo lo que dijo … |
| `talk.thinking` | Thinking … | Pensando … |
| `talk.speaking` | Speaking … | Hablando … |
| `talk.youSaid` | You said: | Usted dijo: |
| `talk.edit` | Edit | Corregir |
| `talk.redo` | Redo | Grabar otra vez |
| `talk.send` | Send | Enviar |
| `talk.language` | Language | Idioma |
| `talk.auto` | Auto | Automático |
| `talk.voice` | Voice | Voz |
| `talk.noMic` | The microphone is blocked or missing. Allow the microphone in the browser, or plug one in. | El micrófono está bloqueado o no existe. Permita el micrófono en el navegador, o conecte uno. |
| `talk.silent` | I couldn't hear anything. Try again closer to the microphone. | No escuché nada. Intente otra vez más cerca del micrófono. |
| `talk.textOnly` | Voice output is not installed; read the answer here. | La voz no está instalada; lea la respuesta aquí. |
| `talk.unavailable` | Voice input is not installed on this computer. | El reconocimiento de voz no está instalado en esta computadora. |
| `library.title` | Library | Biblioteca |
| `library.search` | Search all books | Buscar en todos los libros |
| `library.results` | Results ({n}) | Resultados ({n}) |
| `library.noResults` | Nothing found. Try other words. | No se encontró nada. Pruebe otras palabras. |
| `library.books` | Books | Libros |
| `library.allLanguages` | All languages | Todos los idiomas |
| `library.backToResults` | Back to results | Volver a los resultados |
| `library.none` | The library has no books. Plug in the AI-ARK drive and press Import from drive. | La biblioteca no tiene libros. Conecte el disco AI-ARK y presione Importar desde el disco. |
| `maps.title` | Maps | Mapas |
| `maps.whereami` | Where am I | Dónde estoy |
| `maps.iAmHere` | I am here | Estoy aquí |
| `maps.nearest` | Nearest: | Lo más cercano: |
| `maps.kind.hospital` | hospital | hospital |
| `maps.kind.clinic` | clinic | clínica |
| `maps.kind.water` | water | agua |
| `maps.kind.town` | town | pueblo |
| `maps.noGps` | No GPS. Tap the map where you are, or search for a place. | No hay GPS. Toque el mapa donde está, o busque un lugar. |
| `maps.none` | No offline map is installed. Plug in the ark drive. | No hay un mapa sin internet instalado. Conecte el disco del arca. |
| `manuals.title` | Manuals | Manuales |
| `manuals.card` | Print the Survival card | Imprimir la tarjeta de supervivencia |
| `manuals.all` | All manuals … | Todos los manuales … |
| `status.title` | Status and battery | Estado y batería |
| `status.battery` | Battery | Batería |
| `status.onAc` | Plugged in. | Conectado al cargador. |
| `status.onBattery` | On battery. About {time} left. | Con batería. Quedan unos {time}. |
| `status.powerTip` | Plug in when you can. Close the lid only if it is plugged in. | Conecte el cargador cuando pueda. Cierre la tapa solo si está conectado. |
| `status.mode` | Mode | Modo |
| `status.mode.survival` | Survival (no internet used) | Supervivencia (no usa internet) |
| `status.ark` | Ark | Arca |
| `status.ark.green` | Green: everything is here and up to date. | Verde: todo está aquí y al día. |
| `status.ark.yellow` | Yellow: everything essential is here. | Amarillo: todo lo esencial está aquí. |
| `status.ark.red` | Red: something essential is missing. See problems. | Rojo: falta algo esencial. Vea los problemas. |
| `status.drive` | Ark drive connected. | Disco del arca conectado. |
| `status.works` | What works | Qué funciona |
| `status.problems` | Problems | Problemas |
| `status.clock` | Clock | Reloj |
| `status.clockWrong` | The clock looks wrong. Dates may be off. | El reloj parece incorrecto. Las fechas pueden estar mal. |
| `brain.title` | Switch brain | Cambiar cerebro |
| `brain.runningNow` | Running now: {name} | Funcionando ahora: {name} |
| `brain.running` | Running | Funcionando |
| `brain.use` | Use this | Usar este |
| `brain.seesPhotos` | Sees photos | Ve fotos |
| `brain.note` | Switching takes about a minute. Questions in progress will stop. | Cambiar tarda cerca de un minuto. Las preguntas en curso se detendrán. |
| `brain.confirm` | Switch to {name}? The current brain will stop. | ¿Cambiar a {name}? El cerebro actual se detendrá. |
| `brain.loading` | Loading {name} … {s} s | Cargando {name} … {s} s |
| `brain.failed` | {name} couldn't start. The previous brain was restarted. | {name} no pudo iniciar. Se reinició el cerebro anterior. |
| `brain.speed.fast` | Fast | Rápido |
| `brain.speed.medium` | Medium speed | Velocidad media |
| `brain.speed.slow` | Slow | Lento |
| `unavailable.title` | This isn't available right now | Esto no está disponible ahora |
| `unavailable.manual` | Open the manual page | Abrir la página del manual |
| `help.title` | Emergency help | Ayuda de emergencia |
| `help.survival` | To start survival mode, press **Start survival mode** on the home screen, or open a terminal and type `aictl mode survival`. | Para iniciar el modo supervivencia, presione **Iniciar modo supervivencia** en la pantalla de inicio, o abra una terminal y escriba `aictl mode survival`. |
| `help.drive` | Plug in the AI-ARK drive, then press **Import from drive** in the app, or type `aictl ark import`. | Conecte el disco AI-ARK y presione **Importar desde el disco** en la aplicación, o escriba `aictl ark import`. |
| `help.library` | If the library button doesn't work, open `http://127.0.0.1:8888/library/` in the browser. | Si el botón de la biblioteca no funciona, abra `http://127.0.0.1:8888/library/` en el navegador. |
| `help.codes` | A code like `LAI-SVC-001` comes with a one-sentence fix. Do what it says. The manual *Troubleshooting* has more. | Un código como `LAI-SVC-001` viene con una solución de una frase. Haga lo que dice. El manual *Solución de problemas* tiene más. |
| `help.emergency` | Emergency number: {n} | Número de emergencias: {n} |
| `help.lastResort` | If nothing works: restart the computer and wait 2 minutes. | Si nada funciona: reinicie la computadora y espere 2 minutos. |
| `size.label` | Text size | Tamaño del texto |

Doctor-code titles and fixes come from the agent in the UI language (§3.5).

### 4.10 Accessibility
- **Text:** base 20 px, buttons 30 px bold, never below 16 px anywhere. Three text-scale steps (100 %, 125 %, 150 %) via the `A A+ A++` control and `Ctrl +`/`Ctrl −`; layouts reflow (CSS grid with `minmax`), so 150 % at 1366×768 scrolls vertically but never horizontally. Browser zoom up to 200 % is supported.
- **Font:** Atkinson Hyperlegible (SIL OFL), regular and bold, embedded as WOFF2 (Latin + Latin-1 + Latin Extended-A, which covers Spanish), with `system-ui, sans-serif` fallback.
- **Contrast:** all text ≥ 7:1 (WCAG AAA) against its background; button accent colours are used only for borders and icons, never as the only carrier of meaning (each state also has text and an icon: ✓, !, ✗).
- **Keyboard only:** everything reachable with `Tab`/`Shift+Tab` in reading order; `Enter`/`Space` activate; `1`–`8`, `F`, `Esc` as in §4.1.1; `Space` starts/stops recording on Talk; no keyboard traps (the reader frame's header always receives `Esc` via a `keydown` listener on the parent and, same-origin, on the iframe document). Focus ring 4 px, `#1A4FD6`, with a 2 px white inner outline, visible on every element.
- **Screen readers:** semantic landmarks (`header`, `main`, `nav`), every button has an accessible name and description, status changes are announced via an `aria-live="polite"` region (errors `assertive`), and streaming answers announce only when complete.
- **Motion:** no animation other than the recording level meter and a loading spinner; both honour `prefers-reduced-motion` (static "Recording" text instead).
- **Touch:** targets ≥ 48×48 px; long-press equals right-click on the map.
- **No hover-only information**, no timeouts that dismiss content, no colour-only status.
- **Checks:** axe-core runs in Playwright on every Portal screen in CI (zero serious/critical violations), plus a keyboard-only Playwright walk-through of every flow.

### 4.11 Offline asset rule
Everything the Portal needs is embedded in the agent binary with `go:embed` (built by Vite into `agent/ui/dist/`): JS, CSS, the WOFF2 font, inline SVG icons (no icon fonts), translation files, the embedded emergency page and the drill test media. Nothing is loaded from any other origin, and there is no service worker.

Enforcement:
1. **Response header** on every `/portal*` and `/maps/` response (it **replaces** the agent's global CSP of [agent.md](agent.md) §3.11.3 on these paths; companion-services.md §4.4.3 refers here for `/maps/`):
   `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; connect-src 'self'; worker-src 'self' blob:; frame-src 'self'; frame-ancestors 'self' localai://app; base-uri 'none'; form-action 'self'`.
   (No `unsafe-inline`: Vite emits external CSS; inline `style` attributes from React are allowed by CSP3 only via `style-src-attr`, so the build uses classes only and a lint rule forbids `style={{…}}` in `portal/`.)
2. **Build scan** (`scripts/check-offline-assets.mjs`, CI): fails if any file in `agent/ui/dist/` contains `https?://` other than an allowlist (`http://127.0.0.1:7070`, `http://localhost`, license/attribution text in comments, `http://www.w3.org/2000/svg` namespaces), or references `fonts.googleapis`, `unpkg`, `jsdelivr`, `cdn`.
3. **Playwright offline test:** every Portal screen is opened with `page.route('**/*')` aborting any request whose host is not `127.0.0.1`; the test fails if such a request is even attempted.
4. **No runtime fetch of the WebUI from elsewhere:** the WebUI is part of llama-server's own binary (`tools/ui` embedded), so `/chat/` is offline too.

### 4.12 Survival card
A one-page printable card, rendered per node by `aictl docs render` ([aictl.md](aictl.md) §docs) from the template `docs/runbooks/00-survival-card.md` (placeholders like `<NODE_ID>` filled from the inventory and node state) into `<root>/docs/survival-card-<node-id>.pdf` and `.html`. It is listed under Manuals and linked from START-HERE.html ([ark-sync.md](ark-sync.md) §4.15).

Format: US Letter, content kept inside an 8.0 × 10.5 in safe area so it also prints on A4 without scaling loss. Two columns: English left, Spanish right. Headings 18 pt, body 11 pt minimum, black on white, no colour needed. It must fit on one page (the renderer fails with `LAI-CLI-015` if it overflows).

Content (both columns):
1. Title: "SURVIVAL CARD — Local AI computer <NODE_ID>" / "TARJETA DE SUPERVIVENCIA — computadora de IA local <NODE_ID>".
2. **Start:** 1) Plug in the charger. 2) Turn on and wait 2 minutes. 3) Log in as **<DESKTOP_USER>** (the password is not written here; the value comes from the node's inventory field `desktop_user`, else the primary user recorded by the installer in `/var/lib/localai/install/state.json`). 4) Open the **Local AI** app — or open a web browser and type `127.0.0.1:7070/portal`. 5) Press **Start survival mode**.
3. **The eight buttons**, one line each (from §4.9 `btn.*` and `.sub`).
4. **First aid:** "Press F on the home screen. This is not a doctor; if any phone works call <EMERGENCY_NUMBER>."
5. **If the app doesn't open:** open a terminal and type `aictl mode survival`, then open the browser address above. **Library only:** `127.0.0.1:8888/library/`.
6. **Ark drive:** "Plug it in. Open `START-HERE.html` on the drive."
7. **Battery:** "Keep it plugged in when you can. Closing the lid does not turn it off."
8. **Error codes:** "A code like `LAI-SVC-001` comes with a fix. More in the manual *Troubleshooting*."
9. Footer: node id, brains installed (display names), catalog version, render date. **Never** the PIN, keys, IP addresses or passwords. The renderer refuses to render the card (render fails with `LAI-CLI-016`, [aictl.md](aictl.md) §4.22) if it contains any placeholder that is secret: a fixed list built into the renderer (every `<TAILNET_IP_*>`, `<TAILNET>`, `<ROUTE53_ZONE_ID>`, `<VPS_PUBLIC_IP>`, the VPS hostname placeholder, any e-mail placeholder) **plus** any placeholder whose inventory field is named in the top-level `secret_fields` list of `inventory.example.yaml`. Placeholders map to inventory fields by pairing each placeholder value in `inventory.example.yaml` with the same YAML path in `inventory.local.yaml` (e.g. `nodes[id=ai-n1].tailnet_ip` ↔ `<TAILNET_IP_AI_N1>`; [security-model.md](../architecture/security-model.md) §5).

## 5. Failure handling and edge cases

| Situation | Behaviour | Code |
|---|---|---|
| Embedded Portal assets missing (bad build) | agent serves a minimal built-in HTML page with links to `/chat/`, `/library/`, `/maps/`, `/docs/` and the survival instructions | `LAI-SVC-100` |
| Chat opened outside survival without a session | locked page with Start survival / Log in | `LAI-SVC-101` |
| llama down or no brain | `/chat/` 502 page; Chat button `unavailable` | `LAI-SVC-102`, `106` |
| Microphone blocked or missing | explanation screen | `LAI-SVC-103` |
| Browser can't record | message | `LAI-SVC-104` |
| Path forbidden by the chat proxy | `403` page | `LAI-SVC-115` |
| Portal rate limit hit (model loads / speech) | `429`, message "wait a few minutes" | `LAI-SVC-114` / `LAI-SVC-015` |
| Audio can't be decoded in the browser | message, try again | `LAI-SVC-105` |
| Brain switch fails or takes > 300 s | previous default reloaded | `LAI-SVC-107` |
| Photo too big or unreadable | message | `LAI-SVC-108` |
| First-aid article id doesn't exist in the ZIM | entry hidden; reported on the Status screen | `LAI-SVC-109` |
| Portal session expired or mode left survival | SPA reloads `/portal/status`; session-only buttons lock | `LAI-SVC-110` |
| Battery ≤ 20 % / ≤ 10 % on battery | banner amber / red | `LAI-SVC-111`, `112` |
| No GPU (cpu-only profile) | Switch brain shows "Slow" hints; Talk still works | `LAI-SVC-113` (info) |
| Two browser tabs recording | each tab has its own state; the server queue limits STT | `LAI-SVC-015` |
| Mode changes to online while the Portal is open | `mode.changed` event; banner "Survival mode is off"; Portal stays usable for no-auth parts | — |
| Clock is wrong | Status shows `status.clockWrong` | — |
| Kiwix search returns HTML instead of XML (older kiwix-serve) | the SPA shows the results page in the reader frame instead | — |

## 6. Security considerations

- The Portal itself is read-only and unauthenticated by design (security-model.md §3). `/portal/status` exposes no secrets or network details (§3.5); a unit test asserts the JSON never contains strings matching the redaction patterns or file paths.
- The Portal session can only do what a person at the keyboard in an emergency needs (§3.3). It can't read the key, change mode out of survival, delete, rotate or reconfigure.
- The `/chat/` proxy never exposes the key to the browser, strips cookies, enforces same-origin for writes, and blocks router admin paths (downloads, deletes) and model loads that bypass the fit rules.
- DNS rebinding is blocked by the agent's Host check (security-model.md §2), which applies to `/portal`, `/chat/`, `/library/` and `/maps/` too.
- Photos are re-encoded in the browser (EXIF and GPS removed) and are never stored on disk by the agent. Transcripts and chat text are not logged.
- CSP (§4.11) limits the blast radius of any XSS in the Portal and stops all external loads.
- Clickjacking: `frame-ancestors 'self' localai://app`.

## 7. Observability

- Logs (component `portal`): `portal.session_issued` (no cookie value), `chat_proxy` per request at DEBUG only (`path`, `status`, `ms`, `bytes`; never bodies), `chat_proxy_blocked` at INFO with path and reason.
- Events: the SPA consumes `mode.changed`, `model.*`, `service.state`, `drive.*`, `alert.*`.
- Metrics: `localai_portal_sessions_active`, `localai_chat_proxy_requests_total{result}`, `localai_portal_status_requests_total`.
- Client-side errors (JS exceptions, failed steps) are shown on screen and kept in an in-memory ring that the Status screen shows under "Details". There is no client-log route (§11).

## 8. Doctor codes (LAI-SVC-100 … 149)

| Code | Sev | Meaning | Fix (en) | Fix (es) |
|---|---|---|---|---|
| LAI-SVC-100 | error | The Portal's screens are missing from the program | Use the links on this page. Then run `sudo install-node.sh --repair`. | Use los enlaces de esta página. Luego ejecute `sudo install-node.sh --repair`. |
| LAI-SVC-101 | info | Chat is locked outside survival mode | Press **Start survival mode**, or log in. | Presione **Iniciar modo supervivencia**, o inicie sesión. |
| LAI-SVC-102 | error | The AI (llama) isn't answering | Press **Switch brain** and choose one, or restart the computer. | Presione **Cambiar cerebro** y elija uno, o reinicie la computadora. |
| LAI-SVC-103 | warning | The microphone is blocked or missing | Allow the microphone in the browser (lock icon next to the address), or plug one in. | Permita el micrófono en el navegador (candado junto a la dirección), o conecte uno. |
| LAI-SVC-104 | warning | This browser can't record | Use the Local AI app or Chromium/Firefox. | Use la aplicación IA Local o Chromium/Firefox. |
| LAI-SVC-105 | warning | The recording couldn't be processed | Try again. | Intente otra vez. |
| LAI-SVC-106 | warning | No brain is running | Press **Switch brain** and choose one. | Presione **Cambiar cerebro** y elija uno. |
| LAI-SVC-107 | error | The new brain couldn't start | Choose a smaller brain. The previous one was restarted. | Elija un cerebro más pequeño. Se reinició el anterior. |
| LAI-SVC-108 | warning | The photo can't be used | Try another photo (JPEG or PNG, under 30 MB). | Pruebe otra foto (JPEG o PNG, menos de 30 MB). |
| LAI-SVC-109 | warning | A first-aid link points to a missing article | Report it. Use Library search meanwhile. | Repórtelo. Mientras tanto use la búsqueda de la Biblioteca. |
| LAI-SVC-110 | info | The Portal session ended | Reload the page. | Recargue la página. |
| LAI-SVC-111 | warning | Battery low | Plug in the charger when you can. | Conecte el cargador cuando pueda. |
| LAI-SVC-112 | critical | Battery very low | Plug in now, or the computer will turn off. | Conecte el cargador ya, o la computadora se apagará. |
| LAI-SVC-113 | info | No graphics card in use; answers are slow | Nothing to do. Short questions work best. | Nada que hacer. Las preguntas cortas funcionan mejor. |
| LAI-SVC-114 | warning | Too many brain switches; wait a few minutes | Wait a few minutes, then switch again. | Espere unos minutos y vuelva a cambiar. |
| LAI-SVC-115 | warning | That chat page isn't available from the Portal | Use the chat normally; settings and downloads are done by an operator in the Local AI app. | Use el chat normalmente; la configuración y las descargas las hace un operador en la aplicación IA Local. |

## 9. Test plan

### 9.1 Unit
| Area | Cases |
|---|---|
| capability computation | every row of §4.2 from fake supervisor/model/ark states |
| `/portal/status` | redaction (no paths, IPs, tokens), language selection, 2 s cache |
| Portal session | issued only in survival; invalidated on mode change; scope allowlist (every route of the route table not allowlisted → `401 LAI-SVC-110`); `Origin` required (Sec-Fetch-Site alone refused); forced load body; rate limits (`429` + code). These are the agent.md §9.1 "auth (portal)" tests, owned by PORTAL-02 |
| status line | colour rule (red/amber/green) from capability fixtures |
| chat proxy | enable rule matrix (incl. anonymous survival); header stripping/injection; prefix strip; `POST /models/load` interception calls the agent load path; blocked paths; SSE flush; 502 page; key rotation picked up |
| i18n | key parity; placeholders present in both languages |
| sentence splitter | EN/ES punctuation, abbreviations ("Dr.", "p. ej."), numbers ("1.5") |
| WAV encoder (TS) | header bytes; 16 kHz resample length |

### 9.2 Integration (Playwright, against an agent with fakes for llama, whisper and piper)
- Home renders at 1366×768 without scroll; axe-core clean; keyboard-only walkthrough of every screen.
- Offline test (§4.11 item 3) on every screen.
- Talk: Chromium's fake media stream (`--use-fake-device-for-media-stream --use-file-for-fake-audio-capture=en.wav`) → transcript shown → streamed answer → TTS requests in order → audio played.
- Photo: upload a fixture JPEG with EXIF orientation 6 and GPS tags → the request image is ≤ 1024 px, correctly rotated, with no EXIF.
- Brain switch with events; failure path.
- `/chat/` with the real llama WebUI from llama-server in CI (CPU, a tiny model): page loads, `/chat/props` is requested, a chat completes.

### 9.3 Manual on ai-n3
1. `aictl mode survival`. Open Firefox at `http://127.0.0.1:7070/` → redirected to `/portal`; the home screen fills the screen at 1366×768 with no scrollbar.
2. Unplug the network. Press `3`, hold a question in Spanish, release → transcript in Spanish, spoken answer in the Spanish voice.
3. Press `2`, choose a photo of a plant → switch prompt (8 GB node) → answer after the switch.
4. Press `1` → the WebUI opens without asking for a key; the model dropdown shows catalog ids; choosing another model goes through the agent (check `journalctl -u localai-agent | grep 'model load'`).
5. Press `F`, then "Severe bleeding" → the WikiMed article opens in the reader; `Esc` returns home.
6. Unplug the charger → battery shown; Status screen correct.
7. `aictl mode online` (operator) → Portal shows "Survival mode is off"; Chat shows the lock.
8. Print the card from Manuals → one page, both languages, no secrets.

## 10. Implementation breakdown

1. **PORTAL-01 Portal status endpoint and capability model** (1.5 d). Scope: the capability model and the full §3.5 schema (incl. `clock_ok`, `needs_switch` without code, manuals urls from `docs/index.json`), capability rules §4.2, redaction test. The route, the 2 s cache and a minimal builder are AGENT-14 ([agent.md](agent.md) §10); this item fills the builder. Acceptance: table tests; JSON schema in `agent/api/openapi.yaml`.
2. **PORTAL-02 Portal session** (1 d). Scope: §3.3 cookie, scope allowlist middleware, invalidation on mode change, `Origin` check, forced load body, rate limits with `LAI-SVC-015`/`114`, and the Portal-session auth tests (agent.md §9.1 "auth (portal)" row; AGENT-06 only provides the auth hook). Acceptance: route matrix test.
3. **PORTAL-03 Chat proxy** (1.5 d). Depends on: PORTAL-02, ROUTER-05, AGENT-16. Scope: §4.4 incl. load/unload interception and blocked paths (`LAI-SVC-115`). Acceptance: unit tests; CI test with the real WebUI.
4. **PORTAL-04 Portal shell, home screen, i18n, accessibility base** (2 d). Scope: creates the Vite project scaffold (`agent/ui/`), `agent/ui/embed.go` and the Playwright/axe CI harness if absent; SPA routing (incl. `/portal/help` route), home grid, status line (colour rule §4.1.1), banners, language toggle, text scale, keyboard shortcuts, font embedding, `en.json`/`es.json` from §4.9, parity test. Acceptance: Playwright 1366×768 no-scroll, axe-core clean.
5. **PORTAL-05 Offline asset enforcement** (0.5 d). Scope: CSP header, build scan script, Playwright offline test. Acceptance: CI fails on an injected external URL.
6. **PORTAL-06 Status/Battery and Switch brain screens** (1.5 d). Depends on: PORTAL-01, PORTAL-02. Scope: §4.1.8–4.1.9, events, failure handling. Acceptance: Playwright with fake router events.
7. **PORTAL-07 Talk flow** (2 d). Depends on: PORTAL-02, SVC-07, SVC-08. Scope: §4.5 recording, browser conversion, STT, streaming chat, sentence pipelining, stop/repeat, error screens. Acceptance: fake-media Playwright test; manual step 2.
8. **PORTAL-08 Photo flow** (1.5 d). Depends on: PORTAL-03. Scope: §4.6 capture, resize/rotate/strip, switch prompt, streaming answer, read aloud. Acceptance: EXIF fixture test; manual step 3.
9. **PORTAL-09 Library and reader frame** (1.5 d). Scope: §4.1.5 search via OpenSearch XML, books list, reader with header and `Esc`. Acceptance: Playwright against the test ZIMs.
10. **PORTAL-10 Maps and Manuals screens** (0.5 d). Scope: reader-frame embedding of `/maps/` and `/docs/`, built-in emergency page §4.8 at `/portal/help` with the `help.*` keys of §4.9. Acceptance: works with docs missing.
11. **PORTAL-11 First-aid panel** (1 d). Depends on: CAT-01. Scope: §3.4 config, link resolution with undated book aliases, availability check, panel and full grid, ES fallback. Acceptance: missing article hidden + `LAI-SVC-109`.
12. **PORTAL-12 Survival card template** (1 d). Depends on: AICTL-08. Scope: `docs/runbooks/00-survival-card.md` with both columns, one-page check, secret-placeholder refusal (§4.12 fixed list + `secret: true` fields), `<DESKTOP_USER>` from `desktop_user`. Acceptance: rendered PDF is one page in Letter and A4.

## 11. Open questions (with proposed defaults)

1. **Portal session scope** extends the survival exception of security-model.md §3 and the auth rules of agent.md. *Default:* adopt §3.3 as written; security-model.md gets a one-line addition referencing it.
2. ~~**Catalog `portal_links`**~~ Resolved (2026-11-02): catalog.md §3 defines it with these lint rules.
3. **`node.yaml: portal` block** is new. *Default:* agent.md includes it.
4. **Client error reporting** has no route. *Default:* none (in-memory display only); revisit if debugging needs it.
