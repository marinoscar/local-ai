// The limits as tables over a fake clock (docs/specs/gateway.md sections 4.1 and 9): the RPM window edges, the
// concurrency slot released exactly once, the budget refusing at exactly used == budget, the order of the three checks,
// and that a refused request takes no slot.
import type { GatewayKeyRecord } from '../auth';
import { GatewayError } from '../errors';
import type { AdmissionInput } from '../limits';
import { makeKey, makeToken } from '../gateway.testkit';
import { DAY_MS } from '../usage/usage-day';
import { DailyUsageLedger } from '../usage/usage-ledger';
import { MAX_HOLD_MS, RateLimiter } from './rate-limiter';

const T0 = Date.UTC(2026, 10, 4, 12, 0, 0);

class Clock {
  constructor(public ms: number = T0) {}
  now = (): number => this.ms;
  advance(ms: number): void {
    this.ms += ms;
  }
}

function setup(keyOver: Partial<GatewayKeyRecord> = {}, daily = 0) {
  const clock = new Clock();
  const reads: string[] = [];
  const ledger = new DailyUsageLedger({ read: async (keyId, day) => (reads.push(`${keyId}|${day}`), daily) }, clock.now);
  const limiter = new RateLimiter({ ledger, now: clock.now });
  const key = makeKey(makeToken(), { rpmLimit: 60, concurrentLimit: 4, dailyTokenBudget: null, ...keyOver });
  const input = (): AdmissionInput => ({ ctx: { principal: { key, userId: key.userId } } as never, modelId: 'model-a', endpoint: 'chat.completions', stream: false });
  return { clock, ledger, limiter, key, input, reads };
}

async function refusal(promise: Promise<unknown>): Promise<GatewayError> {
  const error = await promise.then(
    () => null,
    (caught: unknown) => caught,
  );
  expect(error).toBeInstanceOf(GatewayError);
  return error as GatewayError;
}

describe('RPM: a sliding window of the last 60 s', () => {
  it('admits the 60th request in a minute and refuses the 61st with LAI-GW-030 and the seconds until the oldest leaves', async () => {
    const { clock, limiter, input } = setup({ rpmLimit: 60, concurrentLimit: 1000 });
    for (let index = 0; index < 60; index += 1) {
      const ticket = await limiter.admit(input());
      ticket.settle({ promptTokens: null, completionTokens: null, aborted: false });
      clock.advance(500); // 30 s in total for the 60 requests
    }
    expect(clock.ms - T0).toBe(30_000);
    const error = await refusal(limiter.admit(input()));
    expect(error.localaiCode).toBe('LAI-GW-030');
    expect(error.status).toBe(429);
    // The oldest timestamp is T0; it leaves the window at T0 + 60 s, 30 s from now.
    expect(error.retryAfterS).toBe(30);
  });

  it('rounds Retry-After up and never answers less than 1', async () => {
    const { clock, limiter, input } = setup({ rpmLimit: 1 });
    (await limiter.admit(input())).settle({ promptTokens: null, completionTokens: null, aborted: false });
    clock.advance(59_001);
    expect((await refusal(limiter.admit(input()))).retryAfterS).toBe(1);
    clock.advance(998); // 59 999 ms in: 1 ms left
    expect((await refusal(limiter.admit(input()))).retryAfterS).toBe(1);
  });

  it('the window slides: exactly 60 s after the oldest request it is admitted again', async () => {
    const { clock, limiter, input } = setup({ rpmLimit: 2, concurrentLimit: 10 });
    (await limiter.admit(input())).settle({ promptTokens: null, completionTokens: null, aborted: false });
    clock.advance(10_000);
    (await limiter.admit(input())).settle({ promptTokens: null, completionTokens: null, aborted: false });
    clock.advance(49_999); // 59 999 ms after the first
    await refusal(limiter.admit(input()));
    clock.advance(1); // 60 000 ms after the first: it has left the window
    const ticket = await limiter.admit(input());
    ticket.settle({ promptTokens: null, completionTokens: null, aborted: false });
    // ...and the second (at +10 s) is still inside, so the next one is refused until +70 s.
    expect((await refusal(limiter.admit(input()))).retryAfterS).toBe(10);
  });

  it('keys do not share a window', async () => {
    const one = setup({ rpmLimit: 1 });
    const other = makeKey(makeToken(), { rpmLimit: 1 });
    (await one.limiter.admit(one.input())).settle({ promptTokens: null, completionTokens: null, aborted: false });
    await refusal(one.limiter.admit(one.input()));
    const ticket = await one.limiter.admit({ ...one.input(), ctx: { principal: { key: other, userId: other.userId } } as never });
    expect(ticket.headers()['x-ratelimit-remaining-requests']).toBe('0');
  });
});

describe('concurrency: a counter released when the response ends', () => {
  it('refuses the request over the limit with LAI-GW-031 and Retry-After 2, and admits again after a settle', async () => {
    const { limiter, input } = setup({ concurrentLimit: 2 });
    const first = await limiter.admit(input());
    await limiter.admit(input());
    const error = await refusal(limiter.admit(input()));
    expect(error.localaiCode).toBe('LAI-GW-031');
    expect(error.status).toBe(429);
    expect(error.retryAfterS).toBe(2);
    first.settle({ promptTokens: null, completionTokens: null, aborted: false });
    await limiter.admit(input());
  });

  it('settles exactly once: a second settle does not free a second slot', async () => {
    const { limiter, input, key } = setup({ concurrentLimit: 2 });
    const first = await limiter.admit(input());
    await limiter.admit(input());
    first.settle({ promptTokens: null, completionTokens: null, aborted: false });
    first.settle({ promptTokens: null, completionTokens: null, aborted: true });
    first.settle({ promptTokens: 1, completionTokens: 1, aborted: false });
    expect(limiter.inflightOf(key.id)).toBe(1);
  });

  it('returns to 0 when every request ended, whether it finished, failed or was aborted', async () => {
    const { limiter, input, key } = setup({ concurrentLimit: 10 });
    const tickets = await Promise.all([limiter.admit(input()), limiter.admit(input()), limiter.admit(input())]);
    expect(limiter.inflightOf(key.id)).toBe(3);
    tickets[0]!.settle({ promptTokens: 5, completionTokens: 7, aborted: false });
    tickets[1]!.settle({ promptTokens: null, completionTokens: null, aborted: false });
    tickets[2]!.settle({ promptTokens: null, completionTokens: null, aborted: true });
    expect(limiter.inflightOf(key.id)).toBe(0);
  });

  it('reclaims a slot nobody gave back after MAX_HOLD_MS, so a bug cannot lock a key out for ever', async () => {
    const { clock, limiter, input } = setup({ concurrentLimit: 1, rpmLimit: 1000 });
    await limiter.admit(input()); // never settled
    clock.advance(MAX_HOLD_MS - 1);
    await refusal(limiter.admit(input()));
    clock.advance(2);
    await limiter.admit(input());
  });
});

describe('the daily token budget', () => {
  it('refuses at exactly used == budget with LAI-GW-032 and the seconds to 00:00 UTC; one token under is admitted', async () => {
    const under = setup({ dailyTokenBudget: 1000n }, 999);
    await under.limiter.admit(under.input());

    const exact = setup({ dailyTokenBudget: 1000n }, 1000);
    const error = await refusal(exact.limiter.admit(exact.input()));
    expect(error.localaiCode).toBe('LAI-GW-032');
    expect(error.status).toBe(429);
    // 12:00:00 UTC: 12 h to midnight.
    expect(error.retryAfterS).toBe(12 * 3600);
  });

  it('counts the not-yet-flushed tokens of the recorder (used = daily row + pending)', async () => {
    const { ledger, limiter, input, key, clock } = setup({ dailyTokenBudget: 1000n }, 900);
    const day = new Date(clock.now()).toISOString().slice(0, 10);
    ledger.add(key.id, day, 99);
    await limiter.admit(input()); // 999 < 1000
    ledger.add(key.id, day, 1);
    expect((await refusal(limiter.admit(input()))).localaiCode).toBe('LAI-GW-032');
  });

  it('lets a request that started under budget finish (overshoot), and refuses the next', async () => {
    const { ledger, limiter, input, key, clock } = setup({ dailyTokenBudget: 1000n }, 990);
    const ticket = await limiter.admit(input());
    const day = new Date(clock.now()).toISOString().slice(0, 10);
    ledger.add(key.id, day, 500); // the request spends far more than what was left
    expect(() => ticket.settle({ promptTokens: 100, completionTokens: 400, aborted: false })).not.toThrow();
    await refusal(limiter.admit(input()));
  });

  it('rolls over at 00:00 UTC: tomorrow starts from tomorrow\'s row', async () => {
    const { clock, limiter, input, reads } = setup({ dailyTokenBudget: 1000n }, 1000);
    await refusal(limiter.admit(input()));
    clock.advance(DAY_MS / 2 + 1); // past midnight
    // The fake reader answers 1000 for every day, so a new day is a new read and still refused...
    await refusal(limiter.admit(input()));
    expect(new Set(reads.map((read) => read.split('|')[1])).size).toBe(2);
  });

  it('a null budget is unlimited and never reads the daily row', async () => {
    const { limiter, input, reads } = setup({ dailyTokenBudget: null }, 10 ** 12);
    await limiter.admit(input());
    expect(reads).toEqual([]);
  });

  it('reads the daily row once per key and day, not once per request', async () => {
    const { limiter, input, reads } = setup({ dailyTokenBudget: 1_000_000n, concurrentLimit: 100 }, 5);
    for (let index = 0; index < 5; index += 1) await limiter.admit(input());
    expect(reads).toHaveLength(1);
  });
});

describe('the order of the checks and what a refusal costs', () => {
  it('checks RPM, then concurrency, then budget', async () => {
    // Window full, slot full and budget spent: RPM is reported.
    const everything = setup({ rpmLimit: 1, concurrentLimit: 1, dailyTokenBudget: 10n }, 0);
    await everything.limiter.admit(everything.input());
    everything.ledger.add(everything.key.id, new Date(everything.clock.now()).toISOString().slice(0, 10), 10);
    expect((await refusal(everything.limiter.admit(everything.input()))).localaiCode).toBe('LAI-GW-030');

    // Slot full and budget spent, window free: concurrency is reported.
    const slotAndBudget = setup({ rpmLimit: 10, concurrentLimit: 1, dailyTokenBudget: 10n }, 0);
    await slotAndBudget.limiter.admit(slotAndBudget.input());
    slotAndBudget.ledger.add(slotAndBudget.key.id, new Date(slotAndBudget.clock.now()).toISOString().slice(0, 10), 10);
    expect((await refusal(slotAndBudget.limiter.admit(slotAndBudget.input()))).localaiCode).toBe('LAI-GW-031');

    // Only the budget is spent.
    const budgetOnly = setup({ rpmLimit: 10, concurrentLimit: 10, dailyTokenBudget: 10n }, 10);
    expect((await refusal(budgetOnly.limiter.admit(budgetOnly.input()))).localaiCode).toBe('LAI-GW-032');
  });

  it('a request refused by concurrency or by budget takes no RPM slot', async () => {
    const byConcurrency = setup({ rpmLimit: 3, concurrentLimit: 1 });
    const held = await byConcurrency.limiter.admit(byConcurrency.input());
    for (let index = 0; index < 5; index += 1) await refusal(byConcurrency.limiter.admit(byConcurrency.input()));
    held.settle({ promptTokens: null, completionTokens: null, aborted: false });
    // Only ONE request was ever admitted, so two more fit in the window of 3.
    (await byConcurrency.limiter.admit(byConcurrency.input())).settle({ promptTokens: null, completionTokens: null, aborted: false });
    const third = await byConcurrency.limiter.admit(byConcurrency.input());
    expect(third.headers()['x-ratelimit-remaining-requests']).toBe('0');

    const byBudget = setup({ rpmLimit: 1, concurrentLimit: 1, dailyTokenBudget: 5n }, 5);
    for (let index = 0; index < 3; index += 1) await refusal(byBudget.limiter.admit(byBudget.input()));
    expect(byBudget.limiter.inflightOf(byBudget.key.id)).toBe(0);
    expect(await byBudget.limiter.headersFor(byBudget.key)).toMatchObject({ 'x-ratelimit-remaining-requests': '1' });
  });

  it('a request refused by RPM takes no concurrency slot', async () => {
    const { limiter, input, key } = setup({ rpmLimit: 1, concurrentLimit: 5 });
    await limiter.admit(input());
    await refusal(limiter.admit(input()));
    expect(limiter.inflightOf(key.id)).toBe(1);
  });
});

describe('headers', () => {
  it('describe the key after the admission: the request and the tokens left', async () => {
    const { limiter, input } = setup({ rpmLimit: 60, dailyTokenBudget: 2_000_000n }, 1234);
    const headers = (await limiter.admit(input())).headers();
    expect(headers).toEqual({
      'x-ratelimit-limit-requests': '60',
      'x-ratelimit-remaining-requests': '59',
      'x-ratelimit-limit-tokens': '2000000',
      'x-ratelimit-remaining-tokens': String(2_000_000 - 1234),
    });
  });

  it('headersFor describes a key without taking a slot, and never throws', async () => {
    const { limiter, key, input } = setup({ rpmLimit: 5, dailyTokenBudget: 100n }, 40);
    expect(await limiter.headersFor(key)).toMatchObject({ 'x-ratelimit-remaining-requests': '5', 'x-ratelimit-remaining-tokens': '60' });
    await limiter.admit(input());
    expect(await limiter.headersFor(key)).toMatchObject({ 'x-ratelimit-remaining-requests': '4' });
    expect(limiter.inflightOf(key.id)).toBe(1);

    const broken = new RateLimiter({ ledger: { used: async () => { throw new Error('no'); }, usedNow: () => 0 } });
    expect(await broken.headersFor(key)).toEqual({});
  });
});
