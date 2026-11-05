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
    startup.ts                 the start-up sequence, 401 recovery and error-page actions (DSK-03; no Electron import)
    log.ts                     main.log: JSON lines, rotated (DSK-03)
    windows.ts                 main window (BaseWindow + top bar view + content view), chat/library/maps windows, bounds (DSK-04)
    ipc.ts                     the whole IPC surface: origin check, handler tables, settings allowlist, status (DSK-04)
    menu.ts                    the top bar's native menu template (DSK-04)
    navigation.ts              will-navigate / window-open / redirect allowlists
    egress-guard.ts            session webRequest guard + main-process socket guard
    csp.ts                     CSP for localai:// responses
    protocol.ts                localai://app handler (path-traversal safe)
    tray.ts                    tray icon, status colour, menu, mode switch, hide to tray, tray detection
    notify.ts                  native notifications, one per event kind per 5 minutes (DSK-06)
    args.ts                    command line: --hidden, --portal, --diagnose (DSK-07; no Electron import)
    autostart.ts               ~/.config/autostart/ai-desktop.desktop (DSK-07)
    clipboard.ts               copy key + timed clear
    pin.ts                     PIN prompt window + /pin/verify
    survival.ts                full-screen Portal behaviour
    cloud-link.ts              device flow (RFC 8628), token storage
    cloud-api.ts               allowlisted cloud API proxy for the Lab view
    secret-store.ts            safeStorage wrapper + consent
    settings.ts                ~/.config/ai-desktop/settings.json (read/write, unknown keys kept: DSK-03)
    updates.ts                 opt-in electron-updater (off by default)
    i18n.ts                    en + es strings for main-process dialogs, tray and notifications
    diagnose.ts                `ai-desktop --diagnose` (JSON doctor report on stdout)
  src/preload/
    shell.ts                   bridge for shell pages: window.localaiDesktop
    agent.ts                   minimal bridge for agent pages: window.localaiDesktopHost
  src/shared/types.ts          the bridge types (DesktopStatus, ShellSettings, ...), types only
  src/renderer/                shell pages (React 19 + MUI, packages/ui components, no network assets)
    topbar/  error/  pin/  connection-info/  link/  lab/
  tests/unit/  tests/e2e/  tests/fixtures/fake-agent.ts  tests/fixtures/fake-cloud.ts
```
The desktop builds its shell pages with `packages/ui` components, consumed through `"@localai/ui": "file:../../packages/ui"` (desktop CI, `.github/workflows/desktop-ci.yml` on PRs touching `apps/desktop/**` or `packages/ui/**`, builds from the repo root, so `file:` resolves; the release workflow is `desktop-release.yml`, §4.11).

**Who creates `packages/ui`.** Whichever of AGENT-07, DSK-01 or CLOUD-01 lands first creates it if it is absent, with the minimum: `packages/ui/package.json` (`"name": "@localai/ui"`, private) and the brand source image `packages/ui/brand/icon.svg` (the input of `npm run icons`). CLOUD-11 owns the shared components (node card, status chips) that the desktop Lab view reuses. `packages/ui` already exists (created by the cloud and agent items), so DSK-01 adds only the `file:` dependency.

**Build setup (DSK-01).** The package is CommonJS (no `"type": "module"`) because a sandboxed preload must be CommonJS; electron-vite writes `out/main/index.js`, `out/preload/{shell,agent}.js` and `out/renderer/<page>/index.html`. Every dependency is a `devDependency` (renderer libraries are bundled by Vite), so `app.asar` holds only `out/**` and `package.json`, and `dependencies` stays empty. All versions are exact (no `^` or `~`). Electron 44 has no install hook: run `node node_modules/electron/install.js` after `npm ci` (desktop CI does).

**Shell pages and the `file://` fuse.** `grantFileProtocolExtraPrivileges` is off (section 3.3), and with it Electron no longer serves `file://` URLs out of `app.asar`. The shell pages are therefore served by the `localai://app` protocol (section 3.7, DSK-02), which reads `out/renderer` with `fs`; the packaged window loads `localai://app/topbar/`. In `npm run dev` (unpackaged, `ELECTRON_RENDERER_URL` set) the window loads the Vite dev server's `topbar` page instead, and the guards allow that one loopback origin (sections 3.6 and 3.7).

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
**As built (DSK-02).** The list is `PROCESS_SWITCHES` in `egress-guard.ts`; `index.ts` applies it with `applyProcessSwitches(app)` as the first thing `start()` does, before the scheme registration and `ready`.

### 3.4 Windows and views
**Main window:** a `BaseWindow` (1280×860, min 900×600, title "Local AI") with two `WebContentsView`s:
- **top bar** (48 px high, `localai://app/topbar/`, preload `shell.ts`, default session): tabs **This computer** | **Lab** (Lab only when connected), status pill, mode chip, buttons **Chat**, **Library**, **Maps**, **Copy API key**, menu ⋮ (Connection info, Connect to the lab / Disconnect, Start at login ✓, Check for updates ✓/✗, About, Quit).
- **content**: either agent UI `http://127.0.0.1:7070/` (partition `persist:agent`, preload `agent.ts`), the Lab view `localai://app/lab/` (partition `persist:lab`, preload `shell.ts`), or an error page `localai://app/error/?code=LAI-DSK-00N` (default session).

**Child windows** (`BrowserWindow`, partition `persist:agent`, preload none): Chat → `http://127.0.0.1:7070/chat/`, Library → `/library/`, Maps → `/maps/`. One window each (re-focus if open). Navigation is confined to the window's own path prefix plus `/api/` and asset paths of the agent origin (§3.6).

All webContents: `contextIsolation: true`, `sandbox: true`, `nodeIntegration: false`, `webSecurity: true`, `spellcheck: false`, `devTools: !app.isPackaged`. Permission requests (`setPermissionRequestHandler`) are denied except `clipboard-sanitized-write` for shell pages and `media` (microphone) for the agent origin's speech page.
**As built (DSK-02).** `hardenedWebPreferences(isPackaged)` in `navigation.ts` returns exactly the set above; a window adds only `preload` and `partition`. `installPermissionHandlers` sets the request, check and device handlers (Electron approves everything when none is set) on the default session, `persist:agent`, `persist:lab` and the updater session. `media` is granted only to `http://127.0.0.1:7070` and only when every requested kind is `audio` (never `video`, never an empty list); the spec does not name the speech page's path, so any page of the agent origin may ask for the microphone. `clipboard-sanitized-write` is granted to `localai://app` (and to the Vite dev origin in an unpackaged build). Devices (HID, USB, serial) are always refused.

**As built (DSK-04).** `windows.ts` has `createWindowManager()`, the one typed window API of the app; `index.ts` creates it after the guards and passes it to the start-up sequence as its `ContentHost` (`showError`, `showAgentUi`, `reloadAgentUi`). Everything under the `WindowFactory` is unit-tested with fakes; `createElectronWindowFactory()` is the thin real one (it also calls `removeMenu()`, so a packaged app has no default menu bar with Reload and DevTools).
- **Views.** The top bar view loads `localai://app/topbar/` at once (a webContents that never navigated hangs Playwright's launch, the DSK-03 lesson). The content slot holds one of three lazily created views, only one attached at a time: `agent` (`persist:agent`, preload `agent.js`, navigation view `content`), `lab` (`persist:lab`, `shell.js`, `shell`) and `error` (default session, `shell.js`, `shell`). Every view is registered with the navigation guard before it loads. Layout: top bar `{0, 0, w, 48}`, content `{0, 48, w, h - 48}`, redone on every `resize`. The window is created hidden; `showError`/`showAgentUi` reveal it (unless `startHidden`, for DSK-07's `--hidden`).
- **API for other items.** `showMain/hideMain/focusMain/isMainVisible`, `mainWindow()` (raw window, for DSK-09's full screen), `setTopBarHeight(px)` (DSK-09's 32 px strip), `setHideOnClose(true)` and `prepareToQuit()` (DSK-06: with a tray, closing hides; `index.ts` calls `prepareToQuit()` on `before-quit`; the default is that closing quits and closes the child windows), `tab()/showTab()/setLabAvailable()/onChange()`, `openChild(name)`, `childWindow(name)`, `closeChildren()`, `sendToTopBar()`, `broadcastToShell()`. A second launch calls `showMain()`.
- **Tabs.** `tab` is `local` or `lab`. `showTab('lab')` is ignored unless `setLabAvailable(true)`; `index.ts` sets that from `DesktopStatus.connected` after every status change (section 4.9: linked, a token held, mode `online`). Losing connected mode while on the Lab tab returns to `local`; an error page always returns to `local`. Until DSK-10 holds a token, `tokenHeld` is false, so the Lab tab never appears.
- **Child windows** are plain `BrowserWindow`s (partition `persist:agent`, no preload, 1000x780 or 1100x800, minimum 480x360), titled `Local AI: Chat|Library|Maps` in the chosen language (`Biblioteca`, `Mapas`), one each: a second request restores and focuses the open window. They are opened by the top bar buttons, by the host bridge, and by `setNavigationHooks({ openChild })` when the agent UI links to `/chat/`, `/library/` or `/maps/`. Closing the main window closes them.
- **Navigation hooks** are plugged by `wireNavigationHooks()`: `openChild` as above; `contentBlocked` sends `INTERNET_OFF_MESSAGE` (in the language in use) to the top bar, which shows it for 7 s; `openExternal` re-checks that the URL is `https://ai.marin.cr/...` (no credentials, default port) and that connected mode is active before it calls `shell.openExternal`.
- **Window bounds** (`settings.window.bounds`: `{ x, y, width, height, maximized }`, the normal rectangle, never saved while full screen): saved 500 ms after the last `resize`/`move`/`maximize`/`unmaximize` and on close (the close is held until the write finished, then repeated). `resolveBounds()` restores them: nothing saved gives 1280x860 (shrunk to a smaller display, no position); a rectangle with at least 100x50 px on a display that exists is moved and shrunk to lie wholly inside the display it overlaps most (a window spanning two displays is pulled onto one); otherwise it is centred on the primary display. The size is never below 900x600 unless the display itself is smaller. `maximized` re-maximises.
- **Tray wiring (DSK-06).** `index.ts` gives the tray `WindowControl = { showMain, openWindow }` backed by the window manager (so the Chat, Library and Maps tray items exist), and turns hide-to-tray on with `setHideOnClose(true)` when a tray exists. The manager's own close handler does the hiding, so that a real close (Quit, `before-quit`, no tray) first waits for the bounds save; `installHideToTray` in `tray.ts` is therefore not installed on the main window (its listener would cancel the manager's save-then-close). The `setMode` bridge call is a row of the handler table (origin-checked like the rest) that calls DSK-06's `ModeController`; an unknown target answers `{ ok: false }`.
- **The three dots** pop up a native menu from the main process (`menu.ts`: Connection info, Connect to the lab / Disconnect from the lab, Start at login, Check for updates, About, Quit; the two checkboxes are ticked from `settings.json`), because a 48 px view would clip an HTML menu. Start at login and Check for updates already write `settings.json` (`autostart`, `updates.checkForUpdates`); the autostart file (DSK-07) and the updater (DSK-12) subscribe through `onSettingsChanged`. Connection info and the cloud entries show "Not available yet." in the top bar until DSK-08 and DSK-10.
- **Top bar** (`src/renderer/topbar/`): tabs, status pill (Ready, Starting, Not working, Sign-in needed, Needs attention when the agent reports warning alerts or worse), mode chip, Chat, Library, Maps, Copy API key, the menu button. Below 1100 px the pill shows only its colour and Copy API key reads "API key", so everything fits at the 900 px minimum in both languages. It re-reads `status()` on every push; the main process pushes after agent events (`mode.changed`, `alert.raised`, `alert.cleared`, `cloud.state` re-read `/status` first), connection changes, tab changes and settings writes.
- **E2E display.** The e2e tests need a display of at least 1920x1080 (`xvfb-run -a -s "-screen 0 1920x1080x24"`; the default 640x480 is smaller than the minimum window).

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
**As built (DSK-03, folded in by DSK-04).** `errorAction(action, code)` is one member of the bridge: an `ipcRenderer.invoke` on the channel `localai:error-action`, accepted only from a frame whose URL is `localai://app/error/` (the Vite dev origin's `/error/` when unpackaged), only for an `action` that the `code`'s page has (`copy-command`, `logout`, `start-agent`, `open-portal`, `open-browser`), and never taking text or a URL from the page: **Copy command** copies the command the catalog (`src/main/i18n.ts`) holds for that code.
**As built (DSK-04).** `src/main/ipc.ts` holds the whole surface; `src/preload/shell.ts` and `agent.ts` expose it with `contextBridge`.
- **One table, one check.** Every channel is an entry of a handler table (`createShellHandlers`, `createHostHandlers`) with a kind (`invoke`, `send`, `sendSync`) and an origin (`shell` or `host`); `registerHandlers` wraps each entry so that nothing runs unless the sender is the **main frame** of an allowed origin: shell handlers accept only `localai://app` (plus the Vite dev origin when unpackaged), host handlers only `http://127.0.0.1:7070`; `localhost:7070`, `file:`, `data:`, `about:blank`, credentials in the URL, another port or scheme and `https://ai.marin.cr` are refused, and so is an iframe or a destroyed frame. A refused `invoke` rejects, a refused `send` does nothing, a refused `sendSync` answers `null`; the log line carries scheme and host only. Arguments are validated by the handler (`openWindow` takes only `chat|library|maps`, `showTab` only `local|lab`, `setMode` only the three modes); a page never supplies a path, URL or free text.
- **Additions to the interface** (the spec list above is unchanged): `errorAction(action, code)`, `showMenu()` (pops up the native menu, see 3.4) and `onNotice(cb)` (messages for the top bar: a blocked link, "Not available yet."). `lang()` and the host `version` are synchronous (`sendSync`), because the interface says so.
- **Implemented now:** `setMode(target)` (DSK-06: the same `ModeController` as the tray, so survival needs no PIN and leaving it asks for one; the handler is in the table, so it is origin-checked like the others), `status`, `onStatus`, `openWindow`, `showTab`, `settings.get/set`, `lang`, `errorAction`, `showMenu`, `onNotice`, and on the host bridge `isDesktop`, `version`, `openWindow`. `DesktopStatus` is `{ version, agent: starting|up|down|unauthorized, mode, connectivity, alerts, cloud: { linked, tokenHeld, url }, connected, tab }`, built from the agent feed (`/status`, the event stream's connection state), the start-up phase and `settings.json`; `alerts` counts alerts of severity warning, error or critical.
- **Stubs:** `startAgent` (DSK-05), `copyApiKey`, `connectionInfo`, `revealKey` (DSK-08), `cloud.beginLink/openVerification/unlink` (DSK-10), `cloud.request` (DSK-11), `pinSubmit` (DSK-08; refused from every webContents but the PIN window's, whose id the host passes as `pinWindowId`), and the host bridge's `copyApiKey` and `connectionInfo` all answer `{ ok: false, code: 'not_implemented' }`. Their TypeScript results are unions with that shape, and the top bar shows "Not available yet." for it. `cloud.linkState()` answers the real `CloudLinkState`.
- **Settings.** `settings.get()` returns `{ autostart, updates: { checkForUpdates }, lang, window: { bounds }, secrets: { allowPlaintext }, cloud: { url, linked } }` (known keys only; unknown keys of the file are kept but never shown). `settings.set(patch)` accepts exactly `autostart`, `updates.checkForUpdates`, `lang`, `window.bounds` and `secrets.allowPlaintext`; any other key (`cloud.*` included), a wrong type, an out-of-range rectangle or an unknown key inside a section rejects the whole patch (the promise rejects, nothing is written). Writes go through one queue (`createSettingsStore`) so a bounds save never interleaves with a page's change; `onSettingsChanged` listeners (language, status push, later autostart and updater) run after each write.
- **Sandboxed preloads.** Both preloads `require` only `electron`; the channel names are repeated in each file (a shared chunk cannot be loaded from a sandboxed preload) and `tests/unit/preload.test.ts` proves they equal `CHANNELS`. The agent bridge has exactly the five members; `tests/unit/preload.test.ts` and `windows.spec.ts` (in a real agent page) enumerate them.
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

**As built (DSK-02).** Where the code lives and the choices the table left open:
- `egress-guard.ts`: `setEgressState({ connected, updates })` (defaults `false`, `false`), `EgressBlockedError` (`name`, `code: 'LAI-DSK-012'`, `host`), `blockedTotal()`, `egressGuard.installed`. The main-process layer is a global undici dispatcher (npm `undici`, pinned), a wrapped `globalThis.fetch` (so a blocked `fetch` rejects with `EgressBlockedError` itself, not with `TypeError: fetch failed`) and a wrapped `net.Socket.prototype.connect`, which `http`, `https` and `tls` all go through. A blocked socket is destroyed with the error on the next tick; nothing is resolved or sent. Host matching: lowercase, one trailing dot stripped, then exact; IPv6 literals are canonicalised first, so `::ffff:127.0.0.1` and `::ffff:7f00:1` are loopback; non-dotted IPv4 spellings (`2130706433`, `127.1`) are not loopback and are denied. Remote hosts need TLS (`https:`/`wss:` for URLs), so `http://ai.marin.cr` is denied even when connected. Loopback is allowed on any port; the Unix socket allowlist is exactly `/run/localai/agent.sock`.
- `updates` is the *effective* allowance: the caller (DSK-12) passes "Check for updates is on **and** the node is not offline/survival" (section 4.9), the guard does not look at the node mode. Update hosts are reachable only from code running under `runAsUpdater(fn)` (an `AsyncLocalStorage` marker) and, for Chromium requests, only on the updater session.
- Session layer: `installEgressGuard` installs `webRequest.onBeforeRequest` (no URL filter, so every scheme is judged) on the default session, `persist:agent`, `persist:lab` and the updater session, whose partition is the in-memory `updater` (`getUpdaterSession()`; DSK-12 hands it to electron-updater). There is one `onBeforeRequest` listener per session: no later item may set another on these sessions. Loopback is allowed at this layer only as `http://127.0.0.1:7070` and `ws://127.0.0.1:7070` (so `localhost:7070` and `[::1]:7070` are cancelled; navigation rewrites the former). In an unpackaged build `devtools:` and the origin of `ELECTRON_RENDERER_URL` (loopback only) are also allowed; a packaged app never reads that variable. The updater session additionally opens the three GitHub hosts on 443 while `updates` is on. The cloud is never open to a renderer session, connected or not.
- Logging: until the file logger exists (a later item) a block is one JSON line on stderr, `{ ts, level: "error", msg, component: "egress", code: "LAI-DSK-012", host, blocked_total }`; `setEgressLogger` swaps it. For a request without a host the `host` field is the scheme (`file:`) and for a Unix socket it is `unix-socket`; never a path or query.
- `navigation.ts`: `decide(view, url, state)` is the table, with views `content`, `chat`, `library`, `maps` and `shell` (top bar, Lab, error, PIN, connection info, link). A `localhost:7070` URL decides like `127.0.0.1:7070` and `rewriteAgentUrl` gives the canonical URL, which the handler loads instead. Child windows may also reach `/assets/` and `/portal/assets/` (the agent's SPA bundle, agent.md section 3.11) besides their prefix and `/api/v1/`. URLs with credentials, `javascript:`, `file:`, `data:`, `blob:` and every other scheme are `block`. The handlers are installed once on `web-contents-created`; a window declares its view with `registerView(contents, view)`, and an unregistered webContents is treated as `shell` (strictest). `window.open` is always `deny`; when the destination is allowed it is loaded in the same webContents. `will-redirect` never opens a window or the browser, it only allows or cancels. `will-frame-navigate` confines subframes to the same rules (an iframe of the agent UI may move between agent paths). `will-attach-webview` is always cancelled. DSK-04 plugs `openChild`, `openExternal` and `contentBlocked` (top bar message, `INTERNET_OFF_MESSAGE`) in through `setNavigationHooks` (`wireNavigationHooks` in `windows.ts`, section 3.4). `https://ai.marin.cr/activate*` for linking (section 6) is not part of `decide`; DSK-10 opens it from the main process.

### 3.7 CSP for shell pages
`protocol.ts` adds to every `localai://app` response:
```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:;
  font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'
X-Content-Type-Options: nosniff
```
(`'unsafe-inline'` for styles only, because MUI/Emotion injects style tags.) Agent pages carry the agent's own CSP ([agent.md](agent.md) §3.11). The scheme is registered privileged: `{ standard: true, secure: true, supportFetchAPI: true }`; the handler resolves paths inside `out/renderer` only (normalise + `relative()` check, kvox-desktop's pattern) and falls back to the page's `index.html`.
**As built (DSK-02).** `protocol.ts` reads files with `fs` (which Electron's asar support makes work in the main process) and answers with a `Response`, because `file://` no longer serves from the asar. The host must be `app` (else 404). The path is decoded once; a `..` segment, NUL, backslash, dotfile or a path whose `path.relative()` leaves the root is refused with 403; outside an asar a symlink that leaves the folder is also refused. A folder serves its `index.html`; an unknown path without a file extension under a page falls back to that page's `index.html`; a missing asset, an unknown page and `/` are 404. Only GET and HEAD (405 otherwise). The CSP and `X-Content-Type-Options` are on every response, errors and refusals included (`csp.ts`). The handler is registered on the default session (top bar, error, PIN, ...) and on `persist:lab`, not on `persist:agent`. Pages loaded from the Vite dev server in an unpackaged build do not pass through it and carry no CSP.

### 3.8 Files
| Path | Contents |
|---|---|
| `~/.config/ai-desktop/settings.json` | `{ "schema": 1, "autostart": true, "updates": { "checkForUpdates": false }, "cloud": { "url": "https://ai.marin.cr", "linked": false }, "secrets": { "allowPlaintext": false }, "window": { "bounds": {…} }, "lang": "auto" }` (0600) |
| (same file) `notices` | `{ "LAI-DSK-013": true }` once the no-tray notice was shown (DSK-06). Not in the defaults: written only when needed, kept like any unknown key |
| `~/.config/ai-desktop/cloud-token.bin` | safeStorage-encrypted cloud PAT (0600); absent when not linked |
| `~/.config/ai-desktop/lab-cache.json` | last Lab data (node cards, no secrets), for the unreachable view (0600) |
| `~/.config/ai-desktop/logs/main.log` | main-process log, rotated at 5 MB × 3; never tokens or keys |
| `~/.config/autostart/ai-desktop.desktop` | §4.5 |

### 3.9 CLI flags
`ai-desktop` (normal), `ai-desktop --hidden` (start in tray; used by autostart), `ai-desktop --portal` (open the Portal full screen), `ai-desktop --diagnose` (print the §8 checks as JSON and exit, no window).

**As built (DSK-03), `--diagnose`.** Handled in `src/main/index.ts` before the single-instance lock, so it works while the app is running. It prints the object of section 7 as indented JSON on stdout and nothing else (Chromium's own noise goes to stderr), and **always exits 0**; a problem is a `checks` entry with a severity, never an exit code (an internal failure of the command itself exits 1). Electron on Linux cannot start its UI platform without a display, so with neither `DISPLAY` nor `WAYLAND_DISPLAY` the process runs a second copy of itself with `--ozone-platform=headless --disable-gpu` and relays its output and exit code; with a display it runs in place. No window is ever created. Keys: `agent.unit_state` is `systemctl is-active localai-agent.service` (`unknown` when systemd does not answer); `token.group_member` is whether `getent group localai-operators` lists the user (or it is their primary group), `needs_relogin` is true when the file is unreadable (`EACCES`), the user is listed and this login session lacks the group; `session` is `ok` when the agent accepts the token on `GET /status`, `refused` on a 401, `skipped` when it could not be asked (the command opens no session of its own); `tray` is `supported` when a StatusNotifier watcher owns its name on the session bus, `unsupported` when none does and `unknown` when neither `busctl` nor `gdbus` exists to ask (DSK-06); `cloud.reachable` is `null` (no connection is made; DSK-10); `egress_blocked_total` is the last `blocked_total` of an egress line in `main.log` (blocks since the running app started, 0 when none); `autostart` is `{ enabled, file_present }`; a check is `{ code, severity, title, fix }` in the language of `settings.lang` (the fix text ends with the command to run, when there is one). LAI-DSK-015 is raised only for an installed (packaged) app, when `kernel.apparmor_restrict_unprivileged_userns` is `1` and the profile is not in `/sys/kernel/security/apparmor/profiles`; that file is readable by root only, so for other users the check falls back to the absence of `/etc/apparmor.d/ai-desktop`.

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

**As built (DSK-03).** The sequence is `runStartup()` in `src/main/startup.ts` (no Electron import; the window, the survival hook, the client and the supervisor are injected); `src/main/index.ts` wires it. Choices the text left open:
- The window is created hidden and shown by the first screen. Since DSK-04 it is the main window of section 3.4: the agent UI and the Portal load in the `persist:agent` content view, error pages in the default-session error view, both under the always-loaded top bar view (the temporary one-window host of DSK-03 is gone).
- `health()` is 3 tries 1 s timeout 300 ms apart. An agent that answers with `starting: true` is polled once a second for up to 10 s before the token is read. A version whose major is not `SUPPORTED_API_MAJOR` (0) shows LAI-DSK-006 before the token file is touched; an unparsable version counts as a mismatch. A session request answered `503` is retried (5 × 1 s); an unreachable agent at that point is LAI-DSK-004, any other refusal is LAI-DSK-007.
- Reading the token: `EACCES` runs the group diagnosis; `ENOENT`, an empty file and a file that is not `lat_` + 43 characters of base64url are all LAI-DSK-003. Both unreadable and rotated tokens are re-read exactly once on a 401 from `POST /session`.
- `AgentClient` (`agent-client.ts`) keeps the token in a private field; `request(path)` adds the bearer and refuses any path that is not `/api/v1/...` on the agent origin, so the token cannot be sent elsewhere. It re-reads the token file on a 401 and repeats the request once if the token changed. `events()` is the SSE reader of section 3.10 of agent.md: `Last-Event-ID` on reconnect, backoff 1, 2, 4, ... 30 s, reset by a stream that opened, a stall limit of 45 s without a byte (the agent pings every 15 s), a 200 that is not `text/event-stream` counts as a failure. `AgentFeed` wraps it for the tray and top bar (DSK-06, DSK-04): it re-reads `/status` after a `resync` and after every reconnect and emits `event`, `connection` and `status` messages.
- 401 recovery (`SessionRecovery`): `webRequest.onCompleted` on `persist:agent` for `http://127.0.0.1:7070/api/v1/*`, status 401, except the login calls themselves (`/api/v1/session*`, `/api/v1/health`), and only while the agent view shows the dashboard (not the Portal, not an error page). At most one recovery per 30 s; one running at a time. A recovery that fails shows LAI-DSK-007, LAI-DSK-004 or the token-file page.
- The survival branch calls `survival.enter()` (a no-op until DSK-09) and loads `/portal`.

### 4.2 Token-file errors and "Start agent"
**Group diagnosis** on `EACCES`:
- user listed in `getent group localai-operators` but `process.getgroups()` lacks its gid → **LAI-DSK-002**: "You were added to the Local AI operators group, but you need to log out and log back in once." / "Te agregaron al grupo de operadores de Local AI, pero debes cerrar sesión y volver a entrar una vez." Button **Log out now** (`gnome-session-quit --logout` if present, else instructions).
- not listed → **LAI-DSK-001**: "This user is not allowed to control Local AI yet." Shows the exact fix: `sudo usermod -aG localai-operators $USER`, then log out and in. Button **Copy command**. Spanish text alongside.

**Start agent** (LAI-DSK-004 page and tray item):
1. `execFile('/usr/bin/systemctl', ['start', '--no-ask-password', 'localai-agent.service'])`: succeeds without a prompt when the installer's polkit rule `/etc/polkit-1/rules.d/50-localai-operators.rules` is present (installed by default, skipped with `--no-polkit`, [installer.md](installer.md) §4.3 P5).
2. If it fails with an authorization error: `execFile('/usr/bin/pkexec', ['/usr/bin/systemctl', 'start', 'localai-agent.service'])`: the system asks for the administrator password. Exit 126/127 = cancelled/not authorised → LAI-DSK-005.
3. Poll `/api/v1/health` every 1 s for 30 s, then continue the start-up sequence. If still down: show `systemctl status localai-agent.service --no-pager -n 20` output (read-only, no privilege) and the fix "Run `sudo aictl doctor` in a terminal".
No shell is ever used; arguments are fixed.
**As built (DSK-03).** The group diagnosis is `diagnoseGroupAccess()` in `agent-supervisor.ts`: `getent group localai-operators` (fixed path `/usr/bin/getent`, `execFile`) lists the user, or the user's primary gid is the group's, and `process.getgroups()` is compared with the group's gid. Result `not-member` is LAI-DSK-001, `needs-relogin` LAI-DSK-002, and `member-active` (the group is not the cause; the file's own mode is wrong) is shown as LAI-DSK-001 too, since its fix is the closest one. **Log out now** runs `/usr/bin/gnome-session-quit --logout` when it exists and otherwise the page says how to log out by hand. **Start agent** is on the LAI-DSK-003 and 004 pages (DSK-05 implements it, see the next note). **Open in browser** (LAI-DSK-007) opens `http://127.0.0.1:7070/` in the default browser, the one loopback exception to the external-URL list of section 6; the page tells the user to get a login code with `aictl ui-code`. **Open Survival Portal** loads `http://127.0.0.1:7070/portal` in the agent view, with no token.

**As built (DSK-05), Start agent.** `startAgent()` in `agent-supervisor.ts`, behind an injectable `exec` (production: `execFile` with fixed arguments, never a shell, nothing from the page or the user in any argument; tests: a fake). `createAgentSupervisor({ start })` wraps it with a single-flight lock: a second request while one runs does nothing and answers `{ ok: false, busy: true }`, and the page's button shows "Starting..." (disabled) meanwhile. Order and choices the text left open:
- A unit that `systemctl is-active` reports as `active` or `activating` is not started again; it goes straight to the wait of step 3.
- Step 1 runs with a 30 s limit. It counts as an authorization error only when it fails and its output says `Interactive authentication required`, `Access denied` or `authentication required`; exit status alone decides nothing. Any other failure (unit missing, job failed, `systemctl` absent, timeout) skips `pkexec` (a password cannot fix it) and ends in LAI-DSK-005 at once, without waiting 30 s.
- Step 2 (`pkexec /usr/bin/systemctl start localai-agent.service`, 120 s limit for the person to type the password): exit 126 and 127 (dismissed, not authorised), any other failure and a timeout all end in LAI-DSK-005.
- Step 3 asks `/api/v1/health` at once and then every second until 30 s have passed (31 asks, one try each). The first answer is success and the main process restarts the start-up sequence of section 4.1 (`retry`: token, session, status, events).
- Every LAI-DSK-005 carries the last 20 lines of `systemctl status localai-agent.service --no-pager` (read-only, no privilege), scrubbed of anything shaped like `lat_`/`lai_`/`nod_`/`pat_`/`pg_` tokens, 64-hex strings, colour codes and control characters and capped at 4000 characters. The error page shows it in a monospace block under the buttons, with both languages of the LAI-DSK-005 text, the command `sudo aictl doctor` and its **Copy command** button; **Start agent** becomes usable again.
- The button uses the existing error-action channel (`errorAction('start-agent', code)`, section 3.5), whose result is now `{ ok, code?, statusExcerpt?, busy? }`; DSK-04 folds it into the full bridge as it is. There is no separate `startAgent` bridge member. The tray item (DSK-06) calls `AgentSupervisor.start()` in the main process.
- Test hooks, honoured only by an unpackaged build and the e2e test copy (the same rule as `LOCALAI_DESKTOP_TOKEN_PATH`): `LOCALAI_DESKTOP_SYSTEMCTL` (absolute path of a stub that replaces `/usr/bin/systemctl` for `start`, `status` and `is-active`) and `LOCALAI_DESKTOP_PKEXEC` (a stub for `/usr/bin/pkexec`, which then receives the stub systemctl path as its first argument). With a systemctl stub and no pkexec stub, `pkexec` is never run (LAI-DSK-005), so a test cannot reach a real polkit.
- Tests: `tests/unit/agent-supervisor.test.ts` (every outcome with a fake exec and a fake clock, the no-shell source check) and `tests/e2e/agent-down.spec.ts` (stub scripts; start, authorization refused, unit missing).

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

**As built (DSK-06).** `src/main/tray.ts` holds everything about the tray except the Electron objects, which are handed in (`createElectronTrayPort`, `createElectronDialogs`, `createElectronNotificationPort`), so the whole behaviour is unit-tested with fakes; `index.ts` wires the real ones. Choices the text left open:
- **Colour** is the pure `trayColour(status, alerts, connectivity, noneSince, now)`. Precedence: grey (no status yet, or the stream dropped) > red (agent down, unusable by this app, or a critical alert) > orange (survival) > amber (a `warning` or `error` alert, a model that failed to load and has not loaded since, or `connectivity.value == "none"` for 30 minutes or more while online, from `status.connectivity.none_since`) > blue (offline) > green (online). A mode value this version does not know is grey, never green. A controller tick every 60 s re-reads `/status` (connectivity has no event) and redraws, so the 30-minute rule fires with no event.
- **Link states.** `starting` (grey), `up`, `down` (red; the agent does not answer `/health`) and `blocked` (red; the agent answers but the token file or login is refused, so no "Start agent"). An SSE drop or the `shutdown` event makes the tray grey at once and then asks `/health` (one try, 1 s) every 3 s until the stream is back: refused is red within about 4 s, `starting: true` stays grey. After a start-up that ended on an error page the same loop runs, and the event stream is now started in that case too, so the tray recovers by itself when the agent returns. A model that failed to load is forgotten when the link is lost (the router restarted).
- **Menu.** Rebuilt on every change of colour, tooltip or items, never otherwise (an open menu closes when it is replaced). The header is `● <tooltip>` plus ` · <model>` (first loaded model, else the default) while the agent is up. "Chat", "Library" and "Maps" are three items, shown once the window manager (DSK-04) provides `openWindow`; "Copy API key" and "Show connection info" (DSK-08), "Lab (ai.marin.cr)" (DSK-11, only while linked), "Start agent" (only while the agent is `down`) and the "Start at login" check (DSK-07) are likewise shown once their action is passed to `TrayController`. Each owner only has to supply its action in `index.ts`.
- **Mode.** `ModeController.setMode(target)` is the single path for the tray and for `window.localaiDesktop.setMode(target)` (IPC `localai:set-mode`, shell pages only: `localai://app`, or the Vite dev origin, and a target of `online`, `offline` or `survival`; it answers `{ ok, code? }`). Online and Offline send `POST /api/v1/mode {"target":..., "wait":false}` with the bearer token. Survival sends `POST /api/v1/mode/survival` with body `{}` and **no** `Authorization` header and no token read (`AgentClient.request(..., { anonymous: true })`), so it works with an unreadable token file; the tray asks one confirmation first, the bridge does not (the page asks). A refused request opens a native dialog with the agent's message, its fix and its code; a request that cannot reach the agent shows LAI-DSK-004 (token file: LAI-DSK-001 or 003).
- **Leaving survival** goes through the injectable `PinPrompt` (`requestGrant('leave-survival')`) that DSK-08 will implement: the grant is sent as `X-LocalAI-Pin-Grant` on that one request and dropped. When the mode is known to be survival the grant is asked for before the request (no refusal for the agent to audit); if the mode was stale and the agent answers `403 LAI-SEC-002` with `pin_required`, the tray asks and retries once. Until DSK-08 there is no prompt: the Online and Offline items are disabled in survival and the menu says "Leave survival in the Survival Portal" / "Para salir de supervivencia usa el Portal de Supervivencia".
- **No tray.** `detectTray()` asks the session bus whether `org.kde.StatusNotifierWatcher` (or `org.freedesktop.StatusNotifierWatcher`) has an owner, with `busctl --user` and else `gdbus`, fixed arguments. Absent (or no session bus): no tray is created, closing the window closes it and the app quits (`window-all-closed`), and LAI-DSK-013 is logged and shown once as a native notification (title, what happens, the fix), remembered in `settings.json` under `notices`. If neither tool exists the answer is `unknown`, which is treated as available. With a tray, closing the main window hides it (`installHideToTray`; a quit in progress is not turned into a hide), a second launch shows it again, and a click on the icon or "Open Local AI" shows and focuses it. A notification is used rather than a dialog so that a headless or test display is never blocked by a window nobody can click.
- **Test hooks** (an unpackaged run and the e2e test copy only, like the token path): `LOCALAI_DESKTOP_TRAY=available|unavailable` replaces the detection. A CI display has no StatusNotifier watcher, so `tray.spec.ts` forces both cases; the real tray widget itself (icons, menu rendering) is the manual test on a GNOME node.

### 4.4 Notifications
Native `Notification` from the main process (no web notifications): mode changed, model failed to load, sync done/failed, drive mounted/unmounted, alerts raised with severity ≥ warning, the "no internet for 30 minutes" suggestion ([modes.md](../architecture/modes.md) §4). Text in the UI language. At most one notification per event kind per 5 minutes.

**As built (DSK-06).** `src/main/notify.ts`; the Electron `Notification` is behind `NotificationPort` (shown from the main process, kept alive until it closes, a click brings the main window forward). The limiter is keyed by event kind and a suppressed notification does not extend the window, so a burst of ten `alert.raised` shows one, and the next is allowed five minutes after the first. Kinds are the event types, with three refinements so that a message is not hidden by an unrelated one: `mode.changed` is keyed by the new mode (an Offline switch followed by Survival still tells you about Survival), `sync.done` and a failed sync are separate kinds. Which events speak: `mode.changed`; `model.failed`; `sync.done` with state `done` (the text says so when `totals.failed > 0`) or `failed` (`cancelled` and `skipped` runs stay silent); `drive.mounted` and `drive.unmounted` (a `safe_to_unplug: false` removal says it was not ejected); `alert.raised` with severity `warning`, `error` or `critical`; and the 30-minute suggestion, sent once each time `/mode.suggest_survival` turns true (checked when `/status` shows connectivity `none` while online, and when the agent raises `LAI-NET-011`). An event the agent replays after a reconnect that is more than five minutes old is not announced. The text of an alert is the agent's own title and fix plus its code; everything else is in `i18n.ts` in both languages. `drill.done.notice` (the scheduled-drill notification of [drill.md](drill.md) section 4.5) is not wired here; it is a one-line addition to `describeEvent` once its owner asks for it.

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

**As built (DSK-07).** `src/main/args.ts` parses the command line in one place (`parseArgs(argv)`: exact `--hidden`, `--portal`, `--diagnose`; anything else, including `--hidden=1` and everything after a bare `--`, is ignored) and `src/main/autostart.ts` owns the file. Choices the text left open:
- **Where.** `$XDG_CONFIG_HOME/autostart/ai-desktop.desktop` when `XDG_CONFIG_HOME` is an absolute path, else `~/.config/autostart/ai-desktop.desktop`: the folder the desktop session reads, and the one `--diagnose` (`autostart.file_present`) and `settings.json` already follow. It is the only file the module writes or removes; the user's other autostart files are never read, edited or deleted.
- **Content.** Byte for byte the block above for the deb (`Exec=/usr/bin/ai-desktop --hidden`). For an AppImage, `Exec=<$APPIMAGE> --hidden`, where `$APPIMAGE` is used only if it is an absolute path without control characters (otherwise the deb launcher). A path with a space or another reserved character is double-quoted per the Desktop Entry specification (`"`, `` ` ``, `$` and `\` escaped, every backslash then doubled as a string value; `%` written `%%`), so `Exec="/home/person-a/My Apps/Local AI.AppImage" --hidden`.
- **Writing.** Temporary file next to the entry, mode forced to 0644, then a rename; the folder is created (0755) when missing. The entry is re-applied at **every** start, so an AppImage that moved fixes its `Exec` line; an entry that already has the right content is not rewritten (a read-only folder with a correct file is not an error). A development run (`!app.isPackaged`) never writes it, because it would start `/usr/bin/ai-desktop`; the packaged app, the AppImage and the e2e test copy do.
- **The setting.** `settings.autostart` defaults to true when `settings.json` or the key is absent (the first run), and from the first explicit choice the user's choice wins. The "Start at login" tray item (ticked state = what the file system says after the last attempt) calls `AutostartController.set`; the top bar menu calls the same controller, and a change made from a settings page (`settings.update({ autostart })` of the bridge) reaches it through `onSettingsChanged` (`follow`), which ignores the echo of the controller's own writes. Turning it **on** is remembered only when the file was written; turning it **off** is always remembered (a removal that failed is retried at every start). Clicks are serialised.
- **LAI-DSK-014.** When the entry cannot be written or removed: a native notification (title with the code, "Local AI could not write `<path>`...", the fix, in the UI language) and a `warn` line in `main.log` with the code and the errno, never the file content. The app keeps starting. `settings.autostart` is left as it was, so the next start tries again.
- **`--hidden`.** The main window is not shown when the tray is usable (`detectTray()`, or `LOCALAI_DESKTOP_TRAY` in test builds); pages still load behind it and the tray goes green as usual. With **no** tray there would be no way back to a hidden window, so it is shown (and closing it quits, as in §4.3). In survival mode the start-up sequence shows the main window and calls the survival hook (`survival.enter()`, DSK-09 gives it the full-screen body; until then the main window shows the Portal). The window manager gets `startHidden` (DSK-04's option) and reveals the window from the first `showMain()` (tray, second launch, survival).
- **`--portal`.** On the first launch (once start-up finished, unless survival already opened it) and on a second launch (`second-instance` forwards the second command line): show the main window, load the agent's `/portal`, call the survival hook. Full-screen and the collapsed strip are DSK-09's.

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
- `~/.config/ai-desktop/logs/main.log`, JSON lines: `ts, level, msg, component (app|agent|tray|egress|cloud|updates|survival), code`. **As built (DSK-03):** `log.ts`; the file (0600, folder 0700) rotates when the next line would pass 5 MB, keeping `main.log`, `main.log.1` and `main.log.2` (15 MB at most); each line is also written to stderr; every line is scrubbed of anything shaped like a token (`lat_`, `lai_`, `nod_`, `pat_`, `pg_`), a bearer header or a session cookie before it is written. `XDG_CONFIG_HOME` moves the whole `ai-desktop` folder (settings, logs), as it does Electron's own data folder. Egress blocks: `{component: "egress", host, blocked_total}`. Never paths with tokens, never keys, never PIN.
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
| Tray state | status × alerts × connectivity → colour (`tray-colour.test.ts`); menu per state and language, controller over a fake tray/timers, mode switch and PIN flow against the fake agent, tray detection, hide to tray, one-time notice (`tray-menu.test.ts`, `tray-controller.test.ts`, `tray-mode.test.ts`) |
| Notifications | rate limit with a fake clock, which events speak, both languages (`notify.test.ts`) |
| Autostart | file content for deb and AppImage (path with a space, `%`, `$`, backslash), atomic write and 0644, no rewrite when unchanged, removal that spares other files, LAI-DSK-014 on an unwritable folder, the tray toggle controller (`autostart.test.ts`); command-line table (`args.test.ts`) |
| Clipboard | clears only if unchanged after 60 s |
| CSP/protocol | traversal refused; CSP header present on every response |
| IPC origin check (**DSK-04**) | table of `localai://app`, `http://127.0.0.1:7070`, `http://localhost:7070`, `file:`, `data:`, `https://ai.marin.cr`, credentials, iframes, null frames for shell and host handlers; a refused call has no side effect |
| Settings patch (**DSK-04**) | allowlist (`autostart`, `updates.checkForUpdates`, `lang`, `window.bounds`, `secrets.allowPlaintext`); `cloud.*`, unknown keys, `__proto__`, wrong types, bad rectangles refused |
| Window bounds (**DSK-04**) | default, saved, display gone, partly off screen, too big, too small, two displays; debounce; no save in full screen; close saves first |
| Window manager (**DSK-04**) | layout on resize, one content view at a time, Lab tab gating, one child window each, hide-on-close, navigation hooks |
| Bridges (**DSK-04**) | shell members and channels, the agent bridge's exactly five names |

### E2E (Playwright for Electron, `tests/e2e/`, against `fake-agent.ts` on 127.0.0.1:7070 and `fake-cloud.ts`)
- `smoke.spec.ts` (**as built, DSK-03**: also the session cookie in `persist:agent`, the bearer SSE connection, the token absent from every renderer and from `main.log`, and the 401 recovery with its 30 s limit; `startup-errors.spec.ts`: LAI-DSK-003, 004, 006 and 007 pages from the real start-up sequence, **Copy command**, **Open Survival Portal**, and every error page in both languages; `diagnose.spec.ts`: `--diagnose` with no display; **DSK-04**: the top bar's status, mode and buttons with no Lab tab, Copy API key answering "not available yet", the Chat button opening one window at `/chat/` and a second click not opening another, navigation inside the Chat window to `/library/` blocked, an agent-UI link to `/library/` opening the Library window, a blocked internet link shown in the top bar; `windows.spec.ts`: the shell surface and the agent bridge's five members in real pages, IPC refused from a `data:` page and from an agent page holding the shell preload with no side effect, settings through the allowlist, the 900x600 layout in both languages, bounds saved, restored after a restart and clamped when the display is gone): launch → session created (fake agent sees `POST /api/v1/session` with the token from a temp token file via `LOCALAI_DESKTOP_TOKEN_PATH`, honoured only when `!app.isPackaged` or in the test copy) → dashboard visible; Chat window opens at `/chat/`.
- `agent-down.spec.ts` (**as built, DSK-05**: three scenarios, see the DSK-05 note in section 4.2): no fake agent → LAI-DSK-004 page; start fake agent and click Start agent (systemctl stubbed through `LOCALAI_DESKTOP_SYSTEMCTL` in test builds) → dashboard.
- `tray.spec.ts` (**as built, DSK-06**): the `setMode` bridge reaches the agent (bearer for online/offline, none for survival, an unknown target never), the no-tray path (LAI-DSK-013 logged and remembered once, closing the window quits) and the tray path (closing hides, Quit quits), both forced with `LOCALAI_DESKTOP_TRAY` because a CI display has no StatusNotifier watcher.
- `autostart.spec.ts` (**as built, DSK-07**, packaged test copy only): the first run writes the deb entry into the isolated `XDG_CONFIG_HOME`, `settings.autostart: false` removes a stale entry and spares the other files, an unwritable autostart folder is LAI-DSK-014 in `main.log` and the app still starts, `--hidden` with a tray shows no window until a second launch with `--portal` brings it forward on `/portal`, `--hidden` with no tray shows the window, and `--hidden` in survival mode shows it.
- `survival.spec.ts`: fake agent emits `mode.changed: survival` → window full screen on `/portal`; close → hidden; tray "Open" → full screen again.
- `egress.spec.ts` (**as built, DSK-02**: the offline cases run in `desktop-ci` against the packaged test copy under Xvfb; the connected cases arrive with DSK-10; the counter is read from the JSON log lines on stderr, the probe page is served by `fake-agent.ts` at `/egress-probe`): (offline) agent page tries `fetch('https://example.com')`, `<img src=https://…>`, WebSocket → all blocked, counter = 3; main-process `fetch('https://ai.marin.cr')` throws `EgressBlockedError`; (connected, fake cloud via hosts mapping in test) Lab loads; switch fake agent to offline → next Lab request refused without a socket.
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
