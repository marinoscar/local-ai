// The node admin routes of the detail page over HTTP (CLOUD-12): PATCH, credential replacement, retire, and the tailnet
// reveal audit. On the whole AppModule over a mocked database (no PostgreSQL): the real guards with the real seeded
// grants, the real controllers and services, the real audit sink. The credential service and the key store are fakes so
// the specs can see exactly what was minted, revoked and deleted. Real SQL is `node-admin.db.spec.ts`.
import fastifyCookie from '@fastify/cookie';
import { NotFoundException } from '@nestjs/common';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { JobWorker } from '@marinoscar/platform-api/jobs';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { NodeKeyService } from '../../src/localai/keys/node-key.service';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';

const NODE_ROW = 'dddddddd-dddd-4ddd-8ddd-ddddddddddd2';
const WORKER = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2';
const OLD_CREDENTIAL = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc1';
const NEW_CREDENTIAL = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc2';
// Made-up values built from parts: no literal tailnet address and no token-shaped literal sits in the repository.
const TAILNET_IP = [100, 64, 7, 8].join('.');
const NEW_TOKEN = ['nod', 'x'.repeat(6) + Date.now().toString(36)].join('_');

const credentials = {
  createCredential: jest.fn(),
  revokeAnyCredential: jest.fn(),
};
const keys = { deleteKey: jest.fn(), evict: jest.fn(), report: jest.fn(), getKey: jest.fn() };

function nodeRow(patch: Record<string, unknown> = {}) {
  return {
    id: NODE_ROW,
    nodeId: 'ai-n2',
    displayName: 'Node Two',
    hardware: null,
    backend: 'cuda',
    publicHost: 'ai-n2.example.test',
    servePattern: 'B',
    tailnetMachine: 'machine-two',
    tailnetIp: TAILNET_IP,
    profile: 'cuda-12gb',
    mode: 'online',
    connectivity: 'full',
    agentVersion: '0.4.1',
    catalogVersion: '2026.11.02-1',
    catalogPin: null,
    gatewayEnabled: true,
    gatewayPriority: 100,
    phase: 'enrolled',
    workerNodeId: WORKER,
    nodeCredentialId: OLD_CREDENTIAL,
    lastState: { mode: 'online', hardware: { gpus: [] }, ark: { roots: [], drive_last_synced_at: null } },
    lastStateAt: new Date(Date.now() - 5_000),
    lastStateSeq: 3n,
    keyHint: 'ab12…9f0e',
    keyReportedAt: new Date('2026-11-02T10:00:00.000Z'),
    publishChecklist: {},
    createdById: null,
    createdAt: new Date(),
    updatedAt: new Date(),
    retiredAt: null,
    ...patch,
  };
}

describe('Node admin routes over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let row: ReturnType<typeof nodeRow> | null;

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
      .overrideProvider(NodeCredentialService)
      .useValue(credentials)
      .overrideProvider(NodeKeyService)
      .useValue(keys)
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
    row = nodeRow();
    prismaMock.localaiNode.findUnique.mockImplementation(async ({ where }: { where: { nodeId?: string } }) => (row && where.nodeId === row.nodeId ? row : null));
    prismaMock.localaiNode.update.mockImplementation(async ({ data }: { data: Record<string, unknown> }) => ({ ...row, ...data }));
    prismaMock.localaiNode.findMany.mockResolvedValue([]);
    prismaMock.localaiNodeModel.findMany.mockResolvedValue([]);
    prismaMock.workerNode.findMany.mockResolvedValue([{ id: WORKER, status: 'online', lastHeartbeatAt: new Date() }]);
    prismaMock.workerNode.findUnique.mockResolvedValue({ eligibleTypes: ['localai.model.load', 'localai.status.refresh', 'other.type'] });
    prismaMock.localaiAlert.groupBy.mockResolvedValue([]);
    prismaMock.localaiCatalogVersion.findFirst.mockResolvedValue({ version: '2026.11.02-1', files: [{ path: 'models.yaml', content: 'models:\n  - id: model-one-q4\n    files:\n      - size_bytes: 123\n' }] });
    prismaMock.nodeCredential.findUnique.mockImplementation(async ({ where }: { where: { id: string } }) =>
      where.id === OLD_CREDENTIAL ? { id: OLD_CREDENTIAL, tokenPrefix: 'oldpfx', lastUsedAt: new Date('2026-11-03T11:59:00.000Z'), createdAt: new Date('2026-10-01T00:00:00.000Z'), revokedAt: null } : null,
    );
    prismaMock.auditEvent.create.mockResolvedValue({});
    prismaMock.localaiCommand.updateMany.mockResolvedValue({ count: 0 });
    prismaMock.$transaction.mockImplementation(async (fn: (tx: unknown) => unknown) => fn(prismaMock));
    credentials.createCredential.mockReset().mockResolvedValue({ token: NEW_TOKEN, id: NEW_CREDENTIAL, name: 'localai-ai-n2', tokenPrefix: 'newpfx', expiresAt: null, createdAt: '2026-11-04T10:00:00.000Z' });
    credentials.revokeAnyCredential.mockReset().mockResolvedValue(undefined);
    keys.deleteKey.mockReset().mockResolvedValue(undefined);
  });

  const as = (role: FixtureRole) => registerMockUser(jwt, role);
  const send = (method: 'GET' | 'PATCH' | 'POST', url: string, token: string | null, payload?: unknown) =>
    app.inject({ method, url: `/api/localai/nodes${url}`, headers: token ? { authorization: `Bearer ${token}` } : {}, payload: payload as never });
  const audits = (action: string) => prismaMock.auditEvent.create.mock.calls.map((call: any[]) => call[0].data).filter((data: any) => data.action === action); // eslint-disable-line @typescript-eslint/no-explicit-any

  describe('permissions: all three writes are lab:admin', () => {
    const routes: Array<[string, 'PATCH' | 'POST', string, unknown]> = [
      ['PATCH node', 'PATCH', '/ai-n2', { displayName: 'New name' }],
      ['POST credential', 'POST', '/ai-n2/credential', undefined],
      ['POST retire', 'POST', '/ai-n2/retire', { confirm: 'ai-n2' }],
    ];

    it.each(routes)('%s: 401 without a session, 403 for a viewer and a contributor, nothing written', async (_name, method, url, payload) => {
      expect((await send(method, url, null, payload)).statusCode).toBe(401);
      for (const role of ['viewer', 'contributor'] as const) {
        expect((await send(method, url, as(role).accessToken, payload)).statusCode).toBe(403);
      }
      expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
      expect(credentials.createCredential).not.toHaveBeenCalled();
      expect(keys.deleteKey).not.toHaveBeenCalled();
      expect(prismaMock.auditEvent.create).not.toHaveBeenCalled();
    });
  });

  describe('PATCH /api/localai/nodes/:nodeId', () => {
    it('applies the fields, audits the changed ones (the tailnet IP as <set>) and answers the detail without the IP', async () => {
      const response = await send('PATCH', '/ai-n2', as('admin').accessToken, {
        displayName: 'Renamed',
        servePattern: 'A',
        tailnetIp: [100, 90, 1, 1].join('.'),
        gatewayPriority: 7,
      });
      expect(response.statusCode).toBe(200);
      expect(prismaMock.localaiNode.update).toHaveBeenCalledWith({
        where: { id: NODE_ROW },
        data: { displayName: 'Renamed', servePattern: 'A', tailnetIp: [100, 90, 1, 1].join('.'), gatewayPriority: 7 },
      });
      const rows = audits('localai.node.updated');
      expect(rows).toHaveLength(1);
      expect(rows[0]).toMatchObject({ targetType: 'localai_node', targetId: 'ai-n2', meta: { displayName: 'Renamed', servePattern: 'A', tailnetIp: '<set>', gatewayPriority: 7 } });
      expect(JSON.stringify(rows[0].meta)).not.toContain([100, 90, 1, 1].join('.'));
      expect(response.body).not.toContain([100, 90, 1, 1].join('.'));
      expect(response.body).not.toContain(TAILNET_IP);
      expect((response.json() as { data: { nodeId: string } }).data.nodeId).toBe('ai-n2');
    });

    it('audits a cleared tailnet IP as <cleared> and writes nothing when no field changes', async () => {
      await send('PATCH', '/ai-n2', as('admin').accessToken, { tailnetIp: '' });
      expect(audits('localai.node.updated')[0].meta).toEqual({ tailnetIp: '<cleared>' });
      prismaMock.localaiNode.update.mockClear();
      prismaMock.auditEvent.create.mockClear();
      expect((await send('PATCH', '/ai-n2', as('admin').accessToken, { displayName: 'Node Two' })).statusCode).toBe(200);
      expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
      expect(prismaMock.auditEvent.create).not.toHaveBeenCalled();
    });

    it.each([
      ['an unknown servePattern', { servePattern: 'Z' }],
      ['a publicHost that is not a host name', { publicHost: 'not a host' }],
      ['a fractional gatewayPriority', { gatewayPriority: 1.5 }],
      ['an unknown key', { phase: 'retired' }],
      ['an empty body', {}],
    ])('400 for %s, nothing written', async (_name, body) => {
      const response = await send('PATCH', '/ai-n2', as('admin').accessToken, body);
      expect(response.statusCode).toBe(400);
      expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
    });

    it('404 for an unknown node and 409 LAI-CLD-035 for a retired one', async () => {
      expect((await send('PATCH', '/ai-n9', as('admin').accessToken, { displayName: 'x' })).statusCode).toBe(404);
      row = nodeRow({ phase: 'retired' });
      const retired = await send('PATCH', '/ai-n2', as('admin').accessToken, { displayName: 'x' });
      expect(retired.statusCode).toBe(409);
      expect(JSON.stringify(retired.json())).toContain('LAI-CLD-035');
    });
  });

  describe('POST /api/localai/nodes/:nodeId/credential', () => {
    it('mints, binds the new credential, revokes the old one, audits both without the token and shows the token once', async () => {
      const response = await send('POST', '/ai-n2/credential', as('admin').accessToken);
      expect(response.statusCode).toBe(201);
      const data = (response.json() as { data: { token: string; tokenPrefix: string; credentialId: string; reenroll: string[] } }).data;
      expect(data).toMatchObject({ token: NEW_TOKEN, tokenPrefix: 'newpfx', credentialId: NEW_CREDENTIAL });
      expect(data.reenroll.join(' ')).toContain('sudo aictl cloud enroll');

      expect(credentials.createCredential).toHaveBeenCalledTimes(1);
      expect(credentials.createCredential.mock.calls[0]![1]).toEqual({ name: 'localai-ai-n2' });
      expect(prismaMock.localaiNode.update).toHaveBeenCalledWith({ where: { id: NODE_ROW }, data: { nodeCredentialId: NEW_CREDENTIAL } });
      expect(credentials.revokeAnyCredential).toHaveBeenCalledWith(OLD_CREDENTIAL);

      expect(audits('localai.node.credential_minted')[0]).toMatchObject({ targetId: 'ai-n2', meta: { credentialId: NEW_CREDENTIAL, tokenPrefix: 'newpfx' } });
      expect(audits('localai.node.credential_revoked')[0]).toMatchObject({ targetId: 'ai-n2', meta: { credentialId: OLD_CREDENTIAL, tokenPrefix: 'oldpfx' } });
      expect(JSON.stringify(prismaMock.auditEvent.create.mock.calls)).not.toContain(NEW_TOKEN);
    });

    it('works for a node with no credential yet (nothing to revoke) and the detail never carries the token afterwards', async () => {
      row = nodeRow({ nodeCredentialId: null });
      expect((await send('POST', '/ai-n2/credential', as('admin').accessToken)).statusCode).toBe(201);
      expect(credentials.revokeAnyCredential).not.toHaveBeenCalled();
      expect(audits('localai.node.credential_revoked')).toHaveLength(0);

      row = nodeRow({ nodeCredentialId: NEW_CREDENTIAL });
      prismaMock.nodeCredential.findUnique.mockResolvedValue({ id: NEW_CREDENTIAL, tokenPrefix: 'newpfx', lastUsedAt: null, createdAt: new Date(), revokedAt: null });
      const detail = await send('GET', '/ai-n2', as('admin').accessToken);
      expect(detail.statusCode).toBe(200);
      expect(detail.body).not.toContain(NEW_TOKEN);
      expect((detail.json() as { data: { keys: { credential: { prefix: string } } } }).data.keys.credential.prefix).toBe('newpfx');
    });

    it('a revoke that finds the old credential already revoked is not an error; a retired node is refused', async () => {
      credentials.revokeAnyCredential.mockRejectedValue(new NotFoundException());
      expect((await send('POST', '/ai-n2/credential', as('admin').accessToken)).statusCode).toBe(201);
      row = nodeRow({ phase: 'retired' });
      credentials.createCredential.mockClear();
      expect((await send('POST', '/ai-n2/credential', as('admin').accessToken)).statusCode).toBe(409);
      expect(credentials.createCredential).not.toHaveBeenCalled();
    });
  });

  describe('POST /api/localai/nodes/:nodeId/retire', () => {
    it('with the wrong confirmation: 400, nothing changes', async () => {
      for (const confirm of ['', 'ai-n3', 'AI-N2', 'ai-n2 ']) {
        const response = await send('POST', '/ai-n2/retire', as('admin').accessToken, { confirm });
        expect(response.statusCode).toBe(400);
      }
      expect((await send('POST', '/ai-n2/retire', as('admin').accessToken, {})).statusCode).toBe(400);
      expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
      expect(prismaMock.localaiCommand.updateMany).not.toHaveBeenCalled();
      expect(credentials.revokeAnyCredential).not.toHaveBeenCalled();
      expect(keys.deleteKey).not.toHaveBeenCalled();
      expect(audits('localai.node.retired')).toHaveLength(0);
    });

    it('retires: cancels the queued commands, sets the phase, turns the gateway off, revokes the credential, deletes the key, audits', async () => {
      prismaMock.localaiCommand.updateMany.mockResolvedValue({ count: 2 });
      const response = await send('POST', '/ai-n2/retire', as('admin').accessToken, { confirm: 'ai-n2' });
      expect(response.statusCode).toBe(200);
      expect((response.json() as { data: unknown }).data).toEqual({ nodeId: 'ai-n2', phase: 'retired', cancelledCommands: 2 });

      expect(prismaMock.localaiCommand.updateMany).toHaveBeenCalledWith({ where: { nodeId: NODE_ROW, status: 'queued' }, data: { status: 'cancelled', finishedAt: expect.any(Date) } });
      expect(prismaMock.localaiNode.update).toHaveBeenCalledWith({
        where: { id: NODE_ROW },
        data: { phase: 'retired', gatewayEnabled: false, retiredAt: expect.any(Date), nodeCredentialId: null },
      });
      expect(credentials.revokeAnyCredential).toHaveBeenCalledWith(OLD_CREDENTIAL);
      expect(keys.deleteKey).toHaveBeenCalledWith('ai-n2');
      expect(audits('localai.node.retired')[0]).toMatchObject({ targetId: 'ai-n2', meta: { cancelledCommands: 2 } });
      expect(audits('localai.node.credential_revoked')).toHaveLength(1);
    });

    it('404 for an unknown node; a second retire of the same node is 409', async () => {
      expect((await send('POST', '/ai-n9/retire', as('admin').accessToken, { confirm: 'ai-n9' })).statusCode).toBe(404);
      row = nodeRow({ phase: 'retired' });
      expect((await send('POST', '/ai-n2/retire', as('admin').accessToken, { confirm: 'ai-n2' })).statusCode).toBe(409);
    });
  });

  describe('GET /api/localai/nodes/:nodeId?reveal=tailnet', () => {
    it('an admin who reveals writes exactly one localai.node.tailnet_revealed row with empty meta, and the IP is in that answer only', async () => {
      const plain = await send('GET', '/ai-n2', as('admin').accessToken);
      expect(plain.body).not.toContain(TAILNET_IP);
      expect((plain.json() as { data: { settings: { tailnetIpSet: boolean } } }).data.settings.tailnetIpSet).toBe(true);
      expect(audits('localai.node.tailnet_revealed')).toHaveLength(0);

      const admin = as('admin');
      const revealed = await send('GET', '/ai-n2?reveal=tailnet', admin.accessToken);
      expect((revealed.json() as { data: { tailnetIp: string } }).data.tailnetIp).toBe(TAILNET_IP);
      const rows = audits('localai.node.tailnet_revealed');
      expect(rows).toHaveLength(1);
      expect(rows[0]).toMatchObject({ targetType: 'localai_node', targetId: 'ai-n2', meta: {} });
      expect(JSON.stringify(rows[0])).not.toContain(TAILNET_IP);
    });

    it('a viewer or contributor asking to reveal gets no IP and writes no audit row', async () => {
      for (const role of ['viewer', 'contributor'] as const) {
        const response = await send('GET', '/ai-n2?reveal=tailnet', as(role).accessToken);
        expect(response.statusCode).toBe(200);
        expect(response.body).not.toContain(TAILNET_IP);
      }
      expect(audits('localai.node.tailnet_revealed')).toHaveLength(0);
    });

    it('when the audit cannot be written the address is not returned', async () => {
      prismaMock.auditEvent.create.mockRejectedValue(new Error('audit down'));
      const response = await send('GET', '/ai-n2?reveal=tailnet', as('admin').accessToken);
      expect(response.statusCode).toBe(500);
      expect(response.body).not.toContain(TAILNET_IP);
    });
  });

  describe('the detail payload for the tabs', () => {
    it('carries settings, key hint, credential prefix, the accepted command types and the planned model sizes, for a viewer too', async () => {
      const response = await send('GET', '/ai-n2', as('viewer').accessToken);
      expect(response.statusCode).toBe(200);
      const { data } = response.json() as { data: Record<string, any> }; // eslint-disable-line @typescript-eslint/no-explicit-any
      expect(data.settings).toMatchObject({ publicHost: 'ai-n2.example.test', servePattern: 'B', tailnetMachine: 'machine-two', tailnetIpSet: true, gatewayEnabled: true, gatewayPriority: 100, catalogPin: null });
      expect(data.keys).toMatchObject({ keyHint: 'ab12…9f0e', credential: { prefix: 'oldpfx', lastUsedAt: '2026-11-03T11:59:00.000Z' } });
      expect(data.acceptedTypes).toEqual(['model.load', 'status.refresh']);
      expect(data.modelSizes).toEqual({ 'model-one-q4': 123 });
      expect(response.body).not.toContain('tokenHash');
    });

    it('acceptedTypes is null when the node has no worker row (unknown, not "none")', async () => {
      row = nodeRow({ workerNodeId: null });
      const { data } = (await send('GET', '/ai-n2', as('viewer').accessToken)).json() as { data: { acceptedTypes: unknown } };
      expect(data.acceptedTypes).toBeNull();
    });
  });
});
