// The usage API's rules (docs/specs/gateway.md section 3.8): scope, ranges, groupings. The queries run over a small
// in-memory Prisma stand-in; the same shapes on real SQL are in `test/localai/gateway-usage.db.spec.ts`.
import { BadRequestException, ForbiddenException, NotFoundException } from '@nestjs/common';

import { UsageQuerySchema } from './usage.schemas';
import { resolveRange, UsageService, type UsageActor } from './usage.service';

const NOW = Date.UTC(2026, 10, 4, 12);
const ME: UsageActor = { userId: 'user-me', permissions: ['gateway:use'] };
const ADMIN: UsageActor = { userId: 'user-admin', permissions: ['gateway:use', 'gateway:admin'] };
const parse = (query: Record<string, string>) => UsageQuerySchema.parse(query);

describe('resolveRange', () => {
  it('defaults to the last 30 UTC days ending today', () => {
    expect(resolveRange({}, NOW)).toEqual({ from: '2026-10-06', to: '2026-11-04', days: 30, groupBy: 'day' });
  });

  it.each([
    ['one day', { from: '2026-11-04', to: '2026-11-04' }, 1],
    ['seven days', { from: '2026-10-29', to: '2026-11-04' }, 7],
    ['a year', { from: '2025-11-05', to: '2026-11-04' }, 365],
  ])('accepts %s for groupBy=day', (_name, range, days) => {
    expect(resolveRange(range, NOW).days).toBe(days);
  });

  it('refuses a reversed range and one over 366 days', () => {
    expect(() => resolveRange({ from: '2026-11-05', to: '2026-11-04' }, NOW)).toThrow(BadRequestException);
    expect(() => resolveRange({ from: '2025-01-01', to: '2026-11-04' }, NOW)).toThrow(BadRequestException);
  });

  it.each(['model', 'node'] as const)('groupBy=%s covers at most 7 days: 7 is fine, 8 and the default 30 are 400', (groupBy) => {
    expect(resolveRange({ from: '2026-10-29', to: '2026-11-04', groupBy }, NOW).groupBy).toBe(groupBy);
    expect(() => resolveRange({ from: '2026-10-28', to: '2026-11-04', groupBy }, NOW)).toThrow(BadRequestException);
    expect(() => resolveRange({ groupBy }, NOW)).toThrow(BadRequestException);
  });
});

describe('UsageQuerySchema', () => {
  it('takes UTC dates, a group and the all flag, and nothing else', () => {
    expect(parse({ from: '2026-11-01', to: '2026-11-04', groupBy: 'node', all: '1' })).toEqual({ from: '2026-11-01', to: '2026-11-04', groupBy: 'node', all: true });
    expect(() => parse({ from: '2026-13-01' })).toThrow();
    expect(() => parse({ from: '2026-02-30' })).toThrow();
    expect(() => parse({ from: '11/01/2026' })).toThrow();
    expect(() => parse({ groupBy: 'hour' })).toThrow();
    expect(() => parse({ keyId: 'not-a-uuid' })).toThrow();
    expect(() => parse({ extra: '1' })).toThrow();
    expect(parse({}).all).toBe(false);
  });
});

/** The slice of Prisma the service uses, over arrays. */
function fakePrisma(data: { keys: Array<{ id: string; userId: string }>; daily: any[]; usage: any[] }) {
  const inKeys = (where: any, row: any) => (where.keyId?.in ? where.keyId.in.includes(row.keyId) : true);
  return {
    localaiGatewayKey: {
      findUnique: jest.fn(async ({ where }: any) => data.keys.find((key) => key.id === where.id) ?? null),
      findMany: jest.fn(async ({ where }: any) => data.keys.filter((key) => key.userId === where.userId)),
    },
    localaiGatewayUsageDaily: {
      groupBy: jest.fn(async ({ where }: any) => {
        const rows = data.daily.filter((row) => inKeys(where, row) && row.day >= where.day.gte && row.day <= where.day.lte);
        const byDay = new Map<number, any>();
        for (const row of rows) {
          const sum = byDay.get(row.day.getTime()) ?? { day: row.day, _sum: { requests: 0, promptTokens: 0n, completionTokens: 0n } };
          sum._sum.requests += row.requests;
          sum._sum.promptTokens += BigInt(row.promptTokens);
          sum._sum.completionTokens += BigInt(row.completionTokens);
          byDay.set(row.day.getTime(), sum);
        }
        return [...byDay.values()];
      }),
    },
    localaiGatewayUsage: {
      groupBy: jest.fn(async ({ by, where }: any) => {
        const column = by[0];
        const rows = data.usage.filter((row) => inKeys(where, row) && row.createdAt >= where.createdAt.gte && row.createdAt < where.createdAt.lt && (where.nodeId?.not === null ? row.nodeId !== null : true));
        const groups = new Map<string | null, any>();
        for (const row of rows) {
          const group = groups.get(row[column]) ?? { [column]: row[column], _count: { _all: 0 }, _sum: { promptTokens: 0, completionTokens: 0 } };
          group._count._all += 1;
          group._sum.promptTokens += row.promptTokens ?? 0;
          group._sum.completionTokens += row.completionTokens ?? 0;
          groups.set(row[column], group);
        }
        return [...groups.values()];
      }),
    },
  };
}

const day = (iso: string) => new Date(`${iso}T00:00:00Z`);
const at = (iso: string) => new Date(iso);
const FIXTURE = {
  keys: [
    { id: 'key-mine', userId: 'user-me' },
    { id: 'key-other', userId: 'user-other' },
  ],
  daily: [
    { keyId: 'key-mine', day: day('2026-11-03'), requests: 4, promptTokens: 100, completionTokens: 50 },
    { keyId: 'key-mine', day: day('2026-11-04'), requests: 2, promptTokens: 10, completionTokens: 5 },
    { keyId: 'key-other', day: day('2026-11-04'), requests: 9, promptTokens: 900, completionTokens: 90 },
  ],
  usage: [
    { keyId: 'key-mine', model: 'model-a', nodeId: 'ai-n1', promptTokens: 60, completionTokens: 30, createdAt: at('2026-11-03T10:00:00Z') },
    { keyId: 'key-mine', model: 'model-a', nodeId: 'ai-n2', promptTokens: 40, completionTokens: 20, createdAt: at('2026-11-03T11:00:00Z') },
    { keyId: 'key-mine', model: null, nodeId: null, promptTokens: null, completionTokens: null, createdAt: at('2026-11-04T09:00:00Z') },
    { keyId: 'key-other', model: 'model-b', nodeId: 'ai-n1', promptTokens: 900, completionTokens: 90, createdAt: at('2026-11-04T09:00:00Z') },
  ],
};

describe('UsageService', () => {
  const service = (data = FIXTURE) => new UsageService(fakePrisma(data) as never);

  it('groupBy=day reads the daily table for your own keys, fills empty days and totals them', async () => {
    const result = await service().query(ME, parse({ from: '2026-11-01', to: '2026-11-04' }), NOW);
    expect(result).toMatchObject({ scope: 'own', keyId: null, from: '2026-11-01', to: '2026-11-04', groupBy: 'day', totals: { requests: 6, promptTokens: 110, completionTokens: 55 } });
    expect(result.rows.map((row) => [row.key, row.requests])).toEqual([
      ['2026-11-01', 0],
      ['2026-11-02', 0],
      ['2026-11-03', 4],
      ['2026-11-04', 2],
    ]);
  });

  it('over 7 days or less each day also lists its requests per node; over more it does not', async () => {
    const short = await service().query(ME, parse({ from: '2026-11-03', to: '2026-11-04' }), NOW);
    expect(short.rows[0]!.nodes).toEqual([
      { nodeId: 'ai-n1', requests: 1 },
      { nodeId: 'ai-n2', requests: 1 },
    ]);
    expect(short.rows[1]!.nodes).toEqual([]);
    const long = await service().query(ME, parse({}), NOW);
    expect(long.rows.every((row) => row.nodes === undefined)).toBe(true);
  });

  it('never shows another user\'s usage without all=1, and an administrator without all=1 sees only their own', async () => {
    const own = await service().query(ADMIN, parse({ from: '2026-11-04', to: '2026-11-04' }), NOW);
    expect(own.totals.requests).toBe(0);
    const all = await service().query(ADMIN, parse({ from: '2026-11-04', to: '2026-11-04', all: '1' }), NOW);
    expect(all).toMatchObject({ scope: 'all', totals: { requests: 11 } });
  });

  it('all=1 without gateway:admin is 403', async () => {
    await expect(service().query(ME, parse({ all: '1' }), NOW)).rejects.toBeInstanceOf(ForbiddenException);
  });

  it('a key that is not yours does not exist (404), for you and for an administrator without all=1', async () => {
    await expect(service().query(ME, parse({ keyId: '00000000-0000-4000-8000-000000000000' }), NOW)).rejects.toBeInstanceOf(NotFoundException);
    const other = { ...FIXTURE, keys: [{ id: '11111111-1111-4111-8111-111111111111', userId: 'user-other' }] };
    await expect(service(other).query(ME, parse({ keyId: '11111111-1111-4111-8111-111111111111' }), NOW)).rejects.toBeInstanceOf(NotFoundException);
    await expect(service(other).query(ADMIN, parse({ keyId: '11111111-1111-4111-8111-111111111111' }), NOW)).rejects.toBeInstanceOf(NotFoundException);
    const asAdmin = await service(other).query(ADMIN, parse({ keyId: '11111111-1111-4111-8111-111111111111', all: '1', from: '2026-11-04', to: '2026-11-04' }), NOW);
    expect(asAdmin.keyId).toBe('11111111-1111-4111-8111-111111111111');
  });

  it('groupBy=model and groupBy=node read the per-request rows of the range, most requests first (ties by name, "none" last)', async () => {
    const byModel = await service().query(ME, parse({ from: '2026-11-03', to: '2026-11-04', groupBy: 'model' }), NOW);
    expect(byModel.rows).toEqual([
      { key: 'model-a', requests: 2, promptTokens: 100, completionTokens: 50 },
      { key: null, requests: 1, promptTokens: 0, completionTokens: 0 },
    ]);
    const byNode = await service().query(ME, parse({ from: '2026-11-03', to: '2026-11-04', groupBy: 'node' }), NOW);
    expect(byNode.rows.map((row) => [row.key, row.requests])).toEqual([
      ['ai-n1', 1],
      ['ai-n2', 1],
      [null, 1],
    ]);
    expect(byNode.totals.requests).toBe(3);
  });

  it('groupBy=model over more than 7 days is 400', async () => {
    await expect(service().query(ME, parse({ groupBy: 'model' }), NOW)).rejects.toBeInstanceOf(BadRequestException);
  });

  it('a user with no key gets an empty answer without querying usage', async () => {
    const prisma = fakePrisma({ keys: [], daily: [], usage: [] });
    const result = await new UsageService(prisma as never).query(ME, parse({ from: '2026-11-04', to: '2026-11-04' }), NOW);
    expect(result.totals).toEqual({ requests: 0, promptTokens: 0, completionTokens: 0 });
    expect(prisma.localaiGatewayUsageDaily.groupBy).not.toHaveBeenCalled();
  });
});
