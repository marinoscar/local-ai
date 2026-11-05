import { Logger } from '@nestjs/common';

import { LOCALAI_NODE_KEY_REPORTED_EVENT } from '../../keys/node-key.events';
import { LOCALAI_NODE_STATE_CHANNEL } from '../../node-link/state.service';
import type { SourceNodeRow } from './build-candidates';
import { FakeBus, FakeClock, model, node } from './routing.fixtures';
import type { RoutingSource } from './routing-source';
import { RoutingTable, routingOptionsFromEnv } from './routing-table';
import type { SelectedCandidate, SelectResult } from './routing.types';

beforeAll(() => {
  jest.spyOn(Logger.prototype, 'debug').mockImplementation(() => undefined);
  jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
});
afterAll(() => jest.restoreAllMocks());

class FakeSource implements RoutingSource {
  rows: SourceNodeRow[] = [];
  queueFactor = 4;
  loads = 0;
  failNext = false;
  /** When set, `load` waits for it: lets a test trigger events while a rebuild is in flight. */
  gate: Promise<void> | null = null;
  async load() {
    this.loads += 1;
    const snapshot = this.rows.map((row) => ({ ...row }));
    if (this.gate) await this.gate;
    if (this.failNext) {
      this.failNext = false;
      throw new Error('database down');
    }
    return { rows: snapshot, settings: { queueFactor: this.queueFactor } };
  }
}

function setup(timers = false) {
  const source = new FakeSource();
  const bus = new FakeBus();
  const clock = new FakeClock();
  const table = new RoutingTable(source, bus as never, clock, { timers, rebuildIntervalMs: 10_000 });
  table.onModuleInit();
  return { source, bus, clock, table };
}

const nodesOf = (result: SelectResult): string[] => (result.kind === 'candidates' ? result.candidates.map((c) => c.nodeId) : [result.kind]);
const ref = (nodeId: string, modelId = 'model-a') => ({ nodeId, modelId });
const tick = (): Promise<void> => new Promise((resolve) => setImmediate(resolve));

describe('RoutingTable: rebuild and selection', () => {
  it('starts empty, then selects after a rebuild', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    expect(table.select('model-a')).toEqual({ kind: 'none_loaded' });
    await table.rebuild();
    expect(nodesOf(table.select('model-a'))).toEqual(['ai-n1']);
    expect(table.routableNodeCount()).toBe(1);
    expect(table.routableNodes()).toEqual([{ nodeId: 'ai-n1', publicHost: 'ai-n1.example.test' }]);
  });

  it('keeps the previous table when a rebuild fails', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.rebuild();
    source.failNext = true;
    source.rows = [];
    await expect(table.rebuild()).resolves.toBeUndefined();
    expect(table.routableNodeCount()).toBe(1);
  });

  it('hands out snapshots that carry the live counters', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.rebuild();
    table.acquire(ref('ai-n1'));
    table.recordTtfb(ref('ai-n1'), 400);
    const result = table.select('model-a');
    expect(result.kind === 'candidates' && result.candidates[0]).toMatchObject({ inflight: 1, ewmaTtfbMs: 400, suspectUntil: 0 });
  });

  it('reads gateway.queueFactor from the source on each rebuild', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a', { slots: 1 })])];
    await table.rebuild();
    for (let i = 0; i < 4; i += 1) table.acquire(ref('ai-n1'));
    expect(table.select('model-a')).toEqual({ kind: 'saturated' });
    source.queueFactor = 5;
    await table.rebuild();
    expect(table.select('model-a').kind).toBe('candidates');
  });

  it('answers 503 LAI-GW-021 or 404 LAI-GW-020 for a model nothing serves', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-b', { status: 'unloaded' })], { mode: 'offline' })];
    await table.rebuild();
    expect(table.unavailable('model-b', true)).toEqual({ status: 503, code: 'LAI-GW-021' });
    expect(table.unavailable('model-b', false)).toEqual({ status: 404, code: 'LAI-GW-020' });
    expect(table.unavailable('model-zzz', true)).toEqual({ status: 404, code: 'LAI-GW-020' });
  });

  it('keeps the tailnet address out of the node list the probe and the UI read', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')], { tailnetIp: '203.0.113.9' })];
    await table.rebuild();
    expect(JSON.stringify(table.routableNodes())).not.toContain('203.0.113.9');
  });
});

describe('RoutingTable: live counters survive a rebuild', () => {
  async function loaded() {
    const ctx = setup();
    ctx.source.rows = [node('ai-n1', [model('model-a')]), node('ai-n2', [model('model-a')])];
    await ctx.table.rebuild();
    return ctx;
  }
  const first = (table: RoutingTable, id = 'ai-n1'): SelectedCandidate => {
    const result = table.select('model-a');
    if (result.kind !== 'candidates') throw new Error(`expected candidates, got ${result.kind}`);
    return result.candidates.find((c) => c.nodeId === id)!;
  };

  it('inflight', async () => {
    const { table } = await loaded();
    table.acquire(ref('ai-n1'));
    table.acquire(ref('ai-n1'));
    await table.rebuild();
    expect(first(table).inflight).toBe(2);
    table.release(ref('ai-n1'));
    expect(first(table).inflight).toBe(1);
  });

  it('never lets inflight go below zero', async () => {
    const { table } = await loaded();
    table.release(ref('ai-n1'));
    expect(first(table).inflight).toBe(0);
  });

  it('the first-byte average', async () => {
    const { table } = await loaded();
    table.recordTtfb(ref('ai-n1'), 1000);
    await table.rebuild();
    expect(first(table).ewmaTtfbMs).toBe(1000);
  });

  it('suspect', async () => {
    const { table } = await loaded();
    table.markSuspect(ref('ai-n1'));
    await table.rebuild();
    expect(nodesOf(table.select('model-a'))).toEqual(['ai-n2']);
  });

  it('keyInvalid', async () => {
    const { table } = await loaded();
    table.markKeyInvalid(ref('ai-n1'));
    await table.rebuild();
    expect(nodesOf(table.select('model-a'))).toEqual(['ai-n2']);
    expect(table.keyInvalidNodes()).toEqual(['ai-n1']);
  });

  it('the unloaded mark', async () => {
    const { table } = await loaded();
    table.markUnloaded('ai-n1', 'model-a');
    await table.rebuild();
    expect(nodesOf(table.select('model-a'))).toEqual(['ai-n2']);
  });

  it('a node dropping out of the table and coming back (its model stays marked, its counters stay)', async () => {
    const { table, source } = await loaded();
    table.acquire(ref('ai-n1'));
    const rows = source.rows;
    source.rows = rows.filter((row) => row.nodeId !== 'ai-n1');
    await table.rebuild();
    expect(table.routableNodeCount()).toBe(1);
    source.rows = rows;
    await table.rebuild();
    expect(first(table).inflight).toBe(1);
  });

  it('forgets the counters of a node that is gone, unless a request is still in flight on it', async () => {
    const { table, source } = await loaded();
    table.acquire(ref('ai-n1'));
    table.recordTtfb(ref('ai-n2'), 50);
    source.rows = [];
    await table.rebuild();
    expect(table.inflightOf(ref('ai-n1'))).toBe(1);
    table.release(ref('ai-n1'));
    await table.rebuild();
    expect(table.inflightOf(ref('ai-n1'))).toBe(0);
  });
});

describe('RoutingTable: the first-byte average (alpha 0.2)', () => {
  it.each([
    [[1000], 1000],
    [[1000, 2000], 1200],
    [[1000, 2000, 2000], 1360],
    [[500, 500, 500, 500], 500],
  ])('after %j it is %f', async (samples, expected) => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.rebuild();
    for (const ms of samples) table.recordTtfb(ref('ai-n1'), ms);
    const result = table.select('model-a');
    expect(result.kind === 'candidates' && result.candidates[0]!.ewmaTtfbMs).toBeCloseTo(expected, 6);
  });

  it('ignores a negative or non-finite sample', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.rebuild();
    table.recordTtfb(ref('ai-n1'), 800);
    table.recordTtfb(ref('ai-n1'), -5);
    table.recordTtfb(ref('ai-n1'), Number.NaN);
    const result = table.select('model-a');
    expect(result.kind === 'candidates' && result.candidates[0]!.ewmaTtfbMs).toBe(800);
  });
});

describe('RoutingTable: suspect for 30 s', () => {
  it('excludes the node until exactly 30 s have passed', async () => {
    const { source, table, clock } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.rebuild();
    table.markSuspect(ref('ai-n1'));
    expect(table.select('model-a')).toEqual({ kind: 'none_loaded' });
    expect(table.isSuspect('ai-n1')).toBe(true);
    clock.advance(29_999);
    expect(table.select('model-a')).toEqual({ kind: 'none_loaded' });
    clock.advance(1);
    expect(table.isSuspect('ai-n1')).toBe(false);
    expect(table.select('model-a').kind).toBe('candidates');
  });

  it('does not shorten an existing suspicion', async () => {
    const { source, table, clock } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.rebuild();
    table.markSuspect(ref('ai-n1'), 60_000);
    table.markSuspect(ref('ai-n1'));
    clock.advance(31_000);
    expect(table.isSuspect('ai-n1')).toBe(true);
  });

  it('a probe suspect lasts until the probe clears it, however long that takes', async () => {
    const { source, table, clock } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.rebuild();
    table.setProbeSuspect('ai-n1', true);
    clock.advance(3_600_000);
    expect(table.select('model-a')).toEqual({ kind: 'none_loaded' });
    table.setProbeSuspect('ai-n1', false);
    expect(table.select('model-a').kind).toBe('candidates');
  });
});

describe('RoutingTable: events', () => {
  it('a node state event rebuilds the table', async () => {
    const { source, bus, table } = setup();
    source.rows = [node('ai-n1', [model('model-a', { status: 'unloaded' })])];
    await table.rebuild();
    expect(table.select('model-a')).toEqual({ kind: 'none_loaded' });
    source.rows = [node('ai-n1', [model('model-a', { status: 'loaded' })])];
    bus.emit(LOCALAI_NODE_STATE_CHANNEL, { nodeId: 'ai-n1', modelsChanged: true, modeChanged: false });
    await table.rebuild();
    expect(table.select('model-a').kind).toBe('candidates');
  });

  it('an unloaded mark is cleared by the next state event of that node only', async () => {
    const { source, bus, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')]), node('ai-n2', [model('model-a')])];
    await table.rebuild();
    table.markUnloaded('ai-n1', 'model-a');
    table.markUnloaded('ai-n2', 'model-a');
    expect(table.select('model-a')).toEqual({ kind: 'none_loaded' });
    bus.emit(LOCALAI_NODE_STATE_CHANNEL, { nodeId: 'ai-n1', modelsChanged: false, modeChanged: false });
    await table.rebuild();
    expect(nodesOf(table.select('model-a'))).toEqual(['ai-n1']);
  });

  it('a marked-unloaded model is loadable again, and left out when it cannot be loaded', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')]), node('ai-n2', [model('model-a', { loadable: false })])];
    await table.rebuild();
    table.markUnloaded('ai-n1', 'model-a');
    table.markUnloaded('ai-n2', 'model-a');
    expect(table.loadableCandidates('model-a').map((c) => c.nodeId)).toEqual(['ai-n1']);
  });

  it('a key_reported event clears keyInvalid and rebuilds', async () => {
    const { source, bus, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.rebuild();
    table.markKeyInvalid(ref('ai-n1'));
    expect(table.select('model-a')).toEqual({ kind: 'none_loaded' });
    bus.emit(LOCALAI_NODE_KEY_REPORTED_EVENT, { nodeId: 'ai-n1', keyHint: 'aaaa…bbbb', reason: 'rotate' });
    expect(table.select('model-a').kind).toBe('candidates');
    expect(table.keyInvalidNodes()).toEqual([]);
  });

  it('a key_reported event admits a node that had no key', async () => {
    const { source, bus, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')], { keyReportedAt: null })];
    await table.rebuild();
    expect(table.routableNodeCount()).toBe(0);
    source.rows = [node('ai-n1', [model('model-a')])];
    bus.emit(LOCALAI_NODE_KEY_REPORTED_EVENT, { nodeId: 'ai-n1', keyHint: 'aaaa…bbbb', reason: 'enroll' });
    await table.rebuild();
    expect(table.routableNodeCount()).toBe(1);
  });

  it('ignores a malformed event payload without throwing', () => {
    const { bus } = setup();
    expect(() => bus.emit(LOCALAI_NODE_STATE_CHANNEL, null)).not.toThrow();
    expect(() => bus.emit(LOCALAI_NODE_KEY_REPORTED_EVENT, undefined)).not.toThrow();
  });

  it('stops listening when the module is destroyed', async () => {
    const { source, bus, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    table.onModuleDestroy();
    bus.emit(LOCALAI_NODE_STATE_CHANNEL, { nodeId: 'ai-n1', modelsChanged: true, modeChanged: false });
    await tick();
    expect(source.loads).toBe(0);
  });
});

describe('RoutingTable: coalesced rebuilds', () => {
  it('runs one at a time and schedules exactly one more for any number of triggers', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    let release!: () => void;
    source.gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const all = [table.rebuild(), table.rebuild(), table.rebuild(), table.rebuild()];
    await tick();
    expect(source.loads).toBe(1);
    source.gate = null;
    release();
    await Promise.all(all);
    expect(source.loads).toBe(2);
    await table.rebuild();
    expect(source.loads).toBe(3);
  });

  it('a trigger during a rebuild sees the newer data afterwards', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a', { status: 'unloaded' })])];
    let release!: () => void;
    source.gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const first = table.rebuild();
    await tick();
    source.rows = [node('ai-n1', [model('model-a', { status: 'loaded' })])];
    const second = table.rebuild();
    source.gate = null;
    release();
    await Promise.all([first, second]);
    expect(table.select('model-a').kind).toBe('candidates');
  });
});

describe('RoutingTable: the 10 s safety rebuild', () => {
  beforeEach(() => jest.useFakeTimers());
  afterEach(() => jest.useRealTimers());

  it('rebuilds every 10 s once bootstrapped, and stops on destroy', async () => {
    const { source, table } = setup(true);
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.onApplicationBootstrap();
    expect(source.loads).toBe(1);
    await jest.advanceTimersByTimeAsync(10_000);
    expect(source.loads).toBe(2);
    await jest.advanceTimersByTimeAsync(20_000);
    expect(source.loads).toBe(4);
    table.onModuleDestroy();
    await jest.advanceTimersByTimeAsync(60_000);
    expect(source.loads).toBe(4);
  });

  it('starts no timer when timers are off', async () => {
    const { source, table } = setup(false);
    await table.onApplicationBootstrap();
    await jest.advanceTimersByTimeAsync(60_000);
    expect(source.loads).toBe(1);
  });

  it('timers are off under Jest and when the gateway is switched off', () => {
    expect(routingOptionsFromEnv({ NODE_ENV: 'test' }).timers).toBe(false);
    expect(routingOptionsFromEnv({ NODE_ENV: 'production', LOCALAI_GATEWAY_ENABLED: 'false' }).timers).toBe(false);
    expect(routingOptionsFromEnv({ NODE_ENV: 'production' })).toEqual({ timers: true, rebuildIntervalMs: 10_000 });
  });
});

describe('RoutingTable: loadableCandidates', () => {
  it('lists unloaded candidates of healthy nodes, ordered by priority, preference, busy loaded models and node id', async () => {
    const { source, table } = setup();
    source.rows = [
      node('ai-n1', [model('model-a', { status: 'unloaded' }), model('model-x')], { gatewayPriority: 100 }),
      node('ai-n2', [model('model-a', { status: 'unloaded' })], { gatewayPriority: 100 }),
      node('ai-n3', [model('model-a', { status: 'unloaded' })], { gatewayPriority: 10 }),
      node('ai-n4', [model('model-a', { status: 'unloaded' })], { gatewayPriority: 100, mode: 'offline' }),
      node('ai-n5', [model('model-a', { status: 'unloaded' })], { gatewayPriority: 100 }),
    ];
    await table.rebuild();
    table.acquire(ref('ai-n1', 'model-x'));
    expect(table.loadableCandidates('model-a').map((c) => c.nodeId)).toEqual(['ai-n3', 'ai-n2', 'ai-n5', 'ai-n1']);
    expect(table.loadableCandidates('model-a', { preferredNodes: ['ai-n1'] }).map((c) => c.nodeId)).toEqual(['ai-n3', 'ai-n1', 'ai-n2', 'ai-n5']);
    const n1 = table.loadableCandidates('model-a').find((c) => c.nodeId === 'ai-n1')!;
    expect(n1.loadedOnNode).toEqual([{ modelId: 'model-x', inflight: 1 }]);
    expect(n1.modelsMax).toBeNull();
  });

  it('carries the node\'s models_max from its last snapshot', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a', { status: 'unloaded' })], { modelsMax: 3 }), node('ai-n2', [model('model-a', { status: 'unloaded' })], { modelsMax: 0 })];
    await table.rebuild();
    const byNode = new Map(table.loadableCandidates('model-a').map((c) => [c.nodeId, c.modelsMax]));
    expect(byNode.get('ai-n1')).toBe(3);
    expect(byNode.get('ai-n2')).toBeNull();
  });

  it('leaves out suspect and keyInvalid nodes', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a', { status: 'unloaded' })]), node('ai-n2', [model('model-a', { status: 'unloaded' })]), node('ai-n3', [model('model-a', { status: 'unloaded' })])];
    await table.rebuild();
    table.markSuspect(ref('ai-n1'));
    table.markKeyInvalid(ref('ai-n2'));
    expect(table.loadableCandidates('model-a').map((c) => c.nodeId)).toEqual(['ai-n3']);
  });
});

describe('RoutingTable: waitForLoaded', () => {
  beforeEach(() => jest.useFakeTimers());
  afterEach(() => jest.useRealTimers());

  async function unloaded() {
    const ctx = setup();
    ctx.source.rows = [node('ai-n1', [model('model-a', { status: 'unloaded' })])];
    await ctx.table.rebuild();
    return ctx;
  }

  it('resolves true at once when the model is already loaded', async () => {
    const { source, table } = setup();
    source.rows = [node('ai-n1', [model('model-a')])];
    await table.rebuild();
    await expect(table.waitForLoaded('model-a', 'ai-n1', 1000)).resolves.toBe(true);
  });

  it('resolves true when a rebuild shows the model loaded on that node', async () => {
    const { source, bus, table } = await unloaded();
    const wait = table.waitForLoaded('model-a', 'ai-n1', 60_000);
    source.rows = [node('ai-n1', [model('model-a', { status: 'loaded' })])];
    bus.emit(LOCALAI_NODE_STATE_CHANNEL, { nodeId: 'ai-n1', modelsChanged: true, modeChanged: false });
    await expect(wait).resolves.toBe(true);
  });

  it('is not satisfied by the model loading on another node', async () => {
    const { source, bus, table } = await unloaded();
    const wait = table.waitForLoaded('model-a', 'ai-n1', 5_000);
    source.rows = [node('ai-n1', [model('model-a', { status: 'unloaded' })]), node('ai-n2', [model('model-a')])];
    bus.emit(LOCALAI_NODE_STATE_CHANNEL, { nodeId: 'ai-n2', modelsChanged: true, modeChanged: false });
    await jest.advanceTimersByTimeAsync(5_000);
    await expect(wait).resolves.toBe(false);
  });

  it('resolves false at the timeout', async () => {
    const { table } = await unloaded();
    const wait = table.waitForLoaded('model-a', 'ai-n1', 15_000);
    await jest.advanceTimersByTimeAsync(14_999);
    let settled = false;
    void wait.then(() => (settled = true));
    await Promise.resolve();
    expect(settled).toBe(false);
    await jest.advanceTimersByTimeAsync(1);
    await expect(wait).resolves.toBe(false);
  });

  it('resolves false when the caller aborts (the client left while waiting)', async () => {
    const { table } = await unloaded();
    const controller = new AbortController();
    const wait = table.waitForLoaded('model-a', 'ai-n1', 60_000, controller.signal);
    controller.abort();
    await expect(wait).resolves.toBe(false);
    await expect(table.waitForLoaded('model-a', 'ai-n1', 60_000, controller.signal)).resolves.toBe(false);
  });

  it('resolves false for every waiter when the table shuts down', async () => {
    const { table } = await unloaded();
    const wait = table.waitForLoaded('model-a', 'ai-n1', 60_000);
    table.onModuleDestroy();
    await expect(wait).resolves.toBe(false);
  });
});

describe('RoutingTable: healthyCandidates (the listing of /v1/models)', () => {
  async function two() {
    const { source, table, clock } = setup();
    source.rows = [node('ai-n1', [model('model-a', { inputModalities: ['text', 'image'] })]), node('ai-n2', [model('model-a', { status: 'unloaded' })])];
    await table.rebuild();
    return { table, clock };
  }
  const brief = (table: RoutingTable) => table.healthyCandidates('model-a').map((c) => `${c.nodeId}:${c.status}`).sort();

  it('returns loaded and loadable candidates with what the node reported', async () => {
    const { table } = await two();
    expect(brief(table)).toEqual(['ai-n1:loaded', 'ai-n2:loadable']);
    expect(table.healthyCandidates('model-a').find((c) => c.nodeId === 'ai-n1')?.inputModalities).toEqual(['text', 'image']);
    expect(table.healthyCandidates('no-such-model')).toEqual([]);
  });

  it('leaves out a suspect node until the suspicion ends, and a node with an invalid key until it reports one', async () => {
    const { table, clock } = await two();
    table.markSuspect(ref('ai-n1'));
    expect(brief(table)).toEqual(['ai-n2:loadable']);
    clock.advance(30_001);
    expect(brief(table)).toEqual(['ai-n1:loaded', 'ai-n2:loadable']);
    table.markKeyInvalid({ nodeId: 'ai-n2' });
    expect(brief(table)).toEqual(['ai-n1:loaded']);
  });

  it('shows a model marked unloaded as loadable', async () => {
    const { table } = await two();
    table.markUnloaded('ai-n1', 'model-a');
    expect(brief(table)).toEqual(['ai-n1:loadable', 'ai-n2:loadable']);
  });
});
