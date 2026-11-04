// The cloud link end to end against real SQL (docs/specs/cloud-app.md sections 3.6, 4.1, 4.2 and 9): the whole app on
// a migrated database, a `nod_` credential minted by the platform's own `NodeCredentialService`, bound to a Local AI
// node, the agent's `POST /api/nodes/register`, and the handshake. The guard's conditional writes (the enrollment
// that must happen once however many first calls race) only prove themselves on a real `UPDATE ... WHERE`.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';

const fixture = JSON.parse(readFileSync(join(__dirname, '../fixtures/localai/config.response.json'), 'utf8')) as { data: Record<string, unknown> };

const run = `${Date.now()}`;
const nodeIdOf = (n: number) => `ai-n${n}-${run}`;

let app: NestFastifyApplication;
let prisma: PrismaService;
let credentials: NodeCredentialService;
let ownerId: string;
let sessionToken: string;
const mintedCredentialIds: string[] = [];
const nodeIds: string[] = [];

const registerBody = (name: string, hostname = 'host-alpha') => ({
  name,
  hostname,
  platform: 'linux/amd64',
  cliVersion: 'aiagent/0.1.0',
  eligibleTypes: ['localai.status.refresh'],
  concurrency: 1,
  capabilities: { localai: { schema: 1, agent_version: '0.1.0' } },
});

/** Mints a credential, creates the Local AI node bound to it (what the wizard does, CLOUD-14). */
async function enrollable(n: number, phase: 'pending' | 'enrolled' | 'retired' = 'pending') {
  const nodeId = nodeIdOf(n);
  const minted = await credentials.createCredential(ownerId, { name: `${nodeId} credential` } as never);
  mintedCredentialIds.push(minted.id);
  nodeIds.push(nodeId);
  await prisma.localaiNode.create({
    data: {
      nodeId,
      displayName: `Node ${n}`,
      publicHost: `node-${n}.example.test`,
      servePattern: 'none',
      phase,
      nodeCredentialId: minted.id,
      createdById: ownerId,
    },
  });
  return { nodeId, token: minted.token, credentialId: minted.id };
}

async function register(token: string, name: string): Promise<string> {
  const response = await app.inject({
    method: 'POST',
    url: '/api/nodes/register',
    headers: { authorization: `Bearer ${token}` },
    payload: registerBody(name),
  });
  expect([200, 201]).toContain(response.statusCode);
  return (response.json() as { data: { node: { id: string } } }).data.node.id;
}

const config = (workerId: string, token: string) =>
  app.inject({ method: 'GET', url: `/api/nodes/${workerId}/localai/config`, headers: { authorization: `Bearer ${token}` } });

const auditRows = (action: string, targetId: string) => prisma.auditEvent.findMany({ where: { action, targetId } });

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  credentials = app.get(NodeCredentialService);
  const email = `node-link-${run}@example.test`;
  const login = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role: 'admin' } });
  sessionToken = new URL(String(login.headers.location)).searchParams.get('token') ?? '';
  ownerId = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
});

afterAll(async () => {
  if (prisma) {
    await prisma.auditEvent.deleteMany({ where: { targetId: { in: nodeIds }, action: { startsWith: 'localai.node.' } } });
    await prisma.localaiNode.deleteMany({ where: { nodeId: { in: nodeIds } } });
    await prisma.workerNode.deleteMany({ where: { createdById: ownerId } });
    await prisma.nodeCredential.deleteMany({ where: { id: { in: mintedCredentialIds } } });
  }
  await app?.close();
});

describe('the cloud link on real SQL', () => {
  it('mint, bind, register, handshake: 200 with the fixture shape, and the node is enrolled', async () => {
    const { nodeId, token } = await enrollable(1);
    const workerId = await register(token, nodeId);

    const response = await config(workerId, token);
    expect(response.statusCode).toBe(200);
    const body = response.json() as { data: Record<string, unknown>; meta: Record<string, unknown> };
    expect({ ...body.data, nodeId: 'ai-n3', keyReported: true, serverTime: fixture.data.serverTime }).toEqual(fixture.data);
    expect(body.data.nodeId).toBe(nodeId);
    expect(body.data.keyReported).toBe(false);
    expect(typeof body.meta.timestamp).toBe('string');

    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    expect(row).toMatchObject({ phase: 'enrolled', workerNodeId: workerId });
    const enrolled = await auditRows('localai.node.enrolled', nodeId);
    expect(enrolled).toHaveLength(1);
    expect(enrolled[0]).toMatchObject({ actorUserId: ownerId, targetType: 'localai_node', meta: { workerNodeId: workerId, agentVersion: 'aiagent/0.1.0' } });

    // A second call changes nothing.
    expect((await config(workerId, token)).statusCode).toBe(200);
    expect(await auditRows('localai.node.enrolled', nodeId)).toHaveLength(1);
  });

  it('two concurrent first calls produce one enrollment and one audit row', async () => {
    const { nodeId, token } = await enrollable(2);
    const workerId = await register(token, nodeId);
    const responses = await Promise.all(Array.from({ length: 6 }, () => config(workerId, token)));
    expect(responses.map((r) => r.statusCode)).toEqual([200, 200, 200, 200, 200, 200]);
    expect(await auditRows('localai.node.enrolled', nodeId)).toHaveLength(1);
    expect((await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } })).phase).toBe('enrolled');
  });

  it("refuses the credential of one node on another's path with 403 LAI-CLD-004 and one audit row, leaving the victim untouched", async () => {
    const first = await enrollable(3);
    const second = await enrollable(4);
    const firstWorker = await register(first.token, first.nodeId);
    const secondWorker = await register(second.token, second.nodeId);

    const response = await config(secondWorker, first.token);
    expect(response.statusCode).toBe(403);
    expect((response.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-004', reason: 'credential_bound_to_other_node' });
    expect(response.body).not.toContain(first.token);

    const refused = await auditRows('localai.node.binding_refused', second.nodeId);
    expect(refused).toHaveLength(1);
    expect(refused[0].meta).toEqual({ credentialPrefix: first.token.slice(0, 8), claimedNodeId: second.nodeId });
    // The victim never enrolled, and each node still works with its own credential.
    expect(await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId: second.nodeId } })).toMatchObject({ phase: 'pending', workerNodeId: null });
    expect((await config(secondWorker, second.token)).statusCode).toBe(200);
    expect((await config(firstWorker, first.token)).statusCode).toBe(200);
  });

  it('re-links the worker node id after EAB pruned it and the node registered again', async () => {
    const { nodeId, token } = await enrollable(5);
    const oldWorker = await register(token, nodeId);
    expect((await config(oldWorker, token)).statusCode).toBe(200);

    await prisma.workerNode.delete({ where: { id: oldWorker } });
    expect((await config(oldWorker, token)).statusCode).toBe(404);

    const newWorker = await register(token, nodeId);
    expect(newWorker).not.toBe(oldWorker);
    expect((await config(newWorker, token)).statusCode).toBe(200);
    expect(await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } })).toMatchObject({ phase: 'enrolled', workerNodeId: newWorker });
    expect(await auditRows('localai.node.enrolled', nodeId)).toHaveLength(1);
  });

  it('refuses a retired node (LAI-CLD-035) and a revoked credential (401)', async () => {
    const retired = await enrollable(6);
    const retiredWorker = await register(retired.token, retired.nodeId);
    await prisma.localaiNode.update({ where: { nodeId: retired.nodeId }, data: { phase: 'retired' } });
    const response = await config(retiredWorker, retired.token);
    expect(response.statusCode).toBe(403);
    expect((response.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-035', reason: 'node_retired' });

    const revoked = await enrollable(7);
    const revokedWorker = await register(revoked.token, revoked.nodeId);
    await credentials.revokeCredential(ownerId, revoked.credentialId);
    expect((await config(revokedWorker, revoked.token)).statusCode).toBe(401);
  });

  it('answers 404 unknown_localai_node for a worker whose name is not a Local AI node, and 403 to a session', async () => {
    const { token } = await enrollable(8);
    const stranger = await register(token, `not-a-lab-node-${run}`);
    const response = await config(stranger, token);
    expect(response.statusCode).toBe(404);
    expect((response.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-004', reason: 'unknown_localai_node' });

    const asSession = await app.inject({ method: 'GET', url: `/api/nodes/${stranger}/localai/config`, headers: { authorization: `Bearer ${sessionToken}` } });
    expect(asSession.statusCode).toBe(403);
    expect((asSession.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-004', reason: 'node_credential_required' });
  });
});
