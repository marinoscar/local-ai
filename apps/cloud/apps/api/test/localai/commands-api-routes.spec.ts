// The command routes of the cloud UI over HTTP (CLOUD-09), on the whole AppModule over a mocked database (no
// PostgreSQL): the real `JwtAuthGuard` and permissions guard with the real seeded grants, the real controller and
// services, the real audit sink and the real event bus, and the in-memory command table behind
// `prisma.localaiCommand`. This proves the RBAC matrix per command type, the refusal table with its doctor codes,
// the 201/200 split, cancel, the wire shape and the settled-command wiring (one audit row, one notification). The
// SQL itself is `commands-api.db.spec.ts`.
import fastifyCookie from '@fastify/cookie';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { CommandClaimRepository } from '../../src/localai/commands/claim.sql';
import { COMMAND_SETTLED_CHANNEL, LOCALAI_CLOCK } from '../../src/localai/commands/command-events';
import { LOCALAI_COMMAND_NOTIFIER } from '../../src/localai/commands/command-notifier';
import { COMMAND_POLICY, FEATURE_ONLY_TYPES } from '../../src/localai/commands/command-policy';
import { COMMAND_TYPES, type CommandType } from '../../src/localai/contracts/commands';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';
import { FakeClock, FakeCommandStore, StoreClaimRepository } from './support/fake-command-store';
import { readCommandExamples } from '../fixtures/localai/command-examples';

const EXAMPLES = readCommandExamples<Record<CommandType, { params: Record<string, unknown> }>>();
const NODE_ROW = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee5';
const MODEL = 'qwen3.6-35b-a3b-q4km';
const ADMIN_TYPES = COMMAND_TYPES.filter((type) => COMMAND_POLICY[type].permission === 'lab:admin');
/** The types the generic route queues: music.generate is queued by `/api/localai/music/requests` only (its own spec). */
const GENERIC_TYPES = COMMAND_TYPES.filter((type) => !FEATURE_ONLY_TYPES.has(type));

const clock = new FakeClock(new Date('2026-11-02T15:04:00.000Z'));
const store = new FakeCommandStore(clock);
const notifier = { commandFailed: jest.fn().mockResolvedValue(undefined) };

describe('Local AI command routes for the UI over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let node: { id: string; nodeId: string; displayName: string; phase: string; mode: string | null } | null;
  let model: { present: boolean } | null;

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
      .overrideProvider(CommandClaimRepository)
      .useValue(new StoreClaimRepository(store))
      .overrideProvider(LOCALAI_CLOCK)
      .useValue(clock)
      .overrideProvider(LOCALAI_COMMAND_NOTIFIER)
      .useValue(notifier)
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
    store.rows.length = 0;
    store.writes.length = 0;
    store.knownNodes.set('ai-n2', NODE_ROW);
    clock.set(new Date('2026-11-02T15:04:00.000Z'));
    notifier.commandFailed.mockClear();
    node = { id: NODE_ROW, nodeId: 'ai-n2', displayName: 'Node ai-n2', phase: 'enrolled', mode: 'online' };
    model = { present: true };
    prismaMock.localaiNode.findUnique.mockImplementation(async ({ where }: { where: { nodeId?: string } }) => (where.nodeId === 'ai-n2' ? node : null));
    prismaMock.localaiNodeModel.findUnique.mockImplementation(async () => model);
    for (const method of ['create', 'findFirst', 'findMany', 'findUnique', 'count', 'updateMany'] as const) {
      prismaMock.localaiCommand[method].mockImplementation((store.localaiCommand as any)[method]);
    }
    prismaMock.auditEvent.create.mockResolvedValue({});
    prismaMock.auditEvent.findFirst.mockResolvedValue(null);
  });

  const token = (role: FixtureRole) => registerMockUser(jwt, role).accessToken;
  const send = (method: 'GET' | 'POST' | 'DELETE', url: string, accessToken: string | null, payload?: unknown) =>
    app.inject({ method, url: `/api/localai${url}`, headers: accessToken ? { authorization: `Bearer ${accessToken}` } : {}, payload: payload as never });
  const post = (accessToken: string | null, type: string, params: unknown = {}, node = 'ai-n2') => send('POST', `/nodes/${node}/commands`, accessToken, { type, params });
  const audits = (action: string) => prismaMock.auditEvent.create.mock.calls.map((call: any[]) => call[0].data).filter((data: any) => data.action === action);

  describe('authentication and the permission matrix (spec 3.7, 3.9)', () => {
    it('answers 401 on every route without a session', async () => {
      expect((await send('GET', '/nodes/ai-n2/commands', null)).statusCode).toBe(401);
      expect((await post(null, 'status.refresh')).statusCode).toBe(401);
      expect((await send('GET', '/commands/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb1', null)).statusCode).toBe(401);
      expect((await send('DELETE', '/commands/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb1', null)).statusCode).toBe(401);
    });

    it.each(GENERIC_TYPES)('%s: viewer 403, contributor %s, admin 201', async (type) => {
      const short = type.replace('localai.', '');
      const params = EXAMPLES[type].params;
      expect((await post(token('viewer'), short, params)).statusCode).toBe(403);
      expect(store.rows).toHaveLength(0);

      const contributor = await post(token('contributor'), short, params);
      expect(contributor.statusCode).toBe(ADMIN_TYPES.includes(type) ? 403 : 201);
      store.rows.length = 0;

      const admin = await post(token('admin'), short, params);
      expect(admin.statusCode).toBe(201);
      expect(admin.json().data).toMatchObject({ nodeId: 'ai-n2', type, status: 'queued' });
    });

    it('the four admin types are exactly model.delete, mode.offline, key.rotate and agent.update', () => {
      expect([...ADMIN_TYPES].sort()).toEqual(['localai.agent.update', 'localai.key.rotate', 'localai.mode.offline', 'localai.model.delete']);
    });

    it('a viewer reads the history and one command (lab:read) but cannot cancel', async () => {
      const { body } = { body: (await post(token('contributor'), 'status.refresh')).json().data };
      const viewer = token('viewer');
      expect((await send('GET', '/nodes/ai-n2/commands', viewer)).statusCode).toBe(200);
      expect((await send('GET', `/commands/${body.id}`, viewer)).statusCode).toBe(200);
      expect((await send('DELETE', `/commands/${body.id}`, viewer)).statusCode).toBe(403);
      expect(store.get(body.id).status).toBe('queued');
    });
  });

  describe('sending a command', () => {
    it('answers the example of spec 3.9: 201, the prefixed type, the parsed params and the TTL expiry, in the data envelope', async () => {
      const response = await post(token('contributor'), 'model.load', { model_id: MODEL, unload_others: true });
      expect(response.statusCode).toBe(201);
      expect(response.json().data).toMatchObject({
        nodeId: 'ai-n2',
        type: 'localai.model.load',
        status: 'queued',
        params: { model_id: MODEL, wait: true, unload_others: true, set_default: false },
        source: 'ui',
        expiresAt: '2026-11-02T15:19:00.000Z',
        createdAt: '2026-11-02T15:04:00.000Z',
      });
      expect(response.json().data.id).toMatch(/^[0-9a-f-]{36}$/);
      expect(response.json().data).not.toHaveProperty('claimToken');
    });

    it('accepts the prefixed type as well', async () => {
      const response = await post(token('contributor'), 'localai.status.refresh');
      expect(response.statusCode).toBe(201);
      expect(response.json().data.type).toBe('localai.status.refresh');
    });

    it('a second identical request is 200 with the active row; a different model is a new 201', async () => {
      const first = await post(token('contributor'), 'model.load', { model_id: MODEL });
      const second = await post(token('admin'), 'model.load', { model_id: MODEL });
      expect(second.statusCode).toBe(200);
      expect(second.json().data.id).toBe(first.json().data.id);
      expect(store.rows).toHaveLength(1);
      expect((await post(token('contributor'), 'model.load', { model_id: 'gemma-4-26b-q4km' })).statusCode).toBe(201);
    });

    it('audits localai.command.requested once per new command through the real audit sink, and not for the duplicate', async () => {
      const first = await post(token('contributor'), 'model.unload', { model_id: MODEL });
      await post(token('contributor'), 'model.unload', { model_id: MODEL });
      const rows = audits('localai.command.requested');
      expect(rows).toHaveLength(1);
      expect(rows[0]).toMatchObject({ targetType: 'localai_command', targetId: first.json().data.id, meta: { nodeId: 'ai-n2', type: 'localai.model.unload', params: JSON.stringify({ model_id: MODEL }) } });
      expect(rows[0].actorUserId).toBeTruthy();
    });

    it('400 for a body that is not valid, an unknown type, params that fail the schema and an unknown param key', async () => {
      const t = token('admin');
      expect((await send('POST', '/nodes/ai-n2/commands', t, { params: {} })).statusCode).toBe(400);
      expect((await send('POST', '/nodes/ai-n2/commands', t, { type: 'status.refresh', surprise: true })).statusCode).toBe(400);
      expect((await post(t, 'shell.exec', {})).statusCode).toBe(400);
      const bad = await post(t, 'model.load', { model_id: 'NOT VALID' });
      expect(bad.statusCode).toBe(400);
      expect(JSON.stringify(bad.json())).toContain('model_id');
      expect((await post(t, 'status.refresh', { extra: 1 })).statusCode).toBe(400);
      expect((await post(t, 'model.delete', { ...EXAMPLES['localai.model.delete'].params, confirm: 'wrong' })).statusCode).toBe(400);
      expect(store.rows).toHaveLength(0);
      expect(audits('localai.command.requested')).toHaveLength(0);
    });
  });

  describe('refusals (spec 3.9 and 5)', () => {
    it('404 for an unknown node', async () => {
      const response = await post(token('contributor'), 'status.refresh', {}, 'ai-n9');
      expect(response.statusCode).toBe(404);
    });

    it.each(['pending', 'retired'])('409 LAI-CLD-035 for a %s node', async (phase) => {
      node = { ...node!, phase, mode: null };
      const response = await post(token('contributor'), 'status.refresh');
      expect(response.statusCode).toBe(409);
      expect(JSON.stringify(response.json())).toContain('LAI-CLD-035');
      expect(store.rows).toHaveLength(0);
    });

    it.each([
      ['offline', 'Offline'],
      ['survival', 'Survival'],
    ])('422 LAI-CLD-034 in plain words for a node in %s mode', async (mode, label) => {
      node = { ...node!, mode };
      const response = await post(token('admin'), 'status.refresh');
      expect(response.statusCode).toBe(422);
      expect(JSON.stringify(response.json())).toContain('LAI-CLD-034');
      expect(JSON.stringify(response.json())).toContain(`This computer is in ${label} mode. Only someone at it can switch it back.`);
      expect(store.rows).toHaveLength(0);
    });

    it('422 when the model is not on the node, but model.download of the same model is 201', async () => {
      model = null;
      const refused = await post(token('contributor'), 'model.load', { model_id: MODEL });
      expect(refused.statusCode).toBe(422);
      expect(JSON.stringify(refused.json())).toContain('Download it first');
      expect((await post(token('contributor'), 'model.download', { model_id: MODEL })).statusCode).toBe(201);
    });
  });

  describe('reading', () => {
    it('lists a node\'s commands newest first with the page envelope and filters by status', async () => {
      await post(token('contributor'), 'status.refresh');
      await post(token('contributor'), 'model.load', { model_id: MODEL });
      store.rows[0].status = 'failed';
      const all = await send('GET', '/nodes/ai-n2/commands?page=1', token('viewer'));
      expect(all.statusCode).toBe(200);
      expect(all.json().data).toMatchObject({ page: 1, pageSize: 20, total: 2, totalPages: 1 });
      expect(all.json().data.items).toHaveLength(2);
      for (const item of all.json().data.items) expect(item).not.toHaveProperty('result');
      const queued = await send('GET', '/nodes/ai-n2/commands?status=queued', token('viewer'));
      expect(queued.json().data.total).toBe(1);
    });

    it('400 for a bad status or page, 404 for an unknown node', async () => {
      expect((await send('GET', '/nodes/ai-n2/commands?status=bogus', token('viewer'))).statusCode).toBe(400);
      expect((await send('GET', '/nodes/ai-n2/commands?page=0', token('viewer'))).statusCode).toBe(400);
      expect((await send('GET', '/nodes/ai-n9/commands', token('viewer'))).statusCode).toBe(404);
    });

    it('returns one command with its result and error; 404 for an unknown or malformed id', async () => {
      const created = (await post(token('contributor'), 'status.refresh')).json().data;
      store.get(created.id).status = 'succeeded';
      store.get(created.id).result = { seq: 4, reported_at: '2026-11-02T15:04:10.000Z' };
      const one = await send('GET', `/commands/${created.id}`, token('viewer'));
      expect(one.statusCode).toBe(200);
      expect(one.json().data).toMatchObject({ id: created.id, nodeId: 'ai-n2', status: 'succeeded', result: { seq: 4 }, error: null });
      expect((await send('GET', '/commands/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', token('viewer'))).statusCode).toBe(404);
      expect((await send('GET', '/commands/not-a-uuid', token('viewer'))).statusCode).toBe(404);
    });
  });

  describe('cancel', () => {
    it('cancels a queued command (200, cancelled), audits it once and frees the key for a new request', async () => {
      const created = (await post(token('contributor'), 'model.load', { model_id: MODEL })).json().data;
      const cancelled = await send('DELETE', `/commands/${created.id}`, token('contributor'));
      expect(cancelled.statusCode).toBe(200);
      expect(cancelled.json().data).toMatchObject({ id: created.id, status: 'cancelled' });
      expect(audits('localai.command.cancelled')).toHaveLength(1);
      expect(audits('localai.command.cancelled')[0]).toMatchObject({ targetId: created.id, meta: { nodeId: 'ai-n2', type: 'localai.model.load' } });
      expect((await post(token('contributor'), 'model.load', { model_id: MODEL })).statusCode).toBe(201);
    });

    it('409 for a running command, which stays running, and for one already cancelled', async () => {
      const created = (await post(token('contributor'), 'status.refresh')).json().data;
      store.get(created.id).status = 'running';
      const running = await send('DELETE', `/commands/${created.id}`, token('admin'));
      expect(running.statusCode).toBe(409);
      expect(store.get(created.id).status).toBe('running');
      store.get(created.id).status = 'cancelled';
      expect((await send('DELETE', `/commands/${created.id}`, token('admin'))).statusCode).toBe(409);
      expect(audits('localai.command.cancelled')).toHaveLength(0);
    });

    it('a contributor cannot cancel an admin type (403) but an admin can', async () => {
      const created = (await post(token('admin'), 'model.delete', EXAMPLES['localai.model.delete'].params)).json().data;
      expect((await send('DELETE', `/commands/${created.id}`, token('contributor'))).statusCode).toBe(403);
      expect(store.get(created.id).status).toBe('queued');
      expect((await send('DELETE', `/commands/${created.id}`, token('admin'))).statusCode).toBe(200);
    });

    it('404 for an unknown or malformed id', async () => {
      expect((await send('DELETE', '/commands/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', token('admin'))).statusCode).toBe(404);
      expect((await send('DELETE', '/commands/nope', token('admin'))).statusCode).toBe(404);
    });
  });

  describe('a settled command (the audit listener on the real bus)', () => {
    const settle = async (id: string, status: 'succeeded' | 'failed' | 'expired') => {
      await app.get<EventBus>(EVENT_BUS).publish(COMMAND_SETTLED_CHANNEL, { nodeId: NODE_ROW, commandId: id, status });
      await new Promise((resolve) => setTimeout(resolve, 25));
    };

    it('writes one localai.command.settled row for a succeeded command and sends no notification', async () => {
      const created = (await post(token('contributor'), 'status.refresh')).json().data;
      store.get(created.id).status = 'succeeded';
      store.get(created.id).result = { seq: 1, reported_at: '2026-11-02T15:04:10.000Z' };
      await settle(created.id, 'succeeded');
      const rows = audits('localai.command.settled');
      expect(rows).toHaveLength(1);
      expect(rows[0]).toMatchObject({ targetType: 'localai_command', targetId: created.id, meta: { nodeId: 'ai-n2', type: 'localai.status.refresh', status: 'succeeded' } });
      expect(JSON.stringify(rows[0])).not.toContain('reported_at');
      expect(notifier.commandFailed).not.toHaveBeenCalled();
    });

    it('a failed command: one settled row with the error code and one notification to the requester', async () => {
      const requester = registerMockUser(jwt, 'contributor');
      const created = (await post(requester.accessToken, 'model.load', { model_id: MODEL })).json().data;
      Object.assign(store.get(created.id), { status: 'failed', error: { code: 'LAI-CAT-005', message: 'Model does not fit in free VRAM' } });
      await settle(created.id, 'failed');
      expect(audits('localai.command.settled')).toHaveLength(1);
      expect(audits('localai.command.settled')[0].meta).toEqual({ nodeId: 'ai-n2', type: 'localai.model.load', status: 'failed', errorCode: 'LAI-CAT-005' });
      expect(notifier.commandFailed).toHaveBeenCalledTimes(1);
      expect(notifier.commandFailed).toHaveBeenCalledWith(
        expect.objectContaining({ recipientUserId: requester.id, nodeId: 'ai-n2', type: 'model.load', status: 'failed', errorCode: 'LAI-CAT-005' }),
      );
      expect(notifier.commandFailed.mock.calls[0][0].message).toMatch(/^model\.load on Node ai-n2 failed: Choose a smaller model/);
    });

    it('an expired command notifies its requester too', async () => {
      const created = (await post(token('contributor'), 'status.refresh')).json().data;
      Object.assign(store.get(created.id), { status: 'expired', error: { code: 'LAI-CLD-030', message: 'The node did not pick this command up in time' } });
      await settle(created.id, 'expired');
      expect(notifier.commandFailed).toHaveBeenCalledWith(expect.objectContaining({ status: 'expired', errorCode: 'LAI-CLD-030' }));
    });
  });
});
