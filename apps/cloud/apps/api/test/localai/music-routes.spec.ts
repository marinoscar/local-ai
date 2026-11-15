// The music routes over HTTP (E11, #513) on the whole AppModule over a mocked database: the real guards with the real
// seeded grants, the real multipart reader with the app's plugin, the real controller, service, command plane, audit
// sink and event bus; the in-memory command and music tables; a fake object store. This proves the RBAC matrix, the
// multipart limits with their doctor codes, the enqueue (params, signed URLs, sha256), the status transitions from the
// command, the library, the audio redirect, the delete confirmation, the audit rows and that no lyrics reach a log
// line. Also the prompt history and titles of #568 (owner checks, make again, save as new, delete keeps the songs, the
// AI helpers on, off and failing through a fake `MUSIC_AI`). The SQL itself is `music.db.spec.ts`.
import { createHash } from 'node:crypto';
import fastifyCookie from '@fastify/cookie';
import multipart from '@fastify/multipart';
import { JwtService } from '@nestjs/jwt';
import { FastifyAdapter, type NestFastifyApplication } from '@nestjs/platform-fastify';
import { SchedulerRegistry } from '@nestjs/schedule';
import { Test } from '@nestjs/testing';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';
import { JobWorker } from '@marinoscar/platform-api/jobs';

import '../../src/platform/registrations';
import { AppModule } from '../../src/app.module';
import { CommandClaimRepository } from '../../src/localai/commands/claim.sql';
import { COMMAND_SETTLED_CHANNEL, LOCALAI_CLOCK } from '../../src/localai/commands/command-events';
import { LOCALAI_COMMAND_NOTIFIER } from '../../src/localai/commands/command-notifier';
import { COMMANDS } from '../../src/localai/contracts/commands';
import { ClaimService } from '../../src/localai/commands/claim.service';
import { MUSIC_AI, MusicAiUnavailable, type MusicAiAsk, type MusicAiCaller } from '../../src/localai/music/music-ai';
import { SettingsMusicAiSwitch } from '../../src/localai/music/music-ai-switch';
import { MUSIC_PENDING_URL } from '../../src/localai/music/music-limits';
import { MUSIC_OBJECT_STORE, MusicStorageNotConfigured, type MusicObjectStore } from '../../src/localai/music/music-store';
import { PrismaService } from '../../src/prisma/prisma.service';
import { PrismaSystemService } from '../../src/prisma/prisma-system.service';
import { clearMockUsers, installBaseMocks, registerMockUser, type FixtureRole } from '../ai-conformance/mock-users';
import { prismaMock, resetPrismaMock } from '../ai-conformance/prisma.mock';
import { captureOutput, enableNestLogging } from './support/capture-output';
import { FakeClock, FakeCommandStore, StoreClaimRepository } from './support/fake-command-store';
import { FakeMusicTables } from './support/fake-music-tables';
import { multipartBody, wavBytes } from './support/multipart';

const NODE_ROW = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee5';
const LYRICS = '[verse]\nPerson A walks to the river at dawn\n[chorus]\nOrg Alpha sings along';
const SIGNED = 'https://storage.example.test/bucket/';
const WORKLOADS = {
  active: 'coding',
  items: [
    { name: 'coding', label: { en: 'Coding', es: 'Programación' }, models: ['qwen3.6-35b-a3b-q4km'], fits: true, missing: [] },
    { name: 'music', label: { en: 'Music', es: 'Música' }, models: ['acestep-v15-turbo'], fits: true, missing: [] },
    { name: 'video', label: { en: 'Video', es: 'Video' }, models: ['qwen3.6-35b-a3b-q4km'], fits: false, missing: [] },
  ],
};

const clock = new FakeClock(new Date('2026-11-14T12:00:00.000Z'));
const store = new FakeCommandStore(clock);
const music = new FakeMusicTables();
music.now = () => clock.now();

/** The object store: records every call, answers https URLs that name the key. */
class FakeStore implements MusicObjectStore {
  readonly objects = new Map<string, Buffer>();
  readonly calls: string[] = [];
  failWith: Error | null = null;
  scheme = 'https://';
  async put(key: string, bytes: Buffer): Promise<void> {
    this.calls.push(`put ${key}`);
    if (this.failWith) throw this.failWith;
    this.objects.set(key, bytes);
  }
  async signedGet(key: string, ttlS: number, disposition?: { filename: string; attachment: boolean }): Promise<string> {
    this.calls.push(`get ${key} ${ttlS}${disposition ? ` ${disposition.attachment ? 'attachment' : 'inline'}` : ''}`);
    if (this.failWith) throw this.failWith;
    return `${this.scheme}storage.example.test/bucket/${key}?X-Amz-Expires=${ttlS}&X-Amz-Signature=get`;
  }
  async signedPut(key: string, ttlS: number): Promise<string> {
    this.calls.push(`signput ${key} ${ttlS}`);
    if (this.failWith) throw this.failWith;
    return `${this.scheme}storage.example.test/bucket/${key}?X-Amz-Expires=${ttlS}&X-Amz-Signature=put`;
  }
  async delete(key: string): Promise<void> {
    this.calls.push(`delete ${key}`);
    this.objects.delete(key);
  }
}
const objects = new FakeStore();

/**
 * The cloud's AI as the music helpers see it: off, on (answers `answer`), failing, no model for the person, or a model
 * that resolves but whose call finds no key (`gone`). The music switch ("Use AI for music") is `musicSwitch`.
 */
const ai = {
  state: 'off' as 'off' | 'on' | 'error' | 'no_model' | 'gone',
  answer: '',
  asked: [] as MusicAiAsk[],
  callers: [] as MusicAiCaller[],
  async aiEnabled(): Promise<boolean> {
    return ai.state !== 'off';
  },
  async modelResolves(): Promise<boolean> {
    return ai.state !== 'no_model';
  },
  async ask(caller: MusicAiCaller, ask: MusicAiAsk): Promise<string> {
    ai.asked.push(ask);
    ai.callers.push(caller);
    if (ai.state === 'error') throw Object.assign(new Error('the provider said Person B harbour'), { code: 'AI_PROVIDER_ERROR' });
    if (ai.state === 'gone') throw new MusicAiUnavailable('no_model');
    return ai.answer;
  },
};

/** "Use AI for music": the deployment's value, as the switch route and the assist read it. */
const musicSwitch = {
  on: true,
  writes: [] as Array<{ aiEnabled: boolean; actor: string }>,
  async isOn(): Promise<boolean> {
    return musicSwitch.on;
  },
  async deploymentValue() {
    return { aiEnabled: musicSwitch.on };
  },
  async setDeploymentValue(aiEnabled: boolean, actor: string) {
    musicSwitch.writes.push({ aiEnabled, actor });
    musicSwitch.on = aiEnabled;
    return { aiEnabled };
  },
};

describe('Local AI music routes over HTTP', () => {
  let app: NestFastifyApplication;
  let jwt: JwtService;
  let bus: EventBus;
  let node: { id: string; nodeId: string; displayName: string; phase: string; mode: string | null; lastState: unknown } | null;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({ imports: [AppModule] })
      .overrideProvider(PrismaService)
      .useValue(prismaMock)
      .overrideProvider(PrismaSystemService)
      .useValue({ asSystem: () => prismaMock, runAsSystem: (_r: string, fn: (tx: unknown) => unknown) => fn(prismaMock), $connect: async () => undefined, $disconnect: async () => undefined })
      .overrideProvider(JobWorker)
      .useValue({})
      .overrideProvider(CommandClaimRepository)
      .useValue(new StoreClaimRepository(store))
      .overrideProvider(LOCALAI_CLOCK)
      .useValue(clock)
      .overrideProvider(LOCALAI_COMMAND_NOTIFIER)
      .useValue({ commandFailed: jest.fn().mockResolvedValue(undefined) })
      .overrideProvider(MUSIC_OBJECT_STORE)
      .useValue(objects)
      .overrideProvider(MUSIC_AI)
      .useValue(ai)
      .overrideProvider(SettingsMusicAiSwitch)
      .useValue(musicSwitch)
      .compile();
    app = moduleRef.createNestApplication<NestFastifyApplication>(new FastifyAdapter());
    await app.register(fastifyCookie, { secret: 'test-secret' });
    // As main.ts does with the storage slice on: one file, the platform's 100 MiB ceiling (the route applies 20 MiB).
    await app.register(multipart, { limits: { fileSize: 100 * 1024 * 1024, files: 1 } });
    app.setGlobalPrefix('api');
    resetPrismaMock();
    installBaseMocks();
    await app.init();
    await app.getHttpAdapter().getInstance().ready();
    for (const cronJob of app.get(SchedulerRegistry).getCronJobs().values()) void cronJob.stop();
    jwt = moduleRef.get(JwtService);
    bus = moduleRef.get(EVENT_BUS);
  });

  afterAll(async () => {
    clearMockUsers();
    await app?.close();
  });

  beforeEach(() => {
    resetPrismaMock();
    installBaseMocks();
    store.rows.length = 0;
    store.knownNodes.set('ai-n2', NODE_ROW);
    music.reset();
    music.nodes.set(NODE_ROW, { nodeId: 'ai-n2', displayName: 'Node ai-n2' });
    objects.objects.clear();
    objects.calls.length = 0;
    objects.failWith = null;
    objects.scheme = 'https://';
    ai.state = 'off';
    ai.answer = '';
    ai.asked.length = 0;
    ai.callers.length = 0;
    musicSwitch.on = true;
    musicSwitch.writes.length = 0;
    clock.set(new Date('2026-11-14T12:00:00.000Z'));
    node = { id: NODE_ROW, nodeId: 'ai-n2', displayName: 'Node ai-n2', phase: 'enrolled', mode: 'online', lastState: { workloads: WORKLOADS } };
    prismaMock.localaiNode.findUnique.mockImplementation(async ({ where }: { where: { nodeId?: string } }) => (where.nodeId === 'ai-n2' ? node : null));
    prismaMock.localaiNodeModel.findUnique.mockResolvedValue({ present: true });
    prismaMock.localaiNode.findMany.mockResolvedValue([]);
    for (const method of ['create', 'findFirst', 'findMany', 'findUnique', 'count', 'updateMany'] as const) {
      prismaMock.localaiCommand[method].mockImplementation((store.localaiCommand as any)[method]);
    }
    for (const method of ['create', 'findUnique', 'findMany', 'count', 'update', 'updateMany'] as const) {
      prismaMock.localaiMusicRequest[method].mockImplementation((music.localaiMusicRequest as any)[method]);
    }
    for (const method of ['findUnique', 'findMany', 'count', 'updateMany', 'deleteMany'] as const) {
      prismaMock.localaiMusicSong[method].mockImplementation((music.localaiMusicSong as any)[method]);
    }
    for (const method of ['create', 'findUnique', 'findMany', 'count', 'update', 'updateMany', 'deleteMany'] as const) {
      prismaMock.localaiMusicPrompt[method].mockImplementation((music.localaiMusicPrompt as any)[method]);
    }
    prismaMock.auditEvent.create.mockResolvedValue({});
    prismaMock.auditEvent.findFirst.mockResolvedValue(null);
  });

  const token = (role: FixtureRole) => registerMockUser(jwt, role).accessToken;
  const auth = (accessToken: string | null) => (accessToken ? { authorization: `Bearer ${accessToken}` } : {});
  const send = (method: 'GET' | 'POST' | 'DELETE' | 'PUT', url: string, accessToken: string | null, payload?: unknown) =>
    app.inject({ method, url: `/api/localai/music${url}`, headers: auth(accessToken), payload: payload as never });
  const activate = (accessToken: string | null, nodeId: string, name: string, payload: unknown = {}) =>
    app.inject({ method: 'POST', url: `/api/localai/nodes/${nodeId}/workloads/${name}/activate`, headers: auth(accessToken), payload: payload as never });
  const claimMusic = async () => (await app.get(ClaimService).claim(NODE_ROW, { types: ['music.generate'], waitMs: 0 })).jobs[0];
  const submit = (accessToken: string | null, fields: Record<string, string>, file?: Parameters<typeof multipartBody>[1], extra: Array<[string, string]> = []) => {
    const body = multipartBody(fields, file, extra);
    return app.inject({ method: 'POST', url: '/api/localai/music/requests', headers: { ...auth(accessToken), ...body.headers }, payload: body.payload });
  };
  const sample = (bytes = wavBytes(8000)) => ({ field: 'sample', filename: 'hum.wav', contentType: 'audio/wav', bytes });
  const audits = (action: string) => prismaMock.auditEvent.create.mock.calls.map((call: any[]) => call[0].data).filter((data: any) => data.action === action);
  const musicCommands = () => store.rows.filter((row) => row.type === 'localai.music.generate');
  const settle = async (commandId: string, status: 'succeeded' | 'failed' | 'expired', patch: Record<string, unknown>) => {
    Object.assign(store.get(commandId), { status, finishedAt: clock.now(), ...patch });
    await bus.publish(COMMAND_SETTLED_CHANNEL, { nodeId: NODE_ROW, commandId, status });
    await new Promise((resolve) => setImmediate(resolve));
  };

  describe('authentication and the permission matrix', () => {
    const SONG = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1';
    const routes: Array<[string, 'GET' | 'POST' | 'DELETE', string]> = [
      ['list requests', 'GET', '/requests'],
      ['one request', 'GET', `/requests/${SONG}`],
      ['cancel', 'POST', `/requests/${SONG}/cancel`],
      ['library', 'GET', '/songs'],
      ['audio', 'GET', `/songs/${SONG}/audio`],
      ['delete', 'DELETE', `/songs/${SONG}`],
      ['nodes', 'GET', '/nodes'],
    ];

    it.each(routes)('%s: 401 without a session', async (_name, method, url) => {
      expect((await send(method, url, null)).statusCode).toBe(401);
    });

    it('POST /requests: 401 without a session', async () => {
      expect((await submit(null, { nodeId: 'ai-n2', lyrics: LYRICS })).statusCode).toBe(401);
    });

    it('a viewer is 403 on every music route but the node list (lab:read), and nothing is queued or stored', async () => {
      const viewer = token('viewer');
      for (const [, method, url] of routes) {
        const response = await send(method, url, viewer, method === 'DELETE' ? { confirm: SONG } : undefined);
        expect([url, response.statusCode]).toEqual([url, url === '/nodes' ? 200 : 403]);
      }
      expect((await submit(viewer, { nodeId: 'ai-n2', lyrics: LYRICS }, sample())).statusCode).toBe(403);
      expect((await activate(viewer, 'ai-n2', 'music')).statusCode).toBe(403);
      expect((await activate(null, 'ai-n2', 'music')).statusCode).toBe(401);
      expect(store.rows).toHaveLength(0);
      expect(objects.calls).toEqual([]);
    });

    it('a contributor makes and plays music and switches workloads, but cannot delete a song', async () => {
      const contributor = token('contributor');
      expect((await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS })).statusCode).toBe(201);
      expect((await send('GET', '/songs', contributor)).statusCode).toBe(200);
      expect((await activate(contributor, 'ai-n2', 'music')).statusCode).toBe(201);
      expect((await send('DELETE', `/songs/${SONG}`, contributor, { confirm: SONG })).statusCode).toBe(403);
    });

    it('an admin may delete (404 for a song nobody knows)', async () => {
      expect((await send('DELETE', `/songs/${SONG}`, token('admin'), { confirm: SONG })).statusCode).toBe(404);
    });
  });

  describe('POST /requests: the limits (LAI-SVC-154)', () => {
    const expectCode = (response: { statusCode: number; json(): any }, status: number, reason?: string) => {
      expect(response.statusCode).toBe(status);
      expect(response.json().details).toMatchObject({ code: 'LAI-SVC-154', ...(reason ? { reason } : {}) });
      expect(store.rows).toHaveLength(0);
      expect(music.requests).toHaveLength(0);
    };

    it('a JSON body is not a form: 400', async () => {
      const response = await send('POST', '/requests', token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS });
      expect(response.statusCode).toBe(400);
      expect(response.json().details).toMatchObject({ code: 'LAI-SVC-154', reason: 'not_multipart' });
    });

    it('a sample over 20 MiB: 413, and nothing is stored', async () => {
      expectCode(await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS }, sample(wavBytes(20 * 1024 * 1024 + 1))), 413, 'sample_too_large');
      expect(objects.calls).toEqual([]);
    });

    it('a sample of exactly 20 MiB is taken', async () => {
      const response = await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS }, sample(wavBytes(20 * 1024 * 1024)));
      expect(response.statusCode).toBe(201);
    });

    it('a sample that is not audio by its bytes: 415, whatever its name and type say', async () => {
      expectCode(await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS }, { field: 'sample', filename: 'song.wav', contentType: 'audio/wav', bytes: Buffer.from('<script>alert(1)</script>') }), 415, 'sample_type');
    });

    it('lyrics over 8 KiB: 400; far over (past the field cap): 413', async () => {
      expectCode(await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: 'a'.repeat(8193) }), 400, 'invalid_input');
      expectCode(await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: 'a'.repeat(20_000) }), 413, 'field_too_large');
    });

    it('tags over 512 bytes, a bad duration or variation count, an unknown field, a duplicate field: 400', async () => {
      const contributor = token('contributor');
      expectCode(await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS, tags: 't'.repeat(513) }), 400);
      expectCode(await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS, duration_s: '300' }), 400);
      expectCode(await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS, variations: '5' }), 400);
      expectCode(await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS, put_url: 'https://elsewhere.example.test/' }), 400);
      expectCode(await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS }, undefined, [['lyrics', 'again']]), 400, 'duplicate_field');
    });

    it('a file under another name, or sample settings without a sample: 400', async () => {
      const contributor = token('contributor');
      expectCode(await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS }, { ...sample(), field: 'other' }), 400, 'unknown_file_field');
      expectCode(await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS, sample_mode: 'same_song' }), 400, 'sample_settings_without_sample');
    });
  });

  describe('POST /requests: the node and the person', () => {
    it('an unknown node is 404; a retired node 409 LAI-CLD-035; an Offline node 422 LAI-CLD-034; nothing is stored', async () => {
      const contributor = token('contributor');
      expect((await submit(contributor, { nodeId: 'ai-n9', lyrics: LYRICS }, sample())).statusCode).toBe(404);
      node = { ...node!, phase: 'retired' };
      const retired = await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS }, sample());
      expect([retired.statusCode, retired.json().details.code]).toEqual([409, 'LAI-CLD-035']);
      node = { ...node!, phase: 'enrolled', mode: 'offline' };
      const offline = await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS }, sample());
      expect([offline.statusCode, offline.json().details.code]).toEqual([422, 'LAI-CLD-034']);
      expect(objects.calls).toEqual([]);
      expect(store.rows).toHaveLength(0);
    });

    it('three requests waiting is the limit: the fourth is 429 LAI-SVC-153', async () => {
      const contributor = registerMockUser(jwt, 'contributor');
      for (let i = 0; i < 3; i += 1) expect((await submit(contributor.accessToken, { nodeId: 'ai-n2', lyrics: `${LYRICS} ${i}` })).statusCode).toBe(201);
      const fourth = await submit(contributor.accessToken, { nodeId: 'ai-n2', lyrics: LYRICS });
      expect(fourth.statusCode).toBe(429);
      expect(fourth.json().details).toMatchObject({ code: 'LAI-SVC-153', reason: 'too_many_active' });
      // Another person is not affected.
      expect((await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS })).statusCode).toBe(201);
    });

    it('thirty requests in an hour is the limit', async () => {
      const contributor = registerMockUser(jwt, 'contributor');
      for (let i = 0; i < 30; i += 1) {
        await music.localaiMusicRequest.create({ data: { nodeId: NODE_ROW, requestedById: contributor.id, status: 'succeeded', title: 't', tags: '', lyricsBytes: 1, durationS: 60, language: 'en', variations: 1 } });
      }
      const response = await submit(contributor.accessToken, { nodeId: 'ai-n2', lyrics: LYRICS });
      expect(response.statusCode).toBe(429);
      expect(response.json().details).toMatchObject({ code: 'LAI-SVC-153', reason: 'too_many_per_hour' });
    });

    it('storage not set up, or not https: 503, and nothing is queued', async () => {
      objects.failWith = new MusicStorageNotConfigured();
      const missing = await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS }, sample());
      expect([missing.statusCode, missing.json().details.reason]).toEqual([503, 'storage_not_configured']);
      objects.failWith = null;
      objects.scheme = 'http://';
      const plain = await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS }, sample());
      expect([plain.statusCode, plain.json().details.reason]).toEqual([503, 'storage_not_https']);
      expect(objects.objects.size).toBe(0);
      expect(music.requests).toHaveLength(0);
      expect(store.rows).toHaveLength(0);
    });
  });

  describe('the enqueue and the request life', () => {
    it('stores the sample, mints one PUT per variation, queues music.generate with its sha256, audits without lyrics, and logs no lyrics', async () => {
      const restoreLogger = enableNestLogging();
      const output = captureOutput();
      const bytes = wavBytes(12_000);
      let response;
      try {
        response = await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS, tags: 'folk, warm', variations: '2', duration_s: '90', language: 'es', seed: '7', sample_mode: 'same_song' }, sample(bytes));
      } finally {
        output.restore();
        restoreLogger();
      }
      expect(response.statusCode).toBe(201);
      const view = response.json().data;
      expect(view).toMatchObject({
        nodeId: 'ai-n2',
        status: 'queued',
        stage: 'queued',
        title: 'Person A Walks To The River At Dawn',
        tags: 'folk, warm',
        durationS: 90,
        language: 'es',
        seed: 7,
        variations: 2,
        hasSample: true,
        sampleMode: 'same_song',
        strength: 1,
        requestedByMe: true,
      });
      expect(view.songs.map((song: any) => [song.index, song.status])).toEqual([[0, 'pending'], [1, 'pending']]);
      expect(JSON.stringify(view)).not.toContain('river at dawn\n');
      expect(JSON.stringify(view)).not.toContain('X-Amz-Signature');

      // Stored: the strict params with placeholder URLs; no signed URL is ever stored.
      const [command] = musicCommands();
      const params = COMMANDS['localai.music.generate'].params.parse(command.params);
      expect(params).toMatchObject({ request_id: view.id, lyrics: LYRICS, tags: 'folk, warm', duration_s: 90, language: 'es', seed: 7, variations: 2, sample_mode: 'same_song', strength: 1 });
      expect(params.sample).toEqual({ url: MUSIC_PENDING_URL, sha256: createHash('sha256').update(bytes).digest('hex'), mime: 'audio/wav', size_bytes: bytes.length });
      expect(params.results).toEqual([{ put_url: MUSIC_PENDING_URL }, { put_url: MUSIC_PENDING_URL }]);
      expect(JSON.stringify(command.params)).not.toContain('X-Amz-Signature');
      expect(objects.objects.get(`localai-music/samples/${view.id}.wav`)?.equals(bytes)).toBe(true);
      expect(command.dedupKey).toBe(`music.generate:${view.id}`);

      // Claimed: the node gets a one-hour signed GET of the sample and one PUT per song, for the stored keys.
      const claimed = await claimMusic();
      const sent = COMMANDS['localai.music.generate'].params.parse(claimed.params);
      expect(sent.sample?.url).toBe(`${SIGNED}localai-music/samples/${view.id}.wav?X-Amz-Expires=3600&X-Amz-Signature=get`);
      expect(sent.results.map((result) => result.put_url)).toEqual([0, 1].map((index) => `${SIGNED}localai-music/songs/${view.id}/${index}?X-Amz-Expires=3600&X-Amz-Signature=put`));
      expect(sent.lyrics).toBe(LYRICS);
      expect(JSON.stringify(store.get(command.id).params)).not.toContain('X-Amz-Signature');

      const [row] = audits('localai.music.requested');
      expect(row).toMatchObject({ targetType: 'localai_music_request', targetId: view.id });
      expect(row.meta).toMatchObject({ nodeId: 'ai-n2', variations: 2, durationS: 90, hasSample: true, sampleMode: 'same_song', sampleBytes: bytes.length });
      expect(JSON.stringify(row)).not.toMatch(/Person A|folk|https:/);
      expect(output.text()).toContain(`music request ${view.id} queued`);
      expect(output.text()).not.toContain('Person A');
      expect(output.text()).not.toContain('X-Amz-Signature');
    });

    it('the lyrics come back to their author on the single-request route, and to nobody else', async () => {
      const author = registerMockUser(jwt, 'contributor');
      const created = (await submit(author.accessToken, { nodeId: 'ai-n2', lyrics: LYRICS })).json().data;
      expect(created.lyrics).toBe(LYRICS);
      expect((await send('GET', `/requests/${created.id}`, author.accessToken)).json().data.lyrics).toBe(LYRICS);
      const admin = (await send('GET', `/requests/${created.id}`, token('admin'))).json().data;
      expect(admin.lyrics).toBeUndefined();
      expect(admin.lyricsBytes).toBe(new TextEncoder().encode(LYRICS).length);
    });

    it('the command history shows the request without its lyrics or URLs', async () => {
      await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS }, sample());
      const history = await app.inject({ method: 'GET', url: `/api/localai/commands/${musicCommands()[0].id}`, headers: auth(token('viewer')) });
      expect(history.statusCode).toBe(200);
      expect(JSON.stringify(history.json())).not.toMatch(/Person A|X-Amz-Signature/);
      expect(history.json().data.params).toMatchObject({ has_sample: true, results: 1 });
    });

    it('queued -> generating -> done: the songs become ready and appear in the library', async () => {
      const contributor = registerMockUser(jwt, 'contributor');
      const created = (await submit(contributor.accessToken, { nodeId: 'ai-n2', lyrics: LYRICS, variations: '2' })).json().data;
      const command = musicCommands()[0];

      Object.assign(store.get(command.id), { status: 'running', claimedAt: clock.now() });
      expect((await send('GET', `/requests/${created.id}`, contributor.accessToken)).json().data).toMatchObject({ status: 'running', stage: 'generating' });

      clock.advance(90_000);
      const sha = (digit: string) => digit.repeat(64);
      await settle(command.id, 'succeeded', { result: { songs: [{ index: 0, sha256: sha('a'), size_bytes: 1000, mime: 'audio/wav', duration_s: 60.1 }, { index: 1, sha256: sha('b'), size_bytes: 1001, mime: 'audio/wav', duration_s: 59.9 }] } });
      const done = (await send('GET', `/requests/${created.id}`, contributor.accessToken)).json().data;
      expect(done).toMatchObject({ status: 'succeeded', stage: 'done', finishedAt: '2026-11-14T12:01:30.000Z' });
      expect(done.songs.map((song: any) => [song.index, song.status, song.sizeBytes, song.durationS])).toEqual([[0, 'ready', 1000, 60.1], [1, 'ready', 1001, 59.9]]);

      const library = (await send('GET', '/songs', token('admin'))).json().data;
      expect(library.total).toBe(2);
      expect(library.items[0]).toMatchObject({ nodeId: 'ai-n2', title: 'Person A Walks To The River At Dawn', status: 'ready', requestedByMe: false });
      expect((await send('GET', '/songs?nodeId=ai-n9', token('admin'))).json().data.total).toBe(0);
      expect((await send('GET', '/songs?mine=true', token('admin'))).json().data.total).toBe(0);
      expect((await send('GET', '/songs', contributor.accessToken)).json().data.total).toBe(2);
      // Another contributor sees only their own songs (none), and cannot read this request.
      expect((await send('GET', '/songs', token('contributor'))).json().data.total).toBe(0);
      expect((await send('GET', `/requests/${created.id}`, token('contributor'))).statusCode).toBe(404);
    });

    it('a song the node did not upload is failed; a failed command keeps its doctor code', async () => {
      const contributor = token('contributor');
      const created = (await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS, variations: '2' })).json().data;
      const command = musicCommands()[0];
      await settle(command.id, 'succeeded', { result: { songs: [{ index: 1, sha256: '1'.repeat(64), size_bytes: 9, mime: 'audio/wav', duration_s: 60 }] } });
      const view = (await send('GET', `/requests/${created.id}`, contributor)).json().data;
      expect(view.songs.map((song: any) => song.status)).toEqual(['failed', 'ready']);

      const second = (await submit(contributor, { nodeId: 'ai-n2', lyrics: `${LYRICS} 2` })).json().data;
      await settle(musicCommands()[1].id, 'failed', { error: { code: 'LAI-SVC-151', message: 'Music is not loaded on this computer.' } });
      expect((await send('GET', `/requests/${second.id}`, contributor)).json().data).toMatchObject({
        status: 'failed',
        stage: 'failed',
        error: { code: 'LAI-SVC-151', message: 'Music is not loaded on this computer.' },
        songs: [{ status: 'failed' }],
      });
    });

    it('a read reconciles even when the settled event was lost (an expired command)', async () => {
      const contributor = token('contributor');
      const created = (await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS })).json().data;
      Object.assign(store.get(musicCommands()[0].id), { status: 'expired', finishedAt: clock.now() });
      expect((await send('GET', `/requests/${created.id}`, contributor)).json().data).toMatchObject({ status: 'expired', error: { code: null } });
    });

    it('cancel: a waiting request is cancelled (audited); a generating one is 409; someone else\'s is 403 for a contributor', async () => {
      const owner = registerMockUser(jwt, 'contributor');
      const created = (await submit(owner.accessToken, { nodeId: 'ai-n2', lyrics: LYRICS })).json().data;
      expect((await send('POST', `/requests/${created.id}/cancel`, token('contributor'))).statusCode).toBe(403);
      const cancelled = await send('POST', `/requests/${created.id}/cancel`, owner.accessToken);
      expect(cancelled.statusCode).toBe(200);
      expect(cancelled.json().data).toMatchObject({ status: 'cancelled', stage: 'cancelled' });
      expect(audits('localai.music.request_cancelled')).toHaveLength(1);

      const running = (await submit(owner.accessToken, { nodeId: 'ai-n2', lyrics: `${LYRICS} 2` })).json().data;
      Object.assign(store.get(musicCommands()[1].id), { status: 'running', claimedAt: clock.now() });
      const refused = await send('POST', `/requests/${running.id}/cancel`, owner.accessToken);
      expect([refused.statusCode, refused.json().details.status]).toEqual([409, 'running']);
    });

    it('a person lists their own requests (mine=false changes nothing); an org_admin lists all; a bad query is 400', async () => {
      const mine = registerMockUser(jwt, 'contributor');
      await submit(mine.accessToken, { nodeId: 'ai-n2', lyrics: LYRICS });
      await submit(token('contributor'), { nodeId: 'ai-n2', lyrics: LYRICS });
      expect((await send('GET', '/requests', mine.accessToken)).json().data.total).toBe(1);
      expect((await send('GET', '/requests?mine=false', mine.accessToken)).json().data.total).toBe(1);
      expect((await send('GET', '/requests', token('admin'))).json().data.total).toBe(2);
      expect((await send('GET', '/requests?mine=true', token('admin'))).json().data.total).toBe(0);
      expect(JSON.stringify((await send('GET', '/requests', mine.accessToken)).json())).not.toContain('Org Alpha sings');
      expect((await send('GET', '/requests?pageSize=500', mine.accessToken)).statusCode).toBe(400);
      expect((await send('GET', '/requests?debug=1', mine.accessToken)).statusCode).toBe(400);
    });
  });

  describe('audio and delete', () => {
    async function readySong(contributor: string) {
      const created = (await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS }, sample())).json().data;
      await settle(musicCommands().at(-1)!.id, 'succeeded', { result: { songs: [{ index: 0, sha256: 'c'.repeat(64), size_bytes: 10, mime: 'audio/mpeg', duration_s: 30 }] } });
      return { request: created, song: (await send('GET', `/requests/${created.id}`, contributor)).json().data.songs[0] };
    }

    it('GET /songs/:id/audio redirects to a 5-minute signed URL; ?download=1 asks for an attachment; ?as=json answers the URL', async () => {
      const contributor = token('contributor');
      const { request, song } = await readySong(contributor);
      const play = await send('GET', `/songs/${song.id}/audio`, contributor);
      expect(play.statusCode).toBe(302);
      expect(play.headers.location).toBe(`${SIGNED}localai-music/songs/${request.id}/0?X-Amz-Expires=300&X-Amz-Signature=get`);
      expect(play.headers['cache-control']).toBe('no-store');
      expect(objects.calls).toContain(`get localai-music/songs/${request.id}/0 300 inline`);
      await send('GET', `/songs/${song.id}/audio?download=1`, contributor);
      expect(objects.calls).toContain(`get localai-music/songs/${request.id}/0 300 attachment`);
      const json = await send('GET', `/songs/${song.id}/audio?as=json`, contributor);
      expect(json.json().data).toEqual({ url: expect.stringContaining(`songs/${request.id}/0`), expiresAt: '2026-11-14T12:05:00.000Z' });
      // Someone else's song has no audio for a contributor.
      expect((await send('GET', `/songs/${song.id}/audio`, token('contributor'))).statusCode).toBe(404);
    });

    it('a pending or unknown song has no audio (404)', async () => {
      const contributor = token('contributor');
      const created = (await submit(contributor, { nodeId: 'ai-n2', lyrics: LYRICS })).json().data;
      expect((await send('GET', `/songs/${created.songs[0].id}/audio`, contributor)).statusCode).toBe(404);
      expect((await send('GET', '/songs/not-a-uuid/audio', contributor)).statusCode).toBe(404);
    });

    it('DELETE needs the typed confirmation; it removes the object and the row; the sample stays while its prompt holds it; audited', async () => {
      const owner = registerMockUser(jwt, 'contributor');
      const { request, song } = await readySong(owner.accessToken);
      const admin = token('admin');
      expect((await send('DELETE', `/songs/${song.id}`, admin, {})).statusCode).toBe(400);
      expect((await send('DELETE', `/songs/${song.id}`, admin, { confirm: 'yes' })).statusCode).toBe(400);
      expect(music.songs).toHaveLength(1);

      const deleted = await send('DELETE', `/songs/${song.id}`, admin, { confirm: song.id });
      expect(deleted.statusCode).toBe(200);
      expect(deleted.json().data).toEqual({ id: song.id, deleted: true });
      expect(objects.calls).toContain(`delete localai-music/songs/${request.id}/0`);
      // The saved prompt still references the sample ("make again" reuses it), so the object stays.
      expect(objects.calls).not.toContain(`delete localai-music/samples/${request.id}.wav`);
      expect(music.songs).toHaveLength(0);
      expect(audits('localai.music.song_deleted')).toEqual([expect.objectContaining({ targetType: 'localai_music_song', targetId: song.id, meta: expect.objectContaining({ nodeId: 'ai-n2', requestId: request.id }) })]);
      expect((await send('GET', '/songs', admin)).json().data.total).toBe(0);

      // Deleting the prompt then deletes the sample: no request and no prompt references it any more.
      const promptId = request.promptId as string;
      expect((await send('DELETE', `/prompts/${promptId}`, owner.accessToken, { confirm: promptId })).statusCode).toBe(200);
      expect(objects.calls).toContain(`delete localai-music/samples/${request.id}.wav`);
    });
  });

  describe('the prompt history and titles (#568)', () => {
    const DESCRIPTION = 'a slow ballad about Person A and the sea, soft piano, warm voice';
    const form = (extra: Record<string, string> = {}) => ({ nodeId: 'ai-n2', description: DESCRIPTION, lyrics_mode: 'mine', lyrics: LYRICS, ...extra });
    const patch = (accessToken: string, url: string, payload: unknown) =>
      app.inject({ method: 'PATCH', url: `/api/localai/music${url}`, headers: auth(accessToken), payload: payload as never });

    it('every prompt and helper route needs a session and localai_music:use', async () => {
      const ID = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2';
      const routes: Array<['GET' | 'POST' | 'DELETE' | 'PATCH', string, unknown]> = [
        ['GET', '/prompts', undefined],
        ['POST', '/prompts', { description: DESCRIPTION }],
        ['GET', `/prompts/${ID}`, undefined],
        ['PATCH', `/prompts/${ID}`, { title: 'x' }],
        ['DELETE', `/prompts/${ID}`, { confirm: ID }],
        ['POST', `/prompts/${ID}/make`, { nodeId: 'ai-n2' }],
        ['GET', '/assist', undefined],
        ['POST', '/title-suggestion', { description: DESCRIPTION }],
        ['POST', '/lyrics-suggestion', { description: DESCRIPTION }],
      ];
      const viewer = token('viewer');
      for (const [method, url, payload] of routes) {
        const anonymous = await app.inject({ method, url: `/api/localai/music${url}`, payload: payload as never });
        const forbidden = await app.inject({ method, url: `/api/localai/music${url}`, headers: auth(viewer), payload: payload as never });
        expect([method, url, anonymous.statusCode, forbidden.statusCode]).toEqual([method, url, 401, 403]);
      }
      expect(music.prompts).toHaveLength(0);
      expect(ai.asked).toHaveLength(0);
    });

    it('making a song saves a prompt; the request keeps the title and settings it used when the prompt is edited later', async () => {
      const owner = registerMockUser(jwt, 'contributor');
      const created = (await submit(owner.accessToken, form({ title: 'Harbour Song', variations: '2' }), sample())).json().data;
      expect(created).toMatchObject({ title: 'Harbour Song', tags: DESCRIPTION, lyricsMode: 'mine', promptId: expect.any(String), variations: 2 });
      expect(music.songs.map((song) => song.title)).toEqual(['Harbour Song', 'Harbour Song']);

      const list = (await send('GET', '/prompts', owner.accessToken)).json().data;
      expect(list.total).toBe(1);
      expect(list.items[0]).toMatchObject({ id: created.promptId, title: 'Harbour Song', description: DESCRIPTION, lyricsMode: 'mine', hasSample: true, songCount: 0 });
      expect(list.items[0]).not.toHaveProperty('lyrics');
      const one = (await send('GET', `/prompts/${created.promptId}`, owner.accessToken)).json().data;
      expect(one).toMatchObject({ lyrics: LYRICS, variations: 2, durationS: 60, sampleMime: 'audio/wav' });
      expect(JSON.stringify(one)).not.toMatch(/localai-music\//);

      const edited = await patch(owner.accessToken, `/prompts/${created.promptId}`, { title: 'New Name', lyrics: '[verse]\nOther words', variations: 1 });
      expect(edited.statusCode).toBe(200);
      expect(edited.json().data).toMatchObject({ title: 'New Name', lyrics: '[verse]\nOther words', variations: 1 });
      const past = (await send('GET', `/requests/${created.id}`, owner.accessToken)).json().data;
      expect(past).toMatchObject({ title: 'Harbour Song', variations: 2, tags: DESCRIPTION });
      expect(past.lyrics).toBe(LYRICS);
      expect(music.songs.map((song) => song.title)).toEqual(['Harbour Song', 'Harbour Song']);

      // An empty title is generated again from the prompt's (new) lyrics.
      expect((await patch(owner.accessToken, `/prompts/${created.promptId}`, { title: '' })).json().data.title).toBe('Other Words');
      expect(audits('localai.music.prompt_created')).toEqual([expect.objectContaining({ targetType: 'localai_music_prompt', targetId: created.promptId })]);
      expect(JSON.stringify(audits('localai.music.prompt_updated'))).not.toMatch(/Other words|New Name|Person A|ballad/);
    });

    it('an empty title is generated: the first lyric line, else the description, else "Untitled song" in the page\'s language', async () => {
      const contributor = registerMockUser(jwt, 'contributor');
      expect((await submit(contributor.accessToken, form())).json().data.title).toBe('Person A Walks To The River At Dawn');
      const instrumental = (await submit(contributor.accessToken, form({ lyrics_mode: 'instrumental' }))).json().data;
      expect(instrumental.title).toBe('A Slow Ballad About Person A And The Sea');
      // An older client with only section tags and no description: the fallback, in Spanish for a Spanish page.
      const other = registerMockUser(jwt, 'contributor');
      expect((await submit(other.accessToken, { nodeId: 'ai-n2', lyrics: '[verse]', ui_language: 'es' })).json().data.title).toBe('Canción sin título');
    });

    it('"No vocals" sends [Instrumental] and keeps the typed lyrics on the prompt; a description is required; its limit is the contract\'s', async () => {
      const contributor = registerMockUser(jwt, 'contributor');
      const created = (await submit(contributor.accessToken, form({ lyrics_mode: 'instrumental', lyrics: 'for later' }))).json().data;
      const params = COMMANDS['localai.music.generate'].params.parse(musicCommands()[0].params);
      expect(params.lyrics).toBe('[Instrumental]');
      expect(params.tags).toBe(DESCRIPTION);
      expect((await send('GET', `/prompts/${created.promptId}`, contributor.accessToken)).json().data).toMatchObject({ lyricsMode: 'instrumental', lyrics: 'for later' });

      const refusal = async (fields: Record<string, string>, reason: string) => {
        const response = await submit(contributor.accessToken, fields);
        expect([response.statusCode, response.json().details.code, response.json().details.reason]).toEqual([400, 'LAI-SVC-154', reason]);
      };
      await refusal({ nodeId: 'ai-n2', lyrics_mode: 'mine', lyrics: LYRICS }, 'description_required');
      await refusal(form({ lyrics: '  ' }), 'lyrics_required');
      await refusal(form({ lyrics: '', lyrics_mode: 'ai' }), 'lyrics_required');
      await refusal(form({ tags: 'other' }), 'description_and_tags');
      await refusal(form({ description: 'x'.repeat(501) }), 'invalid_input');
      // 500 characters but over the 512 bytes the node takes as `tags`: refused before anything is queued.
      await refusal(form({ description: 'ñ'.repeat(300) }), 'invalid_input');
      expect(musicCommands()).toHaveLength(1);
      // At the limit, the command contract takes it (tags <= 512).
      expect((await submit(contributor.accessToken, form({ description: 'd'.repeat(500) }))).statusCode).toBe(201);
      expect(COMMANDS['localai.music.generate'].params.parse(musicCommands()[1].params).tags).toHaveLength(500);
    });

    it('make again: reuses the stored sample (no new upload), applies overrides, leaves the prompt as it is', async () => {
      const owner = registerMockUser(jwt, 'contributor');
      const bytes = wavBytes(9000);
      const first = (await submit(owner.accessToken, form({ title: 'Again' }), sample(bytes))).json().data;
      const puts = objects.calls.filter((call) => call.startsWith('put ')).length;

      const again = await send('POST', `/prompts/${first.promptId}/make`, owner.accessToken, { nodeId: 'ai-n2', overrides: { variations: 3, seed: 5 } });
      expect(again.statusCode).toBe(201);
      expect(again.json().data).toMatchObject({ title: 'Again', promptId: first.promptId, variations: 3, seed: 5, hasSample: true, sampleMode: 'sounds_like' });
      expect(objects.calls.filter((call) => call.startsWith('put ')).length).toBe(puts);
      const [, second] = musicCommands();
      const params = COMMANDS['localai.music.generate'].params.parse(second.params);
      expect(params).toMatchObject({ lyrics: LYRICS, tags: DESCRIPTION, variations: 3, seed: 5 });
      expect(params.sample?.sha256).toBe(createHash('sha256').update(bytes).digest('hex'));
      // The claim signs the FIRST request's stored sample for the second request.
      clock.advance(1000);
      const jobs = (await app.get(ClaimService).claim(NODE_ROW, { types: ['music.generate'], waitMs: 0, limit: 2 } as never)).jobs;
      const signed = jobs.map((job: any) => COMMANDS['localai.music.generate'].params.parse(job.params).sample?.url);
      expect(signed.every((url: string | undefined) => url?.includes(`localai-music/samples/${first.id}.wav`))).toBe(true);
      const prompt = (await send('GET', `/prompts/${first.promptId}`, owner.accessToken)).json().data;
      expect(prompt).toMatchObject({ variations: 1, seed: null });

      // Someone else's prompt cannot be made, read, changed or deleted (404); an admin may delete it but not read it.
      const stranger = token('contributor');
      expect((await send('POST', `/prompts/${first.promptId}/make`, stranger, { nodeId: 'ai-n2' })).statusCode).toBe(404);
      expect((await send('GET', `/prompts/${first.promptId}`, stranger)).statusCode).toBe(404);
      expect((await patch(stranger, `/prompts/${first.promptId}`, { title: 'Mine now' })).statusCode).toBe(404);
      expect((await send('DELETE', `/prompts/${first.promptId}`, stranger, { confirm: first.promptId })).statusCode).toBe(404);
      expect((await submit(stranger, form({ promptId: first.promptId }))).statusCode).toBe(404);
      expect((await send('GET', `/prompts/${first.promptId}`, token('admin'))).statusCode).toBe(404);
      expect((await send('GET', '/prompts', token('admin'))).json().data.total).toBe(0);
      expect((await send('POST', `/prompts/${first.promptId}/make`, owner.accessToken, { nodeId: 'ai-n2', overrides: { lyrics: 'x' } })).statusCode).toBe(400);
    });

    it('POST /requests with promptId saves the sent fields into that prompt and reuses or removes its sample', async () => {
      const owner = registerMockUser(jwt, 'contributor');
      const first = (await submit(owner.accessToken, form({ title: 'One' }), sample())).json().data;
      const second = (await submit(owner.accessToken, form({ promptId: first.promptId, title: 'Two', lyrics: '[verse]\nNew line' }))).json().data;
      expect(second).toMatchObject({ promptId: first.promptId, title: 'Two', hasSample: true });
      expect((await send('GET', `/prompts/${first.promptId}`, owner.accessToken)).json().data).toMatchObject({ title: 'Two', lyrics: '[verse]\nNew line', hasSample: true });
      expect((await send('GET', '/prompts', owner.accessToken)).json().data.total).toBe(1);

      const third = (await submit(owner.accessToken, form({ promptId: first.promptId, remove_sample: 'true' }))).json().data;
      expect(third.hasSample).toBe(false);
      expect((await send('GET', `/prompts/${first.promptId}`, owner.accessToken)).json().data.hasSample).toBe(false);
      // The two earlier requests still reference the sample, so its object stays.
      expect(objects.calls).not.toContain(`delete localai-music/samples/${first.id}.wav`);
    });

    it('save as new copies the sample reference; delete asks for the id, keeps the songs ("prompt deleted") and the shared sample', async () => {
      const owner = registerMockUser(jwt, 'contributor');
      const made = (await submit(owner.accessToken, form({ title: 'Original' }), sample())).json().data;
      await settle(musicCommands().at(-1)!.id, 'succeeded', { result: { songs: [{ index: 0, sha256: 'd'.repeat(64), size_bytes: 10, mime: 'audio/wav', duration_s: 30 }] } });
      expect((await send('GET', '/prompts', owner.accessToken)).json().data.items[0].songCount).toBe(1);

      const copy = await send('POST', '/prompts', owner.accessToken, { description: DESCRIPTION, lyrics: LYRICS, lyricsMode: 'mine', copySampleFrom: made.promptId, sampleMode: 'same_song' });
      expect(copy.statusCode).toBe(201);
      expect(copy.json().data).toMatchObject({ title: 'Person A Walks To The River At Dawn', hasSample: true, sampleMode: 'same_song', songCount: 0 });
      expect((await send('POST', '/prompts', token('contributor'), { description: DESCRIPTION, copySampleFrom: made.promptId })).statusCode).toBe(404);
      expect((await send('POST', '/prompts', owner.accessToken, { lyrics: LYRICS })).statusCode).toBe(400);
      expect((await send('POST', '/prompts', owner.accessToken, { description: DESCRIPTION, ownerId: 'x' })).statusCode).toBe(400);

      expect((await send('DELETE', `/prompts/${made.promptId}`, owner.accessToken, {})).statusCode).toBe(400);
      expect((await send('DELETE', `/prompts/${made.promptId}`, owner.accessToken, { confirm: 'yes' })).statusCode).toBe(400);
      const deleted = await send('DELETE', `/prompts/${made.promptId}`, owner.accessToken, { confirm: made.promptId });
      expect([deleted.statusCode, deleted.json().data]).toEqual([200, { id: made.promptId, deleted: true }]);
      const library = (await send('GET', '/songs', owner.accessToken)).json().data;
      expect(library.items).toEqual([expect.objectContaining({ requestId: made.id, title: 'Original', promptId: null })]);
      expect((await send('GET', `/requests/${made.id}`, owner.accessToken)).json().data).toMatchObject({ title: 'Original', promptId: null });
      expect(objects.calls).not.toContain(`delete localai-music/samples/${made.id}.wav`);
      const [row] = audits('localai.music.prompt_deleted');
      expect(row).toMatchObject({ targetType: 'localai_music_prompt', targetId: made.promptId, meta: { songsKept: 1 } });
      expect(JSON.stringify(row)).not.toMatch(/Original|ballad|Person A/);

      // An admin (localai_music:delete) may delete another person's prompt; the copy goes and, with it, the sample's
      // last prompt reference (the request still holds it, so the object stays).
      const copyId = copy.json().data.id as string;
      expect((await send('DELETE', `/prompts/${copyId}`, token('admin'), { confirm: copyId })).statusCode).toBe(200);
      expect(music.prompts).toHaveLength(0);
    });

    it('lists newest used first, paged; a bad query is 400', async () => {
      const owner = registerMockUser(jwt, 'contributor');
      for (const title of ['First', 'Second', 'Third']) {
        clock.advance(1000);
        await send('POST', '/prompts', owner.accessToken, { title, description: DESCRIPTION, lyricsMode: 'instrumental' });
      }
      clock.advance(1000);
      const first = music.prompts.find((row) => row.title === 'First')!;
      expect((await send('POST', `/prompts/${first.id}/make`, owner.accessToken, { nodeId: 'ai-n2' })).statusCode).toBe(201);
      const page = (await send('GET', '/prompts?pageSize=2', owner.accessToken)).json().data;
      expect([page.total, page.totalPages, page.items.map((item: any) => item.title)]).toEqual([3, 2, ['First', 'Third']]);
      expect((await send('GET', '/prompts?pageSize=500', owner.accessToken)).statusCode).toBe(400);
      expect((await send('GET', '/prompts?owner=x', owner.accessToken)).statusCode).toBe(400);

      // A draft saved without lyrics in "Use my lyrics" mode cannot be made until it has some.
      const draft = (await send('POST', '/prompts', owner.accessToken, { description: DESCRIPTION, lyricsMode: 'mine' })).json().data;
      const refused = await send('POST', `/prompts/${draft.id}/make`, owner.accessToken, { nodeId: 'ai-n2' });
      expect([refused.statusCode, refused.json().details.reason]).toEqual([400, 'lyrics_required']);
    });

    describe('Suggest a title and Write lyrics now', () => {
      const run = async <T>(fn: () => Promise<T>) => {
        const restoreLogger = enableNestLogging();
        const output = captureOutput();
        try {
          return { result: await fn(), logs: () => output.text() };
        } finally {
          output.restore();
          restoreLogger();
        }
      };
      const SECRET_LINE = 'Person B keeps a secret by the harbour';

      it('AI off: the generated title, source "generated"; lyrics are 409 with a reason and a fix; assist says off', async () => {
        ai.state = 'off';
        const contributor = token('contributor');
        expect((await send('GET', '/assist', contributor)).json().data).toEqual({ ai: { enabled: false, reason: 'ai_off', fix: 'Turn on AI in Settings → AI.' } });
        const title = await send('POST', '/title-suggestion', contributor, { lyrics: `[verse]\n${SECRET_LINE}`, description: DESCRIPTION });
        expect([title.statusCode, title.json().data]).toEqual([200, { title: 'Person B Keeps A Secret By The Harbour', source: 'generated' }]);
        expect((await send('POST', '/title-suggestion', contributor, { lyrics: 'ignored', lyricsMode: 'instrumental', description: 'calm piano, rain' })).json().data.title).toBe('Calm Piano');
        const lyrics = await send('POST', '/lyrics-suggestion', contributor, { description: DESCRIPTION, language: 'es' });
        expect([lyrics.statusCode, lyrics.json().details.reason]).toEqual([409, 'ai_off']);
        expect(lyrics.json().message).toMatch(/write your own lyrics/);
        expect(ai.asked).toHaveLength(0);
      });

      it('AI on: the model\'s title (source "ai") and lyrics, with no key, description or lyrics in a log line', async () => {
        ai.state = 'on';
        const contributor = token('contributor');
        expect((await send('GET', '/assist', contributor)).json().data).toEqual({ ai: { enabled: true, reason: null, fix: null } });
        ai.answer = '"Harbour Secrets"\nA title about...';
        const title = await run(() => send('POST', '/title-suggestion', contributor, { lyrics: `[verse]\n${SECRET_LINE}`, description: DESCRIPTION, language: 'es' }));
        expect(title.result.json().data).toEqual({ title: 'Harbour Secrets', source: 'ai' });
        expect(ai.asked[0].input).toContain(SECRET_LINE);
        expect(ai.asked[0].instructions).toContain('Spanish');

        ai.answer = '```\n[verse]\nThe sea is wide\n[chorus]\nSing it slow\n```';
        const lyrics = await run(() => send('POST', '/lyrics-suggestion', contributor, { description: DESCRIPTION, language: 'en', title: 'Harbour' }));
        expect([lyrics.result.statusCode, lyrics.result.json().data]).toEqual([200, { lyrics: '[verse]\nThe sea is wide\n[chorus]\nSing it slow', source: 'ai' }]);
        expect(ai.asked[1].input).toContain('Title: Harbour');
        for (const logs of [title.logs(), lyrics.logs()]) {
          expect(logs).toContain('music');
          expect(logs).not.toMatch(/Person B|harbour|Harbour|ballad|The sea is wide|Sing it slow/);
        }
        // Lyrics longer than the node takes are cut to 8 KiB, at a line.
        ai.answer = Array.from({ length: 2000 }, (_, i) => `line ${i}`).join('\n');
        const long = (await send('POST', '/lyrics-suggestion', contributor, { description: DESCRIPTION })).json().data.lyrics as string;
        expect(new TextEncoder().encode(long).length).toBeLessThanOrEqual(8192);
        expect(long.endsWith('\n')).toBe(false);
      });

      it('AI erroring: the title falls back to the generated one; lyrics are 502; nothing of the text is logged', async () => {
        ai.state = 'error';
        const contributor = token('contributor');
        const title = await run(() => send('POST', '/title-suggestion', contributor, { lyrics: `[verse]\n${SECRET_LINE}`, description: DESCRIPTION }));
        expect(title.result.json().data).toEqual({ title: 'Person B Keeps A Secret By The Harbour', source: 'generated' });
        const lyrics = await run(() => send('POST', '/lyrics-suggestion', contributor, { description: DESCRIPTION }));
        expect([lyrics.result.statusCode, lyrics.result.json().details.reason]).toEqual([502, 'ai_failed']);
        for (const logs of [title.logs(), lyrics.logs()]) {
          expect(logs).toContain('AI_PROVIDER_ERROR');
          expect(logs).not.toMatch(/Person B|harbour|ballad|provider said/);
        }
        // An empty answer is treated the same.
        ai.state = 'on';
        ai.answer = '   ';
        expect((await send('POST', '/title-suggestion', contributor, { description: DESCRIPTION })).json().data.source).toBe('generated');
        expect((await send('POST', '/lyrics-suggestion', contributor, { description: DESCRIPTION })).statusCode).toBe(502);
      });

      it('validates the bodies: a description is needed for lyrics; limits and unknown fields are 400', async () => {
        ai.state = 'on';
        const contributor = token('contributor');
        expect((await send('POST', '/lyrics-suggestion', contributor, { description: '  ' })).statusCode).toBe(400);
        expect((await send('POST', '/lyrics-suggestion', contributor, { description: 'x'.repeat(501) })).statusCode).toBe(400);
        expect((await send('POST', '/title-suggestion', contributor, { description: DESCRIPTION, model: 'gpt' })).statusCode).toBe(400);
        expect((await send('POST', '/title-suggestion', contributor, { lyrics: 'a'.repeat(8193) })).statusCode).toBe(400);
        expect(ai.asked).toHaveLength(0);
      });

      it('assist in every state (#570): AI off, then Use AI for music off, then no model, else enabled; each with its fix', async () => {
        const contributor = token('contributor');
        const states: Array<[typeof ai.state, boolean, unknown]> = [
          ['off', true, { enabled: false, reason: 'ai_off', fix: 'Turn on AI in Settings → AI.' }],
          // AI off wins over the music switch: the first thing to fix is named first.
          ['off', false, { enabled: false, reason: 'ai_off', fix: 'Turn on AI in Settings → AI.' }],
          ['on', false, { enabled: false, reason: 'music_ai_off', fix: 'Turn on Use AI for music in Settings → AI.' }],
          ['no_model', false, { enabled: false, reason: 'music_ai_off', fix: 'Turn on Use AI for music in Settings → AI.' }],
          ['no_model', true, { enabled: false, reason: 'no_model', fix: 'Add a provider and model in Settings → AI.' }],
          ['on', true, { enabled: true, reason: null, fix: null }],
        ];
        for (const [state, on, expected] of states) {
          ai.state = state;
          musicSwitch.on = on;
          const response = await send('GET', '/assist', contributor);
          expect([state, on, response.statusCode, response.json().data]).toEqual([state, on, 200, { ai: expected }]);
        }
      });

      it('Use AI for music off: no AI title, and Write lyrics now is 409 ai_music_off with its fix; nothing is asked', async () => {
        ai.state = 'on';
        musicSwitch.on = false;
        const contributor = token('contributor');
        expect((await send('POST', '/title-suggestion', contributor, { description: DESCRIPTION })).json().data.source).toBe('generated');
        const lyrics = await send('POST', '/lyrics-suggestion', contributor, { description: DESCRIPTION });
        expect([lyrics.statusCode, lyrics.json().details]).toEqual([409, { reason: 'ai_music_off', fix: 'Turn on Use AI for music in Settings → AI.' }]);
        ai.state = 'no_model';
        musicSwitch.on = true;
        const noModel = await send('POST', '/lyrics-suggestion', contributor, { description: DESCRIPTION });
        expect([noModel.statusCode, noModel.json().details]).toEqual([409, { reason: 'ai_no_model', fix: 'Add a provider and model in Settings → AI.' }]);
        expect(ai.asked).toHaveLength(0);
      });
    });

    describe('Automatic lyrics (#570)', () => {
      const auto = (extra: Record<string, string> = {}) => form({ lyrics_mode: 'auto', lyrics: '', ...extra });
      const WRITTEN = '[verse]\nThe tide comes in\n[chorus]\nHold the line';

      it('is the default of a saved prompt', async () => {
        const owner = token('contributor');
        expect((await send('POST', '/prompts', owner, { description: DESCRIPTION })).json().data.lyricsMode).toBe('auto');
      });

      it('music AI on: empty lyrics are written by the AI at Make song, sent to the node and saved on the prompt; no text in a log line', async () => {
        ai.state = 'on';
        ai.answer = WRITTEN;
        const owner = registerMockUser(jwt, 'contributor');
        const restoreLogger = enableNestLogging();
        const output = captureOutput();
        let response;
        try {
          response = await submit(owner.accessToken, auto({ language: 'es' }));
        } finally {
          output.restore();
          restoreLogger();
        }
        expect(response.statusCode).toBe(201);
        const created = response.json().data;
        expect(created).toMatchObject({ lyricsMode: 'auto' });
        expect(ai.asked).toHaveLength(1);
        expect(ai.asked[0].instructions).toContain('Spanish');
        expect(ai.callers[0]).toMatchObject({ userId: owner.id });
        expect(COMMANDS['localai.music.generate'].params.parse(musicCommands()[0].params).lyrics).toBe(WRITTEN);
        // The written lyrics are on the prompt, to read, edit and remake; the mode stays Automatic.
        expect((await send('GET', `/prompts/${created.promptId}`, owner.accessToken)).json().data).toMatchObject({ lyricsMode: 'auto', lyrics: WRITTEN });
        expect(audits('localai.music.requested')[0].meta).toMatchObject({ lyricsMode: 'auto', autoLyrics: 'written' });
        expect(output.text()).toContain('auto_lyrics=written');
        expect(output.text()).not.toMatch(/tide|Hold the line|Person A|ballad/);

        // Made again, the saved lyrics are used as they are: the AI is not asked again.
        const again = await send('POST', `/prompts/${created.promptId}/make`, owner.accessToken, { nodeId: 'ai-n2' });
        expect(again.statusCode).toBe(201);
        expect(ai.asked).toHaveLength(1);
        expect(COMMANDS['localai.music.generate'].params.parse(musicCommands()[1].params).lyrics).toBe(WRITTEN);
      });

      it('lyrics in the box are used as written, in Automatic mode too; the AI is not asked', async () => {
        ai.state = 'on';
        const owner = token('contributor');
        expect((await submit(owner, auto({ lyrics: LYRICS }))).statusCode).toBe(201);
        expect(COMMANDS['localai.music.generate'].params.parse(musicCommands()[0].params).lyrics).toBe(LYRICS);
        expect(ai.asked).toHaveLength(0);
      });

      it.each([
        ['AI off', 'off', true],
        ['Use AI for music off', 'on', false],
        ['no model', 'no_model', true],
        ['no key found at call time', 'gone', true],
      ] as const)('music AI unavailable (%s): empty lyrics mean no vocals; the prompt keeps no lyrics', async (_name, state, on) => {
        ai.state = state;
        musicSwitch.on = on;
        const owner = registerMockUser(jwt, 'contributor');
        const response = await submit(owner.accessToken, auto());
        expect(response.statusCode).toBe(201);
        expect(COMMANDS['localai.music.generate'].params.parse(musicCommands()[0].params).lyrics).toBe('[Instrumental]');
        expect((await send('GET', `/prompts/${response.json().data.promptId}`, owner.accessToken)).json().data).toMatchObject({ lyricsMode: 'auto', lyrics: '' });
        expect(audits('localai.music.requested')[0].meta).toMatchObject({ autoLyrics: 'instrumental' });
      });

      it('the AI failing is 502 before anything is stored: no prompt, no sample, no command', async () => {
        ai.state = 'error';
        const owner = token('contributor');
        const response = await submit(owner, auto(), sample());
        expect([response.statusCode, response.json().details.reason]).toEqual([502, 'ai_failed']);
        expect(response.json().message).toMatch(/No vocals/);
        expect(musicCommands()).toHaveLength(0);
        expect(music.prompts).toHaveLength(0);
        expect(objects.calls.filter((call) => call.startsWith('put '))).toEqual([]);
      });

      it('a saved Automatic prompt with no lyrics, made from My prompts: the AI writes them and they are saved on it', async () => {
        ai.state = 'on';
        ai.answer = WRITTEN;
        const owner = token('contributor');
        const draft = (await send('POST', '/prompts', owner, { description: DESCRIPTION })).json().data;
        const made = await send('POST', `/prompts/${draft.id}/make`, owner, { nodeId: 'ai-n2' });
        expect(made.statusCode).toBe(201);
        expect(COMMANDS['localai.music.generate'].params.parse(musicCommands()[0].params).lyrics).toBe(WRITTEN);
        expect((await send('GET', `/prompts/${draft.id}`, owner)).json().data).toMatchObject({ lyricsMode: 'auto', lyrics: WRITTEN });
      });
    });

    describe('the Use AI for music switch (#570)', () => {
      it('GET and PUT /ai-settings: ai_config:read and ai_config:write (an administrator), 401 without a session, 403 otherwise', async () => {
        for (const role of [null, 'viewer', 'contributor'] as const) {
          const accessToken = role ? token(role) : null;
          const expected = role ? 403 : 401;
          expect([role, (await send('GET', '/ai-settings', accessToken)).statusCode]).toEqual([role, expected]);
          expect([role, (await send('PUT', '/ai-settings', accessToken, { aiEnabled: true })).statusCode]).toEqual([role, expected]);
        }
        expect(musicSwitch.writes).toEqual([]);
        const admin = registerMockUser(jwt, 'admin');
        musicSwitch.on = false;
        expect((await send('GET', '/ai-settings', admin.accessToken)).json().data).toEqual({ aiEnabled: false });
        const put = await send('PUT', '/ai-settings', admin.accessToken, { aiEnabled: true });
        expect([put.statusCode, put.json().data]).toEqual([200, { aiEnabled: true }]);
        expect(musicSwitch.writes).toEqual([{ aiEnabled: true, actor: admin.id }]);
      });

      it('validates the body: a boolean, nothing else', async () => {
        const admin = token('admin');
        for (const body of [{}, { aiEnabled: 'yes' }, { aiEnabled: true, model: 'x' }]) {
          expect((await send('PUT', '/ai-settings', admin, body)).statusCode).toBe(400);
        }
        expect(musicSwitch.writes).toEqual([]);
      });
    });

    it('no lyrics or description reach a log line across save, edit, make again and delete', async () => {
      const restoreLogger = enableNestLogging();
      const output = captureOutput();
      try {
        const owner = registerMockUser(jwt, 'contributor');
        const made = (await submit(owner.accessToken, form({ title: 'Quiet Title' }), sample())).json().data;
        await patch(owner.accessToken, `/prompts/${made.promptId}`, { lyrics: '[verse]\nOrg Alpha hums', description: 'secret style words' });
        await send('POST', `/prompts/${made.promptId}/make`, owner.accessToken, { nodeId: 'ai-n2' });
        await send('DELETE', `/prompts/${made.promptId}`, owner.accessToken, { confirm: made.promptId });
      } finally {
        output.restore();
        restoreLogger();
      }
      expect(output.text()).toContain('queued');
      expect(output.text()).not.toMatch(/Person A|Org Alpha|ballad|secret style|Quiet Title|river/);
    });
  });

  describe('workloads and nodes', () => {
    it('POST /api/localai/nodes/:nodeId/workloads/:name/activate queues workload.activate (201, then 200 for the duplicate), audited twice', async () => {
      const contributor = token('contributor');
      const first = await activate(contributor, 'ai-n2', 'music');
      expect(first.statusCode).toBe(201);
      expect(first.json().data).toMatchObject({ id: expect.any(String), type: 'localai.workload.activate', params: { name: 'music' }, status: 'queued', nodeId: 'ai-n2' });
      expect((await activate(contributor, 'ai-n2', 'coding', { force: true })).statusCode).toBe(200);
      expect(audits('localai.command.requested')).toHaveLength(1);
      expect(audits('localai.workload.requested')).toEqual([expect.objectContaining({ targetType: 'localai_node', targetId: 'ai-n2', meta: expect.objectContaining({ name: 'music' }) })]);
    });

    it('a workload the node did not report is 422 LAI-LLM-050, one that does not fit 422 LAI-LLM-051, none reported 422; nothing is queued', async () => {
      const contributor = token('contributor');
      const unknown = await activate(contributor, 'ai-n2', 'speech');
      expect([unknown.statusCode, unknown.json().details.code]).toEqual([422, 'LAI-LLM-050']);
      const tooBig = await activate(contributor, 'ai-n2', 'video');
      expect([tooBig.statusCode, tooBig.json().details.code]).toEqual([422, 'LAI-LLM-051']);
      node = { ...node!, lastState: {} };
      const none = await activate(contributor, 'ai-n2', 'music');
      expect([none.statusCode, none.json().details.reason]).toEqual([422, 'workloads_not_reported']);
      expect(store.rows).toHaveLength(0);
    });

    it('a bad workload name, an unknown body field, an unknown node and a Survival node are refused', async () => {
      const contributor = token('contributor');
      expect((await activate(contributor, 'ai-n2', 'Music!')).statusCode).toBe(400);
      expect((await activate(contributor, 'ai-n2', 'music', { wait: true })).statusCode).toBe(400);
      expect((await activate(contributor, 'ai-n9', 'music')).statusCode).toBe(404);
      node = { ...node!, mode: 'survival' };
      const survival = await activate(contributor, 'ai-n2', 'music');
      expect([survival.statusCode, survival.json().details.code]).toEqual([422, 'LAI-CLD-034']);
    });

    it('GET /music/nodes: each enrolled node with its readiness and workloads from its snapshot (an older agent: none)', async () => {
      prismaMock.localaiNode.findMany.mockResolvedValue([
        { id: NODE_ROW, nodeId: 'ai-n2', displayName: 'Node ai-n2', phase: 'enrolled', mode: 'online', lastStateAt: new Date('2026-11-14T11:59:30.000Z'), lastState: { workloads: WORKLOADS } },
        { id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee6', nodeId: 'ai-n3', displayName: '', phase: 'enrolled', mode: 'offline', lastStateAt: null, lastState: null },
        { id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee7', nodeId: 'ai-n4', displayName: 'Node ai-n4', phase: 'enrolled', mode: 'online', lastStateAt: new Date('2026-11-14T11:59:30.000Z'), lastState: {} },
      ]);
      prismaMock.localaiNodeModel.findMany.mockResolvedValue([
        { nodeId: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee7', modelId: 'acestep-v15-turbo', present: true, loadable: true, loadableReason: null, status: 'loaded', roles: ['music'] },
      ]);
      prismaMock.localaiCommand.findMany.mockResolvedValue([{ nodeId: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee7', result: { active: 'music' }, finishedAt: new Date() }]);
      const response = await send('GET', '/nodes', token('contributor'));
      expect(response.statusCode).toBe(200);
      const [n2, n3, n4] = response.json().data;
      expect(n2).toMatchObject({ nodeId: 'ai-n2', readiness: 'can_load', activeWorkload: 'coding' });
      expect(n2.workloads.map((workload: any) => [workload.name, workload.fits])).toEqual([['coding', true], ['music', true], ['video', false]]);
      expect(n3).toMatchObject({ nodeId: 'ai-n3', nodeName: 'ai-n3', readiness: 'offline', workloads: null });
      expect(n4).toMatchObject({ nodeId: 'ai-n4', readiness: 'ready', musicModels: ['acestep-v15-turbo'], activeWorkload: 'music', workloads: null });
    });
  });
});
