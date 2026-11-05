// The SSE passthrough of `stream: true` requests (docs/specs/gateway.md section 4.2, Stream). It is the
// `StreamProxyHandler` bound to `GATEWAY_STREAM_HANDLER`:
//
//   attempt loop (retries on another node until the first 2xx status, GW-06) -> hijack -> pump events -> settle once
//
// WHAT IT GUARANTEES
//   * No buffering: each complete SSE event is written to the client the moment it is complete; only the partial tail of
//     an unfinished event waits, and only for its own remaining bytes (`SseSplitter`).
//   * Backpressure: when the client's socket does not keep up (`write()` returns false) the upstream body is paused until
//     `drain`, so undici stops reading the node's socket and the node's own writes block. Memory stays at one chunk.
//   * Abort: the client leaving, the idle timer and the total timer all end in the same place; the upstream request is
//     aborted (the node frees the slot) and the usage record says `aborted` or carries the error code.
//   * One end: after `hijack()` Fastify sends nothing and handles no error, so EVERY path ends `reply.raw` exactly once,
//     settles the request exactly once and releases the node's `inflight` count, and none of them throws.
//   * After the first byte there is no retry and no JSON error: a failure is one final SSE error event and `[DONE]`.
import type { OutgoingHttpHeaders, ServerResponse } from 'node:http';
import type { Readable } from 'node:stream';

import { Logger } from '@nestjs/common';

import { RequestAbortReason, type AttemptLoop, type CommittedAttempt } from '../attempts';
import { GatewayError } from '../errors';
import { CLIENT_CLOSED_STATUS } from '../gateway.service';
import type { ProxyRequest } from '../proxy-request';
import type { StreamProxyHandler } from '../stream-handler';
import { upstreamRejectionError } from '../upstream/errors';
import { SseSplitter, DEFAULT_MAX_EVENT_BYTES } from './sse-splitter';
import { StreamUsageTracker } from './usage-from-events';

/**
 * The event sent when the node fails or goes quiet after the first byte (spec section 3.3, exact bytes), followed by the
 * `[DONE]` every OpenAI client waits for. The same bytes for an idle timeout, the total timeout and a broken node; the
 * usage row tells them apart (LAI-GW-044 for the timeouts, LAI-GW-045 for the rest).
 */
export const MIDSTREAM_ERROR_EVENT = Buffer.from(
  'data: {"error":{"message":"The node stopped answering in the middle of the reply.","type":"server_error","code":"upstream_interrupted","localai_code":"LAI-GW-045"}}\n\n' +
    'data: [DONE]\n\n',
  'utf8',
);

/** How long the rest of a finished upstream body (the chunked terminator after `[DONE]`) may take before the socket is dropped. */
export const TAIL_DRAIN_MS = 250;

/** How a stream ended. */
export type StreamEnd = 'done' | 'client' | 'timeout' | 'interrupted';

export interface StreamProxyOptions {
  /** The most bytes one SSE event may take (default 1 MiB). */
  maxEventBytes?: number;
}

/** A timer that is restarted on every chunk and stopped while the client, not the node, is the slow side. */
class IdleTimer {
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly ms: number,
    private readonly onExpire: () => void,
  ) {}

  touch(): void {
    this.stop();
    this.timer = setTimeout(this.onExpire, this.ms);
    this.timer.unref?.();
  }

  stop(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
  }
}

const isBodyTimeout = (error: unknown): boolean => typeof error === 'object' && error !== null && (error as { code?: unknown }).code === 'UND_ERR_BODY_TIMEOUT';

/** The end a fired abort signal means: the total time ran out, or the client left. */
function endOfAbort(signal: AbortSignal): StreamEnd {
  const reason: unknown = signal.reason;
  return reason instanceof RequestAbortReason && reason.kind === 'total' ? 'timeout' : 'client';
}

/** Ends `raw` once, with an optional last piece; never throws. */
function endResponse(raw: ServerResponse, last?: Buffer): void {
  try {
    if (!raw.writableEnded && !raw.destroyed) raw.end(last);
  } catch {
    // the socket is gone: nothing left to end
  }
}

/** Reads what is left of a finished body for a moment so the connection can go back to the pool, then drops it. */
function drainThenDestroy(body: Readable, ms: number): Promise<void> {
  return new Promise<void>((resolve) => {
    if (body.destroyed || body.readableEnded) {
      resolve();
      return;
    }
    const timer = setTimeout(done, ms);
    function done(): void {
      clearTimeout(timer);
      if (!body.destroyed) body.destroy();
      resolve();
    }
    body.once('end', done);
    body.once('close', done);
    body.once('error', done);
    body.resume();
  });
}

export class StreamProxy implements StreamProxyHandler {
  private readonly logger = new Logger(StreamProxy.name);
  private readonly maxEventBytes: number;

  constructor(options: StreamProxyOptions = {}) {
    this.maxEventBytes = options.maxEventBytes ?? DEFAULT_MAX_EVENT_BYTES;
  }

  async handle(request: ProxyRequest, loop: AttemptLoop): Promise<unknown> {
    const { ctx } = request;
    // The whole request, attempts included, is bounded by `totalMs`; the abort carries the `total` reason.
    request.startTotalTimer(request.timeouts.totalMs);
    const outcome = await loop.run({
      requestId: request.requestId,
      model: request.resolved!,
      path: request.spec.upstreamPath,
      body: request.upstreamBody,
      accept: 'text/event-stream',
      maxAttempts: request.maxAttempts,
      timeouts: request.timeouts,
      signal: request.signal,
    });
    request.note({ nodeId: outcome.ok ? outcome.candidate.nodeId : outcome.nodeId, attempts: outcome.attempts });
    this.setNodeHeaders(request);

    // Before the first byte everything is an ordinary JSON error (the service settles a thrown GatewayError).
    if (!outcome.ok) {
      if (outcome.reason === 'aborted') {
        request.settle({ status: CLIENT_CLOSED_STATUS, errorCode: null, aborted: true, promptTokens: null, completionTokens: null, usageSource: 'none' });
        return ctx.reply.code(CLIENT_CLOSED_STATUS).send();
      }
      throw outcome.error;
    }
    request.note({ ttfbMs: outcome.ttfbMs });
    if (outcome.kind === 'rejected') throw upstreamRejectionError(outcome.status, outcome.bodyText);

    // The client may have left (or the total time run out) between the node's status line and now.
    if (request.signal.aborted) {
      const end = endOfAbort(request.signal);
      outcome.release();
      outcome.response.destroy();
      if (end === 'timeout') throw new GatewayError('LAI-GW-044');
      request.settle({ status: CLIENT_CLOSED_STATUS, errorCode: null, aborted: true, promptTokens: null, completionTokens: null, usageSource: 'none' });
      return ctx.reply.code(CLIENT_CLOSED_STATUS).send();
    }

    await this.passThrough(request, outcome);
    return undefined;
  }

  /** From the first byte on. Never throws: the response is hijacked and nobody else would end it. */
  private async passThrough(request: ProxyRequest, outcome: CommittedAttempt): Promise<void> {
    const { ctx } = request;
    const raw = ctx.reply.raw;
    const { response } = outcome;
    const tracker = new StreamUsageTracker(request.clientWantsUsageChunk);
    let end: StreamEnd = 'interrupted';
    let sentHead = false;

    try {
      ctx.reply.hijack();
      // A write after the socket broke must not become an uncaught 'error' event.
      raw.on('error', () => undefined);
      raw.socket?.setNoDelay(true);

      const headers: OutgoingHttpHeaders = { ...(ctx.reply.getHeaders() as OutgoingHttpHeaders) };
      delete headers['content-length'];
      delete headers['content-encoding'];
      delete headers['transfer-encoding'];
      raw.writeHead(200, { ...headers, 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache', 'x-accel-buffering': 'no' });
      raw.flushHeaders();
      sentHead = true;

      end = await this.pump(request, response.body, tracker, new SseSplitter({ maxEventBytes: this.maxEventBytes }));
    } catch (error) {
      this.logger.warn(`unexpected error while streaming (request ${request.requestId}): ${error instanceof Error ? error.name : 'unknown'}`);
      end = 'interrupted';
    }

    // Settle first: the log fields must be complete before `raw.end()` lets Fastify's onResponse hook write the line.
    if (!sentHead) {
      // The head could not be written (the socket was already gone): nothing was sent and nobody can be told.
      request.settle({ status: 500, errorCode: 'LAI-GW-052', aborted: false, promptTokens: null, completionTokens: null, usageSource: 'none' });
      if (!raw.destroyed) raw.destroy();
      outcome.release();
      response.destroy();
      return;
    }
    request.settle({
      status: 200,
      errorCode: end === 'timeout' ? 'LAI-GW-044' : end === 'interrupted' ? 'LAI-GW-045' : null,
      aborted: end === 'client',
      ...tracker.result(),
    });
    if (end === 'done') endResponse(raw);
    else if (end === 'timeout' || end === 'interrupted') endResponse(raw, MIDSTREAM_ERROR_EVENT);
    else if (!raw.destroyed && !raw.writableEnded) raw.destroy();

    outcome.release();
    try {
      if (end === 'done') await drainThenDestroy(response.body, TAIL_DRAIN_MS);
    } finally {
      response.destroy();
    }
  }

  /**
   * Moves the node's events to the client until the stream ends, one way or another. Resolves exactly once and never
   * rejects. It does not touch the response's end: the caller decides what is still to be written.
   */
  private pump(request: ProxyRequest, body: Readable, tracker: StreamUsageTracker, splitter: SseSplitter): Promise<StreamEnd> {
    const raw = request.ctx.reply.raw;
    const { signal } = request;

    return new Promise<StreamEnd>((resolve) => {
      let ended = false;
      const idle = new IdleTimer(request.timeouts.idleMs, () => finish('timeout'));

      const finish = (result: StreamEnd): void => {
        if (ended) return;
        ended = true;
        idle.stop();
        signal.removeEventListener('abort', onAbort);
        raw.off('drain', onDrain);
        body.off('data', onData);
        splitter.reset();
        resolve(result);
      };

      const onAbort = (): void => finish(endOfAbort(signal));

      const onDrain = (): void => {
        if (ended) return;
        idle.touch();
        body.resume();
      };

      const onData = (chunk: Buffer | Uint8Array | string): void => {
        if (ended) return;
        try {
          idle.touch();
          const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk as Uint8Array);
          let blocked = false;
          for (const event of splitter.push(bytes)) {
            const verdict = tracker.inspect(event);
            if (verdict.forward && !raw.write(event)) blocked = true;
            if (verdict.done) {
              finish('done');
              return;
            }
          }
          if (blocked) {
            // The client is the slow side: stop reading the node (undici then pauses its socket) and stop the idle clock.
            body.pause();
            idle.stop();
            raw.once('drain', onDrain);
          }
        } catch (error) {
          this.logger.warn(`could not pass an event on (request ${request.requestId}): ${error instanceof Error ? error.name : 'unknown'}`);
          finish('interrupted');
        }
      };

      // Permanent: a body destroyed later (after `finish`) must not raise an unhandled 'error'.
      body.on('error', (error: unknown) => finish(signal.aborted ? endOfAbort(signal) : isBodyTimeout(error) ? 'timeout' : 'interrupted'));
      body.on('end', () => finish(tracker.finished ? 'done' : 'interrupted'));
      body.on('close', () => finish(tracker.finished ? 'done' : signal.aborted ? endOfAbort(signal) : 'interrupted'));
      body.on('data', onData);

      if (signal.aborted) {
        finish(endOfAbort(signal));
        return;
      }
      signal.addEventListener('abort', onAbort, { once: true });
      idle.touch();
    });
  }

  /** `x-localai-node` and `x-localai-attempts`, set on the Fastify reply (the head copies them when it is written). */
  private setNodeHeaders(request: ProxyRequest): void {
    const { reply } = request.ctx;
    if (request.progress.nodeId) void reply.header('x-localai-node', request.progress.nodeId);
    if (request.progress.attempts > 0) void reply.header('x-localai-attempts', String(request.progress.attempts));
  }
}
