// The command API for the UI against real PostgreSQL (CLOUD-09, docs/specs/cloud-app.md sections 3.9, 4.4 and 9):
// the whole app, real sessions and personal access tokens for the three roles, a real `nod_` credential for the fake
// agent. POST -> the agent claims it through the node route -> posts a result -> GET shows `succeeded`, with exactly
// one `requested` and one `settled` audit row; a failure and an expiry each notify the requester once; cancel is a
// conditional write that loses cleanly to a claim; the refusals read the node row and its model rows.
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import { LOCALAI_COMMAND_NOTIFIER, type LocalaiCommandNotifier } from '../../src/localai/commands/command-notifier';
import { COMMAND_TYPES } from '../../src/localai/contracts/commands';
import { CommandsSweepHandler } from '../../src/localai/commands/sweeper.job';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { readCommandExamples } from '../fixtures/localai/command-examples';

const run = `${Date.now()}`;
const MODEL = 'qwen3.6-35b-a3b-q4km';
/** The model id the shared params fixture uses for the unload, set_default and delete examples. */
const FIXTURE_MODEL = 'gemma-4-26b-q4km';
const ALL = COMMAND_TYPES.map((type) => type.replace('localai.', ''));
const loadResult = { model_id: MODEL, status: 'loaded', load_ms: 1000, unloaded: [], vram_used_mib: null, is_default: false };
const examples = readCommandExamples<Record<string, { params: Record<string, unknown> }>>();
/** `model.delete` params for MODEL: the fixture's roots, the typed confirmation equal to the model id. */
const deleteParams = { ...examples['localai.model.delete'].params, model_id: MODEL, confirm: MODEL };
const ADMIN_ONLY = ['model.delete', 'mode.offline', 'key.rotate', 'agent.update'];

let app: NestFastifyApplication;
let prisma: PrismaService;
let credentials: NodeCredentialService;
let notifier: LocalaiCommandNotifier;
let sweeper: CommandsSweepHandler;
let commandFailed: jest.SpyInstance;
const tokens = {} as Record<'admin' | 'contributor' | 'viewer', string>;
const userIds = {} as Record<'admin' | 'contributor' | 'viewer', string>;
let patToken = '';
const nodeRowIds: string[] = [];
const credentialIds: string[] = [];
let counter = 0;

async function login(role: 'admin' | 'contributor' | 'viewer'): Promise<void> {
  const email = `commands-api-${role}-${run}@example.test`;
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role } });
  tokens[role] = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  userIds[role] = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
}

const api = (method: 'GET' | 'POST' | 'DELETE', url: string, bearer: string | null, payload?: unknown, headers: Record<string, string> = {}) =>
  app.inject({ method, url: `/api/localai${url}`, headers: { ...(bearer ? { authorization: `Bearer ${bearer}` } : {}), ...headers }, payload: payload as never });

/** An enrolled node with a real credential, an `online` mode and MODEL downloaded: what the fake agent talks as. */
async function enrolled(options: { phase?: 'pending' | 'enrolled' | 'retired'; mode?: string | null; withModel?: boolean } = {}) {
  counter += 1;
  const nodeId = `ai-n${counter}-${run}`;
  const minted = await credentials.createCredential(userIds.admin, { name: `${nodeId} credential` } as never);
  credentialIds.push(minted.id);
  const node = await prisma.localaiNode.create({
    data: {
      nodeId,
      displayName: `Node ${counter}`,
      publicHost: `node-${counter}.example.test`,
      servePattern: 'none',
      phase: options.phase ?? 'enrolled',
      mode: options.mode === undefined ? 'online' : options.mode,
      nodeCredentialId: minted.id,
      createdById: userIds.admin,
    },
  });
  nodeRowIds.push(node.id);
  if (options.withModel !== false) {
    for (const modelId of [MODEL, FIXTURE_MODEL]) {
      await prisma.localaiNodeModel.create({
        data: { nodeId: node.id, modelId, present: true, verified: true, loadable: true, status: 'unloaded', slots: 1, roles: [], inputModalities: ['text'] },
      });
    }
  }
  const registered = await app.inject({
    method: 'POST',
    url: '/api/nodes/register',
    headers: { authorization: `Bearer ${minted.token}` },
    payload: { name: nodeId, hostname: 'host-alpha', platform: 'linux/amd64', cliVersion: 'aiagent/0.1.0', eligibleTypes: ['localai.status.refresh'], concurrency: 1, capabilities: { localai: { schema: 1, agent_version: '0.1.0' } } },
  });
  const worker = (registered.json() as { data: { node: { id: string } } }).data.node.id;
  const agent = (path: string, payload: unknown) =>
    app.inject({ method: 'POST', url: `/api/nodes/${worker}/localai/commands/${path}`, headers: { authorization: `Bearer ${minted.token}` }, payload: payload as never });
  return { rowId: node.id, nodeId, agent };
}

const auditRows = (action: string, targetId: string) => prisma.auditEvent.findMany({ where: { action, targetType: 'localai_command', targetId } });
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
  notifier = app.get<LocalaiCommandNotifier>(LOCALAI_COMMAND_NOTIFIER);
  sweeper = app.get(CommandsSweepHandler);
  commandFailed = jest.spyOn(notifier, 'commandFailed');
  await login('admin');
  await login('contributor');
  await login('viewer');
  const pat = await app.inject({
    method: 'POST',
    url: '/api/pat',
    headers: { authorization: `Bearer ${tokens.admin}` },
    payload: { name: `commands-api-${run}`, durationValue: 1, durationUnit: 'days' },
  });
  patToken = (pat.json() as { data: { token: string } }).data.token;
});

afterAll(async () => {
  if (prisma) {
    const ids = (await prisma.localaiCommand.findMany({ where: { nodeId: { in: nodeRowIds } }, select: { id: true } })).map((command) => command.id);
    await prisma.auditEvent.deleteMany({ where: { targetType: 'localai_command', targetId: { in: ids } } });
    await prisma.localaiCommand.deleteMany({ where: { nodeId: { in: nodeRowIds } } });
    await prisma.localaiNode.deleteMany({ where: { id: { in: nodeRowIds } } });
    await prisma.workerNode.deleteMany({ where: { createdById: userIds.admin } });
    await prisma.nodeCredential.deleteMany({ where: { id: { in: credentialIds } } });
  }
  await app?.close();
});

beforeEach(() => commandFailed.mockClear());

describe('send, claim, result, read (the whole path)', () => {
  it('POST 201 -> the fake agent claims it -> result -> GET shows succeeded; one requested and one settled audit row; no notification', async () => {
    const n = await enrolled();
    const created = await api('POST', `/nodes/${n.nodeId}/commands`, tokens.contributor, { type: 'model.load', params: { model_id: MODEL, unload_others: true } });
    expect(created.statusCode).toBe(201);
    const command = created.json().data;
    expect(command).toMatchObject({ nodeId: n.nodeId, type: 'localai.model.load', status: 'queued', source: 'ui', requestedById: userIds.contributor, params: { model_id: MODEL, wait: true, unload_others: true, set_default: false } });

    const duplicate = await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'model.load', params: { model_id: MODEL } });
    expect(duplicate.statusCode).toBe(200);
    expect(duplicate.json().data.id).toBe(command.id);

    const claimed = (await n.agent('claim', { types: ALL, limit: 1, waitMs: 0 })).json().data.jobs[0];
    expect(claimed.job.id).toBe(command.id);
    expect((await api('GET', `/commands/${command.id}`, tokens.viewer)).json().data.status).toBe('running');
    expect((await api('DELETE', `/commands/${command.id}`, tokens.admin)).statusCode).toBe(409);

    const done = await n.agent(`${command.id}/result`, { type: 'localai.model.load', claimToken: claimed.claimToken, result: loadResult });
    expect(done.statusCode).toBe(200);
    const read = (await api('GET', `/commands/${command.id}`, tokens.viewer)).json().data;
    expect(read).toMatchObject({ id: command.id, nodeId: n.nodeId, status: 'succeeded', result: loadResult, error: null });
    expect(read).not.toHaveProperty('claimToken');

    const settled = await eventually(() => auditRows('localai.command.settled', command.id), (rows) => rows.length > 0);
    expect(settled).toHaveLength(1);
    expect(settled[0]).toMatchObject({ actorUserId: userIds.contributor, meta: { nodeId: n.nodeId, type: 'localai.model.load', status: 'succeeded' } });
    expect(await auditRows('localai.command.requested', command.id)).toHaveLength(1);
    expect(await auditRows('localai.command.cancelled', command.id)).toHaveLength(0);
    expect(commandFailed).not.toHaveBeenCalled();
  });

  it('a failure settles once: one settled row with the error code and ONE notification to the requester', async () => {
    const n = await enrolled();
    const command = (await api('POST', `/nodes/${n.nodeId}/commands`, tokens.contributor, { type: 'service.restart', params: { service: 'llama' } })).json().data;
    const claimed = (await n.agent('claim', { types: ALL, waitMs: 0 })).json().data.jobs[0];
    expect((await n.agent(`${command.id}/failure`, { claimToken: claimed.claimToken, error: 'LAI-LLM-011: Not enough room to load this model' })).statusCode).toBe(200);
    // The very same post again (a lost response) changes nothing and publishes nothing.
    expect((await n.agent(`${command.id}/failure`, { claimToken: claimed.claimToken, error: 'LAI-LLM-011: Not enough room to load this model' })).statusCode).toBe(200);

    const settled = await eventually(() => auditRows('localai.command.settled', command.id), (rows) => rows.length > 0);
    await eventually(async () => commandFailed.mock.calls.length, (calls) => calls > 0);
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(settled).toHaveLength(1);
    expect(settled[0].meta).toEqual({ nodeId: n.nodeId, type: 'localai.service.restart', status: 'failed', errorCode: 'LAI-LLM-011' });
    expect(commandFailed).toHaveBeenCalledTimes(1);
    expect(commandFailed).toHaveBeenCalledWith(
      expect.objectContaining({ recipientUserId: userIds.contributor, commandId: command.id, nodeId: n.nodeId, type: 'service.restart', status: 'failed', errorCode: 'LAI-LLM-011' }),
    );
    expect(commandFailed.mock.calls[0][0].message).toContain('service.restart on Node');
    expect(commandFailed.mock.calls[0][0].message).toContain('Unload a model first');
    expect((await api('GET', `/commands/${command.id}`, tokens.viewer)).json().data).toMatchObject({ status: 'failed', error: { code: 'LAI-LLM-011' } });
  });

  it('a command nobody claims expires (sweeper): one settled row and one notification', async () => {
    const n = await enrolled();
    const command = (await api('POST', `/nodes/${n.nodeId}/commands`, tokens.contributor, { type: 'status.refresh' })).json().data;
    await prisma.localaiCommand.update({ where: { id: command.id }, data: { expiresAt: new Date(Date.now() - 1000) } });
    await sweeper.sweep();
    const settled = await eventually(() => auditRows('localai.command.settled', command.id), (rows) => rows.length > 0);
    await eventually(async () => commandFailed.mock.calls.length, (calls) => calls > 0);
    expect(settled).toHaveLength(1);
    expect(settled[0].meta).toEqual({ nodeId: n.nodeId, type: 'localai.status.refresh', status: 'expired', errorCode: 'LAI-CLD-030' });
    expect(commandFailed).toHaveBeenCalledTimes(1);
    expect(commandFailed).toHaveBeenCalledWith(expect.objectContaining({ status: 'expired', errorCode: 'LAI-CLD-030', recipientUserId: userIds.contributor }));
  });
});

describe('the permission matrix on real sessions', () => {
  it.each(ALL)('%s: viewer 403, contributor by type, admin 201', async (short) => {
    const n = await enrolled();
    const params = examples[`localai.${short}`].params;
    const send = (token: string) => api('POST', `/nodes/${n.nodeId}/commands`, token, { type: short, params });
    expect((await send(tokens.viewer)).statusCode).toBe(403);
    expect((await send(tokens.contributor)).statusCode).toBe(ADMIN_ONLY.includes(short) ? 403 : 201);
    if (!ADMIN_ONLY.includes(short)) await prisma.localaiCommand.deleteMany({ where: { nodeId: n.rowId } });
    expect((await send(tokens.admin)).statusCode).toBe(201);
  });

  it('a viewer reads the history (lab:read) of a node and a contributor cancels an operate command but not an admin one', async () => {
    const n = await enrolled();
    const operate = (await api('POST', `/nodes/${n.nodeId}/commands`, tokens.contributor, { type: 'ark.verify' })).json().data;
    const destructive = (await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'model.delete', params: deleteParams })).json().data;
    const history = await api('GET', `/nodes/${n.nodeId}/commands`, tokens.viewer);
    expect(history.statusCode).toBe(200);
    expect(history.json().data).toMatchObject({ total: 2, page: 1, pageSize: 20 });
    expect(history.json().data.items.map((item: { id: string }) => item.id)).toEqual([destructive.id, operate.id]);
    expect((await api('GET', `/nodes/${n.nodeId}/commands?status=cancelled`, tokens.viewer)).json().data.total).toBe(0);
    expect((await api('DELETE', `/commands/${operate.id}`, tokens.viewer)).statusCode).toBe(403);
    expect((await api('DELETE', `/commands/${destructive.id}`, tokens.contributor)).statusCode).toBe(403);
    expect((await api('DELETE', `/commands/${operate.id}`, tokens.contributor)).statusCode).toBe(200);
    expect((await api('GET', `/nodes/${n.nodeId}/commands?status=cancelled`, tokens.viewer)).json().data.total).toBe(1);
  });
});

describe('cancel on real rows', () => {
  it('cancels a queued command with one conditional write, audits it once and frees its dedup key', async () => {
    const n = await enrolled();
    const command = (await api('POST', `/nodes/${n.nodeId}/commands`, tokens.contributor, { type: 'model.load', params: { model_id: MODEL } })).json().data;
    const cancelled = await api('DELETE', `/commands/${command.id}`, tokens.contributor);
    expect(cancelled.statusCode).toBe(200);
    expect(cancelled.json().data).toMatchObject({ status: 'cancelled' });
    expect(cancelled.json().data.finishedAt).toBeTruthy();
    expect(await auditRows('localai.command.cancelled', command.id)).toHaveLength(1);
    expect((await api('DELETE', `/commands/${command.id}`, tokens.contributor)).statusCode).toBe(409);
    expect(await auditRows('localai.command.cancelled', command.id)).toHaveLength(1);
    expect((await n.agent('claim', { types: ALL, waitMs: 0 })).json().data.jobs).toHaveLength(0); // never claimed
    const again = await api('POST', `/nodes/${n.nodeId}/commands`, tokens.contributor, { type: 'model.load', params: { model_id: MODEL } });
    expect(again.statusCode).toBe(201);
  });

  it('a claim that lands first wins: the cancel is 409 and the command keeps running', async () => {
    const n = await enrolled();
    const command = (await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'ark.verify' })).json().data;
    await n.agent('claim', { types: ALL, waitMs: 0 });
    const refused = await api('DELETE', `/commands/${command.id}`, tokens.admin);
    expect(refused.statusCode).toBe(409);
    expect(JSON.stringify(refused.json())).toContain('already running');
    expect((await prisma.localaiCommand.findUniqueOrThrow({ where: { id: command.id } })).status).toBe('running');
  });

  it('404 for an unknown or malformed id', async () => {
    expect((await api('DELETE', '/commands/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', tokens.admin)).statusCode).toBe(404);
    expect((await api('DELETE', '/commands/not-a-uuid', tokens.admin)).statusCode).toBe(404);
    expect((await api('GET', '/commands/not-a-uuid', tokens.admin)).statusCode).toBe(404);
  });
});

describe('refusals read the node row and its model rows', () => {
  it('unknown node 404; pending and retired 409 LAI-CLD-035', async () => {
    expect((await api('POST', `/nodes/ai-n0-${run}/commands`, tokens.admin, { type: 'status.refresh' })).statusCode).toBe(404);
    for (const phase of ['pending', 'retired'] as const) {
      const n = await enrolled({ phase, mode: null });
      const response = await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'status.refresh' });
      expect(response.statusCode).toBe(409);
      expect(JSON.stringify(response.json())).toContain('LAI-CLD-035');
      expect(await prisma.localaiCommand.count({ where: { nodeId: n.rowId } })).toBe(0);
    }
  });

  it('a node whose last snapshot says offline or survival is 422 LAI-CLD-034; back online it takes commands again', async () => {
    const n = await enrolled({ mode: 'offline' });
    const refused = await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'status.refresh' });
    expect(refused.statusCode).toBe(422);
    expect(JSON.stringify(refused.json())).toContain('LAI-CLD-034');
    await prisma.localaiNode.update({ where: { id: n.rowId }, data: { mode: 'survival' } });
    expect((await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'status.refresh' })).statusCode).toBe(422);
    await prisma.localaiNode.update({ where: { id: n.rowId }, data: { mode: 'online' } });
    expect((await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'status.refresh' })).statusCode).toBe(201);
  });

  it('mode.offline makes the node refuse the next command once its result lands (the optimistic mode)', async () => {
    const n = await enrolled();
    const command = (await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'mode.offline', params: { reason: 'drill' } })).json().data;
    const claimed = (await n.agent('claim', { types: ALL, waitMs: 0 })).json().data.jobs[0];
    await n.agent(`${command.id}/result`, { type: 'localai.mode.offline', claimToken: claimed.claimToken, result: { previous_mode: 'online', mode: 'offline' } });
    expect((await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'ark.verify' })).statusCode).toBe(422);
  });

  it('a model that is not downloaded on the node is 422 for load, unload, set_default and delete; download is 201', async () => {
    const absent = await enrolled({ withModel: false });
    for (const [type, params] of [
      ['model.load', { model_id: MODEL }],
      ['model.unload', { model_id: MODEL }],
      ['model.set_default', { model_id: MODEL }],
      ['model.delete', deleteParams],
    ] as const) {
      const response = await api('POST', `/nodes/${absent.nodeId}/commands`, tokens.admin, { type, params });
      expect(response.statusCode).toBe(422);
      expect(JSON.stringify(response.json())).toContain('Download it first');
    }
    expect((await api('POST', `/nodes/${absent.nodeId}/commands`, tokens.admin, { type: 'model.download', params: { model_id: MODEL } })).statusCode).toBe(201);

    const listedOnly = await enrolled();
    await prisma.localaiNodeModel.update({ where: { nodeId_modelId: { nodeId: listedOnly.rowId, modelId: MODEL } }, data: { present: false } });
    expect((await api('POST', `/nodes/${listedOnly.nodeId}/commands`, tokens.admin, { type: 'model.load', params: { model_id: MODEL } })).statusCode).toBe(422);
  });

  it('a body that is not valid is 400 and writes nothing', async () => {
    const n = await enrolled();
    expect((await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'model.load', params: { model_id: 'NOT VALID' } })).statusCode).toBe(400);
    expect((await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'shell.exec' })).statusCode).toBe(400);
    expect(await prisma.localaiCommand.count({ where: { nodeId: n.rowId } })).toBe(0);
  });
});

describe('source: session, personal access token and the desktop', () => {
  it('a session is ui (even if it claims to be the desktop), a pat_ caller is api, and desktop only when it says so', async () => {
    const n = await enrolled();
    const session = (await api('POST', `/nodes/${n.nodeId}/commands`, tokens.admin, { type: 'status.refresh' }, { 'x-localai-client': 'desktop' })).json().data;
    expect(session.source).toBe('ui');
    const asApi = await api('POST', `/nodes/${n.nodeId}/commands`, patToken, { type: 'logs.tail', params: { unit: 'agent' } });
    expect(asApi.statusCode).toBe(201);
    expect(asApi.json().data.source).toBe('api');
    const asDesktop = await api('POST', `/nodes/${n.nodeId}/commands`, patToken, { type: 'logs.tail', params: { unit: 'llama' } }, { 'x-localai-client': 'desktop' });
    expect(asDesktop.statusCode).toBe(201);
    expect(asDesktop.json().data.source).toBe('desktop');
    expect((await prisma.localaiCommand.findUniqueOrThrow({ where: { id: asDesktop.json().data.id } })).source).toBe('desktop');
  });
});
