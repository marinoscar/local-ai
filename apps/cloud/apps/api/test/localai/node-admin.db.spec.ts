// The node admin routes of the detail page against real SQL (CLOUD-12): the retire transaction (queued commands
// cancelled, running ones untouched, phase and credential binding, the revoked credential row), credential replacement
// (the old token stops authenticating, the new one does, the binding moves), PATCH persistence and its audit row, and the
// reveal audit. The permission matrix and the audit meta over a mocked database are `node-admin-routes.spec.ts`.
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';

const IDS = ['ai-n9211', 'ai-n9212', 'ai-n9213'] as const;
const run = `${Date.now()}`;
// A made-up value in the shape of a tailnet address, built from parts.
const TAILNET_IP = [100, 91, 5, 6].join('.');
const NEW_IP = [100, 92, 7, 8].join('.');

let app: NestFastifyApplication;
let prisma: PrismaService;
let credentials: NodeCredentialService;
let adminToken: string;
let viewerToken: string;
let adminId: string;

async function login(role: 'admin' | 'viewer', label: string): Promise<{ token: string; id: string }> {
  const email = `nodeadmin-${label}-${run}@example.test`;
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role } });
  expect(response.statusCode).toBeLessThan(400);
  const token = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  const user = await prisma.user.findFirstOrThrow({ where: { email } });
  return { token, id: user.id };
}

async function cleanup(): Promise<void> {
  await prisma.auditEvent.deleteMany({ where: { targetId: { in: [...IDS] } } });
  await prisma.localaiNode.deleteMany({ where: { nodeId: { in: [...IDS] } } });
  await prisma.nodeCredential.deleteMany({ where: { name: { in: IDS.map((id) => `localai-${id}`) } } });
  await prisma.nodeCredential.deleteMany({ where: { name: { startsWith: `nodeadmin-${run}` } } });
}

async function makeNode(nodeId: (typeof IDS)[number], withCredential = true) {
  const credential = withCredential ? await credentials.createCredential(adminId, { name: `nodeadmin-${run}-${nodeId}` }) : null;
  const node = await prisma.localaiNode.create({
    data: {
      nodeId,
      displayName: `Node ${nodeId}`,
      publicHost: `${nodeId}.example.test`,
      servePattern: 'B',
      tailnetIp: TAILNET_IP,
      phase: 'enrolled',
      mode: 'online',
      keyHint: 'ab12…9f0e',
      keyReportedAt: new Date(),
      nodeCredentialId: credential?.id ?? null,
    },
  });
  return { node, credential };
}

const send = (method: 'GET' | 'PATCH' | 'POST', url: string, token: string, payload?: unknown) =>
  app.inject({ method, url: `/api/localai/nodes${url}`, headers: { authorization: `Bearer ${token}` }, payload: payload as never });

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  credentials = app.get(NodeCredentialService);
  const admin = await login('admin', 'admin');
  adminToken = admin.token;
  adminId = admin.id;
  viewerToken = (await login('viewer', 'viewer')).token;
  await cleanup();
});

afterAll(async () => {
  if (prisma) {
    await cleanup();
    await prisma.user.deleteMany({ where: { email: { in: [`nodeadmin-admin-${run}@example.test`, `nodeadmin-viewer-${run}@example.test`] } } }).catch(() => undefined);
  }
  await app?.close();
});

describe('PATCH /api/localai/nodes/:nodeId (real SQL)', () => {
  it('persists the changes, audits them with the tailnet IP as <set>, and a viewer is refused', async () => {
    await makeNode(IDS[0], false);
    expect((await send('PATCH', `/${IDS[0]}`, viewerToken, { displayName: 'Nope' })).statusCode).toBe(403);
    const response = await send('PATCH', `/${IDS[0]}`, adminToken, { displayName: 'Renamed', servePattern: 'none', gatewayPriority: 7, gatewayEnabled: false, tailnetIp: NEW_IP, catalogPin: '2026.11.02-1', tailnetMachine: '' });
    expect(response.statusCode).toBe(200);
    expect(response.body).not.toContain(NEW_IP);
    expect(response.body).not.toContain(TAILNET_IP);
    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId: IDS[0] } });
    expect(row).toMatchObject({ displayName: 'Renamed', servePattern: 'none', gatewayPriority: 7, gatewayEnabled: false, tailnetIp: NEW_IP, catalogPin: '2026.11.02-1', tailnetMachine: null });
    const audit = await prisma.auditEvent.findMany({ where: { action: 'localai.node.updated', targetId: IDS[0] } });
    expect(audit).toHaveLength(1);
    expect(audit[0]!.actorUserId).toBe(adminId);
    expect(audit[0]!.meta).toMatchObject({ displayName: 'Renamed', servePattern: 'none', tailnetIp: '<set>', gatewayPriority: 7 });
    expect(JSON.stringify(audit[0]!.meta)).not.toContain(NEW_IP);
  });
});

describe('the tailnet reveal (real SQL)', () => {
  it('writes exactly one audit row per reveal, only for an admin, and the address is in no other answer', async () => {
    await send('GET', `/${IDS[0]}`, adminToken);
    await send('GET', `/${IDS[0]}?reveal=tailnet`, viewerToken);
    expect(await prisma.auditEvent.count({ where: { action: 'localai.node.tailnet_revealed', targetId: IDS[0] } })).toBe(0);
    const revealed = await send('GET', `/${IDS[0]}?reveal=tailnet`, adminToken);
    expect((revealed.json() as { data: { tailnetIp: string } }).data.tailnetIp).toBe(NEW_IP);
    const rows = await prisma.auditEvent.findMany({ where: { action: 'localai.node.tailnet_revealed', targetId: IDS[0] } });
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ targetType: 'localai_node', actorUserId: adminId });
    expect(rows[0]!.meta ?? {}).toEqual({});
  });
});

describe('POST /api/localai/nodes/:nodeId/credential (real SQL)', () => {
  it('the old credential stops authenticating, the new one does, and the binding moves', async () => {
    const { node, credential: old } = await makeNode(IDS[1]);
    expect(await credentials.validateToken(old!.token)).not.toBeNull();
    const response = await send('POST', `/${IDS[1]}/credential`, adminToken);
    expect(response.statusCode).toBe(201);
    const data = (response.json() as { data: { token: string; credentialId: string; tokenPrefix: string } }).data;
    expect(data.token).toMatch(/^nod_/);
    expect(data.token).not.toBe(old!.token);
    expect(await credentials.validateToken(old!.token)).toBeNull();
    expect(await credentials.validateToken(data.token)).not.toBeNull();
    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { id: node.id } });
    expect(row.nodeCredentialId).toBe(data.credentialId);
    expect((await prisma.nodeCredential.findUniqueOrThrow({ where: { id: old!.id } })).revokedAt).not.toBeNull();
    // The detail shows the prefix and last use, never the token or its hash.
    const detail = await send('GET', `/${IDS[1]}`, viewerToken);
    expect(detail.body).not.toContain(data.token);
    expect((detail.json() as { data: { keys: { credential: { prefix: string } } } }).data.keys.credential.prefix).toBe(data.tokenPrefix);
    const minted = await prisma.auditEvent.findMany({ where: { action: 'localai.node.credential_minted', targetId: IDS[1] } });
    expect(minted).toHaveLength(1);
    expect(JSON.stringify(minted[0]!.meta)).not.toContain(data.token);
    expect(await prisma.auditEvent.count({ where: { action: 'localai.node.credential_revoked', targetId: IDS[1] } })).toBe(1);
  });
});

describe('POST /api/localai/nodes/:nodeId/retire (real SQL)', () => {
  it('a wrong confirmation changes nothing; the right one retires, cancels queued commands only and revokes the credential', async () => {
    const { node, credential } = await makeNode(IDS[2]);
    const future = new Date(Date.now() + 600_000);
    const queued = await prisma.localaiCommand.create({ data: { nodeId: node.id, type: 'localai.status.refresh', params: {}, status: 'queued', dedupKey: 'status.refresh:-', expiresAt: future } });
    const running = await prisma.localaiCommand.create({ data: { nodeId: node.id, type: 'localai.model.load', params: {}, status: 'running', dedupKey: 'model.load:model-one-q4', expiresAt: future, claimedAt: new Date(), attempts: 1 } });

    expect((await send('POST', `/${IDS[2]}/retire`, viewerToken, { confirm: IDS[2] })).statusCode).toBe(403);
    expect((await send('POST', `/${IDS[2]}/retire`, adminToken, { confirm: 'ai-n1' })).statusCode).toBe(400);
    expect((await send('POST', `/${IDS[2]}/retire`, adminToken, {})).statusCode).toBe(400);
    expect((await prisma.localaiNode.findUniqueOrThrow({ where: { id: node.id } })).phase).toBe('enrolled');
    expect((await prisma.localaiCommand.findUniqueOrThrow({ where: { id: queued.id } })).status).toBe('queued');

    const response = await send('POST', `/${IDS[2]}/retire`, adminToken, { confirm: IDS[2] });
    expect(response.statusCode).toBe(200);
    expect((response.json() as { data: unknown }).data).toEqual({ nodeId: IDS[2], phase: 'retired', cancelledCommands: 1 });

    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { id: node.id } });
    expect(row).toMatchObject({ phase: 'retired', gatewayEnabled: false, nodeCredentialId: null, keyHint: null });
    expect(row.retiredAt).not.toBeNull();
    expect(await prisma.localaiCommand.findUniqueOrThrow({ where: { id: queued.id } })).toMatchObject({ status: 'cancelled' });
    expect((await prisma.localaiCommand.findUniqueOrThrow({ where: { id: running.id } })).status).toBe('running');
    expect((await prisma.nodeCredential.findUniqueOrThrow({ where: { id: credential!.id } })).revokedAt).not.toBeNull();
    expect(await credentials.validateToken(credential!.token)).toBeNull();
    expect(await prisma.auditEvent.count({ where: { action: 'localai.node.retired', targetId: IDS[2] } })).toBe(1);

    // A retired node is gone from the lab, refuses edits, and its id is not reusable.
    expect((await send('GET', `/${IDS[2]}`, adminToken)).statusCode).toBe(404);
    expect((await send('PATCH', `/${IDS[2]}`, adminToken, { displayName: 'x' })).statusCode).toBe(409);
    expect((await send('POST', `/${IDS[2]}/retire`, adminToken, { confirm: IDS[2] })).statusCode).toBe(409);
    await expect(prisma.localaiNode.create({ data: { nodeId: IDS[2], displayName: 'again', publicHost: 'again.example.test', servePattern: 'B' } })).rejects.toThrow();
  });
});
