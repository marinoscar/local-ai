// Main-process entry: app lifecycle, single instance, wiring (docs/specs/desktop.md section 3.1).
// DSK-02: the egress guards, the localai://app protocol, the navigation and permission policy are installed here,
// before any window exists. The window itself is still the DSK-01 scaffold window; DSK-04 replaces it with the real
// main window. Nothing here may become the only way to reach a feature: the agent's local UI works in any browser at
// http://127.0.0.1:7070 (ADR-0004).
import { join } from 'node:path';
import { BrowserWindow, app, protocol, session } from 'electron';
import { applyProcessSwitches, egressGuard, getEgressState, installEgressGuard, installMainProcessGuard } from './egress-guard';
import { hardenedWebPreferences, installPermissionHandlers, installWebContentsHardening, registerView } from './navigation';
import { installLocalaiProtocol, registerLocalaiScheme, rendererRoot, shellUrl } from './protocol';

export const MODULE = 'index' as const;

/** Unpackaged builds only: the Vite dev server that electron-vite sets. A packaged app never reads it. */
function devRendererUrl(): string | undefined {
  return app.isPackaged ? undefined : process.env['ELECTRON_RENDERER_URL'];
}

/** What the scaffold window shows: the top bar page, from the Vite server in dev and from localai://app packaged. */
function shellPage(): string {
  const dev = devRendererUrl();
  return dev ? `${dev}/topbar/index.html` : shellUrl('topbar');
}

/** Exported for the start-up order test; DSK-04 replaces this window with the real main window. */
export function createWindow(): BrowserWindow {
  // Section 3.6: both egress layers exist before any window does.
  if (!egressGuard.installed) throw new Error('refusing to create a window before the egress guard is installed');
  const win = new BrowserWindow({
    width: 1100,
    height: 760,
    title: 'ai-desktop',
    webPreferences: {
      ...hardenedWebPreferences(app.isPackaged),
      preload: join(__dirname, '../preload/shell.js'),
    },
  });
  registerView(win.webContents, 'shell');
  void win.loadURL(shellPage());
  return win;
}

export function start(): void {
  // Before `ready`: Chromium's own background networking off, and the custom scheme registered as privileged.
  applyProcessSwitches(app);
  registerLocalaiScheme(protocol);
  // The main-process layer needs no Electron API, so it is up before `ready` and before the single-instance check.
  installMainProcessGuard();

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
    createWindow();
  });
}

// Under vitest `electron` is a stub and there is no Electron runtime: importing this file must start nothing.
if (process.versions['electron']) start();
