// One proxied request, from the moment its body is parsed until its response has ended. It owns the things that must
// happen exactly once per request, whatever path the request takes (non-stream, stream, an error before any node):
//   * the `AbortController` (client gone -> upstream aborted; the total timer -> upstream aborted),
//   * the admission ticket of the limits (GW-08) and the usage record (GW-08), both settled once in `settle()`,
//   * the per-request log line fields.
// The non-stream path (`gateway.service.ts`) and the stream path (GW-07) both use it; neither re-implements any of this.
import type { GatewayRequestContext } from './routes';
import type { AdmissionTicket } from './limits';
import { RequestAbortReason } from './attempts';
import type { ResolvedGatewayModel } from './model-resolver';
import type { GatewayTimeouts } from './proxy-settings';
import type { UsageRecord, UsageSink, UsageSource } from './usage/usage-sink';

export type ProxyEndpointName = 'chat.completions' | 'completions' | 'embeddings';

export interface ProxyEndpointSpec {
  name: ProxyEndpointName;
  /** The path on the node (spec section 3.2). */
  upstreamPath: string;
  supportsStream: boolean;
  /** chat/completions only: the gateway asks the node for the final usage chunk of a stream. */
  forceUsageChunk: boolean;
}

export const PROXY_ENDPOINTS: Readonly<Record<ProxyEndpointName, ProxyEndpointSpec>> = {
  'chat.completions': { name: 'chat.completions', upstreamPath: '/v1/chat/completions', supportsStream: true, forceUsageChunk: true },
  completions: { name: 'completions', upstreamPath: '/v1/completions', supportsStream: true, forceUsageChunk: false },
  embeddings: { name: 'embeddings', upstreamPath: '/v1/embeddings', supportsStream: false, forceUsageChunk: false },
};

/** How a request ended; `ProxyRequest.settle` turns it into the usage record, the limits settle and the log fields. */
export interface ProxyOutcome {
  /** The HTTP status sent (499 when the client left before an answer; 200 for a stream that failed mid-way). */
  status: number;
  errorCode: string | null;
  aborted: boolean;
  promptTokens: number | null;
  completionTokens: number | null;
  usageSource: UsageSource;
}

/** What the attempt loop learned, noted as it goes so an error path can still report the node and the attempts. */
export interface ProxyProgress {
  nodeId: string | null;
  attempts: number;
  ttfbMs: number | null;
}

export interface ProxyRequestInit {
  ctx: GatewayRequestContext;
  spec: ProxyEndpointSpec;
  stream: boolean;
  /** The model exactly as the client sent it. */
  requestedModel: string;
  usage: UsageSink;
  now?: () => number;
}

export class ProxyRequest {
  readonly ctx: GatewayRequestContext;
  readonly spec: ProxyEndpointSpec;
  readonly stream: boolean;
  readonly requestedModel: string;
  readonly abort = new AbortController();
  readonly progress: ProxyProgress = { nodeId: null, attempts: 0, ttfbMs: null };

  /** Set by the service once the model is resolved / the limits admit the request / the body is rewritten. */
  resolved: ResolvedGatewayModel | null = null;
  ticket: AdmissionTicket | null = null;
  upstreamBody: Buffer = Buffer.alloc(0);
  timeouts!: Pick<GatewayTimeouts, 'connectMs' | 'firstByteMs' | 'idleMs'>;
  maxAttempts = 1;
  /** Did the client itself ask for `stream_options.include_usage`? GW-07 forwards the usage chunk only then. */
  clientWantsUsageChunk = false;

  private readonly usage: UsageSink;
  private readonly now: () => number;
  private readonly startedAt: number;
  private totalTimer: ReturnType<typeof setTimeout> | null = null;
  private settled = false;
  private readonly onClose: () => void;

  constructor(init: ProxyRequestInit) {
    this.ctx = init.ctx;
    this.spec = init.spec;
    this.stream = init.stream;
    this.requestedModel = init.requestedModel;
    this.usage = init.usage;
    this.now = init.now ?? Date.now;
    this.startedAt = this.now();
    this.ctx.log.endpoint = init.spec.name;
    this.ctx.log.stream = init.stream;
    this.ctx.log.requested_model = init.requestedModel;

    // Client abort -> upstream abort. `writableFinished` is true once the full response was written, so a normal
    // end of the response (which also closes the socket) is not an abort.
    const raw = this.ctx.reply.raw;
    this.onClose = () => {
      if (!raw.writableFinished && !this.abort.signal.aborted) this.abort.abort(new RequestAbortReason('client'));
    };
    raw.on('close', this.onClose);
  }

  get signal(): AbortSignal {
    return this.abort.signal;
  }

  get requestId(): string {
    return this.ctx.requestId;
  }

  /** Aborts the upstream call with the `total` reason once `ms` have passed (`nonStreamTotalMs` here, `totalMs` for GW-07). */
  startTotalTimer(ms: number): void {
    this.clearTotalTimer();
    this.totalTimer = setTimeout(() => {
      if (!this.abort.signal.aborted) this.abort.abort(new RequestAbortReason('total'));
    }, ms);
    this.totalTimer.unref?.();
  }

  clearTotalTimer(): void {
    if (this.totalTimer) clearTimeout(this.totalTimer);
    this.totalTimer = null;
  }

  note(progress: Partial<ProxyProgress>): void {
    Object.assign(this.progress, progress);
  }

  /** True once the client's socket went away before the response ended. */
  get clientGone(): boolean {
    const reason: unknown = this.abort.signal.reason;
    return this.abort.signal.aborted && reason instanceof RequestAbortReason && reason.kind === 'client';
  }

  /**
   * Ends the request: the usage record goes to the sink, the admission ticket is settled, the log fields are filled.
   * Idempotent (the first call wins) and never throws: a faulty sink or ticket cannot fail a response.
   */
  settle(outcome: ProxyOutcome): void {
    if (this.settled) return;
    this.settled = true;
    this.clearTotalTimer();
    this.ctx.reply.raw.off('close', this.onClose);

    const durationMs = Math.max(0, Math.round(this.now() - this.startedAt));
    const record: UsageRecord = {
      requestId: this.ctx.requestId,
      keyId: this.ctx.principal?.key.id ?? null,
      userId: this.ctx.principal?.userId ?? null,
      endpoint: this.spec.name,
      model: this.resolved?.modelId ?? null,
      requestedModel: this.requestedModel,
      nodeId: this.progress.nodeId,
      status: outcome.status,
      errorCode: outcome.errorCode,
      stream: this.stream,
      aborted: outcome.aborted,
      attempts: Math.max(1, this.progress.attempts),
      promptTokens: outcome.promptTokens,
      completionTokens: outcome.completionTokens,
      usageSource: outcome.usageSource,
      ttfbMs: this.progress.ttfbMs,
      durationMs,
    };

    const log = this.ctx.log;
    log.model = record.model;
    log.node_id = record.nodeId;
    log.attempts = record.attempts;
    log.status = record.status;
    log.aborted = record.aborted;
    log.ttfb_ms = record.ttfbMs;
    log.duration_ms = durationMs;
    log.prompt_tokens = record.promptTokens;
    log.completion_tokens = record.completionTokens;
    log.usage_source = record.usageSource;
    log.error_code = record.errorCode;

    try {
      this.usage.record(record);
    } catch {
      // never fails the response
    }
    try {
      this.ticket?.settle({ promptTokens: record.promptTokens, completionTokens: record.completionTokens, aborted: outcome.aborted });
    } catch {
      // never fails the response
    }
  }
}
