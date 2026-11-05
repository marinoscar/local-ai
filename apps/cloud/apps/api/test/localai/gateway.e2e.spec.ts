// The non-stream proxy against REAL local HTTP servers acting as llama-servers (docs/specs/gateway.md sections 3.5 and
// 4.2): the Fastify gateway scope (fakes for auth), the real `GatewayService`, `AttemptLoop`, `UpstreamClient` and
// `RoutingTable` (over an in-memory source). Only the key store, the model resolver and the settings are fakes.
import { randomBytes } from 'node:crypto';
import { request as httpRequest } from 'node:http';
import type { AddressInfo } from 'node:net';

import { Logger } from '@nestjs/common';

import { AttemptLoop } from '../../src/localai/gateway/attempts';
import { GatewayService } from '../../src/localai/gateway/gateway.service';
import { proxyHandlers } from '../../src/localai/gateway/handlers';
import { buildGateway, type TestGateway } from '../../src/localai/gateway/gateway.testkit';
import type { AdmissionInput, AdmissionTicket, GatewayLimits, SettleInfo } from '../../src/localai/gateway/limits';
import { DEFAULT_MODEL_POLICY, type GatewayModelResolver, type ResolvedGatewayModel } from '../../src/localai/gateway/model-resolver';
import { GatewayError } from '../../src/localai/gateway/errors';
import { keyAllows } from '../../src/localai/gateway/models/resolve';
import { DEFAULT_GATEWAY_TIMEOUTS, type ProxySettings, type ProxySettingsReader } from '../../src/localai/gateway/proxy-settings';
import { FakeBus, FakeClock, model, node } from '../../src/localai/gateway/routing/routing.fixtures';
import { RoutingTable } from '../../src/localai/gateway/routing/routing-table';
import type { SourceNodeRow } from '../../src/localai/gateway/routing/build-candidates';
import { UpstreamClient } from '../../src/localai/gateway/upstream/client';
import type { UsageRecord, UsageSink } from '../../src/localai/gateway/usage/usage-sink';
import { closedOrigin, FakeLlama, okBehaviour, respondJson, type FakeLlamaBehaviour } from '../fakes/fake-llama';

beforeAll(() => {
  jest.spyOn(Logger.prototype, 'debug').mockImplementation(() => undefined);
  jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
});
afterAll(() => jest.restoreAllMocks());

const CHAT = { model: 'model-a', messages: [{ role: 'user', content: 'Hola' }] };

class FakeResolver implements GatewayModelResolver {
  async resolve(requested: string, key: { allowedModels: string[] }): Promise<ResolvedGatewayModel> {
    const aliases: Record<string, string> = { 'lab-model': 'model-a', 'short-fb': 'model-a' };
    const modelId = aliases[requested] ?? requested;
    if (!['model-a', 'embed-a', 'unloaded-a'].includes(modelId)) throw new GatewayError('LAI-GW-020');
    if (!keyAllows(key, requested, { id: modelId })) throw new GatewayError('LAI-GW-012');
    return {
      modelId,
      requestedModel: requested,
      policy: requested === 'short-fb' ? { ...DEFAULT_MODEL_POLICY, firstByteTimeoutS: 1 } : DEFAULT_MODEL_POLICY,
      roles: modelId === 'embed-a' ? ['embedding'] : ['chat'],
      inApprovedCatalog: true,
    };
  }
}

class RecordingSink implements UsageSink {
  records: UsageRecord[] = [];
  record(record: UsageRecord): void {
    this.records.push(record);
  }
}

class RecordingLimits implements GatewayLimits {
  admitted: AdmissionInput[] = [];
  settled: SettleInfo[] = [];
  async admit(input: AdmissionInput): Promise<AdmissionTicket> {
    this.admitted.push(input);
    return { headers: () => ({ 'x-ratelimit-limit-requests': '60' }), settle: (info) => void this.settled.push(info) };
  }
}

interface Lab {
  gw: TestGateway;
  table: RoutingTable;
  sink: RecordingSink;
  limits: RecordingLimits;
  settings: ProxySettings;
  nodeKeys: Map<string, string | null>;
  llamas: Record<string, FakeLlama>;
  origins: Map<string, string>;
  client: UpstreamClient;
  post(path: string, body: unknown, headers?: Record<string, string>): Promise<{ status: number; headers: Record<string, unknown>; json: any; text: string }>;
  cleanup(): Promise<void>;
}

/** Nodes in priority order: the first is tried first. */
async function lab(nodes: Array<{ id: string; behaviour?: FakeLlamaBehaviour; models?: string[] }>, tune: (settings: ProxySettings) => void = () => undefined): Promise<Lab> {
  const llamas: Record<string, FakeLlama> = {};
  const origins = new Map<string, string>();
  const nodeKeys = new Map<string, string | null>();
  const rows: SourceNodeRow[] = [];
  for (const [index, entry] of nodes.entries()) {
    const llama = await FakeLlama.start(entry.behaviour ?? okBehaviour);
    llamas[entry.id] = llama;
    origins.set(`${entry.id}.example.test`, llama.origin);
    nodeKeys.set(entry.id, `llama-${randomBytes(16).toString('hex')}`);
    rows.push(node(entry.id, (entry.models ?? ['model-a', 'embed-a', 'unloaded-a']).map((id) => model(id, id.startsWith('unloaded-') ? { status: 'unloaded' } : {})), { gatewayPriority: 10 + index }));
  }
  const table = new RoutingTable({ load: async () => ({ rows, settings: { queueFactor: 4 } }) }, new FakeBus() as never, new FakeClock(), { timers: false, rebuildIntervalMs: 10_000 });
  table.onModuleInit();
  await table.rebuild();

  const client = new UpstreamClient({ version: 'test', resolveOrigin: (host) => origins.get(host) ?? 'http://127.0.0.1:1' });
  const settings: ProxySettings = { timeouts: { ...DEFAULT_GATEWAY_TIMEOUTS, connectMs: 500, firstByteMs: 3000, idleMs: 3000, nonStreamTotalMs: 8000 }, maxAttempts: 3 };
  tune(settings);
  const settingsReader: ProxySettingsReader = { read: async () => settings };
  const sink = new RecordingSink();
  const limits = new RecordingLimits();
  const loop = new AttemptLoop({ routing: table, keys: { get: async (id) => nodeKeys.get(id) ?? null }, transport: client, log: { warn: () => undefined } });
  const service = new GatewayService(new FakeResolver(), limits, sink, { handle: async () => { throw new GatewayError('LAI-GW-003'); } }, settingsReader, loop);
  const gw = await buildGateway({ handlers: proxyHandlers(service) });
  const auth = { authorization: `Bearer ${gw.token}`, 'content-type': 'application/json' };

  return {
    gw,
    table,
    sink,
    limits,
    settings,
    nodeKeys,
    llamas,
    origins,
    client,
    async post(path, body, headers = {}) {
      const res = await gw.app.inject({ method: 'POST', url: `${gw.prefix}${path}`, headers: { ...auth, ...headers }, payload: typeof body === 'string' ? body : JSON.stringify(body) });
      let json: unknown = null;
      try {
        json = JSON.parse(res.body);
      } catch {
        json = null;
      }
      return { status: res.statusCode, headers: res.headers, json, text: res.body };
    },
    async cleanup() {
      await gw.app.close();
      await client.close();
      table.onModuleDestroy();
      await Promise.all(Object.values(llamas).map((llama) => llama.close()));
    },
  };
}

const labs: Lab[] = [];
afterEach(async () => {
  await Promise.all(labs.splice(0).map((entry) => entry.cleanup()));
});
const setup = async (...args: Parameters<typeof lab>): Promise<Lab> => {
  const created = await lab(...args);
  labs.push(created);
  return created;
};

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

describe('non-stream proxy: success', () => {
  it('proxies chat/completions and rewrites only the model', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const body = { model: 'lab-model', messages: [{ role: 'user', content: 'Hola' }], temperature: 0.2, max_tokens: 16, extra: { a: [1, 2, { b: null }] } };
    const res = await l.post('/chat/completions', body);
    expect(res.status).toBe(200);
    expect(res.json.choices[0].message.content).toBe('hi');
    expect(res.headers['x-localai-node']).toBe('ai-n1');
    expect(res.headers['x-localai-attempts']).toBe('1');
    expect(res.headers['x-request-id']).toEqual(expect.any(String));
    expect(res.headers['x-ratelimit-limit-requests']).toBe('60');
    expect(res.headers['content-type']).toContain('application/json');

    const seen = l.llamas['ai-n1']!.requests[0]!;
    expect(seen.url).toBe('/v1/chat/completions');
    expect(JSON.parse(seen.body)).toEqual({ ...body, model: 'model-a' });
  });

  it('proxies completions and embeddings', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const completions = await l.post('/completions', { model: 'model-a', prompt: 'x' });
    expect(completions.status).toBe(200);
    expect(completions.json.choices[0].text).toBe('hi');
    expect(l.llamas['ai-n1']!.requests[0]!.url).toBe('/v1/completions');

    const embeddings = await l.post('/embeddings', { model: 'embed-a', input: 'x' });
    expect(embeddings.status).toBe(200);
    expect(embeddings.json.data[0].embedding).toEqual([0.1, 0.2]);
    expect(l.llamas['ai-n1']!.requests[1]!.url).toBe('/v1/embeddings');
  });

  it('sends only the five gateway headers upstream, never the client Authorization, cookies or anything else', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    await l.post('/chat/completions', CHAT, { cookie: 'session=abc', 'x-forwarded-for': '203.0.113.9', 'x-custom': 'secret', accept: 'text/plain', 'user-agent': 'client/1.0' });
    const headers = l.llamas['ai-n1']!.requests[0]!.headers;
    expect(headers.authorization).toBe(`Bearer ${l.nodeKeys.get('ai-n1')}`);
    expect(headers.authorization).not.toContain(l.gw.token);
    expect(headers['content-type']).toBe('application/json');
    expect(headers.accept).toBe('application/json');
    expect(headers['x-request-id']).toBe(l.llamas['ai-n1']!.requests[0]!.headers['x-request-id']);
    expect(headers['user-agent']).toBe('localai-gateway/test');
    const allowed = new Set(['authorization', 'content-type', 'accept', 'x-request-id', 'user-agent', 'host', 'connection', 'content-length']);
    expect(Object.keys(headers).filter((name) => !allowed.has(name))).toEqual([]);
  });

  it('forwards only Content-Type from the upstream answer', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_req, res) => respondJson(res, 200, { usage: { prompt_tokens: 1, completion_tokens: 1 } }, { 'x-secret-upstream': 'nope', 'set-cookie': 'a=b', server: 'llama' }) }]);
    const res = await l.post('/chat/completions', CHAT);
    expect(res.headers['x-secret-upstream']).toBeUndefined();
    expect(res.headers['set-cookie']).toBeUndefined();
    expect(res.headers.server).toBeUndefined();
    expect(res.headers['content-type']).toContain('application/json');
  });

  it('records the usage of the answer and settles the limits ticket', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post('/chat/completions', { ...CHAT, model: 'lab-model' });
    expect(res.status).toBe(200);
    expect(l.sink.records).toHaveLength(1);
    expect(l.sink.records[0]).toMatchObject({
      endpoint: 'chat.completions',
      model: 'model-a',
      requestedModel: 'lab-model',
      nodeId: 'ai-n1',
      status: 200,
      errorCode: null,
      stream: false,
      aborted: false,
      attempts: 1,
      promptTokens: 11,
      completionTokens: 4,
      usageSource: 'upstream',
      keyId: expect.any(String),
      userId: 'user-a',
    });
    expect(l.sink.records[0]!.ttfbMs).toEqual(expect.any(Number));
    expect(l.sink.records[0]!.durationMs).toBeGreaterThanOrEqual(0);
    expect(l.limits.settled).toEqual([{ promptTokens: 11, completionTokens: 4, aborted: false }]);
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
    expect(JSON.stringify(l.sink.records)).not.toContain('Hola');
  });

  it('counts embeddings by prompt_tokens', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    await l.post('/embeddings', { model: 'embed-a', input: 'x' });
    expect(l.sink.records[0]).toMatchObject({ promptTokens: 7, completionTokens: 0, usageSource: 'upstream' });
  });

  it('records usageSource none when the answer has no usage', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_req, res) => respondJson(res, 200, { choices: [] }) }]);
    await l.post('/chat/completions', CHAT);
    expect(l.sink.records[0]).toMatchObject({ promptTokens: null, completionTokens: null, usageSource: 'none' });
  });
});

describe('non-stream proxy: request validation before any node', () => {
  it('refuses stream:true on embeddings with LAI-GW-003 and never calls a node', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post('/embeddings', { model: 'embed-a', input: 'x', stream: true });
    expect(res.status).toBe(400);
    expect(res.json.error).toMatchObject({ code: 'unsupported_parameter', localai_code: 'LAI-GW-003', param: 'stream' });
    expect(l.llamas['ai-n1']!.requests).toHaveLength(0);
  });

  it('refuses a non-embedding model on /embeddings with 404 LAI-GW-020', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const res = await l.post('/embeddings', { model: 'model-a', input: 'x' });
    expect(res.status).toBe(404);
    expect(res.json.error.localai_code).toBe('LAI-GW-020');
    expect(l.llamas['ai-n1']!.requests).toHaveLength(0);
  });

  it('maps unknown model, scope and capacity to their errors', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    expect((await l.post('/chat/completions', { model: 'nope', messages: [] })).json.error.localai_code).toBe('LAI-GW-020');
    // Known and in the approved catalog, but loaded on no node: capacity, not "unknown".
    const none = await l.post('/chat/completions', { model: 'unloaded-a', messages: [] });
    expect(none.status).toBe(503);
    expect(none.json.error.localai_code).toBe('LAI-GW-021');
    expect(none.headers['retry-after']).toBe('30');
  });
});

describe('non-stream proxy: attempts', () => {
  it('retries a 503 on the second node, reports 2 attempts and keeps the first node suspect', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_req, res) => respondJson(res, 503, { error: { message: 'loading' } }) }, { id: 'ai-n2' }]);
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(200);
    expect(res.headers['x-localai-node']).toBe('ai-n2');
    expect(res.headers['x-localai-attempts']).toBe('2');
    expect(l.table.isSuspect('ai-n1')).toBe(true);
    expect(l.table.isSuspect('ai-n2')).toBe(false);
    expect(l.sink.records[0]).toMatchObject({ nodeId: 'ai-n2', attempts: 2, status: 200 });
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });

  it('answers 502 LAI-GW-041 when every node returns 5xx', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_r, res) => respondJson(res, 500, {}) }, { id: 'ai-n2', behaviour: (_r, res) => respondJson(res, 502, 'bad gateway') }]);
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(502);
    expect(res.json.error).toMatchObject({ code: 'upstream_error', localai_code: 'LAI-GW-041' });
    expect(res.headers['x-localai-attempts']).toBe('2');
    expect(l.sink.records[0]).toMatchObject({ status: 502, errorCode: 'LAI-GW-041', attempts: 2 });
  });

  it('retries a 401 and marks the key invalid; all 401 gives 502 LAI-GW-043', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_r, res) => respondJson(res, 401, { error: { message: 'bad key' } }) }, { id: 'ai-n2' }]);
    const ok = await l.post('/chat/completions', CHAT);
    expect(ok.status).toBe(200);
    expect(ok.headers['x-localai-attempts']).toBe('2');
    expect(l.table.keyInvalidNodes()).toEqual(['ai-n1']);

    const all = await setup([{ id: 'ai-n1', behaviour: (_r, res) => respondJson(res, 401, {}) }, { id: 'ai-n2', behaviour: (_r, res) => respondJson(res, 401, {}) }]);
    const res = await all.post('/chat/completions', CHAT);
    expect(res.status).toBe(502);
    expect(res.json.error).toMatchObject({ code: 'upstream_auth_failed', localai_code: 'LAI-GW-043' });
    expect(all.table.keyInvalidNodes()).toEqual(['ai-n1', 'ai-n2']);
  });

  it('retries a "model not loaded" 400 on the next node and marks the model unloaded on the first', async () => {
    const l = await setup([
      { id: 'ai-n1', behaviour: (_r, res) => respondJson(res, 400, { error: { code: 400, message: 'model "model-a" is not loaded', type: 'invalid_request_error' } }) },
      { id: 'ai-n2' },
    ]);
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(200);
    expect(res.headers['x-localai-node']).toBe('ai-n2');
    const selected = l.table.select('model-a');
    expect(selected.kind === 'candidates' && selected.candidates.map((c) => c.nodeId)).toEqual(['ai-n2']);
    // Not a node fault: the node itself stays healthy for its other models.
    expect(l.table.isSuspect('ai-n1')).toBe(false);
  });

  it('passes another 4xx through with its status, kept message, string code and LAI-GW-042, without a retry', async () => {
    const l = await setup([
      { id: 'ai-n1', behaviour: (_r, res) => respondJson(res, 400, { error: { code: 400, message: 'the request exceeds the available context size', type: 'exceed_context_size_error' } }) },
      { id: 'ai-n2' },
    ]);
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(400);
    expect(res.json.error).toMatchObject({
      message: 'the request exceeds the available context size',
      type: 'invalid_request_error',
      code: 'upstream_rejected',
      localai_code: 'LAI-GW-042',
      request_id: res.headers['x-request-id'],
    });
    expect(res.headers['x-localai-attempts']).toBe('1');
    expect(l.llamas['ai-n2']!.requests).toHaveLength(0);
    expect(l.table.isSuspect('ai-n1')).toBe(false);
    expect(l.sink.records[0]).toMatchObject({ status: 400, errorCode: 'LAI-GW-042' });
  });

  it('keeps a string code from the upstream and answers 422 with its own status', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_r, res) => respondJson(res, 422, { error: { code: 'context_length_exceeded', message: 'too long' } }) }]);
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(422);
    expect(res.json.error).toMatchObject({ code: 'context_length_exceeded', localai_code: 'LAI-GW-042' });
  });

  it('answers 502 LAI-GW-040 when no node can be reached, then tries the next', async () => {
    const l = await setup([{ id: 'ai-n1' }, { id: 'ai-n2' }]);
    l.origins.set('ai-n1.example.test', await closedOrigin());
    l.origins.set('ai-n2.example.test', await closedOrigin());
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(502);
    expect(res.json.error).toMatchObject({ code: 'upstream_unreachable', localai_code: 'LAI-GW-040' });
    expect(res.headers['x-localai-attempts']).toBe('2');
  });

  it('falls over to the second node when the first is unreachable', async () => {
    const l = await setup([{ id: 'ai-n1' }, { id: 'ai-n2' }]);
    l.origins.set('ai-n1.example.test', await closedOrigin());
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(200);
    expect(res.headers['x-localai-node']).toBe('ai-n2');
    expect(res.headers['x-localai-attempts']).toBe('2');
    expect(l.table.isSuspect('ai-n1')).toBe(true);
  });

  it('stops after gateway.maxAttempts distinct nodes', async () => {
    const fail: FakeLlamaBehaviour = (_r, res) => respondJson(res, 503, {});
    const l = await setup([{ id: 'ai-n1', behaviour: fail }, { id: 'ai-n2', behaviour: fail }, { id: 'ai-n3', behaviour: fail }], (s) => (s.maxAttempts = 2));
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(502);
    expect(res.headers['x-localai-attempts']).toBe('2');
    expect(l.llamas['ai-n3']!.requests).toHaveLength(0);
  });

  it('answers 503 LAI-GW-050 when the node key is unreadable', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    const failing = new AttemptLoop({
      routing: l.table,
      keys: { get: async () => { throw new Error('decrypt failed'); } },
      transport: l.client,
      log: { warn: () => undefined },
    });
    const outcome = await failing.run({
      requestId: 'r1',
      model: { modelId: 'model-a', requestedModel: 'model-a', policy: DEFAULT_MODEL_POLICY, roles: null, inApprovedCatalog: true },
      path: '/v1/chat/completions',
      body: Buffer.from('{}'),
      accept: 'application/json',
      maxAttempts: 3,
      timeouts: { connectMs: 500, firstByteMs: 500, idleMs: 500 },
      signal: new AbortController().signal,
    });
    expect(outcome.ok === false && outcome.reason === 'error' && outcome.error.localaiCode).toBe('LAI-GW-050');
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });

  it('treats a node whose key was removed as key-invalid and tries the next', async () => {
    const l = await setup([{ id: 'ai-n1' }, { id: 'ai-n2' }]);
    l.nodeKeys.set('ai-n1', null);
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(200);
    expect(res.headers['x-localai-node']).toBe('ai-n2');
    expect(l.table.keyInvalidNodes()).toEqual(['ai-n1']);
    expect(l.llamas['ai-n1']!.requests).toHaveLength(0);
  });
});

describe('non-stream proxy: timeouts and limits', () => {
  it('answers 504 LAI-GW-044 when the first byte does not come in time', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: async (req, res) => { await sleep(2500); okBehaviour(req, res, undefined as never); } }], (s) => (s.timeouts.firstByteMs = 1000));
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(504);
    expect(res.json.error).toMatchObject({ code: 'upstream_timeout', localai_code: 'LAI-GW-044' });
    expect(l.table.isSuspect('ai-n1')).toBe(true);
  });

  it('retries a slow first node on a fast one', async () => {
    const l = await setup(
      [{ id: 'ai-n1', behaviour: async (req, res) => { await sleep(2500); okBehaviour(req, res, undefined as never); } }, { id: 'ai-n2' }],
      (s) => (s.timeouts.firstByteMs = 1000),
    );
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(200);
    expect(res.headers['x-localai-attempts']).toBe('2');
    expect(res.headers['x-localai-node']).toBe('ai-n2');
  });

  it('applies the per-model first-byte override instead of the setting', async () => {
    // The setting allows 20 s; the model's policy says 1 s; the node needs 2.5 s.
    const l = await setup([{ id: 'ai-n1', behaviour: async (req, res) => { await sleep(2500); okBehaviour(req, res, undefined as never); } }], (s) => (s.timeouts.firstByteMs = 20_000));
    const res = await l.post('/chat/completions', { ...CHAT, model: 'short-fb' });
    expect(res.status).toBe(504);
    expect(res.json.error.localai_code).toBe('LAI-GW-044');
  });

  it('answers 504 when the total non-stream time runs out while the body is being read', async () => {
    const l = await setup(
      [{ id: 'ai-n1', behaviour: (_req, res) => { res.writeHead(200, { 'content-type': 'application/json' }); res.write('{"usage":'); } }],
      (s) => (s.timeouts.nonStreamTotalMs = 300),
    );
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(504);
    expect(res.json.error.localai_code).toBe('LAI-GW-044');
  });

  it('refuses an upstream body over 32 MiB with 502 LAI-GW-041', async () => {
    const chunk = Buffer.alloc(1024 * 1024, 0x61);
    const l = await setup([
      {
        id: 'ai-n1',
        behaviour: (_req, res) => {
          res.writeHead(200, { 'content-type': 'application/json' });
          let sent = 0;
          const pump = (): void => {
            while (sent < 40) {
              sent += 1;
              if (!res.write(chunk)) return void res.once('drain', pump);
            }
            res.end();
          };
          pump();
        },
      },
    ]);
    const res = await l.post('/chat/completions', CHAT);
    expect(res.status).toBe(502);
    expect(res.json.error).toMatchObject({ code: 'upstream_error', localai_code: 'LAI-GW-041' });
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });
});

describe('non-stream proxy: client abort', () => {
  it('closes the upstream socket within 200 ms of the client leaving, and records an aborted request', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: () => undefined }]);
    await l.gw.app.listen({ port: 0, host: '127.0.0.1' });
    const port = (l.gw.app.server.address() as AddressInfo).port;
    const llama = l.llamas['ai-n1']!;

    const request = httpRequest({ host: '127.0.0.1', port, method: 'POST', path: `${l.gw.prefix}/chat/completions`, headers: { authorization: `Bearer ${l.gw.token}`, 'content-type': 'application/json' } });
    request.on('error', () => undefined);
    request.end(JSON.stringify(CHAT));
    const deadline = Date.now() + 3000;
    while (llama.requests.length === 0 && Date.now() < deadline) await sleep(10);
    expect(llama.requests).toHaveLength(1);

    const leftAt = Date.now();
    request.destroy();
    while (llama.abortedAt.length === 0 && Date.now() < leftAt + 1500) await sleep(5);
    expect(llama.abortedAt).toHaveLength(1);
    expect(llama.abortedAt[0]! - leftAt).toBeLessThan(200);

    const settleDeadline = Date.now() + 1500;
    while (l.sink.records.length === 0 && Date.now() < settleDeadline) await sleep(10);
    expect(l.sink.records[0]).toMatchObject({ aborted: true, status: 499, nodeId: 'ai-n1', errorCode: null });
    expect(l.limits.settled[0]).toMatchObject({ aborted: true });
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });
});
