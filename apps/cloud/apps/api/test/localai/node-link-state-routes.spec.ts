// `POST /api/nodes/:id/localai/state` over HTTP, on the whole AppModule over a mocked database (no PostgreSQL): the
// real JwtAuthGuard, NodeCredentialService, binding guard, StateService, event bus and audit sink, so the status
// codes, the error body format, the 256 KiB route limit and the response envelope are proven end to end. The same
// flow against real SQL is `node-link-state.db.spec.ts`.
import fastifyCookie from '@fastify/cookie';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { LocalaiStateResponseSchema, LOCALAI_STATE_BODY_LIMIT_BYTES } from '../../src/localai/contracts/state.schema';
import { nodeTokenDigest } from '../../src/localai/node-link/binding.guard';
import { registerNodeLinkBodyLimits } from '../../src/localai/node-link/node-link.body-limits';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';
import { ai3, loadFixture, secrets } from './state-fixtures';

const WORKER = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3';
const CREDENTIAL = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc3';
const NODE_ROW = 'dddddddd-dddd-4ddd-8ddd-ddddddddddd3';
// A fixed string with the `nod_` shape; not a real credential.
const NODE_TOKEN = `nod_${'3'.repeat(64)}`;

describe('POST /api/nodes/:id/localai/state over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let published: Array<{ channel: string; payload: unknown }>;
  let unsubscribe: () => void;

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
    // The per-route body limit, exactly as createApp() installs it: before init, when routes register.
    registerNodeLinkBodyLimits(app.getHttpAdapter().getInstance());
    await app.register(fastifyCookie, { secret: 'test-secret' });
    app.setGlobalPrefix('api');
    resetPrismaMock();
    installBaseMocks();
    await app.init();
    await app.getHttpAdapter().getInstance().ready();
    for (const cronJob of app.get(SchedulerRegistry).getCronJobs().values()) void cronJob.stop();
    jwt = moduleRef.get(JwtService);
    const bus = moduleRef.get<EventBus>(EVENT_BUS, { strict: false });
    published = [];
    unsubscribe = bus.subscribe('localai.node.state', (payload) => {
      published.push({ channel: 'localai.node.state', payload });
    });
  });

  afterAll(async () => {
    unsubscribe?.();
    clearMockUsers();
    await app?.close();
  });

  /** An owner, the bound node credential and an enrolled node whose last snapshot is `stored`. */
  async function arrange(stored: { lastState?: unknown; lastStateSeq?: bigint | null; mode?: string | null; models?: unknown[] } = {}) {
    resetPrismaMock();
    installBaseMocks();
    published.length = 0;
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
      where.id === WORKER ? { id: WORKER, name: 'ai-n3', createdById: owner.id, cliVersion: 'aiagent/0.4.0' } : null,
    );
    // `findUnique` serves two callers: the binding guard (a whole row) and the ingestion transaction (a `select`
    // of the stored state it orders against).
    prismaMock.localaiNode.findUnique.mockImplementation(async (args: { select?: unknown }) =>
      args.select
        ? { phase: 'enrolled', mode: stored.mode ?? null, lastState: stored.lastState ?? null, lastStateSeq: stored.lastStateSeq ?? null }
        : {
            id: NODE_ROW,
            nodeId: 'ai-n3',
            displayName: 'Node ai-n3',
            phase: 'enrolled',
            nodeCredentialId: CREDENTIAL,
            workerNodeId: WORKER,
            keyReportedAt: null,
          },
    );
    prismaMock.auditEvent.create.mockResolvedValue({});
    prismaMock.$transaction.mockImplementation(async (fn: (tx: unknown) => unknown) => fn(prismaMock));
    prismaMock.localaiNodeModel.findMany.mockResolvedValue(stored.models ?? []);
    prismaMock.localaiNode.update.mockResolvedValue({});
    // The ingestion transaction's row lock is an updateMany.
    prismaMock.localaiNode.updateMany.mockResolvedValue({ count: 1 });
    return { owner, sessionToken: owner.accessToken };
  }

  const post = (payload: unknown, authorization = `Bearer ${NODE_TOKEN}`, workerId = WORKER) =>
    app.inject({
      method: 'POST',
      url: `/api/nodes/${workerId}/localai/state`,
      headers: { authorization, 'content-type': 'application/json' },
      payload: typeof payload === 'string' ? payload : JSON.stringify(payload),
    });

  it('accepts the ai-n3 fixture: 200 { data: { accepted: true, seq } } in the shared response fixture shape', async () => {
    await arrange();
    const response = await post(ai3());
    expect(response.statusCode).toBe(200);
    const fixture = loadFixture<{ data: { accepted: boolean; seq: number }; meta: Record<string, unknown> }>('state.response.json');
    const body = response.json() as { data: unknown; meta: Record<string, unknown> };
    expect(Object.keys(body)).toEqual(Object.keys(fixture));
    expect(Object.keys(body.meta)).toEqual(Object.keys(fixture.meta));
    expect(LocalaiStateResponseSchema.safeParse(body.data).success).toBe(true);
    expect(body.data).toEqual(fixture.data);
    expect(fixture.data.seq).toBe(ai3().seq);
    // The snapshot was stored and one event published (the in-process bus delivers synchronously enough to await).
    expect(prismaMock.localaiNode.update).toHaveBeenCalledTimes(1);
    expect(prismaMock.localaiNodeModel.createMany).toHaveBeenCalledTimes(1);
    await new Promise((resolve) => setImmediate(resolve));
    expect(published).toEqual([{ channel: 'localai.node.state', payload: { nodeId: 'ai-n3', modelsChanged: true, modeChanged: false } }]);
  });

  it('audits a mode change with the credential owner as actor', async () => {
    const { owner } = await arrange({ mode: 'online', lastState: { boot_id: 'earlier-boot' }, lastStateSeq: 3n });
    const snapshot = ai3();
    snapshot.mode = 'offline';
    expect((await post(snapshot)).statusCode).toBe(200);
    const rows = prismaMock.auditEvent.create.mock.calls.map((call: [{ data: Record<string, unknown> }]) => call[0].data);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ action: 'localai.node.mode_changed', actorUserId: owner.id, targetType: 'localai_node', targetId: 'ai-n3', meta: { from: 'online', to: 'offline' } });
    await new Promise((resolve) => setImmediate(resolve));
    expect(published).toHaveLength(1);
    expect(published[0]!.payload).toMatchObject({ modeChanged: true });
  });

  it('401 without a credential, 403 node_credential_required for a browser session', async () => {
    const { sessionToken } = await arrange();
    expect((await app.inject({ method: 'POST', url: `/api/nodes/${WORKER}/localai/state`, payload: ai3() })).statusCode).toBe(401);
    const response = await post(ai3(), `Bearer ${sessionToken}`);
    expect(response.statusCode).toBe(403);
    expect(response.json()).toMatchObject({ details: { code: 'LAI-CLD-004', reason: 'node_credential_required' } });
    expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
  });

  it('400 with Zod issues in details for a schema failure (201 models), writing nothing', async () => {
    await arrange();
    const snapshot = ai3();
    snapshot.models = Array.from({ length: 201 }, (_, index) => ({ ...snapshot.models[0]!, id: `model-${index}` }));
    const response = await post(snapshot);
    expect(response.statusCode).toBe(400);
    expect(response.json()).toMatchObject({ details: { code: 'LAI-CLD-008', reason: 'schema', issues: [{ path: 'models', code: 'too_big' }] } });
    expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
    expect(published).toHaveLength(0);
  });

  it('400 for an unknown key anywhere', async () => {
    await arrange();
    const snapshot = ai3() as unknown as { hardware: { power: Record<string, unknown> } };
    snapshot.hardware.power.charge_cycles = 12;
    const response = await post(snapshot);
    expect(response.statusCode).toBe(400);
    expect(response.json()).toMatchObject({ details: { reason: 'schema', issues: [{ path: 'hardware.power', code: 'unrecognized_keys' }] } });
  });

  it('400 LAI-CLD-004 when node_id is not the bound node', async () => {
    await arrange();
    const snapshot = ai3();
    snapshot.node_id = 'ai-n4';
    const response = await post(snapshot);
    expect(response.statusCode).toBe(400);
    expect(response.json()).toMatchObject({ details: { code: 'LAI-CLD-004' } });
  });

  it('400 secret_like_value for a tailnet-range IPv4 string, and the response never echoes it', async () => {
    await arrange();
    const snapshot = ai3();
    const ip = secrets.tailnetIp();
    snapshot.alerts[0]!.title = `Cannot reach ${ip}`;
    const response = await post(snapshot);
    expect(response.statusCode).toBe(400);
    expect(response.json()).toMatchObject({ details: { code: 'LAI-CLD-008', reason: 'secret_like_value', path: 'alerts[0].title' } });
    expect(response.body).not.toContain(ip);
    expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
  });

  it('400 secret_like_value for "lat_" + 43 characters', async () => {
    await arrange();
    const snapshot = ai3();
    const token = secrets.labToken();
    snapshot.models[0]!.loadable_reason = token;
    const response = await post(snapshot);
    expect(response.statusCode).toBe(400);
    expect(response.json()).toMatchObject({ details: { reason: 'secret_like_value' } });
    expect(response.body).not.toContain(token);
  });

  it('409 stale_seq for the same boot and a seq that is not newer; a new boot_id with seq 0 is accepted', async () => {
    const snapshot = ai3();
    await arrange({ lastState: { boot_id: snapshot.boot_id }, lastStateSeq: BigInt(snapshot.seq), mode: 'online' });
    const stale = await post(snapshot);
    expect(stale.statusCode).toBe(409);
    expect(stale.json()).toMatchObject({ details: { code: 'LAI-CLD-008', reason: 'stale_seq' } });
    expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();

    const restarted = { ...snapshot, boot_id: '5f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e61', seq: 0 };
    const fresh = await post(restarted);
    expect(fresh.statusCode).toBe(200);
    expect(fresh.json()).toMatchObject({ data: { accepted: true, seq: 0 } });
  });

  it('413 for a body over 256 KiB, before the handler runs', async () => {
    await arrange();
    const padding = 'x'.repeat(LOCALAI_STATE_BODY_LIMIT_BYTES);
    const response = await post(JSON.stringify({ ...ai3(), padding }));
    expect(response.statusCode).toBe(413);
    expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
    expect(prismaMock.$transaction).not.toHaveBeenCalled();
  });

  it('a body just under the limit reaches the handler (and is refused as a schema failure, not as too large)', async () => {
    await arrange();
    const base = JSON.stringify({ ...ai3(), padding: '' });
    const padding = 'x'.repeat(LOCALAI_STATE_BODY_LIMIT_BYTES - Buffer.byteLength(base) - 16);
    const response = await post(JSON.stringify({ ...ai3(), padding }));
    expect(response.statusCode).toBe(400);
    expect(response.json()).toMatchObject({ details: { reason: 'schema' } });
  });

  it('a plain-text body is never treated as a snapshot: 400 schema', async () => {
    await arrange();
    const response = await app.inject({
      method: 'POST',
      url: `/api/nodes/${WORKER}/localai/state`,
      headers: { authorization: `Bearer ${NODE_TOKEN}`, 'content-type': 'text/plain' },
      payload: 'hello',
    });
    expect(response.statusCode).toBe(400);
    expect(response.json()).toMatchObject({ details: { reason: 'schema' } });
    expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
  });
});
