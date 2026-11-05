// The Lab view's controller (src/renderer/lab/model.ts): refresh, the unreachable banner and its retry schedule on a fake
// clock, nothing running while the tab is hidden, command polling every 2 s, and the words for every refusal.
import { describe, expect, it, vi } from 'vitest';
import {
  COMMAND_POLL_MS,
  type Clock,
  LabController,
  type LabBridge,
  REFRESH_MS,
  RETRY_MAX_MS,
  clockLabel,
  connectivityBlocks,
  refusalText,
  retryDelayMs,
  statusText,
} from '../../src/renderer/lab/model';
import { STRINGS, fill } from '../../src/renderer/lab/strings';
import type { CloudResult, DesktopStatus, LabCacheView } from '../../src/shared/types';
import fixture from '../fixtures/lab-3-nodes.json';

const COMMAND_ID = '6b3c1f0e-4a2d-4c8b-9e51-0a7d2b9c3e44';

class FakeClock implements Clock {
  time = Date.parse('2026-11-03T14:05:00.000Z');
  #next = 1;
  readonly timers = new Map<number, { at: number; fn: () => void }>();
  now = (): number => this.time;
  set = (fn: () => void, ms: number): unknown => {
    this.timers.set(this.#next, { at: this.time + ms, fn });
    return this.#next++;
  };
  clear = (handle: unknown): void => void this.timers.delete(handle as number);
  /** The delays of the timers pending now. */
  delays(): number[] {
    return [...this.timers.values()].map((t) => t.at - this.time).sort((a, b) => a - b);
  }
  /** Moves time forward, firing due timers in order; every timer fired is awaited through the microtask queue. */
  async advance(ms: number): Promise<void> {
    const end = this.time + ms;
    for (;;) {
      const due = [...this.timers.entries()].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) break;
      this.timers.delete(due[0]);
      this.time = due[1].at;
      due[1].fn();
      await flush();
    }
    this.time = end;
  }
}

const flush = async (): Promise<void> => {
  for (let i = 0; i < 20; i += 1) await Promise.resolve();
};

const okNodes = (): CloudResult => ({ ok: true, status: 200, body: { data: fixture } });
const cachedView = (): LabCacheView => ({ savedAt: '2026-11-03T10:30:00.000Z', summary: fixture.summary, nodes: fixture.nodes as unknown as LabCacheView['nodes'] });

function status(over: Partial<DesktopStatus> = {}): DesktopStatus {
  return {
    version: '0.1.0',
    agent: 'up',
    mode: 'online',
    connectivity: 'full',
    alerts: 0,
    cloud: { linked: true, tokenHeld: true, url: 'https://ai.marin.cr', user: 'Person A' },
    connected: true,
    tab: 'lab',
    ...over,
  };
}

function setup(handler: (req: { method: string; path: string; body?: unknown }) => CloudResult | Promise<CloudResult>, cached: LabCacheView | null = null) {
  const clock = new FakeClock();
  const listeners = new Set<(s: DesktopStatus) => void>();
  const request = vi.fn(async (req: { method: 'GET' | 'POST' | 'DELETE'; path: string; body?: unknown }) => handler(req));
  const bridge: LabBridge = {
    status: async () => current,
    onStatus: (cb) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    cloud: { request, cached: vi.fn(async () => cached), openNode: vi.fn() },
  };
  let current = status();
  const controller = new LabController(bridge, 'en', clock);
  const push = async (over: Partial<DesktopStatus>): Promise<void> => {
    current = status(over);
    listeners.forEach((l) => l(current));
    await flush();
  };
  const calls = (): string[] => request.mock.calls.map(([r]) => `${r.method} ${r.path}`);
  return { controller, clock, request, bridge, push, calls, state: () => controller.getState() };
}

describe('retry schedule', () => {
  it('10 s, 20 s, 40 s, 80 s, 160 s, then 5 minutes for ever', () => {
    expect([0, 1, 2, 3, 4, 5, 6, 7, 20].map(retryDelayMs)).toEqual([10_000, 20_000, 40_000, 80_000, 160_000, 300_000, 300_000, 300_000, 300_000]);
    expect(RETRY_MAX_MS).toBe(300_000);
    expect(retryDelayMs(-3)).toBe(10_000);
  });

  it('connectivity none or limited blocks; full, not_checked and unknown do not', () => {
    expect(['none', 'limited'].map(connectivityBlocks)).toEqual([true, true]);
    expect(['full', 'not_checked', null].map(connectivityBlocks)).toEqual([false, false, false]);
  });
});

describe('loading the cards', () => {
  it('a shown tab loads the nodes at once, shows them, and refreshes every 15 s', async () => {
    const t = setup(okNodes);
    await t.controller.start();
    await flush();
    expect(t.calls()).toEqual(['GET /api/localai/nodes']);
    expect(t.state()).toMatchObject({ loaded: true, unreachable: false, fromCache: false, user: 'Person A', connected: true, visible: true });
    expect(t.state().nodes.map((n) => n.nodeId)).toEqual(['ai-n1', 'ai-n2', 'ai-n3']);
    expect(t.state().summary).toEqual(fixture.summary);
    expect(t.state().dataAt).toBe('2026-11-03T14:05:00.000Z');
    expect(t.clock.delays()).toEqual([REFRESH_MS]);
    await t.clock.advance(REFRESH_MS);
    expect(t.calls()).toHaveLength(2);
  });

  it('a hidden tab makes no request at all, and showing it loads', async () => {
    const t = setup(okNodes);
    await t.controller.start();
    await t.push({ tab: 'local' });
    t.request.mockClear();
    await t.clock.advance(10 * 60_000);
    expect(t.request).not.toHaveBeenCalled();
    expect(t.clock.timers.size).toBe(0);
    await t.push({ tab: 'lab' });
    expect(t.calls()).toEqual(['GET /api/localai/nodes']);
  });

  it('a window that is hidden (minimised) counts as a hidden tab', async () => {
    const t = setup(okNodes);
    await t.controller.start();
    await flush();
    t.controller.setDocumentVisible(false);
    expect(t.clock.timers.size).toBe(0);
    t.request.mockClear();
    await t.clock.advance(60_000);
    expect(t.request).not.toHaveBeenCalled();
    t.controller.setDocumentVisible(true);
    await flush();
    expect(t.request).toHaveBeenCalledTimes(1);
  });

  it('when connected mode is off nothing is asked, and the state says so', async () => {
    const t = setup(okNodes);
    await t.controller.start();
    await t.push({ connected: false, mode: 'offline' });
    t.request.mockClear();
    await t.clock.advance(60_000);
    expect(t.request).not.toHaveBeenCalled();
    expect(t.state().connected).toBe(false);
    await t.push({ connected: true });
    expect(t.request).toHaveBeenCalledTimes(1);
  });

  it('an answer that is not the node list is "unexpected" and unreachable', async () => {
    const t = setup(() => ({ ok: true, status: 200, body: { data: 'x' } }));
    await t.controller.start();
    await flush();
    expect(t.state()).toMatchObject({ problem: 'unexpected', unreachable: true });
  });

  it('a 403 says the role cannot see the lab and does not show the unreachable banner', async () => {
    const t = setup(() => ({ ok: false, status: 403, body: {} }));
    await t.controller.start();
    await flush();
    expect(t.state()).toMatchObject({ problem: 'forbidden', unreachable: false, loaded: true });
    expect(t.controller.bannerText()).toBeNull();
  });

  it('a 401 (the link was removed) stops everything', async () => {
    const t = setup(() => ({ ok: false, error: 'unauthorized' }));
    await t.controller.start();
    await flush();
    expect(t.state().problem).toBe('removed');
    expect(t.clock.timers.size).toBe(0);
  });
});

describe('the cloud is unreachable', () => {
  it.each<[string, CloudResult]>([
    ['a network error', { ok: false, error: 'unreachable' }],
    ['a timeout', { ok: false, error: 'timeout' }],
    ['a 503', { ok: false, status: 503, body: {} }],
    ['a 500', { ok: false, status: 500, body: null }],
  ])('%s: the cached cards, the banner with their time, and command buttons off', async (_name, failure) => {
    const t = setup(() => failure, cachedView());
    await t.controller.start();
    await flush();
    const s = t.state();
    expect(s).toMatchObject({ unreachable: true, fromCache: true, dataAt: '2026-11-03T10:30:00.000Z', loaded: true });
    expect(s.nodes).toHaveLength(3);
    expect(t.controller.canAct()).toBe(false);
    const banner = t.controller.bannerText(new Date('2026-11-03T14:05:00.000Z'), 'UTC');
    expect(banner).toBe("Can't reach the lab right now. Showing information from 10:30. This computer keeps working normally.");
  });

  it('with no cache the banner says there is nothing saved yet', async () => {
    const t = setup(() => ({ ok: false, error: 'unreachable' }), null);
    await t.controller.start();
    await flush();
    expect(t.state()).toMatchObject({ unreachable: true, nodes: [], fromCache: false });
    expect(t.controller.bannerText()).toBe(STRINGS.unreachableNoData.en);
  });

  it('retries after 10 s, 20 s, 40 s, 80 s, 160 s and then every 5 minutes', async () => {
    const t = setup(() => ({ ok: false, error: 'unreachable' }), cachedView());
    await t.controller.start();
    await flush();
    expect(t.clock.delays()).toEqual([10_000]);
    const seen: number[] = [];
    for (let i = 0; i < 8; i += 1) {
      const [next] = t.clock.delays();
      seen.push(next!);
      await t.clock.advance(next!);
    }
    expect(seen).toEqual([10_000, 20_000, 40_000, 80_000, 160_000, 300_000, 300_000, 300_000]);
    expect(t.request).toHaveBeenCalledTimes(9);
  });

  it('retries only while the tab is visible: hiding it stops the clock, showing it tries at once and starts the schedule over', async () => {
    const t = setup(() => ({ ok: false, error: 'unreachable' }), cachedView());
    await t.controller.start();
    await flush();
    await t.clock.advance(10_000);
    await t.clock.advance(20_000);
    expect(t.request).toHaveBeenCalledTimes(3);
    expect(t.clock.delays()).toEqual([40_000]);

    await t.push({ tab: 'local' });
    expect(t.clock.timers.size).toBe(0);
    t.request.mockClear();
    await t.clock.advance(60 * 60_000);
    expect(t.request).not.toHaveBeenCalled();

    await t.push({ tab: 'lab' });
    expect(t.request).toHaveBeenCalledTimes(1);
    expect(t.clock.delays()).toEqual([10_000]); // the schedule started over
  });

  it('recovers: the first good answer clears the banner and shows live data', async () => {
    let up = false;
    const t = setup(() => (up ? okNodes() : { ok: false, error: 'unreachable' }), cachedView());
    await t.controller.start();
    await flush();
    expect(t.state().unreachable).toBe(true);
    up = true;
    await t.clock.advance(10_000);
    expect(t.state()).toMatchObject({ unreachable: false, fromCache: false, dataAt: new Date(t.clock.time).toISOString() });
    expect(t.controller.bannerText()).toBeNull();
    expect(t.controller.canAct()).toBe(true);
    expect(t.clock.delays()).toEqual([REFRESH_MS]);
  });

  it.each(['none', 'limited'])('connectivity %s: the cache and the banner without asking the cloud, and the internet coming back is tried at once', async (connectivity) => {
    const t = setup(okNodes, cachedView());
    await t.push({ connectivity });
    await t.controller.start();
    await flush();
    expect(t.request).not.toHaveBeenCalled();
    expect(t.state()).toMatchObject({ unreachable: true, fromCache: true });
    await t.clock.advance(10_000);
    expect(t.request).not.toHaveBeenCalled(); // still no internet
    await t.push({ connectivity: 'full' });
    expect(t.calls()).toEqual(['GET /api/localai/nodes']);
    expect(t.state()).toMatchObject({ unreachable: false, fromCache: false });
  });

  it('live cards that are already on screen stay when the cloud drops, with the time they were fetched', async () => {
    let up = true;
    const t = setup(() => (up ? okNodes() : { ok: false, error: 'timeout' }), cachedView());
    await t.controller.start();
    await flush();
    const fetched = t.state().dataAt;
    up = false;
    await t.clock.advance(REFRESH_MS);
    expect(t.state()).toMatchObject({ unreachable: true, fromCache: false, dataAt: fetched });
    expect(t.bridge.cloud.cached).not.toHaveBeenCalled();
  });

  it('clockLabel is the time of day, with the date when the data is from another day, in both languages', () => {
    const now = new Date('2026-11-03T14:05:00.000Z');
    expect(clockLabel('2026-11-03T09:05:00.000Z', 'en', now, 'UTC')).toBe('09:05');
    expect(clockLabel('2026-11-03T09:05:00.000Z', 'es', now, 'UTC')).toBe('09:05');
    expect(clockLabel('2026-11-01T09:05:00.000Z', 'en', now, 'UTC')).toMatch(/^Sun.*Nov.*09:05$/);
    expect(clockLabel('garbage', 'en', now)).toBe('');
    expect(fill(STRINGS.unreachable.es, { time: '14:05' })).toBe('No se puede conectar con el laboratorio. Mostrando información de las 14:05. Esta computadora sigue funcionando normalmente.');
  });
});

describe('commands', () => {
  const view = (status: string, extra: Record<string, unknown> = {}) => ({ ok: true, status: 200, body: { data: { id: COMMAND_ID, nodeId: 'ai-n2', type: 'localai.model.load', status, ...extra } } }) satisfies CloudResult;

  async function ready(commandReads: CloudResult[]) {
    let reads = 0;
    const t = setup((req) => {
      if (req.method === 'POST') return { ok: true, status: 201, body: { data: { id: COMMAND_ID, nodeId: 'ai-n2', type: 'localai.model.load', status: 'queued' } } };
      if (req.path === `/api/localai/commands/${COMMAND_ID}`) return commandReads[Math.min(reads++, commandReads.length - 1)]!;
      return okNodes();
    });
    await t.controller.start();
    await flush();
    t.request.mockClear();
    return t;
  }

  it('Load queues model.load, shows Waiting…, polls every 2 s through Running… to Done, then reads the cards again', async () => {
    const t = await ready([view('queued'), view('running'), view('succeeded')]);
    const spec = { type: 'model.load', params: { model_id: 'qwen3.6-35b-a3b-q4km', unload_others: true }, param: 'qwen3.6-35b-a3b-q4km' } as const;
    await t.controller.send('ai-n2', spec);
    expect(t.request).toHaveBeenCalledWith({ method: 'POST', path: '/api/localai/nodes/ai-n2/commands', body: { type: 'model.load', params: spec.params } });
    expect(t.state().commands['ai-n2']).toMatchObject({ status: 'queued', text: 'Waiting…', id: COMMAND_ID });
    expect(t.controller.isBusy('ai-n2')).toBe(true);
    expect(t.clock.delays()).toContain(COMMAND_POLL_MS);

    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.state().commands['ai-n2']).toMatchObject({ status: 'queued', text: 'Waiting…' });
    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.state().commands['ai-n2']).toMatchObject({ status: 'running', text: 'Running…' });
    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.state().commands['ai-n2']).toMatchObject({ status: 'succeeded', text: 'Done' });
    expect(t.controller.isBusy('ai-n2')).toBe(false);

    const polls = t.calls().filter((c) => c.includes('/commands/'));
    expect(polls).toEqual(Array(3).fill(`GET /api/localai/commands/${COMMAND_ID}`));
    // The cards were read again when it settled, and it stops polling once settled.
    expect(t.calls().filter((c) => c === 'GET /api/localai/nodes')).toHaveLength(1);
    t.request.mockClear();
    await t.clock.advance(COMMAND_POLL_MS * 3);
    expect(t.calls().filter((c) => c.includes('/commands/'))).toEqual([]);
  });

  it('the cards are read again as soon as a command settles', async () => {
    const t = await ready([view('succeeded')]);
    await t.controller.send('ai-n2', { type: 'status.refresh', params: {} });
    t.request.mockClear();
    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.calls()).toEqual([`GET /api/localai/commands/${COMMAND_ID}`, 'GET /api/localai/nodes']);
  });

  it('a failed command shows the cloud\'s message and doctor code', async () => {
    const t = await ready([view('failed', { error: { code: 'LAI-CAT-005', message: 'Model does not fit in free VRAM' } })]);
    await t.controller.send('ai-n2', { type: 'model.load', params: { model_id: 'big-model-q8' }, param: 'big-model-q8' });
    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.state().commands['ai-n2']).toMatchObject({ status: 'failed', text: 'Failed: Model does not fit in free VRAM (LAI-CAT-005)' });
  });

  it('polling pauses while the tab is hidden and goes on when it is shown', async () => {
    const t = await ready([view('running'), view('succeeded')]);
    await t.controller.send('ai-n2', { type: 'ark.verify', params: {} });
    await t.push({ tab: 'local' });
    t.request.mockClear();
    await t.clock.advance(60_000);
    expect(t.request).not.toHaveBeenCalled();
    await t.push({ tab: 'lab' });
    await t.clock.advance(COMMAND_POLL_MS);
    expect(t.calls()).toContain(`GET /api/localai/commands/${COMMAND_ID}`);
  });

  it('a second command for a busy card is ignored, and nothing is sent while the cloud is unreachable or the data is cached', async () => {
    const t = await ready([view('running')]);
    await t.controller.send('ai-n2', { type: 'ark.verify', params: {} });
    t.request.mockClear();
    await t.controller.send('ai-n2', { type: 'ark.sync', params: {} });
    expect(t.request).not.toHaveBeenCalled();

    const down = setup(() => ({ ok: false, error: 'unreachable' }), cachedView());
    await down.controller.start();
    await flush();
    down.request.mockClear();
    await down.controller.send('ai-n2', { type: 'status.refresh', params: {} });
    expect(down.request).not.toHaveBeenCalled();
  });

  it.each<[string, CloudResult, string]>([
    ['403', { ok: false, status: 403, body: { message: 'You are not allowed to send this command' } }, "Your lab role can't do this."],
    ['409 LAI-CLD-035', { ok: false, status: 409, body: { error: { details: { code: 'LAI-CLD-035', reason: 'node_retired' } } } }, STRINGS.refusedNotEnrolled.en],
    ['422 LAI-CLD-034 offline', { ok: false, status: 422, body: { error: { details: { code: 'LAI-CLD-034', mode: 'offline' } } } }, STRINGS.refusedOffline.en],
    ['422 LAI-CLD-034 survival', { ok: false, status: 422, body: { details: { code: 'LAI-CLD-034', mode: 'survival' } } }, STRINGS.refusedSurvival.en],
    ['422 model not on the node', { ok: false, status: 422, body: { details: { reason: 'model_not_on_node' } } }, STRINGS.refusedModel.en],
    ['400', { ok: false, status: 400, body: {} }, STRINGS.refusedGeneric.en],
    ['404', { ok: false, status: 404, body: {} }, STRINGS.refusedGeneric.en],
    ['503', { ok: false, status: 503, body: {} }, STRINGS.refusedUnreachable.en],
    ['a network error', { ok: false, error: 'unreachable' }, STRINGS.refusedUnreachable.en],
  ])('a refusal: %s is told in plain words', async (_name, refusal, text) => {
    const t = setup((req) => (req.method === 'POST' ? refusal : okNodes()));
    await t.controller.start();
    await flush();
    await t.controller.send('ai-n2', { type: 'model.load', params: { model_id: 'qwen3.5-9b-q4km' } });
    expect(t.state().commands['ai-n2']).toMatchObject({ status: 'refused', text });
    expect(t.controller.isBusy('ai-n2')).toBe(false);
    expect(refusalText(refusal, 'en')).toBe(text);
  });

  it('the refusals exist in Spanish too, with their codes', () => {
    expect(refusalText({ ok: false, status: 422, body: { details: { code: 'LAI-CLD-034', mode: 'offline' } } }, 'es')).toContain('LAI-CLD-034');
    expect(refusalText({ ok: false, status: 409, body: { details: { code: 'LAI-CLD-035' } } }, 'es')).toContain('LAI-CLD-035');
    expect(refusalText({ ok: false, status: 403, body: {} }, 'es')).toBe('Tu rol en el laboratorio no puede hacer esto.');
  });

  it('statusText covers every status in both languages', () => {
    for (const status of ['queued', 'running', 'succeeded', 'failed', 'expired', 'cancelled'] as const) {
      for (const lang of ['en', 'es'] as const) expect(statusText({ status, errorCode: null, errorMessage: null }, lang)).not.toBe('');
    }
    expect(statusText({ status: 'expired', errorCode: null, errorMessage: null }, 'en')).toContain('LAI-CLD-030');
  });

  it('the models of a node are read when its panel opens, and Load is offered only for a loadable one', async () => {
    const t = setup((req) =>
      req.path === '/api/localai/nodes/ai-n2'
        ? { ok: true, status: 200, body: { data: { lastState: { models: [{ id: 'qwen3.5-9b-q4km', present: true, loadable: true, status: 'unloaded', default: false }, { id: 'big-model-q8', present: false, loadable: false, status: 'unloaded', default: false }] } } } }
        : okNodes(),
    );
    await t.controller.start();
    await flush();
    t.controller.toggleModels('ai-n2');
    expect(t.state().models['ai-n2']).toMatchObject({ open: true, state: 'loading' });
    await flush();
    expect(t.state().models['ai-n2']).toMatchObject({ open: true, state: 'ready' });
    expect(t.state().models['ai-n2']!.models.map((m) => [m.id, m.loadable])).toEqual([['qwen3.5-9b-q4km', true], ['big-model-q8', false]]);
    t.controller.toggleModels('ai-n2');
    expect(t.state().models['ai-n2']!.open).toBe(false);
  });

  it('openNode hands the id to the main process, which builds the address', () => {
    const t = setup(okNodes);
    t.controller.openNode('ai-n2');
    expect(t.bridge.cloud.openNode).toHaveBeenCalledWith('ai-n2');
  });

  it('stop() ends the timers and the subscription', async () => {
    const t = setup(okNodes);
    await t.controller.start();
    await flush();
    t.controller.stop();
    expect(t.clock.timers.size).toBe(0);
    t.request.mockClear();
    await t.push({ tab: 'lab' });
    expect(t.request).not.toHaveBeenCalled();
  });
});
