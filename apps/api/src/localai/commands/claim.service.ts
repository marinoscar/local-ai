// The claim long poll (docs/specs/cloud-app.md sections 3.6 and 4.4). A node asks for work; the cloud answers at
// once when something is queued for it, and otherwise HOLDS the request until a command is enqueued for that node,
// the wait ends, the client goes away or the server shuts down. An empty answer is the normal one.
//
// What this class owns, and what it deliberately does not hold:
//   * It holds NO database connection and NO transaction while it waits. Each attempt is one short transaction in
//     the repository (`claim.sql.ts`); between attempts the request only waits on a timer and an event.
//   * ONE in-flight claim per node (a second concurrent claim from the same node is answered empty at once, logged
//     as `claim_in_progress`), so a node that restarts and reconnects never piles requests up behind the old one.
//   * The wake-up subscription is made BEFORE the first attempt, so a command enqueued between the attempt and the
//     wait is not missed. Every exit (answer, timeout, abort, error) removes the subscription and the timer.
//   * Shutdown: every held poll is ended when the module is destroyed, before the HTTP server waits for requests.
import { BeforeApplicationShutdown, Inject, Injectable, Logger } from '@nestjs/common';
import type { LocalaiCommand } from '@prisma/client';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

import { COMMAND_TYPE_PREFIX, isCommandType, type CommandType } from '../contracts/commands';
import { LOCALAI_NODE_LINK_TIMING } from '../contracts/config.schema';
import type { ClaimedJob, ClaimRequest, ClaimResponse } from '../contracts/command-wire';
import { COMMAND_ENQUEUED_CHANNEL, LOCALAI_CLOCK, type CommandEnqueuedEvent, type LocalaiClock } from './command-events';
import { COMMAND_POLICY } from './command-policy';
import { CommandClaimRepository } from './claim.sql';

/** The upper bound of `waitMs`: what the handshake advertises as `claimWaitMaxMs`. */
export const CLAIM_WAIT_MAX_MS = LOCALAI_NODE_LINK_TIMING.claimWaitMaxMs;

@Injectable()
export class ClaimService implements BeforeApplicationShutdown {
  private readonly logger = new Logger(ClaimService.name);
  /** `localai_nodes.id` of the nodes with a claim in flight. */
  private readonly inFlight = new Set<string>();
  /** One abort controller per held poll, so shutdown can end them all. */
  private readonly held = new Set<AbortController>();
  private shuttingDown = false;

  constructor(
    private readonly repository: CommandClaimRepository,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
    @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock,
  ) {}

  /** The full types a request asks for: the known ones with the prefix prepended; unknown names are dropped with a warning. */
  resolveTypes(names: readonly string[], nodeId: string): CommandType[] {
    const types = new Set<CommandType>();
    for (const name of names) {
      const full = `${COMMAND_TYPE_PREFIX}${name}`;
      if (isCommandType(full)) types.add(full);
      else this.logger.warn(`Node ${nodeId} asked to claim an unknown command type (dropped): ${name.slice(0, 64)}`);
    }
    return [...types];
  }

  /** `waitMs` clamped to `[0, claimWaitMaxMs]`; a missing or non-finite value is 0. */
  clampWait(waitMs: number | undefined): number {
    if (waitMs === undefined || !Number.isFinite(waitMs)) return 0;
    return Math.min(CLAIM_WAIT_MAX_MS, Math.max(0, Math.floor(waitMs)));
  }

  /**
   * Claims for a node. `signal` ends the poll early (client disconnect); the answer is then empty. The node is the
   * verified `localai_nodes` row's id, never a value from the body.
   */
  async claim(nodeId: string, request: ClaimRequest, signal?: AbortSignal): Promise<ClaimResponse> {
    const empty: ClaimResponse = { jobs: [] };
    if (this.shuttingDown || signal?.aborted) return empty;
    if (this.inFlight.has(nodeId)) {
      this.logger.debug(`claim_in_progress node=${nodeId}`);
      return empty;
    }
    const types = this.resolveTypes(request.types, nodeId);
    if (types.length === 0) return empty;

    this.inFlight.add(nodeId);
    const stop = new AbortController();
    this.held.add(stop);
    const onAbort = () => stop.abort();
    signal?.addEventListener('abort', onAbort, { once: true });

    const wake = new Wakeup();
    const unsubscribe = this.bus.subscribe<CommandEnqueuedEvent>(COMMAND_ENQUEUED_CHANNEL, (event) => {
      if (event?.nodeId === nodeId) wake.fire();
    });
    try {
      const deadline = this.clock.now().getTime() + this.clampWait(request.waitMs);
      for (;;) {
        if (stop.signal.aborted) return empty;
        const claimed = await this.attempt(nodeId, types);
        if (claimed) return { jobs: [claimed] };
        const remaining = deadline - this.clock.now().getTime();
        if (remaining <= 0 || stop.signal.aborted) return empty;
        const woke = await wake.wait(remaining, stop.signal);
        if (!woke) return empty;
      }
    } finally {
      unsubscribe();
      wake.dispose();
      signal?.removeEventListener('abort', onAbort);
      this.held.delete(stop);
      this.inFlight.delete(nodeId);
    }
  }

  /** Ends every held poll (the HTTP server then has nothing to wait for). */
  beforeApplicationShutdown(): void {
    this.shuttingDown = true;
    for (const controller of this.held) controller.abort();
  }

  private async attempt(nodeId: string, types: readonly CommandType[]): Promise<ClaimedJob | null> {
    const row = await this.repository.claimNext({
      nodeId,
      types,
      now: this.clock.now(),
      leaseMsOf: (type) => (isCommandType(type) ? COMMAND_POLICY[type].leaseMs : COMMAND_POLICY['localai.status.refresh'].leaseMs),
    });
    return row ? toClaimedJob(row) : null;
  }
}

/** The claim answer's entry for a row the repository just turned `running`. */
export function toClaimedJob(row: LocalaiCommand): ClaimedJob {
  if (!isCommandType(row.type) || !row.claimToken || !row.leaseExpiresAt) {
    throw new Error(`Command ${row.id} was claimed without a valid type, token or lease`);
  }
  return {
    job: { id: row.id, type: row.type, attempts: row.attempts, createdAt: row.createdAt.toISOString() },
    params: row.params as Record<string, unknown>,
    claimToken: row.claimToken,
    renewIntervalMs: COMMAND_POLICY[row.type].renewIntervalMs,
    leaseExpiresAt: row.leaseExpiresAt.toISOString(),
    traceparent: row.traceparent ?? null,
  };
}

/**
 * A wake-up with memory: `fire()` before `wait()` is remembered, so an event that arrives while an attempt is running
 * is not lost. `wait()` resolves true on a fire, false on the timeout or the abort, and cleans up after itself.
 */
class Wakeup {
  private fired = false;
  private resolve: ((woke: boolean) => void) | null = null;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private cleanup: (() => void) | null = null;

  fire(): void {
    if (this.resolve) this.settle(true);
    else this.fired = true;
  }

  wait(timeoutMs: number, signal: AbortSignal): Promise<boolean> {
    if (this.fired) {
      this.fired = false;
      return Promise.resolve(true);
    }
    if (signal.aborted) return Promise.resolve(false);
    return new Promise<boolean>((resolve) => {
      const onAbort = () => this.settle(false);
      this.resolve = resolve;
      this.timer = setTimeout(() => this.settle(false), timeoutMs);
      signal.addEventListener('abort', onAbort, { once: true });
      this.cleanup = () => signal.removeEventListener('abort', onAbort);
    });
  }

  dispose(): void {
    this.settle(false);
  }

  private settle(woke: boolean): void {
    if (this.timer) clearTimeout(this.timer);
    this.cleanup?.();
    this.timer = null;
    this.cleanup = null;
    const resolve = this.resolve;
    this.resolve = null;
    resolve?.(woke);
  }
}
