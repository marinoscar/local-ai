// The gateway key routes and `GET/PUT /api/localai/settings` over HTTP, on the whole AppModule over a mocked database
// (no PostgreSQL): the real guards, the seeded role grants, the real services. The same queries against real SQL are
// `gateway-keys.db.spec.ts`. Roles: viewer (lab:read), contributor (gateway:use), admin (everything).
import fastifyCookie from '@fastify/cookie';
import { Logger } from '@nestjs/common';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { GatewayAuthenticator } from '../../src/localai/gateway/auth';
import { hashGatewayToken } from '../../src/localai/gateway/gateway.token';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';

const KEY_ID = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee1';
const evict = jest.fn();

function keyRow(userId: string, patch: Record<string, unknown> = {}) {
  return {
    id: KEY_ID,
    userId,
    name: 'OpenCode laptop',
    kind: 'user',
    tokenHash: 'a'.repeat(64),
    tokenPrefix: 'lai_Ab3dEf',
    allowedModels: [],
    rpmLimit: 60,
    concurrentLimit: 4,
    dailyTokenBudget: 2_000_000n,
    expiresAt: new Date('2027-02-02T12:00:00.000Z'),
    revokedAt: null,
    lastUsedAt: null,
    createdById: userId,
    createdAt: new Date('2026-11-04T12:00:00.000Z'),
    user: { email: 'person-a@example.test', displayName: 'Person A' },
    ...patch,
  };
}

describe('Gateway key routes over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let storedSettings: Record<string, unknown>;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({ imports: [AppModule] })
      .overrideProvider(PrismaService)
      .useValue(prismaMock)
      .overrideProvider(PrismaSystemService)
      .useValue({
        asSystem: () => prismaMock,
        runAsSystem: (_reason: string, fn: (tx: unknown) => unknown) => fn(prismaMock),
        $connect: async () => undefined,
        $disconnect: async () => undefined,
      })
      .overrideProvider(JobWorker)
      .useValue({})
      .compile();
    app = moduleRef.createNestApplication<NestFastifyApplication>(new FastifyAdapter());
    await app.register(fastifyCookie, { secret: 'test-secret' });
    app.setGlobalPrefix('api');
    resetPrismaMock();
    installBaseMocks();
    await app.init();
    await app.getHttpAdapter().getInstance().ready();
    for (const cronJob of app.get(SchedulerRegistry).getCronJobs().values()) void cronJob.stop();
    jwt = moduleRef.get(JwtService);
    // Revocation evicts through the /v1 authenticator's own cache (GW-03): observe the call, keep the real class.
    jest.spyOn(app.get(GatewayAuthenticator), 'evict').mockImplementation((hash: string) => void evict(hash));
  });

  afterAll(async () => {
    clearMockUsers();
    await app?.close();
  });

  beforeEach(() => {
    evict.mockReset();
    storedSettings = {};
    prismaMock.localaiGatewayKey.create.mockReset().mockImplementation(async ({ data }: { data: Record<string, unknown> }) => ({ ...keyRow(String(data.userId)), ...data }));
    prismaMock.localaiGatewayKey.findUnique.mockReset().mockImplementation(async () => keyRow('00000000-0000-4000-8000-0000000000aa'));
    prismaMock.localaiGatewayKey.findMany.mockReset().mockResolvedValue([]);
    prismaMock.localaiGatewayKey.updateMany.mockReset().mockResolvedValue({ count: 1 });
    prismaMock.user.count.mockReset().mockResolvedValue(1);
    prismaMock.localaiCatalogVersion.findFirst.mockReset().mockResolvedValue(null);
    prismaMock.localaiModelPolicy.findMany.mockReset().mockResolvedValue([{ modelId: 'qwen3.5-9b-q4km', aliases: ['lab-coder'] }]);
    prismaMock.auditEvent.create.mockReset().mockResolvedValue({});
    prismaMock.systemSettings.findUnique.mockReset().mockImplementation(async () => ({ id: 's1', key: 'global', value: storedSettings, version: 1, updatedAt: new Date(), updatedByUserId: null }));
    prismaMock.systemSettings.create.mockReset().mockImplementation(async () => ({ id: 's1', key: 'global', value: storedSettings, version: 1, updatedAt: new Date(), updatedByUserId: null }));
    prismaMock.systemSettings.update.mockReset().mockImplementation(async ({ data }: { data: { value: Record<string, unknown> } }) => {
      storedSettings = data.value;
      return { id: 's1', key: 'global', value: storedSettings, version: 2, updatedAt: new Date(), updatedByUserId: 'u', updatedByUser: { id: 'u', email: 'admin@example.test' } };
    });
  });

  const call = (method: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE', url: string, token?: string, body?: unknown) =>
    app.inject({ method, url, headers: token ? { authorization: `Bearer ${token}` } : {}, ...(body !== undefined ? { payload: body as object } : {}) });
  const session = (role: FixtureRole) => registerMockUser(jwt, role);
  const as = (role: FixtureRole) => session(role).accessToken;

  describe('every key route', () => {
    const routes: Array<[string, string, unknown]> = [
      ['GET', '/api/localai/gateway/keys', undefined],
      ['POST', '/api/localai/gateway/keys', { name: 'k' }],
      ['PATCH', `/api/localai/gateway/keys/${KEY_ID}`, { name: 'k' }],
      ['DELETE', `/api/localai/gateway/keys/${KEY_ID}`, undefined],
    ];

    it.each(routes)('%s %s: 401 without a session', async (method, url, body) => {
      expect((await call(method as 'GET', url, undefined, body)).statusCode).toBe(401);
    });

    it.each(routes)('%s %s: 403 for a viewer (no gateway:use), and nothing is read or written', async (method, url, body) => {
      expect((await call(method as 'GET', url, as('viewer'), body)).statusCode).toBe(403);
      expect(prismaMock.localaiGatewayKey.create).not.toHaveBeenCalled();
      expect(prismaMock.localaiGatewayKey.findMany).not.toHaveBeenCalled();
      expect(prismaMock.localaiGatewayKey.findUnique).not.toHaveBeenCalled();
      expect(prismaMock.localaiGatewayKey.updateMany).not.toHaveBeenCalled();
    });
  });

  describe('POST /api/localai/gateway/keys', () => {
    it('201: the token once in the wire shape, only its hash stored, and the audit row without it', async () => {
      const user = session('contributor');
      const response = await call('POST', '/api/localai/gateway/keys', user.accessToken, { name: 'OpenCode laptop', allowedModels: ['lab-coder'] });
      expect(response.statusCode).toBe(201);
      const { data } = response.json() as { data: { key: Record<string, unknown>; token: string } };
      expect(data.token).toMatch(/^lai_[A-Za-z0-9_-]{43}$/);
      expect(data.key).toMatchObject({ name: 'OpenCode laptop', kind: 'user', allowedModels: ['lab-coder'], limits: { rpm: 60, concurrent: 4, dailyTokens: 2_000_000 }, status: 'active', tokenPrefix: data.token.slice(0, 10) });
      expect(data.key).not.toHaveProperty('tokenHash');
      expect(data.key).not.toHaveProperty('token');

      const written = prismaMock.localaiGatewayKey.create.mock.calls[0][0].data;
      expect(written.tokenHash).toBe(hashGatewayToken(data.token));
      expect(JSON.stringify(written, (_k, v) => (typeof v === 'bigint' ? String(v) : v))).not.toContain(data.token);
      expect(written).toMatchObject({ userId: user.id, createdById: user.id, kind: 'user' });

      const audit = prismaMock.auditEvent.create.mock.calls.map((call: [{ data: { action: string } }]) => call[0].data).filter((row: { action: string }) => row.action.startsWith('localai.gateway.'));
      expect(audit).toHaveLength(1);
      expect(audit[0]).toMatchObject({ action: 'localai.gateway.key_created', targetType: 'localai_gateway_key', meta: { prefix: data.token.slice(0, 10), name: 'OpenCode laptop', rpm: 60 } });
      expect(JSON.stringify(audit)).not.toContain(data.token);
    });

    it('the token appears nowhere in the application log', async () => {
      const lines: string[] = [];
      const spies = (['log', 'warn', 'error', 'debug', 'verbose'] as const).map((level) => jest.spyOn(Logger.prototype, level).mockImplementation((...args: unknown[]) => void lines.push(args.map(String).join(' '))));
      const response = await call('POST', '/api/localai/gateway/keys', as('contributor'), { name: 'k' });
      const token = (response.json() as { data: { token: string } }).data.token;
      spies.forEach((spy) => spy.mockRestore());
      expect(lines.join('\n')).not.toContain(token);
      expect(lines.join('\n')).not.toContain(token.slice(10));
    });

    it('a second request creates a different token', async () => {
      const a = (await call('POST', '/api/localai/gateway/keys', as('contributor'), { name: 'a' })).json() as { data: { token: string } };
      const b = (await call('POST', '/api/localai/gateway/keys', as('contributor'), { name: 'b' })).json() as { data: { token: string } };
      expect(a.data.token).not.toBe(b.data.token);
    });

    it.each([
      ['a service key', { name: 'k', kind: 'service' }],
      ['an owner other than self', { name: 'k', ownerUserId: '00000000-0000-4000-8000-0000000000bb' }],
      ['higher limits than the default', { name: 'k', limits: { rpm: 600 } }],
    ])('403 for a contributor asking for %s, and nothing is created', async (_label, body) => {
      expect((await call('POST', '/api/localai/gateway/keys', as('contributor'), body)).statusCode).toBe(403);
      expect(prismaMock.localaiGatewayKey.create).not.toHaveBeenCalled();
    });

    it('201 for an administrator creating a service key with no expiry and no budget', async () => {
      const response = await call('POST', '/api/localai/gateway/keys', as('admin'), { name: 'open-webui', kind: 'service' });
      expect(response.statusCode).toBe(201);
      expect((response.json() as { data: { key: unknown } }).data.key).toMatchObject({ kind: 'service', expiresAt: null, limits: { rpm: 600, concurrent: 16, dailyTokens: null } });
    });

    it('takes the defaults from the stored settings: changing gateway.defaults.user changes the next key', async () => {
      storedSettings = { localai: { gateway: { defaults: { user: { rpm: 5, concurrent: 1, dailyTokens: 1000, expiresInDays: 7 } } } } };
      const response = await call('POST', '/api/localai/gateway/keys', as('contributor'), { name: 'k' });
      expect((response.json() as { data: { key: { limits: unknown } } }).data.key.limits).toEqual({ rpm: 5, concurrent: 1, dailyTokens: 1000 });
    });

    it('400 for a body it does not understand, an unknown model, or an expiry beyond the maximum', async () => {
      for (const body of [{}, { name: '' }, { name: 'k', extra: 1 }, { name: 'k', expiresInDays: 366 }, { name: 'k', expiresInDays: null }, { name: 'k', allowedModels: ['no-such-model'] }, { name: 'k', limits: { dailyTokens: null } }]) {
        const response = await call('POST', '/api/localai/gateway/keys', as('contributor'), body);
        expect([response.statusCode, JSON.stringify(body)]).toEqual([400, JSON.stringify(body)]);
      }
      expect(prismaMock.localaiGatewayKey.create).not.toHaveBeenCalled();
    });
  });

  describe('GET /api/localai/gateway/keys', () => {
    it("lists the caller's keys: prefix, name, models, limits, expiry, last used; never the hash or a token", async () => {
      const user = session('contributor');
      prismaMock.localaiGatewayKey.findMany.mockResolvedValue([keyRow(user.id)]);
      const response = await call('GET', '/api/localai/gateway/keys', user.accessToken);
      expect(response.statusCode).toBe(200);
      expect(prismaMock.localaiGatewayKey.findMany).toHaveBeenCalledWith(expect.objectContaining({ where: { userId: user.id, revokedAt: null } }));
      const { data } = response.json() as { data: { keys: Array<Record<string, unknown>> } };
      expect(data.keys).toHaveLength(1);
      expect(data.keys[0]).toMatchObject({ tokenPrefix: 'lai_Ab3dEf', name: 'OpenCode laptop', allowedModels: [], limits: { rpm: 60, concurrent: 4, dailyTokens: 2_000_000 }, expiresAt: '2027-02-02T12:00:00.000Z', lastUsedAt: null });
      expect(data.keys[0]).not.toHaveProperty('owner');
      expect(response.body).not.toContain('a'.repeat(64));
      expect(response.body).not.toContain('tokenHash');
    });

    it('?all=1: 403 for a contributor, every key with its owner for an administrator', async () => {
      expect((await call('GET', '/api/localai/gateway/keys?all=1', as('contributor'))).statusCode).toBe(403);
      prismaMock.localaiGatewayKey.findMany.mockResolvedValue([keyRow('00000000-0000-4000-8000-0000000000aa')]);
      const response = await call('GET', '/api/localai/gateway/keys?all=1', as('admin'));
      expect(response.statusCode).toBe(200);
      expect(prismaMock.localaiGatewayKey.findMany).toHaveBeenLastCalledWith(expect.objectContaining({ where: { revokedAt: null } }));
      expect((response.json() as { data: { keys: Array<{ owner: unknown }> } }).data.keys[0]!.owner).toEqual({ email: 'person-a@example.test', displayName: 'Person A' });
    });

    it('400 for a parameter it does not know', async () => {
      expect((await call('GET', '/api/localai/gateway/keys?bogus=1', as('contributor'))).statusCode).toBe(400);
    });
  });

  describe('PATCH /api/localai/gateway/keys/:id', () => {
    it('the owner renames; the response is the key; the auth cache entry is evicted', async () => {
      const user = session('contributor');
      prismaMock.localaiGatewayKey.findUnique.mockImplementation(async () => keyRow(user.id));
      const response = await call('PATCH', `/api/localai/gateway/keys/${KEY_ID}`, user.accessToken, { name: 'Renamed' });
      expect(response.statusCode).toBe(200);
      expect(prismaMock.localaiGatewayKey.updateMany).toHaveBeenCalledWith({ where: { id: KEY_ID, revokedAt: null }, data: { name: 'Renamed' } });
      expect(evict).toHaveBeenCalledWith('a'.repeat(64));
    });

    it('403 for the owner changing limits or widening; 404 for a key that is not theirs', async () => {
      const user = session('contributor');
      prismaMock.localaiGatewayKey.findUnique.mockImplementation(async () => keyRow(user.id, { allowedModels: ['lab-coder'] }));
      expect((await call('PATCH', `/api/localai/gateway/keys/${KEY_ID}`, user.accessToken, { limits: { rpm: 1 } })).statusCode).toBe(403);
      expect((await call('PATCH', `/api/localai/gateway/keys/${KEY_ID}`, user.accessToken, { allowedModels: [] })).statusCode).toBe(403);
      prismaMock.localaiGatewayKey.findUnique.mockImplementation(async () => keyRow('00000000-0000-4000-8000-0000000000aa'));
      expect((await call('PATCH', `/api/localai/gateway/keys/${KEY_ID}`, user.accessToken, { name: 'x' })).statusCode).toBe(404);
      expect(prismaMock.localaiGatewayKey.updateMany).not.toHaveBeenCalled();
      expect(evict).not.toHaveBeenCalled();
    });

    it('an administrator changes limits of anyone\'s key', async () => {
      const response = await call('PATCH', `/api/localai/gateway/keys/${KEY_ID}`, as('admin'), { limits: { rpm: 500 } });
      expect(response.statusCode).toBe(200);
      expect(prismaMock.localaiGatewayKey.updateMany).toHaveBeenCalledWith({ where: { id: KEY_ID, revokedAt: null }, data: { rpmLimit: 500 } });
    });

    it('400 for a bad id or an empty body', async () => {
      expect((await call('PATCH', '/api/localai/gateway/keys/not-a-uuid', as('contributor'), { name: 'x' })).statusCode).toBe(400);
      expect((await call('PATCH', `/api/localai/gateway/keys/${KEY_ID}`, as('contributor'), {})).statusCode).toBe(400);
    });
  });

  describe('DELETE /api/localai/gateway/keys/:id', () => {
    it('the owner revokes: 200, revokedAt set, the cache entry evicted, one key_revoked audit row', async () => {
      const user = session('contributor');
      prismaMock.localaiGatewayKey.findUnique.mockImplementation(async () => keyRow(user.id));
      const response = await call('DELETE', `/api/localai/gateway/keys/${KEY_ID}`, user.accessToken);
      expect(response.statusCode).toBe(200);
      expect(prismaMock.localaiGatewayKey.updateMany).toHaveBeenCalledWith({ where: { id: KEY_ID, revokedAt: null }, data: { revokedAt: expect.any(Date) } });
      expect(evict).toHaveBeenCalledWith('a'.repeat(64));
      const actions = prismaMock.auditEvent.create.mock.calls.map((c: [{ data: { action: string } }]) => c[0].data.action);
      expect(actions).toContain('localai.gateway.key_revoked');
    });

    it('revoking again is 200 and writes no second audit row', async () => {
      const user = session('contributor');
      prismaMock.localaiGatewayKey.findUnique.mockImplementation(async () => keyRow(user.id, { revokedAt: new Date('2026-11-04T11:00:00.000Z') }));
      prismaMock.localaiGatewayKey.updateMany.mockResolvedValue({ count: 0 });
      const response = await call('DELETE', `/api/localai/gateway/keys/${KEY_ID}`, user.accessToken);
      expect(response.statusCode).toBe(200);
      expect((response.json() as { data: { status: string } }).data.status).toBe('revoked');
      expect(prismaMock.auditEvent.create).not.toHaveBeenCalled();
    });

    it("404 for someone else's key; an administrator may revoke it", async () => {
      expect((await call('DELETE', `/api/localai/gateway/keys/${KEY_ID}`, as('contributor'))).statusCode).toBe(404);
      expect((await call('DELETE', `/api/localai/gateway/keys/${KEY_ID}`, as('admin'))).statusCode).toBe(200);
    });
  });

  describe('GET /api/localai/settings', () => {
    it('401 without a session; 200 for a viewer with the dotted keys and the defaults', async () => {
      expect((await call('GET', '/api/localai/settings')).statusCode).toBe(401);
      const response = await call('GET', '/api/localai/settings', as('viewer'));
      expect(response.statusCode).toBe(200);
      const { settings } = (response.json() as { data: { settings: Record<string, unknown> } }).data;
      expect(settings['gateway.defaults.user']).toEqual({ rpm: 60, concurrent: 4, dailyTokens: 2_000_000, expiresInDays: 90 });
      expect(settings['gateway.maxUserKeyDays']).toBe(365);
      expect(settings['alerts.repeatHours']).toBe(24);
    });

    it('the tailnet name is hidden from a viewer and shown to an administrator', async () => {
      storedSettings = { localai: { gateway: { tailnetSuffix: 'lab-net.ts.net' } } };
      const viewer = (await call('GET', '/api/localai/settings', as('viewer'))).json() as { data: { settings: Record<string, unknown> } };
      expect(viewer.data.settings).not.toHaveProperty('gateway.tailnetSuffix');
      const admin = (await call('GET', '/api/localai/settings', as('admin'))).json() as { data: { settings: Record<string, unknown> } };
      expect(admin.data.settings['gateway.tailnetSuffix']).toBe('lab-net.ts.net');
    });
  });

  describe('PUT /api/localai/settings', () => {
    it('401 without a session; 403 for a viewer and a contributor (neither admin permission)', async () => {
      expect((await call('PUT', '/api/localai/settings', undefined, { 'alerts.repeatHours': 12 })).statusCode).toBe(401);
      expect((await call('PUT', '/api/localai/settings', as('viewer'), { 'alerts.repeatHours': 12 })).statusCode).toBe(403);
      expect((await call('PUT', '/api/localai/settings', as('contributor'), { 'alerts.repeatHours': 12 })).statusCode).toBe(403);
      expect(prismaMock.systemSettings.update).not.toHaveBeenCalled();
    });

    it('an administrator holds both permissions: a gateway key and an alerts key, mixed, are written', async () => {
      const response = await call('PUT', '/api/localai/settings', as('admin'), { 'gateway.maxUserKeyDays': 30, 'alerts.repeatHours': 12 });
      expect(response.statusCode).toBe(200);
      expect(storedSettings).toMatchObject({ localai: { gateway: { maxUserKeyDays: 30 }, alerts: { repeatHours: 12 } } });
      const { settings } = (response.json() as { data: { settings: Record<string, unknown> } }).data;
      expect(settings['gateway.maxUserKeyDays']).toBe(30);
      expect(settings['alerts.repeatHours']).toBe(12);
    });

    it('a write keeps what was stored before (other keys, other namespaces)', async () => {
      storedSettings = { localai: { alerts: { driveStaleDays: 7 } }, nodes: { staleHeartbeatSeconds: 45 } };
      await call('PUT', '/api/localai/settings', as('admin'), { 'gateway.maxAttempts': 2 });
      expect(storedSettings).toMatchObject({ localai: { alerts: { driveStaleDays: 7 }, gateway: { maxAttempts: 2 } }, nodes: { staleHeartbeatSeconds: 45 } });
    });

    it('400 for an unknown key or a bad value, and nothing is written', async () => {
      for (const body of [{ 'gateway.nope': 1 }, { 'gateway.maxAttempts': 99 }, {}, { 'gateway.corsOrigins': ['*'] }]) {
        expect((await call('PUT', '/api/localai/settings', as('admin'), body)).statusCode).toBe(400);
      }
      expect(prismaMock.systemSettings.update).not.toHaveBeenCalled();
    });

    it('the platform routes refuse the namespace, so system_settings:write is not a way around the per-prefix rule', async () => {
      const response = await call('PATCH', '/api/system-settings', as('admin'), { localai: { gateway: { maxUserKeyDays: 3650 } } });
      expect(response.statusCode).toBe(400);
      expect(prismaMock.systemSettings.update).not.toHaveBeenCalled();
    });
  });
});
