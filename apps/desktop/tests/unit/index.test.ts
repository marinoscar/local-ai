import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// A fake Electron that records the order of the calls that matter.
const calls: string[] = [];
const windows: Array<{ options: Record<string, unknown>; children: unknown[] }> = [];
const views: Array<{ options: Record<string, unknown>; url?: string }> = [];
const sessionHooks: Record<string, boolean> = {};

vi.mock('electron', () => {
  const makeSession = (name: string) => ({
    name,
    webRequest: { onBeforeRequest: () => calls.push(`webRequest:${name}`) },
    protocol: { handle: (scheme: string) => calls.push(`protocol.handle:${name}:${scheme}`) },
    setPermissionRequestHandler: () => calls.push(`permissions:${name}`),
    setPermissionCheckHandler: () => undefined,
    setDevicePermissionHandler: () => undefined,
  });
  const sessions = new Map<string, ReturnType<typeof makeSession>>();
  const fromPartition = (p: string) => {
    if (!sessions.has(p)) sessions.set(p, makeSession(p));
    return sessions.get(p)!;
  };
  class BaseWindow {
    children: unknown[] = [];
    contentView = { addChildView: (view: unknown) => this.children.push(view), removeChildView: () => undefined };
    constructor(options: Record<string, unknown>) {
      calls.push('new BaseWindow');
      windows.push({ options, children: this.children });
      sessionHooks['guardAtConstruction'] = calls.includes('webRequest:updater');
    }
    removeMenu(): void {
      calls.push('removeMenu');
    }
    getContentBounds() {
      return { x: 0, y: 0, width: 1280, height: 800 };
    }
    on(): void {}
  }
  class WebContentsView {
    record: { options: Record<string, unknown>; url?: string };
    webContents = {
      id: 1,
      loadURL: (url: string) => {
        this.record.url = url;
        return Promise.resolve();
      },
    };
    constructor(options: Record<string, unknown>) {
      calls.push('new WebContentsView');
      this.record = { options };
      views.push(this.record);
    }
    setBounds(): void {}
  }
  return {
    BaseWindow,
    BrowserWindow: class {},
    WebContentsView,
    screen: { getAllDisplays: () => [{ workArea: { x: 0, y: 0, width: 1920, height: 1080 } }], getPrimaryDisplay: () => ({ workArea: { x: 0, y: 0, width: 1920, height: 1080 } }) },
    app: {
      isPackaged: true,
      commandLine: { appendSwitch: (name: string) => calls.push(`switch:${name}`) },
      requestSingleInstanceLock: () => true,
      quit: () => calls.push('quit'),
      on: (name: string) => calls.push(`app.on:${name}`),
      whenReady: () => {
        calls.push('whenReady');
        return Promise.resolve();
      },
      getAppPath: () => '/opt/ai-desktop/resources/app.asar',
      getLocale: () => 'en-US',
    },
    protocol: { registerSchemesAsPrivileged: () => calls.push('registerSchemesAsPrivileged') },
    session: { defaultSession: makeSession('default'), fromPartition },
  };
});

let configHome: string;
beforeEach(async () => {
  // The window reads settings.json before it exists: point it at an empty folder, not the developer's own.
  configHome = await mkdtemp(join(tmpdir(), 'ai-desktop-index-'));
  vi.stubEnv('XDG_CONFIG_HOME', configHome);
});

afterEach(async () => {
  const { resetEgressGuardForTests } = await import('../../src/main/egress-guard');
  resetEgressGuardForTests();
  calls.length = 0;
  windows.length = 0;
  views.length = 0;
  vi.unstubAllEnvs();
  await rm(configHome, { recursive: true, force: true });
});

describe('start-up order (desktop.md 3.6, implementation note 1)', () => {
  it('sets the switches and the scheme before ready, installs every guard before the first window, then runs the start-up sequence', async () => {
    const { start } = await import('../../src/main/index');
    const bootstrap = vi.fn(() => {
      calls.push('bootstrap');
      return Promise.resolve();
    });
    start({ bootstrap });
    await vi.waitFor(() => expect(bootstrap).toHaveBeenCalledTimes(1));

    const at = (name: string) => calls.indexOf(name);
    expect(at('switch:disable-background-networking')).toBeGreaterThanOrEqual(0);
    expect(at('switch:disable-features')).toBeLessThan(at('whenReady'));
    expect(at('registerSchemesAsPrivileged')).toBeLessThan(at('whenReady'));
    expect(at('app.on:web-contents-created')).toBeLessThan(at('new BaseWindow'));
    for (const name of ['default', 'persist:agent', 'persist:lab', 'updater']) {
      expect(at(`webRequest:${name}`), name).toBeGreaterThanOrEqual(0);
      expect(at(`webRequest:${name}`), name).toBeLessThan(at('new BaseWindow'));
      expect(at(`permissions:${name}`), `permissions ${name}`).toBeLessThan(at('new BaseWindow'));
    }
    expect(at('protocol.handle:default:localai')).toBeLessThan(at('new BaseWindow'));
    expect(at('protocol.handle:persist:lab:localai')).toBeLessThan(at('new BaseWindow'));
    expect(calls.filter((c) => c === 'new BaseWindow')).toHaveLength(1);
    expect(sessionHooks['guardAtConstruction']).toBe(true);

    const { egressGuard } = await import('../../src/main/egress-guard');
    expect(egressGuard.installed).toBe(true);

    // The main window: hidden until the start-up sequence decides what it shows (desktop.md 4.1), default menu removed.
    expect(windows[0]!.options).toMatchObject({ show: false, width: 1280, height: 860, minWidth: 900, minHeight: 600, title: 'Local AI' });
    expect(calls).toContain('removeMenu');
    // Its only view at this point is the top bar, loaded at once: a webContents that never navigated hangs Playwright's launch.
    expect(views).toHaveLength(1);
    expect(views[0]!.url).toBe('localai://app/topbar/');
    expect(windows[0]!.children).toHaveLength(1);
    expect(views[0]!.options['webPreferences']).toMatchObject({
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webSecurity: true,
      spellcheck: false,
      devTools: false,
      preload: expect.stringMatching(/preload[\\/]shell\.js$/),
    });
    expect(views[0]!.options['webPreferences']).not.toHaveProperty('partition'); // the default session

    expect(at('bootstrap')).toBeGreaterThan(at('new BaseWindow'));
    const ctx = (bootstrap.mock.calls[0] as unknown as [{ windows: unknown; settings: { window: unknown } }])[0];
    expect(ctx.windows).toBeDefined();
    expect(ctx.settings.window).toEqual({ bounds: {} });
    expect(at('app.on:before-quit')).toBeGreaterThan(at('new BaseWindow'));
  });

  it('createWindows refuses to run before the guard is installed', async () => {
    const { createWindows } = await import('../../src/main/index');
    const { egressGuard } = await import('../../src/main/egress-guard');
    expect(egressGuard.installed).toBe(false);
    expect(() => createWindows({ savedBounds: {}, saveBounds: async () => undefined, lang: () => 'en', devOrigin: undefined })).toThrow(/before the egress guard/);
    expect(calls).not.toContain('new BaseWindow');
  });
});
