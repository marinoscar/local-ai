// The on-demand load (gateway.md section 4.3 step 3): the pure eviction guard and ordering, and the source over the REAL
// routing table with a fake command plane. The same path over real HTTP and a real database is
// test/localai/gateway-ondemand.e2e.spec.ts.
import { Logger } from '@nestjs/common';

import { TableCandidateSource, type CandidateRequest } from '../attempts';
import { GatewayError } from '../errors';
import { DEFAULT_MODEL_POLICY, type ProxyModelPolicy, type ResolvedGatewayModel } from '../model-resolver';
import { LOCALAI_NODE_STATE_CHANNEL } from '../../node-link/state.service';
import type { SourceNodeRow } from './build-candidates';
import {
  loadParams,
  OnDemandCandidateSource,
  planOnDemand,
  wouldEvict,
  type ActiveLoad,
  type ActiveLoadReader,
  type CommandOutcome,
  type LoadAudit,
  type LoadEnqueuer,
} from './on-demand';
import { FakeBus, FakeClock, model, node } from './routing.fixtures';
import { RoutingTable } from './routing-table';
import type { LoadableCandidate } from './routing.types';

beforeAll(() => {
  jest.spyOn(Logger.prototype, 'debug').mockImplementation(() => undefined);
  jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
});
afterAll(() => jest.restoreAllMocks());

const loadable = (nodeId: string, overrides: Partial<LoadableCandidate> = {}): LoadableCandidate => ({
  nodeId,
  modelId: 'model-a',
  publicHost: `${nodeId}.example.test`,
  servePattern: 'B',
  tailnetIp: null,
  tailnetMachine: null,
  priority: 100,
  status: 'loadable',
  canLoad: true,
  slots: 2,
  ctx: 8192,
  inputModalities: ['text'],
  inflight: 0,
  suspectUntil: 0,
  ewmaTtfbMs: null,
  loadedOnNode: [],
  modelsMax: 2,
  ...overrides,
});

describe('wouldEvict: the eviction guard (models_max x loaded x inflight)', () => {
  // [modelsMax, loaded models' inflight counts, expected unloadOthers, expected busy]
  const cases: Array<[number | null, number[], boolean, boolean]> = [
    [1, [], false, false],
    [1, [0], true, false],
    [1, [3], true, true],
    [2, [], false, false],
    [2, [5], false, false], // room for a second model: nothing is unloaded, a busy one is safe
    [2, [0, 0], true, false],
    [2, [0, 1], true, true],
    [2, [4, 0], true, true],
    [4, [1, 1, 1], false, false],
    [4, [1, 1, 1, 1], true, true],
    [null, [], false, false], // no snapshot value: assumes 1
    [null, [2], true, true],
    [null, [0], true, false],
  ];
  it.each(cases)('models_max %s, loaded inflight %j -> unload_others %s, busy %s', (modelsMax, inflights, unloadOthers, busy) => {
    const loadedOnNode = inflights.map((inflight, index) => ({ modelId: `loaded-${index}`, inflight }));
    expect(wouldEvict({ modelsMax, loadedOnNode })).toEqual({ unloadOthers, busy });
  });
});

describe('planOnDemand', () => {
  it('keeps the order of the table and carries unload_others per node', () => {
    const plan = planOnDemand(
      [loadable('ai-n1', { modelsMax: 1, loadedOnNode: [{ modelId: 'x', inflight: 0 }] }), loadable('ai-n2'), loadable('ai-n3', { modelsMax: 3, loadedOnNode: [{ modelId: 'x', inflight: 0 }] })],
      new Set(),
    );
    expect(plan.eligible.map((entry) => [entry.candidate.nodeId, entry.unloadOthers])).toEqual([
      ['ai-n1', true],
      ['ai-n2', false],
      ['ai-n3', false],
    ]);
    expect(plan.skippedBusy).toEqual([]);
  });

  it('skips a node whose load would evict a busy model, and reports it', () => {
    const plan = planOnDemand(
      [loadable('ai-n1', { modelsMax: 1, loadedOnNode: [{ modelId: 'x', inflight: 2 }] }), loadable('ai-n2', { modelsMax: 1, loadedOnNode: [{ modelId: 'y', inflight: 0 }] })],
      new Set(),
    );
    expect(plan.eligible.map((entry) => entry.candidate.nodeId)).toEqual(['ai-n2']);
    expect(plan.skippedBusy).toEqual(['ai-n1']);
  });

  it('leaves out the nodes this request already tried', () => {
    const plan = planOnDemand([loadable('ai-n1'), loadable('ai-n2')], new Set(['ai-n1']));
    expect(plan.eligible.map((entry) => entry.candidate.nodeId)).toEqual(['ai-n2']);
  });

  it('builds explicit command params (the Zod default of unload_others is true)', () => {
    expect(loadParams('model-a', false)).toEqual({ model_id: 'model-a', wait: true, unload_others: false, set_default: false });
  });
});

// ---- the source over a real table -------------------------------------------------------------------------------

class FakeEnqueuer implements LoadEnqueuer {
  calls: Array<{ nodeId: string; type: string; params: unknown; source: string; actor: { userId: string | null } }> = [];
  /** One command per (node, model): the dedup of the command plane. */
  private readonly active = new Map<string, string>();
  failFor = new Set<string>();
  async enqueue(nodeId: string, type: string, params: unknown, actor: { userId: string | null }, source: string) {
    this.calls.push({ nodeId, type, params, source, actor });
    if (this.failFor.has(nodeId)) throw new Error('refused');
    const key = `${nodeId}:${(params as { model_id: string }).model_id}`;
    const existing = this.active.get(key);
    if (existing) return { id: existing, created: false };
    const id = `cmd-${this.active.size + 1}`;
    this.active.set(key, id);
    return { id, created: true };
  }
}

class FakeLoads implements ActiveLoadReader {
  active: ActiveLoad | null = null;
  outcomes = new Map<string, CommandOutcome>();
  async find() {
    return this.active;
  }
  async outcome(commandId: string) {
    return this.outcomes.get(commandId) ?? 'active';
  }
}

class FakeAudit implements LoadAudit {
  rows: Array<{ commandId: string; nodeId: string; params: Record<string, unknown>; keyPrefix: string | null }> = [];
  async requested(input: (typeof this.rows)[number]) {
    this.rows.push(input);
  }
}

function setup(rows: SourceNodeRow[]) {
  const state = { rows };
  const bus = new FakeBus();
  const table = new RoutingTable({ load: async () => ({ rows: state.rows.map((row) => ({ ...row })), settings: { queueFactor: 4 } }) }, bus as never, new FakeClock(), {
    timers: false,
    rebuildIntervalMs: 10_000,
  });
  table.onModuleInit();
  const commands = new FakeEnqueuer();
  const loads = new FakeLoads();
  const audit = new FakeAudit();
  const warnings: string[] = [];
  const source = new OnDemandCandidateSource({ inner: new TableCandidateSource(table), routing: table, commands, loads, audit, log: { warn: (line) => warnings.push(line) }, pollMs: 20 });
  /** The node reports `modelId` loaded: the snapshot changes and the state event fires. */
  const flip = async (nodeId: string, modelId = 'model-a') => {
    state.rows = state.rows.map((row) =>
      row.nodeId === nodeId ? { ...row, models: row.models.map((entry) => (entry.modelId === modelId ? { ...entry, status: 'loaded' } : entry)) } : row,
    );
    bus.emit(LOCALAI_NODE_STATE_CHANNEL, { nodeId, modelsChanged: true, modeChanged: false });
  };
  return { table, commands, loads, audit, warnings, source, flip, state };
}

const resolved = (policy: Partial<ProxyModelPolicy> = {}, extra: Partial<ResolvedGatewayModel> = {}): ResolvedGatewayModel => ({
  modelId: 'model-a',
  requestedModel: 'model-a',
  policy: { ...DEFAULT_MODEL_POLICY, onDemandLoad: true, loadWaitS: 2, ...policy },
  roles: null,
  inApprovedCatalog: true,
  keyPrefix: 'lai_Ab3dEf',
  ...extra,
});

const asked = (target: ResolvedGatewayModel, over: Partial<CandidateRequest> = {}): CandidateRequest => ({
  model: target,
  tried: new Set(),
  attempt: 1,
  failures: [],
  signal: new AbortController().signal,
  ...over,
});

const unloadedOn = (nodeId: string, others: ReturnType<typeof model>[] = [], extra: Partial<SourceNodeRow> = {}) =>
  node(nodeId, [model('model-a', { status: 'unloaded' }), ...others], extra);

describe('OnDemandCandidateSource', () => {
  it('queues one model.load (source gateway, explicit params), waits for the flip and then hands out the node', async () => {
    const ctx = setup([unloadedOn('ai-n1')]);
    await ctx.table.rebuild();
    const pending = ctx.source.next(asked(resolved()));
    await new Promise((resolve) => setImmediate(resolve));
    expect(ctx.commands.calls).toEqual([
      { nodeId: 'ai-n1', type: 'model.load', params: { model_id: 'model-a', wait: true, unload_others: false, set_default: false }, source: 'gateway', actor: { userId: null } },
    ]);
    await ctx.flip('ai-n1');
    const choice = await pending;
    expect(choice.kind === 'candidate' && choice.candidate.nodeId).toBe('ai-n1');
    expect(ctx.audit.rows).toEqual([{ commandId: 'cmd-1', nodeId: 'ai-n1', params: expect.objectContaining({ model_id: 'model-a' }), keyPrefix: 'lai_Ab3dEf' }]);
  });

  it('two concurrent requests share one command and both complete after the flip', async () => {
    const ctx = setup([unloadedOn('ai-n1')]);
    await ctx.table.rebuild();
    const first = ctx.source.next(asked(resolved()));
    const second = ctx.source.next(asked(resolved()));
    await new Promise((resolve) => setImmediate(resolve));
    await ctx.flip('ai-n1');
    const [a, b] = await Promise.all([first, second]);
    expect([a.kind, b.kind]).toEqual(['candidate', 'candidate']);
    expect(ctx.audit.rows).toHaveLength(1); // only the creating call is audited
    expect(new Set(ctx.commands.calls.map((call) => call.nodeId))).toEqual(new Set(['ai-n1']));
  });

  it('times out with LAI-GW-022 when nothing flips', async () => {
    const ctx = setup([unloadedOn('ai-n1')]);
    await ctx.table.rebuild();
    const choice = await ctx.source.next(asked(resolved({ loadWaitS: 0.15 })));
    expect(choice.kind === 'none' && choice.error?.localaiCode).toBe('LAI-GW-022');
    expect(choice.kind === 'none' && choice.error?.retryAfterS).toBe(15);
  });

  it('ends without an error when the client aborts while waiting; the command stays queued', async () => {
    const ctx = setup([unloadedOn('ai-n1')]);
    await ctx.table.rebuild();
    const controller = new AbortController();
    const pending = ctx.source.next(asked(resolved({ loadWaitS: 30 }), { signal: controller.signal }));
    await new Promise((resolve) => setImmediate(resolve));
    controller.abort();
    await expect(pending).resolves.toEqual({ kind: 'none', error: null });
    expect(ctx.commands.calls).toHaveLength(1);
  });

  it('skips a node whose load would evict a busy model and uses the next; with none left it is LAI-GW-021', async () => {
    const busy = model('model-x', { status: 'loaded' });
    const ctx = setup([
      unloadedOn('ai-n1', [busy], { gatewayPriority: 10, modelsMax: 1 }),
      unloadedOn('ai-n2', [], { gatewayPriority: 50, modelsMax: 1 }),
    ]);
    await ctx.table.rebuild();
    ctx.table.acquire({ nodeId: 'ai-n1', modelId: 'model-x' });
    const pending = ctx.source.next(asked(resolved()));
    await new Promise((resolve) => setImmediate(resolve));
    expect(ctx.commands.calls.map((call) => call.nodeId)).toEqual(['ai-n2']);
    await ctx.flip('ai-n2');
    expect((await pending).kind).toBe('candidate');

    const only = setup([unloadedOn('ai-n1', [busy], { modelsMax: 1 })]);
    await only.table.rebuild();
    only.table.acquire({ nodeId: 'ai-n1', modelId: 'model-x' });
    const refused = await only.source.next(asked(resolved()));
    expect(refused.kind === 'none' && refused.error?.localaiCode).toBe('LAI-GW-021');
    expect(only.commands.calls).toEqual([]);
  });

  it('sends unload_others true when the node is full of idle models, and false when there is room', async () => {
    const idle = model('model-x', { status: 'loaded' });
    const full = setup([unloadedOn('ai-n1', [idle], { modelsMax: 1 })]);
    await full.table.rebuild();
    void full.source.next(asked(resolved({ loadWaitS: 0.05 })));
    await new Promise((resolve) => setImmediate(resolve));
    expect(full.commands.calls[0]!.params).toMatchObject({ unload_others: true });

    const room = setup([unloadedOn('ai-n1', [idle], { modelsMax: 2 })]);
    await room.table.rebuild();
    void room.source.next(asked(resolved({ loadWaitS: 0.05 })));
    await new Promise((resolve) => setImmediate(resolve));
    expect(room.commands.calls[0]!.params).toMatchObject({ unload_others: false });
  });

  it('answers as the table does when the policy has no on-demand load', async () => {
    const ctx = setup([unloadedOn('ai-n1')]);
    await ctx.table.rebuild();
    const choice = await ctx.source.next(asked(resolved({ onDemandLoad: false })));
    expect(choice.kind === 'none' && choice.error?.localaiCode).toBe('LAI-GW-021');
    expect(ctx.commands.calls).toEqual([]);
  });

  it('answers as the table does when a node already has the model loaded', async () => {
    const ctx = setup([node('ai-n1', [model('model-a')])]);
    await ctx.table.rebuild();
    const choice = await ctx.source.next(asked(resolved()));
    expect(choice.kind).toBe('candidate');
    expect(ctx.commands.calls).toEqual([]);
  });

  it('is 404 LAI-GW-020 for a model no node has, even with on-demand on', async () => {
    const ctx = setup([node('ai-n1', [model('model-b')])]);
    await ctx.table.rebuild();
    const choice = await ctx.source.next(asked(resolved({}, { inApprovedCatalog: false })));
    expect(choice.kind === 'none' && choice.error?.localaiCode).toBe('LAI-GW-020');
  });

  it('joins a load that is already running (the model shows as loading, in no candidate list)', async () => {
    const ctx = setup([node('ai-n1', [model('model-a', { status: 'loading' })])]);
    await ctx.table.rebuild();
    ctx.loads.active = { commandId: 'cmd-9', nodeId: 'ai-n1' };
    const pending = ctx.source.next(asked(resolved()));
    await new Promise((resolve) => setImmediate(resolve));
    await ctx.flip('ai-n1');
    expect((await pending).kind).toBe('candidate');
    expect(ctx.commands.calls).toEqual([]);
  });

  it('stops waiting at once when the load command failed (LAI-GW-021)', async () => {
    const ctx = setup([unloadedOn('ai-n1')]);
    await ctx.table.rebuild();
    ctx.loads.outcomes.set('cmd-1', 'failed');
    const started = Date.now();
    const choice = await ctx.source.next(asked(resolved({ loadWaitS: 30 })));
    expect(choice.kind === 'none' && choice.error?.localaiCode).toBe('LAI-GW-021');
    expect(Date.now() - started).toBeLessThan(2000);
  });

  it('tries the next node when the command plane refuses one, and says LAI-GW-052 when all refuse', async () => {
    const ctx = setup([unloadedOn('ai-n1', [], { gatewayPriority: 10 }), unloadedOn('ai-n2', [], { gatewayPriority: 20 })]);
    await ctx.table.rebuild();
    ctx.commands.failFor.add('ai-n1');
    const pending = ctx.source.next(asked(resolved()));
    await new Promise((resolve) => setImmediate(resolve));
    expect(ctx.commands.calls.map((call) => call.nodeId)).toEqual(['ai-n1', 'ai-n2']);
    await ctx.flip('ai-n2');
    expect((await pending).kind).toBe('candidate');
    ctx.commands.failFor.add('ai-n2');
    const refused = setup([unloadedOn('ai-n1')]);
    await refused.table.rebuild();
    refused.commands.failFor.add('ai-n1');
    const choice = await refused.source.next(asked(resolved()));
    expect(choice.kind === 'none' && choice.error?.localaiCode).toBe('LAI-GW-052');
    expect(choice.kind === 'none' && choice.error).toBeInstanceOf(GatewayError);
  });

  it('does not ask a node this request already tried to load the model again', async () => {
    const ctx = setup([unloadedOn('ai-n1')]);
    await ctx.table.rebuild();
    const choice = await ctx.source.next(asked(resolved(), { tried: new Set(['ai-n1']), attempt: 2 }));
    expect(choice).toEqual({ kind: 'none', error: null });
    expect(ctx.commands.calls).toEqual([]);
  });
});
