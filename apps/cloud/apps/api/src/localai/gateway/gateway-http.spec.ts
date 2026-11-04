// The refusals and the pipeline over a real Fastify instance (inject): exact status, JSON and headers for every
// authentication and validation row of spec section 3.3 (001, 002, 004, 010, 011, 013, 024, 051, 052), the request id,
// CORS, and the logging rules of section 6.
import { randomBytes } from 'node:crypto';
import Fastify from 'fastify';

import { GatewayError } from './errors';
import { buildGateway, makeKey, makeToken, type TestGateway } from './gateway.testkit';
import { GATEWAY_JSON_BODY_LIMIT } from './body';
import { registerGatewayRoutesOn } from './routes';
import { hashGatewayToken } from './token-verify';

const JSON_HEADERS = { 'content-type': 'application/json' };

describe('gateway pipeline over HTTP', () => {
  let gw: TestGateway;
  afterEach(async () => {
    await gw.app.close();
  });

  const call = (method: 'GET' | 'POST' | 'OPTIONS' | 'PUT', path: string, options: { token?: string | null; payload?: unknown; headers?: Record<string, string> } = {}) => {
    const token = options.token === undefined ? gw.token : options.token;
    const payload = options.payload === undefined ? undefined : typeof options.payload === 'string' ? options.payload : JSON.stringify(options.payload);
    return gw.app.inject({
      method,
      url: `${gw.prefix}${path}`,
      headers: { ...(payload !== undefined ? JSON_HEADERS : {}), ...(token ? { authorization: `Bearer ${token}` } : {}), ...options.headers },
      payload,
    });
  };

  const expectError = (response: { statusCode: number; json: () => unknown; headers: Record<string, unknown> }, status: number, type: string, code: string, localai: string) => {
    expect(response.statusCode).toBe(status);
    const error = (response.json() as { error: Record<string, unknown> }).error;
    expect(error).toMatchObject({ type, code, localai_code: localai });
    expect([null, 'model']).toContain(error.param);
    expect(typeof error.message).toBe('string');
    expect(error.request_id).toBe(response.headers['x-request-id']);
    expect(String(response.headers['content-type'])).toContain('application/json');
    return error;
  };

  describe('index and request id', () => {
    it('GET /v1/ answers the index without a key, with and without the trailing slash, and echoes a request id', async () => {
      gw = await buildGateway();
      for (const path of ['/', '']) {
        const response = await call('GET', path, { token: null });
        expect(response.statusCode).toBe(200);
        expect(response.json()).toEqual({
          object: 'localai.gateway',
          endpoints: expect.arrayContaining([{ method: 'POST', path: '/v1/chat/completions' }]),
          docs: 'https://github.com/marinoscar/local-ai/blob/main/docs/specs/gateway.md',
        });
        expect(response.headers['x-request-id']).toMatch(/^[0-9a-f]{32}$/);
      }
    });

    it('keeps the id nginx sends and puts it in the error body and the header', async () => {
      gw = await buildGateway();
      const response = await call('GET', '/models', { token: null, headers: { 'x-request-id': '4f1c0a9e5b7d4a2e8c3d1f6a9b0e2c47' } });
      expect(response.headers['x-request-id']).toBe('4f1c0a9e5b7d4a2e8c3d1f6a9b0e2c47');
      expect(response.json().error.request_id).toBe('4f1c0a9e5b7d4a2e8c3d1f6a9b0e2c47');
    });

    it('replaces a malformed id', async () => {
      gw = await buildGateway();
      const response = await call('GET', '/', { headers: { 'x-request-id': 'bad id' } });
      expect(response.headers['x-request-id']).toMatch(/^[0-9a-f]{32}$/);
    });
  });

  describe('authentication (LAI-GW-010, 011, 013)', () => {
    it.each<[string, string | null]>([
      ['no Authorization header', null],
      ['a malformed key', 'lai_short'],
      ['an unknown key', `lai_${randomBytes(32).toString('base64url')}`],
    ])('%s -> 401 LAI-GW-010', async (_label, token) => {
      gw = await buildGateway();
      const response = await call('GET', '/models', { token });
      expectError(response, 401, 'authentication_error', 'invalid_api_key', 'LAI-GW-010');
      expect(response.headers['retry-after']).toBeUndefined();
    });

    it('a revoked key -> 401, an expired key -> 401', async () => {
      gw = await buildGateway();
      const revoked = makeToken();
      const expired = makeToken();
      gw.store.add(makeKey(revoked, { revokedAt: new Date() }));
      gw.store.add(makeKey(expired, { expiresAt: new Date(Date.now() - 1000) }));
      expectError(await call('GET', '/models', { token: revoked }), 401, 'authentication_error', 'invalid_api_key', 'LAI-GW-010');
      expectError(await call('GET', '/models', { token: expired }), 401, 'authentication_error', 'invalid_api_key', 'LAI-GW-010');
    });

    it('a deactivated owner -> 403 LAI-GW-013', async () => {
      gw = await buildGateway();
      gw.owners.set('user-a', { active: false, permissions: ['gateway:use'] });
      expectError(await call('GET', '/models'), 403, 'permission_error', 'key_owner_not_allowed', 'LAI-GW-013');
    });

    it('an owner without gateway:use -> 403 LAI-GW-013', async () => {
      gw = await buildGateway();
      gw.owners.set('user-a', { active: true, permissions: [] });
      expectError(await call('POST', '/chat/completions', { payload: { model: 'm' } }), 403, 'permission_error', 'key_owner_not_allowed', 'LAI-GW-013');
    });

    it('30 wrong keys from one X-Real-IP, then 429 LAI-GW-011 with Retry-After (a valid key included); another address is unaffected', async () => {
      gw = await buildGateway();
      const ip = { 'x-real-ip': '203.0.113.50' };
      for (let i = 0; i < 30; i += 1) expect((await call('GET', '/models', { token: 'lai_nope', headers: ip })).statusCode).toBe(401);
      const blocked = await call('GET', '/models', { headers: ip });
      expectError(blocked, 429, 'rate_limit_error', 'auth_rate_limited', 'LAI-GW-011');
      expect(Number(blocked.headers['retry-after'])).toBeGreaterThan(290);
      expect((await call('GET', '/models', { headers: { 'x-real-ip': '203.0.113.51' } })).statusCode).toBe(503); // authenticated; stub
    });

    it('a malformed key causes zero database queries over HTTP', async () => {
      gw = await buildGateway();
      await call('GET', '/models', { token: 'lai_malformed' });
      await call('POST', '/chat/completions', { token: 'x'.repeat(60), payload: { model: 'm' } });
      expect(gw.store.lookups).toBe(0);
      expect(gw.owners.reads).toBe(0);
    });

    it('a revoked key is refused within 10 s and at once after evict()', async () => {
      gw = await buildGateway();
      expect((await call('GET', '/models')).statusCode).toBe(503); // authenticated, stub
      gw.store.keys.get(hashGatewayToken(gw.token))!.revokedAt = new Date();
      expect((await call('GET', '/models')).statusCode).toBe(503); // still cached
      gw.authenticator.evict(hashGatewayToken(gw.token));
      expectError(await call('GET', '/models'), 401, 'authentication_error', 'invalid_api_key', 'LAI-GW-010');
    });

    it('does not read a body before authenticating: an anonymous oversized post is 401, not 413', async () => {
      gw = await buildGateway();
      const response = await call('POST', '/chat/completions', { token: null, payload: JSON.stringify({ model: 'm', pad: 'x'.repeat(GATEWAY_JSON_BODY_LIMIT + 10) }) });
      expectError(response, 401, 'authentication_error', 'invalid_api_key', 'LAI-GW-010');
    });
  });

  describe('body validation (LAI-GW-001, 002, 004)', () => {
    it.each([
      ['malformed JSON', '{"model": '],
      ['an empty body', ''],
      ['a JSON array', '[1,2]'],
      ['a JSON string', '"hello"'],
      ['null', 'null'],
    ])('%s -> 400 LAI-GW-001', async (_label, payload) => {
      gw = await buildGateway();
      expectError(await call('POST', '/chat/completions', { payload }), 400, 'invalid_request_error', 'invalid_json', 'LAI-GW-001');
    });

    it('a non-JSON content type is the same 400 (never a 415)', async () => {
      gw = await buildGateway();
      const response = await gw.app.inject({
        method: 'POST',
        url: `${gw.prefix}/completions`,
        headers: { authorization: `Bearer ${gw.token}`, 'content-type': 'text/plain' },
        payload: 'hello',
      });
      expectError(response, 400, 'invalid_request_error', 'invalid_json', 'LAI-GW-001');
    });

    it.each([{}, { model: 3 }, { model: null }, { model: '' }, { messages: [] }])('%j -> 400 LAI-GW-002 with param model', async (payload) => {
      gw = await buildGateway();
      const error = expectError(await call('POST', '/embeddings', { payload }), 400, 'invalid_request_error', 'missing_model', 'LAI-GW-002');
      expect(error.param).toBe('model');
    });

    it('a body over 8 MiB -> 413 LAI-GW-004', async () => {
      gw = await buildGateway();
      const response = await call('POST', '/chat/completions', { payload: JSON.stringify({ model: 'm', pad: 'x'.repeat(GATEWAY_JSON_BODY_LIMIT) }) });
      expectError(response, 413, 'invalid_request_error', 'request_too_large', 'LAI-GW-004');
    });

    it('a body just under 8 MiB passes the limit (reaches the handler: 503 stub)', async () => {
      gw = await buildGateway();
      const response = await call('POST', '/chat/completions', { payload: JSON.stringify({ model: 'm', pad: 'x'.repeat(GATEWAY_JSON_BODY_LIMIT - 1024) }) });
      expect(response.statusCode).toBe(503);
    });

    it('a valid body reaches the stub, which answers 503 gateway_unavailable', async () => {
      gw = await buildGateway();
      const error = expectError(await call('POST', '/chat/completions', { payload: { model: 'm', messages: [] } }), 503, 'server_error', 'gateway_unavailable', 'LAI-GW-051');
      expect(error.message).toContain('not available yet');
    });

    it('does not parse a body for the transcription route (multipart is its own parser)', async () => {
      gw = await buildGateway();
      const response = await gw.app.inject({
        method: 'POST',
        url: `${gw.prefix}/audio/transcriptions`,
        headers: { authorization: `Bearer ${gw.token}`, 'content-type': 'multipart/form-data; boundary=x' },
        payload: '--x--',
      });
      expect(response.statusCode).toBe(503);
    });
  });

  describe('endpoints not served (LAI-GW-024)', () => {
    it.each<['GET' | 'POST' | 'PUT', string]>([
      ['POST', '/audio/speech'],
      ['POST', '/images/generations'],
      ['POST', '/responses'],
      ['GET', '/files'],
      ['PUT', '/models/m-1'],
      ['POST', '/models'],
    ])('%s %s -> 404 endpoint_not_supported', async (method, path) => {
      gw = await buildGateway();
      expectError(await call(method, path, { payload: method === 'GET' ? undefined : { model: 'm' } }), 404, 'not_found_error', 'endpoint_not_supported', 'LAI-GW-024');
    });
  });

  describe('gateway disabled and maintenance (LAI-GW-051)', () => {
    it('LOCALAI_GATEWAY_ENABLED=false -> 503 with Retry-After on every route, the index and preflight included', async () => {
      gw = await buildGateway();
      gw.config.enabled = false;
      for (const [method, path] of [['GET', '/'], ['GET', '/models'], ['POST', '/chat/completions'], ['OPTIONS', '/chat/completions']] as const) {
        const response = await call(method, path, { payload: method === 'POST' ? { model: 'm' } : undefined });
        expectError(response, 503, 'server_error', 'gateway_unavailable', 'LAI-GW-051');
        expect(response.headers['retry-after']).toBe('30');
      }
    });

    it('EAB maintenance mode -> 503, before authentication (no key needed to be told)', async () => {
      gw = await buildGateway();
      gw.maintenance.active = true;
      expectError(await call('GET', '/models', { token: null }), 503, 'server_error', 'gateway_unavailable', 'LAI-GW-051');
      expect(gw.store.lookups).toBe(0);
    });

    it('a failing maintenance lookup does not take the gateway down', async () => {
      gw = await buildGateway();
      // replace the check with one that throws, through a fresh app
      await gw.app.close();
      const app = Fastify();
      await registerGatewayRoutesOn(app, {
        authenticator: gw.authenticator,
        config: { enabled: () => true, corsOrigins: async () => [] },
        maintenance: { isActive: async () => { throw new Error('db down'); } },
        logger: { info: () => undefined, warn: () => undefined, error: () => undefined },
      });
      await app.ready();
      expect((await app.inject({ method: 'GET', url: `${gw.prefix}/` })).statusCode).toBe(200);
      await app.close();
      gw = await buildGateway();
    });
  });

  describe('unexpected exceptions (LAI-GW-052)', () => {
    it('a handler that throws -> 500 with the request id; the stack is logged, the body never', async () => {
      const marker = `marker-${randomBytes(6).toString('hex')}`;
      gw = await buildGateway({
        handlers: {
          chatCompletions: async () => {
            throw new Error('boom inside handler');
          },
        },
      });
      const response = await call('POST', '/chat/completions', { payload: { model: 'm', messages: [{ role: 'user', content: marker }] } });
      const error = expectError(response, 500, 'server_error', 'internal_error', 'LAI-GW-052');
      expect(error.message).not.toContain('boom');
      expect(gw.logs.error).toHaveLength(1);
      expect(gw.logs.error[0]!.stack).toContain('boom inside handler');
      expect(JSON.stringify(gw.logs)).not.toContain(marker);
    });

    it('a GatewayError raised by a later item keeps its row (LAI-GW-021 with Retry-After 30)', async () => {
      gw = await buildGateway({
        handlers: {
          chatCompletions: async () => {
            throw new GatewayError('LAI-GW-021', { message: "No online node serves the model 'm-9' right now. Try again later or pick another model." });
          },
        },
      });
      const response = await call('POST', '/chat/completions', { payload: { model: 'm-9' } });
      expectError(response, 503, 'server_error', 'no_capacity', 'LAI-GW-021');
      expect(response.headers['retry-after']).toBe('30');
    });
  });

  describe('CORS', () => {
    const origin = 'https://app.example.test';

    it('empty setting: no Access-Control-* header on any response (even with an Origin), and the preflight is a bare 204', async () => {
      gw = await buildGateway();
      for (const response of [
        await call('GET', '/', { headers: { origin } }),
        await call('GET', '/models', { token: null, headers: { origin } }),
        await call('OPTIONS', '/chat/completions', { headers: { origin, 'access-control-request-method': 'POST' } }),
      ]) {
        expect(Object.keys(response.headers).filter((h) => h.startsWith('access-control-'))).toEqual([]);
      }
      expect((await call('OPTIONS', '/chat/completions', { headers: { origin } })).statusCode).toBe(204);
    });

    it('a listed origin: preflight 204 with the fixed headers and no credentials header', async () => {
      gw = await buildGateway();
      gw.config.origins = [origin];
      const response = await call('OPTIONS', '/chat/completions', { token: null, headers: { origin, 'access-control-request-method': 'POST' } });
      expect(response.statusCode).toBe(204);
      expect(response.headers).toMatchObject({
        'access-control-allow-origin': origin,
        'access-control-allow-methods': 'GET, POST, OPTIONS',
        'access-control-allow-headers': 'authorization, content-type',
        'access-control-max-age': '600',
        vary: 'Origin',
      });
      expect(response.headers['access-control-allow-credentials']).toBeUndefined();
      expect(response.headers['x-request-id']).toBeDefined();
    });

    it('a listed origin also gets Allow-Origin on actual responses, errors included; an unlisted one nothing', async () => {
      gw = await buildGateway();
      gw.config.origins = [origin];
      const ok = await call('GET', '/', { headers: { origin } });
      expect(ok.headers['access-control-allow-origin']).toBe(origin);
      const refused = await call('GET', '/models', { token: null, headers: { origin } });
      expect(refused.statusCode).toBe(401);
      expect(refused.headers['access-control-allow-origin']).toBe(origin);
      const other = await call('OPTIONS', '/chat/completions', { headers: { origin: 'https://evil.example.test' } });
      expect(other.statusCode).toBe(204);
      expect(Object.keys(other.headers).filter((h) => h.startsWith('access-control-'))).toEqual([]);
    });
  });

  describe('logging (spec section 6)', () => {
    it('a request with a marker in the body leaves no marker and no Authorization header in any captured log', async () => {
      // `/control` sits outside the gateway scope: it proves the capture works and that the gateway's `logLevel: 'warn'` is what silences it.
      gw = await buildGateway({ fastifyLogs: true, beforeReady: (app) => void app.get('/control', async () => 'ok') });
      await gw.app.inject({ method: 'GET', url: '/control' });
      const marker = `marker-${randomBytes(8).toString('hex')}`;
      // success path (stub 503), validation failure, malformed JSON that quotes the marker, oversize, anonymous, 500
      await call('POST', '/chat/completions', { payload: { model: 'm', messages: [{ role: 'user', content: marker }] } });
      await call('POST', '/chat/completions', { payload: { messages: [{ role: 'user', content: marker }] } });
      await call('POST', '/chat/completions', { payload: `{"model": "${marker}", ` });
      await call('POST', '/chat/completions', { payload: JSON.stringify({ model: 'm', pad: marker.repeat(GATEWAY_JSON_BODY_LIMIT / marker.length + 2) }) });
      await call('POST', '/chat/completions', { token: null, payload: { model: marker } });
      await call('GET', `/models?q=${marker}`, { headers: { cookie: 'refresh_token=abc' } });
      const everything = JSON.stringify({ app: gw.logs, fastify: gw.fastifyLog });
      expect(everything).not.toContain(marker);
      expect(everything).not.toContain(gw.token);
      expect(everything.toLowerCase()).not.toContain('authorization');
      expect(everything.toLowerCase()).not.toContain('bearer');
      expect(everything).not.toContain('refresh_token');
      expect(gw.logs.info.length + gw.logs.warn.length).toBeGreaterThanOrEqual(6);
      expect(gw.fastifyLog.join('')).toContain('incoming request');
      expect(gw.fastifyLog.join('')).not.toContain('/api/gateway');
    });

    it('writes one fixed-field line per request: request id, key prefix, user, endpoint, status, error code', async () => {
      gw = await buildGateway();
      await call('POST', '/chat/completions', { payload: { model: 'alias-1' } });
      await call('GET', '/models', { token: null });
      const lines = [...gw.logs.info, ...gw.logs.warn].map((l) => JSON.parse(l) as Record<string, unknown>);
      expect(lines).toHaveLength(2);
      const byEndpoint = (endpoint: string) => lines.find((l) => l.endpoint === endpoint);
      expect(byEndpoint('chat.completions')).toMatchObject({ key_prefix: gw.token.slice(0, 10), user_id: 'user-a', endpoint: 'chat.completions', requested_model: 'alias-1', status: 503, error_code: 'LAI-GW-051' });
      expect(byEndpoint('models')).toMatchObject({ key_prefix: null, endpoint: 'models', status: 401, error_code: 'LAI-GW-010' });
      expect(Object.keys(lines[0]!)).toEqual([
        'request_id', 'key_prefix', 'user_id', 'endpoint', 'model', 'requested_model', 'node_id', 'attempts', 'status', 'stream', 'aborted',
        'ttfb_ms', 'duration_ms', 'prompt_tokens', 'completion_tokens', 'usage_source', 'error_code',
      ]);
      // 5xx is a warning, 4xx info
      expect(gw.logs.warn).toHaveLength(1);
      expect(gw.logs.info).toHaveLength(1);
    });
  });
});
