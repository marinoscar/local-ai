// State ingestion end to end against real SQL (docs/specs/cloud-app.md sections 3.6, 4.3 and 9): the whole app on a
// migrated database, a `nod_` credential minted by the platform's `NodeCredentialService` and bound to a Local AI
// node, the agent's `POST /api/nodes/register`, `GET config`, and `POST state`. What only real SQL proves: the row
// lock that orders concurrent snapshots, the JSONB round trip, the BigInt seq, the models diff as real rows, and the
// audit row of a mode change.
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import { LOCALAI_STATE_BODY_LIMIT_BYTES, type LocalaiStateSnapshotV1 } from '../../src/localai/contracts/state.schema';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { ai3, secrets } from './state-fixtures';

// A lab node id must match ^ai-n[1-9][0-9]{0,3}$ (the snapshot's node_id), so these cannot carry a run suffix:
// a high range no real lab uses, cleaned up before and after.
const NODE_IDS = Array.from({ length: 12 }, (_, index) => `ai-n${9100 + index}`);
const run = `${Date.now()}`;

let app: NestFastifyApplication;
let prisma: PrismaService;
let credentials: NodeCredentialService;
let ownerId: string;
const mintedCredentialIds: string[] = [];
const events: Array<{ nodeId: string; modelsChanged: boolean; modeChanged: boolean }> = [];
let unsubscribe: () => void;

async function cleanup(): Promise<void> {
  await prisma.auditEvent.deleteMany({ where: { targetId: { in: NODE_IDS }, action: { startsWith: 'localai.node.' } } });
  await prisma.localaiNode.deleteMany({ where: { nodeId: { in: NODE_IDS } } });
  await prisma.workerNode.deleteMany({ where: { name: { in: NODE_IDS } } });
}

/** Mints a credential, creates the Local AI node bound to it, registers the worker node. */
async function enrolled(slot: number, phase: 'enrolled' | 'retired' = 'enrolled') {
  const nodeId = NODE_IDS[slot]!;
  const minted = await credentials.createCredential(ownerId, { name: `${nodeId} ${run}` } as never);
  mintedCredentialIds.push(minted.id);
  await prisma.localaiNode.create({
    data: { nodeId, displayName: `Node ${nodeId}`, publicHost: `${nodeId}.example.test`, servePattern: 'B', phase, nodeCredentialId: minted.id, createdById: ownerId },
  });
  const registered = await app.inject({
    method: 'POST',
    url: '/api/nodes/register',
    headers: { authorization: `Bearer ${minted.token}` },
    payload: { name: nodeId, hostname: 'host-alpha', platform: 'linux/amd64', cliVersion: 'aiagent/0.4.0', eligibleTypes: ['localai.status.refresh'], concurrency: 1, capabilities: {} },
  });
  expect([200, 201]).toContain(registered.statusCode);
  const workerId = (registered.json() as { data: { node: { id: string } } }).data.node.id;
  // The first bound call links the worker node (and enrolls a pending node); the handshake is what an agent does first.
  const handshake = await app.inject({ method: 'GET', url: `/api/nodes/${workerId}/localai/config`, headers: { authorization: `Bearer ${minted.token}` } });
  if (phase === 'enrolled') expect(handshake.statusCode).toBe(200);
  return { nodeId, token: minted.token, workerId };
}

type Node = Awaited<ReturnType<typeof enrolled>>;

/** A snapshot of the ai-n3 fixture for `nodeId`, with its own boot id. */
function snapshotFor(nodeId: string, bootSuffix: number, seq: number): LocalaiStateSnapshotV1 {
  const snapshot = ai3();
  snapshot.node_id = nodeId;
  snapshot.boot_id = `5f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d${bootSuffix.toString(16).padStart(4, '0')}`;
  snapshot.seq = seq;
  return snapshot;
}

const push = (node: Node, body: unknown) =>
  app.inject({
    method: 'POST',
    url: `/api/nodes/${node.workerId}/localai/state`,
    headers: { authorization: `Bearer ${node.token}`, 'content-type': 'application/json' },
    payload: typeof body === 'string' ? body : JSON.stringify(body),
  });

const rowOf = (nodeId: string) => prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
const modelsOf = (nodeId: string) =>
  prisma.localaiNodeModel.findMany({ where: { node: { nodeId } }, orderBy: { modelId: 'asc' } });
const auditRows = (action: string, nodeId: string) => prisma.auditEvent.findMany({ where: { action, targetId: nodeId } });
const settle = () => new Promise((resolve) => setImmediate(resolve));

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  credentials = app.get(NodeCredentialService);
  const email = `node-state-${run}@example.test`;
  const login = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role: 'admin' } });
  expect(login.statusCode).toBeLessThan(400);
  ownerId = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
  unsubscribe = app.get<EventBus>(EVENT_BUS, { strict: false }).subscribe('localai.node.state', (payload) => {
    events.push(payload as (typeof events)[number]);
  });
  await cleanup();
});

afterAll(async () => {
  unsubscribe?.();
  if (prisma) {
    await cleanup();
    await prisma.nodeCredential.deleteMany({ where: { id: { in: mintedCredentialIds } } });
  }
  await app?.close();
});

beforeEach(() => {
  events.length = 0;
});

describe('state ingestion on real SQL', () => {
  it('accepts the full ai-n3 fixture, stores it verbatim and projects models[] into localai_node_models', async () => {
    const node = await enrolled(0);
    const snapshot = snapshotFor(node.nodeId, 1, 41);
    const response = await push(node, snapshot);
    expect(response.statusCode).toBe(200);
    expect((response.json() as { data: unknown }).data).toEqual({ accepted: true, seq: 41 });

    const row = await rowOf(node.nodeId);
    expect(row.lastState).toEqual(snapshot);
    expect(row.lastStateSeq).toBe(41n);
    expect(row.lastStateAt).toBeInstanceOf(Date);
    expect(Math.abs(row.lastStateAt!.getTime() - Date.now())).toBeLessThan(60_000);
    expect(row).toMatchObject({ mode: 'online', connectivity: 'full', profile: 'large-24gb', agentVersion: '0.4.0', catalogVersion: '2026.11.02-1', backend: 'cuda' });

    const rows = await modelsOf(node.nodeId);
    expect(rows.map((model) => model.modelId)).toEqual(snapshot.models.map((model) => model.id).sort());
    for (const model of snapshot.models) {
      expect(rows.find((stored) => stored.modelId === model.id)).toMatchObject({
        present: model.present,
        verified: model.verified,
        loadable: model.loadable,
        loadableReason: model.loadable_reason,
        status: model.status,
        isDefault: model.default,
        slots: model.slots,
        roles: model.roles,
        inputModalities: model.input_modalities,
        ctx: model.ctx,
      });
    }

    await settle();
    expect(events).toEqual([{ nodeId: node.nodeId, modelsChanged: true, modeChanged: false }]);
    // The first snapshot is not a mode change.
    expect(await auditRows('localai.node.mode_changed', node.nodeId)).toHaveLength(0);
  });

  it('orders by (boot_id, seq): same seq and older seq are 409 stale_seq, a new boot_id with seq 0 is accepted', async () => {
    const node = await enrolled(1);
    expect((await push(node, snapshotFor(node.nodeId, 2, 5))).statusCode).toBe(200);

    for (const seq of [5, 4, 0]) {
      const stale = await push(node, snapshotFor(node.nodeId, 2, seq));
      expect(stale.statusCode).toBe(409);
      expect((stale.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-008', reason: 'stale_seq' });
    }
    expect((await rowOf(node.nodeId)).lastStateSeq).toBe(5n);

    expect((await push(node, snapshotFor(node.nodeId, 2, 6))).statusCode).toBe(200);
    expect((await push(node, snapshotFor(node.nodeId, 3, 0))).statusCode).toBe(200);
    expect(await rowOf(node.nodeId)).toMatchObject({ lastStateSeq: 0n });
    expect(((await rowOf(node.nodeId)).lastState as { boot_id: string }).boot_id).toBe(snapshotFor(node.nodeId, 3, 0).boot_id);
    await settle();
    expect(events).toHaveLength(3); // only accepted snapshots publish
  });

  it('serializes concurrent snapshots of one node: exactly one of eight identical seqs is accepted', async () => {
    const node = await enrolled(2);
    const responses = await Promise.all(Array.from({ length: 8 }, () => push(node, snapshotFor(node.nodeId, 4, 10))));
    const codes = responses.map((response) => response.statusCode).sort();
    expect(codes).toEqual([200, 409, 409, 409, 409, 409, 409, 409]);
    await settle();
    expect(events).toHaveLength(1);
    expect(await modelsOf(node.nodeId)).toHaveLength(ai3().models.length);
  });

  it('applies concurrent increasing seqs without ever going backwards', async () => {
    const node = await enrolled(3);
    await Promise.all([3, 9, 5, 7, 1].map((seq) => push(node, snapshotFor(node.nodeId, 5, seq))));
    const stored = (await rowOf(node.nodeId)).lastStateSeq as bigint;
    expect(stored).toBe(9n);
  });

  it('diffs models: removing one deletes its row, a change updates only that row', async () => {
    const node = await enrolled(4);
    const first = snapshotFor(node.nodeId, 6, 1);
    expect((await push(node, first)).statusCode).toBe(200);
    const before = await modelsOf(node.nodeId);
    expect(before).toHaveLength(first.models.length);

    const second = snapshotFor(node.nodeId, 6, 2);
    const removed = second.models.pop()!; // qwen3-vl-8b-q4km (present: false)
    second.models[1]!.status = 'loading';
    expect((await push(node, second)).statusCode).toBe(200);
    const after = await modelsOf(node.nodeId);
    expect(after.map((model) => model.modelId)).not.toContain(removed.id);
    expect(after).toHaveLength(first.models.length - 1);
    expect(after.find((model) => model.modelId === second.models[1]!.id)!.status).toBe('loading');
    // An unchanged row was not rewritten.
    const untouched = second.models[0]!.id;
    expect(after.find((model) => model.modelId === untouched)!.updatedAt).toEqual(before.find((model) => model.modelId === untouched)!.updatedAt);

    // A third snapshot with the same models changes no model row, and the event says so.
    await settle();
    events.length = 0;
    const third = snapshotFor(node.nodeId, 6, 3);
    third.models = second.models;
    expect((await push(node, third)).statusCode).toBe(200);
    const fourth = snapshotFor(node.nodeId, 6, 4);
    fourth.models = second.models.map((model) => ({ ...model }));
    fourth.models[0]!.slots += 1;
    expect((await push(node, fourth)).statusCode).toBe(200);
    await settle();
    expect(events.map((event) => event.modelsChanged)).toEqual([false, true]);
  });

  it('keeps the previous READY report of a root when the next snapshot sends ready: null', async () => {
    const node = await enrolled(5);
    const first = snapshotFor(node.nodeId, 7, 1);
    expect((await push(node, first)).statusCode).toBe(200);
    const second = snapshotFor(node.nodeId, 7, 2);
    second.ark.roots[0]!.ready = null;
    second.ark.roots[1]!.ready = null;
    expect((await push(node, second)).statusCode).toBe(200);
    const stored = (await rowOf(node.nodeId)).lastState as LocalaiStateSnapshotV1;
    expect(stored.seq).toBe(2);
    expect(stored.ark.roots[0]!.ready).toEqual(first.ark.roots[0]!.ready);
    expect(stored.ark.roots[1]!.ready).toBeNull();
  });

  it('audits localai.node.mode_changed with { from, to } once, and flags the event', async () => {
    const node = await enrolled(6);
    expect((await push(node, snapshotFor(node.nodeId, 8, 1))).statusCode).toBe(200);
    const offline = snapshotFor(node.nodeId, 8, 2);
    offline.mode = 'offline';
    expect((await push(node, offline)).statusCode).toBe(200);
    expect((await push(node, { ...offline, seq: 3 })).statusCode).toBe(200); // same mode again
    const rows = await auditRows('localai.node.mode_changed', node.nodeId);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ actorUserId: ownerId, targetType: 'localai_node', meta: { from: 'online', to: 'offline' } });
    expect((await rowOf(node.nodeId)).mode).toBe('offline');
    await settle();
    expect(events.map((event) => event.modeChanged)).toEqual([false, true, false]);
  });

  it('refuses a node_id that is not the bound node (400 LAI-CLD-004) and writes nothing', async () => {
    const node = await enrolled(7);
    const response = await push(node, snapshotFor(NODE_IDS[11]!, 9, 1));
    expect(response.statusCode).toBe(400);
    expect((response.json() as { details: { code: string } }).details.code).toBe('LAI-CLD-004');
    expect((await rowOf(node.nodeId)).lastState).toBeNull();
    expect(await modelsOf(node.nodeId)).toHaveLength(0);
  });

  it('refuses schema failures and secret-like values with 400 and writes nothing', async () => {
    const node = await enrolled(8);
    const tooMany = snapshotFor(node.nodeId, 10, 1);
    tooMany.models = Array.from({ length: 201 }, (_, index) => ({ ...tooMany.models[0]!, id: `model-${index}` }));
    expect((await push(node, tooMany)).statusCode).toBe(400);

    const unknown = { ...snapshotFor(node.nodeId, 10, 2), surprise: true };
    expect((await push(node, unknown)).statusCode).toBe(400);

    const ip = secrets.tailnetIp();
    const withIp = snapshotFor(node.nodeId, 10, 3);
    withIp.llama.build = `built on ${ip}`;
    const refusedIp = await push(node, withIp);
    expect(refusedIp.statusCode).toBe(400);
    expect((refusedIp.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-008', reason: 'secret_like_value', path: 'llama.build' });
    expect(refusedIp.body).not.toContain(ip);

    const withToken = snapshotFor(node.nodeId, 10, 4);
    withToken.alerts[0]!.title = secrets.labToken();
    expect((await push(node, withToken)).statusCode).toBe(400);

    expect((await rowOf(node.nodeId)).lastState).toBeNull();
    expect(await modelsOf(node.nodeId)).toHaveLength(0);
    await settle();
    expect(events).toHaveLength(0);
    // And the node still works afterwards.
    expect((await push(node, snapshotFor(node.nodeId, 10, 5))).statusCode).toBe(200);
  });

  it('answers 413 for a body over 256 KiB and leaves the node untouched', async () => {
    const node = await enrolled(9);
    const oversized = JSON.stringify({ ...snapshotFor(node.nodeId, 11, 1), padding: 'x'.repeat(LOCALAI_STATE_BODY_LIMIT_BYTES) });
    expect((await push(node, oversized)).statusCode).toBe(413);
    expect((await rowOf(node.nodeId)).lastState).toBeNull();
  });

  it('a retired node is refused by the guard (403 LAI-CLD-035)', async () => {
    const node = await enrolled(10);
    await prisma.localaiNode.update({ where: { nodeId: node.nodeId }, data: { phase: 'retired' } });
    const response = await push(node, snapshotFor(node.nodeId, 12, 1));
    expect(response.statusCode).toBe(403);
    expect((response.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-035', reason: 'node_retired' });
  });

  it("a node's snapshot never lands on another node (the credential of one node on the other's path)", async () => {
    const first = await enrolled(11);
    const response = await app.inject({
      method: 'POST',
      url: `/api/nodes/${first.workerId}/localai/state`,
      headers: { authorization: `Bearer ${first.token}`, 'content-type': 'application/json' },
      payload: JSON.stringify(snapshotFor(NODE_IDS[0]!, 13, 1)),
    });
    expect(response.statusCode).toBe(400);
    expect((await rowOf(NODE_IDS[0]!)).lastStateSeq).toBe(41n);
    expect((await rowOf(first.nodeId)).lastState).toBeNull();
  });
});
