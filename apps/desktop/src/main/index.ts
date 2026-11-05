// Main-process entry: app lifecycle, single instance, wiring (docs/specs/desktop.md section 3.1).
// DSK-02: the egress guards, the localai://app protocol, the navigation and permission policy are installed here,
// before any window exists. DSK-03: then the start-up sequence (startup.ts) decides what the content view shows: an
// error page or the agent UI. DSK-04: the window manager (windows.ts) is that content host, and the IPC handler tables
// (ipc.ts) are registered here. Nothing here may become the only way to reach a feature: the agent's local UI works in
// any browser at http://127.0.0.1:7070 (ADR-0004).
import { execFile } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { Menu, app, clipboard, dialog, ipcMain, protocol, screen, session, shell } from 'electron';
import type { MenuItemConstructorOptions } from 'electron';
import { AgentFeed, createAgentClient, resolveTokenPath, testHooksAllowed } from './agent-client';
import { createAgentSupervisor, execFileRunner, resolveStartBinaries } from './agent-supervisor';
import { runDiagnoseMain } from './diagnose';
import { AGENT_HOST, AGENT_PORT, applyProcessSwitches, egressGuard, getEgressState, installEgressGuard, installMainProcessGuard, setEgressLogger, type GuardedSessions } from './egress-guard';
import { pick, resolveLang } from './i18n';
import { CHANNELS, type SettingsStore, type StartupPhase, buildDesktopStatus, createHostHandlers, createSettingsStore, createShellHandlers, isAllowedOrigin, registerHandlers } from './ipc';
import { type LogEntry, type Logger, createFileLogger } from './log';
import { ABOUT, NOT_AVAILABLE, buildMenuTemplate } from './menu';
import { installPermissionHandlers, installWebContentsHardening } from './navigation';
import { installLocalaiProtocol, registerLocalaiScheme, rendererRoot, shellUrl } from './protocol';
import { type Settings, type SettingsPatch, configDir, readSettingsFile } from './settings';
import { SessionRecovery, type ActionDeps, runStartup } from './startup';
import { enter as enterSurvival } from './survival';
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

  // -- status: what the top bar shows ------------------------------------------------------------------------------
  const cloud = (): CloudLinkState => ({ linked: settings.cloud.linked, tokenHeld: false, url: settings.cloud.url }); // DSK-10 holds the token
  const status = () => buildDesktopStatus({ version: app.getVersion(), phase, connection: feed.connection, agentStatus: feed.status, cloud: cloud(), tab: windows.tab() });
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
  ctx.onSettingsChanged((next) => {
    settings = next;
    lang = resolveLang(next.lang, app.getLocale());
    push();
  });

  // -- start-up ----------------------------------------------------------------------------------------------------
  const start = async (): Promise<void> => {
    phase = 'starting';
    push();
    const result = await runStartup({ client, supervisor, host: windows, survival: { enter: enterSurvival }, log });
    log.log('info', 'agent', `start-up finished: ${result.screen}`, result.screen === 'error' ? { code: result.code } : {});
    phase = result.screen === 'error' ? 'error' : 'ready';
    if (result.screen !== 'error') {
      feed.start();
      void feed.refresh();
    }
    push();
  };

  // 401 recovery: the agent UI got a 401 on an API call (the token was rotated, or the agent restarted).
  const recovery = new SessionRecovery({ client, supervisor, host: windows, log, active: () => windows.current() === 'agent-ui' && windows.agentPath() !== '/portal' });
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
    host: windows,
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
      ...createShellHandlers({ policy, version, lang: () => lang, status, openChild: open, showTab: (tab) => windows.showTab(tab), showMenu, settings: store, pinWindowId: () => null, errorActions, log }),
      ...createHostHandlers({ version, openChild: open }),
    },
    { policy, log },
  );

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
  app.on('second-instance', () => windows?.showMain());
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
