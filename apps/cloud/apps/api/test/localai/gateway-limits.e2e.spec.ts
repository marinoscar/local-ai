// The limits and the usage recorder inside the real gateway pipeline (docs/specs/gateway.md sections 4.1, 4.5, 6 and 9):
// real Fastify scope, real `GatewayService`, real fake-llama sockets, the REAL `RateLimiter` + `UsageRecorder`, and a
// recording store instead of PostgreSQL (the real tables are `gateway-usage.db.spec.ts`).
import { randomBytes } from 'node:crypto';
import { request as httpRequest } from 'node:http';
import type { AddressInfo } from 'node:net';

import { Logger } from '@nestjs/common';

import { rollupsOf, type DailyRollup, type StampedUsage, type UsageStore } from '../../src/localai/gateway/usage/usage-store';
import { utcDay } from '../../src/localai/gateway/usage/usage-day';
import { respondJson, type FakeLlamaBehaviour } from '../fakes/fake-llama';
import { buildLab, type Lab } from './support/gateway-lab';

class MemoryStore implements UsageStore {
  rows: StampedUsage[] = [];
  rollups: DailyRollup[] = [];
  failures = 0;
  async write(rows: readonly StampedUsage[], rollups: readonly DailyRollup[]): Promise<void> {
    if (this.failures > 0) {
      this.failures -= 1;
      throw new Error('db down');
    }
    this.rows.push(...rows);
    this.rollups.push(...rollups);
  }
}

const logged: string[] = [];
beforeAll(() => {
  for (const level of ['log', 'debug', 'warn', 'error'] as const) jest.spyOn(Logger.prototype, level).mockImplementation(((message: unknown) => void logged.push(String(message))) as never);
});
afterAll(() => jest.restoreAllMocks());

const CHAT = { model: 'model-a', messages: [{ role: 'user', content: 'Hola' }] };
const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));
const labs: Lab[] = [];
afterEach(async () => {
  await Promise.all(labs.splice(0).map((lab) => lab.cleanup()));
});
async function setup(options: Partial<Parameters<typeof buildLab>[0]> = {}) {
  const store = new MemoryStore();
  const lab = await buildLab({ store, ...options });
  labs.push(lab);
  return { lab, store };
}

const RATE_HEADERS = ['x-ratelimit-limit-requests', 'x-ratelimit-remaining-requests', 'x-ratelimit-limit-tokens', 'x-ratelimit-remaining-tokens'];

describe('rate-limit headers', () => {
  it('are on every authenticated response: 200, a 404 after authentication, a 429, and GET /v1/models', async () => {
    const { lab } = await setup({ keyOverrides: { rpmLimit: 2, dailyTokenBudget: 1000n } });
    const ok = await lab.post('/chat/completions', CHAT);
    expect(ok.status).toBe(200);
    expect(ok.headers['x-ratelimit-limit-requests']).toBe('2');
    expect(ok.headers['x-ratelimit-remaining-requests']).toBe('1');
    expect(ok.headers['x-ratelimit-limit-tokens']).toBe('1000');
    // The 15 tokens of the answer are already counted (11 prompt + 4 completion) by the time the next response goes out.
    const unknown = await lab.post('/chat/completions', { ...CHAT, model: 'nope' });
    expect(unknown.status).toBe(404);
    for (const name of RATE_HEADERS) expect(unknown.headers[name]).toBeDefined();
    expect(unknown.headers['x-ratelimit-remaining-tokens']).toBe('985');

    const models = await lab.get('/models');
    expect(models.status).toBe(200);
    for (const name of RATE_HEADERS) expect(models.headers[name]).toBeDefined();

    await lab.post('/chat/completions', CHAT);
    const limited = await lab.post('/chat/completions', CHAT);
    expect(limited.status).toBe(429);
    expect(limited.json.error.localai_code).toBe('LAI-GW-030');
    expect(limited.headers['x-ratelimit-remaining-requests']).toBe('0');
    for (const name of RATE_HEADERS) expect(limited.headers[name]).toBeDefined();
  });

  it('are absent on a 401 and on the public index', async () => {
    const { lab } = await setup();
    const none = await lab.gw.app.inject({ method: 'POST', url: `${lab.gw.prefix}/chat/completions`, payload: JSON.stringify(CHAT), headers: { 'content-type': 'application/json' } });
    expect(none.statusCode).toBe(401);
    const wrong = await lab.gw.app.inject({ method: 'POST', url: `${lab.gw.prefix}/chat/completions`, payload: '{}', headers: { authorization: `Bearer lai_${randomBytes(32).toString('base64url')}` } });
    expect(wrong.statusCode).toBe(401);
    const index = await lab.gw.app.inject({ method: 'GET', url: `${lab.gw.prefix}/` });
    for (const response of [none, wrong, index]) for (const name of RATE_HEADERS) expect(response.headers[name]).toBeUndefined();
  });

  it('leave the token headers out for a key with no daily budget', async () => {
    const { lab } = await setup({ keyOverrides: { dailyTokenBudget: null } });
    const ok = await lab.post('/chat/completions', CHAT);
    expect(ok.headers['x-ratelimit-limit-requests']).toBe('60');
    expect(ok.headers['x-ratelimit-limit-tokens']).toBeUndefined();
  });
});

describe('RPM through the pipeline', () => {
  it('the 60th request in a minute is served, the 61st is 429 LAI-GW-030 with a Retry-After inside the minute', async () => {
    const { lab } = await setup({ keyOverrides: { rpmLimit: 60, concurrentLimit: 100, dailyTokenBudget: null } });
    for (let index = 0; index < 60; index += 1) expect((await lab.post('/chat/completions', CHAT)).status).toBe(200);
    const refused = await lab.post('/chat/completions', CHAT);
    expect(refused.status).toBe(429);
    expect(refused.json.error).toMatchObject({ type: 'rate_limit_error', code: 'rate_limit_exceeded', localai_code: 'LAI-GW-030' });
    const retry = Number(refused.headers['retry-after']);
    expect(retry).toBeGreaterThanOrEqual(1);
    expect(retry).toBeLessThanOrEqual(60);
    // The refusal reached no node.
    expect(lab.llamas['ai-n1']!.requests).toHaveLength(60);
  });
});

describe('concurrency slot release', () => {
  const counter = (lab: Lab) => lab.limiter.inflightOf(lab.gw.key.id);

  it('is released after a success, after an upstream error and after a client abort', async () => {
    const slow: FakeLlamaBehaviour = () => undefined; // never answers
    let mode: 'ok' | 'fail' | 'hang' = 'ok';
    const { lab } = await setup({
      keyOverrides: { concurrentLimit: 2, rpmLimit: 1000 },
      nodes: [{ id: 'ai-n1', behaviour: (req, res, raw) => (mode === 'hang' ? slow(req, res, raw) : mode === 'fail' ? respondJson(res, 500, { error: 'boom' }) : respondJson(res, 200, { usage: { prompt_tokens: 1, completion_tokens: 1 } })) }],
    });
    expect((await lab.post('/chat/completions', CHAT)).status).toBe(200);
    expect(counter(lab)).toBe(0);

    // The abort case runs before the error case: a 500 marks the node suspect for 30 s.
    mode = 'hang';
    await lab.gw.app.listen({ port: 0, host: '127.0.0.1' });
    const port = (lab.gw.app.server.address() as AddressInfo).port;
    const request = httpRequest({ host: '127.0.0.1', port, method: 'POST', path: `${lab.gw.prefix}/chat/completions`, headers: { authorization: `Bearer ${lab.gw.token}`, 'content-type': 'application/json' } });
    request.on('error', () => undefined);
    request.end(JSON.stringify(CHAT));
    const deadline = Date.now() + 3000;
    while (counter(lab) === 0 && Date.now() < deadline) await sleep(10);
    expect(counter(lab)).toBe(1);
    request.destroy();
    const releaseBy = Date.now() + 2000;
    while (counter(lab) !== 0 && Date.now() < releaseBy) await sleep(10);
    expect(counter(lab)).toBe(0);

    mode = 'fail';
    expect((await lab.post('/chat/completions', CHAT)).status).toBeGreaterThanOrEqual(500);
    expect(counter(lab)).toBe(0);
  });

  it('a request refused for concurrency is LAI-GW-031 with Retry-After 2 and does not disturb the running one', async () => {
    let release: (() => void) | null = null;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const { lab } = await setup({
      keyOverrides: { concurrentLimit: 1, rpmLimit: 1000 },
      nodes: [{ id: 'ai-n1', behaviour: async (_req, res) => { await gate; respondJson(res, 200, { usage: { prompt_tokens: 1, completion_tokens: 1 } }); } }],
    });
    const running = lab.post('/chat/completions', CHAT);
    const deadline = Date.now() + 3000;
    while (lab.limiter.inflightOf(lab.gw.key.id) === 0 && Date.now() < deadline) await sleep(5);
    const refused = await lab.post('/chat/completions', CHAT);
    expect(refused.status).toBe(429);
    expect(refused.json.error.localai_code).toBe('LAI-GW-031');
    expect(refused.headers['retry-after']).toBe('2');
    release!();
    expect((await running).status).toBe(200);
  });
});

describe('the daily budget', () => {
  it('lets the request that starts under budget finish and refuses the next with LAI-GW-032 and the seconds to 00:00 UTC', async () => {
    // 11 + 4 = 15 tokens per answer; a budget of 16 admits two (15 < 16, then 30 >= 16 refuses the third).
    const { lab } = await setup({ keyOverrides: { dailyTokenBudget: 16n, rpmLimit: 100 } });
    expect((await lab.post('/chat/completions', CHAT)).status).toBe(200);
    expect((await lab.post('/chat/completions', CHAT)).status).toBe(200);
    const refused = await lab.post('/chat/completions', CHAT);
    expect(refused.status).toBe(429);
    expect(refused.json.error).toMatchObject({ code: 'daily_token_budget_exceeded', localai_code: 'LAI-GW-032' });
    const left = Number(refused.headers['retry-after']);
    const now = Date.now();
    const toMidnight = Math.ceil((Date.UTC(new Date(now).getUTCFullYear(), new Date(now).getUTCMonth(), new Date(now).getUTCDate() + 1) - now) / 1000);
    expect(Math.abs(left - toMidnight)).toBeLessThanOrEqual(2);
    expect(refused.headers['x-ratelimit-remaining-tokens']).toBe('0');
  });
});

describe('usage rows', () => {
  it('one row per request, including refused ones and /v1/models, and the rollup totals match', async () => {
    const { lab, store } = await setup({ keyOverrides: { rpmLimit: 3, concurrentLimit: 10, dailyTokenBudget: null } });
    await lab.post('/chat/completions', CHAT);
    await lab.post('/completions', { model: 'model-a', prompt: 'x' });
    await lab.post('/embeddings', { model: 'embed-a', input: 'x' });
    await lab.post('/chat/completions', CHAT); // refused: RPM 3
    await lab.post('/chat/completions', { ...CHAT, model: 'nope' }); // 404 before the limits
    await lab.get('/models');
    await lab.recorder.flush();

    expect(store.rows).toHaveLength(6);
    expect(store.rows.map((row) => [row.endpoint, row.status, row.errorCode, row.usageSource])).toEqual([
      ['chat.completions', 200, null, 'upstream'],
      ['completions', 200, null, 'upstream'],
      ['embeddings', 200, null, 'upstream'],
      ['chat.completions', 429, 'LAI-GW-030', 'none'],
      ['chat.completions', 404, 'LAI-GW-020', 'none'],
      ['models', 200, null, 'none'],
    ]);
    const today = utcDay(Date.now());
    expect(rollupsOf(store.rows, utcDay)).toEqual([{ keyId: lab.gw.key.id, day: today, requests: 6, promptTokens: 11 + 5 + 7, completionTokens: 4 + 3 + 0 }]);
    for (const row of store.rows) {
      expect(row.keyId).toBe(lab.gw.key.id);
      expect(row.userId).toBe(lab.gw.key.userId);
    }
  });

  it('a flush that fails twice drops the batch, logs gateway.usage_dropped, and the responses are unaffected', async () => {
    const { lab, store } = await setup();
    store.failures = 2;
    const statuses = [(await lab.post('/chat/completions', CHAT)).status, (await lab.post('/chat/completions', CHAT)).status];
    expect(statuses).toEqual([200, 200]);
    await lab.recorder.flush();
    expect(store.rows).toHaveLength(0);
    expect(lab.recorder.droppedSince()).toBe(2);
    expect(logged.some((line) => line.includes('gateway.usage_dropped') && line.includes('"count":2'))).toBe(true);
    expect((await lab.post('/chat/completions', CHAT)).status).toBe(200);
  });
});

describe('what is never recorded (spec 6)', () => {
  it('the model name a client sends is clipped (it is the one free-text field of a row)', async () => {
    const { lab, store } = await setup();
    const long = `m${'x'.repeat(5000)}`;
    expect((await lab.post('/chat/completions', { ...CHAT, model: long })).status).toBe(404);
    await lab.recorder.flush();
    expect(store.rows[0]!.requestedModel!.length).toBeLessThanOrEqual(200);
    expect(lab.gw.logs.info.join('\n').length).toBeLessThan(2000);
  });

  it('a marker in the prompt, the system prompt, a tool argument and the embeddings input is in no log line and no usage column', async () => {
    const marker = `marker-${randomBytes(12).toString('hex')}`;
    const { lab, store } = await setup();
    logged.length = 0;
    const body = {
      model: 'model-a',
      messages: [
        { role: 'system', content: `sys ${marker}` },
        { role: 'user', content: `prompt ${marker}` },
      ],
      tools: [{ type: 'function', function: { name: 'f', parameters: { note: marker } } }],
    };
    expect((await lab.post('/chat/completions', body)).status).toBe(200);
    expect((await lab.post('/embeddings', { model: 'embed-a', input: marker })).status).toBe(200);
    await lab.get('/models');
    await lab.recorder.flush();

    expect(store.rows.length).toBeGreaterThanOrEqual(3);
    const dumped = JSON.stringify(store.rows) + JSON.stringify(store.rollups);
    expect(dumped).not.toContain(marker);
    expect(lab.gw.logs.info.join('\n') + lab.gw.logs.warn.join('\n') + JSON.stringify(lab.gw.logs.error) + logged.join('\n')).not.toContain(marker);
    expect(lab.gw.logs.info.length).toBeGreaterThanOrEqual(3);
  });
});
