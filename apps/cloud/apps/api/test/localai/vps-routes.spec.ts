// The VPS stack-agent routes over HTTP, on the whole AppModule over a mocked database (no PostgreSQL): the real guards,
// the seeded role grants and the real controller and services; the credential store, the job queue and the offload
// policy are replaced by in-memory stand-ins. The same flows on real SQL are `vps.db.spec.ts`.
import fastifyCookie from '@fastify/cookie';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { CredentialsService } from '@marinoscar/platform-api/credentials';
import { JobsService, JobWorker, NodeOffloadService } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { fakeKey, fakeTailnet, FakeCredentials } from '../../src/localai/vps/vps-test-support';
import { JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH } from '../../src/localai/vps/vps.constants';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';

describe('VPS stack-agent routes over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  const credentials = new FakeCredentials();
  const enqueue = jest.fn(async (_input: unknown) => ({ id: 'job-77' }));
  const tail = fakeTailnet();
  const savedMode = process.env.JOBS_WORKER_MODE;

  beforeAll(async () => {
    process.env.JOBS_WORKER_MODE = 'system';
    const moduleRef = await Test.createTestingModule({ imports: [AppModule] })
      .overrideProvider(PrismaService)
      .useValue(prismaMock)
      .overrideProvider(PrismaSystemService)
      .useValue({ asSystem: () => prismaMock, runAsSystem: (_r: string, fn: (tx: unknown) => unknown) => fn(prismaMock), $connect: async () => undefined, $disconnect: async () => undefined })
      .overrideProvider(JobWorker)
      .useValue({})
      .overrideProvider(CredentialsService)
      .useValue(credentials)
      .overrideProvider(JobsService)
      .useValue({ enqueue })
      .overrideProvider(NodeOffloadService)
      .useValue({ offeredTypes: async () => [JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH] })
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
    if (savedMode === undefined) delete process.env.JOBS_WORKER_MODE;
    else process.env.JOBS_WORKER_MODE = savedMode;
    clearMockUsers();
    await app?.close();
  });

  beforeEach(() => {
    enqueue.mockClear();
    credentials.rows.clear();
    prismaMock.localaiNode.findUnique.mockReset().mockImplementation(async ({ where }: { where: { nodeId: string } }) =>
      where.nodeId === 'ai-n3' ? { nodeId: 'ai-n3', phase: 'enrolled', servePattern: 'B', ...tail, publishChecklist: {} } : null,
    );
    prismaMock.job.findFirst.mockReset().mockResolvedValue(null);
    prismaMock.workerNode.findFirst.mockReset().mockResolvedValue({ eligibleTypes: [JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH] });
    prismaMock.systemSettings.findUnique.mockReset().mockResolvedValue({ key: 'global', value: { localai: { vps: { stagingSuffix: '-staging' } } } });
  });

  const call = (method: 'GET' | 'POST' | 'PUT' | 'DELETE', url: string, role?: FixtureRole, payload?: unknown) =>
    app.inject({ method, url, headers: role ? { authorization: `Bearer ${registerMockUser(jwt, role).accessToken}` } : {}, payload: payload as never });

  const ROUTES: Array<['GET' | 'POST' | 'PUT' | 'DELETE', string, number]> = [
    ['GET', '/api/localai/nodes/ai-n3/publish', 200],
    ['POST', '/api/localai/nodes/ai-n3/publish', 409], // no key stored in this test: the permission passes, the blocker answers
    ['POST', '/api/localai/nodes/ai-n3/unpublish', 409],
    ['GET', '/api/localai/vps/route53', 200],
    ['DELETE', '/api/localai/vps/route53', 200],
  ];

  describe('permissions (lab:admin on every route)', () => {
    it.each(ROUTES)('%s %s: 401 without a session', async (method, url) => {
      expect((await call(method, url)).statusCode).toBe(401);
    });
    it.each(ROUTES)('%s %s: 403 for a viewer and a contributor', async (method, url) => {
      for (const role of ['viewer', 'contributor'] as const) expect((await call(method, url, role)).statusCode).toBe(403);
      expect(enqueue).not.toHaveBeenCalled();
    });
    it('PUT /api/localai/vps/route53: 401 and 403', async () => {
      expect((await call('PUT', '/api/localai/vps/route53', undefined, fakeKey())).statusCode).toBe(401);
      expect((await call('PUT', '/api/localai/vps/route53', 'contributor', fakeKey())).statusCode).toBe(403);
    });
    it.each(ROUTES)('%s %s: an admin passes the guard', async (method, url, expected) => {
      expect((await call(method, url, 'admin')).statusCode).toBe(expected);
    });
  });

  describe('the Route53 key is write-only', () => {
    it('PUT stores it; no response ever contains it, only a masked hint', async () => {
      const key = fakeKey();
      const put = await call('PUT', '/api/localai/vps/route53', 'admin', key);
      expect(put.statusCode).toBe(200);
      const got = await call('GET', '/api/localai/vps/route53', 'admin');
      const status = await call('GET', '/api/localai/nodes/ai-n3/publish', 'admin');
      for (const response of [put, got, status]) for (const secret of [key.accessKeyId, key.secretAccessKey, key.hostedZoneId]) expect(response.body).not.toContain(secret);
      expect((got.json() as { data: { configured: boolean; accessKeyHint: string } }).data).toMatchObject({ configured: true, accessKeyHint: expect.stringMatching(/^\*+/) });
      expect(got.headers['cache-control']).toContain('no-store');
    });

    it('DELETE removes it', async () => {
      await call('PUT', '/api/localai/vps/route53', 'admin', fakeKey());
      const removed = await call('DELETE', '/api/localai/vps/route53', 'admin');
      expect((removed.json() as { data: { configured: boolean } }).data.configured).toBe(false);
    });

    it.each([[{ accessKeyId: 'nope', secretAccessKey: 'x', hostedZoneId: 'y' }], [{ ...fakeKey(), extra: true }], [{}]])('PUT refuses %j with 400 and does not echo it', async (body) => {
      const response = await call('PUT', '/api/localai/vps/route53', 'admin', body);
      expect(response.statusCode).toBe(400);
      expect(credentials.rows.size).toBe(0);
    });
  });

  describe('publish and unpublish', () => {
    const store = () => call('PUT', '/api/localai/vps/route53', 'admin', fakeKey());

    it('202 and exactly { nodeId } in the job, nothing else', async () => {
      await store();
      const response = await call('POST', '/api/localai/nodes/ai-n3/publish', 'admin');
      expect(response.statusCode).toBe(202);
      expect((response.json() as { data: { jobId: string } }).data.jobId).toBe('job-77');
      expect(enqueue).toHaveBeenCalledTimes(1);
      expect(enqueue.mock.calls[0]![0]).toEqual({ type: JOB_VPS_PUBLISH, reason: 'upload', subjectType: 'localai_node', subjectId: 'ai-n3', payload: { nodeId: 'ai-n3' }, orgId: null });
      expect(JSON.stringify(enqueue.mock.calls)).not.toContain(tail.tailnetIp);
      await call('POST', '/api/localai/nodes/ai-n3/unpublish', 'admin');
      expect(enqueue.mock.calls[1]![0]).toMatchObject({ type: JOB_VPS_UNPUBLISH, payload: { nodeId: 'ai-n3' } });
    });

    it('409 with the blocker code when the key is missing, and nothing is queued', async () => {
      const response = await call('POST', '/api/localai/nodes/ai-n3/publish', 'admin');
      expect(response.statusCode).toBe(409);
      expect(response.body).toContain('route53_not_configured');
      expect(enqueue).not.toHaveBeenCalled();
    });

    it('404 for an unknown node and for an id outside the lab’s shape, without a query', async () => {
      await store();
      expect((await call('POST', '/api/localai/nodes/ai-n9/publish', 'admin')).statusCode).toBe(404);
      prismaMock.localaiNode.findUnique.mockClear();
      expect((await call('POST', '/api/localai/nodes/..%2Fetc/publish', 'admin')).statusCode).toBe(404);
      expect((await call('GET', '/api/localai/nodes/ai-n03/publish', 'admin')).statusCode).toBe(404);
      expect(prismaMock.localaiNode.findUnique).not.toHaveBeenCalled();
    });

    it('the status response never contains the tailnet address or name', async () => {
      await store();
      const response = await call('GET', '/api/localai/nodes/ai-n3/publish', 'admin');
      expect(response.statusCode).toBe(200);
      expect(response.body).not.toContain(tail.tailnetIp);
      expect(response.body).not.toContain(tail.tailnetMachine);
      expect((response.json() as { data: { host: string } }).data.host).toBe('ai-n3-staging.marin.cr');
    });
  });
});
