// The command plane against real PostgreSQL (docs/specs/cloud-app.md sections 4.4 and 9): the claim statement
// (FIFO, types, expiry, one running command per node, SKIP LOCKED under concurrency), the long poll's wake-up, leases
// and hard caps, the sweeper, the dedup index and the whole path over HTTP with a real `nod_` credential.
// Time is the real clock; a test moves a row into the past with SQL when it needs an old claim.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import { LOCALAI_COMMAND_ENQUEUER, type LocalaiCommandEnqueuer } from '../../src/localai/commands/command-enqueuer';
import { ClaimService } from '../../src/localai/commands/claim.service';
import { CommandClaimRepository } from '../../src/localai/commands/claim.sql';
import { COMMAND_SETTLED_CHANNEL } from '../../src/localai/commands/command-events';
import { COMMAND_POLICY } from '../../src/localai/commands/command-policy';
import { CommandsService } from '../../src/localai/commands/commands.service';
import { CommandsSweepHandler } from '../../src/localai/commands/sweeper.job';
import { COMMAND_TYPES, type CommandType } from '../../src/localai/contracts/commands';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

const run = `${Date.now()}`;
const MODEL = 'qwen3.6-35b-a3b-q4km';
const OTHER_MODEL = 'gemma-4-26b-q4km';
const ALL = COMMAND_TYPES.map((type) => type.replace('localai.', ''));
const examples = JSON.parse(readFileSync(join(__dirname, '../fixtures/localai/commands.examples.json'), 'utf8')) as Record<string, { params: unknown; result: unknown }>;
const loadResult = { model_id: MODEL, status: 'loaded', load_ms: 1000, unloaded: [], vram_used_mib: null, is_default: false };

let app: NestFastifyApplication;
let prisma: PrismaService;
let commands: CommandsService;
let claims: ClaimService;
let repository: CommandClaimRepository;
let sweeper: CommandsSweepHandler;
let bus: EventBus;
let credentials: NodeCredentialService;
let ownerId: string;
const nodeRowIds: string[] = [];
const credentialIds: string[] = [];
let counter = 0;

/** A Local AI node row (what the wizard creates), without a credential: enough for the service level. */
async function makeNode(phase: 'pending' | 'enrolled' = 'enrolled'): Promise<string> {
  counter += 1;
  const node = await prisma.localaiNode.create({
    data: { nodeId: `ai-n${counter}-${run}`, displayName: `Node ${counter}`, publicHost: `node-${counter}.example.test`, servePattern: 'none', phase, createdById: ownerId },
  });
  nodeRowIds.push(node.id);
  return node.id;
}

const enqueue = (nodeId: string, type: CommandType, params: unknown = {}) => commands.enqueueForNode(nodeId, type, params, { userId: ownerId }, 'ui');
const claimOne = async (nodeId: string, names: string[] = ALL, waitMs = 0) => (await claims.claim(nodeId, { types: names, waitMs })).jobs[0];
const row = (id: string) => prisma.localaiCommand.findUniqueOrThrow({ where: { id } });
const ago = (ms: number) => new Date(Date.now() - ms);

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  commands = app.get(CommandsService);
  claims = app.get(ClaimService);
  repository = app.get(CommandClaimRepository);
  sweeper = app.get(CommandsSweepHandler);
  bus = app.get<EventBus>(EVENT_BUS);
  credentials = app.get(NodeCredentialService);
  const email = `commands-${run}@example.test`;
  await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role: 'admin' } });
  ownerId = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
});

afterAll(async () => {
  if (prisma) {
    await prisma.localaiCommand.deleteMany({ where: { nodeId: { in: nodeRowIds } } });
    await prisma.auditEvent.deleteMany({ where: { targetType: 'localai_node', targetId: { endsWith: `-${run}` } } });
    await prisma.localaiNode.deleteMany({ where: { id: { in: nodeRowIds } } });
    await prisma.workerNode.deleteMany({ where: { createdById: ownerId } });
    await prisma.nodeCredential.deleteMany({ where: { id: { in: credentialIds } } });
  }
  await app?.close();
});

describe('enqueue and dedup on the real partial unique index', () => {
  it('stores the command and the second identical request returns the active row', async () => {
    const node = await makeNode();
    const first = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    const second = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    expect(first.created).toBe(true);
    expect(second).toMatchObject({ created: false });
    expect(second.command.id).toBe(first.command.id);
    expect(await prisma.localaiCommand.count({ where: { nodeId: node } })).toBe(1);
    expect((await enqueue(node, 'localai.model.load', { model_id: OTHER_MODEL })).created).toBe(true);
  });

  it('many concurrent identical requests collapse onto exactly one row', async () => {
    const node = await makeNode();
    const results = await Promise.all(Array.from({ length: 12 }, () => enqueue(node, 'localai.ark.sync', {})));
    expect(results.filter((r) => r.created)).toHaveLength(1);
    expect(new Set(results.map((r) => r.command.id)).size).toBe(1);
    expect(await prisma.localaiCommand.count({ where: { nodeId: node } })).toBe(1);
  });

  it('a settled command releases its dedup key', async () => {
    const node = await makeNode();
    const first = await enqueue(node, 'localai.status.refresh');
    const job = await claimOne(node);
    await commands.fail(node, first.command.id, { claimToken: job.claimToken, error: 'x' });
    const again = await enqueue(node, 'localai.status.refresh');
    expect(again.created).toBe(true);
    expect(again.command.id).not.toBe(first.command.id);
  });
});

describe('the enqueuer port on real rows', () => {
  it('LOCALAI_COMMAND_ENQUEUER is the command plane: lab id in, unprefixed type, active duplicate reused, claimable', async () => {
    const enqueuer = app.get<LocalaiCommandEnqueuer>(LOCALAI_COMMAND_ENQUEUER);
    expect(enqueuer).toBe(commands);
    const node = await makeNode();
    const labId = (await prisma.localaiNode.findUniqueOrThrow({ where: { id: node } })).nodeId;
    const first = await enqueuer.enqueue(labId, 'status.refresh', {}, { userId: ownerId }, 'system');
    const second = await enqueuer.enqueue(labId, 'status.refresh', {}, { userId: ownerId }, 'system');
    expect(first.created).toBe(true);
    expect(second).toEqual({ id: first.id, created: false });
    expect(await row(first.id)).toMatchObject({ nodeId: node, type: 'localai.status.refresh', source: 'system', requestedById: ownerId });
    expect((await claimOne(node)).job.id).toBe(first.id);
  });
});

describe('the claim statement', () => {
  it('is FIFO and honours the type filter; the node has one running command at a time', async () => {
    const node = await makeNode();
    const a = await enqueue(node, 'localai.model.unload', { model_id: OTHER_MODEL });
    const b = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    const c = await enqueue(node, 'localai.logs.tail', { unit: 'agent' });

    const first = await claimOne(node, ['model.load']); // a is not a requested type, so b is the oldest eligible
    expect(first.job.id).toBe(b.command.id);
    expect(await claimOne(node)).toBeUndefined(); // b is running with a live lease
    await commands.fail(node, b.command.id, { claimToken: first.claimToken, error: 'x' });

    const second = await claimOne(node);
    expect(second.job.id).toBe(a.command.id); // FIFO among what is left
    await commands.fail(node, a.command.id, { claimToken: second.claimToken, error: 'x' });
    expect((await claimOne(node)).job.id).toBe(c.command.id);
  });

  it('turns the row running with attempts 1, a token, claimed_at and the type\'s lease', async () => {
    const node = await makeNode();
    const { command } = await enqueue(node, 'localai.agent.update', { version: '0.2.0' });
    const before = Date.now();
    const job = await claimOne(node);
    const stored = await row(command.id);
    expect(stored).toMatchObject({ status: 'running', attempts: 1, claimToken: job.claimToken });
    expect(stored.claimedAt!.getTime()).toBeGreaterThanOrEqual(before - 5);
    expect(stored.leaseExpiresAt!.getTime() - stored.claimedAt!.getTime()).toBe(COMMAND_POLICY['localai.agent.update'].leaseMs);
    expect(job.leaseExpiresAt).toBe(stored.leaseExpiresAt!.toISOString());
  });

  it('never claims an expired command, another node\'s command, or a type not asked for', async () => {
    const node = await makeNode();
    const other = await makeNode();
    const stale = await enqueue(node, 'localai.status.refresh');
    await prisma.localaiCommand.update({ where: { id: stale.command.id }, data: { expiresAt: ago(1000) } });
    await enqueue(other, 'localai.model.load', { model_id: MODEL });
    expect(await claimOne(node)).toBeUndefined();
    expect((await row(stale.command.id)).status).toBe('queued'); // refusing is not expiring: that is the sweeper's job
    expect(await claimOne(other, ['logs.tail'])).toBeUndefined();
    expect((await claimOne(other, ['model.load'])).job.type).toBe('localai.model.load');
  });

  it('frees the node\'s slot when the running command\'s lease has passed', async () => {
    const node = await makeNode();
    const first = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    const second = await enqueue(node, 'localai.logs.tail', { unit: 'agent' });
    await claimOne(node);
    expect(await claimOne(node)).toBeUndefined();
    await prisma.localaiCommand.update({ where: { id: first.command.id }, data: { leaseExpiresAt: ago(1) } });
    expect((await claimOne(node)).job.id).toBe(second.command.id);
  });
});

describe('claim concurrency (FOR UPDATE SKIP LOCKED and the per-node lock)', () => {
  const attempt = (nodeId: string) => ({ nodeId, types: [...COMMAND_TYPES], now: new Date(), leaseMsOf: () => 30_000 });

  it('ten simultaneous claimers of one node (as from several API processes) get exactly one row between them', async () => {
    const node = await makeNode();
    for (const unit of ['agent', 'llama', 'kiwix', 'whisper', 'sync']) await enqueue(node, 'localai.logs.tail', { unit });
    const results = await Promise.all(Array.from({ length: 10 }, () => repository.claimNext(attempt(node))));
    const got = results.filter((r) => r !== null);
    expect(got).toHaveLength(1);
    expect(await prisma.localaiCommand.count({ where: { nodeId: node, status: 'running' } })).toBe(1);
    expect(await prisma.localaiCommand.count({ where: { nodeId: node, status: 'queued' } })).toBe(4);
    expect((await row(got[0]!.id)).attempts).toBe(1);
  });

  it('simultaneous claims through the service with the same credential\'s node never return the same row twice', async () => {
    const node = await makeNode();
    for (const unit of ['agent', 'llama', 'kiwix']) await enqueue(node, 'localai.logs.tail', { unit });
    const answers = await Promise.all(Array.from({ length: 8 }, () => claims.claim(node, { types: ALL, waitMs: 0 })));
    const jobs = answers.flatMap((answer) => answer.jobs);
    expect(jobs).toHaveLength(1);
    expect(await prisma.localaiCommand.count({ where: { nodeId: node, status: 'running' } })).toBe(1);
  });

  it('claimers of different nodes in parallel each get their own oldest row, with no row handed out twice', async () => {
    const nodes = await Promise.all(Array.from({ length: 6 }, () => makeNode()));
    const queued = new Map<string, string[]>();
    for (const node of nodes) {
      const first = await enqueue(node, 'localai.logs.tail', { unit: 'agent' });
      await enqueue(node, 'localai.logs.tail', { unit: 'llama' });
      queued.set(node, [first.command.id]);
    }
    const results = await Promise.all(nodes.flatMap((node) => Array.from({ length: 3 }, () => repository.claimNext(attempt(node)).then((r) => ({ node, r })))));
    const got = results.filter((entry) => entry.r !== null);
    expect(got).toHaveLength(nodes.length);
    expect(new Set(got.map((entry) => entry.r!.id)).size).toBe(nodes.length);
    for (const { node, r } of got) expect(r!.id).toBe(queued.get(node)![0]);
  });
});

describe('the long poll', () => {
  it('wakes within 200 ms of an enqueue for the node', async () => {
    const node = await makeNode();
    const pending = claims.claim(node, { types: ALL, waitMs: 5000 });
    await new Promise((resolve) => setTimeout(resolve, 100));
    const start = Date.now();
    const { command } = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    const answer = await pending;
    expect(Date.now() - start).toBeLessThan(200);
    expect(answer.jobs[0].job.id).toBe(command.id);
  });

  it('returns empty when the wait ends, and an enqueue for another node does not wake it', async () => {
    const node = await makeNode();
    const other = await makeNode();
    const start = Date.now();
    const pending = claims.claim(node, { types: ALL, waitMs: 400 });
    await new Promise((resolve) => setTimeout(resolve, 100));
    await enqueue(other, 'localai.model.load', { model_id: MODEL });
    expect(await pending).toEqual({ jobs: [] });
    expect(Date.now() - start).toBeGreaterThanOrEqual(380);
    expect(Date.now() - start).toBeLessThan(1500);
  });

  it('a second concurrent claim from the same node is answered empty at once', async () => {
    const node = await makeNode();
    const held = claims.claim(node, { types: ALL, waitMs: 600 });
    await new Promise((resolve) => setTimeout(resolve, 100));
    const start = Date.now();
    expect(await claims.claim(node, { types: ALL, waitMs: 5000 })).toEqual({ jobs: [] });
    expect(Date.now() - start).toBeLessThan(150);
    await held;
  });

  it('holds no database connection while it waits: forty held polls leave the pool free', async () => {
    const waiting = await Promise.all(Array.from({ length: 40 }, async () => makeNode()));
    const polls = waiting.map((node) => claims.claim(node, { types: ALL, waitMs: 1500 }));
    await new Promise((resolve) => setTimeout(resolve, 300));
    const start = Date.now();
    await Promise.all(Array.from({ length: 20 }, () => prisma.localaiNode.count()));
    expect(Date.now() - start).toBeLessThan(1000);
    claims.beforeApplicationShutdown(); // ends them all at once, as the shutdown hook does
    expect((await Promise.all(polls)).every((answer) => answer.jobs.length === 0)).toBe(true);
    (claims as unknown as { shuttingDown: boolean }).shuttingDown = false;
  });
});

describe('leases and the hard cap', () => {
  it('renew: ok with the token, null with a wrong one, null after the lease passed', async () => {
    const node = await makeNode();
    const { command } = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    const job = await claimOne(node);
    expect(await commands.renew(node, command.id, { claimToken: randomToken() })).toBeNull();
    const renewed = await commands.renew(node, command.id, { claimToken: job.claimToken });
    expect(renewed!.getTime()).toBeGreaterThan(Date.parse(job.leaseExpiresAt) - 1);
    expect((await row(command.id)).leaseExpiresAt).toEqual(renewed);
    await prisma.localaiCommand.update({ where: { id: command.id }, data: { leaseExpiresAt: ago(1) } });
    expect(await commands.renew(node, command.id, { claimToken: job.claimToken })).toBeNull();
  });

  it.each(['localai.status.refresh', 'localai.model.load', 'localai.model.delete', 'localai.agent.update'] as CommandType[])(
    '%s: renewal works just inside the type\'s hard cap (clamped to it) and stops at it',
    async (type) => {
      const node = await makeNode();
      const { command } = await enqueue(node, type, examples[type].params);
      const job = await claimOne(node);
      const cap = COMMAND_POLICY[type].hardCapMs;
      const claimedAt = ago(cap - 5_000);
      await prisma.localaiCommand.update({ where: { id: command.id }, data: { claimedAt, leaseExpiresAt: new Date(Date.now() + 10_000) } });
      const renewed = await commands.renew(node, command.id, { claimToken: job.claimToken });
      expect(renewed).not.toBeNull();
      expect(renewed!.getTime()).toBeLessThanOrEqual(claimedAt.getTime() + cap);

      await prisma.localaiCommand.update({ where: { id: command.id }, data: { claimedAt: ago(cap + 1_000), leaseExpiresAt: new Date(Date.now() + 10_000) } });
      expect(await commands.renew(node, command.id, { claimToken: job.claimToken })).toBeNull();
    },
  );

  it('the table values on the wire: 600 s for model.load, 900 s for agent.update, lease times ten otherwise', () => {
    expect(COMMAND_POLICY['localai.model.load'].hardCapMs).toBe(600_000);
    expect(COMMAND_POLICY['localai.agent.update'].hardCapMs).toBe(900_000);
    expect(COMMAND_POLICY['localai.logs.tail'].hardCapMs).toBe(300_000);
  });
});

describe('result and failure on real rows', () => {
  it('enqueue -> claim -> renew -> result -> succeeded, with the settled event', async () => {
    const node = await makeNode();
    const settled: unknown[] = [];
    const off = bus.subscribe(COMMAND_SETTLED_CHANNEL, (event) => void settled.push(event));
    const { command } = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    const job = await claimOne(node);
    expect(await commands.renew(node, command.id, { claimToken: job.claimToken })).not.toBeNull();
    await expect(commands.complete(node, command.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult })).resolves.toEqual({ status: 'succeeded' });
    expect(await row(command.id)).toMatchObject({ status: 'succeeded', result: loadResult, leaseExpiresAt: null });
    await expect(commands.complete(node, command.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult })).resolves.toEqual({ status: 'succeeded' });
    expect(settled).toEqual([{ nodeId: node, commandId: command.id, status: 'succeeded' }]);
    off();
  });

  it('failure path: code split from the message; a result for it afterwards is 409', async () => {
    const node = await makeNode();
    const { command } = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    const job = await claimOne(node);
    await commands.fail(node, command.id, { claimToken: job.claimToken, error: 'LAI-CAT-005: Model does not fit in free VRAM' });
    expect(await row(command.id)).toMatchObject({ status: 'failed', error: { code: 'LAI-CAT-005', message: 'Model does not fit in free VRAM' } });
    await expect(commands.complete(node, command.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult })).rejects.toMatchObject({ status: 409 });
  });

  it('a result and a failure racing for one claim: exactly one wins', async () => {
    const node = await makeNode();
    const { command } = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    const job = await claimOne(node);
    const outcomes = await Promise.allSettled([
      commands.complete(node, command.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult }),
      commands.fail(node, command.id, { claimToken: job.claimToken, error: 'LAI-CAT-005: no room' }),
    ]);
    expect(outcomes.filter((o) => o.status === 'fulfilled')).toHaveLength(1);
    expect(outcomes.filter((o) => o.status === 'rejected')).toHaveLength(1);
    expect(['succeeded', 'failed']).toContain((await row(command.id)).status);
  });

  it('mode.offline sets the node\'s mode; logs.tail scrubs its lines; a result that fails its schema leaves it running', async () => {
    const node = await makeNode();
    const offline = await enqueue(node, 'localai.mode.offline', {});
    const job = await claimOne(node);
    const bad = await commands.complete(node, offline.command.id, { type: 'localai.mode.offline', claimToken: job.claimToken, result: { previous_mode: 'online', mode: 'online' } }).catch((e) => e);
    expect(bad.status).toBe(400);
    expect((await row(offline.command.id)).status).toBe('running');
    await commands.complete(node, offline.command.id, { type: 'localai.mode.offline', claimToken: job.claimToken, result: { previous_mode: 'online', mode: 'offline' } });
    expect((await prisma.localaiNode.findUniqueOrThrow({ where: { id: node } })).mode).toBe('offline');

    const logs = await enqueue(node, 'localai.logs.tail', { unit: 'agent' });
    const logJob = await claimOne(node);
    await commands.complete(node, logs.command.id, { type: 'localai.logs.tail', claimToken: logJob.claimToken, result: { unit: 'agent', lines: [`Bearer ${'s'.repeat(30)}`], truncated: false } });
    expect(JSON.stringify((await row(logs.command.id)).result)).not.toContain('sssssssss');
  });
});

describe('the sweeper on real rows', () => {
  it('expires a queued command past expires_at with LAI-CLD-030 and publishes settled', async () => {
    const node = await makeNode();
    const settled: Array<{ commandId: string }> = [];
    const off = bus.subscribe<{ commandId: string }>(COMMAND_SETTLED_CHANNEL, (event) => void settled.push(event));
    const { command } = await enqueue(node, 'localai.status.refresh');
    await prisma.localaiCommand.update({ where: { id: command.id }, data: { expiresAt: ago(1) } });
    await sweeper.sweep();
    expect(await row(command.id)).toMatchObject({ status: 'expired', error: { code: 'LAI-CLD-030' } });
    expect(settled.map((event) => event.commandId)).toContain(command.id);
    expect(await claimOne(node)).toBeUndefined();
    off();
  });

  it('fails a running command whose lease passed (LAI-CLD-059); a late result, failure and renew then get 409/null', async () => {
    const node = await makeNode();
    const { command } = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    const job = await claimOne(node);
    await prisma.localaiCommand.update({ where: { id: command.id }, data: { leaseExpiresAt: ago(1) } });
    await sweeper.sweep();
    expect(await row(command.id)).toMatchObject({ status: 'failed', error: { code: 'LAI-CLD-059' }, claimToken: null });
    await expect(commands.complete(node, command.id, { type: 'localai.model.load', claimToken: job.claimToken, result: loadResult })).rejects.toMatchObject({ status: 409 });
    await expect(commands.fail(node, command.id, { claimToken: job.claimToken, error: 'late' })).rejects.toMatchObject({ status: 409 });
    expect(await commands.renew(node, command.id, { claimToken: job.claimToken })).toBeNull();
  });

  it('leaves live commands alone, and trims logs.tail results after 7 days and deletes after 180', async () => {
    const node = await makeNode();
    const live = await enqueue(node, 'localai.model.load', { model_id: MODEL });
    await sweeper.sweep();
    expect((await row(live.command.id)).status).toBe('queued');

    const logsNode = await makeNode();
    const logs = await enqueue(logsNode, 'localai.logs.tail', { unit: 'agent' });
    const job = await claimOne(logsNode);
    await commands.complete(logsNode, logs.command.id, { type: 'localai.logs.tail', claimToken: job.claimToken, result: { unit: 'agent', lines: ['a'], truncated: false } });
    await sweeper.sweep();
    expect((await row(logs.command.id)).result).not.toBeNull();
    await prisma.localaiCommand.update({ where: { id: logs.command.id }, data: { finishedAt: ago(8 * 86_400_000) } });
    await sweeper.sweep();
    expect(await row(logs.command.id)).toMatchObject({ status: 'succeeded', result: null });

    await prisma.localaiCommand.update({ where: { id: logs.command.id }, data: { createdAt: ago(181 * 86_400_000) } });
    await prisma.localaiCommand.update({ where: { id: live.command.id }, data: { createdAt: ago(181 * 86_400_000), expiresAt: new Date(Date.now() + 3_600_000) } });
    await sweeper.sweep();
    expect(await prisma.localaiCommand.findUnique({ where: { id: logs.command.id } })).toBeNull();
    expect(await prisma.localaiCommand.findUnique({ where: { id: live.command.id } })).not.toBeNull(); // active rows are never deleted
  });

  it('runs as the registered server-only job type', () => {
    expect(sweeper.type).toBe('localai.commands.sweep');
    expect('nodeResultSchema' in sweeper).toBe(false);
  });
});

function randomToken(): string {
  return '00000000-0000-4000-8000-000000000000';
}

describe('the whole path over HTTP with real node credentials', () => {
  async function enrolled() {
    counter += 1;
    const nodeId = `ai-n${counter}-${run}`;
    const minted = await credentials.createCredential(ownerId, { name: `${nodeId} credential` } as never);
    credentialIds.push(minted.id);
    const node = await prisma.localaiNode.create({
      data: { nodeId, displayName: `Node ${counter}`, publicHost: `node-${counter}.example.test`, servePattern: 'none', phase: 'pending', nodeCredentialId: minted.id, createdById: ownerId },
    });
    nodeRowIds.push(node.id);
    const registered = await app.inject({
      method: 'POST',
      url: '/api/nodes/register',
      headers: { authorization: `Bearer ${minted.token}` },
      payload: { name: nodeId, hostname: 'host-alpha', platform: 'linux/amd64', cliVersion: 'aiagent/0.1.0', eligibleTypes: ['localai.status.refresh'], concurrency: 1, capabilities: { localai: { schema: 1, agent_version: '0.1.0' } } },
    });
    const worker = (registered.json() as { data: { node: { id: string } } }).data.node.id;
    const call = (path: string, payload: unknown, token = minted.token, workerId = worker) =>
      app.inject({ method: 'POST', url: `/api/nodes/${workerId}/localai/commands/${path}`, headers: { authorization: `Bearer ${token}` }, payload: payload as never });
    return { node: node.id, nodeId, token: minted.token, worker, call };
  }

  it('claim long poll -> renew -> result, then failure on a second command', async () => {
    const n = await enrolled();
    const held = n.call('claim', { types: ALL, limit: 1, waitMs: 5000 });
    await new Promise((resolve) => setTimeout(resolve, 100));
    const start = Date.now();
    const { command } = await enqueue(n.node, 'localai.model.load', { model_id: MODEL });
    const claimed = await held;
    expect(Date.now() - start).toBeLessThan(400);
    expect(claimed.statusCode).toBe(200);
    const [entry] = claimed.json().data.jobs;
    expect(entry.job).toMatchObject({ id: command.id, type: 'localai.model.load', attempts: 1 });
    expect(entry.params).toEqual({ model_id: MODEL, wait: true, unload_others: true, set_default: false });

    const renew = await n.call(`${command.id}/renew`, { claimToken: entry.claimToken });
    expect(renew.statusCode).toBe(200);
    const done = await n.call(`${command.id}/result`, { type: 'localai.model.load', claimToken: entry.claimToken, result: loadResult });
    expect(done.statusCode).toBe(200);
    expect(done.json().data).toEqual({ status: 'succeeded' });
    expect((await n.call(`${command.id}/result`, { type: 'localai.model.load', claimToken: entry.claimToken, result: loadResult })).statusCode).toBe(200);
    expect((await row(command.id)).status).toBe('succeeded');

    const second = await enqueue(n.node, 'localai.service.restart', { service: 'llama' });
    const next = (await n.call('claim', { types: ALL, waitMs: 0 })).json().data.jobs[0];
    const failed = await n.call(`${second.command.id}/failure`, { claimToken: next.claimToken, error: 'LAI-SVC-001: restart refused' });
    expect(failed.statusCode).toBe(200);
    expect(await row(second.command.id)).toMatchObject({ status: 'failed', error: { code: 'LAI-SVC-001' } });
    expect((await n.call(`${second.command.id}/renew`, { claimToken: next.claimToken })).statusCode).toBe(409);
  });

  it('a node cannot touch another node\'s command, and its credential is refused on the other\'s path', async () => {
    const a = await enrolled();
    const b = await enrolled();
    const { command } = await enqueue(a.node, 'localai.model.load', { model_id: MODEL });
    const claimed = (await a.call('claim', { types: ALL, waitMs: 0 })).json().data.jobs[0];
    expect((await b.call('claim', { types: ALL, waitMs: 0 })).json().data.jobs).toEqual([]);
    expect((await b.call(`${command.id}/renew`, { claimToken: claimed.claimToken })).statusCode).toBe(409);
    expect((await b.call(`${command.id}/result`, { type: 'localai.model.load', claimToken: claimed.claimToken, result: loadResult })).statusCode).toBe(409);
    expect((await b.call(`${command.id}/failure`, { claimToken: claimed.claimToken, error: 'x' })).statusCode).toBe(409);
    expect((await row(command.id)).status).toBe('running');
    // b's credential on a's worker path: the binding guard refuses before any command code runs.
    const crossed = await b.call('claim', { types: ALL, waitMs: 0 }, b.token, a.worker);
    expect(crossed.statusCode).toBe(403);
    expect(crossed.json().details.code).toBe('LAI-CLD-004');
  });

  it('the sweeper failing a command makes the node\'s late result a 409 over HTTP', async () => {
    const n = await enrolled();
    const { command } = await enqueue(n.node, 'localai.model.load', { model_id: MODEL });
    const claimed = (await n.call('claim', { types: ALL, waitMs: 0 })).json().data.jobs[0];
    await prisma.localaiCommand.update({ where: { id: command.id }, data: { leaseExpiresAt: ago(1) } });
    await sweeper.sweep();
    const late = await n.call(`${command.id}/result`, { type: 'localai.model.load', claimToken: claimed.claimToken, result: loadResult });
    expect(late.statusCode).toBe(409);
    expect(late.json().details).toEqual({ code: 'LAI-CLD-059', reason: 'lease_lost' });
    expect((await row(command.id)).error).toMatchObject({ code: 'LAI-CLD-059' });
  });

  it('HTTP: two simultaneous claims with the same credential: one gets the row, the other is empty', async () => {
    const n = await enrolled();
    await enqueue(n.node, 'localai.model.load', { model_id: MODEL });
    await enqueue(n.node, 'localai.model.unload', { model_id: OTHER_MODEL });
    const answers = await Promise.all([n.call('claim', { types: ALL, waitMs: 0 }), n.call('claim', { types: ALL, waitMs: 0 }), n.call('claim', { types: ALL, waitMs: 0 })]);
    expect(answers.map((a) => a.statusCode)).toEqual([200, 200, 200]);
    expect(answers.flatMap((a) => a.json().data.jobs)).toHaveLength(1);
  });
});
