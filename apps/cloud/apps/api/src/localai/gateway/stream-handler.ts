// The seam for SSE passthrough. `GatewayService` prepares every request the same way (model, scope, limits, body
// rewrite, timeouts) and then hands a `stream: true` request to this handler instead of reading the upstream body
// itself. The implementation is `StreamProxy` (`stream/stream-proxy.ts`, GW-07): it runs `loop.run(...)`, and on a
// committed attempt hijacks the reply and pipes the events (docs/specs/gateway.md section 4.2), then calls
// `request.settle(...)` and `committed.release()` when the stream ends. Bound in `GatewayProxyModule`.
import type { AttemptLoop } from './attempts';
import type { ProxyRequest } from './proxy-request';

export interface StreamProxyHandler {
  /**
   * Called with a fully prepared request (`resolved`, `ticket`, `upstreamBody` with `stream_options.include_usage` set,
   * `timeouts`, `maxAttempts`). Throw a `GatewayError` for anything that happens BEFORE the first byte: the service
   * settles the request from it. After the first byte the handler owns the response and the settle, and never throws.
   */
  handle(request: ProxyRequest, loop: AttemptLoop): Promise<unknown>;
}

/** Injection token of the {@link StreamProxyHandler}. */
export const GATEWAY_STREAM_HANDLER = Symbol('LOCALAI_GATEWAY_STREAM_HANDLER');
