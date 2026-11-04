// The gateway routing table on a real database (docs/specs/gateway.md section 4.3): the whole app on a migrated
// database, three nodes enrolled through the real node-link routes, snapshots pushed through the real state route.
// What only the real stack proves: the table's query (nodes joined with models, the worker heartbeat behind EAB's
// health), the `localai.node.state` event reaching the table through the platform's event bus, and the
// `localai.node.key_reported` event doing the same for a node that gets a key. Every id is invented.
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import type { LocalaiStateSnapshotV1 } from '../../src/localai/contracts/state.schema';
import { RoutingTable } from '../../src/localai/gateway/routing/routing-table';
import { LOCALAI_NODE_KEY_REPORTED_EVENT } from '../../src/localai/keys/node-key.events';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { ai3 } from './state-fixtures';

// Lab node ids must look like ai-n<N>; a high range no real lab uses, cleaned up before and after.
const NODE_IDS = ['ai-n9301', 'ai-n9302', 'ai-n9303'] as const;
const LOADED = 'qwen3.5-9b-q4km';
const FLIPPED = 'qwen3.5-4b-q4km';
const run = `${Date.now()}`;

let app: NestFastifyApplication;
let prisma: PrismaService;
let table: RoutingTable;
let bus: EventBus;
let credentials: NodeCredentialService;
let ownerId: string;
const mintedCredentialIds: string[] = [];

async function cleanup(): Promise<void> {
  await prisma.localaiNode.deleteMany({ where: { nodeId: { in: [...NODE_IDS] } } });
  await prisma.workerNode.deleteMany({ where: { name: { in: [...NODE_IDS] } } });
}

async function enrolled(nodeId: string) {
  const minted = await credentials.createCredential(ownerId, { name: `${nodeId} ${run}` } as never);
  mintedCredentialIds.push(minted.id);
  await prisma.localaiNode.create({
    data: { nodeId, displayName: `Node ${nodeId}`, publicHost: `${nodeId}.example.test`, servePattern: 'B', phase: 'enrolled', nodeCredentialId: minted.id, createdById: ownerId },
  });
  const registered = await app.inject({
    method: 'POST',
    url: '/api/nodes/register',
    headers: { authorization: `Bearer ${minted.token}` },
    payload: { name: nodeId, hostname: 'host-alpha', platform: 'linux/amd64', cliVersion: 'aiagent/0.4.0', eligibleTypes: ['localai.status.refresh'], concurrency: 1, capabilities: {} },
  });
  expect([200, 201]).toContain(registered.statusCode);
  const workerId = (registered.json() as { data: { node: { id: string } } }).data.node.id;
  const handshake = await app.inject({ method: 'GET', url: `/api/nodes/${workerId}/localai/config`, headers: { authorization: `Bearer ${minted.token}` } });
  expect(handshake.statusCode).toBe(200);
  return { nodeId, token: minted.token, workerId };
}

type Node = Awaited<ReturnType<typeof enrolled>>;
let nodes: Node[];
let seq = 100;

/** The ai-n3 fixture for `node`: `LOADED` is loaded, `FLIPPED` is loaded only when asked. */
function snapshotFor(node: Node, index: number, patch: { flippedLoaded?: boolean; mode?: 'online' | 'offline' } = {}): LocalaiStateSnapshotV1 {
  const snapshot = ai3();
  snapshot.node_id = node.nodeId;
  snapshot.boot_id = `5f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d${(0x9300 + index).toString(16).padStart(4, '0')}`;
  seq += 1;
  snapshot.seq = seq;
  if (patch.mode) snapshot.mode = patch.mode;
  for (const model of snapshot.models) if (model.id === FLIPPED) model.status = patch.flippedLoaded ? 'loaded' : 'unloaded';
  return snapshot;
}

async function push(node: Node, snapshot: LocalaiStateSnapshotV1): Promise<void> {
  const response = await app.inject({
    method: 'POST',
    url: `/api/nodes/${node.workerId}/localai/state`,
    headers: { authorization: `Bearer ${node.token}`, 'content-type': 'application/json' },
    payload: JSON.stringify(snapshot),
  });
  expect(response.statusCode).toBe(200);
}

/** Polls the table until `check` holds; resolves with the milliseconds it took. Fails after `limitMs`. */
async function until(check: () => boolean, limitMs: number): Promise<number> {
  const start = Date.now();
  while (!check()) {
    if (Date.now() - start > limitMs) throw new Error(`the routing table did not change within ${limitMs} ms`);
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  return Date.now() - start;
}

const mine = (): string[] => table.routableNodes().map((n) => n.nodeId).filter((id) => (NODE_IDS as readonly string[]).includes(id)).sort();
const loadedNodes = (modelId: string): string[] => {
  const result = table.select(modelId);
  return result.kind === 'candidates' ? result.candidates.map((c) => c.nodeId).filter((id) => (NODE_IDS as readonly string[]).includes(id)).sort() : [];
};

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  table = app.get(RoutingTable, { strict: false });
  bus = app.get<EventBus>(EVENT_BUS, { strict: false });
  credentials = app.get(NodeCredentialService);
  const email = `gw-routing-${run}@example.test`;
  const login = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role: 'admin' } });
  expect(login.statusCode).toBeLessThan(400);
  ownerId = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
  await cleanup();

  nodes = [];
  for (const id of NODE_IDS) nodes.push(await enrolled(id));
  // The mirror of the cloud's 3-node fixture (#80): two nodes online, the third in offline mode.
  await push(nodes[0]!, snapshotFor(nodes[0]!, 0));
  await push(nodes[1]!, snapshotFor(nodes[1]!, 1));
  await push(nodes[2]!, snapshotFor(nodes[2]!, 2, { mode: 'offline' }));
  // The agents heartbeat: EAB derives a node's health from `lastHeartbeatAt` (a registered node has none yet).
  await prisma.workerNode.updateMany({ where: { name: { in: [...NODE_IDS] } }, data: { lastHeartbeatAt: new Date() } });
  // Only the first two have reported a key.
  await prisma.localaiNode.updateMany({ where: { nodeId: { in: [NODE_IDS[0], NODE_IDS[1]] } }, data: { keyReportedAt: new Date() } });
  await table.rebuild();
});

afterAll(async () => {
  if (prisma) {
    await cleanup();
    await prisma.nodeCredential.deleteMany({ where: { id: { in: mintedCredentialIds } } });
  }
  await app?.close();
});

describe('the routing table on real SQL', () => {
  it('admits the two nodes that are online with a key, and not the one in offline mode', () => {
    expect(mine()).toEqual([NODE_IDS[0], NODE_IDS[1]]);
    expect(loadedNodes(LOADED)).toEqual([NODE_IDS[0], NODE_IDS[1]]);
  });

  it('lists the unloaded, loadable model as loadable on both nodes and as no loaded candidate', () => {
    expect(loadedNodes(FLIPPED)).toEqual([]);
    expect(table.loadableCandidates(FLIPPED).map((c) => c.nodeId).filter((id) => mine().includes(id)).sort()).toEqual([NODE_IDS[0], NODE_IDS[1]]);
  });

  it('knows a model is present on a node even when no routable node has it loaded (503 versus 404)', () => {
    expect(table.unavailable(FLIPPED, true)).toEqual({ status: 503, code: 'LAI-GW-021' });
    expect(table.unavailable('no-such-model', true)).toEqual({ status: 404, code: 'LAI-GW-020' });
  });

  it('shows a localai.node.state event within 1 s (a model flipping to loaded)', async () => {
    await push(nodes[1]!, snapshotFor(nodes[1]!, 1, { flippedLoaded: true }));
    const took = await until(() => loadedNodes(FLIPPED).includes(NODE_IDS[1]), 1000);
    expect(took).toBeLessThan(1000);
    expect(loadedNodes(FLIPPED)).toEqual([NODE_IDS[1]]);
  });

  it('shows a mode change within 1 s: a node going offline drops out of the table', async () => {
    await push(nodes[1]!, snapshotFor(nodes[1]!, 1, { flippedLoaded: true, mode: 'offline' }));
    await until(() => !mine().includes(NODE_IDS[1]), 1000);
    expect(loadedNodes(LOADED)).toEqual([NODE_IDS[0]]);
    expect(loadedNodes(FLIPPED)).toEqual([]);
  });

  it('keeps a live counter across the rebuilds the events caused', async () => {
    table.acquire({ nodeId: NODE_IDS[0], modelId: LOADED });
    await push(nodes[0]!, snapshotFor(nodes[0]!, 0));
    await table.rebuild();
    expect(table.inflightOf({ nodeId: NODE_IDS[0], modelId: LOADED })).toBe(1);
    table.release({ nodeId: NODE_IDS[0], modelId: LOADED });
  });

  it('admits a node within 1 s of its key_reported event', async () => {
    await push(nodes[2]!, snapshotFor(nodes[2]!, 2));
    await prisma.localaiNode.update({ where: { nodeId: NODE_IDS[2] }, data: { keyReportedAt: new Date() } });
    await bus.publish(LOCALAI_NODE_KEY_REPORTED_EVENT, { nodeId: NODE_IDS[2], keyHint: 'aaaa…bbbb', reason: 'enroll' });
    await until(() => mine().includes(NODE_IDS[2]), 1000);
    expect(loadedNodes(LOADED)).toContain(NODE_IDS[2]);
  });

  it('clears keyInvalid on the key_reported event', async () => {
    table.markKeyInvalid({ nodeId: NODE_IDS[2] });
    expect(loadedNodes(LOADED)).not.toContain(NODE_IDS[2]);
    expect(table.keyInvalidNodes()).toContain(NODE_IDS[2]);
    await bus.publish(LOCALAI_NODE_KEY_REPORTED_EVENT, { nodeId: NODE_IDS[2], keyHint: 'aaaa…bbbb', reason: 'rotate' });
    await until(() => loadedNodes(LOADED).includes(NODE_IDS[2]), 1000);
    expect(table.keyInvalidNodes()).not.toContain(NODE_IDS[2]);
  });

  it('drops a node whose gateway use was switched off at the next rebuild', async () => {
    await prisma.localaiNode.update({ where: { nodeId: NODE_IDS[2] }, data: { gatewayEnabled: false } });
    await table.rebuild();
    expect(mine()).not.toContain(NODE_IDS[2]);
  });
});
