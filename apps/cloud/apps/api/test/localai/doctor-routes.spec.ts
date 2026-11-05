// The seven cloud Doctor checks on `GET /api/admin/doctor` (docs/specs/cloud-app.md section 7): the whole AppModule over a
// mocked database (no PostgreSQL), the real `CredentialsService` and cipher, the real Doctor service and controller, and
// a fake `aictl` script. What it proves: the checks are REGISTERED and appear in the report, an administrator is
// required, a broken deployment fails with the right codes, and the request writes nothing.
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import fastifyCookie from '@fastify/cookie';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { JobWorker } from '@marinoscar/platform-api/jobs';

// A throwaway key for the cipher, generated per run and never committed.
process.env.SECRETS_ENCRYPTION_KEY ??= randomBytes(32).toString('base64');

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';
import { expectNoSecretShapes } from './support/doctor-asserts';

const IDS = [
  'localai.credentials',
  'localai.aictl',
  'localai.catalog.approved',
  'localai.nodes.pending',
  'localai.commands.stuck',
  'localai.nodes.keys',
  'localai.settings.nodes',
];

interface Check {
  id: string;
  category: string;
  status: string;
  detail: string;
  remedy: string | null;
  data: Record<string, unknown> | null;
}

describe('Cloud Doctor checks over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let dir: string;
  const savedAictl = process.env.LOCALAI_AICTL_PATH;

  beforeAll(async () => {
    dir = mkdtempSync(join(tmpdir(), 'doctor-routes-spec-'));
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
    rmSync(dir, { recursive: true, force: true });
    if (savedAictl === undefined) delete process.env.LOCALAI_AICTL_PATH;
    else process.env.LOCALAI_AICTL_PATH = savedAictl;
  });

  function fakeAictl(body: string): string {
    const path = join(dir, `aictl-${randomBytes(4).toString('hex')}`);
    writeFileSync(path, `#!/bin/sh\n${body}\n`);
    chmodSync(path, 0o755);
    return path;
  }

  /** A deployment where every check should pass; a test then breaks one thing. */
  function arrangeHealthy() {
    resetPrismaMock();
    installBaseMocks();
    process.env.LOCALAI_AICTL_PATH = fakeAictl(`echo '{"cli":{"version":"0.1.0"},"agent":null}'`);
    prismaMock.credential.findMany.mockResolvedValue([]);
    prismaMock.localaiCatalogVersion.findFirst.mockResolvedValue({ version: '2026.11.02-1' });
    prismaMock.localaiNode.findMany.mockResolvedValue([]);
    prismaMock.localaiCommand.count.mockResolvedValue(0);
    prismaMock.auditEvent.findMany.mockResolvedValue([]);
    prismaMock.systemSettings.findUnique.mockResolvedValue({
      key: 'global',
      version: 1,
      value: { nodes: { staleHeartbeatSeconds: 45, offlineStaleMultiplier: 4, offlineRetentionDays: 3650 } },
    });
  }

  async function report(role: 'admin' | 'contributor' | 'viewer' = 'admin') {
    const user = registerMockUser(jwt, role);
    const response = await app.inject({
      method: 'GET',
      url: '/api/admin/doctor?category=localai&refresh=true',
      headers: { authorization: `Bearer ${user.accessToken}` },
    });
    return response;
  }

  const checksOf = (body: string): Check[] => (JSON.parse(body) as { data: { checks: Check[] } }).data.checks;
  const byId = (checks: Check[], id: string) => checks.find((check) => check.id === id) as Check;

  it('lists all seven checks under the localai category, every one passing on a healthy deployment', async () => {
    arrangeHealthy();
    const response = await report();
    expect(response.statusCode).toBe(200);
    // The gateway's four checks (GW-08) share the category; they are covered by their own test below.
    const checks = checksOf(response.body).filter((check) => !check.id.startsWith('localai.gateway.'));
    expect(checks.map((check) => check.id).sort()).toEqual([...IDS].sort());
    for (const check of checks) {
      expect({ id: check.id, category: check.category, status: check.status }).toEqual({ id: check.id, category: 'localai', status: 'pass' });
    }
    expectNoSecretShapes(response.body);
  });

  it('also lists the four gateway checks (GW-08); with no node in the routing table only routable warns', async () => {
    arrangeHealthy();
    const checks = checksOf((await report()).body).filter((check) => check.id.startsWith('localai.gateway.'));
    expect(checks.map((check) => [check.id, check.status])).toEqual(
      expect.arrayContaining([
        ['localai.gateway.enabled', 'pass'],
        ['localai.gateway.routable', 'warn'],
        ['localai.gateway.keys', 'pass'],
        ['localai.gateway.usage', 'pass'],
      ]),
    );
    expect(checks).toHaveLength(4);
    expect(byId(checks, 'localai.gateway.routable').data).toMatchObject({ code: 'LAI-GW-021' });
  });

  it('needs an administrator', async () => {
    arrangeHealthy();
    expect((await report('viewer')).statusCode).toBe(403);
  });

  it('LOCALAI_AICTL_PATH=/nonexistent makes localai.aictl fail with LAI-CLD-033', async () => {
    arrangeHealthy();
    process.env.LOCALAI_AICTL_PATH = '/nonexistent';
    const check = byId(checksOf((await report()).body), 'localai.aictl');
    expect(check.status).toBe('fail');
    expect(check.data).toMatchObject({ code: 'LAI-CLD-033', reason: 'missing' });
    expect(check.remedy).toContain('laictl deploy update');
  });

  it('a wrong encryption key (a stored key that cannot be decrypted) fails localai.credentials with LAI-CLD-032', async () => {
    arrangeHealthy();
    // A row the real cipher cannot authenticate: what a changed SECRETS_ENCRYPTION_KEY looks like.
    prismaMock.credential.findMany.mockResolvedValue([{ name: 'ai-n1', purpose: 'localai_node_llama_key' }]);
    prismaMock.credential.findUnique.mockResolvedValue({ secret: randomBytes(48).toString('base64') });
    const response = await report();
    const check = byId(checksOf(response.body), 'localai.credentials');
    expect(check.status).toBe('fail');
    expect(check.data).toMatchObject({ code: 'LAI-CLD-032' });
    expect(check.remedy).toContain('SECRETS_ENCRYPTION_KEY');
    expectNoSecretShapes(response.body);
  });

  it('reports the platform default fleet policy, a missing approved catalog and a pending node', async () => {
    arrangeHealthy();
    prismaMock.systemSettings.findUnique.mockResolvedValue(null); // the platform defaults: 30 days, 90 s
    prismaMock.localaiCatalogVersion.findFirst.mockResolvedValue(null);
    prismaMock.localaiNode.findMany.mockImplementation(async ({ where }: { where: { phase: string } }) =>
      where.phase === 'pending' ? [{ nodeId: 'ai-n4', createdAt: new Date(Date.now() - 9 * 86_400_000) }] : [],
    );
    const checks = checksOf((await report()).body);
    expect(byId(checks, 'localai.settings.nodes').status).toBe('warn');
    expect(byId(checks, 'localai.catalog.approved').status).toBe('warn');
    const pending = byId(checks, 'localai.nodes.pending');
    expect(pending.status).toBe('warn');
    expect(pending.detail).toContain('Retire or finish ai-n4');
    expect(pending.data).toMatchObject({ code: 'LAI-CLD-031' });
  });

  it('writes nothing', async () => {
    arrangeHealthy();
    await report();
    for (const table of ['credential', 'localaiNode', 'localaiCommand', 'localaiCatalogVersion', 'auditEvent', 'systemSettings']) {
      for (const method of ['create', 'createMany', 'update', 'updateMany', 'upsert', 'delete', 'deleteMany']) {
        expect({ table, method, calls: prismaMock[table][method].mock.calls.length }).toEqual({ table, method, calls: 0 });
      }
    }
  });
});
