// What the proxy hands to the usage recorder when a request ends (docs/specs/gateway.md sections 3.6 and 4.5), and the
// no-op sink used until the recorder (GW-08) exists. The record has the columns of `localai_gateway_usage` and NOTHING
// else: no prompt, no completion, no embedding input, no header, no IP.

export type UsageSource = 'upstream' | 'estimated' | 'none';

export interface UsageRecord {
  requestId: string;
  keyId: string | null;
  userId: string | null;
  /** `chat.completions` | `completions` | `embeddings` (| `audio.transcriptions`, GW-10). */
  endpoint: string;
  /** The resolved catalog id. */
  model: string | null;
  /** As sent by the client (an alias or the id). */
  requestedModel: string | null;
  nodeId: string | null;
  /** The HTTP status sent (499 when the client went away before an answer). */
  status: number;
  /** `LAI-GW-0NN` when the request ended in a gateway error. */
  errorCode: string | null;
  stream: boolean;
  aborted: boolean;
  attempts: number;
  promptTokens: number | null;
  completionTokens: number | null;
  usageSource: UsageSource;
  ttfbMs: number | null;
  durationMs: number;
}

/**
 * Where finished requests go. `record` MUST NOT throw, block or await I/O: the recorder queues the row in memory and
 * flushes later. The proxy still wraps the call, so a faulty sink cannot fail a response.
 */
export interface UsageSink {
  record(record: UsageRecord): void;
}

/** Injection token of the {@link UsageSink}. GW-08 binds its recorder to it. */
export const GATEWAY_USAGE_SINK = Symbol('LOCALAI_GATEWAY_USAGE_SINK');

/** Used until the recorder exists: the request line in the log is the only trace. */
export const noopUsageSink: UsageSink = { record: () => undefined };

/** `usage` of an OpenAI-style JSON body: non-negative integers only; anything else is "no usage". */
export interface ParsedUsage {
  promptTokens: number;
  completionTokens: number;
}

const isCount = (value: unknown): value is number => typeof value === 'number' && Number.isInteger(value) && value >= 0;

/**
 * Reads `usage.prompt_tokens` and `usage.completion_tokens` (spec section 4.2, token accounting; `total_tokens` is
 * never read, so an inconsistent total cannot matter). Embeddings carry no completion tokens: they count as 0. Returns
 * null when the body has no usable `usage`.
 */
export function parseUsage(body: unknown): ParsedUsage | null {
  if (typeof body !== 'object' || body === null) return null;
  const usage = (body as { usage?: unknown }).usage;
  if (typeof usage !== 'object' || usage === null) return null;
  const { prompt_tokens: prompt, completion_tokens: completion } = usage as Record<string, unknown>;
  if (!isCount(prompt)) return null;
  return { promptTokens: prompt, completionTokens: isCount(completion) ? completion : 0 };
}
