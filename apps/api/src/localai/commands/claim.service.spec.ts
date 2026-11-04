// The claim long poll against a scripted repository and a real (in-process) bus: wake-up, timeout, one in-flight
// claim per node, abort and shutdown. The SQL itself is `test/localai/commands.db.spec.ts`.
import type { LocalaiCommand } from '@prisma/client';

import { FakeBus } from '../../../test/localai/support/fake-command-store';
import { ClaimService, CLAIM_WAIT_MAX_MS, toClaimedJob } from './claim.service';
import type { ClaimAttempt } from './claim.sql';
import { COMMAND_ENQUEUED_CHANNEL, systemLocalaiClock } from './command-events';

const NODE = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1';
const OTHER = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb2';

function row(overrides: Partial<LocalaiCommand> = {}): LocalaiCommand {
  return {
    id: '6b3c9a52-7d1e-4c3a-9f0b-2a5e8d417c60',
    nodeId: NODE,
    type: 'localai.model.load',
    params: { model_id: 'qwen3.6-35b-a3b-q4km', wait: true, unload_others: true, set_default: false },
    status: 'running',
    dedupKey: null,
    source: 'ui',
    requestedById: null,
    attempts: 1,
    claimToken: '1f0e8b7a-3c2d-4e9f-8a6b-5d4c3b2a1f0e',
    leaseExpiresAt: new Date('2026-11-02T15:05:00.000Z'),
    claimedAt: new Date('2026-11-02T15:04:00.000Z'),
    expiresAt: new Date('2026-11-02T15:19:00.000Z'),
    finishedAt: null,
    result: null,
    error: null,
    traceparent: null,
    createdAt: new Date('2026-11-02T15:03:00.000Z'),
    ...overrides,
  };
}

/** A repository that answers from a queue of scripted results (null = nothing to claim). */
function scripted(...answers: Array<LocalaiCommand | null | Error | (() => Promise<LocalaiCommand | null>)>) {
  const calls: ClaimAttempt[] = [];
  const repository = {
    claimNext: jest.fn(async (attempt: ClaimAttempt) => {
      calls.push(attempt);
      const next = answers.length > 1 ? answers.shift() : answers[0];
      if (next instanceof Error) throw next;
      if (typeof next === 'function') return next();
      return next ?? null;
    }),
  };
  return { repository, calls };
}

function service(repository: unknown, bus = new FakeBus()) {
  return { claims: new ClaimService(repository as never, bus as never, systemLocalaiClock), bus };
}

const request = (overrides: Record<string, unknown> = {}) => ({ types: ['model.load', 'logs.tail'], limit: 1, waitMs: 0, ...overrides });
const timed = async <T>(work: () => Promise<T>) => {
  const start = Date.now();
  const value = await work();
  return { value, ms: Date.now() - start };
};

describe('ClaimService', () => {
  it('answers at once with the claimed command and asks the repository with the prefixed types and the type\'s lease', async () => {
    const { repository, calls } = scripted(row());
    const { claims, bus } = service(repository);
    const { value, ms } = await timed(() => claims.claim(NODE, request({ waitMs: 25000 })));
    expect(ms).toBeLessThan(150);
    expect(value.jobs).toHaveLength(1);
    expect(value.jobs[0]).toMatchObject({ job: { id: row().id, type: 'localai.model.load', attempts: 1 }, claimToken: row().claimToken, renewIntervalMs: 20000, traceparent: null });
    expect(calls).toHaveLength(1);
    expect(calls[0].nodeId).toBe(NODE);
    expect([...calls[0].types]).toEqual(['localai.model.load', 'localai.logs.tail']);
    expect(calls[0].leaseMsOf('localai.model.load')).toBe(60_000);
    expect(calls[0].leaseMsOf('localai.logs.tail')).toBe(30_000);
    expect(calls[0].leaseMsOf('localai.agent.update')).toBe(300_000);
    expect(bus.subscriberCount(COMMAND_ENQUEUED_CHANNEL)).toBe(0); // the subscription is gone with the request
  });

  it('with waitMs 0 makes one attempt and answers empty', async () => {
    const { repository } = scripted(null);
    const { claims } = service(repository);
    const { value, ms } = await timed(() => claims.claim(NODE, request()));
    expect(value).toEqual({ jobs: [] });
    expect(repository.claimNext).toHaveBeenCalledTimes(1);
    expect(ms).toBeLessThan(150);
  });

  it('holds the request and wakes on an enqueue event for THIS node, within 200 ms', async () => {
    const { repository } = scripted(null, row());
    const { claims, bus } = service(repository);
    const pending = claims.claim(NODE, request({ waitMs: 5000 }));
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(repository.claimNext).toHaveBeenCalledTimes(1); // waiting, not polling
    expect(bus.subscriberCount(COMMAND_ENQUEUED_CHANNEL)).toBe(1);

    const woke = Date.now();
    await bus.publish(COMMAND_ENQUEUED_CHANNEL, { nodeId: NODE });
    const answer = await pending;
    expect(Date.now() - woke).toBeLessThan(200);
    expect(answer.jobs).toHaveLength(1);
    expect(repository.claimNext).toHaveBeenCalledTimes(2);
    expect(bus.subscriberCount(COMMAND_ENQUEUED_CHANNEL)).toBe(0);
  });

  it('ignores an enqueue event for another node, and waits out the time when nothing arrives', async () => {
    const { repository } = scripted(null);
    const { claims, bus } = service(repository);
    const pending = timed(() => claims.claim(NODE, request({ waitMs: 150 })));
    await new Promise((resolve) => setTimeout(resolve, 30));
    await bus.publish(COMMAND_ENQUEUED_CHANNEL, { nodeId: OTHER });
    const { value, ms } = await pending;
    expect(value).toEqual({ jobs: [] });
    expect(ms).toBeGreaterThanOrEqual(130);
    expect(ms).toBeLessThan(400);
    expect(repository.claimNext).toHaveBeenCalledTimes(1); // the other node's event did not trigger another attempt
    expect(bus.subscriberCount(COMMAND_ENQUEUED_CHANNEL)).toBe(0);
  });

  it('keeps waiting after a wake-up that finds nothing for it (an event for a type it did not ask for)', async () => {
    const { repository } = scripted(null, null, row());
    const { claims, bus } = service(repository);
    const pending = claims.claim(NODE, request({ waitMs: 5000 }));
    await new Promise((resolve) => setTimeout(resolve, 20));
    await bus.publish(COMMAND_ENQUEUED_CHANNEL, { nodeId: NODE });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(repository.claimNext).toHaveBeenCalledTimes(2);
    await bus.publish(COMMAND_ENQUEUED_CHANNEL, { nodeId: NODE });
    expect((await pending).jobs).toHaveLength(1);
    expect(repository.claimNext).toHaveBeenCalledTimes(3);
  });

  it('does not lose an event that arrives while the first attempt is still running', async () => {
    const bus = new FakeBus();
    let release: (value: LocalaiCommand | null) => void = () => undefined;
    const first = () => new Promise<LocalaiCommand | null>((resolve) => (release = resolve));
    const { repository } = scripted(first, row());
    const { claims } = service(repository, bus);
    const pending = claims.claim(NODE, request({ waitMs: 5000 }));
    await new Promise((resolve) => setTimeout(resolve, 10));
    await bus.publish(COMMAND_ENQUEUED_CHANNEL, { nodeId: NODE }); // enqueued after the attempt's read, before its answer
    release(null);
    const { ms } = await timed(() => pending);
    expect(ms).toBeLessThan(200);
    expect(repository.claimNext).toHaveBeenCalledTimes(2);
  });

  it('answers a second concurrent claim from the same node empty at once, without a second attempt', async () => {
    const { repository } = scripted(null);
    const { claims } = service(repository);
    const first = claims.claim(NODE, request({ waitMs: 200 }));
    await new Promise((resolve) => setTimeout(resolve, 20));
    const { value, ms } = await timed(() => claims.claim(NODE, request({ waitMs: 5000 })));
    expect(value).toEqual({ jobs: [] });
    expect(ms).toBeLessThan(100);
    expect(repository.claimNext).toHaveBeenCalledTimes(1);
    await first;
    // Once the first is done the node may claim again.
    await claims.claim(NODE, request());
    expect(repository.claimNext).toHaveBeenCalledTimes(2);
  });

  it('lets two different nodes poll at the same time', async () => {
    const { repository } = scripted(null);
    const { claims, bus } = service(repository);
    const a = claims.claim(NODE, request({ waitMs: 5000 }));
    const b = claims.claim(OTHER, request({ waitMs: 5000 }));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(bus.subscriberCount(COMMAND_ENQUEUED_CHANNEL)).toBe(2);
    expect(repository.claimNext).toHaveBeenCalledTimes(2);
    claims.beforeApplicationShutdown();
    await Promise.all([a, b]);
  });

  it('ends early, empty, when the client goes away; and does not even query when it already has', async () => {
    const { repository } = scripted(null);
    const { claims, bus } = service(repository);
    const gone = new AbortController();
    const pending = claims.claim(NODE, request({ waitMs: 5000 }), gone.signal);
    await new Promise((resolve) => setTimeout(resolve, 20));
    gone.abort();
    const { value, ms } = await timed(() => pending);
    expect(value).toEqual({ jobs: [] });
    expect(ms).toBeLessThan(100);
    expect(bus.subscriberCount(COMMAND_ENQUEUED_CHANNEL)).toBe(0);

    repository.claimNext.mockClear();
    expect(await claims.claim(NODE, request(), gone.signal)).toEqual({ jobs: [] });
    expect(repository.claimNext).not.toHaveBeenCalled();
    // ... and the node is not stuck "in progress" afterwards.
    await claims.claim(NODE, request());
    expect(repository.claimNext).toHaveBeenCalledTimes(1);
  });

  it('shutdown ends every held poll at once and refuses new ones', async () => {
    const { repository } = scripted(null);
    const { claims } = service(repository);
    const held = claims.claim(NODE, request({ waitMs: 25000 }));
    await new Promise((resolve) => setTimeout(resolve, 20));
    const { value, ms } = await timed(async () => {
      claims.beforeApplicationShutdown();
      return held;
    });
    expect(value).toEqual({ jobs: [] });
    expect(ms).toBeLessThan(100);
    repository.claimNext.mockClear();
    expect(await claims.claim(OTHER, request({ waitMs: 25000 }))).toEqual({ jobs: [] });
    expect(repository.claimNext).not.toHaveBeenCalled();
  });

  it('holds no timer or subscription after an error, and the node can claim again', async () => {
    const { repository } = scripted(new Error('database down'));
    const { claims, bus } = service(repository);
    await expect(claims.claim(NODE, request({ waitMs: 5000 }))).rejects.toThrow('database down');
    expect(bus.subscriberCount(COMMAND_ENQUEUED_CHANNEL)).toBe(0);
    repository.claimNext.mockResolvedValueOnce(row());
    expect((await claims.claim(NODE, request())).jobs).toHaveLength(1);
  });

  it('answers empty without a query when no requested name is a known type', async () => {
    const { repository } = scripted(row());
    const { claims } = service(repository);
    expect(await claims.claim(NODE, request({ types: ['not.a.type', 'localai.model.load'] }))).toEqual({ jobs: [] });
    expect(await claims.claim(NODE, request({ types: [] }))).toEqual({ jobs: [] });
    expect(repository.claimNext).not.toHaveBeenCalled();
  });

  it('clamps waitMs to [0, claimWaitMaxMs] and treats a missing value as 0', () => {
    const { claims } = service(scripted(null).repository);
    expect(CLAIM_WAIT_MAX_MS).toBe(25000);
    expect(claims.clampWait(undefined)).toBe(0);
    expect(claims.clampWait(-5)).toBe(0);
    expect(claims.clampWait(100)).toBe(100);
    expect(claims.clampWait(25000)).toBe(25000);
    expect(claims.clampWait(99999)).toBe(25000);
    expect(claims.clampWait(Number.NaN)).toBe(0);
    expect(claims.clampWait(1.9)).toBe(1);
  });
});

describe('toClaimedJob', () => {
  it('refuses to build an answer for a row that was not properly claimed', () => {
    expect(() => toClaimedJob(row({ claimToken: null }))).toThrow(/claimed without/);
    expect(() => toClaimedJob(row({ leaseExpiresAt: null }))).toThrow(/claimed without/);
    expect(() => toClaimedJob(row({ type: 'localai.shell.exec' }))).toThrow(/claimed without/);
  });

  it('passes the stored traceparent through', () => {
    expect(toClaimedJob(row({ traceparent: '00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01' })).traceparent).toMatch(/^00-/);
  });
});
