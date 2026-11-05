// Shared by the Playwright specs that drive the real app against tests/fixtures/fake-agent.ts.
//   unpackaged:  npm run build && xvfb-run -a npx playwright test
//   packaged:    node scripts/dist.mjs --linux --x64 && node scripts/packaged-app.mjs test-copy
//                LOCALAI_E2E_EXECUTABLE=release/test-copy/ai-desktop xvfb-run -a npx playwright test
// Needs a display (xvfb) and port 7070 free. As root (a container) Chromium needs --no-sandbox (set automatically).
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { type ElectronApplication, _electron as electron } from '@playwright/test';

export const appDir = resolve(__dirname, '..', '..');
export const executable = process.env['LOCALAI_E2E_EXECUTABLE'] ? resolve(process.env['LOCALAI_E2E_EXECUTABLE']) : undefined;
export const noSandbox = process.env['LOCALAI_E2E_NO_SANDBOX'] === '1' || process.getuid?.() === 0;

export interface Launched {
  app: ElectronApplication;
  /** A folder holding the token file and the app's config folder (XDG_CONFIG_HOME). Removed by `stop()`. */
  work: string;
  tokenPath: string;
  configHome: string;
  stderr(): string;
  stop(options?: { keep?: boolean }): Promise<void>;
}

/**
 * Starts the app with an isolated config folder and, when `token` is given, a token file the app is pointed at.
 * `work` reuses the folder of an earlier run (a restart: the settings file is still there); `stop()` then leaves it
 * alone, `stop({ keep: true })` does the same for the first run.
 */
export async function launch(options: { token?: string | null; env?: Record<string, string>; work?: string; settings?: unknown } = {}): Promise<Launched> {
  const work = options.work ?? (await mkdtemp(join(tmpdir(), 'ai-desktop-e2e-')));
  if (options.settings !== undefined) {
    await mkdir(join(work, 'config', 'ai-desktop'), { recursive: true });
    await writeFile(join(work, 'config', 'ai-desktop', 'settings.json'), JSON.stringify(options.settings));
  }
  const tokenPath = join(work, 'agent-token');
  const configHome = join(work, 'config');
  if (options.token) await writeFile(tokenPath, `${options.token}\n`, { mode: 0o600 });
  const common = ['--disable-gpu', ...(noSandbox ? ['--no-sandbox'] : [])];
  const env = { ...process.env, XDG_CONFIG_HOME: configHome, LOCALAI_DESKTOP_TOKEN_PATH: tokenPath, ...options.env } as Record<string, string>;
  const app = await electron.launch(executable ? { executablePath: executable, args: common, env } : { args: [...common, appDir], cwd: appDir, env });
  let stderr = '';
  app.process().stderr?.on('data', (chunk: Buffer) => {
    stderr += chunk.toString('utf8');
  });
  return {
    app,
    work,
    tokenPath,
    configHome,
    stderr: () => stderr,
    stop: async (stopOptions) => {
      await app.close().catch(() => undefined);
      if (!options.work && !stopOptions?.keep) await rm(work, { recursive: true, force: true });
    },
  };
}

export interface ContentsInfo {
  id: number;
  url: string;
}

export async function listContents(app: ElectronApplication): Promise<ContentsInfo[]> {
  return app.evaluate(({ webContents }) => webContents.getAllWebContents().map((c) => ({ id: c.id, url: c.getURL() })));
}

/** Polls until a webContents whose URL passes `match` exists. */
export async function waitForContents(app: ElectronApplication, match: (url: string) => boolean, timeoutMs = 20_000): Promise<ContentsInfo> {
  const end = Date.now() + timeoutMs;
  for (;;) {
    const found = (await listContents(app)).find((c) => match(c.url));
    if (found) return found;
    if (Date.now() > end) throw new Error(`no page matched; open pages: ${JSON.stringify(await listContents(app))}`);
    await new Promise((r) => setTimeout(r, 100));
  }
}

/** Runs JavaScript inside one webContents (main-process `executeJavaScript`) and returns the result. */
export async function evaluateIn<T>(app: ElectronApplication, id: number, code: string): Promise<T> {
  return app.evaluate(({ webContents }, args) => webContents.fromId(args.id)!.executeJavaScript(args.code) as Promise<unknown>, { id, code }) as Promise<T>;
}

export const isAgentUrl = (url: string): boolean => url.startsWith('http://127.0.0.1:7070/');
export const isErrorPage = (url: string): boolean => url.startsWith('localai://app/error/');

/** Polls an expression in a page until it is truthy. */
export async function pollIn(app: ElectronApplication, id: number, code: string, timeoutMs = 15_000): Promise<unknown> {
  const end = Date.now() + timeoutMs;
  let last: unknown;
  for (;;) {
    try {
      last = await evaluateIn<unknown>(app, id, code);
      if (last) return last;
    } catch {
      // the page is navigating
    }
    if (Date.now() > end) throw new Error(`condition never became true: ${code} (last: ${String(last)})`);
    await new Promise((r) => setTimeout(r, 100));
  }
}

export const isTopBar = (url: string): boolean => url.startsWith('localai://app/topbar/');

/** The Chat, Library and Maps windows currently open (BrowserWindows showing a page of the agent origin). */
export async function childUrls(app: ElectronApplication): Promise<string[]> {
  return app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().map((w) => w.webContents.getURL()).filter((u) => u.startsWith('http://127.0.0.1:7070/')));
}

/**
 * Opens a hidden test window with one of the app's preloads and loads `url` in it, to call the bridge from a page the
 * main process must refuse. Returns the window's webContents id.
 */
export async function openProbeWindow(app: ElectronApplication, options: { url: string; preload: 'shell' | 'agent'; partition?: string }): Promise<number> {
  return app.evaluate(
    async ({ BrowserWindow, app: electronApp }, o) => {
      const win = new BrowserWindow({
        show: false,
        webPreferences: {
          sandbox: true,
          contextIsolation: true,
          nodeIntegration: false,
          preload: `${electronApp.getAppPath()}/out/preload/${o.preload}.js`,
          ...(o.partition ? { partition: o.partition } : {}),
        },
      });
      await win.loadURL(o.url);
      return win.webContents.id;
    },
    options,
  );
}
