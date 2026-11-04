// The Lab page's thresholds from the `localai` settings namespace (docs/specs/cloud-app.md section 3.10):
// `alerts.driveStaleDays` (30), `alerts.driveCriticalDays` (90) and `agent.latestVersion` (null).
//
// The namespace itself is registered by a later slice (the settings card, CLOUD-12); until it exists the read finds
// nothing and the documented defaults apply, so the Lab page never fails because a setting is missing. A stored value
// that is not a positive integer (or an unreadable version) falls back to its default field by field.
import { Injectable, Logger } from '@nestjs/common';
import { SystemSettingsService } from '@marinoscar/platform-api/settings';
import { z } from 'zod';

import { DEFAULT_DRIVE_CRITICAL_DAYS, DEFAULT_DRIVE_STALE_DAYS, type ArkThresholds } from '../health/ark-colour';

export interface LabSettings {
  thresholds: ArkThresholds;
  agentLatestVersion: string | null;
  /** EAB `nodes.staleHeartbeatSeconds`. */
  staleHeartbeatSeconds: number;
}

export const DEFAULT_STALE_HEARTBEAT_SECONDS = 45;

const Days = z.number().int().positive().max(3650);
const Stored = z.object({
  alerts: z.object({ driveStaleDays: Days.optional().catch(undefined), driveCriticalDays: Days.optional().catch(undefined) }).partial().optional().catch(undefined),
  agent: z.object({ latestVersion: z.string().max(64).nullable().optional().catch(undefined) }).partial().optional().catch(undefined),
});

/** Pure: stored `localai` namespace value (anything) to thresholds. Exported for the unit test. */
export function parseLocalaiSettings(stored: unknown): Pick<LabSettings, 'thresholds' | 'agentLatestVersion'> {
  const parsed = Stored.safeParse(stored ?? {});
  const value = parsed.success ? parsed.data : {};
  const driveStaleDays = value.alerts?.driveStaleDays ?? DEFAULT_DRIVE_STALE_DAYS;
  const driveCriticalDays = value.alerts?.driveCriticalDays ?? DEFAULT_DRIVE_CRITICAL_DAYS;
  return {
    // A critical threshold below the stale one would make "amber" unreachable: keep the pair ordered.
    thresholds: { driveStaleDays, driveCriticalDays: Math.max(driveCriticalDays, driveStaleDays) },
    agentLatestVersion: value.agent?.latestVersion ?? null,
  };
}

@Injectable()
export class LabSettingsService {
  private readonly logger = new Logger(LabSettingsService.name);

  constructor(private readonly systemSettings: SystemSettingsService) {}

  async read(): Promise<LabSettings> {
    const [stored, staleHeartbeatSeconds] = await Promise.all([this.readLocalai(), this.readStaleSeconds()]);
    return { ...parseLocalaiSettings(stored), staleHeartbeatSeconds };
  }

  private async readLocalai(): Promise<unknown> {
    try {
      // The `localai` namespace may not be registered yet; the type system only knows the platform's namespaces.
      return await (this.systemSettings as unknown as { getNamespace(key: string): Promise<unknown> }).getNamespace('localai');
    } catch {
      return undefined;
    }
  }

  private async readStaleSeconds(): Promise<number> {
    try {
      const policy = await this.systemSettings.getNodesPolicy();
      return policy.staleHeartbeatSeconds > 0 ? policy.staleHeartbeatSeconds : DEFAULT_STALE_HEARTBEAT_SECONDS;
    } catch (error) {
      this.logger.warn(`could not read the nodes policy, using ${DEFAULT_STALE_HEARTBEAT_SECONDS} s: ${(error as Error).message}`);
      return DEFAULT_STALE_HEARTBEAT_SECONDS;
    }
  }
}
