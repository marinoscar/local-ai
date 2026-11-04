// The node-facing Local AI routes over HTTP, on the whole AppModule over a mocked database (no PostgreSQL): the real
// `JwtAuthGuard`, the real `NodeCredentialService`, the real binding guard and the real audit sink, so the guard
// ORDER, the error body format and the config envelope are proven end to end. The same flow against real SQL is
// `node-link.db.spec.ts`.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import fastifyCookie from '@fastify/cookie';
import { GUARDS_METADATA, METHOD_METADATA, PATH_METADATA } from '@nestjs/common/constants';
import { JwtService } from '@nestjs/jwt';
import { DiscoveryService } from '@nestjs/core';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { JwtAuthGuard } from '@marinoscar/platform-api/identity';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { LocalaiNodeBindingGuard, nodeTokenDigest } from '../../src/localai/node-link/binding.guard';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';

const fixture = JSON.parse(readFileSync(join(__dirname, '../fixtures/localai/config.response.json'), 'utf8')) as {
  data: Record<string, unknown>;
  meta: Record<string, unknown>;
};

const WORKER = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3';
const OTHER_WORKER = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa4';
const CREDENTIAL = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc3';
const OTHER_CREDENTIAL = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc4';
// A fixed string with the `nod_` shape; not a real credential.
const NODE_TOKEN = `nod_${'3'.repeat(64)}`;

describe('Local AI node routes over HTTP', () => {
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

  /** An owner, the node credential they hold and the rows behind it, as the database would answer. */
  async function arrange(options: { phase?: 'pending' | 'enrolled'; boundCredential?: string; workerOwnerId?: string } = {}) {
    resetPrismaMock();
    installBaseMocks();
    const owner = registerMockUser(jwt, 'admin');
    const user = await prismaMock.user.findUnique({ where: { id: owner.id } });
    const credentialRow = {
      id: CREDENTIAL,
      userId: owner.id,
      name: 'ai-n3 credential',
      tokenHash: nodeTokenDigest(NODE_TOKEN),
      tokenPrefix: NODE_TOKEN.slice(0, 8),
      expiresAt: null,
      revokedAt: null,
    };
    prismaMock.nodeCredential.findUnique.mockResolvedValue({ ...credentialRow, user });
    prismaMock.nodeCredential.update.mockResolvedValue({});
    prismaMock.workerNode.findUnique.mockImplementation(async ({ where }: { where: { id: string } }) =>
      where.id === WORKER ? { id: WORKER, name: 'ai-n3', createdById: options.workerOwnerId ?? owner.id, cliVersion: 'aiagent/0.1.0' } : null,
    );
    const nodeRow = {
      id: 'row-ai-n3',
      nodeId: 'ai-n3',
      displayName: 'Node ai-n3',
      phase: options.phase ?? 'pending',
      nodeCredentialId: options.boundCredential ?? CREDENTIAL,
      workerNodeId: null,
      agentVersion: null,
      keyReportedAt: new Date('2026-11-01T00:00:00Z'),
    };
    prismaMock.localaiNode.findUnique.mockImplementation(async ({ where }: { where: { nodeId?: string; id?: string } }) =>
      where.nodeId === 'ai-n3' || where.id === 'row-ai-n3' ? { ...nodeRow } : null,
    );
    prismaMock.localaiNode.updateMany.mockResolvedValue({ count: 1 });
    prismaMock.auditEvent.create.mockResolvedValue({});
    return { owner, sessionToken: owner.accessToken };
  }

  const getConfig = (workerId: string, authorization?: string) =>
    app.inject({ method: 'GET', url: `/api/nodes/${workerId}/localai/config`, headers: authorization ? { authorization } : {} });

  it('runs the binding guard after JwtAuthGuard on every route under /nodes/:id/localai', () => {
    const controllers = app.get(DiscoveryService).getControllers();
    const checked: string[] = [];
    for (const wrapper of controllers) {
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
        const jwtAt = guards.indexOf(JwtAuthGuard);
        const bindingAt = guards.indexOf(LocalaiNodeBindingGuard);
        expect({ route: full, authenticated: jwtAt >= 0, bound: bindingAt >= 0, ordered: jwtAt < bindingAt }).toEqual({
          route: full,
          authenticated: true,
          bound: true,
          ordered: true,
        });
        checked.push(full);
      }
    }
    // The tripwire sees the handshake route (and, as later issues land, theirs).
    expect(checked).toContain('/nodes/:id/localai/config');
  });

  it('answers 401 without a credential', async () => {
    await arrange();
    expect((await getConfig(WORKER)).statusCode).toBe(401);
  });

  it('answers 403 node_credential_required to a browser session, in EAB error format with our code in details', async () => {
    const { sessionToken } = await arrange();
    const response = await getConfig(WORKER, `Bearer ${sessionToken}`);
    expect(response.statusCode).toBe(403);
    expect(response.json()).toMatchObject({
      statusCode: 403,
      message: expect.any(String),
      details: { code: 'LAI-CLD-004', reason: 'node_credential_required' },
    });
    expect(prismaMock.workerNode.findUnique).not.toHaveBeenCalled();
    expect(prismaMock.localaiNode.updateMany).not.toHaveBeenCalled();
  });

  it('answers the handshake to the bound node credential: 200 in the fixture shape, and enrolls the node', async () => {
    await arrange();
    const response = await getConfig(WORKER, `Bearer ${NODE_TOKEN}`);
    expect(response.statusCode).toBe(200);
    const body = response.json() as { data: Record<string, unknown>; meta: Record<string, unknown> };
    expect(Object.keys(body)).toEqual(Object.keys(fixture));
    expect(Object.keys(body.data)).toEqual(Object.keys(fixture.data));
    expect(Object.keys(body.meta)).toEqual(Object.keys(fixture.meta));
    // Everything but the clock and the node id is the fixture; the clock is a current ISO timestamp.
    expect({ ...body.data, serverTime: fixture.data.serverTime }).toEqual(fixture.data);
    expect(Math.abs(Date.parse(String(body.data.serverTime)) - Date.now())).toBeLessThan(60_000);
    // First bound call: the pending node was enrolled and audited once.
    expect(prismaMock.localaiNode.updateMany).toHaveBeenCalledTimes(1);
    const audited = prismaMock.auditEvent.create.mock.calls.map((call: [{ data: { action: string } }]) => call[0].data.action);
    expect(audited).toEqual(['localai.node.enrolled']);
  });

  it('an enrolled node on its own worker id writes nothing', async () => {
    await arrange({ phase: 'enrolled' });
    prismaMock.localaiNode.findUnique.mockImplementation(async () => ({
      id: 'row-ai-n3',
      nodeId: 'ai-n3',
      displayName: 'Node ai-n3',
      phase: 'enrolled',
      nodeCredentialId: CREDENTIAL,
      workerNodeId: WORKER,
      keyReportedAt: null,
    }));
    const response = await getConfig(WORKER, `Bearer ${NODE_TOKEN}`);
    expect(response.statusCode).toBe(200);
    expect((response.json() as { data: { keyReported: boolean } }).data.keyReported).toBe(false);
    expect(prismaMock.localaiNode.updateMany).not.toHaveBeenCalled();
    expect(prismaMock.auditEvent.create).not.toHaveBeenCalled();
  });

  it('refuses a credential bound to another node with 403 LAI-CLD-004 and one binding_refused audit row', async () => {
    await arrange({ phase: 'enrolled', boundCredential: OTHER_CREDENTIAL });
    const response = await getConfig(WORKER, `Bearer ${NODE_TOKEN}`);
    expect(response.statusCode).toBe(403);
    expect((response.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-004', reason: 'credential_bound_to_other_node' });
    expect(response.body).not.toContain(NODE_TOKEN);
    const rows = prismaMock.auditEvent.create.mock.calls.map((call: [{ data: Record<string, unknown> }]) => call[0].data);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ action: 'localai.node.binding_refused', targetType: 'localai_node', targetId: 'ai-n3' });
    expect(rows[0].meta).toEqual({ credentialPrefix: NODE_TOKEN.slice(0, 8), claimedNodeId: 'ai-n3' });
  });

  it('answers 404 for an unknown worker node and 403 for another owner', async () => {
    await arrange();
    expect((await getConfig(OTHER_WORKER, `Bearer ${NODE_TOKEN}`)).statusCode).toBe(404);
    await arrange({ workerOwnerId: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd' });
    expect((await getConfig(WORKER, `Bearer ${NODE_TOKEN}`)).statusCode).toBe(403);
  });
});
