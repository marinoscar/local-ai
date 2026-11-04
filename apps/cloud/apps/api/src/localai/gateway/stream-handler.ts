// The seam for SSE passthrough (GW-07). `GatewayService` prepares every request the same way (model, scope, limits, body
// rewrite, timeouts) and then hands a `stream: true` request to this handler instead of reading the upstream body
// itself. GW-07 implements it: run `loop.run(...)`, and on a committed attempt hijack the reply and pipe the events
// (docs/specs/gateway.md section 4.2), then call `request.settle(...)` and `committed.release()` when the stream ends.
// Until then the default answers 400 LAI-GW-003 (`unsupported_parameter`).
import type { AttemptLoop } from './attempts';
import { GatewayError } from './errors';
import type { ProxyRequest } from './proxy-request';

export interface StreamProxyHandler {
  /**
   * Called with a fully prepared request (`resolved`, `ticket`, `upstreamBody` with `stream_options.include_usage` set,
   * `timeouts`, `maxAttempts`). Throw a `GatewayError` for anything that happens BEFORE the first byte: the service
   * settles the request from it. After the first byte the handler owns the response and the settle.
   */
  handle(request: ProxyRequest, loop: AttemptLoop): Promise<unknown>;
}

/** Injection token of the {@link StreamProxyHandler}. */
export const GATEWAY_STREAM_HANDLER = Symbol('LOCALAI_GATEWAY_STREAM_HANDLER');

/** Until GW-07. */
export const streamingNotAvailable: StreamProxyHandler = {
  async handle() {
    throw new GatewayError('LAI-GW-003', { param: 'stream', message: 'Streaming responses are not available on this gateway yet. Send "stream": false.' });
  },
};
