// The failed-auth limiter (docs/specs/gateway.md section 4.1): per client IP (the inner nginx's `X-Real-IP`), 30 wrong
// keys inside one minute block that IP for 5 minutes (429 LAI-GW-011). In memory, per API replica; a restart clears it.
// The 30th failure still answers 401; the NEXT attempt is the first 429.
import type { Clock } from './auth-cache';

export const FAILED_AUTH_MAX = 30;
export const FAILED_AUTH_WINDOW_MS = 60_000;
export const FAILED_AUTH_BLOCK_MS = 5 * 60_000;
const MAX_TRACKED_IPS = 10_000;

interface IpState {
  failures: number[];
  blockedUntil: number;
}

export class FailedAuthLimiter {
  private readonly ips = new Map<string, IpState>();

  constructor(
    private readonly now: Clock = Date.now,
    private readonly max: number = FAILED_AUTH_MAX,
    private readonly windowMs: number = FAILED_AUTH_WINDOW_MS,
    private readonly blockMs: number = FAILED_AUTH_BLOCK_MS,
  ) {}

  /** Seconds the IP stays blocked (at least 1), or null when it may try. A lifted block forgets the IP. */
  blockedForSeconds(ip: string): number | null {
    const state = this.ips.get(ip);
    if (!state || state.blockedUntil === 0) return null;
    const left = state.blockedUntil - this.now();
    if (left <= 0) {
      this.ips.delete(ip);
      return null;
    }
    return Math.max(1, Math.ceil(left / 1000));
  }

  /** Counts one wrong key; the `max`-th failure inside the window starts the block. */
  recordFailure(ip: string): void {
    const now = this.now();
    if (this.ips.size >= MAX_TRACKED_IPS && !this.ips.has(ip)) this.sweep(now);
    const state = this.ips.get(ip) ?? { failures: [], blockedUntil: 0 };
    state.failures = state.failures.filter((at) => now - at < this.windowMs);
    state.failures.push(now);
    if (state.failures.length >= this.max) {
      state.blockedUntil = now + this.blockMs;
      state.failures = [];
    }
    this.ips.set(ip, state);
  }

  private sweep(now: number): void {
    for (const [ip, state] of this.ips) {
      const idle = state.blockedUntil === 0 ? state.failures.every((at) => now - at >= this.windowMs) : state.blockedUntil <= now;
      if (idle) this.ips.delete(ip);
    }
    // Still full (an attack from many addresses): drop the oldest half rather than grow without bound.
    if (this.ips.size >= MAX_TRACKED_IPS) {
      let drop = Math.floor(this.ips.size / 2);
      for (const ip of this.ips.keys()) {
        if (drop-- <= 0) break;
        this.ips.delete(ip);
      }
    }
  }
}
