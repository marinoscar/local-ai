// The tokens a key has used on one UTC day, as the budget check needs them (docs/specs/gateway.md section 4.1):
//
//   used = the daily row of the database + what the recorder has queued but not flushed yet
//
// The row is read from the database once per (key, day) and then kept in memory: the recorder reports each flush
// (`flushed`), which moves the amount from "pending" to "row", so the sum never drops when a batch is written and never
// counts a request twice. One API replica writes the table (spec 4.1), so a cached row cannot go stale; a second
// replica would need a TTL here. The read is also the only I/O of a budget check, and it happens once per key and day.
import { utcDay } from './usage-day';

/** Reads the `prompt_tokens + completion_tokens` of `localai_gateway_usage_daily` for one key and UTC day (0 when no row). */
export interface DailyUsageReader {
  read(keyId: string, day: string): Promise<number>;
}

interface Entry {
  /** The database row's total, once read. */
  row: number | null;
  loading: Promise<number | null> | null;
  /** Recorded in memory, not yet in the database. */
  pending: number;
}

const PRUNE_ABOVE = 2000;

export class DailyUsageLedger {
  private readonly entries = new Map<string, Entry>();

  constructor(
    private readonly reader: DailyUsageReader,
    private readonly now: () => number = Date.now,
  ) {}

  private static id(keyId: string, day: string): string {
    return `${keyId}|${day}`;
  }

  private entry(keyId: string, day: string): Entry {
    const id = DailyUsageLedger.id(keyId, day);
    let entry = this.entries.get(id);
    if (!entry) {
      if (this.entries.size >= PRUNE_ABOVE) this.prune();
      entry = { row: null, loading: null, pending: 0 };
      this.entries.set(id, entry);
    }
    return entry;
  }

  /**
   * Tokens used by the key on `day`. A failed database read is not an error for the caller (the budget check must not
   * turn a database blip into a refusal or a 500): the in-memory part is answered and the read is retried next time.
   */
  async used(keyId: string, day: string): Promise<number> {
    const entry = this.entry(keyId, day);
    if (entry.row === null) {
      entry.loading ??= this.reader
        .read(keyId, day)
        .then((total) => {
          entry.row = total;
          return total;
        })
        .catch(() => null)
        .finally(() => {
          entry.loading = null;
        });
      await entry.loading;
    }
    return (entry.row ?? 0) + entry.pending;
  }

  /** What is known without I/O: the cached row (0 if unread) plus pending. For response headers after `used()` ran. */
  usedNow(keyId: string, day: string): number {
    const entry = this.entries.get(DailyUsageLedger.id(keyId, day));
    return entry ? (entry.row ?? 0) + entry.pending : 0;
  }

  /** A request finished with `tokens` tokens: pending until the recorder writes it. */
  add(keyId: string, day: string, tokens: number): void {
    if (tokens > 0) this.entry(keyId, day).pending += tokens;
  }

  /**
   * The recorder wrote `tokens` of the key's day (`persisted: true`) or gave up on them (a dropped batch, `persisted:
   * false`). Either way they leave "pending" and join the cached row, so the sum is unchanged: a failing database never
   * lets a key overspend. When the row is not cached yet, a written amount is simply in the row the next read returns,
   * and a dropped one stays pending (nothing else remembers it).
   */
  flushed(keyId: string, day: string, tokens: number, persisted = true): void {
    if (tokens <= 0) return;
    const entry = this.entries.get(DailyUsageLedger.id(keyId, day));
    if (!entry) return;
    if (entry.row !== null) {
      entry.pending = Math.max(0, entry.pending - tokens);
      entry.row += tokens;
    } else if (persisted) {
      entry.pending = Math.max(0, entry.pending - tokens);
    }
  }

  /** Drops the days before today that have nothing pending. */
  prune(): void {
    const today = utcDay(this.now());
    for (const [id, entry] of this.entries) {
      if (!id.endsWith(`|${today}`) && entry.pending === 0 && entry.loading === null) this.entries.delete(id);
    }
  }

  get size(): number {
    return this.entries.size;
  }
}
