import { DailyUsageLedger } from './usage-ledger';

const DAY = '2026-11-04';

function ledgerOver(total: number | (() => Promise<number>)) {
  const reads: string[] = [];
  const ledger = new DailyUsageLedger({
    read: async (keyId, day) => {
      reads.push(`${keyId}|${day}`);
      return typeof total === 'number' ? total : total();
    },
  }, () => Date.UTC(2026, 10, 4, 12));
  return { ledger, reads };
}

describe('DailyUsageLedger', () => {
  it('used = the daily row + what is pending', async () => {
    const { ledger } = ledgerOver(100);
    ledger.add('key-a', DAY, 7);
    expect(await ledger.used('key-a', DAY)).toBe(107);
  });

  it('reads the row once and shares one read between concurrent callers', async () => {
    const { ledger, reads } = ledgerOver(async () => 50);
    const results = await Promise.all([ledger.used('key-a', DAY), ledger.used('key-a', DAY), ledger.used('key-a', DAY)]);
    expect(results).toEqual([50, 50, 50]);
    await ledger.used('key-a', DAY);
    expect(reads).toHaveLength(1);
  });

  it('a flush moves tokens from pending to the row: the sum does not change and nothing counts twice', async () => {
    const { ledger } = ledgerOver(100);
    await ledger.used('key-a', DAY);
    ledger.add('key-a', DAY, 40);
    expect(ledger.usedNow('key-a', DAY)).toBe(140);
    ledger.flushed('key-a', DAY, 40);
    expect(ledger.usedNow('key-a', DAY)).toBe(140);
  });

  it('a flush before the row was ever read leaves it to the next read (the database has the tokens by then)', async () => {
    const { ledger } = ledgerOver(140);
    ledger.add('key-a', DAY, 40);
    ledger.flushed('key-a', DAY, 40);
    expect(await ledger.used('key-a', DAY)).toBe(140);
  });

  it('a dropped batch still counts against the budget', async () => {
    const { ledger } = ledgerOver(100);
    ledger.add('key-a', DAY, 40);
    ledger.flushed('key-a', DAY, 40, false); // row not read yet: stays pending
    expect(await ledger.used('key-a', DAY)).toBe(140);
    ledger.add('key-a', DAY, 10);
    ledger.flushed('key-a', DAY, 10, false); // row read: joins the in-memory row
    expect(ledger.usedNow('key-a', DAY)).toBe(150);
  });

  it('a failing read is not an error: the in-memory part is answered and the read is retried next time', async () => {
    let fail = true;
    const { ledger, reads } = ledgerOver(async () => {
      if (fail) throw new Error('db down');
      return 30;
    });
    ledger.add('key-a', DAY, 5);
    expect(await ledger.used('key-a', DAY)).toBe(5);
    fail = false;
    expect(await ledger.used('key-a', DAY)).toBe(35);
    expect(reads).toHaveLength(2);
  });

  it('prunes the days before today that have nothing pending, and keeps today and unflushed days', async () => {
    const { ledger } = ledgerOver(1);
    await ledger.used('key-a', '2026-11-02');
    ledger.add('key-b', '2026-11-03', 5);
    await ledger.used('key-c', DAY);
    ledger.prune();
    expect(ledger.size).toBe(2);
  });
});
