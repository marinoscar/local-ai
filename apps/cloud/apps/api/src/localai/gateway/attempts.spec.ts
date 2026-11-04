// The attempt loop's seams, with a fake transport and a fake routing port (the real transport and table are exercised by
// test/localai/gateway.e2e.spec.ts).
import { Readable } from 'node:stream';

import { AttemptLoop, RequestAbortReason, TableCandidateSource, type AttemptHooks, type AttemptRequest, type CandidateSource, type RoutingPort } from './attempts';
import { GatewayError } from './errors';
import { DEFAULT_MODEL_POLICY } from './model-resolver';
import { candidate } from './routing/routing.fixtures';
import type { SelectedCandidate, SelectResult } from './routing/routing.types';
import { UpstreamAbortedError, UpstreamTransportError } from './upstream/errors';
import type { UpstreamCall, UpstreamResponse, UpstreamTransport } from './upstream/client';

const model = { modelId: 'model-a', requestedModel: 'model-a', policy: DEFAULT_MODEL_POLICY, roles: null, inApprovedCatalog: true };

function reply(status: number, body = '{}'): UpstreamResponse {
  const stream = Readable.from([Buffer.from(body)]);
  return { status, contentType: 'application/json', body: stream, ttfbMs: 5, destroy: () => stream.destroy() };
}

class FakeRouting implements RoutingPort {
  events: string[] = [];
  candidates: SelectedCandidate[] = [candidate('n1'), candidate('n2'), candidate('n3')];
  select(_modelId: string, _policy: unknown, tried: ReadonlySet<string>): SelectResult {
    const open = this.candidates.filter((c) => !tried.has(c.nodeId));
    return open.length ? { kind: 'candidates', candidates: open } : { kind: 'none_loaded' };
  }
  unavailable() {
    return { status: 503 as const, code: 'LAI-GW-021' as const };
  }
  acquire(ref: { nodeId: string }) { this.events.push(`acquire:${ref.nodeId}`); }
  release(ref: { nodeId: string }) { this.events.push(`release:${ref.nodeId}`); }
  markSuspect(ref: { nodeId: string }) { this.events.push(`suspect:${ref.nodeId}`); }
  markKeyInvalid(ref: { nodeId: string }) { this.events.push(`keyinvalid:${ref.nodeId}`); }
  markUnloaded(nodeId: string) { this.events.push(`unloaded:${nodeId}`); }
  recordTtfb(ref: { nodeId: string }, ms: number) { this.events.push(`ttfb:${ref.nodeId}:${ms}`); }
}

function request(overrides: Partial<AttemptRequest> = {}): AttemptRequest {
  return {
    requestId: 'req-1',
    model,
    path: '/v1/chat/completions',
    body: Buffer.from('{}'),
    accept: 'application/json',
    maxAttempts: 3,
    timeouts: { connectMs: 100, firstByteMs: 100, idleMs: 100 },
    signal: new AbortController().signal,
    ...overrides,
  };
}

function setup(script: Array<UpstreamResponse | Error>, extra: { hooks?: AttemptHooks; source?: CandidateSource } = {}) {
  const routing = new FakeRouting();
  const calls: UpstreamCall[] = [];
  const warnings: string[] = [];
  const queue = [...script];
  const transport: UpstreamTransport = {
    async call(call) {
      calls.push(call);
      const next = queue.shift();
      if (!next) throw new Error('script exhausted');
      if (next instanceof Error) throw next;
      return next;
    },
  };
  const loop = new AttemptLoop({ routing, keys: { get: async (id) => `key-for-${id}` }, transport, log: { warn: (line) => warnings.push(line) }, ...extra });
  return { loop, routing, calls, warnings };
}

describe('AttemptLoop', () => {
  it('commits on the first 2xx, holds inflight until release(), records the first byte', async () => {
    const { loop, routing, calls } = setup([reply(200)]);
    const outcome = await loop.run(request());
    expect(outcome).toMatchObject({ ok: true, kind: 'success', attempts: 1 });
    expect(routing.events).toEqual(['acquire:n1', 'ttfb:n1:5']);
    expect(calls[0]).toMatchObject({ apiKey: 'key-for-n1', publicHost: 'n1.example.test', path: '/v1/chat/completions', requestId: 'req-1' });
    if (outcome.ok && outcome.kind === 'success') {
      outcome.release();
      outcome.release();
    }
    expect(routing.events.filter((e) => e.startsWith('release'))).toEqual(['release:n1']);
  });

  it('applies the table of marks: 5xx suspect, 401 keyInvalid, model-not-loaded unloaded, transport suspect', async () => {
    const { loop, routing, warnings } = setup([reply(503), reply(401), reply(400, '{"error":{"message":"model not loaded"}}')]);
    const outcome = await loop.run(request({ maxAttempts: 3 }));
    expect(outcome).toMatchObject({ ok: false, reason: 'error', attempts: 3 });
    expect(routing.events).toEqual(['acquire:n1', 'suspect:n1', 'release:n1', 'acquire:n2', 'keyinvalid:n2', 'release:n2', 'acquire:n3', 'unloaded:n3', 'release:n3']);
    expect(warnings.map((w) => JSON.parse(w))).toEqual([
      { event: 'gateway.attempt_failed', request_id: 'req-1', node_id: 'n1', attempt: 1, reason: 'status_5xx', upstream_status: 503 },
      { event: 'gateway.attempt_failed', request_id: 'req-1', node_id: 'n2', attempt: 2, reason: 'status_401', upstream_status: 401 },
      { event: 'gateway.attempt_failed', request_id: 'req-1', node_id: 'n3', attempt: 3, reason: 'model_not_loaded', upstream_status: 400 },
    ]);
  });

  it.each([
    ['connect', 'LAI-GW-040'],
    ['tls', 'LAI-GW-040'],
    ['timeout', 'LAI-GW-044'],
  ] as const)('a %s failure on every node ends as %s', async (reason, code) => {
    const { loop, routing } = setup([new UpstreamTransportError(reason, null), new UpstreamTransportError(reason, null)]);
    const outcome = await loop.run(request({ maxAttempts: 2 }));
    expect(outcome.ok === false && outcome.reason === 'error' && outcome.error.localaiCode).toBe(code);
    expect(routing.events.filter((e) => e.startsWith('suspect'))).toHaveLength(2);
  });

  it('never leaks the node key into the failure log', async () => {
    const { loop, warnings } = setup([reply(503)]);
    await loop.run(request({ maxAttempts: 1 }));
    expect(warnings.join('')).not.toContain('key-for-');
  });

  it('returns a final 4xx as rejected, releasing inflight and not touching the marks', async () => {
    const { loop, routing } = setup([reply(422, '{"error":{"message":"bad"}}')]);
    const outcome = await loop.run(request());
    expect(outcome).toMatchObject({ ok: true, kind: 'rejected', status: 422, bodyText: '{"error":{"message":"bad"}}' });
    expect(routing.events).toEqual(['acquire:n1', 'release:n1']);
  });

  it('stops without a retry when the signal aborted (client) and reports a total timeout as 044', async () => {
    const client = new AbortController();
    const first = setup([new UpstreamAbortedError()]);
    client.abort(new RequestAbortReason('client'));
    expect(await first.loop.run(request({ signal: client.signal }))).toMatchObject({ ok: false, reason: 'aborted' });

    const total = new AbortController();
    const second = setup([new UpstreamAbortedError()]);
    total.abort(new RequestAbortReason('total'));
    const outcome = await second.loop.run(request({ signal: total.signal }));
    expect(outcome.ok === false && outcome.reason === 'error' && outcome.error.localaiCode).toBe('LAI-GW-044');
  });

  it('fails with LAI-GW-050 and no retry when the key store is unreadable', async () => {
    const routing = new FakeRouting();
    const loop = new AttemptLoop({ routing, keys: { get: async () => { throw new Error('boom'); } }, transport: { call: async () => reply(200) }, log: { warn: () => undefined } });
    const outcome = await loop.run(request());
    expect(outcome.ok === false && outcome.reason === 'error' && outcome.error.localaiCode).toBe('LAI-GW-050');
    expect(routing.events).toEqual(['acquire:n1', 'release:n1']);
  });

  it('treats a missing key as key-invalid and moves on without calling the node', async () => {
    const routing = new FakeRouting();
    const calls: string[] = [];
    const loop = new AttemptLoop({
      routing,
      keys: { get: async (id) => (id === 'n1' ? null : 'k') },
      transport: { call: async (call) => { calls.push(call.nodeId); return reply(200); } },
      log: { warn: () => undefined },
    });
    expect(await loop.run(request())).toMatchObject({ ok: true, attempts: 2 });
    expect(calls).toEqual(['n2']);
    expect(routing.events).toContain('keyinvalid:n1');
  });

  it('calls the hooks in order and swallows a throwing observer', async () => {
    const order: string[] = [];
    const hooks: AttemptHooks = {
      beforeAttempt: ({ attempt, candidate: c }) => void order.push(`before:${attempt}:${c.nodeId}`),
      onAttemptFailed: (failure) => {
        order.push(`failed:${failure.nodeId}:${failure.reason}`);
        throw new Error('observer bug');
      },
      onFirstByte: ({ attempt, ttfbMs, status }) => {
        order.push(`first:${attempt}:${ttfbMs}:${status}`);
        throw new Error('observer bug');
      },
    };
    const { loop } = setup([reply(500), reply(200)], { hooks });
    expect(await loop.run(request())).toMatchObject({ ok: true, attempts: 2 });
    expect(order).toEqual(['before:1:n1', 'failed:n1:status_5xx', 'before:2:n2', 'first:2:5:200']);
  });

  it('asks a replaceable CandidateSource for every attempt (the GW-09 seam)', async () => {
    const asked: Array<{ attempt: number; tried: string[]; failures: number }> = [];
    const source: CandidateSource = {
      async next({ attempt, tried, failures }) {
        asked.push({ attempt, tried: [...tried], failures: failures.length });
        return attempt === 1 ? { kind: 'candidate', candidate: candidate('only') } : { kind: 'none', error: new GatewayError('LAI-GW-022') };
      },
    };
    const { loop } = setup([reply(503)], { source });
    const outcome = await loop.run(request());
    expect(outcome.ok === false && outcome.reason === 'error' && outcome.error.localaiCode).toBe('LAI-GW-022');
    expect(asked).toEqual([{ attempt: 1, tried: [], failures: 0 }, { attempt: 2, tried: ['only'], failures: 1 }]);
  });
});

describe('TableCandidateSource', () => {
  const ask = (select: SelectResult, attempt: number) =>
    new TableCandidateSource({ select: () => select, unavailable: () => ({ status: 404, code: 'LAI-GW-020' }) }).next({ model, tried: new Set(), attempt, failures: [], signal: new AbortController().signal });

  it('first attempt: nothing loaded is the 404/503 of step 4, saturated is LAI-GW-023', async () => {
    expect(await ask({ kind: 'none_loaded' }, 1)).toMatchObject({ kind: 'none', error: { localaiCode: 'LAI-GW-020' } });
    expect(await ask({ kind: 'saturated' }, 1)).toMatchObject({ kind: 'none', error: { localaiCode: 'LAI-GW-023' } });
  });

  it('later attempts: no node left means "report the failures"', async () => {
    expect(await ask({ kind: 'none_loaded' }, 2)).toEqual({ kind: 'none', error: null });
    expect(await ask({ kind: 'saturated' }, 3)).toEqual({ kind: 'none', error: null });
  });

  it('hands out the best candidate', async () => {
    const best = candidate('n1');
    expect(await ask({ kind: 'candidates', candidates: [best, candidate('n2')] }, 1)).toEqual({ kind: 'candidate', candidate: best });
  });
});
