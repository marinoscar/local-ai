// A small in-memory TTL cache with an injectable clock. Two uses in the gateway (docs/specs/gateway.md section 4.1):
// the positive key lookup (10 s, keyed by `tokenHash`, NEVER holds a negative result, `evict(tokenHash)` on revoke) and
// the owner's effective permissions (60 s, keyed by user id).
export type Clock = () => number;

export const KEY_CACHE_TTL_MS = 10_000;
export const OWNER_CACHE_TTL_MS = 60_000;
const DEFAULT_MAX_ENTRIES = 5_000;

export class TtlCache<V> {
  private readonly entries = new Map<string, { value: V; expiresAt: number }>();

  constructor(
    private readonly ttlMs: number,
    private readonly now: Clock = Date.now,
    private readonly maxEntries: number = DEFAULT_MAX_ENTRIES,
  ) {}

  get(key: string): V | undefined {
    const entry = this.entries.get(key);
    if (!entry) return undefined;
    if (entry.expiresAt <= this.now()) {
      this.entries.delete(key);
      return undefined;
    }
    return entry.value;
  }

  set(key: string, value: V): void {
    if (this.entries.size >= this.maxEntries && !this.entries.has(key)) this.prune();
    this.entries.set(key, { value, expiresAt: this.now() + this.ttlMs });
  }

  /** Forgets one entry; true when there was one. */
  evict(key: string): boolean {
    return this.entries.delete(key);
  }

  clear(): void {
    this.entries.clear();
  }

  get size(): number {
    return this.entries.size;
  }

  /** Drops the expired entries, and the oldest ones when that is not enough (insertion order). */
  private prune(): void {
    const now = this.now();
    for (const [key, entry] of this.entries) if (entry.expiresAt <= now) this.entries.delete(key);
    for (const key of this.entries.keys()) {
      if (this.entries.size < this.maxEntries) break;
      this.entries.delete(key);
    }
  }
}
