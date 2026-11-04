// `GET /v1/models` on a real database (docs/specs/gateway.md section 3.4 and 4.4, issue GW-05): the whole app on a
// migrated database, a seeded approved catalog and model policies, three lab nodes in `localai_nodes` /
// `localai_node_models`, a real `lai_` key row, and the request sent through the real gateway routes. What only the real
// stack proves: the approved-catalog query (one approved row, `models.yaml` read from the stored files), the policy
// query, the routing table's join feeding the listing, and the key's scope read from `allowed_models`.
// Everything the spec creates it removes, and the approved catalog it displaced is approved again. Every id is invented.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';

import { hashGatewayToken } from '../../src/localai/gateway/token-verify';
import { ModelDirectoryProvider, ModelResolver } from '../../src/localai/gateway/models/resolve';
import { RoutingTable } from '../../src/localai/gateway/routing/routing-table';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';

const run = `${Date.now()}`;
const NODE_IDS = ['ai-n1', 'ai-n2', 'ai-n3'] as const;
// Built at run time so no address literal sits in a public file.
const TAILNET_IP = ['100', '64', '0', '9'].join('.');
const VERSION = `2026.11.02-${run.slice(-6)}`;
const APPROVED_AT = new Date('2026-11-02T12:00:00.000Z');

const MODELS_YAML = `schema_version: 1
models:
  - id: qwen3.5-9b-q4km
    display_name: "Qwen 3.5 9B"
    engine: llama
    roles: [chat, reasoning, coder]
    llama: { ctx: 65536 }
    client_hints: { max_output_tokens: 8192, reasoning: true }
  - id: qwen3.6-35b-a3b-q4km
    display_name: "Qwen 3.6 35B A3B"
    engine: llama
    roles: [chat, reasoning]
    llama: { ctx: 65536 }
    client_hints: { max_output_tokens: 8192, reasoning: true }
  - id: qwen3-coder-next-q4km
    display_name: "Qwen3 Coder Next"
    engine: llama
    roles: [coder, chat]
    llama: { ctx: 131072 }
    client_hints: { max_output_tokens: 16384, reasoning: false }
  - id: qwen3.5-4b-q4km
    display_name: "Qwen 3.5 4B"
    engine: llama
    roles: [chat]
    llama: { ctx: 32768 }
    client_hints: { max_output_tokens: 4096, reasoning: false }
`;

const FIXTURE = JSON.parse(readFileSync(join(__dirname, '../fixtures/localai/gateway-models.json'), 'utf8')) as { object: string; data: Array<{ id: string }> };

let app: NestFastifyApplication;
let prisma: PrismaService;
let table: RoutingTable;
let ownerId: string;
let displaced: string[] = [];
const keyIds: string[] = [];
const tokens = new Map<string, string>();

function newToken(): string {
  return `lai_${createHash('sha256').update(`${run}-${tokens.size}-${Math.random()}`).digest('base64url')}`;
}

async function addKey(name: string, allowedModels: string[]): Promise<string> {
  const token = newToken();
  const row = await prisma.localaiGatewayKey.create({
    data: {
      userId: ownerId,
      name: `gw05 ${name} ${run}`,
      kind: 'service',
      tokenHash: hashGatewayToken(token),
      tokenPrefix: token.slice(0, 10),
      allowedModels,
      rpmLimit: 600,
      concurrentLimit: 8,
    },
  });
  keyIds.push(row.id);
  tokens.set(name, token);
  return token;
}

async function addNode(nodeId: string, models: Array<{ modelId: string; status: 'loaded' | 'unloaded'; ctx: number }>): Promise<void> {
  const worker = await prisma.workerNode.create({
    data: { name: nodeId, hostname: 'host-alpha', platform: 'linux/amd64', cliVersion: 'aiagent/0.4.0', eligibleTypes: [], concurrency: 1, status: 'online', lastHeartbeatAt: new Date(), createdById: ownerId },
  });
  await prisma.localaiNode.create({
    data: {
      nodeId,
      displayName: `Node ${nodeId}`,
      publicHost: `${nodeId}.example.test`,
      servePattern: 'B',
      tailnetIp: TAILNET_IP,
      tailnetMachine: `${nodeId}.tailnet-alpha.ts.net`,
      phase: 'enrolled',
      mode: 'online',
      lastStateAt: new Date(),
      keyReportedAt: new Date(),
      workerNodeId: worker.id,
      createdById: ownerId,
      models: {
        create: models.map((model) => ({ modelId: model.modelId, present: true, verified: true, loadable: true, status: model.status, slots: 1, roles: [], inputModalities: ['text'], ctx: model.ctx })),
      },
    },
  });
}

async function cleanup(): Promise<void> {
  await prisma.localaiNode.deleteMany({ where: { nodeId: { in: [...NODE_IDS] } } });
  await prisma.workerNode.deleteMany({ where: { name: { in: [...NODE_IDS] } } });
  await prisma.localaiModelPolicy.deleteMany({ where: { modelId: { in: ['qwen3-coder-next-q4km', 'qwen3.6-35b-a3b-q4km'] } } });
  await prisma.localaiCatalogVersion.deleteMany({ where: { version: VERSION } });
}

const get = (path: string, token: string) => app.inject({ method: 'GET', url: `/api/gateway/v1${path}`, headers: { authorization: `Bearer ${token}` } });
const idsOf = (body: unknown): string[] => (body as { data: Array<{ id: string }> }).data.map((entry) => entry.id);

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  table = app.get(RoutingTable, { strict: false });
  const email = `gw-models-${run}@example.test`;
  const login = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role: 'admin' } });
  expect(login.statusCode).toBeLessThan(400);
  ownerId = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
  await cleanup();

  // At most one catalog is approved (a partial unique index): displace the current one, restore it afterwards.
  const current = await prisma.localaiCatalogVersion.findMany({ where: { status: 'approved' }, select: { version: true } });
  displaced = current.map((row) => row.version);
  await prisma.localaiCatalogVersion.updateMany({ where: { status: 'approved' }, data: { status: 'superseded' } });
  await prisma.localaiCatalogVersion.create({
    data: {
      version: VERSION,
      source: 'editor',
      status: 'approved',
      files: [{ path: 'models.yaml', content: MODELS_YAML, sha256: createHash('sha256').update(MODELS_YAML).digest('hex') }],
      bundleSha256: createHash('sha256').update(MODELS_YAML).digest('hex'),
      approvedAt: APPROVED_AT,
    },
  });
  await prisma.localaiModelPolicy.createMany({
    data: [
      { modelId: 'qwen3-coder-next-q4km', aliases: ['lab-coder'] },
      { modelId: 'qwen3.6-35b-a3b-q4km', onDemandLoad: true, loadWaitS: 120 },
    ],
  });
  await addNode('ai-n1', [{ modelId: 'qwen3-coder-next-q4km', status: 'loaded', ctx: 131072 }]);
  await addNode('ai-n2', [
    { modelId: 'qwen3.6-35b-a3b-q4km', status: 'unloaded', ctx: 32768 },
    { modelId: 'qwen3.5-4b-q4km', status: 'unloaded', ctx: 32768 },
  ]);
  await addNode('ai-n3', [{ modelId: 'qwen3.5-9b-q4km', status: 'loaded', ctx: 32768 }]);
  await addKey('open', []);
  await addKey('scoped', ['qwen3.5-9b-q4km']);
  await addKey('alias', ['lab-coder']);
  app.get(ModelDirectoryProvider, { strict: false }).invalidate();
  await table.rebuild();
});

afterAll(async () => {
  if (prisma) {
    await prisma.localaiGatewayKey.deleteMany({ where: { id: { in: keyIds } } });
    await cleanup();
    if (displaced.length > 0) await prisma.localaiCatalogVersion.updateMany({ where: { version: { in: displaced } }, data: { status: 'approved' } });
  }
  await app?.close();
});

describe('GET /v1/models on real SQL', () => {
  it('lists the fixture lab exactly as the fixture file', async () => {
    const response = await get('/models', tokens.get('open')!);
    expect(response.statusCode).toBe(200);
    expect(response.json()).toEqual(FIXTURE);
  });

  it('never carries a host, an address or a tailnet name', async () => {
    const text = (await get('/models', tokens.get('open')!)).body;
    expect(text).not.toMatch(/example\.test|tailnet|\.ts\.net/);
    expect(text).not.toContain(TAILNET_IP);
  });

  it('filters by the key scope read from allowed_models', async () => {
    expect(idsOf((await get('/models', tokens.get('scoped')!)).json())).toEqual(['qwen3.5-9b-q4km']);
    expect(idsOf((await get('/models', tokens.get('alias')!)).json())).toEqual(['lab-coder']);
    expect((await get('/models/lab-coder', tokens.get('scoped')!)).statusCode).toBe(404);
    expect((await get('/models/qwen3-coder-next-q4km', tokens.get('alias')!)).statusCode).toBe(404);
    expect((await get('/models/lab-coder', tokens.get('alias')!)).statusCode).toBe(200);
  });

  it('answers one entry by id and 404 LAI-GW-020 for an unlisted one', async () => {
    const one = await get('/models/qwen3.6-35b-a3b-q4km', tokens.get('open')!);
    expect(one.json()).toEqual(FIXTURE.data.find((entry) => entry.id === 'qwen3.6-35b-a3b-q4km'));
    const missing = await get('/models/qwen3.5-4b-q4km', tokens.get('open')!); // loadable, but its policy has no on-demand load
    expect(missing.statusCode).toBe(404);
    expect(missing.json().error.localai_code).toBe('LAI-GW-020');
  });

  it('resolves an alias and reads the policy through the same directory', async () => {
    const resolved = await app.get(ModelResolver, { strict: false }).resolve('lab-coder');
    expect(resolved).toMatchObject({ id: 'qwen3-coder-next-q4km', alias: 'lab-coder', policy: { enabled: true } });
  });

  it('drops a model and its alias when its policy is disabled, once the directory is refreshed', async () => {
    await prisma.localaiModelPolicy.update({ where: { modelId: 'qwen3-coder-next-q4km' }, data: { enabled: false } });
    app.get(ModelDirectoryProvider, { strict: false }).invalidate();
    expect(idsOf((await get('/models', tokens.get('open')!)).json())).toEqual(['qwen3.5-9b-q4km', 'qwen3.6-35b-a3b-q4km']);
    await prisma.localaiModelPolicy.update({ where: { modelId: 'qwen3-coder-next-q4km' }, data: { enabled: true } });
    app.get(ModelDirectoryProvider, { strict: false }).invalidate();
  });

  it('is an empty list when no catalog is approved', async () => {
    await prisma.localaiCatalogVersion.update({ where: { version: VERSION }, data: { status: 'superseded' } });
    app.get(ModelDirectoryProvider, { strict: false }).invalidate();
    const response = await get('/models', tokens.get('open')!);
    expect(response.statusCode).toBe(200);
    expect(response.json()).toEqual({ object: 'list', data: [] });
    await prisma.localaiCatalogVersion.update({ where: { version: VERSION }, data: { status: 'approved' } });
    app.get(ModelDirectoryProvider, { strict: false }).invalidate();
  });

  it('refuses a request without a key', async () => {
    const response = await app.inject({ method: 'GET', url: '/api/gateway/v1/models' });
    expect(response.statusCode).toBe(401);
  });
});
