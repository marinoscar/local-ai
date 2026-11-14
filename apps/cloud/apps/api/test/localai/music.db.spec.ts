// The music feature against real PostgreSQL (E11, #513): the whole app with its multipart plugin, real sessions for
// the three roles, a real `nod_` credential for the fake agent, and a fake object store in place of the bucket (the
// one thing a CI database cannot provide). POST a form -> the agent claims it through the node route and receives
// freshly signed URLs -> posts the result -> the songs are ready in the library -> audio -> delete. Also: the workload
// switch, the per-person limit and that no lyrics or signed URLs land in the command row, the history or the audit.
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import { MUSIC_PENDING_URL } from '../../src/localai/music/music-limits';
import { MUSIC_OBJECT_STORE, type MusicObjectStore } from '../../src/localai/music/music-store';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { multipartBody, wavBytes } from './support/multipart';

const run = `${Date.now()}`;
const LYRICS = '[verse]\nPerson B hums by the window\n[chorus]\nRain on the roof';

let app: NestFastifyApplication;
let prisma: PrismaService;
let credentials: NodeCredentialService;
const tokens = {} as Record<'admin' | 'contributor' | 'viewer', string>;
const userIds = {} as Record<'admin' | 'contributor' | 'viewer', string>;
const nodeRowIds: string[] = [];
const credentialIds: string[] = [];
const objects = new Map<string, Buffer>();
let counter = 0;

async function login(role: 'admin' | 'contributor' | 'viewer'): Promise<void> {
  const email = `music-${role}-${run}@example.test`;
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role } });
  tokens[role] = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  userIds[role] = (await prisma.user.findFirstOrThrow({ where: { email } })).id;
}

const auth = (bearer: string | null) => (bearer ? { authorization: `Bearer ${bearer}` } : {});
const api = (method: 'GET' | 'POST' | 'DELETE', url: string, bearer: string | null, payload?: unknown) =>
  app.inject({ method, url: `/api/localai${url}`, headers: auth(bearer), payload: payload as never });
const submit = (bearer: string, fields: Record<string, string>, sample?: Buffer) => {
  const body = multipartBody(fields, sample ? { field: 'sample', filename: 'hum.wav', contentType: 'audio/wav', bytes: sample } : undefined);
  return app.inject({ method: 'POST', url: '/api/localai/music/requests', headers: { ...auth(bearer), ...body.headers }, payload: body.payload });
};

async function enrolled(workloads: unknown = { active: 'coding', items: [{ name: 'music', label: { en: 'Music', es: 'Música' }, models: ['acestep-v15-turbo'], fits: true, missing: [] }] }) {
  counter += 1;
  const nodeId = `ai-m${counter}-${run}`;
  const minted = await credentials.createCredential(userIds.admin, { name: `${nodeId} credential` } as never);
  credentialIds.push(minted.id);
  const node = await prisma.localaiNode.create({
    data: {
      nodeId,
      displayName: `Music node ${counter}`,
      publicHost: `music-${counter}.example.test`,
      servePattern: 'none',
      phase: 'enrolled',
      mode: 'online',
      nodeCredentialId: minted.id,
      createdById: userIds.admin,
      lastState: { workloads } as never,
      lastStateAt: new Date(),
    },
  });
  nodeRowIds.push(node.id);
  const registered = await app.inject({
    method: 'POST',
    url: '/api/nodes/register',
    headers: { authorization: `Bearer ${minted.token}` },
    payload: { name: nodeId, hostname: 'host-beta', platform: 'linux/amd64', cliVersion: 'aiagent/0.1.0', eligibleTypes: ['localai.music.generate'], concurrency: 1, capabilities: { localai: { schema: 1, agent_version: '0.1.0' } } },
  });
  const worker = (registered.json() as { data: { node: { id: string } } }).data.node.id;
  const agent = (path: string, payload: unknown) =>
    app.inject({ method: 'POST', url: `/api/nodes/${worker}/localai/commands/${path}`, headers: { authorization: `Bearer ${minted.token}` }, payload: payload as never });
  return { rowId: node.id, nodeId, agent };
}

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
  // The bucket: the service and the claim hook hold this very instance, so its methods are replaced in place.
  const store = app.get<MusicObjectStore>(MUSIC_OBJECT_STORE);
  jest.spyOn(store, 'put').mockImplementation(async (key, bytes) => void objects.set(key, bytes));
  jest.spyOn(store, 'signedGet').mockImplementation(async (key, ttl) => `https://storage.example.test/b/${key}?X-Amz-Expires=${ttl}&X-Amz-Signature=get`);
  jest.spyOn(store, 'signedPut').mockImplementation(async (key, ttl) => `https://storage.example.test/b/${key}?X-Amz-Expires=${ttl}&X-Amz-Signature=put`);
  jest.spyOn(store, 'delete').mockImplementation(async (key) => void objects.delete(key));
  await login('admin');
  await login('contributor');
  await login('viewer');
});

afterAll(async () => {
  if (prisma) {
    const requests = await prisma.localaiMusicRequest.findMany({ where: { nodeId: { in: nodeRowIds } }, select: { id: true, songs: { select: { id: true } } } });
    const commandIds = (await prisma.localaiCommand.findMany({ where: { nodeId: { in: nodeRowIds } }, select: { id: true } })).map((command) => command.id);
    await prisma.auditEvent.deleteMany({
      where: {
        OR: [
          { targetType: 'localai_command', targetId: { in: commandIds } },
          { targetType: 'localai_music_request', targetId: { in: requests.map((request) => request.id) } },
          { targetType: 'localai_music_song', targetId: { in: requests.flatMap((request) => request.songs.map((song) => song.id)) } },
          { action: 'localai.workload.requested', targetId: { startsWith: 'ai-m' } },
        ],
      },
    });
    await prisma.localaiMusicRequest.deleteMany({ where: { nodeId: { in: nodeRowIds } } });
    await prisma.localaiCommand.deleteMany({ where: { nodeId: { in: nodeRowIds } } });
    await prisma.localaiNode.deleteMany({ where: { id: { in: nodeRowIds } } });
    await prisma.workerNode.deleteMany({ where: { createdById: userIds.admin } });
    await prisma.nodeCredential.deleteMany({ where: { id: { in: credentialIds } } });
  }
  await app?.close();
});

describe('a music request, end to end', () => {
  it('form -> queued with placeholders -> claimed with signed URLs -> result -> ready songs -> audio -> delete', async () => {
    const n = await enrolled();
    const sample = wavBytes(6000);
    const created = await submit(tokens.contributor, { nodeId: n.nodeId, lyrics: LYRICS, tags: 'lofi', variations: '2', duration_s: '30' }, sample);
    expect(created.statusCode).toBe(201);
    const request = created.json().data;
    expect(request).toMatchObject({ nodeId: n.nodeId, status: 'queued', title: 'Person B hums by the window', variations: 2, hasSample: true, sampleMode: 'sounds_like', strength: 0.5, lyrics: LYRICS });

    // Stored: the row, two pending songs, the sample object, the command with placeholders only.
    const row = await prisma.localaiMusicRequest.findUniqueOrThrow({ where: { id: request.id }, include: { songs: true, command: true } });
    expect(row.songs.map((song) => [song.index, song.status, song.storageKey]).sort()).toEqual([
      [0, 'pending', `localai-music/songs/${request.id}/0`],
      [1, 'pending', `localai-music/songs/${request.id}/1`],
    ]);
    expect(row.sampleKey).toBe(`localai-music/samples/${request.id}.wav`);
    expect(objects.get(row.sampleKey!)?.equals(sample)).toBe(true);
    expect(JSON.stringify(row.command?.params)).not.toContain('X-Amz-Signature');
    expect((row.command?.params as { results: unknown[] }).results).toEqual([{ put_url: MUSIC_PENDING_URL }, { put_url: MUSIC_PENDING_URL }]);
    expect(row).not.toHaveProperty('lyrics');

    // The history and the audit show no lyrics and no URL.
    const history = await api('GET', `/commands/${row.commandId}`, tokens.viewer);
    expect(JSON.stringify(history.json())).not.toMatch(/Person B|X-Amz|signed-at-claim/);
    const audit = await prisma.auditEvent.findMany({ where: { targetId: { in: [request.id, row.commandId!] } } });
    expect(audit.map((entry) => entry.action).sort()).toEqual(['localai.music.requested']);
    expect(JSON.stringify(audit)).not.toMatch(/Person B|lofi|https:/);

    // The node claims: real signed URLs, one hour, for the stored keys.
    const claimed = (await n.agent('claim', { types: ['music.generate'], limit: 1, waitMs: 0 })).json().data.jobs[0];
    expect(claimed.job).toMatchObject({ id: row.commandId, type: 'localai.music.generate' });
    expect(claimed.params.sample.url).toBe(`https://storage.example.test/b/localai-music/samples/${request.id}.wav?X-Amz-Expires=3600&X-Amz-Signature=get`);
    expect(claimed.params.results).toEqual([0, 1].map((index) => ({ put_url: `https://storage.example.test/b/localai-music/songs/${request.id}/${index}?X-Amz-Expires=3600&X-Amz-Signature=put` })));
    expect(claimed.params.lyrics).toBe(LYRICS);
    expect((await api('GET', `/music/requests/${request.id}`, tokens.contributor)).json().data.stage).toBe('generating');
    // Still nothing signed in the database.
    expect(JSON.stringify((await prisma.localaiCommand.findUniqueOrThrow({ where: { id: row.commandId! } })).params)).not.toContain('X-Amz-Signature');

    const songs = [0, 1].map((index) => ({ index, sha256: String(index + 3).repeat(64), size_bytes: 5000 + index, mime: 'audio/wav', duration_s: 30.2 }));
    const done = await n.agent(`${row.commandId}/result`, { type: 'localai.music.generate', claimToken: claimed.claimToken, result: { songs } });
    expect(done.statusCode).toBe(200);

    const finished = await eventually(
      async () => (await api('GET', `/music/requests/${request.id}`, tokens.contributor)).json().data,
      (view) => view.status === 'succeeded',
    );
    expect(finished.songs.map((song: { status: string; sizeBytes: number }) => [song.status, song.sizeBytes])).toEqual([['ready', 5000], ['ready', 5001]]);

    const library = (await api('GET', `/music/songs?nodeId=${n.nodeId}`, tokens.contributor)).json().data;
    expect(library.total).toBe(2);
    expect((await api('GET', `/music/songs?nodeId=${n.nodeId}`, tokens.admin)).json().data.total).toBe(2);

    const songId = library.items[0].id as string;
    const audio = await api('GET', `/music/songs/${songId}/audio`, tokens.contributor);
    expect(audio.statusCode).toBe(302);
    expect(audio.headers.location).toMatch(/X-Amz-Expires=300&/);

    expect((await api('DELETE', `/music/songs/${songId}`, tokens.contributor, { confirm: songId })).statusCode).toBe(403);
    expect((await api('DELETE', `/music/songs/${songId}`, tokens.admin, { confirm: songId })).statusCode).toBe(200);
    expect(await prisma.localaiMusicSong.count({ where: { requestId: request.id } })).toBe(1);
    const deletedAudit = await prisma.auditEvent.findMany({ where: { action: 'localai.music.song_deleted', targetId: songId } });
    expect(deletedAudit).toHaveLength(1);
    expect(deletedAudit[0].actorUserId).toBe(userIds.admin);
  });

  it('a failed command keeps its doctor code on the request; the songs fail', async () => {
    const n = await enrolled();
    const request = (await submit(tokens.contributor, { nodeId: n.nodeId, tags: 'ambient piano' })).json().data;
    const claimed = (await n.agent('claim', { types: ['music.generate'], limit: 1, waitMs: 0 })).json().data.jobs[0];
    await n.agent(`${claimed.job.id}/failure`, { claimToken: claimed.claimToken, error: 'LAI-SVC-151: Music is not loaded on this computer.' });
    const view = await eventually(
      async () => (await api('GET', `/music/requests/${request.id}`, tokens.contributor)).json().data,
      (value) => value.status === 'failed',
    );
    expect(view).toMatchObject({ status: 'failed', error: { code: 'LAI-SVC-151' }, songs: [{ status: 'failed' }] });
  });

  it('a person may have three requests waiting; the fourth is 429 LAI-SVC-153', async () => {
    const n = await enrolled();
    // Distinct nodes keep each request queued (one active command per dedup key is per request anyway).
    for (let i = 0; i < 3; i += 1) expect((await submit(tokens.admin, { nodeId: n.nodeId, tags: `style ${i}` })).statusCode).toBe(201);
    const fourth = await submit(tokens.admin, { nodeId: n.nodeId, tags: 'style 4' });
    expect(fourth.statusCode).toBe(429);
    expect(fourth.json().details.code).toBe('LAI-SVC-153');
  });

  it('a viewer cannot make music', async () => {
    const n = await enrolled();
    expect((await submit(tokens.viewer, { nodeId: n.nodeId, tags: 'x' })).statusCode).toBe(403);
  });
});

describe('the workload switch', () => {
  it('queues workload.activate from the snapshot\'s workloads; an unreported workload is 422 LAI-LLM-050', async () => {
    const n = await enrolled();
    const queued = await api('POST', `/nodes/${n.nodeId}/workloads/music/activate`, tokens.contributor, {});
    expect(queued.statusCode).toBe(201);
    expect(queued.json().data).toMatchObject({ nodeId: n.nodeId, type: 'localai.workload.activate', status: 'queued', params: { name: 'music' } });
    expect(await prisma.auditEvent.count({ where: { action: 'localai.workload.requested', targetId: n.nodeId } })).toBe(1);
    const unknown = await api('POST', `/nodes/${n.nodeId}/workloads/chat/activate`, tokens.contributor, {});
    expect([unknown.statusCode, unknown.json().details.code]).toEqual([422, 'LAI-LLM-050']);
    expect((await api('POST', `/nodes/${n.nodeId}/workloads/music/activate`, tokens.viewer, {})).statusCode).toBe(403);
  });
});
