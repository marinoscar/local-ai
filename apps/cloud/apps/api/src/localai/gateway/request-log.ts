// The per-request log line of the gateway (docs/specs/gateway.md sections 6 and 7). It is built from a FIXED field list
// only: nothing here can reach the request body or any header, so a prompt or a key can never be logged by accident.
// Later items fill the model/node/usage fields on the same record as they learn them.
/** The fields of the line, in order (spec section 7). */
export const REQUEST_LOG_FIELDS = [
  'request_id',
  'key_prefix',
  'user_id',
  'endpoint',
  'model',
  'requested_model',
  'node_id',
  'attempts',
  'status',
  'stream',
  'aborted',
  'ttfb_ms',
  'duration_ms',
  'prompt_tokens',
  'completion_tokens',
  'usage_source',
  'error_code',
] as const;

export interface GatewayRequestLog {
  request_id: string;
  key_prefix: string | null;
  user_id: string | null;
  endpoint: string | null;
  model: string | null;
  requested_model: string | null;
  node_id: string | null;
  attempts: number | null;
  status: number | null;
  stream: boolean | null;
  aborted: boolean | null;
  ttfb_ms: number | null;
  duration_ms: number | null;
  prompt_tokens: number | null;
  completion_tokens: number | null;
  usage_source: 'upstream' | 'estimated' | 'none' | null;
  error_code: string | null;
}

export function newRequestLog(requestId: string): GatewayRequestLog {
  return {
    request_id: requestId,
    key_prefix: null,
    user_id: null,
    endpoint: null,
    model: null,
    requested_model: null,
    node_id: null,
    attempts: null,
    status: null,
    stream: null,
    aborted: null,
    ttfb_ms: null,
    duration_ms: null,
    prompt_tokens: null,
    completion_tokens: null,
    usage_source: null,
    error_code: null,
  };
}

/** The longest string a field of the line carries: `requested_model` is text the client chose (up to the 8 MiB body). */
export const MAX_LOG_FIELD_CHARS = 200;

/** The JSON of the line: exactly the listed fields, in order, whatever else the record may have grown. */
export function formatRequestLog(log: GatewayRequestLog): string {
  const line: Record<string, unknown> = {};
  for (const field of REQUEST_LOG_FIELDS) {
    const value = log[field];
    line[field] = typeof value === 'string' && value.length > MAX_LOG_FIELD_CHARS ? value.slice(0, MAX_LOG_FIELD_CHARS) : value;
  }
  return JSON.stringify(line);
}

/** Where the gateway's own log lines go (Nest's logger in the app, an array in a spec). */
export interface GatewayLogSink {
  /** A request line at or below 4xx. */
  info(line: string): void;
  /** A 5xx request line, or an operational warning. */
  warn(line: string): void;
  /** An unexpected exception: a message and its stack, never the body. */
  error(message: string, stack?: string): void;
}
