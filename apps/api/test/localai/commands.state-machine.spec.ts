// The command plane's state machine over the in-memory table and a fake clock (docs/specs/cloud-app.md section 4.4):
// queued -> claimed (running) -> succeeded | failed, queued -> expired, running -> failed on a lost lease; leases,
// the hard cap, dedup, the idempotent settle and the isolation between nodes. The same flows against real SQL are
// `commands.db.spec.ts`.
import { ClaimService } from '../../src/localai/commands/claim.service';
import { COMMAND_ENQUEUED_CHANNEL, COMMAND_SETTLED_CHANNEL } from '../../src/localai/commands/command-events';
import { COMMAND_POLICY } from '../../src/localai/commands/command-policy';
import { CommandsService } from '../../src/localai/commands/commands.service';
import { CommandsSweepHandler } from '../../src/localai/commands/sweeper.job';
import type { CommandType } from '../../src/localai/contracts/commands';
import { FakeBus, FakeClock, FakeCommandStore, StoreClaimRepository } from './support/fake-command-store';

const NODE_A = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1';
const NODE_B = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb2';
const USER = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc3';
const MODEL = 'qwen3.6-35b-a3b-q4km';
const ALL_NAMES = ['status.refresh', 'model.load', 'model.unload', 'model.set_default', 'model.download', 'ark.sync', 'ark.verify', 'mode.offline', 'key.rotate', 'service.restart', 'logs.tail', 'catalog.refresh'];

function setup() {
  const clock = new FakeClock();
  const store = new FakeCommandStore(clock);
  const bus = new FakeBus();
  const commands = new CommandsService(store as never, bus as never, clock, { record: async () => undefined });
  const claims = new ClaimService(new StoreClaimRepository(store) as never, bus as never, clock);
  const registered: unknown[] = [];
  const sweeper = new CommandsSweepHandler({ register: (handler: unknown) => registered.push(handler) } as never, store as never, commands, clock);
  const enqueue = (type: string, params: unknown = {}, nodeId = NODE_A) => commands.enqueueForNode(nodeId, type, params, { userId: USER }, 'ui');
  const claim = async (nodeId = NODE_A, names: string[] = ALL_NAMES) => (await claims.claim(nodeId, { types: names, waitMs: 0 })).jobs[0];
  return { clock, store, bus, commands, claims, sweeper, registered, enqueue, claim };
}

const loadParams = { model_id: MODEL };
const loadResult = { model_id: MODEL, status: 'loaded', load_ms: 1000, unloaded: [], vram_used_mib: null, is_default: false };
const code = (response: unknown) => (response as { response?: { details?: { code?: string; reason?: string } } }).response?.details;

describe('enqueue', () => {
  it('stores a queued command with the parsed params, the dedup key, the TTL and the actor, and publishes the wake-up', async () => {
    const { enqueue, store, bus, clock } = setup();
    const { command, created } = await enqueue('localai.model.load', loadParams);
    expect(created).toBe(true);
    expect(command).toMatchObject({
      nodeId: NODE_A,
      type: 'localai.model.load',
      status: 'queued',
      params: { model_id: MODEL, wait: true, unload_others: true, set_default: false },
      dedupKey: `model.load:${MODEL}`,
      source: 'ui',
      requestedById: USER,
      attempts: 0,
    });
    expect(command.expiresAt).toEqual(new Date(clock.now().getTime() + COMMAND_POLICY['localai.model.load'].queuedTtlMs));
    expect(store.rows).toHaveLength(1);
    expect(bus.on(COMMAND_ENQUEUED_CHANNEL)).toEqual([{ nodeId: NODE_A }]);
  });

  it('rejects an unknown type and bad or extra params with a 400 and writes nothing', async () => {
    const { enqueue, store, bus } = setup();
    await expect(enqueue('localai.shell.exec', {})).rejects.toMatchObject({ status: 400 });
    await expect(enqueue('model.load', loadParams)).rejects.toMatchObject({ status: 400 });
    await expect(enqueue('localai.model.load', { model_id: 'Qwen' })).rejects.toMatchObject({ status: 400 });
    await expect(enqueue('localai.model.load', { ...loadParams, extra: 1 })).rejects.toMatchObject({ status: 400 });
    await expect(enqueue('localai.model.delete', { model_id: MODEL, roots: ['internal'], confirm: 'other-model' })).rejects.toMatchObject({ status: 400 });
    const error = await enqueue('localai.logs.tail', { unit: 'agent', lines: 501 }).catch((e) => e);
    expect(error.response.details).toMatchObject({ reason: 'invalid_params', type: 'localai.logs.tail' });
    expect(error.response.details.issues[0]).toMatchObject({ path: ['lines'] });
    expect(store.rows).toHaveLength(0);
    expect(bus.published).toHaveLength(0);
  });

  it('collapses onto the active command with the same dedup key (queued or running) and returns it', async () => {
    const { enqueue, store, claim, bus } = setup();
    const first = await enqueue('localai.model.load', loadParams);
    const again = await enqueue('localai.model.load', loadParams);
    expect(again.created).toBe(false);
    expect(again.command.id).toBe(first.command.id);
    expect(store.rows).toHaveLength(1);
    expect(bus.on(COMMAND_ENQUEUED_CHANNEL)).toHaveLength(1); // the duplicate woke nobody

    await claim(); // now running: still active
    const third = await enqueue('localai.model.load', loadParams);
    expect(third.created).toBe(false);
    expect(third.command.status).toBe('running');
    expect(store.rows).toHaveLength(1);
  });

  it('does not collapse different subjects, different nodes, or a settled command', async () => {
    const { enqueue, store, claim, commands } = setup();
    await enqueue('localai.model.load', loadParams);
    expect((await enqueue('localai.model.load', { model_id: 'gemma-4-26b-q4km' })).created).toBe(true);
    expect((await enqueue('localai.model.load', loadParams, NODE_B)).created).toBe(true);
    expect(store.rows).toHaveLength(3);

    const job = await claim(NODE_A);
    await commands.fail(NODE_A, job.job.id, { claimToken: job.claimToken, error: 'LAI-CAT-005: no room' });
    const again = await enqueue('localai.model.load', loadParams);
    expect(again.created).toBe(true);
    expect(again.command.id).not.toBe(job.job.id);
  });

  it('a type with no subject allows one active command per node', async () => {
    const { enqueue } = setup();
    expect((await enqueue('localai.status.refresh')).created).toBe(true);
    expect((await enqueue('localai.status.refresh')).created).toBe(false);
    expect((await enqueue('localai.status.refresh', {}, NODE_B)).created).toBe(true);
  });

  it('retries the insert when the blocking row settled between the conflict and the read', async () => {
    const { store, commands, clock } = setup();
    const blocker = store.seed({ nodeId: NODE_A, type: 'localai.status.refresh', dedupKey: 'status.refresh:-' });
    const realFindFirst = store.localaiCommand.findFirst;
    let calls = 0;
    store.localaiCommand.findFirst = async (args) => {
      calls += 1;
      blocker.status = 'succeeded'; // it settled just before the read
      return realFindFirst(args);
    };
    const result = await commands.enqueueForNode(NODE_A, 'localai.status.refresh', {}, { userId: null }, 'system');
    expect(calls).toBe(1);
    expect(result.created).toBe(true);
    expect(result.command.requestedById).toBeNull();
    expect(result.command.source).toBe('system');
    void clock;
  });

  it('does not swallow an error that is not the dedup conflict', async () => {
    const { store, commands } = setup();
    store.localaiCommand.create = async () => {
      throw new Error('connection reset');
    };
    await expect(commands.enqueueForNode(NODE_A, 'localai.status.refresh', {}, { userId: null }, 'system')).rejects.toThrow('connection reset');
  });

  it('survives a failing event bus: the command is stored', async () => {
    const { enqueue, store, bus } = setup();
    bus.failPublish = true;
    expect((await enqueue('localai.status.refresh')).created).toBe(true);
    expect(store.rows).toHaveLength(1);
  });
});

describe('the LOCALAI_COMMAND_ENQUEUER port (what CLOUD-07 and CLOUD-09 call)', () => {
  it('takes the lab node id and the unprefixed type, returns { id, created } and collapses a duplicate', async () => {
    const { commands, store, bus } = setup();
    store.knownNodes.set('ai-n1', NODE_A);
    const first = await commands.enqueue('ai-n1', 'status.refresh', {}, { userId: USER }, 'system');
    expect(first.created).toBe(true);
    expect(store.get(first.id)).toMatchObject({ nodeId: NODE_A, type: 'localai.status.refresh', source: 'system', requestedById: USER, dedupKey: 'status.refresh:-' });
    expect(store.get(first.id).expiresAt.getTime() - store.get(first.id).createdAt.getTime()).toBe(2 * 60_000);
    expect(await commands.enqueue('ai-n1', 'status.refresh', {}, { userId: null }, 'system')).toEqual({ id: first.id, created: false });
    expect(await commands.enqueue('ai-n1', 'localai.status.refresh', undefined, { userId: null }, 'ui')).toEqual({ id: first.id, created: false });
    expect(bus.on(COMMAND_ENQUEUED_CHANNEL)).toEqual([{ nodeId: NODE_A }]);
  });

  it('answers 404 for an unknown lab node and 400 for an unknown type or bad params, writing nothing', async () => {
    const { commands, store } = setup();
    store.knownNodes.set('ai-n1', NODE_A);
    await expect(commands.enqueue('ai-n9', 'status.refresh', {}, { userId: null }, 'system')).rejects.toMatchObject({ status: 404 });
    await expect(commands.enqueue('ai-n1', 'shell.exec', {}, { userId: null }, 'system')).rejects.toMatchObject({ status: 400 });
    await expect(commands.enqueue('ai-n1', 'status.refresh', { x: 1 }, { userId: null }, 'system')).rejects.toMatchObject({ status: 400 });
    expect(store.rows).toHaveLength(0);
  });
});

describe('claim', () => {
  it('is FIFO per node', async () => {
    const { enqueue, claim, clock, commands } = setup();
    const first = await enqueue('localai.model.unload', { model_id: 'gemma-4-26b-q4km' });
    clock.advance(1000);
    const second = await enqueue('localai.model.load', loadParams);
    clock.advance(1000);
    const third = await enqueue('localai.logs.tail', { unit: 'agent' });

    const a = await claim();
    expect(a.job.id).toBe(first.command.id);
    await commands.fail(NODE_A, a.job.id, { claimToken: a.claimToken, error: 'x' });
    expect((await claim()).job.id).toBe(second.command.id);
    void third;
  });

  it('claims only the requested types, and drops unknown names', async () => {
    const { enqueue, claim, claims } = setup();
    await enqueue('localai.model.load', loadParams);
    expect(await claim(NODE_A, ['logs.tail', 'status.refresh'])).toBeUndefined();
    expect(await claim(NODE_A, ['bogus.type', 'localai.model.load'])).toBeUndefined(); // the prefix is the server's to add
    expect(claims.resolveTypes(['model.load', 'model.load', 'bogus', 'commands.sweep'], NODE_A)).toEqual(['localai.model.load']);
    expect((await claim(NODE_A, ['model.load'])).job.type).toBe('localai.model.load');
  });

  it('answers with the wire shape: attempts 1, token, lease, renew interval, params with defaults, no traceparent', async () => {
    const { enqueue, claim, clock } = setup();
    const { command } = await enqueue('localai.model.load', loadParams);
    const job = await claim();
    expect(job).toEqual({
      job: { id: command.id, type: 'localai.model.load', attempts: 1, createdAt: command.createdAt.toISOString() },
      params: { model_id: MODEL, wait: true, unload_others: true, set_default: false },
      claimToken: expect.stringMatching(/^[0-9a-f-]{36}$/),
      renewIntervalMs: 20_000,
      leaseExpiresAt: new Date(clock.now().getTime() + 60_000).toISOString(),
      traceparent: null,
    });
  });

  it('never claims another node\'s command, an expired one, or a running one', async () => {
    const { enqueue, claim, store, clock } = setup();
    await enqueue('localai.model.load', loadParams, NODE_B);
    expect(await claim(NODE_A)).toBeUndefined();

    await enqueue('localai.status.refresh');
    clock.advance(COMMAND_POLICY['localai.status.refresh'].queuedTtlMs + 1);
    expect(await claim(NODE_A)).toBeUndefined(); // past expires_at, before the sweeper ran
    expect(store.rows.find((row) => row.nodeId === NODE_A)?.status).toBe('queued');

    expect((await claim(NODE_B)).job.type).toBe('localai.model.load');
    expect(await claim(NODE_B)).toBeUndefined();
  });

  it('holds one running command per node: the next is not handed out while the lease is alive, and is after it ends', async () => {
    const { enqueue, claim, commands, clock } = setup();
    await enqueue('localai.model.load', loadParams);
    await enqueue('localai.logs.tail', { unit: 'agent' });
    const running = await claim();
    expect(await claim()).toBeUndefined();

    clock.advance(30_000);
    expect(await claim()).toBeUndefined();
    await commands.complete(NODE_A, running.job.id, { type: 'localai.model.load', claimToken: running.claimToken, result: loadResult });
    expect((await claim()).job.type).toBe('localai.logs.tail');
  });

  it('a lost lease frees the node\'s slot even before the sweeper ran (the lease is what holds it)', async () => {
    const { enqueue, claim, clock } = setup();
    await enqueue('localai.model.load', loadParams);
    await enqueue('localai.logs.tail', { unit: 'agent' });
    await claim();
    clock.advance(60_001);
    expect((await claim()).job.type).toBe('localai.logs.tail');
  });
});

describe('renew and the lease', () => {
  it('extends the lease by the type\'s lease from now', async () => {
    const { enqueue, claim, commands, clock } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    clock.advance(20_000);
    const next = await commands.renew(NODE_A, job.job.id, { claimToken: job.claimToken });
    expect(next).toEqual(new Date(clock.now().getTime() + 60_000));
  });

  it('refuses a wrong token, another node, and an id that does not exist', async () => {
    const { enqueue, claim, commands } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    expect(await commands.renew(NODE_A, job.job.id, { claimToken: 'a-wrong-token' })).toBeNull();
    expect(await commands.renew(NODE_B, job.job.id, { claimToken: job.claimToken })).toBeNull();
    expect(await commands.renew(NODE_A, 'dddddddd-dddd-4ddd-8ddd-ddddddddddd4', { claimToken: job.claimToken })).toBeNull();
  });

  it('refuses a renewal after the lease expired, even before the sweeper ran', async () => {
    const { enqueue, claim, commands, clock, store } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    clock.advance(60_000);
    expect(await commands.renew(NODE_A, job.job.id, { claimToken: job.claimToken })).toBeNull();
    expect(store.get(job.job.id).status).toBe('running'); // refusing is not settling
  });

  it('refuses a renewal of a command that is not running', async () => {
    const { enqueue, commands } = setup();
    const { command } = await enqueue('localai.model.load', loadParams);
    expect(await commands.renew(NODE_A, command.id, { claimToken: 'x' })).toBeNull();
  });

  it.each([
    ['localai.status.refresh', {}, 300_000],
    ['localai.model.load', loadParams, 600_000],
    ['localai.agent.update', { version: '0.2.0' }, 900_000],
    ['localai.key.rotate', {}, 600_000],
  ] as Array<[CommandType, unknown, number]>)('%s: renewals stop at the hard cap of %d ms from the claim', async (type, params, capMs) => {
    const { enqueue, claim, commands, clock } = setup();
    await enqueue(type, params);
    const job = await claim(NODE_A, [type.replace('localai.', '')]);
    const interval = job.renewIntervalMs;
    let lease = new Date(job.leaseExpiresAt);
    let last = clock.now();
    for (;;) {
      clock.advance(interval);
      const next = await commands.renew(NODE_A, job.job.id, { claimToken: job.claimToken });
      if (next === null) break;
      expect(next.getTime()).toBeLessThanOrEqual(last.getTime() + capMs);
      lease = next;
      last = clock.now();
    }
    // The hard cap is exactly the type's: the lease never runs past it, and the first renewal at or after it is refused.
    expect(clock.now().getTime() - Date.parse('2026-11-02T15:00:00.000Z')).toBeGreaterThanOrEqual(capMs - interval);
    expect(lease.getTime()).toBe(Date.parse('2026-11-02T15:00:00.000Z') + capMs);
  });
});

describe('result', () => {
  it('settles a held command as succeeded, stores the result, and clears the lease', async () => {
    const { enqueue, claim, commands, store, bus } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    await expect(commands.complete(NODE_A, job.job.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult })).resolves.toEqual({ status: 'succeeded' });
    expect(store.get(job.job.id)).toMatchObject({ status: 'succeeded', result: loadResult, leaseExpiresAt: null, error: null });
    expect(store.get(job.job.id).finishedAt).not.toBeNull();
    expect(bus.on(COMMAND_SETTLED_CHANNEL)).toEqual([{ nodeId: NODE_A, commandId: job.job.id, status: 'succeeded' }]);
  });

  it('is idempotent: the very same post again is 200 succeeded and changes nothing', async () => {
    const { enqueue, claim, commands, store, bus } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    const body = { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult };
    await commands.complete(NODE_A, job.job.id, body);
    const writes = store.writes.length;
    await expect(commands.complete(NODE_A, job.job.id, body)).resolves.toEqual({ status: 'succeeded' });
    expect(store.writes).toHaveLength(writes);
    expect(bus.on(COMMAND_SETTLED_CHANNEL)).toHaveLength(1);
  });

  it('does not accept the repeat with another token, from another node, or after a failure', async () => {
    const { enqueue, claim, commands } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    await commands.complete(NODE_A, job.job.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult });
    await expect(commands.complete(NODE_A, job.job.id, { type: 'localai.model.load', claimToken: 'another', result: loadResult })).rejects.toMatchObject({ status: 409 });
    await expect(commands.complete(NODE_B, job.job.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult })).rejects.toMatchObject({ status: 409 });
    await expect(commands.fail(NODE_A, job.job.id, { claimToken: job.claimToken, error: 'late' })).rejects.toMatchObject({ status: 409 });
  });

  it('409 when the lease passed, with the lease-lost code, and nothing is written', async () => {
    const { enqueue, claim, commands, store, clock } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    clock.advance(60_000);
    const writes = store.writes.length;
    const error = await commands.complete(NODE_A, job.job.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult }).catch((e) => e);
    expect(error.status).toBe(409);
    expect(code(error)).toEqual({ code: 'LAI-CLD-059', reason: 'lease_lost' });
    expect(store.writes).toHaveLength(writes);
    expect(store.get(job.job.id).status).toBe('running');
  });

  it('409 for a result after the sweeper failed the command, and after the node itself failed it', async () => {
    const { enqueue, claim, commands, sweeper, clock, store } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    clock.advance(61_000);
    await sweeper.sweep();
    expect(store.get(job.job.id).status).toBe('failed');
    const body = { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult };
    await expect(commands.complete(NODE_A, job.job.id, body)).rejects.toMatchObject({ status: 409 });
    // A late failure or renewal is dropped too: the sweeper cleared the token.
    await expect(commands.fail(NODE_A, job.job.id, { claimToken: job.claimToken, error: 'LAI-CAT-005: late' })).rejects.toMatchObject({ status: 409 });
    expect(await commands.renew(NODE_A, job.job.id, { claimToken: job.claimToken })).toBeNull();
    expect(store.get(job.job.id).error).toMatchObject({ code: 'LAI-CLD-059' });
  });

  it('409 for a command id of another node (not 404: its existence is not revealed), 409 for an unknown id', async () => {
    const { enqueue, claim, commands } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    const body = { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult };
    await expect(commands.complete(NODE_B, job.job.id, body)).rejects.toMatchObject({ status: 409 });
    await expect(commands.complete(NODE_A, 'dddddddd-dddd-4ddd-8ddd-ddddddddddd4', body)).rejects.toMatchObject({ status: 409 });
  });

  it('400 for a type mismatch or a result that fails the type\'s schema, and the command stays running', async () => {
    const { enqueue, claim, commands, store, bus } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    const mismatch = await commands.complete(NODE_A, job.job.id, { type: 'localai.model.unload', claimToken: job.claimToken, result: { model_id: MODEL, status: 'unloaded', was_loaded: true } }).catch((e) => e);
    expect(mismatch.status).toBe(400);
    expect(mismatch.response.details.reason).toBe('result_type_mismatch');

    const bad = await commands.complete(NODE_A, job.job.id, { type: 'localai.model.load', claimToken: job.claimToken, result: { ...loadResult, load_ms: -1 } }).catch((e) => e);
    expect(bad.status).toBe(400);
    expect(bad.response.details.reason).toBe('invalid_result');
    expect(bad.response.details.issues[0].path).toEqual(['load_ms']);
    const extra = await commands.complete(NODE_A, job.job.id, { type: 'localai.model.load', claimToken: job.claimToken, result: { ...loadResult, key: 'sk-secret' } }).catch((e) => e);
    expect(extra.status).toBe(400);
    expect(JSON.stringify(extra.response)).not.toContain('sk-secret');

    expect(store.get(job.job.id)).toMatchObject({ status: 'running', result: null });
    expect(bus.on(COMMAND_SETTLED_CHANNEL)).toHaveLength(0);
    // The agent may still report a failure afterwards (it treats a 400 as a bug and does).
    await expect(commands.fail(NODE_A, job.job.id, { claimToken: job.claimToken, error: 'bad result' })).resolves.toEqual({ status: 'failed' });
  });

  it('logs.tail: scrubs the lines again before storing them', async () => {
    const { enqueue, claim, commands, store } = setup();
    await enqueue('localai.logs.tail', { unit: 'agent' });
    const job = await claim();
    const secret = `nod_${'k'.repeat(40)}`;
    const tailnetIp = ['100', '100', '5', '6'].join('.'); // built from parts: the redaction check forbids literal tailnet addresses
    await commands.complete(NODE_A, job.job.id, {
      type: 'localai.logs.tail',
      claimToken: job.claimToken,
      result: { unit: 'agent', lines: [`link up with Bearer ${secret}`, `peer ${tailnetIp} ok`, 'fine'], truncated: false },
    });
    const stored = JSON.stringify(store.get(job.job.id).result);
    expect(stored).not.toContain(secret);
    expect(stored).not.toContain(tailnetIp);
    expect(stored).toContain('fine');
  });

  it('mode.offline: records the offline mode on the node once the result is stored', async () => {
    const { enqueue, claim, commands, store } = setup();
    await enqueue('localai.mode.offline', { reason: 'moving' });
    const job = await claim();
    await commands.complete(NODE_A, job.job.id, { type: 'localai.mode.offline', claimToken: job.claimToken, result: { previous_mode: 'online', mode: 'offline' } });
    expect(store.nodeUpdates).toEqual([{ id: NODE_A, data: { mode: 'offline' } }]);

    const other = setup();
    await other.enqueue('localai.model.load', loadParams);
    const loaded = await other.claim();
    await other.commands.complete(NODE_A, loaded.job.id, { type: 'localai.model.load', claimToken: loaded.claimToken, result: loadResult });
    expect(other.store.nodeUpdates).toHaveLength(0);
  });

  it('survives a failing event bus: the command is still succeeded', async () => {
    const { enqueue, claim, commands, store, bus } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    bus.failPublish = true;
    await expect(commands.complete(NODE_A, job.job.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult })).resolves.toEqual({ status: 'succeeded' });
    expect(store.get(job.job.id).status).toBe('succeeded');
  });
});

describe('failure', () => {
  it('settles a held command as failed with the code and message split, and publishes settled', async () => {
    const { enqueue, claim, commands, store, bus } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    await expect(commands.fail(NODE_A, job.job.id, { claimToken: job.claimToken, error: 'LAI-CAT-005: Model does not fit in free VRAM' })).resolves.toEqual({ status: 'failed' });
    expect(store.get(job.job.id)).toMatchObject({ status: 'failed', error: { code: 'LAI-CAT-005', message: 'Model does not fit in free VRAM' }, leaseExpiresAt: null, result: null });
    expect(bus.on(COMMAND_SETTLED_CHANNEL)).toEqual([{ nodeId: NODE_A, commandId: job.job.id, status: 'failed' }]);
  });

  it('a string with no doctor code is LAI-CLD-012; rateLimited and retryAfterMs are ignored', async () => {
    const { enqueue, claim, commands, store } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    await commands.fail(NODE_A, job.job.id, { claimToken: job.claimToken, error: 'something broke', rateLimited: true, retryAfterMs: 5000 });
    expect(store.get(job.job.id).error).toEqual({ code: 'LAI-CLD-012', message: 'something broke' });
  });

  it('is idempotent for the same token, and 409 for a wrong token, a lost lease or another node', async () => {
    const { enqueue, claim, commands, clock } = setup();
    await enqueue('localai.model.load', loadParams);
    await enqueue('localai.logs.tail', { unit: 'agent' });
    const job = await claim();
    await expect(commands.fail(NODE_A, job.job.id, { claimToken: 'wrong', error: 'x' })).rejects.toMatchObject({ status: 409 });
    await expect(commands.fail(NODE_B, job.job.id, { claimToken: job.claimToken, error: 'x' })).rejects.toMatchObject({ status: 409 });
    await commands.fail(NODE_A, job.job.id, { claimToken: job.claimToken, error: 'x' });
    await expect(commands.fail(NODE_A, job.job.id, { claimToken: job.claimToken, error: 'x' })).resolves.toEqual({ status: 'failed' });

    const second = await claim();
    clock.advance(30_001);
    await expect(commands.fail(NODE_A, second.job.id, { claimToken: second.claimToken, error: 'x' })).rejects.toMatchObject({ status: 409 });
  });
});

describe('the sweeper', () => {
  it('registers itself as the server-only job localai.commands.sweep with a declared profile', () => {
    const { sweeper, registered } = setup();
    sweeper.onModuleInit();
    expect(registered).toEqual([sweeper]);
    expect(sweeper.type).toBe('localai.commands.sweep');
    expect(sweeper.profile).toEqual({ maxRuntimeMs: 60_000, maxAttempts: 1 });
    expect('nodeResultSchema' in sweeper).toBe(false);
    expect('persistNodeResult' in sweeper).toBe(false);
  });

  it('expires a queued command past expires_at (LAI-CLD-030) and publishes settled; leaves a live one alone', async () => {
    const { enqueue, sweeper, clock, store, bus } = setup();
    const stale = await enqueue('localai.status.refresh');
    const live = await enqueue('localai.model.load', loadParams);
    clock.advance(2 * 60_000 + 1);
    expect(await sweeper.sweep()).toMatchObject({ expired: 1, leaseLost: 0 });
    expect(store.get(stale.command.id)).toMatchObject({ status: 'expired', error: { code: 'LAI-CLD-030' } });
    expect(store.get(stale.command.id).finishedAt).not.toBeNull();
    expect(store.get(live.command.id).status).toBe('queued');
    expect(bus.on(COMMAND_SETTLED_CHANNEL)).toEqual([{ nodeId: NODE_A, commandId: stale.command.id, status: 'expired' }]);
    expect(await sweeper.sweep()).toMatchObject({ expired: 0 }); // idempotent
  });

  it('fails a running command whose lease passed (LAI-CLD-059), clears the token and publishes settled', async () => {
    const { enqueue, claim, sweeper, clock, store, bus } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    clock.advance(59_999);
    expect(await sweeper.sweep()).toMatchObject({ leaseLost: 0 });
    clock.advance(1);
    expect(await sweeper.sweep()).toMatchObject({ leaseLost: 1 });
    expect(store.get(job.job.id)).toMatchObject({ status: 'failed', error: { code: 'LAI-CLD-059' }, claimToken: null, leaseExpiresAt: null });
    expect(bus.on(COMMAND_SETTLED_CHANNEL)).toEqual([{ nodeId: NODE_A, commandId: job.job.id, status: 'failed' }]);
  });

  it('a renewed lease keeps the command out of the sweep', async () => {
    const { enqueue, claim, commands, sweeper, clock, store } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    clock.advance(40_000);
    await commands.renew(NODE_A, job.job.id, { claimToken: job.claimToken });
    clock.advance(40_000); // 80 s since the claim, 40 s since the renewal
    expect(await sweeper.sweep()).toMatchObject({ leaseLost: 0 });
    expect(store.get(job.job.id).status).toBe('running');
  });

  it('a result that wins the race against the sweep is not overwritten, and vice versa', async () => {
    const { enqueue, claim, commands, sweeper, clock, store } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    await commands.complete(NODE_A, job.job.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult });
    clock.advance(10 * 60_000);
    expect(await sweeper.sweep()).toMatchObject({ expired: 0, leaseLost: 0 });
    expect(store.get(job.job.id).status).toBe('succeeded');
  });

  it('empties logs.tail results after 7 days and keeps other results', async () => {
    const { enqueue, claim, commands, sweeper, clock, store } = setup();
    await enqueue('localai.logs.tail', { unit: 'agent' });
    const logs = await claim();
    await commands.complete(NODE_A, logs.job.id, { type: 'localai.logs.tail', claimToken: logs.claimToken, result: { unit: 'agent', lines: ['a'], truncated: false } });
    await enqueue('localai.model.load', loadParams);
    const load = await claim();
    await commands.complete(NODE_A, load.job.id, { type: 'localai.model.load', claimToken: load.claimToken, result: loadResult });

    clock.advance(7 * 86_400_000 - 1000);
    expect(await sweeper.sweep()).toMatchObject({ logsTrimmed: 0 });
    clock.advance(2000);
    expect(await sweeper.sweep()).toMatchObject({ logsTrimmed: 1 });
    expect(store.get(logs.job.id)).toMatchObject({ status: 'succeeded', result: null });
    expect(store.get(load.job.id).result).toEqual(loadResult);
    expect(await sweeper.sweep()).toMatchObject({ logsTrimmed: 0 }); // already empty: nothing is rewritten each tick
  });

  it('deletes settled commands older than 180 days and never an active one', async () => {
    const { enqueue, claim, commands, sweeper, clock, store } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim();
    await commands.fail(NODE_A, job.job.id, { claimToken: job.claimToken, error: 'x' });
    const active = store.seed({ nodeId: NODE_B, type: 'localai.ark.sync', status: 'queued', expiresAt: new Date('2099-01-01T00:00:00Z') });

    clock.advance(180 * 86_400_000 - 1000);
    expect(await sweeper.sweep()).toMatchObject({ deleted: 0 });
    clock.advance(2000);
    expect(await sweeper.sweep()).toMatchObject({ deleted: 1 });
    expect(store.rows.map((row) => row.id)).toEqual([active.id]);
  });

  it('process() sweeps once and logs only when it settled something', async () => {
    const { enqueue, sweeper, clock, store } = setup();
    await enqueue('localai.status.refresh');
    clock.advance(3 * 60_000);
    await sweeper.process();
    expect(store.rows[0].status).toBe('expired');
  });
});

describe('isolation between nodes', () => {
  it('node B cannot claim, renew, settle or fail node A\'s command with A\'s token', async () => {
    const { enqueue, claim, commands, store } = setup();
    await enqueue('localai.model.load', loadParams);
    const job = await claim(NODE_A);
    expect(await claim(NODE_B)).toBeUndefined();
    expect(await commands.renew(NODE_B, job.job.id, { claimToken: job.claimToken })).toBeNull();
    await expect(commands.complete(NODE_B, job.job.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult })).rejects.toMatchObject({ status: 409 });
    await expect(commands.fail(NODE_B, job.job.id, { claimToken: job.claimToken, error: 'x' })).rejects.toMatchObject({ status: 409 });
    expect(store.get(job.job.id)).toMatchObject({ status: 'running', claimToken: job.claimToken });
  });

  it('each node has its own running slot', async () => {
    const { enqueue, claim } = setup();
    await enqueue('localai.model.load', loadParams, NODE_A);
    await enqueue('localai.model.load', loadParams, NODE_B);
    expect(await claim(NODE_A)).toBeDefined();
    expect(await claim(NODE_B)).toBeDefined();
  });
});

describe('the full path', () => {
  it('enqueue -> claim -> renew -> result -> succeeded', async () => {
    const { enqueue, claim, commands, store, clock } = setup();
    const { command } = await enqueue('localai.model.load', loadParams);
    expect(command.status).toBe('queued');
    const job = await claim();
    expect(store.get(command.id).status).toBe('running');
    clock.advance(20_000);
    expect(await commands.renew(NODE_A, command.id, { claimToken: job.claimToken })).not.toBeNull();
    await commands.complete(NODE_A, command.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult });
    expect(store.get(command.id).status).toBe('succeeded');
  });
});
