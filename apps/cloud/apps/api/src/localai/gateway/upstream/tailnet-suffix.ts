// The `gateway.tailnetSuffix` setting (docs/specs/gateway.md section 3.7) for the tailnet upstream mode. Same approach
// as `../proxy-settings.ts`: the stored `localai` object of the system settings row, nested or dotted form, cached 10 s;
// a missing, invalid or unreadable value is "no suffix" (the Pattern B nodes are then left out of the candidates), so a
// bad setting never takes the gateway down. The value is a tailnet name: it is never logged.
import { Injectable, Logger } from '@nestjs/common';

import { PrismaService } from '../../../prisma/prisma.service';
import { TtlCache } from '../auth-cache';
import { TAILNET_SUFFIX, type TailnetSuffixSource } from './targets';

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/** Pure: the stored `localai` object (anything) to the suffix, or null when it is unset or malformed. */
export function tailnetSuffixFromStored(localai: unknown): string | null {
  if (!isRecord(localai)) return null;
  const nested = isRecord(localai.gateway) ? localai.gateway.tailnetSuffix : undefined;
  const value = nested ?? localai['gateway.tailnetSuffix'];
  return typeof value === 'string' && TAILNET_SUFFIX.test(value) ? value : null;
}

@Injectable()
export class PrismaTailnetSuffixReader implements TailnetSuffixSource {
  private readonly logger = new Logger(PrismaTailnetSuffixReader.name);
  private readonly cache = new TtlCache<{ suffix: string | null }>(10_000);

  constructor(private readonly prisma: PrismaService) {}

  async read(): Promise<string | null> {
    const cached = this.cache.get('suffix');
    if (cached) return cached.suffix;
    let suffix: string | null = null;
    try {
      const row = await this.prisma.systemSettings.findUnique({ where: { key: 'global' } });
      suffix = tailnetSuffixFromStored(isRecord(row?.value) ? row.value.localai : undefined);
    } catch (error) {
      this.logger.warn(`could not read the tailnet suffix, treating it as unset: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
    this.cache.set('suffix', { suffix });
    return suffix;
  }
}
