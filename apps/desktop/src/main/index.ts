// Main-process entry: app lifecycle, single instance, wiring (docs/specs/desktop.md section 3.1).
// DSK-02: the egress guards, the localai://app protocol, the navigation and permission policy are installed here,
// before any window exists. DSK-03: then the start-up sequence (startup.ts) decides what the one temporary window
// shows: an error page or the agent UI. DSK-04 replaces the window. Nothing here may become the only way to reach a
// feature: the agent's local UI works in any browser at http://127.0.0.1:7070 (ADR-0004).
import { execFile } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { BrowserWindow, app, clipboard, ipcMain, protocol, session, shell } from 'electron';
import { AgentFeed, createAgentClient, resolveTokenPath, testHooksAllowed } from './agent-client';
import { createAgentSupervisor, execFileRunner, resolveStartBinaries } from './agent-supervisor';
import { runDiagnoseMain } from './diagnose';
import { AGENT_HOST, AGENT_PORT, applyProcessSwitches, egressGuard, getEgressState, installEgressGuard, installMainProcessGuard, setEgressLogger, type GuardedSessions } from './egress-guard';
import { ERROR_ACTION_CHANNEL, resolveLang } from './i18n';
import { type LogEntry, type Logger, createFileLogger } from './log';
import { hardenedWebPreferences, installPermissionHandlers, installWebContentsHardening, registerView } from './navigation';
import { installLocalaiProtocol, registerLocalaiScheme, rendererRoot, shellUrl } from './protocol';
import { configDir, readSettingsFile } from './settings';
import { SessionRecovery, handleErrorAction, isErrorPageUrl, parseActionRequest, runStartup } from './startup';
import { enter as enterSurvival } from './survival';
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
}

/** The start-up sequence and its wiring (desktop.md 4.1). Replaced by a stub in the start-up order test. */
export async function bootstrap(ctx: BootContext): Promise<void> {
  const { win, sessions, log } = ctx;
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
  const host = createTemporaryHost({ window: win, isPackaged: app.isPackaged, preloadDir: join(__dirname, '../preload'), lang: () => lang, shellPageUrl });
  const feed = new AgentFeed(client);

  const start = async (): Promise<void> => {
    const result = await runStartup({ client, supervisor, host, survival: { enter: enterSurvival }, log });
    log.log('info', 'agent', `start-up finished: ${result.screen}`, result.screen === 'error' ? { code: result.code } : {});
    if (result.screen !== 'error') {
      feed.start();
      void feed.refresh();
    }
  };

  // 401 recovery: the agent UI got a 401 on an API call (the token was rotated, or the agent restarted).
  const recovery = new SessionRecovery({ client, supervisor, host, log, active: () => host.current() === 'agent-ui' && host.agentPath() !== '/portal' });
  sessions.agent.webRequest.onCompleted({ urls: [`http://${AGENT_HOST}:${AGENT_PORT}/api/v1/*`] }, (details) => {
    if (details.statusCode === 401) void recovery.onUnauthorized(details.url).catch(() => undefined);
  });

  // The error page's buttons (the one IPC channel of this item; DSK-04 folds it into the full bridge).
  ipcMain.handle(ERROR_ACTION_CHANNEL, async (event, payload: unknown) => {
    const request = parseActionRequest(payload);
    if (!request || !isErrorPageUrl(event.senderFrame?.url ?? '', ctx.devOrigin)) return { ok: false };
    return handleErrorAction(request, {
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
    });
  });

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
  app.on('second-instance', () => {
    const [win] = BrowserWindow.getAllWindows();
    if (win) {
      if (win.isMinimized()) win.restore();
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
