// Gateway key management against real PostgreSQL (gateway.md section 3.8, issue GW-02): the whole app, real sessions for
// the three roles. create -> list -> patch -> revoke on real rows; the token is returned once and only its sha256 is in
// the table; the audit rows carry no token; the defaults follow the stored `localai` settings; a user delete purges the
// user's keys; the catalog check reads a real approved catalog and real policy aliases; and the settings route writes
// the `localai` namespace while the platform's own route refuses it.
import { createHash, randomBytes } from 'node:crypto';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';

import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';

const run = `${Date.now()}`;
type Role = 'admin' | 'contributor' | 'viewer';

let app: NestFastifyApplication;
let prisma: PrismaService;
const tokens = {} as Record<Role, string>;
const userIds = {} as Record<Role, string>;
const extraUserIds: string[] = [];
const catalogVersions: string[] = [];
const policyIds: string[] = [];
let settingsBefore: unknown;

async function login(role: Role, label: string = role): Promise<{ token: string; id: string }> {
  const email = `gw-keys-${label}-${run}@example.test`;
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role } });
  const token = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  const id = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
  return { token, id };
}

const api = (method: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE', url: string, bearer: string | null, payload?: unknown) =>
  app.inject({ method, url: `/api${url}`, headers: bearer ? { authorization: `Bearer ${bearer}` } : {}, payload: payload as never });

interface KeyJson {
  id: string;
  name: string;
  kind: string;
  tokenPrefix: string;
  allowedModels: string[];
  limits: { rpm: number; concurrent: number; dailyTokens: number | null };
  expiresAt: string | null;
  revokedAt: string | null;
  status: string;
  ownerUserId: string;
}
const data = <T>(response: { json(): unknown }) => (response.json() as { data: T }).data;

async function setSettings(localai: unknown): Promise<void> {
  const row = await prisma.systemSettings.findUnique({ where: { key: 'global' } });
  const value = { ...((row?.value as Record<string, unknown>) ?? {}) };
  if (localai === undefined) delete value.localai;
  else value.localai = localai;
  await prisma.systemSettings.update({ where: { key: 'global' }, data: { value: JSON.parse(JSON.stringify(value)) } });
}

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  for (const role of ['admin', 'contributor', 'viewer'] as const) {
    const { token, id } = await login(role);
    tokens[role] = token;
    userIds[role] = id;
  }
  settingsBefore = ((await prisma.systemSettings.findUnique({ where: { key: 'global' } }))?.value as Record<string, unknown> | null)?.localai;
  await setSettings(undefined);
});

afterAll(async () => {
  await prisma.localaiGatewayKey.deleteMany({ where: { userId: { in: [...Object.values(userIds), ...extraUserIds] } } });
  await prisma.auditEvent.deleteMany({ where: { actorUserId: { in: Object.values(userIds) }, action: { startsWith: 'localai.gateway.' } } });
  if (catalogVersions.length) await prisma.localaiCatalogVersion.deleteMany({ where: { version: { in: catalogVersions } } });
  if (policyIds.length) await prisma.localaiModelPolicy.deleteMany({ where: { modelId: { in: policyIds } } });
  await setSettings(settingsBefore);
  await prisma.user.deleteMany({ where: { id: { in: [...Object.values(userIds), ...extraUserIds] } } });
  await app?.close();
});

describe('create, list, patch, revoke on real rows', () => {
  let key: KeyJson;
  let token: string;

  it('POST returns the token once and stores only its sha256 and a 10-character prefix', async () => {
    const response = await api('POST', '/localai/gateway/keys', tokens.contributor, { name: 'OpenCode laptop' });
    expect(response.statusCode).toBe(201);
    ({ key, token } = data<{ key: KeyJson; token: string }>(response));
    expect(token).toMatch(/^lai_[A-Za-z0-9_-]{43}$/);
    expect(key).toMatchObject({ kind: 'user', ownerUserId: userIds.contributor, limits: { rpm: 60, concurrent: 4, dailyTokens: 2_000_000 }, status: 'active', allowedModels: [], revokedAt: null });
    const days = (Date.parse(key.expiresAt!) - Date.now()) / 86_400_000;
    expect(days).toBeGreaterThan(89.9);
    expect(days).toBeLessThan(90.1);

    const row = await prisma.localaiGatewayKey.findUniqueOrThrow({ where: { id: key.id } });
    expect(row.tokenHash).toBe(createHash('sha256').update(token).digest('hex'));
    expect(row.tokenPrefix).toBe(token.slice(0, 10));
    expect(row.createdById).toBe(userIds.contributor);
    expect(JSON.stringify(row, (_k, v) => (typeof v === 'bigint' ? String(v) : v))).not.toContain(token);
  });

  it('GET lists it with the prefix, and neither the list nor a second read ever returns the token or the hash', async () => {
    const response = await api('GET', '/localai/gateway/keys', tokens.contributor);
    expect(response.statusCode).toBe(200);
    expect(data<{ keys: KeyJson[] }>(response).keys.map((k) => k.id)).toEqual([key.id]);
    expect(response.body).not.toContain(token);
    expect(response.body).not.toContain(token.slice(10));
    expect(response.body).not.toMatch(/[0-9a-f]{64}/);
    expect(response.body).not.toContain('tokenHash');
  });

  it("another user does not see it; an administrator sees it with ?all=1 and the owner's e-mail", async () => {
    expect(data<{ keys: KeyJson[] }>(await api('GET', '/localai/gateway/keys', tokens.admin)).keys.map((k) => k.id)).not.toContain(key.id);
    expect((await api('GET', '/localai/gateway/keys?all=1', tokens.contributor)).statusCode).toBe(403);
    const all = data<{ keys: Array<KeyJson & { owner?: { email: string } }> }>(await api('GET', '/localai/gateway/keys?all=1', tokens.admin)).keys;
    expect(all.find((k) => k.id === key.id)?.owner?.email).toBe(`gw-keys-contributor-${run}@example.test`);
  });

  it('PATCH by the owner renames; widening and limits are 403; a stranger gets 404', async () => {
    expect((await api('PATCH', `/localai/gateway/keys/${key.id}`, tokens.contributor, { name: 'Renamed' })).statusCode).toBe(200);
    expect((await prisma.localaiGatewayKey.findUniqueOrThrow({ where: { id: key.id } })).name).toBe('Renamed');
    expect((await api('PATCH', `/localai/gateway/keys/${key.id}`, tokens.contributor, { limits: { rpm: 1 } })).statusCode).toBe(403);
    const stranger = await login('contributor', 'stranger');
    extraUserIds.push(stranger.id);
    expect((await api('PATCH', `/localai/gateway/keys/${key.id}`, stranger.token, { name: 'x' })).statusCode).toBe(404);
    expect((await api('DELETE', `/localai/gateway/keys/${key.id}`, stranger.token)).statusCode).toBe(404);
  });

  it('PATCH by an administrator changes the limits', async () => {
    const response = await api('PATCH', `/localai/gateway/keys/${key.id}`, tokens.admin, { limits: { rpm: 120, dailyTokens: 5_000_000 } });
    expect(response.statusCode).toBe(200);
    const row = await prisma.localaiGatewayKey.findUniqueOrThrow({ where: { id: key.id } });
    expect([row.rpmLimit, row.dailyTokenBudget]).toEqual([120, 5_000_000n]);
  });

  it('DELETE sets revokedAt and keeps the row; a second DELETE is 200 and leaves the first revokedAt', async () => {
    const first = await api('DELETE', `/localai/gateway/keys/${key.id}`, tokens.contributor);
    expect(first.statusCode).toBe(200);
    const row = await prisma.localaiGatewayKey.findUniqueOrThrow({ where: { id: key.id } });
    expect(row.revokedAt).toBeInstanceOf(Date);
    const again = await api('DELETE', `/localai/gateway/keys/${key.id}`, tokens.contributor);
    expect(again.statusCode).toBe(200);
    expect(data<KeyJson>(again).status).toBe('revoked');
    expect((await prisma.localaiGatewayKey.findUniqueOrThrow({ where: { id: key.id } })).revokedAt).toEqual(row.revokedAt);
  });

  it('the revoked key leaves the default list and shows with ?includeRevoked=1; it cannot be patched (409)', async () => {
    expect(data<{ keys: KeyJson[] }>(await api('GET', '/localai/gateway/keys', tokens.contributor)).keys).toHaveLength(0);
    const listed = data<{ keys: KeyJson[] }>(await api('GET', '/localai/gateway/keys?includeRevoked=1', tokens.contributor)).keys;
    expect(listed.map((k) => [k.id, k.status])).toEqual([[key.id, 'revoked']]);
    expect((await api('PATCH', `/localai/gateway/keys/${key.id}`, tokens.contributor, { name: 'again' })).statusCode).toBe(409);
  });

  it('writes created, updated (x2), revoked (once) audit rows, none carrying the token or the hash', async () => {
    const rows = await prisma.auditEvent.findMany({ where: { targetType: 'localai_gateway_key', targetId: key.id }, orderBy: { createdAt: 'asc' } });
    expect(rows.map((r) => r.action).sort()).toEqual(['localai.gateway.key_created', 'localai.gateway.key_revoked', 'localai.gateway.key_updated', 'localai.gateway.key_updated']);
    const created = rows.find((r) => r.action === 'localai.gateway.key_created')!;
    expect(created.actorUserId).toBe(userIds.contributor);
    expect(created.meta).toMatchObject({ prefix: token.slice(0, 10), name: 'OpenCode laptop', rpm: 60, concurrent: 4, dailyTokens: 2_000_000 });
    const text = JSON.stringify(rows);
    expect(text).not.toContain(token);
    expect(text).not.toContain(token.slice(10));
    expect(text).not.toContain(createHash('sha256').update(token).digest('hex'));
  });
});

describe('roles', () => {
  it('a viewer gets 403 on every route', async () => {
    const fake = '99999999-9999-4999-8999-999999999999';
    for (const [method, url, body] of [
      ['GET', '/localai/gateway/keys', undefined],
      ['POST', '/localai/gateway/keys', { name: 'k' }],
      ['PATCH', `/localai/gateway/keys/${fake}`, { name: 'k' }],
      ['DELETE', `/localai/gateway/keys/${fake}`, undefined],
    ] as const) {
      expect([method, (await api(method, url, tokens.viewer, body)).statusCode]).toEqual([method, 403]);
    }
  });

  it('a contributor cannot create a service key, name another owner or raise limits (403 each); an administrator can', async () => {
    for (const body of [{ name: 's', kind: 'service' }, { name: 'o', ownerUserId: userIds.admin }, { name: 'l', limits: { rpm: 61 } }]) {
      expect((await api('POST', '/localai/gateway/keys', tokens.contributor, body)).statusCode).toBe(403);
    }
    const service = await api('POST', '/localai/gateway/keys', tokens.admin, { name: 'open-webui', kind: 'service' });
    expect(service.statusCode).toBe(201);
    expect(data<{ key: KeyJson }>(service).key).toMatchObject({ kind: 'service', expiresAt: null, limits: { rpm: 600, concurrent: 16, dailyTokens: null } });
    const forOther = await api('POST', '/localai/gateway/keys', tokens.admin, { name: 'for contributor', ownerUserId: userIds.contributor, limits: { rpm: 1000 } });
    expect(forOther.statusCode).toBe(201);
    expect(data<{ key: KeyJson }>(forOther).key).toMatchObject({ ownerUserId: userIds.contributor, limits: { rpm: 1000 } });
  });

  it('a user key must expire within the maximum and must have a budget', async () => {
    for (const body of [{ name: 'k', expiresInDays: 366 }, { name: 'k', expiresInDays: null }, { name: 'k', limits: { dailyTokens: null } }]) {
      expect((await api('POST', '/localai/gateway/keys', tokens.contributor, body)).statusCode).toBe(400);
    }
  });
});

describe('models are checked against the real catalog and policies', () => {
  it('accepts an approved-catalog id and a policy alias, refuses anything else; narrowing is allowed, widening is not', async () => {
    const version = `gw-keys-${run}`;
    catalogVersions.push(version);
    await prisma.localaiCatalogVersion.create({
      data: {
        version,
        source: 'editor',
        status: 'approved',
        files: [{ path: 'models.yaml', content: 'schema_version: 1\nmodels:\n  - id: gwkeys-model-one\n    display_name: One\n' }],
        bundleSha256: randomBytes(32).toString('hex'),
      },
    });
    policyIds.push(`gwkeys-model-two`);
    await prisma.localaiModelPolicy.create({ data: { modelId: 'gwkeys-model-two', aliases: [`gwkeys-alias-${run}`] } });

    const alias = `gwkeys-alias-${run}`;
    const created = await api('POST', '/localai/gateway/keys', tokens.contributor, { name: 'scoped', allowedModels: [alias, 'gwkeys-model-two', 'gwkeys-model-one'] });
    expect(created.statusCode).toBe(201);
    const id = data<{ key: KeyJson }>(created).key.id;
    const unknown = await api('POST', '/localai/gateway/keys', tokens.contributor, { name: 'bad', allowedModels: ['gwkeys-nope'] });
    expect(unknown.statusCode).toBe(400);
    expect(unknown.body).toContain('gwkeys-nope');

    expect((await api('PATCH', `/localai/gateway/keys/${id}`, tokens.contributor, { allowedModels: [alias] })).statusCode).toBe(200);
    expect((await api('PATCH', `/localai/gateway/keys/${id}`, tokens.contributor, { allowedModels: [alias, 'gwkeys-model-two', 'gwkeys-model-one', 'gwkeys-model-three'] })).statusCode).toBe(403);
    expect((await api('PATCH', `/localai/gateway/keys/${id}`, tokens.contributor, { allowedModels: [] })).statusCode).toBe(403);
    expect((await api('PATCH', `/localai/gateway/keys/${id}`, tokens.admin, { allowedModels: [] })).statusCode).toBe(200);
  });
});

describe('settings', () => {
  it('PUT /api/localai/settings stores the nested document; the next key follows gateway.defaults.user', async () => {
    const put = await api('PUT', '/localai/settings', tokens.admin, { 'gateway.defaults.user': { rpm: 10, concurrent: 2, dailyTokens: 12345, expiresInDays: 7 }, 'alerts.repeatHours': 6 });
    expect(put.statusCode).toBe(200);
    const row = await prisma.systemSettings.findUniqueOrThrow({ where: { key: 'global' } });
    expect((row.value as Record<string, unknown>).localai).toMatchObject({ gateway: { defaults: { user: { rpm: 10, concurrent: 2, dailyTokens: 12345, expiresInDays: 7 } } }, alerts: { repeatHours: 6 } });

    const created = await api('POST', '/localai/gateway/keys', tokens.contributor, { name: 'follows settings' });
    const { key } = data<{ key: KeyJson }>(created);
    expect(key.limits).toEqual({ rpm: 10, concurrent: 2, dailyTokens: 12345 });
    const days = (Date.parse(key.expiresAt!) - Date.now()) / 86_400_000;
    expect(days).toBeGreaterThan(6.9);
    expect(days).toBeLessThan(7.1);
    // the pre-existing alert reader sees the nested value too
    const read = data<{ settings: Record<string, unknown> }>(await api('GET', '/localai/settings', tokens.viewer)).settings;
    expect(read['alerts.repeatHours']).toBe(6);
    expect(read['gateway.defaults.user']).toEqual({ rpm: 10, concurrent: 2, dailyTokens: 12345, expiresInDays: 7 });
  });

  it('is 403 for a viewer and a contributor, 400 for an unknown key', async () => {
    expect((await api('PUT', '/localai/settings', tokens.viewer, { 'alerts.repeatHours': 1 })).statusCode).toBe(403);
    expect((await api('PUT', '/localai/settings', tokens.contributor, { 'alerts.repeatHours': 1 })).statusCode).toBe(403);
    expect((await api('PUT', '/localai/settings', tokens.admin, { 'gateway.nope': 1 })).statusCode).toBe(400);
  });

  it("the platform's own route reads the namespace but refuses to write it", async () => {
    const read = await api('GET', '/system-settings', tokens.admin);
    expect(read.statusCode).toBe(200);
    expect(data<{ localai?: unknown }>(read).localai).toBeDefined();
    expect((await api('PATCH', '/system-settings', tokens.admin, { localai: { gateway: { maxUserKeyDays: 3650 } } })).statusCode).toBe(400);
  });
});

describe('user deletion', () => {
  it("deleting a user deletes the user's keys", async () => {
    const doomed = await login('contributor', 'doomed');
    extraUserIds.push(doomed.id);
    const created = await api('POST', '/localai/gateway/keys', doomed.token, { name: 'doomed key' });
    const id = data<{ key: KeyJson }>(created).key.id;
    await prisma.user.delete({ where: { id: doomed.id } });
    expect(await prisma.localaiGatewayKey.findUnique({ where: { id } })).toBeNull();
  });
});
