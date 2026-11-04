// `GET /api/localai/nodes` and `GET /api/localai/nodes/:nodeId` over HTTP, on the whole AppModule over a mocked database
// (no PostgreSQL): the real guards, the seeded role grants and the real service + builder. The same queries against real
// SQL are `lab-nodes.db.spec.ts`.
import fastifyCookie from '@fastify/cookie';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { LabNodesResponseSchema } from '../../src/localai/nodes/dto/node-card';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';

const WORKER = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1';
const NODE_ROW = 'dddddddd-dddd-4ddd-8ddd-ddddddddddd1';
// A made-up value in the shape of a tailnet address, built from parts so no literal address sits in the repository.
const TAILNET_IP = [100, 64, 1, 2].join('.');

function nodeRow(patch: Record<string, unknown> = {}) {
  return {
    id: NODE_ROW,
    nodeId: 'ai-n1',
    displayName: 'Node One',
    hardware: null,
    backend: 'cuda',
    publicHost: 'ai-n1.example.test',
    servePattern: 'B',
    tailnetMachine: 'machine-one',
    tailnetIp: TAILNET_IP,
    profile: 'large-24gb',
    mode: 'online',
    connectivity: 'full',
    agentVersion: '0.4.0',
    catalogVersion: '2026.11.02-1',
    catalogPin: null,
    gatewayEnabled: true,
    gatewayPriority: 100,
    phase: 'enrolled',
    workerNodeId: WORKER,
    nodeCredentialId: null,
    lastState: { mode: 'online', hardware: { gpus: [{ name: 'GPU One', vram_total_mib: 24576, vram_used_mib: 1024 }] }, ark: { roots: [], drive_last_synced_at: null } },
    lastStateAt: new Date(Date.now() - 5_000),
    lastStateSeq: 3n,
    keyHint: null,
    keyReportedAt: new Date(),
    publishChecklist: {},
    createdById: null,
    createdAt: new Date(),
    updatedAt: new Date(),
    retiredAt: null,
    ...patch,
  };
}

describe('Lab overview routes over HTTP', () => {
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
    prismaMock.localaiNode.findMany.mockReset().mockResolvedValue([nodeRow()]);
    prismaMock.localaiNode.findUnique.mockReset().mockImplementation(async ({ where }: { where: { nodeId: string } }) => (where.nodeId === 'ai-n1' ? nodeRow() : null));
    prismaMock.localaiNodeModel.findMany.mockReset().mockResolvedValue([{ nodeId: NODE_ROW, modelId: 'model-one-q4', status: 'loaded', isDefault: true }]);
    prismaMock.workerNode.findMany.mockReset().mockResolvedValue([{ id: WORKER, status: 'online', lastHeartbeatAt: new Date() }]);
    prismaMock.localaiAlert.groupBy.mockReset().mockResolvedValue([
      { nodeId: NODE_ROW, severity: 'critical', _count: { _all: 1 } },
      { nodeId: NODE_ROW, severity: 'warning', _count: { _all: 2 } },
      { nodeId: null, severity: 'warning', _count: { _all: 4 } },
    ]);
    prismaMock.localaiCatalogVersion.findFirst.mockReset().mockResolvedValue({ version: '2026.11.02-1' });
  });

  const get = (url: string, token?: string) => app.inject({ method: 'GET', url, headers: token ? { authorization: `Bearer ${token}` } : {} });
  const as = (role: FixtureRole) => registerMockUser(jwt, role).accessToken;

  describe('GET /api/localai/nodes', () => {
    it('401 without a session', async () => {
      expect((await get('/api/localai/nodes')).statusCode).toBe(401);
      expect(prismaMock.localaiNode.findMany).not.toHaveBeenCalled();
    });

    it.each<FixtureRole>(['viewer', 'contributor', 'admin'])('200 for a %s (lab:read), cards and counts in the wire shape', async (role) => {
      const response = await get('/api/localai/nodes', as(role));
      expect(response.statusCode).toBe(200);
      const body = response.json() as { data: unknown; meta: unknown };
      const data = LabNodesResponseSchema.parse(body.data);
      expect(data.summary).toEqual({ nodes: 1, online: 1, offlineMode: 0, activeAlerts: 7, routable: 1 });
      expect(data.nodes[0]).toMatchObject({
        nodeId: 'ai-n1',
        health: 'healthy',
        mode: 'online',
        routable: true,
        hardware: 'GPU One 24 GB',
        loadedModels: [{ id: 'model-one-q4', isDefault: true }],
        alerts: { active: 3, critical: 1 },
        agentBehind: false,
        catalogBehind: false,
      });
    });

    it('never contains the tailnet IP, whoever asks (admin included)', async () => {
      for (const role of ['viewer', 'admin'] as const) {
        const response = await get('/api/localai/nodes?reveal=tailnet', as(role));
        expect(response.statusCode).toBe(200);
        expect(response.body).not.toContain(TAILNET_IP);
        expect(response.body).not.toMatch(/tailnet/i);
      }
    });

    it('leaves retired nodes out of the query', async () => {
      await get('/api/localai/nodes', as('viewer'));
      expect(prismaMock.localaiNode.findMany).toHaveBeenCalledWith({ where: { phase: { not: 'retired' } } });
    });

    it('lists nodes in numeric id order and an empty lab as an empty list', async () => {
      prismaMock.localaiNode.findMany.mockResolvedValue([nodeRow({ id: 'x2', nodeId: 'ai-n10' }), nodeRow({ id: 'x1', nodeId: 'ai-n2' })]);
      const sorted = (await get('/api/localai/nodes', as('viewer'))).json() as { data: { nodes: Array<{ nodeId: string }> } };
      expect(sorted.data.nodes.map((node) => node.nodeId)).toEqual(['ai-n2', 'ai-n10']);
      prismaMock.localaiNode.findMany.mockResolvedValue([]);
      const empty = (await get('/api/localai/nodes', as('viewer'))).json() as { data: unknown };
      expect(empty.data).toEqual({ summary: { nodes: 0, online: 0, offlineMode: 0, activeAlerts: 4, routable: 0 }, nodes: [] });
    });

    it('a node whose worker row is missing is down (EAB offline), a stale heartbeat degrades it', async () => {
      prismaMock.workerNode.findMany.mockResolvedValue([]);
      const gone = (await get('/api/localai/nodes', as('viewer'))).json() as { data: { nodes: Array<{ health: string; routable: boolean }> } };
      expect(gone.data.nodes[0]).toMatchObject({ health: 'down', routable: false });
      prismaMock.workerNode.findMany.mockResolvedValue([{ id: WORKER, status: 'online', lastHeartbeatAt: new Date(Date.now() - 3_600_000) }]);
      const stale = (await get('/api/localai/nodes', as('viewer'))).json() as { data: { nodes: Array<{ health: string }> } };
      expect(stale.data.nodes[0]!.health).toBe('degraded');
    });
  });

  describe('GET /api/localai/nodes/:nodeId', () => {
    it('401 without a session, 404 for an unknown or retired node', async () => {
      expect((await get('/api/localai/nodes/ai-n1')).statusCode).toBe(401);
      expect((await get('/api/localai/nodes/ai-n9', as('viewer'))).statusCode).toBe(404);
      prismaMock.localaiNode.findUnique.mockResolvedValue(nodeRow({ phase: 'retired' }));
      expect((await get('/api/localai/nodes/ai-n1', as('viewer'))).statusCode).toBe(404);
    });

    it('a viewer gets the card fields plus lastState, and no tailnet IP even when asking for it', async () => {
      for (const url of ['/api/localai/nodes/ai-n1', '/api/localai/nodes/ai-n1?reveal=tailnet']) {
        const response = await get(url, as('viewer'));
        expect(response.statusCode).toBe(200);
        expect(response.body).not.toContain(TAILNET_IP);
        const { data } = response.json() as { data: Record<string, unknown> };
        expect(data).toMatchObject({ nodeId: 'ai-n1', health: 'healthy' });
        expect(data.lastState).toMatchObject({ mode: 'online' });
        expect(data).not.toHaveProperty('tailnetIp');
      }
    });

    it('a contributor (lab:operate, not lab:admin) cannot reveal it either', async () => {
      const response = await get('/api/localai/nodes/ai-n1?reveal=tailnet', as('contributor'));
      expect(response.statusCode).toBe(200);
      expect(response.body).not.toContain(TAILNET_IP);
    });

    it('an admin gets it only with ?reveal=tailnet', async () => {
      const plain = await get('/api/localai/nodes/ai-n1', as('admin'));
      expect(plain.body).not.toContain(TAILNET_IP);
      const revealed = await get('/api/localai/nodes/ai-n1?reveal=tailnet', as('admin'));
      expect((revealed.json() as { data: { tailnetIp: string } }).data.tailnetIp).toBe(TAILNET_IP);
      const other = await get('/api/localai/nodes/ai-n1?reveal=everything', as('admin'));
      expect(other.body).not.toContain(TAILNET_IP);
    });
  });
});
