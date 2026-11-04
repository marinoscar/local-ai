// The ONE OpenAI-shaped error builder of the gateway (docs/specs/gateway.md section 3.3). Every later gateway item
// raises a `GatewayError` with a code from this table and never builds an error body of its own. The doctor codes
// (`LAI-GW-*`) are registered in docs/specs/doctor-codes.md; none is invented here.
import type { FastifyReply } from 'fastify';

export type GatewayErrorType =
  | 'invalid_request_error'
  | 'authentication_error'
  | 'rate_limit_error'
  | 'permission_error'
  | 'not_found_error'
  | 'server_error'
  | 'api_error';

/** The doctor codes the gateway answers with before the first byte (LAI-GW-045 and -060 never become a response). */
export type GatewayLocalaiCode =
  | 'LAI-GW-001'
  | 'LAI-GW-002'
  | 'LAI-GW-003'
  | 'LAI-GW-004'
  | 'LAI-GW-010'
  | 'LAI-GW-011'
  | 'LAI-GW-012'
  | 'LAI-GW-013'
  | 'LAI-GW-020'
  | 'LAI-GW-021'
  | 'LAI-GW-022'
  | 'LAI-GW-023'
  | 'LAI-GW-024'
  | 'LAI-GW-030'
  | 'LAI-GW-031'
  | 'LAI-GW-032'
  | 'LAI-GW-040'
  | 'LAI-GW-041'
  | 'LAI-GW-042'
  | 'LAI-GW-043'
  | 'LAI-GW-044'
  | 'LAI-GW-050'
  | 'LAI-GW-051'
  | 'LAI-GW-052';

export interface GatewayErrorSpec {
  status: number;
  type: GatewayErrorType;
  code: string;
  /** The plain-language message used when the raiser gives none. */
  message: string;
  /** `Retry-After` seconds when the raiser gives none (a row whose value depends on the request has none here). */
  retryAfterS?: number;
}

/** Spec section 3.3, every row. The raiser overrides only what the row leaves open (`message`, `Retry-After`, the 042 status/code). */
export const GATEWAY_ERRORS: Readonly<Record<GatewayLocalaiCode, GatewayErrorSpec>> = {
  'LAI-GW-001': { status: 400, type: 'invalid_request_error', code: 'invalid_json', message: 'The request body is not valid JSON. Send a JSON object.' },
  'LAI-GW-002': { status: 400, type: 'invalid_request_error', code: 'missing_model', message: "The request has no 'model'. Add \"model\": \"<id>\"; GET /v1/models lists the ids." },
  'LAI-GW-003': { status: 400, type: 'invalid_request_error', code: 'unsupported_parameter', message: 'A parameter of the request is not supported on this endpoint.' },
  'LAI-GW-004': { status: 413, type: 'invalid_request_error', code: 'request_too_large', message: 'The request is too large. Send smaller images or audio (limit 8 MB of JSON, 25 MB of audio).' },
  'LAI-GW-010': {
    status: 401,
    type: 'authentication_error',
    code: 'invalid_api_key',
    message: 'The API key is missing, wrong, revoked or expired. Send a valid key as "Authorization: Bearer lai_...".',
  },
  'LAI-GW-011': { status: 429, type: 'rate_limit_error', code: 'auth_rate_limited', message: 'Too many wrong API keys from this address. Wait 5 minutes and use a valid key.' },
  'LAI-GW-012': { status: 403, type: 'permission_error', code: 'model_not_allowed', message: 'This API key may not use that model. Use an allowed model, or ask the administrator to widen the key.' },
  'LAI-GW-013': { status: 403, type: 'permission_error', code: 'key_owner_not_allowed', message: "The key's owner can no longer use the gateway. Ask the administrator to restore access." },
  'LAI-GW-020': { status: 404, type: 'not_found_error', code: 'model_not_found', message: 'Unknown model. Pick an id from GET /v1/models.' },
  'LAI-GW-021': {
    status: 503,
    type: 'server_error',
    code: 'no_capacity',
    message: 'No online node serves that model right now. Try again later or pick another model.',
    retryAfterS: 30,
  },
  'LAI-GW-022': { status: 503, type: 'server_error', code: 'model_loading', message: 'The model is being loaded. Wait about a minute and try again.', retryAfterS: 15 },
  'LAI-GW-023': {
    status: 503,
    type: 'server_error',
    code: 'no_capacity',
    message: 'Every node serving that model is busy. Try again in a few seconds.',
    retryAfterS: 30,
  },
  'LAI-GW-024': {
    status: 404,
    type: 'not_found_error',
    code: 'endpoint_not_supported',
    message: 'That endpoint is not offered. Use chat/completions, completions, embeddings or audio/transcriptions.',
  },
  'LAI-GW-030': { status: 429, type: 'rate_limit_error', code: 'rate_limit_exceeded', message: 'Too many requests per minute for this API key. Slow down, or ask the administrator for a higher limit.' },
  'LAI-GW-031': {
    status: 429,
    type: 'rate_limit_error',
    code: 'concurrency_limit_exceeded',
    message: 'Too many requests at the same time for this API key. Wait for a running request to finish.',
    retryAfterS: 2,
  },
  'LAI-GW-032': {
    status: 429,
    type: 'rate_limit_error',
    code: 'daily_token_budget_exceeded',
    message: "Today's token allowance for this API key is used up. Wait until 00:00 UTC, or ask the administrator for a larger budget.",
  },
  'LAI-GW-040': { status: 502, type: 'api_error', code: 'upstream_unreachable', message: 'The gateway could not reach a node that serves the model. Try again later.' },
  'LAI-GW-041': { status: 502, type: 'api_error', code: 'upstream_error', message: 'The node returned an error. Try again.' },
  // Status and code come from the upstream answer; these are the fallbacks when the upstream gave none.
  'LAI-GW-042': { status: 400, type: 'invalid_request_error', code: 'upstream_rejected', message: 'The node refused the request. Shorten the prompt or follow the error message.' },
  'LAI-GW-043': { status: 502, type: 'api_error', code: 'upstream_auth_failed', message: 'The gateway could not authenticate to the node. The administrator has been alerted.' },
  'LAI-GW-044': { status: 504, type: 'server_error', code: 'upstream_timeout', message: 'The node did not start answering in time. Try again; long prompts on big models can take minutes.' },
  'LAI-GW-050': { status: 503, type: 'server_error', code: 'gateway_misconfigured', message: 'The gateway cannot read the stored node keys. Ask the administrator.', retryAfterS: 30 },
  'LAI-GW-051': { status: 503, type: 'server_error', code: 'gateway_unavailable', message: 'The gateway is turned off or the site is in maintenance. Try again later.', retryAfterS: 30 },
  'LAI-GW-052': { status: 500, type: 'server_error', code: 'internal_error', message: 'Unexpected gateway error. Try again; quote the request id to the administrator.' },
};

export interface GatewayErrorOptions {
  /** Replaces the table's plain-language message. */
  message?: string;
  /** The offending parameter, for the `param` field. */
  param?: string | null;
  /** `Retry-After` in seconds (429 and 503 rows; the table's default when omitted). */
  retryAfterS?: number;
  /** LAI-GW-042 only: the upstream status (4xx) and its normalised string code. Ignored by every other row. */
  upstreamStatus?: number;
  upstreamCode?: string;
}

export class GatewayError extends Error {
  readonly status: number;
  readonly type: GatewayErrorType;
  readonly code: string;
  readonly param: string | null;
  readonly retryAfterS: number | null;

  constructor(
    readonly localaiCode: GatewayLocalaiCode,
    options: GatewayErrorOptions = {},
  ) {
    const spec = GATEWAY_ERRORS[localaiCode];
    super(options.message ?? spec.message);
    this.name = 'GatewayError';
    this.status = localaiCode === 'LAI-GW-042' && options.upstreamStatus !== undefined ? options.upstreamStatus : spec.status;
    this.type = spec.type;
    this.code = localaiCode === 'LAI-GW-042' && options.upstreamCode ? options.upstreamCode : spec.code;
    this.param = options.param ?? null;
    const retry = options.retryAfterS ?? spec.retryAfterS;
    this.retryAfterS = retry !== undefined && (this.status === 429 || this.status === 503) ? Math.max(1, Math.ceil(retry)) : null;
  }
}

export interface GatewayErrorBody {
  error: { message: string; type: GatewayErrorType; param: string | null; code: string; localai_code: GatewayLocalaiCode; request_id: string };
}

/** The section 3.3 JSON body. */
export function buildGatewayErrorBody(err: GatewayError, requestId: string): GatewayErrorBody {
  return { error: { message: err.message, type: err.type, param: err.param, code: err.code, localai_code: err.localaiCode, request_id: requestId } };
}

/**
 * Sends a gateway error as the reply, BEFORE the first byte of a response. The request id is the `x-request-id` the
 * pipeline already set on the reply (a fresh one is never invented here, so the id in the body equals the header).
 */
export function sendGatewayError(reply: FastifyReply, err: GatewayError): FastifyReply {
  const header = reply.getHeader('x-request-id');
  const requestId = typeof header === 'string' ? header : '';
  if (err.retryAfterS !== null) void reply.header('retry-after', String(err.retryAfterS));
  return reply.code(err.status).type('application/json; charset=utf-8').send(JSON.stringify(buildGatewayErrorBody(err, requestId)));
}
