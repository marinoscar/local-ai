// The proxy of the OpenAI-compatible gateway (docs/specs/gateway.md section 4.1, from "resolve model" on):
//
//   resolve model + scope -> limits (GW-08 hook) -> body rewrite -> attempt loop -> non-stream passthrough -> usage
//
// `handle()` is the one entry the route handlers call. Streams (`stream: true`) go through the same preparation and are
// then delegated to the `StreamProxyHandler` (GW-07). Every request, whatever way it ends, is settled exactly once
// (`ProxyRequest.settle`): the usage record, the limits ticket and the log fields.
import { Inject, Injectable, Logger } from '@nestjs/common';

import { AttemptLoop, RequestAbortReason } from './attempts';
import { clientWantsUsageChunk, serialiseBody } from './body-rewrite';
import { GatewayError } from './errors';
import { GATEWAY_LIMITS, type GatewayLimits } from './limits';
import { GATEWAY_MODEL_RESOLVER, type GatewayModelResolver } from './model-resolver';
import { ProxyRequest, PROXY_ENDPOINTS, type ProxyEndpointName } from './proxy-request';
import { GATEWAY_PROXY_SETTINGS, type ProxySettingsReader } from './proxy-settings';
import type { GatewayRequestContext } from './routes';
import { GATEWAY_STREAM_HANDLER, type StreamProxyHandler } from './stream-handler';
import { UpstreamBodyError, UpstreamBodyTooLargeError, upstreamRejectionError } from './upstream/errors';
import type { UpstreamResponse } from './upstream/client';
import { GATEWAY_USAGE_SINK, parseUsage, type UsageSink } from './usage/usage-sink';

/** A non-stream upstream body larger than this is refused (spec section 4.2). */
export const MAX_UPSTREAM_BODY_BYTES = 32 * 1024 * 1024;

/** Status sent when the client left before the gateway could answer (nginx's convention). */
export const CLIENT_CLOSED_STATUS = 499;

/** Reads a whole upstream body up to `cap` bytes. Throws {@link UpstreamBodyTooLargeError} or {@link UpstreamBodyError}. */
export async function readUpstreamBody(response: UpstreamResponse, cap: number): Promise<Buffer> {
  const chunks: Buffer[] = [];
  let size = 0;
  try {
    for await (const chunk of response.body) {
      const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk as Uint8Array);
      size += buffer.length;
      if (size > cap) {
        response.destroy();
        throw new UpstreamBodyTooLargeError(cap);
      }
      chunks.push(buffer);
    }
  } catch (error) {
    if (error instanceof UpstreamBodyTooLargeError) throw error;
    const code = typeof error === 'object' && error !== null ? (error as { code?: unknown }).code : undefined;
    throw new UpstreamBodyError(code === 'UND_ERR_BODY_TIMEOUT');
  }
  return Buffer.concat(chunks);
}

@Injectable()
export class GatewayService {
  private readonly logger = new Logger(GatewayService.name);

  constructor(
    @Inject(GATEWAY_MODEL_RESOLVER) private readonly resolver: GatewayModelResolver,
    @Inject(GATEWAY_LIMITS) private readonly limits: GatewayLimits,
    @Inject(GATEWAY_USAGE_SINK) private readonly usage: UsageSink,
    @Inject(GATEWAY_STREAM_HANDLER) private readonly streams: StreamProxyHandler,
    @Inject(GATEWAY_PROXY_SETTINGS) private readonly settings: ProxySettingsReader,
    private readonly loop: AttemptLoop,
  ) {}

  /** Handles one inference request. Resolves to the reply (already sent) or throws a `GatewayError` for the route's error handler. */
  async handle(endpoint: ProxyEndpointName, ctx: GatewayRequestContext): Promise<unknown> {
    const spec = PROXY_ENDPOINTS[endpoint];
    if (!ctx.body) throw new GatewayError('LAI-GW-052');
    const { model: requestedModel, body } = ctx.body;
    const stream = body.stream === true;

    const request = new ProxyRequest({ ctx, spec, stream, requestedModel, usage: this.usage });
    try {
      if (stream && !spec.supportsStream) {
        throw new GatewayError('LAI-GW-003', { param: 'stream', message: 'This endpoint does not support "stream": true.' });
      }
      const resolved = await this.resolver.resolve(requestedModel, ctx.principal.key);
      request.resolved = resolved;
      ctx.log.model = resolved.modelId;
      if (endpoint === 'embeddings' && resolved.roles !== null && !resolved.roles.includes('embedding')) {
        throw new GatewayError('LAI-GW-020', { param: 'model', message: 'That model does not produce embeddings. Pick an embedding model from GET /v1/models.' });
      }

      request.ticket = await this.limits.admit({ ctx, modelId: resolved.modelId, endpoint: spec.name, stream });
      for (const [name, value] of Object.entries(request.ticket.headers())) void ctx.reply.header(name, value);

      const settings = await this.settings.read();
      request.maxAttempts = settings.maxAttempts;
      request.timeouts = {
        connectMs: settings.timeouts.connectMs,
        firstByteMs: resolved.policy.firstByteTimeoutS !== null ? resolved.policy.firstByteTimeoutS * 1000 : settings.timeouts.firstByteMs,
        idleMs: settings.timeouts.idleMs,
      };
      request.clientWantsUsageChunk = clientWantsUsageChunk(body);
      request.upstreamBody = serialiseBody(body, { modelId: resolved.modelId, stream, forceUsageChunk: spec.forceUsageChunk });

      if (stream) return await this.streams.handle(request, this.loop);
      request.startTotalTimer(settings.timeouts.nonStreamTotalMs);
      return await this.nonStream(request);
    } catch (error) {
      this.settleError(request, error);
      throw error;
    }
  }

  private async nonStream(request: ProxyRequest): Promise<unknown> {
    const { ctx } = request;
    const resolved = request.resolved!;
    const outcome = await this.loop.run({
      requestId: request.requestId,
      model: resolved,
      path: request.spec.upstreamPath,
      body: request.upstreamBody,
      accept: 'application/json',
      maxAttempts: request.maxAttempts,
      timeouts: request.timeouts,
      signal: request.signal,
    });
    request.note({ nodeId: outcome.ok ? outcome.candidate.nodeId : outcome.nodeId, attempts: outcome.attempts });

    if (!outcome.ok) {
      this.setNodeHeaders(request);
      if (outcome.reason === 'aborted') {
        request.settle({ status: CLIENT_CLOSED_STATUS, errorCode: null, aborted: true, promptTokens: null, completionTokens: null, usageSource: 'none' });
        return ctx.reply.code(CLIENT_CLOSED_STATUS).send();
      }
      throw outcome.error;
    }

    request.note({ ttfbMs: outcome.ttfbMs });
    this.setNodeHeaders(request);
    if (outcome.kind === 'rejected') throw upstreamRejectionError(outcome.status, outcome.bodyText);

    const { response } = outcome;
    let payload: Buffer;
    try {
      payload = await readUpstreamBody(response, MAX_UPSTREAM_BODY_BYTES);
    } catch (error) {
      if (request.signal.aborted) return this.abortedWhileReading(request);
      if (error instanceof UpstreamBodyTooLargeError) throw new GatewayError('LAI-GW-041', { message: 'The node answered with more data than the gateway accepts.' });
      throw new GatewayError(error instanceof UpstreamBodyError && error.timedOut ? 'LAI-GW-044' : 'LAI-GW-041');
    } finally {
      outcome.release();
      response.destroy();
    }

    const usage = this.usageOf(payload, response.contentType);
    request.settle({
      status: response.status,
      errorCode: null,
      aborted: false,
      promptTokens: usage?.promptTokens ?? null,
      completionTokens: usage?.completionTokens ?? null,
      usageSource: usage ? 'upstream' : 'none',
    });
    return ctx.reply
      .code(response.status)
      .header('content-type', response.contentType ?? 'application/json; charset=utf-8')
      .send(payload);
  }

  /** The abort fired while the body was being read: the client left (499), or the total time ran out (504 LAI-GW-044). */
  private abortedWhileReading(request: ProxyRequest): unknown {
    const reason: unknown = request.signal.reason;
    if (reason instanceof RequestAbortReason && reason.kind === 'total') throw new GatewayError('LAI-GW-044');
    request.settle({ status: CLIENT_CLOSED_STATUS, errorCode: null, aborted: true, promptTokens: null, completionTokens: null, usageSource: 'none' });
    return request.ctx.reply.code(CLIENT_CLOSED_STATUS).send();
  }

  private usageOf(payload: Buffer, contentType: string | null) {
    if (contentType !== null && !/json/i.test(contentType)) return null;
    try {
      return parseUsage(JSON.parse(payload.toString('utf8')));
    } catch {
      return null;
    }
  }

  /** `x-localai-node` and `x-localai-attempts`, on answers and on errors alike (node ids are not secret). */
  private setNodeHeaders(request: ProxyRequest): void {
    const { reply } = request.ctx;
    if (request.progress.nodeId) void reply.header('x-localai-node', request.progress.nodeId);
    if (request.progress.attempts > 0) void reply.header('x-localai-attempts', String(request.progress.attempts));
  }

  private settleError(request: ProxyRequest, error: unknown): void {
    if (error instanceof GatewayError) {
      request.settle({ status: error.status, errorCode: error.localaiCode, aborted: false, promptTokens: null, completionTokens: null, usageSource: 'none' });
      return;
    }
    this.logger.warn(`unexpected error while proxying (request ${request.requestId}): ${error instanceof Error ? error.name : 'unknown'}`);
    request.settle({ status: 500, errorCode: 'LAI-GW-052', aborted: false, promptTokens: null, completionTokens: null, usageSource: 'none' });
  }
}
