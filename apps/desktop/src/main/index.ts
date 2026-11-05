// Main-process entry: app lifecycle, single instance, wiring (docs/specs/desktop.md section 3.1).
// DSK-02: the egress guards, the localai://app protocol, the navigation and permission policy are installed here,
// before any window exists. DSK-03: then the start-up sequence (startup.ts) decides what the content view shows: an
// error page or the agent UI. DSK-04: the window manager (windows.ts) is that content host, and the IPC handler tables
// (ipc.ts) are registered here. Nothing here may become the only way to reach a feature: the agent's local UI works in
// any browser at http://127.0.0.1:7070 (ADR-0004).
import { execFile } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { Menu, Notification, Tray, app, clipboard, dialog, ipcMain, nativeImage, protocol, screen, session, shell } from 'electron';
import type { MenuItemConstructorOptions } from 'electron';
import { AgentFeed, createAgentClient, resolveTokenPath, testHooksAllowed } from './agent-client';
import { createAgentSupervisor, execFileRunner, resolveStartBinaries } from './agent-supervisor';
import { runDiagnoseMain } from './diagnose';
import { AGENT_HOST, AGENT_PORT, applyProcessSwitches, egressGuard, getEgressState, installEgressGuard, installMainProcessGuard, setEgressLogger, type GuardedSessions } from './egress-guard';
import { DIALOG_TEXT, errorText, pick, resolveLang } from './i18n';
import { CHANNELS, type SettingsStore, type StartupPhase, buildDesktopStatus, createHostHandlers, createSettingsStore, createShellHandlers, isAllowedOrigin, registerHandlers } from './ipc';
import { type LogEntry, type Logger, createFileLogger } from './log';
import { ABOUT, NOT_AVAILABLE, buildMenuTemplate } from './menu';
import { installPermissionHandlers, installWebContentsHardening } from './navigation';
import { Notifier } from './notify';
import { installLocalaiProtocol, registerLocalaiScheme, rendererRoot, shellUrl } from './protocol';
import { type Settings, type SettingsPatch, configDir, readSettingsFile } from './settings';
import { SessionRecovery, type ActionDeps, type ContentHost, handleErrorAction, runStartup } from './startup';
import { SurvivalController, bringForward as bringSurvivalForward, enter as enterSurvival, install as installSurvival } from './survival';
import {
  type ElectronDialogApi,
  type ElectronTrayApi,
  type PinPrompt,
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
}

/** Status changes that make the top bar re-read `/status` (mode, alerts, the cloud link). */
const REFRESH_EVENTS: ReadonlySet<string> = new Set(['mode.changed', 'alert.raised', 'alert.cleared', 'cloud.state']);

/** The start-up sequence and its wiring (desktop.md 4.1). Replaced by a stub in the start-up order test. */
export async function bootstrap(ctx: BootContext): Promise<void> {
  const { windows, sessions, log, store } = ctx;
  let settings = ctx.settings;
  let lang = resolveLang(settings.lang, app.getLocale());
  let phase: StartupPhase = 'starting';

  const client = createAgentClient({
    tokenPath: resolveTokenPath(process.env, testHooksAllowed(app.isPackaged, process.execPath)),
    sessionFetch: (input, init) => sessions.agent.fetch(input, init),
    log,
  });
  const supervisor = createAgentSupervisor({
    start: {
      binaries: resolveStartBinaries(process.env, testHooksAllowed(app.isPackaged, process.execPath)),
      exec: execFileRunner,
      health: async () => (await client.health({ tries: 1 })).reachable,
      log,
    },
  });
  const feed = new AgentFeed(client);

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

  // -- status: what the top bar shows ------------------------------------------------------------------------------
  const cloud = (): CloudLinkState => ({ linked: settings.cloud.linked, tokenHeld: false, url: settings.cloud.url }); // DSK-10 holds the token
  const status = () =>
    buildDesktopStatus({
      version: app.getVersion(),
      phase,
      connection: feed.connection,
      agentStatus: feed.status,
      cloud: cloud(),
      tab: windows.tab(),
      survival: survival.state((code) => errorText(code, lang).title),
    });
  const push = (): void => {
    windows.setLabAvailable(status().connected);
    windows.broadcastToShell(CHANNELS.statusChanged, status());
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
    push();
    if (patch.lang !== undefined) void survival.languageChanged().catch(() => undefined);
  });

  // -- the tray (DSK-06) lives from here on, before the agent is probed: grey until the first status (desktop.md 4.1) ---
  let tray: TrayController | null = null;
  // What the tray needs from the window manager: show the main window, open Chat, Library or Maps.
  // In survival the main window comes back full screen (a tray click, a notification click).
  const control: WindowControl = { showMain: () => survival.bringForward(), openWindow: (which) => void windows.openChild(which) };
  // DSK-08 provides the native PIN window; until then the tray cannot leave survival and says where to do it.
  const pin: PinPrompt | undefined = undefined;
  const modes = new ModeController({ api: createModeApi(client), ...(pin ? { pin } : {}), currentMode: () => tray?.mode ?? readStatus(feed.status).mode, log });
  const dialogs = createElectronDialogs(dialog as unknown as ElectronDialogApi);
  const notifier = new Notifier({ port: createElectronNotificationPort({ Notification }), lang: () => lang, clock: Date.now, onActivate: control.showMain, log });

  // -- start-up ----------------------------------------------------------------------------------------------------
  const start = async (): Promise<void> => {
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
    // `--portal` on a first launch: the Portal full screen, whatever the mode (in survival mode the start-up did it).
    if (result.screen === 'agent-ui' && process.argv.includes('--portal')) void enterSurvival({ reason: 'portal' }).catch(() => undefined);
    push();
  };

  // 401 recovery: the agent UI got a 401 on an API call (the token was rotated, or the agent restarted).
  const recovery = new SessionRecovery({ client, supervisor, host, log, active: () => windows.current() === 'agent-ui' && windows.agentPath() !== '/portal' });
  sessions.agent.webRequest.onCompleted({ urls: [`http://${AGENT_HOST}:${AGENT_PORT}/api/v1/*`] }, (details) => {
    if (details.statusCode === 401) void recovery.onUnauthorized(details.url).catch(() => undefined);
  });

  // -- the top bar's menu ------------------------------------------------------------------------------------------
  const notice = (kind: TopBarNotice['kind'], text: string): void => windows.sendToTopBar(CHANNELS.notice, { kind, text } satisfies TopBarNotice);
  const notAvailable = (): void => notice('not-available', pick(NOT_AVAILABLE, lang));
  const showMenu = (): void => {
    const template = buildMenuTemplate(
      { lang, linked: settings.cloud.linked, autostart: settings.autostart, checkForUpdates: settings.updates.checkForUpdates },
      {
        connectionInfo: notAvailable, // DSK-08
        connect: notAvailable, // DSK-10
        disconnect: notAvailable, // DSK-10
        setAutostart: (on) => void store.update({ autostart: on }).catch(() => undefined), // the autostart file is DSK-07's
        setUpdates: (on) => void store.update({ updates: { checkForUpdates: on } }).catch(() => undefined), // the updater is DSK-12's
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
    log,
  };
  const version = app.getVersion();
  const open = (name: Parameters<WindowManager['openChild']>[0]): void => void windows.openChild(name);
  // Registered before the first await: the top bar's first `status()` call must find its handler.
  registerHandlers(
    ipcMain,
    {
      ...createShellHandlers({ policy, version, lang: () => lang, status, openChild: open, showTab: (tab) => { if (!survival.active) windows.showTab(tab); }, setFullScreen: (flag) => survival.setFullScreen(flag), closePortal: () => survival.closePortal(), showMenu, settings: store, pinWindowId: () => null, errorActions, log, setMode: async (target) => {
        const outcome = await modes.setMode(target);
        return outcome.ok ? { ok: true } : { ok: false, code: outcome.code };
      } }),
      ...createHostHandlers({ version, openChild: open }),
    },
    { policy, log },
  );

  // The tray, when the desktop has one (desktop.md 4.3). No StatusNotifier watcher: a one-time LAI-DSK-013 notice, and
  // closing the window quits the app.
  // `LOCALAI_DESKTOP_TRAY=unavailable|available` pretends there is no StatusNotifier watcher, or one (unpackaged runs and the e2e test copy only).
  const forced = testHooksAllowed(app.isPackaged, process.execPath) ? process.env['LOCALAI_DESKTOP_TRAY'] : undefined;
  const availability = forced === 'unavailable' || forced === 'available' ? forced : await detectTray();
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
        startAgent: async () => {
          const result = await handleErrorAction({ action: 'start-agent', code: 'LAI-DSK-004' }, errorActions);
          // `busy`: a start is already running (the error page's button, or an earlier click): nothing to say.
          if (result.ok || result.busy === true) return;
          const copy = errorText('LAI-DSK-005', lang);
          const excerpt = result.statusExcerpt !== undefined ? `\n\n${result.statusExcerpt}` : '';
          await dialogs.alert({ title: copy.title, message: copy.message, detail: `${copy.fix} ${copy.command ?? ''}${excerpt}\n\n${copy.code}`, okLabel: pick(DIALOG_TEXT.ok, lang) });
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

  await start();
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
  if (process.argv.includes('--diagnose')) {
    runDiagnoseMain();
    return;
  }

  if (!app.requestSingleInstanceLock()) {
    app.quit();
    return;
  }
  // Set once the window exists; a second launch brings the (possibly hidden) main window forward.
  let windows: WindowManager | null = null;
  // A second launch brings the first forward; with `--portal` it opens the Portal full screen (desktop.md 3.9, 4.7).
  app.on('second-instance', (_event, argv) => {
    if (argv.includes('--portal') && bringSurvivalForward()) void enterSurvival({ reason: 'portal' }).catch(() => undefined);
    else if (!bringSurvivalForward()) windows?.showMain();
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
      const store = createSettingsStore(dir, (next, patch) => listeners.forEach((listener) => listener(next, patch)));
      windows = createWindows({
        savedBounds: read.settings.window.bounds,
        saveBounds: async (bounds) => void (await store.update({ window: { bounds: { ...bounds } } })),
        lang: () => resolveLang(read.settings.lang, app.getLocale()),
        devOrigin,
      });
      const manager = windows;
      app.on('before-quit', () => manager.prepareToQuit());
      await (options.bootstrap ?? bootstrap)({ windows: manager, sessions, log, devOrigin, settings: read.settings, store, onSettingsChanged: (listener) => void listeners.add(listener) });
    } catch (error: unknown) {
      log.log('error', 'app', `start-up failed: ${(error as Error).message}`);
    }
  });
}

// Under vitest `electron` is a stub and there is no Electron runtime: importing this file must start nothing.
if (process.versions['electron']) start();
