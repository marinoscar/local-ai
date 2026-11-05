// Where the usage recorder writes (docs/specs/gateway.md section 4.5): a port, so the batching and the failure policy
// are unit tested without a database, and the Prisma adapter that writes one batch in one transaction.
import { Injectable } from '@nestjs/common';
import { Prisma } from '@prisma/client';

import { PrismaService } from '../../../prisma/prisma.service';
import { dayStart } from './usage-day';
import type { DailyUsageReader } from './usage-ledger';
import type { UsageRecord } from './usage-sink';

/** A usage record with the instant it ended: the row's `created_at` and the UTC day of its rollup. */
export interface StampedUsage extends UsageRecord {
  at: Date;
}

/** The increments of one (key, UTC day) in a batch. */
export interface DailyRollup {
  keyId: string;
  /** `YYYY-MM-DD`. */
  day: string;
  requests: number;
  promptTokens: number;
  completionTokens: number;
}

export interface UsageStore {
  /** Writes the rows and the rollup increments together, or throws and writes nothing. */
  write(rows: readonly StampedUsage[], rollups: readonly DailyRollup[]): Promise<void>;
}

/** Injection token of the {@link UsageStore}. */
export const GATEWAY_USAGE_STORE = Symbol('LOCALAI_GATEWAY_USAGE_STORE');

/** The rollups of a batch: one per (key, day), rows without a key (none after authentication) are not rolled up. */
export function rollupsOf(rows: readonly StampedUsage[], dayOf: (at: Date) => string): DailyRollup[] {
  const byKeyDay = new Map<string, DailyRollup>();
  for (const row of rows) {
    if (row.keyId === null) continue;
    const day = dayOf(row.at);
    const id = `${row.keyId}|${day}`;
    let rollup = byKeyDay.get(id);
    if (!rollup) {
      rollup = { keyId: row.keyId, day, requests: 0, promptTokens: 0, completionTokens: 0 };
      byKeyDay.set(id, rollup);
    }
    rollup.requests += 1;
    rollup.promptTokens += row.promptTokens ?? 0;
    rollup.completionTokens += row.completionTokens ?? 0;
  }
  return [...byKeyDay.values()];
}

@Injectable()
export class PrismaUsageStore implements UsageStore, DailyUsageReader {
  constructor(private readonly prisma: PrismaService) {}

  async write(rows: readonly StampedUsage[], rollups: readonly DailyRollup[]): Promise<void> {
    if (rows.length === 0) return;
    // ONE createMany and one increment-upsert per (key, day), in one transaction (an array of statements, so the
    // transaction is held for the round trips only and the rollup always matches the rows).
    await this.prisma.$transaction([
      this.prisma.localaiGatewayUsage.createMany({
        data: rows.map((row) => ({
          keyId: row.keyId,
          userId: row.userId,
          requestId: row.requestId,
          endpoint: row.endpoint,
          model: row.model,
          requestedModel: row.requestedModel,
          nodeId: row.nodeId,
          status: row.status,
          errorCode: row.errorCode,
          stream: row.stream,
          aborted: row.aborted,
          attempts: row.attempts,
          promptTokens: row.promptTokens,
          completionTokens: row.completionTokens,
          usageSource: row.usageSource,
          ttfbMs: row.ttfbMs,
          durationMs: row.durationMs,
          createdAt: row.at,
        })),
      }),
      ...rollups.map(
        (rollup) => this.prisma.$executeRaw(Prisma.sql`
          INSERT INTO localai_gateway_usage_daily (key_id, day, requests, prompt_tokens, completion_tokens)
          VALUES (${rollup.keyId}::uuid, ${dayStart(rollup.day)}::date, ${rollup.requests}, ${BigInt(rollup.promptTokens)}, ${BigInt(rollup.completionTokens)})
          ON CONFLICT (key_id, day) DO UPDATE SET
            requests = localai_gateway_usage_daily.requests + EXCLUDED.requests,
            prompt_tokens = localai_gateway_usage_daily.prompt_tokens + EXCLUDED.prompt_tokens,
            completion_tokens = localai_gateway_usage_daily.completion_tokens + EXCLUDED.completion_tokens`),
      ),
    ]);
  }

  async read(keyId: string, day: string): Promise<number> {
    const row = await this.prisma.localaiGatewayUsageDaily.findUnique({ where: { keyId_day: { keyId, day: dayStart(day) } } });
    return row ? Number(row.promptTokens) + Number(row.completionTokens) : 0;
  }
}
