// `GET /api/localai/alerts` and `POST /api/localai/alerts/:id/ack` over HTTP, on the whole AppModule over a mocked database
// (no PostgreSQL): the real guards, the seeded role grants and the real service. The same queries against real SQL are
// `alerts.db.spec.ts`. RBAC: lab:read lists, lab:operate acknowledges, a viewer can never acknowledge.
import fastifyCookie from '@fastify/cookie';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';

const ALERT_ID = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee1';
const NODE_ROW = 'dddddddd-dddd-4ddd-8ddd-ddddddddddd1';

function alertRow(patch: Record<string, unknown> = {}) {
  return {
    id: ALERT_ID,
    nodeId: NODE_ROW,
    kind: 'ark.drive_stale',
    code: 'LAI-CLD-022',
    severity: 'critical',
    state: 'active',
    title: 'Ark drive not synced for 95 days',
    details: { days: 95, secretLookingValue: 'must-not-leak' },
    firstRaisedAt: new Date('2026-11-01T10:00:00.000Z'),
    lastSeenAt: new Date('2026-11-04T10:00:00.000Z'),
    clearedAt: null,
    acknowledgedAt: null,
    acknowledgedById: null,
    notifiedAt: new Date('2026-11-01T10:00:00.000Z'),
    node: { nodeId: 'ai-n2', displayName: 'Node Two' },
    ...patch,
  };
}

describe('Alert routes over HTTP', () => {
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
    prismaMock.localaiAlert.findMany.mockReset().mockResolvedValue([alertRow()]);
    prismaMock.localaiAlert.findUnique.mockReset().mockImplementation(async ({ where }: { where: { id: string } }) => (where.id === ALERT_ID ? alertRow() : null));
    prismaMock.localaiAlert.updateMany.mockReset().mockResolvedValue({ count: 1 });
    prismaMock.auditEvent.create.mockReset().mockResolvedValue({});
  });

  const get = (url: string, token?: string) => app.inject({ method: 'GET', url, headers: token ? { authorization: `Bearer ${token}` } : {} });
  const post = (url: string, token?: string) => app.inject({ method: 'POST', url, headers: token ? { authorization: `Bearer ${token}` } : {} });
  const as = (role: FixtureRole) => registerMockUser(jwt, role).accessToken;

  describe('GET /api/localai/alerts', () => {
    it('401 without a session', async () => {
      expect((await get('/api/localai/alerts')).statusCode).toBe(401);
      expect(prismaMock.localaiAlert.findMany).not.toHaveBeenCalled();
    });

    it.each<FixtureRole>(['viewer', 'contributor', 'admin'])('200 for a %s (lab:read), in the wire shape with the plain fix', async (role) => {
      const response = await get('/api/localai/alerts', as(role));
      expect(response.statusCode).toBe(200);
      const { data } = response.json() as { data: { alerts: Array<Record<string, unknown>> } };
      expect(data.alerts).toEqual([
        {
          id: ALERT_ID,
          nodeId: 'ai-n2',
          nodeName: 'Node Two',
          kind: 'ark.drive_stale',
          code: 'LAI-CLD-022',
          severity: 'critical',
          state: 'active',
          title: 'Ark drive not synced for 95 days',
          fix: 'Plug the AI-ARK drive into the node and press "Sync now" (or run `aictl ark sync`).',
          firstRaisedAt: '2026-11-01T10:00:00.000Z',
          lastSeenAt: '2026-11-04T10:00:00.000Z',
          clearedAt: null,
          acknowledgedAt: null,
        },
      ]);
    });

    it('never returns the raw details of an alert', async () => {
      const response = await get('/api/localai/alerts', as('viewer'));
      expect(response.body).not.toContain('must-not-leak');
    });

    it('defaults to the active alerts and passes the state and node filters to the query', async () => {
      await get('/api/localai/alerts', as('viewer'));
      expect(prismaMock.localaiAlert.findMany).toHaveBeenLastCalledWith(expect.objectContaining({ where: { state: 'active' }, take: 100 }));
      await get('/api/localai/alerts?state=cleared&nodeId=ai-n2&limit=5', as('viewer'));
      expect(prismaMock.localaiAlert.findMany).toHaveBeenLastCalledWith(
        expect.objectContaining({ where: { state: 'cleared', node: { is: { nodeId: 'ai-n2' } } }, take: 5, orderBy: { clearedAt: 'desc' } }),
      );
      await get('/api/localai/alerts?state=all', as('viewer'));
      expect(prismaMock.localaiAlert.findMany).toHaveBeenLastCalledWith(expect.objectContaining({ where: {} }));
    });

    it('400 for a state, node id, limit or parameter it does not know', async () => {
      for (const query of ['state=bogus', 'nodeId=%27%3B--', 'limit=0', 'limit=1000', 'extra=1']) {
        expect((await get(`/api/localai/alerts?${query}`, as('viewer'))).statusCode).toBe(400);
      }
    });
  });

  describe('POST /api/localai/alerts/:id/ack', () => {
    it('401 without a session', async () => {
      expect((await post(`/api/localai/alerts/${ALERT_ID}/ack`)).statusCode).toBe(401);
      expect(prismaMock.localaiAlert.updateMany).not.toHaveBeenCalled();
    });

    it('403 for a viewer (lab:read only): nothing is written', async () => {
      const response = await post(`/api/localai/alerts/${ALERT_ID}/ack`, as('viewer'));
      expect(response.statusCode).toBe(403);
      expect(prismaMock.localaiAlert.updateMany).not.toHaveBeenCalled();
    });

    it.each<FixtureRole>(['contributor', 'admin'])('200 for a %s (lab:operate): sets the acknowledgement only for an active, unacknowledged alert', async (role) => {
      prismaMock.localaiAlert.findUnique
        .mockResolvedValueOnce(alertRow())
        .mockResolvedValueOnce(alertRow({ acknowledgedAt: new Date('2026-11-04T11:00:00.000Z'), acknowledgedById: 'u' }));
      const response = await post(`/api/localai/alerts/${ALERT_ID}/ack`, as(role));
      expect(response.statusCode).toBe(200);
      expect(prismaMock.localaiAlert.updateMany).toHaveBeenCalledWith({
        where: { id: ALERT_ID, state: 'active', acknowledgedAt: null },
        data: { acknowledgedAt: expect.any(Date), acknowledgedById: expect.any(String) },
      });
      const { data } = response.json() as { data: { state: string; acknowledgedAt: string | null } };
      expect(data).toMatchObject({ state: 'active', acknowledgedAt: '2026-11-04T11:00:00.000Z' });
    });

    it('404 for an unknown alert, 409 for a cleared one, 400 for an id that is not a uuid', async () => {
      expect((await post('/api/localai/alerts/eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee9/ack', as('contributor'))).statusCode).toBe(404);
      prismaMock.localaiAlert.findUnique.mockResolvedValue(alertRow({ state: 'cleared', clearedAt: new Date() }));
      expect((await post(`/api/localai/alerts/${ALERT_ID}/ack`, as('contributor'))).statusCode).toBe(409);
      expect((await post('/api/localai/alerts/not-a-uuid/ack', as('contributor'))).statusCode).toBe(400);
      expect(prismaMock.localaiAlert.updateMany).not.toHaveBeenCalled();
    });
  });
});
