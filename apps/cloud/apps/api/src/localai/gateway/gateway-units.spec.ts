import { corsHeaders, parseCorsOrigins, GatewayCorsOriginsSchema } from './cors';
import { GatewayError } from './errors';
import { bodyErrorToGatewayError, parseInferenceBody, GATEWAY_JSON_BODY_LIMIT, GATEWAY_AUDIO_BODY_LIMIT } from './body';
import { buildGatewayIndex, GATEWAY_DOCS_URL } from './index-route';
import { corsOriginsFromStored, gatewayEnabledFromEnv } from './gateway-settings';
import { newRequestId, resolveRequestId } from './request-id';
import { bearerToken, hashGatewayToken, hashesEqual, isWellFormedGatewayKey } from './token-verify';
import { formatRequestLog, newRequestLog, REQUEST_LOG_FIELDS } from './request-log';
import { TtlCache } from './auth-cache';
import { makeToken } from './gateway.testkit';

describe('cors', () => {
  const origins = ['https://app.example.test', 'http://localhost:5173'];

  it('an empty list sends no Access-Control header at all, even for a preflight', () => {
    expect(corsHeaders('https://app.example.test', [], true)).toEqual({});
    expect(corsHeaders('https://app.example.test', [], false)).toEqual({});
  });

  it('a listed origin gets the exact preflight headers and never Allow-Credentials', () => {
    const headers = corsHeaders('https://app.example.test', origins, true);
    expect(headers).toEqual({
      'access-control-allow-origin': 'https://app.example.test',
      vary: 'Origin',
      'access-control-allow-methods': 'GET, POST, OPTIONS',
      'access-control-allow-headers': 'authorization, content-type',
      'access-control-max-age': '600',
    });
    expect(Object.keys(headers)).not.toContain('access-control-allow-credentials');
  });

  it('a simple request gets only Allow-Origin and Vary', () => {
    expect(corsHeaders('http://localhost:5173', origins, false)).toEqual({ 'access-control-allow-origin': 'http://localhost:5173', vary: 'Origin' });
  });

  it.each(['https://evil.example.test', 'https://app.example.test/', 'HTTPS://app.example.test', 'null', undefined])('does not answer origin %s', (origin) => {
    expect(corsHeaders(origin, origins, true)).toEqual({});
  });

  it('the settings schema refuses "*" and anything that is not an exact origin', () => {
    expect(GatewayCorsOriginsSchema.safeParse(['*']).success).toBe(false);
    expect(GatewayCorsOriginsSchema.safeParse(['https://*.example.test']).success).toBe(false);
    expect(GatewayCorsOriginsSchema.safeParse(['https://app.example.test/path']).success).toBe(false);
    expect(GatewayCorsOriginsSchema.safeParse(['app.example.test']).success).toBe(false);
    expect(GatewayCorsOriginsSchema.safeParse(['https://app.example.test', 'http://localhost:5173']).success).toBe(true);
    expect(GatewayCorsOriginsSchema.parse(undefined)).toEqual([]);
  });

  it('a stored invalid value means no origins (CORS stays off)', () => {
    expect(parseCorsOrigins(['*'])).toEqual([]);
    expect(parseCorsOrigins('https://app.example.test')).toEqual([]);
    expect(parseCorsOrigins(['https://app.example.test'])).toEqual(['https://app.example.test']);
  });
});

describe('gateway settings', () => {
  it('reads both the nested and the dotted stored form', () => {
    expect(corsOriginsFromStored({ gateway: { corsOrigins: ['https://app.example.test'] } })).toEqual(['https://app.example.test']);
    expect(corsOriginsFromStored({ 'gateway.corsOrigins': ['https://app.example.test'] })).toEqual(['https://app.example.test']);
    expect(corsOriginsFromStored(undefined)).toEqual([]);
    expect(corsOriginsFromStored({ gateway: { corsOrigins: ['*'] } })).toEqual([]);
  });

  it('LOCALAI_GATEWAY_ENABLED defaults to true; only "false" turns it off', () => {
    expect(gatewayEnabledFromEnv({})).toBe(true);
    expect(gatewayEnabledFromEnv({ LOCALAI_GATEWAY_ENABLED: 'true' })).toBe(true);
    expect(gatewayEnabledFromEnv({ LOCALAI_GATEWAY_ENABLED: 'FALSE' })).toBe(false);
    expect(gatewayEnabledFromEnv({ LOCALAI_GATEWAY_ENABLED: ' false ' })).toBe(false);
    expect(gatewayEnabledFromEnv({ LOCALAI_GATEWAY_ENABLED: '' })).toBe(true);
  });
});

describe('parseInferenceBody', () => {
  it('returns the model and the body', () => {
    expect(parseInferenceBody({ model: 'm-1', stream: true })).toEqual({ model: 'm-1', body: { model: 'm-1', stream: true } });
  });

  it.each([null, undefined, 'text', 3, [], [{ model: 'x' }]])('%j is not an object: LAI-GW-001', (value) => {
    expect(() => parseInferenceBody(value)).toThrow(expect.objectContaining({ localaiCode: 'LAI-GW-001' }));
  });

  it.each([{}, { model: 1 }, { model: null }, { model: '' }, { model: '  ' }, { model: ['a'] }])('%j has no usable model: LAI-GW-002', (value) => {
    expect(() => parseInferenceBody(value)).toThrow(expect.objectContaining({ localaiCode: 'LAI-GW-002', param: 'model' }));
  });

  it('maps body-reading errors by code only', () => {
    expect(bodyErrorToGatewayError(Object.assign(new Error('x'), { code: 'FST_ERR_CTP_BODY_TOO_LARGE', statusCode: 413 }))?.localaiCode).toBe('LAI-GW-004');
    expect(bodyErrorToGatewayError(Object.assign(new Error('x'), { code: 'FST_ERR_CTP_EMPTY_JSON_BODY', statusCode: 400 }))?.localaiCode).toBe('LAI-GW-001');
    expect(bodyErrorToGatewayError(new SyntaxError('Unexpected token'))?.localaiCode).toBe('LAI-GW-001');
    expect(bodyErrorToGatewayError(new Error('boom'))).toBeNull();
    expect(bodyErrorToGatewayError(new GatewayError('LAI-GW-010'))).toBeNull();
  });

  it('the limits are 8 MiB JSON and 25 MiB audio', () => {
    expect(GATEWAY_JSON_BODY_LIMIT).toBe(8 * 1024 * 1024);
    expect(GATEWAY_AUDIO_BODY_LIMIT).toBe(25 * 1024 * 1024);
  });
});

describe('the index', () => {
  it('names the object, the endpoints and the docs', () => {
    const index = buildGatewayIndex();
    expect(index.object).toBe('localai.gateway');
    expect(index.docs).toBe(GATEWAY_DOCS_URL);
    expect(index.endpoints.map((e) => `${e.method} ${e.path}`)).toEqual([
      'GET /v1/models',
      'GET /v1/models/{id}',
      'POST /v1/chat/completions',
      'POST /v1/completions',
      'POST /v1/embeddings',
      'POST /v1/audio/transcriptions',
    ]);
  });
});

describe('request id', () => {
  it('keeps a well-formed id and replaces anything else', () => {
    expect(resolveRequestId('4f1c0a9e5b7d4a2e8c3d1f6a9b0e2c47')).toBe('4f1c0a9e5b7d4a2e8c3d1f6a9b0e2c47');
    expect(resolveRequestId(['abcdefgh-1'])).toBe('abcdefgh-1');
    for (const bad of [undefined, '', 'short', 'has space in it', 'x'.repeat(129), 'bad\r\nheader: 1']) expect(resolveRequestId(bad)).toMatch(/^[0-9a-f]{32}$/);
    expect(newRequestId()).not.toBe(newRequestId());
  });
});

describe('token verification', () => {
  it('hashes the full token to sha256 hex and compares in constant time', () => {
    const token = makeToken();
    expect(isWellFormedGatewayKey(token)).toBe(true);
    const hash = hashGatewayToken(token);
    expect(hash).toMatch(/^[0-9a-f]{64}$/);
    expect(hashesEqual(hash, hashGatewayToken(token))).toBe(true);
    expect(hashesEqual(hash, hashGatewayToken(makeToken()))).toBe(false);
    expect(hashesEqual(hash, hash.slice(1))).toBe(false);
  });

  it('extracts only a Bearer token', () => {
    expect(bearerToken('Bearer abc')).toBe('abc');
    expect(bearerToken('bearer   abc  ')).toBe('abc');
    expect(bearerToken('Bearer')).toBeNull();
    expect(bearerToken('Bearer a b')).toBeNull();
    expect(bearerToken('Token abc')).toBeNull();
    expect(bearerToken(undefined)).toBeNull();
  });
});

describe('the request log line', () => {
  it('carries exactly the fixed fields, in order, and nothing a record may have grown', () => {
    const log = newRequestLog('req-1');
    Object.assign(log, { body: 'marker text', authorization: 'Bearer x', status: 200 });
    const line = JSON.parse(formatRequestLog(log)) as Record<string, unknown>;
    expect(Object.keys(line)).toEqual([...REQUEST_LOG_FIELDS]);
    expect(line.status).toBe(200);
    expect(JSON.stringify(line)).not.toContain('marker text');
  });
});

describe('TtlCache', () => {
  it('expires entries, evicts, and stays bounded', () => {
    let now = 0;
    const cache = new TtlCache<number>(1000, () => now, 3);
    cache.set('a', 1);
    expect(cache.get('a')).toBe(1);
    now = 1000;
    expect(cache.get('a')).toBeUndefined();
    now = 2000;
    for (const key of ['a', 'b', 'c', 'd']) cache.set(key, 1);
    expect(cache.size).toBeLessThanOrEqual(3);
    expect(cache.get('d')).toBe(1);
    expect(cache.evict('d')).toBe(true);
    expect(cache.evict('d')).toBe(false);
  });
});
