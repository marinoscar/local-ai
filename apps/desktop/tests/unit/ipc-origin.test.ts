// The IPC surface (docs/specs/desktop.md 3.5, 6): the origin check on every call, the settings patch allowlist, the
// handler tables and the status a page gets. No Electron: the registration takes a fake `ipcMain`.
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  CHANNELS,
  type BridgeDeps,
  type HandlerTable,
  type SenderEvent,
  buildDesktopStatus,
  createHostHandlers,
  createSettingsStore,
  createShellHandlers,
  isAllowedOrigin,
  projectSettings,
  registerHandlers,
  sanitizeBounds,
  senderAllowed,
  validateSettingsPatch,
} from '../../src/main/ipc';
import { DEFAULT_SETTINGS, normalizeSettings } from '../../src/main/settings';
import { HOST_MEMBERS } from '../../src/shared/types';

const SHELL = 'localai://app/topbar/';
const AGENT = 'http://127.0.0.1:7070/';

describe('origin check (the table of the issue)', () => {
  const cases: Array<[string, boolean, boolean]> = [
    // url, shell handler accepts, host handler accepts
    ['localai://app/topbar/', true, false],
    ['localai://app/lab/', true, false],
    ['localai://app/', true, false],
    ['http://127.0.0.1:7070/', false, true],
    ['http://127.0.0.1:7070/chat/', false, true],
    ['http://127.0.0.1:7070/portal', false, true],
    ['http://localhost:7070/', false, false],
    ['http://127.0.0.1:7071/', false, false],
    ['https://127.0.0.1:7070/', false, false],
    ['file:///etc/passwd', false, false],
    ['data:text/html,<h1>x</h1>', false, false],
    ['about:blank', false, false],
    ['https://ai.marin.cr/', false, false],
    ['http://ai.marin.cr/', false, false],
    ['localai://evil/topbar/', false, false],
    ['localai://app.evil/x', false, false],
    ['localai://user@app/', false, false],
    ['http://user:pw@127.0.0.1:7070/', false, false],
    ['http://127.0.0.1.evil.example:7070/', false, false],
    ['', false, false],
    ['not a url', false, false],
  ];
  it.each(cases)('%s: shell=%s host=%s', (url, shell, host) => {
    expect(isAllowedOrigin('shell', url)).toBe(shell);
    expect(isAllowedOrigin('host', url)).toBe(host);
  });

  it('the Vite dev origin counts as a shell origin only when it is passed (unpackaged builds)', () => {
    const dev = 'http://127.0.0.1:5173';
    expect(isAllowedOrigin('shell', 'http://127.0.0.1:5173/topbar/index.html')).toBe(false);
    expect(isAllowedOrigin('shell', 'http://127.0.0.1:5173/topbar/index.html', { devOrigin: dev })).toBe(true);
    expect(isAllowedOrigin('shell', 'http://127.0.0.1:5174/', { devOrigin: dev })).toBe(false);
    expect(isAllowedOrigin('host', 'http://127.0.0.1:5173/', { devOrigin: dev })).toBe(false);
    // A dev origin that is not loopback is never accepted.
    expect(isAllowedOrigin('shell', 'http://example.com:5173/', { devOrigin: 'http://example.com:5173' })).toBe(false);
  });

  it('only the main frame of a live frame passes', () => {
    const event = (frame: SenderEvent['senderFrame']): SenderEvent => ({ sender: { id: 1 }, senderFrame: frame });
    expect(senderAllowed('shell', event({ url: SHELL, parent: null }))).toBe(true);
    expect(senderAllowed('shell', event({ url: SHELL, parent: {} }))).toBe(false); // an iframe of a shell page
    expect(senderAllowed('shell', event(null))).toBe(false); // the frame is gone
  });
});

describe('settings patch allowlist (implementation note 2)', () => {
  it('accepts every user-settable key', () => {
    const result = validateSettingsPatch({
      autostart: false,
      updates: { checkForUpdates: true },
      lang: 'es',
      secrets: { allowPlaintext: true },
      window: { bounds: { x: 10, y: 20, width: 1280, height: 860, maximized: false } },
    });
    expect(result).toEqual({
      ok: true,
      patch: {
        autostart: false,
        updates: { checkForUpdates: true },
        lang: 'es',
        secrets: { allowPlaintext: true },
        window: { bounds: { x: 10, y: 20, width: 1280, height: 860, maximized: false } },
      },
    });
  });

  it('an empty patch is accepted and changes nothing', () => {
    expect(validateSettingsPatch({})).toEqual({ ok: true, patch: {} });
    expect(validateSettingsPatch({ updates: {} })).toEqual({ ok: true, patch: {} });
  });

  const refused: Array<[string, unknown]> = [
    ['cloud.linked (main process only)', { cloud: { linked: true } }],
    ['cloud.url', { cloud: { url: 'https://evil.example' } }],
    ['schema', { schema: 2 }],
    ['an unknown top-level key', { theme: 'dark' }],
    ['an unknown key in a section', { updates: { checkForUpdates: true, channel: 'beta' } }],
    ['an unknown key in the bounds', { window: { bounds: { width: 900, height: 600, fullscreen: true } } }],
    ['an unknown key in window', { window: { frame: false } }],
    ['__proto__ smuggling', JSON.parse('{"__proto__": {"autostart": true}}')],
    ['a string for a boolean', { autostart: 'yes' }],
    ['a bad language', { lang: 'fr' }],
    ['NaN bounds', { window: { bounds: { width: Number.NaN } } }],
    ['a string width', { window: { bounds: { width: '900' } } }],
    ['a zero size', { window: { bounds: { width: 0, height: 600 } } }],
    ['an absurd position', { window: { bounds: { x: 1e12 } } }],
    ['an array', []],
    ['null', null],
    ['a string', 'autostart'],
    ['a section that is not an object', { secrets: true }],
  ];
  it.each(refused)('refuses %s', (_name, patch) => {
    expect(validateSettingsPatch(patch).ok).toBe(false);
  });

  it('copies nothing the page sent beyond the known keys', () => {
    const result = validateSettingsPatch({ autostart: true });
    expect(result.ok && Object.keys(result.patch)).toEqual(['autostart']);
  });

  it('sanitizeBounds keeps integers in range and drops the rest', () => {
    expect(sanitizeBounds({ x: -5.4, y: 7, width: 1280.6, height: 860, maximized: true, junk: 1 })).toEqual({ x: -5, y: 7, width: 1281, height: 860, maximized: true });
    expect(sanitizeBounds({ width: -1, height: Number.POSITIVE_INFINITY, x: '3' })).toEqual({});
    expect(sanitizeBounds(null)).toEqual({});
    expect(sanitizeBounds([1, 2])).toEqual({});
  });

  it('projectSettings shows known keys only', () => {
    const settings = normalizeSettings({ ...DEFAULT_SETTINGS, futureKey: { x: 1 }, window: { bounds: { width: 900, junk: 1 }, other: 2 } });
    const shown = projectSettings(settings);
    expect(Object.keys(shown).sort()).toEqual(['autostart', 'cloud', 'lang', 'secrets', 'updates', 'window']);
    expect(shown.window).toEqual({ bounds: { width: 900 } });
  });
});

describe('settings store', () => {
  let dir: string | undefined;
  afterEach(async () => {
    if (dir) await rm(dir, { recursive: true, force: true });
    dir = undefined;
  });

  it('serialises concurrent updates, keeps both changes and reports each write', async () => {
    dir = await mkdtemp(join(tmpdir(), 'ai-desktop-ipc-'));
    const seen: string[] = [];
    const store = createSettingsStore(join(dir, 'cfg'), (_next, patch) => seen.push(Object.keys(patch).join(',')));
    await Promise.all([store.update({ autostart: false }), store.update({ lang: 'es' }), store.update({ window: { bounds: { width: 1000, height: 700 } } })]);
    const settings = await store.get();
    expect(settings.autostart).toBe(false);
    expect(settings.lang).toBe('es');
    expect(settings.window.bounds).toEqual({ width: 1000, height: 700 });
    expect(seen).toEqual(['autostart', 'lang', 'window']);
    expect(JSON.parse(await readFile(join(dir, 'cfg', 'settings.json'), 'utf8')).lang).toBe('es');
  });

  it('a listener that throws does not undo or fail the write', async () => {
    dir = await mkdtemp(join(tmpdir(), 'ai-desktop-ipc-'));
    const store = createSettingsStore(dir, () => {
      throw new Error('boom');
    });
    expect((await store.update({ autostart: false })).autostart).toBe(false);
  });
});

// ---------------------------------------------------------------------------------------------------------------------

function fakeIpc() {
  const handlers = new Map<string, { kind: 'handle' | 'on'; listener: (event: SenderEvent, ...args: unknown[]) => unknown }>();
  return {
    handlers,
    handle: (channel: string, listener: (event: SenderEvent, ...args: unknown[]) => unknown) => void handlers.set(channel, { kind: 'handle', listener }),
    on: (channel: string, listener: (event: SenderEvent, ...args: unknown[]) => unknown) => void handlers.set(channel, { kind: 'on', listener }),
  };
}

const from = (url: string | null, id = 1, parent: unknown = null): SenderEvent => ({ sender: { id }, senderFrame: url === null ? null : { url, parent } });

function makeDeps(overrides: Partial<BridgeDeps> = {}) {
  const calls = { openChild: vi.fn(), showTab: vi.fn(), showMenu: vi.fn(), update: vi.fn(), retry: vi.fn(), showAgentUi: vi.fn(async () => undefined) };
  const settings = normalizeSettings({});
  const deps: BridgeDeps = {
    policy: {},
    version: '9.9.9',
    lang: () => 'es',
    status: () => buildDesktopStatus({ version: '9.9.9', phase: 'ready', connection: 'connected', agentStatus: null, cloud: { linked: false, tokenHeld: false, url: 'https://ai.marin.cr' }, tab: 'local' }),
    openChild: calls.openChild,
    showTab: calls.showTab,
    showMenu: calls.showMenu,
    settings: { get: async () => settings, update: async (patch) => (calls.update(patch), settings) },
    pinWindowId: () => null,
    errorActions: {
      host: { showAgentUi: calls.showAgentUi },
      supervisor: {},
      clipboardWrite: vi.fn(),
      logout: async () => false,
      openAgentInBrowser: async () => undefined,
      retry: calls.retry,
    },
    ...overrides,
  };
  return { deps, calls };
}

describe('registration: nothing runs for a page that may not call', () => {
  const all = (deps: BridgeDeps): HandlerTable => ({ ...createShellHandlers(deps), ...createHostHandlers(deps) });

  it('every channel of CHANNELS (except the pushes) has a handler, and nothing else is registered', () => {
    const { deps } = makeDeps();
    const registered = Object.keys(all(deps)).sort();
    const pushes: string[] = [CHANNELS.statusChanged, CHANNELS.notice];
    const expected = Object.values(CHANNELS).filter((c) => !pushes.includes(c)).sort();
    expect(registered).toEqual(expected);
  });

  const rejectedPages = [
    'data:text/html,<script>1</script>',
    'file:///tmp/x.html',
    'http://localhost:7070/',
    'https://ai.marin.cr/',
    'about:blank',
  ];

  it.each(rejectedPages)('a shell-only handler refuses %s: invoke rejects, send and sync do nothing, no side effect', async (url) => {
    const { deps, calls } = makeDeps();
    const ipc = fakeIpc();
    const log = { write: vi.fn(), log: vi.fn() };
    const wrapped = registerHandlers(ipc, all(deps), { policy: {}, log });

    await expect(wrapped[CHANNELS.settingsSet]!(from(url), { autostart: false })).rejects.toThrow(/may not call/);
    await expect(wrapped[CHANNELS.settingsGet]!(from(url))).rejects.toThrow();
    await expect(wrapped[CHANNELS.status]!(from(url))).rejects.toThrow();
    await expect(wrapped[CHANNELS.setMode]!(from(url), 'survival')).rejects.toThrow();
    await expect(wrapped[CHANNELS.errorAction]!(from(url), { action: 'open-portal', code: 'LAI-DSK-004' })).rejects.toThrow();
    wrapped[CHANNELS.openWindow]!(from(url), 'chat');
    wrapped[CHANNELS.showTab]!(from(url), 'lab');
    wrapped[CHANNELS.showMenu]!(from(url));
    const syncEvent = from(url);
    wrapped[CHANNELS.lang]!(syncEvent);
    expect(syncEvent.returnValue).toBeNull();

    expect(calls.update).not.toHaveBeenCalled();
    expect(calls.openChild).not.toHaveBeenCalled();
    expect(calls.showTab).not.toHaveBeenCalled();
    expect(calls.showMenu).not.toHaveBeenCalled();
    expect(calls.showAgentUi).not.toHaveBeenCalled();
    // Each refusal is logged with the scheme and host only, never the path.
    expect(log.log).toHaveBeenCalled();
    expect(JSON.stringify(log.log.mock.calls)).not.toMatch(/script|etc|x\.html/);
  });

  it('an agent page (or a page in an iframe of a shell page) calling shell-only handlers is refused', async () => {
    const { deps, calls } = makeDeps();
    const wrapped = registerHandlers(fakeIpc(), all(deps), { policy: {} });
    await expect(wrapped[CHANNELS.settingsSet]!(from(AGENT), { autostart: false })).rejects.toThrow();
    wrapped[CHANNELS.showTab]!(from(AGENT), 'lab');
    await expect(wrapped[CHANNELS.settingsSet]!(from(SHELL, 1, {}), { autostart: false })).rejects.toThrow();
    expect(calls.update).not.toHaveBeenCalled();
    expect(calls.showTab).not.toHaveBeenCalled();
  });

  it('host handlers refuse a shell page and answer the agent page', () => {
    const { deps, calls } = makeDeps();
    const wrapped = registerHandlers(fakeIpc(), all(deps), { policy: {} });
    const refused = from(SHELL);
    wrapped[CHANNELS.hostVersion]!(refused);
    expect(refused.returnValue).toBeNull();
    wrapped[CHANNELS.hostOpenWindow]!(from(SHELL), 'chat');
    expect(calls.openChild).not.toHaveBeenCalled();

    const ok = from(AGENT);
    wrapped[CHANNELS.hostVersion]!(ok);
    expect(ok.returnValue).toBe('9.9.9');
    wrapped[CHANNELS.hostOpenWindow]!(from(AGENT), 'library');
    expect(calls.openChild).toHaveBeenCalledWith('library');
  });

  it('registers each handler on the right ipcMain method', () => {
    const { deps } = makeDeps();
    const ipc = fakeIpc();
    registerHandlers(ipc, all(deps), { policy: {} });
    expect(ipc.handlers.get(CHANNELS.status)?.kind).toBe('handle');
    expect(ipc.handlers.get(CHANNELS.openWindow)?.kind).toBe('on');
    expect(ipc.handlers.get(CHANNELS.lang)?.kind).toBe('on');
    expect(ipc.handlers.get(CHANNELS.hostVersion)?.kind).toBe('on');
  });
});

describe('what an allowed shell page can do', () => {
  const wrap = (deps: BridgeDeps) => registerHandlers(fakeIpc(), createShellHandlers(deps), { policy: {} });

  it('openWindow accepts the three names and nothing else', () => {
    const { deps, calls } = makeDeps();
    const wrapped = wrap(deps);
    for (const name of ['chat', 'library', 'maps']) wrapped[CHANNELS.openWindow]!(from(SHELL), name);
    for (const bad of ['settings', '../chat', '', 42, null, { name: 'chat' }]) wrapped[CHANNELS.openWindow]!(from(SHELL), bad);
    expect(calls.openChild.mock.calls.map((c) => c[0])).toEqual(['chat', 'library', 'maps']);
  });

  it('showTab accepts local and lab only', () => {
    const { deps, calls } = makeDeps();
    const wrapped = wrap(deps);
    for (const tab of ['local', 'lab', 'other', 1]) wrapped[CHANNELS.showTab]!(from(SHELL), tab);
    expect(calls.showTab.mock.calls.map((c) => c[0])).toEqual(['local', 'lab']);
  });

  it('settings.set validates, writes only the validated patch and answers the projection', async () => {
    const { deps, calls } = makeDeps();
    const wrapped = wrap(deps);
    const shown = (await wrapped[CHANNELS.settingsSet]!(from(SHELL), { autostart: false })) as Record<string, unknown>;
    expect(calls.update).toHaveBeenCalledWith({ autostart: false });
    expect(Object.keys(shown)).toContain('cloud');
    await expect(wrapped[CHANNELS.settingsSet]!(from(SHELL), { cloud: { linked: true } })).rejects.toThrow(/settings:/);
    expect(calls.update).toHaveBeenCalledTimes(1);
  });

  it('setMode checks its argument, then asks the mode controller (or answers not_implemented without one)', async () => {
    const { deps } = makeDeps();
    expect(await wrap(deps)[CHANNELS.setMode]!(from(SHELL), 'offline')).toEqual({ ok: false, code: 'not_implemented' });
    expect(await wrap(deps)[CHANNELS.setMode]!(from(SHELL), 'rm -rf')).toEqual({ ok: false });

    const setMode = vi.fn(async () => ({ ok: true }));
    const wired = wrap(makeDeps({ setMode }).deps);
    expect(await wired[CHANNELS.setMode]!(from(SHELL), 'survival')).toEqual({ ok: true });
    expect(await wired[CHANNELS.setMode]!(from(SHELL), 'reboot')).toEqual({ ok: false });
    expect(await wired[CHANNELS.setMode]!(from(SHELL), { target: 'online' })).toEqual({ ok: false });
    expect(setMode.mock.calls).toEqual([['survival']]);
    // From a page that may not call, the mode controller is never reached.
    await expect(wired[CHANNELS.setMode]!(from(AGENT), 'offline')).rejects.toThrow();
    expect(setMode).toHaveBeenCalledTimes(1);
  });

  it('the methods of later items answer { ok: false, code: "not_implemented" }', async () => {
    const { deps } = makeDeps();
    const wrapped = wrap(deps);
    for (const channel of [CHANNELS.startAgent, CHANNELS.copyApiKey, CHANNELS.connectionInfo, CHANNELS.revealKey, CHANNELS.cloudBeginLink, CHANNELS.cloudUnlink, CHANNELS.cloudRequest]) {
      expect(await wrapped[channel]!(from(SHELL)), channel).toEqual({ ok: false, code: 'not_implemented' });
    }
  });

  it('lang answers the language in use', () => {
    const { deps } = makeDeps();
    const event = from(SHELL);
    wrap(deps)[CHANNELS.lang]!(event);
    expect(event.returnValue).toBe('es');
  });

  it('the error-page actions work from the error page only, and still check the code owns the button', async () => {
    const { deps, calls } = makeDeps();
    const wrapped = wrap(deps);
    const payload = { action: 'open-portal', code: 'LAI-DSK-004' };
    await expect(wrapped[CHANNELS.errorAction]!(from('localai://app/topbar/'), payload)).rejects.toThrow();
    expect(calls.showAgentUi).not.toHaveBeenCalled();
    expect(await wrapped[CHANNELS.errorAction]!(from('localai://app/error/?code=LAI-DSK-004'), payload)).toEqual({ ok: true });
    expect(calls.showAgentUi).toHaveBeenCalledWith('/portal');
    // copy-command is not a button of LAI-DSK-004.
    expect(await wrapped[CHANNELS.errorAction]!(from('localai://app/error/'), { action: 'copy-command', code: 'LAI-DSK-004' })).toEqual({ ok: false });
  });

  it('pinSubmit is refused from every page but the PIN window', async () => {
    let pin: number | null = null;
    const { deps } = makeDeps({ pinWindowId: () => pin });
    const wrapped = wrap(deps);
    await expect(wrapped[CHANNELS.pinSubmit]!(from(SHELL, 5), '1234')).rejects.toThrow();
    pin = 5;
    await expect(wrapped[CHANNELS.pinSubmit]!(from(SHELL, 6), '1234')).rejects.toThrow();
    expect(await wrapped[CHANNELS.pinSubmit]!(from('localai://app/pin/', 5), '1234')).toEqual({ ok: false, code: 'not_implemented' });
  });
});

describe('the host bridge has the five members and no handler for anything else', () => {
  it('lists exactly isDesktop, version, copyApiKey, openWindow, connectionInfo', () => {
    expect([...HOST_MEMBERS]).toEqual(['isDesktop', 'version', 'copyApiKey', 'openWindow', 'connectionInfo']);
    const { deps } = makeDeps();
    expect(Object.keys(createHostHandlers(deps)).sort()).toEqual([CHANNELS.hostConnectionInfo, CHANNELS.hostCopyApiKey, CHANNELS.hostOpenWindow, CHANNELS.hostVersion].sort());
  });
});

describe('buildDesktopStatus', () => {
  const cloud = { linked: true, tokenHeld: true, url: 'https://ai.marin.cr' };
  const base = { version: '1.2.3', phase: 'ready' as const, connection: 'connected' as const, cloud, tab: 'local' as const };

  it('reads mode, connectivity and the alerts that matter', () => {
    const status = buildDesktopStatus({
      ...base,
      agentStatus: {
        mode: { value: 'online' },
        connectivity: { value: 'full' },
        alerts: [{ severity: 'info' }, { severity: 'warning' }, { severity: 'critical' }, {}],
      } as never,
    });
    expect(status).toMatchObject({ version: '1.2.3', agent: 'up', mode: 'online', connectivity: 'full', alerts: 2, connected: true, tab: 'local' });
  });

  it('connected mode needs the link, a held token AND the online mode (desktop.md 4.9)', () => {
    const online = { mode: { value: 'online' } } as never;
    expect(buildDesktopStatus({ ...base, agentStatus: online }).connected).toBe(true);
    expect(buildDesktopStatus({ ...base, agentStatus: { mode: { value: 'offline' } } as never }).connected).toBe(false);
    expect(buildDesktopStatus({ ...base, agentStatus: { mode: { value: 'survival' } } as never }).connected).toBe(false);
    expect(buildDesktopStatus({ ...base, agentStatus: null }).connected).toBe(false);
    expect(buildDesktopStatus({ ...base, agentStatus: online, cloud: { ...cloud, tokenHeld: false } }).connected).toBe(false);
    expect(buildDesktopStatus({ ...base, agentStatus: online, cloud: { ...cloud, linked: false } }).connected).toBe(false);
  });

  it('an unknown mode value is null, not passed through', () => {
    expect(buildDesktopStatus({ ...base, agentStatus: { mode: { value: 'turbo' } } as never }).mode).toBeNull();
  });

  it.each([
    ['starting', 'connected', 'starting'],
    ['error', 'connected', 'down'],
    ['ready', 'connected', 'up'],
    ['ready', 'connecting', 'starting'],
    ['ready', 'reconnecting', 'down'],
    ['ready', 'shutdown', 'down'],
    ['ready', 'closed', 'down'],
    ['ready', 'unauthorized', 'unauthorized'],
  ] as const)('phase %s, connection %s: agent %s', (phase, connection, agent) => {
    expect(buildDesktopStatus({ ...base, phase, connection, agentStatus: null }).agent).toBe(agent);
  });
});
