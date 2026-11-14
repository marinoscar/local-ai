// Issue #515 (E11): the Lab allowlist's new (method, path) pairs: a node's workload switch, a music request, and the
// request and song reads. Exactly these pass; near misses are refused before any network call; the music request goes
// out as form fields, rebuilt.
import { describe, expect, it, vi } from 'vitest';
import { type CloudDeny, MUSIC_LIMITS, ROUTE_TABLE, createCloudProxy, decideCloudRequest } from '../../src/main/cloud-api';
import { createCloudHttp } from '../../src/main/cloud-link';

const UUID = '6b3c1f0e-4a2d-4c8b-9e51-0a7d2b9c3e44';
const get = (path: string) => ({ method: 'GET', path });
const post = (path: string, body?: unknown) => (body === undefined ? { method: 'POST', path } : { method: 'POST', path, body });
const refused = (raw: unknown, reason: CloudDeny): void => expect(decideCloudRequest(raw)).toEqual({ allow: false, reason });

const song = { nodeId: 'ai-n2', lyrics: 'Line one\nLine two', tags: 'folk, guitar', duration_s: 60, language: 'es', variations: 2 };

describe('the table holds exactly the pairs of the spec', () => {
  it('the seven DSK-11 pairs plus the six of issue #515, nothing else', () => {
    expect(ROUTE_TABLE).toEqual([
      'GET ^/api/localai/nodes$',
      'GET ^/api/localai/nodes/ai-n[1-9][0-9]{0,3}$',
      'GET ^/api/localai/alerts$',
      'GET ^/api/localai/commands/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$',
      'GET ^/api/localai/nodes/ai-n[1-9][0-9]{0,3}/commands$',
      'POST ^/api/localai/nodes/ai-n[1-9][0-9]{0,3}/commands$',
      'GET ^/api/auth/me$',
      'POST ^/api/localai/nodes/ai-n[1-9][0-9]{0,3}/workloads/[a-z][a-z0-9_-]{0,31}/activate$',
      'POST ^/api/localai/music/requests$',
      'GET ^/api/localai/music/requests$',
      'GET ^/api/localai/music/requests/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$',
      'GET ^/api/localai/music/songs$',
      'GET ^/api/localai/music/songs/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/audio$',
    ]);
  });
});

describe('what passes', () => {
  it.each([
    ['/api/localai/music/requests'],
    ['/api/localai/music/requests?page=2'],
    [`/api/localai/music/requests/${UUID}`],
    ['/api/localai/music/songs'],
    ['/api/localai/music/songs?page=1'],
    [`/api/localai/music/songs/${UUID}/audio`],
  ])('GET %s', (path) => {
    expect(decideCloudRequest(get(path))).toEqual({ allow: true, method: 'GET', path });
  });

  it.each([
    ['ai-n1', 'music', undefined, {}],
    ['ai-n2', 'coding', {}, {}],
    ['ai-n9999', 'speech', { force: true }, { force: true }],
    ['ai-n3', 'chat', { force: false }, { force: false }],
    ['ai-n3', 'my_workload-2', {}, {}],
  ])('POST activate %s/%s', (node, name, body, sent) => {
    const path = `/api/localai/nodes/${node}/workloads/${name}/activate`;
    expect(decideCloudRequest(post(path, body))).toEqual({ allow: true, method: 'POST', path, body: sent });
  });

  it('POST a music request: form fields, all text, rebuilt', () => {
    const decision = decideCloudRequest(post('/api/localai/music/requests', song));
    expect(decision).toEqual({
      allow: true,
      method: 'POST',
      path: '/api/localai/music/requests',
      form: { nodeId: 'ai-n2', lyrics: 'Line one\nLine two', tags: 'folk, guitar', duration_s: '60', language: 'es', variations: '2' },
    });
  });

  it('a seed is sent when given; null means none', () => {
    expect(decideCloudRequest(post('/api/localai/music/requests', { ...song, seed: 42 }))).toMatchObject({ allow: true, form: { seed: '42' } });
    const without = decideCloudRequest(post('/api/localai/music/requests', { ...song, seed: null }));
    expect(without.allow && 'form' in without && without.form !== undefined && 'seed' in without.form).toBe(false);
  });

  it('only tags, or only lyrics, is a request', () => {
    expect(decideCloudRequest(post('/api/localai/music/requests', { ...song, lyrics: '' })).allow).toBe(true);
    expect(decideCloudRequest(post('/api/localai/music/requests', { ...song, tags: '' })).allow).toBe(true);
  });

  it('the limits are the contract (lyrics 8 KiB, tags 512 B, 10 to 240 s, 1 to 4 variations)', () => {
    expect(MUSIC_LIMITS).toEqual({ lyricsBytes: 8192, tagsBytes: 512, minDuration: 10, maxDuration: 240, minVariations: 1, maxVariations: 4 });
    expect(decideCloudRequest(post('/api/localai/music/requests', { ...song, lyrics: 'a'.repeat(8192), tags: 't'.repeat(512), duration_s: 240, variations: 4 })).allow).toBe(true);
    expect(decideCloudRequest(post('/api/localai/music/requests', { ...song, duration_s: 10, variations: 1 })).allow).toBe(true);
  });
});

describe('near misses are refused', () => {
  it.each<[string, string]>([
    ['the music root', '/api/localai/music'],
    ['a request with a trailing slash', '/api/localai/music/requests/'],
    ['a request id that is not a uuid', '/api/localai/music/requests/12345'],
    ['an upper-case request id', `/api/localai/music/requests/${UUID.toUpperCase()}`],
    ['a request sub-path', `/api/localai/music/requests/${UUID}/cancel`],
    ['a song without audio', `/api/localai/music/songs/${UUID}`],
    ['a song id that is not a uuid', '/api/localai/music/songs/abc/audio'],
    ['a song audio with a suffix', `/api/localai/music/songs/${UUID}/audio/x`],
    ['the music settings', '/api/localai/music/settings'],
    ['a node workloads list', '/api/localai/nodes/ai-n1/workloads'],
    ['a workload without activate', '/api/localai/nodes/ai-n1/workloads/music'],
    ['traversal through the music routes', '/api/localai/music/../users'],
    ['traversal through a song', `/api/localai/music/songs/${UUID}/../../../users`],
    ['encoded traversal', '/api/localai/music/%2e%2e/users'],
    ['a double slash', '/api/localai//music/songs'],
    ['plural typo', '/api/localai/musics/songs'],
  ])('GET: %s', (_name, path) => refused(get(path), 'path'));

  it.each<[string, string]>([
    ['upper-case node', '/api/localai/nodes/AI-N1/workloads/music/activate'],
    ['node zero', '/api/localai/nodes/ai-n0/workloads/music/activate'],
    ['upper-case workload', '/api/localai/nodes/ai-n1/workloads/Music/activate'],
    ['a workload starting with a digit', '/api/localai/nodes/ai-n1/workloads/1music/activate'],
    ['a workload name that is too long', `/api/localai/nodes/ai-n1/workloads/${'a'.repeat(33)}/activate`],
    ['a dotted workload', '/api/localai/nodes/ai-n1/workloads/mu.sic/activate'],
    ['a deactivate', '/api/localai/nodes/ai-n1/workloads/music/deactivate'],
    ['activate with a trailing slash', '/api/localai/nodes/ai-n1/workloads/music/activate/'],
    ['traversal in the workload', '/api/localai/nodes/ai-n1/workloads/../commands/activate'],
  ])('POST activate: %s', (_name, path) => refused(post(path, {}), 'path'));

  it('the delete of a song is not the Lab\'s (DELETE /songs/:id is refused)', () => {
    refused({ method: 'DELETE', path: `/api/localai/music/songs/${UUID}` }, 'path');
    refused({ method: 'DELETE', path: `/api/localai/music/songs/${UUID}/audio` }, 'method');
  });

  it.each([
    ['GET', '/api/localai/nodes/ai-n1/workloads/music/activate', 'method'],
    ['PUT', '/api/localai/nodes/ai-n1/workloads/music/activate', 'method'],
    ['DELETE', '/api/localai/music/requests', 'method'],
    ['PUT', `/api/localai/music/requests/${UUID}`, 'method'],
    ['POST', `/api/localai/music/requests/${UUID}`, 'method'],
    ['POST', '/api/localai/music/songs', 'method'],
    ['POST', `/api/localai/music/songs/${UUID}/audio`, 'method'],
    ['post', '/api/localai/music/requests', 'method'],
  ] as const)('%s %s', (method, path, reason) => {
    // There is no GET route for activate, and no POST route for the reads: a wrong method is refused as such.
    refused({ method, path, ...(method === 'GET' ? {} : { body: {} }) }, reason);
  });

  it.each<[string, string]>([
    ['a page size', '/api/localai/music/songs?pageSize=100'],
    ['a node filter', '/api/localai/music/songs?nodeId=ai-n1'],
    ['page zero', '/api/localai/music/requests?page=0'],
    ['a repeated page', '/api/localai/music/requests?page=1&page=2'],
    ['a query on one request', `/api/localai/music/requests/${UUID}?x=1`],
    ['a query on the audio', `/api/localai/music/songs/${UUID}/audio?download=1`],
    ['a query on activate', '/api/localai/nodes/ai-n1/workloads/music/activate?force=1'],
    ['a query on the request POST', '/api/localai/music/requests?nodeId=ai-n1'],
  ])('the query: %s', (_name, path) => {
    const raw = path.includes('/activate') || (path.startsWith('/api/localai/music/requests?') && path.includes('nodeId')) ? post(path, path.includes('/activate') ? {} : song) : get(path);
    refused(raw, 'query');
  });

  it.each<[string, unknown]>([
    ['force as a string', { force: 'true' }],
    ['force as a number', { force: 1 }],
    ['an extra key', { force: true, wait: true }],
    ['a node id in the body', { nodeId: 'ai-n2' }],
    ['an array', [true]],
    ['null', null],
    ['a string', 'force'],
  ])('activate with %s', (_name, body) => refused(post('/api/localai/nodes/ai-n1/workloads/music/activate', body), 'body'));

  it.each<[string, unknown]>([
    ['no body', undefined],
    ['a string', 'lyrics'],
    ['an array', [song]],
    ['an unknown key (sample url)', { ...song, sample: { url: 'https://evil.example/x' } }],
    ['an unknown key (results)', { ...song, results: [] }],
    ['an unknown key (request_id)', { ...song, request_id: UUID }],
    ['no node', { ...song, nodeId: undefined }],
    ['a bad node', { ...song, nodeId: 'AI-N2' }],
    ['a node with a path', { ...song, nodeId: 'ai-n2/../x' }],
    ['lyrics that are not text', { ...song, lyrics: 7 }],
    ['lyrics over 8 KiB', { ...song, lyrics: 'a'.repeat(8193) }],
    ['lyrics over 8 KiB in bytes', { ...song, lyrics: 'ñ'.repeat(4097) }],
    ['tags over 512 B', { ...song, tags: 't'.repeat(513) }],
    ['neither lyrics nor tags', { ...song, lyrics: '  ', tags: '' }],
    ['a duration under 10 s', { ...song, duration_s: 9 }],
    ['a duration over 240 s', { ...song, duration_s: 241 }],
    ['a fractional duration', { ...song, duration_s: 60.5 }],
    ['a duration as text', { ...song, duration_s: '60' }],
    ['no language', { ...song, language: undefined }],
    ['a language with a slash', { ...song, language: 'es/../x' }],
    ['a language in upper case', { ...song, language: 'ES' }],
    ['zero variations', { ...song, variations: 0 }],
    ['five variations', { ...song, variations: 5 }],
    ['a negative seed', { ...song, seed: -1 }],
    ['a fractional seed', { ...song, seed: 1.5 }],
    ['a seed as text', { ...song, seed: '42' }],
    ['a class instance', Object.assign(Object.create({ evil: true }) as object, song)],
  ])('a music request with %s', (_name, body) => refused(post('/api/localai/music/requests', body), 'body'));
});

describe('the proxy and the transport', () => {
  it('the music request reaches the session as a form, never as a JSON body', async () => {
    const request = vi.fn(async () => ({ ok: true, status: 202, body: { data: { id: UUID, status: 'queued' } } }));
    const proxy = createCloudProxy({ session: { request }, cache: { save: vi.fn(), load: vi.fn(), clear: vi.fn() } as never, timers: { set: () => 1, clear: () => undefined } });
    const result = await proxy.request(post('/api/localai/music/requests', song));
    expect(result).toEqual({ ok: true, status: 202, body: { data: { id: UUID, status: 'queued' } } });
    const arg = (request.mock.calls[0] as unknown as [Record<string, unknown>])[0];
    expect(Object.keys(arg).sort()).toEqual(['form', 'method', 'path', 'signal']);
    expect(arg['form']).toMatchObject({ nodeId: 'ai-n2', duration_s: '60' });
  });

  it('a refused request never reaches the session', async () => {
    const request = vi.fn();
    const proxy = createCloudProxy({ session: { request }, cache: { save: vi.fn(), load: vi.fn(), clear: vi.fn() } as never });
    expect(await proxy.request(post('/api/localai/music/requests', { ...song, variations: 9 }))).toEqual({ ok: false, error: 'refused', reason: 'body' });
    expect(await proxy.request(get('/api/localai/nodes/ai-n1/workloads'))).toEqual({ ok: false, error: 'refused', reason: 'path' });
    expect(request).not.toHaveBeenCalled();
  });

  it('createCloudHttp sends a form as multipart/form-data with the bearer, and refuses a body plus a form', async () => {
    const fetch = vi.fn(async (_url: URL, _init: RequestInit) => new Response('{"data":{"id":"x"}}', { status: 202 }));
    const http = createCloudHttp({ fetch: fetch as never });
    const result = await http('/api/localai/music/requests', { method: 'POST', form: { nodeId: 'ai-n2', lyrics: 'la la' }, bearer: 'pat_test' });
    expect(result).toEqual({ status: 202, body: { data: { id: 'x' } } });
    const init = fetch.mock.calls[0]![1];
    const headers = init.headers as Record<string, string>;
    expect(headers['content-type']).toBeUndefined();
    expect(headers['authorization']).toBe('Bearer pat_test');
    expect(init.body).toBeInstanceOf(FormData);
    expect([...(init.body as FormData).entries()]).toEqual([
      ['nodeId', 'ai-n2'],
      ['lyrics', 'la la'],
    ]);
    expect(init.redirect).toBe('error');
    await expect(http('/api/localai/music/requests', { method: 'POST', body: {}, form: {} })).rejects.toThrow();
  });
});
