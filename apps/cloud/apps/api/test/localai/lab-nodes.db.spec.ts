// `GET /api/localai/nodes` (and the detail route) against real SQL (docs/specs/cloud-app.md sections 3.9 and 9): the
// whole app on a migrated database. What only real SQL proves: the `groupBy` over active alerts (the partial unique
// index allows one active alert per node and kind, and a lab-wide alert has a null node), the retired-node filter, the
// enum columns, and the worker heartbeat join that feeds EAB's health.
import type { NestFastifyApplication } from '@nestjs/platform-fastify';

import { LabNodesResponseSchema } from '../../src/localai/nodes/dto/node-card';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';

// Lab ids must look like ai-n<N>; a high range no real lab uses, cleaned up before and after.
const IDS = ['ai-n9201', 'ai-n9202', 'ai-n9203'] as const;
const run = `${Date.now()}`;
// A made-up value in the shape of a tailnet address, built from parts.
const TAILNET_IP = [100, 70, 3, 4].join('.');

let app: NestFastifyApplication;
let prisma: PrismaService;
let adminToken: string;
let viewerToken: string;
let ownerId: string;

async function cleanup(): Promise<void> {
  await prisma.localaiAlert.deleteMany({ where: { OR: [{ node: { nodeId: { in: [...IDS] } } }, { kind: `lab.test.${run}` }] } });
  await prisma.localaiNode.deleteMany({ where: { nodeId: { in: [...IDS] } } });
  await prisma.workerNode.deleteMany({ where: { name: { in: [...IDS] } } });
}

async function login(role: 'admin' | 'viewer', label: string): Promise<{ token: string; id: string }> {
  const email = `lab-${label}-${run}@example.test`;
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role } });
  expect(response.statusCode).toBeLessThan(400);
  const token = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  expect(token).not.toBe('');
  const user = await prisma.user.findFirstOrThrow({ where: { email } });
  return { token, id: user.id };
}

const get = (url: string, token: string) => app.inject({ method: 'GET', url, headers: { authorization: `Bearer ${token}` } });

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  const admin = await login('admin', 'admin');
  adminToken = admin.token;
  ownerId = admin.id;
  viewerToken = (await login('viewer', 'viewer')).token;
  await cleanup();

  const worker = await prisma.workerNode.create({
    data: {
      name: IDS[0],
      hostname: 'host-alpha',
      platform: 'linux/amd64',
      cliVersion: 'aiagent/0.4.0',
      eligibleTypes: [],
      concurrency: 1,
      status: 'online',
      lastHeartbeatAt: new Date(),
      createdById: ownerId,
    },
  });
  const now = new Date();
  const first = await prisma.localaiNode.create({
    data: {
      nodeId: IDS[0],
      displayName: 'Node One',
      publicHost: `${IDS[0]}.example.test`,
      servePattern: 'B',
      tailnetIp: TAILNET_IP,
      phase: 'enrolled',
      mode: 'online',
      backend: 'cuda',
      agentVersion: '0.4.0',
      workerNodeId: worker.id,
      keyReportedAt: now,
      lastStateAt: now,
      lastStateSeq: 1n,
      lastState: { mode: 'online', hardware: { gpus: [{ name: 'GPU One', vram_total_mib: 12288, vram_used_mib: 2048 }] }, ark: { roots: [], drive_last_synced_at: null } },
    },
  });
  await prisma.localaiNode.create({
    data: { nodeId: IDS[1], displayName: 'Node Two', publicHost: `${IDS[1]}.example.test`, servePattern: 'none', phase: 'pending' },
  });
  await prisma.localaiNode.create({
    data: { nodeId: IDS[2], displayName: 'Node Three', publicHost: `${IDS[2]}.example.test`, servePattern: 'B', phase: 'retired', retiredAt: now },
  });
  await prisma.localaiNodeModel.createMany({
    data: [
      { nodeId: first.id, modelId: 'model-one-q4', present: true, verified: true, loadable: true, status: 'loaded', isDefault: true, roles: [], inputModalities: ['text'] },
      { nodeId: first.id, modelId: 'model-two-q4', present: true, verified: true, loadable: true, status: 'unloaded', isDefault: false, roles: [], inputModalities: ['text'] },
    ],
  });
  await prisma.localaiAlert.createMany({
    data: [
      { nodeId: first.id, kind: `lab.test.${run}.a`, code: 'LAI-CLD-022', severity: 'critical', title: 'Test alert one' },
      { nodeId: first.id, kind: `lab.test.${run}.b`, code: 'LAI-CLD-023', severity: 'warning', title: 'Test alert two' },
      { nodeId: first.id, kind: `lab.test.${run}.c`, code: 'LAI-CLD-023', severity: 'warning', title: 'Cleared alert', state: 'cleared', clearedAt: now },
      { nodeId: null, kind: `lab.test.${run}`, code: 'LAI-CLD-030', severity: 'warning', title: 'Lab-wide test alert' },
    ],
  });
});

afterAll(async () => {
  if (prisma) {
    await cleanup();
    await prisma.user.deleteMany({ where: { email: { in: [`lab-admin-${run}@example.test`, `lab-viewer-${run}@example.test`] } } }).catch(() => undefined);
  }
  await app?.close();
});

describe('Lab overview on real SQL', () => {
  it('lists the enrolled and the pending node, not the retired one, with derived fields', async () => {
    const response = await get('/api/localai/nodes', viewerToken);
    expect(response.statusCode).toBe(200);
    const data = LabNodesResponseSchema.parse((response.json() as { data: unknown }).data);
    const mine = data.nodes.filter((node) => (IDS as readonly string[]).includes(node.nodeId));
    expect(mine.map((node) => node.nodeId)).toEqual([IDS[0], IDS[1]]);

    const [one, two] = mine;
    expect(one).toMatchObject({
      health: 'healthy',
      mode: 'online',
      routable: true,
      hardware: 'GPU One 12 GB',
      backend: 'cuda',
      loadedModels: [{ id: 'model-one-q4', isDefault: true }],
      lastDefault: 'model-one-q4',
      vram: { usedMib: 2048, totalMib: 12288 },
      alerts: { active: 2, critical: 1 },
    });
    expect(two).toMatchObject({ health: 'quiet', phase: 'pending', routable: false, alerts: { active: 0, critical: 0 } });
    // The lab-wide alert counts in the header only; cleared alerts count nowhere.
    expect(data.summary.activeAlerts).toBeGreaterThanOrEqual(3);
  });

  it('never returns the tailnet IP from the list, for anyone', async () => {
    for (const token of [viewerToken, adminToken]) {
      const response = await get('/api/localai/nodes?reveal=tailnet', token);
      expect(response.body).not.toContain(TAILNET_IP);
    }
  });

  it('detail: a viewer never sees the IP, an admin only with ?reveal=tailnet; a retired node is a 404', async () => {
    expect((await get(`/api/localai/nodes/${IDS[0]}?reveal=tailnet`, viewerToken)).body).not.toContain(TAILNET_IP);
    expect((await get(`/api/localai/nodes/${IDS[0]}`, adminToken)).body).not.toContain(TAILNET_IP);
    const revealed = await get(`/api/localai/nodes/${IDS[0]}?reveal=tailnet`, adminToken);
    expect((revealed.json() as { data: { tailnetIp: string; lastState: unknown } }).data).toMatchObject({ tailnetIp: TAILNET_IP, lastState: { mode: 'online' } });
    expect((await get(`/api/localai/nodes/${IDS[2]}`, adminToken)).statusCode).toBe(404);
    expect((await get('/api/localai/nodes/ai-n9999', adminToken)).statusCode).toBe(404);
  });

  it('401 without a session', async () => {
    expect((await app.inject({ method: 'GET', url: '/api/localai/nodes' })).statusCode).toBe(401);
  });
});
