// The settings the proxy reads per request: `gateway.timeouts` and `gateway.maxAttempts` (docs/specs/gateway.md section
// 3.7). Same approach as `gateway-settings.ts` and `routing/routing-source.ts`: the stored `localai` object of the system
// settings row, nested or dotted form, cached 10 s; an invalid or missing value falls back to the default, so a bad
// setting can never take the gateway down.
import { Injectable, Logger } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { TtlCache } from './auth-cache';

export interface GatewayTimeouts {
  connectMs: number;
  firstByteMs: number;
  idleMs: number;
  totalMs: number;
  nonStreamTotalMs: number;
}

export const DEFAULT_GATEWAY_TIMEOUTS: GatewayTimeouts = { connectMs: 5_000, firstByteMs: 180_000, idleMs: 120_000, totalMs: 1_800_000, nonStreamTotalMs: 900_000 };
export const DEFAULT_MAX_ATTEMPTS = 3;

export interface ProxySettings {
  timeouts: GatewayTimeouts;
  maxAttempts: number;
}

export interface ProxySettingsReader {
  read(): Promise<ProxySettings>;
}

/** Injection token of the {@link ProxySettingsReader}. */
export const GATEWAY_PROXY_SETTINGS = Symbol('LOCALAI_GATEWAY_PROXY_SETTINGS');

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

const boundedInt = (value: unknown, min: number, max: number, fallback: number): number =>
  typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max ? value : fallback;

/** Pure: the stored `localai` object (anything) to the proxy settings. Bounds are those of the settings schema. */
export function parseProxySettings(localai: unknown): ProxySettings {
  const stored = isRecord(localai) ? localai : {};
  const gateway = isRecord(stored.gateway) ? stored.gateway : {};
  const rawTimeouts = gateway.timeouts ?? stored['gateway.timeouts'];
  const t = isRecord(rawTimeouts) ? rawTimeouts : {};
  const d = DEFAULT_GATEWAY_TIMEOUTS;
  return {
    timeouts: {
      connectMs: boundedInt(t.connectMs, 100, 60_000, d.connectMs),
      firstByteMs: boundedInt(t.firstByteMs, 1_000, 3_600_000, d.firstByteMs),
      idleMs: boundedInt(t.idleMs, 1_000, 3_600_000, d.idleMs),
      totalMs: boundedInt(t.totalMs, 1_000, 7_200_000, d.totalMs),
      nonStreamTotalMs: boundedInt(t.nonStreamTotalMs, 1_000, 7_200_000, d.nonStreamTotalMs),
    },
    maxAttempts: boundedInt(gateway.maxAttempts ?? stored['gateway.maxAttempts'], 1, 10, DEFAULT_MAX_ATTEMPTS),
  };
}

@Injectable()
export class PrismaProxySettingsReader implements ProxySettingsReader {
  private readonly logger = new Logger(PrismaProxySettingsReader.name);
  private readonly cache = new TtlCache<ProxySettings>(10_000);

  constructor(private readonly prisma: PrismaService) {}

  async read(): Promise<ProxySettings> {
    const cached = this.cache.get('settings');
    if (cached) return cached;
    let settings = parseProxySettings(undefined);
    try {
      const row = await this.prisma.systemSettings.findUnique({ where: { key: 'global' } });
      settings = parseProxySettings(isRecord(row?.value) ? row.value.localai : undefined);
    } catch (error) {
      this.logger.warn(`could not read the gateway settings, using the defaults: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
    this.cache.set('settings', settings);
    return settings;
  }
}
