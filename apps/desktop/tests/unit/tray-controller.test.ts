import { describe, expect, it } from 'vitest';
import type { AgentEvent, AgentStatus, ConnectionState, FeedMessage } from '../../src/main/agent-client';
import {
  type Dialogs,
  type MenuItemSpec,
  type ModeInfo,
  type ModeOutcome,
  type Timers,
  type TrayActions,
  type TrayControllerDeps,
  type TrayView,
  TrayController,
} from '../../src/main/tray';
import type { Lang } from '../../src/main/i18n';

const NOW = Date.parse('2026-11-02T15:00:00Z');

const status = (over: Record<string, unknown> = {}): AgentStatus =>
  ({
    node: { id: 'ai-n3' },
    mode: { value: 'online' },
    connectivity: { value: 'full', none_since: null },
    models: { default: 'model-default', loaded: ['model-a'] },
    alerts: [],
    ...over,
  }) as AgentStatus;

const ev = (type: string, data: unknown, at?: string): FeedMessage => ({ kind: 'event', event: { id: 1, type, data, ...(at ? { at } : {}) } as AgentEvent });
const conn = (state: ConnectionState): FeedMessage => ({ kind: 'connection', state });
const stat = (s: AgentStatus): FeedMessage => ({ kind: 'status', status: s });

/** A manual clock and timer queue: nothing here waits for real time. */
function fakeTime() {
  let now = NOW;
  const pending: Array<{ at: number; fn: () => void; cancelled: boolean }> = [];
  const timers: Timers = {
    after(ms, fn) {
      const entry = { at: now + ms, fn, cancelled: false };
      pending.push(entry);
      return () => void (entry.cancelled = true);
    },
  };
  const flush = async (): Promise<void> => {
    for (let i = 0; i < 20; i += 1) await Promise.resolve();
  };
  return {
    timers,
    clock: () => now,
    async advance(ms: number): Promise<void> {
      const end = now + ms;
      for (;;) {
        await flush();
        const next = pending.filter((p) => !p.cancelled && p.at <= end).sort((a, b) => a.at - b.at)[0];
        if (!next) break;
        pending.splice(pending.indexOf(next), 1);
        now = Math.max(now, next.at);
        next.fn();
      }
      now = end;
      await flush();
    },
    flush,
    pendingCount: () => pending.filter((p) => !p.cancelled).length,
  };
}

function setup(over: { lang?: Lang; actions?: Partial<TrayActions>; openWindow?: boolean; probes?: Array<'up' | 'starting' | 'down'>; modeInfo?: ModeInfo | null; setMode?: (t: string) => Promise<ModeOutcome>; confirm?: boolean } = {}) {
  const time = fakeTime();
  const views: TrayView[] = [];
  const notified: string[] = [];
  let suggested = 0;
  const modeCalls: string[] = [];
  const alerts: Array<{ title: string; message: string; detail?: string; okLabel: string }> = [];
  const confirms: Array<{ message: string; confirmLabel: string }> = [];
  const windows: string[] = [];
  const refreshCalls = { n: 0 };
  const probes = over.probes ?? [];
  let probeIndex = 0;
  let probeCount = 0;
  let modeInfo: ModeInfo | null = over.modeInfo ?? { mode: 'online', suggestSurvival: false };
  let destroyed = false;
  const dialogs: Dialogs = {
    confirm: async (o) => {
      confirms.push(o);
      return over.confirm ?? true;
    },
    alert: async (o) => void alerts.push(o),
  };
  const deps: TrayControllerDeps = {
    port: { update: (v) => void views.push(v), destroy: () => void (destroyed = true) },
    lang: () => over.lang ?? 'en',
    clock: time.clock,
    timers: time.timers,
    notifier: { notifyEvent: (e) => (notified.push(e.type), true), suggestSurvival: () => (suggested += 1, true) },
    modes: {
      canLeaveSurvival: false,
      setMode: async (target) => {
        modeCalls.push(target);
        return over.setMode ? over.setMode(target) : { ok: true, already: false };
      },
    },
    probe: async () => {
      probeCount += 1;
      return probes[Math.min(probeIndex++, probes.length - 1)] ?? 'down';
    },
    refreshStatus: async () => void (refreshCalls.n += 1),
    readMode: async () => modeInfo,
    windows: {
      showMain: () => void windows.push('main'),
      ...(over.openWindow ? { openWindow: (w) => void windows.push(w) } : {}),
    },
    dialogs,
    actions: { quit: () => void windows.push('quit'), ...over.actions },
  };
  const controller = new TrayController(deps);
  controller.start();
  const last = (): TrayView => views.at(-1)!;
  const labels = (view: TrayView = last()): string[] => {
    const flat = (items: MenuItemSpec[]): string[] => items.flatMap((i) => (i.type === 'submenu' ? [i.label, ...flat(i.items)] : 'label' in i ? [i.label] : []));
    return flat(view.menu);
  };
  return {
    controller,
    time,
    views,
    last,
    labels,
    notified,
    suggested: () => suggested,
    modeCalls,
    alerts,
    confirms,
    windows,
    refreshCalls,
    probeCount: () => probeCount,
    destroyed: () => destroyed,
    setModeInfo: (info: ModeInfo | null) => void (modeInfo = info),
    send: (m: FeedMessage) => controller.handle(m),
  };
}

describe('TrayController status', () => {
  it('starts grey "starting", then goes green with the model once connected and the status is read', () => {
    const t = setup();
    expect(t.last().colour).toBe('grey');
    expect(t.last().tooltip).toBe('Local AI: starting');
    t.send(conn('connected'));
    expect(t.last().colour).toBe('grey'); // connected, but nothing read yet
    t.send(stat(status()));
    expect(t.last().colour).toBe('green');
    expect(t.last().tooltip).toBe('Local AI: ready (online)');
    expect(t.labels()[0]).toBe('● Local AI: ready (online) · model-a');
  });

  it('asks for /status when the stream connects (the feed reads it only after a reconnect)', async () => {
    const t = setup();
    t.send(conn('connected'));
    await t.time.flush();
    expect(t.refreshCalls.n).toBe(1);
  });

  it('follows the mode of /status: offline is blue, survival is orange', () => {
    const t = setup();
    t.send(stat(status({ mode: { value: 'offline' } })));
    expect(t.last().colour).toBe('blue');
    t.send(stat(status({ mode: { value: 'survival' } })));
    expect(t.last().colour).toBe('orange');
    expect(t.last().tooltip).toBe('Local AI: survival mode');
  });

  it('mode.changed moves the light at once, announces itself and re-reads /status', async () => {
    const t = setup();
    t.send(stat(status()));
    t.send(ev('mode.changed', { from: 'online', to: 'offline' }));
    expect(t.last().colour).toBe('blue');
    expect(t.notified).toEqual(['mode.changed']);
    await t.time.flush();
    expect(t.refreshCalls.n).toBe(1);
    expect(t.controller.mode).toBe('offline');
  });

  it('alerts: warning is amber, critical is red, cleared goes back; info changes nothing', () => {
    const t = setup();
    t.send(stat(status()));
    t.send(ev('alert.raised', { code: 'LAI-SVC-013', severity: 'info', title: 'x', fix: 'y' }));
    expect(t.last().colour).toBe('green');
    t.send(ev('alert.raised', { code: 'LAI-HW-012', severity: 'warning', title: 'x', fix: 'y' }));
    expect(t.last().colour).toBe('amber');
    t.send(ev('alert.raised', { code: 'LAI-HW-001', severity: 'critical', title: 'x', fix: 'y' }));
    expect(t.last().colour).toBe('red');
    t.send(ev('alert.cleared', { code: 'LAI-HW-001', severity: 'critical' }));
    expect(t.last().colour).toBe('amber');
    t.send(ev('alert.cleared', { code: 'LAI-HW-012', severity: 'warning' }));
    expect(t.last().colour).toBe('green');
    expect(t.notified).toEqual(['alert.raised', 'alert.raised', 'alert.raised']);
  });

  it('alerts of a fresh /status replace the ones held', () => {
    const t = setup();
    t.send(ev('alert.raised', { code: 'LAI-HW-012', severity: 'warning' }));
    t.send(stat(status({ alerts: [] })));
    expect(t.last().colour).toBe('green');
    t.send(stat(status({ alerts: [{ code: 'LAI-HW-012', severity: 'warning' }] })));
    expect(t.last().colour).toBe('amber');
  });

  it('a model that failed to load is amber until it loads', () => {
    const t = setup();
    t.send(stat(status()));
    t.send(ev('model.failed', { model: 'model-b', code: 'LAI-LLM-001' }));
    expect(t.last().colour).toBe('amber');
    t.send(ev('model.loaded', { model: 'model-b', load_ms: 5 }));
    expect(t.last().colour).toBe('green');
    expect(t.notified).toEqual(['model.failed']);
  });

  it('sync, drive events announce themselves and leave the light alone', () => {
    const t = setup();
    t.send(stat(status()));
    for (const type of ['sync.done', 'drive.mounted', 'drive.unmounted']) t.send(ev(type, {}));
    expect(t.notified).toEqual(['sync.done', 'drive.mounted', 'drive.unmounted']);
    expect(t.last().colour).toBe('green');
  });

  it('an event the agent replays after a reconnect, older than 5 minutes, is history and does not notify', () => {
    const t = setup();
    t.send(stat(status()));
    t.send(ev('drive.mounted', {}, new Date(NOW - 6 * 60_000).toISOString()));
    t.send(ev('drive.unmounted', {}, new Date(NOW - 60_000).toISOString()));
    expect(t.notified).toEqual(['drive.unmounted']);
  });

  it('connectivity none for 30 minutes while online turns amber when the clock ticks, with no event', async () => {
    const since = new Date(NOW - 20 * 60_000).toISOString();
    const t = setup();
    t.send(conn('connected'));
    t.send(stat(status({ connectivity: { value: 'none', none_since: since } })));
    expect(t.last().colour).toBe('green');
    await t.time.advance(9 * 60_000);
    expect(t.last().colour).toBe('green');
    await t.time.advance(2 * 60_000);
    expect(t.last().colour).toBe('amber');
  });

  it('the tick re-reads /status while connected (connectivity has no event), and not when the agent is gone', async () => {
    const t = setup({ probes: ['down'] });
    t.send(conn('connected'));
    t.send(stat(status()));
    await t.time.flush();
    const before = t.refreshCalls.n;
    await t.time.advance(60_000);
    expect(t.refreshCalls.n).toBe(before + 1);
    t.send(conn('reconnecting'));
    await t.time.flush();
    const mid = t.refreshCalls.n;
    await t.time.advance(120_000);
    expect(t.refreshCalls.n).toBe(mid);
  });

  it('does not push an identical view twice (an open menu would close)', () => {
    const t = setup();
    t.send(stat(status()));
    const count = t.views.length;
    t.send(stat(status()));
    t.send(ev('sync.done', {}));
    expect(t.views).toHaveLength(count);
  });

  it('the header and tooltip follow the UI language', () => {
    const t = setup({ lang: 'es' });
    t.send(stat(status()));
    expect(t.last().tooltip).toBe('Local AI: listo (en línea)');
    expect(t.labels()).toContain('Abrir Local AI');
  });
});

describe('TrayController agent restarts and loss (desktop.md 5)', () => {
  it('SSE drop: grey "starting" at once, then red if /health says the agent is gone within the probe interval, with "Start agent" offered', async () => {
    const t = setup({ probes: ['down'], actions: { startAgent: () => undefined } });
    t.send(conn('connected'));
    t.send(stat(status()));
    t.send(conn('reconnecting'));
    expect(t.last().colour).toBe('grey');
    await t.time.flush();
    expect(t.last().colour).toBe('red');
    expect(t.last().tooltip).toBe('Local AI: not working, click for help');
    expect(t.labels()).toContain('Start agent');
  });

  it('agent shutting down for a restart: grey, grey while /health says "starting", green again once the stream is back', async () => {
    const t = setup({ probes: ['down', 'starting', 'starting'] });
    t.send(conn('connected'));
    t.send(stat(status()));
    t.send(conn('shutdown'));
    expect(t.last().colour).toBe('grey');
    await t.time.flush();
    expect(t.last().colour).toBe('red');
    await t.time.advance(3000);
    expect(t.last().colour).toBe('grey');
    t.send(conn('connected'));
    t.send(stat(status()));
    expect(t.last().colour).toBe('green');
  });

  it('stops probing once the stream is connected', async () => {
    const t = setup({ probes: ['starting'] });
    t.send(conn('reconnecting'));
    await t.time.advance(10_000);
    expect(t.probeCount()).toBeGreaterThanOrEqual(3);
    t.send(conn('connected'));
    t.send(stat(status()));
    await t.time.advance(1000);
    const after = t.probeCount();
    await t.time.advance(30_000);
    expect(t.probeCount()).toBe(after);
  });

  it('the failed-model amber does not survive an agent restart', async () => {
    const t = setup({ probes: ['starting'] });
    t.send(conn('connected'));
    t.send(stat(status()));
    t.send(ev('model.failed', { model: 'model-b' }));
    expect(t.last().colour).toBe('amber');
    t.send(conn('reconnecting'));
    t.send(conn('connected'));
    t.send(stat(status()));
    expect(t.last().colour).toBe('green');
  });

  it('a token the agent will not take: red, no "Start agent" (the agent is there)', async () => {
    const t = setup({ probes: ['up'], actions: { startAgent: () => undefined } });
    t.send(conn('unauthorized'));
    await t.time.flush();
    expect(t.last().colour).toBe('red');
    expect(t.labels()).not.toContain('Start agent');
  });

  it('start-up ended on an error page: red; "Start agent" only when the agent is down', async () => {
    const down = setup({ probes: ['down'], actions: { startAgent: () => undefined } });
    down.controller.startupFailed('down');
    expect(down.last().colour).toBe('red');
    expect(down.labels()).toContain('Start agent');
    const blocked = setup({ probes: ['up'], actions: { startAgent: () => undefined } });
    blocked.controller.startupFailed('blocked');
    await blocked.time.flush();
    expect(blocked.last().colour).toBe('red');
    expect(blocked.labels()).not.toContain('Start agent');
  });

  it('red clears by itself when the agent comes back after a failed start-up', async () => {
    const t = setup({ probes: ['down', 'down', 'up'] });
    t.controller.startupFailed('down');
    await t.time.advance(3000);
    t.send(conn('connected'));
    t.send(stat(status()));
    expect(t.last().colour).toBe('green');
  });
});

describe('TrayController suggestion (modes.md 4)', () => {
  it('notifies once when /mode.suggest_survival turns true, and again only after it was false', async () => {
    const noneStatus = status({ connectivity: { value: 'none', none_since: new Date(NOW - 31 * 60_000).toISOString() } });
    const t = setup({ modeInfo: { mode: 'online', suggestSurvival: true } });
    t.send(stat(noneStatus));
    await t.time.flush();
    expect(t.suggested()).toBe(1);
    t.send(stat(noneStatus));
    await t.time.flush();
    expect(t.suggested()).toBe(1);
    t.setModeInfo({ mode: 'online', suggestSurvival: false });
    t.send(stat(noneStatus));
    await t.time.flush();
    t.setModeInfo({ mode: 'online', suggestSurvival: true });
    t.send(stat(noneStatus));
    await t.time.flush();
    expect(t.suggested()).toBe(2);
  });

  it('does not ask /mode while connectivity is fine, and never changes the mode', async () => {
    const t = setup({ modeInfo: { mode: 'online', suggestSurvival: true } });
    t.send(stat(status()));
    await t.time.flush();
    expect(t.suggested()).toBe(0);
    expect(t.modeCalls).toEqual([]);
  });

  it('the agent raising LAI-NET-011 triggers the check', async () => {
    const t = setup({ modeInfo: { mode: 'online', suggestSurvival: true } });
    t.send(stat(status()));
    t.send(ev('alert.raised', { code: 'LAI-NET-011', severity: 'info', title: 'No internet', fix: 'x' }));
    await t.time.flush();
    expect(t.suggested()).toBe(1);
    expect(t.modeCalls).toEqual([]);
  });

  it('an unreadable /mode is no suggestion', async () => {
    const t = setup({ modeInfo: null });
    t.send(stat(status({ connectivity: { value: 'none', none_since: new Date(NOW - 31 * 60_000).toISOString() } })));
    await t.time.flush();
    expect(t.suggested()).toBe(0);
  });
});

describe('TrayController menu actions', () => {
  it('Open brings the main window forward; Chat, Library and Maps ask the window manager', async () => {
    const t = setup({ openWindow: true });
    await t.controller.invoke('open');
    await t.controller.invoke('window:chat');
    await t.controller.invoke('window:library');
    await t.controller.invoke('window:maps');
    expect(t.windows).toEqual(['main', 'chat', 'library', 'maps']);
    t.controller.activate();
    expect(t.windows.at(-1)).toBe('main');
  });

  it('chat, library and maps are not in the menu until the window manager can open them', () => {
    expect(setup().labels()).not.toContain('Chat');
    expect(setup({ openWindow: true }).labels()).toContain('Chat');
  });

  it('Online and Offline switch the mode without a confirmation', async () => {
    const t = setup();
    await t.controller.invoke('mode:offline');
    await t.controller.invoke('mode:online');
    expect(t.modeCalls).toEqual(['offline', 'online']);
    expect(t.confirms).toEqual([]);
    expect(t.alerts).toEqual([]);
  });

  it('Survival asks once, in the UI language, then switches; a "no" switches nothing', async () => {
    const yes = setup({ confirm: true });
    await yes.controller.invoke('survival');
    expect(yes.confirms).toHaveLength(1);
    expect(yes.confirms[0]!.message).toBe('Switch to Survival mode? Everything you need offline will start.');
    expect(yes.modeCalls).toEqual(['survival']);

    const no = setup({ confirm: false, lang: 'es' });
    await no.controller.invoke('survival');
    expect(no.confirms[0]!.message).toBe('¿Cambiar a modo supervivencia? Se iniciará todo lo necesario sin internet.');
    expect(no.modeCalls).toEqual([]);
  });

  it('a refused mode change shows the agent\'s code and fix in a native dialog', async () => {
    const t = setup({ setMode: async () => ({ ok: false, code: 'LAI-AGT-022', message: 'Another operation is already running', fix: 'Wait for it to finish.' }) });
    await t.controller.invoke('mode:offline');
    expect(t.alerts).toHaveLength(1);
    expect(t.alerts[0]!.message).toBe('Another operation is already running');
    expect(t.alerts[0]!.detail).toContain('LAI-AGT-022');
    expect(t.alerts[0]!.detail).toContain('Wait for it to finish.');
    expect(t.alerts[0]!.okLabel).toBe('OK');
  });

  it('a cancelled PIN prompt shows no error', async () => {
    const t = setup({ setMode: async () => ({ ok: false, code: 'LAI-SEC-002', cancelled: true }) });
    await t.controller.invoke('mode:offline');
    expect(t.alerts).toEqual([]);
  });

  it('Copy API key, connection info, Lab and Start agent call their owners', async () => {
    const calls: string[] = [];
    const t = setup({
      actions: {
        copyApiKey: () => void calls.push('copy'),
        showConnectionInfo: async () => void calls.push('info'),
        openLab: () => void calls.push('lab'),
        labConnected: () => true,
        startAgent: () => void calls.push('start'),
      },
    });
    t.send(stat(status()));
    expect(t.labels()).toContain('Lab (ai.marin.cr)');
    for (const a of ['copy-key', 'connection-info', 'lab', 'start-agent'] as const) await t.controller.invoke(a);
    expect(calls).toEqual(['copy', 'info', 'lab', 'start']);
  });

  it('the Lab item needs both the action and a connection', () => {
    expect(setup({ actions: { openLab: () => undefined, labConnected: () => false } }).labels()).not.toContain('Lab (ai.marin.cr)');
    expect(setup({ actions: { openLab: () => undefined } }).labels()).not.toContain('Lab (ai.marin.cr)');
  });

  it('Start at login toggles through its owner and redraws the check mark', async () => {
    let on = true;
    const t = setup({ actions: { autostart: { get: () => on, set: (next) => void (on = next) } } });
    const check = (): boolean => (t.last().menu.find((i) => i.type === 'check') as { checked: boolean }).checked;
    expect(check()).toBe(true);
    await t.controller.invoke('autostart');
    expect(on).toBe(false);
    expect(check()).toBe(false);
  });

  it('Quit quits', async () => {
    const t = setup();
    await t.controller.invoke('quit');
    expect(t.windows).toEqual(['quit']);
  });

  it('an action that throws is logged and does not escape', async () => {
    const t = setup({ actions: { copyApiKey: () => { throw new Error('boom'); } } });
    await expect(t.controller.invoke('copy-key')).resolves.toBeUndefined();
  });

  it('dispose destroys the tray and ignores later messages', () => {
    const t = setup();
    t.controller.dispose();
    const count = t.views.length;
    t.send(stat(status()));
    expect(t.destroyed()).toBe(true);
    expect(t.views).toHaveLength(count);
  });
});
