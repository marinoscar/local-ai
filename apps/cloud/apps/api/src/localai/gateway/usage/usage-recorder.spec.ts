// The recorder (docs/specs/gateway.md section 4.5): batching at 2 s / 100 rows, one write per batch with the rollups of
// each (key, UTC day), a retry once then a counted drop, the shutdown flush, and that `record` never throws or waits.
import { DailyUsageLedger } from './usage-ledger';
import { USAGE_BATCH_SIZE, UsageRecorder } from './usage-recorder';
import type { DailyRollup, StampedUsage, UsageStore } from './usage-store';
import type { UsageRecord } from './usage-sink';

const T0 = Date.UTC(2026, 10, 4, 12, 0, 0);

function record(over: Partial<UsageRecord> = {}): UsageRecord {
  return {
    requestId: 'req-1',
    keyId: 'key-a',
    userId: 'user-a',
    endpoint: 'chat.completions',
    model: 'model-a',
    requestedModel: 'model-a',
    nodeId: 'ai-n1',
    status: 200,
    errorCode: null,
    stream: false,
    aborted: false,
    attempts: 1,
    promptTokens: 10,
    completionTokens: 5,
    usageSource: 'upstream',
    ttfbMs: 20,
    durationMs: 100,
    ...over,
  };
}

class FakeStore implements UsageStore {
  writes: Array<{ rows: readonly StampedUsage[]; rollups: readonly DailyRollup[] }> = [];
  failures = 0;
  async write(rows: readonly StampedUsage[], rollups: readonly DailyRollup[]): Promise<void> {
    if (this.failures > 0) {
      this.failures -= 1;
      throw new Error('db down');
    }
    this.writes.push({ rows, rollups });
  }
  get rows(): number {
    return this.writes.reduce((sum, write) => sum + write.rows.length, 0);
  }
}

function setup(over: { maxQueue?: number } = {}) {
  const store = new FakeStore();
  const clock = { ms: T0 };
  const warnings: string[] = [];
  const ledger = new DailyUsageLedger({ read: async () => 0 }, () => clock.ms);
  const recorder = new UsageRecorder({ store, ledger, now: () => clock.ms, warn: (line) => warnings.push(line), ...over });
  return { store, clock, warnings, ledger, recorder };
}

describe('UsageRecorder batching', () => {
  beforeEach(() => jest.useFakeTimers());
  afterEach(() => jest.useRealTimers());

  it('record only queues: nothing is written until the 2 s timer fires', async () => {
    const { recorder, store } = setup();
    recorder.start();
    recorder.record(record());
    expect(store.writes).toHaveLength(0);
    expect(recorder.queued()).toBe(1);
    await jest.advanceTimersByTimeAsync(1999);
    expect(store.writes).toHaveLength(0);
    await jest.advanceTimersByTimeAsync(1);
    expect(store.writes).toHaveLength(1);
    expect(recorder.queued()).toBe(0);
    await recorder.onModuleDestroy();
  });

  it('flushes at 100 rows without waiting for the timer', async () => {
    const { recorder, store } = setup();
    for (let index = 0; index < USAGE_BATCH_SIZE - 1; index += 1) recorder.record(record({ requestId: `r${index}` }));
    expect(store.writes).toHaveLength(0);
    recorder.record(record({ requestId: 'r-last' }));
    await recorder.flush();
    expect(store.writes).toHaveLength(1);
    expect(store.writes[0]!.rows).toHaveLength(100);
  });

  it('an empty queue writes nothing', async () => {
    const { recorder, store } = setup();
    recorder.start();
    await jest.advanceTimersByTimeAsync(10_000);
    expect(store.writes).toHaveLength(0);
    await recorder.onModuleDestroy();
  });

  it('flushes once on shutdown', async () => {
    const { recorder, store } = setup();
    recorder.start();
    recorder.record(record());
    recorder.record(record({ requestId: 'req-2' }));
    await recorder.onModuleDestroy();
    expect(store.rows).toBe(2);
  });

  it('stamps each row with the instant it ended, and rolls up per (key, UTC day) of that instant', async () => {
    const { recorder, store, clock } = setup();
    clock.ms = Date.UTC(2026, 10, 4, 23, 59, 59);
    recorder.record(record({ requestId: 'late', promptTokens: 10, completionTokens: 5 }));
    recorder.record(record({ requestId: 'late-2', promptTokens: null, completionTokens: null, usageSource: 'none' }));
    clock.ms = Date.UTC(2026, 10, 5, 0, 0, 1); // the flush happens after midnight, the rows ended before it
    recorder.record(record({ requestId: 'early', keyId: 'key-b', promptTokens: 3, completionTokens: 4 }));
    await recorder.flush();
    const { rows, rollups } = store.writes[0]!;
    expect(rows.map((row) => row.at.toISOString())).toEqual(['2026-11-04T23:59:59.000Z', '2026-11-04T23:59:59.000Z', '2026-11-05T00:00:01.000Z']);
    expect(rollups).toEqual([
      { keyId: 'key-a', day: '2026-11-04', requests: 2, promptTokens: 10, completionTokens: 5 },
      { keyId: 'key-b', day: '2026-11-05', requests: 1, promptTokens: 3, completionTokens: 4 },
    ]);
  });

  it('feeds the budget ledger: tokens are pending at once and stay counted after the flush', async () => {
    const { recorder, ledger } = setup();
    recorder.record(record({ promptTokens: 10, completionTokens: 5 }));
    expect(await ledger.used('key-a', '2026-11-04')).toBe(15);
    await recorder.flush();
    expect(await ledger.used('key-a', '2026-11-04')).toBe(15);
  });
});

describe('UsageRecorder failure policy', () => {
  it('retries a failed write once and succeeds without dropping anything', async () => {
    const { recorder, store, warnings } = setup();
    store.failures = 1;
    recorder.record(record());
    await recorder.flush();
    expect(store.rows).toBe(1);
    expect(warnings).toEqual([]);
    expect(recorder.droppedSince()).toBe(0);
  });

  it('drops the batch after two failures, logs gateway.usage_dropped with the count, and later batches still work', async () => {
    const { recorder, store, warnings } = setup();
    store.failures = 2;
    for (let index = 0; index < 3; index += 1) recorder.record(record({ requestId: `r${index}` }));
    await recorder.flush();
    expect(store.rows).toBe(0);
    expect(warnings).toEqual([JSON.stringify({ event: 'gateway.usage_dropped', count: 3 })]);
    expect(recorder.droppedSince()).toBe(3);

    recorder.record(record({ requestId: 'after' }));
    await recorder.flush();
    expect(store.rows).toBe(1);
  });

  it('a dropped batch still counts against the budget', async () => {
    const { recorder, store, ledger } = setup();
    await ledger.used('key-a', '2026-11-04');
    store.failures = 2;
    recorder.record(record({ promptTokens: 40, completionTokens: 2 }));
    await recorder.flush();
    expect(ledger.usedNow('key-a', '2026-11-04')).toBe(42);
  });

  it('droppedSince looks at the last hour only', async () => {
    const { recorder, store, clock } = setup();
    store.failures = 2;
    recorder.record(record());
    await recorder.flush();
    expect(recorder.droppedSince(3_600_000)).toBe(1);
    clock.ms += 3_600_001;
    expect(recorder.droppedSince(3_600_000)).toBe(0);
  });

  it('a database that stays down cannot grow the queue without bound: the oldest rows are dropped and counted', () => {
    const { recorder, warnings } = setup({ maxQueue: 5 });
    for (let index = 0; index < 8; index += 1) recorder.record(record({ requestId: `r${index}` }));
    expect(recorder.queued()).toBe(5);
    expect(recorder.droppedSince()).toBe(3);
    expect(warnings.length).toBeGreaterThan(0);
  });

  it('record never throws and returns before any I/O', () => {
    const { recorder } = setup();
    expect(recorder.record(record())).toBeUndefined();
  });

  it('writes batches one after the other, in the order the rows ended', async () => {
    const { recorder, store } = setup();
    recorder.record(record({ requestId: 'one' }));
    await recorder.flush();
    recorder.record(record({ requestId: 'two' }));
    await recorder.flush();
    expect(store.writes.map((write) => write.rows.map((row) => row.requestId))).toEqual([['one'], ['two']]);
  });

  it('logs counts only, never a record', async () => {
    const { recorder, store, warnings } = setup();
    store.failures = 2;
    recorder.record(record({ requestedModel: 'marker-model-name' }));
    await recorder.flush();
    expect(warnings.join('\n')).not.toContain('marker-model-name');
    expect(warnings.join('\n')).not.toContain('req-1');
  });
});
