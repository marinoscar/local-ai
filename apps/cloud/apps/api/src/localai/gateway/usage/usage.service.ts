// The usage API (docs/specs/gateway.md section 3.8): your own keys' usage, or every key's for a gateway administrator.
//   groupBy=day    from the DAILY table (kept forever): requests and tokens per UTC day, empty days included; over a
//                  range of at most 7 days each day also lists its requests per node (from the per-request rows).
//   groupBy=model|node   from the per-request rows, only for a range of at most 7 days (400 otherwise), because the rows
//                  are purged after `gateway.usageRetentionDays`.
// A key that is not yours does not exist for you (404), the convention of the key routes.
import { BadRequestException, ForbiddenException, Injectable, NotFoundException } from '@nestjs/common';

import { PrismaService } from '../../../prisma/prisma.service';
import { LOCALAI_PERMISSIONS } from '../../localai.permissions';
import { DAY_MS, dayStart, utcDay } from './usage-day';
import { DEFAULT_RANGE_DAYS, MAX_RANGE_DAYS, MAX_REQUEST_ROWS_DAYS, type UsageDto, type UsageGroupBy, type UsageQuery, type UsageRowDto, type UsageTotals } from './usage.schemas';

export interface UsageActor {
  userId: string;
  permissions: readonly string[];
}

const num = (value: number | bigint | null | undefined): number => (value === null || value === undefined ? 0 : Number(value));

/** `from` and `to` of a query with the defaults applied and the range checked (400 on a bad one). */
export function resolveRange(query: Pick<UsageQuery, 'from' | 'to' | 'groupBy'>, now: number): { from: string; to: string; days: number; groupBy: UsageGroupBy } {
  const to = query.to ?? utcDay(now);
  const from = query.from ?? utcDay(dayStart(to).getTime() - (DEFAULT_RANGE_DAYS - 1) * DAY_MS);
  const days = Math.round((dayStart(to).getTime() - dayStart(from).getTime()) / DAY_MS) + 1;
  if (days < 1) throw new BadRequestException('"from" must not be after "to"');
  if (days > MAX_RANGE_DAYS) throw new BadRequestException(`The range is at most ${MAX_RANGE_DAYS} days`);
  const groupBy = query.groupBy ?? 'day';
  if (groupBy !== 'day' && days > MAX_REQUEST_ROWS_DAYS) {
    throw new BadRequestException(`groupBy=${groupBy} covers at most ${MAX_REQUEST_ROWS_DAYS} days; pick a shorter range or groupBy=day`);
  }
  return { from, to, days, groupBy };
}

@Injectable()
export class UsageService {
  constructor(private readonly prisma: PrismaService) {}

  async query(actor: UsageActor, query: UsageQuery, now: number = Date.now()): Promise<UsageDto> {
    const isAdmin = actor.permissions.includes(LOCALAI_PERMISSIONS.GATEWAY_ADMIN.id);
    if (query.all && !isAdmin) throw new ForbiddenException('Only a gateway administrator can read every key');
    const scope = query.all ? 'all' : 'own';
    const { from, to, days, groupBy } = resolveRange(query, now);

    const keyIds = await this.keyFilter(actor, scope, query.keyId);
    const base = { scope, keyId: query.keyId ?? null, from, to, groupBy } as const;
    if (keyIds !== null && keyIds.length === 0) return { ...base, totals: { requests: 0, promptTokens: 0, completionTokens: 0 }, rows: groupBy === 'day' ? emptyDays(from, days) : [] };

    const rows = groupBy === 'day' ? await this.byDay(keyIds, from, to, days) : await this.byRequestColumn(keyIds, groupBy, from, days);
    const totals = rows.reduce<UsageTotals>(
      (sum, row) => ({ requests: sum.requests + row.requests, promptTokens: sum.promptTokens + row.promptTokens, completionTokens: sum.completionTokens + row.completionTokens }),
      { requests: 0, promptTokens: 0, completionTokens: 0 },
    );
    return { ...base, totals, rows };
  }

  /** The key ids to read: null = every key (an administrator, no key chosen). */
  private async keyFilter(actor: UsageActor, scope: 'own' | 'all', keyId: string | undefined): Promise<string[] | null> {
    if (keyId !== undefined) {
      const key = await this.prisma.localaiGatewayKey.findUnique({ where: { id: keyId }, select: { id: true, userId: true } });
      if (!key || (scope === 'own' && key.userId !== actor.userId)) throw new NotFoundException('Key not found');
      return [key.id];
    }
    if (scope === 'all') return null;
    return (await this.prisma.localaiGatewayKey.findMany({ where: { userId: actor.userId }, select: { id: true } })).map((key) => key.id);
  }

  private async byDay(keyIds: string[] | null, from: string, to: string, days: number): Promise<UsageRowDto[]> {
    const keyWhere = keyIds === null ? {} : { keyId: { in: keyIds } };
    const grouped = await this.prisma.localaiGatewayUsageDaily.groupBy({
      by: ['day'],
      where: { day: { gte: dayStart(from), lte: dayStart(to) }, ...keyWhere },
      _sum: { requests: true, promptTokens: true, completionTokens: true },
    });
    const byDay = new Map(grouped.map((row) => [utcDay(row.day), row._sum] as const));
    const result = emptyDays(from, days);
    for (const row of result) {
      const sum = byDay.get(row.key as string);
      if (sum) Object.assign(row, { requests: num(sum.requests), promptTokens: num(sum.promptTokens), completionTokens: num(sum.completionTokens) });
    }
    if (days <= MAX_REQUEST_ROWS_DAYS) {
      await Promise.all(
        result.map(async (row) => {
          const start = dayStart(row.key as string);
          const nodes = await this.prisma.localaiGatewayUsage.groupBy({
            by: ['nodeId'],
            where: { createdAt: { gte: start, lt: new Date(start.getTime() + DAY_MS) }, nodeId: { not: null }, ...keyWhere },
            _count: { _all: true },
          });
          row.nodes = nodes
            .map((node) => ({ nodeId: node.nodeId as string, requests: node._count._all }))
            .sort((a, b) => b.requests - a.requests || a.nodeId.localeCompare(b.nodeId));
        }),
      );
    }
    return result;
  }

  private async byRequestColumn(keyIds: string[] | null, groupBy: 'model' | 'node', from: string, days: number): Promise<UsageRowDto[]> {
    const start = dayStart(from);
    const grouped = await this.prisma.localaiGatewayUsage.groupBy({
      by: [groupBy === 'model' ? 'model' : 'nodeId'],
      where: { createdAt: { gte: start, lt: new Date(start.getTime() + days * DAY_MS) }, ...(keyIds === null ? {} : { keyId: { in: keyIds } }) },
      _count: { _all: true },
      _sum: { promptTokens: true, completionTokens: true },
    });
    return grouped
      .map((row): UsageRowDto => ({
        key: ((groupBy === 'model' ? (row as { model: string | null }).model : (row as { nodeId: string | null }).nodeId) ?? null) as string | null,
        requests: row._count._all,
        promptTokens: num(row._sum.promptTokens),
        completionTokens: num(row._sum.completionTokens),
      }))
      .sort((a, b) => b.requests - a.requests || (a.key === null ? 1 : b.key === null ? -1 : a.key.localeCompare(b.key)));
  }
}

/** One zeroed row per UTC day from `from`, `days` of them. */
function emptyDays(from: string, days: number): UsageRowDto[] {
  const first = dayStart(from).getTime();
  return Array.from({ length: days }, (_, index) => ({ key: utcDay(first + index * DAY_MS), requests: 0, promptTokens: 0, completionTokens: 0 }));
}
