// The only change the gateway makes to a client's JSON body (docs/specs/gateway.md section 4.2): `model` is replaced by
// the resolved catalog id; for `stream: true` on chat/completions, `stream_options.include_usage` is set to true so the
// node ends the stream with a usage chunk (GW-07 reads it). Every other field round-trips as parsed.

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

export interface RewriteOptions {
  /** The resolved catalog id. */
  modelId: string;
  stream: boolean;
  /** `true` only for chat/completions: the only endpoint where the gateway asks for the usage chunk. */
  forceUsageChunk: boolean;
}

/** True when the client itself asked for the usage chunk (`stream_options.include_usage === true`): GW-07 then forwards it. */
export function clientWantsUsageChunk(body: Record<string, unknown>): boolean {
  return isRecord(body.stream_options) && body.stream_options.include_usage === true;
}

/**
 * The object to serialise upstream. A new object (the parsed body is left alone, the caller still needs the client's
 * original `stream_options`), with `model` in its original position. The client's other `stream_options` keys are kept;
 * a `stream_options` that is not an object is replaced.
 */
export function rewriteBody(body: Record<string, unknown>, options: RewriteOptions): Record<string, unknown> {
  const rewritten: Record<string, unknown> = { ...body, model: options.modelId };
  if (options.stream && options.forceUsageChunk) {
    rewritten.stream_options = { ...(isRecord(body.stream_options) ? body.stream_options : {}), include_usage: true };
  }
  return rewritten;
}

/** The bytes sent upstream. */
export function serialiseBody(body: Record<string, unknown>, options: RewriteOptions): Buffer {
  return Buffer.from(JSON.stringify(rewriteBody(body, options)), 'utf8');
}
