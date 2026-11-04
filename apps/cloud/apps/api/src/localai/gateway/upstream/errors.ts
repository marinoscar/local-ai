// What can go wrong on the way to a node, and what each failure means for the attempt loop (docs/specs/gateway.md
// sections 3.3, 4.2 and 5). PURE: no I/O, so the whole classification table is unit tested.
//
// Three questions are answered here:
//   1. A transport error (undici / Node): was it a connect failure, a TLS failure, a first-byte timeout, or the request
//      being aborted on purpose?                                     -> `classifyTransportError`
//   2. An upstream status (+ a small slice of its body): retry on another node, or is this the answer?
//                                                                    -> `classifyStatus`
//   3. Every attempt failed: which gateway error does the client get? -> `aggregateFailures`
// plus the normalisation of an upstream 4xx body into the gateway's error shape (LAI-GW-042).
import { GatewayError, type GatewayLocalaiCode } from '../errors';

/** The `reason` of a per-attempt failure log line (spec section 4.2 and the issue's log contract). */
export type AttemptFailureReason = 'connect' | 'tls' | 'timeout' | 'status_5xx' | 'status_401' | 'model_not_loaded';

/** One failed attempt, as the loop remembers it. */
export interface AttemptFailure {
  nodeId: string;
  /** 1-based. */
  attempt: number;
  reason: AttemptFailureReason;
  /** The upstream HTTP status when the node answered, else null. */
  upstreamStatus: number | null;
}

/** A failure to reach the node or to get headers from it; the cause is dropped on purpose (it can name a host). */
export class UpstreamTransportError extends Error {
  constructor(
    readonly reason: 'connect' | 'tls' | 'timeout',
    /** The Node / undici error code (`ECONNREFUSED`, `UND_ERR_HEADERS_TIMEOUT`, ...), for the log only. */
    readonly code: string | null,
  ) {
    super(`upstream ${reason}`);
    this.name = 'UpstreamTransportError';
  }
}

/** The request's own `AbortSignal` fired (the client went away, or the total timer ran out). */
export class UpstreamAbortedError extends Error {
  constructor() {
    super('upstream request aborted');
    this.name = 'UpstreamAbortedError';
  }
}

/** The upstream body is larger than the gateway reads (non-stream cap, spec section 4.2). */
export class UpstreamBodyTooLargeError extends Error {
  constructor(readonly limitBytes: number) {
    super(`upstream body larger than ${limitBytes} bytes`);
    this.name = 'UpstreamBodyTooLargeError';
  }
}

/** The body broke off after the headers (socket closed, idle timeout). Never retried: the node already answered. */
export class UpstreamBodyError extends Error {
  constructor(readonly timedOut: boolean) {
    super(timedOut ? 'upstream body timed out' : 'upstream body interrupted');
    this.name = 'UpstreamBodyError';
  }
}

const TLS_CODES = new Set([
  'CERT_HAS_EXPIRED',
  'CERT_NOT_YET_VALID',
  'CERT_REVOKED',
  'CERT_UNTRUSTED',
  'DEPTH_ZERO_SELF_SIGNED_CERT',
  'SELF_SIGNED_CERT_IN_CHAIN',
  'UNABLE_TO_VERIFY_LEAF_SIGNATURE',
  'UNABLE_TO_GET_ISSUER_CERT',
  'UNABLE_TO_GET_ISSUER_CERT_LOCALLY',
  'HOSTNAME_MISMATCH',
  'ERR_TLS_CERT_ALTNAME_INVALID',
]);

interface CodedError {
  code?: unknown;
  name?: unknown;
  cause?: unknown;
}

/** The error codes of an error and of its `cause` chain (undici and Node wrap one in another). */
function codesOf(error: unknown): string[] {
  const codes: string[] = [];
  let current: unknown = error;
  for (let depth = 0; depth < 4 && typeof current === 'object' && current !== null; depth += 1) {
    const { code, cause } = current as CodedError;
    if (typeof code === 'string') codes.push(code);
    current = cause;
  }
  return codes;
}

export type TransportClass = 'connect' | 'tls' | 'timeout' | 'aborted';

/**
 * Sorts a thrown error into one of four classes. `aborted` is the caller's own `AbortSignal` (never retried, never a
 * node's fault); `timeout` is the headers timeout (the first byte did not come); `tls` a certificate or handshake
 * problem; everything else that happened before headers (refused, reset, DNS, connect timeout, a socket that closed)
 * is `connect`.
 */
export function classifyTransportError(error: unknown): TransportClass {
  const codes = codesOf(error);
  const name = typeof error === 'object' && error !== null ? (error as CodedError).name : undefined;
  if (name === 'AbortError' || name === 'RequestAbortedError' || codes.includes('UND_ERR_ABORTED') || codes.includes('ABORT_ERR')) return 'aborted';
  if (codes.includes('UND_ERR_HEADERS_TIMEOUT')) return 'timeout';
  if (codes.some((code) => TLS_CODES.has(code) || code.startsWith('ERR_SSL_') || code.startsWith('ERR_TLS_'))) return 'tls';
  return 'connect';
}

/** The first code of the error chain, for the log. */
export function transportErrorCode(error: unknown): string | null {
  return codesOf(error)[0] ?? null;
}

/** llama-server says it in the message: "model not found", "model ... is not loaded", "unloaded". */
const MODEL_NOT_LOADED = /(model[^.\n]{0,80}?(not found|not loaded|unloaded))|((not loaded|unloaded)[^.\n]{0,80}?model)/i;

export type StatusVerdict =
  /** 2xx: the first byte, commit. */
  | { kind: 'success' }
  /** Try the next candidate. */
  | { kind: 'retry'; reason: 'status_5xx' | 'status_401' | 'model_not_loaded' }
  /** A client error that is the answer: pass it through (LAI-GW-042). */
  | { kind: 'final' };

/**
 * Spec section 4.2, one line per row. `bodySnippet` is the start of the upstream body (only read for 400 and 404, where
 * "model not found / not loaded / unloaded" means "another node"). Redirects and other non-final, non-2xx statuses are
 * treated like a 5xx: a node that answers with one is misconfigured, not asking the client to do anything.
 */
export function classifyStatus(status: number, bodySnippet: string): StatusVerdict {
  if (status >= 200 && status < 300) return { kind: 'success' };
  if (status === 401) return { kind: 'retry', reason: 'status_401' };
  if ((status === 400 || status === 404) && MODEL_NOT_LOADED.test(bodySnippet)) return { kind: 'retry', reason: 'model_not_loaded' };
  if (status >= 400 && status < 500) return { kind: 'final' };
  return { kind: 'retry', reason: 'status_5xx' };
}

// ---- LAI-GW-042: an upstream 4xx, normalised -------------------------------------------------------------------

const SAFE_CODE = /^[A-Za-z0-9_.-]{1,64}$/;
const MAX_MESSAGE = 1_000;

export interface NormalisedRejection {
  status: number;
  /** The upstream message when it gave one, else undefined (the table's default is used). */
  message?: string;
  /** A string: the upstream code when it was a safe string, else `upstream_rejected`. */
  code: string;
  param?: string | null;
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/**
 * llama-server answers `{"error":{"code":400,"message":"...","type":"invalid_request_error"}}`. The status is kept, the
 * `message` is kept (bounded), `code` becomes a string (a numeric code is replaced by `upstream_rejected`), and the
 * gateway adds `localai_code: "LAI-GW-042"` when it builds the error. A body that is not that shape yields the defaults.
 */
export function normaliseUpstreamRejection(status: number, bodyText: string): NormalisedRejection {
  let parsed: unknown;
  try {
    parsed = JSON.parse(bodyText);
  } catch {
    parsed = null;
  }
  const error = isRecord(parsed) && isRecord(parsed.error) ? parsed.error : null;
  const message = error && typeof error.message === 'string' && error.message.trim().length > 0 ? error.message.trim().slice(0, MAX_MESSAGE) : undefined;
  const rawCode = error?.code;
  const code = typeof rawCode === 'string' && SAFE_CODE.test(rawCode) ? rawCode : 'upstream_rejected';
  const param = error && typeof error.param === 'string' && SAFE_CODE.test(error.param) ? error.param : null;
  return { status, message, code, param };
}

/** The gateway error for an upstream 4xx. */
export function upstreamRejectionError(status: number, bodyText: string): GatewayError {
  const normalised = normaliseUpstreamRejection(status, bodyText);
  return new GatewayError('LAI-GW-042', { message: normalised.message, upstreamStatus: normalised.status, upstreamCode: normalised.code, param: normalised.param });
}

// ---- all attempts failed ---------------------------------------------------------------------------------------

type FailureCategory = 'auth' | 'timeout' | 'error' | 'unreachable' | 'not_loaded';

const CATEGORY_OF: Record<AttemptFailureReason, FailureCategory> = {
  connect: 'unreachable',
  tls: 'unreachable',
  timeout: 'timeout',
  status_5xx: 'error',
  status_401: 'auth',
  model_not_loaded: 'not_loaded',
};

const CODE_OF: Record<FailureCategory, GatewayLocalaiCode> = {
  auth: 'LAI-GW-043',
  timeout: 'LAI-GW-044',
  error: 'LAI-GW-041',
  unreachable: 'LAI-GW-040',
  // Every node that was tried said the model is not loaded: for the client that is "no capacity right now".
  not_loaded: 'LAI-GW-021',
};

/** On a tie in the count, the category earlier in this list wins: what an administrator must act on first. */
const TIE_ORDER: readonly FailureCategory[] = ['auth', 'timeout', 'error', 'unreachable', 'not_loaded'];

/**
 * The error for a request whose every attempt failed before the first byte. The dominant failure (the category with the
 * most attempts; ties by `TIE_ORDER`) decides: all connect/TLS -> 502 LAI-GW-040; 5xx -> 502 LAI-GW-041; 401 -> 502
 * LAI-GW-043; first-byte timeout -> 504 LAI-GW-044; "model not loaded" -> 503 LAI-GW-021. With no failure at all (the
 * loop never got a candidate) the answer is the generic 503 LAI-GW-021.
 */
export function aggregateFailures(failures: readonly AttemptFailure[]): GatewayError {
  if (failures.length === 0) return new GatewayError('LAI-GW-021');
  const counts = new Map<FailureCategory, number>();
  for (const failure of failures) counts.set(CATEGORY_OF[failure.reason], (counts.get(CATEGORY_OF[failure.reason]) ?? 0) + 1);
  let best: FailureCategory = TIE_ORDER[0]!;
  let bestCount = -1;
  for (const category of TIE_ORDER) {
    const count = counts.get(category) ?? 0;
    if (count > bestCount) {
      best = category;
      bestCount = count;
    }
  }
  return new GatewayError(CODE_OF[best]);
}
