// The node-facing command routes over HTTP, on the whole AppModule over a mocked database (no PostgreSQL): the real
// `JwtAuthGuard`, the real `NodeCredentialService`, the real binding guard, the real controllers and services, and the
// in-memory command table of `support/fake-command-store.ts` behind `prisma.localaiCommand`. This proves the guards,
// the wire shapes (against the shared fixtures), the status codes and the error bodies. The SQL is
// `commands.db.spec.ts`.
import { readFileSync } from 'node:fs';
import { EventEmitter } from 'node:events';
import { join } from 'node:path';
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
import { CommandClaimRepository } from '../../src/localai/commands/claim.sql';
import { LOCALAI_CLOCK } from '../../src/localai/commands/command-events';
import { CommandsService } from '../../src/localai/commands/commands.service';
import { NodeLinkCommandsController } from '../../src/localai/node-link/commands.controller';
import { LocalaiNodeBindingGuard, nodeTokenDigest } from '../../src/localai/node-link/binding.guard';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';
import { FakeBus, FakeClock, FakeCommandStore, StoreClaimRepository } from './support/fake-command-store';

const fixture = (name: string) => JSON.parse(readFileSync(join(__dirname, '../fixtures/localai', name), 'utf8')) as Record<string, any>;

const WORKER = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3';
const CREDENTIAL = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc3';
const NODE_ROW = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee3';
const OTHER_NODE_ROW = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee4';
const NODE_TOKEN = `nod_${'3'.repeat(64)}`;
const MODEL = 'qwen3.6-35b-a3b-q4km';

const clock = new FakeClock(new Date('2026-11-02T15:04:00.000Z'));
const store = new FakeCommandStore(clock);

describe('Local AI command routes over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let bus: FakeBus;

  beforeAll(async () => {
    bus = new FakeBus();
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
      .overrideProvider(CommandClaimRepository)
      .useValue(new StoreClaimRepository(store))
      .overrideProvider(LOCALAI_CLOCK)
      .useValue(clock)
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
    void bus;
  });

  afterAll(async () => {
    clearMockUsers();
    await app?.close();
  });

  beforeEach(() => {
    store.rows.length = 0;
    store.writes.length = 0;
    store.nodeUpdates.length = 0;
    clock.set(new Date('2026-11-02T15:04:00.000Z'));
  });

  /** An owner, the node credential they hold and the rows behind it, as the database would answer. */
  async function arrange() {
    resetPrismaMock();
    installBaseMocks();
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
    prismaMock.localaiNode.findUnique.mockImplementation(async ({ where }: { where: { nodeId?: string } }) =>
      where.nodeId === 'ai-n3'
        ? { id: NODE_ROW, nodeId: 'ai-n3', displayName: 'Node ai-n3', phase: 'enrolled', nodeCredentialId: CREDENTIAL, workerNodeId: WORKER, keyReportedAt: null }
        : null,
    );
    prismaMock.localaiNode.update.mockImplementation(store.localaiNode.update);
    prismaMock.auditEvent.create.mockResolvedValue({});
    for (const method of ['create', 'findFirst', 'findMany', 'updateMany', 'deleteMany'] as const) {
      prismaMock.localaiCommand[method].mockImplementation((store.localaiCommand as any)[method]);
    }
    return { owner };
  }

  const post = (path: string, payload: unknown, token: string | null = NODE_TOKEN) =>
    app.inject({
      method: 'POST',
      url: `/api/nodes/${WORKER}/localai/commands/${path}`,
      headers: token ? { authorization: `Bearer ${token}` } : {},
      payload: payload as never,
    });

  /** Queues a command for the node row and returns it (what CLOUD-09's UI route will do through the service). */
  async function queue(type: string, params: unknown, nodeId = NODE_ROW) {
    return app.get(CommandsService).enqueueForNode(nodeId, type, params, { userId: null }, 'system');
  }

  const claimBody = () => fixture('claim.request.json');

  it('runs the binding guard after JwtAuthGuard on all four command routes', () => {
    const checked: string[] = [];
    for (const wrapper of app.get(DiscoveryService).getControllers()) {
      const type = wrapper.metatype;
      if (type !== NodeLinkCommandsController) continue;
      const base = `/${String(Reflect.getMetadata(PATH_METADATA, type)).replace(/^\//, '')}`;
      const classGuards: unknown[] = Reflect.getMetadata(GUARDS_METADATA, type) ?? [];
      for (const name of Object.getOwnPropertyNames(type.prototype)) {
        const handler = type.prototype[name];
        if (typeof handler !== 'function' || Reflect.getMetadata(METHOD_METADATA, handler) === undefined) continue;
        const guards = [...classGuards, ...(Reflect.getMetadata(GUARDS_METADATA, handler) ?? [])];
        expect({ jwt: guards.indexOf(JwtAuthGuard) >= 0, binding: guards.indexOf(LocalaiNodeBindingGuard) > guards.indexOf(JwtAuthGuard) }).toEqual({ jwt: true, binding: true });
        checked.push(`${base}/${String(Reflect.getMetadata(PATH_METADATA, handler))}`);
      }
    }
    expect(checked.sort()).toEqual(['/nodes/:id/localai/commands/:cmdId/failure', '/nodes/:id/localai/commands/:cmdId/renew', '/nodes/:id/localai/commands/:cmdId/result', '/nodes/:id/localai/commands/claim']);
  });

  it('answers 401 without a credential and 403 to a browser session, on every route', async () => {
    const { owner } = await arrange();
    const id = 'dddddddd-dddd-4ddd-8ddd-ddddddddddd4';
    for (const path of ['claim', `${id}/renew`, `${id}/result`, `${id}/failure`]) {
      expect((await post(path, {}, null)).statusCode).toBe(401);
      const session = await post(path, {}, owner.accessToken);
      expect(session.statusCode).toBe(403);
      expect(session.json().details).toEqual({ code: 'LAI-CLD-004', reason: 'node_credential_required' });
    }
    expect(store.writes).toHaveLength(0);
  });

  it('claim: an empty queue answers 200 with the empty fixture shape', async () => {
    await arrange();
    const response = await post('claim', { ...claimBody(), waitMs: 0 });
    expect(response.statusCode).toBe(200);
    const body = response.json();
    expect(Object.keys(body)).toEqual(Object.keys(fixture('claim.response.empty.json')));
    expect(body.data).toEqual({ jobs: [] });
  });

  it('claim: a queued command comes back in the fixture shape, with the lease and token', async () => {
    await arrange();
    const { command } = await queue('localai.model.load', { model_id: MODEL });
    const response = await post('claim', { ...claimBody(), waitMs: 0 });
    expect(response.statusCode).toBe(200);
    const body = response.json();
    const expected = fixture('claim.response.model-load.json');
    expect(Object.keys(body)).toEqual(Object.keys(expected));
    const [entry] = body.data.jobs;
    expect(Object.keys(entry)).toEqual(Object.keys(expected.data.jobs[0]));
    expect(Object.keys(entry.job)).toEqual(Object.keys(expected.data.jobs[0].job));
    expect(entry.job).toEqual({ id: command.id, type: 'localai.model.load', attempts: 1, createdAt: command.createdAt.toISOString() });
    expect(entry.params).toEqual(expected.data.jobs[0].params);
    expect(entry.renewIntervalMs).toBe(expected.data.jobs[0].renewIntervalMs);
    expect(entry.traceparent).toBeNull();
    expect(entry.leaseExpiresAt).toBe('2026-11-02T15:05:00.000Z');
    expect(entry.claimToken).toMatch(/^[0-9a-f-]{36}$/);
    expect(typeof body.meta.timestamp).toBe('string');
  });

  it('claim: drops unknown type names, clamps limit and waitMs, and never serves another node\'s commands', async () => {
    await arrange();
    await queue('localai.model.load', { model_id: MODEL }, OTHER_NODE_ROW);
    const response = await post('claim', { types: ['model.load', 'bogus'], limit: 50, waitMs: -10 });
    expect(response.statusCode).toBe(200);
    expect(response.json().data).toEqual({ jobs: [] });
  });

  it('claim: a body with an unknown key or a bad shape is a 400 with Zod issues in details', async () => {
    await arrange();
    for (const body of [{ types: ['model.load'], extra: 1 }, { types: 'model.load' }, {}, { types: [], waitMs: 'soon' }, []]) {
      const response = await post('claim', body);
      expect(response.statusCode).toBe(400);
      expect(response.json().details).toMatchObject({ reason: 'invalid_body', issues: expect.any(Array) });
    }
  });

  it('renew: extends the lease; a wrong token is 409 with the lease-lost body of the fixture', async () => {
    await arrange();
    const { command } = await queue('localai.model.load', { model_id: MODEL });
    const claimed = (await post('claim', { ...claimBody(), waitMs: 0 })).json().data.jobs[0];
    clock.advance(20_000);
    const ok = await post(`${command.id}/renew`, { claimToken: claimed.claimToken });
    expect(ok.statusCode).toBe(200);
    expect(Object.keys(ok.json())).toEqual(Object.keys(fixture('renew.response.json')));
    expect(ok.json().data).toEqual({ leaseExpiresAt: '2026-11-02T15:05:20.000Z' });

    const wrong = await post(`${command.id}/renew`, { claimToken: '1f0e8b7a-3c2d-4e9f-8a6b-5d4c3b2a1f0e' });
    expect(wrong.statusCode).toBe(409);
    // EAB's error body: the stable members are the status, the code, the message and our details.
    const { path: _path, timestamp: _timestamp, ...stable } = fixture('failure.conflict.response.json');
    expect(wrong.json()).toMatchObject(stable);
  });

  it('renew: 409 after the lease passed, 409 for an unknown command, 404 for a malformed id, 400 for a bad body', async () => {
    await arrange();
    const { command } = await queue('localai.model.load', { model_id: MODEL });
    const claimed = (await post('claim', { ...claimBody(), waitMs: 0 })).json().data.jobs[0];
    expect((await post(`${command.id}/renew`, { claimToken: claimed.claimToken, extra: true })).statusCode).toBe(400);
    expect((await post(`${command.id}/renew`, {})).statusCode).toBe(400);
    expect((await post('not-a-uuid/renew', { claimToken: claimed.claimToken })).statusCode).toBe(404);
    expect((await post('dddddddd-dddd-4ddd-8ddd-ddddddddddd4/renew', { claimToken: claimed.claimToken })).statusCode).toBe(409);
    clock.advance(60_000);
    expect((await post(`${command.id}/renew`, { claimToken: claimed.claimToken })).statusCode).toBe(409);
  });

  it('result: 200 succeeded in the fixture shape; the same post again is 200; a stranger\'s is 409', async () => {
    await arrange();
    const { command } = await queue('localai.model.load', { model_id: MODEL });
    const claimed = (await post('claim', { ...claimBody(), waitMs: 0 })).json().data.jobs[0];
    const body: Record<string, any> = { ...fixture('result.request.model-load.json'), claimToken: claimed.claimToken };
    const ok = await post(`${command.id}/result`, body);
    expect(ok.statusCode).toBe(200);
    expect(ok.json().data).toEqual(fixture('result.response.json').data);
    expect(store.get(command.id)).toMatchObject({ status: 'succeeded', result: body.result });
    expect((await post(`${command.id}/result`, body)).statusCode).toBe(200);
    expect((await post(`${command.id}/result`, { ...body, claimToken: '1f0e8b7a-3c2d-4e9f-8a6b-5d4c3b2a1f0e' })).statusCode).toBe(409);
  });

  it('result: 400 for a type mismatch or an invalid result, with Zod issues; the command stays running', async () => {
    await arrange();
    const { command } = await queue('localai.model.load', { model_id: MODEL });
    const claimed = (await post('claim', { ...claimBody(), waitMs: 0 })).json().data.jobs[0];
    const good = fixture('result.request.model-load.json');
    const mismatch = await post(`${command.id}/result`, { ...good, claimToken: claimed.claimToken, type: 'localai.model.unload' });
    expect(mismatch.statusCode).toBe(400);
    expect(mismatch.json().details.reason).toBe('result_type_mismatch');
    const invalid = await post(`${command.id}/result`, { ...good, claimToken: claimed.claimToken, result: { ...good.result, status: 'loading' } });
    expect(invalid.statusCode).toBe(400);
    expect(invalid.json().details).toMatchObject({ reason: 'invalid_result', issues: [expect.objectContaining({ path: ['status'] })] });
    const missing = await post(`${command.id}/result`, { type: good.type, claimToken: claimed.claimToken });
    expect(missing.statusCode).toBe(400);
    expect(store.get(command.id).status).toBe('running');
  });

  it('result: 409 after the lease passed, and the command is not changed', async () => {
    await arrange();
    const { command } = await queue('localai.model.load', { model_id: MODEL });
    const claimed = (await post('claim', { ...claimBody(), waitMs: 0 })).json().data.jobs[0];
    clock.advance(61_000);
    const late = await post(`${command.id}/result`, { ...fixture('result.request.model-load.json'), claimToken: claimed.claimToken });
    expect(late.statusCode).toBe(409);
    expect(store.get(command.id).status).toBe('running');
  });

  it('failure: 200 failed in the fixture shape, code split from the message; then 409 for a different token', async () => {
    await arrange();
    const { command } = await queue('localai.model.load', { model_id: MODEL });
    const claimed = (await post('claim', { ...claimBody(), waitMs: 0 })).json().data.jobs[0];
    const ok = await post(`${command.id}/failure`, { ...fixture('failure.request.json'), claimToken: claimed.claimToken });
    expect(ok.statusCode).toBe(200);
    expect(ok.json().data).toEqual(fixture('failure.response.json').data);
    expect(store.get(command.id)).toMatchObject({ status: 'failed', error: { code: 'LAI-CAT-005', message: 'Model does not fit in free VRAM' } });
    expect((await post(`${command.id}/failure`, { claimToken: '1f0e8b7a-3c2d-4e9f-8a6b-5d4c3b2a1f0e', error: 'x' })).statusCode).toBe(409);
  });

  it('failure: accepts rateLimited and retryAfterMs, refuses a too-long error and other unknown keys', async () => {
    await arrange();
    const { command } = await queue('localai.model.load', { model_id: MODEL });
    const claimed = (await post('claim', { ...claimBody(), waitMs: 0 })).json().data.jobs[0];
    expect((await post(`${command.id}/failure`, { claimToken: claimed.claimToken, error: 'x'.repeat(2001) })).statusCode).toBe(400);
    expect((await post(`${command.id}/failure`, { claimToken: claimed.claimToken, error: 'x', unknown: 1 })).statusCode).toBe(400);
    const ok = await post(`${command.id}/failure`, { claimToken: claimed.claimToken, error: 'boom', rateLimited: true, retryAfterMs: 1000 });
    expect(ok.statusCode).toBe(200);
    expect(store.get(command.id).error).toEqual({ code: 'LAI-CLD-012', message: 'boom' });
  });

  it('a command of another node is a 409 on every settle route (its existence is not revealed)', async () => {
    await arrange();
    const foreign = store.seed({ nodeId: OTHER_NODE_ROW, type: 'localai.model.load', status: 'running', claimToken: '1f0e8b7a-3c2d-4e9f-8a6b-5d4c3b2a1f0e', claimedAt: clock.now(), leaseExpiresAt: new Date(clock.now().getTime() + 60_000) });
    const token = { claimToken: '1f0e8b7a-3c2d-4e9f-8a6b-5d4c3b2a1f0e' };
    expect((await post(`${foreign.id}/renew`, token)).statusCode).toBe(409);
    expect((await post(`${foreign.id}/result`, { ...fixture('result.request.model-load.json'), ...token })).statusCode).toBe(409);
    expect((await post(`${foreign.id}/failure`, { ...token, error: 'x' })).statusCode).toBe(409);
    expect(store.get(foreign.id)).toMatchObject({ status: 'running' });
  });
});

describe('the claim route and a client that goes away', () => {
  it('aborts the held poll when the response closes before it was written, and releases it when it was', async () => {
    const seen: AbortSignal[] = [];
    const claims = { claim: jest.fn(async (_node: string, _body: unknown, signal: AbortSignal) => (seen.push(signal), { jobs: [] })) };
    const controller = new NodeLinkCommandsController(claims as never, {} as never);
    const node = { id: NODE_ROW } as never;

    const gone = Object.assign(new EventEmitter(), { writableFinished: false });
    claims.claim.mockImplementationOnce(async (_node, _body, signal) => {
      seen.push(signal);
      gone.emit('close'); // the socket closed while the poll was held
      return { jobs: [] };
    });
    await controller.claim(node, { types: ['model.load'], waitMs: 25000 }, { raw: gone } as never);
    expect(seen[0].aborted).toBe(true);
    expect(gone.listenerCount('close')).toBe(0);

    const finished = Object.assign(new EventEmitter(), { writableFinished: true });
    await controller.claim(node, { types: ['model.load'], waitMs: 25000 }, { raw: finished } as never);
    finished.emit('close'); // a normal close after the answer must not abort anything
    expect(finished.listenerCount('close')).toBe(0);
  });
});
