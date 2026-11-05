// Main-process entry: app lifecycle, single instance, wiring (docs/specs/desktop.md section 3.1).
// DSK-02: the egress guards, the localai://app protocol, the navigation and permission policy are installed here,
// before any window exists. DSK-03: then the start-up sequence (startup.ts) decides what the one temporary window
// shows: an error page or the agent UI. DSK-04 replaces the window. Nothing here may become the only way to reach a
// feature: the agent's local UI works in any browser at http://127.0.0.1:7070 (ADR-0004).
import { execFile } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { BrowserWindow, Menu, Notification, Tray, app, clipboard, dialog, ipcMain, nativeImage, protocol, session, shell } from 'electron';
import { AgentFeed, createAgentClient, resolveTokenPath, testHooksAllowed } from './agent-client';
import { createAgentSupervisor, execFileRunner, resolveStartBinaries } from './agent-supervisor';
import { type LaunchArgs, parseArgs } from './args';
import { AutostartController, applyAutostart, autostartPath } from './autostart';
import { runDiagnoseMain } from './diagnose';
import { AGENT_HOST, AGENT_PORT, applyProcessSwitches, egressGuard, getEgressState, installEgressGuard, installMainProcessGuard, setEgressLogger, type GuardedSessions } from './egress-guard';
import { DIALOG_TEXT, ERROR_ACTION_CHANNEL, errorText, pick, resolveLang } from './i18n';
import { type LogEntry, type Logger, createFileLogger } from './log';
import { hardenedWebPreferences, installPermissionHandlers, installWebContentsHardening, registerView } from './navigation';
import { installLocalaiProtocol, registerLocalaiScheme, rendererRoot, shellUrl } from './protocol';
import { Notifier } from './notify';
import { configDir, readSettingsFile, updateSettings } from './settings';
import { type ActionDeps, type StartupResult, SessionRecovery, handleErrorAction, isErrorPageUrl, parseActionRequest, runStartup } from './startup';
import { enter as enterSurvival } from './survival';
import {
  type ElectronDialogApi,
  type ElectronTrayApi,
  type PinPrompt,
  type WindowControl,
  ModeController,
  SET_MODE_CHANNEL,
  TrayController,
  createElectronDialogs,
  createElectronNotificationPort,
  createElectronTrayPort,
  createModeApi,
  detectTray,
  installHideToTray,
  isShellPageUrl,
  parseModeTarget,
  readStatus,
  showTrayNoticeOnce,
  trayNoticeStore,
  trayUsable,
} from './tray';
import { createTemporaryHost } from './windows';

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

/** Exported for the start-up order test; DSK-04 replaces this window with the real main window. Hidden until the host shows it. */
export function createWindow(): BrowserWindow {
  // Section 3.6: both egress layers exist before any window does.
  if (!egressGuard.installed) throw new Error('refusing to create a window before the egress guard is installed');
  const win = new BrowserWindow({
    width: 1100,
    height: 760,
    title: 'ai-desktop',
    show: false,
    webPreferences: {
      ...hardenedWebPreferences(app.isPackaged),
      preload: join(__dirname, '../preload/shell.js'),
    },
  });
  registerView(win.webContents, 'shell');
  // The window's own page is the DSK-01 top bar placeholder until DSK-04. It must load something: a webContents that
  // never navigated does not answer the DevTools protocol, which hangs Playwright's launch (the agent UI covers it).
  void win.loadURL(shellPageUrl('topbar'));
  return win;
}

export interface BootContext {
  win: BrowserWindow;
  sessions: GuardedSessions;
  log: Logger;
  devOrigin: string | undefined;
  /** The command line (`--hidden`, `--portal`); `process.argv` when absent. */
  args?: LaunchArgs;
}

/** The start-up sequence and its wiring (desktop.md 4.1). Replaced by a stub in the start-up order test. */
export async function bootstrap(ctx: BootContext): Promise<void> {
  const { win, sessions, log } = ctx;
  const args = ctx.args ?? parseArgs(process.argv);
  const dir = configDir();
  const read = await readSettingsFile(dir);
  if (read.problem) log.log('warn', 'app', `settings.json could not be used (${read.problem}); defaults are in effect`);
  const lang = resolveLang(read.settings.lang, app.getLocale());

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
  // The tray decides `--hidden` (desktop.md 4.5): with no tray there would be no way back to a hidden window, so it shows.
  // `LOCALAI_DESKTOP_TRAY=unavailable|available` pretends there is no StatusNotifier watcher, or one (unpackaged runs and the e2e test copy only).
  const forced = testHooksAllowed(app.isPackaged, process.execPath) ? process.env['LOCALAI_DESKTOP_TRAY'] : undefined;
  const availability = forced === 'unavailable' || forced === 'available' ? forced : await detectTray();
  const startHidden = args.hidden && !args.portal && trayUsable(availability);
  const host = createTemporaryHost({
    window: win,
    isPackaged: app.isPackaged,
    preloadDir: join(__dirname, '../preload'),
    lang: () => lang,
    shellPageUrl,
    startHidden,
  });
  const feed = new AgentFeed(client);

  // The tray (DSK-06) lives from here on, before the agent is probed: grey until the first status (desktop.md 4.1).
  let tray: TrayController | null = null;
  const windows: WindowControl = {
    showMain: () => {
      if (win.isDestroyed()) return;
      if (win.isMinimized()) win.restore();
      win.show();
      win.focus();
    },
  };
  // DSK-08 provides the native PIN window; until then the tray cannot leave survival and says where to do it.
  const pin: PinPrompt | undefined = undefined;
  const modes = new ModeController({ api: createModeApi(client), ...(pin ? { pin } : {}), currentMode: () => tray?.mode ?? readStatus(feed.status).mode, log });
  const dialogs = createElectronDialogs(dialog as unknown as ElectronDialogApi);
  const notifier = new Notifier({ port: createElectronNotificationPort({ Notification }), lang: () => lang, clock: Date.now, onActivate: windows.showMain, log });

  // "Start at login" (desktop.md 4.5): applied at every start, toggled from the tray. A development run never writes the
  // entry (it would start /usr/bin/ai-desktop); the packaged app, the AppImage and the e2e test copy do.
  const autostart = new AutostartController({
    wanted: read.settings.autostart,
    apply: async (enabled) => (app.isPackaged ? applyAutostart(enabled, { log }) : { ok: true, action: 'skipped', path: autostartPath() }),
    persist: async (enabled) => {
      await updateSettings(dir, { autostart: enabled });
    },
    onFailure: (failure) => void notifier.notice('LAI-DSK-014', { path: failure.path }),
    log,
  });

  // The Portal full screen (`--portal`, also forwarded by a second launch). DSK-09 gives `enterSurvival` its full-screen body.
  const openPortal = async (): Promise<void> => {
    windows.showMain();
    await host.showAgentUi('/portal');
    await enterSurvival();
  };
  app.on('second-instance', (_event, argv) => {
    if (parseArgs(argv).portal) void openPortal().catch(() => undefined);
  });

  const start = async (): Promise<StartupResult['screen']> => {
    // A node in survival mode opens the Portal even when started with `--hidden` (desktop.md 4.5, 4.7). Until DSK-09 the
    // hook has no full-screen body, so what shows is the main window.
    const survival = {
      enter: async (): Promise<void> => {
        windows.showMain();
        await enterSurvival();
      },
    };
    const result = await runStartup({ client, supervisor, host, survival, log });
    log.log('info', 'agent', `start-up finished: ${result.screen}`, result.screen === 'error' ? { code: result.code } : {});
    if (result.screen === 'error') tray?.startupFailed(result.code === 'LAI-DSK-004' || result.code === 'LAI-DSK-005' ? 'down' : 'blocked');
    // The stream also runs after an error page: it is how the tray notices that the agent came back.
    feed.start();
    if (result.screen !== 'error') void feed.refresh();
    return result.screen;
  };

  // 401 recovery: the agent UI got a 401 on an API call (the token was rotated, or the agent restarted).
  const recovery = new SessionRecovery({ client, supervisor, host, log, active: () => host.current() === 'agent-ui' && host.agentPath() !== '/portal' });
  sessions.agent.webRequest.onCompleted({ urls: [`http://${AGENT_HOST}:${AGENT_PORT}/api/v1/*`] }, (details) => {
    if (details.statusCode === 401) void recovery.onUnauthorized(details.url).catch(() => undefined);
  });

  // The error page's buttons (the one IPC channel of this item; DSK-04 folds it into the full bridge).
  const actionDeps: ActionDeps = {
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
  ipcMain.handle(ERROR_ACTION_CHANNEL, async (event, payload: unknown) => {
    const request = parseActionRequest(payload);
    if (!request || !isErrorPageUrl(event.senderFrame?.url ?? '', ctx.devOrigin)) return { ok: false };
    return handleErrorAction(request, actionDeps);
  });

  // `setMode` of the shell bridge: shell pages only, a known target only. The page shows its own errors from `code`.
  ipcMain.handle(SET_MODE_CHANNEL, async (event, payload: unknown) => {
    const target = parseModeTarget(payload);
    if (target === null || !isShellPageUrl(event.senderFrame?.url ?? '', ctx.devOrigin)) return { ok: false };
    const outcome = await modes.setMode(target);
    return outcome.ok ? { ok: true } : { ok: false, code: outcome.code };
  });

  // The tray, when the desktop has one (desktop.md 4.3). No StatusNotifier watcher: a one-time LAI-DSK-013 notice, and
  // closing the window quits the app.
  if (trayUsable(availability)) {
    let quitting = false;
    app.on('before-quit', () => {
      quitting = true;
    });
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
      windows,
      dialogs,
      actions: {
        quit: () => app.quit(),
        autostart: { get: () => autostart.enabled, set: async (next) => void (await autostart.set(next)) },
        startAgent: async () => {
          const result = await handleErrorAction({ action: 'start-agent', code: 'LAI-DSK-004' }, actionDeps);
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
    installHideToTray(win, { trayActive: () => tray !== null, quitting: () => quitting });
  } else {
    log.log('warn', 'tray', 'no StatusNotifier watcher on the session bus; closing the window quits the app', { code: 'LAI-DSK-013' });
    void showTrayNoticeOnce({ store: trayNoticeStore(dir), notify: () => notifier.notice('LAI-DSK-013') });
  }

  await autostart.applyAtStart();
  const screen = await start();
  // `--portal` on the first launch: the start-up sequence already opened it when the node is in survival mode.
  if (args.portal && screen !== 'survival') await openPortal();
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
  if (parseArgs(process.argv).diagnose) {
    runDiagnoseMain();
    return;
  }

  if (!app.requestSingleInstanceLock()) {
    app.quit();
    return;
  }
  app.on('second-instance', () => {
    const [win] = BrowserWindow.getAllWindows();
    if (win) {
      if (win.isMinimized()) win.restore();
      // Hidden to the tray: a second launch brings it back.
      if (!win.isVisible()) win.show();
      win.focus();
    }
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

  void app.whenReady().then(() => {
    const sessions = installEgressGuard({ session, isPackaged: app.isPackaged, devRendererUrl: devRendererUrl() });
    for (const ses of [sessions.default, sessions.agent, sessions.lab, sessions.updater]) {
      installPermissionHandlers(ses, { dev, devOrigin });
    }
    installLocalaiProtocol([sessions.default, sessions.lab], rendererRoot(app.getAppPath()));
    // From here the log is a file (and still stderr): blocked connections are counted in main.log for `--diagnose`.
    const log = createFileLogger({ dir: join(configDir(), 'logs') });
    setEgressLogger((entry) => log.write(entry as unknown as LogEntry));
    const win = createWindow();
    void (options.bootstrap ?? bootstrap)({ win, sessions, log, devOrigin }).catch((error: unknown) => {
      log.log('error', 'app', `start-up failed: ${(error as Error).message}`);
    });
  });
}

// Under vitest `electron` is a stub and there is no Electron runtime: importing this file must start nothing.
if (process.versions['electron']) start();
