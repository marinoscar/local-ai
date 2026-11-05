// The per-key limits of the gateway (docs/specs/gateway.md section 4.1), in memory, per API replica:
//
//   RPM          a sliding window: the timestamps of the requests ADMITTED in the last 60 s. The (limit+1)th request in
//                the window is refused, 429 LAI-GW-030, `Retry-After` = seconds until the oldest timestamp leaves it.
//   Concurrency  a counter per key, taken at admission and given back by `settle()` when the response has ended
//                (finished, failed or aborted): 429 LAI-GW-031, `Retry-After: 2`.
//   Daily budget used = the daily row + the recorder's not-yet-flushed tokens (`DailyUsageLedger`); refused when
//                `used >= budget`: 429 LAI-GW-032, `Retry-After` = seconds to 00:00 UTC. A request that starts under
//                budget is allowed to finish (overshoot). `null` budget = unlimited.
//
// The three checks run in that order and COMMIT only together: a refused request takes no RPM slot and no concurrency
// slot. The one `await` (the ledger's first read of a key's day) happens BEFORE the checks, so the checks and the
// commit are one synchronous step and two requests cannot both take the last slot.
//
// THE TICKET CONTRACT (shared with the streaming handler, GW-07). `admit()` either throws a `GatewayError`, or returns a
// ticket that MUST be settled exactly once when the response has ended. Nobody calls `settle` by hand: `ProxyRequest.
// settle()` does it, together with the usage record, on every path (success, error before or after a node, client
// abort, mid-stream failure). A second `settle` is ignored. A ticket nobody settles (a bug) gives its slot back after
// `MAX_HOLD_MS` so a key can never be locked out forever.
//
// Tokens are NOT counted here: the budget is fed by the usage record (`UsageSink.record`, which the same
// `ProxyRequest.settle()` calls first), because that is also what is written to the database.
import { GatewayError } from '../errors';
import type { GatewayKeyRecord } from '../auth';
import type { AdmissionInput, AdmissionTicket, GatewayLimits } from '../limits';
import { secondsToUtcMidnight, utcDay } from '../usage/usage-day';
import type { DailyUsageLedger } from '../usage/usage-ledger';
import { buildRateLimitHeaders, type RateLimitSnapshot } from './headers';

export const RPM_WINDOW_MS = 60_000;
/** A slot nobody gave back is reclaimed after this (a little over the longest `gateway.timeouts.totalMs`, 2 h). */
export const MAX_HOLD_MS = 2 * 3_600_000 + 300_000;
const SWEEP_EVERY = 512;

/** Builds the headers of a response for a key without taking a slot (used for responses that were not admitted). */
export interface RateLimitHeaderSource {
  headersFor(key: GatewayKeyRecord): Promise<Record<string, string>>;
}

interface KeyState {
  /** Timestamps (ms) of admitted requests inside the window, oldest first. */
  window: number[];
  /** Admission time of each in-flight request. */
  inflight: Set<InflightSlot>;
}

interface InflightSlot {
  at: number;
}

export interface RateLimiterOptions {
  ledger: Pick<DailyUsageLedger, 'used' | 'usedNow'>;
  /** Epoch ms; a fake in the specs. */
  now?: () => number;
}

export class RateLimiter implements GatewayLimits, RateLimitHeaderSource {
  private readonly ledger: Pick<DailyUsageLedger, 'used' | 'usedNow'>;
  private readonly now: () => number;
  private readonly keys = new Map<string, KeyState>();
  private admissions = 0;

  constructor(options: RateLimiterOptions) {
    this.ledger = options.ledger;
    this.now = options.now ?? Date.now;
  }

  async admit(input: AdmissionInput): Promise<AdmissionTicket> {
    const key = input.ctx.principal.key;
    // The only await: the key's day total, read from the database the first time it is needed.
    if (key.dailyTokenBudget !== null) await this.ledger.used(key.id, utcDay(this.now()));

    const at = this.now();
    const state = this.state(key.id);
    this.expire(state, at);

    if (state.window.length >= key.rpmLimit) {
      const oldest = state.window[0]!;
      throw new GatewayError('LAI-GW-030', { retryAfterS: Math.max(1, Math.ceil((oldest + RPM_WINDOW_MS - at) / 1000)) });
    }
    if (state.inflight.size >= key.concurrentLimit) throw new GatewayError('LAI-GW-031');
    if (key.dailyTokenBudget !== null && BigInt(this.ledger.usedNow(key.id, utcDay(at))) >= key.dailyTokenBudget) {
      throw new GatewayError('LAI-GW-032', { retryAfterS: secondsToUtcMidnight(at) });
    }

    // Admitted: only now does the request take its slots.
    state.window.push(at);
    const slot: InflightSlot = { at };
    state.inflight.add(slot);
    if (++this.admissions % SWEEP_EVERY === 0) this.sweep(at);

    const headers = this.snapshotHeaders(key, state, at);
    let settled = false;
    return {
      headers: () => headers,
      settle: () => {
        if (settled) return;
        settled = true;
        state.inflight.delete(slot);
      },
    };
  }

  /** The headers for `key` as of now, without taking a slot. Never throws (a response is never failed over headers). */
  async headersFor(key: GatewayKeyRecord): Promise<Record<string, string>> {
    try {
      if (key.dailyTokenBudget !== null) await this.ledger.used(key.id, utcDay(this.now()));
      const at = this.now();
      const state = this.keys.get(key.id);
      if (state) this.expire(state, at);
      return this.snapshotHeaders(key, state, at);
    } catch {
      return {};
    }
  }

  /** Requests currently in flight for a key (specs and the sweep). */
  inflightOf(keyId: string): number {
    return this.keys.get(keyId)?.inflight.size ?? 0;
  }

  private snapshotHeaders(key: GatewayKeyRecord, state: KeyState | undefined, at: number): Record<string, string> {
    const snapshot: RateLimitSnapshot = {
      limitRequests: key.rpmLimit,
      remainingRequests: key.rpmLimit - (state?.window.length ?? 0),
      limitTokens: key.dailyTokenBudget,
      remainingTokens: key.dailyTokenBudget === null ? null : key.dailyTokenBudget - BigInt(this.ledger.usedNow(key.id, utcDay(at))),
    };
    return buildRateLimitHeaders(snapshot);
  }

  private state(keyId: string): KeyState {
    let state = this.keys.get(keyId);
    if (!state) {
      state = { window: [], inflight: new Set() };
      this.keys.set(keyId, state);
    }
    return state;
  }

  /** Drops the timestamps that left the window and the slots nobody gave back. */
  private expire(state: KeyState, at: number): void {
    const cutoff = at - RPM_WINDOW_MS;
    let drop = 0;
    while (drop < state.window.length && state.window[drop]! <= cutoff) drop += 1;
    if (drop > 0) state.window.splice(0, drop);
    for (const slot of state.inflight) if (at - slot.at > MAX_HOLD_MS) state.inflight.delete(slot);
  }

  /** Forgets the keys that have been quiet for a window. */
  private sweep(at: number): void {
    for (const [keyId, state] of this.keys) {
      this.expire(state, at);
      if (state.window.length === 0 && state.inflight.size === 0) this.keys.delete(keyId);
    }
  }
}
