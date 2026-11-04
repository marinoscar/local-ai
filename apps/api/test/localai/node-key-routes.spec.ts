// `POST /api/nodes/{id}/localai/key` and `POST /api/localai/nodes/resend-keys` over HTTP, on the whole AppModule over a
// mocked database (no PostgreSQL): the real guards, the real `CredentialsService` and cipher (over an in-memory
// `credentials` table, so what is stored really is ciphertext), the real audit sink. The same flow against real SQL is
// `node-link.db.spec.ts`.
//
// Several tests here are TRIPWIRES for the security rule of docs/specs/cloud-app.md section 6: whatever the request
// does, the key appears in no response body, no error and no log line.
import { randomBytes } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import fastifyCookie from '@fastify/cookie';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { JobWorker } from '@marinoscar/platform-api/jobs';

// A throwaway key for the cipher, generated per run and never committed. Read lazily by the cipher.
process.env.SECRETS_ENCRYPTION_KEY ??= randomBytes(32).toString('base64');

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { nodeTokenDigest } from '../../src/localai/node-link/binding.guard';
import { NodeKeyService } from '../../src/localai/keys/node-key.service';
import { fingerprintOf } from '../../src/localai/contracts/key.schema';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';
import { captureOutput, enableNestLogging, HEX64, testKey } from './support/capture-output';

const requestFixture = JSON.parse(readFileSync(join(__dirname, '../fixtures/localai/key.request.json'), 'utf8')) as Record<string, string>;
const responseFixture = JSON.parse(readFileSync(join(__dirname, '../fixtures/localai/key.response.json'), 'utf8')) as { data: object; meta: object };

const WORKER = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3';
const CREDENTIAL = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc3';
const OTHER_CREDENTIAL = 'cccccccc-cccc-4ccc-8ccc-ccccccccccc4';
// A fixed string with the `nod_` shape; not a real credential. (64 hex digits: the log tripwire would catch it too.)
const NODE_TOKEN = `nod_${'3'.repeat(64)}`;

describe('Node key routes over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let keys: NodeKeyService;
  /** The in-memory `credentials` table: address -> the stored row. */
  let credentialRows: Map<string, { secret: string; hint: string | null }>;
  /** The mutable Local AI node row the mocked database serves. */
  let nodeRow: Record<string, unknown>;
  let auditRows: Array<Record<string, unknown>>;

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
    keys = moduleRef.get(NodeKeyService);
  });

  afterAll(async () => {
    clearMockUsers();
    await app?.close();
  });

  /** An owner, the node credential they hold, an enrolled node and a stateful credential table behind it. */
  async function arrange(options: { boundCredential?: string; keyHint?: string | null; role?: FixtureRole } = {}) {
    resetPrismaMock();
    installBaseMocks();
    const owner = registerMockUser(jwt, options.role ?? 'admin');
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
    nodeRow = {
      id: 'row-ai-n3',
      nodeId: 'ai-n3',
      displayName: 'Node ai-n3',
      phase: 'enrolled',
      nodeCredentialId: options.boundCredential ?? CREDENTIAL,
      workerNodeId: WORKER,
      agentVersion: null,
      keyHint: options.keyHint ?? null,
      keyReportedAt: null,
    };
    prismaMock.localaiNode.findUnique.mockImplementation(async ({ where }: { where: { nodeId?: string; id?: string } }) =>
      where.nodeId === 'ai-n3' || where.id === 'row-ai-n3' ? { ...nodeRow } : null,
    );
    prismaMock.localaiNode.update.mockImplementation(async ({ data }: { data: Record<string, unknown> }) => {
      Object.assign(nodeRow, data);
      return { ...nodeRow };
    });
    prismaMock.localaiNode.updateMany.mockResolvedValue({ count: 1 });
    // The credential table: upsert stores what the real service encrypted, findUnique/deleteMany read and drop it.
    credentialRows = new Map();
    type Address = { purpose_name: { purpose: string; name: string } };
    const addressOf = (where: Address) => `${where.purpose_name.purpose}/${where.purpose_name.name}`;
    prismaMock.credential.upsert.mockImplementation(async ({ where, create, update }: { where: Address; create: { secret: string; hint: string | null }; update: { secret: string; hint: string | null } }) => {
      credentialRows.set(addressOf(where), credentialRows.has(addressOf(where)) ? { secret: update.secret, hint: update.hint } : { secret: create.secret, hint: create.hint });
      return {};
    });
    prismaMock.credential.findUnique.mockImplementation(async ({ where }: { where: Address }) => credentialRows.get(addressOf(where)) ?? null);
    prismaMock.credential.deleteMany.mockImplementation(async ({ where }: { where: { purpose: string; name: string } }) => {
      const had = credentialRows.delete(`${where.purpose}/${where.name}`);
      return { count: had ? 1 : 0 };
    });
    auditRows = [];
    prismaMock.auditEvent.create.mockImplementation(async ({ data }: { data: Record<string, unknown> }) => {
      auditRows.push(data);
      return {};
    });
    keys.evict('ai-n3');
    return { owner, sessionToken: owner.accessToken };
  }

  const bodyFor = (key: string, overrides: Record<string, unknown> = {}) => ({ ...requestFixture, key, fingerprint: fingerprintOf(key), ...overrides });

  const postKey = (payload: unknown, authorization: string | null = `Bearer ${NODE_TOKEN}`, workerId = WORKER) =>
    app.inject({
      method: 'POST',
      url: `/api/nodes/${workerId}/localai/key`,
      headers: authorization ? { authorization } : {},
      payload: payload as never,
    });

  it('stores the key encrypted, answers 200 with the hint only, and getKey returns the same key', async () => {
    await arrange();
    const key = testKey();
    const response = await postKey(bodyFor(key));
    expect(response.statusCode).toBe(200);
    const body = response.json() as { data: { keyHint: string; storedAt: string }; meta: object };
    expect(Object.keys(body)).toEqual(Object.keys(responseFixture));
    expect(Object.keys(body.data)).toEqual(Object.keys(responseFixture.data));
    expect(body.data.keyHint).toBe(fingerprintOf(key));
    expect(Math.abs(Date.parse(body.data.storedAt) - Date.now())).toBeLessThan(60_000);
    expect(response.body).not.toContain(key);

    // At rest it is ciphertext under the purpose and the node id.
    const stored = credentialRows.get('localai_node_llama_key/ai-n3');
    expect(stored).toBeDefined();
    expect(String(stored?.secret)).not.toContain(key);
    expect(JSON.stringify(stored)).not.toContain(key);
    // And it round-trips through the one server-side reader.
    expect(await keys.getKey('ai-n3')).toBe(key);
  });

  it('updates keyHint and keyReportedAt, and writes one audit row with the reason and no key', async () => {
    await arrange();
    const key = testKey();
    expect((await postKey(bodyFor(key))).statusCode).toBe(200);
    expect(nodeRow.keyHint).toBe(fingerprintOf(key));
    expect(nodeRow.keyReportedAt).toBeInstanceOf(Date);
    const rows = auditRows.filter((row) => row.action === 'localai.node.key_reported');
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ targetType: 'localai_node', targetId: 'ai-n3' });
    expect(rows[0].meta).toEqual({ keyHint: fingerprintOf(key), reason: 'enroll' });
    expect(JSON.stringify(auditRows)).not.toContain(key);
  });

  it('derives the reason: the same key again is refresh, another key is rotate', async () => {
    await arrange();
    const first = testKey();
    await postKey(bodyFor(first));
    await postKey(bodyFor(first));
    await postKey(bodyFor(testKey()));
    const reasons = auditRows.filter((row) => row.action === 'localai.node.key_reported').map((row) => (row.meta as { reason: string }).reason);
    expect(reasons).toEqual(['enroll', 'refresh', 'rotate']);
  });

  it('a rotation replaces the cached key (the next getKey sees the new one)', async () => {
    await arrange();
    const first = testKey();
    await postKey(bodyFor(first));
    expect(await keys.getKey('ai-n3')).toBe(first);
    const second = testKey();
    await postKey(bodyFor(second));
    expect(await keys.getKey('ai-n3')).toBe(second);
  });

  describe('refusals', () => {
    const cases: Array<[string, (key: string) => unknown, number]> = [
      ['a fingerprint that does not match the key', (key) => bodyFor(key, { fingerprint: fingerprintOf(testKey()) }), 400],
      ['a 63 character key', (key) => bodyFor(key.slice(1)), 400],
      ['a non-hex key', (key) => bodyFor(key, { key: `${'g'.repeat(32)}${key.slice(32)}` }), 400],
      ['an upper-case key', (key) => bodyFor(key, { key: key.toUpperCase() }), 400],
      ['an unknown field', (key) => bodyFor(key, { extra: key }), 400],
      ['an unknown field named like the key', (key) => bodyFor(key, { [key]: 'x' }), 400],
      ['a missing rotated_at', (key) => ({ key, fingerprint: fingerprintOf(key) }), 400],
      ['an array body', (key) => [bodyFor(key)], 400],
    ];

    it.each(cases)('answers 400 for %s, stores nothing and echoes nothing', async (_name, build, status) => {
      await arrange();
      const key = testKey();
      const restoreLogger = enableNestLogging();
      const capture = captureOutput();
      try {
        const response = await postKey(build(key));
        expect(response.statusCode).toBe(status);
        expect(response.body).not.toContain(key);
        expect(response.body).not.toContain(key.toUpperCase());
        expect(response.body).not.toMatch(HEX64);
        expect(capture.text()).not.toMatch(HEX64);
      } finally {
        capture.restore();
        restoreLogger();
      }
      expect(credentialRows.size).toBe(0);
      expect(prismaMock.localaiNode.update).not.toHaveBeenCalled();
      expect(auditRows.filter((row) => row.action === 'localai.node.key_reported')).toHaveLength(0);
    });

    it('describes a validation failure by path and code only', async () => {
      await arrange();
      const key = testKey();
      const response = await postKey(bodyFor(key, { fingerprint: fingerprintOf(testKey()) }));
      expect(response.json()).toMatchObject({ statusCode: 400, details: { reason: 'fingerprint_mismatch', issues: [{ path: ['fingerprint'], code: 'fingerprint_mismatch' }] } });
      const invalid = await postKey(bodyFor(key.slice(1)));
      expect(invalid.json()).toMatchObject({ statusCode: 400, details: { reason: 'invalid', issues: [{ path: ['key'], code: 'invalid_format' }] } });
    });

    it('does not echo the key when the JSON itself is malformed', async () => {
      await arrange();
      const key = testKey();
      const capture = captureOutput();
      try {
        // A truncated body that contains the key: V8's JSON.parse error message quotes a fragment of the input.
        const response = await app.inject({
          method: 'POST',
          url: `/api/nodes/${WORKER}/localai/key`,
          headers: { authorization: `Bearer ${NODE_TOKEN}`, 'content-type': 'application/json' },
          payload: `{"key":"${key}","fingerprint":"${fingerprintOf(key)}","rotated_at":`,
        });
        expect(response.statusCode).toBe(400);
        expect(response.body).not.toContain(key);
        expect(response.body).not.toMatch(HEX64);
        expect(capture.text()).not.toMatch(HEX64);
      } finally {
        capture.restore();
      }
      expect(credentialRows.size).toBe(0);
    });

    it('does not echo a fragment of the key when the JSON breaks right after it', async () => {
      await arrange();
      const key = testKey();
      const restoreLogger = enableNestLogging();
      const capture = captureOutput();
      try {
        // V8's parse error quotes the characters around the failure, so a break right after the key would quote its tail.
        const response = await app.inject({
          method: 'POST',
          url: `/api/nodes/${WORKER}/localai/key`,
          headers: { authorization: `Bearer ${NODE_TOKEN}`, 'content-type': 'application/json' },
          payload: `{"key":"${key}"oops}`,
        });
        expect(response.statusCode).toBe(400);
        expect(response.body).not.toContain(key.slice(-8));
        expect(capture.text()).not.toContain(key.slice(-8));
      } finally {
        capture.restore();
        restoreLogger();
      }
      expect(credentialRows.size).toBe(0);
    });

    it('refuses a credential bound to another node (403) and stores nothing', async () => {
      await arrange({ boundCredential: OTHER_CREDENTIAL });
      const key = testKey();
      const capture = captureOutput();
      try {
        const response = await postKey(bodyFor(key));
        expect(response.statusCode).toBe(403);
        expect(response.body).not.toContain(key);
        expect(capture.text()).not.toContain(key);
      } finally {
        capture.restore();
      }
      expect(credentialRows.size).toBe(0);
    });

    it('answers 401 without a credential and 403 to a browser session', async () => {
      const { sessionToken } = await arrange();
      const key = testKey();
      expect((await postKey(bodyFor(key), null)).statusCode).toBe(401);
      const session = await postKey(bodyFor(key), `Bearer ${sessionToken}`);
      expect(session.statusCode).toBe(403);
      expect(session.json()).toMatchObject({ details: { code: 'LAI-CLD-004', reason: 'node_credential_required' } });
      expect(credentialRows.size).toBe(0);
    });
  });

  it('a whole report flow writes no 64-hex string to any log sink', async () => {
    await arrange();
    const restoreLogger = enableNestLogging();
    const capture = captureOutput();
    try {
      const first = testKey();
      const second = testKey();
      expect((await postKey(bodyFor(first))).statusCode).toBe(200);
      expect((await postKey(bodyFor(first))).statusCode).toBe(200);
      expect((await postKey(bodyFor(second))).statusCode).toBe(200);
      expect(await keys.getKey('ai-n3')).toBe(second);
      await keys.deleteKey('ai-n3');
      expect(await keys.getKey('ai-n3')).toBeNull();
      const logged = capture.text();
      expect(logged).not.toMatch(HEX64);
      // The store's own line names the address, which is fine; it must not name the key.
      expect(logged).toContain('localai_node_llama_key/ai-n3');
    } finally {
      capture.restore();
      restoreLogger();
    }
  });

  describe('POST /api/localai/nodes/resend-keys', () => {
    const resend = (authorization?: string) =>
      app.inject({ method: 'POST', url: '/api/localai/nodes/resend-keys', headers: authorization ? { authorization } : {}, payload: {} as never });

    it('queues one status.refresh per enrolled node for an administrator and answers 200 with the ids', async () => {
      const { sessionToken } = await arrange();
      prismaMock.localaiNode.findMany.mockResolvedValue([{ nodeId: 'ai-n1' }, { nodeId: 'ai-n3' }]);
      prismaMock.localaiNode.findUnique.mockImplementation(async ({ where }: { where: { nodeId: string } }) => ({ id: `row-${where.nodeId}` }));
      let n = 0;
      prismaMock.localaiCommand.create.mockImplementation(async () => ({ id: `cmd-${(n += 1)}` }));
      const response = await resend(`Bearer ${sessionToken}`);
      expect(response.statusCode).toBe(200);
      expect(response.json()).toMatchObject({ data: { commandIds: ['cmd-1', 'cmd-2'] } });
      expect(prismaMock.localaiNode.findMany.mock.calls[0][0].where).toEqual({ phase: 'enrolled' });
      expect(prismaMock.localaiCommand.create.mock.calls.map((call: [{ data: Record<string, unknown> }]) => call[0].data)).toEqual([
        expect.objectContaining({ nodeId: 'row-ai-n1', type: 'localai.status.refresh', source: 'system', dedupKey: 'status.refresh:-' }),
        expect.objectContaining({ nodeId: 'row-ai-n3', type: 'localai.status.refresh', source: 'system', dedupKey: 'status.refresh:-' }),
      ]);
    });

    it.each<[FixtureRole, number]>([
      ['contributor', 403],
      ['viewer', 403],
    ])('refuses a %s with %i (lab:admin only)', async (role, status) => {
      const { sessionToken } = await arrange({ role });
      expect((await resend(`Bearer ${sessionToken}`)).statusCode).toBe(status);
      expect(prismaMock.localaiCommand.create).not.toHaveBeenCalled();
    });

    it('refuses a node credential and an unauthenticated call', async () => {
      await arrange();
      expect((await resend()).statusCode).toBe(401);
      expect((await resend(`Bearer ${NODE_TOKEN}`)).statusCode).toBeGreaterThanOrEqual(401);
      expect(prismaMock.localaiCommand.create).not.toHaveBeenCalled();
    });
  });
});
