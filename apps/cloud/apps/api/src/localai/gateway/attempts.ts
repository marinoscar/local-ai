// The attempt loop (docs/specs/gateway.md section 4.2): try candidate nodes in the selection's order until one answers,
// retrying on ANOTHER node as long as nothing has been sent to the client. It stops being able to retry at the first
// byte: the first 2xx status commits, and the caller owns the live body from then on.
//
//   attempt(i) on candidate c:
//     no key / connect or TLS failure / headers timeout        -> retry
//     5xx (and any non-2xx, non-4xx)                           -> retry, node suspect 30 s
//     401                                                      -> retry, node keyInvalid (LAI-GW-043 if all fail)
//     400/404 "model not found | not loaded | unloaded"         -> retry, (node, model) marked unloaded
//     any other 4xx                                            -> final: the answer (LAI-GW-042)
//     2xx                                                      -> commit
//
// SEAMS for the items that build on this one (everything is an interface, nothing here reads a Nest container):
//   * `CandidateSource`  GW-09: where the next candidate comes from. The default asks the routing table; the on-demand
//                        load wraps it, waits for the model to load and asks again.
//   * `AttemptHooks`     GW-07/GW-08: `beforeAttempt`, `onAttemptFailed`, `onFirstByte` (the stream passthrough and
//                        the usage recorder observe the loop without being part of it).
//   * `UpstreamTransport` GW-10: the tailnet mode (and `routable()`, below).
// ELIGIBILITY (GW-10). A candidate the request cannot use is skipped WITHOUT being an attempt: the transport's
// `routable()` (a node with no tailnet address in tailnet mode) and the request's own `eligible()` (a transcription goes
// to models that accept audio). The skipped node ids are added to `tried` for the source only, so the source offers the
// next one; `tried.size` (the attempt count) never includes them.
// `inflight` is counted from the moment a candidate is chosen: released here on every failed attempt, and by the
// `release()` of a committed attempt when the caller's response ends.
import type { Readable } from 'node:stream';

import { GatewayError } from './errors';
import type { ResolvedGatewayModel } from './model-resolver';
import type { CandidateRef, RoutingPolicy, SelectedCandidate, SelectResult, UnavailableOutcome } from './routing/routing.types';
import type { GatewayTimeouts } from './proxy-settings';
import {
  aggregateFailures,
  classifyStatus,
  UpstreamAbortedError,
  UpstreamTransportError,
  type AttemptFailure,
  type AttemptFailureReason,
} from './upstream/errors';
import type { UpstreamResponse, UpstreamTransport } from './upstream/client';

/** Why a request's `AbortSignal` fired: the client went away, or the total time ran out. */
export class RequestAbortReason {
  constructor(readonly kind: 'client' | 'total') {}
}

/** An error body of a node is read (and a 5xx body drained) up to this many bytes. */
export const ERROR_BODY_CAP_BYTES = 64 * 1024;

/** The part of the routing table the loop uses; `RoutingTable` satisfies it. */
export interface RoutingPort {
  select(modelId: string, policy: RoutingPolicy, tried: ReadonlySet<string>): SelectResult;
  unavailable(modelId: string, inApprovedCatalog: boolean): UnavailableOutcome;
  acquire(ref: CandidateRef): void;
  release(ref: CandidateRef): void;
  markSuspect(ref: CandidateRef): void;
  markKeyInvalid(ref: Pick<CandidateRef, 'nodeId'>): void;
  markUnloaded(nodeId: string, modelId: string): void;
  recordTtfb(ref: CandidateRef, ms: number): void;
}

/** The part of `NodeKeyProvider` the loop uses. */
export interface NodeKeySource {
  get(nodeId: string): Promise<string | null>;
}

export interface CandidateRequest {
  model: ResolvedGatewayModel;
  /** Node ids already used by this request. */
  tried: ReadonlySet<string>;
  /** 1-based number of the attempt about to start. */
  attempt: number;
  failures: readonly AttemptFailure[];
  signal: AbortSignal;
}

export type CandidateChoice =
  | { kind: 'candidate'; candidate: SelectedCandidate }
  /** Stop. `error: null` means "report the failures so far" (`aggregateFailures`). */
  | { kind: 'none'; error: GatewayError | null };

export interface CandidateSource {
  next(request: CandidateRequest): Promise<CandidateChoice>;
}

/**
 * Steps 1, 2 and 4 of the selection straight from the routing table. On the FIRST attempt "nothing loaded" and "all
 * saturated" are the answer (404/503 LAI-GW-020/021/023); after a failure they just mean "no node left", and the
 * request reports what went wrong with the nodes it did try.
 */
export class TableCandidateSource implements CandidateSource {
  constructor(private readonly routing: Pick<RoutingPort, 'select' | 'unavailable'>) {}

  async next(request: CandidateRequest): Promise<CandidateChoice> {
    const selected = this.routing.select(request.model.modelId, request.model.policy, request.tried);
    if (selected.kind === 'candidates') return { kind: 'candidate', candidate: selected.candidates[0]! };
    if (request.attempt > 1) return { kind: 'none', error: null };
    if (selected.kind === 'saturated') return { kind: 'none', error: new GatewayError('LAI-GW-023') };
    const outcome = this.routing.unavailable(request.model.modelId, request.model.inApprovedCatalog);
    return { kind: 'none', error: new GatewayError(outcome.code) };
  }
}

export interface AttemptEvent {
  attempt: number;
  candidate: SelectedCandidate;
}

export interface AttemptHooks {
  /** Before the call to a node (after the key was read). */
  beforeAttempt?(event: AttemptEvent): void | Promise<void>;
  /** After a failed attempt that will be retried or reported. */
  onAttemptFailed?(failure: AttemptFailure): void;
  /** The first byte arrived (a 2xx status): no more retries. */
  onFirstByte?(event: AttemptEvent & { ttfbMs: number; status: number }): void;
}

/** What one run needs. */
export interface AttemptRequest {
  requestId: string;
  model: ResolvedGatewayModel;
  /** Upstream path, e.g. `/v1/chat/completions`. */
  path: string;
  body: Buffer;
  accept: string;
  /** The request's `Content-Type`; `application/json` when absent. A multipart body carries its own boundary here. */
  contentType?: string;
  /** Candidates this request can use at all (GW-10: audio-capable for a transcription). Absent means every candidate. */
  eligible?(candidate: SelectedCandidate): boolean;
  maxAttempts: number;
  /** `firstByteMs` is already the model policy's override when there is one. */
  timeouts: Pick<GatewayTimeouts, 'connectMs' | 'firstByteMs' | 'idleMs'>;
  signal: AbortSignal;
}

export interface CommittedAttempt {
  ok: true;
  kind: 'success';
  candidate: SelectedCandidate;
  attempts: number;
  response: UpstreamResponse;
  ttfbMs: number;
  /** Ends the `inflight` count of the node. Call it (once) when the response has ended. Idempotent. */
  release(): void;
}

export interface RejectedAttempt {
  ok: true;
  kind: 'rejected';
  candidate: SelectedCandidate;
  attempts: number;
  /** The upstream 4xx (not 401, not "model not loaded"): LAI-GW-042 unless the caller does something else. */
  status: number;
  bodyText: string;
  ttfbMs: number;
}

export interface FailedAttempts {
  ok: false;
  reason: 'error';
  error: GatewayError;
  attempts: number;
  /** The last node tried, if any. */
  nodeId: string | null;
  failures: readonly AttemptFailure[];
}

export interface AbortedAttempts {
  ok: false;
  reason: 'aborted';
  attempts: number;
  nodeId: string | null;
  failures: readonly AttemptFailure[];
}

export type AttemptOutcome = CommittedAttempt | RejectedAttempt | FailedAttempts | AbortedAttempts;

export interface AttemptLoopDeps {
  routing: RoutingPort;
  keys: NodeKeySource;
  transport: UpstreamTransport;
  source?: CandidateSource;
  hooks?: AttemptHooks;
  /** Per-attempt failure lines (warn). */
  log: { warn(line: string): void };
}

/** Reads at most `cap` bytes of a stream as UTF-8 text and then destroys it. Never throws. */
export async function readCapped(stream: Readable, cap: number): Promise<string> {
  const chunks: Buffer[] = [];
  let size = 0;
  try {
    for await (const chunk of stream) {
      const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk as Uint8Array);
      chunks.push(buffer.subarray(0, Math.max(0, cap - size)));
      size += buffer.length;
      if (size >= cap) break;
    }
  } catch {
    // A body that breaks off is read as far as it got: the status already decided what happens next.
  } finally {
    if (!stream.destroyed) stream.destroy();
  }
  return Buffer.concat(chunks).toString('utf8');
}

export class AttemptLoop {
  private readonly source: CandidateSource;

  constructor(private readonly deps: AttemptLoopDeps) {
    this.source = deps.source ?? new TableCandidateSource(deps.routing);
  }

  async run(request: AttemptRequest): Promise<AttemptOutcome> {
    const { routing } = this.deps;
    const tried = new Set<string>();
    const failures: AttemptFailure[] = [];
    let lastNode: string | null = null;

    const aborted = (): AbortedAttempts | FailedAttempts => {
      const reason: unknown = request.signal.reason;
      if (reason instanceof RequestAbortReason && reason.kind === 'total') {
        return { ok: false, reason: 'error', error: new GatewayError('LAI-GW-044'), attempts: tried.size, nodeId: lastNode, failures };
      }
      return { ok: false, reason: 'aborted', attempts: tried.size, nodeId: lastNode, failures };
    };

    for (let attempt = 1; attempt <= request.maxAttempts; attempt += 1) {
      if (request.signal.aborted) return aborted();

      const choice = await this.nextEligible(request, tried, attempt, failures);
      if (request.signal.aborted) return aborted();
      if (choice.kind === 'none') {
        const error = choice.error ?? aggregateFailures(failures);
        return { ok: false, reason: 'error', error, attempts: tried.size, nodeId: lastNode, failures };
      }

      const candidate = choice.candidate;
      const ref: CandidateRef = { nodeId: candidate.nodeId, modelId: request.model.modelId };
      tried.add(candidate.nodeId);
      lastNode = candidate.nodeId;
      routing.acquire(ref);
      let held = true;
      const release = (): void => {
        if (!held) return;
        held = false;
        routing.release(ref);
      };
      const fail = (reason: AttemptFailureReason, upstreamStatus: number | null): void => {
        const failure: AttemptFailure = { nodeId: candidate.nodeId, attempt, reason, upstreamStatus };
        failures.push(failure);
        this.deps.log.warn(
          JSON.stringify({ event: 'gateway.attempt_failed', request_id: request.requestId, node_id: failure.nodeId, attempt, reason, upstream_status: upstreamStatus }),
        );
        this.safeHook(() => this.deps.hooks?.onAttemptFailed?.(failure));
      };

      try {
        let apiKey: string | null;
        try {
          apiKey = await this.deps.keys.get(candidate.nodeId);
        } catch {
          // The credential store is unreadable: every node would fail the same way, so there is nothing to retry.
          release();
          return { ok: false, reason: 'error', error: new GatewayError('LAI-GW-050'), attempts: tried.size, nodeId: candidate.nodeId, failures };
        }
        if (apiKey === null) {
          // The key was removed since the last table rebuild: like a rejected key, until the node reports one again.
          routing.markKeyInvalid(ref);
          fail('status_401', null);
          release();
          continue;
        }

        await this.deps.hooks?.beforeAttempt?.({ attempt, candidate });

        let response: UpstreamResponse;
        try {
          response = await this.deps.transport.call({
            nodeId: candidate.nodeId,
            publicHost: candidate.publicHost,
            route: { servePattern: candidate.servePattern, tailnetIp: candidate.tailnetIp, tailnetMachine: candidate.tailnetMachine },
            path: request.path,
            body: request.body,
            contentType: request.contentType ?? 'application/json',
            accept: request.accept,
            requestId: request.requestId,
            apiKey,
            signal: request.signal,
            connectMs: request.timeouts.connectMs,
            firstByteMs: request.timeouts.firstByteMs,
            idleMs: request.timeouts.idleMs,
          });
        } catch (error) {
          if (error instanceof UpstreamAbortedError || request.signal.aborted) {
            release();
            return aborted();
          }
          const reason = error instanceof UpstreamTransportError ? error.reason : 'connect';
          fail(reason, null);
          routing.markSuspect(ref);
          release();
          continue;
        }

        if (classifyStatus(response.status, '').kind === 'success') {
          routing.recordTtfb(ref, response.ttfbMs);
          this.safeHook(() => this.deps.hooks?.onFirstByte?.({ attempt, candidate, ttfbMs: response.ttfbMs, status: response.status }));
          return { ok: true, kind: 'success', candidate, attempts: tried.size, response, ttfbMs: response.ttfbMs, release };
        }

        // Not a success: read what the node said (bounded) and drop the connection.
        const bodyText = await readCapped(response.body, ERROR_BODY_CAP_BYTES);
        if (request.signal.aborted) {
          release();
          return aborted();
        }
        const decision = classifyStatus(response.status, bodyText);
        if (decision.kind === 'final') {
          release();
          return { ok: true, kind: 'rejected', candidate, attempts: tried.size, status: response.status, bodyText, ttfbMs: response.ttfbMs };
        }
        if (decision.kind === 'retry') {
          fail(decision.reason, response.status);
          if (decision.reason === 'status_401') routing.markKeyInvalid(ref);
          else if (decision.reason === 'model_not_loaded') routing.markUnloaded(candidate.nodeId, request.model.modelId);
          else routing.markSuspect(ref);
          release();
          continue;
        }
        // `success` was returned above; this is unreachable, kept so a future verdict kind cannot fall through silently.
        fail('status_5xx', response.status);
        routing.markSuspect(ref);
        release();
      } catch (error) {
        release();
        throw error;
      }
    }

    return { ok: false, reason: 'error', error: aggregateFailures(failures), attempts: tried.size, nodeId: lastNode, failures };
  }

  /** The next candidate from the source that this request can use; the ones it cannot use are skipped, not attempted. */
  private async nextEligible(request: AttemptRequest, tried: ReadonlySet<string>, attempt: number, failures: readonly AttemptFailure[]): Promise<CandidateChoice> {
    const skipped = new Set<string>();
    for (;;) {
      const visible = skipped.size === 0 ? tried : new Set([...tried, ...skipped]);
      const choice = await this.source.next({ model: request.model, tried: visible, attempt, failures, signal: request.signal });
      if (choice.kind !== 'candidate') return choice;
      const candidate = choice.candidate;
      const usable =
        (request.eligible?.(candidate) ?? true) &&
        (await (this.deps.transport.routable?.({
          nodeId: candidate.nodeId,
          publicHost: candidate.publicHost,
          route: { servePattern: candidate.servePattern, tailnetIp: candidate.tailnetIp, tailnetMachine: candidate.tailnetMachine },
        }) ?? true));
      if (usable) return choice;
      skipped.add(candidate.nodeId);
      if (request.signal.aborted) return { kind: 'none', error: null };
    }
  }

  /** A hook is an observer: whatever it throws must never fail the request. */
  private safeHook(run: () => void): void {
    try {
      run();
    } catch {
      // swallowed on purpose
    }
  }
}
