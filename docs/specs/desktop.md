# Spec: ai-desktop (Electron shell)

> **Status:** approved design · **Epic:** E8 · **Code:** `apps/desktop/` · **Doctor area:** `LAI-DSK`
> Canonical names: [overview.md](../architecture/overview.md) §3 (desktop names in §3.8). Decision: [ADR-0004](../decisions/0004-desktop-thin-shell.md). Precedent: kvox-desktop (`marinoscar/kvox-desktop`: `electron-builder.yml`, `src/main/index.ts`, `src/main/updates.ts`, `src/server/egress-guard.ts`).

## 1. Purpose and scope

`ai-desktop` is the icon a non-expert double-clicks. It is a **thin Electron shell** over the agent's local UI (`http://127.0.0.1:7070/`). It adds only what a browser can't:

- reads the agent token and opens a session (so nobody types a code),
- detects a stopped agent and offers **Start agent**,
- a tray icon with status colour, quick mode switch and a **Survival mode** item,
- start at login,
- separate windows for Chat (`/chat/`), Library (`/library/`) and Maps (`/maps/`),
- **Copy API key** to the native clipboard and **Show connection info**,
- native PIN prompts for desktop-only actions,
- **Connected mode:** links to `ai.marin.cr` with EAB device authorization (RFC 8628) and shows a **Lab** tab,
- **Survival UX:** the Portal full screen.

**Non-goals**
- No domain logic, no database, no copy of agent state. The agent is the single source of truth.
- No server process inside the app (unlike kvox-desktop).
- Windows and macOS builds (Ubuntu only, [overview.md](../architecture/overview.md) §1).
- No auto-update by default, no telemetry, no crash upload.
- If Electron is broken, any browser at `http://127.0.0.1:7070` provides the same local UI (login with `aictl ui-code`). The desktop must never be the only way to do anything.

## 2. Concepts

| Term | Meaning |
|---|---|
| **Shell pages** | The desktop's own small pages (top bar, error screens, PIN prompt, connection info, device-link dialog, Lab view), served from the custom scheme `localai://app` out of `app.asar` |
| **Agent UI** | The pages served by the agent at `http://127.0.0.1:7070` (`/`, `/portal`, `/chat/`, `/library/`, `/maps/`, `/docs/`) |
| **Agent partition** | Electron session `persist:agent`: holds the agent session cookie; used by every view that shows agent pages |
| **Lab partition** | Electron session `persist:lab`: used only by the Lab view (shell page); holds no cookies (the cloud token stays in the main process) |
| **Connected mode** | The desktop holds a cloud token for `ai.marin.cr` and the node is `online`. Otherwise the desktop is in **Offline mode** (strict no egress) |

## 3. Interfaces

### 3.1 Project layout (`apps/desktop/`)
```
apps/desktop/
  package.json                 name "ai-desktop", productName "ai-desktop", desktopName "ai-desktop.desktop"
  package-lock.json
  electron-builder.yml
  electron.vite.config.ts      main, preload, renderer (shell pages) builds
  tsconfig.json tsconfig.node.json tsconfig.web.json
  vitest.config.mts  playwright.config.ts
  build/linux/icons/           16..512 PNG (generated from packages/ui/brand by `npm run icons`, committed)
  build/linux/icon.png         512 px copy, shipped as the `icon.png` extraResource
  build/linux/ai-desktop.apparmor
  build/linux/tray/            tray-{green,blue,orange,amber,red,grey}.png (22 px and 44 px @2x)
  scripts/icons.mjs            `npm run icons`: packages/ui/brand/icon.svg -> app and tray PNGs (sharp, offline)
  scripts/dist.mjs             wraps electron-builder --publish never
  scripts/packaged-app.mjs     verify (asar layout, fuses) | test-copy (re-enable inspect for e2e)
  src/main/
    index.ts                   app lifecycle, single instance, wiring
    agent-client.ts            health, token read, session, status/events (main side only)
    agent-supervisor.ts        systemctl/pkexec start, group-membership diagnosis
    windows.ts                 main window (BaseWindow + 2 WebContentsViews), chat/library/maps windows
    navigation.ts              will-navigate / window-open / redirect allowlists
    egress-guard.ts            session webRequest guard + main-process socket guard
    csp.ts                     CSP for localai:// responses
    protocol.ts                localai://app handler (path-traversal safe)
    tray.ts                    tray icon + menu
    autostart.ts               ~/.config/autostart/ai-desktop.desktop
    clipboard.ts               copy key + timed clear
    pin.ts                     PIN prompt window + /pin/verify
    survival.ts                full-screen Portal behaviour
    cloud-link.ts              device flow (RFC 8628), token storage
    cloud-api.ts               allowlisted cloud API proxy for the Lab view
    secret-store.ts            safeStorage wrapper + consent
    settings.ts                ~/.config/ai-desktop/settings.json
    updates.ts                 opt-in electron-updater (off by default)
    i18n.ts                    en + es strings for main-process dialogs, tray and notifications
    diagnose.ts                `ai-desktop --diagnose` (JSON doctor report on stdout)
  src/preload/
    shell.ts                   bridge for shell pages: window.localaiDesktop
    agent.ts                   minimal bridge for agent pages: window.localaiDesktopHost
  src/renderer/                shell pages (React 19 + MUI, packages/ui components, no network assets)
    topbar/  error/  pin/  connection-info/  link/  lab/
  tests/unit/  tests/e2e/  tests/fixtures/fake-agent.ts  tests/fixtures/fake-cloud.ts
```
The desktop builds its shell pages with `packages/ui` components, consumed through `"@localai/ui": "file:../../packages/ui"` (desktop CI, `.github/workflows/desktop-ci.yml` on PRs touching `apps/desktop/**` or `packages/ui/**`, builds from the repo root, so `file:` resolves; the release workflow is `desktop-release.yml`, §4.11).

**Who creates `packages/ui`.** Whichever of AGENT-07, DSK-01 or CLOUD-01 lands first creates it if it is absent, with the minimum: `packages/ui/package.json` (`"name": "@localai/ui"`, private) and the brand source image `packages/ui/brand/icon.svg` (the input of `npm run icons`). CLOUD-11 owns the shared components (node card, status chips) that the desktop Lab view reuses. `packages/ui` already exists (created by the cloud and agent items), so DSK-01 adds only the `file:` dependency.

**Build setup (DSK-01).** The package is CommonJS (no `"type": "module"`) because a sandboxed preload must be CommonJS; electron-vite writes `out/main/index.js`, `out/preload/{shell,agent}.js` and `out/renderer/<page>/index.html`. Every dependency is a `devDependency` (renderer libraries are bundled by Vite), so `app.asar` holds only `out/**` and `package.json`, and `dependencies` stays empty. All versions are exact (no `^` or `~`). Electron 44 has no install hook: run `node node_modules/electron/install.js` after `npm ci` (desktop CI does).

**Shell pages and the `file://` fuse.** `grantFileProtocolExtraPrivileges` is off (section 3.3), and with it Electron no longer serves `file://` URLs out of `app.asar`. The shell pages therefore cannot be loaded from the packaged app until the `localai://app` protocol (section 3.7, DSK-02) exists. Until then the packaged DSK-01 window shows a static placeholder page (`data:` URL); in `npm run dev` it loads the Vite dev server's `topbar` page.

### 3.2 Electron version policy
- `electron` is pinned to an **exact** version in `devDependencies` (no caret), like kvox-desktop. Start at the version kvox-desktop uses (44.x) and keep the bundled Node ≥ 24.
- Stay on a major that Electron still supports (the newest three majors). Upgrade within two weeks of a security release of the pinned major, and move to a newer major before the pinned one leaves support. Renovate opens the PR (`apps/desktop/**` rule, `rangeStrategy: pin`); a PR that bumps Electron must pass the packaged e2e suite (§9).
- `electron-builder`, `@electron/fuses`, `electron-vite` follow the versions kvox-desktop pins unless a security fix needs newer.
- **Versions chosen in DSK-01 (2026-11-04).** kvox-desktop's `package.json` was not readable from the session that built the scaffold, so the pins are the newest releases that agree with each other and with Node 24: `electron` **44.10.2** (the newest 44.x; bundled Node is 24.x), `electron-builder` 26.18.0, `@electron/fuses` 2.1.3, `electron-vite` 6.0.0 (with `vite` 8.3.5, `@vitejs/plugin-react` 6.2.1, `vitest` 5.1.0, `typescript` 6.0.3). When kvox-desktop is next compared, a difference is a Renovate bump, not a bug. The first Renovate pass moves them forward within the policy above.

### 3.3 `electron-builder.yml`
```yaml
appId: cr.marin.localai.desktop
productName: ai-desktop
copyright: Copyright © 2026 Oscar Marin
directories: { output: release, buildResources: build }
asar: true
files:
  - out/**
  - package.json
  - "!**/*.map"
extraResources:
  - { from: build/linux/icon.png, to: icon.png }
  - { from: build/linux/tray, to: tray }
npmRebuild: false
electronFuses:
  runAsNode: false
  enableCookieEncryption: true
  enableNodeOptionsEnvironmentVariable: false
  enableNodeCliInspectArguments: false
  enableEmbeddedAsarIntegrityValidation: true
  onlyLoadAppFromAsar: true
  loadBrowserProcessSpecificV8Snapshot: false
  grantFileProtocolExtraPrivileges: false
publish:                    # metadata only (latest-linux.yml); nothing is uploaded by electron-builder
  provider: github
  owner: marinoscar
  repo: local-ai
  releaseType: draft
linux:
  icon: build/linux/icons
  target: [ { target: deb, arch: [x64] }, { target: AppImage, arch: [x64] } ]
  executableName: ai-desktop
  syncDesktopName: true
  category: Utility
  maintainer: Local AI maintainers <noreply@marin.cr>
  synopsis: Local AI on this computer, with or without internet.
  description: Desktop app for the Local AI node agent. Works fully offline.
deb:
  artifactName: ai-desktop_${version}_${arch}.${ext}   # electron-builder's deb default: ai-desktop_<version>_amd64.deb (arm64: _arm64)
  appArmorProfile: build/linux/ai-desktop.apparmor
  depends: [libnotify4, libxtst6, libnss3, xdg-utils, libsecret-1-0, policykit-1]
  recommends: [gnome-shell-extension-appindicator]
appImage:
  artifactName: ai-desktop-${version}-x86_64.AppImage
```
Two differences from the first draft of this block, both found when DSK-01 ran the build: the AppImage key is `appImage` (camelCase; `AppImage:` fails electron-builder's schema validation), and there is **no `LICENSE.txt` extraResource**: the repository has no license file yet (README section 12), so there is nothing to copy and no license is invented. Add `{ from: ../../LICENSE, to: LICENSE.txt }` back when the owner adds one.
Only `deb` and `AppImage`. The deb installs to `/opt/ai-desktop/` with `/usr/bin/ai-desktop` → `/opt/ai-desktop/ai-desktop`, and `/usr/share/applications/ai-desktop.desktop`. The AppArmor profile is kvox-desktop's (`flags=(unconfined)`, `userns,`) for `/opt/ai-desktop/ai-desktop`, needed on Ubuntu 24.04+ for the Chromium sandbox.

Process switches set in `index.ts` before `ready`: `app.commandLine.appendSwitch('disable-background-networking')`, `'no-pings'`, `'disable-component-update'`, `'disable-domain-reliability'`, `'disable-features', 'AutofillServerCommunication,MediaRouter,OptimizationHints,Translate'`. `spellcheck: false` on every webContents (dictionary downloads).

### 3.4 Windows and views
**Main window:** a `BaseWindow` (1280×860, min 900×600, title "Local AI") with two `WebContentsView`s:
- **top bar** (48 px high, `localai://app/topbar/`, preload `shell.ts`, default session): tabs **This computer** | **Lab** (Lab only when connected), status pill, mode chip, buttons **Chat**, **Library**, **Maps**, **Copy API key**, menu ⋮ (Connection info, Connect to the lab / Disconnect, Start at login ✓, Check for updates ✓/✗, About, Quit).
- **content**: either agent UI `http://127.0.0.1:7070/` (partition `persist:agent`, preload `agent.ts`), the Lab view `localai://app/lab/` (partition `persist:lab`, preload `shell.ts`), or an error page `localai://app/error/?code=LAI-DSK-00N` (default session).

**Child windows** (`BrowserWindow`, partition `persist:agent`, preload none): Chat → `http://127.0.0.1:7070/chat/`, Library → `/library/`, Maps → `/maps/`. One window each (re-focus if open). Navigation is confined to the window's own path prefix plus `/api/` and asset paths of the agent origin (§3.6).

All webContents: `contextIsolation: true`, `sandbox: true`, `nodeIntegration: false`, `webSecurity: true`, `spellcheck: false`, `devTools: !app.isPackaged`. Permission requests (`setPermissionRequestHandler`) are denied except `clipboard-sanitized-write` for shell pages and `media` (microphone) for the agent origin's speech page.

### 3.5 Preload bridges (the whole IPC surface)
`window.localaiDesktop` (shell pages only):
```ts
interface LocalaiDesktop {
  status(): Promise<DesktopStatus>;                         // agent reachability, mode, connectivity, alerts count, cloud link
  onStatus(cb: (s: DesktopStatus) => void): () => void;
  startAgent(): Promise<{ ok: boolean; code?: string }>;   // §4.2
  copyApiKey(): Promise<{ ok: boolean; clearsInS: number }>;
  connectionInfo(): Promise<ConnectionInfo>;               // §4.6 (key masked until reveal)
  revealKey(): Promise<string>;                            // audited by the agent's GET /key
  setMode(target: 'online' | 'offline' | 'survival'): Promise<{ ok: boolean; code?: string }>;
  openWindow(which: 'chat' | 'library' | 'maps'): void;
  showTab(tab: 'local' | 'lab'): void;
  cloud: {
    beginLink(): Promise<{ userCode: string; verificationUri: string; expiresIn: number }>;
    openVerification(): void;                               // opens https://ai.marin.cr/activate?code=… in the default browser
    linkState(): Promise<CloudLinkState>;
    unlink(): Promise<void>;
    request(req: { method: 'GET' | 'POST' | 'DELETE'; path: string; body?: unknown }): Promise<CloudResponse>;  // allowlisted, §4.8
  };
  settings: { get(): Promise<ShellSettings>; set(patch: Partial<ShellSettings>): Promise<ShellSettings> };
  pinSubmit(pin: string): Promise<{ ok: boolean; code?: string }>;   // only from the PIN window
  lang(): 'en' | 'es';
}
```
`window.localaiDesktopHost` (agent pages, so the agent UI can adapt): `{ isDesktop: true, version: string, copyApiKey(), openWindow(which), connectionInfo() }`. Nothing else. Each IPC handler checks `event.senderFrame.url` origin (shell handlers accept only `localai://app`; host handlers only `http://127.0.0.1:7070`).

### 3.6 Navigation and egress allowlists

**Navigation** (`will-navigate`, `will-redirect`, `setWindowOpenHandler`):
| From | Allowed destinations | Anything else |
|---|---|---|
| content (agent) | `http://127.0.0.1:7070/*` (a `localhost:7070` URL is rewritten to `127.0.0.1:7070`) | links to `/chat/`, `/library/`, `/maps/` open the matching child window; `https://ai.marin.cr/*` opens in the default browser **only** in connected mode; everything else is blocked and the top bar shows "Links to the internet are turned off on this computer." / "Los enlaces a internet están desactivados en esta computadora." |
| child windows | same origin, same path prefix (`/chat/`, `/library/`, `/maps/`) plus `/api/v1/` and static assets | blocked silently (Kiwix articles contain external links; they are offline content) |
| top bar, Lab, error, PIN | `localai://app/*` | blocked |
`window.open` always returns `{ action: 'deny' }` after routing as above.

**Egress** (two layers, both installed before any window exists):
1. **Session layer:** `webRequest.onBeforeRequest` on the default session, `persist:agent`, `persist:lab`, and the updater session. Allowed: schemes `localai:`, `data:`, `blob:`, `devtools:` (dev only); `http://127.0.0.1:7070/*` and `ws://127.0.0.1:7070/*`. Nothing else, ever, from any renderer (the Lab view gets cloud data through IPC, not fetch).
2. **Main-process layer** (port of kvox-desktop `src/server/egress-guard.ts` into `src/main/egress-guard.ts`): a global undici dispatcher plus a wrapped `net.Socket.prototype.connect`. Always allowed: loopback (127.0.0.0/8, `::1`, `localhost`) and Unix sockets (`/run/localai/agent.sock`). Additionally allowed **only while connected mode is active** (§4.9): `ai.marin.cr` on port 443. While "Check for updates" is on: `github.com`, `objects.githubusercontent.com`, `release-assets.githubusercontent.com` on 443, only from the updater.
Every block is logged at error level with the host only and counted; the count is in `--diagnose`.

### 3.7 CSP for shell pages
`protocol.ts` adds to every `localai://app` response:
```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:;
  font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'
X-Content-Type-Options: nosniff
```
(`'unsafe-inline'` for styles only, because MUI/Emotion injects style tags.) Agent pages carry the agent's own CSP ([agent.md](agent.md) §3.11). The scheme is registered privileged: `{ standard: true, secure: true, supportFetchAPI: true }`; the handler resolves paths inside `out/renderer` only (normalise + `relative()` check, kvox-desktop's pattern) and falls back to the page's `index.html`.

### 3.8 Files
| Path | Contents |
|---|---|
| `~/.config/ai-desktop/settings.json` | `{ "schema": 1, "autostart": true, "updates": { "checkForUpdates": false }, "cloud": { "url": "https://ai.marin.cr", "linked": false }, "secrets": { "allowPlaintext": false }, "window": { "bounds": {…} }, "lang": "auto" }` (0600) |
| `~/.config/ai-desktop/cloud-token.bin` | safeStorage-encrypted cloud PAT (0600); absent when not linked |
| `~/.config/ai-desktop/lab-cache.json` | last Lab data (node cards, no secrets), for the unreachable view (0600) |
| `~/.config/ai-desktop/logs/main.log` | main-process log, rotated at 5 MB × 3; never tokens or keys |
| `~/.config/autostart/ai-desktop.desktop` | §4.5 |

### 3.9 CLI flags
`ai-desktop` (normal), `ai-desktop --hidden` (start in tray; used by autostart), `ai-desktop --portal` (open the Portal full screen), `ai-desktop --diagnose` (print the §8 checks as JSON and exit, no window).

## 4. Behaviour

### 4.1 Start-up sequence
```
single-instance lock ──(second launch: focus window, pass --portal if given)
  ▼
install egress guards, protocol, CSP ─► read settings ─► tray
  ▼
probe agent: GET http://127.0.0.1:7070/api/v1/health (1 s timeout, 3 tries 300 ms apart)
  ├─ unreachable ─► diagnose unit (systemctl is-active localai-agent.service) ─► error page LAI-DSK-004 [Start agent]
  ▼
read /etc/localai/agent-token
  ├─ EACCES ─► group diagnosis (§4.2) ─► error page LAI-DSK-001 or 002
  ├─ ENOENT ─► error page LAI-DSK-003
  ▼
POST /api/v1/session (Authorization: Bearer lat_…) via session.fromPartition('persist:agent').fetch
  ├─ 401 ─► re-read token once ─► still 401 ─► LAI-DSK-007
  ▼
GET /api/v1/status ─► mode = survival ? survival UX (§4.7) : load agent UI in content view
  ▼
subscribe GET /api/v1/events (SSE, bearer, main process) ─► tray + top bar updates; reconnect with backoff 1→30 s
```
The token is read by the main process only, kept in memory, never sent to a renderer. The agent session cookie (HttpOnly, SameSite=Strict, set by the agent on `127.0.0.1:7070`) lives in `persist:agent`, which uses cookie encryption (fuse). On any `401` seen by `webRequest.onCompleted` for `http://127.0.0.1:7070/api/v1/*` in the agent partition, the main process re-reads the token file (it may have been rotated, [security-model.md](../architecture/security-model.md) §2), re-creates the session and reloads the content view (at most once per 30 s).

### 4.2 Token-file errors and "Start agent"
**Group diagnosis** on `EACCES`:
- user listed in `getent group localai-operators` but `process.getgroups()` lacks its gid → **LAI-DSK-002**: "You were added to the Local AI operators group, but you need to log out and log back in once." / "Te agregaron al grupo de operadores de Local AI, pero debes cerrar sesión y volver a entrar una vez." Button **Log out now** (`gnome-session-quit --logout` if present, else instructions).
- not listed → **LAI-DSK-001**: "This user is not allowed to control Local AI yet." Shows the exact fix: `sudo usermod -aG localai-operators $USER`, then log out and in. Button **Copy command**. Spanish text alongside.

**Start agent** (LAI-DSK-004 page and tray item):
1. `execFile('/usr/bin/systemctl', ['start', '--no-ask-password', 'localai-agent.service'])`: succeeds without a prompt when the installer's polkit rule `/etc/polkit-1/rules.d/50-localai-operators.rules` is present (installed by default, skipped with `--no-polkit`, [installer.md](installer.md) §4.3 P5).
2. If it fails with an authorization error: `execFile('/usr/bin/pkexec', ['/usr/bin/systemctl', 'start', 'localai-agent.service'])`: the system asks for the administrator password. Exit 126/127 = cancelled/not authorised → LAI-DSK-005.
3. Poll `/api/v1/health` every 1 s for 30 s, then continue the start-up sequence. If still down: show `systemctl status localai-agent.service --no-pager -n 20` output (read-only, no privilege) and the fix "Run `sudo aictl doctor` in a terminal".
No shell is ever used; arguments are fixed.

### 4.3 Tray
Status colour (icon file) and meaning, computed from the agent status and events:
| Colour | When | Tooltip (en / es) |
|---|---|---|
| green | agent up, mode online, no warning/critical alerts | "Local AI: ready (online)" / "Local AI: listo (en línea)" |
| blue | mode offline | "Local AI: ready (offline)" / "Local AI: listo (sin conexión)" |
| orange | mode survival | "Local AI: survival mode" / "Local AI: modo supervivencia" |
| amber | warning alerts, a model failed to load, or connectivity `none` for 30 min while online | "Local AI: needs attention" / "Local AI: necesita atención" |
| red | agent unreachable or a critical alert | "Local AI: not working, click for help" / "Local AI: no funciona, haz clic para ayuda" |
| grey | starting | "Local AI: starting" / "Local AI: iniciando" |

Menu:
```
● Local AI: ready (online) · qwen3.5-9b-q4km
──────────────
Open Local AI
Chat · Library · Maps
──────────────
Mode ▸  (•) Online   ( ) Offline
⚠ Survival mode                    (always enabled, no PIN)
──────────────
Copy API key
Show connection info
Lab (ai.marin.cr)                  (connected only)
Start agent                        (only when the agent is down)
──────────────
✓ Start at login
Quit
```
Switching Online↔Offline calls `POST /api/v1/mode {target}`. Leaving **survival** from the tray needs the PIN (§4.6). "Survival mode" asks one confirmation ("Switch to Survival mode? Everything you need offline will start." / "¿Cambiar a modo supervivencia? Se iniciará todo lo necesario sin internet.") and calls `POST /api/v1/mode/survival`. Closing the main window hides it to the tray; **Quit** quits. If no tray is available (no AppIndicator), the app shows a one-time notice (LAI-DSK-013) and closing the window quits.

### 4.4 Notifications
Native `Notification` from the main process (no web notifications): mode changed, model failed to load, sync done/failed, drive mounted/unmounted, alerts raised with severity ≥ warning, the "no internet for 30 minutes" suggestion ([modes.md](../architecture/modes.md) §4). Text in the UI language. At most one notification per event kind per 5 minutes.

### 4.5 Autostart
`~/.config/autostart/ai-desktop.desktop`, written by `autostart.ts` when `settings.autostart` is true (default **true** on first run; the ark should be ready after a reboot), removed when false:
```ini
[Desktop Entry]
Type=Application
Name=Local AI
Exec=/usr/bin/ai-desktop --hidden
Icon=ai-desktop
X-GNOME-Autostart-enabled=true
X-GNOME-Autostart-Delay=5
NoDisplay=false
```
For the AppImage, `Exec` is the AppImage's absolute path (`process.env.APPIMAGE`). With `--hidden`, the app starts in the tray unless the node is in survival mode, in which case it opens the Portal full screen (§4.7).

### 4.6 Copy API key, connection info and PIN prompts
- **Copy API key:** main calls `GET /api/v1/key` (bearer; the agent writes an audit event), `clipboard.writeText(key)`, shows "API key copied. It will be cleared from the clipboard in 60 seconds." / "Clave copiada. Se borrará del portapapeles en 60 segundos." After 60 s, if the clipboard still holds exactly that key, it is cleared.
- **Show connection info** (`localai://app/connection-info/`, modal window): from the same `/key` response ([agent.md](agent.md) §3.8.13: key, base URLs, snippets). Shows:
  - Local URL `http://127.0.0.1:8080/v1` (works offline), the node's public URL when online, and the lab gateway `https://ai.marin.cr/v1` (needs a `lai_` key from the cloud, not this key),
  - the key masked `ab12…9z0f` with **Reveal** and **Copy**,
  - tabs with ready snippets: **OpenCode** (provider block like [gateway.md](gateway.md) Appendix A.2 but `baseURL` local and `apiKey` `{file:~/.config/opencode/llama-api-key}` plus the command to write that file), **Open WebUI** (URL + key, for a local Open WebUI), **curl** (`curl http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $(cat ~/.config/opencode/llama-api-key)"`).
- **PIN prompts:** the agent UI handles its own PIN dialogs. The desktop shows a native PIN window only for actions it starts itself: leaving survival from the tray, and **Rotate key** from connection info. Flow: modal `localai://app/pin/` (digits only, 4–8, masked, Spanish/English) → `POST /api/v1/pin/verify {pin}` → `{grant}` → the action is sent with `X-LocalAI-Pin-Grant: <grant>`. The grant is used for that one action and discarded. A 429 from the agent shows "Too many tries. Wait 5 minutes." (LAI-SEC code from the agent). The PIN never touches disk or logs.

### 4.7 Survival UX
On `mode.changed` → `survival` (or start-up in survival, or `--portal`):
1. Show and focus the main window, `setFullScreen(true)`, content view → `http://127.0.0.1:7070/portal`, top bar collapsed to a 32 px strip: "Survival mode" / "Modo supervivencia", language toggle EN/ES, **Exit full screen**. Tabs and the Lab are hidden.
2. Closing the window hides it but a tray click or a re-launch returns to the full-screen Portal. Quit stays available from the tray (an operator can always get out of the app; the agent keeps running).
3. Not `setKiosk` (it blocks the system's own keys on some desktops and could trap a user).
4. Leaving survival happens in the Portal (agent PIN) or the tray (desktop PIN). On `mode.changed` → other: exit full screen and load the dashboard.
5. The Portal is served by the agent without auth, so it shows even when the token file is unreadable (the desktop loads `/portal` directly in that case and shows LAI-DSK-001 as a small banner instead of the error page).

### 4.8 Connected mode (cloud link)
**Link** (Settings ⋮ → **Connect to the lab**), RFC 8628 against EAB ([EAB DEVICE-AUTH.md](https://github.com/marinoscar/EnterpriseAppBase/blob/main/docs/DEVICE-AUTH.md)):
1. Refused while the node mode is `offline`/`survival` ("Switch this computer to Online first.").
2. `POST https://ai.marin.cr/api/auth/device/code` `{ "clientInfo": { "deviceName": "ai-desktop on <hostname>", "userAgent": "ai-desktop/<version>", "tokenType": "pat" } }`.
3. Show the `userCode` large, with **Open the browser** (`shell.openExternal(verificationUriComplete)`; only URLs starting `https://ai.marin.cr/activate` are opened) and the text "On any phone or computer, open ai.marin.cr/activate and enter this code." / Spanish.
4. Poll `POST /api/auth/device/token {deviceCode}` every `interval` s; on `slow_down` add 5 s; stop on `expired_token`/`access_denied` (LAI-DSK-009) or after `expiresIn`.
5. On success (`credentialType: "pat"`, `accessToken: "pat_…"`, `expiresAt`, `tokenId`): store with `secret-store.ts` and set `settings.cloud.linked = true`.

**Token storage:** `safeStorage.encryptString(token)` → `cloud-token.bin`. If `safeStorage.getSelectedStorageBackend()` is `basic_text` (no keyring), the desktop asks for explicit consent ("This computer has no password keyring. The lab key would be stored with weak protection. Store it anyway?"); without consent the token is kept in memory only for this run (LAI-DSK-008). The PAT is never logged and never given to a renderer. **Disconnect** deletes the file, calls `DELETE https://ai.marin.cr/api/pat/{tokenId}` if reachable, and sets `linked: false`. A `401` from the cloud deletes the token and shows LAI-DSK-011.

**Lab tab** uses the cloud **API**, not the cloud web UI: Google sign-in refuses embedded browsers, and keeping cloud cookies out of the shell keeps the egress rule simple. The Lab view (`localai://app/lab/`) renders the same node cards as the cloud overview ([cloud-app.md](cloud-app.md) §3.11) with `packages/ui` components, and gets data only through `localaiDesktop.cloud.request()`. The main process forwards only these (method, path) pairs, adding `Authorization: Bearer pat_…`:

| Method | Path | Note |
|---|---|---|
| GET | `/api/localai/nodes`, `/api/localai/nodes/:nodeId`, `/api/localai/alerts` | |
| GET | `/api/localai/commands/:id`, `/api/localai/nodes/:nodeId/commands` | |
| POST | `/api/localai/nodes/:nodeId/commands` | only `type` ∈ {`status.refresh`, `model.load`, `model.unload`, `model.set_default`, `ark.sync`, `ark.verify`, `service.restart`} (the cloud still enforces RBAC) |
| GET | `/api/auth/me` | show who is linked |
Paths are matched against fixed patterns (`:nodeId` = `^ai-n[1-9][0-9]{0,3}$`, `:id` = uuid); anything else is refused in the main process without a network call. Each card has **Open in browser** (`https://ai.marin.cr/lab/nodes/<id>`) for everything else.

**When the cloud is unreachable** (network error, 5xx, or connectivity not `full`): the Lab shows the last data from `lab-cache.json` with a banner: "Can't reach the lab right now. Showing information from 14:05. This computer keeps working normally." / "No se puede conectar con el laboratorio. Mostrando información de las 14:05. Esta computadora sigue funcionando normalmente." Command buttons are disabled. Retries with backoff 10 s → 5 min while the tab is visible, never in the background. Local features are never affected (LAI-DSK-010).

### 4.9 Offline mode (strict no egress)
"Connected mode is active" = `settings.cloud.linked` **and** a token is held **and** the node's mode is `online`. In every other case (desktop not linked, or node `offline`/`survival`) the main-process guard allows loopback only, the Lab tab is hidden, and nothing in the app opens a network connection except to `127.0.0.1:7070` and the agent socket. A mode change to `offline`/`survival` closes cloud requests in flight within 1 s (abort controllers) and removes `ai.marin.cr` from the allowlist **before** the agent finishes its own transition. Updates are also off in this state, even if enabled.

### 4.10 Updates (off by default)
kvox-desktop's `updates.ts` pattern: `electron-updater` is not loaded until the user ticks **Check for updates** (menu ⋮). When on and in connected-or-online state, it checks GitHub Releases of `marinoscar/local-ai` for `desktop-v*` tags every 6 h, downloads in the background, verifies the SHA-512 from `latest-linux.yml`, and installs on quit (AppImage, and deb via electron-updater's deb updater, which asks for the admin password). Off again → timer stopped and the updater session blocked. Offline nodes update the desktop from the rebuild kit instead ([rebuild-kit.md](rebuild-kit.md)): `sudo apt install <root>/kit/desktop/ai-desktop_<version>_amd64.deb`.

### 4.11 Packaging and release
Workflow `.github/workflows/desktop-release.yml` on tags `desktop-v*` (and `workflow_dispatch` = dry run):
1. Check the tag equals `desktop-v` + `apps/desktop/package.json` version.
2. `ubuntu-latest`: `npm ci` (in `apps/desktop`), `node node_modules/electron/install.js`, typecheck, unit tests, `npm run build`, `node scripts/dist.mjs --linux --x64 --publish never`, `node scripts/packaged-app.mjs verify` (asar on, fuses as §3.3, no `.map`, no `node_modules/electron` in the asar).
3. Inspect the deb (`dpkg -I`, `dpkg-deb -c | grep apparmor`), install it, prepare a test copy, run the Playwright smoke + egress suites under `xvfb-run` against the fake agent.
4. Release job: collect `ai-desktop_<v>_amd64.deb`, `ai-desktop-<v>-x86_64.AppImage`, `latest-linux.yml`; write `SHA256SUMS`; sign it with **minisign** using the same release key as the agent (`MINISIGN_SECRET_KEY` secret; public key embedded in the agent, [security-model.md](../architecture/security-model.md) §4) → `SHA256SUMS.minisig`; create a **draft** GitHub release `desktop-v<v>`, then publish (pre-release if the tag contains `-`).
5. Shipping into the rebuild kit: the catalog's `kit-desktop` item ([catalog.md](catalog.md) §3.1, source type `github_release`, repo `marinoscar/local-ai`, tag `desktop-v<v>`, asset the deb, sha256 pinned) is bumped by a PR; ark sync then places the deb in `<root>/kit/desktop/` ([rebuild-kit.md](rebuild-kit.md)), and the offline installer installs it from there ([installer.md](installer.md)).
Signing note: Linux installers are not code-signed; integrity comes from `SHA256SUMS` + minisign (offline-verifiable with the agent's embedded key: `aictl verify-release <dir>`, [aictl.md](aictl.md)).

## 5. Failure handling and edge cases

| Situation | Behaviour | Code |
|---|---|---|
| User not in `localai-operators` | Error page with the `usermod` command; Portal still reachable via button "Open Survival Portal" | LAI-DSK-001 |
| In the group but needs re-login | Error page "log out and in once" | LAI-DSK-002 |
| Token file missing | Agent not installed or first start not finished; "Start agent" + installer hint | LAI-DSK-003 |
| Agent not running | Error page + **Start agent** | LAI-DSK-004 |
| pkexec cancelled / not authorised / agent won't start | Message + `systemctl status` excerpt + "Run `sudo aictl doctor`" | LAI-DSK-005 |
| Agent API major version unsupported (`/health.version` major ≠ supported) | Page "Update ai-desktop or the agent"; still offers the Portal | LAI-DSK-006 |
| Session refused after token re-read | Error page; fallback "Open in browser" with `aictl ui-code` instructions | LAI-DSK-007 |
| No keyring for the cloud token | Consent dialog; memory-only if refused | LAI-DSK-008 |
| Device code denied or expired | Dialog "The code expired. Try again." | LAI-DSK-009 |
| Cloud unreachable | Cached Lab view + banner | LAI-DSK-010 |
| Cloud token revoked/expired | Token deleted; "Connect to the lab again" | LAI-DSK-011 |
| Something tried to reach the internet in offline state | Blocked, logged (host only), counted | LAI-DSK-012 |
| No tray support | One-time notice how to enable the AppIndicator extension | LAI-DSK-013 |
| Autostart file can't be written | Toast with the path | LAI-DSK-014 |
| Chromium sandbox fails (AppArmor userns) | Starts nothing; `--diagnose` explains: reinstall the deb (it loads the profile) | LAI-DSK-015 |
| Update check/download fails | Status in the menu; nothing else changes | LAI-DSK-016 |
| Agent restarts (SSE drops) | Tray grey, reconnect with backoff; content view shows the agent's own reconnecting state | — |
| Second instance launched | Focuses the first; `--portal` is forwarded | — |
| Agent token rotated | Next 401 → re-read → new session, transparent | — |

## 6. Security considerations
- Fuses (§3.3): no `ELECTRON_RUN_AS_NODE`, no `NODE_OPTIONS`, no `--inspect`, asar-only, cookie encryption on.
- The agent token and cloud PAT live only in the main process; renderers see neither. The agent key is shown only on explicit Reveal/Copy, each audited by the agent.
- The preload surface is the §3.5 list, origin-checked per call. Agent pages get four harmless host functions.
- Egress is denied by default at two layers; only `ai.marin.cr:443` is ever added, and only in connected mode. The Lab renderer can't make network requests at all (its session allows only `localai:`).
- External URLs opened in the default browser are limited to `https://ai.marin.cr/*` (connected) and `https://ai.marin.cr/activate*` (linking).
- The agent's DNS-rebinding and Origin checks ([security-model.md](../architecture/security-model.md) §2) accept `localai://app` and same-origin; the desktop never sends any other Origin.
- `execFile` with fixed arguments for `systemctl`/`pkexec`; never a shell, never user input in arguments.
- A stolen desktop session gives at most what a `localai-operators` member already has; destructive actions still need the PIN.

## 7. Observability
- `~/.config/ai-desktop/logs/main.log`, JSON lines: `ts, level, msg, component (agent|tray|egress|cloud|updates|survival), code`. Egress blocks: `{component: "egress", host, blocked_total}`. Never paths with tokens, never keys, never PIN.
- `ai-desktop --diagnose` prints `{ version, electron, agent: {reachable, version, unit_state}, token: {readable, group_member, needs_relogin}, session: ok, tray: supported, safe_storage_backend, cloud: {linked, reachable}, egress_blocked_total, autostart, checks: [{code, severity, title, fix}] }`. The local UI's Doctor page and [runbooks/07-troubleshooting.md](../runbooks/07-troubleshooting.md) tell users to run it.

## 8. Doctor codes (`LAI-DSK`)

| Code | Sev | Meaning | Plain-language fix |
|---|---|---|---|
| LAI-DSK-001 | error | This user isn't allowed to control Local AI | Run `sudo usermod -aG localai-operators $USER`, then log out and log back in |
| LAI-DSK-002 | warning | You were added to the operators group but haven't logged in again | Log out and log back in once |
| LAI-DSK-003 | error | The agent's key file doesn't exist yet | Press "Start agent". If it keeps happening, reinstall with `install-node.sh` (runbook 05) |
| LAI-DSK-004 | error | The Local AI agent is not running | Press "Start agent" and enter the administrator password |
| LAI-DSK-005 | error | The agent couldn't be started | Open a terminal and run `sudo aictl doctor`; follow what it says |
| LAI-DSK-006 | error | ai-desktop and the agent are different versions that don't work together | Update both from the rebuild kit or with `sudo aictl update` |
| LAI-DSK-007 | error | The agent refused this app's login | Run `sudo aictl token rotate`, then restart ai-desktop. Meanwhile use a browser with `aictl ui-code` |
| LAI-DSK-008 | warning | No password keyring to protect the lab key | Accept weaker storage, or set up the GNOME keyring and connect again |
| LAI-DSK-009 | warning | The lab connection code expired or was refused | Press "Connect to the lab" again and enter the new code within 15 minutes |
| LAI-DSK-010 | info | Can't reach the lab | Nothing to fix here; this computer works normally. The Lab tab retries by itself |
| LAI-DSK-011 | warning | The lab connection was removed or expired | Press "Connect to the lab" again |
| LAI-DSK-012 | info | The app blocked a connection to the internet | Nothing to do: this is the privacy protection working |
| LAI-DSK-013 | warning | The system tray isn't available | Turn on "AppIndicator and KStatusNotifierItem Support" in Extensions, then restart ai-desktop |
| LAI-DSK-014 | warning | Couldn't turn on "Start at login" | Check that `~/.config/autostart` is writable, then try again |
| LAI-DSK-015 | critical | The app's security sandbox couldn't start | Reinstall ai-desktop from the .deb (`sudo apt install ./ai-desktop_<version>_amd64.deb`) |
| LAI-DSK-016 | warning | Couldn't check or download an update | Try later, or install the update from the rebuild kit |

## 9. Test plan

### Unit (Vitest, `tests/unit/`)
| Area | Cases |
|---|---|
| Group diagnosis | (listed, gid present) → readable path; (listed, gid absent) → 002; (not listed) → 001; ENOENT → 003 |
| Navigation | table of (view, URL) → allow / open child window / open external / block, including `localhost:7070` rewrite, `/chat/../api`, `javascript:`, `file:`, uppercase host, IPv6 loopback |
| Egress guard | allow/deny table per state (offline, connected, updates on); IPv4-mapped loopback; `ai.marin.cr:80` denied; DNS name trick `ai.marin.cr.evil.example` denied |
| Cloud proxy | allowed patterns pass; command type outside the list refused; path traversal `/api/localai/../users` refused; no network call on refusal |
| Device flow | pending → slow_down (+5 s) → success; expired; denied; tokenType `pat` handled via `credentialType` |
| Secret store | basic_text without consent → memory only; with consent → file 0600; decrypt failure → unlinked + 011 |
| Tray state | status × alerts × connectivity → colour |
| Autostart | file content for deb and AppImage; removal |
| Clipboard | clears only if unchanged after 60 s |
| CSP/protocol | traversal refused; CSP header present on every response |

### E2E (Playwright for Electron, `tests/e2e/`, against `fake-agent.ts` on 127.0.0.1:7070 and `fake-cloud.ts`)
- `smoke.spec.ts`: launch → session created (fake agent sees `POST /api/v1/session` with the token from a temp token file via `LOCALAI_DESKTOP_TOKEN_PATH`, honoured only when `!app.isPackaged` or in the test copy) → dashboard visible; Chat window opens at `/chat/`.
- `agent-down.spec.ts`: no fake agent → LAI-DSK-004 page; start fake agent and click Start agent (systemctl stubbed through `LOCALAI_DESKTOP_SYSTEMCTL` in test builds) → dashboard.
- `survival.spec.ts`: fake agent emits `mode.changed: survival` → window full screen on `/portal`; close → hidden; tray "Open" → full screen again.
- `egress.spec.ts`: (offline) agent page tries `fetch('https://example.com')`, `<img src=https://…>`, WebSocket → all blocked, counter = 3; main-process `fetch('https://ai.marin.cr')` throws `EgressBlockedError`; (connected, fake cloud via hosts mapping in test) Lab loads; switch fake agent to offline → next Lab request refused without a socket.
- `connected.spec.ts`: device flow against fake cloud → token stored; Lab cards render; fake cloud down → cached view + banner.
- `key.spec.ts`: Copy API key → clipboard holds the key; after the (test-shortened) timeout it is cleared.

### Manual on a real node (ai-n3)
1. `sudo apt install ./ai-desktop_<v>_amd64.deb` → app menu shows "Local AI"; `aa-status | grep ai-desktop` lists the profile.
2. Launch → dashboard in under 3 s; tray green.
3. `sudo gpasswd -d $USER localai-operators`, log out/in, launch → LAI-DSK-001 page with the command; re-add, log in → works.
4. `sudo systemctl stop localai-agent` → tray red within 5 s; click **Start agent** → password prompt → back to dashboard.
5. Tray → **Survival mode** → confirm → Portal full screen in Spanish if the OS locale is `es_*`.
6. Pull the network cable / `nmcli networking off`; `ai-desktop --diagnose | jq .egress_blocked_total` grows only if something tried egress; everything local works.
7. Connect to the lab with the device code from a phone → Lab tab shows ai-n1/ai-n2/ai-n3 cards.
8. Reboot → app starts hidden in the tray (or full-screen Portal if in survival).

## 10. Implementation breakdown

1. **DSK-01 Scaffold `apps/desktop`** — Scope: §3.1 layout, electron-vite, exact Electron pin, `electron-builder.yml` (§3.3), fuses, AppArmor profile, `packaged-app.mjs verify`, desktop CI workflow `.github/workflows/desktop-ci.yml` (`paths: apps/desktop/**, packages/ui/**`; typecheck, unit tests, build, `packaged-app.mjs verify`), and the minimal `packages/ui` package if it does not exist yet (§3.1). AC: `npm run dist` builds a deb and an AppImage; `verify` passes; the deb installs to `/opt/ai-desktop/`. Deps: none.
2. **DSK-02 Egress, navigation, CSP and protocol** — Scope: §3.6, §3.7, process switches, permission handler. AC: all unit tables of §9 for navigation/egress/CSP; `egress.spec.ts` offline cases pass. Deps: DSK-01.
3. **DSK-03 Agent client and error pages** — Scope: §4.1 start-up, token read, group diagnosis, session in `persist:agent`, 401 recovery, SSE subscription, error pages with en/es text, `--diagnose`. AC: `smoke.spec.ts`; diagnosis unit cases; token never appears in renderer memory (test queries `webContents.executeJavaScript` for it). Deps: DSK-02.
4. **DSK-04 Main window and child windows** — Scope: BaseWindow + top bar + content views, tab switching, Chat/Library/Maps windows, window bounds persistence, preload bridges with origin checks. AC: Chat opens `/chat/`; IPC from a non-allowed origin rejected (test). Deps: DSK-03.
5. **DSK-05 Start agent** — Scope: §4.2 systemctl → pkexec fallback, health polling, status excerpt. AC: `agent-down.spec.ts`; manual step 4. Deps: DSK-03.
6. **DSK-06 Tray and notifications** — Scope: §4.3, §4.4, mode switch, survival item, AppIndicator fallback. AC: tray colour unit table; mode switch calls the agent; manual tray check on GNOME. Deps: DSK-03.
7. **DSK-07 Autostart and `--hidden`** — Scope: §4.5, settings, AppImage path. AC: unit tests; manual reboot test. Deps: DSK-06.
8. **DSK-08 Copy key, connection info, PIN window** — Scope: §4.6 clipboard with timed clear, connection-info page with snippets, PIN prompt + grant use. AC: `key.spec.ts`; PIN never in logs (log capture test). Deps: DSK-04.
9. **DSK-09 Survival UX** — Scope: §4.7 full-screen Portal, collapsed strip, hide-not-quit, `--portal`, Portal without token. AC: `survival.spec.ts`. Deps: DSK-04, DSK-06.
10. **DSK-10 Connected mode: device link and token storage** — Scope: §4.8 link/unlink, safeStorage + consent, connected-state egress switch (§4.9). AC: device-flow unit cases; `connected.spec.ts` link part; offline switch aborts cloud requests within 1 s. Deps: DSK-02, DSK-04.
11. **DSK-11 Lab tab** — Scope: Lab view with `packages/ui` cards, allowlisted cloud proxy, cache + unreachable banner, Open in browser. AC: cloud proxy unit table; `connected.spec.ts` cached-view part. Deps: DSK-10, CLOUD-09.
12. **DSK-12 Release workflow and updates** — Scope: §4.11 `desktop-release.yml`, SHA256SUMS + minisign, packaged e2e in CI, catalog `kit-desktop` bump PR template, §4.10 opt-in updater. AC: a `desktop-v0.1.0-rc.1` tag produces a draft pre-release with deb, AppImage, `SHA256SUMS`, `SHA256SUMS.minisig`; `sha256sum -c` passes; updater makes zero requests when off (test). Deps: DSK-01, DSK-02, DSK-03, DSK-04, DSK-05, DSK-06, DSK-07, DSK-08, DSK-09.

## 11. Open questions

| Question | Proposed default |
|---|---|
| arm64 builds? | Not now (all nodes are amd64). Add an `arm64` deb leg when an arm64 node exists. |
| ~~Should the installer add a polkit rule so operators can start the agent without the admin password?~~ | Resolved (2026-11-02): yes, [installer.md](installer.md) §4.3 P5 installs it by default (`start`/`restart` of `localai-agent.service` only; `--no-polkit` skips it). The desktop works either way (§4.2). |
