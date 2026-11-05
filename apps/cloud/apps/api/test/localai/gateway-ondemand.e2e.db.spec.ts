// gateway-ondemand.e2e (db tier). Policies and the on-demand load (docs/specs/gateway.md section 4.3 step 3) end to end: the whole app on a migrated
// database (real routing table, real command plane, real policy store and audit), the /v1 routes on Fastify, and REAL local
// HTTP servers acting as the nodes' llama-servers. "Snapshot ingestion" is faked at its seam: the test changes a node's
// model row and publishes `localai.node.state`, exactly what the state route does after it stores a snapshot.
// Every id, host and key is invented; node keys are random per run.
import { createHash, randomBytes } from 'node:crypto';
import { request as httpRequest } from 'node:http';
import type { AddressInfo } from 'node:net';
import fastifyCookie from '@fastify/cookie';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { Test } from '@nestjs/testing';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

import { AppModule } from '../../src/app.module';
import { hashGatewayToken } from '../../src/localai/gateway/gateway.token';
import { registerGatewayRoutes } from '../../src/localai/gateway/routes';
import { ModelDirectoryProvider } from '../../src/localai/gateway/models/resolve';
import { NodeKeyProvider } from '../../src/localai/gateway/routing/node-key.provider';
import { RoutingTable } from '../../src/localai/gateway/routing/routing-table';
import { UPSTREAM_TRANSPORT, UpstreamClient } from '../../src/localai/gateway/upstream/client';
import { LOCALAI_NODE_STATE_CHANNEL } from '../../src/localai/node-link/state.service';
import { PrismaService } from '../../src/prisma/prisma.service';
import { FakeLlama, okBehaviour, type FakeLlamaBehaviour } from '../fakes/fake-llama';

jest.setTimeout(60_000);

const run = `${Date.now()}`;
const N1 = 'ai-n9411';
const N2 = 'ai-n9412';
const NODE_IDS = [N1, N2];
const VERSION = `2026.11.04-${run.slice(-6)}`;
const MODELS = ['gwod-m1', 'gwod-m2', 'gwod-m3', 'gwod-hold'] as const;
const MODELS_YAML = `schema_version: 1\nmodels:\n${MODELS.map((id) => `  - id: ${id}\n    display_name: "${id}"\n    engine: llama\n    roles: [chat]\n    llama: { ctx: 8192 }\n`).join('')}`;

let app: NestFastifyApplication;
let prisma: PrismaService;
let table: RoutingTable;
let bus: EventBus;
let directory: ModelDirectoryProvider;
let adminToken: string;
let ownerId: string;
let gatewayToken: string;
let keyId: string;
let displaced: string[] = [];
const llamas = new Map<string, FakeLlama>();

const api = (method: 'GET' | 'PUT', url: string, bearer: string, payload?: unknown) =>
  app.inject({ method, url: `/api${url}`, headers: { authorization: `Bearer ${bearer}` }, payload: payload as never });

const chat = (model: string) =>
  app.inject({
    method: 'POST',
    url: '/api/gateway/v1/chat/completions',
    headers: { authorization: `Bearer ${gatewayToken}`, 'content-type': 'application/json' },
    payload: JSON.stringify({ model, messages: [{ role: 'user', content: 'Hola' }] }),
  });

async function until(check: () => Promise<boolean> | boolean, limitMs = 8000): Promise<void> {
  const start = Date.now();
  while (!(await check())) {
    if (Date.now() - start > limitMs) throw new Error(`condition not met within ${limitMs} ms`);
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
}

const loadCommands = async (modelId?: string) =>
  (await prisma.localaiCommand.findMany({ where: { node: { nodeId: { in: NODE_IDS } }, type: 'localai.model.load' }, include: { node: { select: { nodeId: true } } }, orderBy: { createdAt: 'asc' } })).filter(
    (row) => modelId === undefined || (row.params as { model_id?: string }).model_id === modelId,
  );

async function setStatus(nodeId: string, modelId: string, status: 'loaded' | 'unloaded'): Promise<void> {
  const node = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
  await prisma.localaiNodeModel.update({ where: { nodeId_modelId: { nodeId: node.id, modelId } }, data: { status } });
  await bus.publish(LOCALAI_NODE_STATE_CHANNEL, { nodeId, modelsChanged: true, modeChanged: false });
}

async function addNode(nodeId: string, priority: number, modelsMax: number, models: Array<{ modelId: string; status: 'loaded' | 'unloaded' }>): Promise<void> {
  const worker = await prisma.workerNode.create({
    data: { name: nodeId, hostname: 'host-alpha', platform: 'linux/amd64', cliVersion: 'aiagent/0.4.0', eligibleTypes: [], concurrency: 1, status: 'online', lastHeartbeatAt: new Date(), createdById: ownerId },
  });
  await prisma.localaiNode.create({
    data: {
      nodeId,
      displayName: `Node ${nodeId}`,
      publicHost: `${nodeId}.example.test`,
      servePattern: 'B',
      phase: 'enrolled',
      mode: 'online',
      gatewayPriority: priority,
      lastStateAt: new Date(),
      lastState: { llama: { models_max: modelsMax } },
      keyReportedAt: new Date(),
      workerNodeId: worker.id,
      createdById: ownerId,
      models: { create: models.map((model) => ({ modelId: model.modelId, present: true, verified: true, loadable: true, status: model.status, slots: 2, roles: [], inputModalities: ['text'], ctx: 8192 })) },
    },
  });
}

async function cleanup(): Promise<void> {
  await prisma.localaiNode.deleteMany({ where: { nodeId: { in: NODE_IDS } } });
  await prisma.workerNode.deleteMany({ where: { name: { in: NODE_IDS } } });
  await prisma.localaiModelPolicy.deleteMany({ where: { modelId: { in: [...MODELS] } } });
  await prisma.localaiCatalogVersion.deleteMany({ where: { version: VERSION } });
}

/** Starts every scenario from the same lab: m1/m2/m3 unloaded, nothing busy, no policy, no command. */
async function resetLab(): Promise<void> {
  await prisma.localaiCommand.deleteMany({ where: { node: { nodeId: { in: NODE_IDS } } } });
  for (const nodeId of NODE_IDS) {
    const node = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    await prisma.localaiNodeModel.updateMany({ where: { nodeId: node.id }, data: { status: 'unloaded' } });
  }
  await setStatus(N1, 'gwod-hold', 'loaded');
  for (const llama of llamas.values()) {
    llama.requests.length = 0;
    llama.behaviour = okBehaviour;
  }
  await prisma.localaiModelPolicy.deleteMany({ where: { modelId: { in: [...MODELS] } } });
  directory.invalidate();
  await table.rebuild();
}

async function setPolicy(modelId: string, patch: { onDemandLoad?: boolean; loadWaitS?: number; aliases?: string[] } = {}): Promise<void> {
  await prisma.localaiModelPolicy.upsert({
    where: { modelId },
    create: { modelId, onDemandLoad: true, loadWaitS: 1, ...patch },
    update: { onDemandLoad: true, loadWaitS: 1, ...patch },
  });
  directory.invalidate();
}

beforeAll(async () => {
  for (const nodeId of NODE_IDS) llamas.set(nodeId, await FakeLlama.start());
  const client = new UpstreamClient({ version: 'test', resolveOrigin: (host) => llamas.get(host.split('.')[0]!)!.origin });
  const nodeKey = `llama-${randomBytes(16).toString('hex')}`;
  const moduleRef = await Test.createTestingModule({ imports: [AppModule] })
    .overrideProvider(UPSTREAM_TRANSPORT)
    .useValue(client)
    .overrideProvider(NodeKeyProvider)
    .useValue({ get: async () => nodeKey })
    .compile();
  app = moduleRef.createNestApplication<NestFastifyApplication>(new FastifyAdapter());
  await app.register(fastifyCookie, { secret: process.env.COOKIE_SECRET || process.env.JWT_SECRET });
  app.setGlobalPrefix('api');
  await registerGatewayRoutes(app);
  await app.init();
  await app.getHttpAdapter().getInstance().ready();

  prisma = app.get(PrismaService);
  table = app.get(RoutingTable, { strict: false });
  directory = app.get(ModelDirectoryProvider, { strict: false });
  bus = app.get<EventBus>(EVENT_BUS, { strict: false });

  const email = `gw-ondemand-${run}@example.test`;
  const login = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role: 'admin' } });
  adminToken = new URL(String(login.headers.location)).searchParams.get('token') ?? '';
  ownerId = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
  await cleanup();

  // At most one catalog is approved (a partial unique index): displace the current one, restore it afterwards.
  displaced = (await prisma.localaiCatalogVersion.findMany({ where: { status: 'approved' }, select: { version: true } })).map((row) => row.version);
  await prisma.localaiCatalogVersion.updateMany({ where: { status: 'approved' }, data: { status: 'superseded' } });
  await prisma.localaiCatalogVersion.create({
    data: {
      version: VERSION,
      source: 'editor',
      status: 'approved',
      files: [{ path: 'models.yaml', content: MODELS_YAML, sha256: createHash('sha256').update(MODELS_YAML).digest('hex') }],
      bundleSha256: createHash('sha256').update(MODELS_YAML).digest('hex'),
      approvedAt: new Date('2026-11-04T12:00:00.000Z'),
    },
  });

  // n1 (preferred, priority 10) keeps one model loaded and has room for one: models_max 1. n2 has room for two.
  await addNode(N1, 10, 1, [
    { modelId: 'gwod-hold', status: 'loaded' },
    { modelId: 'gwod-m1', status: 'unloaded' },
    { modelId: 'gwod-m2', status: 'unloaded' },
    { modelId: 'gwod-m3', status: 'unloaded' },
  ]);
  await addNode(N2, 50, 2, [{ modelId: 'gwod-m3', status: 'unloaded' }]);

  gatewayToken = `lai_${createHash('sha256').update(`${run}-${Math.random()}`).digest('base64url')}`;
  const key = await prisma.localaiGatewayKey.create({
    data: { userId: ownerId, name: `gwod ${run}`, kind: 'service', tokenHash: hashGatewayToken(gatewayToken), tokenPrefix: gatewayToken.slice(0, 10), allowedModels: [], rpmLimit: 600, concurrentLimit: 16 },
  });
  keyId = key.id;
  await resetLab();
});

afterAll(async () => {
  if (prisma) {
    await cleanup();
    await prisma.auditEvent.deleteMany({ where: { actorUserId: ownerId, action: 'localai.gateway.policy_updated' } });
    await prisma.localaiGatewayKey.deleteMany({ where: { id: keyId } });
    await prisma.localaiCatalogVersion.updateMany({ where: { version: { in: displaced } }, data: { status: 'approved' } });
    await prisma.user.deleteMany({ where: { id: ownerId } });
  }
  await app?.close();
  await Promise.all([...llamas.values()].map((llama) => llama.close()));
});

describe('on-demand load', () => {
  beforeEach(resetLab);

  it('queues exactly one model.load (source gateway), waits for the flip, and the request completes on that node', async () => {
    await setPolicy('gwod-m1', { loadWaitS: 20 });
    const pending = chat('gwod-m1');
    await until(async () => (await loadCommands('gwod-m1')).length === 1);
    expect(llamas.get(N1)!.requests).toEqual([]); // the client receives nothing, and no node is called, while the load runs

    const [command] = await loadCommands('gwod-m1');
    expect(command).toMatchObject({ source: 'gateway', requestedById: null, status: 'queued', node: { nodeId: N1 }, dedupKey: 'model.load:gwod-m1' });
    // models_max 1 and one model loaded: loading another unloads it, and it is idle, so the node is allowed.
    expect(command!.params).toEqual({ model_id: 'gwod-m1', wait: true, unload_others: true, set_default: false });
    const audit = await prisma.auditEvent.findFirstOrThrow({ where: { action: 'localai.command.requested', targetId: command!.id } });
    expect(audit.meta).toMatchObject({ source: 'gateway', keyPrefix: gatewayToken.slice(0, 10), nodeId: N1 });
    expect(JSON.stringify(audit.meta)).not.toContain(gatewayToken);

    await setStatus(N1, 'gwod-m1', 'loaded');
    const response = await pending;
    expect(response.statusCode).toBe(200);
    expect(response.headers['x-localai-node']).toBe(N1);
    expect(llamas.get(N1)!.requests).toHaveLength(1);
    expect(JSON.parse(llamas.get(N1)!.requests[0]!.body)).toMatchObject({ model: 'gwod-m1' });
    expect(await loadCommands('gwod-m1')).toHaveLength(1);
  });

  it('without a flip: 503 LAI-GW-022 with Retry-After 15 after loadWaitS, and the command stays queued', async () => {
    await setPolicy('gwod-m1', { loadWaitS: 1 });
    const started = Date.now();
    const response = await chat('gwod-m1');
    expect(response.statusCode).toBe(503);
    expect(response.headers['retry-after']).toBe('15');
    expect((response.json() as { error: { localai_code: string } }).error.localai_code).toBe('LAI-GW-022');
    expect(Date.now() - started).toBeGreaterThanOrEqual(900);
    const commands = await loadCommands('gwod-m1');
    expect(commands.map((row) => row.status)).toEqual(['queued']);
    expect(llamas.get(N1)!.requests).toEqual([]);
  });

  it('two concurrent requests share one command row, and both complete after the flip', async () => {
    await setPolicy('gwod-m1', { loadWaitS: 20 });
    const first = chat('gwod-m1');
    const second = chat('gwod-m1');
    await until(async () => (await loadCommands('gwod-m1')).length >= 1);
    await new Promise((resolve) => setTimeout(resolve, 300));
    expect(await loadCommands('gwod-m1')).toHaveLength(1);
    await setStatus(N1, 'gwod-m1', 'loaded');
    const [a, b] = await Promise.all([first, second]);
    expect([a.statusCode, b.statusCode]).toEqual([200, 200]);
    expect(await loadCommands('gwod-m1')).toHaveLength(1);
    const audits = await prisma.auditEvent.count({ where: { action: 'localai.command.requested', targetId: (await loadCommands('gwod-m1'))[0]!.id } });
    expect(audits).toBe(1);
  });

  it('a request that arrives while the model is LOADING waits for the running command instead of failing', async () => {
    await setPolicy('gwod-m1', { loadWaitS: 20 });
    const first = chat('gwod-m1');
    await until(async () => (await loadCommands('gwod-m1')).length === 1);
    // The node reports the model as loading: it is in no candidate list any more.
    const node = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId: N1 } });
    await prisma.localaiNodeModel.update({ where: { nodeId_modelId: { nodeId: node.id, modelId: 'gwod-m1' } }, data: { status: 'loading' } });
    await prisma.localaiCommand.updateMany({ where: { id: (await loadCommands('gwod-m1'))[0]!.id }, data: { status: 'running' } });
    await bus.publish(LOCALAI_NODE_STATE_CHANNEL, { nodeId: N1, modelsChanged: true, modeChanged: false });
    await until(() => table.loadableCandidates('gwod-m1').length === 0);
    const second = chat('gwod-m1');
    await new Promise((resolve) => setTimeout(resolve, 300));
    await setStatus(N1, 'gwod-m1', 'loaded');
    const [a, b] = await Promise.all([first, second]);
    expect([a.statusCode, b.statusCode]).toEqual([200, 200]);
    expect(await loadCommands('gwod-m1')).toHaveLength(1);
  });

  it('a load command that failed ends the wait with 503 LAI-GW-021 well before loadWaitS', async () => {
    await setPolicy('gwod-m1', { loadWaitS: 30 });
    const pending = chat('gwod-m1');
    await until(async () => (await loadCommands('gwod-m1')).length === 1);
    await prisma.localaiCommand.updateMany({ where: { id: (await loadCommands('gwod-m1'))[0]!.id }, data: { status: 'failed', finishedAt: new Date() } });
    const started = Date.now();
    const response = await pending;
    expect(response.statusCode).toBe(503);
    expect((response.json() as { error: { localai_code: string } }).error.localai_code).toBe('LAI-GW-021');
    expect(Date.now() - started).toBeLessThan(5000);
  });

  it('is off by default: no on-demand policy means 503 LAI-GW-021 and no command', async () => {
    const response = await chat('gwod-m1');
    expect(response.statusCode).toBe(503);
    expect((response.json() as { error: { localai_code: string } }).error.localai_code).toBe('LAI-GW-021');
    expect(await loadCommands()).toEqual([]);
  });

  describe('never evicts a busy model', () => {
    /** A request to the model n1 has loaded, held open until `release()`. */
    async function hold(): Promise<{ done: Promise<{ statusCode: number }>; release: () => void }> {
      let release!: () => void;
      const gate = new Promise<void>((resolve) => (release = resolve));
      const behaviour: FakeLlamaBehaviour = async (request, response, raw) => {
        await gate;
        await okBehaviour(request, response, raw);
      };
      llamas.get(N1)!.behaviour = behaviour;
      const done = chat('gwod-hold');
      await until(() => table.inflightOf({ nodeId: N1, modelId: 'gwod-hold' }) === 1);
      return { done, release };
    }

    it('skips the node whose load would unload a model that is serving a request; with none left: 503 LAI-GW-021', async () => {
      await setPolicy('gwod-m2');
      const held = await hold();
      const response = await chat('gwod-m2');
      expect(response.statusCode).toBe(503);
      expect(response.headers['retry-after']).toBe('30');
      expect((response.json() as { error: { localai_code: string } }).error.localai_code).toBe('LAI-GW-021');
      expect(await loadCommands()).toEqual([]);
      held.release();
      expect((await held.done).statusCode).toBe(200);

      // Once the node is idle the same request is allowed to evict: unload_others is true.
      const idle = await chat('gwod-m2');
      expect((idle.json() as { error: { localai_code: string } }).error.localai_code).toBe('LAI-GW-022');
      expect((await loadCommands('gwod-m2'))[0]!.params).toMatchObject({ model_id: 'gwod-m2', unload_others: true });
    });

    it('uses the next node when the first would evict a busy model', async () => {
      await setPolicy('gwod-m3');
      const held = await hold();
      const response = await chat('gwod-m3');
      expect(response.statusCode).toBe(503); // nothing flips here: it waited on n2
      const commands = await loadCommands('gwod-m3');
      expect(commands.map((row) => row.node.nodeId)).toEqual([N2]);
      expect(commands[0]!.params).toEqual({ model_id: 'gwod-m3', wait: true, unload_others: false, set_default: false });
      held.release();
      await held.done;
    });
  });

  it('a client that goes away while the load runs ends only its request: the command stays, and nothing is sent to a node', async () => {
    await setPolicy('gwod-m1', { loadWaitS: 30 });
    await app.listen({ port: 0, host: '127.0.0.1' });
    const port = (app.getHttpServer().address() as AddressInfo).port;
    const body = JSON.stringify({ model: 'gwod-m1', messages: [{ role: 'user', content: 'Hola' }] });
    const outgoing = httpRequest({ host: '127.0.0.1', port, path: '/api/gateway/v1/chat/completions', method: 'POST', headers: { authorization: `Bearer ${gatewayToken}`, 'content-type': 'application/json' } });
    outgoing.on('error', () => undefined);
    outgoing.end(body);
    await until(async () => (await loadCommands('gwod-m1')).length === 1);
    outgoing.destroy();
    await new Promise((resolve) => setTimeout(resolve, 500));

    expect((await loadCommands('gwod-m1')).map((row) => row.status)).toEqual(['queued']);
    await setStatus(N1, 'gwod-m1', 'loaded');
    await new Promise((resolve) => setTimeout(resolve, 500));
    expect(llamas.get(N1)!.requests).toEqual([]); // the aborted request did not carry on to the node
  });
});

describe('model policy routes on real SQL', () => {
  beforeEach(resetLab);

  const put = (modelId: string, body: Record<string, unknown>, bearer = adminToken) =>
    api('PUT', `/localai/gateway/models/${modelId}`, bearer, { enabled: true, aliases: [], onDemandLoad: false, loadWaitS: 120, firstByteTimeoutS: null, preferredNodes: [], ...body });

  it('stores a policy that /v1/models and the next request use at once (no cache wait), and audits it', async () => {
    const response = await put('gwod-m1', { onDemandLoad: true, loadWaitS: 45, aliases: ['gwod-lab-alias'], preferredNodes: [N1] });
    expect(response.statusCode).toBe(200);
    const row = await prisma.localaiModelPolicy.findUniqueOrThrow({ where: { modelId: 'gwod-m1' } });
    expect(row).toMatchObject({ onDemandLoad: true, loadWaitS: 45, aliases: ['gwod-lab-alias'], preferredNodes: [N1], updatedById: ownerId });

    const listed = await app.inject({ method: 'GET', url: '/api/gateway/v1/models', headers: { authorization: `Bearer ${gatewayToken}` } });
    const entries = (listed.json() as { data: Array<{ id: string; 'x-localai': { state: string; load_wait_s?: number } }> }).data;
    expect(entries.find((entry) => entry.id === 'gwod-lab-alias')?.['x-localai']).toMatchObject({ state: 'loadable', load_wait_s: 45 });

    const audit = await prisma.auditEvent.findFirstOrThrow({ where: { action: 'localai.gateway.policy_updated', targetId: 'gwod-m1' } });
    expect(audit).toMatchObject({ actorUserId: ownerId, targetType: 'localai_model_policy' });
    expect(audit.meta).toMatchObject({ changed: 'created', aliases: 'gwod-lab-alias' });
  });

  it('enabled:false removes the model and its aliases from /v1/models within one request', async () => {
    await put('gwod-m1', { onDemandLoad: true, aliases: ['gwod-lab-alias'] });
    await put('gwod-m1', { enabled: false, onDemandLoad: true, aliases: ['gwod-lab-alias'] });
    const listed = await app.inject({ method: 'GET', url: '/api/gateway/v1/models', headers: { authorization: `Bearer ${gatewayToken}` } });
    const ids = (listed.json() as { data: Array<{ id: string }> }).data.map((entry) => entry.id);
    expect(ids).not.toContain('gwod-m1');
    expect(ids).not.toContain('gwod-lab-alias');
    expect((await chat('gwod-lab-alias')).statusCode).toBe(404);
  });

  it('400 for an alias equal to a catalog id; 409 for an alias another model owns; 404 for an unknown model; nothing extra stored', async () => {
    expect((await put('gwod-m1', { aliases: ['gwod-m2'] })).statusCode).toBe(400);
    expect((await put('gwod-m1', { aliases: ['gwod-shared'] })).statusCode).toBe(200);
    expect((await put('gwod-m2', { aliases: ['gwod-shared'] })).statusCode).toBe(409);
    expect((await put('gwod-nope', {})).statusCode).toBe(404);
    expect(await prisma.localaiModelPolicy.findMany({ where: { modelId: { in: [...MODELS, 'gwod-nope'] } }, select: { modelId: true } })).toEqual([{ modelId: 'gwod-m1' }]);
  });

  it('two writes that claim one alias at the same moment: exactly one wins', async () => {
    const results = await Promise.all([put('gwod-m1', { aliases: ['gwod-race'] }), put('gwod-m2', { aliases: ['gwod-race'] }), put('gwod-m3', { aliases: ['gwod-race'] })]);
    expect(results.map((r) => r.statusCode).sort()).toEqual([200, 409, 409]);
    const owners = await prisma.localaiModelPolicy.findMany({ where: { aliases: { has: 'gwod-race' } } });
    expect(owners).toHaveLength(1);
  });

  it('GET lists the live state: loaded, loadable and unloaded, with the nodes', async () => {
    await put('gwod-m1', { onDemandLoad: true });
    const response = await api('GET', '/localai/gateway/models', adminToken);
    const rows = (response.json() as { data: { models: Array<{ modelId: string; state: string; listed: boolean; nodes: Array<{ nodeId: string; status: string }> }> } }).data.models;
    const byId = new Map(rows.map((row) => [row.modelId, row]));
    expect(byId.get('gwod-hold')).toMatchObject({ state: 'loaded', listed: true, nodes: [{ nodeId: N1, status: 'loaded' }] });
    expect(byId.get('gwod-m1')).toMatchObject({ state: 'loadable', listed: true, nodes: [{ nodeId: N1, status: 'loadable' }] });
    expect(byId.get('gwod-m2')).toMatchObject({ state: 'unloaded', listed: false });
  });
});
