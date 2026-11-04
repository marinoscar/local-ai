// Gateway limits and usage against real PostgreSQL (docs/specs/gateway.md sections 4.1, 4.5 and 3.8; issue GW-08):
//  - 120+ requests through the real pipeline (Fastify inject, fake llama-servers on real sockets, the real RateLimiter and
//    UsageRecorder over the Prisma store): usage rows == requests, the daily rollup matches row for row, the flush uses
//    one createMany and one upsert per (key, day);
//  - the budget reads the REAL daily row (and the recorder's unflushed tokens on top);
//  - no prompt, no marker, no embeddings input in any column of either table;
//  - the purge job deletes only the rows older than the retention and never a rollup;
//  - `GET /api/localai/gateway/usage` with real sessions: own keys, `all=1` for gateway:admin, the 7-day rule;
//  - the four gateway checks on `/api/admin/doctor`.
import { randomBytes } from 'node:crypto';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';

import { DAY_MS, dayStart, utcDay } from '../../src/localai/gateway/usage/usage-day';
import { PrismaUsageStore } from '../../src/localai/gateway/usage/usage-store';
import { UsagePurgeHandler } from '../../src/localai/gateway/usage/usage-purge.job';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { buildLab, type Lab } from './support/gateway-lab';

const run = `${Date.now()}`;
type Role = 'admin' | 'contributor' | 'viewer';

let app: NestFastifyApplication;
let prisma: PrismaService;
let store: PrismaUsageStore;
const tokens = {} as Record<Role, string>;
const userIds = {} as Record<Role, string>;
const keyIds: string[] = [];
let reader: { token: string; id: string };
const labs: Lab[] = [];

async function login(role: Role, label: string = role): Promise<{ token: string; id: string }> {
  const email = `gw-usage-${label}-${run}@example.test`;
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role } });
  const token = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  return { token, id: (await prisma.user.findFirstOrThrow({ where: { email } })).id };
}

const api = (url: string, bearer: string) => app.inject({ method: 'GET', url: `/api${url}`, headers: { authorization: `Bearer ${bearer}` } });
const data = <T>(response: { json(): unknown }) => (response.json() as { data: T }).data;

async function newKey(userId: string, over: { dailyTokenBudget?: bigint | null; rpmLimit?: number; concurrentLimit?: number } = {}) {
  const row = await prisma.localaiGatewayKey.create({
    data: {
      userId,
      name: `usage-spec ${run}`,
      tokenHash: randomBytes(32).toString('hex'),
      tokenPrefix: `lai_${randomBytes(3).toString('hex')}`,
      rpmLimit: over.rpmLimit ?? 100_000,
      concurrentLimit: over.concurrentLimit ?? 1_000,
      dailyTokenBudget: over.dailyTokenBudget === undefined ? null : over.dailyTokenBudget,
      expiresAt: new Date(Date.now() + 30 * DAY_MS),
    },
  });
  keyIds.push(row.id);
  return row;
}

async function lab(userId: string, keyId: string, over: { rpmLimit?: number; concurrentLimit?: number; dailyTokenBudget?: bigint | null } = {}) {
  const created = await buildLab({
    store,
    reader: store,
    keyOverrides: { id: keyId, userId, rpmLimit: over.rpmLimit ?? 100_000, concurrentLimit: over.concurrentLimit ?? 1_000, dailyTokenBudget: over.dailyTokenBudget === undefined ? null : over.dailyTokenBudget },
  });
  labs.push(created);
  return created;
}

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  store = new PrismaUsageStore(prisma);
  for (const role of ['admin', 'contributor', 'viewer'] as const) ({ token: tokens[role], id: userIds[role] } = await login(role));
  reader = await login('contributor', 'reader');
});

afterEach(async () => {
  await Promise.all(labs.splice(0).map((entry) => entry.cleanup()));
});

afterAll(async () => {
  await prisma.localaiGatewayUsage.deleteMany({ where: { keyId: { in: keyIds } } });
  await prisma.localaiGatewayUsageDaily.deleteMany({ where: { keyId: { in: keyIds } } });
  await prisma.localaiGatewayKey.deleteMany({ where: { id: { in: keyIds } } });
  await prisma.user.deleteMany({ where: { id: { in: [...Object.values(userIds), reader.id] } } });
  await app?.close();
});

const CHAT = { model: 'model-a', messages: [{ role: 'user', content: 'Hola' }] };

describe('120 requests through the pipeline into the real tables', () => {
  it('writes one usage row per request and a daily rollup that matches the rows', async () => {
    const key = await newKey(userIds.contributor);
    const l = await lab(userIds.contributor, key.id);

    // In small waves: the fake node has one slot per model, so a burst of 120 would be (rightly) refused as busy.
    const statuses: number[] = [];
    const send = (index: number) => (index % 3 === 0 ? l.post('/embeddings', { model: 'embed-a', input: 'x' }) : index % 3 === 1 ? l.post('/completions', { model: 'model-a', prompt: 'x' }) : l.post('/chat/completions', CHAT));
    for (let index = 0; index < 120; index += 3) {
      statuses.push(...(await Promise.all([send(index), send(index + 1), send(index + 2)])).map((r) => r.status));
    }
    statuses.push((await l.post('/chat/completions', { ...CHAT, model: 'unknown-model' })).status); // 404, still a row
    statuses.push((await l.get('/models')).status); // a models row
    expect(statuses.filter((s) => s === 200)).toHaveLength(121);
    expect(statuses).toContain(404);
    await l.recorder.flush();

    const total = 122;
    const rows = await prisma.localaiGatewayUsage.findMany({ where: { keyId: key.id } });
    expect(rows).toHaveLength(total);
    expect(new Set(rows.map((row) => row.requestId)).size).toBe(total);
    expect(rows.filter((row) => row.endpoint === 'models')).toHaveLength(1);
    expect(rows.find((row) => row.endpoint === 'models')).toMatchObject({ status: 200, model: null, nodeId: null, usageSource: 'none', userId: userIds.contributor });
    expect(rows.filter((row) => row.status === 404).map((row) => row.errorCode)).toEqual(['LAI-GW-020']);
    // The row's created_at is the instant the request ended, not the flush.
    for (const row of rows) expect(Math.abs(row.createdAt.getTime() - Date.now())).toBeLessThan(60_000);

    const daily = await prisma.localaiGatewayUsageDaily.findMany({ where: { keyId: key.id } });
    expect(daily).toHaveLength(1);
    expect(daily[0]!.day.toISOString().slice(0, 10)).toBe(utcDay(Date.now()));
    expect(daily[0]!.requests).toBe(total);
    expect(Number(daily[0]!.promptTokens)).toBe(rows.reduce((sum, row) => sum + (row.promptTokens ?? 0), 0));
    expect(Number(daily[0]!.completionTokens)).toBe(rows.reduce((sum, row) => sum + (row.completionTokens ?? 0), 0));
    // The fake node: chat 11+4, completions 5+3, embeddings 7+0.
    expect(Number(daily[0]!.promptTokens)).toBe(40 * 11 + 40 * 5 + 40 * 7);
    expect(Number(daily[0]!.completionTokens)).toBe(40 * 4 + 40 * 3);

    // A second batch increments the same row (ON CONFLICT ... DO UPDATE), it does not replace it.
    await l.post('/chat/completions', CHAT);
    await l.recorder.flush();
    const after = await prisma.localaiGatewayUsageDaily.findUniqueOrThrow({ where: { keyId_day: { keyId: key.id, day: dayStart(utcDay(Date.now())) } } });
    expect(after.requests).toBe(total + 1);
    expect(Number(after.promptTokens)).toBe(Number(daily[0]!.promptTokens) + 11);
  });

  it('no prompt, system prompt, tool argument or embeddings input is in any column of either table', async () => {
    const key = await newKey(userIds.contributor);
    const l = await lab(userIds.contributor, key.id);
    const marker = `marker-${randomBytes(12).toString('hex')}`;
    await l.post('/chat/completions', {
      model: 'model-a',
      messages: [
        { role: 'system', content: `sys ${marker}` },
        { role: 'user', content: `prompt ${marker}` },
      ],
      tools: [{ type: 'function', function: { name: 'f', parameters: { note: marker } } }],
    });
    await l.post('/embeddings', { model: 'embed-a', input: marker });
    await l.get('/models');
    await l.recorder.flush();

    const rows = await prisma.localaiGatewayUsage.findMany({ where: { keyId: key.id } });
    expect(rows).toHaveLength(3);
    const daily = await prisma.localaiGatewayUsageDaily.findMany({ where: { keyId: key.id } });
    expect(JSON.stringify(rows) + JSON.stringify(daily, (_k, v) => (typeof v === 'bigint' ? String(v) : v))).not.toContain(marker);
    const everything = await prisma.$queryRaw<Array<{ row: string }>>`SELECT u::text AS row FROM localai_gateway_usage u WHERE u.key_id = ${key.id}::uuid`;
    expect(everything.map((r) => r.row).join('\n')).not.toContain(marker);
    expect(l.gw.logs.info.join('\n') + l.gw.logs.warn.join('\n')).not.toContain(marker);
  });
});

describe('the budget reads the real daily row', () => {
  it('refuses with LAI-GW-032 at exactly used == budget, where used is the stored row plus the unflushed tokens', async () => {
    const key = await newKey(userIds.contributor, { dailyTokenBudget: 100n });
    await prisma.localaiGatewayUsageDaily.create({ data: { keyId: key.id, day: dayStart(utcDay(Date.now())), requests: 3, promptTokens: 60n, completionTokens: 25n } }); // 85 used
    const l = await lab(userIds.contributor, key.id, { dailyTokenBudget: 100n });

    const first = await l.post('/chat/completions', CHAT); // starts at 85 < 100, spends 15: 100 == budget
    expect(first.status).toBe(200);
    expect(first.headers['x-ratelimit-remaining-tokens']).toBe('0');
    // Not flushed yet: the refusal comes from the in-memory part on top of the stored row.
    const refused = await l.post('/chat/completions', CHAT);
    expect(refused.status).toBe(429);
    expect(refused.json.error.localai_code).toBe('LAI-GW-032');
    // ...and after the flush the stored row says the same, so a fresh process (a new ledger) agrees.
    await l.recorder.flush();
    const fresh = await lab(userIds.contributor, key.id, { dailyTokenBudget: 100n });
    const again = await fresh.post('/chat/completions', CHAT);
    expect(again.status).toBe(429);
    expect(again.json.error.localai_code).toBe('LAI-GW-032');
    const row = await prisma.localaiGatewayUsageDaily.findUniqueOrThrow({ where: { keyId_day: { keyId: key.id, day: dayStart(utcDay(Date.now())) } } });
    expect(Number(row.promptTokens) + Number(row.completionTokens)).toBe(100 + 0);
  });
});

describe('the purge job on real rows', () => {
  it('deletes the per-request rows older than the retention (90 days), keeps the newer ones and every daily rollup', async () => {
    const key = await newKey(userIds.contributor);
    const base = { keyId: key.id, userId: userIds.contributor, endpoint: 'chat.completions', status: 200, stream: false, usageSource: 'upstream', durationMs: 5 };
    const old = new Date(Date.now() - 91 * DAY_MS);
    const recent = new Date(Date.now() - 89 * DAY_MS);
    await prisma.localaiGatewayUsage.createMany({
      data: [
        { ...base, requestId: 'old-1', createdAt: old },
        { ...base, requestId: 'old-2', createdAt: new Date(old.getTime() - 40 * DAY_MS) },
        { ...base, requestId: 'recent', createdAt: recent },
        { ...base, requestId: 'today', createdAt: new Date() },
      ],
    });
    await prisma.localaiGatewayUsageDaily.create({ data: { keyId: key.id, day: dayStart(utcDay(old)), requests: 2, promptTokens: 1n, completionTokens: 1n } });

    const handler = app.get(UsagePurgeHandler, { strict: false });
    const deleted = await handler.purge();
    expect(deleted).toBeGreaterThanOrEqual(2);
    expect((await prisma.localaiGatewayUsage.findMany({ where: { keyId: key.id }, orderBy: { requestId: 'asc' } })).map((row) => row.requestId)).toEqual(['recent', 'today']);
    expect(await prisma.localaiGatewayUsageDaily.count({ where: { keyId: key.id } })).toBe(1);
  });
});

describe('GET /api/localai/gateway/usage with real sessions', () => {
  let mine: string;
  let theirs: string;

  beforeAll(async () => {
    mine = (await newKey(reader.id)).id;
    theirs = (await newKey(userIds.admin)).id;
    const today = dayStart(utcDay(Date.now()));
    const yesterday = new Date(today.getTime() - DAY_MS);
    await prisma.localaiGatewayUsageDaily.createMany({
      data: [
        { keyId: mine, day: today, requests: 5, promptTokens: 500n, completionTokens: 50n },
        { keyId: mine, day: yesterday, requests: 2, promptTokens: 20n, completionTokens: 2n },
        { keyId: theirs, day: today, requests: 7, promptTokens: 7000n, completionTokens: 700n },
      ],
    });
    const base = { userId: reader.id, endpoint: 'chat.completions', status: 200, stream: false, usageSource: 'upstream', durationMs: 5 };
    await prisma.localaiGatewayUsage.createMany({
      data: [
        { ...base, keyId: mine, requestId: `u1-${run}`, model: 'model-a', nodeId: 'ai-n1', promptTokens: 300, completionTokens: 30 },
        { ...base, keyId: mine, requestId: `u2-${run}`, model: 'model-a', nodeId: 'ai-n2', promptTokens: 200, completionTokens: 20 },
        { ...base, keyId: mine, requestId: `u3-${run}`, model: 'model-b', nodeId: 'ai-n1', promptTokens: 0, completionTokens: 0 },
        { ...base, keyId: theirs, requestId: `u4-${run}`, userId: userIds.admin, model: 'model-z', nodeId: 'ai-n9', promptTokens: 1, completionTokens: 1 },
      ],
    });
  });

  it('groupBy=day (the default) totals your own keys from the daily table over the last 30 days, empty days included', async () => {
    const response = await api('/localai/gateway/usage', reader.token);
    expect(response.statusCode).toBe(200);
    const body = data<{ scope: string; from: string; to: string; groupBy: string; totals: any; rows: Array<{ key: string; requests: number }> }>(response);
    expect(body).toMatchObject({ scope: 'own', groupBy: 'day', to: utcDay(Date.now()), totals: { requests: 7, promptTokens: 520, completionTokens: 52 } });
    expect(body.rows).toHaveLength(30);
    expect(body.rows.slice(-2).map((row) => [row.key, row.requests])).toEqual([
      [utcDay(Date.now() - DAY_MS), 2],
      [utcDay(Date.now()), 5],
    ]);
  });

  it('groupBy=model|node read the per-request rows for a range of at most 7 days', async () => {
    const from = utcDay(Date.now() - 6 * DAY_MS);
    const model = data<{ rows: Array<{ key: string; requests: number; promptTokens: number }> }>(await api(`/localai/gateway/usage?groupBy=model&from=${from}&to=${utcDay(Date.now())}`, reader.token));
    expect(model.rows).toEqual([
      { key: 'model-a', requests: 2, promptTokens: 500, completionTokens: 50 },
      { key: 'model-b', requests: 1, promptTokens: 0, completionTokens: 0 },
    ]);
    const node = data<{ rows: Array<{ key: string; requests: number }> }>(await api(`/localai/gateway/usage?groupBy=node&from=${from}&to=${utcDay(Date.now())}`, reader.token));
    expect(node.rows.map((row) => [row.key, row.requests])).toEqual([
      ['ai-n1', 2],
      ['ai-n2', 1],
    ]);
  });

  it('a day over a short range lists its requests per node', async () => {
    const today = utcDay(Date.now());
    const body = data<{ rows: Array<{ key: string; nodes?: Array<{ nodeId: string; requests: number }> }> }>(await api(`/localai/gateway/usage?from=${today}&to=${today}`, reader.token));
    expect(body.rows[0]!.nodes).toEqual([
      { nodeId: 'ai-n1', requests: 2 },
      { nodeId: 'ai-n2', requests: 1 },
    ]);
  });

  it('refuses groupBy=model|node over more than 7 days (400), a reversed range (400) and a malformed date (400)', async () => {
    expect((await api('/localai/gateway/usage?groupBy=model', reader.token)).statusCode).toBe(400);
    expect((await api('/localai/gateway/usage?groupBy=node&from=2026-01-01&to=2026-01-31', reader.token)).statusCode).toBe(400);
    expect((await api('/localai/gateway/usage?from=2026-02-02&to=2026-02-01', reader.token)).statusCode).toBe(400);
    expect((await api('/localai/gateway/usage?from=yesterday', reader.token)).statusCode).toBe(400);
  });

  it('?all=1 is gateway:admin only (403 for the contributor) and then sums every key; keyId narrows it', async () => {
    expect((await api('/localai/gateway/usage?all=1', reader.token)).statusCode).toBe(403);
    const all = data<{ scope: string; totals: { requests: number } }>(await api(`/localai/gateway/usage?all=1&from=${utcDay(Date.now())}&to=${utcDay(Date.now())}`, tokens.admin));
    expect(all.scope).toBe('all');
    expect(all.totals.requests).toBeGreaterThanOrEqual(12); // mine 5 + theirs 7 (+ rows other specs left, if any)
    const one = data<{ keyId: string; totals: { requests: number } }>(await api(`/localai/gateway/usage?all=1&keyId=${theirs}&from=${utcDay(Date.now())}&to=${utcDay(Date.now())}`, tokens.admin));
    expect(one).toMatchObject({ keyId: theirs, totals: { requests: 7 } });
  });

  it("another user's key does not exist for you (404), the viewer has no access (403) and nobody is anonymous (401)", async () => {
    expect((await api(`/localai/gateway/usage?keyId=${theirs}`, reader.token)).statusCode).toBe(404);
    expect((await api('/localai/gateway/usage', tokens.viewer)).statusCode).toBe(403);
    expect((await app.inject({ method: 'GET', url: '/api/localai/gateway/usage' })).statusCode).toBe(401);
  });
});

describe('the gateway Doctor checks on /api/admin/doctor', () => {
  it('lists the four checks, and an administrator is required', async () => {
    const response = await api('/admin/doctor?category=localai&refresh=true', tokens.admin);
    expect(response.statusCode).toBe(200);
    const checks = data<{ checks: Array<{ id: string; status: string; data?: Record<string, unknown> }> }>(response).checks.filter((check) => check.id.startsWith('localai.gateway.'));
    expect(checks.map((check) => check.id).sort()).toEqual(['localai.gateway.enabled', 'localai.gateway.keys', 'localai.gateway.routable', 'localai.gateway.usage']);
    expect(checks.find((check) => check.id === 'localai.gateway.enabled')!.status).toBe('pass');
    expect(checks.find((check) => check.id === 'localai.gateway.usage')!.status).toBe('pass');
    expect((await api('/admin/doctor?category=localai', tokens.contributor)).statusCode).toBe(403);
  });
});
