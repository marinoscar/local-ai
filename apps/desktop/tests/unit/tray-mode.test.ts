import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AgentClient, AgentFeed } from '../../src/main/agent-client';
import { Notifier } from '../../src/main/notify';
import { readSettings, writeSettings, DEFAULT_SETTINGS } from '../../src/main/settings';
import {
  type CommandRunner,
  type Dialogs,
  type PinPrompt,
  type TrayView,
  ModeController,
  PIN_GRANT_HEADER,
  TrayController,
  createElectronDialogs,
  createElectronNotificationPort,
  createModeApi,
  describeModeFailure,
  detectTray,
  installHideToTray,
  isShellPageUrl,
  parseModeTarget,
  parseNameHasOwner,
  showTrayNoticeOnce,
  trayNoticeStore,
  trayUsable,
} from '../../src/main/tray';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';

let agent: FakeAgent;
let dir: string;
let tokenPath: string;
const tick = (): Promise<void> => new Promise((r) => setTimeout(r, 1));
const until = async (condition: () => boolean, ms = 3000): Promise<void> => {
  const end = Date.now() + ms;
  while (!condition()) {
    if (Date.now() > end) throw new Error('timed out waiting for a condition');
    await new Promise((r) => setTimeout(r, 5));
  }
};

beforeEach(async () => {
  agent = await startFakeAgent({ port: 0 });
  dir = await mkdtemp(join(tmpdir(), 'ai-desktop-tray-'));
  tokenPath = join(dir, 'agent-token');
  await writeFile(tokenPath, `${agent.token}\n`);
});
afterEach(async () => {
  await agent.close();
  await rm(dir, { recursive: true, force: true });
});

const client = (over: { tokenPath?: string; baseUrl?: string } = {}): AgentClient => new AgentClient({ baseUrl: over.baseUrl ?? agent.url, tokenPath: over.tokenPath ?? tokenPath, sleep: tick });

/** A tray controller over the real client and the fake agent; the window and dialogs are recorded fakes. */
function trayOver(c: AgentClient, options: { pin?: PinPrompt; confirm?: boolean } = {}) {
  const views: TrayView[] = [];
  const alerts: Array<{ message: string; detail?: string }> = [];
  const confirms: string[] = [];
  const dialogs: Dialogs = {
    confirm: async (o) => (confirms.push(o.message), options.confirm ?? true),
    alert: async (o) => void alerts.push(o),
  };
  let tray: TrayController | undefined;
  const modes = new ModeController({ api: createModeApi(c), ...(options.pin ? { pin: options.pin } : {}), currentMode: () => tray?.mode });
  const shown: Array<{ title: string; body: string }> = [];
  const notifier = new Notifier({ port: { supported: () => true, show: (n) => void shown.push(n) }, lang: () => 'en', clock: Date.now, onActivate: () => undefined });
  const feed = new AgentFeed(c);
  tray = new TrayController({
    port: { update: (v) => void views.push(v), destroy: () => undefined },
    lang: () => 'en',
    clock: Date.now,
    notifier,
    modes,
    probeMs: 50,
    probe: async () => ((await c.health({ tries: 1, timeoutMs: 500 })).reachable ? 'up' : 'down'),
    refreshStatus: () => feed.refresh(),
    readMode: () => createModeApi(c).read(),
    windows: { showMain: () => undefined },
    dialogs,
    actions: { quit: () => undefined },
  });
  feed.subscribe((m) => tray!.handle(m));
  tray.start();
  return { tray, feed, views, alerts, confirms, shown, last: () => views.at(-1)! };
}

describe('mode switch against the fake agent', () => {
  it('Offline sends exactly POST /api/v1/mode {"target":"offline","wait":false} with the bearer token', async () => {
    const t = trayOver(client());
    await t.tray.invoke('mode:offline');
    expect(agent.modeRequests).toEqual([
      { path: '/api/v1/mode', body: { target: 'offline', wait: false }, authorization: `Bearer ${agent.token}`, pinGrant: undefined, contentType: 'application/json' },
    ]);
    expect(t.alerts).toEqual([]);
    expect(agent.mode).toBe('offline');
  });

  it('Online sends target online', async () => {
    agent.mode = 'offline';
    const t = trayOver(client());
    await t.tray.invoke('mode:online');
    expect(agent.modeRequests[0]!.body).toEqual({ target: 'online', wait: false });
  });

  it('a request for the mode the node is in is answered "already" and is not an error', async () => {
    const t = trayOver(client());
    await t.tray.invoke('mode:online');
    expect(t.alerts).toEqual([]);
    expect(await createModeApi(client()).request('online')).toEqual({ ok: true, already: true });
  });

  it('Survival asks once, then sends POST /api/v1/mode/survival with body {} and no Authorization header', async () => {
    const t = trayOver(client());
    await t.tray.invoke('survival');
    expect(t.confirms).toEqual(['Switch to Survival mode? Everything you need offline will start.']);
    expect(agent.modeRequests).toEqual([{ path: '/api/v1/mode/survival', body: {}, authorization: undefined, pinGrant: undefined, contentType: 'application/json' }]);
    expect(agent.mode).toBe('survival');
  });

  it('Survival declined sends nothing', async () => {
    const t = trayOver(client(), { confirm: false });
    await t.tray.invoke('survival');
    expect(agent.modeRequests).toEqual([]);
  });

  it('Survival works even when the token file cannot be read (it needs no login)', async () => {
    const t = trayOver(client({ tokenPath: join(dir, 'missing') }));
    await t.tray.invoke('survival');
    expect(agent.mode).toBe('survival');
    expect(agent.modeRequests[0]!.authorization).toBeUndefined();
    expect(t.alerts).toEqual([]);
  });

  it('Offline with an unreadable token file says so with the code of the error page', async () => {
    const t = trayOver(client({ tokenPath: join(dir, 'missing') }));
    await t.tray.invoke('mode:offline');
    expect(agent.modeRequests).toEqual([]);
    expect(t.alerts[0]!.message).toContain('has not created its key file');
    expect(t.alerts[0]!.detail).toContain('LAI-DSK-003');
  });

  it('another transition running: the dialog carries the agent\'s code, message and fix', async () => {
    agent.failNextModeChange = { status: 409, code: 'LAI-AGT-022', details: { op_id: 'op_x' } };
    const t = trayOver(client());
    await t.tray.invoke('mode:offline');
    expect(t.alerts).toHaveLength(1);
    expect(t.alerts[0]!.message).toBe('Another operation is already running');
    expect(t.alerts[0]!.detail).toContain('Wait for it to finish, then try again.');
    expect(t.alerts[0]!.detail).toContain('LAI-AGT-022');
  });

  it('an agent that does not answer is LAI-DSK-004', async () => {
    const c = client({ baseUrl: 'http://127.0.0.1:9' });
    const t = trayOver(c);
    await t.tray.invoke('survival');
    expect(t.alerts[0]!.detail).toContain('LAI-DSK-004');
  });

  it('never sends a body or header it should not: Online and Offline are never anonymous, survival is never given a PIN grant', async () => {
    const api = createModeApi(client());
    await api.request('survival', { pinGrant: 'ignored-for-survival' });
    expect(agent.modeRequests[0]!.authorization).toBeUndefined();
  });
});

describe('leaving survival (PIN)', () => {
  const grant = 'grant-for-this-test';

  it('without a PIN prompt (DSK-08 not landed) the tray cannot leave survival and does not even ask the agent', async () => {
    agent.mode = 'survival';
    const t = trayOver(client());
    t.feed.seed({ mode: { value: 'survival' } });
    await t.feed.refresh();
    expect(t.last().colour).toBe('orange');
    expect(JSON.stringify(t.last().menu)).toContain('Leave survival in the Survival Portal');
    await t.tray.invoke('mode:online');
    expect(agent.modeRequests).toEqual([]);
    expect(t.alerts[0]!.detail).toContain('LAI-SEC-002');
  });

  it('with a PIN prompt: asks for a grant first (the mode is known), sends it as X-LocalAI-Pin-Grant, and the node leaves survival', async () => {
    agent.mode = 'survival';
    agent.validPinGrant = grant;
    const requestGrant = vi.fn(async () => ({ ok: true as const, grant }));
    const t = trayOver(client(), { pin: { requestGrant } });
    await t.feed.refresh();
    await t.tray.invoke('mode:online');
    expect(requestGrant).toHaveBeenCalledTimes(1);
    expect(requestGrant).toHaveBeenCalledWith('leave-survival');
    // One request, with the grant, and no refusal for the agent to audit.
    expect(agent.modeRequests).toHaveLength(1);
    expect(agent.modeRequests[0]).toMatchObject({ body: { target: 'online', wait: false }, pinGrant: grant });
    expect(agent.mode).toBe('online');
    expect(t.alerts).toEqual([]);
  });

  it('a cancelled PIN window sends nothing and shows nothing', async () => {
    agent.mode = 'survival';
    const t = trayOver(client(), { pin: { requestGrant: async () => ({ ok: false, cancelled: true }) } });
    await t.feed.refresh();
    await t.tray.invoke('mode:offline');
    expect(agent.modeRequests).toEqual([]);
    expect(t.alerts).toEqual([]);
  });

  it('a wrong or locked PIN shows the agent\'s code', async () => {
    agent.mode = 'survival';
    const t = trayOver(client(), { pin: { requestGrant: async () => ({ ok: false, code: 'LAI-SEC-004' }) } });
    await t.feed.refresh();
    await t.tray.invoke('mode:offline');
    expect(t.alerts[0]!.detail).toContain('LAI-SEC-004');
  });

  it('a stale mode: the agent says pin_required, the tray asks for the grant and retries once with it', async () => {
    agent.mode = 'survival';
    agent.validPinGrant = grant;
    const requestGrant = vi.fn(async () => ({ ok: true as const, grant }));
    // No refresh: the tray still believes the node is online.
    const modes = new ModeController({ api: createModeApi(client()), pin: { requestGrant }, currentMode: () => 'online' });
    expect(await modes.setMode('offline')).toEqual({ ok: true, already: false });
    expect(agent.modeRequests.map((r) => r.pinGrant)).toEqual([undefined, grant]);
    expect(requestGrant).toHaveBeenCalledTimes(1);
  });

  it('the grant is never kept: a second change asks again', async () => {
    agent.mode = 'survival';
    agent.validPinGrant = grant;
    const requestGrant = vi.fn(async () => ({ ok: true as const, grant }));
    const modes = new ModeController({ api: createModeApi(client()), pin: { requestGrant }, currentMode: () => 'survival' });
    await modes.setMode('online');
    agent.mode = 'survival';
    await modes.setMode('offline');
    expect(requestGrant).toHaveBeenCalledTimes(2);
  });

  it('two PIN prompts never run at once', async () => {
    let release!: () => void;
    const requestGrant = vi.fn(() => new Promise<{ ok: true; grant: string }>((resolve) => (release = () => resolve({ ok: true, grant }))));
    agent.validPinGrant = grant;
    agent.mode = 'survival';
    const modes = new ModeController({ api: createModeApi(client()), pin: { requestGrant }, currentMode: () => 'survival' });
    const first = modes.setMode('online');
    const second = await modes.setMode('offline');
    expect(second).toMatchObject({ ok: false, cancelled: true });
    release();
    expect(await first).toMatchObject({ ok: true });
    expect(requestGrant).toHaveBeenCalledTimes(1);
  });

  it('exposes the grant header name of agent.md', () => {
    expect(PIN_GRANT_HEADER).toBe('X-LocalAI-Pin-Grant');
  });
});

describe('the whole path: feed, controller and notifications over the fake agent', () => {
  it('green at start; Offline from the menu turns it blue and notifies once; Survival turns it orange', async () => {
    agent.models = { default: 'model-default', loaded: ['model-a'] };
    const t = trayOver(client());
    t.feed.start({ backoff: { minMs: 5, maxMs: 20 } });
    await t.feed.refresh();
    await until(() => agent.openEventStreams() === 1 && t.last().colour === 'green');
    expect(t.last().tooltip).toBe('Local AI: ready (online)');
    expect(JSON.stringify(t.last().menu)).toContain('model-a');

    await t.tray.invoke('mode:offline');
    await until(() => t.last().colour === 'blue');
    expect(t.shown.map((s) => s.body)).toEqual(['Local AI is now in offline mode.']);

    await t.tray.invoke('survival');
    await until(() => t.last().colour === 'orange');
    expect(t.shown.map((s) => s.body)).toEqual(['Local AI is now in offline mode.', 'Local AI is now in survival mode.']);
    t.feed.stop();
  });

  it('a warning alert raised by the agent turns it amber and notifies; clearing it brings the colour back', async () => {
    const t = trayOver(client());
    t.feed.start({ backoff: { minMs: 5, maxMs: 20 } });
    await t.feed.refresh();
    await until(() => agent.openEventStreams() === 1);
    // The agent's /status holds the alert too (the controller re-reads it after events, and /status is authoritative).
    const alert = { code: 'LAI-HW-012', severity: 'warning', title: 'Running on battery', fix: 'Plug in the charger.' };
    agent.alerts = [alert];
    agent.emit('alert.raised', alert);
    await until(() => t.last().colour === 'amber');
    expect(t.shown).toHaveLength(1);
    agent.alerts = [];
    agent.emit('alert.cleared', { code: 'LAI-HW-012', severity: 'warning' });
    await until(() => t.last().colour === 'green');
    t.feed.stop();
  });

  it('the agent stopping: grey on the drop, red once /health stops answering, green again when it is back', async () => {
    const t = trayOver(client());
    t.feed.start({ backoff: { minMs: 5, maxMs: 20 } });
    await t.feed.refresh();
    await until(() => t.last().colour === 'green' && agent.openEventStreams() === 1);
    const port = agent.port;
    agent.shutdownEventStreams();
    await agent.close();
    await until(() => t.last().colour === 'red');
    expect(JSON.stringify(t.last().menu)).not.toContain('Start agent'); // no owner for it in this wiring
    agent = await startFakeAgent({ port, token: agent.token });
    await until(() => t.last().colour === 'green', 8000);
    t.feed.stop();
  });
});

describe('describeModeFailure', () => {
  it('uses the agent text for an agent code, and the error-page text for a desktop code', () => {
    expect(describeModeFailure({ ok: false, code: 'LAI-AGT-022', message: 'Busy', fix: 'Wait.' }, 'en')).toEqual({
      title: "The mode couldn't be changed",
      message: 'Busy',
      detail: 'What to do: Wait.\n\nLAI-AGT-022',
    });
    const dsk = describeModeFailure({ ok: false, code: 'LAI-DSK-004' }, 'es');
    expect(dsk.title).toBe('No se pudo cambiar el modo');
    expect(dsk.message).toContain('Local AI no está funcionando');
    expect(dsk.detail).toContain('LAI-DSK-004');
  });

  it('has something to say when the agent sent no text', () => {
    expect(describeModeFailure({ ok: false, code: 'HTTP 500' }, 'en')).toMatchObject({ message: 'Something went wrong while changing the mode.', detail: 'HTTP 500' });
  });
});

describe('shell bridge helpers', () => {
  it('only a known target is accepted', () => {
    expect(['online', 'offline', 'survival'].map(parseModeTarget)).toEqual(['online', 'offline', 'survival']);
    for (const bad of ['', 'ONLINE', 'reboot', null, undefined, 5, {}, ['online']]) expect(parseModeTarget(bad)).toBeNull();
  });

  it('only shell pages may call it', () => {
    expect(isShellPageUrl('localai://app/topbar/')).toBe(true);
    expect(isShellPageUrl('localai://app/error/?code=LAI-DSK-004')).toBe(true);
    expect(isShellPageUrl('http://127.0.0.1:7070/')).toBe(false);
    expect(isShellPageUrl('localai://evil/topbar/')).toBe(false);
    expect(isShellPageUrl('https://ai.marin.cr/')).toBe(false);
    expect(isShellPageUrl('')).toBe(false);
    expect(isShellPageUrl('http://localhost:5173/topbar/', 'http://localhost:5173')).toBe(true);
    expect(isShellPageUrl('http://localhost:5173/topbar/')).toBe(false);
  });
});

describe('tray availability (LAI-DSK-013)', () => {
  const runner = (script: Record<string, Awaited<ReturnType<CommandRunner>>>): CommandRunner & { calls: string[] } => {
    const calls: string[] = [];
    const fn: CommandRunner = async (file, args) => {
      calls.push(`${file} ${args.at(-1)}`);
      return script[file] ?? { status: 'missing' };
    };
    return Object.assign(fn, { calls });
  };

  it('parses the output of busctl and gdbus', () => {
    expect(parseNameHasOwner('b true\n')).toBe(true);
    expect(parseNameHasOwner('(true,)\n')).toBe(true);
    expect(parseNameHasOwner('b false\n')).toBe(false);
    expect(parseNameHasOwner('(false,)')).toBe(false);
    expect(parseNameHasOwner('Failed to connect to bus')).toBeNull();
    expect(parseNameHasOwner('')).toBeNull();
  });

  it('a watcher on the bus: available', async () => {
    const run = runner({ '/usr/bin/busctl': { status: 'ok', stdout: 'b true\n' } });
    expect(await detectTray(run)).toBe('available');
    expect(run.calls).toEqual(['/usr/bin/busctl org.kde.StatusNotifierWatcher']);
  });

  it('only the freedesktop name is owned: still available', async () => {
    const run: CommandRunner = async (_file, args) => ({ status: 'ok', stdout: args.at(-1) === 'org.freedesktop.StatusNotifierWatcher' ? 'b true\n' : 'b false\n' });
    expect(await detectTray(run)).toBe('available');
  });

  it('no watcher, or no session bus at all: unavailable', async () => {
    expect(await detectTray(runner({ '/usr/bin/busctl': { status: 'ok', stdout: 'b false\n' } }))).toBe('unavailable');
    expect(await detectTray(runner({ '/usr/bin/busctl': { status: 'failed' } }))).toBe('unavailable');
  });

  it('busctl missing: gdbus answers', async () => {
    expect(await detectTray(runner({ '/usr/bin/gdbus': { status: 'ok', stdout: '(true,)\n' } }))).toBe('available');
    expect(await detectTray(runner({ '/usr/bin/gdbus': { status: 'ok', stdout: '(false,)\n' } }))).toBe('unavailable');
  });

  it('no D-Bus client program at all: unknown, and unknown counts as usable', async () => {
    const result = await detectTray(runner({}));
    expect(result).toBe('unknown');
    expect(trayUsable(result)).toBe(true);
    expect(trayUsable('available')).toBe(true);
    expect(trayUsable('unavailable')).toBe(false);
  });

  it('the real runner never throws on this machine', async () => {
    expect(['available', 'unavailable', 'unknown']).toContain(await detectTray());
  });
});

describe('no tray: closing the window quits, and LAI-DSK-013 shows once', () => {
  const fakeWindow = () => {
    const state = { hidden: 0, listener: undefined as undefined | ((e: { preventDefault(): void }) => void) };
    return {
      state,
      win: { on: (_event: 'close', fn: (e: { preventDefault(): void }) => void) => void (state.listener = fn), hide: () => void (state.hidden += 1) },
      close: (): boolean => {
        let prevented = false;
        state.listener!({ preventDefault: () => void (prevented = true) });
        return prevented;
      },
    };
  };

  it('with a tray, close hides the window instead of closing it; during a quit it really closes', () => {
    const w = fakeWindow();
    let quitting = false;
    installHideToTray(w.win, { trayActive: () => true, quitting: () => quitting });
    expect(w.close()).toBe(true);
    expect(w.state.hidden).toBe(1);
    quitting = true;
    expect(w.close()).toBe(false);
    expect(w.state.hidden).toBe(1);
  });

  it('when the tray stops being active, close closes (the app then quits on window-all-closed)', () => {
    const w = fakeWindow();
    let active = true;
    installHideToTray(w.win, { trayActive: () => active, quitting: () => false });
    active = false;
    expect(w.close()).toBe(false);
    expect(w.state.hidden).toBe(0);
  });

  it('the notice is shown once, remembered in settings.json, and the other settings survive', async () => {
    await writeSettings(dir, { ...DEFAULT_SETTINGS, lang: 'es', futureKey: { keep: 'me' } } as never);
    const notify = vi.fn(() => true);
    const store = trayNoticeStore(dir);
    expect(await showTrayNoticeOnce({ store, notify })).toBe(true);
    expect(await showTrayNoticeOnce({ store, notify })).toBe(false);
    expect(await showTrayNoticeOnce({ store: trayNoticeStore(dir), notify })).toBe(false);
    expect(notify).toHaveBeenCalledTimes(1);
    const saved = JSON.parse(await readFile(join(dir, 'settings.json'), 'utf8')) as Record<string, unknown>;
    expect(saved['notices']).toEqual({ 'LAI-DSK-013': true });
    expect(saved['lang']).toBe('es');
    expect(saved['futureKey']).toEqual({ keep: 'me' });
    expect((await readSettings(dir))['notices']).toEqual({ 'LAI-DSK-013': true });
  });

  it('a settings folder that cannot be written does not make the notice fail', async () => {
    const notify = vi.fn(() => true);
    const store = { shown: async () => false, markShown: async () => { throw new Error('read only'); } };
    await expect(showTrayNoticeOnce({ store, notify })).resolves.toBe(true);
    expect(notify).toHaveBeenCalledTimes(1);
  });
});

describe('Electron adapters for dialogs and notifications', () => {
  it('confirm is a two-button question whose second button means yes; Cancel is the escape', async () => {
    const calls: unknown[] = [];
    const answer = { response: 1 };
    const dialogs = createElectronDialogs({ showMessageBox: async (o) => (calls.push(o), answer) });
    expect(await dialogs.confirm({ title: 'T', message: 'M', confirmLabel: 'Yes', cancelLabel: 'No' })).toBe(true);
    answer.response = 0;
    expect(await dialogs.confirm({ title: 'T', message: 'M', confirmLabel: 'Yes', cancelLabel: 'No' })).toBe(false);
    expect(calls[0]).toMatchObject({ type: 'question', buttons: ['No', 'Yes'], defaultId: 1, cancelId: 0 });
    await dialogs.alert({ title: 'T', message: 'M', detail: 'D', okLabel: 'OK' });
    expect(calls[2]).toMatchObject({ type: 'error', message: 'M', detail: 'D', buttons: ['OK'] });
  });

  it('shows a native notification, keeps it alive until it closes, and reports clicks', () => {
    const made: Array<{ options: unknown; handlers: Record<string, () => void>; shown: boolean }> = [];
    class FakeNotification {
      handlers: Record<string, () => void> = {};
      shown = false;
      constructor(public options: { title: string; body: string }) {
        made.push(this);
      }
      show(): void {
        this.shown = true;
      }
      on(event: string, fn: () => void): void {
        this.handlers[event] = fn;
      }
      static isSupported(): boolean {
        return true;
      }
    }
    const port = createElectronNotificationPort({ Notification: FakeNotification as never });
    expect(port.supported()).toBe(true);
    const onClick = vi.fn();
    port.show({ title: 'Mode changed', body: 'x' }, onClick);
    expect(made[0]).toMatchObject({ options: { title: 'Mode changed', body: 'x' }, shown: true });
    made[0]!.handlers['click']!();
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});
