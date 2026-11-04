// Main-process entry: app lifecycle, single instance, wiring (docs/specs/desktop.md section 3.1).
// DSK-01 scaffold: one hardened, blank window. Later items wire in the agent client, windows, tray and the guards.
// Nothing here may become the only way to reach a feature: the agent's local UI works in any browser at
// http://127.0.0.1:7070 (ADR-0004).
import { join } from 'node:path';
import { BrowserWindow, app } from 'electron';

export const MODULE = 'index' as const;

/**
 * What the scaffold window shows. In dev, the Vite server's topbar page. Packaged, a static placeholder: the
 * `grantFileProtocolExtraPrivileges` fuse is off (desktop.md section 3.3), so Electron no longer serves file:// URLs
 * out of app.asar, and the shell pages need the `localai://app` protocol that DSK-02 adds (protocol.ts).
 */
function shellPage(): string {
  const dev = process.env['ELECTRON_RENDERER_URL'];
  if (!app.isPackaged && dev) return `${dev}/topbar/index.html`;
  const html =
    '<!doctype html><meta charset="utf-8"><title>ai-desktop</title>' +
    '<body style="font-family:system-ui,sans-serif;margin:2rem"><h1>ai-desktop</h1>' +
    '<p>Scaffold build. The shell pages arrive with the next desktop items.</p>' +
    '<p>The local UI works in any browser at http://127.0.0.1:7070.</p></body>';
  return `data:text/html;charset=utf-8,${encodeURIComponent(html)}`;
}

function createWindow(): BrowserWindow {
  const win = new BrowserWindow({
    width: 1100,
    height: 760,
    title: 'ai-desktop',
    webPreferences: {
      preload: join(__dirname, '../preload/shell.js'),
      sandbox: true,
      contextIsolation: true,
      nodeIntegration: false,
      spellcheck: false,
    },
  });
  void win.loadURL(shellPage());
  return win;
}

export function start(): void {
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
  void app.whenReady().then(() => {
    createWindow();
  });
}

// Under vitest `electron` is a stub and there is no Electron runtime: importing this file must start nothing.
if (process.versions['electron']) start();
