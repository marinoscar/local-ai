// The alert thresholds from the `localai` settings namespace (docs/specs/cloud-app.md section 3.10):
// `alerts.driveStaleDays` 30, `alerts.driveCriticalDays` 90, `alerts.nodeStaleMinutes` 10, `alerts.nodeOfflineHours` 24,
// `alerts.catalogBehindDays` 14, `alerts.repeatHours` 24 and `agent.latestVersion` null.
//
// The `localai` namespace itself is registered by the settings issue (GW-02); until then this reads the stored system
// settings row, `value.localai`, exactly as the catalog reader does (`catalog/catalog-settings.ts`): the nested form
// (`{ alerts: { driveStaleDays } }`) and the dotted key (`{ 'alerts.driveStaleDays': ... }`) are both accepted. A stored
// value that is not a positive integer falls back to its default field by field, and the critical drive threshold never
// falls below the stale one (the same rule the Lab card applies). Nothing here can make the evaluator fail.
import { Injectable, Logger } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { LabSettingsService } from '../nodes/lab-settings.service';

export interface AlertSettings {
  driveStaleDays: number;
  driveCriticalDays: number;
  nodeStaleMinutes: number;
  nodeOfflineHours: number;
  catalogBehindDays: number;
  repeatHours: number;
  /** `agent.latestVersion`, or null. */
  agentLatestVersion: string | null;
  /** EAB `nodes.staleHeartbeatSeconds`, which the derived health reads. */
  staleHeartbeatSeconds: number;
}

export const DEFAULT_ALERT_SETTINGS: Omit<AlertSettings, 'staleHeartbeatSeconds'> = {
  driveStaleDays: 30,
  driveCriticalDays: 90,
  nodeStaleMinutes: 10,
  nodeOfflineHours: 24,
  catalogBehindDays: 14,
  repeatHours: 24,
  agentLatestVersion: null,
};

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

function positiveInt(value: unknown, fallback: number, max: number): number {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 && value <= max ? value : fallback;
}

/** `localai.<group>.<key>`: the nested value, else the dotted key. */
function pick(localai: Record<string, unknown>, group: string, key: string): unknown {
  const nested = isRecord(localai[group]) ? (localai[group] as Record<string, unknown>)[key] : undefined;
  return nested ?? localai[`${group}.${key}`];
}

/** Pure: the stored `localai` object (anything) to the thresholds, with defaults. Exported for the unit test. */
export function parseAlertSettings(stored: unknown): Omit<AlertSettings, 'staleHeartbeatSeconds'> {
  const localai = isRecord(stored) ? stored : {};
  const driveStaleDays = positiveInt(pick(localai, 'alerts', 'driveStaleDays'), DEFAULT_ALERT_SETTINGS.driveStaleDays, 3650);
  const driveCriticalDays = positiveInt(pick(localai, 'alerts', 'driveCriticalDays'), DEFAULT_ALERT_SETTINGS.driveCriticalDays, 3650);
  const latest = pick(localai, 'agent', 'latestVersion');
  return {
    driveStaleDays,
    driveCriticalDays: Math.max(driveCriticalDays, driveStaleDays),
    nodeStaleMinutes: positiveInt(pick(localai, 'alerts', 'nodeStaleMinutes'), DEFAULT_ALERT_SETTINGS.nodeStaleMinutes, 10_080),
    nodeOfflineHours: positiveInt(pick(localai, 'alerts', 'nodeOfflineHours'), DEFAULT_ALERT_SETTINGS.nodeOfflineHours, 8760),
    catalogBehindDays: positiveInt(pick(localai, 'alerts', 'catalogBehindDays'), DEFAULT_ALERT_SETTINGS.catalogBehindDays, 3650),
    repeatHours: positiveInt(pick(localai, 'alerts', 'repeatHours'), DEFAULT_ALERT_SETTINGS.repeatHours, 8760),
    agentLatestVersion: typeof latest === 'string' && latest.length > 0 && latest.length <= 64 ? latest : null,
  };
}

@Injectable()
export class AlertSettingsReader {
  private readonly logger = new Logger(AlertSettingsReader.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly lab: LabSettingsService,
  ) {}

  async read(): Promise<AlertSettings> {
    const [stored, lab] = await Promise.all([this.readStored(), this.lab.read()]);
    const parsed = parseAlertSettings(stored);
    return { ...parsed, agentLatestVersion: parsed.agentLatestVersion ?? lab.agentLatestVersion, staleHeartbeatSeconds: lab.staleHeartbeatSeconds };
  }

  private async readStored(): Promise<unknown> {
    try {
      const row = await this.prisma.systemSettings.findUnique({ where: { key: 'global' } });
      return isRecord(row?.value) ? row.value.localai : undefined;
    } catch (error) {
      this.logger.warn(`could not read the localai settings, using the defaults: ${error instanceof Error ? error.name : 'unknown error'}`);
      return undefined;
    }
  }
}
