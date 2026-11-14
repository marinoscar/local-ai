// Main-process entry: app lifecycle, single instance, wiring (docs/specs/desktop.md section 3.1).
// DSK-02: the egress guards, the localai://app protocol, the navigation and permission policy are installed here,
// before any window exists. DSK-03: then the start-up sequence (startup.ts) decides what the content view shows: an
// error page or the agent UI. DSK-04: the window manager (windows.ts) is that content host, and the IPC handler tables
// (ipc.ts) are registered here. Nothing here may become the only way to reach a feature: the agent's local UI works in
// any browser at http://127.0.0.1:7070 (ADR-0004).
import { execFile } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { hostname } from 'node:os';
import { BrowserWindow, Menu, Notification, Tray, app, clipboard, dialog, ipcMain, nativeImage, protocol, safeStorage, screen, session, shell } from 'electron';
import type { MenuItemConstructorOptions } from 'electron';
import { AgentFeed, createAgentClient, resolveTokenPath, testHooksAllowed } from './agent-client';
import { createAgentSupervisor, execFileRunner, resolveStartBinaries } from './agent-supervisor';
import { type LaunchArgs, parseArgs } from './args';
import { AutostartController, applyAutostart, autostartPath } from './autostart';
import { runDiagnoseMain } from './diagnose';
import { createCloudProxy, createLabCacheStore, labNodeUrl } from './cloud-api';
import { CLOUD_TEXT, CODE_LINK_REMOVED, CODE_NO_KEYRING, CloudSession, LinkWindowController, abortableSleep, createCloudHttp, createLoopbackDispatcher, modeFromEvent, modeFromStatus, withCode, type LinkWindowFactory } from './cloud-link';
import { AGENT_HOST, AGENT_PORT, applyProcessSwitches, egressGuard, getEgressState, getUpdaterSession, installEgressGuard, installMainProcessGuard, patchEgressState, runAsUpdater, setEgressLogger, type GuardedSessions } from './egress-guard';
import { KeyCopier, copyNotification, createKeyActions, createKeyApi } from './clipboard';
import { DIALOG_TEXT, PAGE_STRINGS, START_FAILURE_TEXT, errorText, isStartFailureReason, pick, resolveLang } from './i18n';
import { LIBRARY_URL, createSystemActions } from './system-actions';
import { CHANNELS, type SettingsStore, type StartupPhase, buildDesktopStatus, createHostHandlers, createSettingsStore, createShellHandlers, isAllowedOrigin, registerHandlers } from './ipc';
import { type LogEntry, type Logger, createFileLogger } from './log';
import { ABOUT, buildMenuTemplate } from './menu';
import { hardenedWebPreferences, installPermissionHandlers, installWebContentsHardening, viewOf } from './navigation';
import { MusicProbe, installDownloadHandler } from './music';
import { Notifier } from './notify';
import { createEnsureActions, createEnsureApi } from './ensure';
import { PinService, createPinApi } from './pin';
import { installLocalaiProtocol, registerLocalaiScheme, rendererRoot, shellUrl } from './protocol';
import { createSecretStore, tokenFilePath } from './secret-store';
import { type Settings, type SettingsPatch, configDir, readSettingsFile } from './settings';
import { UpdateController, describeUpdateStatus, loadElectronUpdater } from './updates';
import { SessionRecovery, type ActionDeps, type ContentHost, type StartupResult, handleErrorAction, runStartup } from './startup';
import { SurvivalController, install as installSurvival } from './survival';
import {
  type ElectronDialogApi,
  type ElectronTrayApi,
  type PinPrompt,
  type TrayAvailability,
  type WindowControl,
  ModeController,
  TrayController,
  createElectronDialogs,
  createElectronNotificationPort,
  createElectronTrayPort,
  createModeApi,
  detectTray,
  readStatus,
  showTrayNoticeOnce,
  trayNoticeStore,
  trayUsable,
} from './tray';
import { type WindowManager, createElectronWindowFactory, createWindowManager, wireNavigationHooks } from './windows';
import type { CloudLinkState, TopBarNotice } from '../shared/types';

export const MODULE = 'index' as const;

/** Unpackaged builds only: the Vite dev server that electron-vite sets. A packaged app never reads it. */
function devRendererUrl(): string | undefined {
  return app.isPackaged ? undefined : process.env['ELECTRON_RENDERER_URL'];
}

/** A built shell page: from the Vite server in dev and from localai://app packaged. `query` is `?a=b` or empty. */
function shellPageUrl(page: string, query = ''): string {
  const dev = devRendererUrl();
  return dev ? `${dev}/${page}/index.html${query}` : shellUrl(page, query);
}

export interface WindowsContext {
  savedBounds: Settings['window']['bounds'];
  saveBounds(bounds: Record<string, unknown>): Promise<void>;
  lang(): ReturnType<typeof resolveLang>;
  devOrigin: string | undefined;
  /** `--hidden` with a usable tray (DSK-07): the main window stays hidden until something shows it. */
  startHidden?: boolean;
}

/** Exported for the start-up order test. Hidden until the start-up sequence decides what it shows (desktop.md 4.1). */
export function createWindows(ctx: WindowsContext): WindowManager {
  // Section 3.6: both egress layers exist before any window does.
  if (!egressGuard.installed) throw new Error('refusing to create a window before the egress guard is installed');
  return createWindowManager({
    factory: createElectronWindowFactory({ isPackaged: app.isPackaged, preloadDir: join(__dirname, '../preload') }),
    lang: ctx.lang,
    shellPageUrl,
    isShellUrl: (url) => isAllowedOrigin('shell', url, { devOrigin: ctx.devOrigin }),
    savedBounds: ctx.savedBounds,
    saveBounds: ctx.saveBounds,
    displays: () => ({ all: screen.getAllDisplays(), primary: screen.getPrimaryDisplay() }),
    quitApp: () => app.quit(),
    ...(ctx.startHidden ? { startHidden: true } : {}),
  });
}

export interface BootContext {
  windows: WindowManager;
  sessions: GuardedSessions;
  log: Logger;
  devOrigin: string | undefined;
  /** The settings as read before the window was created. */
  settings: Settings;
  store: SettingsStore;
  /** Runs after every settings write (DSK-07 hooks the autostart file here). */
  onSettingsChanged(listener: (next: Settings, patch: SettingsPatch) => void): void;
  /** The command line of this launch (`--hidden`, `--portal`). */
  args: LaunchArgs;
  /** StatusNotifier availability, asked once before the window was created (it decides `--hidden`). */
  trayAvailability: TrayAvailability;
  /** A second launch of the app (its command line); the main window is already brought forward. */
  onSecondLaunch(listener: (args: LaunchArgs) => void): void;
}

/** `LOCALAI_DESKTOP_TRAY=unavailable|available` pretends there is no StatusNotifier watcher, or one (unpackaged runs and the e2e test copy only). */
async function resolveTrayAvailability(): Promise<TrayAvailability> {
  const forced = testHooksAllowed(app.isPackaged, process.execPath) ? process.env['LOCALAI_DESKTOP_TRAY'] : undefined;
  return forced === 'unavailable' || forced === 'available' ? forced : detectTray();
}

/** Status changes that make the top bar re-read `/status` (mode, alerts, the cloud link). */
const REFRESH_EVENTS: ReadonlySet<string> = new Set(['mode.changed', 'alert.raised', 'alert.cleared', 'cloud.state']);
/** Events after which music may have become installed (issue #515): a finished sync, a loaded model, a finished workload switch. */
const MUSIC_REFRESH_EVENTS: ReadonlySet<string> = new Set(['sync.done', 'model.loaded', 'workload.done']);

/** The start-up sequence and its wiring (desktop.md 4.1). Replaced by a stub in the start-up order test. */
export async function bootstrap(ctx: BootContext): Promise<void> {
  const { windows, sessions, log, store, args, trayAvailability: availability } = ctx;
  let settings = ctx.settings;
  let lang = resolveLang(settings.lang, app.getLocale());
  let phase: StartupPhase = 'starting';

  const client = createAgentClient({
    tokenPath: resolveTokenPath(process.env, testHooksAllowed(app.isPackaged, process.execPath)),
    sessionFetch: (input, init) => sessions.agent.fetch(input, init),
    log,
  });
  // The same binaries (and the same test hooks) as "Start agent": a test build without a pkexec stub never runs pkexec,
  // and with a systemctl stub "Restart the computer" runs the stub, never the real reboot.
  const binaries = resolveStartBinaries(process.env, testHooksAllowed(app.isPackaged, process.execPath));
  const supervisor = createAgentSupervisor({
    start: {
      binaries,
      exec: execFileRunner,
      health: async () => (await client.health({ tries: 1 })).reachable,
      log,
    },
  });
  const systemActions = createSystemActions({ binaries, exec: execFileRunner, log });
  const feed = new AgentFeed(client);
  // Issue #515: is music installed here? Read again whenever /status is (start-up, reconnect, resync, mode and alert events).
  const music = new MusicProbe({ request: (path, options) => client.request(path, options), log });
  feed.subscribe((message) => {
    if (message.kind === 'status' || (message.kind === 'connection' && message.state === 'connected')) void music.refresh();
    // A sync run or an install pass may have just put the music model or runtime on disk.
    else if (message.kind === 'event' && MUSIC_REFRESH_EVENTS.has(message.event.type)) void music.refresh();
  });
  // A song from the Music window asks where to save it; other downloads from that window are refused.
  installDownloadHandler(sessions.agent, { viewOf, lang: () => lang, downloadsDir: () => app.getPath('downloads'), log });

  // -- survival UX (DSK-09): the full-screen Portal and the 32 px strip ---------------------------------------------
  const survival = new SurvivalController({
    windows,
    currentMode: () => readStatus(feed.status).mode,
    chosenLang: () => (settings.lang === 'en' || settings.lang === 'es' ? settings.lang : null),
    probeMode: async () => {
      const health = await client.health({ tries: 1, timeoutMs: 1000 });
      return health.reachable ? health.health.mode : null;
    },
    restart: () => void start(),
    log,
  });
  installSurvival(survival);
  // The content host of the start-up sequence and the error page: a Portal page that finished loading gets the language
  // the person picked in the strip.
  const host: ContentHost = {
    showError: (code, options) => windows.showError(code, options),
    showAgentUi: async (path) => {
      await windows.showAgentUi(path);
      if (path === '/portal') await survival.portalLoaded();
    },
    reloadAgentUi: () => windows.reloadAgentUi(),
  };

  // -- opt-in updates (DSK-12): nothing is loaded or requested until "Check for updates" is ticked and the node is online ---
  const updates = new UpdateController({
    packaged: app.isPackaged,
    currentVersion: app.getVersion(),
    loadUpdater: () => loadElectronUpdater({ session: getUpdaterSession(), log }),
    // The release feed, through the guarded updater session (the GitHub hosts are open there only while updates are on).
    fetchText: async (url) => {
      const response = await getUpdaterSession().fetch(url, { headers: { Accept: 'application/atom+xml' } });
      if (!response.ok) throw new Error(`HTTP ${response.status} from the release feed`);
      return response.text();
    },
    // `connected` belongs to DSK-10 (cloud-link.ts): change only `updates`.
    setEgress: (allowed) => patchEgressState({ updates: allowed }),
    runAsUpdater,
    log,
  });
  app.on('will-quit', () => updates.dispose());

  // -- status: what the top bar shows ------------------------------------------------------------------------------
  // -- the lab link (DSK-10): the only holder of the PAT. Its closures use `dialogs` and `notifier`, declared below; they run later. ---
  const testHooks = testHooksAllowed(app.isPackaged, process.execPath);
  // Test seam (the e2e fake cloud): `ai.marin.cr:443` is connected to a plain-HTTP server on this loopback port. Never in a shipped app.
  const fakeCloudPort = testHooks ? Number(process.env['LOCALAI_DESKTOP_TEST_CLOUD_PORT']) : Number.NaN;
  const secrets = createSecretStore({
    file: tokenFilePath(configDir()),
    safeStorage,
    allowPlaintext: () => settings.secrets.allowPlaintext,
    rememberConsent: async () => void (await store.update({ secrets: { allowPlaintext: true } })),
    askConsent: () =>
      dialogs.confirm({
        title: pick(CLOUD_TEXT.consentTitle, lang),
        message: withCode(pick(CLOUD_TEXT.consentMessage, lang), CODE_NO_KEYRING),
        confirmLabel: pick(CLOUD_TEXT.consentConfirm, lang),
        cancelLabel: pick(CLOUD_TEXT.consentCancel, lang),
      }),
    log,
  });
  const cloudSession = new CloudSession({
    http: createCloudHttp(Number.isInteger(fakeCloudPort) && fakeCloudPort > 0 && fakeCloudPort < 65536 ? { dispatcher: createLoopbackDispatcher(fakeCloudPort) } : {}),
    secrets,
    linked: () => settings.cloud.linked,
    setLinked: async (linked) => void (await store.update({ cloud: { linked } })),
    url: () => settings.cloud.url,
    setEgressConnected: (connected) => patchEgressState({ connected }),
    hostname: () => hostname(),
    version: app.getVersion(),
    openExternal: (url) => void shell.openExternal(url),
    now: Date.now,
    sleep: abortableSleep,
    onRemoved: () => {
      const text = `${pick(CLOUD_TEXT.removedTitle, lang)}. ${pick(CLOUD_TEXT.removedBody, lang)}`;
      windows.sendToTopBar(CHANNELS.notice, { kind: 'not-available', text: withCode(text, CODE_LINK_REMOVED) } satisfies TopBarNotice);
      notifier.send({ kind: 'cloud.removed', title: pick(CLOUD_TEXT.removedTitle, lang), body: withCode(pick(CLOUD_TEXT.removedBody, lang), CODE_LINK_REMOVED) });
    },
    log,
  });
  // The agent's mode closes the cloud: `mode.step` toward offline/survival does it the moment the transition STARTS,
  // before the agent has finished (desktop.md 4.9). Subscribed first, so nothing else sees the event before the egress switch.
  cloudSession.setModeSignal(modeFromStatus(feed.status));
  feed.subscribe((message) => {
    if (message.kind === 'status') cloudSession.setModeSignal(modeFromStatus(message.status));
    else if (message.kind === 'event') {
      const change = modeFromEvent(message.event);
      if (change) cloudSession.setModeSignal(change);
      // A step that failed may leave the node where it was: read the committed mode again.
      const data = message.event.data as { state?: unknown } | null;
      if (message.event.type === 'mode.step' && data?.state === 'failed') void feed.refresh();
    }
  });

  const linkWindowFactory: LinkWindowFactory = {
    create: (init) => {
      const win = new BrowserWindow({
        width: init.width,
        height: init.height,
        title: init.title,
        resizable: true,
        minimizable: false,
        webPreferences: { ...hardenedWebPreferences(app.isPackaged), preload: join(__dirname, '../preload/shell.js') },
      });
      win.removeMenu();
      void win.loadURL(shellPageUrl('link'));
      return {
        isDestroyed: () => win.isDestroyed(),
        isMinimized: () => win.isMinimized(),
        restore: () => win.restore(),
        show: () => win.show(),
        focus: () => win.focus(),
        close: () => win.close(),
        onClosed: (listener) => void win.on('closed', listener),
      };
    },
  };
  const linkWindow = new LinkWindowController({ factory: linkWindowFactory, title: () => pick(CLOUD_TEXT.linkWindowTitle, lang), onClosed: () => cloudSession.cancelLink() });

  const cloud = (): CloudLinkState => cloudSession.state();
  // `connected` is the session's own answer: it also knows that a transition toward offline/survival has started.
  const status = () => ({
    ...buildDesktopStatus({
      version: app.getVersion(),
      phase,
      connection: feed.connection,
      agentStatus: feed.status,
      cloud: cloud(),
      tab: windows.tab(),
      survival: survival.state((code) => errorText(code, lang).title),
      music: music.value,
    }),
    connected: cloudSession.connectedModeActive(),
  });
  const push = (): void => {
    const current = status();
    windows.setLabAvailable(current.connected);
    updates.apply({ setting: settings.updates.checkForUpdates, mode: current.mode, connected: current.connected });
    windows.broadcastToShell(CHANNELS.statusChanged, current);
  };
  let refreshTimer: ReturnType<typeof setTimeout> | null = null;
  feed.subscribe((message) => {
    if (message.kind === 'event' && REFRESH_EVENTS.has(message.event.type) && !refreshTimer) {
      refreshTimer = setTimeout(() => {
        refreshTimer = null;
        void feed.refresh();
      }, 150);
    }
    push();
  });
  windows.onChange(push);
  survival.onChange(push);
  feed.subscribe((message) => survival.handle(message));
  ctx.onSettingsChanged((next, patch) => {
    settings = next;
    lang = resolveLang(next.lang, app.getLocale());
    cloudSession.refresh();
    push();
    if (patch.lang !== undefined) void survival.languageChanged().catch(() => undefined);
  });
  cloudSession.onChange(push);
  music.onChange(push);

  // -- the Lab tab's cloud proxy and its cache (DSK-11) ---------------------------------------------------------------
  const labProxy = createCloudProxy({ session: cloudSession, cache: createLabCacheStore({ dir: configDir(), log }), log });
  // Disconnecting forgets the cached cards: they belong to the account that was linked.
  let wasLinked = settings.cloud.linked;
  cloudSession.onChange(() => {
    if (wasLinked && !settings.cloud.linked) void labProxy.clearCache();
    wasLinked = settings.cloud.linked;
  });

  // -- the tray (DSK-06) lives from here on, before the agent is probed: grey until the first status (desktop.md 4.1) ---
  let tray: TrayController | null = null;
  // The tray's "Lab (ai.marin.cr)" item follows connected mode (DSK-11).
  cloudSession.onChange(() => tray?.refresh());
  // What the tray needs from the window manager: show the main window, open Chat, Library or Maps.
  // In survival the main window comes back full screen (a tray click, a notification click).
  const control: WindowControl = { showMain: () => survival.bringForward(), openWindow: (which) => void windows.openChild(which) };
  // DSK-08: the native PIN window (leaving survival from the tray, Rotate key) and the API key (copy, connection info).
  const pin = new PinService({
    api: createPinApi(client),
    lang: () => lang,
    log,
    open: (purpose, language) => {
      const win = windows.openModal('pin', `?${new URLSearchParams({ purpose, lang: language }).toString()}`);
      return { id: win.webContents.id, focus: () => win.focus(), close: () => win.close(), isDestroyed: () => win.isDestroyed(), onClosed: (listener) => win.onClosed(listener) };
    },
  });
  const keyApi = createKeyApi(client);
  // The e2e test copy shortens the 60 s clipboard timeout; a packaged app ignores the variable.
  const clearMs = testHooksAllowed(app.isPackaged, process.execPath) ? Number(process.env['LOCALAI_DESKTOP_CLIPBOARD_CLEAR_MS']) : Number.NaN;
  const copier = new KeyCopier({ keys: keyApi, clipboard, log, ...(Number.isFinite(clearMs) && clearMs > 0 ? { clearAfterMs: clearMs } : {}) });
  const keys = createKeyActions({ keys: keyApi, copier, pin });
  // Whatever is still on the clipboard when the app quits would never be cleared by the timer.
  app.on('will-quit', (event) => {
    if (!copier.pending) return; // the second pass, after the clear: let the app quit
    event.preventDefault();
    // A clipboard that does not answer must not keep the app from quitting.
    void Promise.race([copier.clearIfHeld(), new Promise((resolve) => setTimeout(resolve, 1500))]).finally(() => app.quit());
  });
  const showConnectionInfo = (): void => void windows.openModal('connection-info');
  const showEnsure = (): void => void windows.openModal('ensure');
  // "Install what's missing" (issue #480, ADR-0010): the window's four calls; confirming compiling or a restart goes through the PIN.
  const ensure = createEnsureActions({ api: createEnsureApi(client), pin, lang: () => lang, log });
  const modes = new ModeController({ api: createModeApi(client), pin: pin satisfies PinPrompt, currentMode: () => tray?.mode ?? readStatus(feed.status).mode, log });
  const dialogs = createElectronDialogs(dialog as unknown as ElectronDialogApi);
  const notificationPort = createElectronNotificationPort({ Notification });
  const notifier = new Notifier({ port: notificationPort, lang: () => lang, clock: Date.now, onActivate: control.showMain, log });

  // -- "Start at login" (desktop.md 4.5) ---------------------------------------------------------------------------
  // The file follows `settings.autostart`: applied at every start, and again whenever a menu, the tray or a settings
  // page changes the setting. A development run never writes the entry (it would start /usr/bin/ai-desktop); the
  // packaged app, the AppImage and the e2e test copy do.
  const autostart = new AutostartController({
    wanted: settings.autostart,
    apply: async (enabled) => (app.isPackaged ? applyAutostart(enabled, { log }) : { ok: true, action: 'skipped', path: autostartPath() }),
    persist: async (enabled) => void (await store.update({ autostart: enabled })),
    onFailure: (failure) => void notifier.notice('LAI-DSK-014', { path: failure.path }),
    log,
  });
  ctx.onSettingsChanged((next, patch) => {
    if (typeof patch.autostart === 'boolean') void autostart.follow(next.autostart);
  });

  // A second launch (main.ts already showed the window): `--portal` opens the Portal full screen, anything else brings
  // the full-screen Portal back when the node is in survival mode (desktop.md 3.9, 4.7).
  ctx.onSecondLaunch((second) => {
    if (second.portal) void survival.enter({ reason: 'portal' }).catch(() => undefined);
    else survival.bringForward();
  });

  // -- start-up ----------------------------------------------------------------------------------------------------
  const start = async (): Promise<StartupResult['screen']> => {
    phase = 'starting';
    push();
    const result = await runStartup({
      client,
      supervisor,
      host,
      // The sequence loads `/portal` itself right after, so the hook only changes the window.
      survival: { enter: (options) => survival.enter({ reason: 'mode', load: false, ...(options?.banner !== undefined ? { banner: options.banner } : {}) }) },
      log,
    });
    log.log('info', 'agent', `start-up finished: ${result.screen}`, result.screen === 'error' ? { code: result.code } : {});
    phase = result.screen === 'error' ? 'error' : 'ready';
    if (result.screen === 'error') tray?.startupFailed(result.code === 'LAI-DSK-004' || result.code === 'LAI-DSK-005' ? 'down' : 'blocked');
    // The stream also runs after an error page: it is how the tray notices that the agent came back.
    feed.start();
    if (result.screen !== 'error') void feed.refresh();
    push();
    return result.screen;
  };

  // 401 recovery: the agent UI got a 401 on an API call (the token was rotated, or the agent restarted).
  const recovery = new SessionRecovery({ client, supervisor, host, log, active: () => windows.current() === 'agent-ui' && windows.agentPath() !== '/portal' });
  sessions.agent.webRequest.onCompleted({ urls: [`http://${AGENT_HOST}:${AGENT_PORT}/api/v1/*`] }, (details) => {
    if (details.statusCode === 401) void recovery.onUnauthorized(details.url).catch(() => undefined);
  });

  // -- the top bar's menu ------------------------------------------------------------------------------------------
  const notice = (kind: TopBarNotice['kind'], text: string): void => windows.sendToTopBar(CHANNELS.notice, { kind, text } satisfies TopBarNotice);
  const showMenu = (): void => {
    const template = buildMenuTemplate(
      { lang, linked: settings.cloud.linked, autostart: settings.autostart, checkForUpdates: settings.updates.checkForUpdates, updateLines: describeUpdateStatus(updates.status, lang) },
      {
        connectionInfo: showConnectionInfo,
        ensure: showEnsure,
        connect: () => {
          // Desktop.md 4.8 step 1: refused while the node is offline or in survival (or its mode is not known).
          if (!cloudSession.nodeOnline) notice('not-available', pick(CLOUD_TEXT.switchOnline, lang));
          else linkWindow.open();
        },
        disconnect: () => void cloudSession.unlink().catch(() => undefined),
        setAutostart: (on) => void autostart.set(on),
        setUpdates: (on) => void store.update({ updates: { checkForUpdates: on } }).catch(() => undefined), // the settings listener reaches `updates.apply` through push()
        about: () => {
          void dialog.showMessageBox({ type: 'info', title: pick(ABOUT.title, lang), message: pick(ABOUT.message, lang), detail: ABOUT.detail(app.getVersion(), lang), buttons: [pick(ABOUT.close, lang)] });
        },
        quit: () => app.quit(),
      },
    );
    Menu.buildFromTemplate(template as MenuItemConstructorOptions[]).popup();
  };

  // -- navigation hooks and IPC ------------------------------------------------------------------------------------
  wireNavigationHooks(windows, { openExternal: (url) => void shell.openExternal(url), lang: () => lang, connected: () => status().connected });
  const policy = { devOrigin: ctx.devOrigin };
  const errorActions: ActionDeps = {
    host,
    supervisor,
    clipboardWrite: (text) => clipboard.writeText(text),
    logout: () =>
      new Promise<boolean>((resolve) => {
        const quit = '/usr/bin/gnome-session-quit';
        if (!existsSync(quit)) return resolve(false);
        execFile(quit, ['--logout'], { timeout: 5000 }, (error) => resolve(!error));
      }),
    // The LAI-DSK-007 fallback: the agent's own UI in the default browser (loopback only; desktop.md 5).
    openAgentInBrowser: () => shell.openExternal(`http://${AGENT_HOST}:${AGENT_PORT}/`),
    retry: () => void start(),
    system: systemActions,
    confirmRestart: () =>
      dialogs.confirm({ title: pick(PAGE_STRINGS.rebootTitle, lang), message: pick(PAGE_STRINGS.rebootMessage, lang), confirmLabel: pick(PAGE_STRINGS.rebootYes, lang), cancelLabel: pick(PAGE_STRINGS.rebootNo, lang) }),
    // kiwix-serve keeps running without the agent (companion-services.md): a loopback probe, then the default browser.
    libraryReachable: async () => {
      try {
        const res = await fetch(LIBRARY_URL, { signal: AbortSignal.timeout(1500), redirect: 'manual' });
        await res.body?.cancel();
        return res.status < 500;
      } catch {
        return false;
      }
    },
    openLibraryInBrowser: () => shell.openExternal(LIBRARY_URL),
    sessionWorks: async () => (await client.recreateSession()).ok,
    log,
  };
  const version = app.getVersion();
  const open = (name: Parameters<WindowManager['openChild']>[0]): void => void windows.openChild(name);
  // Registered before the first await: the top bar's first `status()` call must find its handler.
  registerHandlers(
    ipcMain,
    {
      ...createShellHandlers({ policy, version, lang: () => lang, status, openChild: open, showTab: (tab) => { if (!survival.active) windows.showTab(tab); }, setFullScreen: (flag) => survival.setFullScreen(flag), closePortal: () => survival.closePortal(), showMenu, settings: store, cloud: cloudSession, lab: { request: (req) => labProxy.request(req), cached: () => labProxy.cached(), openNode: (nodeId) => { const url = labNodeUrl(nodeId); if (url !== null && cloudSession.connectedModeActive()) void shell.openExternal(url); } }, pinWindowId: () => pin.windowId(), keys, ensure, pin: { submit: (id, value) => pin.submit(id, value), cancel: (id) => pin.cancel(id) }, errorActions, log, setMode: async (target) => {
        const outcome = await modes.setMode(target);
        return outcome.ok ? { ok: true } : { ok: false, code: outcome.code };
      } }),
      ...createHostHandlers({ version, openChild: open, keys }),
    },
    { policy, log },
  );

  // The tray, when the desktop has one (desktop.md 4.3). No StatusNotifier watcher: a one-time LAI-DSK-013 notice, and
  // closing the window quits the app.
  if (trayUsable(availability)) {
    const iconDir = app.isPackaged ? join(process.resourcesPath, 'tray') : join(app.getAppPath(), 'build', 'linux', 'tray');
    const controller = new TrayController({
      port: createElectronTrayPort({ Tray, Menu, nativeImage } as unknown as ElectronTrayApi, {
        iconDir,
        onAction: (action) => void controller.invoke(action),
        onActivate: () => controller.activate(),
      }),
      lang: () => lang,
      clock: Date.now,
      notifier,
      modes,
      probe: async () => {
        const health = await client.health({ tries: 1, timeoutMs: 1000 });
        return !health.reachable ? 'down' : health.health.starting ? 'starting' : 'up';
      },
      refreshStatus: () => feed.refresh(),
      readMode: () => createModeApi(client).read(),
      windows: control,
      dialogs,
      actions: {
        quit: () => app.quit(),
        // DSK-11: "Lab (ai.marin.cr)" brings the window forward and shows the Lab tab (ignored unless connected mode is active).
        openLab: () => {
          control.showMain();
          if (!survival.active) windows.showTab('lab');
        },
        labConnected: () => cloudSession.connectedModeActive(),
        copyApiKey: async () => {
          const message = copyNotification(await copier.copy(), lang);
          if (notificationPort.supported()) notificationPort.show(message, control.showMain);
        },
        showConnectionInfo,
        autostart: { get: () => autostart.enabled, set: async (next) => void (await autostart.set(next)) },
        startAgent: async () => {
          const result = await handleErrorAction({ action: 'start-agent', code: 'LAI-DSK-004' }, errorActions);
          // `busy`: a start is already running (the error page's button, or an earlier click): nothing to say.
          if (result.ok || result.busy === true) return;
          // Plain words first (what failed, the next step); the excerpt and the command are for whoever repairs it.
          const copy = errorText('LAI-DSK-005', lang);
          const why = isStartFailureReason(result.reason) ? `${pick(START_FAILURE_TEXT[result.reason].what, lang)} ${pick(START_FAILURE_TEXT[result.reason].next, lang)}\n\n` : '';
          const excerpt = result.statusExcerpt !== undefined ? `\n\n${result.statusExcerpt}` : '';
          await dialogs.alert({ title: copy.title, message: copy.message, detail: `${why}${copy.fix}${excerpt}\n\n${copy.command ?? ''}\n${copy.code}`, okLabel: pick(DIALOG_TEXT.ok, lang) });
        },
      },
      log,
    });
    tray = controller;
    feed.subscribe((message) => controller.handle(message));
    controller.start();
    // With a tray, closing the main window hides it (the window manager does it; Quit and `before-quit` really close).
    windows.setHideOnClose(true);
  } else {
    log.log('warn', 'tray', 'no StatusNotifier watcher on the session bus; closing the window quits the app', { code: 'LAI-DSK-013' });
    void showTrayNoticeOnce({ store: trayNoticeStore(configDir()), notify: () => notifier.notice('LAI-DSK-013') });
  }

  await autostart.applyAtStart();
  // The stored token is read back before the first status push: a lost or unreadable one is LAI-DSK-011 and "unlinked".
  await cloudSession.start();
  const screen = await start();
  // `--portal` on the first launch: the start-up sequence already opened it when the node is in survival mode.
  if (args.portal && screen !== 'survival') await survival.enter({ reason: 'portal' });
  // The e2e test copy opens the connection info at once: a native menu item cannot be clicked from Playwright.
  if (testHooksAllowed(app.isPackaged, process.execPath) && process.env['LOCALAI_DESKTOP_TEST_OPEN'] === 'connection-info') showConnectionInfo();
  if (testHooksAllowed(app.isPackaged, process.execPath) && process.env['LOCALAI_DESKTOP_TEST_OPEN'] === 'ensure') showEnsure();
}

export interface StartOptions {
  /** Test seam: replaces the start-up sequence. */
  bootstrap?: (ctx: BootContext) => Promise<void>;
}

export function start(options: StartOptions = {}): void {
  // Before `ready`: Chromium's own background networking off, and the custom scheme registered as privileged.
  applyProcessSwitches(app);
  registerLocalaiScheme(protocol);
  // The main-process layer needs no Electron API, so it is up before `ready` and before the single-instance check.
  installMainProcessGuard();

  // `ai-desktop --diagnose`: a JSON report on stdout, no window, no single-instance lock (desktop.md 3.9).
  const args = parseArgs(process.argv);
  if (args.diagnose) {
    runDiagnoseMain();
    return;
  }

  if (!app.requestSingleInstanceLock()) {
    app.quit();
    return;
  }
  // Set once the window exists; a second launch brings the (possibly hidden) main window forward and forwards its flags (`--portal`).
  let windows: WindowManager | null = null;
  const secondLaunch = new Set<(second: LaunchArgs) => void>();
  app.on('second-instance', (_event, argv) => {
    windows?.showMain();
    const second = parseArgs(argv);
    secondLaunch.forEach((listener) => listener(second));
  });
  app.on('window-all-closed', () => app.quit());

  const dev = !app.isPackaged;
  const devOrigin = (() => {
    try {
      const url = devRendererUrl();
      return url ? new URL(url).origin : undefined;
    } catch {
      return undefined;
    }
  })();
  // Every webContents created from now on gets the navigation, window-open and webview policy.
  installWebContentsHardening(app, { getState: () => ({ connected: getEgressState().connected, dev, devOrigin }) });

  void app.whenReady().then(async () => {
    const sessions = installEgressGuard({ session, isPackaged: app.isPackaged, devRendererUrl: devRendererUrl() });
    for (const ses of [sessions.default, sessions.agent, sessions.lab, sessions.updater]) {
      installPermissionHandlers(ses, { dev, devOrigin });
    }
    installLocalaiProtocol([sessions.default, sessions.lab], rendererRoot(app.getAppPath()));
    // From here the log is a file (and still stderr): blocked connections are counted in main.log for `--diagnose`.
    const log = createFileLogger({ dir: join(configDir(), 'logs') });
    setEgressLogger((entry) => log.write(entry as unknown as LogEntry));
    try {
      const dir = configDir();
      const read = await readSettingsFile(dir);
      if (read.problem) log.log('warn', 'app', `settings.json could not be used (${read.problem}); defaults are in effect`);
      const listeners = new Set<(next: Settings, patch: SettingsPatch) => void>();
      // The tray decides `--hidden` (desktop.md 4.5): with no tray there would be no way back to a hidden window, so it shows.
      const trayAvailability = await resolveTrayAvailability();
      const store = createSettingsStore(dir, (next, patch) => listeners.forEach((listener) => listener(next, patch)));
      windows = createWindows({
        savedBounds: read.settings.window.bounds,
        saveBounds: async (bounds) => void (await store.update({ window: { bounds: { ...bounds } } })),
        lang: () => resolveLang(read.settings.lang, app.getLocale()),
        devOrigin,
        startHidden: args.hidden && !args.portal && trayUsable(trayAvailability),
      });
      const manager = windows;
      app.on('before-quit', () => manager.prepareToQuit());
      await (options.bootstrap ?? bootstrap)({ windows: manager, sessions, log, devOrigin, settings: read.settings, store, onSettingsChanged: (listener) => void listeners.add(listener), args, trayAvailability, onSecondLaunch: (listener) => void secondLaunch.add(listener) });
    } catch (error: unknown) {
      log.log('error', 'app', `start-up failed: ${(error as Error).message}`);
    }
  });
}

// Under vitest `electron` is a stub and there is no Electron runtime: importing this file must start nothing.
if (process.versions['electron']) start();
