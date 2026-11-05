// The model policy routes over HTTP (docs/specs/gateway.md section 3.8), on the whole AppModule over a mocked database (no
// PostgreSQL): the real guards, the seeded role grants, the real services. The same writes against real SQL (the advisory
// lock, the upsert) are gateway-ondemand.e2e.spec.ts. Roles: viewer (lab:read), contributor (gateway:use), admin.
import fastifyCookie from '@fastify/cookie';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { ModelDirectoryProvider } from '../../src/localai/gateway/models/resolve';
import { MODELS_YAML } from '../../src/localai/gateway/models/models.fixtures';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';

const MODEL = 'qwen3-coder-next-q4km';
const BODY = { enabled: true, aliases: ['lab-coder'], onDemandLoad: true, loadWaitS: 90, firstByteTimeoutS: null, preferredNodes: ['ai-n2'] };

interface Row {
  modelId: string;
  enabled: boolean;
  aliases: string[];
  onDemandLoad: boolean;
  loadWaitS: number;
  firstByteTimeoutS: number | null;
  preferredNodes: string[];
  updatedById: string | null;
  updatedAt: Date;
}

describe('Gateway model policy routes over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let stored: Map<string, Row>;

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
    stored = new Map();
    // The directory caches the catalog and the policies for a few seconds; every case starts from what it stubs.
    app.get(ModelDirectoryProvider, { strict: false }).invalidate();
    // The approved catalog: one version whose models.yaml is the fixture lab's.
    prismaMock.localaiCatalogVersion.findFirst.mockReset().mockResolvedValue({ version: '2026.11.02-1', approvedAt: new Date('2026-11-02T12:00:00.000Z'), createdAt: new Date('2026-11-02T11:00:00.000Z') });
    prismaMock.localaiCatalogVersion.findUnique.mockReset().mockResolvedValue({ files: [{ path: 'models.yaml', content: MODELS_YAML }] });
    prismaMock.localaiModelPolicy.findMany.mockReset().mockImplementation(async () => [...stored.values()]);
    prismaMock.localaiModelPolicy.upsert.mockReset().mockImplementation(async ({ where, create, update }: { where: { modelId: string }; create: Row; update: Partial<Row> }) => {
      const existing = stored.get(where.modelId);
      const row: Row = { ...(existing ?? create), ...(existing ? update : {}), updatedAt: new Date('2026-11-04T12:00:00.000Z') } as Row;
      stored.set(where.modelId, row);
      return row;
    });
    prismaMock.$transaction.mockReset().mockImplementation(async (fn: (tx: unknown) => unknown) => fn(prismaMock));
    prismaMock.$executeRaw.mockReset().mockResolvedValue(0);
    prismaMock.localaiNode.findMany.mockReset().mockResolvedValue([]);
    prismaMock.auditEvent.create.mockReset().mockResolvedValue({});
  });

  const call = (method: 'GET' | 'PUT', url: string, token?: string, body?: unknown) =>
    app.inject({ method, url, headers: token ? { authorization: `Bearer ${token}` } : {}, ...(body !== undefined ? { payload: body as object } : {}) });
  const as = (role: FixtureRole) => registerMockUser(jwt, role).accessToken;
  const put = (token: string | undefined, body: unknown = BODY, model = MODEL) => call('PUT', `/api/localai/gateway/models/${model}`, token, body);
  const policyAudit = () =>
    prismaMock.auditEvent.create.mock.calls.map((c: [{ data: { action: string } }]) => c[0].data).filter((row: { action: string }) => row.action === 'localai.gateway.policy_updated');

  describe('access', () => {
    it('401 without a session', async () => {
      expect((await call('GET', '/api/localai/gateway/models')).statusCode).toBe(401);
      expect((await put(undefined)).statusCode).toBe(401);
    });

    it('403 for a viewer (no gateway:use) on both routes', async () => {
      expect((await call('GET', '/api/localai/gateway/models', as('viewer'))).statusCode).toBe(403);
      expect((await put(as('viewer'))).statusCode).toBe(403);
    });

    it('403 for a contributor on PUT (gateway:use is not gateway:admin), and nothing is written', async () => {
      expect((await put(as('contributor'))).statusCode).toBe(403);
      expect(prismaMock.localaiModelPolicy.upsert).not.toHaveBeenCalled();
      expect(policyAudit()).toEqual([]);
    });
  });

  describe('GET /api/localai/gateway/models', () => {
    it('a contributor reads the listed models (none are loaded here); an administrator sees every catalog model', async () => {
      const user = (await call('GET', '/api/localai/gateway/models', as('contributor'))).json() as { data: { models: unknown[] } };
      expect(user.data.models).toEqual([]);
      const admin = (await call('GET', '/api/localai/gateway/models', as('admin'))).json() as { data: { models: Array<{ modelId: string; state: string; policy: { stored: boolean } }> } };
      expect(admin.data.models.map((row) => row.modelId)).toEqual(['catalog-only-model', 'qwen3-coder-next-q4km', 'qwen3.5-4b-q4km', 'qwen3.5-9b-q4km', 'qwen3.6-35b-a3b-q4km', 'whisper-small']);
      expect(admin.data.models.every((row) => row.state === 'none' && row.policy.stored === false)).toBe(true);
    });

    it('is an empty list when no catalog is approved', async () => {
      prismaMock.localaiCatalogVersion.findFirst.mockResolvedValue(null);
      app.get(ModelDirectoryProvider, { strict: false }).invalidate();
      const response = await call('GET', '/api/localai/gateway/models', as('admin'));
      expect(response.statusCode).toBe(200);
      expect((response.json() as { data: { models: unknown[] } }).data.models).toEqual([]);
    });
  });

  describe('PUT /api/localai/gateway/models/:modelId', () => {
    it('200 for an administrator: the policy is stored with the author, shown in the row, and audited once', async () => {
      const user = registerMockUser(jwt, 'admin');
      const response = await call('PUT', `/api/localai/gateway/models/${MODEL}`, user.accessToken, BODY);
      expect(response.statusCode).toBe(200);
      const { data } = response.json() as { data: { modelId: string; policy: Record<string, unknown> } };
      expect(data.modelId).toBe(MODEL);
      expect(data.policy).toMatchObject({ ...BODY, stored: true, updatedAt: '2026-11-04T12:00:00.000Z' });
      expect(stored.get(MODEL)).toMatchObject({ aliases: ['lab-coder'], updatedById: user.id });
      expect(prismaMock.$executeRaw).toHaveBeenCalled(); // the advisory lock
      const audit = policyAudit();
      expect(audit).toHaveLength(1);
      expect(audit[0]).toMatchObject({ targetType: 'localai_model_policy', targetId: MODEL, actorUserId: user.id, meta: { changed: 'created', aliases: 'lab-coder', onDemandLoad: true, loadWaitS: 90 } });
    });

    it('400 for an alias equal to a catalog id, and nothing is written', async () => {
      for (const alias of ['qwen3.5-9b-q4km', MODEL]) {
        const response = await put(as('admin'), { ...BODY, aliases: [alias] });
        expect([response.statusCode, alias]).toEqual([400, alias]);
      }
      expect(stored.size).toBe(0);
      expect(policyAudit()).toEqual([]);
    });

    it('409 for an alias another model already owns', async () => {
      const admin = as('admin');
      expect((await put(admin)).statusCode).toBe(200);
      const clash = await put(admin, { ...BODY, aliases: ['lab-coder'] }, 'qwen3.5-9b-q4km');
      expect(clash.statusCode).toBe(409);
      expect(stored.has('qwen3.5-9b-q4km')).toBe(false);
      // The owner may save its own alias again.
      expect((await put(admin, { ...BODY, enabled: false })).statusCode).toBe(200);
    });

    it('404 for a model that is not in the approved catalog', async () => {
      expect((await put(as('admin'), BODY, 'no-such-model')).statusCode).toBe(404);
      expect(stored.size).toBe(0);
    });

    it.each([
      ['an empty body', {}],
      ['loadWaitS below the minimum', { ...BODY, loadWaitS: 9 }],
      ['loadWaitS above the maximum', { ...BODY, loadWaitS: 601 }],
      ['an unknown field', { ...BODY, priority: 1 }],
      ['a bad node id', { ...BODY, preferredNodes: ['laptop'] }],
      ['a bad alias', { ...BODY, aliases: ['Not An Alias'] }],
    ])('400 for %s', async (_label, body) => {
      expect((await put(as('admin'), body)).statusCode).toBe(400);
      expect(stored.size).toBe(0);
    });

    it('400 for a model id that is not shaped like one', async () => {
      expect((await put(as('admin'), BODY, 'Not_A_Model')).statusCode).toBe(400);
    });

    it('an enabled:false write is visible to the administrator as listed:false', async () => {
      const admin = as('admin');
      const response = await put(admin, { ...BODY, enabled: false });
      expect((response.json() as { data: { policy: { enabled: boolean }; listed: boolean } }).data).toMatchObject({ policy: { enabled: false }, listed: false });
    });
  });
});
