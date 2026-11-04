// The catalog routes over HTTP, on the whole AppModule over a mocked database (no PostgreSQL): the real guards and
// the real seeded grants decide who may call what; the real controllers, Zod schemas and error format answer. The
// state machine is `catalog.service.spec.ts`; the same flows against real SQL and the real `aictl` are
// `catalog.db.spec.ts`.
import fastifyCookie from '@fastify/cookie';
import { GUARDS_METADATA, METHOD_METADATA, PATH_METADATA } from '@nestjs/common/constants';
import { DiscoveryService } from '@nestjs/core';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { JwtAuthGuard } from '@marinoscar/platform-api/identity';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { toCatalogFiles, bundleSha256 } from '../../src/localai/catalog/bundle-hash';
import { LocalaiNodeBindingGuard, nodeTokenDigest } from '../../src/localai/node-link/binding.guard';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';

const WORKER = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa5';
const CREDENTIAL = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc5';
const NODE_TOKEN = `nod_${'5'.repeat(64)}`;

const files = toCatalogFiles([
  { path: 'CATALOG_VERSION', content: '2026.11.03-1\n' },
  { path: 'models.yaml', content: 'schema_version: 1\n' },
  { path: 'profiles/small-8gb.yaml', content: 'id: small-8gb\n' },
]);
const row = (version: string, status: string, over: Record<string, unknown> = {}) => ({
  id: `row-${version}`,
  version,
  source: 'github_release',
  sourceRef: `catalog-${version}`,
  status,
  files,
  bundleSha256: bundleSha256(files),
  lintResult: status === 'valid' || status === 'approved' ? { valid: true, problems: [] } : null,
  lintedAt: new Date('2026-11-03T08:00:00Z'),
  notes: null,
  createdById: null,
  approvedById: null,
  approvedAt: status === 'approved' ? new Date('2026-11-03T10:00:00Z') : null,
  createdAt: new Date('2026-11-03T07:00:00Z'),
  ...over,
});

describe('catalog routes over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let moduleRef: Awaited<ReturnType<ReturnType<typeof Test.createTestingModule>['compile']>>;

  beforeAll(async () => {
    moduleRef = await Test.createTestingModule({ imports: [AppModule] })
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
    resetPrismaMock();
    installBaseMocks();
    prismaMock.job.create.mockImplementation(async ({ data }: { data: Record<string, unknown> }) => ({ id: 'job-1', ...data }));
    prismaMock.auditEvent.create.mockResolvedValue({});
  });

  const token = (role: FixtureRole) => registerMockUser(jwt, role).accessToken;
  const call = (method: 'GET' | 'POST', url: string, bearer?: string, payload?: unknown) =>
    app.inject({ method, url: `/api/localai/catalog${url}`, headers: bearer ? { authorization: `Bearer ${bearer}` } : {}, ...(payload === undefined ? {} : { payload: payload as object }) });

  describe('permissions', () => {
    const READ = [{ method: 'GET' as const, url: '/versions' }, { method: 'GET' as const, url: '/versions/2026.11.03-1' }];
    const ADMIN = [
      { method: 'POST' as const, url: '/import', payload: { tag: 'catalog-2026.11.04-1' } },
      { method: 'POST' as const, url: '/versions', payload: { version: '2026.11.04-1', files: [{ path: 'models.yaml', content: 'x' }] } },
      { method: 'POST' as const, url: '/versions/2026.11.03-1/lint', payload: {} },
      { method: 'POST' as const, url: '/versions/2026.11.03-1/approve', payload: {} },
    ];

    beforeEach(() => {
      prismaMock.localaiCatalogVersion.findMany.mockResolvedValue([row('2026.11.03-1', 'valid')]);
      prismaMock.localaiCatalogVersion.findUnique.mockImplementation(async ({ where }: { where: { version: string } }) =>
        where.version === '2026.11.03-1' ? row('2026.11.03-1', 'invalid') : null,
      );
    });

    it('answers 401 to everyone without a session', async () => {
      for (const route of [...READ, ...ADMIN]) expect((await call(route.method, route.url)).statusCode).toBe(401);
    });

    it.each(['viewer', 'contributor'] as FixtureRole[])('lets a %s read and gives 403 on every route that changes anything', async (role) => {
      const bearer = token(role);
      for (const route of READ) expect((await call(route.method, route.url, bearer)).statusCode).toBe(200);
      for (const route of ADMIN) expect((await call(route.method, route.url, bearer, route.payload)).statusCode).toBe(403);
      expect(prismaMock.job.create).not.toHaveBeenCalled();
      expect(prismaMock.localaiCatalogVersion.create).not.toHaveBeenCalled();
    });

    it('lets the administrator call all of them', async () => {
      prismaMock.localaiCatalogVersion.create.mockImplementation(async ({ data }: { data: Record<string, unknown> }) => row(String(data.version), 'draft'));
      const bearer = token('admin');
      for (const route of READ) expect((await call(route.method, route.url, bearer)).statusCode).toBe(200);
      expect((await call('POST', '/import', bearer, { tag: 'catalog-2026.11.04-1' })).statusCode).toBe(202);
      expect((await call('POST', '/versions', bearer, ADMIN[1].payload)).statusCode).toBe(201);
      expect((await call('POST', '/versions/2026.11.03-1/lint', bearer, {})).statusCode).toBe(202);
    });
  });

  describe('responses and validation', () => {
    it('lists versions in the { data, meta } envelope without file contents', async () => {
      prismaMock.localaiCatalogVersion.findMany.mockResolvedValue([row('2026.11.02-1', 'superseded'), row('2026.11.03-1', 'approved')]);
      const response = await call('GET', '/versions', token('viewer'));
      const body = response.json() as { data: Array<Record<string, unknown>>; meta: Record<string, unknown> };
      expect(body.data.map((item) => item.version)).toEqual(['2026.11.03-1', '2026.11.02-1']);
      expect(body.data[0]).not.toHaveProperty('files');
      expect(typeof body.meta.timestamp).toBe('string');
    });

    it('returns a version with its files, 404 for an unknown or malformed one, and 400 for a bad diff', async () => {
      prismaMock.localaiCatalogVersion.findUnique.mockImplementation(async ({ where }: { where: { version: string } }) =>
        where.version === '2026.11.03-1' ? row('2026.11.03-1', 'valid') : null,
      );
      prismaMock.localaiCatalogVersion.findFirst.mockResolvedValue(null);
      const bearer = token('viewer');
      const found = (await call('GET', '/versions/2026.11.03-1?diff=approved', bearer)).json() as { data: { files: unknown[]; diff: { against: unknown } } };
      expect(found.data.files).toHaveLength(3);
      expect(found.data.diff.against).toBeNull();
      expect((await call('GET', '/versions/2026.11.09-1', bearer)).statusCode).toBe(404);
      expect((await call('GET', '/versions/..%2Fetc', bearer)).statusCode).toBe(404);
      expect((await call('GET', '/versions/2026.11.03-1?diff=other', bearer)).statusCode).toBe(400);
    });

    it('rejects a body that is not valid with 400 and the Zod paths', async () => {
      const bearer = token('admin');
      const bad = await call('POST', '/import', bearer, { tag: 'catalog-stable' });
      expect(bad.statusCode).toBe(400);
      expect(JSON.stringify(bad.json())).toContain('tag');
      expect((await call('POST', '/import', bearer, { tag: 'catalog-2026.11.04-1', extra: 1 })).statusCode).toBe(400);
      expect((await call('POST', '/versions', bearer, { version: 'nope', files: [] })).statusCode).toBe(400);
      expect((await call('POST', '/versions/2026.11.03-1/approve', bearer, { notifyNodes: 'yes' })).statusCode).toBe(400);
    });

    it('answers 409 version_exists to an import of a version we already have', async () => {
      prismaMock.localaiCatalogVersion.findUnique.mockResolvedValue({ id: 'x' });
      const response = await call('POST', '/import', token('admin'), { tag: 'catalog-2026.11.03-1' });
      expect(response.statusCode).toBe(409);
      expect((response.json() as { details: unknown }).details).toMatchObject({ reason: 'version_exists' });
    });

    it('answers 400 / 404 / 409 for editor drafts, lints and approvals that the state does not allow', async () => {
      const bearer = token('admin');
      prismaMock.localaiCatalogVersion.findMany.mockResolvedValue([{ version: '2026.11.03-1' }]);
      const notGreater = await call('POST', '/versions', bearer, { version: '2026.11.03-1', files: [{ path: 'models.yaml', content: 'x' }] });
      expect(notGreater.statusCode).toBe(400);
      expect((notGreater.json() as { details: unknown }).details).toMatchObject({ reason: 'version_not_greater' });
      const badPath = await call('POST', '/versions', bearer, { version: '2026.11.04-1', files: [{ path: 'README.md', content: 'x' }] });
      expect(badPath.statusCode).toBe(400);

      prismaMock.localaiCatalogVersion.findUnique.mockImplementation(async ({ where }: { where: { version: string } }) =>
        where.version === '2026.11.03-1' ? row('2026.11.03-1', 'draft') : null,
      );
      const approve = await call('POST', '/versions/2026.11.03-1/approve', bearer, {});
      expect(approve.statusCode).toBe(409);
      expect((approve.json() as { details: unknown }).details).toMatchObject({ reason: 'not_valid', status: 'draft' });
      expect((await call('POST', '/versions/2026.12.01-1/lint', bearer, {})).statusCode).toBe(404);
    });
  });

  describe('GET /api/nodes/:id/localai/catalog', () => {
    async function arrangeNode(over: { catalogPin?: string | null } = {}) {
      const owner = registerMockUser(jwt, 'admin');
      const user = await prismaMock.user.findUnique({ where: { id: owner.id } });
      prismaMock.nodeCredential.findUnique.mockResolvedValue({
        id: CREDENTIAL,
        userId: owner.id,
        name: 'ai-n3 credential',
        tokenHash: nodeTokenDigest(NODE_TOKEN),
        tokenPrefix: NODE_TOKEN.slice(0, 8),
        expiresAt: null,
        revokedAt: null,
        user,
      });
      prismaMock.nodeCredential.update.mockResolvedValue({});
      prismaMock.workerNode.findUnique.mockImplementation(async ({ where }: { where: { id: string } }) =>
        where.id === WORKER ? { id: WORKER, name: 'ai-n3', createdById: owner.id, cliVersion: 'aiagent/0.1.0' } : null,
      );
      prismaMock.localaiNode.findUnique.mockResolvedValue({
        id: 'row-ai-n3',
        nodeId: 'ai-n3',
        displayName: 'Node ai-n3',
        phase: 'enrolled',
        nodeCredentialId: CREDENTIAL,
        workerNodeId: WORKER,
        catalogPin: over.catalogPin ?? null,
      });
    }
    const getCatalog = (headers: Record<string, string> = {}) =>
      app.inject({ method: 'GET', url: `/api/nodes/${WORKER}/localai/catalog`, headers: { authorization: `Bearer ${NODE_TOKEN}`, ...headers } });

    it('carries the binding guard after JwtAuthGuard, like every route under /nodes/:id/localai', () => {
      const found: string[] = [];
      for (const wrapper of app.get(DiscoveryService).getControllers()) {
        const type = wrapper.metatype;
        if (!type) continue;
        const base = `/${String(Reflect.getMetadata(PATH_METADATA, type) ?? '').replace(/^\//, '')}`;
        const classGuards: unknown[] = Reflect.getMetadata(GUARDS_METADATA, type) ?? [];
        for (const name of Object.getOwnPropertyNames(type.prototype)) {
          const handler = type.prototype[name];
          if (typeof handler !== 'function' || Reflect.getMetadata(METHOD_METADATA, handler) === undefined) continue;
          const full = `${base}/${String(Reflect.getMetadata(PATH_METADATA, handler) ?? '').replace(/^\//, '')}`;
          if (!/^\/nodes\/:[A-Za-z]+\/localai(\/|$)/.test(full)) continue;
          const guards = [...classGuards, ...(Reflect.getMetadata(GUARDS_METADATA, handler) ?? [])];
          expect({ full, ordered: guards.indexOf(JwtAuthGuard) >= 0 && guards.indexOf(JwtAuthGuard) < guards.indexOf(LocalaiNodeBindingGuard) }).toEqual({ full, ordered: true });
          found.push(full);
        }
      }
      expect(found).toContain('/nodes/:id/localai/catalog');
    });

    it('is refused to a browser session and to no credential at all', async () => {
      await arrangeNode();
      expect((await app.inject({ method: 'GET', url: `/api/nodes/${WORKER}/localai/catalog` })).statusCode).toBe(401);
      const session = await app.inject({ method: 'GET', url: `/api/nodes/${WORKER}/localai/catalog`, headers: { authorization: `Bearer ${token('admin')}` } });
      expect(session.statusCode).toBe(403);
      expect(prismaMock.localaiCatalogVersion.findFirst).not.toHaveBeenCalled();
    });

    it('answers 404 LAI-CLD-013 no_approved_catalog while nothing is approved', async () => {
      await arrangeNode();
      prismaMock.localaiCatalogVersion.findFirst.mockResolvedValue(null);
      const response = await getCatalog();
      expect(response.statusCode).toBe(404);
      expect((response.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-013', reason: 'no_approved_catalog' });
    });

    it('answers 200 with the bundle and a strong ETag, then 304 with an empty body for that ETag', async () => {
      await arrangeNode();
      prismaMock.localaiCatalogVersion.findFirst.mockResolvedValue(row('2026.11.03-1', 'approved'));
      const ok = await getCatalog();
      expect(ok.statusCode).toBe(200);
      const etag = `"${bundleSha256(files)}"`;
      expect(ok.headers.etag).toBe(etag);
      expect(ok.headers['cache-control']).toBe('private, no-cache');
      const body = ok.json() as { data: { version: string; bundleSha256: string; approvedAt: string; files: Array<{ path: string; sha256: string; content: string }> } };
      expect(Object.keys(body.data)).toEqual(['version', 'bundleSha256', 'approvedAt', 'files']);
      expect(body.data).toMatchObject({ version: '2026.11.03-1', bundleSha256: bundleSha256(files), approvedAt: '2026-11-03T10:00:00.000Z' });
      expect(body.data.files.map((file) => file.path)).toEqual(['CATALOG_VERSION', 'models.yaml', 'profiles/small-8gb.yaml']);
      expect(body.data.files[0]).toEqual({ path: 'CATALOG_VERSION', sha256: files[0].sha256, content: '2026.11.03-1\n' });

      const notModified = await getCatalog({ 'if-none-match': etag });
      expect(notModified.statusCode).toBe(304);
      expect(notModified.body).toBe('');
      expect(notModified.headers.etag).toBe(etag);
      expect((await getCatalog({ 'if-none-match': `W/${etag}` })).statusCode).toBe(200);
      expect((await getCatalog({ 'if-none-match': `"${'0'.repeat(64)}"` })).statusCode).toBe(200);
    });

    it('serves the pinned version regardless of status, 404 reason catalog_pin_not_found for a missing pin', async () => {
      await arrangeNode({ catalogPin: '2026.11.04-1' });
      prismaMock.localaiCatalogVersion.findUnique.mockImplementation(async ({ where }: { where: { version: string } }) =>
        where.version === '2026.11.04-1' ? row('2026.11.04-1', 'draft') : null,
      );
      const pinned = await getCatalog();
      expect(pinned.statusCode).toBe(200);
      expect((pinned.json() as { data: { version: string; approvedAt: unknown } }).data).toMatchObject({ version: '2026.11.04-1', approvedAt: null });
      expect(prismaMock.localaiCatalogVersion.findFirst).not.toHaveBeenCalled();

      await arrangeNode({ catalogPin: '2026.01.01-1' });
      const missing = await getCatalog();
      expect(missing.statusCode).toBe(404);
      expect((missing.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-013', reason: 'catalog_pin_not_found' });
    });
  });
});
