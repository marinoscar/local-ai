// The music routes over HTTP (E11, #513) on the whole AppModule over a mocked database: the real guards with the real
// seeded grants, the real multipart reader with the app's plugin, the real controller, service, command plane, audit
// sink and event bus; the in-memory command and music tables; a fake object store. This proves the RBAC matrix, the
// multipart limits with their doctor codes, the enqueue (params, signed URLs, sha256), the status transitions from the
// command, the library, the audio redirect, the delete confirmation, the audit rows and that no lyrics reach a log
// line. The SQL itself is `music.db.spec.ts`.
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
    prismaMock.auditEvent.create.mockResolvedValue({});
    prismaMock.auditEvent.findFirst.mockResolvedValue(null);
  });

  const token = (role: FixtureRole) => registerMockUser(jwt, role).accessToken;
  const auth = (accessToken: string | null) => (accessToken ? { authorization: `Bearer ${accessToken}` } : {});
  const send = (method: 'GET' | 'POST' | 'DELETE', url: string, accessToken: string | null, payload?: unknown) =>
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
        title: 'Person A walks to the river at dawn',
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
      expect(library.items[0]).toMatchObject({ nodeId: 'ai-n2', title: 'Person A walks to the river at dawn', status: 'ready', requestedByMe: false });
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

    it('DELETE needs the typed confirmation; it removes the object, the row and, for the last song, the sample; audited', async () => {
      const { request, song } = await readySong(token('contributor'));
      const admin = token('admin');
      expect((await send('DELETE', `/songs/${song.id}`, admin, {})).statusCode).toBe(400);
      expect((await send('DELETE', `/songs/${song.id}`, admin, { confirm: 'yes' })).statusCode).toBe(400);
      expect(music.songs).toHaveLength(1);

      const deleted = await send('DELETE', `/songs/${song.id}`, admin, { confirm: song.id });
      expect(deleted.statusCode).toBe(200);
      expect(deleted.json().data).toEqual({ id: song.id, deleted: true });
      expect(objects.calls).toEqual(expect.arrayContaining([`delete localai-music/songs/${request.id}/0`, `delete localai-music/samples/${request.id}.wav`]));
      expect(music.songs).toHaveLength(0);
      expect(audits('localai.music.song_deleted')).toEqual([expect.objectContaining({ targetType: 'localai_music_song', targetId: song.id, meta: expect.objectContaining({ nodeId: 'ai-n2', requestId: request.id }) })]);
      expect((await send('GET', '/songs', admin)).json().data.total).toBe(0);
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
