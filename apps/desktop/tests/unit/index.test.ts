import { afterEach, describe, expect, it, vi } from 'vitest';

// A fake Electron that records the order of the calls that matter.
const calls: string[] = [];
const windows: Array<{ options: Record<string, unknown>; url?: string }> = [];
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
  class BrowserWindow {
    webContents = {};
    constructor(options: Record<string, unknown>) {
      calls.push('new BrowserWindow');
      windows.push({ options });
      sessionHooks['guardAtConstruction'] = calls.includes('webRequest:updater');
    }
    loadURL(url: string): Promise<void> {
      windows[windows.length - 1]!.url = url;
      return Promise.resolve();
    }
    static getAllWindows(): unknown[] {
      return [];
    }
  }
  return {
    BrowserWindow,
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
    },
    protocol: { registerSchemesAsPrivileged: () => calls.push('registerSchemesAsPrivileged') },
    session: { defaultSession: makeSession('default'), fromPartition },
  };
});

afterEach(async () => {
  const { resetEgressGuardForTests } = await import('../../src/main/egress-guard');
  resetEgressGuardForTests();
  calls.length = 0;
  windows.length = 0;
});

describe('start-up order (desktop.md 3.6, implementation note 1)', () => {
  it('sets the switches and the scheme before ready, installs every guard before the first window, then runs the start-up sequence', async () => {
    const { start } = await import('../../src/main/index');
    const bootstrap = vi.fn(() => {
      calls.push('bootstrap');
      return Promise.resolve();
    });
    start({ bootstrap });
    await Promise.resolve();
    await Promise.resolve();

    const at = (name: string) => calls.indexOf(name);
    expect(at('switch:disable-background-networking')).toBeGreaterThanOrEqual(0);
    expect(at('switch:disable-features')).toBeLessThan(at('whenReady'));
    expect(at('registerSchemesAsPrivileged')).toBeLessThan(at('whenReady'));
    expect(at('app.on:web-contents-created')).toBeLessThan(at('new BrowserWindow'));
    for (const name of ['default', 'persist:agent', 'persist:lab', 'updater']) {
      expect(at(`webRequest:${name}`), name).toBeGreaterThanOrEqual(0);
      expect(at(`webRequest:${name}`), name).toBeLessThan(at('new BrowserWindow'));
      expect(at(`permissions:${name}`), `permissions ${name}`).toBeLessThan(at('new BrowserWindow'));
    }
    expect(at('protocol.handle:default:localai')).toBeLessThan(at('new BrowserWindow'));
    expect(at('protocol.handle:persist:lab:localai')).toBeLessThan(at('new BrowserWindow'));
    expect(calls.filter((c) => c === 'new BrowserWindow')).toHaveLength(1);
    expect(sessionHooks['guardAtConstruction']).toBe(true);

    const { egressGuard } = await import('../../src/main/egress-guard');
    expect(egressGuard.installed).toBe(true);
    // Hidden until the start-up sequence decides what it shows (desktop.md 4.1); its own page is the top bar placeholder.
    expect(windows[0]!.url).toBe('localai://app/topbar/');
    expect(windows[0]!.options['show']).toBe(false);
    expect(bootstrap).toHaveBeenCalledTimes(1);
    expect(at('bootstrap')).toBeGreaterThan(at('new BrowserWindow'));
    expect((bootstrap.mock.calls[0] as unknown as [{ win: unknown }])[0].win).toBeDefined();
    expect(windows[0]!.options['webPreferences']).toMatchObject({
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webSecurity: true,
      spellcheck: false,
      devTools: false,
    });
  });

  it('createWindow refuses to run before the guard is installed', async () => {
    const { createWindow } = await import('../../src/main/index');
    const { egressGuard } = await import('../../src/main/egress-guard');
    expect(egressGuard.installed).toBe(false);
    expect(() => createWindow()).toThrow(/before the egress guard/);
    expect(calls).not.toContain('new BrowserWindow');
  });
});
