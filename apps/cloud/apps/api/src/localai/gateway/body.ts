// Body limits and the shared inference-body parser (docs/specs/gateway.md sections 3.2, 3.3 and 6).
import { GatewayError } from './errors';

/** JSON endpoints (chat, completions, embeddings). Fastify's own default is 1 MiB, so the routes set this explicitly. */
export const GATEWAY_JSON_BODY_LIMIT = 8 * 1024 * 1024;
/** `/v1/audio/transcriptions` (multipart). */
export const GATEWAY_AUDIO_BODY_LIMIT = 25 * 1024 * 1024;

export interface InferenceBody {
  /** The `model` exactly as sent (an id or an alias). */
  model: string;
  body: Record<string, unknown>;
}

/**
 * The parsed JSON body of an inference request: an object (else 400 LAI-GW-001) with a non-empty string `model` (else
 * 400 LAI-GW-002). Shared by every inference endpoint. It never echoes any part of the body in an error.
 */
export function parseInferenceBody(parsedBody: unknown): InferenceBody {
  if (typeof parsedBody !== 'object' || parsedBody === null || Array.isArray(parsedBody)) throw new GatewayError('LAI-GW-001');
  const body = parsedBody as Record<string, unknown>;
  const model = body.model;
  if (typeof model !== 'string' || model.trim().length === 0) throw new GatewayError('LAI-GW-002', { param: 'model' });
  return { model, body };
}

interface FastifyLikeError {
  code?: unknown;
  statusCode?: unknown;
}

/**
 * Maps what Fastify throws while reading a body to a gateway error, or null when it is not a body error. Only the
 * error's CODE is read: its message can quote part of the body (Node's JSON.parse does), so it is never used.
 */
export function bodyErrorToGatewayError(error: unknown): GatewayError | null {
  if (error instanceof SyntaxError) return new GatewayError('LAI-GW-001');
  if (typeof error !== 'object' || error === null) return null;
  const { code, statusCode } = error as FastifyLikeError;
  if (code === 'FST_ERR_CTP_BODY_TOO_LARGE' || statusCode === 413) return new GatewayError('LAI-GW-004');
  if (typeof code === 'string' && code.startsWith('FST_ERR_CTP_')) return new GatewayError('LAI-GW-001');
  return null;
}
