// The Add-node wizard on real SQL (docs/specs/cloud-app.md sections 3.9 and 4.8, issue CLOUD-14): the whole app on a
// migrated database, the platform's own `NodeCredentialService`, a fake `NetworkProbe` and a fake release-checksum
// reader (the only two seams that would leave the machine), and a fake agent that registers through EAB's
// `POST /api/nodes/register` with the credential the wizard minted.
//
// What only real SQL proves: the node row and its credential bound in one go (and a failed mint leaving no pending
// row), the unique index refusing a reused id (a retired one included), `FOR UPDATE` merging two checklist steps, the
// automatic steps following the node row, and the secret tripwire over the whole flow: the node credential, the llama
// key and the tailnet address reach no log line, audit row, stored checklist, error body or later response.
import { randomBytes } from 'node:crypto';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

// A throwaway key for the cipher, generated per run and never committed. The CI database has none configured.
process.env.SECRETS_ENCRYPTION_KEY ??= randomBytes(32).toString('base64');

import { fingerprintOf } from '../../src/localai/contracts/key.schema';
import { NodeKeyService } from '../../src/localai/keys/node-key.service';
import { nextFreeNodeId } from '../../src/localai/wizard/node-id';
import { NETWORK_PROBE, type HttpProbeOptions, type NetworkProbe, type TlsCertInfo } from '../../src/localai/wizard/network-probe.port';
import { RELEASE_CHECKSUMS, type ReleaseChecksums } from '../../src/localai/wizard/release-checksums.port';
import { WizardSettingsService, type WizardSettings } from '../../src/localai/wizard/wizard-settings';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { captureOutput, enableNestLogging, HEX64, testKey } from './support/capture-output';

// Lab ids must look like ai-n<N>; a high range no real lab uses, removed before and after.
const IDS = Array.from({ length: 60 }, (_, i) => `ai-n${9300 + i}`);
const run = `${Date.now()}`;
// A made-up value in the shape of a tailnet address, built from parts so no literal address is in the repository.
const TAILNET_IP = ['100', '71', '8', '9'].join('.');
// A tailnet name, built from parts like the address.
const TAILNET_NAME = ['tail', '1a2b3c'].join('');
const LAN_IP = ['192', '168', '1', '5'].join('.');
const NOW = Date.now();
const daysFromNow = (days: number) => new Date(NOW + days * 86_400_000);

let app: NestFastifyApplication;
let prisma: PrismaService;
let credentials: NodeCredentialService;
let keys: NodeKeyService;
let adminToken: string;
let viewerToken: string;
let contributorToken: string;
let adminId: string;
let nextNumber = 0;

/** A scripted network: tests set what each call answers. */
interface Script {
  dns: Record<string, string[]>;
  tls: TlsCertInfo | Error;
  http: Record<string, { status: number; body?: string } | Error>;
  seenHeaders: Array<Readonly<Record<string, string>> | undefined>;
}
const script: Script = { dns: {}, tls: new Error('no script'), http: {}, seenHeaders: [] };
const fakeProbe: NetworkProbe = {
  async resolve4(host) {
    const answer = script.dns[host];
    if (!answer) throw new Error('ENOTFOUND');
    return answer;
  },
  async tlsCheck() {
    if (script.tls instanceof Error) throw script.tls;
    return script.tls;
  },
  async httpGet(url: string, options: HttpProbeOptions) {
    script.seenHeaders.push(options.headers);
    const answer = script.http[url] ?? new Error('ECONNREFUSED');
    if (answer instanceof Error) throw answer;
    return { status: answer.status, body: answer.body ?? '' };
  },
};

const SHA = 'ab'.repeat(32);
const checksums: ReleaseChecksums & { calls: number; value: string | null } = {
  calls: 0,
  value: SHA,
  async scriptSha256() {
    this.calls += 1;
    return this.value;
  },
};
const settings: WizardSettings = { agentLatestVersion: '0.4.1', vpsPublicHostname: 'ai.marin.cr', githubRepo: 'marinoscar/local-ai' };

const idOf = () => IDS[nextNumber++]!;

async function cleanup(): Promise<void> {
  const rows = await prisma.localaiNode.findMany({ where: { nodeId: { in: IDS } }, select: { nodeCredentialId: true } });
  await prisma.credential.deleteMany({ where: { purpose: 'localai_node_llama_key', name: { in: IDS } } });
  await prisma.auditEvent.deleteMany({ where: { targetId: { in: IDS }, action: { startsWith: 'localai.' } } });
  await prisma.localaiNode.deleteMany({ where: { nodeId: { in: IDS } } });
  await prisma.workerNode.deleteMany({ where: { name: { in: IDS } } });
  await prisma.nodeCredential.deleteMany({ where: { OR: [{ id: { in: rows.flatMap((r) => (r.nodeCredentialId ? [r.nodeCredentialId] : [])) } }, { name: { in: IDS.map((id) => `localai-${id}`) } }] } });
}

async function login(role: 'admin' | 'viewer' | 'contributor'): Promise<{ token: string; id: string }> {
  const email = `wizard-${role}-${run}@example.test`;
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role } });
  expect(response.statusCode).toBeLessThan(400);
  const token = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  expect(token).not.toBe('');
  return { token, id: (await prisma.user.findFirstOrThrow({ where: { email } })).id };
}

const call = (method: 'GET' | 'POST', url: string, token: string, payload?: unknown) =>
  app.inject({ method, url, headers: { authorization: `Bearer ${token}` }, ...(payload === undefined ? {} : { payload: payload as never }) });
const create = (body: Record<string, unknown>, token = adminToken) => call('POST', '/api/localai/nodes', token, body);

interface Created {
  node: { nodeId: string; displayName: string; publicHost: string; servePattern: string; phase: string };
  credential: { token: string; prefix: string };
  install: { oneLiner: string | null; pinnedScriptUrl: string | null; scriptSha256: string | null; note: string | null };
}
async function createNode(extra: Record<string, unknown> = {}): Promise<{ nodeId: string; created: Created; response: Awaited<ReturnType<typeof create>> }> {
  const nodeId = idOf();
  const response = await create({ nodeId, ...extra });
  expect(response.statusCode).toBe(201);
  return { nodeId, created: (response.json() as { data: Created }).data, response };
}

/** The fake agent's first bound call and key report, with the credential the wizard minted. */
async function enrollAndReportKey(nodeId: string, token: string, key?: string): Promise<string> {
  const registered = await app.inject({
    method: 'POST',
    url: '/api/nodes/register',
    headers: { authorization: `Bearer ${token}` },
    payload: {
      name: nodeId,
      hostname: 'host-alpha',
      platform: 'linux/amd64',
      cliVersion: 'aiagent/0.1.0',
      eligibleTypes: ['localai.status.refresh'],
      concurrency: 1,
      capabilities: { localai: { schema: 1, agent_version: '0.1.0' } },
    },
  });
  expect([200, 201]).toContain(registered.statusCode);
  const workerId = (registered.json() as { data: { node: { id: string } } }).data.node.id;
  const config = await app.inject({ method: 'GET', url: `/api/nodes/${workerId}/localai/config`, headers: { authorization: `Bearer ${token}` } });
  expect(config.statusCode).toBe(200);
  if (key !== undefined) {
    const posted = await app.inject({
      method: 'POST',
      url: `/api/nodes/${workerId}/localai/key`,
      headers: { authorization: `Bearer ${token}` },
      payload: { key, fingerprint: fingerprintOf(key), rotated_at: '2026-11-02T15:04:05Z' },
    });
    expect(posted.statusCode).toBe(200);
  }
  return workerId;
}

type ChecklistBody = {
  data: {
    nodeId: string;
    complete: boolean;
    steps: Array<{ step: string; state: string; kind: string; detail: string | null; checkedAt: string | null; snippets: Array<{ content: string; path?: string }>; missing: string[] }>;
  };
};
const stepOf = (body: ChecklistBody, step: string) => body.data.steps.find((s) => s.step === step)!;
const checklist = async (nodeId: string) => (await call('GET', `/api/localai/nodes/${nodeId}/checklist`, adminToken)).json() as ChecklistBody;
const act = (nodeId: string, step: string, action: string) => call('POST', `/api/localai/nodes/${nodeId}/checklist/${step}`, adminToken, { action });

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  credentials = app.get(NodeCredentialService);
  keys = app.get(NodeKeyService);
  // The two seams that would leave the machine: replace what the module bound.
  Object.assign(app.get<NetworkProbe>(NETWORK_PROBE, { strict: false }), fakeProbe);
  Object.assign(app.get<ReleaseChecksums>(RELEASE_CHECKSUMS, { strict: false }), { scriptSha256: checksums.scriptSha256.bind(checksums) });
  jest.spyOn(app.get(WizardSettingsService), 'read').mockImplementation(async () => ({ ...settings }));
  const admin = await login('admin');
  adminToken = admin.token;
  adminId = admin.id;
  viewerToken = (await login('viewer')).token;
  contributorToken = (await login('contributor')).token;
  await cleanup();
});

afterAll(async () => {
  jest.restoreAllMocks();
  if (prisma) await cleanup();
  await app?.close();
});

beforeEach(() => {
  script.dns = {};
  script.tls = new Error('no script');
  script.http = {};
  script.seenHeaders = [];
  checksums.value = SHA;
  checksums.calls = 0;
  Object.assign(settings, { agentLatestVersion: '0.4.1', vpsPublicHostname: 'ai.marin.cr' });
});

describe('POST /api/localai/nodes', () => {
  it('creates a pending node with its credential bound, returns the token once, and never shows it again', async () => {
    const { nodeId, created, response } = await createNode({ displayName: 'Node Four', tailnetMachine: 'host-alpha', tailnetIp: TAILNET_IP, profile: 'cuda-12gb' });
    const token = created.credential.token;

    expect(created.node).toEqual({ nodeId, displayName: 'Node Four', publicHost: `${nodeId}.marin.cr`, servePattern: 'B', phase: 'pending' });
    expect(token).toMatch(/^nod_[0-9a-f]{64}$/);
    expect(created.credential.prefix).toBe(token.slice(0, 8));
    expect(created.install).toMatchObject({
      pinnedScriptUrl: 'https://github.com/marinoscar/local-ai/releases/download/node-v0.4.1/install-node.sh',
      scriptSha256: SHA,
      note: null,
    });
    expect(created.install.oneLiner).toContain(`--node-id ${nodeId} --cloud-url https://ai.marin.cr --enroll`);
    expect(created.install.oneLiner).not.toContain(token);

    // No browser or proxy may keep it.
    expect(String(response.headers['cache-control'])).toContain('no-store');

    // The row: pending, the credential bound, the tailnet address stored (admin-only data).
    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    expect(row).toMatchObject({ phase: 'pending', servePattern: 'B', tailnetMachine: 'host-alpha', tailnetIp: TAILNET_IP, profile: 'cuda-12gb', createdById: adminId, workerNodeId: null });
    const stored = await prisma.nodeCredential.findUniqueOrThrow({ where: { id: row.nodeCredentialId! } });
    expect(stored).toMatchObject({ name: `localai-${nodeId}`, tokenPrefix: created.credential.prefix, userId: adminId, expiresAt: null, revokedAt: null });
    // At rest: a hash. The token is nowhere in the row.
    expect(JSON.stringify(stored)).not.toContain(token);

    // The token is what the platform validates: it is the live credential of this node.
    expect(await credentials.validateToken(token)).toMatchObject({ id: adminId });

    // Never again: the node detail, the list, the checklist, the platform's own credential list, and the audit rows.
    for (const url of [`/api/localai/nodes/${nodeId}`, `/api/localai/nodes/${nodeId}?reveal=tailnet`, '/api/localai/nodes', `/api/localai/nodes/${nodeId}/checklist`, '/api/node-credentials']) {
      const later = await call('GET', url, adminToken);
      expect(later.statusCode).toBe(200);
      expect(later.body).not.toContain(token);
    }
    const audits = await prisma.auditEvent.findMany({ where: { targetId: nodeId } });
    expect(audits.map((a) => a.action).sort()).toEqual(['localai.node.created', 'localai.node.credential_minted']);
    expect(JSON.stringify(audits)).not.toContain(token);
    expect(JSON.stringify(audits)).not.toContain(TAILNET_IP);
    expect(audits.find((a) => a.action === 'localai.node.credential_minted')).toMatchObject({ actorUserId: adminId, targetType: 'localai_node', meta: { credentialId: stored.id, credentialPrefix: created.credential.prefix } });
  });

  it('defaults: Pattern B, publicHost <nodeId>.marin.cr, the node id as its name; honours an override', async () => {
    const plain = await createNode();
    expect(plain.created.node).toMatchObject({ displayName: plain.nodeId, publicHost: `${plain.nodeId}.marin.cr`, servePattern: 'B' });
    const custom = await createNode({ publicHost: 'node-x.example.test', servePattern: 'A' });
    expect(custom.created.node).toMatchObject({ publicHost: 'node-x.example.test', servePattern: 'A' });
  });

  it('shows no install commands while no agent release is configured, and an unknown checksum is said to be unknown', async () => {
    settings.agentLatestVersion = null;
    const none = await createNode();
    expect(none.created.install).toMatchObject({ oneLiner: null, pinnedScriptUrl: null, scriptSha256: null });
    expect(checksums.calls).toBe(0);

    settings.agentLatestVersion = '0.4.1';
    checksums.value = null;
    const unknown = await createNode();
    expect(unknown.created.install).toMatchObject({ scriptSha256: null, note: 'Checksum unknown, check the release page.' });
    expect(unknown.created.install.oneLiner).toContain('node-v0.4.1/install-node.sh');
  });

  it('reusing an id answers 409 node_id_used, a RETIRED id included, and changes nothing', async () => {
    const { nodeId, created } = await createNode();
    const again = await create({ nodeId });
    expect(again.statusCode).toBe(409);
    expect(again.json()).toMatchObject({ details: { reason: 'node_id_used' } });
    expect(again.body).not.toContain(created.credential.token);

    await prisma.localaiNode.update({ where: { nodeId }, data: { phase: 'retired', retiredAt: new Date() } });
    const retired = await create({ nodeId });
    expect(retired.statusCode).toBe(409);
    expect(retired.json()).toMatchObject({ details: { reason: 'node_id_used' } });
    expect(await prisma.localaiNode.count({ where: { nodeId } })).toBe(1);
    // Only the first create minted a credential.
    expect(await prisma.nodeCredential.count({ where: { name: `localai-${nodeId}` } })).toBe(1);
  });

  it('two creates of one id at once: one 201, one 409, and the loser leaves no live credential', async () => {
    const nodeId = idOf();
    const [a, b] = await Promise.all([create({ nodeId }), create({ nodeId })]);
    expect([a.statusCode, b.statusCode].sort()).toEqual([201, 409]);
    const rows = await prisma.nodeCredential.findMany({ where: { name: `localai-${nodeId}` } });
    expect(rows.filter((row) => row.revokedAt === null)).toHaveLength(1);
    const node = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    expect(rows.find((row) => row.revokedAt === null)!.id).toBe(node.nodeCredentialId);
  });

  it('a failed mint leaves no pending row; a failure after the mint revokes the credential and leaves no row', async () => {
    const mintFails = jest.spyOn(credentials, 'createCredential').mockRejectedValueOnce(new Error('mint failed'));
    const first = idOf();
    expect((await create({ nodeId: first })).statusCode).toBe(500);
    expect(await prisma.localaiNode.count({ where: { nodeId: first } })).toBe(0);
    mintFails.mockRestore();

    // The mint succeeds, then binding it to the node row fails: the row rolls back and the minted credential is revoked.
    const realTransaction = prisma.$transaction.bind(prisma) as (fn: (tx: any) => Promise<unknown>) => Promise<unknown>; // eslint-disable-line @typescript-eslint/no-explicit-any
    const minted: string[] = [];
    const real = credentials.createCredential.bind(credentials);
    const mint = jest.spyOn(credentials, 'createCredential').mockImplementationOnce(async (userId, dto) => {
      const result = await real(userId, dto);
      minted.push(result.id);
      return result;
    });
    const tx = jest.spyOn(prisma, '$transaction').mockImplementationOnce(((fn: (tx: any) => Promise<unknown>) => // eslint-disable-line @typescript-eslint/no-explicit-any
      realTransaction((client) =>
        fn(new Proxy(client, { get: (target, property) => (property === 'localaiNode' ? { create: target.localaiNode.create.bind(target.localaiNode), update: async () => { throw new Error('bind failed'); } } : target[property]) })),
      )) as never);
    const second = idOf();
    expect((await create({ nodeId: second })).statusCode).toBe(500);
    mint.mockRestore();
    tx.mockRestore();
    expect(await prisma.localaiNode.count({ where: { nodeId: second } })).toBe(0);
    expect(minted).toHaveLength(1);
    expect((await prisma.nodeCredential.findUniqueOrThrow({ where: { id: minted[0]! } })).revokedAt).toBeInstanceOf(Date);
  });

  it('refuses a body that is not valid with 400 and creates nothing', async () => {
    const before = await prisma.localaiNode.count({ where: { nodeId: { in: IDS } } });
    for (const body of [{ nodeId: 'ai-n04' }, { nodeId: 'localai' }, { nodeId: IDS[0], tailnetIp: LAN_IP }, { nodeId: IDS[0], publicHost: '127.0.0.1' }, {}]) {
      const response = await create(body as never);
      expect(response.statusCode).toBe(400);
    }
    expect(await prisma.localaiNode.count({ where: { nodeId: { in: IDS } } })).toBe(before);
  });

  it('a refused tailnet address is not echoed in the error body', async () => {
    const bad = ['100', '5', '6', '7'].join('.');
    const response = await create({ nodeId: idOf(), tailnetIp: bad });
    expect(response.statusCode).toBe(400);
    expect(response.body).not.toContain(bad);
  });

  it('is lab:admin only: 401 without a session, 403 for a viewer and a contributor, nothing created', async () => {
    const nodeId = idOf();
    expect((await app.inject({ method: 'POST', url: '/api/localai/nodes', payload: { nodeId } })).statusCode).toBe(401);
    expect((await create({ nodeId }, viewerToken)).statusCode).toBe(403);
    expect((await create({ nodeId }, contributorToken)).statusCode).toBe(403);
    expect(await prisma.localaiNode.count({ where: { nodeId } })).toBe(0);
  });
});

describe('GET /api/localai/nodes/next-id', () => {
  it('is ai-n<max+1> over every row including retired ones, and lab:admin only', async () => {
    const { nodeId } = await createNode();
    await prisma.localaiNode.update({ where: { nodeId }, data: { phase: 'retired', retiredAt: new Date() } });
    const all = await prisma.localaiNode.findMany({ select: { nodeId: true } });
    const expected = nextFreeNodeId(all.map((row) => row.nodeId));
    const response = await call('GET', '/api/localai/nodes/next-id', adminToken);
    expect(response.statusCode).toBe(200);
    expect((response.json() as { data: { nodeId: string | null } }).data.nodeId).toBe(expected);
    // The retired node counts: the suggestion is above its number.
    expect(Number(expected!.slice(4))).toBeGreaterThan(Number(nodeId.slice(4)));
    expect((await call('GET', '/api/localai/nodes/next-id', viewerToken)).statusCode).toBe(403);
  });
});

describe('the publish checklist', () => {
  it('lists the eight steps in order; the real tailnet address is in the lab:admin response only', async () => {
    const { nodeId } = await createNode({ servePattern: 'B', tailnetMachine: `host-alpha.${TAILNET_NAME}.ts.net`, tailnetIp: TAILNET_IP });
    const response = await call('GET', `/api/localai/nodes/${nodeId}/checklist`, adminToken);
    expect(response.statusCode).toBe(200);
    expect(String(response.headers['cache-control'])).toContain('no-store');
    const body = response.json() as ChecklistBody;
    expect(body.data.steps.map((s) => s.step)).toEqual(['tailnet', 'dns', 'bootstrap_vhost', 'certificate', 'https_vhost', 'enrolled', 'key', 'gateway_test']);
    expect(body.data.steps.map((s) => s.kind)).toEqual(['manual', 'check', 'manual', 'check', 'check', 'auto', 'auto', 'check']);
    expect(body.data.complete).toBe(false);

    const vhost = stepOf(body, 'https_vhost').snippets.find((s) => s.path)!;
    expect(vhost.path).toBe(`/opt/infra/proxy/nginx/conf.d/${nodeId}.marin.cr.conf`);
    expect(vhost.content).toContain(`proxy_pass https://${TAILNET_IP}:443;`);
    expect(vhost.content).toContain(`proxy_ssl_name host-alpha.${TAILNET_NAME}.ts.net;`);
    expect(stepOf(body, 'https_vhost').missing).toEqual([]);

    // Everyone else is refused, so the address reaches nobody else.
    for (const token of [viewerToken, contributorToken]) {
      const refused = await call('GET', `/api/localai/nodes/${nodeId}/checklist`, token);
      expect(refused.statusCode).toBe(403);
      expect(refused.body).not.toContain(TAILNET_IP);
    }
    // And the card routes a viewer can read never carry it.
    const card = await call('GET', `/api/localai/nodes/${nodeId}?reveal=tailnet`, viewerToken);
    expect(card.body).not.toContain(TAILNET_IP);
  });

  it('without a stored tailnet address the snippets keep the runbook placeholder and say what is missing', async () => {
    const { nodeId } = await createNode({ servePattern: 'A' });
    const body = await checklist(nodeId);
    expect(stepOf(body, 'https_vhost').snippets[0]!.content).toContain(`<TAILNET_IP_AI_N${nodeId.slice(4)}>`);
    expect(stepOf(body, 'https_vhost').missing).toEqual(['tailnetIp']);
  });

  it('marks manual steps done and back to do; the stored entry has a state, a time and plain text', async () => {
    const { nodeId } = await createNode();
    const done = await act(nodeId, 'tailnet', 'mark_done');
    expect(done.statusCode).toBe(200);
    expect(stepOf(done.json() as ChecklistBody, 'tailnet')).toMatchObject({ state: 'done', detail: 'Marked done by an administrator.' });
    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    expect(row.publishChecklist).toMatchObject({ tailnet: { state: 'done', detail: 'Marked done by an administrator.' } });
    expect(Date.parse((row.publishChecklist as { tailnet: { checkedAt: string } }).tailnet.checkedAt)).not.toBeNaN();

    expect(stepOf((await act(nodeId, 'tailnet', 'mark_todo')).json() as ChecklistBody, 'tailnet').state).toBe('todo');
    const audits = await prisma.auditEvent.findMany({ where: { targetId: nodeId, action: 'localai.checklist.updated' }, orderBy: { createdAt: 'asc' } });
    expect(audits.map((a) => a.meta)).toEqual([
      { step: 'tailnet', action: 'mark_done', state: 'done' },
      { step: 'tailnet', action: 'mark_todo', state: 'todo' },
    ]);
    expect(audits[0]).toMatchObject({ actorUserId: adminId, targetType: 'localai_node' });
  });

  it('two steps saved at once both survive (the row is locked while they merge)', async () => {
    const { nodeId } = await createNode();
    const [a, b] = await Promise.all([act(nodeId, 'tailnet', 'mark_done'), act(nodeId, 'bootstrap_vhost', 'mark_done')]);
    expect([a.statusCode, b.statusCode]).toEqual([200, 200]);
    const body = await checklist(nodeId);
    expect(stepOf(body, 'tailnet').state).toBe('done');
    expect(stepOf(body, 'bootstrap_vhost').state).toBe('done');
  });

  it('refuses the wrong action for a step with 400, an unknown step or node with 404, a retired node with 409', async () => {
    const { nodeId } = await createNode();
    expect(await act(nodeId, 'dns', 'mark_done')).toMatchObject({ statusCode: 400 });
    expect((await act(nodeId, 'tailnet', 'check')).json()).toMatchObject({ details: { reason: 'not_a_check_step' } });
    expect((await act(nodeId, 'enrolled', 'mark_done')).json()).toMatchObject({ details: { reason: 'automatic_step' } });
    expect((await act(nodeId, 'key', 'check')).statusCode).toBe(400);
    expect((await act(nodeId, 'publish', 'check')).statusCode).toBe(404);
    expect((await call('POST', `/api/localai/nodes/${nodeId}/checklist/dns`, adminToken, { action: 'explode' })).statusCode).toBe(400);
    expect((await call('GET', '/api/localai/nodes/ai-n9998/checklist', adminToken)).statusCode).toBe(404);
    expect((await call('GET', '/api/localai/nodes/not-a-node/checklist', adminToken)).statusCode).toBe(404);
    for (const token of [viewerToken, contributorToken]) {
      expect((await call('POST', `/api/localai/nodes/${nodeId}/checklist/tailnet`, token, { action: 'mark_done' })).statusCode).toBe(403);
    }
    await prisma.localaiNode.update({ where: { nodeId }, data: { phase: 'retired', retiredAt: new Date() } });
    const retired = await call('GET', `/api/localai/nodes/${nodeId}/checklist`, adminToken);
    expect(retired.statusCode).toBe(409);
    expect(retired.json()).toMatchObject({ details: { code: 'LAI-CLD-035', reason: 'node_retired' } });
    expect((await act(nodeId, 'tailnet', 'mark_done')).statusCode).toBe(409);
  });

  describe('automatic checks against the fake network', () => {
    const vps = ['198', '51', '100', '1'].join('.');
    const other = ['198', '51', '100', '2'].join('.');
    const cert = (patch: Partial<TlsCertInfo> = {}): TlsCertInfo => ({ authorized: true, issuerOrganization: "Let's Encrypt", issuerCommonName: 'R11', validTo: daysFromNow(60), ...patch });

    it('dns: green for the VPS address; red, with a plain detail and without the addresses, for a wrong record', async () => {
      const { nodeId } = await createNode();
      const host = `${nodeId}.marin.cr`;
      script.dns = { 'ai.marin.cr': [vps], [host]: [vps] };
      const good = await act(nodeId, 'dns', 'check');
      expect(stepOf(good.json() as ChecklistBody, 'dns')).toMatchObject({ state: 'done', detail: `${host} points to the same address as ai.marin.cr.` });

      script.dns = { 'ai.marin.cr': [vps], [host]: [other] };
      const bad = stepOf((await act(nodeId, 'dns', 'check')).json() as ChecklistBody, 'dns');
      expect(bad.state).toBe('failed');
      expect(bad.detail).toContain('does not point to the same address');
      expect(JSON.stringify(bad)).not.toContain(other);
      expect(JSON.stringify(bad)).not.toContain(vps);
      expect((await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } })).publishChecklist).toMatchObject({ dns: { state: 'failed' } });

      script.dns = { 'ai.marin.cr': [vps] };
      expect(stepOf((await act(nodeId, 'dns', 'check')).json() as ChecklistBody, 'dns').detail).toMatch(/No A record/);
    });

    it('certificate: green with 60 days; red under 7 days, for a foreign issuer, and when unreachable', async () => {
      const { nodeId } = await createNode();
      script.tls = cert();
      expect(stepOf((await act(nodeId, 'certificate', 'check')).json() as ChecklistBody, 'certificate')).toMatchObject({ state: 'done' });
      script.tls = cert({ validTo: daysFromNow(3.4) });
      const soon = stepOf((await act(nodeId, 'certificate', 'check')).json() as ChecklistBody, 'certificate');
      expect(soon.state).toBe('failed');
      expect(soon.detail).toMatch(/expires in 3 days/);
      script.tls = cert({ issuerOrganization: 'Org Alpha CA' });
      expect(stepOf((await act(nodeId, 'certificate', 'check')).json() as ChecklistBody, 'certificate').detail).toMatch(/not issued by Let's Encrypt/);
      script.tls = new Error('boom');
      expect(stepOf((await act(nodeId, 'certificate', 'check')).json() as ChecklistBody, 'certificate').state).toBe('failed');
    });

    it('https_vhost: green for /health 200 and /v1/models 401; red when /v1/models answers 200 without a key', async () => {
      const { nodeId } = await createNode();
      const base = `https://${nodeId}.marin.cr`;
      script.http = { [`${base}/health`]: { status: 200 }, [`${base}/v1/models`]: { status: 401 } };
      expect(stepOf((await act(nodeId, 'https_vhost', 'check')).json() as ChecklistBody, 'https_vhost')).toMatchObject({ state: 'done' });
      expect(script.seenHeaders.every((headers) => headers === undefined)).toBe(true);

      script.http = { [`${base}/health`]: { status: 200 }, [`${base}/v1/models`]: { status: 200, body: '{"data":[]}' } };
      const open = stepOf((await act(nodeId, 'https_vhost', 'check')).json() as ChecklistBody, 'https_vhost');
      expect(open.state).toBe('failed');
      expect(open.detail).toMatch(/^DANGER/);
    });

    it('a stored check result survives a page reload (GET) and is cleared by mark_todo', async () => {
      const { nodeId } = await createNode();
      script.dns = {};
      await act(nodeId, 'dns', 'check');
      expect(stepOf(await checklist(nodeId), 'dns').state).toBe('failed');
      expect(stepOf((await act(nodeId, 'dns', 'mark_todo')).json() as ChecklistBody, 'dns')).toMatchObject({ state: 'todo', detail: null });
    });

    it('refuses to probe a public host that is not a plain DNS name', async () => {
      const { nodeId } = await createNode();
      await prisma.localaiNode.update({ where: { nodeId }, data: { publicHost: '127.0.0.1' } });
      const result = stepOf((await act(nodeId, 'certificate', 'check')).json() as ChecklistBody, 'certificate');
      expect(result.state).toBe('failed');
      expect(result.detail).toMatch(/not a valid DNS name/);
    });
  });

  it('Enrolled and Key received turn green by themselves after the fake agent’s first bound call and POST key; all eight done is complete', async () => {
    const { nodeId, created } = await createNode({ tailnetMachine: `host-alpha.${TAILNET_NAME}.ts.net`, tailnetIp: TAILNET_IP });
    const before = await checklist(nodeId);
    expect(stepOf(before, 'enrolled').state).toBe('todo');
    expect(stepOf(before, 'key').state).toBe('todo');
    // The wizard's poll: node detail reports the phase and whether the key arrived.
    expect((await call('GET', `/api/localai/nodes/${nodeId}`, adminToken)).json()).toMatchObject({ data: { phase: 'pending', keyReceived: false } });

    const key = testKey();
    await enrollAndReportKey(nodeId, created.credential.token, key);

    const after = await checklist(nodeId);
    expect(stepOf(after, 'enrolled')).toMatchObject({ state: 'done', kind: 'auto' });
    expect(stepOf(after, 'key')).toMatchObject({ state: 'done', kind: 'auto' });
    expect((await call('GET', `/api/localai/nodes/${nodeId}`, adminToken)).json()).toMatchObject({ data: { phase: 'enrolled', keyReceived: true } });
    expect(after.data.complete).toBe(false);

    // The gateway test reads the key server-side and sends it in one header; the stored detail has no key in it.
    const base = `https://${nodeId}.marin.cr`;
    const node = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    await prisma.localaiNodeModel.create({ data: { nodeId: node.id, modelId: 'model-one', present: true, verified: true, loadable: true, status: 'loaded', isDefault: true } });
    script.http = { [`${base}/v1/models`]: { status: 200, body: JSON.stringify({ data: [{ id: 'model-one' }, { id: 'model-two' }] }) } };
    const gateway = stepOf((await act(nodeId, 'gateway_test', 'check')).json() as ChecklistBody, 'gateway_test');
    expect(gateway).toMatchObject({ state: 'done' });
    expect(script.seenHeaders).toEqual([{ Authorization: `Bearer ${key}` }]);
    expect(JSON.stringify((await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } })).publishChecklist)).not.toContain(key);

    // A loaded model the node does not list turns it red.
    script.http = { [`${base}/v1/models`]: { status: 200, body: JSON.stringify({ data: [{ id: 'model-two' }] }) } };
    const missing = stepOf((await act(nodeId, 'gateway_test', 'check')).json() as ChecklistBody, 'gateway_test');
    expect(missing.state).toBe('failed');
    expect(missing.detail).toContain('model-one');

    // Finish the rest: all eight done.
    script.http = { [`${base}/v1/models`]: { status: 200, body: JSON.stringify({ data: [{ id: 'model-one' }] }) } };
    await act(nodeId, 'gateway_test', 'check');
    script.dns = { 'ai.marin.cr': [vps0()], [`${nodeId}.marin.cr`]: [vps0()] };
    await act(nodeId, 'dns', 'check');
    script.tls = { authorized: true, issuerOrganization: "Let's Encrypt", issuerCommonName: 'R11', validTo: daysFromNow(80) };
    await act(nodeId, 'certificate', 'check');
    script.http = { [`${base}/health`]: { status: 200 }, [`${base}/v1/models`]: { status: 401 } };
    await act(nodeId, 'https_vhost', 'check');
    await act(nodeId, 'tailnet', 'mark_done');
    const last = await act(nodeId, 'bootstrap_vhost', 'mark_done');
    expect((last.json() as ChecklistBody).data.complete).toBe(true);
    // Finishing the checklist does not flip the gateway switch (spec 4.8).
    expect((await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } })).gatewayEnabled).toBe(true);
  });

  it('the gateway test fails plainly, and never calls the node, while the cloud holds no key', async () => {
    const { nodeId } = await createNode();
    const result = stepOf((await act(nodeId, 'gateway_test', 'check')).json() as ChecklistBody, 'gateway_test');
    expect(result.state).toBe('failed');
    expect(result.detail).toMatch(/no API key for this node/);
    expect(script.seenHeaders).toEqual([]);
  });
});

describe('secret tripwire over the whole flow', () => {
  it('the node credential, the llama key and the tailnet address reach no log line, audit row, stored checklist, error body or later response', async () => {
    const restoreLogger = enableNestLogging();
    const capture = captureOutput();
    try {
      const { nodeId, created, response } = await createNode({ tailnetMachine: `host-alpha.${TAILNET_NAME}.ts.net`, tailnetIp: TAILNET_IP });
      const token = created.credential.token;
      const key = testKey();
      const base = `https://${nodeId}.marin.cr`;

      // Every response of the flow except the one create response, which carries the token by design. A successful
      // admin checklist response carries the node's tailnet address by design (its snippets), so it is kept apart.
      const bodies: string[] = [];
      const checklists: string[] = [];
      const collect = async (promise: ReturnType<typeof call>, into = bodies) => {
        const r = await promise;
        into.push(r.body);
        return r;
      };
      await collect(create({ nodeId })); // refused: id used
      await collect(create({ nodeId: 'ai-n04', tailnetIp: TAILNET_IP })); // refused: invalid
      await collect(call('GET', `/api/localai/nodes/${nodeId}`, adminToken));
      await collect(call('GET', `/api/localai/nodes/${nodeId}?reveal=tailnet`, viewerToken));
      await collect(call('GET', '/api/localai/nodes', viewerToken));
      await collect(call('GET', `/api/localai/nodes/${nodeId}/checklist`, viewerToken)); // 403
      await enrollAndReportKey(nodeId, token, key);
      await collect(call('GET', `/api/localai/nodes/${nodeId}/checklist`, adminToken), checklists);
      await collect(act(nodeId, 'dns', 'mark_done')); // refused
      await collect(act(nodeId, 'enrolled', 'check')); // refused
      script.dns = { 'ai.marin.cr': ['198.51.100.1'] }; // the host does not resolve: a failed check
      await collect(act(nodeId, 'dns', 'check'), checklists);
      script.tls = new Error(`connect failed ${token} ${key}`);
      await collect(act(nodeId, 'certificate', 'check'), checklists);
      script.http = { [`${base}/v1/models`]: new Error(`Authorization: Bearer ${key} ${token} ${TAILNET_IP}`) };
      await collect(act(nodeId, 'gateway_test', 'check'), checklists);
      script.http = { [`${base}/v1/models`]: { status: 401 } };
      await collect(act(nodeId, 'gateway_test', 'check'), checklists);

      // Everything an operator, a log pipeline or a later reader can see.
      const surfaces = [
        capture.text(),
        ...bodies,
        JSON.stringify(await prisma.auditEvent.findMany({ where: { targetId: nodeId } })),
        JSON.stringify((await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } })).publishChecklist),
      ].join('\n');
      // And what is stored: a hash for the credential (itself 64 hex, so not scanned for hex), ciphertext for the key.
      const stores = [
        JSON.stringify(await prisma.nodeCredential.findMany({ where: { name: `localai-${nodeId}` } })),
        JSON.stringify(await prisma.credential.findMany({ where: { purpose: 'localai_node_llama_key', name: nodeId } })),
      ].join('\n');

      for (const text of [surfaces, stores, ...checklists]) {
        expect(text).not.toContain(token);
        expect(text).not.toContain(token.slice('nod_'.length));
        expect(text).not.toContain(key);
      }
      expect(surfaces).not.toContain(TAILNET_IP);
      expect(surfaces).not.toMatch(/nod_[0-9a-f]{20,}/);
      expect(surfaces).not.toMatch(HEX64);
      // The admin's checklist is the one place the address appears.
      expect(checklists[0]).toContain(TAILNET_IP);
      for (const text of checklists) expect(text).not.toMatch(HEX64);
      // The create response itself: the token is in it once, and the key, the address and the cache headers are right.
      expect(response.body.split(token)).toHaveLength(2);
      expect(response.body).not.toContain(TAILNET_IP);
      // The cipher agrees with the key that was sent (so the above is not vacuous).
      expect(await keys.getKey(nodeId)).toBe(key);
      expect(capture.text().length).toBeGreaterThan(0);
    } finally {
      capture.restore();
      restoreLogger();
    }
  });
});

/** The VPS address used by the completion test. */
function vps0(): string {
  return ['198', '51', '100', '1'].join('.');
}
