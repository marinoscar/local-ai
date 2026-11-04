import Fastify from 'fastify';

import { GATEWAY_ERRORS, GatewayError, buildGatewayErrorBody, sendGatewayError, type GatewayLocalaiCode } from './errors';

// Spec gateway.md section 3.3, row by row: [code, status, type, code string, Retry-After or null].
const TABLE: Array<[GatewayLocalaiCode, number, string, string, string | null]> = [
  ['LAI-GW-001', 400, 'invalid_request_error', 'invalid_json', null],
  ['LAI-GW-002', 400, 'invalid_request_error', 'missing_model', null],
  ['LAI-GW-003', 400, 'invalid_request_error', 'unsupported_parameter', null],
  ['LAI-GW-004', 413, 'invalid_request_error', 'request_too_large', null],
  ['LAI-GW-010', 401, 'authentication_error', 'invalid_api_key', null],
  ['LAI-GW-011', 429, 'rate_limit_error', 'auth_rate_limited', '300'],
  ['LAI-GW-012', 403, 'permission_error', 'model_not_allowed', null],
  ['LAI-GW-013', 403, 'permission_error', 'key_owner_not_allowed', null],
  ['LAI-GW-020', 404, 'not_found_error', 'model_not_found', null],
  ['LAI-GW-021', 503, 'server_error', 'no_capacity', '30'],
  ['LAI-GW-022', 503, 'server_error', 'model_loading', '15'],
  ['LAI-GW-023', 503, 'server_error', 'no_capacity', '30'],
  ['LAI-GW-024', 404, 'not_found_error', 'endpoint_not_supported', null],
  ['LAI-GW-030', 429, 'rate_limit_error', 'rate_limit_exceeded', '17'],
  ['LAI-GW-031', 429, 'rate_limit_error', 'concurrency_limit_exceeded', '2'],
  ['LAI-GW-032', 429, 'rate_limit_error', 'daily_token_budget_exceeded', '3600'],
  ['LAI-GW-040', 502, 'api_error', 'upstream_unreachable', null],
  ['LAI-GW-041', 502, 'api_error', 'upstream_error', null],
  ['LAI-GW-042', 400, 'invalid_request_error', 'upstream_rejected', null],
  ['LAI-GW-043', 502, 'api_error', 'upstream_auth_failed', null],
  ['LAI-GW-044', 504, 'server_error', 'upstream_timeout', null],
  ['LAI-GW-050', 503, 'server_error', 'gateway_misconfigured', '30'],
  ['LAI-GW-051', 503, 'server_error', 'gateway_unavailable', '30'],
  ['LAI-GW-052', 500, 'server_error', 'internal_error', null],
];

// Retry-After values the raiser supplies because they depend on the request (spec: RPM window, budget day, auth block).
const RAISER_RETRY: Partial<Record<GatewayLocalaiCode, number>> = { 'LAI-GW-011': 300, 'LAI-GW-030': 17, 'LAI-GW-032': 3600 };

describe('the gateway error table', () => {
  it('has exactly the rows of the spec (every localai code once)', () => {
    expect(Object.keys(GATEWAY_ERRORS).sort()).toEqual(TABLE.map((row) => row[0]).sort());
  });

  it.each(TABLE)('%s -> %i %s %s, Retry-After %s: exact JSON and headers over Fastify', async (localaiCode, status, type, code, retryAfter) => {
    const app = Fastify();
    app.get('/x', async (_request, reply) => {
      void reply.header('x-request-id', 'req-0123456789');
      return sendGatewayError(reply, new GatewayError(localaiCode, { retryAfterS: RAISER_RETRY[localaiCode] }));
    });
    const response = await app.inject({ method: 'GET', url: '/x' });
    expect(response.statusCode).toBe(status);
    expect(response.headers['content-type']).toContain('application/json');
    expect(response.headers['retry-after']).toBe(retryAfter ?? undefined);
    expect(response.json()).toEqual({
      error: { message: GATEWAY_ERRORS[localaiCode].message, type, param: null, code, localai_code: localaiCode, request_id: 'req-0123456789' },
    });
    await app.close();
  });

  it('keeps the OpenAI field order and carries a custom message and param', () => {
    const body = buildGatewayErrorBody(new GatewayError('LAI-GW-021', { message: "No online node serves the model 'm-1' right now. Try again later or pick another model.", param: 'model' }), 'abc');
    expect(Object.keys(body.error)).toEqual(['message', 'type', 'param', 'code', 'localai_code', 'request_id']);
    expect(body.error.param).toBe('model');
    expect(body.error.message).toContain("'m-1'");
  });

  it('LAI-GW-042 takes the upstream status and string code and keeps its own localai_code', () => {
    const err = new GatewayError('LAI-GW-042', { upstreamStatus: 422, upstreamCode: 'context_length_exceeded', message: 'prompt too long' });
    expect(buildGatewayErrorBody(err, 'r')).toEqual({
      error: { message: 'prompt too long', type: 'invalid_request_error', param: null, code: 'context_length_exceeded', localai_code: 'LAI-GW-042', request_id: 'r' },
    });
    expect(err.status).toBe(422);
    expect(err.retryAfterS).toBeNull();
  });

  it('only 429 and 503 answers carry Retry-After', () => {
    expect(new GatewayError('LAI-GW-010', { retryAfterS: 5 }).retryAfterS).toBeNull();
    expect(new GatewayError('LAI-GW-044', { retryAfterS: 5 }).retryAfterS).toBeNull();
    expect(new GatewayError('LAI-GW-030', { retryAfterS: 0.2 }).retryAfterS).toBe(1);
  });
});
