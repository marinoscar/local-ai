// The usage recorder: the `UsageSink` of the gateway (docs/specs/gateway.md section 4.5). `record` is synchronous and
// only appends to an in-memory queue, so it never blocks or fails a response; a flush (every 2 s, or as soon as the
// queue holds 100 rows) writes the batch with one `createMany` and one increment-upsert per (key, UTC day) through the
// `UsageStore`. A failed write is retried once; then the batch is dropped, counted and logged as `gateway.usage_dropped`
// (the Doctor check `localai.gateway.usage` reads the count, LAI-GW-060). The tokens of every record are also told to
// the `DailyUsageLedger`, which is what the daily budget check reads: used = daily row + not yet flushed.
//
// What a record holds is the `UsageRecord` of usage-sink.ts: metadata only. This file logs counts, never a record.
import { Logger, type OnModuleDestroy, type OnModuleInit } from '@nestjs/common';

import { utcDay } from './usage-day';
import type { DailyUsageLedger } from './usage-ledger';
import type { UsageRecord, UsageSink } from './usage-sink';
import { rollupsOf, type StampedUsage, type UsageStore } from './usage-store';

export const USAGE_FLUSH_INTERVAL_MS = 2000;
export const USAGE_BATCH_SIZE = 100;
/** A database that stays down must not grow the queue without bound: past this the OLDEST rows are dropped. */
export const USAGE_MAX_QUEUE = 10_000;
/** The one free-text field of a row is the model name the client sent; it is clipped like the log line's. */
export const MAX_REQUESTED_MODEL_CHARS = 200;
const HOUR_MS = 3_600_000;

export interface UsageRecorderOptions {
  store: UsageStore;
  ledger: DailyUsageLedger;
  now?: () => number;
  flushIntervalMs?: number;
  batchSize?: number;
  maxQueue?: number;
  /** A warning line (default: the Nest logger). */
  warn?: (line: string) => void;
}

/** What the Doctor check needs to know. */
export interface UsageHealth {
  /** Rows dropped within the window (default: the last hour). */
  droppedSince(windowMs?: number): number;
  queued(): number;
}

export class UsageRecorder implements UsageSink, UsageHealth, OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger('GatewayUsage');
  private readonly store: UsageStore;
  private readonly ledger: DailyUsageLedger;
  private readonly now: () => number;
  private readonly flushIntervalMs: number;
  private readonly batchSize: number;
  private readonly maxQueue: number;
  private readonly warn: (line: string) => void;

  private queue: StampedUsage[] = [];
  private timer: ReturnType<typeof setInterval> | null = null;
  /** Flushes run one after the other, so two batches never race on the same (key, day) row. */
  private chain: Promise<void> = Promise.resolve();
  private readonly drops: Array<{ at: number; count: number }> = [];

  constructor(options: UsageRecorderOptions) {
    this.store = options.store;
    this.ledger = options.ledger;
    this.now = options.now ?? Date.now;
    this.flushIntervalMs = options.flushIntervalMs ?? USAGE_FLUSH_INTERVAL_MS;
    this.batchSize = options.batchSize ?? USAGE_BATCH_SIZE;
    this.maxQueue = options.maxQueue ?? USAGE_MAX_QUEUE;
    this.warn = options.warn ?? ((line) => this.logger.warn(line));
  }

  onModuleInit(): void {
    this.start();
  }

  /** Starts the 2 s flush timer (unref'd: it never keeps the process alive). */
  start(): void {
    if (this.timer) return;
    this.timer = setInterval(() => void this.flush(), this.flushIntervalMs);
    this.timer.unref?.();
  }

  /** Stops the timer and writes what is queued, once (shutdown). */
  async onModuleDestroy(): Promise<void> {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
    await this.flush();
  }

  record(record: UsageRecord): void {
    const at = new Date(this.now());
    if (this.queue.length >= this.maxQueue) this.drop(this.queue.splice(0, this.queue.length - this.maxQueue + 1));
    this.queue.push({ ...record, requestedModel: record.requestedModel === null ? null : record.requestedModel.slice(0, MAX_REQUESTED_MODEL_CHARS), at });
    if (record.keyId) this.ledger.add(record.keyId, utcDay(at), (record.promptTokens ?? 0) + (record.completionTokens ?? 0));
    if (this.queue.length >= this.batchSize) void this.flush();
  }

  /** Writes the queue now. Resolves when this batch (and any earlier one) is done; never rejects. */
  flush(): Promise<void> {
    this.chain = this.chain.then(() => this.flushOnce()).catch(() => undefined);
    return this.chain;
  }

  private async flushOnce(): Promise<void> {
    if (this.queue.length === 0) return;
    const batch = this.queue;
    this.queue = [];
    const rollups = rollupsOf(batch, (at) => utcDay(at));
    try {
      await this.store.write(batch, rollups);
    } catch {
      try {
        await this.store.write(batch, rollups);
      } catch {
        this.drop(batch);
        return;
      }
    }
    this.settleLedger(batch, true);
  }

  private drop(batch: readonly StampedUsage[]): void {
    if (batch.length === 0) return;
    this.drops.push({ at: this.now(), count: batch.length });
    this.pruneDrops();
    this.warn(JSON.stringify({ event: 'gateway.usage_dropped', count: batch.length }));
    // A dropped row still counts against the budget (its tokens move from "pending" into the in-memory row).
    this.settleLedger(batch, false);
  }

  private settleLedger(batch: readonly StampedUsage[], persisted: boolean): void {
    for (const rollup of rollupsOf(batch, (at) => utcDay(at))) {
      this.ledger.flushed(rollup.keyId, rollup.day, rollup.promptTokens + rollup.completionTokens, persisted);
    }
  }

  private pruneDrops(): void {
    const cutoff = this.now() - 24 * HOUR_MS;
    while (this.drops.length > 0 && this.drops[0]!.at < cutoff) this.drops.shift();
  }

  droppedSince(windowMs: number = HOUR_MS): number {
    const cutoff = this.now() - windowMs;
    return this.drops.filter((drop) => drop.at >= cutoff).reduce((sum, drop) => sum + drop.count, 0);
  }

  queued(): number {
    return this.queue.length;
  }
}
