// `GET /api/localai/audit` and `GET /api/localai/audit/filters` over HTTP (CLOUD-16), on the whole AppModule over a
// mocked database (no PostgreSQL): the real guards and the seeded role grants, the real controller, service and
// summary formatter. This proves who may read (lab:admin only), the query validation, how the filters combine into
// one `where`, and the wire shape. The same queries against real SQL are `audit.db.spec.ts`.
import fastifyCookie from '@fastify/cookie';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { buildPermissionCatalog, catalogGrants } from '@marinoscar/platform-api/core';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { AUDIT_GROUPS, KNOWN_AUDIT_ACTIONS } from '../../src/localai/audit/audit-actions';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';

const ADMIN_USER = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1';
const CMD = '6b3c1c9e-7a52-4f4e-9d5e-0a1b2c3d4e5f';
const NOW = new Date('2026-11-04T12:00:00.000Z');

function event(patch: Record<string, unknown> = {}) {
  return {
    id: 'cccccccc-cccc-4ccc-8ccc-ccccccccccc1',
    actorUserId: ADMIN_USER,
    action: 'localai.command.requested',
    targetType: 'localai_command',
    targetId: CMD,
    meta: { nodeId: 'ai-n2', type: 'localai.model.load', params: JSON.stringify({ model_id: 'qwen3.6-35b-a3b-q4km' }) },
    orgId: null,
    createdAt: new Date('2026-11-03T15:04:00.000Z'),
    ...patch,
  };
}

describe('Audit routes over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;

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
  });

  afterAll(async () => {
    clearMockUsers();
    await app?.close();
  });

  beforeEach(() => {
    prismaMock.auditEvent.findMany.mockReset().mockResolvedValue([event()]);
    prismaMock.auditEvent.count.mockReset().mockResolvedValue(1);
    prismaMock.user.findMany.mockReset().mockResolvedValue([{ id: ADMIN_USER, email: 'admin@example.test' }]);
    prismaMock.localaiNode.findMany.mockReset().mockResolvedValue([]);
    prismaMock.localaiNode.findUnique.mockReset().mockResolvedValue({ id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee2' });
  });

  const get = (url: string, token?: string) => app.inject({ method: 'GET', url, headers: token ? { authorization: `Bearer ${token}` } : {} });
  const as = (role: FixtureRole) => registerMockUser(jwt, role).accessToken;
  const admin = () => as('admin');
  const lastWhere = () => prismaMock.auditEvent.findMany.mock.calls.at(-1)[0] as { where: any; orderBy: unknown; skip: number; take: number };

  describe('who may read (lab:admin only)', () => {
    it('only the system admin role holds lab:admin: org_admin, contributor and viewer do not', () => {
      const holders = catalogGrants(buildPermissionCatalog())
        .filter((grant) => grant.permission === 'lab:admin')
        .map((grant) => grant.role);
      expect(holders).toEqual(['admin']);
    });

    it('401 without a session on both routes', async () => {
      expect((await get('/api/localai/audit')).statusCode).toBe(401);
      expect((await get('/api/localai/audit/filters')).statusCode).toBe(401);
      expect(prismaMock.auditEvent.findMany).not.toHaveBeenCalled();
    });

    it.each<FixtureRole>(['viewer', 'contributor'])('403 for a %s on both routes, before any read', async (role) => {
      const token = as(role);
      expect((await get('/api/localai/audit', token)).statusCode).toBe(403);
      expect((await get('/api/localai/audit/filters', token)).statusCode).toBe(403);
      expect(prismaMock.auditEvent.findMany).not.toHaveBeenCalled();
      expect(prismaMock.localaiNode.findMany).not.toHaveBeenCalled();
    });

    it('200 for the admin', async () => {
      expect((await get('/api/localai/audit', admin())).statusCode).toBe(200);
    });
  });

  describe('the default query', () => {
    it('reads localai.* rows newest first, 50 per page, from 30 days ago, in the data envelope', async () => {
      const before = Date.now();
      const response = await get('/api/localai/audit', admin());
      const after = Date.now();
      expect(response.statusCode).toBe(200);
      const call = lastWhere();
      expect(call.where.action).toEqual({ startsWith: 'localai.' });
      expect(call.orderBy).toEqual([{ createdAt: 'desc' }, { id: 'desc' }]);
      expect(call.take).toBe(50);
      expect(call.skip).toBe(0);
      const gte = (call.where.createdAt.gte as Date).getTime();
      const window = 30 * 24 * 60 * 60 * 1000;
      expect(gte).toBeGreaterThanOrEqual(before - window);
      expect(gte).toBeLessThanOrEqual(after - window);
      expect(call.where.createdAt.lte).toBeUndefined();
      expect(call.where.OR).toBeUndefined();
      expect((response.json() as { data: unknown }).data).toMatchObject({ page: 1, pageSize: 50, total: 1, totalPages: 1 });
    });

    it('answers the row shape of the issue: actor, action, target, summary and link', async () => {
      const body = (await get('/api/localai/audit', admin())).json() as { data: { items: unknown[] } };
      expect(body.data.items).toEqual([
        {
          id: 'cccccccc-cccc-4ccc-8ccc-ccccccccccc1',
          at: '2026-11-03T15:04:00.000Z',
          actor: { kind: 'user', label: 'admin@example.test' },
          action: 'localai.command.requested',
          targetType: 'localai_command',
          targetId: CMD,
          summary: 'model.load qwen3.6-35b-a3b-q4km on ai-n2',
          link: `/lab/nodes/ai-n2?command=${CMD}`,
        },
      ]);
    });

    it('labels the actor as a user, a node or the system', async () => {
      prismaMock.auditEvent.findMany.mockResolvedValue([
        event({ id: '1', action: 'localai.node.enrolled', targetType: 'localai_node', targetId: 'ai-n3', meta: { workerNodeId: CMD, agentVersion: '0.4.1' } }),
        event({ id: '2', actorUserId: null, action: 'localai.command.settled', meta: { nodeId: 'ai-n2', type: 'localai.model.load', status: 'expired' } }),
        event({ id: '3' }),
      ]);
      const { data } = (await get('/api/localai/audit', admin())).json() as { data: { items: Array<{ actor: unknown }> } };
      expect(data.items.map((item) => item.actor)).toEqual([
        { kind: 'node', label: 'node ai-n3' },
        { kind: 'system', label: 'system' },
        { kind: 'user', label: 'admin@example.test' },
      ]);
    });

    it('an unknown action still renders, as its own name', async () => {
      prismaMock.auditEvent.findMany.mockResolvedValue([event({ action: 'localai.future.thing', targetType: 'localai_future', targetId: 'x1', meta: { anything: 'goes' } })]);
      const { data } = (await get('/api/localai/audit', admin())).json() as { data: { items: Array<{ summary: string; link: string | null }> } };
      expect(data.items[0]).toMatchObject({ summary: 'localai.future.thing', link: null });
    });

    it('never puts a secret-shaped meta value on the wire', async () => {
      const token = `nod_${'a1B2'.repeat(8)}`;
      const hex = 'cd'.repeat(32);
      prismaMock.auditEvent.findMany.mockResolvedValue([
        event({ action: 'localai.node.credential_minted', targetType: 'localai_node', targetId: 'ai-n3', meta: { tokenPrefix: token, token, key: hex, authorization: `Bearer ${token}` } }),
        event({ id: '9', meta: { nodeId: 'ai-n2', type: 'localai.model.load', params: JSON.stringify({ model_id: 'qwen3.5-9b-q4km', password: token }) } }),
      ]);
      const response = await get('/api/localai/audit', admin());
      expect(response.body).not.toContain(token);
      expect(response.body).not.toContain(hex);
      expect(response.body).not.toContain('password');
      // A row carries no `meta` of its own: only the whitelisted summary derived from it.
      const { data } = response.json() as { data: { items: Array<Record<string, unknown>> } };
      for (const item of data.items) expect(Object.keys(item).sort()).toEqual(['action', 'actor', 'at', 'id', 'link', 'summary', 'targetId', 'targetType']);
    });
  });

  describe('filters', () => {
    it('node, action prefix, date range and page combine into one where', async () => {
      const response = await get('/api/localai/audit?nodeId=ai-n2&action=localai.command.&from=2026-11-01&to=2026-11-02&page=3', admin());
      expect(response.statusCode).toBe(200);
      const call = lastWhere();
      expect(call.skip).toBe(100);
      expect(call.where.action).toEqual({ startsWith: 'localai.command.' });
      expect(call.where.createdAt).toEqual({ gte: new Date('2026-11-01T00:00:00.000Z'), lte: new Date('2026-11-02T23:59:59.999Z') });
      expect(call.where.OR).toEqual([
        { targetType: 'localai_node', targetId: { in: ['ai-n2', 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee2'] } },
        { meta: { path: ['nodeId'], equals: 'ai-n2' } },
        { meta: { path: ['claimedNodeId'], equals: 'ai-n2' } },
        { meta: { path: ['nodeId'], equals: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee2' } },
        { meta: { path: ['claimedNodeId'], equals: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee2' } },
      ]);
      expect(prismaMock.auditEvent.count).toHaveBeenLastCalledWith({ where: call.where });
    });

    it('an unknown node id still filters by the id as given (a refused credential names ids that do not exist)', async () => {
      prismaMock.localaiNode.findUnique.mockResolvedValue(null);
      await get('/api/localai/audit?nodeId=ai-n99', admin());
      expect(lastWhere().where.OR[0]).toEqual({ targetType: 'localai_node', targetId: { in: ['ai-n99'] } });
    });

    it('takes full ISO instants and empty fields (a browser form sends them)', async () => {
      const response = await get('/api/localai/audit?nodeId=&action=&from=2026-11-01T10:00:00.000Z&to=', admin());
      expect(response.statusCode).toBe(200);
      const call = lastWhere();
      expect(call.where.createdAt).toEqual({ gte: new Date('2026-11-01T10:00:00.000Z') });
      expect(call.where.OR).toBeUndefined();
      expect(call.where.action).toEqual({ startsWith: 'localai.' });
    });
  });

  describe('validation (400, and nothing is read)', () => {
    it.each([
      ['an action outside the localai namespace', 'action=users.'],
      ['an action with a wildcard', 'action=localai.%25'],
      ['an action with a space', 'action=localai.command%20x'],
      ['a node id with a slash', 'nodeId=ai-n2/x'],
      ['a node id with a quote', "nodeId=ai-n2'"],
      ['page 0', 'page=0'],
      ['page that is not a number', 'page=abc'],
      ['a date that is not a date', 'from=yesterday'],
      ['a calendar day that does not exist', 'to=2026-13-45'],
      ['from after to', 'from=2026-11-03&to=2026-11-01'],
      ['an unknown parameter', 'pageSize=500'],
    ])('%s', async (_name, query) => {
      const response = await get(`/api/localai/audit?${query}`, admin());
      expect(response.statusCode).toBe(400);
      expect(prismaMock.auditEvent.findMany).not.toHaveBeenCalled();
    });
  });

  describe('GET /api/localai/audit/filters', () => {
    it('lists the known actions (one constant), the groups and every node in natural order', async () => {
      prismaMock.localaiNode.findMany.mockResolvedValue([
        { nodeId: 'ai-n10', displayName: 'Ten', phase: 'enrolled' },
        { nodeId: 'ai-n2', displayName: 'Two', phase: 'retired' },
      ]);
      const response = await get('/api/localai/audit/filters', admin());
      expect(response.statusCode).toBe(200);
      const { data } = response.json() as { data: { actions: Array<{ action: string }>; groups: unknown[]; nodes: Array<{ nodeId: string }>; pageSize: number } };
      expect(data.actions.map((entry) => entry.action)).toEqual(KNOWN_AUDIT_ACTIONS.map((entry) => entry.action));
      expect(data.actions.map((entry) => entry.action)).toContain('localai.node.tailnet_revealed');
      expect(data.groups).toEqual(AUDIT_GROUPS);
      expect(data.nodes.map((node) => node.nodeId)).toEqual(['ai-n2', 'ai-n10']);
      expect(data.pageSize).toBe(50);
    });
  });
});
