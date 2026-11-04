// The audit trail API against real SQL (CLOUD-16, docs/specs/cloud-app.md sections 3.5, 3.9 and 9): the whole app on a
// migrated database. One story node goes through the REAL flows that write audit rows today (the cloud link: enrol,
// key, state with a mode change, a refused credential; the command plane: request, claim, result, cancel; the catalog:
// draft, lint, approve), and the rows are then read back ONLY through `GET /api/localai/audit` as the lab admin.
//
// The wizard and node routes (CLOUD-14, CLOUD-12) write node.created, credential_minted, credential_revoked, updated,
// retired, tailnet_revealed and checklist.updated: they come from the REAL routes on a wizard node. The actions whose
// writers have not landed on this branch (the gateway) are inserted with the exact
// `targetType` and `meta` shapes of the §3.5 table, so the page is proven against every action the table lists; when
// those items land, their own flows can replace the inserts.
//
// What only real SQL proves: the `LIKE` prefix with `_` taken literally, the JSON-path node filter, the time window
// and offset paging order, and the count.
process.env.JOBS_WORKER_MODE = 'off';

import { randomBytes } from 'node:crypto';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import type { Prisma } from '@prisma/client';
import { JobHandlerRegistry, type Job } from '@marinoscar/platform-api/jobs';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import { KNOWN_AUDIT_ACTIONS } from '../../src/localai/audit/audit-actions';
import { CATALOG_LINTER, type CatalogLinter } from '../../src/localai/catalog/catalog-linter';
import { fingerprintOf } from '../../src/localai/contracts/key.schema';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { repoCatalogFiles } from './helpers/tar-builder';
import { ai3 } from './state-fixtures';

// The node key is stored encrypted: the suite needs a key whatever the environment holds (CI sets none).
process.env.SECRETS_ENCRYPTION_KEY ??= randomBytes(32).toString('base64');

const run = `${Date.now()}`;
const STARTED = new Date(Date.now() - 1000);
const LINT = 'localai.catalog.lint';
const CATALOG_VERSION = '2097.05.01-1';
const HEX64 = /\b[0-9a-f]{64}\b/;
const NOD_TOKEN = /\bnod_[A-Za-z0-9_-]{20,}/;
const WIZARD_NODE = 'ai-n9203';

type Role = 'admin' | 'contributor' | 'viewer';
interface Row {
  id: string;
  at: string;
  actor: { kind: string; label: string };
  action: string;
  targetType: string;
  targetId: string;
  summary: string;
  link: string | null;
}
interface Page {
  items: Row[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
}

let app: NestFastifyApplication;
let prisma: PrismaService;
let credentials: NodeCredentialService;
let registry: JobHandlerRegistry;
const tokens = {} as Record<Role, string>;
const emails = {} as Record<Role, string>;
const userIds = {} as Record<Role, string>;
const credentialIds: string[] = [];
const labNodeIds: string[] = [];
const bodies: string[] = [];
const seededTargetIds: string[] = [];
const commandIds: string[] = [];
let story: { nodeId: string; workerId: string; token: string };
let other: { nodeId: string; workerId: string; token: string };
let secretKey = '';

async function login(role: Role): Promise<void> {
  emails[role] = `audit-${role}-${run}@example.test`;
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email: emails[role], role } });
  tokens[role] = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  userIds[role] = (await prisma.user.findFirstOrThrow({ where: { email: emails[role] } })).id;
}

/** An enrolled node with its own real credential, registered as a worker (what the agent does first). */
async function enrolled(slot: number) {
  // The snapshot's node_id must match ^ai-n[1-9][0-9]{0,3}$, so a node id cannot carry a run suffix: a high range no
  // real lab uses, cleaned up before and after.
  const nodeId = `ai-n${9200 + slot}`;
  const minted = await credentials.createCredential(userIds.admin, { name: `${nodeId} credential` } as never);
  credentialIds.push(minted.id);
  labNodeIds.push(nodeId);
  await prisma.localaiNode.create({
    data: { nodeId, displayName: `Node ${nodeId}`, publicHost: `${nodeId}.example.test`, servePattern: 'B', phase: 'pending', nodeCredentialId: minted.id, createdById: userIds.admin },
  });
  const registered = await app.inject({
    method: 'POST',
    url: '/api/nodes/register',
    headers: { authorization: `Bearer ${minted.token}` },
    payload: { name: nodeId, hostname: 'host-alpha', platform: 'linux/amd64', cliVersion: 'aiagent/0.4.0', eligibleTypes: ['localai.status.refresh'], concurrency: 1, capabilities: { localai: { schema: 1, agent_version: '0.4.0' } } },
  });
  expect([200, 201]).toContain(registered.statusCode);
  const workerId = (registered.json() as { data: { node: { id: string } } }).data.node.id;
  return { nodeId, workerId, token: minted.token };
}

const nodeRoute = (node: { workerId: string; token: string }, method: 'GET' | 'POST', path: string, payload?: unknown) =>
  app.inject({ method, url: `/api/nodes/${node.workerId}/localai/${path}`, headers: { authorization: `Bearer ${node.token}`, 'content-type': 'application/json' }, ...(payload === undefined ? {} : { payload: JSON.stringify(payload) }) });

const ui = (method: 'GET' | 'POST' | 'DELETE' | 'PATCH', url: string, role: Role, payload?: unknown) =>
  app.inject({ method, url: `/api/localai${url}`, headers: { authorization: `Bearer ${tokens[role]}` }, ...(payload === undefined ? {} : { payload: payload as object }) });

async function audit(query: string, role: Role = 'admin'): Promise<Page> {
  const response = await ui('GET', `/audit?${query}`, role);
  bodies.push(response.body);
  expect(response.statusCode).toBe(200);
  return (response.json() as { data: Page }).data;
}

/** Every page of a query. */
async function auditAll(query: string): Promise<Row[]> {
  const rows: Row[] = [];
  for (let page = 1; ; page += 1) {
    const result = await audit(`${query}&page=${page}`);
    rows.push(...result.items);
    if (page >= result.totalPages) return rows;
  }
}

const since = `from=${encodeURIComponent(STARTED.toISOString())}`;
const snapshotFor = (nodeId: string, seq: number, mode: 'online' | 'offline') => {
  const snapshot = ai3();
  snapshot.node_id = nodeId;
  snapshot.boot_id = '5f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d0001';
  snapshot.seq = seq;
  snapshot.mode = mode;
  return snapshot;
};
async function eventually<T>(read: () => Promise<T>, done: (value: T) => boolean): Promise<T> {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    const value = await read();
    if (done(value)) return value;
    await new Promise((resolve) => setTimeout(resolve, 40));
  }
  return read();
}

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  credentials = app.get(NodeCredentialService);
  registry = app.get(JobHandlerRegistry);
  for (const role of ['admin', 'contributor', 'viewer'] as const) await login(role);
  await removeLeftovers();
  // A key shaped like a real one (64 hex), unique per run, to prove it never reaches the audit page.
  secretKey = randomBytes(32).toString('hex');
  jest.spyOn(app.get<CatalogLinter>(CATALOG_LINTER), 'lint').mockResolvedValue({ status: 'valid', result: { valid: true, problems: [], exitCode: 0 } });
});

/** Rows a crashed earlier run may have left on the fixed node ids. */
async function removeLeftovers(): Promise<void> {
  const ids = [9201, 9202, 9203].map((n) => `ai-n${n}`);
  await prisma.auditEvent.deleteMany({ where: { targetId: { in: ids } } });
  await prisma.nodeCredential.deleteMany({ where: { name: { in: ids.map((id) => `localai-${id}`) } } });
  await prisma.localaiNode.deleteMany({ where: { nodeId: { in: ids } } });
  await prisma.workerNode.deleteMany({ where: { name: { in: ids } } });
}

afterAll(async () => {
  jest.restoreAllMocks();
  if (prisma) {
    await prisma.auditEvent.deleteMany({
      where: {
        OR: [
          { targetId: { in: [...labNodeIds, ...commandIds, ...seededTargetIds, CATALOG_VERSION] } },
          { actorUserId: { in: Object.values(userIds) }, action: { startsWith: 'localai.' } },
        ],
      },
    });
    await prisma.localaiCommand.deleteMany({ where: { node: { nodeId: { in: labNodeIds } } } });
    await prisma.credential.deleteMany({ where: { purpose: 'localai_node_llama_key', name: { in: labNodeIds } } });
    await prisma.localaiNode.deleteMany({ where: { nodeId: { in: labNodeIds } } });
    await prisma.localaiCatalogVersion.deleteMany({ where: { version: CATALOG_VERSION } });
    await prisma.job.deleteMany({ where: { type: LINT, subjectId: CATALOG_VERSION } });
    await prisma.workerNode.deleteMany({ where: { createdById: userIds.admin } });
    await prisma.nodeCredential.deleteMany({ where: { OR: [{ id: { in: credentialIds } }, { name: `localai-${WIZARD_NODE}` }] } });
  }
  await app?.close();
});

describe('the flows that write audit rows today', () => {
  it('replays node-link, key, state, commands and catalog on a story node', async () => {
    story = await enrolled(1);
    other = await enrolled(2);

    // Cloud link: the first bound call enrols the node; a foreign credential on this node's path is refused.
    expect((await nodeRoute(story, 'GET', 'config')).statusCode).toBe(200);
    expect((await nodeRoute(other, 'GET', 'config')).statusCode).toBe(200);
    const refused = await app.inject({ method: 'GET', url: `/api/nodes/${story.workerId}/localai/config`, headers: { authorization: `Bearer ${other.token}` } });
    expect(refused.statusCode).toBe(403);

    // The node reports its key (64 hex) and a first online snapshot.
    expect((await nodeRoute(story, 'POST', 'key', { key: secretKey, fingerprint: fingerprintOf(secretKey), rotated_at: '2026-11-02T15:04:05Z' })).statusCode).toBe(200);
    expect((await nodeRoute(story, 'POST', 'state', snapshotFor(story.nodeId, 1, 'online'))).statusCode).toBe(200);

    // Commands: contributor loads a model (the agent claims it and reports), then queues one that is cancelled.
    const model = ai3().models.find((entry) => entry.present)!.id;
    const loaded = await ui('POST', `/nodes/${story.nodeId}/commands`, 'contributor', { type: 'model.load', params: { model_id: model } });
    expect(loaded.statusCode).toBe(201);
    const loadId = (loaded.json() as { data: { id: string } }).data.id;
    commandIds.push(loadId);
    const claimed = ((await nodeRoute(story, 'POST', 'commands/claim', { types: ['model.load', 'status.refresh'], limit: 1, waitMs: 0 })).json() as { data: { jobs: Array<{ claimToken: string }> } }).data.jobs[0]!;
    const result = { model_id: model, status: 'loaded', load_ms: 1000, unloaded: [], vram_used_mib: null, is_default: false };
    expect((await nodeRoute(story, 'POST', `commands/${loadId}/result`, { type: 'localai.model.load', claimToken: claimed.claimToken, result })).statusCode).toBe(200);
    await eventually(() => prisma.auditEvent.findMany({ where: { action: 'localai.command.settled', targetId: loadId } }), (rows) => rows.length > 0);

    const queued = await ui('POST', `/nodes/${story.nodeId}/commands`, 'contributor', { type: 'status.refresh' });
    const cancelId = (queued.json() as { data: { id: string } }).data.id;
    commandIds.push(cancelId);
    expect((await ui('DELETE', `/commands/${cancelId}`, 'contributor')).statusCode).toBe(200);

    // Catalog: an editor draft, its lint (the linter is faked to "valid"), then approval.
    const files = [...repoCatalogFiles()].filter(([path]) => path !== 'CATALOG_VERSION').map(([path, bytes]) => ({ path, content: bytes.toString('utf8') }));
    await prisma.localaiCatalogVersion.deleteMany({});
    const draft = await app.inject({ method: 'POST', url: '/api/localai/catalog/versions', headers: { authorization: `Bearer ${tokens.admin}` }, payload: { version: CATALOG_VERSION, files } as object });
    expect(draft.statusCode).toBe(201);
    const lintJob = await prisma.job.findFirstOrThrow({ where: { type: LINT, subjectId: CATALOG_VERSION }, orderBy: { createdAt: 'desc' } });
    await registry.get(LINT)!.process(lintJob as unknown as Job);
    expect((await app.inject({ method: 'POST', url: `/api/localai/catalog/versions/${CATALOG_VERSION}/approve`, headers: { authorization: `Bearer ${tokens.admin}` }, payload: {} })).statusCode).toBe(200);

    // The node later goes to Offline mode (the 'mode changed' row).
    expect((await nodeRoute(story, 'POST', 'state', snapshotFor(story.nodeId, 2, 'offline'))).statusCode).toBe(200);

    // The wizard (CLOUD-14): create a node (created + credential_minted), then mark a checklist step (checklist.updated).
    labNodeIds.push(WIZARD_NODE);
    const created = await ui('POST', '/nodes', 'admin', { nodeId: WIZARD_NODE, displayName: 'Wizard node' });
    expect(created.statusCode).toBe(201);
    expect((await ui('POST', `/nodes/${WIZARD_NODE}/checklist/tailnet`, 'admin', { action: 'mark_done' })).statusCode).toBe(200);
    // The node routes (CLOUD-12): edit, reveal the tailnet address (audited), replace the credential (minted + revoked), retire.
    expect((await ui('PATCH', `/nodes/${WIZARD_NODE}`, 'admin', { displayName: 'Wizard node two', gatewayPriority: 50 })).statusCode).toBe(200);
    expect((await ui('GET', `/nodes/${WIZARD_NODE}?reveal=tailnet`, 'admin')).statusCode).toBe(200);
    expect((await ui('POST', `/nodes/${WIZARD_NODE}/credential`, 'admin', {})).statusCode).toBe(201);
    expect((await ui('POST', `/nodes/${WIZARD_NODE}/retire`, 'admin', { confirm: WIZARD_NODE })).statusCode).toBe(200);

    // The alerts (CLOUD-15): an active alert on the story node (its own row, so no other suite's alert can collide with
    // the one-active-per-(node, kind) index), acknowledged through the real route.
    const storyRow = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId: story.nodeId }, select: { id: true } });
    const alert = await prisma.localaiAlert.create({ data: { nodeId: storyRow.id, kind: 'node.stale', code: 'LAI-CLD-020', severity: 'warning', title: 'Node is quiet' } });
    const acked = await ui('POST', `/alerts/${alert.id}/ack`, 'admin');
    expect(acked.statusCode).toBeLessThan(300);
  });

  it('inserts the rows of the actions whose writers are not on this branch (the gateway), in the §3.5 shapes', async () => {
    seededTargetIds.push('gw-key-1', 'gw-policy-1');
    await prisma.auditEvent.createMany({
      data: [
        { actorUserId: userIds.admin, action: 'localai.gateway.key_created', targetType: 'localai_gateway_key', targetId: 'gw-key-1', meta: { prefix: 'lai_ab12', name: 'Desk laptop' } },
        { actorUserId: userIds.admin, action: 'localai.gateway.key_revoked', targetType: 'localai_gateway_key', targetId: 'gw-key-1', meta: { prefix: 'lai_ab12' } },
        { actorUserId: userIds.admin, action: 'localai.gateway.key_updated', targetType: 'localai_gateway_key', targetId: 'gw-key-1', meta: { prefix: 'lai_ab12', name: 'Desk' } },
        { actorUserId: null, action: 'localai.gateway.policy_updated', targetType: 'localai_model_policy', targetId: 'gw-policy-1', meta: { allowedModels: 'x' } },
      ],
    });
  });
});

describe('every action of the §3.5 table appears', () => {
  it('after the flows, the audit API lists every known action at least once, each with a summary', async () => {
    const rows = await auditAll(since);
    const seen = new Set(rows.map((row) => row.action));
    const missing = KNOWN_AUDIT_ACTIONS.map((def) => def.action).filter((action) => !seen.has(action));
    expect(missing).toEqual([]);
    for (const row of rows) {
      expect(row.summary.length).toBeGreaterThan(0);
      expect(row.action.startsWith('localai.')).toBe(true);
    }
  });

  it('labels actors: the node for its own actions, the requester for commands, the admin for catalog and the rest', async () => {
    const rows = await auditAll(`${since}&nodeId=${story.nodeId}`);
    const actorOf = (action: string) => rows.find((row) => row.action === action)?.actor;
    expect(actorOf('localai.node.enrolled')).toEqual({ kind: 'node', label: `node ${story.nodeId}` });
    expect(actorOf('localai.node.key_reported')).toEqual({ kind: 'node', label: `node ${story.nodeId}` });
    expect(actorOf('localai.node.mode_changed')).toEqual({ kind: 'node', label: `node ${story.nodeId}` });
    expect(actorOf('localai.node.binding_refused')).toEqual({ kind: 'node', label: `node ${story.nodeId}` });
    expect(actorOf('localai.command.requested')).toEqual({ kind: 'user', label: emails.contributor });
    expect(actorOf('localai.command.settled')).toEqual({ kind: 'user', label: emails.contributor });
    const wizard = await auditAll(`${since}&nodeId=${WIZARD_NODE}`);
    expect(wizard.map((row) => row.action).sort()).toEqual([
      'localai.checklist.updated',
      'localai.node.created',
      'localai.node.credential_minted',
      'localai.node.credential_minted',
      'localai.node.credential_revoked',
      'localai.node.credential_revoked',
      'localai.node.retired',
      'localai.node.tailnet_revealed',
      'localai.node.updated',
    ]);
    for (const row of wizard) expect(row.actor).toEqual({ kind: 'user', label: emails.admin });
  });

  it('summaries and links of the real rows read as plain sentences pointing at the target', async () => {
    const rows = await auditAll(since);
    const one = (action: string, predicate: (row: Row) => boolean = () => true) => rows.find((row) => row.action === action && predicate(row))!;
    const model = ai3().models.find((entry) => entry.present)!.id;
    const requested = one('localai.command.requested', (row) => row.summary.startsWith('model.load'));
    expect(requested.summary).toBe(`model.load ${model} on ${story.nodeId}`);
    expect(requested.link).toBe(`/lab/nodes/${story.nodeId}?command=${requested.targetId}`);
    expect(one('localai.command.settled', (row) => row.targetId === requested.targetId).summary).toBe(`model.load on ${story.nodeId} succeeded`);
    expect(one('localai.command.cancelled').summary).toBe(`Cancelled status.refresh on ${story.nodeId}`);
    expect(one('localai.node.mode_changed').summary).toBe(`${story.nodeId} mode changed from online to offline`);
    expect(one('localai.node.enrolled', (row) => row.targetId === story.nodeId).link).toBe(`/lab/nodes/${story.nodeId}`);
    expect(one('localai.node.key_reported').summary).toBe(`${story.nodeId} reported its key (enroll) hint ${fingerprintOf(secretKey)}`);
    expect(one('localai.node.binding_refused').summary).toContain(`claiming ${story.nodeId}`);
    expect(one('localai.alert.acknowledged').link).toBe('/lab/alerts');
    expect(one('localai.alert.acknowledged').summary).toBe(`Acknowledged node.stale alert on ${story.nodeId}`);
    expect(one('localai.gateway.key_created').link).toBe('/gateway/keys');

    const wizard = await auditAll(`${since}&nodeId=${WIZARD_NODE}`);
    const wizardRow = (action: string) => wizard.find((row) => row.action === action)!;
    expect(wizardRow('localai.node.created').summary).toBe(`Added node ${WIZARD_NODE} (${WIZARD_NODE}.marin.cr)`);
    expect(wizardRow('localai.node.credential_minted').summary).toMatch(new RegExp(`^Created credential nod_[0-9a-f]{4} for ${WIZARD_NODE}$`));
    expect(wizardRow('localai.node.credential_revoked').summary).toMatch(new RegExp(`^Revoked credential nod_[0-9a-f]{4} of ${WIZARD_NODE}$`));
    expect(wizardRow('localai.node.updated').summary).toBe(`Changed displayName, gatewayPriority on ${WIZARD_NODE}`);
    expect(wizardRow('localai.node.tailnet_revealed').summary).toBe(`Tailnet address of ${WIZARD_NODE} revealed`);
    expect(wizardRow('localai.node.retired').summary).toBe(`Retired node ${WIZARD_NODE}`);
    expect(wizardRow('localai.checklist.updated').summary).toBe(`Checklist step tailnet of ${WIZARD_NODE} is now done (mark_done)`);
    for (const row of wizard) expect(row.link).toBe(`/lab/nodes/${WIZARD_NODE}`);

    const catalog = (await auditAll(`${since}&action=localai.catalog.`)).filter((row) => row.targetId === CATALOG_VERSION);
    expect(catalog.map((row) => row.action).sort()).toEqual(['localai.catalog.approved', 'localai.catalog.imported', 'localai.catalog.linted']);
    for (const row of catalog) expect(row.link).toBe(`/lab/catalog?version=${CATALOG_VERSION}`);
  });
});

describe('filters', () => {
  it('the node filter finds the node rows AND the command rows that only name it in meta; another node sees neither', async () => {
    const mine = await auditAll(`${since}&nodeId=${story.nodeId}`);
    const actions = new Set(mine.map((row) => row.action));
    expect(actions.has('localai.command.requested')).toBe(true);
    expect(actions.has('localai.command.settled')).toBe(true);
    expect(actions.has('localai.node.binding_refused')).toBe(true);
    expect(actions.has('localai.catalog.approved')).toBe(false);
    for (const row of mine) expect(row.link === null || row.link.startsWith(`/lab/nodes/${story.nodeId}`) || row.link === '/lab/alerts' || row.link === '/gateway/keys').toBe(true);

    const theirs = await auditAll(`${since}&nodeId=${other.nodeId}`);
    expect(theirs.map((row) => row.action).sort()).toEqual(['localai.node.enrolled']);
    expect(theirs.some((row) => row.action.startsWith('localai.command.'))).toBe(false);
  });

  it('the action filter is a prefix, and "_" in it is a literal underscore, not a wildcard', async () => {
    const commands = await auditAll(`${since}&action=localai.command.`);
    expect(commands.length).toBeGreaterThanOrEqual(4);
    for (const row of commands) expect(row.action.startsWith('localai.command.')).toBe(true);

    seededTargetIds.push(`decoy-${run}`);
    await prisma.auditEvent.create({ data: { actorUserId: userIds.admin, action: 'localai.node.modexchanged', targetType: 'localai_node', targetId: `decoy-${run}`, meta: {} } });
    const modes = await auditAll(`${since}&action=localai.node.mode_changed`);
    expect(modes.length).toBeGreaterThanOrEqual(1);
    expect(modes.every((row) => row.action === 'localai.node.mode_changed')).toBe(true);
    // The unknown action still lists and still renders as its own name.
    const decoy = (await auditAll(`${since}&action=localai.node.modexchanged`)).find((row) => row.targetId === `decoy-${run}`)!;
    expect(decoy.summary).toBe('localai.node.modexchanged');
  });

  it('node, action and date range combine', async () => {
    const both = await auditAll(`${since}&nodeId=${story.nodeId}&action=localai.command.settled`);
    expect(both.map((row) => row.action)).toEqual(['localai.command.settled']);

    const future = await audit(`from=${encodeURIComponent(new Date(Date.now() + 3_600_000).toISOString())}&nodeId=${story.nodeId}`);
    expect(future).toMatchObject({ total: 0, items: [], totalPages: 1 });
    const past = await audit(`from=2020-01-01&to=2020-01-31&nodeId=${story.nodeId}`);
    expect(past.total).toBe(0);
    const day = new Date().toISOString().slice(0, 10);
    const today = await audit(`from=${day}&to=${day}&nodeId=${story.nodeId}&action=localai.command.`);
    expect(today.total).toBe(4); // requested + settled of the load, requested + cancelled of the other
  });

  it('defaults to the last 30 days; an explicit from reaches further back', async () => {
    const pageNode = `ai-n-page-${run}`;
    seededTargetIds.push(pageNode);
    const base = Date.now() - 600_000;
    await prisma.auditEvent.createMany({
      data: [
        ...Array.from({ length: 120 }, (_unused, index) => ({ actorUserId: userIds.admin, action: 'localai.node.updated', targetType: 'localai_node', targetId: pageNode, meta: { displayName: 'x' }, createdAt: new Date(base + index * 1000) })),
        { actorUserId: userIds.admin, action: 'localai.node.created', targetType: 'localai_node', targetId: pageNode, meta: {}, createdAt: new Date(Date.now() - 40 * 24 * 3_600_000) },
      ],
    });
    expect((await audit(`nodeId=${pageNode}`)).total).toBe(120);
    const longer = new Date(Date.now() - 45 * 24 * 3_600_000).toISOString().slice(0, 10);
    expect((await audit(`nodeId=${pageNode}&from=${longer}`)).total).toBe(121);
  });

  it('pages of 50, newest first, no row twice or missed', async () => {
    const pageNode = `ai-n-page-${run}`;
    const pages = [await audit(`nodeId=${pageNode}&page=1`), await audit(`nodeId=${pageNode}&page=2`), await audit(`nodeId=${pageNode}&page=3`), await audit(`nodeId=${pageNode}&page=4`)];
    expect(pages.map((page) => page.items.length)).toEqual([50, 50, 20, 0]);
    expect(pages.map((page) => [page.total, page.totalPages, page.pageSize])).toEqual([[120, 3, 50], [120, 3, 50], [120, 3, 50], [120, 3, 50]]);
    const all = pages.flatMap((page) => page.items);
    expect(new Set(all.map((row) => row.id)).size).toBe(120);
    const times = all.map((row) => Date.parse(row.at));
    expect([...times].sort((a, b) => b - a)).toEqual(times);
    expect(all[0]!.summary).toBe(`Changed displayName on ${pageNode}`);
  });
});

describe('who may read', () => {
  it('only the lab admin: viewer, contributor and no session are refused, and nothing is returned', async () => {
    for (const role of ['viewer', 'contributor'] as const) {
      const response = await ui('GET', '/audit', role);
      expect(response.statusCode).toBe(403);
      expect(response.body).not.toContain('localai.');
      expect((await ui('GET', '/audit/filters', role)).statusCode).toBe(403);
    }
    expect((await app.inject({ method: 'GET', url: '/api/localai/audit' })).statusCode).toBe(401);
  });

  it('the filters route lists the known actions and the nodes, retired ones included', async () => {
    await prisma.localaiNode.update({ where: { nodeId: other.nodeId }, data: { phase: 'retired' } });
    const response = await ui('GET', '/audit/filters', 'admin');
    expect(response.statusCode).toBe(200);
    const data = (response.json() as { data: { actions: Array<{ action: string }>; nodes: Array<{ nodeId: string; phase: string }> } }).data;
    expect(data.actions.map((entry) => entry.action)).toEqual(KNOWN_AUDIT_ACTIONS.map((entry) => entry.action));
    expect(data.nodes.find((node) => node.nodeId === other.nodeId)).toMatchObject({ phase: 'retired' });
    expect(data.nodes.find((node) => node.nodeId === story.nodeId)).toBeDefined();
  });
});

describe('secrets', () => {
  it('no audit response held a 64-hex key, a node token or the node key, whatever the rows contained', async () => {
    expect(bodies.length).toBeGreaterThan(10);
    for (const body of bodies) {
      expect(body).not.toContain(secretKey);
      expect(body).not.toContain(story.token);
      expect(HEX64.test(body)).toBe(false);
      expect(NOD_TOKEN.test(body)).toBe(false);
    }
  });
});
