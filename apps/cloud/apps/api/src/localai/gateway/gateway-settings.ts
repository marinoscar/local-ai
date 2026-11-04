// The gateway's runtime inputs: the deployment switch (`LOCALAI_GATEWAY_ENABLED`, env, default true) and the CORS origin
// list (`gateway.corsOrigins` of the `localai` settings namespace). The namespace is registered with the settings
// issue; until then the stored system settings row is read directly, exactly as `alerts/alert-settings.ts` does, and
// both the nested form (`{ gateway: { corsOrigins } }`) and the dotted key are accepted. Cached for 10 s so the
// per-request read is a map hit. A failed read, or an invalid stored value, means "no CORS origins".
import { Injectable, Logger } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { TtlCache } from './auth-cache';
import { GATEWAY_CORS_ORIGINS_KEY, parseCorsOrigins } from './cors';

const SETTINGS_CACHE_TTL_MS = 10_000;

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/** `LOCALAI_GATEWAY_ENABLED`: `false` (any case) turns the gateway off; unset or anything else leaves it on. */
export function gatewayEnabledFromEnv(env: NodeJS.ProcessEnv = process.env): boolean {
  return (env.LOCALAI_GATEWAY_ENABLED ?? 'true').trim().toLowerCase() !== 'false';
}

/** Pure: the stored `localai` object (anything) to the CORS origin list. */
export function corsOriginsFromStored(localai: unknown): string[] {
  if (!isRecord(localai)) return [];
  const nested = isRecord(localai.gateway) ? localai.gateway.corsOrigins : undefined;
  return parseCorsOrigins(nested ?? localai[GATEWAY_CORS_ORIGINS_KEY] ?? []);
}

/** What the routes need from the settings; the interface lets a spec supply a fixed list. */
export interface GatewayConfig {
  enabled(): boolean;
  corsOrigins(): Promise<readonly string[]>;
}

@Injectable()
export class GatewaySettingsReader implements GatewayConfig {
  private readonly logger = new Logger(GatewaySettingsReader.name);
  private readonly cache = new TtlCache<string[]>(SETTINGS_CACHE_TTL_MS);

  constructor(private readonly prisma: PrismaService) {}

  enabled(): boolean {
    return gatewayEnabledFromEnv();
  }

  async corsOrigins(): Promise<readonly string[]> {
    const cached = this.cache.get('cors');
    if (cached) return cached;
    let origins: string[] = [];
    try {
      const row = await this.prisma.systemSettings.findUnique({ where: { key: 'global' } });
      origins = corsOriginsFromStored(isRecord(row?.value) ? row.value.localai : undefined);
    } catch (error) {
      this.logger.warn(`could not read the gateway CORS origins, CORS stays off: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
    this.cache.set('cors', origins);
    return origins;
  }
}
