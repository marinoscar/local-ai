// The node llama keys on real SQL (docs/specs/cloud-app.md sections 3.6, 3.8, 3.9 and 9): the whole app on a migrated
// database, a `nod_` credential minted by the platform's own `NodeCredentialService`, the agent's `register`, and
// `POST /api/nodes/{id}/localai/key`. What only real SQL proves: the stored value is ciphertext in `credentials`, the
// round trip through the real cipher, the audit rows, and the "Ask all nodes to re-send keys" action against the
// partial unique index `localai_commands_active_dedup`.
import { randomBytes } from 'node:crypto';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

// A throwaway key for the cipher, generated per run and never committed. The CI database has none configured.
process.env.SECRETS_ENCRYPTION_KEY ??= randomBytes(32).toString('base64');

import { fingerprintOf } from '../../src/localai/contracts/key.schema';
import { LOCALAI_NODE_KEY_REPORTED_EVENT } from '../../src/localai/keys/node-key.events';
import { NodeKeyService, NodeKeyUnreadableError } from '../../src/localai/keys/node-key.service';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { captureOutput, enableNestLogging, HEX64, testKey } from './support/capture-output';

const PURPOSE = 'localai_node_llama_key';
const run = `${Date.now()}`;
const nodeIdOf = (n: number) => `ai-n${n}-${run}`;

let app: NestFastifyApplication;
let prisma: PrismaService;
let credentials: NodeCredentialService;
let keys: NodeKeyService;
let bus: EventBus;
let ownerId: string;
let sessionToken: string;
const mintedCredentialIds: string[] = [];
const nodeIds: string[] = [];

/** Mints a credential and creates the Local AI node bound to it (what the wizard does, CLOUD-14). */
async function enrollable(n: number, phase: 'pending' | 'enrolled' | 'retired' = 'pending') {
  const nodeId = nodeIdOf(n);
  const minted = await credentials.createCredential(ownerId, { name: `${nodeId} credential` } as never);
  mintedCredentialIds.push(minted.id);
  nodeIds.push(nodeId);
  await prisma.localaiNode.create({
    data: { nodeId, displayName: `Node ${n}`, publicHost: `node-${n}.example.test`, servePattern: 'none', phase, nodeCredentialId: minted.id, createdById: ownerId },
  });
  return { nodeId, token: minted.token };
}

async function register(token: string, name: string): Promise<string> {
  const response = await app.inject({
    method: 'POST',
    url: '/api/nodes/register',
    headers: { authorization: `Bearer ${token}` },
    payload: {
      name,
      hostname: 'host-alpha',
      platform: 'linux/amd64',
      cliVersion: 'aiagent/0.1.0',
      eligibleTypes: ['localai.status.refresh'],
      concurrency: 1,
      capabilities: { localai: { schema: 1, agent_version: '0.1.0' } },
    },
  });
  expect([200, 201]).toContain(response.statusCode);
  return (response.json() as { data: { node: { id: string } } }).data.node.id;
}

const bodyFor = (key: string) => ({ key, fingerprint: fingerprintOf(key), rotated_at: '2026-11-02T15:04:05Z' });

const postKey = (workerId: string, token: string, payload: unknown) =>
  app.inject({ method: 'POST', url: `/api/nodes/${workerId}/localai/key`, headers: { authorization: `Bearer ${token}` }, payload: payload as never });

const handshake = (workerId: string, token: string) =>
  app.inject({ method: 'GET', url: `/api/nodes/${workerId}/localai/config`, headers: { authorization: `Bearer ${token}` } });

const keyAudit = (nodeId: string) => prisma.auditEvent.findMany({ where: { action: 'localai.node.key_reported', targetId: nodeId }, orderBy: { createdAt: 'asc' } });

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  credentials = app.get(NodeCredentialService);
  keys = app.get(NodeKeyService);
  bus = app.get<EventBus>(EVENT_BUS);
  const email = `node-key-${run}@example.test`;
  const login = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role: 'admin' } });
  sessionToken = new URL(String(login.headers.location)).searchParams.get('token') ?? '';
  ownerId = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
});

afterAll(async () => {
  if (prisma) {
    await prisma.credential.deleteMany({ where: { purpose: PURPOSE, name: { in: nodeIds } } });
    await prisma.auditEvent.deleteMany({ where: { targetId: { in: nodeIds }, action: { startsWith: 'localai.node.' } } });
    await prisma.localaiNode.deleteMany({ where: { nodeId: { in: nodeIds } } }); // cascades to localai_commands
    await prisma.workerNode.deleteMany({ where: { createdById: ownerId } });
    await prisma.nodeCredential.deleteMany({ where: { id: { in: mintedCredentialIds } } });
  }
  await app?.close();
});

describe('node llama keys on real SQL', () => {
  it('POST key stores ciphertext, round-trips, updates the node row, audits enroll, rotate and refresh, and publishes the event', async () => {
    const restoreLogger = enableNestLogging();
    const capture = captureOutput();
    const events: unknown[] = [];
    const unsubscribe = bus.subscribe(LOCALAI_NODE_KEY_REPORTED_EVENT, (payload) => {
      events.push(payload);
    });
    try {
      const { nodeId, token } = await enrollable(1);
      const workerId = await register(token, nodeId);
      expect(((await handshake(workerId, token)).json() as { data: { keyReported: boolean } }).data.keyReported).toBe(false);

      const first = testKey();
      const response = await postKey(workerId, token, bodyFor(first));
      expect(response.statusCode).toBe(200);
      const body = response.json() as { data: { keyHint: string; storedAt: string } };
      expect(body.data.keyHint).toBe(fingerprintOf(first));
      expect(response.body).not.toContain(first);

      // At rest: a ciphertext under the purpose and the node id.
      const stored = await prisma.credential.findUniqueOrThrow({ where: { purpose_name: { purpose: PURPOSE, name: nodeId } } });
      expect(stored.secret).not.toContain(first);
      expect(JSON.stringify(stored)).not.toContain(first);
      // And back through the one server-side reader.
      expect(await keys.getKey(nodeId)).toBe(first);

      const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
      expect(row.keyHint).toBe(fingerprintOf(first));
      expect(row.keyReportedAt).toBeInstanceOf(Date);
      expect(Math.abs(row.keyReportedAt!.getTime() - Date.parse(body.data.storedAt))).toBeLessThan(1000);
      expect(((await handshake(workerId, token)).json() as { data: { keyReported: boolean } }).data.keyReported).toBe(true);

      // Same key again: refresh. A different key: rotate, and the reader sees it at once (cache evicted).
      expect((await postKey(workerId, token, bodyFor(first))).statusCode).toBe(200);
      const second = testKey();
      expect((await postKey(workerId, token, bodyFor(second))).statusCode).toBe(200);
      expect(await keys.getKey(nodeId)).toBe(second);

      const audited = await keyAudit(nodeId);
      expect(audited.map((a) => (a.meta as { reason: string }).reason)).toEqual(['enroll', 'refresh', 'rotate']);
      expect(audited[0]).toMatchObject({ actorUserId: ownerId, targetType: 'localai_node' });
      expect(audited[2].meta).toEqual({ keyHint: fingerprintOf(second), reason: 'rotate' });
      expect(JSON.stringify(audited)).not.toContain(first);
      expect(JSON.stringify(audited)).not.toContain(second);

      expect(events).toEqual([
        { nodeId, keyHint: fingerprintOf(first), reason: 'enroll' },
        { nodeId, keyHint: fingerprintOf(first), reason: 'refresh' },
        { nodeId, keyHint: fingerprintOf(second), reason: 'rotate' },
      ]);

      // The whole flow, on every log sink: no 64-hex string.
      expect(capture.text()).not.toMatch(HEX64);
      expect(capture.text()).toContain(`${PURPOSE}/${nodeId}`);
    } finally {
      unsubscribe();
      capture.restore();
      restoreLogger();
    }
  });

  it('refuses a bad key report with 400 and stores nothing', async () => {
    const { nodeId, token } = await enrollable(2);
    const workerId = await register(token, nodeId);
    const key = testKey();
    for (const payload of [
      { ...bodyFor(key), fingerprint: fingerprintOf(testKey()) },
      bodyFor(key.slice(1)),
      { ...bodyFor(key), extra: 1 },
    ]) {
      const response = await postKey(workerId, token, payload);
      expect(response.statusCode).toBe(400);
      expect(response.body).not.toContain(key);
    }
    expect(await prisma.credential.count({ where: { purpose: PURPOSE, name: nodeId } })).toBe(0);
    expect((await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } })).keyHint).toBeNull();
    expect(await keyAudit(nodeId)).toHaveLength(0);
  });

  it("refuses another node's credential (403) and stores nothing for either node", async () => {
    const first = await enrollable(3);
    const second = await enrollable(4);
    await register(first.token, first.nodeId);
    const secondWorker = await register(second.token, second.nodeId);
    const key = testKey();
    const response = await postKey(secondWorker, first.token, bodyFor(key));
    expect(response.statusCode).toBe(403);
    expect(response.body).not.toContain(key);
    expect(await prisma.credential.count({ where: { purpose: PURPOSE, name: { in: [first.nodeId, second.nodeId] } } })).toBe(0);
  });

  it('deleteKey removes the secret and clears the node row (the retire path); getKey then answers null', async () => {
    const { nodeId, token } = await enrollable(5);
    const workerId = await register(token, nodeId);
    const key = testKey();
    expect((await postKey(workerId, token, bodyFor(key))).statusCode).toBe(200);
    expect(await keys.getKey(nodeId)).toBe(key);

    await keys.deleteKey(nodeId);
    expect(await prisma.credential.count({ where: { purpose: PURPOSE, name: nodeId } })).toBe(0);
    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    expect(row.keyHint).toBeNull();
    expect(row.keyReportedAt).toBeNull();
    expect(await keys.getKey(nodeId)).toBeNull();
    await expect(keys.deleteKey(nodeId)).resolves.toBeUndefined();
  });

  it('a stored key that cannot be decrypted reads as NodeKeyUnreadableError, never as "no key"', async () => {
    const { nodeId, token } = await enrollable(6);
    const workerId = await register(token, nodeId);
    expect((await postKey(workerId, token, bodyFor(testKey()))).statusCode).toBe(200);
    keys.evict(nodeId);
    // What a lost or changed SECRETS_ENCRYPTION_KEY looks like to the reader: a payload that does not authenticate.
    await prisma.credential.update({ where: { purpose_name: { purpose: PURPOSE, name: nodeId } }, data: { secret: 'v1:not-a-valid-payload' } });
    await expect(keys.getKey(nodeId)).rejects.toBeInstanceOf(NodeKeyUnreadableError);
  });

  describe('POST /api/localai/nodes/resend-keys', () => {
    const resend = () =>
      app.inject({ method: 'POST', url: '/api/localai/nodes/resend-keys', headers: { authorization: `Bearer ${sessionToken}` }, payload: {} as never });

    it('queues one status.refresh per enrolled, non-retired node, reuses an active one, and queues again once it settled', async () => {
      const enrolled = await enrollable(7, 'enrolled');
      const alsoEnrolled = await enrollable(8, 'enrolled');
      const pending = await enrollable(9, 'pending');
      const retired = await enrollable(10, 'retired');
      const mine = [enrolled.nodeId, alsoEnrolled.nodeId, pending.nodeId, retired.nodeId];
      const commandsOf = (...ids: string[]) =>
        prisma.localaiCommand.findMany({ where: { node: { nodeId: { in: ids } } }, include: { node: { select: { nodeId: true } } }, orderBy: { createdAt: 'asc' } });

      const first = await resend();
      expect(first.statusCode).toBe(200);
      const firstIds = (first.json() as { data: { commandIds: string[] } }).data.commandIds;
      const queued = await commandsOf(...mine);
      expect(queued.map((c) => c.node.nodeId).sort()).toEqual([enrolled.nodeId, alsoEnrolled.nodeId].sort());
      for (const command of queued) {
        expect(command).toMatchObject({ type: 'localai.status.refresh', status: 'queued', source: 'system', requestedById: ownerId, dedupKey: 'status.refresh:-' });
        expect(firstIds).toContain(command.id);
        expect(command.expiresAt.getTime() - command.createdAt.getTime()).toBeGreaterThanOrEqual(2 * 60_000 - 5000);
      }

      // Pressed twice: the active commands are returned, nothing new is queued.
      const again = await resend();
      expect(again.statusCode).toBe(200);
      expect((again.json() as { data: { commandIds: string[] } }).data.commandIds).toEqual(expect.arrayContaining(queued.map((c) => c.id)));
      expect(await commandsOf(...mine)).toHaveLength(2);

      // Once one settled it is no longer active, so the next press queues a fresh one for that node.
      await prisma.localaiCommand.update({ where: { id: queued[0].id }, data: { status: 'succeeded', finishedAt: new Date() } });
      expect((await resend()).statusCode).toBe(200);
      const after = await commandsOf(...mine);
      expect(after).toHaveLength(3);
      expect(after.filter((c) => c.status === 'queued')).toHaveLength(2);
    });

    it('is refused without a lab:admin session', async () => {
      const response = await app.inject({ method: 'POST', url: '/api/localai/nodes/resend-keys', payload: {} as never });
      expect(response.statusCode).toBe(401);
    });
  });
});
