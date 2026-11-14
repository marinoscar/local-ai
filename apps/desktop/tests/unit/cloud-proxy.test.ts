// The Lab's cloud proxy (src/main/cloud-api.ts, docs/specs/desktop.md 4.8): the allowlist table with every deny path, and the
// proxy that sits on it. A refusal must never reach the session, and so never the network.
import { describe, expect, it, vi } from 'vitest';
import { COMMAND_TYPES, type CloudCaller, type CloudDeny, createCloudProxy, decideCloudRequest, labNodeUrl, REQUEST_TIMEOUT_MS, type TimerPort } from '../../src/main/cloud-api';
import { CloudRequestError, type CloudRequestFailure } from '../../src/main/cloud-link';
import type { CloudResponse } from '../../src/shared/types';

const UUID = '6b3c1f0e-4a2d-4c8b-9e51-0a7d2b9c3e44';
const get = (path: string, extra: Record<string, unknown> = {}) => ({ method: 'GET', path, ...extra });
const post = (path: string, body: unknown) => ({ method: 'POST', path, body });

describe('the allowlist: what passes', () => {
  it.each([
    ['/api/localai/nodes'],
    ['/api/localai/nodes/ai-n1'],
    ['/api/localai/nodes/ai-n2'],
    ['/api/localai/nodes/ai-n9999'],
    ['/api/localai/alerts'],
    ['/api/localai/alerts?state=active'],
    [`/api/localai/commands/${UUID}`],
    ['/api/localai/nodes/ai-n3/commands'],
    ['/api/localai/nodes/ai-n3/commands?status=queued'],
    ['/api/localai/nodes/ai-n3/commands?page=2'],
    ['/api/localai/nodes/ai-n3/commands?status=failed&page=12'],
    ['/api/localai/nodes/ai-n3/commands?page=1&status=running'],
    ['/api/auth/me'],
  ])('GET %s', (path) => {
    expect(decideCloudRequest(get(path))).toEqual({ allow: true, method: 'GET', path });
  });

  it.each(COMMAND_TYPES)('POST a %s command', (type) => {
    const decision = decideCloudRequest(post('/api/localai/nodes/ai-n2/commands', { type, params: { model_id: 'qwen3.6-35b-a3b-q4km' } }));
    expect(decision).toEqual({ allow: true, method: 'POST', path: '/api/localai/nodes/ai-n2/commands', body: { type, params: { model_id: 'qwen3.6-35b-a3b-q4km' } } });
  });

  it('a command without params gets an empty object', () => {
    expect(decideCloudRequest(post('/api/localai/nodes/ai-n2/commands', { type: 'status.refresh' }))).toMatchObject({ allow: true, body: { type: 'status.refresh', params: {} } });
  });

  it('the body that goes out is rebuilt: nothing is carried over by reference', () => {
    const params = { model_id: 'qwen3.6-35b-a3b-q4km', unload_others: true };
    const decision = decideCloudRequest(post('/api/localai/nodes/ai-n2/commands', { type: 'model.load', params }));
    expect(decision.allow && (decision.body as { params?: unknown } | undefined)?.params).toEqual(params);
    expect(decision.allow && (decision.body as { params?: unknown } | undefined)?.params).not.toBe(params);
  });
});

describe('the allowlist: every way to be refused', () => {
  const refused = (raw: unknown, reason: CloudDeny): void => expect(decideCloudRequest(raw)).toEqual({ allow: false, reason });

  it.each<[string, unknown, CloudDeny]>([
    ['not an object', 'GET /api/localai/nodes', 'bad_request'],
    ['null', null, 'bad_request'],
    ['an array', ['GET', '/api/localai/nodes'], 'bad_request'],
    ['no method', { path: '/api/localai/nodes' }, 'bad_request'],
    ['no path', { method: 'GET' }, 'bad_request'],
    ['a number for a path', { method: 'GET', path: 7 }, 'bad_request'],
    ['a headers key', get('/api/localai/nodes', { headers: { authorization: 'Bearer x' } }), 'bad_request'],
    ['a url key', get('/api/localai/nodes', { url: 'https://evil.example/' }), 'bad_request'],
    ['a signal key', get('/api/localai/nodes', { signal: {} }), 'bad_request'],
    ['a credentials key', get('/api/localai/nodes', { credentials: 'include' }), 'bad_request'],
  ])('%s', (_name, raw, reason) => refused(raw, reason));

  it.each<[string, string]>([
    ['traversal', '/api/localai/../users'],
    ['traversal at the end', '/api/localai/nodes/..'],
    ['traversal below a node', '/api/localai/nodes/ai-n1/../../users'],
    ['a dot segment', '/api/localai/./nodes'],
    ['encoded dots', '/api/localai/%2e%2e/users'],
    ['encoded dots upper case', '/api/localai/%2E%2E/users'],
    ['an encoded slash', '/api/localai%2fnodes'],
    ['a percent in an id', '/api/localai/nodes/ai-n%31'],
    ['a double slash', '/api/localai//nodes'],
    ['a double slash at the start', '//ai.marin.cr/api/localai/nodes'],
    ['a backslash', '/api/localai\\nodes'],
    ['an encoded backslash', '/api/localai/%5cnodes'],
    ['a fragment', '/api/localai/nodes#x'],
    ['a space', '/api/localai/nodes '],
    ['a newline', '/api/localai/nodes\n'],
    ['a NUL', '/api/localai/nodes\u0000'],
    ['a unicode look-alike', '/api/localai/nodes/ai-n１'],
    ['an absolute URL', 'https://ai.marin.cr/api/localai/nodes'],
    ['another host', 'https://evil.example/api/localai/nodes'],
    ['a relative path', 'api/localai/nodes'],
    ['an empty path', ''],
    ['the root', '/'],
    ['a path that is too long', `/api/localai/nodes/${'a'.repeat(300)}`],
    ['the users route', '/api/users'],
    ['the PAT route', `/api/pat/${UUID}`],
    ['the auth device route', '/api/auth/device/code'],
    ['an admin route of the lab', '/api/localai/nodes/resend-keys'],
    ['the settings route', '/api/localai/settings'],
    ['the gateway keys', '/api/localai/gateway/keys'],
    ['the catalog', '/api/localai/catalog/versions'],
    ['the audit log', '/api/localai/audit'],
    ['a trailing slash', '/api/localai/nodes/'],
    ['a longer prefix', '/api/localai/nodes-admin'],
    ['a deeper path', '/api/localai/nodes/ai-n1/key'],
    ['the commands collection', '/api/localai/commands'],
    ['an ack route', `/api/localai/alerts/${UUID}/ack`],
    ['upper-case node id', '/api/localai/nodes/AI-N3'],
    ['upper-case node id on commands', '/api/localai/nodes/AI-N3/commands'],
    ['node zero', '/api/localai/nodes/ai-n0'],
    ['a leading zero', '/api/localai/nodes/ai-n01'],
    ['five digits', '/api/localai/nodes/ai-n12345'],
    ['no digits', '/api/localai/nodes/ai-n'],
    ['a name that is not a node', '/api/localai/nodes/node-1'],
    ['a command id that is not a uuid', '/api/localai/commands/12345'],
    ['an upper-case uuid', `/api/localai/commands/${UUID.toUpperCase()}`],
    ['a uuid with a suffix', `/api/localai/commands/${UUID}x`],
    ['a uuid for a node', `/api/localai/nodes/${UUID}`],
  ])('the path: %s', (_name, path) => refused(get(path), 'path'));

  it.each<[string, string]>([
    ['a query on the node list', '/api/localai/nodes?reveal=tailnet'],
    ['the tailnet reveal on a node', '/api/localai/nodes/ai-n1?reveal=tailnet'],
    ['an empty query', '/api/localai/nodes?'],
    ['a query on /me', '/api/auth/me?x=1'],
    ['a query on a command', `/api/localai/commands/${UUID}?x=1`],
    ['alerts state cleared', '/api/localai/alerts?state=cleared'],
    ['alerts state all', '/api/localai/alerts?state=all'],
    ['alerts for a node', '/api/localai/alerts?nodeId=ai-n1'],
    ['alerts with a limit', '/api/localai/alerts?limit=5'],
    ['alerts with a second key', '/api/localai/alerts?state=active&limit=5'],
    ['alerts state repeated', '/api/localai/alerts?state=active&state=active'],
    ['alerts state empty', '/api/localai/alerts?state='],
    ['alerts state with no value', '/api/localai/alerts?state'],
    ['commands with pageSize', '/api/localai/nodes/ai-n1/commands?pageSize=100'],
    ['commands with an unknown status', '/api/localai/nodes/ai-n1/commands?status=deleted'],
    ['commands with an upper-case status', '/api/localai/nodes/ai-n1/commands?status=QUEUED'],
    ['commands with page zero', '/api/localai/nodes/ai-n1/commands?page=0'],
    ['commands with page -1', '/api/localai/nodes/ai-n1/commands?page=-1'],
    ['commands with page 1.5', '/api/localai/nodes/ai-n1/commands?page=1.5'],
    ['commands with page 100001', '/api/localai/nodes/ai-n1/commands?page=100001'],
    ['commands with page 1e3', '/api/localai/nodes/ai-n1/commands?page=1e3'],
    ['commands with a repeated page', '/api/localai/nodes/ai-n1/commands?page=1&page=2'],
    ['commands with a double ampersand', '/api/localai/nodes/ai-n1/commands?page=1&&status=queued'],
    ['commands with a trailing ampersand', '/api/localai/nodes/ai-n1/commands?page=1&'],
    ['commands with a second question mark', '/api/localai/nodes/ai-n1/commands?page=1?x=2'],
    ['commands with an equals inside a value', '/api/localai/nodes/ai-n1/commands?page=1=2'],
  ])('the query: %s', (_name, path) => refused(get(path), 'query'));

  it.each<[string, string]>([
    ['the node list', '/api/localai/nodes'],
    ['a node', '/api/localai/nodes/ai-n1'],
    ['alerts', '/api/localai/alerts'],
    ['a command', `/api/localai/commands/${UUID}`],
    ['/me', '/api/auth/me'],
  ])('POST is refused for %s, and so are PUT, PATCH and DELETE', (_name, path) => {
    for (const method of ['POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS', 'get', 'Get', '']) {
      refused({ method, path, body: { type: 'status.refresh' } }, 'method');
    }
  });

  it.each(['PUT', 'PATCH', 'DELETE', 'GET', 'HEAD', 'post', ''])('the command route takes POST only, not %s', (method) => {
    refused({ method, path: '/api/localai/nodes/ai-n1/commands', body: { type: 'status.refresh' } }, method === 'GET' ? 'body' : 'method');
  });

  it.each<[string, unknown, CloudDeny]>([
    ['a type outside the seven: model.delete', { type: 'model.delete', params: {} }, 'command_type'],
    ['a type outside the seven: mode.offline', { type: 'mode.offline' }, 'command_type'],
    ['a type outside the seven: key.rotate', { type: 'key.rotate' }, 'command_type'],
    ['a type outside the seven: agent.update', { type: 'agent.update' }, 'command_type'],
    ['a type outside the seven: logs.tail', { type: 'logs.tail' }, 'command_type'],
    ['a type outside the seven: model.download', { type: 'model.download' }, 'command_type'],
    ['a type outside the seven: catalog.refresh', { type: 'catalog.refresh' }, 'command_type'],
    ['the prefixed form', { type: 'localai.model.load' }, 'command_type'],
    ['an upper-case type', { type: 'Model.Load' }, 'command_type'],
    ['a type with a trailing space', { type: 'model.load ' }, 'command_type'],
    ['an empty type', { type: '' }, 'command_type'],
    ['a type that is not a string', { type: 7 }, 'command_type'],
    ['a type that is an array', { type: ['model.load'] }, 'command_type'],
    ['no type', { params: {} }, 'command_type'],
    ['an extra key', { type: 'model.load', params: {}, nodeId: 'ai-n9' }, 'body'],
    ['a prototype key', JSON.parse('{"type":"model.load","__proto__":{"x":1}}') as unknown, 'body'],
    ['params that are an array', { type: 'model.load', params: [] }, 'body'],
    ['params that are a string', { type: 'model.load', params: 'qwen' }, 'body'],
    ['params that are null', { type: 'model.load', params: null }, 'body'],
    ['params that are too big', { type: 'model.load', params: { model_id: 'x'.repeat(5000) } }, 'body'],
    ['a body that is a string', 'status.refresh', 'body'],
    ['a body that is null', null, 'body'],
    ['a body that is an array', [{ type: 'status.refresh' }], 'body'],
  ])('a command POST with %s', (_name, body, reason) => refused(post('/api/localai/nodes/ai-n1/commands', body), reason));

  it('params that are a class instance or have a cycle are refused', () => {
    class Params {
      model_id = 'x';
    }
    refused(post('/api/localai/nodes/ai-n1/commands', { type: 'model.load', params: new Params() }), 'body');
    const cyclic: Record<string, unknown> = {};
    cyclic['self'] = cyclic;
    refused(post('/api/localai/nodes/ai-n1/commands', { type: 'model.load', params: cyclic }), 'body');
  });

  it('a body on a GET is refused', () => {
    refused({ method: 'GET', path: '/api/localai/nodes', body: {} }, 'body');
  });

  it('a command POST with a query string is refused', () => {
    refused(post('/api/localai/nodes/ai-n1/commands?status=queued', { type: 'status.refresh' }), 'query');
  });

  it('the command route for a node that is not a node is refused as a path', () => {
    refused(post('/api/localai/nodes/AI-N3/commands', { type: 'status.refresh' }), 'path');
    refused(post('/api/localai/nodes/../commands', { type: 'status.refresh' }), 'path');
  });
});

describe('labNodeUrl', () => {
  it('builds the cloud address of a valid node id only', () => {
    expect(labNodeUrl('ai-n2')).toBe('https://ai.marin.cr/lab/nodes/ai-n2');
    for (const bad of ['AI-N2', 'ai-n0', 'ai-n2/../x', 'ai-n2?x=1', '../ai-n2', '', 7, null, undefined, { toString: () => 'ai-n2' }]) expect(labNodeUrl(bad), String(bad)).toBeNull();
  });
});

// ---------------------------------------------------------------------------------------------------------------------
// The proxy
// ---------------------------------------------------------------------------------------------------------------------

function fakeTimers() {
  let next = 1;
  const pending = new Map<number, { fn: () => void; ms: number }>();
  const port: TimerPort = {
    set: (fn, ms) => {
      pending.set(next, { fn, ms });
      return next++;
    },
    clear: (handle) => void pending.delete(handle as number),
  };
  return { port, pending, fire: () => [...pending.values()].forEach((t) => t.fn()) };
}

const okResponse = (body: unknown, status = 200): CloudResponse => ({ ok: status >= 200 && status < 300, status, body });

function setup(request: CloudCaller['request']) {
  const session = { request: vi.fn(request) };
  const cache = { save: vi.fn(async () => true), load: vi.fn(async () => null), clear: vi.fn(async () => undefined) };
  const timers = fakeTimers();
  const proxy = createCloudProxy({ session, cache, timers: timers.port });
  return { proxy, session, cache, timers };
}

describe('the proxy', () => {
  it('forwards an allowed request with the normalised method and path, a rebuilt body and a signal, and nothing else', async () => {
    const { proxy, session } = setup(async () => okResponse({ data: {} }, 201));
    const result = await proxy.request(post('/api/localai/nodes/ai-n2/commands', { type: 'model.load', params: { model_id: 'qwen3.6-35b-a3b-q4km' } }));
    expect(result).toEqual({ ok: true, status: 201, body: { data: {} } });
    expect(session.request).toHaveBeenCalledTimes(1);
    const arg = session.request.mock.calls[0]![0];
    expect(Object.keys(arg).sort()).toEqual(['body', 'method', 'path', 'signal']);
    expect(arg).toMatchObject({ method: 'POST', path: '/api/localai/nodes/ai-n2/commands', body: { type: 'model.load', params: { model_id: 'qwen3.6-35b-a3b-q4km' } } });
    expect(arg.signal).toBeInstanceOf(AbortSignal);
  });

  it.each([
    ['a traversal', get('/api/localai/../users')],
    ['an upper-case node', get('/api/localai/nodes/AI-N3')],
    ['a type outside the seven', post('/api/localai/nodes/ai-n1/commands', { type: 'model.delete', params: {} })],
    ['a DELETE', { method: 'DELETE', path: `/api/localai/commands/${UUID}` }],
    ['a header', get('/api/localai/nodes', { headers: { 'x-evil': '1' } })],
    ['junk', 42],
  ])('refuses %s without touching the session (so without a network call)', async (_name, raw) => {
    const { proxy, session, cache, timers } = setup(async () => okResponse({}));
    const result = await proxy.request(raw);
    expect(result).toMatchObject({ ok: false, error: 'refused' });
    expect(typeof (result as { reason?: unknown }).reason).toBe('string');
    expect(session.request).not.toHaveBeenCalled();
    expect(cache.save).not.toHaveBeenCalled();
    expect(timers.pending.size).toBe(0);
  });

  it('the refusal carries the reason and never the text the page sent', async () => {
    const { proxy } = setup(async () => okResponse({}));
    const result = await proxy.request(get('/api/localai/../users-secret-marker'));
    expect(JSON.stringify(result)).not.toContain('secret-marker');
  });

  it('gives up after 10 s: the signal aborts and the page gets "timeout"', async () => {
    const { proxy, timers, session } = setup(
      (req) =>
        new Promise((_resolve, reject) => {
          req.signal!.addEventListener('abort', () => reject(new CloudRequestError('aborted')));
        }),
    );
    const pending = proxy.request(get('/api/localai/nodes'));
    expect([...timers.pending.values()].map((t) => t.ms)).toEqual([REQUEST_TIMEOUT_MS]);
    expect(REQUEST_TIMEOUT_MS).toBe(10_000);
    timers.fire();
    expect(await pending).toEqual({ ok: false, error: 'timeout' });
    expect(session.request.mock.calls[0]![0].signal!.aborted).toBe(true);
    expect(timers.pending.size).toBe(0);
  });

  it.each<[CloudRequestFailure, string]>([
    ['offline', 'offline'],
    ['aborted', 'aborted'],
    ['unreachable', 'unreachable'],
    ['unauthorized', 'unauthorized'],
    ['bad_request', 'refused'],
  ])('a session failure %s reaches the page as %s, and the timer is cleared', async (kind, expected) => {
    const { proxy, timers } = setup(async () => {
      throw new CloudRequestError(kind);
    });
    expect(await proxy.request(get('/api/localai/nodes'))).toMatchObject({ ok: false, error: expected });
    expect(timers.pending.size).toBe(0);
  });

  it('an error that is not a CloudRequestError is "unreachable"', async () => {
    const { proxy } = setup(async () => {
      throw new TypeError('fetch failed');
    });
    expect(await proxy.request(get('/api/localai/nodes'))).toEqual({ ok: false, error: 'unreachable' });
  });

  it('a 4xx or 5xx answer is passed through as an answer (the page decides what it means)', async () => {
    for (const status of [403, 409, 422, 503]) {
      const { proxy } = setup(async () => okResponse({ message: 'no' }, status));
      expect(await proxy.request(get('/api/localai/nodes/ai-n1'))).toEqual({ ok: false, status, body: { message: 'no' } });
    }
  });

  it('caches a good node list, and only that', async () => {
    const body = { data: { summary: {}, nodes: [] } };
    const { proxy, cache } = setup(async () => okResponse(body));
    await proxy.request(get('/api/localai/nodes'));
    expect(cache.save).toHaveBeenCalledWith(body);
    await proxy.request(get('/api/localai/nodes/ai-n1'));
    await proxy.request(get('/api/localai/alerts'));
    await proxy.request(get('/api/auth/me'));
    expect(cache.save).toHaveBeenCalledTimes(1);
  });

  it('does not cache a failed or non-2xx node list, and a cache that throws does not fail the request', async () => {
    const bad = setup(async () => okResponse({ message: 'down' }, 503));
    await bad.proxy.request(get('/api/localai/nodes'));
    expect(bad.cache.save).not.toHaveBeenCalled();

    const throwing = setup(async () => okResponse({ data: { summary: {}, nodes: [] } }));
    throwing.cache.save.mockRejectedValueOnce(new Error('disk full'));
    expect(await throwing.proxy.request(get('/api/localai/nodes'))).toMatchObject({ ok: true, status: 200 });
  });

  it('reads and clears the cache through the store', async () => {
    const { proxy, cache } = setup(async () => okResponse({}));
    await proxy.cached();
    await proxy.clearCache();
    expect(cache.load).toHaveBeenCalledTimes(1);
    expect(cache.clear).toHaveBeenCalledTimes(1);
  });
});
